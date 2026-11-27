// Package web_test — FIX-29 lane C (R29-C-F3): stocktake close/cancel
// mutate via GET.
//
// handleStocktakeClose and handleStocktakeCancel had no method check, so
// a plain GET on /stocktake/{id}/close or /stocktake/{id}/cancel changed
// the session status (a link, a prefetch, or a crawler could close or
// cancel a session). The fix makes both handlers POST-only: a non-POST
// request 303-redirects to the detail page with no action, the same shape
// as the sibling HTML mutating handlers (handlePromoDelete, stocktake
// create/scan/set). The permission gate (PermManageStocktakes) and the
// close/cancel logic are unchanged. No template change was needed: the
// stocktake detail page already renders close/cancel as POST forms
// (stocktake_detail.html:45-46), never GET links.
package web_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// fix29CStocktakeStatus returns 1 when the stocktake has the given status.
func fix29CStocktakeStatus(t *testing.T, e *e2eEnv, id int64, status string) int64 {
	t.Helper()
	return e.queryInt("SELECT COUNT(*) FROM Stocktakes WHERE StocktakeID=? AND Status=?", id, status)
}

// TestFix29C_StocktakeClosePostWorksAndGetDoesNot: GET on the close route
// must not close (303 redirect, status stays open); POST must close (303,
// status closed).
func TestFix29C_StocktakeClosePostWorksAndGetDoesNot(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	sid := fix26AStocktake(t, e, admin)
	path := fmt.Sprintf("/stocktake/%d/close", sid)
	code, _ := e.get(noRedirect(admin), path)
	if code != http.StatusSeeOther {
		t.Fatalf("GET close: status = %d, want 303", code)
	}
	if got := fix29CStocktakeStatus(t, e, sid, "open"); got != 1 {
		t.Fatalf("GET close changed status: open count = %d, want 1", got)
	}
	code, _ = e.postForm(admin, path, url.Values{})
	if code != http.StatusSeeOther {
		t.Fatalf("POST close: status = %d, want 303", code)
	}
	if got := fix29CStocktakeStatus(t, e, sid, "closed"); got != 1 {
		t.Fatalf("POST close did not close: closed count = %d, want 1", got)
	}
}

// TestFix29C_StocktakeCancelPostWorksAndGetDoesNot: GET on the cancel route
// must not cancel (303 redirect, status stays open); POST must cancel
// (303, status cancelled).
func TestFix29C_StocktakeCancelPostWorksAndGetDoesNot(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	sid := fix26AStocktake(t, e, admin)
	path := fmt.Sprintf("/stocktake/%d/cancel", sid)
	code, _ := e.get(noRedirect(admin), path)
	if code != http.StatusSeeOther {
		t.Fatalf("GET cancel: status = %d, want 303", code)
	}
	if got := fix29CStocktakeStatus(t, e, sid, "open"); got != 1 {
		t.Fatalf("GET cancel changed status: open count = %d, want 1", got)
	}
	code, _ = e.postForm(admin, path, url.Values{})
	if code != http.StatusSeeOther {
		t.Fatalf("POST cancel: status = %d, want 303", code)
	}
	if got := fix29CStocktakeStatus(t, e, sid, "cancelled"); got != 1 {
		t.Fatalf("POST cancel did not cancel: cancelled count = %d, want 1", got)
	}
}

// TestFix29C_StocktakeDetailHasPostFormsNoGetLinks asserts the served
// detail page carries close/cancel as POST forms and has no GET links for
// either action.
func TestFix29C_StocktakeDetailHasPostFormsNoGetLinks(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	sid := fix26AStocktake(t, e, admin)
	code, body := e.get(admin, fmt.Sprintf("/stocktake/%d", sid))
	if code != http.StatusOK {
		t.Fatalf("GET detail: status = %d, want 200", code)
	}
	for _, action := range []string{"close", "cancel"} {
		form := fmt.Sprintf(`<form method="post" action="/stocktake/%d/%s"`, sid, action)
		if !strings.Contains(body, form) {
			t.Fatalf("detail page has no POST %s form for stocktake %d", action, sid)
		}
		if strings.Contains(body, fmt.Sprintf(`href="/stocktake/%d/%s"`, sid, action)) {
			t.Fatalf("detail page still has a GET %s link for stocktake %d", action, sid)
		}
	}
}

// TestFix29C_StocktakeCloseCancelCashierDenied: the permission gate is
// unchanged — a cashier GETting or POSTing either route gets 403 and the
// session stays open.
func TestFix29C_StocktakeCloseCancelCashierDenied(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	cashier := e.cashierClient()
	sid := fix26AStocktake(t, e, admin)
	for _, action := range []string{"close", "cancel"} {
		path := fmt.Sprintf("/stocktake/%d/%s", sid, action)
		code, _ := e.get(noRedirect(cashier), path)
		if code != http.StatusForbidden {
			t.Fatalf("cashier GET %s: status = %d, want 403", action, code)
		}
		code, _ = e.postForm(cashier, path, url.Values{})
		if code != http.StatusForbidden {
			t.Fatalf("cashier POST %s: status = %d, want 403", action, code)
		}
	}
	if got := fix29CStocktakeStatus(t, e, sid, "open"); got != 1 {
		t.Fatalf("cashier requests changed status: open count = %d, want 1", got)
	}
}
