//! RED TAPE — a paddle-and-brick machine about the only way through
//! bureaucracy: repeatedly, at speed, from below. Original name, art, and
//! desk layouts; the bat-and-ball genre mechanics only. Forms shred in one
//! hit, red tape takes two, filing cabinets are load-bearing and immortal.

use bevy::prelude::*;

use crate::retro::{popup, text, AMBER, CYAN, DIM, GREEN, MAGENTA, WHITE};
use crate::rng::Rng;
use crate::shell::{sfx, stat};
use crate::{FinalScore, GameTag, Phase};

pub const BLURB: &[&str] = &[
    "CUT THROUGH THE PAPERWORK.",
    "MOUSE OR ARROWS STEER. SPACE SERVES (AND STAPLES).",
    "FORMS SHRED. TAPE RESISTS. CABINETS ENDURE.",
];

const PADDLE_Y: f32 = -262.0;
const PADDLE_W: f32 = 96.0;
const PADDLE_H: f32 = 14.0;
const BALL_R: f32 = 7.0;
const BALL_SPEED: f32 = 330.0;
const COLS: i32 = 12;
const BRICK_W: f32 = 56.0;
const BRICK_H: f32 = 22.0;
const FIELD_TOP: f32 = 268.0;
const WALL_X: f32 = 352.0;
const CEIL_Y: f32 = 296.0;
const FLOOR_Y: f32 = -318.0;

#[derive(Resource)]
struct Game {
    score: u32,
    lives: i32,
    wave: u32,
    bricks_left: u32, // breakable ones only
    next_extra: u32,
    /// Perk timers: paddle width, slow ball, stapler ammo-time.
    wide_t: f32,
    slow_t: f32,
    laser_t: f32,
    laser_cd: f32,
    serve_wait: bool, // a ball rides the paddle until Space
    over: Option<Timer>,
}

#[derive(Component)]
struct Paddle;

#[derive(Component)]
struct Ball {
    vel: Vec2,
    stuck: bool,
}

#[derive(Component)]
struct Brick {
    hits: i32, // -1 = filing cabinet (unbreakable)
    row: i32,
}

#[derive(Component)]
struct Perk {
    kind: u8, // 0 wide, 1 multi, 2 slow, 3 stapler, 4 extra life
}

#[derive(Component)]
struct Staple;

#[derive(Component)]
struct Hud;

pub struct RedTapePlugin;

impl Plugin for RedTapePlugin {
    fn build(&self, app: &mut App) {
        app.add_systems(OnEnter(Phase::Playing), setup).add_systems(
            Update,
            (steer, serve_and_staple, ball_run, perks_fall, staples_fly, hud, endgame)
                .chain()
                .run_if(in_state(Phase::Playing))
                .run_if(crate::unpaused),
        );
    }
}

fn perk_color(kind: u8) -> Color {
    match kind {
        0 => CYAN,
        1 => MAGENTA,
        2 => GREEN,
        3 => AMBER,
        _ => WHITE,
    }
}

/// Lays out one desk (wave). Patterns escalate: more tape, cabinets creep in.
fn spawn_wave(commands: &mut Commands, rng: &mut Rng, wave: u32) -> u32 {
    let rows = (4 + wave).min(8) as i32;
    let mut breakable = 0;
    for r in 0..rows {
        for c in 0..COLS {
            // Later waves salt the field: some tape, a few cabinets.
            let tape_chance = 0.08 + wave as f32 * 0.05;
            let cab_chance = if wave >= 2 { 0.02 + wave as f32 * 0.015 } else { 0.0 };
            let (hits, color) = if rng.chance(cab_chance.min(0.12)) {
                (-1, Color::srgb(0.32, 0.34, 0.40)) // filing cabinet
            } else if r % 3 == 1 && rng.chance(tape_chance.min(0.6)) {
                (2, Color::srgb(0.78, 0.18, 0.16)) // red tape: two hits
            } else {
                // Forms, tinted by row so the stack reads at a glance.
                let t = r as f32 / rows.max(2) as f32;
                (1, Color::srgb(0.75 - t * 0.25, 0.66 - t * 0.18, 0.45 - t * 0.10))
            };
            if hits > 0 {
                breakable += 1;
            }
            let x = -WALL_X + 8.0 + BRICK_W / 2.0 + c as f32 * (BRICK_W + 1.6);
            let y = FIELD_TOP - r as f32 * (BRICK_H + 2.0);
            commands.spawn((
                Sprite { color, custom_size: Some(Vec2::new(BRICK_W, BRICK_H)), ..default() },
                Transform::from_xyz(x, y, 2.0),
                Brick { hits, row: r },
                GameTag,
            ));
        }
    }
    breakable
}

fn spawn_ball(commands: &mut Commands, at: Vec2, vel: Vec2, stuck: bool) {
    commands.spawn((
        Sprite { color: WHITE, custom_size: Some(Vec2::splat(BALL_R * 2.0)), ..default() },
        Transform::from_xyz(at.x, at.y, 4.0),
        Ball { vel, stuck },
        GameTag,
    ));
}

fn setup(mut commands: Commands, mut rng: ResMut<Rng>) {
    let breakable = spawn_wave(&mut commands, &mut rng, 1);
    commands.insert_resource(Game {
        score: 0,
        lives: 3,
        wave: 1,
        bricks_left: breakable,
        next_extra: 8_000,
        wide_t: 0.0,
        slow_t: 0.0,
        laser_t: 0.0,
        laser_cd: 0.0,
        serve_wait: true,
        over: None,
    });
    commands.spawn((
        Sprite { color: GREEN, custom_size: Some(Vec2::new(PADDLE_W, PADDLE_H)), ..default() },
        Transform::from_xyz(0.0, PADDLE_Y, 3.0),
        Paddle,
        GameTag,
    ));
    spawn_ball(&mut commands, Vec2::new(0.0, PADDLE_Y + 14.0), Vec2::ZERO, true);
    // Side rails so the office reads as a well.
    for x in [-WALL_X - 4.0, WALL_X + 4.0] {
        commands.spawn((
            Sprite { color: DIM, custom_size: Some(Vec2::new(4.0, 620.0)), ..default() },
            Transform::from_xyz(x, -10.0, 1.0),
            GameTag,
        ));
    }
    let hud = text(&mut commands, "", 20.0, WHITE, Vec3::new(0.0, 306.0, 5.0));
    commands.entity(hud).insert((Hud, GameTag));
}

/// Mouse steers absolutely; arrows nudge. The paddle never leaves the well.
fn steer(
    time: Res<Time>,
    keys: Res<ButtonInput<KeyCode>>,
    windows: Query<&Window>,
    cameras: Query<(&Camera, &GlobalTransform)>,
    game: Res<Game>,
    mut paddles: Query<&mut Transform, With<Paddle>>,
    mut balls: Query<(&Ball, &mut Transform), Without<Paddle>>,
) {
    let Ok(mut pt) = paddles.single_mut() else { return };
    let half = paddle_half(&game);
    if let (Ok(window), Ok((camera, cam_tf))) = (windows.single(), cameras.single()) {
        if let Some(world) = crate::retro::cursor_world(window, camera, cam_tf) {
            pt.translation.x = world.x;
        }
    }
    let dir = i32::from(keys.pressed(KeyCode::ArrowRight))
        - i32::from(keys.pressed(KeyCode::ArrowLeft));
    pt.translation.x += dir as f32 * 460.0 * time.delta_secs();
    pt.translation.x = pt.translation.x.clamp(-WALL_X + half, WALL_X - half);
    // A stuck ball rides along.
    for (b, mut bt) in &mut balls {
        if b.stuck {
            bt.translation.x = pt.translation.x;
            bt.translation.y = PADDLE_Y + 14.0;
        }
    }
}

fn paddle_half(game: &Game) -> f32 {
    if game.wide_t > 0.0 {
        PADDLE_W * 0.75
    } else {
        PADDLE_W / 2.0
    }
}

fn serve_and_staple(
    keys: Res<ButtonInput<KeyCode>>,
    buttons: Res<ButtonInput<MouseButton>>,
    mut commands: Commands,
    mut game: ResMut<Game>,
    paddles: Query<&Transform, With<Paddle>>,
    mut balls: Query<(&mut Ball, &Transform), Without<Paddle>>,
) {
    if game.over.is_some() {
        return;
    }
    let fire = keys.just_pressed(KeyCode::Space) || buttons.just_pressed(MouseButton::Left);
    if !fire {
        return;
    }
    // Serve first if a ball is waiting.
    for (mut b, bt) in &mut balls {
        if b.stuck {
            b.stuck = false;
            let lean = (bt.translation.x / WALL_X).clamp(-0.6, 0.6);
            b.vel = Vec2::new(lean, 1.0).normalize() * BALL_SPEED;
            game.serve_wait = false;
            sfx("place");
            return;
        }
    }
    // Otherwise the stapler speaks, if it's loaded.
    if game.laser_t > 0.0 && game.laser_cd <= 0.0 {
        game.laser_cd = 0.22;
        stat("staples_fired", 1);
        sfx("fire");
        if let Ok(pt) = paddles.single() {
            let half = paddle_half(&game);
            for dx in [-half + 8.0, half - 8.0] {
                commands.spawn((
                    Sprite { color: AMBER, custom_size: Some(Vec2::new(4.0, 12.0)), ..default() },
                    Transform::from_xyz(pt.translation.x + dx, PADDLE_Y + 12.0, 4.0),
                    Staple,
                    GameTag,
                ));
            }
        }
    }
}

/// One brick down: score, perk roll, tape credit. Returns the brick's value.
#[allow(clippy::too_many_arguments)]
fn brick_down(
    commands: &mut Commands,
    rng: &mut Rng,
    game: &mut Game,
    entity: Entity,
    brick: &Brick,
    at: Vec3,
) {
    commands.entity(entity).despawn();
    game.bricks_left = game.bricks_left.saturating_sub(1);
    let value = 50 + (7 - brick.row.min(7)) as u32 * 10;
    game.score += value;
    stat("forms_shredded", 1);
    sfx("capture");
    if rng.chance(0.13) {
        let kind = match rng.range(10) {
            0 | 1 | 2 => 0u8,
            3 | 4 => 1,
            5 | 6 => 2,
            7 | 8 => 3,
            _ => 4,
        };
        commands.spawn((
            Sprite { color: perk_color(kind), custom_size: Some(Vec2::new(26.0, 14.0)), ..default() },
            Transform::from_xyz(at.x, at.y, 3.0),
            Perk { kind },
            GameTag,
        ));
    }
}

#[allow(clippy::too_many_arguments, clippy::type_complexity)]
fn ball_run(
    time: Res<Time>,
    mut commands: Commands,
    mut rng: ResMut<Rng>,
    mut game: ResMut<Game>,
    paddles: Query<&Transform, (With<Paddle>, Without<Ball>)>,
    mut balls: Query<(Entity, &mut Ball, &mut Transform), Without<Paddle>>,
    mut bricks: Query<(Entity, &mut Brick, &Transform, &mut Sprite), (Without<Ball>, Without<Paddle>)>,
) {
    if game.over.is_some() {
        return;
    }
    let dt = time.delta_secs();
    game.wide_t = (game.wide_t - dt).max(0.0);
    game.slow_t = (game.slow_t - dt).max(0.0);
    game.laser_t = (game.laser_t - dt).max(0.0);
    game.laser_cd = (game.laser_cd - dt).max(0.0);

    let Ok(pt) = paddles.single() else { return };
    let half = paddle_half(&game);
    let speed_mul = if game.slow_t > 0.0 { 0.68 } else { 1.0 }
        + (game.wave as f32 - 1.0) * 0.06;

    let mut live_balls = 0;
    for (be, mut ball, mut bt) in &mut balls {
        if ball.stuck {
            live_balls += 1;
            continue;
        }
        let step = ball.vel * speed_mul * dt;
        bt.translation.x += step.x;
        bt.translation.y += step.y;
        // Walls and ceiling.
        if bt.translation.x < -WALL_X + BALL_R {
            bt.translation.x = -WALL_X + BALL_R;
            ball.vel.x = ball.vel.x.abs();
            sfx("tick");
        } else if bt.translation.x > WALL_X - BALL_R {
            bt.translation.x = WALL_X - BALL_R;
            ball.vel.x = -ball.vel.x.abs();
            sfx("tick");
        }
        if bt.translation.y > CEIL_Y - BALL_R {
            bt.translation.y = CEIL_Y - BALL_R;
            ball.vel.y = -ball.vel.y.abs();
            sfx("tick");
        }
        // Paddle: position on the face steers the rebound (the whole game).
        if ball.vel.y < 0.0
            && (bt.translation.y - BALL_R) <= (PADDLE_Y + PADDLE_H / 2.0)
            && bt.translation.y > PADDLE_Y - 6.0
            && (bt.translation.x - pt.translation.x).abs() <= half + BALL_R
        {
            let lean = ((bt.translation.x - pt.translation.x) / half).clamp(-1.0, 1.0);
            let angle = lean * 1.05; // radians off vertical
            ball.vel = Vec2::new(angle.sin(), angle.cos()) * BALL_SPEED;
            bt.translation.y = PADDLE_Y + PADDLE_H / 2.0 + BALL_R;
            sfx("place");
        }
        // Bricks: nearest overlap wins; flip the axis of least penetration.
        let mut hit: Option<(Entity, Vec3, f32, f32)> = None;
        for (e, brick, tf, _) in &bricks {
            let dx = bt.translation.x - tf.translation.x;
            let dy = bt.translation.y - tf.translation.y;
            if dx.abs() <= BRICK_W / 2.0 + BALL_R && dy.abs() <= BRICK_H / 2.0 + BALL_R {
                let _ = brick;
                hit = Some((e, tf.translation, dx, dy));
                break;
            }
        }
        if let Some((e, at, dx, dy)) = hit {
            if let Ok((_, mut brick, _, mut sprite)) = bricks.get_mut(e) {
                let px = BRICK_W / 2.0 + BALL_R - dx.abs();
                let py = BRICK_H / 2.0 + BALL_R - dy.abs();
                if px < py {
                    ball.vel.x = if dx > 0.0 { ball.vel.x.abs() } else { -ball.vel.x.abs() };
                } else {
                    ball.vel.y = if dy > 0.0 { ball.vel.y.abs() } else { -ball.vel.y.abs() };
                }
                match brick.hits {
                    -1 => sfx("drop"), // the cabinet doesn't care
                    2 => {
                        brick.hits = 1;
                        sprite.color = Color::srgb(0.55, 0.13, 0.12);
                        game.score += 20;
                        stat("tape_cut", 1);
                        sfx("rotate");
                    }
                    _ => {
                        let b = Brick { hits: brick.hits, row: brick.row };
                        brick_down(&mut commands, &mut rng, &mut game, e, &b, at);
                    }
                }
            }
        }
        // The floor takes the rest.
        if bt.translation.y < FLOOR_Y {
            commands.entity(be).despawn();
            continue;
        }
        live_balls += 1;
    }

    // No balls in play: pay a life, rack a fresh serve.
    if live_balls == 0 {
        game.lives -= 1;
        stat("balls_dropped", 1);
        sfx("death");
        if game.lives >= 0 {
            spawn_ball(&mut commands, Vec2::new(pt.translation.x, PADDLE_Y + 14.0), Vec2::ZERO, true);
            game.serve_wait = true;
        } else {
            game.over = Some(Timer::from_seconds(2.2, TimerMode::Once));
            popup(&mut commands, "OUT OF PATIENCE", 30.0, AMBER, Vec2::new(0.0, 0.0));
            sfx("over");
        }
    }

    // Desk cleared: bonus, next wave, fresh serve.
    if game.bricks_left == 0 && game.over.is_none() {
        game.wave += 1;
        game.score += 500 + game.wave * 100;
        stat("desks_cleared", 1);
        sfx("levelup");
        for (e, _, _, _) in &bricks {
            commands.entity(e).despawn(); // clear leftover cabinets
        }
        for (be, _, _) in &balls {
            commands.entity(be).despawn();
        }
        game.bricks_left = spawn_wave(&mut commands, &mut rng, game.wave);
        spawn_ball(&mut commands, Vec2::new(pt.translation.x, PADDLE_Y + 14.0), Vec2::ZERO, true);
        game.serve_wait = true;
        popup(&mut commands, &format!("DESK {} CLEARED", game.wave - 1), 28.0, GREEN, Vec2::new(0.0, 40.0));
    }

    // Extra life ladder.
    if game.score >= game.next_extra {
        game.next_extra += 8_000;
        game.lives += 1;
        stat("extra_balls", 1);
        sfx("extra");
    }
}

fn perks_fall(
    time: Res<Time>,
    mut commands: Commands,
    mut game: ResMut<Game>,
    paddles: Query<&Transform, With<Paddle>>,
    mut perks: Query<(Entity, &Perk, &mut Transform), Without<Paddle>>,
    balls: Query<(&Ball, &Transform), (Without<Paddle>, Without<Perk>)>,
) {
    let Ok(pt) = paddles.single() else { return };
    let half = paddle_half(&game);
    let dt = time.delta_secs();
    let mut split_from: Option<(Vec2, Vec2)> = None;
    for (e, perk, mut tf) in &mut perks {
        tf.translation.y -= 130.0 * dt;
        if tf.translation.y < FLOOR_Y {
            commands.entity(e).despawn();
            continue;
        }
        let caught = (tf.translation.y - PADDLE_Y).abs() < 14.0
            && (tf.translation.x - pt.translation.x).abs() <= half + 14.0;
        if caught {
            commands.entity(e).despawn();
            stat("perks_caught", 1);
            sfx("power");
            match perk.kind {
                0 => game.wide_t = 20.0,
                1 => {
                    // Split the fastest live ball three ways.
                    if let Some((b, bt)) = balls.iter().find(|(b, _)| !b.stuck) {
                        split_from = Some((bt.translation.truncate(), b.vel));
                    }
                }
                2 => game.slow_t = 12.0,
                3 => game.laser_t = 12.0,
                _ => {
                    game.lives += 1;
                    stat("extra_balls", 1);
                }
            }
        }
    }
    if let Some((at, vel)) = split_from {
        for angle in [-0.5f32, 0.5] {
            let rot = Vec2::new(
                vel.x * angle.cos() - vel.y * angle.sin(),
                vel.x * angle.sin() + vel.y * angle.cos(),
            );
            spawn_ball(&mut commands, at, rot, false);
        }
    }
}

fn staples_fly(
    time: Res<Time>,
    mut commands: Commands,
    mut rng: ResMut<Rng>,
    mut game: ResMut<Game>,
    mut staples: Query<(Entity, &mut Transform), (With<Staple>, Without<Brick>)>,
    mut bricks: Query<(Entity, &mut Brick, &Transform, &mut Sprite), Without<Staple>>,
) {
    let dt = time.delta_secs();
    for (se, mut tf) in &mut staples {
        tf.translation.y += 540.0 * dt;
        if tf.translation.y > CEIL_Y {
            commands.entity(se).despawn();
            continue;
        }
        for (e, mut brick, btf, mut sprite) in &mut bricks {
            if (tf.translation.x - btf.translation.x).abs() <= BRICK_W / 2.0
                && (tf.translation.y - btf.translation.y).abs() <= BRICK_H / 2.0 + 6.0
            {
                match brick.hits {
                    -1 => sfx("drop"),
                    2 => {
                        brick.hits = 1;
                        sprite.color = Color::srgb(0.55, 0.13, 0.12);
                        game.score += 20;
                        stat("tape_cut", 1);
                        sfx("rotate");
                    }
                    _ => {
                        let b = Brick { hits: brick.hits, row: brick.row };
                        brick_down(&mut commands, &mut rng, &mut game, e, &b, btf.translation);
                    }
                }
                commands.entity(se).despawn();
                break;
            }
        }
    }
}

fn hud(game: Res<Game>, mut hud: Query<&mut Text2d, With<Hud>>) {
    if let Ok(mut t) = hud.single_mut() {
        let mut tags = String::new();
        if game.wide_t > 0.0 {
            tags.push_str("  [WIDE]");
        }
        if game.slow_t > 0.0 {
            tags.push_str("  [SLOW]");
        }
        if game.laser_t > 0.0 {
            tags.push_str("  [STAPLER]");
        }
        let serve = if game.serve_wait { "  SPACE SERVES" } else { "" };
        let s = format!(
            "SCORE {}   DESK {}   BACKUPS {}{}{}",
            game.score,
            game.wave,
            game.lives.max(0),
            tags,
            serve
        );
        if t.0 != s {
            t.0 = s;
        }
    }
}

fn endgame(
    time: Res<Time>,
    mut game: ResMut<Game>,
    mut final_score: ResMut<FinalScore>,
    mut next: ResMut<NextState<Phase>>,
) {
    if let Some(t) = game.over.as_mut() {
        if t.tick(time.delta()).finished() {
            final_score.0 = game.score;
            next.set(Phase::GameOver);
        }
    }
}

