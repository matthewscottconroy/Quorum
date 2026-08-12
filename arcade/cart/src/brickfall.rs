//! BRICKFALL — four-square bricks fall, complete rows pay out, gravity
//! climbs. Deliberately its own monochrome-green machine: distinct name,
//! colors, and presentation from any commercial falling-block product.

use bevy::prelude::*;

use crate::retro::{popup, text, AMBER, DIM, GREEN, WHITE};
use crate::rng::Rng;
use crate::shell::{sfx, stat};
use crate::{FinalScore, GameTag, Phase};

pub const BLURB: &[&str] = &[
    "FOUR-SQUARE BRICKS FALL. FULL ROWS PAY.",
    "ARROWS MOVE / UP-X ROTATE / SPACE DROPS / C HOLDS",
    "THE ONLY WAY OUT IS UP",
];

const COLS: i32 = 10;
const ROWS: i32 = 20;
const CELL: f32 = 28.0;
const X0: f32 = -240.0; // left edge of the well
const Y0: f32 = 280.0; // top edge of the well

/// Piece definitions: 7 kinds × 4 rotations × 4 minos as (x, y) in a 4×4
/// box, y downward. Classic shapes — plain geometry.
type Minos = [(i32, i32); 4];
const PIECES: [[Minos; 4]; 7] = [
    // I
    [
        [(0, 1), (1, 1), (2, 1), (3, 1)],
        [(2, 0), (2, 1), (2, 2), (2, 3)],
        [(0, 1), (1, 1), (2, 1), (3, 1)],
        [(2, 0), (2, 1), (2, 2), (2, 3)],
    ],
    // O
    [
        [(1, 0), (2, 0), (1, 1), (2, 1)],
        [(1, 0), (2, 0), (1, 1), (2, 1)],
        [(1, 0), (2, 0), (1, 1), (2, 1)],
        [(1, 0), (2, 0), (1, 1), (2, 1)],
    ],
    // T
    [
        [(0, 1), (1, 1), (2, 1), (1, 0)],
        [(1, 0), (1, 1), (1, 2), (2, 1)],
        [(0, 1), (1, 1), (2, 1), (1, 2)],
        [(1, 0), (1, 1), (1, 2), (0, 1)],
    ],
    // S
    [
        [(1, 0), (2, 0), (0, 1), (1, 1)],
        [(1, 0), (1, 1), (2, 1), (2, 2)],
        [(1, 0), (2, 0), (0, 1), (1, 1)],
        [(1, 0), (1, 1), (2, 1), (2, 2)],
    ],
    // Z
    [
        [(0, 0), (1, 0), (1, 1), (2, 1)],
        [(2, 0), (1, 1), (2, 1), (1, 2)],
        [(0, 0), (1, 0), (1, 1), (2, 1)],
        [(2, 0), (1, 1), (2, 1), (1, 2)],
    ],
    // J
    [
        [(0, 0), (0, 1), (1, 1), (2, 1)],
        [(1, 0), (2, 0), (1, 1), (1, 2)],
        [(0, 1), (1, 1), (2, 1), (2, 2)],
        [(1, 0), (1, 1), (0, 2), (1, 2)],
    ],
    // L
    [
        [(2, 0), (0, 1), (1, 1), (2, 1)],
        [(1, 0), (1, 1), (1, 2), (2, 2)],
        [(0, 1), (1, 1), (2, 1), (0, 2)],
        [(0, 0), (1, 0), (1, 1), (1, 2)],
    ],
];

#[derive(Resource)]
struct Well {
    grid: [[bool; 10]; 20],
    kind: usize,
    rot: usize,
    x: i32,
    y: i32,
    queue: [usize; 3],
    bag: Vec<usize>,
    score: u32,
    lines: u32,
    fall: Timer,
    das: Timer,
    soft: Timer,
    arr: Timer, // horizontal auto-repeat — its own clock, never shared with soft drop
    hold: Option<usize>,
    hold_used: bool, // one swap per piece
}

#[derive(Resource)]
struct CellEnts {
    well: Vec<Entity>,   // ROWS*COLS sprites
    preview: Vec<Entity>, // 16 sprites for the next-piece box
    hold_box: Vec<Entity>, // 16 sprites for the hold box
    hud: Entity,          // one text entity, rewritten each change
}

pub struct BrickPlugin;

impl Plugin for BrickPlugin {
    fn build(&self, app: &mut App) {
        app.add_systems(OnEnter(Phase::Playing), setup)
            .add_systems(OnExit(Phase::Attract), rain_out)
            .add_systems(Update, attract_rain.run_if(in_state(Phase::Attract)))
            .add_systems(
                Update,
                (steer, gravity, flash_fade, paint)
                    .chain()
                    .run_if(in_state(Phase::Playing))
                    .run_if(crate::unpaused),
            );
    }
}

/// A cleared row's send-off: a white bar that burns out in a fifth of a second.
#[derive(Component)]
struct FlashRow(Timer);

fn spawn_flashes(commands: &mut Commands, rows: &[usize]) {
    for &row in rows {
        commands.spawn((
            Sprite {
                color: Color::srgba(1.0, 1.0, 1.0, 0.85),
                custom_size: Some(Vec2::new(CELL * COLS as f32, CELL - 2.0)),
                ..default()
            },
            Transform::from_translation(
                Vec3::new(X0 + CELL * COLS as f32 / 2.0, Y0 - row as f32 * CELL - CELL / 2.0, 6.0),
            ),
            FlashRow(Timer::from_seconds(0.22, TimerMode::Once)),
            GameTag,
        ));
    }
}

fn flash_fade(
    time: Res<Time>,
    mut commands: Commands,
    mut flashes: Query<(Entity, &mut FlashRow, &mut Sprite)>,
) {
    for (e, mut f, mut sprite) in &mut flashes {
        if f.0.tick(time.delta()).finished() {
            commands.entity(e).despawn();
        } else {
            sprite.color.set_alpha(0.85 * f.0.fraction_remaining());
        }
    }
}

/// Attract-mode set dressing: a slow rain of lone bricks behind the title.
#[derive(Component)]
struct RainBrick(f32); // fall speed

fn attract_rain(
    time: Res<Time>,
    mut commands: Commands,
    mut rng: ResMut<Rng>,
    mut clock: Local<f32>,
    mut drops: Query<(Entity, &RainBrick, &mut Transform)>,
) {
    *clock += time.delta_secs();
    if *clock > 0.35 {
        *clock = 0.0;
        let x = rng.between(-340.0, 340.0);
        commands.spawn((
            Sprite {
                color: GREEN.with_alpha(rng.between(0.12, 0.35)),
                custom_size: Some(Vec2::splat(CELL - 4.0)),
                ..default()
            },
            Transform::from_xyz(x, 340.0, 0.5),
            RainBrick(rng.between(50.0, 130.0)),
        ));
    }
    for (e, brick, mut tf) in &mut drops {
        tf.translation.y -= brick.0 * time.delta_secs();
        if tf.translation.y < -340.0 {
            commands.entity(e).despawn();
        }
    }
}

fn rain_out(mut commands: Commands, drops: Query<Entity, With<RainBrick>>) {
    for e in &drops {
        commands.entity(e).despawn();
    }
}

fn bag_next(rng: &mut Rng, bag: &mut Vec<usize>) -> usize {
    if bag.is_empty() {
        let mut fresh: Vec<usize> = (0..7).collect();
        // Fisher-Yates off our own PRNG.
        for i in (1..fresh.len()).rev() {
            let j = rng.range(i as u32 + 1) as usize;
            fresh.swap(i, j);
        }
        *bag = fresh;
    }
    bag.pop().unwrap()
}

fn level(lines: u32) -> u32 {
    lines / 10
}

fn fall_secs(lines: u32) -> f32 {
    (0.8 * 0.85f32.powi(level(lines) as i32)).max(0.07)
}

fn setup(mut commands: Commands, mut rng: ResMut<Rng>) {
    let mut bag = Vec::new();
    let kind = bag_next(&mut rng, &mut bag);
    let queue = [
        bag_next(&mut rng, &mut bag),
        bag_next(&mut rng, &mut bag),
        bag_next(&mut rng, &mut bag),
    ];
    commands.insert_resource(Well {
        grid: [[false; 10]; 20],
        kind,
        rot: 0,
        x: 3,
        y: 0,
        queue,
        bag,
        score: 0,
        lines: 0,
        fall: Timer::from_seconds(fall_secs(0), TimerMode::Repeating),
        das: Timer::from_seconds(0.16, TimerMode::Once),
        soft: Timer::from_seconds(0.045, TimerMode::Repeating),
        arr: Timer::from_seconds(0.045, TimerMode::Repeating),
        hold: None,
        hold_used: false,
    });

    // Well frame.
    let frame = Color::srgb(0.10, 0.35, 0.16);
    for (w, h, x, y) in [
        (CELL * COLS as f32 + 8.0, 4.0, X0 + CELL * COLS as f32 / 2.0, Y0 - CELL * ROWS as f32 - 2.0),
        (4.0, CELL * ROWS as f32 + 8.0, X0 - 2.0, Y0 - CELL * ROWS as f32 / 2.0),
        (4.0, CELL * ROWS as f32 + 8.0, X0 + CELL * COLS as f32 + 2.0, Y0 - CELL * ROWS as f32 / 2.0),
    ] {
        commands.spawn((
            Sprite { color: frame, custom_size: Some(Vec2::new(w, h)), ..default() },
            Transform::from_xyz(x, y, 1.0),
            GameTag,
        ));
    }

    // One sprite per cell, toggled by visibility. ~200 quads: trivial.
    let mut well_cells = Vec::with_capacity((ROWS * COLS) as usize);
    for row in 0..ROWS {
        for col in 0..COLS {
            let e = commands
                .spawn((
                    Sprite {
                        color: GREEN,
                        custom_size: Some(Vec2::splat(CELL - 3.0)),
                        ..default()
                    },
                    Transform::from_translation(cell_pos(col, row, 2.0)),
                    Visibility::Hidden,
                    GameTag,
                ))
                .id();
            well_cells.push(e);
        }
    }
    let mut preview = Vec::with_capacity(48);
    for q in 0..3 {
        for y in 0..4 {
            for x in 0..4 {
            let e = commands
                .spawn((
                    Sprite {
                        color: DIM,
                        custom_size: Some(Vec2::splat(CELL * 0.6)),
                        ..default()
                    },
                    Transform::from_xyz(
                        190.0 + x as f32 * CELL * 0.7,
                        -20.0 - q as f32 * 56.0 - y as f32 * CELL * 0.7,
                        2.0,
                    ),
                    Visibility::Hidden,
                    GameTag,
                ))
                .id();
            preview.push(e);
        }
    }
    }
    let mut hold_box = Vec::with_capacity(16);
    for y in 0..4 {
        for x in 0..4 {
            let e = commands
                .spawn((
                    Sprite {
                        color: DIM,
                        custom_size: Some(Vec2::splat(CELL * 0.6)),
                        ..default()
                    },
                    Transform::from_xyz(
                        190.0 + x as f32 * CELL * 0.7,
                        -235.0 - y as f32 * CELL * 0.7,
                        2.0,
                    ),
                    Visibility::Hidden,
                    GameTag,
                ))
                .id();
            hold_box.push(e);
        }
    }
    let hud = text(&mut commands, "", 24.0, WHITE, Vec3::new(230.0, 240.0, 2.0));
    commands.entity(hud).insert(GameTag);
    let nx = text(&mut commands, "NEXT", 20.0, AMBER, Vec3::new(230.0, 20.0, 2.0));
    commands.entity(nx).insert(GameTag);
    let hl = text(&mut commands, "HOLD", 20.0, AMBER, Vec3::new(230.0, -195.0, 2.0));
    commands.entity(hl).insert(GameTag);
    commands.insert_resource(CellEnts { well: well_cells, preview, hold_box, hud });
}

fn cell_pos(col: i32, row: i32, z: f32) -> Vec3 {
    Vec3::new(
        X0 + col as f32 * CELL + CELL / 2.0,
        Y0 - row as f32 * CELL - CELL / 2.0,
        z,
    )
}

fn collides(w: &Well, kind: usize, rot: usize, x: i32, y: i32) -> bool {
    PIECES[kind][rot].iter().any(|&(dx, dy)| {
        let (cx, cy) = (x + dx, y + dy);
        cx < 0 || cx >= COLS || cy >= ROWS || (cy >= 0 && w.grid[cy as usize][cx as usize])
    })
}

fn lock_piece(
    w: &mut Well,
    rng: &mut Rng,
    score_out: &mut Option<u32>,
    cleared_rows: &mut Vec<usize>,
    leveled: &mut bool,
) {
    // Lock-out scan BEFORE any grid write, so a game-over never leaves a
    // half-written piece behind.
    if PIECES[w.kind][w.rot].iter().any(|&(_, dy)| w.y + dy < 0) {
        *score_out = Some(w.score);
        stat("top_outs", 1);
        return;
    }
    for &(dx, dy) in &PIECES[w.kind][w.rot] {
        let (cx, cy) = (w.x + dx, w.y + dy);
        w.grid[cy as usize][cx as usize] = true;
    }
    stat("pieces_locked", 1);
    // Clear full rows.
    let mut cleared = 0;
    for row in 0..ROWS as usize {
        if w.grid[row].iter().all(|&c| c) {
            cleared += 1;
            cleared_rows.push(row);
            for r in (1..=row).rev() {
                w.grid[r] = w.grid[r - 1];
            }
            w.grid[0] = [false; 10];
        }
    }
    if cleared > 0 {
        sfx("clear");
        stat("lines_cleared", cleared as u64);
        if cleared >= 4 {
            stat("quads", 1);
        }
        let before = level(w.lines);
        let base = [0u32, 40, 100, 300, 1200][cleared.min(4)];
        w.score += base * (before + 1);
        w.lines += cleared as u32;
        w.fall.set_duration(std::time::Duration::from_secs_f32(fall_secs(w.lines)));
        *leveled = level(w.lines) > before;
    } else {
        sfx("place");
    }
    // Next piece. Game over only if the SPAWN position itself is blocked —
    // the player always gets the spawn frame to shift or rotate free.
    w.kind = w.queue[0];
    w.queue.rotate_left(1);
    w.queue[2] = bag_next(rng, &mut w.bag);
    w.rot = 0;
    w.x = 3;
    w.y = -1;
    w.hold_used = false;
    if collides(w, w.kind, w.rot, w.x, w.y) {
        *score_out = Some(w.score);
        stat("top_outs", 1);
    }
}

fn steer(
    keys: Res<ButtonInput<KeyCode>>,
    time: Res<Time>,
    mut commands: Commands,
    mut w: ResMut<Well>,
    mut rng: ResMut<Rng>,
    mut final_score: ResMut<FinalScore>,
    mut next: ResMut<NextState<Phase>>,
) {
    let mut over = None;
    let mut cleared_rows = Vec::new();
    let mut leveled = false;

    // Horizontal with delayed auto-shift, on its own repeat clock so a held
    // soft drop can't double-tick it into erratic diagonals.
    let dir = i32::from(keys.pressed(KeyCode::ArrowRight)) - i32::from(keys.pressed(KeyCode::ArrowLeft));
    let tapped = keys.just_pressed(KeyCode::ArrowRight) || keys.just_pressed(KeyCode::ArrowLeft);
    if tapped {
        w.das.reset();
        w.arr.reset();
        if !collides(&w, w.kind, w.rot, w.x + dir, w.y) {
            w.x += dir;
        }
    } else if dir != 0 {
        w.das.tick(time.delta());
        if w.das.finished() {
            w.arr.tick(time.delta());
            for _ in 0..w.arr.times_finished_this_tick() {
                if !collides(&w, w.kind, w.rot, w.x + dir, w.y) {
                    w.x += dir;
                }
            }
        }
    }

    // Rotation, with simple wall kicks.
    let cw = keys.just_pressed(KeyCode::ArrowUp) || keys.just_pressed(KeyCode::KeyX);
    let ccw = keys.just_pressed(KeyCode::KeyZ);
    if cw || ccw {
        let nrot = if cw { (w.rot + 1) % 4 } else { (w.rot + 3) % 4 };
        for kick in [0, -1, 1, -2, 2] {
            if !collides(&w, w.kind, nrot, w.x + kick, w.y) {
                w.rot = nrot;
                w.x += kick;
                sfx("rotate");
                break;
            }
        }
    }

    // Hold/swap: one stash per piece, C to trade.
    if keys.just_pressed(KeyCode::KeyC) && !w.hold_used {
        w.hold_used = true;
        stat("holds_used", 1);
        let stashed = w.hold;
        w.hold = Some(w.kind);
        w.kind = match stashed {
            Some(k) => k,
            None => {
                let n = w.queue[0];
                w.queue.rotate_left(1);
                w.queue[2] = bag_next(&mut rng, &mut w.bag);
                n
            }
        };
        w.rot = 0;
        w.x = 3;
        w.y = -1;
        sfx("rotate");
    }

    // Soft drop pays +1 per cell — descending fast should never be a
    // strictly worse play than waiting.
    if keys.pressed(KeyCode::ArrowDown) && !collides(&w, w.kind, w.rot, w.x, w.y + 1) {
        w.soft.tick(time.delta());
        for _ in 0..w.soft.times_finished_this_tick() {
            if collides(&w, w.kind, w.rot, w.x, w.y + 1) {
                break;
            }
            w.y += 1;
            w.score += 1;
            stat("soft_cells", 1);
        }
    }

    // Hard drop pays +2 per cell.
    if keys.just_pressed(KeyCode::Space) {
        sfx("drop");
        stat("hard_drops", 1);
        while !collides(&w, w.kind, w.rot, w.x, w.y + 1) {
            w.y += 1;
            w.score += 2;
        }
        lock_piece(&mut w, &mut rng, &mut over, &mut cleared_rows, &mut leveled);
    }

    spawn_flashes(&mut commands, &cleared_rows);
    announce_level(&mut commands, &w, leveled);
    if let Some(score) = over {
        final_score.0 = score;
        next.set(Phase::GameOver);
    }
}

/// Level-up fanfare: a banner over the well and a page-side chirp.
fn announce_level(commands: &mut Commands, w: &Well, leveled: bool) {
    if leveled {
        sfx("levelup");
        stat("levels_reached", 1);
        popup(
            commands,
            &format!("LEVEL {}", level(w.lines)),
            30.0,
            AMBER,
            Vec2::new(X0 + CELL * COLS as f32 / 2.0, 40.0),
        );
    }
}

fn gravity(
    time: Res<Time>,
    mut commands: Commands,
    mut w: ResMut<Well>,
    mut rng: ResMut<Rng>,
    mut final_score: ResMut<FinalScore>,
    mut next: ResMut<NextState<Phase>>,
) {
    w.fall.tick(time.delta());
    let mut over = None;
    let mut cleared_rows = Vec::new();
    let mut leveled = false;
    for _ in 0..w.fall.times_finished_this_tick() {
        if collides(&w, w.kind, w.rot, w.x, w.y + 1) {
            lock_piece(&mut w, &mut rng, &mut over, &mut cleared_rows, &mut leveled);
            break;
        }
        w.y += 1;
    }
    spawn_flashes(&mut commands, &cleared_rows);
    announce_level(&mut commands, &w, leveled);
    if let Some(score) = over {
        final_score.0 = score;
        next.set(Phase::GameOver);
    }
}

fn paint(
    w: Res<Well>,
    ents: Res<CellEnts>,
    mut gizmos: Gizmos,
    mut vis: Query<&mut Visibility>,
    mut texts: Query<&mut Text2d>,
) {
    let mut active = [[false; 10]; 20];
    for &(dx, dy) in &PIECES[w.kind][w.rot] {
        let (cx, cy) = (w.x + dx, w.y + dy);
        if (0..COLS).contains(&cx) && (0..ROWS).contains(&cy) {
            active[cy as usize][cx as usize] = true;
        }
    }
    // Ghost piece: dim outlines where the piece would rest on a hard drop.
    let mut gy = w.y;
    while !collides(&w, w.kind, w.rot, w.x, gy + 1) {
        gy += 1;
    }
    if gy > w.y {
        for &(dx, dy) in &PIECES[w.kind][w.rot] {
            let (cx, cy) = (w.x + dx, gy + dy);
            if (0..COLS).contains(&cx) && (0..ROWS).contains(&cy) {
                let c = cell_pos(cx, cy, 3.0).truncate();
                let h = (CELL - 6.0) / 2.0;
                gizmos.linestrip_2d(
                    [
                        c + Vec2::new(-h, -h),
                        c + Vec2::new(h, -h),
                        c + Vec2::new(h, h),
                        c + Vec2::new(-h, h),
                        c + Vec2::new(-h, -h),
                    ],
                    DIM,
                );
            }
        }
    }
    for row in 0..ROWS as usize {
        for col in 0..COLS as usize {
            if let Ok(mut v) = vis.get_mut(ents.well[row * COLS as usize + col]) {
                *v = if w.grid[row][col] || active[row][col] {
                    Visibility::Inherited
                } else {
                    Visibility::Hidden
                };
            }
        }
    }
    for q in 0..3 {
        let mins = PIECES[w.queue[q]][0];
        for y in 0..4 {
            for x in 0..4 {
                if let Ok(mut v) = vis.get_mut(ents.preview[q * 16 + y * 4 + x]) {
                    *v = if mins.contains(&(x as i32, y as i32)) {
                        Visibility::Inherited
                    } else {
                        Visibility::Hidden
                    };
                }
            }
        }
    }
    let hold_mins = w.hold.map(|k| PIECES[k][0]);
    for y in 0..4 {
        for x in 0..4 {
            if let Ok(mut v) = vis.get_mut(ents.hold_box[y * 4 + x]) {
                *v = match hold_mins {
                    Some(m) if m.contains(&(x as i32, y as i32)) => Visibility::Inherited,
                    _ => Visibility::Hidden,
                };
            }
        }
    }
    if let Ok(mut t) = texts.get_mut(ents.hud) {
        let s = format!("SCORE\n{}\n\nLINES\n{}\n\nLEVEL\n{}", w.score, w.lines, level(w.lines));
        if t.0 != s {
            t.0 = s;
        }
    }
}
