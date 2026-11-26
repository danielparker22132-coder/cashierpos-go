// Package web — auto-lock tests (feature 4, PyQt L-14 parity).
//
// The tests drive the real HTTP server (httptest) with a cookie-jar client
// against a fresh :memory: database. Idle time is simulated by backdating
// the in-memory last-activity stamp for the test session token — the
// timeout itself is never waited out.
package web

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"cashierpos-go/internal/store"
)

const (
	lockAdminPass   = "BossPass123"
	lockCashierPass = "CashPass123"
)

type lockEnv struct {
	t      *testing.T
	srv    *Server
	ts     *httptest.Server
	st     *store.Store
	base   string
	admin  *http.Client
	cash   *http.Client
	noFlow *http.Client // no-redirect client sharing the cashier jar
}

func lockSHA(pw string) string {
	h := sha256.Sum256([]byte(pw))
	return hex.EncodeToString(h[:])
}

func newLockEnv(t *testing.T) *lockEnv {
	t.Helper()
	st, err := store.OpenTest()
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv, err := New(st)
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	// Lock routes are wired by routes() (registerLockRoutes in routes.go).
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	e := &lockEnv{t: t, srv: srv, ts: ts, st: st, base: ts.URL}
	for _, u := range []struct{ name, pass, role string }{
		{"boss", lockAdminPass, "admin"},
		{"cashier", lockCashierPass, "cashier"},
	} {
		if _, err := st.DB.Exec(`INSERT INTO Users(UserName,Password,FullName,Role,IsActive,MustChangePassword,CreatedDate)
			VALUES(?,?,?,?,1,0,?)`, u.name, lockSHA(u.pass), u.name, u.role, store.Today()); err != nil {
			t.Fatalf("seed user %s: %v", u.name, err)
		}
	}
	e.admin = e.login("boss", lockAdminPass)
	e.cash = e.login("cashier", lockCashierPass)
	// noFlow shares the cashier jar but does not follow redirects.
	e.noFlow = &http.Client{
		Jar: e.cash.Jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return e
}

// login POSTs /login and returns a client holding the session cookie.
func (e *lockEnv) login(username, password string) *http.Client {
	e.t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	resp, err := c.PostForm(e.base+"/login", url.Values{"username": {username}, "password": {password}})
	if err != nil {
		e.t.Fatalf("login %s: %v", username, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusSeeOther {
		e.t.Fatalf("login %s: status %d", username, resp.StatusCode)
	}
	return c
}

// tokenOf returns the session cookie value held by a client.
func (e *lockEnv) tokenOf(c *http.Client) string {
	e.t.Helper()
	u, _ := url.Parse(e.base)
	for _, ck := range c.Jar.Cookies(u) {
		if ck.Name == "cashierpos_session" {
			return ck.Value
		}
	}
	e.t.Fatal("no session cookie")
	return ""
}

// backdate moves the cashier session's last activity d into the past.
func (e *lockEnv) backdate(d time.Duration) {
	e.t.Helper()
	token := e.tokenOf(e.cash)
	lockStateMu.Lock()
	lockLast[token] = time.Now().Add(-d)
	lockStateMu.Unlock()
}

// get returns status, Location header and body for a no-redirect GET
// with the cashier client.
func (e *lockEnv) get(path string) (int, string, string) {
	return e.getAs(e.noFlow, path)
}

// getAs is get with an explicit client.
func (e *lockEnv) getAs(c *http.Client, path string) (int, string, string) {
	e.t.Helper()
	resp, err := c.Get(e.base + path)
	if err != nil {
		e.t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Location"), string(b)
}

// clearLockMemory drops all in-memory idle state (simulates a server
// restart: the DB rows survive, the maps do not).
func (e *lockEnv) clearLockMemory() {
	e.t.Helper()
	lockStateMu.Lock()
	for token := range lockLast {
		delete(lockLast, token)
	}
	for token := range lockFails {
		delete(lockFails, token)
	}
	for token := range lockPersisted {
		delete(lockPersisted, token)
	}
	lockStateMu.Unlock()
}

// postForm returns status, Location header and body for a no-redirect POST.
func (e *lockEnv) postForm(c *http.Client, path string, v url.Values) (int, string, string) {
	e.t.Helper()
	nc := &http.Client{
		Jar: c.Jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := nc.PostForm(e.base+path, v)
	if err != nil {
		e.t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Location"), string(b)
}

func (e *lockEnv) sessionCount() int {
	var n int
	_ = e.st.DB.QueryRow(`SELECT COUNT(*) FROM Sessions`).Scan(&n)
	return n
}

// TestAutoLockAfterIdle: idle past the global timeout redirects a protected
// request to /lock?next=..., the lock page renders, and leaving the lock
// page without unlocking re-shows the lock on the next request.
func TestAutoLockAfterIdle(t *testing.T) {
	e := newLockEnv(t)
	if err := e.st.SetSetting("auto_lock_minutes", "1"); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := e.get("/"); code != http.StatusOK {
		t.Fatalf("fresh session GET / = %d, want 200", code)
	}
	e.backdate(2 * time.Minute)
	code, loc, _ := e.get("/")
	if code != http.StatusSeeOther || loc != "/lock?next=%2F" {
		t.Fatalf("idle GET / = %d loc %q, want 303 to /lock?next=%%2F", code, loc)
	}
	code, _, body := e.get("/lock?next=/sales")
	if code != http.StatusOK || !strings.Contains(body, "الشاشة مقفلة") {
		t.Fatalf("GET /lock = %d, body has lock title: %v", code, strings.Contains(body, "الشاشة مقفلة"))
	}
	if !strings.Contains(body, `value="/sales"`) {
		t.Fatalf("lock page does not preserve next=/sales")
	}
	// Leaving the lock page without unlocking: the next protected request
	// must show the lock again (the app is never left open).
	code, loc, _ = e.get("/")
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/lock?next=") {
		t.Fatalf("after leaving lock page, GET / = %d loc %q, want redirect to /lock", code, loc)
	}
}

// TestAutoLockUnlockReturnsToNext: the correct password resets the idle
// clock and redirects back to next; the session still works afterwards.
func TestAutoLockUnlockReturnsToNext(t *testing.T) {
	e := newLockEnv(t)
	if err := e.st.SetSetting("auto_lock_minutes", "1"); err != nil {
		t.Fatal(err)
	}
	e.backdate(2 * time.Minute)
	if code, _, _ := e.get("/"); code != http.StatusSeeOther {
		t.Fatalf("expected locked, GET / = %d", code)
	}
	code, loc, _ := e.postForm(e.cash, "/lock", url.Values{"password": {lockCashierPass}, "next": {"/"}})
	if code != http.StatusSeeOther || loc != "/" {
		t.Fatalf("unlock POST = %d loc %q, want 303 to /", code, loc)
	}
	if code, _, _ := e.get("/"); code != http.StatusOK {
		t.Fatalf("after unlock GET / = %d, want 200", code)
	}
	if e.sessionCount() != 2 {
		t.Fatalf("unlock must not log out: sessions = %d, want 2 (admin+cashier)", e.sessionCount())
	}
}

// TestAutoLockFiveWrongPasswordsForceLogout: 5 wrong passwords delete the
// session (forced logout); the 5th attempt redirects to /login.
func TestAutoLockFiveWrongPasswordsForceLogout(t *testing.T) {
	e := newLockEnv(t)
	if err := e.st.SetSetting("auto_lock_minutes", "1"); err != nil {
		t.Fatal(err)
	}
	e.backdate(2 * time.Minute)
	for i := 1; i <= 4; i++ {
		code, loc, body := e.postForm(e.cash, "/lock", url.Values{"password": {"wrong"}, "next": {"/"}})
		if code != http.StatusOK || !strings.Contains(body, "كلمة السر غير صحيحة") {
			t.Fatalf("wrong attempt %d: status %d loc %q, want 200 with error", i, code, loc)
		}
		if !strings.Contains(body, "محاولات متبقية") {
			t.Fatalf("wrong attempt %d: remaining-attempts text missing", i)
		}
	}
	code, loc, _ := e.postForm(e.cash, "/lock", url.Values{"password": {"wrong"}, "next": {"/"}})
	if code != http.StatusSeeOther || loc != "/login" {
		t.Fatalf("5th wrong attempt = %d loc %q, want 303 to /login", code, loc)
	}
	if e.sessionCount() != 1 {
		t.Fatalf("after 5 failures sessions = %d, want 1 (only admin left)", e.sessionCount())
	}
	code, loc, _ = e.get("/")
	if code != http.StatusSeeOther || loc != "/login" {
		t.Fatalf("after forced logout GET / = %d loc %q, want 303 to /login", code, loc)
	}
}

// TestAutoLockPerUserOverridesGlobal: a per-user 0 disables the lock for
// that user even when the global value is 1, and a per-user 1 locks the
// user even when the global value is 0.
func TestAutoLockPerUserOverridesGlobal(t *testing.T) {
	e := newLockEnv(t)
	if err := e.st.SetSetting("auto_lock_minutes", "1"); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetSetting("auto_lock_minutes:cashier", "0"); err != nil {
		t.Fatal(err)
	}
	e.backdate(2 * time.Hour)
	if code, _, _ := e.get("/"); code != http.StatusOK {
		t.Fatalf("per-user 0 with global 1: GET / = %d, want 200 (disabled for this user)", code)
	}

	if err := e.st.SetSetting("auto_lock_minutes", "0"); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetSetting("auto_lock_minutes:cashier", "1"); err != nil {
		t.Fatal(err)
	}
	e.backdate(2 * time.Minute)
	code, loc, _ := e.get("/")
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/lock?next=") {
		t.Fatalf("per-user 1 with global 0: GET / = %d loc %q, want redirect to /lock", code, loc)
	}
}

// TestAutoLockGlobalZeroDisables: global 0 disables the lock for everyone
// with no per-user override.
func TestAutoLockGlobalZeroDisables(t *testing.T) {
	e := newLockEnv(t)
	if err := e.st.SetSetting("auto_lock_minutes", "0"); err != nil {
		t.Fatal(err)
	}
	e.backdate(2 * time.Hour)
	if code, _, _ := e.get("/"); code != http.StatusOK {
		t.Fatalf("global 0: GET / = %d, want 200", code)
	}
}

// TestAutoLockAdminCanSetGlobal: an admin can set the global value from the
// lock settings page; a cashier's global field is ignored but their own
// per-user value is saved.
func TestAutoLockAdminCanSetGlobal(t *testing.T) {
	e := newLockEnv(t)
	code, _, body := e.postForm(e.admin, "/lock/settings", url.Values{"global_minutes": {"30"}, "own_minutes": {""}})
	if code != http.StatusOK || !strings.Contains(body, "تم حفظ") {
		t.Fatalf("admin save global: status %d, saved notice: %v", code, strings.Contains(body, "تم حفظ"))
	}
	if got := e.st.GetAutoLockMinutes(); got != 30 {
		t.Fatalf("global after admin save = %d, want 30", got)
	}

	code, _, body = e.postForm(e.cash, "/lock/settings", url.Values{"global_minutes": {"99"}, "own_minutes": {"5"}})
	if code != http.StatusOK || !strings.Contains(body, "تم حفظ") {
		t.Fatalf("cashier save own: status %d, saved notice: %v", code, strings.Contains(body, "تم حفظ"))
	}
	if got := e.st.GetAutoLockMinutes(); got != 30 {
		t.Fatalf("cashier must not change global: got %d, want 30", got)
	}
	if got := e.st.GetSetting("auto_lock_minutes:cashier"); got != "5" {
		t.Fatalf("cashier own minutes = %q, want \"5\"", got)
	}
}

// TestAutoLockAPI: a locked session calling the JSON API gets 423 Locked,
// not a redirect and not the protected data.
func TestAutoLockAPI(t *testing.T) {
	e := newLockEnv(t)
	if err := e.st.SetSetting("auto_lock_minutes", "1"); err != nil {
		t.Fatal(err)
	}
	e.backdate(2 * time.Minute)
	resp, err := e.noFlow.Get(e.base + "/api/notifications/unread-count")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 423 || !strings.Contains(string(b), `"locked":true`) {
		t.Fatalf("locked API call = %d body %s, want 423 with locked:true", resp.StatusCode, b)
	}
}

// TestAutoLockPageWhenActive: visiting /lock with an active (not idle)
// session redirects away instead of showing a lock screen.
func TestAutoLockPageWhenActive(t *testing.T) {
	e := newLockEnv(t)
	if err := e.st.SetSetting("auto_lock_minutes", "15"); err != nil {
		t.Fatal(err)
	}
	code, loc, _ := e.get("/lock?next=/")
	if code != http.StatusSeeOther || loc != "/" {
		t.Fatalf("active GET /lock = %d loc %q, want 303 to /", code, loc)
	}
}

// --- FIX-3 lane 2 (H3/M2/M3/L2/L3) ---

// TestAutoLockSurvivesRestart (M2): after the in-memory idle state is
// dropped (server restart), an already-idle session is still locked —
// the clock re-seeds from Sessions.LastActivity, never from now — while
// a fresh session is not locked.
func TestAutoLockSurvivesRestart(t *testing.T) {
	e := newLockEnv(t)
	if err := e.st.SetSetting("auto_lock_minutes", "1"); err != nil {
		t.Fatal(err)
	}
	token := e.tokenOf(e.cash)
	old := time.Now().Add(-2 * time.Minute).Format("2006-01-02 15:04:05")
	if _, err := e.st.DB.Exec(`UPDATE Sessions SET LastActivity=?, CreatedAt=? WHERE Token=?`,
		old, old, token); err != nil {
		t.Fatal(err)
	}
	e.clearLockMemory()
	code, loc, _ := e.get("/")
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/lock?next=") {
		t.Fatalf("after restart with 2-min-old activity, GET / = %d loc %q, want redirect to /lock", code, loc)
	}
	// A fresh session (created at login, seconds ago) is not locked by
	// the same restart.
	e.clearLockMemory()
	if code, _, _ := e.getAs(e.admin, "/"); code != http.StatusOK {
		t.Fatalf("after restart with fresh activity, admin GET / = %d, want 200", code)
	}
}

// TestAutoLockPersistsActivityThrottled (M2): an active request persists
// the session's last activity to Sessions.LastActivity, and a second
// immediate request does not rewrite it (60s throttle).
func TestAutoLockPersistsActivityThrottled(t *testing.T) {
	e := newLockEnv(t)
	if err := e.st.SetSetting("auto_lock_minutes", "60"); err != nil {
		t.Fatal(err)
	}
	token := e.tokenOf(e.cash)
	old := time.Now().Add(-2 * time.Minute).Format("2006-01-02 15:04:05")
	if _, err := e.st.DB.Exec(`UPDATE Sessions SET LastActivity=? WHERE Token=?`, old, token); err != nil {
		t.Fatal(err)
	}
	e.clearLockMemory() // force a DB seed, so the persisted stamp is old
	if code, _, _ := e.get("/"); code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", code)
	}
	var got string
	_ = e.st.DB.QueryRow(`SELECT LastActivity FROM Sessions WHERE Token=?`, token).Scan(&got)
	if got == old {
		t.Fatalf("LastActivity was not persisted: still %q", got)
	}
	// Throttle: move the DB stamp back, request again at once — the
	// in-memory persisted stamp is fresh, so the DB row must not move.
	if _, err := e.st.DB.Exec(`UPDATE Sessions SET LastActivity=? WHERE Token=?`, old, token); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := e.get("/"); code != http.StatusOK {
		t.Fatalf("second GET / = %d, want 200", code)
	}
	_ = e.st.DB.QueryRow(`SELECT LastActivity FROM Sessions WHERE Token=?`, token).Scan(&got)
	if got != old {
		t.Fatalf("throttled write rewrote LastActivity to %q, want it left at %q", got, old)
	}
}

// TestAutoLockNonAdminCannotDisableOwnLock (M3): a cashier setting
// own_minutes=0 is rejected with an error and nothing is stored; a
// non-zero own value is still allowed, and an admin may set 0 for
// themselves.
func TestAutoLockNonAdminCannotDisableOwnLock(t *testing.T) {
	e := newLockEnv(t)
	code, _, body := e.postForm(e.cash, "/lock/settings", url.Values{"own_minutes": {"0"}})
	if code != http.StatusOK || strings.Contains(body, "تم حفظ") {
		t.Fatalf("cashier own 0: status %d, saved notice: %v", code, strings.Contains(body, "تم حفظ"))
	}
	if !strings.Contains(body, "غير مسموح") {
		t.Fatalf("cashier own 0: want a refusal error, body lacks it")
	}
	if got := e.st.GetSetting("auto_lock_minutes:cashier"); got == "0" {
		t.Fatalf("cashier own 0 was stored: %q", got)
	}

	code, _, body = e.postForm(e.cash, "/lock/settings", url.Values{"own_minutes": {"5"}})
	if code != http.StatusOK || !strings.Contains(body, "تم حفظ") {
		t.Fatalf("cashier own 5: status %d, saved notice: %v", code, strings.Contains(body, "تم حفظ"))
	}
	if got := e.st.GetSetting("auto_lock_minutes:cashier"); got != "5" {
		t.Fatalf("cashier own minutes = %q, want \"5\"", got)
	}

	code, _, body = e.postForm(e.admin, "/lock/settings", url.Values{"own_minutes": {"0"}})
	if code != http.StatusOK || !strings.Contains(body, "تم حفظ") {
		t.Fatalf("admin own 0: status %d, saved notice: %v", code, strings.Contains(body, "تم حفظ"))
	}
	if got := e.st.GetSetting("auto_lock_minutes:boss"); got != "0" {
		t.Fatalf("admin own minutes = %q, want \"0\"", got)
	}
}

// TestAutoLockOverrideListAndClear (M3): the settings page lists every
// per-user override to a manager, with a clear action per override; a
// cashier sees no list and cannot clear someone else's override.
func TestAutoLockOverrideListAndClear(t *testing.T) {
	e := newLockEnv(t)
	if err := e.st.SetSetting("auto_lock_minutes:cashier", "30"); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetSetting("auto_lock_minutes:boss", "0"); err != nil {
		t.Fatal(err)
	}
	_, _, body := e.getAs(e.admin, "/lock/settings")
	if !strings.Contains(body, `name="clear_override" value="cashier"`) {
		t.Fatalf("admin settings page lacks a clear action for the cashier override")
	}
	if !strings.Contains(body, `name="clear_override" value="boss"`) {
		t.Fatalf("admin settings page lacks a clear action for the boss override")
	}

	code, _, body := e.postForm(e.admin, "/lock/settings", url.Values{"clear_override": {"cashier"}})
	if code != http.StatusOK || !strings.Contains(body, "تم مسح") {
		t.Fatalf("admin clear cashier: status %d, cleared notice: %v", code, strings.Contains(body, "تم مسح"))
	}
	if got := e.st.GetSetting("auto_lock_minutes:cashier"); got != "" {
		t.Fatalf("cashier override after clear = %q, want empty", got)
	}
	if got := e.st.GetSetting("auto_lock_minutes:boss"); got != "0" {
		t.Fatalf("clearing one override touched another: boss = %q, want \"0\"", got)
	}

	_, _, body = e.getAs(e.cash, "/lock/settings")
	if strings.Contains(body, "clear_override") {
		t.Fatalf("cashier settings page exposes the override list/clear action")
	}
	_, _, _ = e.postForm(e.cash, "/lock/settings", url.Values{"clear_override": {"boss"}})
	if got := e.st.GetSetting("auto_lock_minutes:boss"); got != "0" {
		t.Fatalf("cashier cleared the boss override: %q", got)
	}
}

// TestAutoLockInvalidGlobalRejected (L2): a non-numeric or out-of-range
// global_minutes is a validation error, not a silent skip with a false
// "saved" notice; the stored global value is unchanged.
func TestAutoLockInvalidGlobalRejected(t *testing.T) {
	e := newLockEnv(t)
	if err := e.st.SetSetting("auto_lock_minutes", "15"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"abc", "601", "-1"} {
		code, _, body := e.postForm(e.admin, "/lock/settings", url.Values{"global_minutes": {bad}, "own_minutes": {""}})
		if code != http.StatusOK || strings.Contains(body, "تم حفظ") {
			t.Fatalf("global %q: status %d, saved notice shown", bad, code)
		}
		if got := e.st.GetAutoLockMinutes(); got != 15 {
			t.Fatalf("global %q changed the stored global to %d, want 15", bad, got)
		}
	}
	// An invalid own value is likewise rejected, not silently ignored.
	code, _, body := e.postForm(e.cash, "/lock/settings", url.Values{"own_minutes": {"abc"}})
	if code != http.StatusOK || strings.Contains(body, "تم حفظ") {
		t.Fatalf("own abc: status %d, saved notice shown", code)
	}
	if got := e.st.GetSetting("auto_lock_minutes:cashier"); got == "abc" {
		t.Fatalf("own abc was stored: %q", got)
	}
}

// TestChangePasswordLockedWhenIdle (L3): /change-password is reachable
// when the session is active, but an idle-locked session is sent to
// /lock first — GET and POST alike.
func TestChangePasswordLockedWhenIdle(t *testing.T) {
	e := newLockEnv(t)
	if err := e.st.SetSetting("auto_lock_minutes", "1"); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := e.get("/change-password"); code != http.StatusOK {
		t.Fatalf("active GET /change-password = %d, want 200", code)
	}
	e.backdate(2 * time.Minute)
	code, loc, _ := e.get("/change-password")
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/lock?next=") {
		t.Fatalf("idle GET /change-password = %d loc %q, want redirect to /lock", code, loc)
	}
	code, loc, _ = e.postForm(e.cash, "/change-password", url.Values{
		"current_password": {lockCashierPass}, "new_password": {"NewPass123"}, "confirm_password": {"NewPass123"},
	})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/lock?next=") {
		t.Fatalf("idle POST /change-password = %d loc %q, want redirect to /lock", code, loc)
	}
}

// TestAutoLockCartSurvivesLockRoundTrip (H3): the sales page persists the
// cart in sessionStorage (restored on load, cleared only after a
// successful save), and a lock/unlock round trip returns to /sales in
// the same tab — where that stored cart is restored.
func TestAutoLockCartSurvivesLockRoundTrip(t *testing.T) {
	e := newLockEnv(t)
	if err := e.st.SetSetting("auto_lock_minutes", "1"); err != nil {
		t.Fatal(err)
	}
	_, _, body := e.get("/sales")
	for _, marker := range []string{
		"sessionStorage",
		"cashierpos_pos_cart",
		"cashierpos_pos_cart_submitted",
		"restoreCart",
		"clearSavedCart",
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("sales page missing cart-persistence marker %q", marker)
		}
	}
	e.backdate(2 * time.Minute)
	code, loc, _ := e.get("/sales")
	if code != http.StatusSeeOther || loc != "/lock?next=%2Fsales" {
		t.Fatalf("idle GET /sales = %d loc %q, want 303 to /lock?next=%%2Fsales", code, loc)
	}
	code, loc, _ = e.postForm(e.cash, "/lock", url.Values{"password": {lockCashierPass}, "next": {"/sales"}})
	if code != http.StatusSeeOther || loc != "/sales" {
		t.Fatalf("unlock POST = %d loc %q, want 303 to /sales", code, loc)
	}
	if code, _, _ := e.get("/sales"); code != http.StatusOK {
		t.Fatalf("after unlock GET /sales = %d, want 200", code)
	}
}
