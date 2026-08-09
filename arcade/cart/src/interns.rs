//! INTERNS — a walkers-and-jobs cabinet. A stream of clueless new hires
//! marches out of the door; you hand out seven jobs (climb, parachute,
//! supervise, build, bash, dig, quit-loudly) to steer enough of them to the
//! exit. Original name, art, and levels; the genre's mechanics only.
//!
//! Modes: 1P, 2P local (P1 mouse + P2 keyboard cursor), 2P online (host
//! authoritative — guests send assignments, the host streams walker
//! snapshots and terrain deltas), plus a LEVEL EDITOR that saves to the
//! community shelf via the page bridge.

use bevy::prelude::*;
use serde::{Deserialize, Serialize};

use crate::retro::{text, AMBER, CYAN, GREEN, MAGENTA, RED, WHITE};
use crate::shell::{net_send, sfx};
use crate::{CabinetConfig, FinalScore, GameTag, NetIn, NetMode, Phase};

pub const BLURB: &[&str] = &[
    "THE NEW HIRES WALK. THAT'S ALL THEY KNOW.",
    "ASSIGN JOBS. SAVE THE QUOTA. BEAT THE CLOCK.",
    "1P / 2P LOCAL / 2P ONLINE / LEVEL EDITOR",
];

// Terrain grid: 360x270 cells at 2px = 720x540 on screen; HUD below.
pub const GW: i32 = 360;
pub const GH: i32 = 270;
const CELL: f32 = 2.0;
const TICKS_PER_SEC: f32 = 30.0;
const MAX_FALL: i32 = 32; // cells of survivable fall without a parachute
const SKILL_NAMES: [&str; 7] = ["CLIMB", "CHUTE", "SUPER", "BUILD", "BASH", "DIG", "QUIT"];

fn grid_to_world(x: i32, y: i32) -> Vec2 {
    Vec2::new(-360.0 + (x as f32 + 0.5) * CELL, 320.0 - (y as f32 + 0.5) * CELL)
}

fn world_to_grid(w: Vec2) -> (i32, i32) {
    (((w.x + 360.0) / CELL) as i32, ((320.0 - w.y) / CELL) as i32)
}

// ---- level documents ----

fn d_count() -> u32 { 30 }
fn d_rate() -> u32 { 45 }
fn d_need() -> u32 { 15 }
fn d_time() -> u32 { 300 }
fn d_skills() -> [u32; 7] { [5, 5, 5, 10, 5, 5, 5] }

#[derive(Serialize, Deserialize, Clone)]
pub struct LevelDoc {
    pub v: u32,
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub rects: Vec<[i32; 4]>, // additive terrain [x,y,w,h]
    #[serde(default)]
    pub holes: Vec<[i32; 4]>, // subtractive
    #[serde(default)]
    pub runs: Vec<[i32; 3]>, // editor freeform: [y, x0, len]
    pub e1: [i32; 2],
    pub x1: [i32; 2],
    #[serde(default)]
    pub e2: Option<[i32; 2]>,
    #[serde(default)]
    pub x2: Option<[i32; 2]>,
    #[serde(default = "d_count")]
    pub count: u32,
    #[serde(default = "d_rate")]
    pub rate: u32, // ticks between spawns
    #[serde(default = "d_need")]
    pub need: u32,
    #[serde(default = "d_time")]
    pub time: u32, // seconds
    #[serde(default = "d_skills")]
    pub skills: [u32; 7],
}

/// Four house levels so the cabinet works before anyone edits anything.
pub fn builtin(n: usize) -> LevelDoc {
    let base = |rects: Vec<[i32; 4]>, e1: [i32; 2], x1: [i32; 2], skills: [u32; 7]| LevelDoc {
        v: 1,
        name: String::new(),
        rects,
        holes: vec![],
        runs: vec![],
        e1,
        x1,
        e2: None,
        x2: None,
        count: 30,
        rate: 45,
        need: 15,
        time: 300,
        skills,
    };
    match n {
        // ORIENTATION DAY: flat floor, one pit to bridge.
        1 => {
            let mut l = base(
                vec![[10, 200, 150, 12], [210, 200, 140, 12], [0, 260, 360, 10]],
                [30, 180],
                [320, 190],
                [2, 2, 2, 12, 2, 2, 2],
            );
            l.e2 = Some([330, 180]);
            l.x2 = Some([40, 190]);
            l
        }
        // THE BASEMENT: dig down through three slabs.
        2 => {
            let mut l = base(
                vec![
                    [40, 90, 280, 14],
                    [80, 150, 280, 14],
                    [40, 210, 280, 14],
                    [0, 260, 360, 10],
                ],
                [60, 70],
                [300, 240],
                [2, 4, 2, 4, 2, 14, 2],
            );
            l.e2 = Some([300, 70]);
            l.x2 = Some([60, 240]);
            l
        }
        // CUBICLE WALLS: bash through the partitions.
        3 => {
            let mut l = base(
                vec![
                    [0, 210, 360, 14],
                    [90, 150, 12, 60],
                    [180, 150, 12, 60],
                    [270, 150, 12, 60],
                ],
                [30, 190],
                [330, 190],
                [2, 2, 4, 4, 14, 2, 4],
            );
            l.e2 = Some([330, 100]); // P2 arrives on top of the partitions
            l.x2 = Some([30, 100]);
            l.rects.push([0, 120, 360, 8]);
            l
        }
        // TWO TOWERS: symmetric 2P race down the towers.
        _ => {
            let mut l = base(
                vec![
                    [20, 100, 100, 12],
                    [240, 100, 100, 12],
                    [60, 170, 240, 12],
                    [0, 240, 360, 14],
                ],
                [40, 80],
                [330, 220],
                [4, 4, 2, 8, 4, 6, 2],
            );
            l.e2 = Some([320, 80]);
            l.x2 = Some([30, 220]);
            l
        }
    }
}

/// Reads the page-provided level (window.__ARCADE_LEVEL): either a full
/// document or {"builtin": n}. Falls back to builtin 1.
fn page_level() -> LevelDoc {
    #[derive(Deserialize)]
    struct BuiltinRef {
        builtin: usize,
    }
    #[cfg(target_arch = "wasm32")]
    let raw = js_sys::Reflect::get(&js_sys::global(), &"__ARCADE_LEVEL".into())
        .ok()
        .and_then(|v| v.as_string());
    #[cfg(not(target_arch = "wasm32"))]
    let raw: Option<String> = None;
    if let Some(raw) = raw {
        if let Ok(b) = serde_json::from_str::<BuiltinRef>(&raw) {
            return builtin(b.builtin);
        }
        if let Ok(doc) = serde_json::from_str::<LevelDoc>(&raw) {
            return doc;
        }
    }
    builtin(1)
}

// ---- state ----

/// The destructible terrain plus its GPU image.
#[derive(Resource)]
struct Site {
    mask: Vec<bool>,
    image: Handle<Image>,
    dirty: bool,
    /// Cells changed since the last network flush (host only): idx<<1 | set.
    changed: Vec<u32>,
}

impl Site {
    fn solid(&self, x: i32, y: i32) -> bool {
        if x < 0 || x >= GW {
            return true; // walls
        }
        if y < 0 {
            return false;
        }
        if y >= GH {
            return true; // floor of the world
        }
        self.mask[(y * GW + x) as usize]
    }
    fn set(&mut self, x: i32, y: i32, v: bool) {
        if x < 0 || x >= GW || y < 0 || y >= GH {
            return;
        }
        let i = (y * GW + x) as usize;
        if self.mask[i] != v {
            self.mask[i] = v;
            self.dirty = true;
            self.changed.push(((i as u32) << 1) | v as u32);
        }
    }
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum Job {
    Walk,
    Fall { dist: i32, chute: bool },
    Climb,
    Block,
    Build { steps: i32, cooldown: i32 },
    Bash { cooldown: i32 },
    Dig { cooldown: i32 },
    PreQuit { ticks: i32 },
    Splat { ticks: i32 },
    Exiting { ticks: i32 },
}

#[derive(Clone, Copy)]
struct Walker {
    x: i32,
    y: i32, // feet cell
    dir: i32,
    owner: u8,
    job: Job,
    can_climb: bool,
    has_chute: bool,
    alive: bool,
}

#[derive(Resource)]
struct Game {
    doc: LevelDoc,
    players: u8,
    walkers: Vec<Walker>,
    tick: u64,
    acc: f32,
    spawned: [u32; 2],
    saved: [u32; 2],
    dead: [u32; 2],
    skills: [[u32; 7]; 2],
    selected: [usize; 2],
    rate: u32,
    time_left: i32, // ticks
    p2cursor: Vec2, // world coords of the keyboard cursor
    nuke_armed: bool,
    over: bool,
    over_timer: Timer,
    result: String,
    // Networking
    guest_ready: bool, // guest: level received
    net_flush: Timer,
    end_sent: bool,
}

#[derive(Component)]
struct TerrainSprite;

#[derive(Component)]
struct Hud;

#[derive(Component)]
struct SkillBar(u8); // per player

// ---- editor state ----

/// Mailbox: the page asks for the editor instead of a game (no credit).
pub static EDITOR_START: std::sync::Mutex<bool> = std::sync::Mutex::new(false);

#[derive(Resource)]
struct Editor {
    active: bool,
    testing: bool,
    brush: i32,
    doc: LevelDoc,
    mask: Vec<bool>, // authoring surface (compiled to runs on save/test)
}

pub struct InternsPlugin;

impl Plugin for InternsPlugin {
    fn build(&self, app: &mut App) {
        app.add_systems(OnEnter(Phase::Playing), setup)
            .add_systems(
                Update,
                poll_editor_start.run_if(in_state(Phase::Attract).or(in_state(Phase::GameOver))),
            )
            .add_systems(
                Update,
                (
                    net_apply,
                    editor_update,
                    input_p1,
                    input_p2,
                    simulate,
                    host_broadcast,
                    refresh_terrain,
                    draw,
                    hud_update,
                    endgame,
                )
                    .chain()
                    .run_if(in_state(Phase::Playing))
                    .run_if(crate::unpaused),
            );
    }
}

/// The editor is entered like a coin drop, but free: it is a tool.
fn poll_editor_start(mut next: ResMut<NextState<Phase>>, mut net: ResMut<NetMode>, mut cfg: ResMut<CabinetConfig>) {
    let mut flag = EDITOR_START.lock().unwrap();
    if *flag {
        *flag = false;
        net.0 = None;
        cfg.players = 1;
        cfg.humans = 1;
        crate::shell::mark_editor_pending();
        next.set(Phase::Playing);
    }
}

fn setup(
    mut commands: Commands,
    config: Res<CabinetConfig>,
    net: Res<NetMode>,
    mut images: ResMut<Assets<Image>>,
) {
    let editor_mode = crate::shell::take_editor_pending();
    let is_net = net.0.is_some();
    let is_guest = matches!(&net.0, Some(cfg) if !cfg.is_host());

    let doc = if editor_mode || !is_guest {
        page_level()
    } else {
        builtin(1) // guests replace this when the host's level arrives
    };
    let players: u8 = if is_net { 2 } else { config.humans.clamp(1, 2) as u8 };

    let mut mask = vec![false; (GW * GH) as usize];
    build_mask(&doc, &mut mask);

    // Terrain texture: one RGBA8 image, nearest-scaled 2x by the sprite.
    let image = images.add(terrain_image(&mask));
    commands.spawn((
        Sprite { image: image.clone(), custom_size: Some(Vec2::new(720.0, 540.0)), ..default() },
        Transform::from_xyz(0.0, 50.0, 1.0),
        TerrainSprite,
        GameTag,
    ));

    commands.insert_resource(Site { mask: mask.clone(), image, dirty: false, changed: Vec::new() });
    commands.insert_resource(Game {
        players,
        walkers: Vec::new(),
        tick: 0,
        acc: 0.0,
        spawned: [0; 2],
        saved: [0; 2],
        dead: [0; 2],
        skills: [doc.skills, doc.skills],
        selected: [3, 3], // BUILD is the friendliest default
        rate: doc.rate,
        time_left: (doc.time * TICKS_PER_SEC as u32) as i32,
        p2cursor: Vec2::new(100.0, 50.0),
        nuke_armed: false,
        over: false,
        over_timer: Timer::from_seconds(3.0, TimerMode::Once),
        result: String::new(),
        guest_ready: !is_guest,
        net_flush: Timer::from_seconds(0.1, TimerMode::Repeating),
        end_sent: false,
        doc,
    });
    commands.insert_resource(Editor {
        active: editor_mode,
        testing: false,
        brush: 4,
        doc: LevelDoc {
            v: 1,
            name: String::new(),
            rects: vec![[0, 240, 360, 14]],
            holes: vec![],
            runs: vec![],
            e1: [40, 220],
            x1: [320, 230],
            e2: Some([320, 220]),
            x2: Some([40, 230]),
            count: d_count(),
            rate: d_rate(),
            need: d_need(),
            time: d_time(),
            skills: d_skills(),
        },
        mask: {
            let mut m = vec![false; (GW * GH) as usize];
            for x in 0..GW {
                for y in 240..254 {
                    m[(y * GW + x) as usize] = true;
                }
            }
            m
        },
    });
    // Editor boots straight into its authoring surface.
    if editor_mode {
        // handled by editor_update each frame
    }
    // Host sends the level to the guest before anything else.
    if let Some(cfg) = &net.0 {
        if cfg.is_host() {
            if let Ok(msg) = serde_json::to_string(&WireLevel::from_doc(&page_level())) {
                net_send(&msg);
            }
        }
    }

    let hud = text(&mut commands, "", 18.0, WHITE, Vec3::new(0.0, -276.0, 8.0));
    commands.entity(hud).insert((Hud, GameTag));
    let bar1 = text(&mut commands, "", 15.0, GREEN, Vec3::new(0.0, -298.0, 8.0));
    commands.entity(bar1).insert((SkillBar(0), GameTag));
    let bar2 = text(&mut commands, "", 15.0, MAGENTA, Vec3::new(0.0, -316.0, 8.0));
    commands.entity(bar2).insert((SkillBar(1), GameTag));
}

fn build_mask(doc: &LevelDoc, mask: &mut [bool]) {
    mask.fill(false);
    for r in &doc.rects {
        for y in r[1].max(0)..(r[1] + r[3]).min(GH) {
            for x in r[0].max(0)..(r[0] + r[2]).min(GW) {
                mask[(y * GW + x) as usize] = true;
            }
        }
    }
    for run in &doc.runs {
        let (y, x0, len) = (run[0], run[1], run[2]);
        if y < 0 || y >= GH {
            continue;
        }
        for x in x0.max(0)..(x0 + len).min(GW) {
            mask[(y * GW + x) as usize] = true;
        }
    }
    for h in &doc.holes {
        for y in h[1].max(0)..(h[1] + h[3]).min(GH) {
            for x in h[0].max(0)..(h[0] + h[2]).min(GW) {
                mask[(y * GW + x) as usize] = false;
            }
        }
    }
}

fn terrain_image(mask: &[bool]) -> Image {
    use bevy::render::render_asset::RenderAssetUsages;
    use bevy::render::render_resource::{Extent3d, TextureDimension, TextureFormat};
    let mut data = vec![0u8; (GW * GH * 4) as usize];
    paint_mask(mask, &mut data);
    Image::new(
        Extent3d { width: GW as u32, height: GH as u32, depth_or_array_layers: 1 },
        TextureDimension::D2,
        data,
        TextureFormat::Rgba8UnormSrgb,
        RenderAssetUsages::RENDER_WORLD | RenderAssetUsages::MAIN_WORLD,
    )
}

fn paint_mask(mask: &[bool], data: &mut [u8]) {
    for (i, &solid) in mask.iter().enumerate() {
        let o = i * 4;
        if solid {
            // Two-tone dirt, banded by a cheap hash for retro texture.
            let x = (i as i32) % GW;
            let y = (i as i32) / GW;
            let n = ((x * 7 + y * 13) ^ (x * 3)) & 15;
            let (r, g, b) = if n < 12 { (94, 62, 34) } else { (122, 82, 46) };
            data[o] = r;
            data[o + 1] = g;
            data[o + 2] = b;
            data[o + 3] = 255;
        } else {
            data[o + 3] = 0;
        }
    }
}

// ---- wire formats ----

#[derive(Serialize, Deserialize)]
struct WireLevel {
    t: String, // "lv"
    doc: LevelDoc,
}
impl WireLevel {
    fn from_doc(doc: &LevelDoc) -> WireLevel {
        WireLevel { t: "lv".into(), doc: doc.clone() }
    }
}

#[derive(Serialize, Deserialize)]
struct WireAssign {
    t: String, // "as": guest asks host to assign at a spot
    x: i32,
    y: i32,
    skill: usize,
}

#[derive(Serialize, Deserialize)]
struct WireState {
    t: String, // "st"
    w: Vec<[i32; 6]>, // x, y, dir, owner, job code, flags
    saved: [u32; 2],
    dead: [u32; 2],
    spawned: [u32; 2],
    skills: [[u32; 7]; 2],
    tl: i32,
}

#[derive(Serialize, Deserialize)]
struct WireTerrain {
    t: String, // "tr"
    chg: Vec<u32>,
}

#[derive(Serialize, Deserialize)]
struct WireEnd {
    t: String, // "end"
    saved: [u32; 2],
    result: String,
}

fn job_code(j: Job) -> i32 {
    match j {
        Job::Walk => 0,
        Job::Fall { chute: false, .. } => 1,
        Job::Fall { chute: true, .. } => 2,
        Job::Climb => 3,
        Job::Block => 4,
        Job::Build { .. } => 5,
        Job::Bash { .. } => 6,
        Job::Dig { .. } => 7,
        Job::PreQuit { .. } => 8,
        Job::Splat { .. } => 9,
        Job::Exiting { .. } => 10,
    }
}

// ---- the simulation (host and local play) ----

fn is_guest(net: &NetMode) -> bool {
    matches!(&net.0, Some(c) if !c.is_host())
}

#[allow(clippy::too_many_arguments)]
fn simulate(
    time: Res<Time>,
    net: Res<NetMode>,
    editor: Res<Editor>,
    mut game: ResMut<Game>,
    mut site: ResMut<Site>,
) {
    if editor.active || is_guest(&net) || game.over || !game.guest_ready {
        return;
    }
    game.acc += time.delta_secs();
    let step = 1.0 / TICKS_PER_SEC;
    let mut steps = 0;
    while game.acc >= step && steps < 4 {
        game.acc -= step;
        steps += 1;
        game.tick += 1;
        game.time_left -= 1;

        // Spawns: one stream per player.
        let rate = game.rate.max(10) as u64;
        if game.tick % rate == 0 {
            for p in 0..game.players as usize {
                if game.spawned[p] < game.doc.count {
                    let e = if p == 0 { game.doc.e1 } else { game.doc.e2.unwrap_or(game.doc.e1) };
                    game.walkers.push(Walker {
                        x: e[0],
                        y: e[1],
                        dir: 1,
                        owner: p as u8,
                        job: Job::Fall { dist: 0, chute: false },
                        can_climb: false,
                        has_chute: false,
                        alive: true,
                    });
                    game.spawned[p] += 1;
                    sfx("tick");
                }
            }
        }

        step_walkers(&mut game, &mut site);

        // End conditions.
        let active = game.walkers.iter().filter(|w| w.alive).count();
        let all_out = (0..game.players as usize).all(|p| game.spawned[p] >= game.doc.count);
        if game.time_left <= 0 || (all_out && active == 0) {
            finish(&mut game);
        }
    }
}

fn blocker_at(walkers: &[Walker], x: i32, y: i32, skip: usize) -> bool {
    walkers.iter().enumerate().any(|(i, w)| {
        i != skip
            && w.alive
            && matches!(w.job, Job::Block)
            && (w.x - x).abs() <= 3
            && (w.y - y).abs() <= 8
    })
}

fn step_walkers(game: &mut Game, site: &mut Site) {
    let doc_x1 = game.doc.x1;
    let doc_x2 = game.doc.x2.unwrap_or(game.doc.x1);
    let n = game.walkers.len();
    for i in 0..n {
        let mut w = game.walkers[i];
        if !w.alive {
            continue;
        }
        match w.job {
            Job::Splat { ref mut ticks } | Job::Exiting { ref mut ticks } => {
                *ticks -= 1;
                if *ticks <= 0 {
                    if matches!(w.job, Job::Exiting { .. }) {
                        game.saved[w.owner as usize] += 1;
                    } else {
                        game.dead[w.owner as usize] += 1;
                    }
                    w.alive = false;
                }
                game.walkers[i] = w;
                continue;
            }
            Job::PreQuit { ref mut ticks } => {
                *ticks -= 1;
                if *ticks <= 0 {
                    // A loud exit: crater and gone.
                    for dy in -6..=6i32 {
                        for dx in -6..=6i32 {
                            if dx * dx + dy * dy <= 36 {
                                site.set(w.x + dx, w.y - 3 + dy, false);
                            }
                        }
                    }
                    sfx("boom");
                    game.dead[w.owner as usize] += 1;
                    w.alive = false;
                    game.walkers[i] = w;
                    continue;
                }
            }
            _ => {}
        }

        let grounded = site.solid(w.x, w.y + 1);
        match w.job {
            Job::Fall { mut dist, chute } => {
                if grounded {
                    if dist > MAX_FALL && !chute {
                        w.job = Job::Splat { ticks: 20 };
                        sfx("death");
                    } else {
                        w.job = Job::Walk;
                    }
                } else {
                    let speed = if chute { 1 } else { 2 };
                    for _ in 0..speed {
                        if !site.solid(w.x, w.y + 1) {
                            w.y += 1;
                            dist += 1;
                        }
                    }
                    let opened = chute || (w.has_chute && dist >= 8);
                    w.job = Job::Fall { dist, chute: opened };
                }
            }
            Job::Walk => {
                if !grounded {
                    w.job = Job::Fall { dist: 0, chute: false };
                } else if blocker_at(&game.walkers, w.x + w.dir * 2, w.y, i) {
                    w.dir = -w.dir;
                } else {
                    // Try to advance: step up to 4, else climb or turn.
                    let ahead_x = w.x + w.dir;
                    let mut climbed = -1;
                    for up in 0..=4 {
                        if !site.solid(ahead_x, w.y - up) && !site.solid(ahead_x, w.y - up - 3) {
                            climbed = up;
                            break;
                        }
                    }
                    if climbed >= 0 {
                        w.x = ahead_x;
                        w.y -= climbed;
                    } else if w.can_climb {
                        w.job = Job::Climb;
                    } else {
                        w.dir = -w.dir;
                    }
                }
            }
            Job::Climb => {
                let wall_x = w.x + w.dir;
                if !site.solid(wall_x, w.y - 4) {
                    // Ledge: pull up onto it.
                    w.x = wall_x;
                    w.y -= 4;
                    w.job = Job::Walk;
                } else if site.solid(w.x, w.y - 4) {
                    // Overhang knocks the climber off.
                    w.dir = -w.dir;
                    w.job = Job::Fall { dist: 0, chute: false };
                } else {
                    w.y -= 1;
                }
            }
            Job::Block => { /* stands there, radiating authority */ }
            Job::Build { mut steps, mut cooldown } => {
                cooldown -= 1;
                if cooldown <= 0 {
                    if steps <= 0 || site.solid(w.x + w.dir * 3, w.y - 2) {
                        w.job = Job::Walk; // out of planks or bumped a ceiling
                    } else {
                        for k in 0..4 {
                            site.set(w.x + w.dir * (k + 1), w.y + 1 - 1, true);
                            site.set(w.x + w.dir * (k + 1), w.y + 1, true);
                        }
                        w.x += w.dir * 2;
                        w.y -= 1;
                        steps -= 1;
                        w.job = Job::Build { steps, cooldown: 12 };
                        sfx("place");
                    }
                } else {
                    w.job = Job::Build { steps, cooldown };
                }
            }
            Job::Bash { mut cooldown } => {
                cooldown -= 1;
                if cooldown <= 0 {
                    let mut any = false;
                    for k in 1..=5 {
                        for dy in -7..=0i32 {
                            if site.solid(w.x + w.dir * k, w.y + dy) {
                                any = true;
                            }
                            site.set(w.x + w.dir * k, w.y + dy, false);
                        }
                    }
                    if any {
                        w.x += w.dir * 2;
                        w.job = Job::Bash { cooldown: 6 };
                    } else {
                        w.job = Job::Walk;
                    }
                } else if !grounded {
                    w.job = Job::Fall { dist: 0, chute: false };
                } else {
                    w.job = Job::Bash { cooldown };
                }
            }
            Job::Dig { mut cooldown } => {
                cooldown -= 1;
                if cooldown <= 0 {
                    let mut any = false;
                    for dx in -3..=3i32 {
                        for dy in 1..=2i32 {
                            if site.solid(w.x + dx, w.y + dy) {
                                any = true;
                            }
                            site.set(w.x + dx, w.y + dy, false);
                        }
                    }
                    if any {
                        w.y += 2;
                        w.job = Job::Dig { cooldown: 6 };
                    } else {
                        w.job = Job::Fall { dist: 0, chute: false };
                    }
                } else {
                    w.job = Job::Dig { cooldown };
                }
            }
            _ => {}
        }

        // Exit check: near your own door.
        let exit = if w.owner == 0 { doc_x1 } else { doc_x2 };
        if (w.x - exit[0]).abs() <= 4 && (w.y - exit[1]).abs() <= 6 && !matches!(w.job, Job::Exiting { .. } | Job::Splat { .. }) {
            w.job = Job::Exiting { ticks: 15 };
            sfx("eat");
        }
        // The void below the world.
        if w.y >= GH - 1 {
            w.job = Job::Splat { ticks: 1 };
        }
        game.walkers[i] = w;
    }
}

fn finish(game: &mut Game) {
    game.over = true;
    game.over_timer.reset();
    if game.players == 1 {
        let ok = game.saved[0] >= game.doc.need;
        game.result = format!(
            "{}\nSAVED {}/{} (NEED {})",
            if ok { "QUOTA MET" } else { "QUOTA MISSED" },
            game.saved[0],
            game.doc.count,
            game.doc.need
        );
        sfx(if ok { "clear" } else { "death" });
    } else {
        let (a, b) = (game.saved[0], game.saved[1]);
        game.result = format!(
            "P1 SAVED {a}  /  P2 SAVED {b}\n{}",
            if a > b { "P1 TAKES THE FLOOR" } else if b > a { "P2 TAKES THE FLOOR" } else { "A TIE. HR IS FURIOUS." }
        );
        sfx("clear");
    }
}

/// Assigns `skill` to the nearest live walker owned by `player` within reach.
fn try_assign(game: &mut Game, player: usize, gx: i32, gy: i32, skill: usize) -> bool {
    if game.skills[player][skill] == 0 {
        return false;
    }
    let mut best: Option<(usize, i32)> = None;
    for (i, w) in game.walkers.iter().enumerate() {
        if !w.alive || w.owner as usize != player {
            continue;
        }
        if matches!(w.job, Job::Splat { .. } | Job::Exiting { .. } | Job::PreQuit { .. }) {
            continue;
        }
        let d = (w.x - gx).abs() + (w.y - 4 - gy).abs();
        if d <= 10 && best.map(|(_, bd)| d < bd).unwrap_or(true) {
            best = Some((i, d));
        }
    }
    let Some((i, _)) = best else { return false };
    let w = &mut game.walkers[i];
    let applied = match skill {
        0 if !w.can_climb => {
            w.can_climb = true;
            true
        }
        1 if !w.has_chute => {
            w.has_chute = true;
            true
        }
        2 if !matches!(w.job, Job::Block) && matches!(w.job, Job::Walk) => {
            w.job = Job::Block;
            true
        }
        3 if matches!(w.job, Job::Walk) => {
            w.job = Job::Build { steps: 12, cooldown: 1 };
            true
        }
        4 if matches!(w.job, Job::Walk) => {
            w.job = Job::Bash { cooldown: 1 };
            true
        }
        5 if matches!(w.job, Job::Walk) => {
            w.job = Job::Dig { cooldown: 1 };
            true
        }
        6 => {
            w.job = Job::PreQuit { ticks: 60 };
            true
        }
        _ => false,
    };
    if applied {
        game.skills[player][skill] -= 1;
        sfx("place");
    }
    applied
}

// ---- input ----

#[allow(clippy::too_many_arguments)]
fn input_p1(
    buttons: Res<ButtonInput<MouseButton>>,
    keys: Res<ButtonInput<KeyCode>>,
    windows: Query<&Window>,
    cameras: Query<(&Camera, &GlobalTransform)>,
    net: Res<NetMode>,
    editor: Res<Editor>,
    mut game: ResMut<Game>,
) {
    if editor.active || game.over {
        return;
    }
    // My seat: local P1, or my network seat.
    let me: usize = match &net.0 {
        Some(cfg) => cfg.seat as usize,
        None => 0,
    };
    for (i, key) in [
        KeyCode::Digit1,
        KeyCode::Digit2,
        KeyCode::Digit3,
        KeyCode::Digit4,
        KeyCode::Digit5,
        KeyCode::Digit6,
        KeyCode::Digit7,
    ]
    .iter()
    .enumerate()
    {
        if keys.just_pressed(*key) {
            game.selected[me] = i;
            sfx("tick");
        }
    }
    // Release rate: host/local P1 only.
    if net.0.as_ref().map(|c| c.is_host()).unwrap_or(true) {
        if keys.just_pressed(KeyCode::KeyR) {
            game.rate = (game.rate + 10).min(120);
        }
        if keys.just_pressed(KeyCode::KeyT) {
            game.rate = game.rate.saturating_sub(10).max(10);
        }
    }
    // Nuke: N twice.
    if keys.just_pressed(KeyCode::KeyN) {
        if game.nuke_armed {
            if is_guest(&net) {
                // only the host nukes; guests would desync — politely ignore
            } else {
                for w in game.walkers.iter_mut() {
                    if w.alive && !matches!(w.job, Job::PreQuit { .. }) {
                        w.job = Job::PreQuit { ticks: 60 + (w.x % 30) };
                    }
                }
                sfx("saucer");
            }
            game.nuke_armed = false;
        } else {
            game.nuke_armed = true;
        }
    }

    if !buttons.just_pressed(MouseButton::Left) {
        return;
    }
    let Ok(window) = windows.single() else { return };
    let Ok((camera, cam_tf)) = cameras.single() else { return };
    let Some(world) = crate::retro::cursor_world(window, camera, cam_tf) else { return };
    let (gx, gy) = world_to_grid(world);
    if gy < 0 || gy >= GH {
        return;
    }
    let skill = game.selected[me];
    if is_guest(&net) {
        // Ask the host to make the assignment.
        if let Ok(msg) = serde_json::to_string(&WireAssign { t: "as".into(), x: gx, y: gy, skill }) {
            net_send(&msg);
        }
        return;
    }
    try_assign(&mut game, me, gx, gy, skill);
}

/// Local P2: an arrow-keys cursor, Enter assigns, Q/E cycles skills.
fn input_p2(
    keys: Res<ButtonInput<KeyCode>>,
    time: Res<Time>,
    net: Res<NetMode>,
    editor: Res<Editor>,
    mut game: ResMut<Game>,
) {
    if editor.active || game.over || net.0.is_some() || game.players < 2 {
        return;
    }
    let speed = 220.0 * time.delta_secs();
    if keys.pressed(KeyCode::ArrowLeft) {
        game.p2cursor.x -= speed;
    }
    if keys.pressed(KeyCode::ArrowRight) {
        game.p2cursor.x += speed;
    }
    if keys.pressed(KeyCode::ArrowUp) {
        game.p2cursor.y += speed;
    }
    if keys.pressed(KeyCode::ArrowDown) {
        game.p2cursor.y -= speed;
    }
    game.p2cursor.x = game.p2cursor.x.clamp(-360.0, 360.0);
    game.p2cursor.y = game.p2cursor.y.clamp(-220.0, 320.0);
    if keys.just_pressed(KeyCode::KeyQ) {
        game.selected[1] = (game.selected[1] + 6) % 7;
        sfx("tick");
    }
    if keys.just_pressed(KeyCode::KeyE) {
        game.selected[1] = (game.selected[1] + 1) % 7;
        sfx("tick");
    }
    if keys.just_pressed(KeyCode::Enter) {
        let (gx, gy) = world_to_grid(game.p2cursor);
        let skill = game.selected[1];
        try_assign(&mut game, 1, gx, gy, skill);
    }
}

// ---- networking ----

fn host_broadcast(time: Res<Time>, net: Res<NetMode>, mut game: ResMut<Game>, mut site: ResMut<Site>) {
    let Some(cfg) = &net.0 else { return };
    if !cfg.is_host() {
        return;
    }
    if !game.net_flush.tick(time.delta()).just_finished() {
        return;
    }
    if !site.changed.is_empty() {
        // Flush terrain deltas in bounded chunks.
        for chunk in site.changed.chunks(6000) {
            if let Ok(msg) = serde_json::to_string(&WireTerrain { t: "tr".into(), chg: chunk.to_vec() }) {
                net_send(&msg);
            }
        }
        site.changed.clear();
    }
    let w: Vec<[i32; 6]> = game
        .walkers
        .iter()
        .filter(|w| w.alive)
        .map(|w| {
            let flags = w.can_climb as i32 | ((w.has_chute as i32) << 1) | (((w.dir > 0) as i32) << 2);
            [w.x, w.y, flags, w.owner as i32, job_code(w.job), 0]
        })
        .collect();
    let st = WireState {
        t: "st".into(),
        w,
        saved: game.saved,
        dead: game.dead,
        spawned: game.spawned,
        skills: game.skills,
        tl: game.time_left,
    };
    if let Ok(msg) = serde_json::to_string(&st) {
        net_send(&msg);
    }
    if game.over && !game.end_sent {
        game.end_sent = true;
        if let Ok(msg) = serde_json::to_string(&WireEnd { t: "end".into(), saved: game.saved, result: game.result.clone() }) {
            net_send(&msg);
        }
    }
}

#[allow(clippy::too_many_arguments)]
fn net_apply(
    mut events: EventReader<NetIn>,
    net: Res<NetMode>,
    mut game: ResMut<Game>,
    mut site: ResMut<Site>,
) {
    let Some(cfg) = &net.0 else {
        events.clear();
        return;
    };
    let host = cfg.is_host();
    for ev in events.read() {
        if ev.left {
            if !game.over {
                game.over = true;
                game.over_timer.reset();
                game.result = "OPPONENT LEFT\nTHE FLOOR IS YOURS".into();
            }
            continue;
        }
        if ev.seat == cfg.seat {
            continue;
        }
        let Ok(v) = serde_json::from_str::<serde_json::Value>(&ev.data) else { continue };
        match v.get("t").and_then(|t| t.as_str()) {
            Some("as") if host => {
                if let Ok(a) = serde_json::from_str::<WireAssign>(&ev.data) {
                    if a.skill < 7 {
                        try_assign(&mut game, ev.seat as usize, a.x, a.y, a.skill);
                    }
                }
            }
            Some("lv") if !host => {
                if let Ok(wl) = serde_json::from_str::<WireLevel>(&ev.data) {
                    game.doc = wl.doc;
                    game.skills = [game.doc.skills, game.doc.skills];
                    game.time_left = (game.doc.time * TICKS_PER_SEC as u32) as i32;
                    build_mask(&game.doc, &mut site.mask);
                    site.dirty = true;
                    game.guest_ready = true;
                }
            }
            Some("tr") if !host => {
                if let Ok(tr) = serde_json::from_str::<WireTerrain>(&ev.data) {
                    for c in tr.chg {
                        let idx = (c >> 1) as usize;
                        if idx < site.mask.len() {
                            site.mask[idx] = c & 1 == 1;
                        }
                    }
                    site.dirty = true;
                }
            }
            Some("st") if !host => {
                if let Ok(st) = serde_json::from_str::<WireState>(&ev.data) {
                    game.saved = st.saved;
                    game.dead = st.dead;
                    game.spawned = st.spawned;
                    game.skills = st.skills;
                    game.time_left = st.tl;
                    game.walkers = st
                        .w
                        .iter()
                        .map(|a| Walker {
                            x: a[0],
                            y: a[1],
                            dir: if a[2] & 4 != 0 { 1 } else { -1 },
                            owner: a[3] as u8,
                            job: match a[4] {
                                1 => Job::Fall { dist: 0, chute: false },
                                2 => Job::Fall { dist: 0, chute: true },
                                3 => Job::Climb,
                                4 => Job::Block,
                                5 => Job::Build { steps: 1, cooldown: 99 },
                                6 => Job::Bash { cooldown: 99 },
                                7 => Job::Dig { cooldown: 99 },
                                8 => Job::PreQuit { ticks: 99 },
                                9 => Job::Splat { ticks: 99 },
                                10 => Job::Exiting { ticks: 99 },
                                _ => Job::Walk,
                            },
                            can_climb: a[2] & 1 != 0,
                            has_chute: a[2] & 2 != 0,
                            alive: true,
                        })
                        .collect();
                }
            }
            Some("end") if !host => {
                if let Ok(end) = serde_json::from_str::<WireEnd>(&ev.data) {
                    game.saved = end.saved;
                    game.result = end.result;
                    game.over = true;
                    game.over_timer.reset();
                }
            }
            _ => {}
        }
    }
}

// ---- rendering ----

fn refresh_terrain(mut site: ResMut<Site>, mut images: ResMut<Assets<Image>>) {
    if !site.dirty {
        return;
    }
    site.dirty = false;
    if let Some(img) = images.get_mut(&site.image) {
        if let Some(data) = img.data.as_mut() {
            paint_mask(&site.mask, data);
        }
    }
}

fn job_color(job: Job, owner: u8) -> Color {
    let base = if owner == 0 { GREEN } else { MAGENTA };
    match job {
        Job::Block => AMBER,
        Job::Build { .. } => CYAN,
        Job::Bash { .. } | Job::Dig { .. } => WHITE,
        Job::PreQuit { .. } | Job::Splat { .. } => RED,
        _ => base,
    }
}

fn draw(mut gizmos: Gizmos, game: Res<Game>, editor: Res<Editor>, net: Res<NetMode>) {
    if editor.active {
        draw_editor(&mut gizmos, &editor);
        return;
    }
    // Doors: entrance chevrons down, exit arches up.
    let doors = [
        (game.doc.e1, GREEN, true),
        (game.doc.x1, GREEN, false),
    ];
    for (pos, color, is_in) in doors {
        draw_door(&mut gizmos, pos, color, is_in);
    }
    if game.players == 2 {
        if let Some(e2) = game.doc.e2 {
            draw_door(&mut gizmos, e2, MAGENTA, true);
        }
        if let Some(x2) = game.doc.x2 {
            draw_door(&mut gizmos, x2, MAGENTA, false);
        }
    }
    for w in game.walkers.iter().filter(|w| w.alive) {
        let p = grid_to_world(w.x, w.y - 4);
        let color = job_color(w.job, w.owner);
        gizmos.rect_2d(p, Vec2::new(4.0, 12.0), color);
        gizmos.rect_2d(p + Vec2::new(w.dir as f32 * 1.0, 7.0), Vec2::new(4.0, 3.0), color);
        if matches!(w.job, Job::Fall { chute: true, .. }) {
            gizmos.circle_2d(p + Vec2::new(0.0, 12.0), 6.0, CYAN);
        }
        if matches!(w.job, Job::PreQuit { .. }) {
            gizmos.circle_2d(p + Vec2::new(0.0, 12.0), 3.0, RED);
        }
    }
    // P2's keyboard crosshair (local 2P only).
    if game.players == 2 && net.0.is_none() {
        let c = game.p2cursor;
        gizmos.line_2d(c - Vec2::X * 8.0, c + Vec2::X * 8.0, MAGENTA);
        gizmos.line_2d(c - Vec2::Y * 8.0, c + Vec2::Y * 8.0, MAGENTA);
    }
}

fn draw_door(gizmos: &mut Gizmos, pos: [i32; 2], color: Color, entrance: bool) {
    let p = grid_to_world(pos[0], pos[1]);
    gizmos.rect_2d(p, Vec2::new(16.0, 20.0), color);
    if entrance {
        gizmos.line_2d(p + Vec2::new(-4.0, 4.0), p + Vec2::new(0.0, -4.0), color);
        gizmos.line_2d(p + Vec2::new(4.0, 4.0), p + Vec2::new(0.0, -4.0), color);
    } else {
        gizmos.line_2d(p + Vec2::new(-4.0, -4.0), p + Vec2::new(0.0, 4.0), color);
        gizmos.line_2d(p + Vec2::new(4.0, -4.0), p + Vec2::new(0.0, 4.0), color);
    }
}

fn hud_update(
    game: Res<Game>,
    editor: Res<Editor>,
    net: Res<NetMode>,
    mut hud: Query<&mut Text2d, (With<Hud>, Without<SkillBar>)>,
    mut bars: Query<(&SkillBar, &mut Text2d), Without<Hud>>,
) {
    let Ok(mut t) = hud.single_mut() else { return };
    if editor.active {
        let s = format!(
            "EDITOR  LMB PAINT / RMB ERASE  [ ] BRUSH {}  1-4 DOORS\n8/9 POOL {}  C/V COUNT {}  B/M NEED {}  S SAVE  G TEST  X BACK",
            editor.brush, editor.doc.skills[0], editor.doc.count, editor.doc.need
        );
        if t.0 != s {
            t.0 = s;
        }
        return;
    }
    let s = if game.over {
        game.result.clone()
    } else if !game.guest_ready {
        "WAITING FOR THE HOST'S LEVEL...".to_string()
    } else {
        let secs = (game.time_left as f32 / TICKS_PER_SEC).max(0.0) as i32;
        let nuke = if game.nuke_armed { "   N AGAIN: EVERYONE QUITS" } else { "" };
        format!(
            "OUT {}  IN {}/{}  NEED {}   TIME {}:{:02}   RATE {} (R/T){}",
            game.walkers.iter().filter(|w| w.alive).count(),
            game.saved[0] + game.saved[1],
            game.doc.count * game.players as u32,
            game.doc.need,
            secs / 60,
            secs % 60,
            game.rate,
            nuke
        )
    };
    if t.0 != s {
        t.0 = s;
    }
    let me: usize = match &net.0 {
        Some(cfg) => cfg.seat as usize,
        None => 0,
    };
    for (bar, mut bt) in &mut bars {
        let p = bar.0 as usize;
        if p >= game.players as usize {
            if !bt.0.is_empty() {
                bt.0 = String::new();
            }
            continue;
        }
        let marker = |i: usize| if game.selected[p] == i { ">" } else { " " };
        let s: String = (0..7)
            .map(|i| format!("{}{} {} ", marker(i), SKILL_NAMES[i], game.skills[p][i]))
            .collect();
        let who = if net.0.is_some() && p == me { format!("P{} (YOU) ", p + 1) } else { format!("P{} ", p + 1) };
        let s = who + &s;
        if bt.0 != s {
            bt.0 = s;
        }
    }
}

fn endgame(
    time: Res<Time>,
    net: Res<NetMode>,
    editor: Res<Editor>,
    mut game: ResMut<Game>,
    mut final_score: ResMut<FinalScore>,
    mut next: ResMut<NextState<Phase>>,
) {
    if editor.active || !game.over {
        return;
    }
    if !game.over_timer.tick(time.delta()).finished() {
        return;
    }
    let me: usize = match &net.0 {
        Some(cfg) => cfg.seat as usize,
        None => 0,
    };
    let mine = game.saved[me.min(1)];
    let mut score = mine * 100;
    if game.players == 1 {
        if mine >= game.doc.need {
            score += 500 + (game.time_left.max(0) as u32 / 30) * 2;
        }
    } else {
        let other = game.saved[1 - me.min(1)];
        if mine > other {
            score += 300;
        }
    }
    final_score.0 = score;
    next.set(Phase::GameOver);
}

// ---- the level editor ----

#[allow(clippy::too_many_arguments)]
fn editor_update(
    buttons: Res<ButtonInput<MouseButton>>,
    keys: Res<ButtonInput<KeyCode>>,
    windows: Query<&Window>,
    cameras: Query<(&Camera, &GlobalTransform)>,
    mut editor: ResMut<Editor>,
    mut game: ResMut<Game>,
    mut site: ResMut<Site>,
) {
    if !editor.active {
        return;
    }
    // Test-play round-trip: G compiles the doc and plays it; X returns.
    if editor.testing {
        if keys.just_pressed(KeyCode::KeyX) {
            editor.testing = false;
            editor.active = true;
            site.mask.copy_from_slice(&editor.mask);
            site.dirty = true;
            game.over = false;
            game.walkers.clear();
            game.spawned = [0; 2];
            game.saved = [0; 2];
            game.dead = [0; 2];
        }
        return;
    }
    // Keep the site showing the authoring mask.
    if keys.just_pressed(KeyCode::KeyG) {
        // Compile and play (1P sandbox).
        let doc = compile_doc(&editor);
        game.doc = doc;
        game.players = 1;
        game.walkers.clear();
        game.spawned = [0; 2];
        game.saved = [0; 2];
        game.dead = [0; 2];
        game.skills = [game.doc.skills, game.doc.skills];
        game.time_left = (game.doc.time * TICKS_PER_SEC as u32) as i32;
        game.over = false;
        game.guest_ready = true;
        build_mask(&game.doc, &mut site.mask);
        site.dirty = true;
        editor.testing = true;
        editor.active = false; // sim + inputs take over; X returns
        sfx("coin");
        return;
    }
    if keys.just_pressed(KeyCode::KeyS) {
        let doc = compile_doc(&editor);
        if let Ok(json) = serde_json::to_string(&doc) {
            crate::shell::save_level(&json);
            sfx("clear");
        }
    }
    if keys.just_pressed(KeyCode::BracketLeft) {
        editor.brush = (editor.brush - 2).max(2);
    }
    if keys.just_pressed(KeyCode::BracketRight) {
        editor.brush = (editor.brush + 2).min(16);
    }
    if keys.just_pressed(KeyCode::KeyC) {
        editor.doc.count = editor.doc.count.saturating_sub(5).max(5);
    }
    if keys.just_pressed(KeyCode::KeyV) {
        editor.doc.count = (editor.doc.count + 5).min(80);
    }
    if keys.just_pressed(KeyCode::KeyB) {
        editor.doc.need = editor.doc.need.saturating_sub(5);
    }
    if keys.just_pressed(KeyCode::KeyM) {
        editor.doc.need = (editor.doc.need + 5).min(editor.doc.count);
    }
    if keys.just_pressed(KeyCode::Digit8) {
        let v = editor.doc.skills[0].saturating_sub(2);
        editor.doc.skills = [v; 7];
    }
    if keys.just_pressed(KeyCode::Digit9) {
        let v = (editor.doc.skills[0] + 2).min(50);
        editor.doc.skills = [v; 7];
    }

    let Ok(window) = windows.single() else { return };
    let Ok((camera, cam_tf)) = cameras.single() else { return };
    let Some(world) = crate::retro::cursor_world(window, camera, cam_tf) else { return };
    let (gx, gy) = world_to_grid(world);
    if gy < 0 || gy >= GH || gx < 0 || gx >= GW {
        return;
    }
    // Door placement.
    for (key, which) in [
        (KeyCode::Digit1, 0),
        (KeyCode::Digit2, 1),
        (KeyCode::Digit3, 2),
        (KeyCode::Digit4, 3),
    ] {
        if keys.just_pressed(key) {
            match which {
                0 => editor.doc.e1 = [gx, gy],
                1 => editor.doc.x1 = [gx, gy],
                2 => editor.doc.e2 = Some([gx, gy]),
                _ => editor.doc.x2 = Some([gx, gy]),
            }
            sfx("place");
        }
    }
    // Painting.
    let paint = buttons.pressed(MouseButton::Left);
    let erase = buttons.pressed(MouseButton::Right);
    if paint || erase {
        let b = editor.brush;
        for dy in -b..=b {
            for dx in -b..=b {
                if dx * dx + dy * dy > b * b {
                    continue;
                }
                let (x, y) = (gx + dx, gy + dy);
                if x < 0 || x >= GW || y < 0 || y >= GH {
                    continue;
                }
                let i = (y * GW + x) as usize;
                let v = paint && !erase;
                if editor.mask[i] != v {
                    editor.mask[i] = v;
                    site.mask[i] = v;
                    site.dirty = true;
                }
            }
        }
    }
}

/// Compiles the authoring mask + settings into a shareable level document.
fn compile_doc(editor: &Editor) -> LevelDoc {
    let mut doc = editor.doc.clone();
    doc.rects.clear();
    doc.holes.clear();
    doc.runs.clear();
    for y in 0..GH {
        let mut x = 0;
        while x < GW {
            if editor.mask[(y * GW + x) as usize] {
                let x0 = x;
                while x < GW && editor.mask[(y * GW + x) as usize] {
                    x += 1;
                }
                doc.runs.push([y, x0, x - x0]);
            } else {
                x += 1;
            }
        }
    }
    doc
}

fn draw_editor(gizmos: &mut Gizmos, editor: &Editor) {
    draw_door(gizmos, editor.doc.e1, GREEN, true);
    draw_door(gizmos, editor.doc.x1, GREEN, false);
    if let Some(e2) = editor.doc.e2 {
        draw_door(gizmos, e2, MAGENTA, true);
    }
    if let Some(x2) = editor.doc.x2 {
        draw_door(gizmos, x2, MAGENTA, false);
    }
}
