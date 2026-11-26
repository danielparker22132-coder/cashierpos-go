// Package web_test — FIX-9 tests (Review-9, 0H / 1M / 4L).
//
// R9-M1: render() must skip zero-qty parts when emitting hidden
// inputs, or one dead part aborts the whole invoice server-side.
// R9-L3: a discounted row parked at qty 0 must not show a negative
// row total. R9-L2: the FIX-8 stash consumed itself on the first
// non-zero retype, so 3->0->0.001->3 drifted to 3003 — FIX-9 widens
// the stash so every retype scales from the stashed true split and
// only the exact restore consumes it. R9-L4: the scaleParts comment
// now states the honest spread/qty-dependent bound (no code change).
// R9-L5: only one of the two FIX-8 guard tests actually guarded the
// stash — this file adds a repo test that really exercises the
// round-trip, by executing the served sales script in node.
package web_test

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// extractSalesScript returns the <script> block containing the cart
// logic from a served /sales page. NOTE: JS `//` comments do NOT
// survive into served HTML — html/template strips them — so this
// test asserts behaviour by executing the code, never comment text.
func extractSalesScript(t *testing.T, body string) string {
	t.Helper()
	rest := body
	for {
		start := strings.Index(rest, "<script>")
		if start < 0 {
			break
		}
		rest = rest[start+len("<script>"):]
		end := strings.Index(rest, "</script>")
		if end < 0 {
			break
		}
		block := rest[:end]
		rest = rest[end+len("</script>"):]
		if strings.Contains(block, "function scaleParts") {
			return block
		}
	}
	t.Fatal("served /sales: no <script> block containing the cart logic (scaleParts)")
	return ""
}

// TestFix9_StashRoundTripNode is the R9-L5 real guard: it executes
// the SERVED /sales script in node, driving it through DOM stubs
// exactly like a browser (testdata/fix9_sales_driver.js), and
// asserts the stash round-trip behaviour end to end — 3->0->3 and
// 3->0->0.001->3 both restore gross 3002, zero-qty parts are never
// emitted, a discounted qty-0 row shows 0.00. Reverting the stash
// (or the R9-M1 skip) makes the driver fail, so this test FAILS on
// a stash regression — unlike TestFix8_MergedRowFormTotalExact,
// which passed with the stash removed.
func TestFix9_StashRoundTripNode(t *testing.T) {
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
	driver, err := os.ReadFile(filepath.Join("testdata", "fix9_sales_driver.js"))
	if err != nil {
		t.Fatalf("read testdata driver: %v", err)
	}
	if !strings.Contains(string(driver), "/*__SALES_SCRIPT__*/") {
		t.Fatalf("testdata driver lacks the __SALES_SCRIPT__ insertion marker")
	}
	combined := strings.Replace(string(driver), "/*__SALES_SCRIPT__*/", script, 1)
	dir := t.TempDir()
	combinedPath := filepath.Join(dir, "fix9-combined.mjs")
	if err := os.WriteFile(combinedPath, []byte(combined), 0o600); err != nil {
		t.Fatalf("write combined driver: %v", err)
	}
	cmd := exec.Command("node", "--check", combinedPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("node --check on the served /sales script failed: %v\n%s", err, out)
	}
	cmd = exec.Command("node", combinedPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node driver failed (stash round-trip broken): %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "FIX9-DRIVER-DONE") {
		t.Fatalf("node driver did not complete: %s", out)
	}
}

// TestFix9_ZeroQtyPartsSkippedLive is the live form-path check for
// R9-M1: the line shapes the fixed JS emits after the Review-9
// end-states (zero-qty parts omitted) must post 303 with the exact
// stored totals. The old poisonous shapes (zero-qty lines included)
// must still be refused — the server behaviour the fix relies on.
func TestFix9_ZeroQtyPartsSkippedLive(t *testing.T) {
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

	post := func(name, paid string, qtys, prices []string, wantTotal int64) {
		t.Helper()
		before := e.queryInt(`SELECT COUNT(*) FROM Invoices`)
		v := url.Values{
			"customer_id":      {"1"},
			"sale_type":        {"retail"},
			"payment_method":   {"Cash"},
			"paid":             {paid},
			"invoice_discount": {"0"},
		}
		for _, q := range qtys {
			v.Add("line_qty", q)
		}
		for range qtys {
			v.Add("line_product_id", itoa(pid))
		}
		for _, p := range prices {
			v.Add("line_price", p)
		}
		for range qtys {
			v.Add("line_discount", "0")
		}
		code, loc, body := e.postFormLoc(mgr, "/sales", v)
		if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/sales/") {
			t.Fatalf("%s: want 303 to /sales/{id}, got %d loc %q body=%q", name, code, loc, truncate(body, 300))
		}
		var invID int64
		if _, err := fmt.Sscanf(loc, "/sales/%d", &invID); err != nil || invID == 0 {
			t.Fatalf("%s: cannot parse invoice id from %q: %v", name, loc, err)
		}
		if n := e.queryInt(`SELECT COUNT(*) FROM InvoiceDetails WHERE InvoiceID=?`, invID); n != 1 {
			t.Fatalf("%s: detail lines = %d, want 1", name, n)
		}
		if total := e.queryInt(`SELECT TotalPrice FROM InvoiceDetails WHERE InvoiceID=?`, invID); total != wantTotal {
			t.Fatalf("%s: stored line total = %d, want %d", name, total, wantTotal)
		}
		if n := e.queryInt(`SELECT COUNT(*) FROM Invoices`); n != before+1 {
			t.Fatalf("%s: invoice count = %d, want %d", name, n, before+1)
		}
	}

	// A. merge-at-0 + retype 3, as the fixed JS emits it: only the
	// live 3x10.00 part (the two dead zero-qty parts are skipped).
	post("R9-M1 case A (merge-at-0 retype 3)", "30.00", []string{"3"}, []string{"10.00"}, 3000)
	// B. 3->0.001, as emitted: only the live 0.001x10.01 part.
	post("R9-M1 case B (3->0.001)", "0.01", []string{"0.001"}, []string{"10.01"}, 1)
	// C. 3->0.001->3, as emitted: only the live 3x10.01 part.
	post("R9-M1 case C (3->0.001->3)", "30.03", []string{"3"}, []string{"10.01"}, 3003)
	// D. control, 3->1: both parts live, total 1001 exact.
	post("R9-M1 control D (3->1)", "10.01", []string{"0.333", "0.667"}, []string{"10.00", "10.01"}, 1001)

	// The old poisonous shapes (zero-qty lines included) must still
	// be refused by the server — nothing wrong can be stored.
	before := e.queryInt(`SELECT COUNT(*) FROM Invoices`)
	code, _ := e.postForm(mgr, "/sales", url.Values{
		"customer_id":      {"1"},
		"sale_type":        {"retail"},
		"payment_method":   {"Cash"},
		"paid":             {"30.00"},
		"invoice_discount": {"0"},
		"line_product_id":  {itoa(pid), itoa(pid), itoa(pid)},
		"line_qty":         {"0", "0", "3"},
		"line_price":       {"10.00", "10.01", "10.00"},
		"line_discount":    {"0", "0", "0"},
	})
	if code == http.StatusSeeOther {
		t.Fatalf("poisonous shape A (zero-qty lines included): want a refusal, got 303")
	}
	if n := e.queryInt(`SELECT COUNT(*) FROM Invoices`); n != before {
		t.Fatalf("poisonous shape A: invoice count = %d, want %d (no invoice from the refused post)", n, before)
	}
}
