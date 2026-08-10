//! Hexfection rules: a clone-and-jump territory game on a hexagonal board
//! (axial coordinates). Moving to an adjacent cell clones your blob; jumping
//! two cells relocates it. Either way, every enemy blob adjacent to the
//! destination converts to your color. Supports 2–12 players.

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub struct Hex {
    pub q: i32,
    pub r: i32,
}

impl Hex {
    pub fn dist(self, other: Hex) -> i32 {
        let dq = self.q - other.q;
        let dr = self.r - other.r;
        (dq.abs() + dr.abs() + (dq + dr).abs()) / 2
    }
    pub fn neighbors(self) -> [Hex; 6] {
        const D: [(i32, i32); 6] = [(1, 0), (1, -1), (0, -1), (-1, 0), (-1, 1), (0, 1)];
        D.map(|(dq, dr)| Hex { q: self.q + dq, r: self.r + dr })
    }
}

#[derive(Clone)]
pub struct HexBoard {
    pub radius: i32,
    /// Occupancy: None = empty, Some(p) = player p's blob. Indexed via `index`.
    pub cells: Vec<Option<u8>>,
    pub coords: Vec<Hex>,
    pub players: u8,
    pub turn: u8,
    /// Players eliminated or otherwise out (no blobs). Once true, stays true.
    pub out: Vec<bool>,
    /// Consecutive moves that neither converted nor grew anything (jump
    /// shuffles and skips). A futility backstop: sealed-pocket endgames can
    /// otherwise shuffle forever, since jumps never fill space.
    pub quiet: u32,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub struct HexMove {
    pub from: usize,
    pub to: usize,
}

/// A blocked cell (a hole punched by the dish editor): occupies space, is
/// never landable (`moves_for` targets empty cells only), never converts,
/// and belongs to nobody.
pub const HOLE: u8 = 254;

impl HexBoard {
    /// Board radius scales with the head count so a dozen players fit AND
    /// nobody starts inside a neighbor's turn-one kill range: starting blobs
    /// need pairwise hex distance ≥ 4 (land adjacent + jump reach = 3).
    pub fn radius_for(players: u8) -> i32 {
        match players {
            0..=6 => 4, // 61 cells, rim 24: gaps of 4+
            7 => 5,     // 91 cells, rim 30: gaps of 4+
            _ => 6,     // 127 cells, rim 36
        }
    }

    pub fn new(players: u8) -> HexBoard {
        let players = players.clamp(2, 12);
        let radius = Self::radius_for(players);
        let mut coords = Vec::new();
        for q in -radius..=radius {
            for r in (-radius).max(-q - radius)..=radius.min(-q + radius) {
                coords.push(Hex { q, r });
            }
        }
        let mut board = HexBoard {
            radius,
            cells: vec![None; coords.len()],
            coords,
            players,
            turn: 0,
            out: vec![false; players as usize],
            quiet: 0,
        };
        // Starting blobs spaced around the rim, one per player.
        let rim: Vec<usize> = (0..board.coords.len())
            .filter(|&i| board.coords[i].dist(Hex { q: 0, r: 0 }) == radius)
            .collect();
        // Order the rim by angle so seats spread evenly.
        let mut rim_sorted = rim;
        rim_sorted.sort_by(|&a, &b| {
            let pa = board.angle(a);
            let pb = board.angle(b);
            pa.partial_cmp(&pb).unwrap()
        });
        let n = rim_sorted.len();
        if players >= 10 {
            // Ten seats and up pack the rim tighter than the safe spacing,
            // so every player gets TWO blobs, interleaved evenly with each
            // player's pair half a rim apart: no single landing can reach
            // both, so nobody can be eliminated before they ever act.
            let k2 = players as usize * 2;
            for k in 0..k2 {
                let idx = rim_sorted[(k * n) / k2];
                board.cells[idx] = Some((k % players as usize) as u8);
            }
        } else {
            for p in 0..players {
                let idx = rim_sorted[(p as usize * n) / players as usize];
                board.cells[idx] = Some(p);
            }
        }
        board
    }

    /// A standard board with holes punched into it (the dish editor's
    /// output). Holes never bury a starting blob, and coordinates outside
    /// this player-count's radius are ignored.
    pub fn with_holes(players: u8, holes: &[(i32, i32)]) -> HexBoard {
        let mut board = HexBoard::new(players);
        for &(q, r) in holes {
            if let Some(i) = board.index(Hex { q, r }) {
                if board.cells[i].is_none() {
                    board.cells[i] = Some(HOLE);
                }
            }
        }
        board
    }

    fn angle(&self, i: usize) -> f64 {
        // Axial → cartesian (pointy-top) purely for seat ordering.
        let h = self.coords[i];
        let x = 3f64.sqrt() * (h.q as f64 + h.r as f64 / 2.0);
        let y = 1.5 * h.r as f64;
        y.atan2(x)
    }

    /// O(radius) arithmetic index — this is the board's hottest call (every
    /// neighbor probe in apply/exposure/bot goes through it), so no linear
    /// scan. Mirrors the generation order in `new`: q ascending, r ascending
    /// within each column.
    pub fn index(&self, h: Hex) -> Option<usize> {
        let rr = self.radius;
        if h.dist(Hex { q: 0, r: 0 }) > rr {
            return None;
        }
        let mut offset = 0usize;
        for q in -rr..h.q {
            offset += (2 * rr + 1 - q.abs()) as usize;
        }
        let r_min = (-rr).max(-h.q - rr);
        Some(offset + (h.r - r_min) as usize)
    }

    pub fn count(&self, p: u8) -> usize {
        self.cells.iter().filter(|&&c| c == Some(p)).count()
    }

    pub fn moves_for(&self, p: u8) -> Vec<HexMove> {
        let mut out = Vec::new();
        for (i, &cell) in self.cells.iter().enumerate() {
            if cell != Some(p) {
                continue;
            }
            for (j, &target) in self.cells.iter().enumerate() {
                if target.is_none() && self.coords[i].dist(self.coords[j]) <= 2 {
                    out.push(HexMove { from: i, to: j });
                }
            }
        }
        out
    }

    /// Applies a move for the current player; returns the indices of every
    /// converted enemy cell (for the UI's conversion pulses).
    /// The caller must pass a move from `moves_for(self.turn)`.
    pub fn apply(&mut self, m: HexMove) -> Vec<usize> {
        let p = self.turn;
        let d = self.coords[m.from].dist(self.coords[m.to]);
        debug_assert!(self.cells[m.from] == Some(p) && self.cells[m.to].is_none() && d <= 2);
        if d == 2 {
            self.cells[m.from] = None; // jump: vacate the origin
        }
        self.cells[m.to] = Some(p);
        let mut converted = Vec::new();
        for n in self.coords[m.to].neighbors() {
            if let Some(idx) = self.index(n) {
                if let Some(owner) = self.cells[idx] {
                    if owner != p && owner != HOLE {
                        self.cells[idx] = Some(p);
                        converted.push(idx);
                    }
                }
            }
        }
        // A clone grows the swarm; a conversion changes ownership. A jump
        // that does neither is a shuffle — count it toward futility.
        if d == 2 && converted.is_empty() {
            self.quiet += 1;
        } else {
            self.quiet = 0;
        }
        self.advance_turn();
        converted
    }

    /// Skips the current player (used when they have no legal moves).
    pub fn skip(&mut self) {
        self.quiet += 1;
        self.advance_turn();
    }

    fn advance_turn(&mut self) {
        for p in 0..self.players {
            if self.count(p) == 0 {
                self.out[p as usize] = true;
            }
        }
        for _ in 0..self.players {
            self.turn = (self.turn + 1) % self.players;
            if !self.out[self.turn as usize] {
                return;
            }
        }
    }

    /// After this many consecutive no-progress moves the game is called on
    /// standings — the sealed-pocket stalemate backstop.
    pub const FUTILITY_LIMIT: u32 = 40;

    /// The game ends when nobody can move (board full, or every surviving
    /// player is blocked), only one player remains, or play has gone
    /// FUTILITY_LIMIT moves without a conversion or a clone.
    pub fn over(&self) -> bool {
        let alive = (0..self.players).filter(|&p| !self.out[p as usize]).count();
        if alive <= 1 {
            return true;
        }
        if self.quiet >= Self::FUTILITY_LIMIT {
            return true;
        }
        (0..self.players)
            .all(|p| self.out[p as usize] || self.moves_for(p).is_empty())
    }

    /// Final standing: players sorted by blob count, best first.
    pub fn standings(&self) -> Vec<(u8, usize)> {
        let mut v: Vec<(u8, usize)> = (0..self.players).map(|p| (p, self.count(p))).collect();
        v.sort_by(|a, b| b.1.cmp(&a.1));
        v
    }

    /// How exposed player `p`'s blobs are: for every empty cell an enemy can
    /// reach (within jump range of any enemy blob), count the `p` blobs it
    /// touches, and return the worst single landing. An approximation of the
    /// opponent's best immediate counter-conversion.
    fn exposure(&self, p: u8) -> i32 {
        let mut worst = 0;
        for (e, &cell) in self.cells.iter().enumerate() {
            if cell.is_some() {
                continue;
            }
            let mine_adjacent = self.coords[e]
                .neighbors()
                .iter()
                .filter_map(|&n| self.index(n))
                .filter(|&i| self.cells[i] == Some(p))
                .count() as i32;
            if mine_adjacent <= worst {
                continue;
            }
            let enemy_reaches = self.cells.iter().enumerate().any(|(i, &c)| {
                matches!(c, Some(o) if o != p && o != HOLE)
                    && self.coords[i].dist(self.coords[e]) <= 2
            });
            if enemy_reaches {
                worst = mine_adjacent;
            }
        }
        worst
    }

    /// Heuristic value of one move for `p` — the bot's scoring key.
    fn move_score(&self, p: u8, m: HexMove) -> i64 {
        let gain: i64 = self.coords[m.to]
            .neighbors()
            .iter()
            .filter_map(|&n| self.index(n))
            .filter(|&i| matches!(self.cells[i], Some(o) if o != p && o != HOLE))
            .count() as i64;
        let clone_bonus = if self.coords[m.from].dist(self.coords[m.to]) == 1 { 1 } else { 0 };
        let mut sim = self.clone();
        sim.apply(m);
        let risk = sim.exposure(p) as i64;
        let centrality = -self.coords[m.to].dist(Hex { q: 0, r: 0 }) as i64;
        (gain * 4 + clone_bonus * 2 - risk * 3) * 100 + centrality
    }

    /// The bot move: maximize immediate conversions, prefer cloning over
    /// jumping (clones grow the swarm), and avoid landings that hand the
    /// next player a fat counter-conversion. Ties break toward the center.
    pub fn bot_move(&self, p: u8) -> Option<HexMove> {
        let moves = self.moves_for(p);
        moves.into_iter().max_by_key(|&m| self.move_score(p, m))
    }

    /// [`bot_move`] with variety: samples among moves scoring within one
    /// conversion (400) of the best, keyed by `seed`. Unlike a uniform roll
    /// over ALL legal moves, this never volunteers a reckless landing just
    /// to look unpredictable.
    pub fn bot_move_seeded(&self, p: u8, seed: u64) -> Option<HexMove> {
        let moves = self.moves_for(p);
        if moves.is_empty() {
            return None;
        }
        let scored: Vec<(HexMove, i64)> = moves.iter().map(|&m| (m, self.move_score(p, m))).collect();
        let best = scored.iter().map(|&(_, s)| s).max().expect("non-empty");
        let good: Vec<HexMove> =
            scored.into_iter().filter(|&(_, s)| s >= best - 400).map(|(m, _)| m).collect();
        let mut x = seed ^ 0x9E37_79B9_7F4A_7C15;
        x ^= x >> 33;
        x = x.wrapping_mul(0xFF51_AFD7_ED55_8CCD);
        Some(good[(x % good.len() as u64) as usize])
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn board_sizes_match_radius() {
        assert_eq!(HexBoard::new(2).cells.len(), 61); // radius 4
        assert_eq!(HexBoard::new(7).cells.len(), 91); // radius 5
        assert_eq!(HexBoard::new(12).cells.len(), 127); // radius 6
    }

    #[test]
    fn every_player_gets_a_starting_presence() {
        for n in 2..=12u8 {
            let b = HexBoard::new(n);
            let expected = if n >= 10 { 2 } else { 1 };
            for p in 0..n {
                assert_eq!(b.count(p), expected, "{n} players, player {p}");
            }
        }
    }

    #[test]
    fn no_turn_one_elimination_is_possible() {
        // Either starts are spaced beyond kill range (land adjacent + jump
        // reach = pairwise distance 3), or players hold a second blob a
        // single landing cannot also reach.
        for n in 2..=12u8 {
            let b = HexBoard::new(n);
            if n >= 10 {
                // Two blobs per player, far apart: verify no empty landing
                // touches both of any one player's blobs.
                for p in 0..n {
                    let blobs: Vec<usize> =
                        (0..b.cells.len()).filter(|&i| b.cells[i] == Some(p)).collect();
                    assert_eq!(blobs.len(), 2);
                    assert!(
                        b.coords[blobs[0]].dist(b.coords[blobs[1]]) > 2,
                        "{n} players: player {p}'s blobs are one-landing adjacent"
                    );
                }
            } else {
                for i in 0..b.cells.len() {
                    for j in 0..b.cells.len() {
                        if i != j && b.cells[i].is_some() && b.cells[j].is_some() {
                            assert!(
                                b.coords[i].dist(b.coords[j]) >= 4,
                                "{n} players: starts {i}/{j} within turn-one kill range"
                            );
                        }
                    }
                }
            }
        }
    }

    #[test]
    fn futility_counter_calls_sealed_endgames() {
        let mut b = HexBoard::new(2);
        // Jump shuffles with no conversions tick the counter up to the call.
        for _ in 0..HexBoard::FUTILITY_LIMIT {
            assert!(!b.over(), "called too early at quiet={}", b.quiet);
            let p = b.turn;
            let jump = b
                .moves_for(p)
                .into_iter()
                .find(|m| {
                    b.coords[m.from].dist(b.coords[m.to]) == 2
                        && b.coords[m.to].neighbors().iter().all(|&h| {
                            b.index(h).map(|i| b.cells[i].is_none() || b.cells[i] == Some(p)).unwrap_or(true)
                        })
                })
                .expect("a quiet jump exists on an open board");
            b.apply(jump);
        }
        assert!(b.over(), "futility limit must end the shuffle");
        // A real clone resets the counter.
        let mut c = HexBoard::new(2);
        c.quiet = HexBoard::FUTILITY_LIMIT - 1;
        let clone = c
            .moves_for(c.turn)
            .into_iter()
            .find(|m| c.coords[m.from].dist(c.coords[m.to]) == 1)
            .unwrap();
        c.apply(clone);
        assert_eq!(c.quiet, 0);
    }

    #[test]
    fn arithmetic_index_matches_generation_order() {
        for players in [2u8, 7, 12] {
            let b = HexBoard::new(players);
            for (i, &h) in b.coords.iter().enumerate() {
                assert_eq!(b.index(h), Some(i), "radius {} coord {:?}", b.radius, h);
            }
            assert_eq!(b.index(Hex { q: b.radius + 1, r: 0 }), None);
        }
    }

    #[test]
    fn holes_block_landings_and_never_convert_or_bury_starts() {
        // Punch out the center and a ring cell; verify nobody can land
        // there, conversions skip it, and start blobs survive the punch.
        let start_cells: Vec<(i32, i32)> = {
            let b = HexBoard::new(2);
            (0..b.cells.len())
                .filter(|&i| b.cells[i].is_some())
                .map(|i| (b.coords[i].q, b.coords[i].r))
                .collect()
        };
        let mut holes = vec![(0, 0)];
        holes.extend(start_cells.iter().copied()); // trying to bury starts…
        let b = HexBoard::with_holes(2, &holes);
        for p in 0..2 {
            assert_eq!(b.count(p), 1, "start blob for {p} must survive");
        }
        let center = b.index(Hex { q: 0, r: 0 }).unwrap();
        assert_eq!(b.cells[center], Some(HOLE));
        for p in 0..2 {
            assert!(
                b.moves_for(p).iter().all(|m| m.to != center),
                "no landing on a hole"
            );
        }
        // A landing adjacent to the hole converts nothing there.
        let mut c = HexBoard::with_holes(2, &[(1, 0)]);
        for cell in c.cells.iter_mut() {
            if *cell != Some(HOLE) {
                *cell = None;
            }
        }
        let src = c.index(Hex { q: -1, r: 0 }).unwrap();
        let center = c.index(Hex { q: 0, r: 0 }).unwrap();
        c.cells[src] = Some(0);
        c.turn = 0;
        c.out = vec![false, false];
        let converted = c.apply(HexMove { from: src, to: center });
        assert!(converted.is_empty(), "holes never convert");
        let hole = c.index(Hex { q: 1, r: 0 }).unwrap();
        assert_eq!(c.cells[hole], Some(HOLE), "the hole is untouched");
    }

    #[test]
    fn seeded_bot_varies_but_never_picks_junk() {
        let b = HexBoard::new(2);
        let best = b.bot_move(0).unwrap();
        let best_score = b.move_score(0, best);
        for seed in 0..24u64 {
            let m = b.bot_move_seeded(0, seed).unwrap();
            assert!(
                b.move_score(0, m) >= best_score - 400,
                "seed {seed} picked a move more than one conversion below best"
            );
        }
    }

    #[test]
    fn clone_keeps_origin_jump_vacates_it() {
        let mut b = HexBoard::new(2);
        let from = b.cells.iter().position(|&c| c == Some(0)).unwrap();
        let clone = b
            .moves_for(0)
            .into_iter()
            .find(|m| b.coords[m.from].dist(b.coords[m.to]) == 1)
            .unwrap();
        let mut b2 = b.clone();
        b2.apply(clone);
        assert_eq!(b2.cells[from], Some(0), "clone keeps the origin blob");
        assert_eq!(b2.count(0), 2);

        let jump = b
            .moves_for(0)
            .into_iter()
            .find(|m| b.coords[m.from].dist(b.coords[m.to]) == 2)
            .unwrap();
        b.apply(jump);
        assert_eq!(b.cells[from], None, "jump vacates the origin");
        assert_eq!(b.count(0), 1);
    }

    #[test]
    fn landing_converts_adjacent_enemies() {
        let mut b = HexBoard::new(2);
        // Clear the board and stage a conversion by hand.
        for c in b.cells.iter_mut() {
            *c = None;
        }
        let center = b.index(Hex { q: 0, r: 0 }).unwrap();
        let adj = b.index(Hex { q: 1, r: 0 }).unwrap();
        let src = b.index(Hex { q: -1, r: 0 }).unwrap();
        b.cells[adj] = Some(1); // enemy next to the center
        b.cells[src] = Some(0); // our blob one step away
        b.turn = 0;
        let converted = b.apply(HexMove { from: src, to: center });
        assert_eq!(converted, vec![adj]);
        assert_eq!(b.cells[adj], Some(0), "adjacent enemy converts");
    }

    #[test]
    fn eliminated_players_lose_their_turn_permanently() {
        let mut b = HexBoard::new(3);
        // Wipe player 1 off the board.
        for c in b.cells.iter_mut() {
            if *c == Some(1) {
                *c = None;
            }
        }
        b.turn = 0;
        let m = b.moves_for(0)[0];
        b.apply(m);
        assert!(b.out[1]);
        assert_eq!(b.turn, 2, "turn skips the eliminated player");
    }

    #[test]
    fn bot_declines_a_reckless_landing() {
        // No conversion is available anywhere (the enemy wall is out of
        // reach), so every move gains zero. Eastward landings sit inside the
        // enemy's counter-jump range; westward ones are safe. Without the
        // exposure term the centrality tie-break walks the bot straight
        // toward the wall — with it, the bot must stay west.
        let mut b = HexBoard::new(2);
        for c in b.cells.iter_mut() {
            *c = None;
        }
        b.turn = 0;
        b.out = vec![false, false];
        let mine = b.index(Hex { q: -1, r: 0 }).unwrap();
        let wall = [
            b.index(Hex { q: 3, r: 0 }).unwrap(),
            b.index(Hex { q: 4, r: -1 }).unwrap(),
            b.index(Hex { q: 4, r: 0 }).unwrap(),
        ];
        b.cells[mine] = Some(0);
        for w in wall {
            b.cells[w] = Some(1);
        }
        let m = b.bot_move(0).expect("moves exist");
        let dest = b.coords[m.to];
        // The landing is safe iff no empty cell adjacent to it can be reached
        // by an enemy jump (dist ≤ 2 from any enemy blob).
        let exposed = dest.neighbors().iter().any(|&n| {
            b.index(n).is_some_and(|ni| {
                b.cells[ni].is_none()
                    && b.cells.iter().enumerate().any(|(ei, &c)| {
                        c == Some(1) && b.coords[ei].dist(n) <= 2
                    })
            })
        });
        assert!(!exposed, "bot landed in counter-jump range at {:?}", dest);
    }

    #[test]
    fn full_board_is_over_and_standings_sort_by_count() {
        let mut b = HexBoard::new(2);
        for (i, c) in b.cells.iter_mut().enumerate() {
            *c = Some((i % 3 == 0) as u8); // ~1/3 player 1, ~2/3 player 0
        }
        assert!(b.over());
        let s = b.standings();
        assert_eq!(s[0].0, 0);
        assert!(s[0].1 > s[1].1);
    }
}
