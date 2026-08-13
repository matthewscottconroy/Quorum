//! LEMONADE — the classic corner-stand business sim, rebuilt original for
//! the Top Secret floor, with the ECONOMY MODELED ON THE 1979 SOURCE: base
//! demand of 30 glasses against a 10-cent anchor price (linear gain below
//! it, inverse-square collapse above it), diminishing sign returns
//! (1 - e^(-signs/2)), heat waves that double the street, cloudy days that
//! roll for a washout thunderstorm, street-crew days that kill traffic or
//! buy you out at lunch, and a cost of goods that climbs as the summer
//! wears on. Every morning starts with an empty pitcher — you decide the
//! mix fresh each day. Cents only, and they are fictional cents.
//!
//! UP/DOWN pick a line, LEFT/RIGHT adjust (SHIFT jumps by 10), ENTER opens
//! the stand. Final score is the cash box after day fourteen.

use bevy::prelude::*;

use crate::retro::{popup, text, AMBER, CYAN, DIM, GREEN, RED, WHITE};
use crate::rng::Rng;
use crate::shell::{sfx, stat};
use crate::{FinalScore, GameTag, Phase};

pub const BLURB: &[&str] = &[
    "FOURTEEN DAYS. ONE PITCHER. NO MERCY FROM THE SKY.",
    "UP/DOWN PICK A LINE - LEFT/RIGHT ADJUST (SHIFT x10) - ENTER OPENS.",
    "TEN CENTS IS THE STREET'S IDEA OF FAIR. STRAY FAR AND LEARN.",
];

const DAYS: u32 = 14;
const SIGN_COST: i32 = 15;

const WEATHER_NAMES: [&str; 4] = ["SUNNY", "HOT AND DRY", "CLOUDY", "THUNDERSTORM"];
const SKY: [Color; 4] = [
    Color::srgb(0.25, 0.55, 0.85),
    Color::srgb(0.95, 0.65, 0.25),
    Color::srgb(0.45, 0.48, 0.55),
    Color::srgb(0.16, 0.18, 0.28),
];

/// Cost of goods climbs across the summer, like the original's sugar
/// squeeze: 2 cents early, 4 from day three, 5 from day seven.
fn glass_cost(day: u32) -> i32 {
    match day {
        0..=2 => 2,
        3..=6 => 4,
        _ => 5,
    }
}

#[derive(Resource)]
struct Stand {
    day: u32,
    money: i32,
    phase: u8, // 0 plan, 1 the day runs, 2 report, 3 season over
    field: u8, // 0 glasses, 1 signs, 2 price
    glasses: i32,
    signs: i32,
    price: i32,
    sky: u8,       // 0 sunny, 1 hot+dry, 2 cloudy (3 = storm, mid-day only)
    rain_pct: u32,  // cloudy days announce their odds
    street: bool,   // the street department is tearing up the block
    storm: bool,    // resolved at open: the washout happened
    crews: bool,    // resolved at close: the crews bought everything
    notice: String, // morning paper: cost hikes, roadwork
    cost_glass: i32,
    run_t: f32,
    demand: i32,
    made: i32,
    sold_shown: i32,
    report: String,
    over: Option<Timer>,
    dirty: bool,
    customers: Vec<(Entity, f32)>, // walker + speed
}

#[derive(Component)]
struct Hud;

#[derive(Component)]
struct Panel;

#[derive(Component)]
struct Sky;

#[derive(Component)]
struct SunDisc;

pub struct LemonadePlugin;

impl Plugin for LemonadePlugin {
    fn build(&self, app: &mut App) {
        app.add_systems(OnEnter(Phase::Playing), setup).add_systems(
            Update,
            (input, run_day, paint, endgame)
                .chain()
                .run_if(in_state(Phase::Playing))
                .run_if(crate::unpaused),
        );
    }
}

fn roll_day(rng: &mut Rng, s: &mut Stand) {
    // Original odds: mostly sunny, sometimes cloudy, sometimes a scorcher.
    let roll = rng.range(10);
    s.sky = if roll < 6 { 0 } else if roll < 8 { 2 } else { 1 };
    s.rain_pct = if s.sky == 2 { 30 + rng.range(41) } else { 0 };
    s.street = rng.chance(0.18);
    s.storm = false;
    s.crews = false;
    s.cost_glass = glass_cost(s.day);
    s.notice = match s.day {
        3 => "(YOUR MOTHER QUIT GIVING YOU FREE SUGAR: 4 CENTS A GLASS NOW.)".into(),
        7 => "(THE PRICE OF LEMONADE MIX WENT UP: 5 CENTS A GLASS NOW.)".into(),
        _ => String::new(),
    };
    if s.street {
        let road = "THE STREET DEPARTMENT IS TEARING UP THE BLOCK TODAY.";
        if s.notice.is_empty() {
            s.notice = road.into();
        } else {
            s.notice = format!("{}\n{road}", s.notice);
        }
    }
    // Fresh pitcher every morning, like the original prompts.
    s.glasses = 0;
    s.signs = 0;
    s.dirty = true;
}

fn setup(mut commands: Commands, mut rng: ResMut<Rng>) {
    // Sky, street, and the stand itself.
    commands.spawn((
        Sprite { color: SKY[0], custom_size: Some(Vec2::new(760.0, 330.0)), ..default() },
        Transform::from_xyz(0.0, 135.0, 0.2),
        Sky,
        GameTag,
    ));
    commands.spawn((
        Sprite { color: Color::srgb(0.95, 0.85, 0.35), custom_size: Some(Vec2::splat(52.0)), ..default() },
        Transform::from_xyz(-270.0, 220.0, 0.4),
        SunDisc,
        GameTag,
    ));
    commands.spawn((
        Sprite { color: Color::srgb(0.35, 0.32, 0.30), custom_size: Some(Vec2::new(760.0, 90.0)), ..default() },
        Transform::from_xyz(0.0, -75.0, 0.3),
        GameTag,
    ));
    // The stand: counter, striped awning, pitcher.
    commands.spawn((
        Sprite { color: Color::srgb(0.55, 0.38, 0.20), custom_size: Some(Vec2::new(120.0, 58.0)), ..default() },
        Transform::from_xyz(0.0, -20.0, 1.0),
        GameTag,
    ));
    for i in 0..5 {
        let c = if i % 2 == 0 { Color::srgb(0.9, 0.85, 0.2) } else { WHITE };
        commands.spawn((
            Sprite { color: c, custom_size: Some(Vec2::new(24.0, 12.0)), ..default() },
            Transform::from_xyz(-48.0 + i as f32 * 24.0, 22.0, 1.1),
            GameTag,
        ));
    }
    commands.spawn((
        Sprite { color: Color::srgb(0.85, 0.9, 0.4), custom_size: Some(Vec2::new(16.0, 22.0)), ..default() },
        Transform::from_xyz(-30.0, 2.0, 1.2),
        GameTag,
    ));
    let hud = text(&mut commands, "", 14.0, WHITE, Vec3::new(0.0, 306.0, 6.0));
    commands.entity(hud).insert((Hud, GameTag));
    let panel = text(&mut commands, "", 14.0, CYAN, Vec3::new(0.0, -190.0, 6.0));
    commands.entity(panel).insert((Panel, GameTag));
    let mut s = Stand {
        day: 1,
        money: 200,
        phase: 0,
        field: 0,
        glasses: 0,
        signs: 0,
        price: 10,
        sky: 0,
        rain_pct: 0,
        street: false,
        storm: false,
        crews: false,
        notice: String::new(),
        cost_glass: 2,
        run_t: 0.0,
        demand: 0,
        made: 0,
        sold_shown: 0,
        report: String::new(),
        over: None,
        dirty: true,
        customers: Vec::new(),
    };
    roll_day(&mut rng, &mut s);
    commands.insert_resource(s);
}

fn open_stand(commands: &mut Commands, rng: &mut Rng, s: &mut Stand) {
    // Clamp the mix to what the cash box can cover.
    let max_glasses = ((s.money - s.signs * SIGN_COST).max(0) / s.cost_glass).max(0);
    s.glasses = s.glasses.min(max_glasses);
    let spend = s.glasses * s.cost_glass + s.signs * SIGN_COST;
    if s.glasses <= 0 {
        s.report = "NOTHING MIXED. THE STREET SHRUGS AND WALKS ON.".into();
        s.phase = 2;
        s.dirty = true;
        return;
    }
    s.money -= spend;
    stat("days_open", 1);
    stat("signs_posted", s.signs.max(0) as u64);
    // The 1979 demand curve: 30 glasses is the street's appetite at the
    // 10-cent anchor. Cheaper climbs gently (up to +80%); dearer collapses
    // by the inverse square. Signs multiply by 1 + (1 - e^(-signs/2)).
    let p = s.price.max(1) as f32;
    let n1 = if s.price <= 10 {
        (10.0 - p) / 10.0 * 0.8 * 30.0 + 30.0
    } else {
        (10.0 * 10.0 * 30.0) / (p * p)
    };
    let v = 1.0 - (-(s.signs.max(0) as f32) * 0.5).exp();
    let mut n2 = n1 * (1.0 + v);
    if s.sky == 1 {
        n2 *= 2.0; // heat wave: everyone is thirsty
    }
    if s.sky == 2 && rng.range(100) < s.rain_pct {
        s.storm = true; // the clouds meant it
        n2 = 0.0;
    }
    if s.street && !s.storm {
        if rng.chance(0.5) {
            s.crews = true; // thirsty crews buy the lot at lunch
        } else {
            n2 *= 0.1; // dug-up street: no foot traffic
        }
    }
    s.demand = if s.crews { s.glasses } else { n2 as i32 };
    s.made = s.glasses;
    s.sold_shown = 0;
    s.run_t = 0.0;
    s.phase = 1;
    s.dirty = true;
    sfx("coin");
    // Walkers for the day: one sprite per ~4 would-be customers.
    let walkers = (s.demand / 4).clamp(2, 26);
    for i in 0..walkers {
        let y = -52.0 - (i % 3) as f32 * 14.0;
        let e = commands
            .spawn((
                Sprite {
                    color: Color::srgb(
                        0.4 + 0.06 * (i % 7) as f32,
                        0.35 + 0.05 * (i % 5) as f32,
                        0.5 + 0.05 * (i % 4) as f32,
                    ),
                    custom_size: Some(Vec2::new(10.0, 16.0)),
                    ..default()
                },
                Transform::from_xyz(-420.0 - i as f32 * 34.0, y, 2.0),
                GameTag,
            ))
            .id();
        s.customers.push((e, 60.0 + (i % 5) as f32 * 22.0));
    }
}

fn close_day(commands: &mut Commands, s: &mut Stand) {
    let sold = s.demand.min(s.made);
    let spoiled = (s.made - sold).max(0);
    let revenue = sold * s.price;
    s.money += revenue;
    stat("glasses_sold", sold.max(0) as u64);
    stat("glasses_spoiled", spoiled.max(0) as u64);
    if s.storm {
        stat("rainouts", 1);
    }
    if sold >= s.made && (s.demand > s.made || s.crews) {
        stat("sellouts", 1);
    }
    let sky_line = if s.storm {
        "THUNDERSTORM".to_string()
    } else {
        format!(
            "{}{}",
            WEATHER_NAMES[s.sky as usize],
            if s.crews {
                " - THE CREWS BOUGHT THE LOT AT LUNCH!"
            } else if s.street {
                " - TORN-UP STREET, NO TRAFFIC"
            } else {
                ""
            }
        )
    };
    let verdict = if s.storm {
        "A WALL OF RAIN. EVERYTHING DOWN THE DRAIN."
    } else if s.crews {
        "BLESS THE STREET DEPARTMENT."
    } else if s.demand > s.made {
        "SOLD OUT - THE LINE WANTED MORE."
    } else if spoiled > s.made / 2 {
        "MOSTLY SPOILED. AMBITION HAS A PRICE."
    } else if revenue > s.made * s.cost_glass {
        "A FAIR DAY'S TRADE."
    } else {
        "YOU PAID THE STREET FOR THE PRIVILEGE."
    };
    s.report = format!(
        "DAY {} - {sky_line}\nSOLD {sold}/{} AT {} CENTS - TOOK IN {revenue}\n{verdict}",
        s.day, s.made, s.price
    );
    for (e, _) in s.customers.drain(..) {
        commands.entity(e).despawn();
    }
    s.phase = 2;
    s.dirty = true;
    sfx(if sold > 0 { "clear" } else { "buzz" });
    popup(
        commands,
        &format!("+{revenue}"),
        18.0,
        if revenue > 0 { GREEN } else { RED },
        Vec2::new(0.0, 60.0),
    );
}

fn input(
    keys: Res<ButtonInput<KeyCode>>,
    mut commands: Commands,
    mut rng: ResMut<Rng>,
    mut s: ResMut<Stand>,
) {
    if s.over.is_some() {
        return;
    }
    match s.phase {
        0 => {
            if keys.just_pressed(KeyCode::ArrowUp) {
                s.field = (s.field + 2) % 3;
                s.dirty = true;
                sfx("tick");
            }
            if keys.just_pressed(KeyCode::ArrowDown) {
                s.field = (s.field + 1) % 3;
                s.dirty = true;
                sfx("tick");
            }
            let step = if keys.pressed(KeyCode::ShiftLeft) || keys.pressed(KeyCode::ShiftRight) {
                10
            } else {
                1
            };
            let delta = i32::from(keys.just_pressed(KeyCode::ArrowRight))
                - i32::from(keys.just_pressed(KeyCode::ArrowLeft));
            if delta != 0 {
                match s.field {
                    0 => s.glasses = (s.glasses + delta * step).clamp(0, 300),
                    1 => s.signs = (s.signs + delta * step).clamp(0, 20),
                    _ => s.price = (s.price + delta * step).clamp(1, 60),
                }
                s.dirty = true;
                sfx("tick");
            }
            if keys.just_pressed(KeyCode::Enter) || keys.just_pressed(KeyCode::Space) {
                open_stand(&mut commands, &mut rng, &mut s);
            }
        }
        2 => {
            if keys.just_pressed(KeyCode::Enter) || keys.just_pressed(KeyCode::Space) {
                if s.day >= DAYS || s.money <= 0 {
                    s.phase = 3;
                    let broke = s.money <= 0;
                    s.report = if broke {
                        format!("OUT OF LEMONS AND LUCK ON DAY {}.", s.day)
                    } else {
                        format!("SEASON CLOSED. CASH BOX: {} CENTS.", s.money)
                    };
                    stat("stands_retired", 1);
                    s.over = Some(Timer::from_seconds(2.6, TimerMode::Once));
                    s.dirty = true;
                    sfx(if broke { "over" } else { "win" });
                } else {
                    s.day += 1;
                    s.phase = 0;
                    roll_day(&mut rng, &mut s);
                }
            }
        }
        _ => {}
    }
}

fn run_day(
    time: Res<Time>,
    mut commands: Commands,
    mut s: ResMut<Stand>,
    mut tfs: Query<&mut Transform>,
) {
    if s.phase != 1 {
        return;
    }
    let dt = time.delta_secs();
    s.run_t += dt;
    // Walkers cross; some pause at the stand for a beat.
    for i in 0..s.customers.len() {
        let (e, speed) = s.customers[i];
        if let Ok(mut tf) = tfs.get_mut(e) {
            let near_stand = tf.translation.x > -26.0 && tf.translation.x < 6.0;
            let will_buy = (i as i32) < (s.demand.min(s.made) / 4 + 1);
            let pace = if near_stand && will_buy { speed * 0.25 } else { speed };
            tf.translation.x += pace * dt;
        }
    }
    // The sales counter ticks up over the day.
    let frac = (s.run_t / 3.6).min(1.0);
    let target = (s.demand.min(s.made) as f32 * frac) as i32;
    if target > s.sold_shown {
        s.sold_shown = target;
        s.dirty = true;
        if s.sold_shown % 5 == 0 {
            sfx("chip");
        }
    }
    if s.run_t >= 4.0 {
        close_day(&mut commands, &mut s);
    }
}

#[allow(clippy::type_complexity)]
fn paint(
    mut s: ResMut<Stand>,
    mut hud: Query<&mut Text2d, (With<Hud>, Without<Panel>)>,
    mut panel: Query<&mut Text2d, (With<Panel>, Without<Hud>)>,
    mut sky: Query<&mut Sprite, (With<Sky>, Without<SunDisc>)>,
    mut sun: Query<&mut Sprite, (With<SunDisc>, Without<Sky>)>,
) {
    if !s.dirty {
        return;
    }
    s.dirty = false;
    let shown_weather = if s.phase != 0 && s.storm { 3 } else { s.sky };
    if let Ok(mut sp) = sky.single_mut() {
        sp.color = SKY[shown_weather as usize];
    }
    if let Ok(mut sp) = sun.single_mut() {
        sp.color = match shown_weather {
            0 | 1 => Color::srgb(0.95, 0.85, 0.35),
            _ => Color::srgba(0.8, 0.8, 0.85, 0.45), // a cloud where the sun was
        };
    }
    if let Ok(mut t) = hud.single_mut() {
        let rain = if s.sky == 2 { format!(" ({}% CHANCE OF RAIN)", s.rain_pct) } else { String::new() };
        let line = format!(
            "DAY {}/{}   CASH BOX {} CENTS   SKY: {}{}   COST {} CENTS/GLASS",
            s.day,
            DAYS,
            s.money,
            WEATHER_NAMES[shown_weather as usize],
            rain,
            s.cost_glass
        );
        if t.0 != line {
            t.0 = line;
        }
    }
    if let Ok(mut t) = panel.single_mut() {
        let line = match s.phase {
            0 => {
                let mark = |f: u8| if s.field == f { ">" } else { " " };
                let notice = if s.notice.is_empty() { String::new() } else { format!("{}\n", s.notice) };
                format!(
                    "{notice}{} GLASSES TO MIX  {:>3}   (COSTS {})\n{} SIGNS TO POST   {:>3}   (COSTS {})\n{} PRICE PER GLASS {:>3} CENTS\nUP/DOWN PICK - LEFT/RIGHT ADJUST (SHIFT x10) - ENTER OPENS THE STAND",
                    mark(0),
                    s.glasses,
                    s.glasses * s.cost_glass,
                    mark(1),
                    s.signs,
                    s.signs * SIGN_COST,
                    mark(2),
                    s.price
                )
            }
            1 => format!("OPEN FOR BUSINESS... SOLD {}", s.sold_shown),
            _ => format!("{}\nENTER CONTINUES", s.report),
        };
        if t.0 != line {
            t.0 = line;
        }
    }
    let _ = (AMBER, DIM);
}

fn endgame(
    time: Res<Time>,
    mut s: ResMut<Stand>,
    mut final_score: ResMut<FinalScore>,
    mut next: ResMut<NextState<Phase>>,
) {
    if let Some(timer) = s.over.as_mut() {
        if timer.tick(time.delta()).finished() {
            final_score.0 = s.money.max(0) as u32;
            next.set(Phase::GameOver);
        }
    }
}
