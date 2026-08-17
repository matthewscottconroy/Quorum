import { api, getUser, currentMemberId, canWrite, isAdmin } from '../app.js';
import { toast } from './toast-notification.js';
import { esc, openModal, guardButton, formatMoney, parseMoney, knownCurrencies, loadFilters, saveFilters } from '../utils.js';

// Purchase statuses ride the shared badge system (dark-safe) instead of a
// private hex-and-emoji palette.
const STATUS_BADGE = {
  pending: 'pending', approved: 'approved', completed: 'done',
  rejected: 'denied', cancelled: 'cancelled',
};

/**
 * Restricted funds & multi-signature purchases (accounting Phase B).
 * Balances are read straight off the general ledger; approvals are
 * password-confirmed signatures; completing a purchase posts the books.
 */
class PageFunds extends HTMLElement {
  constructor() {
    super();
    this._funds = [];
    this._users = [];
    this._purchases = [];
    this._fund = loadFilters('funds')?.fund ?? '';   // filter, survives navigation like dues/payables
  }

  connectedCallback() {
    this.render();
    this.load();
  }

  render() {
    this.innerHTML = `
      <div class="page-header"><h1>Funds</h1>
        ${isAdmin() ? '<button class="btn-primary" id="fund-new">+ New fund</button>' : ''}</div>
      <money-nav current="funds"></money-nav>
      <p style="color:var(--color-text-muted);font-size:.85rem;margin-bottom:1rem">
        Purpose-restricted money with multi-person sign-off. Every approval is a password-confirmed,
        recorded signature; completing a purchase posts it to the books automatically.</p>
      <div id="funds-grid" style="display:grid;grid-template-columns:repeat(auto-fit,minmax(290px,1fr));gap:1rem;margin-bottom:1.2rem">
        <span class="spinner"></span></div>
      <div class="page-header" style="margin-top:.5rem"><h2 style="font-size:1.1rem">Purchase requests</h2>
        <div style="display:flex;gap:.5rem">
          <select id="pf-fund" aria-label="Filter purchase requests by fund"><option value="">All funds</option></select>
          ${canWrite() ? '<button class="btn-secondary" id="pr-new">+ Request purchase</button>' : ''}
        </div></div>
      <div id="pr-strip" style="font-size:.85rem;color:var(--color-text-muted);padding:.15rem .2rem .5rem"></div>
      <div id="pr-list" style="display:flex;flex-direction:column;gap:.6rem"></div>`;
    this.querySelector('#fund-new')?.addEventListener('click', () => this.openFundModal());
    this.querySelector('#pr-new')?.addEventListener('click', () => this.openPurchaseModal());
    this.querySelector('#pf-fund').addEventListener('change', e => { this._fund = e.target.value; saveFilters('funds', { fund: this._fund }); this.loadPurchases(); });
  }

  async load() {
    try {
      const [funds, users] = await Promise.all([
        api('GET', '/funds'),
        api('GET', '/channels/users').catch(() => []),
      ]);
      this._funds = funds ?? [];
      this._users = users ?? [];
      // A remembered filter pointing at a deleted fund would silently show
      // an empty queue forever; fall back to "All funds" instead.
      if (this._fund && !this._funds.some(f => f.id === this._fund)) {
        this._fund = '';
        saveFilters('funds', { fund: '' });
      }
      this.renderFunds();
      const sel = this.querySelector('#pf-fund');
      sel.innerHTML = `<option value="">All funds</option>` +
        this._funds.map(f => `<option value="${esc(f.id)}" ${this._fund === f.id ? 'selected' : ''}>${esc(f.name)}</option>`).join('');
      await this.loadPurchases();
    } catch { toast('Failed to load funds', 'error'); }
  }

  renderFunds() {
    const grid = this.querySelector('#funds-grid');
    grid.innerHTML = this._funds.map(f => `
      <div class="card" style="padding:1rem">
        <div style="display:flex;justify-content:space-between;align-items:baseline;gap:.5rem">
          <b>${esc(f.name)}</b>
          <span style="font-size:.72rem;color:var(--color-text-muted)">GL ${esc(f.cash_account_code)}</span>
        </div>
        ${f.purpose ? `<div style="font-size:.8rem;color:var(--color-text-muted);margin:.2rem 0">${esc(f.purpose)}</div>` : ''}
        <div style="font-size:1.15rem;font-weight:700;margin:.4rem 0">
          ${(f.balances ?? []).map(b => `${formatMoney(b.balance, b.currency)} <span style="font-size:.72rem;color:var(--color-text-muted)">${esc(b.currency)}</span>`).join(' · ') || '<span style="font-size:.85rem;color:var(--color-text-muted)">empty</span>'}
        </div>
        <div style="font-size:.78rem;color:var(--color-text-muted)">
          Policy: ${f.approvals_required} approval${f.approvals_required === 1 ? '' : 's'}${(f.signers ?? []).length ? ` + required: ${f.signers.map(s => esc(s.name)).join(', ')}` : ''}
          ${f.open_requests ? ` · ${f.open_requests} open request${f.open_requests === 1 ? '' : 's'}` : ''}
        </div>
        ${isAdmin() ? `<div style="display:flex;gap:.4rem;margin-top:.6rem">
          <button class="btn-secondary fd-transfer" data-id="${esc(f.id)}" style="font-size:.78rem">Transfer</button>
          <button class="btn-ghost fd-edit" data-id="${esc(f.id)}" style="font-size:.78rem">Policy</button>
        </div>` : ''}
      </div>`).join('') || `<div class="empty-state"><p>No funds yet${isAdmin() ? '' : ' — an admin can create one'}.</p>
        ${isAdmin() ? '<button class="btn-primary" id="fd-empty-new" style="margin-top:.5rem">+ Create the first fund</button>' : ''}</div>`;
    grid.querySelector('#fd-empty-new')?.addEventListener('click', () => this.openFundModal());
    grid.querySelectorAll('.fd-transfer').forEach(b => b.addEventListener('click', () =>
      this.openTransferModal(this._funds.find(f => f.id === b.dataset.id))));
    grid.querySelectorAll('.fd-edit').forEach(b => b.addEventListener('click', () =>
      this.openFundModal(this._funds.find(f => f.id === b.dataset.id))));
  }

  async loadPurchases() {
    const qs = this._fund ? `?fund_id=${this._fund}` : '';
    try { this._purchases = await api('GET', '/purchases' + qs) ?? []; }
    catch { toast('Failed to load purchases', 'error'); return; }
    const me = getUser()?.id;
    const myMember = currentMemberId();
    // The strip answers the queue's real questions for the SAME filtered
    // set: how many are stuck waiting for ink, and how many need MY pen.
    const strip = this.querySelector('#pr-strip');
    if (strip) {
      const pending = this._purchases.filter(p => p.status === 'pending');
      const ready = this._purchases.filter(p => p.status === 'approved');
      const mine = pending.filter(p => p.requester_id !== me && !(p.approvals ?? []).some(a => a.approver_id === me)).length;
      const parts = [];
      if (pending.length) parts.push(`<strong style="color:var(--color-text)">${pending.length}</strong> awaiting signatures${mine ? ` (<strong style="color:var(--color-text)">${mine}</strong> can take yours)` : ''}`);
      if (ready.length) parts.push(`<strong style="color:var(--color-text)">${ready.length}</strong> approved, ready to complete`);
      strip.innerHTML = parts.join(' &nbsp;·&nbsp; ');
    }
    const box = this.querySelector('#pr-list');
    box.innerHTML = this._purchases.map(p => {
      const badge = STATUS_BADGE[p.status] ?? 'draft';
      const iApproved = (p.approvals ?? []).some(a => a.approver_id === me);
      const canApprove = p.status === 'pending' && p.requester_id !== me && !iApproved;
      return `
      <div class="card" style="padding:.8rem 1rem">
        <div style="display:flex;justify-content:space-between;gap:.8rem;flex-wrap:wrap;align-items:baseline">
          <div>
            <b style="font-variant-numeric:tabular-nums">${formatMoney(p.amount_minor, p.currency)} ${esc(p.currency)}</b> → ${esc(p.payee)}
            <span style="font-size:.78rem;color:var(--color-text-muted)">from ${esc(p.fund_name)}</span>
          </div>
          <span class="badge badge-${esc(badge)}">${esc(p.status)}</span>
        </div>
        ${p.memo ? `<div style="font-size:.82rem;color:var(--color-text-muted);margin-top:.2rem">${esc(p.memo)}</div>` : ''}
        ${p.resource_id ? `<button class="btn-ghost pr-doc-open" data-rid="${esc(p.resource_id)}" style="font-size:.75rem;padding:.05rem .4rem">📄 supporting document</button>` : ''}
        <div style="font-size:.75rem;color:var(--color-text-muted);margin-top:.35rem">
          Requested by ${esc(p.requester_name ?? 'former user')} · ${esc(new Date(p.created_at).toLocaleDateString())}
          · signatures: ${(p.approvals ?? []).length}/${p.approvals_required}
          ${(p.approvals ?? []).map(a => `<span class="badge badge-none" title="${esc(a.ip)} · ${esc(new Date(a.approved_at).toLocaleString())}" style="margin-left:.2rem">✍ ${esc(a.approver_name)}</span>`).join('')}
          ${(p.missing_signers ?? []).length && p.status === 'pending' ? `<span style="color:var(--color-warning,#b45309)"> · awaiting required: ${p.missing_signers.map(esc).join(', ')}</span>` : ''}
          ${(p.recusals ?? []).length ? `<span style="display:block;margin-top:.2rem">Recused: ${p.recusals.map(x => esc(x.member_name)).join(', ')}</span>` : ''}
        </div>
        <div style="display:flex;gap:.4rem;margin-top:.5rem;flex-wrap:wrap">
          ${canApprove ? `<button class="btn-primary pr-approve" data-id="${esc(p.id)}" style="font-size:.78rem">✍ Sign approval</button>` : ''}
          ${p.status === 'approved' && canWrite() ? `<button class="btn-primary pr-complete" data-id="${esc(p.id)}" style="font-size:.78rem">Complete purchase (posts to books)</button>` : ''}
          ${p.status === 'pending' && canWrite() ? `<button class="btn-ghost pr-reject" data-id="${esc(p.id)}" style="font-size:.78rem;color:var(--color-danger)">Reject</button>` : ''}
          ${['pending', 'approved'].includes(p.status) && myMember && !(p.recusals ?? []).some(x => x.member_id === myMember) ? `<button class="btn-ghost pr-recuse" data-id="${esc(p.id)}" style="font-size:.78rem">Recuse myself</button>` : ''}
          ${['pending', 'approved'].includes(p.status) && (p.requester_id === me || isAdmin()) ? `<button class="btn-ghost pr-cancel" data-id="${esc(p.id)}" style="font-size:.78rem">Cancel</button>` : ''}
        </div>
      </div>`;
    }).join('') || `<div class="empty-state"><p>No purchase requests${this._fund ? ' for this fund' : ''}.</p>
      ${canWrite() && this._funds.length ? '<button class="btn-secondary" id="pr-empty-new" style="margin-top:.5rem">+ Request a purchase</button>' : ''}</div>`;
    box.querySelector('#pr-empty-new')?.addEventListener('click', () => this.openPurchaseModal());

    box.querySelectorAll('.pr-doc-open').forEach(b => b.addEventListener('click', async () => {
      try {
        const r0 = await api('GET', `/resources/${b.dataset.rid}`);
        if (r0.file_name) (await import('./doc-preview.js')).openDocPreview(r0);
        else if (r0.url) window.open(r0.url, '_blank', 'noopener');
      } catch { toast("You don't have access to this document", 'error'); }
    }));
    box.querySelectorAll('.pr-approve').forEach(b => b.addEventListener('click', () => this.signApproval(b.dataset.id)));
    box.querySelectorAll('.pr-complete').forEach(b => b.addEventListener('click', guardButton(b, async () => {
      try {
        await api('POST', `/purchases/${b.dataset.id}/complete`);
        toast('Purchase completed and posted to the books', 'success');
        this.load();
      } catch (err) { toast(err.error ?? 'Complete failed', 'error'); }
    })));
    box.querySelectorAll('.pr-reject').forEach(b => b.addEventListener('click', async () => {
      try { await api('POST', `/purchases/${b.dataset.id}/reject`); this.load(); }
      catch (err) { toast(err.error ?? 'Reject failed', 'error'); }
    }));
    box.querySelectorAll('.pr-recuse').forEach(b => b.addEventListener('click', () => {
      // The last raw prompt() in the money area — governance already swept
      // its own (page-meetings openRecuseModal); same shared-dialog treatment.
      const { dialog, close } = openModal({
        title: 'Recuse from this purchase',
        maxWidth: '420px',
        body: `
          <div class="modal-body">
            <div class="form-group">
              <label for="rec-reason">Reason (optional, recorded for the audit trail)</label>
              <input id="rec-reason" placeholder="e.g. vendor is my employer">
            </div>
          </div>
          <div class="modal-footer">
            <button class="btn-secondary" id="rec-cancel">Cancel</button>
            <button class="btn-primary" id="rec-go">Record recusal</button>
          </div>`,
      });
      dialog.querySelector('#rec-cancel').addEventListener('click', close);
      const go = dialog.querySelector('#rec-go');
      go.addEventListener('click', guardButton(go, async () => {
        const reason = dialog.querySelector('#rec-reason').value.trim();
        try {
          await api('POST', `/recusals/${b.dataset.id}`, { type: 'purchase', reason });
          toast('Recusal recorded', 'success');
          close();
          this.load();
        } catch (err) { toast(err.error ?? 'Failed', 'error'); }
      }));
    }));
    box.querySelectorAll('.pr-cancel').forEach(b => b.addEventListener('click', async () => {
      try { await api('POST', `/purchases/${b.dataset.id}/cancel`); this.load(); }
      catch (err) { toast(err.error ?? 'Cancel failed', 'error'); }
    }));
  }

  /** Password-confirmed signature. */
  signApproval(id) {
    const { dialog, close } = openModal({
      title: 'Sign approval',
      maxWidth: '420px',
      body: `
        <div class="modal-body">
          <p style="font-size:.85rem;margin-top:0">Signing is a recorded act — your name, the time, and your
            network address become part of this purchase's permanent evidence. Confirm with your password.</p>
          <div class="form-group"><label for="ap-pw">Your password</label>
            <input id="ap-pw" type="password" autocomplete="current-password"></div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="ap-cancel">Cancel</button>
          <button class="btn-primary" id="ap-sign">✍ Sign</button>
        </div>`,
    });
    dialog.querySelector('#ap-cancel').addEventListener('click', close);
    const signBtn = dialog.querySelector('#ap-sign');
    signBtn.addEventListener('click', guardButton(signBtn, async () => {
      const password = dialog.querySelector('#ap-pw').value;
      if (!password) { toast('Password required', 'error'); return; }
      try {
        const p = await api('POST', `/purchases/${id}/approve`, { password });
        toast(p.status === 'approved' ? 'Signed — request is fully approved' : 'Signed', 'success');
        close(); this.load();
      } catch (err) { toast(err.error ?? 'Approval failed', 'error'); }
    }));
    dialog.querySelector('#ap-pw').focus();
  }

  openPurchaseModal() {
    const { dialog, close } = openModal({
      title: 'Request a purchase',
      body: `
        <div class="modal-body">
          <div class="form-group"><label for="pr-fundsel">Fund *</label>
            <select id="pr-fundsel">${this._funds.map(f => `<option value="${esc(f.id)}">${esc(f.name)}</option>`).join('')}</select></div>
          <div class="form-row">
            <div class="form-group"><label for="pr-amount">Amount *</label>
              <input id="pr-amount" inputmode="decimal" placeholder="250.00"></div>
            <div class="form-group"><label for="pr-cur">Currency</label>
              <input id="pr-cur" value="USD" maxlength="3" list="pr-cur-list"><datalist id="pr-cur-list"></datalist></div>
          </div>
          <div class="form-group"><label for="pr-payee">Payee *</label>
            <input id="pr-payee" placeholder="Who gets paid"></div>
          <div class="form-group"><label for="pr-memo">Memo</label>
            <textarea id="pr-memo" rows="2" placeholder="What is this for?"></textarea></div>
          <div class="form-row">
            <div class="form-group"><label for="pr-doc">Supporting document (quote/invoice)</label>
              <select id="pr-doc"><option value="">— none —</option></select></div>
            <div class="form-group"><label for="pr-exp">Expense account</label>
              <select id="pr-exp"><option value="">Default</option></select>
              <div id="pr-budget-hint" style="font-size:.75rem;color:var(--color-text-muted);margin-top:.2rem"></div></div>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="pr-cancel2">Cancel</button>
          <button class="btn-primary" id="pr-save">File request</button>
        </div>`,
    });
    dialog.querySelector('#pr-cancel2').addEventListener('click', close);
    knownCurrencies(api).then(cs => { const dl = dialog.querySelector('#pr-cur-list'); if (dl) dl.innerHTML = cs.map(c => `<option value="${esc(c)}"></option>`).join(''); });
    api('GET', '/resources?limit=200').then(pg => {
      dialog.querySelector('#pr-doc').innerHTML = '<option value="">— none —</option>' +
        (pg?.data ?? []).map(r0 => `<option value="${esc(r0.id)}">${r0.file_name ? '📄' : '🔗'} ${esc(r0.title)}</option>`).join('');
    }).catch(() => {});
    api('GET', '/accounting/accounts').then(accts => {
      dialog.querySelector('#pr-exp').innerHTML = '<option value="">Default</option>' +
        (accts ?? []).filter(a => a.type === 'expense' && a.active)
          .map(a => `<option value="${esc(a.id)}">${esc(a.code)} ${esc(a.name)}</option>`).join('');
    }).catch(() => {});
    // Read-only budget context: when the chosen account is covered by the
    // active budget, say how much of that envelope is left.
    dialog.querySelector('#pr-exp').addEventListener('change', async e => {
      const hint = dialog.querySelector('#pr-budget-hint');
      if (!hint) return;
      hint.textContent = '';
      const acct = e.target.value;
      if (!acct) return;
      try {
        const b = await api('GET', `/budgets/remaining?account_id=${acct}`);
        const over = b.remaining_minor < 0;
        hint.innerHTML = `Budget \u201C${esc(b.scenario)}\u201D: <strong style="color:${over ? 'var(--color-danger)' : 'var(--color-success)'}">${formatMoney(b.remaining_minor, b.currency)}</strong> of ${formatMoney(b.budget_minor, b.currency)} remaining`;
      } catch { /* no active budget for this account — say nothing */ }
    });
    const saveBtn = dialog.querySelector('#pr-save');
    saveBtn.addEventListener('click', guardButton(saveBtn, async () => {
      const currency = dialog.querySelector('#pr-cur').value.trim().toUpperCase();
      const amount_minor = parseMoney(dialog.querySelector('#pr-amount').value.trim(), currency);
      const payee = dialog.querySelector('#pr-payee').value.trim();
      if (!amount_minor || amount_minor <= 0 || !payee) { toast('Amount and payee required', 'error'); return; }
      try {
        await api('POST', '/purchases', {
          fund_id: dialog.querySelector('#pr-fundsel').value,
          amount_minor, currency, payee,
          memo: dialog.querySelector('#pr-memo').value.trim() || null,
          resource_id: dialog.querySelector('#pr-doc').value || null,
          expense_account_id: dialog.querySelector('#pr-exp').value || null,
        });
        toast('Request filed — signatures needed', 'success');
        close(); this.load();
      } catch (err) { toast(err.error ?? 'Failed', 'error'); }
    }));
  }

  openFundModal(existing = null) {
    const signerIDs = new Set((existing?.signers ?? []).map(s => s.user_id));
    const { dialog, close } = openModal({
      title: existing ? `Policy — ${existing.name}` : 'New fund',
      body: `
        <div class="modal-body">
          ${existing ? '' : `<div class="form-group"><label for="fd-name">Name *</label>
            <input id="fd-name" placeholder="e.g. Scholarship Fund"></div>`}
          <div class="form-group"><label for="fd-purpose">Purpose</label>
            <input id="fd-purpose" value="${esc(existing?.purpose ?? '')}" placeholder="What may this money be spent on?"></div>
          <div class="form-group"><label for="fd-req">Approvals required (1–10)</label>
            <input id="fd-req" type="number" min="1" max="10" value="${existing?.approvals_required ?? 2}"></div>
          <div class="form-group"><label>Required signers (each must sign, in addition to the count)</label>
            <div style="max-height:180px;overflow-y:auto;display:flex;flex-direction:column;gap:.2rem">
              ${this._users.map(u => `
                <label style="display:flex;gap:.4rem;align-items:center;margin:0;text-transform:none;font-weight:500;letter-spacing:normal">
                  <input type="checkbox" class="fd-signer" value="${esc(u.user_id)}" ${signerIDs.has(u.user_id) ? 'checked' : ''} style="width:auto;margin:0">
                  ${esc(u.name)}</label>`).join('')}
            </div></div>
          ${existing ? '<p style="font-size:.75rem;color:var(--color-warning,#b45309)">Changing the policy voids in-flight approvals on pending requests — signatures must be re-collected under the new rules.</p>' : ''}
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="fd-cancel">Cancel</button>
          <button class="btn-primary" id="fd-save">${existing ? 'Save policy' : 'Create fund'}</button>
        </div>`,
    });
    dialog.querySelector('#fd-cancel').addEventListener('click', close);
    const saveBtn = dialog.querySelector('#fd-save');
    saveBtn.addEventListener('click', guardButton(saveBtn, async () => {
      const signer_ids = [...dialog.querySelectorAll('.fd-signer:checked')].map(c => c.value);
      const approvals_required = Number(dialog.querySelector('#fd-req').value);
      const purpose = dialog.querySelector('#fd-purpose').value.trim() || null;
      try {
        if (existing) {
          const res = await api('PATCH', `/funds/${existing.id}`, { purpose, approvals_required, signer_ids });
          toast(res.approvals_voided ? `Policy saved — ${res.approvals_voided} in-flight approval(s) voided` : 'Policy saved', 'success');
        } else {
          const name = dialog.querySelector('#fd-name').value.trim();
          if (!name) { toast('Name required', 'error'); return; }
          await api('POST', '/funds', { name, purpose, approvals_required, signer_ids });
          toast('Fund created — transfer money in to start spending', 'success');
        }
        close(); this.load();
      } catch (err) { toast(err.error ?? 'Failed', 'error'); }
    }));
  }

  openTransferModal(fund) {
    const { dialog, close } = openModal({
      title: `Transfer — ${fund.name}`,
      maxWidth: '420px',
      body: `
        <div class="modal-body">
          <div class="form-group"><label for="tr-dir">Direction</label>
            <select id="tr-dir">
              <option value="in">Into the fund (from operating cash)</option>
              <option value="out">Out of the fund (back to operating)</option>
            </select></div>
          <div class="form-row">
            <div class="form-group"><label for="tr-amount">Amount *</label>
              <input id="tr-amount" inputmode="decimal" placeholder="500.00"></div>
            <div class="form-group"><label for="tr-cur">Currency</label>
              <input id="tr-cur" value="USD" maxlength="3" list="tr-cur-list"><datalist id="tr-cur-list"></datalist></div>
          </div>
          <div class="form-group"><label for="tr-memo">Memo</label>
            <input id="tr-memo" placeholder="e.g. Board allocation 2026-08"></div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="tr-cancel">Cancel</button>
          <button class="btn-primary" id="tr-save">Transfer (posts to books)</button>
        </div>`,
    });
    dialog.querySelector('#tr-cancel').addEventListener('click', close);
    knownCurrencies(api).then(cs => { const dl = dialog.querySelector('#tr-cur-list'); if (dl) dl.innerHTML = cs.map(c => `<option value="${esc(c)}"></option>`).join(''); });
    const saveBtn = dialog.querySelector('#tr-save');
    saveBtn.addEventListener('click', guardButton(saveBtn, async () => {
      const currency = dialog.querySelector('#tr-cur').value.trim().toUpperCase();
      const amount_minor = parseMoney(dialog.querySelector('#tr-amount').value.trim(), currency);
      if (!amount_minor || amount_minor <= 0) { toast('Enter a valid amount', 'error'); return; }
      try {
        await api('POST', `/funds/${fund.id}/transfers`, {
          direction: dialog.querySelector('#tr-dir').value,
          amount_minor, currency,
          memo: dialog.querySelector('#tr-memo').value.trim(),
        });
        toast('Transferred', 'success');
        close(); this.load();
      } catch (err) { toast(err.error ?? 'Transfer failed', 'error'); }
    }));
  }
}
customElements.define('page-funds', PageFunds);
