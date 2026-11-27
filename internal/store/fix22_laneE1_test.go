package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"cashierpos-go/internal/money"
)

// --- FIX-22 R22-E-F1: unchecked totalRefund accumulation stored
// absurd refunds on non-cash returns ---
//
// domain_returns.go:368 accumulated the per-line refunds with a plain
// `totalRefund += refund`, while each per-line refund is built with
// money.AddChecked (:347). Three stored absurd lines (TotalPrice =
// 6148914691236517206 each, set by direct DB write on a genuine sale)
// wrapped the total to 2, the cap check at :372 compared the wrapped
// total, and a Visa return committed 200 with three Returns rows of
// 6148914691236517206 and a Payments row of -2. The fix accumulates
// with money.AddChecked, so the wrap refuses with money.ErrOverflow
// (Arabic overflow message) before anything is stored — never the raw
// SQLite error (O1) and never the misleading "لا يوجد مبلغ قابل
// للاسترداد" (O2).

const fix22BigTotal = int64(6148914691236517206)

// testSaleInvoiceLines inserts a sale invoice with one 1-unit detail
// line per product (unit price 100, buy 1, paid in full in cash),
// returning the invoice id. NetAmount is 100 per line.
func testSaleInvoiceLines(t *testing.T, s *Store, userID int64, userName string, shiftID int64, number int64, pids []int64) int64 {
	t.Helper()
	n := int64(len(pids)) * 100
	res, err := s.DB.Exec(`INSERT INTO Invoices(InvoiceNumber,CustomerID,SaleType,TotalAmount,Discount,
		PromoDiscount,NetAmount,PaidAmount,RemainingAmount,PaymentMethod,UserID,UserName,ShiftID,InvoiceDate,IsCanceled)
		VALUES(? ,1,'retail',?,0,0,?,?,0,'Cash',?,?,?, ?,0)`,
		number, n, n, n, userID, userName, shiftID, Now())
	if err != nil {
		t.Fatalf("insert invoice: %v", err)
	}
	invID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("invoice id: %v", err)
	}
	for _, pid := range pids {
		if _, err := s.DB.Exec(`INSERT INTO InvoiceDetails(InvoiceID,ProductID,ProductName,Quantity,UnitPrice,BuyPrice,TotalPrice)
			VALUES(?,?,?,1000,100,1,100)`, invID, pid, "صنف"); err != nil {
			t.Fatalf("insert detail: %v", err)
		}
	}
	if _, err := s.DB.Exec(`INSERT INTO Payments(InvoiceID,CustomerID,Amount,PaymentDate,PaymentMethod,Notes,UserID)
		VALUES(?,?,?,?,'Cash','دفعة عند البيع',?)`, invID, 1, n, Now(), userID); err != nil {
		t.Fatalf("insert payment: %v", err)
	}
	return invID
}

func fix22ReturnLines(pids []int64) []ReturnLineInput {
	lines := make([]ReturnLineInput, 0, len(pids))
	for _, pid := range pids {
		lines = append(lines, ReturnLineInput{ProductID: pid, Qty: 1000})
	}
	return lines
}

func fix22Count(t *testing.T, s *Store, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := s.DB.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", q, err)
	}
	return n
}

// TestFix22_ReturnTotalRefundChecked_ThreeLines is the primary shape:
// Visa 3-line return on stored absurd TotalPrice lines must refuse
// with the Arabic overflow message and store nothing.
func TestFix22_ReturnTotalRefundChecked_ThreeLines(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "sup1", "supervisor")
	shiftID, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pids := []int64{
		testProductB2(t, s, "شاي-1", 1, 100, 100000),
		testProductB2(t, s, "شاي-2", 1, 100, 100000),
		testProductB2(t, s, "شاي-3", 1, 100, 100000),
	}
	invID := testSaleInvoiceLines(t, s, u.ID, u.UserName, shiftID, 1, pids)
	if _, err := s.DB.Exec(`UPDATE InvoiceDetails SET TotalPrice=? WHERE InvoiceID=?`, fix22BigTotal, invID); err != nil {
		t.Fatalf("poison details: %v", err)
	}
	_, err = s.ProcessReturn(ctx, u, invID, fix22ReturnLines(pids), "تالف", "Visa")
	if err == nil {
		t.Fatal("expected a Visa 3-line absurd return to be refused, got nil error")
	}
	if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("err = %v, want money.ErrOverflow", err)
	}
	if strings.Contains(err.Error(), "SQL logic error") {
		t.Fatalf("refusal leaked a raw SQLite error (O1): %v", err)
	}
	if strings.Contains(err.Error(), "لا يوجد مبلغ قابل للاسترداد") {
		t.Fatalf("refusal used the misleading message (O2): %v", err)
	}
	if n := fix22Count(t, s, `SELECT COUNT(*) FROM Returns WHERE InvoiceID=?`, invID); n != 0 {
		t.Fatalf("refused return stored %d Returns rows, want 0", n)
	}
	if n := fix22Count(t, s, `SELECT COUNT(*) FROM Payments WHERE InvoiceID=? AND Amount<0`, invID); n != 0 {
		t.Fatalf("refused return stored %d negative Payments rows, want 0", n)
	}
}

// TestFix22_ReturnTotalRefundChecked_FourLines is the H3 shape: the
// pre-fix code cap-trimmed the WRAPPED total and stored [BIG x3, 398]
// with total 400. Post-fix it must refuse with ErrOverflow, 0 rows.
func TestFix22_ReturnTotalRefundChecked_FourLines(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "sup1", "supervisor")
	shiftID, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pids := []int64{
		testProductB2(t, s, "شاي-1", 1, 100, 100000),
		testProductB2(t, s, "شاي-2", 1, 100, 100000),
		testProductB2(t, s, "شاي-3", 1, 100, 100000),
		testProductB2(t, s, "شاي-4", 1, 100, 100000),
	}
	invID := testSaleInvoiceLines(t, s, u.ID, u.UserName, shiftID, 1, pids)
	if _, err := s.DB.Exec(`UPDATE InvoiceDetails SET TotalPrice=? WHERE InvoiceID=?`, fix22BigTotal, invID); err != nil {
		t.Fatalf("poison details: %v", err)
	}
	_, err = s.ProcessReturn(ctx, u, invID, fix22ReturnLines(pids), "تالف", "Visa")
	if err == nil {
		t.Fatal("expected a Visa 4-line absurd return to be refused, got nil error")
	}
	if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("err = %v, want money.ErrOverflow", err)
	}
	if n := fix22Count(t, s, `SELECT COUNT(*) FROM Returns WHERE InvoiceID=?`, invID); n != 0 {
		t.Fatalf("refused return stored %d Returns rows, want 0", n)
	}
}

// TestFix22_ReturnTotalRefundChecked_TwoLinesCash is the H2/O2 shape:
// two absurd lines wrap the total NEGATIVE, which the pre-fix code
// refused with the misleading "لا يوجد مبلغ قابل للاسترداد".
// Post-fix the refusal must be the Arabic overflow message.
func TestFix22_ReturnTotalRefundChecked_TwoLinesCash(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "sup1", "supervisor")
	shiftID, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pids := []int64{
		testProductB2(t, s, "شاي-1", 1, 100, 100000),
		testProductB2(t, s, "شاي-2", 1, 100, 100000),
	}
	invID := testSaleInvoiceLines(t, s, u.ID, u.UserName, shiftID, 1, pids)
	if _, err := s.DB.Exec(`UPDATE InvoiceDetails SET TotalPrice=? WHERE InvoiceID=?`, fix22BigTotal, invID); err != nil {
		t.Fatalf("poison details: %v", err)
	}
	_, err = s.ProcessReturn(ctx, u, invID, fix22ReturnLines(pids), "تالف", "Cash")
	if err == nil {
		t.Fatal("expected a Cash 2-line absurd return to be refused, got nil error")
	}
	if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("err = %v, want money.ErrOverflow", err)
	}
	if strings.Contains(err.Error(), "لا يوجد مبلغ قابل للاسترداد") {
		t.Fatalf("refusal used the misleading message (O2): %v", err)
	}
	if n := fix22Count(t, s, `SELECT COUNT(*) FROM Returns WHERE InvoiceID=?`, invID); n != 0 {
		t.Fatalf("refused return stored %d Returns rows, want 0", n)
	}
}

// TestFix22_ReturnAllAbsurdRefuses: ProcessReturnAll shares the same
// prepare loop, so a full return on absurd lines must refuse too.
func TestFix22_ReturnAllAbsurdRefuses(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "sup1", "supervisor")
	shiftID, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pids := []int64{
		testProductB2(t, s, "شاي-1", 1, 100, 100000),
		testProductB2(t, s, "شاي-2", 1, 100, 100000),
		testProductB2(t, s, "شاي-3", 1, 100, 100000),
	}
	invID := testSaleInvoiceLines(t, s, u.ID, u.UserName, shiftID, 1, pids)
	if _, err := s.DB.Exec(`UPDATE InvoiceDetails SET TotalPrice=? WHERE InvoiceID=?`, fix22BigTotal, invID); err != nil {
		t.Fatalf("poison details: %v", err)
	}
	_, err = s.ProcessReturnAll(ctx, u, invID, "إرجاع كامل", "Visa")
	if err == nil {
		t.Fatal("expected a full absurd return to be refused, got nil error")
	}
	if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("err = %v, want money.ErrOverflow", err)
	}
	if n := fix22Count(t, s, `SELECT COUNT(*) FROM Returns WHERE InvoiceID=?`, invID); n != 0 {
		t.Fatalf("refused return stored %d Returns rows, want 0", n)
	}
}

// TestFix22_NormalMultiLineReturnUnchanged guards the fix: an ordinary
// 3-line Visa return still refunds exactly, with rows stored.
func TestFix22_NormalMultiLineReturnUnchanged(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "sup1", "supervisor")
	shiftID, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pids := []int64{
		testProductB2(t, s, "شاي-1", 1, 100, 100000),
		testProductB2(t, s, "شاي-2", 1, 100, 100000),
		testProductB2(t, s, "شاي-3", 1, 100, 100000),
	}
	invID := testSaleInvoiceLines(t, s, u.ID, u.UserName, shiftID, 1, pids)
	res, err := s.ProcessReturn(ctx, u, invID, fix22ReturnLines(pids), "تالف", "Visa")
	if err != nil {
		t.Fatalf("normal 3-line return: %v", err)
	}
	if res.TotalRefund != 300 {
		t.Fatalf("normal return total = %v, want 300", res.TotalRefund)
	}
	if n := fix22Count(t, s, `SELECT COUNT(*) FROM Returns WHERE InvoiceID=? AND TotalRefund=100`, invID); n != 3 {
		t.Fatalf("want 3 Returns rows of 100, got %d", n)
	}
	// The analytics on the same rows stay exact too.
	admin := b2TestUser(t, s, "adm1", "admin")
	rep, err := s.GetReturnsAnalytics(ctx, admin, Today(), Today())
	if err != nil {
		t.Fatalf("analytics after normal return: %v", err)
	}
	if rep.TotalValue != 300 || rep.Count != 3 {
		t.Fatalf("analytics after normal return: total=%v count=%v, want 300/3", rep.TotalValue, rep.Count)
	}
}

// fix22PoisonReturns inserts two legacy-poison Returns rows (TotalRefund
// 5e18 each) on the given invoice, standing in for rows stored before
// this fix. Their SUM overflows SQLite's int64 SUM.
func fix22PoisonReturns(t *testing.T, s *Store, invID, pid, shiftID, userID int64) {
	t.Helper()
	for i := 0; i < 2; i++ {
		if _, err := s.DB.Exec(`INSERT INTO Returns(InvoiceID,ProductID,ProductName,Quantity,UnitPrice,BuyPrice,
			TotalRefund,Reason,UserID,UserName,ReturnDate,ReturnPaymentMethod,ShiftID,IsProcessed)
			VALUES(?,?,?,1000,100,1,5000000000000000000,'poison',?,'admin',?,'Visa',?,1)`,
			invID, pid, "شاي-1", userID, Now(), shiftID); err != nil {
			t.Fatalf("insert poison return: %v", err)
		}
	}
}

// TestFix22_PaymentReportReturnsSumOverflowSurfaces: with legacy-poison
// Returns rows the payments report must return the Arabic overflow
// error, never a silent المرتجعات: 0.00 (the :722 `_ =` swallow).
func TestFix22_PaymentReportReturnsSumOverflowSurfaces(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b2TestUser(t, s, "adm1", "admin")
	u := b2TestUser(t, s, "sup1", "supervisor")
	shiftID, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pid := testProductB2(t, s, "شاي-1", 1, 100, 100000)
	invID := testSaleInvoiceLines(t, s, u.ID, u.UserName, shiftID, 1, []int64{pid})
	fix22PoisonReturns(t, s, invID, pid, shiftID, admin.ID)
	_, err = s.PaymentReportFor(ctx, admin, Today(), Today())
	if err == nil {
		t.Fatal("expected PaymentReportFor to fail on poisoned Returns SUM, got nil error")
	}
	if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("err = %v, want money.ErrOverflow", err)
	}
	if strings.Contains(err.Error(), "SQL logic error") {
		t.Fatalf("report error leaked a raw SQLite error: %v", err)
	}
}

// TestFix22_ReturnsAnalyticsOverflowRefuses: two legacy-poison Returns
// rows wrap the Go-side `+=` accumulation (pre-fix: TotalValue wrapped
// to a negative and the call returned 200 incoherent). Post-fix the
// analytics refuse with the Arabic overflow message.
func TestFix22_ReturnsAnalyticsOverflowRefuses(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b2TestUser(t, s, "adm1", "admin")
	u := b2TestUser(t, s, "sup1", "supervisor")
	shiftID, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pid := testProductB2(t, s, "شاي-1", 1, 100, 100000)
	invID := testSaleInvoiceLines(t, s, u.ID, u.UserName, shiftID, 1, []int64{pid})
	fix22PoisonReturns(t, s, invID, pid, shiftID, admin.ID)
	_, err = s.GetReturnsAnalytics(ctx, admin, Today(), Today())
	if err == nil {
		t.Fatal("expected GetReturnsAnalytics to fail on poisoned rows, got nil error")
	}
	if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("err = %v, want money.ErrOverflow", err)
	}
}

// TestFix22_DashboardInvoiceSumOverflowSurfaces: two stored invoices
// with NetAmount 5e18 each overflow the dashboard's SUM(NetAmount);
// the dashboard must surface the error, never a silent 0.00 today-sales.
func TestFix22_DashboardInvoiceSumOverflowSurfaces(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b2TestUser(t, s, "adm1", "admin")
	u := b2TestUser(t, s, "sup1", "supervisor")
	shiftID, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	for i := int64(1); i <= 2; i++ {
		if _, err := s.DB.Exec(`INSERT INTO Invoices(InvoiceNumber,CustomerID,SaleType,TotalAmount,Discount,
			PromoDiscount,NetAmount,PaidAmount,RemainingAmount,PaymentMethod,UserID,UserName,ShiftID,InvoiceDate,IsCanceled)
			VALUES(?,1,'retail',5000000000000000000,0,0,5000000000000000000,0,0,'Cash',?,?,?, ?,0)`,
			i, u.ID, u.UserName, shiftID, Now()); err != nil {
			t.Fatalf("insert poison invoice: %v", err)
		}
	}
	_, err = s.Dashboard(ctx, admin)
	if err == nil {
		t.Fatal("expected Dashboard to fail on poisoned Invoices SUM, got nil error")
	}
	if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("err = %v, want money.ErrOverflow", err)
	}
}
