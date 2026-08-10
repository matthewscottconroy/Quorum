//! An arcade-strength Go opponent for the 9×9 cabinet: one-ply heuristic
//! scoring over the real rules engine. Deliberately beatable — it plays a
//! coherent territorial game, punishes stones left in atari, and never fills
//! its own eyes, but it does not read ladders or fight ko wars.

use crate::go::{GoBoard, Stone, CELLS, SIZE};

/// Picks a move for the side to move, or `None` to pass. `seed` adds a small
/// deterministic jitter so the machine varies from credit to credit.
///
/// Passing logic: if the opponent just passed and the machine is ahead on
/// the board as it stands, it passes too and takes the win; otherwise it
/// plays on as long as any move clears the worth-playing floor.
pub fn bot_move(board: &GoBoard, seed: u64) -> Option<usize> {
    if board.over() {
        return None;
    }
    let me = board.turn;
    if board.passes_in_a_row == 1 && lead_for(board, me) > 0 {
        return None; // opponent passed and the count favors us: close it out
    }
    let mut best: Option<(i32, usize)> = None;
    for pos in 0..CELLS {
        if board.cells[pos].is_some() || fills_own_eye(board, pos, me) {
            continue;
        }
        let mut probe = board.clone();
        if probe.play(pos).is_err() {
            continue;
        }
        let score = score_result(board, &probe, pos, me) + jitter(seed, pos);
        if best.map(|(s, _)| score > s).unwrap_or(true) {
            best = Some((score, pos));
        }
    }
    match best {
        // Nothing worth playing (all that's left is dame-crawling into our
        // own territory): pass rather than lose points to stubbornness.
        Some((score, _)) if score < PASS_FLOOR && board.passes_in_a_row == 1 => None,
        Some((_, pos)) => Some(pos),
        None => None,
    }
}

/// Below this a move is considered not worth preferring over ending an
/// already-decided game.
const PASS_FLOOR: i32 = 4;

fn lead_for(board: &GoBoard, me: Stone) -> i32 {
    let (black, white) = board.area_score();
    match me {
        Stone::Black => black - white,
        Stone::White => white - black,
    }
}

/// True when `pos` is a single-point eye of `me`: every neighbor is our
/// stone and the diagonals don't hand the point to the enemy. Filling those
/// is how bots strangle their own groups.
fn fills_own_eye(board: &GoBoard, pos: usize, me: Stone) -> bool {
    let (x, y) = (pos % SIZE, pos / SIZE);
    let mut neighbors = 0;
    let mut mine = 0;
    for (dx, dy) in [(-1i32, 0i32), (1, 0), (0, -1), (0, 1)] {
        let (nx, ny) = (x as i32 + dx, y as i32 + dy);
        if !(0..SIZE as i32).contains(&nx) || !(0..SIZE as i32).contains(&ny) {
            continue;
        }
        neighbors += 1;
        if board.cells[(ny as usize) * SIZE + nx as usize] == Some(me) {
            mine += 1;
        }
    }
    if mine < neighbors {
        return false;
    }
    // All orthogonal neighbors are ours; require the diagonals to not be
    // majority-enemy (false eye) before refusing to fill.
    let mut enemy_diag = 0;
    let mut diag = 0;
    for (dx, dy) in [(-1i32, -1i32), (1, -1), (-1, 1), (1, 1)] {
        let (nx, ny) = (x as i32 + dx, y as i32 + dy);
        if !(0..SIZE as i32).contains(&nx) || !(0..SIZE as i32).contains(&ny) {
            continue;
        }
        diag += 1;
        if board.cells[(ny as usize) * SIZE + nx as usize] == Some(me.other()) {
            enemy_diag += 1;
        }
    }
    enemy_diag * 2 <= diag
}

/// Heuristic value of the move that turned `before` into `after`.
fn score_result(before: &GoBoard, after: &GoBoard, pos: usize, me: Stone) -> i32 {
    let mut score = 0i32;

    // Captures are king.
    let captured = match me {
        Stone::Black => after.captures_black - before.captures_black,
        Stone::White => after.captures_white - before.captures_white,
    };
    score += captured as i32 * 90;

    // Saving our own group from atari.
    for n in neighbors_of(pos) {
        if before.cells[n] == Some(me) && group_liberties(before, n) == 1 {
            let now = group_liberties(after, pos);
            if now > 1 {
                score += 70;
            }
        }
    }
    // Self-atari is almost always a blunder.
    let own_libs = group_liberties(after, pos);
    if own_libs == 1 {
        score -= 90;
    } else {
        score += (own_libs.min(4) as i32) * 4;
    }
    // Putting an enemy group in atari applies pressure.
    let mut seen_start = [false; CELLS];
    for n in neighbors_of(pos) {
        if after.cells[n] == Some(me.other()) && !seen_start[n] {
            for g in after.group_at(n) {
                seen_start[g] = true;
            }
            if group_liberties(after, n) == 1 {
                score += 28;
            }
        }
    }
    // Mild center/side shaping: the 9x9 middle game is fought around the
    // center, and first-line stones are usually slow.
    let (x, y) = (pos % SIZE, pos / SIZE);
    let edge = x.min(SIZE - 1 - x).min(y).min(SIZE - 1 - y);
    score += match edge {
        0 => -6,
        1 => 2,
        _ => 6,
    };
    score
}

fn neighbors_of(pos: usize) -> impl Iterator<Item = usize> {
    let (x, y) = (pos % SIZE, pos / SIZE);
    [
        (x > 0).then(|| pos - 1),
        (x + 1 < SIZE).then(|| pos + 1),
        (y > 0).then(|| pos - SIZE),
        (y + 1 < SIZE).then(|| pos + SIZE),
    ]
    .into_iter()
    .flatten()
}

fn group_liberties(board: &GoBoard, pos: usize) -> usize {
    let group = board.group_at(pos);
    let mut libs = [false; CELLS];
    for &g in &group {
        for n in neighbors_of(g) {
            if board.cells[n].is_none() {
                libs[n] = true;
            }
        }
    }
    libs.iter().filter(|&&l| l).count()
}

fn jitter(seed: u64, pos: usize) -> i32 {
    let mut x = seed ^ (pos as u64).wrapping_mul(0x9E37_79B9_7F4A_7C15);
    x ^= x >> 33;
    x = x.wrapping_mul(0xFF51_AFD7_ED55_8CCD);
    (x % 5) as i32 - 2
}

#[cfg(test)]
mod tests {
    use super::*;

    fn at(x: usize, y: usize) -> usize {
        y * SIZE + x
    }

    #[test]
    fn captures_a_stone_in_atari() {
        let mut b = GoBoard::new();
        // White stone at (1,1) with a single liberty at (1,2); White to move
        // elsewhere first so it's Black's (the bot's) turn to kill.
        b.play(at(1, 0)).unwrap(); // B
        b.play(at(1, 1)).unwrap(); // W victim
        b.play(at(0, 1)).unwrap(); // B
        b.play(at(8, 8)).unwrap(); // W
        b.play(at(2, 1)).unwrap(); // B
        b.play(at(8, 7)).unwrap(); // W — Black to move, (1,2) captures
        let m = bot_move(&b, 42).expect("bot should play, not pass");
        assert_eq!(m, at(1, 2), "the capture dwarfs every quiet move");
    }

    #[test]
    fn saves_its_own_group_from_atari() {
        let mut b = GoBoard::new();
        // Black stone at (1,1) reduced to one liberty; Black (bot) to move.
        b.play(at(1, 1)).unwrap(); // B (future victim)
        b.play(at(1, 0)).unwrap(); // W
        b.play(at(7, 7)).unwrap(); // B elsewhere
        b.play(at(0, 1)).unwrap(); // W
        b.play(at(7, 6)).unwrap(); // B elsewhere
        b.play(at(2, 1)).unwrap(); // W — black (1,1) has one liberty at (1,2)
        let m = bot_move(&b, 7).expect("bot should play");
        // Either extend at (1,2) or capture something that relieves — here
        // extension is the only rescue.
        assert_eq!(m, at(1, 2), "must answer the atari");
    }

    #[test]
    fn refuses_to_fill_its_own_eye() {
        let mut b = GoBoard::new();
        // Give Black an eye at (0,0) with support; make it Black's turn.
        b.play(at(1, 0)).unwrap(); // B
        b.play(at(8, 8)).unwrap(); // W
        b.play(at(0, 1)).unwrap(); // B
        b.play(at(8, 7)).unwrap(); // W
        b.play(at(1, 1)).unwrap(); // B — eye at (0,0) is now real
        b.play(at(7, 8)).unwrap(); // W — Black to move
        let m = bot_move(&b, 3);
        assert_ne!(m, Some(at(0, 0)), "single-point eye must not be filled");
    }

    #[test]
    fn passes_to_win_after_opponent_pass_when_ahead() {
        let mut b = GoBoard::new();
        // Black owns the whole board minus white's corner scrap.
        for y in 0..SIZE {
            b.play(at(4, y)).unwrap(); // Black wall
            if y < 2 {
                b.play(at(8, y)).unwrap(); // token white stones
            } else {
                b.pass(); // white gives up the exchange
                if b.over() {
                    break;
                }
            }
        }
        // Ensure it is Black's turn with White having just passed.
        if b.turn == Stone::White {
            b.pass();
        }
        assert_eq!(b.passes_in_a_row, 1);
        assert!(lead_for(&b, Stone::Black) > 0);
        assert_eq!(bot_move(&b, 9), None, "ahead + opponent passed = take the win");
    }
}
