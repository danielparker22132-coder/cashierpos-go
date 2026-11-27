// Package web_test — FIX-25 lane A (Observation E, shifts + sales):
// no JSON-API 400/404 may echo a raw driver/DB error to the client.
// Every raw-capable site in handlers_shifts.go / handlers_sales.go now
// routes store errors through apiErrForService: denial -> 403 with the
// service text, overflow -> 400 Arabic, an exact "…غير موجود" sentinel
// (or "لم يتم العثور على …") -> 404 with that text, a raw driver error
// -> 500 with the generic Arabic message, and any other safe Arabic
// business error stays 400 with the service text.
package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"cashierpos-go/internal/store"
)

const fix25AGeneric500 = "حدث خطأ داخلي، حاول مرة أخرى"
const fix25AOverflow = "المبلغ كبير جداً — تجاوز الحد الأقصى المسموح به"
const fix25ABig = int64(5000000000000000000)

var fix25ARawLeaks = []string{"SQL logic error", "no such table", "no such column",
	"integer overflow", "sqlite", "no rows in result set", "converting driver"}

// fix25AAssertNoRaw fails if the error text carries any raw driver string.
func fix25AAssertNoRaw(t *testing.T, what, errMsg string) {
	t.Helper()
	for _, leak := range fix25ARawLeaks {
		if strings.Contains(errMsg, leak) {
			t.Fatalf("%s: error leaks raw string %q in %q", what, leak, errMsg)
		}
	}
}

// fix25AAssertGeneric500 fails unless the call answered 500 with exactly
// the generic Arabic message and no raw driver string anywhere.
func fix25AAssertGeneric500(t *testing.T, what string, code int, body map[string]any) {
	t.Helper()
	if code != http.StatusInternalServerError {
		t.Fatalf("%s: status = %d, want 500 (body %v)", what, code, body)
	}
	if body["ok"] != false {
		t.Fatalf("%s: ok = %v, want false (body %v)", what, body["ok"], body)
	}
	errMsg, _ := body["error"].(string)
	if errMsg != fix25AGeneric500 {
		t.Fatalf("%s: error = %q, want generic %q", what, errMsg, fix25AGeneric500)
	}
	fix25AAssertNoRaw(t, what, errMsg)
}

// fix25ADrop drops one table out from under the running server.
func fix25ADrop(t *testing.T, e *e2eEnv, table string) {
	t.Helper()
	if _, err := e.store.DB.Exec("DROP TABLE " + table); err != nil {
		t.Fatalf("DROP TABLE %s: %v", table, err)
	}
}

func fix25AEnv(t *testing.T) (*e2eEnv, *http.Client) {
	t.Helper()
	e := newEnv(t, false)
	e.seedUsers()
	e.changeAdminPassword()
	return e, e.adminClient()
}

// TestFix25A_ShiftCloseDropExpensesGeneric500 is the Observation E repro
// itself: DROP TABLE Expenses, then POST /api/shifts/close must answer
// 500 generic — never the 400 raw "SQL logic error: no such table:
// Expenses (1)" the pre-fix helper echoed.
func TestFix25A_ShiftCloseDropExpensesGeneric500(t *testing.T) {
	e, _ := fix25AEnv(t)
	mgr := e.managerClient()
	e.openShift(mgr, 100000)
	fix25ADrop(t, e, "Expenses")
	code, body := e.postJSON(mgr, "/api/shifts/close", map[string]any{})
	fix25AAssertGeneric500(t, "close after DROP Expenses", code, body)
}

// TestFix25A_ExpenseDeleteDropExpensesGeneric500 covers apiExpenseDelete
// (:1336 via apiErrForService): DELETE /api/expenses/1 after DROP
// TABLE Expenses must answer 500 generic, not 400 raw.
func TestFix25A_ExpenseDeleteDropExpensesGeneric500(t *testing.T) {
	e, admin := fix25AEnv(t)
	fix25ADrop(t, e, "Expenses")
	code, body := e.deleteJSON(admin, "/api/expenses/1")
	fix25AAssertGeneric500(t, "DELETE /api/expenses/1 after DROP Expenses", code, body)
}

// TestFix25A_ExpenseCreateDropExpensesGeneric500 covers apiExpenseCreate
// (:1283 RegisterExpense): POST /api/expenses after DROP TABLE Expenses
// must answer 500 generic, not 400 raw.
func TestFix25A_ExpenseCreateDropExpensesGeneric500(t *testing.T) {
	e, admin := fix25AEnv(t)
	// RegisterExpense checks for an open shift before touching the
	// Expenses table, so open one first, then drop Expenses.
	e.openShift(admin, 100000)
	fix25ADrop(t, e, "Expenses")
	code, body := e.postJSON(admin, "/api/expenses", map[string]any{
		"description": "test", "amount_cents": 1000, "category": "عام"})
	fix25AAssertGeneric500(t, "POST /api/expenses after DROP Expenses", code, body)
}

// TestFix25A_ProductGetDropProductsGeneric500 covers handleAPIProductGet
// (:1489): GET /api/products/1 after DROP TABLE Products must answer
// 500 generic, not 404 raw.
func TestFix25A_ProductGetDropProductsGeneric500(t *testing.T) {
	e, admin := fix25AEnv(t)
	fix25ADrop(t, e, "Products")
	code, body := e.getJSON(admin, "/api/products/1")
	fix25AAssertGeneric500(t, "GET /api/products/1 after DROP Products", code, body)
}

// TestFix25A_SaleGetDropInvoicesGeneric500 covers handleAPISaleGet
// (:1387): GET /api/sales/1 after DROP TABLE Invoices must answer 500
// generic, not 404 raw.
func TestFix25A_SaleGetDropInvoicesGeneric500(t *testing.T) {
	e, admin := fix25AEnv(t)
	fix25ADrop(t, e, "Invoices")
	code, body := e.getJSON(admin, "/api/sales/1")
	fix25AAssertGeneric500(t, "GET /api/sales/1 after DROP Invoices", code, body)
}

// TestFix25A_BarcodeDropProductsGeneric500 covers handleAPIProductBarcode
// (:1468): the barcode lookup after DROP TABLE Products must answer 500
// generic, not 404 raw.
func TestFix25A_BarcodeDropProductsGeneric500(t *testing.T) {
	e, admin := fix25AEnv(t)
	fix25ADrop(t, e, "Products")
	code, body := e.getJSON(admin, "/api/products/barcode/NOPE-1")
	fix25AAssertGeneric500(t, "GET barcode after DROP Products", code, body)
}

// TestFix25A_StocktakeGetDropStocktakesGeneric500 covers
// handleAPIStocktakeGet (:1521): GET /api/stocktakes/1 after DROP TABLE
// Stocktakes must answer 500 generic, not 404 raw.
func TestFix25A_StocktakeGetDropStocktakesGeneric500(t *testing.T) {
	e, admin := fix25AEnv(t)
	fix25ADrop(t, e, "Stocktakes")
	code, body := e.getJSON(admin, "/api/stocktake/1")
	fix25AAssertGeneric500(t, "GET /api/stocktake/1 after DROP Stocktakes", code, body)
}

// TestFix25A_DropTableMatrix covers every other changed JSON site in
// the lane: each case drops the table its store call reads, on a fresh
// env, and the endpoint must answer 500 generic — never a raw driver
// string at 400/404.
func TestFix25A_DropTableMatrix(t *testing.T) {
	cases := []struct {
		name  string
		table string
		call  func(e *e2eEnv, admin *http.Client) (int, map[string]any)
	}{
		{"shift-open", "Shifts", func(e *e2eEnv, admin *http.Client) (int, map[string]any) {
			return e.postJSON(admin, "/api/shifts/open", map[string]any{"opening_cents": 100})
		}},
		{"cashbox-in", "Shifts", func(e *e2eEnv, admin *http.Client) (int, map[string]any) {
			return e.postJSON(admin, "/api/cashbox/in", map[string]any{"shift_id": 1, "amount_cents": 100, "reason": "x"})
		}},
		{"cashbox-out", "Shifts", func(e *e2eEnv, admin *http.Client) (int, map[string]any) {
			return e.postJSON(admin, "/api/cashbox/out", map[string]any{"shift_id": 1, "amount_cents": 100, "reason": "x"})
		}},
		{"cashcount-create", "Shifts", func(e *e2eEnv, admin *http.Client) (int, map[string]any) {
			return e.postJSON(admin, "/api/cashbox/count", map[string]any{"shift_id": 1, "amount_cents": 100})
		}},
		{"cashcount-review", "CashCounts", func(e *e2eEnv, admin *http.Client) (int, map[string]any) {
			return e.postJSON(admin, "/api/cashbox/count/review", map[string]any{"count_id": 1, "approve": true})
		}},
		{"cashcounts-list", "Shifts", func(e *e2eEnv, admin *http.Client) (int, map[string]any) {
			return e.getJSON(admin, "/api/cashbox/counts?shift_id=1")
		}},
		{"cashcount-approvals", "CashCounts", func(e *e2eEnv, admin *http.Client) (int, map[string]any) {
			return e.getJSON(admin, "/api/cashcount-approvals")
		}},
		{"returns-analysis", "Returns", func(e *e2eEnv, admin *http.Client) (int, map[string]any) {
			return e.getJSON(admin, "/api/returns/analysis")
		}},
		{"return-create", "Invoices", func(e *e2eEnv, admin *http.Client) (int, map[string]any) {
			return e.postJSON(admin, "/api/returns", map[string]any{
				"invoice_id": 1, "reason": "x", "method": "cash",
				"lines": []map[string]any{{"product_id": 1, "qty": "1"}}})
		}},
		{"expense-update", "Expenses", func(e *e2eEnv, admin *http.Client) (int, map[string]any) {
			return e.putJSON(admin, "/api/expenses/1", map[string]any{
				"description": "x", "amount_cents": 100, "category": "عام"})
		}},
		{"sale-preview", "Products", func(e *e2eEnv, admin *http.Client) (int, map[string]any) {
			mgr := e.managerClient()
			e.openShift(mgr, 100000)
			return e.postJSON(mgr, "/api/sales/preview", map[string]any{
				"sale_type": "retail",
				"lines":     []map[string]any{{"product_id": 1, "qty": "1"}},
				"paid":      1000})
		}},
		{"sale-cancel", "Invoices", func(e *e2eEnv, admin *http.Client) (int, map[string]any) {
			return e.postJSON(admin, "/api/sales/1/cancel", map[string]any{"reason": "x"})
		}},
		{"product-search", "Products", func(e *e2eEnv, admin *http.Client) (int, map[string]any) {
			return e.getJSON(admin, "/api/products/search?q=x")
		}},
		{"offers-list", "Offers", func(e *e2eEnv, admin *http.Client) (int, map[string]any) {
			return e.getJSON(admin, "/api/offers")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, admin := fix25AEnv(t)
			fix25ADrop(t, e, tc.table)
			code, body := tc.call(e, admin)
			fix25AAssertGeneric500(t, tc.name+" after DROP "+tc.table, code, body)
		})
	}
}

// TestFix25A_ReturnAllDropInvoiceDetailsGeneric500 covers apiReturnAll
// (:1226 ProcessReturnAll): ProcessReturnAll reads the invoice lines
// from InvoiceDetails first, so the dropped table that reaches its
// driver error is InvoiceDetails, on a real invoice — dropping Invoices
// alone never reaches a driver error for a missing invoice (the store
// answers the Arabic "لا توجد كمية متاحة للمرتجع" business error).
func TestFix25A_ReturnAllDropInvoiceDetailsGeneric500(t *testing.T) {
	e, _ := fix25AEnv(t)
	mgr := e.managerClient()
	pid := e.createProduct(mgr, "A25-RETALL", "retall", "10.00", "10")
	e.openShift(mgr, 100000)
	invID, _ := e.cashSale(mgr, pid, "1", 1000)
	fix25ADrop(t, e, "InvoiceDetails")
	code, body := e.postJSON(mgr, "/api/returns/all", map[string]any{"invoice_id": invID, "reason": "x", "method": "cash"})
	fix25AAssertGeneric500(t, "return-all after DROP InvoiceDetails", code, body)
}

// TestFix25A_SaleCreateDropOffersGeneric500 covers handleAPISaleCreate
// (:1079 CreateInvoice): prepareSaleLines swallows any product-load
// error into the Arabic "المنتج غير موجود" (domain_sales.go:206-209),
// so dropping Products can never leak raw here; the raw-capable leg is
// EvaluateOffers, reached with a real product + open shift after
// dropping Offers.
func TestFix25A_SaleCreateDropOffersGeneric500(t *testing.T) {
	e, _ := fix25AEnv(t)
	mgr := e.managerClient()
	pid := e.createProduct(mgr, "A25-CREATE", "create", "10.00", "10")
	e.openShift(mgr, 100000)
	fix25ADrop(t, e, "Offers")
	code, body := e.postJSON(mgr, "/api/sales/create", map[string]any{
		"sale_type": "retail",
		"lines":     []map[string]any{{"product_id": pid, "qty": "1"}},
		"paid":      1000})
	fix25AAssertGeneric500(t, "sale-create after DROP Offers", code, body)
}

// TestFix25A_CSVExportsDropTableGeneric500 covers the three CSV
// endpoints whose error branch answers apiErr JSON: returns analysis
// CSV, expenses export and the shift-monitor export must answer 500
// generic, not 400 raw, when their table is dropped.
func TestFix25A_CSVExportsDropTableGeneric500(t *testing.T) {
	cases := []struct {
		name  string
		table string
		path  string
	}{
		{"returns-analysis-csv", "Returns", "/returns/analysis?format=csv"},
		{"expenses-export", "Expenses", "/expenses/export"},
		{"shift-monitor-export", "Shifts", "/shifts/monitor/export"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, admin := fix25AEnv(t)
			fix25ADrop(t, e, tc.table)
			code, bodyStr := e.get(admin, tc.path)
			if code != http.StatusInternalServerError {
				t.Fatalf("%s: status = %d, want 500 (body %.200q)", tc.name, code, bodyStr)
			}
			if !strings.Contains(bodyStr, fix25AGeneric500) {
				t.Fatalf("%s: body lacks generic message: %.200q", tc.name, bodyStr)
			}
			fix25AAssertNoRaw(t, tc.name, bodyStr)
		})
	}
}

// TestFix25A_AddStockDropProductsGenericHTML covers the HTML site
// handlers_sales.go:571: posting add-stock with the Products table
// dropped must answer 400 with the generic message page, never the
// raw driver string in the HTML.
func TestFix25A_AddStockDropProductsGenericHTML(t *testing.T) {
	e, admin := fix25AEnv(t)
	fix25ADrop(t, e, "Products")
	code, bodyStr := e.postForm(admin, "/products/1/add-stock", url.Values{"quantity": {"1"}})
	if code != http.StatusBadRequest {
		t.Fatalf("add-stock after DROP Products: status = %d, want 400 (body %.300q)", code, bodyStr)
	}
	if !strings.Contains(bodyStr, "تعذّر إضافة المخزون") {
		t.Fatalf("add-stock after DROP Products: body lacks generic message: %.300q", bodyStr)
	}
	fix25AAssertNoRaw(t, "add-stock after DROP Products", bodyStr)
}

// TestFix25A_Guards pins the semantics the fix must NOT flatten:
// genuine missing entities stay 404 with their Arabic sentinel, a
// genuine validation error stays 400 with its safe text, an overflow
// refusal stays 400 Arabic, and a cross-user denial stays 403.
func TestFix25A_Guards(t *testing.T) {
	t.Run("close-no-open-shift-400-arabic", func(t *testing.T) {
		e, _ := fix25AEnv(t)
		cash := e.cashierClient()
		code, body := e.postJSON(cash, "/api/shifts/close", map[string]any{})
		if code != http.StatusBadRequest {
			t.Fatalf("close with no open shift: status = %d, want 400 (body %v)", code, body)
		}
		errMsg, _ := body["error"].(string)
		if errMsg != "لا يوجد شيفت مفتوح لإغلاقه" {
			t.Fatalf("close with no open shift: error = %q, want لا يوجد شيفت مفتوح لإغلاقه", errMsg)
		}
	})
	t.Run("missing-product-404", func(t *testing.T) {
		e, admin := fix25AEnv(t)
		code, body := e.getJSON(admin, "/api/products/999999")
		if code != http.StatusNotFound {
			t.Fatalf("missing product: status = %d, want 404 (body %v)", code, body)
		}
		if errMsg, _ := body["error"].(string); errMsg != "المنتج غير موجود" {
			t.Fatalf("missing product: error = %q, want المنتج غير موجود", errMsg)
		}
	})
	t.Run("missing-sale-404", func(t *testing.T) {
		e, admin := fix25AEnv(t)
		code, body := e.getJSON(admin, "/api/sales/999999")
		if code != http.StatusNotFound {
			t.Fatalf("missing sale: status = %d, want 404 (body %v)", code, body)
		}
		if errMsg, _ := body["error"].(string); errMsg != "الفاتورة غير موجودة" {
			t.Fatalf("missing sale: error = %q, want الفاتورة غير موجودة", errMsg)
		}
	})
	t.Run("missing-barcode-404", func(t *testing.T) {
		e, admin := fix25AEnv(t)
		code, body := e.getJSON(admin, "/api/products/barcode/NOPE-999999")
		if code != http.StatusNotFound {
			t.Fatalf("missing barcode: status = %d, want 404 (body %v)", code, body)
		}
		errMsg, _ := body["error"].(string)
		if !strings.Contains(errMsg, "لم يتم العثور على المنتج") {
			t.Fatalf("missing barcode: error = %q, want لم يتم العثور على المنتج", errMsg)
		}
	})
	t.Run("missing-stocktake-404", func(t *testing.T) {
		e, admin := fix25AEnv(t)
		code, body := e.getJSON(admin, "/api/stocktake/999999")
		if code != http.StatusNotFound {
			t.Fatalf("missing stocktake: status = %d, want 404 (body %v)", code, body)
		}
		if errMsg, _ := body["error"].(string); errMsg != "جلسة الجرد غير موجودة" {
			t.Fatalf("missing stocktake: error = %q, want جلسة الجرد غير موجودة", errMsg)
		}
	})
	t.Run("missing-expense-delete-404", func(t *testing.T) {
		e, admin := fix25AEnv(t)
		code, body := e.deleteJSON(admin, "/api/expenses/999999")
		if code != http.StatusNotFound {
			t.Fatalf("missing expense delete: status = %d, want 404 (body %v)", code, body)
		}
		if errMsg, _ := body["error"].(string); errMsg != "المصروف غير موجود" {
			t.Fatalf("missing expense delete: error = %q, want المصروف غير موجود", errMsg)
		}
	})
	t.Run("missing-invoice-return-404", func(t *testing.T) {
		e, _ := fix25AEnv(t)
		mgr := e.managerClient()
		code, body := e.postJSON(mgr, "/api/returns", map[string]any{
			"invoice_id": 999999, "reason": "x", "method": "cash",
			"lines": []map[string]any{{"product_id": 1, "qty": "1"}}})
		if code != http.StatusNotFound {
			t.Fatalf("missing invoice return: status = %d, want 404 (body %v)", code, body)
		}
		if errMsg, _ := body["error"].(string); errMsg != "الفاتورة غير موجودة" {
			t.Fatalf("missing invoice return: error = %q, want الفاتورة غير موجودة", errMsg)
		}
	})
	t.Run("missing-invoice-cancel-404", func(t *testing.T) {
		e, admin := fix25AEnv(t)
		code, body := e.postJSON(admin, "/api/sales/999999/cancel", map[string]any{"reason": "x"})
		if code != http.StatusNotFound {
			t.Fatalf("missing invoice cancel: status = %d, want 404 (body %v)", code, body)
		}
		if errMsg, _ := body["error"].(string); errMsg != "الفاتورة غير موجودة" {
			t.Fatalf("missing invoice cancel: error = %q, want الفاتورة غير موجودة", errMsg)
		}
	})
	t.Run("cross-user-expense-delete-403", func(t *testing.T) {
		e, admin := fix25AEnv(t)
		e.openShift(admin, 100000)
		code, body := e.postJSON(admin, "/api/expenses", map[string]any{
			"description": "admin exp", "amount_cents": 1000, "category": "عام"})
		if code != http.StatusOK {
			t.Fatalf("create admin expense: status = %d, want 200 (body %v)", code, body)
		}
		id, _ := body["expense_id"].(float64)
		cash := e.cashierClient()
		code, body = e.deleteJSON(cash, "/api/expenses/"+itoa(int64(id)))
		if code != http.StatusForbidden {
			t.Fatalf("cross-user expense delete: status = %d, want 403 (body %v)", code, body)
		}
	})
	t.Run("close-overflow-400-arabic", func(t *testing.T) {
		e, _ := fix25AEnv(t)
		mgr := e.managerClient()
		shiftID := e.openShift(mgr, 100000)
		for i := 0; i < 2; i++ {
			if _, err := e.store.DB.Exec(`INSERT INTO Expenses(Amount,Description,Category,ExpenseDate,ShiftID,UserID)
				VALUES(?, 'poison', 'عام', ?, ?, 1)`, fix25ABig, store.Today(), shiftID); err != nil {
				t.Fatalf("insert poison expense: %v", err)
			}
		}
		code, body := e.postJSON(mgr, "/api/shifts/close", map[string]any{})
		if code != http.StatusBadRequest {
			t.Fatalf("poisoned close: status = %d, want 400 (body %v)", code, body)
		}
		errMsg, _ := body["error"].(string)
		if errMsg != fix25AOverflow {
			t.Fatalf("poisoned close: error = %q, want %q", errMsg, fix25AOverflow)
		}
		fix25AAssertNoRaw(t, "poisoned close", errMsg)
	})
	t.Run("validation-bad-id-400", func(t *testing.T) {
		e, admin := fix25AEnv(t)
		code, body := e.getJSON(admin, "/api/products/abc")
		if code != http.StatusBadRequest {
			t.Fatalf("bad product id: status = %d, want 400 (body %v)", code, body)
		}
		// pathID returns the stdlib strconv text (parse-sourced, not
		// a driver string — left alone); assert 400 + no SQL leak.
		fix25AAssertNoRaw(t, "bad product id", body["error"].(string))
	})
	t.Run("validation-bad-expense-amount-400", func(t *testing.T) {
		e, admin := fix25AEnv(t)
		code, body := e.postJSON(admin, "/api/expenses", map[string]any{
			"description": "x", "amount_cents": "not-a-number", "category": "عام"})
		if code != http.StatusBadRequest {
			t.Fatalf("bad expense amount: status = %d, want 400 (body %v)", code, body)
		}
		fix25AAssertNoRaw(t, "bad expense amount", body["error"].(string))
	})
}
