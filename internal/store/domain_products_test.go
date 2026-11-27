package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"cashierpos-go/internal/auth"
)

func addDays(date string, days int) string {
	t, _ := time.Parse("2006-01-02", date)
	return t.AddDate(0, 0, days).Format("2006-01-02")
}

var b1UserSeq int

func b1Admin(t *testing.T, s *Store) *auth.User {
	t.Helper()
	b1UserSeq++
	return b2TestUser(t, s, "b1admin"+itoa(b1UserSeq), "admin")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func b1ProductInput(name string) ProductInput {
	return ProductInput{
		Name: name, BuyPrice: 1000, BulkPrice: 1200, WholesalePrice: 1500,
		RetailPrice: 2000, Quantity: 10000, MinQuantity: 5000, Unit: "قطعة",
		ExpiryAlertDays: 30, ScaleBarcodeType: "WeightEmbedded", IsActive: true,
	}
}

func b1MustProduct(t *testing.T, s *Store, admin *auth.User, name string) *Product {
	t.Helper()
	p, err := s.CreateProduct(context.Background(), admin, b1ProductInput(name))
	if err != nil {
		t.Fatalf("create product %s: %v", name, err)
	}
	return p
}

func TestB1ProductCreateValidation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	cashier := b2TestUser(t, s, "b1cash", "cashier")

	// Cashier may not manage products.
	if _, err := s.CreateProduct(ctx, cashier, b1ProductInput("سكر")); err == nil {
		t.Fatal("cashier create product should be denied")
	}
	// Empty name.
	in := b1ProductInput("")
	if _, err := s.CreateProduct(ctx, admin, in); err == nil || !strings.Contains(err.Error(), "اسم المنتج مطلوب") {
		t.Fatalf("empty name err = %v", err)
	}
	// Price order violated.
	in = b1ProductInput("أرز")
	in.RetailPrice = 900 // below buy price
	if _, err := s.CreateProduct(ctx, admin, in); err == nil || !strings.Contains(err.Error(), "ترتيب الأسعار") {
		t.Fatalf("price order err = %v", err)
	}
	// Zero price.
	in = b1ProductInput("أرز")
	in.BuyPrice = 0
	if _, err := s.CreateProduct(ctx, admin, in); err == nil {
		t.Fatal("zero buy price should be rejected")
	}
	// Negative quantity.
	in = b1ProductInput("أرز")
	in.Quantity = -1000
	if _, err := s.CreateProduct(ctx, admin, in); err == nil {
		t.Fatal("negative quantity should be rejected")
	}
	// Duplicate barcode.
	in = b1ProductInput("سكر")
	in.Barcode = "6221001"
	if _, err := s.CreateProduct(ctx, admin, in); err != nil {
		t.Fatalf("first barcode create: %v", err)
	}
	in2 := b1ProductInput("سكر آخر")
	in2.Barcode = "6221001"
	if _, err := s.CreateProduct(ctx, admin, in2); err == nil || !strings.Contains(err.Error(), "الباركود موجود") {
		t.Fatalf("duplicate barcode err = %v", err)
	}
	// Weighted validation: PLU required, digits only, unique, PricePerKg > 0.
	in = b1ProductInput("جبنة")
	in.IsWeighted = true
	in.ScalePLU = ""
	if _, err := s.CreateProduct(ctx, admin, in); err == nil || !strings.Contains(err.Error(), "PLU") {
		t.Fatalf("weighted no PLU err = %v", err)
	}
	in.ScalePLU = "12A"
	if _, err := s.CreateProduct(ctx, admin, in); err == nil || !strings.Contains(err.Error(), "أرقامًا فقط") {
		t.Fatalf("weighted bad PLU err = %v", err)
	}
	in.ScalePLU = "123"
	in.PricePerKg = 0
	if _, err := s.CreateProduct(ctx, admin, in); err == nil || !strings.Contains(err.Error(), "سعر الكيلو") {
		t.Fatalf("weighted no price/kg err = %v", err)
	}
	in.PricePerKg = 5000
	in.ScaleEnabled = true
	if _, err := s.CreateProduct(ctx, admin, in); err != nil {
		t.Fatalf("weighted create: %v", err)
	}
	in2 = b1ProductInput("جبنة 2")
	in2.IsWeighted = true
	in2.ScalePLU = "123"
	in2.PricePerKg = 5000
	if _, err := s.CreateProduct(ctx, admin, in2); err == nil || !strings.Contains(err.Error(), "PLU مستخدم") {
		t.Fatalf("duplicate PLU err = %v", err)
	}
}

func TestB1ProductUpdateSearchLowExpiring(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	p := b1MustProduct(t, s, admin, "سكر")
	// Update price order still enforced.
	upd := b1ProductInput("سكر")
	upd.Barcode = p.Barcode
	upd.Quantity = p.Quantity
	upd.MinQuantity = p.MinQuantity
	upd.RetailPrice = 500
	if _, err := s.UpdateProduct(ctx, admin, p.ID, upd); err == nil {
		t.Fatal("update with bad price order should fail")
	}
	upd.RetailPrice = 2500
	got, err := s.UpdateProduct(ctx, admin, p.ID, upd)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.RetailPrice != 2500 {
		t.Fatalf("retail = %d, want 2500", got.RetailPrice)
	}
	// Search by name and barcode.
	found, err := s.SearchProducts(ctx, admin, "سك")
	if err != nil || len(found) != 1 {
		t.Fatalf("search name: %v %+v", err, found)
	}
	// Low stock: qty 10000 > min 5000 -> none; drop qty below min.
	low, err := s.LowStockProducts(ctx, admin)
	if err != nil || len(low) != 0 {
		t.Fatalf("low stock before: %v %+v", err, low)
	}
	if _, err := s.AdjustStock(ctx, admin, p.ID, 4000, "جرد"); err != nil {
		t.Fatalf("adjust: %v", err)
	}
	low, _ = s.LowStockProducts(ctx, admin)
	if len(low) != 1 {
		t.Fatalf("low stock after adjust: %+v", low)
	}
	// Expiring: set expiry 5 days ahead (alert 30 days).
	// FIX-12 SW-L04: expiry alerts cover tracked products only, so the
	// fixture must be tracked for this section to exercise the alert.
	upd.IsExpiryTracked = true
	upd.ExpiryDate = "2999-01-01"
	upd.Quantity = 4000
	if _, err := s.UpdateProduct(ctx, admin, p.ID, upd); err != nil {
		t.Fatalf("set far expiry: %v", err)
	}
	exp, _ := s.ExpiringProducts(ctx, admin)
	if len(exp) != 0 {
		t.Fatalf("far expiry should not alert: %+v", exp)
	}
	// Expiry 10 days from today.
	upd.ExpiryDate = addDays(Today(), 10)
	if _, err := s.UpdateProduct(ctx, admin, p.ID, upd); err != nil {
		t.Fatalf("set near expiry: %v", err)
	}
	exp, _ = s.ExpiringProducts(ctx, admin)
	if len(exp) != 1 {
		t.Fatalf("near expiry should alert: %+v", exp)
	}
	// Delete (deactivate) then search finds nothing.
	if err := s.DeleteProduct(ctx, admin, p.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	found, _ = s.SearchProducts(ctx, admin, "سك")
	if len(found) != 0 {
		t.Fatalf("deleted product still searchable: %+v", found)
	}
}

func TestB1AddAdjustStockPermissions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	sup := b2TestUser(t, s, "b1sup", "supervisor")
	wh := b2TestUser(t, s, "b1wh", "warehouse")
	cashier := b2TestUser(t, s, "b1cash2", "cashier")
	p := b1MustProduct(t, s, admin, "زيت")

	// AddStock: supervisor + warehouse ok, cashier denied.
	if _, err := s.AddStock(ctx, cashier, p.ID, 2000); err == nil {
		t.Fatal("cashier add stock should be denied")
	}
	if _, err := s.AddStock(ctx, sup, p.ID, 2000); err != nil {
		t.Fatalf("supervisor add stock: %v", err)
	}
	if _, err := s.AddStock(ctx, wh, p.ID, 1000); err != nil {
		t.Fatalf("warehouse add stock: %v", err)
	}
	got, _ := s.GetProduct(ctx, admin, p.ID)
	if got.Quantity != 13000 {
		t.Fatalf("qty = %d, want 13000", got.Quantity)
	}
	if _, err := s.AddStock(ctx, admin, p.ID, 0); err == nil {
		t.Fatal("zero add stock should be rejected")
	}
	// AdjustStock: warehouse ok, supervisor/cashier denied, reason required.
	if _, err := s.AdjustStock(ctx, sup, p.ID, 9000, "x"); err == nil {
		t.Fatal("supervisor adjust should be denied")
	}
	if _, err := s.AdjustStock(ctx, wh, p.ID, 9000, ""); err == nil || !strings.Contains(err.Error(), "سبب التعديل") {
		t.Fatalf("adjust without reason err = %v", err)
	}
	if _, err := s.AdjustStock(ctx, wh, p.ID, -1000, "x"); err == nil {
		t.Fatal("negative adjust should be rejected")
	}
	if _, err := s.AdjustStock(ctx, wh, p.ID, 9000, "تصحيح"); err != nil {
		t.Fatalf("warehouse adjust: %v", err)
	}
	// Movement ledger: add (purchase) + adjust rows exist.
	movs, err := s.ListStockMovements(ctx, admin, p.ID, 10)
	if err != nil || len(movs) < 3 {
		t.Fatalf("movements = %+v err %v", movs, err)
	}
	ledger, err := s.GetStockLedger(ctx, admin, p.ID)
	if err != nil || len(ledger) == 0 {
		t.Fatalf("ledger err %v", err)
	}
}

func b1ScaleBarcode(plu, value string) string {
	body := "20" + plu + value
	total := 0
	weight := 3
	for i := len(body) - 1; i >= 0; i-- {
		total += int(body[i]-'0') * weight
		if weight == 3 {
			weight = 1
		} else {
			weight = 3
		}
	}
	check := (10 - (total % 10)) % 10
	return body + string(rune('0'+check))
}

func TestB1ScaleBarcode(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	in := b1ProductInput("جبنة رومي")
	in.IsWeighted = true
	in.ScalePLU = "123"
	in.PricePerKg = 2000 // 20 EGP/kg
	in.ScaleEnabled = true
	in.Quantity = 50000 // 50 kg
	p, err := s.CreateProduct(ctx, admin, in)
	if err != nil {
		t.Fatalf("create weighted: %v", err)
	}
	code := b1ScaleBarcode("00123", "01500") // 1.500 kg
	if len(code) != 13 {
		t.Fatalf("scale barcode len = %d (%s)", len(code), code)
	}
	dec, err := s.DecodeScaleBarcode(ctx, code)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dec.ProductID != p.ID || dec.Weight != 1500 || dec.TotalPrice != 3000 {
		t.Fatalf("decoded = %+v", dec)
	}
	got, dec2, err := s.GetProductByBarcode(ctx, admin, code)
	if err != nil || got.ID != p.ID || dec2 == nil || dec2.Weight != 1500 {
		t.Fatalf("by barcode: %+v %+v %v", got, dec2, err)
	}
	// Bad check digit rejected.
	bad := code[:len(code)-1] + "0"
	if code[len(code)-1] == '0' {
		bad = code[:len(code)-1] + "1"
	}
	if _, err := s.DecodeScaleBarcode(ctx, bad); err == nil {
		t.Fatal("bad check digit should be rejected")
	}
	// Unknown barcode.
	if _, _, err := s.GetProductByBarcode(ctx, admin, "9999999999999"); err == nil {
		t.Fatal("unknown barcode should error")
	}
	// Plain barcode lookup still works.
	in2 := b1ProductInput("سكر")
	in2.Barcode = "6221001001"
	p2, err := s.CreateProduct(ctx, admin, in2)
	if err != nil {
		t.Fatalf("create plain: %v", err)
	}
	got, dec3, err := s.GetProductByBarcode(ctx, admin, "6221001001")
	if err != nil || got.ID != p2.ID || dec3 != nil {
		t.Fatalf("plain barcode: %+v %+v %v", got, dec3, err)
	}
}

// --- FIX-1 regression tests ---

func TestFix1_ScalePriceEmbeddedRounds(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	in := b1ProductInput("جبنة رومي")
	in.IsWeighted = true
	in.ScalePLU = "123"
	in.PricePerKg = 700 // 7 EGP/kg
	in.ScaleEnabled = true
	in.Quantity = 50000
	p, err := s.CreateProduct(ctx, admin, in)
	if err != nil {
		t.Fatalf("create weighted: %v", err)
	}
	// Switch the product-level and global scale modes to PriceEmbedded
	// (the product's ScaleBarcodeType overrides the global setting).
	if _, err := s.DB.Exec(`UPDATE Products SET ScaleBarcodeType='PriceEmbedded' WHERE ProductID=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	// Switch the global scale mode to PriceEmbedded, 3 value decimals.
	if _, err := s.DB.Exec(`INSERT INTO ScaleSettings(ID,IsEnabled,BarcodePrefix,ProductCodeLength,
		ValueLength,BarcodeMode,HasCheckDigit,WeightUnit,ValueDecimalPlaces)
		VALUES(1,1,'20',5,5,'PriceEmbedded',1,'Kilogram',3)`); err != nil {
		t.Fatal(err)
	}
	// Value 15999 → total 15999×100/1000 = 1599.9 cents → 1600 rounded
	// (1599 truncated); weight 1600×1000/700 = 2285.7 g → 2286 rounded.
	code := b1ScaleBarcode("00123", "15999")
	dec, err := s.DecodeScaleBarcode(ctx, code)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dec.ProductID != p.ID || dec.TotalPrice != 1600 || dec.Weight != 2286 {
		t.Fatalf("decoded = %+v, want price 1600 weight 2286 (L3 rounding)", dec)
	}
}

func TestFix2_ScaleWeightEmbeddedRounds(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	in := b1ProductInput("جبنة رومي")
	in.IsWeighted = true
	in.ScalePLU = "123"
	in.PricePerKg = 2000
	in.ScaleEnabled = true
	in.Quantity = 50000
	p, err := s.CreateProduct(ctx, admin, in)
	if err != nil {
		t.Fatalf("create weighted: %v", err)
	}
	// Global mode WeightEmbedded, 4 value decimals: value 15005 means
	// 1.5005 kg -> weightMilli = 15005*1000/10000 = 1500.5 -> 1501
	// rounded (1500 truncated).
	if _, err := s.DB.Exec(`INSERT INTO ScaleSettings(ID,IsEnabled,BarcodePrefix,ProductCodeLength,
		ValueLength,BarcodeMode,HasCheckDigit,WeightUnit,ValueDecimalPlaces)
		VALUES(1,1,'20',5,5,'WeightEmbedded',1,'Kilogram',4)`); err != nil {
		t.Fatal(err)
	}
	code := b1ScaleBarcode("00123", "15005")
	dec, err := s.DecodeScaleBarcode(ctx, code)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dec.ProductID != p.ID || dec.Weight != 1501 {
		t.Fatalf("decoded = %+v, want weight 1501 (L-5 rounding, not 1500 truncated)", dec)
	}
	// Gram branch: value 01500 with 3 decimals = 1.500 g -> 1.5 milli-kg
	// -> 2 rounded (1 truncated).
	if _, err := s.DB.Exec(`UPDATE ScaleSettings SET WeightUnit='Gram', ValueDecimalPlaces=3 WHERE ID=1`); err != nil {
		t.Fatal(err)
	}
	code = b1ScaleBarcode("00123", "01500")
	dec, err = s.DecodeScaleBarcode(ctx, code)
	if err != nil {
		t.Fatalf("decode gram: %v", err)
	}
	if dec.Weight != 2 {
		t.Fatalf("gram weight = %d, want 2 (rounded, not 1 truncated)", dec.Weight)
	}
}
