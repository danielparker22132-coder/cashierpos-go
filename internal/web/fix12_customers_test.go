// Package web_test — FIX-12 lane C (customers / payables / courier /
// vendors) HTTP tests: SW-H01, SW-H02, SW-H03, SW-H04, SW-H08, SW-M01,
// SW-M02, SW-M04, SW-M12, SW-L01, SW-L03.
package web_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"cashierpos-go/internal/store"
)

func TestFix12Web_H01_VendorDeleteBlockedByOpenPayable(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()

	code, data := e.postJSON(admin, "/api/vendors", map[string]any{"name": "مورد زيتا"})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("create vendor: want 200 ok, got %d %v", code, data)
	}
	vid, _ := data["vendor_id"].(float64)
	code, data = e.postJSON(admin, "/api/payables", map[string]any{
		"kind": "vendor", "party_id": int64(vid), "amount": "100.00",
		"due_date": "2026-12-20", "description": "اختبار حذف",
	})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("create payable: want 200 ok, got %d %v", code, data)
	}
	// Delete refused AND the refusal renders (SW-M04): 200 with the
	// Arabic message, not a 500.
	code, body := e.postForm(admin, fmt.Sprintf("/vendors/%d/delete", int64(vid)), url.Values{})
	if code != http.StatusOK {
		t.Fatalf("POST /vendors/%d/delete: want 200, got %d body=%q", int64(vid), code, truncate(body, 300))
	}
	if !strings.Contains(body, "مستحقات مفتوحة") {
		t.Fatalf("delete refusal page lacks the message: %q", truncate(body, 300))
	}
	// Vendor still exists; payable still open.
	code, data = e.getJSON(admin, "/api/vendors")
	if code != http.StatusOK {
		t.Fatalf("GET /api/vendors: %d", code)
	}
	found := false
	if vendors, ok := data["vendors"].([]any); ok {
		for _, v := range vendors {
			if m, ok := v.(map[string]any); ok && m["ID"] == vid {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("vendor gone after refused delete")
	}
	code, data = e.getJSON(admin, "/api/payables")
	if code != http.StatusOK {
		t.Fatalf("GET /api/payables: %d", code)
	}
	if payables, ok := data["payables"].([]any); ok {
		if len(payables) != 1 {
			t.Fatalf("payables = %d, want 1 (orphan check)", len(payables))
		}
		if m, ok := payables[0].(map[string]any); ok && m["Status"] != "open" {
			t.Fatalf("payable status = %v, want open", m["Status"])
		}
	}
}

func TestFix12Web_H02_PayableCancelAPIAndHTML(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()

	code, data := e.postJSON(admin, "/api/payables", map[string]any{
		"kind": "courier", "party_name": "مندوب", "amount": "100.00",
		"due_date": "2026-12-01", "description": "توصيل",
	})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("create payable: want 200 ok, got %d %v", code, data)
	}
	pid, _ := data["payable_id"].(float64)

	// Cancel without a reason is refused.
	code, data = e.postJSON(admin, fmt.Sprintf("/api/payables/%d/cancel", int64(pid)), map[string]any{})
	if code != http.StatusBadRequest {
		t.Fatalf("cancel without reason: want 400, got %d %v", code, data)
	}
	// Cancel with a reason succeeds.
	code, data = e.postJSON(admin, fmt.Sprintf("/api/payables/%d/cancel", int64(pid)), map[string]any{"reason": "ألغي الطلب"})
	if code != http.StatusOK || data["status"] != "cancelled" {
		t.Fatalf("cancel payable: want 200 cancelled, got %d %v", code, data)
	}
	// Cancelled leaves the default list; shows under status=cancelled.
	code, data = e.getJSON(admin, "/api/payables")
	if code != http.StatusOK {
		t.Fatalf("GET /api/payables: %d", code)
	}
	if payables, ok := data["payables"].([]any); ok {
		for _, p := range payables {
			if m, ok := p.(map[string]any); ok && m["ID"] == pid {
				t.Fatal("cancelled payable still in the default list")
			}
		}
	}
	code, data = e.getJSON(admin, "/api/payables?status=cancelled")
	if code != http.StatusOK {
		t.Fatalf("GET /api/payables?status=cancelled: %d", code)
	}
	found := false
	if payables, ok := data["payables"].([]any); ok {
		for _, p := range payables {
			if m, ok := p.(map[string]any); ok && m["ID"] == pid {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("cancelled payable missing from status=cancelled list")
	}
	// Cancel again refused; settle refused.
	code, _ = e.postJSON(admin, fmt.Sprintf("/api/payables/%d/cancel", int64(pid)), map[string]any{"reason": "مرة ثانية"})
	if code != http.StatusBadRequest {
		t.Fatalf("cancel already-cancelled: want 400, got %d", code)
	}
	code, _ = e.postJSON(admin, fmt.Sprintf("/api/payables/%d/settle", int64(pid)), map[string]any{"amount": "10.00", "method": "Visa"})
	if code != http.StatusBadRequest {
		t.Fatalf("settle cancelled: want 400, got %d", code)
	}
	// A paid payable cannot be cancelled.
	code, data = e.postJSON(admin, "/api/payables", map[string]any{
		"kind": "courier", "party_name": "مندوب 2", "amount": "50.00",
		"due_date": "2026-12-01", "description": "توصيل",
	})
	pid2, _ := data["payable_id"].(float64)
	code, _ = e.postJSON(admin, fmt.Sprintf("/api/payables/%d/settle", int64(pid2)), map[string]any{"amount": "50.00", "method": "Visa"})
	if code != http.StatusOK {
		t.Fatalf("settle payable 2: want 200, got %d", code)
	}
	code, _ = e.postJSON(admin, fmt.Sprintf("/api/payables/%d/cancel", int64(pid2)), map[string]any{"reason": "بعد السداد"})
	if code != http.StatusBadRequest {
		t.Fatalf("cancel paid payable: want 400, got %d", code)
	}
	// HTML cancel works too (admin, with a reason).
	code, data = e.postJSON(admin, "/api/payables", map[string]any{
		"kind": "courier", "party_name": "مندوب 3", "amount": "30.00",
		"due_date": "2026-12-01", "description": "توصيل",
	})
	pid3, _ := data["payable_id"].(float64)
	code, loc, _ := e.postFormLoc(admin, fmt.Sprintf("/payables/%d/cancel", int64(pid3)), url.Values{"reason": {"إلغاء من الصفحة"}})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/payables") {
		t.Fatalf("HTML cancel: want 303 to /payables, got %d loc %q", code, loc)
	}
	// Cashier may not cancel (route gate).
	cashier := e.cashierClient()
	code, data = e.postJSON(cashier, fmt.Sprintf("/api/payables/%d/cancel", int64(pid3)), map[string]any{"reason": "x"})
	if code != http.StatusForbidden {
		t.Fatalf("cashier cancel: want 403, got %d %v", code, data)
	}
}

func TestFix12Web_H03_CashierSettlesCourierDebt(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	cashier := e.cashierClient()

	code, data := e.postJSON(admin, "/api/courier-debts", map[string]any{
		"courier_name": "مندوب أحمد", "amount": "100.00", "order_ref": "ORD-1",
	})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("create courier debt: want 200 ok, got %d %v", code, data)
	}
	debtID, _ := data["debt_id"].(float64)
	// Cashier settle via the API (cash needs an open shift).
	e.openShift(cashier, 0)
	code, data = e.postJSON(cashier, fmt.Sprintf("/api/courier-debts/%d/settle", int64(debtID)), map[string]any{"amount": "40.00", "method": "Cash"})
	if code != http.StatusOK || data["remaining_cents"] != float64(6000) {
		t.Fatalf("cashier settle courier debt: want 200 remaining 6000, got %d %v", code, data)
	}
	// Recorded as a COLLECTION on the receiving cashier.
	var cbUser, cbKind string
	var cbAmt int64
	if err := e.store.DB.QueryRow(`SELECT UserName,AutoKind,Amount FROM CashBox WHERE MovementType='in' AND Reason LIKE 'سداد دين مندوب%'`).
		Scan(&cbUser, &cbKind, &cbAmt); err != nil {
		t.Fatalf("courier collection CashBox row: %v", err)
	}
	if cbUser != "cashier" || cbKind != "courier_collection" || cbAmt != 4000 {
		t.Fatalf("collection row = user %q kind %q amount %d, want cashier/courier_collection/4000", cbUser, cbKind, cbAmt)
	}
	// The list page shows the settle form to the cashier while the
	// debt is still open (SW-H03 UI affordance).
	code, body := e.get(cashier, "/courier-debts")
	if code != http.StatusOK {
		t.Fatalf("GET /courier-debts as cashier: %d", code)
	}
	if !strings.Contains(body, fmt.Sprintf(`action="/courier-debts/%d/settle"`, int64(debtID))) {
		t.Fatalf("cashier courier list lacks the settle form: %q", truncate(body, 300))
	}
	// Cashier full settle via the HTML form; debt cleared.
	code, loc, _ := e.postFormLoc(cashier, fmt.Sprintf("/courier-debts/%d/settle", int64(debtID)), url.Values{
		"amount": {"60.00"}, "method": {"Visa"},
	})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/courier-debts") {
		t.Fatalf("cashier HTML settle: want 303 to /courier-debts, got %d loc %q", code, loc)
	}
	code, data = e.getJSON(admin, "/api/courier-debts")
	if code != http.StatusOK {
		t.Fatalf("GET /api/courier-debts: %d", code)
	}
	if debts, ok := data["debts"].([]any); ok && len(debts) == 1 {
		if m, ok := debts[0].(map[string]any); ok {
			if m["Status"] != "paid" || m["PaidBy"] != "cashier" {
				t.Fatalf("debt after cashier settle = status %v paidBy %v, want paid/cashier", m["Status"], m["PaidBy"])
			}
		}
	}
	// Cashier still cannot create / edit / delete a courier debt.
	code, _ = e.postJSON(cashier, "/api/courier-debts", map[string]any{"courier_name": "x", "amount": "10.00"})
	if code != http.StatusForbidden {
		t.Fatalf("cashier create courier debt: want 403, got %d", code)
	}
	code, _ = e.postForm(cashier, fmt.Sprintf("/courier-debts/%d/edit", int64(debtID)), url.Values{"courier_name": {"x"}, "amount": {"10.00"}})
	if code != http.StatusForbidden {
		t.Fatalf("cashier edit courier debt: want 403, got %d", code)
	}
	code, _ = e.postForm(cashier, fmt.Sprintf("/courier-debts/%d/delete", int64(debtID)), url.Values{})
	if code != http.StatusForbidden {
		t.Fatalf("cashier delete courier debt: want 403, got %d", code)
	}
}

func TestFix12Web_H04_DeliveryConfirmationBooksCourierDebt(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()

	// Intake one online order: 2× سكر 2000 + 1× زيت 5000 = 9000.
	code, data := e.postJSON(admin, "/api/staff/orders", map[string]any{
		"customer_name": "عميل أونلاين", "customer_phone": "01000000000",
		"items": []map[string]any{
			{"product_id": 1, "name": "سكر", "qty": "2", "unit_price": 2000},
			{"product_id": 2, "name": "زيت", "qty": "1", "unit_price": 5000},
		},
	})
	if code != http.StatusCreated || data["ok"] != true {
		t.Fatalf("create order: want 201 ok, got %d %v", code, data)
	}
	order, _ := data["order"].(map[string]any)
	orderID, _ := order["order_id"].(float64)
	// Advance to READY, then confirm delivery with the courier's name.
	for _, st := range []string{"PREPARED", "READY"} {
		code, data = e.postJSON(admin, fmt.Sprintf("/api/staff/orders/%d/status", int64(orderID)), map[string]any{"status": st})
		if code != http.StatusOK {
			t.Fatalf("order status %s: want 200, got %d %v", st, code, data)
		}
	}
	code, data = e.postJSON(admin, fmt.Sprintf("/api/staff/orders/%d/status", int64(orderID)), map[string]any{
		"status": "DELIVERED", "courier_name": "مندوب التوصيل",
	})
	if code != http.StatusOK {
		t.Fatalf("confirm delivery: want 200, got %d %v", code, data)
	}
	debtID, _ := data["courier_debt_id"].(float64)
	if debtID == 0 {
		t.Fatalf("delivery confirmation booked no courier debt: %v", data)
	}
	// The debt exists with the order amount (9000).
	code, data = e.getJSON(admin, "/api/courier-debts")
	if code != http.StatusOK {
		t.Fatalf("GET /api/courier-debts: %d", code)
	}
	debts, _ := data["debts"].([]any)
	if len(debts) != 1 {
		t.Fatalf("courier debts = %d, want 1", len(debts))
	}
	if m, ok := debts[0].(map[string]any); ok {
		if m["CourierName"] != "مندوب التوصيل" || m["Amount"] != float64(9000) || m["Status"] != "open" {
			t.Fatalf("booked debt wrong: %v", m)
		}
		if m["OrderRef"] != "ONLINE-1" {
			t.Fatalf("OrderRef = %v, want ONLINE-1", m["OrderRef"])
		}
	}
	// Confirming the same delivery again must not book a second debt
	// (the transition itself is refused; the debt count stays 1).
	code, _ = e.postJSON(admin, fmt.Sprintf("/api/staff/orders/%d/status", int64(orderID)), map[string]any{
		"status": "DELIVERED", "courier_name": "مندوب التوصيل",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("second DELIVERED: want 400, got %d", code)
	}
	code, data = e.getJSON(admin, "/api/courier-debts")
	debts, _ = data["debts"].([]any)
	if len(debts) != 1 {
		t.Fatalf("courier debts after 2nd confirm = %d, want still 1", len(debts))
	}
}

func TestFix12Web_H08_M12_PayablesAndCustomersAPIs(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	wh := e.warehouseClient()
	cashier := e.cashierClient()
	sup := e.supClient()

	// Warehouse: payables views are 403 (SW-H08).
	for _, path := range []string{"/payables", "/api/payables", "/api/payables/due", "/payables/alerts"} {
		code, body := e.get(wh, path)
		if code != http.StatusForbidden {
			t.Fatalf("warehouse GET %s: want 403, got %d body=%q", path, code, truncate(body, 200))
		}
		if !strings.Contains(body, "غير مسموح") {
			t.Fatalf("warehouse GET %s: 403 without the uniform denial: %q", path, truncate(body, 200))
		}
	}
	// Cashier and supervisor keep payables access (SW-H08).
	for _, c := range []*http.Client{cashier, sup} {
		for _, path := range []string{"/payables", "/api/payables", "/api/payables/due"} {
			code, _ := e.get(c, path)
			if code != http.StatusOK {
				t.Fatalf("GET %s: want 200, got %d", path, code)
			}
		}
	}
	// Warehouse: /api/customers and /api/debtors are 403, not 500 (SW-M12).
	for _, path := range []string{"/api/customers", "/api/debtors"} {
		code, data := e.getJSON(wh, path)
		if code != http.StatusForbidden {
			t.Fatalf("warehouse GET %s: want 403, got %d %v", path, code, data)
		}
		if msg, _ := data["error"].(string); !strings.Contains(msg, "غير مسموح") {
			t.Fatalf("warehouse GET %s: error %q, want the uniform denial", path, msg)
		}
	}
	// Admin still reads both fine.
	for _, path := range []string{"/api/customers", "/api/debtors", "/api/payables"} {
		code, _ := e.getJSON(admin, path)
		if code != http.StatusOK {
			t.Fatalf("admin GET %s: want 200, got %d", path, code)
		}
	}
}

func TestFix12Web_M01M02_PayableCreateRules(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()

	// SW-M01: no due date is allowed.
	code, data := e.postJSON(admin, "/api/payables", map[string]any{
		"kind": "courier", "party_name": "مندوب", "amount": "100.00", "description": "بدون تاريخ",
	})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("create payable without due date: want 200 ok, got %d %v", code, data)
	}
	// SW-M02: no reason is refused.
	code, data = e.postJSON(admin, "/api/payables", map[string]any{
		"kind": "courier", "party_name": "مندوب", "amount": "100.00", "due_date": "2026-12-01", "description": "",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("create payable without description: want 400, got %d %v", code, data)
	}
	if msg, _ := data["error"].(string); !strings.Contains(msg, "وصف المستحق مطلوب") {
		t.Fatalf("empty-description error = %q, want the reason-required message", msg)
	}
}

func TestFix12Web_M04_GuardedRefusalsRender200(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()

	// Customers page: deleting the default cash customer is refused in
	// the service layer; the message must render with 200, not 500.
	code, body := e.postForm(admin, "/customers/1/delete", url.Values{})
	if code != http.StatusOK {
		t.Fatalf("POST /customers/1/delete: want 200, got %d body=%q", code, truncate(body, 300))
	}
	if !strings.Contains(body, "لا يمكن حذف العميل النقدي الافتراضي") {
		t.Fatalf("customer delete refusal lacks the message: %q", truncate(body, 300))
	}
	// Customers page: a negative payment amount is refused with 200.
	code, data := e.postJSON(admin, "/api/customers", map[string]any{"name": "عميل اختبار"})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("create customer: want 200 ok, got %d %v", code, data)
	}
	cid, _ := data["customer_id"].(float64)
	code, body = e.postForm(admin, fmt.Sprintf("/customers/%d/pay", int64(cid)), url.Values{
		"amount": {"-5"}, "method": {"Cash"},
	})
	if code != http.StatusOK {
		t.Fatalf("POST /customers/%d/pay amount=-5: want 200, got %d body=%q", int64(cid), code, truncate(body, 300))
	}
	if !strings.Contains(body, "المبلغ لا يمكن أن يكون سالباً") {
		t.Fatalf("negative pay refusal lacks the message: %q", truncate(body, 300))
	}
	// Vendors page: deleting a missing vendor renders 200 + message.
	code, body = e.postForm(admin, "/vendors/9999/delete", url.Values{})
	if code != http.StatusOK {
		t.Fatalf("POST /vendors/9999/delete: want 200, got %d body=%q", code, truncate(body, 300))
	}
	if !strings.Contains(body, "المورد غير موجود") {
		t.Fatalf("vendor delete refusal lacks the message: %q", truncate(body, 300))
	}
	// Vendors page: a bad pay amount renders 200 + message.
	code, data = e.postJSON(admin, "/api/vendors", map[string]any{"name": "مورد اختبار"})
	vid, _ := data["vendor_id"].(float64)
	code, body = e.postForm(admin, fmt.Sprintf("/vendors/%d/pay", int64(vid)), url.Values{
		"amount": {"abc"}, "method": {"Visa"},
	})
	if code != http.StatusOK {
		t.Fatalf("POST /vendors/%d/pay amount=abc: want 200, got %d body=%q", int64(vid), code, truncate(body, 300))
	}
	if !strings.Contains(body, "مبلغ غير صحيح") {
		t.Fatalf("vendor pay refusal lacks the message: %q", truncate(body, 300))
	}
}

func TestFix12Web_L01_VendorPayTolerance(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()

	prodID := e.createProduct(admin, "1001", "سكر", "15.00", "100")
	code, data := e.postJSON(admin, "/api/vendors", map[string]any{"name": "مورد السداد"})
	vid, _ := data["vendor_id"].(float64)
	code, data = e.postJSON(admin, "/api/purchases", map[string]any{
		"vendor_id": int64(vid),
		"lines":     []map[string]any{{"product_id": prodID, "qty": "10", "price": "10.00"}},
		"paid": "0", "method": "Visa",
	})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("create purchase: want 200 ok, got %d %v", code, data)
	}
	// Balance is exactly 100.00; paying 100.01 is allowed, balance 0.
	code, data = e.postJSON(admin, fmt.Sprintf("/api/vendors/%d/pay", int64(vid)), map[string]any{"amount": "100.01", "method": "Visa"})
	if code != http.StatusOK || data["balance_after_cents"] != float64(0) {
		t.Fatalf("pay balance + 0.01: want 200 balance 0, got %d %v", code, data)
	}
	// A second vendor: balance + 0.02 is still refused.
	code, data = e.postJSON(admin, "/api/vendors", map[string]any{"name": "مورد السداد 2"})
	vid2, _ := data["vendor_id"].(float64)
	code, data = e.postJSON(admin, "/api/purchases", map[string]any{
		"vendor_id": int64(vid2),
		"lines":     []map[string]any{{"product_id": prodID, "qty": "10", "price": "10.00"}},
		"paid": "0", "method": "Visa",
	})
	if code != http.StatusOK {
		t.Fatalf("create purchase 2: want 200, got %d %v", code, data)
	}
	code, data = e.postJSON(admin, fmt.Sprintf("/api/vendors/%d/pay", int64(vid2)), map[string]any{"amount": "100.02", "method": "Visa"})
	if code != http.StatusBadRequest {
		t.Fatalf("pay balance + 0.02: want 400, got %d %v", code, data)
	}
}

func TestFix12Web_L03_DebtorsDateOnly(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()

	// Fixture: one debtor whose last purchase/payment are datetimes.
	res, err := e.store.DB.Exec(`INSERT INTO Customers(CustomerName,Phone,Address,Balance,CreditLimit,IsActive,CreatedDate)
		VALUES('مدين','01000000000','',0,0,1,?)`, store.Today())
	if err != nil {
		t.Fatal(err)
	}
	cid, _ := res.LastInsertId()
	if _, err := e.store.DB.Exec(`INSERT INTO Invoices(InvoiceNumber,CustomerID,TotalAmount,NetAmount,PaidAmount,RemainingAmount,PaymentMethod,InvoiceDate,IsCanceled)
		VALUES(1,?,10000,10000,0,10000,'Cash','2026-11-22 04:01:28',0)`, cid); err != nil {
		t.Fatal(err)
	}
	code, data := e.getJSON(admin, "/api/debtors")
	if code != http.StatusOK {
		t.Fatalf("GET /api/debtors: %d", code)
	}
	debtors, _ := data["debtors"].([]any)
	if len(debtors) != 1 {
		t.Fatalf("debtors = %d, want 1", len(debtors))
	}
	if m, ok := debtors[0].(map[string]any); ok {
		if lp, _ := m["LastPurchase"].(string); lp != "2026-11-22" {
			t.Fatalf("LastPurchase = %q, want date only 2026-11-22", lp)
		}
	}
	// The HTML grid shows the same date-only value.
	code, body := e.get(admin, "/debtors")
	if code != http.StatusOK {
		t.Fatalf("GET /debtors: %d", code)
	}
	if !strings.Contains(body, "2026-11-22") {
		t.Fatalf("debtors page lacks the date: %q", truncate(body, 300))
	}
	if strings.Contains(body, "2026-11-22 04:01:28") {
		t.Fatal("debtors page still shows the full datetime")
	}
}
