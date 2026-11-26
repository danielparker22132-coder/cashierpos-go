// Package web_test — FIX-2 web-level regression tests.
//
// L-4 (opening_cents rounding) and L-7 (preview credit-limit check) live
// in the HTTP layer, so they are tested over HTTP here with the e2e
// harness from e2e_test.go.
package web_test

import (
	"strings"
	"testing"

	"cashierpos-go/internal/store"
)

func TestFix2_ShiftOpenCentsRounds(t *testing.T) {
	e := newEnv(t, false)
	e.seedUsers()
	e.changeAdminPassword()
	admin := e.adminClient()
	// FIX-2 L-4: opening_cents 10000.99 rounds to 10001, like the
	// numeric "opening" branch (truncation gave 10000).
	code, body := e.postJSON(admin, "/api/shifts/open", map[string]any{"opening_cents": 10000.99})
	expectOK(t, "open shift", code, body)
	code, body = e.getJSON(admin, "/api/shifts/summary")
	expectOK(t, "shift summary", code, body)
	sum, ok := body["summary"].(map[string]any)
	if !ok {
		t.Fatalf("summary missing in %v", body)
	}
	if sum["OpeningBalance"] != float64(10001) {
		t.Fatalf("opening = %v, want 10001 (rounded, not truncated)", sum["OpeningBalance"])
	}
}

func TestFix2_PreviewRefusesOverCreditLimit(t *testing.T) {
	e := newEnv(t, false)
	e.seedUsers()
	e.changeAdminPassword()
	admin := e.adminClient()
	res, err := e.store.DB.Exec(`INSERT INTO Products(ProductName,BuyPrice,RetailPrice,Quantity,MinQuantity,IsActive)
		VALUES('سكر',1000,2000,100000,5,1)`)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := res.LastInsertId()
	res, err = e.store.DB.Exec(`INSERT INTO Customers(CustomerName,CreditLimit,IsActive) VALUES('عميل حد',3000,1)`)
	if err != nil {
		t.Fatal(err)
	}
	custID, _ := res.LastInsertId()
	// The customer already owes 2000 on an open invoice.
	if _, err := e.store.DB.Exec(`INSERT INTO Invoices(InvoiceNumber,CustomerID,TotalAmount,NetAmount,PaidAmount,RemainingAmount,PaymentMethod,InvoiceDate,IsCanceled)
		VALUES(1,?,2000,2000,0,2000,'Cash',?,0)`, custID, store.Now()); err != nil {
		t.Fatal(err)
	}
	e.openShift(admin, 10000)
	preview := func(body map[string]any) (int, map[string]any) {
		return e.postJSON(admin, "/api/sales/preview", body)
	}
	base := map[string]any{
		"customer_id": custID, "sale_type": "retail", "payment_method": "Cash",
		"paid": 0, "lines": []any{map[string]any{"product_id": pid, "qty": "1.000"}},
	}
	// Live 2000 + new remaining 2000 > limit 3000: refused (L-7).
	code, body := preview(base)
	expectDenied(t, "preview over credit limit", code, body)
	if msg, _ := body["error"].(string); !strings.Contains(msg, "حد الائتمان") {
		t.Fatalf("preview error = %q, want credit-limit refusal", msg)
	}
	// Paid in full: remaining 0, no overrun -> OK.
	paid := map[string]any{
		"customer_id": custID, "sale_type": "retail", "payment_method": "Cash",
		"paid": 2000, "lines": []any{map[string]any{"product_id": pid, "qty": "1.000"}},
	}
	code, body = preview(paid)
	expectOK(t, "preview paid in full", code, body)
	// With allow_over_limit (manager/admin approval): OK.
	over := map[string]any{
		"customer_id": custID, "sale_type": "retail", "payment_method": "Cash",
		"paid": 0, "allow_over_limit": true, "lines": []any{map[string]any{"product_id": pid, "qty": "1.000"}},
	}
	code, body = preview(over)
	expectOK(t, "preview over limit with approval", code, body)
}
