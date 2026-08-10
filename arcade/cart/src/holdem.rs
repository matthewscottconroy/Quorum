//! HOLD 'EM — tournament Texas hold 'em against the house bots. The game
//! is public domain and this is an original table: our own cards, our own
//! chips (play tokens, never money), our own opponents. Blinds climb, the
//! short stacks bust, the last stack standing keeps the table.
//!
//! Online rooms (2-6 humans, no bots) are dealt by the SERVER: each seat's
//! hole cards travel only down that seat's own connection, so no player's
//! client ever holds another player's cards. The acting host paces the
//! dealer (deal / street / reveal — all public verbs); betting actions are
//! relayed and validated by every client in lockstep.

use arcade_logic::poker::{
    best_hand, deck, hand_name, Card, HandRank, FLUSH, FULL_HOUSE, QUADS, STRAIGHT, STRAIGHT_FLUSH,
};
use bevy::prelude::*;
use serde::{Deserialize, Serialize};

use crate::retro::{text, AMBER, DIM, WHITE};
use crate::rng::Rng;
use crate::shell::{net_op, net_send, sfx, stat};
use crate::{CabinetConfig, FinalScore, GameTag, NetIn, NetMode, Phase};

pub const BLURB: &[&str] = &[
    "TOURNAMENT TABLES. FICTIONAL CHIPS. REAL GRUDGES.",
    "F FOLDS / C CHECKS OR CALLS / R RAISES / A SHOVES",
    "BLINDS CLIMB. LAST STACK KEEPS THE TABLE.",
];

/// Relayed betting action: "f" fold, "c" check/call, "r" pot raise,
/// "x" all-in — plus the acting host's "afk" fold for a stalled seat.
#[derive(Serialize, Deserialize)]
struct WireAct {
    t: String, // "act" | "afk"
    #[serde(default)]
    a: String,
    #[serde(default)]
    seat: usize,
}

fn decode_card(c: i64) -> Card {
    Card { rank: 2 + (c % 13) as u8, suit: ((c / 13) % 4) as u8 }
}

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
    // ---- online (server-dealt) state ----
    net: bool,
    my_seat: usize,
    present: Vec<bool>,
    /// One dealer verb in flight at a time (deal/street/reveal).
    dealer_req: bool,
    /// Waiting on the server dealer (cards or board) before play resumes.
    dealer_wait: bool,
    /// The acting host's per-turn stall clock; lapse = relayed fold.
    afk: Timer,
    afk_turn: Option<usize>,
    /// Online: the showdown holes have arrived from the dealer.
    revealed: bool,
}

impl Table {
    /// Online: the lowest present seat paces the dealer. Local: seat 0.
    fn acting_host(&self) -> usize {
        self.present.iter().position(|&p| p).unwrap_or(0)
    }
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

/// Shared handles for drawing suit pips: the ASCII-only default font has no
/// card symbols, so hearts, diamonds, spades, and clubs are BUILT — circles,
/// rotated squares, and stems, like a real print shop would.
#[derive(Resource)]
struct CardFx {
    circle: Handle<Mesh>,
    small_circle: Handle<Mesh>,
    suit_mats: Vec<Handle<ColorMaterial>>,
}

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
            (net_apply, table_run, paint, texts)
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

fn setup(
    mut commands: Commands,
    config: Res<CabinetConfig>,
    net: Res<NetMode>,
    mut meshes: ResMut<Assets<Mesh>>,
    mut materials: ResMut<Assets<ColorMaterial>>,
) {
    commands.insert_resource(CardFx {
        circle: meshes.add(Circle::new(4.0)),
        small_circle: meshes.add(Circle::new(3.6)),
        suit_mats: (0..4).map(|i| materials.add(suit_color(i))).collect(),
    });
    let is_net = net.0.is_some();
    let (my_seat, present, players) = match &net.0 {
        Some(cfg) => {
            let n = cfg.seats.clamp(2, 6) as usize;
            let mut present = vec![false; n];
            for (i, &p) in cfg.present.iter().enumerate().take(n) {
                present[i] = p;
            }
            (cfg.seat as usize, present, n)
        }
        None => {
            let n = (config.players.clamp(2, 6)) as usize;
            (0, vec![true; n], n)
        }
    };
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
    let mut seats = seats;
    // Online: unoccupied seats never existed; they are out from hand one.
    for (i, seat) in seats.iter_mut().enumerate() {
        seat.out = is_net && !present[i];
    }
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
        net: is_net,
        my_seat,
        present,
        dealer_req: false,
        dealer_wait: false,
        afk: Timer::from_seconds(35.0, TimerMode::Once),
        afk_turn: None,
        revealed: false,
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

fn rank_label(c: Card) -> String {
    match c.rank {
        14 => "A".into(),
        13 => "K".into(),
        12 => "Q".into(),
        11 => "J".into(),
        n => n.to_string(),
    }
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
    if !table.net {
        table.deck = shuffled_deck(rng);
    }
    table.board.clear();
    table.showdown_lines.clear();
    for s in table.seats.iter_mut() {
        s.folded = s.out;
        s.committed_street = 0;
        s.committed_hand = 0;
        s.acted = false;
        s.shown = false;
        if !s.out && !table.net {
            s.hole = [table.deck.pop().expect("deck"), table.deck.pop().expect("deck")];
        } else if !s.out {
            // Online: placeholders until the dealer's private "cards" and
            // the showdown "holes" fill them in.
            s.hole = [Card { rank: 2, suit: 0 }; 2];
        }
    }
    begin_betting(table);
}

/// Blinds, button walk, first action — shared by local and online hands.
fn begin_betting(table: &mut Table) {
    table.revealed = false;
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

/// Online hand start, triggered by the server's public "dealt" notice. The
/// dealer's hand number keeps every client in lockstep; seats the server
/// did NOT deal (disconnected) sit out.
fn start_hand_net(table: &mut Table, hand: u32, dealt_seats: &[usize]) {
    table.hand_no = hand;
    stat("hands_played", 1);
    table.bb = (START_BB << (hand.saturating_sub(1) / BLINDS_UP_EVERY).min(4)).min(MAX_BB);
    table.board.clear();
    table.showdown_lines.clear();
    for (i, s) in table.seats.iter_mut().enumerate() {
        s.folded = s.out || !dealt_seats.contains(&i);
        s.committed_street = 0;
        s.committed_hand = 0;
        s.acted = false;
        s.shown = false;
        s.hole = [Card { rank: 2, suit: 0 }; 2]; // "cards" fills in our own
    }
    table.dealer_req = false;
    table.dealer_wait = false;
    begin_betting(table);
}

/// Fresh betting street: commitments roll into the pot, everyone may act.
fn reset_street(table: &mut Table) {
    for s in table.seats.iter_mut() {
        s.committed_street = 0;
        s.acted = false;
    }
    table.bet = 0;
    table.min_raise = table.bb;
    table.raises_this_street = 0;
}

/// One betting action. `raise_to` of None = call/check, Some(0) = fold.
fn act(table: &mut Table, seat: usize, action: BetAction) {
    let name = seat_name_for(table, seat);
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

fn seat_name_for(table: &Table, i: usize) -> String {
    if i == table.my_seat {
        "YOU".into()
    } else if table.net {
        format!("SEAT {}", i + 1)
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
            seat_name_for(table, i),
            hand_name(r.cat, r.tie[0]),
            if won { format!(" - WINS {}", awarded_total[i]) } else { String::new() }
        ));
        if i == table.my_seat && won {
            stat("hands_won", 1);
            stat("chips_won", awarded_total[table.my_seat] as u64);
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
            final_score.0 = table.seats[table.my_seat].chips;
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
                    if i == table.my_seat {
                        stat("bust_outs", 1);
                    }
                }
            }
            let me = table.my_seat;
            let alive: Vec<usize> =
                (0..table.seats.len()).filter(|&i| !table.seats[i].out).collect();
            if table.seats[me].out && !table.net {
                table.over = true;
                table.result = "BUSTED OUT".into();
                table.msg = "BUSTED. THE HOUSE THANKS YOU.".into();
                table.dirty = true;
                return;
            }
            // Online, a busted player spectates until the table resolves —
            // walking away mid-tournament is what the ticker is for.
            if alive.len() <= 1 || table.hand_no >= MAX_HANDS {
                table.over = true;
                if alive.len() == 1 && alive[0] == me {
                    stat("tables_swept", 1);
                    table.msg = "THE TABLE IS YOURS.".into();
                } else if table.seats[me].out {
                    table.msg = "BUSTED. THE HOUSE THANKS YOU.".into();
                } else {
                    table.msg = "CLOSING TIME. CHIPS COUNT.".into();
                }
                table.dirty = true;
                return;
            }
            if table.net {
                // The server deals; the acting host asks exactly once.
                if table.acting_host() == me && !table.dealer_req {
                    net_op("deal");
                    table.dealer_req = true;
                }
                table.dealer_wait = true;
                if table.msg.is_empty() {
                    table.msg = "THE HOUSE IS DEALING...".into();
                }
                table.wait = Timer::from_seconds(0.4, TimerMode::Once);
                return; // net_apply's "dealt" moves the table on
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
                if w == table.my_seat {
                    stat("hands_won", 1);
                    stat("chips_won", pot as u64);
                }
                for s in table.seats.iter_mut() {
                    s.committed_hand = 0;
                    s.committed_street = 0;
                }
                table.msg = format!("{} TAKES {} UNCONTESTED", seat_name_for(&table, w), pot);
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
            // Online stall clock: the acting host folds ANY seat that
            // sleeps on its turn (its own included) and tells the room.
            if table.net {
                if table.afk_turn != Some(i) {
                    table.afk_turn = Some(i);
                    table.afk.reset();
                } else if table.acting_host() == table.my_seat
                    && table.afk.tick(time.delta()).finished()
                {
                    if let Ok(w) =
                        serde_json::to_string(&WireAct { t: "afk".into(), a: String::new(), seat: i })
                    {
                        net_send(&w);
                    }
                    act(&mut table, i, BetAction::Fold);
                    table.turn = table.next_from(i, true);
                    return;
                }
            }
            if i == table.my_seat && !table.seats[i].out {
                // Your keys, your move (any seat number online).
                let action = if keys.just_pressed(KeyCode::KeyF) {
                    stat("folds", 1);
                    Some(("f", BetAction::Fold))
                } else if keys.just_pressed(KeyCode::KeyC) {
                    Some(("c", BetAction::CheckCall))
                } else if keys.just_pressed(KeyCode::KeyR) {
                    stat("raises", 1);
                    Some(("r", BetAction::Raise(table.pot().max(table.bb))))
                } else if keys.just_pressed(KeyCode::KeyA) {
                    stat("all_ins", 1);
                    let chips = table.seats[i].chips;
                    Some(("x", BetAction::Raise(chips)))
                } else {
                    None
                };
                if let Some((code, a)) = action {
                    if table.net {
                        if let Ok(w) = serde_json::to_string(&WireAct {
                            t: "act".into(),
                            a: code.into(),
                            seat: i,
                        }) {
                            net_send(&w);
                        }
                    }
                    act(&mut table, i, a);
                    table.turn = table.next_from(i, true);
                }
            } else if !table.net && table.bot_clock.tick(time.delta()).just_finished() {
                let a = bot_decide(&table, i, &mut rng);
                act(&mut table, i, a);
                table.turn = table.next_from(i, true);
            }
            // Online: someone else's turn resolves via the relay.
        }
        Stage::AdvanceStreet => {
            if table.net {
                if table.board.len() >= 5 {
                    table.stage = Stage::Showdown;
                    table.wait = Timer::from_seconds(0.4, TimerMode::Once);
                    return;
                }
                // The server holds the deck; ask once, then wait for "board".
                if table.acting_host() == table.my_seat && !table.dealer_req {
                    net_op("street");
                    table.dealer_req = true;
                }
                table.dealer_wait = true;
                table.wait = Timer::from_seconds(0.3, TimerMode::Once);
                return;
            }
            reset_street(&mut table);
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
            if table.net && !table.revealed {
                // The server knows the cards; the acting host asks once.
                if table.acting_host() == table.my_seat && !table.dealer_req {
                    net_op("reveal");
                    table.dealer_req = true;
                }
                table.msg = "CALLING FOR THE CARDS...".into();
                table.wait = Timer::from_seconds(0.3, TimerMode::Once);
                return;
            }
            showdown(&mut table);
            table.dealer_req = false;
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
    fx: Res<CardFx>,
    cards: Query<Entity, With<CardSprite>>,
) {
    if !table.dirty {
        return;
    }
    table.dirty = false;
    for e in &cards {
        commands.entity(e).despawn();
    }
    let spawn_card = |commands: &mut Commands, fx: &CardFx, pos: Vec2, card: Option<Card>| {
        let bg = if card.is_some() {
            Color::srgb(0.92, 0.9, 0.86)
        } else {
            Color::srgb(0.16, 0.2, 0.42)
        };
        commands
            .spawn((
                Sprite { color: bg, custom_size: Some(Vec2::new(40.0, 56.0)), ..default() },
                Transform::from_xyz(pos.x, pos.y, 3.0),
                CardSprite,
                GameTag,
            ))
            .with_children(|kid| {
                let Some(c) = card else { return };
                let color = suit_color(c.suit);
                kid.spawn((
                    Text2d::new(rank_label(c)),
                    TextFont { font_size: 16.0, ..default() },
                    TextColor(color),
                    Transform::from_xyz(0.0, 15.0, 0.1),
                ));
                spawn_pip(kid, fx, c.suit, Vec2::new(0.0, -8.0));
            });
    };
    // Board.
    for (k, c) in table.board.iter().enumerate() {
        spawn_card(&mut commands, &fx, Vec2::new(-96.0 + k as f32 * 48.0, 0.0), Some(*c));
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
            let face = if i == table.my_seat || s.shown { Some(*c) } else { None };
            spawn_card(&mut commands, &fx, Vec2::new(p.x - 22.0 + k as f32 * 44.0, y), face);
        }
    }
}

/// Draws one suit symbol from primitives, centered at `at` (child space).
/// Spades and clubs get stems; hearts and spades share the two-lobes-plus-
/// point construction, one of them upside down.
fn spawn_pip(kid: &mut ChildSpawnerCommands, fx: &CardFx, suit: u8, at: Vec2) {
    let mat = fx.suit_mats[suit as usize % 4].clone();
    let lobe = |kid: &mut ChildSpawnerCommands, x: f32, y: f32, small: bool| {
        kid.spawn((
            Mesh2d(if small { fx.small_circle.clone() } else { fx.circle.clone() }),
            MeshMaterial2d(mat.clone()),
            Transform::from_xyz(at.x + x, at.y + y, 0.1),
        ));
    };
    let square = |kid: &mut ChildSpawnerCommands, x: f32, y: f32, size: f32| {
        kid.spawn((
            Sprite {
                color: suit_color(suit),
                custom_size: Some(Vec2::splat(size)),
                ..default()
            },
            Transform::from_xyz(at.x + x, at.y + y, 0.1)
                .with_rotation(Quat::from_rotation_z(std::f32::consts::FRAC_PI_4)),
        ));
    };
    let stem = |kid: &mut ChildSpawnerCommands| {
        kid.spawn((
            Sprite {
                color: suit_color(suit),
                custom_size: Some(Vec2::new(2.6, 5.5)),
                ..default()
            },
            Transform::from_xyz(at.x, at.y - 6.5, 0.1),
        ));
    };
    match suit % 4 {
        0 => {
            // Spade: point up, lobes below, stem.
            square(kid, 0.0, 2.5, 8.0);
            lobe(kid, -3.4, -1.2, false);
            lobe(kid, 3.4, -1.2, false);
            stem(kid);
        }
        1 => {
            // Heart: lobes up, point down.
            lobe(kid, -3.4, 2.0, false);
            lobe(kid, 3.4, 2.0, false);
            square(kid, 0.0, -1.5, 8.0);
        }
        2 => {
            // Diamond: one rotated square.
            square(kid, 0.0, 0.0, 9.5);
        }
        _ => {
            // Club: three lobes and a stem.
            lobe(kid, 0.0, 3.6, true);
            lobe(kid, -3.8, -1.4, true);
            lobe(kid, 3.8, -1.4, true);
            stem(kid);
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
        let name = seat_name_for(&table, i);
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
        let s = if table.board.len() >= 3
            && !table.seats[table.my_seat].folded
            && !table.seats[table.my_seat].out
        {
            let mut cards = table.board.clone();
            cards.push(table.seats[table.my_seat].hole[0]);
            cards.push(table.seats[table.my_seat].hole[1]);
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

/// Online events: the dealer's private cards and public board/reveal, other
/// seats' relayed betting actions, AFK folds, and departures. Every client
/// applies the same public sequence, so the tables never diverge.
fn net_apply(mut events: EventReader<NetIn>, net: Res<NetMode>, mut table: ResMut<Table>) {
    if net.0.is_none() {
        events.clear();
        return;
    }
    for ev in events.read() {
        if ev.left {
            let s = ev.seat as usize;
            if s < table.seats.len() {
                if s < table.present.len() {
                    table.present[s] = false;
                }
                if !table.seats[s].folded && !table.seats[s].out {
                    act(&mut table, s, BetAction::Fold);
                    if table.stage == Stage::Betting && table.turn == s {
                        table.turn = table.next_from(s, true);
                    }
                }
                table.seats[s].out = true; // gone for good; stack retires
                let name = seat_name_for(&table, s);
                table.msg = format!("{name} LEFT THE TABLE");
                table.dirty = true;
            }
            continue;
        }
        // Seat 255 marks messages from the house dealer.
        if ev.seat == 255 {
            let Ok(v) = serde_json::from_str::<serde_json::Value>(&ev.data) else { continue };
            match v.get("op").and_then(|o| o.as_str()) {
                Some("cards") => {
                    if let Some(arr) = v.get("hole").and_then(|h| h.as_array()) {
                        if arr.len() == 2 {
                            let me = table.my_seat;
                            table.seats[me].hole = [
                                decode_card(arr[0].as_i64().unwrap_or(0)),
                                decode_card(arr[1].as_i64().unwrap_or(0)),
                            ];
                            table.dirty = true;
                        }
                    }
                }
                Some("dealt") => {
                    let hand = v.get("hand").and_then(|h| h.as_u64()).unwrap_or(0) as u32;
                    let seats: Vec<usize> = v
                        .get("seats")
                        .and_then(|a| a.as_array())
                        .map(|a| a.iter().filter_map(|x| x.as_u64().map(|n| n as usize)).collect())
                        .unwrap_or_default();
                    start_hand_net(&mut table, hand, &seats);
                }
                Some("board") => {
                    if let Some(cards) = v.get("cards").and_then(|c| c.as_array()) {
                        for c in cards {
                            table.board.push(decode_card(c.as_i64().unwrap_or(0)));
                        }
                        reset_street(&mut table);
                        table.stage = Stage::Betting;
                        table.turn = table.next_from(table.dealer, true);
                        table.dealer_req = false;
                        table.dealer_wait = false;
                        table.msg = match table.board.len() {
                            3 => "THE FLOP".into(),
                            4 => "THE TURN".into(),
                            _ => "THE RIVER".into(),
                        };
                        table.wait = Timer::from_seconds(0.6, TimerMode::Once);
                        table.dirty = true;
                        sfx("drop");
                    }
                }
                Some("holes") => {
                    if let Some(map) = v.get("holes").and_then(|h| h.as_object()) {
                        for (k, arr) in map {
                            if let (Ok(seat), Some(a)) = (k.parse::<usize>(), arr.as_array()) {
                                if seat < table.seats.len() && a.len() == 2 {
                                    table.seats[seat].hole = [
                                        decode_card(a[0].as_i64().unwrap_or(0)),
                                        decode_card(a[1].as_i64().unwrap_or(0)),
                                    ];
                                }
                            }
                        }
                        table.revealed = true;
                        table.dealer_req = false;
                        table.dealer_wait = false;
                        table.dirty = true;
                    }
                }
                _ => {}
            }
            continue;
        }
        // Other players' betting actions, applied in lockstep.
        if ev.seat as usize == table.my_seat {
            continue;
        }
        let Ok(w) = serde_json::from_str::<WireAct>(&ev.data) else { continue };
        match w.t.as_str() {
            "act" if table.stage == Stage::Betting && table.turn == ev.seat as usize => {
                let action = match w.a.as_str() {
                    "f" => BetAction::Fold,
                    "c" => BetAction::CheckCall,
                    "r" => BetAction::Raise(table.pot().max(table.bb)),
                    _ => {
                        let chips = table.seats[ev.seat as usize].chips;
                        BetAction::Raise(chips)
                    }
                };
                act(&mut table, ev.seat as usize, action);
                table.turn = table.next_from(ev.seat as usize, true);
            }
            "afk"
                if table.stage == Stage::Betting
                    && table.turn == w.seat
                    && ev.seat as usize == table.acting_host() =>
            {
                act(&mut table, w.seat, BetAction::Fold);
                table.turn = table.next_from(w.seat, true);
            }
            _ => {}
        }
    }
}
