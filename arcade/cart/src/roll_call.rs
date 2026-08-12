//! ROLL CALL — the traditional dice category game (the old public-domain
//! Yacht family): five dice, three rolls, thirteen boxes, agonizing choices.
//! Original name and presentation. Solo is a score-attack for the ladder;
//! online seats up to eight, turn by turn over the relay — the roller
//! broadcasts each roll and the box they filled, everyone keeps the sheet.
//!
//! Categories: ONES..SIXES (upper, with the +35 bonus at 63), three- and
//! four-of-a-kind (sum), full house (25), small run (30), large run (40),
//! FIVE ALIKE (50), and chance (sum).

use bevy::prelude::*;
use serde::{Deserialize, Serialize};

use crate::retro::{text, PLAYER_COLORS, AMBER, CYAN, DIM, GREEN, WHITE};
use crate::rng::Rng;
use crate::shell::{net_send, sfx, stat};
use crate::{FinalScore, GameTag, NetIn, NetMode, Phase};

pub const BLURB: &[&str] = &[
    "FIVE DICE. THREE ROLLS. THIRTEEN BOXES.",
    "R ROLLS - CLICK DICE TO HOLD - CLICK A BOX TO SCORE IT.",
    "SOLO FOR THE LADDER, OR UP TO EIGHT AT THE TABLE.",
];

const CATS: [&str; 13] = [
    "ONES", "TWOS", "THREES", "FOURS", "FIVES", "SIXES",
    "3 OF A KIND", "4 OF A KIND", "FULL HOUSE", "SMALL RUN", "LARGE RUN", "FIVE ALIKE", "CHANCE",
];

fn score_cat(cat: usize, d: &[u8; 5]) -> u32 {
    let mut counts = [0u8; 7];
    for &v in d {
        counts[v as usize] += 1;
    }
    let sum: u32 = d.iter().map(|&v| v as u32).sum();
    match cat {
        0..=5 => counts[cat + 1] as u32 * (cat as u32 + 1),
        6 => if counts.iter().any(|&c| c >= 3) { sum } else { 0 },
        7 => if counts.iter().any(|&c| c >= 4) { sum } else { 0 },
        8 => {
            let three = counts.iter().any(|&c| c == 3);
            let two = counts.iter().any(|&c| c == 2);
            let five = counts.iter().any(|&c| c == 5);
            if (three && two) || five { 25 } else { 0 }
        }
        9 => {
            let has = |a: usize, b: usize| (a..=b).all(|v| counts[v] > 0);
            if has(1, 4) || has(2, 5) || has(3, 6) { 30 } else { 0 }
        }
        10 => {
            let has = |a: usize, b: usize| (a..=b).all(|v| counts[v] > 0);
            if has(1, 5) || has(2, 6) { 40 } else { 0 }
        }
        11 => if counts.iter().any(|&c| c == 5) { 50 } else { 0 },
        _ => sum,
    }
}

struct Sheet {
    boxes: [Option<u32>; 13],
}

impl Sheet {
    fn upper(&self) -> u32 {
        (0..6).map(|i| self.boxes[i].unwrap_or(0)).sum()
    }
    fn total(&self) -> u32 {
        let bonus = if self.upper() >= 63 { 35 } else { 0 };
        self.boxes.iter().map(|b| b.unwrap_or(0)).sum::<u32>() + bonus
    }
    fn done(&self) -> bool {
        self.boxes.iter().all(|b| b.is_some())
    }
}

#[derive(Resource)]
struct Table {
    sheets: Vec<Sheet>,
    seats: Vec<usize>, // seat numbers, in turn order
    turn: usize,       // index into seats
    my_seat: usize,
    net: bool,
    dice: [u8; 5],
    held: [bool; 5],
    rolls_left: u32,
    over: Option<Timer>,
    result: String,
    dirty: bool,
}

impl Table {
    fn my_turn(&self) -> bool {
        !self.net || self.seats[self.turn] == self.my_seat
    }
}

#[derive(Serialize, Deserialize)]
struct WRoll {
    t: String, // "roll": the roller's dice after a roll
    d: [u8; 5],
    left: u32,
}

#[derive(Serialize, Deserialize)]
struct WScore {
    t: String, // "score": the roller filled a box
    cat: u8,
    val: u32,
}

#[derive(Component)]
struct DieVis(usize);

#[derive(Component)]
struct Hud;

#[derive(Component)]
struct SheetText;

pub struct RollCallPlugin;

impl Plugin for RollCallPlugin {
    fn build(&self, app: &mut App) {
        app.add_systems(OnEnter(Phase::Playing), setup).add_systems(
            Update,
            (net_apply, input, paint, endgame)
                .chain()
                .run_if(in_state(Phase::Playing))
                .run_if(crate::unpaused),
        );
    }
}

fn setup(mut commands: Commands, net: Res<NetMode>) {
    let (seats, my_seat, is_net) = match &net.0 {
        Some(cfg) => {
            let s: Vec<usize> = cfg
                .present
                .iter()
                .enumerate()
                .filter(|(_, p)| **p)
                .map(|(i, _)| i)
                .collect();
            (s, cfg.seat as usize, true)
        }
        None => (vec![0], 0, false),
    };
    let sheets = seats.iter().map(|_| Sheet { boxes: [None; 13] }).collect();
    commands.insert_resource(Table {
        sheets,
        seats,
        turn: 0,
        my_seat,
        net: is_net,
        dice: [1, 2, 3, 4, 5],
        held: [false; 5],
        rolls_left: 3,
        over: None,
        result: String::new(),
        dirty: true,
    });
    // Five dice slots.
    for i in 0..5 {
        commands
            .spawn((
                Sprite { color: WHITE, custom_size: Some(Vec2::splat(62.0)), ..default() },
                Transform::from_xyz(-260.0 + i as f32 * 90.0, 190.0, 2.0),
                DieVis(i),
                GameTag,
            ))
            .with_children(|kid| {
                kid.spawn((
                    Text2d::new("?"),
                    TextFont { font_size: 34.0, ..default() },
                    TextColor(Color::srgb(0.05, 0.05, 0.1)),
                    Transform::from_xyz(0.0, 0.0, 0.1),
                ));
            });
    }
    let hud = text(&mut commands, "", 15.0, WHITE, Vec3::new(0.0, 288.0, 5.0));
    commands.entity(hud).insert((Hud, GameTag));
    let sheet = text(&mut commands, "", 14.0, CYAN, Vec3::new(0.0, -90.0, 5.0));
    commands.entity(sheet).insert((SheetText, GameTag));
}

fn roll(rng: &mut Rng, t: &mut Table) {
    for i in 0..5 {
        if !t.held[i] {
            t.dice[i] = 1 + (rng.range(6) as u8);
        }
    }
    t.rolls_left -= 1;
    t.dirty = true;
    sfx("drop");
}

fn advance_turn(t: &mut Table) {
    t.turn = (t.turn + 1) % t.seats.len();
    t.rolls_left = 3;
    t.held = [false; 5];
    t.dice = [1, 2, 3, 4, 5];
    t.dirty = true;
}

fn apply_score(t: &mut Table, who: usize, cat: usize, val: u32) {
    if let Some(sheet) = t.sheets.get_mut(who) {
        sheet.boxes[cat] = Some(val);
    }
    advance_turn(t);
}

#[allow(clippy::too_many_arguments)]
fn input(
    keys: Res<ButtonInput<KeyCode>>,
    buttons: Res<ButtonInput<MouseButton>>,
    windows: Query<&Window>,
    cameras: Query<(&Camera, &GlobalTransform)>,
    mut rng: ResMut<Rng>,
    mut t: ResMut<Table>,
) {
    if t.over.is_some() || !t.my_turn() {
        return;
    }
    if (keys.just_pressed(KeyCode::KeyR) || keys.just_pressed(KeyCode::Space)) && t.rolls_left > 0 {
        roll(&mut rng, &mut t);
        stat("rolls_thrown", 1);
        if t.net {
            if let Ok(m) =
                serde_json::to_string(&WRoll { t: "roll".into(), d: t.dice, left: t.rolls_left })
            {
                net_send(&m);
            }
        }
    }
    if !buttons.just_pressed(MouseButton::Left) {
        return;
    }
    let Ok(window) = windows.single() else { return };
    let Ok((camera, cam_tf)) = cameras.single() else { return };
    let Some(world) = crate::retro::cursor_world(window, camera, cam_tf) else { return };
    // Hold/unhold dice (only after the first roll of a turn).
    if t.rolls_left < 3 {
        for i in 0..5 {
            let cx = -260.0 + i as f32 * 90.0;
            if (world.x - cx).abs() < 34.0 && (world.y - 190.0).abs() < 34.0 {
                t.held[i] = !t.held[i];
                t.dirty = true;
                sfx("tick");
                return;
            }
        }
    }
    // Score a box: the sheet rows are laid out by paint() below.
    if t.rolls_left == 3 {
        return; // roll first
    }
    let row_h = 22.0;
    let top = 96.0;
    for cat in 0..13 {
        let y = top - cat as f32 * row_h;
        if (world.y - y).abs() < row_h / 2.0 && world.x.abs() < 330.0 {
            let me = t.turn;
            if t.sheets[me].boxes[cat].is_some() {
                sfx("buzz");
                return;
            }
            let val = score_cat(cat, &t.dice);
            if cat == 11 && val == 50 {
                stat("five_alikes", 1);
            }
            if val == 0 {
                stat("zeroes_taken", 1);
            }
            stat("boxes_filled", 1);
            apply_score(&mut t, me, cat, val);
            sfx("place");
            if t.net {
                if let Ok(m) =
                    serde_json::to_string(&WScore { t: "score".into(), cat: cat as u8, val })
                {
                    net_send(&m);
                }
            }
            check_over(&mut t);
            return;
        }
    }
}

fn check_over(t: &mut Table) {
    if t.sheets.iter().all(|s| s.done()) && t.over.is_none() {
        let me_idx = t.seats.iter().position(|&s| s == t.my_seat).unwrap_or(0);
        let mine = t.sheets[me_idx].total();
        let best = t.sheets.iter().map(|s| s.total()).max().unwrap_or(0);
        t.result = if t.seats.len() == 1 {
            format!("SHEET FULL. {mine} POINTS.")
        } else if mine == best {
            format!("YOU TAKE THE TABLE. {mine} POINTS.")
        } else {
            format!("BEST SHEET {best}. YOURS {mine}.")
        };
        if mine == best && t.seats.len() > 1 {
            stat("tables_won", 1);
        }
        t.over = Some(Timer::from_seconds(2.6, TimerMode::Once));
        t.dirty = true;
        sfx(if mine == best { "win" } else { "over" });
    }
}

fn net_apply(mut events: EventReader<NetIn>, net: Res<NetMode>, mut t: ResMut<Table>) {
    let Some(cfg) = &net.0 else {
        events.clear();
        return;
    };
    for ev in events.read() {
        if ev.left {
            // A leaver's sheet fills with zeros so the game can end.
            if let Some(idx) = t.seats.iter().position(|&s| s == ev.seat as usize) {
                for b in t.sheets[idx].boxes.iter_mut() {
                    b.get_or_insert(0);
                }
                if t.turn == idx {
                    advance_turn(&mut t);
                }
                check_over(&mut t);
            }
            continue;
        }
        if ev.seat == cfg.seat {
            continue;
        }
        let Ok(v) = serde_json::from_str::<serde_json::Value>(&ev.data) else { continue };
        match v.get("t").and_then(|s| s.as_str()) {
            Some("roll") => {
                if let Ok(r) = serde_json::from_str::<WRoll>(&ev.data) {
                    if t.seats.get(t.turn) == Some(&(ev.seat as usize)) {
                        t.dice = r.d;
                        t.rolls_left = r.left;
                        t.dirty = true;
                        sfx("drop");
                    }
                }
            }
            Some("score") => {
                if let Ok(s) = serde_json::from_str::<WScore>(&ev.data) {
                    if t.seats.get(t.turn) == Some(&(ev.seat as usize)) {
                        let who = t.turn;
                        apply_score(&mut t, who, (s.cat as usize).min(12), s.val);
                        check_over(&mut t);
                    }
                }
            }
            _ => {}
        }
    }
}

fn paint(
    mut t: ResMut<Table>,
    mut dice: Query<(&DieVis, &mut Sprite, &Children)>,
    mut texts: Query<&mut Text2d, (Without<Hud>, Without<SheetText>)>,
    mut hud: Query<&mut Text2d, (With<Hud>, Without<SheetText>)>,
    mut sheet: Query<&mut Text2d, (With<SheetText>, Without<Hud>)>,
) {
    if !t.dirty {
        return;
    }
    t.dirty = false;
    for (d, mut sp, kids) in &mut dice {
        sp.color = if t.held[d.0] { AMBER } else { WHITE };
        for kid in kids {
            if let Ok(mut txt) = texts.get_mut(*kid) {
                txt.0 = if t.rolls_left == 3 { "?".into() } else { t.dice[d.0].to_string() };
            }
        }
    }
    if let Ok(mut h) = hud.single_mut() {
        let s = if t.over.is_some() {
            t.result.clone()
        } else {
            let whose = if t.my_turn() {
                "YOUR TURN".to_string()
            } else {
                format!("SEAT {} ROLLING", t.seats[t.turn] + 1)
            };
            format!("{whose}   ROLLS LEFT {}   (R ROLLS, CLICK DICE TO HOLD)", t.rolls_left)
        };
        if h.0 != s {
            h.0 = s;
        }
    }
    if let Ok(mut sh) = sheet.single_mut() {
        let mut lines = Vec::new();
        for cat in 0..13 {
            let mut cells = Vec::new();
            for (i, s) in t.sheets.iter().enumerate() {
                let mark = match s.boxes[cat] {
                    Some(v) => format!("{v:>3}"),
                    None if i == t.turn && t.rolls_left < 3 => {
                        format!("({})", score_cat(cat, &t.dice))
                    }
                    None => "  -".into(),
                };
                cells.push(format!("{mark:>5}"));
            }
            lines.push(format!("{:<12}{}", CATS[cat], cells.join(" ")));
        }
        let totals: Vec<String> = t.sheets.iter().map(|s| format!("{:>5}", s.total())).collect();
        lines.push(format!("{:<12}{}", "TOTAL", totals.join(" ")));
        let s = lines.join("\n");
        if sh.0 != s {
            sh.0 = s;
        }
    }
    let _ = PLAYER_COLORS;
    let _ = GREEN;
    let _ = DIM;
}

fn endgame(
    time: Res<Time>,
    mut t: ResMut<Table>,
    mut final_score: ResMut<FinalScore>,
    mut next: ResMut<NextState<Phase>>,
) {
    if let Some(timer) = t.over.as_mut() {
        if timer.tick(time.delta()).finished() {
            let me_idx = t.seats.iter().position(|&s| s == t.my_seat).unwrap_or(0);
            final_score.0 = t.sheets[me_idx].total();
            next.set(Phase::GameOver);
        }
    }
}
