//! SUB-BASEMENT — a MUD, in a cabinet. The building has a floor nobody
//! talks about: sixteen rooms of dead files, feral interns, and one
//! AUDITOR who never signed off. Type commands, read the room, find the
//! PAYROLL LEDGER, and ride the freight elevator up. First one out ends
//! the shift for everyone — pennies you pocketed are your score, the
//! escapee banks a 500 bonus.
//!
//! Online (2-12): the host runs the whole world; everyone else's commands
//! travel the relay and the room text comes back. Same trust model as the
//! rest of the floor. Solo runs the identical world locally.
//!
//! COMMANDS: N/E/S/W, LOOK, GET <X>, INV, USE <X>, HIT <X>, SAY <...>,
//! WHO, SCORE, HELP.

use bevy::input::keyboard::{Key, KeyboardInput};
use bevy::prelude::*;
use bevy::sprite::Anchor;
use serde::{Deserialize, Serialize};

use crate::retro::{DIM, GREEN, WHITE};
use crate::rng::Rng;
use crate::shell::{net_send, sfx, stat};
use crate::{FinalScore, GameTag, NetIn, NetMode, Phase};

pub const BLURB: &[&str] = &[
    "THERE IS A FLOOR BELOW THE BASEMENT. IT KNOWS YOUR NAME.",
    "TYPE: N/E/S/W, LOOK, GET X, INV, USE X, HIT X, SAY ..., WHO, HELP.",
    "FIND THE LEDGER. RIDE THE ELEVATOR. FIRST ONE OUT ENDS THE SHIFT.",
];

// ── the world ────────────────────────────────────────────────────────────

struct RoomDef {
    name: &'static str,
    desc: &'static str,
    exits: [i32; 4], // N E S W, -1 = wall
}

const ROOMS: [RoomDef; 16] = [
    RoomDef { name: "FREIGHT ELEVATOR", desc: "A cage of brass and grease. The lever is missing its handle; a slot reads LEDGER REQUIRED.", exits: [7, 2, 10, 1] },
    RoomDef { name: "STORAGE CAGE", desc: "Chain-link and mystery boxes. Something in here was labeled URGENT in 1987.", exits: [-1, 0, 12, -1] },
    RoomDef { name: "BOILER CORRIDOR", desc: "Pipes knock out a rhythm nobody taught them. The heat has opinions.", exits: [6, 5, 11, 0] },
    RoomDef { name: "MAIL TOMBS", desc: "Undelivered interoffice envelopes, stacked into hills. Some of the addressees retired. Some never existed.", exits: [5, -1, 15, 11] },
    RoomDef { name: "OLD BREAK ROOM", desc: "A fridge that hums in a minor key. The bulletin board still says WELLNESS WEEK.", exits: [-1, 12, -1, -1] },
    RoomDef { name: "FILING CATACOMBS", desc: "Cabinets to the ceiling, drawers open like drawbridges. The alphabet gave up around M.", exits: [14, -1, 3, 2] },
    RoomDef { name: "SERVER CRYPT", desc: "Racks of dead machines, one blinking light that should not be blinking.", exits: [13, -1, 2, 7] },
    RoomDef { name: "ARCHIVE GATE", desc: "A steel door with a card reader, polished by worried thumbs. North lies the ARCHIVE.", exits: [9, 6, 0, 8] },
    RoomDef { name: "LOST AND FOUND", desc: "Bins of orphaned scarves, mugs, and one shoe. Everything smells faintly of 2003.", exits: [-1, 7, -1, -1] },
    RoomDef { name: "THE ARCHIVE", desc: "Green-shaded lamps over one perfect desk. The books balance here, or nowhere.", exits: [-1, -1, 7, -1] },
    RoomDef { name: "FLOODED SUB-HALL", desc: "An inch of patient black water. Your steps announce you to the whole floor.", exits: [0, 11, -1, 12] },
    RoomDef { name: "PNEUMATIC JUNCTION", desc: "Brass tubes split like veins. Occasionally one THUNKS with a delivery for no one.", exits: [2, 3, -1, 10] },
    RoomDef { name: "JANITOR'S CHAPEL", desc: "Mops racked like organ pipes. A hand-lettered sign: THE FLOOR REMEMBERS.", exits: [1, 10, -1, 4] },
    RoomDef { name: "COLD STORAGE", desc: "Your breath ghosts. Boxes labeled EVIDENCE and also PICNIC.", exits: [-1, -1, 6, -1] },
    RoomDef { name: "RECORDS ANNEX", desc: "Ledgers from before the merger. A dust angel on the floor, recently made.", exits: [-1, -1, 5, -1] },
    RoomDef { name: "STAIRWELL STUB", desc: "Eight steps up to a bricked-over door. Someone counted them in chalk: 8.", exits: [3, -1, -1, -1] },
];

#[derive(Clone, Copy, PartialEq)]
enum ItemKind {
    Weapon(i32),
    Heal(i32),
    Key,
    Ledger,
    Junk,
}

struct ItemDef {
    word: &'static str, // what you type
    label: &'static str,
    kind: ItemKind,
    home: usize,
}

const ITEMS: [ItemDef; 9] = [
    ItemDef { word: "stapler", label: "A DESK STAPLER", kind: ItemKind::Weapon(4), home: 1 },
    ItemDef { word: "red", label: "THE RED STAPLER", kind: ItemKind::Weapon(9), home: 6 },
    ItemDef { word: "mop", label: "A BLESSED MOP", kind: ItemKind::Weapon(6), home: 12 },
    ItemDef { word: "coffee", label: "A THERMOS OF ANCIENT COFFEE", kind: ItemKind::Heal(12), home: 4 },
    ItemDef { word: "donut", label: "A SHELF-STABLE DONUT", kind: ItemKind::Heal(6), home: 4 },
    ItemDef { word: "sandwich", label: "A SANDWICH OF UNKNOWN AGE", kind: ItemKind::Heal(8), home: 13 },
    ItemDef { word: "keycard", label: "A LAMINATED KEYCARD", kind: ItemKind::Key, home: 8 },
    ItemDef { word: "ledger", label: "THE PAYROLL LEDGER", kind: ItemKind::Ledger, home: 9 },
    ItemDef { word: "umbrella", label: "AN UMBRELLA (INDOORS?)", kind: ItemKind::Junk, home: 8 },
];

const PENNY_PILES: [(usize, u32); 3] = [(5, 30), (11, 25), (14, 40)];

struct MobDef {
    word: &'static str,
    label: &'static str,
    hp: i32,
    atk: i32,
    home: usize,
}

const MOBS: [MobDef; 7] = [
    MobDef { word: "bunny", label: "A DUST BUNNY THE SIZE OF REGRET", hp: 8, atk: 2, home: 2 },
    MobDef { word: "bunny", label: "A DUST BUNNY WITH SENIORITY", hp: 8, atk: 2, home: 12 },
    MobDef { word: "intern", label: "A FERAL INTERN", hp: 14, atk: 4, home: 3 },
    MobDef { word: "intern", label: "A FERAL INTERN (COVER SHEETS)", hp: 14, atk: 4, home: 14 },
    MobDef { word: "gremlin", label: "A CABLE GREMLIN", hp: 18, atk: 5, home: 6 },
    MobDef { word: "slug", label: "THE SUMP SLUG", hp: 12, atk: 3, home: 10 },
    MobDef { word: "auditor", label: "THE AUDITOR", hp: 40, atk: 7, home: 9 },
];
const AUDITOR: usize = 6;

struct Adventurer {
    seat: usize,
    room: usize,
    hp: i32,
    inv: Vec<usize>,
    pennies: u32,
    visited: [bool; 16],
    alive: bool, // false once escaped or departed
}

impl Adventurer {
    fn weapon(&self) -> (i32, &'static str) {
        let mut best = (0, "BARE HANDS");
        for &it in &self.inv {
            if let ItemKind::Weapon(p) = ITEMS[it].kind {
                if p > best.0 {
                    best = (p, ITEMS[it].label);
                }
            }
        }
        best
    }
}

struct World {
    item_at: Vec<i32>,  // per item: room index, or -1 (carried/consumed)
    item_by: Vec<i32>,  // per item: player idx carrying, or -1
    mob_hp: Vec<i32>,   // per mob: hp (<=0 down)
    mob_respawn: Vec<f32>,
    pennies_left: [u32; 3],
    players: Vec<Adventurer>,
    ended: bool,
}

impl World {
    fn new(seats: &[usize]) -> Self {
        World {
            item_at: ITEMS.iter().map(|i| i.home as i32).collect(),
            item_by: vec![-1; ITEMS.len()],
            mob_hp: MOBS.iter().map(|m| m.hp).collect(),
            mob_respawn: vec![0.0; MOBS.len()],
            pennies_left: [PENNY_PILES[0].1, PENNY_PILES[1].1, PENNY_PILES[2].1],
            players: seats
                .iter()
                .map(|&s| Adventurer {
                    seat: s,
                    room: 0,
                    hp: 24,
                    inv: Vec::new(),
                    pennies: 0,
                    visited: {
                        let mut v = [false; 16];
                        v[0] = true;
                        v
                    },
                    alive: true,
                })
                .collect(),
            ended: false,
        }
    }
}

const DIRS: [(&str, usize); 4] = [("north", 0), ("east", 1), ("south", 2), ("west", 3)];
const DIR_NAMES: [&str; 4] = ["NORTH", "EAST", "SOUTH", "WEST"];

/// Output events: (player index, text). The caller routes them — to the
/// local terminal, or over the wire as a "tx".
type Out = Vec<(usize, String)>;

/// A per-seat stat credit the host hands out; clients apply their own.
type Stats = Vec<(usize, &'static str, u64)>;

fn room_text(w: &World, who: usize) -> String {
    let me = &w.players[who];
    let r = &ROOMS[me.room];
    let mut s = format!("-- {} --\n{}", r.name, r.desc);
    for (i, item) in ITEMS.iter().enumerate() {
        if w.item_at[i] == me.room as i32 {
            let guarded = i == 7 && w.mob_hp[AUDITOR] > 0;
            if !guarded {
                s.push_str(&format!("\nYOU SEE {} ({}).", item.label, item.word.to_uppercase()));
            }
        }
    }
    for (pi, (room, n)) in PENNY_PILES.iter().enumerate() {
        if *room == me.room && w.pennies_left[pi] > 0 {
            s.push_str(&format!("\nA SCATTER OF {n} PENNIES GLINTS HERE (GET PENNIES)."));
        }
    }
    for (mi, mob) in MOBS.iter().enumerate() {
        if mob.home == me.room && w.mob_hp[mi] > 0 {
            s.push_str(&format!("\n{} BLOCKS THE DUST ({}).", mob.label, mob.word.to_uppercase()));
        }
    }
    for (oi, other) in w.players.iter().enumerate() {
        if oi != who && other.alive && other.room == me.room {
            s.push_str(&format!("\nP{} IS HERE.", other.seat + 1));
        }
    }
    let exits: Vec<&str> = (0..4).filter(|&d| r.exits[d] >= 0).map(|d| DIR_NAMES[d]).collect();
    s.push_str(&format!("\nEXITS: {}.", exits.join(", ")));
    s
}

fn to_room(w: &World, room: usize, except: usize, text: &str, out: &mut Out) {
    for (oi, other) in w.players.iter().enumerate() {
        if oi != except && other.alive && other.room == room {
            out.push((oi, text.to_string()));
        }
    }
}

#[allow(clippy::too_many_lines)]
fn exec(w: &mut World, rng: &mut Rng, who: usize, raw: &str, out: &mut Out, stats: &mut Stats) {
    if w.ended || !w.players[who].alive {
        return;
    }
    let line = raw.trim().to_lowercase();
    if line.is_empty() {
        return;
    }
    let mut parts = line.splitn(2, ' ');
    let verb = parts.next().unwrap_or("");
    let rest = parts.next().unwrap_or("").trim().to_string();
    // Direction sugar: n / north / go north.
    let dir = match verb {
        "n" | "north" => Some(0usize),
        "e" | "east" => Some(1),
        "s" | "south" => Some(2),
        "w" | "west" => Some(3),
        "go" => DIRS.iter().find(|(name, _)| name.starts_with(&rest)).map(|&(_, d)| d),
        _ => None,
    };
    if let Some(d) = dir {
        let here = w.players[who].room;
        let dest = ROOMS[here].exits[d];
        if dest < 0 {
            out.push((who, "A WALL, AND BEHIND IT, MORE BUILDING.".into()));
            return;
        }
        let dest = dest as usize;
        // The archive gate reads badges.
        if here == 7 && dest == 9 {
            let has_card = w.players[who].inv.iter().any(|&i| ITEMS[i].kind == ItemKind::Key);
            if !has_card {
                out.push((who, "THE CARD READER BLINKS RED. IT WANTS A KEYCARD.".into()));
                return;
            }
            out.push((who, "THE READER TAKES YOUR KEYCARD'S MEASURE AND THE GATE ROLLS BACK.".into()));
        }
        to_room(w, here, who, &format!("P{} LEAVES {}.", w.players[who].seat + 1, DIR_NAMES[d]), out);
        w.players[who].room = dest;
        to_room(w, dest, who, &format!("P{} SHUFFLES IN.", w.players[who].seat + 1), out);
        if !w.players[who].visited[dest] {
            w.players[who].visited[dest] = true;
            stats.push((who, "rooms_explored", 1));
        }
        out.push((who, room_text(w, who)));
        return;
    }
    match verb {
        "look" | "l" => out.push((who, room_text(w, who))),
        "help" | "?" => out.push((who, "COMMANDS: N/E/S/W, LOOK, GET <X>, INV, USE <X>, HIT <X>, SAY <...>, WHO, SCORE.\nFIND THE PAYROLL LEDGER, CARRY IT TO THE FREIGHT ELEVATOR, AND TYPE: USE LEDGER.\nFIRST ONE OUT ENDS THE SHIFT FOR EVERYONE.".into())),
        "inv" | "i" => {
            let me = &w.players[who];
            if me.inv.is_empty() {
                out.push((who, "POCKETS: LINT.".into()));
            } else {
                let list: Vec<&str> = me.inv.iter().map(|&i| ITEMS[i].label).collect();
                out.push((who, format!("YOU CARRY: {}.", list.join(", "))));
            }
        }
        "score" => {
            let me = &w.players[who];
            let (wp, wname) = me.weapon();
            out.push((who, format!("HP {}   PENNIES {}   WEAPON: {} (+{}).", me.hp, me.pennies, wname, wp)));
        }
        "who" => {
            let list: Vec<String> = w
                .players
                .iter()
                .filter(|p| p.alive)
                .map(|p| format!("P{} ({})", p.seat + 1, ROOMS[p.room].name))
                .collect();
            out.push((who, format!("ON SHIFT: {}.", list.join(", "))));
        }
        "say" => {
            if rest.is_empty() {
                out.push((who, "SAY WHAT?".into()));
            } else {
                let room = w.players[who].room;
                let seat = w.players[who].seat;
                out.push((who, format!("YOU SAY: {}", rest.to_uppercase())));
                to_room(w, room, who, &format!("P{} SAYS: {}", seat + 1, rest.to_uppercase()), out);
            }
        }
        "get" | "take" => {
            let room = w.players[who].room;
            if rest.starts_with("penn") {
                for (pi, (proom, _)) in PENNY_PILES.iter().enumerate() {
                    if *proom == room && w.pennies_left[pi] > 0 {
                        let n = w.pennies_left[pi];
                        w.pennies_left[pi] = 0;
                        w.players[who].pennies += n;
                        stats.push((who, "pennies_pocketed", n as u64));
                        out.push((who, format!("YOU SWEEP UP {n} PENNIES. THE FLOOR APPROVES.")));
                        return;
                    }
                }
                out.push((who, "NO PENNIES HERE. SOMEONE GOT GREEDY FIRST.".into()));
                return;
            }
            let found = (0..ITEMS.len()).find(|&i| {
                w.item_at[i] == room as i32 && ITEMS[i].word.starts_with(rest.as_str()) && !rest.is_empty()
            });
            let Some(i) = found else {
                out.push((who, "NOTHING HERE BY THAT NAME.".into()));
                return;
            };
            if i == 7 && w.mob_hp[AUDITOR] > 0 {
                out.push((who, "THE AUDITOR'S HAND COMES DOWN FLAT ON THE LEDGER. \"NOT SIGNED OFF.\"".into()));
                return;
            }
            w.item_at[i] = -1;
            w.item_by[i] = who as i32;
            w.players[who].inv.push(i);
            if i == 7 {
                stats.push((who, "ledgers_lifted", 1));
                out.push((who, "YOU TAKE THE PAYROLL LEDGER. THE ROOM GETS FIVE DEGREES COLDER.".into()));
                let seat = w.players[who].seat;
                to_room(w, room, who, &format!("P{} LIFTS THE PAYROLL LEDGER. RUN.", seat + 1), out);
            } else {
                out.push((who, format!("YOU TAKE {}.", ITEMS[i].label)));
            }
        }
        "use" | "eat" | "drink" => {
            let me_room = w.players[who].room;
            let carried = (0..ITEMS.len()).find(|&i| {
                w.item_by[i] == who as i32 && ITEMS[i].word.starts_with(rest.as_str()) && !rest.is_empty()
            });
            let Some(i) = carried else {
                out.push((who, "YOU AREN'T CARRYING THAT.".into()));
                return;
            };
            match ITEMS[i].kind {
                ItemKind::Heal(hp) => {
                    w.players[who].hp = (w.players[who].hp + hp).min(24);
                    w.item_by[i] = -2; // consumed
                    w.players[who].inv.retain(|&x| x != i);
                    out.push((who, format!("YOU CONSUME {}. HP {}.", ITEMS[i].label, w.players[who].hp)));
                }
                ItemKind::Ledger => {
                    if me_room == 0 {
                        w.ended = true;
                        stats.push((who, "floors_escaped", 1));
                        out.push((who, "THE LEDGER SLOTS HOME. THE CAGE SHUDDERS, AND UP YOU GO.".into()));
                        for oi in 0..w.players.len() {
                            if oi != who && w.players[oi].alive {
                                out.push((oi, format!("A GRINDING OF BRASS: P{} RIDES THE FREIGHT ELEVATOR UP.\nTHE SHIFT IS OVER.", w.players[who].seat + 1)));
                            }
                        }
                    } else {
                        out.push((who, "THE LEDGER ONLY MEANS SOMETHING AT THE FREIGHT ELEVATOR.".into()));
                    }
                }
                ItemKind::Key => out.push((who, "WAVE IT AT THE ARCHIVE GATE BY WALKING NORTH FROM THERE.".into())),
                _ => out.push((who, "IT DOES WHAT IT DOES, WHICH IS NOTHING.".into())),
            }
        }
        "hit" | "kill" | "attack" | "swat" => {
            let room = w.players[who].room;
            let target = (0..MOBS.len()).find(|&m| {
                MOBS[m].home == room && w.mob_hp[m] > 0 && MOBS[m].word.starts_with(rest.as_str()) && !rest.is_empty()
            });
            let Some(m) = target else {
                out.push((who, "NOTHING HERE WANTS THE SMOKE.".into()));
                return;
            };
            let (wp, wname) = w.players[who].weapon();
            let dmg = 2 + rng.range((3 + wp) as u32) as i32;
            w.mob_hp[m] -= dmg;
            let seat = w.players[who].seat;
            if w.mob_hp[m] <= 0 {
                w.mob_respawn[m] = 90.0;
                stats.push((who, "pests_bopped", 1));
                out.push((who, format!("{wname} CONNECTS FOR {dmg}. {} GOES DOWN.", MOBS[m].label)));
                to_room(w, room, who, &format!("P{} FLATTENS {}.", seat + 1, MOBS[m].label), out);
                if m == AUDITOR {
                    out.push((who, "THE AUDITOR FOLDS LIKE A BAD QUARTER. THE LEDGER IS UNGUARDED.".into()));
                }
            } else {
                out.push((who, format!("{wname} CONNECTS FOR {dmg}. IT IS NOT AMUSED.")));
                // It hits back; the auditor bills twice.
                let swings = if m == AUDITOR { 2 } else { 1 };
                for _ in 0..swings {
                    let back = 1 + rng.range(MOBS[m].atk as u32) as i32;
                    w.players[who].hp -= back;
                    out.push((who, format!("{} STRIKES BACK FOR {back}. HP {}.", MOBS[m].label, w.players[who].hp.max(0))));
                }
                if w.players[who].hp <= 0 {
                    stats.push((who, "naps_taken", 1));
                    w.players[who].hp = 24;
                    w.players[who].room = 0;
                    out.push((who, "EVERYTHING GOES BEIGE. YOU WAKE IN THE FREIGHT ELEVATOR, HUMILIATED BUT WHOLE.".into()));
                    out.push((who, room_text(w, who)));
                }
            }
        }
        _ => out.push((who, "THE FLOOR DOES NOT RECOGNIZE THAT MEMO. TRY HELP.".into())),
    }
}

// ── the cabinet ──────────────────────────────────────────────────────────

#[derive(Resource)]
struct Term {
    lines: Vec<String>,
    input: String,
    dirty: bool,
}

impl Term {
    fn push(&mut self, text: &str) {
        for raw in text.split('\n') {
            // Wrap at 74 columns, breaking on spaces.
            let mut line = String::new();
            for word in raw.split(' ') {
                if !line.is_empty() && line.len() + word.len() + 1 > 74 {
                    self.lines.push(line.clone());
                    line.clear();
                }
                if !line.is_empty() {
                    line.push(' ');
                }
                line.push_str(word);
            }
            self.lines.push(line);
        }
        if self.lines.len() > 240 {
            let cut = self.lines.len() - 240;
            self.lines.drain(0..cut);
        }
        self.dirty = true;
    }
}

#[derive(Resource)]
struct Mud {
    world: Option<World>, // host and solo carry the world; guests don't
    my_idx: usize, // index into world players (host/solo)
    net: bool,
    host: bool,
    pennies_final: u32,
    escaped: bool,
    over: Option<Timer>,
}

#[derive(Serialize, Deserialize)]
struct WCmd {
    t: String, // "cmd"
    c: String,
}

#[derive(Serialize, Deserialize)]
struct WTx {
    t: String, // "tx"
    to: u8,
    s: String,
}

#[derive(Serialize, Deserialize)]
struct WStat8 {
    t: String, // "st8"
    to: u8,
    n: String,
    d: u64,
}

#[derive(Serialize, Deserialize)]
struct WEnd {
    t: String, // "end": per-seat pennies, plus who escaped
    by: u8,
    seats: Vec<u8>,
    pennies: Vec<u32>,
}

#[derive(Component)]
struct Screen;

#[derive(Component)]
struct InputLine;

pub struct BasementPlugin;

impl Plugin for BasementPlugin {
    fn build(&self, app: &mut App) {
        app.add_systems(OnEnter(Phase::Playing), setup).add_systems(
            Update,
            (net_apply, typing, tick_world, paint, endgame)
                .chain()
                .run_if(in_state(Phase::Playing))
                .run_if(crate::unpaused),
        );
    }
}

fn setup(mut commands: Commands, net: Res<NetMode>) {
    let (seats, my_seat, is_net, host) = match &net.0 {
        Some(cfg) => {
            let s: Vec<usize> = cfg
                .present
                .iter()
                .enumerate()
                .filter(|(_, p)| **p)
                .map(|(i, _)| i)
                .collect();
            (s, cfg.seat as usize, true, cfg.is_host())
        }
        None => (vec![0usize], 0, false, true),
    };
    let my_idx = seats.iter().position(|&s| s == my_seat).unwrap_or(0);
    let world = if host || !is_net { Some(World::new(&seats)) } else { None };

    // CRT dressing.
    commands.spawn((
        Sprite { color: Color::srgb(0.02, 0.05, 0.02), custom_size: Some(Vec2::new(744.0, 624.0)), ..default() },
        Transform::from_xyz(0.0, 0.0, 0.5),
        GameTag,
    ));
    commands.spawn((
        Text2d::new(""),
        TextFont { font_size: 11.0, ..default() },
        TextColor(GREEN),
        TextLayout::new_with_justify(JustifyText::Left),
        Anchor::TopLeft,
        Transform::from_xyz(-356.0, 296.0, 2.0),
        Screen,
        GameTag,
    ));
    commands.spawn((
        Text2d::new("> _"),
        TextFont { font_size: 12.0, ..default() },
        TextColor(WHITE),
        TextLayout::new_with_justify(JustifyText::Left),
        Anchor::BottomLeft,
        Transform::from_xyz(-356.0, -300.0, 2.0),
        InputLine,
        GameTag,
    ));

    let mut term = Term { lines: Vec::new(), input: String::new(), dirty: true };
    term.push("SUB-BASEMENT. THE ELEVATOR CAGE RATTLES SHUT BEHIND YOU.");
    term.push("TYPE HELP FOR THE SHORT VERSION. GOOD LUCK DOWN HERE.");
    let mud = Mud {
        world,
        my_idx,
        net: is_net,
        host,
        pennies_final: 0,
        escaped: false,
        over: None,
    };
    if let Some(w) = &mud.world {
        term.push(&room_text(w, my_idx));
    } else {
        term.push("(WAITING FOR THE FLOOR TO NOTICE YOU...)");
    }
    commands.insert_resource(term);
    commands.insert_resource(mud);
}

/// Routes engine output: my lines to my terminal, everyone else's to the
/// wire. Also hands out stat credits and detects the end of the shift.
fn route(m: &mut Mud, term: &mut Term, out: Out, stats: Stats) {
    let mut ended_by: Option<usize> = None;
    {
        let Some(w) = &m.world else { return };
        for (idx, text) in &out {
            if *idx == m.my_idx {
                term.push(text);
            } else if m.net {
                let msg = WTx { t: "tx".into(), to: w.players[*idx].seat as u8, s: text.clone() };
                if let Ok(j) = serde_json::to_string(&msg) {
                    net_send(&j);
                }
            }
        }
        for (idx, name, d) in &stats {
            if *idx == m.my_idx {
                stat_by_name(name, *d);
            } else if m.net {
                let msg = WStat8 {
                    t: "st8".into(),
                    to: w.players[*idx].seat as u8,
                    n: (*name).into(),
                    d: *d,
                };
                if let Ok(j) = serde_json::to_string(&msg) {
                    net_send(&j);
                }
            }
        }
        if w.ended {
            // Whoever used the ledger got the floors_escaped credit.
            ended_by = stats
                .iter()
                .find(|(_, n, _)| *n == "floors_escaped")
                .map(|(idx, _, _)| *idx);
        }
    }
    if let Some(by) = ended_by {
        let w = m.world.as_ref().unwrap();
        if m.net {
            let msg = WEnd {
                t: "end".into(),
                by: w.players[by].seat as u8,
                seats: w.players.iter().map(|p| p.seat as u8).collect(),
                pennies: w.players.iter().map(|p| p.pennies).collect(),
            };
            if let Ok(j) = serde_json::to_string(&msg) {
                net_send(&j);
            }
        }
        m.escaped = by == m.my_idx;
        m.pennies_final = w.players[m.my_idx].pennies;
        m.over = Some(Timer::from_seconds(3.0, TimerMode::Once));
        sfx(if m.escaped { "win" } else { "over" });
    }
}

/// stat() takes &'static str; the engine hands back known names only.
fn stat_by_name(name: &str, d: u64) {
    for known in [
        "rooms_explored",
        "pests_bopped",
        "pennies_pocketed",
        "ledgers_lifted",
        "floors_escaped",
        "naps_taken",
    ] {
        if known == name {
            stat(known, d);
            return;
        }
    }
}

fn typing(
    mut events: EventReader<KeyboardInput>,
    mut term: ResMut<Term>,
    mut m: ResMut<Mud>,
    mut rng: ResMut<Rng>,
) {
    if m.over.is_some() {
        events.clear();
        return;
    }
    let mut submitted: Option<String> = None;
    for ev in events.read() {
        if !ev.state.is_pressed() {
            continue;
        }
        match &ev.logical_key {
            Key::Character(c) => {
                if term.input.len() < 60 {
                    for ch in c.chars().filter(|ch| ch.is_ascii() && !ch.is_control()) {
                        term.input.push(ch);
                    }
                    term.dirty = true;
                }
            }
            Key::Space => {
                if term.input.len() < 60 {
                    term.input.push(' ');
                    term.dirty = true;
                }
            }
            Key::Backspace => {
                term.input.pop();
                term.dirty = true;
            }
            Key::Enter => {
                let line = term.input.trim().to_string();
                term.input.clear();
                term.dirty = true;
                if !line.is_empty() {
                    submitted = Some(line);
                }
            }
            _ => {}
        }
    }
    let Some(line) = submitted else { return };
    term.push(&format!("> {}", line.to_uppercase()));
    sfx("tick");
    if m.world.is_some() {
        let mut out: Out = Vec::new();
        let mut stats: Stats = Vec::new();
        let idx = m.my_idx;
        exec(m.world.as_mut().unwrap(), &mut rng, idx, &line, &mut out, &mut stats);
        route(&mut m, &mut term, out, stats);
    } else if let Ok(j) = serde_json::to_string(&WCmd { t: "cmd".into(), c: line }) {
        net_send(&j);
    }
}

fn net_apply(
    mut events: EventReader<NetIn>,
    net: Res<NetMode>,
    mut term: ResMut<Term>,
    mut m: ResMut<Mud>,
    mut rng: ResMut<Rng>,
) {
    let Some(cfg) = &net.0 else {
        events.clear();
        return;
    };
    let my_seat = cfg.seat;
    for ev in events.read() {
        if ev.left {
            if let Some(w) = m.world.as_mut() {
                if let Some(idx) = w.players.iter().position(|p| p.seat == ev.seat as usize) {
                    if w.players[idx].alive {
                        // Their pockets empty onto the floor of the room.
                        for i in 0..ITEMS.len() {
                            if w.item_by[i] == idx as i32 {
                                w.item_by[i] = -1;
                                w.item_at[i] = w.players[idx].room as i32;
                            }
                        }
                        w.players[idx].alive = false;
                        let room = w.players[idx].room;
                        let mut out: Out = Vec::new();
                        to_room(w, room, idx, &format!("P{} FADES INTO THE HVAC.", ev.seat + 1), &mut out);
                        route(&mut m, &mut term, out, Vec::new());
                    }
                }
            }
            continue;
        }
        if ev.seat == my_seat {
            continue;
        }
        let Ok(v) = serde_json::from_str::<serde_json::Value>(&ev.data) else { continue };
        match v.get("t").and_then(|t| t.as_str()) {
            Some("cmd") => {
                // Host only: run a guest's command through the world.
                if m.host {
                    if let Ok(c) = serde_json::from_str::<WCmd>(&ev.data) {
                        let idx = m
                            .world
                            .as_ref()
                            .and_then(|w| w.players.iter().position(|p| p.seat == ev.seat as usize && p.alive));
                        if let Some(idx) = idx {
                            let mut out: Out = Vec::new();
                            let mut stats: Stats = Vec::new();
                            exec(m.world.as_mut().unwrap(), &mut rng, idx, &c.c, &mut out, &mut stats);
                            route(&mut m, &mut term, out, stats);
                        }
                    }
                }
            }
            Some("tx") => {
                if let Ok(tx) = serde_json::from_str::<WTx>(&ev.data) {
                    if tx.to == my_seat {
                        term.push(&tx.s);
                    }
                }
            }
            Some("st8") => {
                if let Ok(s8) = serde_json::from_str::<WStat8>(&ev.data) {
                    if s8.to == my_seat {
                        stat_by_name(&s8.n, s8.d);
                    }
                }
            }
            Some("end") => {
                if m.over.is_none() {
                    if let Ok(end) = serde_json::from_str::<WEnd>(&ev.data) {
                        m.escaped = end.by == my_seat;
                        m.pennies_final = end
                            .seats
                            .iter()
                            .position(|&s| s == my_seat)
                            .and_then(|i| end.pennies.get(i).copied())
                            .unwrap_or(0);
                        term.push(if m.escaped {
                            "UP AND OUT. DAYLIGHT. PAYROLL."
                        } else {
                            "THE SHIFT IS OVER. THE FLOOR KEEPS WHAT IT KEEPS."
                        });
                        m.over = Some(Timer::from_seconds(3.0, TimerMode::Once));
                        sfx(if m.escaped { "win" } else { "over" });
                    }
                }
            }
            _ => {}
        }
    }
}

/// Host-side world upkeep: downed pests eventually dust themselves off.
fn tick_world(time: Res<Time>, mut m: ResMut<Mud>) {
    let Some(w) = m.world.as_mut() else { return };
    if w.ended {
        return;
    }
    let dt = time.delta_secs();
    for i in 0..MOBS.len() {
        if w.mob_hp[i] <= 0 && i != AUDITOR {
            w.mob_respawn[i] -= dt;
            if w.mob_respawn[i] <= 0.0 {
                w.mob_hp[i] = MOBS[i].hp;
            }
        }
    }
}

fn paint(
    mut term: ResMut<Term>,
    m: Res<Mud>,
    mut screen: Query<&mut Text2d, (With<Screen>, Without<InputLine>)>,
    mut input: Query<&mut Text2d, (With<InputLine>, Without<Screen>)>,
) {
    if !term.dirty {
        return;
    }
    term.dirty = false;
    if let Ok(mut t) = screen.single_mut() {
        let n = term.lines.len();
        let shown = &term.lines[n.saturating_sub(38)..];
        let s = shown.join("\n").to_uppercase();
        if t.0 != s {
            t.0 = s;
        }
    }
    if let Ok(mut t) = input.single_mut() {
        let status = if let Some(w) = &m.world {
            let me = &w.players[m.my_idx];
            format!("HP {}  PENNIES {}", me.hp.max(0), me.pennies)
        } else {
            "REMOTE SHIFT".into()
        };
        let s = format!("[{}]  > {}_", status, term.input.to_uppercase());
        if t.0 != s {
            t.0 = s;
        }
    }
    let _ = DIM;
}

fn endgame(
    time: Res<Time>,
    mut m: ResMut<Mud>,
    mut final_score: ResMut<FinalScore>,
    mut next: ResMut<NextState<Phase>>,
) {
    if let Some(t) = m.over.as_mut() {
        if t.tick(time.delta()).finished() {
            final_score.0 = m.pennies_final + if m.escaped { 500 } else { 0 };
            next.set(Phase::GameOver);
        }
    }
}
