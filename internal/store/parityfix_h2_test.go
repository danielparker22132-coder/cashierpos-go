package store

import (
	"context"
	"testing"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/money"
)

// Parity fix PR-H2 + PR-L2 tests.
//
// PR-H2: price-drop / offer triggers must fire for staff sellers whose
// only sales were to the seeded cash customer (Customers ID 1). The old
// audienceRecipients product branch filtered CustomerID<>1, which
// excluded exactly those sales.
// PR-L2: a trigger that reaches nobody must leave no 'sent' definition
// row behind (it would read as delivered in the admin manage view).
// Dedupe must survive the fix: it lives in NotificationSendLog
// UNIQUE(UserName, EventKey), not in the definition row.

func h2DefCount(t *testing.T, s *Store, typ string) int {
	t.Helper()
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM Notifications
		WHERE UserName='' AND Type=?`, typ).Scan(&n)
	if err != nil {
		t.Fatalf("def count %s: %v", typ, err)
	}
	return n
}

func h2AdminDefCount(t *testing.T, s *Store, admin *auth.User, typ string) int {
	t.Helper()
	list, err := s.ListNotificationsAdmin(context.Background(), admin)
	if err != nil {
		t.Fatalf("ListNotificationsAdmin: %v", err)
	}
	n := 0
	for _, row := range list {
		if row.Type == typ {
			n++
		}
	}
	return n
}

func h2DropPrice(t *testing.T, s *Store, admin *auth.User, p *Product, newRetail money.Cents) {
	t.Helper()
	cur, err := s.GetProduct(context.Background(), admin, p.ID)
	if err != nil {
		t.Fatalf("GetProduct: %v", err)
	}
	in := b1ProductInput(cur.Name)
	in.BuyPrice = cur.BuyPrice
	in.BulkPrice = cur.BulkPrice
	in.WholesalePrice = cur.WholesalePrice
	in.RetailPrice = newRetail
	in.Quantity = cur.Quantity
	in.MinQuantity = cur.MinQuantity
	in.Unit = cur.Unit
	in.ExpiryAlertDays = cur.ExpiryAlertDays
	in.ScaleBarcodeType = cur.ScaleBarcodeType
	in.IsActive = true
	if _, err := s.UpdateProduct(context.Background(), admin, p.ID, in); err != nil {
		t.Fatalf("UpdateProduct price drop: %v", err)
	}
}

// TestParityFixH2CashCustomerPriceDrop: cash-customer-only prior sale,
// then a 2000 -> 1500 price drop. The selling cashier must get the
// alert (unread 0 -> 1); the admin manage view must show exactly one
// price_drop definition.
func TestParityFixH2CashCustomerPriceDrop(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	cashier := b2TestUser(t, s, "h2cash", "cashier")
	p := b1MustProduct(t, s, admin, "سكر") // retail 2000, stock 10
	b1OpenShiftFor(t, s, cashier)

	// Cash-customer-only sale (CustomerID 1).
	b1CashCustomerSale(t, s, b1SaleEnv{ctx: ctx, admin: admin, cashier: cashier}, cashier, p.ID, 1000)
	if n := inboxCount(t, s, cashier); n != 0 {
		t.Fatalf("cashier unread before drop = %d, want 0", n)
	}

	h2DropPrice(t, s, admin, p, 1500)
	if n := inboxCount(t, s, cashier); n != 1 {
		t.Fatalf("cashier unread after drop = %d, want 1 (PR-H2)", n)
	}
	if n := h2DefCount(t, s, NotifPriceDrop); n != 1 {
		t.Fatalf("price_drop definition rows = %d, want 1", n)
	}
	if n := h2AdminDefCount(t, s, admin, NotifPriceDrop); n != 1 {
		t.Fatalf("admin list price_drop rows = %d, want 1", n)
	}
	// The inbox row is the price-drop alert for this product.
	inbox, err := s.ListInbox(ctx, cashier, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 || inbox[0].Type != NotifPriceDrop || inbox[0].LinkedProductID != p.ID {
		t.Fatalf("inbox = %+v, want 1 price_drop row for product %d", inbox, p.ID)
	}
}

// TestParityFixH2NamedCustomerPriceDrop: same flow for a named customer
// (the pre-fix working path) — must keep working after the filter removal.
func TestParityFixH2NamedCustomerPriceDrop(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	cashier := b2TestUser(t, s, "h2cash2", "cashier")
	p := b1MustProduct(t, s, admin, "أرز")
	b1OpenShiftFor(t, s, cashier)

	namedID := testCustomer(t, s, "عميل مسجل")
	res, err := s.CreateInvoice(ctx, cashier, CreateInvoiceInput{
		CustomerID: namedID, SaleType: "retail", PaymentMethod: "Cash",
		Lines: []SaleLineInput{{ProductID: p.ID, Qty: 1000}},
	})
	if err != nil {
		t.Fatalf("named-customer sale: %v", err)
	}
	_ = res

	h2DropPrice(t, s, admin, p, 1500)
	if n := inboxCount(t, s, cashier); n != 1 {
		t.Fatalf("cashier unread after drop = %d, want 1", n)
	}
}

// TestParityFixH2CashCustomerOffer: a live offer targeting a product
// whose only sale was to the cash customer must reach the seller.
func TestParityFixH2CashCustomerOffer(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	cashier := b2TestUser(t, s, "h2cash3", "cashier")
	p := b1MustProduct(t, s, admin, "زيت")
	b1OpenShiftFor(t, s, cashier)

	b1CashCustomerSale(t, s, b1SaleEnv{ctx: ctx, admin: admin, cashier: cashier}, cashier, p.ID, 1000)

	offer, err := s.CreateOffer(ctx, admin, OfferInput{
		Name: "عرض الزيت", Type: OfferProductPct, Percent: 10, IsActive: true,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p.ID}},
	})
	if err != nil {
		t.Fatalf("CreateOffer live: %v", err)
	}
	_ = offer
	if n := inboxCount(t, s, cashier); n != 1 {
		t.Fatalf("cashier unread after live offer = %d, want 1 (PR-H2)", n)
	}
	if n := inboxCount(t, s, admin); n != 0 {
		t.Fatalf("admin unread after product-targeted offer = %d, want 0 (never sold it)", n)
	}
	if n := h2DefCount(t, s, NotifOffer); n != 1 {
		t.Fatalf("offer definition rows = %d, want 1", n)
	}
}

// TestParityFixL2EmptyDefinitionSkipped covers the PR-L2 shape and
// re-verifies every dedupe case the fix could regress:
//   - price drop with no prior sellers: sent 0, no definition row.
//   - same price drop repeated: deduped, sent 0, still exactly one
//     definition row (the first, real one) — no phantom second row.
//   - low-stock same product + same quantity: deduped, no new row;
//     fresh lower quantity: fires, new row.
//   - offer create-inactive: nothing; edit newly-live: fires once;
//     live -> live edit: nothing; re-activation: nothing (per-offer
//     dedupe), and no phantom definition row from the re-activation.
//   - live offer targeting a product nobody sold: sent 0, no row.
func TestParityFixL2EmptyDefinitionSkipped(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	// Use the seeded admin (testAdmin) as the actor: b1Admin would insert
	// a second admin user and low-stock counts admin+manager only, so
	// the expected recipient count here is exactly 2.
	admin := testAdmin()
	cashier := b2TestUser(t, s, "h2cash4", "cashier")
	manager := b2TestUser(t, s, "h2mgr", "manager")
	p := b1MustProduct(t, s, admin, "سكر") // retail 2000
	b1OpenShiftFor(t, s, cashier)

	// 1. Price drop with no prior sellers: sent 0, no definition row.
	sent, err := s.NotifyPriceDrop(ctx, admin, p.ID, "سكر", 2000, 1500)
	if err != nil {
		t.Fatalf("NotifyPriceDrop (no sellers): %v", err)
	}
	if sent != 0 {
		t.Fatalf("sent = %d, want 0 (no prior sellers)", sent)
	}
	if n := h2DefCount(t, s, NotifPriceDrop); n != 0 {
		t.Fatalf("price_drop definition rows = %d, want 0 (PR-L2)", n)
	}
	if n := h2AdminDefCount(t, s, admin, NotifPriceDrop); n != 0 {
		t.Fatalf("admin list price_drop rows = %d, want 0 (PR-L2)", n)
	}

	// 2. Cash-customer sale, then the same drop: fires once.
	b1CashCustomerSale(t, s, b1SaleEnv{ctx: ctx, admin: admin, cashier: cashier}, cashier, p.ID, 1000)
	sent, err = s.NotifyPriceDrop(ctx, admin, p.ID, "سكر", 2000, 1500)
	if err != nil {
		t.Fatal(err)
	}
	if sent != 1 {
		t.Fatalf("sent = %d, want 1 after cash sale", sent)
	}
	if n := h2DefCount(t, s, NotifPriceDrop); n != 1 {
		t.Fatalf("price_drop definition rows = %d, want 1", n)
	}

	// 3. Same drop repeated: deduped — sent 0 and NO new definition row.
	sent, err = s.NotifyPriceDrop(ctx, admin, p.ID, "سكر", 2000, 1500)
	if err != nil {
		t.Fatal(err)
	}
	if sent != 0 {
		t.Fatalf("repeat drop sent = %d, want 0 (dedupe)", sent)
	}
	if n := h2DefCount(t, s, NotifPriceDrop); n != 1 {
		t.Fatalf("price_drop definition rows after deduped repeat = %d, want 1 (PR-L2)", n)
	}
	if n := inboxCount(t, s, cashier); n != 1 {
		t.Fatalf("cashier unread after deduped repeat = %d, want 1", n)
	}

	// 4. Low stock: fires to admin+manager, same qty dedupes with no
	// new row, fresh lower qty fires again.
	sent, err = s.NotifyLowStock(ctx, admin, p.ID, "سكر", 1000, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if sent != 2 {
		t.Fatalf("low-stock sent = %d, want 2 (admin+manager)", sent)
	}
	if n := h2DefCount(t, s, NotifLowStock); n != 1 {
		t.Fatalf("low_stock definition rows = %d, want 1", n)
	}
	sent, err = s.NotifyLowStock(ctx, admin, p.ID, "سكر", 1000, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if sent != 0 {
		t.Fatalf("low-stock repeat sent = %d, want 0 (dedupe)", sent)
	}
	if n := h2DefCount(t, s, NotifLowStock); n != 1 {
		t.Fatalf("low_stock definition rows after deduped repeat = %d, want 1 (PR-L2)", n)
	}
	sent, err = s.NotifyLowStock(ctx, admin, p.ID, "سكر", 500, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if sent != 2 {
		t.Fatalf("low-stock lower-qty sent = %d, want 2", sent)
	}
	if n := h2DefCount(t, s, NotifLowStock); n != 2 {
		t.Fatalf("low_stock definition rows after fresh crossing = %d, want 2", n)
	}
	_ = manager

	// 5. Offer lifecycle on a product nobody sold: create-inactive
	// fires nothing and leaves no row.
	offer, err := s.CreateOffer(ctx, admin, OfferInput{
		Name: "عرض خامل", Type: OfferProductPct, Percent: 10, IsActive: false,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p.ID}},
	})
	if err != nil {
		t.Fatalf("CreateOffer inactive: %v", err)
	}
	if n := h2DefCount(t, s, NotifOffer); n != 0 {
		t.Fatalf("offer definition rows after inactive create = %d, want 0", n)
	}

	// 6. Edit newly-live: fires once — but the product audience is
	// empty for offers? No: cashier sold p (step 2), so it fires to
	// the cashier exactly once.
	if _, err := s.UpdateOffer(ctx, admin, offer.ID, OfferInput{
		Name: "عرض خامل", Type: OfferProductPct, Percent: 10, IsActive: true,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p.ID}},
	}); err != nil {
		t.Fatalf("UpdateOffer newly-live: %v", err)
	}
	if n := h2DefCount(t, s, NotifOffer); n != 1 {
		t.Fatalf("offer definition rows after newly-live edit = %d, want 1", n)
	}
	// Cashier had 1 (price drop); the offer adds exactly one more.
	if n := inboxCount(t, s, cashier); n != 2 {
		t.Fatalf("cashier unread after newly-live offer = %d, want 2", n)
	}

	// 7. Live -> live edit: nothing new, no new row.
	if _, err := s.UpdateOffer(ctx, admin, offer.ID, OfferInput{
		Name: "عرض خامل معدل", Type: OfferProductPct, Percent: 10, IsActive: true,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p.ID}},
	}); err != nil {
		t.Fatalf("UpdateOffer live->live: %v", err)
	}
	if n := h2DefCount(t, s, NotifOffer); n != 1 {
		t.Fatalf("offer definition rows after live->live edit = %d, want 1", n)
	}

	// 8. Re-activation (deactivate then re-activate): per-offer dedupe
	// sends nothing, and must leave NO phantom definition row.
	if _, err := s.SetOfferActive(ctx, admin, offer.ID, false); err != nil {
		t.Fatalf("SetOfferActive false: %v", err)
	}
	if _, err := s.SetOfferActive(ctx, admin, offer.ID, true); err != nil {
		t.Fatalf("SetOfferActive true: %v", err)
	}
	if n := h2DefCount(t, s, NotifOffer); n != 1 {
		t.Fatalf("offer definition rows after re-activation = %d, want 1 (PR-L2)", n)
	}
	if n := inboxCount(t, s, cashier); n != 2 {
		t.Fatalf("cashier unread after re-activation = %d, want 2 (dedupe)", n)
	}

	// 9. Live offer targeting a product nobody ever sold: sent 0,
	// no definition row.
	unsold := b1MustProduct(t, s, admin, "شاي")
	if _, err := s.CreateOffer(ctx, admin, OfferInput{
		Name: "عرض الشاي", Type: OfferProductPct, Percent: 10, IsActive: true,
		Targets: []OfferTarget{{TargetType: "product", TargetID: unsold.ID}},
	}); err != nil {
		t.Fatalf("CreateOffer unsold product: %v", err)
	}
	if n := h2DefCount(t, s, NotifOffer); n != 1 {
		t.Fatalf("offer definition rows after empty-audience offer = %d, want 1 (PR-L2)", n)
	}
	if n := inboxCount(t, s, cashier); n != 2 {
		t.Fatalf("cashier unread after empty-audience offer = %d, want 2", n)
	}
}

// TestParityFixL2ManualCreateToNobody: manual creation with a product
// audience nobody sold, and a duplicate custom event key, must both
// leave no extra definition row behind.
func TestParityFixL2ManualCreateToNobody(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	cashier := b2TestUser(t, s, "h2cash5", "cashier")
	_ = cashier

	// Product audience, no sellers at all.
	id, err := s.CreateNotification(ctx, admin, NotificationInput{
		Type: NotifCustom, Title: "لا أحد", Body: "x",
		Audience: "product:999999",
	})
	// Audience validation rejects unknown product ids only when the
	// format is bad; a well-formed but seller-less audience delivers
	// to nobody. Either an error or a 0 id with no row is acceptable —
	// what must NOT happen is a 'sent' row with zero recipients.
	if err == nil {
		if id != 0 {
			t.Fatalf("CreateNotification to nobody returned id %d, want 0 (PR-L2)", id)
		}
		if n := h2DefCount(t, s, NotifCustom); n != 0 {
			t.Fatalf("custom definition rows = %d, want 0 (PR-L2)", n)
		}
	}

	// Duplicate custom event key: first create delivers, second is
	// fully deduped and leaves no second definition row.
	if _, err := s.CreateNotification(ctx, admin, NotificationInput{
		Type: NotifCustom, Title: "أول", Body: "x", Audience: "all", EventKey: "h2-evt-1",
	}); err != nil {
		t.Fatalf("first custom create: %v", err)
	}
	if n := h2DefCount(t, s, NotifCustom); n != 1 {
		t.Fatalf("custom definition rows after first = %d, want 1", n)
	}
	id2, err := s.CreateNotification(ctx, admin, NotificationInput{
		Type: NotifCustom, Title: "مكرر", Body: "x", Audience: "all", EventKey: "h2-evt-1",
	})
	if err != nil {
		t.Fatalf("duplicate custom create: %v", err)
	}
	if id2 != 0 {
		t.Fatalf("duplicate custom create returned id %d, want 0 (PR-L2)", id2)
	}
	if n := h2DefCount(t, s, NotifCustom); n != 1 {
		t.Fatalf("custom definition rows after duplicate = %d, want 1 (PR-L2)", n)
	}
	if n := inboxCount(t, s, cashier); n != 1 {
		t.Fatalf("cashier unread after duplicate = %d, want 1 (dedupe)", n)
	}
}
