// Package web_test — FIX-28 lane B: the promos delete link no-op.
//
// The /promotions delete control was a plain GET link gated on
// confirm(): headless/cloud browsers auto-dismiss confirm() (it returns
// false), so the click cancelled navigation and the offer was never
// deleted — while the route itself still deleted on GET. The fix makes
// deletion a POST form with NO JS confirm gate (a POST form with
// onsubmit="return confirm(...)" would no-op exactly the same way) and
// makes handlePromoDelete POST-only (non-POST redirects to the list with
// no action, the sibling HTML-handler shape). Deleting an unused offer
// is reversible by re-creating it, and the handler still refuses a used
// offer. Permission gate (PermManagePromotions) unchanged.
package web_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/money"
	"cashierpos-go/internal/store"
)

func fix28BCreateOffer(t *testing.T, e *e2eEnv, name string, amount money.Cents) int64 {
	t.Helper()
	o, err := e.store.CreateOffer(context.Background(),
		&auth.User{ID: 1, UserName: "admin", FullName: "مدير النظام", Role: "admin"},
		store.OfferInput{Name: name, Type: store.OfferInvoiceFixed, Amount: amount, IsActive: true})
	if err != nil {
		t.Fatalf("CreateOffer %s: %v", name, err)
	}
	return o.ID
}

func fix28BOfferCount(t *testing.T, e *e2eEnv, id int64) int64 {
	t.Helper()
	return e.queryInt("SELECT COUNT(*) FROM Offers WHERE OfferID=?", id)
}

// TestFix28B_PromosDeleteIsPostFormNoConfirmGate asserts the served
// /promotions page carries the delete action as a POST form and has no
// confirm()-gated GET delete link for the offer.
func TestFix28B_PromosDeleteIsPostFormNoConfirmGate(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	id := fix28BCreateOffer(t, e, "عرض اختبار ب", 500)
	code, body := e.get(admin, "/promotions")
	if code != http.StatusOK {
		t.Fatalf("GET /promotions: status = %d, want 200", code)
	}
	form := fmt.Sprintf(`<form method="post" action="/promotions/%d/delete"`, id)
	if !strings.Contains(body, form) {
		t.Fatalf("promos page has no POST delete form for offer %d (body %.500q)", id, body)
	}
	if strings.Contains(body, fmt.Sprintf(`href="/promotions/%d/delete"`, id)) {
		t.Fatalf("promos page still has a GET delete link for offer %d", id)
	}
	if strings.Contains(body, "confirm('حذف هذا العرض") {
		t.Fatalf("promos page still has the confirm()-gated delete control")
	}
}

// TestFix28B_PromosDeletePostWorksAndGetDoesNot: GET on the delete route
// must not delete (303 redirect, offer intact); POST must delete (303,
// offer gone).
func TestFix28B_PromosDeletePostWorksAndGetDoesNot(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	id := fix28BCreateOffer(t, e, "عرض اختبار ب", 500)
	path := fmt.Sprintf("/promotions/%d/delete", id)
	code, _ := e.get(noRedirect(admin), path)
	if code != http.StatusSeeOther {
		t.Fatalf("GET delete: status = %d, want 303", code)
	}
	if got := fix28BOfferCount(t, e, id); got != 1 {
		t.Fatalf("GET delete removed the offer: count = %d, want 1", got)
	}
	code, _ = e.postForm(admin, path, url.Values{})
	if code != http.StatusSeeOther {
		t.Fatalf("POST delete: status = %d, want 303", code)
	}
	if got := fix28BOfferCount(t, e, id); got != 0 {
		t.Fatalf("POST delete left the offer: count = %d, want 0", got)
	}
}

// TestFix28B_PromosDeleteUsedOfferRefused: an offer applied to an invoice
// must still be refused (200 + refusal text, offer intact) via POST.
func TestFix28B_PromosDeleteUsedOfferRefused(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	pid := e.createProduct(admin, "F28-B1", "منتج ب28", "20.00", "100")
	id := fix28BCreateOffer(t, e, "عرض مستخدم ب", 500)
	// Sell 1 unit at 20.00; the 5.00 invoice-fixed offer applies, net 15.00.
	e.openShift(admin, 10000)
	invID, _ := e.cashSale(admin, pid, "1", 1500)
	_ = invID
	if got := e.queryInt("SELECT COUNT(*) FROM InvoiceOffers WHERE OfferID=?", id); got != 1 {
		t.Fatalf("sale did not record the offer: InvoiceOffers = %d, want 1", got)
	}
	code, body := e.postForm(admin, fmt.Sprintf("/promotions/%d/delete", id), url.Values{})
	if code != http.StatusOK {
		t.Fatalf("POST delete used offer: status = %d, want 200 (refusal page)", code)
	}
	if !strings.Contains(body, "لا يمكن حذف العرض") {
		t.Fatalf("POST delete used offer: no refusal text (body %.300q)", body)
	}
	if got := fix28BOfferCount(t, e, id); got != 1 {
		t.Fatalf("used offer was deleted: count = %d, want 1", got)
	}
}

// TestFix28B_PromosDeleteCashierDenied: the permission gate is unchanged —
// a cashier POSTing the delete route gets 403 and the offer survives.
func TestFix28B_PromosDeleteCashierDenied(t *testing.T) {
	e := readyEnv(t, false)
	id := fix28BCreateOffer(t, e, "عرض اختبار ب", 500)
	code, _ := e.postForm(e.cashierClient(), fmt.Sprintf("/promotions/%d/delete", id), url.Values{})
	if code != http.StatusForbidden {
		t.Fatalf("cashier POST delete: status = %d, want 403", code)
	}
	if got := fix28BOfferCount(t, e, id); got != 1 {
		t.Fatalf("cashier delete removed the offer: count = %d, want 1", got)
	}
}
