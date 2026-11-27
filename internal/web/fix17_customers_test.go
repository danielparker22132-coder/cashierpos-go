// Package web_test — FIX-17 lane B (customers): SW2-M02, SW2-M03,
// SW2-L04, SW2-L08.
//
// M02: per-invoice payment must verify the invoice belongs to the
// customer in the URL (HTML + API). M03: JSON numbers for numeric
// fields (credit_limit / paid / qty / price) must be honoured, not
// silently dropped to 0. L04: debtors "آخر سداد" must include payments
// made at sale time (invoice PaidAmount). L08: GET /api/customers/{id}
// must return 403 (not 404) for a warehouse denial.
package web_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"cashierpos-go/internal/store"
)

// fix17Customer creates a customer via the API and returns its id.
func fix17Customer(t *testing.T, e *e2eEnv, c *http.Client, body map[string]any) int64 {
	t.Helper()
	code, data := e.postJSON(c, "/api/customers", body)
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("create customer %v: want 200 ok, got %d %v", body, code, data)
	}
	id, _ := data["customer_id"].(float64)
	if id == 0 {
		t.Fatalf("create customer %v: no customer_id in %v", body, data)
	}
	return int64(id)
}

// fix17Debtor finds one debtor row by customer id in /api/debtors.
func fix17Debtor(t *testing.T, e *e2eEnv, c *http.Client, custID int64) map[string]any {
	t.Helper()
	code, data := e.getJSON(c, "/api/debtors")
	if code != http.StatusOK {
		t.Fatalf("GET /api/debtors: want 200, got %d %v", code, data)
	}
	debtors, _ := data["debtors"].([]any)
	for _, d := range debtors {
		if m, ok := d.(map[string]any); ok {
			if cust, ok := m["Customer"].(map[string]any); ok && cust["ID"] == float64(custID) {
				return m
			}
		}
	}
	t.Fatalf("debtor %d not in /api/debtors: %v", custID, data)
	return nil
}

func TestFix17Web_M02_PayInvoiceRejectsOtherCustomersInvoice(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	cashier := e.cashierClient()
	e.openShift(cashier, 10000)

	// Product: rice, retail 30.00.
	rice := e.createProduct(admin, "F17-M02", "أرز", "30.00", "100")
	// Customer A buys 1× rice paying 10.00 at the till: invoice
	// PaidAmount 1000, RemainingAmount 2000. Customer B has no invoices.
	custA := fix17Customer(t, e, admin, map[string]any{"name": "عميل أ"})
	custB := fix17Customer(t, e, admin, map[string]any{"name": "عميل ب"})
	code, body := e.sell(cashier, map[string]any{
		"sale_type": "retail", "customer_id": custA,
		"lines": []map[string]any{{"product_id": rice, "qty": "1"}},
		"paid": 1000, "payment_method": "Cash",
	})
	if code != http.StatusOK || body["ok"] != true {
		t.Fatalf("credit sale to A: want 200 ok, got %d %v", code, body)
	}
	invID, _ := body["invoice_id"].(float64)
	if invID == 0 {
		t.Fatalf("credit sale to A: no invoice_id in %v", body)
	}
	if rem := e.queryInt(`SELECT RemainingAmount FROM Invoices WHERE InvoiceID=?`, int64(invID)); rem != 2000 {
		t.Fatalf("invoice remaining = %d, want 2000", rem)
	}
	paymentsBefore := e.queryInt(`SELECT COUNT(*) FROM CustomerPayments`)

	// Mismatch over HTML: pay A's invoice from B's page.
	code, respBody := e.postForm(cashier, fmt.Sprintf("/customers/%d/pay-invoice", custB), url.Values{
		"invoice_id": {fmt.Sprintf("%d", int64(invID))}, "amount": {"5.00"}, "method": {"Cash"},
	})
	if code != http.StatusOK {
		t.Fatalf("mismatched HTML pay: want 200 (error page), got %d body=%q", code, truncate(respBody, 300))
	}
	if !strings.Contains(respBody, "لا تخص هذا العميل") {
		t.Fatalf("mismatched HTML pay lacks the ownership message: %q", truncate(respBody, 300))
	}
	// Nothing moved: no payment row, invoice + both balances untouched.
	if n := e.queryInt(`SELECT COUNT(*) FROM CustomerPayments`); n != paymentsBefore {
		t.Fatalf("CustomerPayments rows = %d, want still %d after refused mismatch", n, paymentsBefore)
	}
	if rem := e.queryInt(`SELECT RemainingAmount FROM Invoices WHERE InvoiceID=?`, int64(invID)); rem != 2000 {
		t.Fatalf("invoice remaining after refused mismatch = %d, want 2000", rem)
	}
	if bal := e.queryInt(`SELECT Balance FROM Customers WHERE CustomerID=?`, custB); bal != 0 {
		t.Fatalf("customer B balance = %d, want 0 after refused mismatch", bal)
	}

	// Matched HTML payment still works: A's invoice from A's page.
	code, loc, _ := e.postFormLoc(cashier, fmt.Sprintf("/customers/%d/pay-invoice", custA), url.Values{
		"invoice_id": {fmt.Sprintf("%d", int64(invID))}, "amount": {"5.00"}, "method": {"Cash"},
	})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, fmt.Sprintf("/customers/%d/statement", custA)) {
		t.Fatalf("matched HTML pay: want 303 to A's statement, got %d loc %q", code, loc)
	}
	if rem := e.queryInt(`SELECT RemainingAmount FROM Invoices WHERE InvoiceID=?`, int64(invID)); rem != 1500 {
		t.Fatalf("invoice remaining after matched pay = %d, want 1500", rem)
	}

	// Mismatch over the API: the route carries {id} — it must be honoured.
	code, data := e.postJSON(cashier, fmt.Sprintf("/api/customers/%d/pay-invoice", custB), map[string]any{
		"invoice_id": int64(invID), "amount": "5.00", "method": "Cash",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("mismatched API pay: want 400, got %d %v", code, data)
	}
	if msg, _ := data["error"].(string); !strings.Contains(msg, "لا تخص هذا العميل") {
		t.Fatalf("mismatched API pay error = %q, want the ownership message", msg)
	}
	if rem := e.queryInt(`SELECT RemainingAmount FROM Invoices WHERE InvoiceID=?`, int64(invID)); rem != 1500 {
		t.Fatalf("invoice remaining after refused API mismatch = %d, want 1500", rem)
	}

	// Matched API payment still works.
	code, data = e.postJSON(cashier, fmt.Sprintf("/api/customers/%d/pay-invoice", custA), map[string]any{
		"invoice_id": int64(invID), "amount": "5.00", "method": "Cash",
	})
	if code != http.StatusOK || data["new_remaining_cents"] != float64(1000) {
		t.Fatalf("matched API pay: want 200 remaining 1000, got %d %v", code, data)
	}
}

func TestFix17Web_M03_NumericCreditLimitHonoured(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()

	// JSON number: 100 (CENTS, like the sale/order APIs — FIX-18
	// R18-B-01) -> stored 100 cents. The fix-17 reading (100 -> 10000,
	// number = EGP) contradicted the rest of the API and is superseded.
	numID := fix17Customer(t, e, admin, map[string]any{"name": "NumLimit", "credit_limit": 100})
	if lim := e.queryInt(`SELECT CreditLimit FROM Customers WHERE CustomerID=?`, numID); lim != 100 {
		t.Fatalf("numeric credit_limit: stored %d, want 100", lim)
	}
	// JSON number with decimals truncates to whole cents: 100.50 -> 100.
	numDecID := fix17Customer(t, e, admin, map[string]any{"name": "NumLimitDec", "credit_limit": 100.50})
	if lim := e.queryInt(`SELECT CreditLimit FROM Customers WHERE CustomerID=?`, numDecID); lim != 100 {
		t.Fatalf("numeric credit_limit 100.50: stored %d, want 100", lim)
	}
	// String form still works: "50.00" -> 5000.
	strID := fix17Customer(t, e, admin, map[string]any{"name": "StrLimit", "credit_limit": "50.00"})
	if lim := e.queryInt(`SELECT CreditLimit FROM Customers WHERE CustomerID=?`, strID); lim != 5000 {
		t.Fatalf("string credit_limit: stored %d, want 5000", lim)
	}
	// Negative is still refused, number and string alike.
	code, data := e.postJSON(admin, "/api/customers", map[string]any{"name": "NegNum", "credit_limit": -5})
	if code != http.StatusBadRequest {
		t.Fatalf("negative numeric credit_limit: want 400, got %d %v", code, data)
	}
	code, data = e.postJSON(admin, "/api/customers", map[string]any{"name": "NegStr", "credit_limit": "-5.00"})
	if code != http.StatusBadRequest {
		t.Fatalf("negative string credit_limit: want 400, got %d %v", code, data)
	}
	// No limit given at all still means unlimited (0).
	freeID := fix17Customer(t, e, admin, map[string]any{"name": "NoLimit"})
	if lim := e.queryInt(`SELECT CreditLimit FROM Customers WHERE CustomerID=?`, freeID); lim != 0 {
		t.Fatalf("absent credit_limit: stored %d, want 0", lim)
	}

	// The stored numeric limit actually gates credit: a 275.00 sale to
	// the 1.00-limit customer (100 cents) is refused for the cashier.
	cashier := e.cashierClient()
	e.openShift(cashier, 10000)
	oil := e.createProduct(admin, "F17-M03", "زيت", "55.00", "50")
	code, data = e.sell(cashier, map[string]any{
		"sale_type": "retail", "customer_id": numID,
		"lines": []map[string]any{{"product_id": oil, "qty": "5"}},
		"paid": 0, "payment_method": "Cash",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("275.00 credit sale over a 1.00 numeric limit: want 400, got %d %v", code, data)
	}
	if msg, _ := data["error"].(string); !strings.Contains(msg, "حد الائتمان") {
		t.Fatalf("over-limit sale error = %q, want the credit-limit message", msg)
	}
	if n := e.queryInt(`SELECT COUNT(*) FROM Invoices WHERE CustomerID=?`, numID); n != 0 {
		t.Fatalf("invoices for NumLimit = %d, want 0 after refused over-limit sale", n)
	}
}

func TestFix17Web_M03_OtherNumericFieldsHonoured(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()

	// Vendor numeric credit_limit: 200 -> 200 cents (FIX-18 R18-B-01:
	// bare number = cents, was 20000 under the fix-17 EGP reading).
	code, data := e.postJSON(admin, "/api/vendors", map[string]any{"name": "مورد رقمي", "credit_limit": 200})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("create vendor numeric limit: want 200 ok, got %d %v", code, data)
	}
	vid, _ := data["vendor_id"].(float64)
	if lim := e.queryInt(`SELECT CreditLimit FROM Vendors WHERE VendorID=?`, int64(vid)); lim != 200 {
		t.Fatalf("vendor numeric credit_limit: stored %d, want 200", lim)
	}

	// Purchase with numeric paid/qty/price: 2 units @ 10 cents, paid 5
	// cents (bare numbers = cents, FIX-18 R18-B-01).
	pid := e.createProduct(admin, "F17-M03B", "سكر", "15.00", "100")
	code, data = e.postJSON(admin, "/api/purchases", map[string]any{
		"vendor_id": int64(vid),
		"lines":     []map[string]any{{"product_id": pid, "qty": 2, "price": 10.00}},
		"paid": 5, "method": "Visa",
	})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("create purchase numeric fields: want 200 ok, got %d %v", code, data)
	}
	purchID, _ := data["purchase_id"].(float64)
	if total := e.queryInt(`SELECT TotalAmount FROM PurchaseInvoices WHERE PurchaseID=?`, int64(purchID)); total != 20 {
		t.Fatalf("purchase total = %d, want 20 (2 x 10 cents)", total)
	}
	if paid := e.queryInt(`SELECT PaidAmount FROM PurchaseInvoices WHERE PurchaseID=?`, int64(purchID)); paid != 5 {
		t.Fatalf("purchase paid = %d, want 5 (numeric paid 5 honoured as cents)", paid)
	}
}

func TestFix17Web_L04_DebtorsLastPaymentIncludesAtSalePayment(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	cashier := e.cashierClient()
	e.openShift(cashier, 10000)

	rice := e.createProduct(admin, "F17-L04", "أرز", "30.00", "100")
	// Customer D buys 1× rice (30.00) paying 10.00 at the till.
	custD := fix17Customer(t, e, admin, map[string]any{"name": "عميل دفع عند البيع"})
	code, body := e.sell(cashier, map[string]any{
		"sale_type": "retail", "customer_id": custD,
		"lines": []map[string]any{{"product_id": rice, "qty": "1"}},
		"paid": 1000, "payment_method": "Cash",
	})
	if code != http.StatusOK || body["ok"] != true {
		t.Fatalf("at-sale partial payment: want 200 ok, got %d %v", code, body)
	}
	// Customer E buys on full credit (paid 0): no payment yet.
	custE := fix17Customer(t, e, admin, map[string]any{"name": "عميل آجل بالكامل"})
	code, body = e.sell(cashier, map[string]any{
		"sale_type": "retail", "customer_id": custE,
		"lines": []map[string]any{{"product_id": rice, "qty": "1"}},
		"paid": 0, "payment_method": "Cash",
	})
	if code != http.StatusOK || body["ok"] != true {
		t.Fatalf("full-credit sale: want 200 ok, got %d %v", code, body)
	}

	today := store.Today()
	rowD := fix17Debtor(t, e, admin, custD)
	if lp, _ := rowD["LastPayment"].(string); lp != today {
		t.Fatalf("debtor D LastPayment = %q, want %q (at-sale payment date)", lp, today)
	}
	if lp, _ := rowD["LastPurchase"].(string); lp != today {
		t.Fatalf("debtor D LastPurchase = %q, want %q", lp, today)
	}
	rowE := fix17Debtor(t, e, admin, custE)
	if lp, _ := rowE["LastPayment"].(string); lp != "" {
		t.Fatalf("debtor E LastPayment = %q, want blank (never paid)", lp)
	}
}

func TestFix17Web_L08_SingleCustomerGetDenialIs403(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	wh := e.warehouseClient()
	cashier := e.cashierClient()

	// Warehouse is denied manage_customers: single-get must be 403
	// with the uniform denial text (SW-M12 pattern), not 404.
	code, data := e.getJSON(wh, "/api/customers/2")
	if code != http.StatusForbidden {
		t.Fatalf("warehouse GET /api/customers/2: want 403, got %d %v", code, data)
	}
	if msg, _ := data["error"].(string); !strings.Contains(msg, "غير مسموح") {
		t.Fatalf("warehouse GET /api/customers/2: error %q, want the uniform denial", msg)
	}
	// Cashier still reads a customer fine.
	code, data = e.getJSON(cashier, "/api/customers/1")
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("cashier GET /api/customers/1: want 200 ok, got %d %v", code, data)
	}
	// A genuinely missing customer is still 404 (not 403).
	code, data = e.getJSON(admin, "/api/customers/9999")
	if code != http.StatusNotFound {
		t.Fatalf("admin GET /api/customers/9999: want 404, got %d %v", code, data)
	}
}
