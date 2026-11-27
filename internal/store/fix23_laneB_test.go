package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"cashierpos-go/internal/money"
)

// --- FIX-23 R23-C-F1: Dashboard / Shift Monitor silently zero the
// expected drawer on overflow ---
//
// Dashboard (domain_reports.go, shifts loop) and ShiftMonitorFor
// (open-shifts loop) both ran
// `_ = s.Tx(ctx, func(tx *sql.Tx) error { exp, err :=
// DrawerExpectedBalance(tx, ...); if err == nil { ...ExpectedDrawer
// = exp }; return nil })`, swallowing the error so ExpectedDrawer
// stayed at its zero value. Live repro (review 23, lane C): dashboard
// before poison showed the shift row (opening 100.00, expected
// 102.00); after 2 CashBox 'in' rows of 5e18 on the open shift it
// rendered 200 with (100.00, 0.00) and no overflow message; the
// monitor did the same (was 101.00, became 0.00). The fix propagates
// the DrawerExpectedBalance error from the loop, like the :900/:909
// SUMs already do. The poison value 5e18 is the review's repro value:
// two such 'in' rows overflow SQLite's int64 SUM, which
// drawerOverflowErr maps to money.ErrOverflow (Arabic, never the raw
// SQLite string).

const fix23PoisonCashBox = int64(5000000000000000000)

// TestFix23_DashboardDrawerOverflowSurfaces is the primary F1 shape:
// a CashBox-poisoned open shift must make Dashboard refuse with the
// Arabic overflow error, never render ExpectedDrawer as a silent 0.00.
func TestFix23_DashboardDrawerOverflowSurfaces(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b2TestUser(t, s, "adm1", "admin")
	u := b2TestUser(t, s, "sup1", "supervisor")
	shiftID, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := s.DB.Exec(`INSERT INTO CashBox(UserID,UserName,ShiftID,MovementType,Amount,Reason,MovementDate)
			VALUES(?,?,?,'in',?,'poison-in',?)`,
			u.ID, u.UserName, shiftID, fix23PoisonCashBox, Now()); err != nil {
			t.Fatalf("insert poison CashBox row: %v", err)
		}
	}
	d, err := s.Dashboard(ctx, admin)
	if err == nil {
		t.Fatalf("expected Dashboard to fail on poisoned CashBox SUM, got nil error (shifts=%+v)", d.Shifts)
	}
	if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("err = %v, want money.ErrOverflow", err)
	}
	if strings.Contains(err.Error(), "SQL logic error") {
		t.Fatalf("dashboard error leaked a raw SQLite error: %v", err)
	}
}

// TestFix23_ShiftMonitorDrawerOverflowSurfaces is the same F1 shape
// on ShiftMonitorFor: the open-shifts loop must propagate the error.
func TestFix23_ShiftMonitorDrawerOverflowSurfaces(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b2TestUser(t, s, "adm1", "admin")
	u := b2TestUser(t, s, "sup1", "supervisor")
	shiftID, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := s.DB.Exec(`INSERT INTO CashBox(UserID,UserName,ShiftID,MovementType,Amount,Reason,MovementDate)
			VALUES(?,?,?,'in',?,'poison-in',?)`,
			u.ID, u.UserName, shiftID, fix23PoisonCashBox, Now()); err != nil {
			t.Fatalf("insert poison CashBox row: %v", err)
		}
	}
	m, err := s.ShiftMonitorFor(ctx, admin, Today(), Today())
	if err == nil {
		t.Fatalf("expected ShiftMonitorFor to fail on poisoned CashBox SUM, got nil error (shifts=%+v)", m.OpenShifts)
	}
	if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("err = %v, want money.ErrOverflow", err)
	}
	if strings.Contains(err.Error(), "SQL logic error") {
		t.Fatalf("monitor error leaked a raw SQLite error: %v", err)
	}
}

// TestFix23_DashboardDrawerNormalUnchanged is the control: a healthy
// shift still reports its real expected drawer (opening 100.00 +
// 2.00 cash in = 102.00) and no error.
func TestFix23_DashboardDrawerNormalUnchanged(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b2TestUser(t, s, "adm1", "admin")
	u := b2TestUser(t, s, "sup1", "supervisor")
	shiftID, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	if _, err := s.DB.Exec(`INSERT INTO CashBox(UserID,UserName,ShiftID,MovementType,Amount,Reason,MovementDate,AutoKind)
		VALUES(?,?,?,'in',?,'بيع نقدي',?,'sale')`,
		u.ID, u.UserName, shiftID, 200, Now()); err != nil {
		t.Fatalf("insert cash-in row: %v", err)
	}
	d, err := s.Dashboard(ctx, admin)
	if err != nil {
		t.Fatalf("Dashboard on a healthy shift refused: %v", err)
	}
	if len(d.Shifts) != 1 {
		t.Fatalf("Dashboard shifts = %d, want 1", len(d.Shifts))
	}
	if d.Shifts[0].ExpectedDrawer != money.Cents(10200) {
		t.Fatalf("ExpectedDrawer = %v, want 10200 (100.00 opening + 2.00 cash in)", d.Shifts[0].ExpectedDrawer)
	}
}

// TestFix23_ShiftMonitorDrawerNormalUnchanged is the monitor control.
func TestFix23_ShiftMonitorDrawerNormalUnchanged(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b2TestUser(t, s, "adm1", "admin")
	u := b2TestUser(t, s, "sup1", "supervisor")
	shiftID, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	if _, err := s.DB.Exec(`INSERT INTO CashBox(UserID,UserName,ShiftID,MovementType,Amount,Reason,MovementDate,AutoKind)
		VALUES(?,?,?,'in',?,'بيع نقدي',?,'sale')`,
		u.ID, u.UserName, shiftID, 200, Now()); err != nil {
		t.Fatalf("insert cash-in row: %v", err)
	}
	m, err := s.ShiftMonitorFor(ctx, admin, Today(), Today())
	if err != nil {
		t.Fatalf("ShiftMonitorFor on a healthy shift refused: %v", err)
	}
	if len(m.OpenShifts) != 1 {
		t.Fatalf("monitor open shifts = %d, want 1", len(m.OpenShifts))
	}
	if m.OpenShifts[0].ExpectedDrawer != money.Cents(10200) {
		t.Fatalf("ExpectedDrawer = %v, want 10200", m.OpenShifts[0].ExpectedDrawer)
	}
}
