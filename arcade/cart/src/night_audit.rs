//! NIGHT AUDIT — an original after-hours infiltration shooter in the
//! corridor-crawling style of the late-90s console classics: a raycast
//! first-person view, patrolling guards, and a mission with OBJECTIVES
//! rather than a kill count. Lift the intel, bug the server, walk out.
//! Original name, art, story, and map; genre mechanics only. The sidearm
//! fires tranquilizer darts — nobody dies at this office, they nap.
//!
//! Controls: W/S (or ↑/↓) walk, A/D strafe, ←/→ turn, Space fires,
//! E interacts. All state is grid + angles; rendering is 180 shaded
//! column sprites (a DDA raycast per column) plus billboard sprites for
//! guards and pickups, occluded against the column depth buffer.

use bevy::prelude::*;
use serde::{Deserialize, Serialize};

use crate::retro::{text, PLAYER_COLORS, AMBER, CYAN, DIM, GREEN, MAGENTA, RED, WHITE};
use crate::rng::Rng;
use crate::shell::{net_send, sfx, stat};
use crate::{FinalScore, GameTag, NetIn, NetMode, Phase};

pub const BLURB: &[&str] = &[
    "THE BOOKS DON'T SLEEP. NEITHER DO YOU.",
    "WASD WALKS / ARROWS TURN / SPACE DARTS / E INTERACTS",
    "SOLO: LIFT FILES, BUG THE SERVER, WALK OUT.",
    "ONLINE: OFFICE PARTY - THREE DARTS AND YOU NAP.",
];

/// Deathmatch tuning: three darts and you nap, first to ten (or the best
/// score when the party clock runs out) takes the office.
const DM_HP: i32 = 3;
const DM_TARGET: u32 = 10;
const DM_CLOCK: f32 = 240.0;
/// Fallback party spawns (used when a map carries no 's' markers): corners,
/// mid-edges, and inner corners — twelve seats, spread wide.
const DM_SPAWNS: [(f32, f32); 12] = [
    (1.5, 1.5),
    (22.5, 21.5),
    (22.5, 1.5),
    (1.5, 21.5),
    (12.5, 1.5),
    (12.5, 21.5),
    (1.5, 9.5),
    (22.5, 9.5),
    (7.5, 7.5),
    (16.5, 17.5),
    (16.5, 7.5),
    (7.5, 17.5),
];

const MW: usize = 24;
const MH: usize = 24;
const FOV: f32 = 1.15; // radians
const NCOL: usize = 180;
const COLW: f32 = 4.0;
const VIEW_H: f32 = 520.0; // the 3D window; HUD lives below
const VIEW_CY: f32 = 40.0; // vertical center of the 3D window
const MOVE_SPEED: f32 = 3.1; // cells/s
const TURN_SPEED: f32 = 2.4; // rad/s
const GUARD_HP: i32 = 2;

/// The office, after hours. '#' concrete, 'W' wood paneling, 'S' server
/// racks, 'D' a door that opens for anyone (E), 'X' the extraction door
/// (opens only once the paperwork objectives are done). Lowercase letters
/// are floor markers: p start, g guard post, f intel file, a darts,
/// c coffee, v the server console (stand here, press E).
const MAP: [&str; MH] = [
    "########################",
    "#p....D....#...f...#...#",
    "#.....#....#...#...#.g.#",
    "###D###.g..#...#...D...#",
    "#.....#....D...#...#####",
    "#..g..#....#...#.......#",
    "#.....######...####D####",
    "#.a...#....#......#....#",
    "####D##....D..g...#..c.#",
    "#....#..c..#......D....#",
    "#....#.....#...a..#..g.#",
    "#.f..###D###......#....#",
    "#....#....########ded###",
    "#....D....#......#ddd..#",
    "######....#.vSSd.#ddd..#",
    "#....#....D.dddd.#ddd..#",
    "#.g..#....#.dddd.####D##",
    "#....######......#.....#",
    "#.a.......D......D..f..#",
    "#....#....#......#.....#",
    "###D##....########..g..#",
    "#........g#......#.....#",
    "#....#....D..c...####X##",
    "########################",
];

#[derive(Clone, Copy, PartialEq)]
enum Cell {
    Open,
    Wall(u8), // 1 concrete, 2 wood, 3 server, 4 door(open=walkable? no: closed), 5 exit
}

/// Everything a parsed office yields. One parser serves the built-in map,
/// the editor's canvas, and maps arriving over the relay.
struct Parsed {
    grid: Vec<Cell>,
    guards: Vec<Guard>,
    pickups: Vec<Pickup>,
    px: f32,
    py: f32,
    server_cell: (usize, usize),
    exit_cell: (usize, usize),
    files_total: u32,
    spawns: Vec<(f32, f32)>,
}

fn parse_office(rows: &[String]) -> Parsed {
    let mut out = Parsed {
        grid: vec![Cell::Open; MW * MH],
        guards: Vec::new(),
        pickups: Vec::new(),
        px: 1.5,
        py: 1.5,
        server_cell: (0, 0),
        exit_cell: (0, 0),
        files_total: 0,
        spawns: Vec::new(),
    };
    for (y, row) in rows.iter().enumerate().take(MH) {
        for (x, ch) in row.chars().enumerate().take(MW) {
            let fx = x as f32 + 0.5;
            let fy = y as f32 + 0.5;
            out.grid[y * MW + x] = match ch {
                '#' => Cell::Wall(1),
                'W' => Cell::Wall(2),
                'S' => Cell::Wall(3),
                'D' => Cell::Wall(4),
                'X' => {
                    out.exit_cell = (x, y);
                    Cell::Wall(5)
                }
                'p' => {
                    out.px = fx;
                    out.py = fy;
                    Cell::Open
                }
                's' => {
                    out.spawns.push((fx, fy));
                    Cell::Open
                }
                'g' => {
                    out.guards.push(Guard {
                        x: fx,
                        y: fy,
                        hp: GUARD_HP,
                        alert: false,
                        shoot_cd: 0.0,
                        wander_cd: 0.0,
                        dir: Vec2::X,
                    });
                    Cell::Open
                }
                'f' => {
                    out.files_total += 1;
                    out.pickups.push(Pickup { x: fx, y: fy, kind: PickupKind::File, taken: false });
                    Cell::Open
                }
                'a' => {
                    out.pickups.push(Pickup { x: fx, y: fy, kind: PickupKind::Darts, taken: false });
                    Cell::Open
                }
                'c' => {
                    out.pickups.push(Pickup { x: fx, y: fy, kind: PickupKind::Coffee, taken: false });
                    Cell::Open
                }
                'v' => {
                    out.server_cell = (x, y);
                    Cell::Open
                }
                _ => Cell::Open,
            };
        }
    }
    // The border always holds, whatever a document claims.
    for y in 0..MH {
        for x in 0..MW {
            if (x == 0 || y == 0 || x == MW - 1 || y == MH - 1) && out.grid[y * MW + x] == Cell::Open {
                out.grid[y * MW + x] = Cell::Wall(1);
            }
        }
    }
    // Twelve party spawns, padding from the classics when the map is shy.
    let mut i = 0;
    while out.spawns.len() < 12 {
        out.spawns.push(DM_SPAWNS[i % DM_SPAWNS.len()]);
        i += 1;
    }
    out
}

/// A shareable office: 24 rows of 24 characters, the MAP alphabet verbatim.
#[derive(serde::Serialize, serde::Deserialize, Clone)]
struct OfficeDoc {
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
            // The editor's empty canvas: a bare shell with the essentials.
            let mut rows: Vec<String> = (0..MH)
                .map(|y| {
                    (0..MW)
                        .map(|x| if x == 0 || y == 0 || x == MW - 1 || y == MH - 1 { '#' } else { '.' })
                        .collect()
                })
                .collect();
            rows[1].replace_range(1..2, "p");
            rows[MH - 2].replace_range(MW - 3..MW - 2, "v");
            rows[MH - 1].replace_range(MW / 2..MW / 2 + 1, "X");
            return Some(rows);
        }
        if let Ok(doc) = serde_json::from_str::<OfficeDoc>(&raw) {
            if doc.rows.len() == MH && doc.rows.iter().all(|r| r.chars().count() == MW) {
                return Some(doc.rows);
            }
        }
    }
    None
}

#[derive(Resource)]
struct Mission {
    grid: Vec<Cell>,
    doors_open: Vec<bool>, // parallel: a door cell toggled open
    px: f32,
    py: f32,
    ang: f32,
    hp: i32,
    darts: i32,
    files: u32,
    files_total: u32,
    server_bugged: bool,
    server_cell: (usize, usize),
    exit_cell: (usize, usize),
    spotted: u32,
    fire_cd: f32,
    flash: f32,   // muzzle flash frames
    hurt: f32,    // red damage veil
    clock: f32,
    over: Option<Timer>,
    result: String,
    score: u32,
    done: bool,
    /// The map as text, kept for the editor and for handing to guests.
    rows: Vec<String>,
    /// Party spawn points parsed from the map ('s'), padded to twelve.
    spawns: Vec<(f32, f32)>,
    /// OFFICE PARTY (online): peers trade positions and hit claims over the
    /// room relay. Victim-authoritative health, shooter-authoritative aim —
    /// friendly-arcade honesty, same as every self-reported score here.
    dm: bool,
    my_seat: usize,
    dm_hp: i32,
    dm_scores: Vec<u32>,
    dm_clock: f32,
    nap_t: f32,
    pos_timer: Timer,
    fired_flag: bool,
}

/// A rival auditor as last heard from, eased between updates.
struct Remote {
    seat: usize,
    x: f32,
    y: f32,
    px: f32,
    py: f32,
    t: f32,
    napping: bool,
    heard: bool,
}

#[derive(Resource, Default)]
struct Remotes(Vec<Remote>);

#[derive(Serialize, Deserialize)]
struct WirePos {
    t: String, // "pos"
    x: f32,
    y: f32,
    a: f32,
    f: bool, // fired since the last update (remote muzzle pop)
    n: bool, // napping
}

#[derive(Serialize, Deserialize)]
struct WireHit {
    t: String, // "hit"
    v: u8,     // victim seat
}

#[derive(Serialize, Deserialize)]
struct WireNap {
    t: String, // "nap"
    by: u8,
}

#[derive(Serialize, Deserialize)]
struct WireDoor {
    t: String, // "door"
    x: i32,
    y: i32,
}

#[derive(Serialize, Deserialize)]
struct WireEnd {
    t: String, // "end"
    scores: Vec<u32>,
}

#[derive(Serialize, Deserialize)]
struct WireLevel {
    t: String, // "lv"
    rows: Vec<String>,
}

/// The office map editor: a top-down canvas painted with the MAP alphabet.
#[derive(Resource)]
struct OfficeEditor {
    active: bool,
    testing: bool,
    rows: Vec<String>,
    brush: char,
}

fn editor_off(editor: Option<Res<OfficeEditor>>) -> bool {
    editor.map(|e| !e.active).unwrap_or(true)
}

impl Mission {
    fn at(&self, x: i32, y: i32) -> Cell {
        if x < 0 || y < 0 || x >= MW as i32 || y >= MH as i32 {
            return Cell::Wall(1);
        }
        self.grid[y as usize * MW + x as usize]
    }
    fn solid(&self, x: i32, y: i32) -> bool {
        match self.at(x, y) {
            Cell::Open => false,
            Cell::Wall(4) => !self.doors_open[y as usize * MW + x as usize],
            Cell::Wall(5) => !self.objectives_done(),
            Cell::Wall(_) => true,
        }
    }
    fn objectives_done(&self) -> bool {
        self.files >= self.files_total && self.server_bugged
    }
}

struct Guard {
    x: f32,
    y: f32,
    hp: i32,
    alert: bool,
    shoot_cd: f32,
    wander_cd: f32,
    dir: Vec2,
}

#[derive(Resource, Default)]
struct Guards(Vec<Guard>);

#[derive(Clone, Copy, PartialEq)]
enum PickupKind {
    File,
    Darts,
    Coffee,
}

struct Pickup {
    x: f32,
    y: f32,
    kind: PickupKind,
    taken: bool,
}

#[derive(Resource, Default)]
struct Pickups(Vec<Pickup>);

/// Per-frame render pools: the wall columns and a fixed pool of billboards.
#[derive(Resource)]
struct View {
    cols: Vec<Entity>,
    depth: Vec<f32>,
    bills: Vec<Entity>, // reused for guards + pickups, nearest first
}

#[derive(Component)]
struct HudText;

#[derive(Component)]
struct ObjText;

#[derive(Component)]
struct Veil;

pub struct NightAuditPlugin;

impl Plugin for NightAuditPlugin {
    fn build(&self, app: &mut App) {
        app.init_resource::<Remotes>()
            .add_systems(OnEnter(Phase::Playing), setup)
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
                (
                    net_apply,
                    player_move,
                    interact,
                    fire,
                    guards_think,
                    dm_run,
                    render_view,
                    hud,
                    endgame,
                )
                    .chain()
                    .run_if(in_state(Phase::Playing))
                    .run_if(crate::unpaused)
                    .run_if(editor_off),
            );
    }
}

fn setup(
    mut commands: Commands,
    net: Res<NetMode>,
    existing_editor: Option<ResMut<OfficeEditor>>,
) {
    let editor_mode = crate::shell::take_editor_pending();
    let base_rows: Vec<String> = MAP.iter().map(|r| r.to_string()).collect();
    let rows = page_level().unwrap_or(base_rows);
    // The editor survives rounds, same pattern as every other cabinet.
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
            commands.insert_resource(OfficeEditor {
                active: editing,
                testing: false,
                rows: rows.clone(),
                brush: '#',
            });
        }
    }
    let parsed = parse_office(&rows);
    let Parsed {
        grid,
        mut guards,
        mut pickups,
        mut px,
        mut py,
        server_cell,
        exit_cell,
        files_total,
        spawns,
    } = parsed;
    let dm = net.0.is_some() && !editor_mode;
    let my_seat = net.0.as_ref().map(|c| c.seat as usize).unwrap_or(0);
    let seats = net.0.as_ref().map(|c| c.seats as usize).unwrap_or(1);
    if dm {
        // The party spreads across the map's spawn markers; guards are off.
        let (sx, sy) = spawns[my_seat % spawns.len()];
        px = sx;
        py = sy;
        guards.clear();
        for p in pickups.iter_mut() {
            p.taken = true; // deathmatch is dart tag: no pickups, no errands
        }
        // The host hands the room its map before anyone moves.
        if net.0.as_ref().map(|c| c.is_host()).unwrap_or(false) {
            if let Ok(w) = serde_json::to_string(&WireLevel { t: "lv".into(), rows: rows.clone() }) {
                net_send(&w);
            }
        }
    }
    let mut remotes = Vec::new();
    if let Some(cfg) = &net.0 {
        for (s, present) in cfg.present.iter().enumerate() {
            if *present && s != my_seat {
                let (rx, ry) = spawns[s % spawns.len()];
                remotes.push(Remote {
                    seat: s,
                    x: rx,
                    y: ry,
                    px: rx,
                    py: ry,
                    t: 1.0,
                    napping: false,
                    heard: false,
                });
            }
        }
    }
    commands.insert_resource(Remotes(remotes));
    commands.insert_resource(Mission {
        rows,
        spawns,
        dm,
        my_seat,
        dm_hp: DM_HP,
        dm_scores: vec![0; seats.max(1)],
        dm_clock: DM_CLOCK,
        nap_t: 0.0,
        pos_timer: Timer::from_seconds(0.1, TimerMode::Repeating),
        fired_flag: false,
        grid,
        doors_open: vec![false; MW * MH],
        px,
        py,
        ang: 0.0,
        hp: 100,
        darts: if dm { 9999 } else { 24 },
        files: 0,
        files_total,
        server_bugged: false,
        server_cell,
        exit_cell,
        spotted: 0,
        fire_cd: 0.0,
        flash: 0.0,
        hurt: 0.0,
        clock: 0.0,
        over: None,
        result: String::new(),
        score: 0,
        done: false,
    });
    commands.insert_resource(Guards(guards));
    commands.insert_resource(Pickups(pickups));

    // Ceiling and floor plates behind the columns.
    commands.spawn((
        Sprite { color: Color::srgb(0.05, 0.06, 0.10), custom_size: Some(Vec2::new(720.0, VIEW_H / 2.0)), ..default() },
        Transform::from_xyz(0.0, VIEW_CY + VIEW_H / 4.0, 0.5),
        GameTag,
    ));
    commands.spawn((
        Sprite { color: Color::srgb(0.09, 0.08, 0.07), custom_size: Some(Vec2::new(720.0, VIEW_H / 2.0)), ..default() },
        Transform::from_xyz(0.0, VIEW_CY - VIEW_H / 4.0, 0.5),
        GameTag,
    ));
    // The column pool.
    let mut cols = Vec::with_capacity(NCOL);
    for i in 0..NCOL {
        let x = -360.0 + COLW / 2.0 + i as f32 * COLW;
        let e = commands
            .spawn((
                Sprite { color: DIM, custom_size: Some(Vec2::new(COLW, 10.0)), ..default() },
                Transform::from_xyz(x, VIEW_CY, 1.0),
                GameTag,
            ))
            .id();
        cols.push(e);
    }
    // Billboard pool: plenty for every guard and pickup on screen at once.
    let mut bills = Vec::with_capacity(24);
    for _ in 0..24 {
        let e = commands
            .spawn((
                Sprite { color: WHITE, custom_size: Some(Vec2::splat(10.0)), ..default() },
                Transform::from_xyz(0.0, VIEW_CY, 2.0),
                Visibility::Hidden,
                GameTag,
            ))
            .id();
        bills.push(e);
    }
    commands.insert_resource(View { cols, depth: vec![f32::MAX; NCOL], bills });

    // Crosshair.
    commands.spawn((
        Sprite { color: GREEN, custom_size: Some(Vec2::new(2.0, 10.0)), ..default() },
        Transform::from_xyz(0.0, VIEW_CY, 6.0),
        GameTag,
    ));
    commands.spawn((
        Sprite { color: GREEN, custom_size: Some(Vec2::new(10.0, 2.0)), ..default() },
        Transform::from_xyz(0.0, VIEW_CY, 6.0),
        GameTag,
    ));
    // Damage veil (alpha animated).
    commands.spawn((
        Sprite { color: RED.with_alpha(0.0), custom_size: Some(Vec2::new(720.0, VIEW_H)), ..default() },
        Transform::from_xyz(0.0, VIEW_CY, 5.5),
        Veil,
        GameTag,
    ));
    let hud = text(&mut commands, "", 18.0, WHITE, Vec3::new(0.0, -282.0, 6.0));
    commands.entity(hud).insert((HudText, GameTag));
    let obj = text(&mut commands, "", 12.0, AMBER, Vec3::new(0.0, -304.0, 6.0));
    commands.entity(obj).insert((ObjText, GameTag));
}

fn try_move(m: &Mission, x: f32, y: f32) -> bool {
    // A little body radius so walls don't shave the camera.
    let r = 0.22;
    for (dx, dy) in [(-r, -r), (r, -r), (-r, r), (r, r)] {
        if m.solid((x + dx).floor() as i32, (y + dy).floor() as i32) {
            return false;
        }
    }
    true
}

fn player_move(time: Res<Time>, keys: Res<ButtonInput<KeyCode>>, mut m: ResMut<Mission>) {
    if m.over.is_some() || m.nap_t > 0.0 {
        return;
    }
    let dt = time.delta_secs();
    m.clock += dt;
    m.fire_cd = (m.fire_cd - dt).max(0.0);
    m.flash = (m.flash - dt).max(0.0);
    m.hurt = (m.hurt - dt).max(0.0);
    let turn = i32::from(keys.pressed(KeyCode::ArrowRight)) - i32::from(keys.pressed(KeyCode::ArrowLeft));
    m.ang += turn as f32 * TURN_SPEED * dt;
    let fwd = i32::from(keys.pressed(KeyCode::KeyW) || keys.pressed(KeyCode::ArrowUp)) as f32
        - i32::from(keys.pressed(KeyCode::KeyS) || keys.pressed(KeyCode::ArrowDown)) as f32;
    let side = i32::from(keys.pressed(KeyCode::KeyD)) as f32 - i32::from(keys.pressed(KeyCode::KeyA)) as f32;
    let dir = Vec2::new(m.ang.cos(), m.ang.sin());
    let right = Vec2::new(-dir.y, dir.x);
    let step = (dir * fwd + right * side).clamp_length_max(1.0) * MOVE_SPEED * dt;
    let (nx, ny) = (m.px + step.x, m.py + step.y);
    // Slide along walls: try the full move, then each axis alone.
    if try_move(&m, nx, ny) {
        m.px = nx;
        m.py = ny;
    } else if try_move(&m, nx, m.py) {
        m.px = nx;
    } else if try_move(&m, m.px, ny) {
        m.py = ny;
    }
}

fn interact(keys: Res<ButtonInput<KeyCode>>, mut m: ResMut<Mission>, mut pickups: ResMut<Pickups>) {
    if m.over.is_some() || m.nap_t > 0.0 {
        return;
    }
    // Walk-over pickups need no key at all.
    let (px, py) = (m.px, m.py);
    for p in pickups.0.iter_mut() {
        if p.taken || (p.x - px).abs() + (p.y - py).abs() > 0.8 {
            continue;
        }
        p.taken = true;
        match p.kind {
            PickupKind::File => {
                m.files += 1;
                m.score += 500;
                stat("files_lifted", 1);
                sfx("coin");
            }
            PickupKind::Darts => {
                m.darts += 12;
                sfx("eat");
            }
            PickupKind::Coffee => {
                m.hp = (m.hp + 25).min(100);
                stat("coffees_drunk", 1);
                sfx("power");
            }
        }
    }
    if !keys.just_pressed(KeyCode::KeyE) {
        return;
    }
    // Doors: open the cell one step ahead (they stay open — it's an office).
    let dir = Vec2::new(m.ang.cos(), m.ang.sin());
    let (tx, ty) = ((m.px + dir.x * 1.0).floor() as i32, (m.py + dir.y * 1.0).floor() as i32);
    if let Cell::Wall(4) = m.at(tx, ty) {
        let idx = ty as usize * MW + tx as usize;
        if !m.doors_open[idx] {
            m.doors_open[idx] = true;
            sfx("drop");
            if m.dm {
                // The whole party shares one office: doors open for everyone.
                if let Ok(w) = serde_json::to_string(&WireDoor { t: "door".into(), x: tx, y: ty }) {
                    net_send(&w);
                }
            }
            return;
        }
    }
    if m.dm {
        return; // no errands at the office party
    }
    // The server console: stand on the marked tile, plant the bug.
    let (cx, cy) = (m.px.floor() as usize, m.py.floor() as usize);
    if !m.server_bugged && (cx, cy) == m.server_cell {
        m.server_bugged = true;
        m.score += 800;
        stat("servers_bugged", 1);
        sfx("clear");
    }
}

/// Applies relay traffic: rival positions, hit claims against ME, naps
/// (scoreboard), doors opening elsewhere, and the host's final horn.
fn net_apply(
    mut events: EventReader<NetIn>,
    net: Res<NetMode>,
    mut m: ResMut<Mission>,
    mut remotes: ResMut<Remotes>,
) {
    let Some(cfg) = &net.0 else {
        events.clear();
        return;
    };
    for ev in events.read() {
        if ev.left {
            remotes.0.retain(|r| r.seat != ev.seat as usize);
            if remotes.0.is_empty() && m.over.is_none() {
                m.result = "EVERYONE ELSE WENT HOME.\nTHE OFFICE IS YOURS.".into();
                m.score += m.dm_scores[m.my_seat] * 100 + 500;
                m.over = Some(Timer::from_seconds(2.2, TimerMode::Once));
                sfx("win");
            }
            continue;
        }
        if ev.seat == cfg.seat {
            continue;
        }
        let Ok(v) = serde_json::from_str::<serde_json::Value>(&ev.data) else { continue };
        match v.get("t").and_then(|t| t.as_str()) {
            Some("pos") => {
                if let Ok(p) = serde_json::from_str::<WirePos>(&ev.data) {
                    if let Some(r) = remotes.0.iter_mut().find(|r| r.seat == ev.seat as usize) {
                        r.px = if r.heard { r.x } else { p.x };
                        r.py = if r.heard { r.y } else { p.y };
                        r.x = p.x;
                        r.y = p.y;
                        r.t = 0.0;
                        r.napping = p.n;
                        r.heard = true;
                        if p.f {
                            sfx("drop"); // a dart pops somewhere in the office
                        }
                    }
                }
            }
            Some("hit") => {
                if let Ok(h) = serde_json::from_str::<WireHit>(&ev.data) {
                    if h.v == cfg.seat && m.nap_t <= 0.0 && m.over.is_none() {
                        m.dm_hp -= 1;
                        m.hurt = 0.4;
                        sfx("death");
                        if m.dm_hp <= 0 {
                            // Down for a nap: tell the office who did it.
                            m.nap_t = 3.0;
                            let shooter = ev.seat;
                            if let Ok(w) =
                                serde_json::to_string(&WireNap { t: "nap".into(), by: shooter })
                            {
                                net_send(&w);
                            }
                            if (shooter as usize) < m.dm_scores.len() {
                                m.dm_scores[shooter as usize] += 1;
                            }
                            stat("audits_failed", 1);
                            sfx("over");
                        }
                    }
                }
            }
            Some("nap") => {
                if let Ok(n) = serde_json::from_str::<WireNap>(&ev.data) {
                    if (n.by as usize) < m.dm_scores.len() {
                        m.dm_scores[n.by as usize] += 1;
                    }
                    if n.by == cfg.seat {
                        m.score += 150;
                        stat("guards_tranqed", 1);
                        sfx("capture");
                    }
                }
            }
            Some("door") => {
                if let Ok(d) = serde_json::from_str::<WireDoor>(&ev.data) {
                    if d.x >= 0 && d.y >= 0 && (d.x as usize) < MW && (d.y as usize) < MH {
                        m.doors_open[d.y as usize * MW + d.x as usize] = true;
                    }
                }
            }
            Some("end") => {
                if let Ok(e) = serde_json::from_str::<WireEnd>(&ev.data) {
                    if m.over.is_none() {
                        finish_party(&mut m, &e.scores);
                    }
                }
            }
            Some("lv") => {
                // The host's map, before anyone has really moved: rebuild the
                // grid in place and stand everyone on its spawn markers.
                if let Ok(lv) = serde_json::from_str::<WireLevel>(&ev.data) {
                    if lv.rows.len() == MH
                        && lv.rows.iter().all(|r| r.chars().count() == MW)
                        && lv.rows != m.rows
                    {
                        let parsed = parse_office(&lv.rows);
                        m.grid = parsed.grid;
                        m.doors_open = vec![false; MW * MH];
                        m.spawns = parsed.spawns;
                        m.rows = lv.rows;
                        let (sx, sy) = m.spawns[m.my_seat % m.spawns.len()];
                        m.px = sx;
                        m.py = sy;
                        for r in remotes.0.iter_mut() {
                            let (rx, ry) = m.spawns[r.seat % m.spawns.len()];
                            r.x = rx;
                            r.y = ry;
                            r.px = rx;
                            r.py = ry;
                        }
                    }
                }
            }
            _ => {}
        }
    }
}

/// The party clock, my position broadcasts, naps and respawns, and the
/// host's whistle when time or the target is reached.
fn dm_run(
    time: Res<Time>,
    net: Res<NetMode>,
    mut m: ResMut<Mission>,
    mut remotes: ResMut<Remotes>,
    mut rng: ResMut<Rng>,
) {
    if !m.dm || m.over.is_some() {
        return;
    }
    let dt = time.delta_secs();
    m.dm_clock -= dt;
    for r in remotes.0.iter_mut() {
        r.t = (r.t + dt / 0.1).min(1.0);
    }
    // Napping: the room spins gently, then you're back in a random corner.
    if m.nap_t > 0.0 {
        m.nap_t -= dt;
        m.ang += dt * 1.2;
        if m.nap_t <= 0.0 {
            let idx = rng.range(m.spawns.len() as u32) as usize;
            let (sx, sy) = m.spawns[idx];
            m.px = sx;
            m.py = sy;
            m.dm_hp = DM_HP;
            m.hurt = 0.0;
        }
    }
    // Everyone streams their own position; f carries the muzzle pop.
    if m.pos_timer.tick(time.delta()).just_finished() {
        let msg = WirePos {
            t: "pos".into(),
            x: m.px,
            y: m.py,
            a: m.ang,
            f: m.fired_flag,
            n: m.nap_t > 0.0,
        };
        m.fired_flag = false;
        if let Ok(w) = serde_json::to_string(&msg) {
            net_send(&w);
        }
    }
    // The host calls time (or the winning tranq) for everyone.
    let is_host = net.0.as_ref().map(|c| c.is_host()).unwrap_or(false);
    if is_host && (m.dm_clock <= 0.0 || m.dm_scores.iter().any(|&s| s >= DM_TARGET)) {
        let scores = m.dm_scores.clone();
        if let Ok(w) = serde_json::to_string(&WireEnd { t: "end".into(), scores: scores.clone() }) {
            net_send(&w);
        }
        finish_party(&mut m, &scores);
    }
}

/// Standings, my payout, and the horn.
fn finish_party(m: &mut Mission, scores: &[u32]) {
    let mine = scores.get(m.my_seat).copied().unwrap_or(0);
    let best = scores.iter().copied().max().unwrap_or(0);
    let won = mine == best && best > 0;
    let lines: Vec<String> = scores
        .iter()
        .enumerate()
        .map(|(s, n)| format!("AUDITOR {}: {} TRANQS{}", s + 1, n, if s == m.my_seat { " (YOU)" } else { "" }))
        .collect();
    m.result = format!(
        "{}\n{}",
        if won { "THE OFFICE IS YOURS." } else { "PARTY'S OVER." },
        lines.join("\n")
    );
    m.score += mine * 100 + if won { 500 } else { 0 };
    if won {
        stat("extractions", 1);
    }
    m.over = Some(Timer::from_seconds(3.0, TimerMode::Once));
    sfx(if won { "win" } else { "over" });
}

/// A grid ray via DDA: returns (distance, wall kind, vertical-face flag).
fn cast(m: &Mission, ox: f32, oy: f32, ang: f32, max: f32) -> (f32, u8, bool) {
    let (dx, dy) = (ang.cos(), ang.sin());
    let (mut mx, mut my) = (ox.floor() as i32, oy.floor() as i32);
    let ddx = if dx == 0.0 { f32::MAX } else { (1.0 / dx).abs() };
    let ddy = if dy == 0.0 { f32::MAX } else { (1.0 / dy).abs() };
    let (sx, mut tx) = if dx < 0.0 { (-1, (ox - mx as f32) * ddx) } else { (1, (mx as f32 + 1.0 - ox) * ddx) };
    let (sy, mut ty) = if dy < 0.0 { (-1, (oy - my as f32) * ddy) } else { (1, (my as f32 + 1.0 - oy) * ddy) };
    let mut vertical;
    for _ in 0..96 {
        if tx < ty {
            mx += sx;
            tx += ddx;
            vertical = true;
        } else {
            my += sy;
            ty += ddy;
            vertical = false;
        }
        if m.solid(mx, my) {
            let kind = match m.at(mx, my) {
                Cell::Wall(k) => k,
                Cell::Open => 1,
            };
            let d = if vertical { tx - ddx } else { ty - ddy };
            return (d.min(max), kind, vertical);
        }
        let d = if vertical { tx - ddx } else { ty - ddy };
        if d > max {
            break;
        }
    }
    (max, 0, false)
}

/// Clear line of sight between two points (doors count once opened).
fn los(m: &Mission, ax: f32, ay: f32, bx: f32, by: f32) -> bool {
    let d = ((bx - ax).powi(2) + (by - ay).powi(2)).sqrt();
    if d < 0.001 {
        return true;
    }
    let ang = (by - ay).atan2(bx - ax);
    let (hit, _, _) = cast(m, ax, ay, ang, d);
    hit >= d - 0.05
}

fn fire(
    keys: Res<ButtonInput<KeyCode>>,
    buttons: Res<ButtonInput<MouseButton>>,
    mut m: ResMut<Mission>,
    mut guards: ResMut<Guards>,
    remotes: Res<Remotes>,
) {
    if m.over.is_some() || m.nap_t > 0.0 {
        return;
    }
    let pressed = keys.just_pressed(KeyCode::Space) || buttons.just_pressed(MouseButton::Left);
    if !pressed || m.fire_cd > 0.0 {
        return;
    }
    if m.darts <= 0 {
        sfx("tick"); // dry click: the office is out of tranquilizer
        return;
    }
    m.darts -= 1;
    m.fire_cd = 0.45;
    m.flash = 0.08;
    m.fired_flag = true;
    stat("darts_fired", 1);
    sfx("fire");
    // OFFICE PARTY: the same hitscan, aimed at rival auditors instead.
    if m.dm {
        let (wall_d, _, _) = cast(&m, m.px, m.py, m.ang, 24.0);
        let mut best: Option<(usize, f32)> = None;
        for r in remotes.0.iter().filter(|r| !r.napping && r.heard) {
            let rel = Vec2::new(r.x - m.px, r.y - m.py);
            let d = rel.length();
            if d > 20.0 || d >= wall_d + 0.3 {
                continue;
            }
            let mut da = rel.y.atan2(rel.x) - m.ang;
            while da > std::f32::consts::PI {
                da -= std::f32::consts::TAU;
            }
            while da < -std::f32::consts::PI {
                da += std::f32::consts::TAU;
            }
            if da.abs() < (0.05 + 0.25 / d.max(0.5)) && los(&m, m.px, m.py, r.x, r.y) {
                if best.map(|(_, bd)| d < bd).unwrap_or(true) {
                    best = Some((r.seat, d));
                }
            }
        }
        if let Some((seat, _)) = best {
            if let Ok(w) = serde_json::to_string(&WireHit { t: "hit".into(), v: seat as u8 }) {
                net_send(&w);
            }
            sfx("rotate"); // the thock of a dart landing
        }
        return;
    }
    // Hitscan: nearest awake guard within a narrow cone and line of sight.
    let (wall_d, _, _) = cast(&m, m.px, m.py, m.ang, 24.0);
    let mut best: Option<(usize, f32)> = None;
    for (i, g) in guards.0.iter().enumerate() {
        if g.hp <= 0 {
            continue;
        }
        let rel = Vec2::new(g.x - m.px, g.y - m.py);
        let d = rel.length();
        if d > 20.0 || d >= wall_d + 0.3 {
            continue;
        }
        let mut da = rel.y.atan2(rel.x) - m.ang;
        while da > std::f32::consts::PI {
            da -= std::f32::consts::TAU;
        }
        while da < -std::f32::consts::PI {
            da += std::f32::consts::TAU;
        }
        // The cone widens up close, like an actual arm.
        if da.abs() < (0.05 + 0.25 / d.max(0.5)) && los(&m, m.px, m.py, g.x, g.y) {
            if best.map(|(_, bd)| d < bd).unwrap_or(true) {
                best = Some((i, d));
            }
        }
    }
    if let Some((i, _)) = best {
        let g = &mut guards.0[i];
        g.hp -= 1;
        g.alert = true;
        if g.hp <= 0 {
            m.score += 150;
            stat("guards_tranqed", 1);
            sfx("capture");
        } else {
            sfx("rotate");
        }
    }
}

fn guards_think(
    time: Res<Time>,
    mut rng: ResMut<Rng>,
    mut m: ResMut<Mission>,
    mut guards: ResMut<Guards>,
) {
    if m.over.is_some() || m.dm {
        return;
    }
    let dt = time.delta_secs();
    for g in guards.0.iter_mut() {
        if g.hp <= 0 {
            continue;
        }
        let rel = Vec2::new(m.px - g.x, m.py - g.y);
        let dist = rel.length();
        let sees = dist < 9.0 && los(&m, g.x, g.y, m.px, m.py) && {
            // Guards have eyes in the front of their heads only.
            let facing = g.dir.normalize_or_zero();
            facing.dot(rel.normalize_or_zero()) > 0.1 || dist < 1.6
        };
        if sees && !g.alert {
            g.alert = true;
            m.spotted += 1;
            stat("times_spotted", 1);
            sfx("buzz");
        }
        if g.alert && !sees && dist > 12.0 {
            g.alert = false; // lost you: back to the rounds
        }
        if g.alert {
            // Close distance, keep a little spacing, take pot shots.
            g.dir = rel.normalize_or_zero();
            if dist > 2.0 {
                let step = g.dir * 2.1 * dt;
                let (nx, ny) = (g.x + step.x, g.y + step.y);
                if !m.solid(nx.floor() as i32, g.y.floor() as i32) {
                    g.x = nx;
                }
                if !m.solid(g.x.floor() as i32, ny.floor() as i32) {
                    g.y = ny;
                }
            }
            g.shoot_cd -= dt;
            if g.shoot_cd <= 0.0 && sees && dist < 8.0 {
                g.shoot_cd = 0.9;
                sfx("drop");
                // Aim wobbles with range; getting hit stings but telegraphs.
                let hit_chance = (0.85 - dist * 0.07).max(0.25);
                if rng.chance(hit_chance) {
                    let dmg = 6 + rng.range(8) as i32;
                    m.hp -= dmg;
                    m.hurt = 0.35;
                    sfx("death");
                }
            }
        } else {
            // Patrol: drift, bounce off walls, occasionally pick a new heading.
            g.wander_cd -= dt;
            if g.wander_cd <= 0.0 {
                g.wander_cd = 1.5 + rng.range(20) as f32 / 10.0;
                let a = rng.range(628) as f32 / 100.0;
                g.dir = Vec2::new(a.cos(), a.sin());
            }
            let step = g.dir * 0.9 * dt;
            let (nx, ny) = (g.x + step.x, g.y + step.y);
            if m.solid(nx.floor() as i32, ny.floor() as i32) {
                g.dir = -g.dir;
            } else {
                g.x = nx;
                g.y = ny;
            }
        }
    }
    if m.hp <= 0 && m.over.is_none() {
        m.result = "AUDITED.\nTHE GUARDS FILE THEIR REPORT.".into();
        m.over = Some(Timer::from_seconds(2.4, TimerMode::Once));
        stat("audits_failed", 1);
        sfx("over");
    }
    // Extraction: stand in the exit doorway once it's unlocked.
    let (ex, ey) = m.exit_cell;
    if m.objectives_done()
        && (m.px.floor() as usize, m.py.floor() as usize) == (ex, ey.saturating_sub(1))
        && m.over.is_none()
    {
        // Score: objectives + time + stealth.
        let time_bonus = ((360.0 - m.clock).max(0.0) * 8.0) as u32;
        let stealth_bonus = if m.spotted == 0 { 1500 } else { 500u32.saturating_sub(m.spotted * 100) };
        m.score += 1000 + time_bonus + stealth_bonus;
        if m.spotted == 0 {
            stat("ghost_runs", 1);
        }
        stat("extractions", 1);
        m.result = if m.spotted == 0 {
            "EXTRACTED. NOBODY EVER KNEW.\nA PERFECT NIGHT.".into()
        } else {
            "EXTRACTED WITH THE GOODS.".into()
        };
        m.done = true;
        m.over = Some(Timer::from_seconds(2.6, TimerMode::Once));
        sfx("win");
    }
}

/// The whole 3D view, every frame: raycast the wall columns, then splat
/// billboards (guards, pickups, muzzle flash, damage veil) over them.
#[allow(clippy::too_many_arguments)]
fn render_view(
    m: Res<Mission>,
    guards: Res<Guards>,
    pickups: Res<Pickups>,
    remotes: Res<Remotes>,
    mut view: ResMut<View>,
    mut sprites: Query<(&mut Sprite, &mut Transform, &mut Visibility), Without<Veil>>,
    mut veil: Query<&mut Sprite, With<Veil>>,
) {
    // Columns.
    for i in 0..NCOL {
        let lens = (i as f32 / (NCOL - 1) as f32 - 0.5) * FOV;
        let (d, kind, vertical) = cast(&m, m.px, m.py, m.ang + lens, 22.0);
        let dcorr = (d * lens.cos()).max(0.05);
        view.depth[i] = dcorr;
        let h = (VIEW_H * 0.9 / dcorr).min(VIEW_H);
        let base = match kind {
            2 => Color::srgb(0.42, 0.30, 0.16),
            3 => Color::srgb(0.16, 0.30, 0.44),
            4 => Color::srgb(0.34, 0.30, 0.22),
            5 => {
                if m.objectives_done() {
                    Color::srgb(0.10, 0.5, 0.22)
                } else {
                    Color::srgb(0.30, 0.10, 0.12)
                }
            }
            _ => Color::srgb(0.30, 0.32, 0.38),
        };
        let shade = (1.0 - (dcorr / 16.0)).clamp(0.15, 1.0) * if vertical { 1.0 } else { 0.78 };
        let c = base.to_srgba();
        if let Ok((mut sp, mut tf, mut vis)) = sprites.get_mut(view.cols[i]) {
            sp.color = Color::srgb(c.red * shade, c.green * shade, c.blue * shade);
            sp.custom_size = Some(Vec2::new(COLW, h));
            tf.translation.y = VIEW_CY;
            *vis = if kind == 0 { Visibility::Hidden } else { Visibility::Inherited };
        }
    }
    // Billboards, farthest first so near ones overwrite via z.
    struct Bill {
        x: f32,
        y: f32,
        w: f32,
        h: f32,
        color: Color,
        dy: f32, // vertical offset factor (pickups sit low)
    }
    let mut items: Vec<(f32, Bill)> = Vec::new();
    for g in &guards.0 {
        let color = if g.hp <= 0 {
            Color::srgb(0.35, 0.35, 0.40)
        } else if g.alert {
            RED
        } else {
            AMBER
        };
        let (h, dy) = if g.hp <= 0 { (0.35, -0.28) } else { (0.95, 0.0) };
        items.push((0.0, Bill { x: g.x, y: g.y, w: 0.5, h, color, dy }));
    }
    for r in remotes.0.iter().filter(|r| r.heard) {
        let (x, y) = (r.px + (r.x - r.px) * r.t, r.py + (r.y - r.py) * r.t);
        let color = PLAYER_COLORS[r.seat % 12];
        let (h, dy) = if r.napping { (0.35, -0.28) } else { (0.95, 0.0) };
        items.push((0.0, Bill { x, y, w: 0.5, h, color, dy }));
    }
    for p in pickups.0.iter().filter(|p| !p.taken) {
        let color = match p.kind {
            PickupKind::File => AMBER,
            PickupKind::Darts => CYAN,
            PickupKind::Coffee => MAGENTA,
        };
        items.push((0.0, Bill { x: p.x, y: p.y, w: 0.28, h: 0.3, color, dy: -0.3 }));
    }
    // The server console tile glows until bugged — the "go here" beacon.
    if !m.server_bugged && !m.dm {
        let (sx, sy) = m.server_cell;
        items.push((0.0, Bill { x: sx as f32 + 0.5, y: sy as f32 + 0.5, w: 0.4, h: 0.2, color: GREEN, dy: -0.35 }));
    }
    for it in items.iter_mut() {
        let rel = Vec2::new(it.1.x - m.px, it.1.y - m.py);
        it.0 = rel.length();
    }
    items.sort_by(|a, b| b.0.partial_cmp(&a.0).unwrap_or(std::cmp::Ordering::Equal));
    let mut used = 0;
    for (dist, b) in items {
        if used >= view.bills.len() || dist < 0.3 || dist > 20.0 {
            continue;
        }
        let mut da = (b.y - m.py).atan2(b.x - m.px) - m.ang;
        while da > std::f32::consts::PI {
            da -= std::f32::consts::TAU;
        }
        while da < -std::f32::consts::PI {
            da += std::f32::consts::TAU;
        }
        if da.abs() > FOV / 2.0 + 0.25 {
            continue;
        }
        let sx = (da / FOV + 0.5) * 720.0 - 360.0;
        let col = (((sx + 360.0) / COLW) as usize).min(NCOL - 1);
        let dcorr = dist * (da.cos()).max(0.3);
        if view.depth[col] + 0.4 < dcorr {
            continue; // a wall is in front
        }
        let scale = VIEW_H * 0.9 / dcorr.max(0.2);
        let e = view.bills[used];
        used += 1;
        if let Ok((mut sp, mut tf, mut vis)) = sprites.get_mut(e) {
            sp.color = b.color;
            sp.custom_size = Some(Vec2::new(scale * b.w, scale * b.h));
            tf.translation.x = sx;
            tf.translation.y = VIEW_CY + scale * b.dy;
            tf.translation.z = 2.0 + (30.0 - dist) * 0.01;
            *vis = Visibility::Inherited;
        }
    }
    for &e in view.bills.iter().skip(used) {
        if let Ok((_, _, mut vis)) = sprites.get_mut(e) {
            *vis = Visibility::Hidden;
        }
    }
    // Veils: damage red, muzzle pop, and a heavy lid while napping.
    if let Ok(mut sp) = veil.single_mut() {
        let a = if m.nap_t > 0.0 {
            0.55
        } else {
            (m.hurt * 0.9).min(0.35) + if m.flash > 0.0 { 0.10 } else { 0.0 }
        };
        sp.color = RED.with_alpha(a);
    }
}

fn hud(
    m: Res<Mission>,
    mut hud: Query<&mut Text2d, (With<HudText>, Without<ObjText>)>,
    mut obj: Query<&mut Text2d, (With<ObjText>, Without<HudText>)>,
) {
    if let Ok(mut t) = hud.single_mut() {
        let s = if m.over.is_some() {
            m.result.clone()
        } else if m.dm {
            if m.nap_t > 0.0 {
                format!("NAPPING... BACK IN {:.0}", m.nap_t.max(0.0) + 0.99)
            } else {
                format!(
                    "HITS LEFT {}   FIRST TO {}   {}",
                    m.dm_hp.max(0),
                    DM_TARGET,
                    fmt_clock(m.dm_clock.max(0.0))
                )
            }
        } else {
            format!("HEALTH {}   DARTS {}   {}", m.hp.max(0), m.darts, fmt_clock(m.clock))
        };
        if t.0 != s {
            t.0 = s;
        }
    }
    if let Ok(mut t) = obj.single_mut() {
        if m.dm {
            let s = m
                .dm_scores
                .iter()
                .enumerate()
                .map(|(i, n)| {
                    format!("A{}{}: {}", i + 1, if i == m.my_seat { "*" } else { "" }, n)
                })
                .collect::<Vec<_>>()
                .join("   ");
            if t.0 != s {
                t.0 = s;
            }
            return;
        }
        let tick = |b: bool| if b { "x" } else { "-" };
        let s = format!(
            "[{}] LIFT THE FILES ({}/{})   [{}] BUG THE SERVER   [{}] EXTRACT (GREEN DOOR)",
            tick(m.files >= m.files_total),
            m.files,
            m.files_total,
            tick(m.server_bugged),
            tick(m.done),
        );
        if t.0 != s {
            t.0 = s;
        }
    }
}

fn fmt_clock(secs: f32) -> String {
    let s = secs as u32;
    format!("{}:{:02}", s / 60, s % 60)
}

fn endgame(
    time: Res<Time>,
    mut m: ResMut<Mission>,
    editor: Option<ResMut<OfficeEditor>>,
    mut final_score: ResMut<FinalScore>,
    mut next: ResMut<NextState<Phase>>,
) {
    if let Some(t) = m.over.as_mut() {
        if t.tick(time.delta()).finished() {
            // A finished TEST run returns to the canvas: no score, no exit.
            if let Some(mut e) = editor {
                if e.testing {
                    e.testing = false;
                    e.active = true;
                    m.over = None;
                    m.score = 0;
                    return;
                }
            }
            final_score.0 = m.score;
            next.set(Phase::GameOver);
        }
    }
}

// ── the office map editor ────────────────────────────────────────────────

#[derive(Component)]
struct EditorTag;

#[derive(Component)]
struct EditorCell(usize, usize);

#[derive(Component)]
struct EditorHud;

const BRUSHES: [(char, &str); 10] = [
    ('#', "WALL"),
    ('W', "WOOD"),
    ('S', "RACKS"),
    ('D', "DOOR"),
    ('.', "FLOOR"),
    ('f', "FILE"),
    ('a', "DARTS"),
    ('c', "COFFEE"),
    ('g', "GUARD"),
    ('s', "SPAWN"),
];

fn cell_color(ch: char) -> Color {
    match ch {
        '#' => Color::srgb(0.34, 0.36, 0.42),
        'W' => Color::srgb(0.42, 0.30, 0.16),
        'S' => Color::srgb(0.16, 0.30, 0.44),
        'D' => Color::srgb(0.55, 0.48, 0.30),
        'X' => Color::srgb(0.10, 0.55, 0.22),
        'f' => AMBER,
        'a' => CYAN,
        'c' => MAGENTA,
        'g' => RED,
        's' => Color::srgb(0.30, 0.75, 0.35),
        'v' => WHITE,
        'p' => GREEN,
        _ => Color::srgb(0.07, 0.08, 0.12),
    }
}

const ED_CELL: f32 = 22.0;
const ED_X0: f32 = -264.0;
const ED_Y0: f32 = 282.0;

fn ed_cell_pos(x: usize, y: usize) -> Vec2 {
    Vec2::new(
        ED_X0 + x as f32 * ED_CELL + ED_CELL / 2.0,
        ED_Y0 - y as f32 * ED_CELL - ED_CELL / 2.0,
    )
}

fn row_get(rows: &[String], x: usize, y: usize) -> char {
    rows[y].chars().nth(x).unwrap_or('.')
}

fn row_set(rows: &mut [String], x: usize, y: usize, ch: char) {
    let mut chars: Vec<char> = rows[y].chars().collect();
    if x < chars.len() {
        chars[x] = ch;
        rows[y] = chars.into_iter().collect();
    }
}

/// Removes every instance of a single-placement marker (p, v, X).
fn clear_marker(rows: &mut [String], ch: char) {
    for y in 0..rows.len() {
        for x in 0..MW {
            if row_get(rows, x, y) == ch {
                row_set(rows, x, y, if ch == 'X' { '#' } else { '.' });
            }
        }
    }
}

fn validate_office(rows: &[String]) -> Option<String> {
    let count = |ch: char| -> usize {
        rows.iter().map(|r| r.chars().filter(|&c| c == ch).count()).sum()
    };
    if count('p') != 1 {
        return Some("PLACE THE START (P)".into());
    }
    if count('X') == 0 {
        return Some("PLACE THE EXIT DOOR (X)".into());
    }
    if count('v') == 0 {
        return Some("PLACE THE SERVER CONSOLE (V)".into());
    }
    if count('f') == 0 {
        return Some("PLACE AT LEAST ONE INTEL FILE (6)".into());
    }
    None
}

/// Opens the editor from the page (no credit needed — it's a tool).
fn poll_editor_start(
    mut next: ResMut<NextState<Phase>>,
    mut net: ResMut<NetMode>,
    mut cfg: ResMut<crate::CabinetConfig>,
) {
    if crate::shell::take_editor_start() {
        net.0 = None;
        cfg.players = 1;
        cfg.humans = 1;
        crate::shell::mark_editor_pending();
        next.set(Phase::Playing);
    }
}

/// The whole editor: canvas lifecycle, painting, brushes, save, test-play.
#[allow(clippy::too_many_arguments)]
fn editor_update(
    mut commands: Commands,
    keys: Res<ButtonInput<KeyCode>>,
    buttons: Res<ButtonInput<MouseButton>>,
    windows: Query<&Window>,
    cameras: Query<(&Camera, &GlobalTransform)>,
    editor: Option<ResMut<OfficeEditor>>,
    mission: Option<ResMut<Mission>>,
    guards: Option<ResMut<Guards>>,
    pickups: Option<ResMut<Pickups>>,
    canvas: Query<Entity, With<EditorTag>>,
    mut cells: Query<(&EditorCell, &mut Sprite)>,
    mut ed_hud: Query<&mut Text2d, With<EditorHud>>,
) {
    let Some(mut editor) = editor else { return };
    let (Some(mut m), Some(mut guards), Some(mut pickups)) = (mission, guards, pickups) else {
        return;
    };

    // Returning from a test round: X bails back to the canvas.
    if !editor.active {
        if editor.testing && keys.just_pressed(KeyCode::KeyX) {
            editor.testing = false;
            editor.active = true;
        }
        if !editor.active {
            return;
        }
    }

    // Canvas lifecycle: build the overlay once per activation.
    if canvas.is_empty() {
        commands.spawn((
            Sprite { color: Color::srgb(0.02, 0.02, 0.05), custom_size: Some(Vec2::new(720.0, 640.0)), ..default() },
            Transform::from_xyz(0.0, 0.0, 9.5),
            EditorTag,
            GameTag,
        ));
        for y in 0..MH {
            for x in 0..MW {
                let p = ed_cell_pos(x, y);
                commands.spawn((
                    Sprite { color: DIM, custom_size: Some(Vec2::splat(ED_CELL - 2.0)), ..default() },
                    Transform::from_xyz(p.x, p.y, 10.0),
                    EditorCell(x, y),
                    EditorTag,
                    GameTag,
                ));
            }
        }
        let legend = text(
            &mut commands,
            "",
            11.0,
            AMBER,
            Vec3::new(0.0, -296.0, 10.5),
        );
        commands.entity(legend).insert((EditorHud, EditorTag, GameTag));
        return; // paint next frame, once the cells exist
    }

    // Brush keys 1-0, then the single-placement markers.
    for (i, key) in [
        KeyCode::Digit1,
        KeyCode::Digit2,
        KeyCode::Digit3,
        KeyCode::Digit4,
        KeyCode::Digit5,
        KeyCode::Digit6,
        KeyCode::Digit7,
        KeyCode::Digit8,
        KeyCode::Digit9,
        KeyCode::Digit0,
    ]
    .iter()
    .enumerate()
    {
        if keys.just_pressed(*key) {
            editor.brush = BRUSHES[i].0;
            sfx("tick");
        }
    }
    if keys.just_pressed(KeyCode::KeyX) {
        editor.brush = 'X';
        sfx("tick");
    }
    if keys.just_pressed(KeyCode::KeyV) {
        editor.brush = 'v';
        sfx("tick");
    }
    if keys.just_pressed(KeyCode::KeyP) {
        editor.brush = 'p';
        sfx("tick");
    }

    // Save to the community shelf.
    if keys.just_pressed(KeyCode::KeyS) && keys.pressed(KeyCode::ShiftLeft) {
        // Shift+S so plain S stays free for a future brush; muscle memory
        // from the other editors is close enough.
        if let Some(problem) = validate_office(&editor.rows) {
            if let Ok(mut t) = ed_hud.single_mut() {
                t.0 = format!("!! {problem} !!");
            }
            sfx("buzz");
        } else {
            let doc = OfficeDoc { v: 1, name: String::new(), rows: editor.rows.clone() };
            if let Ok(json) = serde_json::to_string(&doc) {
                crate::shell::save_level(&json);
                sfx("clear");
            }
        }
        return;
    }

    // Test-play the canvas in place.
    if keys.just_pressed(KeyCode::KeyG) {
        if let Some(problem) = validate_office(&editor.rows) {
            if let Ok(mut t) = ed_hud.single_mut() {
                t.0 = format!("!! {problem} !!");
            }
            sfx("buzz");
            return;
        }
        let rows = editor.rows.clone();
        let parsed = parse_office(&rows);
        m.grid = parsed.grid;
        m.doors_open = vec![false; MW * MH];
        m.spawns = parsed.spawns;
        m.px = parsed.px;
        m.py = parsed.py;
        m.ang = 0.0;
        m.hp = 100;
        m.darts = 24;
        m.files = 0;
        m.files_total = parsed.files_total;
        m.server_bugged = false;
        m.server_cell = parsed.server_cell;
        m.exit_cell = parsed.exit_cell;
        m.spotted = 0;
        m.clock = 0.0;
        m.over = None;
        m.done = false;
        m.result.clear();
        m.rows = rows;
        guards.0 = parsed.guards;
        pickups.0 = parsed.pickups;
        editor.active = false;
        editor.testing = true;
        for e in &canvas {
            commands.entity(e).despawn();
        }
        sfx("coin");
        return;
    }

    // Paint. The border is immutable; single markers relocate themselves.
    let place = buttons.pressed(MouseButton::Left);
    let erase = buttons.pressed(MouseButton::Right);
    if place || erase {
        if let (Ok(window), Ok((camera, cam_tf))) = (windows.single(), cameras.single()) {
            if let Some(world) = crate::retro::cursor_world(window, camera, cam_tf) {
                let x = ((world.x - ED_X0) / ED_CELL).floor() as i32;
                let y = ((ED_Y0 - world.y) / ED_CELL).floor() as i32;
                if x > 0 && (x as usize) < MW - 1 && y > 0 && (y as usize) < MH - 1 {
                    let ch = if erase { '.' } else { editor.brush };
                    if matches!(ch, 'p' | 'v' | 'X') && buttons.just_pressed(MouseButton::Left) {
                        clear_marker(&mut editor.rows, ch);
                        row_set(&mut editor.rows, x as usize, y as usize, ch);
                    } else if !matches!(ch, 'p' | 'v' | 'X') {
                        row_set(&mut editor.rows, x as usize, y as usize, ch);
                    }
                }
            }
        }
    }

    // Repaint cells + legend.
    for (c, mut sp) in &mut cells {
        let want = cell_color(row_get(&editor.rows, c.0, c.1));
        if sp.color != want {
            sp.color = want;
        }
    }
    if let Ok(mut t) = ed_hud.single_mut() {
        let brush_name = BRUSHES
            .iter()
            .find(|(c, _)| *c == editor.brush)
            .map(|(_, n)| *n)
            .unwrap_or(match editor.brush {
                'X' => "EXIT",
                'v' => "CONSOLE",
                _ => "START",
            });
        let s = format!(
            "OFFICE EDITOR - BRUSH: {brush_name}\n1 WALL 2 WOOD 3 RACKS 4 DOOR 5 FLOOR 6 FILE 7 DARTS 8 COFFEE 9 GUARD 0 SPAWN - X EXIT V CONSOLE P START\nLCLICK PAINTS - RCLICK ERASES - SHIFT+S SAVES - G TEST-PLAYS - X RETURNS FROM A TEST"
        );
        if t.0 != s {
            t.0 = s;
        }
    }
}
