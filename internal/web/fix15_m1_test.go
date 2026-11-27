// Package web_test — FIX-15 (R15-M1): the single-line unit buy price was
// still derivable via the vendor pages. GET /vendors/{id},
// /vendors/{id}/ledger and /vendors rendered per-purchase Total/Paid/
// Remaining unguarded, so a vendor with ONE single-line purchase
// (12.34×3 unpaid) showed 37.02 on all three pages while the fix-14
// purchase detail page still showed Qty 3 → 37.02 ÷ 3 = 12.34.
//
// The fix gates the per-purchase money columns (and the per-invoice
// amount in the list-page due alerts) on canViewProfit, for ALL rows —
// not single-line-only: the ledger rows carry no line count, and
// settlement does not use these tables (it uses the pay-form dropdown).
//
// FIX-16 R16-M1 (mandated spec change, re-review 16): the vendor-level
// CurrentBalance / header balance is NO LONGER visible to every role —
// it is gated on canViewVendorMoney (view_profit OR pay_vendors), and
// the ledger payments-table money columns on canViewProfit alone.
// Warehouse holds neither permission and sees no vendor money at all.
// The old "kept visible on purpose" assertions in this file were
// updated accordingly; they pin the NEW rule, not a weakening.
// Kept visible to settling roles (parent decision, review option (a)):
// the pay-form dropdown + amount prefill (the settlement surface),
// and the vendor-level balance for roles that may view profit or
// settle (it IS the amount owed).
//
// Residual, pinned explicitly by TestFix15M1_SettlementSurfaceKept
// rather than hidden: a cashier who can settle a single-purchase vendor
// can still derive the unit price from the amount owed in the dropdown /
// prefill + the Qty on the purchase detail page. That is inherent to
// letting cashiers settle.
package web_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// fix15M1Setup builds: one vendor with a single-line purchase
// (12.34×3 = 37.02, unpaid, due today so it raises a due alert) and a
// multi-line purchase (12.34×3 + 7.77×2 = 52.56, unpaid, due today).
// Vendor balance is 37.02 + 52.56 = 89.58.
type fix15M1Fixture struct {
	e          *e2eEnv
	vendorID   int64
	singleInv  string
	multiInv   string
	dueDate    string
	purchaseID int64 // FIX-16: the single-line purchase id (0 for the fix-15 fixture)
}

func setupFix15M1(t *testing.T) *fix15M1Fixture {
	t.Helper()
	e := readyEnv(t, false)
	admin := e.adminClient()
	pid1 := e.createProduct(admin, "F15-1", "سكر", "15.00", "100")
	pid2 := e.createProduct(admin, "F15-2", "أرز", "12.00", "100")
	code, data := e.postJSON(admin, "/api/vendors", map[string]any{"name": "مورد ف15"})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("create vendor: want 200 ok, got %d %v", code, data)
	}
	vid, _ := data["vendor_id"].(float64)
	due := time.Now().Format("2006-01-02")
	mkPurchase := func(lines []map[string]any) (int64, string) {
		t.Helper()
		code, data := e.postJSON(admin, "/api/purchases", map[string]any{
			"vendor_id": int64(vid), "lines": lines, "paid": "0",
			"method": "Visa", "due_date": due,
		})
		if code != http.StatusOK || data["ok"] != true {
			t.Fatalf("create purchase: want 200 ok, got %d %v", code, data)
		}
		id, _ := data["purchase_id"].(float64)
		inv, _ := data["invoice_number"].(string)
		return int64(id), inv
	}
	_, singleInv := mkPurchase([]map[string]any{{"product_id": pid1, "qty": "3", "price": "12.34"}})
	_, multiInv := mkPurchase([]map[string]any{
		{"product_id": pid1, "qty": "3", "price": "12.34"},
		{"product_id": pid2, "qty": "2", "price": "7.77"},
	})
	return &fix15M1Fixture{e: e, vendorID: int64(vid), singleInv: singleInv, multiInv: multiInv, dueDate: due}
}

// section returns the substring of html between start and end markers
// (end "" means to the end of the string); fatal if start is missing.
func section(t *testing.T, html, start, end string) string {
	t.Helper()
	i := strings.Index(html, start)
	if i < 0 {
		t.Fatalf("page is missing section marker %q", start)
	}
	s := html[i+len(start):]
	if end != "" {
		j := strings.Index(s, end)
		if j < 0 {
			t.Fatalf("page is missing section end marker %q", end)
		}
		s = s[:j]
	}
	return s
}

func TestFix15M1_VendorPagesMoneyGated(t *testing.T) {
	f := setupFix15M1(t)
	e := f.e
	admin := e.adminClient()
	detailPath := fmt.Sprintf("/vendors/%d", f.vendorID)
	ledgerPath := fmt.Sprintf("/vendors/%d/ledger", f.vendorID)

	for _, tc := range []struct {
		name        string
		cl          *http.Client
		want        bool // want the per-purchase money visible (view_profit)
		wantBalance bool // want the vendor-level balance visible (view_profit OR pay_vendors)
	}{
		{"cashier", e.cashierClient(), false, true},
		{"warehouse", e.warehouseClient(), false, false},
		{"admin", admin, true, true},
		{"supervisor", e.supClient(), true, true},
	} {
		// --- vendor detail: purchases table only (after the <h2>) ---
		code, html := e.get(tc.cl, detailPath)
		if code != http.StatusOK {
			t.Fatalf("%s GET %s: want 200, got %d", tc.name, detailPath, code)
		}
		table := section(t, html, "<h2>فواتير الشراء</h2>", "")
		// Both invoice numbers stay visible to every role.
		for _, inv := range []string{f.singleInv, f.multiInv} {
			if !strings.Contains(table, inv) {
				t.Fatalf("%s: detail table must still show invoice %s", tc.name, inv)
			}
		}
		// Single-line AND multi-line per-purchase totals gated the same way.
		for _, s := range []string{"37.02", "52.56", "الإجمالي", "المدفوع", "المتبقي"} {
			if got := strings.Contains(table, s); got != tc.want {
				t.Fatalf("%s: detail purchases table contains %q = %v, want %v", tc.name, s, got, tc.want)
			}
		}

		// --- vendor ledger: purchases table only (between the two <h2>s) ---
		code, html = e.get(tc.cl, ledgerPath)
		if code != http.StatusOK {
			t.Fatalf("%s GET %s: want 200, got %d", tc.name, ledgerPath, code)
		}
		// FIX-16 R16-M1 (mandated spec change): the vendor-level header
		// balance is visible only to roles that may view profit or
		// settle (canViewVendorMoney) — warehouse holds neither, so
		// it sees no balance at all.
		if got := strings.Contains(html, "89.58"); got != tc.wantBalance {
			t.Fatalf("%s: ledger header balance 89.58 present = %v, want %v", tc.name, got, tc.wantBalance)
		}
		ledgerTable := section(t, html, "<h2>المشتريات</h2>", "<h2>المدفوعات</h2>")
		for _, inv := range []string{f.singleInv, f.multiInv} {
			if !strings.Contains(ledgerTable, inv) {
				t.Fatalf("%s: ledger table must still show invoice %s", tc.name, inv)
			}
		}
		for _, s := range []string{"37.02", "52.56", "الإجمالي", "المدفوع", "المتبقي"} {
			if got := strings.Contains(ledgerTable, s); got != tc.want {
				t.Fatalf("%s: ledger purchases table contains %q = %v, want %v", tc.name, s, got, tc.want)
			}
		}
		// FIX-16 R16-M1 (mandated spec change): the payments-table
		// money headers are gated on canViewProfit alone — the table
		// is a historical record that survives settlement, so it IS
		// buy-price money (BalanceBefore is the total). No payments
		// yet, so check the headers.
		for _, s := range []string{"المبلغ", "الرصيد قبل", "الرصيد بعد"} {
			if got := strings.Contains(html, s); got != tc.want {
				t.Fatalf("%s: ledger payments header %q present = %v, want %v", tc.name, s, got, tc.want)
			}
		}

		// --- vendors list: due-alert amounts only (the alert div) ---
		code, html = e.get(tc.cl, "/vendors")
		if code != http.StatusOK {
			t.Fatalf("%s GET /vendors: want 200, got %d", tc.name, code)
		}
		// The layout also has an "alert warn" live-orders banner, so
		// anchor on the due-alert text itself, not the div class.
		alerts := section(t, html, "مورد له مستحقات قريبة", "</div>")
		if !strings.Contains(alerts, "مورد ف15") || !strings.Contains(alerts, f.dueDate) {
			t.Fatalf("%s: due alert must still show the vendor name and due date", tc.name)
		}
		for _, s := range []string{"37.02", "52.56"} {
			if got := strings.Contains(alerts, s); got != tc.want {
				t.Fatalf("%s: due alert contains %q = %v, want %v", tc.name, s, got, tc.want)
			}
		}
		// FIX-16 R16-M1 (mandated spec change): the CurrentBalance
		// column is visible only to roles that may view profit or
		// settle — warehouse sees no balance.
		if got := strings.Contains(html, "89.58"); got != tc.wantBalance {
			t.Fatalf("%s: vendors list balance 89.58 present = %v, want %v", tc.name, got, tc.wantBalance)
		}
	}
}

// TestFix15M1_SettlementSurfaceKept pins the accepted residual: on the
// vendor detail page the pay-form dropdown and the amount prefill still
// show the amount owed (37.02 for the single-line purchase, prefill
// 89.58 = the vendor balance) — including for the cashier, so settlement
// still works. The dropdown exposes no qty/product detail alongside the
// amount (option text is invoice number + remaining only).
// FIX-16 R16-M1: warehouse holds no pay_vendors and no view_profit, so
// it sees NO pay form and no vendor money at all (its POST was already
// 403 — the form was dead UI that leaked the total).
func TestFix15M1_SettlementSurfaceKept(t *testing.T) {
	f := setupFix15M1(t)
	e := f.e

	for _, tc := range []struct {
		name   string
		cl     *http.Client
		settle bool // role may view profit or settle (pay form visible)
	}{
		{"cashier", e.cashierClient(), true},
		{"warehouse", e.warehouseClient(), false},
		{"admin", e.adminClient(), true},
		{"supervisor", e.supClient(), true},
	} {
		code, html := e.get(tc.cl, fmt.Sprintf("/vendors/%d", f.vendorID))
		if code != http.StatusOK {
			t.Fatalf("%s GET vendor detail: want 200, got %d", tc.name, code)
		}
		if !tc.settle {
			if strings.Contains(html, "سداد مورد") || strings.Contains(html, `/pay"`) {
				t.Fatalf("warehouse: vendor pay form must be hidden (no pay_vendors, no view_profit)")
			}
			head := section(t, html, "<h1>", "<h2>فواتير الشراء</h2>")
			for _, s := range []string{"37.02", "89.58", "الرصيد الحالي"} {
				if strings.Contains(head, s) {
					t.Fatalf("warehouse: vendor head must not contain %q", s)
				}
			}
			continue
		}
		// Pay form present (settlement surface intact) …
		if !strings.Contains(html, "سداد مورد") || !strings.Contains(html, `/pay"`) {
			t.Fatalf("%s: vendor pay form must still be present", tc.name)
		}
		// … and the pre-pay section (before the purchases table) still
		// carries the amounts settlement needs. This is the accepted
		// residual: 37.02 (single-line amount owed) + prefill 89.58.
		head := section(t, html, "<h1>", "<h2>فواتير الشراء</h2>")
		if !strings.Contains(head, "37.02") {
			t.Fatalf("%s: pay-form dropdown must still show the 37.02 amount owed (residual, pinned)", tc.name)
		}
		if !strings.Contains(head, `value="89.58"`) {
			t.Fatalf("%s: amount prefill must still be the 89.58 vendor balance", tc.name)
		}
		// … but the dropdown option carries no qty/product detail.
		if strings.Contains(head, "سكر") || strings.Contains(head, "أرز") {
			t.Fatalf("%s: pay-form dropdown must not expose product names", tc.name)
		}
	}
}
