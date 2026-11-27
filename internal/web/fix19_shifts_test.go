// Package web_test — FIX-19 lane D regression tests.
//
// R19-C1: POST /api/cashbox/count on a missing shift must be 404 (was 400),
// like the sibling cashbox reads (counts list, shift summary) for the
// identical "الشيفت غير موجود" string; existing shift still 200 and the
// cross-user denial still 403. Local check only — apiErrForService is
// untouched (a global branch would flip 6+ endpoints incl. O-C4).
// R19-D-F2: the GLOBAL auto-lock value must never be labelled "معطل" —
// global 0 means "no global value, per-user applies", not "disabled".
// All four global spots now read "بلا مدة عامة"; the per-user 0 =
// never-lock spots (own error, override table) keep "(معطل)".
package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestFix19_CashCountCreateMissingShiftIs404(t *testing.T) {
	e := readyEnv(t, false)
	mgr := e.managerClient()
	cash := e.cashierClient()
	cash2 := e.secondCashierClient()
	cashShift := e.openShift(cash, 10000)

	// Nonexistent shift: 404 (was 400), same message as the sibling
	// counts-list and shift-summary endpoints for the same shift.
	for _, c := range []*http.Client{cash, mgr} {
		code, body := e.postJSON(c, "/api/cashbox/count", map[string]any{
			"shift_id": 999999, "amount_cents": 10000,
		})
		if code != http.StatusNotFound || body["ok"] != false {
			t.Fatalf("count for missing shift: want 404 ok=false, got %d body=%v", code, body)
		}
		if msg, _ := body["error"].(string); !strings.Contains(msg, "الشيفت غير موجود") {
			t.Fatalf("missing-shift count error = %q, want الشيفت غير موجود", msg)
		}
	}
	// shift_id 0 is the same missing shift, not a different shape.
	code, body := e.postJSON(cash, "/api/cashbox/count", map[string]any{
		"shift_id": 0, "amount_cents": 10000,
	})
	if code != http.StatusNotFound || body["ok"] != false {
		t.Fatalf("count for shift 0: want 404 ok=false, got %d body=%v", code, body)
	}
	// No row was written for the missing shift.
	if n := e.queryInt(`SELECT COUNT(*) FROM CashCounts WHERE ShiftID=?`, 999999); n != 0 {
		t.Fatalf("refused count wrote %d CashCounts rows for shift 999999", n)
	}
	// Existing shift still counts: 200.
	code, body = e.postJSON(cash, "/api/cashbox/count", map[string]any{
		"shift_id": cashShift, "amount_cents": 10000,
	})
	expectOK(t, "cashier own count", code, body)
	// Cross-user denial still 403 (R18-C1 guard).
	code, body = e.postJSON(cash2, "/api/cashbox/count", map[string]any{
		"shift_id": cashShift, "amount_cents": 10000,
	})
	if code != http.StatusForbidden || body["ok"] != false {
		t.Fatalf("cashier2 counting cashier1 shift: want 403 ok=false, got %d body=%v", code, body)
	}
}

func TestFix19_AutoLockGlobalWording(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()

	// (1) /settings global field label: "0 = بلا مدة عامة", and no
	// "0 = معطل)" anywhere on the page. (The alert-threshold labels
	// "(0 = معطلة)" are a different feature and must stay — hence the
	// closing-paren marker, which does not match "معطلة".)
	code, html := e.get(admin, "/settings")
	if code != http.StatusOK {
		t.Fatalf("GET /settings: want 200, got %d", code)
	}
	if !strings.Contains(html, "0 = بلا مدة عامة") {
		t.Fatalf("GET /settings: new global label %q missing", "0 = بلا مدة عامة")
	}
	if strings.Contains(html, "0 = معطل)") {
		t.Fatalf("GET /settings: old global label %q still present", "0 = معطل)")
	}

	// (2) /lock/settings line 13 (no own override yet): the fallback
	// sentence must say "بلا مدة عامة", never "معطلة".
	code, html = e.get(admin, "/lock/settings")
	if code != http.StatusOK {
		t.Fatalf("GET /lock/settings: want 200, got %d", code)
	}
	if !strings.Contains(html, "أو 15 دقيقة إذا كانت بلا مدة عامة") {
		t.Fatalf("GET /lock/settings: new line-13 wording missing")
	}
	if strings.Contains(html, "إذا كانت معطلة") {
		t.Fatalf("GET /lock/settings: old line-13 wording still present")
	}

	// (3) Global validation error from the lock handler.
	code, html = e.postForm(admin, "/lock/settings",
		url.Values{"global_minutes": {"999"}, "own_minutes": {""}})
	if code != http.StatusOK || !strings.Contains(html, "المدة العامة بين 0 (بلا مدة عامة) و 600 دقيقة") {
		t.Fatalf("lock global error: want 200 + new wording, got %d body=%.300q", code, html)
	}
	if strings.Contains(html, "المدة العامة بين 0 (معطل)") {
		t.Fatalf("lock global error: old wording still present")
	}

	// (4) Global save error from the store, via /settings/general.
	code, html = e.postForm(admin, "/settings/general",
		url.Values{"auto_lock_minutes": {"999"}})
	if code != http.StatusOK || !strings.Contains(html, "مهلة القفل التلقائي بين 0 (بلا مدة عامة) و 600 دقيقة") {
		t.Fatalf("settings global error: want 200 + new wording, got %d body=%.300q", code, html)
	}
	if strings.Contains(html, "مهلة القفل التلقائي بين 0 (معطل)") {
		t.Fatalf("settings global error: old wording still present")
	}

	// Per-user spots UNTOUCHED: (a) the own-value error still says
	// "(معطل)" — own 0 = never lock is correct.
	code, html = e.postForm(admin, "/lock/settings",
		url.Values{"global_minutes": {""}, "own_minutes": {"999"}})
	if code != http.StatusOK || !strings.Contains(html, "مدة القفل الخاصة بك بين 0 (معطل) و 600 دقيقة") {
		t.Fatalf("lock own error: want 200 + per-user (معطل) wording, got %d body=%.300q", code, html)
	}
	// (b) the per-user override table still renders "(معطل)" for a
	// 0-minute own value (admin may set 0 for themselves).
	code, html = e.postForm(admin, "/lock/settings",
		url.Values{"global_minutes": {""}, "own_minutes": {"0"}})
	if code != http.StatusOK {
		t.Fatalf("setting own 0: want 200, got %d body=%.300q", code, html)
	}
	code, html = e.get(admin, "/lock/settings")
	if code != http.StatusOK || !strings.Contains(html, "(معطل)") {
		t.Fatalf("override table: want per-user (معطل) to render, got %d body=%.500q", code, html)
	}
}
