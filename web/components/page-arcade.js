import { api, getToken } from '../app.js';
import { toast } from './toast-notification.js';
import { esc } from '../utils.js';

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
    id: 'chess', name: 'CHESS', tag: 'The old game. Two players, one board, no mercy.',
    players: '2P hotseat or online', controls: 'Mouse — click a piece, then a destination. Promotion offers a picker.',
  },
  {
    id: 'go', name: 'GO', tag: 'Surround territory on a 9×9 board. Two passes end it.',
    players: '2P hotseat or online', controls: 'Mouse — click to place a stone. P to pass.',
  },
  {
    id: 'comet-buster', name: 'COMET BUSTER', tag: 'Vector rocks, one ship, zero friction.',
    players: '1P', controls: '← → rotate · ↑ thrust · Space fire · H hyperspace.',
  },
  {
    id: 'penny-pincher', name: 'PENNY PINCHER', tag: 'Grab every coin. The auditors want a word.',
    players: '1P', controls: 'Arrow keys or WASD. Gold bars turn the tables.',
  },
  {
    id: 'brickfall', name: 'BRICKFALL', tag: 'Four-square bricks fall. Lines pay out. Speed climbs.',
    players: '1P', controls: '← → move · ↑/X rotate · ↓ soft drop · Space hard drop.',
  },
  {
    id: 'powder-keg', name: 'POWDER KEG', tag: 'Kegs, fuses, and up to a dozen rivals in the cellar.',
    players: 'up to 12 — local + bots, or online', controls: 'P1: WASD + Space. P2 (local): arrows + Enter. Online: either set.',
  },
  {
    id: 'hexfection', name: 'HEXFECTION', tag: 'Spread across the hex dish. Convert your neighbours.',
    players: 'up to 12 — hotseat, bots, or online', controls: 'Mouse — click your blob, then a target cell.',
  },
];

// Local seat pickers (hotseat/bots) for the big cabinets.
const MULTI = { 'powder-keg': { min: 2, max: 12, humans: 2 }, hexfection: { min: 2, max: 12, humans: 12 } };
// Networked cabinets: seat ranges for hosting a room.
const NET = { chess: { min: 2, max: 2 }, go: { min: 2, max: 2 }, 'powder-keg': { min: 2, max: 12 }, hexfection: { min: 2, max: 12 } };

function gameFromHash() {
  const q = (location.hash.split('?')[1]) ?? '';
  return new URLSearchParams(q).get('g');
}

class PageArcade extends HTMLElement {
  connectedCallback() {
    this._wasmStarted = false;
    this._ws = null;
    const g = gameFromHash();
    const cab = CABINETS.find(c => c.id === g);
    if (cab) this.renderCabinet(cab); else this.renderFloor();
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
        <div class="tsx-grid" id="floor">${CABINETS.map(c => `
          <div class="tsx-cab" data-g="${esc(c.id)}" role="button" tabindex="0" aria-label="Play ${esc(c.name)}">
            <h3>${esc(c.name)}</h3>
            <div class="tag">${esc(c.tag)}</div>
            <div class="meta"><span>${esc(c.players)}</span><span id="m-${esc(c.id)}">…</span></div>
            <div class="meta"><span></span><span class="hs" id="h-${esc(c.id)}"></span></div>
          </div>`).join('')}
        </div>
      </div>`;
    this.querySelectorAll('.tsx-cab').forEach(el => {
      const open = () => { location.hash = `#/arcade?g=${el.dataset.g}`; };
      el.addEventListener('click', open);
      el.addEventListener('keydown', e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); open(); } });
    });
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
            <div class="tsc-bezel"><canvas id="arcade-canvas" width="720" height="640"></canvas></div>
            <div class="tsc-slot">
              ${multi ? `
                <label class="tsc-note">PLAYERS
                  <select id="sel-players" class="tsc-sel">${Array.from({ length: multi.max - multi.min + 1 },
                    (_, i) => `<option ${multi.min + i === Math.min(4, multi.max) ? 'selected' : ''}>${multi.min + i}</option>`).join('')}</select>
                </label>
                <label class="tsc-note">HUMANS
                  <select id="sel-humans" class="tsc-sel">${Array.from({ length: multi.humans },
                    (_, i) => `<option ${i === 0 ? 'selected' : ''}>${i + 1}</option>`).join('')}</select>
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

    // Cartridge → page: final score on game over.
    window.__arcadeScore = async score => {
      const n = Math.max(0, Math.floor(Number(score) || 0));
      try {
        await api('POST', `/arcade/${cab.id}/score`, { score: n });
        toast(`Score recorded: ${n.toLocaleString()}`, 'success');
      } catch { /* unscored play is fine */ }
      try { this._ws?.close(); } catch { /* fine */ }
      this._ws = null;
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
        console.error('arcade boot:', err);
        return;
      }
    }
    this._wasmStarted = true;
    this._mod = mod;
    if (note) note.textContent = '';
    if (coin) {
      coin.disabled = false;
      coin.addEventListener('click', async () => {
        coin.disabled = true;
        const players = Number(this.querySelector('#sel-players')?.value ?? 1);
        const humans = Math.min(players, Number(this.querySelector('#sel-humans')?.value ?? 1));
        try {
          const res = await api('POST', `/arcade/${cab.id}/credit`);
          toast(`Credit ${res.credits} logged. Go!`, 'success');
          mod.arcade_insert_credit(players, humans);
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
            this._mod.arcade_start_net(JSON.stringify({
              seat: m.seat, seats: m.seats, present: m.present ?? [],
            }));
            this.querySelector('#arcade-canvas')?.focus();
            this.loadScores(cab.id);
            break;
          }
          case 'msg':
            this._mod.arcade_net_event(JSON.stringify({ seat: m.seat, data: m.data }));
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
