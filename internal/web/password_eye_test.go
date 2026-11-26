// Package web_test — password eye toggle + login sync note (feature 3).
//
// Every check reads the HTML the server actually renders: each password
// input must be type=password with an adjacent eye toggle button, no
// password input may be type=text, and the login page must state plainly
// that invoices are stored locally only and sync is not enabled yet
// (the app has no real sync mode — server.go/routes.go carry no sync
// setting, so the note is always shown).
//
// Run: go test -timeout 30m -count=1 ./internal/web/ -run 'TestPasswordEye' -v
package web_test

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

const syncNoteText = "الفواتير تُحفظ محليًا فقط — المزامنة غير مفعلة بعد"

// assertPasswordFields checks the rendered HTML for the named password
// fields: each must be a type=password input, each must have exactly one
// adjacent eye toggle button (type=button, tooltip "إظهار كلمة المرور",
// open + closed eye icons), and no password input may be type=text.
func assertPasswordFields(t *testing.T, body string, fields ...string) {
	t.Helper()
	for _, f := range fields {
		re := regexp.MustCompile(`<input[^>]*name="` + regexp.QuoteMeta(f) + `"[^>]*>`)
		tag := re.FindString(body)
		if tag == "" {
			t.Fatalf("password field %q: no <input> found in rendered HTML", f)
		}
		if !strings.Contains(tag, `type="password"`) {
			t.Fatalf("password field %q: input is not type=password: %s", f, tag)
		}
	}
	// One eye toggle button per password field, and it must be
	// type="button" so clicking it never submits the form.
	if n := strings.Count(body, `<button type="button" data-pw-eye`); n != len(fields) {
		t.Fatalf("want %d eye toggle buttons, got %d", len(fields), n)
	}
	// Tooltip on every toggle (masked state) + both icons per toggle.
	if n := strings.Count(body, `title="إظهار كلمة المرور"`); n != len(fields) {
		t.Fatalf("want %d eye tooltips, got %d", len(fields), n)
	}
	if n := strings.Count(body, `class="pw-eye-open"`); n != len(fields) {
		t.Fatalf("want %d open-eye icons, got %d", len(fields), n)
	}
	if n := strings.Count(body, `class="pw-eye-closed"`); n != len(fields) {
		t.Fatalf("want %d closed-eye icons, got %d", len(fields), n)
	}
	// No password input rendered as type=text, in either attribute order.
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`<input[^>]*type="text"[^>]*name="(password|current|new|confirm)"`),
		regexp.MustCompile(`<input[^>]*name="(password|current|new|confirm)"[^>]*type="text"`),
	} {
		if m := re.FindString(body); m != "" {
			t.Fatalf("password input rendered as type=text: %s", m)
		}
	}
}

func TestPasswordEye_LoginPage(t *testing.T) {
	e := newEnv(t, false)
	c := newClient(t, true)
	code, body := e.get(c, "/login")
	if code != http.StatusOK {
		t.Fatalf("GET /login: want 200, got %d", code)
	}
	assertPasswordFields(t, body, "password")
	if !strings.Contains(body, syncNoteText) {
		t.Fatalf("login page: sync note %q not found", syncNoteText)
	}
	if strings.Contains(body, "المزامنة مفعلة") || strings.Contains(body, "تمت المزامنة") {
		t.Fatalf("login page: must not claim sync exists")
	}
}

func TestPasswordEye_LoginPageAfterFailedLogin(t *testing.T) {
	e := newEnv(t, false)
	c := newClient(t, false)
	st, _, body := e.loginPOST(c, "admin", "wrong-password")
	if st != http.StatusOK {
		t.Fatalf("failed login: want re-rendered login page (200), got %d", st)
	}
	assertPasswordFields(t, body, "password")
	if !strings.Contains(body, syncNoteText) {
		t.Fatalf("login page after failed login: sync note %q not found", syncNoteText)
	}
}

func TestPasswordEye_ChangePasswordPage(t *testing.T) {
	e := newEnv(t, false)
	e.seedUsers()
	e.changeAdminPassword()
	c := e.adminClient()
	code, body := e.get(c, "/change-password")
	if code != http.StatusOK {
		t.Fatalf("GET /change-password: want 200, got %d", code)
	}
	assertPasswordFields(t, body, "current", "new", "confirm")
}

func TestPasswordEye_UserFormNew(t *testing.T) {
	e := newEnv(t, false)
	e.seedUsers()
	e.changeAdminPassword()
	c := e.adminClient()
	code, body := e.get(c, "/users/new")
	if code != http.StatusOK {
		t.Fatalf("GET /users/new: want 200, got %d", code)
	}
	assertPasswordFields(t, body, "password")
}

func TestPasswordEye_UserFormEdit(t *testing.T) {
	e := newEnv(t, false)
	e.seedUsers()
	e.changeAdminPassword()
	c := e.adminClient()
	code, body := e.get(c, fmt.Sprintf("/users/%d/edit", e.userID["cashier"]))
	if code != http.StatusOK {
		t.Fatalf("GET /users/{id}/edit: want 200, got %d", code)
	}
	assertPasswordFields(t, body, "password")
}
