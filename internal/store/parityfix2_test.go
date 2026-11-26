// Parity fix-2 tests: PR2-H1 (shift summary after sell → return →
// cancel) and R2-H1 (NotifyOffer '' LinkedProductID breaks the admin
// manage list).
//
// PR2-H1: shiftSummaryTx built CashSales from non-canceled invoices
// only, still subtracted the return leg in CashReturns, and the FIX-2
// H-1 rule excluded the same-shift cancel refund from CashOut — net
// understatement exactly the returned amount, which CloseShift then
// certified as balanced (Expected = Actual, متوازن). The fix excludes
// a canceled same-shift invoice's return legs from CashReturns only up
// to (at-sale cash − same-shift cancel refund): a Visa sale (at-sale
// cash 0) whose return is paid in cash, or a sale whose cash was
// already collected back via debt collections, keeps its return legs
// counted. These tests drive the real sale → return → cancel path and
// assert the summary equals the CashBox-derived drawer, both shapes,
// plus the close certification and the DailyClose net.
//
// R2-H1: NotifyOffer inserted a literal '' for LinkedProductID while
// every other trigger uses nullableInt; ListNotificationsAdmin /
// GetNotification scan COALESCE(LinkedProductID,0) into int64, and
// COALESCE('',0) returns '', so the manage list rendered zero rows.
// The fix stores NULL in the insert and makes the four
// COALESCE(LinkedProductID,0) scans tolerant (NULLIF) so legacy ''
// rows list too.
package store

import (
	"context"
	"database/sql"
	"testing"

	"cashierpos-go/internal/money"
)

// p2Summary fetches the live shift summary for the h1 env shift.
func (e *h1Env) p2Summary(t *testing.T) *ShiftSummary {
	t.Helper()
	sum, err := e.s.ShiftSummary(e.ctx, e.shiftID)
	if err != nil {
		t.Fatalf("ShiftSummary: %v", err)
	}
	return sum
}

// p2AssertSummaryDrawer asserts the summary's ExpectedBalance equals
// the CashBox-derived drawer (opening + in legs − out legs) and equals
// want. The two numbers must agree: the summary is the close control,
// the legs are what actually moved.
func (e *h1Env) p2AssertSummaryDrawer(t *testing.T, want money.Cents) {
	t.Helper()
	sum := e.p2Summary(t)
	if sum.ExpectedBalance != want {
		t.Fatalf("summary ExpectedBalance = %d, want %d (CashSales %d, CashReturns %d, CashOut %d)",
			sum.ExpectedBalance, want, sum.CashSales, sum.CashReturns, sum.CashOut)
	}
	var net int64
	err := e.s.DB.QueryRow(`SELECT COALESCE(SUM(CASE WHEN MovementType='in' THEN Amount
		WHEN MovementType='out' THEN -Amount ELSE 0 END),0)
		FROM CashBox WHERE ShiftID=?`, e.shiftID).Scan(&net)
	if err != nil {
		t.Fatalf("drawer legs: %v", err)
	}
	if drawer := money.Cents(10000) + money.Cents(net); drawer != want {
		t.Fatalf("drawer from CashBox legs = %d, want %d (net legs %d)", drawer, want, net)
	}
}

// Review shape 1: sell 4×2000¢, return 1×2000¢, cancel → summary
// Expected 10000 (was 8000: the return leg was subtracted while the
// sale and its refund were both excluded).
func TestParityFix2_Shape1_SummaryAfterCancel(t *testing.T) {
	e := h1Setup(t)
	invID := e.h1Sell(t, 4) // paid 8000
	e.h1Return(t, invID, 1, "Cash", 2000)
	e.h1PreviewAndCancel(t, invID, 6000)
	e.h1AssertLegs(t, 8000, 2000, 6000)
	sum := e.p2Summary(t)
	if sum.CashSales != 0 || sum.CashReturns != 0 || sum.CashOut != 0 {
		t.Fatalf("summary buckets CashSales/CashReturns/CashOut = %d/%d/%d, want 0/0/0",
			sum.CashSales, sum.CashReturns, sum.CashOut)
	}
	e.p2AssertSummaryDrawer(t, 10000)
	e.h1AssertDrawerBackToOpening(t)
}

// Review shape 2: sell 10×2000¢, return 3×2000¢, cancel → summary
// Expected 10000 (was 4000).
func TestParityFix2_Shape2_SummaryAfterCancel(t *testing.T) {
	e := h1Setup(t)
	invID := e.h1Sell(t, 10) // paid 20000
	e.h1Return(t, invID, 3, "Cash", 6000)
	e.h1PreviewAndCancel(t, invID, 14000)
	e.h1AssertLegs(t, 20000, 6000, 14000)
	e.p2AssertSummaryDrawer(t, 10000)
	e.h1AssertDrawerBackToOpening(t)
}

// The close must certify the TRUE drawer: after shape 1, CloseShift
// persists Expected = Actual = 10000, Difference 0, متوازن (was
// Expected = Actual = 8000 — a false balanced close).
func TestParityFix2_CloseAfterCancelCertifiesTrueDrawer(t *testing.T) {
	e := h1Setup(t)
	invID := e.h1Sell(t, 4)
	e.h1Return(t, invID, 1, "Cash", 2000)
	e.h1PreviewAndCancel(t, invID, 6000)
	if _, err := e.s.CloseShift(e.ctx, e.admin, e.shiftID); err != nil {
		t.Fatalf("CloseShift: %v", err)
	}
	sh, err := e.s.GetShift(e.ctx, e.shiftID)
	if err != nil {
		t.Fatalf("GetShift: %v", err)
	}
	if sh.ExpectedBalance != 10000 || sh.ActualBalance != 10000 || sh.Difference != 0 || sh.DifferenceStatus != "متوازن" {
		t.Fatalf("closed shift Expected/Actual/Difference/Status = %d/%d/%d/%q, want 10000/10000/0/متوازن",
			sh.ExpectedBalance, sh.ActualBalance, sh.Difference, sh.DifferenceStatus)
	}
}

// DailyClose shares the error: same-day sell → return → cancel showed
// NetCash −2000 on a day whose true net is 0.
func TestParityFix2_DailyCloseAfterCancel(t *testing.T) {
	e := h1Setup(t)
	invID := e.h1Sell(t, 4)
	e.h1Return(t, invID, 1, "Cash", 2000)
	e.h1PreviewAndCancel(t, invID, 6000)
	rep, err := e.s.DailyClose(e.ctx, e.admin, Today())
	if err != nil {
		t.Fatalf("DailyClose: %v", err)
	}
	if rep.TotalCash != 0 || rep.CashReturns != 0 || rep.NetCash != 0 {
		t.Fatalf("daily close TotalCash/CashReturns/NetCash = %d/%d/%d, want 0/0/0",
			rep.TotalCash, rep.CashReturns, rep.NetCash)
	}
	// TotalReturns is return ACTIVITY (the Returns row exists), not a
	// cash bucket — it stays 2000 while the cash impact nets to 0.
	if rep.TotalReturns != 2000 {
		t.Fatalf("daily close TotalReturns = %d, want 2000 (return activity, not cash)", rep.TotalReturns)
	}
}

// No-return control: cancel with no prior return — summary was already
// right and must stay right (Expected 10000).
func TestParityFix2_NoReturnCancelControl(t *testing.T) {
	e := h1Setup(t)
	invID := e.h1Sell(t, 5) // paid 10000
	e.h1PreviewAndCancel(t, invID, 10000)
	e.h1AssertLegs(t, 10000, 0, 10000)
	e.p2AssertSummaryDrawer(t, 10000)
}

// Return-without-cancel control: the return leg of a NON-canceled
// invoice must still count (Expected 16000, CashReturns 2000) — the
// fix must not swallow live returns.
func TestParityFix2_ReturnWithoutCancelControl(t *testing.T) {
	e := h1Setup(t)
	invID := e.h1Sell(t, 4) // paid 8000
	e.h1Return(t, invID, 1, "Cash", 2000)
	sum := e.p2Summary(t)
	if sum.CashSales != 8000 || sum.CashReturns != 2000 || sum.ExpectedBalance != 16000 {
		t.Fatalf("summary CashSales/CashReturns/Expected = %d/%d/%d, want 8000/2000/16000",
			sum.CashSales, sum.CashReturns, sum.ExpectedBalance)
	}
	e.p2AssertSummaryDrawer(t, 16000)
}

// Cap guard — cash return on a non-cash sale: Visa sale 2×2000¢,
// return 1 unit in cash (2000¢ leaves the drawer), cancel refund 0.
// At-sale cash is 0, so the cap is 0 and the return leg MUST stay
// counted: summary Expected 8000 = drawer 8000. A blanket exclusion
// of canceled-invoice return legs would report 10000 here.
func TestParityFix2_CashReturnOnCardSaleSummary(t *testing.T) {
	e := h1Setup(t)
	res, err := e.s.CreateInvoice(e.ctx, e.admin, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Visa", Paid: 4000,
		Lines: []SaleLineInput{{ProductID: e.pid, Qty: 2000}},
	})
	if err != nil {
		t.Fatalf("visa sale: %v", err)
	}
	invID := res.InvoiceID
	e.h1Return(t, invID, 1, "Cash", 2000)
	e.h1PreviewAndCancel(t, invID, 0)
	sum := e.p2Summary(t)
	if sum.CashReturns != 2000 || sum.ExpectedBalance != 8000 {
		t.Fatalf("summary CashReturns/Expected = %d/%d, want 2000/8000 (cap 0: Visa sale brought no cash in)",
			sum.CashReturns, sum.ExpectedBalance)
	}
	e.p2AssertSummaryDrawer(t, 8000)
}

// R2-H1: fire a live offer, then the admin manage list must render its
// definition row (before the fix ListNotificationsAdmin failed with a
// Scan error: COALESCE('',0) returns ''), and GetNotification must
// work too. The definition row stores NULL for LinkedProductID.
func TestParityFix2_OfferFireThenManageList(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	cashier := b2TestUser(t, s, "p2cash", "cashier")
	p := b1MustProduct(t, s, admin, "زيت")
	b1OpenShiftFor(t, s, cashier)
	b1CashCustomerSale(t, s, b1SaleEnv{ctx: ctx, admin: admin, cashier: cashier}, cashier, p.ID, 1000)

	_, err := s.CreateOffer(ctx, admin, OfferInput{
		Name: "عرض الزيت", Type: OfferProductPct, Percent: 10, IsActive: true,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p.ID}},
	})
	if err != nil {
		t.Fatalf("CreateOffer live: %v", err)
	}
	if n := inboxCount(t, s, cashier); n != 1 {
		t.Fatalf("cashier unread after live offer = %d, want 1", n)
	}
	// The definition row must carry NULL (not '') for LinkedProductID.
	var linked sql.NullInt64
	var linkedType string
	err = s.DB.QueryRow(`SELECT LinkedProductID, typeof(LinkedProductID) FROM Notifications
		WHERE UserName='' AND Type='offer' AND NotificationID=?`, offerNotificationDefID(t, s)).Scan(&linked, &linkedType)
	if err != nil {
		t.Fatalf("offer definition row: %v", err)
	}
	if linked.Valid || linkedType != "null" {
		t.Fatalf("offer definition LinkedProductID = %v (%s), want NULL", linked, linkedType)
	}
	// The admin manage list must render the row (this Scan-failed
	// before the fix) and GetNotification must open it.
	list, err := s.ListNotificationsAdmin(ctx, admin)
	if err != nil {
		t.Fatalf("ListNotificationsAdmin after offer fired: %v", err)
	}
	found := false
	var defID int64
	for _, row := range list {
		if row.Type == NotifOffer {
			found = true
			defID = row.ID
			if row.LinkedProductID != 0 {
				t.Fatalf("manage-list offer row LinkedProductID = %d, want 0 (NULL)", row.LinkedProductID)
			}
		}
	}
	if !found {
		t.Fatalf("manage list has no offer row (rows: %d)", len(list))
	}
	got, err := s.GetNotification(ctx, admin, defID)
	if err != nil {
		t.Fatalf("GetNotification(offer def): %v", err)
	}
	if got.Type != NotifOffer || got.LinkedProductID != 0 {
		t.Fatalf("GetNotification = %+v, want offer row with LinkedProductID 0", got)
	}
}

// offerNotificationDefID returns the definition id of the offer
// notification created by the last live offer (UserName='' rows).
func offerNotificationDefID(t *testing.T, s *Store) int64 {
	t.Helper()
	var id int64
	err := s.DB.QueryRow(`SELECT NotificationID FROM Notifications
		WHERE UserName='' AND Type='offer' ORDER BY NotificationID DESC LIMIT 1`).Scan(&id)
	if err != nil {
		t.Fatalf("offer definition id: %v", err)
	}
	return id
}

// R2-H1 (legacy rows): a definition row already stored with '' for
// LinkedProductID (written before the fix) must still list and open —
// the scans are tolerant via NULLIF.
func TestParityFix2_LegacyEmptyLinkedProductIDLists(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	res, err := s.DB.Exec(`INSERT INTO Notifications
		(Type,Title,Body,LinkedProductID,Audience,UserName,IsRead,CreatedAt,SendAt,Status)
		VALUES('offer','عرض قديم','عرض من قبل الإصلاح','', 'all','',0,?,?,'sent')`, Now(), Now())
	if err != nil {
		t.Fatalf("insert legacy '' row: %v", err)
	}
	legacyID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("legacy row id: %v", err)
	}
	list, err := s.ListNotificationsAdmin(ctx, admin)
	if err != nil {
		t.Fatalf("ListNotificationsAdmin with legacy '' row: %v", err)
	}
	found := false
	for _, row := range list {
		if row.ID == legacyID {
			found = true
			if row.LinkedProductID != 0 {
				t.Fatalf("legacy row LinkedProductID = %d, want 0", row.LinkedProductID)
			}
		}
	}
	if !found {
		t.Fatalf("legacy '' row missing from manage list (rows: %d)", len(list))
	}
	got, err := s.GetNotification(ctx, admin, legacyID)
	if err != nil {
		t.Fatalf("GetNotification(legacy '' row): %v", err)
	}
	if got.LinkedProductID != 0 {
		t.Fatalf("GetNotification legacy LinkedProductID = %d, want 0", got.LinkedProductID)
	}
	// The inbox path must stay intact too (it shares the scan shape).
	if _, err := s.ListInbox(ctx, admin, 10); err != nil {
		t.Fatalf("ListInbox: %v", err)
	}
}
