import { api, getToken } from '../app.js';
import { toast } from './toast-notification.js';
import { esc, orgFlag } from '../utils.js';

/**
 * The Top Secret arcade. Seven cabinets, all original builds compiled from
 * Rust to WebAssembly (Bevy engine), served same-origin from /arcade/.
 *
 * Credits are play tokens, NOT money: inserting one records a play against
 * your account (POST /arcade/:game/credit) and starts the cartridge. Scores
 * post back through window.__arcadeScore, which the cartridge calls on game
 * over. One shared wasm binary holds every game; the cabinet choice and
 * player counts are passed through window globals read at startup.
 *
 * Network play (chess, go, powder-keg, hexfection) uses a server-side room
 * relay at /api/v1/arcade/ws: the page owns the WebSocket and forwards
 * traffic to the cartridge via arcade_net_event / window.__arcadeNetSend.
 * Rules run in every player's cartridge; the server only manages seats and
 * ordered fan-out.
 *
 * Navigation note: a running Bevy app can't be torn down cleanly, so leaving
 * a cabinet after the cartridge booted forces a page reload (the session
 * survives via the refresh cookie). Arcade rules: you walk away, it resets.
 */
const CABINETS = [
  {
    id: 'chess', name: 'CHESS', tag: 'The old game. Take on the machine, a friend, or the network.',
    players: '1P vs machine · 2P hotseat · online', controls: 'Mouse — click a piece, then a destination. R twice resigns. Draws: 50-move, repetition, bare kings.',
  },
  {
    id: 'go', name: 'GO', tag: 'Surround territory on a 9×9 board. Two passes end it.',
    players: '1P vs machine · 2P hotseat · online', controls: 'Mouse places a stone · P passes · U undoes (local) · R-R resigns. After two passes: click dead groups, Enter accepts, M plays on.',
  },
  {
    id: 'comet-buster', name: 'COMET BUSTER', tag: 'Vector rocks, one ship, and now a saucer with opinions.',
    players: '1P', controls: '← → rotate · ↑ or ↓ thrust · Space fire · H hyperspace · Esc pause.',
  },
  {
    id: 'penny-pincher', name: 'PENNY PINCHER', tag: 'Grab every coin. The auditors want a word.',
    players: '1P', controls: 'Arrows or WASD · Esc pause. Gold bars turn the tables — they blink when time runs short.',
  },
  {
    id: 'brickfall', name: 'BRICKFALL', tag: 'Four-square bricks fall. Lines pay out. Speed climbs.',
    players: '1P', controls: '← → move · ↑/X rotate · ↓ soft drop · Space hard drop · C holds a piece · Esc pause. The dim outline is where it lands.',
  },
  {
    id: 'powder-keg', name: 'POWDER KEG', tag: 'Kegs, fuses, and up to a dozen rivals in the cellar.',
    players: 'up to 12 — local + bots, or online', controls: 'TO START: pick PLAYERS (bots fill the rest) and HUMANS, then INSERT CREDIT. P1: WASD + Space · P2 (local): arrows + Enter · online: either set · Esc pause (local). Perks from crates — RED longer blast · BLUE extra keg · GREEN faster · WHITE kicks kegs · ORANGE blasts pierce crates · CYAN vest survives one hit · PURPLE walks through crates.',
  },
  {
    id: 'hexfection', name: 'HEXFECTION', tag: 'Spread across the hex dish. Convert your neighbours.',
    players: 'up to 12 — hotseat, bots, or online', controls: 'Mouse — click your blob, then a target cell. Step 1 splits, jump 2 leaps; landing converts neighbours.',
  },
  {
    id: 'texas-holdem', name: "HOLD 'EM", tag: 'Tournament tables. Fictional chips, real grudges.',
    players: 'you vs 1-5 machine sharks · 2-6 online', controls: 'F fold · C check/call · R raise (pot) · A all-in · Esc pause (local). Blinds climb every 8 hands; last stack keeps the table. Online: the SERVER deals, so nobody\u2019s browser ever sees your hole cards.',
  },
  {
    id: 'red-tape', name: 'RED TAPE', tag: 'Paperwork from here to the ceiling. You have a ball.',
    players: '1P', controls: 'Mouse or ← → steers the paddle · Space (or click) serves and fires the stapler. Forms shred in one hit, red tape takes two, filing cabinets are forever. Falling perks: CYAN wide · MAGENTA split ball · GREEN slow · AMBER stapler · WHITE extra ball.',
  },
  {
    id: 'night-audit', name: 'NIGHT AUDIT', tag: 'After hours. Three objectives. Nobody gets hurt — they nap.',
    players: '1P mission · 2-12 online deathmatch · editor', controls: 'W/S walk · A/D strafe · ← → turn · Space fires a tranquilizer dart (watch the gun kick — a RED flash means YOU got hit) · E opens doors and plants the bug (on-screen prompts say when). MISSION: grab the 3 amber files, press E standing on the glowing green server tile, then walk out the green door. ONLINE: the OFFICE PARTY — host or join a room in the bar right below the screen; no guards, three darts and you nap, first to 10 tranqs (or best at the horn) wins.',
  },
  {
    id: 'lucky-penny', name: 'LUCKY PENNY', tag: 'A coin with a face and everything to prove. Three boards up.',
    players: '1P', controls: '← → (or Z and /) flip · Space launches. Knock the amber targets to open the ceiling hatch, ride up: BASEMENT → LOBBY → BOARDROOM. Five hits crack the VAULT for the jackpot. Falling through a floor only drops you a board — the basement drain costs a penny. Three pennies.',
  },
  {
    id: 'off-the-roof', name: 'OFF THE ROOF', tag: 'Fifteen gobs. One street. No witnesses.',
    players: '1P', controls: 'Overhead view: you shuffle along the ledge at the bottom — the mouse slides you left and right. HOLD LEFT to wind up: a marker crawls up the street showing where the gob lands; longer holds carry farther AND hit heavier (more points) — hold too long and it dribbles. RIGHT button winds up the MEGA GOB (costs 3, wide splash, triple weight). Scoring: base × lane (×1 near / ×2 road / ×3 far) × weight × chain (up to ×5) — bullseyes double. The manhole swish pays 500 and refunds 3 gobs.',
  },
  {
    id: 'pest-control', name: 'PEST CONTROL', tag: 'Somebody left a donut out. Now it\'s your problem.',
    players: '1P', controls: 'Mouse moves the swatter · click swats (short recovery — no flailing). Streak ×1-×5 for consecutive connecting swats; a whiff resets it. Two bugs under one swat = double each. WASPS: swat only during the calm amber beat — red means sting. After three waves, THE HORNET: dodge its dive, swat it dazed (green) for double damage.',
  },
  {
    id: 'interns', name: 'INTERNS', tag: 'The new hires walk. That\u2019s all they know. Save the quota.',
    players: '1P · 2P local · 2P online · editor', controls: 'P1: mouse assigns (hover shows who), 1-8 or click picks a job (MINE digs diagonally · BOOM self-destructs ONE intern after a 6s fuse, cratering terrain), T/R your flow, A/D or screen edge scrolls, click the minimap to jump, the red NUKE button (or N-N) is BOOM for your whole crew and ends the round, the >> button cycles game speed 1x-8x (F holds a burst on top). P2 (local): arrows + Enter, Q/E job. Esc pause (you can still assign).',
  },
  {
    id: 'bumper-chairs', name: 'BUMPER CHAIRS', tag: 'Last chair rolling wins. Facilities is furious.',
    players: '1P vs bots · 2-12 online · editor', controls: 'You ride BEHIND your chair, 16-bit kart style — the minimap top-right sees the whole garage. ← →/A D steer · ↑/W drives · Space uses your item. Three balloons each — staplers pop them. Supply crates drop: STAPLER · COFFEE PUDDLE (spins whoever rolls through) · ESPRESSO (boost) · TRIPLE STAPLER. Last chair holding a balloon wins; at the horn, most balloons.',
  },
  {
    id: 'roll-call', name: 'ROLL CALL', tag: 'Five dice, three throws, thirteen boxes, no excuses.',
    players: '1P score attack · 2-8 online', controls: 'R (or Space) rolls · click dice to hold them · click a row on the sheet to score it. Upper-section bonus pays 35 at 63+, FIVE ALIKE pays 50, zeroes are forever. Highest sheet takes the table.',
  },
  {
    id: 'floor-plan', name: 'FLOOR PLAN', tag: 'Forty spaces of prime office real estate. Rent is due.',
    players: '1P vs 3 bots · 2-8 online', controls: 'R rolls · B buys the deed, N passes · click a wing you own (full color set) to build desks · E ends the turn. HR REVIEW: P pays 50 or R rolls for doubles — three doubles sends you there. Bankrupt the floor, or be richest when the fiscal year ends.',
  },
  {
    id: 'homestead', name: 'HOMESTEAD', tag: 'Nineteen lots. Five supplies. One inspector.',
    players: '1P vs 3 bots · 3-4 online', controls: 'Draft two outposts, then R rolls each turn — lots pay COFFEE, PAPER, TONER, SNACKS, and STAPLES. Click corners for outposts (click yours again for an office), edges for halls · I buys an idea · G plays a guard · T trades 4:1 · E ends. A 7 wakes THE INSPECTOR. First to 10 points owns the park.',
  },
];

// Local seat pickers (hotseat/bots) for the big cabinets.
const MULTI = { 'powder-keg': { min: 2, max: 12, humans: 2 }, hexfection: { min: 2, max: 12, humans: 12 }, 'texas-holdem': { min: 2, max: 6, humans: 1 }, 'bumper-chairs': { min: 2, max: 12, humans: 1 } };
// Local mode pickers: fixed seat count, choice of how many humans sit down.
const MODES = { chess: [{ label: 'VS MACHINE', humans: 1 }, { label: '2P HOTSEAT', humans: 2 }], go: [{ label: 'VS MACHINE', humans: 1 }, { label: '2P HOTSEAT', humans: 2 }], interns: [{ label: '1 PLAYER', humans: 1 }, { label: '2P LOCAL', humans: 2 }] };
// Networked cabinets: seat ranges for hosting a room.
const NET = { chess: { min: 2, max: 2 }, go: { min: 2, max: 2 }, 'powder-keg': { min: 2, max: 12 }, hexfection: { min: 2, max: 12 }, interns: { min: 2, max: 2 }, 'texas-holdem': { min: 2, max: 6 }, 'night-audit': { min: 2, max: 12 }, 'bumper-chairs': { min: 2, max: 12 }, 'roll-call': { min: 2, max: 8 }, 'floor-plan': { min: 2, max: 8 }, homestead: { min: 3, max: 4 } };
// Level-capable cabinets: house options, the editor's blank-canvas label,
// and the toast shown when the editor opens.
const LEVELS = {
  interns: {
    house: [['b1', 'HOUSE: ORIENTATION DAY'], ['b2', 'HOUSE: THE BASEMENT'], ['b3', 'HOUSE: CUBICLE WALLS'], ['b4', 'HOUSE: TWO TOWERS (WIDE)'], ['b5', 'HOUSE: THE MINESHAFT'], ['b6', 'HOUSE: THE CROSSING (WIDE)'], ['b7', 'HOUSE: OBSTACLE COURSE'], ['b8', 'HOUSE: HEADQUARTERS (WIDE)']],
    blank: 'EDITOR: BLANK CANVAS', playFallback: { builtin: 1 },
    toast: 'Editor: paint (Shift+click lines, F mirrors, U undoes), W widens, S saves, G test-plays, X returns',
  },
  'powder-keg': {
    house: [['std', 'HOUSE: RANDOM CELLAR']],
    blank: 'EDITOR: BLANK CELLAR', playFallback: null,
    toast: 'Editor: 1 crate / 2 wall / 3 erase / 4 loaded crate (press 4 again to pick its perk), click paints, rings mark spawns, S saves, G test-plays, X returns',
  },
  hexfection: {
    house: [['std', 'HOUSE: STANDARD DISH']],
    blank: 'EDITOR: PUNCH HOLES', playFallback: null, localOnly: true,
    toast: 'Editor: click punches or fills holes (starting blobs stay), S saves, G test-plays 12 seats, X returns',
  },
  'night-audit': {
    house: [['std', 'MISSION: THE LEDGER JOB']],
    blank: 'EDITOR: EMPTY OFFICE', playFallback: null,
    toast: 'Editor: 1-0 pick brushes (walls, doors, files, guards, spawns), X/V/P place exit/console/start, click paints, right-click erases, Shift+S saves, G test-plays, X returns',
  },
  chess: {
    house: [['std', 'STANDARD GAME'], ['fischer', 'FISCHER RANDOM']],
    blank: 'EDITOR: SET UP A POSITION', playFallback: null,
    toast: 'Editor: 1-6 picks a piece, C flips its color, click places, right-click clears, T sets who moves, G test-plays, S saves, X returns',
  },
  'bumper-chairs': {
    house: [['std', 'HOUSE: THE PARKING GARAGE']],
    blank: 'EDITOR: EMPTY GARAGE', playFallback: null,
    toast: 'Editor: 1 wall / 2 floor / 3 crate spot / 4 spawn, click paints, right-click erases, Shift+S saves (needs 2+ spawns), G test-plays, X returns',
  },
};
// Clock-capable cabinets: per-side game clock choices, in seconds. Online,
// the host's pick travels with the board setup so both chairs agree.
const CLOCKS = {
  chess: [[0, 'NO CLOCK'], [180, 'BLITZ · 3 MIN'], [300, 'BLITZ · 5 MIN'], [600, 'RAPID · 10 MIN'], [1800, 'CLASSICAL · 30 MIN']],
};

/**
 * A tiny WebAudio chip synth for the cartridges (window.__arcadeSfx).
 * The context is created lazily on the first effect — always after the
 * coin-click gesture, so autoplay policy never mutes it. Square waves and
 * one noise burst; no samples, no network.
 */
let _actx = null;
function _ctx() {
  if (!_actx) {
    try { _actx = new (window.AudioContext || window.webkitAudioContext)(); } catch { return null; }
  }
  if (_actx.state === 'suspended') _actx.resume().catch(() => {});
  return _actx;
}
function _tone(f0, f1, dur, delay = 0, vol = 0.08, type = 'square') {
  const ctx = _ctx();
  if (!ctx) return;
  const t0 = ctx.currentTime + delay;
  const osc = ctx.createOscillator();
  const gain = ctx.createGain();
  osc.type = type;
  osc.frequency.setValueAtTime(f0, t0);
  if (f1 !== f0) osc.frequency.exponentialRampToValueAtTime(Math.max(f1, 1), t0 + dur);
  gain.gain.setValueAtTime(vol, t0);
  gain.gain.exponentialRampToValueAtTime(0.0008, t0 + dur);
  osc.connect(gain).connect(ctx.destination);
  osc.start(t0);
  osc.stop(t0 + dur + 0.02);
}
// One shared noise buffer, built once: allocating a fresh ~50KB AudioBuffer
// per explosion was steady garbage that surfaced as occasional frame hitches
// (most visible in COMET BUSTER's smooth drift).
let _noiseBuf = null;
function _noise(dur, delay = 0, vol = 0.1) {
  const ctx = _ctx();
  if (!ctx) return;
  const t0 = ctx.currentTime + delay;
  if (!_noiseBuf) {
    const len = Math.floor(ctx.sampleRate * 0.6);
    _noiseBuf = ctx.createBuffer(1, len, ctx.sampleRate);
    const data = _noiseBuf.getChannelData(0);
    for (let i = 0; i < len; i++) data[i] = (Math.random() * 2 - 1) * (1 - i / len);
  }
  const src = ctx.createBufferSource();
  src.buffer = _noiseBuf;
  const filt = ctx.createBiquadFilter();
  filt.type = 'lowpass';
  filt.frequency.value = 900;
  const gain = ctx.createGain();
  gain.gain.setValueAtTime(vol, t0);
  gain.gain.exponentialRampToValueAtTime(0.001, t0 + dur);
  src.connect(filt).connect(gain).connect(ctx.destination);
  src.start(t0);
  src.stop(t0 + dur + 0.02);
}
// The thrust rumble reuses ONE always-running oscillator whose gain is
// re-fired on every tick — thrusting is continuous, and spawning fresh
// nodes seven times a second built up garbage the collector paid for later.
let _thrustOsc = null;
let _thrustGain = null;
function _thrust() {
  const ctx = _ctx();
  if (!ctx) return;
  if (!_thrustOsc) {
    _thrustOsc = ctx.createOscillator();
    _thrustOsc.type = 'square';
    _thrustOsc.frequency.value = 85;
    _thrustGain = ctx.createGain();
    _thrustGain.gain.value = 0;
    _thrustOsc.connect(_thrustGain).connect(ctx.destination);
    _thrustOsc.start();
  }
  const t = ctx.currentTime;
  _thrustGain.gain.cancelScheduledValues(t);
  _thrustGain.gain.setValueAtTime(0.035, t);
  _thrustGain.gain.exponentialRampToValueAtTime(0.0008, t + 0.2);
}
const SFX = {
  coin:    () => { _tone(880, 880, 0.06); _tone(1320, 1320, 0.09, 0.07); },
  place:   () => _tone(220, 220, 0.05),
  capture: () => _tone(330, 165, 0.09),
  tick:    () => _tone(660, 660, 0.03, 0, 0.04),
  clear:   () => { _tone(440, 440, 0.06); _tone(587, 587, 0.06, 0.07); _tone(880, 880, 0.1, 0.14); },
  drop:    () => _tone(150, 110, 0.06),
  boom:    () => _noise(0.25),
  death:   () => { _tone(392, 392, 0.09); _tone(311, 311, 0.09, 0.1); _tone(196, 98, 0.22, 0.2); },
  power:   () => _tone(400, 950, 0.16),
  eat:     () => _tone(500, 950, 0.07),
  fire:    () => _tone(720, 280, 0.06, 0, 0.05),
  hyper:   () => { _tone(300, 900, 0.08); _tone(900, 300, 0.08, 0.09); },
  saucer:  () => { _tone(520, 640, 0.1, 0, 0.05); _tone(640, 520, 0.1, 0.11, 0.05); },
  extra:   () => { for (let i = 0; i < 4; i++) _tone(523 * (1 + i * 0.25), 523 * (1 + i * 0.25), 0.08, i * 0.09); },
  over:    () => { _tone(523, 523, 0.12); _tone(392, 392, 0.12, 0.13); _tone(311, 311, 0.12, 0.26); _tone(262, 131, 0.4, 0.39); },
  pause:   () => _tone(440, 440, 0.05, 0, 0.05),
  thrust:  () => _thrust(),
  rotate:  () => _tone(540, 620, 0.03, 0, 0.035),
  levelup: () => { _tone(523, 523, 0.07); _tone(659, 659, 0.07, 0.08); _tone(784, 784, 0.07, 0.16); _tone(1047, 1047, 0.12, 0.24); },
  buzz:    () => _tone(140, 110, 0.12, 0, 0.06, 'sawtooth'),
  win:     () => { for (let i = 0; i < 5; i++) _tone(523 * Math.pow(1.2, i), 523 * Math.pow(1.2, i), 0.09, i * 0.1); },
  chip:    () => _tone(170 + Math.random() * 50, 130, 0.04, 0, 0.05),
  chute:   () => _tone(880, 320, 0.22, 0, 0.035, 'triangle'),
};
window.__arcadeSfx = name => { try { SFX[name]?.(); } catch { /* silence is golden */ } };

// Service-record labels: every counter a cartridge reports, with the
// display copy it deserves. Unknown keys fall back to SHOUTING_SNAKE_CASE.
const STAT_LABELS = {
  seconds_played: 'TIME ON THE FLOOR',
  rounds_finished: 'ROUNDS FINISHED',
  // Comet Buster
  bullets_fired: 'BULLETS FIRED', rocks_smashed: 'ROCKS SMASHED',
  saucers_downed: 'SAUCERS DOWNED', ships_lost: 'SHIPS LOST',
  hyperspace_jumps: 'PANIC BUTTON PRESSES', hyperspace_misfires: 'EATEN BY OWN HYPERDRIVE',
  waves_cleared: 'WAVES CLEARED', extra_ships: 'EXTRA SHIPS EARNED',
  // Penny Pincher
  coins_pocketed: 'COINS POCKETED', gold_bars: 'GOLD BARS SEIZED',
  auditors_bitten: 'AUDITORS BITTEN', times_audited: 'AUDITS FAILED (CAUGHT)',
  shifts_cleared: 'SHIFTS SURVIVED', tunnel_trips: 'TUNNEL COMMUTES',
  extra_lives: 'EXTRA LIVES EARNED', about_faces: 'PANICKED U-TURNS',
  // Brickfall
  pieces_locked: 'BRICKS LAID', lines_cleared: 'LINES CLEARED',
  quads: 'FOUR-LINERS', hard_drops: 'SLAMS', soft_cells: 'CELLS NUDGED DOWN',
  holds_used: 'BRICKS POCKETED FOR LATER', top_outs: 'CEILINGS MET',
  levels_reached: 'GEARS SHIFTED UP',
  // Chess
  moves_played: 'MOVES PLAYED', captures_made: 'PIECES TAKEN',
  pieces_lost: 'PIECES DONATED', checks_given: 'CHECKS DELIVERED',
  pawns_promoted: 'PAWNS PROMOTED', knight_promotions: 'PROMOTED TO KNIGHT (WHY?)',
  machine_beaten: 'MACHINE HUMBLED', beaten_by_machine: 'HUMBLED BY MACHINE',
  draws: 'PEACE TREATIES', wins_online: 'ONLINE WINS', losses_online: 'ONLINE LOSSES',
  resignations: 'TABLES FLIPPED (RESIGNED)', hotseat_rounds: 'HOTSEAT ROUNDS',
  fischer_deals: 'FISCHER DEALS TAKEN', puzzles_tested: 'PUZZLES TESTED',
  time_forfeits: 'FLAGS FALLEN (CLOCK RAN OUT)', wins_on_time: 'WINS ON TIME',
  // Go
  stones_placed: 'STONES PLACED', stones_captured: 'PRISONERS TAKEN',
  stones_lost: 'STONES SURRENDERED', passes: 'POLITE PASSES',
  takebacks: 'TAKEBACKS BEGGED', dead_stones_marked: 'STONES DECLARED DEAD',
  // Powder Keg
  bombs_laid: 'KEGS PLANTED', kills: 'RIVALS RETIRED', deaths: 'TIMES RETIRED',
  self_demolitions: 'SELF-DEMOLITIONS', crates_smashed: 'CRATES SMASHED',
  perks_grabbed: 'UPGRADES LOOTED', kegs_kicked: 'KEGS PUNTED',
  vests_shredded: 'VESTS TAKEN FOR THE TEAM',
  wall_crushes: 'FLATTENED BY MASONRY', steps_walked: 'TILES SPRINTED',
  cellar_wins: 'CELLARS KEPT', settle_draws: 'MUTUAL STANDOFFS',
  // Hexfection
  clones: 'BLOBS SPLIT', jumps: 'BLOBS LEAPT', blobs_converted: 'NEIGHBOURS CONVERTED',
  blobs_lost: 'BLOBS LOST TO THE CAUSE', times_consumed: 'TIMES CONSUMED ENTIRELY',
  dish_wins: 'DISHES TAKEN', seats_skipped: 'RIVALS BOXED IN',
  // HOLD 'EM
  hands_played: 'HANDS DEALT IN', hands_won: 'POTS DRAGGED',
  chips_won: 'FICTIONAL CHIPS WON', folds: 'DISCRETION EXERCISED (FOLDS)',
  raises: 'PRESSURE APPLIED (RAISES)', all_ins: 'CHIPS SHOVED (ALL-INS)',
  bust_outs: 'TIMES FELTED', tables_swept: 'TABLES SWEPT',
  royal_flushes: 'ROYAL FLUSHES (FRAME THIS)', straight_flushes: 'STRAIGHT FLUSHES',
  quads_made: 'FOUR-OF-A-KINDS SHOWN', full_houses: 'FULL HOUSES SHOWN',
  flushes_shown: 'FLUSHES SHOWN', straights_shown: 'STRAIGHTS SHOWN',
  // INTERNS
  interns_saved: 'INTERNS RESCUED', interns_lost: 'INTERNS LOST (REGRETTABLE)',
  gravity_lessons: 'GRAVITY LESSONS TAUGHT', quits_ordered: 'LOUD QUITS ARRANGED',
  nukes_ordered: 'MASS RESIGNATIONS FILED', climbers_hired: 'CLIMBERS CERTIFIED',
  miners_deployed: 'MINERS SENT DIAGONAL',
  medals_gold: 'GOLD MEDALS (90%+ SAVED)', medals_silver: 'SILVER MEDALS (75%+ SAVED)',
  medals_bronze: 'BRONZE MEDALS (QUOTA MET)',
  // RED TAPE
  forms_shredded: 'FORMS SHREDDED', tape_cut: 'RED TAPE CUT',
  balls_dropped: 'BALLS FILED UNDER THE DESK', perks_caught: 'OFFICE SUPPLIES CAUGHT',
  staples_fired: 'STAPLES DISCHARGED', desks_cleared: 'DESKS CLEARED', extra_balls: 'SPARE BALLS EARNED',
  // NIGHT AUDIT
  files_lifted: 'INTEL FILES LIFTED', guards_tranqed: 'GUARDS PUT TO BED',
  darts_fired: 'DARTS EXPENSED', times_spotted: 'TIMES SPOTTED (EMBARRASSING)',
  coffees_drunk: 'MIDNIGHT COFFEES', servers_bugged: 'SERVERS BUGGED',
  extractions: 'CLEAN EXTRACTIONS', ghost_runs: 'GHOST RUNS (NEVER SEEN)',
  audits_failed: 'NIGHTS THAT WENT LOUD',
  // OFF THE ROOF
  gobs_hocked: 'GOBS HOCKED', splats: 'DIRECT HITS', bullseyes: 'DEAD-CENTER BULLSEYES',
  mega_gobs: 'MEGA GOBS UNLEASHED', manholes: 'MANHOLE SWISHES', chains_maxed: 'CHAINS MAXED (X5)',
  // PEST CONTROL
  swats: 'SWATS TAKEN', bugs_swatted: 'BUGS DISPATCHED', whiffs: 'AIR SWATTED (WHIFFS)',
  stings: 'STINGS ABSORBED', multi_kills: 'TWO-FOR-ONES', hornets_downed: 'HORNETS DOWNED',
  shifts_cleared: 'SHIFTS SURVIVED (PESTS)',
  // LUCKY PENNY
  flips: 'FLIPPER FLIPS', bumpers_bounced: 'BUMPERS RUNG',
  targets_knocked: 'TARGETS FLATTENED', boards_climbed: 'FLOORS ASCENDED',
  jackpots: 'VAULTS CRACKED', pennies_lost: 'PENNIES DOWN THE DRAIN',
  chutes_issued: 'PARACHUTES ISSUED', supervisors_promoted: 'SUPERVISORS PROMOTED',
  bridges_ordered: 'BRIDGES COMMISSIONED', bashers_unleashed: 'WALLS EXPENSED',
  diggers_deployed: 'FLOORS EXCAVATED', floor_wins: 'QUOTAS CRUSHED',
  // BUMPER CHAIRS
  items_used: 'ITEMS DEPLOYED', staplers_thrown: 'STAPLERS THROWN',
  boxes_grabbed: 'SUPPLY CRATES RAIDED', balloons_lost: 'BALLOONS SURRENDERED',
  balloons_popped: 'BALLOONS POPPED (THEIRS)', chairs_lost: 'CHAIRS RETIRED',
  floors_taken: 'GARAGES TAKEN',
  // ROLL CALL
  rolls_thrown: 'DICE THROWN', boxes_filled: 'BOXES FILLED',
  five_alikes: 'FIVE ALIKES (THE BIG 50)', zeroes_taken: 'ZEROES SWALLOWED',
  tables_won: 'TABLES TAKEN',
  // FLOOR PLAN
  deeds_bought: 'DEEDS BOUGHT', desks_built: 'DESKS BUILT',
  rent_paid: 'RENT COUGHED UP', rent_collected: 'RENT COLLECTED',
  hr_visits: 'HR REVIEWS ATTENDED', laps_completed: 'PAYDAYS COLLECTED',
  floors_owned: 'FLOORS OWNED OUTRIGHT',
  // HOMESTEAD
  halls_built: 'HALLS LAID', outposts_founded: 'OUTPOSTS FOUNDED',
  offices_upgraded: 'OFFICES OPENED', ideas_drawn: 'IDEAS FILED',
  guards_played: 'GUARDS DEPLOYED', inspector_moves: 'INSPECTOR RELOCATIONS',
  resources_gained: 'SUPPLIES COLLECTED', homesteads_won: 'PARKS OWNED',
};
function statLabel(key) {
  return STAT_LABELS[key] ?? key.replace(/_/g, ' ').toUpperCase();
}
function statValue(key, v) {
  if (key === 'seconds_played') {
    const h = Math.floor(v / 3600), m = Math.floor((v % 3600) / 60);
    return h > 0 ? `${h}H ${m}M` : `${m}M ${v % 60}S`;
  }
  return Number(v).toLocaleString();
}

// Cabinets steered by keys: these get the click-to-refocus overlay.
const KEY_GAMES = new Set(['brickfall', 'comet-buster', 'penny-pincher', 'powder-keg', 'interns', 'texas-holdem', 'red-tape', 'night-audit', 'lucky-penny', 'off-the-roof', 'pest-control', 'bumper-chairs', 'roll-call', 'floor-plan', 'homestead']);

function gameFromHash() {
  const q = (location.hash.split('?')[1]) ?? '';
  return new URLSearchParams(q).get('g');
}

class PageArcade extends HTMLElement {
  connectedCallback() {
    this._wasmStarted = false;
    this._ws = null;
    // Admin switch (Settings → arcade_visible). The API is gated server-side
    // too; this handles anyone arriving via a bookmark once the nav is gone.
    if (orgFlag('arcade_visible') === 'off') { this.renderDarkFloor(); return; }
    const g = gameFromHash();
    const cab = CABINETS.find(c => c.id === g);
    if (cab) this.renderCabinet(cab); else this.renderFloor();
  }

  renderDarkFloor() {
    this.innerHTML = `
      <div style="font-family:ui-monospace,'Cascadia Mono','Fira Mono',monospace;background:#0a0a12;border:1px solid #232338;border-radius:8px;padding:2.2rem;text-align:center;color:#8f8fa8">
        <div style="font-size:1.2rem;letter-spacing:.35em;color:#5a5a72">LIGHTS OUT</div>
        <p style="font-size:.85rem;margin-top:.8rem">The basement is locked. An administrator has switched the arcade off.</p>
      </div>`;
  }

  disconnectedCallback() {
    try { this._ws?.close(); } catch { /* already gone */ }
    // A booted cartridge keeps its render loop on a detached canvas; the only
    // clean teardown is a reload. The refresh cookie keeps the user signed in.
    if (this._wasmStarted) setTimeout(() => location.reload(), 0);
  }

  // ---- the floor ----

  async renderFloor() {
    this.innerHTML = `
      <style>
        .tsx-wrap { font-family: ui-monospace, 'Cascadia Mono', 'Fira Mono', monospace; }
        .tsx-head { background:#0a0a12; border:1px solid #232338; border-radius:8px; padding:1.1rem 1.4rem; margin-bottom:1rem;
                    color:#e8e8f0; position:relative; overflow:hidden; }
        .tsx-head::after { content:''; position:absolute; inset:0; pointer-events:none;
                    background:repeating-linear-gradient(0deg, rgba(0,0,0,.25) 0 1px, transparent 1px 3px); }
        .tsx-title { font-size:1.5rem; letter-spacing:.35em; color:#7CFC9A; text-shadow:0 0 8px rgba(124,252,154,.7); margin:0; }
        .tsx-sub { color:#8f8fa8; font-size:.8rem; margin-top:.3rem; }
        .tsx-stamp { position:absolute; top:.7rem; right:1rem; border:2px solid #ff5577; color:#ff5577; padding:.1rem .5rem;
                    font-size:.68rem; letter-spacing:.2em; transform:rotate(6deg); opacity:.85; }
        .tsx-grid { display:grid; grid-template-columns:repeat(auto-fill,minmax(250px,1fr)); gap:.9rem; }
        .tsx-cab { background:#0a0a12; border:1px solid #232338; border-radius:8px; padding:1rem 1.1rem; color:#cfcfe0;
                   cursor:pointer; transition:transform .08s ease, box-shadow .08s ease; position:relative; overflow:hidden; }
        .tsx-cab:hover { transform:translateY(-2px); box-shadow:0 0 14px rgba(124,252,154,.25); }
        .tsx-cab h3 { color:#7CFC9A; letter-spacing:.18em; font-size:1rem; margin:0 0 .3rem; text-shadow:0 0 6px rgba(124,252,154,.6); }
        .tsx-cab .tag { font-size:.78rem; color:#9a9ab2; min-height:2.2em; }
        .tsx-cab .meta { font-size:.72rem; color:#6d6d88; margin-top:.55rem; display:flex; justify-content:space-between; }
        .tsx-cab .hs { color:#ffd166; }
      </style>
      <div class="tsx-wrap">
        <div class="tsx-head">
          <span class="tsx-stamp">CLASSIFIED</span>
          <h1 class="tsx-title">TOP SECRET</h1>
          <div class="tsx-sub">Basement arcade. Credits are play tokens — logged to your account, never billed. You didn't see this room.</div>
        </div>
        <div class="tsx-head" id="challenge" style="display:none;margin-bottom:1rem"></div>
        <div class="tsx-grid" id="floor">${CABINETS.map(c => `
          <div class="tsx-cab" data-g="${esc(c.id)}" role="button" tabindex="0" aria-label="Play ${esc(c.name)}">
            <h3>${esc(c.name)}</h3>
            <div class="tag">${esc(c.tag)}</div>
            <div class="meta"><span>${esc(c.players)}</span><span id="m-${esc(c.id)}">…</span></div>
            <div class="meta"><span></span><span class="hs" id="h-${esc(c.id)}"></span></div>
          </div>`).join('')}
        </div>
        <div class="tsx-head" style="margin-top:1rem">
          <h2 class="tsx-title" style="font-size:1.05rem">SERVICE RECORDS</h2>
          <div class="tsx-sub" style="display:flex;gap:.6rem;align-items:center;flex-wrap:wrap;margin-top:.5rem">
            PERSONNEL FILE OF
            <select id="rec-player" style="background:#11121f;color:#7CFC9A;border:1px solid #232338;
                    font-family:inherit;font-size:.8rem;padding:.2rem .4rem"></select>
            <span style="color:#6d6d88">— every deed on this floor is a matter of public record.</span>
          </div>
          <div id="rec-cards" class="tsx-grid" style="margin-top:.8rem"></div>
        </div>
      </div>`;
    this.loadChallenge();
    this.querySelectorAll('.tsx-cab').forEach(el => {
      const open = () => { location.hash = `#/arcade?g=${el.dataset.g}`; };
      el.addEventListener('click', open);
      el.addEventListener('keydown', e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); open(); } });
    });
    this.wireRecords();
    try {
      const stats = await api('GET', '/arcade/stats') ?? [];
      for (const s of stats) {
        const m = this.querySelector(`#m-${CSS.escape(s.game)}`);
        const h = this.querySelector(`#h-${CSS.escape(s.game)}`);
        if (m) m.textContent = `${s.total_plays} play${s.total_plays === 1 ? '' : 's'}`;
        if (h && s.high_score > 0) h.textContent = `HI ${s.high_score.toLocaleString()} — ${s.high_scorer}`;
      }
    } catch { /* stats are decoration */ }
  }

  // ---- one cabinet ----

  renderCabinet(cab) {
    const multi = MULTI[cab.id];
    const net = NET[cab.id];
    this.innerHTML = `
      <style>
        .tsc-wrap { font-family: ui-monospace, 'Cascadia Mono', 'Fira Mono', monospace; color:#cfcfe0; }
        .tsc-bar { display:flex; align-items:baseline; gap:1rem; margin-bottom:.8rem; flex-wrap:wrap; }
        .tsc-back { color:#7CFC9A; text-decoration:none; font-size:.8rem; letter-spacing:.1em; }
        .tsc-name { color:#7CFC9A; font-size:1.3rem; letter-spacing:.3em; margin:0; text-shadow:0 0 8px rgba(124,252,154,.7); }
        .tsc-cols { display:flex; gap:1rem; align-items:flex-start; flex-wrap:wrap; }
        .tsc-bezel { background:#0a0a12; border:1px solid #232338; border-radius:10px; padding:14px; position:relative; }
        .tsc-bezel::after { content:''; position:absolute; inset:14px; pointer-events:none; border-radius:4px;
                    background:repeating-linear-gradient(0deg, rgba(0,0,0,.18) 0 1px, transparent 1px 3px); }
        #arcade-canvas { display:block; width:720px; max-width:100%; height:auto; background:#000; border-radius:4px; outline:none; }
        @keyframes tsc-blink { 50% { opacity: 0; } }
        .tsc-slot { margin-top:.8rem; display:flex; gap:.7rem; align-items:center; flex-wrap:wrap; }
        .tsc-coin { background:#101020; color:#ffd166; border:2px solid #ffd166; border-radius:6px; padding:.55rem 1.2rem;
                    font-family:inherit; font-size:.9rem; letter-spacing:.15em; cursor:pointer; text-shadow:0 0 6px rgba(255,209,102,.6); }
        .tsc-coin:hover:not(:disabled) { box-shadow:0 0 12px rgba(255,209,102,.45); }
        .tsc-coin:disabled { opacity:.45; cursor:default; }
        .tsc-net { margin-top:.6rem; border-top:1px dashed #232338; padding-top:.6rem; display:flex; gap:.6rem; align-items:center; flex-wrap:wrap; }
        .tsc-btn { background:#101020; color:#7CFC9A; border:1px solid #7CFC9A; border-radius:6px; padding:.4rem .9rem;
                   font-family:inherit; font-size:.8rem; letter-spacing:.1em; cursor:pointer; }
        .tsc-btn:hover:not(:disabled) { box-shadow:0 0 10px rgba(124,252,154,.4); }
        .tsc-btn:disabled { opacity:.4; cursor:default; }
        .tsc-btn.warm { color:#ff77ff; border-color:#ff77ff; }
        .tsc-note { font-size:.72rem; color:#6d6d88; }
        .tsc-code { background:#101020; border:1px solid #232338; color:#ffd166; border-radius:4px; padding:.35rem .5rem;
                    font-family:inherit; width:6ch; text-transform:uppercase; letter-spacing:.2em; }
        .tsc-lobby { font-size:.8rem; color:#ffd166; letter-spacing:.08em; }
        .tsc-rail { background:#0a0a12; border:1px solid #232338; border-radius:8px; padding:1rem 1.1rem; min-width:230px; flex:1; max-width:330px; }
        .tsc-rail h3 { color:#ff77ff; letter-spacing:.2em; font-size:.85rem; margin:0 0 .5rem; text-shadow:0 0 6px rgba(255,119,255,.5); }
        .tsc-rail table { width:100%; font-size:.78rem; border-collapse:collapse; }
        .tsc-rail td { padding:.15rem 0; border:0; color:#cfcfe0; }
        .tsc-rail td.sc { text-align:right; color:#ffd166; font-variant-numeric:tabular-nums; }
        .tsc-you { margin-top:.7rem; font-size:.75rem; color:#8f8fa8; }
        .tsc-ctl { margin-top:.7rem; font-size:.72rem; color:#6d6d88; border-top:1px dashed #232338; padding-top:.6rem; }
        .tsc-sel { background:#101020; color:#cfcfe0; border:1px solid #232338; border-radius:4px; padding:.3rem .4rem; font-family:inherit; }
      </style>
      <div class="tsc-wrap">
        <div class="tsc-bar">
          <a class="tsc-back" href="#/arcade" id="back">◄ THE FLOOR</a>
          <h1 class="tsc-name">${esc(cab.name)}</h1>
          <span class="tsc-note">${esc(cab.players)}</span>
        </div>
        <div class="tsc-cols">
          <div>
            <div class="tsc-bezel" style="position:relative">
              <canvas id="arcade-canvas" width="720" height="640"></canvas>
              <div id="boot-veil" style="position:absolute;inset:14px;display:flex;pointer-events:none;z-index:4;
                   color:#7CFC9A;font-size:1.1rem;letter-spacing:.25em;align-items:center;justify-content:center;
                   text-shadow:0 0 8px rgba(124,252,154,.7);animation:tsc-blink 1s steps(1) infinite">LOADING...</div>
              <div id="focus-veil" style="display:none;position:absolute;inset:14px;background:rgba(0,0,0,.55);
                   color:#ffd166;font-size:.95rem;letter-spacing:.15em;cursor:pointer;z-index:5;
                   align-items:center;justify-content:center;text-align:center">CLICK TO RESUME CONTROL</div>
            </div>
            <div class="tsc-slot">
              ${MODES[cab.id] ? `
                <label class="tsc-note">MODE
                  <select id="sel-mode" class="tsc-sel">${MODES[cab.id].map((m, i) =>
                    `<option value="${m.humans}" ${i === 0 ? 'selected' : ''}>${m.label}</option>`).join('')}</select>
                </label>` : ''}
              ${multi ? `
                <label class="tsc-note">PLAYERS
                  <select id="sel-players" class="tsc-sel">${Array.from({ length: multi.max - multi.min + 1 },
                    (_, i) => `<option ${multi.min + i === Math.min(4, multi.max) ? 'selected' : ''}>${multi.min + i}</option>`).join('')}</select>
                </label>
                <label class="tsc-note">HUMANS
                  <select id="sel-humans" class="tsc-sel">${Array.from({ length: multi.humans },
                    (_, i) => `<option ${i === 0 ? 'selected' : ''}>${i + 1}</option>`).join('')}</select>
                </label>` : ''}
              ${LEVELS[cab.id] ? `
                <label class="tsc-note">LEVEL
                  <select id="sel-level" class="tsc-sel" style="max-width:180px">
                    ${LEVELS[cab.id].house.map(([v, l]) => `<option value="${v}">${l}</option>`).join('')}
                  </select>
                </label>
                <button class="tsc-btn" id="editor-btn" disabled title="Opens the selected level as a template (or a blank canvas)">${cab.id === 'chess' ? 'POSITION EDITOR' : 'LEVEL EDITOR'}</button>
                <button class="tsc-btn" id="del-level-btn" style="display:none;color:var(--color-danger,#f66);border-color:currentColor" title="Delete this community level (authors and admins)">✕</button>` : ''}
              ${cab.id === 'powder-keg' ? `
                <label class="tsc-note">ROUNDS
                  <select id="sel-rounds" class="tsc-sel" title="Best-of-3: first to two round wins takes the match (local only)">
                    <option value="1">SINGLE</option>
                    <option value="3">BEST OF 3</option>
                  </select>
                </label>` : ''}
              ${CLOCKS[cab.id] ? `
                <label class="tsc-note">CLOCK
                  <select id="sel-clock" class="tsc-sel">
                    ${CLOCKS[cab.id].map(([v, l]) => `<option value="${v}">${l}</option>`).join('')}
                  </select>
                </label>` : ''}
              <button class="tsc-coin" id="coin" disabled>◉ INSERT CREDIT</button>
              <span class="tsc-note" id="boot-note">booting cartridge…</span>
            </div>
            ${net ? `
            <div class="tsc-net" id="net-box">
              <span class="tsc-note">ONLINE:</span>
              ${net.max > 2 ? `
                <label class="tsc-note">SEATS
                  <select id="net-seats" class="tsc-sel">${Array.from({ length: net.max - net.min + 1 },
                    (_, i) => `<option ${net.min + i === Math.min(4, net.max) ? 'selected' : ''}>${net.min + i}</option>`).join('')}</select>
                </label>` : ''}
              <button class="tsc-btn" id="net-host" disabled>HOST ROOM</button>
              <input class="tsc-code" id="net-code" maxlength="4" placeholder="CODE" aria-label="Room code">
              <button class="tsc-btn" id="net-join" disabled>JOIN</button>
              <span class="tsc-lobby" id="net-lobby"></span>
              <button class="tsc-btn warm" id="net-start" style="display:none">START</button>
            </div>` : ''}
          </div>
          <div class="tsc-rail">
            <h3>HIGH SCORES</h3>
            <table><tbody id="scores"><tr><td>—</td></tr></tbody></table>
            <div class="tsc-you" id="you"></div>
            <div class="tsc-ctl">${esc(cab.controls)}</div>
          </div>
        </div>
      </div>`;

    this.loadScores(cab.id);
    this.bootCartridge(cab);
  }

  /** This week's spotlight cabinet and its week-only ladder. */
  async loadChallenge() {
    let ch;
    try { ch = await api('GET', '/arcade/challenge'); } catch { return; }
    const el = this.querySelector('#challenge');
    const cab = CABINETS.find(c => c.id === ch.game);
    if (!el || !cab) return;
    const rows = (ch.top ?? []).map((s, i) =>
      `<div style="display:flex;gap:.6rem;font-size:.78rem;color:#cfcfe0">
         <span style="color:#6d6d88;width:1.1rem">${i + 1}.</span>
         <span style="flex:1">${esc(s.player_name)}</span>
         <span style="color:#ffd166;font-variant-numeric:tabular-nums">${Number(s.score).toLocaleString()}</span>
       </div>`).join('') ||
      '<div style="font-size:.78rem;color:#6d6d88">No scores yet this week — the ladder is wide open.</div>';
    el.innerHTML = `
      <div style="display:flex;gap:1.5rem;flex-wrap:wrap;align-items:flex-start">
        <div style="flex:1;min-width:240px">
          <div style="font-size:.68rem;letter-spacing:.25em;color:#ff5577">WEEKLY CHALLENGE · ${esc(ch.week)}</div>
          <div style="font-size:1.1rem;letter-spacing:.2em;color:#7CFC9A;text-shadow:0 0 6px rgba(124,252,154,.6);margin:.25rem 0">${esc(ch.name)} — ${esc(cab.name)}</div>
          <div style="font-size:.8rem;color:#9a9ab2">${esc(ch.blurb)} Scores this week only; the ladder resets Monday.</div>
          <button class="tsc-btn" id="challenge-play" style="margin-top:.6rem">PLAY ${esc(cab.name)} →</button>
        </div>
        <div style="min-width:220px">
          <div style="font-size:.68rem;letter-spacing:.2em;color:#8f8fa8;margin-bottom:.35rem">THIS WEEK'S LADDER</div>
          ${rows}
        </div>
      </div>`;
    el.style.display = '';
    el.querySelector('#challenge-play').addEventListener('click', () => {
      location.hash = `#/arcade?g=${ch.game}`;
    });
  }

  /** The service-record browser: pick any member, read their whole file. */
  async wireRecords() {
    const sel = this.querySelector('#rec-player');
    const cards = this.querySelector('#rec-cards');
    if (!sel || !cards) return;
    let me = null;
    try { me = (await api('GET', '/auth/me'))?.id ?? null; } catch { /* fine */ }
    let players = [];
    try { players = await api('GET', '/arcade/players') ?? []; } catch { /* fine */ }
    if (!players.length) {
      cards.innerHTML = '<div class="tsx-sub">NO RECORDS YET. THE FLOOR AWAITS.</div>';
      sel.style.display = 'none';
      return;
    }
    sel.innerHTML = players.map(p =>
      `<option value="${esc(p.user_id)}" ${p.user_id === me ? 'selected' : ''}>${esc(p.name)} — ${p.total_plays} credit${p.total_plays === 1 ? '' : 's'}</option>`).join('');
    const show = async () => {
      cards.innerHTML = '<div class="tsx-sub">PULLING THE FILE…</div>';
      try {
        const rec = await api('GET', `/arcade/player-stats?user=${sel.value}`);
        const games = rec?.games ?? {};
        const order = CABINETS.map(c => c.id).filter(id => games[id]);
        if (!order.length) {
          cards.innerHTML = '<div class="tsx-sub">A CLEAN RECORD. SUSPICIOUSLY CLEAN.</div>';
          return;
        }
        cards.innerHTML = order.map(id => {
          const name = CABINETS.find(c => c.id === id)?.name ?? id.toUpperCase();
          const rows = Object.entries(games[id])
            .sort(([a], [b]) => statLabel(a).localeCompare(statLabel(b)))
            .map(([k, v]) =>
              `<div class="meta"><span>${esc(statLabel(k))}</span><span class="hs">${esc(statValue(k, v))}</span></div>`)
            .join('');
          return `<div class="tsx-cab" style="cursor:default"><h3>${esc(name)}</h3>${rows}</div>`;
        }).join('');
      } catch {
        cards.innerHTML = '<div class="tsx-sub">THE FILING CABINET JAMMED. TRY AGAIN.</div>';
      }
    };
    sel.addEventListener('change', show);
    await show();
  }

  async loadScores(game) {
    try {
      const [scores, stats] = await Promise.all([
        api('GET', `/arcade/${game}/scores`),
        api('GET', '/arcade/stats'),
      ]);
      const tb = this.querySelector('#scores');
      if (tb) {
        tb.innerHTML = (scores ?? []).map((s, i) =>
          `<tr><td>${i + 1}. ${esc(s.player_name)}</td><td class="sc">${s.score.toLocaleString()}</td></tr>`).join('')
          || '<tr><td>NO SCORES YET. BE FIRST.</td></tr>';
      }
      const mine = (stats ?? []).find(s => s.game === game);
      const you = this.querySelector('#you');
      if (you && mine) {
        you.textContent = `YOU — credits inserted: ${mine.your_plays}` +
          (mine.your_best > 0 ? ` · best: ${mine.your_best.toLocaleString()}` : '');
      }
    } catch { /* rail is decoration */ }
  }

  async bootCartridge(cab) {
    const note = this.querySelector('#boot-note');
    const coin = this.querySelector('#coin');

    // Cartridge → page: the round's service-record counters on game over.
    window.__arcadeStats = async json => {
      try {
        const stats = JSON.parse(json);
        // Campaign: a medal on a house floor unlocks the next one in the picker.
        if (cab.id === 'interns' && (stats.medals_gold || stats.medals_silver || stats.medals_bronze)) {
          const sel = this.querySelector('#sel-level');
          const m = /^b(\d+)$/.exec(sel?.value ?? '');
          if (m && Number(m[1]) < 8) {
            sel.value = `b${Number(m[1]) + 1}`;
            const medal = stats.medals_gold ? 'GOLD' : stats.medals_silver ? 'SILVER' : 'BRONZE';
            toast(`${medal} medal — floor cleared! The next floor is loaded in the LEVEL picker.`, 'success');
          }
        }
        await api('POST', `/arcade/${cab.id}/stats-report`, { stats });
      } catch { /* the record is decoration; the round still counts */ }
    };
    // Cartridge → page: final score on game over.
    window.__arcadeScore = async score => {
      const n = Math.max(0, Math.floor(Number(score) || 0));
      try {
        await api('POST', `/arcade/${cab.id}/score`, { score: n });
        toast(`Score recorded: ${n.toLocaleString()}`, 'success');
      } catch { /* unscored play is fine */ }
      try { this._ws?.close(); } catch { /* fine */ }
      this._ws = null;
      this._playing = false;
      const veil = this.querySelector('#focus-veil');
      if (veil) veil.style.display = 'none';
      this.loadScores(cab.id);
      const c = this.querySelector('#coin');
      if (c) c.disabled = false;
      this._netIdle();
    };
    // Page → cartridge: which cabinet to boot.
    window.__ARCADE_GAME = cab.id;

    // The cartridge is an optional build artifact (`make arcade`) and is not
    // in the source repo — probe before importing so a missing build gets a
    // helpful message instead of a module error.
    try {
      const probe = await fetch('/arcade/arcade.js', { method: 'HEAD' });
      if (!probe.ok) {
        if (note) note.textContent = 'CARTRIDGE NOT INSTALLED — run `make arcade` (see README), rebuild, and redeploy';
        return;
      }
    } catch {
      if (note) note.textContent = 'CARTRIDGE NOT INSTALLED — run `make arcade` (see README), rebuild, and redeploy';
      return;
    }
    let mod;
    try {
      mod = await import('/arcade/arcade.js');
      await mod.default();
    } catch (err) {
      // Bevy/winit escapes its main() via a control-flow exception on wasm —
      // that one means SUCCESS. Anything else is a real boot failure.
      if (!String(err).includes('control flow')) {
        if (note) note.textContent = 'CARTRIDGE FAULT — see console';
        this.querySelector('#boot-veil')?.remove();
        console.error('arcade boot:', err);
        return;
      }
    }
    this._wasmStarted = true;
    this._mod = mod;
    if (note) note.textContent = '';
    this.querySelector('#boot-veil')?.remove();
    if (LEVELS[cab.id]) this.wireLevels(cab, mod);
    // Keyboard cabinets: when the canvas loses focus the keys go dead — say
    // so instead of letting the player mash a silent keyboard.
    if (KEY_GAMES.has(cab.id)) {
      const canvas = this.querySelector('#arcade-canvas');
      const veil = this.querySelector('#focus-veil');
      if (canvas && veil) {
        this._playing = false;
        canvas.addEventListener('blur', () => {
          if (this._playing) veil.style.display = 'flex';
        });
        canvas.addEventListener('focus', () => { veil.style.display = 'none'; });
        veil.addEventListener('click', () => {
          veil.style.display = 'none';
          canvas.focus();
        });
      }
    }
    if (coin) {
      coin.disabled = false;
      coin.addEventListener('click', async () => {
        coin.disabled = true;
        const mode = this.querySelector('#sel-mode');
        const players = mode ? 2 : Number(this.querySelector('#sel-players')?.value ?? 1);
        const humans = mode
          ? Number(mode.value)
          : Math.min(players, Number(this.querySelector('#sel-humans')?.value ?? 1));
        try {
          await this.resolveLevel(cab);
          this.resolveClock();
          const rounds = this.querySelector('#sel-rounds');
          if (rounds) window.__ARCADE_ROUNDS = Number(rounds.value); else delete window.__ARCADE_ROUNDS;
          const res = await api('POST', `/arcade/${cab.id}/credit`);
          toast(`Credit ${res.credits} logged. Go!`, 'success');
          mod.arcade_insert_credit(players, humans);
          this._playing = true;
          this.querySelector('#arcade-canvas')?.focus();
          this.loadScores(cab.id);
        } catch (err) {
          toast(err.error ?? 'Coin jammed', 'error');
          coin.disabled = false;
        }
      });
    }
    if (NET[cab.id]) this.wireNet(cab);
  }

  // ---- community levels & editors ----

  /** Sets window.__ARCADE_LEVEL from the picker: house option, blank-canvas
      marker (editor only), or a fetched community document. */
  async resolveLevel(cab, forEditor = false) {
    const cfg = LEVELS[cab.id];
    const sel = this.querySelector('#sel-level');
    if (!cfg || !sel) return;
    const v = sel.value;
    if (v === 'std') {
      delete window.__ARCADE_LEVEL; // the cabinet's standard setup
      return;
    }
    if (v === 'fischer') {
      window.__ARCADE_LEVEL = JSON.stringify({ fischer: true });
      return;
    }
    if (v === 'blank') {
      // Playing a blank canvas makes no sense; the standard setup stands in.
      if (forEditor) window.__ARCADE_LEVEL = JSON.stringify({ blank: true });
      else if (cfg.playFallback) window.__ARCADE_LEVEL = JSON.stringify(cfg.playFallback);
      else delete window.__ARCADE_LEVEL;
      return;
    }
    if (v.startsWith('b')) {
      window.__ARCADE_LEVEL = JSON.stringify({ builtin: Number(v.slice(1)) || 1 });
      return;
    }
    const lvl = await api('GET', `/arcade/levels/${v}`);
    window.__ARCADE_LEVEL = lvl.data;
  }

  /** Sets window.__ARCADE_TIME (seconds per side) from the clock picker.
      The cartridge treats absent or zero as an unclocked game. */
  resolveClock() {
    const secs = Number(this.querySelector('#sel-clock')?.value ?? 0);
    if (secs > 0) window.__ARCADE_TIME = secs;
    else delete window.__ARCADE_TIME;
  }

  async wireLevels(cab, mod) {
    const sel = this.querySelector('#sel-level');
    const editorBtn = this.querySelector('#editor-btn');
    const delBtn = this.querySelector('#del-level-btn');
    const cfg = LEVELS[cab.id];
    const isHouse = v => v === 'blank' || v === 'std' || v === 'fischer' || v.startsWith('b');
    const updateDel = () => {
      if (delBtn) delBtn.style.display = isHouse(sel.value) ? 'none' : '';
    };
    const refresh = async () => {
      try {
        const community = await api('GET', `/arcade/${cab.id}/levels`) ?? [];
        // Rebuild options: house levels, blank canvas, then the shelf.
        const current = sel.value;
        sel.innerHTML = cfg.house.map(([v, l]) => `<option value="${v}">${l}</option>`).join('') +
          `<option value="blank">${cfg.blank}</option>` +
          community.map(l => `<option value="${esc(l.id)}">${esc(l.name)} — ${esc(l.author_name)}${cfg.localOnly ? ' (LOCAL)' : ''}</option>`).join('');
        if ([...sel.options].some(o => o.value === current)) sel.value = current;
        updateDel();
      } catch { /* house levels always exist */ }
    };
    await refresh();
    sel.addEventListener('change', updateDel);
    delBtn?.addEventListener('click', async () => {
      const opt = sel.options[sel.selectedIndex];
      if (!opt || isHouse(sel.value)) return;
      if (!confirm(`Delete level “${opt.textContent}” from the community shelf? Authors and admins only.`)) return;
      try {
        await api('DELETE', `/arcade/levels/${sel.value}`);
        toast('Level deleted', 'success');
        sel.value = cfg.house[0][0];
        await refresh();
      } catch (err) {
        toast(err.error ?? 'Not yours to delete', 'error');
      }
    });
    // The editor hands us a compiled level document; we name and shelve it.
    window.__arcadeSaveLevel = async json => {
      let suggested = '';
      try { suggested = JSON.parse(json).name ?? ''; } catch { /* fine */ }
      const name = prompt('Name this level (1-60 chars). Re-using one of your own names updates it:', suggested);
      if (!name || !name.trim()) return;
      try {
        const doc = JSON.parse(json);
        doc.name = name.trim();
        const res = await api('POST', `/arcade/${cab.id}/levels`, { name: name.trim(), data: doc });
        toast(`Level “${res.name}” saved to the community shelf`, 'success');
        await refresh();
      } catch (err) {
        toast(err.error ?? 'Save failed', 'error');
      }
    };
    if (editorBtn) {
      editorBtn.disabled = false;
      editorBtn.addEventListener('click', async () => {
        // The picker's selection becomes the editing template.
        await this.resolveLevel(cab, true);
        mod.arcade_start_editor();
        this._playing = true;
        this.querySelector('#arcade-canvas')?.focus();
        toast(cfg.toast, 'info');
      });
    }
  }

  // ---- online rooms ----

  _netIdle() {
    for (const id of ['net-host', 'net-join']) {
      const b = this.querySelector('#' + id);
      if (b) b.disabled = false;
    }
    const lobby = this.querySelector('#net-lobby');
    if (lobby) lobby.textContent = '';
    const start = this.querySelector('#net-start');
    if (start) start.style.display = 'none';
  }

  wireNet(cab) {
    const hostBtn = this.querySelector('#net-host');
    const joinBtn = this.querySelector('#net-join');
    const codeInput = this.querySelector('#net-code');
    const lobby = this.querySelector('#net-lobby');
    const startBtn = this.querySelector('#net-start');
    hostBtn.disabled = false;
    joinBtn.disabled = false;

    const busy = on => {
      hostBtn.disabled = on;
      joinBtn.disabled = on;
      const coin = this.querySelector('#coin');
      if (coin) coin.disabled = on;
    };

    const open = async mode => {
      busy(true);
      lobby.textContent = 'CONNECTING…';
      try {
        if (mode === 'host') { await this.resolveLevel(cab); this.resolveClock(); }
        await api('POST', `/arcade/${cab.id}/credit`); // online play still costs a credit
      } catch (err) {
        toast(err.error ?? 'Coin jammed', 'error');
        busy(false);
        lobby.textContent = '';
        return;
      }
      const proto = location.protocol === 'https:' ? 'wss' : 'ws';
      const ws = new WebSocket(`${proto}://${location.host}/api/v1/arcade/ws`);
      this._ws = ws;
      let mySeat = -1;
      let started = false;
      ws.onopen = () => ws.send(JSON.stringify({ op: 'auth', token: getToken() }));
      ws.onclose = () => {
        if (!started) {
          lobby.textContent = '';
          busy(false);
          this._netIdle();
        } else {
          toast('Connection lost', 'error');
        }
      };
      ws.onmessage = e => {
        let m;
        try { m = JSON.parse(e.data); } catch { return; }
        switch (m.op) {
          case 'welcome': {
            if (mode === 'host') {
              const seats = Number(this.querySelector('#net-seats')?.value ?? 2);
              ws.send(JSON.stringify({ op: 'create', game: cab.id, seats }));
            } else {
              ws.send(JSON.stringify({ op: 'join', code: codeInput.value.trim().toUpperCase() }));
            }
            break;
          }
          case 'room': {
            mySeat = m.seat;
            const present = (m.present ?? []).length;
            lobby.textContent = `ROOM ${m.code} · ${present}/${m.seats} SEATED · YOU ARE SEAT ${m.seat + 1}`;
            if (m.seat === 0) {
              startBtn.style.display = '';
              const twoSeater = (NET[cab.id] ?? {}).max === 2;
              startBtn.disabled = twoSeater ? present < 2 : present < 1;
              startBtn.textContent = twoSeater && present < 2 ? 'WAITING…' : 'START';
            } else {
              lobby.textContent += ' · WAITING FOR HOST';
            }
            break;
          }
          case 'started': {
            started = true;
            startBtn.style.display = 'none';
            lobby.textContent = `LIVE · SEAT ${m.seat + 1} OF ${m.seats}`;
            window.__arcadeNetSend = s => {
              try { ws.send(JSON.stringify({ op: 'msg', data: JSON.parse(s) })); } catch { /* closed */ }
            };
            // Dealer verbs (hold 'em): the cartridge paces the server dealer.
            window.__arcadeNetOp = op => {
              try { ws.send(JSON.stringify({ op })); } catch { /* closed */ }
            };
            this._mod.arcade_start_net(JSON.stringify({
              seat: m.seat, seats: m.seats, present: m.present ?? [],
            }));
            this._playing = true;
            this.querySelector('#arcade-canvas')?.focus();
            this.loadScores(cab.id);
            break;
          }
          case 'msg':
            this._mod.arcade_net_event(JSON.stringify({ seat: m.seat, data: m.data }));
            break;
          // Dealer messages (hold 'em): seat 255 marks "from the house".
          case 'cards':
          case 'dealt':
          case 'board':
          case 'holes':
            // Pass the object itself: the shell stringifies `data` for the
            // cartridge, so pre-stringifying would double-encode it.
            this._mod.arcade_net_event(JSON.stringify({ seat: 255, data: m }));
            break;
          case 'peer_left':
            if (started) {
              this._mod.arcade_net_event(JSON.stringify({ seat: m.seat, left: true }));
            }
            break;
          case 'error':
            toast(`Arcade: ${m.reason}`, 'error');
            if (!started) {
              try { ws.close(); } catch { /* fine */ }
            }
            break;
        }
      };
    };

    hostBtn.addEventListener('click', () => open('host'));
    joinBtn.addEventListener('click', () => {
      if (codeInput.value.trim().length !== 4) {
        toast('Enter the 4-letter room code', 'error');
        return;
      }
      open('join');
    });
    startBtn.addEventListener('click', () => {
      startBtn.disabled = true;
      try { this._ws?.send(JSON.stringify({ op: 'start' })); } catch { /* fine */ }
    });
  }
}
customElements.define('page-arcade', PageArcade);
