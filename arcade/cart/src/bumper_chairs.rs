//! BUMPER CHAIRS — a top-down office-chair battle in the parking garage,
//! built from scratch: original name, art, arenas, and items. Genre
//! mechanics only: drive, grab item boxes, pop the other chairs' balloons.
//! Three balloons each; the last chair still rolling takes the floor.
//!
//! Items: STAPLER (straight shot, bounces once), TRIPLE STAPLER, a spilled
//! COFFEE puddle (spins whoever rolls through it), and ESPRESSO (a boost).
//! Online (2-12): every client owns its chair and streams position; shots
//! spawn everywhere, but only your OWN chair decides it was hit — victim-
//! authoritative balloons, shooter-credited pops. Local play fills empty
//! seats with bot chairs. The ARENA EDITOR paints walls, boxes, and spawn
//! points; the host's arena ships to the whole room at start.

use bevy::prelude::*;
use serde::{Deserialize, Serialize};

use crate::retro::{popup, text, PLAYER_COLORS, AMBER, CYAN, DIM, GREEN, MAGENTA, RED, WHITE};
use crate::shell::{net_send, sfx, stat};
use crate::{CabinetConfig, FinalScore, GameTag, NetIn, NetMode, Phase};

pub const BLURB: &[&str] = &[
    "THREE BALLOONS. ONE GARAGE. NO BRAKES WORTH USING.",
    "UP DRIVES - LEFT/RIGHT STEER - SPACE USES THE ITEM IN THE BOX (TOP LEFT).",
    "CRATES HOLD: STAPLER, TRIPLE, SMART, COFFEE, ESPRESSO, OVERTIME,",
    "EJECTOR, BLACKOUT, AND THE GHOST VENDOR. LAST CHAIR ROLLING WINS.",
];

const AW: usize = 24; // arena cells
const AH: usize = 18;
const ACELL: f32 = 30.0;
/// Everything a supply crate can hold — the full battle kit:
/// 0 STAPLER (straight, bounces once)     4 SMART STAPLER (seeks a rival)
/// 1 COFFEE (puddle: spins whoever)       5 OVERTIME (untouchable rush)
/// 2 ESPRESSO (speed burst)               6 EJECTOR (hop a wall)
/// 3 TRIPLE STAPLER                       7 BLACKOUT (spins every rival)
///                                        8 GHOST VENDOR (steals a balloon)
fn item_icon(k: u8) -> (Color, &'static str) {
    match k {
        0 => (CYAN, "STAPLER"),
        1 => (Color::srgb(0.5, 0.32, 0.12), "COFFEE"),
        2 => (GREEN, "ESPRESSO"),
        3 => (CYAN, "TRIPLE"),
        4 => (MAGENTA, "SMART"),
        5 => (AMBER, "OVERTIME"),
        6 => (WHITE, "EJECTOR"),
        7 => (Color::srgb(0.45, 0.5, 1.0), "BLACKOUT"),
        8 => (Color::srgb(0.85, 0.9, 1.0), "GHOST"),
        _ => (DIM, ""),
    }
}

const TURN_RATE: f32 = 3.4;
const ACCEL: f32 = 310.0;
const MAX_SPEED: f32 = 300.0;

const DEFAULT_ARENA: [&str; AH] = [
    "########################",
    "#s.......B......#....s.#",
    "#..##........#..#..##..#",
    "#..##..s.....#.....##..#",
    "#......###...#..B......#",
    "#.B........s.#####..##.#",
    "#....#..............##.#",
    "#....#...####..........#",
    "#s...#...#..#....s...B.#",
    "#....B...#..#..........#",
    "#....#...####...#####..#",
    "#....#....... .........#",
    "#..#####....B....#...s.#",
    "#.......s........#.....#",
    "#..B.....#####...#..B..#",
    "#..##....#...........s.#",
    "#s.......#....B........#",
    "########################",
];

fn cell_at(rows: &[String], x: i32, y: i32) -> char {
    if x < 0 || y < 0 || x >= AW as i32 || y >= AH as i32 {
        return '#';
    }
    rows[y as usize].chars().nth(x as usize).unwrap_or('#')
}

fn world_of(x: f32, y: f32) -> Vec2 {
    Vec2::new(x * ACELL - 360.0, 250.0 - y * ACELL)
}

// ── the mode-7 style view ────────────────────────────────────────────────
// The garage is simulated top-down (nothing about physics or the wire
// changed) but DRAWN from behind your chair: everything in the world is a
// billboard projected each frame from your position and heading, the way
// the 16-bit kart racers faked it. Camera sits one cell off the floor.

const HORIZON: f32 = 70.0;
const FOCAL: f32 = 300.0;

/// A projected world object. `pos` is in cell units; sizes are in cells.
#[derive(Component)]
struct Bill {
    pos: Vec2,
    w: f32,
    hh: f32,
    alt: f32,   // altitude of the sprite's base above the floor
    flat: bool, // ground decal (puddles, floor dots): squashed, on the deck
    base: Color,
}

/// Screen-fixed view furniture: sky, floor, minimap, your own chair rig.
#[derive(Component)]
struct FixedView;

fn bill(commands: &mut Commands, pos: Vec2, w: f32, hh: f32, alt: f32, flat: bool, base: Color) -> Entity {
    commands
        .spawn((
            Sprite { color: base, custom_size: Some(Vec2::splat(2.0)), ..default() },
            Transform::from_xyz(0.0, 0.0, 1.0),
            Visibility::Hidden,
            Bill { pos, w, hh, alt, flat, base },
            GameTag,
        ))
        .id()
}

fn mini_xy(pos: Vec2) -> Vec2 {
    Vec2::new(232.0 + pos.x * 5.0, 280.0 - pos.y * 5.0)
}

struct Chair {
    seat: usize,
    human: bool,   // this machine's keyboard
    remote: bool,  // a live human elsewhere
    pos: Vec2,     // in CELL units
    ang: f32,
    mdir: f32, // motion direction: chases ang; the gap between them is drift
    speed: f32,
    balloons: i32,
    item: Option<u8>,
    spin_t: f32,
    boost_t: f32,
    inv_t: f32,
    star_t: f32, // OVERTIME: untouchable, fast, pops on contact
    pops: u32,
    think: f32,
    ent: Entity,        // seat cushion billboard
    back_ent: Entity,   // backrest billboard
    base_ent: Entity,   // wheel-hub billboard
    balloon_ent: Entity,
}

struct Shot {
    pos: Vec2,
    vel: Vec2,
    owner: usize,
    bounces: i32,
    ttl: f32,
    homing: bool,
    ent: Entity,
}

struct Puddle {
    pos: Vec2,
    ttl: f32,
    owner: usize,
    ent: Entity,
}

struct Box_ {
    cell: (usize, usize),
    up_in: f32, // respawn countdown; 0 = available
    ent: Entity,
}

#[derive(Resource)]
struct Garage {
    rows: Vec<String>,
    chairs: Vec<Chair>,
    shots: Vec<Shot>,
    puddles: Vec<Puddle>,
    boxes: Vec<Box_>,
    my_seat: usize,
    net: bool,
    clock: f32,
    pos_t: f32,
    score: u32,
    over: Option<Timer>,
    result: String,
    mini: Vec<Entity>, // minimap dots, parallel to chairs
    rig: Entity,       // your own chair, fixed at the bottom of the screen
    my_balloons: Vec<Entity>, // balloons drawn above your rig, up to five
    slot_icon: Entity, // the item box's window
    slot_label: Entity,
    slot_spin: f32, // the slot machine wobble after grabbing a crate
    start_t: f32,   // the 3-2-1-GO gate at the horn
    bump_t: f32,    // wall-thud sound cooldown
    sky: Vec<(Entity, f32)>, // parallax skyline: entity + angular phase
    veil: Entity,   // full-screen flash for item drama
    fx_t: f32,
    fx_color: Color,
    hop_t: f32,     // your EJECTOR arc
    i_won: bool,
    gear: i8, // last engine-pitch step sent to the synth
}

#[derive(Component)]
struct Hud;

#[derive(Serialize, Deserialize)]
struct WPos {
    t: String, // "pos"
    x: f32,
    y: f32,
    a: f32,
    b: i32,
    #[serde(default)]
    s: bool, // OVERTIME running
}

#[derive(Serialize, Deserialize)]
struct WFire {
    t: String, // "fire": a shot or a puddle everyone should spawn
    k: u8,     // 0 stapler, 1 puddle
    x: f32,
    y: f32,
    a: f32,
}

#[derive(Serialize, Deserialize)]
struct WPop {
    t: String, // "pop": MY balloon went; credit `by`
    by: u8,
    #[serde(default)]
    g: bool, // a GHOST theft: `by` gains the balloon instead of a pop
}

#[derive(Serialize, Deserialize)]
struct WBox {
    t: String, // "box": I claimed box #i
    i: u32,
}

#[derive(Serialize, Deserialize)]
struct WLevel {
    t: String, // "lv"
    rows: Vec<String>,
}

/// The arena editor (same shape as the office editor next door).
#[derive(Resource)]
struct ArenaEditor {
    active: bool,
    testing: bool,
    rows: Vec<String>,
    brush: char,
}

fn editor_off(editor: Option<Res<ArenaEditor>>) -> bool {
    editor.map(|e| !e.active).unwrap_or(true)
}

#[derive(serde::Serialize, serde::Deserialize, Clone)]
struct ArenaDoc {
    v: u32,
    #[serde(default)]
    name: String,
    rows: Vec<String>,
}

fn page_level() -> Option<Vec<String>> {
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
        if serde_json::from_str::<BlankRef>(&raw).map(|b| b.blank).unwrap_or(false) {
            let mut rows: Vec<String> = (0..AH)
                .map(|y| {
                    (0..AW)
                        .map(|x| if x == 0 || y == 0 || x == AW - 1 || y == AH - 1 { '#' } else { '.' })
                        .collect()
                })
                .collect();
            for (x, y) in [(2usize, 2usize), (21, 15), (21, 2), (2, 15)] {
                rows[y].replace_range(x..x + 1, "s");
            }
            return Some(rows);
        }
        if let Ok(doc) = serde_json::from_str::<ArenaDoc>(&raw) {
            if doc.rows.len() == AH && doc.rows.iter().all(|r| r.chars().count() == AW) {
                return Some(doc.rows);
            }
        }
    }
    None
}

pub struct ChairsPlugin;

impl Plugin for ChairsPlugin {
    fn build(&self, app: &mut App) {
        app.add_systems(OnEnter(Phase::Playing), setup)
            .add_systems(
                Update,
                poll_editor_start.run_if(in_state(Phase::Attract).or(in_state(Phase::GameOver))),
            )
            .add_systems(
                Update,
                editor_update.run_if(in_state(Phase::Playing)).run_if(crate::unpaused),
            )
            .add_systems(
                Update,
                (net_apply, drive, bots, shots_fly, hazards, boxes_spin, hud, endgame)
                    .chain()
                    .run_if(in_state(Phase::Playing))
                    .run_if(crate::unpaused)
                    .run_if(editor_off),
            )
            .add_systems(
                Update,
                project
                    .after(hud)
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
        cfg.players = 4;
        cfg.humans = 1;
        crate::shell::mark_editor_pending();
        next.set(Phase::Playing);
    }
}

/// A rival chair reads as a chair: dark wheel hub, colored seat cushion,
/// taller backrest, balloons floating above the lot.
fn spawn_chair(commands: &mut Commands, seat: usize, at: Vec2) -> (Entity, Entity, Entity, Entity) {
    let color = PLAYER_COLORS[seat % 12];
    let base_ent = bill(commands, at, 0.62, 0.16, 0.0, false, Color::srgb(0.13, 0.13, 0.17));
    let ent = bill(commands, at, 0.86, 0.26, 0.18, false, color);
    let back_ent = bill(commands, at, 0.66, 0.62, 0.46, false, dim_color(color));
    let balloon_ent = bill(commands, at, 0.6, 0.22, 1.28, false, color.with_alpha(0.95));
    (ent, back_ent, base_ent, balloon_ent)
}

fn dim_color(c: Color) -> Color {
    let s = c.to_srgba();
    Color::srgb(s.red * 0.68, s.green * 0.68, s.blue * 0.68)
}

fn wrap_pi(mut a: f32) -> f32 {
    while a > std::f32::consts::PI {
        a -= std::f32::consts::TAU;
    }
    while a < -std::f32::consts::PI {
        a += std::f32::consts::TAU;
    }
    a
}

fn setup(
    mut commands: Commands,
    config: Res<CabinetConfig>,
    net: Res<NetMode>,
    existing_editor: Option<ResMut<ArenaEditor>>,
) {
    let editor_mode = crate::shell::take_editor_pending();
    let base: Vec<String> = DEFAULT_ARENA.iter().map(|r| r.to_string()).collect();
    let rows = page_level().unwrap_or(base);
    match (existing_editor, editor_mode) {
        (Some(mut e), true) => {
            e.active = true;
            e.testing = false;
            e.rows = rows.clone();
        }
        (Some(mut e), false) => {
            e.active = false;
            e.testing = false;
        }
        (None, editing) => {
            commands.insert_resource(ArenaEditor {
                active: editing,
                testing: false,
                rows: rows.clone(),
                brush: '#',
            });
        }
    }

    // Ceiling and floor planes of the projected view.
    for (y0, y1, color, z) in [
        (HORIZON, 320.0, Color::srgb(0.05, 0.06, 0.10), 0.02),
        // The base plane matches the darker asphalt so tile seams vanish.
        (-320.0, HORIZON, Color::srgb(0.115, 0.11, 0.10), 0.03),
    ] {
        let e = commands
            .spawn((
                Sprite { color, custom_size: Some(Vec2::new(760.0, y1 - y0)), ..default() },
                Transform::from_xyz(0.0, (y0 + y1) / 2.0, z),
                Visibility::Hidden,
                FixedView,
                GameTag,
            ))
            .id();
        let _ = e;
    }
    // Walls, boxes, and floor markings become projected billboards.
    let mut spawns: Vec<Vec2> = Vec::new();
    let mut boxes = Vec::new();
    for y in 0..AH {
        for x in 0..AW {
            let ch = cell_at(&rows, x as i32, y as i32);
            let center = Vec2::new(x as f32 + 0.5, y as f32 + 0.5);
            match ch {
                '#' => {
                    // Waist-high bumpers, kart-battle style: you can see the
                    // whole arena over them, they just won't let you through.
                    // Walls wear hazard paint — nothing on the FLOOR is
                    // ever this color, so a barrier reads as a barrier.
                    let tone = if (x + y) % 2 == 0 {
                        Color::srgb(0.85, 0.66, 0.16)
                    } else {
                        Color::srgb(0.32, 0.30, 0.26)
                    };
                    bill(&mut commands, center, 1.08, 0.5, 0.0, false, tone);
                }
                'B' => {
                    let ent = bill(&mut commands, center, 0.55, 0.55, 0.30, false, AMBER);
                    boxes.push(Box_ { cell: (x, y), up_in: 0.0, ent });
                }
                's' => spawns.push(center),
                _ => {}
            }
            // The mode-7 checkerboard: every open cell is a floor tile, so
            // the ground streams under you the way a kart track should.
            if ch != '#' {
                // Asphalt: warm, flat, unmistakably UNDER you.
                let tone = if (x + y) % 2 == 0 {
                    Color::srgb(0.165, 0.155, 0.135)
                } else {
                    Color::srgb(0.105, 0.10, 0.09)
                };
                let e = bill(&mut commands, center, 1.02, 1.0, -0.1, true, tone);
                let _ = e;
            }
        }
    }
    // Minimap (top right): the arena at five pixels a cell.
    commands.spawn((
        Sprite { color: Color::srgba(0.0, 0.0, 0.0, 0.55), custom_size: Some(Vec2::new(132.0, 102.0)), ..default() },
        Transform::from_xyz(292.0, 235.0, 28.0),
        Visibility::Hidden,
        FixedView,
        GameTag,
    ));
    for y in 0..AH {
        for x in 0..AW {
            if cell_at(&rows, x as i32, y as i32) == '#' {
                let mp = mini_xy(Vec2::new(x as f32 + 0.5, y as f32 + 0.5));
                commands.spawn((
                    Sprite { color: Color::srgba(0.5, 0.52, 0.6, 0.8), custom_size: Some(Vec2::splat(5.0)), ..default() },
                    Transform::from_xyz(mp.x, mp.y, 28.2),
                    Visibility::Hidden,
                    FixedView,
                    GameTag,
                ));
            }
        }
    }
    while spawns.len() < 12 {
        spawns.push(Vec2::new(2.5 + (spawns.len() as f32 * 1.7) % 19.0, 2.5 + (spawns.len() as f32 * 2.3) % 13.0));
    }

    let my_seat = net.0.as_ref().map(|c| c.seat as usize).unwrap_or(0);
    let is_net = net.0.is_some() && !editor_mode;
    let players = if is_net {
        net.0.as_ref().map(|c| c.seats as usize).unwrap_or(2)
    } else {
        (config.players.clamp(2, 12)) as usize
    };
    let mut chairs = Vec::new();
    for seat in 0..players {
        let (human, remote) = if is_net {
            let present = net.0.as_ref().map(|c| c.present.get(seat).copied().unwrap_or(false)).unwrap_or(false);
            (seat == my_seat, present && seat != my_seat)
        } else {
            (seat == 0, false)
        };
        // Online, absent seats simply don't exist — nobody drives them.
        if is_net && !human && !remote {
            continue;
        }
        let at = spawns[seat % spawns.len()];
        let (ent, back_ent, base_ent, balloon_ent) = spawn_chair(&mut commands, seat, at);
        chairs.push(Chair {
            seat,
            human,
            remote,
            pos: at,
            ang: 0.0,
            mdir: 0.0,
            speed: 0.0,
            balloons: 3,
            item: None,
            spin_t: 0.0,
            boost_t: 0.0,
            inv_t: 0.0,
            star_t: 0.0,
            pops: 0,
            think: seat as f32 * 0.1,
            ent,
            back_ent,
            base_ent,
            balloon_ent,
        });
    }
    if is_net {
        if net.0.as_ref().map(|c| c.is_host()).unwrap_or(false) {
            if let Ok(w) = serde_json::to_string(&WLevel { t: "lv".into(), rows: rows.clone() }) {
                net_send(&w);
            }
        }
    }
    let me = if is_net { my_seat } else { 0 };
    // Minimap dots, one per chair; yours is bigger.
    let mut mini = Vec::new();
    for c in &chairs {
        let size = if c.seat == me { 8.0 } else { 6.0 };
        let e = commands
            .spawn((
                Sprite { color: PLAYER_COLORS[c.seat % 12], custom_size: Some(Vec2::splat(size)), ..default() },
                Transform::from_xyz(292.0, 235.0, 28.5),
                Visibility::Hidden,
                FixedView,
                GameTag,
            ))
            .id();
        mini.push(e);
    }
    // Your own chair, seen from behind, parked at the bottom of the screen.
    let my_color = PLAYER_COLORS[me % 12];
    let rig = commands
        .spawn((
            Sprite { color: my_color, custom_size: Some(Vec2::new(66.0, 46.0)), ..default() },
            Transform::from_xyz(0.0, -238.0, 24.0),
            Visibility::Hidden,
            FixedView,
            GameTag,
        ))
        .with_children(|kid| {
            kid.spawn((
                Sprite { color: WHITE.with_alpha(0.75), custom_size: Some(Vec2::new(58.0, 8.0)), ..default() },
                Transform::from_xyz(0.0, 27.0, 0.1),
            ));
            kid.spawn((
                Sprite { color: Color::srgb(0.12, 0.12, 0.16), custom_size: Some(Vec2::new(30.0, 12.0)), ..default() },
                Transform::from_xyz(0.0, -29.0, 0.1),
            ));
        })
        .id();
    // YOUR balloons, big and countable, floating over your chair. Ghost
    // vendors can take you to five.
    let mut my_balloons = Vec::new();
    for i in 0..5 {
        let x = (i as f32 - 2.0) * 24.0;
        let e = commands
            .spawn((
                Sprite { color: my_color.with_alpha(0.95), custom_size: Some(Vec2::new(18.0, 22.0)), ..default() },
                Transform::from_xyz(x, -178.0, 24.5),
                Visibility::Hidden,
                FixedView,
                GameTag,
            ))
            .with_children(|kid| {
                kid.spawn((
                    Sprite { color: WHITE.with_alpha(0.5), custom_size: Some(Vec2::new(2.0, 14.0)), ..default() },
                    Transform::from_xyz(0.0, -17.0, -0.1),
                ));
            })
            .id();
        my_balloons.push(e);
    }
    // The item box, slot-machine style, top left.
    commands.spawn((
        Sprite { color: Color::srgba(0.0, 0.0, 0.0, 0.6), custom_size: Some(Vec2::new(62.0, 62.0)), ..default() },
        Transform::from_xyz(-322.0, 244.0, 27.0),
        Visibility::Hidden,
        FixedView,
        GameTag,
    ));
    commands.spawn((
        Sprite { color: WHITE.with_alpha(0.25), custom_size: Some(Vec2::new(68.0, 68.0)), ..default() },
        Transform::from_xyz(-322.0, 244.0, 26.9),
        Visibility::Hidden,
        FixedView,
        GameTag,
    ));
    let slot_icon = commands
        .spawn((
            Sprite { color: Color::NONE, custom_size: Some(Vec2::new(38.0, 38.0)), ..default() },
            Transform::from_xyz(-322.0, 244.0, 27.2),
            Visibility::Hidden,
            FixedView,
            GameTag,
        ))
        .id();
    let slot_label = text(&mut commands, "", 11.0, WHITE, Vec3::new(-322.0, 204.0, 27.2));
    commands.entity(slot_label).insert((FixedView, GameTag));
    commands.entity(slot_label).insert(Visibility::Hidden);
    let veil = commands
        .spawn((
            Sprite { color: Color::NONE, custom_size: Some(Vec2::new(744.0, 664.0)), ..default() },
            Transform::from_xyz(0.0, 0.0, 29.0),
            Visibility::Hidden,
            FixedView,
            GameTag,
        ))
        .id();
    // A skyline of parking-garage pillars above the horizon: it slides
    // opposite your steering, which is most of what "turning" looks like.
    let mut sky = Vec::new();
    for i in 0..12 {
        let ph = i as f32 / 12.0 * std::f32::consts::TAU;
        let h = 22.0 + ((i * 37) % 5) as f32 * 9.0;
        let wdt = 26.0 + ((i * 53) % 4) as f32 * 16.0;
        let tone = if i % 2 == 0 {
            Color::srgb(0.10, 0.12, 0.19)
        } else {
            Color::srgb(0.075, 0.09, 0.15)
        };
        let e = commands
            .spawn((
                Sprite { color: tone, custom_size: Some(Vec2::new(wdt, h)), ..default() },
                Transform::from_xyz(0.0, HORIZON + h / 2.0 + 1.0, 0.06),
                Visibility::Hidden,
                FixedView,
                GameTag,
            ))
            .id();
        sky.push((e, ph));
    }
    commands.insert_resource(Garage {
        rows,
        chairs,
        shots: Vec::new(),
        puddles: Vec::new(),
        boxes,
        my_seat: me,
        net: is_net,
        clock: 180.0,
        pos_t: 0.0,
        score: 0,
        over: None,
        result: String::new(),
        mini,
        rig,
        my_balloons,
        slot_icon,
        slot_label,
        slot_spin: 0.0,
        start_t: 3.2,
        bump_t: 0.0,
        sky,
        veil,
        fx_t: 0.0,
        fx_color: WHITE,
        hop_t: 0.0,
        i_won: false,
        gear: -1,
    });
    let hud = text(&mut commands, "", 18.0, WHITE, Vec3::new(0.0, 300.0, 30.0));
    commands.entity(hud).insert((Hud, GameTag));
}

fn solid_at(rows: &[String], pos: Vec2) -> bool {
    cell_at(rows, pos.x.floor() as i32, pos.y.floor() as i32) == '#'
}

fn fire_item(
    commands: &mut Commands,
    g: &mut Garage,
    shooter: usize,
    kind: u8,
    broadcast: bool,
) {
    let (pos, ang) = {
        let c = &g.chairs[shooter];
        (c.pos, c.ang)
    };
    let seat = g.chairs[shooter].seat;
    match kind {
        1 => {
            // Coffee: a puddle just behind the chair.
            let back = pos - Vec2::new(ang.cos(), -ang.sin()) * 0.9;
            let ent = bill(commands, back, 1.15, 0.85, 0.0, true, Color::srgb(0.72, 0.5, 0.2));
            g.puddles.push(Puddle { pos: back, ttl: 12.0, owner: seat, ent });
        }
        4 => {
            // The SMART stapler: slower off the line, but it steers.
            let dir = Vec2::new(ang.cos(), -ang.sin());
            let start = pos + dir * 0.8;
            let ent = bill(commands, start, 0.30, 0.30, 0.40, false, MAGENTA);
            g.shots.push(Shot { pos: start, vel: dir * 7.5, owner: seat, bounces: 0, ttl: 5.0, homing: true, ent });
        }
        _ => {
            let dir = Vec2::new(ang.cos(), -ang.sin());
            let start = pos + dir * 0.8;
            let ent = bill(commands, start, 0.26, 0.26, 0.40, false, CYAN);
            // Three ricochets, kart-shell style: the fear is the point.
            g.shots.push(Shot { pos: start, vel: dir * 9.0, owner: seat, bounces: 3, ttl: 8.0, homing: false, ent });
        }
    }
    sfx(if kind == 1 { "drop" } else { "fire" });
    if broadcast && g.net {
        if let Ok(m) = serde_json::to_string(&WFire { t: "fire".into(), k: kind, x: pos.x, y: pos.y, a: ang }) {
            net_send(&m);
        }
    }
}

/// EJECTOR SEAT: hop forward over whatever is in the way.
fn eject_hop(rows: &[String], g: &mut Garage, i: usize) {
    let (pos, ang) = (g.chairs[i].pos, g.chairs[i].ang);
    let dir = Vec2::new(ang.cos(), -ang.sin());
    for dist in [2.3f32, 1.6, 1.0] {
        let t = pos + dir * dist;
        if t.x > 1.0 && t.x < AW as f32 - 1.0 && t.y > 1.0 && t.y < AH as f32 - 1.0 && !solid_at(rows, t) {
            g.chairs[i].pos = t;
            sfx("power");
            return;
        }
    }
    sfx("buzz");
}

/// BLACKOUT: every rival chair this machine simulates goes into a spin.
/// Online, everyone else's client does the same to their own chair when
/// the flash arrives on the wire.
fn blackout(g: &mut Garage, by_idx: usize, broadcast: bool) {
    let by_seat = g.chairs[by_idx].seat;
    let net = g.net;
    for c in g.chairs.iter_mut() {
        let owned = c.human || (!net && !c.remote);
        if owned && c.seat != by_seat && c.balloons > 0 && c.star_t <= 0.0 {
            c.spin_t = 1.6;
            c.speed *= 0.3;
        }
    }
    sfx("boom");
    if broadcast && net {
        if let Ok(m) = serde_json::to_string(&WFire { t: "fire".into(), k: 7, x: 0.0, y: 0.0, a: 0.0 }) {
            net_send(&m);
        }
    }
}

/// GHOST VENDOR: lifts a balloon off the nearest rival and ties it to your
/// chair. Online the victim's own client concedes the theft (same
/// victim-authoritative rule as every pop).
fn ghost_steal(commands: &mut Commands, g: &mut Garage, i: usize, broadcast: bool) {
    let (pos, my_seat_of_thief) = (g.chairs[i].pos, g.chairs[i].seat);
    let target = g
        .chairs
        .iter()
        .enumerate()
        .filter(|(j, c)| *j != i && c.balloons > 0 && c.star_t <= 0.0)
        .min_by(|a, b| {
            a.1.pos
                .distance(pos)
                .partial_cmp(&b.1.pos.distance(pos))
                .unwrap_or(std::cmp::Ordering::Equal)
        })
        .map(|(j, c)| (j, c.seat, c.remote));
    let Some((vj, vseat, remote)) = target else {
        sfx("buzz");
        return;
    };
    sfx("eat");
    if remote {
        // Ask the wire: the victim's client will send the pop back.
        if broadcast && g.net {
            if let Ok(m) = serde_json::to_string(&WFire {
                t: "fire".into(),
                k: 8,
                x: vseat as f32,
                y: 0.0,
                a: 0.0,
            }) {
                net_send(&m);
            }
        }
    } else {
        // Local victim: settle it here.
        g.chairs[vj].balloons -= 1;
        g.chairs[i].balloons = (g.chairs[i].balloons + 1).min(5);
        if g.chairs[i].human {
            popup(commands, "+1 BALLOON (STOLEN)", 16.0, GREEN, Vec2::new(0.0, 110.0));
            stat("balloons_popped", 1);
        }
        if g.chairs[vj].human {
            popup(commands, "A GHOST TOOK A BALLOON!", 18.0, RED, Vec2::new(0.0, -100.0));
            stat("balloons_lost", 1);
        }
        let alive: Vec<usize> = g.chairs.iter().filter(|c| c.balloons > 0).map(|c| c.seat).collect();
        let _ = my_seat_of_thief;
        if alive.len() <= 1 && g.over.is_none() {
            finish(commands, g, alive.first().copied());
        }
    }
}

/// My chair (and the shared physics for every locally-simulated chair).
#[allow(clippy::too_many_arguments)]
fn drive(
    time: Res<Time>,
    keys: Res<ButtonInput<KeyCode>>,
    mut commands: Commands,
    mut g: ResMut<Garage>,
) {
    if g.over.is_some() {
        return;
    }
    let dt = time.delta_secs();
    g.bump_t = (g.bump_t - dt).max(0.0);
    // The grid holds everyone until the horn: 3... 2... 1... GO!
    if g.start_t > 0.0 {
        let before = g.start_t.ceil() as i32;
        g.start_t -= dt;
        let after = g.start_t.max(0.0).ceil() as i32;
        if after < before && after > 0 {
            popup(&mut commands, &after.to_string(), 44.0, AMBER, Vec2::new(0.0, 60.0));
            sfx("tick");
        }
        if g.start_t <= 0.0 {
            popup(&mut commands, "GO!", 48.0, GREEN, Vec2::new(0.0, 60.0));
            sfx("power");
        }
        return;
    }
    g.clock -= dt;
    let rows = g.rows.clone();
    let my = g.my_seat;
    let net = g.net;

    // Inputs act on MY chair; bots and physics run for locally-owned chairs.
    // MK-style item box: pressing the button while it spins STOPS the
    // spin; the next press uses what settled.
    let stop_slot = keys.just_pressed(KeyCode::Space) && g.slot_spin > 0.0;
    if stop_slot {
        g.slot_spin = 0.0;
        sfx("place");
    }
    let mut fire_req: Option<(usize, u8)> = None;
    let mut bumped = false;
    for i in 0..g.chairs.len() {
        let local = {
            let c = &g.chairs[i];
            c.human || (!net && !c.human) // local mode simulates the bots too
        };
        if !local {
            continue;
        }
        let c = &mut g.chairs[i];
        if c.balloons <= 0 {
            c.speed = 0.0;
            continue;
        }
        c.spin_t = (c.spin_t - dt).max(0.0);
        c.boost_t = (c.boost_t - dt).max(0.0);
        c.inv_t = (c.inv_t - dt).max(0.0);
        let mut coasting = false;
        if c.human {
            let turn = i32::from(keys.pressed(KeyCode::ArrowLeft) || keys.pressed(KeyCode::KeyA))
                - i32::from(keys.pressed(KeyCode::ArrowRight) || keys.pressed(KeyCode::KeyD));
            let gas = keys.pressed(KeyCode::ArrowUp) || keys.pressed(KeyCode::KeyW);
            let brake = keys.pressed(KeyCode::ArrowDown) || keys.pressed(KeyCode::KeyS);
            if c.spin_t > 0.0 {
                c.ang += 9.0 * dt; // the spin-out: all wheel, no say
            } else {
                // A parked car doesn't pivot: steering authority comes with
                // road speed.
                let authority = (0.10 + 0.90 * (c.speed.abs() / MAX_SPEED).min(1.0).sqrt()).min(1.0);
                c.ang += turn as f32 * TURN_RATE * dt * authority;
                if gas {
                    // Gas pedal: hard launch off the line, tapering as the
                    // motor winds out toward top speed.
                    let curve = (1.25 - 0.9 * (c.speed.max(0.0) / MAX_SPEED)).max(0.2);
                    c.speed += ACCEL * curve * dt;
                }
                // The brake is a brake: kill forward motion first, and only
                // once you've stopped does holding it creep you backward.
                if brake {
                    if c.speed > 6.0 {
                        c.speed = (c.speed - 460.0 * dt).max(0.0);
                    } else {
                        c.speed -= 120.0 * dt;
                    }
                }
                coasting = !gas && !brake;
            }
            if keys.just_pressed(KeyCode::Space) && !stop_slot {
                if let Some(kind) = c.item.take() {
                    // Say what the item is DOING, right when it happens.
                    let (line, color) = match kind {
                        0 => ("STAPLER AWAY!", CYAN),
                        1 => ("COFFEE SPILLED BEHIND YOU", Color::srgb(0.85, 0.6, 0.25)),
                        2 => ("ESPRESSO! FLOOR IT", GREEN),
                        3 => ("TRIPLE SPREAD!", CYAN),
                        4 => ("SMART STAPLER SEEKING...", MAGENTA),
                        5 => ("OVERTIME! UNTOUCHABLE - RAM THEM", AMBER),
                        6 => ("EJECTOR SEAT!", WHITE),
                        7 => ("BLACKOUT! EVERY RIVAL SPINS", Color::srgb(0.5, 0.55, 1.0)),
                        _ => ("GHOST SENT FOR A BALLOON...", Color::srgb(0.85, 0.9, 1.0)),
                    };
                    popup(&mut commands, line, 18.0, color, Vec2::new(0.0, 150.0));
                    match kind {
                        2 => {
                            // The shot hits NOW: full send, not a gentle ramp.
                            c.boost_t = 1.4;
                            c.speed = MAX_SPEED * 1.5;
                            c.mdir = c.ang; // grip snaps so the zoom goes where you look
                            sfx("power");
                        }
                        3 => fire_req = Some((i, 10)), // triple
                        5 => {
                            c.star_t = 5.0; // OVERTIME
                            sfx("power");
                        }
                        6 => fire_req = Some((i, 20)), // ejector hop
                        7 => fire_req = Some((i, 7)),  // blackout
                        8 => fire_req = Some((i, 8)),  // ghost vendor
                        k => fire_req = Some((i, k)),
                    }
                    stat("items_used", 1);
                }
            }
        }
        let max = if c.boost_t > 0.0 || c.star_t > 0.0 { MAX_SPEED * 1.55 } else { MAX_SPEED };
        c.speed = c.speed.clamp(-max * 0.35, max);
        // Off the gas you coast down gently; on it, mild rolling drag.
        c.speed -= c.speed * if coasting { 1.1 } else { 0.55 } * dt;
        // Grip: the direction you MOVE chases the direction you FACE. At
        // speed there's less grip, so hard steering drifts — and yanking
        // the wheel flat-out spins you.
        // Spins come from HITS, never from steering: hard cornering only
        // drifts (the slip below), it never punishes you with a spin-out.
        let slip = wrap_pi(c.ang - c.mdir);
        let grip = 7.5 - 5.0 * (c.speed.abs() / MAX_SPEED).min(1.0);
        c.mdir += slip * (grip * dt).min(1.0);
        let dirv = Vec2::new(c.mdir.cos(), -c.mdir.sin());
        let step = dirv * (c.speed / ACELL) * dt;
        let next = c.pos + step;
        // Walls: glance off at an angle (motion direction reflects, your
        // facing doesn't), square hits bounce you straight back.
        let bx = solid_at(&rows, Vec2::new(next.x, c.pos.y));
        let by = solid_at(&rows, Vec2::new(c.pos.x, next.y));
        if !bx {
            c.pos.x = next.x;
        }
        if !by {
            c.pos.y = next.y;
        }
        if bx && by {
            c.speed *= -0.35;
            if c.human && c.speed.abs() > 40.0 {
                bumped = true;
            }
        } else if bx {
            c.mdir = (-dirv.y).atan2(-dirv.x);
            if c.human && c.speed.abs() > 110.0 {
                bumped = true;
            }
            c.speed *= 0.78;
        } else if by {
            c.mdir = dirv.y.atan2(dirv.x);
            if c.human && c.speed.abs() > 110.0 {
                bumped = true;
            }
            c.speed *= 0.78;
        }
    }
    if bumped && g.bump_t <= 0.0 {
        g.bump_t = 0.35;
        sfx("thud"); // upholstery meets masonry
    }
    // The engine hums with your actual speed — hear yourself accelerate,
    // coast down, and brake.
    const ENGINE: [&str; 7] =
        ["engine0", "engine1", "engine2", "engine3", "engine4", "engine5", "engine6"];
    let my_speed = g
        .chairs
        .iter()
        .find(|c| c.seat == g.my_seat)
        .map(|c| c.speed.abs())
        .unwrap_or(0.0);
    let gear = ((my_speed / MAX_SPEED * 6.0).round() as i8).clamp(0, 6);
    if gear != g.gear {
        g.gear = gear;
        sfx(ENGINE[gear as usize]);
    }
    if let Some((i, kind)) = fire_req {
        match kind {
            10 => {
                for spread in [-0.25f32, 0.0, 0.25] {
                    g.chairs[i].ang += spread;
                    fire_item(&mut commands, &mut g, i, 0, true);
                    g.chairs[i].ang -= spread;
                }
                stat("staplers_thrown", 3);
            }
            20 => {
                eject_hop(&rows, &mut g, i);
                if g.chairs[i].human {
                    g.hop_t = 0.45;
                }
            }
            7 => {
                blackout(&mut g, i, true);
                g.fx_t = 0.45;
                g.fx_color = Color::srgb(0.35, 0.4, 1.0);
            }
            8 => ghost_steal(&mut commands, &mut g, i, true),
            _ => {
                fire_item(&mut commands, &mut g, i, kind, true);
                if kind == 0 || kind == 4 {
                    stat("staplers_thrown", 1);
                }
            }
        }
    }

    // Box pickups (locally-owned chairs claim; net broadcasts the claim).
    let mut claims: Vec<u32> = Vec::new();
    for bi in 0..g.boxes.len() {
        if g.boxes[bi].up_in > 0.0 {
            continue;
        }
        let bc = g.boxes[bi].cell;
        let bpos = Vec2::new(bc.0 as f32 + 0.5, bc.1 as f32 + 0.5);
        let clock = g.clock;
        let mut taken = false;
        let mut mine = false;
        for c in g.chairs.iter_mut() {
            let local = c.human || (!net && !c.remote && !c.human);
            let alive = c.balloons > 0;
            if local && alive && c.item.is_none() && c.pos.distance(bpos) < 0.8 {
                let roll = ((c.pos.x * 13.7 + c.pos.y * 7.3 + clock * 31.0) as u32) % 12;
                c.item = Some(match roll {
                    0 | 1 => 0,  // stapler
                    2 | 3 => 1,  // coffee
                    4 | 5 => 2,  // espresso
                    6 => 3,      // triple
                    7 => 4,      // smart
                    8 => 5,      // overtime
                    9 => 6,      // ejector
                    10 => 7,     // blackout
                    _ => 8,      // ghost
                });
                taken = true;
                if c.human {
                    mine = true;
                    stat("boxes_grabbed", 1);
                    sfx("coin");
                }
                break;
            }
        }
        if taken {
            g.boxes[bi].up_in = 7.0;
            claims.push(bi as u32);
        }
        if mine {
            g.slot_spin = 1.0; // the box spins before it settles
        }
    }
    if net {
        for i in claims {
            if let Ok(m) = serde_json::to_string(&WBox { t: "box".into(), i }) {
                net_send(&m);
            }
        }
    }

    // Stream my chair.
    if net {
        g.pos_t -= dt;
        if g.pos_t <= 0.0 {
            g.pos_t = 0.1;
            if let Some(c) = g.chairs.iter().find(|c| c.seat == my) {
                if let Ok(m) = serde_json::to_string(&WPos {
                    t: "pos".into(),
                    x: c.pos.x,
                    y: c.pos.y,
                    a: c.ang,
                    b: c.balloons,
                    s: c.star_t > 0.0,
                }) {
                    net_send(&m);
                }
            }
        }
    }

}

/// Bot chairs (local rounds): chase a box or the nearest live rival, fire
/// when roughly lined up. Dumb, cheerful, dangerous in numbers.
fn bots(time: Res<Time>, mut commands: Commands, mut g: ResMut<Garage>) {
    if g.net || g.over.is_some() || g.start_t > 0.0 {
        return;
    }
    let dt = time.delta_secs();
    let positions: Vec<(usize, Vec2, i32)> =
        g.chairs.iter().map(|c| (c.seat, c.pos, c.balloons)).collect();
    let boxes: Vec<Vec2> = g
        .boxes
        .iter()
        .filter(|b| b.up_in <= 0.0)
        .map(|b| Vec2::new(b.cell.0 as f32 + 0.5, b.cell.1 as f32 + 0.5))
        .collect();
    let mut fire_req: Vec<(usize, u8)> = Vec::new();
    for i in 0..g.chairs.len() {
        let c = &mut g.chairs[i];
        if c.human || c.balloons <= 0 {
            continue;
        }
        c.think -= dt;
        // Target: a box when unarmed, the nearest live rival when armed.
        let target = if c.item.is_none() {
            boxes.iter().min_by(|a, b| {
                a.distance(c.pos).partial_cmp(&b.distance(c.pos)).unwrap_or(std::cmp::Ordering::Equal)
            }).copied()
        } else {
            positions
                .iter()
                .filter(|(s, _, b)| *s != c.seat && *b > 0)
                .min_by(|a, b| a.1.distance(c.pos).partial_cmp(&b.1.distance(c.pos)).unwrap_or(std::cmp::Ordering::Equal))
                .map(|(_, p, _)| *p)
        };
        let Some(t) = target else { continue };
        let want = (-(t.y - c.pos.y)).atan2(t.x - c.pos.x);
        let mut da = want - c.ang;
        while da > std::f32::consts::PI {
            da -= std::f32::consts::TAU;
        }
        while da < -std::f32::consts::PI {
            da += std::f32::consts::TAU;
        }
        if c.spin_t <= 0.0 {
            c.ang += da.clamp(-TURN_RATE * dt, TURN_RATE * dt);
            c.speed += ACCEL * 0.8 * dt;
        }
        if c.item.is_some() && da.abs() < 0.25 && c.think <= 0.0 && t.distance(c.pos) < 9.0 {
            c.think = 0.8;
            let kind = c.item.take().unwrap();
            match kind {
                2 => {
                    c.boost_t = 1.2;
                    c.speed = MAX_SPEED * 1.4;
                    c.mdir = c.ang;
                }
                5 => c.star_t = 4.0,
                k => fire_req.push((i, k)),
            }
        }
    }
    let rows = g.rows.clone();
    for (i, kind) in fire_req {
        match kind {
            6 => eject_hop(&rows, &mut g, i),
            7 => blackout(&mut g, i, false),
            8 => ghost_steal(&mut commands, &mut g, i, false),
            1 => fire_item(&mut commands, &mut g, i, 1, false),
            4 => fire_item(&mut commands, &mut g, i, 4, false),
            _ => fire_item(&mut commands, &mut g, i, 0, false),
        }
    }
}

/// Shots travel, bounce once, and pop balloons on locally-owned chairs.
fn shots_fly(time: Res<Time>, mut commands: Commands, mut g: ResMut<Garage>) {
    if g.over.is_some() {
        return;
    }
    let dt = time.delta_secs();
    let rows = g.rows.clone();
    let net = g.net;
    let mut pops: Vec<(usize, usize)> = Vec::new(); // chair idx, by-seat
    let mut dead_shots = Vec::new();
    // Homing shots steer toward the nearest rival first.
    let chair_spots: Vec<(usize, Vec2, i32)> =
        g.chairs.iter().map(|c| (c.seat, c.pos, c.balloons)).collect();
    for si in 0..g.shots.len() {
        let s = &mut g.shots[si];
        s.ttl -= dt;
        if s.ttl <= 0.0 {
            dead_shots.push(si);
            continue;
        }
        if s.homing {
            if let Some((_, tpos, _)) = chair_spots
                .iter()
                .filter(|(seat, _, b)| *seat != s.owner && *b > 0)
                .min_by(|a, b| {
                    a.1.distance(s.pos).partial_cmp(&b.1.distance(s.pos)).unwrap_or(std::cmp::Ordering::Equal)
                })
            {
                let want = (*tpos - s.pos).normalize_or_zero() * s.vel.length();
                s.vel = (s.vel + (want - s.vel) * (3.5 * dt).min(1.0)).normalize_or_zero() * s.vel.length();
            }
        }
        let next = s.pos + s.vel * dt;
        if solid_at(&rows, next) {
            if s.bounces > 0 {
                s.bounces -= 1;
                // Bounce off whichever axis hit.
                if solid_at(&rows, Vec2::new(next.x, s.pos.y)) {
                    s.vel.x = -s.vel.x;
                } else {
                    s.vel.y = -s.vel.y;
                }
                sfx("tick");
            } else {
                dead_shots.push(si);
                continue;
            }
        } else {
            s.pos = next;
        }
    }
    // Hits: only chairs this machine owns decide they were hit. OVERTIME
    // chairs cannot be popped by anything.
    for (ci, c) in g.chairs.iter().enumerate() {
        let owned = c.human || (!net && !c.remote);
        if !owned || c.balloons <= 0 || c.inv_t > 0.0 || c.star_t > 0.0 {
            continue;
        }
        for (si, s) in g.shots.iter().enumerate() {
            if s.owner != c.seat && s.pos.distance(c.pos) < 0.6 {
                pops.push((ci, s.owner));
                dead_shots.push(si);
                break;
            }
        }
    }
    dead_shots.sort_unstable();
    dead_shots.dedup();
    for si in dead_shots.into_iter().rev() {
        let s = g.shots.remove(si);
        commands.entity(s.ent).despawn();
    }
    for (ci, by) in pops {
        pop_balloon(&mut commands, &mut g, ci, by);
    }
}

fn pop_balloon(commands: &mut Commands, g: &mut Garage, ci: usize, by: usize) {
    let my = g.my_seat;
    let net = g.net;
    {
        let c = &mut g.chairs[ci];
        c.balloons -= 1;
        c.inv_t = 1.5;
        c.spin_t = 0.8;
        sfx("boom");
        if c.seat == my {
            popup(commands, "POP! BALLOON GONE", 20.0, RED, Vec2::new(0.0, -100.0));
            sfx("death"); // unmistakably YOU got hit
            stat("balloons_lost", 1);
        }
    }
    // Credit the popper.
    if let Some(p) = g.chairs.iter_mut().find(|c| c.seat == by) {
        p.pops += 1;
        if p.seat == my {
            g.score += 200;
            popup(commands, "+200 POP", 16.0, GREEN, Vec2::new(0.0, 110.0));
            stat("balloons_popped", 1);
        }
    }
    let seat = g.chairs[ci].seat;
    if net && seat == my {
        if let Ok(m) = serde_json::to_string(&WPop { t: "pop".into(), by: by as u8, g: false }) {
            net_send(&m);
        }
    }
    // Elimination and the end of the derby.
    if g.chairs[ci].balloons <= 0 {
        if seat == my {
            popup(commands, "ELIMINATED!", 34.0, RED, Vec2::new(0.0, 0.0));
            g.fx_t = 0.6;
            g.fx_color = RED;
            stat("chairs_lost", 1);
            // Solo: your derby is over the moment you are — call it for
            // the leading bot instead of leaving you on the carpet.
            if !g.net && g.over.is_none() {
                let leader = g
                    .chairs
                    .iter()
                    .filter(|c| c.balloons > 0)
                    .max_by_key(|c| (c.balloons, c.pops))
                    .map(|c| c.seat);
                finish(commands, g, leader);
                return;
            }
        } else {
            popup(commands, &format!("CHAIR {} IS OUT", seat + 1), 14.0, AMBER, Vec2::new(0.0, 140.0));
        }
    }
    let alive: Vec<usize> = g.chairs.iter().filter(|c| c.balloons > 0).map(|c| c.seat).collect();
    if alive.len() <= 1 && g.over.is_none() {
        finish(commands, g, alive.first().copied());
    }
}

fn finish(commands: &mut Commands, g: &mut Garage, winner: Option<usize>) {
    let my = g.my_seat;
    g.i_won = winner == Some(my);
    let mine = g.chairs.iter().find(|c| c.seat == my);
    let my_pops = mine.map(|c| c.pops).unwrap_or(0);
    let survived = mine.map(|c| c.balloons > 0).unwrap_or(false);
    g.score += my_pops * 200 + if survived { 500 } else { 0 } + if winner == Some(my) { 500 } else { 0 };
    if winner == Some(my) {
        stat("floors_taken", 1);
    }
    g.result = match winner {
        Some(w) if w == my => "THE FLOOR IS YOURS.".into(),
        Some(w) => format!("CHAIR {} TAKES THE FLOOR.", w + 1),
        None => "EVERYONE'S ON THE CARPET.".into(),
    };
    popup(commands, &g.result.clone(), 28.0, GREEN, Vec2::new(0.0, 40.0));
    sfx("engine_off");
    g.over = Some(Timer::from_seconds(2.6, TimerMode::Once));
    sfx(if winner == Some(my) { "win" } else { "over" });
}

/// Puddles spin chairs; box respawns tick; the round clock can call time.
fn hazards(time: Res<Time>, mut commands: Commands, mut g: ResMut<Garage>) {
    if g.over.is_some() {
        return;
    }
    let dt = time.delta_secs();
    let net = g.net;
    let mut expired = Vec::new();
    let mut pops = Vec::new();
    for (pi, p) in g.puddles.iter_mut().enumerate() {
        p.ttl -= dt;
        if p.ttl <= 0.0 {
            expired.push(pi);
        }
    }
    for pi in expired.into_iter().rev() {
        let p = g.puddles.remove(pi);
        commands.entity(p.ent).despawn();
    }
    // OVERTIME clocks tick down for everyone (remotes get refreshed by wire).
    for c in g.chairs.iter_mut() {
        c.star_t = (c.star_t - dt).max(0.0);
    }
    let puds: Vec<(usize, Vec2)> = g.puddles.iter().map(|p| (p.owner, p.pos)).collect();
    let stars: Vec<(usize, Vec2)> = g
        .chairs
        .iter()
        .filter(|c| c.star_t > 0.0 && c.balloons > 0)
        .map(|c| (c.seat, c.pos))
        .collect();
    for (ci, c) in g.chairs.iter_mut().enumerate() {
        let owned = c.human || (!net && !c.remote);
        if !owned || c.balloons <= 0 || c.inv_t > 0.0 || c.star_t > 0.0 {
            continue;
        }
        for &(owner, pos) in &puds {
            if owner != c.seat && c.pos.distance(pos) < 0.7 && c.spin_t <= 0.0 {
                pops.push((ci, owner));
                break;
            }
        }
        // Brushing an OVERTIME chair pops you, credited to the rusher.
        for &(sseat, spos) in &stars {
            if sseat != c.seat && c.pos.distance(spos) < 0.75 {
                pops.push((ci, sseat));
                break;
            }
        }
    }
    for (ci, by) in pops {
        pop_balloon(&mut commands, &mut g, ci, by);
    }
    if g.clock <= 0.0 && g.over.is_none() {
        // Time: most balloons wins; pops break ties.
        let best = g
            .chairs
            .iter()
            .max_by_key(|c| (c.balloons, c.pops))
            .map(|c| c.seat);
        finish(&mut commands, &mut g, best);
    }
}

fn boxes_spin(time: Res<Time>, mut g: ResMut<Garage>) {
    let dt = time.delta_secs();
    for b in g.boxes.iter_mut() {
        b.up_in = (b.up_in - dt).max(0.0);
    }
}

fn net_apply(
    mut events: EventReader<NetIn>,
    net: Res<NetMode>,
    mut commands: Commands,
    mut g: ResMut<Garage>,
) {
    let Some(cfg) = &net.0 else {
        events.clear();
        return;
    };
    for ev in events.read() {
        if ev.left {
            if let Some(c) = g.chairs.iter_mut().find(|c| c.seat == ev.seat as usize) {
                c.balloons = 0;
                c.remote = false;
            }
            let alive: Vec<usize> =
                g.chairs.iter().filter(|c| c.balloons > 0).map(|c| c.seat).collect();
            if alive.len() <= 1 && g.over.is_none() {
                finish(&mut commands, &mut g, alive.first().copied());
            }
            continue;
        }
        if ev.seat == cfg.seat {
            continue;
        }
        let Ok(v) = serde_json::from_str::<serde_json::Value>(&ev.data) else { continue };
        match v.get("t").and_then(|t| t.as_str()) {
            Some("pos") => {
                if let Ok(p) = serde_json::from_str::<WPos>(&ev.data) {
                    if let Some(c) = g.chairs.iter_mut().find(|c| c.seat == ev.seat as usize) {
                        c.pos = Vec2::new(p.x, p.y);
                        c.ang = p.a;
                        c.balloons = p.b;
                        c.star_t = if p.s { 0.5 } else { 0.0 };
                    }
                }
            }
            Some("fire") => {
                if let Ok(f) = serde_json::from_str::<WFire>(&ev.data) {
                    match f.k {
                        7 => {
                            // BLACKOUT: my own chair concedes the spin.
                            let my = g.my_seat;
                            if let Some(c) = g.chairs.iter_mut().find(|c| c.seat == my) {
                                if c.balloons > 0 && c.star_t <= 0.0 {
                                    c.spin_t = 1.6;
                                    c.speed *= 0.3;
                                    popup(&mut commands, "BLACKOUT!", 18.0, RED, Vec2::new(0.0, -100.0));
                                    g.fx_t = 0.5;
                                    g.fx_color = Color::srgb(0.35, 0.4, 1.0);
                                    sfx("boom");
                                }
                            }
                        }
                        8 => {
                            // GHOST: if it picked me, I concede the balloon.
                            let my = g.my_seat;
                            if f.x as usize == my {
                                let mut conceded = false;
                                if let Some(c) = g.chairs.iter_mut().find(|c| c.seat == my) {
                                    if c.balloons > 0 && c.star_t <= 0.0 {
                                        c.balloons -= 1;
                                        conceded = true;
                                    }
                                }
                                if conceded {
                                    popup(&mut commands, "A GHOST TOOK A BALLOON!", 18.0, RED, Vec2::new(0.0, -100.0));
                                    g.fx_t = 0.45;
                                    g.fx_color = RED;
                                    stat("balloons_lost", 1);
                                    sfx("eat");
                                    if let Ok(m) = serde_json::to_string(&WPop { t: "pop".into(), by: ev.seat, g: true }) {
                                        net_send(&m);
                                    }
                                    let alive: Vec<usize> =
                                        g.chairs.iter().filter(|c| c.balloons > 0).map(|c| c.seat).collect();
                                    if alive.len() <= 1 && g.over.is_none() {
                                        finish(&mut commands, &mut g, alive.first().copied());
                                    }
                                }
                            }
                        }
                        _ => {
                            if let Some(idx) = g.chairs.iter().position(|c| c.seat == ev.seat as usize) {
                                let (save_pos, save_ang) = (g.chairs[idx].pos, g.chairs[idx].ang);
                                g.chairs[idx].pos = Vec2::new(f.x, f.y);
                                g.chairs[idx].ang = f.a;
                                fire_item(&mut commands, &mut g, idx, f.k, false);
                                g.chairs[idx].pos = save_pos;
                                g.chairs[idx].ang = save_ang;
                            }
                        }
                    }
                }
            }
            Some("pop") => {
                if let Ok(p) = serde_json::from_str::<WPop>(&ev.data) {
                    if p.g {
                        // A conceded GHOST theft: the thief ties on a balloon.
                        let my = g.my_seat;
                        if p.by as usize == my {
                            if let Some(c) = g.chairs.iter_mut().find(|c| c.seat == my) {
                                c.balloons = (c.balloons + 1).min(5);
                            }
                            popup(&mut commands, "+1 BALLOON (STOLEN)", 16.0, GREEN, Vec2::new(0.0, 110.0));
                            stat("balloons_popped", 1);
                        }
                    } else if let Some(c) = g.chairs.iter_mut().find(|c| c.seat == p.by as usize) {
                        // Their balloon count arrives via pos; here we credit.
                        c.pops += 1;
                        if c.seat == g.my_seat {
                            g.score += 200;
                            stat("balloons_popped", 1);
                        }
                    }
                }
            }
            Some("box") => {
                if let Ok(b) = serde_json::from_str::<WBox>(&ev.data) {
                    if let Some(bx) = g.boxes.get_mut(b.i as usize) {
                        bx.up_in = 7.0;
                    }
                }
            }
            Some("lv") => {
                // Arrived before we really started: adopt the host's arena
                // by just restarting our local state from it would be heavy;
                // instead walls/boxes were already built from OUR rows. The
                // page hands hosts and guests the same doc via the picker in
                // the common case; a custom host map redraw is accepted as a
                // follow-up if the rows differ. For now: adopt collision.
                if let Ok(lv) = serde_json::from_str::<WLevel>(&ev.data) {
                    if lv.rows.len() == AH && lv.rows.iter().all(|r| r.chars().count() == AW) {
                        g.rows = lv.rows;
                    }
                }
            }
            _ => {}
        }
    }
}

/// The mode-7 pass: sync billboard state from the sim, then project every
/// billboard from behind YOUR chair. Runs even while the editor owns the
/// canvas (it hides the whole view layer there, so the canvas stays clean).
#[allow(clippy::type_complexity)]
fn project(
    time: Res<Time>,
    g: Option<ResMut<Garage>>,
    editor: Option<Res<ArenaEditor>>,
    keys: Res<ButtonInput<KeyCode>>,
    mut bills: Query<(&mut Bill, &mut Transform, &mut Sprite, &mut Visibility)>,
    mut fixed: Query<(&mut Visibility, &mut Transform), (With<FixedView>, Without<Bill>)>,
    mut fixed_sprites: Query<&mut Sprite, (With<FixedView>, Without<Bill>)>,
    mut texts: Query<&mut Text2d>,
) {
    let editing = editor.map(|e| e.active).unwrap_or(false);
    if editing || g.is_none() {
        for (_, _, _, mut vis) in bills.iter_mut() {
            *vis = Visibility::Hidden;
        }
        for (mut vis, _) in fixed.iter_mut() {
            *vis = Visibility::Hidden;
        }
        return;
    }
    let mut g = g.unwrap();
    // The slot machine settles; flashes and hops decay.
    let was = g.slot_spin;
    let dt = time.delta_secs();
    g.slot_spin = (g.slot_spin - dt).max(0.0);
    g.fx_t = (g.fx_t - dt).max(0.0);
    g.hop_t = (g.hop_t - dt).max(0.0);
    if was > 0.0 && g.slot_spin == 0.0 {
        sfx("place");
    }
    let g = &*g;
    // Camera: my chair.
    let me = g.chairs.iter().find(|c| c.seat == g.my_seat);
    let (cam, ang) = me.map(|c| (c.pos, c.ang)).unwrap_or((Vec2::new(12.0, 9.0), 0.0));
    let fwd = Vec2::new(ang.cos(), -ang.sin());
    let rt = Vec2::new(-fwd.y, fwd.x);
    // Sync dynamic billboards from the sim.
    let flicker = (g.clock * 12.0) as i32 % 2 == 0;
    for c in &g.chairs {
        let base = PLAYER_COLORS[c.seat % 12];
        let behind = c.seat == g.my_seat; // your chair is the fixed rig instead
        let pos = if behind { cam - fwd * 3.0 } else { c.pos };
        // One tint rule for every part of the silhouette.
        let tint = |part: Color| -> Color {
            if c.balloons <= 0 {
                part.with_alpha(0.22)
            } else if c.star_t > 0.0 && flicker {
                WHITE // OVERTIME strobe
            } else if c.inv_t > 0.0 {
                part.with_alpha(0.5)
            } else {
                part
            }
        };
        if let Ok((mut b, _, _, _)) = bills.get_mut(c.base_ent) {
            b.pos = pos;
            b.base = tint(Color::srgb(0.13, 0.13, 0.17));
        }
        if let Ok((mut b, _, _, _)) = bills.get_mut(c.ent) {
            b.pos = pos;
            b.base = tint(base);
        }
        if let Ok((mut b, _, _, _)) = bills.get_mut(c.back_ent) {
            b.pos = pos;
            b.base = tint(dim_color(base));
        }
        if let Ok((mut b, _, _, _)) = bills.get_mut(c.balloon_ent) {
            b.pos = pos;
            b.w = 0.3 * c.balloons.max(0) as f32;
            b.base = base.with_alpha(if c.balloons > 0 { 0.95 } else { 0.0 });
        }
    }
    // Staplers strobe and bob so they never read as furniture.
    for s in &g.shots {
        if let Ok((mut b, _, _, _)) = bills.get_mut(s.ent) {
            b.pos = s.pos;
            b.base = if flicker {
                WHITE
            } else if s.homing {
                MAGENTA
            } else {
                CYAN
            };
            b.w = if flicker { 0.34 } else { 0.24 };
            b.alt = 0.42 + 0.10 * (g.clock * 9.0).sin();
        }
    }
    // Coffee reads as a bright fresh spill, not a shadow.
    for p in &g.puddles {
        if let Ok((mut b, _, _, _)) = bills.get_mut(p.ent) {
            b.pos = p.pos;
            b.w = 1.15;
            let shimmer = 0.75 + 0.2 * (g.clock * 5.0).sin();
            b.base = Color::srgba(0.72, 0.5, 0.2, (p.ttl / 6.0).clamp(0.4, 1.0) * shimmer);
        }
    }
    for (i, bx) in g.boxes.iter().enumerate() {
        if let Ok((mut b, _, _, _)) = bills.get_mut(bx.ent) {
            // Item crates cycle the rainbow, kart-item-box style: nothing
            // else in the garage is ever this colorful.
            b.base = if bx.up_in > 0.0 {
                AMBER.with_alpha(0.10)
            } else {
                let hue = (g.clock * 160.0 + i as f32 * 47.0).rem_euclid(360.0);
                Color::hsl(hue, 0.85, 0.62)
            };
            b.alt = 0.25 + 0.10 * (g.clock * 3.0 + i as f32).sin();
        }
    }
    // Project everything.
    for (b, mut tf, mut sp, mut vis) in bills.iter_mut() {
        let rel = b.pos - cam;
        let cx = rel.dot(rt);
        let cy = rel.dot(fwd);
        if cy < 0.30 || cy > 17.0 {
            *vis = Visibility::Hidden;
            continue;
        }
        let inv = FOCAL / cy;
        let sx = cx * inv;
        if sx.abs() > 640.0 {
            *vis = Visibility::Hidden;
            continue;
        }
        let gy = HORIZON - inv; // camera rides one cell above the deck
        let (w_px, h_px, sy, zoff) = if b.flat {
            // Ground decals project by their DEPTH: near edge low on the
            // screen, far edge higher, like a real mode-7 floor tile.
            let d = b.hh.max(0.2);
            let near = (cy - d / 2.0).max(0.26);
            let far = cy + d / 2.0;
            let y_near = HORIZON - FOCAL / near;
            let y_far = HORIZON - FOCAL / far;
            let w = (b.w * inv).min(900.0);
            (w, (y_far - y_near).max(1.5), (y_near + y_far) / 2.0, -0.4 + b.alt)
        } else {
            let w = (b.w * inv).min(900.0);
            let h = (b.hh * inv).min(900.0);
            (w, h, gy + h / 2.0 + b.alt * inv, 0.0)
        };
        let shade = (2.4 / cy).clamp(0.30, 1.0);
        let c = b.base.to_srgba();
        sp.color = Color::srgba(c.red * shade, c.green * shade, c.blue * shade, c.alpha);
        sp.custom_size = Some(Vec2::new(w_px.max(1.0), h_px.max(1.0)));
        tf.translation = Vec3::new(sx, sy, (21.0 - cy).clamp(1.0, 21.0) + zoff);
        *vis = Visibility::Inherited;
    }
    // Screen-fixed furniture on, then the per-chair minimap dots and the rig.
    for (mut vis, _) in fixed.iter_mut() {
        *vis = Visibility::Inherited;
    }
    for (i, c) in g.chairs.iter().enumerate() {
        if let Some(&e) = g.mini.get(i) {
            if let Ok((mut vis, mut tf)) = fixed.get_mut(e) {
                *vis = if c.balloons > 0 { Visibility::Inherited } else { Visibility::Hidden };
                let mp = mini_xy(c.pos);
                tf.translation.x = mp.x;
                tf.translation.y = mp.y;
            }
        }
    }
    let me_dead = me.map(|c| c.balloons <= 0).unwrap_or(false);
    if let Ok((_, mut tf)) = fixed.get_mut(g.rig) {
        if me_dead {
            // Knocked out: the chair goes over on its side. No ambiguity.
            tf.rotation = Quat::from_rotation_z(1.35);
            tf.translation.y = -272.0;
        } else {
            let lean = (i32::from(keys.pressed(KeyCode::ArrowLeft) || keys.pressed(KeyCode::KeyA))
                - i32::from(keys.pressed(KeyCode::ArrowRight) || keys.pressed(KeyCode::KeyD)))
                as f32;
            // The chair leans into the drift and jitters with speed — the seat
            // of the pants doing its share of the storytelling.
            let (slip, spd) = me.map(|c| (wrap_pi(c.ang - c.mdir), c.speed)).unwrap_or((0.0, 0.0));
            tf.rotation = Quat::from_rotation_z((lean * 0.05 + slip * 0.55).clamp(-0.5, 0.5));
            let bob = (g.clock * 11.0).sin() * (spd.abs() / MAX_SPEED).min(1.0) * 3.0;
            let hop = (std::f32::consts::PI * (1.0 - g.hop_t / 0.45).clamp(0.0, 1.0)).sin() * 46.0;
            tf.translation.y = -238.0 + bob + if g.hop_t > 0.0 { hop } else { 0.0 };
        }
    }
    if let Ok(mut sp) = fixed_sprites.get_mut(g.rig) {
        let my_color = PLAYER_COLORS[g.my_seat % 12];
        sp.color = if me_dead {
            Color::srgb(0.35, 0.35, 0.4)
        } else if me.map(|c| c.star_t > 0.0).unwrap_or(false) && flicker {
            WHITE
        } else {
            my_color
        };
    }
    // The drama veil: blackout blue, ghost/elimination red.
    if let Ok(mut sp) = fixed_sprites.get_mut(g.veil) {
        sp.color = if g.fx_t > 0.0 {
            g.fx_color.with_alpha((g.fx_t / 0.6 * 0.38).min(0.38))
        } else {
            Color::NONE
        };
    }
    // Skyline slides opposite the steering.
    for &(e, phase) in &g.sky {
        if let Ok((mut vis, mut tf)) = fixed.get_mut(e) {
            let x = wrap_pi(phase - ang) * 230.0;
            if x.abs() > 400.0 {
                *vis = Visibility::Hidden;
            } else {
                tf.translation.x = x;
            }
        }
    }
    // Your balloon rack: what you can still afford to lose.
    let my_balloons_now = me.map(|c| c.balloons.max(0)).unwrap_or(0);
    for (i, &e) in g.my_balloons.iter().enumerate() {
        if let Ok((mut vis, _)) = fixed.get_mut(e) {
            *vis = if (i as i32) < my_balloons_now { Visibility::Inherited } else { Visibility::Hidden };
        }
    }
    // The item box: spinning, held, or empty.
    let held = me.and_then(|c| c.item);
    let (icon, label) = if g.slot_spin > 0.0 {
        let k = ((g.clock * 14.0).abs() as u32 % 9) as u8;
        (item_icon(k).0, "? ? ?".to_string())
    } else if let Some(k) = held {
        let (c, n) = item_icon(k);
        (c, n.to_string())
    } else if me.map(|c| c.star_t > 0.0).unwrap_or(false) {
        (if flicker { AMBER } else { WHITE }, "OVERTIME!".to_string())
    } else {
        (Color::NONE, String::new())
    };
    if let Ok(mut sp) = fixed_sprites.get_mut(g.slot_icon) {
        sp.color = icon;
    }
    if let Ok(mut t) = texts.get_mut(g.slot_label) {
        if t.0 != label {
            t.0 = label;
        }
    }
}

fn hud(g: Res<Garage>, mut hud: Query<&mut Text2d, With<Hud>>) {
    if let Ok(mut t) = hud.single_mut() {
        let s = if g.over.is_some() {
            g.result.clone()
        } else if g.start_t > 0.0 {
            format!("GET READY... {}", g.start_t.max(0.0).ceil() as i32)
        } else if g.chairs.iter().find(|c| c.seat == g.my_seat).map(|c| c.balloons <= 0).unwrap_or(false) {
            let alive = g.chairs.iter().filter(|c| c.balloons > 0).count();
            format!(
                "ELIMINATED - SPECTATING   {} STILL ROLLING   {}:{:02}",
                alive,
                (g.clock.max(0.0) as u32) / 60,
                (g.clock.max(0.0) as u32) % 60
            )
        } else {
            let me = g.chairs.iter().find(|c| c.seat == g.my_seat);
            let item = match me.and_then(|c| c.item) {
                Some(0) => "  [STAPLER]",
                Some(1) => "  [COFFEE]",
                Some(2) => "  [ESPRESSO]",
                Some(3) => "  [TRIPLE]",
                Some(4) => "  [SMART]",
                Some(5) => "  [OVERTIME]",
                Some(6) => "  [EJECTOR]",
                Some(7) => "  [BLACKOUT]",
                Some(8) => "  [GHOST]",
                _ => "",
            };
            let alive = g.chairs.iter().filter(|c| c.balloons > 0).count();
            format!(
                "BALLOONS {}   POPS {}   {} ROLLING   {}:{:02}{}",
                me.map(|c| c.balloons.max(0)).unwrap_or(0),
                me.map(|c| c.pops).unwrap_or(0),
                alive,
                (g.clock.max(0.0) as u32) / 60,
                (g.clock.max(0.0) as u32) % 60,
                item
            )
        };
        if t.0 != s {
            t.0 = s;
        }
    }
}

fn endgame(
    time: Res<Time>,
    mut g: ResMut<Garage>,
    editor: Option<ResMut<ArenaEditor>>,
    mut final_score: ResMut<FinalScore>,
    mut banner: ResMut<crate::EndBanner>,
    mut next: ResMut<NextState<Phase>>,
) {
    if let Some(t) = g.over.as_mut() {
        if t.tick(time.delta()).finished() {
            if let Some(mut e) = editor {
                if e.testing {
                    e.testing = false;
                    e.active = true;
                    g.over = None;
                    return;
                }
            }
            final_score.0 = g.score;
            banner.0 = Some(if g.i_won { "THE FLOOR IS YOURS!".into() } else { "KNOCKED OUT".into() });
            next.set(Phase::GameOver);
        }
    }
}

// ── the arena editor ─────────────────────────────────────────────────────

#[derive(Component)]
struct EdTag;

#[derive(Component)]
struct EdCell(usize, usize);

#[derive(Component)]
struct EdHud;

const ED_BRUSHES: [(char, &str); 4] = [('#', "WALL"), ('.', "FLOOR"), ('B', "BOX"), ('s', "SPAWN")];

fn ed_color(ch: char) -> Color {
    match ch {
        '#' => Color::srgb(0.34, 0.36, 0.42),
        'B' => AMBER,
        's' => GREEN,
        _ => Color::srgb(0.08, 0.09, 0.13),
    }
}

#[allow(clippy::too_many_arguments)]
fn editor_update(
    mut commands: Commands,
    keys: Res<ButtonInput<KeyCode>>,
    buttons: Res<ButtonInput<MouseButton>>,
    windows: Query<&Window>,
    cameras: Query<(&Camera, &GlobalTransform)>,
    editor: Option<ResMut<ArenaEditor>>,
    garage: Option<ResMut<Garage>>,
    canvas: Query<Entity, With<EdTag>>,
    mut cells: Query<(&EdCell, &mut Sprite)>,
    mut ed_hud: Query<&mut Text2d, With<EdHud>>,
    mut next: ResMut<NextState<Phase>>,
) {
    let Some(mut editor) = editor else { return };
    if !editor.active {
        if editor.testing && keys.just_pressed(KeyCode::KeyX) {
            editor.testing = false;
            editor.active = true;
        }
        if !editor.active {
            return;
        }
    }
    let _ = garage;
    if canvas.is_empty() {
        commands.spawn((
            Sprite { color: Color::srgb(0.02, 0.02, 0.05), custom_size: Some(Vec2::new(720.0, 640.0)), ..default() },
            Transform::from_xyz(0.0, 0.0, 9.5),
            EdTag,
            GameTag,
        ));
        for y in 0..AH {
            for x in 0..AW {
                let p = world_of(x as f32 + 0.5, y as f32 + 0.5);
                commands.spawn((
                    Sprite { color: DIM, custom_size: Some(Vec2::splat(ACELL - 2.0)), ..default() },
                    Transform::from_xyz(p.x, p.y, 10.0),
                    EdCell(x, y),
                    EdTag,
                    GameTag,
                ));
            }
        }
        let legend = text(&mut commands, "", 12.0, AMBER, Vec3::new(0.0, -300.0, 10.5));
        commands.entity(legend).insert((EdHud, EdTag, GameTag));
        return;
    }
    for (i, key) in [KeyCode::Digit1, KeyCode::Digit2, KeyCode::Digit3, KeyCode::Digit4].iter().enumerate() {
        if keys.just_pressed(*key) {
            editor.brush = ED_BRUSHES[i].0;
            sfx("tick");
        }
    }
    if keys.just_pressed(KeyCode::KeyS) && keys.pressed(KeyCode::ShiftLeft) {
        let spawns: usize = editor.rows.iter().map(|r| r.chars().filter(|&c| c == 's').count()).sum();
        if spawns < 2 {
            if let Ok(mut t) = ed_hud.single_mut() {
                t.0 = "!! PLACE AT LEAST TWO SPAWNS (4) !!".into();
            }
            sfx("buzz");
        } else {
            let doc = ArenaDoc { v: 1, name: String::new(), rows: editor.rows.clone() };
            if let Ok(json) = serde_json::to_string(&doc) {
                crate::shell::save_level(&json);
                sfx("clear");
            }
        }
        return;
    }
    if keys.just_pressed(KeyCode::KeyG) {
        // Test-play: tear down and restart the Playing phase on this canvas.
        editor.active = false;
        editor.testing = true;
        for e in &canvas {
            commands.entity(e).despawn();
        }
        crate::shell::mark_editor_pending(); // setup would re-open the editor;
        crate::shell::take_editor_pending(); // consume it: we want a ROUND.
        #[cfg(target_arch = "wasm32")]
        {
            let doc = ArenaDoc { v: 1, name: String::new(), rows: editor.rows.clone() };
            if let Ok(json) = serde_json::to_string(&doc) {
                let _ = js_sys::Reflect::set(
                    &js_sys::global(),
                    &"__ARCADE_LEVEL".into(),
                    &json.into(),
                );
            }
        }
        next.set(Phase::Playing);
        sfx("coin");
        return;
    }
    let place = buttons.pressed(MouseButton::Left);
    let erase = buttons.pressed(MouseButton::Right);
    if place || erase {
        if let (Ok(window), Ok((camera, cam_tf))) = (windows.single(), cameras.single()) {
            if let Some(world) = crate::retro::cursor_world(window, camera, cam_tf) {
                let x = ((world.x + 360.0) / ACELL).floor() as i32;
                let y = ((250.0 - world.y) / ACELL + 1.0).floor() as i32 - 1;
                if x > 0 && (x as usize) < AW - 1 && y > 0 && (y as usize) < AH - 1 {
                    let ch = if erase { '.' } else { editor.brush };
                    let mut chars: Vec<char> = editor.rows[y as usize].chars().collect();
                    chars[x as usize] = ch;
                    editor.rows[y as usize] = chars.into_iter().collect();
                }
            }
        }
    }
    for (c, mut sp) in &mut cells {
        let ch = cell_at(&editor.rows, c.0 as i32, c.1 as i32);
        let want = ed_color(ch);
        if sp.color != want {
            sp.color = want;
        }
    }
    if let Ok(mut t) = ed_hud.single_mut() {
        let name = ED_BRUSHES.iter().find(|(c, _)| *c == editor.brush).map(|(_, n)| *n).unwrap_or("?");
        let s = format!(
            "GARAGE EDITOR - BRUSH: {name}\n1 WALL 2 FLOOR 3 BOX 4 SPAWN - LCLICK PAINTS - RCLICK ERASES - SHIFT+S SAVES - G TEST-PLAYS - X RETURNS"
        );
        if t.0 != s {
            t.0 = s;
        }
    }
}
