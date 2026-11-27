package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"cashierpos-go/internal/money"
)

// --- FIX-22 R22-E-F2: shift opening has no magnitude cap ---
//
// OpenShift used to refuse only opening < 0, so an opening of
// 9223372036854774784 cents (~92 quadrillion EGP, typed by any manager
// in the app, no DB access needed) was accepted and stored verbatim.
// The first cash sale then wrapped ExpectedBalance to
// -9223372036854774832, every cash drawer-gated operation failed with
// a raw SQLite overflow error, and close persisted the wrapped
// negative as the shift's official totals with difference 0 متوازن.
//
// The fix caps the opening at 100,000,000 EGP (10,000,000,000 cents —
// the bound is written as a literal here on purpose so this file also
// compiles against the pre-fix tree for the mutation check; the
// production constant is money.MaxOpeningCents and must equal it),
// computes ExpectedBalance with checked addition, refuses a resume of
// a stored absurd opening, and makes DrawerExpectedBalance sum 'in'
// rows only (the 'open' row duplicates the opening and used to be
// added to the day's cash inside SQLite's int64 SUM).

// fix22E2Cap is the R22-E-F2 opening cap in cents (100,000,000 EGP).
const fix22E2Cap = money.Cents(10000000000)

// fix22E2BigOpening is the review's repro value: 2^63 - 1024.
const fix22E2BigOpening = money.Cents(9223372036854774784)

func TestFix22E2_AbsurdOpeningRefused(t *testing.T) {
	s := testStore(t)
	u := b2TestUser(t, s, "mgr1", "manager")
	if _, err := s.OpenShift(context.Background(), u, fix22E2BigOpening, false); err == nil {
		t.Fatal("expected absurd opening 9223372036854774784 to be refused")
	} else if !strings.Contains(err.Error(), "تجاوز الحد الأقصى") {
		t.Fatalf("refusal = %q, want Arabic cap message", err.Error())
	}
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM Shifts WHERE UserID=?`, u.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("expected no shift row after refused opening, got %d", n)
	}
}

func TestFix22E2_OpeningJustAboveCapRefused(t *testing.T) {
	s := testStore(t)
	u := b2TestUser(t, s, "mgr1", "manager")
	if _, err := s.OpenShift(context.Background(), u, fix22E2Cap+1, false); err == nil {
		t.Fatal("expected opening cap+1 to be refused")
	}
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM Shifts WHERE UserID=?`, u.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("expected no shift row after refused opening, got %d", n)
	}
}

func TestFix22E2_OpeningAtCapOpens(t *testing.T) {
	s := testStore(t)
	u := b2TestUser(t, s, "mgr1", "manager")
	id, err := s.OpenShift(context.Background(), u, fix22E2Cap, false)
	if err != nil {
		t.Fatalf("opening exactly at cap refused: %v", err)
	}
	var stored int64
	if err := s.DB.QueryRow(`SELECT OpeningBalance FROM Shifts WHERE ShiftID=?`, id).Scan(&stored); err != nil {
		t.Fatalf("read opening: %v", err)
	}
	if stored != int64(fix22E2Cap) {
		t.Fatalf("stored opening = %d, want %d", stored, int64(fix22E2Cap))
	}
}

func TestFix22E2_OpeningJustBelowCapOpens(t *testing.T) {
	s := testStore(t)
	u := b2TestUser(t, s, "mgr1", "manager")
	if _, err := s.OpenShift(context.Background(), u, fix22E2Cap-1, false); err != nil {
		t.Fatalf("opening cap-1 refused: %v", err)
	}
}

func TestFix22E2_OpeningNormalStillOpens(t *testing.T) {
	s := testStore(t)
	u := b2TestUser(t, s, "mgr1", "manager")
	if _, err := s.OpenShift(context.Background(), u, money.Cents(100000), false); err != nil {
		t.Fatalf("normal opening refused: %v", err)
	}
}

// TestFix22E2_LegacyAbsurdOpeningSummaryRefuses covers a shift stored
// before the cap (or by direct DB write): its summary must refuse with
// the Arabic overflow message instead of wrapping ExpectedBalance
// negative, and the drawer gate must surface the same Arabic message
// instead of the raw SQLite error.
func TestFix22E2_LegacyAbsurdOpeningSummaryRefuses(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "mgr1", "manager")
	pid := testProductB2(t, s, "أرز", 1, 100, 100000)
	// Direct-DB legacy shift: absurd opening, as the pre-fix code stored it.
	res, err := s.DB.Exec(`INSERT INTO Shifts(UserID,UserName,ShiftDate,OpenTime,Status,OpeningBalance,IsOpen,SessionCount)
		VALUES(?,?,?,?,'Open',?,1,1)`, u.ID, u.UserName, Today(), Now(), int64(fix22E2BigOpening))
	if err != nil {
		t.Fatalf("insert legacy shift: %v", err)
	}
	shiftID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("shift id: %v", err)
	}
	if _, err := s.DB.Exec(`INSERT INTO CashBox(UserID,UserName,ShiftID,MovementType,Amount,Reason,MovementDate)
		VALUES(?,?,?,'open',?,'فتح يوم العمل',?)`,
		u.ID, u.UserName, shiftID, int64(fix22E2BigOpening), Now()); err != nil {
		t.Fatalf("insert open row: %v", err)
	}
	// A 2000-cent cash sale on the shift (as the live sale API writes it).
	testSaleInvoice(t, s, u, shiftID, 1, pid, 20000, 100, 1, 2000, 0, "Cash", 1)
	if _, err := s.DB.Exec(`INSERT INTO CashBox(UserID,UserName,ShiftID,MovementType,Amount,Reason,MovementDate,AutoKind)
		VALUES(?,?,?,'in',?,'بيع نقدي',?,'sale')`,
		u.ID, u.UserName, shiftID, 2000, Now()); err != nil {
		t.Fatalf("insert in row: %v", err)
	}
	sum, err := s.ShiftSummary(ctx, shiftID)
	if err == nil {
		t.Fatalf("expected summary to refuse on wrapped balance, got %+v", sum)
	}
	if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("summary err = %v, want money.ErrOverflow", err)
	}
	if strings.Contains(err.Error(), "SQL logic error") {
		t.Fatalf("summary err leaks raw SQLite text: %v", err)
	}
	// Drawer gate: same Arabic overflow, never the raw SQLite error.
	var gateErr error
	_ = s.Tx(ctx, func(tx *sql.Tx) error {
		_, gateErr = DrawerExpectedBalance(tx, shiftID)
		return nil
	})
	if gateErr == nil {
		t.Fatal("expected drawer gate to refuse on legacy absurd opening")
	}
	if !errors.Is(gateErr, money.ErrOverflow) {
		t.Fatalf("drawer gate err = %v, want money.ErrOverflow", gateErr)
	}
	if strings.Contains(gateErr.Error(), "SQL logic error") {
		t.Fatalf("drawer gate err leaks raw SQLite text: %v", gateErr)
	}
	// Close must refuse too — it must never persist the wrapped
	// negative as متوازن.
	if _, err := s.CloseShift(ctx, u, shiftID); err == nil {
		t.Fatal("expected close to refuse on legacy absurd opening")
	} else if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("close err = %v, want money.ErrOverflow", err)
	}
	var isOpen int
	_ = s.DB.QueryRow(`SELECT IsOpen FROM Shifts WHERE ShiftID=?`, shiftID).Scan(&isOpen)
	if isOpen != 1 {
		t.Fatalf("poisoned shift must stay open after refused close, IsOpen=%d", isOpen)
	}
}

// TestFix22E2_AtCapDrawerGateAgrees proves the restructured
// DrawerExpectedBalance (sums 'in' rows only) returns the same value
// the old 'open'+'in' minus 'open' arithmetic returned for a fresh
// at-cap shift.
func TestFix22E2_AtCapDrawerGateAgrees(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "mgr1", "manager")
	id, err := s.OpenShift(ctx, u, fix22E2Cap, false)
	if err != nil {
		t.Fatalf("open at cap: %v", err)
	}
	var got money.Cents
	var gateErr error
	_ = s.Tx(ctx, func(tx *sql.Tx) error {
		got, gateErr = DrawerExpectedBalance(tx, id)
		return nil
	})
	if gateErr != nil {
		t.Fatalf("drawer gate: %v", gateErr)
	}
	if got != fix22E2Cap {
		t.Fatalf("drawer expected = %d, want cap %d", got, fix22E2Cap)
	}
}

// TestFix22E2_ResumeRefusesStoredAbsurdOpening: a shift closed with a
// normal opening whose stored opening was later raised past the cap
// (pre-cap legacy row) must not resume onto a poisoned shift.
func TestFix22E2_ResumeRefusesStoredAbsurdOpening(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "mgr1", "manager")
	id, err := s.OpenShift(ctx, u, money.Cents(100000), false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := s.CloseShift(ctx, u, id); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := s.DB.Exec(`UPDATE Shifts SET OpeningBalance=? WHERE ShiftID=?`,
		int64(fix22E2BigOpening), id); err != nil {
		t.Fatalf("raise stored opening: %v", err)
	}
	if _, err := s.OpenShift(ctx, u, money.Cents(0), true); err == nil {
		t.Fatal("expected resume of stored absurd opening to be refused")
	} else if !strings.Contains(err.Error(), "تجاوز الحد الأقصى") {
		t.Fatalf("resume refusal = %q, want Arabic cap message", err.Error())
	}
}

func TestFix22E2_ResumeNormalStillWorks(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "mgr1", "manager")
	id, err := s.OpenShift(ctx, u, money.Cents(100000), false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := s.CloseShift(ctx, u, id); err != nil {
		t.Fatalf("close: %v", err)
	}
	id2, err := s.OpenShift(ctx, u, money.Cents(0), true)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if id2 != id {
		t.Fatalf("resume returned shift %d, want %d", id2, id)
	}
}
