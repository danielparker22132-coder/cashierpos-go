// Package web — FIX-17 lane E lock tests (SW2-L06 upper-case locked bell
// path, SW2-L07 global-wins auto-lock precedence).
package web

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"cashierpos-go/internal/auth"
)

// TestFix17LockedUppercaseBellReturns423 (SW2-L06): a locked request to an
// upper-case /api/ path gets 423 JSON, exactly like the lowercase path —
// the locked branch case-folds the /api/ prefix the same way
// lockNoRefresh does. The session stays locked afterwards.
func TestFix17LockedUppercaseBellReturns423(t *testing.T) {
	e := newLockEnv(t)
	if err := e.st.SetSetting("auto_lock_minutes", "1"); err != nil {
		t.Fatal(err)
	}
	e.backdate(2 * time.Minute)
	for _, path := range []string{
		"/API/STAFF/ORDERS/PENDING-EVIDENCE",
		"/api/staff/orders/pending-evidence",
		"/API/STAFF/ORDERS/PENDING-EVIDENCE/",
		"/Api/Staff/Orders/Pending-Count",
	} {
		resp, err := e.noFlow.Get(e.base + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != 423 || !strings.Contains(string(b), `"locked":true`) {
			t.Fatalf("locked GET %s = %d body %q, want 423 with locked:true", path, resp.StatusCode, b)
		}
	}
	// Session stays locked: a page request still redirects to /lock.
	if code, loc, _ := e.get("/"); code != http.StatusSeeOther || !strings.HasPrefix(loc, "/lock?next=") {
		t.Fatalf("after locked polls GET / = %d loc %q, want 303 to /lock", code, loc)
	}
}

// TestFix17EffectiveAutoLockPrecedence pins the SW2-L07 precedence table
// (FEATURE_MAP L-14): global > 0 wins for everyone; global 0 -> the
// user's own value; per-user NULL -> 15; per-user 0 -> never lock.
func TestFix17EffectiveAutoLockPrecedence(t *testing.T) {
	e := newLockEnv(t)
	u := &auth.User{UserName: "cashier"}
	set := func(key, val string) {
		t.Helper()
		if err := e.st.SetSetting(key, val); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name   string
		global string
		own    string // "" = absent (NULL)
		want   int
	}{
		{"global 1 + own 0 -> global", "1", "0", 1},
		{"global 1 + own 5 -> global", "1", "5", 1},
		{"global 0 + own 5 -> own", "0", "5", 5},
		{"global 0 + own absent -> 15", "0", "", 15},
		{"global 0 + own 0 -> never", "0", "0", 0},
		{"global 30 + own invalid -> global", "30", "abc", 30},
		{"global 0 + own invalid -> 15", "0", "abc", 15},
	}
	for _, c := range cases {
		set("auto_lock_minutes", c.global)
		set("auto_lock_minutes:cashier", c.own)
		if got := e.srv.effectiveAutoLockMinutes(u); got != c.want {
			t.Errorf("%s: effective = %d, want %d", c.name, got, c.want)
		}
	}
}

// TestFix17GlobalWinsLiveShape exercises the global-wins branch through
// the middleware: global 1 + per-user 0 locks after 2 min idle, and
// global 0 + per-user 0 never locks.
func TestFix17GlobalWinsLiveShape(t *testing.T) {
	e := newLockEnv(t)
	if err := e.st.SetSetting("auto_lock_minutes", "1"); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetSetting("auto_lock_minutes:cashier", "0"); err != nil {
		t.Fatal(err)
	}
	e.backdate(2 * time.Minute)
	if code, loc, _ := e.get("/"); code != http.StatusSeeOther || !strings.HasPrefix(loc, "/lock?next=") {
		t.Fatalf("global 1 + own 0, idle: GET / = %d loc %q, want 303 to /lock", code, loc)
	}

	if err := e.st.SetSetting("auto_lock_minutes", "0"); err != nil {
		t.Fatal(err)
	}
	// Fresh activity (unlock path would reset the clock; simulate it by
	// re-seeding the stamp to now, then backdating past any timeout).
	token := e.tokenOf(e.cash)
	lockStateMu.Lock()
	lockLast[token] = time.Now()
	lockStateMu.Unlock()
	e.backdate(2 * time.Hour)
	if code, _, _ := e.get("/"); code != http.StatusOK {
		t.Fatalf("global 0 + own 0, idle 2h: GET / = %d, want 200 (never lock)", code)
	}
}

// TestFix17LockSettingsStatesGlobalWins (SW2-L07): the settings page no
// longer claims the per-user value beats the global one.
func TestFix17LockSettingsStatesGlobalWins(t *testing.T) {
	e := newLockEnv(t)
	_, _, body := e.get("/lock/settings")
	if strings.Contains(body, "المدة الخاصة بك تتغلب على المدة العامة") {
		t.Fatal("settings page still states the old per-user-wins rule")
	}
	if !strings.Contains(body, "تطبق على الجميع") {
		t.Fatal("settings page does not state the global-wins rule")
	}
}
