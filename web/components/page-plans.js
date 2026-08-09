import { api, canWrite, isAuthenticated, isSuperadmin } from '../app.js';
import { toast } from './toast-notification.js';
import { esc, openModal, guardButton, confirmDelete, renderPager } from '../utils.js';

const PLAN_STATUSES = ['draft', 'active', 'completed', 'archived'];

const STATUSES = ['draft','active','completed','archived'];

class PagePlans extends HTMLElement {
  constructor() { super(); this._plans = []; this._seq = 0; this._status = ''; this._offset = 0; this._total = 0; }

  connectedCallback() {
    this.render();
    this.load();
  }

  /** Renders the static page shell once; load() only touches #plan-list. */
  render() {
    this.innerHTML = `
      <div class="page-header">
        <h1>Plans &amp; Initiatives</h1>
        ${canWrite() ? '<button class="btn-primary" id="add-btn">+ New plan</button>' : ''}
      </div>
      <div class="search-bar">
        <select id="status-sel" aria-label="Filter by status">
          <option value="">All statuses</option>
          ${PLAN_STATUSES.map(s => `<option value="${s}" ${this._status===s?'selected':''}>${s}</option>`).join('')}
        </select>
      </div>
      <div style="display:flex;flex-direction:column;gap:.75rem" id="plan-list"></div>
      <div id="pager"></div>
    `;

    this.querySelector('#status-sel')?.addEventListener('change', e => { this._status = e.target.value; this._offset = 0; this.load(); });
    this.querySelector('#add-btn')?.addEventListener('click', () => this.openCreateModal());
  }

  async load() {
    const seq = ++this._seq;
    const list = this.querySelector('#plan-list');
    list.innerHTML = '<div style="text-align:center;padding:1rem"><span class="spinner"></span></div>';
    try {
      const params = new URLSearchParams({ limit: '25', offset: String(this._offset) });
      if (this._status) params.set('status', this._status);
      const _plPage = await api('GET', '/plans?' + params);
      if (seq !== this._seq) return; // A newer load() superseded this one.
      this._plans = _plPage?.data ?? _plPage ?? [];
      this._total = _plPage?.total ?? this._plans.length;
      this.renderList(list);
      renderPager(this.querySelector('#pager'), { offset: this._offset, limit: 25, total: this._total, onNavigate: o => { this._offset = o; this.load(); } });
    } catch {
      if (seq !== this._seq) return;
      list.innerHTML = '<div class="empty-state"><p>Failed to load plans.</p></div>';
      toast('Failed to load plans','error');
    }
  }

  renderList(list) {
    list.innerHTML = this._plans?.length
      ? this._plans.map(p => `
          <div class="card plan-card" data-id="${esc(p.id)}" style="padding:1rem 1.25rem;cursor:pointer;display:flex;align-items:center;gap:1rem" tabindex="0" role="button">
            <div style="flex:1">
              <div style="font-weight:700">${esc(p.title)}</div>
              ${p.description ? `<div style="font-size:.85rem;color:var(--color-text-muted);margin-top:.2rem">${esc(p.description.slice(0,100))}${p.description.length>100?'…':''}</div>` : ''}
              ${p.owner_name ? `<div style="font-size:.8rem;color:var(--color-text-muted)">Owner: ${esc(p.owner_name)}</div>` : ''}
            </div>
            <span class="badge badge-${esc(p.status)}">${esc(p.status)}</span>
            ${isSuperadmin()?`<button class="btn-ghost del-btn" data-id="${esc(p.id)}" style="color:var(--color-danger)">Del</button>`:''}
          </div>`).join('')
      : '<div class="empty-state"><p>No plans yet.</p></div>';

    list.querySelectorAll('.plan-card').forEach(card => {
      const open = e => {
        if (e.target.classList.contains('del-btn')) return;
        this.openDetail(card.dataset.id);
      };
      card.addEventListener('click', open);
      card.addEventListener('keydown', e => {
        // Let nested action buttons handle their own Enter/Space activation;
        // only the card itself (when directly focused) opens on keyboard.
        if (e.target !== card && e.target.closest('button, a, input, select, textarea')) return;
        if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); open(e); }
      });
    });
    list.querySelectorAll('.del-btn').forEach(btn => {
      btn.addEventListener('click', () => {
        const p = this._plans.find(x => x.id === btn.dataset.id);
        confirmDelete({
          noun: 'plan',
          name: p?.title ?? '',
          onConfirm: async (confirmVal) => {
            try {
              await api('DELETE', `/plans/${btn.dataset.id}?confirm=${encodeURIComponent(confirmVal)}`);
              toast('Deleted','success');
              this.load();
            } catch (err) { toast(err.error ?? 'Delete failed','error'); throw err; }
          },
        });
      });
    });
  }

  openCreateModal() {
    const { dialog, close } = openModal({
      title: 'New plan',
      body: `
        <div class="modal-body">
          <div class="form-group"><label for="f-title">Title *</label><input id="f-title"></div>
          <div class="form-group"><label for="f-desc">Description</label><textarea id="f-desc" rows="3"></textarea></div>
          <div class="form-row">
            <div class="form-group"><label for="f-status">Status</label>
              <select id="f-status">${STATUSES.map(s=>`<option value="${s}">${s}</option>`).join('')}</select>
            </div>
            <div class="form-group"><label for="f-date">Target date</label><input id="f-date" type="date"></div>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="cancel-btn">Cancel</button>
          <button class="btn-primary" id="save-btn">Create</button>
        </div>
      `,
    });

    dialog.querySelector('#cancel-btn').addEventListener('click', close);
    const saveBtn = dialog.querySelector('#save-btn');
    saveBtn.addEventListener('click', guardButton(saveBtn, async () => {
      const title = dialog.querySelector('#f-title').value.trim();
      if (!title) { toast('Title required','error'); return; }
      try {
        const p = await api('POST', '/plans', {
          title,
          description: dialog.querySelector('#f-desc').value.trim()||null,
          status:      dialog.querySelector('#f-status').value,
          target_date: dialog.querySelector('#f-date').value||null,
        });
        toast('Plan created','success'); close(); this.load();
        setTimeout(() => this.openDetail(p.id), 100);
      } catch (err) { toast(err.error??'Failed','error'); }
    }));
  }

  async openDetail(id) {
    let pl;
    try { pl = await api('GET', `/plans/${id}`); }
    catch { toast('Load failed','error'); return; }

    const { dialog, close } = openModal({
      title: pl.title,
      maxWidth: '700px',
      body: `
        <div class="modal-body">
          ${canWrite()?`
          <div class="form-row" style="margin-bottom:.75rem">
            <div class="form-group"><label for="f-title">Title</label><input id="f-title" value="${esc(pl.title)}"></div>
            <div class="form-group"><label for="f-status">Status</label>
              <select id="f-status">${STATUSES.map(s=>`<option value="${s}" ${pl.status===s?'selected':''}>${s}</option>`).join('')}</select>
            </div>
          </div>
          <div class="form-group"><label for="f-desc">Description</label><textarea id="f-desc" rows="3">${esc(pl.description??'')}</textarea></div>
          `:`<p style="margin-bottom:1rem;color:var(--color-text-muted)">${esc(pl.description??'No description.')}</p>`}

          <h3 style="margin:.75rem 0 .5rem;font-size:.95rem">Decision log</h3>
          <div id="decisions-list"></div>
          ${canWrite()?`
            <div style="border:1px solid var(--color-border);border-radius:var(--radius);padding:.75rem;margin-top:.5rem">
              <div class="form-group"><label for="new-decision">Decision summary *</label><input id="new-decision" placeholder="What was decided?"></div>
              <div class="form-group"><label for="new-rationale">Rationale</label><textarea id="new-rationale" rows="2"></textarea></div>
              <button class="btn-secondary" id="add-decision-btn">+ Add decision</button>
            </div>`:''}
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="cancel-btn">Close</button>
          ${canWrite()?'<button class="btn-primary" id="save-btn">Save</button>':''}
        </div>
      `,
    });

    // Renders only the decision-log region so unsaved edits in the form survive
    // adding/removing a decision.
    const renderDecisions = decisions => {
      const listEl = dialog.querySelector('#decisions-list');
      listEl.innerHTML = (decisions ?? []).length === 0
        ? '<p style="color:var(--color-text-muted);font-size:.9rem">No decisions recorded.</p>'
        : (decisions ?? []).map(d => `
            <div style="border:1px solid var(--color-border);border-radius:var(--radius);padding:.65rem .8rem;margin-bottom:.5rem">
              <div style="font-weight:600">${esc(d.summary)}</div>
              ${d.rationale?`<div style="font-size:.85rem;color:var(--color-text-muted)">${esc(d.rationale)}</div>`:''}
              ${canWrite()?`<button class="btn-ghost del-decision" data-id="${esc(d.id)}" style="font-size:.75rem;color:var(--color-danger)">Remove</button>`:''}
            </div>`).join('');

      listEl.querySelectorAll('.del-decision').forEach(btn => {
        btn.addEventListener('click', async () => {
          try {
            await api('DELETE', `/plans/${id}/decisions/${btn.dataset.id}`);
            toast('Removed','success');
            await refreshDecisions();
          } catch { toast('Failed','error'); }
        });
      });
    };

    const refreshDecisions = async () => {
      try {
        const fresh = await api('GET', `/plans/${id}`);
        renderDecisions(fresh.decisions);
      } catch { toast('Failed to refresh decisions','error'); }
    };

    renderDecisions(pl.decisions);

    // Reload the list whenever the detail dialog closes (close button, Close, or Escape),
    // but only while still mounted and authenticated so a logout-triggered close
    // (openModal force-closes on auth-changed) doesn't fire a stray authed fetch.
    dialog.addEventListener('close', () => { if (this.isConnected && isAuthenticated()) this.load(); });
    dialog.querySelector('#cancel-btn').addEventListener('click', close);

    const saveBtn = dialog.querySelector('#save-btn');
    saveBtn?.addEventListener('click', guardButton(saveBtn, async () => {
      try {
        await api('PATCH', `/plans/${id}`, {
          title:       dialog.querySelector('#f-title').value.trim()||null,
          description: dialog.querySelector('#f-desc').value.trim()||null,
          status:      dialog.querySelector('#f-status').value,
        });
        toast('Saved','success');
      } catch { toast('Save failed','error'); }
    }));

    dialog.querySelector('#add-decision-btn')?.addEventListener('click', async () => {
      const summary = dialog.querySelector('#new-decision').value.trim();
      if (!summary) { toast('Summary required','error'); return; }
      try {
        await api('POST', `/plans/${id}/decisions`, {
          summary,
          rationale: dialog.querySelector('#new-rationale').value.trim()||null,
        });
        toast('Decision added','success');
        dialog.querySelector('#new-decision').value = '';
        dialog.querySelector('#new-rationale').value = '';
        await refreshDecisions();
      } catch { toast('Failed','error'); }
    });
  }
}
customElements.define('page-plans', PagePlans);
