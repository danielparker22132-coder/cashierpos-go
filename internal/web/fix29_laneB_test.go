// Package web_test — FIX-29 lane B: stocktake approve applied diffs via GET.
//
// handleStocktakeApprove had no method check, so a GET on
// /stocktake/{id}/approve called ApproveStocktake and rewrote stock.
// The UI only ever links it as a POST form (stocktake_detail.html), which
// is why this is MEDIUM not HIGH — but a prefetched/crawled GET would
// still have silently applied the counted diffs. The fix makes approval
// POST-only: a non-POST request 303-redirects to the detail page with no
// action, the same shape as handlePromoDelete (FIX-28 lane B). The
// permission gate (PermApproveStocktake) and the approve logic are
// unchanged.
package web_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// fix29BClosedStocktake builds a stocktake with a counted diff (product
// stocked 10, counted 8) and closes it, ready for approval. Returns the
// stocktake id and the product id.
func fix29BClosedStocktake(t *testing.T, e *e2eEnv, mgr *http.Client) (int64, int64) {
	t.Helper()
	pid := e.createProduct(mgr, "F29-B1", "منتج جرد ب29", "10.00", "10")
	code, loc, body := e.postFormLoc(mgr, "/stocktake/new", url.Values{"notes": {"جرد ب29"}})
	if code != http.StatusSeeOther {
		t.Fatalf("create stocktake: want 303, got %d body=%q", code, truncate(body, 300))
	}
	stID, err := strconv.ParseInt(strings.TrimPrefix(loc, "/stocktake/"), 10, 64)
	if err != nil || stID == 0 {
		t.Fatalf("stocktake redirect %q has no id", loc)
	}
	code, _, body = e.postFormLoc(mgr, fmt.Sprintf("/stocktake/%d/scan", stID), url.Values{
		"barcode": {"F29-B1"}, "qty": {"8"},
	})
	if code != http.StatusSeeOther {
		t.Fatalf("scan: want 303, got %d body=%q", code, truncate(body, 300))
	}
	code, _, body = e.postFormLoc(mgr, fmt.Sprintf("/stocktake/%d/close", stID), url.Values{})
	if code != http.StatusSeeOther {
		t.Fatalf("close: want 303, got %d body=%q", code, truncate(body, 300))
	}
	return stID, pid
}

func fix29BApprovedCount(t *testing.T, e *e2eEnv, stID int64) int64 {
	t.Helper()
	return e.queryInt(`SELECT COUNT(*) FROM Stocktakes WHERE StocktakeID=? AND ApprovedAt IS NOT NULL AND ApprovedAt<>''`, stID)
}

func fix29BProductQty(t *testing.T, e *e2eEnv, pid int64) int64 {
	t.Helper()
	return e.queryInt(`SELECT Quantity FROM Products WHERE ProductID=?`, pid)
}

// TestFix29B_StocktakeApproveIsPostForm asserts the served detail page
// carries approval as a POST form and has no GET approve link.
func TestFix29B_StocktakeApproveIsPostForm(t *testing.T) {
	e := readyEnv(t, false)
	mgr := e.managerClient()
	stID, _ := fix29BClosedStocktake(t, e, mgr)
	code, body := e.get(mgr, fmt.Sprintf("/stocktake/%d", stID))
	if code != http.StatusOK {
		t.Fatalf("GET /stocktake/%d: status = %d, want 200", stID, code)
	}
	form := fmt.Sprintf(`<form method="post" action="/stocktake/%d/approve"`, stID)
	if !strings.Contains(body, form) {
		t.Fatalf("detail page has no POST approve form for stocktake %d (body %.500q)", stID, body)
	}
	if strings.Contains(body, fmt.Sprintf(`href="/stocktake/%d/approve"`, stID)) {
		t.Fatalf("detail page still has a GET approve link for stocktake %d", stID)
	}
}

// TestFix29B_StocktakeApproveGetDoesNotApprovePostDoes: GET on the approve
// route must not approve (303 redirect, ApprovedAt unset, stock untouched);
// POST must approve (303, ApprovedAt set, counted diff applied).
func TestFix29B_StocktakeApproveGetDoesNotApprovePostDoes(t *testing.T) {
	e := readyEnv(t, false)
	mgr := e.managerClient()
	stID, pid := fix29BClosedStocktake(t, e, mgr)
	path := fmt.Sprintf("/stocktake/%d/approve", stID)

	if got := fix29BProductQty(t, e, pid); got != 10000 {
		t.Fatalf("product quantity before approve = %d milli, want 10000", got)
	}
	code, _ := e.get(noRedirect(mgr), path)
	if code != http.StatusSeeOther {
		t.Fatalf("GET approve: status = %d, want 303", code)
	}
	if got := fix29BApprovedCount(t, e, stID); got != 0 {
		t.Fatalf("GET approve set ApprovedAt: approved count = %d, want 0", got)
	}
	if got := fix29BProductQty(t, e, pid); got != 10000 {
		t.Fatalf("GET approve moved stock: quantity = %d milli, want still 10000", got)
	}
	code, _, _ = e.postFormLoc(mgr, path, url.Values{"reason": {"اعتماد جرد ب29"}})
	if code != http.StatusSeeOther {
		t.Fatalf("POST approve: status = %d, want 303", code)
	}
	if got := fix29BApprovedCount(t, e, stID); got != 1 {
		t.Fatalf("POST approve did not set ApprovedAt: approved count = %d, want 1", got)
	}
	if got := fix29BProductQty(t, e, pid); got != 8000 {
		t.Fatalf("product quantity after POST approve = %d milli, want 8000", got)
	}
}

// TestFix29B_StocktakeApproveCashierGetDenied: the permission gate is
// unchanged — a cashier GETting the approve route gets 403, not the
// 303 redirect, and nothing is approved.
func TestFix29B_StocktakeApproveCashierGetDenied(t *testing.T) {
	e := readyEnv(t, false)
	mgr := e.managerClient()
	stID, pid := fix29BClosedStocktake(t, e, mgr)
	code, _ := e.get(noRedirect(e.cashierClient()), fmt.Sprintf("/stocktake/%d/approve", stID))
	if code != http.StatusForbidden {
		t.Fatalf("cashier GET approve: status = %d, want 403", code)
	}
	if got := fix29BApprovedCount(t, e, stID); got != 0 {
		t.Fatalf("cashier GET approve set ApprovedAt: approved count = %d, want 0", got)
	}
	if got := fix29BProductQty(t, e, pid); got != 10000 {
		t.Fatalf("cashier GET approve moved stock: quantity = %d milli, want still 10000", got)
	}
}
