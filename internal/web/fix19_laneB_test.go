// Package web_test — FIX-19 lane B (R19-B-F2, R19-B-F3).
//
// B-F2: MulQty is checked — a unit price whose raw price*qty product
// overflows int64 is refused with 400 and no row is stored, on the
// purchase path and on the typed sale path. Large-but-valid values just
// below the boundary still store exactly.
// B-F3: out-of-range floats are refused at parse time with the too-large
// message, on every amountFromValue / apiAmount / paid_cents / shift
// opening site. The benign 1e17 credit limit still stores exactly.
package web_test

import (
	"net/http"
	"strings"
	"testing"
)

func fix19Err(t *testing.T, body map[string]any) string {
	t.Helper()
	s, _ := body["error"].(string)
	return s
}

func fix19WantTooLarge(t *testing.T, what string, code int, body map[string]any) {
	t.Helper()
	if code != http.StatusBadRequest {
		t.Fatalf("%s: want 400, got %d %v", what, code, body)
	}
	if !strings.Contains(fix19Err(t, body), "كبير جداً") {
		t.Fatalf("%s: error %q does not cite the overflow (want the parse-time too-large refusal)", what, fix19Err(t, body))
	}
}

func fix19Vendor(t *testing.T, e *e2eEnv, c *http.Client, name string) int64 {
	t.Helper()
	code, body := e.postJSON(c, "/api/vendors", map[string]any{"name": name})
	if code != http.StatusOK || body["ok"] != true {
		t.Fatalf("create vendor %s: want 200 ok, got %d %v", name, code, body)
	}
	vid, _ := body["vendor_id"].(float64)
	if vid == 0 {
		t.Fatalf("create vendor %s: no vendor_id in %v", name, body)
	}
	return int64(vid)
}

func TestFix19Web_B2_PurchaseMulQtyOverflow(t *testing.T) {
	e := readyEnv(t, false)
	mgr := e.managerClient()
	admin := e.adminClient()

	vid := fix19Vendor(t, e, mgr, "مورد F19-B2")
	pid := e.createProduct(admin, "F19-B2", "أرز", "30.00", "100")
	before := e.queryInt(`SELECT COUNT(*) FROM PurchaseInvoices`)
	stockBefore := e.queryInt(`SELECT Quantity FROM Products WHERE ProductID=?`, pid)

	// The review repro: price 1e17¢ × qty 100 used to store 200 with the
	// wrapped total 1,864,712,049,423,024¢. Now it must be refused.
	code, body := e.postJSON(mgr, "/api/purchases", map[string]any{
		"vendor_id": vid,
		"lines":     []map[string]any{{"product_id": pid, "qty": "100", "price": float64(1e17)}},
		"paid": 0, "method": "Visa",
	})
	fix19WantTooLarge(t, "purchase price 1e17 x qty 100", code, body)
	if got := e.queryInt(`SELECT COUNT(*) FROM PurchaseInvoices`); got != before {
		t.Fatalf("purchase count after refused 1e17 x 100 = %d, want %d (no row stored)", got, before)
	}
	if got := e.queryInt(`SELECT Quantity FROM Products WHERE ProductID=?`, pid); got != stockBefore {
		t.Fatalf("stock after refused purchase = %d, want %d (no stock posted)", got, stockBefore)
	}

	// qty:2 control — the wrap lands negative there, so the old total<=0
	// guard already refused it. It must still be 400.
	code, body = e.postJSON(mgr, "/api/purchases", map[string]any{
		"vendor_id": vid,
		"lines":     []map[string]any{{"product_id": pid, "qty": "2", "price": float64(1e17)}},
		"paid": 0, "method": "Visa",
	})
	fix19WantTooLarge(t, "purchase price 1e17 x qty 2 (negative-wrap control)", code, body)
	if got := e.queryInt(`SELECT COUNT(*) FROM PurchaseInvoices`); got != before {
		t.Fatalf("purchase count after refused 1e17 x 2 = %d, want %d", got, before)
	}

	// Just below the boundary: 9e15¢ × qty 1 has raw product 9e18, which
	// fits — it must succeed and store exactly 9e15¢.
	code, body = e.postJSON(mgr, "/api/purchases", map[string]any{
		"vendor_id": vid,
		"lines":     []map[string]any{{"product_id": pid, "qty": "1", "price": float64(9e15)}},
		"paid": 0, "method": "Visa",
	})
	if code != http.StatusOK || body["ok"] != true {
		t.Fatalf("purchase price 9e15 x qty 1: want 200 ok, got %d %v", code, body)
	}
	purchID, _ := body["purchase_id"].(float64)
	if total := e.queryInt(`SELECT TotalAmount FROM PurchaseInvoices WHERE PurchaseID=?`, int64(purchID)); total != 9000000000000000 {
		t.Fatalf("valid large purchase TotalAmount = %d, want 9000000000000000", total)
	}
	if got := e.queryInt(`SELECT Quantity FROM Products WHERE ProductID=?`, pid); got != stockBefore+1000 {
		t.Fatalf("stock after valid large purchase = %d, want %d", got, stockBefore+1000)
	}
}

func TestFix19Web_B2_TypedSaleMulQtyOverflow(t *testing.T) {
	e := readyEnv(t, false)
	mgr := e.managerClient()
	admin := e.adminClient()
	e.openShift(mgr, 100000)

	pid := e.createProduct(admin, "F19-B2-SALE", "زيت", "30.00", "100")
	before := e.queryInt(`SELECT COUNT(*) FROM Invoices`)

	// The review repro: unit_price 1e17¢, qty 1, as manager used to store
	// 200 with net 7,766,279,631,452,242¢. Now it must be refused.
	code, body := e.sell(mgr, map[string]any{
		"sale_type": "retail",
		"lines":     []map[string]any{{"product_id": pid, "qty": "1", "unit_price": int64(100000000000000000)}},
		"paid": 0, "payment_method": "Cash",
	})
	fix19WantTooLarge(t, "typed sale unit_price 1e17 x qty 1", code, body)
	if got := e.queryInt(`SELECT COUNT(*) FROM Invoices`); got != before {
		t.Fatalf("invoice count after refused 1e17 sale = %d, want %d (no row stored)", got, before)
	}

	// Just below the boundary: 9e15¢ × qty 1 stores exactly (paid 0 keeps
	// the S-19 auto-pay-full convention for the cash customer).
	code, body = e.sell(mgr, map[string]any{
		"sale_type": "retail",
		"lines":     []map[string]any{{"product_id": pid, "qty": "1", "unit_price": int64(9000000000000000)}},
		"paid": 0, "payment_method": "Cash",
	})
	if code != http.StatusOK || body["ok"] != true {
		t.Fatalf("typed sale unit_price 9e15 x qty 1: want 200 ok, got %d %v", code, body)
	}
	if net, _ := body["net"].(float64); net != 9000000000000000 {
		t.Fatalf("valid large sale net = %v, want 9000000000000000", body["net"])
	}
	invID, _ := body["invoice_id"].(float64)
	if net := e.queryInt(`SELECT NetAmount FROM Invoices WHERE InvoiceID=?`, int64(invID)); net != 9000000000000000 {
		t.Fatalf("valid large sale stored NetAmount = %d, want 9000000000000000", net)
	}
}

func TestFix19Web_B3_ParseTimeRangeGuard(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	mgr := e.managerClient()
	cashier := e.cashierClient()

	// amountFromValue: customer credit_limit 1e19 and 2^63 refuse at parse
	// time (before the fix they wrapped to min-int64 and were refused only
	// by the downstream negative check, with the wrong message).
	custBefore := e.queryInt(`SELECT COUNT(*) FROM Customers`)
	code, body := e.postJSON(admin, "/api/customers", map[string]any{"name": "عميل F19-1e19", "credit_limit": float64(1e19)})
	fix19WantTooLarge(t, "customer credit_limit 1e19", code, body)
	code, body = e.postJSON(admin, "/api/customers", map[string]any{"name": "عميل F19-2^63", "credit_limit": 9223372036854775808.0})
	fix19WantTooLarge(t, "customer credit_limit 2^63", code, body)
	if got := e.queryInt(`SELECT COUNT(*) FROM Customers`); got != custBefore {
		t.Fatalf("customer count after refused limits = %d, want %d", got, custBefore)
	}

	// The benign 1e17 limit still stores exactly (no product cap).
	id := fix17Customer(t, e, admin, map[string]any{"name": "عميل F19-1e17", "credit_limit": float64(1e17)})
	if lim := e.queryInt(`SELECT CreditLimit FROM Customers WHERE CustomerID=?`, id); lim != 100000000000000000 {
		t.Fatalf("credit_limit 1e17: stored %d, want 100000000000000000", lim)
	}

	// amountFromValue: vendor credit_limit 1e19.
	vendBefore := e.queryInt(`SELECT COUNT(*) FROM Vendors`)
	code, body = e.postJSON(admin, "/api/vendors", map[string]any{"name": "مورد F19-1e19", "credit_limit": float64(1e19)})
	fix19WantTooLarge(t, "vendor credit_limit 1e19", code, body)
	if got := e.queryInt(`SELECT COUNT(*) FROM Vendors`); got != vendBefore {
		t.Fatalf("vendor count after refused limit = %d, want %d", got, vendBefore)
	}

	// amountFromValue on the purchase path: paid 1e19 and line price 1e19.
	vid := fix19Vendor(t, e, mgr, "مورد F19-B3")
	pid := e.createProduct(admin, "F19-B3", "سكر", "30.00", "100")
	purchBefore := e.queryInt(`SELECT COUNT(*) FROM PurchaseInvoices`)
	code, body = e.postJSON(mgr, "/api/purchases", map[string]any{
		"vendor_id": vid,
		"lines":     []map[string]any{{"product_id": pid, "qty": "1", "price": "5.00"}},
		"paid": float64(1e19), "method": "Visa",
	})
	fix19WantTooLarge(t, "purchase paid 1e19", code, body)
	code, body = e.postJSON(mgr, "/api/purchases", map[string]any{
		"vendor_id": vid,
		"lines":     []map[string]any{{"product_id": pid, "qty": "1", "price": float64(1e19)}},
		"paid": 0, "method": "Visa",
	})
	fix19WantTooLarge(t, "purchase line price 1e19", code, body)

	// paid_cents fallback: 1e19 refuses at parse time, not downstream.
	code, body = e.postJSON(mgr, "/api/purchases", map[string]any{
		"vendor_id": vid,
		"lines":     []map[string]any{{"product_id": pid, "qty": "1", "price": "5.00"}},
		"paid_cents": float64(1e19), "method": "Visa",
	})
	fix19WantTooLarge(t, "purchase paid_cents 1e19", code, body)
	if got := e.queryInt(`SELECT COUNT(*) FROM PurchaseInvoices`); got != purchBefore {
		t.Fatalf("purchase count after refused 1e19 payloads = %d, want %d", got, purchBefore)
	}

	// apiAmount amount_cents branch: expense 1e19 and 2^63 refuse at parse time.
	e.openShift(cashier, 100000)
	code, body = e.postJSON(cashier, "/api/expenses", map[string]any{
		"description": "مصروف F19", "amount_cents": float64(1e19), "category": "عام",
	})
	fix19WantTooLarge(t, "expense amount_cents 1e19", code, body)
	code, body = e.postJSON(cashier, "/api/expenses", map[string]any{
		"description": "مصروف F19", "amount_cents": 9223372036854775808.0, "category": "عام",
	})
	fix19WantTooLarge(t, "expense amount_cents 2^63", code, body)
	if got := e.queryInt(`SELECT COUNT(*) FROM Expenses WHERE Description='مصروف F19'`); got != 0 {
		t.Fatalf("expenses stored after refused 1e19 payloads = %d, want 0", got)
	}

	// Shift opening branches: opening_cents 1e19 and opening 1e17 EGP
	// (x100 = 1e19¢) refuse at parse time; a normal open still works.
	code, body = e.postJSON(mgr, "/api/shifts/open", map[string]any{"opening_cents": float64(1e19)})
	fix19WantTooLarge(t, "shift opening_cents 1e19", code, body)
	code, body = e.postJSON(mgr, "/api/shifts/open", map[string]any{"opening": float64(1e17)})
	fix19WantTooLarge(t, "shift opening 1e17 EGP", code, body)
	if id := e.openShift(mgr, 100000); id == 0 {
		t.Fatal("open shift with a normal amount must still work after the refused opens")
	}
}
