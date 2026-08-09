//! CHESS cabinet: two players share the mouse. Click a piece, click a
//! destination; promotion pops a picker. Rules live in arcade-logic.

use arcade_logic::chess::{file, rank, Board, Color as Side, Move, Piece, Status};
use bevy::prelude::*;
use serde::{Deserialize, Serialize};

use crate::retro::{text, AMBER, DIM, GREEN, MAGENTA, WHITE};
use crate::shell::net_send;
use crate::{FinalScore, GameTag, NetIn, NetMode, Phase};

pub const BLURB: &[&str] = &[
    "TWO PLAYERS. ONE MOUSE - OR ONE ROOM.",
    "CLICK A PIECE, CLICK A SQUARE.",
    "MATE PAYS 1000. STALEMATE SPLITS THE POT.",
];

/// Relayed move: seat 0 is White, seat 1 is Black.
#[derive(Serialize, Deserialize)]
struct WireMove {
    t: String, // "mv"
    from: usize,
    to: usize,
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

const CELL: f32 = 64.0;
const X0: f32 = -320.0; // left edge of a1
const Y0: f32 = -256.0; // bottom edge of a1

fn square_center(sq: usize) -> Vec2 {
    Vec2::new(
        X0 + file(sq) as f32 * CELL + CELL / 2.0,
        Y0 + rank(sq) as f32 * CELL + CELL / 2.0,
    )
}

fn square_at(world: Vec2) -> Option<usize> {
    let fx = ((world.x - X0) / CELL).floor();
    let fy = ((world.y - Y0) / CELL).floor();
    if (0.0..8.0).contains(&fx) && (0.0..8.0).contains(&fy) {
        Some(fy as usize * 8 + fx as usize)
    } else {
        None
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
    dirty: bool, // repaint requested (set at round start)
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
            .add_systems(Update, (net_apply, clicks, endgame).chain().run_if(in_state(Phase::Playing)));
    }
}

fn setup(mut commands: Commands) {
    commands.insert_resource(Table {
        board: Board::start(),
        selected: None,
        legal: Vec::new(),
        promo: None,
        over_wait: None,
        final_score: 0,
        dirty: true,
    });
    // The board: light/dark squares in muted CRT blues.
    for sq in 0..64 {
        let dark = (file(sq) + rank(sq)) % 2 == 0;
        let color = if dark {
            Color::srgb(0.10, 0.13, 0.25)
        } else {
            Color::srgb(0.17, 0.22, 0.38)
        };
        let p = square_center(sq);
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
        "WHITE: GREEN\nBLACK: MAGENTA",
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

fn side_color(side: Side) -> bevy::prelude::Color {
    match side {
        Side::White => GREEN,
        Side::Black => MAGENTA,
    }
}

/// Full repaint: pieces, highlights, promo picker, HUD. Called after every
/// state change — 64 squares of despawn/respawn is nothing.
fn repaint(
    commands: &mut Commands,
    table: &Table,
    pieces: &Query<Entity, With<PieceSprite>>,
    highlights: &Query<Entity, With<Highlight>>,
) {
    for e in pieces.iter().chain(highlights.iter()) {
        commands.entity(e).despawn();
    }
    for sq in 0..64 {
        if let Some((side, piece)) = table.board.cells[sq] {
            let p = square_center(sq);
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
        let p = square_center(sel);
        commands.spawn((
            Sprite { color: Color::srgba(1.0, 0.82, 0.4, 0.25), custom_size: Some(Vec2::splat(CELL - 2.0)), ..default() },
            Transform::from_xyz(p.x, p.y, 3.0),
            Highlight,
            GameTag,
        ));
        for m in table.legal.iter().filter(|m| m.from == sel) {
            let p = square_center(m.to);
            commands.spawn((
                Sprite { color: Color::srgba(1.0, 0.82, 0.4, 0.55), custom_size: Some(Vec2::splat(12.0)), ..default() },
                Transform::from_xyz(p.x, p.y, 3.0),
                Highlight,
                GameTag,
            ));
        }
    }
    if table.promo.is_some() {
        // Picker: four buttons over the board's right edge.
        let panel_y = 0.0;
        commands.spawn((
            Sprite { color: Color::srgba(0.0, 0.0, 0.0, 0.85), custom_size: Some(Vec2::new(120.0, 300.0)), ..default() },
            Transform::from_xyz(275.0, panel_y, 10.0),
            Highlight,
            GameTag,
        ));
        for (i, piece) in [Piece::Queen, Piece::Rook, Piece::Bishop, Piece::Knight].iter().enumerate() {
            let y = panel_y + 105.0 - i as f32 * 70.0;
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

#[allow(clippy::too_many_arguments)]
fn clicks(
    buttons: Res<ButtonInput<MouseButton>>,
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
    // HUD refresh is cheap; do it every frame.
    if let Ok(mut t) = hud.single_mut() {
        let s = if table.over_wait.is_some() {
            match table.board.status() {
                Status::Checkmate { winner } => {
                    format!("CHECKMATE\n{} WINS", if winner == Side::White { "WHITE" } else { "BLACK" })
                }
                Status::Stalemate => "STALEMATE".to_string(),
                Status::Ongoing => String::new(),
            }
        } else {
            let turn = if table.board.turn == Side::White { "WHITE" } else { "BLACK" };
            let check = if table.board.in_check(table.board.turn) { "\nCHECK!" } else { "" };
            let mine = match &net.0 {
                Some(cfg) if table.board.turn == seat_side(cfg.seat) => "\n(YOUR MOVE)",
                Some(_) => "\n(THEIR MOVE)",
                None => "",
            };
            format!("{turn}\nTO MOVE{check}{mine}")
        };
        if t.0 != s {
            t.0 = s;
        }
    }

    if table.dirty {
        table.dirty = false;
        repaint(&mut commands, &table, &pieces, &highlights);
    }
    if table.over_wait.is_some() || !buttons.just_pressed(MouseButton::Left) {
        return;
    }
    let Ok(window) = windows.single() else { return };
    let Ok((camera, cam_tf)) = cameras.single() else { return };
    let Some(world) = crate::retro::cursor_world(window, camera, cam_tf) else { return };

    // Networked game: you may only move on your own turn.
    if let Some(cfg) = &net.0 {
        if table.board.turn != seat_side(cfg.seat) {
            return;
        }
    }

    // Promotion picker eats every click while open.
    if let Some((from, to)) = table.promo {
        for (btn, tf) in &promo_buttons {
            if (world - tf.translation.truncate()).abs().max_element() < 30.0 {
                let m = Move { from, to, promo: Some(btn.0) };
                table.board.apply(m);
                if net.0.is_some() {
                    send_move(m);
                }
                table.promo = None;
                table.selected = None;
                table.legal = table.board.legal_moves();
                check_end(&mut table);
                repaint(&mut commands, &table, &pieces, &highlights);
                return;
            }
        }
        return;
    }

    let Some(sq) = square_at(world) else { return };
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
                table.board.apply(candidates[0]);
                if net.0.is_some() {
                    send_move(candidates[0]);
                }
                table.selected = None;
                table.legal = table.board.legal_moves();
                check_end(&mut table);
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
    }
    repaint(&mut commands, &table, &pieces, &highlights);
}

fn send_move(m: Move) {
    let wire = WireMove { t: "mv".into(), from: m.from, to: m.to, promo: promo_str(m.promo) };
    if let Ok(s) = serde_json::to_string(&wire) {
        net_send(&s);
    }
}

/// Applies relayed opponent moves; handles the opponent leaving mid-game.
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
        if ev.left {
            if table.over_wait.is_none() {
                // Abandonment: the remaining player takes the win.
                table.final_score = 700;
                table.over_wait = Some(Timer::from_seconds(2.0, TimerMode::Once));
                let e = text(&mut commands, "OPPONENT LEFT - YOU WIN", 30.0, AMBER, Vec3::new(0.0, 0.0, 30.0));
                commands.entity(e).insert(GameTag);
            }
            continue;
        }
        // Only the seat whose turn it is may move; the relay stamps seats.
        if seat_side(ev.seat) != table.board.turn || ev.seat == cfg.seat {
            continue;
        }
        let Ok(wire) = serde_json::from_str::<WireMove>(&ev.data) else { continue };
        if wire.t != "mv" {
            continue;
        }
        let promo = promo_piece(&wire.promo);
        let candidate = table
            .board
            .legal_moves()
            .into_iter()
            .find(|m| m.from == wire.from && m.to == wire.to && m.promo == promo);
        if let Some(m) = candidate {
            table.board.apply(m);
            table.selected = None;
            table.legal = table.board.legal_moves();
            table.dirty = true;
            check_end(&mut table);
        }
    }
}

fn check_end(table: &mut Table) {
    match table.board.status() {
        Status::Ongoing => {}
        Status::Checkmate { winner } => {
            let edge = table.board.material(winner).max(0) as u32;
            table.final_score = 1000 + edge * 20;
            table.over_wait = Some(Timer::from_seconds(3.0, TimerMode::Once));
        }
        Status::Stalemate => {
            table.final_score = 500;
            table.over_wait = Some(Timer::from_seconds(3.0, TimerMode::Once));
        }
    }
}

fn endgame(
    time: Res<Time>,
    mut table: ResMut<Table>,
    mut final_score: ResMut<FinalScore>,
    mut next: ResMut<NextState<Phase>>,
) {
    let score = table.final_score;
    if let Some(t) = table.over_wait.as_mut() {
        if t.tick(time.delta()).finished() {
            final_score.0 = score;
            next.set(Phase::GameOver);
        }
    }
}
