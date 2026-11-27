// Package store — purchase from-to expiry: migrate() must add the
// ValidFrom column to databases created before the feature, and a
// purchase created afterwards must store and return it.
package store

import (
	"context"
	"path/filepath"
	"testing"
)

func testStoreHasColumn(t *testing.T, s *Store, table, column string) bool {
	t.Helper()
	rows, err := s.DB.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan pragma: %v", err)
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("pragma rows: %v", err)
	}
	return false
}

func TestPurchaseValidFrom_MigrationAddsColumn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open fresh DB: %v", err)
	}
	if !testStoreHasColumn(t, s, "PurchaseInvoiceDetails", "ValidFrom") {
		t.Fatalf("fresh DB lacks PurchaseInvoiceDetails.ValidFrom")
	}
	if !testStoreHasColumn(t, s, "ProductBatches", "ValidFrom") {
		t.Fatalf("fresh DB lacks ProductBatches.ValidFrom")
	}
	// Simulate a database created before the feature: drop the column
	// from both tables, then reopen — migrate() must re-add it.
	for _, q := range []string{
		`ALTER TABLE PurchaseInvoiceDetails DROP COLUMN ValidFrom`,
		`ALTER TABLE ProductBatches DROP COLUMN ValidFrom`,
	} {
		if _, err := s.DB.Exec(q); err != nil {
			t.Fatalf("drop column %q: %v", q, err)
		}
	}
	if testStoreHasColumn(t, s, "PurchaseInvoiceDetails", "ValidFrom") {
		t.Fatalf("simulated legacy DB still has PurchaseInvoiceDetails.ValidFrom after DROP")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close legacy DB: %v", err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen legacy DB: %v", err)
	}
	defer s2.Close()
	if !testStoreHasColumn(t, s2, "PurchaseInvoiceDetails", "ValidFrom") {
		t.Fatalf("migrate() did not re-add PurchaseInvoiceDetails.ValidFrom on reopen")
	}
	if !testStoreHasColumn(t, s2, "ProductBatches", "ValidFrom") {
		t.Fatalf("migrate() did not re-add ProductBatches.ValidFrom on reopen")
	}
	// An insert through the reopened (migrated) DB must store ValidFrom.
	ctx := context.Background()
	admin := b2TestUser(t, s2, "migadmin", "admin")
	vid, err := s2.CreateVendor(ctx, admin, VendorInput{Name: "مورد الهجرة"})
	if err != nil {
		t.Fatalf("create vendor: %v", err)
	}
	prod := testProductB2(t, s2, "سكر", 500, 1000, 100000)
	pid, _, err := s2.CreatePurchase(ctx, admin, PurchaseInput{
		VendorID: vid, Paid: 0, PaymentMethod: "Visa",
		Lines: []PurchaseLineInput{{ProductID: prod, Qty: 1000, PurchasePrice: 500,
			BatchNumber: "M-1", ExpiryDate: "2026-07-15", ValidFrom: "2026-01-15"}},
	})
	if err != nil {
		t.Fatalf("CreatePurchase on migrated DB: %v", err)
	}
	d, err := s2.GetPurchase(ctx, pid)
	if err != nil {
		t.Fatalf("GetPurchase: %v", err)
	}
	if len(d.Lines) != 1 || d.Lines[0].ValidFrom != "2026-01-15" {
		t.Fatalf("GetPurchase lines = %+v, want one line with ValidFrom 2026-01-15", d.Lines)
	}
	var batchVF string
	if err := s2.DB.QueryRow(`SELECT ValidFrom FROM ProductBatches WHERE PurchaseID=?`, pid).Scan(&batchVF); err != nil {
		t.Fatalf("batch ValidFrom query: %v", err)
	}
	if batchVF != "2026-01-15" {
		t.Fatalf("ProductBatches.ValidFrom = %q, want %q", batchVF, "2026-01-15")
	}
}
