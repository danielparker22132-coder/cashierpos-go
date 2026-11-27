// Package web_test — FIX-17 lane E HTTP tests (SW2-L10 warehouse invoice
// view, SW2-L11 below-cost dead-end prompt).
package web_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// fix17SaleProduct creates a product with retail 30.00 and buy 20.00.
func fix17SaleProduct(t *testing.T, e *e2eEnv, admin *http.Client) int64 {
	t.Helper()
	id := e.createProduct(admin, "171001", "fix17prod", "30.00", "10")
	if _, err := e.store.DB.Exec(`UPDATE Products SET BuyPrice=2000 WHERE ProductID=?`, id); err != nil {
		t.Fatalf("set buy price: %v", err)
	}
	return id
}

// fix17SaleReq builds a /api/sales/create payload. unit 0 = default price.
func fix17SaleReq(pid int64, unit, disc int64, flag bool) map[string]any {
	return map[string]any{
		"sale_type": "retail",
		"lines": []map[string]any{
			{"product_id": pid, "qty": "1", "unit_price": unit, "item_discount": disc},
		},
		"paid": 1900, "payment_method": "Cash", "allow_below_cost": flag,
	}
}

// TestFix17WarehouseInvoiceViewHTTP (SW2-L10): warehouse gets the invoice
// list (read-only) and detail/API read access with no buy price or profit,
// stays 403 on every invoice mutation, and cashier stays own-only.
func TestFix17WarehouseInvoiceViewHTTP(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	wh := e.warehouseClient()
	cash := e.cashierClient()
	cash2 := e.loginAs("cashier", cashierPass) // second session, same cashier user; own-only checked via another user below
	_ = cash2
	pid := fix17SaleProduct(t, e, admin)
	e.openShift(cash, 10000)
	invID, invNum := e.cashSale(cash, pid, "1", 3000)
	_ = invNum

	// List: read-only, 200, shows the invoice, no POS form.
	code, body := e.get(wh, "/sales")
	if code != http.StatusOK {
		t.Fatalf("warehouse GET /sales = %d, want 200", code)
	}
	if !strings.Contains(body, fmt.Sprintf("%d", invNum)) {
		t.Fatalf("warehouse list missing invoice %d", invNum)
	}
	if strings.Contains(body, "فاتورة جديدة") || strings.Contains(body, "حفظ الفاتورة") {
		t.Fatal("warehouse list shows the POS sell form (must be read-only)")
	}

	// Detail: 200, no profit line, no cancel form, no buy price.
	code, body = e.get(wh, fmt.Sprintf("/sales/%d", invID))
	if code != http.StatusOK {
		t.Fatalf("warehouse GET /sales/%d = %d, want 200", invID, code)
	}
	if strings.Contains(body, "الربح:") {
		t.Fatal("warehouse detail leaks the profit line")
	}
	if strings.Contains(body, "إلغاء الفاتورة") {
		t.Fatal("warehouse detail shows the cancel form")
	}

	// API detail: 200, lines carry no buy_price.
	code, data := e.getJSON(wh, fmt.Sprintf("/api/sales/%d", invID))
	if code != http.StatusOK {
		t.Fatalf("warehouse GET /api/sales/%d = %d, want 200", invID, code)
	}
	lines, _ := data["lines"].([]any)
	if len(lines) != 1 {
		t.Fatalf("warehouse API lines = %v, want 1 line", data["lines"])
	}
	if lm, ok := lines[0].(map[string]any); ok {
		if _, leaked := lm["buy_price"]; leaked {
			t.Fatal("warehouse API line leaks buy_price")
		}
	}

	// Mutations stay 403: sale-create, cancel, return.
	code, _ = e.sell(wh, fix17SaleReq(pid, 0, 0, false))
	if code != http.StatusForbidden {
		t.Fatalf("warehouse POST /api/sales/create = %d, want 403", code)
	}
	code, _ = e.postJSON(wh, fmt.Sprintf("/api/sales/%d/cancel", invID), map[string]any{"reason": "x"})
	if code != http.StatusForbidden {
		t.Fatalf("warehouse POST cancel = %d, want 403", code)
	}
	code, _ = e.postForm(wh, "/sales", nil)
	if code != http.StatusForbidden {
		t.Fatalf("warehouse POST /sales form = %d, want 403", code)
	}

	// Cashier stays own-only: a different cashier's invoice is 403.
	// (Seed a second cashier user directly.)
	if _, err := e.store.DB.Exec(`INSERT INTO Users(UserName,Password,FullName,Role,IsActive,MustChangePassword,CreatedDate)
		VALUES(?,?,?,?,1,0,?)`, "cashier2", sha256Hex("Cash2Pass123"), "cashier2", "cashier", "2026-11-26"); err != nil {
		t.Fatalf("seed cashier2: %v", err)
	}
	cash2 = e.loginAs("cashier2", "Cash2Pass123")
	code, _ = e.get(cash2, fmt.Sprintf("/sales/%d", invID))
	if code != http.StatusForbidden {
		t.Fatalf("other cashier GET /sales/%d = %d, want 403", invID, code)
	}
	code, _ = e.getJSON(cash2, fmt.Sprintf("/api/sales/%d", invID))
	if code != http.StatusForbidden {
		t.Fatalf("other cashier GET /api/sales/%d = %d, want 403", invID, code)
	}
}

// TestFix17WarehouseInvoicePrint (SW2-L10 follow-on, coordinator): a
// warehouse user who may view an invoice may print it; another cashier
// may not print an invoice they cannot view. The print route is
// RequireLogin and handleSalePrint enforces the same GetInvoice
// visibility as the detail page.
func TestFix17WarehouseInvoicePrint(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	wh := e.warehouseClient()
	cash := e.cashierClient()
	pid := fix17SaleProduct(t, e, admin)
	e.openShift(cash, 10000)
	invID, _ := e.cashSale(cash, pid, "1", 3000)

	code, body := e.get(wh, fmt.Sprintf("/sales/%d/print", invID))
	if code != http.StatusOK {
		t.Fatalf("warehouse GET /sales/%d/print = %d, want 200", invID, code)
	}
	if strings.Contains(body, "سعر الشراء") || strings.Contains(body, "الربح") {
		t.Fatal("warehouse print leaks buy price or profit")
	}

	if _, err := e.store.DB.Exec(`INSERT INTO Users(UserName,Password,FullName,Role,IsActive,MustChangePassword,CreatedDate)
		VALUES(?,?,?,?,1,0,?)`, "cashier2", sha256Hex("Cash2Pass123"), "cashier2", "cashier", "2026-11-26"); err != nil {
		t.Fatalf("seed cashier2: %v", err)
	}
	cash2 := e.loginAs("cashier2", "Cash2Pass123")
	code, _ = e.get(cash2, fmt.Sprintf("/sales/%d/print", invID))
	if code != http.StatusForbidden {
		t.Fatalf("other cashier GET /sales/%d/print = %d, want 403", invID, code)
	}
}

// TestFix17BelowCostPromptHTTP (SW2-L11): supervisor gets an immediate 403
// (no dead-end prompt) on both below-cost paths; admin keeps the 400
// prompt unflagged and succeeds flagged; cashier keeps its 403.
func TestFix17BelowCostPromptHTTP(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	sup := e.supClient()
	cash := e.cashierClient()
	pid := fix17SaleProduct(t, e, admin)
	e.openShift(sup, 10000)
	e.openShift(cash, 10000)
	e.openShift(admin, 10000)

	before := e.queryInt(`SELECT COUNT(*) FROM Invoices`)
	deny := func(t *testing.T, who string, code int, body map[string]any) {
		t.Helper()
		if code != http.StatusForbidden {
			t.Fatalf("%s: want 403, got %d body=%v", who, code, body)
		}
		if errStr, _ := body["error"].(string); !strings.HasPrefix(errStr, "غير مسموح") {
			t.Fatalf("%s: 403 error = %q, want the uniform denial text", who, errStr)
		}
	}

	// Net-of-discount path: retail 3000, discount 1100 -> net 1900 < buy 2000.
	code, body := e.sell(sup, fix17SaleReq(pid, 0, 1100, false))
	deny(t, "supervisor net unflagged", code, body)
	code, body = e.sell(sup, fix17SaleReq(pid, 0, 1100, true))
	deny(t, "supervisor net flagged", code, body)
	code, body = e.sell(cash, fix17SaleReq(pid, 0, 1100, false))
	deny(t, "cashier net unflagged (discount gate)", code, body)
	code, body = e.sell(admin, fix17SaleReq(pid, 0, 1100, false))
	if code != http.StatusBadRequest {
		t.Fatalf("admin net unflagged: want 400 prompt, got %d body=%v", code, body)
	}
	if errStr, _ := body["error"].(string); !strings.Contains(errStr, "هل تريد البيع بخسارة") {
		t.Fatalf("admin net unflagged: error = %q, want the confirm prompt", errStr)
	}
	// Preview mirrors creation for the supervisor: 403, not the prompt.
	code, body = e.postJSON(sup, "/api/sales/preview", fix17SaleReq(pid, 0, 1100, false))
	deny(t, "supervisor preview net unflagged", code, body)

	// Unit-price path: unit 1900 < buy 2000.
	code, body = e.sell(sup, fix17SaleReq(pid, 1900, 0, false))
	deny(t, "supervisor unit-price unflagged", code, body)
	code, body = e.sell(admin, fix17SaleReq(pid, 1900, 0, false))
	if code != http.StatusBadRequest {
		t.Fatalf("admin unit-price unflagged: want 400 prompt, got %d body=%v", code, body)
	}

	// Admin flagged succeeds on both paths (2 invoices, nothing else posted).
	code, body = e.sell(admin, fix17SaleReq(pid, 0, 1100, true))
	if code != http.StatusOK {
		t.Fatalf("admin net flagged: want 200, got %d body=%v", code, body)
	}
	code, body = e.sell(admin, fix17SaleReq(pid, 1900, 0, true))
	if code != http.StatusOK {
		t.Fatalf("admin unit-price flagged: want 200, got %d body=%v", code, body)
	}
	if got := e.queryInt(`SELECT COUNT(*) FROM Invoices`); got != before+2 {
		t.Fatalf("invoice count = %d, want %d (denials must post nothing)", got, before+2)
	}
}
