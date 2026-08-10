//! COMET BUSTER — one ship, drifting space rocks, pure vector lines. An
//! original wrap-around shooter drawn entirely with gizmo polylines.

use bevy::prelude::*;

use crate::retro::{popup, text, AMBER, GREEN, SCREEN_H, SCREEN_W, WHITE};
use crate::rng::Rng;
use crate::shell::sfx;
use crate::{FinalScore, GameTag, Phase};
use crate::retro::MAGENTA;

pub const BLURB: &[&str] = &[
    "ROCKS DRIFT. YOU DON'T HAVE TO.",
    "ARROWS STEER  /  SPACE FIRES  /  H PANICS",
    "BIG ROCKS SPLIT. SMALL ROCKS PAY.",
];

const TURN: f32 = 4.0; // rad/s
const THRUST: f32 = 280.0;
const MAX_SPEED: f32 = 430.0;
const BULLET_SPEED: f32 = 520.0;
const BULLET_TTL: f32 = 1.05;
const MAX_BULLETS: usize = 4;

#[derive(Resource)]
struct Game {
    score: u32,
    lives: i32,
    level: u32,
    respawn: Timer,   // waiting to respawn after death
    clear: Timer,     // pause between cleared board and next wave
    waiting_spawn: bool,
    waiting_wave: bool,
    hyper_cooldown: Timer,
    next_extra_life: u32,
    saucer_clock: Timer,
    thrust_tick: Timer,
}

#[derive(Component)]
struct Ship {
    vel: Vec2,
    angle: f32,
    invuln: Timer,
}

#[derive(Component)]
struct Rock {
    size: u8, // 2 large, 1 medium, 0 small
    vel: Vec2,
    spin: f32,
    shape: Vec<Vec2>,
}

#[derive(Component)]
struct Bullet {
    vel: Vec2,
    ttl: Timer,
}

#[derive(Component)]
struct Shard {
    vel: Vec2,
    ttl: Timer,
    dir: Vec2,
}

/// The hunter: drifts across the field taking pot shots. Big and sloppy
/// early; small and personal once your score says you can handle it.
#[derive(Component)]
struct Saucer {
    vel: Vec2,
    small: bool,
    fire: Timer,
    wobble: Timer,
}

#[derive(Component)]
struct EnemyBullet {
    vel: Vec2,
    ttl: Timer,
}

/// Attract-mode set dressing: rocks drifting behind the title card.
#[derive(Component)]
struct AttractRock {
    vel: Vec2,
    spin: f32,
    shape: Vec<Vec2>,
}

#[derive(Component)]
struct Hud;

pub struct CometPlugin;

impl Plugin for CometPlugin {
    fn build(&self, app: &mut App) {
        app.add_systems(OnEnter(Phase::Playing), setup)
            .add_systems(OnEnter(Phase::Attract), attract_in)
            .add_systems(OnExit(Phase::Attract), attract_out)
            .add_systems(
                Update,
                (attract_drift, draw_attract).run_if(in_state(Phase::Attract)),
            )
            .add_systems(
                Update,
                (control, physics, saucer_run, collide, waves, draw)
                    .chain()
                    .run_if(in_state(Phase::Playing))
                    .run_if(crate::unpaused),
            );
    }
}

fn rock_shape(rng: &mut Rng, radius: f32) -> Vec<Vec2> {
    let n = 10;
    (0..n)
        .map(|i| {
            let a = i as f32 / n as f32 * std::f32::consts::TAU;
            let r = radius * rng.between(0.66, 1.28);
            Vec2::new(a.cos() * r, a.sin() * r)
        })
        .collect()
}

fn rock_radius(size: u8) -> f32 {
    match size {
        2 => 46.0,
        1 => 26.0,
        _ => 13.0,
    }
}

fn spawn_rock(
    commands: &mut Commands,
    rng: &mut Rng,
    pos: Vec2,
    size: u8,
    level: u32,
    parent_vel: Option<Vec2>,
) {
    let vel = match parent_vel {
        // Fragments inherit the parent's momentum, deflected and hotter.
        Some(pv) => {
            let angle = pv.y.atan2(pv.x) + rng.between(-1.1, 1.1);
            let speed = pv.length() * rng.between(1.15, 1.5);
            Vec2::new(angle.cos(), angle.sin()) * speed
        }
        None => {
            let speed = rng.between(40.0, 90.0) + 12.0 * level as f32 + (2 - size) as f32 * 30.0;
            let dir = rng.between(0.0, std::f32::consts::TAU);
            Vec2::new(dir.cos(), dir.sin()) * speed
        }
    };
    commands.spawn((
        Rock { size, vel, spin: rng.between(-1.6, 1.6), shape: rock_shape(rng, rock_radius(size)) },
        Transform::from_xyz(pos.x, pos.y, 3.0),
        GameTag,
    ));
}

fn spawn_wave(commands: &mut Commands, rng: &mut Rng, level: u32, avoid: Vec2) {
    let count = 3 + level.min(6);
    for _ in 0..count {
        // Spawn on the rim, never near the ship.
        let mut pos;
        loop {
            let edge = rng.range(4);
            pos = match edge {
                0 => Vec2::new(rng.between(-360.0, 360.0), 310.0),
                1 => Vec2::new(rng.between(-360.0, 360.0), -310.0),
                2 => Vec2::new(-350.0, rng.between(-320.0, 320.0)),
                _ => Vec2::new(350.0, rng.between(-320.0, 320.0)),
            };
            if pos.distance(avoid) > 140.0 {
                break;
            }
        }
        spawn_rock(commands, rng, pos, 2, level, None);
    }
}

fn setup(mut commands: Commands, mut rng: ResMut<Rng>) {
    commands.insert_resource(Game {
        score: 0,
        lives: 3,
        level: 1,
        respawn: Timer::from_seconds(1.2, TimerMode::Once),
        clear: Timer::from_seconds(1.5, TimerMode::Once),
        waiting_spawn: false,
        waiting_wave: false,
        hyper_cooldown: Timer::from_seconds(1.0, TimerMode::Once),
        next_extra_life: 10_000,
        saucer_clock: Timer::from_seconds(16.0, TimerMode::Once),
        thrust_tick: Timer::from_seconds(0.14, TimerMode::Repeating),
    });
    commands.spawn((
        Ship { vel: Vec2::ZERO, angle: std::f32::consts::FRAC_PI_2, invuln: Timer::from_seconds(2.0, TimerMode::Once) },
        Transform::from_xyz(0.0, 0.0, 4.0),
        GameTag,
    ));
    spawn_wave(&mut commands, &mut rng, 1, Vec2::ZERO);
    let hud = text(&mut commands, "", 26.0, WHITE, Vec3::new(-270.0, 292.0, 5.0));
    commands.entity(hud).insert((Hud, GameTag));
}

fn control(
    keys: Res<ButtonInput<KeyCode>>,
    time: Res<Time>,
    mut commands: Commands,
    mut rng: ResMut<Rng>,
    mut game: ResMut<Game>,
    mut final_score: ResMut<FinalScore>,
    mut next: ResMut<NextState<Phase>>,
    mut ships: Query<(Entity, &mut Ship, &mut Transform)>,
    bullets: Query<(), With<Bullet>>,
) {
    game.hyper_cooldown.tick(time.delta());
    let Ok((ship_e, mut ship, mut tf)) = ships.single_mut() else { return };
    let dt = time.delta_secs();
    ship.invuln.tick(time.delta());

    let turn = i32::from(keys.pressed(KeyCode::ArrowLeft)) - i32::from(keys.pressed(KeyCode::ArrowRight));
    ship.angle += TURN * dt * turn as f32;
    // Up or down: both thrust forward. There is no reverse in space — the
    // second binding is for pilots whose thumb lands on the wrong arrow.
    if keys.pressed(KeyCode::ArrowUp) || keys.pressed(KeyCode::ArrowDown) {
        let dir = Vec2::new(ship.angle.cos(), ship.angle.sin());
        ship.vel = (ship.vel + dir * THRUST * dt).clamp_length_max(MAX_SPEED);
        if game.thrust_tick.tick(time.delta()).just_finished() {
            sfx("thrust");
        }
    }

    if keys.just_pressed(KeyCode::Space) && bullets.iter().count() < MAX_BULLETS {
        sfx("fire");
        let dir = Vec2::new(ship.angle.cos(), ship.angle.sin());
        commands.spawn((
            Bullet {
                vel: ship.vel + dir * BULLET_SPEED,
                ttl: Timer::from_seconds(BULLET_TTL, TimerMode::Once),
            },
            Transform::from_translation(tf.translation + (dir * 16.0).extend(0.0)),
            GameTag,
        ));
    }

    // Hyperspace: instant relocation, one-in-eight chance the drive eats you.
    if keys.just_pressed(KeyCode::KeyH) && game.hyper_cooldown.finished() {
        sfx("hyper");
        game.hyper_cooldown.reset();
        tf.translation.x = rng.between(-330.0, 330.0);
        tf.translation.y = rng.between(-290.0, 290.0);
        ship.vel = Vec2::ZERO;
        if rng.chance(0.125) {
            // The drive misfires — treated exactly like a rock hit: the ship
            // is gone (despawned, not left as a ghost) and a last life ends
            // the game here, since collide's check never sees this death.
            let at = tf.translation.truncate();
            commands.entity(ship_e).despawn();
            explode_ship(&mut commands, &mut rng, at, &mut game);
            if game.lives <= 0 {
                final_score.0 = game.score;
                next.set(Phase::GameOver);
            }
        }
    }
}

fn wrap(v: &mut Vec3) {
    let (hw, hh) = (SCREEN_W / 2.0 + 20.0, SCREEN_H / 2.0 + 20.0);
    if v.x > hw {
        v.x = -hw;
    } else if v.x < -hw {
        v.x = hw;
    }
    if v.y > hh {
        v.y = -hh;
    } else if v.y < -hh {
        v.y = hh;
    }
}

fn physics(
    time: Res<Time>,
    mut commands: Commands,
    mut ships: Query<(&Ship, &mut Transform), (Without<Rock>, Without<Bullet>, Without<Shard>)>,
    mut rocks: Query<(&Rock, &mut Transform), (Without<Bullet>, Without<Shard>)>,
    mut bullets: Query<(Entity, &mut Bullet, &mut Transform), (Without<Rock>, Without<Shard>)>,
    mut shards: Query<(Entity, &mut Shard, &mut Transform), Without<Rock>>,
) {
    let dt = time.delta_secs();
    if let Ok((ship, mut tf)) = ships.single_mut() {
        tf.translation += (ship.vel * dt).extend(0.0);
        wrap(&mut tf.translation);
    }
    for (rock, mut tf) in &mut rocks {
        tf.translation += (rock.vel * dt).extend(0.0);
        tf.rotate_z(rock.spin * dt);
        wrap(&mut tf.translation);
    }
    for (e, mut b, mut tf) in &mut bullets {
        tf.translation += (b.vel * dt).extend(0.0);
        wrap(&mut tf.translation);
        if b.ttl.tick(time.delta()).finished() {
            commands.entity(e).despawn();
        }
    }
    for (e, mut s, mut tf) in &mut shards {
        tf.translation += (s.vel * dt).extend(0.0);
        if s.ttl.tick(time.delta()).finished() {
            commands.entity(e).despawn();
        }
    }
}

fn explode_ship(commands: &mut Commands, rng: &mut Rng, pos: Vec2, game: &mut Game) {
    sfx("death");
    game.lives -= 1;
    game.waiting_spawn = true;
    game.respawn.reset();
    for _ in 0..7 {
        let a = rng.between(0.0, std::f32::consts::TAU);
        let dir = Vec2::new(a.cos(), a.sin());
        commands.spawn((
            Shard {
                vel: dir * rng.between(40.0, 140.0),
                ttl: Timer::from_seconds(rng.between(0.5, 1.1), TimerMode::Once),
                dir,
            },
            Transform::from_xyz(pos.x, pos.y, 3.0),
            GameTag,
        ));
    }
}

fn collide(
    mut commands: Commands,
    mut rng: ResMut<Rng>,
    mut game: ResMut<Game>,
    mut final_score: ResMut<FinalScore>,
    mut next: ResMut<NextState<Phase>>,
    ships: Query<(Entity, &Ship, &Transform)>,
    rocks: Query<(Entity, &Rock, &Transform)>,
    bullets: Query<(Entity, &Transform), With<Bullet>>,
    saucers: Query<(Entity, &Saucer, &Transform)>,
    enemy_bullets: Query<(Entity, &EnemyBullet, &Transform)>,
) {
    // Bullets vs rocks.
    let mut dead_rocks: Vec<Entity> = Vec::new();
    let mut dead_bullets: Vec<Entity> = Vec::new();
    let mut dead_enemy_bullets: Vec<Entity> = Vec::new();
    for (re, rock, rtf) in &rocks {
        let rp = rtf.translation.truncate();
        let rr = rock_radius(rock.size);
        for (be, btf) in &bullets {
            if dead_bullets.contains(&be) || dead_rocks.contains(&re) {
                continue;
            }
            if btf.translation.truncate().distance(rp) < rr {
                dead_rocks.push(re);
                dead_bullets.push(be);
                sfx("boom");
                game.score += match rock.size {
                    2 => 20,
                    1 => 50,
                    _ => 100,
                };
                if rock.size > 0 {
                    for _ in 0..2 {
                        spawn_rock(&mut commands, &mut rng, rp, rock.size - 1, game.level, Some(rock.vel));
                    }
                }
            }
        }
        // Saucer fire splits rocks too (no score) — the saucer reshapes the
        // field instead of being a pure turret.
        for (ee, _, etf) in &enemy_bullets {
            if dead_enemy_bullets.contains(&ee) || dead_rocks.contains(&re) {
                continue;
            }
            if etf.translation.truncate().distance(rp) < rr {
                dead_rocks.push(re);
                dead_enemy_bullets.push(ee);
                sfx("boom");
                if rock.size > 0 {
                    for _ in 0..2 {
                        spawn_rock(&mut commands, &mut rng, rp, rock.size - 1, game.level, Some(rock.vel));
                    }
                }
            }
        }
    }
    // Bullets vs the saucer.
    for (se, saucer, stf) in &saucers {
        let sp = stf.translation.truncate();
        for (be, btf) in &bullets {
            if dead_bullets.contains(&be) {
                continue;
            }
            if btf.translation.truncate().distance(sp) < if saucer.small { 14.0 } else { 24.0 } {
                dead_bullets.push(be);
                commands.entity(se).despawn();
                sfx("boom");
                let pay = if saucer.small { 1000 } else { 200 };
                game.score += pay;
                popup(&mut commands, &format!("+{pay}"), 20.0, MAGENTA, sp);
            }
        }
    }
    // Extra ship every 10k, the honest arcade way.
    if game.score >= game.next_extra_life {
        game.lives += 1;
        game.next_extra_life += 10_000;
        sfx("extra");
        popup(&mut commands, "EXTRA SHIP", 20.0, GREEN, Vec2::new(-220.0, 250.0));
    }
    // Ship vs rocks and hostile fire.
    if let Ok((se, ship, stf)) = ships.single() {
        if ship.invuln.finished() && !game.waiting_spawn {
            let sp = stf.translation.truncate();
            let mut hit = false;
            for (re, rock, rtf) in &rocks {
                if dead_rocks.contains(&re) {
                    continue;
                }
                if rtf.translation.truncate().distance(sp) < rock_radius(rock.size) + 10.0 {
                    hit = true;
                    break;
                }
            }
            if !hit {
                for (ee, _, etf) in &enemy_bullets {
                    if dead_enemy_bullets.contains(&ee) {
                        continue;
                    }
                    if etf.translation.truncate().distance(sp) < 10.0 {
                        commands.entity(ee).despawn();
                        hit = true;
                        break;
                    }
                }
            }
            // Ramming the saucer is not free.
            if !hit {
                for (_, saucer, stf2) in &saucers {
                    let body = if saucer.small { 14.0 } else { 24.0 };
                    if stf2.translation.truncate().distance(sp) < body + 10.0 {
                        hit = true;
                        break;
                    }
                }
            }
            if hit {
                commands.entity(se).despawn();
                explode_ship(&mut commands, &mut rng, sp, &mut game);
                if game.lives <= 0 {
                    final_score.0 = game.score;
                    next.set(Phase::GameOver);
                }
            }
        }
    }
    for e in dead_bullets {
        commands.entity(e).despawn();
    }
    for e in dead_enemy_bullets {
        commands.entity(e).despawn();
    }
    for e in dead_rocks {
        commands.entity(e).despawn();
    }
}

fn waves(
    time: Res<Time>,
    mut commands: Commands,
    mut rng: ResMut<Rng>,
    mut game: ResMut<Game>,
    rocks: Query<&Transform, With<Rock>>,
    ships: Query<&Transform, With<Ship>>,
) {
    // Respawn the ship once its grace period passes AND the center is clear —
    // the timer keeps ticking, so the ship appears the moment a parked rock
    // drifts off the pad instead of materializing inside it.
    if game.waiting_spawn && game.lives > 0 {
        game.respawn.tick(time.delta());
        let center_clear = rocks.iter().all(|t| t.translation.truncate().length() > 90.0);
        if game.respawn.finished() && center_clear {
            game.waiting_spawn = false;
            commands.spawn((
                Ship {
                    vel: Vec2::ZERO,
                    angle: std::f32::consts::FRAC_PI_2,
                    invuln: Timer::from_seconds(2.0, TimerMode::Once),
                },
                Transform::from_xyz(0.0, 0.0, 4.0),
                GameTag,
            ));
        }
    }
    // Next wave after the board clears.
    if rocks.is_empty() {
        if !game.waiting_wave {
            game.waiting_wave = true;
            game.clear.reset();
        }
        game.clear.tick(time.delta());
        if game.clear.finished() {
            game.waiting_wave = false;
            let bonus = 300 + 100 * game.level;
            game.score += bonus; // wave-clear bonus
            game.level += 1;
            let avoid = ships.single().map(|t| t.translation.truncate()).unwrap_or(Vec2::ZERO);
            let level = game.level;
            popup(&mut commands, &format!("WAVE {level}"), 30.0, AMBER, Vec2::new(0.0, 60.0));
            popup(&mut commands, &format!("+{bonus}"), 20.0, WHITE, Vec2::new(0.0, 24.0));
            spawn_wave(&mut commands, &mut rng, level, avoid);
        }
    }
}

#[allow(clippy::type_complexity)]
fn draw(
    mut gizmos: Gizmos,
    game: Res<Game>,
    keys: Res<ButtonInput<KeyCode>>,
    time: Res<Time>,
    ships: Query<(&Ship, &Transform)>,
    rocks: Query<(&Rock, &Transform)>,
    bullets: Query<&Transform, With<Bullet>>,
    shards: Query<(&Shard, &Transform)>,
    saucers: Query<(&Saucer, &Transform)>,
    enemy_bullets: Query<&Transform, With<EnemyBullet>>,
    mut hud: Query<&mut Text2d, With<Hud>>,
) {
    // Ship: a classic three-line dart, blinking while invulnerable.
    if let Ok((ship, tf)) = ships.single() {
        let blink_off = !ship.invuln.finished() && (time.elapsed_secs() * 8.0) as i32 % 2 == 0;
        if !blink_off {
            let p = tf.translation.truncate();
            let a = ship.angle;
            let rot = |v: Vec2| Vec2::new(v.x * a.cos() - v.y * a.sin(), v.x * a.sin() + v.y * a.cos()) + p;
            let nose = rot(Vec2::new(14.0, 0.0));
            let left = rot(Vec2::new(-10.0, 9.0));
            let right = rot(Vec2::new(-10.0, -9.0));
            let notch = rot(Vec2::new(-6.0, 0.0));
            gizmos.linestrip_2d([nose, left, notch, right, nose], GREEN);
            if (keys.pressed(KeyCode::ArrowUp) || keys.pressed(KeyCode::ArrowDown))
                && (time.elapsed_secs() * 20.0) as i32 % 2 == 0
            {
                gizmos.linestrip_2d([rot(Vec2::new(-8.0, 4.0)), rot(Vec2::new(-16.0, 0.0)), rot(Vec2::new(-8.0, -4.0))], AMBER);
            }
        }
    }
    for (rock, tf) in &rocks {
        let p = tf.translation.truncate();
        let (s, c) = tf.rotation.to_euler(EulerRot::ZYX).0.sin_cos();
        let pts: Vec<Vec2> = rock
            .shape
            .iter()
            .map(|v| Vec2::new(v.x * c - v.y * s, v.x * s + v.y * c) + p)
            .chain(std::iter::once({
                let v = rock.shape[0];
                Vec2::new(v.x * c - v.y * s, v.x * s + v.y * c) + p
            }))
            .collect();
        gizmos.linestrip_2d(pts, WHITE);
    }
    for tf in &bullets {
        let p = tf.translation.truncate();
        gizmos.line_2d(p - Vec2::X * 2.0, p + Vec2::X * 2.0, AMBER);
        gizmos.line_2d(p - Vec2::Y * 2.0, p + Vec2::Y * 2.0, AMBER);
    }
    for (shard, tf) in &shards {
        let p = tf.translation.truncate();
        gizmos.line_2d(p - shard.dir * 5.0, p + shard.dir * 5.0, GREEN);
    }
    for (saucer, tf) in &saucers {
        let p = tf.translation.truncate();
        gizmos.linestrip_2d(saucer_outline(p, saucer.small), MAGENTA);
        let s = if saucer.small { 0.6 } else { 1.0 };
        gizmos.line_2d(p + Vec2::new(-10.0, 8.0) * s, p + Vec2::new(-5.0, 14.0) * s, MAGENTA);
        gizmos.line_2d(p + Vec2::new(10.0, 8.0) * s, p + Vec2::new(5.0, 14.0) * s, MAGENTA);
        gizmos.line_2d(p + Vec2::new(-5.0, 14.0) * s, p + Vec2::new(5.0, 14.0) * s, MAGENTA);
    }
    for tf in &enemy_bullets {
        let p = tf.translation.truncate();
        gizmos.line_2d(p - Vec2::splat(2.0), p + Vec2::splat(2.0), MAGENTA);
        gizmos.line_2d(p + Vec2::new(-2.0, 2.0), p + Vec2::new(2.0, -2.0), MAGENTA);
    }
    // Lives as little ship glyphs under the score.
    for i in 0..game.lives.max(0) {
        let x = -262.0 + i as f32 * 22.0;
        let y = 262.0;
        gizmos.linestrip_2d(
            [
                Vec2::new(x, y + 8.0),
                Vec2::new(x - 6.0, y - 6.0),
                Vec2::new(x + 6.0, y - 6.0),
                Vec2::new(x, y + 8.0),
            ],
            GREEN,
        );
    }
    if let Ok(mut t) = hud.single_mut() {
        let s = format!("{:06}", game.score);
        if t.0 != s {
            t.0 = s;
        }
    }
}

// ---- the saucer ----

/// Spawns, steers, and fires the saucer. It exists to punish camping: sit
/// still and the shots start landing.
fn saucer_run(
    time: Res<Time>,
    mut commands: Commands,
    mut rng: ResMut<Rng>,
    mut game: ResMut<Game>,
    mut saucers: Query<(Entity, &mut Saucer, &mut Transform), Without<Ship>>,
    ships: Query<&Transform, With<Ship>>,
    mut enemy_bullets: Query<(Entity, &mut EnemyBullet, &mut Transform), (Without<Saucer>, Without<Ship>)>,
) {
    let dt = time.delta_secs();
    // Spawn cadence: one saucer at a time, on a randomized timer.
    if saucers.is_empty() {
        game.saucer_clock.tick(time.delta());
        if game.saucer_clock.finished() {
            // Anti-lurk: the hunt tightens as the score grows — saucers come
            // sooner and shoot faster, so parking on one small rock turns
            // from a farm into a firing squad.
            let heat = (game.score / 2_000) as f32; // one step per 2k points
            let base = (16.0 - heat * 0.9).max(6.0);
            game.saucer_clock =
                Timer::from_seconds(rng.between(base * 0.85, base * 1.4), TimerMode::Once);
            let small = game.score >= 10_000 || rng.chance(0.25);
            let from_left = rng.chance(0.5);
            let y = rng.between(-220.0, 220.0);
            commands.spawn((
                Saucer {
                    vel: Vec2::new(if from_left { 90.0 } else { -90.0 }, 0.0),
                    small,
                    fire: Timer::from_seconds((1.15 - heat * 0.02).max(0.7), TimerMode::Repeating),
                    wobble: Timer::from_seconds(1.6, TimerMode::Repeating),
                },
                Transform::from_xyz(if from_left { -380.0 } else { 380.0 }, y, 3.5),
                GameTag,
            ));
            sfx("saucer");
        }
    }
    for (e, mut saucer, mut tf) in &mut saucers {
        // Occasional vertical lurch so it isn't a shooting-gallery duck.
        if saucer.wobble.tick(time.delta()).just_finished() {
            saucer.vel.y = rng.between(-60.0, 60.0);
        }
        tf.translation += (saucer.vel * dt).extend(0.0);
        // Leaves the far side rather than wrapping.
        if tf.translation.x.abs() > 400.0 {
            commands.entity(e).despawn();
            continue;
        }
        if tf.translation.y > 300.0 || tf.translation.y < -300.0 {
            saucer.vel.y = -saucer.vel.y;
        }
        if saucer.fire.tick(time.delta()).just_finished() {
            let origin = tf.translation.truncate();
            let dir = if saucer.small {
                // Aimed, with a little scatter that shrinks nothing: personal.
                match ships.single() {
                    Ok(stf) => {
                        // Aim scatter narrows with score: the hunter learns.
                        let scatter = (0.18 - game.score as f32 / 150_000.0).max(0.05);
                        let to = (stf.translation.truncate() - origin).normalize_or_zero();
                        let a = to.y.atan2(to.x) + rng.between(-scatter, scatter);
                        Vec2::new(a.cos(), a.sin())
                    }
                    Err(_) => {
                        let a = rng.between(0.0, std::f32::consts::TAU);
                        Vec2::new(a.cos(), a.sin())
                    }
                }
            } else {
                let a = rng.between(0.0, std::f32::consts::TAU);
                Vec2::new(a.cos(), a.sin())
            };
            commands.spawn((
                EnemyBullet {
                    vel: dir * 300.0,
                    ttl: Timer::from_seconds(1.6, TimerMode::Once),
                },
                Transform::from_translation(tf.translation + (dir * 20.0).extend(0.0)),
                GameTag,
            ));
        }
    }
    for (e, mut b, mut tf) in &mut enemy_bullets {
        tf.translation += (b.vel * dt).extend(0.0);
        wrap(&mut tf.translation);
        if b.ttl.tick(time.delta()).finished() {
            commands.entity(e).despawn();
        }
    }
}

fn saucer_outline(p: Vec2, small: bool) -> [Vec2; 7] {
    let s = if small { 0.6 } else { 1.0 };
    [
        p + Vec2::new(-24.0, 0.0) * s,
        p + Vec2::new(-10.0, 8.0) * s,
        p + Vec2::new(10.0, 8.0) * s,
        p + Vec2::new(24.0, 0.0) * s,
        p + Vec2::new(10.0, -8.0) * s,
        p + Vec2::new(-10.0, -8.0) * s,
        p + Vec2::new(-24.0, 0.0) * s,
    ]
}

// ---- attract-mode set dressing ----

fn attract_in(mut commands: Commands, mut rng: ResMut<Rng>) {
    for _ in 0..5 {
        let pos = Vec2::new(rng.between(-340.0, 340.0), rng.between(-300.0, -80.0));
        let a = rng.between(0.0, std::f32::consts::TAU);
        let radius = rng.between(18.0, 42.0);
        commands.spawn((
            AttractRock {
                vel: Vec2::new(a.cos(), a.sin()) * rng.between(20.0, 55.0),
                spin: rng.between(-0.8, 0.8),
                shape: rock_shape(&mut rng, radius),
            },
            Transform::from_xyz(pos.x, pos.y, 1.0),
        ));
    }
}

fn attract_out(mut commands: Commands, rocks: Query<Entity, With<AttractRock>>) {
    for e in &rocks {
        commands.entity(e).despawn();
    }
}

fn attract_drift(time: Res<Time>, mut rocks: Query<(&AttractRock, &mut Transform)>) {
    let dt = time.delta_secs();
    for (rock, mut tf) in &mut rocks {
        tf.translation += (rock.vel * dt).extend(0.0);
        tf.rotate_z(rock.spin * dt);
        wrap(&mut tf.translation);
    }
}

fn draw_attract(mut gizmos: Gizmos, rocks: Query<(&AttractRock, &Transform)>) {
    for (rock, tf) in &rocks {
        let p = tf.translation.truncate();
        let (s, c) = tf.rotation.to_euler(EulerRot::ZYX).0.sin_cos();
        let pts: Vec<Vec2> = rock
            .shape
            .iter()
            .chain(std::iter::once(&rock.shape[0]))
            .map(|v| Vec2::new(v.x * c - v.y * s, v.x * s + v.y * c) + p)
            .collect();
        gizmos.linestrip_2d(pts, DIM_ROCK);
    }
}

const DIM_ROCK: Color = Color::srgb(0.35, 0.35, 0.42);
