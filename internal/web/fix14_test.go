// Package web_test — FIX-14 (R14-L1): single-line purchase header totals
// must not leak the unit buy price to roles without view_profit.
package web_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestFix14_SingleLinePurchaseHeaderTotalsHidden — R14-L1: on a single-line
// purchase the header totals leak the unit buy price by division, so for
// roles without view_profit the whole header money block (Total, Paid,
// Remaining) is hidden on the page and zeroed in the API. Total =
// Paid + Remaining exactly (creation sets remaining = total - paid and
// every payment moves both together), so hiding Total alone would leave
// (Paid+Remaining)/Qty = unit price — and with Paid=0, Remaining IS the
// total. Distinctive prices 12.34 × 3 = 37.02, part-paid 11.11 → 25.91.
func TestFix14_SingleLinePurchaseHeaderTotalsHidden(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()

	pid := e.createProduct(admin, "F14-1", "سكر", "15.00", "100")
	code, data := e.postJSON(admin, "/api/vendors", map[string]any{"name": "مورد ف14"})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("create vendor: want 200 ok, got %d %v", code, data)
	}
	vid, _ := data["vendor_id"].(float64)

	mkPurchase := func(lines []map[string]any, paid string) int64 {
		t.Helper()
		code, data := e.postJSON(admin, "/api/purchases", map[string]any{
			"vendor_id": int64(vid), "lines": lines, "paid": paid, "method": "Visa",
		})
		if code != http.StatusOK || data["ok"] != true {
			t.Fatalf("create purchase: want 200 ok, got %d %v", code, data)
		}
		id, _ := data["purchase_id"].(float64)
		return int64(id)
	}

	singleUnpaid := mkPurchase([]map[string]any{{"product_id": pid, "qty": "3", "price": "12.34"}}, "0")
	singlePart := mkPurchase([]map[string]any{{"product_id": pid, "qty": "3", "price": "12.34"}}, "11.11")

	header := func(t *testing.T, cl *http.Client, id int64) map[string]any {
		t.Helper()
		code, data := e.getJSON(cl, fmt.Sprintf("/api/purchases/%d", id))
		if code != http.StatusOK {
			t.Fatalf("GET /api/purchases/%d: want 200, got %d %v", id, code, data)
		}
		// PurchaseDetail marshals as {"Purchase": {...header...}, "Lines": [...]}.
		purchase, _ := data["purchase"].(map[string]any)
		hdr, _ := purchase["Purchase"].(map[string]any)
		return hdr
	}

	for _, tc := range []struct {
		name string
		cl   *http.Client
		want bool // want the money block visible
	}{
		{"cashier", e.cashierClient(), false},
		{"warehouse", e.warehouseClient(), false},
		{"admin", admin, true},
		{"supervisor", e.supClient(), true},
	} {
		// --- HTML page, unpaid single-line purchase ---
		path := fmt.Sprintf("/purchases/%d", singleUnpaid)
		code, html := e.get(tc.cl, path)
		if code != http.StatusOK {
			t.Fatalf("%s GET %s: want 200, got %d", tc.name, path, code)
		}
		if !strings.Contains(html, "سكر") || !strings.Contains(html, `<td class="num">3</td>`) {
			t.Fatalf("%s: detail page must still show the product and its qty", tc.name)
		}
		for _, s := range []string{"37.02", "12.34", "سعر الشراء", "الإجمالي", "المدفوع", "المتبقي"} {
			if got := strings.Contains(html, s); got != tc.want {
				t.Fatalf("%s: page contains %q = %v, want %v", tc.name, s, got, tc.want)
			}
		}
		// --- API, unpaid single-line purchase ---
		p := header(t, tc.cl, singleUnpaid)
		if tc.want {
			if p["Total"] != float64(3702) || p["Paid"] != float64(0) || p["Remaining"] != float64(3702) {
				t.Fatalf("%s: API header want 3702/0/3702, got %v/%v/%v", tc.name, p["Total"], p["Paid"], p["Remaining"])
			}
		} else if p["Total"] != float64(0) || p["Paid"] != float64(0) || p["Remaining"] != float64(0) {
			t.Fatalf("%s: API header leaked (Total=%v Paid=%v Remaining=%v)", tc.name, p["Total"], p["Paid"], p["Remaining"])
		}
		// --- HTML + API, part-paid single-line purchase: 37.02 = 11.11 + 25.91 ---
		path = fmt.Sprintf("/purchases/%d", singlePart)
		code, html = e.get(tc.cl, path)
		if code != http.StatusOK {
			t.Fatalf("%s GET %s: want 200, got %d", tc.name, path, code)
		}
		for _, s := range []string{"37.02", "11.11", "25.91", "12.34"} {
			if got := strings.Contains(html, s); got != tc.want {
				t.Fatalf("%s (part-paid): page contains %q = %v, want %v", tc.name, s, got, tc.want)
			}
		}
		p = header(t, tc.cl, singlePart)
		if tc.want {
			if p["Total"] != float64(3702) || p["Paid"] != float64(1111) || p["Remaining"] != float64(2591) {
				t.Fatalf("%s (part-paid): API header want 3702/1111/2591, got %v/%v/%v", tc.name, p["Total"], p["Paid"], p["Remaining"])
			}
		} else if p["Total"] != float64(0) || p["Paid"] != float64(0) || p["Remaining"] != float64(0) {
			t.Fatalf("%s (part-paid): API header leaked (Total=%v Paid=%v Remaining=%v)", tc.name, p["Total"], p["Paid"], p["Remaining"])
		}
	}
}

// TestFix14_MultiLinePurchaseHeaderTotalsVisible — R14-L1 scope: multi-line
// header totals stay visible to every role (payables settlement; the total
// is a sum, so no per-line unit price is derivable), while the per-line
// buy prices stay hidden from roles without view_profit.
func TestFix14_MultiLinePurchaseHeaderTotalsVisible(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()

	pid1 := e.createProduct(admin, "F14-1", "سكر", "15.00", "100")
	pid2 := e.createProduct(admin, "F14-2", "أرز", "12.00", "100")
	code, data := e.postJSON(admin, "/api/vendors", map[string]any{"name": "مورد ف14"})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("create vendor: want 200 ok, got %d %v", code, data)
	}
	vid, _ := data["vendor_id"].(float64)
	// 12.34×3 + 7.77×2 = 37.02 + 15.54 = 52.56.
	code, data = e.postJSON(admin, "/api/purchases", map[string]any{
		"vendor_id": int64(vid),
		"lines": []map[string]any{
			{"product_id": pid1, "qty": "3", "price": "12.34"},
			{"product_id": pid2, "qty": "2", "price": "7.77"},
		},
		"paid": "0", "method": "Visa",
	})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("create purchase: want 200 ok, got %d %v", code, data)
	}
	purchaseID, _ := data["purchase_id"].(float64)

	for _, tc := range []struct {
		name string
		cl   *http.Client
		want bool // want the per-line buy price visible
	}{
		{"cashier", e.cashierClient(), false},
		{"warehouse", e.warehouseClient(), false},
		{"admin", admin, true},
		{"supervisor", e.supClient(), true},
	} {
		code, html := e.get(tc.cl, fmt.Sprintf("/purchases/%d", int64(purchaseID)))
		if code != http.StatusOK {
			t.Fatalf("%s GET purchase: want 200, got %d", tc.name, code)
		}
		if !strings.Contains(html, "52.56") {
			t.Fatalf("%s: multi-line header total 52.56 must stay visible", tc.name)
		}
		for _, s := range []string{"12.34", "7.77", "سعر الشراء"} {
			if got := strings.Contains(html, s); got != tc.want {
				t.Fatalf("%s: page contains %q = %v, want %v", tc.name, s, got, tc.want)
			}
		}
		code, data := e.getJSON(tc.cl, fmt.Sprintf("/api/purchases/%d", int64(purchaseID)))
		if code != http.StatusOK {
			t.Fatalf("%s GET API purchase: want 200, got %d %v", tc.name, code, data)
		}
		purchase, _ := data["purchase"].(map[string]any)
		hdr, _ := purchase["Purchase"].(map[string]any)
		if hdr["Total"] != float64(5256) || hdr["Remaining"] != float64(5256) {
			t.Fatalf("%s: API header want Total/Remaining 5256, got %v/%v", tc.name, hdr["Total"], hdr["Remaining"])
		}
		lines, _ := purchase["Lines"].([]any)
		for _, l := range lines {
			ln, _ := l.(map[string]any)
			if tc.want {
				if ln["PurchasePrice"] == float64(0) {
					t.Fatalf("%s: API line buy price must be visible", tc.name)
				}
			} else if ln["PurchasePrice"] != float64(0) || ln["Total"] != float64(0) {
				t.Fatalf("%s: API line buy price leaked (price=%v total=%v)", tc.name, ln["PurchasePrice"], ln["Total"])
			}
		}
	}
}
