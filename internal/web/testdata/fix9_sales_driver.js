// FIX-9 node driver for the sales cart script (R9-L5).
//
// The Go test extracts the <script> block from the SERVED /sales page
// and inserts it at the __SALES_SCRIPT__ marker below, then runs this
// file in node. The script is an IIFE with private state, so the
// driver talks to it exactly like a browser: minimal DOM stubs +
// dispatched events. Scenario 1 seeds the cart through sessionStorage
// (the same restore path the lock redirect uses) to get the exact
// Review-9 merged row 1x10.00 + 2x10.01; scenario 2 builds a row by
// clicking Add, like a user. It asserts the stash round-trip
// behaviour (R8-M1 / R9-L2), the zero-qty emission skip (R9-M1), the
// clamped row total (R9-L3) and the strict parsers (R6-L3) end to
// end — through the real render() and its emitted hidden inputs, not
// through re-implemented logic. A CHECK failure throws and node exits
// non-zero; success prints FIX9-DRIVER-DONE.
'use strict';
// ---- minimal DOM stubs ----
class El {
  constructor(tag) {
    this.tagName = tag; this.value = ''; this.textContent = '';
    this._html = ''; this.checked = false; this.children = [];
    this.listeners = {}; this.attrs = {}; this.options = [];
    this.selectedIndex = 0;
  }
  set innerHTML(v) { this._html = v; this.children = []; }
  get innerHTML() { return this._html; }
  appendChild(c) { this.children.push(c); return c; }
  addEventListener(t, f) { (this.listeners[t] = this.listeners[t] || []).push(f); }
  dispatch(t, e) { (this.listeners[t] || []).forEach(function (f) { f(e); }); }
  getAttribute(n) { return this.attrs[n]; }
  setAttribute(n, v) { this.attrs[n] = String(v); }
}
const __els = {};
function __el(id) { if (!__els[id]) __els[id] = new El(id); return __els[id]; }
const document = {
  getElementById: function (id) { return __el(id); },
  querySelector: function (sel) {
    if (sel === '#cart tbody') return __el('tbody');
    if (sel === '.alert.err') return null;
    return __el(sel);
  },
  getElementsByName: function () { return []; },
  createElement: function (tag) { return new El(tag); },
  addEventListener: function () {},
};
const sessionStorage = {
  _m: new Map(),
  getItem(k) { return this._m.has(k) ? this._m.get(k) : null; },
  setItem(k, v) { this._m.set(k, String(v)); },
  removeItem(k) { this._m.delete(k); },
};
const fetch = function () {
  return Promise.resolve({ json: function () { return Promise.resolve({ ok: false }); } });
};
// Seed the form fields the way the real page renders them.
__el('inv-disc').value = '0.00 ج.م';
__el('paid').value = '0.00 ج.م';
__el('sale-type').value = 'retail';
__el('pay-method').value = 'Cash';
__el('customer').value = '1';
// Product picker: one option whose price the driver can change.
let __optPrice = 1000;
const __opt = {
  getAttribute: function (n) {
    if (n === 'data-name') return 'سكر';
    if (n === 'data-price') return String(__optPrice);
    return null;
  },
};
const __pick = __el('product-pick');
__pick.value = '1';
__pick.options = [__opt];
__pick.selectedIndex = 0;
// Scenario 1 seed: the exact Review-9 merged row 1x10.00 + 2x10.01
// (qty 3000, gross 3002), restored from sessionStorage on load.
sessionStorage.setItem('cashierpos_pos_cart', JSON.stringify({
  cart: [{ id: 1, name: 'سكر', qty: 3000, price: 1001, disc: 0, parts: [[1000, 1000], [1001, 2000]] }],
  form: { customer: '1', saleType: 'retail', payMethod: 'Cash', invDisc: '0.00 ج.م', paid: '0.00 ج.م', notes: '', allowOverLimit: false, allowBelowCost: false },
}));

/*__SALES_SCRIPT__*/

// ---- driver helpers (run AFTER the script) ----
function clickAdd() { __el('add-btn').dispatch('click', {}); }
function clickDeleteRow() {
  const btn = new El('button');
  btn.getAttribute = function (n) { if (n === 'data-del') return '0'; return null; };
  __el('tbody').dispatch('click', { target: btn });
}
function lineInput(field, v) {
  const inp = new El('input');
  inp.value = v;
  inp.getAttribute = function (n) {
    if (n === 'data-i') return '0';
    if (n === 'data-f') return field;
    return null;
  };
  __el('tbody').dispatch('input', { target: inp });
}
function qtyInput(v) { lineInput('qty', v); }
function priceInput(v) { lineInput('price', v); }
function discInput(v) { lineInput('disc', v); }
// Hidden inputs currently emitted by render(), grouped per part.
function emittedLines() {
  const kids = __el('cart-lines').children;
  const lines = [];
  for (let i = 0; i + 3 < kids.length; i += 4) {
    lines.push({
      id: kids[i].value, qty: kids[i + 1].value,
      price: kids[i + 2].value, disc: kids[i + 3].value,
    });
  }
  return lines;
}
// Row total cell text from the rendered cart row.
function rowTotalText() {
  const tr = __el('tbody').children[0];
  const m = tr ? tr.innerHTML.match(/<td class="num">([^<]*)<\/td>/) : null;
  return m ? m[1] : null;
}
function grossText() { return __el('gross').textContent; }
function check(name, cond, detail) {
  if (!cond) throw new Error('CHECK FAILED: ' + name + (detail ? ' — ' + detail : ''));
  console.log('CHECK ok: ' + name);
}
// ---- scenario 1: seeded merged row 1x10.00 + 2x10.01 ----
check('seeded row emits exact parts', JSON.stringify(emittedLines()) === '[{"id":1,"qty":1,"price":10,"disc":0},{"id":1,"qty":2,"price":10.01,"disc":0}]', JSON.stringify(emittedLines()));
check('seeded gross 30.02', rowTotalText() === '30.02 ج.م', rowTotalText());
// R8-M1 direct round-trip: 3 -> 0 -> 3 restores 3002.
qtyInput('0');
check('qty 0 shows 0.00', rowTotalText() === '0.00 ج.م', rowTotalText());
check('qty 0 emits no lines', emittedLines().length === 0, JSON.stringify(emittedLines()));
qtyInput('3');
check('3->0->3 gross 30.02', rowTotalText() === '30.02 ج.م', rowTotalText());
check('3->0->3 emits exact parts', JSON.stringify(emittedLines()) === '[{"id":1,"qty":1,"price":10,"disc":0},{"id":1,"qty":2,"price":10.01,"disc":0}]', JSON.stringify(emittedLines()));
// R9-L2 widened stash: 3 -> 0 -> 0.001 -> 3 must restore 3002, not 3003.
qtyInput('0');
qtyInput('0.001');
check('3->0->0.001 row total 0.01', rowTotalText() === '0.01 ج.م', rowTotalText());
check('3->0->0.001 emits only the live 0.001 part (R9-M1)', JSON.stringify(emittedLines()) === '[{"id":1,"qty":0.001,"price":10.01,"disc":0}]', JSON.stringify(emittedLines()));
qtyInput('3');
check('3->0->0.001->3 gross 30.02', rowTotalText() === '30.02 ج.م', rowTotalText());
check('3->0->0.001->3 emits exact parts', JSON.stringify(emittedLines()) === '[{"id":1,"qty":1,"price":10,"disc":0},{"id":1,"qty":2,"price":10.01,"disc":0}]', JSON.stringify(emittedLines()));
// Stash survives a non-zero retype and a later exact restore.
qtyInput('0');
qtyInput('5');
check('3->0->5 gross 50.03', rowTotalText() === '50.03 ج.م', rowTotalText());
check('3->0->5 emits pro-rata parts', JSON.stringify(emittedLines()) === '[{"id":1,"qty":1.667,"price":10,"disc":0},{"id":1,"qty":3.333,"price":10.01,"disc":0}]', JSON.stringify(emittedLines()));
qtyInput('3');
check('3->0->5->3 gross 30.02', rowTotalText() === '30.02 ج.م', rowTotalText());
// ---- scenario 2: click-built row (3 parts, same gross) ----
clickDeleteRow();
check('row deleted, cart empty', __el('tbody').children.length === 0 && emittedLines().length === 0, JSON.stringify(emittedLines()));
__optPrice = 1000; clickAdd();
__optPrice = 1001; clickAdd(); clickAdd();
check('click-built row gross 30.02', rowTotalText() === '30.02 ج.م', rowTotalText());
check('click-built row emits 3 parts', JSON.stringify(emittedLines()) === '[{"id":1,"qty":1,"price":10,"disc":0},{"id":1,"qty":1,"price":10.01,"disc":0},{"id":1,"qty":1,"price":10.01,"disc":0}]', JSON.stringify(emittedLines()));
// R9-M1 case A: merge at qty 0, retype 3 — dead parts skipped.
qtyInput('0');
__optPrice = 1000; clickAdd();
check('merge-at-0 qty 1 shows 10.00', rowTotalText() === '10.00 ج.م', rowTotalText());
qtyInput('3');
check('merge-at-0 retype 3 emits only live parts', JSON.stringify(emittedLines()) === '[{"id":1,"qty":3,"price":10,"disc":0}]', JSON.stringify(emittedLines()));
check('merge-at-0 gross 30.00', rowTotalText() === '30.00 ج.م', rowTotalText());
// R9-L3: discounted row parked at qty 0 must show 0.00, not -5.00.
discInput('5.00');
check('disc 5.00 row total 25.00', rowTotalText() === '25.00 ج.م', rowTotalText());
check('disc rides on first emitted part', JSON.stringify(emittedLines()) === '[{"id":1,"qty":3,"price":10,"disc":5}]', JSON.stringify(emittedLines()));
qtyInput('0');
check('discounted qty 0 row total 0.00 not negative', rowTotalText() === '0.00 ج.م', rowTotalText());
check('discounted qty 0 gross 0.00', grossText() === '0.00 ج.م', grossText());
check('discounted qty 0 emits no lines', emittedLines().length === 0, JSON.stringify(emittedLines()));
qtyInput('3');
check('discounted retype 3 restores 25.00', rowTotalText() === '25.00 ج.م', rowTotalText());
// R7-L2 regression: retyping the same qty never rewrites.
qtyInput('3');
check('same-qty retype keeps 25.00', rowTotalText() === '25.00 ج.م', rowTotalText());
// R6-L3: unparseable qty edits leave the row untouched ('10.005'
// is a VALID qty — 3 decimals allowed — so the refused cases are
// a comma and a 4-decimal value).
qtyInput('1,000');
check('qty 1,000 refused, row unchanged', rowTotalText() === '25.00 ج.م', rowTotalText());
qtyInput('10.0005');
check('qty 10.0005 refused, row unchanged', rowTotalText() === '25.00 ج.م', rowTotalText());
qtyInput('');
check('qty empty refused, row unchanged', rowTotalText() === '25.00 ج.م', rowTotalText());
// Price edit at qty 0 then retype 3: stash cleared, explicit edit wins.
discInput('0');
qtyInput('0');
priceInput('12.00');
qtyInput('3');
check('price edit at qty 0 then retype 3 gross 36.00', rowTotalText() === '36.00 ج.م', rowTotalText());
check('price edit emits single part at 12.00', JSON.stringify(emittedLines()) === '[{"id":1,"qty":3,"price":12,"disc":0}]', JSON.stringify(emittedLines()));
console.log('FIX9-DRIVER-DONE');
