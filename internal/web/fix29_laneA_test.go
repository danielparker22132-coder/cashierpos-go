// Package web_test — FIX-29 lane A: the promos toggle link no-op.
//
// The /promotions toggle control was a plain GET link and handlePromoToggle
// had no method check, so any prefetch, crawler or pasted URL flipped an
// offer's active state. The fix makes the toggle a POST form (same inline
// shape as the fix-28 delete form in the same template) and makes
// handlePromoToggle POST-only (non-POST redirects to the list with no
// action, the same shape as handlePromoDelete). Permission gate
// (PermManagePromotions) and toggle logic unchanged.
package web_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func fix29AOfferIsActive(t *testing.T, e *e2eEnv, id int64) int64 {
	t.Helper()
	return e.queryInt("SELECT IsActive FROM Offers WHERE OfferID=?", id)
}

// TestFix29A_PromosToggleIsPostForm asserts the served /promotions page
// carries the toggle action as a POST form and has no GET toggle link
// for the offer.
func TestFix29A_PromosToggleIsPostForm(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	id := fix28BCreateOffer(t, e, "عرض اختبار أ29", 500)
	code, body := e.get(admin, "/promotions")
	if code != http.StatusOK {
		t.Fatalf("GET /promotions: status = %d, want 200", code)
	}
	form := fmt.Sprintf(`<form method="post" action="/promotions/%d/toggle"`, id)
	if !strings.Contains(body, form) {
		t.Fatalf("promos page has no POST toggle form for offer %d (body %.500q)", id, body)
	}
	if strings.Contains(body, fmt.Sprintf(`href="/promotions/%d/toggle"`, id)) {
		t.Fatalf("promos page still has a GET toggle link for offer %d", id)
	}
}

// TestFix29A_PromosTogglePostFlipsAndGetDoesNot: GET on the toggle route
// must not flip IsActive (303 redirect, value intact); POST must flip it
// exactly once per request (303, value toggled).
func TestFix29A_PromosTogglePostFlipsAndGetDoesNot(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	id := fix28BCreateOffer(t, e, "عرض اختبار أ29", 500)
	path := fmt.Sprintf("/promotions/%d/toggle", id)
	if got := fix29AOfferIsActive(t, e, id); got != 1 {
		t.Fatalf("setup: IsActive = %d, want 1", got)
	}
	code, _ := e.get(noRedirect(admin), path)
	if code != http.StatusSeeOther {
		t.Fatalf("GET toggle: status = %d, want 303", code)
	}
	if got := fix29AOfferIsActive(t, e, id); got != 1 {
		t.Fatalf("GET toggle flipped the offer: IsActive = %d, want 1", got)
	}
	code, _ = e.postForm(admin, path, url.Values{})
	if code != http.StatusSeeOther {
		t.Fatalf("POST toggle: status = %d, want 303", code)
	}
	if got := fix29AOfferIsActive(t, e, id); got != 0 {
		t.Fatalf("POST toggle did not flip the offer: IsActive = %d, want 0", got)
	}
	code, _ = e.postForm(admin, path, url.Values{})
	if code != http.StatusSeeOther {
		t.Fatalf("POST toggle again: status = %d, want 303", code)
	}
	if got := fix29AOfferIsActive(t, e, id); got != 1 {
		t.Fatalf("second POST toggle did not flip back: IsActive = %d, want 1", got)
	}
}

// TestFix29A_PromosToggleCashierDenied: the permission gate is unchanged —
// a cashier POSTing the toggle route gets 403 and the offer is untouched.
func TestFix29A_PromosToggleCashierDenied(t *testing.T) {
	e := readyEnv(t, false)
	id := fix28BCreateOffer(t, e, "عرض اختبار أ29", 500)
	code, _ := e.postForm(e.cashierClient(), fmt.Sprintf("/promotions/%d/toggle", id), url.Values{})
	if code != http.StatusForbidden {
		t.Fatalf("cashier POST toggle: status = %d, want 403", code)
	}
	if got := fix29AOfferIsActive(t, e, id); got != 1 {
		t.Fatalf("cashier toggle flipped the offer: IsActive = %d, want 1", got)
	}
}
