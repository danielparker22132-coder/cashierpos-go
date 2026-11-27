// Package web_test — purchase-form combobox + expiry duration picker
// (lane A): the product field is a typed name resolved server-side
// against existing products (exact, trimmed, case-insensitive); an
// unknown name refuses the whole submission and saves nothing; the
// legacy product_id post still works; the template renders one shared
// datalist and five duration selects.
package web_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func purchaseFormCount(t *testing.T, e *e2eEnv) int64 {
	t.Helper()
	return e.queryInt(`SELECT COUNT(*) FROM PurchaseInvoices`)
}

func purchaseFormLastLineProduct(t *testing.T, e *e2eEnv) int64 {
	t.Helper()
	return e.queryInt(`SELECT ProductID FROM PurchaseInvoiceDetails WHERE PurchaseID=(SELECT MAX(PurchaseID) FROM PurchaseInvoices)`)
}

func purchaseFormLastLineCount(t *testing.T, e *e2eEnv) int64 {
	t.Helper()
	return e.queryInt(`SELECT COUNT(*) FROM PurchaseInvoiceDetails WHERE PurchaseID=(SELECT MAX(PurchaseID) FROM PurchaseInvoices)`)
}

func TestPurchaseForm_TypedExactNameResolves(t *testing.T) {
	e, admin, _, _ := fix25BEnv(t)
	vid := fix25BVendor(t, e, admin, "مورد شراء أ")
	prod := e.createProduct(admin, "PF-A1", "سكر", "15.00", "100")

	code, loc, _ := e.postFormLoc(admin, "/purchases/new", url.Values{
		"vendor_id": {fmt.Sprint(vid)}, "paid": {"0"}, "method": {"Visa"},
		"product": {"سكر"}, "qty": {"2"}, "price": {"10.00"},
	})
	if code != http.StatusSeeOther {
		t.Fatalf("POST /purchases/new typed exact name: status = %d, want 303 (loc %q)", code, loc)
	}
	if !strings.HasPrefix(loc, "/purchases/") {
		t.Fatalf("POST /purchases/new typed exact name: Location = %q, want /purchases/<id>", loc)
	}
	if n := purchaseFormCount(t, e); n != 1 {
		t.Fatalf("purchases created = %d, want 1", n)
	}
	if got := purchaseFormLastLineProduct(t, e); got != prod {
		t.Fatalf("purchase line ProductID = %d, want %d (typed name must resolve to the product)", got, prod)
	}
}

func TestPurchaseForm_TypedNameCaseAndSpacesResolve(t *testing.T) {
	e, admin, _, _ := fix25BEnv(t)
	vid := fix25BVendor(t, e, admin, "مورد شراء ب")
	prod := e.createProduct(admin, "PF-B1", "Sugar", "15.00", "100")

	code, loc, _ := e.postFormLoc(admin, "/purchases/new", url.Values{
		"vendor_id": {fmt.Sprint(vid)}, "paid": {"0"}, "method": {"Visa"},
		"product": {"  sUgAr "}, "qty": {"1"}, "price": {"10.00"},
	})
	if code != http.StatusSeeOther {
		t.Fatalf("POST /purchases/new typed mixed-case name with spaces: status = %d, want 303 (loc %q)", code, loc)
	}
	if n := purchaseFormCount(t, e); n != 1 {
		t.Fatalf("purchases created = %d, want 1", n)
	}
	if got := purchaseFormLastLineProduct(t, e); got != prod {
		t.Fatalf("purchase line ProductID = %d, want %d (case/space-insensitive match)", got, prod)
	}
}

func TestPurchaseForm_TypedUnknownNameRefusesAndSavesNothing(t *testing.T) {
	e, admin, _, _ := fix25BEnv(t)
	vid := fix25BVendor(t, e, admin, "مورد شراء ج")
	e.createProduct(admin, "PF-C1", "سكر", "15.00", "100")

	code, body := e.postForm(admin, "/purchases/new", url.Values{
		"vendor_id": {fmt.Sprint(vid)}, "paid": {"0"}, "method": {"Visa"},
		"product": {"صنف غير موجود xyz"}, "qty": {"1"}, "price": {"10.00"},
	})
	if code != http.StatusOK {
		t.Fatalf("POST /purchases/new unknown name: status = %d, want 200 (form re-render with error)", code)
	}
	for _, want := range []string{"صنف غير موجود xyz", "غير موجود", "أنشئه أولاً من صفحة المنتجات"} {
		if !strings.Contains(body, want) {
			t.Fatalf("POST /purchases/new unknown name: body lacks %q: %q", want, truncate(body, 400))
		}
	}
	if n := purchaseFormCount(t, e); n != 0 {
		t.Fatalf("purchases created = %d, want 0 (unknown name must save nothing)", n)
	}
}

func TestPurchaseForm_EmptyProductRowsIgnored(t *testing.T) {
	e, admin, _, _ := fix25BEnv(t)
	vid := fix25BVendor(t, e, admin, "مورد شراء د")
	prod := e.createProduct(admin, "PF-D1", "سكر", "15.00", "100")

	// All rows empty: no purchase is created.
	code, body := e.postForm(admin, "/purchases/new", url.Values{
		"vendor_id": {fmt.Sprint(vid)}, "paid": {"0"}, "method": {"Visa"},
		"product": {""}, "qty": {""}, "price": {""},
	})
	if code != http.StatusOK {
		t.Fatalf("POST /purchases/new all-empty rows: status = %d, want 200", code)
	}
	if !strings.Contains(body, "لا يوجد أصناف") {
		t.Fatalf("POST /purchases/new all-empty rows: body lacks empty-lines message: %q", truncate(body, 300))
	}
	if n := purchaseFormCount(t, e); n != 0 {
		t.Fatalf("purchases created after all-empty post = %d, want 0", n)
	}

	// One valid row + four empty rows: exactly one line is saved.
	code, loc, _ := e.postFormLoc(admin, "/purchases/new", url.Values{
		"vendor_id": {fmt.Sprint(vid)}, "paid": {"0"}, "method": {"Visa"},
		"product": {"سكر", "", "", "", ""},
		"qty":     {"2", "", "", "", ""},
		"price":   {"10.00", "", "", "", ""},
	})
	if code != http.StatusSeeOther {
		t.Fatalf("POST /purchases/new valid + empty rows: status = %d, want 303 (loc %q)", code, loc)
	}
	if n := purchaseFormCount(t, e); n != 1 {
		t.Fatalf("purchases created = %d, want 1", n)
	}
	if n := purchaseFormLastLineCount(t, e); n != 1 {
		t.Fatalf("purchase lines = %d, want 1 (empty rows must be ignored)", n)
	}
	if got := purchaseFormLastLineProduct(t, e); got != prod {
		t.Fatalf("purchase line ProductID = %d, want %d", got, prod)
	}
}

func TestPurchaseForm_LegacyProductIDStillWorks(t *testing.T) {
	e, admin, _, _ := fix25BEnv(t)
	vid := fix25BVendor(t, e, admin, "مورد شراء هـ")
	prod := e.createProduct(admin, "PF-E1", "سكر", "15.00", "100")

	code, loc, _ := e.postFormLoc(admin, "/purchases/new", url.Values{
		"vendor_id": {fmt.Sprint(vid)}, "paid": {"0"}, "method": {"Visa"},
		"product_id": {fmt.Sprint(prod)}, "qty": {"3"}, "price": {"10.00"},
	})
	if code != http.StatusSeeOther {
		t.Fatalf("POST /purchases/new legacy product_id: status = %d, want 303 (loc %q)", code, loc)
	}
	if n := purchaseFormCount(t, e); n != 1 {
		t.Fatalf("purchases created = %d, want 1", n)
	}
	if got := purchaseFormLastLineProduct(t, e); got != prod {
		t.Fatalf("purchase line ProductID = %d, want %d (legacy product_id fallback)", got, prod)
	}
}

func TestPurchaseForm_TemplateMarkers(t *testing.T) {
	e, admin, _, _ := fix25BEnv(t)
	e.createProduct(admin, "PF-F1", "سكر", "15.00", "100")

	code, body := e.get(admin, "/purchases/new")
	if code != http.StatusOK {
		t.Fatalf("GET /purchases/new: status = %d, want 200", code)
	}
	if !strings.Contains(body, `<datalist id="purchase-products">`) {
		t.Fatalf("form lacks shared datalist: %q", truncate(body, 400))
	}
	if n := strings.Count(body, `list="purchase-products"`); n != 5 {
		t.Fatalf("combobox inputs with list=purchase-products = %d, want 5", n)
	}
	if n := strings.Count(body, `name="product"`); n != 5 {
		t.Fatalf(`inputs name="product" = %d, want 5`, n)
	}
	if n := strings.Count(body, `class="expiry-duration"`); n != 5 {
		t.Fatalf("duration selects = %d, want 5", n)
	}
	for _, want := range []string{`value="7">أسبوع`, `value="14">أسبوعين`, `value="1m">شهر`, `value="12m">سنة`, `value="24m">سنتين`} {
		if !strings.Contains(body, want) {
			t.Fatalf("form lacks duration option %q", want)
		}
	}
	if strings.Contains(body, `<select name="product_id">`) {
		t.Fatalf("form still renders the old product_id select")
	}
	// The duration script must exist, clamp months to the target
	// month's last day (31 Jan + 1m -> Feb 28/29, never Mar 3), and
	// must contain no // comments (html/template strips them).
	start := strings.Index(body, "<script>")
	end := strings.Index(body, "</script>")
	if start < 0 || end < 0 || end <= start {
		t.Fatalf("form lacks the expiry-duration <script> block")
	}
	script := body[start:end]
	for _, want := range []string{"expiry-duration", "addMonths", "new Date(y, m + 1, 0)", "closest"} {
		if !strings.Contains(script, want) {
			t.Fatalf("duration script lacks %q", want)
		}
	}
	if strings.Contains(script, "//") {
		t.Fatalf("duration script contains a // comment marker")
	}
}
