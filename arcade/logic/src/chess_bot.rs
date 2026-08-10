//! An arcade-strength chess opponent: alpha-beta negamax to a shallow fixed
//! depth with a capture-only quiescence tail, over the perft-verified move
//! generator. Deliberately beatable — a cabinet sparring partner, not an
//! engine — but it will not hang pieces to one-move tactics and it finds
//! short mates.

use crate::chess::{file, rank, Board, Color, Move, Piece};

const MATE: i32 = 30_000;
/// Full-width search depth. Three plies plus quiescence sees the quiet move
/// behind a fork or skewer — the class of tactic depth two walked into.
/// Cabinets spread the root loop across frames (see `root_moves` /
/// `score_root_move`), so the extra depth never freezes a frame.
const DEPTH: u32 = 3;
const QUIESCE_DEPTH: u32 = 4;

/// The root move list, strongest-looking first. Score each entry with
/// [`score_root_move`] — incrementally across frames if you like — and keep
/// the max; that is exactly what [`best_move_with_history`] does in one call.
pub fn root_moves(board: &Board) -> Vec<Move> {
    let mut moves = board.legal_moves();
    order(board, &mut moves);
    moves
}

/// Exact score of one root move (full window: a fail-hard cutoff would
/// collapse every refuted sibling onto the same bound, and jitter would then
/// happily promote one). `seen` is the game's position-hash history: a move
/// that recreates a twice-seen position scores as the draw it is, so a
/// winning machine sidesteps repetition instead of shuffling into it — and a
/// losing one correctly grabs the escape hatch.
pub fn score_root_move(board: &Board, m: Move, seen: &[u64]) -> i32 {
    let mut next = board.clone();
    next.apply(m);
    let hash = next.position_hash();
    if seen.iter().filter(|&&h| h == hash).count() >= 2 {
        return 0;
    }
    -negamax(&next, DEPTH - 1, -MATE * 2, MATE * 2, 1)
}

/// Picks a move for the side to move; `seed` adds a small deterministic
/// jitter so the machine doesn't play the identical game every credit.
pub fn best_move(board: &Board, seed: u64) -> Option<Move> {
    best_move_with_history(board, seed, &[])
}

/// [`best_move`], with repetition awareness from the hash history.
pub fn best_move_with_history(board: &Board, seed: u64, seen: &[u64]) -> Option<Move> {
    let moves = root_moves(board);
    let mut best: Option<Move> = None;
    let mut best_score = -MATE * 2;
    for (i, &m) in moves.iter().enumerate() {
        // Deterministic per-move jitter (±4 centipawns) for variety.
        let score = score_root_move(board, m, seen) + jitter(seed, i);
        if best.is_none() || score > best_score {
            best_score = score;
            best = Some(m);
        }
    }
    best
}

/// Deterministic ±4-centipawn noise per (seed, root index) — public so a
/// cabinet running the root loop incrementally applies the same variety.
pub fn jitter(seed: u64, i: usize) -> i32 {
    let mut x = seed ^ (i as u64).wrapping_mul(0x9E37_79B9_7F4A_7C15);
    x ^= x >> 33;
    x = x.wrapping_mul(0xFF51_AFD7_ED55_8CCD);
    (x % 9) as i32 - 4
}

fn negamax(board: &Board, depth: u32, mut alpha: i32, beta: i32, ply: i32) -> i32 {
    let mut moves = board.legal_moves();
    if moves.is_empty() {
        return if board.in_check(board.turn) { -MATE + ply } else { 0 };
    }
    if depth == 0 {
        return quiesce(board, alpha, beta, QUIESCE_DEPTH);
    }
    order(board, &mut moves);
    for m in moves {
        let mut next = board.clone();
        next.apply(m);
        let score = -negamax(&next, depth - 1, -beta, -alpha, ply + 1);
        if score >= beta {
            return beta;
        }
        if score > alpha {
            alpha = score;
        }
    }
    alpha
}

/// Captures-only extension so the horizon doesn't hallucinate free pieces.
fn quiesce(board: &Board, mut alpha: i32, beta: i32, depth: u32) -> i32 {
    let stand = eval(board);
    if stand >= beta {
        return beta;
    }
    if stand > alpha {
        alpha = stand;
    }
    if depth == 0 {
        return alpha;
    }
    let mut captures: Vec<Move> = board
        .legal_moves()
        .into_iter()
        .filter(|m| is_capture(board, *m))
        .collect();
    order(board, &mut captures);
    for m in captures {
        let mut next = board.clone();
        next.apply(m);
        let score = -quiesce(&next, -beta, -alpha, depth - 1);
        if score >= beta {
            return beta;
        }
        if score > alpha {
            alpha = score;
        }
    }
    alpha
}

fn is_capture(board: &Board, m: Move) -> bool {
    board.cells[m.to].is_some()
        || (board.cells[m.from].map(|(_, p)| p) == Some(Piece::Pawn) && Some(m.to) == board.ep)
}

/// Most-valuable-victim first, quiet moves last — cheap and effective.
fn order(board: &Board, moves: &mut [Move]) {
    moves.sort_by_key(|m| {
        let victim = board.cells[m.to].map(|(_, p)| p.value()).unwrap_or(0);
        let promo = m.promo.map(|p| p.value()).unwrap_or(0);
        -(victim * 10 + promo)
    });
}

/// Static evaluation from the side to move's perspective: material plus a
/// whisper of positional sense (centralization, pawn advancement).
fn eval(board: &Board) -> i32 {
    let mut score = 0;
    for (sq, cell) in board.cells.iter().enumerate() {
        let Some((c, p)) = cell else { continue };
        let f = file(sq) as i32;
        let r = rank(sq) as i32;
        let centrality = 3 - ((2 * f - 7).abs() + (2 * r - 7).abs()) / 4; // 0..3
        let advance = if *c == Color::White { r } else { 7 - r };
        let positional = match p {
            Piece::Pawn => advance * 4 + centrality,
            Piece::Knight | Piece::Bishop => centrality * 6,
            Piece::Rook => centrality * 2,
            Piece::Queen => centrality * 2,
            Piece::King => -centrality * 2, // stay out of the middlegame center
        };
        let value = p.value() * 100 + positional;
        if *c == board.turn {
            score += value;
        } else {
            score -= value;
        }
    }
    score
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::chess::Status;

    fn mv(b: &mut Board, from: usize, to: usize) {
        let m = b
            .legal_moves()
            .into_iter()
            .find(|m| m.from == from && m.to == to)
            .expect("move should be legal");
        b.apply(m);
    }

    #[test]
    fn finds_mate_in_one() {
        // Scholar's mate position, White to play Qxf7#.
        let mut b = Board::start();
        mv(&mut b, 12, 28); // e4
        mv(&mut b, 52, 36); // e5
        mv(&mut b, 5, 26); // Bc4
        mv(&mut b, 57, 42); // Nc6
        mv(&mut b, 3, 39); // Qh5
        mv(&mut b, 62, 45); // Nf6??
        let m = best_move(&b, 7).unwrap();
        b.apply(m);
        assert_eq!(
            b.status(),
            Status::Checkmate { winner: Color::White },
            "bot must play the mate, played {}->{}",
            m.from,
            m.to
        );
    }

    #[test]
    fn takes_a_free_queen() {
        use Color::*;
        use Piece::*;
        let mut cells: [Option<(Color, Piece)>; 64] = [None; 64];
        cells[4] = Some((White, King));
        cells[60] = Some((Black, King));
        cells[27] = Some((White, Rook)); // d4 rook
        cells[35] = Some((Black, Queen)); // d5 queen, undefended, on the rook's file
        let b = Board { cells, turn: White, castle: [false; 4], ep: None, halfmove: 0 };
        let m = best_move(&b, 1).unwrap();
        assert_eq!((m.from, m.to), (27, 35), "rook must take the free queen");
    }

    #[test]
    fn does_not_grab_a_defended_pawn_with_the_queen() {
        // Queen takes pawn, pawn is defended by a knight: quiescence must see
        // the recapture and decline.
        use Color::*;
        use Piece::*;
        let mut cells: [Option<(Color, Piece)>; 64] = [None; 64];
        cells[4] = Some((White, King));
        cells[60] = Some((Black, King));
        cells[3] = Some((White, Queen)); // d1
        cells[35] = Some((Black, Pawn)); // d5
        cells[52] = Some((Black, Knight)); // e7 defends d5
        let b = Board { cells, turn: White, castle: [false; 4], ep: None, halfmove: 0 };
        let m = best_move(&b, 3).unwrap();
        assert_ne!((m.from, m.to), (3, 35), "QxP into a knight recapture loses the queen");
    }
}
