//! Go rules on a 9×9 board: captures, suicide prohibition, the simple ko
//! rule, passing, and Chinese (area) scoring. Pure logic, no presentation.

pub const SIZE: usize = 9;
pub const CELLS: usize = SIZE * SIZE;
/// Compensation for white moving second, in half points (5.5 → 11).
pub const KOMI_HALF_POINTS: i32 = 11;

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Stone {
    Black,
    White,
}

impl Stone {
    pub fn other(self) -> Stone {
        match self {
            Stone::Black => Stone::White,
            Stone::White => Stone::Black,
        }
    }
}

#[derive(Clone, PartialEq, Eq)]
pub struct GoBoard {
    pub cells: [Option<Stone>; CELLS],
    pub turn: Stone,
    pub captures_black: u32,
    pub captures_white: u32,
    pub passes_in_a_row: u32,
    /// The most recently placed stone, for the UI's last-move marker.
    pub last: Option<usize>,
    /// Simple ko: a board position (cells only) that the next move may not recreate.
    ko_forbidden: Option<[Option<Stone>; CELLS]>,
    /// Positional superko: hashes of every board position seen so far. No
    /// play may recreate ANY of them — the backstop for triple-ko and
    /// sending-two-returning-one cycles that simple ko lets loop forever.
    seen: Vec<u64>,
    /// Undo history: one snapshot per play/pass (hotseat courtesy undo).
    history: Vec<GoSnapshot>,
}

#[derive(Clone, PartialEq, Eq)]
struct GoSnapshot {
    cells: [Option<Stone>; CELLS],
    turn: Stone,
    captures_black: u32,
    captures_white: u32,
    passes_in_a_row: u32,
    last: Option<usize>,
    ko_forbidden: Option<[Option<Stone>; CELLS]>,
    seen_len: usize,
}

fn hash_cells(cells: &[Option<Stone>; CELLS]) -> u64 {
    let mut h: u64 = 0xcbf2_9ce4_8422_2325;
    for cell in cells {
        let code: u8 = match cell {
            None => 0,
            Some(Stone::Black) => 1,
            Some(Stone::White) => 2,
        };
        h ^= code as u64;
        h = h.wrapping_mul(0x100_0000_01b3);
    }
    h
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum PlayError {
    Occupied,
    Suicide,
    Ko,
    Over,
}

fn neighbors(pos: usize) -> impl Iterator<Item = usize> {
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

impl Default for GoBoard {
    fn default() -> Self {
        Self::new()
    }
}

impl GoBoard {
    pub fn new() -> GoBoard {
        let cells = [None; CELLS];
        GoBoard {
            cells,
            turn: Stone::Black,
            captures_black: 0,
            captures_white: 0,
            passes_in_a_row: 0,
            last: None,
            ko_forbidden: None,
            seen: vec![hash_cells(&cells)],
            history: Vec::new(),
        }
    }

    pub fn over(&self) -> bool {
        self.passes_in_a_row >= 2
    }

    /// Reopens a game closed by two passes — the escape hatch from the
    /// dead-stone marking phase when someone disputes and wants to play on.
    pub fn resume(&mut self) {
        self.passes_in_a_row = 0;
    }

    /// The stones connected to `pos` (empty when the point is empty).
    /// Public for the UI's mark-dead-groups phase.
    pub fn group_at(&self, pos: usize) -> Vec<usize> {
        if self.cells[pos].is_none() {
            return Vec::new();
        }
        self.group(pos).0
    }

    /// The group containing `pos` and whether it has any liberty.
    fn group(&self, pos: usize) -> (Vec<usize>, bool) {
        let color = self.cells[pos];
        let mut seen = [false; CELLS];
        let mut stack = vec![pos];
        let mut group = Vec::new();
        let mut liberty = false;
        seen[pos] = true;
        while let Some(p) = stack.pop() {
            group.push(p);
            for n in neighbors(p) {
                match self.cells[n] {
                    None => liberty = true,
                    c if c == color && !seen[n] => {
                        seen[n] = true;
                        stack.push(n);
                    }
                    _ => {}
                }
            }
        }
        (group, liberty)
    }

    fn snapshot(&self) -> GoSnapshot {
        GoSnapshot {
            cells: self.cells,
            turn: self.turn,
            captures_black: self.captures_black,
            captures_white: self.captures_white,
            passes_in_a_row: self.passes_in_a_row,
            last: self.last,
            ko_forbidden: self.ko_forbidden,
            seen_len: self.seen.len(),
        }
    }

    /// Reverts the most recent play or pass. Returns false when there is
    /// nothing to undo. Hotseat courtesy — network games never call this.
    pub fn undo(&mut self) -> bool {
        let Some(snap) = self.history.pop() else { return false };
        self.cells = snap.cells;
        self.turn = snap.turn;
        self.captures_black = snap.captures_black;
        self.captures_white = snap.captures_white;
        self.passes_in_a_row = snap.passes_in_a_row;
        self.last = snap.last;
        self.ko_forbidden = snap.ko_forbidden;
        self.seen.truncate(snap.seen_len);
        true
    }

    pub fn pass(&mut self) {
        if self.over() {
            return;
        }
        self.history.push(self.snapshot());
        self.passes_in_a_row += 1;
        self.ko_forbidden = None;
        self.turn = self.turn.other();
    }

    pub fn play(&mut self, pos: usize) -> Result<(), PlayError> {
        if self.over() {
            return Err(PlayError::Over);
        }
        if self.cells[pos].is_some() {
            return Err(PlayError::Occupied);
        }
        let me = self.turn;
        let mut next = self.cells;
        next[pos] = Some(me);

        // Capture enemy groups left without liberties.
        let mut captured = 0u32;
        let mut checked = [false; CELLS];
        for n in neighbors(pos) {
            if next[n] == Some(me.other()) && !checked[n] {
                let probe = GoBoard { cells: next, ..self.clone() };
                let (group, liberty) = probe.group(n);
                for &g in &group {
                    checked[g] = true;
                }
                if !liberty {
                    for g in group {
                        next[g] = None;
                        captured += 1;
                    }
                }
            }
        }
        // Suicide: after removals, my own group must have a liberty.
        {
            let probe = GoBoard { cells: next, ..self.clone() };
            let (_, liberty) = probe.group(pos);
            if !liberty {
                return Err(PlayError::Suicide);
            }
        }
        // Simple ko: may not recreate the position the opponent just faced.
        if let Some(forbidden) = &self.ko_forbidden {
            if next == *forbidden {
                return Err(PlayError::Ko);
            }
        }
        // Positional superko: may not recreate ANY earlier whole-board
        // position (covers triple ko and longer cycles).
        let next_hash = hash_cells(&next);
        if self.seen.contains(&next_hash) {
            return Err(PlayError::Ko);
        }
        self.history.push(self.snapshot());
        self.seen.push(next_hash);
        // Ko arises when exactly one stone is captured by a single new stone.
        self.ko_forbidden = if captured == 1 { Some(self.cells) } else { None };
        match me {
            Stone::Black => self.captures_black += captured,
            Stone::White => self.captures_white += captured,
        }
        self.cells = next;
        self.passes_in_a_row = 0;
        self.last = Some(pos);
        self.turn = me.other();
        Ok(())
    }

    /// Chinese area scoring in half points: stones on the board plus empty
    /// regions bordered exclusively by one color. White receives komi.
    /// Returns (black_half_points, white_half_points).
    pub fn area_score(&self) -> (i32, i32) {
        let mut black = 0i32;
        let mut white = 0i32;
        let mut seen = [false; CELLS];
        for pos in 0..CELLS {
            match self.cells[pos] {
                Some(Stone::Black) => black += 2,
                Some(Stone::White) => white += 2,
                None if !seen[pos] => {
                    // Flood the empty region; note which colors border it.
                    let mut stack = vec![pos];
                    let mut region = 0i32;
                    let (mut b_border, mut w_border) = (false, false);
                    seen[pos] = true;
                    while let Some(p) = stack.pop() {
                        region += 2;
                        for n in neighbors(p) {
                            match self.cells[n] {
                                Some(Stone::Black) => b_border = true,
                                Some(Stone::White) => w_border = true,
                                None if !seen[n] => {
                                    seen[n] = true;
                                    stack.push(n);
                                }
                                None => {}
                            }
                        }
                    }
                    if b_border && !w_border {
                        black += region;
                    } else if w_border && !b_border {
                        white += region;
                    }
                }
                None => {}
            }
        }
        (black, white + KOMI_HALF_POINTS)
    }

    /// Area score after removing the stones marked dead — the settlement the
    /// marking phase agrees on. `dead[pos]` marks a stone for removal.
    pub fn score_with_dead(&self, dead: &[bool; CELLS]) -> (i32, i32) {
        let mut cells = self.cells;
        for (pos, is_dead) in dead.iter().enumerate() {
            if *is_dead {
                cells[pos] = None;
            }
        }
        let probe = GoBoard { cells, ..self.clone() };
        probe.area_score()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn at(x: usize, y: usize) -> usize {
        y * SIZE + x
    }

    #[test]
    fn single_stone_capture() {
        let mut b = GoBoard::new();
        // Black surrounds a white stone at (1,1) from three sides, white
        // plays elsewhere, black completes the capture.
        b.play(at(1, 0)).unwrap(); // B
        b.play(at(1, 1)).unwrap(); // W (the victim)
        b.play(at(0, 1)).unwrap(); // B
        b.play(at(8, 8)).unwrap(); // W elsewhere
        b.play(at(2, 1)).unwrap(); // B
        b.play(at(8, 7)).unwrap(); // W elsewhere
        b.play(at(1, 2)).unwrap(); // B completes
        assert_eq!(b.cells[at(1, 1)], None, "white stone must be captured");
        assert_eq!(b.captures_black, 1);
    }

    #[test]
    fn suicide_is_illegal() {
        let mut b = GoBoard::new();
        // White builds an eye at (0,0): stones at (1,0) and (0,1).
        b.play(at(5, 5)).unwrap(); // B elsewhere
        b.play(at(1, 0)).unwrap(); // W
        b.play(at(5, 6)).unwrap(); // B elsewhere
        b.play(at(0, 1)).unwrap(); // W
        // Black playing (0,0) would have zero liberties and captures nothing.
        assert_eq!(b.play(at(0, 0)), Err(PlayError::Suicide));
    }

    #[test]
    fn ko_immediate_recapture_forbidden() {
        let mut b = GoBoard::new();
        // Standard ko shape around (2,1)/(1,1).
        b.play(at(1, 0)).unwrap(); // B
        b.play(at(2, 0)).unwrap(); // W
        b.play(at(0, 1)).unwrap(); // B
        b.play(at(3, 1)).unwrap(); // W
        b.play(at(1, 2)).unwrap(); // B
        b.play(at(2, 2)).unwrap(); // W
        b.play(at(2, 1)).unwrap(); // B takes the ko point
        b.play(at(1, 1)).unwrap(); // W captures the black ko stone
        assert_eq!(b.cells[at(2, 1)], None);
        // Black may not immediately recapture.
        assert_eq!(b.play(at(2, 1)), Err(PlayError::Ko));
        // After a ko threat elsewhere, the recapture becomes legal.
        b.play(at(8, 8)).unwrap(); // B elsewhere
        b.play(at(7, 8)).unwrap(); // W answers
        assert!(b.play(at(2, 1)).is_ok());
    }

    #[test]
    fn undo_reverts_captures_ko_and_turn() {
        let mut b = GoBoard::new();
        b.play(at(1, 0)).unwrap(); // B
        b.play(at(1, 1)).unwrap(); // W (victim)
        b.play(at(0, 1)).unwrap(); // B
        b.play(at(8, 8)).unwrap(); // W
        b.play(at(2, 1)).unwrap(); // B
        b.play(at(8, 7)).unwrap(); // W
        let before = b.clone();
        b.play(at(1, 2)).unwrap(); // B captures
        assert_eq!(b.captures_black, 1);
        assert!(b.undo());
        assert!(b.cells == before.cells && b.turn == before.turn);
        assert_eq!(b.captures_black, 0);
        assert_eq!(b.last, Some(at(8, 7)));
        // Replay works identically after the undo.
        b.play(at(1, 2)).unwrap();
        assert_eq!(b.captures_black, 1);
    }

    #[test]
    fn last_move_marker_follows_plays() {
        let mut b = GoBoard::new();
        assert_eq!(b.last, None);
        b.play(at(4, 4)).unwrap();
        assert_eq!(b.last, Some(at(4, 4)));
        b.pass();
        assert_eq!(b.last, Some(at(4, 4)), "a pass leaves the marker in place");
    }

    #[test]
    fn two_passes_end_the_game_and_area_score_counts_territory() {
        let mut b = GoBoard::new();
        // Full walls: black column 4, white column 6. Column 5 is neutral
        // (touches both); columns 0-3 are black's, columns 7-8 white's.
        for y in 0..SIZE {
            b.play(at(4, y)).unwrap(); // Black
            b.play(at(6, y)).unwrap(); // White
        }
        b.pass(); // Black
        b.pass(); // White → over
        assert!(b.over());
        let (black, white) = b.area_score();
        // Black: 9 stones + 36 territory (cols 0-3) = 45 points = 90 half.
        assert_eq!(black, 90);
        // White: 9 stones + 18 territory (cols 7-8) = 27 points = 54 half + komi.
        assert_eq!(white, 54 + KOMI_HALF_POINTS);
    }
}
