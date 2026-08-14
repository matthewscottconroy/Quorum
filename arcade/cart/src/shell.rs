//! The cabinet shell every game runs inside: attract screen, the credit
//! mailbox fed by JavaScript, the game-over card, and score reporting back
//! to the page. Games only implement Phase::Playing; the shell owns the rest.

use std::sync::Mutex;

use bevy::input::gamepad::{Gamepad, GamepadButton};
use bevy::input::InputSystem;
use bevy::prelude::*;
use serde::Deserialize;

use crate::retro::{self, text, AMBER, DIM, GREEN, MAGENTA};
use crate::{CabinetConfig, FinalScore, GameTag, NetCfg, NetIn, NetMode, Paused, Phase};

/// Credit mailbox: JavaScript pushes (players, humans) when the user feeds
/// the coin slot; the shell polls it once per frame. A Mutex is overkill on
/// single-threaded wasm but keeps the native build honest.
static CREDIT: Mutex<Option<(u32, u32)>> = Mutex::new(None);

/// Network mailboxes: the page pushes a room-start config and relayed events;
/// shell systems drain them into ECS state each frame.
static NET_START: Mutex<Option<String>> = Mutex::new(None);
static NET_IN: Mutex<Vec<String>> = Mutex::new(Vec::new());

pub fn push_credit(players: u32, humans: u32) {
    *CREDIT.lock().unwrap() = Some((players, humans));
}

fn take_credit() -> Option<(u32, u32)> {
    CREDIT.lock().unwrap().take()
}

pub fn push_net_start(cfg: String) {
    *NET_START.lock().unwrap() = Some(cfg);
}

pub fn push_net_event(msg: String) {
    NET_IN.lock().unwrap().push(msg);
}

/// Editor entry request from the page (arcade_start_editor). Each
/// editor-capable cabinet polls this from Attract/GameOver; only the active
/// cabinet's plugin is compiled in, so one flag serves them all.
static EDITOR_START: Mutex<bool> = Mutex::new(false);

pub fn request_editor() {
    *EDITOR_START.lock().unwrap() = true;
}

pub fn take_editor_start() -> bool {
    let mut p = EDITOR_START.lock().unwrap();
    let v = *p;
    *p = false;
    v
}

/// Editor handshake: poll_editor_start (game side) sets this; the game's
/// setup consumes it to boot into authoring mode instead of a round.
static EDITOR_PENDING: Mutex<bool> = Mutex::new(false);

pub fn mark_editor_pending() {
    *EDITOR_PENDING.lock().unwrap() = true;
}

pub fn take_editor_pending() -> bool {
    let mut p = EDITOR_PENDING.lock().unwrap();
    let v = *p;
    *p = false;
    v
}

/// The round's service-record counters. Games sprinkle `stat("bombs_laid",
/// 1)` wherever deeds happen; the shell flushes the whole map to the page
/// at game over (editor test-plays never reach game over, so they never
/// pollute anyone's record).
static STATS: Mutex<Vec<(&'static str, u64)>> = Mutex::new(Vec::new());

pub fn stat(name: &'static str, delta: u64) {
    if delta == 0 {
        return;
    }
    let mut s = STATS.lock().unwrap();
    if let Some(entry) = s.iter_mut().find(|(n, _)| *n == name) {
        entry.1 += delta;
    } else if s.len() < 64 {
        s.push((name, delta));
    }
}

pub fn reset_stats() {
    STATS.lock().unwrap().clear();
}

/// Serializes and clears the round's counters; None when nothing happened.
fn take_stats() -> Option<String> {
    let mut s = STATS.lock().unwrap();
    if s.is_empty() {
        return None;
    }
    let body: Vec<String> = s.iter().map(|(n, v)| format!("\"{n}\":{v}")).collect();
    s.clear();
    Some(format!("{{{}}}", body.join(",")))
}

/// Reports the round's counters to the page (window.__arcadeStats), which
/// POSTs them to the service-record ledger.
fn report_stats() {
    let Some(json) = take_stats() else { return };
    #[cfg(target_arch = "wasm32")]
    {
        use wasm_bindgen::JsCast;
        if let Ok(f) = js_sys::Reflect::get(&js_sys::global(), &"__arcadeStats".into()) {
            if let Some(f) = f.dyn_ref::<js_sys::Function>() {
                let _ = f.call1(&wasm_bindgen::JsValue::NULL, &json.into());
            }
        }
    }
    #[cfg(not(target_arch = "wasm32"))]
    {
        let _ = json;
    }
}

/// Hands a compiled level document to the page (window.__arcadeSaveLevel),
/// which prompts for a name and POSTs it to the community shelf.
pub fn save_level(json: &str) {
    #[cfg(target_arch = "wasm32")]
    {
        use wasm_bindgen::JsCast;
        if let Ok(f) = js_sys::Reflect::get(&js_sys::global(), &"__arcadeSaveLevel".into()) {
            if let Some(f) = f.dyn_ref::<js_sys::Function>() {
                let _ = f.call1(&wasm_bindgen::JsValue::NULL, &json.into());
            }
        }
    }
    #[cfg(not(target_arch = "wasm32"))]
    {
        let _ = json;
    }
}

/// Fires a named chip-synth sound effect. The page owns the WebAudio
/// context (it can only start after a user gesture, which the coin click
/// provides); missing handler or muted page is a silent no-op.
pub fn sfx(name: &str) {
    #[cfg(target_arch = "wasm32")]
    {
        use wasm_bindgen::JsCast;
        if let Ok(f) = js_sys::Reflect::get(&js_sys::global(), &"__arcadeSfx".into()) {
            if let Some(f) = f.dyn_ref::<js_sys::Function>() {
                let _ = f.call1(&wasm_bindgen::JsValue::NULL, &name.into());
            }
        }
    }
    #[cfg(not(target_arch = "wasm32"))]
    {
        let _ = name;
    }
}

/// Asks the page to send a raw room op ("deal" / "street" / "reveal") — the
/// hold 'em dealer verbs, paced by the acting host's cartridge.
pub fn net_op(op: &str) {
    #[cfg(target_arch = "wasm32")]
    {
        use wasm_bindgen::JsCast;
        if let Ok(f) = js_sys::Reflect::get(&js_sys::global(), &"__arcadeNetOp".into()) {
            if let Some(f) = f.dyn_ref::<js_sys::Function>() {
                let _ = f.call1(&wasm_bindgen::JsValue::NULL, &op.into());
            }
        }
    }
    #[cfg(not(target_arch = "wasm32"))]
    {
        let _ = op;
    }
}

/// Sends a game payload to the room (host relays state; players relay moves).
pub fn net_send(payload: &str) {
    #[cfg(target_arch = "wasm32")]
    {
        use wasm_bindgen::JsCast;
        if let Ok(f) = js_sys::Reflect::get(&js_sys::global(), &"__arcadeNetSend".into()) {
            if let Some(f) = f.dyn_ref::<js_sys::Function>() {
                let _ = f.call1(&wasm_bindgen::JsValue::NULL, &payload.into());
            }
        }
    }
    #[cfg(not(target_arch = "wasm32"))]
    {
        let _ = payload;
    }
}

/// Which cabinet did the page ask for? (window.__ARCADE_GAME)
pub fn selected_game() -> String {
    #[cfg(target_arch = "wasm32")]
    {
        js_sys::Reflect::get(&js_sys::global(), &"__ARCADE_GAME".into())
            .ok()
            .and_then(|v| v.as_string())
            .unwrap_or_else(|| "brickfall".to_string())
    }
    #[cfg(not(target_arch = "wasm32"))]
    {
        std::env::var("ARCADE_GAME").unwrap_or_else(|_| "brickfall".to_string())
    }
}

/// Reports the final score to the page (window.__arcadeScore).
pub fn report_score(score: u32) {
    #[cfg(target_arch = "wasm32")]
    {
        use wasm_bindgen::JsCast;
        if let Ok(f) = js_sys::Reflect::get(&js_sys::global(), &"__arcadeScore".into()) {
            if let Some(f) = f.dyn_ref::<js_sys::Function>() {
                let _ = f.call1(&wasm_bindgen::JsValue::NULL, &(score as f64).into());
            }
        }
    }
    #[cfg(not(target_arch = "wasm32"))]
    {
        println!("final score: {score}");
    }
}

#[derive(Component)]
struct ShellTag;

#[derive(Component)]
struct Blink(Timer);

/// Attract/game-over furniture and phase transitions.
/// Gamepads become keyboards: every cabinet already speaks arrows/WASD/
/// Space/Enter, so the shell translates pads into those keys. One pad
/// drives BOTH key clusters (any solo cabinet just works); with two pads,
/// pad 1 takes the P1 cluster (WASD/Space/E) and pad 2 the P2 cluster
/// (arrows/Enter/R-Shift) — local versus, two chairs, no keyboard sharing.
/// Extra buttons: East=E (utility), North=R (roll), West=B (buy).
fn gamepad_keys(
    pads: Query<(Entity, &Gamepad)>,
    mut order: Local<Vec<Entity>>,
    mut injected: Local<Vec<KeyCode>>,
    mut keys: ResMut<ButtonInput<KeyCode>>,
) {
    for (e, _) in pads.iter() {
        if !order.contains(&e) {
            order.push(e);
        }
    }
    order.retain(|e| pads.get(*e).is_ok());
    let mut want: Vec<KeyCode> = Vec::new();
    let solo = order.len() <= 1;
    for (idx, e) in order.iter().enumerate() {
        let Ok((_, pad)) = pads.get(*e) else { continue };
        let stick = pad.left_stick();
        let up = pad.pressed(GamepadButton::DPadUp) || stick.y > 0.5;
        let down = pad.pressed(GamepadButton::DPadDown) || stick.y < -0.5;
        let left = pad.pressed(GamepadButton::DPadLeft) || stick.x < -0.5;
        let right = pad.pressed(GamepadButton::DPadRight) || stick.x > 0.5;
        let south = pad.pressed(GamepadButton::South);
        let east = pad.pressed(GamepadButton::East);
        let north = pad.pressed(GamepadButton::North);
        let west = pad.pressed(GamepadButton::West);
        let start = pad.pressed(GamepadButton::Start);
        let mut push = |on: bool, k: KeyCode| {
            if on && !want.contains(&k) {
                want.push(k);
            }
        };
        if idx == 0 {
            push(up, KeyCode::KeyW);
            push(down, KeyCode::KeyS);
            push(left, KeyCode::KeyA);
            push(right, KeyCode::KeyD);
            push(south, KeyCode::Space);
            push(east, KeyCode::KeyE);
            push(north, KeyCode::KeyR);
            push(west, KeyCode::KeyB);
            push(start, KeyCode::Enter);
            if solo {
                push(up, KeyCode::ArrowUp);
                push(down, KeyCode::ArrowDown);
                push(left, KeyCode::ArrowLeft);
                push(right, KeyCode::ArrowRight);
            }
        } else {
            push(up, KeyCode::ArrowUp);
            push(down, KeyCode::ArrowDown);
            push(left, KeyCode::ArrowLeft);
            push(right, KeyCode::ArrowRight);
            push(south, KeyCode::Enter);
            push(east, KeyCode::ShiftRight);
        }
    }
    // Apply the delta between what we injected last frame and now.
    let released: Vec<KeyCode> = injected.iter().copied().filter(|k| !want.contains(k)).collect();
    for k in released {
        keys.release(k);
    }
    for k in &want {
        if !keys.pressed(*k) {
            keys.press(*k);
        }
    }
    *injected = want;
}

pub struct ShellPlugin {
    pub title: &'static str,
    pub blurb: &'static [&'static str],
}

#[derive(Resource)]
struct CabinetCard {
    title: &'static str,
    blurb: &'static [&'static str],
}

impl Plugin for ShellPlugin {
    fn build(&self, app: &mut App) {
        app.insert_resource(CabinetCard { title: self.title, blurb: self.blurb })
            .init_resource::<RoundClock>()
            .add_systems(Startup, boot)
            .add_systems(OnEnter(Phase::Attract), attract_in)
            .add_systems(OnExit(Phase::Attract), shell_out)
            .add_systems(PreUpdate, gamepad_keys.after(InputSystem))
            .add_systems(OnEnter(Phase::GameOver), (game_over_in, clear_pause))
            .add_systems(OnEnter(Phase::Playing), (clear_pause, round_begin))
            .add_systems(OnExit(Phase::GameOver), (shell_out, clear_game_entities))
            .add_systems(
                Update,
                (
                    blink,
                    poll_credit.run_if(in_state(Phase::Attract).or(in_state(Phase::GameOver))),
                    poll_net_start.run_if(in_state(Phase::Attract).or(in_state(Phase::GameOver))),
                    pump_net_events.run_if(in_state(Phase::Playing)),
                    toggle_pause.run_if(in_state(Phase::Playing)),
                ),
            );
    }
}

#[derive(Component)]
struct PauseTag;

/// Esc pauses local rounds. Networked rounds never pause: the host's
/// simulation and the relay wait for nobody.
fn toggle_pause(
    keys: Res<ButtonInput<KeyCode>>,
    net: Res<NetMode>,
    mut paused: ResMut<Paused>,
    mut commands: Commands,
    overlay: Query<Entity, With<PauseTag>>,
) {
    if !keys.just_pressed(KeyCode::Escape) || net.0.is_some() {
        return;
    }
    paused.0 = !paused.0;
    sfx("pause");
    if paused.0 {
        commands.spawn((
            Sprite {
                color: Color::srgba(0.0, 0.0, 0.0, 0.6),
                custom_size: Some(Vec2::new(retro::SCREEN_W, retro::SCREEN_H)),
                ..default()
            },
            Transform::from_xyz(0.0, 0.0, 60.0),
            PauseTag,
        ));
        let t = text(&mut commands, "PAUSED", 48.0, AMBER, Vec3::new(0.0, 20.0, 61.0));
        commands.entity(t).insert(PauseTag);
        let hint = text(&mut commands, "ESC TO RESUME", 20.0, DIM, Vec3::new(0.0, -40.0, 61.0));
        commands.entity(hint).insert(PauseTag);
    } else {
        for e in &overlay {
            commands.entity(e).despawn();
        }
    }
}

/// Wire format of the page's room-start config.
#[derive(Deserialize)]
struct NetStartMsg {
    seat: u8,
    seats: u8,
    #[serde(default)]
    present: Vec<u8>,
}

/// Wire format of a relayed room event.
#[derive(Deserialize)]
struct NetInMsg {
    seat: u8,
    #[serde(default)]
    left: bool,
    #[serde(default)]
    data: serde_json::Value,
}

fn poll_net_start(
    mut config: ResMut<CabinetConfig>,
    mut net: ResMut<NetMode>,
    mut next: ResMut<NextState<Phase>>,
) {
    let Some(raw) = NET_START.lock().unwrap().take() else { return };
    let Ok(msg) = serde_json::from_str::<NetStartMsg>(&raw) else { return };
    let seats = msg.seats.clamp(2, 12);
    let mut present = vec![false; seats as usize];
    for s in msg.present {
        if (s as usize) < present.len() {
            present[s as usize] = true;
        }
    }
    net.0 = Some(NetCfg { seat: msg.seat.min(seats - 1), seats, present });
    config.players = seats as u32;
    config.humans = 1;
    NET_IN.lock().unwrap().clear(); // no stale traffic from a previous room
    next.set(Phase::Playing);
}

fn pump_net_events(mut writer: EventWriter<NetIn>) {
    let drained: Vec<String> = std::mem::take(NET_IN.lock().unwrap().as_mut());
    for raw in drained {
        let Ok(msg) = serde_json::from_str::<NetInMsg>(&raw) else { continue };
        writer.write(NetIn {
            seat: msg.seat,
            left: msg.left,
            data: if msg.left { String::new() } else { msg.data.to_string() },
        });
    }
}

fn boot(mut commands: Commands) {
    commands.spawn(Camera2d);
}

fn attract_in(mut commands: Commands, card: Res<CabinetCard>) {
    let title = text(&mut commands, card.title, 64.0, GREEN, Vec3::new(0.0, 160.0, 5.0));
    commands.entity(title).insert(ShellTag);
    for (i, line) in card.blurb.iter().enumerate() {
        let e = text(
            &mut commands,
            line,
            20.0,
            DIM,
            Vec3::new(0.0, 60.0 - 30.0 * i as f32, 5.0),
        );
        commands.entity(e).insert(ShellTag);
    }
    let coin = text(&mut commands, "INSERT CREDIT", 30.0, AMBER, Vec3::new(0.0, -180.0, 5.0));
    commands
        .entity(coin)
        .insert((ShellTag, Blink(Timer::from_seconds(0.6, TimerMode::Repeating))));
}

/// Wall-clock anchor for the round, feeding the seconds_played counter.
#[derive(Resource, Default)]
struct RoundClock(f32);

fn round_begin(time: Res<Time>, mut clock: ResMut<RoundClock>) {
    reset_stats();
    clock.0 = time.elapsed_secs();
}

fn game_over_in(
    mut commands: Commands,
    score: Res<FinalScore>,
    time: Res<Time>,
    clock: Res<RoundClock>,
    mut banner: ResMut<crate::EndBanner>,
) {
    sfx("over");
    report_score(score.0);
    stat("seconds_played", (time.elapsed_secs() - clock.0).max(0.0) as u64);
    stat("rounds_finished", 1);
    report_stats();
    // Dim scrim so the final board stays faintly visible underneath.
    commands.spawn((
        Sprite {
            color: Color::srgba(0.0, 0.0, 0.0, 0.72),
            custom_size: Some(Vec2::new(retro::SCREEN_W, retro::SCREEN_H)),
            ..default()
        },
        Transform::from_xyz(0.0, 0.0, 40.0),
        ShellTag,
    ));
    let (label, color) = match banner.0.take() {
        Some(win) => (win, GREEN),
        None => ("GAME OVER".to_string(), MAGENTA),
    };
    let size = if label.len() > 14 { 34.0 } else { 56.0 };
    let over = text(&mut commands, &label, size, color, Vec3::new(0.0, 90.0, 50.0));
    commands.entity(over).insert(ShellTag);
    let sc = text(
        &mut commands,
        &format!("SCORE {}", score.0),
        34.0,
        GREEN,
        Vec3::new(0.0, 20.0, 50.0),
    );
    commands.entity(sc).insert(ShellTag);
    let coin = text(
        &mut commands,
        "INSERT CREDIT TO PLAY AGAIN",
        24.0,
        AMBER,
        Vec3::new(0.0, -120.0, 50.0),
    );
    commands
        .entity(coin)
        .insert((ShellTag, Blink(Timer::from_seconds(0.6, TimerMode::Repeating))));
}

fn shell_out(mut commands: Commands, q: Query<Entity, With<ShellTag>>) {
    for e in &q {
        commands.entity(e).despawn();
    }
}

/// Between rounds, every entity a game spawned is swept before the next
/// round's OnEnter(Playing) setup runs.
fn clear_game_entities(mut commands: Commands, q: Query<Entity, With<GameTag>>) {
    for e in &q {
        commands.entity(e).despawn();
    }
}

fn blink(time: Res<Time>, mut q: Query<(&mut Blink, &mut Visibility)>) {
    for (mut b, mut v) in &mut q {
        if b.0.tick(time.delta()).just_finished() {
            *v = match *v {
                Visibility::Hidden => Visibility::Inherited,
                _ => Visibility::Hidden,
            };
        }
    }
}

fn poll_credit(
    mut config: ResMut<CabinetConfig>,
    mut net: ResMut<NetMode>,
    mut next: ResMut<NextState<Phase>>,
) {
    if let Some((players, humans)) = take_credit() {
        net.0 = None; // the plain coin slot always starts a LOCAL round
        config.players = players.clamp(1, 12);
        config.humans = humans.clamp(1, config.players);
        sfx("coin");
        next.set(Phase::Playing);
    }
}

/// Any phase change unpauses and clears the overlay (a game over while
/// paused must not leave a stuck scrim).
fn clear_pause(
    mut paused: ResMut<Paused>,
    mut commands: Commands,
    overlay: Query<Entity, With<PauseTag>>,
) {
    paused.0 = false;
    for e in &overlay {
        commands.entity(e).despawn();
    }
}
