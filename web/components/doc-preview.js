import { apiBlob, apiDownload } from '../app.js';
import { toast } from './toast-notification.js';
import { esc, openModal } from '../utils.js';

/**
 * In-app document viewer. Renders common formats directly from the preview
 * endpoint (which serves the ORIGINAL bytes and records a PREVIEW audit
 * entry): images, SVG, PDF (browser's native viewer), sandboxed HTML,
 * Markdown (built-in renderer), CSV/TSV tables, plain text/code, Mermaid
 * diagrams, XLSX workbooks, and DOCX documents — the last three via vendored,
 * self-hosted libraries loaded only on first use (the CSP allows no CDNs).
 *
 * Preview-only documents can ONLY be seen here; the download button hides
 * and the server refuses /file for them.
 */

const vendorLoaded = new Map();
function loadVendor(file) {
  if (!vendorLoaded.has(file)) {
    vendorLoaded.set(file, new Promise((resolve, reject) => {
      const s = document.createElement('script');
      s.src = `/vendor/${file}`;
      s.onload = () => resolve();
      s.onerror = () => { vendorLoaded.delete(file); reject(new Error(`failed to load ${file}`)); };
      document.head.appendChild(s);
    }));
  }
  return vendorLoaded.get(file);
}

function extOf(name) {
  const m = /\.([a-z0-9]+)$/i.exec(name ?? '');
  return m ? m[1].toLowerCase() : '';
}

/** Minimal, escape-first Markdown renderer (headings, lists, quotes, code
 *  fences, hr, bold/italic/inline code, http(s) links). Everything is
 *  esc()'d before any HTML is introduced, so content cannot inject markup. */
export function renderMarkdown(src) {
  const inline = (t) => t
    .replace(/`([^`]+)`/g, (_, c) => `<code>${c}</code>`)
    .replace(/\*\*([^*]+)\*\*/g, '<b>$1</b>')
    .replace(/\*([^*]+)\*/g, '<i>$1</i>')
    .replace(/\[([^\]]+)\]\((https?:\/\/[^)\s]+)\)/g,
      (_, txt, url) => `<a href="${url}" target="_blank" rel="noopener">${txt}</a>`);
  const lines = esc(src).replaceAll('\r\n', '\n').split('\n');
  const out = [];
  let list = null, code = false, para = [];
  const flushPara = () => { if (para.length) { out.push(`<p>${inline(para.join(' '))}</p>`); para = []; } };
  const flushList = () => { if (list) { out.push(`</${list}>`); list = null; } };
  for (const line of lines) {
    if (code) {
      if (/^```/.test(line)) { out.push('</code></pre>'); code = false; } else out.push(line);
      continue;
    }
    if (/^```/.test(line)) { flushPara(); flushList(); out.push('<pre><code>'); code = true; continue; }
    const h = /^(#{1,6})\s+(.*)$/.exec(line);
    if (h) { flushPara(); flushList(); out.push(`<h${h[1].length}>${inline(h[2])}</h${h[1].length}>`); continue; }
    if (/^(-{3,}|\*{3,})\s*$/.test(line)) { flushPara(); flushList(); out.push('<hr>'); continue; }
    const q = /^&gt;\s?(.*)$/.exec(line);
    if (q) { flushPara(); flushList(); out.push(`<blockquote>${inline(q[1])}</blockquote>`); continue; }
    const ul = /^\s*[-*+]\s+(.*)$/.exec(line);
    const ol = /^\s*\d+[.)]\s+(.*)$/.exec(line);
    if (ul || ol) {
      flushPara();
      const want = ul ? 'ul' : 'ol';
      if (list !== want) { flushList(); out.push(`<${want}>`); list = want; }
      out.push(`<li>${inline((ul ?? ol)[1])}</li>`);
      continue;
    }
    if (!line.trim()) { flushPara(); flushList(); continue; }
    para.push(line.trim());
  }
  if (code) out.push('</code></pre>');
  flushPara(); flushList();
  return out.join('\n');
}

/** RFC-4180-ish CSV/TSV parser with quoted fields. */
function parseDSV(text, sep) {
  const rows = []; let row = [], field = '', inQ = false;
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (inQ) {
      if (c === '"') { if (text[i + 1] === '"') { field += '"'; i++; } else inQ = false; }
      else field += c;
    } else if (c === '"') inQ = true;
    else if (c === sep) { row.push(field); field = ''; }
    else if (c === '\n') { row.push(field.replace(/\r$/, '')); rows.push(row); row = []; field = ''; }
    else field += c;
    if (rows.length >= 1000) break; // cap huge sheets
  }
  if (field !== '' || row.length) { row.push(field.replace(/\r$/, '')); rows.push(row); }
  return rows;
}

function tableHTML(rows, firstIsHeader) {
  if (!rows.length) return '<p style="color:var(--color-text-muted)">Empty file.</p>';
  const cap = r => r.slice(0, 60);
  const head = firstIsHeader
    ? `<thead><tr>${cap(rows[0]).map(c => `<th>${esc(String(c ?? ''))}</th>`).join('')}</tr></thead>` : '';
  const body = rows.slice(firstIsHeader ? 1 : 0).map(r =>
    `<tr>${cap(r).map(c => `<td>${esc(String(c ?? ''))}</td>`).join('')}</tr>`).join('');
  return `<div style="overflow:auto;max-height:60vh"><table>${head}<tbody>${body}</tbody></table></div>`;
}

/** Allowlist sanitizer for library-produced HTML (mammoth output). */
function sanitizeHTML(html) {
  const ALLOW = new Set(['P', 'H1', 'H2', 'H3', 'H4', 'H5', 'H6', 'UL', 'OL', 'LI', 'TABLE', 'THEAD',
    'TBODY', 'TR', 'TD', 'TH', 'B', 'STRONG', 'I', 'EM', 'U', 'S', 'BR', 'HR', 'BLOCKQUOTE',
    'PRE', 'CODE', 'SUP', 'SUB', 'IMG', 'A']);
  const doc = new DOMParser().parseFromString(html, 'text/html');
  const walk = (node) => {
    for (const child of [...node.childNodes]) {
      if (child.nodeType === Node.ELEMENT_NODE) {
        if (!ALLOW.has(child.tagName)) { child.replaceWith(...child.childNodes); continue; }
        for (const attr of [...child.attributes]) {
          const keep =
            (child.tagName === 'IMG' && attr.name === 'src' && attr.value.startsWith('data:image/')) ||
            (child.tagName === 'A' && attr.name === 'href' && /^https?:\/\//.test(attr.value)) ||
            ['colspan', 'rowspan'].includes(attr.name);
          if (!keep) child.removeAttribute(attr.name);
        }
        if (child.tagName === 'A') { child.setAttribute('rel', 'noopener'); child.setAttribute('target', '_blank'); }
        walk(child);
      } else if (child.nodeType !== Node.TEXT_NODE) {
        child.remove();
      }
    }
  };
  walk(doc.body);
  return doc.body.innerHTML;
}

async function renderInto(el, resource, blob) {
  const name = resource.file_name ?? 'file';
  const ext = extOf(name);
  const obj = () => URL.createObjectURL(blob);

  if (['png', 'jpg', 'jpeg', 'gif', 'webp', 'bmp', 'ico', 'avif', 'svg'].includes(ext)) {
    el.innerHTML = `<img src="${obj()}" alt="${esc(name)}" style="max-width:100%;max-height:65vh;display:block;margin:0 auto">`;
    return;
  }
  if (ext === 'pdf') {
    el.innerHTML = `<iframe src="${obj()}" title="${esc(name)}" style="width:100%;height:65vh;border:1px solid var(--color-border);border-radius:6px"></iframe>`;
    return;
  }
  if (ext === 'html' || ext === 'htm') {
    // sandbox with NO allow-scripts/allow-same-origin: inert rendering.
    el.innerHTML = `<iframe sandbox src="${obj()}" title="${esc(name)}" style="width:100%;height:65vh;border:1px solid var(--color-border);border-radius:6px;background:#fff"></iframe>`;
    return;
  }
  if (ext === 'md' || ext === 'markdown') {
    el.innerHTML = `<div class="md-preview">${renderMarkdown(await blob.text())}</div>`;
    return;
  }
  if (ext === 'mmd' || ext === 'mermaid') {
    const src = await blob.text();
    el.innerHTML = '<span class="spinner"></span>';
    await loadVendor('mermaid.min.js');
    window.mermaid.initialize({ startOnLoad: false, securityLevel: 'strict', theme: 'neutral' });
    const { svg } = await window.mermaid.render('pv-mermaid-' + Date.now(), src);
    el.innerHTML = `<div style="overflow:auto;max-height:65vh;text-align:center">${svg}</div>`;
    return;
  }
  if (ext === 'csv' || ext === 'tsv') {
    el.innerHTML = tableHTML(parseDSV(await blob.text(), ext === 'tsv' ? '\t' : ','), true);
    return;
  }
  if (ext === 'xlsx' || ext === 'xls' || ext === 'ods') {
    el.innerHTML = '<span class="spinner"></span>';
    await loadVendor('xlsx.full.min.js');
    const wb = window.XLSX.read(await blob.arrayBuffer(), { type: 'array' });
    const parts = wb.SheetNames.slice(0, 5).map(sn => {
      const rows = window.XLSX.utils.sheet_to_json(wb.Sheets[sn], { header: 1 });
      return `<h4 style="margin:.6rem 0 .3rem">${esc(sn)}</h4>${tableHTML(rows.slice(0, 1000), true)}`;
    });
    el.innerHTML = parts.join('') || '<p>Empty workbook.</p>';
    return;
  }
  if (ext === 'docx') {
    el.innerHTML = '<span class="spinner"></span>';
    await loadVendor('mammoth.browser.min.js');
    const out = await window.mammoth.convertToHtml({ arrayBuffer: await blob.arrayBuffer() });
    el.innerHTML = `<div class="md-preview">${sanitizeHTML(out.value)}</div>`;
    return;
  }
  // Text-ish fallback by extension or content type.
  const texty = ['txt', 'log', 'text', 'json', 'yaml', 'yml', 'toml', 'ini', 'conf', 'sql', 'go',
    'js', 'mjs', 'ts', 'py', 'sh', 'css', 'xml'].includes(ext) || (blob.type || '').startsWith('text/');
  if (texty) {
    const text = await blob.text();
    el.innerHTML = `<pre style="max-height:60vh;overflow:auto;white-space:pre-wrap">${esc(text.slice(0, 400_000))}</pre>`;
    return;
  }
  el.innerHTML = `
    <div class="empty-state" style="padding:2rem">
      <p>No in-app viewer for <b>.${esc(ext || '?')}</b> files${resource.file_preview_only ? ', and this document is preview-only' : ' — download it to open'}.</p>
    </div>`;
}

/** Opens the viewer modal for a document-bearing resource. */
export function openDocPreview(resource) {
  const { dialog, close } = openModal({
    title: `${resource.file_preview_only ? '👁 ' : ''}${resource.file_name ?? resource.title}`,
    maxWidth: 'min(940px, 94vw)',
    body: `
      <div class="modal-body">
        <div id="pv-body"><span class="spinner"></span></div>
        <div style="display:flex;justify-content:space-between;align-items:center;margin-top:.6rem;font-size:.75rem;color:var(--color-text-muted)">
          <span>${resource.file_preview_only
            ? '👁 Preview-only: downloading this document is disabled.'
            : 'Downloads are watermarked where possible and always recorded (who, when, from where).'}</span>
          ${!resource.file_preview_only ? `<button class="btn-secondary" id="pv-dl">Download</button>` : ''}
        </div>
      </div>
      <div class="modal-footer"><button class="btn-secondary" id="pv-close">Close</button></div>`,
  });
  dialog.querySelector('#pv-close').addEventListener('click', close);
  dialog.querySelector('#pv-dl')?.addEventListener('click', () => {
    apiDownload(`/resources/${resource.id}/file`, resource.file_name)
      .catch(err => toast(err.error ?? 'Download failed', 'error'));
  });

  const body = dialog.querySelector('#pv-body');
  apiBlob(`/resources/${resource.id}/preview`)
    .then(blob => renderInto(body, resource, blob))
    .catch(err => {
      body.innerHTML = `<div class="empty-state"><p>${esc(err?.error ?? 'Preview failed')}</p></div>`;
    });

  if (!document.getElementById('md-preview-style')) {
    const s = document.createElement('style');
    s.id = 'md-preview-style';
    s.textContent = `
      .md-preview { max-height: 62vh; overflow-y: auto; line-height: 1.55; }
      .md-preview h1,.md-preview h2,.md-preview h3 { margin: .8em 0 .3em; }
      .md-preview pre { background: var(--color-bg); padding: .6em .8em; border-radius: 6px; overflow-x: auto; }
      .md-preview code { background: var(--color-bg); padding: .05em .3em; border-radius: 4px; }
      .md-preview blockquote { border-left: 3px solid var(--color-border); margin-left: 0; padding-left: .8em; color: var(--color-text-muted); }
      .md-preview table { border-collapse: collapse; }
      .md-preview td,.md-preview th { border: 1px solid var(--color-border); padding: .25em .5em; }
      .md-preview img { max-width: 100%; }`;
    document.head.appendChild(s);
  }
}
