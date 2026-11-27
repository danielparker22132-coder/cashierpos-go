package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/money"
)

// --- FIX-24 lane F: refund-cap SUM swallow (domain_returns.go:258) ---
//
// ProcessReturn computed the refund cap from
// `SELECT COALESCE(SUM(TotalRefund),0) FROM Returns WHERE InvoiceID=?`
// with `_ = ...Scan(&alreadyRefunded)`, so on SUM overflow (absurd
// stored Returns rows, planted here by direct DB write — the app's own
// writers can never store such rows) alreadyRefunded stayed 0 and the
// cap inflated to the full net. The cash path failed closed only
// downstream (shiftSummaryTx overflows on the same poison rows and
// rolls the tx back), but a refund fully absorbed by debt (paidOut==0,
// cashRefund==0) never runs that backstop and POSTED: genuine remaining
// debt wiped, a Returns row stored, stock restocked.
//
// The fix checks the SUM scan error at :258 via sumScanErr (as the
// shift summary does), mapping an integer overflow to
// money.ErrOverflow, before any posting. ProcessReturnAll delegates to
// ProcessReturn, so it inherits the refusal.
//
// On pre-fix code the debt-absorbed and return-all poison tests FAIL
// (the return posts instead of refusing); the cash and partial-debt
// poison tests pass on both versions (they already refused downstream)
// and are regression guards. The cap-control and normal-return tests
// prove the fix changes nothing for non-overflow data.

// fix24FPoison is one absurd stored Returns row: TotalRefund 5e18,
// Quantity 0 (so the qty-availability check is untouched — the money
// cap is isolated). Two of them overflow the int64 SUM.
const fix24FPoison = money.Cents(5000000000000000000)

func fix24FSetup(t *testing.T) (*Store, *auth.User, int64, int64, int64) {
	t.Helper()
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "mgr24f", "manager")
	shiftID, err := s.OpenShift(ctx, u, money.Cents(100000), false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	pid := testProductB2(t, s, "أرز", 100, 1000, 97000)
	// Simulate the sale having taken 10 units out of stock.
	if _, err := s.DB.Exec(`UPDATE Products SET Quantity=87000 WHERE ProductID=?`, pid); err != nil {
		t.Fatal(err)
	}
	cust := testCustomer(t, s, "عميل اختبار")
	return s, u, shiftID, pid, cust
}

// fix24FInvoice inserts a 10-unit × 1000¢ invoice (net 10000) with the
// given paid/remaining split, and sets the customer balance to the
// remaining debt (as a real credit sale would).
func fix24FInvoice(t *testing.T, s *Store, u *auth.User, shiftID, pid, cust int64, paid, remaining money.Cents) int64 {
	t.Helper()
	invID := fix17SaleInvoice(t, s, u, shiftID, 1, pid, 10000, 1000, 100, 0, 0, 10000, 0, 0, 10000, paid, remaining, "Cash", cust)
	if _, err := s.DB.Exec(`UPDATE Customers SET Balance=? WHERE CustomerID=?`, int64(remaining), cust); err != nil {
		t.Fatal(err)
	}
	return invID
}

func fix24FPlantReturns(t *testing.T, s *Store, invID, pid, shiftID int64, amounts ...money.Cents) {
	t.Helper()
	for _, amt := range amounts {
		if _, err := s.DB.Exec(`INSERT INTO Returns(InvoiceID,ProductID,ProductName,Quantity,UnitPrice,BuyPrice,TotalRefund,Reason,UserID,UserName,ReturnDate,ReturnPaymentMethod,ShiftID,IsProcessed)
			VALUES(?,?,'أرز',0,1000,100,?,'plant',1,'manager',?,'Cash',?,1)`,
			invID, pid, int64(amt), Today(), shiftID); err != nil {
			t.Fatalf("plant returns row %d: %v", int64(amt), err)
		}
	}
}

func fix24FReturnsCount(t *testing.T, s *Store, invID int64) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM Returns WHERE InvoiceID=?`, invID).Scan(&n); err != nil {
		t.Fatalf("returns count: %v", err)
	}
	return n
}

func fix24FRemaining(t *testing.T, s *Store, invID int64) int64 {
	t.Helper()
	var r int64
	if err := s.DB.QueryRow(`SELECT RemainingAmount FROM Invoices WHERE InvoiceID=?`, invID).Scan(&r); err != nil {
		t.Fatalf("remaining: %v", err)
	}
	return r
}

func fix24FBalance(t *testing.T, s *Store, cust int64) int64 {
	t.Helper()
	var b int64
	if err := s.DB.QueryRow(`SELECT Balance FROM Customers WHERE CustomerID=?`, cust).Scan(&b); err != nil {
		t.Fatalf("balance: %v", err)
	}
	return b
}

func fix24FExpectOverflowRefusal(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected refusal, got nil error (return posted)", what)
	}
	if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("%s: err = %v, want money.ErrOverflow", what, err)
	}
}

func TestFix24F_DebtAbsorbedPoisonRefused(t *testing.T) {
	s, u, shiftID, pid, cust := fix24FSetup(t)
	ctx := context.Background()
	invID := fix24FInvoice(t, s, u, shiftID, pid, cust, 9000, 1000)
	fix24FPlantReturns(t, s, invID, pid, shiftID, fix24FPoison, fix24FPoison)
	if _, err := s.ProcessReturn(ctx, u, invID, []ReturnLineInput{{ProductID: pid, Qty: 1000}}, "اختبار", "Cash"); err == nil {
		t.Fatal("debt-absorbed poison return posted, want money.ErrOverflow refusal")
	} else if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("debt-absorbed poison err = %v, want money.ErrOverflow", err)
	}
	if n := fix24FReturnsCount(t, s, invID); n != 2 {
		t.Fatalf("Returns rows = %d, want 2 (no new row posted)", n)
	}
	if r := fix24FRemaining(t, s, invID); r != 1000 {
		t.Fatalf("remaining = %d, want 1000", r)
	}
	if b := fix24FBalance(t, s, cust); b != 1000 {
		t.Fatalf("customer balance = %d, want 1000", b)
	}
	if q := productQty(t, s, pid); q != 87000 {
		t.Fatalf("stock = %d, want 87000 (no restock)", q)
	}
}

func TestFix24F_CashPoisonRefused(t *testing.T) {
	s, u, shiftID, pid, cust := fix24FSetup(t)
	ctx := context.Background()
	invID := fix24FInvoice(t, s, u, shiftID, pid, cust, 10000, 0)
	fix24FPlantReturns(t, s, invID, pid, shiftID, fix24FPoison, fix24FPoison)
	_, err := s.ProcessReturn(ctx, u, invID, []ReturnLineInput{{ProductID: pid, Qty: 1000}}, "اختبار", "Cash")
	fix24FExpectOverflowRefusal(t, err, "cash poison")
	if n := fix24FReturnsCount(t, s, invID); n != 2 {
		t.Fatalf("Returns rows = %d, want 2", n)
	}
	if q := productQty(t, s, pid); q != 87000 {
		t.Fatalf("stock = %d, want 87000", q)
	}
}

func TestFix24F_PartialDebtPoisonRefused(t *testing.T) {
	s, u, shiftID, pid, cust := fix24FSetup(t)
	ctx := context.Background()
	invID := fix24FInvoice(t, s, u, shiftID, pid, cust, 9500, 500)
	fix24FPlantReturns(t, s, invID, pid, shiftID, fix24FPoison, fix24FPoison)
	_, err := s.ProcessReturn(ctx, u, invID, []ReturnLineInput{{ProductID: pid, Qty: 1000}}, "اختبار", "Cash")
	fix24FExpectOverflowRefusal(t, err, "partial-debt poison")
	if r := fix24FRemaining(t, s, invID); r != 500 {
		t.Fatalf("remaining = %d, want 500", r)
	}
}

func TestFix24F_ReturnAllPoisonRefused(t *testing.T) {
	s, u, shiftID, pid, cust := fix24FSetup(t)
	ctx := context.Background()
	// Fully unpaid invoice (paid 0, remaining 10000): return-all refunds
	// the full 10000, all of it debt-absorbed (cashRefund == 0), so the
	// cash-leg backstop never runs and the hole is reachable — a paid
	// invoice's return-all would take a cash leg and fail closed
	// downstream even pre-fix.
	invID := fix24FInvoice(t, s, u, shiftID, pid, cust, 0, 10000)
	fix24FPlantReturns(t, s, invID, pid, shiftID, fix24FPoison, fix24FPoison)
	_, err := s.ProcessReturnAll(ctx, u, invID, "إرجاع كامل", "Cash")
	fix24FExpectOverflowRefusal(t, err, "return-all poison")
	if n := fix24FReturnsCount(t, s, invID); n != 2 {
		t.Fatalf("Returns rows = %d, want 2 (no new row posted)", n)
	}
	if r := fix24FRemaining(t, s, invID); r != 10000 {
		t.Fatalf("remaining = %d, want 10000", r)
	}
	if b := fix24FBalance(t, s, cust); b != 10000 {
		t.Fatalf("customer balance = %d, want 10000", b)
	}
	if q := productQty(t, s, pid); q != 87000 {
		t.Fatalf("stock = %d, want 87000 (no restock)", q)
	}
}

func TestFix24F_NormalDebtReturnWorks(t *testing.T) {
	s, u, shiftID, pid, cust := fix24FSetup(t)
	ctx := context.Background()
	invID := fix24FInvoice(t, s, u, shiftID, pid, cust, 9000, 1000)
	res, err := s.ProcessReturn(ctx, u, invID, []ReturnLineInput{{ProductID: pid, Qty: 1000}}, "تالف", "Cash")
	if err != nil {
		t.Fatalf("normal debt return: %v", err)
	}
	if res.TotalRefund != 1000 || res.DebtReduction != 1000 || res.CashRefund != 0 {
		t.Fatalf("normal debt refund wrong: %+v (want 1000/1000/0)", res)
	}
	if r := fix24FRemaining(t, s, invID); r != 0 {
		t.Fatalf("remaining = %d, want 0", r)
	}
	if b := fix24FBalance(t, s, cust); b != 0 {
		t.Fatalf("customer balance = %d, want 0", b)
	}
	if q := productQty(t, s, pid); q != 88000 {
		t.Fatalf("stock = %d, want 88000 (1 unit restocked)", q)
	}
}

func TestFix24F_NormalCashReturnWorks(t *testing.T) {
	s, u, shiftID, pid, cust := fix24FSetup(t)
	ctx := context.Background()
	invID := fix24FInvoice(t, s, u, shiftID, pid, cust, 10000, 0)
	res, err := s.ProcessReturn(ctx, u, invID, []ReturnLineInput{{ProductID: pid, Qty: 1000}}, "تالف", "Cash")
	if err != nil {
		t.Fatalf("normal cash return: %v", err)
	}
	if res.TotalRefund != 1000 || res.CashRefund != 1000 || res.DebtReduction != 0 {
		t.Fatalf("normal cash refund wrong: %+v (want 1000/1000/0)", res)
	}
	if q := productQty(t, s, pid); q != 88000 {
		t.Fatalf("stock = %d, want 88000", q)
	}
}

func TestFix24F_NonOverflowCapStillApplies(t *testing.T) {
	s, u, shiftID, pid, cust := fix24FSetup(t)
	ctx := context.Background()
	invID := fix24FInvoice(t, s, u, shiftID, pid, cust, 10000, 0)
	fix24FPlantReturns(t, s, invID, pid, shiftID, 4000, 4000)
	res, err := s.ProcessReturn(ctx, u, invID, []ReturnLineInput{{ProductID: pid, Qty: 3000}}, "تالف", "Cash")
	if err != nil {
		t.Fatalf("cap-control return: %v", err)
	}
	if res.TotalRefund != 2000 {
		t.Fatalf("cap-control refund = %d, want 2000 (cap net 10000 - already 8000)", res.TotalRefund)
	}
}

// TestFix24F_CapSumIsChecked is a source guard: the refund-cap SUM
// scan must go through sumScanErr, never a swallowed `_ =` scan.
func TestFix24F_CapSumIsChecked(t *testing.T) {
	b, err := os.ReadFile("domain_returns.go")
	if err != nil {
		t.Fatalf("read domain_returns.go: %v", err)
	}
	src := string(b)
	if strings.Contains(src, "_ = tx.QueryRow(`SELECT COALESCE(SUM(TotalRefund),0) FROM Returns") {
		t.Fatal("refund-cap SUM scan is still swallowed with `_ =`")
	}
	if !strings.Contains(src, "sumScanErr(tx.QueryRow(`SELECT COALESCE(SUM(TotalRefund),0) FROM Returns") {
		t.Fatal("refund-cap SUM scan does not go through sumScanErr")
	}
}
