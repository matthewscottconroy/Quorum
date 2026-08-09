//! GO cabinet: 9×9 hotseat. Click an intersection to place a stone, P to
//! pass; two passes end the game and area scoring settles it. Rules live in
//! arcade-logic.

use arcade_logic::go::{GoBoard, PlayError, Stone, SIZE};
use bevy::prelude::*;
use serde::{Deserialize, Serialize};

use crate::retro::{text, AMBER, DIM, GREEN, MAGENTA, WHITE};
use crate::shell::net_send;
use crate::{FinalScore, GameTag, NetIn, NetMode, Phase};

/// Relayed play: seat 0 is Black, seat 1 is White.
#[derive(Serialize, Deserialize)]
struct WirePlay {
    t: String, // "mv" | "pass"
    #[serde(default)]
    pos: usize,
}

fn seat_stone(seat: u8) -> Stone {
    if seat == 0 {
        Stone::Black
    } else {
        Stone::White
    }
}

pub const BLURB: &[&str] = &[
    "SURROUND TERRITORY. 9x9. TWO PLAYERS.",
    "CLICK TO PLACE  /  P TO PASS",
    "TWO PASSES END IT. AREA SCORING, KOMI 5.5",
];

const CELL: f32 = 58.0;
const ORIGIN: Vec2 = Vec2::new(-CELL * 4.0 - 60.0, -CELL * 4.0 - 10.0); // (0,0) intersection

fn point(x: usize, y: usize) -> Vec2 {
    ORIGIN + Vec2::new(x as f32 * CELL, y as f32 * CELL)
}

#[derive(Resource)]
struct Table {
    board: GoBoard,
    flash: Option<(String, Timer)>,
    over_wait: Option<Timer>,
    final_score: u32,
    result: String,
    dirty: bool, // repaint requested by the network path
}

#[derive(Component)]
struct StoneSprite;

#[derive(Component)]
struct Hud;

pub struct GoPlugin;

impl Plugin for GoPlugin {
    fn build(&self, app: &mut App) {
        app.add_systems(OnEnter(Phase::Playing), setup)
            .add_systems(Update, (net_apply, input, grid, hud, endgame).chain().run_if(in_state(Phase::Playing)));
    }
}

fn setup(mut commands: Commands) {
    commands.insert_resource(Table {
        board: GoBoard::new(),
        flash: None,
        over_wait: None,
        final_score: 0,
        result: String::new(),
        dirty: false,
    });
    let hud = text(&mut commands, "", 22.0, WHITE, Vec3::new(250.0, 120.0, 2.0));
    commands.entity(hud).insert((Hud, GameTag));
    let help = text(
        &mut commands,
        "BLACK: GREEN\nWHITE: MAGENTA\n\nP = PASS",
        18.0,
        DIM,
        Vec3::new(250.0, -140.0, 2.0),
    );
    commands.entity(help).insert(GameTag);
}

/// Board lines and star points, drawn fresh each frame with gizmos.
fn grid(mut gizmos: Gizmos) {
    let c = Color::srgb(0.28, 0.34, 0.30);
    for i in 0..SIZE {
        gizmos.line_2d(point(0, i), point(SIZE - 1, i), c);
        gizmos.line_2d(point(i, 0), point(i, SIZE - 1), c);
    }
    for &(x, y) in &[(2, 2), (6, 2), (4, 4), (2, 6), (6, 6)] {
        gizmos.circle_2d(point(x, y), 3.0, c);
    }
}

fn stone_color(s: Stone) -> Color {
    match s {
        Stone::Black => GREEN,
        Stone::White => MAGENTA,
    }
}

fn repaint(commands: &mut Commands, table: &Table, stones: &Query<Entity, With<StoneSprite>>) {
    for e in stones.iter() {
        commands.entity(e).despawn();
    }
    for pos in 0..SIZE * SIZE {
        if let Some(s) = table.board.cells[pos] {
            let p = point(pos % SIZE, pos / SIZE);
            // Square stones: honest 8-bit Go.
            commands.spawn((
                Sprite { color: stone_color(s), custom_size: Some(Vec2::splat(CELL * 0.62)), ..default() },
                Transform::from_xyz(p.x, p.y, 3.0),
                StoneSprite,
                GameTag,
            ));
        }
    }
}

#[allow(clippy::too_many_arguments)]
fn input(
    buttons: Res<ButtonInput<MouseButton>>,
    keys: Res<ButtonInput<KeyCode>>,
    windows: Query<&Window>,
    cameras: Query<(&Camera, &GlobalTransform)>,
    mut commands: Commands,
    mut table: ResMut<Table>,
    net: Res<NetMode>,
    stones: Query<Entity, With<StoneSprite>>,
) {
    if table.dirty {
        table.dirty = false;
        repaint(&mut commands, &table, &stones);
    }
    if table.over_wait.is_some() {
        return;
    }
    // Networked game: you may only act on your own turn.
    if let Some(cfg) = &net.0 {
        if table.board.turn != seat_stone(cfg.seat) {
            return;
        }
    }
    if keys.just_pressed(KeyCode::KeyP) {
        table.board.pass();
        if net.0.is_some() {
            if let Ok(w) = serde_json::to_string(&WirePlay { t: "pass".into(), pos: 0 }) {
                net_send(&w);
            }
        }
        if table.board.over() {
            settle(&mut table);
        }
        return;
    }
    if !buttons.just_pressed(MouseButton::Left) {
        return;
    }
    let Ok(window) = windows.single() else { return };
    let Ok((camera, cam_tf)) = cameras.single() else { return };
    let Some(world) = crate::retro::cursor_world(window, camera, cam_tf) else { return };
    let gx = ((world.x - ORIGIN.x) / CELL).round();
    let gy = ((world.y - ORIGIN.y) / CELL).round();
    if !(0.0..SIZE as f32).contains(&gx) || !(0.0..SIZE as f32).contains(&gy) {
        return;
    }
    // Reject clicks too far from an intersection.
    let snapped = point(gx as usize, gy as usize);
    if (world - snapped).length() > CELL * 0.42 {
        return;
    }
    let pos = gy as usize * SIZE + gx as usize;
    match table.board.play(pos) {
        Ok(()) => {
            if net.0.is_some() {
                if let Ok(w) = serde_json::to_string(&WirePlay { t: "mv".into(), pos }) {
                    net_send(&w);
                }
            }
            repaint(&mut commands, &table, &stones);
        }
        Err(e) => {
            let why = match e {
                PlayError::Occupied => "TAKEN",
                PlayError::Suicide => "NO SUICIDE",
                PlayError::Ko => "KO - PLAY ELSEWHERE FIRST",
                PlayError::Over => "GAME OVER",
            };
            table.flash = Some((why.to_string(), Timer::from_seconds(1.4, TimerMode::Once)));
        }
    }
}

/// Applies relayed opponent plays; handles the opponent leaving mid-game.
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
                table.final_score = 700;
                table.result = "OPPONENT LEFT\nYOU WIN".into();
                table.over_wait = Some(Timer::from_seconds(2.0, TimerMode::Once));
                let e = text(&mut commands, "OPPONENT LEFT - YOU WIN", 28.0, AMBER, Vec3::new(-60.0, 0.0, 30.0));
                commands.entity(e).insert(GameTag);
            }
            continue;
        }
        if ev.seat == cfg.seat || seat_stone(ev.seat) != table.board.turn {
            continue;
        }
        let Ok(wire) = serde_json::from_str::<WirePlay>(&ev.data) else { continue };
        match wire.t.as_str() {
            "pass" => {
                table.board.pass();
                if table.board.over() {
                    settle(&mut table);
                }
            }
            "mv" => {
                if wire.pos < SIZE * SIZE && table.board.play(wire.pos).is_ok() {
                    table.dirty = true;
                }
            }
            _ => {}
        }
    }
}

fn settle(table: &mut Table) {
    let (black, white) = table.board.area_score();
    let (bp, wp) = (black as f32 / 2.0, white as f32 / 2.0);
    let margin_half = (black - white).unsigned_abs();
    table.result = if black > white {
        format!("BLACK {bp:.1} - WHITE {wp:.1}\nBLACK WINS BY {:.1}", margin_half as f32 / 2.0)
    } else {
        format!("BLACK {bp:.1} - WHITE {wp:.1}\nWHITE WINS BY {:.1}", margin_half as f32 / 2.0)
    };
    // House scoring: decisive play pays; komi means no draws.
    table.final_score = 250 + margin_half * 5;
    table.over_wait = Some(Timer::from_seconds(3.5, TimerMode::Once));
}

fn hud(time: Res<Time>, mut table: ResMut<Table>, mut hud: Query<&mut Text2d, With<Hud>>) {
    let Ok(mut t) = hud.single_mut() else { return };
    if let Some((msg, timer)) = table.flash.as_mut() {
        let s = msg.clone();
        if timer.tick(time.delta()).finished() {
            table.flash = None;
        } else if t.0 != s {
            t.0 = s;
            return;
        } else {
            return;
        }
    }
    let s = if !table.result.is_empty() {
        table.result.clone()
    } else {
        let (b, w) = (table.board.captures_black, table.board.captures_white);
        let turn = match table.board.turn {
            Stone::Black => "BLACK",
            Stone::White => "WHITE",
        };
        let passes = if table.board.passes_in_a_row == 1 { "\n1 PASS - ONE MORE ENDS IT" } else { "" };
        format!("{turn}\nTO PLAY\n\nCAPTURES\nB {b} / W {w}{passes}")
    };
    if t.0 != s {
        t.0 = s;
    }
}

fn endgame(
    time: Res<Time>,
    mut table: ResMut<Table>,
    mut final_score: ResMut<FinalScore>,
    mut next: ResMut<NextState<Phase>>,
) {
    let score = table.final_score;
    if let Some(timer) = table.over_wait.as_mut() {
        if timer.tick(time.delta()).finished() {
            final_score.0 = score;
            next.set(Phase::GameOver);
        }
    }
}
