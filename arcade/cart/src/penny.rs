//! PENNY PINCHER — a broke accountant sweeps a maze for loose change while
//! four auditors close in. Original maze, characters, and theme; gold bars
//! briefly turn the tables ("write-off mode").

use bevy::prelude::*;

use crate::retro::{text, AMBER, CYAN, GREEN, MAGENTA, RED, WHITE};
use crate::rng::Rng;
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
}

#[derive(Component)]
struct Player;

#[derive(Component)]
struct Auditor {
    idx: usize,
    dead: bool, // heading home after being caught
}

#[derive(Component)]
struct Hud;

pub struct PennyPlugin;

impl Plugin for PennyPlugin {
    fn build(&self, app: &mut App) {
        app.add_systems(OnEnter(Phase::Playing), setup).add_systems(
            Update,
            (steer, advance, auditor_brains, munch, draw_hud)
                .chain()
                .run_if(in_state(Phase::Playing)),
        );
    }
}

fn setup(mut commands: Commands, mut rng: ResMut<Rng>) {
    let _ = &mut rng;
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
                '.' => {
                    coins_left += 1;
                    let e = commands
                        .spawn((
                            Sprite {
                                color: AMBER,
                                custom_size: Some(Vec2::splat(5.0)),
                                ..default()
                            },
                            Transform::from_xyz(pos.x, pos.y, 2.0),
                            GameTag,
                        ))
                        .id();
                    coins[idx] = Some(e);
                }
                'o' => {
                    coins_left += 1;
                    bars[idx] = true;
                    let e = commands
                        .spawn((
                            Sprite {
                                color: AMBER,
                                custom_size: Some(Vec2::new(14.0, 9.0)),
                                ..default()
                            },
                            Transform::from_xyz(pos.x, pos.y, 2.0),
                            GameTag,
                        ))
                        .id();
                    coins[idx] = Some(e);
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
    });

    // Our hero: a small green square with a money grin (a lighter inlay).
    commands
        .spawn((
            Player,
            Runner { tile: player_start, dir: IVec2::ZERO, want: IVec2::ZERO, progress: 0.0, speed: SPEED },
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
                Auditor { idx: i, dead: false },
                Runner { tile: home, dir: IVec2::ZERO, want: IVec2::ZERO, progress: 0.0, speed: SPEED * 0.92 },
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
/// centers when the wanted direction is open, stop at walls.
fn advance(time: Res<Time>, maze: Res<Maze>, mut runners: Query<(&mut Runner, &mut Transform)>) {
    if maze.paused_for_death {
        return;
    }
    let dt = time.delta_secs();
    for (mut r, mut tf) in &mut runners {
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
                r.tile.x = (r.tile.x + W) % W; // tunnel wrap
                r.progress = 0.0;
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
        // Decide only when stopped or exactly at a tile center.
        if r.dir != IVec2::ZERO && r.progress > f32::EPSILON {
            continue;
        }
        let target: IVec2 = if a.dead {
            IVec2::new(10, 10) // the office block
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
            let dist = (nx - target.x).abs().min((target.x - nx).abs()) + (r.tile.y + d.y - target.y).abs();
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

    // Coin pickup at the current tile.
    let idx = (pr.tile.y * W + pr.tile.x) as usize;
    if let Some(coin) = maze.coins[idx].take() {
        commands.entity(coin).despawn();
        maze.coins_left -= 1;
        if maze.bars[idx] {
            maze.score += 50;
            maze.frightened = true;
            maze.chain = 0;
            maze.fright.reset();
        } else {
            maze.score += 10;
        }
        if maze.coins_left == 0 {
            // Board cleared — next shift, a touch faster.
            maze.score += 500;
            maze.level += 1;
            final_score.0 = maze.score; // provisional, in case they bail
            respawn_board(&mut commands, &mut maze);
            pr.speed = SPEED * (1.0 + 0.08 * (maze.level - 1) as f32);
            pr.tile = IVec2::new(10, 12);
            pr.dir = IVec2::ZERO;
            pr.want = IVec2::ZERO;
            pr.progress = 0.0;
        }
    }

    // Contact with auditors.
    let ppos = ptf.translation.truncate();
    for (mut ar, mut aud, mut sprite, atf) in &mut auditors {
        if aud.dead {
            // Reached the office? Back to work.
            if ar.tile.y == 10 && (6..=15).contains(&ar.tile.x) {
                aud.dead = false;
                sprite.color.set_alpha(1.0);
            }
            continue;
        }
        if atf.translation.truncate().distance(ppos) < CELL * 0.55 {
            if maze.frightened {
                maze.chain += 1;
                maze.score += 100 * (1 << maze.chain.min(4)); // 200 400 800 1600
                aud.dead = true;
                ar.speed = SPEED * 1.4;
                sprite.color.set_alpha(0.35);
            } else {
                maze.lives -= 1;
                if maze.lives <= 0 {
                    final_score.0 = maze.score;
                    next.set(Phase::GameOver);
                    return;
                }
                maze.paused_for_death = true;
                maze.respawn_pause.reset();
                return;
            }
        } else if !maze.frightened {
            ar.speed = SPEED * (0.92 + 0.05 * (maze.level - 1) as f32).min(1.15);
            sprite.color.set_alpha(1.0);
        } else {
            ar.speed = SPEED * 0.6; // frightened auditors shuffle
            sprite.color.set_alpha(0.6);
        }
    }
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
            match ch {
                '.' => {
                    let e = commands
                        .spawn((
                            Sprite { color: AMBER, custom_size: Some(Vec2::splat(5.0)), ..default() },
                            Transform::from_xyz(pos.x, pos.y, 2.0),
                            GameTag,
                        ))
                        .id();
                    maze.coins[idx] = Some(e);
                }
                'o' => {
                    let e = commands
                        .spawn((
                            Sprite { color: AMBER, custom_size: Some(Vec2::new(14.0, 9.0)), ..default() },
                            Transform::from_xyz(pos.x, pos.y, 2.0),
                            GameTag,
                        ))
                        .id();
                    maze.coins[idx] = Some(e);
                }
                _ => continue,
            }
            left += 1;
        }
    }
    maze.coins_left = left;
    maze.frightened = false;
    maze.chain = 0;
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
