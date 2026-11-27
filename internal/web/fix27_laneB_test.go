// Package web_test — FIX-27 lane B (R27-C-F2 / R27-E-F1 / R27-E-F3): the
// login and change-password renders (routes.go:58, :109) and the API
// login render (handlers_orders.go:116) gated raw driver errors verbatim
// to the client. These tests drive the real handlers against a broken
// store (Users dropped / poisoned UPDATE trigger) and assert the single
// generic Arabic message with no raw driver text, and that the genuine
// Arabic messages (wrong password, empty fields, short password, wrong
// current password) keep their exact text.
package web_test

import (
	"net/url"
	"strings"
	"testing"
)

const fix27LaneBGeneric = "حدث خطأ داخلي، حاول مرة أخرى"

var fix27LaneBRawMarkers = []string{
	"SQL logic error", "no such table", "no such column",
	"constraint failed", "NOT NULL constraint", "UNIQUE constraint",
	"cannot modify", "readonly database", "Scan error",
}

// fix27LaneBHasRaw reports whether body carries any raw driver marker.
func fix27LaneBHasRaw(body string) string {
	for _, m := range fix27LaneBRawMarkers {
		if strings.Contains(body, m) {
			return m
		}
	}
	return ""
}

// TestFix27LaneB_LoginUsersDroppedRendersGeneric is the unauthenticated
// leg: with Users dropped, POST /login must answer the generic Arabic
// message, never "SQL logic error: no such table: Users (1)".
func TestFix27LaneB_LoginUsersDroppedRendersGeneric(t *testing.T) {
	e := newEnv(t, false)
	if _, err := e.store.DB.Exec(`DROP TABLE Users`); err != nil {
		t.Fatalf("drop Users: %v", err)
	}
	c := newClient(t, false)
	st, _, body := e.loginPOST(c, "admin", "admin123")
	if st != 200 {
		t.Fatalf("POST /login Users-dropped: want 200, got %d", st)
	}
	if !strings.Contains(body, fix27LaneBGeneric) {
		t.Fatalf("POST /login Users-dropped: generic message missing, body=%q", truncate(body, 400))
	}
	if m := fix27LaneBHasRaw(body); m != "" {
		t.Fatalf("POST /login Users-dropped: raw driver marker %q leaked, body=%q", m, truncate(body, 400))
	}
}

// TestFix27LaneB_ChangePasswordPoisonedUpdateRendersGeneric poisons the
// password UPDATE with a trigger (the live repro shape: the UPDATE
// fails with a NOT NULL constraint error while SELECTs keep working),
// then POST /change-password must answer the generic Arabic message.
func TestFix27LaneB_ChangePasswordPoisonedUpdateRendersGeneric(t *testing.T) {
	e := newEnv(t, false)
	e.changeAdminPassword()
	c := e.loginAs("admin", adminNewPass)
	if _, err := e.store.DB.Exec(`CREATE TRIGGER trg_pw_fail BEFORE UPDATE ON Users
		BEGIN INSERT INTO Users(UserID) VALUES(NEW.UserID); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	code, body := e.postForm(c, "/change-password", url.Values{
		"current": {adminNewPass}, "new": {"NewPass1234"}, "confirm": {"NewPass1234"},
	})
	if code != 200 {
		t.Fatalf("POST /change-password poisoned: want 200, got %d", code)
	}
	if !strings.Contains(body, fix27LaneBGeneric) {
		t.Fatalf("POST /change-password poisoned: generic message missing, body=%q", truncate(body, 400))
	}
	if m := fix27LaneBHasRaw(body); m != "" {
		t.Fatalf("POST /change-password poisoned: raw driver marker %q leaked, body=%q", m, truncate(body, 400))
	}
}

// TestFix27LaneB_APILoginUsersDroppedRendersGeneric is the audit extra:
// POST /api/auth/login with Users dropped must answer 401 with the
// generic message, never the raw driver string in JSON.
func TestFix27LaneB_APILoginUsersDroppedRendersGeneric(t *testing.T) {
	e := newEnv(t, false)
	if _, err := e.store.DB.Exec(`DROP TABLE Users`); err != nil {
		t.Fatalf("drop Users: %v", err)
	}
	c := newClient(t, false)
	code, m := e.postJSON(c, "/api/auth/login", map[string]any{
		"username": "admin", "password": "admin123",
	})
	if code != 401 {
		t.Fatalf("POST /api/auth/login Users-dropped: want 401, got %d (%v)", code, m)
	}
	if m["error"] != fix27LaneBGeneric {
		t.Fatalf("POST /api/auth/login Users-dropped: error=%v, want generic %q", m["error"], fix27LaneBGeneric)
	}
}

// TestFix27LaneB_GenuineMessagesKept proves the gate does not touch
// safe Arabic: wrong password, empty fields, short new password and
// wrong current password keep their exact text.
func TestFix27LaneB_GenuineMessagesKept(t *testing.T) {
	e := newEnv(t, false)
	c := newClient(t, false)
	cases := []struct{ user, pass, want string }{
		{"admin", "WrongPass999", "اسم المستخدم أو كلمة المرور غير صحيحة"},
		{"", "x", "برجاء إدخال اسم المستخدم"},
		{"admin", "", "برجاء إدخال كلمة المرور"},
	}
	for _, tc := range cases {
		st, _, body := e.loginPOST(c, tc.user, tc.pass)
		if st != 200 || !strings.Contains(body, tc.want) {
			t.Fatalf("login %q/%q: want 200 + %q, got %d body=%q", tc.user, tc.pass, tc.want, st, truncate(body, 300))
		}
		if strings.Contains(body, fix27LaneBGeneric) {
			t.Fatalf("login %q/%q: genuine message replaced by generic, body=%q", tc.user, tc.pass, truncate(body, 300))
		}
	}
	e.changeAdminPassword()
	ac := e.loginAs("admin", adminNewPass)
	code, body := e.postForm(ac, "/change-password", url.Values{
		"current": {adminNewPass}, "new": {"abc"}, "confirm": {"abc"},
	})
	if code != 200 || !strings.Contains(body, "كلمة المرور يجب أن تكون 4 أحرف على الأقل") {
		t.Fatalf("short new password: want 200 + exact Arabic, got %d body=%q", code, truncate(body, 300))
	}
	code, body = e.postForm(ac, "/change-password", url.Values{
		"current": {"WrongCur1"}, "new": {"NewPass1234"}, "confirm": {"NewPass1234"},
	})
	if code != 200 || !strings.Contains(body, "كلمة السر الحالية غير صحيحة") {
		t.Fatalf("wrong current password: want 200 + exact Arabic, got %d body=%q", code, truncate(body, 300))
	}
}
