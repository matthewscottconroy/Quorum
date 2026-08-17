import { esc, openModal, wordFrequencies } from '../utils.js';

/**
 * In-app document preview as a word heat map: the document's meaningful words
 * laid out as tiles, colored cold (blue, rare) → hot (red, frequent) and
 * subtly sized by frequency, with counts on hover. Pure client-side analysis —
 * previewing never hits the export endpoints, so it leaves no EXPORT audit
 * entry (only real document downloads do).
 */

/** Assembles the plain text of a meeting's minutes from data the app already
 *  has: the journal, motions (title/detail), decisions, agenda, and notes. */
export function assembleMinutesText(mt, entries, motions) {
  const parts = [mt?.title, mt?.agenda, mt?.notes];
  for (const e of entries ?? []) parts.push(e.body);
  for (const m of motions ?? []) parts.push(m.title, m.detail);
  for (const d of mt?.decisions ?? []) parts.push(d.summary, d.detail);
  return parts.filter(Boolean).join('\n');
}

/** Cold→hot color pair for a normalized frequency t in [0,1]. The
 *  foreground rides the app's text token so tiles stay readable on BOTH
 *  themes — the old hardcoded dark HSL text was dark-on-dark in dark mode —
 *  and the hue arrives through the low-alpha background wash instead. */
function heat(t) {
  const hue = Math.round(215 - t * 215); // 215 = cool blue … 0 = red
  return {
    bg: `hsla(${hue}, 85%, 50%, ${(0.14 + 0.34 * t).toFixed(2)})`,
    fg: 'var(--color-text)',
  };
}

class WordHeatmap extends HTMLElement {
  /** Renders the heat map for the given text. */
  set text(value) {
    const words = wordFrequencies(value, 60);
    if (!words.length) {
      this.innerHTML = '<p style="color:var(--color-text-muted);font-size:.85rem">Not enough text to analyze.</p>';
      return;
    }
    const max = words[0].count;
    const min = words[words.length - 1].count;
    const span = Math.max(1, max - min);
    const totalTokens = words.reduce((n, w) => n + w.count, 0);
    this.innerHTML = `
      <div style="display:flex;justify-content:space-between;align-items:center;font-size:.75rem;color:var(--color-text-muted);margin-bottom:.6rem">
        <span>${words.length} distinct words · ${totalTokens} occurrences (stopwords excluded)</span>
        <span style="display:inline-flex;align-items:center;gap:.3rem">rare
          <span style="display:inline-block;width:70px;height:8px;border-radius:4px;background:linear-gradient(90deg,hsla(215,85%,50%,.35),hsla(110,85%,45%,.4),hsla(0,85%,50%,.45))"></span>
        frequent</span>
      </div>
      <div style="display:flex;flex-wrap:wrap;gap:.35rem;align-items:baseline">
        ${words.map(({ word, count }) => {
          const t = (count - min) / span;
          const c = heat(t);
          return `<span title="${esc(word)}: ${count}×" style="background:${c.bg};color:${c.fg};` +
            `padding:.1rem .45rem;border-radius:5px;font-weight:${t > 0.6 ? 700 : 500};` +
            `font-size:${(0.78 + t * 0.72).toFixed(2)}rem;cursor:default">${esc(word)}</span>`;
        }).join('')}
      </div>`;
  }
}
customElements.define('word-heatmap', WordHeatmap);

/** Opens a modal previewing `text` as a heat map. */
export function openHeatmapModal(title, text) {
  const { dialog, close } = openModal({
    title: `Heat map — ${title}`,
    maxWidth: '640px',
    body: `
      <div class="modal-body"><word-heatmap></word-heatmap></div>
      <div class="modal-footer"><button class="btn-secondary" id="close-btn">Close</button></div>`,
  });
  dialog.querySelector('word-heatmap').text = text;
  dialog.querySelector('#close-btn').addEventListener('click', close);
}
