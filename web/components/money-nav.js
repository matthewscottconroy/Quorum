import { canWrite } from '../app.js';
import { esc, vocab } from '../utils.js';

/**
 * `<money-nav current="dues">` — the money map. Three different pages touch
 * "payments" (member invoices in, a confirmation queue, vendor bills out) and
 * the page names alone don't say which is which, especially once an org
 * renames them. Every money page mounts this strip: one line saying what
 * lives HERE, sibling links with task hints, and a "which page do I need?"
 * cheat sheet keyed by task, not by page name.
 */
const PAGES = [
  {
    key: 'dues', hash: '#/dues', label: 'Dues', officer: true,
    here: 'Money coming in: the invoices you send members, and every payment recorded against them.',
    task: 'Bill members, or record a payment you received',
  },
  {
    key: 'payment-reports', hash: '#/payment-reports', label: 'Confirmations', officer: true,
    here: 'The review queue: members report manual payments (Zelle, check…) from My Account; confirming posts them to their invoice.',
    task: 'A member says “I already paid” — review and confirm it',
  },
  {
    key: 'payables', hash: '#/payables', label: 'Payables', officer: true,
    here: 'Money going out: bills the organization owes, entered on the books and settled later.',
    task: 'Enter or settle a bill the organization owes',
  },
  {
    key: 'funds', hash: '#/funds', label: 'Funds', officer: false,
    here: 'Balances by purpose: how the money on hand is earmarked.',
    task: 'See how much money there is, and what it’s set aside for',
  },
  {
    key: 'accounting', hash: '#/accounting', label: 'Accounting', officer: true,
    here: 'The books behind everything: accounts, journal entries, statements.',
    task: 'Work the ledger or pull a statement',
  },
  {
    key: 'budget', hash: '#/budget', label: 'Budget', officer: true,
    here: 'Forward-looking only: model scenarios and compare them — nothing here posts to the books.',
    task: 'Plan next period’s income and spending',
  },
  {
    key: 'currencies', hash: '#/currencies', label: 'Currencies', officer: true,
    here: 'Exchange rates, used whenever money crosses currencies.',
    task: 'Manage exchange rates',
  },
];

class MoneyNav extends HTMLElement {
  connectedCallback() {
    const current = this.getAttribute('current');
    const me = PAGES.find(p => p.key === current);
    const visible = PAGES.filter(p => !p.officer || canWrite());
    this.innerHTML = `
      <div style="border:1px solid var(--color-border);border-radius:var(--radius);padding:.6rem .9rem;margin-bottom:1rem;background:var(--color-surface,transparent)">
        ${me ? `<div style="font-size:.85rem;margin-bottom:.45rem"><strong style="font-size:.75rem;letter-spacing:.06em;text-transform:uppercase;color:var(--color-text-muted)">This page</strong> — ${esc(me.here)}</div>` : ''}
        <div style="display:flex;gap:.35rem;flex-wrap:wrap;align-items:center">
          ${visible.map(p => p.key === current
            ? `<span style="font-size:.75rem;padding:.2rem .6rem;border-radius:99px;background:var(--color-primary);color:#fff">${esc(vocab(p.label))}</span>`
            : `<a href="${p.hash}" title="${esc(p.task)}" style="font-size:.75rem;padding:.2rem .6rem;border-radius:99px;border:1px solid var(--color-border);color:var(--color-text);text-decoration:none">${esc(vocab(p.label))}</a>`).join('')}
          <details style="margin-left:auto;position:relative">
            <summary style="cursor:pointer;font-size:.75rem;color:var(--color-primary);list-style:none">Which page do I need?</summary>
            <div style="position:absolute;right:0;z-index:30;margin-top:.4rem;min-width:340px;max-width:90vw;background:var(--color-surface,#fff);border:1px solid var(--color-border);border-radius:var(--radius);box-shadow:0 8px 24px rgba(0,0,0,.15);padding:.6rem .8rem">
              ${visible.map(p => `
                <a href="${p.hash}" style="display:flex;justify-content:space-between;gap:.8rem;padding:.3rem 0;text-decoration:none;color:var(--color-text);font-size:.8rem;border-bottom:1px solid var(--color-border)">
                  <span>${esc(p.task)}</span>
                  <strong style="white-space:nowrap">${esc(vocab(p.label))} →</strong>
                </a>`).join('')}
            </div>
          </details>
        </div>
      </div>`;
  }
}
customElements.define('money-nav', MoneyNav);
