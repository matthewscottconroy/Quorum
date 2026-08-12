//! HOMESTEAD — an original hex resource-settlement game on a 19-lot office
//! park. Corner lots produce COFFEE, PAPER, TONER, SNACKS, and STAPLES when
//! their number rolls; spend them on halls, outposts, offices, and ideas.
//! First to 10 points owns the park. Solo seats you against three bots;
//! online seats three or four (the genre's sweet spot), turn by turn — the
//! acting player simulates locally and broadcasts a full state snapshot.
//!
//! Deliberate scope: bank trades only (4:1), no player-to-player trading,
//! idea deck is guards and patents. Rolling 7 wakes THE INSPECTOR.

use bevy::prelude::*;
use serde::{Deserialize, Serialize};

use crate::retro::{cursor_world, text, AMBER, CYAN, DIM, GREEN, PLAYER_COLORS, RED, WHITE};
use crate::rng::Rng;
use crate::shell::{net_send, sfx, stat};
use crate::{FinalScore, GameTag, NetIn, NetMode, Phase};

pub const BLURB: &[&str] = &[
    "SETTLE THE OFFICE PARK. FIRST TO 10 POINTS OWNS IT.",
    "R ROLLS. CLICK CORNERS AND EDGES TO BUILD. I IDEA, G GUARD, T TRADE, E END.",
    "SOLO VS THREE BOTS, OR THREE TO FOUR ONLINE.",
];

const RES_NAMES: [&str; 5] = ["COFFEE", "PAPER", "TONER", "SNACKS", "STAPLES"];
const RES_COLORS: [Color; 5] = [
    Color::srgb(0.45, 0.30, 0.15),
    Color::srgb(0.80, 0.80, 0.72),
    Color::srgb(0.30, 0.30, 0.38),
    Color::srgb(0.90, 0.55, 0.20),
    Color::srgb(0.52, 0.58, 0.64),
];
const EMPTY_LOT: u8 = 5;
const HEX_SIZE: f32 = 52.0;
// give, in resource units, indexed by RES_NAMES
const COST_HALL: [u8; 5] = [0, 1, 1, 0, 0];
const COST_OUTPOST: [u8; 5] = [1, 1, 1, 1, 0];
const COST_OFFICE: [u8; 5] = [2, 0, 0, 0, 3];
const COST_IDEA: [u8; 5] = [1, 0, 0, 1, 1];

#[derive(Clone, Serialize, Deserialize)]
struct Pl {
    seat: usize,
    human: bool,
    res: [u8; 5],
    guards: u8,  // unplayed guard cards
    played: u8,  // guards played
    patents: u8, // point cards
    alive: bool,
}

#[derive(Component)]
struct NumText(usize);

#[derive(Component)]
struct Hud;

#[derive(Component)]
struct LogText;

#[derive(Resource)]
struct Hs {
    // Geometry (identical on every client — derived, never sent).
    centers: Vec<Vec2>,
    verts: Vec<Vec2>,
    edges: Vec<(usize, usize)>,
    hex_verts: Vec<[usize; 6]>,
    vert_edges: Vec<Vec<usize>>,
    // Board.
    hex_res: [u8; 19],
    hex_num: [u8; 19],
    have_board: bool,
    // State.
    players: Vec<Pl>,
    vert_own: Vec<i8>,
    vert_city: Vec<bool>,
    edge_own: Vec<i8>,
    robber: usize,
    turn: usize,
    phase: u8, // 0 setup, 1 pre-roll, 2 inspector move, 3 build, 4 trade-give, 5 trade-get
    setup_n: u8,
    last_outpost: i16,
    dice: u8,
    guard_left: u8,
    patent_left: u8,
    trade_give: u8,
    my_idx: usize,
    net: bool,
    over: Option<Timer>,
    winner: i8,
    result: String,
    log: Vec<String>,
    dirty: bool,
    bot_t: Timer,
    // UI handles.
    hex_mats: Vec<Handle<ColorMaterial>>,
    vert_ents: Vec<Entity>,
    edge_ents: Vec<Entity>,
    robber_ent: Entity,
}

impl Hs {
    fn n(&self) -> usize {
        self.players.len()
    }
    fn placer(&self) -> usize {
        let n = self.n();
        let k = self.setup_n as usize;
        if k < n {
            k
        } else {
            (2 * n - 1).saturating_sub(k)
        }
    }
    fn actor(&self) -> usize {
        if self.phase == 0 {
            self.placer()
        } else {
            self.turn
        }
    }
    fn me_acting(&self) -> bool {
        self.over.is_none()
            && self.have_board
            && (!self.net || self.actor() == self.my_idx)
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
    hr: Vec<u8>,
    hn: Vec<u8>,
    pl: Vec<Pl>,
    vo: Vec<i8>,
    vc: Vec<u8>,
    eo: Vec<i8>,
    rob: u8,
    turn: u8,
    ph: u8,
    sn: u8,
    lo: i16,
    d: u8,
    gl: u8,
    ptl: u8,
    log: String,
    win: i8,
}

fn send_state(h: &Hs, line: &str) {
    if !h.net {
        return;
    }
    let w = WState {
        t: "st".into(),
        hr: h.hex_res.to_vec(),
        hn: h.hex_num.to_vec(),
        pl: h.players.clone(),
        vo: h.vert_own.clone(),
        vc: h.vert_city.iter().map(|&c| c as u8).collect(),
        eo: h.edge_own.clone(),
        rob: h.robber as u8,
        turn: h.turn as u8,
        ph: h.phase,
        sn: h.setup_n,
        lo: h.last_outpost,
        d: h.dice,
        gl: h.guard_left,
        ptl: h.patent_left,
        log: line.to_string(),
        win: h.winner,
    };
    if let Ok(m) = serde_json::to_string(&w) {
        net_send(&m);
    }
}

pub struct HomesteadPlugin;

impl Plugin for HomesteadPlugin {
    fn build(&self, app: &mut App) {
        app.add_systems(OnEnter(Phase::Playing), setup).add_systems(
            Update,
            (net_apply, skip_dead, input, bots, paint, endgame)
                .chain()
                .run_if(in_state(Phase::Playing))
                .run_if(crate::unpaused),
        );
    }
}

fn hex_center(q: i32, r: i32) -> Vec2 {
    let x = 3f32.sqrt() * HEX_SIZE * (q as f32 + r as f32 / 2.0);
    let y = 1.5 * HEX_SIZE * r as f32;
    Vec2::new(x, y)
}

fn corner(center: Vec2, k: usize) -> Vec2 {
    let a = (90.0 + 60.0 * k as f32).to_radians();
    center + HEX_SIZE * Vec2::new(a.cos(), a.sin())
}

#[allow(clippy::too_many_arguments)]
fn setup(
    mut commands: Commands,
    net: Res<NetMode>,
    mut meshes: ResMut<Assets<Mesh>>,
    mut mats: ResMut<Assets<ColorMaterial>>,
    mut rng: ResMut<Rng>,
) {
    // 19-lot flower, radius 2 in axial coordinates.
    let mut centers = Vec::new();
    for q in -2i32..=2 {
        for r in -2i32..=2 {
            if (q + r).abs() <= 2 {
                centers.push(hex_center(q, r));
            }
        }
    }
    // Vertices and edges, deduped by rounded position.
    let mut verts: Vec<Vec2> = Vec::new();
    let mut hex_verts: Vec<[usize; 6]> = Vec::new();
    let vid = |verts: &mut Vec<Vec2>, p: Vec2| -> usize {
        for (i, v) in verts.iter().enumerate() {
            if (*v - p).length() < 4.0 {
                return i;
            }
        }
        verts.push(p);
        verts.len() - 1
    };
    for &c in &centers {
        let mut ids = [0usize; 6];
        for (k, id) in ids.iter_mut().enumerate() {
            *id = vid(&mut verts, corner(c, k));
        }
        hex_verts.push(ids);
    }
    let mut edges: Vec<(usize, usize)> = Vec::new();
    for ids in &hex_verts {
        for k in 0..6 {
            let a = ids[k].min(ids[(k + 1) % 6]);
            let b = ids[k].max(ids[(k + 1) % 6]);
            if !edges.contains(&(a, b)) {
                edges.push((a, b));
            }
        }
    }
    let mut vert_edges = vec![Vec::new(); verts.len()];
    for (i, &(a, b)) in edges.iter().enumerate() {
        vert_edges[a].push(i);
        vert_edges[b].push(i);
    }
    // Seats.
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
            let pl = seats
                .iter()
                .map(|&s| Pl {
                    seat: s,
                    human: true,
                    res: [0; 5],
                    guards: 0,
                    played: 0,
                    patents: 0,
                    alive: true,
                })
                .collect();
            (pl, my, true)
        }
        None => {
            let pl = (0..4)
                .map(|i| Pl {
                    seat: i,
                    human: i == 0,
                    res: [0; 5],
                    guards: 0,
                    played: 0,
                    patents: 0,
                    alive: true,
                })
                .collect();
            (pl, 0, false)
        }
    };
    // The first present seat deals the board; guests wait for the snapshot.
    let deals = !is_net || my_idx == 0;
    let mut hex_res = [EMPTY_LOT; 19];
    let mut hex_num = [0u8; 19];
    let mut robber = 0usize;
    if deals {
        let mut pool: Vec<u8> = Vec::new();
        for (r, count) in [(0u8, 4), (1, 4), (3, 4), (2, 3), (4, 3), (EMPTY_LOT, 1)] {
            for _ in 0..count {
                pool.push(r);
            }
        }
        for i in (1..pool.len()).rev() {
            pool.swap(i, rng.range(i as u32 + 1) as usize);
        }
        let mut nums: Vec<u8> = vec![2, 3, 3, 4, 4, 5, 5, 6, 6, 8, 8, 9, 9, 10, 10, 11, 11, 12];
        for i in (1..nums.len()).rev() {
            nums.swap(i, rng.range(i as u32 + 1) as usize);
        }
        let mut ni = 0;
        for i in 0..19 {
            hex_res[i] = pool[i];
            if pool[i] == EMPTY_LOT {
                robber = i;
            } else {
                hex_num[i] = nums[ni];
                ni += 1;
            }
        }
    }
    // Lot meshes.
    let hexmesh = meshes.add(RegularPolygon::new(HEX_SIZE - 3.0, 6));
    let mut hex_mats = Vec::new();
    for (i, &c) in centers.iter().enumerate() {
        let color = if deals {
            lot_color(hex_res[i])
        } else {
            DIM
        };
        let mat = mats.add(ColorMaterial::from(color));
        commands.spawn((
            Mesh2d(hexmesh.clone()),
            MeshMaterial2d(mat.clone()),
            Transform::from_translation(c.extend(1.0)),
            GameTag,
        ));
        hex_mats.push(mat);
        let label = if deals && hex_num[i] > 0 { hex_num[i].to_string() } else { String::new() };
        let col = if hex_num[i] == 6 || hex_num[i] == 8 { RED } else { WHITE };
        let e = text(&mut commands, &label, 16.0, col, c.extend(5.0));
        commands.entity(e).insert((NumText(i), GameTag));
    }
    // Edge and vertex slots (invisible until built on).
    let mut edge_ents = Vec::new();
    for &(a, b) in &edges {
        let mid = (verts[a] + verts[b]) / 2.0;
        let ang = (verts[b] - verts[a]).to_angle();
        let e = commands
            .spawn((
                Sprite { color: Color::NONE, custom_size: Some(Vec2::new(36.0, 7.0)), ..default() },
                Transform::from_translation(mid.extend(2.0))
                    .with_rotation(Quat::from_rotation_z(ang)),
                GameTag,
            ))
            .id();
        edge_ents.push(e);
    }
    let mut vert_ents = Vec::new();
    for &v in &verts {
        let e = commands
            .spawn((
                Sprite { color: Color::NONE, custom_size: Some(Vec2::splat(13.0)), ..default() },
                Transform::from_translation(v.extend(3.0)),
                GameTag,
            ))
            .id();
        vert_ents.push(e);
    }
    let robber_ent = commands
        .spawn((
            Sprite {
                color: Color::srgb(0.1, 0.1, 0.12),
                custom_size: Some(Vec2::new(18.0, 24.0)),
                ..default()
            },
            Transform::from_translation((centers[robber] + Vec2::new(0.0, -16.0)).extend(4.0)),
            GameTag,
        ))
        .id();
    let hud = text(&mut commands, "", 12.0, WHITE, Vec3::new(0.0, 272.0, 6.0));
    commands.entity(hud).insert((Hud, GameTag));
    let log = text(&mut commands, "", 11.0, CYAN, Vec3::new(0.0, -262.0, 6.0));
    commands.entity(log).insert((LogText, GameTag));
    let h = Hs {
        centers,
        verts: verts.clone(),
        edges,
        hex_verts,
        vert_edges,
        hex_res,
        hex_num,
        have_board: deals,
        players,
        vert_own: vec![-1; verts.len()],
        vert_city: vec![false; verts.len()],
        edge_own: vec![-1; edge_ents.len()],
        robber,
        turn: 0,
        phase: 0,
        setup_n: 0,
        last_outpost: -1,
        dice: 0,
        guard_left: 10,
        patent_left: 5,
        trade_give: 0,
        my_idx,
        net: is_net,
        over: None,
        winner: -1,
        result: String::new(),
        log: vec!["SNAKE DRAFT: PLACE TWO OUTPOSTS EACH.".into()],
        dirty: true,
        bot_t: Timer::from_seconds(0.7, TimerMode::Repeating),
        hex_mats,
        vert_ents,
        edge_ents,
        robber_ent,
    };
    if deals && is_net {
        send_state(&h, "THE BOARD IS DEALT.");
    }
    commands.insert_resource(h);
}

fn lot_color(res: u8) -> Color {
    if res == EMPTY_LOT {
        Color::srgb(0.20, 0.22, 0.20)
    } else {
        RES_COLORS[res as usize]
    }
}

fn can_pay(p: &Pl, cost: &[u8; 5]) -> bool {
    (0..5).all(|i| p.res[i] >= cost[i])
}

fn pay(p: &mut Pl, cost: &[u8; 5]) {
    for i in 0..5 {
        p.res[i] -= cost[i];
    }
}

fn can_outpost(h: &Hs, p: usize, v: usize, setup: bool) -> bool {
    if h.vert_own[v] >= 0 {
        return false;
    }
    // Distance rule: no building on any neighboring corner.
    for &e in &h.vert_edges[v] {
        let (a, b) = h.edges[e];
        let other = if a == v { b } else { a };
        if h.vert_own[other] >= 0 {
            return false;
        }
    }
    setup || h.vert_edges[v].iter().any(|&e| h.edge_own[e] == p as i8)
}

fn can_hall(h: &Hs, p: usize, e: usize) -> bool {
    if h.edge_own[e] >= 0 {
        return false;
    }
    if h.phase == 0 {
        let lo = h.last_outpost;
        let (a, b) = h.edges[e];
        return lo >= 0 && (a == lo as usize || b == lo as usize);
    }
    let (a, b) = h.edges[e];
    for v in [a, b] {
        if h.vert_own[v] == p as i8 {
            return true;
        }
        if h.vert_own[v] < 0 && h.vert_edges[v].iter().any(|&e2| h.edge_own[e2] == p as i8) {
            return true;
        }
    }
    false
}

fn longest_dfs(h: &Hs, p: usize, v: usize, used: &mut Vec<bool>, len: usize, best: &mut usize) {
    *best = (*best).max(len);
    for &e in &h.vert_edges[v] {
        if used[e] || h.edge_own[e] != p as i8 {
            continue;
        }
        let (a, b) = h.edges[e];
        let next = if a == v { b } else { a };
        used[e] = true;
        longest_dfs(h, p, next, used, len + 1, best);
        used[e] = false;
    }
}

fn longest_hall(h: &Hs, p: usize) -> usize {
    let mut best = 0;
    let mut used = vec![false; h.edges.len()];
    for (e, &own) in h.edge_own.iter().enumerate() {
        if own != p as i8 {
            continue;
        }
        let (a, b) = h.edges[e];
        for start in [a, b] {
            longest_dfs(h, p, start, &mut used, 0, &mut best);
        }
    }
    best
}

fn vp_of(h: &Hs, p: usize) -> u32 {
    let mut vp = 0u32;
    for (v, &own) in h.vert_own.iter().enumerate() {
        if own == p as i8 {
            vp += if h.vert_city[v] { 2 } else { 1 };
        }
    }
    vp += h.players[p].patents as u32;
    // Longest hall (>=5, strictly ahead).
    let mine = longest_hall(h, p);
    if mine >= 5 && (0..h.n()).all(|o| o == p || longest_hall(h, o) < mine) {
        vp += 2;
    }
    let g = h.players[p].played;
    if g >= 3 && (0..h.n()).all(|o| o == p || h.players[o].played < g) {
        vp += 2;
    }
    vp
}

fn check_win(h: &mut Hs) {
    if h.over.is_some() || h.phase == 0 {
        return;
    }
    let p = h.turn;
    if vp_of(h, p) >= 10 {
        h.winner = p as i8;
        finish(h);
    }
}

fn finish(h: &mut Hs) {
    let w = h.winner as usize;
    let vp = vp_of(h, w);
    h.result = if w == h.my_idx {
        format!("THE PARK IS YOURS. {vp} POINTS.")
    } else {
        format!("P{} OWNS THE PARK WITH {vp}.", h.players[w].seat + 1)
    };
    if w == h.my_idx {
        stat("homesteads_won", 1);
    }
    h.over = Some(Timer::from_seconds(3.0, TimerMode::Once));
    h.dirty = true;
    sfx(if w == h.my_idx { "win" } else { "over" });
}

fn produce(h: &mut Hs, d: u8) {
    let mut my_gain = [0u8; 5];
    for hx in 0..19 {
        if h.hex_num[hx] != d || h.robber == hx || h.hex_res[hx] == EMPTY_LOT {
            continue;
        }
        let res = h.hex_res[hx] as usize;
        for &v in &h.hex_verts[hx] {
            let own = h.vert_own[v];
            if own >= 0 {
                let gain = if h.vert_city[v] { 2 } else { 1 };
                let who = own as usize;
                h.players[who].res[res] = h.players[who].res[res].saturating_add(gain);
                if who == h.my_idx {
                    my_gain[res] += gain;
                    stat("resources_gained", gain as u64);
                }
            }
        }
    }
    let gains: Vec<String> = (0..5)
        .filter(|&i| my_gain[i] > 0)
        .map(|i| format!("+{} {}", my_gain[i], RES_NAMES[i]))
        .collect();
    if !gains.is_empty() {
        h.push_log(format!("PAYDAY ON {d}: {}", gains.join(" ")));
    }
}

fn roll(h: &mut Hs, rng: &mut Rng) {
    let d = 2 + rng.range(6) as u8 + rng.range(6) as u8;
    h.dice = d;
    sfx("drop");
    h.push_log(format!("P{} ROLLS {d}.", h.players[h.turn].seat + 1));
    if d == 7 {
        // Everyone holding more than seven loses half, then the inspector moves.
        for p in 0..h.n() {
            let total: u32 = h.players[p].res.iter().map(|&r| r as u32).sum();
            if total > 7 {
                let mut drop = total / 2;
                while drop > 0 {
                    let pick = rng.range(5) as usize;
                    if h.players[p].res[pick] > 0 {
                        h.players[p].res[pick] -= 1;
                        drop -= 1;
                    }
                }
                if p == h.my_idx {
                    h.push_log(format!("AUDIT: YOU SURRENDER {} SUPPLIES.", total / 2));
                }
            }
        }
        h.phase = 2;
    } else {
        produce(h, d);
        h.phase = 3;
    }
    h.dirty = true;
}

fn move_robber(h: &mut Hs, rng: &mut Rng, hx: usize) {
    h.robber = hx;
    if h.turn == h.my_idx {
        stat("inspector_moves", 1);
    }
    // Steal one random resource from a random rival on the lot.
    let victims: Vec<usize> = h.hex_verts[hx]
        .iter()
        .filter_map(|&v| {
            let o = h.vert_own[v];
            (o >= 0 && o as usize != h.turn).then_some(o as usize)
        })
        .filter(|&o| h.players[o].alive && h.players[o].res.iter().any(|&r| r > 0))
        .collect();
    if !victims.is_empty() {
        let victim = victims[rng.range(victims.len() as u32) as usize];
        loop {
            let pick = rng.range(5) as usize;
            if h.players[victim].res[pick] > 0 {
                h.players[victim].res[pick] -= 1;
                h.players[h.turn].res[pick] += 1;
                break;
            }
        }
        let line = format!(
            "THE INSPECTOR SHAKES DOWN P{}.",
            h.players[victim].seat + 1
        );
        h.push_log(line);
    } else {
        h.push_log("THE INSPECTOR FINDS THE LOT DESERTED.".into());
    }
    h.phase = 3;
    h.dirty = true;
    sfx("buzz");
}

fn place_outpost(h: &mut Hs, v: usize) {
    let p = h.actor();
    h.vert_own[v] = p as i8;
    if p == h.my_idx {
        stat("outposts_founded", 1);
    }
    sfx("place");
    if h.phase == 0 {
        h.last_outpost = v as i16;
        // Second-round outposts pay out their neighbors.
        if h.setup_n as usize >= h.n() {
            for hx in 0..19 {
                if h.hex_verts[hx].contains(&v) && h.hex_res[hx] != EMPTY_LOT {
                    let r = h.hex_res[hx] as usize;
                    h.players[p].res[r] += 1;
                }
            }
        }
    } else {
        pay(&mut h.players[p], &COST_OUTPOST);
    }
    h.dirty = true;
}

fn place_hall(h: &mut Hs, e: usize) {
    let p = h.actor();
    h.edge_own[e] = p as i8;
    if p == h.my_idx {
        stat("halls_built", 1);
    }
    sfx("tick");
    if h.phase == 0 {
        h.last_outpost = -1;
        h.setup_n += 1;
        if h.setup_n as usize >= 2 * h.n() {
            h.phase = 1;
            h.turn = 0;
            h.push_log("THE DRAFT IS DONE. P1 ROLLS FIRST.".into());
        }
    } else {
        pay(&mut h.players[p], &COST_HALL);
    }
    h.dirty = true;
}

fn end_turn(h: &mut Hs) {
    for _ in 0..h.n() {
        h.turn = (h.turn + 1) % h.n();
        if h.players[h.turn].alive {
            break;
        }
    }
    h.phase = 1;
    h.dice = 0;
    h.dirty = true;
}

fn buy_idea(h: &mut Hs, rng: &mut Rng) {
    let p = h.turn;
    let total = h.guard_left as u32 + h.patent_left as u32;
    if total == 0 || !can_pay(&h.players[p], &COST_IDEA) {
        sfx("buzz");
        return;
    }
    pay(&mut h.players[p], &COST_IDEA);
    if rng.range(total) < h.guard_left as u32 {
        h.guard_left -= 1;
        h.players[p].guards += 1;
        if p == h.my_idx {
            h.push_log("IDEA: A GUARD JOINS YOUR PAYROLL.".into());
        }
    } else {
        h.patent_left -= 1;
        h.players[p].patents += 1;
        if p == h.my_idx {
            h.push_log("IDEA: A PATENT. +1 POINT.".into());
        }
    }
    if p != h.my_idx {
        h.push_log(format!("P{} FILES AN IDEA.", h.players[p].seat + 1));
    } else {
        stat("ideas_drawn", 1);
    }
    sfx("chip");
    h.dirty = true;
}

fn nearest_vert(h: &Hs, world: Vec2) -> Option<usize> {
    let mut best = None;
    let mut bd = 14.0f32;
    for (i, &v) in h.verts.iter().enumerate() {
        let d = (v - world).length();
        if d < bd {
            bd = d;
            best = Some(i);
        }
    }
    best
}

fn nearest_edge(h: &Hs, world: Vec2) -> Option<usize> {
    let mut best = None;
    let mut bd = 14.0f32;
    for (i, &(a, b)) in h.edges.iter().enumerate() {
        let mid = (h.verts[a] + h.verts[b]) / 2.0;
        let d = (mid - world).length();
        if d < bd {
            bd = d;
            best = Some(i);
        }
    }
    best
}

fn nearest_hex(h: &Hs, world: Vec2) -> Option<usize> {
    let mut best = None;
    let mut bd = HEX_SIZE * 0.8;
    for (i, &c) in h.centers.iter().enumerate() {
        let d = (c - world).length();
        if d < bd {
            bd = d;
            best = Some(i);
        }
    }
    best
}

fn handle_click(h: &mut Hs, rng: &mut Rng, world: Vec2) {
    let p = h.actor();
    match h.phase {
        0 => {
            if h.last_outpost < 0 {
                if let Some(v) = nearest_vert(h, world) {
                    if can_outpost(h, p, v, true) {
                        place_outpost(h, v);
                        send_state(h, "");
                    }
                }
            } else if let Some(e) = nearest_edge(h, world) {
                if can_hall(h, p, e) {
                    place_hall(h, e);
                    send_state(h, h.log.last().cloned().unwrap_or_default().as_str());
                }
            }
        }
        2 => {
            if let Some(hx) = nearest_hex(h, world) {
                if hx != h.robber {
                    move_robber(h, rng, hx);
                    send_state(h, h.log.last().cloned().unwrap_or_default().as_str());
                }
            }
        }
        3 => {
            if let Some(v) = nearest_vert(h, world) {
                if h.vert_own[v] == p as i8
                    && !h.vert_city[v]
                    && can_pay(&h.players[p], &COST_OFFICE)
                {
                    pay(&mut h.players[p], &COST_OFFICE);
                    h.vert_city[v] = true;
                    h.push_log(format!("P{} OPENS AN OFFICE.", h.players[p].seat + 1));
                    if p == h.my_idx {
                        stat("offices_upgraded", 1);
                    }
                    sfx("win");
                    check_win(h);
                    send_state(h, h.log.last().cloned().unwrap_or_default().as_str());
                    return;
                }
                if can_outpost(h, p, v, false) && can_pay(&h.players[p], &COST_OUTPOST) {
                    place_outpost(h, v);
                    h.push_log(format!("P{} FOUNDS AN OUTPOST.", h.players[p].seat + 1));
                    check_win(h);
                    send_state(h, h.log.last().cloned().unwrap_or_default().as_str());
                    return;
                }
            }
            if let Some(e) = nearest_edge(h, world) {
                if can_hall(h, p, e) && can_pay(&h.players[p], &COST_HALL) {
                    place_hall(h, e);
                    check_win(h);
                    send_state(h, "");
                }
            }
        }
        _ => {}
    }
}

#[allow(clippy::too_many_arguments)]
fn input(
    keys: Res<ButtonInput<KeyCode>>,
    buttons: Res<ButtonInput<MouseButton>>,
    windows: Query<&Window>,
    cameras: Query<(&Camera, &GlobalTransform)>,
    mut rng: ResMut<Rng>,
    mut h: ResMut<Hs>,
) {
    if !h.me_acting() || !h.players[h.actor()].human {
        return;
    }
    let digit = [
        (KeyCode::Digit1, 0usize),
        (KeyCode::Digit2, 1),
        (KeyCode::Digit3, 2),
        (KeyCode::Digit4, 3),
        (KeyCode::Digit5, 4),
    ]
    .iter()
    .find(|(k, _)| keys.just_pressed(*k))
    .map(|&(_, i)| i);
    match h.phase {
        1 => {
            if keys.just_pressed(KeyCode::KeyR) || keys.just_pressed(KeyCode::Space) {
                roll(&mut h, &mut rng);
                send_state(&h, h.log.last().cloned().unwrap_or_default().as_str());
                return;
            }
        }
        3 => {
            if keys.just_pressed(KeyCode::KeyE) || keys.just_pressed(KeyCode::Space) {
                end_turn(&mut h);
                send_state(&h, "");
                return;
            }
            if keys.just_pressed(KeyCode::KeyI) {
                buy_idea(&mut h, &mut rng);
                check_win(&mut h);
                send_state(&h, h.log.last().cloned().unwrap_or_default().as_str());
                return;
            }
            if keys.just_pressed(KeyCode::KeyG) && h.players[h.turn].guards > 0 {
                let t = h.turn;
                h.players[t].guards -= 1;
                h.players[t].played += 1;
                if t == h.my_idx {
                    stat("guards_played", 1);
                }
                let line = format!("P{} SENDS OUT A GUARD.", h.players[t].seat + 1);
                h.push_log(line);
                h.phase = 2;
                h.dirty = true;
                check_win(&mut h);
                send_state(&h, h.log.last().cloned().unwrap_or_default().as_str());
                return;
            }
            if keys.just_pressed(KeyCode::KeyT) {
                h.phase = 4;
                h.dirty = true;
                return;
            }
        }
        4 => {
            if let Some(g) = digit {
                if h.players[h.turn].res[g] >= 4 {
                    h.trade_give = g as u8;
                    h.phase = 5;
                    h.dirty = true;
                } else {
                    sfx("buzz");
                }
                return;
            }
            if keys.just_pressed(KeyCode::KeyT) || keys.just_pressed(KeyCode::Escape) {
                h.phase = 3;
                h.dirty = true;
                return;
            }
        }
        5 => {
            if let Some(get) = digit {
                let t = h.turn;
                let give = h.trade_give as usize;
                h.players[t].res[give] -= 4;
                h.players[t].res[get] += 1;
                h.push_log(format!(
                    "TRADED 4 {} FOR 1 {}.",
                    RES_NAMES[give], RES_NAMES[get]
                ));
                h.phase = 3;
                h.dirty = true;
                sfx("chip");
                send_state(&h, h.log.last().cloned().unwrap_or_default().as_str());
                return;
            }
        }
        _ => {}
    }
    if buttons.just_pressed(MouseButton::Left) {
        let Ok(window) = windows.single() else { return };
        let Ok((camera, cam_tf)) = cameras.single() else { return };
        let Some(world) = cursor_world(window, camera, cam_tf) else { return };
        handle_click(&mut h, &mut rng, world);
    }
}

fn bot_best_vertex(h: &Hs, setup: bool, p: usize) -> Option<usize> {
    let pips = |n: u8| -> u32 {
        if n == 0 {
            0
        } else {
            6 - (7i32 - n as i32).unsigned_abs()
        }
    };
    let mut best = None;
    let mut bs = 0u32;
    for v in 0..h.verts.len() {
        if !can_outpost(h, p, v, setup) {
            continue;
        }
        let score: u32 = (0..19)
            .filter(|&hx| h.hex_verts[hx].contains(&v))
            .map(|hx| pips(h.hex_num[hx]))
            .sum();
        if best.is_none() || score > bs {
            bs = score;
            best = Some(v);
        }
    }
    best
}

fn bots(time: Res<Time>, mut rng: ResMut<Rng>, mut h: ResMut<Hs>) {
    if h.net || h.over.is_some() || !h.have_board {
        return;
    }
    let p = h.actor();
    if h.players[p].human || !h.players[p].alive {
        return;
    }
    if !h.bot_t.tick(time.delta()).finished() {
        return;
    }
    match h.phase {
        0 => {
            if h.last_outpost < 0 {
                if let Some(v) = bot_best_vertex(&h, true, p) {
                    place_outpost(&mut h, v);
                }
            } else {
                let lo = h.last_outpost as usize;
                let opts: Vec<usize> = h.vert_edges[lo]
                    .iter()
                    .copied()
                    .filter(|&e| h.edge_own[e] < 0)
                    .collect();
                if let Some(&e) = opts.first() {
                    place_hall(&mut h, e);
                }
            }
        }
        1 => roll(&mut h, &mut rng),
        2 => {
            // Move the inspector to the busiest rival lot.
            let mut best = h.robber;
            let mut bs = -1i32;
            for hx in 0..19 {
                if hx == h.robber || h.hex_res[hx] == EMPTY_LOT {
                    continue;
                }
                let mut rivals = 0;
                let mut mine = 0;
                for &v in &h.hex_verts[hx] {
                    match h.vert_own[v] {
                        o if o == p as i8 => mine += 1,
                        o if o >= 0 => rivals += 1,
                        _ => {}
                    }
                }
                let score = rivals * 2 - mine * 3;
                if score > bs {
                    bs = score;
                    best = hx;
                }
            }
            if best != h.robber {
                move_robber(&mut h, &mut rng, best);
            } else {
                h.phase = 3;
            }
        }
        3 => {
            // One action per tick: office > outpost > hall > idea > trade > end.
            if can_pay(&h.players[p], &COST_OFFICE) {
                if let Some(v) =
                    (0..h.verts.len()).find(|&v| h.vert_own[v] == p as i8 && !h.vert_city[v])
                {
                    pay(&mut h.players[p], &COST_OFFICE);
                    h.vert_city[v] = true;
                    let line = format!("P{} OPENS AN OFFICE.", h.players[p].seat + 1);
                    h.push_log(line);
                    check_win(&mut h);
                    return;
                }
            }
            if can_pay(&h.players[p], &COST_OUTPOST) {
                if let Some(v) = bot_best_vertex(&h, false, p) {
                    place_outpost(&mut h, v);
                    let line = format!("P{} FOUNDS AN OUTPOST.", h.players[p].seat + 1);
                    h.push_log(line);
                    check_win(&mut h);
                    return;
                }
            }
            if can_pay(&h.players[p], &COST_HALL) {
                let opts: Vec<usize> =
                    (0..h.edges.len()).filter(|&e| can_hall(&h, p, e)).collect();
                if !opts.is_empty() && rng.chance(0.8) {
                    let e = opts[rng.range(opts.len() as u32) as usize];
                    place_hall(&mut h, e);
                    check_win(&mut h);
                    return;
                }
            }
            if can_pay(&h.players[p], &COST_IDEA) && rng.chance(0.6) {
                buy_idea(&mut h, &mut rng);
                check_win(&mut h);
                return;
            }
            // 4:1 a surplus toward staples or coffee.
            if let Some(give) = (0..5).find(|&i| h.players[p].res[i] >= 6) {
                let get = if h.players[p].res[4] < 3 { 4 } else { 0 };
                h.players[p].res[give] -= 4;
                h.players[p].res[get] += 1;
                return;
            }
            end_turn(&mut h);
        }
        _ => {}
    }
}

fn skip_dead(mut h: ResMut<Hs>) {
    if h.over.is_some() || !h.have_board {
        return;
    }
    // Deterministic on every client: dead placers forfeit their draft picks,
    // dead players forfeit their turns.
    let mut guard = 0;
    while h.phase == 0 && (h.setup_n as usize) < 2 * h.n() && !h.players[h.placer()].alive {
        h.last_outpost = -1;
        h.setup_n += 1;
        guard += 1;
        if guard > 32 {
            break;
        }
        if h.setup_n as usize >= 2 * h.n() {
            h.phase = 1;
            h.turn = 0;
        }
    }
    if h.phase != 0 && !h.players[h.turn].alive {
        end_turn(&mut h);
    }
}

fn net_apply(mut events: EventReader<NetIn>, net: Res<NetMode>, mut h: ResMut<Hs>) {
    let Some(cfg) = &net.0 else {
        events.clear();
        return;
    };
    for ev in events.read() {
        if ev.left {
            if let Some(idx) = h.players.iter().position(|p| p.seat == ev.seat as usize) {
                if h.players[idx].alive {
                    h.players[idx].alive = false;
                    let line = format!("P{} ABANDONS THE PARK.", ev.seat + 1);
                    h.push_log(line);
                    let alive: Vec<usize> =
                        (0..h.n()).filter(|&i| h.players[i].alive).collect();
                    if alive.len() == 1 && h.over.is_none() {
                        h.winner = alive[0] as i8;
                        finish(&mut h);
                    }
                }
            }
            continue;
        }
        if ev.seat == cfg.seat {
            continue;
        }
        let Ok(w) = serde_json::from_str::<WState>(&ev.data) else { continue };
        if w.t != "st" || w.pl.len() != h.players.len() {
            continue;
        }
        for i in 0..19 {
            h.hex_res[i] = w.hr.get(i).copied().unwrap_or(EMPTY_LOT);
            h.hex_num[i] = w.hn.get(i).copied().unwrap_or(0);
        }
        h.have_board = true;
        h.players = w.pl;
        if w.vo.len() == h.vert_own.len() {
            h.vert_own = w.vo;
        }
        if w.vc.len() == h.vert_city.len() {
            h.vert_city = w.vc.iter().map(|&c| c != 0).collect();
        }
        if w.eo.len() == h.edge_own.len() {
            h.edge_own = w.eo;
        }
        h.robber = (w.rob as usize).min(18);
        h.turn = (w.turn as usize).min(h.players.len() - 1);
        h.phase = w.ph;
        h.setup_n = w.sn;
        h.last_outpost = w.lo;
        h.dice = w.d;
        h.guard_left = w.gl;
        h.patent_left = w.ptl;
        if !w.log.is_empty() {
            h.push_log(w.log.clone());
        }
        if w.win >= 0 && h.over.is_none() {
            h.winner = w.win;
            finish(&mut h);
        }
        h.dirty = true;
    }
}

#[allow(clippy::type_complexity, clippy::too_many_arguments)]
fn paint(
    mut h: ResMut<Hs>,
    mut mats: ResMut<Assets<ColorMaterial>>,
    mut sprites: Query<&mut Sprite>,
    mut tfs: Query<&mut Transform>,
    mut nums: Query<(&NumText, &mut Text2d, &mut TextColor), (Without<Hud>, Without<LogText>)>,
    mut hud: Query<&mut Text2d, (With<Hud>, Without<LogText>, Without<NumText>)>,
    mut logq: Query<&mut Text2d, (With<LogText>, Without<Hud>, Without<NumText>)>,
) {
    if !h.dirty {
        return;
    }
    h.dirty = false;
    for (i, mat) in h.hex_mats.iter().enumerate() {
        if let Some(m) = mats.get_mut(mat) {
            m.color = if h.have_board { lot_color(h.hex_res[i]) } else { DIM };
        }
    }
    for (n, mut t, mut c) in &mut nums {
        let s = if h.have_board && h.hex_num[n.0] > 0 {
            h.hex_num[n.0].to_string()
        } else {
            String::new()
        };
        if t.0 != s {
            t.0 = s;
        }
        c.0 = if h.hex_num[n.0] == 6 || h.hex_num[n.0] == 8 { RED } else { WHITE };
    }
    for (v, &e) in h.vert_ents.iter().enumerate() {
        if let Ok(mut s) = sprites.get_mut(e) {
            if h.vert_own[v] >= 0 {
                let seat = h.players[h.vert_own[v] as usize].seat;
                s.color = PLAYER_COLORS[seat % PLAYER_COLORS.len()];
                s.custom_size = Some(Vec2::splat(if h.vert_city[v] { 20.0 } else { 13.0 }));
            } else {
                s.color = Color::NONE;
            }
        }
    }
    for (i, &e) in h.edge_ents.iter().enumerate() {
        if let Ok(mut s) = sprites.get_mut(e) {
            s.color = if h.edge_own[i] >= 0 {
                let seat = h.players[h.edge_own[i] as usize].seat;
                PLAYER_COLORS[seat % PLAYER_COLORS.len()]
            } else {
                Color::NONE
            };
        }
    }
    let robber_ent = h.robber_ent;
    let robber_pos = h.centers[h.robber] + Vec2::new(0.0, -16.0);
    if let Ok(mut tf) = tfs.get_mut(robber_ent) {
        tf.translation = robber_pos.extend(4.0);
    }
    if let Ok(mut t) = hud.single_mut() {
        let me = &h.players[h.my_idx];
        let res_line: Vec<String> =
            (0..5).map(|i| format!("{} {}", &RES_NAMES[i][..3], me.res[i])).collect();
        let prompt = if h.over.is_some() {
            h.result.clone()
        } else if !h.have_board {
            "WAITING FOR THE BOARD...".into()
        } else if !h.me_acting() || !h.players[h.actor()].human {
            format!("P{} IS ACTING...", h.players[h.actor()].seat + 1)
        } else {
            match h.phase {
                0 if h.last_outpost < 0 => "DRAFT: CLICK A CORNER FOR AN OUTPOST".into(),
                0 => "DRAFT: CLICK AN EDGE FOR ITS HALL".into(),
                1 => "R ROLLS".into(),
                2 => "INSPECTOR: CLICK A LOT".into(),
                4 => "TRADE: GIVE WHICH? 1-5 (NEED 4). T CANCELS".into(),
                5 => "TRADE: GET WHICH? 1-5".into(),
                _ => "CLICK CORNER=OUTPOST/OFFICE EDGE=HALL. I IDEA G GUARD T TRADE E END".into(),
            }
        };
        let rivals: Vec<String> = (0..h.n())
            .filter(|&i| i != h.my_idx && h.players[i].alive)
            .map(|i| {
                let cards: u32 = h.players[i].res.iter().map(|&r| r as u32).sum();
                format!("P{}:{}VP/{}C", h.players[i].seat + 1, vp_of(&h, i), cards)
            })
            .collect();
        let s = format!(
            "{}   IDEAS G{} P{}   VP {}\n{}\n{}",
            res_line.join(" "),
            me.guards,
            me.patents,
            vp_of(&h, h.my_idx),
            prompt,
            rivals.join("  ")
        );
        if t.0 != s {
            t.0 = s;
        }
    }
    if let Ok(mut l) = logq.single_mut() {
        let s = h.log.join("\n");
        if l.0 != s {
            l.0 = s;
        }
    }
    let _ = (AMBER, GREEN);
}

fn endgame(
    time: Res<Time>,
    mut h: ResMut<Hs>,
    mut final_score: ResMut<FinalScore>,
    mut next: ResMut<NextState<Phase>>,
) {
    if let Some(timer) = h.over.as_mut() {
        if timer.tick(time.delta()).finished() {
            final_score.0 = vp_of(&h, h.my_idx) * 100;
            next.set(Phase::GameOver);
        }
    }
}
