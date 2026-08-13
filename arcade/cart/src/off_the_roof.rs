//! OFF THE ROOF — an original spitting-distance game, no brands attached.
//! You look straight DOWN from the roof ledge: your balcony runs along the
//! bottom of the screen and the mouse slides you left and right along it.
//! The street stretches away above — near sidewalk, then the road, then the
//! far sidewalk — and pedestrians, cars, and one smug pigeon cross it.
//!
//! HOLD the left button to wind up: the longer the hold, the farther the
//! gob carries (a marker crawls up the street showing where it will land)
//! and the heavier it hits — heavier gobs score more. Hold too long and it
//! dribbles down the ledge. RIGHT button winds up a MEGA GOB: costs three,
//! splashes wide, triple weight. Fifteen gobs; make them count.
//!
//! SCORING: target base x lane (x1 near / x2 road / x3 far) x weight
//! (x1.0-x2.0 by charge, x3 mega) x chain (consecutive hits, up to x5).
//! Dead-center bullseyes double. The manhole swish pays 500 and refunds 3.

use bevy::prelude::*;

use crate::retro::{cursor_world, popup, text, AMBER, CYAN, DIM, GREEN, RED, WHITE};
use crate::rng::Rng;
use crate::shell::{sfx, stat};
use crate::{FinalScore, GameTag, Phase};

pub const BLURB: &[&str] = &[
    "FIFTEEN GOBS. ONE STREET. NO WITNESSES.",
    "MOUSE SLIDES YOU ALONG THE LEDGE. HOLD LEFT TO WIND UP - LONGER = FARTHER",
    "AND HEAVIER (MORE POINTS). TOO LONG = DRIBBLE. RIGHT = MEGA (COSTS 3).",
];

const GOBS_START: i32 = 15;
const CHARGE_MAX: f32 = 1.5;
const OVERCHARGE: f32 = 1.78;
const LEDGE_Y: f32 = -262.0;
const STREET_NEAR: f32 = -228.0; // where the pavement starts
const STREET_FAR: f32 = 296.0;

fn land_y(frac: f32) -> f32 {
    STREET_NEAR + frac * (STREET_FAR - STREET_NEAR - 8.0)
}

/// Lane multiplier by landing depth: near sidewalk, road, far sidewalk.
fn lane_mult(y: f32) -> u32 {
    if y < -56.0 {
        1
    } else if y < 128.0 {
        2
    } else {
        3
    }
}

#[derive(Clone, Copy, PartialEq)]
enum Kind {
    Mailbox,
    Walker,
    Car,
    HatGuy,
    Pigeon,
    Manhole,
}

struct Target {
    kind: Kind,
    pos: Vec2,
    vel_x: f32,
    radius: f32,
    base: u32,
    wait: f32, // off-screen respawn delay
    ent: Entity,
}

struct Flight {
    from: Vec2,
    to: Vec2,
    t: f32,
    dur: f32,
    mega: bool,
    weight: f32,
    dribble: bool,
    ent: Entity,
    shadow: Entity,
}

#[derive(Resource)]
struct Roof {
    gobs: i32,
    score: u32,
    chain: u32,
    charge: Option<(f32, bool)>, // held seconds, mega?
    targets: Vec<Target>,
    flights: Vec<Flight>,
    splats: Vec<(Entity, f32)>,
    over: Option<Timer>,
    result: String,
    player: Entity,
    marker: Entity,
    meter: Entity,
}

#[derive(Component)]
struct Hud;

pub struct RoofPlugin;

impl Plugin for RoofPlugin {
    fn build(&self, app: &mut App) {
        app.add_systems(OnEnter(Phase::Playing), setup).add_systems(
            Update,
            (aim, gobs_fly, targets_move, hud, endgame)
                .chain()
                .run_if(in_state(Phase::Playing))
                .run_if(crate::unpaused),
        );
    }
}

fn spawn_target(commands: &mut Commands, kind: Kind, pos: Vec2, vel_x: f32) -> Target {
    let (color, size, radius, base) = match kind {
        Kind::Mailbox => (Color::srgb(0.25, 0.35, 0.75), Vec2::new(22.0, 22.0), 16.0, 50),
        Kind::Walker => (Color::srgb(0.75, 0.60, 0.45), Vec2::new(14.0, 20.0), 15.0, 100),
        Kind::Car => (Color::srgb(0.70, 0.25, 0.20), Vec2::new(48.0, 24.0), 24.0, 150),
        Kind::HatGuy => (Color::srgb(0.45, 0.65, 0.50), Vec2::new(14.0, 20.0), 15.0, 200),
        Kind::Pigeon => (Color::srgb(0.62, 0.62, 0.70), Vec2::new(11.0, 9.0), 11.0, 300),
        Kind::Manhole => (Color::srgb(0.10, 0.10, 0.12), Vec2::new(30.0, 22.0), 15.0, 500),
    };
    let ent = commands
        .spawn((
            Sprite { color, custom_size: Some(size), ..default() },
            Transform::from_translation(pos.extend(2.0)),
            GameTag,
        ))
        .with_children(|kid| {
            match kind {
                Kind::Walker | Kind::HatGuy => {
                    // A head from above; the hat guy's hat is the point.
                    let head = if kind == Kind::HatGuy { AMBER } else { Color::srgb(0.85, 0.70, 0.55) };
                    kid.spawn((
                        Sprite { color: head, custom_size: Some(Vec2::splat(9.0)), ..default() },
                        Transform::from_xyz(0.0, 2.0, 0.2),
                    ));
                }
                Kind::Car => {
                    kid.spawn((
                        Sprite { color: Color::srgb(0.85, 0.45, 0.40), custom_size: Some(Vec2::new(22.0, 18.0)), ..default() },
                        Transform::from_xyz(-4.0, 0.0, 0.2),
                    ));
                }
                Kind::Mailbox => {
                    kid.spawn((
                        Sprite { color: WHITE.with_alpha(0.7), custom_size: Some(Vec2::new(12.0, 3.0)), ..default() },
                        Transform::from_xyz(0.0, 4.0, 0.2),
                    ));
                }
                Kind::Manhole => {
                    kid.spawn((
                        Sprite { color: Color::srgb(0.22, 0.22, 0.26), custom_size: Some(Vec2::new(20.0, 13.0)), ..default() },
                        Transform::from_xyz(0.0, 0.0, 0.2),
                    ));
                }
                Kind::Pigeon => {}
            }
        })
        .id();
    Target { kind, pos, vel_x, radius, base, wait: 0.0, ent }
}

fn setup(mut commands: Commands, mut rng: ResMut<Rng>) {
    // The street, from the roof: far sidewalk on top, road, near sidewalk,
    // then the ledge you shuffle along.
    for (y0, y1, color) in [
        (128.0, STREET_FAR, Color::srgb(0.16, 0.16, 0.18)),   // far sidewalk
        (-56.0, 128.0, Color::srgb(0.10, 0.10, 0.12)),        // road
        (STREET_NEAR, -56.0, Color::srgb(0.16, 0.16, 0.18)),  // near sidewalk
        (-320.0, STREET_NEAR, Color::srgb(0.13, 0.09, 0.07)), // the building ledge
    ] {
        commands.spawn((
            Sprite { color, custom_size: Some(Vec2::new(760.0, y1 - y0)), ..default() },
            Transform::from_xyz(0.0, (y0 + y1) / 2.0, 0.5),
            GameTag,
        ));
    }
    // Lane dashes on the road.
    for i in 0..9 {
        commands.spawn((
            Sprite { color: Color::srgb(0.30, 0.30, 0.20), custom_size: Some(Vec2::new(34.0, 4.0)), ..default() },
            Transform::from_xyz(-340.0 + i as f32 * 85.0, 36.0, 0.8),
            GameTag,
        ));
    }
    // Curbs.
    for y in [STREET_NEAR, -56.0, 128.0] {
        commands.spawn((
            Sprite { color: Color::srgb(0.28, 0.28, 0.30), custom_size: Some(Vec2::new(760.0, 3.0)), ..default() },
            Transform::from_xyz(0.0, y, 0.9),
            GameTag,
        ));
    }
    // You, from above: shoulders and a cap, on the ledge.
    let player = commands
        .spawn((
            Sprite { color: Color::srgb(0.55, 0.35, 0.55), custom_size: Some(Vec2::new(30.0, 16.0)), ..default() },
            Transform::from_xyz(0.0, LEDGE_Y, 3.0),
            GameTag,
        ))
        .with_children(|kid| {
            kid.spawn((
                Sprite { color: RED, custom_size: Some(Vec2::splat(12.0)), ..default() },
                Transform::from_xyz(0.0, 3.0, 0.2),
            ));
        })
        .id();
    // The landing marker (shown while winding up) and the charge meter.
    let marker = commands
        .spawn((
            Sprite { color: GREEN.with_alpha(0.0), custom_size: Some(Vec2::new(22.0, 14.0)), ..default() },
            Transform::from_xyz(0.0, STREET_NEAR, 4.0),
            GameTag,
        ))
        .id();
    let meter = commands
        .spawn((
            Sprite { color: GREEN.with_alpha(0.0), custom_size: Some(Vec2::new(4.0, 4.0)), ..default() },
            Transform::from_xyz(0.0, LEDGE_Y - 18.0, 4.0),
            GameTag,
        ))
        .id();
    // The street's regulars.
    let mut targets = Vec::new();
    targets.push(spawn_target(&mut commands, Kind::Mailbox, Vec2::new(rng.between(-260.0, 260.0), -150.0), 0.0));
    targets.push(spawn_target(&mut commands, Kind::Manhole, Vec2::new(rng.between(-200.0, 200.0), 40.0), 0.0));
    targets.push(spawn_target(&mut commands, Kind::Walker, Vec2::new(-380.0, -110.0), rng.between(35.0, 55.0)));
    targets.push(spawn_target(&mut commands, Kind::Walker, Vec2::new(380.0, -185.0), -rng.between(30.0, 50.0)));
    targets.push(spawn_target(&mut commands, Kind::Car, Vec2::new(-380.0, 90.0), rng.between(120.0, 170.0)));
    targets.push(spawn_target(&mut commands, Kind::Car, Vec2::new(380.0, -10.0), -rng.between(100.0, 150.0)));
    targets.push(spawn_target(&mut commands, Kind::HatGuy, Vec2::new(-380.0, 170.0), rng.between(40.0, 60.0)));
    targets.push(spawn_target(&mut commands, Kind::HatGuy, Vec2::new(380.0, 240.0), -rng.between(35.0, 55.0)));
    targets.push(spawn_target(&mut commands, Kind::Pigeon, Vec2::new(-380.0, 205.0), rng.between(150.0, 210.0)));
    commands.insert_resource(Roof {
        gobs: GOBS_START,
        score: 0,
        chain: 0,
        charge: None,
        targets,
        flights: Vec::new(),
        splats: Vec::new(),
        over: None,
        result: String::new(),
        player,
        marker,
        meter,
    });
    let hud = text(&mut commands, "", 16.0, WHITE, Vec3::new(0.0, 308.0, 6.0));
    commands.entity(hud).insert((Hud, GameTag));
}

/// Mouse slides you along the ledge; hold winds up, release hocks.
#[allow(clippy::too_many_arguments)]
fn aim(
    time: Res<Time>,
    buttons: Res<ButtonInput<MouseButton>>,
    windows: Query<&Window>,
    cameras: Query<(&Camera, &GlobalTransform)>,
    mut commands: Commands,
    mut r: ResMut<Roof>,
    mut tfs: Query<&mut Transform>,
    mut sprites: Query<&mut Sprite>,
) {
    if r.over.is_some() {
        return;
    }
    let dt = time.delta_secs();
    let mut px = 0.0;
    if let (Ok(window), Ok((camera, cam_tf))) = (windows.single(), cameras.single()) {
        if let Some(world) = cursor_world(window, camera, cam_tf) {
            px = world.x.clamp(-350.0, 350.0);
        } else if let Ok(tf) = tfs.get(r.player) {
            px = tf.translation.x;
        }
    }
    if let Ok(mut tf) = tfs.get_mut(r.player) {
        tf.translation.x = px;
    }
    // Wind-up.
    if r.charge.is_none() && r.gobs > 0 {
        if buttons.just_pressed(MouseButton::Left) {
            r.charge = Some((0.0, false));
        } else if buttons.just_pressed(MouseButton::Right) {
            if r.gobs >= 3 {
                r.charge = Some((0.0, true));
                sfx("power");
            } else {
                sfx("buzz");
            }
        }
    }
    let mut release: Option<(f32, bool)> = None;
    if let Some((ref mut held, mega)) = r.charge {
        *held += dt;
        let frac = (*held / CHARGE_MAX).min(1.0);
        let over = *held > OVERCHARGE;
        let button = if mega { MouseButton::Right } else { MouseButton::Left };
        if !buttons.pressed(button) {
            release = Some((*held, mega));
        }
        // Marker crawls up the street; meter fills under your feet.
        if let Ok(mut tf) = tfs.get_mut(r.marker) {
            tf.translation.x = px;
            tf.translation.y = land_y(frac);
        }
        if let Ok(mut sp) = sprites.get_mut(r.marker) {
            let c = if over { RED } else if mega { CYAN } else { GREEN };
            sp.color = c.with_alpha(0.85);
            let w = if mega { 60.0 } else { 22.0 };
            sp.custom_size = Some(Vec2::new(w, w * 0.6));
        }
        if let Ok(mut sp) = sprites.get_mut(r.meter) {
            let c = if over { RED } else if frac > 0.95 { AMBER } else { GREEN };
            sp.color = c;
            sp.custom_size = Some(Vec2::new(6.0 + frac * 120.0, 5.0));
        }
    } else {
        if let Ok(mut sp) = sprites.get_mut(r.marker) {
            sp.color = sp.color.with_alpha(0.0);
        }
        if let Ok(mut sp) = sprites.get_mut(r.meter) {
            sp.color = sp.color.with_alpha(0.0);
        }
    }
    if let Ok(mut tf) = tfs.get_mut(r.meter) {
        tf.translation.x = px;
    }
    let Some((held, mega)) = release else { return };
    r.charge = None;
    let frac = (held / CHARGE_MAX).min(1.0);
    let dribble = held > OVERCHARGE;
    let cost = if mega { 3 } else { 1 };
    r.gobs -= cost;
    stat("gobs_hocked", 1);
    if mega {
        stat("mega_gobs", 1);
    }
    let to = if dribble {
        Vec2::new(px + 6.0, STREET_NEAR + 6.0)
    } else {
        Vec2::new(px, land_y(frac))
    };
    let gob = commands
        .spawn((
            Sprite { color: Color::srgb(0.55, 0.75, 0.35), custom_size: Some(Vec2::splat(if mega { 14.0 } else { 9.0 })), ..default() },
            Transform::from_xyz(px, LEDGE_Y + 8.0, 5.0),
            GameTag,
        ))
        .id();
    let shadow = commands
        .spawn((
            Sprite { color: Color::srgba(0.0, 0.0, 0.0, 0.35), custom_size: Some(Vec2::new(10.0, 6.0)), ..default() },
            Transform::from_translation(to.extend(1.2)),
            GameTag,
        ))
        .id();
    r.flights.push(Flight {
        from: Vec2::new(px, LEDGE_Y + 8.0),
        to,
        t: 0.0,
        dur: if dribble { 0.35 } else { 0.45 + frac * 0.75 },
        mega,
        weight: if mega { 3.0 } else { 1.0 + frac },
        dribble,
        ent: gob,
        shadow,
    });
    sfx(if dribble { "buzz" } else if mega { "fire" } else { "drop" });
}

fn gobs_fly(
    time: Res<Time>,
    mut commands: Commands,
    mut r: ResMut<Roof>,
    mut tfs: Query<&mut Transform>,
    mut sprites: Query<&mut Sprite>,
) {
    let dt = time.delta_secs();
    // Splat decals fade out.
    let mut gone = Vec::new();
    for (i, (ent, ttl)) in r.splats.iter_mut().enumerate() {
        *ttl -= dt;
        if *ttl <= 0.0 {
            gone.push(i);
        } else if let Ok(mut sp) = sprites.get_mut(*ent) {
            sp.color = sp.color.with_alpha((*ttl / 2.0).min(0.6));
        }
    }
    for i in gone.into_iter().rev() {
        let (ent, _) = r.splats.remove(i);
        commands.entity(ent).despawn();
    }
    if r.over.is_some() {
        return;
    }
    let mut landings: Vec<(Vec2, bool, f32, bool)> = Vec::new(); // to, mega, weight, dribble
    let mut done = Vec::new();
    for (i, f) in r.flights.iter_mut().enumerate() {
        f.t += dt;
        let k = (f.t / f.dur).min(1.0);
        let pos = f.from.lerp(f.to, k);
        let arc = 1.0 + (std::f32::consts::PI * k).sin() * 1.3;
        if let Ok(mut tf) = tfs.get_mut(f.ent) {
            tf.translation.x = pos.x;
            tf.translation.y = pos.y;
            tf.scale = Vec3::splat(arc);
        }
        if let Ok(mut sp) = sprites.get_mut(f.shadow) {
            sp.color = Color::srgba(0.0, 0.0, 0.0, 0.15 + 0.3 * k);
        }
        if k >= 1.0 {
            landings.push((f.to, f.mega, f.weight, f.dribble));
            done.push(i);
        }
    }
    for i in done.into_iter().rev() {
        let f = r.flights.remove(i);
        commands.entity(f.ent).despawn();
        commands.entity(f.shadow).despawn();
    }
    for (at, mega, weight, dribble) in landings {
        land(&mut commands, &mut r, at, mega, weight, dribble);
    }
    // Out of gobs and nothing in the air: call it a night.
    if r.gobs <= 0 && r.flights.is_empty() && r.charge.is_none() && r.over.is_none() {
        r.result = format!("STREET'S CLEAN. {} POINTS.", r.score);
        r.over = Some(Timer::from_seconds(2.4, TimerMode::Once));
        sfx("over");
    }
}

fn land(commands: &mut Commands, r: &mut Roof, at: Vec2, mega: bool, weight: f32, dribble: bool) {
    // The splat itself.
    let splat = commands
        .spawn((
            Sprite {
                color: Color::srgba(0.55, 0.75, 0.35, 0.6),
                custom_size: Some(Vec2::splat(if mega { 46.0 } else { 18.0 })),
                ..default()
            },
            Transform::from_translation(at.extend(1.4)).with_rotation(Quat::from_rotation_z(0.6)),
            GameTag,
        ))
        .id();
    r.splats.push((splat, 2.0));
    if dribble {
        popup(commands, "DRIBBLE...", 14.0, DIM, at + Vec2::new(0.0, 18.0));
        r.chain = 0;
        sfx("buzz");
        return;
    }
    let reach = if mega { 70.0 } else { 26.0 };
    // Manhole swish first: dead reckoning pays the plumber.
    let mut swished = false;
    for t in r.targets.iter() {
        if t.kind == Kind::Manhole && t.pos.distance(at) < 24.0 {
            r.score += 500;
            r.gobs += 3;
            stat("manholes", 1);
            popup(commands, "MANHOLE! +500 & 3 GOBS", 16.0, CYAN, t.pos + Vec2::new(0.0, 24.0));
            sfx("clear");
            swished = true;
            break;
        }
    }
    let mut hits: u32 = 0;
    let mut best_points = 0u32;
    let mut best_at = at;
    for t in r.targets.iter() {
        if t.wait > 0.0 || t.kind == Kind::Manhole {
            continue;
        }
        let d = t.pos.distance(at);
        if d > reach + t.radius {
            continue;
        }
        hits += 1;
        let bullseye = d < 11.0;
        let mut pts = t.base * lane_mult(t.pos.y);
        pts = (pts as f32 * weight) as u32;
        if bullseye {
            pts *= 2;
            stat("bullseyes", 1);
        }
        if pts > best_points {
            best_points = pts;
            best_at = t.pos;
        }
        r.score += pts;
    }
    if hits > 0 || swished {
        stat("splats", hits.max(1) as u64);
        r.chain = (r.chain + 1).min(5);
        if r.chain == 5 {
            stat("chains_maxed", 1);
        }
        let mult = r.chain.max(1);
        if mult > 1 {
            r.score += best_points * (mult - 1) / 2; // chain gravy on the best hit
        }
        if best_points > 0 {
            let label = format!("+{best_points}{}", if r.chain > 1 { " CHAIN!" } else { "" });
            popup(commands, &label, 16.0, GREEN, best_at + Vec2::new(0.0, 20.0));
        }
        sfx("capture");
    } else if !swished {
        r.chain = 0;
        sfx("tick");
    }
}

fn targets_move(time: Res<Time>, mut rng: ResMut<Rng>, mut r: ResMut<Roof>, mut tfs: Query<&mut Transform>) {
    if r.over.is_some() {
        return;
    }
    let dt = time.delta_secs();
    for t in r.targets.iter_mut() {
        if t.vel_x == 0.0 {
            continue;
        }
        if t.wait > 0.0 {
            t.wait -= dt;
            if t.wait <= 0.0 {
                // Re-enter from the side you left, going the other way? No:
                // same heading, fresh crossing — plus a little lane wobble.
                t.pos.x = if t.vel_x > 0.0 { -390.0 } else { 390.0 };
                t.pos.y += rng.between(-14.0, 14.0);
                t.pos.y = t.pos.y.clamp(STREET_NEAR + 18.0, STREET_FAR - 12.0);
            }
            continue;
        }
        t.pos.x += t.vel_x * dt;
        if t.pos.x.abs() > 385.0 {
            t.wait = rng.between(0.5, 2.2);
        }
        if let Ok(mut tf) = tfs.get_mut(t.ent) {
            tf.translation.x = t.pos.x;
            tf.translation.y = t.pos.y;
        }
    }
}

fn hud(r: Res<Roof>, mut hud: Query<&mut Text2d, With<Hud>>) {
    if let Ok(mut t) = hud.single_mut() {
        let s = if r.over.is_some() {
            r.result.clone()
        } else {
            let charge_note = match r.charge {
                Some((h, true)) => format!("   MEGA {}%", ((h / CHARGE_MAX).min(1.0) * 100.0) as u32),
                Some((h, false)) => format!("   WIND-UP {}%", ((h / CHARGE_MAX).min(1.0) * 100.0) as u32),
                None => String::new(),
            };
            format!(
                "GOBS {}   SCORE {}   CHAIN x{}{}",
                r.gobs.max(0),
                r.score,
                r.chain.max(1),
                charge_note
            )
        };
        if t.0 != s {
            t.0 = s;
        }
    }
}

fn endgame(
    time: Res<Time>,
    mut r: ResMut<Roof>,
    mut final_score: ResMut<FinalScore>,
    mut next: ResMut<NextState<Phase>>,
) {
    if let Some(timer) = r.over.as_mut() {
        if timer.tick(time.delta()).finished() {
            final_score.0 = r.score;
            next.set(Phase::GameOver);
        }
    }
}
