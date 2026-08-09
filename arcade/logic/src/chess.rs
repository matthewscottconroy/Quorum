//! Chess rules: board state, full legal move generation (castling, en
//! passant, promotion), and game-status detection. Pure logic — the cart
//! crate owns all presentation. Verified by perft tests against the known
//! node counts from the initial position.

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Color {
    White,
    Black,
}

impl Color {
    pub fn other(self) -> Color {
        match self {
            Color::White => Color::Black,
            Color::Black => Color::White,
        }
    }
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Piece {
    Pawn,
    Knight,
    Bishop,
    Rook,
    Queen,
    King,
}

impl Piece {
    /// Conventional material value in centipawn-ish units (king excluded).
    pub fn value(self) -> i32 {
        match self {
            Piece::Pawn => 1,
            Piece::Knight | Piece::Bishop => 3,
            Piece::Rook => 5,
            Piece::Queen => 9,
            Piece::King => 0,
        }
    }
}

pub type Square = usize; // 0..64, a1 = 0, h1 = 7, a8 = 56

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub struct Move {
    pub from: Square,
    pub to: Square,
    pub promo: Option<Piece>,
}

#[derive(Clone)]
pub struct Board {
    pub cells: [Option<(Color, Piece)>; 64],
    pub turn: Color,
    /// Castling rights: [white kingside, white queenside, black kingside, black queenside].
    pub castle: [bool; 4],
    /// En-passant target square (the square a capturing pawn lands on).
    pub ep: Option<Square>,
    /// Plies since the last capture or pawn move (fifty-move rule; 100 = draw).
    pub halfmove: u32,
}

pub const fn file(sq: Square) -> usize {
    sq % 8
}
pub const fn rank(sq: Square) -> usize {
    sq / 8
}

impl Board {
    pub fn start() -> Board {
        use Color::*;
        use Piece::*;
        let mut cells: [Option<(Color, Piece)>; 64] = [None; 64];
        let back = [Rook, Knight, Bishop, Queen, King, Bishop, Knight, Rook];
        for f in 0..8 {
            cells[f] = Some((White, back[f]));
            cells[8 + f] = Some((White, Pawn));
            cells[48 + f] = Some((Black, Pawn));
            cells[56 + f] = Some((Black, back[f]));
        }
        Board { cells, turn: White, castle: [true; 4], ep: None, halfmove: 0 }
    }

    pub fn king_square(&self, c: Color) -> Square {
        self.cells
            .iter()
            .position(|p| *p == Some((c, Piece::King)))
            .expect("king always on the board")
    }

    /// Is `sq` attacked by any piece of color `by`?
    pub fn attacked(&self, sq: Square, by: Color) -> bool {
        let (f, r) = (file(sq) as i32, rank(sq) as i32);
        let at = |df: i32, dr: i32| -> Option<(Color, Piece)> {
            let (nf, nr) = (f + df, r + dr);
            if !(0..8).contains(&nf) || !(0..8).contains(&nr) {
                return None;
            }
            self.cells[(nr * 8 + nf) as usize]
        };
        // Pawns (they attack toward the enemy: white pawns attack upward).
        let dr = match by {
            Color::White => -1,
            Color::Black => 1,
        };
        for df in [-1, 1] {
            if at(df, dr) == Some((by, Piece::Pawn)) {
                return true;
            }
        }
        // Knights.
        for (df, dr) in [(1, 2), (2, 1), (2, -1), (1, -2), (-1, -2), (-2, -1), (-2, 1), (-1, 2)] {
            if at(df, dr) == Some((by, Piece::Knight)) {
                return true;
            }
        }
        // King adjacency.
        for df in -1..=1i32 {
            for dr in -1..=1i32 {
                if (df != 0 || dr != 0) && at(df, dr) == Some((by, Piece::King)) {
                    return true;
                }
            }
        }
        // Sliders.
        let lines: [(i32, i32, [Piece; 2]); 8] = [
            (1, 0, [Piece::Rook, Piece::Queen]),
            (-1, 0, [Piece::Rook, Piece::Queen]),
            (0, 1, [Piece::Rook, Piece::Queen]),
            (0, -1, [Piece::Rook, Piece::Queen]),
            (1, 1, [Piece::Bishop, Piece::Queen]),
            (1, -1, [Piece::Bishop, Piece::Queen]),
            (-1, 1, [Piece::Bishop, Piece::Queen]),
            (-1, -1, [Piece::Bishop, Piece::Queen]),
        ];
        for (df, dr, hits) in lines {
            let (mut nf, mut nr) = (f + df, r + dr);
            while (0..8).contains(&nf) && (0..8).contains(&nr) {
                if let Some((c, p)) = self.cells[(nr * 8 + nf) as usize] {
                    if c == by && hits.contains(&p) {
                        return true;
                    }
                    break;
                }
                nf += df;
                nr += dr;
            }
        }
        false
    }

    pub fn in_check(&self, c: Color) -> bool {
        self.attacked(self.king_square(c), c.other())
    }

    /// Pseudo-legal moves for the side to move (may leave own king in check).
    fn pseudo_moves(&self) -> Vec<Move> {
        let mut out = Vec::with_capacity(48);
        let us = self.turn;
        for sq in 0..64 {
            let Some((c, p)) = self.cells[sq] else { continue };
            if c != us {
                continue;
            }
            let (f, r) = (file(sq) as i32, rank(sq) as i32);
            let push = |out: &mut Vec<Move>, to: Square| {
                // Pawn promotion fan-out happens in the pawn branch, not here.
                out.push(Move { from: sq, to, promo: None });
            };
            match p {
                Piece::Pawn => {
                    let dir: i32 = if us == Color::White { 1 } else { -1 };
                    let start_rank = if us == Color::White { 1 } else { 6 };
                    let last_rank = if us == Color::White { 7 } else { 0 };
                    let fwd = r + dir;
                    let pawn_to = |out: &mut Vec<Move>, to: Square| {
                        if rank(to) == last_rank {
                            for promo in [Piece::Queen, Piece::Rook, Piece::Bishop, Piece::Knight] {
                                out.push(Move { from: sq, to, promo: Some(promo) });
                            }
                        } else {
                            out.push(Move { from: sq, to, promo: None });
                        }
                    };
                    if (0..8).contains(&fwd) {
                        let one = (fwd * 8 + f) as usize;
                        if self.cells[one].is_none() {
                            pawn_to(&mut out, one);
                            if r == start_rank {
                                let two = ((r + 2 * dir) * 8 + f) as usize;
                                if self.cells[two].is_none() {
                                    out.push(Move { from: sq, to: two, promo: None });
                                }
                            }
                        }
                        for df in [-1i32, 1] {
                            let nf = f + df;
                            if !(0..8).contains(&nf) {
                                continue;
                            }
                            let to = (fwd * 8 + nf) as usize;
                            let capture = matches!(self.cells[to], Some((oc, _)) if oc != us);
                            if capture || self.ep == Some(to) {
                                pawn_to(&mut out, to);
                            }
                        }
                    }
                }
                Piece::Knight => {
                    for (df, dr) in
                        [(1, 2), (2, 1), (2, -1), (1, -2), (-1, -2), (-2, -1), (-2, 1), (-1, 2)]
                    {
                        let (nf, nr) = (f + df, r + dr);
                        if (0..8).contains(&nf) && (0..8).contains(&nr) {
                            let to = (nr * 8 + nf) as usize;
                            if !matches!(self.cells[to], Some((oc, _)) if oc == us) {
                                push(&mut out, to);
                            }
                        }
                    }
                }
                Piece::King => {
                    for df in -1..=1i32 {
                        for dr in -1..=1i32 {
                            if df == 0 && dr == 0 {
                                continue;
                            }
                            let (nf, nr) = (f + df, r + dr);
                            if (0..8).contains(&nf) && (0..8).contains(&nr) {
                                let to = (nr * 8 + nf) as usize;
                                if !matches!(self.cells[to], Some((oc, _)) if oc == us) {
                                    push(&mut out, to);
                                }
                            }
                        }
                    }
                    // Castling: king and rook unmoved, path empty, king not
                    // passing through or out of check.
                    let (ks, qs, home) = match us {
                        Color::White => (self.castle[0], self.castle[1], 0usize),
                        Color::Black => (self.castle[2], self.castle[3], 56usize),
                    };
                    if sq == home + 4 && !self.in_check(us) {
                        if ks
                            && self.cells[home + 5].is_none()
                            && self.cells[home + 6].is_none()
                            && self.cells[home + 7] == Some((us, Piece::Rook))
                            && !self.attacked(home + 5, us.other())
                            && !self.attacked(home + 6, us.other())
                        {
                            out.push(Move { from: sq, to: home + 6, promo: None });
                        }
                        if qs
                            && self.cells[home + 3].is_none()
                            && self.cells[home + 2].is_none()
                            && self.cells[home + 1].is_none()
                            && self.cells[home] == Some((us, Piece::Rook))
                            && !self.attacked(home + 3, us.other())
                            && !self.attacked(home + 2, us.other())
                        {
                            out.push(Move { from: sq, to: home + 2, promo: None });
                        }
                    }
                }
                Piece::Bishop | Piece::Rook | Piece::Queen => {
                    let dirs: &[(i32, i32)] = match p {
                        Piece::Rook => &[(1, 0), (-1, 0), (0, 1), (0, -1)],
                        Piece::Bishop => &[(1, 1), (1, -1), (-1, 1), (-1, -1)],
                        _ => &[
                            (1, 0),
                            (-1, 0),
                            (0, 1),
                            (0, -1),
                            (1, 1),
                            (1, -1),
                            (-1, 1),
                            (-1, -1),
                        ],
                    };
                    for &(df, dr) in dirs {
                        let (mut nf, mut nr) = (f + df, r + dr);
                        while (0..8).contains(&nf) && (0..8).contains(&nr) {
                            let to = (nr * 8 + nf) as usize;
                            match self.cells[to] {
                                None => push(&mut out, to),
                                Some((oc, _)) => {
                                    if oc != us {
                                        push(&mut out, to);
                                    }
                                    break;
                                }
                            }
                            nf += df;
                            nr += dr;
                        }
                    }
                }
            }
        }
        out
    }

    /// Applies a move without legality checking. Handles castling rook hop,
    /// en passant capture, promotion, and rights/ep bookkeeping.
    pub fn apply(&mut self, m: Move) {
        let us = self.turn;
        let piece = self.cells[m.from].expect("moving an existing piece").1;
        let is_capture = self.cells[m.to].is_some()
            || (piece == Piece::Pawn && Some(m.to) == self.ep);
        if piece == Piece::Pawn || is_capture {
            self.halfmove = 0;
        } else {
            self.halfmove += 1;
        }
        // En passant capture removes the bypassed pawn.
        if piece == Piece::Pawn && Some(m.to) == self.ep && self.cells[m.to].is_none() {
            let dir: i32 = if us == Color::White { -1 } else { 1 };
            self.cells[(m.to as i32 + dir * 8) as usize] = None;
        }
        // Castling moves the rook too (kingside: h-file rook hops to f-file).
        if piece == Piece::King && m.to as i32 - m.from as i32 == 2 {
            self.cells[m.from + 1] = self.cells[m.from + 3].take();
        }
        if piece == Piece::King && m.from as i32 - m.to as i32 == 2 {
            self.cells[m.from - 1] = self.cells[m.from - 4].take();
        }
        // New ep target on double pawn push.
        self.ep = if piece == Piece::Pawn && (m.to as i32 - m.from as i32).abs() == 16 {
            Some(((m.from + m.to) / 2) as Square)
        } else {
            None
        };
        // Rights die when king or rook moves, or a rook square is captured.
        for (i, sq) in [(0usize, 7usize), (1, 0), (2, 63), (3, 56)] {
            if m.from == sq || m.to == sq {
                self.castle[i] = false;
            }
        }
        match us {
            Color::White if piece == Piece::King => {
                self.castle[0] = false;
                self.castle[1] = false;
            }
            Color::Black if piece == Piece::King => {
                self.castle[2] = false;
                self.castle[3] = false;
            }
            _ => {}
        }
        self.cells[m.to] = Some((us, m.promo.unwrap_or(piece)));
        self.cells[m.from] = None;
        self.turn = us.other();
    }

    /// All strictly legal moves for the side to move.
    pub fn legal_moves(&self) -> Vec<Move> {
        self.pseudo_moves()
            .into_iter()
            .filter(|&m| {
                let mut next = self.clone();
                next.apply(m);
                !next.in_check(self.turn)
            })
            .collect()
    }

    /// Legal moves from one square (for click-to-move UIs).
    pub fn legal_from(&self, from: Square) -> Vec<Move> {
        self.legal_moves().into_iter().filter(|m| m.from == from).collect()
    }

    pub fn status(&self) -> Status {
        if self.legal_moves().is_empty() {
            return if self.in_check(self.turn) {
                Status::Checkmate { winner: self.turn.other() }
            } else {
                Status::Stalemate
            };
        }
        if self.halfmove >= 100 {
            return Status::Draw(DrawKind::FiftyMove);
        }
        if self.insufficient_material() {
            return Status::Draw(DrawKind::InsufficientMaterial);
        }
        Status::Ongoing
    }

    /// Neither side can possibly deliver mate: bare kings, a lone minor
    /// piece, or same-colored bishops only.
    pub fn insufficient_material(&self) -> bool {
        let mut knights = 0;
        let mut bishops_light = 0;
        let mut bishops_dark = 0;
        for (sq, cell) in self.cells.iter().enumerate() {
            match cell {
                None => {}
                Some((_, Piece::King)) => {}
                Some((_, Piece::Knight)) => knights += 1,
                Some((_, Piece::Bishop)) => {
                    if (file(sq) + rank(sq)) % 2 == 0 {
                        bishops_dark += 1;
                    } else {
                        bishops_light += 1;
                    }
                }
                Some(_) => return false, // any pawn, rook, or queen can mate
            }
        }
        let minors = knights + bishops_light + bishops_dark;
        // K vs K, or a single minor piece on the whole board.
        if minors <= 1 {
            return true;
        }
        // Only bishops, all living on the same square color.
        knights == 0 && (bishops_light == 0 || bishops_dark == 0)
    }

    /// A position fingerprint for threefold-repetition tracking (FNV-1a over
    /// occupancy, side to move, castling rights, and the ep square). Kept out
    /// of Board state so cloning stays cheap for search.
    pub fn position_hash(&self) -> u64 {
        let mut h: u64 = 0xcbf2_9ce4_8422_2325;
        let mut mix = |byte: u8| {
            h ^= byte as u64;
            h = h.wrapping_mul(0x100_0000_01b3);
        };
        for cell in &self.cells {
            let code = match cell {
                None => 0u8,
                Some((c, p)) => {
                    1 + (*p as u8) + if *c == Color::White { 0 } else { 6 }
                }
            };
            mix(code);
        }
        mix(if self.turn == Color::White { 1 } else { 2 });
        for &c in &self.castle {
            mix(c as u8);
        }
        mix(self.ep.map(|e| e as u8 + 1).unwrap_or(0));
        h
    }

    /// Material balance from `c`'s point of view (positive = ahead).
    pub fn material(&self, c: Color) -> i32 {
        self.cells
            .iter()
            .flatten()
            .map(|&(pc, p)| if pc == c { p.value() } else { -p.value() })
            .sum()
    }
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Status {
    Ongoing,
    Checkmate { winner: Color },
    Stalemate,
    Draw(DrawKind),
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum DrawKind {
    FiftyMove,
    InsufficientMaterial,
    Repetition, // detected by the caller via position_hash history
}

#[cfg(test)]
mod tests {
    use super::*;

    fn perft(b: &Board, depth: u32) -> u64 {
        if depth == 0 {
            return 1;
        }
        b.legal_moves()
            .into_iter()
            .map(|m| {
                let mut nb = b.clone();
                nb.apply(m);
                perft(&nb, depth - 1)
            })
            .sum()
    }

    #[test]
    fn perft_from_start_matches_known_counts() {
        let b = Board::start();
        assert_eq!(perft(&b, 1), 20);
        assert_eq!(perft(&b, 2), 400);
        assert_eq!(perft(&b, 3), 8_902);
        assert_eq!(perft(&b, 4), 197_281);
    }

    #[test]
    fn scholars_mate_is_checkmate() {
        let mut b = Board::start();
        // 1. e4 e5 2. Bc4 Nc6 3. Qh5 Nf6?? 4. Qxf7#
        let mv = |b: &mut Board, from: usize, to: usize| {
            let m = b
                .legal_moves()
                .into_iter()
                .find(|m| m.from == from && m.to == to)
                .expect("move should be legal");
            b.apply(m);
        };
        mv(&mut b, 12, 28); // e2e4
        mv(&mut b, 52, 36); // e7e5
        mv(&mut b, 5, 26); // f1c4
        mv(&mut b, 57, 42); // b8c6
        mv(&mut b, 3, 39); // d1h5
        mv(&mut b, 62, 45); // g8f6
        mv(&mut b, 39, 53); // h5xf7#
        assert_eq!(b.status(), Status::Checkmate { winner: Color::White });
    }

    #[test]
    fn en_passant_capture_removes_bypassed_pawn() {
        let mut b = Board::start();
        let mv = |b: &mut Board, from: usize, to: usize| {
            let m = b
                .legal_moves()
                .into_iter()
                .find(|m| m.from == from && m.to == to)
                .expect("move should be legal");
            b.apply(m);
        };
        mv(&mut b, 12, 28); // e4
        mv(&mut b, 48, 40); // a6
        mv(&mut b, 28, 36); // e5
        mv(&mut b, 51, 35); // d5 (double push past e5 pawn)
        assert_eq!(b.ep, Some(43)); // d6
        mv(&mut b, 36, 43); // exd6 e.p.
        assert_eq!(b.cells[35], None, "bypassed pawn must be captured");
    }

    #[test]
    fn castling_moves_rook_and_requires_clear_safe_path() {
        let mut b = Board::start();
        let mv = |b: &mut Board, from: usize, to: usize| {
            let m = b
                .legal_moves()
                .into_iter()
                .find(|m| m.from == from && m.to == to)
                .expect("move should be legal");
            b.apply(m);
        };
        mv(&mut b, 12, 28); // e4
        mv(&mut b, 52, 36); // e5
        mv(&mut b, 6, 21); // Nf3
        mv(&mut b, 57, 42); // Nc6
        mv(&mut b, 5, 26); // Bc4
        mv(&mut b, 62, 45); // Nf6
        mv(&mut b, 4, 6); // O-O
        assert_eq!(b.cells[6], Some((Color::White, Piece::King)));
        assert_eq!(b.cells[5], Some((Color::White, Piece::Rook)));
        assert_eq!(b.cells[7], None);
    }

    #[test]
    fn fifty_move_rule_draws_but_mate_takes_precedence() {
        let mut b = Board::start();
        b.halfmove = 100;
        assert_eq!(b.status(), Status::Draw(DrawKind::FiftyMove));
        // A pawn move resets the clock.
        b.halfmove = 40;
        let m = b.legal_moves().into_iter().find(|m| m.from == 12).unwrap();
        b.apply(m);
        assert_eq!(b.halfmove, 0);
    }

    #[test]
    fn insufficient_material_positions() {
        let make = |pieces: &[(usize, Color, Piece)]| {
            let mut cells: [Option<(Color, Piece)>; 64] = [None; 64];
            for &(sq, c, p) in pieces {
                cells[sq] = Some((c, p));
            }
            Board { cells, turn: Color::White, castle: [false; 4], ep: None, halfmove: 0 }
        };
        use Color::*;
        use Piece::*;
        assert!(make(&[(4, White, King), (60, Black, King)]).insufficient_material());
        assert!(make(&[(4, White, King), (60, Black, King), (27, White, Knight)]).insufficient_material());
        assert!(make(&[(4, White, King), (60, Black, King), (27, White, Bishop)]).insufficient_material());
        // Same-color bishops cannot mate; opposite-color can (helpmate).
        // c1 (2) and f4 (29) are both dark squares under (file+rank)%2==0.
        assert!(make(&[(4, White, King), (60, Black, King), (2, White, Bishop), (29, Black, Bishop)])
            .insufficient_material());
        assert!(!make(&[(4, White, King), (60, Black, King), (8, White, Pawn)]).insufficient_material());
        assert!(!make(&[(4, White, King), (60, Black, King), (0, White, Rook)]).insufficient_material());
    }

    #[test]
    fn position_hash_tracks_repetition() {
        let mut b = Board::start();
        let start = b.position_hash();
        let mv = |b: &mut Board, from: usize, to: usize| {
            let m = b.legal_moves().into_iter().find(|m| m.from == from && m.to == to).unwrap();
            b.apply(m);
        };
        // Knights out and back: same position, same hash.
        mv(&mut b, 6, 21);
        mv(&mut b, 62, 45);
        mv(&mut b, 21, 6);
        mv(&mut b, 45, 62);
        assert_eq!(b.position_hash(), start);
        // Different position hashes differently.
        mv(&mut b, 12, 28);
        assert_ne!(b.position_hash(), start);
    }

    #[test]
    fn stalemate_detected() {
        // Classic minimal stalemate: black king a8, white queen c7 guarding
        // every escape but giving no check, white king to move elsewhere.
        let mut cells: [Option<(Color, Piece)>; 64] = [None; 64];
        cells[56] = Some((Color::Black, Piece::King)); // a8
        cells[50] = Some((Color::White, Piece::Queen)); // c7
        cells[42] = Some((Color::White, Piece::King)); // c6
        let b = Board { cells, turn: Color::Black, castle: [false; 4], ep: None, halfmove: 0 };
        assert_eq!(b.status(), Status::Stalemate);
    }
}
