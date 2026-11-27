package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"cashierpos-go/internal/money"
)

// --- FIX-25 lane C: R25-C-F1 (report SUMs leak raw SQLite overflow as 400) ---
//
// DailyClose's Expenses SUM and SUM(DifferenceAmount), and BestCashiers'
// SUM(NetAmount), returned the scan error raw (no sumScanErr), and
// apiDailyClose / apiDailyCloseReport echoed ANY store error verbatim
// as 400. The fix maps every SUM scan below through sumScanErr
// (money.ErrOverflow, Arabic) and gives the two handlers the
// apiShiftSummary trichotomy. Verification scope (same class, live
// surfaces): PeakHours, SalesReportFor, DebtsReportFor and
// loadProfitLines — all fixed the same way here.
//
// Poison values are written by direct DB insert only (legacy poison);
// no app path is used to store them.

const fix25CBig = int64(5000000000000000000) // 5e18

func fix25CExpectOverflow(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected an overflow refusal, got nil error", what)
	}
	if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("%s: err = %v, want money.ErrOverflow", what, err)
	}
	if strings.Contains(err.Error(), "SQL logic error") || strings.Contains(err.Error(), "integer overflow") {
		t.Fatalf("%s: err leaks raw SQLite text: %v", what, err)
	}
}

func fix25CPlantExpenses(t *testing.T, s *Store, shiftID int64) {
	t.Helper()
	for i := 0; i < 2; i++ {
		if _, err := s.DB.Exec(`INSERT INTO Expenses(Description,Amount,Category,UserID,ShiftID,ExpenseDate)
			VALUES(?,?, 'عام', 1, ?, ?)`, "مصروف ضخم", fix25CBig, shiftID, Today()+" 10:00:00"); err != nil {
			t.Fatalf("insert poisoned expense: %v", err)
		}
	}
}

func fix25CPlantDiffShifts(t *testing.T, s *Store, userID int64) {
	t.Helper()
	for i := 0; i < 2; i++ {
		if _, err := s.DB.Exec(`INSERT INTO Shifts(UserID,UserName,ShiftDate,OpenTime,Status,IsOpen,OpeningBalance,DifferenceAmount)
			VALUES(?, 'admin', ?, '10:00:00', 'Closed', 0, 0, ?)`, userID, Today(), fix25CBig); err != nil {
			t.Fatalf("insert poisoned shift: %v", err)
		}
	}
}

// fix25CPlantProfitPoison plants one normal invoice with two details
// whose ItemDiscount values sum past int64, so loadProfitLines'
// per-invoice SUM(ItemDiscount) subquery overflows on Query/Scan.
// (Test DBs enforce FK, so the details reference a real product.)
func fix25CPlantProfitPoison(t *testing.T, s *Store, shiftID, userID int64) {
	t.Helper()
	prodID := testProduct(t, s, "أرز", money.Cents(100), money.Cents(10000), money.Qty(100000))
	res, err := s.DB.Exec(`INSERT INTO Invoices(InvoiceNumber,CustomerID,ShiftID,TotalAmount,Discount,
		PromoDiscount,NetAmount,PaidAmount,RemainingAmount,PaymentMethod,UserName,UserID,InvoiceDate,IsCanceled)
		VALUES(9501, 0, ?, 20000, 0, 0, 20000, 20000, 0, 'Cash', 'admin', ?, ?, 0)`,
		shiftID, userID, Today())
	if err != nil {
		t.Fatalf("insert profit-poison invoice: %v", err)
	}
	invID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("profit-poison invoice id: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := s.DB.Exec(`INSERT INTO InvoiceDetails(InvoiceID,ProductID,ProductName,Quantity,UnitPrice,BuyPrice,ItemDiscount,PromoDiscount,TotalPrice)
			VALUES(?, ?, 'أرز', 1000, 10000, 100, ?, 0, 10000)`, invID, prodID, fix25CBig); err != nil {
			t.Fatalf("insert profit-poison detail: %v", err)
		}
	}
}

func TestFix25C_DailyClose_ExpensesOverflow(t *testing.T) {
	s := testStore(t)
	u := b2TestUser(t, s, "fix25c-admin", "admin")
	shiftID, err := s.OpenShift(context.Background(), u, money.Cents(100000), false)
	if err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	fix25CPlantExpenses(t, s, shiftID)
	_, err = s.DailyClose(context.Background(), u, Today())
	fix25CExpectOverflow(t, "DailyClose Expenses SUM", err)
}

func TestFix25C_DailyClose_DifferenceOverflow(t *testing.T) {
	s := testStore(t)
	u := b2TestUser(t, s, "fix25c-admin", "admin")
	fix25CPlantDiffShifts(t, s, u.ID)
	_, err := s.DailyClose(context.Background(), u, Today())
	fix25CExpectOverflow(t, "DailyClose SUM(DifferenceAmount)", err)
}

func TestFix25C_BestCashiers_Overflow(t *testing.T) {
	s := testStore(t)
	u := b2TestUser(t, s, "fix25c-admin", "admin")
	shiftID, err := s.OpenShift(context.Background(), u, money.Cents(100000), false)
	if err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	cust := testCustomer(t, s, "عميل مسموم")
	fix24CPoisonInvoices(t, s, cust, shiftID, u.ID)
	_, err = s.BestCashiers(context.Background(), u, Today(), Today())
	fix25CExpectOverflow(t, "BestCashiers SUM(NetAmount)", err)
}

func TestFix25C_PeakHours_Overflow(t *testing.T) {
	s := testStore(t)
	u := b2TestUser(t, s, "fix25c-admin", "admin")
	shiftID, err := s.OpenShift(context.Background(), u, money.Cents(100000), false)
	if err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	cust := testCustomer(t, s, "عميل مسموم")
	fix24CPoisonInvoices(t, s, cust, shiftID, u.ID)
	_, err = s.PeakHours(context.Background(), u, Today(), Today())
	fix25CExpectOverflow(t, "PeakHours SUM(NetAmount)", err)
}

func TestFix25C_SalesReportFor_Overflow(t *testing.T) {
	s := testStore(t)
	u := b2TestUser(t, s, "fix25c-admin", "admin")
	shiftID, err := s.OpenShift(context.Background(), u, money.Cents(100000), false)
	if err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	cust := testCustomer(t, s, "عميل مسموم")
	fix24CPoisonInvoices(t, s, cust, shiftID, u.ID)
	_, err = s.SalesReportFor(context.Background(), u, Today(), Today())
	fix25CExpectOverflow(t, "SalesReportFor SUMs", err)
}

func TestFix25C_DebtsReportFor_Overflow(t *testing.T) {
	s := testStore(t)
	u := b2TestUser(t, s, "fix25c-admin", "admin")
	shiftID, err := s.OpenShift(context.Background(), u, money.Cents(100000), false)
	if err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	cust := testCustomer(t, s, "عميل مسموم")
	fix24CPoisonInvoices(t, s, cust, shiftID, u.ID)
	_, _, err = s.DebtsReportFor(context.Background(), u)
	fix25CExpectOverflow(t, "DebtsReportFor SUM(RemainingAmount)", err)
}

func TestFix25C_LoadProfitLines_Overflow(t *testing.T) {
	s := testStore(t)
	u := b2TestUser(t, s, "fix25c-admin", "admin")
	shiftID, err := s.OpenShift(context.Background(), u, money.Cents(100000), false)
	if err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	fix25CPlantProfitPoison(t, s, shiftID, u.ID)
	_, err = loadProfitLines(s.DB, Today(), Today(), "")
	fix25CExpectOverflow(t, "loadProfitLines SUM(ItemDiscount)", err)
	// The same poison must also refuse through the TopProducts API path.
	_, err = s.TopProducts(context.Background(), u, Today(), Today(), 10, false)
	fix25CExpectOverflow(t, "TopProducts via loadProfitLines", err)
}

func TestFix25C_NormalDailyCloseAndCashiers(t *testing.T) {
	s := testStore(t)
	u := b2TestUser(t, s, "fix25c-admin", "admin")
	shiftID, err := s.OpenShift(context.Background(), u, money.Cents(100000), false)
	if err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	cust := testCustomer(t, s, "عميل عادي")
	if _, err := s.DB.Exec(`INSERT INTO Invoices(InvoiceNumber,CustomerID,ShiftID,TotalAmount,Discount,
		PromoDiscount,NetAmount,PaidAmount,RemainingAmount,PaymentMethod,UserName,UserID,InvoiceDate,IsCanceled)
		VALUES(9502, ?, ?, 2500, 0, 0, 2500, 2500, 0, 'Cash', 'admin', ?, ?, 0)`,
		cust, shiftID, u.ID, Today()); err != nil {
		t.Fatalf("insert normal invoice: %v", err)
	}
	if _, err := s.DB.Exec(`INSERT INTO Expenses(Description,Amount,Category,UserID,ShiftID,ExpenseDate)
		VALUES('مصروف عادي', 1000, 'عام', 1, ?, ?)`, shiftID, Today()+" 10:00:00"); err != nil {
		t.Fatalf("insert normal expense: %v", err)
	}
	rep, err := s.DailyClose(context.Background(), u, Today())
	if err != nil {
		t.Fatalf("normal DailyClose: %v", err)
	}
	if rep.TotalExpenses != money.Cents(1000) {
		t.Fatalf("normal DailyClose TotalExpenses = %v, want 1000", rep.TotalExpenses)
	}
	rows, err := s.BestCashiers(context.Background(), u, Today(), Today())
	if err != nil {
		t.Fatalf("normal BestCashiers: %v", err)
	}
	if len(rows) != 1 || rows[0].TotalSales != money.Cents(2500) {
		t.Fatalf("normal BestCashiers rows = %+v, want one row TotalSales 2500", rows)
	}
	rep2, err := s.SalesReportFor(context.Background(), u, Today(), Today())
	if err != nil {
		t.Fatalf("normal SalesReportFor: %v", err)
	}
	if rep2.TotalSales != money.Cents(2500) {
		t.Fatalf("normal SalesReportFor TotalSales = %v, want 2500", rep2.TotalSales)
	}
}

func TestFix25C_PaymentReportFor_ByMethodOverflow(t *testing.T) {
	s := testStore(t)
	u := b2TestUser(t, s, "fix25c-admin", "admin")
	shiftID, err := s.OpenShift(context.Background(), u, money.Cents(100000), false)
	if err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	cust := testCustomer(t, s, "عميل مسموم")
	// PaidAmount 5e18 on both invoices, so the ByMethod
	// SUM(netPaidExpr) overflows (PaidAmount is the net-paid term).
	for n := int64(9505); n <= 9506; n++ {
		if _, err := s.DB.Exec(`INSERT INTO Invoices(InvoiceNumber,CustomerID,ShiftID,TotalAmount,Discount,
			PromoDiscount,NetAmount,PaidAmount,RemainingAmount,PaymentMethod,UserName,UserID,InvoiceDate,IsCanceled)
			VALUES(?,?,?,?,0,0,?,?,?,'Cash','admin',?,?,0)`,
			n, cust, shiftID, fix25CBig, fix25CBig, fix25CBig, 0, u.ID, Today()); err != nil {
			t.Fatalf("insert poisoned invoice: %v", err)
		}
	}
	_, err = s.PaymentReportFor(context.Background(), u, Today(), Today())
	fix25CExpectOverflow(t, "PaymentReportFor ByMethod SUM", err)
}
