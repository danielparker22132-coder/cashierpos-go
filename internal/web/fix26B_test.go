// Package web_test — FIX-26 lane B (R26-B-O1, customers/admin/orders HTML):
// no HTML error render may echo a raw driver string. A SQLite driver
// error answers 200 with the single generic Arabic message (the accepted
// "HTML form refusals render 200" residual covers the status, not the
// content); safe Arabic business/validation text is unchanged.
package web_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"cashierpos-go/internal/store"
)

const fix26BGeneric = "حدث خطأ داخلي، حاول مرة أخرى"

// fix26BAssertGeneric200 fails unless the call answered 200 with the
// generic Arabic message and no raw driver/DB string anywhere.
func fix26BAssertGeneric200(t *testing.T, what string, code int, body string) {
	t.Helper()
	if code != http.StatusOK {
		t.Fatalf("%s: status = %d, want 200 (body %q)", what, code, truncate(body, 300))
	}
	// Raw-string check first: on a gate revert this failure names the
	// exact leaked driver string instead of only "lacks generic".
	fix25BAssertNoRaw(t, what, body)
	if !strings.Contains(body, fix26BGeneric) {
		t.Fatalf("%s: body lacks generic message: %q", what, truncate(body, 300))
	}
}

// fix26BAssertGuard fails unless the call answered wantStatus with the
// safe Arabic text and no raw driver string.
func fix26BAssertGuard(t *testing.T, what string, code int, body string, wantStatus int, wantText string) {
	t.Helper()
	if code != wantStatus {
		t.Fatalf("%s: status = %d, want %d (body %q)", what, code, wantStatus, truncate(body, 300))
	}
	if !strings.Contains(body, wantText) {
		t.Fatalf("%s: body lacks %q: %q", what, wantText, truncate(body, 300))
	}
	fix25BAssertNoRaw(t, what, body)
}

func TestFix26B_CustomersHTMLDropGeneric200(t *testing.T) {
	e, admin, _, _ := fix25BEnv(t)
	cust := fix17Customer(t, e, admin, map[string]any{"name": "عميل ب26"})
	if _, err := e.store.DB.Exec(`INSERT INTO Invoices(InvoiceNumber,CustomerID,TotalAmount,NetAmount,PaidAmount,RemainingAmount,PaymentMethod,InvoiceDate,IsCanceled)
		VALUES(9002,?,5000,5000,0,5000,'Cash',?,0)`, cust, store.Today()); err != nil {
		t.Fatalf("plant open invoice: %v", err)
	}
	e.openShift(admin, 0)
	fix25BDrop(t, e, "Customers")

	code, body := e.postForm(admin, "/customers/new", url.Values{
		"name": {"عميل"}, "phone": {""}, "address": {""}, "credit_limit": {"0"},
	})
	fix26BAssertGeneric200(t, "POST /customers/new after DROP Customers", code, body)

	code, body = e.get(admin, "/customers/1")
	fix26BAssertGeneric200(t, "GET /customers/1 after DROP Customers", code, body)

	code, body = e.postForm(admin, "/customers/1/edit", url.Values{
		"name": {"عميل"}, "phone": {""}, "address": {""}, "credit_limit": {"0"}, "is_active": {"1"},
	})
	fix26BAssertGeneric200(t, "POST /customers/1/edit after DROP Customers", code, body)

	code, body = e.get(admin, "/customers/1/edit")
	fix26BAssertGeneric200(t, "GET /customers/1/edit after DROP Customers", code, body)

	code, body = e.get(admin, "/customers/1/statement")
	fix26BAssertGeneric200(t, "GET /customers/1/statement after DROP Customers", code, body)

	code, body = e.postForm(admin, fmt.Sprintf("/customers/%d/pay", cust), url.Values{
		"amount": {"10.00"}, "method": {"Cash"}, "notes": {""},
	})
	fix26BAssertGeneric200(t, "POST /customers/id/pay after DROP Customers", code, body)

	code, body = e.postForm(admin, fmt.Sprintf("/customers/%d/delete", cust), url.Values{})
	fix26BAssertGeneric200(t, "POST /customers/id/delete after DROP Customers", code, body)
}

func TestFix26B_VendorsPurchasesHTMLDropGeneric200(t *testing.T) {
	e, admin, _, _ := fix25BEnv(t)
	vid := fix25BVendor(t, e, admin, "مورد ب26")
	fix25BDrop(t, e, "Vendors")

	code, body := e.postForm(admin, "/vendors/new", url.Values{
		"name": {"مورد"}, "phone": {""}, "address": {""}, "notes": {""}, "credit_limit": {"0"},
	})
	fix26BAssertGeneric200(t, "POST /vendors/new after DROP Vendors", code, body)

	code, body = e.get(admin, fmt.Sprintf("/vendors/%d", vid))
	fix26BAssertGeneric200(t, "GET /vendors/id after DROP Vendors", code, body)

	code, body = e.postForm(admin, fmt.Sprintf("/vendors/%d/pay", vid), url.Values{
		"amount": {"10.00"}, "method": {"Cash"}, "notes": {""},
	})
	fix26BAssertGeneric200(t, "POST /vendors/id/pay after DROP Vendors", code, body)

	code, body = e.postForm(admin, fmt.Sprintf("/vendors/%d/edit", vid), url.Values{
		"name": {"مورد"}, "phone": {""}, "address": {""}, "notes": {""}, "credit_limit": {"0"}, "is_active": {"1"},
	})
	fix26BAssertGeneric200(t, "POST /vendors/id/edit after DROP Vendors", code, body)

	code, body = e.postForm(admin, fmt.Sprintf("/vendors/%d/delete", vid), url.Values{})
	fix26BAssertGeneric200(t, "POST /vendors/id/delete after DROP Vendors", code, body)

	code, body = e.get(admin, fmt.Sprintf("/vendors/%d/ledger", vid))
	fix26BAssertGeneric200(t, "GET /vendors/id/ledger after DROP Vendors", code, body)

	// Purchases need their own env: Vendors must stay intact or
	// CreatePurchase fails on Vendors first.
	e2, admin2, _, _ := fix25BEnv(t)
	vid2 := fix25BVendor(t, e2, admin2, "مورد ب26ج")
	prod := e2.createProduct(admin2, "B26P", "سكر", "15.00", "100")
	pur2 := fix25BPurchase(t, e2, admin2, vid2)
	// DROP TABLE is FK-blocked while the planted purchase's child rows
	// exist: clear them (and the invoice row) first, then drop.
	for _, q := range []string{
		"DELETE FROM PurchaseInvoiceDetails WHERE PurchaseID=?",
		"DELETE FROM VendorPayments WHERE PurchaseID=?",
		"DELETE FROM ProductBatches WHERE PurchaseID=?",
		"DELETE FROM PurchaseInvoices WHERE PurchaseID=?",
	} {
		if _, err := e2.store.DB.Exec(q, pur2); err != nil {
			t.Fatalf("clear planted purchase: %v", err)
		}
	}
	fix25BDrop(t, e2, "PurchaseInvoices")

	code, body = e2.get(admin2, fmt.Sprintf("/purchases/%d", pur2))
	fix26BAssertGeneric200(t, "GET /purchases/id after DROP PurchaseInvoices", code, body)

	code, body = e2.postForm(admin2, "/purchases/new", url.Values{
		"vendor_id": {fmt.Sprint(vid2)}, "paid": {"0"}, "method": {"Cash"},
		"product_id": {fmt.Sprint(prod)}, "qty": {"1"}, "price": {"10.00"},
	})
	fix26BAssertGeneric200(t, "POST /purchases/new after DROP PurchaseInvoices", code, body)
}

func TestFix26B_PayablesCourierTransfersHTMLDropGeneric200(t *testing.T) {
	e, admin, _, _ := fix25BEnv(t)
	fix25BDrop(t, e, "Payables")

	code, body := e.postForm(admin, "/payables/new", url.Values{
		"amount": {"100.00"}, "kind": {"courier"}, "party_name": {"مندوب"},
		"due_date": {"2026-12-20"}, "description": {"اختبار"},
	})
	fix26BAssertGeneric200(t, "POST /payables/new after DROP Payables", code, body)

	code, body = e.postForm(admin, "/payables/1/settle", url.Values{
		"amount": {"10.00"}, "method": {"Cash"}, "notes": {""},
	})
	fix26BAssertGeneric200(t, "POST /payables/1/settle after DROP Payables", code, body)

	code, body = e.postForm(admin, "/payables/1/cancel", url.Values{"reason": {"x"}})
	fix26BAssertGeneric200(t, "POST /payables/1/cancel after DROP Payables", code, body)

	e2, admin2, _, _ := fix25BEnv(t)
	fix25BDrop(t, e2, "CourierDebts")

	code, body = e2.postForm(admin2, "/courier-debts/new", url.Values{
		"amount": {"100.00"}, "courier_name": {"مندوب"}, "order_ref": {""}, "invoice_number": {""},
	})
	fix26BAssertGeneric200(t, "POST /courier-debts/new after DROP CourierDebts", code, body)

	code, body = e2.get(admin2, "/courier-debts/1/edit")
	fix26BAssertGeneric200(t, "GET /courier-debts/1/edit after DROP CourierDebts", code, body)

	code, body = e2.postForm(admin2, "/courier-debts/1/edit", url.Values{
		"amount": {"100.00"}, "courier_name": {"مندوب"}, "order_ref": {""}, "invoice_number": {""},
	})
	fix26BAssertGeneric200(t, "POST /courier-debts/1/edit after DROP CourierDebts", code, body)

	code, body = e2.postForm(admin2, "/courier-debts/1/settle", url.Values{
		"amount": {"10.00"}, "method": {"Cash"}, "notes": {""},
	})
	fix26BAssertGeneric200(t, "POST /courier-debts/1/settle after DROP CourierDebts", code, body)

	code, body = e2.postForm(admin2, "/courier-debts/1/delete", url.Values{})
	fix26BAssertGeneric200(t, "POST /courier-debts/1/delete after DROP CourierDebts", code, body)

	e3, admin3, _, _ := fix25BEnv(t)
	fix25BDrop(t, e3, "ElectronicTransfers")

	code, body = e3.postForm(admin3, "/transfers/new", url.Values{
		"amount": {"100.00"}, "sender_name": {"مرسل"}, "method": {"Vodafone Cash"}, "reference": {"x"},
	})
	fix26BAssertGeneric200(t, "POST /transfers/new after DROP ElectronicTransfers", code, body)

	code, body = e3.postForm(admin3, "/transfers/1/approve", url.Values{"note": {"ok"}})
	fix26BAssertGeneric200(t, "POST /transfers/1/approve after DROP ElectronicTransfers", code, body)

	code, body = e3.postForm(admin3, "/transfers/1/reject", url.Values{"note": {"no"}})
	fix26BAssertGeneric200(t, "POST /transfers/1/reject after DROP ElectronicTransfers", code, body)
}

func TestFix26B_AdminHTMLDropGeneric200(t *testing.T) {
	e, admin, _, _ := fix25BEnv(t)
	fix25BDrop(t, e, "Notifications")
	code, body := e.get(admin, "/notifications")
	fix26BAssertGeneric200(t, "GET /notifications after DROP Notifications", code, body)

	e2, admin2, _, _ := fix25BEnv(t)
	fix25BDrop(t, e2, "AuditLog")
	code, body = e2.get(admin2, "/audit")
	fix26BAssertGeneric200(t, "GET /audit after DROP AuditLog", code, body)

	e3, admin3, _, _ := fix25BEnv(t)
	fix25BDrop(t, e3, "Shifts")
	code, body = e3.get(admin3, "/shifts/monitor")
	fix26BAssertGeneric200(t, "GET /shifts/monitor after DROP Shifts", code, body)

	e4, admin4, _, _ := fix25BEnv(t)
	fix25BDrop(t, e4, "UserAttendance")
	code, body = e4.get(admin4, "/attendance")
	fix26BAssertGeneric200(t, "GET /attendance after DROP UserAttendance", code, body)

	e5, admin5, _, _ := fix25BEnv(t)
	fix25BDrop(t, e5, "AppSettings")
	code, body = e5.postForm(admin5, "/settings/profile", url.Values{
		"store_name": {"محل"}, "store_phone": {""}, "store_address": {""},
	})
	fix26BAssertGeneric200(t, "POST /settings/profile after DROP AppSettings", code, body)

	e6, admin6, _, _ := fix25BEnv(t)
	fix25BDrop(t, e6, "ScaleSettings")
	code, body = e6.postForm(admin6, "/settings/scale/test", url.Values{"barcode": {"1234567890123"}})
	fix26BAssertGeneric200(t, "POST /settings/scale-test after DROP ScaleSettings", code, body)

	e7, admin7, _, _ := fix25BEnv(t)
	fix25BDrop(t, e7, "Invoices")
	code, body = e7.get(admin7, "/reports/sales?from=2026-11-01&to=2026-11-27")
	fix26BAssertGeneric200(t, "GET /reports/sales after DROP Invoices", code, body)

	code, body = e7.get(admin7, "/dashboard")
	fix26BAssertGeneric200(t, "GET /dashboard after DROP Invoices", code, body)
}

func TestFix26B_OrdersPageDropGeneric200(t *testing.T) {
	e, admin, _, _ := fix25BEnv(t)
	fix25BDrop(t, e, "OnlineOrders")
	code, body := e.get(admin, "/orders")
	fix26BAssertGeneric200(t, "GET /orders after DROP OnlineOrders", code, body)
}

func TestFix26B_Guards(t *testing.T) {
	e, admin, _, _ := fix25BEnv(t)

	code, body := e.postForm(admin, "/customers/new", url.Values{
		"name": {""}, "phone": {""}, "address": {""}, "credit_limit": {"0"},
	})
	fix26BAssertGuard(t, "POST /customers/new empty name", code, body, 200, "اسم العميل مطلوب")

	code, body = e.postForm(admin, "/customers/new", url.Values{
		"name": {"عميل"}, "phone": {""}, "address": {""}, "credit_limit": {"abc"},
	})
	fix26BAssertGuard(t, "POST /customers/new bad money", code, body, 200, "مبلغ غير صحيح")

	code, body = e.postForm(admin, "/vendors/new", url.Values{
		"name": {""}, "phone": {""}, "address": {""}, "notes": {""}, "credit_limit": {"0"},
	})
	fix26BAssertGuard(t, "POST /vendors/new empty name", code, body, 200, "اسم المورد مطلوب")

	code, body = e.get(admin, "/customers/999999")
	fix26BAssertGuard(t, "GET /customers/999999 missing", code, body, 200, "العميل غير موجود")

	// Intact pages: 200, no generic error, no raw string.
	code, body = e.get(admin, "/orders")
	if code != http.StatusOK || strings.Contains(body, fix26BGeneric) {
		t.Fatalf("GET /orders intact: want 200 without generic message, got %d body=%q", code, truncate(body, 300))
	}
	fix25BAssertNoRaw(t, "GET /orders intact", body)

	code, body = e.get(admin, "/dashboard")
	if code != http.StatusOK || strings.Contains(body, fix26BGeneric) {
		t.Fatalf("GET /dashboard intact: want 200 without generic message, got %d body=%q", code, truncate(body, 300))
	}
	fix25BAssertNoRaw(t, "GET /dashboard intact", body)
}
