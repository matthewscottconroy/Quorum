//! OFF THE ROOF — a gross-out distance-spitting game, built from scratch:
//! original name, characters, street, and scoring. You are a slacker on a
//! rooftop ledge; the street below is full of targets that never asked for
//! this. Genre mechanics only: hold to power up, release to let fly.
//!
//! THE SCORING (designed to make the leaderboard a skill contest):
//!   base value × distance band (NEAR ×1 / MID ×2 / FAR ×3)
//!   × the CHAIN (consecutive hits: ×1, ×2 … ×5; any miss resets it)
//!   and a dead-center BULLSEYE doubles that gob's take.
//! The strategic loop: build the chain on fat, near targets, then spend it
//! where the multipliers are — far, small, moving. The manhole pays 500
//! flat AND refills three gobs; the MEGA gob (right-click, costs three)
//! splashes wide and feeds every splashed target through the chain.
//! Overcharge the meter and the gob dribbles down the ledge: a miss.

use bevy::prelude::*;

use crate::retro::{popup, text, AMBER, CYAN, DIM, GREEN, MAGENTA, RED, WHITE};
use crate::rng::Rng;
use crate::shell::{sfx, stat};
use crate::{FinalScore, GameTag, Phase};

pub const BLURB: &[&str] = &[
    "FIFTEEN GOBS. ONE STREET. NO WITNESSES.",
    "AIM WITH THE MOUSE, HOLD TO CHARGE, RELEASE TO FIRE.",
    "RIGHT-CLICK: THE MEGA GOB (COSTS THREE). CHAIN HITS FOR X5.",
];

const LEDGE: Vec2 = Vec2::new(-310.0, 150.0); // launch point
const STREET_Y: f32 = -210.0;
const CHARGE_TIME: f32 = 1.15;
const OVERCHARGE: f32 = 1.45; // past this the gob dribbles
const GRAVITY: f32 = -420.0;

#[derive(Clone, Copy, PartialEq)]
enum TKind {
    Mailbox, // near, fat, 100
    Car,     // mid, fat, 200
    Walker,  // near lane, moving, 150
    HatGuy,  // far lane, fast, 300
    Pigeon,  // mid/far, intermittent, 400
    Manhole, // far, tiny, 500 flat + 3 gobs back
}

struct Target {
    kind: TKind,
    x: f32,
    w: f32,
    speed: f32, // movers pace between bounds
    min_x: f32,
    max_x: f32,
    perched: f32, // pigeons: time left before flying off
    ent: Entity,
    splat_t: f32,
}

#[derive(Resource)]
struct Roof {
    targets: Vec<Target>,
    gobs: i32,
    score: u32,
    chain: u32,
    chains_maxed_flag: bool,
    charging: bool,
    charge: f32,
    mega_armed: bool,
    wind: f32, // px/s^2 sideways, announced per gob
    in_flight: Option<(Vec2, Vec2, bool)>, // pos, vel, mega
    over: Option<Timer>,
}

#[derive(Component)]
struct Gob;

#[derive(Component)]
struct Hud;

#[derive(Component)]
struct MeterFill;

#[derive(Component)]
struct SplatBit {
    vel: Vec2,
    ttl: f32,
}

pub struct RoofPlugin;

impl Plugin for RoofPlugin {
    fn build(&self, app: &mut App) {
        app.add_systems(OnEnter(Phase::Playing), setup).add_systems(
            Update,
            (aim_and_fire, flight, movers, splat_bits, hud, endgame)
                .chain()
                .run_if(in_state(Phase::Playing))
                .run_if(crate::unpaused),
        );
    }
}

fn band_mult(x: f32) -> u32 {
    if x < -60.0 {
        1
    } else if x < 150.0 {
        2
    } else {
        3
    }
}

fn base_value(kind: TKind) -> u32 {
    match kind {
        TKind::Mailbox => 100,
        TKind::Walker => 150,
        TKind::Car => 200,
        TKind::HatGuy => 300,
        TKind::Pigeon => 400,
        TKind::Manhole => 500,
    }
}

fn target_color(kind: TKind) -> Color {
    match kind {
        TKind::Mailbox => CYAN,
        TKind::Car => Color::srgb(0.65, 0.3, 0.3),
        TKind::Walker => AMBER,
        TKind::HatGuy => MAGENTA,
        TKind::Pigeon => WHITE,
        TKind::Manhole => Color::srgb(0.25, 0.28, 0.32),
    }
}

fn spawn_target(commands: &mut Commands, kind: TKind, x: f32, min_x: f32, max_x: f32, speed: f32) -> Target {
    let (w, h) = match kind {
        TKind::Mailbox => (44.0, 52.0),
        TKind::Car => (110.0, 34.0),
        TKind::Walker => (26.0, 58.0),
        TKind::HatGuy => (26.0, 62.0),
        TKind::Pigeon => (30.0, 18.0),
        TKind::Manhole => (34.0, 8.0),
    };
    let ent = commands
        .spawn((
            Sprite { color: target_color(kind), custom_size: Some(Vec2::new(w, h)), ..default() },
            Transform::from_xyz(x, STREET_Y + h / 2.0, 2.0),
            GameTag,
        ))
        .id();
    Target { kind, x, w, speed, min_x, max_x, perched: 4.0, ent, splat_t: 0.0 }
}

fn setup(mut commands: Commands) {
    let mut targets = Vec::new();
    targets.push(spawn_target(&mut commands, TKind::Mailbox, -150.0, 0.0, 0.0, 0.0));
    targets.push(spawn_target(&mut commands, TKind::Car, 40.0, 0.0, 0.0, 0.0));
    targets.push(spawn_target(&mut commands, TKind::Manhole, 240.0, 0.0, 0.0, 0.0));
    targets.push(spawn_target(&mut commands, TKind::Walker, -120.0, -230.0, -70.0, 42.0));
    targets.push(spawn_target(&mut commands, TKind::HatGuy, 220.0, 160.0, 330.0, 95.0));
    targets.push(spawn_target(&mut commands, TKind::Pigeon, 120.0, 60.0, 320.0, 0.0));
    commands.insert_resource(Roof {
        targets,
        gobs: 15,
        score: 0,
        chain: 1,
        chains_maxed_flag: false,
        charging: false,
        charge: 0.0,
        mega_armed: false,
        wind: 0.0,
        in_flight: None,
        over: None,
    });
    // The rooftop, the ledge, the slacker (a green blob with a cowlick).
    commands.spawn((
        Sprite { color: Color::srgb(0.14, 0.15, 0.20), custom_size: Some(Vec2::new(140.0, 380.0)), ..default() },
        Transform::from_xyz(-360.0 + 55.0, 150.0 - 190.0 + 190.0 - 190.0 + 95.0, 1.0),
        GameTag,
    ));
    commands.spawn((
        Sprite { color: GREEN, custom_size: Some(Vec2::new(26.0, 44.0)), ..default() },
        Transform::from_xyz(LEDGE.x + 14.0, LEDGE.y + 8.0, 2.0),
        GameTag,
    ));
    // The street: distance bands painted on the ground.
    for (x0, x1, c) in [
        (-260.0f32, -60.0f32, Color::srgb(0.10, 0.11, 0.14)),
        (-60.0, 150.0, Color::srgb(0.12, 0.13, 0.17)),
        (150.0, 360.0, Color::srgb(0.14, 0.15, 0.20)),
    ] {
        commands.spawn((
            Sprite { color: c, custom_size: Some(Vec2::new(x1 - x0, 14.0)), ..default() },
            Transform::from_xyz((x0 + x1) / 2.0, STREET_Y - 7.0, 1.0),
            GameTag,
        ));
    }
    for (x, label) in [(-160.0, "X1"), (45.0, "X2"), (255.0, "X3")] {
        let t = text(&mut commands, label, 14.0, DIM, Vec3::new(x, STREET_Y - 26.0, 1.5));
        commands.entity(t).insert(GameTag);
    }
    // Power meter shell + fill.
    commands.spawn((
        Sprite { color: DIM, custom_size: Some(Vec2::new(18.0, 204.0)), ..default() },
        Transform::from_xyz(-346.0, 40.0, 3.0),
        GameTag,
    ));
    commands.spawn((
        Sprite { color: GREEN, custom_size: Some(Vec2::new(14.0, 0.0)), ..default() },
        Transform::from_xyz(-346.0, -60.0, 3.5),
        MeterFill,
        GameTag,
    ));
    let hud = text(&mut commands, "", 18.0, WHITE, Vec3::new(0.0, 300.0, 5.0));
    commands.entity(hud).insert((Hud, GameTag));
}

/// Charging, releasing, and arming the mega. Angle comes from the cursor.
#[allow(clippy::too_many_arguments)]
fn aim_and_fire(
    time: Res<Time>,
    buttons: Res<ButtonInput<MouseButton>>,
    windows: Query<&Window>,
    cameras: Query<(&Camera, &GlobalTransform)>,
    mut commands: Commands,
    mut rng: ResMut<Rng>,
    mut roof: ResMut<Roof>,
    mut meter: Query<(&mut Sprite, &mut Transform), With<MeterFill>>,
    mut gizmos: Gizmos,
) {
    if roof.over.is_some() || roof.in_flight.is_some() {
        return;
    }
    // Cursor sets the launch angle (higher cursor = loftier arc).
    let mut angle = 0.75f32;
    if let (Ok(window), Ok((camera, cam_tf))) = (windows.single(), cameras.single()) {
        if let Some(world) = crate::retro::cursor_world(window, camera, cam_tf) {
            angle = ((world.y + 320.0) / 640.0).clamp(0.1, 0.95) * 1.15 + 0.25;
        }
    }
    // A faint aim tick on the ledge.
    let dir = Vec2::new(angle.cos(), angle.sin());
    gizmos.line_2d(LEDGE, LEDGE + dir * 40.0, DIM);

    if buttons.just_pressed(MouseButton::Right) {
        if roof.mega_armed {
            roof.mega_armed = false;
            sfx("tick");
        } else if roof.gobs >= 3 {
            roof.mega_armed = true;
            sfx("power");
        } else {
            sfx("buzz"); // not enough left in the tank
        }
    }
    if buttons.just_pressed(MouseButton::Left) && roof.gobs > 0 {
        roof.charging = true;
        roof.charge = 0.0;
        // Wind is rolled per gob and shown on the HUD while you charge.
        roof.wind = (rng.range(120) as f32 - 60.0) * 1.4;
    }
    if roof.charging {
        roof.charge += time.delta_secs();
        let frac = (roof.charge / CHARGE_TIME).min(1.0);
        if let Ok((mut sp, mut tf)) = meter.single_mut() {
            sp.custom_size = Some(Vec2::new(14.0, 200.0 * frac));
            tf.translation.y = -60.0 + 100.0 * frac;
            sp.color = if roof.charge > CHARGE_TIME {
                RED // over the top: about to fizzle
            } else if frac > 0.8 {
                AMBER
            } else {
                GREEN
            };
        }
        if buttons.just_released(MouseButton::Left) {
            roof.charging = false;
            let mega = roof.mega_armed;
            let cost = if mega { 3 } else { 1 };
            roof.gobs -= cost;
            roof.mega_armed = false;
            stat("gobs_hocked", 1);
            if mega {
                stat("mega_gobs", 1);
            }
            if let Ok((mut sp, _)) = meter.single_mut() {
                sp.custom_size = Some(Vec2::new(14.0, 0.0));
            }
            if roof.charge > OVERCHARGE {
                // The dribble: all wind-up, no distance. Chain broken.
                roof.chain = 1;
                popup(&mut commands, "DRIBBLE...", 20.0, RED, LEDGE + Vec2::new(40.0, -20.0));
                sfx("buzz");
                return;
            }
            let power = (roof.charge / CHARGE_TIME).min(1.0) * 620.0 + 120.0;
            let heavy = if mega { 0.82 } else { 1.0 }; // the big one flies shorter
            roof.in_flight = Some((LEDGE, dir * power * heavy, mega));
            sfx("fire");
        }
    }
}

fn flight(
    time: Res<Time>,
    mut commands: Commands,
    mut rng: ResMut<Rng>,
    mut roof: ResMut<Roof>,
    gob_vis: Query<Entity, With<Gob>>,
) {
    let Some((mut pos, mut vel, mega)) = roof.in_flight else { return };
    let dt = time.delta_secs();
    vel.y += GRAVITY * dt;
    vel.x += roof.wind * dt;
    pos += vel * dt;
    for e in &gob_vis {
        commands.entity(e).despawn();
    }
    if pos.y > STREET_Y + 4.0 && pos.x < 370.0 {
        let size = if mega { 16.0 } else { 9.0 };
        commands.spawn((
            Sprite { color: GREEN, custom_size: Some(Vec2::splat(size)), ..default() },
            Transform::from_xyz(pos.x, pos.y, 4.0).with_rotation(Quat::from_rotation_z(0.785)),
            Gob,
            GameTag,
        ));
        roof.in_flight = Some((pos, vel, mega));
        return;
    }
    // Touchdown: score the splat.
    roof.in_flight = None;
    let splash = if mega { 70.0 } else { 0.0 };
    let mut hits: Vec<(u32, f32, bool, TKind)> = Vec::new();
    for t in roof.targets.iter_mut() {
        let half = t.w / 2.0 + splash;
        let dx = (pos.x - t.x).abs();
        let live = t.kind != TKind::Pigeon || t.perched > 0.0;
        if dx <= half && live {
            let bullseye = dx <= t.w * 0.2;
            hits.push((base_value(t.kind), t.x, bullseye, t.kind));
            t.splat_t = 0.8;
        }
    }
    // Splat spray, always: green pixels off the pavement.
    for k in 0..18u32 {
        let a = 0.3 + (k as f32 / 18.0) * 2.5;
        commands.spawn((
            Sprite { color: GREEN, custom_size: Some(Vec2::splat(2.5)), ..default() },
            Transform::from_xyz(pos.x, STREET_Y + 3.0, 4.5),
            SplatBit { vel: Vec2::new(a.cos(), a.sin().abs()) * (50.0 + ((k * 41) % 80) as f32), ttl: 0.7 },
            GameTag,
        ));
    }
    if hits.is_empty() {
        roof.chain = 1;
        popup(&mut commands, "SPLAT. NOBODY.", 16.0, DIM, Vec2::new(pos.x, STREET_Y + 40.0));
        sfx("drop");
    } else {
        for (base, tx, bullseye, kind) in &hits {
            let mult = band_mult(*tx) * roof.chain * if *bullseye { 2 } else { 1 };
            let pay = base * mult;
            roof.score += pay;
            stat("splats", 1);
            if *bullseye {
                stat("bullseyes", 1);
            }
            if *kind == TKind::Manhole {
                roof.gobs += 3;
                stat("manholes", 1);
                popup(&mut commands, "DOWN THE MANHOLE! +3 GOBS", 18.0, CYAN, Vec2::new(*tx, STREET_Y + 70.0));
            }
            popup(
                &mut commands,
                &format!("+{pay}{}", if *bullseye { " BULLSEYE!" } else { "" }),
                18.0,
                AMBER,
                Vec2::new(*tx, STREET_Y + 46.0),
            );
            // Each splashed target feeds the chain.
            roof.chain = (roof.chain + 1).min(5);
        }
        if roof.chain == 5 && !roof.chains_maxed_flag {
            roof.chains_maxed_flag = true;
            stat("chains_maxed", 1);
            popup(&mut commands, "CHAIN X5!", 22.0, MAGENTA, Vec2::new(0.0, 60.0));
        }
        if roof.chain < 5 {
            roof.chains_maxed_flag = false;
        }
        sfx("capture");
    }
    // Pigeons startle whatever happens nearby.
    for t in roof.targets.iter_mut() {
        if t.kind == TKind::Pigeon && (pos.x - t.x).abs() < 120.0 {
            t.perched = -3.0 - rng.range(40) as f32 / 10.0; // flies off a while
        }
    }
    if roof.gobs <= 0 {
        roof.over = Some(Timer::from_seconds(2.2, TimerMode::Once));
        popup(&mut commands, "ALL OUT. WIPE YOUR CHIN.", 26.0, AMBER, Vec2::new(0.0, 0.0));
        sfx("over");
    }
}

fn movers(time: Res<Time>, mut roof: ResMut<Roof>, mut tfs: Query<&mut Transform>, mut sprites: Query<&mut Sprite>) {
    let dt = time.delta_secs();
    for t in roof.targets.iter_mut() {
        if t.speed > 0.0 {
            t.x += t.speed * dt;
            if t.x < t.min_x || t.x > t.max_x {
                t.speed = -t.speed;
                t.x = t.x.clamp(t.min_x, t.max_x);
            }
        }
        if t.kind == TKind::Pigeon {
            t.perched += dt;
            // While away (negative), the pigeon is gone; on return, reperch
            // somewhere new along its stretch.
            if t.perched > 6.0 {
                t.perched = 4.0;
            }
            if t.perched >= 0.0 && t.perched < dt * 1.5 {
                let span = t.max_x - t.min_x;
                t.x = t.min_x + (t.x * 7.3).abs() % span;
            }
        }
        t.splat_t = (t.splat_t - dt).max(0.0);
        if let Ok(mut tf) = tfs.get_mut(t.ent) {
            tf.translation.x = t.x;
        }
        if let Ok(mut sp) = sprites.get_mut(t.ent) {
            let hidden = t.kind == TKind::Pigeon && t.perched < 0.0;
            sp.color = if hidden {
                target_color(t.kind).with_alpha(0.0)
            } else if t.splat_t > 0.0 {
                GREEN
            } else {
                target_color(t.kind)
            };
        }
    }
}

fn splat_bits(time: Res<Time>, mut commands: Commands, mut bits: Query<(Entity, &mut SplatBit, &mut Transform)>) {
    let dt = time.delta_secs();
    for (e, mut b, mut tf) in &mut bits {
        b.ttl -= dt;
        if b.ttl <= 0.0 {
            commands.entity(e).despawn();
            continue;
        }
        b.vel.y -= 300.0 * dt;
        tf.translation.x += b.vel.x * dt;
        tf.translation.y += b.vel.y * dt;
    }
}

fn hud(roof: Res<Roof>, mut hud: Query<&mut Text2d, With<Hud>>) {
    if let Ok(mut t) = hud.single_mut() {
        let wind = if roof.charging || roof.in_flight.is_some() {
            let dir = if roof.wind > 0.0 { ">>" } else { "<<" };
            format!("   WIND {} {:.0}", dir, roof.wind.abs() / 10.0)
        } else {
            String::new()
        };
        let mega = if roof.mega_armed { "   [MEGA ARMED]" } else { "" };
        let s = format!(
            "SCORE {}   GOBS {}   CHAIN X{}{}{}",
            roof.score,
            roof.gobs.max(0),
            roof.chain,
            wind,
            mega
        );
        if t.0 != s {
            t.0 = s;
        }
    }
}

fn endgame(
    time: Res<Time>,
    mut roof: ResMut<Roof>,
    mut final_score: ResMut<FinalScore>,
    mut next: ResMut<NextState<Phase>>,
) {
    if let Some(t) = roof.over.as_mut() {
        if t.tick(time.delta()).finished() {
            final_score.0 = roof.score;
            next.set(Phase::GameOver);
        }
    }
}
