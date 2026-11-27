// Package web — FIX-27 lane A (R27-C-F1 / R27-E-F2): unit tests for the
// safeHTMLErr gate behind the four autolock-settings error renders in
// handlers_lock.go. The exact driver strings live-proven pre-fix must
// map to the single generic Arabic message; a safe Arabic
// business/validation error is kept verbatim; nil maps to "".
package web

import (
	"errors"
	"testing"
)

func TestFix27A_SafeHTMLErrLockErrors(t *testing.T) {
	if got := safeHTMLErr(nil); got != "" {
		t.Fatalf("safeHTMLErr(nil) = %q, want %q", got, "")
	}
	if got := safeHTMLErr(errors.New("SQL logic error: no such table: AppSettings (1)")); got != "حدث خطأ داخلي، حاول مرة أخرى" {
		t.Fatalf("safeHTMLErr(AppSettings driver) = %q, want generic", got)
	}
	if got := safeHTMLErr(errors.New("SQL logic error: no such table: AuditLog (1)")); got != "حدث خطأ داخلي، حاول مرة أخرى" {
		t.Fatalf("safeHTMLErr(AuditLog driver) = %q, want generic", got)
	}
	if got := safeHTMLErr(errors.New("no such table: AppSettings")); got != "حدث خطأ داخلي، حاول مرة أخرى" {
		t.Fatalf("safeHTMLErr(no such table) = %q, want generic", got)
	}
	if got := safeHTMLErr(errors.New("مدة القفل الخاصة بك بين 0 (معطل) و 600 دقيقة")); got != "مدة القفل الخاصة بك بين 0 (معطل) و 600 دقيقة" {
		t.Fatalf("safeHTMLErr(Arabic validation) = %q, want it kept verbatim", got)
	}
}
