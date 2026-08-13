//! POWDER KEG — up to twelve rivals in a cellar full of crates and kegs.
//! Kegs blast in a cross, crates hide upgrades, the walls close in at the
//! end. One or two humans share the keyboard; bots fill the remaining seats.

use bevy::prelude::*;
use serde::{Deserialize, Serialize};

use crate::retro::{text, PLAYER_COLORS, AMBER, RED, WHITE};
use crate::rng::Rng;
use crate::shell::{net_send, sfx, stat};
use crate::{CabinetConfig, FinalScore, GameTag, NetIn, NetMode, Phase};

pub const BLURB: &[&str] = &[
    "LAST ONE STANDING KEEPS THE CELLAR.",
    "PICK PLAYERS + HUMANS BELOW, THEN INSERT CREDIT.",
    "P1: WASD + SPACE   P2: ARROWS + ENTER",
    "CRATES HIDE PERKS. WALLS CLOSE IN.",
];

// ---- cellar documents (the level editor's format) ----

/// A shareable cellar: 19x13 tiles, row-major, '0' empty '1' solid
/// '2' crate. The border is forced solid and spawn pockets are cleared at
/// load, so no document can brick a round.
#[derive(Serialize, Deserialize, Clone)]
struct PowderDoc {
    v: u32,
    #[serde(default)]
    name: String,
    tiles: String,
}

/// Parses a cellar document: '0' empty, '1' solid, '2' crate, and a perk
/// letter (R/B/S/K/P/V/G) is a crate with that upgrade planted inside —
/// the editor's guaranteed drops. Old digit-only documents parse unchanged.
fn doc_tiles(doc: &PowderDoc) -> Option<(Vec<Tile>, Vec<Option<Perk>>)> {
    let chars: Vec<char> = doc.tiles.chars().collect();
    if chars.len() != (COLS * ROWS) as usize {
        return None;
    }
    let mut tiles = Vec::with_capacity(chars.len());
    let mut planted = Vec::with_capacity(chars.len());
    for c in &chars {
        match (c, char_perk(*c)) {
            ('1', _) => {
                tiles.push(Tile::Solid);
                planted.push(None);
            }
            ('2', _) => {
                tiles.push(Tile::Crate);
                planted.push(None);
            }
            (_, Some(p)) => {
                tiles.push(Tile::Crate);
                planted.push(Some(p));
            }
            _ => {
                tiles.push(Tile::Empty);
                planted.push(None);
            }
        }
    }
    enforce_shell(&mut tiles);
    Some((tiles, planted))
}

/// The invariants every cellar keeps: a solid border ring.
fn enforce_shell(tiles: &mut [Tile]) {
    for r in 0..ROWS {
        for c in 0..COLS {
            if r == 0 || r == ROWS - 1 || c == 0 || c == COLS - 1 {
                tiles[Arena::idx(c, r)] = Tile::Solid;
            }
        }
    }
}

/// Clears breathing room around each active spawn (cargo included).
fn clear_spawn_pockets(tiles: &mut [Tile], planted: &mut [Option<Perk>], players: usize) {
    for &(sc, sr) in SPAWNS.iter().take(players) {
        for (dc, dr) in [(0, 0), (1, 0), (-1, 0), (0, 1), (0, -1)] {
            let (c, r) = (sc + dc, sr + dr);
            if c > 0 && c < COLS - 1 && r > 0 && r < ROWS - 1 {
                if tiles[Arena::idx(c, r)] != Tile::Empty {
                    tiles[Arena::idx(c, r)] = Tile::Empty;
                }
                planted[Arena::idx(c, r)] = None;
            }
        }
    }
}

/// The page's ROUNDS picker: 1 (single) or 3 (best-of-3). Local only.
fn page_rounds() -> u32 {
    #[cfg(target_arch = "wasm32")]
    let n = js_sys::Reflect::get(&js_sys::global(), &"__ARCADE_ROUNDS".into())
        .ok()
        .and_then(|v| v.as_f64())
        .unwrap_or(1.0);
    #[cfg(not(target_arch = "wasm32"))]
    let n = 1.0f64;
    if n >= 3.0 {
        2 // first to two round wins
    } else {
        1
    }
}

enum CellarSpec {
    Random,
    Doc(PowderDoc),
    Blank, // editor: the classic frame with an empty floor
}

fn page_cellar() -> CellarSpec {
    #[derive(Deserialize)]
    struct BlankRef {
        blank: bool,
    }
    #[cfg(target_arch = "wasm32")]
    let raw = js_sys::Reflect::get(&js_sys::global(), &"__ARCADE_LEVEL".into())
        .ok()
        .and_then(|v| v.as_string());
    #[cfg(not(target_arch = "wasm32"))]
    let raw: Option<String> = None;
    if let Some(raw) = raw {
        if serde_json::from_str::<BlankRef>(&raw).map(|b| b.blank).unwrap_or(false) {
            return CellarSpec::Blank;
        }
        if let Ok(doc) = serde_json::from_str::<PowderDoc>(&raw) {
            return CellarSpec::Doc(doc);
        }
    }
    CellarSpec::Random
}

/// The cellar editor: paint solid walls and crates, test with bots, save to
/// the community shelf. Fighters idle hidden while the canvas is open.
#[derive(Resource)]
struct CellarEditor {
    active: bool,
    testing: bool,
    tiles: Vec<Tile>,
    /// Cargo per cell: a Some crate always drops that perk when it burns.
    planted: Vec<Option<Perk>>,
    brush: Tile,
    /// When set, painting lays a LOADED crate carrying this perk (key 4
    /// cycles which one); 1/2/3 return to the plain brushes.
    perk_brush: Option<Perk>,
    /// Set by `finish` when a test round ends; editor_update executes the
    /// return (it owns the queries needed to reset the scene).
    want_return: bool,
}

fn editor_off(editor: Option<Res<CellarEditor>>) -> bool {
    editor.map(|e| !e.active).unwrap_or(true)
}

fn blank_cellar() -> Vec<Tile> {
    let mut tiles = vec![Tile::Empty; (COLS * ROWS) as usize];
    for r in 0..ROWS {
        for c in 0..COLS {
            if r % 2 == 0 && c % 2 == 0 {
                tiles[Arena::idx(c, r)] = Tile::Solid; // the pillar lattice
            }
        }
    }
    enforce_shell(&mut tiles);
    tiles
}

/// The classic randomized cellar: lattice, crates, clear spawn pockets.
fn random_cellar(rng: &mut Rng, players: usize) -> Vec<Tile> {
    let mut tiles = blank_cellar();
    for r in 1..ROWS - 1 {
        for c in 1..COLS - 1 {
            if tiles[Arena::idx(c, r)] == Tile::Empty && rng.chance(0.62) {
                tiles[Arena::idx(c, r)] = Tile::Crate;
            }
        }
    }
    let mut planted = vec![None; tiles.len()];
    clear_spawn_pockets(&mut tiles, &mut planted, players);
    tiles
}

const COLS: i32 = 19;
const ROWS: i32 = 13;
const CELL: f32 = 34.0;
const Y_OFF: f32 = -34.0; // shift the arena down to leave HUD room
const FUSE: f32 = 2.4;
const FLAME_SECS: f32 = 0.45;
const SUDDEN_DEATH_AT: f32 = 75.0;

#[derive(Clone, Copy, PartialEq, Eq)]
enum Tile {
    Empty,
    Solid,
    Crate,
}

// ---- network wire formats (host-authoritative) ----
// Guests send inputs; the host runs the one true simulation and broadcasts
// compact snapshots ~20x a second. Rendering everywhere, physics in one place.

#[derive(Serialize, Deserialize)]
struct WireInput {
    t: String, // "in"
    d: [i32; 2],
    #[serde(default)]
    b: bool,
}

#[derive(Serialize, Deserialize)]
struct WireFighter(u8, i32, i32, i32, i32, u8, u8, u32, u8); // seat,cx,cy,dx,dy,progress%,alive,kills,speed*10

#[derive(Serialize, Deserialize)]
struct WireState {
    t: String, // "st"
    tiles: String,
    f: Vec<WireFighter>,
    b: Vec<(u16, u16)>, // cell, fuse centiseconds
    fl: Vec<u16>,
    p: Vec<(u16, u8)>,
    clk: u32, // deciseconds
    /// Sudden-death progress: how far the wall spiral has closed, so guests
    /// can draw the warning tint and the HUD reads right everywhere.
    #[serde(default)]
    sp: u32,
    /// Settle-fuse deciseconds remaining once the walls stop (u32::MAX
    /// before the settle phase).
    #[serde(default = "u32_max")]
    sd: u32,
}

fn u32_max() -> u32 {
    u32::MAX
}

#[derive(Serialize, Deserialize)]
struct WireEnd {
    t: String, // "end"
    ranks: Vec<(u8, u8)>,
    kills: Vec<(u8, u32)>,
    alive: Vec<u8>,
}

fn net_guest(net: &NetMode) -> bool {
    matches!(&net.0, Some(cfg) if !cfg.is_host())
}

/// Guest-side entity pools for snapshot-driven effects.
#[derive(Resource, Default)]
struct GuestFx {
    bombs: std::collections::HashMap<usize, Entity>,
    flames: std::collections::HashMap<usize, Entity>,
    perks: std::collections::HashMap<usize, Entity>,
    /// Latest fuse per bomb cell — feeds the telegraph pulse on guests.
    fuses: std::collections::HashMap<usize, f32>,
}

fn tile_color(t: Tile) -> Color {
    match t {
        Tile::Solid => Color::srgb(0.16, 0.16, 0.24),
        Tile::Crate => Color::srgb(0.45, 0.30, 0.13),
        Tile::Empty => Color::srgb(0.05, 0.05, 0.09),
    }
}

fn perk_color_by_kind(kind: u8) -> Color {
    match kind {
        0 => RED,
        1 => Color::srgb(0.3, 0.6, 1.0),
        2 => Color::srgb(0.55, 1.0, 0.3),
        3 => WHITE,
        4 => Color::srgb(1.0, 0.62, 0.2),
        5 => Color::srgb(0.35, 0.9, 1.0),
        _ => Color::srgb(0.75, 0.5, 1.0),
    }
}

fn perk_kind(p: Perk) -> u8 {
    match p {
        Perk::Range => 0,
        Perk::Bombs => 1,
        Perk::Speed => 2,
        Perk::Kick => 3,
        Perk::Pierce => 4,
        Perk::Vest => 5,
        Perk::Phase => 6,
    }
}

fn perk_name(p: Perk) -> &'static str {
    match p {
        Perk::Range => "RANGE",
        Perk::Bombs => "KEGS",
        Perk::Speed => "SPEED",
        Perk::Kick => "KICK",
        Perk::Pierce => "PIERCE",
        Perk::Vest => "VEST",
        Perk::Phase => "PHASE",
    }
}

const ALL_PERKS: [Perk; 7] = [
    Perk::Range,
    Perk::Bombs,
    Perk::Speed,
    Perk::Kick,
    Perk::Pierce,
    Perk::Vest,
    Perk::Phase,
];

/// A planted crate's cargo, as saved in a cellar document.
fn perk_char(p: Perk) -> char {
    match p {
        Perk::Range => 'R',
        Perk::Bombs => 'B',
        Perk::Speed => 'S',
        Perk::Kick => 'K',
        Perk::Pierce => 'P',
        Perk::Vest => 'V',
        Perk::Phase => 'G',
    }
}

fn char_perk(c: char) -> Option<Perk> {
    Some(match c {
        'R' => Perk::Range,
        'B' => Perk::Bombs,
        'S' => Perk::Speed,
        'K' => Perk::Kick,
        'P' => Perk::Pierce,
        'V' => Perk::Vest,
        'G' => Perk::Phase,
        _ => return None,
    })
}

/// Shared meshes and materials: the round keg body (normal and about-to-blow
/// red), plus small circles for the perk icons.
#[derive(Resource)]
struct PowderFx {
    keg: Handle<Mesh>,
    puck: Handle<Mesh>,
    puck_small: Handle<Mesh>,
    keg_mat: Handle<ColorMaterial>,
    keg_hot: Handle<ColorMaterial>,
    dark_mat: Handle<ColorMaterial>,
    icon_mats: [Handle<ColorMaterial>; 7],
}

/// A round keg with a lit fuse — spawned identically on host and guest.
fn spawn_keg(commands: &mut Commands, fx: &PowderFx, p: Vec2) -> Entity {
    commands
        .spawn((
            Mesh2d(fx.keg.clone()),
            MeshMaterial2d(fx.keg_mat.clone()),
            Transform::from_xyz(p.x, p.y, 4.0),
            BombSprite,
            GameTag,
        ))
        .with_children(|kid| {
            // Barrel band, fuse cord, and the spark on top.
            kid.spawn((
                Sprite { color: Color::srgb(0.42, 0.42, 0.52), custom_size: Some(Vec2::new(18.0, 3.0)), ..default() },
                Transform::from_xyz(0.0, 1.0, 0.1),
            ));
            kid.spawn((
                Sprite { color: Color::srgb(0.55, 0.45, 0.30), custom_size: Some(Vec2::new(2.5, 7.0)), ..default() },
                Transform::from_xyz(2.0, 13.0, 0.1).with_rotation(Quat::from_rotation_z(-0.4)),
            ));
            kid.spawn((
                Sprite { color: AMBER, custom_size: Some(Vec2::splat(4.5)), ..default() },
                Transform::from_xyz(4.5, 16.0, 0.2)
                    .with_rotation(Quat::from_rotation_z(std::f32::consts::FRAC_PI_4)),
            ));
        })
        .id()
}

/// Perk pickups drawn as icons instead of mystery squares: a framed tile
/// with a symbol — arrows for RANGE, a keg-plus for BOMBS, chevrons for
/// SPEED, a boot for KICK. Same builder on host and guest.
fn spawn_perk_sprite(commands: &mut Commands, fx: &PowderFx, kind: u8, p: Vec2) -> Entity {
    let color = perk_color_by_kind(kind);
    commands
        .spawn((
            Sprite { color, custom_size: Some(Vec2::splat(CELL * 0.74)), ..default() },
            Transform::from_xyz(p.x, p.y, 3.0),
            GameTag,
        ))
        .with_children(|kid| {
            kid.spawn((
                Sprite {
                    color: Color::srgb(0.08, 0.08, 0.14),
                    custom_size: Some(Vec2::splat(CELL * 0.74 - 4.0)),
                    ..default()
                },
                Transform::from_xyz(0.0, 0.0, 0.05),
            ));
            let rect = |kid: &mut ChildSpawnerCommands, x: f32, y: f32, w: f32, h: f32, rot: f32| {
                kid.spawn((
                    Sprite { color, custom_size: Some(Vec2::new(w, h)), ..default() },
                    Transform::from_xyz(x, y, 0.1).with_rotation(Quat::from_rotation_z(rot)),
                ));
            };
            let dot = |kid: &mut ChildSpawnerCommands, x: f32, y: f32| {
                kid.spawn((
                    Mesh2d(fx.puck.clone()),
                    MeshMaterial2d(fx.icon_mats[kind as usize].clone()),
                    Transform::from_xyz(x, y, 0.1),
                ));
            };
            match kind {
                0 => {
                    // RANGE: a blast reaching both ways.
                    rect(kid, 0.0, 0.0, 15.0, 3.0, 0.0);
                    rect(kid, -7.5, 0.0, 6.5, 6.5, std::f32::consts::FRAC_PI_4);
                    rect(kid, 7.5, 0.0, 6.5, 6.5, std::f32::consts::FRAC_PI_4);
                }
                1 => {
                    // BOMBS: one more keg (+).
                    dot(kid, -2.5, -2.5);
                    rect(kid, 6.0, 6.0, 8.0, 2.5, 0.0);
                    rect(kid, 6.0, 6.0, 2.5, 8.0, 0.0);
                }
                2 => {
                    // SPEED: double chevron.
                    rect(kid, -4.5, 2.4, 8.0, 2.5, -0.65);
                    rect(kid, -4.5, -2.4, 8.0, 2.5, 0.65);
                    rect(kid, 4.5, 2.4, 8.0, 2.5, -0.65);
                    rect(kid, 4.5, -2.4, 8.0, 2.5, 0.65);
                }
                3 => {
                    // KICK: a boot and the keg it just sent flying.
                    rect(kid, -4.0, 2.0, 4.5, 10.0, 0.0);
                    rect(kid, -2.0, -4.0, 9.0, 4.0, 0.0);
                    dot(kid, 6.5, -1.0);
                }
                4 => {
                    // PIERCE: an arrow drilling straight through a crate.
                    rect(kid, 2.0, 5.0, 9.0, 2.0, 0.0);
                    rect(kid, 2.0, -5.0, 9.0, 2.0, 0.0);
                    rect(kid, -2.0, 0.0, 2.0, 12.0, 0.0);
                    rect(kid, 6.0, 0.0, 2.0, 12.0, 0.0);
                    rect(kid, 0.0, 0.0, 17.0, 2.5, 0.0);
                    rect(kid, 8.0, 0.0, 6.0, 6.0, std::f32::consts::FRAC_PI_4);
                }
                5 => {
                    // VEST: a shield bubble.
                    kid.spawn((
                        Mesh2d(fx.puck.clone()),
                        MeshMaterial2d(fx.icon_mats[5].clone()),
                        Transform::from_xyz(0.0, 0.0, 0.1),
                    ));
                    kid.spawn((
                        Mesh2d(fx.puck_small.clone()),
                        MeshMaterial2d(fx.dark_mat.clone()),
                        Transform::from_xyz(0.0, 0.0, 0.15),
                    ));
                    rect(kid, 0.0, 6.5, 3.0, 3.0, std::f32::consts::FRAC_PI_4);
                }
                _ => {
                    // PHASE: the little ghost that walks through crates.
                    dot(kid, 0.0, 1.5);
                    rect(kid, 0.0, -2.5, 10.0, 5.0, 0.0);
                    kid.spawn((
                        Sprite {
                            color: Color::srgb(0.08, 0.08, 0.14),
                            custom_size: Some(Vec2::new(2.5, 2.5)),
                            ..default()
                        },
                        Transform::from_xyz(-2.5, -5.0, 0.15),
                    ));
                    kid.spawn((
                        Sprite {
                            color: Color::srgb(0.08, 0.08, 0.14),
                            custom_size: Some(Vec2::new(2.5, 2.5)),
                            ..default()
                        },
                        Transform::from_xyz(2.5, -5.0, 0.15),
                    ));
                    kid.spawn((
                        Sprite { color: Color::srgb(0.08, 0.08, 0.14), custom_size: Some(Vec2::new(1.8, 2.4)), ..default() },
                        Transform::from_xyz(-2.0, 2.2, 0.2),
                    ));
                    kid.spawn((
                        Sprite { color: Color::srgb(0.08, 0.08, 0.14), custom_size: Some(Vec2::new(1.8, 2.4)), ..default() },
                        Transform::from_xyz(2.0, 2.2, 0.2),
                    ));
                }
            }
        })
        .id()
}

/// Falling ticker-tape for the winner banner.
#[derive(Component)]
struct Confetti {
    vel: Vec2,
    spin: f32,
}

fn confetti_fall(time: Res<Time>, mut bits: Query<(&Confetti, &mut Transform)>) {
    let dt = time.delta_secs();
    for (c, mut tf) in &mut bits {
        tf.translation.x += c.vel.x * dt;
        tf.translation.y += c.vel.y * dt;
        tf.rotate_z(c.spin * dt);
    }
}

/// The winner gets a show, not a shrug: their color on the marquee and a
/// rain of ticker-tape. Banner-tagged so a test round sweeps it all away.
fn celebrate(commands: &mut Commands, rng: &mut Rng, seat: usize) {
    let color = PLAYER_COLORS[seat % 12];
    sfx("win");
    let line1 = text(commands, &format!("SEAT {} WINS!", seat + 1), 46.0, color, Vec3::new(0.0, 30.0, 20.0));
    commands.entity(line1).insert((GameTag, Banner));
    let line2 = text(commands, "THE CELLAR IS THEIRS", 24.0, WHITE, Vec3::new(0.0, -12.0, 20.0));
    commands.entity(line2).insert((GameTag, Banner));
    for i in 0..70 {
        let x = rng.range(720) as f32 - 360.0;
        let c = match i % 3 {
            0 => color,
            1 => AMBER,
            _ => WHITE,
        };
        commands.spawn((
            Sprite { color: c, custom_size: Some(Vec2::new(5.0, 9.0)), ..default() },
            Transform::from_xyz(x, 330.0 + rng.range(220) as f32, 19.0),
            Confetti {
                vel: Vec2::new(rng.range(80) as f32 - 40.0, -(100.0 + rng.range(160) as f32)),
                spin: (rng.range(600) as f32 - 300.0) / 100.0,
            },
            Banner,
            GameTag,
        ));
    }
}

/// The blast shape, computed once and shared by every consumer (danger map,
/// bot self-blast sim, chain detection, burst application): each of the four
/// arms stops at Solid, includes a Crate cell, then stops. `(cell, was_crate)`
/// per reached cell, center NOT included.
fn blast_cells(tiles: &[Tile], cell: usize, range: i32, pierce: bool) -> Vec<(usize, bool)> {
    let (c0, r0) = ((cell as i32) % COLS, (cell as i32) / COLS);
    let mut out = Vec::new();
    for (dc, dr) in [(1, 0), (-1, 0), (0, 1), (0, -1)] {
        for k in 1..=range {
            let (c, r) = (c0 + dc * k, r0 + dr * k);
            if c < 0 || c >= COLS || r < 0 || r >= ROWS {
                break;
            }
            let j = Arena::idx(c, r);
            if tiles[j] == Tile::Solid {
                break;
            }
            let is_crate = tiles[j] == Tile::Crate;
            out.push((j, is_crate));
            // A piercing blast drills straight through crates.
            if is_crate && !pierce {
                break;
            }
        }
    }
    out
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum Perk {
    Range,
    Bombs,
    Speed,
    Kick,   // walk into a keg to send it sliding
    Pierce, // your blasts drill through crates instead of stopping
    Vest,   // survive one blast (consumed, with a moment of cover)
    Phase,  // walk through crates like they're fog
}

#[derive(Resource)]
struct Arena {
    tiles: Vec<Tile>,
    bombs: Vec<Option<(Entity, f32, i32, usize, bool)>>, // fuse left, range, owner, pierce
    flames: Vec<f32>,                              // seconds of flame left per cell
    flame_owner: Vec<usize>,
    perks: Vec<Option<(Perk, Entity)>>,
    /// Editor-planted cargo: this crate ALWAYS drops this perk when it burns.
    planted: Vec<Option<Perk>>,
    clock: f32,
    spiral: Vec<usize>, // sudden-death closing order
    spiral_next: usize,
    spiral_timer: Timer,
    finished: bool,
    finish_timer: Timer,
    p1_score: u32,
    /// Deaths in order, grouped by simultaneity: everyone killed by the same
    /// blast/wall tick shares a group and therefore a rank — no more ECS
    /// iteration order deciding who "died first".
    death_groups: Vec<Vec<usize>>,
    net_timer: Timer, // host snapshot cadence
    end_sent: bool,
    /// Once the walls stop, this fuse burns toward a declared draw so two
    /// cowards in the final chamber cannot stall the cabinet forever.
    settle: Timer,
    /// Best-of-3 (local): first to `match_target` round wins takes the
    /// match. 1 = the classic single round.
    match_target: u32,
    round_wins: Vec<u32>,
    rounds_played: u32,
    /// Set when a round (not the match) ends; the next-round system resets
    /// the field when the banner timer runs out.
    next_round: bool,
    /// The freshly-loaded field, kept to restock every match round.
    initial_tiles: Vec<Tile>,
    initial_planted: Vec<Option<Perk>>,
}

impl Arena {
    fn idx(c: i32, r: i32) -> usize {
        (r * COLS + c) as usize
    }
}

fn world(c: i32, r: i32) -> Vec2 {
    Vec2::new(
        (c as f32 - (COLS as f32 - 1.0) / 2.0) * CELL,
        ((ROWS as f32 - 1.0) / 2.0 - r as f32) * CELL + Y_OFF,
    )
}

#[derive(Component)]
struct Fighter {
    seat: usize,
    human: Option<u8>, // 0 = P1, 1 = P2 (local keyboards)
    remote: bool,      // a live human elsewhere on the network (host view)
    tile: IVec2,
    dir: IVec2,
    want: IVec2,
    progress: f32,
    speed: f32, // tiles per second
    range: i32,
    max_bombs: i32,
    live_bombs: i32,
    alive: bool,
    kills: u32,
    wants_bomb: bool,
    kick: bool,
    pierce: bool,
    vest: bool,
    phase: bool,
    /// Post-vest mercy window: flames can't touch a blinking fighter.
    iframes: f32,
    think: Timer,
}

#[derive(Component)]
struct BombSprite; // visual for a ticking keg

#[derive(Component)]
struct FlameSprite {
    ttl: Timer,
}

#[derive(Component)]
struct TileSprite(usize);

#[derive(Component)]
struct Hud;

/// End-of-round banner text, swept when a test round returns to the editor.
#[derive(Component)]
struct Banner;

pub struct PowderPlugin;

impl Plugin for PowderPlugin {
    fn build(&self, app: &mut App) {
        app.add_systems(OnEnter(Phase::Playing), setup)
            .add_systems(
                Update,
                poll_editor_start.run_if(in_state(Phase::Attract).or(in_state(Phase::GameOver))),
            )
            .add_systems(
                Update,
                editor_update
                    .run_if(in_state(Phase::Playing))
                    .run_if(crate::unpaused),
            )
            .add_systems(
                Update,
                (
                    host_net_input,
                    human_input,
                    bot_brains,
                    movement,
                    bombs_and_flames,
                    pickups,
                    closing_walls,
                    host_broadcast,
                    finish,
                    match_next_round,
                    guest_input,
                    guest_apply,
                    guest_smooth,
                    bomb_telegraph,
                    confetti_fall,
                    wall_warning,
                    hud,
                )
                    .chain()
                    .run_if(in_state(Phase::Playing))
                    .run_if(crate::unpaused)
                    .run_if(editor_off),
            );
    }
}

fn poll_editor_start(
    mut next: ResMut<NextState<Phase>>,
    mut net: ResMut<NetMode>,
    mut cfg: ResMut<CabinetConfig>,
) {
    if crate::shell::take_editor_start() {
        net.0 = None;
        cfg.players = 4; // test rounds: you + three bots
        cfg.humans = 1;
        crate::shell::mark_editor_pending();
        next.set(Phase::Playing);
    }
}

/// Twelve spawn cells spread around the rim, each guaranteed breathing room.
const SPAWNS: [(i32, i32); 12] = [
    (1, 1),
    (17, 11),
    (17, 1),
    (1, 11),
    (9, 1),
    (9, 11),
    (1, 6),
    (17, 6),
    (5, 1),
    (13, 11),
    (13, 1),
    (5, 11),
];

fn setup(
    mut commands: Commands,
    mut rng: ResMut<Rng>,
    config: Res<CabinetConfig>,
    net: Res<NetMode>,
    mut meshes: ResMut<Assets<Mesh>>,
    mut materials: ResMut<Assets<ColorMaterial>>,
    existing_editor: Option<ResMut<CellarEditor>>,
) {
    commands.insert_resource(PowderFx {
        keg: meshes.add(Circle::new((CELL - 8.0) / 2.0)),
        puck: meshes.add(Circle::new(5.0)),
        puck_small: meshes.add(Circle::new(3.0)),
        keg_mat: materials.add(Color::srgb(0.22, 0.22, 0.28)),
        keg_hot: materials.add(Color::srgb(0.75, 0.22, 0.18)),
        dark_mat: materials.add(Color::srgb(0.08, 0.08, 0.14)),
        icon_mats: [0u8, 1, 2, 3, 4, 5, 6].map(|k| materials.add(perk_color_by_kind(k))),
    });
    let editor_mode = crate::shell::take_editor_pending();
    let players = config.players.clamp(2, 12) as usize;
    let humans = if net.0.is_some() { 1 } else { config.humans.clamp(1, 2.min(players as u32)) as usize };

    // The editor survives rounds; fresh entry loads the page's template.
    let n_cells = (COLS * ROWS) as usize;
    let mut canvas: Option<(Vec<Tile>, Vec<Option<Perk>>)> = None;
    match (existing_editor, editor_mode) {
        (Some(mut e), true) => {
            if let CellarSpec::Doc(doc) = page_cellar() {
                if let Some((t, pl)) = doc_tiles(&doc) {
                    e.tiles = t;
                    e.planted = pl;
                }
            }
            e.active = true;
            e.testing = false;
            e.want_return = false;
            canvas = Some((e.tiles.clone(), e.planted.clone()));
        }
        (Some(mut e), false) => {
            e.active = false;
            e.testing = false;
            e.want_return = false;
        }
        (None, editing) => {
            let (t, pl) = match page_cellar() {
                CellarSpec::Doc(ref doc) if editing => {
                    doc_tiles(doc).unwrap_or_else(|| (blank_cellar(), vec![None; n_cells]))
                }
                _ => (blank_cellar(), vec![None; n_cells]),
            };
            if editing {
                canvas = Some((t.clone(), pl.clone()));
            }
            commands.insert_resource(CellarEditor {
                active: editing,
                testing: false,
                tiles: t,
                planted: pl,
                brush: Tile::Crate,
                perk_brush: None,
                want_return: false,
            });
        }
    }

    let (mut tiles, planted) = if let Some(canvas) = canvas {
        canvas // the editor shows its canvas; the round starts on G
    } else if let CellarSpec::Doc(doc) = page_cellar() {
        match doc_tiles(&doc) {
            Some((mut t, mut pl)) => {
                clear_spawn_pockets(&mut t, &mut pl, players);
                (t, pl)
            }
            None => (random_cellar(&mut rng, players), vec![None; n_cells]),
        }
    } else {
        (random_cellar(&mut rng, players), vec![None; n_cells])
    };
    enforce_shell(&mut tiles);

    // Sudden-death spiral: perimeter-inward walk over the inner field.
    let mut spiral = Vec::new();
    let (mut top, mut bottom, mut left, mut right) = (1, ROWS - 2, 1, COLS - 2);
    while top <= bottom && left <= right {
        for c in left..=right {
            spiral.push(Arena::idx(c, top));
        }
        for r in top + 1..=bottom {
            spiral.push(Arena::idx(right, r));
        }
        if bottom > top {
            for c in (left..right).rev() {
                spiral.push(Arena::idx(c, bottom));
            }
        }
        if right > left {
            for r in (top + 1..bottom).rev() {
                spiral.push(Arena::idx(left, r));
            }
        }
        top += 1;
        bottom -= 1;
        left += 1;
        right -= 1;
    }

    // Tile sprites (one per cell, recolored as the arena changes).
    for r in 0..ROWS {
        for c in 0..COLS {
            let i = Arena::idx(c, r);
            let color = tile_color(tiles[i]);
            let p = world(c, r);
            commands.spawn((
                Sprite { color, custom_size: Some(Vec2::splat(CELL - 2.0)), ..default() },
                Transform::from_xyz(p.x, p.y, 1.0),
                TileSprite(i),
                GameTag,
            ));
        }
    }

    let n = (COLS * ROWS) as usize;
    let match_target = if net.0.is_none() && !editor_mode { page_rounds() } else { 1 };
    commands.insert_resource(Arena {
        initial_tiles: tiles.clone(),
        initial_planted: planted.clone(),
        match_target,
        round_wins: vec![0; players],
        rounds_played: 0,
        next_round: false,
        tiles,
        planted,
        bombs: vec![None; n],
        flames: vec![0.0; n],
        flame_owner: vec![usize::MAX; n],
        perks: vec![None; n],
        clock: 0.0,
        spiral,
        spiral_next: 0,
        spiral_timer: Timer::from_seconds(0.32, TimerMode::Repeating),
        finished: false,
        finish_timer: Timer::from_seconds(3.5, TimerMode::Once),
        p1_score: 0,
        death_groups: Vec::new(),
        net_timer: Timer::from_seconds(0.05, TimerMode::Repeating),
        end_sent: false,
        settle: Timer::from_seconds(20.0, TimerMode::Once),
    });
    commands.init_resource::<GuestFx>();

    for seat in 0..players {
        let (sc, sr) = SPAWNS[seat];
        // Local: first `humans` seats are keyboards. Online: the host's own
        // seat is its keyboard; other present seats are remote humans.
        let (human, remote) = match &net.0 {
            Some(cfg) => (
                if seat == cfg.seat as usize { Some(0u8) } else { None },
                seat != cfg.seat as usize && cfg.present.get(seat).copied().unwrap_or(false),
            ),
            None => (if seat < humans { Some(seat as u8) } else { None }, false),
        };
        let p = world(sc, sr);
        let mut fighter_entity = commands
            .spawn((
                Fighter {
                    seat,
                    human,
                    remote,
                    tile: IVec2::new(sc, sr),
                    dir: IVec2::ZERO,
                    want: IVec2::ZERO,
                    progress: 0.0,
                    speed: 3.4,
                    range: 2,
                    max_bombs: 1,
                    live_bombs: 0,
                    alive: true,
                    kills: 0,
                    wants_bomb: false,
                    kick: false,
                    pierce: false,
                    vest: false,
                    phase: false,
                    iframes: 0.0,
                    think: Timer::from_seconds(0.12 + 0.013 * seat as f32, TimerMode::Repeating),
                },
                Sprite {
                    color: PLAYER_COLORS[seat % 12],
                    custom_size: Some(Vec2::splat(CELL - 10.0)),
                    ..default()
                },
                Transform::from_xyz(p.x, p.y, 6.0),
                GameTag,
            ));
        fighter_entity.with_children(|kid| {
            if human.is_some() {
                kid.spawn((
                    Sprite { color: WHITE, custom_size: Some(Vec2::new(8.0, 3.0)), ..default() },
                    Transform::from_xyz(0.0, CELL * 0.42, 0.1),
                ));
            }
        });
        if editor_mode {
            // Fighters idle invisibly while the canvas is open; a test round
            // resets and reveals them.
            fighter_entity.insert(Visibility::Hidden);
        }
    }

    let hud = text(&mut commands, "", 22.0, WHITE, Vec3::new(0.0, 292.0, 8.0));
    commands.entity(hud).insert((Hud, GameTag));
}

fn human_input(keys: Res<ButtonInput<KeyCode>>, net: Res<NetMode>, mut fighters: Query<&mut Fighter>) {
    if net_guest(&net) {
        return;
    }
    // Online the host is the only local keyboard, so both key sets steer it.
    let both_sets = net.0.is_some();
    for mut f in &mut fighters {
        let Some(h) = f.human else { continue };
        if !f.alive {
            continue;
        }
        let p1 = both_sets || h == 0;
        let p2 = both_sets || h != 0;
        let hit = |a: KeyCode, b: KeyCode| (p1 && keys.pressed(a)) || (p2 && keys.pressed(b));
        f.want = if hit(KeyCode::KeyA, KeyCode::ArrowLeft) {
            IVec2::new(-1, 0)
        } else if hit(KeyCode::KeyD, KeyCode::ArrowRight) {
            IVec2::new(1, 0)
        } else if hit(KeyCode::KeyW, KeyCode::ArrowUp) {
            IVec2::new(0, -1)
        } else if hit(KeyCode::KeyS, KeyCode::ArrowDown) {
            IVec2::new(0, 1)
        } else {
            IVec2::ZERO
        };
        let bomb_hit = (p1 && keys.just_pressed(KeyCode::Space)) || (p2 && keys.just_pressed(KeyCode::Enter));
        if bomb_hit {
            f.wants_bomb = true;
        }
    }
}

/// Graded danger: 0 = safe, 1 = a blast will arrive eventually, 2 = lethal
/// RIGHT NOW (standing flame, or a fuse about to blow). Bots may sprint
/// across grade-1 cells when cornered; grade 2 is never crossed.
fn danger_map(arena: &Arena, soon: f32) -> Vec<u8> {
    let n = (COLS * ROWS) as usize;
    let mut danger = vec![0u8; n];
    for i in 0..n {
        if arena.flames[i] > 0.0 {
            danger[i] = 2;
        }
    }
    for (i, b) in arena.bombs.iter().enumerate() {
        let Some((_, fuse, range, _, pierce)) = b else { continue };
        if *fuse > soon {
            continue;
        }
        let grade = if *fuse < 0.55 { 2 } else { 1 };
        danger[i] = danger[i].max(grade);
        for (j, _) in blast_cells(&arena.tiles, i, *range, *pierce) {
            danger[j] = danger[j].max(grade);
        }
    }
    danger
}

/// BFS from `from` to the nearest cell satisfying `goal`; returns first
/// step. Cells with danger above `max_danger` are never entered.
fn bfs_step(
    arena: &Arena,
    danger: &[u8],
    from: IVec2,
    max_danger: u8,
    goal: impl Fn(usize) -> bool,
) -> Option<IVec2> {
    let n = (COLS * ROWS) as usize;
    let start = Arena::idx(from.x, from.y);
    let mut prev = vec![usize::MAX; n];
    let mut queue = std::collections::VecDeque::new();
    prev[start] = start;
    queue.push_back(start);
    while let Some(cur) = queue.pop_front() {
        if goal(cur) && cur != start {
            // Walk back to the first step.
            let mut step = cur;
            while prev[step] != start {
                step = prev[step];
            }
            let (c, r) = ((step as i32) % COLS, (step as i32) / COLS);
            return Some(IVec2::new(c - from.x, r - from.y));
        }
        let (c0, r0) = ((cur as i32) % COLS, (cur as i32) / COLS);
        for (dc, dr) in [(1, 0), (-1, 0), (0, 1), (0, -1)] {
            let (c, r) = (c0 + dc, r0 + dr);
            if c < 0 || c >= COLS || r < 0 || r >= ROWS {
                continue;
            }
            let j = Arena::idx(c, r);
            if prev[j] != usize::MAX {
                continue;
            }
            if arena.tiles[j] != Tile::Empty || arena.bombs[j].is_some() {
                continue;
            }
            if danger[j] > max_danger {
                continue;
            }
            prev[j] = cur;
            queue.push_back(j);
        }
    }
    None
}

fn bot_brains(
    time: Res<Time>,
    arena: Res<Arena>,
    net: Res<NetMode>,
    mut rng: ResMut<Rng>,
    mut fighters: Query<(&mut Fighter, &Transform)>,
) {
    if net_guest(&net) {
        return;
    }
    // Snapshot enemy tiles for targeting.
    let positions: Vec<(usize, IVec2, bool)> =
        fighters.iter().map(|(f, _)| (f.seat, f.tile, f.alive)).collect();
    let danger = danger_map(&arena, FUSE + 0.1);

    for (mut f, _) in &mut fighters {
        if f.human.is_some() || f.remote || !f.alive {
            continue;
        }
        // Reflex, every frame: never walk into a cell that kills right now.
        if f.want != IVec2::ZERO {
            let t = f.tile + f.want;
            if t.x >= 0
                && t.x < COLS
                && t.y >= 0
                && t.y < ROWS
                && danger[Arena::idx(t.x, t.y)] == 2
            {
                f.want = IVec2::ZERO;
            }
        }
        if !f.think.tick(time.delta()).just_finished() {
            continue;
        }
        let here = Arena::idx(f.tile.x, f.tile.y);

        // 1. In danger: sprint out. Prefer a route through clean cells;
        // only cross not-yet-burning blast lanes when truly cornered, and
        // never cross standing flame. Cornered WITH the kick perk: shove
        // the keg in the way — the shove clears a lane the pathfinder
        // can't see.
        if danger[here] > 0 {
            if let Some(step) = bfs_step(&arena, &danger, f.tile, 0, |i| danger[i] == 0) {
                f.want = step;
            } else if let Some(step) = bfs_step(&arena, &danger, f.tile, 1, |i| danger[i] == 0) {
                f.want = step;
            } else if f.kick {
                for d in [IVec2::new(1, 0), IVec2::new(-1, 0), IVec2::new(0, 1), IVec2::new(0, -1)] {
                    let t = f.tile + d;
                    if t.x >= 0
                        && t.x < COLS
                        && t.y >= 0
                        && t.y < ROWS
                        && arena.bombs[Arena::idx(t.x, t.y)].is_some()
                    {
                        f.want = d;
                        break;
                    }
                }
            }
            f.wants_bomb = false;
            continue;
        }
        // 2. Adjacent crate or enemy, with an escape route? Drop a keg.
        let worth_bombing = blast_cells(&arena.tiles, here, f.range, f.pierce).iter().any(|&(j, is_crate)| {
            is_crate || {
                let cell = IVec2::new((j as i32) % COLS, (j as i32) / COLS);
                positions.iter().any(|&(s, t, alive)| alive && s != f.seat && t == cell)
            }
        });
        if worth_bombing && f.live_bombs < f.max_bombs {
            // Simulate our own blast: only plant when a CLEAN escape route
            // exists — no betting your chassis on sprinting through some
            // other keg's blast lane.
            let mut sim = danger.clone();
            sim[here] = sim[here].max(1);
            for (j, _) in blast_cells(&arena.tiles, here, f.range, f.pierce) {
                sim[j] = sim[j].max(1);
            }
            if bfs_step(&arena, &sim, f.tile, 0, |i| sim[i] == 0).is_some() {
                f.wants_bomb = true;
                continue;
            }
        }
        // 3. Otherwise walk toward loot, then toward the nearest rival.
        let target_step = bfs_step(&arena, &danger, f.tile, 0, |i| arena.perks[i].is_some())
            .or_else(|| {
                bfs_step(&arena, &danger, f.tile, 0, |i| {
                    let cell = IVec2::new((i as i32) % COLS, (i as i32) / COLS);
                    positions.iter().any(|&(s, t, alive)| {
                        alive && s != f.seat && (t - cell).abs().element_sum() <= 1
                    })
                })
            });
        if let Some(step) = target_step {
            f.want = step;
        } else if rng.chance(0.3) {
            let dirs = [IVec2::new(1, 0), IVec2::new(-1, 0), IVec2::new(0, 1), IVec2::new(0, -1)];
            let d = dirs[rng.range(4) as usize];
            let t = f.tile + d;
            if t.x >= 0
                && t.x < COLS
                && t.y >= 0
                && t.y < ROWS
                && danger[Arena::idx(t.x, t.y)] == 0
            {
                f.want = d;
            }
        }
    }
}

/// Slides a kicked keg from `from` along `dir` until something stops it:
/// walls, crates, another keg, or a fighter standing in the lane.
fn slide_bomb(
    arena: &mut Arena,
    bomb_tfs: &mut Query<&mut Transform, (With<BombSprite>, Without<Fighter>)>,
    occupied: &[IVec2],
    from: usize,
    dir: IVec2,
) -> bool {
    let (mut c, mut r) = ((from as i32) % COLS, (from as i32) / COLS);
    let mut moved = false;
    loop {
        let (nc, nr) = (c + dir.x, r + dir.y);
        if nc < 0 || nc >= COLS || nr < 0 || nr >= ROWS {
            break;
        }
        let j = Arena::idx(nc, nr);
        if arena.tiles[j] != Tile::Empty
            || arena.bombs[j].is_some()
            || arena.flames[j] > 0.0
            || occupied.contains(&IVec2::new(nc, nr))
        {
            break;
        }
        c = nc;
        r = nr;
        moved = true;
    }
    if moved {
        let to = Arena::idx(c, r);
        let bomb = arena.bombs[from].take();
        if let Some((e, fuse, range, owner, pierce)) = bomb {
            let p = world(c, r);
            if let Ok(mut tf) = bomb_tfs.get_mut(e) {
                tf.translation.x = p.x;
                tf.translation.y = p.y;
            }
            arena.bombs[to] = Some((e, fuse, range, owner, pierce));
        }
        sfx("drop");
    }
    moved
}

#[allow(clippy::type_complexity)]
fn movement(
    time: Res<Time>,
    mut arena: ResMut<Arena>,
    net: Res<NetMode>,
    mut fighters: Query<(&mut Fighter, &mut Transform, &mut Sprite), Without<BombSprite>>,
    mut bomb_tfs: Query<&mut Transform, (With<BombSprite>, Without<Fighter>)>,
) {
    if net_guest(&net) {
        return;
    }
    // Kick pass: a fighter with the perk shoves the keg in their way.
    let occupied: Vec<IVec2> = fighters.iter().filter(|(f, _, _)| f.alive).map(|(f, _, _)| f.tile).collect();
    for (f, _, _) in &fighters {
        if !f.alive || !f.kick || f.want == IVec2::ZERO {
            continue;
        }
        if f.dir != IVec2::ZERO && f.progress > f32::EPSILON {
            continue; // only shove from a standstill at a tile center
        }
        let target = f.tile + f.want;
        if target.x < 0 || target.x >= COLS || target.y < 0 || target.y >= ROWS {
            continue;
        }
        let j = Arena::idx(target.x, target.y);
        if arena.bombs[j].is_some()
            && slide_bomb(&mut arena, &mut bomb_tfs, &occupied, j, f.want)
            && f.human.is_some()
        {
            stat("kegs_kicked", 1);
        }
    }
    let dt = time.delta_secs();
    let mut crushed: Vec<usize> = Vec::new();
    for (mut f, mut tf, mut sprite) in &mut fighters {
        if !f.alive {
            continue;
        }
        // A phaser treats crates as fog; walls and kegs still stop everyone.
        let open_for = |arena: &Arena, phase: bool, c: i32, r: i32| -> bool {
            if c < 0 || c >= COLS || r < 0 || r >= ROWS {
                return false;
            }
            let j = Arena::idx(c, r);
            if arena.bombs[j].is_some() {
                return false;
            }
            match arena.tiles[j] {
                Tile::Empty => true,
                Tile::Crate => phase,
                Tile::Solid => false,
            }
        };
        let mut remaining = f.speed * dt;
        while remaining > 0.0 {
            if f.dir == IVec2::ZERO {
                if f.want != IVec2::ZERO && open_for(&arena, f.phase, f.tile.x + f.want.x, f.tile.y + f.want.y) {
                    f.dir = f.want;
                } else {
                    break;
                }
            }
            let step = remaining.min(1.0 - f.progress);
            f.progress += step;
            remaining -= step;
            if f.progress >= 1.0 - f32::EPSILON {
                let d = f.dir;
                f.tile += d;
                f.progress = 0.0;
                // The closing wall can solidify the destination mid-step;
                // finishing the step would entomb the fighter alive and
                // stall the round. The wall wins: crushed.
                if arena.tiles[Arena::idx(f.tile.x, f.tile.y)] == Tile::Solid {
                    f.alive = false;
                    sprite.color.set_alpha(0.15);
                    sfx("death");
                    if f.human.is_some() {
                        stat("deaths", 1);
                        stat("wall_crushes", 1);
                    }
                    crushed.push(f.seat);
                    break;
                }
                if f.human.is_some() {
                    stat("steps_walked", 1);
                }
                let turn_ok = f.want != IVec2::ZERO && open_for(&arena, f.phase, f.tile.x + f.want.x, f.tile.y + f.want.y);
                if turn_ok {
                    f.dir = f.want;
                } else if f.want == IVec2::ZERO || !open_for(&arena, f.phase, f.tile.x + f.dir.x, f.tile.y + f.dir.y) {
                    // No key held: stop at this tile center. (Holding a
                    // blocked direction keeps you sliding the way you were
                    // going — classic corner forgiveness.)
                    f.dir = IVec2::ZERO;
                }
            }
        }
        let from = world(f.tile.x, f.tile.y);
        let to = world(f.tile.x + f.dir.x, f.tile.y + f.dir.y);
        let p = from.lerp(to, f.progress);
        tf.translation.x = p.x;
        tf.translation.y = p.y;
    }
    if !crushed.is_empty() {
        arena.death_groups.push(crushed);
    }
}

#[allow(clippy::too_many_arguments, clippy::type_complexity)]
fn bombs_and_flames(
    time: Res<Time>,
    mut commands: Commands,
    mut arena: ResMut<Arena>,
    net: Res<NetMode>,
    fx: Res<PowderFx>,
    mut rng: ResMut<Rng>,
    mut fighters: Query<(&mut Fighter, &mut Sprite), (Without<TileSprite>, Without<FlameSprite>)>,
    mut tiles_q: Query<(&TileSprite, &mut Sprite), Without<FlameSprite>>,
    mut flames_q: Query<(Entity, &mut FlameSprite, &mut Sprite), (Without<Fighter>, Without<TileSprite>)>,
) {
    if net_guest(&net) {
        return;
    }
    let dt = time.delta_secs();
    arena.clock += dt;

    // Place requested bombs.
    for (mut f, _) in &mut fighters {
        if !f.alive || !f.wants_bomb {
            f.wants_bomb = false;
            continue;
        }
        f.wants_bomb = false;
        let i = Arena::idx(f.tile.x, f.tile.y);
        if f.live_bombs < f.max_bombs && arena.bombs[i].is_none() && arena.tiles[i] == Tile::Empty {
            if f.human.is_some() {
                stat("bombs_laid", 1);
            }
            let p = world(f.tile.x, f.tile.y);
            let e = spawn_keg(&mut commands, &fx, p);
            arena.bombs[i] = Some((e, FUSE, f.range, f.seat, f.pierce));
            f.live_bombs += 1;
            sfx("place");
        }
    }

    // Which seats belong to hands on THIS machine (for the service record).
    let human_seats: Vec<usize> =
        fighters.iter().filter(|(f, _)| f.human.is_some()).map(|(f, _)| f.seat).collect();
    // Tick fuses; collect exploding cells. Chained kegs blame the player
    // whose keg STARTED the chain — the classic credit rule.
    let mut exploding: Vec<(usize, Option<usize>)> = Vec::new(); // cell, initiator
    for i in 0..arena.bombs.len() {
        if let Some((_, fuse, _, _, _)) = arena.bombs[i].as_mut() {
            *fuse -= dt;
            if *fuse <= 0.0 {
                exploding.push((i, None));
            }
        }
    }
    if !exploding.is_empty() {
        sfx("boom");
    }
    let mut burst: Vec<(usize, i32, usize, bool)> = Vec::new(); // cell, range, credited seat, pierce
    while let Some((i, initiator)) = exploding.pop() {
        let Some((e, _, range, owner, pierce)) = arena.bombs[i].take() else { continue };
        commands.entity(e).despawn();
        // Give the owner their slot back.
        for (mut f, _) in &mut fighters {
            if f.seat == owner {
                f.live_bombs = (f.live_bombs - 1).max(0);
            }
        }
        let credited = initiator.unwrap_or(owner);
        burst.push((i, range, credited, pierce));
        // Chain: any bomb in this blast goes off now, on the initiator's tab.
        for (j, _) in blast_cells(&arena.tiles, i, range, pierce) {
            if arena.bombs[j].is_some() && !exploding.iter().any(|&(cell, _)| cell == j) {
                exploding.push((j, Some(credited)));
            }
        }
    }
    // Apply bursts: flames, crate destruction, perk reveals.
    for (i, range, owner, pierce) in burst {
        let (c0, r0) = ((i as i32) % COLS, (i as i32) / COLS);
        let lay_flame = |arena: &mut Arena, commands: &mut Commands, c: i32, r: i32, dir: (i32, i32)| {
            let j = Arena::idx(c, r);
            arena.flames[j] = FLAME_SECS;
            arena.flame_owner[j] = owner;
            if let Some((_, e)) = arena.perks[j].take() {
                commands.entity(e).despawn(); // flames eat loose perks
            }
            // Cross-shaped blast: a fat core, slim arms along the blast axis.
            let size = match dir {
                (0, 0) => Vec2::splat(CELL - 6.0),
                (_, 0) => Vec2::new(CELL - 2.0, CELL * 0.45),
                _ => Vec2::new(CELL * 0.45, CELL - 2.0),
            };
            let p = world(c, r);
            commands.spawn((
                Sprite { color: AMBER, custom_size: Some(size), ..default() },
                Transform::from_xyz(p.x, p.y, 5.0),
                FlameSprite { ttl: Timer::from_seconds(FLAME_SECS, TimerMode::Once) },
                GameTag,
            ));
        };
        lay_flame(&mut arena, &mut commands, c0, r0, (0, 0));
        let cells = blast_cells(&arena.tiles, i, range, pierce);
        for (j, was_crate) in cells {
            let (c, r) = ((j as i32) % COLS, (j as i32) / COLS);
            if was_crate {
                arena.tiles[j] = Tile::Empty;
                if human_seats.contains(&owner) {
                    stat("crates_smashed", 1);
                }
                // Editor-planted cargo always drops; a third of the rest
                // hide something from the table.
                let planted = arena.planted[j].take();
                let rolled = if planted.is_some() {
                    planted
                } else if rng.chance(0.34) {
                    Some(match rng.range(13) {
                        0..=2 => Perk::Range,
                        3..=5 => Perk::Bombs,
                        6 | 7 => Perk::Speed,
                        8 => Perk::Kick,
                        9 => Perk::Pierce,
                        10 | 11 => Perk::Vest,
                        _ => Perk::Phase,
                    })
                } else {
                    None
                };
                if let Some(perk) = rolled {
                    let p = world(c, r);
                    let e = spawn_perk_sprite(&mut commands, &fx, perk_kind(perk), p);
                    arena.perks[j] = Some((perk, e));
                }
            }
            let dir = ((c - c0).signum(), (r - r0).signum());
            lay_flame(&mut arena, &mut commands, c, r, dir);
        }
    }
    // Refresh tile colors (crates burn away).
    for (ts, mut sprite) in &mut tiles_q {
        let want = tile_color(arena.tiles[ts.0]);
        if sprite.color != want {
            sprite.color = want;
        }
    }
    // Flame timers.
    for i in 0..arena.flames.len() {
        if arena.flames[i] > 0.0 {
            arena.flames[i] -= dt;
        }
    }
    for (e, mut fs, mut sprite) in &mut flames_q {
        if fs.ttl.tick(time.delta()).finished() {
            commands.entity(e).despawn();
        } else {
            sprite.color.set_alpha(0.35 + 0.65 * fs.ttl.fraction_remaining());
        }
    }

    // Flames kill. Everyone burned this frame shares one death group (same
    // rank), and kill credit lands even when the killer died in the same
    // blast — trades count, posthumously.
    let mut kill_credit: Vec<(usize, usize)> = Vec::new(); // victim, owner
    let mut burned: Vec<usize> = Vec::new();
    for (mut f, mut sprite) in &mut fighters {
        if !f.alive {
            continue;
        }
        if f.iframes > 0.0 {
            f.iframes -= dt;
            if f.iframes <= 0.0 {
                sprite.color.set_alpha(1.0);
            }
            continue; // still blinking from the vest — untouchable
        }
        let i = Arena::idx(f.tile.x, f.tile.y);
        // Also check the cell being entered when mid-step.
        let j = if f.dir != IVec2::ZERO && f.progress > 0.45 {
            Arena::idx(f.tile.x + f.dir.x, f.tile.y + f.dir.y)
        } else {
            i
        };
        let hit = if arena.flames[i] > 0.0 {
            Some(arena.flame_owner[i])
        } else if arena.flames[j] > 0.0 {
            Some(arena.flame_owner[j])
        } else {
            None
        };
        if let Some(owner) = hit {
            if f.vest {
                // The vest takes it: consumed, with a mercy blink so the
                // same flame doesn't finish the job a frame later.
                f.vest = false;
                f.iframes = 0.8;
                sprite.color.set_alpha(0.55);
                if f.human.is_some() {
                    stat("vests_shredded", 1);
                }
                sfx("buzz");
                continue;
            }
            f.alive = false;
            sprite.color.set_alpha(0.15);
            sfx("death");
            if f.human.is_some() {
                stat("deaths", 1);
                if owner == f.seat {
                    stat("self_demolitions", 1);
                }
            }
            burned.push(f.seat);
            if owner != f.seat && owner != usize::MAX {
                kill_credit.push((f.seat, owner));
            }
        }
    }
    if !burned.is_empty() {
        arena.death_groups.push(burned);
    }
    for (_victim, owner) in kill_credit {
        for (mut f, _) in &mut fighters {
            if f.seat == owner {
                f.kills += 1;
                if f.human.is_some() {
                    stat("kills", 1);
                }
            }
        }
    }
}

fn pickups(
    mut commands: Commands,
    mut arena: ResMut<Arena>,
    net: Res<NetMode>,
    mut fighters: Query<&mut Fighter>,
) {
    if net_guest(&net) {
        return;
    }
    for mut f in &mut fighters {
        if !f.alive {
            continue;
        }
        let i = Arena::idx(f.tile.x, f.tile.y);
        if let Some((perk, e)) = arena.perks[i].take() {
            commands.entity(e).despawn();
            if f.human.is_some() {
                stat("perks_grabbed", 1);
            }
            match perk {
                Perk::Range => f.range = (f.range + 1).min(7),
                Perk::Bombs => f.max_bombs = (f.max_bombs + 1).min(6),
                Perk::Speed => f.speed = (f.speed + 0.45).min(6.5),
                Perk::Kick => f.kick = true,
                Perk::Pierce => f.pierce = true,
                Perk::Vest => f.vest = true,
                Perk::Phase => f.phase = true,
            }
            sfx("power");
        }
    }
}

fn closing_walls(
    time: Res<Time>,
    mut commands: Commands,
    mut arena: ResMut<Arena>,
    net: Res<NetMode>,
    mut fighters: Query<(&mut Fighter, &mut Sprite)>,
) {
    if net_guest(&net) {
        return;
    }
    if arena.clock < SUDDEN_DEATH_AT || arena.finished {
        return;
    }
    // Once the walls stop at the final chamber, a settle fuse starts: when
    // it burns out, everyone still standing goes down together (draw).
    if arena.spiral_next >= arena.spiral.len().saturating_sub(9) {
        if arena.settle.tick(time.delta()).just_finished() {
            let mut settled = Vec::new();
            for (mut f, mut sprite) in &mut fighters {
                if f.alive {
                    f.alive = false;
                    sprite.color.set_alpha(0.15);
                    settled.push(f.seat);
                }
            }
            if !settled.is_empty() {
                arena.death_groups.push(settled); // a true draw: shared rank
            }
            sfx("death");
        }
        return;
    }
    if !arena.spiral_timer.tick(time.delta()).just_finished() {
        return;
    }
    let i = arena.spiral[arena.spiral_next];
    arena.spiral_next += 1;
    arena.tiles[i] = Tile::Solid;
    if let Some((e, _, _, owner, _)) = arena.bombs[i].take() {
        commands.entity(e).despawn();
        for (mut f, _) in &mut fighters {
            if f.seat == owner {
                f.live_bombs = (f.live_bombs - 1).max(0);
            }
        }
    }
    if let Some((_, e)) = arena.perks[i].take() {
        commands.entity(e).despawn();
    }
    arena.planted[i] = None;
    let cell = IVec2::new((i as i32) % COLS, (i as i32) / COLS);
    let mut walled = Vec::new();
    for (mut f, mut sprite) in &mut fighters {
        if f.alive && f.tile == cell {
            f.alive = false;
            sprite.color.set_alpha(0.15);
            walled.push(f.seat);
        }
    }
    if !walled.is_empty() {
        arena.death_groups.push(walled);
    }
}

/// Competition ranking over the death groups: survivors are rank 1;
/// everyone in a death group shares the rank equal to how many players were
/// still standing when that group fell (earlier death = worse rank,
/// simultaneous deaths = the same rank).
fn seat_rank(arena: &Arena, total: usize, seat: usize, alive: bool) -> usize {
    if alive {
        return 1;
    }
    let mut fallen_before = 0usize;
    for group in &arena.death_groups {
        if group.contains(&seat) {
            return total - fallen_before;
        }
        fallen_before += group.len();
    }
    total
}

fn placement_score(total: usize, rank: usize, kills: u32, alive: bool) -> u32 {
    kills * 100 + ((total.saturating_sub(rank)) as u32) * 50 + if alive { 300 } else { 0 }
}

#[allow(clippy::too_many_arguments)]
fn finish(
    time: Res<Time>,
    mut arena: ResMut<Arena>,
    net: Res<NetMode>,
    editor: Option<ResMut<CellarEditor>>,
    mut rng: ResMut<Rng>,
    mut final_score: ResMut<FinalScore>,
    mut next: ResMut<NextState<Phase>>,
    fighters: Query<&Fighter>,
    mut commands: Commands,
) {
    if net_guest(&net) {
        // Guests get the same 2s banner window the host does; guest_apply
        // arms the timer when the "end" message lands.
        if arena.finished && arena.finish_timer.tick(time.delta()).finished() {
            final_score.0 = arena.p1_score;
            next.set(Phase::GameOver);
        }
        return;
    }
    let alive: Vec<&Fighter> = fighters.iter().filter(|f| f.alive).collect();
    let total = fighters.iter().count();
    if !arena.finished && alive.len() <= 1 {
        arena.finished = true;
        arena.finish_timer.reset();
        // The credited score is this machine's own seat (0 locally and for
        // the online host, whose seat is always 0).
        let me = fighters.iter().find(|f| f.seat == 0);
        let mut score = 0u32;
        if let Some(me) = me {
            let rank = seat_rank(&arena, total, 0, me.alive);
            score = placement_score(total, rank, me.kills, me.alive);
        }
        arena.p1_score += score;
        // Online: tell the guests how it ended, exactly once.
        if net.0.is_some() && !arena.end_sent {
            arena.end_sent = true;
            let end = WireEnd {
                t: "end".into(),
                ranks: fighters
                    .iter()
                    .map(|f| (f.seat as u8, seat_rank(&arena, total, f.seat, f.alive) as u8))
                    .collect(),
                kills: fighters.iter().map(|f| (f.seat as u8, f.kills)).collect(),
                alive: fighters.iter().filter(|f| f.alive).map(|f| f.seat as u8).collect(),
            };
            if let Ok(msg) = serde_json::to_string(&end) {
                net_send(&msg);
            }
        }
        match alive.first() {
            Some(winner) if winner.human.is_some() => stat("cellar_wins", 1),
            None => stat("settle_draws", 1),
            _ => {}
        }
        // Best-of-3: bank the round and play on unless someone just clinched
        // (or five rounds of draws exhausted everyone's patience).
        arena.rounds_played += 1;
        if let Some(w) = alive.first() {
            if arena.match_target > 1 {
                arena.round_wins[w.seat] += 1;
            }
        }
        let clinched = arena.round_wins.iter().any(|&x| x >= arena.match_target);
        if arena.match_target > 1 && !clinched && arena.rounds_played < 5 {
            arena.next_round = true;
            let tally =
                arena.round_wins.iter().map(|w| w.to_string()).collect::<Vec<_>>().join("-");
            let line = match alive.first() {
                Some(w) => format!("SEAT {} TAKES ROUND {}", w.seat + 1, arena.rounds_played),
                None => format!("ROUND {} - NOBODY", arena.rounds_played),
            };
            let e = text(&mut commands, &line, 30.0, AMBER, Vec3::new(0.0, 24.0, 20.0));
            commands.entity(e).insert((GameTag, Banner));
            let e2 = text(&mut commands, &format!("WINS {tally} - NEXT ROUND COMING"), 20.0, WHITE, Vec3::new(0.0, -14.0, 20.0));
            commands.entity(e2).insert((GameTag, Banner));
            sfx("clear");
        } else {
            match alive.first() {
                Some(w) => celebrate(&mut commands, &mut rng, w.seat),
                None => {
                    let e = text(&mut commands, "MUTUAL DESTRUCTION", 34.0, AMBER, Vec3::new(0.0, 0.0, 20.0));
                    commands.entity(e).insert((GameTag, Banner));
                }
            }
        }
    }
    if arena.finished && !arena.next_round && arena.finish_timer.tick(time.delta()).finished() {
        // A finished TEST round returns to the editor's canvas — no score,
        // no game over. editor_update owns the scene reset.
        if let Some(mut e) = editor {
            if e.testing {
                e.want_return = true;
                return;
            }
        }
        final_score.0 = arena.p1_score;
        next.set(Phase::GameOver);
    }
}

/// Between match rounds: when the round banner has had its moment, restock
/// the field from the loaded layout and stand everyone back up.
#[allow(clippy::type_complexity)]
fn match_next_round(
    time: Res<Time>,
    mut commands: Commands,
    mut arena: ResMut<Arena>,
    net: Res<NetMode>,
    mut fighters: Query<
        (&mut Fighter, &mut Transform, &mut Sprite, &mut Visibility),
        (Without<TileSprite>, Without<FlameSprite>, Without<BombSprite>),
    >,
    bombs: Query<Entity, With<BombSprite>>,
    flames: Query<Entity, With<FlameSprite>>,
    banners: Query<Entity, With<Banner>>,
) {
    if net_guest(&net) || !arena.next_round {
        return;
    }
    if !arena.finish_timer.tick(time.delta()).finished() {
        return;
    }
    arena.next_round = false;
    let (score, wins, played) =
        (arena.p1_score, arena.round_wins.clone(), arena.rounds_played);
    reset_field(&mut arena, &mut commands, &bombs, &flames, &banners);
    arena.p1_score = score;
    arena.round_wins = wins;
    arena.rounds_played = played;
    arena.tiles = arena.initial_tiles.clone();
    arena.planted = arena.initial_planted.clone();
    for (mut f, mut tf, mut sprite, mut vis) in &mut fighters {
        let (sc, sr) = SPAWNS[f.seat];
        f.tile = IVec2::new(sc, sr);
        f.dir = IVec2::ZERO;
        f.want = IVec2::ZERO;
        f.progress = 0.0;
        f.speed = 3.4;
        f.range = 2;
        f.max_bombs = 1;
        f.live_bombs = 0;
        f.alive = true;
        f.kills = 0;
        f.wants_bomb = false;
        f.kick = false;
        f.pierce = false;
        f.vest = false;
        f.phase = false;
        f.iframes = 0.0;
        let p = world(sc, sr);
        tf.translation.x = p.x;
        tf.translation.y = p.y;
        sprite.color.set_alpha(1.0);
        *vis = Visibility::Inherited;
    }
    sfx("coin");
}

fn hud(arena: Res<Arena>, fighters: Query<&Fighter>, mut hud: Query<&mut Text2d, With<Hud>>) {
    if let Ok(mut t) = hud.single_mut() {
        let alive = fighters.iter().filter(|f| f.alive).count();
        let to_walls = (SUDDEN_DEATH_AT - arena.clock).max(0.0);
        let ladder = if arena.match_target > 1 {
            let tally =
                arena.round_wins.iter().map(|w| w.to_string()).collect::<Vec<_>>().join("-");
            format!("RD {} [{}]   ", arena.rounds_played + 1, tally)
        } else {
            String::new()
        };
        let s = if to_walls > 0.0 {
            format!("{ladder}{alive} STANDING   WALLS IN {to_walls:.0}s")
        } else if arena.spiral_next >= arena.spiral.len().saturating_sub(9) {
            let settle = (arena.settle.duration().as_secs_f32() - arena.settle.elapsed_secs()).max(0.0);
            format!("{alive} STANDING   SETTLE IT IN {settle:.0}s")
        } else {
            format!("{alive} STANDING   THE WALLS ARE COMING")
        };
        if t.0 != s {
            t.0 = s;
        }
    }
}

// ---- network systems ----

/// Host: applies relayed guest inputs to their fighters, and converts a
/// departed human's fighter into a bot so the round keeps its shape.
fn host_net_input(
    mut events: EventReader<NetIn>,
    net: Res<NetMode>,
    mut fighters: Query<&mut Fighter>,
) {
    let Some(cfg) = &net.0 else {
        return; // local play: events drain in guest_apply's clear below
    };
    if !cfg.is_host() {
        return;
    }
    for ev in events.read() {
        for mut f in &mut fighters {
            if f.seat != ev.seat as usize {
                continue;
            }
            if ev.left {
                f.remote = false; // the bot brain takes the seat over
                continue;
            }
            let Ok(wire) = serde_json::from_str::<WireInput>(&ev.data) else { continue };
            if wire.t == "in" && f.alive {
                // An input from this seat proves a human is at it — including
                // one who dropped, was botified, and reclaimed their seat
                // through the relay. Welcome back; the bot stands down.
                f.remote = true;
                f.want = IVec2::new(wire.d[0].clamp(-1, 1), wire.d[1].clamp(-1, 1));
                if wire.b {
                    f.wants_bomb = true;
                }
            }
        }
    }
}

/// Host: broadcasts the world ~20x/second. Rendering is everywhere,
/// simulation is here.
fn host_broadcast(
    time: Res<Time>,
    net: Res<NetMode>,
    mut arena: ResMut<Arena>,
    fighters: Query<&Fighter>,
) {
    let Some(cfg) = &net.0 else { return };
    if !cfg.is_host() {
        return;
    }
    // Keep broadcasting through the finish window: the killing flame and the
    // final death usually land BETWEEN snapshots, and guests deserve to see
    // how it ended before the score screen.
    if arena.finished && arena.finish_timer.finished() {
        return;
    }
    if !arena.net_timer.tick(time.delta()).just_finished() {
        return;
    }
    let tiles: String = arena
        .tiles
        .iter()
        .map(|t| match t {
            Tile::Empty => '0',
            Tile::Solid => '1',
            Tile::Crate => '2',
        })
        .collect();
    let state = WireState {
        t: "st".into(),
        tiles,
        f: fighters
            .iter()
            .map(|f| {
                WireFighter(
                    f.seat as u8,
                    f.tile.x,
                    f.tile.y,
                    f.dir.x,
                    f.dir.y,
                    (f.progress * 100.0) as u8,
                    u8::from(f.alive),
                    f.kills,
                    (f.speed * 10.0) as u8,
                )
            })
            .collect(),
        b: arena
            .bombs
            .iter()
            .enumerate()
            .filter_map(|(i, b)| b.as_ref().map(|(_, fuse, _, _, _)| (i as u16, (fuse * 100.0).max(0.0) as u16)))
            .collect(),
        fl: arena
            .flames
            .iter()
            .enumerate()
            .filter_map(|(i, &f)| (f > 0.0).then_some(i as u16))
            .collect(),
        p: arena
            .perks
            .iter()
            .enumerate()
            .filter_map(|(i, p)| {
                p.as_ref().map(|(perk, _)| (i as u16, perk_kind(*perk)))
            })
            .collect(),
        clk: (arena.clock * 10.0) as u32,
        sp: arena.spiral_next as u32,
        sd: if arena.spiral_next >= arena.spiral.len().saturating_sub(9) {
            ((arena.settle.duration().as_secs_f32() - arena.settle.elapsed_secs()).max(0.0) * 10.0)
                as u32
        } else {
            u32::MAX
        },
    };
    if let Ok(msg) = serde_json::to_string(&state) {
        net_send(&msg);
    }
}

/// Guest: reads the local keyboard (either key set) and relays it to the host.
fn guest_input(
    keys: Res<ButtonInput<KeyCode>>,
    net: Res<NetMode>,
    mut last: Local<IVec2>,
) {
    if !net_guest(&net) {
        return;
    }
    let want = if keys.pressed(KeyCode::KeyA) || keys.pressed(KeyCode::ArrowLeft) {
        IVec2::new(-1, 0)
    } else if keys.pressed(KeyCode::KeyD) || keys.pressed(KeyCode::ArrowRight) {
        IVec2::new(1, 0)
    } else if keys.pressed(KeyCode::KeyW) || keys.pressed(KeyCode::ArrowUp) {
        IVec2::new(0, -1)
    } else if keys.pressed(KeyCode::KeyS) || keys.pressed(KeyCode::ArrowDown) {
        IVec2::new(0, 1)
    } else {
        IVec2::ZERO
    };
    let bomb = keys.just_pressed(KeyCode::Space) || keys.just_pressed(KeyCode::Enter);
    if bomb {
        // Instant local feedback; the keg itself appears when the snapshot
        // echoes back. Sound now, sight in ~100ms — feels responsive.
        sfx("place");
        stat("bombs_laid", 1);
    }
    if want != *last || bomb {
        *last = want;
        let wire = WireInput { t: "in".into(), d: [want.x, want.y], b: bomb };
        if let Ok(msg) = serde_json::to_string(&wire) {
            net_send(&msg);
        }
    }
}

/// Guest: applies host snapshots to the local scene and handles the ending.
#[allow(clippy::too_many_arguments, clippy::type_complexity)]
fn guest_apply(
    mut events: EventReader<NetIn>,
    net: Res<NetMode>,
    mut commands: Commands,
    mut arena: ResMut<Arena>,
    pfx: Res<PowderFx>,
    mut rng: ResMut<Rng>,
    mut fx: ResMut<GuestFx>,
    mut fighters: Query<(&mut Fighter, &mut Sprite, &mut Transform), Without<TileSprite>>,
    mut tiles_q: Query<(&TileSprite, &mut Sprite), Without<Fighter>>,
    mut final_score: ResMut<FinalScore>,
    mut next: ResMut<NextState<Phase>>,
) {
    if !net_guest(&net) {
        // Not a guest: host_net_input consumed what it needed; drop the rest.
        events.clear();
        return;
    }
    let my_seat = net.0.as_ref().map(|c| c.seat).unwrap_or(0) as usize;
    // Only the newest snapshot matters; ends are processed immediately.
    let mut latest_state: Option<WireState> = None;
    for ev in events.read() {
        if ev.left && ev.seat == 0 {
            // The simulation left the building. Bank what we know and end it.
            let kills = fighters
                .iter()
                .find(|(f, _, _)| f.seat == my_seat)
                .map(|(f, _, _)| f.kills)
                .unwrap_or(0);
            final_score.0 = kills * 100;
            next.set(Phase::GameOver);
            return;
        }
        if ev.left || ev.seat != 0 {
            continue; // only the host speaks state
        }
        let Ok(v) = serde_json::from_str::<serde_json::Value>(&ev.data) else { continue };
        match v.get("t").and_then(|t| t.as_str()) {
            Some("st") => {
                if let Ok(st) = serde_json::from_str::<WireState>(&ev.data) {
                    latest_state = Some(st);
                }
            }
            Some("end") if !arena.finished => {
                if let Ok(end) = serde_json::from_str::<WireEnd>(&ev.data) {
                    let total = end.ranks.len().max(2);
                    let rank = end
                        .ranks
                        .iter()
                        .find(|(s, _)| *s as usize == my_seat)
                        .map(|(_, r)| *r as usize)
                        .unwrap_or(total);
                    let kills = end
                        .kills
                        .iter()
                        .find(|(s, _)| *s as usize == my_seat)
                        .map(|(_, k)| *k)
                        .unwrap_or(0);
                    let alive = end.alive.iter().any(|&s| s as usize == my_seat);
                    if alive && end.alive.len() == 1 {
                        stat("cellar_wins", 1);
                    }
                    stat("kills", kills as u64);
                    // Same 2s winner banner the host shows, then the score
                    // screen — no more jump-cut past the ending.
                    arena.finished = true;
                    arena.finish_timer.reset();
                    arena.p1_score = placement_score(total, rank, kills, alive);
                    match end.alive.first() {
                        Some(&w) => celebrate(&mut commands, &mut rng, w as usize),
                        None => {
                            let e = text(&mut commands, "MUTUAL DESTRUCTION", 34.0, AMBER, Vec3::new(0.0, 0.0, 20.0));
                            commands.entity(e).insert((GameTag, Banner));
                        }
                    }
                }
            }
            _ => {}
        }
    }
    let Some(st) = latest_state else { return };

    // Tiles.
    for (i, ch) in st.tiles.chars().enumerate() {
        if i >= arena.tiles.len() {
            break;
        }
        arena.tiles[i] = match ch {
            '1' => Tile::Solid,
            '2' => Tile::Crate,
            _ => Tile::Empty,
        };
    }
    for (ts, mut sprite) in &mut tiles_q {
        let want = tile_color(arena.tiles[ts.0]);
        if sprite.color != want {
            sprite.color = want;
        }
    }
    arena.clock = st.clk as f32 / 10.0;
    // Sudden-death state rides along so the warning tint and the HUD read
    // correctly on guests too (spiral cell order is deterministic).
    arena.spiral_next = (st.sp as usize).min(arena.spiral.len());
    if st.sd != u32::MAX {
        let dur = arena.settle.duration().as_secs_f32();
        let elapsed = (dur - st.sd as f32 / 10.0).clamp(0.0, dur);
        arena.settle.set_elapsed(std::time::Duration::from_secs_f32(elapsed));
    }

    // Fighters: update targets; guest_smooth eases the sprites over.
    for wf in &st.f {
        for (mut f, mut sprite, _) in &mut fighters {
            if f.seat != wf.0 as usize {
                continue;
            }
            f.tile = IVec2::new(wf.1, wf.2);
            f.dir = IVec2::new(wf.3, wf.4);
            f.progress = wf.5 as f32 / 100.0;
            f.speed = wf.8 as f32 / 10.0;
            let was_alive = f.alive;
            f.alive = wf.6 == 1;
            f.kills = wf.7;
            if was_alive && !f.alive {
                sprite.color.set_alpha(0.15);
                sfx("death");
                if f.seat == my_seat {
                    stat("deaths", 1);
                }
            }
        }
    }

    // Bombs / flames / perks: diff the pools against the snapshot.
    let want_bombs: std::collections::HashSet<usize> = st.b.iter().map(|&(c, _)| c as usize).collect();
    fx.bombs.retain(|cell, e| {
        if want_bombs.contains(cell) {
            true
        } else {
            commands.entity(*e).despawn();
            false
        }
    });
    fx.fuses.retain(|cell, _| want_bombs.contains(cell));
    for &(cell, fuse) in &st.b {
        fx.fuses.insert(cell as usize, fuse as f32 / 100.0);
    }
    for &(cell, _) in &st.b {
        let cell = cell as usize;
        if !fx.bombs.contains_key(&cell) {
            sfx("place");
        }
        fx.bombs.entry(cell).or_insert_with(|| {
            let p = world((cell as i32) % COLS, (cell as i32) / COLS);
            spawn_keg(&mut commands, &pfx, p)
        });
    }
    let want_flames: std::collections::HashSet<usize> = st.fl.iter().map(|&c| c as usize).collect();
    fx.flames.retain(|cell, e| {
        if want_flames.contains(cell) {
            true
        } else {
            commands.entity(*e).despawn();
            false
        }
    });
    let mut new_flames = false;
    for &cell in &st.fl {
        let cell = cell as usize;
        if !fx.flames.contains_key(&cell) {
            new_flames = true;
        }
        fx.flames.entry(cell).or_insert_with(|| {
            let p = world((cell as i32) % COLS, (cell as i32) / COLS);
            commands
                .spawn((
                    Sprite { color: AMBER, custom_size: Some(Vec2::splat(CELL - 6.0)), ..default() },
                    Transform::from_xyz(p.x, p.y, 5.0),
                    GameTag,
                ))
                .id()
        });
    }
    if new_flames {
        sfx("boom");
    }
    let want_perks: std::collections::HashSet<usize> = st.p.iter().map(|&(c, _)| c as usize).collect();
    fx.perks.retain(|cell, e| {
        if want_perks.contains(cell) {
            true
        } else {
            commands.entity(*e).despawn();
            false
        }
    });
    for &(cell, kind) in &st.p {
        let cell = cell as usize;
        fx.perks.entry(cell).or_insert_with(|| {
            let p = world((cell as i32) % COLS, (cell as i32) / COLS);
            spawn_perk_sprite(&mut commands, &pfx, kind, p)
        });
    }
}

/// Guest render smoothing: dead-reckon each fighter forward at its known
/// speed between 20 Hz snapshots (clamped at the next tile center so we
/// never invent a turn), then ease the sprite toward that moving target.
/// Snaps across large jumps (spawn, respawn) so easing never smears.
fn guest_smooth(
    time: Res<Time>,
    net: Res<NetMode>,
    mut fighters: Query<(&mut Fighter, &mut Transform)>,
) {
    if !net_guest(&net) {
        return;
    }
    let dt = time.delta_secs();
    let blend = 1.0 - (-14.0 * dt).exp();
    for (mut f, mut tf) in &mut fighters {
        if f.alive && f.dir != IVec2::ZERO {
            // Chase a moving target, not a 20 Hz stairstep.
            f.progress = (f.progress + f.speed * dt).min(0.99);
        }
        let from = world(f.tile.x, f.tile.y);
        let to = world(f.tile.x + f.dir.x, f.tile.y + f.dir.y);
        let target = from.lerp(to, f.progress);
        let here = tf.translation.truncate();
        let next = if here.distance(target) > CELL * 2.5 {
            target // teleport-scale jump: snap
        } else {
            here.lerp(target, blend)
        };
        tf.translation.x = next.x;
        tf.translation.y = next.y;
    }
}

/// Kegs telegraph their fuse everywhere: a slow gray blink that reddens and
/// accelerates as the boom approaches. The wire already carried the fuse;
/// now somebody reads it.
/// Kegs breathe from the moment they land — a gentle swell that grows and
/// quickens as the fuse burns, with the body flashing red in the last
/// stretch (the flash swaps between two shared materials, so it's free).
fn bomb_telegraph(
    time: Res<Time>,
    net: Res<NetMode>,
    arena: Res<Arena>,
    fx: Res<GuestFx>,
    pfx: Res<PowderFx>,
    mut bombs: Query<(Entity, &mut MeshMaterial2d<ColorMaterial>, &mut Transform), With<BombSprite>>,
) {
    let mut fuses: std::collections::HashMap<Entity, f32> = std::collections::HashMap::new();
    if net_guest(&net) {
        for (cell, e) in &fx.bombs {
            if let Some(&fuse) = fx.fuses.get(cell) {
                fuses.insert(*e, fuse);
            }
        }
    } else {
        for b in arena.bombs.iter().flatten() {
            fuses.insert(b.0, b.1);
        }
    }
    for (e, mut mat, mut tf) in &mut bombs {
        let Some(&fuse) = fuses.get(&e) else { continue };
        let urgency = ((FUSE - fuse) / FUSE).clamp(0.0, 1.0);
        let swell = (time.elapsed_secs() * (5.0 + 7.0 * urgency)).sin();
        tf.scale = Vec3::splat(1.0 + (0.05 + 0.09 * urgency) * swell);
        let hot = fuse < 1.2 && (time.elapsed_secs() * (2.0 + urgency * 9.0)) as i32 % 2 == 0;
        let want = if hot { &pfx.keg_hot } else { &pfx.keg_mat };
        if mat.0 != *want {
            mat.0 = want.clone();
        }
    }
}

// ---- the cellar editor ----

/// Clears every keg, flame, perk, and banner; resets the round clocks.
fn reset_field(
    arena: &mut Arena,
    commands: &mut Commands,
    bombs: &Query<Entity, With<BombSprite>>,
    flames: &Query<Entity, With<FlameSprite>>,
    banners: &Query<Entity, With<Banner>>,
) {
    for e in bombs.iter().chain(flames.iter()).chain(banners.iter()) {
        commands.entity(e).despawn();
    }
    for b in arena.bombs.iter_mut() {
        *b = None;
    }
    for f in arena.flames.iter_mut() {
        *f = 0.0;
    }
    for p in arena.perks.iter_mut() {
        if let Some((_, e)) = p.take() {
            commands.entity(e).despawn();
        }
    }
    for p in arena.planted.iter_mut() {
        *p = None;
    }
    arena.clock = 0.0;
    arena.spiral_next = 0;
    arena.finished = false;
    arena.finish_timer.reset();
    arena.death_groups.clear();
    arena.end_sent = false;
    arena.settle.reset();
    arena.p1_score = 0;
}

#[allow(clippy::too_many_arguments, clippy::type_complexity)]
fn editor_update(
    buttons: Res<ButtonInput<MouseButton>>,
    keys: Res<ButtonInput<KeyCode>>,
    windows: Query<&Window>,
    cameras: Query<(&Camera, &GlobalTransform)>,
    mut gizmos: Gizmos,
    mut commands: Commands,
    editor: Option<ResMut<CellarEditor>>,
    arena: Option<ResMut<Arena>>,
    mut fighters: Query<
        (&mut Fighter, &mut Transform, &mut Sprite, &mut Visibility),
        (Without<TileSprite>, Without<FlameSprite>, Without<BombSprite>),
    >,
    mut tiles_q: Query<(&TileSprite, &mut Sprite), (Without<Fighter>, Without<FlameSprite>)>,
    bombs: Query<Entity, With<BombSprite>>,
    flames: Query<Entity, With<FlameSprite>>,
    banners: Query<Entity, With<Banner>>,
    mut hud: Query<(&mut Text2d, &mut TextFont), With<Hud>>,
) {
    let Some(mut editor) = editor else { return };
    let Some(mut arena) = arena else { return };

    // Return from a test round: X bails early, finish() requests it at the
    // natural end. Either way the canvas comes back untouched.
    if !editor.active {
        if editor.testing && (editor.want_return || keys.just_pressed(KeyCode::KeyX)) {
            editor.testing = false;
            editor.want_return = false;
            editor.active = true;
            reset_field(&mut arena, &mut commands, &bombs, &flames, &banners);
            arena.tiles = editor.tiles.clone();
            arena.planted = editor.planted.clone();
            for (_, _, _, mut vis) in &mut fighters {
                *vis = Visibility::Hidden;
            }
            sfx("tick");
        }
        return;
    }

    // The canvas paints straight onto the tile sprites; spawn rings show
    // where the twelve fighters would enter, and a colored square marks a
    // loaded crate's guaranteed drop.
    for (ts, mut sprite) in &mut tiles_q {
        let want = tile_color(editor.tiles[ts.0]);
        if sprite.color != want {
            sprite.color = want;
        }
    }
    for (i, planted) in editor.planted.iter().enumerate() {
        if let Some(p) = planted {
            let (c, r) = ((i as i32) % COLS, (i as i32) / COLS);
            let at = world(c, r);
            gizmos.rect_2d(at, Vec2::splat(CELL * 0.4), perk_color_by_kind(perk_kind(*p)));
        }
    }
    for (i, &(sc, sr)) in SPAWNS.iter().enumerate() {
        gizmos.circle_2d(world(sc, sr), CELL * 0.42, PLAYER_COLORS[i % 12].with_alpha(0.6));
    }
    if let Ok((mut t, mut tfnt)) = hud.single_mut() {
        let b = match (editor.perk_brush, editor.brush) {
            (Some(p), _) => format!("LOADED CRATE ({})", perk_name(p)),
            (None, Tile::Crate) => "CRATE".into(),
            (None, Tile::Solid) => "WALL".into(),
            (None, Tile::Empty) => "ERASE".into(),
        };
        let s = format!(
            "CELLAR EDITOR - BRUSH: {b}\n1 CRATE / 2 WALL / 3 ERASE / 4 LOADED CRATE (PRESS AGAIN TO PICK THE PERK) - LEFT-CLICK PAINTS - RIGHT-CLICK ERASES\nCOLORED SQUARES = GUARANTEED DROPS - RINGS = SPAWNS - S SAVES - G TEST-PLAYS - X RETURNS"
        );
        if t.0 != s {
            t.0 = s;
        }
        if tfnt.font_size != 13.0 {
            tfnt.font_size = 13.0;
        }
    }

    if keys.just_pressed(KeyCode::Digit1) {
        editor.brush = Tile::Crate;
        editor.perk_brush = None;
        sfx("tick");
    }
    if keys.just_pressed(KeyCode::Digit2) {
        editor.brush = Tile::Solid;
        editor.perk_brush = None;
        sfx("tick");
    }
    if keys.just_pressed(KeyCode::Digit3) {
        editor.brush = Tile::Empty;
        editor.perk_brush = None;
        sfx("tick");
    }
    if keys.just_pressed(KeyCode::Digit4) {
        // First press arms the loaded-crate brush; further presses cycle
        // through the seven perks.
        editor.perk_brush = Some(match editor.perk_brush {
            None => ALL_PERKS[0],
            Some(p) => {
                let i = ALL_PERKS.iter().position(|&x| x == p).unwrap_or(0);
                ALL_PERKS[(i + 1) % ALL_PERKS.len()]
            }
        });
        editor.brush = Tile::Crate;
        sfx("tick");
    }

    if keys.just_pressed(KeyCode::KeyS) {
        let mut tiles = editor.tiles.clone();
        enforce_shell(&mut tiles);
        let doc = PowderDoc {
            v: 1,
            name: String::new(),
            tiles: tiles
                .iter()
                .enumerate()
                .map(|(i, t)| match (t, editor.planted[i]) {
                    (Tile::Crate, Some(p)) => perk_char(p),
                    (Tile::Empty, _) => '0',
                    (Tile::Solid, _) => '1',
                    (Tile::Crate, None) => '2',
                })
                .collect(),
        };
        if let Ok(json) = serde_json::to_string(&doc) {
            crate::shell::save_level(&json);
            sfx("clear");
        }
        return;
    }

    if keys.just_pressed(KeyCode::KeyG) {
        editor.active = false;
        editor.testing = true;
        // The game HUD takes the line back at its own size.
        if let Ok((_, mut tfnt)) = hud.single_mut() {
            tfnt.font_size = 22.0;
        }
        let mut tiles = editor.tiles.clone();
        let mut planted = editor.planted.clone();
        enforce_shell(&mut tiles);
        clear_spawn_pockets(&mut tiles, &mut planted, fighters.iter().count());
        reset_field(&mut arena, &mut commands, &bombs, &flames, &banners);
        arena.tiles = tiles;
        arena.planted = planted;
        for (mut f, mut tf, mut sprite, mut vis) in &mut fighters {
            let (sc, sr) = SPAWNS[f.seat];
            f.tile = IVec2::new(sc, sr);
            f.dir = IVec2::ZERO;
            f.want = IVec2::ZERO;
            f.progress = 0.0;
            f.speed = 3.4;
            f.range = 2;
            f.max_bombs = 1;
            f.live_bombs = 0;
            f.alive = true;
            f.kills = 0;
            f.wants_bomb = false;
            f.kick = false;
            f.pierce = false;
            f.vest = false;
            f.phase = false;
            f.iframes = 0.0;
            let p = world(sc, sr);
            tf.translation.x = p.x;
            tf.translation.y = p.y;
            sprite.color.set_alpha(1.0);
            *vis = Visibility::Inherited;
        }
        sfx("coin");
        return;
    }

    // Paint. The border ring is immutable — the shell always holds.
    let place = buttons.pressed(MouseButton::Left);
    let erase = buttons.pressed(MouseButton::Right);
    if !place && !erase {
        return;
    }
    let Ok(window) = windows.single() else { return };
    let Ok((camera, cam_tf)) = cameras.single() else { return };
    let Some(w) = crate::retro::cursor_world(window, camera, cam_tf) else { return };
    let c = (w.x / CELL + (COLS as f32 - 1.0) / 2.0).round() as i32;
    let r = ((ROWS as f32 - 1.0) / 2.0 - (w.y - Y_OFF) / CELL).round() as i32;
    if c > 0 && c < COLS - 1 && r > 0 && r < ROWS - 1 {
        let v = if erase { Tile::Empty } else { editor.brush };
        let i = Arena::idx(c, r);
        if editor.tiles[i] != v {
            editor.tiles[i] = v;
        }
        editor.planted[i] = if erase || v != Tile::Crate { None } else { editor.perk_brush };
    }
}

/// Tints the next few cells the sudden-death spiral will claim — one tick of
/// warning before the wall closes. Runs after both tile-refresh paths so the
/// tint wins the frame on host and guest alike.
fn wall_warning(
    arena: Res<Arena>,
    mut tiles_q: Query<(&TileSprite, &mut Sprite), Without<Fighter>>,
) {
    if arena.clock < SUDDEN_DEATH_AT
        || arena.finished
        || arena.spiral_next >= arena.spiral.len().saturating_sub(9)
    {
        return;
    }
    let upcoming: Vec<usize> = arena.spiral[arena.spiral_next..]
        .iter()
        .take(3)
        .copied()
        .collect();
    for (ts, mut sprite) in &mut tiles_q {
        if upcoming.contains(&ts.0) && arena.tiles[ts.0] != Tile::Solid {
            sprite.color = Color::srgb(0.45, 0.12, 0.14);
        }
    }
}
