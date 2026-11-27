// Package web_test — FIX-3 web-level regression tests.
package web_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestFix3_DuplicateLinesMergeHTTP (FIX-3 duplicate rule): the API,
// the preview and the HTML form all merge a re-added product into one
// invoice line — same price and different price — with the exact total.
func TestFix3_DuplicateLinesMergeHTTP(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	res, err := e.store.DB.Exec(`INSERT INTO Products(ProductName,BuyPrice,RetailPrice,Quantity,MinQuantity,IsActive)
		VALUES('سكر',1000,2000,100000,5,1)`)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := res.LastInsertId()
	e.openShift(admin, 10000)

	dupBody := map[string]any{
		"customer_id": 1, "sale_type": "retail", "payment_method": "Cash",
		"lines": []any{
			map[string]any{"product_id": pid, "qty": "2.000", "unit_price": 5000},
			map[string]any{"product_id": pid, "qty": "1.000", "unit_price": 6000},
		},
	}
	// Preview: merged gross/net 16000 (2×5000 + 1×6000).
	if code, data := e.postJSON(admin, "/api/sales/preview", dupBody); code != http.StatusOK {
		t.Fatalf("preview duplicate: want 200, got %d %v", code, data)
	} else if data["gross"] != float64(16000) || data["net"] != float64(16000) {
		t.Fatalf("preview duplicate: gross=%v net=%v, want 16000/16000", data["gross"], data["net"])
	}
	// API create: one detail line, qty 3, unit 5333 (weighted avg),
	// total 16000, invoice gross/net 16000.
	code, data := e.sell(admin, dupBody)
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("sale duplicate: want 200 ok, got %d %v", code, data)
	}
	if data["gross"] != float64(16000) || data["net"] != float64(16000) {
		t.Fatalf("sale duplicate: gross=%v net=%v, want 16000/16000", data["gross"], data["net"])
	}
	invID, _ := data["invoice_id"].(float64)
	if n := e.queryInt(`SELECT COUNT(*) FROM InvoiceDetails WHERE InvoiceID=?`, int64(invID)); n != 1 {
		t.Fatalf("sale duplicate: detail lines = %d, want 1", n)
	}
	if unit := e.queryInt(`SELECT UnitPrice FROM InvoiceDetails WHERE InvoiceID=?`, int64(invID)); unit != 5333 {
		t.Fatalf("sale duplicate: stored unit price = %d, want 5333 (weighted avg)", unit)
	}
	if total := e.queryInt(`SELECT TotalPrice FROM InvoiceDetails WHERE InvoiceID=?`, int64(invID)); total != 16000 {
		t.Fatalf("sale duplicate: stored line total = %d, want 16000", total)
	}

	// HTML form: two rows for the same product at 50.00 / 60.00 post
	// as two additions and still land as ONE invoice line.
	code, loc, _ := e.postFormLoc(admin, "/sales", url.Values{
		"customer_id":      {"1"},
		"sale_type":        {"retail"},
		"payment_method":   {"Cash"},
		"paid":             {"160"},
		"invoice_discount": {"0"},
		"line_product_id":  {itoa(pid), itoa(pid)},
		"line_qty":         {"2", "1"},
		"line_price":       {"50.00", "60.00"},
		"line_discount":    {"0", "0"},
	})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/sales/") {
		t.Fatalf("form duplicate: want 303 to /sales/{id}, got %d loc %q", code, loc)
	}
	var formInvID int64
	if _, err := fmt.Sscanf(loc, "/sales/%d", &formInvID); err != nil || formInvID == 0 {
		t.Fatalf("form duplicate: cannot parse invoice id from %q: %v", loc, err)
	}
	if n := e.queryInt(`SELECT COUNT(*) FROM InvoiceDetails WHERE InvoiceID=?`, formInvID); n != 1 {
		t.Fatalf("form duplicate: detail lines = %d, want 1", n)
	}
	if total := e.queryInt(`SELECT TotalPrice FROM InvoiceDetails WHERE InvoiceID=?`, formInvID); total != 16000 {
		t.Fatalf("form duplicate: stored line total = %d, want 16000", total)
	}
}

// TestFix3_BelowCostPermissionHTTP (FIX-3 H2, amended by FIX-12 SW-M10 +
// SW-L08): the preview refuses a cashier's AND a supervisor's below-cost
// confirmation with 403 (§15 row 17: supervisor ❌), service-layer
// denials map to 403, and the sales form only shows the checkbox to
// roles that hold approve_below_cost (admin/manager only).
func TestFix3_BelowCostPermissionHTTP(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	cash := e.cashierClient()
	sup := e.supClient()
	res, err := e.store.DB.Exec(`INSERT INTO Products(ProductName,BuyPrice,RetailPrice,Quantity,MinQuantity,IsActive)
		VALUES('سكر',1000,2000,100000,5,1)`)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := res.LastInsertId()
	res, err = e.store.DB.Exec(`INSERT INTO Customers(CustomerName,CreditLimit,IsActive) VALUES('عميل',0,1)`)
	if err != nil {
		t.Fatal(err)
	}
	custID, _ := res.LastInsertId()
	e.openShift(cash, 10000)
	e.openShift(sup, 10000)
	e.openShift(admin, 10000)
	body := map[string]any{
		"customer_id": custID, "sale_type": "retail", "payment_method": "Cash",
		"paid": 500, "allow_below_cost": true,
		// Below cost via item discount (retail 2000 - 1500 = 500 < buy
		// 1000), not a lowered unit price — the supervisor holds
		// PermGiveDiscount but not PermEditPrice.
		"lines": []any{map[string]any{"product_id": pid, "qty": "1.000", "item_discount": 1500}},
	}
	// Cashier: 403 from the preview.
	if code, data := e.postJSON(cash, "/api/sales/preview", body); code != http.StatusForbidden {
		t.Fatalf("cashier preview with allow_below_cost: want 403, got %d %v", code, data)
	}
	// Cashier: refused from the real sale endpoint too (FIX-12 SW-L08:
	// service-layer denials map to 403, not 400).
	if code, data := e.sell(cash, body); code != http.StatusForbidden {
		t.Fatalf("cashier sale with allow_below_cost: want 403, got %d %v", code, data)
	} else if msg, _ := data["error"].(string); !strings.Contains(msg, "غير مسموح") {
		t.Fatalf("cashier sale error = %q, want a permission refusal", msg)
	}
	// Supervisor: refused too (FIX-12 SW-M10, §15 row 17) — preview and
	// sale both 403, no invoice created.
	if code, data := e.postJSON(sup, "/api/sales/preview", body); code != http.StatusForbidden {
		t.Fatalf("supervisor preview with allow_below_cost: want 403, got %d %v", code, data)
	}
	if code, data := e.sell(sup, body); code != http.StatusForbidden {
		t.Fatalf("supervisor sale with allow_below_cost: want 403, got %d %v", code, data)
	} else if msg, _ := data["error"].(string); !strings.Contains(msg, "غير مسموح") {
		t.Fatalf("supervisor sale error = %q, want a permission refusal", msg)
	}
	// Admin: preview OK, sale posts (admin still holds the permission).
	if code, data := e.postJSON(admin, "/api/sales/preview", body); code != http.StatusOK || data["ok"] != true {
		t.Fatalf("admin preview with allow_below_cost: want 200 ok, got %d %v", code, data)
	}
	if code, data := e.sell(admin, body); code != http.StatusOK || data["ok"] != true {
		t.Fatalf("admin sale with allow_below_cost: want 200 ok, got %d %v", code, data)
	}
	// Form: no checkbox for the cashier, none for the supervisor either.
	if code, html := e.get(cash, "/sales"); code != http.StatusOK {
		t.Fatalf("cashier /sales: want 200, got %d", code)
	} else if strings.Contains(html, `name="allow_below_cost"`) {
		t.Fatal("cashier sales form must not show the below-cost checkbox")
	}
	if code, html := e.get(sup, "/sales"); code != http.StatusOK {
		t.Fatalf("supervisor /sales: want 200, got %d", code)
	} else if strings.Contains(html, `name="allow_below_cost"`) {
		t.Fatal("supervisor sales form must not show the below-cost checkbox")
	}
}
