// Package web_test — product_price promo (R-P11) end-to-end tests.
//
// Drives the real HTTP server: create the offer through the HTML form,
// sell through the sales API, and verify the final promo price lands on
// the invoice net. Rejection of a promo price >= the current retail
// price and the expiry window are covered here too; the engine-level
// rules (cap, competition, free price) live in the store tests.
package web_test

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// promoPriceForm posts the promo form as the given client and returns
// the status code, redirect Location and body.
func (e *e2eEnv) promoPriceForm(c *http.Client, productID int64, name, amount, endAt string) (int, string, string) {
	e.t.Helper()
	v := url.Values{
		"name":        {name},
		"type":        {"product_price"},
		"amount":      {amount},
		"is_active":   {"1"},
		"target_type": {"product"},
		"target_id":   {strconv.FormatInt(productID, 10)},
		"target_name": {""},
		"target_qty":  {"1"},
	}
	if endAt != "" {
		v.Set("end_at", endAt)
	}
	return e.postFormLoc(c, "/promotions/new", v)
}

func TestE2E_PromoPrice(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()

	// The form offers the new type with its Arabic label.
	form := e.getBody(admin, "/promotions/new")
	if !strings.Contains(form, `value="product_price"`) {
		t.Fatalf("promo form has no product_price option")
	}
	if !strings.Contains(form, "السعر الترويجي (ج.م)") {
		t.Fatalf("promo form has no 'السعر الترويجي (ج.م)' label")
	}
	if !strings.Contains(form, "سيُباع بـ") {
		t.Fatalf("promo form has no 'سيُباع بـ' price hint")
	}

	// Product at 50.00, promo price 35.00.
	pid := e.createProduct(admin, "PP-1", "سكر برومو", "50.00", "100")
	code, loc, body := e.promoPriceForm(admin, pid, "سعر ترويجي سكر", "35.00", "")
	if code != http.StatusSeeOther || loc != "/promotions" {
		t.Fatalf("create product_price offer: want 303 to /promotions, got %d loc=%q body=%q", code, loc, truncate(body, 300))
	}
	list := e.getBody(admin, "/promotions")
	if !strings.Contains(list, "سعر ترويجي سكر") {
		t.Fatalf("created offer missing from /promotions list")
	}

	// Sell 2 units: the final price is 35.00 each -> net 7000, saved 3000.
	e.openShift(admin, 0)
	invID, _ := e.cashSale(admin, pid, "2", 7000)
	if got := e.queryInt(`SELECT NetAmount FROM Invoices WHERE InvoiceID=?`, invID); got != 7000 {
		t.Fatalf("promo-price sale net = %d, want 7000", got)
	}
	if got := e.queryInt(`SELECT COALESCE(SUM(SavedAmount),0) FROM InvoiceOffers WHERE InvoiceID=?`, invID); got != 3000 {
		t.Fatalf("promo-price saved = %d, want 3000", got)
	}
	if got := e.queryInt(`SELECT COUNT(*) FROM InvoiceOffers WHERE InvoiceID=? AND OfferName=?`, invID, "سعر ترويجي سكر"); got != 1 {
		t.Fatalf("promo-price InvoiceOffers rows = %d, want 1", got)
	}

	// A promo price equal to the current retail price is refused by the form.
	code, body = e.postForm(admin, "/promotions/new", url.Values{
		"name":        {"سعر يساوي البيع"},
		"type":        {"product_price"},
		"amount":      {"50.00"},
		"is_active":   {"1"},
		"target_type": {"product"},
		"target_id":   {strconv.FormatInt(pid, 10)},
		"target_name": {""},
		"target_qty":  {"1"},
	})
	if code != http.StatusOK || !strings.Contains(body, "أقل من سعر البيع الحالي للمنتج") {
		t.Fatalf("promo >= retail via form: code=%d body=%q", code, truncate(body, 300))
	}

	// An expired offer does not apply: second product, offer ended yesterday.
	pid2 := e.createProduct(admin, "PP-2", "زيت برومو", "50.00", "100")
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	code, loc, body = e.promoPriceForm(admin, pid2, "عرض منتهٍ", "35.00", yesterday)
	if code != http.StatusSeeOther || loc != "/promotions" {
		t.Fatalf("create expired product_price offer: want 303, got %d loc=%q body=%q", code, loc, truncate(body, 300))
	}
	invID2, _ := e.cashSale(admin, pid2, "2", 10000)
	if got := e.queryInt(`SELECT NetAmount FROM Invoices WHERE InvoiceID=?`, invID2); got != 10000 {
		t.Fatalf("expired promo-price sale net = %d, want 10000 (full price)", got)
	}
	if got := e.queryInt(`SELECT COUNT(*) FROM InvoiceOffers WHERE InvoiceID=?`, invID2); got != 0 {
		t.Fatalf("expired promo-price should record no offers, got %d rows", got)
	}

	// A cashier may not open the promo form at all (RequirePerm -> 403).
	cashier := e.cashierClient()
	code, body = e.get(cashier, "/promotions/new")
	if code != http.StatusForbidden || !strings.Contains(body, "غير مسموح") {
		t.Fatalf("cashier promo form: want 403 + denial, got %d body=%q", code, truncate(body, 200))
	}
}

// TestE2E_PromoPriceBlankAmount (P-6): a blank amount on CREATE of a
// product_price offer is refused with "سعر العرض الترويجي مطلوب" and
// creates nothing — it must not silently become a free (0) offer.
// A typed 0 stays allowed (free).
func TestE2E_PromoPriceBlankAmount(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	pid := e.createProduct(admin, "PP-BLANK", "سكر برومو فارغ", "50.00", "100")

	// Blank amount on create -> form re-rendered (200) with the error.
	code, body := e.postForm(admin, "/promotions/new", url.Values{
		"name":        {"سعر فارغ"},
		"type":        {"product_price"},
		"amount":      {""},
		"is_active":   {"1"},
		"target_type": {"product"},
		"target_id":   {strconv.FormatInt(pid, 10)},
		"target_name": {""},
		"target_qty":  {"1"},
	})
	if code != http.StatusOK || !strings.Contains(body, "سعر العرض الترويجي مطلوب") {
		t.Fatalf("blank product_price create: want 200 + error, got code=%d body=%q", code, truncate(body, 300))
	}
	if got := e.queryInt(`SELECT COUNT(*) FROM Offers WHERE Name=?`, "سعر فارغ"); got != 0 {
		t.Fatalf("blank product_price created %d offer(s), want 0", got)
	}

	// Typed 0 stays allowed (free): creates and redirects to /promotions.
	code, loc, _ := e.promoPriceForm(admin, pid, "سعر مجاني صفر", "0", "")
	if code != http.StatusSeeOther || loc != "/promotions" {
		t.Fatalf("typed-0 product_price create: want 303 to /promotions, got %d loc=%q", code, loc)
	}
	if got := e.queryInt(`SELECT COUNT(*) FROM Offers WHERE Name=? AND Value='0'`, "سعر مجاني صفر"); got != 1 {
		t.Fatalf("typed-0 product_price offers = %d, want 1", got)
	}
}

// TestE2E_ProductFixedPerUnit (P-1): live HTTP probe — a product_fixed
// offer created through the form saves PER UNIT, not flat per line.
func TestE2E_ProductFixedPerUnit(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()

	// Product at 25.00, fixed 10.00/unit offer via the form.
	pid := e.createProduct(admin, "PF-1", "سكر ثابت", "25.00", "100")
	code, loc, body := e.postFormLoc(admin, "/promotions/new", url.Values{
		"name":        {"ثابت للوحدة"},
		"type":        {"product_fixed"},
		"amount":      {"10.00"},
		"is_active":   {"1"},
		"target_type": {"product"},
		"target_id":   {strconv.FormatInt(pid, 10)},
		"target_name": {""},
		"target_qty":  {"1"},
	})
	if code != http.StatusSeeOther || loc != "/promotions" {
		t.Fatalf("create product_fixed offer: want 303 to /promotions, got %d loc=%q body=%q", code, loc, truncate(body, 300))
	}
	e.openShift(admin, 0)

	// qty 2: saved 2000, net 3000.
	invID, _ := e.cashSale(admin, pid, "2", 3000)
	if got := e.queryInt(`SELECT NetAmount FROM Invoices WHERE InvoiceID=?`, invID); got != 3000 {
		t.Fatalf("qty-2 net = %d, want 3000", got)
	}
	if got := e.queryInt(`SELECT COALESCE(SUM(SavedAmount),0) FROM InvoiceOffers WHERE InvoiceID=?`, invID); got != 2000 {
		t.Fatalf("qty-2 saved = %d, want 2000", got)
	}

	// qty 3: saved 3000, net 4500 (PyQt pin: qty 3 × 10 -> 30).
	invID, _ = e.cashSale(admin, pid, "3", 4500)
	if got := e.queryInt(`SELECT NetAmount FROM Invoices WHERE InvoiceID=?`, invID); got != 4500 {
		t.Fatalf("qty-3 net = %d, want 4500", got)
	}
	if got := e.queryInt(`SELECT COALESCE(SUM(SavedAmount),0) FROM InvoiceOffers WHERE InvoiceID=?`, invID); got != 3000 {
		t.Fatalf("qty-3 saved = %d, want 3000", got)
	}

	// Cap: 10.00/unit on an 8.00 product, qty 2 -> saved capped at the
	// 1600 line total, net 0 (below-cost confirm needed: net < buy 5.00).
	pid2 := e.createProduct(admin, "PF-2", "منتج رخيص", "8.00", "100")
	code, loc, body = e.postFormLoc(admin, "/promotions/new", url.Values{
		"name":        {"ثابت سقف"},
		"type":        {"product_fixed"},
		"amount":      {"10.00"},
		"is_active":   {"1"},
		"target_type": {"product"},
		"target_id":   {strconv.FormatInt(pid2, 10)},
		"target_name": {""},
		"target_qty":  {"1"},
	})
	if code != http.StatusSeeOther || loc != "/promotions" {
		t.Fatalf("create capped product_fixed offer: want 303, got %d loc=%q body=%q", code, loc, truncate(body, 300))
	}
	sc, sb := e.sell(admin, map[string]any{
		"sale_type":        "retail",
		"lines":            []map[string]any{{"product_id": pid2, "qty": "2"}},
		"paid":             0,
		"payment_method":   "Cash",
		"allow_below_cost": true,
	})
	expectOK(t, "capped fixed sale", sc, sb)
	invID3, _ := sb["invoice_id"].(float64)
	if got := e.queryInt(`SELECT NetAmount FROM Invoices WHERE InvoiceID=?`, int64(invID3)); got != 0 {
		t.Fatalf("capped qty-2 net = %d, want 0", got)
	}
	if got := e.queryInt(`SELECT COALESCE(SUM(SavedAmount),0) FROM InvoiceOffers WHERE InvoiceID=?`, int64(invID3)); got != 1600 {
		t.Fatalf("capped qty-2 saved = %d, want 1600", got)
	}
}
