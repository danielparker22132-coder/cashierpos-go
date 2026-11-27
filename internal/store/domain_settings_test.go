package store

import (
	"context"
	"strings"
	"testing"

	"cashierpos-go/internal/auth"
)

func TestSettingsStoreProfileAndLock(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testAdmin()

	if p := s.GetStoreProfile(); p.Name == "" {
		t.Fatal("seeded store name should not be empty")
	}
	if err := s.SaveStoreProfile(ctx, admin, StoreProfile{Name: "", Phone: "x"}); err == nil {
		t.Fatal("empty store name should be refused")
	}
	if err := s.SaveStoreProfile(ctx, admin, StoreProfile{Name: "هايبر الفتح", Phone: "01000000000", Address: "ناجي حمادي"}); err != nil {
		t.Fatalf("SaveStoreProfile: %v", err)
	}
	if p := s.GetStoreProfile(); p.Name != "هايبر الفتح" || p.Phone != "01000000000" {
		t.Fatalf("profile = %+v", p)
	}
	if s.GetAutoLockMinutes() != 15 {
		t.Fatalf("default auto-lock = %d, want 15", s.GetAutoLockMinutes())
	}
	if err := s.SaveAutoLockMinutes(ctx, admin, 0); err != nil {
		t.Fatalf("auto-lock 0 (disabled) should be allowed: %v", err)
	}
	if s.GetAutoLockMinutes() != 0 {
		t.Fatal("auto-lock should be 0 after save")
	}
	if err := s.SaveAutoLockMinutes(ctx, admin, -5); err == nil {
		t.Fatal("negative auto-lock should be refused")
	}
	if err := s.SaveAutoLockMinutes(ctx, admin, 601); err == nil {
		t.Fatal("auto-lock above 600 should be refused")
	}
	if err := s.SetDarkMode(ctx, admin, true); err != nil {
		t.Fatalf("SetDarkMode: %v", err)
	}
	if !s.GetDarkMode() {
		t.Fatal("dark mode should be on")
	}
}

func TestSettingsZonesNagHammadiFree(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testAdmin()

	// Seed has ناجي حمادي fee 0 active.
	zones := s.GetDeliveryZones()
	if len(zones) != 1 || zones[0].Name != NagHammadiZone || zones[0].Fee != 0 || !zones[0].Active {
		t.Fatalf("seeded zones = %+v", zones)
	}
	// Saving ناجي حمادي with a fee of 20 EGP (2000 cents) must force it back to 0.
	if err := s.SaveDeliveryZones(ctx, admin, []DeliveryZone{
		{Name: NagHammadiZone, Fee: 2000, Active: true},
		{Name: "بهجورة", Fee: 1500, Active: true},
	}); err != nil {
		t.Fatalf("SaveDeliveryZones: %v", err)
	}
	zones = s.GetDeliveryZones()
	for _, z := range zones {
		if z.Name == NagHammadiZone && z.Fee != 0 {
			t.Fatalf("ناجي حمادي fee = %d, must stay 0", z.Fee)
		}
	}
	// Deleting ناجي حمادي from the list re-adds it (fee 0, active).
	if err := s.SaveDeliveryZones(ctx, admin, []DeliveryZone{{Name: "بهجورة", Fee: 1500, Active: true}}); err != nil {
		t.Fatalf("SaveDeliveryZones without Nag Hammadi: %v", err)
	}
	found := false
	for _, z := range s.GetDeliveryZones() {
		if z.Name == NagHammadiZone {
			found = true
			if z.Fee != 0 || !z.Active {
				t.Fatalf("re-added ناجي حمادي = %+v", z)
			}
		}
	}
	if !found {
		t.Fatal("ناجي حمادي must always be present after save")
	}
	// Fee lookup: substring match on an active zone; unknown address = closed.
	fee, ok := s.ZoneFeeForAddress("شارع النيل، ناجي حمادي")
	if !ok || fee != 0 {
		t.Fatalf("fee for Nag Hammadi address = %d ok=%v, want 0/true", fee, ok)
	}
	fee, ok = s.ZoneFeeForAddress("شارع النيل، بهجورة")
	if !ok || fee != 1500 {
		t.Fatalf("fee for بهجورة address = %d ok=%v, want 1500/true", fee, ok)
	}
	if _, ok := s.ZoneFeeForAddress("القاهرة"); ok {
		t.Fatal("unknown address must not match any zone")
	}
	// Duplicate names and negative fees are refused.
	if err := s.SaveDeliveryZones(ctx, admin, []DeliveryZone{{Name: "أ", Fee: 0, Active: true}, {Name: "أ", Fee: 0, Active: true}}); err == nil {
		t.Fatal("duplicate zone names should be refused")
	}
	if err := s.SaveDeliveryZones(ctx, admin, []DeliveryZone{{Name: "أ", Fee: -5, Active: true}}); err == nil {
		t.Fatal("negative zone fee should be refused")
	}
	// Malformed JSON is treated as no zones.
	_ = s.SetSetting("delivery_zones", "{broken")
	if zones := s.GetDeliveryZones(); len(zones) != 0 {
		t.Fatalf("malformed zones JSON should yield no zones, got %+v", zones)
	}
	if _, ok := s.ZoneFeeForAddress("ناجي حمادي"); ok {
		t.Fatal("malformed zones must close delivery")
	}
	// Cashier cannot save zones.
	cashier := &auth.User{ID: 2, UserName: "cash1", Role: "cashier"}
	if err := s.SaveDeliveryZones(ctx, cashier, []DeliveryZone{{Name: "أ", Fee: 0, Active: true}}); err == nil {
		t.Fatal("cashier should not save zones")
	}
}

func TestSettingsScaleAndCloud(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testAdmin()

	// Scale config validation.
	if err := ValidateScaleConfig(ScaleConfig{BarcodePrefix: "2A", ProductCodeLength: 5, ValueLength: 5, BarcodeMode: "WeightEmbedded", WeightUnit: "Kilogram", ValueDecimalPlaces: 3}); err == nil {
		t.Fatal("non-digit prefix should be refused")
	}
	if err := ValidateScaleConfig(ScaleConfig{BarcodePrefix: "20", ProductCodeLength: 0, ValueLength: 5, BarcodeMode: "WeightEmbedded", WeightUnit: "Kilogram", ValueDecimalPlaces: 3}); err == nil {
		t.Fatal("code length 0 should be refused")
	}
	if err := ValidateScaleConfig(ScaleConfig{BarcodePrefix: "20", ProductCodeLength: 5, ValueLength: 5, BarcodeMode: "WeightEmbedded", WeightUnit: "Kilogram", ValueDecimalPlaces: 6}); err == nil {
		t.Fatal("6 decimals should be refused")
	}
	cfg := ScaleConfig{IsEnabled: true, BarcodePrefix: "21", ProductCodeLength: 5, ValueLength: 5, BarcodeMode: "WeightEmbedded", HasCheckDigit: true, WeightUnit: "Kilogram", ValueDecimalPlaces: 3}
	if err := s.SaveScaleConfig(ctx, admin, cfg); err != nil {
		t.Fatalf("SaveScaleConfig: %v", err)
	}
	// B1's reader sees the saved config.
	saved, err := s.GetScaleSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Prefix != "21" {
		t.Fatalf("saved scale prefix = %q, want 21", saved.Prefix)
	}
	// B1's decoder works against the saved config (21 + 5-digit PLU + 5-digit value + check digit 2).
	_, err = s.DB.Exec(`INSERT INTO Products(Barcode,ProductName,BuyPrice,RetailPrice,Quantity,MinQuantity,IsWeighted,ScalePLU,PricePerKg,ScaleEnabled)
		VALUES('2100001012342','جبنة رومي',5000,10000,100000,5000,1,'00001',10000,1)`)
	if err != nil {
		t.Fatalf("seed weighted product: %v", err)
	}
	dec, err := s.DecodeScaleBarcode(ctx, "2100001012342")
	if err != nil {
		t.Fatalf("DecodeScaleBarcode: %v", err)
	}
	if dec.PLU != "1" {
		t.Fatalf("decoded PLU = %q, want 1 (trimmed)", dec.PLU)
	}
	if int64(dec.Weight) != 1234 {
		t.Fatalf("decoded weight = %d milli-kg, want 1234", dec.Weight)
	}

	// Cloud backup settings.
	if err := s.SaveCloudBackupSettings(ctx, admin, CloudBackupSettings{Enabled: true, Dir: "/tmp/x", Time: "25:00", Keep: 5}); err == nil {
		t.Fatal("invalid cloud time should be refused")
	}
	if err := s.SaveCloudBackupSettings(ctx, admin, CloudBackupSettings{Enabled: true, Dir: "/tmp/x", Time: "03:00", Keep: 0}); err == nil {
		t.Fatal("keep 0 should be refused")
	}
	if err := s.SaveCloudBackupSettings(ctx, admin, CloudBackupSettings{Enabled: true, Dir: "/tmp/x", Time: "03:00", Keep: 7}); err != nil {
		t.Fatalf("SaveCloudBackupSettings: %v", err)
	}
	got := s.GetCloudBackupSettings()
	if !got.Enabled || got.Dir != "/tmp/x" || got.Time != "03:00" || got.Keep != 7 {
		t.Fatalf("cloud settings = %+v", got)
	}
}

func TestSettingsResetDatabase(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testAdmin()
	mustCreateUser(t, s, "cash1", "cashier")

	if err := s.ResetDatabase(ctx, admin, "خطأ"); err == nil {
		t.Fatal("reset without the exact confirmation should be refused")
	}
	if err := s.ResetDatabase(ctx, admin, ResetConfirmation); err != nil {
		t.Fatalf("ResetDatabase: %v", err)
	}
	// Users are gone except the re-seeded admin; cash customer is back.
	users, err := s.ListUsers(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].UserName != "admin" {
		t.Fatalf("after reset users = %+v, want only admin", users)
	}
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM Customers WHERE CustomerID=1`).Scan(&n)
	if n != 1 {
		t.Fatal("cash customer should be re-seeded after reset")
	}
}

// TestFix19_AutoLockGlobalErrorWording pins the R19-D-F2 wording: the
// global save error must say "بلا مدة عامة" — global 0 means "no global
// value, per-user applies", not "disabled".
func TestFix19_AutoLockGlobalErrorWording(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	admin := testAdmin()
	err = s.SaveAutoLockMinutes(context.Background(), admin, 601)
	if err == nil {
		t.Fatal("auto-lock above 600 should be refused")
	}
	if !strings.Contains(err.Error(), "بلا مدة عامة") {
		t.Fatalf("global save error = %q, want it to say بلا مدة عامة", err.Error())
	}
	if strings.Contains(err.Error(), "معطل") {
		t.Fatalf("global save error = %q, must not label the global value معطل", err.Error())
	}
}
