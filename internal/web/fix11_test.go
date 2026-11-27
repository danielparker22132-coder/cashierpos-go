// Package web — fix 11: the web-bell 5 s poll must not refresh the idle
// clock. Before this fix the poll refreshed lockLast on every hit, so
// with any app page open the 1-minute (or any) timeout could never
// elapse and the auto-lock was effectively dead in real browser use.
// The poll still goes through the expiry check: a locked session gets
// 423 from it, never a resurrection.
package web

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// stampAge returns how long ago the cashier session's last activity was.
func (e *lockEnv) stampAge() time.Duration {
	e.t.Helper()
	token := e.tokenOf(e.cash)
	lockStateMu.Lock()
	last := lockLast[token]
	lockStateMu.Unlock()
	return time.Since(last)
}

func TestFix11BellPollDoesNotRefreshIdleClock(t *testing.T) {
	e := newLockEnv(t)
	if err := e.st.SetSetting("auto_lock_minutes", "1"); err != nil {
		t.Fatal(err)
	}
	// Seed the clock 30 s in the past: inside the 1-minute timeout.
	e.backdate(30 * time.Second)

	// The bell poll must succeed while the session is active...
	for _, tc := range []struct{ path, marker string }{
		{"/api/staff/orders/pending-count", "pendingCount"},
		{"/api/staff/orders/pending-evidence", "pendingEvidence"},
	} {
		code, _, body := e.get(tc.path)
		if code != http.StatusOK || !strings.Contains(body, tc.marker) {
			t.Fatalf("bell poll %s = %d body %s, want 200 with %q", tc.path, code, body, tc.marker)
		}
		// ...but must NOT refresh the idle stamp.
		if age := e.stampAge(); age < 25*time.Second {
			t.Fatalf("bell poll %s refreshed the idle clock: stamp age %v, want >= 25s", tc.path, age)
		}
	}

	// A normal page request DOES refresh the clock (control).
	e.backdate(30 * time.Second)
	if code, _, _ := e.get("/"); code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", code)
	}
	if age := e.stampAge(); age > 5*time.Second {
		t.Fatalf("GET / did not refresh the idle clock: stamp age %v, want < 5s", age)
	}

	// The event-driven heartbeat DOES refresh the clock (control: it is
	// real user activity — mouse/key only, at most once per 60 s).
	e.backdate(30 * time.Second)
	if code, _, _ := e.get("/api/activity"); code != http.StatusOK {
		t.Fatalf("GET /api/activity = %d, want 200", code)
	}
	if age := e.stampAge(); age > 5*time.Second {
		t.Fatalf("heartbeat did not refresh the idle clock: stamp age %v, want < 5s", age)
	}

	// An already-locked session still gets 423 from the bell poll.
	e.backdate(61 * time.Second)
	for _, path := range []string{"/api/staff/orders/pending-count", "/api/staff/orders/pending-evidence"} {
		code, _, body := e.get(path)
		if code != 423 || !strings.Contains(body, `"locked":true`) {
			t.Fatalf("locked bell poll %s = %d body %s, want 423 with locked:true", path, code, body)
		}
	}
	// ...and the poll must not resurrect the session: a page request
	// after it still redirects to /lock.
	if code, loc, _ := e.get("/"); code != http.StatusSeeOther || !strings.HasPrefix(loc, "/lock?next=") {
		t.Fatalf("after locked polls, GET / = %d loc %q, want redirect to /lock", code, loc)
	}
}
