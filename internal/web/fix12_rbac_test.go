// Package web_test — FIX-12 lane E tests (RBAC / hub / routes).
//
// SW-M10: supervisor below-cost sale refused server-side; admin/manager
//   sales with allow_below_cost still succeed.
// SW-M11: warehouse GET /zones → 200; warehouse zone management → 403.
// SW-M03: hub due-payables reminder (first hub load of the day only).
// SW-L10: hub links to Transfers / Courier Debts / Vendors per role.
package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"cashierpos-go/internal/store"
)

// belowCostBody is the sweep repro shape: product 2 in the leg (retail
// 3000, buy 2000), item discount 1100 → net 1900 < buy 2000.
func fix12BelowCostBody(pid int64) map[string]any {
	return map[string]any{
		"customer_id": 1, "sale_type": "retail", "payment_method": "Cash",
		"paid": 1900, "allow_below_cost": true,
		"lines": []any{map[string]any{"product_id": pid, "qty": "1", "unit_price": 3000, "item_discount": 1100}},
	}
}

func TestFix12_BelowCostSupervisorRefused(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	mgr := e.managerClient()
	sup := e.supClient()
	cash := e.cashierClient()
	wh := e.warehouseClient()
	res, err := e.store.DB.Exec(`INSERT INTO Products(ProductName,BuyPrice,RetailPrice,Quantity,MinQuantity,IsActive)
		VALUES('أرز',2000,3000,100000,5,1)`)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := res.LastInsertId()
	e.openShift(admin, 100000)
	e.openShift(mgr, 100000)
	e.openShift(sup, 100000)
	e.openShift(cash, 100000)

	// Admin + manager: below-cost sale with the flag still succeeds.
	for name, c := range map[string]any{"admin": admin, "manager": mgr} {
		client := c.(*http.Client)
		code, data := e.sell(client, fix12BelowCostBody(pid))
		if code != http.StatusOK || data["ok"] != true {
			t.Fatalf("%s below-cost sale: want 200 ok, got %d %v", name, code, data)
		}
	}
	// Supervisor: refused SERVER-SIDE with 403 + the uniform
	// permission message (FIX-12 SW-L08 maps service-layer
	// permission denials to 403, not 400), and no invoice is created.
	before := e.queryInt(`SELECT COUNT(*) FROM Invoices`)
	code, data := e.sell(sup, fix12BelowCostBody(pid))
	if code != http.StatusForbidden {
		t.Fatalf("supervisor below-cost sale: want 403, got %d %v", code, data)
	}
	if msg, _ := data["error"].(string); !strings.Contains(msg, "غير مسموح") {
		t.Fatalf("supervisor below-cost sale error = %q, want a permission refusal", msg)
	}
	if after := e.queryInt(`SELECT COUNT(*) FROM Invoices`); after != before {
		t.Fatalf("supervisor below-cost sale created an invoice (count %d → %d)", before, after)
	}
	// Supervisor preview with the flag is refused too (403, route-level
	// preview gate), and the sales form hides the checkbox from them.
	if code, data := e.postJSON(sup, "/api/sales/preview", fix12BelowCostBody(pid)); code != http.StatusForbidden {
		t.Fatalf("supervisor below-cost preview: want 403, got %d %v", code, data)
	}
	if code, html := e.get(sup, "/sales"); code != http.StatusOK {
		t.Fatalf("supervisor /sales: want 200, got %d", code)
	} else if strings.Contains(html, `name="allow_below_cost"`) {
		t.Fatal("supervisor sales form must not show the below-cost checkbox")
	}
	// Cashier: refused as well (403 + permission message).
	if code, data := e.sell(cash, fix12BelowCostBody(pid)); code != http.StatusForbidden {
		t.Fatalf("cashier below-cost sale: want 403, got %d %v", code, data)
	} else if msg, _ := data["error"].(string); !strings.Contains(msg, "غير مسموح") {
		t.Fatalf("cashier below-cost sale error = %q, want a permission refusal", msg)
	}
	// Warehouse: no sale permission at all — route-level 403.
	if code, data := e.postJSON(wh, "/api/sales/create", fix12BelowCostBody(pid)); code != http.StatusForbidden {
		t.Fatalf("warehouse below-cost sale: want 403, got %d %v", code, data)
	}
}

func TestFix12_WarehouseZonesViewOnly(t *testing.T) {
	e := readyEnv(t, false)
	wh := e.warehouseClient()
	sup := e.supClient()
	admin := e.adminClient()

	// Warehouse can now VIEW zones (SW-M11, §15 row 64).
	if code, body := e.get(wh, "/zones"); code != http.StatusOK {
		t.Fatalf("warehouse GET /zones: want 200, got %d body=%q", code, truncate(body, 200))
	}
	// Supervisor / admin keep their view access.
	if code, _ := e.get(sup, "/zones"); code != http.StatusOK {
		t.Fatalf("supervisor GET /zones: want 200, got %d", code)
	}
	if code, _ := e.get(admin, "/zones"); code != http.StatusOK {
		t.Fatalf("admin GET /zones: want 200, got %d", code)
	}
	// Zone MANAGEMENT stays admin-only: warehouse still 403s on save.
	if code, body := e.postForm(wh, "/settings/zones", url.Values{
		"zone_name": {"TestZone"}, "zone_fee": {"9.50"}, "zone_active": {"1"},
	}); code != http.StatusForbidden {
		t.Fatalf("warehouse POST /settings/zones: want 403, got %d body=%q", code, truncate(body, 200))
	}
	// ... and on the zone create/edit/delete surface (manage_zones).
	if code, _ := e.postForm(wh, "/settings/zones", url.Values{
		"zone_name": {"ناجي حمادي"}, "zone_fee": {"0"}, "zone_active": {"1"},
	}); code != http.StatusForbidden {
		t.Fatalf("warehouse POST /settings/zones (existing zone): want 403, got %d", code)
	}
}

func TestFix12_HubDuePayablesReminder(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()

	// No due payables yet: no reminder on the hub.
	if code, html := e.get(admin, "/"); code != http.StatusOK {
		t.Fatalf("GET /: want 200, got %d", code)
	} else if strings.Contains(html, "hub-payables-reminder") {
		t.Fatal("hub shows a due-payables reminder with no payables due")
	}

	// Create one due payable (due today) as admin.
	code, data := e.postJSON(admin, "/api/vendors", map[string]any{
		"name": "مورد اختبار", "phone": "01000000001",
	})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("create vendor: want 200 ok, got %d %v", code, data)
	}
	vendorID, _ := data["vendor_id"].(float64)
	code, data = e.postJSON(admin, "/api/payables", map[string]any{
		"kind": "vendor", "party_id": int64(vendorID), "amount": "100.00",
		"due_date": store.Today(), "description": "اختبار تذكير",
	})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("create payable: want 200 ok, got %d %v", code, data)
	}

	// First hub load with a payable due: reminder shows, with the count.
	if code, html := e.get(admin, "/"); code != http.StatusOK {
		t.Fatalf("GET / with due payable: want 200, got %d", code)
	} else if !strings.Contains(html, "hub-payables-reminder") {
		t.Fatal("hub shows no due-payables reminder with a payable due")
	} else if !strings.Contains(html, "تنبيه المستحقات") {
		t.Fatal("hub reminder lacks the 'تنبيه المستحقات' heading")
	} else if !strings.Contains(html, "/payables/alerts") {
		t.Fatal("hub reminder does not link to /payables/alerts")
	}
	// Second hub load the same day: deduped — reminder hidden.
	if code, html := e.get(admin, "/"); code != http.StatusOK {
		t.Fatalf("GET / (2nd load): want 200, got %d", code)
	} else if strings.Contains(html, "hub-payables-reminder") {
		t.Fatal("hub still shows the due-payables reminder on the 2nd load of the day (dedupe failed)")
	}
	// The reminder is read-only: the payable itself is untouched.
	if n := e.queryInt(`SELECT COUNT(*) FROM Payables WHERE Status='open'`); n != 1 {
		t.Fatalf("open payables = %d, want 1 (reminder must not mutate payables)", n)
	}

	// On-shift cashier audience: clear the cashier's once-per-day flag
	// (their login already consumed a hub load) and verify the
	// reminder shows for them too, then dedupes.
	cash := e.cashierClient()
	e.openShift(cash, 100000)
	flagKey := "payables_reminder_shown:cashier:" + store.Today()
	if _, err := e.store.DB.Exec(`DELETE FROM AppSettings WHERE Key=?`, flagKey); err != nil {
		t.Fatal(err)
	}
	if code, html := e.get(cash, "/"); code != http.StatusOK {
		t.Fatalf("cashier GET / with due payable: want 200, got %d", code)
	} else if !strings.Contains(html, "hub-payables-reminder") {
		t.Fatal("on-shift cashier hub shows no due-payables reminder")
	}
	if _, html := e.get(cash, "/"); strings.Contains(html, "hub-payables-reminder") {
		t.Fatal("cashier hub still shows the reminder on the 2nd load of the day (dedupe failed)")
	}
	// Warehouse never sees the reminder (no shift, not admin).
	wh := e.warehouseClient()
	if _, err := e.store.DB.Exec(`DELETE FROM AppSettings WHERE Key=?`, "payables_reminder_shown:warehouse:"+store.Today()); err != nil {
		t.Fatal(err)
	}
	if code, html := e.get(wh, "/"); code != http.StatusOK {
		t.Fatalf("warehouse GET /: want 200, got %d", code)
	} else if strings.Contains(html, "hub-payables-reminder") {
		t.Fatal("warehouse hub shows a due-payables reminder (must not — no payables audience)")
	}
}

func TestFix12_HubNavLinks(t *testing.T) {
	e := readyEnv(t, false)
	clients := map[string]*http.Client{
		"admin":      e.adminClient(),
		"manager":    e.managerClient(),
		"supervisor": e.supClient(),
		"cashier":    e.cashierClient(),
		"warehouse":  e.warehouseClient(),
	}
	// Transfers + Courier Debts: viewable by A/M/S/C, not warehouse.
	// Vendors: A/M (manage), cashier (create), supervisor (view — the
	// list/detail pages are login-open server-side); not warehouse.
	for role, c := range clients {
		code, html := e.get(c, "/")
		if code != http.StatusOK {
			t.Fatalf("%s GET /: want 200, got %d", role, code)
		}
		wantLinks := role != "warehouse"
		if got := strings.Contains(html, `href="/transfers"`); got != wantLinks {
			t.Errorf("%s hub: transfers link present=%v, want %v", role, got, wantLinks)
		}
		if got := strings.Contains(html, `href="/courier-debts"`); got != wantLinks {
			t.Errorf("%s hub: courier-debts link present=%v, want %v", role, got, wantLinks)
		}
		if got := strings.Contains(html, `href="/vendors"`); got != wantLinks {
			t.Errorf("%s hub: vendors link present=%v, want %v", role, got, wantLinks)
		}
	}
	// Spot-check the card titles render (not just the search data).
	if _, html := e.get(clients["supervisor"], "/"); !strings.Contains(html, "ديون المناديب") {
		t.Error("supervisor hub lacks the 'ديون المناديب' card")
	}
	if _, html := e.get(clients["admin"], "/"); !strings.Contains(html, "التحويلات") {
		t.Error("admin hub lacks the 'التحويلات' card")
	}
}
