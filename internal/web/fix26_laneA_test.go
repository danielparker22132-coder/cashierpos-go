// Package web_test — FIX-26 lane A (R26-A-F1, shifts + sales HTML):
// no HTML error page in handlers_shifts.go / handlers_sales.go may echo
// a raw driver/DB error to the client. Every renderErr/renderErrStatus
// call site (via safeHTMLErr in renderErrStatus) and every direct
// Page{Error: err.Error()} site now renders the generic Arabic message
// "حدث خطأ داخلي، حاول مرة أخرى" for a driver error, keeps a safe Arabic
// business/validation error verbatim, and keeps the 200 status (the
// accepted "HTML form refusals render 200" residual stands for status;
// this fix is about content only).
package web_test

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// fix26AAssertHTMLNoRaw fails unless the HTML page answered 200 with no
// raw driver string anywhere and an Arabic alert (the generic message,
// or a safe Arabic business message such as "الباركود غير موجود").
func fix26AAssertHTMLNoRaw(t *testing.T, what string, code int, body string) {
	t.Helper()
	if code != http.StatusOK {
		t.Fatalf("%s: status = %d, want 200 (body %.300q)", what, code, body)
	}
	fix25AAssertNoRaw(t, what, body)
	m := regexp.MustCompile(`(?s)<div class="alert err"[^>]*>(.*?)</div>`).FindStringSubmatch(body)
	if m == nil || !strings.ContainsAny(m[1], "ابتثجحخدرزسشصضطظعغفقكلمنهويىة") {
		t.Fatalf("%s: body has no Arabic alert div: %.300q", what, body)
	}
}

// fix26ADrop drops one table out from under the running server on a
// dedicated connection with foreign_keys OFF.
func fix26ADrop(t *testing.T, e *e2eEnv, table string) {
	t.Helper()
	ctx := context.Background()
	conn, err := e.store.DB.Conn(ctx)
	if err != nil {
		t.Fatalf("DB.Conn: %v", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatalf("PRAGMA foreign_keys=OFF: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "DROP TABLE "+table); err != nil {
		t.Fatalf("DROP TABLE %s: %v", table, err)
	}
}

// fix26AStocktake creates a stocktake session through the HTML form and
// returns its id.
func fix26AStocktake(t *testing.T, e *e2eEnv, admin *http.Client) int64 {
	t.Helper()
	code, _ := e.postForm(admin, "/stocktake/new", url.Values{"notes": {"x"}})
	if code != http.StatusSeeOther {
		t.Fatalf("create stocktake: status = %d, want 303", code)
	}
	return e.queryInt("SELECT StocktakeID FROM Stocktakes ORDER BY StocktakeID DESC LIMIT 1")
}

// TestFix26A_HTMLDropTableMatrix covers the 12 live-proven re-review 26
// shapes (H1-H13, H12 already clean) plus every remaining same-pattern
// HTML site in the two files: each case drops the table its handler
// depends on, hits the HTML handler, and the page must answer 200 with
// the generic (or safe Arabic) message — never the raw
// "SQL logic error: no such table: X (1)" the pre-fix page echoed.
func TestFix26A_HTMLDropTableMatrix(t *testing.T) {
	cases := []struct {
		name  string
		table string
		setup func(e *e2eEnv, admin *http.Client, bag map[string]int64)
		call  func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string)
	}{
		{"H1-product-detail", "Products", nil, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			return e.get(admin, "/products/1")
		}},
		{"H2-adjust-stock", "Products", nil, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			return e.postForm(admin, "/products/1/adjust-stock", url.Values{"quantity": {"1"}, "reason": {"x"}})
		}},
		{"H3-stocktake-list", "Stocktakes", nil, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			return e.get(admin, "/stocktake")
		}},
		{"H4-shift-detail", "Shifts", nil, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			return e.get(admin, "/shifts/1")
		}},
		{"H5-shift-close-submit", "Expenses", func(e *e2eEnv, admin *http.Client, bag map[string]int64) {
			e.openShift(e.managerClient(), 100000)
		}, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			return e.postForm(e.managerClient(), "/shifts/close", url.Values{})
		}},
		{"H6-sale-detail", "Invoices", nil, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			return e.get(admin, "/sales/1")
		}},
		{"H7-products-list", "Products", nil, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			return e.get(admin, "/products")
		}},
		{"H8-expense-delete-submit", "Expenses", func(e *e2eEnv, admin *http.Client, bag map[string]int64) {
			e.openShift(admin, 100000)
			code, body := e.postJSON(admin, "/api/expenses", map[string]any{
				"description": "h", "amount_cents": 1000, "category": "عام"})
			if code != http.StatusOK {
				t.Fatalf("setup expense: status = %d (body %v)", code, body)
			}
		}, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			return e.postForm(admin, "/expenses/1/delete", url.Values{})
		}},
		{"H9-promos-list", "Offers", nil, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			return e.get(admin, "/promotions")
		}},
		{"H10-stocktake-detail", "Stocktakes", nil, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			return e.get(admin, "/stocktake/1")
		}},
		{"H11-cashcount-approvals", "CashCounts", nil, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			return e.get(admin, "/cashcount-approvals")
		}},
		{"H12-expenses-page", "Expenses", nil, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			return e.get(admin, "/expenses")
		}},
		{"H13-expense-create-submit", "Expenses", func(e *e2eEnv, admin *http.Client, bag map[string]int64) {
			e.openShift(admin, 100000)
		}, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			return e.postForm(admin, "/expenses", url.Values{
				"description": {"x"}, "amount": {"10.00"}, "category": {"عام"}})
		}},
		{"X1-sale-post-create", "Offers", func(e *e2eEnv, admin *http.Client, bag map[string]int64) {
			mgr := e.managerClient()
			e.createProduct(mgr, "A26X-P", "xp", "10.00", "10")
			e.openShift(mgr, 100000)
		}, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			mgr := e.managerClient()
			pid := e.queryInt("SELECT ProductID FROM Products WHERE Barcode=?", "A26X-P")
			return e.postForm(mgr, "/sales", url.Values{
				"sale_type":       {"retail"},
				"paid":            {"10.00"},
				"line_product_id": {itoa(pid)},
				"line_qty":        {"1"}})
		}},
		{"X2-sale-print", "Invoices", nil, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			return e.get(admin, "/sales/1/print")
		}},
		{"X3-product-form-get", "Products", nil, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			return e.get(admin, "/products/1/edit")
		}},
		{"X4-product-form-post", "Products", nil, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			return e.postForm(admin, "/products/new", url.Values{
				"name": {"n"}, "barcode": {"A26X-NEW"}, "buy_price": {"5.00"},
				"bulk_price": {"7.00"}, "wholesale_price": {"8.00"},
				"retail_price": {"10.00"}, "quantity": {"10"},
				"min_quantity": {"1"}, "unit": {"قطعة"}})
		}},
		{"X5-promo-form-get", "Offers", nil, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			return e.get(admin, "/promotions/1/edit")
		}},
		{"X6-promo-toggle", "Offers", nil, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			return e.postForm(admin, "/promotions/1/toggle", url.Values{})
		}},
		{"X7-promo-delete", "Offers", nil, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			return e.postForm(admin, "/promotions/1/delete", url.Values{})
		}},
		{"X8-stocktake-create", "Stocktakes", nil, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			return e.postForm(admin, "/stocktake/new", url.Values{"notes": {"x"}})
		}},
		{"X9-stocktake-scan", "Products", func(e *e2eEnv, admin *http.Client, bag map[string]int64) {
			e.createProduct(admin, "A26X-ST", "stprod", "10.00", "10")
			bag["sid"] = fix26AStocktake(t, e, admin)
		}, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			sid := bag["sid"]
			return e.postForm(admin, "/stocktake/"+itoa(sid)+"/scan",
				url.Values{"barcode": {"A26X-ST"}, "qty": {"1"}})
		}},
		{"X10-stocktake-set", "Stocktakes", func(e *e2eEnv, admin *http.Client, bag map[string]int64) {
			bag["sid"] = fix26AStocktake(t, e, admin)
		}, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			sid := bag["sid"]
			return e.postForm(admin, "/stocktake/"+itoa(sid)+"/set",
				url.Values{"product_id": {"1"}, "qty": {"1"}})
		}},
		{"X11-stocktake-close", "Stocktakes", func(e *e2eEnv, admin *http.Client, bag map[string]int64) {
			bag["sid"] = fix26AStocktake(t, e, admin)
		}, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			sid := bag["sid"]
			return e.postForm(admin, "/stocktake/"+itoa(sid)+"/close", url.Values{})
		}},
		{"X12-stocktake-cancel", "Stocktakes", func(e *e2eEnv, admin *http.Client, bag map[string]int64) {
			bag["sid"] = fix26AStocktake(t, e, admin)
		}, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			sid := bag["sid"]
			return e.postForm(admin, "/stocktake/"+itoa(sid)+"/cancel", url.Values{})
		}},
		{"X13-stocktake-approve", "Stocktakes", func(e *e2eEnv, admin *http.Client, bag map[string]int64) {
			bag["sid"] = fix26AStocktake(t, e, admin)
		}, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			sid := bag["sid"]
			return e.postForm(admin, "/stocktake/"+itoa(sid)+"/approve",
				url.Values{"reason": {"x"}})
		}},
		{"X14-shifts-page", "Shifts", nil, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			return e.get(admin, "/shifts")
		}},
		{"X15-shift-open-submit", "Shifts", nil, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			return e.postForm(admin, "/shifts/open", url.Values{"opening": {"100.00"}})
		}},
		{"X16-cashbox-in", "Shifts", func(e *e2eEnv, admin *http.Client, bag map[string]int64) {
			e.openShift(admin, 100000)
		}, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			return e.postForm(admin, "/cashbox/in", url.Values{
				"amount": {"10.00"}, "reason": {"x"}, "shift_id": {"1"}})
		}},
		{"X17-cashcount-submit", "Shifts", func(e *e2eEnv, admin *http.Client, bag map[string]int64) {
			e.openShift(admin, 100000)
		}, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			return e.postForm(admin, "/cashbox/count", url.Values{
				"shift_id": {"1"}, "amount": {"100.00"}})
		}},
		{"X18-cashcount-review", "CashCounts", func(e *e2eEnv, admin *http.Client, bag map[string]int64) {
			sh := e.openShift(admin, 100000)
			code, body := e.postJSON(admin, "/api/cashbox/count",
				map[string]any{"shift_id": sh, "amount_cents": 100000})
			if code != http.StatusOK {
				t.Fatalf("setup cashcount: status = %d (body %v)", code, body)
			}
			bag["cid"] = e.queryInt("SELECT CountID FROM CashCounts ORDER BY CountID DESC LIMIT 1")
		}, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			cid := bag["cid"]
			return e.postForm(admin, "/cashbox/count/"+itoa(cid)+"/review",
				url.Values{"approve": {"1"}})
		}},
		{"X19-return-submit", "Invoices", func(e *e2eEnv, admin *http.Client, bag map[string]int64) {
			mgr := e.managerClient()
			pid := e.createProduct(mgr, "A26X-SALE", "xprod", "10.00", "10")
			e.openShift(mgr, 100000)
			inv, _ := e.cashSale(mgr, pid, "1", 1000)
			bag["inv"], bag["pid"] = inv, pid
		}, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			mgr := e.managerClient()
			inv, pid := bag["inv"], bag["pid"]
			return e.postForm(mgr, "/returns", url.Values{
				"invoice_id": {itoa(inv)}, "reason": {"x"}, "method": {"cash"},
				"product_id": {itoa(pid)}, "qty": {"1"}})
		}},
		{"X20-return-all", "InvoiceDetails", func(e *e2eEnv, admin *http.Client, bag map[string]int64) {
			mgr := e.managerClient()
			pid := e.createProduct(mgr, "A26X-SALE", "xprod", "10.00", "10")
			e.openShift(mgr, 100000)
			inv, _ := e.cashSale(mgr, pid, "1", 1000)
			bag["inv"] = inv
		}, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			mgr := e.managerClient()
			inv := bag["inv"]
			return e.postForm(mgr, "/returns/all", url.Values{
				"invoice_id": {itoa(inv)}, "reason": {"x"}, "method": {"cash"}})
		}},
		{"X21-returns-analysis", "Returns", nil, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			return e.get(admin, "/returns/analysis")
		}},
		{"X22-expense-edit", "Expenses", func(e *e2eEnv, admin *http.Client, bag map[string]int64) {
			e.openShift(admin, 100000)
			code, body := e.postJSON(admin, "/api/expenses", map[string]any{
				"description": "x", "amount_cents": 1000, "category": "عام"})
			if code != http.StatusOK {
				t.Fatalf("setup expense: status = %d (body %v)", code, body)
			}
			id, _ := body["expense_id"].(float64)
			bag["id"] = int64(id)
		}, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			id := bag["id"]
			return e.postForm(admin, "/expenses/"+itoa(id)+"/edit", url.Values{
				"description": {"y"}, "amount": {"10.00"}, "category": {"عام"}})
		}},
		{"X23-dailyclose", "Expenses", nil, func(e *e2eEnv, admin *http.Client, bag map[string]int64) (int, string) {
			return e.get(admin, "/dailyclose")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, admin := fix25AEnv(t)
			bag := map[string]int64{}
			if tc.setup != nil {
				tc.setup(e, admin, bag)
			}
			fix26ADrop(t, e, tc.table)
			code, body := tc.call(e, admin, bag)
			fix26AAssertHTMLNoRaw(t, tc.name+" after DROP "+tc.table, code, body)
		})
	}
}

// TestFix26A_SafeArabicKept pins the semantics the gate must NOT
// flatten: a genuine Arabic business refusal on an HTML form still
// renders its own text (200, per the accepted residual) — here the
// shift-close submit with no open shift keeps "لا يوجد شيفت مفتوح
// لإغلاقه" instead of being replaced by the generic message.
func TestFix26A_SafeArabicKept(t *testing.T) {
	e, _ := fix25AEnv(t)
	mgr := e.managerClient()
	code, body := e.postForm(mgr, "/shifts/close", url.Values{})
	if code != http.StatusOK {
		t.Fatalf("close with no open shift (HTML): status = %d, want 200 (body %.300q)", code, body)
	}
	if !strings.Contains(body, "لا يوجد شيفت مفتوح لإغلاقه") {
		t.Fatalf("close with no open shift (HTML): body lacks the safe Arabic refusal: %.300q", body)
	}
	fix25AAssertNoRaw(t, "close with no open shift (HTML)", body)
}
