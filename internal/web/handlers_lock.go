// Package web — auto-lock on idle (feature 4, PyQt L-14 parity).
//
// Rules (from the PyQt reference, app/main.py check_lock/_do_lock):
//   - Setting "auto_lock_minutes" (global, default 15, 0 = disabled) and a
//     per-user override "auto_lock_minutes:<username>". Precedence
//     (FIX-17 SW2-L07, FEATURE_MAP L-14): a global value > 0 wins for
//     everyone; global 0 -> the user's own value; per-user NULL -> 15;
//     per-user 0 -> never lock. Both are read through the store
//     settings helpers.
//   - Middleware tracks the last activity of every session; an authenticated
//     request whose idle time reached the effective timeout is redirected
//     to /lock?next=<original URL>. Locking is NOT a logout: the session,
//     the shift and the cart stay exactly as they are. Last activity is
//     persisted per session in Sessions.LastActivity (throttled to at most
//     one write per 60s), so a restart does not reset any idle clock; an
//     unseen token seeds from that row (LastActivity else CreatedAt),
//     never from now. The in-memory maps are pruned (entries older than
//     24h, once the map exceeds 1000 entries).
//   - The lock page asks for the password. The correct password resets the
//     activity clock and redirects back to next. 5 wrong passwords delete
//     the session (forced logout). Leaving the lock page without unlocking
//     re-shows the lock on the next protected request — the app is never
//     left open.
//   - Works in every mode (the Go build has no real sync; the lock applies
//     regardless of mode and claims no sync).
//
// Routes are registered by registerLockRoutes; routes.go (coordinator)
// calls it from Server.routes().
package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/rbac"
	"cashierpos-go/internal/store"
)

// lockMaxAttempts is the number of wrong passwords on the lock page that
// forces a logout (deletes the session).
const lockMaxAttempts = 5

// lockStateMu guards lockLast/lockFails/lockPersisted.
var lockStateMu sync.Mutex

// lockLast maps a session token to its last observed activity.
// lockFails maps a session token to consecutive wrong lock passwords.
// lockPersisted maps a session token to when its last activity was last
// written to Sessions.LastActivity (throttle for the M2 DB writes).
var (
	lockLast      = make(map[string]time.Time)
	lockFails     = make(map[string]int)
	lockPersisted = make(map[string]time.Time)
)

// pruneLockStateLocked drops idle-clock entries older than 24h. The caller
// holds lockStateMu. Only runs once the map exceeds 1000 entries, so the
// common case pays no pruning cost; a pruned token re-seeds from the DB.
func pruneLockStateLocked(now time.Time) {
	if len(lockLast) <= 1000 {
		return
	}
	cutoff := now.Add(-24 * time.Hour)
	for token, last := range lockLast {
		if last.Before(cutoff) {
			delete(lockLast, token)
			delete(lockPersisted, token)
			delete(lockFails, token)
		}
	}
}

// lockLastSeen returns the last activity for a token, seeding an unseen
// token from the Sessions row (LastActivity else CreatedAt) — never from
// now, so a restart does not grant a fresh full timeout to a session
// that was already idle. ok=false only when the token has no session row.
func (s *Server) lockLastSeen(token string) (time.Time, bool) {
	lockStateMu.Lock()
	last, seen := lockLast[token]
	lockStateMu.Unlock()
	if seen {
		return last, true
	}
	last, ok := s.Store.SessionLastActivity(token)
	if !ok {
		return time.Time{}, false
	}
	lockStateMu.Lock()
	if prev, ok2 := lockLast[token]; ok2 {
		last = prev
	} else {
		lockLast[token] = last
		if _, pok := lockPersisted[token]; !pok {
			lockPersisted[token] = last
		}
		pruneLockStateLocked(time.Now())
	}
	lockStateMu.Unlock()
	return last, true
}

// perUserLockKey is the AppSettings key holding one user's override.
func perUserLockKey(username string) string { return "auto_lock_minutes:" + username }

// effectiveAutoLockMinutes returns the timeout in minutes for this user
// (FIX-17 SW2-L07, FEATURE_MAP L-14): a global value > 0 wins for
// everyone; global 0 -> the user's own value (0-600, 0 = never lock);
// a missing or invalid per-user value -> the 15-minute default.
func (s *Server) effectiveAutoLockMinutes(u *auth.User) int {
	if g := s.Store.GetAutoLockMinutes(); g > 0 {
		return g
	}
	if u != nil {
		if raw := strings.TrimSpace(s.Store.GetSetting(perUserLockKey(u.UserName))); raw != "" {
			if v, err := strconv.Atoi(raw); err == nil && v >= 0 && v <= 600 {
				return v
			}
		}
	}
	return 15
}

// lockExempt reports whether a path bypasses the idle check: the lock page
// itself (a locked user must be able to reach it), login/logout, health
// and static assets. /change-password is NOT exempt (FIX-3 L3): a locked
// session must unlock before changing the password.
func lockExempt(path string) bool {
	switch path {
	case "/lock", "/login", "/logout", "/health":
		return true
	}
	return strings.HasPrefix(path, "/static/")
}

// lockNoRefresh reports whether an authenticated request must NOT
// refresh the idle clock: pure background polls. The web bell polls
// GET /api/staff/orders/pending-count and /pending-evidence every 5 s
// on every logged-in page (layout.html); if those hits counted as
// activity, the timeout could never elapse while a page is open and
// the auto-lock would be dead in real browser use (FIX-11). A poll
// still goes through the expiry check in autoLock, so a locked session
// gets 423 from it — the poll never resurrects an expired session.
// Everything else that reaches the middleware is real activity: the
// /api/activity heartbeat fires only on mouse/key events, and the
// sales preview / barcode lookups are user-initiated.
// FIX-12 L2: the path is normalised (trailing slashes stripped,
// case-folded) before matching — autoLock refreshes before the mux
// routes, so a trailing-slash or upper-case variant of a bell path
// used to refresh the clock even though the mux then 404s it. The
// method is deliberately NOT checked: a POST to a bell path is not a
// real route either (this tree answers 404 for it) and must not
// refresh the clock.
// FIX-13 I1: TrimSuffix stripped exactly ONE trailing slash, so a
// double-slash variant (…/pending-count//) still refreshed the
// clock. TrimRight strips every trailing slash, so any number of
// them normalises to the exact bell path.
func lockNoRefresh(path string) bool {
	p := strings.ToLower(strings.TrimRight(path, "/"))
	return p == "/api/staff/orders/pending-count" ||
		p == "/api/staff/orders/pending-evidence"
}

// sessionToken returns the session cookie value, or "".
func sessionToken(r *http.Request) string {
	if c, err := r.Cookie("cashierpos_session"); err == nil {
		return c.Value
	}
	return ""
}

// autoLock is the idle middleware. It runs after withSession (so
// CurrentUser is set) and before the mux. Every authenticated request
// refreshes the session's last-activity stamp — except when the session
// is already idle-expired, in which case the request is sent to /lock
// with ?next= preserved, and except pure background polls (lockNoRefresh:
// the web-bell 5 s poll), which go through the expiry check but never
// refresh the stamp. Anonymous requests pass through (RequireLogin
// handles them).
func (s *Server) autoLock(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := CurrentUser(r)
		if u == nil || lockExempt(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		token := sessionToken(r)
		if token == "" {
			next.ServeHTTP(w, r)
			return
		}
		minutes := s.effectiveAutoLockMinutes(u)
		now := time.Now()
		last, ok := s.lockLastSeen(token)
		if !ok {
			// No session row (should not happen for an authenticated
			// request): the clock starts now.
			last = now
			lockStateMu.Lock()
			lockLast[token] = last
			lockPersisted[token] = last
			lockStateMu.Unlock()
		}
		if minutes > 0 && now.Sub(last) >= time.Duration(minutes)*time.Minute {
			// Idle-expired: locked. Do NOT refresh the stamp, so leaving
			// the lock page re-shows the lock on the next request.
			// FIX-17 SW2-L06: case-fold the /api/ prefix check, like
			// lockNoRefresh does — a locked upper-case bell path
			// (/API/STAFF/...) is an API call (423 JSON), not a page.
			if strings.HasPrefix(strings.ToLower(r.URL.Path), "/api/") {
				writeJSON(w, 423, map[string]any{"ok": false, "error": "الجلسة مقفلة بسبب الخمول — أدخل كلمة السر لفتحها", "locked": true})
				return
			}
			http.Redirect(w, r, "/lock?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		if lockNoRefresh(r.URL.Path) {
			// Pure background poll (FIX-11): answer without touching
			// the idle clock. The expiry check above already ran, so
			// a locked session got 423 and never reaches here.
			next.ServeHTTP(w, r)
			return
		}
		// Active request: refresh the in-memory stamp, and persist it to
		// Sessions.LastActivity at most once per 60s (M2 throttle).
		lockStateMu.Lock()
		lockLast[token] = now
		persisted, hasPersisted := lockPersisted[token]
		needPersist := !hasPersisted || now.Sub(persisted) >= time.Minute
		if needPersist {
			lockPersisted[token] = now
		}
		lockStateMu.Unlock()
		if needPersist {
			s.Store.TouchSessionActivity(token, now)
		}
		next.ServeHTTP(w, r)
	})
}

// lockNoteActivity resets the idle clock for a session (after unlock),
// in memory and in the Sessions row.
func (s *Server) lockNoteActivity(token string) {
	if token == "" {
		return
	}
	now := time.Now()
	lockStateMu.Lock()
	lockLast[token] = now
	lockPersisted[token] = now
	delete(lockFails, token)
	lockStateMu.Unlock()
	s.Store.TouchSessionActivity(token, now)
}

// lockAttemptsLeft returns how many wrong passwords remain for a token.
func lockAttemptsLeft(token string) int {
	lockStateMu.Lock()
	defer lockStateMu.Unlock()
	return lockMaxAttempts - lockFails[token]
}

// isLocked reports whether the session behind r is currently idle-expired
// (used by the lock page itself, which is exempt from the middleware).
func (s *Server) isLocked(r *http.Request) bool {
	u := CurrentUser(r)
	if u == nil {
		return false
	}
	token := sessionToken(r)
	if token == "" {
		return false
	}
	minutes := s.effectiveAutoLockMinutes(u)
	if minutes <= 0 {
		return false
	}
	last, ok := s.lockLastSeen(token)
	if !ok {
		return false
	}
	return time.Since(last) >= time.Duration(minutes)*time.Minute
}

// safeNext validates the ?next= return URL: only a same-site path is
// accepted, everything else falls back to def ("/").
func safeNext(r *http.Request, def string) string {
	next := strings.TrimSpace(r.URL.Query().Get("next"))
	if next == "" {
		// POSTs carry next as a hidden form field.
		next = strings.TrimSpace(r.FormValue("next"))
	}
	if def == "" {
		def = "/"
	}
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return def
	}
	return next
}

// registerLockRoutes registers the auto-lock routes on m. Called from
// routes.go (coordinator wiring).
func (s *Server) registerLockRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /lock", s.RequireLogin(s.handleLockPage))
	m.HandleFunc("POST /lock", s.RequireLogin(s.handleLockUnlock))
	m.HandleFunc("GET /lock/settings", s.RequireLogin(s.handleLockSettings))
	m.HandleFunc("POST /lock/settings", s.RequireLogin(s.handleLockSettingsSave))
	// H-10: client activity heartbeat. No work is needed here beyond
	// answering: the autoLock middleware (server.go Handler chain)
	// refreshes the session's idle clock on every authenticated
	// request, so each heartbeat ping is itself the refresh. A locked
	// session gets 423 from the middleware and never reaches this
	// handler, so a ping can never resurrect an expired session.
	m.HandleFunc("GET /api/activity", s.RequireLogin(s.handleActivityHeartbeat))
}

// handleActivityHeartbeat answers the H-10 heartbeat ping.
func (s *Server) handleActivityHeartbeat(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- Lock page ---

type lockPageData struct {
	Next       string
	Attempts   int // wrong attempts left
	LockedOut  bool
	Username   string
	HasAttempt bool
}

func (s *Server) handleLockPage(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	next := safeNext(r, "/")
	// Visiting /lock while the session is NOT idle-expired means there is
	// nothing to unlock (or locking is disabled): go back where we came
	// from instead of showing a lock screen for an active session.
	if !s.isLocked(r) {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	_ = s.render(w, r, "lock.html", Page{Title: "الشاشة مقفلة", Data: lockPageData{
		Next: next, Attempts: lockAttemptsLeft(sessionToken(r)), Username: u.UserName,
	}})
}

func (s *Server) handleLockUnlock(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	token := sessionToken(r)
	next := safeNext(r, "/")
	password := r.FormValue("password")

	var stored string
	_ = s.Store.DB.QueryRow(`SELECT Password FROM Users WHERE UserID=?`, u.ID).Scan(&stored)
	ok, _ := auth.VerifyPassword(stored, password)
	if ok {
		// Correct password: reset the idle clock and go back to next.
		// The session, shift and cart are untouched — locking is not a logout.
		s.lockNoteActivity(token)
		_ = s.Store.Audit(u.ID, u.UserName, "unlock", "lock", nil, 0, 0, "", "فتح القفل بعد خمول")
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}

	// Wrong password: count it. At 5 failures the session is deleted
	// (forced logout) — the lock must never be brute-forced open.
	lockStateMu.Lock()
	lockFails[token]++
	fails := lockFails[token]
	lockStateMu.Unlock()
	left := lockMaxAttempts - fails
	if fails >= lockMaxAttempts {
		auth.DeleteSession(s.Store.DB, token)
		lockStateMu.Lock()
		delete(lockLast, token)
		delete(lockFails, token)
		delete(lockPersisted, token)
		lockStateMu.Unlock()
		_ = s.Store.Audit(u.ID, u.UserName, "lockout", "lock", nil, 0, 0, "",
			"خروج إجباري بعد 5 محاولات فاشلة لفتح القفل")
		http.SetCookie(w, &http.Cookie{Name: "cashierpos_session", Value: "", Path: "/", MaxAge: -1})
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	_ = s.render(w, r, "lock.html", Page{Title: "الشاشة مقفلة", Error: fmt.Sprintf("كلمة السر غير صحيحة — محاولات متبقية: %d", left), Data: lockPageData{
		Next: next, Attempts: left, Username: u.UserName, HasAttempt: true,
	}})
}

// --- Per-user settings page ---

// lockSettingsData is the data for autolock_settings.html.
type lockSettingsData struct {
	Global     int
	OwnMinutes string // "" = inherit the global value
	HasOwn     bool
	CanGlobal  bool // admin/manager: may change the global value
	// Overrides lists every user's per-user override (manager-visible
	// only, FIX-3 M3); nil for users who may not see it.
	Overrides []store.AutoLockOverride
}

// lockSettingsDataFor builds the settings-page data for u, fresh from the
// store (including the manager-visible override list).
func (s *Server) lockSettingsDataFor(u *auth.User) lockSettingsData {
	raw := strings.TrimSpace(s.Store.GetSetting(perUserLockKey(u.UserName)))
	d := lockSettingsData{
		Global: s.Store.GetAutoLockMinutes(), OwnMinutes: raw, HasOwn: raw != "",
		CanGlobal: rbac.Can(u.Role, rbac.PermAccessSettings),
	}
	if d.CanGlobal {
		d.Overrides = s.Store.ListAutoLockOverrides()
	}
	return d
}

func (s *Server) handleLockSettings(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	_ = s.render(w, r, "autolock_settings.html", Page{Title: "إعدادات القفل التلقائي", Data: s.lockSettingsDataFor(u)})
}

func (s *Server) handleLockSettingsSave(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	canGlobal := rbac.Can(u.Role, rbac.PermAccessSettings)
	// Manager clear action: clear one user's override (manager only).
	if clearUser := strings.TrimSpace(r.FormValue("clear_override")); clearUser != "" {
		data := s.lockSettingsDataFor(u)
		if !canGlobal {
			_ = s.render(w, r, "autolock_settings.html", Page{Title: "إعدادات القفل التلقائي", Error: "مسح مدة مستخدم آخر للمدير فقط", Data: data})
			return
		}
		if err := s.Store.SetSetting(perUserLockKey(clearUser), ""); err != nil {
			_ = s.render(w, r, "autolock_settings.html", Page{Title: "إعدادات القفل التلقائي", Error: safeHTMLErr(err), Data: data})
			return
		}
		_ = s.Store.Audit(u.ID, u.UserName, "clear_auto_lock_override", "settings", nil, 0, 0, "",
			"مسح المدة الخاصة بالقفل التلقائي للمستخدم "+clearUser)
		_ = s.render(w, r, "autolock_settings.html", Page{Title: "إعدادات القفل التلقائي", Notice: "تم مسح المدة الخاصة بالمستخدم " + clearUser, Data: s.lockSettingsDataFor(u)})
		return
	}
	// Validate BOTH values before saving either, so an invalid value
	// never saves half the form nor shows a false "saved" notice (L2).
	ownRaw := strings.TrimSpace(r.FormValue("own_minutes"))
	var ownVal *int // nil = inherit the global value
	if ownRaw != "" {
		v, err := strconv.Atoi(ownRaw)
		if err != nil || v < 0 || v > 600 {
			data := s.lockSettingsDataFor(u)
			data.OwnMinutes = ownRaw
			_ = s.render(w, r, "autolock_settings.html", Page{Title: "إعدادات القفل التلقائي", Error: "مدة القفل الخاصة بك بين 0 (معطل) و 600 دقيقة", Data: data})
			return
		}
		// FIX-3 M3: a non-admin may not disable their own idle lock.
		// (Admins/managers may set 0 for themselves; the store override
		// semantics are unchanged — direct setting still works.)
		if v == 0 && !canGlobal {
			data := s.lockSettingsDataFor(u)
			data.OwnMinutes = ownRaw
			_ = s.render(w, r, "autolock_settings.html", Page{Title: "إعدادات القفل التلقائي", Error: "تعطيل القفل التلقائي لنفسك غير مسموح — اطلب من المدير", Data: data})
			return
		}
		ownVal = &v
	}
	// Global value: admin/manager only (SaveAutoLockMinutes enforces
	// PermAccessSettings in the service layer as well). A non-numeric or
	// out-of-range value is a validation error, not a silent skip (L2).
	var globalVal *int
	if canGlobal {
		if gRaw := strings.TrimSpace(r.FormValue("global_minutes")); gRaw != "" {
			gv, err := strconv.Atoi(gRaw)
			if err != nil || gv < 0 || gv > 600 {
				data := s.lockSettingsDataFor(u)
				data.OwnMinutes = ownRaw
				_ = s.render(w, r, "autolock_settings.html", Page{Title: "إعدادات القفل التلقائي", Error: "المدة العامة بين 0 (بلا مدة عامة) و 600 دقيقة", Data: data})
				return
			}
			globalVal = &gv
		}
	}
	// Per-user value: empty = no override (the reader falls back to the
	// 15-minute default when the global value is 0); otherwise 0-600
	// minutes (0 = never lock for me, only when the global value is 0 —
	// FIX-17 SW2-L07, global > 0 wins for everyone).
	if ownVal == nil {
		if err := s.Store.SetSetting(perUserLockKey(u.UserName), ""); err != nil {
			data := s.lockSettingsDataFor(u)
			data.OwnMinutes = ownRaw
			_ = s.render(w, r, "autolock_settings.html", Page{Title: "إعدادات القفل التلقائي", Error: safeHTMLErr(err), Data: data})
			return
		}
	} else {
		if err := s.Store.SetSetting(perUserLockKey(u.UserName), strconv.Itoa(*ownVal)); err != nil {
			data := s.lockSettingsDataFor(u)
			data.OwnMinutes = ownRaw
			_ = s.render(w, r, "autolock_settings.html", Page{Title: "إعدادات القفل التلقائي", Error: safeHTMLErr(err), Data: data})
			return
		}
	}
	if globalVal != nil {
		if err := s.Store.SaveAutoLockMinutes(r.Context(), u, *globalVal); err != nil {
			data := s.lockSettingsDataFor(u)
			data.OwnMinutes = ownRaw
			_ = s.render(w, r, "autolock_settings.html", Page{Title: "إعدادات القفل التلقائي", Error: safeHTMLErr(err), Data: data})
			return
		}
	}
	_ = s.Store.Audit(u.ID, u.UserName, "save_auto_lock_self", "settings", nil, 0, 0, "",
		"حفظ إعداد القفل التلقائي للمستخدم "+u.UserName)
	// FIX-12 SW-L09: the notice must only claim what was actually
	// saved. A caller without PermAccessSettings cannot change the
	// global value (globalVal stays nil above), so the generic
	// "تم حفظ إعدادات القفل التلقائي" would falsely claim the global
	// value was saved for them — they only saved their own override.
	notice := "تم حفظ إعدادات القفل التلقائي"
	if !canGlobal {
		notice = "تم حفظ إعداد القفل التلقائي الخاص بك"
	}
	_ = s.render(w, r, "autolock_settings.html", Page{Title: "إعدادات القفل التلقائي", Notice: notice, Data: s.lockSettingsDataFor(u)})
}
