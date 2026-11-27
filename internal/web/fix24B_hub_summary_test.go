// Package web — FIX-24 lane B (R24-C-N1) unit tests.
//
// Direct hubData coverage for the SUM-error path: the day/shift
// SUM(NetAmount) scan failure must land in HubData.SummaryError as
// the Arabic overflow message (the same mapping Dashboard gets via
// store.sumScanErr), and the normal path must leave it empty with
// exact totals.
package web

import (
	"errors"
	"testing"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/money"
	"cashierpos-go/internal/store"
)

func TestFix24B_HubSummaryErrMapping(t *testing.T) {
	if got := hubSummaryErr(nil); got != "" {
		t.Errorf("hubSummaryErr(nil) = %q, want empty", got)
	}
	overflow := errors.New("SQL logic error: integer overflow (1)")
	if got := hubSummaryErr(overflow); got != money.ErrOverflow.Error() {
		t.Errorf("hubSummaryErr(overflow) = %q, want %q", got, money.ErrOverflow.Error())
	}
	other := errors.New("SQL logic error: no such table: Invoices (1)")
	if got := hubSummaryErr(other); got != other.Error() {
		t.Errorf("hubSummaryErr(other) = %q, want %q verbatim (Dashboard parity)", got, other.Error())
	}
}

func fix24BTestServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.OpenTest()
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv, err := New(st)
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	return srv
}

func fix24BInsertHubInvoice(t *testing.T, srv *Server, num int64, net int64, userID int64, userName string, shiftID int64) {
	t.Helper()
	if _, err := srv.Store.DB.Exec(`INSERT INTO Invoices(InvoiceNumber,CustomerID,SaleType,TotalAmount,Discount,
			PromoDiscount,NetAmount,PaidAmount,RemainingAmount,PaymentMethod,UserID,UserName,ShiftID,InvoiceDate,IsCanceled)
			VALUES(?,1,'retail',?,0,0,?,0,0,'Cash',?,?,?, ?,0)`,
		num, net, net, userID, userName, shiftID, store.Now()); err != nil {
		t.Fatalf("insert invoice %d: %v", num, err)
	}
}

func TestFix24B_HubDataDayOverflowSetsSummaryError(t *testing.T) {
	srv := fix24BTestServer(t)
	for i := int64(1); i <= 2; i++ {
		fix24BInsertHubInvoice(t, srv, i, 5000000000000000000, 1, "admin", 0)
	}
	d := srv.hubData(&auth.User{ID: 1, UserName: "admin", Role: "admin"})
	if d.SummaryScope != "day" {
		t.Fatalf("SummaryScope = %q, want %q", d.SummaryScope, "day")
	}
	if d.SummaryError != money.ErrOverflow.Error() {
		t.Fatalf("SummaryError = %q, want %q", d.SummaryError, money.ErrOverflow.Error())
	}
	if d.TodaySales != 0 {
		t.Fatalf("TodaySales = %v, want 0 (the SUM failed, no total is known)", d.TodaySales)
	}
}

func TestFix24B_HubDataShiftOverflowSetsSummaryError(t *testing.T) {
	srv := fix24BTestServer(t)
	res, err := srv.Store.DB.Exec(`INSERT INTO Shifts(UserID,UserName,ShiftDate,OpenTime,Status,OpeningBalance,IsOpen,SessionCount)
		VALUES(?, 'cashier', ?, ?, 'Open', 10000, 1, 1)`, 2, store.Today(), store.Now())
	if err != nil {
		t.Fatalf("insert shift: %v", err)
	}
	shiftID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("shift id: %v", err)
	}
	for i := int64(1); i <= 2; i++ {
		fix24BInsertHubInvoice(t, srv, i, 5000000000000000000, 2, "cashier", shiftID)
	}
	d := srv.hubData(&auth.User{ID: 2, UserName: "cashier", Role: "cashier"})
	if d.SummaryScope != "shift" {
		t.Fatalf("SummaryScope = %q, want %q", d.SummaryScope, "shift")
	}
	if d.SummaryError != money.ErrOverflow.Error() {
		t.Fatalf("SummaryError = %q, want %q", d.SummaryError, money.ErrOverflow.Error())
	}
}

func TestFix24B_HubDataNormalTotalsExact(t *testing.T) {
	srv := fix24BTestServer(t)
	fix24BInsertHubInvoice(t, srv, 1, 10000, 1, "admin", 0)
	fix24BInsertHubInvoice(t, srv, 2, 5000, 1, "admin", 0)
	d := srv.hubData(&auth.User{ID: 1, UserName: "admin", Role: "admin"})
	if d.SummaryError != "" {
		t.Fatalf("SummaryError = %q, want empty on the normal path", d.SummaryError)
	}
	if d.TodaySales != 15000 || d.TodayInvoices != 2 {
		t.Fatalf("normal day totals = %v / %d, want 15000 / 2", d.TodaySales, d.TodayInvoices)
	}
}
