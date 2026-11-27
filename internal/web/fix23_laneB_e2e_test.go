// Package web_test — FIX-23 lane B (R23-C-F1 + R23-C-F2) HTTP tests.
//
// End-to-end over the real server: a poisoned shift / poisoned invoices
// must surface the Arabic overflow message on /dashboard and
// /shifts/monitor (200 error page), never a silent 0.00 expected
// drawer (F1) and never the generic 500 `خطأ في عرض الصفحة` (F2).
package web_test

import (
	"strings"
	"testing"

	"cashierpos-go/internal/store"
)

const fix23E2EPoison = int64(5000000000000000000)

// fix23E2EAdminID returns the seeded admin's user id.
func fix23E2EAdminID(e *e2eEnv) int64 {
	var id int64
	if err := e.store.DB.QueryRow(`SELECT UserID FROM Users WHERE UserName='admin'`).Scan(&id); err != nil {
		e.t.Fatalf("admin id: %v", err)
	}
	return id
}

// TestFix23_DashboardInvoiceOverflowRendersMessage is the F2 live
// shape: two invoices with NetAmount=5e18 each overflow the
// dashboard's SUM(NetAmount); GET /dashboard must answer 200 with
// the Arabic overflow message, not 500.
func TestFix23_DashboardInvoiceOverflowRendersMessage(t *testing.T) {
	e := newEnv(t, false)
	e.seedUsers()
	e.changeAdminPassword()
	admin := e.adminClient()
	adminID := fix23E2EAdminID(e)
	for i := int64(1); i <= 2; i++ {
		if _, err := e.store.DB.Exec(`INSERT INTO Invoices(InvoiceNumber,CustomerID,SaleType,TotalAmount,Discount,
			PromoDiscount,NetAmount,PaidAmount,RemainingAmount,PaymentMethod,UserID,UserName,ShiftID,InvoiceDate,IsCanceled)
			VALUES(?,1,'retail',5000000000000000000,0,0,5000000000000000000,0,0,'Cash',?,'admin',0, ?,0)`,
			i, adminID, store.Now()); err != nil {
			t.Fatalf("insert poison invoice: %v", err)
		}
	}
	code, body := e.get(admin, "/dashboard")
	if code != 200 {
		t.Fatalf("GET /dashboard: status = %d, want 200 (body head %q)", code, truncate(body, 200))
	}
	if !strings.Contains(body, "تجاوز الحد") {
		t.Fatalf("GET /dashboard: body does not carry the Arabic overflow message (head %q)", truncate(body, 300))
	}
	if strings.Contains(body, "خطأ في عرض الصفحة") {
		t.Fatalf("GET /dashboard: body contains the generic 500 page-error text")
	}
}

// TestFix23_MonitorCashBoxOverflowRendersMessage is the F1+F2 live
// shape: 2 CashBox 'in' rows of 5e18 on the open shift overflow the
// drawer SUM; GET /shifts/monitor must answer 200 with the Arabic
// overflow message and must NOT show a silent 0.00 expected drawer.
func TestFix23_MonitorCashBoxOverflowRendersMessage(t *testing.T) {
	e := newEnv(t, false)
	e.seedUsers()
	e.changeAdminPassword()
	admin := e.adminClient()
	adminID := fix23E2EAdminID(e)
	res, err := e.store.DB.Exec(`INSERT INTO Shifts(UserID,UserName,ShiftDate,OpenTime,Status,OpeningBalance,IsOpen,SessionCount)
		VALUES(?, 'admin', ?, ?, 'Open', 10000, 1, 1)`, adminID, store.Today(), store.Now())
	if err != nil {
		t.Fatalf("insert shift: %v", err)
	}
	shiftID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("shift id: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := e.store.DB.Exec(`INSERT INTO CashBox(UserID,UserName,ShiftID,MovementType,Amount,Reason,MovementDate)
			VALUES(?, 'admin', ?, 'in', ?, 'poison-in', ?)`,
			adminID, shiftID, fix23E2EPoison, store.Now()); err != nil {
			t.Fatalf("insert poison CashBox row: %v", err)
		}
	}
	code, body := e.get(admin, "/shifts/monitor")
	if code != 200 {
		t.Fatalf("GET /shifts/monitor: status = %d, want 200 (body head %q)", code, truncate(body, 200))
	}
	if !strings.Contains(body, "تجاوز الحد") {
		t.Fatalf("GET /shifts/monitor: body does not carry the Arabic overflow message (head %q)", truncate(body, 300))
	}
	if strings.Contains(body, "خطأ في عرض الصفحة") {
		t.Fatalf("GET /shifts/monitor: body contains the generic 500 page-error text")
	}
	// The poisoned shift row must not render as a silent 0.00 drawer.
	if strings.Contains(body, "0.00 ج.م</td>") {
		t.Fatalf("GET /shifts/monitor: body still shows a 0.00 expected drawer (head %q)", truncate(body, 300))
	}
}
