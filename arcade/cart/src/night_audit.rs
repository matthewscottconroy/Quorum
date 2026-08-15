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

use bevy::input::mouse::{MouseMotion, MouseWheel};
use bevy::prelude::*;
use serde::{Deserialize, Serialize};

use crate::retro::{text, PLAYER_COLORS, AMBER, CYAN, DIM, GREEN, MAGENTA, RED, WHITE};
use crate::rng::Rng;
use crate::shell::{net_send, sfx, stat};
use crate::{FinalScore, GameTag, NetIn, NetMode, Phase};

pub const BLURB: &[&str] = &[
    "THE BOOKS DON'T SLEEP. NEITHER DO YOU.",
    "MOUSE TURNS - WASD MOVES - CLICK DARTS - E USES",
    "SOLO: 3 FILES, BUG THE GREEN TILE, EXIT GREEN.",
    "DEATHMATCH (2-12): HOST/JOIN IN THE BAR BELOW.",
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

// ── the arsenal ─────────────────────────────────────────────────────────
// 0 CLIPBOARD SLAP (always), 1 DART PISTOL (always), 2 RAPID STAPLER,
// 3 PARTY POPPER (shotgun), 4 MEMO LAUNCHER, 5 LETTER OPENERS (thrown,
// silent), 6 CONFETTI MORTAR (rocket + splash), 7 GOLDEN STAPLER (one
// tap, one nap), 8 STICKY MEMO MINES (plant with fire, X detonates).
const W_SLAP: u8 = 0;
const W_PISTOL: u8 = 1;
const W_RAPID: u8 = 2;
const W_POPPER: u8 = 3;
const W_MEMO: u8 = 4;
const W_OPENERS: u8 = 5;
const W_MORTAR: u8 = 6;
const W_GOLDEN: u8 = 7;
const W_MINES: u8 = 8;

fn weapon_name(w: u8) -> &'static str {
    match w {
        W_SLAP => "CLIPBOARD SLAP",
        W_PISTOL => "DART PISTOL",
        W_RAPID => "RAPID STAPLER",
        W_POPPER => "PARTY POPPER",
        W_MEMO => "MEMO LAUNCHER",
        W_OPENERS => "LETTER OPENERS",
        W_MORTAR => "CONFETTI MORTAR",
        W_GOLDEN => "GOLDEN STAPLER",
        _ => "STICKY MINES",
    }
}

fn weapon_cd(w: u8) -> f32 {
    match w {
        W_SLAP => 0.38,
        W_RAPID => 0.16,
        W_POPPER => 0.85,
        W_MEMO => 0.95,
        W_OPENERS => 0.5,
        W_MORTAR => 1.15,
        W_GOLDEN => 0.7,
        W_MINES => 0.45,
        _ => 0.45,
    }
}

/// How much of this weapon's ammo pool is left (-1 = never runs dry).
fn ammo_pool(m: &Mission, w: u8) -> i32 {
    match w {
        W_SLAP => -1,
        W_PISTOL | W_RAPID | W_MEMO => m.darts,
        W_POPPER => m.shells,
        W_OPENERS => m.knives,
        W_MORTAR => m.rockets,
        W_GOLDEN => m.golden_ammo,
        _ => m.mine_ammo,
    }
}

fn spend(m: &mut Mission, w: u8, cost: i32) {
    match w {
        W_PISTOL | W_RAPID | W_MEMO => m.darts -= cost,
        W_POPPER => m.shells -= cost,
        W_OPENERS => m.knives -= cost,
        W_MORTAR => m.rockets -= cost,
        W_GOLDEN => m.golden_ammo -= cost,
        W_MINES => m.mine_ammo -= cost,
        _ => {}
    }
}

/// Owned and not dry: worth switching to.
fn weapon_ready(m: &Mission, w: u8) -> bool {
    m.owned & (1 << w) != 0 && ammo_pool(m, w) != 0
}

/// Rebuild the per-cell brightness from whichever lamps still work.
/// An office with no lamps at all is just lit (older maps stay bright).
fn recompute_light(m: &mut Mission) {
    m.light = initial_light(&m.lamps);
}

/// The same math as recompute_light, for setup time.
fn initial_light(lamps: &[(usize, usize, bool)]) -> Vec<f32> {
    if lamps.is_empty() {
        return vec![1.0; MW * MH];
    }
    let mut l = vec![0.30f32; MW * MH];
    for (lx, ly, alive) in lamps.iter().copied() {
        if !alive {
            continue;
        }
        for y in ly.saturating_sub(6)..(ly + 7).min(MH) {
            for x in lx.saturating_sub(6)..(lx + 7).min(MW) {
                let d2 = (x as f32 - lx as f32).powi(2) + (y as f32 - ly as f32).powi(2);
                l[y * MW + x] += 1.1 / (1.0 + 0.45 * d2);
            }
        }
    }
    for v in l.iter_mut() {
        *v = v.clamp(0.18, 1.0);
    }
    l
}

fn light_at(m: &Mission, x: f32, y: f32) -> f32 {
    if m.light.is_empty() {
        return 1.0;
    }
    let (cx, cy) = ((x.floor() as usize).min(MW - 1), (y.floor() as usize).min(MH - 1));
    m.light[cy * MW + cx]
}

// ── the cast ────────────────────────────────────────────────────────────
// Purely cosmetic and purely funny: body build, skin, and headwear.
// (name, torso w, torso h, skin rgb, hat rgb or None)
type CharDef = (&'static str, f32, f32, (f32, f32, f32), Option<(f32, f32, f32)>);
const CHARACTERS: [CharDef; 8] = [
    ("THE TEMP", 0.42, 0.62, (0.85, 0.70, 0.55), None),
    ("FACILITIES DAVE", 0.56, 0.56, (0.80, 0.62, 0.45), Some((0.15, 0.25, 0.55))),
    ("AUDITOR PRIME", 0.38, 0.72, (0.92, 0.80, 0.68), Some((0.08, 0.08, 0.10))),
    ("MAINFRAME MARY", 0.42, 0.66, (0.75, 0.58, 0.45), Some((0.10, 0.80, 0.85))),
    ("THE NIGHT JANITOR", 0.48, 0.60, (0.70, 0.60, 0.50), Some((0.45, 0.45, 0.48))),
    ("CUBICLE CRYPTID", 0.62, 0.46, (0.55, 0.65, 0.55), None),
    ("H.R. SPECTRE", 0.42, 0.70, (0.95, 0.92, 0.95), Some((0.90, 0.90, 0.95))),
    ("INTERN FOREVER", 0.32, 0.54, (0.85, 0.70, 0.55), None),
];

/// A thrown or launched thing mid-flight.
struct Proj {
    x: f32,
    y: f32,
    dx: f32,
    dy: f32,
    kind: u8, // 0 letter opener, 1 confetti rocket
}

/// A planted sticky memo mine, waiting for the X button.
struct Mine {
    x: f32,
    y: f32,
}

/// The office, after hours. '#' concrete, 'W' wood paneling, 'S' server
/// racks, 'D' a door that opens for anyone (E), 'X' the extraction door
/// (opens only once the paperwork objectives are done). Lowercase letters
/// are floor markers: p start, g guard post, f intel file, a darts,
/// c coffee, v the server console (stand here, press E).
const MAP: [&str; MH] = [
    "########################",
    "#p.o..D....#.o.f...#o..#",
    "#.....#....#...#...#.g.#",
    "###D###.g..#...#...D...#",
    "#.....#....D...#...#####",
    "#..g..#.o..#...#...o...#",
    "#.....######...####D####",
    "#.a...#....#......#....#",
    "####D##.o..D..g...#..c.#",
    "#....#..c..#......D....#",
    "#.o..#.....#.o.a..#..g.#",
    "#.f..###D###......#....#",
    "#....#....########ded###",
    "#.o..D....#..o...#ddd..#",
    "######....#.vSSd.#ddd..#",
    "#....#....D.dddd.#ddd..#",
    "#.g..#....#.dddd.####D##",
    "#.o..######..o...#.....#",
    "#.a.......D......D..f..#",
    "#....#....#......#..,..#",
    "###D##....########.^^g.#",
    "#...o....g#..o...#.^^..#",
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
    heights: Vec<f32>, // raised floor per cell: 0 ground, 0.22 step, 0.45 deck
    guards: Vec<Guard>,
    pickups: Vec<Pickup>,
    px: f32,
    py: f32,
    server_cell: (usize, usize),
    exit_cell: (usize, usize),
    files_total: u32,
    spawns: Vec<(f32, f32)>,
    lamps: Vec<(usize, usize, bool)>,
}

fn parse_office(rows: &[String]) -> Parsed {
    let mut out = Parsed {
        grid: vec![Cell::Open; MW * MH],
        heights: vec![0.0; MW * MH],
        guards: Vec::new(),
        pickups: Vec::new(),
        px: 1.5,
        py: 1.5,
        server_cell: (0, 0),
        exit_cell: (0, 0),
        files_total: 0,
        spawns: Vec::new(),
        lamps: Vec::new(),
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
                'o' => {
                    out.lamps.push((x, y, true));
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
                ',' => {
                    out.heights[y * MW + x] = 0.22; // the half step
                    Cell::Open
                }
                '^' => {
                    out.heights[y * MW + x] = 0.45; // the raised deck
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
    heights: Vec<f32>, // per-cell floor elevation (steps and decks)
    pitch: f32,        // vertical look, in screen pixels of horizon shear
    pz: f32,           // your eye's current elevation (eases up steps)
    weapon: u8,        // index into the arsenal above
    owned: u16,        // bitmask of carried weapons (slap+pistol always)
    shells: i32,       // PARTY POPPER ammo
    knives: i32,       // LETTER OPENERS ammo
    rockets: i32,      // CONFETTI MORTAR ammo
    golden_ammo: i32,  // GOLDEN STAPLER ammo
    mine_ammo: i32,    // unplanted STICKY MINES
    zoom: f32,         // scope: right button eases this toward 2.3
    projs: Vec<Proj>,
    mines: Vec<Mine>,
    cloak_t: f32,      // GHOST BADGE timer: nobody sees you
    thermal_t: f32,    // THERMAL SPECS timer: warm bodies glow through walls
    crouch: bool,      // low and slow and hard to spot
    lamps: Vec<(usize, usize, bool)>, // ceiling lamps; shoot them out
    light: Vec<f32>,   // per-cell brightness from the surviving lamps
    my_char: u8,       // my pick from the CHARACTERS roster
    practice: bool,    // OFFICE PARTY vs local bots (no relay)
    bot_hits: Vec<u8>, // hits I claimed on bot seats this frame
    mode: u8,          // party rules: 0 free-for-all, 1 teams, 2 ctf, 3 gladiator
    glad: usize,       // gladiator mode: who wears the crown right now
    flags: [FlagSt; 2], // ctf: the RED and BLUE team binders
    team_scores: [u32; 2],
    flag_timer: Timer, // host cadence for flag-state broadcasts
    armor: i32,       // vest points soak hits first
    espresso_t: f32,  // fast feet timer
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
    ch: u8,       // which of the CHARACTERS they came dressed as
    crouch: bool, // low profile right now
    cloak: bool,  // ghost badge running: barely a shimmer
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
    #[serde(default)]
    c: u8, // character index
    #[serde(default)]
    r: bool, // crouched
    #[serde(default)]
    k: bool, // cloaked
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
struct WireFlag {
    t: String, // "flag" — the host's word on both binders
    r: (u8, u8, f32, f32), // red: state, carrier, x, y
    b: (u8, u8, f32, f32), // blue
    s0: u32,
    s1: u32,
}

#[derive(Serialize, Deserialize)]
struct WireLamp {
    t: String, // "lamp" — someone shot the lights out
    x: i32,
    y: i32,
}

#[derive(Serialize, Deserialize)]
struct WireLevel {
    t: String, // "lv"
    rows: Vec<String>,
    #[serde(default)]
    mode: u8, // the host's party rules
}

/// One team binder (the CTF flag): home on its base plinth, carried on
/// somebody's back, or dropped where its last carrier went down.
#[derive(Clone, Copy)]
struct FlagSt {
    hx: f32,
    hy: f32,
    state: u8, // 0 home, 1 carried, 2 dropped
    carrier: usize,
    x: f32,
    y: f32,
    drop_t: f32,
}

impl FlagSt {
    fn at(hx: f32, hy: f32) -> Self {
        FlagSt { hx, hy, state: 0, carrier: 0, x: hx, y: hy, drop_t: 0.0 }
    }
}

/// Team split: even seats RED, odd seats BLUE.
fn team_of(seat: usize) -> usize {
    seat % 2
}

/// In a team mode, is this seat on MY side?
fn same_team(m: &Mission, seat: usize) -> bool {
    (m.mode == 1 || m.mode == 2) && team_of(seat) == team_of(m.my_seat)
}

/// A practice-party rival: guard brains, auditor manners. Bots strafe,
/// keep their distance, pick fights with each other, and respawn.
struct Bot {
    seat: usize,
    x: f32,
    y: f32,
    dir: Vec2,
    hp: i32,
    nap_t: f32,
    shoot_cd: f32,
    wander_cd: f32,
    strafe: f32,
    ch: u8,
}

#[derive(Resource, Default)]
struct Bots(Vec<Bot>);

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
    Rapid,    // the RAPID STAPLER: hoses darts
    Memo,     // the MEMO LAUNCHER: one shot naps a whole cluster
    Vest,     // body armor: soaks damage before health does
    Espresso, // fast feet for a while
    Popper,   // the PARTY POPPER: five pellets, close work
    Openers,  // LETTER OPENERS: silent, thrown
    Mortar,   // the CONFETTI MORTAR: rocket with a splash
    Golden,   // the GOLDEN STAPLER: one tap, one nap
    MinesKit, // STICKY MEMO MINES
    Shells,   // popper ammo box
    Cloak,    // the GHOST BADGE: unseen for a while
    Thermal,  // THERMAL SPECS: warm bodies glow through walls
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
    gun: Entity,        // the dart gun in the corner; kicks on fire
    tracer: Entity,     // dart streak toward the crosshair
    steps: Vec<Entity>, // step-face columns for raised decks
    ceil: Entity,       // horizon plates, sheared by pitch
    floor: Entity,
}

#[derive(Component)]
struct PromptText;

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
                    munitions,
                    bots_think,
                    flags_run,
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

/// True when the cell is walkable floor — where armory pickups may land.
fn parsed_cell_for_armory(grid: &[Cell], x: usize, y: usize) -> bool {
    matches!(grid[y * MW + x], Cell::Open)
}

fn setup(
    mut commands: Commands,
    net: Res<NetMode>,
    cfg: Res<crate::CabinetConfig>,
    mut rng: ResMut<Rng>,
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
        heights: parsed_heights,
        mut guards,
        mut pickups,
        mut px,
        mut py,
        server_cell,
        exit_cell,
        files_total,
        spawns,
        lamps: parsed_lamps,
    } = parsed;
    // Two ways to throw an OFFICE PARTY: a relay room full of humans, or
    // a local practice party against however many bots the coin bought.
    let practice = net.0.is_none() && !editor_mode && cfg.players >= 2;
    let dm = (net.0.is_some() && !editor_mode) || practice;
    // The party rules come from the page picker. The host's pick rides the
    // level broadcast so every guest plays the same game.
    let is_host_or_local = net.0.as_ref().map(|c| c.is_host()).unwrap_or(true);
    let party_mode = if dm && is_host_or_local {
        (crate::shell::page_knob("__ARCADE_PARTY_MODE") as u8).min(3)
    } else {
        0
    };
    let my_seat = net.0.as_ref().map(|c| c.seat as usize).unwrap_or(0);
    let seats = net
        .0
        .as_ref()
        .map(|c| c.seats as usize)
        .unwrap_or(if practice { cfg.players.clamp(2, 12) as usize } else { 1 });
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
            if let Ok(w) =
                serde_json::to_string(&WireLevel { t: "lv".into(), rows: rows.clone(), mode: party_mode })
            {
                net_send(&w);
            }
        }
    }
    let mut remotes = Vec::new();
    let mut bots = Vec::new();
    if let Some(ncfg) = &net.0 {
        for (s, present) in ncfg.present.iter().enumerate() {
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
                    ch: 0,
                    crouch: false,
                    cloak: false,
                });
            }
        }
    } else if practice {
        for s in 1..seats {
            let (rx, ry) = spawns[s % spawns.len()];
            let ch = rng.range(8) as u8;
            bots.push(Bot {
                seat: s,
                x: rx,
                y: ry,
                dir: Vec2::X,
                hp: DM_HP,
                nap_t: 0.0,
                shoot_cd: 1.0 + rng.range(10) as f32 / 10.0,
                wander_cd: 0.0,
                strafe: 1.0,
                ch,
            });
            remotes.push(Remote {
                seat: s,
                x: rx,
                y: ry,
                px: rx,
                py: ry,
                t: 1.0,
                napping: false,
                heard: true,
                ch,
                crouch: false,
                cloak: false,
            });
        }
    }
    commands.insert_resource(Remotes(remotes));
    commands.insert_resource(Bots(bots));
    // The armory scatter: the whole rack hides on random open floor
    // tiles every shift; the GOLDEN STAPLER only some nights.
    {
        let mut placed = 0;
        let mut tries = 0;
        let mut kinds = vec![
            PickupKind::Rapid,
            PickupKind::Memo,
            PickupKind::Popper,
            PickupKind::Openers,
            PickupKind::Mortar,
            PickupKind::MinesKit,
            PickupKind::Shells,
            PickupKind::Vest,
            PickupKind::Espresso,
            PickupKind::Cloak,
            PickupKind::Thermal,
        ];
        if rng.chance(0.4) {
            kinds.push(PickupKind::Golden);
        }
        while placed < kinds.len() && tries < 900 {
            tries += 1;
            let cx = 1 + rng.range((MW - 2) as u32) as usize;
            let cy = 1 + rng.range((MH - 2) as u32) as usize;
            let open = pickups.iter().all(|p: &Pickup| {
                (p.x - (cx as f32 + 0.5)).abs() + (p.y - (cy as f32 + 0.5)).abs() > 1.5
            });
            let cell_ok = matches!(parsed_cell_for_armory(&grid, cx, cy), true);
            if cell_ok && open {
                pickups.push(Pickup { x: cx as f32 + 0.5, y: cy as f32 + 0.5, kind: kinds[placed], taken: false });
                placed += 1;
            }
        }
    }
    let flag_homes = [
        FlagSt::at(spawns[0].0, spawns[0].1),
        FlagSt::at(spawns[1 % spawns.len()].0, spawns[1 % spawns.len()].1),
    ];
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
        heights: parsed_heights,
        pitch: 0.0,
        pz: 0.0,
        weapon: W_PISTOL,
        owned: (1 << W_SLAP) | (1 << W_PISTOL),
        shells: 0,
        knives: 0,
        rockets: 0,
        golden_ammo: 0,
        mine_ammo: 0,
        zoom: 1.0,
        projs: Vec::new(),
        mines: Vec::new(),
        cloak_t: 0.0,
        thermal_t: 0.0,
        crouch: false,
        lamps: parsed_lamps.clone(),
        light: initial_light(&parsed_lamps),
        my_char: (crate::shell::page_knob("__ARCADE_CHAR") as u8).min(7),
        practice,
        bot_hits: Vec::new(),
        mode: party_mode,
        glad: 0,
        flags: flag_homes,
        team_scores: [0, 0],
        flag_timer: Timer::from_seconds(0.25, TimerMode::Repeating),
        armor: 0,
        espresso_t: 0.0,
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

    // Ceiling and floor plates behind the columns — oversized so the
    // horizon can shear up and down with your pitch without gaps.
    let ceil = commands
        .spawn((
            Sprite { color: Color::srgb(0.05, 0.06, 0.10), custom_size: Some(Vec2::new(720.0, VIEW_H)), ..default() },
            Transform::from_xyz(0.0, VIEW_CY + VIEW_H / 2.0, 0.5),
            GameTag,
        ))
        .id();
    let floor = commands
        .spawn((
            Sprite { color: Color::srgb(0.09, 0.08, 0.07), custom_size: Some(Vec2::new(720.0, VIEW_H)), ..default() },
            Transform::from_xyz(0.0, VIEW_CY - VIEW_H / 2.0, 0.5),
            GameTag,
        ))
        .id();
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
    let mut bills = Vec::with_capacity(88);
    for _ in 0..88 {
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
    // The dart gun: a visible prop in the lower-right so firing has a place
    // to come FROM (and a kick), instead of an ambiguous screen pulse.
    let gun = commands
        .spawn((
            Sprite { color: Color::srgb(0.16, 0.17, 0.22), custom_size: Some(Vec2::new(46.0, 90.0)), ..default() },
            Transform::from_xyz(150.0, VIEW_CY - VIEW_H / 2.0 + 40.0, 5.8).with_rotation(Quat::from_rotation_z(-0.22)),
            GameTag,
        ))
        .with_children(|kid| {
            kid.spawn((
                Sprite { color: AMBER, custom_size: Some(Vec2::new(14.0, 18.0)), ..default() },
                Transform::from_xyz(0.0, 52.0, 0.1),
            ));
        })
        .id();
    let tracer = commands
        .spawn((
            Sprite { color: Color::srgb(1.0, 0.85, 0.4), custom_size: Some(Vec2::new(3.0, 200.0)), ..default() },
            Transform::from_xyz(78.0, VIEW_CY - 110.0, 5.7).with_rotation(Quat::from_rotation_z(-0.6)),
            Visibility::Hidden,
            GameTag,
        ))
        .id();
    // Step faces: a second column pool for the fronts of raised decks.
    let mut steps = Vec::with_capacity(NCOL);
    for i in 0..NCOL {
        let x = -360.0 + COLW / 2.0 + i as f32 * COLW;
        let e = commands
            .spawn((
                Sprite { color: DIM, custom_size: Some(Vec2::new(COLW, 10.0)), ..default() },
                Transform::from_xyz(x, VIEW_CY, 1.4),
                Visibility::Hidden,
                GameTag,
            ))
            .id();
        steps.push(e);
    }
    commands.insert_resource(View { cols, depth: vec![f32::MAX; NCOL], bills, gun, tracer, steps, ceil, floor });

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
    // Contextual "press E" prompt, low in the 3D window.
    let prompt = text(&mut commands, "", 16.0, GREEN, Vec3::new(0.0, VIEW_CY - 180.0, 6.0));
    commands.entity(prompt).insert((PromptText, GameTag));
    // A standing key legend so nobody has to guess the controls mid-heist.
    let help = text(&mut commands, "", 10.0, DIM, Vec3::new(0.0, 288.0, 6.0));
    commands.entity(help).insert(GameTag);
    commands.entity(help).insert(HelpLine);
}

#[derive(Component)]
struct HelpLine;

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

fn player_move(
    time: Res<Time>,
    keys: Res<ButtonInput<KeyCode>>,
    buttons: Res<ButtonInput<MouseButton>>,
    mut motion: EventReader<MouseMotion>,
    mut wheel: EventReader<MouseWheel>,
    mut m: ResMut<Mission>,
) {
    if m.over.is_some() || m.nap_t > 0.0 {
        motion.clear();
        wheel.clear();
        return;
    }
    let dt = time.delta_secs();
    m.clock += dt;
    m.fire_cd = (m.fire_cd - dt).max(0.0);
    m.flash = (m.flash - dt).max(0.0);
    m.hurt = (m.hurt - dt).max(0.0);
    m.espresso_t = (m.espresso_t - dt).max(0.0);
    m.cloak_t = (m.cloak_t - dt).max(0.0);
    m.thermal_t = (m.thermal_t - dt).max(0.0);
    // Mouse turns the view, PC-shooter style (click the screen to lock the
    // pointer) — and tilts it: mouse Y shears the horizon so you can look
    // up at the racks and down the stairwells. Arrows still work.
    let (mut mouse_dx, mut mouse_dy) = (0.0f32, 0.0f32);
    for ev in motion.read() {
        mouse_dx += ev.delta.x;
        mouse_dy += ev.delta.y;
    }
    m.ang += mouse_dx * 0.0032 / m.zoom;
    m.pitch = (m.pitch - mouse_dy * 0.9 / m.zoom).clamp(-170.0, 170.0);
    // Your eye eases to the floor under you: stairs feel like stairs.
    let here = (m.py.floor() as usize).min(MH - 1) * MW + (m.px.floor() as usize).min(MW - 1);
    let target = m.heights[here] - if m.crouch { 0.22 } else { 0.0 };
    m.pz += (target - m.pz) * (7.0 * dt).min(1.0);
    let turn = i32::from(keys.pressed(KeyCode::ArrowRight)) - i32::from(keys.pressed(KeyCode::ArrowLeft));
    m.ang += turn as f32 * TURN_SPEED * dt;
    // The armory rack: 1-9 pick a weapon, Q and the wheel cycle through
    // whatever you actually carry (and haven't run dry).
    const DIGITS: [KeyCode; 9] = [
        KeyCode::Digit1,
        KeyCode::Digit2,
        KeyCode::Digit3,
        KeyCode::Digit4,
        KeyCode::Digit5,
        KeyCode::Digit6,
        KeyCode::Digit7,
        KeyCode::Digit8,
        KeyCode::Digit9,
    ];
    for (w, key) in DIGITS.iter().enumerate() {
        if keys.just_pressed(*key) && weapon_ready(&m, w as u8) {
            m.weapon = w as u8;
            sfx("tick");
        }
    }
    let mut cycle = i32::from(keys.just_pressed(KeyCode::KeyQ));
    for ev in wheel.read() {
        cycle += if ev.y < 0.0 { 1 } else { -1 };
    }
    if cycle != 0 {
        for _ in 0..9 {
            m.weapon = ((m.weapon as i32 + cycle).rem_euclid(9)) as u8;
            if weapon_ready(&m, m.weapon) {
                break;
            }
        }
        sfx("tick");
    }
    // Crouch: low profile, slow feet, quiet shoes.
    m.crouch = keys.pressed(KeyCode::KeyC) || keys.pressed(KeyCode::ControlLeft);
    // The scope: hold the right button and the lens eases in.
    let zt = if buttons.pressed(MouseButton::Right) { 2.3 } else { 1.0 };
    let zr = (10.0 * dt).min(1.0);
    m.zoom += (zt - m.zoom) * zr;
    let fwd = i32::from(keys.pressed(KeyCode::KeyW) || keys.pressed(KeyCode::ArrowUp)) as f32
        - i32::from(keys.pressed(KeyCode::KeyS) || keys.pressed(KeyCode::ArrowDown)) as f32;
    let side = i32::from(keys.pressed(KeyCode::KeyD)) as f32 - i32::from(keys.pressed(KeyCode::KeyA)) as f32;
    let dir = Vec2::new(m.ang.cos(), m.ang.sin());
    let right = Vec2::new(-dir.y, dir.x);
    let mut sp = MOVE_SPEED * if m.espresso_t > 0.0 { 1.35 } else { 1.0 };
    if m.crouch {
        sp *= 0.55;
    }
    let step = (dir * fwd + right * side).clamp_length_max(1.0) * sp * dt;
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
            PickupKind::Rapid => {
                m.owned |= 1 << W_RAPID;
                m.weapon = W_RAPID;
                m.darts += 20;
                sfx("clear");
            }
            PickupKind::Memo => {
                m.owned |= 1 << W_MEMO;
                m.weapon = W_MEMO;
                m.darts += 9;
                sfx("clear");
            }
            PickupKind::Popper => {
                m.owned |= 1 << W_POPPER;
                m.weapon = W_POPPER;
                m.shells += 8;
                sfx("clear");
            }
            PickupKind::Openers => {
                m.owned |= 1 << W_OPENERS;
                m.weapon = W_OPENERS;
                m.knives += 6;
                sfx("clear");
            }
            PickupKind::Mortar => {
                m.owned |= 1 << W_MORTAR;
                m.weapon = W_MORTAR;
                m.rockets += 4;
                sfx("clear");
            }
            PickupKind::Golden => {
                m.owned |= 1 << W_GOLDEN;
                m.weapon = W_GOLDEN;
                m.golden_ammo += 3;
                sfx("power");
            }
            PickupKind::MinesKit => {
                m.owned |= 1 << W_MINES;
                m.mine_ammo += 3;
                sfx("clear");
            }
            PickupKind::Shells => {
                m.shells += 8;
                sfx("eat");
            }
            PickupKind::Cloak => {
                m.cloak_t = 18.0;
                sfx("power");
            }
            PickupKind::Thermal => {
                m.thermal_t = 20.0;
                sfx("power");
            }
            PickupKind::Vest => {
                m.armor = (m.armor + 50).min(100);
                sfx("power");
            }
            PickupKind::Espresso => {
                m.espresso_t = 20.0;
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
                        r.ch = p.c.min(7);
                        r.crouch = p.r;
                        r.cloak = p.k;
                        if p.f {
                            sfx("drop"); // a dart pops somewhere in the office
                        }
                    }
                }
            }
            Some("hit") => {
                if let Ok(h) = serde_json::from_str::<WireHit>(&ev.data) {
                    // Team modes turn friendly fire off at the victim's end.
                    if h.v == cfg.seat
                        && m.nap_t <= 0.0
                        && m.over.is_none()
                        && !same_team(&m, ev.seat as usize)
                    {
                        if m.armor >= 25 {
                            m.armor -= 25; // the vest eats the tag
                        } else {
                            m.dm_hp -= 1;
                        }
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
                            // Gladiator: only the crown scores; napping the
                            // crown means taking it.
                            let scores_count = m.mode != 3 || shooter as usize == m.glad;
                            if scores_count && (shooter as usize) < m.dm_scores.len() {
                                m.dm_scores[shooter as usize] += 1;
                            }
                            if m.mode == 3 && m.glad == m.my_seat {
                                m.glad = shooter as usize;
                            }
                            stat("audits_failed", 1);
                            sfx("over");
                        }
                    }
                }
            }
            Some("nap") => {
                if let Ok(n) = serde_json::from_str::<WireNap>(&ev.data) {
                    let scores_count = m.mode != 3 || n.by as usize == m.glad;
                    if scores_count && (n.by as usize) < m.dm_scores.len() {
                        m.dm_scores[n.by as usize] += 1;
                    }
                    if m.mode == 3 && m.glad == ev.seat as usize {
                        m.glad = n.by as usize; // the crown changes heads
                        sfx("power");
                    }
                    if n.by == cfg.seat && scores_count {
                        m.score += 150;
                        stat("guards_tranqed", 1);
                        sfx("capture");
                    }
                }
            }
            Some("lamp") => {
                if let Ok(l) = serde_json::from_str::<WireLamp>(&ev.data) {
                    let mut any = false;
                    for lamp in m.lamps.iter_mut() {
                        if lamp.0 == l.x as usize && lamp.1 == l.y as usize && lamp.2 {
                            lamp.2 = false;
                            any = true;
                        }
                    }
                    if any {
                        recompute_light(&mut m);
                        sfx("thud");
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
            Some("flag") => {
                if let Ok(f) = serde_json::from_str::<WireFlag>(&ev.data) {
                    for (i, (st, ca, x, y)) in [f.r, f.b].into_iter().enumerate() {
                        m.flags[i].state = st.min(2);
                        m.flags[i].carrier = ca as usize;
                        m.flags[i].x = x;
                        m.flags[i].y = y;
                    }
                    if m.team_scores != [f.s0, f.s1] {
                        m.team_scores = [f.s0, f.s1];
                        sfx("clear");
                    }
                }
            }
            Some("lv") => {
                // The host's map, before anyone has really moved: rebuild the
                // grid in place and stand everyone on its spawn markers.
                if let Ok(lv) = serde_json::from_str::<WireLevel>(&ev.data) {
                    m.mode = lv.mode.min(3);
                    if lv.rows.len() == MH
                        && lv.rows.iter().all(|r| r.chars().count() == MW)
                        && lv.rows != m.rows
                    {
                        let parsed = parse_office(&lv.rows);
                        m.grid = parsed.grid;
                        m.doors_open = vec![false; MW * MH];
                        m.spawns = parsed.spawns;
                        m.heights = parsed.heights;
                        m.lamps = parsed.lamps;
                        recompute_light(&mut m);
                        m.flags = [
                            FlagSt::at(m.spawns[0].0, m.spawns[0].1),
                            FlagSt::at(m.spawns[1 % m.spawns.len()].0, m.spawns[1 % m.spawns.len()].1),
                        ];
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

/// CAPTURE THE BINDER. The authority (relay host, or the local machine in
/// a practice party) referees both flags from positions it already knows:
/// walk over the enemy binder to grab it, run it back to your base while
/// your own binder is home to score a cap. Nappers drop it on the carpet.
fn flags_run(
    time: Res<Time>,
    net: Res<NetMode>,
    mut m: ResMut<Mission>,
    remotes: Res<Remotes>,
) {
    if !m.dm || m.mode != 2 || m.over.is_some() {
        return;
    }
    let authority = m.practice || net.0.as_ref().map(|c| c.is_host()).unwrap_or(false);
    if !authority {
        return; // guests hear the referee over the wire
    }
    let dt = time.delta_secs();
    // Everyone the referee can see: me plus every remote (bots included).
    let mut actors = vec![(m.my_seat, m.px, m.py, m.nap_t > 0.0)];
    for r in remotes.0.iter().filter(|r| r.heard) {
        actors.push((r.seat, r.x, r.y, r.napping));
    }
    let mut capped = false;
    for fi in 0..2 {
        let mut f = m.flags[fi];
        match f.state {
            0 | 2 => {
                // On its plinth or dropped: an enemy grabs it by touch; a
                // teammate touching a dropped one sends it home.
                let (fx, fy) = if f.state == 0 { (f.hx, f.hy) } else { (f.x, f.y) };
                if f.state == 2 {
                    f.drop_t -= dt;
                    if f.drop_t <= 0.0 {
                        f.state = 0;
                    }
                }
                for (seat, x, y, napping) in actors.iter().copied() {
                    if napping {
                        continue;
                    }
                    let close = (x - fx).abs() + (y - fy).abs() < 0.9;
                    if !close {
                        continue;
                    }
                    if team_of(seat) != fi {
                        f.state = 1;
                        f.carrier = seat;
                        sfx("coin");
                        break;
                    } else if f.state == 2 {
                        f.state = 0;
                        sfx("eat");
                        break;
                    }
                }
            }
            _ => {
                // Carried: follow the carrier; drop when they nap; cap when
                // they reach their own base with their own binder home.
                if let Some((_, x, y, napping)) = actors.iter().copied().find(|a| a.0 == f.carrier) {
                    f.x = x;
                    f.y = y;
                    if napping {
                        f.state = 2;
                        f.drop_t = 25.0;
                    } else {
                        let my_base = m.flags[team_of(f.carrier)];
                        let home = (x - my_base.hx).abs() + (y - my_base.hy).abs() < 0.9;
                        if home && my_base.state == 0 {
                            m.team_scores[team_of(f.carrier)] += 1;
                            if f.carrier == m.my_seat {
                                m.score += 400;
                                stat("files_lifted", 1);
                            }
                            f.state = 0;
                            f.x = f.hx;
                            f.y = f.hy;
                            capped = true;
                        }
                    }
                } else {
                    f.state = 2;
                    f.drop_t = 25.0;
                }
            }
        }
        m.flags[fi] = f;
    }
    if capped {
        sfx("win");
    }
    // The referee's word goes out on a short cadence.
    if !m.practice && m.flag_timer.tick(time.delta()).just_finished() {
        let msg = WireFlag {
            t: "flag".into(),
            r: (m.flags[0].state, m.flags[0].carrier as u8, m.flags[0].x, m.flags[0].y),
            b: (m.flags[1].state, m.flags[1].carrier as u8, m.flags[1].x, m.flags[1].y),
            s0: m.team_scores[0],
            s1: m.team_scores[1],
        };
        if let Ok(w) = serde_json::to_string(&msg) {
            net_send(&w);
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
            c: m.my_char,
            r: m.crouch,
            k: m.cloak_t > 0.0,
        };
        m.fired_flag = false;
        if let Ok(w) = serde_json::to_string(&msg) {
            net_send(&w);
        }
    }
    // The host calls time (or the winning tranq) for everyone.
    let is_host = net.0.as_ref().map(|c| c.is_host()).unwrap_or(false) || m.practice;
    let target_hit = match m.mode {
        1 => {
            let red: u32 = m.dm_scores.iter().enumerate().filter(|(s, _)| s % 2 == 0).map(|(_, n)| n).sum();
            let blu: u32 = m.dm_scores.iter().enumerate().filter(|(s, _)| s % 2 == 1).map(|(_, n)| n).sum();
            red >= DM_TARGET || blu >= DM_TARGET
        }
        2 => m.team_scores.iter().any(|&c| c >= 3),
        _ => m.dm_scores.iter().any(|&s| s >= DM_TARGET),
    };
    if is_host && (m.dm_clock <= 0.0 || target_hit) {
        let scores = m.dm_scores.clone();
        if let Ok(w) = serde_json::to_string(&WireEnd { t: "end".into(), scores: scores.clone() }) {
            net_send(&w);
        }
        finish_party(&mut m, &scores);
    }
}

/// Standings, my payout, and the horn.
fn finish_party(m: &mut Mission, scores: &[u32]) {
    if m.mode == 1 || m.mode == 2 {
        // Team result: caps for CTF, pooled tags for team tag.
        let (red, blu) = if m.mode == 2 {
            (m.team_scores[0], m.team_scores[1])
        } else {
            (
                scores.iter().enumerate().filter(|(s, _)| s % 2 == 0).map(|(_, n)| n).sum(),
                scores.iter().enumerate().filter(|(s, _)| s % 2 == 1).map(|(_, n)| n).sum(),
            )
        };
        let my_team = team_of(m.my_seat);
        let mine = if my_team == 0 { red } else { blu };
        let theirs = if my_team == 0 { blu } else { red };
        let won = mine > theirs;
        m.result = format!(
            "{}
RED {} - BLUE {}
YOU PLAYED FOR {}",
            if won {
                "YOUR SIDE OF THE FLOOR WINS."
            } else if mine == theirs {
                "DEAD EVEN. SPLIT THE SNACKS."
            } else {
                "THE OTHER POD WINS."
            },
            red,
            blu,
            if my_team == 0 { "RED" } else { "BLUE" },
        );
        m.score += mine * 100 + if won { 500 } else { 0 };
        if won {
            stat("extractions", 1);
        }
        m.over = Some(Timer::from_seconds(3.0, TimerMode::Once));
        sfx(if won { "win" } else { "over" });
        return;
    }
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
/// March the ray for the first RAISED floor cell (deck or step) before a
/// wall; returns (distance, deck height). Coarse steps are plenty here.
fn cast_step(m: &Mission, ox: f32, oy: f32, ang: f32, max: f32) -> Option<(f32, f32)> {
    let (dx, dy) = (ang.cos(), ang.sin());
    let mut t = 0.12;
    while t < max {
        let (cx, cy) = (ox + dx * t, oy + dy * t);
        let (ix, iy) = (cx.floor() as i32, cy.floor() as i32);
        if ix < 0 || ix >= MW as i32 || iy < 0 || iy >= MH as i32 {
            return None;
        }
        if m.solid(ix, iy) {
            return None;
        }
        let h = m.heights[iy as usize * MW + ix as usize];
        if h > 0.01 {
            return Some((t, h));
        }
        t += 0.08;
    }
    None
}

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
    // F sets off every sticky mine you've planted, wherever you are.
    if keys.just_pressed(KeyCode::KeyF) && !m.mines.is_empty() {
        let planted = std::mem::take(&mut m.mines);
        for mn in planted {
            boom(&mut m, &mut guards, &remotes, mn.x, mn.y);
        }
    }
    let pressed = keys.just_pressed(KeyCode::Space) || buttons.just_pressed(MouseButton::Left);
    if !pressed || m.fire_cd > 0.0 {
        return;
    }
    let w = m.weapon;
    let cost = match w {
        W_SLAP => 0,
        W_MEMO => 3,
        _ => 1,
    };
    let pool = ammo_pool(&m, w);
    if pool >= 0 && pool < cost {
        sfx("tick"); // dry click: the office is out
        return;
    }
    spend(&mut m, w, cost);
    m.fire_cd = weapon_cd(w);
    m.flash = 0.08;
    if cost > 0 {
        stat("darts_fired", cost as u64);
    }
    // The quiet tools don't announce you; everything else does.
    let loud = !matches!(w, W_SLAP | W_OPENERS | W_MINES);
    if loud {
        m.fired_flag = true;
        m.cloak_t = m.cloak_t.min(1.5); // gunfire burns the ghost badge
    }
    match w {
        W_OPENERS => {
            // A letter opener, thrown hard and silently.
            let d = Vec2::new(m.ang.cos(), m.ang.sin());
            let p = Proj { x: m.px + d.x * 0.4, y: m.py + d.y * 0.4, dx: d.x * 13.0, dy: d.y * 13.0, kind: 0 };
            m.projs.push(p);
            sfx("tick");
            return;
        }
        W_MORTAR => {
            // The confetti mortar lobs a slow, unmissable dot of regret.
            let d = Vec2::new(m.ang.cos(), m.ang.sin());
            let p = Proj { x: m.px + d.x * 0.5, y: m.py + d.y * 0.5, dx: d.x * 8.5, dy: d.y * 8.5, kind: 1 };
            m.projs.push(p);
            sfx("fire");
            return;
        }
        W_MINES => {
            // Plant on the surface you're looking at (or the carpet ahead).
            if m.mines.len() >= 5 {
                sfx("tick");
                return;
            }
            let (wall_d, _, _) = cast(&m, m.px, m.py, m.ang, 8.0);
            let d = (wall_d - 0.35).clamp(0.4, 6.0);
            let mx = m.px + m.ang.cos() * d;
            let my = m.py + m.ang.sin() * d;
            m.mines.push(Mine { x: mx, y: my });
            sfx("drop");
            return;
        }
        W_SLAP => sfx("thud"),
        _ => sfx("fire"),
    }
    // Hitscan (and the slap, which is just a very short hitscan).
    let range = match w {
        W_SLAP => 1.6,
        W_POPPER => 8.0,
        _ => 20.0,
    };
    let take = match w {
        W_MEMO => 3,
        W_POPPER => 2,
        _ => 1,
    };
    let cone_for = |d: f32| -> f32 {
        match w {
            W_SLAP => 0.7,
            W_MEMO => 0.30 + 0.30 / d.max(0.5),
            W_POPPER => 0.22 + 0.30 / d.max(0.5),
            W_GOLDEN => 0.04 + 0.18 / d.max(0.5),
            _ => 0.05 + 0.25 / d.max(0.5),
        }
    };
    let (wall_d, _, _) = cast(&m, m.px, m.py, m.ang, 24.0);
    // Lighting control, the fun way: an aimed shot pops a ceiling lamp.
    if w != W_SLAP {
        for i in 0..m.lamps.len() {
            let (lx, ly, alive) = m.lamps[i];
            if !alive {
                continue;
            }
            let rel = Vec2::new(lx as f32 + 0.5 - m.px, ly as f32 + 0.5 - m.py);
            let d = rel.length();
            if d > 14.0 || d >= wall_d + 0.3 {
                continue;
            }
            let mut da = rel.y.atan2(rel.x) - m.ang;
            while da > std::f32::consts::PI {
                da -= std::f32::consts::TAU;
            }
            while da < -std::f32::consts::PI {
                da += std::f32::consts::TAU;
            }
            // You have to aim UP at a lamp: pitch well above level.
            if da.abs() < 0.05 + 0.20 / d.max(0.5) && m.pitch > 40.0 && los(&m, m.px, m.py, lx as f32 + 0.5, ly as f32 + 0.5) {
                break_lamp(&mut m, i);
                break;
            }
        }
    }
    if m.dm {
        // OFFICE PARTY: the same aim, pointed at rival auditors.
        let mut hits: Vec<(usize, f32)> = Vec::new();
        for r in remotes.0.iter().filter(|r| !r.napping && r.heard) {
            if same_team(&m, r.seat) {
                continue; // no staplers at your own desk pod
            }
            let rel = Vec2::new(r.x - m.px, r.y - m.py);
            let d = rel.length();
            if d > range || d >= wall_d + 0.3 {
                continue;
            }
            let mut da = rel.y.atan2(rel.x) - m.ang;
            while da > std::f32::consts::PI {
                da -= std::f32::consts::TAU;
            }
            while da < -std::f32::consts::PI {
                da += std::f32::consts::TAU;
            }
            if da.abs() < cone_for(d) && los(&m, m.px, m.py, r.x, r.y) {
                hits.push((r.seat, d));
            }
        }
        hits.sort_by(|a, b| a.1.partial_cmp(&b.1).unwrap_or(std::cmp::Ordering::Equal));
        let mut landed = false;
        for (seat, d) in hits.into_iter().take(take) {
            // One wire hit per point of hurt: the golden stapler sends a
            // whole stack so one tap ends the argument, vest or not.
            let stack = match w {
                W_GOLDEN => 5,
                W_POPPER if d < 3.0 => 2,
                _ => 1,
            };
            for _ in 0..stack {
                claim(&mut m, seat as u8);
            }
            landed = true;
        }
        if landed {
            sfx(if w == W_MEMO { "boom" } else { "rotate" });
        }
        return;
    }
    // Solo: hitscan against guards.
    let mut matched: Vec<(usize, f32)> = Vec::new();
    for (i, g) in guards.0.iter().enumerate() {
        if g.hp <= 0 {
            continue;
        }
        let rel = Vec2::new(g.x - m.px, g.y - m.py);
        let d = rel.length();
        if d > range || d >= wall_d + 0.3 {
            continue;
        }
        let mut da = rel.y.atan2(rel.x) - m.ang;
        while da > std::f32::consts::PI {
            da -= std::f32::consts::TAU;
        }
        while da < -std::f32::consts::PI {
            da += std::f32::consts::TAU;
        }
        if da.abs() < cone_for(d) && los(&m, m.px, m.py, g.x, g.y) {
            matched.push((i, d));
        }
    }
    matched.sort_by(|a, b| a.1.partial_cmp(&b.1).unwrap_or(std::cmp::Ordering::Equal));
    let dmg = match w {
        W_SLAP | W_MEMO | W_POPPER => 2,
        W_GOLDEN => 99,
        _ => 1,
    };
    let mut any = false;
    for (i, _) in matched.into_iter().take(take) {
        let g = &mut guards.0[i];
        g.hp -= dmg;
        g.alert = true;
        any = true;
        if g.hp <= 0 {
            m.score += 150;
            stat("guards_tranqed", 1);
        }
    }
    if any {
        sfx(if w == W_MEMO { "boom" } else { "capture" });
    }
}

/// One point of hurt claimed on a rival seat: over the wire in a real
/// party, straight onto the bot ledger in a practice one.
fn claim(m: &mut Mission, seat: u8) {
    if m.practice {
        m.bot_hits.push(seat);
    } else if let Ok(w) = serde_json::to_string(&WireHit { t: "hit".into(), v: seat }) {
        net_send(&w);
    }
}

/// Shoot out a lamp: the cell goes dark for everyone.
fn break_lamp(m: &mut Mission, i: usize) {
    let (lx, ly, _) = m.lamps[i];
    m.lamps[i].2 = false;
    recompute_light(m);
    sfx("thud");
    if m.dm {
        if let Ok(w) = serde_json::to_string(&WireLamp { t: "lamp".into(), x: lx as i32, y: ly as i32 }) {
            net_send(&w);
        }
    }
}

/// Splash damage at a point: confetti mortar shells and sticky mines.
fn boom(m: &mut Mission, guards: &mut Guards, remotes: &Remotes, x: f32, y: f32) {
    sfx("boom");
    m.flash = 0.14;
    for i in 0..m.lamps.len() {
        let (lx, ly, alive) = m.lamps[i];
        if alive && ((lx as f32 + 0.5 - x).powi(2) + (ly as f32 + 0.5 - y).powi(2)).sqrt() < 2.3 {
            break_lamp(m, i);
        }
    }
    if m.dm {
        let mut hit: Vec<(usize, f32)> = remotes
            .0
            .iter()
            .filter(|r| !r.napping && r.heard && !same_team(m, r.seat))
            .map(|r| (r.seat, ((r.x - x).powi(2) + (r.y - y).powi(2)).sqrt()))
            .filter(|(_, d)| *d < 2.3)
            .collect();
        hit.sort_by(|a, b| a.1.partial_cmp(&b.1).unwrap_or(std::cmp::Ordering::Equal));
        for (seat, _) in hit.into_iter().take(3) {
            claim(m, seat as u8);
            claim(m, seat as u8);
        }
        return;
    }
    for g in guards.0.iter_mut() {
        if g.hp <= 0 {
            continue;
        }
        let d = ((g.x - x).powi(2) + (g.y - y).powi(2)).sqrt();
        if d < 2.3 {
            g.hp -= 3;
            g.alert = true;
            if g.hp <= 0 {
                m.score += 150;
                stat("guards_tranqed", 1);
            }
        }
    }
    // Standing next to your own fireworks is a choice.
    let dme = ((m.px - x).powi(2) + (m.py - y).powi(2)).sqrt();
    if dme < 1.8 {
        let dmg = 20;
        let soak = dmg.min(m.armor);
        m.armor -= soak;
        m.hp -= dmg - soak;
        m.hurt = 0.45;
    }
}

/// Letter openers and mortar shells in flight, frame by frame.
fn munitions(
    time: Res<Time>,
    mut m: ResMut<Mission>,
    mut guards: ResMut<Guards>,
    remotes: Res<Remotes>,
) {
    if m.over.is_some() || m.projs.is_empty() {
        return;
    }
    let dt = time.delta_secs();
    let mut flying = std::mem::take(&mut m.projs);
    let mut booms: Vec<(f32, f32)> = Vec::new();
    let mut knifed: Vec<u8> = Vec::new();
    flying.retain_mut(|p| {
        p.x += p.dx * dt;
        p.y += p.dy * dt;
        if m.solid(p.x.floor() as i32, p.y.floor() as i32) {
            if p.kind == 1 {
                booms.push((p.x - p.dx * dt, p.y - p.dy * dt));
            }
            return false;
        }
        if m.dm {
            for r in remotes.0.iter().filter(|r| !r.napping && r.heard) {
                if same_team(&m, r.seat) {
                    continue;
                }
                if ((r.x - p.x).powi(2) + (r.y - p.y).powi(2)).sqrt() < 0.6 {
                    if p.kind == 1 {
                        booms.push((p.x, p.y));
                    } else {
                        knifed.push(r.seat as u8);
                        sfx("rotate");
                    }
                    return false;
                }
            }
        } else {
            for g in guards.0.iter_mut().filter(|g| g.hp > 0) {
                if ((g.x - p.x).powi(2) + (g.y - p.y).powi(2)).sqrt() < 0.55 {
                    if p.kind == 1 {
                        booms.push((p.x, p.y));
                    } else {
                        g.hp -= 2;
                        g.alert = true;
                        if g.hp <= 0 {
                            m.score += 150;
                            stat("guards_tranqed", 1);
                        }
                        sfx("capture");
                    }
                    return false;
                }
            }
        }
        true
    });
    m.projs = flying;
    for seat in knifed {
        claim(&mut m, seat);
        claim(&mut m, seat);
    }
    for (bx, by) in booms {
        boom(&mut m, &mut guards, &remotes, bx, by);
    }
}

/// Bots carry badges: a closed door in their way simply opens. (Practice
/// parties are local, so nobody needs to hear about it on the wire.)
fn bot_unlock(m: &mut Mission, x: i32, y: i32) {
    if let Cell::Wall(4) = m.at(x, y) {
        let idx = y as usize * MW + x as usize;
        if !m.doors_open[idx] {
            m.doors_open[idx] = true;
            sfx("drop");
        }
    }
}

/// The practice party's rivals: apply the hits I claimed, run each bot's
/// strafe-and-shoot brain, let them feud with each other, mirror them into
/// the Remotes list the renderer and my own aim already understand.
#[allow(clippy::too_many_arguments)]
fn bots_think(
    time: Res<Time>,
    mut rng: ResMut<Rng>,
    mut m: ResMut<Mission>,
    mut bots: ResMut<Bots>,
    mut remotes: ResMut<Remotes>,
) {
    if !m.practice || m.over.is_some() {
        return;
    }
    let dt = time.delta_secs();
    // Hits I landed this frame (hitscan, knives, splash).
    let hits = std::mem::take(&mut m.bot_hits);
    for seat in hits {
        if let Some(b) = bots.0.iter_mut().find(|b| b.seat == seat as usize && b.nap_t <= 0.0) {
            b.hp -= 1;
            if b.hp <= 0 {
                b.nap_t = 3.0;
                let was_glad = b.seat == m.glad;
                let my_seat = m.my_seat;
                if m.mode != 3 || my_seat == m.glad {
                    m.dm_scores[my_seat] += 1;
                    m.score += 150;
                }
                if m.mode == 3 && was_glad {
                    m.glad = my_seat;
                    sfx("power");
                }
                stat("guards_tranqed", 1);
                sfx("capture");
            }
        }
    }
    let me = Vec2::new(m.px, m.py);
    let my_light = light_at(&m, m.px, m.py);
    let snap: Vec<(usize, f32, f32, f32)> = bots.0.iter().map(|b| (b.seat, b.x, b.y, b.nap_t)).collect();
    let mut feud: Vec<(usize, usize)> = Vec::new(); // (victim seat, shooter seat)
    for b in bots.0.iter_mut() {
        if b.nap_t > 0.0 {
            b.nap_t -= dt;
            if b.nap_t <= 0.0 {
                let idx = rng.range(m.spawns.len() as u32) as usize;
                let (sx, sy) = m.spawns[idx];
                b.x = sx;
                b.y = sy;
                b.hp = DM_HP;
            }
            continue;
        }
        // Target me if I'm visible: darkness, crouching, and the ghost
        // badge all work on bots exactly like they work on guards.
        let dme = (me - Vec2::new(b.x, b.y)).length();
        let mut reach = 11.0 * (0.35 + 0.65 * my_light);
        if m.crouch {
            reach *= 0.75;
        }
        let teams_on = m.mode == 1 || m.mode == 2;
        let see_me = m.nap_t <= 0.0
            && m.cloak_t <= 0.0
            && !(teams_on && team_of(b.seat) == team_of(m.my_seat))
            && dme < reach
            && los(&m, b.x, b.y, m.px, m.py);
        let mut target: Option<(Vec2, Option<usize>)> = if see_me { Some((me, None)) } else { None };
        if target.is_none() {
            // No sign of the player: pick a fight with the nearest bot.
            let mut best: Option<(f32, usize, Vec2)> = None;
            for (s, x, y, nap) in snap.iter().copied() {
                if s == b.seat || nap > 0.0 || (teams_on && team_of(s) == team_of(b.seat)) {
                    continue;
                }
                let p = Vec2::new(x, y);
                let d = (p - Vec2::new(b.x, b.y)).length();
                if d < 10.0
                    && los(&m, b.x, b.y, x, y)
                    && best.map(|(bd, _, _)| d < bd).unwrap_or(true)
                {
                    best = Some((d, s, p));
                }
            }
            target = best.map(|(_, s, p)| (p, Some(s)));
        }
        // A bot with the binder forgets everything except the way home.
        if m.mode == 2 {
            if m.flags.iter().any(|f| f.state == 1 && f.carrier == b.seat) {
                let base = m.flags[team_of(b.seat)];
                let rel = Vec2::new(base.hx - b.x, base.hy - b.y);
                let d = rel.length().max(0.01);
                let fwd = rel / d;
                let (nx, ny) = (b.x + fwd.x * 2.6 * dt, b.y + fwd.y * 2.6 * dt);
                bot_unlock(&mut m, nx.floor() as i32, ny.floor() as i32);
                if !m.solid(nx.floor() as i32, b.y.floor() as i32) {
                    b.x = nx;
                }
                if !m.solid(b.x.floor() as i32, ny.floor() as i32) {
                    b.y = ny;
                }
                b.dir = fwd;
                continue;
            }
        }
        if let Some((tp, tseat)) = target {
            let rel = tp - Vec2::new(b.x, b.y);
            let d = rel.length().max(0.01);
            let fwd = rel / d;
            b.wander_cd -= dt;
            if b.wander_cd <= 0.0 {
                b.wander_cd = 0.8 + rng.range(14) as f32 / 10.0;
                b.strafe = if rng.chance(0.5) { 1.0 } else { -1.0 };
            }
            // Circle-strafe: sidestep hard, close only when far, back off
            // when crowded. It reads as "smart" because it never stands still.
            let perp = Vec2::new(-fwd.y, fwd.x) * b.strafe;
            let approach = if d > 6.0 {
                1.0
            } else if d < 2.8 {
                -0.8
            } else {
                0.0
            };
            let vel = (fwd * approach + perp * 0.8).normalize_or_zero() * 2.6;
            let (nx, ny) = (b.x + vel.x * dt, b.y + vel.y * dt);
            bot_unlock(&mut m, nx.floor() as i32, ny.floor() as i32);
            if !m.solid(nx.floor() as i32, b.y.floor() as i32) {
                b.x = nx;
            }
            if !m.solid(b.x.floor() as i32, ny.floor() as i32) {
                b.y = ny;
            }
            b.dir = fwd;
            b.shoot_cd -= dt;
            if b.shoot_cd <= 0.0 && d < 9.0 {
                b.shoot_cd = 0.7 + rng.range(8) as f32 / 10.0;
                sfx("drop");
                if tseat.is_none() {
                    let mut chance = (0.62 - d * 0.045).max(0.15) * (0.45 + 0.55 * my_light);
                    if m.crouch {
                        chance *= 0.7;
                    }
                    if rng.chance(chance) {
                        if m.armor >= 25 {
                            m.armor -= 25;
                        } else {
                            m.dm_hp -= 1;
                        }
                        m.hurt = 0.4;
                        sfx("death");
                        if m.dm_hp <= 0 {
                            m.nap_t = 3.0;
                            let scores_count = m.mode != 3 || b.seat == m.glad;
                            if scores_count && b.seat < m.dm_scores.len() {
                                m.dm_scores[b.seat] += 1;
                            }
                            if m.mode == 3 && m.glad == m.my_seat {
                                m.glad = b.seat;
                                sfx("power");
                            }
                            stat("audits_failed", 1);
                            sfx("over");
                        }
                    }
                } else if let Some(ts) = tseat {
                    if rng.chance(0.30) {
                        feud.push((ts, b.seat));
                    }
                }
            }
        } else {
            // Nobody around: drift the halls like a guard on rounds.
            b.wander_cd -= dt;
            if b.wander_cd <= 0.0 {
                b.wander_cd = 1.2 + rng.range(20) as f32 / 10.0;
                let a = rng.range(628) as f32 / 100.0;
                b.dir = Vec2::new(a.cos(), a.sin());
            }
            let step = b.dir * 1.5 * dt;
            bot_unlock(&mut m, (b.x + step.x).floor() as i32, (b.y + step.y).floor() as i32);
            if m.solid((b.x + step.x).floor() as i32, (b.y + step.y).floor() as i32) {
                b.dir = -b.dir;
            } else {
                b.x += step.x;
                b.y += step.y;
            }
        }
    }
    for (victim, shooter) in feud {
        if let Some(v) = bots.0.iter_mut().find(|b| b.seat == victim && b.nap_t <= 0.0) {
            v.hp -= 1;
            if v.hp <= 0 {
                v.nap_t = 3.0;
                if (m.mode != 3 || shooter == m.glad) && shooter < m.dm_scores.len() {
                    m.dm_scores[shooter] += 1;
                }
                if m.mode == 3 && victim == m.glad {
                    m.glad = shooter;
                }
            }
        }
    }
    // Mirror into the Remotes list (position, nap, costume).
    for b in bots.0.iter() {
        if let Some(r) = remotes.0.iter_mut().find(|r| r.seat == b.seat) {
            r.px = b.x;
            r.py = b.y;
            r.x = b.x;
            r.y = b.y;
            r.t = 1.0;
            r.napping = b.nap_t > 0.0;
            r.heard = true;
            r.ch = b.ch;
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
        // Standing in the dark, crouching, or wearing the ghost badge all
        // shrink how far a guard can pick you out.
        let mut reach = 9.0 * (0.35 + 0.65 * light_at(&m, m.px, m.py));
        if m.crouch {
            reach *= 0.7;
        }
        let sees = m.cloak_t <= 0.0 && dist < reach && los(&m, g.x, g.y, m.px, m.py) && {
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
                    let soak = dmg.min(m.armor);
                    m.armor -= soak;
                    m.hp -= dmg - soak;
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
    // Columns. The scope narrows the lens.
    let fov = FOV / m.zoom;
    for i in 0..NCOL {
        let lens = (i as f32 / (NCOL - 1) as f32 - 0.5) * fov;
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
        let a = m.ang + lens;
        let lw = light_at(&m, m.px + a.cos() * (d - 0.2).max(0.1), m.py + a.sin() * (d - 0.2).max(0.1));
        let dark = 0.30 + 0.70 * lw;
        let shade = (1.0 - (dcorr / 16.0)).clamp(0.15, 1.0) * if vertical { 1.0 } else { 0.78 } * dark;
        let c = base.to_srgba();
        // Pitch shears the horizon; your eye height shifts near walls more
        // than far ones — that parallax is what sells standing on a deck.
        let hfull = VIEW_H * 0.9 / dcorr;
        let ycenter = VIEW_CY + m.pitch - m.pz * hfull;
        if let Ok((mut sp, mut tf, mut vis)) = sprites.get_mut(view.cols[i]) {
            sp.color = Color::srgb(c.red * shade, c.green * shade, c.blue * shade);
            sp.custom_size = Some(Vec2::new(COLW, h));
            tf.translation.y = ycenter;
            *vis = if kind == 0 { Visibility::Hidden } else { Visibility::Inherited };
        }
        // A raised deck between you and that wall: draw its front face
        // rising from the floor line.
        if let Ok((mut sp, mut tf, mut vis)) = sprites.get_mut(view.steps[i]) {
            match cast_step(&m, m.px, m.py, m.ang + lens, dcorr.min(14.0)) {
                Some((ds, deck)) if ds < dcorr => {
                    let dsc = (ds * lens.cos()).max(0.15);
                    let hf = VIEW_H * 0.9 / dsc;
                    let bottom = VIEW_CY + m.pitch - m.pz * hf - hf / 2.0;
                    let face = deck * hf;
                    let s2 = (1.0 - (dsc / 16.0)).clamp(0.2, 1.0);
                    sp.color = Color::srgb(0.34 * s2, 0.33 * s2, 0.30 * s2);
                    sp.custom_size = Some(Vec2::new(COLW, face.min(VIEW_H)));
                    tf.translation.y = bottom + face / 2.0;
                    *vis = Visibility::Inherited;
                }
                _ => {
                    *vis = Visibility::Hidden;
                }
            }
        }
    }
    // Billboards, farthest first so near ones overwrite via z.
    struct Bill {
        x: f32,
        y: f32,
        w: f32,
        h: f32,
        color: Color,
        dy: f32,   // vertical offset factor (pickups sit low)
        zb: f32,   // tiny z bump so heads draw over torsos
        elev: f32, // floor elevation under this thing (decks lift it)
        person: bool, // warm body: thermal specs light these up
    }
    let mut items: Vec<(f32, Bill)> = Vec::new();
    // Guards and rival auditors are FIGURES now — torso plus a head —
    // instead of anonymous slabs. Sleepers stay lumps.
    for g in &guards.0 {
        let color = if g.hp <= 0 {
            Color::srgb(0.35, 0.35, 0.40)
        } else if g.alert {
            RED
        } else {
            AMBER
        };
        if g.hp <= 0 {
            items.push((0.0, Bill { x: g.x, y: g.y, w: 0.5, h: 0.35, color, dy: -0.28, zb: 0.0, elev: 0.0, person: true }));
        } else {
            items.push((0.0, Bill { x: g.x, y: g.y, w: 0.42, h: 0.62, color, dy: -0.14, zb: 0.0, elev: 0.0, person: true }));
            items.push((0.0, Bill { x: g.x, y: g.y, w: 0.20, h: 0.20, color: Color::srgb(0.85, 0.70, 0.55), dy: 0.30, zb: 0.004, elev: 0.0, person: true }));
            items.push((0.0, Bill { x: g.x, y: g.y, w: 0.24, h: 0.08, color: Color::srgb(0.12, 0.14, 0.22), dy: 0.42, zb: 0.006, elev: 0.0, person: true })); // the cap
        }
    }
    for r in remotes.0.iter().filter(|r| r.heard) {
        let (x, y) = (r.px + (r.x - r.px) * r.t, r.py + (r.y - r.py) * r.t);
        let (_, cw, chh, skin, hat) = CHARACTERS[r.ch as usize % CHARACTERS.len()];
        // Seat color says WHO (and, in team modes, WHOSE SIDE); the
        // character sets the silhouette. A ghost badge is just a shimmer.
        let mut color = if m.mode == 1 || m.mode == 2 {
            if team_of(r.seat) == 0 {
                Color::srgb(0.95, 0.25, 0.22)
            } else {
                Color::srgb(0.25, 0.45, 0.95)
            }
        } else {
            PLAYER_COLORS[r.seat % 12]
        };
        if r.cloak {
            color = color.with_alpha(0.10);
        }
        if r.napping {
            items.push((0.0, Bill { x, y, w: 0.5, h: 0.35, color, dy: -0.28, zb: 0.0, elev: 0.0, person: true }));
        } else {
            let (bw, bh, hdy) = if r.crouch { (cw * 1.15, chh * 0.6, 0.12) } else { (cw, chh, 0.30) };
            let skin_c = Color::srgb(skin.0, skin.1, skin.2);
            items.push((0.0, Bill { x, y, w: bw, h: bh, color, dy: -0.14, zb: 0.0, elev: 0.0, person: true }));
            items.push((0.0, Bill {
                x,
                y,
                w: 0.20,
                h: 0.20,
                color: if r.cloak { skin_c.with_alpha(0.10) } else { skin_c },
                dy: hdy,
                zb: 0.004,
                elev: 0.0,
                person: true,
            }));
            if let Some(hc) = hat {
                let hat_c = Color::srgb(hc.0, hc.1, hc.2);
                items.push((0.0, Bill {
                    x,
                    y,
                    w: 0.24,
                    h: 0.08,
                    color: if r.cloak { hat_c.with_alpha(0.10) } else { hat_c },
                    dy: hdy + 0.12,
                    zb: 0.006,
                    elev: 0.0,
                    person: true,
                }));
            }
            // The gladiator wears a gold crown you can spot across the floor.
            if m.mode == 3 && r.seat == m.glad {
                items.push((0.0, Bill { x, y, w: 0.18, h: 0.10, color: Color::srgb(1.0, 0.84, 0.20), dy: hdy + 0.22, zb: 0.008, elev: 0.0, person: false }));
            }
        }
    }
    // The team binders: tall bright banners wherever they currently are.
    if m.dm && m.mode == 2 {
        for (fi, f) in m.flags.iter().enumerate() {
            if f.state == 1 && f.carrier == m.my_seat {
                continue; // it's on my back; the HUD carries that news
            }
            let (fx, fy) = if f.state == 0 { (f.hx, f.hy) } else { (f.x, f.y) };
            let color = if fi == 0 { Color::srgb(1.0, 0.20, 0.15) } else { Color::srgb(0.20, 0.45, 1.0) };
            let dy = if f.state == 1 { 0.55 } else { -0.10 };
            items.push((0.0, Bill { x: fx, y: fy, w: 0.14, h: 0.55, color, dy, zb: 0.007, elev: 0.0, person: false }));
        }
    }
    for p in pickups.0.iter().filter(|p| !p.taken) {
        let color = match p.kind {
            PickupKind::File => AMBER,
            PickupKind::Darts => CYAN,
            PickupKind::Coffee => MAGENTA,
            PickupKind::Rapid => Color::srgb(0.3, 0.6, 1.0),
            PickupKind::Memo => Color::srgb(0.95, 0.95, 0.92),
            PickupKind::Vest => GREEN,
            PickupKind::Espresso => Color::srgb(0.95, 0.55, 0.15),
            PickupKind::Popper => Color::srgb(0.95, 0.35, 0.65),
            PickupKind::Openers => Color::srgb(0.75, 0.80, 0.88),
            PickupKind::Mortar => Color::srgb(0.90, 0.25, 0.20),
            PickupKind::Golden => Color::srgb(1.0, 0.84, 0.20),
            PickupKind::MinesKit => Color::srgb(0.55, 0.90, 0.75),
            PickupKind::Shells => Color::srgb(0.85, 0.55, 0.85),
            PickupKind::Cloak => Color::srgb(0.65, 0.70, 1.0),
            PickupKind::Thermal => Color::srgb(1.0, 0.45, 0.10),
        };
        items.push((0.0, Bill { x: p.x, y: p.y, w: 0.28, h: 0.3, color, dy: -0.3, zb: 0.0, elev: 0.0, person: false }));
    }
    // Ceiling lamps: warm and bright until somebody shoots them out.
    for (lx, ly, alive) in m.lamps.iter().copied() {
        let color = if alive { Color::srgb(1.0, 0.92, 0.55) } else { Color::srgb(0.22, 0.22, 0.24) };
        items.push((0.0, Bill { x: lx as f32 + 0.5, y: ly as f32 + 0.5, w: 0.30, h: 0.10, color, dy: 0.52, zb: 0.001, elev: 0.0, person: false }));
    }
    // Things in flight, and the blinking sticky mines.
    for p in m.projs.iter() {
        let (w, h, color) = if p.kind == 1 {
            (0.22, 0.22, Color::srgb(0.95, 0.30, 0.20))
        } else {
            (0.10, 0.10, Color::srgb(0.82, 0.86, 0.92))
        };
        items.push((0.0, Bill { x: p.x, y: p.y, w, h, color, dy: 0.0, zb: 0.003, elev: 0.0, person: false }));
    }
    for mn in m.mines.iter() {
        let blink = if (m.clock * 6.0) as i32 % 2 == 0 {
            Color::srgb(1.0, 0.25, 0.20)
        } else {
            Color::srgb(0.70, 0.60, 0.20)
        };
        items.push((0.0, Bill { x: mn.x, y: mn.y, w: 0.16, h: 0.12, color: blink, dy: -0.38, zb: 0.002, elev: 0.0, person: false }));
    }
    // The server console tile glows until bugged — the "go here" beacon.
    if !m.server_bugged && !m.dm {
        let (sx, sy) = m.server_cell;
        items.push((0.0, Bill { x: sx as f32 + 0.5, y: sy as f32 + 0.5, w: 0.4, h: 0.2, color: GREEN, dy: -0.35, zb: 0.0, elev: 0.0, person: false }));
    }
    for it in items.iter_mut() {
        let rel = Vec2::new(it.1.x - m.px, it.1.y - m.py);
        it.0 = rel.length();
        let (cx, cy) = (
            (it.1.x.floor() as usize).min(MW - 1),
            (it.1.y.floor() as usize).min(MH - 1),
        );
        it.1.elev = m.heights[cy * MW + cx];
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
        if da.abs() > fov / 2.0 + 0.25 {
            continue;
        }
        let sx = (da / fov + 0.5) * 720.0 - 360.0;
        let col = (((sx + 360.0) / COLW) as usize).min(NCOL - 1);
        let dcorr = dist * (da.cos()).max(0.3);
        let thermal_body = m.thermal_t > 0.0 && b.person;
        if view.depth[col] + 0.4 < dcorr && !thermal_body {
            continue; // a wall is in front (thermal sees warm bodies through it)
        }
        let scale = VIEW_H * 0.9 / dcorr.max(0.2);
        let e = view.bills[used];
        used += 1;
        if let Ok((mut sp, mut tf, mut vis)) = sprites.get_mut(e) {
            sp.color = if thermal_body {
                Color::srgb(1.0, 0.45, 0.08)
            } else {
                let lw = 0.30 + 0.70 * light_at(&m, b.x, b.y);
                let c = b.color.to_srgba();
                Color::srgb(c.red * lw, c.green * lw, c.blue * lw)
            };
            sp.custom_size = Some(Vec2::new(scale * b.w, scale * b.h));
            tf.translation.x = sx;
            tf.translation.y = VIEW_CY + m.pitch + scale * b.dy + (b.elev - m.pz) * scale;
            tf.translation.z = 2.0 + (30.0 - dist) * 0.01 + b.zb;
            *vis = Visibility::Inherited;
        }
    }
    for &e in view.bills.iter().skip(used) {
        if let Ok((_, _, mut vis)) = sprites.get_mut(e) {
            *vis = Visibility::Hidden;
        }
    }
    // Horizon plates follow the pitch shear.
    if let Ok((_, mut tf, _)) = sprites.get_mut(view.ceil) {
        tf.translation.y = VIEW_CY + VIEW_H / 2.0 + m.pitch - m.pz * 40.0;
    }
    if let Ok((_, mut tf, _)) = sprites.get_mut(view.floor) {
        tf.translation.y = VIEW_CY - VIEW_H / 2.0 + m.pitch - m.pz * 40.0;
    }
    // Veils: RED means you were hurt; firing never tints the screen red —
    // the gun kick and amber tracer carry that instead.
    if let Ok(mut sp) = veil.single_mut() {
        if m.nap_t > 0.0 {
            sp.color = RED.with_alpha(0.55);
        } else if m.hurt > 0.0 {
            sp.color = RED.with_alpha((m.hurt * 0.9).min(0.35));
        } else if m.thermal_t > 0.0 {
            sp.color = Color::srgb(0.1, 0.2, 0.9).with_alpha(0.16);
        } else {
            sp.color = RED.with_alpha(0.0);
        }
    }
    // The gun kicks back while the muzzle timer runs; the tracer streaks.
    if let Ok((_, mut tf, _)) = sprites.get_mut(view.gun) {
        tf.translation.y = VIEW_CY - VIEW_H / 2.0 + 40.0 - m.flash * 220.0;
    }
    if let Ok((_, _, mut vis)) = sprites.get_mut(view.tracer) {
        *vis = if m.flash > 0.0 { Visibility::Inherited } else { Visibility::Hidden };
    }
}

#[allow(clippy::type_complexity)]
fn hud(
    m: Res<Mission>,
    mut hud: Query<&mut Text2d, (With<HudText>, Without<ObjText>, Without<PromptText>, Without<HelpLine>)>,
    mut obj: Query<&mut Text2d, (With<ObjText>, Without<HudText>, Without<PromptText>, Without<HelpLine>)>,
    mut prompt: Query<&mut Text2d, (With<PromptText>, Without<HudText>, Without<ObjText>, Without<HelpLine>)>,
    mut help: Query<&mut Text2d, (With<HelpLine>, Without<HudText>, Without<ObjText>, Without<PromptText>)>,
) {
    // Contextual prompt: what E would do right now, spelled out.
    if let Ok(mut t) = prompt.single_mut() {
        let s = if m.over.is_some() || m.nap_t > 0.0 {
            String::new()
        } else {
            let dir = Vec2::new(m.ang.cos(), m.ang.sin());
            let (tx, ty) = ((m.px + dir.x * 1.0).floor() as i32, (m.py + dir.y * 1.0).floor() as i32);
            let door_ahead = matches!(m.at(tx, ty), Cell::Wall(4))
                && !m.doors_open[(ty as usize) * MW + tx as usize];
            let on_server = !m.dm
                && !m.server_bugged
                && (m.px.floor() as usize, m.py.floor() as usize) == m.server_cell;
            let near_server = !m.dm
                && !m.server_bugged
                && Vec2::new(
                    m.server_cell.0 as f32 + 0.5 - m.px,
                    m.server_cell.1 as f32 + 0.5 - m.py,
                )
                .length()
                    < 3.0;
            if door_ahead {
                "[E] OPEN THE DOOR".into()
            } else if on_server {
                "[E] PLANT THE BUG".into()
            } else if near_server {
                "STAND ON THE GLOWING TILE, THEN PRESS E".into()
            } else if !m.dm && m.objectives_done() && !m.done {
                "OBJECTIVES DONE - WALK THROUGH THE GREEN DOOR".into()
            } else {
                String::new()
            }
        };
        if t.0 != s {
            t.0 = s;
        }
    }
    if let Ok(mut t) = help.single_mut() {
        let s = if m.dm {
            "MOUSE TURNS (CLICK LOCKS)  WASD MOVE  CLICK FIRES  RMB SCOPE  1-9/Q/WHEEL WEAPONS  C CROUCH  F MINES  E DOORS"
        } else {
            "MOUSE TURNS (CLICK LOCKS)  WASD MOVE  CLICK FIRES  RMB SCOPE  1-9/Q/WHEEL WEAPONS  C CROUCH  F MINES  E USE"
        };
        if t.0 != s {
            t.0 = s.into();
        }
    }
    if let Ok(mut t) = hud.single_mut() {
        let s = if m.over.is_some() {
            m.result.clone()
        } else if m.dm {
            if m.nap_t > 0.0 {
                format!("NAPPING... BACK IN {:.0}", m.nap_t.max(0.0) + 0.99)
            } else {
                let rules = match m.mode {
                    1 => {
                        let red: u32 = m.dm_scores.iter().enumerate().filter(|(s, _)| s % 2 == 0).map(|(_, n)| n).sum();
                        let blu: u32 = m.dm_scores.iter().enumerate().filter(|(s, _)| s % 2 == 1).map(|(_, n)| n).sum();
                        format!("RED {red} - BLUE {blu} ({})", if team_of(m.my_seat) == 0 { "RED" } else { "BLUE" })
                    }
                    2 => {
                        let carrying = m.flags.iter().any(|f| f.state == 1 && f.carrier == m.my_seat);
                        format!(
                            "CAPS R{} - B{}{}",
                            m.team_scores[0],
                            m.team_scores[1],
                            if carrying { "  RUN IT HOME!" } else { "" }
                        )
                    }
                    3 => {
                        if m.glad == m.my_seat {
                            "YOU ARE THE GLADIATOR".into()
                        } else {
                            format!("CROWN: AUDITOR {}", m.glad + 1)
                        }
                    }
                    _ => format!("FIRST TO {DM_TARGET}"),
                };
                format!(
                    "HITS LEFT {}{}   [{}{}]   {}   {}",
                    m.dm_hp.max(0),
                    if m.armor > 0 { " +VEST" } else { "" },
                    weapon_name(m.weapon),
                    ammo_readout(&m),
                    rules,
                    fmt_clock(m.dm_clock.max(0.0))
                )
            }
        } else {
            let vest = if m.armor > 0 { format!(" +{} VEST", m.armor) } else { String::new() };
            let mut zoomies = String::new();
            if m.espresso_t > 0.0 {
                zoomies.push_str("  [FAST]");
            }
            if m.cloak_t > 0.0 {
                zoomies.push_str(&format!("  [GHOST {:.0}]", m.cloak_t.ceil()));
            }
            if m.thermal_t > 0.0 {
                zoomies.push_str("  [THERMAL]");
            }
            if m.crouch {
                zoomies.push_str("  [CROUCHED]");
            }
            format!(
                "HEALTH {}{vest}   [{}{}]{zoomies}   {}",
                m.hp.max(0),
                weapon_name(m.weapon),
                ammo_readout(&m),
                fmt_clock(m.clock)
            )
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

/// " x12" after the weapon name, or nothing for fists.
fn ammo_readout(m: &Mission) -> String {
    let p = ammo_pool(m, m.weapon);
    if p < 0 {
        String::new()
    } else {
        format!(" x{p}")
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
        '^' => Color::srgb(0.30, 0.30, 0.36),
        ',' => Color::srgb(0.22, 0.22, 0.27),
        'f' => AMBER,
        'a' => CYAN,
        'c' => MAGENTA,
        'g' => RED,
        's' => Color::srgb(0.30, 0.75, 0.35),
        'o' => Color::srgb(1.0, 0.92, 0.55),
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

/// The prefab stamps: whole rooms and corridors dropped in one click,
/// the way the classic console map-makers did it. Center-anchored.
fn stamp_pattern(kind: char) -> &'static [&'static str] {
    match kind {
        'R' => &["#####", "#...#", "D...#", "#...#", "#####"],
        'H' => &["#####", ".....", "#####"],
        'I' => &["#.#", "#.#", "#.#", "#.#", "#.#"],
        _ => &[",,", "^^"], // 'T': a two-tier stair up to a deck
    }
}

fn stamp_at(rows: &mut [String], kind: char, cx: usize, cy: usize) {
    let pat = stamp_pattern(kind);
    let ph = pat.len();
    let pw = pat[0].len();
    for (dy, prow) in pat.iter().enumerate() {
        for (dx, ch) in prow.chars().enumerate() {
            let x = (cx + dx).wrapping_sub(pw / 2);
            let y = (cy + dy).wrapping_sub(ph / 2);
            if x == 0 || y == 0 || x >= MW - 1 || y >= MH - 1 {
                continue; // the border is load-bearing
            }
            // Don't stamp over the one-of-a-kind markers.
            if matches!(row_get(rows, x, y), 'p' | 'v' | 'X') {
                continue;
            }
            row_set(rows, x, y, ch);
        }
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
    if keys.just_pressed(KeyCode::Minus) {
        editor.brush = '^';
        sfx("tick");
    }
    if keys.just_pressed(KeyCode::Comma) {
        editor.brush = ',';
        sfx("tick");
    }
    if keys.just_pressed(KeyCode::KeyO) {
        editor.brush = 'o';
        sfx("tick");
    }
    for (key, stamp) in [
        (KeyCode::KeyB, 'R'),
        (KeyCode::KeyN, 'H'),
        (KeyCode::KeyM, 'I'),
        (KeyCode::KeyK, 'T'),
    ] {
        if keys.just_pressed(key) {
            editor.brush = stamp;
            sfx("tick");
        }
    }
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

    // Test-play the canvas in place — or walk its bare shell in first
    // person (T): no guards, no errands, just you and the architecture.
    let preview = keys.just_pressed(KeyCode::KeyT);
    if keys.just_pressed(KeyCode::KeyG) || preview {
        let problem = if preview {
            let starts: usize =
                editor.rows.iter().map(|r| r.chars().filter(|&c| c == 'p').count()).sum();
            if starts == 1 { None } else { Some("PLACE THE START (P)".to_string()) }
        } else {
            validate_office(&editor.rows)
        };
        if let Some(problem) = problem {
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
        m.heights = parsed.heights;
        m.lamps = parsed.lamps;
        recompute_light(&mut m);
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
        if preview {
            guards.0.clear();
            pickups.0.clear();
            m.darts = 999;
        }
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
                    if !erase && matches!(ch, 'R' | 'H' | 'I' | 'T') {
                        if buttons.just_pressed(MouseButton::Left) {
                            stamp_at(&mut editor.rows, ch, x as usize, y as usize);
                            sfx("drop");
                        }
                    } else if matches!(ch, 'p' | 'v' | 'X') && buttons.just_pressed(MouseButton::Left) {
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
                'o' => "LAMP",
                ',' => "STEP",
                '^' => "DECK",
                'R' => "ROOM STAMP",
                'H' => "HALL STAMP",
                'I' => "SHAFT STAMP",
                'T' => "STAIR STAMP",
                _ => "START",
            });
        let s = format!(
            "OFFICE EDITOR - BRUSH: {brush_name}\n1 WALL 2 WOOD 3 RACKS 4 DOOR 5 FLOOR 6 FILE 7 DARTS 8 COFFEE 9 GUARD 0 SPAWN - X EXIT V CONSOLE P START\nO LAMP , STEP - DECK | STAMPS: B ROOM N HALL M SHAFT K STAIRS | S+SHIFT SAVES G TESTS T WALKS-3D X RETURNS"
        );
        if t.0 != s {
            t.0 = s;
        }
    }
}
