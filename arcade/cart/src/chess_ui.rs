//! CHESS cabinet: hotseat, versus the machine, or online over the room
//! relay. Click a piece, click a destination; promotion pops a picker; R-R
//! resigns. Fischer Random deals a scrambled back rank; the POSITION
//! EDITOR builds puzzles and absurd starts for the community shelf. Rules
//! and the sparring bot live in arcade-logic.

use arcade_logic::chess::{file, rank, Board, Color as Side, DrawKind, Move, Piece, Status};
use arcade_logic::chess_bot;
use bevy::prelude::*;
use serde::{Deserialize, Serialize};

use crate::retro::{text, AMBER, CYAN, DIM, GREEN, MAGENTA, WHITE};
use crate::rng::Rng;
use crate::shell::{net_send, sfx, stat};
use crate::{CabinetConfig, FinalScore, GameTag, NetIn, NetMode, Phase};

pub const BLURB: &[&str] = &[
    "HOTSEAT, VS THE MACHINE, OR ONLINE.",
    "CLICK A PIECE, CLICK A SQUARE. R TWICE RESIGNS.",
    "FISCHER RANDOM DEALS. THE EDITOR BUILDS PUZZLES.",
];

// ---- position documents (puzzles, absurd starts, Fischer deals) ----

/// A shareable position: 64 chars a1..h8 (KQRBNP white, kqrbnp black,
/// '.' empty) plus whose move it is. Castling is not encoded — custom
/// positions play without castling rights, the honest house rule.
#[derive(Serialize, Deserialize, Clone)]
struct ChessDoc {
    v: u32,
    #[serde(default)]
    name: String,
    board: String,
    turn: String, // "w" | "b"
}

fn piece_char(side: Side, p: Piece) -> char {
    let c = match p {
        Piece::Pawn => 'p',
        Piece::Knight => 'n',
        Piece::Bishop => 'b',
        Piece::Rook => 'r',
        Piece::Queen => 'q',
        Piece::King => 'k',
    };
    if side == Side::White {
        c.to_ascii_uppercase()
    } else {
        c
    }
}

fn char_piece(ch: char) -> Option<(Side, Piece)> {
    let side = if ch.is_ascii_uppercase() { Side::White } else { Side::Black };
    let p = match ch.to_ascii_lowercase() {
        'p' => Piece::Pawn,
        'n' => Piece::Knight,
        'b' => Piece::Bishop,
        'r' => Piece::Rook,
        'q' => Piece::Queen,
        'k' => Piece::King,
        _ => return None,
    };
    Some((side, p))
}

fn cells_to_string(cells: &[Option<(Side, Piece)>; 64]) -> String {
    cells.iter().map(|c| c.map(|(s, p)| piece_char(s, p)).unwrap_or('.')).collect()
}

fn doc_to_board(doc: &ChessDoc) -> Option<Board> {
    let chars: Vec<char> = doc.board.chars().collect();
    if chars.len() != 64 {
        return None;
    }
    let mut cells: [Option<(Side, Piece)>; 64] = [None; 64];
    for (i, &ch) in chars.iter().enumerate() {
        if ch != '.' {
            cells[i] = Some(char_piece(ch)?);
        }
    }
    let turn = if doc.turn == "b" { Side::Black } else { Side::White };
    let board = Board { cells, turn, castle: [false; 4], ep: None, halfmove: 0 };
    validate_position(&board).is_none().then_some(board)
}

/// Position sanity: one king per side, no pawns on the back ranks, and the
/// side NOT to move isn't already in check (their king would just be taken).
fn validate_position(board: &Board) -> Option<String> {
    let count = |side, piece| {
        board.cells.iter().flatten().filter(|&&(s, p)| s == side && p == piece).count()
    };
    if count(Side::White, Piece::King) != 1 || count(Side::Black, Piece::King) != 1 {
        return Some("EACH SIDE NEEDS EXACTLY ONE KING".into());
    }
    for (sq, cell) in board.cells.iter().enumerate() {
        if let Some((_, Piece::Pawn)) = cell {
            if rank(sq) == 0 || rank(sq) == 7 {
                return Some("NO PAWNS ON THE BACK RANKS".into());
            }
        }
    }
    if board.in_check(board.turn.other()) {
        return Some("THE SIDE NOT TO MOVE IS IN CHECK".into());
    }
    None
}

/// A Fischer Random (chess960-style) deal: bishops on opposite colors, the
/// king somewhere between the rooks, mirrored for both sides. Castling is
/// off in this cabinet's Fischer games — the honest house rule, stated on
/// the HUD, rather than a half-right implementation of 960 castling.
fn fischer_cells(seed: u64) -> [Option<(Side, Piece)>; 64] {
    let mut s = seed | 1;
    let mut next = move |n: usize| -> usize {
        s ^= s << 13;
        s ^= s >> 7;
        s ^= s << 17;
        (s % n as u64) as usize
    };
    let mut back: [Option<Piece>; 8] = [None; 8];
    // Bishops: one on a light file, one on a dark file.
    back[[0, 2, 4, 6][next(4)]] = Some(Piece::Bishop);
    back[[1, 3, 5, 7][next(4)]] = Some(Piece::Bishop);
    // Queen on any remaining file.
    let mut free: Vec<usize> = (0..8).filter(|&i| back[i].is_none()).collect();
    back[free.remove(next(6))] = Some(Piece::Queen);
    // Two knights on the remaining five.
    let k1 = free.remove(next(5));
    back[k1] = Some(Piece::Knight);
    let k2 = free.remove(next(4));
    back[k2] = Some(Piece::Knight);
    // What's left is rook, king, rook — king in the middle by construction.
    free = (0..8).filter(|&i| back[i].is_none()).collect();
    back[free[0]] = Some(Piece::Rook);
    back[free[1]] = Some(Piece::King);
    back[free[2]] = Some(Piece::Rook);

    let mut cells: [Option<(Side, Piece)>; 64] = [None; 64];
    for f in 0..8 {
        let p = back[f].expect("all eight files filled");
        cells[f] = Some((Side::White, p));
        cells[8 + f] = Some((Side::White, Piece::Pawn));
        cells[48 + f] = Some((Side::Black, Piece::Pawn));
        cells[56 + f] = Some((Side::Black, p));
    }
    cells
}

/// What the page asked this round to start from.
enum StartSpec {
    Standard,
    Fischer,
    Doc(ChessDoc),
    Blank, // editor: an empty board with just the kings
}

fn page_start() -> StartSpec {
    #[derive(Deserialize)]
    struct FischerRef {
        fischer: bool,
    }
    #[derive(Deserialize)]
    struct BlankRef {
        blank: bool,
    }
    #[cfg(target_arch = "wasm32")]
    let raw = js_sys::Reflect::get(&js_sys::global(), &"__ARCADE_LEVEL".into())
        .ok()
        .and_then(|v| v.as_string());
    #[cfg(not(target_arch = "wasm32"))]
    let raw: Option<String> = None;
    if let Some(raw) = raw {
        if serde_json::from_str::<FischerRef>(&raw).map(|f| f.fischer).unwrap_or(false) {
            return StartSpec::Fischer;
        }
        if serde_json::from_str::<BlankRef>(&raw).map(|b| b.blank).unwrap_or(false) {
            return StartSpec::Blank;
        }
        if let Ok(doc) = serde_json::from_str::<ChessDoc>(&raw) {
            return StartSpec::Doc(doc);
        }
    }
    StartSpec::Standard
}

const CELL: f32 = 64.0;
const X0: f32 = -320.0; // left edge of a1 (unflipped)
const Y0: f32 = -256.0; // bottom edge of a1 (unflipped)

/// Board orientation: online Black sees the board from their own side.
fn oriented(sq: usize, flip: bool) -> usize {
    if flip {
        63 - sq
    } else {
        sq
    }
}

fn square_center(sq: usize, flip: bool) -> Vec2 {
    let v = oriented(sq, flip);
    Vec2::new(
        X0 + file(v) as f32 * CELL + CELL / 2.0,
        Y0 + rank(v) as f32 * CELL + CELL / 2.0,
    )
}

fn square_at(world: Vec2, flip: bool) -> Option<usize> {
    let fx = ((world.x - X0) / CELL).floor();
    let fy = ((world.y - Y0) / CELL).floor();
    if (0.0..8.0).contains(&fx) && (0.0..8.0).contains(&fy) {
        Some(oriented(fy as usize * 8 + fx as usize, flip))
    } else {
        None
    }
}

/// Relayed messages: seat 0 is White, seat 1 is Black.
#[derive(Serialize, Deserialize)]
struct WireMove {
    t: String, // "mv" | "rs" (resign) | "st8" (custom start, see WireSetup)
    #[serde(default)]
    from: usize,
    #[serde(default)]
    to: usize,
    #[serde(default)]
    promo: Option<String>,
}

/// Host → guest: play from this position (Fischer deal or community puzzle).
#[derive(Serialize, Deserialize)]
struct WireSetup {
    t: String, // "st8"
    board: String,
    turn: String,
}

fn promo_str(p: Option<Piece>) -> Option<String> {
    p.map(|p| match p {
        Piece::Queen => "q".into(),
        Piece::Rook => "r".into(),
        Piece::Bishop => "b".into(),
        _ => "n".into(),
    })
}

fn promo_piece(s: &Option<String>) -> Option<Piece> {
    s.as_deref().map(|s| match s {
        "q" => Piece::Queen,
        "r" => Piece::Rook,
        "b" => Piece::Bishop,
        _ => Piece::Knight,
    })
}

fn seat_side(seat: u8) -> Side {
    if seat == 0 {
        Side::White
    } else {
        Side::Black
    }
}

#[derive(Resource)]
struct Table {
    board: Board,
    selected: Option<usize>,
    legal: Vec<Move>,
    promo: Option<(usize, usize)>, // pending promotion from→to
    over_wait: Option<Timer>,
    final_score: u32,
    dirty: bool,
    flip: bool,
    last_move: Option<(usize, usize)>,
    /// Position fingerprints since the start, for threefold repetition.
    hashes: Vec<u64>,
    /// First R press arms resignation for a moment; second confirms.
    resign_arm: Option<Timer>,
    /// The machine plays `bot_side` in local single-player rounds — Black
    /// from a standard start, whichever side the human ISN'T for puzzles.
    bot: bool,
    bot_side: Side,
    bot_think: Timer,
    /// In-flight incremental search: a few root moves per frame, no hitch.
    search: Option<BotSearch>,
    /// Two humans, one keyboard: every result pays a flat token so nobody
    /// farms the leaderboard by playing both chairs.
    hotseat: bool,
    /// Online per-turn clock: stall past it and you forfeit.
    turn_clock: Timer,
    result: String,
}

struct BotSearch {
    moves: Vec<Move>,
    idx: usize,
    best: Option<Move>,
    best_score: i32,
    seed: u64,
}

/// Online per-turn allowance, generous by design: this is a stall-breaker,
/// not a blitz clock.
const TURN_SECS: f32 = 150.0;

/// The position editor: place pieces, pick the side to move, test against
/// the machine, save to the community shelf. Survives test rounds.
#[derive(Resource)]
struct PosEditor {
    active: bool,
    testing: bool,
    piece: Piece,
    side: Side,
    /// Snapshot of the canvas, restored when a test-play ends.
    setup_cells: [Option<(Side, Piece)>; 64],
    setup_turn: Side,
    warning: Option<(String, Timer)>,
}

impl PosEditor {
    fn idle() -> PosEditor {
        PosEditor {
            active: false,
            testing: false,
            piece: Piece::Pawn,
            side: Side::White,
            setup_cells: [None; 64],
            setup_turn: Side::White,
            warning: None,
        }
    }
}

/// A board holding only the two kings — the editor's blank canvas.
fn blank_cells() -> [Option<(Side, Piece)>; 64] {
    let mut cells: [Option<(Side, Piece)>; 64] = [None; 64];
    cells[4] = Some((Side::White, Piece::King)); // e1
    cells[60] = Some((Side::Black, Piece::King)); // e8
    cells
}

#[derive(Component)]
struct PieceSprite;

#[derive(Component)]
struct Highlight;

#[derive(Component)]
struct PromoButton(Piece);

#[derive(Component)]
struct Hud;

pub struct ChessPlugin;

impl Plugin for ChessPlugin {
    fn build(&self, app: &mut App) {
        app.add_systems(OnEnter(Phase::Playing), setup)
            .add_systems(
                Update,
                poll_editor_start.run_if(in_state(Phase::Attract).or(in_state(Phase::GameOver))),
            )
            .add_systems(
                Update,
                (net_apply, editor_update, bot_move, clicks, turn_clock_run, endgame)
                    .chain()
                    .run_if(in_state(Phase::Playing))
                    .run_if(crate::unpaused),
            );
    }
}

fn poll_editor_start(
    mut next: ResMut<NextState<Phase>>,
    mut net: ResMut<NetMode>,
    mut cfg: ResMut<CabinetConfig>,
) {
    if crate::shell::take_editor_start() {
        net.0 = None;
        cfg.players = 2;
        cfg.humans = 1;
        crate::shell::mark_editor_pending();
        next.set(Phase::Playing);
    }
}

fn setup(
    mut commands: Commands,
    config: Res<CabinetConfig>,
    net: Res<NetMode>,
    mut rng: ResMut<Rng>,
    mut meshes: ResMut<Assets<Mesh>>,
    mut materials: ResMut<Assets<ColorMaterial>>,
    existing_editor: Option<ResMut<PosEditor>>,
) {
    commands.insert_resource(ChessFx::build(&mut meshes, &mut materials));
    let editor_mode = crate::shell::take_editor_pending();
    let flip = matches!(&net.0, Some(cfg) if cfg.seat == 1);
    let spec = page_start();

    // The starting position. Guests take whatever arrives over the wire
    // ("st8"); the host generates and relays. Custom docs and Fischer deals
    // carry no castling rights (stated on the HUD).
    let is_guest = matches!(&net.0, Some(cfg) if !cfg.is_host());
    let (board, fischer) = if editor_mode {
        let cells = match &spec {
            StartSpec::Doc(doc) => doc_to_board(doc).map(|b| b.cells).unwrap_or_else(blank_cells),
            _ => blank_cells(),
        };
        (Board { cells, turn: Side::White, castle: [false; 4], ep: None, halfmove: 0 }, false)
    } else if is_guest {
        (Board::start(), false) // replaced by "st8" if the host customized
    } else {
        match &spec {
            StartSpec::Fischer => {
                let cells = fischer_cells(rng.next_u64());
                stat("fischer_deals", 1);
                (Board { cells, turn: Side::White, castle: [false; 4], ep: None, halfmove: 0 }, true)
            }
            StartSpec::Doc(doc) => match doc_to_board(doc) {
                Some(b) => (b, false),
                None => (Board::start(), false),
            },
            _ => (Board::start(), false),
        }
    };
    // Host with a non-standard start: relay it before anyone moves.
    if let Some(cfg) = &net.0 {
        if cfg.is_host() && !editor_mode {
            let is_custom = !matches!(&spec, StartSpec::Standard | StartSpec::Blank);
            if is_custom {
                if let Ok(w) = serde_json::to_string(&WireSetup {
                    t: "st8".into(),
                    board: cells_to_string(&board.cells),
                    turn: if board.turn == Side::White { "w".into() } else { "b".into() },
                }) {
                    net_send(&w);
                }
            }
        }
    }
    // Vs the machine, the human plays the side to move at the start (the
    // puzzle experience); from a standard start that is White as always.
    let bot_side = board.turn.other();

    // The editor survives test rounds — same pattern as INTERNS.
    match (existing_editor, editor_mode) {
        (Some(mut e), true) => {
            e.active = true;
            e.testing = false;
            e.setup_cells = board.cells;
            e.setup_turn = board.turn;
        }
        (Some(mut e), false) => {
            e.active = false;
            e.testing = false;
        }
        (None, editing) => {
            let mut e = PosEditor::idle();
            if editing {
                e.active = true;
                e.setup_cells = board.cells;
                e.setup_turn = board.turn;
            }
            commands.insert_resource(e);
        }
    }

    let first_hash = board.position_hash();
    commands.insert_resource(Table {
        board,
        selected: None,
        legal: Vec::new(),
        promo: None,
        over_wait: None,
        final_score: 0,
        dirty: true,
        flip,
        last_move: None,
        hashes: vec![first_hash],
        resign_arm: None,
        bot: !editor_mode && net.0.is_none() && config.humans == 1,
        bot_side,
        bot_think: Timer::from_seconds(0.55, TimerMode::Once),
        search: None,
        hotseat: net.0.is_none() && config.humans >= 2,
        turn_clock: Timer::from_seconds(TURN_SECS, TimerMode::Once),
        result: String::new(),
    });
    if fischer {
        let e = text(
            &mut commands,
            "FISCHER RANDOM\nNO CASTLING\n(HOUSE RULE)",
            16.0,
            DIM,
            Vec3::new(275.0, -60.0, 2.0),
        );
        commands.entity(e).insert(GameTag);
    }
    // The board: light/dark squares in muted CRT blues.
    for sq in 0..64 {
        let dark = (file(sq) + rank(sq)) % 2 == 0;
        let color = if dark {
            Color::srgb(0.10, 0.13, 0.25)
        } else {
            Color::srgb(0.17, 0.22, 0.38)
        };
        let p = square_center(sq, flip);
        commands.spawn((
            Sprite { color, custom_size: Some(Vec2::splat(CELL - 1.0)), ..default() },
            Transform::from_xyz(p.x, p.y, 1.0),
            GameTag,
        ));
    }
    let hud = text(&mut commands, "", 24.0, WHITE, Vec3::new(275.0, 120.0, 2.0));
    commands.entity(hud).insert((Hud, GameTag));
    let help = text(
        &mut commands,
        "WHITE: GREEN\nBLACK: MAGENTA\n\nR+R RESIGN",
        18.0,
        DIM,
        Vec3::new(275.0, -160.0, 2.0),
    );
    commands.entity(help).insert(GameTag);
}

/// Shared mesh handles for the drawn piece silhouettes. The cart font is
/// ASCII-only, so like the card pips the pieces are built from primitives:
/// circles for heads and finials, triangles for tapered bodies, sprites for
/// bases, walls and crowns.
#[derive(Resource)]
struct ChessFx {
    head: Handle<Mesh>,
    ball: Handle<Mesh>,
    pawn_body: Handle<Mesh>,
    bishop_body: Handle<Mesh>,
    royal_body: Handle<Mesh>,
    spike: Handle<Mesh>,
    /// White, black, promo-picker amber.
    mats: [Handle<ColorMaterial>; 3],
}

impl ChessFx {
    fn build(meshes: &mut Assets<Mesh>, materials: &mut Assets<ColorMaterial>) -> Self {
        let tri = |meshes: &mut Assets<Mesh>, a: (f32, f32), b: (f32, f32), c: (f32, f32)| {
            meshes.add(Triangle2d::new(
                Vec2::new(a.0, a.1),
                Vec2::new(b.0, b.1),
                Vec2::new(c.0, c.1),
            ))
        };
        ChessFx {
            head: meshes.add(Circle::new(6.0)),
            ball: meshes.add(Circle::new(2.6)),
            pawn_body: tri(meshes, (-8.0, -8.0), (8.0, -8.0), (0.0, 10.0)),
            bishop_body: tri(meshes, (-7.5, -10.5), (7.5, -10.5), (0.0, 10.5)),
            royal_body: tri(meshes, (-9.5, -9.5), (9.5, -9.5), (0.0, 9.5)),
            spike: tri(meshes, (-3.0, -4.5), (3.0, -4.5), (0.0, 4.5)),
            mats: [
                materials.add(GREEN),
                materials.add(MAGENTA),
                materials.add(AMBER),
            ],
        }
    }
}

/// One staunton-ish silhouette, ~40px tall, centered in the piece tile.
/// `mat` indexes ChessFx::mats and must agree with `color` (sprites tint
/// directly, meshes go through the material).
fn spawn_piece(kid: &mut ChildSpawnerCommands, fx: &ChessFx, mat: usize, color: Color, piece: Piece) {
    let material = fx.mats[mat].clone();
    // Parts get stacking z offsets so overlapping sprites never shimmer.
    let layer = std::cell::Cell::new(0.0f32);
    let bump = || {
        layer.set(layer.get() + 0.01);
        0.1 + layer.get()
    };
    let rect = |kid: &mut ChildSpawnerCommands, x: f32, y: f32, w: f32, h: f32, rot: f32| {
        kid.spawn((
            Sprite { color, custom_size: Some(Vec2::new(w, h)), ..default() },
            Transform::from_xyz(x, y, bump()).with_rotation(Quat::from_rotation_z(rot)),
        ));
    };
    let blob = |kid: &mut ChildSpawnerCommands, mesh: &Handle<Mesh>, x: f32, y: f32| {
        kid.spawn((
            Mesh2d(mesh.clone()),
            MeshMaterial2d(material.clone()),
            Transform::from_xyz(x, y, bump()),
        ));
    };
    // Every piece stands on the same plinth.
    rect(kid, 0.0, -15.0, 24.0, 4.5, 0.0);
    match piece {
        Piece::Pawn => {
            blob(kid, &fx.pawn_body, 0.0, -4.0);
            blob(kid, &fx.head, 0.0, 8.0);
        }
        Piece::Rook => {
            rect(kid, 0.0, -3.0, 16.0, 18.0, 0.0);
            rect(kid, 0.0, 8.0, 22.0, 4.0, 0.0);
            for x in [-8.0, 0.0, 8.0] {
                rect(kid, x, 13.5, 5.5, 6.0, 0.0);
            }
        }
        Piece::Knight => {
            // Horse profile facing left: chest, leaning neck, head, muzzle, ear.
            rect(kid, 3.5, -6.0, 13.0, 14.0, 0.0);
            rect(kid, 0.0, 2.0, 11.0, 18.0, 0.45);
            rect(kid, -4.5, 10.0, 14.0, 8.5, 0.0);
            rect(kid, -11.5, 8.5, 7.0, 5.5, 0.0);
            blob(kid, &fx.spike, 1.0, 17.0);
        }
        Piece::Bishop => {
            blob(kid, &fx.bishop_body, 0.0, -1.5);
            blob(kid, &fx.head, 0.0, 8.0);
            blob(kid, &fx.ball, 0.0, 15.5);
        }
        Piece::Queen => {
            blob(kid, &fx.royal_body, 0.0, -2.5);
            rect(kid, 0.0, 6.5, 19.0, 3.5, 0.0);
            for (x, y) in [(-6.5, 11.5), (0.0, 12.5), (6.5, 11.5)] {
                blob(kid, &fx.spike, x, y);
            }
            for (x, y) in [(-6.5, 16.5), (0.0, 17.5), (6.5, 16.5)] {
                blob(kid, &fx.ball, x, y);
            }
        }
        Piece::King => {
            blob(kid, &fx.royal_body, 0.0, -3.0);
            rect(kid, 0.0, 5.5, 19.0, 3.5, 0.0);
            rect(kid, 0.0, 12.5, 3.5, 10.0, 0.0);
            rect(kid, 0.0, 13.0, 8.5, 3.5, 0.0);
        }
    }
}

fn side_color(side: Side) -> Color {
    match side {
        Side::White => GREEN,
        Side::Black => MAGENTA,
    }
}

/// Applies a move to the table: board, bookkeeping, feedback, end check.
/// `mine` marks moves made from THIS keyboard (hotseat counts both chairs —
/// you did physically play them), feeding the service record.
fn commit_move(table: &mut Table, m: Move, mine: bool) {
    let capture = table.board.cells[m.to].is_some()
        || (table.board.cells[m.from].map(|(_, p)| p) == Some(Piece::Pawn)
            && Some(m.to) == table.board.ep);
    if mine {
        stat("moves_played", 1);
        if capture {
            stat("captures_made", 1);
        }
        if m.promo.is_some() {
            stat("pawns_promoted", 1);
        }
        if m.promo == Some(Piece::Knight) {
            stat("knight_promotions", 1);
        }
    } else if capture {
        stat("pieces_lost", 1);
    }
    table.board.apply(m);
    if mine && table.board.in_check(table.board.turn) {
        stat("checks_given", 1);
    }
    table.last_move = Some((m.from, m.to));
    table.hashes.push(table.board.position_hash());
    table.selected = None;
    table.legal = table.board.legal_moves();
    table.dirty = true;
    table.turn_clock.reset();
    sfx(if capture { "capture" } else { "place" });
    check_end(table);
}

/// Full repaint: pieces, last-move markers, highlights, promo picker.
fn repaint(
    commands: &mut Commands,
    table: &Table,
    fx: &ChessFx,
    pieces: &Query<Entity, With<PieceSprite>>,
    highlights: &Query<Entity, With<Highlight>>,
) {
    for e in pieces.iter().chain(highlights.iter()) {
        commands.entity(e).despawn();
    }
    if let Some((from, to)) = table.last_move {
        for sq in [from, to] {
            let p = square_center(sq, table.flip);
            commands.spawn((
                Sprite {
                    color: CYAN.with_alpha(0.16),
                    custom_size: Some(Vec2::splat(CELL - 2.0)),
                    ..default()
                },
                Transform::from_xyz(p.x, p.y, 1.5),
                Highlight,
                GameTag,
            ));
        }
    }
    for sq in 0..64 {
        if let Some((side, piece)) = table.board.cells[sq] {
            let p = square_center(sq, table.flip);
            commands
                .spawn((
                    Sprite {
                        color: Color::srgb(0.04, 0.05, 0.08),
                        custom_size: Some(Vec2::splat(CELL - 16.0)),
                        ..default()
                    },
                    Transform::from_xyz(p.x, p.y, 2.0),
                    PieceSprite,
                    GameTag,
                ))
                .with_children(|kid| {
                    let mat = if side == Side::White { 0 } else { 1 };
                    spawn_piece(kid, fx, mat, side_color(side), piece);
                });
        }
    }
    if let Some(sel) = table.selected {
        let p = square_center(sel, table.flip);
        commands.spawn((
            Sprite { color: Color::srgba(1.0, 0.82, 0.4, 0.25), custom_size: Some(Vec2::splat(CELL - 2.0)), ..default() },
            Transform::from_xyz(p.x, p.y, 3.0),
            Highlight,
            GameTag,
        ));
        for m in table.legal.iter().filter(|m| m.from == sel) {
            let p = square_center(m.to, table.flip);
            commands.spawn((
                Sprite { color: Color::srgba(1.0, 0.82, 0.4, 0.55), custom_size: Some(Vec2::splat(12.0)), ..default() },
                Transform::from_xyz(p.x, p.y, 3.0),
                Highlight,
                GameTag,
            ));
        }
    }
    if table.promo.is_some() {
        commands.spawn((
            Sprite { color: Color::srgba(0.0, 0.0, 0.0, 0.85), custom_size: Some(Vec2::new(120.0, 300.0)), ..default() },
            Transform::from_xyz(275.0, 0.0, 10.0),
            Highlight,
            GameTag,
        ));
        for (i, piece) in [Piece::Queen, Piece::Rook, Piece::Bishop, Piece::Knight].iter().enumerate() {
            let y = 105.0 - i as f32 * 70.0;
            commands
                .spawn((
                    Sprite { color: Color::srgb(0.15, 0.2, 0.35), custom_size: Some(Vec2::splat(56.0)), ..default() },
                    Transform::from_xyz(275.0, y, 11.0),
                    PromoButton(*piece),
                    Highlight,
                    GameTag,
                ))
                .with_children(|kid| {
                    spawn_piece(kid, fx, 2, AMBER, *piece);
                });
        }
    }
}

/// The machine's turn: think briefly, then search TWO root moves per frame —
/// depth three never stalls a frame, the CRT keeps flickering.
fn bot_move(
    time: Res<Time>,
    editor: Res<PosEditor>,
    mut table: ResMut<Table>,
    mut rng: ResMut<Rng>,
    net: Res<NetMode>,
) {
    if editor.active || !table.bot || net.0.is_some() || table.over_wait.is_some() || table.promo.is_some() {
        return;
    }
    if table.board.turn != table.bot_side {
        table.bot_think.reset();
        table.search = None;
        return;
    }
    if !table.bot_think.tick(time.delta()).finished() {
        return;
    }
    if table.search.is_none() {
        let moves = chess_bot::root_moves(&table.board);
        if moves.is_empty() {
            return; // check_end already handled mate/stalemate
        }
        table.search = Some(BotSearch {
            moves,
            idx: 0,
            best: None,
            best_score: i32::MIN,
            seed: rng.next_u64(),
        });
    }
    let mut search = table.search.take().expect("initialized above");
    let seen = table.hashes.clone();
    for _ in 0..2 {
        if search.idx >= search.moves.len() {
            break;
        }
        let m = search.moves[search.idx];
        let score = chess_bot::score_root_move(&table.board, m, &seen)
            + chess_bot::jitter(search.seed, search.idx);
        if search.best.is_none() || score > search.best_score {
            search.best_score = score;
            search.best = Some(m);
        }
        search.idx += 1;
    }
    if search.idx >= search.moves.len() {
        if let Some(m) = search.best {
            commit_move(&mut table, m, false);
        }
        table.search = None;
    } else {
        table.search = Some(search);
    }
}

// ---- the position editor ----

fn piece_name(p: Piece) -> &'static str {
    match p {
        Piece::Pawn => "PAWN",
        Piece::Knight => "KNIGHT",
        Piece::Bishop => "BISHOP",
        Piece::Rook => "ROOK",
        Piece::Queen => "QUEEN",
        Piece::King => "KING",
    }
}

fn side_name(s: Side) -> &'static str {
    if s == Side::White {
        "WHITE"
    } else {
        "BLACK"
    }
}

/// Puts the canvas back exactly as it was before a test-play.
fn return_to_editor(editor: &mut PosEditor, table: &mut Table) {
    editor.testing = false;
    editor.active = true;
    table.board = Board {
        cells: editor.setup_cells,
        turn: editor.setup_turn,
        castle: [false; 4],
        ep: None,
        halfmove: 0,
    };
    table.hashes = vec![table.board.position_hash()];
    table.legal.clear();
    table.selected = None;
    table.promo = None;
    table.over_wait = None;
    table.result.clear();
    table.last_move = None;
    table.resign_arm = None;
    table.bot = false;
    table.search = None;
    table.dirty = true;
}

#[allow(clippy::too_many_arguments)]
fn editor_update(
    buttons: Res<ButtonInput<MouseButton>>,
    keys: Res<ButtonInput<KeyCode>>,
    time: Res<Time>,
    windows: Query<&Window>,
    cameras: Query<(&Camera, &GlobalTransform)>,
    mut editor: ResMut<PosEditor>,
    mut table: ResMut<Table>,
) {
    if !editor.active {
        // X bails out of a test-play early, canvas intact.
        if editor.testing && keys.just_pressed(KeyCode::KeyX) {
            return_to_editor(&mut editor, &mut table);
        }
        return;
    }
    if let Some((_, t)) = editor.warning.as_mut() {
        if t.tick(time.delta()).finished() {
            editor.warning = None;
        }
    }

    // Palette.
    for (key, p) in [
        (KeyCode::Digit1, Piece::Pawn),
        (KeyCode::Digit2, Piece::Knight),
        (KeyCode::Digit3, Piece::Bishop),
        (KeyCode::Digit4, Piece::Rook),
        (KeyCode::Digit5, Piece::Queen),
        (KeyCode::Digit6, Piece::King),
    ] {
        if keys.just_pressed(key) {
            editor.piece = p;
            sfx("tick");
        }
    }
    if keys.just_pressed(KeyCode::KeyC) {
        editor.side = editor.side.other();
        sfx("tick");
    }
    if keys.just_pressed(KeyCode::KeyT) {
        table.board.turn = table.board.turn.other();
        table.dirty = true;
        sfx("tick");
    }

    if keys.just_pressed(KeyCode::KeyG) {
        match validate_position(&table.board) {
            Some(w) => {
                editor.warning = Some((w, Timer::from_seconds(2.5, TimerMode::Once)));
                sfx("buzz");
            }
            None => {
                editor.setup_cells = table.board.cells;
                editor.setup_turn = table.board.turn;
                editor.active = false;
                editor.testing = true;
                // Arm the table for a round: the human plays the side to
                // move, the machine defends the other chairs.
                table.hashes = vec![table.board.position_hash()];
                table.legal = table.board.legal_moves();
                table.last_move = None;
                table.over_wait = None;
                table.result.clear();
                table.final_score = 0;
                table.bot = true;
                table.bot_side = table.board.turn.other();
                table.hotseat = false;
                table.bot_think.reset();
                table.search = None;
                table.dirty = true;
                sfx("coin");
                check_end(&mut table); // a stalemate setup ends immediately
            }
        }
        return;
    }
    if keys.just_pressed(KeyCode::KeyS) {
        match validate_position(&table.board) {
            Some(w) => {
                editor.warning = Some((w, Timer::from_seconds(2.5, TimerMode::Once)));
                sfx("buzz");
            }
            None => {
                let doc = ChessDoc {
                    v: 1,
                    name: String::new(),
                    board: cells_to_string(&table.board.cells),
                    turn: if table.board.turn == Side::White { "w".into() } else { "b".into() },
                };
                if let Ok(json) = serde_json::to_string(&doc) {
                    crate::shell::save_level(&json);
                    sfx("clear");
                }
            }
        }
        return;
    }

    // Place / clear pieces.
    let place = buttons.just_pressed(MouseButton::Left);
    let erase = buttons.just_pressed(MouseButton::Right);
    if !place && !erase {
        return;
    }
    let Ok(window) = windows.single() else { return };
    let Ok((camera, cam_tf)) = cameras.single() else { return };
    let Some(world) = crate::retro::cursor_world(window, camera, cam_tf) else { return };
    let Some(sq) = square_at(world, table.flip) else { return };
    if place {
        table.board.cells[sq] = Some((editor.side, editor.piece));
    } else {
        table.board.cells[sq] = None;
    }
    table.dirty = true;
    sfx("place");
}

#[allow(clippy::too_many_arguments)]
fn clicks(
    buttons: Res<ButtonInput<MouseButton>>,
    keys: Res<ButtonInput<KeyCode>>,
    time: Res<Time>,
    windows: Query<&Window>,
    cameras: Query<(&Camera, &GlobalTransform)>,
    mut commands: Commands,
    mut table: ResMut<Table>,
    net: Res<NetMode>,
    editor: Res<PosEditor>,
    fx: Res<ChessFx>,
    pieces: Query<Entity, With<PieceSprite>>,
    highlights: Query<Entity, With<Highlight>>,
    promo_buttons: Query<(&PromoButton, &Transform)>,
    mut hud: Query<&mut Text2d, With<Hud>>,
) {
    if table.dirty {
        table.dirty = false;
        repaint(&mut commands, &table, &fx, &pieces, &highlights);
    }
    // The position editor owns input while active; keep its HUD current.
    if editor.active {
        if let Ok(mut t) = hud.single_mut() {
            let warn = editor
                .warning
                .as_ref()
                .map(|(w, _)| format!("\n\n!! {w} !!"))
                .unwrap_or_default();
            let s = format!(
                "EDITOR\n\nBRUSH:\n{} {}\n\n1-6 PIECE\nC COLOR\nT TO MOVE\n({})\nCLICK PLACE\nRCLICK CLEAR\n\nG TEST\nS SAVE{warn}",
                side_name(editor.side),
                piece_name(editor.piece),
                side_name(table.board.turn),
            );
            if t.0 != s {
                t.0 = s;
            }
        }
        return;
    }

    // Resign: R arms, a second R inside two seconds confirms.
    if let Some(t) = table.resign_arm.as_mut() {
        if t.tick(time.delta()).finished() {
            table.resign_arm = None;
        }
    }
    if keys.just_pressed(KeyCode::KeyR) && table.over_wait.is_none() && table.promo.is_none() {
        // Resignation is legal OFF-turn online — a stalled opponent must
        // never be able to hold your seat hostage.
        let its_my_input = match &net.0 {
            Some(_) => true,
            None => !(table.bot && table.board.turn == table.bot_side),
        };
        if its_my_input {
            if table.resign_arm.is_some() {
                table.resign_arm = None;
                if net.0.is_some() {
                    if let Ok(w) = serde_json::to_string(&WireMove {
                        t: "rs".into(),
                        from: 0,
                        to: 0,
                        promo: None,
                    }) {
                        net_send(&w);
                    }
                }
                let quitter = match &net.0 {
                    Some(cfg) => seat_side(cfg.seat),
                    None => table.board.turn,
                };
                table.result = format!(
                    "{} RESIGNS\n{} WINS",
                    if quitter == Side::White { "WHITE" } else { "BLACK" },
                    if quitter == Side::White { "BLACK" } else { "WHITE" }
                );
                // The credited account resigned (online / vs machine) or sat
                // both chairs (hotseat): a token payout either way.
                table.final_score = 100;
                stat("resignations", 1);
                if net.0.is_some() {
                    stat("losses_online", 1);
                }
                table.over_wait = Some(Timer::from_seconds(2.5, TimerMode::Once));
            } else {
                table.resign_arm = Some(Timer::from_seconds(2.0, TimerMode::Once));
            }
        }
    }

    // HUD refresh is cheap; do it every frame.
    if let Ok(mut t) = hud.single_mut() {
        let s = if table.over_wait.is_some() {
            table.result.clone()
        } else {
            let turn = if table.board.turn == Side::White { "WHITE" } else { "BLACK" };
            let check = if table.board.in_check(table.board.turn) { "\nCHECK!" } else { "" };
            let whose = match (&net.0, table.bot) {
                (Some(cfg), _) if table.board.turn == seat_side(cfg.seat) => "\n(YOUR MOVE)",
                (Some(_), _) => "\n(THEIR MOVE)",
                (None, true) if table.board.turn == table.bot_side => "\nMACHINE\nTHINKING...",
                _ => "",
            };
            let resign = if table.resign_arm.is_some() { "\n\nR AGAIN\nTO RESIGN" } else { "" };
            // Material tally: the vs-machine game reads at a glance.
            let mat = table.board.material(Side::White);
            let mat_line = match mat.signum() {
                1 => format!("\n\nMATERIAL\nWHITE +{mat}"),
                -1 => format!("\n\nMATERIAL\nBLACK +{}", -mat),
                _ => String::new(),
            };
            // Online stall-breaker countdown, surfaced for the last minute.
            let clock = match &net.0 {
                Some(_) if table.turn_clock.remaining_secs() < 60.0 => {
                    format!("\n\nCLOCK 0:{:02}", table.turn_clock.remaining_secs() as u32)
                }
                _ => String::new(),
            };
            format!("{turn}\nTO MOVE{check}{whose}{mat_line}{clock}{resign}")
        };
        if t.0 != s {
            t.0 = s;
        }
    }

    if table.over_wait.is_some() || !buttons.just_pressed(MouseButton::Left) {
        return;
    }
    let Ok(window) = windows.single() else { return };
    let Ok((camera, cam_tf)) = cameras.single() else { return };
    let Some(world) = crate::retro::cursor_world(window, camera, cam_tf) else { return };

    // Whose mouse is live? Online: only on your turn. Vs machine: only White.
    if let Some(cfg) = &net.0 {
        if table.board.turn != seat_side(cfg.seat) {
            return;
        }
    } else if table.bot && table.board.turn == table.bot_side {
        return;
    }

    // Promotion picker eats every click while open.
    if let Some((from, to)) = table.promo {
        for (btn, tf) in &promo_buttons {
            if (world - tf.translation.truncate()).abs().max_element() < 30.0 {
                let m = Move { from, to, promo: Some(btn.0) };
                if net.0.is_some() {
                    if let Ok(w) = serde_json::to_string(&WireMove {
                        t: "mv".into(),
                        from: m.from,
                        to: m.to,
                        promo: promo_str(m.promo),
                    }) {
                        net_send(&w);
                    }
                }
                table.promo = None;
                commit_move(&mut table, m, true);
                repaint(&mut commands, &table, &fx, &pieces, &highlights);
                return;
            }
        }
        return;
    }

    let flip = table.flip;
    let Some(sq) = square_at(world, flip) else { return };
    if table.legal.is_empty() && table.selected.is_none() {
        table.legal = table.board.legal_moves();
    }
    if let Some(sel) = table.selected {
        let candidates: Vec<Move> =
            table.legal.iter().copied().filter(|m| m.from == sel && m.to == sq).collect();
        if !candidates.is_empty() {
            if candidates[0].promo.is_some() {
                table.promo = Some((sel, sq)); // picker chooses which piece
            } else {
                if net.0.is_some() {
                    if let Ok(w) = serde_json::to_string(&WireMove {
                        t: "mv".into(),
                        from: candidates[0].from,
                        to: candidates[0].to,
                        promo: None,
                    }) {
                        net_send(&w);
                    }
                }
                commit_move(&mut table, candidates[0], true);
            }
            repaint(&mut commands, &table, &fx, &pieces, &highlights);
            return;
        }
        table.selected = None;
    }
    // Select own piece.
    if matches!(table.board.cells[sq], Some((c, _)) if c == table.board.turn) {
        table.selected = Some(sq);
        table.legal = table.board.legal_moves();
        sfx("tick");
    }
    repaint(&mut commands, &table, &fx, &pieces, &highlights);
}

/// Applies relayed opponent moves; handles resignation and departure.
fn net_apply(
    mut events: EventReader<NetIn>,
    mut table: ResMut<Table>,
    net: Res<NetMode>,
    mut commands: Commands,
) {
    let Some(cfg) = &net.0 else {
        events.clear();
        return;
    };
    for ev in events.read() {
        if table.over_wait.is_some() {
            continue;
        }
        if ev.left {
            table.result = "OPPONENT LEFT\nYOU WIN".into();
            table.final_score = 700;
            stat("wins_online", 1);
            table.over_wait = Some(Timer::from_seconds(2.0, TimerMode::Once));
            let e = text(&mut commands, "OPPONENT LEFT - YOU WIN", 30.0, AMBER, Vec3::new(0.0, 0.0, 30.0));
            commands.entity(e).insert(GameTag);
            continue;
        }
        if ev.seat == cfg.seat {
            continue;
        }
        let Ok(wire) = serde_json::from_str::<WireMove>(&ev.data) else { continue };
        match wire.t.as_str() {
            // Custom start (Fischer deal or a community puzzle) from the
            // host, accepted only before anyone has moved.
            "st8" if table.hashes.len() <= 1 => {
                if let Ok(su) = serde_json::from_str::<WireSetup>(&ev.data) {
                    let doc = ChessDoc { v: 1, name: String::new(), board: su.board, turn: su.turn };
                    if let Some(b) = doc_to_board(&doc) {
                        table.board = b;
                        table.hashes = vec![table.board.position_hash()];
                        table.legal = table.board.legal_moves();
                        table.last_move = None;
                        table.selected = None;
                        table.turn_clock.reset();
                        table.dirty = true;
                    }
                }
            }
            "rs" => {
                table.result = "OPPONENT RESIGNS\nYOU WIN".into();
                table.final_score = 700;
                stat("wins_online", 1);
                table.over_wait = Some(Timer::from_seconds(2.5, TimerMode::Once));
            }
            "mv" => {
                let promo = promo_piece(&wire.promo);
                let candidate = if seat_side(ev.seat) == table.board.turn {
                    table
                        .board
                        .legal_moves()
                        .into_iter()
                        .find(|m| m.from == wire.from && m.to == wire.to && m.promo == promo)
                } else {
                    None // an off-turn move from the peer is already a desync
                };
                match candidate {
                    Some(m) => commit_move(&mut table, m, false),
                    None => {
                        // The peers disagree about the position. The sender
                        // has already committed; silently dropping the move
                        // would wedge both clients forever. End it honestly.
                        table.result = "SYNC LOST\nCALLED A DRAW".into();
                        table.final_score = 250;
                        table.over_wait = Some(Timer::from_seconds(2.5, TimerMode::Once));
                        table.dirty = true;
                    }
                }
            }
            _ => {}
        }
    }
}

fn check_end(table: &mut Table) {
    // Threefold repetition: the current position occurred twice before.
    let current = *table.hashes.last().expect("hash pushed on every move");
    let repeats = table.hashes.iter().filter(|&&h| h == current).count();
    let status = if repeats >= 3 {
        Status::Draw(DrawKind::Repetition)
    } else {
        table.board.status()
    };
    match status {
        Status::Ongoing => {}
        Status::Checkmate { winner } => {
            let edge = table.board.material(winner).max(0) as u32;
            // Vs the machine, the purse is only for beating it.
            let human_won = if table.bot { winner != table.bot_side } else { true };
            if table.bot {
                stat(if human_won { "machine_beaten" } else { "beaten_by_machine" }, 1);
            }
            table.final_score = if human_won { 1000 + edge * 20 } else { 150 };
            table.result = format!(
                "CHECKMATE\n{} WINS",
                if winner == Side::White { "WHITE" } else { "BLACK" }
            );
            table.over_wait = Some(Timer::from_seconds(3.0, TimerMode::Once));
        }
        Status::Stalemate => {
            stat("draws", 1);
            table.final_score = 500;
            table.result = "STALEMATE".into();
            table.over_wait = Some(Timer::from_seconds(3.0, TimerMode::Once));
        }
        Status::Draw(kind) => {
            stat("draws", 1);
            table.final_score = 500;
            table.result = match kind {
                DrawKind::FiftyMove => "DRAW\n50-MOVE RULE".into(),
                DrawKind::InsufficientMaterial => "DRAW\nBARE BONES".into(),
                DrawKind::Repetition => "DRAW\nREPETITION".into(),
            };
            table.over_wait = Some(Timer::from_seconds(3.0, TimerMode::Once));
        }
    }
    // Hotseat: one person controls both chairs, so every result — mate,
    // draw, shuffle-to-repetition — pays the same flat token. The graded
    // purse is for rounds the credited account actually had to win.
    if table.hotseat && table.over_wait.is_some() {
        table.final_score = 100;
        stat("hotseat_rounds", 1);
    }
}

/// Online stall-breaker: the side that lets its turn clock lapse forfeits.
/// Both clients run the clock independently; they agree on whose turn it is,
/// so they agree on the verdict (frame-drift of a second is harmless — the
/// result is the same either way).
fn turn_clock_run(time: Res<Time>, net: Res<NetMode>, mut table: ResMut<Table>) {
    let Some(cfg) = &net.0 else { return };
    if table.over_wait.is_some() {
        return;
    }
    table.turn_clock.tick(time.delta());
    if table.turn_clock.finished() {
        let slow = table.board.turn;
        table.result = format!(
            "OUT OF TIME\n{} FORFEITS",
            if slow == Side::White { "WHITE" } else { "BLACK" }
        );
        table.final_score = if seat_side(cfg.seat) == slow { 100 } else { 700 };
        stat(if seat_side(cfg.seat) == slow { "losses_online" } else { "wins_online" }, 1);
        table.over_wait = Some(Timer::from_seconds(2.5, TimerMode::Once));
        table.dirty = true;
    }
}

fn endgame(
    time: Res<Time>,
    net: Res<NetMode>,
    mut editor: ResMut<PosEditor>,
    mut table: ResMut<Table>,
    mut final_score: ResMut<FinalScore>,
    mut next: ResMut<NextState<Phase>>,
) {
    // A finished TEST-PLAY returns to the canvas: no score, nothing lost.
    if editor.testing {
        if let Some(t) = table.over_wait.as_mut() {
            if t.tick(time.delta()).finished() {
                return_to_editor(&mut editor, &mut table);
            }
        }
        return;
    }
    // Online mate payouts: the loser gets a consolation, not the pot.
    if let (Some(cfg), Some(_)) = (&net.0, &table.over_wait) {
        if table.result.starts_with("CHECKMATE") {
            let i_won = table
                .result
                .contains(if cfg.seat == 0 { "WHITE WINS" } else { "BLACK WINS" });
            if !i_won && table.final_score >= 1000 {
                table.final_score = 100;
                stat("losses_online", 1);
            } else if i_won && table.final_score >= 1000 {
                stat("wins_online", 1);
            }
        }
    }
    let score = table.final_score;
    if let Some(t) = table.over_wait.as_mut() {
        if t.tick(time.delta()).finished() {
            final_score.0 = score;
            next.set(Phase::GameOver);
        }
    }
}
