// Package web_test — FIX-16 (R16-M1): the single-purchase total was
// still served ungated BEYOND the settlement surface — vendor-level
// CurrentBalance on /vendors, /vendors/{id}, /vendors/{id}/ledger and
// GET /api/vendors, plus the ledger payments table (BalanceBefore IS
// the invoice total for a single-purchase vendor, and it persists
// after full settlement when the pay form has disappeared) — to every
// role including warehouse, which cannot settle (POST .../pay → 403).
//
// Parent decision implemented here: vendor money figures are visible
// only to roles with view_profit OR pay_vendors, EXCEPT the ledger
// payments-table money columns (Amount/BalanceBefore/After), which
// require view_profit alone. Warehouse (neither) sees no vendor money
// at all; cashier (pay_vendors) keeps the settlement surface
// (dropdown + prefill + CurrentBalance = the amount owed) but the
// ledger payments money is hidden from it too.
//
// The accepted residual stays narrow and pinned: a SETTLING cashier
// can still derive the unit price from the amount owed
// (dropdown/prefill) + the Qty on the purchase detail page — inherent
// to letting cashiers settle. After this fix no OTHER surface may
// expose a single-purchase total to a non-profit role.
package web_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// setupFix16 builds: one vendor with ONE single-line purchase
// (12.34×3 = 37.02, unpaid, due today). Vendor balance is 37.02.
func setupFix16(t *testing.T) *fix15M1Fixture {
	t.Helper()
	e := readyEnv(t, false)
	admin := e.adminClient()
	pid1 := e.createProduct(admin, "F16-1", "سكر", "15.00", "100")
	code, data := e.postJSON(admin, "/api/vendors", map[string]any{"name": "مورد ف16"})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("create vendor: want 200 ok, got %d %v", code, data)
	}
	vid, _ := data["vendor_id"].(float64)
	due := time.Now().Format("2006-01-02")
	code, data = e.postJSON(admin, "/api/purchases", map[string]any{
		"vendor_id": int64(vid),
		"lines":     []map[string]any{{"product_id": pid1, "qty": "3", "price": "12.34"}},
		"paid": "0", "method": "Visa", "due_date": due,
	})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("create purchase: want 200 ok, got %d %v", code, data)
	}
	pid, _ := data["purchase_id"].(float64)
	inv, _ := data["invoice_number"].(string)
	return &fix15M1Fixture{e: e, vendorID: int64(vid), singleInv: inv, dueDate: due, purchaseID: int64(pid)}
}

// fix15M1Fixture gains a purchaseID field for fix-16 (same struct,
// same package — the field is unused by the fix-15 tests).

// TestFix16_R16M1_WarehouseNoVendorMoney is the review's exact repro:
// as warehouse, GET /vendors → read 37.02 → GET /purchases/1 → read
// Qty 3 → 37.02 ÷ 3 = 12.34. After the fix the first step must fail:
// no 37.02 on any vendor surface, and no pay form (dead UI, POST 403).
func TestFix16_R16M1_WarehouseNoVendorMoney(t *testing.T) {
	f := setupFix16(t)
	e := f.e
	wh := e.warehouseClient()
	admin := e.adminClient()
	detailPath := fmt.Sprintf("/vendors/%d", f.vendorID)
	ledgerPath := fmt.Sprintf("/vendors/%d/ledger", f.vendorID)

	// --- vendors list: no balance column, no total anywhere ---
	code, html := e.get(wh, "/vendors")
	if code != http.StatusOK {
		t.Fatalf("warehouse GET /vendors: want 200, got %d", code)
	}
	for _, s := range []string{"37.02", "الرصيد الحالي"} {
		if strings.Contains(html, s) {
			t.Fatalf("warehouse: vendors list must not contain %q", s)
		}
	}

	// --- vendor detail: no balance, no pay form, no amounts ---
	code, html = e.get(wh, detailPath)
	if code != http.StatusOK {
		t.Fatalf("warehouse GET %s: want 200, got %d", detailPath, code)
	}
	for _, s := range []string{"37.02", "الرصيد الحالي", "سداد مورد", `/pay"`} {
		if strings.Contains(html, s) {
			t.Fatalf("warehouse: vendor detail must not contain %q", s)
		}
	}

	// --- vendor ledger: no balance, no payments money headers ---
	code, html = e.get(wh, ledgerPath)
	if code != http.StatusOK {
		t.Fatalf("warehouse GET %s: want 200, got %d", ledgerPath, code)
	}
	for _, s := range []string{"37.02", "الرصيد الحالي", "المبلغ", "الرصيد قبل", "الرصيد بعد"} {
		if strings.Contains(html, s) {
			t.Fatalf("warehouse: vendor ledger must not contain %q", s)
		}
	}

	// --- GET /api/vendors: balance zeroed for warehouse, intact for admin ---
	code, data := e.getJSON(wh, "/api/vendors")
	if code != http.StatusOK {
		t.Fatalf("warehouse GET /api/vendors: want 200, got %d", code)
	}
	vendors, _ := data["vendors"].([]any)
	if len(vendors) != 1 {
		t.Fatalf("warehouse /api/vendors: want 1 vendor, got %d", len(vendors))
	}
	v, _ := vendors[0].(map[string]any)
	if v["CurrentBalance"] != float64(0) {
		t.Fatalf("warehouse /api/vendors CurrentBalance = %v, want 0", v["CurrentBalance"])
	}
	if v["Name"] != "مورد ف16" {
		t.Fatalf("warehouse /api/vendors Name = %v, want مورد ف16 (no breakage)", v["Name"])
	}
	code, data = e.getJSON(admin, "/api/vendors")
	if code != http.StatusOK {
		t.Fatalf("admin GET /api/vendors: want 200, got %d", code)
	}
	vendors, _ = data["vendors"].([]any)
	v, _ = vendors[0].(map[string]any)
	if v["CurrentBalance"] != float64(3702) {
		t.Fatalf("admin /api/vendors CurrentBalance = %v, want 3702", v["CurrentBalance"])
	}

	// --- warehouse cannot settle at all (POST is 403) ---
	code, _ = e.postForm(wh, fmt.Sprintf("/vendors/%d/pay", f.vendorID),
		url.Values{"amount": {"37.02"}, "method": {"Visa"}})
	if code != http.StatusForbidden {
		t.Fatalf("warehouse POST pay: want 403, got %d", code)
	}

	// --- the other half of the derivation (fix-14): the purchase
	// detail page still shows Qty 3 but no total for warehouse ---
	code, html = e.get(wh, fmt.Sprintf("/purchases/%d", f.purchaseID))
	if code != http.StatusOK {
		t.Fatalf("warehouse GET purchase detail: want 200, got %d", code)
	}
	for _, s := range []string{"37.02", "12.34", "سعر الشراء", "الإجمالي"} {
		if strings.Contains(html, s) {
			t.Fatalf("warehouse: purchase detail must not contain %q", s)
		}
	}
}

// TestFix16_R16M1_CashierSettlementAndLedgerHidden: the cashier keeps
// the settlement surface (balance, dropdown, prefill) and can settle,
// but the ledger payments money is hidden from it — including after
// partial and full settlement, when the payments table was the sole
// surviving source of the original total. Admin still sees it all.
func TestFix16_R16M1_CashierSettlementAndLedgerHidden(t *testing.T) {
	f := setupFix16(t)
	e := f.e
	cash := e.cashierClient()
	wh := e.warehouseClient()
	admin := e.adminClient()
	detailPath := fmt.Sprintf("/vendors/%d", f.vendorID)
	ledgerPath := fmt.Sprintf("/vendors/%d/ledger", f.vendorID)
	payPath := fmt.Sprintf("/vendors/%d/pay", f.vendorID)

	// --- settlement surface kept for the cashier ---
	code, html := e.get(cash, detailPath)
	if code != http.StatusOK {
		t.Fatalf("cashier GET %s: want 200, got %d", detailPath, code)
	}
	if !strings.Contains(html, "سداد مورد") || !strings.Contains(html, "37.02") {
		t.Fatalf("cashier: vendor detail must still show the pay form and the 37.02 amount owed")
	}
	if !strings.Contains(html, `value="37.02"`) {
		t.Fatalf("cashier: amount prefill must still be 37.02")
	}
	code, html = e.get(cash, "/vendors")
	if !strings.Contains(html, "37.02") {
		t.Fatalf("cashier: vendors list must still show the 37.02 balance (amount owed)")
	}
	// Ledger header shows the balance (amount owed) but the payments
	// table money headers are hidden from the cashier even with no
	// payments yet.
	code, html = e.get(cash, ledgerPath)
	if !strings.Contains(html, "37.02") {
		t.Fatalf("cashier: ledger header must still show the 37.02 balance")
	}
	for _, s := range []string{"المبلغ", "الرصيد قبل", "الرصيد بعد"} {
		if strings.Contains(html, s) {
			t.Fatalf("cashier: ledger payments header %q must be hidden", s)
		}
	}

	// --- partial Visa payment 11.11 as cashier ---
	code, _ = e.postForm(cash, payPath, url.Values{"amount": {"11.11"}, "method": {"Visa"}})
	if code != http.StatusSeeOther {
		t.Fatalf("cashier partial pay 11.11: want 303, got %d", code)
	}
	// Admin sees the payment row with BalanceBefore 37.02 …
	code, html = e.get(admin, ledgerPath)
	payments := section(t, html, "<h2>المدفوعات</h2>", "")
	for _, s := range []string{"37.02", "11.11", "25.91"} {
		if !strings.Contains(payments, s) {
			t.Fatalf("admin: ledger payments must still show %q after partial payment", s)
		}
	}
	// … cashier and warehouse see none of the money in that table,
	// while the cashier's settlement surface still shows 25.91 owed.
	for _, tc := range []struct {
		name string
		cl   *http.Client
	}{
		{"cashier", cash},
		{"warehouse", wh},
	} {
		code, html = e.get(tc.cl, ledgerPath)
		if code != http.StatusOK {
			t.Fatalf("%s GET %s: want 200, got %d", tc.name, ledgerPath, code)
		}
		payments = section(t, html, "<h2>المدفوعات</h2>", "")
		for _, s := range []string{"37.02", "11.11", "25.91", "المبلغ", "الرصيد قبل", "الرصيد بعد"} {
			if strings.Contains(payments, s) {
				t.Fatalf("%s: ledger payments must not contain %q after partial payment", tc.name, s)
			}
		}
	}
	code, html = e.get(cash, detailPath)
	if !strings.Contains(html, "25.91") {
		t.Fatalf("cashier: detail dropdown must still show the 25.91 remaining (settlement surface)")
	}

	// --- full settlement of the remaining 25.91 as cashier ---
	code, _ = e.postForm(cash, payPath, url.Values{"amount": {"25.91"}, "method": {"Visa"}})
	if code != http.StatusSeeOther {
		t.Fatalf("cashier final pay 25.91: want 303, got %d", code)
	}
	// After full settlement the payments table is the sole surviving
	// source of the original total for admin — and hidden from the
	// cashier and the warehouse.
	code, html = e.get(admin, ledgerPath)
	payments = section(t, html, "<h2>المدفوعات</h2>", "")
	if !strings.Contains(payments, "37.02") {
		t.Fatalf("admin: ledger payments must still show BalanceBefore 37.02 after full settlement")
	}
	for _, tc := range []struct {
		name string
		cl   *http.Client
	}{
		{"cashier", cash},
		{"warehouse", wh},
	} {
		code, html = e.get(tc.cl, ledgerPath)
		if strings.Contains(html, "37.02") {
			t.Fatalf("%s: ledger must not contain 37.02 anywhere after full settlement", tc.name)
		}
		code, html = e.get(tc.cl, detailPath)
		if strings.Contains(html, "37.02") {
			t.Fatalf("%s: vendor detail must not contain 37.02 anywhere after full settlement", tc.name)
		}
	}
}

// TestFix16_R16M1_ApiVendorsBalanceByRole: GET /api/vendors serves the
// balance to roles that may view profit or settle, and zeroes it for
// warehouse (neither). CreditLimit is not a purchase total and stays.
func TestFix16_R16M1_ApiVendorsBalanceByRole(t *testing.T) {
	f := setupFix16(t)
	e := f.e
	for _, tc := range []struct {
		name string
		cl   *http.Client
		want float64
	}{
		{"warehouse", e.warehouseClient(), 0},
		{"cashier", e.cashierClient(), 3702},
		{"supervisor", e.supClient(), 3702},
		{"admin", e.adminClient(), 3702},
	} {
		code, data := e.getJSON(tc.cl, "/api/vendors")
		if code != http.StatusOK {
			t.Fatalf("%s GET /api/vendors: want 200, got %d", tc.name, code)
		}
		vendors, _ := data["vendors"].([]any)
		if len(vendors) != 1 {
			t.Fatalf("%s /api/vendors: want 1 vendor, got %d", tc.name, len(vendors))
		}
		v, _ := vendors[0].(map[string]any)
		if v["CurrentBalance"] != tc.want {
			t.Fatalf("%s /api/vendors CurrentBalance = %v, want %v", tc.name, v["CurrentBalance"], tc.want)
		}
	}
}
