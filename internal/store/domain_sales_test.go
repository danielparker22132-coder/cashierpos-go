package store

import (
	"context"
	"strings"
	"testing"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/money"
)

// testProduct restores the shared test helper (removed mid-build) that
// the vendors tests call: insert a product and return its id.
func testProduct(t *testing.T, s *Store, name string, buy, retail money.Cents, qty money.Qty) int64 {
	t.Helper()
	return testProductB2(t, s, name, buy, retail, qty)
}

// testUser restores the shared test helper (removed mid-build) that
// several domain test files call: insert a user and return it.
func testUser(t *testing.T, s *Store, username, role string) *auth.User {
	t.Helper()
	return b2TestUser(t, s, username, role)
}

type b1SaleEnv struct {
	ctx     context.Context
	admin   *auth.User
	manager *auth.User
	cashier *auth.User
	sup     *auth.User
}

func b1SaleEnvSetup(t *testing.T, s *Store) b1SaleEnv {
	t.Helper()
	return b1SaleEnv{
		ctx:     context.Background(),
		admin:   b1Admin(t, s),
		manager: b2TestUser(t, s, "b1mgr", "manager"),
		cashier: b2TestUser(t, s, "b1cashier", "cashier"),
		sup:     b2TestUser(t, s, "b1sup2", "supervisor"),
	}
}

func b1OpenShiftFor(t *testing.T, s *Store, u *auth.User) int64 {
	t.Helper()
	return testOpenShift(t, s, u, 10000) // opening 100.00 EGP
}

func b1CashCustomerSale(t *testing.T, s *Store, env b1SaleEnv, u *auth.User, pid int64, qty money.Qty) *InvoiceResult {
	t.Helper()
	res, err := s.CreateInvoice(env.ctx, u, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
		Lines: []SaleLineInput{{ProductID: pid, Qty: qty}},
	})
	if err != nil {
		t.Fatalf("sale: %v", err)
	}
	return res
}

func TestB1FullSaleCashCustomer(t *testing.T) {
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	p := b1MustProduct(t, s, env.admin, "سكر") // retail 2000, buy 1000, stock 10
	shiftID := b1OpenShiftFor(t, s, env.cashier)

	res := b1CashCustomerSale(t, s, env, env.cashier, p.ID, 2000)
	if res.InvoiceNumber != 1 || res.Net != 4000 || res.Paid != 4000 || res.Remaining != 0 {
		t.Fatalf("sale result = %+v", res)
	}
	if res.Profit != 2000 { // (2000-1000)*2
		t.Fatalf("profit = %d, want 2000", res.Profit)
	}
	if res.ShiftID != shiftID {
		t.Fatalf("shift = %d, want %d", res.ShiftID, shiftID)
	}
	// Stock decremented 10 -> 8.
	got, _ := s.GetProduct(env.ctx, env.admin, p.ID)
	if got.Quantity != 8000 {
		t.Fatalf("stock = %d, want 8000", got.Quantity)
	}
	// Payments row + CashBox 'in' 4000.
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM Payments WHERE InvoiceID=? AND Amount=4000`, res.InvoiceID).Scan(&n)
	if n != 1 {
		t.Fatal("payments row missing")
	}
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM CashBox WHERE ShiftID=? AND MovementType='in' AND Amount=4000`, shiftID).Scan(&n)
	if n != 1 {
		t.Fatal("cashbox in missing")
	}
	// StockMovements 'sale' -2.
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM StockMovements WHERE ProductID=? AND MovementType='sale' AND Quantity=-2000`, p.ID).Scan(&n)
	if n != 1 {
		t.Fatal("sale movement missing")
	}
	// Audit row exists.
	if auditCount(t, s, "إنشاء فاتورة") != 1 {
		t.Fatal("sale audit missing")
	}
	// Second invoice gets number 2 (never reused).
	res2 := b1CashCustomerSale(t, s, env, env.cashier, p.ID, 1000)
	if res2.InvoiceNumber != 2 {
		t.Fatalf("second invoice number = %d, want 2", res2.InvoiceNumber)
	}
	// GetInvoice + list.
	inv, err := s.GetInvoice(env.ctx, env.admin, res.InvoiceID)
	if err != nil || len(inv.Lines) != 1 || inv.Lines[0].Qty != 2000 {
		t.Fatalf("get invoice: %+v %v", inv, err)
	}
	list, err := s.ListInvoices(env.ctx, env.admin, Today(), Today(), 0)
	if err != nil || len(list) != 2 {
		t.Fatalf("list invoices: %+v %v", list, err)
	}
	// Profit visible to admin, hidden from cashier.
	if _, err := s.InvoiceProfit(env.ctx, env.admin, res.InvoiceID); err != nil {
		t.Fatalf("admin profit: %v", err)
	}
	if _, err := s.InvoiceProfit(env.ctx, env.cashier, res.InvoiceID); err == nil {
		t.Fatal("cashier must not see profit")
	}
}

func TestB1SaleRequiresShiftAndMergesDuplicates(t *testing.T) {
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	p := b1MustProduct(t, s, env.admin, "سكر")
	// No shift -> refused.
	_, err := s.CreateInvoice(env.ctx, env.cashier, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
		Lines: []SaleLineInput{{ProductID: p.ID, Qty: 1000}},
	})
	if err == nil || !strings.Contains(err.Error(), "شيفت مفتوح") {
		t.Fatalf("no-shift err = %v", err)
	}
	b1OpenShiftFor(t, s, env.cashier)
	// Duplicate lines merge into one detail line (qty 3).
	res, err := s.CreateInvoice(env.ctx, env.cashier, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
		Lines: []SaleLineInput{
			{ProductID: p.ID, Qty: 1000},
			{ProductID: p.ID, Qty: 2000},
		},
	})
	if err != nil {
		t.Fatalf("merged sale: %v", err)
	}
	inv, _ := s.GetInvoice(env.ctx, env.admin, res.InvoiceID)
	if len(inv.Lines) != 1 || inv.Lines[0].Qty != 3000 {
		t.Fatalf("merged lines = %+v", inv.Lines)
	}
	// Empty cart refused.
	_, err = s.CreateInvoice(env.ctx, env.cashier, CreateInvoiceInput{CustomerID: 1, SaleType: "retail"})
	if err == nil || !strings.Contains(err.Error(), "لا يوجد أصناف") {
		t.Fatalf("empty cart err = %v", err)
	}
	// Warehouse cannot sell.
	wh := b2TestUser(t, s, "b1wh2", "warehouse")
	_, err = s.CreateInvoice(env.ctx, wh, CreateInvoiceInput{CustomerID: 1, SaleType: "retail",
		Lines: []SaleLineInput{{ProductID: p.ID, Qty: 1000}}})
	if err == nil {
		t.Fatal("warehouse sale should be denied")
	}
}

func TestB1SaleStockAndExpiryGuards(t *testing.T) {
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	p := b1MustProduct(t, s, env.admin, "سكر") // stock 10
	b1OpenShiftFor(t, s, env.cashier)
	// Over stock.
	_, err := s.CreateInvoice(env.ctx, env.cashier, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
		Lines: []SaleLineInput{{ProductID: p.ID, Qty: 11000}},
	})
	if err == nil || !strings.Contains(err.Error(), "تتجاوز المخزون") {
		t.Fatalf("over stock err = %v", err)
	}
	// Two lines of the same product that together exceed stock.
	_, err = s.CreateInvoice(env.ctx, env.cashier, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
		Lines: []SaleLineInput{
			{ProductID: p.ID, Qty: 6000},
			{ProductID: p.ID, Qty: 5000, UnitPrice: 2500}, // different price, admin-only price edit
		},
	})
	if err == nil {
		t.Fatal("combined over-stock (or price edit) should fail")
	}
	// Expired product blocked.
	exp := b1MustProduct(t, s, env.admin, "لبن")
	upd := b1ProductInput("لبن")
	upd.ExpiryDate = addDays(Today(), -1)
	upd.Quantity = 10000
	upd.MinQuantity = 5000
	if _, err := s.UpdateProduct(env.ctx, env.admin, exp.ID, upd); err != nil {
		t.Fatalf("set expiry: %v", err)
	}
	_, err = s.CreateInvoice(env.ctx, env.cashier, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
		Lines: []SaleLineInput{{ProductID: exp.ID, Qty: 1000}},
	})
	if err == nil || !strings.Contains(err.Error(), "منتهي الصلاحية") {
		t.Fatalf("expired err = %v", err)
	}
	if auditCount(t, s, "منع بيع منتج منتهي") != 1 {
		t.Fatal("expired-sale audit missing")
	}
	// Near expiry warns but sells.
	near := b1MustProduct(t, s, env.admin, "زبادي")
	upd2 := b1ProductInput("زبادي")
	upd2.ExpiryDate = addDays(Today(), 5)
	upd2.Quantity = 10000
	upd2.MinQuantity = 5000
	if _, err := s.UpdateProduct(env.ctx, env.admin, near.ID, upd2); err != nil {
		t.Fatalf("set near expiry: %v", err)
	}
	res, err := s.CreateInvoice(env.ctx, env.cashier, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
		Lines: []SaleLineInput{{ProductID: near.ID, Qty: 1000}},
	})
	if err != nil {
		t.Fatalf("near-expiry sale: %v", err)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "تنبيه صلاحية") {
		t.Fatalf("warnings = %+v", res.Warnings)
	}
}

func TestB1SaleDiscountsAndPriceEdit(t *testing.T) {
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	p := b1MustProduct(t, s, env.admin, "سكر")
	b1OpenShiftFor(t, s, env.cashier)
	// Cashier invoice discount forced 0: any positive value refused.
	_, err := s.CreateInvoice(env.ctx, env.cashier, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash", InvoiceDiscount: 500,
		Lines: []SaleLineInput{{ProductID: p.ID, Qty: 1000}},
	})
	if err == nil {
		t.Fatal("cashier invoice discount should be refused")
	}
	// Cashier item discount refused.
	_, err = s.CreateInvoice(env.ctx, env.cashier, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
		Lines: []SaleLineInput{{ProductID: p.ID, Qty: 1000, ItemDiscount: 100}},
	})
	if err == nil {
		t.Fatal("cashier item discount should be refused")
	}
	// Supervisor invoice discount works; item discount capped at line gross.
	// FIX-2 M-3: the capped discount leaves a net of 0, which is below
	// BuyPrice, so the sale needs AllowBelowCost (the capping behaviour
	// itself is unchanged: net 0, discount 2000).
	b1OpenShiftFor(t, s, env.sup)
	res, err := s.CreateInvoice(env.ctx, env.sup, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash", InvoiceDiscount: 500,
		AllowBelowCost: true,
		Lines:          []SaleLineInput{{ProductID: p.ID, Qty: 1000, ItemDiscount: 99999}},
	})
	if err != nil {
		t.Fatalf("supervisor discount sale: %v", err)
	}
	// Line gross 2000, item discount capped at 2000; the invoice discount
	// is then capped at gross - item discounts = 0, so net is 0.
	if res.Net != 0 || res.Discount != 2000 {
		t.Fatalf("capped discount result = %+v", res)
	}
	// Cashier custom price refused; admin custom price allowed.
	_, err = s.CreateInvoice(env.ctx, env.cashier, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
		Lines: []SaleLineInput{{ProductID: p.ID, Qty: 1000, UnitPrice: 2500}},
	})
	if err == nil {
		t.Fatal("cashier price edit should be refused")
	}
	b1OpenShiftFor(t, s, env.admin)
	res, err = s.CreateInvoice(env.ctx, env.admin, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
		Lines: []SaleLineInput{{ProductID: p.ID, Qty: 1000, UnitPrice: 1500}},
	})
	if err != nil {
		t.Fatalf("admin price edit: %v", err)
	}
	if res.Net != 1500 {
		t.Fatalf("admin edited price net = %d, want 1500", res.Net)
	}
	// Wholesale repricing: 1500/unit.
	res, err = s.CreateInvoice(env.ctx, env.admin, CreateInvoiceInput{
		CustomerID: 1, SaleType: "wholesale", PaymentMethod: "Cash",
		Lines: []SaleLineInput{{ProductID: p.ID, Qty: 2000}},
	})
	if err != nil {
		t.Fatalf("wholesale: %v", err)
	}
	if res.Net != 3000 {
		t.Fatalf("wholesale net = %d, want 3000", res.Net)
	}
	// Paid above net refused (registered customer, partial pay).
	custID := testCustomer(t, s, "عميل آجل")
	_, err = s.CreateInvoice(env.ctx, env.admin, CreateInvoiceInput{
		CustomerID: custID, SaleType: "retail", PaymentMethod: "Cash", Paid: 999999,
		Lines: []SaleLineInput{{ProductID: p.ID, Qty: 1000}},
	})
	if err == nil || !strings.Contains(err.Error(), "المبلغ المدفوع غير صحيح") {
		t.Fatalf("overpay err = %v", err)
	}
}

func TestB1SaleCreditLimit(t *testing.T) {
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	p := b1MustProduct(t, s, env.admin, "سكر") // retail 2000
	custID := testCustomer(t, s, "عميل آجل")
	if _, err := s.DB.Exec(`UPDATE Customers SET CreditLimit=5000 WHERE CustomerID=?`, custID); err != nil {
		t.Fatal(err)
	}
	b1OpenShiftFor(t, s, env.cashier)
	b1OpenShiftFor(t, s, env.manager)
	// The cash customer always pays the full net automatically (S-19),
	// so a cash sale can never leave a remaining debt.
	_, err := s.CreateInvoice(env.ctx, env.cashier, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash", Paid: 0,
		Lines: []SaleLineInput{{ProductID: p.ID, Qty: 1000}},
	})
	if err != nil {
		t.Fatalf("cash customer auto-pays in full: %v", err)
	}
	// Remaining 4000 <= limit 5000: ok for cashier (partial pay 0? paid must be <= net;
	// use Visa so no CashBox leg, paid 0 -> remaining 4000).
	_, err = s.CreateInvoice(env.ctx, env.cashier, CreateInvoiceInput{
		CustomerID: custID, SaleType: "retail", PaymentMethod: "Visa", Paid: 0,
		Lines: []SaleLineInput{{ProductID: p.ID, Qty: 2000}},
	})
	if err != nil {
		t.Fatalf("within-limit credit: %v", err)
	}
	// Live balance is now 4000; another 2000-unit sale (4000) would total 8000 > 5000.
	_, err = s.CreateInvoice(env.ctx, env.cashier, CreateInvoiceInput{
		CustomerID: custID, SaleType: "retail", PaymentMethod: "Visa", Paid: 0,
		Lines: []SaleLineInput{{ProductID: p.ID, Qty: 2000}},
	})
	if err == nil || err.Error() != "تجاوز حد الائتمان يحتاج موافقة المدير" {
		t.Fatalf("over-limit cashier err = %v", err)
	}
	// Supervisor also refused (only Manager/Admin approve).
	b1OpenShiftFor(t, s, env.sup)
	_, err = s.CreateInvoice(env.ctx, env.sup, CreateInvoiceInput{
		CustomerID: custID, SaleType: "retail", PaymentMethod: "Visa", Paid: 0, AllowOverLimit: true,
		Lines: []SaleLineInput{{ProductID: p.ID, Qty: 2000}},
	})
	if err == nil {
		t.Fatal("supervisor override should be denied")
	}
	// Manager with the flag succeeds and writes the override audit row.
	res, err := s.CreateInvoice(env.ctx, env.manager, CreateInvoiceInput{
		CustomerID: custID, SaleType: "retail", PaymentMethod: "Visa", Paid: 0, AllowOverLimit: true,
		Lines: []SaleLineInput{{ProductID: p.ID, Qty: 2000}},
	})
	if err != nil {
		t.Fatalf("manager override: %v", err)
	}
	if res.Remaining != 4000 {
		t.Fatalf("override remaining = %d", res.Remaining)
	}
	if auditCount(t, s, "تجاوز حد الائتمان") != 1 {
		t.Fatal("override audit missing")
	}
	// Stored balance synced to the live sum (4000 + 4000 = 8000).
	var bal money.Cents
	_ = s.DB.QueryRow(`SELECT Balance FROM Customers WHERE CustomerID=?`, custID).Scan(&bal)
	if bal != 8000 {
		t.Fatalf("customer balance = %d, want 8000", bal)
	}
}

func TestB1CancelInvoice(t *testing.T) {
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	p := b1MustProduct(t, s, env.admin, "سكر")
	custID := testCustomer(t, s, "عميل آجل")
	shiftID := b1OpenShiftFor(t, s, env.cashier)
	// Credit sale: 2 units, paid 1000 Visa, remaining 3000.
	res, err := s.CreateInvoice(env.ctx, env.cashier, CreateInvoiceInput{
		CustomerID: custID, SaleType: "retail", PaymentMethod: "Visa", Paid: 1000,
		Lines: []SaleLineInput{{ProductID: p.ID, Qty: 2000}},
	})
	if err != nil {
		t.Fatalf("credit sale: %v", err)
	}
	if res.Remaining != 3000 {
		t.Fatalf("remaining = %d", res.Remaining)
	}
	// Cashier cannot cancel; reason required.
	if _, err := s.CancelInvoice(env.ctx, env.cashier, res.InvoiceID, "خطأ"); err == nil {
		t.Fatal("cashier cancel should be denied")
	}
	if _, err := s.CancelInvoice(env.ctx, env.admin, res.InvoiceID, ""); err == nil {
		t.Fatal("cancel without reason should be refused")
	}
	// Admin cancels: stock restored 8 -> 10, debt reversed, no cash refund (Visa).
	refund, err := s.CancelInvoice(env.ctx, env.admin, res.InvoiceID, "فاتورة خاطئة")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if refund != 0 {
		t.Fatalf("visa cancel refund = %d, want 0", refund)
	}
	got, _ := s.GetProduct(env.ctx, env.admin, p.ID)
	if got.Quantity != 10000 {
		t.Fatalf("stock after cancel = %d, want 10000", got.Quantity)
	}
	var bal money.Cents
	_ = s.DB.QueryRow(`SELECT Balance FROM Customers WHERE CustomerID=?`, custID).Scan(&bal)
	if bal != 0 {
		t.Fatalf("balance after cancel = %d, want 0", bal)
	}
	// Double cancel refused.
	if _, err := s.CancelInvoice(env.ctx, env.admin, res.InvoiceID, "مرة أخرى"); err == nil {
		t.Fatal("double cancel should fail")
	}
	// Cash sale cancel refunds the cash via CashBox 'out'.
	res2 := b1CashCustomerSale(t, s, env, env.cashier, p.ID, 1000)
	refund, err = s.CancelInvoice(env.ctx, env.manager, res2.InvoiceID, "إلغاء نقدي")
	if err != nil {
		t.Fatalf("cash cancel: %v", err)
	}
	if refund != 2000 {
		t.Fatalf("cash refund = %d, want 2000", refund)
	}
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM CashBox WHERE ShiftID=? AND MovementType='out' AND Amount=2000`, shiftID).Scan(&n)
	if n != 1 {
		t.Fatal("cancel cashbox out missing")
	}
	inv, _ := s.GetInvoice(env.ctx, env.admin, res2.InvoiceID)
	if !inv.Summary.IsCanceled {
		t.Fatal("invoice should be canceled")
	}
}

func TestB1SaleWithBxgyOffer(t *testing.T) {
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	p := b1MustProduct(t, s, env.admin, "سكر") // retail 2000, buy 1000, stock 10
	b1CreateOffer(t, s, OfferInput{Name: "اشتر 2 واحصل على 1", Type: OfferBuyXGetY,
		BuyX: 2, FreeY: 1, IsActive: true,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p.ID}}})
	b1OpenShiftFor(t, s, env.cashier)
	res := b1CashCustomerSale(t, s, env, env.cashier, p.ID, 3000)
	if res.Net != 6000 {
		t.Fatalf("bxgy net = %d, want 6000 (free unit does not reduce net)", res.Net)
	}
	if res.FreeUnits[p.ID] != 1000 || res.FreeCost != 1000 {
		t.Fatalf("bxgy free = %+v cost %d", res.FreeUnits, res.FreeCost)
	}
	// Stock: 10 - 3 paid - 1 free = 6.
	got, _ := s.GetProduct(env.ctx, env.admin, p.ID)
	if got.Quantity != 6000 {
		t.Fatalf("stock after bxgy = %d, want 6000", got.Quantity)
	}
	// Profit reduced by the free unit's buy cost: (2000-1000)*3 - 1000 = 2000.
	if res.Profit != 2000 {
		t.Fatalf("bxgy profit = %d, want 2000", res.Profit)
	}
	// InvoiceOffers row + promo audit.
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM InvoiceOffers WHERE InvoiceID=?`, res.InvoiceID).Scan(&n)
	if n != 1 {
		t.Fatal("invoice offer row missing")
	}
	if auditCount(t, s, "تطبيق عرض ترويجي") != 1 {
		t.Fatal("promo audit missing")
	}
	// Cancel restores paid + earned free units (10 -> back).
	if _, err := s.CancelInvoice(env.ctx, env.admin, res.InvoiceID, "إلغاء"); err != nil {
		t.Fatalf("cancel bxgy: %v", err)
	}
	got, _ = s.GetProduct(env.ctx, env.admin, p.ID)
	if got.Quantity != 10000 {
		t.Fatalf("stock after bxgy cancel = %d, want 10000", got.Quantity)
	}
}

func TestB1WeightedSaleFromScaleBarcode(t *testing.T) {
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	in := b1ProductInput("جبنة رومي")
	in.IsWeighted = true
	in.ScalePLU = "123"
	in.PricePerKg = 2000
	in.ScaleEnabled = true
	in.Quantity = 50000
	p, err := s.CreateProduct(env.ctx, env.admin, in)
	if err != nil {
		t.Fatalf("create weighted: %v", err)
	}
	code := b1ScaleBarcode("00123", "01500") // 1.500 kg
	dec, err := s.DecodeScaleBarcode(env.ctx, code)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	b1OpenShiftFor(t, s, env.cashier)
	res, err := s.CreateInvoice(env.ctx, env.cashier, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
		Lines: []SaleLineInput{{ProductID: p.ID, Qty: dec.Weight, IsWeighted: true,
			UnitPrice: dec.TotalPrice / 1500 * 1000, ScaleBarcode: code, ScalePLU: "123"}},
	})
	// UnitPrice above is PricePerKg (2000) computed as total/weight; simpler check:
	if err != nil {
		t.Fatalf("weighted sale: %v", err)
	}
	if res.Net != 3000 {
		t.Fatalf("weighted net = %d, want 3000", res.Net)
	}
	got, _ := s.GetProduct(env.ctx, env.admin, p.ID)
	if got.Quantity != 48500 {
		t.Fatalf("weighted stock = %d, want 48500", got.Quantity)
	}
}

// --- FIX-1 regression tests ---

func TestFix1_CancelInvoiceDrawerGate(t *testing.T) {
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	pid := testProduct(t, s, "سكر", money.Cents(1000), money.Cents(2000), money.Qty(10000))
	shiftID := b1OpenShiftFor(t, s, env.admin)
	_ = shiftID
	res := b1CashCustomerSale(t, s, env, env.admin, pid, money.Qty(2000)) // net 4000 cash
	// Drain the drawer to 500: expected 10000+4000=14000, out 13500.
	if _, err := s.CashBoxOut(env.ctx, env.admin, 0, money.Cents(13500), "سحب اختبار"); err != nil {
		t.Fatalf("cashbox out: %v", err)
	}
	if _, err := s.CancelInvoice(env.ctx, env.admin, res.InvoiceID, "خطأ"); err == nil {
		t.Fatal("cancel with a 4000 cash refund against a 500 drawer should be refused (H5)")
	}
	inv, err := s.GetInvoice(env.ctx, env.admin, res.InvoiceID)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Summary.IsCanceled {
		t.Fatal("refused cancel must not cancel the invoice")
	}
}

func TestFix1_CancelInvoiceSingleFreeMovement(t *testing.T) {
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	pid := testProduct(t, s, "سكر", money.Cents(1000), money.Cents(2000), money.Qty(10000))
	shiftID := b1OpenShiftFor(t, s, env.admin)
	invID := testSaleInvoice(t, s, env.admin, shiftID, 1, pid, 2000, 2000, 1000, 4000, 0, "Cash", 1)
	// bxgy: buy 2 get 1 free, earned on the kept 2 units. A real Offers
	// row is needed (InvoiceOffers.OfferID is a foreign key now, L7).
	res, err := s.DB.Exec(`INSERT INTO Offers(Name,Type,Value,FreeQty,IsActive)
		VALUES('اشترِ 2 واحصل على 1','buy_x_get_y','0','1',1)`)
	if err != nil {
		t.Fatal(err)
	}
	offerID, _ := res.LastInsertId()
	if _, err := s.DB.Exec(`INSERT INTO InvoiceOffers(InvoiceID,OfferID,OfferName,ProductID,BuyQty,FreeQty)
		VALUES(?,?,'اشترِ 2 واحصل على 1',?,2000,1000)`, invID, offerID, pid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelInvoice(env.ctx, env.admin, invID, "خطأ"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	var count int
	var total int64
	if err := s.DB.QueryRow(`SELECT COUNT(*),COALESCE(SUM(Quantity),0) FROM StockMovements
		WHERE ProductID=? AND MovementType='cancel_return'`, pid).Scan(&count, &total); err != nil {
		t.Fatal(err)
	}
	// M5: one movement for the whole restore (sold 2000 + free 1000).
	if count != 1 || total != 3000 {
		t.Fatalf("cancel_return movements = %d rows totalling %d, want 1 row totalling 3000", count, total)
	}
}

func TestFix1_BelowCostRefusedAndAllowed(t *testing.T) {
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	pid := testProduct(t, s, "سكر", money.Cents(1000), money.Cents(2000), money.Qty(10000))
	b1OpenShiftFor(t, s, env.admin)
	in := CreateInvoiceInput{CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
		Lines: []SaleLineInput{{ProductID: pid, Qty: 1000, UnitPrice: money.Cents(500)}}}
	if _, err := s.CreateInvoice(env.ctx, env.admin, in); err == nil {
		t.Fatal("selling below BuyPrice without confirmation should be refused (M9)")
	} else if !strings.Contains(err.Error(), "أقل من سعر الشراء") {
		t.Fatalf("wrong error: %v", err)
	}
	in.AllowBelowCost = true
	res, err := s.CreateInvoice(env.ctx, env.admin, in)
	if err != nil {
		t.Fatalf("confirmed below-cost sale: %v", err)
	}
	if n := auditCount(t, s, "بيع بخسارة"); n != 1 {
		t.Fatalf("below-cost audit rows = %d, want 1", n)
	}
	_ = res
}

// TestFix3_BelowCostNeedsPermission (FIX-3 H2): the below-cost flag is
// an approval, not a self-confirmation. A cashier passing
// AllowBelowCost is refused even with a valid below-cost sale; a
// supervisor holding approve_below_cost is allowed.
func TestFix3_BelowCostNeedsPermission(t *testing.T) {
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	pid := testProduct(t, s, "سكر", money.Cents(1000), money.Cents(2000), money.Qty(10000))
	cashier := b2TestUser(t, s, "h2cash", "cashier")
	sup := b2TestUser(t, s, "h2sup", "supervisor")
	b1OpenShiftFor(t, s, cashier)
	b1OpenShiftFor(t, s, sup)
	// Below cost via an ITEM DISCOUNT, not a lowered unit price: retail
	// 2000 - discount 1500 = net 500 < BuyPrice 1000. (A lowered unit
	// price would also need PermEditPrice, which the supervisor does
	// not hold — the discount path isolates the below-cost gate.)
	in := CreateInvoiceInput{CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash", AllowBelowCost: true,
		Lines: []SaleLineInput{{ProductID: pid, Qty: 1000, ItemDiscount: money.Cents(1500)}}}
	if _, err := s.CreateInvoice(env.ctx, cashier, in); err == nil {
		t.Fatal("cashier with AllowBelowCost should be refused")
	} else if !strings.Contains(err.Error(), "غير مسموح") {
		t.Fatalf("cashier refusal = %v, want a permission error", err)
	}
	if _, err := s.CreateInvoice(env.ctx, sup, in); err != nil {
		t.Fatalf("supervisor with AllowBelowCost: %v", err)
	}
	// Without the flag the below-cost sale is still refused for everyone
	// (the FIX-2 net gate is untouched).
	in.AllowBelowCost = false
	if _, err := s.CreateInvoice(env.ctx, sup, in); err == nil {
		t.Fatal("supervisor below-cost without the flag should still be refused")
	}
}

// TestFix3_DuplicateLinesMerge (FIX-3, jony rule 2026-11-25): an
// invoice never holds two lines for the same product. Re-adding a
// product merges into the one line — same price and different price.
func TestFix3_DuplicateLinesMerge(t *testing.T) {
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	pid := testProduct(t, s, "سكر", money.Cents(1000), money.Cents(2000), money.Qty(10000))
	b1OpenShiftFor(t, s, env.admin)

	detail := func(invoiceID int64) (n int, qty money.Qty, unit, total money.Cents) {
		t.Helper()
		if err := s.DB.QueryRow(`SELECT COUNT(*), COALESCE(SUM(Quantity),0),
			COALESCE(MAX(UnitPrice),0), COALESCE(SUM(TotalPrice),0)
			FROM InvoiceDetails WHERE InvoiceID=?`, invoiceID).Scan(&n, &qty, &unit, &total); err != nil {
			t.Fatalf("detail query: %v", err)
		}
		return n, qty, unit, total
	}

	// Case A — same product, same price, twice: one line, qty 2, gross 4000.
	res, err := s.CreateInvoice(env.ctx, env.admin, CreateInvoiceInput{CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
		Lines: []SaleLineInput{
			{ProductID: pid, Qty: 1000, UnitPrice: money.Cents(2000)},
			{ProductID: pid, Qty: 1000, UnitPrice: money.Cents(2000)},
		}})
	if err != nil {
		t.Fatalf("same-price duplicate sale: %v", err)
	}
	if res.Gross != 4000 || res.Net != 4000 {
		t.Fatalf("same-price: gross=%d net=%d, want 4000/4000", res.Gross, res.Net)
	}
	if n, qty, unit, total := detail(res.InvoiceID); n != 1 || qty != 2000 || unit != 2000 || total != 4000 {
		t.Fatalf("same-price detail: lines=%d qty=%d unit=%d total=%d, want 1/2000/2000/4000", n, qty, unit, total)
	}

	// Case B — same product, DIFFERENT prices: 2 × 5000 + 1 × 6000 =
	// gross 16000, one line, qty 3, displayed unit = weighted avg 5333.
	res, err = s.CreateInvoice(env.ctx, env.admin, CreateInvoiceInput{CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
		Lines: []SaleLineInput{
			{ProductID: pid, Qty: 2000, UnitPrice: money.Cents(5000)},
			{ProductID: pid, Qty: 1000, UnitPrice: money.Cents(6000)},
		}})
	if err != nil {
		t.Fatalf("different-price duplicate sale: %v", err)
	}
	if res.Gross != 16000 || res.Net != 16000 {
		t.Fatalf("different-price: gross=%d net=%d, want 16000/16000", res.Gross, res.Net)
	}
	if n, qty, unit, total := detail(res.InvoiceID); n != 1 || qty != 3000 || unit != 5333 || total != 16000 {
		t.Fatalf("different-price detail: lines=%d qty=%d unit=%d total=%d, want 1/3000/5333/16000", n, qty, unit, total)
	}

	// Stock fell by the merged total only (2 + 3 = 5 of 10 units).
	var stock int64
	if err := s.DB.QueryRow(`SELECT Quantity FROM Products WHERE ProductID=?`, pid).Scan(&stock); err != nil {
		t.Fatalf("stock query: %v", err)
	}
	if stock != 5000 {
		t.Fatalf("stock = %d, want 5000 (10 - 5 units)", stock)
	}
}

func TestFix1_CreditLimitRefused(t *testing.T) {
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	pid := testProduct(t, s, "سكر", money.Cents(1000), money.Cents(2000), money.Qty(10000))
	b1OpenShiftFor(t, s, env.admin)
	cust := testCustomer(t, s, "عميل حد")
	if _, err := s.DB.Exec(`UPDATE Customers SET CreditLimit=3000 WHERE CustomerID=?`, cust); err != nil {
		t.Fatal(err)
	}
	// Open remaining 2000 on the customer first (direct invoice).
	testSaleInvoice(t, s, env.admin, 1, 1, pid, 1000, 2000, 1000, 2000, 2000, "Cash", cust)
	in := CreateInvoiceInput{CustomerID: cust, SaleType: "retail", PaymentMethod: "Cash", Paid: 0,
		Lines: []SaleLineInput{{ProductID: pid, Qty: 1000}}}
	if _, err := s.CreateInvoice(env.ctx, env.admin, in); err == nil {
		t.Fatal("live 2000 + new remaining 2000 > limit 3000 should be refused (M6)")
	}
}

// --- FIX-2 regression tests (M-3: the below-cost gate must see the NET
// unit price after item discount, allocated invoice discount and promos,
// not just the nominal unit price). ---

func TestFix2_BelowCostNetViaItemDiscount(t *testing.T) {
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	pid := testProduct(t, s, "سكر", money.Cents(1000), money.Cents(2000), money.Qty(10000))
	b1OpenShiftFor(t, s, env.admin)
	// Nominal price 2000 is above BuyPrice 1000, but the 1500 item
	// discount leaves a net of 500 < cost 1000.
	in := CreateInvoiceInput{CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
		Lines: []SaleLineInput{{ProductID: pid, Qty: 1000, ItemDiscount: money.Cents(1500)}}}
	if _, err := s.CreateInvoice(env.ctx, env.admin, in); err == nil {
		t.Fatal("net below cost via item discount should be refused (M-3)")
	} else if !strings.Contains(err.Error(), "أقل من سعر الشراء") {
		t.Fatalf("wrong error: %v", err)
	}
	in.AllowBelowCost = true
	if _, err := s.CreateInvoice(env.ctx, env.admin, in); err != nil {
		t.Fatalf("confirmed net below-cost sale: %v", err)
	}
	if n := auditCount(t, s, "بيع بخسارة"); n != 1 {
		t.Fatalf("below-cost audit rows = %d, want 1", n)
	}
}

func TestFix2_BelowCostNetViaInvoiceDiscount(t *testing.T) {
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	pid := testProduct(t, s, "سكر", money.Cents(1000), money.Cents(2000), money.Qty(10000))
	b1OpenShiftFor(t, s, env.admin)
	// Nominal price 2000 is above BuyPrice 1000, but the 1500 invoice
	// discount (allocated to the only line) leaves a net of 500 < 1000.
	in := CreateInvoiceInput{CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
		InvoiceDiscount: money.Cents(1500),
		Lines:           []SaleLineInput{{ProductID: pid, Qty: 1000}}}
	if _, err := s.CreateInvoice(env.ctx, env.admin, in); err == nil {
		t.Fatal("net below cost via invoice discount should be refused (M-3)")
	} else if !strings.Contains(err.Error(), "أقل من سعر الشراء") {
		t.Fatalf("wrong error: %v", err)
	}
	in.AllowBelowCost = true
	if _, err := s.CreateInvoice(env.ctx, env.admin, in); err != nil {
		t.Fatalf("confirmed net below-cost sale: %v", err)
	}
	if n := auditCount(t, s, "بيع بخسارة"); n != 1 {
		t.Fatalf("below-cost audit rows = %d, want 1", n)
	}
}

func TestFix2_BelowCostNetViaPromo(t *testing.T) {
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	pid := testProduct(t, s, "سكر", money.Cents(1000), money.Cents(2000), money.Qty(10000))
	b1OpenShiftFor(t, s, env.admin)
	b1CreateOffer(t, s, OfferInput{Name: "خصم ثابت", Type: OfferProductFixed, Amount: 1500, IsActive: true,
		Targets: []OfferTarget{{TargetType: "product", TargetID: pid}}})
	// Nominal price 2000 is above BuyPrice 1000, but the 1500 promo
	// leaves a net of 500 < cost 1000.
	in := CreateInvoiceInput{CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
		Lines: []SaleLineInput{{ProductID: pid, Qty: 1000}}}
	if _, err := s.CreateInvoice(env.ctx, env.admin, in); err == nil {
		t.Fatal("net below cost via promo should be refused (M-3)")
	} else if !strings.Contains(err.Error(), "أقل من سعر الشراء") {
		t.Fatalf("wrong error: %v", err)
	}
	in.AllowBelowCost = true
	if _, err := s.CreateInvoice(env.ctx, env.admin, in); err != nil {
		t.Fatalf("confirmed net below-cost sale: %v", err)
	}
	if n := auditCount(t, s, "بيع بخسارة"); n != 1 {
		t.Fatalf("below-cost audit rows = %d, want 1", n)
	}
}
