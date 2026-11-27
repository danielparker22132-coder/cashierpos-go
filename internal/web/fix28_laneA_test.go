// Fix-28 lane A: the header logout link was a no-op in the real
// (headless/cloud) browser — navigation was gated on a native JS
// confirm(), which the cloud browser auto-dismisses (returns false),
// so the click cancelled navigation entirely. The fix is a plain link
// to /logout: logout is fully reversible (log back in, no data is
// destroyed — handleLogout only deletes the session row and clears the
// cookie), so no confirmation step is needed.
//
// These tests assert code markers only (the served HTML), never
// template comment markers — html/template strips JS // comments.
package web_test

import (
	"net/http"
	"strings"
	"testing"
)

// The logout link must be a plain link with no confirm() gate: any
// onclick="return confirm(...)" on it makes the click a no-op wherever
// confirm() is auto-dismissed.
func TestFix28LaneA_LogoutLinkHasNoConfirmGate(t *testing.T) {
	e := newEnv(t, false)
	e.seedUsers()
	c := e.cashierClient()

	for _, path := range []string{"/", "/sales"} {
		code, body := e.get(c, path)
		if code != http.StatusOK {
			t.Fatalf("GET %s: want 200, got %d", path, code)
		}
		if !strings.Contains(body, `<a class="toplink" href="/logout">خروج</a>`) {
			t.Fatalf("GET %s: served HTML lacks the plain logout link", path)
		}
		if strings.Contains(body, `href="/logout" onclick`) {
			t.Fatalf("GET %s: logout link still carries an onclick gate", path)
		}
		if strings.Contains(body, `confirm('هل تريد تسجيل الخروج؟')`) {
			t.Fatalf("GET %s: logout confirm() gate still present in served HTML", path)
		}
	}
}

// GET /logout must end the session: 303 to /login, session cookie
// cleared, session row deleted, and the same cookie no longer admits
// the caller to a protected page.
func TestFix28LaneA_LogoutEndsSession(t *testing.T) {
	e := newEnv(t, false)
	e.seedUsers()
	c := newClient(t, false)
	st, loc, _ := e.loginPOST(c, "cashier", cashierPass)
	if st != http.StatusSeeOther || loc != "/" {
		t.Fatalf("login: want 303 to /, got %d loc %q", st, loc)
	}
	// One live session row for cashier before logout.
	var before int
	if err := e.store.DB.QueryRow(`SELECT COUNT(*) FROM Sessions WHERE UserName='cashier'`).Scan(&before); err != nil || before != 1 {
		t.Fatalf("sessions before logout = %d, err %v, want 1", before, err)
	}

	resp, err := c.Get(e.base + "/logout")
	if err != nil {
		t.Fatalf("GET /logout: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("GET /logout: want 303, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/login" {
		t.Fatalf("GET /logout: want Location /login, got %q", got)
	}
	cleared := false
	for _, ck := range resp.Cookies() {
		if ck.Name == "cashierpos_session" && ck.Value == "" && ck.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatalf("GET /logout: session cookie not cleared (cookies: %v)", resp.Cookies())
	}
	var after int
	if err := e.store.DB.QueryRow(`SELECT COUNT(*) FROM Sessions WHERE UserName='cashier'`).Scan(&after); err != nil || after != 0 {
		t.Fatalf("sessions after logout = %d, err %v, want 0", after, err)
	}
	// The same client (same jar) is now unauthenticated.
	resp2, err := c.Get(e.base + "/sales")
	if err != nil {
		t.Fatalf("GET /sales after logout: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusSeeOther || resp2.Header.Get("Location") != "/login" {
		t.Fatalf("GET /sales after logout: want 303 to /login, got %d loc %q", resp2.StatusCode, resp2.Header.Get("Location"))
	}
}

// Normal human flow still works end to end: log in, use hub + sales,
// log out via the plain link, log back in.
func TestFix28LaneA_NormalFlowLoginLogoutLogin(t *testing.T) {
	e := newEnv(t, false)
	e.seedUsers()
	c := e.cashierClient()
	for _, path := range []string{"/", "/sales"} {
		code, _ := e.get(c, path)
		if code != http.StatusOK {
			t.Fatalf("GET %s before logout: want 200, got %d", path, code)
		}
	}
	code, body := e.get(c, "/logout")
	if code != http.StatusOK {
		t.Fatalf("GET /logout (following): want 200 login page, got %d", code)
	}
	if !strings.Contains(body, "تسجيل الدخول") {
		t.Fatalf("GET /logout (following): body is not the login page")
	}
	// Log back in with the same client; the hub must answer again.
	c2 := e.cashierClient()
	code, _ = e.get(c2, "/")
	if code != http.StatusOK {
		t.Fatalf("GET / after re-login: want 200, got %d", code)
	}
}
