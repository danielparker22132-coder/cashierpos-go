package store

import (
	"context"
	"testing"

	"cashierpos-go/internal/money"
)

func TestVendorCRUDRoles(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	cashier := testUser(t, s, "cash1", "cashier")
	supervisor := testUser(t, s, "sup1", "supervisor")

	// Cashier MAY create a new vendor (V-01 / matrix row 39).
	id, err := s.CreateVendor(ctx, cashier, VendorInput{Name: "مورد الكاشير", Phone: "01000000001"})
	if err != nil {
		t.Fatalf("cashier create vendor: %v", err)
	}
	// Negative balance / credit limit refused.
	if _, err := s.CreateVendor(ctx, admin, VendorInput{Name: "سالب", Balance: -1}); err == nil {
		t.Fatal("negative vendor balance should be refused")
	}
	if _, err := s.CreateVendor(ctx, admin, VendorInput{Name: "سالب", CreditLimit: -1}); err == nil {
		t.Fatal("negative vendor credit limit should be refused")
	}
	// Cashier may NOT edit an existing vendor.
	mustErr(t, s.UpdateVendor(ctx, cashier, id, VendorInput{Name: "معدل", IsActive: true}), "cashier edit vendor")
	// Supervisor may NOT create a vendor.
	mustErr(t, func() error {
		_, err := s.CreateVendor(ctx, supervisor, VendorInput{Name: "مشرف"})
		return err
	}(), "supervisor create vendor")
	// Admin edits fine.
	if err := s.UpdateVendor(ctx, admin, id, VendorInput{Name: "مورد معدل", CreditLimit: money.Cents(100000), IsActive: true}); err != nil {
		t.Fatalf("admin update vendor: %v", err)
	}
	// Cashier may NOT delete.
	mustErr(t, s.DeleteVendor(ctx, cashier, id), "cashier delete vendor")
	// Admin deletes a vendor with no purchases.
	if err := s.DeleteVendor(ctx, admin, id); err != nil {
		t.Fatalf("admin delete vendor: %v", err)
	}
}

func TestPurchaseIncreasesStockAndVendorBalance(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	vid, _ := s.CreateVendor(ctx, admin, VendorInput{Name: "مورد الشراء"})
	prod := testProduct(t, s, "سكر", money.Cents(1000), money.Cents(2000), money.Qty(5000)) // 5 units @ 10 EGP

	pid, invNo, err := s.CreatePurchase(ctx, admin, PurchaseInput{
		VendorID: vid,
		Lines: []PurchaseLineInput{
			{ProductID: prod, Qty: money.Qty(2500), PurchasePrice: money.Cents(1200)}, // 2.5 units @ 12
		},
		Paid:          money.Cents(1000),
		PaymentMethod: "Visa",
		DueDate:       "2026-12-01",
	})
	if err != nil {
		t.Fatalf("create purchase: %v", err)
	}
	if invNo == "" || len(invNo) < 3 || invNo[:3] != "PI-" {
		t.Fatalf("purchase number should start with PI-, got %q", invNo)
	}
	// Stock 5 + 2.5 = 7.5 units (7500 milli).
	var qty int64
	_ = s.DB.QueryRow(`SELECT Quantity FROM Products WHERE ProductID=?`, prod).Scan(&qty)
	if qty != 7500 {
		t.Fatalf("stock = %d milli, want 7500", qty)
	}
	// Admin's purchase updates BuyPrice to 1200.
	var buy int64
	_ = s.DB.QueryRow(`SELECT BuyPrice FROM Products WHERE ProductID=?`, prod).Scan(&buy)
	if buy != 1200 {
		t.Fatalf("BuyPrice = %d, want 1200 (admin updates it)", buy)
	}
	// Total 2.5*12 = 30 EGP; paid 10; remaining 20 -> vendor balance 20.
	v, _ := s.GetVendor(ctx, admin, vid)
	if v.CurrentBalance != money.Cents(2000) {
		t.Fatalf("vendor balance = %v, want 20.00 EGP", v.CurrentBalance)
	}
	// StockMovements row per line.
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM StockMovements WHERE ProductID=?`, prod).Scan(&n)
	if n != 1 {
		t.Fatalf("StockMovements rows = %d, want 1", n)
	}
	// Details view.
	d, err := s.GetPurchase(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if d.Purchase.Total != money.Cents(3000) || len(d.Lines) != 1 || d.Lines[0].Qty != money.Qty(2500) {
		t.Fatalf("purchase detail wrong: %+v", d.Purchase)
	}
}

func TestPurchaseBuyPriceNotUpdatedForCashier(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	cashier := testUser(t, s, "cash1", "cashier")
	vid, _ := s.CreateVendor(ctx, admin, VendorInput{Name: "مورد"})
	prod := testProduct(t, s, "أرز", money.Cents(1000), money.Cents(2000), money.Qty(0))
	// Cashier may receive a purchase (matrix row 37) but may NOT change BuyPrice.
	if _, _, err := s.CreatePurchase(ctx, cashier, PurchaseInput{
		VendorID: vid,
		Lines:    []PurchaseLineInput{{ProductID: prod, Qty: money.Qty(1000), PurchasePrice: money.Cents(2000)}},
		Paid:     0, PaymentMethod: "Visa",
	}); err != nil {
		t.Fatalf("cashier purchase: %v", err)
	}
	var buy int64
	_ = s.DB.QueryRow(`SELECT BuyPrice FROM Products WHERE ProductID=?`, prod).Scan(&buy)
	if buy != 1000 {
		t.Fatalf("BuyPrice changed by cashier purchase: %d", buy)
	}
	// Supervisor may NOT receive a purchase.
	sup := testUser(t, s, "sup1", "supervisor")
	mustErr(t, func() error {
		_, _, err := s.CreatePurchase(ctx, sup, PurchaseInput{
			VendorID: vid,
			Lines:    []PurchaseLineInput{{ProductID: prod, Qty: money.Qty(1000), PurchasePrice: money.Cents(2000)}},
		})
		return err
	}(), "supervisor receive purchase")
}

func TestPurchaseValidation(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	vid, _ := s.CreateVendor(ctx, admin, VendorInput{Name: "مورد"})
	prod := testProduct(t, s, "شاي", money.Cents(500), money.Cents(1000), money.Qty(0))
	cases := []struct {
		name string
		in   PurchaseInput
	}{
		{"no vendor", PurchaseInput{Lines: []PurchaseLineInput{{ProductID: prod, Qty: 1000, PurchasePrice: 500}}}},
		{"no lines", PurchaseInput{VendorID: vid}},
		{"zero qty", PurchaseInput{VendorID: vid, Lines: []PurchaseLineInput{{ProductID: prod, Qty: 0, PurchasePrice: 500}}}},
		{"zero price", PurchaseInput{VendorID: vid, Lines: []PurchaseLineInput{{ProductID: prod, Qty: 1000, PurchasePrice: 0}}}},
		{"paid over total", PurchaseInput{VendorID: vid, Lines: []PurchaseLineInput{{ProductID: prod, Qty: 1000, PurchasePrice: 500}}, Paid: money.Cents(501), PaymentMethod: "Visa"}},
		{"unknown vendor", PurchaseInput{VendorID: 9999, Lines: []PurchaseLineInput{{ProductID: prod, Qty: 1000, PurchasePrice: 500}}}},
		{"unknown product", PurchaseInput{VendorID: vid, Lines: []PurchaseLineInput{{ProductID: 9999, Qty: 1000, PurchasePrice: 500}}}},
	}
	for _, c := range cases {
		mustErr(t, func() error {
			_, _, err := s.CreatePurchase(ctx, admin, c.in)
			return err
		}(), c.name)
	}
	// Nothing was written by the refused attempts.
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM PurchaseInvoices`).Scan(&n)
	if n != 0 {
		t.Fatalf("refused purchases wrote %d invoices", n)
	}
}

func TestPurchaseBatchAndExpiry(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	vid, _ := s.CreateVendor(ctx, admin, VendorInput{Name: "مورد"})
	prod := testProduct(t, s, "لبن", money.Cents(1000), money.Cents(2000), money.Qty(0))
	if _, _, err := s.CreatePurchase(ctx, admin, PurchaseInput{
		VendorID: vid,
		Lines: []PurchaseLineInput{{ProductID: prod, Qty: money.Qty(10000), PurchasePrice: money.Cents(1500),
			BatchNumber: "B-77", ExpiryDate: "2027-01-01"}},
		Paid: 0, PaymentMethod: "Visa",
	}); err != nil {
		t.Fatalf("batch purchase: %v", err)
	}
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM ProductBatches WHERE ProductID=? AND BatchNumber='B-77'`, prod).Scan(&n)
	if n != 1 {
		t.Fatalf("ProductBatches rows = %d, want 1", n)
	}
	var batch, exp string
	var tracked int
	_ = s.DB.QueryRow(`SELECT BatchNumber,ExpiryDate,IsExpiryTracked FROM Products WHERE ProductID=?`, prod).Scan(&batch, &exp, &tracked)
	if batch != "B-77" || exp != "2027-01-01" || tracked != 1 {
		t.Fatalf("product batch fields: %q %q %d", batch, exp, tracked)
	}
}

func TestPayVendorRules(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	cashier := testUser(t, s, "cash1", "cashier")
	vid, _ := s.CreateVendor(ctx, admin, VendorInput{Name: "مورد السداد"})
	prod := testProduct(t, s, "زيت", money.Cents(1000), money.Cents(2000), money.Qty(0))
	pid, _, err := s.CreatePurchase(ctx, admin, PurchaseInput{
		VendorID: vid,
		Lines:    []PurchaseLineInput{{ProductID: prod, Qty: money.Qty(10000), PurchasePrice: money.Cents(1000)}},
		Paid:     0, PaymentMethod: "Visa",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Vendor balance is now 100 EGP.
	// Over-balance refused.
	mustErr(t, func() error {
		_, err := s.PayVendor(ctx, admin, vid, 0, money.Cents(10001), "Visa", "")
		return err
	}(), "pay over vendor balance")
	// Zero refused.
	mustErr(t, func() error {
		_, err := s.PayVendor(ctx, admin, vid, 0, 0, "Visa", "")
		return err
	}(), "pay zero")
	// Cash without a shift refused.
	mustErr(t, func() error {
		_, err := s.PayVendor(ctx, admin, vid, 0, money.Cents(1000), "Cash", "")
		return err
	}(), "cash pay without shift")
	// Cash with a shift but over the drawer refused.
	testOpenShift(t, s, admin, money.Cents(500)) // drawer has 5 EGP only
	mustErr(t, func() error {
		_, err := s.PayVendor(ctx, admin, vid, 0, money.Cents(1000), "Cash", "")
		return err
	}(), "cash pay over drawer")
	// Non-cash pay works without touching the drawer, targeting the purchase.
	after, err := s.PayVendor(ctx, admin, vid, pid, money.Cents(4000), "Visa", "تحويل بنكي")
	if err != nil {
		t.Fatalf("non-cash pay: %v", err)
	}
	if after != money.Cents(6000) {
		t.Fatalf("balance after = %v, want 60.00 EGP", after)
	}
	var cb int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM CashBox`).Scan(&cb)
	if cb != 0 {
		t.Fatalf("non-cash pay must not touch CashBox, found %d rows", cb)
	}
	// BalanceBefore/After recorded.
	var before, aft int64
	_ = s.DB.QueryRow(`SELECT BalanceBefore,BalanceAfter FROM VendorPayments WHERE VendorID=?`, vid).Scan(&before, &aft)
	if before != 10000 || aft != 6000 {
		t.Fatalf("BalanceBefore/After = %d/%d, want 10000/6000", before, aft)
	}
	// Purchase remaining reduced too.
	var rem int64
	_ = s.DB.QueryRow(`SELECT RemainingAmount FROM PurchaseInvoices WHERE PurchaseID=?`, pid).Scan(&rem)
	if rem != 6000 {
		t.Fatalf("purchase remaining = %d, want 6000", rem)
	}
	// Cashier may pay vendors (matrix row 41) — cash, drawer covers it.
	testOpenShift(t, s, cashier, money.Cents(10000))
	after, err = s.PayVendor(ctx, cashier, vid, 0, money.Cents(6000), "Cash", "")
	if err != nil {
		t.Fatalf("cashier cash pay: %v", err)
	}
	if after != 0 {
		t.Fatalf("balance after cashier pay = %v", after)
	}
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM CashBox WHERE MovementType='out' AND Amount=6000`).Scan(&cb)
	if cb != 1 {
		t.Fatalf("CashBox 'out' rows = %d, want 1", cb)
	}
	// Supervisor may NOT pay vendors.
	sup := testUser(t, s, "sup1", "supervisor")
	mustErr(t, func() error {
		_, err := s.PayVendor(ctx, sup, vid, 0, money.Cents(100), "Visa", "")
		return err
	}(), "supervisor pay vendor")
}

func TestVendorDueAlertsAndLedger(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	vid, _ := s.CreateVendor(ctx, admin, VendorInput{Name: "مورد التنبيه"})
	prod := testProduct(t, s, "قهوة", money.Cents(1000), money.Cents(2000), money.Qty(0))
	// Due in 10 days -> alert; due in 60 days -> no alert.
	_, _, _ = s.CreatePurchase(ctx, admin, PurchaseInput{
		VendorID: vid, Lines: []PurchaseLineInput{{ProductID: prod, Qty: money.Qty(1000), PurchasePrice: money.Cents(1000)}},
		Paid: 0, PaymentMethod: "Visa", DueDate: "2026-12-05",
	})
	_, _, _ = s.CreatePurchase(ctx, admin, PurchaseInput{
		VendorID: vid, Lines: []PurchaseLineInput{{ProductID: prod, Qty: money.Qty(1000), PurchasePrice: money.Cents(1000)}},
		Paid: 0, PaymentMethod: "Visa", DueDate: "2027-01-25",
	})
	alerts, err := s.VendorDueAlerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 1 || alerts[0].DueDate != "2026-12-05" {
		t.Fatalf("due alerts = %d, want 1 due 2026-12-05", len(alerts))
	}
	led, err := s.VendorLedger(ctx, admin, vid)
	if err != nil {
		t.Fatal(err)
	}
	if len(led.Purchases) != 2 || led.Vendor.CurrentBalance != money.Cents(2000) {
		t.Fatalf("ledger: %d purchases, balance %v", len(led.Purchases), led.Vendor.CurrentBalance)
	}
}

// --- FIX-1 regression tests ---

func TestFix1_DeleteVendorWithPaymentsRefused(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	vid, err := s.CreateVendor(ctx, admin, VendorInput{Name: "مورد"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO VendorPayments(VendorID,Amount,PaymentDate,PaymentMethod)
		VALUES(?, 500, ?, 'Cash')`, vid, Today()); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteVendor(ctx, admin, vid); err == nil {
		t.Fatal("vendor with recorded payments must not be deletable (L7)")
	}
}
