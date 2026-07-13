import { api, canWrite } from '../app.js';
import { toast } from './toast-notification.js';
import { confirm } from './confirm-dialog.js';

function esc(s) { return String(s ?? '').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;'); }

const STATUSES = ['draft','active','completed','archived'];

class PagePlans extends HTMLElement {
  constructor() { super(); this._plans = []; }

  async connectedCallback() { await this.load(); }

  async load() {
    try { const _plPage = await api('GET', '/plans');
    this._plans = _plPage?.data ?? _plPage ?? []; }
    catch { toast('Failed to load plans','error'); }
    this.render();
  }

  render() {
    this.innerHTML = `
      <div class="page-header">
        <h1>Plans &amp; Initiatives</h1>
        ${canWrite() ? '<button class="btn-primary" id="add-btn">+ New plan</button>' : ''}
      </div>
      <div style="display:flex;flex-direction:column;gap:.75rem">
        ${this._plans?.length
          ? this._plans.map(p => `
              <div class="card plan-card" data-id="${p.id}" style="padding:1rem 1.25rem;cursor:pointer;display:flex;align-items:center;gap:1rem">
                <div style="flex:1">
                  <div style="font-weight:700">${esc(p.title)}</div>
                  ${p.description ? `<div style="font-size:.85rem;color:var(--color-text-muted);margin-top:.2rem">${esc(p.description.slice(0,100))}${p.description.length>100?'…':''}</div>` : ''}
                  ${p.owner_name ? `<div style="font-size:.8rem;color:var(--color-text-muted)">Owner: ${esc(p.owner_name)}</div>` : ''}
                </div>
                <span class="badge badge-${p.status}">${p.status}</span>
                ${canWrite()?`<button class="btn-ghost del-btn" data-id="${p.id}" style="color:var(--color-danger)">Del</button>`:''}
              </div>`).join('')
          : '<div class="empty-state"><p>No plans yet.</p></div>'}
      </div>
    `;

    this.querySelector('#add-btn')?.addEventListener('click', () => this.openCreateModal());
    this.querySelectorAll('.plan-card').forEach(card => {
      card.addEventListener('click', e => {
        if (e.target.classList.contains('del-btn')) return;
        this.openDetail(card.dataset.id);
      });
    });
    this.querySelectorAll('.del-btn').forEach(btn => {
      btn.addEventListener('click', async () => {
        if (!await confirm('Delete this plan?','Delete plan')) return;
        try { await api('DELETE', `/plans/${btn.dataset.id}`); toast('Deleted','success'); this.load(); }
        catch { toast('Delete failed','error'); }
      });
    });
  }

  openCreateModal() {
    const backdrop = document.createElement('div');
    backdrop.className = 'modal-backdrop';
    backdrop.innerHTML = `
      <div class="modal">
        <div class="modal-header"><h2>New plan</h2><button class="btn-ghost" id="close-btn">✕</button></div>
        <div class="modal-body">
          <div class="form-group"><label>Title *</label><input id="f-title"></div>
          <div class="form-group"><label>Description</label><textarea id="f-desc" rows="3"></textarea></div>
          <div class="form-row">
            <div class="form-group"><label>Status</label>
              <select id="f-status">${STATUSES.map(s=>`<option value="${s}">${s}</option>`).join('')}</select>
            </div>
            <div class="form-group"><label>Target date</label><input id="f-date" type="date"></div>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="cancel-btn">Cancel</button>
          <button class="btn-primary" id="save-btn">Create</button>
        </div>
      </div>
    `;
    document.body.appendChild(backdrop);
    const close = () => backdrop.remove();
    backdrop.querySelector('#close-btn').addEventListener('click', close);
    backdrop.querySelector('#cancel-btn').addEventListener('click', close);
    backdrop.querySelector('#save-btn').addEventListener('click', async () => {
      const title = backdrop.querySelector('#f-title').value.trim();
      if (!title) { toast('Title required','error'); return; }
      try {
        const p = await api('POST', '/plans', {
          title,
          description: backdrop.querySelector('#f-desc').value.trim()||null,
          status:      backdrop.querySelector('#f-status').value,
          target_date: backdrop.querySelector('#f-date').value||null,
        });
        toast('Plan created','success'); close(); this.load();
        setTimeout(() => this.openDetail(p.id), 100);
      } catch (err) { toast(err.error??'Failed','error'); }
    });
  }

  async openDetail(id) {
    let pl;
    try { pl = await api('GET', `/plans/${id}`); }
    catch { toast('Load failed','error'); return; }

    const backdrop = document.createElement('div');
    backdrop.className = 'modal-backdrop';
    backdrop.innerHTML = `
      <div class="modal" style="max-width:700px">
        <div class="modal-header">
          <h2>${esc(pl.title)}</h2>
          <button class="btn-ghost" id="close-btn">✕</button>
        </div>
        <div class="modal-body">
          ${canWrite()?`
          <div class="form-row" style="margin-bottom:.75rem">
            <div class="form-group"><label>Title</label><input id="f-title" value="${esc(pl.title)}"></div>
            <div class="form-group"><label>Status</label>
              <select id="f-status">${STATUSES.map(s=>`<option value="${s}" ${pl.status===s?'selected':''}>${s}</option>`).join('')}</select>
            </div>
          </div>
          <div class="form-group"><label>Description</label><textarea id="f-desc" rows="3">${esc(pl.description??'')}</textarea></div>
          `:`<p style="margin-bottom:1rem;color:var(--color-text-muted)">${esc(pl.description??'No description.')}</p>`}

          <h3 style="margin:.75rem 0 .5rem;font-size:.95rem">Decision log</h3>
          <div id="decisions-list">
            ${(pl.decisions??[]).length===0
              ? '<p style="color:var(--color-text-muted);font-size:.9rem">No decisions recorded.</p>'
              : (pl.decisions??[]).map(d => `
                  <div style="border:1px solid var(--color-border);border-radius:var(--radius);padding:.65rem .8rem;margin-bottom:.5rem">
                    <div style="font-weight:600">${esc(d.summary)}</div>
                    ${d.rationale?`<div style="font-size:.85rem;color:var(--color-text-muted)">${esc(d.rationale)}</div>`:''}
                    ${canWrite()?`<button class="btn-ghost del-decision" data-id="${d.id}" style="font-size:.75rem;color:var(--color-danger)">Remove</button>`:''}
                  </div>`).join('')}
          </div>
          ${canWrite()?`
            <div style="border:1px solid var(--color-border);border-radius:var(--radius);padding:.75rem;margin-top:.5rem">
              <div class="form-group"><label>Decision summary *</label><input id="new-decision" placeholder="What was decided?"></div>
              <div class="form-group"><label>Rationale</label><textarea id="new-rationale" rows="2"></textarea></div>
              <button class="btn-secondary" id="add-decision-btn">+ Add decision</button>
            </div>`:''}
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="cancel-btn">Close</button>
          ${canWrite()?'<button class="btn-primary" id="save-btn">Save</button>':''}
        </div>
      </div>
    `;
    document.body.appendChild(backdrop);
    const close = () => { backdrop.remove(); this.load(); };
    backdrop.querySelector('#close-btn').addEventListener('click', close);
    backdrop.querySelector('#cancel-btn').addEventListener('click', close);

    backdrop.querySelector('#save-btn')?.addEventListener('click', async () => {
      try {
        await api('PATCH', `/plans/${id}`, {
          title:       backdrop.querySelector('#f-title').value.trim()||null,
          description: backdrop.querySelector('#f-desc').value.trim()||null,
          status:      backdrop.querySelector('#f-status').value,
        });
        toast('Saved','success');
      } catch { toast('Save failed','error'); }
    });

    backdrop.querySelectorAll('.del-decision').forEach(btn => {
      btn.addEventListener('click', async () => {
        try { await api('DELETE', `/plans/${id}/decisions/${btn.dataset.id}`); toast('Removed','success'); backdrop.remove(); this.openDetail(id); }
        catch { toast('Failed','error'); }
      });
    });

    backdrop.querySelector('#add-decision-btn')?.addEventListener('click', async () => {
      const summary = backdrop.querySelector('#new-decision').value.trim();
      if (!summary) { toast('Summary required','error'); return; }
      try {
        await api('POST', `/plans/${id}/decisions`, {
          summary,
          rationale: backdrop.querySelector('#new-rationale').value.trim()||null,
        });
        toast('Decision added','success'); backdrop.remove(); this.openDetail(id);
      } catch { toast('Failed','error'); }
    });
  }
}
customElements.define('page-plans', PagePlans);
