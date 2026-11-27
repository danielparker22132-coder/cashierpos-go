// Package web — FIX-26 lane A (R26-A-F1): unit tests for safeHTMLErr,
// the gate behind every HTML error render in handlers_shifts.go and
// handlers_sales.go. A raw driver/library error becomes the single
// generic Arabic message; a safe Arabic business/validation error is
// kept verbatim; nil maps to "".
package web

import (
	"errors"
	"testing"
)

func TestFix26A_SafeHTMLErr(t *testing.T) {
	if got := safeHTMLErr(nil); got != "" {
		t.Fatalf("safeHTMLErr(nil) = %q, want %q", got, "")
	}
	if got := safeHTMLErr(errors.New("SQL logic error: no such table: Products (1)")); got != "حدث خطأ داخلي، حاول مرة أخرى" {
		t.Fatalf("safeHTMLErr(driver) = %q, want generic", got)
	}
	if got := safeHTMLErr(errors.New("no such table: Expenses")); got != "حدث خطأ داخلي، حاول مرة أخرى" {
		t.Fatalf("safeHTMLErr(no such table) = %q, want generic", got)
	}
	if got := safeHTMLErr(errors.New("sql: Scan error on column index 4")); got != "حدث خطأ داخلي، حاول مرة أخرى" {
		t.Fatalf("safeHTMLErr(scan) = %q, want generic", got)
	}
	if got := safeHTMLErr(errors.New("لا يوجد شيفت مفتوح لإغلاقه")); got != "لا يوجد شيفت مفتوح لإغلاقه" {
		t.Fatalf("safeHTMLErr(Arabic business) = %q, want it kept verbatim", got)
	}
	if got := safeHTMLErr(errors.New("تعذّر إضافة المخزون")); got != "تعذّر إضافة المخزون" {
		t.Fatalf("safeHTMLErr(Arabic generic) = %q, want it kept verbatim", got)
	}
	if got := safeHTMLErr(errors.New("المبلغ كبير جداً — تجاوز الحد الأقصى المسموح به")); got != "المبلغ كبير جداً — تجاوز الحد الأقصى المسموح به" {
		t.Fatalf("safeHTMLErr(overflow Arabic) = %q, want it kept verbatim", got)
	}
}
