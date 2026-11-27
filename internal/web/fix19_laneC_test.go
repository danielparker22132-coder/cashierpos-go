// Package web_test — FIX-19 lane C HTTP tests (R19-D-F1): the cashier
// /sales list must show only the caller's own invoices.
//
// The leak was list-level only: handleSalesIndex passed userID=0 to
// ListInvoices for every role, so cashier2's "فواتير اليوم" table
// rendered cashier1's invoice number, customer, net/paid/remaining
// amounts, payment method and seller name in full, while the detail
// and print links already 403'd (GetInvoice own-only, FIX-3 M4).
//
// Assertions slice the HTML from the "فواتير اليوم" heading onward so
// the POS form above the table (product/customer picker, JS cart keys)
// cannot produce false positives: the customer picker lists every
// customer and the JS contains "cashierpos_pos_cart", so bare
// substring checks on the whole page would be meaningless. Inside the
// table section the only rows are invoice rows, so the seller cell
// "<td>cashier</td>", the number cell and the net/paid amounts are
// exact per-invoice markers.
package web_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// fix19SalesListSection returns the /sales HTML from the "فواتير اليوم"
// heading onward (the invoice table only).
func fix19SalesListSection(t *testing.T, e *e2eEnv, c *http.Client) string {
	t.Helper()
	code, body := e.get(c, "/sales")
	if code != http.StatusOK {
		t.Fatalf("GET /sales: want 200, got %d body=%q", code, truncate(body, 300))
	}
	i := strings.Index(body, "فواتير اليوم")
	if i < 0 {
		t.Fatalf("GET /sales: no فواتير اليوم table in body %q", truncate(body, 300))
	}
	return body[i:]
}

// TestFix19CashierSalesListScoped (R19-D-F1): cashier1 sells 1 unit at
// 30.00 (net/paid 30.00), cashier2 sells 2 units at 30.00 (net/paid
// 60.00). Each cashier's list must show only their own invoice;
// warehouse/admin/manager/supervisor lists must still show both;
// cashier2's detail/print of cashier1's invoice stays 403.
func TestFix19CashierSalesListScoped(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	cash1 := e.cashierClient()
	if _, err := e.store.DB.Exec(`INSERT INTO Users(UserName,Password,FullName,Role,IsActive,MustChangePassword,CreatedDate)
		VALUES(?,?,?,?,1,0,?)`, "cashier2", sha256Hex("Cash2Pass123"), "cashier2", "cashier", "2026-11-26"); err != nil {
		t.Fatalf("seed cashier2: %v", err)
	}
	cash2 := e.loginAs("cashier2", "Cash2Pass123")

	pid := e.createProduct(admin, "F19C-001", "منتج ف19", "30.00", "10")
	e.openShift(cash1, 10000)
	e.openShift(cash2, 10000)
	invID1, num1 := e.cashSale(cash1, pid, "1", 3000)
	_, num2 := e.cashSale(cash2, pid, "2", 6000)

	// Exact per-invoice markers inside the table section.
	numCell1 := fmt.Sprintf(`<td class="num">%d</td>`, num1)
	numCell2 := fmt.Sprintf(`<td class="num">%d</td>`, num2)

	t.Run("cashier2 list hides cashier1 invoice", func(t *testing.T) {
		sec := fix19SalesListSection(t, e, cash2)
		if strings.Contains(sec, numCell1) {
			t.Fatalf("cashier2 list leaks cashier1 invoice number cell %s", numCell1)
		}
		if strings.Contains(sec, "<td>cashier</td>") {
			t.Fatal("cashier2 list leaks the seller cell <td>cashier</td>")
		}
		if strings.Contains(sec, "30.00 ج.م") {
			t.Fatal("cashier2 list leaks cashier1 amounts (30.00 ج.م net/paid)")
		}
		// Own invoice still present: number, seller, amounts.
		if !strings.Contains(sec, numCell2) {
			t.Fatalf("cashier2 list missing own invoice number cell %s", numCell2)
		}
		if !strings.Contains(sec, "<td>cashier2</td>") {
			t.Fatal("cashier2 list missing own seller cell <td>cashier2</td>")
		}
		if !strings.Contains(sec, "60.00 ج.م") {
			t.Fatal("cashier2 list missing own amounts (60.00 ج.م net/paid)")
		}
	})

	t.Run("cashier1 list hides cashier2 invoice", func(t *testing.T) {
		sec := fix19SalesListSection(t, e, cash1)
		if strings.Contains(sec, numCell2) {
			t.Fatalf("cashier1 list leaks cashier2 invoice number cell %s", numCell2)
		}
		if strings.Contains(sec, "<td>cashier2</td>") {
			t.Fatal("cashier1 list leaks the seller cell <td>cashier2</td>")
		}
		if strings.Contains(sec, "60.00 ج.م") {
			t.Fatal("cashier1 list leaks cashier2 amounts (60.00 ج.م net/paid)")
		}
		if !strings.Contains(sec, numCell1) {
			t.Fatalf("cashier1 list missing own invoice number cell %s", numCell1)
		}
		if !strings.Contains(sec, "30.00 ج.م") {
			t.Fatal("cashier1 list missing own amounts (30.00 ج.م net/paid)")
		}
	})

	t.Run("warehouse list still shows all", func(t *testing.T) {
		wh := e.warehouseClient()
		sec := fix19SalesListSection(t, e, wh)
		for _, want := range []string{numCell1, numCell2, "<td>cashier</td>", "<td>cashier2</td>", "30.00 ج.م", "60.00 ج.م"} {
			if !strings.Contains(sec, want) {
				t.Fatalf("warehouse list missing %q (L10 view-any must hold)", want)
			}
		}
	})

	t.Run("admin manager supervisor lists unchanged", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			c    *http.Client
		}{
			{"admin", admin},
			{"manager", e.managerClient()},
			{"supervisor", e.supClient()},
		} {
			sec := fix19SalesListSection(t, e, tc.c)
			for _, want := range []string{numCell1, numCell2, "<td>cashier</td>", "<td>cashier2</td>", "30.00 ج.م", "60.00 ج.م"} {
				if !strings.Contains(sec, want) {
					t.Fatalf("%s list missing %q (must still see all invoices)", tc.name, want)
				}
			}
		}
	})

	t.Run("cashier2 detail and print of cashier1 invoice stay 403", func(t *testing.T) {
		code, _ := e.get(cash2, fmt.Sprintf("/sales/%d", invID1))
		if code != http.StatusForbidden {
			t.Fatalf("cashier2 GET /sales/%d: want 403, got %d", invID1, code)
		}
		code, _ = e.get(cash2, fmt.Sprintf("/sales/%d/print", invID1))
		if code != http.StatusForbidden {
			t.Fatalf("cashier2 GET /sales/%d/print: want 403, got %d", invID1, code)
		}
	})
}
