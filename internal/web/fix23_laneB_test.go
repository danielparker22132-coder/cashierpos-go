// Package web — FIX-23 lane B (R23-C-F2) tests.
//
// R23-C-F2: renderAdminErr re-rendered the template with a Page that
// had no Data, and reports_dashboard.html:4 dereferences
// `.Data.TodaySales` — so a Dashboard overflow answered 500
// `خطأ في عرض الصفحة` and the Arabic overflow message never reached
// the user. The fix gives renderAdminErr a zero-value Data of the
// template's expected type (adminErrZeroData) and falls back to
// error.html — which needs no Data and always shows p.Error — if the
// template still fails. The audit covered all 18 templates
// renderAdminErr serves: 16 dereference `.Data` unguarded and would
// 500 on nil Data (only notifications_manage.html and users_list.html
// guard with `{{if .Data}}`); this test drives every one of them, plus
// an unknown template name for the fallback path.
//
// NOTE: html/template strips JS `//` comments, so these tests assert
// code markers only, never comment markers.
package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cashierpos-go/internal/store"
)

// fix23AdminErrTemplates is every template renderAdminErr is called
// with anywhere in handlers_admin.go.
var fix23AdminErrTemplates = []string{
	"notifications_inbox.html",
	"notifications_manage.html",
	"notifications_form.html",
	"notifications_preview.html",
	"notifications_log.html",
	"notifications_settings.html",
	"audit_list.html",
	"audit_detail.html",
	"audit_smart.html",
	"users_list.html",
	"users_form.html",
	"users_permissions.html",
	"settings_page.html",
	"backup_list.html",
	"reports_page.html",
	"reports_dashboard.html",
	"shifts_monitor.html",
	"attendance_list.html",
}

// fix23Head is a local head-truncation helper (the e2e truncate helper
// lives in package web_test and is not visible to package web tests).
func fix23Head(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// TestFix23_RenderAdminErrNilDataShowsMessage: for every template,
// renderAdminErr with nil Data must answer 200 with the Arabic error
// message in the body — never the generic 500 page-error text.
func TestFix23_RenderAdminErrNilDataShowsMessage(t *testing.T) {
	st, err := store.OpenTest()
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	defer st.Close()
	srv, err := New(st)
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	overflow := errors.New("المبلغ كبير جداً — تجاوز الحد الأقصى المسموح به")
	for _, tmpl := range fix23AdminErrTemplates {
		req := httptest.NewRequest("GET", "/dashboard", nil)
		rec := httptest.NewRecorder()
		srv.renderAdminErr(rec, req, tmpl, Page{Title: "اختبار"}, overflow)
		res := rec.Result()
		body := rec.Body.String()
		if res.StatusCode != http.StatusOK {
			t.Errorf("%s: status = %d, want 200 (body head %q)", tmpl, res.StatusCode, fix23Head(body, 200))
			continue
		}
		if !strings.Contains(body, "تجاوز الحد") {
			t.Errorf("%s: body does not carry the Arabic overflow message (body head %q)", tmpl, fix23Head(body, 200))
		}
		if strings.Contains(body, "خطأ في عرض الصفحة") {
			t.Errorf("%s: body contains the generic 500 page-error text", tmpl)
		}
	}
}

// TestFix23_RenderAdminErrUnknownTemplateFallsBack: a template name
// with no zero-Data mapping must still show the message via the
// error.html fallback, not the generic 500 text.
func TestFix23_RenderAdminErrUnknownTemplateFallsBack(t *testing.T) {
	st, err := store.OpenTest()
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	defer st.Close()
	srv, err := New(st)
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	req := httptest.NewRequest("GET", "/dashboard", nil)
	rec := httptest.NewRecorder()
	srv.renderAdminErr(rec, req, "no_such_template.html", Page{Title: "اختبار"}, errors.New("رسالة خطأ عربية"))
	res := rec.Result()
	body := rec.Body.String()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("unknown template: status = %d, want 200 (body head %q)", res.StatusCode, fix23Head(body, 200))
	}
	if !strings.Contains(body, "رسالة خطأ عربية") {
		t.Fatalf("unknown template: body does not carry the error message (body head %q)", fix23Head(body, 200))
	}
	if strings.Contains(body, "خطأ في عرض الصفحة") {
		t.Fatalf("unknown template: body contains the generic 500 page-error text")
	}
}

// TestFix23_RenderAdminErrKeepsCallerData: when the caller already
// supplied Data (several call sites do), renderAdminErr must not
// replace it with the zero value.
func TestFix23_RenderAdminErrKeepsCallerData(t *testing.T) {
	st, err := store.OpenTest()
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	defer st.Close()
	srv, err := New(st)
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	req := httptest.NewRequest("GET", "/dashboard", nil)
	rec := httptest.NewRecorder()
	srv.renderAdminErr(rec, req, "reports_dashboard.html",
		Page{Title: "لوحة المدير", Data: store.AdminDashboard{TodaySales: 12345}},
		errors.New("المبلغ كبير جداً — تجاوز الحد الأقصى المسموح به"))
	res := rec.Result()
	body := rec.Body.String()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body head %q)", res.StatusCode, fix23Head(body, 200))
	}
	// 12345 cents renders as 123.45 EGP via the egp func.
	if !strings.Contains(body, "123.45") {
		t.Fatalf("body lost the caller-supplied Data (TodaySales 123.45 not found; head %q)", fix23Head(body, 300))
	}
	if !strings.Contains(body, "تجاوز الحد") {
		t.Fatalf("body does not carry the Arabic overflow message (head %q)", fix23Head(body, 200))
	}
}
