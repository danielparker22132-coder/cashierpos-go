package store

import (
	"context"
	"strings"
	"testing"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/money"
)

func TestCashBoxInOut(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "cash1", "cashier")
	id, err := s.OpenShift(ctx, u, money.Cents(5000), false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := s.CashBoxIn(ctx, u, id, money.Cents(0), "صفر"); err == nil {
		t.Fatal("expected zero amount to be rejected")
	}
	if _, err := s.CashBoxIn(ctx, u, id, money.Cents(1000), ""); err == nil {
		t.Fatal("expected missing reason to be rejected")
	}
	if _, err := s.CashBoxIn(ctx, u, id, money.Cents(2000), "إيداع"); err != nil {
		t.Fatalf("cash in: %v", err)
	}
	if _, err := s.CashBoxOut(ctx, u, id, money.Cents(3000), "سحب"); err != nil {
		t.Fatalf("cash out: %v", err)
	}
	sum, err := s.ShiftSummary(ctx, id)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	// 5000 + 2000 − 3000 = 4000
	if sum.ExpectedBalance != 4000 || sum.CashIn != 2000 || sum.CashOut != 3000 {
		t.Fatalf("summary wrong: %+v", sum)
	}
}

func TestCashBoxOutExceedingDrawerRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "cash1", "cashier")
	id, err := s.OpenShift(ctx, u, money.Cents(5000), false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// More than the drawer holds: refused (negative forbidden).
	if _, err := s.CashBoxOut(ctx, u, id, money.Cents(5001), "سحب كبير"); err == nil {
		t.Fatal("expected withdrawal exceeding the drawer to be rejected")
	}
	// Exactly the balance: allowed.
	if _, err := s.CashBoxOut(ctx, u, id, money.Cents(5000), "سحب كل الرصيد"); err != nil {
		t.Fatalf("exact-balance withdrawal must be allowed: %v", err)
	}
	sum, _ := s.ShiftSummary(ctx, id)
	if sum.ExpectedBalance != 0 {
		t.Fatalf("expected drawer 0 after exact withdrawal, got %d", sum.ExpectedBalance)
	}
	// Nothing left: any further withdrawal is refused.
	if _, err := s.CashBoxOut(ctx, u, id, money.Cents(1), "سحب بعد التصفير"); err == nil {
		t.Fatal("expected withdrawal from an empty drawer to be rejected")
	}
}

func TestCashBoxListVisibility(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u1 := b2TestUser(t, s, "cash1", "cashier")
	u2 := b2TestUser(t, s, "cash2", "cashier")
	admin := b2TestUser(t, s, "admin1", "admin")
	id1, _ := s.OpenShift(ctx, u1, money.Cents(1000), false)
	id2, _ := s.OpenShift(ctx, u2, money.Cents(1000), false)
	if _, err := s.CashBoxIn(ctx, u1, id1, money.Cents(100), "أ"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CashBoxIn(ctx, u2, id2, money.Cents(200), "ب"); err != nil {
		t.Fatal(err)
	}
	// A cashier sees only their own movements.
	own, err := s.ListCashBoxMovements(ctx, u1, "", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, m := range own {
		if m.UserID != u1.ID {
			t.Fatalf("cashier saw another user's movement: %+v", m)
		}
	}
	if len(own) == 0 {
		t.Fatal("expected own movements")
	}
	// Admin sees all.
	all, err := s.ListCashBoxMovements(ctx, admin, "", 0)
	if err != nil {
		t.Fatalf("admin list: %v", err)
	}
	if len(all) < 4 { // 2 open + 2 in
		t.Fatalf("admin should see all movements, got %d", len(all))
	}
}

func TestCashCountAndReview(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "cash1", "cashier")
	admin := b2TestUser(t, s, "admin1", "admin")
	id, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	cc, err := s.RecordCashCount(ctx, u, id, money.Cents(10500), "", "")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if cc.Difference != 500 || cc.DifferenceStatus != "زيادة" || cc.ExpectedAmount != 10000 {
		t.Fatalf("count wrong: %+v", cc)
	}
	if cc.ReviewStatus != "pending" {
		t.Fatalf("new count must be pending, got %s", cc.ReviewStatus)
	}
	// Cashier may not review.
	if _, err := s.ReviewCashCount(ctx, u, cc.ID, true, ""); err == nil {
		t.Fatal("expected cashier to be denied reviewing a count")
	}
	// Manager/Admin approves.
	rev, err := s.ReviewCashCount(ctx, admin, cc.ID, true, "مطابق")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if rev.ReviewStatus != "approved" || rev.ReviewedBy != "admin1" {
		t.Fatalf("review wrong: %+v", rev)
	}
	// No double review.
	if _, err := s.ReviewCashCount(ctx, admin, cc.ID, true, "مرة أخرى"); err == nil {
		t.Fatal("expected double review to be rejected")
	}
	if _, err := s.ReviewCashCount(ctx, admin, cc.ID, false, ""); err == nil {
		t.Fatal("expected second review (reject) to be rejected too")
	}
}

func TestCashCountCrossUserDenialText(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "cash1", "cashier")
	other := b2TestUser(t, s, "cash2", "cashier")
	mgr := b2TestUser(t, s, "mgr1", "manager")
	id, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// FIX-18 R18-C1: cross-user count denial carries the uniform
	// "غير مسموح — …" prefix so the handler maps it to 403.
	_, err = s.RecordCashCount(ctx, other, id, money.Cents(10000), "", "")
	if err == nil || !strings.HasPrefix(err.Error(), "غير مسموح — ") {
		t.Fatalf("cross-user count: want the uniform denial prefix, got %v", err)
	}
	// Owner still counts; manager (view-all holder) may count too.
	if _, err := s.RecordCashCount(ctx, u, id, money.Cents(10000), "", ""); err != nil {
		t.Fatalf("own count: %v", err)
	}
	if _, err := s.RecordCashCount(ctx, mgr, id, money.Cents(10000), "", ""); err != nil {
		t.Fatalf("manager count on another shift: %v", err)
	}
}

func TestCashCountBalancedCannotBeRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "cash1", "cashier")
	admin := b2TestUser(t, s, "admin1", "admin")
	id, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	cc, err := s.RecordCashCount(ctx, u, id, money.Cents(10000), "", "")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if cc.Difference != 0 || cc.DifferenceStatus != "متوازن" {
		t.Fatalf("balanced count wrong: %+v", cc)
	}
	// A balanced count may NOT be rejected.
	if _, err := s.ReviewCashCount(ctx, admin, cc.ID, false, "رفض بلا سبب"); err == nil {
		t.Fatal("expected rejecting a balanced count to be refused")
	}
	// But it may be approved.
	if _, err := s.ReviewCashCount(ctx, admin, cc.ID, true, "متوازن"); err != nil {
		t.Fatalf("approving a balanced count must be allowed: %v", err)
	}
}

func TestCashCountClosedShiftRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "cash1", "cashier")
	id, err := s.OpenShift(ctx, u, money.Cents(1000), false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := s.CloseShift(ctx, u, 0); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := s.RecordCashCount(ctx, u, id, money.Cents(1000), "", ""); err == nil {
		t.Fatal("expected counting a closed shift's drawer to be rejected")
	}
}

// --- FIX-1 regression tests ---

func TestFix1_CashBoxShiftOwnership(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := b2TestUser(t, s, "cashA", "cashier")
	b := b2TestUser(t, s, "cashB", "cashier")
	admin := b2TestUser(t, s, "admin1", "admin")
	shiftA, err := s.OpenShift(ctx, a, money.Cents(10000), false)
	if err != nil {
		t.Fatal(err)
	}
	// Another cashier may not move cash on A's shift (M4).
	if _, err := s.CashBoxIn(ctx, b, shiftA, money.Cents(100), "اختبار"); err == nil {
		t.Fatal("cashier B must not move cash on cashier A's shift (M4)")
	}
	// A nonexistent shift is refused.
	if _, err := s.CashBoxIn(ctx, a, 999999, money.Cents(100), "اختبار"); err == nil {
		t.Fatal("nonexistent shift must be refused (M4)")
	}
	// Admin may move on another user's open shift.
	if _, err := s.CashBoxIn(ctx, admin, shiftA, money.Cents(100), "إيداع إداري"); err != nil {
		t.Fatalf("admin move on another open shift: %v", err)
	}
	// A closed shift is refused.
	if _, err := s.CloseShift(ctx, a, shiftA); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CashBoxIn(ctx, a, shiftA, money.Cents(100), "اختبار"); err == nil {
		t.Fatal("closed shift must be refused (M4)")
	}
}

// --- PEER APPROVAL (jony 2026-11-25): cashier reviews ANOTHER cashier's
// count, never their own. ---

// b2Count opens a shift for u and records one count on it.
func b2Count(t *testing.T, s *Store, u *auth.User, opening, counted money.Cents) (*CashCount, int64) {
	t.Helper()
	ctx := context.Background()
	shiftID, err := s.OpenShift(ctx, u, opening, false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	cc, err := s.RecordCashCount(ctx, u, shiftID, counted, "", "")
	if err != nil {
		t.Fatalf("record count: %v", err)
	}
	return cc, shiftID
}

// b2ReviewAuditCount counts review-decision audit rows (approve + reject).
func b2ReviewAuditCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM AuditLog
		WHERE ActionType IN ('اعتماد فرق عدّ النقدية','رفض فرق عدّ النقدية')`).Scan(&n); err != nil {
		t.Fatalf("audit count: %v", err)
	}
	return n
}

// b2LastAuditNotes returns the Notes of the newest review-decision audit row.
func b2LastAuditNotes(t *testing.T, s *Store) string {
	t.Helper()
	var notes string
	if err := s.DB.QueryRow(`SELECT Notes FROM AuditLog
		WHERE ActionType IN ('اعتماد فرق عدّ النقدية','رفض فرق عدّ النقدية')
		ORDER BY LogID DESC LIMIT 1`).Scan(&notes); err != nil {
		t.Fatalf("audit notes: %v", err)
	}
	return notes
}

// b2SetReviewedAt back-dates a reviewed count (the service stamps Now(),
// so fixed past dates can only be set this way).
func b2SetReviewedAt(t *testing.T, s *Store, countID int64, reviewedAt string) {
	t.Helper()
	if _, err := s.DB.Exec(`UPDATE CashCounts SET ReviewedAt=? WHERE CountID=?`, reviewedAt, countID); err != nil {
		t.Fatalf("set ReviewedAt: %v", err)
	}
}

func TestPeerCashierApprovesAnotherCashiersCount(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := b2TestUser(t, s, "cashA", "cashier")
	b := b2TestUser(t, s, "cashB", "cashier")
	cc, _ := b2Count(t, s, a, money.Cents(10000), money.Cents(9000))
	before := b2ReviewAuditCount(t, s)
	rev, err := s.ReviewCashCount(ctx, b, cc.ID, true, "مطابق")
	if err != nil {
		t.Fatalf("peer approve: %v", err)
	}
	if rev.ReviewStatus != "approved" || rev.ReviewedBy != "cashB" {
		t.Fatalf("review wrong: %+v", rev)
	}
	if b2ReviewAuditCount(t, s) != before+1 {
		t.Fatal("expected exactly one review audit row")
	}
	notes := b2LastAuditNotes(t, s)
	for _, want := range []string{"cashA", "100.00 ج.م", "90.00 ج.م", "معتمد"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("audit detail %q missing %q", notes, want)
		}
	}
}

func TestPeerCashierRejectsAnotherCashiersCount(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := b2TestUser(t, s, "cashA", "cashier")
	b := b2TestUser(t, s, "cashB", "cashier")
	cc, _ := b2Count(t, s, a, money.Cents(10000), money.Cents(9500))
	rev, err := s.ReviewCashCount(ctx, b, cc.ID, false, "فرق غير مبرر")
	if err != nil {
		t.Fatalf("peer reject: %v", err)
	}
	if rev.ReviewStatus != "rejected" {
		t.Fatalf("review wrong: %+v", rev)
	}
	notes := b2LastAuditNotes(t, s)
	for _, want := range []string{"cashA", "مرفوض", "فرق غير مبرر"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("audit detail %q missing %q", notes, want)
		}
	}
}

func TestPeerSelfApproveRefusedZeroWrites(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := b2TestUser(t, s, "cashA", "cashier")
	cc, _ := b2Count(t, s, a, money.Cents(10000), money.Cents(9000))
	before := b2ReviewAuditCount(t, s)
	_, err := s.ReviewCashCount(ctx, a, cc.ID, true, "")
	if err == nil || !strings.Contains(err.Error(), "عدّك الخاص") {
		t.Fatalf("expected self-review refusal, got %v", err)
	}
	// Still pending, untouched, zero new audit rows.
	var status, reviewedBy, reviewedAt string
	var n int
	_ = s.DB.QueryRow(`SELECT ReviewStatus, COALESCE(ReviewedBy,''), COALESCE(ReviewedAt,'')
		FROM CashCounts WHERE CountID=?`, cc.ID).Scan(&status, &reviewedBy, &reviewedAt)
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM CashCounts WHERE CountID=?`, cc.ID).Scan(&n)
	if status != "pending" || reviewedBy != "" || reviewedAt != "" || n != 1 {
		t.Fatalf("self-review attempt wrote to the count: status=%q by=%q at=%q", status, reviewedBy, reviewedAt)
	}
	if b2ReviewAuditCount(t, s) != before {
		t.Fatal("self-review attempt wrote audit rows")
	}
}

func TestPeerSelfRejectRefusedZeroWrites(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := b2TestUser(t, s, "cashA", "cashier")
	cc, _ := b2Count(t, s, a, money.Cents(10000), money.Cents(9000))
	before := b2ReviewAuditCount(t, s)
	_, err := s.ReviewCashCount(ctx, a, cc.ID, false, "أرفض نفسي")
	if err == nil || !strings.Contains(err.Error(), "عدّك الخاص") {
		t.Fatalf("expected self-reject refusal, got %v", err)
	}
	var status string
	_ = s.DB.QueryRow(`SELECT ReviewStatus FROM CashCounts WHERE CountID=?`, cc.ID).Scan(&status)
	if status != "pending" {
		t.Fatalf("self-reject changed status to %q", status)
	}
	if b2ReviewAuditCount(t, s) != before {
		t.Fatal("self-reject attempt wrote audit rows")
	}
}

func TestSelfBanMatchesByStoredIDWhenUserRowMissing(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := b2TestUser(t, s, "cashA", "cashier")
	cc, _ := b2Count(t, s, a, money.Cents(10000), money.Cents(9000))
	// FIX-3 L8b: the counting user no longer exists in Users, but the
	// count carries the stored CountedByUserID, so the owner is still
	// recognised — no bare username comparison is involved.
	if _, err := s.DB.Exec(`DELETE FROM Users WHERE UserName=?`, "cashA"); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	_, err := s.ReviewCashCount(ctx, a, cc.ID, true, "")
	if err == nil || !strings.Contains(err.Error(), "عدّك الخاص") {
		t.Fatalf("expected stored-id self-review refusal, got %v", err)
	}
}

// C-3 (jony's ruling 2026-11-25, reversing FIX-3 M1 for Manager/Admin
// only): a manager MAY approve their own count. The approval succeeds,
// is audited as a normal approval (one approve audit row, zero
// self-review-attempt rows), and appears in the approvals log with
// owner == approver. A cashier self-approving is still refused (see
// TestPeerSelfApproveRefusedZeroWrites /
// TestCashierSelfReviewStillRefusedAndAudited).
func TestManagerSelfReviewAllowed(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mgr := b2TestUser(t, s, "mgr1", "manager")
	cc, _ := b2Count(t, s, mgr, money.Cents(10000), money.Cents(9000))
	before := b2ReviewAuditCount(t, s)
	rev, err := s.ReviewCashCount(ctx, mgr, cc.ID, true, "اعتماد ذاتي")
	if err != nil {
		t.Fatalf("manager self-approve must succeed (C-3), got %v", err)
	}
	if rev.ReviewStatus != "approved" || rev.ReviewedBy != "mgr1" {
		t.Fatalf("review wrong: %+v", rev)
	}
	if b2ReviewAuditCount(t, s) != before+1 {
		t.Fatal("manager self-approval must write exactly one approve audit row")
	}
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM AuditLog WHERE ActionType='محاولة اعتماد عدّ خاص'`).Scan(&n)
	if n != 0 {
		t.Fatalf("manager self-approval must write no self-review-attempt row, got %d", n)
	}
	// It appears in the approvals log like any approval (day filter).
	rows, err := s.ListCashcountReviews(ctx, mgr, "", "", "", "")
	if err != nil {
		t.Fatalf("approvals log: %v", err)
	}
	if len(rows) != 1 || rows[0].CountID != cc.ID || rows[0].Owner != "mgr1" || rows[0].Approver != "mgr1" || rows[0].Decision != "approved" {
		t.Fatalf("self-approval must appear in the approvals log, got %+v", rows)
	}
	// No double review even for a self-approval.
	if _, err := s.ReviewCashCount(ctx, mgr, cc.ID, true, "مرة أخرى"); err == nil {
		t.Fatal("expected double review to be rejected even for self-approval")
	}
}

func TestAdminSelfReviewAllowed(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b2TestUser(t, s, "admin1", "admin")
	cc, _ := b2Count(t, s, admin, money.Cents(10000), money.Cents(9000))
	before := b2ReviewAuditCount(t, s)
	rev, err := s.ReviewCashCount(ctx, admin, cc.ID, true, "")
	if err != nil {
		t.Fatalf("admin self-approve must succeed (C-3), got %v", err)
	}
	if rev.ReviewStatus != "approved" || rev.ReviewedBy != "admin1" {
		t.Fatalf("review wrong: %+v", rev)
	}
	if b2ReviewAuditCount(t, s) != before+1 {
		t.Fatal("admin self-approval must write exactly one approve audit row")
	}
}

func TestManagerSelfRejectAllowed(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mgr := b2TestUser(t, s, "mgr1", "manager")
	cc, _ := b2Count(t, s, mgr, money.Cents(10000), money.Cents(9000))
	rev, err := s.ReviewCashCount(ctx, mgr, cc.ID, false, "فرق غير مبرر")
	if err != nil {
		t.Fatalf("manager self-reject must succeed (C-3), got %v", err)
	}
	if rev.ReviewStatus != "rejected" || rev.ReviewedBy != "mgr1" {
		t.Fatalf("review wrong: %+v", rev)
	}
}

// The balanced-count reject ban still applies to a Manager/Admin
// self-review: only the self-review ban was relaxed, not the other
// review rules.
func TestManagerSelfRejectBalancedStillRefused(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mgr := b2TestUser(t, s, "mgr1", "manager")
	cc, _ := b2Count(t, s, mgr, money.Cents(10000), money.Cents(10000))
	if _, err := s.ReviewCashCount(ctx, mgr, cc.ID, false, "رفض بلا سبب"); err == nil ||
		!strings.Contains(err.Error(), "لا يمكن رفض عدّ متوازن") {
		t.Fatalf("manager self-reject of a balanced count must hit the balanced ban, got %v", err)
	}
	var status string
	_ = s.DB.QueryRow(`SELECT ReviewStatus FROM CashCounts WHERE CountID=?`, cc.ID).Scan(&status)
	if status != "pending" {
		t.Fatalf("refused review changed status to %q", status)
	}
}

func TestCashierSelfReviewStillRefusedAndAudited(t *testing.T) {
	// C-3 leaves the cashier ban untouched: approve AND reject of
	// one's own count are refused with the same message, the count
	// stays pending, no approve/reject audit row is written, and
	// each attempt writes one "محاولة اعتماد عدّ خاص" audit row (L8a).
	s := testStore(t)
	ctx := context.Background()
	a := b2TestUser(t, s, "cashA", "cashier")
	cc, _ := b2Count(t, s, a, money.Cents(10000), money.Cents(9000))
	before := b2ReviewAuditCount(t, s)
	_, err := s.ReviewCashCount(ctx, a, cc.ID, true, "")
	if err == nil || !strings.Contains(err.Error(), "عدّك الخاص") {
		t.Fatalf("cashier self-approve must still be refused, got %v", err)
	}
	_, err = s.ReviewCashCount(ctx, a, cc.ID, false, "")
	if err == nil || !strings.Contains(err.Error(), "عدّك الخاص") {
		t.Fatalf("cashier self-reject must still be refused, got %v", err)
	}
	var status string
	_ = s.DB.QueryRow(`SELECT ReviewStatus FROM CashCounts WHERE CountID=?`, cc.ID).Scan(&status)
	if status != "pending" {
		t.Fatalf("cashier self-review changed status to %q", status)
	}
	if b2ReviewAuditCount(t, s) != before {
		t.Fatal("cashier self-review wrote approve/reject audit rows")
	}
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM AuditLog WHERE ActionType='محاولة اعتماد عدّ خاص'`).Scan(&n)
	if n != 2 {
		t.Fatalf("expected 2 self-review-attempt audit rows (approve + reject), got %d", n)
	}
}

func TestSupervisorStillCannotReview(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := b2TestUser(t, s, "cashA", "cashier")
	sup := b2TestUser(t, s, "sup1", "supervisor")
	cc, _ := b2Count(t, s, a, money.Cents(10000), money.Cents(9000))
	if _, err := s.ReviewCashCount(ctx, sup, cc.ID, true, ""); err == nil {
		t.Fatal("supervisor must still be denied reviewing a count")
	}
}

func TestListPendingCashCounts(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := b2TestUser(t, s, "cashA", "cashier")
	b := b2TestUser(t, s, "cashB", "cashier")
	sup := b2TestUser(t, s, "sup1", "supervisor")
	admin := b2TestUser(t, s, "admin1", "admin")
	cc1, _ := b2Count(t, s, a, money.Cents(10000), money.Cents(9000))
	cc2, _ := b2Count(t, s, b, money.Cents(5000), money.Cents(5000))
	// Review cc2 so only cc1 stays pending.
	if _, err := s.ReviewCashCount(ctx, admin, cc2.ID, true, ""); err != nil {
		t.Fatalf("review cc2: %v", err)
	}
	pending, err := s.ListPendingCashCounts(ctx, b)
	if err != nil {
		t.Fatalf("peer pending list: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != cc1.ID {
		t.Fatalf("expected only cc1 pending, got %+v", pending)
	}
	if _, err := s.ListPendingCashCounts(ctx, sup); err == nil {
		t.Fatal("supervisor must be denied the pending list")
	}
}

// --- ListCashcountReviews ---

func TestListCashcountReviewsDayFilter(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := b2TestUser(t, s, "cashA", "cashier")
	admin := b2TestUser(t, s, "admin1", "admin")
	cc1, shift1 := b2Count(t, s, a, money.Cents(10000), money.Cents(9000))
	cc2, _ := b2Count(t, s, a, money.Cents(5000), money.Cents(5000))
	// cc1 reviewed 2026-11-20, cc2 reviewed 2026-11-21; a third count
	// stays pending and must never appear.
	cc3, _ := b2Count(t, s, a, money.Cents(1000), money.Cents(1000))
	_ = cc3
	if _, err := s.ReviewCashCount(ctx, admin, cc1.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewCashCount(ctx, admin, cc2.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	b2SetReviewedAt(t, s, cc1.ID, "2026-11-20 10:00:00")
	b2SetReviewedAt(t, s, cc2.ID, "2026-11-21 10:00:00")
	rows, err := s.ListCashcountReviews(ctx, admin, "2026-11-20", "", "", "")
	if err != nil {
		t.Fatalf("day filter: %v", err)
	}
	if len(rows) != 1 || rows[0].CountID != cc1.ID {
		t.Fatalf("day filter must return only cc1, got %+v", rows)
	}
	r := rows[0]
	if r.Owner != "cashA" || r.Approver != "admin1" || r.ShiftID != shift1 ||
		r.Expected != 10000 || r.Counted != 9000 || r.Difference != -1000 || r.Decision != "approved" {
		t.Fatalf("review row keys/values wrong: %+v", r)
	}
	if r.ReviewedAt != "2026-11-20 10:00:00" {
		t.Fatalf("reviewed_at wrong: %q", r.ReviewedAt)
	}
}

func TestListCashcountReviewsMonthFilter(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := b2TestUser(t, s, "cashA", "cashier")
	admin := b2TestUser(t, s, "admin1", "admin")
	ccOct, _ := b2Count(t, s, a, money.Cents(10000), money.Cents(9000))
	ccNov1, _ := b2Count(t, s, a, money.Cents(5000), money.Cents(5000))
	ccNov2, _ := b2Count(t, s, a, money.Cents(3000), money.Cents(3000))
	ccDec, _ := b2Count(t, s, a, money.Cents(2000), money.Cents(2000))
	for _, tc := range []struct {
		id   int64
		when string
	}{{ccOct.ID, "2026-10-31 23:59:59"}, {ccNov1.ID, "2026-11-01 00:00:01"},
		{ccNov2.ID, "2026-11-30 23:59:59"}, {ccDec.ID, "2026-12-01 00:00:01"}} {
		if _, err := s.ReviewCashCount(ctx, admin, tc.id, true, ""); err != nil {
			t.Fatal(err)
		}
		b2SetReviewedAt(t, s, tc.id, tc.when)
	}
	rows, err := s.ListCashcountReviews(ctx, admin, "", "2026-11", "", "")
	if err != nil {
		t.Fatalf("month filter: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("month filter must return the 2 November reviews, got %d", len(rows))
	}
	// Newest first.
	if rows[0].CountID != ccNov2.ID || rows[1].CountID != ccNov1.ID {
		t.Fatalf("month order wrong: %+v", rows)
	}
}

func TestListCashcountReviewsCustomPeriodInclusive(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := b2TestUser(t, s, "cashA", "cashier")
	admin := b2TestUser(t, s, "admin1", "admin")
	cc1, _ := b2Count(t, s, a, money.Cents(10000), money.Cents(9000))
	cc2, _ := b2Count(t, s, a, money.Cents(5000), money.Cents(5000))
	cc3, _ := b2Count(t, s, a, money.Cents(3000), money.Cents(3000))
	for _, tc := range []struct {
		id   int64
		when string
	}{{cc1.ID, "2026-11-20 09:00:00"}, {cc2.ID, "2026-11-21 09:00:00"}, {cc3.ID, "2026-11-22 09:00:00"}} {
		if _, err := s.ReviewCashCount(ctx, admin, tc.id, true, ""); err != nil {
			t.Fatal(err)
		}
		b2SetReviewedAt(t, s, tc.id, tc.when)
	}
	rows, err := s.ListCashcountReviews(ctx, admin, "", "", "2026-11-20", "2026-11-21")
	if err != nil {
		t.Fatalf("period: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("inclusive period must return both ends, got %d", len(rows))
	}
	// Single-day period works.
	rows, err = s.ListCashcountReviews(ctx, admin, "", "", "2026-11-22", "2026-11-22")
	if err != nil {
		t.Fatalf("single-day period: %v", err)
	}
	if len(rows) != 1 || rows[0].CountID != cc3.ID {
		t.Fatalf("single-day period must return cc3, got %+v", rows)
	}
}

func TestListCashcountReviewsDefaultDayIsToday(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := b2TestUser(t, s, "cashA", "cashier")
	admin := b2TestUser(t, s, "admin1", "admin")
	ccToday, _ := b2Count(t, s, a, money.Cents(10000), money.Cents(9000))
	ccOld, _ := b2Count(t, s, a, money.Cents(5000), money.Cents(5000))
	if _, err := s.ReviewCashCount(ctx, admin, ccToday.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewCashCount(ctx, admin, ccOld.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	b2SetReviewedAt(t, s, ccOld.ID, "2026-01-05 09:00:00")
	rows, err := s.ListCashcountReviews(ctx, admin, "", "", "", "")
	if err != nil {
		t.Fatalf("default: %v", err)
	}
	if len(rows) != 1 || rows[0].CountID != ccToday.ID {
		t.Fatalf("default must return only today's review, got %+v", rows)
	}
}

func TestListCashcountReviewsRejectedIncluded(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := b2TestUser(t, s, "cashA", "cashier")
	admin := b2TestUser(t, s, "admin1", "admin")
	cc, _ := b2Count(t, s, a, money.Cents(10000), money.Cents(9000))
	if _, err := s.ReviewCashCount(ctx, admin, cc.ID, false, "فرق"); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListCashcountReviews(ctx, admin, "", "", "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].Decision != "rejected" {
		t.Fatalf("rejected decision must appear, got %+v", rows)
	}
}

func TestListCashcountReviewsDeniedRoles(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	cash := b2TestUser(t, s, "cashA", "cashier")
	sup := b2TestUser(t, s, "sup1", "supervisor")
	if _, err := s.ListCashcountReviews(ctx, cash, "", "", "", ""); err == nil {
		t.Fatal("cashier must be denied the approvals log")
	}
	if _, err := s.ListCashcountReviews(ctx, sup, "", "", "", ""); err == nil {
		t.Fatal("supervisor must be denied the approvals log")
	}
	if _, err := s.ListCashcountReviews(ctx, nil, "", "", "", ""); err == nil {
		t.Fatal("nil user must be denied the approvals log")
	}
}

func TestListCashcountReviewsBadInput(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b2TestUser(t, s, "admin1", "admin")
	badDays := []string{"20-11-2026", "2026-13-01", "2026/11/20", "2026-02-30"}
	for _, d := range badDays {
		if _, err := s.ListCashcountReviews(ctx, admin, d, "", "", ""); err == nil {
			t.Fatalf("bad day %q must be refused", d)
		}
	}
	for _, m := range []string{"2026-13", "Nov 2026", "2026-11-20", "2026-1"} {
		if _, err := s.ListCashcountReviews(ctx, admin, "", m, "", ""); err == nil {
			t.Fatalf("bad month %q must be refused", m)
		}
	}
	// Reversed range: the exact message.
	_, err := s.ListCashcountReviews(ctx, admin, "", "", "2026-11-21", "2026-11-20")
	if err == nil || err.Error() != "تاريخ البداية يجب أن يكون قبل تاريخ النهاية أو مساويًا له" {
		t.Fatalf("reversed range must raise the exact message, got %v", err)
	}
	// Two modes at once.
	if _, err := s.ListCashcountReviews(ctx, admin, "2026-11-20", "2026-11", "", ""); err == nil {
		t.Fatal("day+month must be refused")
	}
	if _, err := s.ListCashcountReviews(ctx, admin, "", "2026-11", "2026-11-01", "2026-11-02"); err == nil {
		t.Fatal("month+period must be refused")
	}
	// Lone bound.
	if _, err := s.ListCashcountReviews(ctx, admin, "", "", "2026-11-01", ""); err == nil {
		t.Fatal("lone date_from must be refused")
	}
	if _, err := s.ListCashcountReviews(ctx, admin, "", "", "", "2026-11-02"); err == nil {
		t.Fatal("lone date_to must be refused")
	}
	// Bad format inside a period.
	if _, err := s.ListCashcountReviews(ctx, admin, "", "", "2026-11-01", "bad"); err == nil {
		t.Fatal("bad date_to must be refused")
	}
}
