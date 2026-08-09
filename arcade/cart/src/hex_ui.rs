//! HEXFECTION cabinet: clone-and-jump territory war on a hex dish for up to
//! twelve players — any mix of hotseat humans and bots. Rules in arcade-logic.

use arcade_logic::hex::{HexBoard, HexMove};
use bevy::prelude::*;
use serde::{Deserialize, Serialize};

use crate::retro::{text, DIM, PLAYER_COLORS, WHITE};
use crate::rng::Rng;
use crate::shell::{net_send, sfx};
use crate::{CabinetConfig, FinalScore, GameTag, NetIn, NetMode, Phase};

/// Relayed move. Bots have no seat online: the host computes their moves and
/// relays them; receivers accept host-sent moves for any bot seat.
#[derive(Serialize, Deserialize)]
struct WireHexMove {
    t: String, // "mv"
    from: usize,
    to: usize,
}

pub const BLURB: &[&str] = &[
    "SPREAD. CONVERT. OUTGROW THEM ALL.",
    "STEP 1 CELL TO SPLIT, JUMP 2 TO LEAP.",
    "LANDING CONVERTS EVERY NEIGHBOUR.",
];

const BOARD_X: f32 = -80.0;

/// Material handles created once at setup. The old code allocated a fresh
/// ColorMaterial for every cell every frame (~5k asset inserts a second);
/// this is the entire fix.
#[derive(Resource)]
struct HexFx {
    empty: Handle<ColorMaterial>,
    players: Vec<Handle<ColorMaterial>>,
    selected: Vec<Handle<ColorMaterial>>,
    targets: Vec<Handle<ColorMaterial>>,
    pulse_mesh: Handle<Mesh>,
    pulse_mat: Handle<ColorMaterial>,
}

/// A conversion pulse: a bright ring that swells and dies over a blink.
#[derive(Component)]
struct Pulse(Timer);

#[derive(Resource)]
struct Dish {
    board: HexBoard,
    humans: u8,
    cell_size: f32,
    selected: Option<usize>,
    bot_pause: Timer,
    over_wait: Option<Timer>,
    final_score: u32,
    skip_flash: Option<(u8, Timer)>,
    last_turn: Option<u8>,
}

#[derive(Component)]
struct CellHex(usize);

#[derive(Component)]
struct BlobHex(usize);

#[derive(Component)]
struct Hud;

pub struct HexPlugin;

impl Plugin for HexPlugin {
    fn build(&self, app: &mut App) {
        app.add_systems(OnEnter(Phase::Playing), setup).add_systems(
            Update,
            (net_apply, human_clicks, bot_turns, turn_splash, splash_fade, pulse_fade, paint, hud, endgame)
                .chain()
                .run_if(in_state(Phase::Playing))
                .run_if(crate::unpaused),
        );
    }
}

fn cell_world(board: &HexBoard, i: usize, size: f32) -> Vec2 {
    let h = board.coords[i];
    let x = size * 3f32.sqrt() * (h.q as f32 + h.r as f32 / 2.0);
    let y = -size * 1.5 * h.r as f32;
    Vec2::new(x + BOARD_X, y)
}

fn setup(
    mut commands: Commands,
    config: Res<CabinetConfig>,
    net: Res<NetMode>,
    mut meshes: ResMut<Assets<Mesh>>,
    mut materials: ResMut<Assets<ColorMaterial>>,
) {
    let players = config.players.clamp(2, 12) as u8;
    let humans = if net.0.is_some() { 0 } else { config.humans.clamp(1, players as u32) as u8 };
    let board = HexBoard::new(players);
    let cell_size = if board.radius <= 4 { 30.0 } else { 25.0 };

    let cell_mesh = meshes.add(RegularPolygon::new(cell_size * 0.94, 6));
    let blob_mesh = meshes.add(RegularPolygon::new(cell_size * 0.55, 6));
    let empty_mat = materials.add(Color::srgb(0.07, 0.08, 0.13));
    let fx = HexFx {
        empty: empty_mat.clone(),
        players: (0..12).map(|i| materials.add(PLAYER_COLORS[i])).collect(),
        selected: (0..12).map(|i| materials.add(PLAYER_COLORS[i].with_alpha(0.5))).collect(),
        targets: (0..12).map(|i| materials.add(PLAYER_COLORS[i].with_alpha(0.22))).collect(),
        pulse_mesh: meshes.add(RegularPolygon::new(cell_size * 0.8, 6)),
        pulse_mat: materials.add(Color::srgba(1.0, 1.0, 1.0, 0.35)),
    };
    commands.insert_resource(fx);

    for i in 0..board.cells.len() {
        let p = cell_world(&board, i, cell_size);
        commands.spawn((
            Mesh2d(cell_mesh.clone()),
            MeshMaterial2d(empty_mat.clone()),
            Transform::from_xyz(p.x, p.y, 1.0),
            CellHex(i),
            GameTag,
        ));
        commands.spawn((
            Mesh2d(blob_mesh.clone()),
            MeshMaterial2d(empty_mat.clone()),
            Transform::from_xyz(p.x, p.y, 2.0),
            Visibility::Hidden,
            BlobHex(i),
            GameTag,
        ));
    }

    commands.insert_resource(Dish {
        board,
        humans,
        cell_size,
        selected: None,
        bot_pause: Timer::from_seconds(0.45, TimerMode::Repeating),
        over_wait: None,
        final_score: 0,
        skip_flash: None,
        last_turn: None,
    });
    let hud = text(&mut commands, "", 22.0, WHITE, Vec3::new(255.0, 150.0, 3.0));
    commands.entity(hud).insert((Hud, GameTag));
    let help = text(
        &mut commands,
        "SEATS 1..N CLOCKWISE\nFIRST SEATS ARE HUMAN\nREST ARE MACHINES",
        16.0,
        DIM,
        Vec3::new(255.0, -180.0, 3.0),
    );
    commands.entity(help).insert(GameTag);
}

/// Is this seat controlled by THIS machine's mouse?
fn is_local_human(dish: &Dish, net: &NetMode, seat: u8) -> bool {
    match &net.0 {
        Some(cfg) => seat == cfg.seat,
        None => seat < dish.humans,
    }
}

/// Is this seat driven by a human anywhere (local hotseat or remote)?
fn is_any_human(dish: &Dish, net: &NetMode, seat: u8) -> bool {
    match &net.0 {
        Some(cfg) => cfg.present.get(seat as usize).copied().unwrap_or(false),
        None => seat < dish.humans,
    }
}

/// `my_seat` is whose perspective the credited score uses: seat 0 locally
/// (the machine's own player), your network seat online.
fn end_check_for(dish: &mut Dish, my_seat: u8) {
    if !dish.board.over() {
        // Skip seats with no moves (with a visible note).
        for _ in 0..dish.board.players {
            let turn = dish.board.turn;
            if dish.board.moves_for(turn).is_empty() {
                dish.skip_flash = Some((turn, Timer::from_seconds(1.0, TimerMode::Once)));
                dish.board.skip();
            } else {
                break;
            }
        }
        if !dish.board.over() {
            return;
        }
    }
    let standings = dish.board.standings();
    let total = dish.board.players as usize;
    let my_rank = standings.iter().position(|&(p, _)| p == my_seat).unwrap_or(total - 1) + 1;
    let my_count = dish.board.count(my_seat) as u32;
    let mut score = my_count * 10 + ((total - my_rank) as u32) * 25;
    if my_rank == 1 {
        score += 200;
    }
    dish.final_score = score;
    dish.over_wait = Some(Timer::from_seconds(3.0, TimerMode::Once));
}

/// Spawns a swell-and-fade ring on every converted cell.
fn spawn_pulses(commands: &mut Commands, dish: &Dish, fx: &HexFx, cells: &[usize]) {
    for &i in cells {
        let p = cell_world(&dish.board, i, dish.cell_size);
        commands.spawn((
            Mesh2d(fx.pulse_mesh.clone()),
            MeshMaterial2d(fx.pulse_mat.clone()),
            Transform::from_xyz(p.x, p.y, 3.0),
            Pulse(Timer::from_seconds(0.35, TimerMode::Once)),
            GameTag,
        ));
    }
}

fn pulse_fade(time: Res<Time>, mut commands: Commands, mut pulses: Query<(Entity, &mut Pulse, &mut Transform)>) {
    for (e, mut pulse, mut tf) in &mut pulses {
        if pulse.0.tick(time.delta()).finished() {
            commands.entity(e).despawn();
        } else {
            let t = pulse.0.fraction();
            tf.scale = Vec3::splat(1.0 + t * 0.6);
        }
    }
}

fn human_clicks(
    buttons: Res<ButtonInput<MouseButton>>,
    windows: Query<&Window>,
    cameras: Query<(&Camera, &GlobalTransform)>,
    net: Res<NetMode>,
    mut commands: Commands,
    fx: Res<HexFx>,
    mut dish: ResMut<Dish>,
) {
    if dish.over_wait.is_some() || !is_local_human(&dish, &net, dish.board.turn) {
        return;
    }
    if !buttons.just_pressed(MouseButton::Left) {
        return;
    }
    let Ok(window) = windows.single() else { return };
    let Ok((camera, cam_tf)) = cameras.single() else { return };
    let Some(world) = crate::retro::cursor_world(window, camera, cam_tf) else { return };
    // Nearest cell within one cell radius.
    let size = dish.cell_size;
    let mut nearest: Option<(usize, f32)> = None;
    for i in 0..dish.board.cells.len() {
        let d = (cell_world(&dish.board, i, size) - world).length();
        if d < size && nearest.map(|(_, bd)| d < bd).unwrap_or(true) {
            nearest = Some((i, d));
        }
    }
    let Some((cell, _)) = nearest else { return };

    let me = dish.board.turn;
    if dish.board.cells[cell] == Some(me) {
        dish.selected = Some(cell);
        return;
    }
    if let Some(from) = dish.selected {
        let legal = dish
            .board
            .moves_for(me)
            .into_iter()
            .any(|m| m.from == from && m.to == cell);
        if legal {
            let converted = dish.board.apply(HexMove { from, to: cell });
            sfx(if converted.is_empty() { "place" } else { "capture" });
            spawn_pulses(&mut commands, &dish, &fx, &converted);
            if net.0.is_some() {
                if let Ok(w) = serde_json::to_string(&WireHexMove { t: "mv".into(), from, to: cell }) {
                    net_send(&w);
                }
            }
            dish.selected = None;
            let my_seat = net.0.as_ref().map(|c| c.seat).unwrap_or(0);
            end_check_for(&mut dish, my_seat);
        }
    }
}

fn bot_turns(
    time: Res<Time>,
    net: Res<NetMode>,
    mut commands: Commands,
    fx: Res<HexFx>,
    mut dish: ResMut<Dish>,
    mut rng: ResMut<Rng>,
) {
    let seat = dish.board.turn;
    if dish.over_wait.is_some() || is_any_human(&dish, &net, seat) {
        return;
    }
    // Online, exactly one machine may drive the bots: the host.
    if let Some(cfg) = &net.0 {
        if !cfg.is_host() {
            return;
        }
    }
    if !dish.bot_pause.tick(time.delta()).just_finished() {
        return;
    }
    // Mostly greedy, occasionally second-guess itself so bots differ.
    let mv = if rng.chance(0.15) {
        let moves = dish.board.moves_for(seat);
        if moves.is_empty() {
            None
        } else {
            Some(moves[rng.range(moves.len() as u32) as usize])
        }
    } else {
        dish.board.bot_move(seat)
    };
    match mv {
        Some(m) => {
            let converted = dish.board.apply(m);
            spawn_pulses(&mut commands, &dish, &fx, &converted);
            if net.0.is_some() {
                if let Ok(w) = serde_json::to_string(&WireHexMove { t: "mv".into(), from: m.from, to: m.to }) {
                    net_send(&w);
                }
            }
        }
        None => dish.board.skip(),
    }
    let my_seat = net.0.as_ref().map(|c| c.seat).unwrap_or(0);
    end_check_for(&mut dish, my_seat);
}

/// Applies relayed moves. A move is accepted when it is legal for the seat
/// whose turn it is AND the sender is entitled to make it: the seat itself,
/// or the host on behalf of a bot seat. Departed humans become host-driven
/// bots so the dish never stalls.
fn net_apply(
    mut events: EventReader<NetIn>,
    mut net: ResMut<NetMode>,
    mut commands: Commands,
    fx: Res<HexFx>,
    mut dish: ResMut<Dish>,
) {
    if net.0.is_none() {
        events.clear();
        return;
    }
    for ev in events.read() {
        let Some(cfg) = net.0.as_mut() else { break };
        if ev.left {
            if let Some(p) = cfg.present.get_mut(ev.seat as usize) {
                *p = false; // host's bot_turns picks the seat up next turn
            }
            dish.skip_flash = Some((ev.seat, Timer::from_seconds(1.4, TimerMode::Once)));
            continue;
        }
        if ev.seat == cfg.seat || dish.over_wait.is_some() {
            continue;
        }
        let turn = dish.board.turn;
        let sender_owns_turn = ev.seat == turn;
        let host_drives_bot = ev.seat == 0
            && !cfg.present.get(turn as usize).copied().unwrap_or(false);
        if !sender_owns_turn && !host_drives_bot {
            continue;
        }
        let Ok(wire) = serde_json::from_str::<WireHexMove>(&ev.data) else { continue };
        if wire.t != "mv" {
            continue;
        }
        let legal = dish
            .board
            .moves_for(turn)
            .into_iter()
            .any(|m| m.from == wire.from && m.to == wire.to);
        if legal {
            let converted = dish.board.apply(HexMove { from: wire.from, to: wire.to });
            sfx(if converted.is_empty() { "place" } else { "capture" });
            spawn_pulses(&mut commands, &dish, &fx, &converted);
            let my_seat = net.0.as_ref().map(|c| c.seat).unwrap_or(0);
            end_check_for(&mut dish, my_seat);
        }
    }
}

#[allow(clippy::type_complexity)]
fn paint(
    dish: Res<Dish>,
    net: Res<NetMode>,
    fx: Res<HexFx>,
    mut cells: Query<(&CellHex, &mut MeshMaterial2d<ColorMaterial>), Without<BlobHex>>,
    mut blobs: Query<(&BlobHex, &mut MeshMaterial2d<ColorMaterial>, &mut Visibility)>,
) {
    // Legal targets for the selection glow faintly in the mover's color.
    let mut target_of: Vec<bool> = vec![false; dish.board.cells.len()];
    if let (Some(from), true) = (dish.selected, is_local_human(&dish, &net, dish.board.turn)) {
        for m in dish.board.moves_for(dish.board.turn) {
            if m.from == from {
                target_of[m.to] = true;
            }
        }
    }
    let seat = dish.board.turn as usize % 12;
    for (cell, mut mat) in &mut cells {
        let want = if Some(cell.0) == dish.selected {
            &fx.selected[seat]
        } else if target_of[cell.0] {
            &fx.targets[seat]
        } else {
            &fx.empty
        };
        if mat.0 != *want {
            mat.0 = want.clone();
        }
    }
    for (blob, mut mat, mut vis) in &mut blobs {
        match dish.board.cells[blob.0] {
            Some(p) => {
                let want = &fx.players[p as usize % 12];
                if mat.0 != *want {
                    mat.0 = want.clone();
                }
                *vis = Visibility::Inherited;
            }
            None => {
                *vis = Visibility::Hidden;
            }
        }
    }
}

/// A big between-turns banner so pass-the-mouse play always knows whose go
/// it is. Online, it only announces YOUR turn.
#[derive(Component)]
struct Splash(Timer);

fn turn_splash(
    net: Res<NetMode>,
    mut commands: Commands,
    mut dish: ResMut<Dish>,
) {
    let turn = dish.board.turn;
    if dish.last_turn == Some(turn) || dish.over_wait.is_some() {
        return;
    }
    dish.last_turn = Some(turn);
    let (announce, label) = match &net.0 {
        Some(cfg) => (turn == cfg.seat, "YOUR TURN".to_string()),
        None => (
            is_local_human(&dish, &net, turn) && dish.humans > 1,
            format!("SEAT {} - GO", turn + 1),
        ),
    };
    if !announce {
        return;
    }
    sfx("tick");
    let e = crate::retro::text(
        &mut commands,
        &label,
        44.0,
        PLAYER_COLORS[turn as usize % 12],
        Vec3::new(-80.0, 0.0, 20.0),
    );
    commands.entity(e).insert((Splash(Timer::from_seconds(0.85, TimerMode::Once)), GameTag));
}

fn splash_fade(
    time: Res<Time>,
    mut commands: Commands,
    mut splashes: Query<(Entity, &mut Splash, &mut TextColor)>,
) {
    for (e, mut sp, mut color) in &mut splashes {
        if sp.0.tick(time.delta()).finished() {
            commands.entity(e).despawn();
        } else {
            color.0.set_alpha(sp.0.fraction_remaining());
        }
    }
}

fn hud(time: Res<Time>, net: Res<NetMode>, mut dish: ResMut<Dish>, mut hud: Query<&mut Text2d, With<Hud>>) {
    let Ok(mut t) = hud.single_mut() else { return };
    if let Some((seat, timer)) = dish.skip_flash.as_mut() {
        let s = format!("SEAT {} IS STUCK - SKIPPED", *seat + 1);
        if timer.tick(time.delta()).finished() {
            dish.skip_flash = None;
        } else {
            if t.0 != s {
                t.0 = s;
            }
            return;
        }
    }
    let s = if dish.over_wait.is_some() {
        let standings = dish.board.standings();
        let mut lines = vec!["FINAL DISH".to_string()];
        for (i, (p, n)) in standings.iter().take(6).enumerate() {
            lines.push(format!("{}. SEAT {} - {}", i + 1, p + 1, n));
        }
        lines.join("\n")
    } else {
        let seat = dish.board.turn;
        let who = if is_local_human(&dish, &net, seat) {
            "YOU"
        } else if is_any_human(&dish, &net, seat) {
            "HUMAN"
        } else {
            "MACHINE"
        };
        let counts: Vec<String> = dish
            .board
            .standings()
            .iter()
            .take(4)
            .map(|(p, n)| format!("S{} {}", p + 1, n))
            .collect();
        format!("SEAT {} ({who})\nTO MOVE\n\nTOP: {}", seat + 1, counts.join(" / "))
    };
    if t.0 != s {
        t.0 = s;
    }
}

fn endgame(
    time: Res<Time>,
    mut dish: ResMut<Dish>,
    mut final_score: ResMut<FinalScore>,
    mut next: ResMut<NextState<Phase>>,
) {
    let score = dish.final_score;
    if let Some(timer) = dish.over_wait.as_mut() {
        if timer.tick(time.delta()).finished() {
            final_score.0 = score;
            next.set(Phase::GameOver);
        }
    }
}
