package store

import (
	"context"
	"errors"
	"math"
	"testing"

	"cashierpos-go/internal/money"
)

// fix-21 lane C (R21-C-F1/F2): cancel/return stock-restore additions
// wrapped on absurd stored bxgy grants.
//
// Re-derived mechanism (see lane REPORT.md): EarnedFreeUnitsChecked
// only refuses a grant that does not fit int64, so 1 group x Y =
// 9223372036854775000 fits — and the caller's restore + grant then
// wrapped: 2000 + 9223372036854775000 = -9223372036854774616, and
// stock 97000 + that = -9223372036854677616 (the review's exact
// numbers). The fix refuses in two layers: (A) a stored per-group
// X/Y above MaxBxgyUnits*1000 milliunits is refused in
// EarnedFreeUnitsChecked; (B) every stock-quantity addition in the
// cancel/return paths uses money.AddQtyChecked.

// fix21SeedGrantInvoice inserts an invoice + one detail line + one
// stored InvoiceOffers row directly (a stored/legacy row: the values
// need not be creatable through the app). Stock is left untouched so
// the caller can assert it afterwards. paid > 0 also writes the
// at-sale Cash Payments row.
func fix21SeedGrantInvoice(t *testing.T, s *Store, userID int64, userName string, shiftID int64,
	custID, pid int64, qty money.Qty, buyQty, freeQty int64, paid money.Cents) int64 {
	t.Helper()
	res, err := s.DB.Exec(`INSERT INTO Invoices(InvoiceNumber,CustomerID,SaleType,TotalAmount,Discount,
		PromoDiscount,NetAmount,PaidAmount,RemainingAmount,PaymentMethod,UserID,UserName,ShiftID,InvoiceDate,IsCanceled)
		VALUES(900021,?,'retail',200,0,0,200,?,200,'Cash',?,?,?,?,0)`,
		custID, int64(paid), userID, userName, shiftID, Now())
	if err != nil {
		t.Fatalf("insert fix21 invoice: %v", err)
	}
	invID, _ := res.LastInsertId()
	if _, err := s.DB.Exec(`INSERT INTO InvoiceDetails(InvoiceID,ProductID,ProductName,Quantity,UnitPrice,BuyPrice,TotalPrice)
		VALUES(?,?,?,?,?,?,200)`, invID, pid, "أرز", int64(qty), 100, 1); err != nil {
		t.Fatalf("insert fix21 detail: %v", err)
	}
	// A real Offers row: GetInvoiceOffers scans OfferID into int64,
	// so a NULL OfferID (unlike any app-written row) fails the scan.
	ores, err := s.DB.Exec(`INSERT INTO Offers(Name,Type,Value,Threshold,FreeQty,IsActive)
		VALUES('عرض اختبار','buy_x_get_y','2','0','1',1)`)
	if err != nil {
		t.Fatalf("insert fix21 offer row: %v", err)
	}
	offerID, _ := ores.LastInsertId()
	if _, err := s.DB.Exec(`INSERT INTO InvoiceOffers(InvoiceID,OfferID,OfferName,SavedAmount,ProductID,BuyQty,FreeQty)
		VALUES(?,?,'عرض اختبار',0,?,?,?)`, invID, offerID, pid, buyQty, freeQty); err != nil {
		t.Fatalf("insert fix21 offer: %v", err)
	}
	if paid > 0 {
		if _, err := s.DB.Exec(`INSERT INTO Payments(InvoiceID,CustomerID,Amount,PaymentDate,PaymentMethod,Notes,UserID)
			VALUES(?,?,?,?,'Cash','دفعة عند البيع',?)`, invID, custID, int64(paid), Now(), userID); err != nil {
			t.Fatalf("insert fix21 payment: %v", err)
		}
	}
	return invID
}

func fix21Stock(t *testing.T, s *Store, pid int64) money.Qty {
	t.Helper()
	return productQty(t, s, pid)
}

func fix21CountReturns(t *testing.T, s *Store, invID int64) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM Returns WHERE InvoiceID=?`, invID).Scan(&n); err != nil {
		t.Fatalf("count returns: %v", err)
	}
	return n
}

func fix21IsCanceled(t *testing.T, s *Store, invID int64) int {
	t.Helper()
	var v int
	if err := s.DB.QueryRow(`SELECT IsCanceled FROM Invoices WHERE InvoiceID=?`, invID).Scan(&v); err != nil {
		t.Fatalf("isCanceled: %v", err)
	}
	return v
}

func TestFix21_AddQtyChecked(t *testing.T) {
	// The review's exact pair: grant + sold restore.
	if _, err := money.AddQtyChecked(9223372036854775000, 2000); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("AddQtyChecked(top-band, 2000): err = %v, want money.ErrOverflow", err)
	}
	if _, err := money.AddQtyChecked(math.MaxInt64, 1); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("AddQtyChecked(MaxInt64, 1): err = %v, want money.ErrOverflow", err)
	}
	if _, err := money.AddQtyChecked(math.MinInt64, -1); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("AddQtyChecked(MinInt64, -1): err = %v, want money.ErrOverflow", err)
	}
	if got, err := money.AddQtyChecked(97000, 2000); err != nil || got != 99000 {
		t.Fatalf("AddQtyChecked(97000, 2000) = %d, %v; want 99000, nil", got, err)
	}
	if got, err := money.AddQtyChecked(math.MaxInt64-1, 1); err != nil || got != math.MaxInt64 {
		t.Fatalf("AddQtyChecked(MaxInt64-1, 1) = %d, %v; want MaxInt64, nil", got, err)
	}
	if got, err := money.AddQtyChecked(-5000, 5000); err != nil || got != 0 {
		t.Fatalf("AddQtyChecked(-5000, 5000) = %d, %v; want 0, nil", got, err)
	}
}

func TestFix21_EarnedFreeUnitsCheckedStoredGrantCap(t *testing.T) {
	// Review repro values: kept 2000 milli (2 units), buy 2, Y top-band.
	if _, err := EarnedFreeUnitsChecked(2000, 2000, 9223372036854775000); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("EarnedFreeUnitsChecked(top-band Y): err = %v, want money.ErrOverflow", err)
	}
	if _, err := EarnedFreeUnitsChecked(2000, 9223372036854775000, 1000); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("EarnedFreeUnitsChecked(top-band X): err = %v, want money.ErrOverflow", err)
	}
	// One milliunit above the cap is refused too.
	if _, err := EarnedFreeUnitsChecked(2000, 2000, 100000001); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("EarnedFreeUnitsChecked(Y=cap+1): err = %v, want money.ErrOverflow", err)
	}
	// At the cap is allowed: 1 group x 100000 units = 100000000 milli.
	if got, err := EarnedFreeUnitsChecked(2000, 2000, 100000000); err != nil || got != 100000000 {
		t.Fatalf("EarnedFreeUnitsChecked(Y=cap) = %d, %v; want 100000000, nil", got, err)
	}
	// Normal buy-2-get-1 is exact.
	if got, err := EarnedFreeUnitsChecked(4000, 2000, 1000); err != nil || got != 2000 {
		t.Fatalf("EarnedFreeUnitsChecked(4, 2, 1) = %d, %v; want 2000, nil", got, err)
	}
}

func TestFix21_CancelRefusesTopBandStoredGrant(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b2TestUser(t, s, "f21adm", "admin")
	shiftID, err := s.OpenShift(ctx, admin, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pid := testProductB2(t, s, "أرز", 1, 100, 97000)
	cust := testCustomer(t, s, "عميل اختبار")
	invID := fix21SeedGrantInvoice(t, s, admin.ID, admin.UserName, shiftID, cust, pid,
		2000, 2000, 9223372036854775000, 0)
	if _, err := s.CancelInvoice(ctx, admin, invID, "إلغاء اختبار"); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("CancelInvoice(top-band grant): err = %v, want money.ErrOverflow", err)
	}
	if got := fix21Stock(t, s, pid); got != 97000 {
		t.Fatalf("stock after refused cancel = %d, want 97000 (unchanged)", got)
	}
	if fix21IsCanceled(t, s, invID) != 0 {
		t.Fatalf("invoice was canceled despite the refusal")
	}
	if n := fix21CountReturns(t, s, invID); n != 0 {
		t.Fatalf("Returns rows after refused cancel = %d, want 0", n)
	}
}

func TestFix21_ReturnRefusesTopBandStoredGrant(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mgr := b2TestUser(t, s, "f21mgr", "manager")
	shiftID, err := s.OpenShift(ctx, mgr, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	// 1¢-buy product, as in the review repro (buy=500¢ was refused
	// incidentally via MulQty; buy=1¢ reached the wrapping addition).
	pid := testProductB2(t, s, "أرز", 1, 100, 97000)
	cust := testCustomer(t, s, "عميل اختبار")
	invID := fix21SeedGrantInvoice(t, s, mgr.ID, mgr.UserName, shiftID, cust, pid,
		2000, 2000, 9223372036854775000, 200)
	if _, err := s.ProcessReturn(ctx, mgr, invID, []ReturnLineInput{{ProductID: pid, Qty: 2000}}, "تالف", "Cash"); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("ProcessReturn(top-band grant): err = %v, want money.ErrOverflow", err)
	}
	if got := fix21Stock(t, s, pid); got != 97000 {
		t.Fatalf("stock after refused return = %d, want 97000 (unchanged)", got)
	}
	if n := fix21CountReturns(t, s, invID); n != 0 {
		t.Fatalf("Returns rows after refused return = %d, want 0", n)
	}
}

func TestFix21_CancelCheckedAddRefusesNearMaxGrant(t *testing.T) {
	// Layer B on its own: the grant PASSES the stored-grant cap
	// (Y = 100000 units = 100000000 milli, exactly at the cap) and
	// the earned grant fits int64 — but restore + grant still
	// overflows, so the checked addition must refuse. Numbers:
	// kept = 92233720368000 milli, X = 1000 (buy 1), Y = 100000000;
	// groups = 92233720368, earned = 9223372036800000000 (< MaxInt64);
	// restore + earned = 9223464270520368000 (> MaxInt64, wraps pre-fix).
	s := testStore(t)
	ctx := context.Background()
	admin := b2TestUser(t, s, "f21adm2", "admin")
	shiftID, err := s.OpenShift(ctx, admin, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pid := testProductB2(t, s, "أرز", 1, 100, 97000)
	cust := testCustomer(t, s, "عميل اختبار")
	invID := fix21SeedGrantInvoice(t, s, admin.ID, admin.UserName, shiftID, cust, pid,
		92233720368000, 1000, 100000000, 0)
	if _, err := s.CancelInvoice(ctx, admin, invID, "إلغاء اختبار"); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("CancelInvoice(near-max grant): err = %v, want money.ErrOverflow", err)
	}
	if got := fix21Stock(t, s, pid); got != 97000 {
		t.Fatalf("stock after refused cancel = %d, want 97000 (unchanged)", got)
	}
	if fix21IsCanceled(t, s, invID) != 0 {
		t.Fatalf("invoice was canceled despite the refusal")
	}
}

func TestFix21_ReturnCheckedAddRefusesNearMaxVoided(t *testing.T) {
	// Layer B on the return side: same in-cap grant and absurd sold
	// qty as the cancel test above; the voided free qty
	// (9223372036800000000) fits int64, but the voided restock
	// addition wraps pre-fix (stock already restocked to
	// 92233720465000, + voided = 9223464270520465000 > MaxInt64).
	// The checked addition must refuse and the tx must roll back,
	// so even the plain restock is undone and stock stays 97000.
	s := testStore(t)
	ctx := context.Background()
	mgr := b2TestUser(t, s, "f21mgr2", "manager")
	shiftID, err := s.OpenShift(ctx, mgr, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pid := testProductB2(t, s, "أرز", 1, 100, 97000)
	cust := testCustomer(t, s, "عميل اختبار")
	invID := fix21SeedGrantInvoice(t, s, mgr.ID, mgr.UserName, shiftID, cust, pid,
		92233720368000, 1000, 100000000, 200)
	if _, err := s.ProcessReturn(ctx, mgr, invID, []ReturnLineInput{{ProductID: pid, Qty: 92233720368000}}, "تالف", "Cash"); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("ProcessReturn(near-max voided): err = %v, want money.ErrOverflow", err)
	}
	if got := fix21Stock(t, s, pid); got != 97000 {
		t.Fatalf("stock after refused return = %d, want 97000 (unchanged)", got)
	}
	if n := fix21CountReturns(t, s, invID); n != 0 {
		t.Fatalf("Returns rows after refused return = %d, want 0", n)
	}
}
