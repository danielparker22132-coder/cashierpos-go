// Package web — Wave 2 parity trigger tests (GAP-REPORT §C G-8, §A H-10).
//
// The three notification triggers are wired at the store layer, so these
// tests drive the store directly (fresh :memory: DB per test) plus the
// real HTTP server for the H-10 heartbeat. Live HTTP probes of the same
// flows are recorded in ~/workspace/go-parity-triggers/REPORT.md.
package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/money"
	"cashierpos-go/internal/store"
)

// --- shared helpers (Wave 2 only; other test files keep their own) ---

func trigAdmin() *auth.User {
	return &auth.User{ID: 1, UserName: "admin", FullName: "مدير النظام", Role: "admin"}
}

func trigMustUser(t *testing.T, s *store.Store, name, role string) *auth.User {
	t.Helper()
	id, err := s.CreateUser(context.Background(), trigAdmin(), store.UserInput{
		UserName: name, Password: "pass1234", FullName: name, Role: role, IsActive: true,
	})
	if err != nil {
		t.Fatalf("CreateUser %s: %v", name, err)
	}
	return &auth.User{ID: id, UserName: name, FullName: name, Role: role}
}

func trigProductInput(name string, qty, minQty money.Qty) store.ProductInput {
	return store.ProductInput{
		Name: name, BuyPrice: 1000, BulkPrice: 1200, WholesalePrice: 1500,
		RetailPrice: 2000, Quantity: qty, MinQuantity: minQty, Unit: "قطعة",
		ExpiryAlertDays: 30, ScaleBarcodeType: "WeightEmbedded", IsActive: true,
	}
}

func trigMustProduct(t *testing.T, s *store.Store, admin *auth.User, name string, qty, minQty money.Qty) *store.Product {
	t.Helper()
	p, err := s.CreateProduct(context.Background(), admin, trigProductInput(name, qty, minQty))
	if err != nil {
		t.Fatalf("CreateProduct %s: %v", name, err)
	}
	return p
}

// trigInputFromProduct rebuilds a full update input from a fresh read, so
// an update never resets stock (or any other field) to a stale value.
func trigInputFromProduct(p *store.Product) store.ProductInput {
	return store.ProductInput{
		Barcode: p.Barcode, Name: p.Name, Category: p.Category, Supplier: p.Supplier,
		BuyPrice: p.BuyPrice, RetailPrice: p.RetailPrice, WholesalePrice: p.WholesalePrice,
		BulkPrice: p.BulkPrice, Quantity: p.Quantity, MinQuantity: p.MinQuantity,
		Unit: p.Unit, ExpiryDate: p.ExpiryDate, BatchNumber: p.BatchNumber,
		ExpiryAlertDays: p.ExpiryAlertDays, IsExpiryTracked: p.IsExpiryTracked,
		IsWeighted: p.IsWeighted, ScalePLU: p.ScalePLU, PricePerKg: p.PricePerKg,
		ScaleBarcodeType: p.ScaleBarcodeType, ScaleEnabled: p.ScaleEnabled,
		ScalePrefixOverride: p.ScalePrefixOverride, IsActive: p.IsActive,
	}
}

func trigUnread(t *testing.T, s *store.Store, u *auth.User) int {
	t.Helper()
	n, err := s.UnreadNotificationCount(context.Background(), u)
	if err != nil {
		t.Fatalf("UnreadNotificationCount %s: %v", u.UserName, err)
	}
	return n
}

func trigInboxTypes(t *testing.T, s *store.Store, u *auth.User) []store.Notification {
	t.Helper()
	inbox, err := s.ListInbox(context.Background(), u, 50)
	if err != nil {
		t.Fatalf("ListInbox %s: %v", u.UserName, err)
	}
	return inbox
}

func trigMustSale(t *testing.T, s *store.Store, u *auth.User, customerID, pid int64, qty money.Qty, paid money.Cents) {
	t.Helper()
	if _, err := s.CreateInvoice(context.Background(), u, store.CreateInvoiceInput{
		CustomerID: customerID, SaleType: "retail", PaymentMethod: "Cash",
		Paid: paid, Lines: []store.SaleLineInput{{ProductID: pid, Qty: qty}},
	}); err != nil {
		t.Fatalf("CreateInvoice: %v", err)
	}
}

// --- G-8: price drop ---

func TestParityTriggersPriceDrop(t *testing.T) {
	s, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := trigAdmin()
	manager := trigMustUser(t, s, "trig-mgr", "manager")
	cashier := trigMustUser(t, s, "trig-cash", "cashier")

	p := trigMustProduct(t, s, admin, "سكر", 10000, 5000)
	custID, err := s.CreateCustomer(ctx, admin, store.CustomerInput{
		Name: "عميل اختبار", Phone: "01000000001", CreditLimit: 50000,
	})
	if err != nil {
		t.Fatalf("CreateCustomer: %v", err)
	}
	if _, err := s.OpenShift(ctx, cashier, 10000, false); err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	// The cashier sells 1 unit to a REGISTERED customer: the price-drop
	// audience is staff who previously sold the product (cash-customer
	// sales included — the audience is staff sellers, CustomerID 1 is
	// the cash customer and carries no username).
	trigMustSale(t, s, cashier, custID, p.ID, 1000, 2000)
	if n := trigUnread(t, s, cashier); n != 0 {
		t.Fatalf("sale itself must not notify: cashier unread = %d, want 0", n)
	}

	// Lower the retail price 2000 -> 1500.
	cur, err := s.GetProduct(ctx, admin, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	in := trigInputFromProduct(cur)
	in.RetailPrice = 1500
	if _, err := s.UpdateProduct(ctx, admin, p.ID, in); err != nil {
		t.Fatalf("UpdateProduct (lower): %v", err)
	}
	if n := trigUnread(t, s, cashier); n != 1 {
		t.Fatalf("after price drop, cashier unread = %d, want 1", n)
	}
	inbox := trigInboxTypes(t, s, cashier)
	if len(inbox) != 1 || inbox[0].Type != store.NotifPriceDrop || inbox[0].LinkedProductID != p.ID {
		t.Fatalf("cashier inbox = %+v, want 1 price_drop for product %d", inbox, p.ID)
	}
	// Staff who never sold the product get nothing.
	if n := trigUnread(t, s, admin); n != 0 {
		t.Fatalf("admin unread = %d, want 0 (never sold it)", n)
	}
	if n := trigUnread(t, s, manager); n != 0 {
		t.Fatalf("manager unread = %d, want 0 (never sold it)", n)
	}

	// Raising the price again fires nothing.
	cur, _ = s.GetProduct(ctx, admin, p.ID)
	in = trigInputFromProduct(cur)
	in.RetailPrice = 1800
	if _, err := s.UpdateProduct(ctx, admin, p.ID, in); err != nil {
		t.Fatalf("UpdateProduct (raise): %v", err)
	}
	if n := trigUnread(t, s, cashier); n != 1 {
		t.Fatalf("after price raise, cashier unread = %d, want 1 (no re-fire)", n)
	}
}

// --- G-8: offer ---

func TestParityTriggersOffer(t *testing.T) {
	s, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := trigAdmin()
	manager := trigMustUser(t, s, "trig-mgr", "manager")
	cashier := trigMustUser(t, s, "trig-cash", "cashier")
	users := []*auth.User{admin, manager, cashier}
	unreadAll := func() []int {
		out := make([]int, len(users))
		for i, u := range users {
			out[i] = trigUnread(t, s, u)
		}
		return out
	}
	wantAll := func(want int, what string) {
		t.Helper()
		for i, u := range users {
			if got := unreadAll()[i]; got != want {
				t.Fatalf("%s: %s unread = %d, want %d", what, u.UserName, got, want)
			}
		}
	}

	// Live offer created -> every active user gets one item.
	o1, err := s.CreateOffer(ctx, admin, store.OfferInput{
		Name: "عرض التوفير", Type: store.OfferInvoiceFixed, Amount: 500, IsActive: true,
	})
	if err != nil {
		t.Fatalf("CreateOffer live: %v", err)
	}
	wantAll(1, "after live create")
	inbox := trigInboxTypes(t, s, cashier)
	if len(inbox) != 1 || inbox[0].Type != store.NotifOffer {
		t.Fatalf("cashier inbox = %+v, want 1 offer item", inbox)
	}

	// An edit that only changes dates on an already-live offer must not
	// re-fire.
	if _, err := s.UpdateOffer(ctx, admin, o1.ID, store.OfferInput{
		Name: "عرض التوفير", Type: store.OfferInvoiceFixed, Amount: 500,
		IsActive: true, EndAt: "2999-12-31",
	}); err != nil {
		t.Fatalf("UpdateOffer dates-only: %v", err)
	}
	wantAll(1, "after dates-only edit")

	// Inactive offer created -> nothing; activation fires once;
	// deactivate + re-activate fires nothing new (dedupe per offer id).
	o2, err := s.CreateOffer(ctx, admin, store.OfferInput{
		Name: "عرض لاحق", Type: store.OfferInvoiceFixed, Amount: 300, IsActive: false,
	})
	if err != nil {
		t.Fatalf("CreateOffer inactive: %v", err)
	}
	wantAll(1, "after inactive create")
	if _, err := s.SetOfferActive(ctx, admin, o2.ID, true); err != nil {
		t.Fatalf("SetOfferActive(true): %v", err)
	}
	wantAll(2, "after activation")
	if _, err := s.SetOfferActive(ctx, admin, o2.ID, false); err != nil {
		t.Fatalf("SetOfferActive(false): %v", err)
	}
	if _, err := s.SetOfferActive(ctx, admin, o2.ID, true); err != nil {
		t.Fatalf("SetOfferActive(true) again: %v", err)
	}
	wantAll(2, "after re-activation (dedupe)")

	// Future offer created -> nothing; editing it live fires once.
	o3, err := s.CreateOffer(ctx, admin, store.OfferInput{
		Name: "عرض قادم", Type: store.OfferInvoiceFixed, Amount: 300,
		IsActive: true, StartAt: "2999-01-01",
	})
	if err != nil {
		t.Fatalf("CreateOffer future: %v", err)
	}
	wantAll(2, "after future create")
	if _, err := s.UpdateOffer(ctx, admin, o3.ID, store.OfferInput{
		Name: "عرض قادم", Type: store.OfferInvoiceFixed, Amount: 300, IsActive: true,
	}); err != nil {
		t.Fatalf("UpdateOffer newly-live: %v", err)
	}
	wantAll(3, "after edit newly-live")
}

// Product-targeted offer: only staff who previously sold the product.
func TestParityTriggersOfferProductTargeting(t *testing.T) {
	s, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := trigAdmin()
	manager := trigMustUser(t, s, "trig-mgr", "manager")
	cashier := trigMustUser(t, s, "trig-cash", "cashier")

	p := trigMustProduct(t, s, admin, "سكر", 10000, 5000)
	custID, err := s.CreateCustomer(ctx, admin, store.CustomerInput{
		Name: "عميل اختبار", Phone: "01000000001", CreditLimit: 50000,
	})
	if err != nil {
		t.Fatalf("CreateCustomer: %v", err)
	}
	if _, err := s.OpenShift(ctx, cashier, 10000, false); err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	trigMustSale(t, s, cashier, custID, p.ID, 1000, 2000)

	if _, err := s.CreateOffer(ctx, admin, store.OfferInput{
		Name: "خصم سكر", Type: store.OfferProductPct, Percent: 10, IsActive: true,
		Targets: []store.OfferTarget{{TargetType: "product", TargetID: p.ID}},
	}); err != nil {
		t.Fatalf("CreateOffer product-targeted: %v", err)
	}
	if n := trigUnread(t, s, cashier); n != 1 {
		t.Fatalf("previous seller unread = %d, want 1", n)
	}
	if n := trigUnread(t, s, admin); n != 0 {
		t.Fatalf("admin unread = %d, want 0 (never sold it)", n)
	}
	if n := trigUnread(t, s, manager); n != 0 {
		t.Fatalf("manager unread = %d, want 0 (never sold it)", n)
	}
}

// --- G-8: low stock, no spam ---

func TestParityTriggersLowStockCrossingNoSpam(t *testing.T) {
	s, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := trigAdmin()
	manager := trigMustUser(t, s, "trig-mgr", "manager")
	cashier := trigMustUser(t, s, "trig-cash", "cashier")

	p := trigMustProduct(t, s, admin, "سكر", 12000, 10000)
	if _, err := s.OpenShift(ctx, cashier, 10000, false); err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	// Sale 1: 12 -> 9 crosses the minimum (10): exactly one item each
	// for admin + manager, none for the cashier.
	trigMustSale(t, s, cashier, 1, p.ID, 3000, 6000)
	if n := trigUnread(t, s, admin); n != 1 {
		t.Fatalf("after crossing sale, admin unread = %d, want 1", n)
	}
	if n := trigUnread(t, s, manager); n != 1 {
		t.Fatalf("after crossing sale, manager unread = %d, want 1", n)
	}
	if n := trigUnread(t, s, cashier); n != 0 {
		t.Fatalf("after crossing sale, cashier unread = %d, want 0 (admin/manager only)", n)
	}
	inbox := trigInboxTypes(t, s, admin)
	if len(inbox) != 1 || inbox[0].Type != store.NotifLowStock || inbox[0].LinkedProductID != p.ID {
		t.Fatalf("admin inbox = %+v, want 1 low_stock for product %d", inbox, p.ID)
	}
	// Sale 2: 9 -> 8, still below minimum but no new crossing: no new
	// inbox item. (Without the call-site guard this would re-fire:
	// NotifyLowStock dedupes per product+quantity, and 8000 != 9000.)
	trigMustSale(t, s, cashier, 1, p.ID, 1000, 2000)
	if n := trigUnread(t, s, admin); n != 1 {
		t.Fatalf("after second sale below minimum, admin unread = %d, want 1 (no spam)", n)
	}
	if n := trigUnread(t, s, manager); n != 1 {
		t.Fatalf("after second sale below minimum, manager unread = %d, want 1 (no spam)", n)
	}
	// A product already below its minimum when sold never fires at all.
	p2 := trigMustProduct(t, s, admin, "أرز", 3000, 5000)
	trigMustSale(t, s, cashier, 1, p2.ID, 1000, 2000)
	if n := trigUnread(t, s, admin); n != 1 {
		t.Fatalf("sale of already-low product: admin unread = %d, want 1", n)
	}
}

// --- H-10: heartbeat ---

func TestParityHeartbeatKeepsActiveSessionAlive(t *testing.T) {
	e := newLockEnv(t)
	if err := e.st.SetSetting("auto_lock_minutes", "1"); err != nil {
		t.Fatal(err)
	}
	// The endpoint answers for an authenticated session.
	code, _, body := e.get("/api/activity")
	if code != http.StatusOK || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("GET /api/activity = %d body %q, want 200 {\"ok\":true}", code, body)
	}
	// Anonymous: 401, never a refresh.
	anon := &http.Client{}
	if code, _, _ := e.getAs(anon, "/api/activity"); code != http.StatusUnauthorized {
		t.Fatalf("anonymous GET /api/activity = %d, want 401", code)
	}
	// Activity inside the window keeps the session alive past the
	// point where it would otherwise have locked: backdate 50 s of a
	// 60 s timeout, ping, then a protected page still answers 200.
	e.backdate(50 * time.Second)
	if code, _, _ := e.get("/api/activity"); code != http.StatusOK {
		t.Fatalf("heartbeat after 50s idle = %d, want 200", code)
	}
	if code, _, _ := e.get("/"); code != http.StatusOK {
		t.Fatalf("page after heartbeat = %d, want 200 (session kept alive)", code)
	}
	// Inactivity still locks on schedule: 2 min idle, the heartbeat
	// itself gets 423 (it can never resurrect an expired session) and
	// the next page redirects to /lock.
	e.backdate(2 * time.Minute)
	if code, _, _ := e.get("/api/activity"); code != 423 {
		t.Fatalf("heartbeat when locked = %d, want 423", code)
	}
	code, loc, _ := e.get("/")
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/lock?next=") {
		t.Fatalf("idle GET / = %d loc %q, want 303 to /lock", code, loc)
	}
}

// The layout ships the heartbeat script (and keeps the 5 s orders poll).
func TestParityHeartbeatScriptInLayout(t *testing.T) {
	e := newLockEnv(t)
	_, _, body := e.get("/")
	if !strings.Contains(body, "/api/activity") {
		t.Fatal("rendered page has no /api/activity heartbeat call")
	}
	if !strings.Contains(body, "mousemove") || !strings.Contains(body, "keydown") {
		t.Fatal("rendered page heartbeat does not listen to mousemove/keydown")
	}
	if !strings.Contains(body, "pending-count") {
		t.Fatal("rendered page lost the 5 s orders poll")
	}
	if !strings.Contains(body, "beforeunload") {
		t.Fatal("rendered page lost the beforeunload handler")
	}
}
