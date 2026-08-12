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

use crate::retro::{text, AMBER, CYAN, DIM, GREEN, MAGENTA, RED, WHITE};
use crate::rng::Rng;
use crate::shell::{sfx, stat};
use crate::{FinalScore, GameTag, Phase};

pub const BLURB: &[&str] = &[
    "THE BOOKS DON'T SLEEP. NEITHER DO YOU.",
    "WASD WALKS / ARROWS TURN / SPACE DARTS / E INTERACTS",
    "LIFT THE FILES. BUG THE SERVER. WALK OUT.",
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
        app.add_systems(OnEnter(Phase::Playing), setup).add_systems(
            Update,
            (player_move, interact, fire, guards_think, render_view, hud, endgame)
                .chain()
                .run_if(in_state(Phase::Playing))
                .run_if(crate::unpaused),
        );
    }
}

fn setup(mut commands: Commands) {
    let mut grid = vec![Cell::Open; MW * MH];
    let mut guards = Vec::new();
    let mut pickups = Vec::new();
    let (mut px, mut py) = (1.5f32, 1.5f32);
    let mut server_cell = (0usize, 0usize);
    let mut exit_cell = (0usize, 0usize);
    let mut files_total = 0u32;
    for (y, row) in MAP.iter().enumerate() {
        for (x, ch) in row.chars().enumerate() {
            let fx = x as f32 + 0.5;
            let fy = y as f32 + 0.5;
            grid[y * MW + x] = match ch {
                '#' => Cell::Wall(1),
                'W' => Cell::Wall(2),
                'S' => Cell::Wall(3),
                'D' => Cell::Wall(4),
                'X' => {
                    exit_cell = (x, y);
                    Cell::Wall(5)
                }
                'p' => {
                    px = fx;
                    py = fy;
                    Cell::Open
                }
                'g' => {
                    guards.push(Guard {
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
                    files_total += 1;
                    pickups.push(Pickup { x: fx, y: fy, kind: PickupKind::File, taken: false });
                    Cell::Open
                }
                'a' => {
                    pickups.push(Pickup { x: fx, y: fy, kind: PickupKind::Darts, taken: false });
                    Cell::Open
                }
                'c' => {
                    pickups.push(Pickup { x: fx, y: fy, kind: PickupKind::Coffee, taken: false });
                    Cell::Open
                }
                'v' => {
                    server_cell = (x, y);
                    Cell::Open
                }
                _ => Cell::Open, // '.', 'd' and friends are open floor
            };
        }
    }
    commands.insert_resource(Mission {
        grid,
        doors_open: vec![false; MW * MH],
        px,
        py,
        ang: 0.0,
        hp: 100,
        darts: 24,
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
    if m.over.is_some() {
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
    if m.over.is_some() {
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
            return;
        }
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
) {
    if m.over.is_some() {
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
    stat("darts_fired", 1);
    sfx("fire");
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
    if m.over.is_some() {
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
    for p in pickups.0.iter().filter(|p| !p.taken) {
        let color = match p.kind {
            PickupKind::File => AMBER,
            PickupKind::Darts => CYAN,
            PickupKind::Coffee => MAGENTA,
        };
        items.push((0.0, Bill { x: p.x, y: p.y, w: 0.28, h: 0.3, color, dy: -0.3 }));
    }
    // The server console tile glows until bugged — the "go here" beacon.
    if !m.server_bugged {
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
    // Veils: damage red, muzzle white-ish.
    if let Ok(mut sp) = veil.single_mut() {
        let a = (m.hurt * 0.9).min(0.35) + if m.flash > 0.0 { 0.10 } else { 0.0 };
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
        } else {
            format!("HEALTH {}   DARTS {}   {}", m.hp.max(0), m.darts, fmt_clock(m.clock))
        };
        if t.0 != s {
            t.0 = s;
        }
    }
    if let Ok(mut t) = obj.single_mut() {
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
    mut final_score: ResMut<FinalScore>,
    mut next: ResMut<NextState<Phase>>,
) {
    if let Some(t) = m.over.as_mut() {
        if t.tick(time.delta()).finished() {
            final_score.0 = m.score;
            next.set(Phase::GameOver);
        }
    }
}
