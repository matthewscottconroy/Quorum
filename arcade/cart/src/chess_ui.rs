//! CHESS cabinet: hotseat, versus the machine, or online over the room
//! relay. Click a piece, click a destination; promotion pops a picker; R-R
//! resigns. Rules and the sparring bot live in arcade-logic.

use arcade_logic::chess::{file, rank, Board, Color as Side, DrawKind, Move, Piece, Status};
use arcade_logic::chess_bot;
use bevy::prelude::*;
use serde::{Deserialize, Serialize};

use crate::retro::{text, AMBER, CYAN, DIM, GREEN, MAGENTA, WHITE};
use crate::rng::Rng;
use crate::shell::{net_send, sfx};
use crate::{CabinetConfig, FinalScore, GameTag, NetIn, NetMode, Phase};

pub const BLURB: &[&str] = &[
    "HOTSEAT, VS THE MACHINE, OR ONLINE.",
    "CLICK A PIECE, CLICK A SQUARE.",
    "R TWICE RESIGNS. MATE PAYS 1000.",
];

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
    t: String, // "mv" | "rs" (resign)
    #[serde(default)]
    from: usize,
    #[serde(default)]
    to: usize,
    #[serde(default)]
    promo: Option<String>,
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
    /// The machine plays Black in local single-player rounds.
    bot: bool,
    bot_think: Timer,
    result: String,
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
        app.add_systems(OnEnter(Phase::Playing), setup).add_systems(
            Update,
            (net_apply, bot_move, clicks, endgame)
                .chain()
                .run_if(in_state(Phase::Playing))
                .run_if(crate::unpaused),
        );
    }
}

fn setup(mut commands: Commands, config: Res<CabinetConfig>, net: Res<NetMode>) {
    let flip = matches!(&net.0, Some(cfg) if cfg.seat == 1);
    let board = Board::start();
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
        bot: net.0.is_none() && config.humans == 1,
        bot_think: Timer::from_seconds(0.55, TimerMode::Once),
        result: String::new(),
    });
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

fn glyph(p: Piece) -> &'static str {
    match p {
        Piece::Pawn => "P",
        Piece::Knight => "N",
        Piece::Bishop => "B",
        Piece::Rook => "R",
        Piece::Queen => "Q",
        Piece::King => "K",
    }
}

fn side_color(side: Side) -> Color {
    match side {
        Side::White => GREEN,
        Side::Black => MAGENTA,
    }
}

/// Applies a move to the table: board, bookkeeping, feedback, end check.
fn commit_move(table: &mut Table, m: Move) {
    let capture = table.board.cells[m.to].is_some()
        || (table.board.cells[m.from].map(|(_, p)| p) == Some(Piece::Pawn)
            && Some(m.to) == table.board.ep);
    table.board.apply(m);
    table.last_move = Some((m.from, m.to));
    table.hashes.push(table.board.position_hash());
    table.selected = None;
    table.legal = table.board.legal_moves();
    table.dirty = true;
    sfx(if capture { "capture" } else { "place" });
    check_end(table);
}

/// Full repaint: pieces, last-move markers, highlights, promo picker.
fn repaint(
    commands: &mut Commands,
    table: &Table,
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
                    kid.spawn((
                        Text2d::new(glyph(piece)),
                        TextFont { font_size: 34.0, ..default() },
                        TextColor(side_color(side)),
                        Transform::from_xyz(0.0, 0.0, 0.1),
                    ));
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
                    kid.spawn((
                        Text2d::new(glyph(*piece)),
                        TextFont { font_size: 30.0, ..default() },
                        TextColor(AMBER),
                        Transform::from_xyz(0.0, 0.0, 0.1),
                    ));
                });
        }
    }
}

/// The machine's turn: think briefly, then play like it means it.
fn bot_move(time: Res<Time>, mut table: ResMut<Table>, mut rng: ResMut<Rng>, net: Res<NetMode>) {
    if !table.bot || net.0.is_some() || table.over_wait.is_some() || table.promo.is_some() {
        return;
    }
    if table.board.turn != Side::Black {
        table.bot_think.reset();
        return;
    }
    if !table.bot_think.tick(time.delta()).finished() {
        return;
    }
    let seed = rng.next_u64();
    if let Some(m) = chess_bot::best_move(&table.board, seed) {
        commit_move(&mut table, m);
    }
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
    pieces: Query<Entity, With<PieceSprite>>,
    highlights: Query<Entity, With<Highlight>>,
    promo_buttons: Query<(&PromoButton, &Transform)>,
    mut hud: Query<&mut Text2d, With<Hud>>,
) {
    if table.dirty {
        table.dirty = false;
        repaint(&mut commands, &table, &pieces, &highlights);
    }

    // Resign: R arms, a second R inside two seconds confirms.
    if let Some(t) = table.resign_arm.as_mut() {
        if t.tick(time.delta()).finished() {
            table.resign_arm = None;
        }
    }
    if keys.just_pressed(KeyCode::KeyR) && table.over_wait.is_none() && table.promo.is_none() {
        let its_my_input = match &net.0 {
            Some(cfg) => table.board.turn == seat_side(cfg.seat),
            None => !(table.bot && table.board.turn == Side::Black),
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
                let quitter = table.board.turn;
                table.result = format!(
                    "{} RESIGNS\n{} WINS",
                    if quitter == Side::White { "WHITE" } else { "BLACK" },
                    if quitter == Side::White { "BLACK" } else { "WHITE" }
                );
                // The credited account resigned (online / vs machine) or sat
                // both chairs (hotseat): a token payout either way.
                table.final_score = if net.0.is_some() || table.bot { 100 } else { 400 };
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
                (None, true) if table.board.turn == Side::Black => "\nMACHINE\nTHINKING...",
                _ => "",
            };
            let resign = if table.resign_arm.is_some() { "\n\nR AGAIN\nTO RESIGN" } else { "" };
            format!("{turn}\nTO MOVE{check}{whose}{resign}")
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
    } else if table.bot && table.board.turn == Side::Black {
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
                commit_move(&mut table, m);
                repaint(&mut commands, &table, &pieces, &highlights);
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
                commit_move(&mut table, candidates[0]);
            }
            repaint(&mut commands, &table, &pieces, &highlights);
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
    repaint(&mut commands, &table, &pieces, &highlights);
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
            "rs" => {
                table.result = "OPPONENT RESIGNS\nYOU WIN".into();
                table.final_score = 700;
                table.over_wait = Some(Timer::from_seconds(2.5, TimerMode::Once));
            }
            "mv" if seat_side(ev.seat) == table.board.turn => {
                let promo = promo_piece(&wire.promo);
                let candidate = table
                    .board
                    .legal_moves()
                    .into_iter()
                    .find(|m| m.from == wire.from && m.to == wire.to && m.promo == promo);
                if let Some(m) = candidate {
                    commit_move(&mut table, m);
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
            let human_won = if table.bot { winner == Side::White } else { true };
            table.final_score = if human_won { 1000 + edge * 20 } else { 150 };
            table.result = format!(
                "CHECKMATE\n{} WINS",
                if winner == Side::White { "WHITE" } else { "BLACK" }
            );
            table.over_wait = Some(Timer::from_seconds(3.0, TimerMode::Once));
        }
        Status::Stalemate => {
            table.final_score = 500;
            table.result = "STALEMATE".into();
            table.over_wait = Some(Timer::from_seconds(3.0, TimerMode::Once));
        }
        Status::Draw(kind) => {
            table.final_score = 500;
            table.result = match kind {
                DrawKind::FiftyMove => "DRAW\n50-MOVE RULE".into(),
                DrawKind::InsufficientMaterial => "DRAW\nBARE BONES".into(),
                DrawKind::Repetition => "DRAW\nREPETITION".into(),
            };
            table.over_wait = Some(Timer::from_seconds(3.0, TimerMode::Once));
        }
    }
}

fn endgame(
    time: Res<Time>,
    net: Res<NetMode>,
    mut table: ResMut<Table>,
    mut final_score: ResMut<FinalScore>,
    mut next: ResMut<NextState<Phase>>,
) {
    // Online mate payouts: the loser gets a consolation, not the pot.
    if let (Some(cfg), Some(_)) = (&net.0, &table.over_wait) {
        if table.result.starts_with("CHECKMATE") {
            let i_won = table
                .result
                .contains(if cfg.seat == 0 { "WHITE WINS" } else { "BLACK WINS" });
            if !i_won && table.final_score >= 1000 {
                table.final_score = 100;
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
