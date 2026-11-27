package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/money"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenTest()
	if err != nil {
		t.Fatalf("OpenTest: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// b2TestUser creates a user (B2-local helper; the shared B3 helpers
// were removed mid-build, so B2 tests are self-contained).
func b2TestUser(t *testing.T, s *Store, username, role string) *auth.User {
	t.Helper()
	res, err := s.DB.Exec(`INSERT INTO Users(UserName,Password,FullName,Role,IsActive,MustChangePassword,CreatedDate)
		VALUES(?,?,?,?,1,0,?)`, username, "x", username+" الاسم", role, Today())
	if err != nil {
		t.Fatalf("insert user %s: %v", username, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("user id: %v", err)
	}
	return &auth.User{ID: id, UserName: username, FullName: username + " الاسم", Role: role, IsActive: true}
}

func testCustomer(t *testing.T, s *Store, name string) int64 {
	t.Helper()
	res, err := s.DB.Exec(`INSERT INTO Customers(CustomerName,Phone,Balance,CreditLimit,IsActive,CreatedDate)
		VALUES(?,?,0,0,1,?)`, name, "01000000000", Today())
	if err != nil {
		t.Fatalf("insert customer: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("customer id: %v", err)
	}
	return id
}

// testProductB2 inserts a product with an explicit retail price
// (B3's shared testProduct sets RetailPrice = BuyPrice+1000).
func testProductB2(t *testing.T, s *Store, name string, buy, retail money.Cents, qty money.Qty) int64 {
	t.Helper()
	res, err := s.DB.Exec(`INSERT INTO Products(ProductName,BuyPrice,RetailPrice,Quantity,MinQuantity,Unit,IsActive,CreatedDate)
		VALUES(?,?,?,?,5,'قطعة',1,?)`, name, int64(buy), int64(retail), int64(qty), Today())
	if err != nil {
		t.Fatalf("insert product: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("product id: %v", err)
	}
	return id
}

// testSaleInvoice inserts a sale invoice + one detail line + the
// at-sale Payments row, returning the invoice id. (B3's shared
// testInvoice inserts a bare invoice with no lines/payments.)
func testSaleInvoice(t *testing.T, s *Store, user *auth.User, shiftID int64, number int64, productID int64,
	qty money.Qty, unitPrice, buyPrice money.Cents, paid, remaining money.Cents, method string, customerID int64) int64 {
	t.Helper()
	total := mustMulQty(unitPrice, qty)
	res, err := s.DB.Exec(`INSERT INTO Invoices(InvoiceNumber,CustomerID,SaleType,TotalAmount,Discount,
		PromoDiscount,NetAmount,PaidAmount,RemainingAmount,PaymentMethod,UserID,UserName,ShiftID,InvoiceDate,IsCanceled)
		VALUES(?,?,'retail',?,0,0,?,?,?,?,?,?,?,?,0)`,
		number, customerID, int64(total), int64(total), int64(paid), int64(remaining), method,
		user.ID, user.UserName, shiftID, Now())
	if err != nil {
		t.Fatalf("insert invoice: %v", err)
	}
	invID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("invoice id: %v", err)
	}
	if _, err := s.DB.Exec(`INSERT INTO InvoiceDetails(InvoiceID,ProductID,ProductName,Quantity,UnitPrice,BuyPrice,TotalPrice)
		VALUES(?,?,?,?,?,?,?)`, invID, productID, "صنف", int64(qty), int64(unitPrice), int64(buyPrice), int64(total)); err != nil {
		t.Fatalf("insert detail: %v", err)
	}
	if paid > 0 {
		if _, err := s.DB.Exec(`INSERT INTO Payments(InvoiceID,CustomerID,Amount,PaymentDate,PaymentMethod,Notes,UserID)
			VALUES(?,?,?,?,?,'دفعة عند البيع',?)`, invID, customerID, int64(paid), Now(), method, user.ID); err != nil {
			t.Fatalf("insert payment: %v", err)
		}
	}
	return invID
}

func productQty(t *testing.T, s *Store, productID int64) money.Qty {
	t.Helper()
	var q int64
	if err := s.DB.QueryRow(`SELECT Quantity FROM Products WHERE ProductID=?`, productID).Scan(&q); err != nil {
		t.Fatalf("product qty: %v", err)
	}
	return money.Qty(q)
}

func yesterday() string {
	d, _ := time.Parse("2006-01-02", Today())
	return d.AddDate(0, 0, -1).Format("2006-01-02")
}

func TestOpenShiftFresh(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "cash1", "cashier")
	id, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	if id == 0 {
		t.Fatal("expected a shift id")
	}
	// Opening again returns the same shift (H-05), no second row.
	id2, err := s.OpenShift(ctx, u, money.Cents(5000), false)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	if id2 != id {
		t.Fatalf("expected same shift %d, got %d", id, id2)
	}
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM Shifts WHERE UserID=?`, u.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("expected 1 shift row, got %d", n)
	}
	// CashBox 'open' row written (H-06).
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM CashBox WHERE ShiftID=? AND MovementType='open'`, id).Scan(&n)
	if n != 1 {
		t.Fatalf("expected 1 open CashBox row, got %d", n)
	}
	st, err := s.GetShiftStatus(ctx, u.ID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.State != "open" || !st.HasOpen {
		t.Fatalf("expected open status, got %+v", st)
	}
}

func TestOpenShiftNegativeOpeningRejected(t *testing.T) {
	s := testStore(t)
	u := b2TestUser(t, s, "cash1", "cashier")
	if _, err := s.OpenShift(context.Background(), u, money.Cents(-100), false); err == nil {
		t.Fatal("expected negative opening balance to be rejected")
	}
}

func TestOpenShiftWarehouseDenied(t *testing.T) {
	s := testStore(t)
	u := b2TestUser(t, s, "wh1", "warehouse")
	if _, err := s.OpenShift(context.Background(), u, 0, false); err == nil {
		t.Fatal("expected warehouse to be denied open_shift")
	}
}

func TestCloseShiftUsesExpectedAsActual(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "cash1", "cashier")
	id, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	pid := testProductB2(t, s, "سكر", 1000, 2000, 10000)
	testSaleInvoice(t, s, u, id, 1, pid, 2000, 2000, 1000, 4000, 0, "Cash", 1)
	sum, err := s.CloseShift(ctx, u, 0)
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if sum.ExpectedBalance != 14000 {
		t.Fatalf("expected 14000, got %d", sum.ExpectedBalance)
	}
	sh, err := s.GetShift(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if sh.IsOpen || sh.Status != "Closed" {
		t.Fatalf("shift should be closed, got %+v", sh)
	}
	if sh.ActualBalance != sh.ExpectedBalance || sh.Difference != 0 || sh.DifferenceStatus != "متوازن" {
		t.Fatalf("close snapshot wrong: actual=%d expected=%d diff=%d status=%s",
			sh.ActualBalance, sh.ExpectedBalance, sh.Difference, sh.DifferenceStatus)
	}
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM CashBox WHERE ShiftID=? AND MovementType='close'`, id).Scan(&n)
	if n != 1 {
		t.Fatalf("expected 1 close CashBox row, got %d", n)
	}
	st, _ := s.GetShiftStatus(ctx, u.ID)
	if st.State != "closed" {
		t.Fatalf("expected closed status, got %+v", st)
	}
}

func TestCloseShiftNoOpenShift(t *testing.T) {
	s := testStore(t)
	u := b2TestUser(t, s, "cash1", "cashier")
	if _, err := s.CloseShift(context.Background(), u, 0); err == nil {
		t.Fatal("expected error closing with no open shift")
	}
}

func TestSameDayResume(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "cash1", "cashier")
	id, err := s.OpenShift(ctx, u, money.Cents(7000), false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := s.CloseShift(ctx, u, 0); err != nil {
		t.Fatalf("close: %v", err)
	}
	// Without resume=true the open must refuse and ask for confirmation.
	if _, err := s.OpenShift(ctx, u, money.Cents(9999), false); err == nil {
		t.Fatal("expected resume confirmation error")
	}
	// With resume=true the SAME shift reopens, same opening balance.
	id2, err := s.OpenShift(ctx, u, money.Cents(9999), true)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if id2 != id {
		t.Fatalf("resume should reopen shift %d, got %d", id, id2)
	}
	sh, _ := s.GetShift(ctx, id)
	if !sh.IsOpen || sh.OpeningBalance != 7000 || sh.SessionCount != 2 {
		t.Fatalf("resumed shift wrong: %+v", sh)
	}
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM CashBox WHERE ShiftID=? AND MovementType='resume'`, id).Scan(&n)
	if n != 1 {
		t.Fatalf("expected 1 resume CashBox row, got %d", n)
	}
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM ShiftSessions WHERE ShiftID=?`, id).Scan(&n)
	if n != 2 {
		t.Fatalf("expected 2 sessions, got %d", n)
	}
}

func TestAutoCloseStaleShift(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "cash1", "cashier")
	id, err := s.OpenShift(ctx, u, money.Cents(8000), false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Move the shift to yesterday.
	if _, err := s.DB.Exec(`UPDATE Shifts SET ShiftDate=? WHERE ShiftID=?`, yesterday(), id); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	// Any consult auto-closes it (actual = expected, no close row).
	// FIX-3 L17: the status reports the just-auto-closed shift
	// (State "closed", ShiftID, StatusText naming the auto-close)
	// instead of "none".
	st, err := s.GetShiftStatus(ctx, u.ID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.State != "closed" || st.ShiftID != id {
		t.Fatalf("expected closed status for auto-closed shift %d, got %+v", id, st)
	}
	if !strings.Contains(st.StatusText, "تلقائي") {
		t.Fatalf("status text must name the auto-close, got %q", st.StatusText)
	}
	sh, _ := s.GetShift(ctx, id)
	if sh.IsOpen || sh.Status != "Closed" {
		t.Fatalf("stale shift should be auto-closed, got %+v", sh)
	}
	if sh.ActualBalance != sh.ExpectedBalance || sh.Difference != 0 {
		t.Fatalf("auto-close must use expected as actual: %+v", sh)
	}
	if sh.Notes == "" || !strings.Contains(sh.Notes, "إغلاق تلقائي") {
		t.Fatalf("auto-close note missing flag: %q", sh.Notes)
	}
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM CashBox WHERE ShiftID=? AND MovementType='close'`, id).Scan(&n)
	if n != 0 {
		t.Fatalf("auto-close must post NO CashBox close row, got %d", n)
	}
	// A new shift opens fresh from zero (never carries the old close).
	id2, err := s.OpenShift(ctx, u, 0, false)
	if err != nil {
		t.Fatalf("open new day: %v", err)
	}
	if id2 == id {
		t.Fatal("new day must open a NEW shift")
	}
	sh2, _ := s.GetShift(ctx, id2)
	if sh2.OpeningBalance != 0 {
		t.Fatalf("new day opening must be freshly typed (0), got %d", sh2.OpeningBalance)
	}
}

func TestCloseOtherShiftPermissions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	cash1 := b2TestUser(t, s, "cash1", "cashier")
	cash2 := b2TestUser(t, s, "cash2", "cashier")
	admin := b2TestUser(t, s, "admin1", "admin")
	sup := b2TestUser(t, s, "sup1", "supervisor")
	id, err := s.OpenShift(ctx, cash1, 0, false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Another cashier may not close it.
	if _, err := s.CloseShift(ctx, cash2, id); err == nil {
		t.Fatal("expected another cashier to be denied closing someone else's shift")
	}
	// Supervisor may.
	if _, err := s.CloseShift(ctx, sup, id); err != nil {
		t.Fatalf("supervisor close: %v", err)
	}
	// Reopen by resume, then admin closes.
	if _, err := s.OpenShift(ctx, cash1, 0, true); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if _, err := s.CloseShift(ctx, admin, id); err != nil {
		t.Fatalf("admin close: %v", err)
	}
}

func TestShiftSummaryH09(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "cash1", "cashier")
	id, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	pid := testProductB2(t, s, "سكر", 1000, 2000, 10000)
	testSaleInvoice(t, s, u, id, 1, pid, 2000, 2000, 1000, 4000, 0, "Cash", 1)
	// Cash debt collection: credit invoice + collection.
	cust := testCustomer(t, s, "عميل آجل")
	testSaleInvoice(t, s, u, id, 2, pid, 1000, 2000, 1000, 0, 2000, "Cash", cust)
	if _, err := s.DB.Exec(`INSERT INTO CustomerPayments(CustomerID,InvoiceID,Amount,PaymentDate,ReceivedBy,UserID,ShiftID)
		VALUES(?,?,?,?,?,?,?)`, cust, 2, 2000, Now(), u.UserName, u.ID, id); err != nil {
		t.Fatalf("collection: %v", err)
	}
	if _, err := s.CashBoxIn(ctx, u, id, money.Cents(3000), "إيداع"); err != nil {
		t.Fatalf("cash in: %v", err)
	}
	if _, err := s.CashBoxOut(ctx, u, id, money.Cents(1000), "سحب"); err != nil {
		t.Fatalf("cash out: %v", err)
	}
	if _, err := s.RegisterExpense(ctx, u, "كهرباء", money.Cents(500), "تشغيل"); err != nil {
		t.Fatalf("expense: %v", err)
	}
	sum, err := s.ShiftSummary(ctx, id)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	// Expected = 10000 + 4000 + 2000 + 3000 − 0 − 1000 − 500 = 17500
	if sum.ExpectedBalance != 17500 {
		t.Fatalf("H-09 expected 17500, got %d (cashSales=%d coll=%d in=%d out=%d exp=%d)",
			sum.ExpectedBalance, sum.CashSales, sum.CashDebtCollection, sum.CashIn, sum.CashOut, sum.ExpensesTotal)
	}
	if sum.InvoiceCount != 2 || sum.TotalSales != 6000 {
		t.Fatalf("invoice count/sales wrong: %+v", sum)
	}
}

func TestDailyClose(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b2TestUser(t, s, "admin1", "admin")
	u := b2TestUser(t, s, "cash1", "cashier")
	id, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	pid := testProductB2(t, s, "سكر", 1000, 2000, 10000)
	testSaleInvoice(t, s, u, id, 1, pid, 2000, 2000, 1000, 4000, 0, "Cash", 1)
	cust := testCustomer(t, s, "عميل آجل")
	testSaleInvoice(t, s, u, id, 2, pid, 1000, 2000, 1000, 0, 2000, "Cash", cust)
	if _, err := s.RegisterExpense(ctx, u, "نظافة", money.Cents(500), "تشغيل"); err != nil {
		t.Fatalf("expense: %v", err)
	}
	// Partial return of 1 unit of invoice 1 (refund 2000 cash).
	// do_return is Admin/Manager/Supervisor only — the cashier may
	// not return; the supervisor returns from their own open shift.
	sup := b2TestUser(t, s, "sup1", "supervisor")
	if _, err := s.OpenShift(ctx, sup, money.Cents(10000), false); err != nil {
		t.Fatalf("supervisor open: %v", err)
	}
	if _, err := s.ProcessReturn(ctx, sup, 1, []ReturnLineInput{{ProductID: pid, Qty: 1000}}, "تالف", "Cash"); err != nil {
		t.Fatalf("return: %v", err)
	}
	// Cashier may not read the daily close.
	if _, err := s.DailyClose(ctx, u, ""); err == nil {
		t.Fatal("expected cashier to be denied daily close")
	}
	rep, err := s.DailyClose(ctx, admin, "")
	if err != nil {
		t.Fatalf("daily close: %v", err)
	}
	// totalSales = 4000+2000 = 6000; totalCash = 4000 at-sale; totalCredit = 2000
	// totalReturns = 2000; totalExpenses = 500; cashReturns = 2000
	// netCash = 4000 − 500 − 2000 = 1500
	// profit: lines (4000−2×1000)+(2000−1×1000)=3000, minus return profit (2000−1000)=1000 → 2000
	if rep.TotalSales != 6000 || rep.TotalCash != 4000 || rep.TotalCredit != 2000 {
		t.Fatalf("daily totals wrong: sales=%d cash=%d credit=%d", rep.TotalSales, rep.TotalCash, rep.TotalCredit)
	}
	if rep.TotalReturns != 2000 || rep.TotalExpenses != 500 || rep.CashReturns != 2000 {
		t.Fatalf("returns/expenses wrong: ret=%d exp=%d cashRet=%d", rep.TotalReturns, rep.TotalExpenses, rep.CashReturns)
	}
	if rep.NetCash != 1500 {
		t.Fatalf("netCash expected 1500, got %d", rep.NetCash)
	}
	if rep.TotalProfit != 2000 {
		t.Fatalf("profit expected 2000, got %d", rep.TotalProfit)
	}
	if rep.InvoiceCount != 2 || len(rep.Shifts) != 2 {
		t.Fatalf("counts wrong: invoices=%d shifts=%d", rep.InvoiceCount, len(rep.Shifts))
	}
}

// --- FIX-1 regression tests ---

// fix1Scenario builds: admin shift opening 10000; cash sale 2×2000=4000;
// credit sale 2×2000=4000 (unpaid); cash debt collection 1000;
// Visa debt collection 500; expense 500.
// Expected drawer = 10000 + 4000 + 1000 − 500 = 14500.
func fix1Scenario(t *testing.T, s *Store) (admin *auth.User, shiftID int64, pid int64, custID int64) {
	t.Helper()
	ctx := context.Background()
	admin = b2TestUser(t, s, "fix1admin", "admin")
	var err error
	shiftID, err = s.OpenShift(ctx, admin, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pid = testProductB2(t, s, "سكر", 1000, 2000, 10000)
	if _, err := s.CreateInvoice(ctx, admin, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
		Lines: []SaleLineInput{{ProductID: pid, Qty: 2000}},
	}); err != nil {
		t.Fatalf("cash sale: %v", err)
	}
	custID = testCustomer(t, s, "عميل آجل")
	if _, err := s.CreateInvoice(ctx, admin, CreateInvoiceInput{
		CustomerID: custID, SaleType: "retail", PaymentMethod: "Cash", Paid: 0,
		Lines: []SaleLineInput{{ProductID: pid, Qty: 2000}},
	}); err != nil {
		t.Fatalf("credit sale: %v", err)
	}
	if _, err := s.PayCustomerDebtGeneral(ctx, admin, custID, money.Cents(1000), "Cash", ""); err != nil {
		t.Fatalf("cash collection: %v", err)
	}
	if _, err := s.PayCustomerDebtGeneral(ctx, admin, custID, money.Cents(500), "Visa", ""); err != nil {
		t.Fatalf("visa collection: %v", err)
	}
	if _, err := s.RegisterExpense(ctx, admin, "نظافة", money.Cents(500), "تشغيل"); err != nil {
		t.Fatalf("expense: %v", err)
	}
	return admin, shiftID, pid, custID
}

func TestFix1_DrawerEqualsShiftSummary(t *testing.T) {
	s := testStore(t)
	admin, shiftID, _, _ := fix1Scenario(t, s)
	ctx := context.Background()
	var drawer money.Cents
	if err := s.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		drawer, err = DrawerExpectedBalance(tx, shiftID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// H1: expenses were never subtracted before → 15000; H2: the cash
	// collection was counted twice in the summary → 15500.
	if drawer != 14500 {
		t.Fatalf("drawer expected = %d, want 14500", drawer)
	}
	sum, err := s.ShiftSummary(ctx, shiftID)
	if err != nil {
		t.Fatal(err)
	}
	if sum.ExpectedBalance != 14500 {
		t.Fatalf("summary expected = %d, want 14500", sum.ExpectedBalance)
	}
	if sum.CashDebtCollection != 1000 {
		t.Fatalf("cash collection = %d, want 1000 (H2: must not double-count)", sum.CashDebtCollection)
	}
	if sum.CashIn != 0 {
		t.Fatalf("manual CashIn = %d, want 0 (auto collection row must be excluded)", sum.CashIn)
	}
	if sum.VisaDebtCollection != 500 {
		t.Fatalf("visa collection = %d, want 500", sum.VisaDebtCollection)
	}
	_ = admin
}

func TestFix1_CloseRowNotCountedAfterResume(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b2TestUser(t, s, "fix1admin2", "admin")
	shiftID, err := s.OpenShift(ctx, admin, money.Cents(10000), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseShift(ctx, admin, shiftID); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := s.OpenShift(ctx, admin, 0, true); err != nil {
		t.Fatalf("resume: %v", err)
	}
	var drawer money.Cents
	if err := s.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		drawer, err = DrawerExpectedBalance(tx, shiftID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// H4: the leftover 'close' row (10000) used to be subtracted as an outflow.
	if drawer != 10000 {
		t.Fatalf("drawer after resume = %d, want 10000", drawer)
	}
}

func TestFix1_DailyCloseSplitsCollections(t *testing.T) {
	s := testStore(t)
	fix1Scenario(t, s)
	ctx := context.Background()
	admin := &auth.User{ID: 1, UserName: "fix1admin", Role: "admin"}
	rep, err := s.DailyClose(ctx, admin, "")
	if err != nil {
		t.Fatal(err)
	}
	if rep.CashDebtCollection != 1000 || rep.VisaDebtCollection != 500 {
		t.Fatalf("collections cash=%d visa=%d, want 1000/500", rep.CashDebtCollection, rep.VisaDebtCollection)
	}
	// Only the cash collection enters TotalCash: 4000 at-sale + 1000.
	if rep.TotalCash != 5000 {
		t.Fatalf("totalCash = %d, want 5000 (visa collection must not enter cash)", rep.TotalCash)
	}
}

func TestFix1_CloseShiftCreditIsSalesOnly(t *testing.T) {
	s := testStore(t)
	admin, shiftID, _, _ := fix1Scenario(t, s)
	ctx := context.Background()
	if _, err := s.CloseShift(ctx, admin, shiftID); err != nil {
		t.Fatal(err)
	}
	var credit int64
	if err := s.DB.QueryRow(`SELECT TotalSalesCredit FROM Shifts WHERE ShiftID=?`, shiftID).Scan(&credit); err != nil {
		t.Fatal(err)
	}
	// FIX-2 L-2: TotalSalesCredit is credit SALES only. The scenario has
	// no Visa/Vodafone sales — only a 500 Visa debt collection, which is
	// not a sale — so the total is 0. (FIX-1 pinned the wrong value 500:
	// collections were added to credit sales. This test was updated in
	// FIX-2 to pin the corrected definition.)
	if credit != 0 {
		t.Fatalf("TotalSalesCredit = %d, want 0 (collections are not sales)", credit)
	}
}

// --- FIX-2 regression tests ---

func TestFix2_CancelCrossShiftDrawerEqualsSummary(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b2TestUser(t, s, "fix2admin", "admin")
	shiftA, err := s.OpenShift(ctx, admin, money.Cents(10000), false)
	if err != nil {
		t.Fatal(err)
	}
	pid := testProductB2(t, s, "سكر", 1000, 2000, 10000)
	res, err := s.CreateInvoice(ctx, admin, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
		Lines: []SaleLineInput{{ProductID: pid, Qty: 2000}},
	})
	if err != nil {
		t.Fatalf("cash sale: %v", err)
	}
	// Close shift A (Actual = Expected = 14000), then move it to
	// yesterday: the invoice's shift is closed, so the cancel refund
	// must post in the new shift B opened today.
	if _, err := s.CloseShift(ctx, admin, shiftA); err != nil {
		t.Fatalf("close A: %v", err)
	}
	if _, err := s.DB.Exec(`UPDATE Shifts SET ShiftDate=? WHERE ShiftID=?`, yesterday(), shiftA); err != nil {
		t.Fatal(err)
	}
	shiftB, err := s.OpenShift(ctx, admin, money.Cents(5000), false)
	if err != nil {
		t.Fatalf("open B: %v", err)
	}
	if shiftB == shiftA {
		t.Fatal("B must be a new shift, not a resume of A")
	}
	refund, err := s.CancelInvoice(ctx, admin, res.InvoiceID, "خطأ")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if refund != 4000 {
		t.Fatalf("cash refund = %d, want 4000", refund)
	}
	var drawer money.Cents
	if err := s.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		drawer, err = DrawerExpectedBalance(tx, shiftB)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if drawer != 1000 {
		t.Fatalf("drawer B = %d, want 1000 (5000 opening - 4000 refund)", drawer)
	}
	sum, err := s.ShiftSummary(ctx, shiftB)
	if err != nil {
		t.Fatal(err)
	}
	// H-1: the cross-shift cancel refund must enter CashOut of the
	// refund shift, or drawer and summary disagree by the refund.
	if sum.CashOut != 4000 {
		t.Fatalf("CashOut B = %d, want 4000 (cross-shift cancel refund)", sum.CashOut)
	}
	if sum.ExpectedBalance != 1000 {
		t.Fatalf("summary B = %d, want 1000", sum.ExpectedBalance)
	}
	if sum.ExpectedBalance != drawer {
		t.Fatalf("drawer %d != summary %d", drawer, sum.ExpectedBalance)
	}
	if _, err := s.CloseShift(ctx, admin, shiftB); err != nil {
		t.Fatalf("close B: %v", err)
	}
	var actual, expected int64
	if err := s.DB.QueryRow(`SELECT ActualBalance,ExpectedBalance FROM Shifts WHERE ShiftID=?`, shiftB).Scan(&actual, &expected); err != nil {
		t.Fatal(err)
	}
	if actual != 1000 || expected != 1000 {
		t.Fatalf("close B stored actual=%d expected=%d, want 1000/1000", actual, expected)
	}
}

func TestFix2_CourierCashInSummaryAndDailyClose(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b2TestUser(t, s, "fix2admin2", "admin")
	shiftID := testOpenShift(t, s, admin, 10000)
	id, err := s.CreateCourierDebt(ctx, admin, CourierDebtInput{CourierName: "مندوب", Amount: money.Cents(10000), OrderRef: "ORD-1"})
	if err != nil {
		t.Fatalf("create debt: %v", err)
	}
	if _, err := s.SettleCourierDebt(ctx, admin, id, money.Cents(4000), "Cash", ""); err != nil {
		t.Fatalf("settle: %v", err)
	}
	sum, err := s.ShiftSummary(ctx, shiftID)
	if err != nil {
		t.Fatal(err)
	}
	// L-1: courier cash is its own bucket — not manual CashIn — and it
	// still enters the expected balance exactly once.
	if sum.CourierCollection != 4000 {
		t.Fatalf("courier collection = %d, want 4000", sum.CourierCollection)
	}
	if sum.CashIn != 0 {
		t.Fatalf("manual CashIn = %d, want 0 (courier cash is not manual)", sum.CashIn)
	}
	if sum.ExpectedBalance != 14000 {
		t.Fatalf("expected = %d, want 14000 (10000 + 4000 courier cash)", sum.ExpectedBalance)
	}
	rep, err := s.DailyClose(ctx, admin, "")
	if err != nil {
		t.Fatal(err)
	}
	if rep.CourierCollection != 4000 {
		t.Fatalf("daily courier = %d, want 4000", rep.CourierCollection)
	}
	if rep.TotalCash != 4000 {
		t.Fatalf("daily TotalCash = %d, want 4000 (courier cash is drawer cash)", rep.TotalCash)
	}
}

func TestFix1_TopProductsRoundsCost(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b2TestUser(t, s, "fix1admin3", "admin")
	shiftID := testOpenShift(t, s, admin, 10000)
	pid := testProductB2(t, s, "صنف كسري", 1667, 2000, 10000)
	// 1.5 units at 2000 → TotalPrice 3000; cost 1667×1.5 = 2500.5 → 2501 rounded.
	testSaleInvoice(t, s, admin, shiftID, 1, pid, 1500, 2000, 1667, 3000, 0, "Cash", 1)
	top, err := s.TopProducts(ctx, admin, Today(), Today(), 10, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 1 {
		t.Fatalf("top = %d rows, want 1", len(top))
	}
	if top[0].Profit != 499 {
		t.Fatalf("profit = %d, want 499 (rounded cost 2501, not truncated 2500)", top[0].Profit)
	}
}
