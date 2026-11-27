// Package web — FIX-27 lane B (R27-C-F2 / R27-E-F1 / R27-E-F3), internal
// half: calls the auth functions directly against a broken store and
// asserts safeHTMLErr maps the genuine driver errors to the generic
// Arabic message while the sentinel Arabic errors pass through verbatim.
package web

import (
	"testing"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/store"
)

// TestFix27LaneB_GateOnRealAuthErrors drives auth.Authenticate and
// auth.ChangePassword against a broken store (Users dropped / poisoned
// UPDATE trigger) and checks the gate on the real errors, not on string
// literals.
func TestFix27LaneB_GateOnRealAuthErrors(t *testing.T) {
	st, err := store.OpenTest()
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	defer st.Close()
	if _, err := st.DB.Exec(`DROP TABLE Users`); err != nil {
		t.Fatalf("drop Users: %v", err)
	}
	if _, err := auth.Authenticate(st.DB, "admin", "admin123"); err == nil {
		t.Fatalf("Authenticate Users-dropped: want an error")
	} else if got := safeHTMLErr(err); got != "حدث خطأ داخلي، حاول مرة أخرى" {
		t.Fatalf("safeHTMLErr(Authenticate raw err %q) = %q, want generic", err.Error(), got)
	}
	if got := safeHTMLErr(auth.ErrBadCredentials); got != auth.ErrBadCredentials.Error() {
		t.Fatalf("safeHTMLErr(ErrBadCredentials) = %q, want it verbatim", got)
	}
	if got := safeHTMLErr(auth.ErrInactive); got != auth.ErrInactive.Error() {
		t.Fatalf("safeHTMLErr(ErrInactive) = %q, want it verbatim", got)
	}

	// ChangePassword raw error: fresh store, poisoned UPDATE trigger.
	st2, err := store.OpenTest()
	if err != nil {
		t.Fatalf("open test store 2: %v", err)
	}
	defer st2.Close()
	if _, err := st2.DB.Exec(`CREATE TRIGGER trg_pw_fail BEFORE UPDATE ON Users
		BEGIN INSERT INTO Users(UserID) VALUES(NEW.UserID); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	var id int64
	if err := st2.DB.QueryRow(`SELECT UserID FROM Users WHERE UserName='admin'`).Scan(&id); err != nil {
		t.Fatalf("admin id: %v", err)
	}
	if err := auth.ChangePassword(st2.DB, id, "NewPass1234"); err == nil {
		t.Fatalf("ChangePassword poisoned: want an error")
	} else if got := safeHTMLErr(err); got != "حدث خطأ داخلي، حاول مرة أخرى" {
		t.Fatalf("safeHTMLErr(ChangePassword raw err %q) = %q, want generic", err.Error(), got)
	}
}
