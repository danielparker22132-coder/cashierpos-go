// Package web — fix 12, lane A (lock / review-12).
//
// R12-L1: a locked tab's bell poll used to die silently on 423
// (clearInterval, stale badges, no route to the lock screen). The
// served JS must now navigate to /lock?next=<current path> on a 423
// and keep the bell alive with a back-off retry on other errors.
// R12-L2: trailing-slash / case variants of the bell paths used to
// refresh the idle clock (exact string equality in lockNoRefresh,
// refresh before mux routing). They must now behave like the exact
// paths: no refresh while active, 423 while locked, no resurrection.
// SW-L09: a lock-settings save as warehouse/cashier used to show the
// generic "تم حفظ إعدادات القفل التلقائي" notice even though the
// global value was never written for them. The notice must only claim
// what was actually saved.
package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"cashierpos-go/internal/store"
)

// TestFix12BellPollVariantsDoNotRefreshIdleClock — R12-L2.
func TestFix12BellPollVariantsDoNotRefreshIdleClock(t *testing.T) {
	e := newLockEnv(t)
	if err := e.st.SetSetting("auto_lock_minutes", "1"); err != nil {
		t.Fatal(err)
	}
	e.backdate(30 * time.Second)

	// Trailing-slash, double-trailing-slash and upper-case variants
	// of the bell paths must NOT refresh the idle clock (the mux
	// 404s them — the point is the stamp, not the status).
	for _, path := range []string{
		"/api/staff/orders/pending-count/",
		"/api/staff/orders/pending-evidence/",
		"/API/staff/orders/pending-count",
		"/API/STaff/orders/pending-evidence",
		// FIX-13 I1: double trailing slash (TrimSuffix stripped
		// exactly one, so these used to refresh the clock).
		"/api/staff/orders/pending-count//",
		"/api/staff/orders/pending-evidence//",
		"/API/staff/orders/pending-count//",
	} {
		code, _, _ := e.get(path)
		// Status is incidental (the stamp is the assertion): the
		// single-slash / upper-case variants 404 (no such route),
		// while a double-slash path gets the mux's canonical-path
		// 301 to the single-slash variant (which then 404s) —
		// either way the request is not a real bell poll.
		if code != http.StatusNotFound && code != http.StatusMovedPermanently {
			t.Fatalf("GET %s = %d, want 404 or 301 (mux has no such route)", path, code)
		}
		if age := e.stampAge(); age < 25*time.Second {
			t.Fatalf("GET %s refreshed the idle clock: stamp age %v, want >= 25s", path, age)
		}
	}

	// A POST to a bell path is not a real route either and must not
	// refresh the clock either. (This tree answers 404 for it — the
	// GET / hub subtree pattern means the mux reports no match for
	// the wrong method — not 405; the status is incidental, the
	// stamp is the assertion.)
	e.backdate(30 * time.Second)
	if code, _, _ := e.postForm(e.cash, "/api/staff/orders/pending-count", url.Values{}); code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
		t.Fatalf("POST bell path = %d, want 404 or 405", code)
	}
	if age := e.stampAge(); age < 25*time.Second {
		t.Fatalf("POST bell path refreshed the idle clock: stamp age %v, want >= 25s", age)
	}

	// Locked + trailing slash: still 423, and no resurrection — a
	// following page request still redirects to /lock.
	e.backdate(61 * time.Second)
	if code, _, body := e.get("/api/staff/orders/pending-count/"); code != 423 || !strings.Contains(body, `"locked":true`) {
		t.Fatalf("locked trailing-slash poll = %d body %s, want 423 with locked:true", code, body)
	}
	// FIX-13 I1: locked + double trailing slash on the other bell
	// path: still 423, still no resurrection.
	if code, _, body := e.get("/api/staff/orders/pending-evidence//"); code != 423 || !strings.Contains(body, `"locked":true`) {
		t.Fatalf("locked double-slash poll = %d body %s, want 423 with locked:true", code, body)
	}
	if code, loc, _ := e.get("/"); code != http.StatusSeeOther || !strings.HasPrefix(loc, "/lock?next=") {
		t.Fatalf("after locked trailing-slash poll, GET / = %d loc %q, want redirect to /lock", code, loc)
	}
}

// TestFix12LockedTabNavigatesToLock — R12-L1 (code + curl-level
// proof; a real-browser navigation check is the parent's job).
// The served page must carry the 423 -> /lock redirect for the bell
// poll (which the "تحديث الآن" button shares) and for the heartbeat,
// plus the back-off retry on transient errors; and a locked session's
// poll must answer 423, which is what the JS keys on.
func TestFix12LockedTabNavigatesToLock(t *testing.T) {
	e := newLockEnv(t)
	if err := e.st.SetSetting("auto_lock_minutes", "1"); err != nil {
		t.Fatal(err)
	}
	code, _, body := e.get("/")
	if code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", code)
	}
	// The bell poll keys a 423 into a navigation to /lock?next=<path>.
	if !strings.Contains(body, "status===423") {
		t.Fatalf("served page has no 423 check in the bell poll JS")
	}
	if !strings.Contains(body, "window.location.href='/lock?next='") {
		t.Fatalf("served page has no /lock?next= navigation in the bell poll JS")
	}
	if !strings.Contains(body, "err.message==='locked'") {
		t.Fatalf("served page does not distinguish the locked error from transient errors")
	}
	// Transient errors must NOT kill the poll permanently: a back-off
	// retry must be scheduled instead of clearInterval-until-reload.
	if !strings.Contains(body, "setTimeout(function(){ retryPending=false; poll();") {
		t.Fatalf("served page has no back-off retry after a transient poll failure")
	}
	// The old silent-stop must be gone: no bare clearInterval that
	// kills the timer on any error.
	if strings.Contains(body, ".catch(function(){if(timer){clearInterval(timer);timer=null;}}") {
		t.Fatalf("served page still contains the old silent-stop poll catch")
	}
	// The "تحديث الآن" button shares the same poll() (one fix covers
	// both), and the heartbeat also navigates on 423.
	if !strings.Contains(body, "orders-refresh-now") {
		t.Fatalf("served page has no 'تحديث الآن' refresh button")
	}
	if !strings.Contains(body, "r.status === 423") {
		t.Fatalf("served page heartbeat has no 423 -> /lock navigation")
	}

	// Curl-level half: a locked session's poll answers 423.
	e.backdate(61 * time.Second)
	if code, _, body := e.get("/api/staff/orders/pending-count"); code != 423 || !strings.Contains(body, `"locked":true`) {
		t.Fatalf("locked bell poll = %d body %s, want 423 with locked:true", code, body)
	}
}

// TestFix12LockSettingsSaveNoticeHonest — SW-L09.
// A warehouse save that carries global_minutes=45 must leave the
// global value unchanged AND must not claim the global was saved;
// an admin save with the same value must change it and may use the
// generic notice. Both directions.
func TestFix12LockSettingsSaveNoticeHonest(t *testing.T) {
	e := newLockEnv(t)
	if err := e.st.SetSetting("auto_lock_minutes", "15"); err != nil {
		t.Fatal(err)
	}
	// Seed a warehouse user (no PermAccessSettings) and log in.
	if _, err := e.st.DB.Exec(`INSERT INTO Users(UserName,Password,FullName,Role,IsActive,MustChangePassword,CreatedDate)
		VALUES(?,?,?,?,1,0,?)`, "wh", lockSHA("WhPass123"), "wh", "warehouse", store.Today()); err != nil {
		t.Fatalf("seed warehouse user: %v", err)
	}
	wh := e.login("wh", "WhPass123")

	// Warehouse POST with global_minutes=45 and an own override.
	code, _, body := e.postForm(wh, "/lock/settings", url.Values{
		"global_minutes": {"45"},
		"own_minutes":    {"30"},
	})
	if code != http.StatusOK {
		t.Fatalf("warehouse POST /lock/settings = %d, want 200", code)
	}
	// The global value must be unchanged...
	if got := e.st.GetAutoLockMinutes(); got != 15 {
		t.Fatalf("warehouse save changed the global lock minutes to %d, want 15 (unchanged)", got)
	}
	// ...and the notice must not claim the global settings were saved.
	if strings.Contains(body, "تم حفظ إعدادات القفل التلقائي") {
		t.Fatalf("warehouse save shows the false global notice %q", "تم حفظ إعدادات القفل التلقائي")
	}
	if !strings.Contains(body, "تم حفظ إعداد القفل التلقائي الخاص بك") {
		t.Fatalf("warehouse save notice does not say only the own setting was saved; body excerpt: %.200s", body)
	}
	// The own override WAS saved (the notice's claim is true).
	if got := strings.TrimSpace(e.st.GetSetting("auto_lock_minutes:wh")); got != "30" {
		t.Fatalf("warehouse own override = %q, want \"30\"", got)
	}

	// Positive direction: the same values as admin DO save the global
	// and may use the generic notice.
	code, _, body = e.postForm(e.admin, "/lock/settings", url.Values{
		"global_minutes": {"45"},
		"own_minutes":    {"30"},
	})
	if code != http.StatusOK {
		t.Fatalf("admin POST /lock/settings = %d, want 200", code)
	}
	if got := e.st.GetAutoLockMinutes(); got != 45 {
		t.Fatalf("admin save left global lock minutes at %d, want 45", got)
	}
	if !strings.Contains(body, "تم حفظ إعدادات القفل التلقائي") {
		t.Fatalf("admin save does not show the generic saved notice")
	}
}
