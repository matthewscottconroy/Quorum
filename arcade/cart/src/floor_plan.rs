//! FLOOR PLAN — an original roll-and-buy property board game set in a
//! 40-space office tower. Buy deeds, complete a wing, build desks, and
//! bleed your rivals dry with rent. Last solvent player (or richest when
//! the fiscal year ends) wins. Original board, names, and card decks.
//! Solo seats you against three bots; online seats two to eight, turn by
//! turn — the acting player simulates locally and broadcasts a full state
//! snapshot that every other client adopts (same trust model as scores).
//!
//! Deliberate scope: no trading, no auctions, no mortgages.

use bevy::prelude::*;
use serde::{Deserialize, Serialize};

use crate::retro::{cursor_world, text, AMBER, CYAN, DIM, GREEN, PLAYER_COLORS, RED, WHITE};
use crate::rng::Rng;
use crate::shell::{net_send, sfx, stat};
use crate::{FinalScore, GameTag, NetIn, NetMode, Phase};

pub const BLURB: &[&str] = &[
    "BUY THE FLOOR OUT FROM UNDER THEM.",
    "R ROLLS. B BUYS. CLICK A WING YOU OWN TO BUILD DESKS. E ENDS TURN.",
    "LAST SOLVENT SUIT WINS. ONLINE SEATS EIGHT.",
];

#[derive(Clone, Copy, PartialEq)]
enum Kind {
    Start,
    Prop(u8),
    Elev,
    Util,
    Tax(i32),
    Memo,
    Rumor,
    Hr,
    GoHr,
    Lounge,
}

struct SpaceDef {
    name: &'static str,
    kind: Kind,
    price: i32,
}

const fn pr(name: &'static str, g: u8, price: i32) -> SpaceDef {
    SpaceDef { name, kind: Kind::Prop(g), price }
}
const fn sp(name: &'static str, kind: Kind, price: i32) -> SpaceDef {
    SpaceDef { name, kind, price }
}

const SPACES: [SpaceDef; 40] = [
    sp("START", Kind::Start, 0),
    pr("MAIL SLOT", 0, 60),
    sp("MEMO", Kind::Memo, 0),
    pr("MAIL BIN", 0, 60),
    sp("DESK FEE", Kind::Tax(200), 0),
    sp("ELEVATOR A", Kind::Elev, 200),
    pr("SUPPLY SHELF", 1, 100),
    sp("RUMOR", Kind::Rumor, 0),
    pr("SUPPLY CART", 1, 100),
    pr("SUPPLY CAGE", 1, 120),
    sp("HR REVIEW", Kind::Hr, 0),
    pr("CUBICLE 1A", 2, 140),
    sp("COFFEE MAKER", Kind::Util, 150),
    pr("CUBICLE 2B", 2, 140),
    pr("CUBICLE 3C", 2, 160),
    sp("ELEVATOR B", Kind::Elev, 200),
    pr("VENDING NOOK", 3, 180),
    sp("MEMO", Kind::Memo, 0),
    pr("MICROWAVE ROW", 3, 180),
    pr("SNACK TABLE", 3, 200),
    sp("LOUNGE", Kind::Lounge, 0),
    pr("HUDDLE ROOM", 4, 220),
    sp("RUMOR", Kind::Rumor, 0),
    pr("WAR ROOM", 4, 220),
    pr("BOARD ANNEX", 4, 240),
    sp("ELEVATOR C", Kind::Elev, 200),
    pr("AD ALCOVE", 5, 260),
    pr("BRAND LAB", 5, 260),
    sp("COPY ROOM", Kind::Util, 150),
    pr("PITCH DECK", 5, 280),
    sp("GO TO HR", Kind::GoHr, 0),
    pr("CORNER SUITE", 6, 300),
    pr("VP LOUNGE", 6, 300),
    sp("MEMO", Kind::Memo, 0),
    pr("C-SUITE", 6, 320),
    sp("ELEVATOR D", Kind::Elev, 200),
    sp("RUMOR", Kind::Rumor, 0),
    pr("SKY TERRACE", 7, 350),
    sp("PLANT FEE", Kind::Tax(100), 0),
    pr("PENTHOUSE", 7, 400),
];

const GROUP_COLORS: [Color; 8] = [
    Color::srgb(0.55, 0.38, 0.22),
    Color::srgb(0.55, 0.80, 0.95),
    Color::srgb(0.85, 0.45, 0.75),
    Color::srgb(0.95, 0.60, 0.20),
    Color::srgb(0.90, 0.20, 0.20),
    Color::srgb(0.95, 0.90, 0.25),
    Color::srgb(0.25, 0.75, 0.30),
    Color::srgb(0.30, 0.35, 0.90),
];

const CELL: f32 = 52.0;
const HALF: f32 = 260.0;
const ROUND_CAP: u32 = 40;

fn space_xy(i: usize) -> Vec2 {
    match i {
        0..=10 => Vec2::new(HALF - i as f32 * CELL, -HALF),
        11..=19 => Vec2::new(-HALF, -HALF + (i - 10) as f32 * CELL),
        20..=30 => Vec2::new(-HALF + (i - 20) as f32 * CELL, HALF),
        _ => Vec2::new(HALF, HALF - (i - 30) as f32 * CELL),
    }
}

fn inward(i: usize) -> Vec2 {
    let p = space_xy(i);
    Vec2::new(
        if p.x <= -HALF { 1.0 } else if p.x >= HALF { -1.0 } else { 0.0 },
        if p.y <= -HALF { 1.0 } else if p.y >= HALF { -1.0 } else { 0.0 },
    )
}

fn group_size(g: u8) -> u8 {
    match g {
        0 | 7 => 2,
        _ => 3,
    }
}

#[derive(Clone, Copy)]
enum Card {
    Cash(i32),
    Move(usize),
    GoHr,
    Each(i32),
}

const MEMOS: [(&str, Card); 8] = [
    ("EXPENSE REPORT APPROVED. +120.", Card::Cash(120)),
    ("PRINTER JAM FINE. -60.", Card::Cash(-60)),
    ("ANNUAL REVIEW. ADVANCE TO START.", Card::Move(0)),
    ("AUDIT. PAY EACH RIVAL 25.", Card::Each(-25)),
    ("BIRTHDAY FUND. COLLECT 15 EACH.", Card::Each(15)),
    ("CALLED TO THE LOUNGE.", Card::Move(20)),
    ("SECURITY ESCORT TO HR.", Card::GoHr),
    ("STAMPS IN THE COUCH. +40.", Card::Cash(40)),
];

const RUMORS: [(&str, Card); 8] = [
    ("STOCK TIP PAYS OFF. +150.", Card::Cash(150)),
    ("PARKING TICKET. -50.", Card::Cash(-50)),
    ("HEADHUNTED. ADVANCE TO START.", Card::Move(0)),
    ("KARAOKE NIGHT. PAY EACH RIVAL 20.", Card::Each(-20)),
    ("SETTLED A BET. COLLECT 10 EACH.", Card::Each(10)),
    ("SNACK RUN TO THE VENDING NOOK.", Card::Move(16)),
    ("BLAMED FOR THE LEAK. GO TO HR.", Card::GoHr),
    ("REBATE CHECK CLEARS. +90.", Card::Cash(90)),
];

#[derive(Clone, Serialize, Deserialize)]
struct Player {
    seat: usize,
    human: bool,
    money: i32,
    pos: usize,
    hr: u8,
    alive: bool,
    doubles: u8,
}

#[derive(Resource)]
struct Board {
    players: Vec<Player>,
    owner: [i8; 40],
    desks: [u8; 40],
    turn: usize,
    tphase: u8, // 0 pre-roll, 1 buy offer, 2 turn done
    dice: (u8, u8),
    round: u32,
    my_idx: usize,
    net: bool,
    over: Option<Timer>,
    winner: i8,
    result: String,
    log: Vec<String>,
    dirty: bool,
    bot_t: Timer,
    tokens: Vec<Entity>,
}

impl Board {
    fn me_acting(&self) -> bool {
        !self.over.is_some() && (!self.net || self.turn == self.my_idx)
    }
    fn cur(&self) -> &Player {
        &self.players[self.turn]
    }
    fn owns_group(&self, who: usize, g: u8) -> bool {
        let need = group_size(g);
        let have = (0..40)
            .filter(|&i| matches!(SPACES[i].kind, Kind::Prop(pg) if pg == g))
            .filter(|&i| self.owner[i] == who as i8)
            .count();
        have as u8 >= need
    }
    fn count_kind(&self, who: usize, kind: Kind) -> i32 {
        (0..40).filter(|&i| SPACES[i].kind == kind && self.owner[i] == who as i8).count() as i32
    }
    fn worth(&self, who: usize) -> i32 {
        let p = &self.players[who];
        if !p.alive {
            return 0;
        }
        let deeds: i32 = (0..40)
            .filter(|&i| self.owner[i] == who as i8)
            .map(|i| SPACES[i].price + self.desks[i] as i32 * SPACES[i].price / 2)
            .sum();
        p.money + deeds
    }
    fn push_log(&mut self, s: String) {
        self.log.push(s);
        if self.log.len() > 4 {
            self.log.remove(0);
        }
        self.dirty = true;
    }
}

#[derive(Serialize, Deserialize)]
struct WState {
    t: String, // "st"
    pl: Vec<Player>,
    ow: Vec<i8>,
    dk: Vec<u8>,
    turn: u8,
    ph: u8,
    d: (u8, u8),
    rd: u32,
    log: String,
    win: i8,
}

fn send_state(b: &Board, line: &str) {
    if !b.net {
        return;
    }
    let w = WState {
        t: "st".into(),
        pl: b.players.clone(),
        ow: b.owner.to_vec(),
        dk: b.desks.to_vec(),
        turn: b.turn as u8,
        ph: b.tphase,
        d: b.dice,
        rd: b.round,
        log: line.to_string(),
        win: b.winner,
    };
    if let Ok(m) = serde_json::to_string(&w) {
        net_send(&m);
    }
}

#[derive(Component)]
struct OwnerMark(usize);

#[derive(Component)]
struct DeskText(usize);

#[derive(Component)]
struct Hud;

#[derive(Component)]
struct LogText;

pub struct FloorPlanPlugin;

impl Plugin for FloorPlanPlugin {
    fn build(&self, app: &mut App) {
        app.add_systems(OnEnter(Phase::Playing), setup).add_systems(
            Update,
            (net_apply, input, bots, paint, endgame)
                .chain()
                .run_if(in_state(Phase::Playing))
                .run_if(crate::unpaused),
        );
    }
}

fn setup(mut commands: Commands, net: Res<NetMode>) {
    let (players, my_idx, is_net) = match &net.0 {
        Some(cfg) => {
            let seats: Vec<usize> = cfg
                .present
                .iter()
                .enumerate()
                .filter(|(_, p)| **p)
                .map(|(i, _)| i)
                .collect();
            let my = seats.iter().position(|&s| s == cfg.seat as usize).unwrap_or(0);
            let pl: Vec<Player> = seats
                .iter()
                .map(|&s| Player {
                    seat: s,
                    human: true,
                    money: 1500,
                    pos: 0,
                    hr: 0,
                    alive: true,
                    doubles: 0,
                })
                .collect();
            (pl, my, true)
        }
        None => {
            let pl = (0..4)
                .map(|i| Player {
                    seat: i,
                    human: i == 0,
                    money: 1500,
                    pos: 0,
                    hr: 0,
                    alive: true,
                    doubles: 0,
                })
                .collect();
            (pl, 0, false)
        }
    };
    // The board ring.
    for (i, def) in SPACES.iter().enumerate() {
        let xy = space_xy(i);
        let color = match def.kind {
            Kind::Prop(g) => GROUP_COLORS[g as usize],
            Kind::Elev => Color::srgb(0.6, 0.6, 0.65),
            Kind::Util => Color::srgb(0.4, 0.55, 0.55),
            Kind::Hr | Kind::GoHr => RED.with_alpha(0.6),
            Kind::Start | Kind::Lounge => GREEN.with_alpha(0.6),
            _ => Color::srgb(0.25, 0.25, 0.3),
        };
        commands.spawn((
            Sprite { color, custom_size: Some(Vec2::splat(46.0)), ..default() },
            Transform::from_translation(xy.extend(1.0)),
            GameTag,
        ));
        let label = text(
            &mut commands,
            def.name,
            7.0,
            WHITE,
            (xy + inward(i) * 38.0).extend(2.0),
        );
        commands.entity(label).insert(GameTag);
        let mark = commands
            .spawn((
                Sprite {
                    color: Color::NONE,
                    custom_size: Some(Vec2::new(40.0, 5.0)),
                    ..default()
                },
                Transform::from_translation((xy - inward(i) * 20.0).extend(2.5)),
                OwnerMark(i),
                GameTag,
            ))
            .id();
        let _ = mark;
        let dt = text(&mut commands, "", 9.0, WHITE, (xy + Vec2::new(0.0, 14.0)).extend(3.0));
        commands.entity(dt).insert((DeskText(i), GameTag));
    }
    // Player tokens.
    let mut tokens = Vec::new();
    for (i, p) in players.iter().enumerate() {
        let off = Vec2::new((i % 3) as f32 * 12.0 - 12.0, (i / 3) as f32 * 12.0 - 6.0);
        let e = commands
            .spawn((
                Sprite {
                    color: PLAYER_COLORS[p.seat % PLAYER_COLORS.len()],
                    custom_size: Some(Vec2::splat(11.0)),
                    ..default()
                },
                Transform::from_translation((space_xy(0) + off).extend(4.0)),
                GameTag,
            ))
            .id();
        tokens.push(e);
    }
    let hud = text(&mut commands, "", 13.0, WHITE, Vec3::new(0.0, 110.0, 5.0));
    commands.entity(hud).insert((Hud, GameTag));
    let log = text(&mut commands, "", 11.0, CYAN, Vec3::new(0.0, -110.0, 5.0));
    commands.entity(log).insert((LogText, GameTag));
    commands.insert_resource(Board {
        players,
        owner: [-1; 40],
        desks: [0; 40],
        turn: 0,
        tphase: 0,
        dice: (0, 0),
        round: 1,
        my_idx,
        net: is_net,
        over: None,
        winner: -1,
        result: String::new(),
        log: vec!["A NEW FISCAL YEAR BEGINS.".into()],
        dirty: true,
        bot_t: Timer::from_seconds(0.8, TimerMode::Repeating),
        tokens,
    });
}

fn calc_rent(b: &Board, i: usize) -> i32 {
    let owner = b.owner[i];
    if owner < 0 {
        return 0;
    }
    let who = owner as usize;
    match SPACES[i].kind {
        Kind::Prop(g) => {
            let base = SPACES[i].price / 10;
            let set = if b.owns_group(who, g) { 2 } else { 1 };
            base * set * (1 + b.desks[i] as i32 * 3)
        }
        Kind::Elev => 25 * (1 << (b.count_kind(who, Kind::Elev).clamp(1, 4) - 1)),
        Kind::Util => {
            let roll = (b.dice.0 + b.dice.1) as i32;
            if b.count_kind(who, Kind::Util) >= 2 {
                roll * 10
            } else {
                roll * 4
            }
        }
        _ => 0,
    }
}

fn liquidate(b: &mut Board, who: usize, creditor: Option<usize>) {
    // Sell desks back at half price until solvent.
    for i in 0..40 {
        while b.players[who].money < 0 && b.owner[i] == who as i8 && b.desks[i] > 0 {
            b.desks[i] -= 1;
            b.players[who].money += SPACES[i].price / 4;
        }
    }
    if b.players[who].money < 0 {
        // Out of the game: deeds go to the creditor, or back to the bank.
        for i in 0..40 {
            if b.owner[i] == who as i8 {
                b.owner[i] = creditor.map(|c| c as i8).unwrap_or(-1);
                b.desks[i] = 0;
            }
        }
        b.players[who].alive = false;
        b.players[who].money = 0;
        let line = format!("P{} IS BANKRUPT.", b.players[who].seat + 1);
        b.push_log(line);
        sfx("boom");
    }
}

fn transfer(b: &mut Board, from: usize, to: Option<usize>, amount: i32) {
    b.players[from].money -= amount;
    if let Some(t) = to {
        b.players[t].money += amount;
    }
    if b.players[from].money < 0 {
        liquidate(b, from, to);
    }
}

fn send_to_hr(b: &mut Board) {
    let who = b.turn;
    b.players[who].pos = 10;
    b.players[who].hr = 3;
    b.players[who].doubles = 0;
    b.tphase = 2;
    let line = format!("P{} SENT TO HR REVIEW.", b.players[who].seat + 1);
    b.push_log(line);
    if who == b.my_idx {
        stat("hr_visits", 1);
    }
    sfx("buzz");
}

fn draw_card(b: &mut Board, rng: &mut Rng, rumor: bool) {
    let deck = if rumor { &RUMORS } else { &MEMOS };
    let (txt, card) = deck[rng.range(deck.len() as u32) as usize];
    let tag = if rumor { "RUMOR" } else { "MEMO" };
    b.push_log(format!("{tag}: {txt}"));
    match card {
        Card::Cash(v) => transfer(b, b.turn, None, -v),
        Card::Each(v) => {
            let rivals: Vec<usize> = (0..b.players.len())
                .filter(|&i| i != b.turn && b.players[i].alive)
                .collect();
            for r in rivals {
                transfer(b, b.turn, Some(r), -v);
                if b.players[b.turn].money < 0 || !b.players[b.turn].alive {
                    break;
                }
            }
        }
        Card::Move(dest) => {
            if dest <= b.players[b.turn].pos {
                b.players[b.turn].money += 200; // wraps past START
            }
            b.players[b.turn].pos = dest;
            land(b, rng);
        }
        Card::GoHr => send_to_hr(b),
    }
}

fn land(b: &mut Board, rng: &mut Rng) {
    let i = b.players[b.turn].pos;
    match SPACES[i].kind {
        Kind::Start | Kind::Lounge | Kind::Hr => {}
        Kind::GoHr => {
            send_to_hr(b);
            return;
        }
        Kind::Tax(a) => {
            transfer(b, b.turn, None, a);
            b.push_log(format!("P{} PAYS {a} IN FEES.", b.cur().seat + 1));
        }
        Kind::Memo => draw_card(b, rng, false),
        Kind::Rumor => draw_card(b, rng, true),
        Kind::Prop(_) | Kind::Elev | Kind::Util => {
            let owner = b.owner[i];
            if owner < 0 {
                b.tphase = 1;
                b.dirty = true;
                return;
            }
            let who = owner as usize;
            if who != b.turn && b.players[who].alive {
                let rent = calc_rent(b, i);
                transfer(b, b.turn, Some(who), rent);
                let line = format!(
                    "P{} PAYS {rent} RENT AT {}.",
                    b.players[b.turn].seat + 1,
                    SPACES[i].name
                );
                b.push_log(line);
                if b.turn == b.my_idx {
                    stat("rent_paid", 1);
                } else if who == b.my_idx {
                    stat("rent_collected", 1);
                }
                sfx("chip");
            }
        }
    }
    if b.tphase != 1 {
        b.tphase = 2;
    }
    b.dirty = true;
}

fn move_by(b: &mut Board, rng: &mut Rng, steps: usize) {
    let old = b.players[b.turn].pos;
    let next = (old + steps) % 40;
    if next < old {
        b.players[b.turn].money += 200;
        if b.turn == b.my_idx {
            stat("laps_completed", 1);
        }
    }
    b.players[b.turn].pos = next;
    land(b, rng);
}

fn roll_dice(b: &mut Board, rng: &mut Rng) {
    let d = (1 + rng.range(6) as u8, 1 + rng.range(6) as u8);
    b.dice = d;
    sfx("drop");
    let who = b.turn;
    if b.players[who].hr > 0 {
        if d.0 == d.1 {
            b.players[who].hr = 0;
            b.push_log(format!("P{} ROLLS DOUBLES OUT OF HR.", b.players[who].seat + 1));
            move_by(b, rng, (d.0 + d.1) as usize);
        } else {
            b.players[who].hr -= 1;
            if b.players[who].hr == 0 {
                transfer(b, who, None, 50);
                b.push_log(format!("P{} PAYS 50 TO LEAVE HR.", b.players[who].seat + 1));
                if b.players[who].alive {
                    move_by(b, rng, (d.0 + d.1) as usize);
                }
            } else {
                b.push_log(format!("P{} STAYS IN HR REVIEW.", b.players[who].seat + 1));
                b.tphase = 2;
            }
        }
        b.players[who].doubles = 0;
        b.dirty = true;
        return;
    }
    if d.0 == d.1 {
        b.players[who].doubles += 1;
        if b.players[who].doubles >= 3 {
            send_to_hr(b);
            return;
        }
    } else {
        b.players[who].doubles = 0;
    }
    move_by(b, rng, (d.0 + d.1) as usize);
}

fn end_turn(b: &mut Board) {
    let who = b.turn;
    if b.players[who].alive && b.players[who].doubles > 0 && b.players[who].hr == 0 {
        // Doubles earn another roll.
        b.tphase = 0;
        b.dirty = true;
        return;
    }
    b.players[who].doubles = 0;
    for _ in 0..b.players.len() {
        b.turn = (b.turn + 1) % b.players.len();
        if b.turn == 0 {
            b.round += 1;
        }
        if b.players[b.turn].alive {
            break;
        }
    }
    b.tphase = 0;
    b.dirty = true;
    check_over(b);
}

fn check_over(b: &mut Board) {
    if b.over.is_some() {
        return;
    }
    let alive: Vec<usize> = (0..b.players.len()).filter(|&i| b.players[i].alive).collect();
    let winner = if alive.len() == 1 {
        Some(alive[0])
    } else if b.round > ROUND_CAP {
        alive.iter().copied().max_by_key(|&i| b.worth(i))
    } else {
        None
    };
    if let Some(w) = winner {
        b.winner = w as i8;
        finish(b);
    }
}

fn finish(b: &mut Board) {
    let w = b.winner as usize;
    b.result = if w == b.my_idx {
        format!("THE FLOOR IS YOURS. WORTH {}.", b.worth(w))
    } else {
        format!("P{} TAKES THE FLOOR.", b.players[w].seat + 1)
    };
    if w == b.my_idx {
        stat("floors_owned", 1);
    }
    b.over = Some(Timer::from_seconds(3.0, TimerMode::Once));
    b.dirty = true;
    sfx(if w == b.my_idx { "win" } else { "over" });
}

fn try_build(b: &mut Board, i: usize) -> bool {
    let who = b.turn;
    let Kind::Prop(g) = SPACES[i].kind else { return false };
    if b.owner[i] != who as i8 || !b.owns_group(who, g) || b.desks[i] >= 4 {
        return false;
    }
    let cost = SPACES[i].price / 2;
    if b.players[who].money < cost {
        return false;
    }
    b.players[who].money -= cost;
    b.desks[i] += 1;
    b.push_log(format!("P{} BUILDS A DESK AT {}.", b.players[who].seat + 1, SPACES[i].name));
    if who == b.my_idx {
        stat("desks_built", 1);
    }
    sfx("place");
    true
}

fn buy_current(b: &mut Board) -> bool {
    let i = b.players[b.turn].pos;
    let price = SPACES[i].price;
    if b.owner[i] >= 0 || b.players[b.turn].money < price {
        return false;
    }
    b.players[b.turn].money -= price;
    b.owner[i] = b.turn as i8;
    b.push_log(format!("P{} BUYS {} FOR {price}.", b.cur().seat + 1, SPACES[i].name));
    if b.turn == b.my_idx {
        stat("deeds_bought", 1);
    }
    b.tphase = 2;
    b.dirty = true;
    sfx("chip");
    true
}

#[allow(clippy::too_many_arguments)]
fn input(
    keys: Res<ButtonInput<KeyCode>>,
    buttons: Res<ButtonInput<MouseButton>>,
    windows: Query<&Window>,
    cameras: Query<(&Camera, &GlobalTransform)>,
    mut rng: ResMut<Rng>,
    mut b: ResMut<Board>,
) {
    if !b.me_acting() || !b.cur().human {
        return;
    }
    let line_hint;
    match b.tphase {
        0 => {
            if b.cur().hr > 0 && keys.just_pressed(KeyCode::KeyP) && b.cur().money >= 50 {
                let who = b.turn;
                transfer(&mut b, who, None, 50);
                b.players[who].hr = 0;
                b.push_log("PAID 50. HR LETS YOU GO.".into());
                roll_dice(&mut b, &mut rng);
                send_state(&b, b.log.last().cloned().unwrap_or_default().as_str());
                return;
            }
            if keys.just_pressed(KeyCode::KeyR) || keys.just_pressed(KeyCode::Space) {
                roll_dice(&mut b, &mut rng);
                check_over(&mut b);
                line_hint = b.log.last().cloned().unwrap_or_default();
                send_state(&b, &line_hint);
                return;
            }
        }
        1 => {
            if keys.just_pressed(KeyCode::KeyB) && buy_current(&mut b) {
                send_state(&b, b.log.last().cloned().unwrap_or_default().as_str());
                return;
            }
            if keys.just_pressed(KeyCode::KeyN) {
                b.tphase = 2;
                b.dirty = true;
                send_state(&b, "PASSED ON THE DEED.");
                return;
            }
        }
        _ => {
            if keys.just_pressed(KeyCode::KeyE) || keys.just_pressed(KeyCode::Space) {
                end_turn(&mut b);
                send_state(&b, b.log.last().cloned().unwrap_or_default().as_str());
                return;
            }
        }
    }
    // Click a wing you own to build a desk (any point during your turn).
    if buttons.just_pressed(MouseButton::Left) && b.tphase != 1 {
        let Ok(window) = windows.single() else { return };
        let Ok((camera, cam_tf)) = cameras.single() else { return };
        let Some(world) = cursor_world(window, camera, cam_tf) else { return };
        for i in 0..40 {
            if (world - space_xy(i)).length() < 26.0 {
                if try_build(&mut b, i) {
                    send_state(&b, b.log.last().cloned().unwrap_or_default().as_str());
                }
                return;
            }
        }
    }
}

fn bots(time: Res<Time>, mut rng: ResMut<Rng>, mut b: ResMut<Board>) {
    if b.net || b.over.is_some() || b.cur().human || !b.cur().alive {
        return;
    }
    if !b.bot_t.tick(time.delta()).finished() {
        return;
    }
    match b.tphase {
        0 => {
            // Build one desk if a full wing allows it and cash is deep.
            let who = b.turn;
            if b.players[who].money > 400 {
                for i in 0..40 {
                    if b.owner[i] == who as i8 && b.desks[i] < 4 {
                        if let Kind::Prop(g) = SPACES[i].kind {
                            if b.owns_group(who, g) && try_build(&mut b, i) {
                                return;
                            }
                        }
                    }
                }
            }
            if b.cur().hr > 0 && b.cur().money > 200 {
                transfer(&mut b, who, None, 50);
                b.players[who].hr = 0;
            }
            roll_dice(&mut b, &mut rng);
            check_over(&mut b);
        }
        1 => {
            let i = b.cur().pos;
            if b.cur().money > SPACES[i].price + 150 {
                buy_current(&mut b);
            } else {
                b.tphase = 2;
                b.dirty = true;
            }
        }
        _ => end_turn(&mut b),
    }
}

fn net_apply(mut events: EventReader<NetIn>, net: Res<NetMode>, mut b: ResMut<Board>) {
    let Some(cfg) = &net.0 else {
        events.clear();
        return;
    };
    for ev in events.read() {
        if ev.left {
            if let Some(idx) = b.players.iter().position(|p| p.seat == ev.seat as usize) {
                if b.players[idx].alive {
                    b.players[idx].alive = false;
                    for i in 0..40 {
                        if b.owner[i] == idx as i8 {
                            b.owner[i] = -1;
                            b.desks[i] = 0;
                        }
                    }
                    let line = format!("P{} LEFT THE BUILDING.", ev.seat + 1);
                    b.push_log(line);
                    if b.turn == idx {
                        b.tphase = 2;
                        end_turn(&mut b);
                    }
                    check_over(&mut b);
                }
            }
            continue;
        }
        if ev.seat == cfg.seat {
            continue;
        }
        let Ok(w) = serde_json::from_str::<WState>(&ev.data) else { continue };
        if w.t != "st" {
            continue;
        }
        // Only the acting seat's snapshots count.
        if b.players.get(b.turn).map(|p| p.seat) != Some(ev.seat as usize)
            && b.players.get(w.turn as usize).map(|p| p.seat) != Some(ev.seat as usize)
        {
            continue;
        }
        if w.pl.len() == b.players.len() {
            b.players = w.pl;
            for i in 0..40 {
                b.owner[i] = w.ow.get(i).copied().unwrap_or(-1);
                b.desks[i] = w.dk.get(i).copied().unwrap_or(0);
            }
            b.turn = (w.turn as usize).min(b.players.len() - 1);
            b.tphase = w.ph;
            b.dice = w.d;
            b.round = w.rd;
            if !w.log.is_empty() {
                b.push_log(w.log.clone());
            }
            if w.win >= 0 && b.over.is_none() {
                b.winner = w.win;
                finish(&mut b);
            }
            b.dirty = true;
        }
    }
}

#[allow(clippy::type_complexity)]
fn paint(
    mut b: ResMut<Board>,
    mut marks: Query<(&OwnerMark, &mut Sprite)>,
    mut desks: Query<(&DeskText, &mut Text2d), (Without<Hud>, Without<LogText>)>,
    mut hud: Query<&mut Text2d, (With<Hud>, Without<LogText>, Without<DeskText>)>,
    mut logq: Query<&mut Text2d, (With<LogText>, Without<Hud>, Without<DeskText>)>,
    mut tfs: Query<&mut Transform>,
) {
    if !b.dirty {
        return;
    }
    b.dirty = false;
    for (m, mut s) in &mut marks {
        s.color = if b.owner[m.0] >= 0 {
            let idx = b.owner[m.0] as usize;
            PLAYER_COLORS[b.players[idx].seat % PLAYER_COLORS.len()]
        } else {
            Color::NONE
        };
    }
    for (d, mut t) in &mut desks {
        let n = b.desks[d.0];
        let s = if n > 0 { format!("D{n}") } else { String::new() };
        if t.0 != s {
            t.0 = s;
        }
    }
    for (i, &e) in b.tokens.iter().enumerate() {
        if let Ok(mut tf) = tfs.get_mut(e) {
            if !b.players[i].alive {
                tf.translation = Vec3::new(9999.0, 9999.0, 4.0);
            } else {
                let off =
                    Vec2::new((i % 3) as f32 * 12.0 - 12.0, (i / 3) as f32 * 12.0 - 6.0);
                tf.translation = (space_xy(b.players[i].pos) + off).extend(4.0);
            }
        }
    }
    if let Ok(mut h) = hud.single_mut() {
        let s = if b.over.is_some() {
            b.result.clone()
        } else {
            let cur = b.cur();
            let space = &SPACES[cur.pos];
            let whose = if b.me_acting() && cur.human {
                "YOUR TURN".to_string()
            } else {
                format!("P{}", cur.seat + 1)
            };
            let prompt = if !b.me_acting() || !cur.human {
                String::new()
            } else if b.tphase == 1 {
                format!("  BUY {} FOR {}? B/N", space.name, space.price)
            } else if b.tphase == 2 {
                "  E ENDS TURN".to_string()
            } else if cur.hr > 0 {
                "  HR: P PAY 50 / R ROLL".to_string()
            } else {
                "  R ROLLS".to_string()
            };
            let money: Vec<String> = b
                .players
                .iter()
                .filter(|p| p.alive)
                .map(|p| format!("P{}:{}", p.seat + 1, p.money))
                .collect();
            format!(
                "{whose} AT {}{prompt}\nDICE {} {}   YEAR {}/{}\n{}",
                space.name,
                b.dice.0,
                b.dice.1,
                b.round,
                ROUND_CAP,
                money.join("  ")
            )
        };
        if h.0 != s {
            h.0 = s;
        }
    }
    if let Ok(mut l) = logq.single_mut() {
        let s = b.log.join("\n");
        if l.0 != s {
            l.0 = s;
        }
    }
    let _ = (AMBER, DIM);
}

fn endgame(
    time: Res<Time>,
    mut b: ResMut<Board>,
    mut final_score: ResMut<FinalScore>,
    mut next: ResMut<NextState<Phase>>,
) {
    if let Some(timer) = b.over.as_mut() {
        if timer.tick(time.delta()).finished() {
            let my = b.my_idx;
            final_score.0 = b.worth(my).max(0) as u32;
            next.set(Phase::GameOver);
        }
    }
}
