//! HEXFECTION cabinet: clone-and-jump territory war on a hex dish for up to
//! twelve players — any mix of hotseat humans and bots. Rules in arcade-logic.

use arcade_logic::hex::{HexBoard, HexMove};
use bevy::prelude::*;
use serde::{Deserialize, Serialize};

use crate::retro::{text, DIM, PLAYER_COLORS, WHITE};
use crate::rng::Rng;
use crate::shell::{net_send, sfx};
use crate::{CabinetConfig, FinalScore, GameTag, NetIn, NetMode, Phase};

/// Relayed move. Bots have no seat online: the ACTING host (lowest still-
/// present seat, so a host disconnect never strands the dish) computes their
/// moves and relays them; receivers accept acting-host moves for any bot
/// seat. "afk" flags a stalled human seat over to the bots.
#[derive(Serialize, Deserialize)]
struct WireHexMove {
    t: String, // "mv" | "afk"
    #[serde(default)]
    from: usize,
    #[serde(default)]
    to: usize,
    #[serde(default)]
    seat: u8,
}

/// The seat that drives bot turns online: the lowest seat still holding a
/// live human. Every client computes this identically from `present`, so
/// exactly one machine acts even after the original host leaves.
fn acting_host(cfg: &crate::NetCfg) -> u8 {
    cfg.present.iter().position(|&p| p).map(|i| i as u8).unwrap_or(0)
}

/// Online per-turn allowance for HUMAN seats before the bots take the seat
/// over — an AFK breaker, not a blitz clock.
const AFK_SECS: f32 = 35.0;

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
    /// Every seat skipped in the current turn-resolution, not just the last.
    skip_flash: Vec<(u8, Timer)>,
    last_turn: Option<u8>,
    /// AFK watch: whose turn the clock is timing, and the clock.
    afk_turn: Option<u8>,
    afk: Timer,
    /// Who still had blobs after the previous move — elimination detector.
    alive_prev: Vec<bool>,
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
            (
                (net_apply, human_clicks, bot_turns, afk_watch, eliminations),
                (turn_splash, splash_fade, pulse_fade, paint, hud, endgame),
            )
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

    let alive_prev = (0..players).map(|p| board.count(p) > 0).collect();
    commands.insert_resource(Dish {
        board,
        humans,
        cell_size,
        selected: None,
        bot_pause: Timer::from_seconds(0.45, TimerMode::Repeating),
        over_wait: None,
        final_score: 0,
        skip_flash: Vec::new(),
        last_turn: None,
        afk_turn: None,
        afk: Timer::from_seconds(AFK_SECS, TimerMode::Once),
        alive_prev,
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

/// Is this seat controlled by THIS machine's mouse? Online, an AFK-flagged
/// seat (present = false) has been handed to the bots — its human is a
/// spectator from then on, which keeps every client's view consistent.
fn is_local_human(dish: &Dish, net: &NetMode, seat: u8) -> bool {
    match &net.0 {
        Some(cfg) => seat == cfg.seat && cfg.present.get(seat as usize).copied().unwrap_or(false),
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
/// (the machine's own player), your network seat online. On game end it
/// throws the winner a proper celebration — splash, tone, pulsing blobs.
fn end_check_for(dish: &mut Dish, my_seat: u8, commands: &mut Commands, fx: &HexFx) {
    if !dish.board.over() {
        // Skip seats with no moves (with a visible note; several can be
        // stuck in one resolution, and every one deserves its line).
        for _ in 0..dish.board.players {
            let turn = dish.board.turn;
            if dish.board.moves_for(turn).is_empty() {
                dish.skip_flash.push((turn, Timer::from_seconds(1.4, TimerMode::Once)));
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
    // The dish has a winner: say so like an arcade machine means it.
    if let Some(&(winner, _)) = standings.first() {
        sfx("win");
        let e = text(
            commands,
            &format!("SEAT {} TAKES THE DISH", winner + 1),
            40.0,
            PLAYER_COLORS[winner as usize % 12],
            Vec3::new(BOARD_X, 0.0, 20.0),
        );
        commands.entity(e).insert((Splash(Timer::from_seconds(2.8, TimerMode::Once)), GameTag));
        let winner_cells: Vec<usize> = (0..dish.board.cells.len())
            .filter(|&i| dish.board.cells[i] == Some(winner))
            .collect();
        spawn_pulses(commands, dish, fx, &winner_cells);
    }
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
                if let Ok(w) =
                    serde_json::to_string(&WireHexMove { t: "mv".into(), from, to: cell, seat: 0 })
                {
                    net_send(&w);
                }
            }
            dish.selected = None;
            let my_seat = net.0.as_ref().map(|c| c.seat).unwrap_or(0);
            end_check_for(&mut dish, my_seat, &mut commands, &fx);
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
    // Online, exactly one machine drives the bots: the ACTING host (lowest
    // still-present seat) — a host disconnect hands the duty down instead of
    // deadlocking the dish on the next bot turn.
    if let Some(cfg) = &net.0 {
        if cfg.seat != acting_host(cfg) {
            return;
        }
    }
    if !dish.bot_pause.tick(time.delta()).just_finished() {
        return;
    }
    // Seeded variety: samples among near-best moves, so bots differ from
    // game to game without ever volunteering a reckless landing.
    let mv = dish.board.bot_move_seeded(seat, rng.next_u64());
    match mv {
        Some(m) => {
            let converted = dish.board.apply(m);
            spawn_pulses(&mut commands, &dish, &fx, &converted);
            if net.0.is_some() {
                if let Ok(w) = serde_json::to_string(&WireHexMove {
                    t: "mv".into(),
                    from: m.from,
                    to: m.to,
                    seat: 0,
                }) {
                    net_send(&w);
                }
            }
        }
        None => dish.board.skip(),
    }
    let my_seat = net.0.as_ref().map(|c| c.seat).unwrap_or(0);
    end_check_for(&mut dish, my_seat, &mut commands, &fx);
}

/// Online AFK breaker: when a HUMAN seat stalls past the allowance, the
/// acting host flags it over to the bots (relayed as "afk" so every client
/// flips `present` together). The flagged player keeps watching; the bots
/// play the seat out.
fn afk_watch(time: Res<Time>, mut net: ResMut<NetMode>, mut dish: ResMut<Dish>) {
    let Some(cfg) = net.0.as_mut() else { return };
    if dish.over_wait.is_some() {
        return;
    }
    let turn = dish.board.turn;
    if dish.afk_turn != Some(turn) {
        dish.afk_turn = Some(turn);
        dish.afk.reset();
        return;
    }
    if !cfg.present.get(turn as usize).copied().unwrap_or(false) {
        return; // bot seat: the bot pause handles pace, not the AFK clock
    }
    dish.afk.tick(time.delta());
    if !dish.afk.finished() {
        return;
    }
    if cfg.seat == acting_host(cfg) {
        if let Ok(w) =
            serde_json::to_string(&WireHexMove { t: "afk".into(), from: 0, to: 0, seat: turn })
        {
            net_send(&w);
        }
        if let Some(p) = cfg.present.get_mut(turn as usize) {
            *p = false;
        }
        dish.skip_flash.push((turn, Timer::from_seconds(1.6, TimerMode::Once)));
    }
}

/// Announces every seat whose blob count just hit zero — the game's biggest
/// event used to pass in silence.
fn eliminations(mut commands: Commands, mut dish: ResMut<Dish>) {
    for p in 0..dish.board.players {
        let alive = dish.board.count(p) > 0;
        let was = dish.alive_prev[p as usize];
        dish.alive_prev[p as usize] = alive;
        if was && !alive {
            sfx("death");
            let e = text(
                &mut commands,
                &format!("SEAT {} CONSUMED", p + 1),
                34.0,
                PLAYER_COLORS[p as usize % 12],
                Vec3::new(BOARD_X, -40.0, 20.0),
            );
            commands.entity(e).insert((Splash(Timer::from_seconds(1.6, TimerMode::Once)), GameTag));
        }
    }
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
                *p = false; // the acting host's bot_turns picks the seat up
            }
            dish.skip_flash.push((ev.seat, Timer::from_seconds(1.4, TimerMode::Once)));
            continue;
        }
        if ev.seat == cfg.seat || dish.over_wait.is_some() {
            continue;
        }
        let Ok(wire) = serde_json::from_str::<WireHexMove>(&ev.data) else { continue };
        // AFK flag from the acting host: the seat goes to the bots on every
        // client at once (only if it is still that seat's turn — a move that
        // squeaked in first wins the race).
        if wire.t == "afk" {
            if ev.seat == acting_host(cfg) && dish.board.turn == wire.seat {
                if let Some(p) = cfg.present.get_mut(wire.seat as usize) {
                    *p = false;
                }
                dish.skip_flash.push((wire.seat, Timer::from_seconds(1.6, TimerMode::Once)));
            }
            continue;
        }
        if wire.t != "mv" {
            continue;
        }
        let turn = dish.board.turn;
        let sender_owns_turn = ev.seat == turn;
        let host_drives_bot = ev.seat == acting_host(cfg)
            && !cfg.present.get(turn as usize).copied().unwrap_or(false);
        if !sender_owns_turn && !host_drives_bot {
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
            end_check_for(&mut dish, my_seat, &mut commands, &fx);
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
    // Skip notes accumulate (several seats can be skipped back-to-back) and
    // display together until each timer runs out.
    for (_, timer) in dish.skip_flash.iter_mut() {
        timer.tick(time.delta());
    }
    dish.skip_flash.retain(|(_, timer)| !timer.finished());
    if !dish.skip_flash.is_empty() {
        let seats: Vec<String> =
            dish.skip_flash.iter().map(|(s, _)| format!("{}", s + 1)).collect();
        let s = format!("SEAT{} {} SKIPPED", if seats.len() > 1 { "S" } else { "" }, seats.join(", "));
        if t.0 != s {
            t.0 = s;
        }
        return;
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
        // Your own standing, always — twelve seats can't all fit the top list.
        let my_seat = net.0.as_ref().map(|c| c.seat).unwrap_or(0);
        let standings = dish.board.standings();
        let my_rank =
            standings.iter().position(|&(p, _)| p == my_seat).unwrap_or(standings.len() - 1) + 1;
        let mine = format!(
            "\nYOU: {}/{} - {} BLOBS",
            my_rank,
            dish.board.players,
            dish.board.count(my_seat)
        );
        // AFK countdown, surfaced once it gets close.
        let afk = match &net.0 {
            Some(_) if is_any_human(&dish, &net, seat) && dish.afk.remaining_secs() < 15.0 => {
                format!("\nAFK CALL IN 0:{:02}", dish.afk.remaining_secs() as u32)
            }
            _ => String::new(),
        };
        format!("SEAT {} ({who})\nTO MOVE\n\nTOP: {}{mine}{afk}", seat + 1, counts.join(" / "))
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
