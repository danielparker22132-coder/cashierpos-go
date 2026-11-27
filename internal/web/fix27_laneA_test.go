// Package web_test — FIX-27 lane A (R27-C-F1 / R27-E-F2): autolock
// settings save must never echo a raw driver/DB error into
// autolock_settings.html. The four handleLockSettingsSave failure legs
// (handlers_lock.go :439 clear-override, :494 own-empty, :501
// own-value, :509 global via SaveAutoLockMinutes) rendered err.Error()
// verbatim at 200 — live-proven pre-fix as
// "SQL logic error: no such table: AppSettings (1)" (×3 legs) and
// "SQL logic error: no such table: AuditLog (1)" (global leg). All four
// now go through safeHTMLErr: a driver error becomes the single generic
// Arabic message, the 200 status is unchanged, and the healthy save
// flows keep their byte-identical success pages.
package web_test

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestFix27A_LockSettingsDropTableMatrix covers all four live-proven
// leak legs: each case drops the table its save leg depends on, posts
// the settings form, and the page must answer 200 with the generic
// Arabic message — never the raw driver string the pre-fix page echoed.
func TestFix27A_LockSettingsDropTableMatrix(t *testing.T) {
	cases := []struct {
		name  string
		table string
		form  url.Values
	}{
		{"L439-clear-override", "AppSettings", url.Values{"clear_override": {"cashier"}}},
		{"L494-own-empty", "AppSettings", url.Values{"own_minutes": {""}}},
		{"L501-own-value", "AppSettings", url.Values{"own_minutes": {"30"}}},
		{"L509-global", "AuditLog", url.Values{"own_minutes": {"30"}, "global_minutes": {"20"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := fix25AEnv(t)
			mgr := e.managerClient()
			fix26ADrop(t, e, tc.table)
			code, body := e.postForm(mgr, "/lock/settings", tc.form)
			fix26AAssertHTMLNoRaw(t, tc.name+" after DROP "+tc.table, code, body)
			if !strings.Contains(body, "حدث خطأ داخلي، حاول مرة أخرى") {
				t.Fatalf("%s: body lacks the generic message: %.300q", tc.name, body)
			}
		})
	}
}

// TestFix27A_LockSettingsNormalSave pins the healthy flows the gate
// must NOT change: clear-override, own-minutes set, own-minutes clear
// and global set all still succeed with their exact notices, and the
// saved values are visible on a fresh GET of the settings page.
func TestFix27A_LockSettingsNormalSave(t *testing.T) {
	e, _ := fix25AEnv(t)
	mgr := e.managerClient()

	code, body := e.postForm(mgr, "/lock/settings", url.Values{"own_minutes": {"30"}})
	if code != http.StatusOK || !strings.Contains(body, "تم حفظ إعدادات القفل التلقائي") {
		t.Fatalf("own set: status = %d, want 200 + saved notice (body %.300q)", code, body)
	}
	fix25AAssertNoRaw(t, "own set", body)
	code, body = e.get(mgr, "/lock/settings")
	if code != http.StatusOK || !strings.Contains(body, `value="30"`) {
		t.Fatalf("settings GET after own set: status = %d, want value=\"30\" (body %.300q)", code, body)
	}

	code, body = e.postForm(mgr, "/lock/settings", url.Values{"own_minutes": {""}})
	if code != http.StatusOK || !strings.Contains(body, "تم حفظ إعدادات القفل التلقائي") {
		t.Fatalf("own clear: status = %d, want 200 + saved notice (body %.300q)", code, body)
	}
	fix25AAssertNoRaw(t, "own clear", body)

	code, body = e.postForm(mgr, "/lock/settings", url.Values{"own_minutes": {"30"}, "global_minutes": {"20"}})
	if code != http.StatusOK || !strings.Contains(body, "تم حفظ إعدادات القفل التلقائي") {
		t.Fatalf("global set: status = %d, want 200 + saved notice (body %.300q)", code, body)
	}
	fix25AAssertNoRaw(t, "global set", body)

	code, body = e.postForm(mgr, "/lock/settings", url.Values{"clear_override": {"cashier"}})
	if code != http.StatusOK || !strings.Contains(body, "تم مسح المدة الخاصة بالمستخدم cashier") {
		t.Fatalf("clear override: status = %d, want 200 + cleared notice (body %.300q)", code, body)
	}
	fix25AAssertNoRaw(t, "clear override", body)
}

// TestFix27A_LockNoRawErrorRender is the code-marker guard for this
// round: handlers_lock.go must contain zero raw Page{Error:
// err.Error()} renders, and exactly the four gated sites
// (safeHTMLErr(err)) — one per save leg. The other Error renders in
// the file are static Arabic validation/denial text or an int-only
// format, never a driver string.
func TestFix27A_LockNoRawErrorRender(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "handlers_lock.go"))
	if err != nil {
		t.Fatalf("read handlers_lock.go: %v", err)
	}
	if strings.Contains(string(src), "Error: err.Error()") {
		t.Fatal("handlers_lock.go still contains a raw Page{Error: err.Error()} render")
	}
	if n := strings.Count(string(src), "Error: safeHTMLErr(err)"); n != 4 {
		t.Fatalf("handlers_lock.go has %d gated Error: safeHTMLErr(err) renders, want exactly 4", n)
	}
}
