package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/money"
)

// --- FIX-23 R23-D-F1: manual CashBox 'in' has no magnitude cap;
// CashIn wraps and close persists a wrapped negative as متوازن ---
//
// cashBoxMoveTx used to refuse only amount <= 0 and an empty reason,
// so a manager could store any positive int64 'in' amount. Two 2^62
// 'in' rows wrapped sum.CashIn to MinInt64 in shiftSummaryTx (plain
// += at the old :837, upstream of the checked ExpectedBalance chain,
// which cannot detect a wrap that already happened), and close then
// persisted the wrapped negative as the shift's official totals with
// difference 0 متوازن — the exact falsified-record outcome the
// R22-E-F2 opening cap eliminated for the opening.
//
// The fix has two halves, and this file proves both:
// (a) manual 'in' is capped at money.MaxOpeningCents (100,000,000
// EGP, the same bound as the opening) in cashBoxMoveTx — the single
// funnel for both entry points (HTML form POST /cashbox/in and API
// POST /api/cashbox/in both end in CashBoxIn). Automatic 'in' rows
// (sales, collections, courier) are direct INSERTs elsewhere and
// never pass through here, so no legitimate flow is affected.
// (b) the CashBox-loop terms in shiftSummaryTx (CashIn,
// CourierCollection, CashOut, VendorCashPaid, CashReturns,
// RetainedCanceledCash, plus the per-invoice refund/leg totals that
// feed them) accumulate with money.AddChecked/SubChecked, so a
// legacy-poisoned shift (absurd rows stored before the cap, or by
// direct DB write) refuses with money.ErrOverflow instead of
// returning wrapped numbers — and close refuses with it too.
//
// The cap literal is written out here on purpose (like the fix-22
// E2 file) so this file also compiles against the pre-fix tree for
// the mutation check; the production constant is
// money.MaxOpeningCents and must equal it.

// fix23CCap is the R23-D-F1 manual-'in' cap in cents (100,000,000 EGP).
const fix23CCap = money.Cents(10000000000)

// fix23CHalf is 2^62: two rows of this size wrap an int64 sum to MinInt64.
const fix23CHalf = money.Cents(4611686018427387904)

// fix23CShift opens a normal 100000-cent shift for a manager and
// returns the store, user and shift id.
func fix23CShift(t *testing.T) (*Store, *auth.User, int64) {
	t.Helper()
	s := testStore(t)
	u := b2TestUser(t, s, "mgr1", "manager")
	id, err := s.OpenShift(context.Background(), u, money.Cents(100000), false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s, u, id
}

// fix23CPoison inserts one legacy CashBox row directly (bypassing the
// cap, as pre-fix code or a direct DB write would have stored it).
func fix23CPoison(t *testing.T, s *Store, u *auth.User, shiftID int64, mtype string, amt money.Cents, autoKind string, reason string) {
	t.Helper()
	if _, err := s.DB.Exec(`INSERT INTO CashBox(UserID,UserName,ShiftID,MovementType,Amount,Reason,MovementDate,AutoKind)
		VALUES(?,?,?,?,?,?,?,?)`,
		u.ID, u.UserName, shiftID, mtype, int64(amt), reason, Now(), autoKind); err != nil {
		t.Fatalf("insert poison row %s %d: %v", mtype, int64(amt), err)
	}
}

func TestFix23C_CashBoxInAbsurdRefused(t *testing.T) {
	s, u, id := fix23CShift(t)
	ctx := context.Background()
	for _, amt := range []money.Cents{fix23CHalf, money.Cents(9223372036854775807)} {
		if _, err := s.CashBoxIn(ctx, u, id, amt, "إيداع ضخم"); err == nil {
			t.Fatalf("expected CashBoxIn %d to be refused", int64(amt))
		} else if !errors.Is(err, money.ErrOverflow) {
			t.Fatalf("CashBoxIn %d err = %v, want money.ErrOverflow", int64(amt), err)
		}
	}
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM CashBox WHERE ShiftID=? AND MovementType='in'`, id).Scan(&n)
	if n != 0 {
		t.Fatalf("expected 0 'in' rows after refused deposits, got %d", n)
	}
}

func TestFix23C_CashBoxInJustAboveCapRefused(t *testing.T) {
	s, u, id := fix23CShift(t)
	ctx := context.Background()
	if _, err := s.CashBoxIn(ctx, u, id, fix23CCap+1, "إيداع فوق الحد"); err == nil {
		t.Fatal("expected CashBoxIn cap+1 to be refused")
	} else if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("cap+1 err = %v, want money.ErrOverflow", err)
	}
}

func TestFix23C_CashBoxInAtCapAccepted(t *testing.T) {
	s, u, id := fix23CShift(t)
	ctx := context.Background()
	if _, err := s.CashBoxIn(ctx, u, id, fix23CCap, "إيداع عند الحد"); err != nil {
		t.Fatalf("CashBoxIn exactly at cap refused: %v", err)
	}
	sum, err := s.ShiftSummary(ctx, id)
	if err != nil {
		t.Fatalf("summary after at-cap deposit: %v", err)
	}
	if sum.CashIn != fix23CCap || sum.ExpectedBalance != 100000+fix23CCap {
		t.Fatalf("summary wrong after at-cap deposit: CashIn=%d Expected=%d", sum.CashIn, sum.ExpectedBalance)
	}
}

func TestFix23C_CashBoxInNormalStillWorks(t *testing.T) {
	s, u, id := fix23CShift(t)
	ctx := context.Background()
	if _, err := s.CashBoxIn(ctx, u, id, money.Cents(2000), "إيداع"); err != nil {
		t.Fatalf("normal CashBoxIn refused: %v", err)
	}
	if _, err := s.CashBoxIn(ctx, u, id, money.Cents(3000000000), "إيداع كبير مشروع"); err != nil {
		t.Fatalf("large-but-below-cap CashBoxIn refused: %v", err)
	}
	sum, err := s.ShiftSummary(ctx, id)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.CashIn != 3000002000 || sum.ExpectedBalance != 100000+3000002000 {
		t.Fatalf("summary wrong: CashIn=%d Expected=%d", sum.CashIn, sum.ExpectedBalance)
	}
}

func TestFix23C_CashBoxOutUnaffectedByCap(t *testing.T) {
	s, u, id := fix23CShift(t)
	ctx := context.Background()
	// A normal withdrawal still works (drawer-gated, not capped).
	if _, err := s.CashBoxOut(ctx, u, id, money.Cents(50000), "سحب"); err != nil {
		t.Fatalf("normal CashBoxOut refused: %v", err)
	}
	// Withdrawing more than the drawer holds is still refused by the
	// drawer gate (negative forbidden), not by the 'in' cap.
	if _, err := s.CashBoxOut(ctx, u, id, money.Cents(10000000000), "سحب ضخم"); err == nil {
		t.Fatal("expected over-drawer withdrawal to be refused")
	}
	sum, err := s.ShiftSummary(ctx, id)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.CashOut != 50000 || sum.ExpectedBalance != 50000 {
		t.Fatalf("summary wrong: CashOut=%d Expected=%d", sum.CashOut, sum.ExpectedBalance)
	}
}

// TestFix23C_LegacyWrappedCashInSummaryRefuses is the review's live
// repro at unit level: two pre-cap 2^62 'in' rows (stored by direct
// DB write, as the pre-fix code stored them) must make the summary
// refuse with money.ErrOverflow — never return CashIn MinInt64 — and
// close must refuse too, leaving the shift open with nothing
// persisted as متوازن.
func TestFix23C_LegacyWrappedCashInSummaryRefuses(t *testing.T) {
	s, u, id := fix23CShift(t)
	ctx := context.Background()
	fix23CPoison(t, s, u, id, "in", fix23CHalf, "", "إيداع اختبار 1")
	fix23CPoison(t, s, u, id, "in", fix23CHalf, "", "إيداع اختبار 2")
	sum, err := s.ShiftSummary(ctx, id)
	if err == nil {
		t.Fatalf("expected summary to refuse on wrapped CashIn, got %+v", sum)
	}
	if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("summary err = %v, want money.ErrOverflow", err)
	}
	if strings.Contains(err.Error(), "SQL logic error") {
		t.Fatalf("summary err leaks raw SQLite text: %v", err)
	}
	if _, err := s.CloseShift(ctx, u, id); err == nil {
		t.Fatal("expected close to refuse on wrapped CashIn")
	} else if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("close err = %v, want money.ErrOverflow", err)
	}
	var isOpen int
	var expected sql.NullInt64
	_ = s.DB.QueryRow(`SELECT IsOpen, ExpectedBalance FROM Shifts WHERE ShiftID=?`, id).Scan(&isOpen, &expected)
	if isOpen != 1 {
		t.Fatalf("poisoned shift must stay open after refused close, IsOpen=%d", isOpen)
	}
	if expected.Valid {
		t.Fatalf("poisoned shift must not persist an ExpectedBalance, got %d", expected.Int64)
	}
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM CashBox WHERE ShiftID=? AND MovementType='close'`, id).Scan(&n)
	if n != 0 {
		t.Fatalf("expected no 'close' CashBox row after refused close, got %d", n)
	}
}

func TestFix23C_LegacyWrappedCourierCollectionRefuses(t *testing.T) {
	s, u, id := fix23CShift(t)
	ctx := context.Background()
	fix23CPoison(t, s, u, id, "in", fix23CHalf, "courier_collection", "تحصيل مندوب 1")
	fix23CPoison(t, s, u, id, "in", fix23CHalf, "courier_collection", "تحصيل مندوب 2")
	if _, err := s.ShiftSummary(ctx, id); err == nil {
		t.Fatal("expected summary to refuse on wrapped CourierCollection")
	} else if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("summary err = %v, want money.ErrOverflow", err)
	}
}

func TestFix23C_LegacyWrappedCashOutRefuses(t *testing.T) {
	// NOTE: the poison rows are 5e18, not 2^62. Two 2^62 rows sum to
	// exactly MinInt64, and the downstream SubChecked(expected,
	// MinInt64) happens to refuse that value even pre-fix — so a
	// 2^62 variant would PASS on the pre-fix tree and prove nothing
	// about this half. Two 5e18 rows wrap to -8446744073709551616,
	// and subtracting that (a negative) yields a wrong-but-fitting
	// ExpectedBalance pre-fix, so only the checked accumulation
	// in the CashBox loop refuses it.
	s, u, id := fix23CShift(t)
	ctx := context.Background()
	fix23CPoison(t, s, u, id, "out", money.Cents(5000000000000000000), "", "سحب قديم 1")
	fix23CPoison(t, s, u, id, "out", money.Cents(5000000000000000000), "", "سحب قديم 2")
	if sum, err := s.ShiftSummary(ctx, id); err == nil {
		t.Fatalf("expected summary to refuse on wrapped CashOut, got %+v", sum)
	} else if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("summary err = %v, want money.ErrOverflow", err)
	}
}

func TestFix23C_LegacyWrappedCashReturnsRefuses(t *testing.T) {
	// Same 5e18 note as CashOut above: two 2^62 return legs sum to
	// exactly MinInt64, which the downstream SubChecked happens to
	// refuse pre-fix; two 5e18 legs wrap to a negative the chain
	// subtracts into a wrong-but-fitting balance.
	s, u, id := fix23CShift(t)
	ctx := context.Background()
	fix23CPoison(t, s, u, id, "out", money.Cents(5000000000000000000), "return", "مرتجع قديم 1")
	fix23CPoison(t, s, u, id, "out", money.Cents(5000000000000000000), "return", "مرتجع قديم 2")
	if sum, err := s.ShiftSummary(ctx, id); err == nil {
		t.Fatalf("expected summary to refuse on wrapped CashReturns, got %+v", sum)
	} else if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("summary err = %v, want money.ErrOverflow", err)
	}
}
