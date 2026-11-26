// Package web_test — FIX-8 tests (Review-8, 0H / 1M / 1L).
//
// R8-M1: typing qty 0 in the sales cart and retyping a qty must not
// destroy the row price. The fix stashes the pre-zero parts (own
// prices, exact split) and re-inflates from them; the JS itself is
// verified by node against functions + the qty branch extracted
// verbatim from sales.html — see
// ~/workspace/go-cashierpos-fix8/REPORT.md (/tmp/fix8-r8m1.mjs,
// 38/38 cases). These Go tests guard the served template and the
// live form path.
// R8-L1 is the discrete pro-rata scheme's known limit (recorded in
// the report and in a comment above scaleParts); no code change.
package web_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestFix8_SalesQtyZeroStashServed guards R8-M1 in the served
// /sales page: the qty-0 stash logic must be present, and the old
// fix-7 branch (scale then unconditionally recompute the price with
// rowAvg, which returns 0 at qty 0) must be gone.
func TestFix8_SalesQtyZeroStashServed(t *testing.T) {
	e := readyEnv(t, false)
	mgr := e.managerClient()

	code, body := e.get(mgr, "/sales")
	if code != http.StatusOK {
		t.Fatalf("GET /sales as manager: want 200, got %d", code)
	}
	for _, marker := range []string{
		// NOTE: JS `//` comments do NOT survive into served HTML —
		// html/template strips them (proven live: the served /sales
		// page has zero `//` lines, and the older FIX-7/FIX-3 comment
		// markers are absent too). Guard on code text only.
		"validStash",
		"stashed = {parts:",
		"q === st.qty",
		"cart[i].stashed = null",
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("served /sales lacks FIX-8 marker %q", marker)
		}
	}
	// The stash must be invalidated wherever the parts change outside
	// the qty branch: price edit, addLine merge, and the qty branch
	// itself (non-zero rescale + restore consumption).
	if n := strings.Count(body, "cart[i].stashed = null"); n < 3 {
		t.Errorf("served /sales: stash invalidation appears %d time(s), want >= 3 (price edit, addLine, qty branch)", n)
	}
	// The old fix-7 qty branch: a single-line scale + unconditional
	// rowAvg recompute. Its shape must not survive.
	if strings.Contains(body, "if (oldQty > 0) cart[i].parts = scaleParts(cart[i].parts, oldQty, q);") {
		t.Errorf("served /sales still contains the old fix-7 qty branch (unconditional rowAvg recompute)")
	}
}

// TestFix8_MergedRowFormTotalExact is the live form-path check for
// R8-M1: the merged row the fixed JS posts (1x10.00 + 2x10.01) must
// store exactly 3002, and the qty-0 state itself must be
// unsubmittable — the server refuses zero-qty lines, so nothing
// wrong can be stored from it.
func TestFix8_MergedRowFormTotalExact(t *testing.T) {
	e := readyEnv(t, false)
	mgr := e.managerClient()
	// Same seed shape as TestFix3_DuplicateLinesMergeHTTP: buy 10.00,
	// retail 10.00, stock 100 units (Quantity is milli-units).
	res, err := e.store.DB.Exec(`INSERT INTO Products(ProductName,BuyPrice,RetailPrice,Quantity,MinQuantity,IsActive)
		VALUES('سكر',1000,1000,100000,5,1)`)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := res.LastInsertId()
	e.openShift(mgr, 10000)

	before := e.queryInt(`SELECT COUNT(*) FROM Invoices`)
	code, loc, _ := e.postFormLoc(mgr, "/sales", url.Values{
		"customer_id":      {"1"},
		"sale_type":        {"retail"},
		"payment_method":   {"Cash"},
		"paid":             {"30.02"},
		"invoice_discount": {"0"},
		"line_product_id":  {itoa(pid), itoa(pid)},
		"line_qty":         {"1", "2"},
		"line_price":       {"10.00", "10.01"},
		"line_discount":    {"0", "0"},
	})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/sales/") {
		t.Fatalf("form merged sale: want 303 to /sales/{id}, got %d loc %q", code, loc)
	}
	var invID int64
	if _, err := fmt.Sscanf(loc, "/sales/%d", &invID); err != nil || invID == 0 {
		t.Fatalf("form merged sale: cannot parse invoice id from %q: %v", loc, err)
	}
	if n := e.queryInt(`SELECT COUNT(*) FROM InvoiceDetails WHERE InvoiceID=?`, invID); n != 1 {
		t.Fatalf("form merged sale: detail lines = %d, want 1", n)
	}
	if total := e.queryInt(`SELECT TotalPrice FROM InvoiceDetails WHERE InvoiceID=?`, invID); total != 3002 {
		t.Fatalf("form merged sale: stored line total = %d, want 3002", total)
	}

	// A zero-qty line (the transient qty-0 cart state) must be
	// refused and must not create an invoice.
	code, _ = e.postForm(mgr, "/sales", url.Values{
		"customer_id":      {"1"},
		"sale_type":        {"retail"},
		"payment_method":   {"Cash"},
		"paid":             {"0"},
		"invoice_discount": {"0"},
		"line_product_id":  {itoa(pid)},
		"line_qty":         {"0"},
		"line_price":       {"10.00"},
		"line_discount":    {"0"},
	})
	if code == http.StatusSeeOther {
		t.Fatalf("form zero-qty sale: want a refusal, got 303 (an invoice was created)")
	}
	if n := e.queryInt(`SELECT COUNT(*) FROM Invoices`); n != before+1 {
		t.Fatalf("form zero-qty sale: invoice count = %d, want %d (no invoice from the refused post)", n, before+1)
	}
}
