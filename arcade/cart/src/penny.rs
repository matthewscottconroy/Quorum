//! PENNY PINCHER — a broke accountant sweeps a maze for loose change while
//! four auditors close in. Original maze, characters, and theme; gold bars
//! briefly turn the tables ("write-off mode").

use bevy::prelude::*;

use crate::retro::{popup, text, AMBER, CYAN, DIM, GREEN, MAGENTA, RED, WHITE};
use crate::rng::Rng;
use crate::shell::{sfx, stat};
use crate::{FinalScore, GameTag, Phase};

pub const BLURB: &[&str] = &[
    "COLLECT EVERY COIN. DODGE THE AUDITORS.",
    "GOLD BARS = WRITE-OFF MODE. BITE BACK.",
    "ARROWS OR WASD TO RUN.",
];

// An original 21×21 maze. '#' wall, '.' coin, 'o' gold bar, ' ' bare floor,
// 'T' tunnel mouth (wraps horizontally), 'H' auditor office, 'P' player start.
const MAZE: [&str; 21] = [
    "#####################",
    "#.........#.........#",
    "#o###.###.#.###.###o#",
    "#...#.#.......#.#...#",
    "###.#.#.#####.#.#.###",
    "#.......#...#.......#",
    "#.###.#.#.#.#.#.###.#",
    "#.#...#...#...#...#.#",
    "#.#.#####.#.#####.#.#",
    "T...#   #.#.#   #...T",
    "#.#.# H....... H#.#.#",
    "#.#.#   #####   #.#.#",
    "#...#.....P.....#...#",
    "#.#.#####.#.#####.#.#",
    "#.#...#...#...#...#.#",
    "#.###.#.#####.#.###.#",
    "#..o..#...#...#..o..#",
    "###.#####.#.#####.###",
    "#.........#.........#",
    "#.###.###.#.###.###.#",
    "#####################",
];

const W: i32 = 21;
const H: i32 = 21;
const CELL: f32 = 27.0;
const SPEED: f32 = 105.0; // player px/s at level 1
const FRIGHT_SECS: f32 = 6.0;

fn tile_pos(x: i32, y: i32) -> Vec2 {
    Vec2::new(
        (x as f32 - (W as f32 - 1.0) / 2.0) * CELL,
        ((H as f32 - 1.0) / 2.0 - y as f32) * CELL - 8.0,
    )
}

#[derive(Resource)]
struct Maze {
    walls: Vec<bool>,
    coins: Vec<Option<Entity>>, // coin/bar entity per tile
    bars: Vec<bool>,
    coins_left: u32,
    score: u32,
    lives: i32,
    level: u32,
    fright: Timer,
    frightened: bool,
    chain: u32, // auditors caught during one write-off
    respawn_pause: Timer,
    paused_for_death: bool,
    next_extra_life: u32,
}

impl Maze {
    fn wall(&self, x: i32, y: i32) -> bool {
        if y < 0 || y >= H {
            return true;
        }
        // Horizontal tunnel wrap.
        let x = (x + W) % W;
        self.walls[(y * W + x) as usize]
    }
}

#[derive(Component)]
struct Runner {
    // Grid movement: current tile, fractional progress toward `dir`.
    tile: IVec2,
    dir: IVec2,
    want: IVec2,
    progress: f32,
    speed: f32,
    arrived: bool, // crossed a tile center this frame (decision point)
}

impl Runner {
    fn at(tile: IVec2, speed: f32) -> Self {
        Runner { tile, dir: IVec2::ZERO, want: IVec2::ZERO, progress: 0.0, speed, arrived: false }
    }
}

#[derive(Component)]
struct Player;

#[derive(Component)]
struct Auditor {
    idx: usize,
    dead: bool,   // heading home after being caught
    home: IVec2,  // the office tile this auditor revives at
    edible: bool, // armed by the CURRENT write-off; revived auditors are not
}

/// Attract-mode set dressing: a dim coin grid and a patrolling auditor.
#[derive(Component)]
struct AttractBit {
    vel: Vec2,
}

#[derive(Component)]
struct Hud;

pub struct PennyPlugin;

impl Plugin for PennyPlugin {
    fn build(&self, app: &mut App) {
        app.add_systems(OnEnter(Phase::Playing), setup)
            .add_systems(OnEnter(Phase::Attract), attract_in)
            .add_systems(OnExit(Phase::Attract), attract_out)
            .add_systems(Update, attract_drift.run_if(in_state(Phase::Attract)))
            .add_systems(
                Update,
                (steer, advance, auditor_brains, munch, draw_hud)
                    .chain()
                    .run_if(in_state(Phase::Playing))
                    .run_if(crate::unpaused),
            );
    }
}

fn setup(mut commands: Commands) {
    let mut walls = vec![false; (W * H) as usize];
    let mut coins: Vec<Option<Entity>> = vec![None; (W * H) as usize];
    let mut bars = vec![false; (W * H) as usize];
    let mut coins_left = 0;
    let mut player_start = IVec2::new(10, 12);
    let mut offices: Vec<IVec2> = Vec::new();

    for (y, row) in MAZE.iter().enumerate() {
        for (x, ch) in row.chars().enumerate() {
            let idx = y * W as usize + x;
            let pos = tile_pos(x as i32, y as i32);
            match ch {
                '#' => {
                    walls[idx] = true;
                    commands.spawn((
                        Sprite {
                            color: Color::srgb(0.09, 0.13, 0.42),
                            custom_size: Some(Vec2::splat(CELL - 1.0)),
                            ..default()
                        },
                        Transform::from_xyz(pos.x, pos.y, 1.0),
                        GameTag,
                    ));
                }
                '.' | 'o' => {
                    coins_left += 1;
                    if ch == 'o' {
                        bars[idx] = true;
                    }
                    coins[idx] = spawn_pickup(&mut commands, ch, pos);
                }
                'P' => player_start = IVec2::new(x as i32, y as i32),
                'H' => offices.push(IVec2::new(x as i32, y as i32)),
                _ => {}
            }
        }
    }

    commands.insert_resource(Maze {
        walls,
        coins,
        bars,
        coins_left,
        score: 0,
        lives: 3,
        level: 1,
        fright: Timer::from_seconds(FRIGHT_SECS, TimerMode::Once),
        frightened: false,
        chain: 0,
        respawn_pause: Timer::from_seconds(1.2, TimerMode::Once),
        paused_for_death: false,
        next_extra_life: 10_000,
    });

    // Our hero: a small green square with a money grin (a lighter inlay).
    commands
        .spawn((
            Player,
            Runner::at(player_start, SPEED),
            Sprite { color: GREEN, custom_size: Some(Vec2::splat(CELL - 7.0)), ..default() },
            Transform::from_translation(tile_pos(player_start.x, player_start.y).extend(5.0)),
            GameTag,
        ))
        .with_children(|p| {
            p.spawn((
                Sprite { color: Color::srgb(0.06, 0.35, 0.16), custom_size: Some(Vec2::new(10.0, 3.0)), ..default() },
                Transform::from_xyz(0.0, -4.0, 0.1),
            ));
        });

    // Four auditors in suit colors, parked at their offices.
    let colors = [RED, MAGENTA, CYAN, Color::srgb(1.0, 0.65, 0.30)];
    for i in 0..4 {
        let home = offices.get(i % offices.len().max(1)).copied().unwrap_or(IVec2::new(10, 10));
        commands
            .spawn((
                Auditor { idx: i, dead: false, home, edible: false },
                Runner::at(home, SPEED * 0.92),
                Sprite { color: colors[i], custom_size: Some(Vec2::splat(CELL - 6.0)), ..default() },
                Transform::from_translation(tile_pos(home.x, home.y).extend(6.0)),
                GameTag,
            ))
            .with_children(|p| {
                // A little white collar: unmistakably middle management.
                p.spawn((
                    Sprite { color: WHITE, custom_size: Some(Vec2::new(12.0, 3.0)), ..default() },
                    Transform::from_xyz(0.0, 6.0, 0.1),
                ));
            });
    }

    let hud = text(&mut commands, "", 22.0, WHITE, Vec3::new(0.0, 300.0, 5.0));
    commands.entity(hud).insert((Hud, GameTag));
}

fn steer(keys: Res<ButtonInput<KeyCode>>, mut players: Query<&mut Runner, With<Player>>) {
    let Ok(mut r) = players.single_mut() else { return };
    let want = if keys.pressed(KeyCode::ArrowLeft) || keys.pressed(KeyCode::KeyA) {
        IVec2::new(-1, 0)
    } else if keys.pressed(KeyCode::ArrowRight) || keys.pressed(KeyCode::KeyD) {
        IVec2::new(1, 0)
    } else if keys.pressed(KeyCode::ArrowUp) || keys.pressed(KeyCode::KeyW) {
        IVec2::new(0, -1)
    } else if keys.pressed(KeyCode::ArrowDown) || keys.pressed(KeyCode::KeyS) {
        IVec2::new(0, 1)
    } else {
        r.want
    };
    r.want = want;
}

/// Moves every runner along the grid: advance toward the next tile, turn at
/// centers when the wanted direction is open, stop at walls. The player may
/// reverse instantly mid-segment (a core defensive move in this genre);
/// auditors keep the never-reverse rule and only flip via their brains.
fn advance(
    time: Res<Time>,
    maze: Res<Maze>,
    mut runners: Query<(&mut Runner, &mut Transform, Option<&Player>)>,
) {
    if maze.paused_for_death {
        return;
    }
    let dt = time.delta_secs();
    for (mut r, mut tf, player) in &mut runners {
        if player.is_some() && r.dir != IVec2::ZERO && r.want == -r.dir && r.progress > 0.0 {
            stat("about_faces", 1);
            // Instant about-face: re-express the same position walking back.
            let d = r.dir;
            r.tile += d;
            r.tile.x = (r.tile.x + W) % W;
            r.dir = -d;
            r.progress = 1.0 - r.progress;
        }
        let step = r.speed * dt / CELL; // progress in tiles
        let mut remaining = step;
        while remaining > 0.0 {
            if r.dir == IVec2::ZERO {
                // Stopped at a center: take the wanted turn if open.
                if r.want != IVec2::ZERO && !maze.wall(r.tile.x + r.want.x, r.tile.y + r.want.y) {
                    r.dir = r.want;
                } else {
                    break;
                }
            }
            let advance_by = remaining.min(1.0 - r.progress);
            r.progress += advance_by;
            remaining -= advance_by;
            if r.progress >= 1.0 - f32::EPSILON {
                // Arrived at the next tile center.
                let d = r.dir;
                r.tile += d;
                if player.is_some() && (r.tile.x < 0 || r.tile.x >= W) {
                    stat("tunnel_trips", 1);
                }
                r.tile.x = (r.tile.x + W) % W; // tunnel wrap
                r.progress = 0.0;
                r.arrived = true; // decision point for the brains this frame
                // Turn or continue or stop.
                let turn_open = r.want != IVec2::ZERO && !maze.wall(r.tile.x + r.want.x, r.tile.y + r.want.y);
                let ahead_open = !maze.wall(r.tile.x + r.dir.x, r.tile.y + r.dir.y);
                if turn_open {
                    r.dir = r.want;
                } else if !ahead_open {
                    r.dir = IVec2::ZERO;
                }
            }
        }
        let from = tile_pos(r.tile.x, r.tile.y);
        let to = tile_pos(r.tile.x + r.dir.x, r.tile.y + r.dir.y);
        let p = from.lerp(to, r.progress);
        tf.translation.x = p.x;
        tf.translation.y = p.y;
    }
}

/// Auditor decision-making at tile centers. Four temperaments:
/// 0 chases directly, 1 aims ahead of the runner, 2 mixes chase with random
/// turns, 3 patrols corners until the runner is close. Frightened auditors
/// flee; caught ones walk home to their office.
fn auditor_brains(
    maze: Res<Maze>,
    mut rng: ResMut<Rng>,
    players: Query<&Runner, (With<Player>, Without<Auditor>)>,
    mut auditors: Query<(&mut Runner, &Auditor), Without<Player>>,
) {
    if maze.paused_for_death {
        return;
    }
    let Ok(p) = players.single() else { return };
    for (mut r, a) in &mut auditors {
        // Decide when stopped or when `advance` just crossed a tile center
        // this frame (movement rolls leftover progress past exact centers,
        // so a raw progress==0 test would almost never fire mid-run).
        if r.dir != IVec2::ZERO && !r.arrived {
            continue;
        }
        r.arrived = false;
        let target: IVec2 = if a.dead {
            a.home // walk back to your own office
        } else if maze.frightened {
            // Run to the corner farthest from the player.
            let corners = [IVec2::new(1, 1), IVec2::new(W - 2, 1), IVec2::new(1, H - 2), IVec2::new(W - 2, H - 2)];
            *corners
                .iter()
                .max_by_key(|c| (c.x - p.tile.x).abs() + (c.y - p.tile.y).abs())
                .unwrap()
        } else {
            match a.idx {
                0 => p.tile,
                1 => p.tile + p.dir * 4,
                2 => {
                    if rng.chance(0.35) {
                        IVec2::new(rng.range(W as u32) as i32, rng.range(H as u32) as i32)
                    } else {
                        p.tile
                    }
                }
                _ => {
                    let near = (p.tile.x - r.tile.x).abs() + (p.tile.y - r.tile.y).abs() < 7;
                    if near {
                        p.tile
                    } else {
                        [IVec2::new(1, 1), IVec2::new(W - 2, H - 2), IVec2::new(W - 2, 1), IVec2::new(1, H - 2)]
                            [(a.idx + maze.level as usize) % 4]
                    }
                }
            }
        };
        // Pick the open direction that gets closest to the target; never
        // reverse unless boxed in.
        let mut best: Option<(i32, IVec2)> = None;
        for d in [IVec2::new(1, 0), IVec2::new(-1, 0), IVec2::new(0, 1), IVec2::new(0, -1)] {
            if d == -r.dir && r.dir != IVec2::ZERO {
                continue;
            }
            if maze.wall(r.tile.x + d.x, r.tile.y + d.y) {
                continue;
            }
            let nx = (r.tile.x + d.x + W) % W;
            // Wrap-aware horizontal distance so auditors use the tunnels too.
            let dx = (nx - target.x).abs();
            let dist = dx.min(W - dx) + (r.tile.y + d.y - target.y).abs();
            if best.map(|(bd, _)| dist < bd).unwrap_or(true) {
                best = Some((dist, d));
            }
        }
        if let Some((_, d)) = best {
            r.want = d;
            if r.dir == IVec2::ZERO {
                r.dir = d;
            }
        } else if r.dir != IVec2::ZERO {
            r.want = -r.dir; // boxed in: reverse
            r.dir = -r.dir;
        }
    }
}

#[allow(clippy::too_many_arguments)]
fn munch(
    time: Res<Time>,
    mut commands: Commands,
    mut maze: ResMut<Maze>,
    mut final_score: ResMut<FinalScore>,
    mut next: ResMut<NextState<Phase>>,
    mut players: Query<(&mut Runner, &mut Transform), (With<Player>, Without<Auditor>)>,
    mut auditors: Query<(&mut Runner, &mut Auditor, &mut Sprite, &mut Transform), Without<Player>>,
) {
    let Ok((mut pr, mut ptf)) = players.single_mut() else { return };

    // Death pause: freeze the world for a beat, then reset positions.
    if maze.paused_for_death {
        maze.respawn_pause.tick(time.delta());
        if maze.respawn_pause.finished() {
            maze.paused_for_death = false;
            pr.tile = IVec2::new(10, 12);
            pr.dir = IVec2::ZERO;
            pr.want = IVec2::ZERO;
            pr.progress = 0.0;
            let home = tile_pos(10, 12);
            ptf.translation.x = home.x;
            ptf.translation.y = home.y;
            let mut i = 0;
            for (mut ar, mut aud, _, mut atf) in &mut auditors {
                let spot = [IVec2::new(6, 10), IVec2::new(15, 10), IVec2::new(9, 10), IVec2::new(12, 10)][i % 4];
                ar.tile = spot;
                ar.dir = IVec2::ZERO;
                ar.want = IVec2::ZERO;
                ar.progress = 0.0;
                aud.dead = false;
                aud.edible = false;
                let p = tile_pos(spot.x, spot.y);
                atf.translation.x = p.x;
                atf.translation.y = p.y;
                i += 1;
            }
        }
        return;
    }

    // Write-off timer.
    if maze.frightened && maze.fright.tick(time.delta()).finished() {
        maze.frightened = false;
        maze.chain = 0;
    }

    // Extra accountant every 10k — same honest threshold as Comet Buster.
    if maze.score >= maze.next_extra_life {
        maze.lives += 1;
        maze.next_extra_life += 10_000;
        sfx("extra");
        stat("extra_lives", 1);
        popup(&mut commands, "EXTRA LIFE", 20.0, GREEN, Vec2::new(0.0, 270.0));
    }

    // Coin pickup at the current tile.
    let idx = (pr.tile.y * W + pr.tile.x) as usize;
    let mut reverse_auditors = false;
    if let Some(coin) = maze.coins[idx].take() {
        commands.entity(coin).despawn();
        maze.coins_left -= 1;
        if maze.bars[idx] {
            maze.score += 50;
            maze.frightened = true;
            maze.chain = 0;
            let secs = (FRIGHT_SECS - 0.5 * (maze.level - 1) as f32).max(2.5);
            maze.fright.set_duration(std::time::Duration::from_secs_f32(secs));
            maze.fright.reset();
            reverse_auditors = true;
            sfx("power");
            stat("gold_bars", 1);
            popup(&mut commands, "+50", 16.0, AMBER, ptf.translation.truncate());
        } else {
            maze.score += 10;
            sfx("coin");
            stat("coins_pocketed", 1);
        }
        if maze.coins_left == 0 {
            // Board cleared — next shift, a touch faster. Reuse the death
            // freeze: a 1.2s beat, then EVERYONE resets to their spawn spot
            // (no more shift-two spawn camping by a lucky auditor).
            maze.score += 500;
            maze.level += 1;
            stat("shifts_cleared", 1);
            final_score.0 = maze.score; // provisional, in case they bail
            respawn_board(&mut commands, &mut maze);
            pr.speed = SPEED * (1.0 + 0.08 * (maze.level - 1) as f32).min(1.25);
            maze.paused_for_death = true;
            maze.respawn_pause.reset();
            sfx("clear");
            popup(&mut commands, &format!("SHIFT {}", maze.level), 30.0, AMBER, Vec2::new(0.0, 40.0));
            popup(&mut commands, "+500", 20.0, WHITE, Vec2::new(0.0, 8.0));
            return;
        }
    }

    // Contact with auditors.
    let fright_ending = maze.frightened && maze.fright.remaining_secs() < 1.8;
    let blink_on = (time.elapsed_secs() * 6.0) as i32 % 2 == 0;
    let ppos = ptf.translation.truncate();
    for (mut ar, mut aud, mut sprite, atf) in &mut auditors {
        if reverse_auditors && !aud.dead {
            // Mode change: every live auditor is fair game and snaps around
            // (the classic tell) if it was moving.
            aud.edible = true;
            if ar.dir != IVec2::ZERO {
                ar.want = -ar.dir;
                ar.dir = -ar.dir;
            }
        }
        if aud.dead {
            // Reached their own office? Back to work — and NOT edible: a
            // revived auditor takes no part in the write-off that caught it.
            if ar.tile == aud.home {
                aud.dead = false;
                aud.edible = false;
                sprite.color.set_alpha(1.0);
            }
            continue;
        }
        let edible_now = maze.frightened && aud.edible;
        if atf.translation.truncate().distance(ppos) < CELL * 0.55 {
            if edible_now {
                maze.chain += 1;
                let pay = 100 * (1 << maze.chain.min(4)); // 200 400 800 1600
                maze.score += pay;
                aud.dead = true;
                ar.speed = SPEED * 1.4;
                sprite.color.set_alpha(0.35);
                sfx("eat");
                stat("auditors_bitten", 1);
                popup(&mut commands, &format!("+{pay}"), 18.0, WHITE, atf.translation.truncate());
            } else {
                maze.lives -= 1;
                sfx("death");
                stat("times_audited", 1);
                if maze.lives <= 0 {
                    final_score.0 = maze.score;
                    next.set(Phase::GameOver);
                    return;
                }
                maze.paused_for_death = true;
                maze.respawn_pause.reset();
                return;
            }
        } else if !edible_now {
            // Cap the pursuit just under the player's own cap: late shifts
            // stay tense through fright windows, not through unwinnable
            // straight-line chases.
            ar.speed = SPEED * (0.92 + 0.06 * (maze.level - 1) as f32).min(1.22);
            sprite.color.set_alpha(1.0);
        } else {
            ar.speed = SPEED * 0.6; // frightened auditors shuffle
            // Blink during the final stretch: the tables are about to turn back.
            sprite.color.set_alpha(if fright_ending && blink_on { 1.0 } else { 0.6 });
        }
    }
}

/// Spawns one coin ('.') or gold bar ('o') sprite — shared by first-board
/// setup and the next-shift respawn.
fn spawn_pickup(commands: &mut Commands, ch: char, pos: Vec2) -> Option<Entity> {
    let sprite = match ch {
        '.' => Sprite { color: AMBER, custom_size: Some(Vec2::splat(5.0)), ..default() },
        'o' => Sprite { color: AMBER, custom_size: Some(Vec2::new(14.0, 9.0)), ..default() },
        _ => return None,
    };
    Some(commands.spawn((sprite, Transform::from_xyz(pos.x, pos.y, 2.0), GameTag)).id())
}

/// Respawns coins and bars for the next level.
fn respawn_board(commands: &mut Commands, maze: &mut Maze) {
    let mut left = 0;
    for (y, row) in MAZE.iter().enumerate() {
        for (x, ch) in row.chars().enumerate() {
            let idx = y * W as usize + x;
            if maze.coins[idx].is_some() {
                continue;
            }
            let pos = tile_pos(x as i32, y as i32);
            let Some(e) = spawn_pickup(commands, ch, pos) else { continue };
            maze.coins[idx] = Some(e);
            left += 1;
        }
    }
    maze.coins_left = left;
    maze.frightened = false;
    maze.chain = 0;
}

// ---- attract-mode set dressing ----
// A dim coin trail and two patrolling auditor squares, so the title card
// matches its siblings (Comet drifts rocks, Brickfall rains bricks).

fn attract_in(mut commands: Commands, mut rng: ResMut<Rng>) {
    for i in 0..14 {
        let x = -290.0 + i as f32 * 45.0;
        commands.spawn((
            AttractBit { vel: Vec2::ZERO },
            Sprite { color: DIM, custom_size: Some(Vec2::splat(5.0)), ..default() },
            Transform::from_xyz(x, -180.0, 1.0),
        ));
    }
    for (i, color) in [RED, CYAN].iter().enumerate() {
        let dir = if i == 0 { 1.0 } else { -1.0 };
        commands.spawn((
            AttractBit { vel: Vec2::new(dir * rng.between(50.0, 75.0), 0.0) },
            Sprite {
                color: color.with_alpha(0.55),
                custom_size: Some(Vec2::splat(CELL - 6.0)),
                ..default()
            },
            Transform::from_xyz(dir * -300.0, -180.0, 2.0),
        ));
    }
}

fn attract_out(mut commands: Commands, bits: Query<Entity, With<AttractBit>>) {
    for e in &bits {
        commands.entity(e).despawn();
    }
}

fn attract_drift(time: Res<Time>, mut bits: Query<(&AttractBit, &mut Transform)>) {
    let dt = time.delta_secs();
    for (bit, mut tf) in &mut bits {
        tf.translation += (bit.vel * dt).extend(0.0);
        if tf.translation.x > 340.0 {
            tf.translation.x = -340.0;
        } else if tf.translation.x < -340.0 {
            tf.translation.x = 340.0;
        }
    }
}

fn draw_hud(maze: Res<Maze>, mut hud: Query<&mut Text2d, With<Hud>>) {
    if let Ok(mut t) = hud.single_mut() {
        let s = format!(
            "SCORE {:06}   LIVES {}   SHIFT {}{}",
            maze.score,
            maze.lives.max(0),
            maze.level,
            if maze.frightened { "   WRITE-OFF!" } else { "" }
        );
        if t.0 != s {
            t.0 = s;
        }
    }
}
