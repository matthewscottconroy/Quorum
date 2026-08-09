//! POWDER KEG — up to twelve rivals in a cellar full of crates and kegs.
//! Kegs blast in a cross, crates hide upgrades, the walls close in at the
//! end. One or two humans share the keyboard; bots fill the remaining seats.

use bevy::prelude::*;
use serde::{Deserialize, Serialize};

use crate::retro::{text, PLAYER_COLORS, AMBER, RED, WHITE};
use crate::rng::Rng;
use crate::shell::{net_send, sfx};
use crate::{CabinetConfig, FinalScore, GameTag, NetIn, NetMode, Phase};

pub const BLURB: &[&str] = &[
    "LAST ONE STANDING KEEPS THE CELLAR.",
    "P1: WASD + SPACE   P2: ARROWS + ENTER",
    "CRATES HIDE UPGRADES. WALLS CLOSE IN.",
];

const COLS: i32 = 19;
const ROWS: i32 = 13;
const CELL: f32 = 34.0;
const Y_OFF: f32 = -34.0; // shift the arena down to leave HUD room
const FUSE: f32 = 2.4;
const FLAME_SECS: f32 = 0.45;
const SUDDEN_DEATH_AT: f32 = 75.0;

#[derive(Clone, Copy, PartialEq, Eq)]
enum Tile {
    Empty,
    Solid,
    Crate,
}

// ---- network wire formats (host-authoritative) ----
// Guests send inputs; the host runs the one true simulation and broadcasts
// compact snapshots ~20x a second. Rendering everywhere, physics in one place.

#[derive(Serialize, Deserialize)]
struct WireInput {
    t: String, // "in"
    d: [i32; 2],
    #[serde(default)]
    b: bool,
}

#[derive(Serialize, Deserialize)]
struct WireFighter(u8, i32, i32, i32, i32, u8, u8, u32); // seat,cx,cy,dx,dy,progress%,alive,kills

#[derive(Serialize, Deserialize)]
struct WireState {
    t: String, // "st"
    tiles: String,
    f: Vec<WireFighter>,
    b: Vec<(u16, u16)>, // cell, fuse centiseconds
    fl: Vec<u16>,
    p: Vec<(u16, u8)>,
    clk: u32, // deciseconds
}

#[derive(Serialize, Deserialize)]
struct WireEnd {
    t: String, // "end"
    ranks: Vec<(u8, u8)>,
    kills: Vec<(u8, u32)>,
    alive: Vec<u8>,
}

fn net_guest(net: &NetMode) -> bool {
    matches!(&net.0, Some(cfg) if !cfg.is_host())
}

/// Guest-side entity pools for snapshot-driven effects.
#[derive(Resource, Default)]
struct GuestFx {
    bombs: std::collections::HashMap<usize, Entity>,
    flames: std::collections::HashMap<usize, Entity>,
    perks: std::collections::HashMap<usize, Entity>,
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum Perk {
    Range,
    Bombs,
    Speed,
    Kick, // walk into a keg to send it sliding
}

#[derive(Resource)]
struct Arena {
    tiles: Vec<Tile>,
    bombs: Vec<Option<(Entity, f32, i32, usize)>>, // fuse left, range, owner
    flames: Vec<f32>,                              // seconds of flame left per cell
    flame_owner: Vec<usize>,
    perks: Vec<Option<(Perk, Entity)>>,
    clock: f32,
    spiral: Vec<usize>, // sudden-death closing order
    spiral_next: usize,
    spiral_timer: Timer,
    finished: bool,
    finish_timer: Timer,
    p1_score: u32,
    death_order: Vec<usize>,
    net_timer: Timer, // host snapshot cadence
    end_sent: bool,
    /// Once the walls stop, this fuse burns toward a declared draw so two
    /// cowards in the final chamber cannot stall the cabinet forever.
    settle: Timer,
}

impl Arena {
    fn idx(c: i32, r: i32) -> usize {
        (r * COLS + c) as usize
    }
    fn open(&self, c: i32, r: i32) -> bool {
        if c < 0 || c >= COLS || r < 0 || r >= ROWS {
            return false;
        }
        self.tiles[Self::idx(c, r)] == Tile::Empty && self.bombs[Self::idx(c, r)].is_none()
    }
}

fn world(c: i32, r: i32) -> Vec2 {
    Vec2::new(
        (c as f32 - (COLS as f32 - 1.0) / 2.0) * CELL,
        ((ROWS as f32 - 1.0) / 2.0 - r as f32) * CELL + Y_OFF,
    )
}

#[derive(Component)]
struct Fighter {
    seat: usize,
    human: Option<u8>, // 0 = P1, 1 = P2 (local keyboards)
    remote: bool,      // a live human elsewhere on the network (host view)
    tile: IVec2,
    dir: IVec2,
    want: IVec2,
    progress: f32,
    speed: f32, // tiles per second
    range: i32,
    max_bombs: i32,
    live_bombs: i32,
    alive: bool,
    kills: u32,
    wants_bomb: bool,
    kick: bool,
    think: Timer,
}

#[derive(Component)]
struct BombSprite; // visual for a ticking keg

#[derive(Component)]
struct FlameSprite {
    ttl: Timer,
}

#[derive(Component)]
struct TileSprite(usize);

#[derive(Component)]
struct Hud;

pub struct PowderPlugin;

impl Plugin for PowderPlugin {
    fn build(&self, app: &mut App) {
        app.add_systems(OnEnter(Phase::Playing), setup).add_systems(
            Update,
            (
                host_net_input,
                human_input,
                bot_brains,
                movement,
                bombs_and_flames,
                pickups,
                closing_walls,
                host_broadcast,
                finish,
                guest_input,
                guest_apply,
                guest_smooth,
                hud,
            )
                .chain()
                .run_if(in_state(Phase::Playing))
                .run_if(crate::unpaused),
        );
    }
}

/// Twelve spawn cells spread around the rim, each guaranteed breathing room.
const SPAWNS: [(i32, i32); 12] = [
    (1, 1),
    (17, 11),
    (17, 1),
    (1, 11),
    (9, 1),
    (9, 11),
    (1, 6),
    (17, 6),
    (5, 1),
    (13, 11),
    (13, 1),
    (5, 11),
];

fn setup(mut commands: Commands, mut rng: ResMut<Rng>, config: Res<CabinetConfig>, net: Res<NetMode>) {
    let players = config.players.clamp(2, 12) as usize;
    let humans = if net.0.is_some() { 1 } else { config.humans.clamp(1, 2.min(players as u32)) as usize };

    let mut tiles = vec![Tile::Empty; (COLS * ROWS) as usize];
    // Border ring + classic pillar lattice.
    for r in 0..ROWS {
        for c in 0..COLS {
            if r == 0 || r == ROWS - 1 || c == 0 || c == COLS - 1 || (r % 2 == 0 && c % 2 == 0) {
                tiles[Arena::idx(c, r)] = Tile::Solid;
            }
        }
    }
    // Crates everywhere else, except spawn pockets.
    for r in 1..ROWS - 1 {
        for c in 1..COLS - 1 {
            if tiles[Arena::idx(c, r)] == Tile::Empty && rng.chance(0.62) {
                tiles[Arena::idx(c, r)] = Tile::Crate;
            }
        }
    }
    for &(sc, sr) in SPAWNS.iter().take(players) {
        for (dc, dr) in [(0, 0), (1, 0), (-1, 0), (0, 1), (0, -1)] {
            let (c, r) = (sc + dc, sr + dr);
            if c > 0 && c < COLS - 1 && r > 0 && r < ROWS - 1 && tiles[Arena::idx(c, r)] == Tile::Crate {
                tiles[Arena::idx(c, r)] = Tile::Empty;
            }
        }
    }

    // Sudden-death spiral: perimeter-inward walk over the inner field.
    let mut spiral = Vec::new();
    let (mut top, mut bottom, mut left, mut right) = (1, ROWS - 2, 1, COLS - 2);
    while top <= bottom && left <= right {
        for c in left..=right {
            spiral.push(Arena::idx(c, top));
        }
        for r in top + 1..=bottom {
            spiral.push(Arena::idx(right, r));
        }
        if bottom > top {
            for c in (left..right).rev() {
                spiral.push(Arena::idx(c, bottom));
            }
        }
        if right > left {
            for r in (top + 1..bottom).rev() {
                spiral.push(Arena::idx(left, r));
            }
        }
        top += 1;
        bottom -= 1;
        left += 1;
        right -= 1;
    }

    // Tile sprites (one per cell, recolored as the arena changes).
    for r in 0..ROWS {
        for c in 0..COLS {
            let i = Arena::idx(c, r);
            let color = match tiles[i] {
                Tile::Solid => Color::srgb(0.16, 0.16, 0.24),
                Tile::Crate => Color::srgb(0.45, 0.30, 0.13),
                Tile::Empty => Color::srgb(0.05, 0.05, 0.09),
            };
            let p = world(c, r);
            commands.spawn((
                Sprite { color, custom_size: Some(Vec2::splat(CELL - 2.0)), ..default() },
                Transform::from_xyz(p.x, p.y, 1.0),
                TileSprite(i),
                GameTag,
            ));
        }
    }

    let n = (COLS * ROWS) as usize;
    commands.insert_resource(Arena {
        tiles,
        bombs: vec![None; n],
        flames: vec![0.0; n],
        flame_owner: vec![usize::MAX; n],
        perks: vec![None; n],
        clock: 0.0,
        spiral,
        spiral_next: 0,
        spiral_timer: Timer::from_seconds(0.32, TimerMode::Repeating),
        finished: false,
        finish_timer: Timer::from_seconds(2.0, TimerMode::Once),
        p1_score: 0,
        death_order: Vec::new(),
        net_timer: Timer::from_seconds(0.05, TimerMode::Repeating),
        end_sent: false,
        settle: Timer::from_seconds(20.0, TimerMode::Once),
    });
    commands.init_resource::<GuestFx>();

    for seat in 0..players {
        let (sc, sr) = SPAWNS[seat];
        // Local: first `humans` seats are keyboards. Online: the host's own
        // seat is its keyboard; other present seats are remote humans.
        let (human, remote) = match &net.0 {
            Some(cfg) => (
                if seat == cfg.seat as usize { Some(0u8) } else { None },
                seat != cfg.seat as usize && cfg.present.get(seat).copied().unwrap_or(false),
            ),
            None => (if seat < humans { Some(seat as u8) } else { None }, false),
        };
        let p = world(sc, sr);
        commands
            .spawn((
                Fighter {
                    seat,
                    human,
                    remote,
                    tile: IVec2::new(sc, sr),
                    dir: IVec2::ZERO,
                    want: IVec2::ZERO,
                    progress: 0.0,
                    speed: 3.4,
                    range: 2,
                    max_bombs: 1,
                    live_bombs: 0,
                    alive: true,
                    kills: 0,
                    wants_bomb: false,
                    kick: false,
                    think: Timer::from_seconds(0.12 + 0.013 * seat as f32, TimerMode::Repeating),
                },
                Sprite {
                    color: PLAYER_COLORS[seat % 12],
                    custom_size: Some(Vec2::splat(CELL - 10.0)),
                    ..default()
                },
                Transform::from_xyz(p.x, p.y, 6.0),
                GameTag,
            ))
            .with_children(|kid| {
                if human.is_some() {
                    kid.spawn((
                        Sprite { color: WHITE, custom_size: Some(Vec2::new(8.0, 3.0)), ..default() },
                        Transform::from_xyz(0.0, CELL * 0.42, 0.1),
                    ));
                }
            });
    }

    let hud = text(&mut commands, "", 22.0, WHITE, Vec3::new(0.0, 292.0, 8.0));
    commands.entity(hud).insert((Hud, GameTag));
}

fn human_input(keys: Res<ButtonInput<KeyCode>>, net: Res<NetMode>, mut fighters: Query<&mut Fighter>) {
    if net_guest(&net) {
        return;
    }
    // Online the host is the only local keyboard, so both key sets steer it.
    let both_sets = net.0.is_some();
    for mut f in &mut fighters {
        let Some(h) = f.human else { continue };
        if !f.alive {
            continue;
        }
        let p1 = both_sets || h == 0;
        let p2 = both_sets || h != 0;
        let hit = |a: KeyCode, b: KeyCode| (p1 && keys.pressed(a)) || (p2 && keys.pressed(b));
        f.want = if hit(KeyCode::KeyA, KeyCode::ArrowLeft) {
            IVec2::new(-1, 0)
        } else if hit(KeyCode::KeyD, KeyCode::ArrowRight) {
            IVec2::new(1, 0)
        } else if hit(KeyCode::KeyW, KeyCode::ArrowUp) {
            IVec2::new(0, -1)
        } else if hit(KeyCode::KeyS, KeyCode::ArrowDown) {
            IVec2::new(0, 1)
        } else {
            IVec2::ZERO
        };
        let bomb_hit = (p1 && keys.just_pressed(KeyCode::Space)) || (p2 && keys.just_pressed(KeyCode::Enter));
        if bomb_hit {
            f.wants_bomb = true;
        }
    }
}

/// Cells currently dangerous: flame now, or bomb blast arriving within `soon`.
fn danger_map(arena: &Arena, soon: f32) -> Vec<bool> {
    let n = (COLS * ROWS) as usize;
    let mut danger = vec![false; n];
    for i in 0..n {
        if arena.flames[i] > 0.0 {
            danger[i] = true;
        }
    }
    for (i, b) in arena.bombs.iter().enumerate() {
        let Some((_, fuse, range, _)) = b else { continue };
        if *fuse > soon {
            continue;
        }
        let (c0, r0) = ((i as i32) % COLS, (i as i32) / COLS);
        danger[i] = true;
        for (dc, dr) in [(1, 0), (-1, 0), (0, 1), (0, -1)] {
            for k in 1..=*range {
                let (c, r) = (c0 + dc * k, r0 + dr * k);
                if c < 0 || c >= COLS || r < 0 || r >= ROWS {
                    break;
                }
                let j = Arena::idx(c, r);
                if arena.tiles[j] == Tile::Solid {
                    break;
                }
                danger[j] = true;
                if arena.tiles[j] == Tile::Crate {
                    break;
                }
            }
        }
    }
    danger
}

/// BFS from `from` to the nearest cell satisfying `goal`; returns first step.
fn bfs_step(
    arena: &Arena,
    danger: &[bool],
    from: IVec2,
    allow_danger_transit: bool,
    goal: impl Fn(usize) -> bool,
) -> Option<IVec2> {
    let n = (COLS * ROWS) as usize;
    let start = Arena::idx(from.x, from.y);
    let mut prev = vec![usize::MAX; n];
    let mut queue = std::collections::VecDeque::new();
    prev[start] = start;
    queue.push_back(start);
    while let Some(cur) = queue.pop_front() {
        if goal(cur) && cur != start {
            // Walk back to the first step.
            let mut step = cur;
            while prev[step] != start {
                step = prev[step];
            }
            let (c, r) = ((step as i32) % COLS, (step as i32) / COLS);
            return Some(IVec2::new(c - from.x, r - from.y));
        }
        let (c0, r0) = ((cur as i32) % COLS, (cur as i32) / COLS);
        for (dc, dr) in [(1, 0), (-1, 0), (0, 1), (0, -1)] {
            let (c, r) = (c0 + dc, r0 + dr);
            if c < 0 || c >= COLS || r < 0 || r >= ROWS {
                continue;
            }
            let j = Arena::idx(c, r);
            if prev[j] != usize::MAX {
                continue;
            }
            if arena.tiles[j] != Tile::Empty || arena.bombs[j].is_some() {
                continue;
            }
            if !allow_danger_transit && danger[j] {
                continue;
            }
            prev[j] = cur;
            queue.push_back(j);
        }
    }
    None
}

fn bot_brains(
    time: Res<Time>,
    arena: Res<Arena>,
    net: Res<NetMode>,
    mut rng: ResMut<Rng>,
    mut fighters: Query<(&mut Fighter, &Transform)>,
) {
    if net_guest(&net) {
        return;
    }
    // Snapshot enemy tiles for targeting.
    let positions: Vec<(usize, IVec2, bool)> =
        fighters.iter().map(|(f, _)| (f.seat, f.tile, f.alive)).collect();
    let danger = danger_map(&arena, FUSE + 0.1);

    for (mut f, _) in &mut fighters {
        if f.human.is_some() || f.remote || !f.alive {
            continue;
        }
        if !f.think.tick(time.delta()).just_finished() {
            continue;
        }
        let here = Arena::idx(f.tile.x, f.tile.y);

        // 1. In danger: sprint to the nearest safe cell, danger transit allowed.
        if danger[here] {
            if let Some(step) = bfs_step(&arena, &danger, f.tile, true, |i| !danger[i]) {
                f.want = step;
            }
            f.wants_bomb = false;
            continue;
        }
        // 2. Adjacent crate or enemy, with an escape route? Drop a keg.
        let mut worth_bombing = false;
        for (dc, dr) in [(1, 0), (-1, 0), (0, 1), (0, -1)] {
            for k in 1..=f.range {
                let (c, r) = (f.tile.x + dc * k, f.tile.y + dr * k);
                if c < 0 || c >= COLS || r < 0 || r >= ROWS {
                    break;
                }
                let j = Arena::idx(c, r);
                if arena.tiles[j] == Tile::Solid {
                    break;
                }
                if arena.tiles[j] == Tile::Crate {
                    worth_bombing = true;
                    break;
                }
                if positions.iter().any(|&(s, t, alive)| alive && s != f.seat && t == IVec2::new(c, r)) {
                    worth_bombing = true;
                    break;
                }
            }
        }
        if worth_bombing && f.live_bombs < f.max_bombs {
            // Simulate our own blast: is there still a safe cell to reach?
            let mut sim = vec![false; danger.len()];
            sim.copy_from_slice(&danger);
            sim[here] = true;
            for (dc, dr) in [(1, 0), (-1, 0), (0, 1), (0, -1)] {
                for k in 1..=f.range {
                    let (c, r) = (f.tile.x + dc * k, f.tile.y + dr * k);
                    if c < 0 || c >= COLS || r < 0 || r >= ROWS {
                        break;
                    }
                    let j = Arena::idx(c, r);
                    if arena.tiles[j] == Tile::Solid {
                        break;
                    }
                    sim[j] = true;
                    if arena.tiles[j] == Tile::Crate {
                        break;
                    }
                }
            }
            if bfs_step(&arena, &sim, f.tile, true, |i| !sim[i]).is_some() {
                f.wants_bomb = true;
                continue;
            }
        }
        // 3. Otherwise walk toward loot, then toward the nearest rival.
        let target_step = bfs_step(&arena, &danger, f.tile, false, |i| arena.perks[i].is_some())
            .or_else(|| {
                bfs_step(&arena, &danger, f.tile, false, |i| {
                    let cell = IVec2::new((i as i32) % COLS, (i as i32) / COLS);
                    positions.iter().any(|&(s, t, alive)| {
                        alive && s != f.seat && (t - cell).abs().element_sum() <= 1
                    })
                })
            });
        if let Some(step) = target_step {
            f.want = step;
        } else if rng.chance(0.3) {
            let dirs = [IVec2::new(1, 0), IVec2::new(-1, 0), IVec2::new(0, 1), IVec2::new(0, -1)];
            f.want = dirs[rng.range(4) as usize];
        }
    }
}

/// Slides a kicked keg from `from` along `dir` until something stops it:
/// walls, crates, another keg, or a fighter standing in the lane.
fn slide_bomb(
    arena: &mut Arena,
    bomb_tfs: &mut Query<&mut Transform, (With<BombSprite>, Without<Fighter>)>,
    occupied: &[IVec2],
    from: usize,
    dir: IVec2,
) -> bool {
    let (mut c, mut r) = ((from as i32) % COLS, (from as i32) / COLS);
    let mut moved = false;
    loop {
        let (nc, nr) = (c + dir.x, r + dir.y);
        if nc < 0 || nc >= COLS || nr < 0 || nr >= ROWS {
            break;
        }
        let j = Arena::idx(nc, nr);
        if arena.tiles[j] != Tile::Empty
            || arena.bombs[j].is_some()
            || arena.flames[j] > 0.0
            || occupied.contains(&IVec2::new(nc, nr))
        {
            break;
        }
        c = nc;
        r = nr;
        moved = true;
    }
    if moved {
        let to = Arena::idx(c, r);
        let bomb = arena.bombs[from].take();
        if let Some((e, fuse, range, owner)) = bomb {
            let p = world(c, r);
            if let Ok(mut tf) = bomb_tfs.get_mut(e) {
                tf.translation.x = p.x;
                tf.translation.y = p.y;
            }
            arena.bombs[to] = Some((e, fuse, range, owner));
        }
        sfx("drop");
    }
    moved
}

#[allow(clippy::type_complexity)]
fn movement(
    time: Res<Time>,
    mut arena: ResMut<Arena>,
    net: Res<NetMode>,
    mut fighters: Query<(&mut Fighter, &mut Transform), Without<BombSprite>>,
    mut bomb_tfs: Query<&mut Transform, (With<BombSprite>, Without<Fighter>)>,
) {
    if net_guest(&net) {
        return;
    }
    // Kick pass: a fighter with the perk shoves the keg in their way.
    let occupied: Vec<IVec2> = fighters.iter().filter(|(f, _)| f.alive).map(|(f, _)| f.tile).collect();
    for (f, _) in &fighters {
        if !f.alive || !f.kick || f.want == IVec2::ZERO {
            continue;
        }
        if f.dir != IVec2::ZERO && f.progress > f32::EPSILON {
            continue; // only shove from a standstill at a tile center
        }
        let target = f.tile + f.want;
        if target.x < 0 || target.x >= COLS || target.y < 0 || target.y >= ROWS {
            continue;
        }
        let j = Arena::idx(target.x, target.y);
        if arena.bombs[j].is_some() {
            slide_bomb(&mut arena, &mut bomb_tfs, &occupied, j, f.want);
        }
    }
    let dt = time.delta_secs();
    for (mut f, mut tf) in &mut fighters {
        if !f.alive {
            continue;
        }
        let mut remaining = f.speed * dt;
        while remaining > 0.0 {
            if f.dir == IVec2::ZERO {
                if f.want != IVec2::ZERO && arena.open(f.tile.x + f.want.x, f.tile.y + f.want.y) {
                    f.dir = f.want;
                } else {
                    break;
                }
            }
            let step = remaining.min(1.0 - f.progress);
            f.progress += step;
            remaining -= step;
            if f.progress >= 1.0 - f32::EPSILON {
                let d = f.dir;
                f.tile += d;
                f.progress = 0.0;
                let turn_ok = f.want != IVec2::ZERO && arena.open(f.tile.x + f.want.x, f.tile.y + f.want.y);
                if turn_ok {
                    f.dir = f.want;
                } else if !arena.open(f.tile.x + f.dir.x, f.tile.y + f.dir.y) {
                    f.dir = IVec2::ZERO;
                }
            }
        }
        let from = world(f.tile.x, f.tile.y);
        let to = world(f.tile.x + f.dir.x, f.tile.y + f.dir.y);
        let p = from.lerp(to, f.progress);
        tf.translation.x = p.x;
        tf.translation.y = p.y;
    }
}

#[allow(clippy::too_many_arguments, clippy::type_complexity)]
fn bombs_and_flames(
    time: Res<Time>,
    mut commands: Commands,
    mut arena: ResMut<Arena>,
    net: Res<NetMode>,
    mut rng: ResMut<Rng>,
    mut fighters: Query<(&mut Fighter, &mut Sprite), (Without<TileSprite>, Without<FlameSprite>)>,
    mut tiles_q: Query<(&TileSprite, &mut Sprite), Without<FlameSprite>>,
    mut flames_q: Query<(Entity, &mut FlameSprite, &mut Sprite), (Without<Fighter>, Without<TileSprite>)>,
) {
    if net_guest(&net) {
        return;
    }
    let dt = time.delta_secs();
    arena.clock += dt;

    // Place requested bombs.
    for (mut f, _) in &mut fighters {
        if !f.alive || !f.wants_bomb {
            f.wants_bomb = false;
            continue;
        }
        f.wants_bomb = false;
        let i = Arena::idx(f.tile.x, f.tile.y);
        if f.live_bombs < f.max_bombs && arena.bombs[i].is_none() && arena.tiles[i] == Tile::Empty {
            let p = world(f.tile.x, f.tile.y);
            let e = commands
                .spawn((
                    Sprite { color: Color::srgb(0.25, 0.25, 0.3), custom_size: Some(Vec2::splat(CELL - 8.0)), ..default() },
                    Transform::from_xyz(p.x, p.y, 4.0),
                    BombSprite,
                    GameTag,
                ))
                .id();
            arena.bombs[i] = Some((e, FUSE, f.range, f.seat));
            f.live_bombs += 1;
            sfx("place");
        }
    }

    // Tick fuses; collect exploding cells. Chained kegs blame the player
    // whose keg STARTED the chain — the classic credit rule.
    let mut exploding: Vec<(usize, Option<usize>)> = Vec::new(); // cell, initiator
    for i in 0..arena.bombs.len() {
        if let Some((_, fuse, _, _)) = arena.bombs[i].as_mut() {
            *fuse -= dt;
            if *fuse <= 0.0 {
                exploding.push((i, None));
            }
        }
    }
    if !exploding.is_empty() {
        sfx("boom");
    }
    let mut burst: Vec<(usize, i32, usize)> = Vec::new(); // cell, range, credited seat
    while let Some((i, initiator)) = exploding.pop() {
        let Some((e, _, range, owner)) = arena.bombs[i].take() else { continue };
        commands.entity(e).despawn();
        // Give the owner their slot back.
        for (mut f, _) in &mut fighters {
            if f.seat == owner {
                f.live_bombs = (f.live_bombs - 1).max(0);
            }
        }
        let credited = initiator.unwrap_or(owner);
        burst.push((i, range, credited));
        // Chain: any bomb in this blast goes off now, on the initiator's tab.
        let (c0, r0) = ((i as i32) % COLS, (i as i32) / COLS);
        for (dc, dr) in [(1, 0), (-1, 0), (0, 1), (0, -1)] {
            for k in 1..=range {
                let (c, r) = (c0 + dc * k, r0 + dr * k);
                if c < 0 || c >= COLS || r < 0 || r >= ROWS {
                    break;
                }
                let j = Arena::idx(c, r);
                if arena.tiles[j] == Tile::Solid {
                    break;
                }
                if arena.bombs[j].is_some() && !exploding.iter().any(|&(cell, _)| cell == j) {
                    exploding.push((j, Some(credited)));
                }
                if arena.tiles[j] == Tile::Crate {
                    break;
                }
            }
        }
    }
    // Apply bursts: flames, crate destruction, perk reveals.
    for (i, range, owner) in burst {
        let (c0, r0) = ((i as i32) % COLS, (i as i32) / COLS);
        let lay_flame = |arena: &mut Arena, commands: &mut Commands, c: i32, r: i32, dir: (i32, i32)| {
            let j = Arena::idx(c, r);
            arena.flames[j] = FLAME_SECS;
            arena.flame_owner[j] = owner;
            if let Some((_, e)) = arena.perks[j].take() {
                commands.entity(e).despawn(); // flames eat loose perks
            }
            // Cross-shaped blast: a fat core, slim arms along the blast axis.
            let size = match dir {
                (0, 0) => Vec2::splat(CELL - 6.0),
                (_, 0) => Vec2::new(CELL - 2.0, CELL * 0.45),
                _ => Vec2::new(CELL * 0.45, CELL - 2.0),
            };
            let p = world(c, r);
            commands.spawn((
                Sprite { color: AMBER, custom_size: Some(size), ..default() },
                Transform::from_xyz(p.x, p.y, 5.0),
                FlameSprite { ttl: Timer::from_seconds(FLAME_SECS, TimerMode::Once) },
                GameTag,
            ));
        };
        lay_flame(&mut arena, &mut commands, c0, r0, (0, 0));
        for (dc, dr) in [(1, 0), (-1, 0), (0, 1), (0, -1)] {
            for k in 1..=range {
                let (c, r) = (c0 + dc * k, r0 + dr * k);
                if c < 0 || c >= COLS || r < 0 || r >= ROWS {
                    break;
                }
                let j = Arena::idx(c, r);
                if arena.tiles[j] == Tile::Solid {
                    break;
                }
                let was_crate = arena.tiles[j] == Tile::Crate;
                if was_crate {
                    arena.tiles[j] = Tile::Empty;
                    // A third of crates hide an upgrade.
                    if rng.chance(0.34) {
                        let perk = match rng.range(7) {
                            0 | 1 => Perk::Range,
                            2 | 3 => Perk::Bombs,
                            4 | 5 => Perk::Speed,
                            _ => Perk::Kick,
                        };
                        let p = world(c, r);
                        let color = match perk {
                            Perk::Range => RED,
                            Perk::Bombs => Color::srgb(0.3, 0.6, 1.0),
                            Perk::Speed => Color::srgb(0.55, 1.0, 0.3),
                            Perk::Kick => WHITE,
                        };
                        let e = commands
                            .spawn((
                                Sprite { color, custom_size: Some(Vec2::splat(CELL * 0.45)), ..default() },
                                Transform::from_xyz(p.x, p.y, 3.0),
                                GameTag,
                            ))
                            .id();
                        arena.perks[j] = Some((perk, e));
                    }
                }
                lay_flame(&mut arena, &mut commands, c, r, (dc, dr));
                if was_crate {
                    break;
                }
            }
        }
    }
    // Refresh tile colors (crates burn away).
    for (ts, mut sprite) in &mut tiles_q {
        let want = match arena.tiles[ts.0] {
            Tile::Solid => Color::srgb(0.16, 0.16, 0.24),
            Tile::Crate => Color::srgb(0.45, 0.30, 0.13),
            Tile::Empty => Color::srgb(0.05, 0.05, 0.09),
        };
        if sprite.color != want {
            sprite.color = want;
        }
    }
    // Flame timers.
    for i in 0..arena.flames.len() {
        if arena.flames[i] > 0.0 {
            arena.flames[i] -= dt;
        }
    }
    for (e, mut fs, mut sprite) in &mut flames_q {
        if fs.ttl.tick(time.delta()).finished() {
            commands.entity(e).despawn();
        } else {
            sprite.color.set_alpha(0.35 + 0.65 * fs.ttl.fraction_remaining());
        }
    }

    // Flames kill.
    let mut kill_credit: Vec<(usize, usize)> = Vec::new(); // victim, owner
    for (mut f, mut sprite) in &mut fighters {
        if !f.alive {
            continue;
        }
        let i = Arena::idx(f.tile.x, f.tile.y);
        // Also check the cell being entered when mid-step.
        let j = if f.dir != IVec2::ZERO && f.progress > 0.45 {
            Arena::idx(f.tile.x + f.dir.x, f.tile.y + f.dir.y)
        } else {
            i
        };
        let hit = if arena.flames[i] > 0.0 {
            Some(arena.flame_owner[i])
        } else if arena.flames[j] > 0.0 {
            Some(arena.flame_owner[j])
        } else {
            None
        };
        if let Some(owner) = hit {
            f.alive = false;
            sprite.color.set_alpha(0.15);
            sfx("death");
            arena.death_order.push(f.seat);
            if owner != f.seat && owner != usize::MAX {
                kill_credit.push((f.seat, owner));
            }
        }
    }
    for (_victim, owner) in kill_credit {
        for (mut f, _) in &mut fighters {
            if f.seat == owner && f.alive {
                f.kills += 1;
            }
        }
    }
}

fn pickups(
    mut commands: Commands,
    mut arena: ResMut<Arena>,
    net: Res<NetMode>,
    mut fighters: Query<&mut Fighter>,
) {
    if net_guest(&net) {
        return;
    }
    for mut f in &mut fighters {
        if !f.alive {
            continue;
        }
        let i = Arena::idx(f.tile.x, f.tile.y);
        if let Some((perk, e)) = arena.perks[i].take() {
            commands.entity(e).despawn();
            match perk {
                Perk::Range => f.range = (f.range + 1).min(7),
                Perk::Bombs => f.max_bombs = (f.max_bombs + 1).min(6),
                Perk::Speed => f.speed = (f.speed + 0.45).min(6.5),
                Perk::Kick => f.kick = true,
            }
            sfx("power");
        }
    }
}

fn closing_walls(
    time: Res<Time>,
    mut commands: Commands,
    mut arena: ResMut<Arena>,
    net: Res<NetMode>,
    mut fighters: Query<(&mut Fighter, &mut Sprite)>,
) {
    if net_guest(&net) {
        return;
    }
    if arena.clock < SUDDEN_DEATH_AT || arena.finished {
        return;
    }
    // Once the walls stop at the final chamber, a settle fuse starts: when
    // it burns out, everyone still standing goes down together (draw).
    if arena.spiral_next >= arena.spiral.len().saturating_sub(9) {
        if arena.settle.tick(time.delta()).just_finished() {
            for (mut f, mut sprite) in &mut fighters {
                if f.alive {
                    f.alive = false;
                    sprite.color.set_alpha(0.15);
                    let seat = f.seat;
                    arena.death_order.push(seat);
                }
            }
            sfx("death");
        }
        return;
    }
    if !arena.spiral_timer.tick(time.delta()).just_finished() {
        return;
    }
    let i = arena.spiral[arena.spiral_next];
    arena.spiral_next += 1;
    arena.tiles[i] = Tile::Solid;
    if let Some((e, _, _, owner)) = arena.bombs[i].take() {
        commands.entity(e).despawn();
        for (mut f, _) in &mut fighters {
            if f.seat == owner {
                f.live_bombs = (f.live_bombs - 1).max(0);
            }
        }
    }
    if let Some((_, e)) = arena.perks[i].take() {
        commands.entity(e).despawn();
    }
    let cell = IVec2::new((i as i32) % COLS, (i as i32) / COLS);
    for (mut f, mut sprite) in &mut fighters {
        if f.alive && f.tile == cell {
            f.alive = false;
            sprite.color.set_alpha(0.15);
            arena.death_order.push(f.seat);
        }
    }
}

fn seat_rank(arena: &Arena, total: usize, seat: usize, alive: bool) -> usize {
    if alive {
        1
    } else {
        // death_order position: earlier death = worse rank.
        let pos = arena.death_order.iter().position(|&s| s == seat).unwrap_or(0);
        total - pos
    }
}

fn placement_score(total: usize, rank: usize, kills: u32, alive: bool) -> u32 {
    kills * 100 + ((total.saturating_sub(rank)) as u32) * 50 + if alive { 300 } else { 0 }
}

fn finish(
    time: Res<Time>,
    mut arena: ResMut<Arena>,
    net: Res<NetMode>,
    mut final_score: ResMut<FinalScore>,
    mut next: ResMut<NextState<Phase>>,
    fighters: Query<&Fighter>,
    mut commands: Commands,
) {
    if net_guest(&net) {
        return;
    }
    let alive: Vec<&Fighter> = fighters.iter().filter(|f| f.alive).collect();
    let total = fighters.iter().count();
    if !arena.finished && alive.len() <= 1 {
        arena.finished = true;
        arena.finish_timer.reset();
        // The credited score is this machine's own seat (0 locally and for
        // the online host, whose seat is always 0).
        let me = fighters.iter().find(|f| f.seat == 0);
        let mut score = 0u32;
        if let Some(me) = me {
            let rank = seat_rank(&arena, total, 0, me.alive);
            score = placement_score(total, rank, me.kills, me.alive);
        }
        arena.p1_score = score;
        // Online: tell the guests how it ended, exactly once.
        if net.0.is_some() && !arena.end_sent {
            arena.end_sent = true;
            let end = WireEnd {
                t: "end".into(),
                ranks: fighters
                    .iter()
                    .map(|f| (f.seat as u8, seat_rank(&arena, total, f.seat, f.alive) as u8))
                    .collect(),
                kills: fighters.iter().map(|f| (f.seat as u8, f.kills)).collect(),
                alive: fighters.iter().filter(|f| f.alive).map(|f| f.seat as u8).collect(),
            };
            if let Ok(msg) = serde_json::to_string(&end) {
                net_send(&msg);
            }
        }
        let banner = match alive.first() {
            Some(w) => format!("SEAT {} TAKES THE CELLAR", w.seat + 1),
            None => "MUTUAL DESTRUCTION".to_string(),
        };
        let e = text(&mut commands, &banner, 34.0, AMBER, Vec3::new(0.0, 0.0, 20.0));
        commands.entity(e).insert(GameTag);
    }
    if arena.finished && arena.finish_timer.tick(time.delta()).finished() {
        final_score.0 = arena.p1_score;
        next.set(Phase::GameOver);
    }
}

fn hud(arena: Res<Arena>, fighters: Query<&Fighter>, mut hud: Query<&mut Text2d, With<Hud>>) {
    if let Ok(mut t) = hud.single_mut() {
        let alive = fighters.iter().filter(|f| f.alive).count();
        let to_walls = (SUDDEN_DEATH_AT - arena.clock).max(0.0);
        let s = if to_walls > 0.0 {
            format!("{alive} STANDING   WALLS IN {to_walls:.0}s")
        } else if arena.spiral_next >= arena.spiral.len().saturating_sub(9) {
            let settle = (arena.settle.duration().as_secs_f32() - arena.settle.elapsed_secs()).max(0.0);
            format!("{alive} STANDING   SETTLE IT IN {settle:.0}s")
        } else {
            format!("{alive} STANDING   THE WALLS ARE COMING")
        };
        if t.0 != s {
            t.0 = s;
        }
    }
}

// ---- network systems ----

/// Host: applies relayed guest inputs to their fighters, and converts a
/// departed human's fighter into a bot so the round keeps its shape.
fn host_net_input(
    mut events: EventReader<NetIn>,
    net: Res<NetMode>,
    mut fighters: Query<&mut Fighter>,
) {
    let Some(cfg) = &net.0 else {
        return; // local play: events drain in guest_apply's clear below
    };
    if !cfg.is_host() {
        return;
    }
    for ev in events.read() {
        for mut f in &mut fighters {
            if f.seat != ev.seat as usize {
                continue;
            }
            if ev.left {
                f.remote = false; // the bot brain takes the seat over
                continue;
            }
            let Ok(wire) = serde_json::from_str::<WireInput>(&ev.data) else { continue };
            if wire.t == "in" && f.remote && f.alive {
                f.want = IVec2::new(wire.d[0].clamp(-1, 1), wire.d[1].clamp(-1, 1));
                if wire.b {
                    f.wants_bomb = true;
                }
            }
        }
    }
}

/// Host: broadcasts the world ~20x/second. Rendering is everywhere,
/// simulation is here.
fn host_broadcast(
    time: Res<Time>,
    net: Res<NetMode>,
    mut arena: ResMut<Arena>,
    fighters: Query<&Fighter>,
) {
    let Some(cfg) = &net.0 else { return };
    if !cfg.is_host() || arena.finished {
        return;
    }
    if !arena.net_timer.tick(time.delta()).just_finished() {
        return;
    }
    let tiles: String = arena
        .tiles
        .iter()
        .map(|t| match t {
            Tile::Empty => '0',
            Tile::Solid => '1',
            Tile::Crate => '2',
        })
        .collect();
    let state = WireState {
        t: "st".into(),
        tiles,
        f: fighters
            .iter()
            .map(|f| {
                WireFighter(
                    f.seat as u8,
                    f.tile.x,
                    f.tile.y,
                    f.dir.x,
                    f.dir.y,
                    (f.progress * 100.0) as u8,
                    u8::from(f.alive),
                    f.kills,
                )
            })
            .collect(),
        b: arena
            .bombs
            .iter()
            .enumerate()
            .filter_map(|(i, b)| b.as_ref().map(|(_, fuse, _, _)| (i as u16, (fuse * 100.0).max(0.0) as u16)))
            .collect(),
        fl: arena
            .flames
            .iter()
            .enumerate()
            .filter_map(|(i, &f)| (f > 0.0).then_some(i as u16))
            .collect(),
        p: arena
            .perks
            .iter()
            .enumerate()
            .filter_map(|(i, p)| {
                p.as_ref().map(|(perk, _)| {
                    (i as u16, match perk {
                        Perk::Range => 0u8,
                        Perk::Bombs => 1,
                        Perk::Speed => 2,
                        Perk::Kick => 3,
                    })
                })
            })
            .collect(),
        clk: (arena.clock * 10.0) as u32,
    };
    if let Ok(msg) = serde_json::to_string(&state) {
        net_send(&msg);
    }
}

/// Guest: reads the local keyboard (either key set) and relays it to the host.
fn guest_input(
    keys: Res<ButtonInput<KeyCode>>,
    net: Res<NetMode>,
    mut last: Local<IVec2>,
) {
    if !net_guest(&net) {
        return;
    }
    let want = if keys.pressed(KeyCode::KeyA) || keys.pressed(KeyCode::ArrowLeft) {
        IVec2::new(-1, 0)
    } else if keys.pressed(KeyCode::KeyD) || keys.pressed(KeyCode::ArrowRight) {
        IVec2::new(1, 0)
    } else if keys.pressed(KeyCode::KeyW) || keys.pressed(KeyCode::ArrowUp) {
        IVec2::new(0, -1)
    } else if keys.pressed(KeyCode::KeyS) || keys.pressed(KeyCode::ArrowDown) {
        IVec2::new(0, 1)
    } else {
        IVec2::ZERO
    };
    let bomb = keys.just_pressed(KeyCode::Space) || keys.just_pressed(KeyCode::Enter);
    if want != *last || bomb {
        *last = want;
        let wire = WireInput { t: "in".into(), d: [want.x, want.y], b: bomb };
        if let Ok(msg) = serde_json::to_string(&wire) {
            net_send(&msg);
        }
    }
}

/// Guest: applies host snapshots to the local scene and handles the ending.
#[allow(clippy::too_many_arguments, clippy::type_complexity)]
fn guest_apply(
    mut events: EventReader<NetIn>,
    net: Res<NetMode>,
    mut commands: Commands,
    mut arena: ResMut<Arena>,
    mut fx: ResMut<GuestFx>,
    mut fighters: Query<(&mut Fighter, &mut Sprite, &mut Transform), Without<TileSprite>>,
    mut tiles_q: Query<(&TileSprite, &mut Sprite), Without<Fighter>>,
    mut final_score: ResMut<FinalScore>,
    mut next: ResMut<NextState<Phase>>,
) {
    if !net_guest(&net) {
        // Not a guest: host_net_input consumed what it needed; drop the rest.
        events.clear();
        return;
    }
    let my_seat = net.0.as_ref().map(|c| c.seat).unwrap_or(0) as usize;
    // Only the newest snapshot matters; ends are processed immediately.
    let mut latest_state: Option<WireState> = None;
    for ev in events.read() {
        if ev.left && ev.seat == 0 {
            // The simulation left the building. Bank what we know and end it.
            let kills = fighters
                .iter()
                .find(|(f, _, _)| f.seat == my_seat)
                .map(|(f, _, _)| f.kills)
                .unwrap_or(0);
            final_score.0 = kills * 100;
            next.set(Phase::GameOver);
            return;
        }
        if ev.left || ev.seat != 0 {
            continue; // only the host speaks state
        }
        let Ok(v) = serde_json::from_str::<serde_json::Value>(&ev.data) else { continue };
        match v.get("t").and_then(|t| t.as_str()) {
            Some("st") => {
                if let Ok(st) = serde_json::from_str::<WireState>(&ev.data) {
                    latest_state = Some(st);
                }
            }
            Some("end") => {
                if let Ok(end) = serde_json::from_str::<WireEnd>(&ev.data) {
                    let total = end.ranks.len().max(2);
                    let rank = end
                        .ranks
                        .iter()
                        .find(|(s, _)| *s as usize == my_seat)
                        .map(|(_, r)| *r as usize)
                        .unwrap_or(total);
                    let kills = end
                        .kills
                        .iter()
                        .find(|(s, _)| *s as usize == my_seat)
                        .map(|(_, k)| *k)
                        .unwrap_or(0);
                    let alive = end.alive.iter().any(|&s| s as usize == my_seat);
                    final_score.0 = placement_score(total, rank, kills, alive);
                    next.set(Phase::GameOver);
                }
            }
            _ => {}
        }
    }
    let Some(st) = latest_state else { return };

    // Tiles.
    for (i, ch) in st.tiles.chars().enumerate() {
        if i >= arena.tiles.len() {
            break;
        }
        arena.tiles[i] = match ch {
            '1' => Tile::Solid,
            '2' => Tile::Crate,
            _ => Tile::Empty,
        };
    }
    for (ts, mut sprite) in &mut tiles_q {
        let want = match arena.tiles[ts.0] {
            Tile::Solid => Color::srgb(0.16, 0.16, 0.24),
            Tile::Crate => Color::srgb(0.45, 0.30, 0.13),
            Tile::Empty => Color::srgb(0.05, 0.05, 0.09),
        };
        if sprite.color != want {
            sprite.color = want;
        }
    }
    arena.clock = st.clk as f32 / 10.0;

    // Fighters: update targets; guest_smooth eases the sprites over.
    for wf in &st.f {
        for (mut f, mut sprite, _) in &mut fighters {
            if f.seat != wf.0 as usize {
                continue;
            }
            f.tile = IVec2::new(wf.1, wf.2);
            f.dir = IVec2::new(wf.3, wf.4);
            f.progress = wf.5 as f32 / 100.0;
            let was_alive = f.alive;
            f.alive = wf.6 == 1;
            f.kills = wf.7;
            if was_alive && !f.alive {
                sprite.color.set_alpha(0.15);
                sfx("death");
            }
        }
    }

    // Bombs / flames / perks: diff the pools against the snapshot.
    let want_bombs: std::collections::HashSet<usize> = st.b.iter().map(|&(c, _)| c as usize).collect();
    fx.bombs.retain(|cell, e| {
        if want_bombs.contains(cell) {
            true
        } else {
            commands.entity(*e).despawn();
            false
        }
    });
    for &(cell, _) in &st.b {
        let cell = cell as usize;
        if !fx.bombs.contains_key(&cell) {
            sfx("place");
        }
        fx.bombs.entry(cell).or_insert_with(|| {
            let p = world((cell as i32) % COLS, (cell as i32) / COLS);
            commands
                .spawn((
                    Sprite { color: Color::srgb(0.25, 0.25, 0.3), custom_size: Some(Vec2::splat(CELL - 8.0)), ..default() },
                    Transform::from_xyz(p.x, p.y, 4.0),
                    BombSprite,
                    GameTag,
                ))
                .id()
        });
    }
    let want_flames: std::collections::HashSet<usize> = st.fl.iter().map(|&c| c as usize).collect();
    fx.flames.retain(|cell, e| {
        if want_flames.contains(cell) {
            true
        } else {
            commands.entity(*e).despawn();
            false
        }
    });
    let mut new_flames = false;
    for &cell in &st.fl {
        let cell = cell as usize;
        if !fx.flames.contains_key(&cell) {
            new_flames = true;
        }
        fx.flames.entry(cell).or_insert_with(|| {
            let p = world((cell as i32) % COLS, (cell as i32) / COLS);
            commands
                .spawn((
                    Sprite { color: AMBER, custom_size: Some(Vec2::splat(CELL - 6.0)), ..default() },
                    Transform::from_xyz(p.x, p.y, 5.0),
                    GameTag,
                ))
                .id()
        });
    }
    if new_flames {
        sfx("boom");
    }
    let want_perks: std::collections::HashSet<usize> = st.p.iter().map(|&(c, _)| c as usize).collect();
    fx.perks.retain(|cell, e| {
        if want_perks.contains(cell) {
            true
        } else {
            commands.entity(*e).despawn();
            false
        }
    });
    for &(cell, kind) in &st.p {
        let cell = cell as usize;
        fx.perks.entry(cell).or_insert_with(|| {
            let color = match kind {
                0 => RED,
                1 => Color::srgb(0.3, 0.6, 1.0),
                2 => Color::srgb(0.55, 1.0, 0.3),
                _ => WHITE,
            };
            let p = world((cell as i32) % COLS, (cell as i32) / COLS);
            commands
                .spawn((
                    Sprite { color, custom_size: Some(Vec2::splat(CELL * 0.45)), ..default() },
                    Transform::from_xyz(p.x, p.y, 3.0),
                    GameTag,
                ))
                .id()
        });
    }
}

/// Guest render smoothing: ease each fighter toward its latest snapshot
/// target instead of teleporting between 20 Hz frames. Snaps across large
/// jumps (spawn, respawn) so easing never smears the arena.
fn guest_smooth(
    time: Res<Time>,
    net: Res<NetMode>,
    mut fighters: Query<(&Fighter, &mut Transform)>,
) {
    if !net_guest(&net) {
        return;
    }
    let blend = 1.0 - (-14.0 * time.delta_secs()).exp();
    for (f, mut tf) in &mut fighters {
        let from = world(f.tile.x, f.tile.y);
        let to = world(f.tile.x + f.dir.x, f.tile.y + f.dir.y);
        let target = from.lerp(to, f.progress);
        let here = tf.translation.truncate();
        let next = if here.distance(target) > CELL * 2.5 {
            target // teleport-scale jump: snap
        } else {
            here.lerp(target, blend)
        };
        tf.translation.x = next.x;
        tf.translation.y = next.y;
    }
}
