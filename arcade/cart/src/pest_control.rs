//! PEST CONTROL — a reflex swatting game, built from scratch: original
//! name, break room, bug roster, boss, and scoring. Somebody left a donut
//! out and now it's YOUR problem. Genre mechanics only: a mouse-driven
//! swatter, things that buzz, and the eternal whiff.
//!
//! THE SCORING (a skill contest, not a clickfest):
//!   kill value × the STREAK (consecutive connecting swats: ×1 … ×5;
//!   a whiffed swat resets it — so swinging wildly is the losing play),
//!   MULTI-KILL: two or more bugs under one swat doubles each of them,
//!   wave-clear pays a speed bonus, and the hornet pays big × streak.
//! WASPS enforce discipline: swat one while it flashes red and it stings —
//! a band-aid gone AND the streak. Wait for the calm amber beat.

use bevy::prelude::*;

use crate::retro::{popup, text, AMBER, CYAN, DIM, GREEN, MAGENTA, RED, WHITE};
use crate::rng::Rng;
use crate::shell::{sfx, stat};
use crate::{FinalScore, GameTag, Phase};

pub const BLURB: &[&str] = &[
    "SOMEBODY LEFT A DONUT OUT.",
    "MOUSE MOVES THE SWATTER. CLICK SWATS. DON'T WHIFF.",
    "WASPS FLASH RED: WAIT FOR THE CALM. STREAKS PAY X5.",
];

const SWAT_RADIUS: f32 = 34.0;
const SWAT_RECOVERY: f32 = 0.28;
const BANDAIDS: i32 = 3;

#[derive(Clone, Copy, PartialEq)]
enum BugKind {
    Gnat,  // 10 — tiny, jittery
    Fly,   // 25 — sine cruiser
    Wasp,  // 75 — swat ONLY while calm; red = sting
    Hornet, // the boss: 8 hits, dives at the swatter
}

struct Bug {
    kind: BugKind,
    pos: Vec2,
    vel: Vec2,
    phase: f32,
    hp: i32,
    /// Wasps: >0 calm (amber), <0 enraged (red). Hornet: >0 circling,
    /// <0 diving, and a short dazed window (green) right after a dive.
    mood: f32,
    dazed: f32,
    ent: Entity,
}

#[derive(Resource)]
struct Room {
    bugs: Vec<Bug>,
    score: u32,
    streak: u32,
    bandaids: i32,
    shift: u32,
    wave: u32,
    wave_quota: u32, // bugs left to spawn this wave
    spawn_t: f32,
    wave_clock: f32,
    swat_cd: f32,
    boss_up: bool,
    over: Option<Timer>,
}

#[derive(Component)]
struct Swatter;

/// A fleck of ex-bug.
#[derive(Component)]
struct BugBit {
    vel: Vec2,
    ttl: f32,
}

fn bug_bits(time: Res<Time>, mut commands: Commands, mut bits: Query<(Entity, &mut BugBit, &mut Transform)>) {
    let dt = time.delta_secs();
    for (e, mut b, mut tf) in &mut bits {
        b.ttl -= dt;
        if b.ttl <= 0.0 {
            commands.entity(e).despawn();
            continue;
        }
        b.vel.y -= 260.0 * dt;
        tf.translation.x += b.vel.x * dt;
        tf.translation.y += b.vel.y * dt;
    }
}

#[derive(Component)]
struct Hud;

pub struct PestPlugin;

impl Plugin for PestPlugin {
    fn build(&self, app: &mut App) {
        app.add_systems(OnEnter(Phase::Playing), setup).add_systems(
            Update,
            (swat, buzz, waves, bug_bits, hud, endgame)
                .chain()
                .run_if(in_state(Phase::Playing))
                .run_if(crate::unpaused),
        );
    }
}

fn bug_value(kind: BugKind) -> u32 {
    match kind {
        BugKind::Gnat => 10,
        BugKind::Fly => 25,
        BugKind::Wasp => 75,
        BugKind::Hornet => 500,
    }
}

fn bug_size(kind: BugKind) -> Vec2 {
    match kind {
        BugKind::Gnat => Vec2::splat(9.0),
        BugKind::Fly => Vec2::splat(15.0),
        BugKind::Wasp => Vec2::new(18.0, 13.0),
        BugKind::Hornet => Vec2::new(34.0, 24.0),
    }
}

fn setup(mut commands: Commands) {
    commands.insert_resource(Room {
        bugs: Vec::new(),
        score: 0,
        streak: 1,
        bandaids: BANDAIDS,
        shift: 1,
        wave: 1,
        wave_quota: 8,
        spawn_t: 0.5,
        wave_clock: 0.0,
        swat_cd: 0.0,
        boss_up: false,
        over: None,
    });
    // The break room: a table band and the fateful donut.
    commands.spawn((
        Sprite { color: Color::srgb(0.16, 0.12, 0.09), custom_size: Some(Vec2::new(720.0, 150.0)), ..default() },
        Transform::from_xyz(0.0, -240.0, 0.5),
        GameTag,
    ));
    commands.spawn((
        Sprite { color: Color::srgb(0.75, 0.55, 0.30), custom_size: Some(Vec2::splat(46.0)), ..default() },
        Transform::from_xyz(150.0, -220.0, 1.0).with_rotation(Quat::from_rotation_z(0.785)),
        GameTag,
    ));
    commands.spawn((
        Sprite { color: MAGENTA, custom_size: Some(Vec2::splat(22.0)), ..default() },
        Transform::from_xyz(150.0, -220.0, 1.1).with_rotation(Quat::from_rotation_z(0.785)),
        GameTag,
    ));
    // The swatter: a handle and a mesh head that rides the cursor.
    commands
        .spawn((
            Sprite { color: Color::srgb(0.85, 0.2, 0.2), custom_size: Some(Vec2::new(46.0, 46.0)), ..default() },
            Transform::from_xyz(0.0, 0.0, 6.0),
            Swatter,
            GameTag,
        ))
        .with_children(|kid| {
            kid.spawn((
                Sprite { color: Color::srgb(0.55, 0.35, 0.2), custom_size: Some(Vec2::new(9.0, 54.0)), ..default() },
                Transform::from_xyz(18.0, -44.0, -0.1).with_rotation(Quat::from_rotation_z(-0.5)),
            ));
            for gy in [-12.0f32, 0.0, 12.0] {
                kid.spawn((
                    Sprite { color: Color::srgb(0.6, 0.12, 0.12), custom_size: Some(Vec2::new(42.0, 2.0)), ..default() },
                    Transform::from_xyz(0.0, gy, 0.1),
                ));
            }
        });
    let hud = text(&mut commands, "", 18.0, WHITE, Vec3::new(0.0, 300.0, 5.0));
    commands.entity(hud).insert((Hud, GameTag));
}

fn spawn_bug(commands: &mut Commands, rng: &mut Rng, kind: BugKind) -> Bug {
    let side = rng.chance(0.5);
    let pos = Vec2::new(if side { -380.0 } else { 380.0 }, 100.0 + rng.range(180) as f32 - 90.0);
    let ent = commands
        .spawn((
            Sprite { color: DIM, custom_size: Some(bug_size(kind)), ..default() },
            Transform::from_xyz(pos.x, pos.y, 3.0),
            GameTag,
        ))
        .id();
    Bug {
        kind,
        pos,
        vel: Vec2::new(if side { 1.0 } else { -1.0 } * (60.0 + rng.range(80) as f32), 0.0),
        phase: rng.range(628) as f32 / 100.0,
        hp: if kind == BugKind::Hornet { 8 } else { 1 },
        mood: 1.5 + rng.range(20) as f32 / 10.0,
        dazed: 0.0,
        ent,
    }
}

/// The swat: recovery-gated, streak-scored, multi-kill aware.
#[allow(clippy::too_many_arguments)]
fn swat(
    time: Res<Time>,
    buttons: Res<ButtonInput<MouseButton>>,
    windows: Query<&Window>,
    cameras: Query<(&Camera, &GlobalTransform)>,
    mut commands: Commands,
    mut room: ResMut<Room>,
    mut swatter: Query<&mut Transform, With<Swatter>>,
) {
    let dt = time.delta_secs();
    room.swat_cd = (room.swat_cd - dt).max(0.0);
    let mut cursor = None;
    if let (Ok(window), Ok((camera, cam_tf))) = (windows.single(), cameras.single()) {
        cursor = crate::retro::cursor_world(window, camera, cam_tf);
    }
    let Some(at) = cursor else { return };
    if let Ok(mut tf) = swatter.single_mut() {
        tf.translation.x = at.x;
        tf.translation.y = at.y;
        // A little cock-back while recovering sells the slap.
        tf.rotation = Quat::from_rotation_z(if room.swat_cd > 0.0 { -0.5 } else { 0.0 });
    }
    if room.over.is_some() || !buttons.just_pressed(MouseButton::Left) || room.swat_cd > 0.0 {
        return;
    }
    room.swat_cd = SWAT_RECOVERY;
    stat("swats", 1);
    sfx("drop");
    // Collect everything under the swat.
    let mut killed: Vec<usize> = Vec::new();
    let mut stung = false;
    let mut boss_hit = false;
    for (i, b) in room.bugs.iter_mut().enumerate() {
        if b.pos.distance(at) > SWAT_RADIUS + bug_size(b.kind).x / 2.0 {
            continue;
        }
        match b.kind {
            BugKind::Wasp if b.mood < 0.0 => stung = true,
            BugKind::Hornet => {
                if b.mood > 0.0 || b.dazed > 0.0 {
                    b.hp -= if b.dazed > 0.0 { 2 } else { 1 };
                    boss_hit = true;
                    if b.hp <= 0 {
                        killed.push(i);
                    }
                }
            }
            _ => killed.push(i),
        }
    }
    if stung {
        room.bandaids -= 1;
        room.streak = 1;
        stat("stings", 1);
        popup(&mut commands, "STUNG! IT WAS RED!", 20.0, RED, at + Vec2::new(0.0, 40.0));
        sfx("death");
        if room.bandaids <= 0 && room.over.is_none() {
            room.over = Some(Timer::from_seconds(2.2, TimerMode::Once));
            popup(&mut commands, "OUT OF BAND-AIDS", 26.0, RED, Vec2::new(0.0, 0.0));
            sfx("over");
        }
        return;
    }
    if killed.is_empty() && !boss_hit {
        // The whiff: the streak dies in the air.
        if room.streak > 1 {
            popup(&mut commands, "WHIFF", 16.0, DIM, at + Vec2::new(0.0, 34.0));
        }
        room.streak = 1;
        stat("whiffs", 1);
        return;
    }
    let multi = killed.len() >= 2;
    if multi {
        stat("multi_kills", 1);
    }
    let mut total = 0u32;
    for &i in killed.iter().rev() {
        let b = room.bugs.remove(i);
        commands.entity(b.ent).despawn();
        let pay = bug_value(b.kind) * room.streak * if multi { 2 } else { 1 };
        total += pay;
        stat(if b.kind == BugKind::Hornet { "hornets_downed" } else { "bugs_swatted" }, 1);
        if b.kind == BugKind::Hornet {
            room.boss_up = false;
            popup(&mut commands, &format!("HORNET DOWN +{pay}"), 24.0, GREEN, b.pos);
            sfx("win");
        }
        // Little burst of bug bits.
        for k in 0..8u32 {
            let a = k as f32 * 0.785;
            commands.spawn((
                Sprite { color: DIM, custom_size: Some(Vec2::splat(2.0)), ..default() },
                Transform::from_xyz(b.pos.x, b.pos.y, 4.0),
                BugBit { vel: Vec2::new(a.cos(), a.sin()) * 90.0, ttl: 0.4 },
                GameTag,
            ));
        }
    }
    if boss_hit && total == 0 {
        sfx("capture"); // a dent in the hornet
        room.score += 50 * room.streak;
    }
    if total > 0 {
        room.score += total;
        popup(
            &mut commands,
            &format!("+{total}{}", if multi { " MULTI!" } else { "" }),
            16.0,
            AMBER,
            at + Vec2::new(0.0, 34.0),
        );
        sfx("capture");
    }
    room.streak = (room.streak + 1).min(5);
}

/// Bug brains: cruising, jitter, wasp moods, and the hornet's dive.
fn buzz(
    time: Res<Time>,
    mut rng: ResMut<Rng>,
    mut commands: Commands,
    mut room: ResMut<Room>,
    swatter: Query<&Transform, With<Swatter>>,
    mut tfs: Query<&mut Transform, Without<Swatter>>,
    mut sprites: Query<&mut Sprite>,
) {
    if room.over.is_some() {
        return;
    }
    let dt = time.delta_secs();
    let swat_at = swatter.single().map(|t| t.translation.truncate()).unwrap_or(Vec2::ZERO);
    let mut sting_player = false;
    for b in room.bugs.iter_mut() {
        b.phase += dt;
        match b.kind {
            BugKind::Gnat => {
                // Jitter: constant nervous redirection.
                if rng.chance(0.08) {
                    let a = rng.range(628) as f32 / 100.0;
                    b.vel = Vec2::new(a.cos(), a.sin()) * (110.0 + rng.range(70) as f32);
                }
            }
            BugKind::Fly => {
                b.vel.y = (b.phase * 3.0).sin() * 70.0;
            }
            BugKind::Wasp => {
                b.mood -= dt;
                if b.mood < -1.2 {
                    b.mood = 1.2 + rng.range(18) as f32 / 10.0; // calm again
                }
                b.vel.y = (b.phase * 2.0).sin() * 40.0;
            }
            BugKind::Hornet => {
                b.mood -= dt;
                b.dazed = (b.dazed - dt).max(0.0);
                if b.mood > 0.0 {
                    // Circle the room's middle, eyeing the swatter.
                    let target = Vec2::new((b.phase * 1.3).cos() * 220.0, 80.0 + (b.phase * 1.7).sin() * 90.0);
                    b.vel = (target - b.pos) * 2.0;
                } else if b.mood > -0.8 {
                    // The dive: straight at the swatter's last position.
                    b.vel = (swat_at - b.pos).normalize_or_zero() * 520.0;
                    if b.pos.distance(swat_at) < 30.0 {
                        sting_player = true;
                        b.mood = 2.0 + rng.range(15) as f32 / 10.0;
                        b.dazed = 0.0;
                    }
                } else {
                    // Pulled out of the dive: dazed and swattable for double.
                    b.dazed = 1.0;
                    b.mood = 2.2 + rng.range(15) as f32 / 10.0;
                    b.vel = Vec2::new(0.0, 60.0);
                }
            }
        }
        b.pos += b.vel * dt;
        b.pos.x = b.pos.x.clamp(-350.0, 350.0);
        b.pos.y = b.pos.y.clamp(-180.0, 290.0);
        if let Ok(mut tf) = tfs.get_mut(b.ent) {
            tf.translation.x = b.pos.x;
            tf.translation.y = b.pos.y;
        }
        if let Ok(mut sp) = sprites.get_mut(b.ent) {
            sp.color = match b.kind {
                BugKind::Gnat => Color::srgb(0.55, 0.55, 0.6),
                BugKind::Fly => Color::srgb(0.35, 0.45, 0.55),
                BugKind::Wasp => {
                    if b.mood < 0.0 {
                        if (b.phase * 10.0) as i32 % 2 == 0 { RED } else { AMBER }
                    } else {
                        AMBER
                    }
                }
                BugKind::Hornet => {
                    if b.dazed > 0.0 {
                        GREEN
                    } else if b.mood < 0.0 {
                        RED
                    } else {
                        Color::srgb(0.9, 0.6, 0.1)
                    }
                }
            };
        }
    }
    if sting_player {
        room.bandaids -= 1;
        room.streak = 1;
        stat("stings", 1);
        popup(&mut commands, "DIVE-BOMBED!", 22.0, RED, swat_at + Vec2::new(0.0, 40.0));
        sfx("death");
        if room.bandaids <= 0 && room.over.is_none() {
            room.over = Some(Timer::from_seconds(2.2, TimerMode::Once));
            popup(&mut commands, "OUT OF BAND-AIDS", 26.0, RED, Vec2::new(0.0, 0.0));
            sfx("over");
        }
    }
}

/// Wave pacing: quotas, the hornet, shift roll-over, speed bonuses.
fn waves(time: Res<Time>, mut rng: ResMut<Rng>, mut commands: Commands, mut room: ResMut<Room>) {
    if room.over.is_some() {
        return;
    }
    let dt = time.delta_secs();
    room.wave_clock += dt;
    room.spawn_t -= dt;
    if room.wave_quota > 0 && room.spawn_t <= 0.0 && room.bugs.len() < 9 {
        room.spawn_t = (1.1 - room.shift as f32 * 0.12).max(0.35);
        room.wave_quota -= 1;
        let roll = rng.range(10);
        let kind = if roll < 5 {
            BugKind::Gnat
        } else if roll < 8 {
            BugKind::Fly
        } else {
            BugKind::Wasp
        };
        let bug = spawn_bug(&mut commands, &mut rng, kind);
        room.bugs.push(bug);
    }
    // Wave cleared?
    if room.wave_quota == 0 && room.bugs.is_empty() {
        if room.wave < 3 {
            // Speed bonus: the faster the sweep, the fatter the check.
            let bonus = ((45.0 - room.wave_clock).max(0.0) * 10.0) as u32;
            if bonus > 0 {
                room.score += bonus;
                popup(&mut commands, &format!("WAVE CLEAR +{bonus}"), 20.0, GREEN, Vec2::new(0.0, 40.0));
            }
            room.wave += 1;
            room.wave_quota = 6 + room.wave * 2 + room.shift;
            room.wave_clock = 0.0;
            stat("waves_cleared", 1);
            sfx("levelup");
        } else if !room.boss_up {
            // The hornet arrives.
            room.boss_up = true;
            let boss = spawn_bug(&mut commands, &mut rng, BugKind::Hornet);
            room.bugs.push(boss);
            popup(&mut commands, "THE HORNET.", 28.0, RED, Vec2::new(0.0, 60.0));
            sfx("buzz");
        } else {
            // Boss down: next shift, meaner and faster.
            room.boss_up = false;
            room.shift += 1;
            room.wave = 1;
            room.wave_quota = 8 + room.shift * 2;
            room.wave_clock = 0.0;
            room.score += 1000;
            stat("shifts_cleared", 1);
            popup(&mut commands, &format!("SHIFT {} - IT GETS WORSE", room.shift), 24.0, CYAN, Vec2::new(0.0, 0.0));
            sfx("win");
        }
    }
}

fn hud(room: Res<Room>, mut hud: Query<&mut Text2d, With<Hud>>) {
    if let Ok(mut t) = hud.single_mut() {
        let s = format!(
            "SCORE {}   STREAK X{}   BAND-AIDS {}   SHIFT {} WAVE {}",
            room.score,
            room.streak,
            room.bandaids.max(0),
            room.shift,
            room.wave
        );
        if t.0 != s {
            t.0 = s;
        }
    }
}

fn endgame(
    time: Res<Time>,
    mut room: ResMut<Room>,
    mut final_score: ResMut<FinalScore>,
    mut next: ResMut<NextState<Phase>>,
) {
    if let Some(t) = room.over.as_mut() {
        if t.tick(time.delta()).finished() {
            final_score.0 = room.score;
            next.set(Phase::GameOver);
        }
    }
}
