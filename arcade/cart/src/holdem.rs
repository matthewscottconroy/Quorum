//! HOLD 'EM — tournament Texas hold 'em against the house bots. The game
//! is public domain and this is an original table: our own cards, our own
//! chips (play tokens, never money), our own opponents. Blinds climb, the
//! short stacks bust, the last stack standing keeps the table.
//!
//! Local only by design: hole cards are secrets, and the arcade's room
//! relay broadcasts to every seat — an honest online table would need a
//! dealer the relay deliberately isn't.

use arcade_logic::poker::{
    best_hand, deck, hand_name, Card, HandRank, FLUSH, FULL_HOUSE, QUADS, STRAIGHT, STRAIGHT_FLUSH,
};
use bevy::prelude::*;

use crate::retro::{text, AMBER, CYAN, DIM, MAGENTA, RED, WHITE};
use crate::rng::Rng;
use crate::shell::{sfx, stat};
use crate::{CabinetConfig, FinalScore, GameTag, Phase};

pub const BLURB: &[&str] = &[
    "TOURNAMENT TABLES. FICTIONAL CHIPS. REAL GRUDGES.",
    "F FOLDS / C CHECKS OR CALLS / R RAISES / A SHOVES",
    "BLINDS CLIMB. LAST STACK KEEPS THE TABLE.",
];

const START_CHIPS: u32 = 1000;
const START_BB: u32 = 20;
const BLINDS_UP_EVERY: u32 = 8;
const MAX_BB: u32 = 320;
const MAX_HANDS: u32 = 48;
const MAX_RAISES_PER_STREET: u8 = 3;

#[derive(Clone, Copy, PartialEq, Eq)]
enum Stage {
    NewHand,
    Betting,
    AdvanceStreet,
    Showdown,
    HandOver,
}

#[derive(Clone)]
struct Seat {
    chips: u32,
    hole: [Card; 2],
    folded: bool,
    out: bool, // busted from the tournament
    committed_street: u32,
    committed_hand: u32,
    acted: bool,
    shown: bool, // hole cards face up (showdown)
}

#[derive(Resource)]
struct Table {
    seats: Vec<Seat>,
    deck: Vec<Card>,
    board: Vec<Card>,
    dealer: usize,
    turn: usize,
    bet: u32,
    min_raise: u32,
    raises_this_street: u8,
    stage: Stage,
    wait: Timer,
    bot_clock: Timer,
    msg: String,
    showdown_lines: Vec<String>,
    hand_no: u32,
    bb: u32,
    over: bool,
    over_wait: Timer,
    result: String,
    dirty: bool,
}

impl Table {
    fn pot(&self) -> u32 {
        self.seats.iter().map(|s| s.committed_hand).sum()
    }
    fn in_hand(&self) -> Vec<usize> {
        (0..self.seats.len()).filter(|&i| !self.seats[i].out && !self.seats[i].folded).collect()
    }
    /// Seats still able to make a decision (not folded, not all-in).
    fn can_act(&self) -> Vec<usize> {
        self.in_hand().into_iter().filter(|&i| self.seats[i].chips > 0).collect()
    }
    fn commit(&mut self, i: usize, amount: u32) {
        let amount = amount.min(self.seats[i].chips);
        self.seats[i].chips -= amount;
        self.seats[i].committed_street += amount;
        self.seats[i].committed_hand += amount;
    }
    fn next_from(&self, from: usize, skip_unactionable: bool) -> usize {
        let n = self.seats.len();
        let mut i = (from + 1) % n;
        for _ in 0..n {
            let s = &self.seats[i];
            let live = !s.out && !s.folded && (!skip_unactionable || s.chips > 0);
            if live {
                return i;
            }
            i = (i + 1) % n;
        }
        from
    }
}

#[derive(Component)]
struct CardSprite;

#[derive(Component)]
struct SeatText(usize);

#[derive(Component)]
struct PotText;

#[derive(Component)]
struct MsgText;

#[derive(Component)]
struct HintText;

pub struct HoldemPlugin;

impl Plugin for HoldemPlugin {
    fn build(&self, app: &mut App) {
        app.add_systems(OnEnter(Phase::Playing), setup).add_systems(
            Update,
            (table_run, paint, texts)
                .chain()
                .run_if(in_state(Phase::Playing))
                .run_if(crate::unpaused),
        );
    }
}

/// Seat layout: YOU at the bottom, the bots arced across the top.
fn seat_pos(i: usize) -> Vec2 {
    match i {
        0 => Vec2::new(0.0, -218.0),
        1 => Vec2::new(-272.0, 120.0),
        2 => Vec2::new(-140.0, 208.0),
        3 => Vec2::new(0.0, 240.0),
        4 => Vec2::new(140.0, 208.0),
        _ => Vec2::new(272.0, 120.0),
    }
}

const BOT_NAMES: [&str; 5] = ["MARGE", "SLIM", "DOC", "TILLY", "BRICK"];

fn setup(mut commands: Commands, config: Res<CabinetConfig>) {
    let players = (config.players.clamp(2, 6)) as usize;
    let seats = vec![
        Seat {
            chips: START_CHIPS,
            hole: [Card { rank: 2, suit: 0 }; 2],
            folded: true,
            out: false,
            committed_street: 0,
            committed_hand: 0,
            acted: false,
            shown: false,
        };
        players
    ];
    commands.insert_resource(Table {
        seats,
        deck: Vec::new(),
        board: Vec::new(),
        dealer: 0,
        turn: 0,
        bet: 0,
        min_raise: START_BB,
        raises_this_street: 0,
        stage: Stage::NewHand,
        wait: Timer::from_seconds(0.6, TimerMode::Once),
        bot_clock: Timer::from_seconds(0.9, TimerMode::Repeating),
        msg: "SHUFFLING UP...".into(),
        showdown_lines: Vec::new(),
        hand_no: 0,
        bb: START_BB,
        over: false,
        over_wait: Timer::from_seconds(3.0, TimerMode::Once),
        result: String::new(),
        dirty: true,
    });

    // Static furniture: seat panels, pot, ticker, controls, felt line.
    for i in 0..players {
        let p = seat_pos(i);
        let e = text(&mut commands, "", 15.0, WHITE, Vec3::new(p.x, p.y, 5.0));
        commands.entity(e).insert((SeatText(i), GameTag));
    }
    let pot = text(&mut commands, "", 22.0, AMBER, Vec3::new(0.0, 66.0, 5.0));
    commands.entity(pot).insert((PotText, GameTag));
    let msg = text(&mut commands, "", 16.0, WHITE, Vec3::new(0.0, -120.0, 5.0));
    commands.entity(msg).insert((MsgText, GameTag));
    let hint = text(&mut commands, "", 15.0, DIM, Vec3::new(0.0, -286.0, 5.0));
    commands.entity(hint).insert((HintText, GameTag));
    let controls = text(
        &mut commands,
        "F FOLD   C CHECK/CALL   R RAISE   A ALL-IN",
        15.0,
        DIM,
        Vec3::new(0.0, -308.0, 5.0),
    );
    commands.entity(controls).insert(GameTag);
}

/// Pip colors chosen for the LIGHT card face: two dark "black" suits, two
/// reds — readable, and unmistakably ours.
fn suit_color(suit: u8) -> Color {
    match suit {
        0 => Color::srgb(0.09, 0.09, 0.16), // spades: near-black
        1 => Color::srgb(0.80, 0.13, 0.16), // hearts: red
        2 => Color::srgb(0.72, 0.14, 0.45), // diamonds: deep magenta
        _ => Color::srgb(0.05, 0.32, 0.22), // clubs: bottle green
    }
}

fn card_label(c: Card) -> String {
    let r = match c.rank {
        14 => "A".into(),
        13 => "K".into(),
        12 => "Q".into(),
        11 => "J".into(),
        n => n.to_string(),
    };
    let s = ["S", "H", "D", "C"][c.suit as usize % 4];
    format!("{r}{s}")
}

/// Shuffle with the cabinet's own PRNG.
fn shuffled_deck(rng: &mut Rng) -> Vec<Card> {
    let mut d = deck();
    for i in (1..d.len()).rev() {
        let j = rng.range(i as u32 + 1) as usize;
        d.swap(i, j);
    }
    d
}

fn start_hand(table: &mut Table, rng: &mut Rng) {
    table.hand_no += 1;
    stat("hands_played", 1);
    table.bb = (START_BB << ((table.hand_no - 1) / BLINDS_UP_EVERY).min(4)).min(MAX_BB);
    table.deck = shuffled_deck(rng);
    table.board.clear();
    table.showdown_lines.clear();
    for s in table.seats.iter_mut() {
        s.folded = s.out;
        s.committed_street = 0;
        s.committed_hand = 0;
        s.acted = false;
        s.shown = false;
        if !s.out {
            s.hole = [table.deck.pop().expect("deck"), table.deck.pop().expect("deck")];
        }
    }
    // The button walks; blinds post; action starts left of the big blind.
    table.dealer = table.next_from(table.dealer, false);
    let sb = table.next_from(table.dealer, false);
    let bb_seat = table.next_from(sb, false);
    let (small, big) = (table.bb / 2, table.bb);
    table.commit(sb, small);
    table.commit(bb_seat, big);
    table.bet = big;
    table.min_raise = big;
    table.raises_this_street = 0;
    table.turn = table.next_from(bb_seat, true);
    table.stage = Stage::Betting;
    table.msg = format!("HAND {} - BLINDS {}/{}", table.hand_no, small, big);
    table.dirty = true;
    sfx("coin");
}

/// One betting action. `raise_to` of None = call/check, Some(0) = fold.
fn act(table: &mut Table, seat: usize, action: BetAction) {
    let name = seat_name(seat);
    match action {
        BetAction::Fold => {
            table.seats[seat].folded = true;
            table.msg = format!("{name} FOLDS");
            sfx("tick");
        }
        BetAction::CheckCall => {
            let need = table.bet.saturating_sub(table.seats[seat].committed_street);
            if need == 0 {
                table.msg = format!("{name} CHECKS");
            } else {
                let paid = need.min(table.seats[seat].chips);
                table.commit(seat, paid);
                table.msg = if table.seats[seat].chips == 0 && paid < need {
                    format!("{name} CALLS ALL-IN {paid}")
                } else {
                    format!("{name} CALLS {paid}")
                };
            }
            sfx("place");
        }
        BetAction::Raise(raise_by) => {
            let old_bet = table.bet;
            let target = old_bet + raise_by.max(table.min_raise);
            let need = target.saturating_sub(table.seats[seat].committed_street);
            let paid = need.min(table.seats[seat].chips);
            table.commit(seat, paid);
            let new_committed = table.seats[seat].committed_street;
            if new_committed > old_bet {
                table.bet = new_committed;
                table.min_raise = (new_committed - old_bet).max(table.bb);
                table.raises_this_street += 1;
                for (i, s) in table.seats.iter_mut().enumerate() {
                    if i != seat {
                        s.acted = false;
                    }
                }
                table.msg = if table.seats[seat].chips == 0 {
                    format!("{name} SHOVES {new_committed}")
                } else {
                    format!("{name} RAISES TO {new_committed}")
                };
                sfx("capture");
            } else {
                table.msg = format!("{name} CALLS ALL-IN"); // couldn't cover a raise
                sfx("place");
            }
        }
    }
    table.seats[seat].acted = true;
    table.dirty = true;
}

enum BetAction {
    Fold,
    CheckCall,
    Raise(u32),
}

fn seat_name(i: usize) -> String {
    if i == 0 {
        "YOU".into()
    } else {
        BOT_NAMES[(i - 1) % BOT_NAMES.len()].into()
    }
}

/// Crude but honest hand strength in 0..1 for the bots.
fn bot_strength(seat: &Seat, board: &[Card]) -> f32 {
    if board.is_empty() {
        let [a, b] = seat.hole;
        let (hi, lo) = (a.rank.max(b.rank) as f32, a.rank.min(b.rank) as f32);
        let mut s = (hi + lo) / 56.0 + 0.08;
        if a.rank == b.rank {
            s = 0.5 + hi / 40.0; // pairs play themselves
        }
        if a.suit == b.suit {
            s += 0.04;
        }
        if hi - lo <= 2.0 {
            s += 0.03; // connectors
        }
        s.min(0.95)
    } else {
        let mut cards = board.to_vec();
        cards.push(seat.hole[0]);
        cards.push(seat.hole[1]);
        let r = best_hand(&cards);
        let base = match r.cat {
            0 => 0.18 + r.tie[0] as f32 / 120.0,
            1 => 0.42 + r.tie[0] as f32 / 140.0,
            2 => 0.60,
            3 => 0.72,
            4 => 0.80,
            5 => 0.86,
            6 => 0.92,
            _ => 0.98,
        };
        base
    }
}

fn bot_decide(table: &Table, seat: usize, rng: &mut Rng) -> BetAction {
    let s = &table.seats[seat];
    let strength = bot_strength(s, &table.board) + rng.between(-0.08, 0.08);
    let need = table.bet.saturating_sub(s.committed_street);
    let pot = table.pot().max(1);
    let can_raise = table.raises_this_street < MAX_RAISES_PER_STREET;
    if need == 0 {
        if can_raise && (strength > 0.68 || rng.chance(0.07)) {
            return BetAction::Raise(pot.min(s.chips).max(table.bb));
        }
        return BetAction::CheckCall;
    }
    let odds = need as f32 / (pot + need) as f32;
    if strength > 0.8 && can_raise {
        return BetAction::Raise(pot.min(s.chips));
    }
    if strength > odds + 0.12 || (need <= table.bb && strength > 0.3) || rng.chance(0.05) {
        // Calling off the whole stack wants a real hand.
        if need >= s.chips && strength < 0.62 {
            return BetAction::Fold;
        }
        return BetAction::CheckCall;
    }
    BetAction::Fold
}

/// Awards the pot(s) at showdown, side pots handled properly: each all-in
/// contribution level forms its own pot, won by the best hand among those
/// who covered it.
fn showdown(table: &mut Table) {
    let contenders = table.in_hand();
    for &i in &contenders {
        table.seats[i].shown = true;
    }
    // Best hand per contender.
    let rank_of = |t: &Table, i: usize| -> HandRank {
        let mut cards = t.board.clone();
        cards.push(t.seats[i].hole[0]);
        cards.push(t.seats[i].hole[1]);
        best_hand(&cards)
    };
    // Contribution levels ascending (every distinct all-in cap plus the max).
    let mut levels: Vec<u32> =
        contenders.iter().map(|&i| table.seats[i].committed_hand).collect();
    levels.sort_unstable();
    levels.dedup();
    let mut prev_level = 0u32;
    let mut awarded_total: Vec<u32> = vec![0; table.seats.len()];
    for &level in &levels {
        if level == prev_level {
            continue;
        }
        // This slice collects from EVERY seat (folded chips too).
        let mut slice = 0u32;
        for s in &table.seats {
            slice += s.committed_hand.clamp(prev_level, level) - prev_level.min(s.committed_hand);
        }
        let eligible: Vec<usize> = contenders
            .iter()
            .copied()
            .filter(|&i| table.seats[i].committed_hand >= level)
            .collect();
        if slice == 0 || eligible.is_empty() {
            prev_level = level;
            continue;
        }
        let best = eligible.iter().map(|&i| rank_of(table, i)).max().expect("non-empty");
        let winners: Vec<usize> =
            eligible.iter().copied().filter(|&i| rank_of(table, i) == best).collect();
        let share = slice / winners.len() as u32;
        let mut leftover = slice - share * winners.len() as u32;
        for &w in &winners {
            let extra = if leftover > 0 { 1 } else { 0 };
            leftover = leftover.saturating_sub(1);
            table.seats[w].chips += share + extra;
            awarded_total[w] += share + extra;
        }
        prev_level = level;
    }
    // The story of the showdown.
    for &i in &contenders {
        let r = rank_of(table, i);
        let won = awarded_total[i] > 0;
        table.showdown_lines.push(format!(
            "{}: {}{}",
            seat_name(i),
            hand_name(r.cat, r.tie[0]),
            if won { format!(" - WINS {}", awarded_total[i]) } else { String::new() }
        ));
        if i == 0 && won {
            stat("hands_won", 1);
            stat("chips_won", awarded_total[0] as u64);
            match r.cat {
                STRAIGHT_FLUSH if r.tie[0] == 14 => stat("royal_flushes", 1),
                STRAIGHT_FLUSH => stat("straight_flushes", 1),
                QUADS => stat("quads_made", 1),
                FULL_HOUSE => stat("full_houses", 1),
                FLUSH => stat("flushes_shown", 1),
                STRAIGHT => stat("straights_shown", 1),
                _ => {}
            }
        }
    }
    // Zero the hand's committed chips (they've been paid out).
    for s in table.seats.iter_mut() {
        s.committed_hand = 0;
        s.committed_street = 0;
    }
    table.msg = "SHOWDOWN".into();
    table.dirty = true;
    sfx("clear");
}

#[allow(clippy::too_many_arguments)]
fn table_run(
    time: Res<Time>,
    keys: Res<ButtonInput<KeyCode>>,
    mut rng: ResMut<Rng>,
    mut table: ResMut<Table>,
    mut final_score: ResMut<FinalScore>,
    mut next: ResMut<NextState<Phase>>,
) {
    if table.over {
        if table.over_wait.tick(time.delta()).finished() {
            final_score.0 = table.seats[0].chips;
            next.set(Phase::GameOver);
        }
        return;
    }
    if !table.wait.tick(time.delta()).finished() {
        return;
    }

    match table.stage {
        Stage::NewHand => {
            // Bust-outs leave the table between hands.
            for i in 0..table.seats.len() {
                if table.seats[i].chips == 0 && !table.seats[i].out {
                    table.seats[i].out = true;
                    if i == 0 {
                        stat("bust_outs", 1);
                    }
                }
            }
            let alive: Vec<usize> =
                (0..table.seats.len()).filter(|&i| !table.seats[i].out).collect();
            if table.seats[0].out {
                table.over = true;
                table.result = "BUSTED OUT".into();
                table.msg = "BUSTED. THE HOUSE THANKS YOU.".into();
                table.dirty = true;
                return;
            }
            if alive.len() == 1 || table.hand_no >= MAX_HANDS {
                table.over = true;
                if alive.len() == 1 {
                    stat("tables_swept", 1);
                    table.msg = "THE TABLE IS YOURS.".into();
                } else {
                    table.msg = "CLOSING TIME. CHIPS COUNT.".into();
                }
                table.dirty = true;
                return;
            }
            start_hand(&mut table, &mut rng);
            table.wait = Timer::from_seconds(0.7, TimerMode::Once);
        }
        Stage::Betting => {
            // A walkover ends the hand without a showdown.
            let in_hand = table.in_hand();
            if in_hand.len() == 1 {
                let w = in_hand[0];
                let pot = table.pot();
                table.seats[w].chips += pot;
                if w == 0 {
                    stat("hands_won", 1);
                    stat("chips_won", pot as u64);
                }
                for s in table.seats.iter_mut() {
                    s.committed_hand = 0;
                    s.committed_street = 0;
                }
                table.msg = format!("{} TAKES {} UNCONTESTED", seat_name(w), pot);
                table.stage = Stage::NewHand;
                table.wait = Timer::from_seconds(2.0, TimerMode::Once);
                table.dirty = true;
                return;
            }
            // Betting closed? Everyone able to act has matched the bet.
            let bet = table.bet;
            let open = table
                .can_act()
                .iter()
                .any(|&i| !table.seats[i].acted || table.seats[i].committed_street < bet);
            if !open {
                // Everyone able to act has matched: on to the next street.
                // (A lone player facing an all-in still counts as open until
                // they call or fold — nobody's decision gets skipped.)
                table.stage = Stage::AdvanceStreet;
                table.wait = Timer::from_seconds(0.5, TimerMode::Once);
                return;
            }
            // Current actor.
            let i = table.turn;
            if table.seats[i].out
                || table.seats[i].folded
                || table.seats[i].chips == 0
                || (table.seats[i].acted && table.seats[i].committed_street >= bet)
            {
                table.turn = table.next_from(i, true);
                return;
            }
            if i == 0 {
                // The human acts on keys.
                let action = if keys.just_pressed(KeyCode::KeyF) {
                    stat("folds", 1);
                    Some(BetAction::Fold)
                } else if keys.just_pressed(KeyCode::KeyC) {
                    Some(BetAction::CheckCall)
                } else if keys.just_pressed(KeyCode::KeyR) {
                    stat("raises", 1);
                    Some(BetAction::Raise(table.pot().max(table.bb)))
                } else if keys.just_pressed(KeyCode::KeyA) {
                    stat("all_ins", 1);
                    let chips = table.seats[0].chips;
                    Some(BetAction::Raise(chips))
                } else {
                    None
                };
                if let Some(a) = action {
                    act(&mut table, 0, a);
                    table.turn = table.next_from(0, true);
                }
            } else if table.bot_clock.tick(time.delta()).just_finished() {
                let a = bot_decide(&table, i, &mut rng);
                act(&mut table, i, a);
                table.turn = table.next_from(i, true);
            }
        }
        Stage::AdvanceStreet => {
            for s in table.seats.iter_mut() {
                s.committed_street = 0;
                s.acted = false;
            }
            table.bet = 0;
            table.min_raise = table.bb;
            table.raises_this_street = 0;
            match table.board.len() {
                0 => {
                    for _ in 0..3 {
                        let c = table.deck.pop().expect("deck");
                        table.board.push(c);
                    }
                    table.msg = "THE FLOP".into();
                }
                3 | 4 => {
                    let c = table.deck.pop().expect("deck");
                    table.board.push(c);
                    table.msg = if table.board.len() == 4 { "THE TURN".into() } else { "THE RIVER".into() };
                }
                _ => {
                    table.stage = Stage::Showdown;
                    table.wait = Timer::from_seconds(0.4, TimerMode::Once);
                    return;
                }
            }
            sfx("drop");
            table.dirty = true;
            // With fewer than two live actors, keep running the board out.
            table.stage = Stage::Betting;
            table.turn = table.next_from(table.dealer, true);
            table.wait = Timer::from_seconds(0.8, TimerMode::Once);
        }
        Stage::Showdown => {
            showdown(&mut table);
            table.stage = Stage::HandOver;
            table.wait = Timer::from_seconds(3.4, TimerMode::Once);
        }
        Stage::HandOver => {
            table.stage = Stage::NewHand;
            table.wait = Timer::from_seconds(0.3, TimerMode::Once);
        }
    }
}

/// Card sprites: repaint-on-dirty, chess style.
#[allow(clippy::type_complexity)]
fn paint(
    mut commands: Commands,
    mut table: ResMut<Table>,
    cards: Query<Entity, With<CardSprite>>,
) {
    if !table.dirty {
        return;
    }
    table.dirty = false;
    for e in &cards {
        commands.entity(e).despawn();
    }
    let spawn_card = |commands: &mut Commands, pos: Vec2, card: Option<Card>| {
        let (bg, label, color) = match card {
            Some(c) => (Color::srgb(0.92, 0.9, 0.86), card_label(c), suit_color(c.suit)),
            None => (Color::srgb(0.16, 0.2, 0.42), String::new(), WHITE),
        };
        commands
            .spawn((
                Sprite { color: bg, custom_size: Some(Vec2::new(40.0, 56.0)), ..default() },
                Transform::from_xyz(pos.x, pos.y, 3.0),
                CardSprite,
                GameTag,
            ))
            .with_children(|kid| {
                if !label.is_empty() {
                    kid.spawn((
                        Text2d::new(label),
                        TextFont { font_size: 17.0, ..default() },
                        TextColor(color),
                        Transform::from_xyz(0.0, 0.0, 0.1),
                    ));
                }
            });
    };
    // Board.
    for (k, c) in table.board.iter().enumerate() {
        spawn_card(&mut commands, Vec2::new(-96.0 + k as f32 * 48.0, 0.0), Some(*c));
    }
    // Hole cards: yours up, theirs down until shown.
    for i in 0..table.seats.len() {
        let s = table.seats[i].clone();
        if s.out || s.folded {
            continue;
        }
        let p = seat_pos(i);
        let y = if i == 0 { p.y + 52.0 } else { p.y - 52.0 };
        for (k, c) in s.hole.iter().enumerate() {
            let face = if i == 0 || s.shown { Some(*c) } else { None };
            spawn_card(&mut commands, Vec2::new(p.x - 22.0 + k as f32 * 44.0, y), face);
        }
    }
}

/// Seat panels, pot, ticker, and the "you currently hold" hint.
#[allow(clippy::type_complexity)]
fn texts(
    table: Res<Table>,
    mut seat_texts: Query<(&SeatText, &mut Text2d), (Without<PotText>, Without<MsgText>, Without<HintText>)>,
    mut pot: Query<&mut Text2d, (With<PotText>, Without<MsgText>, Without<HintText>)>,
    mut msg: Query<&mut Text2d, (With<MsgText>, Without<PotText>, Without<HintText>)>,
    mut hint: Query<&mut Text2d, (With<HintText>, Without<PotText>, Without<MsgText>)>,
) {
    for (st, mut t) in &mut seat_texts {
        let i = st.0;
        let s = &table.seats[i];
        let name = seat_name(i);
        let line = if s.out {
            format!("{name}\nBUSTED")
        } else {
            let status = if s.folded {
                "FOLDED".to_string()
            } else if s.chips == 0 {
                "ALL-IN".to_string()
            } else if table.stage == Stage::Betting && table.turn == i && !table.over {
                "TO ACT...".to_string()
            } else {
                String::new()
            };
            let bet = if s.committed_street > 0 { format!("  BET {}", s.committed_street) } else { String::new() };
            let btn = if table.dealer == i { " (D)" } else { "" };
            format!("{name}{btn}\n{} CHIPS{bet}\n{status}", s.chips)
        };
        if t.0 != line {
            t.0 = line;
        }
    }
    if let Ok(mut t) = pot.single_mut() {
        let s = format!("POT {}", table.pot());
        if t.0 != s {
            t.0 = s;
        }
    }
    if let Ok(mut t) = msg.single_mut() {
        let s = if table.showdown_lines.is_empty() {
            table.msg.clone()
        } else {
            table.showdown_lines.join("\n")
        };
        if t.0 != s {
            t.0 = s;
        }
    }
    if let Ok(mut t) = hint.single_mut() {
        let s = if table.board.len() >= 3 && !table.seats[0].folded && !table.seats[0].out {
            let mut cards = table.board.clone();
            cards.push(table.seats[0].hole[0]);
            cards.push(table.seats[0].hole[1]);
            let r = best_hand(&cards);
            format!("YOU HOLD: {}", hand_name(r.cat, r.tie[0]))
        } else {
            String::new()
        };
        if t.0 != s {
            t.0 = s;
        }
    }
}
