// Package store — FIX-13 money leg (R13-M3): reset-DB FK re-enable.
package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// fkOn reports the PRAGMA foreign_keys value (1 = ON) on the store.
func fkOn(t *testing.T, s *Store) int {
	t.Helper()
	var v int
	if err := s.DB.QueryRow(`PRAGMA foreign_keys`).Scan(&v); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	return v
}

// fkViolationFails asserts FK enforcement is live: a Payments row pointing
// at a nonexistent invoice must be refused while FK is ON.
func fkViolationFails(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.DB.Exec(`INSERT INTO Payments(InvoiceID,CustomerID,Amount)
		VALUES(999999,1,100)`); err == nil {
		t.Fatal("FK-violating Payments insert succeeded — foreign_keys is OFF")
	}
}

// TestFix13ResetLeavesFKOn — R13-M3: after a successful reset on a DB
// with sales, FK enforcement is ON (read back, not assumed) and live,
// and a second consecutive reset works too.
func TestFix13ResetLeavesFKOn(t *testing.T) {
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	pid := testProduct(t, s, "سكر", 1000, 2000, 10000)
	b1OpenShiftFor(t, s, env.cashier)
	b1CashCustomerSale(t, s, env, env.cashier, pid, 1000)

	if err := s.ResetDatabase(env.ctx, env.admin, ResetConfirmation); err != nil {
		t.Fatalf("ResetDatabase with a sale in the DB: %v", err)
	}
	if got := fkOn(t, s); got != 1 {
		t.Fatalf("PRAGMA foreign_keys after reset = %d, want 1", got)
	}
	fkViolationFails(t, s)

	// A second consecutive reset works and leaves FK ON as well.
	if err := s.ResetDatabase(env.ctx, env.admin, ResetConfirmation); err != nil {
		t.Fatalf("second ResetDatabase: %v", err)
	}
	if got := fkOn(t, s); got != 1 {
		t.Fatalf("PRAGMA foreign_keys after second reset = %d, want 1", got)
	}
	fkViolationFails(t, s)
}

// TestFix13ResetFailedKeepsFKOn — R13-M3: after a FAILED reset, FK is
// still ON. The failure is forced for real: a second connection holds a
// RESERVED write lock (BEGIN IMMEDIATE) on the same file DB, so the
// drop transaction cannot get its write lock and fails with
// "database is locked" (after the 5 s busy timeout, twice — once for
// the best-effort audit, once for the drop). The reset must return the
// error AND leave FK enforcement ON and the DB usable.
func TestFix13ResetFailedKeepsFKOn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reset-fk.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open file DB: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	admin := testAdmin()

	// Second connection holding a RESERVED lock on the same file.
	locker, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open locker DB: %v", err)
	}
	defer locker.Close()
	locker.SetMaxOpenConns(1)
	if _, err := locker.Exec(`BEGIN IMMEDIATE`); err != nil {
		t.Fatalf("BEGIN IMMEDIATE on locker: %v", err)
	}
	// The reset must fail (its drop cannot get the write lock) ...
	if err := s.ResetDatabase(ctx, admin, ResetConfirmation); err == nil {
		t.Fatal("ResetDatabase under a write lock should fail, got nil error")
	}
	// ... but FK must be ON afterwards, live, and the DB intact.
	// (The pragma read works under the lock; the FK-violation probe
	// needs a write, so the lock is released before running it —
	// otherwise the lock itself, not FK, would refuse the insert.)
	if got := fkOn(t, s); got != 1 {
		t.Fatalf("PRAGMA foreign_keys after FAILED reset = %d, want 1", got)
	}
	if _, err := locker.Exec(`ROLLBACK`); err != nil {
		t.Fatalf("locker ROLLBACK: %v", err)
	}
	fkViolationFails(t, s)
	var users int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM Users`).Scan(&users); err != nil || users != 1 {
		t.Fatalf("users after failed reset = %d, err %v (want 1 — DB untouched)", users, err)
	}

	// A normal reset now succeeds and leaves FK ON.
	if err := s.ResetDatabase(ctx, admin, ResetConfirmation); err != nil {
		t.Fatalf("ResetDatabase after the lock is released: %v", err)
	}
	if got := fkOn(t, s); got != 1 {
		t.Fatalf("PRAGMA foreign_keys after recovery reset = %d, want 1", got)
	}
	fkViolationFails(t, s)
}
