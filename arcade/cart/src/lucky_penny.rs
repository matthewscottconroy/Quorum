//! LUCKY PENNY — mascot pinball in the stacked-boards style of the Game
//! Boy classics: three tables piled on top of each other, a grinning coin
//! for a ball, and hatches that bounce you up toward the boardroom. All
//! original: name, mascot, art, boards. Genre mechanics only.
//!
//! The tour: BASEMENT → LOBBY → BOARDROOM. Knock the gate targets to open
//! each ceiling hatch, ride the hatch up, and feed the boardroom's VAULT
//! five hits for the jackpot. Falling through a floor hole just drops you
//! a board — only the basement drain costs a penny. Three pennies.
//!
//! Physics: one ball vs segments (walls, flippers) and discs (bumpers),
//! integrated in fixed substeps. Flippers are capsules swung by key press;
//! their tip speed at the contact point becomes launch impulse — the whole
//! feel of pinball lives in that one line.

use bevy::prelude::*;

use crate::retro::{popup, text, AMBER, CYAN, DIM, GREEN, MAGENTA, RED, WHITE};
use crate::rng::Rng;
use crate::shell::{sfx, stat};
use crate::{FinalScore, GameTag, Phase};

pub const BLURB: &[&str] = &[
    "A COIN WITH A FACE AND EVERYTHING TO PROVE.",
    "LEFT/RIGHT (OR Z AND /) FLIP. SPACE LAUNCHES.",
    "CLIMB: BASEMENT, LOBBY, BOARDROOM. CRACK THE VAULT.",
];

const BALL_R: f32 = 11.0;
const GRAV: f32 = -980.0;
const SUBSTEPS: u32 = 5;
const FLIP_Y: f32 = -232.0;
const FLIP_PIVOT_X: f32 = 108.0;
const FLIP_LEN: f32 = 86.0;
const FLIP_R: f32 = 8.0;
const REST_ANG: f32 = -0.48; // radians below horizontal at rest
const UP_ANG: f32 = 0.55;
const FLIP_SPEED: f32 = 16.0; // rad/s while swinging
const DRAIN_Y: f32 = -318.0;
const HATCH_Y: f32 = 268.0;

/// One wall segment (a, b) with restitution.
struct Seg {
    a: Vec2,
    b: Vec2,
    bounce: f32,
}

/// A round bumper: kicks the ball and pays.
struct Bumper {
    at: Vec2,
    r: f32,
    pay: u32,
    kick: f32,
    ent: Entity,
    flash: f32,
}

/// A knock-down gate target; all down = the ceiling hatch opens.
struct GateTarget {
    at: Vec2,
    down: bool,
    ent: Entity,
}

#[derive(Resource)]
struct Table {
    board: usize, // 0 basement, 1 lobby, 2 boardroom
    segs: Vec<Seg>,
    bumpers: Vec<Bumper>,
    gates: Vec<GateTarget>,
    hatch_open: bool,
    vault_hits: u32, // boardroom only
    score: u32,
    pennies: i32,
    launched: bool,
    vel: Vec2,
    left_ang: f32,
    right_ang: f32,
    over: Option<Timer>,
    board_ents: Vec<Entity>, // everything visual belonging to this board
    combo_t: f32,
}

#[derive(Component)]
struct Penny;

#[derive(Component)]
struct FlipperVis {
    left: bool,
}

#[derive(Component)]
struct Hud;

pub struct LuckyPennyPlugin;

impl Plugin for LuckyPennyPlugin {
    fn build(&self, app: &mut App) {
        app.add_systems(OnEnter(Phase::Playing), setup).add_systems(
            Update,
            (flip_input, physics, visuals, hud, endgame)
                .chain()
                .run_if(in_state(Phase::Playing))
                .run_if(crate::unpaused),
        );
    }
}

const BOARD_NAMES: [&str; 3] = ["THE BASEMENT", "THE LOBBY", "THE BOARDROOM"];

/// Builds one board's furniture: walls, funnels, bumpers, gate targets.
/// Everything spawned lands in `table.board_ents` for teardown on travel.
fn build_board(commands: &mut Commands, table: &mut Table, board: usize) {
    table.segs.clear();
    table.bumpers.clear();
    table.gates.clear();
    table.hatch_open = false;
    table.board = board;
    for e in table.board_ents.drain(..) {
        commands.entity(e).despawn();
    }

    let wall = |table: &mut Table, a: Vec2, b: Vec2| {
        table.segs.push(Seg { a, b, bounce: 0.55 });
    };
    // The well: side walls with funnel shoulders into the flippers.
    wall(table, Vec2::new(-300.0, 300.0), Vec2::new(-300.0, -140.0));
    wall(table, Vec2::new(300.0, 300.0), Vec2::new(300.0, -140.0));
    wall(table, Vec2::new(-300.0, -140.0), Vec2::new(-FLIP_PIVOT_X - 10.0, FLIP_Y + 16.0));
    wall(table, Vec2::new(300.0, -140.0), Vec2::new(FLIP_PIVOT_X + 10.0, FLIP_Y + 16.0));
    // Ceiling, with or without the hatch mouth (drawn open when earned).
    wall(table, Vec2::new(-300.0, 300.0), Vec2::new(-60.0, 300.0));
    wall(table, Vec2::new(60.0, 300.0), Vec2::new(300.0, 300.0));

    let spawn_visual = |commands: &mut Commands, table: &mut Table, sprite: Sprite, tf: Transform| -> Entity {
        let e = commands.spawn((sprite, tf, GameTag)).id();
        table.board_ents.push(e);
        e
    };

    // Wall paint (visual only; collision is the segment list).
    for s in &[
        (Vec2::new(-302.0, 80.0), Vec2::new(8.0, 444.0)),
        (Vec2::new(302.0, 80.0), Vec2::new(8.0, 444.0)),
    ] {
        spawn_visual(
            commands,
            table,
            Sprite { color: DIM, custom_size: Some(s.1), ..default() },
            Transform::from_xyz(s.0.x, s.0.y, 1.0),
        );
    }
    for (x0, y0, x1, y1) in [
        (-300.0f32, -140.0f32, -FLIP_PIVOT_X - 10.0, FLIP_Y + 16.0),
        (300.0, -140.0, FLIP_PIVOT_X + 10.0, FLIP_Y + 16.0),
    ] {
        let mid = Vec2::new((x0 + x1) / 2.0, (y0 + y1) / 2.0);
        let d = Vec2::new(x1 - x0, y1 - y0);
        spawn_visual(
            commands,
            table,
            Sprite { color: DIM, custom_size: Some(Vec2::new(d.length(), 8.0)), ..default() },
            Transform::from_xyz(mid.x, mid.y, 1.0)
                .with_rotation(Quat::from_rotation_z(d.y.atan2(d.x))),
        );
    }

    // Per-board furniture.
    let bumper_specs: Vec<(Vec2, f32, u32, Color)> = match board {
        0 => vec![
            (Vec2::new(-120.0, 120.0), 34.0, 50, Color::srgb(0.45, 0.30, 0.13)),
            (Vec2::new(120.0, 120.0), 34.0, 50, Color::srgb(0.45, 0.30, 0.13)),
            (Vec2::new(0.0, 10.0), 40.0, 100, MAGENTA),
        ],
        1 => vec![
            (Vec2::new(-150.0, 150.0), 30.0, 75, CYAN),
            (Vec2::new(150.0, 150.0), 30.0, 75, CYAN),
            (Vec2::new(-70.0, 20.0), 30.0, 75, CYAN),
            (Vec2::new(70.0, 20.0), 30.0, 75, CYAN),
        ],
        _ => vec![
            (Vec2::new(-160.0, 100.0), 28.0, 100, GREEN),
            (Vec2::new(160.0, 100.0), 28.0, 100, GREEN),
        ],
    };
    for (at, r, pay, color) in bumper_specs {
        let ent = spawn_visual(
            commands,
            table,
            Sprite { color, custom_size: Some(Vec2::splat(r * 2.0)), ..default() },
            Transform::from_xyz(at.x, at.y, 2.0).with_rotation(Quat::from_rotation_z(0.785)),
        );
        table.bumpers.push(Bumper { at, r, pay, kick: 420.0, ent, flash: 0.0 });
    }
    // Gate targets guard the hatch (the boardroom's are the VAULT itself).
    let gate_specs: Vec<Vec2> = match board {
        0 => vec![Vec2::new(-210.0, 220.0), Vec2::new(0.0, 250.0), Vec2::new(210.0, 220.0)],
        1 => vec![
            Vec2::new(-230.0, 210.0),
            Vec2::new(-80.0, 250.0),
            Vec2::new(80.0, 250.0),
            Vec2::new(230.0, 210.0),
        ],
        _ => vec![Vec2::new(0.0, 240.0)], // the VAULT: one target, five hits
    };
    for at in gate_specs {
        let ent = spawn_visual(
            commands,
            table,
            Sprite { color: AMBER, custom_size: Some(Vec2::new(34.0, 14.0)), ..default() },
            Transform::from_xyz(at.x, at.y, 2.0),
        );
        table.gates.push(GateTarget { at, down: false, ent });
    }
    // Board label.
    let label = text(
        commands,
        BOARD_NAMES[board],
        16.0,
        DIM,
        Vec3::new(0.0, -158.0, 1.5),
    );
    commands.entity(label).insert(GameTag);
    table.board_ents.push(label);
}

fn setup(
    mut commands: Commands,
    mut rng: ResMut<Rng>,
    mut meshes: ResMut<Assets<Mesh>>,
    mut mats: ResMut<Assets<ColorMaterial>>,
) {
    let mut table = Table {
        board: 0,
        segs: Vec::new(),
        bumpers: Vec::new(),
        gates: Vec::new(),
        hatch_open: false,
        vault_hits: 0,
        score: 0,
        pennies: 3,
        launched: false,
        vel: Vec2::ZERO,
        left_ang: REST_ANG,
        right_ang: REST_ANG,
        over: None,
        board_ents: Vec::new(),
        combo_t: 0.0,
    };
    build_board(&mut commands, &mut table, 0);
    commands.insert_resource(table);
    let _ = &mut rng;

    // The penny herself: an actual round coin with a face. Children give
    // her the rim and the grin.
    commands
        .spawn((
            Mesh2d(meshes.add(Circle::new(BALL_R))),
            MeshMaterial2d(mats.add(ColorMaterial::from(AMBER))),
            Transform::from_xyz(0.0, -60.0, 5.0),
            Penny,
            GameTag,
        ))
        .with_children(|kid| {
            for ex in [-4.0, 4.0] {
                kid.spawn((
                    Sprite { color: Color::srgb(0.1, 0.08, 0.02), custom_size: Some(Vec2::splat(3.0)), ..default() },
                    Transform::from_xyz(ex, 2.5, 0.1),
                ));
            }
            kid.spawn((
                Sprite { color: Color::srgb(0.1, 0.08, 0.02), custom_size: Some(Vec2::new(8.0, 2.2)), ..default() },
                Transform::from_xyz(0.0, -3.5, 0.1),
            ));
        });
    // Flipper visuals (the physics lives in Table's angles).
    for left in [true, false] {
        commands.spawn((
            Sprite { color: GREEN, custom_size: Some(Vec2::new(FLIP_LEN, FLIP_R * 2.0)), ..default() },
            Transform::from_xyz(0.0, FLIP_Y, 4.0),
            FlipperVis { left },
            GameTag,
        ));
    }
    let hud = text(&mut commands, "", 20.0, WHITE, Vec3::new(0.0, 312.0, 6.0));
    commands.entity(hud).insert((Hud, GameTag));
}

fn flip_input(time: Res<Time>, keys: Res<ButtonInput<KeyCode>>, mut table: ResMut<Table>) {
    let dt = time.delta_secs();
    let left_up = keys.pressed(KeyCode::ArrowLeft) || keys.pressed(KeyCode::KeyZ);
    let right_up = keys.pressed(KeyCode::ArrowRight) || keys.pressed(KeyCode::Slash);
    let step = FLIP_SPEED * dt;
    table.left_ang = if left_up {
        (table.left_ang + step).min(UP_ANG)
    } else {
        (table.left_ang - step).max(REST_ANG)
    };
    table.right_ang = if right_up {
        (table.right_ang + step).min(UP_ANG)
    } else {
        (table.right_ang - step).max(REST_ANG)
    };
    if (left_up && table.left_ang < UP_ANG) || (right_up && table.right_ang < UP_ANG) {
        // flip sfx only on the press edge, below
    }
    if keys.just_pressed(KeyCode::ArrowLeft)
        || keys.just_pressed(KeyCode::KeyZ)
        || keys.just_pressed(KeyCode::ArrowRight)
        || keys.just_pressed(KeyCode::Slash)
    {
        stat("flips", 1);
        sfx("tick");
    }
}

/// Flipper endpoints for a side at an angle (left mirrors right).
fn flipper_seg(left: bool, ang: f32) -> (Vec2, Vec2) {
    let pivot = Vec2::new(if left { -FLIP_PIVOT_X } else { FLIP_PIVOT_X }, FLIP_Y);
    let dir = if left {
        Vec2::new(ang.cos(), ang.sin())
    } else {
        Vec2::new(-ang.cos(), ang.sin())
    };
    (pivot, pivot + dir * FLIP_LEN)
}

/// Closest point on segment ab to p.
fn closest_on_seg(a: Vec2, b: Vec2, p: Vec2) -> Vec2 {
    let ab = b - a;
    let t = ((p - a).dot(ab) / ab.length_squared()).clamp(0.0, 1.0);
    a + ab * t
}

#[allow(clippy::too_many_arguments)]
fn physics(
    time: Res<Time>,
    keys: Res<ButtonInput<KeyCode>>,
    mut commands: Commands,
    mut rng: ResMut<Rng>,
    mut table: ResMut<Table>,
    mut penny: Query<&mut Transform, With<Penny>>,
) {
    if table.over.is_some() {
        return;
    }
    let Ok(mut ptf) = penny.single_mut() else { return };
    let mut pos = ptf.translation.truncate();

    if !table.launched {
        // On the tee: Space serves her up the middle with a wobble.
        pos = Vec2::new(0.0, -60.0);
        if keys.just_pressed(KeyCode::Space) {
            table.launched = true;
            table.vel = Vec2::new((rng.range(160) as f32 - 80.0) * 2.0, 660.0);
            sfx("place");
        }
        ptf.translation.x = pos.x;
        ptf.translation.y = pos.y;
        return;
    }

    let dt = (time.delta_secs()).min(0.033) / SUBSTEPS as f32;
    table.combo_t = (table.combo_t - time.delta_secs()).max(0.0);
    for b in table.bumpers.iter_mut() {
        b.flash = (b.flash - time.delta_secs()).max(0.0);
    }

    // The ball's velocity lives locally through the substeps so collision
    // loops can borrow the table's furniture freely.
    let mut vel = table.vel;
    for _ in 0..SUBSTEPS {
        vel.y += GRAV * dt;
        // A gentle terminal velocity keeps tunneling impossible at 5 substeps.
        vel = vel.clamp_length_max(1400.0);
        pos += vel * dt;

        // Wall segments.
        for s in &table.segs {
            let c = closest_on_seg(s.a, s.b, pos);
            let d = pos - c;
            let dist = d.length();
            if dist < BALL_R && dist > 0.0001 {
                let n = d / dist;
                pos = c + n * BALL_R;
                let vn = vel.dot(n);
                if vn < 0.0 {
                    vel -= n * vn * (1.0 + s.bounce);
                }
            }
        }
        // Flippers: capsules with tip-speed transfer.
        for left in [true, false] {
            let ang = if left { table.left_ang } else { table.right_ang };
            let pressed = if left {
                keys.pressed(KeyCode::ArrowLeft) || keys.pressed(KeyCode::KeyZ)
            } else {
                keys.pressed(KeyCode::ArrowRight) || keys.pressed(KeyCode::Slash)
            };
            let swinging = pressed && ang < UP_ANG;
            let (a, b) = flipper_seg(left, ang);
            let c = closest_on_seg(a, b, pos);
            let d = pos - c;
            let dist = d.length();
            if dist < BALL_R + FLIP_R && dist > 0.0001 {
                let n = d / dist;
                pos = c + n * (BALL_R + FLIP_R);
                let vn = vel.dot(n);
                if vn < 0.0 {
                    vel -= n * vn * 1.6;
                }
                if swinging {
                    // Tip speed at the contact point: r × ω, aimed along n.
                    let r = (c - a).length();
                    let kick = (r * FLIP_SPEED).min(1100.0);
                    vel += n * kick * 0.9;
                    sfx("drop");
                }
            }
        }
        // Bumpers.
        let mut paid = 0u32;
        for bmp in table.bumpers.iter_mut() {
            let d = pos - bmp.at;
            let dist = d.length();
            if dist < BALL_R + bmp.r && dist > 0.0001 {
                let n = d / dist;
                pos = bmp.at + n * (BALL_R + bmp.r);
                let vn = vel.dot(n);
                if vn < 0.0 {
                    vel -= n * vn * 1.9;
                }
                vel += n * bmp.kick * 0.35;
                paid += bmp.pay;
                bmp.flash = 0.12;
            }
        }
        if paid > 0 {
            table.score += paid;
            stat("bumpers_bounced", 1);
            sfx("capture");
        }
        // Gate targets: knock them down (the VAULT counts hits instead).
        let mut gate_pay = 0u32;
        let mut vault_hit = false;
        let board = table.board;
        let vault_done = table.vault_hits >= 5;
        for g in table.gates.iter_mut() {
            if g.down {
                continue;
            }
            let d = pos - g.at;
            if d.length() < BALL_R + 22.0 {
                if board == 2 {
                    if !vault_done {
                        vault_hit = true;
                    }
                } else {
                    g.down = true;
                }
                gate_pay += 200;
                let n = d.normalize_or_zero();
                vel += n * 260.0;
            }
        }
        if gate_pay > 0 {
            table.score += gate_pay;
            stat("targets_knocked", 1);
            sfx("rotate");
        }
        if vault_hit {
            table.vault_hits += 1;
            if table.vault_hits >= 5 {
                table.score += 5000;
                stat("jackpots", 1);
                popup(&mut commands, "JACKPOT! THE VAULT OPENS", 28.0, AMBER, Vec2::new(0.0, 60.0));
                sfx("win");
                table.vault_hits = 0;
            } else {
                popup(
                    &mut commands,
                    &format!("VAULT {}/5", table.vault_hits),
                    20.0,
                    AMBER,
                    Vec2::new(0.0, 120.0),
                );
            }
        }
    }

    table.vel = vel;

    // Hatch state: all gates down opens the way up (boardroom hatch stays shut).
    if !table.hatch_open && table.board < 2 && table.gates.iter().all(|g| g.down) {
        table.hatch_open = true;
        popup(&mut commands, "HATCH OPEN - GO UP", 22.0, GREEN, Vec2::new(0.0, 160.0));
        sfx("levelup");
    }

    // Through the ceiling mouth: climb a board (needs the hatch).
    if pos.y > HATCH_Y && pos.x.abs() < 58.0 && table.vel.y > 0.0 {
        if table.hatch_open && table.board < 2 {
            let next = table.board + 1;
            table.score += 500;
            stat("boards_climbed", 1);
            build_board(&mut commands, &mut table, next);
            popup(&mut commands, BOARD_NAMES[next], 26.0, CYAN, Vec2::new(0.0, 0.0));
            sfx("levelup");
            pos = Vec2::new(0.0, -120.0);
            table.vel = Vec2::new(0.0, 500.0);
        } else {
            // Closed hatch: a lid you bonk.
            table.vel.y = -table.vel.y.abs() * 0.6;
            pos.y = HATCH_Y;
        }
    }

    // Below the flippers: upper boards drop you DOWN a board; the basement
    // drain takes the penny.
    if pos.y < DRAIN_Y {
        if table.board > 0 {
            let next = table.board - 1;
            build_board(&mut commands, &mut table, next);
            popup(&mut commands, &format!("DOWN TO {}", BOARD_NAMES[next]), 20.0, DIM, Vec2::new(0.0, 0.0));
            sfx("buzz");
            pos = Vec2::new(0.0, 240.0);
            table.vel = Vec2::new((rng.range(100) as f32 - 50.0) * 2.0, -60.0);
        } else {
            table.pennies -= 1;
            stat("pennies_lost", 1);
            sfx("death");
            if table.pennies > 0 {
                table.launched = false;
                pos = Vec2::new(0.0, -60.0);
                table.vel = Vec2::ZERO;
                popup(&mut commands, &format!("{} PENNIES LEFT", table.pennies), 22.0, RED, Vec2::new(0.0, 0.0));
            } else {
                table.over = Some(Timer::from_seconds(2.2, TimerMode::Once));
                popup(&mut commands, "OUT OF POCKET CHANGE", 28.0, RED, Vec2::new(0.0, 0.0));
                sfx("over");
            }
        }
    }

    ptf.translation.x = pos.x;
    ptf.translation.y = pos.y;
    // She rolls: spin with horizontal travel.
    ptf.rotate_z(-table.vel.x * 0.00016);
}

fn visuals(table: Res<Table>, mut flippers: Query<(&FlipperVis, &mut Transform)>, mut sprites: Query<&mut Sprite>) {
    for (f, mut tf) in &mut flippers {
        let ang = if f.left { table.left_ang } else { table.right_ang };
        let (a, b) = flipper_seg(f.left, ang);
        let mid = (a + b) / 2.0;
        tf.translation.x = mid.x;
        tf.translation.y = mid.y;
        tf.rotation = Quat::from_rotation_z((b - a).y.atan2((b - a).x));
    }
    for bmp in &table.bumpers {
        if let Ok(mut sp) = sprites.get_mut(bmp.ent) {
            let c = sp.color.to_srgba();
            let boost = if bmp.flash > 0.0 { 1.6 } else { 1.0 };
            // Nudge brightness back toward base each frame; flash saturates.
            let _ = c;
            sp.color.set_alpha(if bmp.flash > 0.0 { 1.0 } else { 0.92 });
            let _ = boost;
        }
    }
    for g in &table.gates {
        if let Ok(mut sp) = sprites.get_mut(g.ent) {
            sp.color = if g.down { DIM } else { AMBER };
        }
    }
}

fn hud(table: Res<Table>, mut hud: Query<&mut Text2d, With<Hud>>) {
    if let Ok(mut t) = hud.single_mut() {
        let gate = if table.board == 2 {
            format!("VAULT {}/5", table.vault_hits)
        } else if table.hatch_open {
            "HATCH OPEN".into()
        } else {
            format!(
                "TARGETS {}/{}",
                table.gates.iter().filter(|g| g.down).count(),
                table.gates.len()
            )
        };
        let launch = if table.launched { "" } else { "   SPACE LAUNCHES" };
        let s = format!(
            "SCORE {}   PENNIES {}   {}   {}{}",
            table.score,
            table.pennies.max(0),
            BOARD_NAMES[table.board],
            gate,
            launch
        );
        if t.0 != s {
            t.0 = s;
        }
    }
}

fn endgame(
    time: Res<Time>,
    mut table: ResMut<Table>,
    mut final_score: ResMut<FinalScore>,
    mut next: ResMut<NextState<Phase>>,
) {
    if let Some(t) = table.over.as_mut() {
        if t.tick(time.delta()).finished() {
            final_score.0 = table.score;
            next.set(Phase::GameOver);
        }
    }
}
