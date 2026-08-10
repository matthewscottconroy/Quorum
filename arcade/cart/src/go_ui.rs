//! GO cabinet: 9×9 hotseat, versus the machine, or online. Click an
//! intersection to place a stone, P to pass; two passes open a dead-stone
//! marking phase, then area scoring settles it. R-R resigns. Rules and the
//! machine opponent live in arcade-logic.

use arcade_logic::go::{GoBoard, PlayError, Stone, CELLS, SIZE};
use arcade_logic::go_bot;
use bevy::prelude::*;
use serde::{Deserialize, Serialize};

use crate::retro::{popup, text, AMBER, DIM, GREEN, MAGENTA, RED, WHITE};
use crate::rng::Rng;
use crate::shell::{net_send, sfx, stat};
use crate::{CabinetConfig, FinalScore, GameTag, NetIn, NetMode, Phase};

/// Relayed play: seat 0 is Black, seat 1 is White.
#[derive(Serialize, Deserialize)]
struct WirePlay {
    t: String, // "mv" | "pass" | "rs" | "dead" | "ok" | "resume"
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
    "SURROUND TERRITORY. 9x9. SOLO OR TWO PLAYERS.",
    "CLICK TO PLACE / P TO PASS / U UNDOES (HOTSEAT)",
    "TWO PASSES: MARK DEAD GROUPS, THEN AREA SCORING",
    "KOMI 5.5. R TWICE RESIGNS.",
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
    /// The machine plays White in local single-player rounds.
    bot: bool,
    bot_think: Timer,
    /// Two humans, one keyboard: flat token payout (see `settle`).
    hotseat: bool,
    /// Dead-stone marking phase after two passes.
    marking: bool,
    dead: [bool; CELLS],
    confirm_me: bool,
    confirm_them: bool,
    /// First R press arms resignation; second confirms.
    resign_arm: Option<Timer>,
}

#[derive(Component)]
struct StoneSprite;

#[derive(Component)]
struct Hud;

pub struct GoPlugin;

impl Plugin for GoPlugin {
    fn build(&self, app: &mut App) {
        app.add_systems(OnEnter(Phase::Playing), setup)
            .add_systems(
                Update,
                (net_apply, bot_play, input, grid, hud, endgame)
                    .chain()
                    .run_if(in_state(Phase::Playing))
                    .run_if(crate::unpaused),
            );
    }
}

fn setup(mut commands: Commands, config: Res<CabinetConfig>, net: Res<NetMode>) {
    commands.insert_resource(Table {
        board: GoBoard::new(),
        flash: None,
        over_wait: None,
        final_score: 0,
        result: String::new(),
        dirty: false,
        bot: net.0.is_none() && config.humans == 1,
        bot_think: Timer::from_seconds(0.7, TimerMode::Once),
        hotseat: net.0.is_none() && config.humans >= 2,
        marking: false,
        dead: [false; CELLS],
        confirm_me: false,
        confirm_them: false,
        resign_arm: None,
    });
    let hud = text(&mut commands, "", 22.0, WHITE, Vec3::new(250.0, 120.0, 2.0));
    commands.entity(hud).insert((Hud, GameTag));
    let help = text(
        &mut commands,
        "BLACK: GREEN\nWHITE: MAGENTA\n\nP = PASS\nU = UNDO (HOTSEAT)\nR+R RESIGN",
        18.0,
        DIM,
        Vec3::new(250.0, -140.0, 2.0),
    );
    commands.entity(help).insert(GameTag);
}

/// Board lines, star points, and the last-move ring, drawn with gizmos.
fn grid(mut gizmos: Gizmos, table: Res<Table>) {
    let c = Color::srgb(0.28, 0.34, 0.30);
    for i in 0..SIZE {
        gizmos.line_2d(point(0, i), point(SIZE - 1, i), c);
        gizmos.line_2d(point(i, 0), point(i, SIZE - 1), c);
    }
    for &(x, y) in &[(2, 2), (6, 2), (4, 4), (2, 6), (6, 6)] {
        gizmos.circle_2d(point(x, y), 3.0, c);
    }
    if let Some(last) = table.board.last {
        let p = point(last % SIZE, last / SIZE);
        gizmos.circle_2d(p, CELL * 0.44, crate::retro::WHITE);
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
            // Square stones: honest 8-bit Go. Marked-dead stones go ghostly.
            let mut color = stone_color(s);
            if table.marking && table.dead[pos] {
                color = color.with_alpha(0.3);
            }
            commands.spawn((
                Sprite { color, custom_size: Some(Vec2::splat(CELL * 0.62)), ..default() },
                Transform::from_xyz(p.x, p.y, 3.0),
                StoneSprite,
                GameTag,
            ));
        }
    }
}

/// Two passes: the game pauses for dead-stone marking instead of scoring
/// blind — a premature pass no longer silently forfeits live territory.
fn enter_marking(table: &mut Table) {
    table.marking = true;
    table.dead = [false; CELLS];
    table.confirm_me = false;
    table.confirm_them = false;
    table.dirty = true;
    sfx("tick");
}

fn send_wire(t: &str, pos: usize) {
    if let Ok(w) = serde_json::to_string(&WirePlay { t: t.into(), pos }) {
        net_send(&w);
    }
}

fn stone_name(s: Stone) -> &'static str {
    match s {
        Stone::Black => "BLACK",
        Stone::White => "WHITE",
    }
}

/// Board coordinates under the cursor, if the click lands near an
/// intersection.
fn clicked_point(
    windows: &Query<&Window>,
    cameras: &Query<(&Camera, &GlobalTransform)>,
) -> Option<usize> {
    let window = windows.single().ok()?;
    let (camera, cam_tf) = cameras.single().ok()?;
    let world = crate::retro::cursor_world(window, camera, cam_tf)?;
    let gx = ((world.x - ORIGIN.x) / CELL).round();
    let gy = ((world.y - ORIGIN.y) / CELL).round();
    if !(0.0..SIZE as f32).contains(&gx) || !(0.0..SIZE as f32).contains(&gy) {
        return None;
    }
    let snapped = point(gx as usize, gy as usize);
    if (world - snapped).length() > CELL * 0.42 {
        return None;
    }
    Some(gy as usize * SIZE + gx as usize)
}

#[allow(clippy::too_many_arguments)]
fn input(
    buttons: Res<ButtonInput<MouseButton>>,
    keys: Res<ButtonInput<KeyCode>>,
    time: Res<Time>,
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

    // Resign: R arms, a second R inside two seconds confirms. Legal at any
    // time in every mode — nobody gets held hostage by a stalled opponent.
    if let Some(t) = table.resign_arm.as_mut() {
        if t.tick(time.delta()).finished() {
            table.resign_arm = None;
        }
    }
    if keys.just_pressed(KeyCode::KeyR) {
        if table.resign_arm.take().is_some() {
            if net.0.is_some() {
                send_wire("rs", 0);
            }
            let quitter = match &net.0 {
                Some(cfg) => seat_stone(cfg.seat),
                None if table.bot => Stone::Black, // the human seat
                None => table.board.turn,
            };
            table.result = format!("{} RESIGNS\n{} WINS", stone_name(quitter), stone_name(quitter.other()));
            table.final_score = 100;
            stat("resignations", 1);
            if net.0.is_some() {
                stat("losses_online", 1);
            }
            table.over_wait = Some(Timer::from_seconds(2.5, TimerMode::Once));
            return;
        }
        table.resign_arm = Some(Timer::from_seconds(2.0, TimerMode::Once));
    }

    // Dead-stone marking phase: both players act, no turn gate.
    if table.marking {
        if keys.just_pressed(KeyCode::Enter) {
            if net.0.is_none() {
                settle(&mut table, &net);
            } else if !table.confirm_me {
                table.confirm_me = true;
                send_wire("ok", 0);
                sfx("tick");
                if table.confirm_them {
                    settle(&mut table, &net);
                }
            }
            return;
        }
        if keys.just_pressed(KeyCode::KeyM) {
            table.board.resume();
            table.marking = false;
            table.dirty = true;
            sfx("tick");
            if net.0.is_some() {
                send_wire("resume", 0);
            }
            return;
        }
        if buttons.just_pressed(MouseButton::Left) {
            if let Some(pos) = clicked_point(&windows, &cameras) {
                let group = table.board.group_at(pos);
                if !group.is_empty() {
                    let flip = !table.dead[pos];
                    if flip {
                        stat("dead_stones_marked", group.len() as u64);
                    }
                    for g in group {
                        table.dead[g] = flip;
                    }
                    table.confirm_me = false;
                    table.confirm_them = false;
                    table.dirty = true;
                    sfx("tick");
                    if net.0.is_some() {
                        send_wire("dead", pos);
                    }
                }
            }
        }
        return;
    }

    // Whose input is live? Online: your turn only. Vs machine: Black only.
    if let Some(cfg) = &net.0 {
        if table.board.turn != seat_stone(cfg.seat) {
            return;
        }
    } else if table.bot && table.board.turn == Stone::White {
        return;
    }
    if keys.just_pressed(KeyCode::KeyP) {
        table.board.pass();
        sfx("tick");
        stat("passes", 1);
        if net.0.is_some() {
            send_wire("pass", 0);
        }
        if table.board.over() {
            enter_marking(&mut table);
        }
        return;
    }
    // Courtesy undo (local only). Vs the machine it takes back the full
    // exchange — its reply and your stone — so it's your move again.
    if keys.just_pressed(KeyCode::KeyU) && net.0.is_none() {
        let undone = if table.bot {
            let a = table.board.undo();
            let b = table.board.undo();
            a || b
        } else {
            table.board.undo()
        };
        if undone {
            sfx("tick");
            stat("takebacks", 1);
            repaint(&mut commands, &table, &stones);
        }
        return;
    }
    if !buttons.just_pressed(MouseButton::Left) {
        return;
    }
    let Some(pos) = clicked_point(&windows, &cameras) else { return };
    let before = table.board.captures_black + table.board.captures_white;
    match table.board.play(pos) {
        Ok(()) => {
            let after = table.board.captures_black + table.board.captures_white;
            sfx(if after > before { "capture" } else { "place" });
            stat("stones_placed", 1);
            stat("stones_captured", (after - before) as u64);
            if net.0.is_some() {
                send_wire("mv", pos);
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
            // A rejected click must never look dead: buzz + a note right at
            // the attempted intersection, not only in the far HUD.
            sfx("buzz");
            let short = match e {
                PlayError::Occupied => "TAKEN",
                PlayError::Suicide => "SUICIDE",
                PlayError::Ko => "KO",
                PlayError::Over => "OVER",
            };
            popup(&mut commands, short, 16.0, RED, point(pos % SIZE, pos / SIZE));
            table.flash = Some((why.to_string(), Timer::from_seconds(1.4, TimerMode::Once)));
        }
    }
}

/// The machine's move (it plays White locally when one human coined up).
fn bot_play(time: Res<Time>, mut table: ResMut<Table>, mut rng: ResMut<Rng>, net: Res<NetMode>) {
    if !table.bot || net.0.is_some() || table.over_wait.is_some() || table.marking {
        return;
    }
    if table.board.turn != Stone::White {
        table.bot_think.reset();
        return;
    }
    if !table.bot_think.tick(time.delta()).finished() {
        return;
    }
    let before = table.board.captures_black + table.board.captures_white;
    match go_bot::bot_move(&table.board, rng.next_u64()) {
        Some(pos) => {
            if table.board.play(pos).is_ok() {
                let after = table.board.captures_black + table.board.captures_white;
                sfx(if after > before { "capture" } else { "place" });
                stat("stones_lost", (after - before) as u64);
                table.dirty = true;
            }
        }
        None => {
            table.board.pass();
            sfx("tick");
            if table.board.over() {
                enter_marking(&mut table);
            }
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
                stat("wins_online", 1);
                table.result = "OPPONENT LEFT\nYOU WIN".into();
                table.over_wait = Some(Timer::from_seconds(2.0, TimerMode::Once));
                let e = text(&mut commands, "OPPONENT LEFT - YOU WIN", 28.0, AMBER, Vec3::new(-60.0, 0.0, 30.0));
                commands.entity(e).insert(GameTag);
            }
            continue;
        }
        if ev.seat == cfg.seat {
            continue;
        }
        let Ok(wire) = serde_json::from_str::<WirePlay>(&ev.data) else { continue };
        let sync_lost = |table: &mut Table| {
            // The peers disagree about the position; the sender already
            // committed. Dropping the play silently would wedge both clients
            // forever — end it honestly instead.
            table.result = "SYNC LOST\nCALLED OFF".into();
            table.final_score = 250;
            table.over_wait = Some(Timer::from_seconds(2.5, TimerMode::Once));
            table.dirty = true;
        };
        match wire.t.as_str() {
            "pass" => {
                if seat_stone(ev.seat) != table.board.turn || table.marking {
                    sync_lost(&mut table);
                    continue;
                }
                table.board.pass();
                if table.board.over() {
                    enter_marking(&mut table);
                }
            }
            "mv" => {
                if seat_stone(ev.seat) != table.board.turn
                    || table.marking
                    || wire.pos >= SIZE * SIZE
                    || table.board.play(wire.pos).is_err()
                {
                    sync_lost(&mut table);
                    continue;
                }
                table.dirty = true;
                sfx("place");
            }
            "rs" => {
                table.result = "OPPONENT RESIGNS\nYOU WIN".into();
                table.final_score = 700;
                stat("wins_online", 1);
                table.over_wait = Some(Timer::from_seconds(2.5, TimerMode::Once));
            }
            "dead" if table.marking && wire.pos < SIZE * SIZE => {
                let group = table.board.group_at(wire.pos);
                if !group.is_empty() {
                    let flip = !table.dead[wire.pos];
                    for g in group {
                        table.dead[g] = flip;
                    }
                    // Any re-mark reopens the question for both sides.
                    table.confirm_me = false;
                    table.confirm_them = false;
                    table.dirty = true;
                    sfx("tick");
                }
            }
            "ok" if table.marking => {
                table.confirm_them = true;
                if table.confirm_me {
                    let netmode = NetMode(Some(cfg.clone()));
                    settle(&mut table, &netmode);
                }
            }
            "resume" if table.marking => {
                table.board.resume();
                table.marking = false;
                table.dirty = true;
                sfx("tick");
            }
            _ => {}
        }
    }
}

fn settle(table: &mut Table, net: &NetMode) {
    let (black, white) = table.board.score_with_dead(&table.dead);
    let (bp, wp) = (black as f32 / 2.0, white as f32 / 2.0);
    let margin_half = (black - white).unsigned_abs();
    table.result = if black > white {
        format!("BLACK {bp:.1} - WHITE {wp:.1}\nBLACK WINS BY {:.1}", margin_half as f32 / 2.0)
    } else {
        format!("BLACK {bp:.1} - WHITE {wp:.1}\nWHITE WINS BY {:.1}", margin_half as f32 / 2.0)
    };
    // Seat-aware payout: only the seat that WON collects the pot (margin
    // bonus capped so running up the score stays flavor, not strategy).
    // Hotseat pays a flat token — one person sat both chairs.
    table.final_score = if table.hotseat {
        stat("hotseat_rounds", 1);
        100
    } else {
        let my_stone = match &net.0 {
            Some(cfg) => seat_stone(cfg.seat),
            None => Stone::Black, // vs the machine, the human holds Black
        };
        let i_won = (black > white) == (my_stone == Stone::Black);
        match (&net.0, i_won) {
            (Some(_), true) => stat("wins_online", 1),
            (Some(_), false) => stat("losses_online", 1),
            (None, true) => stat("machine_beaten", 1),
            (None, false) => stat("beaten_by_machine", 1),
        }
        if i_won {
            400 + margin_half.min(60) * 3
        } else {
            150
        }
    };
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
    } else if table.marking {
        let (black, white) = table.board.score_with_dead(&table.dead);
        let wait = if table.confirm_me && !table.confirm_them {
            "\nWAITING FOR\nOPPONENT..."
        } else {
            ""
        };
        format!(
            "MARK DEAD\nGROUPS\n\nCLICK TOGGLES\nENTER ACCEPTS\nM PLAYS ON\n\nB {:.1} / W {:.1}{wait}",
            black as f32 / 2.0,
            white as f32 / 2.0
        )
    } else {
        let (b, w) = (table.board.captures_black, table.board.captures_white);
        let turn = match table.board.turn {
            Stone::Black => "BLACK",
            Stone::White => "WHITE",
        };
        let thinking = if table.bot && table.board.turn == Stone::White { "\nMACHINE\nTHINKING..." } else { "" };
        let resign = if table.resign_arm.is_some() { "\n\nR AGAIN\nTO RESIGN" } else { "" };
        let passes = if table.board.passes_in_a_row == 1 { "\n1 PASS - ONE MORE ENDS IT" } else { "" };
        format!("{turn}\nTO PLAY{thinking}\n\nCAPTURES\nB {b} / W {w}{passes}{resign}")
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
