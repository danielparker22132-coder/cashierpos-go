// Package web_test — FIX-25 lane B (Observation E, customers/admin/orders):
// no JSON/CSV/HTML store-error branch may echo a raw driver string.
// A SQLite driver error answers 500 with the single generic Arabic
// message; denial stays 403 with the service text, an exact
// "…غير موجود" sentinel stays 404 (400 on the staff orders API, its
// established convention), overflow stays 400 Arabic, and genuine
// business/validation refusals stay 400 with their safe Arabic text.
package web_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"cashierpos-go/internal/store"
)

const fix25BGeneric500 = "حدث خطأ داخلي، حاول مرة أخرى"
const fix25BBig = int64(5000000000000000000)

// fix25BEnv builds a fresh env with working admin/manager/cashier logins.
func fix25BEnv(t *testing.T) (*e2eEnv, *http.Client, *http.Client, *http.Client) {
	t.Helper()
	e := newEnv(t, false)
	e.seedUsers()
	e.changeAdminPassword()
	return e, e.adminClient(), e.managerClient(), e.cashierClient()
}

func fix25BDrop(t *testing.T, e *e2eEnv, table string) {
	t.Helper()
	if _, err := e.store.DB.Exec("DROP TABLE " + table); err != nil {
		t.Fatalf("DROP TABLE %s: %v", table, err)
	}
}

// fix25BAssertGeneric500 fails unless the call answered 500 with exactly
// the generic Arabic message and no raw driver/DB string anywhere.
func fix25BAssertGeneric500(t *testing.T, what string, code int, body map[string]any) {
	t.Helper()
	if code != http.StatusInternalServerError {
		t.Fatalf("%s: status = %d, want 500 (body %v)", what, code, body)
	}
	if body["ok"] != false {
		t.Fatalf("%s: ok = %v, want false (body %v)", what, body["ok"], body)
	}
	errMsg, _ := body["error"].(string)
	if errMsg != fix25BGeneric500 {
		t.Fatalf("%s: error = %q, want generic %q", what, errMsg, fix25BGeneric500)
	}
	fix25BAssertNoRaw(t, what, errMsg)
}

func fix25BAssertNoRaw(t *testing.T, what, s string) {
	t.Helper()
	for _, leak := range []string{"SQL logic error", "no such table", "integer overflow",
		"sqlite", "converting driver", "no such column", "mkdir", "not a directory"} {
		if strings.Contains(s, leak) {
			t.Fatalf("%s: leaks raw string %q in %q", what, leak, s)
		}
	}
}

// fix25BVendor creates a vendor via the API and returns its id.
func fix25BVendor(t *testing.T, e *e2eEnv, c *http.Client, name string) int64 {
	t.Helper()
	code, data := e.postJSON(c, "/api/vendors", map[string]any{"name": name})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("create vendor %q: want 200 ok, got %d %v", name, code, data)
	}
	id, _ := data["vendor_id"].(float64)
	return int64(id)
}

// fix25BPurchase creates a 10x10.00 Visa purchase for a vendor.
func fix25BPurchase(t *testing.T, e *e2eEnv, c *http.Client, vid int64) int64 {
	t.Helper()
	prod := e.createProduct(c, "B25", "سكر", "15.00", "100")
	code, data := e.postJSON(c, "/api/purchases", map[string]any{
		"vendor_id": vid,
		"lines":     []map[string]any{{"product_id": prod, "qty": "10", "price": "10.00"}},
		"paid": "0", "method": "Visa",
	})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("create purchase: want 200 ok, got %d %v", code, data)
	}
	id, _ := data["purchase_id"].(float64)
	return int64(id)
}

// TestFix25B_CustomersDropGeneric500 covers the CreateCustomer,
// PayCustomerDebtGeneral and PayCustomerInvoiceForCustomer legs. The
// pay legs need a debt-carrying customer: with no invoices the
// live-balance check answers "ليس لديه مديونية" before any
// Customers-table query runs, so plant one open invoice first.
func TestFix25B_CustomersDropGeneric500(t *testing.T) {
	e, admin, _, _ := fix25BEnv(t)
	cust := fix17Customer(t, e, admin, map[string]any{"name": "عميل ب25"})
	if _, err := e.store.DB.Exec(`INSERT INTO Invoices(InvoiceNumber,CustomerID,TotalAmount,NetAmount,PaidAmount,RemainingAmount,PaymentMethod,InvoiceDate,IsCanceled)
		VALUES(9001,?,5000,5000,0,5000,'Cash',?,0)`, cust, store.Today()); err != nil {
		t.Fatalf("plant open invoice: %v", err)
	}
	invID := e.queryInt(`SELECT InvoiceID FROM Invoices WHERE InvoiceNumber=9001`)
	fix25BDrop(t, e, "Customers")
	code, body := e.postJSON(admin, "/api/customers", map[string]any{"name": "عميل جديد"})
	fix25BAssertGeneric500(t, "create customer after DROP Customers", code, body)
	code, body = e.postJSON(admin, fmt.Sprintf("/api/customers/%d/pay", cust),
		map[string]any{"amount": "10.00", "method": "Visa"})
	fix25BAssertGeneric500(t, "pay customer debt after DROP Customers", code, body)
	code, body = e.postJSON(admin, fmt.Sprintf("/api/customers/%d/pay-invoice", cust),
		map[string]any{"invoice_id": invID, "amount": "10.00", "method": "Visa"})
	fix25BAssertGeneric500(t, "pay customer invoice after DROP Customers", code, body)
}

// TestFix25B_VendorsDropGeneric500 covers CreateVendor and PayVendor.
func TestFix25B_VendorsDropGeneric500(t *testing.T) {
	e, admin, _, _ := fix25BEnv(t)
	vid := fix25BVendor(t, e, admin, "مورد ب25")
	fix25BDrop(t, e, "Vendors")
	code, body := e.postJSON(admin, "/api/vendors", map[string]any{"name": "مورد جديد"})
	fix25BAssertGeneric500(t, "create vendor after DROP Vendors", code, body)
	code, body = e.postJSON(admin, fmt.Sprintf("/api/vendors/%d/pay", vid),
		map[string]any{"amount": "10.00", "method": "Visa"})
	fix25BAssertGeneric500(t, "pay vendor after DROP Vendors", code, body)
}

// TestFix25B_PurchaseCreateDropGeneric500 covers CreatePurchase.
func TestFix25B_PurchaseCreateDropGeneric500(t *testing.T) {
	e, admin, _, _ := fix25BEnv(t)
	vid := fix25BVendor(t, e, admin, "مورد شراء ب25")
	prod := e.createProduct(admin, "B25P", "أرز", "15.00", "100")
	fix25BDrop(t, e, "PurchaseInvoices")
	code, body := e.postJSON(admin, "/api/purchases", map[string]any{
		"vendor_id": vid,
		"lines":     []map[string]any{{"product_id": prod, "qty": "10", "price": "10.00"}},
		"paid": "0", "method": "Visa",
	})
	fix25BAssertGeneric500(t, "create purchase after DROP PurchaseInvoices", code, body)
}

// TestFix25B_PurchaseGetDropGeneric500AndMissing404 covers apiPurchaseGet:
// dropped table -> 500 generic (was 404 raw); genuinely missing -> 404
// with the Arabic sentinel.
func TestFix25B_PurchaseGetDropGeneric500AndMissing404(t *testing.T) {
	e, admin, _, _ := fix25BEnv(t)
	vid := fix25BVendor(t, e, admin, "مورد جلب ب25")
	pid := fix25BPurchase(t, e, admin, vid)
	code, body := e.getJSON(admin, fmt.Sprintf("/api/purchases/%d", pid))
	if code != http.StatusOK || body["ok"] != true {
		t.Fatalf("purchase GET before DROP: want 200 ok, got %d %v", code, body)
	}
	// Details reference PurchaseInvoices (FK): drop them first.
	fix25BDrop(t, e, "PurchaseInvoiceDetails")
	fix25BDrop(t, e, "PurchaseInvoices")
	code, body = e.getJSON(admin, fmt.Sprintf("/api/purchases/%d", pid))
	fix25BAssertGeneric500(t, "purchase GET after DROP PurchaseInvoices", code, body)

	e2, admin2, _, _ := fix25BEnv(t)
	code, body = e2.getJSON(admin2, "/api/purchases/999999")
	if code != http.StatusNotFound || body["error"] != "فاتورة الشراء غير موجودة" {
		t.Fatalf("missing purchase GET: want 404 فاتورة الشراء غير موجودة, got %d %v", code, body)
	}
}

// TestFix25B_PayablesDropGeneric500 covers CreatePayable, SettlePayable
// and CancelPayable.
func TestFix25B_PayablesDropGeneric500(t *testing.T) {
	e, admin, _, _ := fix25BEnv(t)
	vid := fix25BVendor(t, e, admin, "مورد مستحق ب25")
	code, data := e.postJSON(admin, "/api/payables", map[string]any{
		"kind": "vendor", "party_id": vid, "amount": "100.00",
		"due_date": "2026-12-20", "description": "اختبار ب25",
	})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("create payable: want 200 ok, got %d %v", code, data)
	}
	payID, _ := data["payable_id"].(float64)
	fix25BDrop(t, e, "Payables")
	code, body := e.postJSON(admin, "/api/payables", map[string]any{
		"kind": "vendor", "party_id": vid, "amount": "100.00",
		"due_date": "2026-12-20", "description": "اختبار ب25",
	})
	fix25BAssertGeneric500(t, "create payable after DROP Payables", code, body)
	code, body = e.postJSON(admin, fmt.Sprintf("/api/payables/%d/settle", int64(payID)),
		map[string]any{"amount": "10.00", "method": "Visa"})
	fix25BAssertGeneric500(t, "settle payable after DROP Payables", code, body)
	code, body = e.postJSON(admin, fmt.Sprintf("/api/payables/%d/cancel", int64(payID)),
		map[string]any{"reason": "إلغاء تجريبي"})
	fix25BAssertGeneric500(t, "cancel payable after DROP Payables", code, body)
}

// TestFix25B_CourierDebtsDropGeneric500 covers CreateCourierDebt and
// SettleCourierDebt.
func TestFix25B_CourierDebtsDropGeneric500(t *testing.T) {
	e, admin, _, _ := fix25BEnv(t)
	code, data := e.postJSON(admin, "/api/courier-debts", map[string]any{
		"courier_name": "مندوب ب25", "amount": "50.00",
	})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("create courier debt: want 200 ok, got %d %v", code, data)
	}
	debtID, _ := data["debt_id"].(float64)
	fix25BDrop(t, e, "CourierDebts")
	code, body := e.postJSON(admin, "/api/courier-debts", map[string]any{
		"courier_name": "مندوب جديد", "amount": "50.00",
	})
	fix25BAssertGeneric500(t, "create courier debt after DROP CourierDebts", code, body)
	code, body = e.postJSON(admin, fmt.Sprintf("/api/courier-debts/%d/settle", int64(debtID)),
		map[string]any{"amount": "10.00", "method": "Visa"})
	fix25BAssertGeneric500(t, "settle courier debt after DROP CourierDebts", code, body)
}

// TestFix25B_TransfersDropGeneric500 covers CreateTransfer,
// ApproveTransfer and RejectTransfer.
func TestFix25B_TransfersDropGeneric500(t *testing.T) {
	e, admin, mgr, _ := fix25BEnv(t)
	code, data := e.postJSON(mgr, "/api/transfers", map[string]any{
		"sender_name": "عميل تحويل ب25", "amount_cents": 5000,
		"method": "Vodafone Cash", "reference": "B25-1",
	})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("create transfer: want 200 ok, got %d %v", code, data)
	}
	tid, _ := data["transfer_id"].(float64)
	fix25BDrop(t, e, "ElectronicTransfers")
	code, body := e.postJSON(mgr, "/api/transfers", map[string]any{
		"sender_name": "عميل تحويل", "amount_cents": 5000,
		"method": "Vodafone Cash", "reference": "B25-2",
	})
	fix25BAssertGeneric500(t, "create transfer after DROP ElectronicTransfers", code, body)
	code, body = e.postJSON(mgr, fmt.Sprintf("/api/transfers/%d/approve", int64(tid)),
		map[string]any{"note": "موافقة"})
	fix25BAssertGeneric500(t, "approve transfer after DROP ElectronicTransfers", code, body)
	code, body = e.postJSON(mgr, fmt.Sprintf("/api/transfers/%d/reject", int64(tid)),
		map[string]any{"note": "رفض"})
	fix25BAssertGeneric500(t, "reject transfer after DROP ElectronicTransfers", code, body)
	_ = admin
}

// fix25BCreateOrder intakes one online order and returns its id.
func fix25BCreateOrder(t *testing.T, e *e2eEnv, c *http.Client) int64 {
	t.Helper()
	code, data := e.postJSON(c, "/api/staff/orders", orderBody("عميل ب25", ""))
	if code != http.StatusCreated || data["ok"] != true {
		t.Fatalf("intake order: want 201 ok, got %d %v", code, data)
	}
	o, _ := data["order"].(map[string]any)
	id, _ := o["order_id"].(float64)
	return int64(id)
}

// TestFix25B_OrdersDropGeneric500 covers all seven staff-orders JSON legs.
func TestFix25B_OrdersDropGeneric500(t *testing.T) {
	e, admin, _, cash := fix25BEnv(t)
	oid := fix25BCreateOrder(t, e, admin)
	fix25BDrop(t, e, "OnlineOrders")
	code, body := e.getJSON(cash, "/api/staff/orders/pending-count")
	fix25BAssertGeneric500(t, "pending-count after DROP OnlineOrders", code, body)
	code, body = e.getJSON(cash, "/api/staff/orders/pending-evidence")
	fix25BAssertGeneric500(t, "pending-evidence after DROP OnlineOrders", code, body)
	code, body = e.getJSON(cash, "/api/staff/orders/all")
	fix25BAssertGeneric500(t, "orders all after DROP OnlineOrders", code, body)
	code, body = e.getJSON(cash, fmt.Sprintf("/api/staff/orders/%d", oid))
	fix25BAssertGeneric500(t, "order GET after DROP OnlineOrders", code, body)
	code, body = e.postJSON(admin, "/api/staff/orders", orderBody("عميل جديد", ""))
	fix25BAssertGeneric500(t, "order create after DROP OnlineOrders", code, body)
	code, body = e.postJSON(admin, fmt.Sprintf("/api/staff/orders/%d/resolve-evidence", oid), nil)
	fix25BAssertGeneric500(t, "resolve-evidence after DROP OnlineOrders", code, body)
	code, body = e.postJSON(admin, fmt.Sprintf("/api/staff/orders/%d/status", oid),
		map[string]any{"status": "PREPARED"})
	fix25BAssertGeneric500(t, "order set-status after DROP OnlineOrders", code, body)
}

// TestFix25B_AdminDropGeneric500 covers the five admin raw-echo sites:
// ExportReportCSV (CSV, 500 plain text), SalesReportFor,
// MarkNotificationRead, UnreadAuditAlertsCount, ListBackups (denial
// guard only — ListBackups cannot return a driver error by
// construction, see the lane report).
func TestFix25B_AdminDropGeneric500(t *testing.T) {
	e, admin, _, _ := fix25BEnv(t)
	code, body := e.getJSON(admin, "/api/reports/sales?from="+store.Today()+"&to="+store.Today())
	if code != http.StatusOK || body["ok"] != true {
		t.Fatalf("sales report before DROP: want 200 ok, got %d %v", code, body)
	}
	fix25BDrop(t, e, "Invoices")
	code, body = e.getJSON(admin, "/api/reports/sales?from="+store.Today()+"&to="+store.Today())
	fix25BAssertGeneric500(t, "sales report after DROP Invoices", code, body)

	fix25BDrop(t, e, "AuditAlerts")
	code, body = e.getJSON(admin, "/api/audit/unread-count")
	fix25BAssertGeneric500(t, "audit unread-count after DROP AuditAlerts", code, body)

	fix25BDrop(t, e, "Notifications")
	code, body = e.postJSON(admin, "/api/notifications/1/read", nil)
	fix25BAssertGeneric500(t, "notification read after DROP Notifications", code, body)

	// CSV export: plain-text 500 with exactly the generic message.
	e2, admin2, _, _ := fix25BEnv(t)
	st, raw := e2.get(admin2, "/reports/export?kind=sales&from="+store.Today()+"&to="+store.Today())
	if st != http.StatusOK {
		t.Fatalf("CSV export before DROP: want 200, got %d body=%q", st, truncate(raw, 200))
	}
	fix25BDrop(t, e2, "Invoices")
	st, raw = e2.get(admin2, "/reports/export?kind=sales&from="+store.Today()+"&to="+store.Today())
	if st != http.StatusInternalServerError {
		t.Fatalf("CSV export after DROP Invoices: want 500, got %d body=%q", st, truncate(raw, 200))
	}
	if strings.TrimSpace(raw) != fix25BGeneric500 {
		t.Fatalf("CSV export after DROP: body = %q, want exactly generic %q", truncate(raw, 200), fix25BGeneric500)
	}
	fix25BAssertNoRaw(t, "CSV export after DROP", raw)

	// ListBackups: admin 200, cashier 403 (denial guard).
	e3, admin3, _, cash3 := fix25BEnv(t)
	code, body = e3.getJSON(admin3, "/api/backup/list")
	if code != http.StatusOK || body["ok"] != true {
		t.Fatalf("backup list admin: want 200 ok, got %d %v", code, body)
	}
	code, body = e3.getJSON(cash3, "/api/backup/list")
	if code != http.StatusForbidden {
		t.Fatalf("backup list cashier: want 403, got %d %v", code, body)
	}
}

// TestFix25B_Guards keeps the guarded semantics: genuine validation
// errors stay 400 with their safe text, missing entities keep their
// Arabic sentinel (404 on customers/admin, 400 on staff orders), a
// denial stays 403, and an overflow stays 400 Arabic.
func TestFix25B_Guards(t *testing.T) {
	e, admin, mgr, cash := fix25BEnv(t)

	// Validation: empty customer name.
	code, body := e.postJSON(admin, "/api/customers", map[string]any{"name": ""})
	if code != http.StatusBadRequest || body["error"] != "اسم العميل مطلوب" {
		t.Fatalf("empty customer name: want 400 اسم العميل مطلوب, got %d %v", code, body)
	}
	// Validation: bad payable kind.
	code, body = e.postJSON(admin, "/api/payables", map[string]any{
		"kind": "weird", "party_id": 1, "amount": "10.00", "description": "x",
	})
	if code != http.StatusBadRequest || !strings.Contains(body["error"].(string), "نوع المستحق غير صحيح") {
		t.Fatalf("bad payable kind: want 400 نوع المستحق غير صحيح, got %d %v", code, body)
	}
	// Validation: bad payment method on a purchase.
	vid := fix25BVendor(t, e, admin, "مورد تحقق ب25")
	prod := e.createProduct(admin, "B25G", "سكر", "15.00", "100")
	code, body = e.postJSON(admin, "/api/purchases", map[string]any{
		"vendor_id": vid,
		"lines":     []map[string]any{{"product_id": prod, "qty": "1", "price": "10.00"}},
		"paid": "0", "method": "Bitcoin",
	})
	if code != http.StatusBadRequest || body["error"] != "طريقة الدفع غير صحيحة" {
		t.Fatalf("bad purchase method: want 400 طريقة الدفع غير صحيحة, got %d %v", code, body)
	}
	// Missing customer on pay: the live-balance check runs first and a
	// missing customer has zero balance, so the refusal is the safe
	// Arabic "هذا العميل ليس لديه مديونية" at 400 (the "العميل غير
	// موجود" sentinel inside applyDebtPaymentTx is unreachable here).
	code, body = e.postJSON(admin, "/api/customers/999999/pay",
		map[string]any{"amount": "10.00", "method": "Visa"})
	if code != http.StatusBadRequest || body["error"] != "هذا العميل ليس لديه مديونية" {
		t.Fatalf("pay missing customer: want 400 هذا العميل ليس لديه مديونية, got %d %v", code, body)
	}
	code, body = e.postJSON(admin, "/api/payables/999999/settle",
		map[string]any{"amount": "10.00", "method": "Visa"})
	if code != http.StatusNotFound || body["error"] != "المستحق غير موجود" {
		t.Fatalf("settle missing payable: want 404 المستحق غير موجود, got %d %v", code, body)
	}
	code, body = e.postJSON(admin, "/api/notifications/999999/read", nil)
	if code != http.StatusNotFound || body["error"] != "التنبيه غير موجود" {
		t.Fatalf("read missing notification: want 404 التنبيه غير موجود, got %d %v", code, body)
	}
	// Missing order on the staff API: 400 with the sentinel (established
	// convention, re-review 25 lane C O2).
	code, body = e.getJSON(cash, "/api/staff/orders/999999")
	if code != http.StatusBadRequest || body["error"] != "الطلب غير موجود" {
		t.Fatalf("missing order GET: want 400 الطلب غير موجود, got %d %v", code, body)
	}
	// Orders validation: bad status, bad total, resolve on a fresh order.
	oid := fix25BCreateOrder(t, e, admin)
	code, body = e.postJSON(admin, fmt.Sprintf("/api/staff/orders/%d/status", oid),
		map[string]any{"status": "NOPE"})
	if code != http.StatusBadRequest || body["error"] != "حالة الطلب غير صحيحة" {
		t.Fatalf("bad order status: want 400 حالة الطلب غير صحيحة, got %d %v", code, body)
	}
	code, body = e.postJSON(admin, "/api/staff/orders", map[string]any{
		"customer_name": "عميل", "items": []map[string]any{
			{"product_id": 1, "name": "سكر", "qty": "2", "unit_price": 2000},
		}, "total": 12345,
	})
	if code != http.StatusBadRequest || body["error"] != "إجمالي الطلب لا يطابق مجموع الأصناف" {
		t.Fatalf("order total mismatch: want 400 إجمالي الطلب لا يطابق مجموع الأصناف, got %d %v", code, body)
	}
	code, body = e.postJSON(admin, fmt.Sprintf("/api/staff/orders/%d/resolve-evidence", oid), nil)
	if code != http.StatusBadRequest || body["error"] != "هذا الطلب ليس في انتظار مراجعة دليل الدفع" {
		t.Fatalf("resolve fresh order: want 400 هذا الطلب ليس في انتظار مراجعة دليل الدفع, got %d %v", code, body)
	}
	// Denial: warehouse may not create customers (cashier CAN — the
	// matrix grants cashiers manage_customers — so the denial probe
	// uses warehouse, not cashier).
	wh := e.warehouseClient()
	code, body = e.postJSON(wh, "/api/customers", map[string]any{"name": "ممنوع"})
	if code != http.StatusForbidden {
		t.Fatalf("warehouse create customer: want 403, got %d %v", code, body)
	}
	// Parse errors keep 400 with safe text.
	code, body = e.postJSON(admin, "/api/customers/abc/pay",
		map[string]any{"amount": "10.00", "method": "Visa"})
	if code != http.StatusBadRequest || body["error"] != "معرّف غير صحيح" {
		t.Fatalf("bad path id: want 400 معرّف غير صحيح, got %d %v", code, body)
	}
	// Overflow: poisoned customer balance -> 400 Arabic, no raw string.
	cust := fix17Customer(t, e, admin, map[string]any{"name": "عميل مسموم ب25"})
	for i := 1; i <= 2; i++ {
		if _, err := e.store.DB.Exec(`INSERT INTO Invoices(InvoiceNumber,CustomerID,TotalAmount,NetAmount,PaidAmount,RemainingAmount,PaymentMethod,InvoiceDate,IsCanceled)
			VALUES(?,?,?, ?,0,?,'Cash',?,0)`, 9000+i, cust, fix25BBig, fix25BBig, fix25BBig, store.Today()); err != nil {
			t.Fatalf("plant poison invoice: %v", err)
		}
	}
	code, body = e.postJSON(admin, fmt.Sprintf("/api/customers/%d/pay", cust),
		map[string]any{"amount": "10.00", "method": "Visa"})
	if code != http.StatusBadRequest {
		t.Fatalf("poisoned pay: want 400, got %d %v", code, body)
	}
	errMsg, _ := body["error"].(string)
	if !strings.Contains(errMsg, "تجاوز الحد") {
		t.Fatalf("poisoned pay: error = %q, want Arabic overflow", errMsg)
	}
	fix25BAssertNoRaw(t, "poisoned pay", errMsg)
	_ = mgr
}

// TestFix25B_HTMLResolveDropGeneric500AndBusinessRefusal covers the HTML
// resolve-evidence form: dropped table -> 500 with the generic message
// in the page; a business refusal stays 400 with its safe text.
func TestFix25B_HTMLResolveDropGeneric500AndBusinessRefusal(t *testing.T) {
	e, admin, _, _ := fix25BEnv(t)
	oid := fix25BCreateOrder(t, e, admin)
	fix25BDrop(t, e, "OnlineOrders")
	code, body := e.postForm(admin, fmt.Sprintf("/orders/%d/resolve-evidence", oid), url.Values{})
	if code != http.StatusInternalServerError {
		t.Fatalf("HTML resolve after DROP OnlineOrders: want 500, got %d body=%q", code, truncate(body, 300))
	}
	if !strings.Contains(body, fix25BGeneric500) {
		t.Fatalf("HTML resolve after DROP: body lacks generic message: %q", truncate(body, 300))
	}
	fix25BAssertNoRaw(t, "HTML resolve after DROP", body)

	// Business refusal (cancelled order) stays 400 with its safe text.
	e2, admin2, _, _ := fix25BEnv(t)
	// Use an awaiting-verification order, mirroring TestFix6.
	code2, data2 := e2.postJSON(admin2, "/api/staff/orders", orderBody("عميل دليل ملغى", "AWAITING_VERIFICATION"))
	if code2 != http.StatusCreated {
		t.Fatalf("intake evidence order: want 201, got %d %v", code2, data2)
	}
	o2, _ := data2["order"].(map[string]any)
	oid3 := int64(o2["order_id"].(float64))
	code2, _ = e2.postJSON(admin2, fmt.Sprintf("/api/staff/orders/%d/status", oid3),
		map[string]any{"status": "CANCELED"})
	if code2 != http.StatusOK {
		t.Fatalf("cancel evidence order: want 200, got %d", code2)
	}
	code, body = e2.postForm(admin2, fmt.Sprintf("/orders/%d/resolve-evidence", oid3), url.Values{})
	if code != http.StatusBadRequest {
		t.Fatalf("HTML resolve on cancelled order: want 400, got %d body=%q", code, truncate(body, 300))
	}
	if !strings.Contains(body, "لا يمكن مراجعة دليل دفع طلب ملغى") {
		t.Fatalf("HTML resolve 400 body lacks the refusal message: %q", truncate(body, 300))
	}
	// Missing order on the HTML form: 400 with the sentinel.
	code, body = e2.postForm(admin2, "/orders/999999/resolve-evidence", url.Values{})
	if code != http.StatusBadRequest {
		t.Fatalf("HTML resolve missing order: want 400, got %d body=%q", code, truncate(body, 300))
	}
	if !strings.Contains(body, "الطلب غير موجود") {
		t.Fatalf("HTML resolve missing order lacks the sentinel: %q", truncate(body, 300))
	}
}

// TestFix25B_CourierDebtErrorGeneric covers the adjacent courier_debt_error
// echo in the set-status success body: dropped CourierDebts -> 200 with
// the generic message; table intact -> a real debt id, no error field.
func TestFix25B_CourierDebtErrorGeneric(t *testing.T) {
	e, admin, _, _ := fix25BEnv(t)
	oid := fix25BCreateOrder(t, e, admin)
	for _, st := range []string{"PREPARED", "READY"} {
		code, _ := e.postJSON(admin, fmt.Sprintf("/api/staff/orders/%d/status", oid),
			map[string]any{"status": st})
		if code != http.StatusOK {
			t.Fatalf("advance to %s: want 200, got %d", st, code)
		}
	}
	fix25BDrop(t, e, "CourierDebts")
	code, body := e.postJSON(admin, fmt.Sprintf("/api/staff/orders/%d/status", oid),
		map[string]any{"status": "DELIVERED", "courier_name": "أحمد"})
	if code != http.StatusOK || body["ok"] != true {
		t.Fatalf("deliver after DROP CourierDebts: want 200 ok, got %d %v", code, body)
	}
	if body["courier_debt_error"] != "تعذّر تسجيل دين المندوب" {
		t.Fatalf("courier_debt_error = %v, want generic تعذّر تسجيل دين المندوب", body["courier_debt_error"])
	}
	fix25BAssertNoRaw(t, "courier_debt_error", fmt.Sprint(body["courier_debt_error"]))

	e2, admin2, _, _ := fix25BEnv(t)
	oid2 := fix25BCreateOrder(t, e2, admin2)
	for _, st := range []string{"PREPARED", "READY"} {
		code, _ := e2.postJSON(admin2, fmt.Sprintf("/api/staff/orders/%d/status", oid2),
			map[string]any{"status": st})
		if code != http.StatusOK {
			t.Fatalf("control advance to %s: want 200, got %d", st, code)
		}
	}
	code, body = e2.postJSON(admin2, fmt.Sprintf("/api/staff/orders/%d/status", oid2),
		map[string]any{"status": "DELIVERED", "courier_name": "أحمد"})
	if code != http.StatusOK || body["ok"] != true {
		t.Fatalf("control deliver: want 200 ok, got %d %v", code, body)
	}
	if _, bad := body["courier_debt_error"]; bad {
		t.Fatalf("control deliver: unexpected courier_debt_error %v", body["courier_debt_error"])
	}
	if body["courier_debt_id"] == nil {
		t.Fatalf("control deliver: no courier_debt_id in %v", body)
	}
}
