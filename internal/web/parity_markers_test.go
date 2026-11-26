// Package web_test — parity ≈ markers (GAP-REPORT §C, G-1..G-5).
//
// THE RULE (PyQt fix-4): mark ≈ exactly when the displayed average
// does not multiply back to the exact gross; a terminating average
// stays unmarked. Go money is integer cents, so the displayed average
// is the stored 2-decimal unit price.
//
// FORMULA NOTE: the gap report states gross = TotalPrice +
// ItemDiscount + PromoDiscount, but the stored TotalPrice is gross
// minus the item discount ONLY — PromoDiscount lives in its own column
// (domain_sales.go:295,714; domain_reports.go:155). The promo-exact
// sub-case below pins this: a line with PromoDiscount 1000 and an
// exact unit price must show NO ≈. Had the literal report formula
// been implemented, that line would be falsely marked.
package web_test

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// paritySellMerged sells one product in two additions at different
// prices through the API and returns the invoice id. The two parts
// merge into ONE invoice line with the weighted-average unit price.
// Prices are integer cents: the API decodes unit_price as int64.
func paritySellMerged(t *testing.T, e *e2eEnv, c *http.Client, pid int64, qty1 string, price1 int64, qty2 string, price2 int64) int64 {
	t.Helper()
	code, data := e.sell(c, map[string]any{
		"customer_id": 1, "sale_type": "retail", "payment_method": "Cash",
		"lines": []any{
			map[string]any{"product_id": pid, "qty": qty1, "unit_price": price1},
			map[string]any{"product_id": pid, "qty": qty2, "unit_price": price2},
		},
	})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("merged sale: want 200 ok, got %d %v", code, data)
	}
	invID, _ := data["invoice_id"].(float64)
	return int64(invID)
}

// parityDetail fetches the sale-detail page and fails on non-200.
func parityDetail(t *testing.T, e *e2eEnv, c *http.Client, invID int64) string {
	t.Helper()
	code, body := e.get(c, fmt.Sprintf("/sales/%d", invID))
	if code != http.StatusOK {
		t.Fatalf("GET /sales/%d: want 200, got %d", invID, code)
	}
	return body
}

// TestParityMarkers_SaleDetailAndPrint (G-2, G-3) drives the real
// server: a merged different-price sale shows ≈ on the detail page
// and on the A4 print page, while a same-price merge and a terminating
// average show none — and the exact total beside the marker is
// unchanged.
func TestParityMarkers_SaleDetailAndPrint(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	res, err := e.store.DB.Exec(`INSERT INTO Products(ProductName,BuyPrice,RetailPrice,Quantity,MinQuantity,IsActive)
		VALUES('سكر',1000,2000,100000,5,1)`)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := res.LastInsertId()
	e.openShift(admin, 10000)

	// 2×5000 + 1×6000 = gross 16000, qty 3, stored unit 5333:
	// 5333×3 = 15999 ≠ 16000, so the average is approximate.
	invID := paritySellMerged(t, e, admin, pid, "2.000", 5000, "1.000", 6000)
	if unit := e.queryInt(`SELECT UnitPrice FROM InvoiceDetails WHERE InvoiceID=?`, invID); unit != 5333 {
		t.Fatalf("stored unit price = %d, want 5333 (weighted avg)", unit)
	}
	if total := e.queryInt(`SELECT TotalPrice FROM InvoiceDetails WHERE InvoiceID=?`, invID); total != 16000 {
		t.Fatalf("stored line total = %d, want 16000", total)
	}
	detail := parityDetail(t, e, admin, invID)
	if !strings.Contains(detail, "≈53.33 ج.م") {
		t.Errorf("detail: want ≈53.33 ج.م for the 16000/3 average, body has no such cell")
	}
	if !strings.Contains(detail, "160.00 ج.م") {
		t.Errorf("detail: exact total 160.00 ج.م missing beside the marker")
	}
	code, print := e.get(admin, fmt.Sprintf("/sales/%d/print", invID))
	if code != http.StatusOK {
		t.Fatalf("GET /sales/%d/print: want 200, got %d", invID, code)
	}
	if !strings.Contains(print, "≈53.33 ج.م") {
		t.Errorf("print: want ≈53.33 ج.م on the A4 page")
	}
	if !strings.Contains(print, "160.00 ج.م") {
		t.Errorf("print: exact total 160.00 ج.م missing")
	}

	// Same-price merge: 2×5000 + 1×5000 = 15000, unit 5000 exactly —
	// the average multiplies back, so NO marker anywhere.
	invID2 := paritySellMerged(t, e, admin, pid, "2.000", 5000, "1.000", 5000)
	detail2 := parityDetail(t, e, admin, invID2)
	if strings.Contains(detail2, "≈") {
		t.Errorf("detail (same-price merge): want no ≈, got one")
	}
	if !strings.Contains(detail2, "50.00 ج.م") {
		t.Errorf("detail (same-price merge): want 50.00 ج.م unit price")
	}

	// Terminating average: 1×2000 + 1×2000 + 1×3000 + 1×3000 =
	// gross 10000, qty 4, unit 2500 exactly — NO marker.
	code, data := e.sell(admin, map[string]any{
		"customer_id": 1, "sale_type": "retail", "payment_method": "Cash",
		"lines": []any{
			map[string]any{"product_id": pid, "qty": "1.000", "unit_price": 2000},
			map[string]any{"product_id": pid, "qty": "1.000", "unit_price": 2000},
			map[string]any{"product_id": pid, "qty": "1.000", "unit_price": 3000},
			map[string]any{"product_id": pid, "qty": "1.000", "unit_price": 3000},
		},
	})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("terminating sale: want 200 ok, got %d %v", code, data)
	}
	invID3, _ := data["invoice_id"].(float64)
	if unit := e.queryInt(`SELECT UnitPrice FROM InvoiceDetails WHERE InvoiceID=?`, int64(invID3)); unit != 2500 {
		t.Fatalf("terminating stored unit = %d, want 2500", unit)
	}
	if strings.Contains(parityDetail(t, e, admin, int64(invID3)), "≈") {
		t.Errorf("detail (terminating average 2500×4=10000): want no ≈")
	}

	// Promo-exact pin (formula correction): a 10%% product_pct offer
	// on a 4×2500 line stores PromoDiscount 1000 while TotalPrice
	// stays 10000 and the unit price stays exactly 2500 — the marker
	// must NOT fire. (The gap report's literal formula,
	// TotalPrice+ItemDiscount+PromoDiscount = 11000, would fire.)
	pid2 := e.createProduct(admin, "PM-PCT", "زيت", "25.00", "100")
	code, loc, body := e.postFormLoc(admin, "/promotions/new", url.Values{
		"name": {"خصم 10% زيت"}, "type": {"product_pct"}, "percent": {"10"},
		"is_active": {"1"}, "target_type": {"product"},
		"target_id":   {strconv.FormatInt(pid2, 10)},
		"target_name": {""}, "target_qty": {"1"},
	})
	if code != http.StatusSeeOther || loc != "/promotions" {
		t.Fatalf("create product_pct offer: want 303 to /promotions, got %d loc=%q body=%q", code, loc, body)
	}
	code, data = e.sell(admin, map[string]any{
		"customer_id": 1, "sale_type": "retail", "payment_method": "Cash",
		"lines": []any{map[string]any{"product_id": pid2, "qty": "4.000", "unit_price": 2500}},
	})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("promo sale: want 200 ok, got %d %v", code, data)
	}
	invID4, _ := data["invoice_id"].(float64)
	if got := e.queryInt(`SELECT PromoDiscount FROM InvoiceDetails WHERE InvoiceID=?`, int64(invID4)); got != 1000 {
		t.Fatalf("promo line PromoDiscount = %d, want 1000 (offer must apply for the pin to mean anything)", got)
	}
	if got := e.queryInt(`SELECT TotalPrice FROM InvoiceDetails WHERE InvoiceID=?`, int64(invID4)); got != 10000 {
		t.Fatalf("promo line TotalPrice = %d, want 10000 (promo NOT subtracted from TotalPrice)", got)
	}
	if strings.Contains(parityDetail(t, e, admin, int64(invID4)), "≈") {
		t.Errorf("detail (promo line, exact 2500 price): want no ≈ — a promo discount alone must not mark the price approximate")
	}
}

// TestParityMarkers_ReturnsUnitPrice (G-4): the returns list gains a
// unit-price column; the truncated average carries ≈ exactly when it
// does not multiply back to the refund.
func TestParityMarkers_ReturnsUnitPrice(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	res, err := e.store.DB.Exec(`INSERT INTO Products(ProductName,BuyPrice,RetailPrice,Quantity,MinQuantity,IsActive)
		VALUES('سكر',1000,2000,100000,5,1)`)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := res.LastInsertId()
	e.openShift(admin, 10000)

	// 3 units at 34¢ with a 1¢ item discount: gross 102, TotalPrice
	// 101. The return path allocates the invoice Discount (which
	// carries the 1¢ item discount) once more, so effTotal is 100
	// and returning all 3 refunds exactly 100¢; the stored unit is
	// 100*1000/3000 = 33¢ (truncated) and 33×3 = 99 ≠ 100, so the
	// column shows ≈0.33 next to the exact 1.00 refund.
	code, data := e.sell(admin, map[string]any{
		"customer_id": 1, "sale_type": "retail", "payment_method": "Cash", "allow_below_cost": true,
		"lines": []any{map[string]any{"product_id": pid, "qty": "3.000", "unit_price": 34, "item_discount": 1}},
	})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("sale for return: want 200 ok, got %d %v", code, data)
	}
	invID, _ := data["invoice_id"].(float64)
	code, loc, body := e.postFormLoc(admin, "/returns", url.Values{
		"invoice_id": {strconv.FormatInt(int64(invID), 10)},
		"reason":     {"تالف"}, "method": {"Cash"},
		"product_id": {strconv.FormatInt(pid, 10)}, "qty": {"3"},
	})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/returns") {
		t.Fatalf("partial return: want 303 to /returns, got %d loc=%q body=%q", code, loc, body)
	}
	if unit := e.queryInt(`SELECT UnitPrice FROM Returns WHERE InvoiceID=?`, int64(invID)); unit != 33 {
		t.Fatalf("stored return unit = %d, want 33 (truncated 100/3)", unit)
	}
	if refund := e.queryInt(`SELECT TotalRefund FROM Returns WHERE InvoiceID=?`, int64(invID)); refund != 100 {
		t.Fatalf("stored return refund = %d, want 100", refund)
	}
	code, list := e.get(admin, "/returns")
	if code != http.StatusOK {
		t.Fatalf("GET /returns: want 200, got %d", code)
	}
	if !strings.Contains(list, "سعر الوحدة") {
		t.Errorf("returns list: no unit-price column header")
	}
	if !strings.Contains(list, "≈0.33 ج.م") {
		t.Errorf("returns list: want ≈0.33 ج.م for the 100¢/3 truncated average")
	}
	if !strings.Contains(list, "1.00 ج.م") {
		t.Errorf("returns list: exact refund 1.00 ج.م missing beside the marker")
	}
}

// TestParityMarkers_ReturnsUnitPriceExact: a refund that divides
// evenly shows the unit price with NO marker.
func TestParityMarkers_ReturnsUnitPriceExact(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	res, err := e.store.DB.Exec(`INSERT INTO Products(ProductName,BuyPrice,RetailPrice,Quantity,MinQuantity,IsActive)
		VALUES('سكر',1000,2000,100000,5,1)`)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := res.LastInsertId()
	e.openShift(admin, 10000)
	code, data := e.sell(admin, map[string]any{
		"customer_id": 1, "sale_type": "retail", "payment_method": "Cash", "allow_below_cost": true,
		"lines": []any{map[string]any{"product_id": pid, "qty": "2.000", "unit_price": 50}},
	})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("sale for return: want 200 ok, got %d %v", code, data)
	}
	invID, _ := data["invoice_id"].(float64)
	code, loc, _ := e.postFormLoc(admin, "/returns", url.Values{
		"invoice_id": {strconv.FormatInt(int64(invID), 10)},
		"reason":     {"تالف"}, "method": {"Cash"},
		"product_id": {strconv.FormatInt(pid, 10)}, "qty": {"2"},
	})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/returns") {
		t.Fatalf("partial return: want 303 to /returns, got %d loc=%q", code, loc)
	}
	code, list := e.get(admin, "/returns")
	if code != http.StatusOK {
		t.Fatalf("GET /returns: want 200, got %d", code)
	}
	if !strings.Contains(list, "0.50 ج.م") {
		t.Errorf("returns list: want exact unit price 0.50 ج.م")
	}
	if strings.Contains(list, "≈") {
		t.Errorf("returns list (exact 50×2=100 refund): want no ≈")
	}
}

// TestParityMarkers_CustomerDrillDown (G-5): a customer invoice row
// links to its sale detail page, which carries the G-2 marker.
func TestParityMarkers_CustomerDrillDown(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	res, err := e.store.DB.Exec(`INSERT INTO Products(ProductName,BuyPrice,RetailPrice,Quantity,MinQuantity,IsActive)
		VALUES('سكر',1000,2000,100000,5,1)`)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := res.LastInsertId()
	// Registered customer via the API (the supported creation path),
	// with a 1000.00 EGP credit limit.
	code, data := e.postJSON(admin, "/api/customers", map[string]any{
		"name": "عميل اختبار", "phone": "01000000002", "credit_limit": "1000.00",
	})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("create customer: want 200 ok, got %d %v", code, data)
	}
	custID, _ := data["customer_id"].(float64)
	if custID == 0 {
		t.Fatalf("no customer_id in %v", data)
	}
	e.openShift(admin, 10000)
	// Unpaid credit sale: 2×5000 + 1×6000 merged (gross 16000) with
	// nothing paid, so the invoice stays open with Remaining 16000.
	code, data = e.sell(admin, map[string]any{
		"customer_id": custID, "sale_type": "retail", "payment_method": "Cash", "paid": 0,
		"lines": []any{
			map[string]any{"product_id": pid, "qty": "2.000", "unit_price": 5000},
			map[string]any{"product_id": pid, "qty": "1.000", "unit_price": 6000},
		},
	})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("credit sale: want 200 ok, got %d %v", code, data)
	}
	invID, _ := data["invoice_id"].(float64)
	want := fmt.Sprintf(`href="/sales/%d"`, int64(invID))

	code, detail := e.get(admin, fmt.Sprintf("/customers/%d", int64(custID)))
	if code != http.StatusOK {
		t.Fatalf("GET /customers/%d: want 200, got %d", int64(custID), code)
	}
	if !strings.Contains(detail, want) {
		t.Errorf("customer detail: open-invoice row has no drill-down link %s", want)
	}
	code, stmt := e.get(admin, fmt.Sprintf("/customers/%d/statement", int64(custID)))
	if code != http.StatusOK {
		t.Fatalf("GET /customers/%d/statement: want 200, got %d", int64(custID), code)
	}
	if !strings.Contains(stmt, want) {
		t.Errorf("customer statement: invoice row has no drill-down link %s", want)
	}
	// The link lands on the detail page carrying the G-2 marker.
	if !strings.Contains(parityDetail(t, e, admin, int64(invID)), "≈53.33 ج.م") {
		t.Errorf("drill-down target: detail page lacks the ≈53.33 marker")
	}
}

// TestParityMarkers_AnalysisAvgAndReportCustomerLink (G-4 analysis,
// G-5 reports kind): the returns-analysis by-product rows carry a
// unit-price average marked ≈ exactly when it does not multiply back
// (100¢ over qty 3 → ≈0.33), and the reports customer kind links each
// invoice number to its sale detail page.
func TestParityMarkers_AnalysisAvgAndReportCustomerLink(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	res, err := e.store.DB.Exec(`INSERT INTO Products(ProductName,BuyPrice,RetailPrice,Quantity,MinQuantity,IsActive)
		VALUES('سكر',1000,2000,100000,5,1)`)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := res.LastInsertId()
	code, data := e.postJSON(admin, "/api/customers", map[string]any{
		"name": "عميل اختبار", "phone": "01000000003", "credit_limit": "1000.00",
	})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("create customer: want 200 ok, got %d %v", code, data)
	}
	custID, _ := data["customer_id"].(float64)
	e.openShift(admin, 10000)
	// Merged credit sale to the customer (2×5000 + 1×6000).
	code, data = e.sell(admin, map[string]any{
		"customer_id": custID, "sale_type": "retail", "payment_method": "Cash", "paid": 0,
		"lines": []any{
			map[string]any{"product_id": pid, "qty": "2.000", "unit_price": 5000},
			map[string]any{"product_id": pid, "qty": "1.000", "unit_price": 6000},
		},
	})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("credit sale: want 200 ok, got %d %v", code, data)
	}
	invID, _ := data["invoice_id"].(float64)
	code, rep := e.get(admin, fmt.Sprintf("/reports/customer?customer_id=%d", int64(custID)))
	if code != http.StatusOK {
		t.Fatalf("GET /reports/customer: want 200, got %d", code)
	}
	if want := fmt.Sprintf(`href="/sales/%d"`, int64(invID)); !strings.Contains(rep, want) {
		t.Errorf("reports customer kind: invoice row has no drill-down link %s", want)
	}
	// Return setup (same shape as ReturnsUnitPrice): 3 units at 34¢
	// with a 1¢ item discount, all returned → refund 100¢, stored
	// unit 33¢. The analysis aggregates one product row: total 100
	// over qty 3 → average ≈0.33.
	code, data = e.sell(admin, map[string]any{
		"customer_id": 1, "sale_type": "retail", "payment_method": "Cash", "allow_below_cost": true,
		"lines": []any{map[string]any{"product_id": pid, "qty": "3.000", "unit_price": 34, "item_discount": 1}},
	})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("sale for return: want 200 ok, got %d %v", code, data)
	}
	invID2, _ := data["invoice_id"].(float64)
	code, loc, _ := e.postFormLoc(admin, "/returns", url.Values{
		"invoice_id": {strconv.FormatInt(int64(invID2), 10)},
		"reason":     {"تالف"}, "method": {"Cash"},
		"product_id": {strconv.FormatInt(pid, 10)}, "qty": {"3"},
	})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/returns") {
		t.Fatalf("return: want 303 to /returns, got %d loc=%q", code, loc)
	}
	code, analysis := e.get(admin, "/returns/analysis")
	if code != http.StatusOK {
		t.Fatalf("GET /returns/analysis: want 200, got %d", code)
	}
	if !strings.Contains(analysis, "سعر الوحدة") {
		t.Errorf("returns analysis: no unit-price column header")
	}
	if !strings.Contains(analysis, "≈0.33 ج.م") {
		t.Errorf("returns analysis: want ≈0.33 ج.م for the 100¢/3 aggregate average")
	}
	code, repRet := e.get(admin, "/reports/returns")
	if code != http.StatusOK {
		t.Fatalf("GET /reports/returns: want 200, got %d", code)
	}
	if !strings.Contains(repRet, "≈0.33 ج.م") {
		t.Errorf("reports returns kind: want ≈0.33 ج.م for the 100¢/3 aggregate average")
	}
}

// parityCartDriver is the node driver for the G-1 cart test. The Go
// test extracts the <script> block from the SERVED /sales page and
// inserts it at the marker below, then runs this file in node with a
// seeded cart (one scenario per run). The driver talks to the script
// exactly like a browser: minimal DOM stubs + sessionStorage seed.
// It prints CART-APPROX:<yes|no> for the rendered first row's price
// cell, and CART-DRIVER-DONE on success. No backticks anywhere in
// this driver: it is embedded in a Go raw string.
const parityCartDriver = `
'use strict';
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
__el('inv-disc').value = '0.00 ج.م';
__el('paid').value = '0.00 ج.م';
__el('sale-type').value = 'retail';
__el('pay-method').value = 'Cash';
__el('customer').value = '1';
const __pick = __el('product-pick');
__pick.value = '1';
__pick.options = [{ getAttribute: function () { return '1'; } }];
__pick.selectedIndex = 0;
sessionStorage.setItem('cashierpos_pos_cart', JSON.stringify({
  cart: [__SEED__],
  form: { customer: '1', saleType: 'retail', payMethod: 'Cash', invDisc: '0.00 ج.م', paid: '0.00 ج.م', notes: '', allowOverLimit: false, allowBelowCost: false },
}));

/*__SALES_SCRIPT__*/

const tr = __el('tbody').children[0];
if (!tr) { console.log('CART-DRIVER-FAIL no rendered row'); process.exit(1); }
const html = tr.innerHTML;
const cells = html.split('</td>');
const priceCell = cells.length > 2 ? cells[2] : '';
console.log('CART-APPROX:' + (priceCell.indexOf('≈') >= 0 ? 'yes' : 'no'));
console.log('CART-PRICE-CELL:' + priceCell);
console.log('CART-DRIVER-DONE');
`

// TestParityMarkers_CartApproxNode (G-1) is the real guard for the
// cart marker: it executes the SERVED /sales script in node with a
// seeded merged row and asserts the rendered price cell carries ≈
// exactly when the displayed average does not multiply back.
func TestParityMarkers_CartApproxNode(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available — cannot execute the sales script")
	}
	e := readyEnv(t, false)
	mgr := e.managerClient()
	code, body := e.get(mgr, "/sales")
	if code != http.StatusOK {
		t.Fatalf("GET /sales as manager: want 200, got %d", code)
	}
	script := extractSalesScript(t, body)
	if strings.Contains(script, "{{") {
		t.Fatalf("served /sales script still contains an unsubstituted template tag")
	}
	if !strings.Contains(script, "≈") {
		t.Fatalf("served /sales script has no ≈ marker code (G-1 not present in the served page)")
	}
	cases := []struct {
		name string
		seed string // one cart row, JSON
		want string // CART-APPROX:yes|no
	}{
		// 2x50.00 + 1x60.00: avg 53.33, 53.33x3 = 159.99 != 160.00.
		{"merged-different-price", `{ "id": 1, "name": "سكر", "qty": 3000, "price": 5333, "disc": 0, "parts": [[5000, 2000], [6000, 1000]] }`, "CART-APPROX:yes"},
		// 3x50.00: avg 50.00 exactly.
		{"merged-same-price", `{ "id": 1, "name": "سكر", "qty": 3000, "price": 5000, "disc": 0, "parts": [[5000, 3000]] }`, "CART-APPROX:no"},
		// 2x20.00 + 2x30.00: avg 25.00, 25.00x4 = 100.00 exactly.
		{"terminating-average", `{ "id": 1, "name": "سكر", "qty": 4000, "price": 2500, "disc": 0, "parts": [[2000, 2000], [3000, 2000]] }`, "CART-APPROX:no"},
		// Single part: exact by construction.
		{"single-part", `{ "id": 1, "name": "سكر", "qty": 1000, "price": 5000, "disc": 0, "parts": [[5000, 1000]] }`, "CART-APPROX:no"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			combined := strings.Replace(parityCartDriver, "__SEED__", tc.seed, 1)
			combined = strings.Replace(combined, "/*__SALES_SCRIPT__*/", script, 1)
			dir := t.TempDir()
			p := filepath.Join(dir, "parity-cart-driver.mjs")
			if err := os.WriteFile(p, []byte(combined), 0o600); err != nil {
				t.Fatalf("write combined driver: %v", err)
			}
			cmd := exec.Command("node", "--check", p)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("node --check failed: %v\n%s", err, out)
			}
			cmd = exec.Command("node", p)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("node driver failed: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), "CART-DRIVER-DONE") {
				t.Fatalf("node driver did not complete: %s", out)
			}
			if !strings.Contains(string(out), tc.want) {
				t.Fatalf("cart %s: want %s, node said:\n%s", tc.name, tc.want, out)
			}
		})
	}
}
