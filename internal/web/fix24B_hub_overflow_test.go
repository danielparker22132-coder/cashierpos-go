// Package web_test — FIX-24 lane B (R24-C-N1) HTTP tests.
//
// R24-C-N1: the hub summary SUM(NetAmount) was swallowed with `_ =`
// (routes.go:311 day scope, :319 shift scope), so a SUM overflow left
// the totals at zero and the hub rendered 0.00 with no message —
// while /dashboard on the same DB correctly surfaced `تجاوز الحد`.
// The fix surfaces the SUM error like Dashboard does: the sales box
// shows the Arabic overflow message instead of 0.00, and the page
// error banner carries it too. Both legs (day + shift) are covered
// here, plus the normal path (totals must render exactly as before).
package web_test

import (
	"regexp"
	"strings"
	"testing"

	"cashierpos-go/internal/money"
	"cashierpos-go/internal/store"
)

const fix24BPoison = int64(5000000000000000000)

// fix24BInsertInvoice inserts one invoice directly (legacy/poison rows
// cannot be created through the API, which refuses overflowing nets).
func fix24BInsertInvoice(e *e2eEnv, num int64, net int64, userID int64, userName string, shiftID int64) {
	e.t.Helper()
	if _, err := e.store.DB.Exec(`INSERT INTO Invoices(InvoiceNumber,CustomerID,SaleType,TotalAmount,Discount,
			PromoDiscount,NetAmount,PaidAmount,RemainingAmount,PaymentMethod,UserID,UserName,ShiftID,InvoiceDate,IsCanceled)
			VALUES(?,1,'retail',?,0,0,?,0,0,'Cash',?,?,?, ?,0)`,
		num, net, net, userID, userName, shiftID, store.Now()); err != nil {
		e.t.Fatalf("insert invoice %d: %v", num, err)
	}
}

// fix24BOpenShift inserts an open shift for the user today and returns it.
func fix24BOpenShift(e *e2eEnv, userID int64, userName string) int64 {
	e.t.Helper()
	res, err := e.store.DB.Exec(`INSERT INTO Shifts(UserID,UserName,ShiftDate,OpenTime,Status,OpeningBalance,IsOpen,SessionCount)
		VALUES(?, ?, ?, ?, 'Open', 10000, 1, 1)`, userID, userName, store.Today(), store.Now())
	if err != nil {
		e.t.Fatalf("insert shift: %v", err)
	}
	shiftID, err := res.LastInsertId()
	if err != nil {
		e.t.Fatalf("shift id: %v", err)
	}
	return shiftID
}

var fix24BDaySalesRe = regexp.MustCompile(`مبيعات اليوم</span><span class="summary-value num">([^<]*)</span>`)
var fix24BShiftSalesRe = regexp.MustCompile(`مبيعات شيفتي</span><span class="summary-value num">([^<]*)</span>`)
var fix24BDayCountRe = regexp.MustCompile(`عدد فواتير اليوم</span><span class="summary-value num">([^<]*)</span>`)
var fix24BShiftCountRe = regexp.MustCompile(`فواتير شيفتي</span><span class="summary-value num">([^<]*)</span>`)
var fix24BPageErrRe = regexp.MustCompile(`<div class="alert err" role="alert">([^<]*)</div>`)

func fix24BExtract(re *regexp.Regexp, body string) string {
	m := re.FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// TestFix24B_HubDayOverflowRendersMessage is the day-scope leg
// (routes.go:311): two 5e18-net invoices today overflow the day SUM;
// GET / as admin must answer 200 with the Arabic overflow message in
// the sales box (not a silent 0.00) and in the page error banner.
func TestFix24B_HubDayOverflowRendersMessage(t *testing.T) {
	e := newEnv(t, false)
	e.seedUsers()
	e.changeAdminPassword()
	admin := e.adminClient()
	adminID := fix23E2EAdminID(e)
	for i := int64(1); i <= 2; i++ {
		fix24BInsertInvoice(e, i, fix24BPoison, adminID, "admin", 0)
	}
	code, body := e.get(admin, "/")
	if code != 200 {
		t.Fatalf("GET /: status = %d, want 200 (body head %q)", code, truncate(body, 200))
	}
	if got := fix24BExtract(fix24BDaySalesRe, body); got != money.ErrOverflow.Error() {
		t.Fatalf("day sales box = %q, want overflow message %q", got, money.ErrOverflow.Error())
	}
	if got := fix24BExtract(fix24BPageErrRe, body); got != money.ErrOverflow.Error() {
		t.Fatalf("page error banner = %q, want overflow message %q", got, money.ErrOverflow.Error())
	}
	if got := fix24BExtract(fix24BDayCountRe, body); got != "—" {
		t.Fatalf("day invoice count box = %q, want %q (count unknown when the SUM fails)", got, "—")
	}
	if strings.Contains(body, "خطأ في عرض الصفحة") {
		t.Fatalf("GET /: body contains the generic 500 page-error text")
	}
}

// TestFix24B_HubShiftOverflowRendersMessage is the shift-scope leg
// (routes.go:319): two 5e18-net invoices on the cashier's own open
// shift overflow the shift SUM; GET / as that cashier must answer 200
// with the Arabic overflow message in the shift sales box, not 0.00.
func TestFix24B_HubShiftOverflowRendersMessage(t *testing.T) {
	e := newEnv(t, false)
	e.seedUsers()
	cashierID := e.userID["cashier"]
	shiftID := fix24BOpenShift(e, cashierID, "cashier")
	cash := e.cashierClient()
	for i := int64(1); i <= 2; i++ {
		fix24BInsertInvoice(e, i, fix24BPoison, cashierID, "cashier", shiftID)
	}
	code, body := e.get(cash, "/")
	if code != 200 {
		t.Fatalf("GET /: status = %d, want 200 (body head %q)", code, truncate(body, 200))
	}
	if got := fix24BExtract(fix24BShiftSalesRe, body); got != money.ErrOverflow.Error() {
		t.Fatalf("shift sales box = %q, want overflow message %q", got, money.ErrOverflow.Error())
	}
	if got := fix24BExtract(fix24BPageErrRe, body); got != money.ErrOverflow.Error() {
		t.Fatalf("page error banner = %q, want overflow message %q", got, money.ErrOverflow.Error())
	}
	if got := fix24BExtract(fix24BShiftCountRe, body); got != "—" {
		t.Fatalf("shift invoice count box = %q, want %q (count unknown when the SUM fails)", got, "—")
	}
	if strings.Contains(body, "خطأ في عرض الصفحة") {
		t.Fatalf("GET /: body contains the generic 500 page-error text")
	}
}

// TestFix24B_HubNormalDayTotalsUnchanged: without poison the admin hub
// must render the exact day totals as before (150.00 over 2 invoices)
// and carry no overflow message.
func TestFix24B_HubNormalDayTotalsUnchanged(t *testing.T) {
	e := newEnv(t, false)
	e.seedUsers()
	e.changeAdminPassword()
	admin := e.adminClient()
	adminID := fix23E2EAdminID(e)
	fix24BInsertInvoice(e, 1, 10000, adminID, "admin", 0)
	fix24BInsertInvoice(e, 2, 5000, adminID, "admin", 0)
	code, body := e.get(admin, "/")
	if code != 200 {
		t.Fatalf("GET /: status = %d, want 200 (body head %q)", code, truncate(body, 200))
	}
	if got := fix24BExtract(fix24BDaySalesRe, body); got != "150.00 ج.م" {
		t.Fatalf("day sales box = %q, want %q", got, "150.00 ج.م")
	}
	if got := fix24BExtract(fix24BDayCountRe, body); got != "2" {
		t.Fatalf("day invoice count box = %q, want %q", got, "2")
	}
	if strings.Contains(body, "تجاوز الحد") {
		t.Fatalf("GET /: normal hub must not carry the overflow message")
	}
}

// TestFix24B_HubNormalShiftTotalsUnchanged: without poison the cashier
// hub must render the exact shift totals as before (100.00, 1 invoice)
// and carry no overflow message.
func TestFix24B_HubNormalShiftTotalsUnchanged(t *testing.T) {
	e := newEnv(t, false)
	e.seedUsers()
	cashierID := e.userID["cashier"]
	shiftID := fix24BOpenShift(e, cashierID, "cashier")
	cash := e.cashierClient()
	fix24BInsertInvoice(e, 1, 10000, cashierID, "cashier", shiftID)
	code, body := e.get(cash, "/")
	if code != 200 {
		t.Fatalf("GET /: status = %d, want 200 (body head %q)", code, truncate(body, 200))
	}
	if got := fix24BExtract(fix24BShiftSalesRe, body); got != "100.00 ج.م" {
		t.Fatalf("shift sales box = %q, want %q", got, "100.00 ج.م")
	}
	if got := fix24BExtract(fix24BShiftCountRe, body); got != "1" {
		t.Fatalf("shift invoice count box = %q, want %q", got, "1")
	}
	if strings.Contains(body, "تجاوز الحد") {
		t.Fatalf("GET /: normal hub must not carry the overflow message")
	}
}
