// Package web_test — FIX-18 lane B (R18-B-01, R18-B-02).
//
// B-01: bare JSON numbers in amountFromValue are CENTS (100 -> 100),
// matching the core typed-struct money APIs; strings stay EGP decimals.
// B-02: voided invoices no longer feed the debtor date columns — a
// fully-returned invoice drops out of LastPayment (net cash received
// is 0) and a canceled invoice drops out of LastPurchase.
package web_test

import (
	"fmt"
	"net/http"
	"testing"

	"cashierpos-go/internal/store"
)

// fix18Sell is one retail invoice through the API; returns the invoice id.
func fix18Sell(t *testing.T, e *e2eEnv, c *http.Client, custID, productID int64, qty string, paid int64) int64 {
	t.Helper()
	code, body := e.sell(c, map[string]any{
		"sale_type": "retail", "customer_id": custID,
		"lines": []map[string]any{{"product_id": productID, "qty": qty}},
		"paid": paid, "payment_method": "Cash",
	})
	if code != http.StatusOK || body["ok"] != true {
		t.Fatalf("sale to %d: want 200 ok, got %d %v", custID, code, body)
	}
	invID, _ := body["invoice_id"].(float64)
	if invID == 0 {
		t.Fatalf("sale to %d: no invoice_id in %v", custID, body)
	}
	return int64(invID)
}

// fix18ReturnAll returns every unit of one product on an invoice.
func fix18ReturnAll(t *testing.T, e *e2eEnv, c *http.Client, invID, productID int64, qty string) map[string]any {
	t.Helper()
	code, body := e.postJSON(c, "/api/returns", map[string]any{
		"invoice_id": invID,
		"lines":      []map[string]any{{"product_id": productID, "qty": qty}},
		"reason":     "مرتجع تجريبي", "method": "Cash",
	})
	if code != http.StatusOK || body["ok"] != true {
		t.Fatalf("return invoice %d: want 200 ok, got %d %v", invID, code, body)
	}
	return body
}

func TestFix18Web_B01_SweepPayloadIsCents(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()

	// The sweep repro payload: {"credit_limit":10000} means a 100.00 EGP
	// limit (10000 cents), not 10,000 EGP (1000000 cents).
	id := fix17Customer(t, e, admin, map[string]any{"name": "SweepLimit", "credit_limit": 10000})
	if lim := e.queryInt(`SELECT CreditLimit FROM Customers WHERE CustomerID=?`, id); lim != 10000 {
		t.Fatalf("sweep credit_limit 10000: stored %d, want 10000", lim)
	}
	// String form is untouched: "100.50" is still an EGP decimal.
	strID := fix17Customer(t, e, admin, map[string]any{"name": "StrLimit18", "credit_limit": "100.50"})
	if lim := e.queryInt(`SELECT CreditLimit FROM Customers WHERE CustomerID=?`, strID); lim != 10050 {
		t.Fatalf("string credit_limit 100.50: stored %d, want 10050", lim)
	}
}

func TestFix18Web_B02_FullyReturnedInvoiceDropsFromLastPayment(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	cashier := e.cashierClient()
	sup := e.supClient()
	mgr := e.managerClient()
	e.openShift(cashier, 100000)
	e.openShift(sup, 100000)
	e.openShift(mgr, 100000)

	rice := e.createProduct(admin, "F18-B02", "أرز", "30.00", "100")
	today := store.Today()

	// (a) J: invoice 1 (1x rice, paid 1000 at the till) + invoice 2
	// (1x rice, full credit). Full return of invoice 1 refunds the
	// 1000: PaidAmount stays 1000 (historical fact) but the net cash
	// received is 0, so LastPayment must go blank while J stays a
	// debtor (3000) via invoice 2.
	custJ := fix17Customer(t, e, admin, map[string]any{"name": "عميل J"})
	invJ1 := fix18Sell(t, e, cashier, custJ, rice, "1", 1000)
	fix18Sell(t, e, cashier, custJ, rice, "1", 0)
	body := fix18ReturnAll(t, e, sup, invJ1, rice, "1")
	if body["total_refund_cents"] != float64(3000) {
		t.Fatalf("full return refund = %v, want 3000", body["total_refund_cents"])
	}
	if paid := e.queryInt(`SELECT PaidAmount FROM Invoices WHERE InvoiceID=?`, invJ1); paid != 1000 {
		t.Fatalf("fully-returned invoice PaidAmount = %d, want 1000 (not zeroed)", paid)
	}
	if net := e.queryInt(`SELECT COALESCE(SUM(Amount),0) FROM Payments WHERE InvoiceID=?`, invJ1); net != 0 {
		t.Fatalf("fully-returned invoice SUM(Payments) = %d, want 0", net)
	}
	rowJ := fix17Debtor(t, e, admin, custJ)
	if bal, _ := rowJ["Customer"].(map[string]any)["Balance"].(float64); bal != 3000 {
		t.Fatalf("debtor J balance = %v, want 3000", bal)
	}
	if lp, _ := rowJ["LastPayment"].(string); lp != "" {
		t.Fatalf("debtor J LastPayment = %q, want blank (only payment fully refunded)", lp)
	}

	// (a-control) K: 2x rice paid 2000, return 1 unit — the refund is
	// absorbed by debt, net cash received stays 2000, LastPayment
	// still shows today.
	custK := fix17Customer(t, e, admin, map[string]any{"name": "عميل K"})
	invK1 := fix18Sell(t, e, cashier, custK, rice, "2", 2000)
	body = fix18ReturnAll(t, e, sup, invK1, rice, "1")
	if body["total_refund_cents"] != float64(3000) {
		t.Fatalf("partial return refund = %v, want 3000", body["total_refund_cents"])
	}
	rowK := fix17Debtor(t, e, admin, custK)
	if lp, _ := rowK["LastPayment"].(string); lp != today {
		t.Fatalf("debtor K LastPayment = %q, want %q (part of the payment still held)", lp, today)
	}

	// (b) F: invoice 1 (paid 1000) canceled, invoice 2 (full credit)
	// backdated to 2026-11-19 — LastPurchase must be 11-19, not the
	// canceled invoice's date, and LastPayment blank.
	custF := fix17Customer(t, e, admin, map[string]any{"name": "عميل F"})
	invF1 := fix18Sell(t, e, cashier, custF, rice, "1", 1000)
	invF2 := fix18Sell(t, e, cashier, custF, rice, "1", 0)
	code, data := e.postJSON(mgr, fmt.Sprintf("/api/sales/%d/cancel", invF1), map[string]any{"reason": "إلغاء تجريبي"})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("cancel invoice %d: want 200 ok, got %d %v", invF1, code, data)
	}
	if _, err := e.store.DB.Exec(`UPDATE Invoices SET InvoiceDate='2026-11-19 10:00:00' WHERE InvoiceID=?`, invF2); err != nil {
		t.Fatalf("backdate invoice %d: %v", invF2, err)
	}
	rowF := fix17Debtor(t, e, admin, custF)
	if lp, _ := rowF["LastPurchase"].(string); lp != "2026-11-19" {
		t.Fatalf("debtor F LastPurchase = %q, want 2026-11-19 (canceled invoice ignored)", lp)
	}
	if lp, _ := rowF["LastPayment"].(string); lp != "" {
		t.Fatalf("debtor F LastPayment = %q, want blank (canceled invoice's payment excluded)", lp)
	}

	// (c) G: a collection payment (CustomerPayments leg) later refunded
	// by a full return of its invoice must drop out of LastPayment too.
	custG := fix17Customer(t, e, admin, map[string]any{"name": "عميل G"})
	invG1 := fix18Sell(t, e, cashier, custG, rice, "1", 0)
	code, data = e.postJSON(cashier, fmt.Sprintf("/api/customers/%d/pay-invoice", custG), map[string]any{
		"invoice_id": invG1, "amount": "5.00", "method": "Cash",
	})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("collection on invoice %d: want 200 ok, got %d %v", invG1, code, data)
	}
	fix18Sell(t, e, cashier, custG, rice, "1", 0)
	fix18ReturnAll(t, e, sup, invG1, rice, "1")
	rowG := fix17Debtor(t, e, admin, custG)
	if lp, _ := rowG["LastPayment"].(string); lp != "" {
		t.Fatalf("debtor G LastPayment = %q, want blank (collection refunded by full return)", lp)
	}

	// (d) H: a collection payment on an invoice that is then canceled
	// must drop out of LastPayment (the CustomerPayments leg had no
	// IsCanceled check at all).
	custH := fix17Customer(t, e, admin, map[string]any{"name": "عميل H"})
	invH1 := fix18Sell(t, e, cashier, custH, rice, "1", 0)
	code, data = e.postJSON(cashier, fmt.Sprintf("/api/customers/%d/pay-invoice", custH), map[string]any{
		"invoice_id": invH1, "amount": "5.00", "method": "Cash",
	})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("collection on invoice %d: want 200 ok, got %d %v", invH1, code, data)
	}
	fix18Sell(t, e, cashier, custH, rice, "1", 0)
	code, data = e.postJSON(mgr, fmt.Sprintf("/api/sales/%d/cancel", invH1), map[string]any{"reason": "إلغاء تجريبي"})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("cancel invoice %d: want 200 ok, got %d %v", invH1, code, data)
	}
	rowH := fix17Debtor(t, e, admin, custH)
	if lp, _ := rowH["LastPayment"].(string); lp != "" {
		t.Fatalf("debtor H LastPayment = %q, want blank (collection on canceled invoice)", lp)
	}
}
