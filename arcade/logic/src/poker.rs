//! Texas hold 'em hand evaluation: rank any 5-to-7 cards to a totally
//! ordered value with full kicker tiebreaks. Pure logic — the deal, the
//! betting, and the chips live in the cabinet. The game itself is public
//! domain; this is an original implementation.

/// A playing card: rank 2..=14 (11 J, 12 Q, 13 K, 14 A), suit 0..=3.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub struct Card {
    pub rank: u8,
    pub suit: u8,
}

/// Hand categories, weakest to strongest. `tie` carries the ranks that
/// break ties within a category, most significant first — deriving Ord on
/// (cat, tie) gives the full poker ordering.
#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Debug)]
pub struct HandRank {
    pub cat: u8, // 0 high card … 8 straight flush
    pub tie: [u8; 5],
}

pub const HIGH_CARD: u8 = 0;
pub const PAIR: u8 = 1;
pub const TWO_PAIR: u8 = 2;
pub const TRIPS: u8 = 3;
pub const STRAIGHT: u8 = 4;
pub const FLUSH: u8 = 5;
pub const FULL_HOUSE: u8 = 6;
pub const QUADS: u8 = 7;
pub const STRAIGHT_FLUSH: u8 = 8;

pub fn hand_name(cat: u8, high: u8) -> &'static str {
    match cat {
        STRAIGHT_FLUSH if high == 14 => "ROYAL FLUSH",
        STRAIGHT_FLUSH => "STRAIGHT FLUSH",
        QUADS => "FOUR OF A KIND",
        FULL_HOUSE => "FULL HOUSE",
        FLUSH => "FLUSH",
        STRAIGHT => "STRAIGHT",
        TRIPS => "THREE OF A KIND",
        TWO_PAIR => "TWO PAIR",
        PAIR => "A PAIR",
        _ => "HIGH CARD",
    }
}

/// The 52-card deck in a fixed order; the cabinet shuffles it.
pub fn deck() -> Vec<Card> {
    let mut d = Vec::with_capacity(52);
    for suit in 0..4u8 {
        for rank in 2..=14u8 {
            d.push(Card { rank, suit });
        }
    }
    d
}

/// Straight detection over a set of distinct ranks (descending). Returns
/// the straight's high card, honoring the wheel (A-5-4-3-2 → high 5).
fn straight_high(mut ranks: Vec<u8>) -> Option<u8> {
    ranks.sort_unstable_by(|a, b| b.cmp(a));
    ranks.dedup();
    if ranks.contains(&14) {
        ranks.push(1); // the ace also plays low
    }
    let mut run = 1;
    for i in 1..ranks.len() {
        if ranks[i - 1] - ranks[i] == 1 {
            run += 1;
            if run >= 5 {
                return Some(ranks[i] + 4);
            }
        } else {
            run = 1;
        }
    }
    None
}

/// Evaluates exactly five cards.
pub fn eval5(cards: &[Card; 5]) -> HandRank {
    let mut ranks: Vec<u8> = cards.iter().map(|c| c.rank).collect();
    ranks.sort_unstable_by(|a, b| b.cmp(a));
    let flush = cards.iter().all(|c| c.suit == cards[0].suit);
    let straight = straight_high(ranks.clone());

    if flush {
        if let Some(high) = straight {
            return HandRank { cat: STRAIGHT_FLUSH, tie: [high, 0, 0, 0, 0] };
        }
    }
    // Count multiples: (count, rank) sorted by count then rank, descending.
    let mut counts: Vec<(u8, u8)> = Vec::new();
    for &r in &ranks {
        if let Some(e) = counts.iter_mut().find(|(_, cr)| *cr == r) {
            e.0 += 1;
        } else {
            counts.push((1, r));
        }
    }
    counts.sort_unstable_by(|a, b| b.cmp(a));

    let mut tie = [0u8; 5];
    let fill = |tie: &mut [u8; 5], counts: &Vec<(u8, u8)>| {
        for (i, &(_, r)) in counts.iter().enumerate().take(5) {
            tie[i] = r;
        }
    };
    match (counts[0].0, counts.get(1).map(|c| c.0).unwrap_or(0)) {
        (4, _) => {
            fill(&mut tie, &counts);
            HandRank { cat: QUADS, tie }
        }
        (3, 2) => {
            fill(&mut tie, &counts);
            HandRank { cat: FULL_HOUSE, tie }
        }
        _ if flush => {
            for (i, &r) in ranks.iter().enumerate() {
                tie[i] = r;
            }
            HandRank { cat: FLUSH, tie }
        }
        _ if straight.is_some() => {
            HandRank { cat: STRAIGHT, tie: [straight.expect("checked"), 0, 0, 0, 0] }
        }
        (3, _) => {
            fill(&mut tie, &counts);
            HandRank { cat: TRIPS, tie }
        }
        (2, 2) => {
            fill(&mut tie, &counts);
            HandRank { cat: TWO_PAIR, tie }
        }
        (2, _) => {
            fill(&mut tie, &counts);
            HandRank { cat: PAIR, tie }
        }
        _ => {
            for (i, &r) in ranks.iter().enumerate() {
                tie[i] = r;
            }
            HandRank { cat: HIGH_CARD, tie }
        }
    }
}

/// Best five-card hand from 5, 6, or 7 cards (hole + board): evaluate every
/// 5-subset (at most 21 of them) and keep the maximum.
pub fn best_hand(cards: &[Card]) -> HandRank {
    let n = cards.len();
    debug_assert!((5..=7).contains(&n));
    fn excluding(cards: &[Card], ex1: usize, ex2: usize, best: &mut Option<HandRank>) {
        let mut five = [Card { rank: 0, suit: 0 }; 5];
        let mut k = 0;
        for (i, &c) in cards.iter().enumerate() {
            if i != ex1 && i != ex2 {
                five[k] = c;
                k += 1;
            }
        }
        let r = eval5(&five);
        if best.map(|b| r > b).unwrap_or(true) {
            *best = Some(r);
        }
    }
    let mut best: Option<HandRank> = None;
    match n {
        5 => excluding(cards, usize::MAX, usize::MAX, &mut best),
        6 => {
            for a in 0..6 {
                excluding(cards, a, usize::MAX, &mut best);
            }
        }
        _ => {
            for a in 0..7 {
                for b in (a + 1)..7 {
                    excluding(cards, a, b, &mut best);
                }
            }
        }
    }
    best.expect("at least one 5-card hand")
}

#[cfg(test)]
mod tests {
    use super::*;

    fn c(rank: u8, suit: u8) -> Card {
        Card { rank, suit }
    }

    #[test]
    fn category_ladder_holds() {
        let royal = eval5(&[c(14, 0), c(13, 0), c(12, 0), c(11, 0), c(10, 0)]);
        let sflush = eval5(&[c(9, 1), c(8, 1), c(7, 1), c(6, 1), c(5, 1)]);
        let quads = eval5(&[c(9, 0), c(9, 1), c(9, 2), c(9, 3), c(2, 0)]);
        let boat = eval5(&[c(8, 0), c(8, 1), c(8, 2), c(4, 0), c(4, 1)]);
        let flush = eval5(&[c(14, 2), c(10, 2), c(8, 2), c(6, 2), c(3, 2)]);
        let straight = eval5(&[c(10, 0), c(9, 1), c(8, 2), c(7, 3), c(6, 0)]);
        let trips = eval5(&[c(7, 0), c(7, 1), c(7, 2), c(14, 0), c(2, 1)]);
        let two_pair = eval5(&[c(13, 0), c(13, 1), c(4, 2), c(4, 3), c(9, 0)]);
        let pair = eval5(&[c(12, 0), c(12, 1), c(9, 2), c(6, 3), c(3, 0)]);
        let high = eval5(&[c(14, 0), c(12, 1), c(9, 2), c(6, 3), c(3, 0)]);
        let ladder = [high, pair, two_pair, trips, straight, flush, boat, quads, sflush, royal];
        for w in ladder.windows(2) {
            assert!(w[0] < w[1], "{:?} must lose to {:?}", w[0], w[1]);
        }
        assert_eq!(hand_name(royal.cat, royal.tie[0]), "ROYAL FLUSH");
    }

    #[test]
    fn the_wheel_is_a_five_high_straight() {
        let wheel = eval5(&[c(14, 0), c(5, 1), c(4, 2), c(3, 3), c(2, 0)]);
        assert_eq!(wheel.cat, STRAIGHT);
        assert_eq!(wheel.tie[0], 5);
        let six_high = eval5(&[c(6, 0), c(5, 1), c(4, 2), c(3, 3), c(2, 0)]);
        assert!(six_high > wheel, "6-high straight beats the wheel");
    }

    #[test]
    fn kickers_break_ties() {
        let pair_ace_k = eval5(&[c(9, 0), c(9, 1), c(14, 2), c(13, 3), c(2, 0)]);
        let pair_ace_q = eval5(&[c(9, 2), c(9, 3), c(14, 0), c(12, 1), c(2, 1)]);
        assert!(pair_ace_k > pair_ace_q, "king kicker beats queen kicker");

        let boat_a = eval5(&[c(8, 0), c(8, 1), c(8, 2), c(14, 0), c(14, 1)]);
        let boat_b = eval5(&[c(8, 0), c(8, 1), c(8, 3), c(13, 0), c(13, 1)]);
        assert!(boat_a > boat_b, "eights full of aces beats eights full of kings");

        let tp_high = eval5(&[c(13, 0), c(13, 1), c(4, 2), c(4, 3), c(10, 0)]);
        let tp_low = eval5(&[c(13, 2), c(13, 3), c(4, 0), c(4, 1), c(9, 0)]);
        assert!(tp_high > tp_low, "two-pair kicker decides");
    }

    #[test]
    fn seven_cards_pick_the_best_five() {
        // Hole: two hearts completing a flush hidden inside seven cards.
        let seven = [c(14, 1), c(9, 1), c(13, 1), c(7, 1), c(2, 1), c(13, 0), c(13, 2)];
        let best = best_hand(&seven);
        assert_eq!(best.cat, FLUSH, "the flush outranks trips of kings: {best:?}");
        assert_eq!(best.tie[0], 14);
        // Six cards work too.
        let six = [c(10, 0), c(9, 1), c(8, 2), c(7, 3), c(6, 0), c(2, 1)];
        assert_eq!(best_hand(&six).cat, STRAIGHT);
    }

    #[test]
    fn split_pots_compare_equal() {
        // Identical straights on the board dominate both hands.
        let board = [c(10, 0), c(9, 1), c(8, 2), c(7, 3), c(6, 0)];
        let a = best_hand(&[board[0], board[1], board[2], board[3], board[4], c(2, 1), c(3, 2)]);
        let b = best_hand(&[board[0], board[1], board[2], board[3], board[4], c(4, 3), c(2, 2)]);
        assert_eq!(a, b, "board plays: split pot");
    }
}
