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
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub struct HexMove {
    pub from: usize,
    pub to: usize,
}

impl HexBoard {
    /// Board radius scales with the head count so a dozen players fit.
    pub fn radius_for(players: u8) -> i32 {
        if players <= 6 {
            4 // 61 cells
        } else {
            5 // 91 cells
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
        for p in 0..players {
            let idx = rim_sorted[(p as usize * n) / players as usize];
            board.cells[idx] = Some(p);
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

    pub fn index(&self, h: Hex) -> Option<usize> {
        if h.dist(Hex { q: 0, r: 0 }) > self.radius {
            return None;
        }
        self.coords.iter().position(|&c| c == h)
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
                    if owner != p {
                        self.cells[idx] = Some(p);
                        converted.push(idx);
                    }
                }
            }
        }
        self.advance_turn();
        converted
    }

    /// Skips the current player (used when they have no legal moves).
    pub fn skip(&mut self) {
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

    /// The game ends when nobody can move (board full, or every surviving
    /// player is blocked) or only one player remains.
    pub fn over(&self) -> bool {
        let alive = (0..self.players).filter(|&p| !self.out[p as usize]).count();
        if alive <= 1 {
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
                matches!(c, Some(o) if o != p) && self.coords[i].dist(self.coords[e]) <= 2
            });
            if enemy_reaches {
                worst = mine_adjacent;
            }
        }
        worst
    }

    /// The bot move: maximize immediate conversions, prefer cloning over
    /// jumping (clones grow the swarm), and avoid landings that hand the
    /// next player a fat counter-conversion. Ties break toward the center.
    pub fn bot_move(&self, p: u8) -> Option<HexMove> {
        let moves = self.moves_for(p);
        moves.into_iter().max_by_key(|&m| {
            let gain: i32 = self.coords[m.to]
                .neighbors()
                .iter()
                .filter_map(|&n| self.index(n))
                .filter(|&i| matches!(self.cells[i], Some(o) if o != p))
                .count() as i32;
            let clone_bonus = if self.coords[m.from].dist(self.coords[m.to]) == 1 { 1 } else { 0 };
            let mut sim = self.clone();
            sim.apply(m);
            let risk = sim.exposure(p);
            let centrality = -self.coords[m.to].dist(Hex { q: 0, r: 0 });
            (gain * 4 + clone_bonus * 2 - risk * 3) * 100 + centrality
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn board_sizes_match_radius() {
        assert_eq!(HexBoard::new(2).cells.len(), 61); // radius 4
        assert_eq!(HexBoard::new(12).cells.len(), 91); // radius 5
    }

    #[test]
    fn every_player_gets_a_starting_blob() {
        for n in 2..=12u8 {
            let b = HexBoard::new(n);
            for p in 0..n {
                assert_eq!(b.count(p), 1, "{n} players, player {p}");
            }
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
