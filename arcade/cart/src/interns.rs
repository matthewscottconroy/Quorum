//! INTERNS — a walkers-and-jobs cabinet. A stream of clueless new hires
//! marches out of the door; you hand out seven jobs (climb, parachute,
//! supervise, build, bash, dig, quit-loudly) to steer enough of them to the
//! exit. Original name, art, and levels; the genre's mechanics only.
//!
//! Modes: 1P, 2P local (P1 mouse + P2 keyboard cursor), 2P online (host
//! authoritative — guests send assignments, the host streams id-stable
//! walker snapshots and terrain deltas), plus a LEVEL EDITOR that loads any
//! level as a template and saves to the community shelf.
//!
//! Levels are 360-1080 cells wide; a fixed camera window scrolls across
//! them (A/D, mouse at the play-area edge, or P2 shoving the crosshair).
//! The minimap up top shows walkers, doors, and the camera window.

use std::collections::HashMap;

use bevy::prelude::*;
use serde::{Deserialize, Serialize};

use crate::retro::{text, AMBER, CYAN, GREEN, MAGENTA, RED, WHITE};
use crate::shell::{net_send, sfx, stat};
use crate::{CabinetConfig, FinalScore, GameTag, NetIn, NetMode, Phase};

pub const BLURB: &[&str] = &[
    "THE NEW HIRES WALK. THAT'S ALL THEY KNOW.",
    "ASSIGN JOBS. SAVE THE QUOTA. BEAT THE CLOCK.",
    "1P / 2P LOCAL / 2P ONLINE / LEVEL EDITOR",
];

// Terrain: rows are fixed at 270 cells (540 px); width varies per level.
pub const GH: i32 = 270;
const MIN_W: i32 = 360;
const MAX_W: i32 = 1080;
const CELL: f32 = 2.0;
const VIEW_W: f32 = 720.0; // the camera window, in px
const TICKS_PER_SEC: f32 = 30.0;
const MAX_FALL: i32 = 32; // survivable fall, in cells, without a chute
const MAX_COUNT: u32 = 100; // walkers per player: sim comfort + sane rounds
const SKILL_NAMES: [&str; 7] = ["CLIMB", "CHUTE", "SUPER", "BUILD", "BASH", "DIG", "QUIT"];

/// The scrolling window: a pixel offset into the level, camera itself fixed.
#[derive(Resource, Default)]
struct Cam {
    x: f32,
    max: f32,
}

fn grid_to_world(cam: &Cam, x: i32, y: i32) -> Vec2 {
    Vec2::new(
        -360.0 + (x as f32 + 0.5) * CELL - cam.x,
        320.0 - (y as f32 + 0.5) * CELL,
    )
}

fn world_to_grid(cam: &Cam, w: Vec2) -> (i32, i32) {
    (((w.x + 360.0 + cam.x) / CELL) as i32, ((320.0 - w.y) / CELL) as i32)
}

// ---- level documents ----

fn d_count() -> u32 { 30 }
fn d_rate() -> u32 { 45 }
fn d_need() -> u32 { 15 }
fn d_time() -> u32 { 300 }
fn d_skills() -> [u32; 7] { [5, 5, 5, 10, 5, 5, 5] }
fn d_width() -> i32 { MIN_W }

#[derive(Serialize, Deserialize, Clone)]
pub struct LevelDoc {
    pub v: u32,
    #[serde(default)]
    pub name: String,
    #[serde(default = "d_width")]
    pub w: i32,
    #[serde(default)]
    pub rects: Vec<[i32; 4]>,
    #[serde(default)]
    pub holes: Vec<[i32; 4]>,
    #[serde(default)]
    pub runs: Vec<[i32; 3]>, // [y, x0, len]
    pub e1: [i32; 2],
    pub x1: [i32; 2],
    #[serde(default)]
    pub e2: Option<[i32; 2]>,
    #[serde(default)]
    pub x2: Option<[i32; 2]>,
    #[serde(default = "d_count")]
    pub count: u32,
    #[serde(default = "d_rate")]
    pub rate: u32,
    #[serde(default = "d_need")]
    pub need: u32,
    #[serde(default = "d_time")]
    pub time: u32,
    #[serde(default = "d_skills")]
    pub skills: [u32; 7],
}

/// Community levels are opaque JSON to the server, so the cartridge is the
/// last line of defense: clamp everything a hand-written document could
/// weaponize (out-of-bounds doors, absurd head-counts, zero rates).
fn sanitize_doc(doc: &mut LevelDoc) {
    doc.w = doc.w.clamp(MIN_W, MAX_W);
    // Geometry caps: a hand-written document with millions of entries must
    // not stall build_mask (or get relayed verbatim to a guest).
    doc.rects.truncate(10_000);
    doc.holes.truncate(10_000);
    doc.runs.truncate(30_000);
    doc.count = doc.count.clamp(1, MAX_COUNT);
    doc.rate = doc.rate.clamp(10, 120);
    doc.need = doc.need.clamp(1, doc.count);
    doc.time = doc.time.clamp(60, 900);
    for s in doc.skills.iter_mut() {
        *s = (*s).min(99);
    }
    let clamp_door = |d: &mut [i32; 2], w: i32| {
        d[0] = d[0].clamp(6, w - 7);
        d[1] = d[1].clamp(6, GH - 12);
    };
    clamp_door(&mut doc.e1, doc.w);
    clamp_door(&mut doc.x1, doc.w);
    if let Some(e2) = doc.e2.as_mut() {
        clamp_door(e2, doc.w);
    }
    if let Some(x2) = doc.x2.as_mut() {
        clamp_door(x2, doc.w);
    }
}

/// Four house levels; TWO TOWERS is double-wide to show off the camera.
pub fn builtin(n: usize) -> LevelDoc {
    let base = |w: i32, rects: Vec<[i32; 4]>, e1: [i32; 2], x1: [i32; 2], skills: [u32; 7]| LevelDoc {
        v: 1,
        name: String::new(),
        w,
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
    let mut l = match n {
        1 => {
            let mut l = base(
                360,
                vec![[10, 200, 150, 12], [210, 200, 140, 12], [0, 260, 360, 10]],
                [30, 180],
                [320, 190],
                [2, 2, 2, 12, 2, 2, 2],
            );
            l.e2 = Some([330, 180]);
            l.x2 = Some([40, 190]);
            l
        }
        2 => {
            let mut l = base(
                360,
                vec![[40, 90, 280, 14], [80, 150, 280, 14], [40, 210, 280, 14], [0, 260, 360, 10]],
                [60, 70],
                [300, 240],
                [2, 4, 2, 4, 2, 14, 2],
            );
            l.e2 = Some([300, 70]);
            l.x2 = Some([60, 240]);
            l
        }
        3 => {
            let mut l = base(
                360,
                vec![
                    [0, 210, 360, 14],
                    [90, 150, 12, 60],
                    [180, 150, 12, 60],
                    [270, 150, 12, 60],
                    [0, 120, 360, 8],
                ],
                [30, 190],
                [330, 190],
                [2, 2, 4, 4, 14, 2, 4],
            );
            l.e2 = Some([330, 100]);
            l.x2 = Some([30, 100]);
            l
        }
        // TWO TOWERS, wide edition: a 720-cell march with a valley between.
        _ => {
            let mut l = base(
                720,
                vec![
                    [30, 100, 140, 12],
                    [550, 100, 140, 12],
                    [100, 170, 200, 12],
                    [420, 170, 200, 12],
                    [0, 240, 340, 14],
                    [380, 240, 340, 14],
                    [340, 262, 40, 8], // a thin bridge over the void
                ],
                [60, 80],
                [660, 220],
                [4, 4, 2, 10, 4, 6, 2],
            );
            l.e2 = Some([660, 80]);
            l.x2 = Some([60, 220]);
            l
        }
    };
    sanitize_doc(&mut l);
    l
}

/// Reads the page-provided level: a full document, {"builtin": n}, or the
/// editor's {"blank": true} template marker (None = blank slab).
fn page_level() -> Option<LevelDoc> {
    #[derive(Deserialize)]
    struct BuiltinRef {
        builtin: usize,
    }
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
        if let Ok(b) = serde_json::from_str::<BlankRef>(&raw) {
            if b.blank {
                return None;
            }
        }
        if let Ok(b) = serde_json::from_str::<BuiltinRef>(&raw) {
            return Some(builtin(b.builtin));
        }
        if let Ok(mut doc) = serde_json::from_str::<LevelDoc>(&raw) {
            sanitize_doc(&mut doc);
            return Some(doc);
        }
    }
    Some(builtin(1))
}

// ---- terrain ----

#[derive(Resource)]
struct Site {
    w: i32,
    mask: Vec<bool>,
    image: Handle<Image>,
    /// Dirty rectangles awaiting a partial CPU repaint. A short LIST, not a
    /// single union: two diggers at opposite ends of a 1080-cell floor must
    /// not degrade into a full-texture repaint every frame.
    dirty: Vec<(i32, i32, i32, i32)>,
    /// Record per-cell deltas for the network? Host-only, else the buffer
    /// would grow without bound in local play.
    record: bool,
    changed: Vec<u32>,
}

const DIRTY_RECTS_MAX: usize = 4;
const DIRTY_MERGE_NEAR: i32 = 48;

impl Site {
    fn solid(&self, x: i32, y: i32) -> bool {
        if x < 0 || x >= self.w {
            return true;
        }
        if y < 0 {
            return false;
        }
        if y >= GH {
            return true;
        }
        self.mask[(y * self.w + x) as usize]
    }
    fn set(&mut self, x: i32, y: i32, v: bool) {
        if x < 0 || x >= self.w || y < 0 || y >= GH {
            return;
        }
        let i = (y * self.w + x) as usize;
        if self.mask[i] != v {
            self.mask[i] = v;
            self.grow_dirty(x, y);
            if self.record {
                self.changed.push(((i as u32) << 1) | v as u32);
            }
        }
    }
    fn grow_dirty(&mut self, x: i32, y: i32) {
        // Join a nearby rect if one exists…
        for r in self.dirty.iter_mut() {
            if x >= r.0 - DIRTY_MERGE_NEAR
                && x <= r.2 + DIRTY_MERGE_NEAR
                && y >= r.1 - DIRTY_MERGE_NEAR
                && y <= r.3 + DIRTY_MERGE_NEAR
            {
                r.0 = r.0.min(x);
                r.1 = r.1.min(y);
                r.2 = r.2.max(x);
                r.3 = r.3.max(y);
                return;
            }
        }
        // …otherwise start a new one, or as a last resort widen the nearest.
        if self.dirty.len() < DIRTY_RECTS_MAX {
            self.dirty.push((x, y, x, y));
            return;
        }
        let nearest = self
            .dirty
            .iter_mut()
            .min_by_key(|r| ((r.0 + r.2) / 2 - x).abs() + ((r.1 + r.3) / 2 - y).abs())
            .expect("dirty is non-empty here");
        nearest.0 = nearest.0.min(x);
        nearest.1 = nearest.1.min(y);
        nearest.2 = nearest.2.max(x);
        nearest.3 = nearest.3.max(y);
    }
    fn all_dirty(&mut self) {
        self.dirty.clear();
        self.dirty.push((0, 0, self.w - 1, GH - 1));
    }
}

fn build_mask(doc: &LevelDoc, w: i32, mask: &mut Vec<bool>) {
    mask.clear();
    mask.resize((w * GH) as usize, false);
    for r in &doc.rects {
        for y in r[1].max(0)..(r[1] + r[3]).min(GH) {
            for x in r[0].max(0)..(r[0] + r[2]).min(w) {
                mask[(y * w + x) as usize] = true;
            }
        }
    }
    for run in &doc.runs {
        let (y, x0, len) = (run[0], run[1], run[2]);
        if y < 0 || y >= GH {
            continue;
        }
        for x in x0.max(0)..(x0 + len).min(w) {
            mask[(y * w + x) as usize] = true;
        }
    }
    for h in &doc.holes {
        for y in h[1].max(0)..(h[1] + h[3]).min(GH) {
            for x in h[0].max(0)..(h[0] + h[2]).min(w) {
                mask[(y * w + x) as usize] = false;
            }
        }
    }
}

fn terrain_image(w: i32, mask: &[bool]) -> Image {
    use bevy::render::render_asset::RenderAssetUsages;
    use bevy::render::render_resource::{Extent3d, TextureDimension, TextureFormat};
    let mut data = vec![0u8; (w * GH * 4) as usize];
    paint_region(w, mask, &mut data, 0, 0, w - 1, GH - 1);
    Image::new(
        Extent3d { width: w as u32, height: GH as u32, depth_or_array_layers: 1 },
        TextureDimension::D2,
        data,
        TextureFormat::Rgba8UnormSrgb,
        RenderAssetUsages::RENDER_WORLD | RenderAssetUsages::MAIN_WORLD,
    )
}

/// Repaints only the dirty rectangle — the optimization that makes wide
/// levels cheap: a digger's frame touches ~100 cells, not w×270.
fn paint_region(w: i32, mask: &[bool], data: &mut [u8], x0: i32, y0: i32, x1: i32, y1: i32) {
    for y in y0.max(0)..=y1.min(GH - 1) {
        for x in x0.max(0)..=x1.min(w - 1) {
            let i = (y * w + x) as usize;
            let o = i * 4;
            if mask[i] {
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
}

// ---- walkers ----

#[derive(Clone, Copy, PartialEq, Eq)]
enum Job {
    Walk,
    Fall { dist: i32, chute: bool },
    Climb,
    Block,
    Build { steps: i32, cooldown: i32 },
    Bash { cooldown: i32 },
    Dig { cooldown: i32 },
    Splat { ticks: i32 },
    Exiting { ticks: i32 },
}

#[derive(Clone, Copy)]
struct Walker {
    id: u32,
    x: i32,
    y: i32,
    dir: i32,
    owner: u8,
    job: Job,
    can_climb: bool,
    has_chute: bool,
    /// The loud-quit countdown runs ALONGSIDE the current job — quitters
    /// keep walking, falling, even blocking, right up to the bang.
    quit: i32, // -1 = not armed
    alive: bool,
}

#[derive(Resource)]
struct Game {
    doc: LevelDoc,
    players: u8,
    walkers: Vec<Walker>,
    next_id: u32,
    tick: u64,
    acc: f32,
    spawned: [u32; 2],
    saved: [u32; 2],
    dead: [u32; 2],
    skills: [[u32; 7]; 2],
    selected: [usize; 2],
    /// Per-player spawn tempo: your T/R only throttles YOUR stream.
    rate: [u32; 2],
    time_left: i32,
    /// P2's crosshair in WORLD coordinates (x: 0..w*CELL px, y: screen) so
    /// camera scrolling never drags their aim off a walker mid-assignment.
    p2cursor: Vec2,
    nuke_armed: bool,
    over: bool,
    over_timer: Timer,
    result: String,
    hover: [Option<u32>; 2], // walker ids under each player's cursor
    /// Fall/floor deaths per player, for the service record only.
    splats: [u32; 2],
    // Networking
    guest_ready: bool,
    net_flush: Timer,
    end_sent: bool,
    /// Guest: seconds spent waiting for the host's level (timeout notice),
    /// and the multi-part level transfer in flight.
    lv_wait: f32,
    lv_doc: Option<LevelDoc>,
    lv_parts: u32,
    lv_runs: Vec<[i32; 3]>,
    // Guest interpolation: id → grid position, previous and current snapshot.
    guest_prev: HashMap<u32, (f32, f32)>,
    guest_curr: HashMap<u32, (f32, f32)>,
    guest_t: f32,
    guest_dt: f32,
}

#[derive(Component)]
struct TerrainSprite;

#[derive(Component)]
struct Hud;

#[derive(Component)]
struct HudLine2;

#[derive(Component)]
struct SkillBar(u8);

// ---- editor ----

const EDITOR_WIDTHS: [i32; 3] = [360, 720, 1080];
const UNDO_CAP: usize = 20;

#[derive(Resource)]
struct Editor {
    active: bool,
    testing: bool,
    brush: i32,
    doc: LevelDoc,
    w: i32,
    mask: Vec<bool>,
    undo: Vec<Vec<bool>>,
    stroke_open: bool,
    mirror: bool,
    skill_sel: usize,
    last_click: Option<(i32, i32)>,
    /// Validation notice ("ENTRANCE 1 BURIED") shown on the HUD for a beat.
    warning: Option<(String, Timer)>,
}

impl Editor {
    fn blank() -> Editor {
        let w = MIN_W;
        let mut mask = vec![false; (w * GH) as usize];
        for x in 0..w {
            for y in 240..254 {
                mask[(y * w + x) as usize] = true;
            }
        }
        Editor {
            active: false,
            testing: false,
            brush: 4,
            doc: LevelDoc {
                v: 1,
                name: String::new(),
                w,
                rects: vec![],
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
            w,
            mask,
            undo: Vec::new(),
            stroke_open: false,
            mirror: false,
            skill_sel: 3,
            last_click: None,
            warning: None,
        }
    }

    /// Loads any level document as an editing template.
    fn from_doc(doc: LevelDoc) -> Editor {
        let mut e = Editor::blank();
        e.w = doc.w;
        build_mask(&doc, doc.w, &mut e.mask);
        e.doc = doc;
        e.doc.rects.clear();
        e.doc.holes.clear();
        e.doc.runs.clear();
        e
    }
}

pub struct InternsPlugin;

impl Plugin for InternsPlugin {
    fn build(&self, app: &mut App) {
        app.init_resource::<Cam>()
            .add_systems(OnEnter(Phase::Playing), setup)
            .add_systems(
                Update,
                poll_editor_start.run_if(in_state(Phase::Attract).or(in_state(Phase::GameOver))),
            )
            // Inputs live OUTSIDE the pause gate: surveying the field and
            // queuing assignments while paused is a genre birthright.
            .add_systems(
                Update,
                (input_p1, input_p2, camera_scroll).run_if(in_state(Phase::Playing)),
            )
            .add_systems(
                Update,
                (
                    net_apply,
                    editor_update,
                    simulate,
                    host_broadcast,
                    refresh_terrain,
                    position_terrain,
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

fn poll_editor_start(
    mut next: ResMut<NextState<Phase>>,
    mut net: ResMut<NetMode>,
    mut cfg: ResMut<CabinetConfig>,
) {
    if crate::shell::take_editor_start() {
        net.0 = None;
        cfg.players = 1;
        cfg.humans = 1;
        crate::shell::mark_editor_pending();
        next.set(Phase::Playing);
    }
}

#[allow(clippy::too_many_arguments)]
fn setup(
    mut commands: Commands,
    config: Res<CabinetConfig>,
    net: Res<NetMode>,
    mut cam: ResMut<Cam>,
    mut images: ResMut<Assets<Image>>,
    existing_editor: Option<ResMut<Editor>>,
) {
    let editor_mode = crate::shell::take_editor_pending();
    let is_net = net.0.is_some();
    let is_guest = matches!(&net.0, Some(cfg) if !cfg.is_host());

    // The editor survives rounds: re-entering reuses the canvas; a test-play
    // round that runs to completion must NOT cost anyone their work.
    let editor = match (existing_editor, editor_mode) {
        (Some(mut e), true) => {
            // Fresh editor entry: load the page's selected template, or keep
            // the previous canvas when the page says blank and one exists.
            if let Some(doc) = page_level() {
                *e = Editor::from_doc(doc);
            }
            e.active = true;
            e.testing = false;
            None // resource already lives
        }
        (Some(mut e), false) => {
            e.active = false;
            e.testing = false;
            None
        }
        (None, true) => {
            let mut e = match page_level() {
                Some(doc) => Editor::from_doc(doc),
                None => Editor::blank(),
            };
            e.active = true;
            Some(e)
        }
        (None, false) => Some(Editor::blank()),
    };
    let editor_active = editor_mode;
    if let Some(e) = editor {
        commands.insert_resource(e);
    }

    let doc = if editor_active {
        // The site shows the editor canvas; game doc is irrelevant until G.
        page_level().unwrap_or_else(|| builtin(1))
    } else if is_guest {
        builtin(1) // replaced by the host's level
    } else {
        page_level().unwrap_or_else(|| builtin(1))
    };
    let players: u8 = if is_net { 2 } else { config.humans.clamp(1, 2) as u8 };

    let w = doc.w;
    let mut mask = vec![false; (w * GH) as usize];
    build_mask(&doc, w, &mut mask);

    let image = images.add(terrain_image(w, &mask));
    commands.spawn((
        Sprite { image: image.clone(), custom_size: Some(Vec2::new(w as f32 * CELL, 540.0)), ..default() },
        Transform::from_xyz(0.0, 50.0, 1.0),
        TerrainSprite,
        GameTag,
    ));

    cam.x = 0.0;
    cam.max = (w as f32 * CELL - VIEW_W).max(0.0);

    commands.insert_resource(Site {
        w,
        mask,
        image,
        dirty: Vec::new(),
        record: is_net && !is_guest,
        changed: Vec::new(),
    });
    commands.insert_resource(Game {
        players,
        walkers: Vec::new(),
        next_id: 1,
        tick: 0,
        acc: 0.0,
        spawned: [0; 2],
        saved: [0; 2],
        dead: [0; 2],
        skills: [doc.skills, doc.skills],
        selected: [3, 3],
        rate: [doc.rate; 2],
        time_left: (doc.time * TICKS_PER_SEC as u32) as i32,
        p2cursor: Vec2::new(460.0, 50.0),
        nuke_armed: false,
        over: false,
        splats: [0; 2],
        over_timer: Timer::from_seconds(3.0, TimerMode::Once),
        result: String::new(),
        hover: [None; 2],
        guest_ready: !is_guest,
        net_flush: Timer::from_seconds(0.1, TimerMode::Repeating),
        end_sent: false,
        lv_wait: 0.0,
        lv_doc: None,
        lv_parts: 0,
        lv_runs: Vec::new(),
        guest_prev: HashMap::new(),
        guest_curr: HashMap::new(),
        guest_t: 0.0,
        guest_dt: 0.1,
        doc,
    });

    if let Some(cfg) = &net.0 {
        if cfg.is_host() {
            if let Some(doc) = page_level() {
                send_level(doc);
            }
        }
    }

    let hud = text(&mut commands, "", 18.0, WHITE, Vec3::new(0.0, -276.0, 8.0));
    commands.entity(hud).insert((Hud, GameTag));
    let hud2 = text(&mut commands, "", 14.0, AMBER, Vec3::new(0.0, -258.0, 8.0));
    commands.entity(hud2).insert((HudLine2, GameTag));
    let bar1 = text(&mut commands, "", 15.0, GREEN, Vec3::new(0.0, -298.0, 8.0));
    commands.entity(bar1).insert((SkillBar(0), GameTag));
    let bar2 = text(&mut commands, "", 15.0, MAGENTA, Vec3::new(0.0, -316.0, 8.0));
    commands.entity(bar2).insert((SkillBar(1), GameTag));
}

// ---- wire formats ----

#[derive(Serialize, Deserialize)]
struct WireLevel {
    t: String,
    doc: LevelDoc,
    /// How many "lvr" run-batches follow (0 = the doc is complete as sent).
    #[serde(default)]
    parts: u32,
}

/// One batch of terrain runs for a level too painty to fit one websocket
/// frame — a heavily drawn 1080-cell canvas can exceed the 56 KB cap as one
/// document, which used to strand the guest on the waiting screen forever.
#[derive(Serialize, Deserialize)]
struct WireRuns {
    t: String, // "lvr"
    i: u32,
    runs: Vec<[i32; 3]>,
}

const LV_RUNS_PER_PART: usize = 2500;

/// Sends the level to the guest, splitting the run list into parts that
/// each fit comfortably under the websocket message cap.
fn send_level(mut doc: LevelDoc) {
    let runs = std::mem::take(&mut doc.runs);
    if runs.len() <= LV_RUNS_PER_PART {
        doc.runs = runs;
        if let Ok(msg) = serde_json::to_string(&WireLevel { t: "lv".into(), doc, parts: 0 }) {
            net_send(&msg);
        }
        return;
    }
    let parts = runs.chunks(LV_RUNS_PER_PART).count() as u32;
    if let Ok(msg) = serde_json::to_string(&WireLevel { t: "lv".into(), doc, parts }) {
        net_send(&msg);
    }
    for (i, chunk) in runs.chunks(LV_RUNS_PER_PART).enumerate() {
        if let Ok(msg) =
            serde_json::to_string(&WireRuns { t: "lvr".into(), i: i as u32, runs: chunk.to_vec() })
        {
            net_send(&msg);
        }
    }
}

#[derive(Serialize, Deserialize)]
struct WireAssign {
    t: String, // "as"
    x: i32,
    y: i32,
    skill: usize,
}

/// Guest → host: throttle MY spawn stream (rate is per player now).
#[derive(Serialize, Deserialize)]
struct WireRate {
    t: String, // "rt"
    v: u32,
}

/// Guest → host: MY crew quits (owner-scoped nuke).
#[derive(Serialize, Deserialize)]
struct WireNuke {
    t: String, // "nk"
}

/// Arms the loud-quit countdown for every walker `player` owns. Nukes are
/// personal: nobody detonates the other player's workforce.
fn nuke_own(game: &mut Game, player: u8) {
    for w in game.walkers.iter_mut() {
        if w.alive && w.owner == player && w.quit < 0 {
            w.quit = 60 + (w.x % 30);
        }
    }
    sfx("saucer");
}

#[derive(Serialize, Deserialize)]
struct WireState {
    t: String, // "st"
    w: Vec<[i32; 6]>, // id, x, y, flags, owner, job
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
        Job::Splat { .. } => 9,
        Job::Exiting { .. } => 10,
    }
}

fn job_name(j: Job, quit: i32) -> &'static str {
    if quit >= 0 {
        return "QUITTING";
    }
    match j {
        Job::Walk => "WALKING",
        Job::Fall { chute: true, .. } => "FLOATING",
        Job::Fall { .. } => "FALLING",
        Job::Climb => "CLIMBING",
        Job::Block => "SUPERVISING",
        Job::Build { .. } => "BUILDING",
        Job::Bash { .. } => "BASHING",
        Job::Dig { .. } => "DIGGING",
        Job::Splat { .. } => "DOWN",
        Job::Exiting { .. } => "CLOCKING OUT",
    }
}

// ---- simulation ----

fn is_guest(net: &NetMode) -> bool {
    matches!(&net.0, Some(c) if !c.is_host())
}

fn simulate(
    time: Res<Time>,
    keys: Res<ButtonInput<KeyCode>>,
    net: Res<NetMode>,
    editor: Res<Editor>,
    mut game: ResMut<Game>,
    mut site: ResMut<Site>,
) {
    if editor.active || is_guest(&net) || game.over || !game.guest_ready {
        return;
    }
    game.acc += time.delta_secs();
    // Fast-forward (local rounds): hold F to run the fixed step at triple
    // speed — the last stragglers' march is dead time nobody signed up for.
    // Determinism is untouched; it's the same step, taken more often.
    let mut step_cap = 4;
    if net.0.is_none() && keys.pressed(KeyCode::KeyF) {
        game.acc += 2.0 * time.delta_secs();
        step_cap = 12;
    }
    let step = 1.0 / TICKS_PER_SEC;
    let mut steps = 0;
    while game.acc >= step && steps < step_cap {
        game.acc -= step;
        steps += 1;
        game.tick += 1;
        game.time_left -= 1;

        for p in 0..game.players as usize {
            let rate = game.rate[p].max(10) as u64;
            if game.tick % rate == 0 {
                if game.spawned[p] < game.doc.count {
                    let e = if p == 0 { game.doc.e1 } else { game.doc.e2.unwrap_or(game.doc.e1) };
                    let id = game.next_id;
                    game.next_id += 1;
                    game.walkers.push(Walker {
                        id,
                        x: e[0],
                        y: e[1],
                        dir: 1,
                        owner: p as u8,
                        job: Job::Fall { dist: 0, chute: false },
                        can_climb: false,
                        has_chute: false,
                        quit: -1,
                        alive: true,
                    });
                    game.spawned[p] += 1;
                    sfx("tick");
                }
            }
        }

        step_walkers(&mut game, &mut site);

        let active = game.walkers.iter().filter(|w| w.alive).count();
        let all_out = (0..game.players as usize).all(|p| game.spawned[p] >= game.doc.count);
        if game.time_left <= 0 || (all_out && active == 0) {
            finish(&mut game);
        }
    }
}

fn step_walkers(game: &mut Game, site: &mut Site) {
    let doc_x1 = game.doc.x1;
    let doc_x2 = game.doc.x2.unwrap_or(game.doc.x1);
    // One pass to find the supervisors, so blocking is O(walkers + blockers)
    // instead of O(walkers²) — the wide-level head-count optimization.
    let blockers: Vec<(u32, i32, i32)> = game
        .walkers
        .iter()
        .filter(|w| w.alive && matches!(w.job, Job::Block))
        .map(|w| (w.id, w.x, w.y))
        .collect();
    // A supervisor only turns walkers moving TOWARD them: a faller landing
    // inside the field used to flip direction every tick, frozen forever.
    let blocked = |id: u32, wx: i32, wy: i32, dir: i32| {
        blockers.iter().any(|&(bid, bx, by)| {
            bid != id
                && (bx - (wx + dir * 2)).abs() <= 3
                && (by - wy).abs() <= 8
                && (bx - wx) * dir > 0
        })
    };

    let n = game.walkers.len();
    for i in 0..n {
        let mut w = game.walkers[i];
        if !w.alive {
            continue;
        }

        // The quit countdown ticks over WHATEVER the walker is doing —
        // except one who already reached the door: crossed the sill = saved,
        // no exploding in the doorway.
        if w.quit >= 0 && !matches!(w.job, Job::Exiting { .. }) {
            w.quit -= 1;
            if w.quit < 0 {
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
            _ => {}
        }

        let grounded = site.solid(w.x, w.y + 1);
        match w.job {
            Job::Fall { mut dist, chute } => {
                if grounded {
                    if dist > MAX_FALL && !chute {
                        w.job = Job::Splat { ticks: 20 };
                        game.splats[w.owner as usize] += 1;
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
                } else if blocked(w.id, w.x, w.y, w.dir) {
                    w.dir = -w.dir;
                } else {
                    let ahead_x = w.x + w.dir;
                    let mut climbed = -1;
                    for up in 0..=4 {
                        // Full-body clearance: feet, torso, and head — no
                        // more phasing through thin shelves.
                        if !site.solid(ahead_x, w.y - up)
                            && !site.solid(ahead_x, w.y - up - 1)
                            && !site.solid(ahead_x, w.y - up - 2)
                            && !site.solid(ahead_x, w.y - up - 3)
                        {
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
                // The map border reads as an endless "wall": a climber there
                // would ascend into negative-y forever. Turn back instead.
                if wall_x < 0 || wall_x >= site.w || w.y < -4 {
                    w.dir = -w.dir;
                    w.job = Job::Fall { dist: 0, chute: false };
                } else if !site.solid(wall_x, w.y - 4) {
                    w.x = wall_x;
                    w.y -= 4;
                    w.job = Job::Walk;
                } else if site.solid(w.x, w.y - 4) {
                    w.dir = -w.dir;
                    w.job = Job::Fall { dist: 0, chute: false };
                } else {
                    w.y -= 1;
                }
            }
            Job::Block => {
                // Undermined supervisors drop like anyone else.
                if !grounded {
                    w.job = Job::Fall { dist: 0, chute: false };
                }
            }
            Job::Build { mut steps, mut cooldown } => {
                cooldown -= 1;
                if cooldown <= 0 {
                    if steps <= 0 || site.solid(w.x + w.dir * 3, w.y - 2) {
                        w.job = Job::Walk;
                    } else {
                        for k in 0..4 {
                            site.set(w.x + w.dir * (k + 1), w.y, true);
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
                            let x = w.x + w.dir * k;
                            // Only IN-BOUNDS rock counts as work: the border
                            // reads as solid, and a basher crediting it would
                            // tunnel off the map and live there forever.
                            if x >= 0 && x < site.w && site.solid(x, w.y + dy) {
                                any = true;
                            }
                            site.set(x, w.y + dy, false);
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

        // The exit takes walkers at its base — same surface, feet near the
        // door sill, and standing on something. No rescues through floors.
        let exit = if w.owner == 0 { doc_x1 } else { doc_x2 };
        if (w.x - exit[0]).abs() <= 3
            && w.y >= exit[1] - 1
            && w.y <= exit[1] + 3
            && site.solid(w.x, w.y + 1)
            && !matches!(w.job, Job::Exiting { .. } | Job::Splat { .. })
        {
            w.job = Job::Exiting { ticks: 15 };
            sfx("eat");
        }
        if w.y >= GH - 1 {
            if !matches!(w.job, Job::Splat { .. }) {
                sfx("death");
                game.splats[w.owner as usize] += 1;
            }
            w.job = Job::Splat { ticks: 1 };
        }
        game.walkers[i] = w;
    }
}

/// 2P winner with tiebreakers: most rescues, then fewest casualties, then
/// fewest skills spent. None = a genuine dead heat.
fn winner_2p(game: &Game) -> Option<usize> {
    let (a, b) = (game.saved[0], game.saved[1]);
    if a != b {
        return Some(if a > b { 0 } else { 1 });
    }
    if game.dead[0] != game.dead[1] {
        return Some(if game.dead[0] < game.dead[1] { 0 } else { 1 });
    }
    let spent =
        |p: usize| -> u32 { game.doc.skills.iter().sum::<u32>() - game.skills[p].iter().sum::<u32>() };
    if spent(0) != spent(1) {
        return Some(if spent(0) < spent(1) { 0 } else { 1 });
    }
    None
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
        let verdict = match winner_2p(game) {
            Some(0) if a == b => "EQUAL RESCUES - P1 WINS ON CONDUCT",
            Some(1) if a == b => "EQUAL RESCUES - P2 WINS ON CONDUCT",
            Some(0) => "P1 TAKES THE FLOOR",
            Some(_) => "P2 TAKES THE FLOOR",
            None => "A DEAD HEAT. HR IS FURIOUS.",
        };
        game.result = format!("P1 SAVED {a}  /  P2 SAVED {b}\n{verdict}");
        sfx("clear");
    }
}

/// Finds the walker a cursor at (gx, gy) would address for `player`.
fn candidate(game: &Game, player: usize, gx: i32, gy: i32) -> Option<usize> {
    let mut best: Option<(usize, i32)> = None;
    for (i, w) in game.walkers.iter().enumerate() {
        if !w.alive || w.owner as usize != player {
            continue;
        }
        if matches!(w.job, Job::Splat { .. } | Job::Exiting { .. }) {
            continue;
        }
        let d = (w.x - gx).abs() + (w.y - 4 - gy).abs();
        if d <= 10 && best.map(|(_, bd)| d < bd).unwrap_or(true) {
            best = Some((i, d));
        }
    }
    best.map(|(i, _)| i)
}

/// Assigns `skill` near (gx, gy). Active jobs are REASSIGNABLE (build →
/// dig, bash → supervise, …) the way the genre expects; only airborne,
/// exiting, and downed walkers refuse.
fn try_assign(game: &mut Game, player: usize, gx: i32, gy: i32, skill: usize, mine: bool) -> bool {
    if game.skills[player][skill] == 0 {
        return false;
    }
    let Some(i) = candidate(game, player, gx, gy) else { return false };
    let w = &mut game.walkers[i];
    let on_feet = matches!(
        w.job,
        Job::Walk | Job::Block | Job::Build { .. } | Job::Bash { .. } | Job::Dig { .. }
    );
    let applied = match skill {
        0 if !w.can_climb => {
            w.can_climb = true;
            true
        }
        1 if !w.has_chute => {
            w.has_chute = true;
            true
        }
        2 if on_feet && !matches!(w.job, Job::Block) => {
            w.job = Job::Block;
            true
        }
        3 if on_feet && !matches!(w.job, Job::Build { .. }) => {
            w.job = Job::Build { steps: 12, cooldown: 1 };
            true
        }
        4 if on_feet && !matches!(w.job, Job::Bash { .. }) => {
            w.job = Job::Bash { cooldown: 1 };
            true
        }
        5 if on_feet && !matches!(w.job, Job::Dig { .. }) => {
            w.job = Job::Dig { cooldown: 1 };
            true
        }
        6 if w.quit < 0 => {
            w.quit = 60;
            true
        }
        _ => false,
    };
    if applied {
        game.skills[player][skill] -= 1;
        sfx("place");
        if mine {
            stat(
                match skill {
                    0 => "climbers_hired",
                    1 => "chutes_issued",
                    2 => "supervisors_promoted",
                    3 => "bridges_ordered",
                    4 => "bashers_unleashed",
                    5 => "diggers_deployed",
                    _ => "quits_ordered",
                },
                1,
            );
        }
    }
    applied
}

// ---- camera ----

fn camera_scroll(
    keys: Res<ButtonInput<KeyCode>>,
    time: Res<Time>,
    windows: Query<&Window>,
    cameras: Query<(&Camera, &GlobalTransform)>,
    editor: Res<Editor>,
    game: Res<Game>,
    mut cam: ResMut<Cam>,
) {
    let dt = time.delta_secs();
    let mut dx = 0.0;
    // A/D scroll for the mouse player (P2's arrows drive their crosshair).
    if keys.pressed(KeyCode::KeyA) {
        dx -= 480.0 * dt;
    }
    if keys.pressed(KeyCode::KeyD) {
        dx += 480.0 * dt;
    }
    // Mouse at the play-area edge nudges the window (classic).
    if let (Ok(window), Ok((camera, cam_tf))) = (windows.single(), cameras.single()) {
        if let Some(world) = crate::retro::cursor_world(window, camera, cam_tf) {
            if world.y > -220.0 {
                if world.x > 320.0 {
                    dx += 420.0 * dt;
                }
                if world.x < -320.0 {
                    dx -= 420.0 * dt;
                }
            }
        }
    }
    // P2 shoving the crosshair against the view edge scrolls too (local 2P).
    // The crosshair is world-space now, so compare against the camera window.
    if !editor.active && game.players == 2 {
        let sx = game.p2cursor.x - cam.x; // 0..VIEW_W inside the window
        if sx >= VIEW_W - 12.0 {
            dx += 360.0 * dt;
        }
        if sx <= 12.0 {
            dx -= 360.0 * dt;
        }
    }
    if dx != 0.0 {
        cam.x = (cam.x + dx).clamp(0.0, cam.max);
    }
}

/// The terrain sprite slides so the camera window shows the right slice.
fn position_terrain(cam: Res<Cam>, site: Res<Site>, mut q: Query<&mut Transform, With<TerrainSprite>>) {
    let Ok(mut tf) = q.single_mut() else { return };
    tf.translation.x = site.w as f32 * CELL / 2.0 - 360.0 - cam.x;
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
    mut cam: ResMut<Cam>,
    mut game: ResMut<Game>,
) {
    if editor.active || game.over {
        return;
    }
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
    // T = faster flow, R = slower — YOUR stream only; guests relay theirs.
    let guest = is_guest(&net);
    if keys.just_pressed(KeyCode::KeyR) {
        game.rate[me.min(1)] = (game.rate[me.min(1)] + 10).min(120);
        if guest {
            if let Ok(msg) = serde_json::to_string(&WireRate { t: "rt".into(), v: game.rate[me.min(1)] }) {
                net_send(&msg);
            }
        }
    }
    if keys.just_pressed(KeyCode::KeyT) {
        game.rate[me.min(1)] = game.rate[me.min(1)].saturating_sub(10).max(10);
        if guest {
            if let Ok(msg) = serde_json::to_string(&WireRate { t: "rt".into(), v: game.rate[me.min(1)] }) {
                net_send(&msg);
            }
        }
    }
    // N-N: YOUR crew quits. Owner-scoped — no detonating the opposition.
    if keys.just_pressed(KeyCode::KeyN) {
        if game.nuke_armed {
            game.nuke_armed = false;
            if guest {
                if let Ok(msg) = serde_json::to_string(&WireNuke { t: "nk".into() }) {
                    net_send(&msg);
                    stat("nukes_ordered", 1);
                }
            } else {
                nuke_own(&mut game, me.min(1) as u8);
                stat("nukes_ordered", 1);
            }
        } else {
            game.nuke_armed = true;
        }
    }

    // Hover feedback every frame; assignment on click.
    let Ok(window) = windows.single() else { return };
    let Ok((camera, cam_tf)) = cameras.single() else { return };
    let Some(world) = crate::retro::cursor_world(window, camera, cam_tf) else {
        game.hover[me.min(1)] = None;
        return;
    };
    // Click-to-jump on the minimap: teleport the camera window there.
    let site_px = cam.max + VIEW_W;
    if site_px > VIEW_W
        && (world.x - MM_CX).abs() <= (MM_W + 4.0) / 2.0
        && (world.y - MM_CY).abs() <= (MM_H + 4.0) / 2.0
    {
        game.hover[me.min(1)] = None;
        if buttons.just_pressed(MouseButton::Left) {
            let frac = ((world.x - (MM_CX - MM_W / 2.0)) / MM_W).clamp(0.0, 1.0);
            cam.x = (frac * site_px - VIEW_W / 2.0).clamp(0.0, cam.max);
            sfx("tick");
        }
        return;
    }
    let (gx, gy) = world_to_grid(&cam, world);
    game.hover[me.min(1)] = candidate(&game, me, gx, gy).map(|i| game.walkers[i].id);

    if !buttons.just_pressed(MouseButton::Left) || gy < 0 || gy >= GH {
        return;
    }
    let skill = game.selected[me];
    if is_guest(&net) {
        if let Ok(msg) = serde_json::to_string(&WireAssign { t: "as".into(), x: gx, y: gy, skill }) {
            net_send(&msg);
            stat(
                match skill {
                    0 => "climbers_hired",
                    1 => "chutes_issued",
                    2 => "supervisors_promoted",
                    3 => "bridges_ordered",
                    4 => "bashers_unleashed",
                    5 => "diggers_deployed",
                    _ => "quits_ordered",
                },
                1,
            );
        }
        return;
    }
    try_assign(&mut game, me, gx, gy, skill, true);
}

fn input_p2(
    keys: Res<ButtonInput<KeyCode>>,
    time: Res<Time>,
    net: Res<NetMode>,
    editor: Res<Editor>,
    site: Res<Site>,
    mut game: ResMut<Game>,
) {
    if editor.active || game.over || net.0.is_some() || game.players < 2 {
        return;
    }
    // The crosshair lives in WORLD x: camera scrolling (P1's or their own)
    // never slides P2's aim off the walker they lined up.
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
    game.p2cursor.x = game.p2cursor.x.clamp(0.0, site.w as f32 * CELL);
    game.p2cursor.y = game.p2cursor.y.clamp(-220.0, 320.0);
    if keys.just_pressed(KeyCode::KeyQ) {
        game.selected[1] = (game.selected[1] + 6) % 7;
        sfx("tick");
    }
    if keys.just_pressed(KeyCode::KeyE) {
        game.selected[1] = (game.selected[1] + 1) % 7;
        sfx("tick");
    }
    let gx = (game.p2cursor.x / CELL) as i32;
    let gy = ((320.0 - game.p2cursor.y) / CELL) as i32;
    game.hover[1] = candidate(&game, 1, gx, gy).map(|i| game.walkers[i].id);
    if keys.just_pressed(KeyCode::Enter) {
        let skill = game.selected[1];
        try_assign(&mut game, 1, gx, gy, skill, true);
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
            let flags = w.can_climb as i32
                | ((w.has_chute as i32) << 1)
                | (((w.dir > 0) as i32) << 2)
                | (((w.quit >= 0) as i32) << 3);
            [w.id as i32, w.x, w.y, flags, w.owner as i32, job_code(w.job)]
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
        if let Ok(msg) =
            serde_json::to_string(&WireEnd { t: "end".into(), saved: game.saved, result: game.result.clone() })
        {
            net_send(&msg);
        }
    }
}

fn net_apply(
    mut events: EventReader<NetIn>,
    time: Res<Time>,
    net: Res<NetMode>,
    mut game: ResMut<Game>,
    mut site: ResMut<Site>,
    mut cam: ResMut<Cam>,
    mut images: ResMut<Assets<Image>>,
    mut terrain: Query<(&mut Sprite, &mut Transform), With<TerrainSprite>>,
) {
    // Advance the guest's interpolation clock every frame.
    game.guest_t += time.delta_secs();

    let Some(cfg) = &net.0 else {
        events.clear();
        return;
    };
    let host = cfg.is_host();
    // A guest can't sit on "WAITING FOR THE HOST'S LEVEL..." forever: if the
    // transfer never lands (dropped frames, a host bug), say so and end it.
    if !host && !game.guest_ready && !game.over {
        game.lv_wait += time.delta_secs();
        if game.lv_wait > 20.0 {
            game.over = true;
            game.over_timer.reset();
            game.result = "THE LEVEL NEVER ARRIVED\nTRY A NEW ROOM".into();
        }
    }
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
                        try_assign(&mut game, ev.seat as usize, a.x, a.y, a.skill, false);
                    }
                }
            }
            Some("rt") if host => {
                if let Ok(r) = serde_json::from_str::<WireRate>(&ev.data) {
                    game.rate[(ev.seat as usize).min(1)] = r.v.clamp(10, 120);
                }
            }
            Some("nk") if host => {
                nuke_own(&mut game, (ev.seat as usize).min(1) as u8);
            }
            Some("lv") if !host => {
                if let Ok(mut wl) = serde_json::from_str::<WireLevel>(&ev.data) {
                    sanitize_doc(&mut wl.doc);
                    if wl.parts == 0 {
                        apply_level(wl.doc, &mut game, &mut site, &mut cam, &mut images, &mut terrain);
                    } else {
                        // Big painted level: the runs arrive in "lvr" parts.
                        game.lv_parts = wl.parts;
                        game.lv_runs.clear();
                        game.lv_doc = Some(wl.doc);
                    }
                }
            }
            Some("lvr") if !host => {
                if let Ok(part) = serde_json::from_str::<WireRuns>(&ev.data) {
                    if game.lv_doc.is_some() && game.lv_parts > 0 {
                        game.lv_runs.extend(part.runs);
                        game.lv_parts -= 1;
                        if game.lv_parts == 0 {
                            let mut doc = game.lv_doc.take().expect("checked above");
                            doc.runs = std::mem::take(&mut game.lv_runs);
                            sanitize_doc(&mut doc);
                            apply_level(doc, &mut game, &mut site, &mut cam, &mut images, &mut terrain);
                        }
                    }
                }
            }
            Some("tr") if !host => {
                if let Ok(tr) = serde_json::from_str::<WireTerrain>(&ev.data) {
                    for c in tr.chg {
                        let idx = (c >> 1) as usize;
                        if idx < site.mask.len() {
                            site.mask[idx] = c & 1 == 1;
                            let (x, y) = ((idx as i32) % site.w, (idx as i32) / site.w);
                            site.grow_dirty(x, y);
                        }
                    }
                }
            }
            Some("st") if !host => {
                if let Ok(st) = serde_json::from_str::<WireState>(&ev.data) {
                    game.saved = st.saved;
                    game.dead = st.dead;
                    game.spawned = st.spawned;
                    game.skills = st.skills;
                    game.time_left = st.tl;
                    // Roll the interpolation window.
                    game.guest_prev = std::mem::take(&mut game.guest_curr);
                    game.guest_dt = game.guest_t.clamp(0.05, 0.3);
                    game.guest_t = 0.0;
                    game.walkers = st
                        .w
                        .iter()
                        .map(|a| {
                            game.guest_curr.insert(a[0] as u32, (a[1] as f32, a[2] as f32));
                            Walker {
                                id: a[0] as u32,
                                x: a[1],
                                y: a[2],
                                dir: if a[3] & 4 != 0 { 1 } else { -1 },
                                owner: a[4] as u8,
                                job: match a[5] {
                                    1 => Job::Fall { dist: 0, chute: false },
                                    2 => Job::Fall { dist: 0, chute: true },
                                    3 => Job::Climb,
                                    4 => Job::Block,
                                    5 => Job::Build { steps: 1, cooldown: 99 },
                                    6 => Job::Bash { cooldown: 99 },
                                    7 => Job::Dig { cooldown: 99 },
                                    9 => Job::Splat { ticks: 99 },
                                    10 => Job::Exiting { ticks: 99 },
                                    _ => Job::Walk,
                                },
                                can_climb: a[3] & 1 != 0,
                                has_chute: a[3] & 2 != 0,
                                quit: if a[3] & 8 != 0 { 1 } else { -1 },
                                alive: true,
                            }
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

/// Points the guest's site at a freshly received host level.
fn apply_level(
    doc: LevelDoc,
    game: &mut Game,
    site: &mut Site,
    cam: &mut Cam,
    images: &mut Assets<Image>,
    terrain: &mut Query<(&mut Sprite, &mut Transform), With<TerrainSprite>>,
) {
    let w = doc.w;
    site.w = w;
    build_mask(&doc, w, &mut site.mask);
    site.image = images.add(terrain_image(w, &site.mask));
    site.dirty.clear();
    if let Ok((mut sprite, _)) = terrain.single_mut() {
        sprite.image = site.image.clone();
        sprite.custom_size = Some(Vec2::new(w as f32 * CELL, 540.0));
    }
    cam.x = 0.0;
    cam.max = (w as f32 * CELL - VIEW_W).max(0.0);
    game.skills = [doc.skills, doc.skills];
    game.time_left = (doc.time * TICKS_PER_SEC as u32) as i32;
    game.doc = doc;
    game.guest_ready = true;
}

// ---- rendering ----

fn refresh_terrain(mut site: ResMut<Site>, mut images: ResMut<Assets<Image>>) {
    if site.dirty.is_empty() {
        return;
    }
    let rects = std::mem::take(&mut site.dirty);
    let w = site.w;
    if let Some(img) = images.get_mut(&site.image) {
        if let Some(data) = img.data.as_mut() {
            for (x0, y0, x1, y1) in rects {
                paint_region(w, &site.mask, data, x0, y0, x1, y1);
            }
        }
    }
}

fn job_color(w: &Walker) -> Color {
    let base = if w.owner == 0 { GREEN } else { MAGENTA };
    if w.quit >= 0 {
        return RED;
    }
    match w.job {
        Job::Block => AMBER,
        Job::Build { .. } => CYAN,
        Job::Bash { .. } | Job::Dig { .. } => WHITE,
        Job::Splat { .. } => RED,
        _ => base,
    }
}

/// Guest walkers ease between the last two snapshots instead of teleporting
/// at the snapshot rate.
fn walker_screen_pos(game: &Game, cam: &Cam, w: &Walker, guest: bool) -> Vec2 {
    if guest {
        if let (Some(&(px, py)), Some(&(cx, cy))) =
            (game.guest_prev.get(&w.id), game.guest_curr.get(&w.id))
        {
            let t = (game.guest_t / game.guest_dt).clamp(0.0, 1.0);
            let gx = px + (cx - px) * t;
            let gy = py + (cy - py) * t;
            return Vec2::new(
                -360.0 + (gx + 0.5) * CELL - cam.x,
                320.0 - (gy - 4.0 + 0.5) * CELL,
            );
        }
    }
    grid_to_world(cam, w.x, w.y - 4)
}

const MM_W: f32 = 216.0;
const MM_H: f32 = 54.0;
const MM_CX: f32 = 244.0;
const MM_CY: f32 = 285.0;

fn minimap_pos(site_w: i32, gx: f32, gy: f32) -> Vec2 {
    Vec2::new(
        MM_CX - MM_W / 2.0 + gx / site_w as f32 * MM_W,
        MM_CY + MM_H / 2.0 - gy / GH as f32 * MM_H,
    )
}

#[allow(clippy::too_many_arguments)]
fn draw(
    mut gizmos: Gizmos,
    game: Res<Game>,
    editor: Res<Editor>,
    net: Res<NetMode>,
    cam: Res<Cam>,
    site: Res<Site>,
    buttons: Res<ButtonInput<MouseButton>>,
    windows: Query<&Window>,
    cameras: Query<(&Camera, &GlobalTransform)>,
) {
    let wide = site.w as f32 * CELL > VIEW_W;
    if editor.active {
        draw_doors(&mut gizmos, &editor.doc, &cam, true);
        // Brush cursor: size, position, paint-vs-erase, and the mirror twin —
        // authoring blind was the editor's biggest friction.
        if let (Ok(window), Ok((camera, cam_tf))) = (windows.single(), cameras.single()) {
            if let Some(world) = crate::retro::cursor_world(window, camera, cam_tf) {
                let r = editor.brush as f32 * CELL;
                let color = if buttons.pressed(MouseButton::Right) { RED } else { GREEN };
                gizmos.circle_2d(world, r, color.with_alpha(0.85));
                if editor.mirror {
                    let (gx, gy) = world_to_grid(&cam, world);
                    let twin = grid_to_world(&cam, editor.w - 1 - gx, gy);
                    if twin.x > -380.0 && twin.x < 380.0 {
                        gizmos.circle_2d(twin, r, color.with_alpha(0.35));
                    }
                }
            }
        }
        if wide {
            draw_minimap(&mut gizmos, &game, &cam, &site, true, &editor.doc);
        }
        return;
    }
    draw_doors(&mut gizmos, &game.doc, &cam, game.players == 2);

    let guest = is_guest(&net);
    for w in game.walkers.iter().filter(|w| w.alive) {
        let p = walker_screen_pos(&game, &cam, w, guest);
        if p.x < -380.0 || p.x > 380.0 {
            continue; // off the camera window
        }
        let color = job_color(w);
        gizmos.rect_2d(p, Vec2::new(4.0, 12.0), color);
        gizmos.rect_2d(p + Vec2::new(w.dir as f32, 7.0), Vec2::new(4.0, 3.0), color);
        if matches!(w.job, Job::Fall { chute: true, .. }) {
            gizmos.circle_2d(p + Vec2::new(0.0, 12.0), 6.0, CYAN);
        }
        if w.quit >= 0 {
            gizmos.circle_2d(p + Vec2::new(0.0, 12.0), 3.0, RED);
        }
        // Hover ring: this is the walker your click would address.
        for (pl, hover) in game.hover.iter().enumerate() {
            if *hover == Some(w.id) {
                let ring = if pl == 0 { WHITE } else { MAGENTA };
                gizmos.circle_2d(p, 11.0, ring);
            }
        }
    }
    if game.players == 2 && net.0.is_none() {
        // World-space crosshair → screen position under the current camera.
        let c = Vec2::new(game.p2cursor.x - cam.x - 360.0, game.p2cursor.y);
        if c.x >= -368.0 && c.x <= 368.0 {
            gizmos.line_2d(c - Vec2::X * 8.0, c + Vec2::X * 8.0, MAGENTA);
            gizmos.line_2d(c - Vec2::Y * 8.0, c + Vec2::Y * 8.0, MAGENTA);
        }
    }
    if wide {
        draw_minimap(&mut gizmos, &game, &cam, &site, false, &game.doc);
    }
}

fn draw_doors(gizmos: &mut Gizmos, doc: &LevelDoc, cam: &Cam, two_p: bool) {
    draw_door(gizmos, cam, doc.e1, GREEN, true);
    draw_door(gizmos, cam, doc.x1, GREEN, false);
    if two_p {
        if let Some(e2) = doc.e2 {
            draw_door(gizmos, cam, e2, MAGENTA, true);
        }
        if let Some(x2) = doc.x2 {
            draw_door(gizmos, cam, x2, MAGENTA, false);
        }
    }
}

fn draw_door(gizmos: &mut Gizmos, cam: &Cam, pos: [i32; 2], color: Color, entrance: bool) {
    let p = grid_to_world(cam, pos[0], pos[1]);
    if p.x < -400.0 || p.x > 400.0 {
        return;
    }
    gizmos.rect_2d(p, Vec2::new(16.0, 20.0), color);
    if entrance {
        gizmos.line_2d(p + Vec2::new(-4.0, 4.0), p + Vec2::new(0.0, -4.0), color);
        gizmos.line_2d(p + Vec2::new(4.0, 4.0), p + Vec2::new(0.0, -4.0), color);
    } else {
        gizmos.line_2d(p + Vec2::new(-4.0, -4.0), p + Vec2::new(0.0, 4.0), color);
        gizmos.line_2d(p + Vec2::new(4.0, -4.0), p + Vec2::new(0.0, 4.0), color);
    }
}

/// Wide levels get an overview strip: doors, walkers, the camera window.
fn draw_minimap(gizmos: &mut Gizmos, game: &Game, cam: &Cam, site: &Site, editor: bool, doc: &LevelDoc) {
    let frame = Color::srgba(0.6, 0.6, 0.7, 0.8);
    gizmos.rect_2d(Vec2::new(MM_CX, MM_CY), Vec2::new(MM_W + 4.0, MM_H + 4.0), frame);
    // Camera window.
    let vis0 = cam.x / CELL;
    let vis1 = (cam.x + VIEW_W) / CELL;
    let wl = minimap_pos(site.w, vis0, 0.0);
    let wr = minimap_pos(site.w, vis1, GH as f32);
    gizmos.rect_2d(
        Vec2::new((wl.x + wr.x) / 2.0, MM_CY),
        Vec2::new(wr.x - wl.x, MM_H),
        AMBER,
    );
    // Doors.
    for (pos, color) in [(Some(doc.e1), GREEN), (Some(doc.x1), GREEN), (doc.e2, MAGENTA), (doc.x2, MAGENTA)] {
        if let Some(pos) = pos {
            let p = minimap_pos(site.w, pos[0] as f32, pos[1] as f32);
            gizmos.rect_2d(p, Vec2::splat(3.0), color);
        }
    }
    if editor {
        return;
    }
    // Walkers.
    for w in game.walkers.iter().filter(|w| w.alive) {
        let p = minimap_pos(site.w, w.x as f32, w.y as f32);
        let c = if w.owner == 0 { GREEN } else { MAGENTA };
        gizmos.rect_2d(p, Vec2::splat(2.0), c);
    }
}

#[allow(clippy::too_many_arguments)]
fn hud_update(
    game: Res<Game>,
    editor: Res<Editor>,
    net: Res<NetMode>,
    mut hud: Query<&mut Text2d, (With<Hud>, Without<HudLine2>, Without<SkillBar>)>,
    mut hud2: Query<&mut Text2d, (With<HudLine2>, Without<Hud>, Without<SkillBar>)>,
    mut bars: Query<(&SkillBar, &mut Text2d), (Without<Hud>, Without<HudLine2>)>,
) {
    let Ok(mut t) = hud.single_mut() else { return };
    let Ok(mut t2) = hud2.single_mut() else { return };
    if editor.active {
        let s = format!(
            "EDITOR {}x270 (W)  BRUSH {} [ ]  MIRROR {} (F)  DOORS 1-4  U UNDO  S SAVE  G TEST",
            editor.w,
            editor.brush,
            if editor.mirror { "ON" } else { "OFF" }
        );
        // A validation warning owns the second line while it lasts.
        let s2 = if let Some((warning, _)) = &editor.warning {
            format!("!! {warning} !!")
        } else {
            format!(
                "POOL {} {} (TAB, 8/9, 0=ALL)  COUNT {} (C/V)  NEED {} (B/N)  RATE {} (R/T)  TIME {}s (K/L)",
                SKILL_NAMES[editor.skill_sel],
                editor.doc.skills[editor.skill_sel],
                editor.doc.count,
                editor.doc.need,
                editor.doc.rate,
                editor.doc.time
            )
        };
        if t.0 != s {
            t.0 = s;
        }
        if t2.0 != s2 {
            t2.0 = s2;
        }
        for (_, mut bt) in &mut bars {
            if !bt.0.is_empty() {
                bt.0 = String::new();
            }
        }
        return;
    }
    let guest = is_guest(&net);
    let s = if game.over {
        game.result.clone()
    } else if !game.guest_ready {
        "WAITING FOR THE HOST'S LEVEL...".to_string()
    } else {
        let secs = (game.time_left as f32 / TICKS_PER_SEC).max(0.0) as i32;
        let me: usize = match &net.0 {
            Some(cfg) => (cfg.seat as usize).min(1),
            None => 0,
        };
        let flow = 1800 / game.rate[me].max(10); // YOUR spawns per minute
        let nuke = if game.nuke_armed && !guest { "   N AGAIN: EVERYONE QUITS" } else { "" };
        let goal = if game.players == 2 {
            "MOST RESCUES WINS".to_string()
        } else {
            format!("NEED {}", game.doc.need)
        };
        // Guests control their own stream now, so everyone sees the dial.
        let rate_ui = format!("   FLOW {flow}/MIN (T+ R-)");
        format!(
            "OUT {}  IN {}/{}  {}   TIME {}:{:02}{}{}",
            game.walkers.iter().filter(|w| w.alive).count(),
            game.saved[0] + game.saved[1],
            game.doc.count * game.players as u32,
            goal,
            secs / 60,
            secs % 60,
            rate_ui,
            nuke
        )
    };
    if t.0 != s {
        t.0 = s;
    }
    // Second line: what the hovered walker is doing.
    let me: usize = match &net.0 {
        Some(cfg) => cfg.seat as usize,
        None => 0,
    };
    let mut s2 = String::new();
    for (pl, hover) in game.hover.iter().enumerate() {
        if let Some(id) = hover {
            if let Some(w) = game.walkers.iter().find(|w| w.id == *id) {
                let tags = format!(
                    "{}{}",
                    if w.can_climb { " +CLIMB" } else { "" },
                    if w.has_chute { " +CHUTE" } else { "" }
                );
                s2.push_str(&format!("P{}: {}{}   ", pl + 1, job_name(w.job, w.quit), tags));
            }
        }
    }
    if t2.0 != s2 {
        t2.0 = s2;
    }
    for (bar, mut bt) in &mut bars {
        let p = bar.0 as usize;
        if p >= game.players as usize {
            if !bt.0.is_empty() {
                bt.0 = String::new();
            }
            continue;
        }
        // Selection marker only on rows this machine controls.
        let controls_row = if net.0.is_some() { p == me } else { true };
        let marker = |i: usize| {
            if controls_row && game.selected[p] == i {
                ">"
            } else {
                " "
            }
        };
        let s: String = (0..7)
            .map(|i| format!("{}{} {} ", marker(i), SKILL_NAMES[i], game.skills[p][i]))
            .collect();
        let who = if net.0.is_some() && p == me {
            format!("P{} (YOU) ", p + 1)
        } else {
            format!("P{} ", p + 1)
        };
        let s = who + &s;
        if bt.0 != s {
            bt.0 = s;
        }
    }
}

fn endgame(
    time: Res<Time>,
    net: Res<NetMode>,
    mut editor: ResMut<Editor>,
    mut game: ResMut<Game>,
    mut site: ResMut<Site>,
    mut cam: ResMut<Cam>,
    mut images: ResMut<Assets<Image>>,
    mut terrain: Query<&mut Sprite, With<TerrainSprite>>,
    mut final_score: ResMut<FinalScore>,
    mut next: ResMut<NextState<Phase>>,
) {
    if editor.active || !game.over {
        return;
    }
    if !game.over_timer.tick(time.delta()).finished() {
        return;
    }
    // A finished TEST-PLAY returns to the editor: no score, no game over,
    // and absolutely no losing the level you were building.
    if editor.testing {
        editor.testing = false;
        editor.active = true;
        restore_editor_site(&editor, &mut site, &mut cam, &mut images, &mut terrain);
        reset_round(&mut game);
        return;
    }
    let me: usize = match &net.0 {
        Some(cfg) => cfg.seat as usize,
        None => 0,
    };
    // Service record: local rounds credit every locally-controlled stream
    // (one account, however many hands); online rounds credit your seat.
    let owners: &[usize] = if net.0.is_some() {
        &[me]
    } else if game.players == 2 {
        &[0, 1]
    } else {
        &[0]
    };
    for &o in owners {
        let o = o.min(1);
        stat("interns_saved", game.saved[o] as u64);
        stat("interns_lost", game.dead[o] as u64);
        stat("gravity_lessons", game.splats[o] as u64);
    }
    let mine = game.saved[me.min(1)];
    let mut score = mine * 100;
    if game.players == 1 {
        if mine >= game.doc.need {
            score += 500 + (game.time_left.max(0) as u32 / 30) * 2;
            stat("floor_wins", 1);
        }
    } else if winner_2p(&game) == Some(me.min(1)) {
        score += 300;
        stat("floor_wins", 1);
    }
    final_score.0 = score;
    next.set(Phase::GameOver);
}

fn reset_round(game: &mut Game) {
    game.walkers.clear();
    game.spawned = [0; 2];
    game.saved = [0; 2];
    game.dead = [0; 2];
    game.splats = [0; 2];
    game.over = false;
    game.nuke_armed = false;
    game.hover = [None; 2];
}

/// Points the site (mask + texture + sprite + camera) at the editor canvas.
fn restore_editor_site(
    editor: &Editor,
    site: &mut Site,
    cam: &mut Cam,
    images: &mut Assets<Image>,
    terrain: &mut Query<&mut Sprite, With<TerrainSprite>>,
) {
    site.w = editor.w;
    site.mask = editor.mask.clone();
    site.image = images.add(terrain_image(editor.w, &site.mask));
    site.dirty.clear();
    if let Ok(mut sprite) = terrain.single_mut() {
        sprite.image = site.image.clone();
        sprite.custom_size = Some(Vec2::new(editor.w as f32 * CELL, 540.0));
    }
    cam.max = (editor.w as f32 * CELL - VIEW_W).max(0.0);
    cam.x = cam.x.clamp(0.0, cam.max);
}

// ---- the level editor ----

/// Save/test gate: a buried door ships a broken round — walkers spawn inside
/// rock or nothing can ever leave. Requires each door's pocket to be mostly
/// open. (No reachability check on purpose: bashers and diggers exist.)
fn validate_canvas(editor: &Editor) -> Option<String> {
    let clear = |pos: [i32; 2]| -> bool {
        let (mut solid, mut total) = (0, 0);
        for dy in -4..=1i32 {
            for dx in -2..=2i32 {
                let (x, y) = (pos[0] + dx, pos[1] + dy);
                if x < 0 || x >= editor.w || y < 0 || y >= GH {
                    continue;
                }
                total += 1;
                if editor.mask[(y * editor.w + x) as usize] {
                    solid += 1;
                }
            }
        }
        total == 0 || solid * 2 < total
    };
    if !clear(editor.doc.e1) {
        return Some("ENTRANCE 1 IS BURIED - CLEAR IT".into());
    }
    if !clear(editor.doc.x1) {
        return Some("EXIT 1 IS BURIED - CLEAR IT".into());
    }
    if let Some(e2) = editor.doc.e2 {
        if !clear(e2) {
            return Some("ENTRANCE 2 IS BURIED - CLEAR IT".into());
        }
    }
    if let Some(x2) = editor.doc.x2 {
        if !clear(x2) {
            return Some("EXIT 2 IS BURIED - CLEAR IT".into());
        }
    }
    None
}

#[allow(clippy::too_many_arguments)]
fn editor_update(
    buttons: Res<ButtonInput<MouseButton>>,
    keys: Res<ButtonInput<KeyCode>>,
    time: Res<Time>,
    windows: Query<&Window>,
    cameras: Query<(&Camera, &GlobalTransform)>,
    mut cam: ResMut<Cam>,
    mut editor: ResMut<Editor>,
    mut game: ResMut<Game>,
    mut site: ResMut<Site>,
    mut images: ResMut<Assets<Image>>,
    mut terrain: Query<&mut Sprite, With<TerrainSprite>>,
) {
    if !editor.active {
        // X during a test-play returns to the canvas early.
        if editor.testing && keys.just_pressed(KeyCode::KeyX) {
            editor.testing = false;
            editor.active = true;
            restore_editor_site(&editor, &mut site, &mut cam, &mut images, &mut terrain);
            reset_round(&mut game);
        }
        return;
    }

    // Tick the validation notice.
    if let Some((_, t)) = editor.warning.as_mut() {
        if t.tick(time.delta()).finished() {
            editor.warning = None;
        }
    }

    if keys.just_pressed(KeyCode::KeyG) {
        if let Some(problem) = validate_canvas(&editor) {
            editor.warning = Some((problem, Timer::from_seconds(2.5, TimerMode::Once)));
            sfx("buzz");
            return;
        }
        let doc = compile_doc(&editor);
        game.doc = doc;
        game.players = 1;
        reset_round(&mut game);
        game.skills = [game.doc.skills, game.doc.skills];
        game.rate = [game.doc.rate; 2];
        game.time_left = (game.doc.time * TICKS_PER_SEC as u32) as i32;
        game.guest_ready = true;
        build_mask(&game.doc, site.w, &mut site.mask);
        site.all_dirty();
        editor.testing = true;
        editor.active = false;
        sfx("coin");
        return;
    }
    if keys.just_pressed(KeyCode::KeyS) {
        if let Some(problem) = validate_canvas(&editor) {
            editor.warning = Some((problem, Timer::from_seconds(2.5, TimerMode::Once)));
            sfx("buzz");
            return;
        }
        let doc = compile_doc(&editor);
        if let Ok(json) = serde_json::to_string(&doc) {
            crate::shell::save_level(&json);
            sfx("clear");
        }
    }
    // Width cycling rebuilds the canvas, preserving the overlap.
    if keys.just_pressed(KeyCode::KeyW) {
        let idx = EDITOR_WIDTHS.iter().position(|&w| w == editor.w).unwrap_or(0);
        let new_w = EDITOR_WIDTHS[(idx + 1) % EDITOR_WIDTHS.len()];
        let mut new_mask = vec![false; (new_w * GH) as usize];
        for y in 0..GH {
            for x in 0..editor.w.min(new_w) {
                new_mask[(y * new_w + x) as usize] = editor.mask[(y * editor.w + x) as usize];
            }
        }
        editor.w = new_w;
        editor.mask = new_mask;
        editor.undo.clear();
        editor.doc.w = new_w;
        sanitize_doc(&mut editor.doc);
        restore_editor_site(&editor, &mut site, &mut cam, &mut images, &mut terrain);
        sfx("tick");
    }
    if keys.just_pressed(KeyCode::KeyU) {
        if let Some(prev) = editor.undo.pop() {
            editor.mask = prev;
            site.mask = editor.mask.clone();
            site.all_dirty();
            sfx("tick");
        }
    }
    if keys.just_pressed(KeyCode::KeyF) {
        editor.mirror = !editor.mirror;
        sfx("tick");
    }
    if keys.just_pressed(KeyCode::Tab) {
        editor.skill_sel = (editor.skill_sel + 1) % 7;
    }
    if keys.just_pressed(KeyCode::BracketLeft) {
        editor.brush = (editor.brush - 2).max(2);
    }
    if keys.just_pressed(KeyCode::BracketRight) {
        editor.brush = (editor.brush + 2).min(16);
    }
    if keys.just_pressed(KeyCode::Digit8) {
        let i = editor.skill_sel;
        editor.doc.skills[i] = editor.doc.skills[i].saturating_sub(2);
    }
    if keys.just_pressed(KeyCode::Digit9) {
        let i = editor.skill_sel;
        editor.doc.skills[i] = (editor.doc.skills[i] + 2).min(99);
    }
    if keys.just_pressed(KeyCode::Digit0) {
        let v = editor.doc.skills[editor.skill_sel];
        editor.doc.skills = [v; 7];
    }
    if keys.just_pressed(KeyCode::KeyC) {
        editor.doc.count = editor.doc.count.saturating_sub(5).max(5);
    }
    if keys.just_pressed(KeyCode::KeyV) {
        editor.doc.count = (editor.doc.count + 5).min(MAX_COUNT);
    }
    if keys.just_pressed(KeyCode::KeyB) {
        editor.doc.need = editor.doc.need.saturating_sub(5).max(1);
    }
    if keys.just_pressed(KeyCode::KeyN) {
        editor.doc.need = (editor.doc.need + 5).min(editor.doc.count);
    }
    if keys.just_pressed(KeyCode::KeyR) {
        editor.doc.rate = (editor.doc.rate + 10).min(120);
    }
    if keys.just_pressed(KeyCode::KeyT) {
        editor.doc.rate = editor.doc.rate.saturating_sub(10).max(10);
    }
    if keys.just_pressed(KeyCode::KeyK) {
        editor.doc.time = editor.doc.time.saturating_sub(60).max(60);
    }
    if keys.just_pressed(KeyCode::KeyL) {
        editor.doc.time = (editor.doc.time + 60).min(900);
    }

    let Ok(window) = windows.single() else { return };
    let Ok((camera, cam_tf)) = cameras.single() else { return };
    let Some(world) = crate::retro::cursor_world(window, camera, cam_tf) else { return };
    let (gx, gy) = world_to_grid(&cam, world);
    if gy < 0 || gy >= GH || gx < 0 || gx >= editor.w {
        return;
    }
    for (key, which) in [
        (KeyCode::Digit1, 0),
        (KeyCode::Digit2, 1),
        (KeyCode::Digit3, 2),
        (KeyCode::Digit4, 3),
    ] {
        if keys.just_pressed(key) {
            // Mirror mode exists FOR symmetric 2P maps: placing a P1 door
            // auto-places P2's twin across the axis (and vice versa).
            let twin = [editor.w - 1 - gx, gy];
            match which {
                0 => {
                    editor.doc.e1 = [gx, gy];
                    if editor.mirror {
                        editor.doc.e2 = Some(twin);
                    }
                }
                1 => {
                    editor.doc.x1 = [gx, gy];
                    if editor.mirror {
                        editor.doc.x2 = Some(twin);
                    }
                }
                2 => {
                    editor.doc.e2 = Some([gx, gy]);
                    if editor.mirror {
                        editor.doc.e1 = twin;
                    }
                }
                _ => {
                    editor.doc.x2 = Some([gx, gy]);
                    if editor.mirror {
                        editor.doc.x1 = twin;
                    }
                }
            }
            sanitize_doc(&mut editor.doc);
            sfx("place");
        }
    }

    let paint = buttons.pressed(MouseButton::Left);
    let erase = buttons.pressed(MouseButton::Right);
    if paint || erase {
        // One undo snapshot per stroke.
        if !editor.stroke_open {
            editor.stroke_open = true;
            if editor.undo.len() >= UNDO_CAP {
                editor.undo.remove(0);
            }
            let snap = editor.mask.clone();
            editor.undo.push(snap);
        }
        let v = paint && !erase;
        let shift = keys.pressed(KeyCode::ShiftLeft) || keys.pressed(KeyCode::ShiftRight);
        if shift && buttons.just_pressed(MouseButton::Left) {
            // Line tool: connect from the previous anchor with the brush.
            if let Some((lx, ly)) = editor.last_click {
                stamp_line(&mut editor, &mut site, lx, ly, gx, gy, v);
            }
        } else {
            stamp(&mut editor, &mut site, gx, gy, v);
        }
        if buttons.just_pressed(MouseButton::Left) {
            editor.last_click = Some((gx, gy));
        }
    } else {
        editor.stroke_open = false;
    }
}

fn stamp(editor: &mut Editor, site: &mut Site, gx: i32, gy: i32, v: bool) {
    let b = editor.brush;
    let w = editor.w;
    let mirror = editor.mirror;
    for dy in -b..=b {
        for dx in -b..=b {
            if dx * dx + dy * dy > b * b {
                continue;
            }
            for &x in &[gx + dx, if mirror { w - 1 - (gx + dx) } else { gx + dx }] {
                let y = gy + dy;
                if x < 0 || x >= w || y < 0 || y >= GH {
                    continue;
                }
                let i = (y * w + x) as usize;
                if editor.mask[i] != v {
                    editor.mask[i] = v;
                    site.mask[i] = v;
                    site.grow_dirty(x, y);
                }
            }
        }
    }
}

fn stamp_line(editor: &mut Editor, site: &mut Site, x0: i32, y0: i32, x1: i32, y1: i32, v: bool) {
    let (dx, dy) = ((x1 - x0).abs(), -(y1 - y0).abs());
    let (sx, sy) = (if x0 < x1 { 1 } else { -1 }, if y0 < y1 { 1 } else { -1 });
    let (mut x, mut y, mut err) = (x0, y0, dx + dy);
    loop {
        stamp(editor, site, x, y, v);
        if x == x1 && y == y1 {
            break;
        }
        let e2 = 2 * err;
        if e2 >= dy {
            err += dy;
            x += sx;
        }
        if e2 <= dx {
            err += dx;
            y += sy;
        }
    }
}

/// Compiles the canvas + settings into a shareable, sanitized document.
fn compile_doc(editor: &Editor) -> LevelDoc {
    let mut doc = editor.doc.clone();
    doc.w = editor.w;
    doc.rects.clear();
    doc.holes.clear();
    doc.runs.clear();
    for y in 0..GH {
        let mut x = 0;
        while x < editor.w {
            if editor.mask[(y * editor.w + x) as usize] {
                let x0 = x;
                while x < editor.w && editor.mask[(y * editor.w + x) as usize] {
                    x += 1;
                }
                doc.runs.push([y, x0, x - x0]);
            } else {
                x += 1;
            }
        }
    }
    sanitize_doc(&mut doc);
    doc
}
