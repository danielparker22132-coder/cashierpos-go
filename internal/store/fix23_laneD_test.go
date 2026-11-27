package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"cashierpos-go/internal/money"
)

// --- FIX-23 lane D: R23-E-F1 + R23-E-F2 (shift summary overflows) ---
//
// R23-E-F1: shiftSummaryTx returned the raw SQLite "integer overflow"
// error from the Returns / Expenses SUM scans, and apiShiftSummary
// mapped ANY summary error to 404 with the string verbatim. The fix
// maps both SUM sites through sumScanErr (money.ErrOverflow, Arabic)
// and the handler now 400s an overflow on an existing shift (404 stays
// correct only for a genuinely missing shift).
//
// R23-E-F2: shiftSummaryTx accumulated the invoice-driven totals with
// plain +=, so two 5e18-net invoices wrapped TotalSales/CashSales to a
// negative with no error at all. The fix accumulates them with
// shiftSummaryAdd (money.AddChecked), so the summary refuses with the
// Arabic overflow message instead of returning wrapped numbers.
//
// Poison values are written by direct DB insert only (legacy poison);
// no app path is used to store them. Quantities in testSaleInvoice
// calls are milliunits (2000 = 2 units), like the fix-22 E2 tests.

const fix23DBig = int64(5000000000000000000) // 5e18

func TestFix23D_ReturnsSumOverflowRefuses(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "mgr1", "manager")
	shiftID, err := s.OpenShift(ctx, u, money.Cents(100000), false)
	if err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	pid := testProductB2(t, s, "أرز", 1, 100, 100000)
	cust := testCustomer(t, s, "عميل اختبار")
	invID := testSaleInvoice(t, s, u, shiftID, 1, pid, 2000, 100, 1, 200, 0, "Cash", cust)
	for i := 0; i < 2; i++ {
		if _, err := s.DB.Exec(`INSERT INTO Returns(InvoiceID,ProductID,ProductName,Quantity,UnitPrice,TotalRefund,ReturnDate,ShiftID,UserID,IsProcessed)
			VALUES(?,?,?,?,?,?,?,?,?,1)`, invID, pid, "أرز", 1000, 100, fix23DBig, Today(), shiftID, u.ID); err != nil {
			t.Fatalf("insert poisoned return: %v", err)
		}
	}
	sum, err := s.ShiftSummary(ctx, shiftID)
	if err == nil {
		t.Fatalf("expected summary to refuse on Returns SUM overflow, got %+v", sum)
	}
	if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("summary err = %v, want money.ErrOverflow", err)
	}
	if strings.Contains(err.Error(), "SQL logic error") || strings.Contains(err.Error(), "integer overflow") {
		t.Fatalf("summary err leaks raw SQLite text: %v", err)
	}
}

func TestFix23D_ExpensesSumOverflowRefuses(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "mgr1", "manager")
	shiftID, err := s.OpenShift(ctx, u, money.Cents(100000), false)
	if err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := s.DB.Exec(`INSERT INTO Expenses(Amount,Description,Category,ExpenseDate,ShiftID,UserID)
			VALUES(?,?,?,?,?,?)`, fix23DBig, "poison", "عام", Today(), shiftID, u.ID); err != nil {
			t.Fatalf("insert poisoned expense: %v", err)
		}
	}
	sum, err := s.ShiftSummary(ctx, shiftID)
	if err == nil {
		t.Fatalf("expected summary to refuse on Expenses SUM overflow, got %+v", sum)
	}
	if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("summary err = %v, want money.ErrOverflow", err)
	}
	if strings.Contains(err.Error(), "SQL logic error") || strings.Contains(err.Error(), "integer overflow") {
		t.Fatalf("summary err leaks raw SQLite text: %v", err)
	}
}

// TestFix23D_TwoAbsurdInvoicesRefuse covers the R23-E-F2 live repro:
// two directly-inserted invoices with NetAmount=PaidAmount=5e18, Cash,
// no Payments rows. Pre-fix the summary returned 200 with
// TotalSales -8446744073709551616; post-fix it must refuse with
// money.ErrOverflow, and close must refuse too (never persist the
// wrapped totals).
func TestFix23D_TwoAbsurdInvoicesRefuse(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "mgr1", "manager")
	shiftID, err := s.OpenShift(ctx, u, money.Cents(100000), false)
	if err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	cust := testCustomer(t, s, "عميل اختبار")
	for _, n := range []int64{101, 102} {
		if _, err := s.DB.Exec(`INSERT INTO Invoices(InvoiceNumber,CustomerID,ShiftID,TotalAmount,Discount,PromoDiscount,NetAmount,PaidAmount,RemainingAmount,PaymentMethod,UserName,InvoiceDate,IsCanceled)
			VALUES(?,?,?,?,?,?,?,?,?,'Cash',?, ?,0)`,
			n, cust, shiftID, fix23DBig, 0, 0, fix23DBig, fix23DBig, 0, u.UserName, Today()); err != nil {
			t.Fatalf("insert absurd invoice: %v", err)
		}
	}
	sum, err := s.ShiftSummary(ctx, shiftID)
	if err == nil {
		t.Fatalf("expected summary to refuse on wrapped totals, got TotalSales=%d CashSales=%d ExpectedBalance=%d",
			sum.TotalSales, sum.CashSales, sum.ExpectedBalance)
	}
	if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("summary err = %v, want money.ErrOverflow", err)
	}
	if _, err := s.CloseShift(ctx, u, shiftID); err == nil {
		t.Fatal("expected close to refuse on absurd shift")
	} else if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("close err = %v, want money.ErrOverflow", err)
	}
	var isOpen int
	_ = s.DB.QueryRow(`SELECT IsOpen FROM Shifts WHERE ShiftID=?`, shiftID).Scan(&isOpen)
	if isOpen != 1 {
		t.Fatalf("poisoned shift must stay open after refused close, IsOpen=%d", isOpen)
	}
}

// TestFix23D_AtSalePaymentsWrapRefuses isolates the at-sale
// accumulation (:708-712): one small-net invoice whose two at-sale
// Payments rows are 5e18 each. TotalSales stays small, so only the
// checked CashSales accumulation can refuse.
func TestFix23D_AtSalePaymentsWrapRefuses(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "mgr1", "manager")
	shiftID, err := s.OpenShift(ctx, u, money.Cents(100000), false)
	if err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	pid := testProductB2(t, s, "أرز", 1, 100, 100000)
	cust := testCustomer(t, s, "عميل اختبار")
	invID := testSaleInvoice(t, s, u, shiftID, 1, pid, 1000, 100, 1, 100, 0, "Cash", cust)
	for i := 0; i < 2; i++ {
		if _, err := s.DB.Exec(`INSERT INTO Payments(InvoiceID,CustomerID,Amount,PaymentDate,PaymentMethod,Notes,UserID)
			VALUES(?,?,?,?,?,'دفعة عند البيع',?)`, invID, cust, fix23DBig, Now(), "Cash", u.ID); err != nil {
			t.Fatalf("insert poisoned at-sale payment: %v", err)
		}
	}
	sum, err := s.ShiftSummary(ctx, shiftID)
	if err == nil {
		t.Fatalf("expected summary to refuse on at-sale wrap, got CashSales=%d", sum.CashSales)
	}
	if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("summary err = %v, want money.ErrOverflow", err)
	}
}

// TestFix23D_FallbackWrapRefuses isolates the fallback accumulation
// (:755-759): two invoices with small nets but PaidAmount 5e18 each
// and no Payments rows at all, so each falls back to its PaidAmount
// under Cash. TotalSales stays small; the checked fallback CashSales
// accumulation must refuse on the second invoice.
func TestFix23D_FallbackWrapRefuses(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "mgr1", "manager")
	shiftID, err := s.OpenShift(ctx, u, money.Cents(100000), false)
	if err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	cust := testCustomer(t, s, "عميل اختبار")
	for _, n := range []int64{101, 102} {
		if _, err := s.DB.Exec(`INSERT INTO Invoices(InvoiceNumber,CustomerID,ShiftID,TotalAmount,Discount,PromoDiscount,NetAmount,PaidAmount,RemainingAmount,PaymentMethod,UserName,InvoiceDate,IsCanceled)
			VALUES(?,?,?,?,?,?,?,?,?,'Cash',?, ?,0)`,
			n, cust, shiftID, 100, 0, 0, 100, fix23DBig, 0, u.UserName, Today()); err != nil {
			t.Fatalf("insert fallback invoice: %v", err)
		}
	}
	sum, err := s.ShiftSummary(ctx, shiftID)
	if err == nil {
		t.Fatalf("expected summary to refuse on fallback wrap, got CashSales=%d TotalSales=%d", sum.CashSales, sum.TotalSales)
	}
	if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("summary err = %v, want money.ErrOverflow", err)
	}
}

// TestFix23D_CollectionsWrapRefuses isolates the collections
// accumulation (:780-782): two CustomerPayments rows of 5e18 each
// stamped with this shift. No invoices at all, so only the checked
// CashDebtCollection accumulation can refuse.
func TestFix23D_CollectionsWrapRefuses(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "mgr1", "manager")
	shiftID, err := s.OpenShift(ctx, u, money.Cents(100000), false)
	if err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	cust := testCustomer(t, s, "عميل اختبار")
	for i := 0; i < 2; i++ {
		if _, err := s.DB.Exec(`INSERT INTO CustomerPayments(CustomerID,Amount,PaymentDate,PaymentMethod,ShiftID,UserID)
			VALUES(?,?,?,?,?,?)`, cust, fix23DBig, Now(), "Cash", shiftID, u.ID); err != nil {
			t.Fatalf("insert poisoned collection: %v", err)
		}
	}
	sum, err := s.ShiftSummary(ctx, shiftID)
	if err == nil {
		t.Fatalf("expected summary to refuse on collections wrap, got CashDebtCollection=%d ExpectedBalance=%d",
			sum.CashDebtCollection, sum.ExpectedBalance)
	}
	if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("summary err = %v, want money.ErrOverflow", err)
	}
}

// TestFix23D_NormalSummaryUnchanged is the no-behaviour-change guard
// for R23-E-F2: on realistic values the checked chain must agree
// exactly with the old plain-+= totals (TotalSales 200, CashSales 200,
// ExpectedBalance 100000+200).
func TestFix23D_NormalSummaryUnchanged(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "mgr1", "manager")
	shiftID, err := s.OpenShift(ctx, u, money.Cents(100000), false)
	if err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	pid := testProductB2(t, s, "أرز", 1, 100, 100000)
	cust := testCustomer(t, s, "عميل اختبار")
	testSaleInvoice(t, s, u, shiftID, 1, pid, 2000, 100, 1, 200, 0, "Cash", cust)
	sum, err := s.ShiftSummary(ctx, shiftID)
	if err != nil {
		t.Fatalf("normal summary refused: %v", err)
	}
	if sum.TotalSales != 200 || sum.CashSales != 200 || sum.ExpectedBalance != 100200 {
		t.Fatalf("normal summary = TotalSales %d CashSales %d ExpectedBalance %d, want 200/200/100200",
			sum.TotalSales, sum.CashSales, sum.ExpectedBalance)
	}
}
