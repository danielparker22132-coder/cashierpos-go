package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"cashierpos-go/internal/money"
)

// fix-22 lane B (R22-B-F1): AddStock stored a wrapped negative stock.
//
// Re-derived mechanism (see lane REPORT.md): AddStock did a plain
// `newQty := p.Quantity + qty`, so a stored top-band stock
// 9223372036854775000 + 1000 milli wrapped to -9223372036854775616
// and was STORED, with a StockMovements row and an audit row. The
// fix uses money.AddQtyChecked, refusing with money.ErrOverflow
// (Arabic overflow message) and writing nothing. The live driver
// also re-verifies the two sibling guards the review cites —
// purchase (domain_vendors.go) and stocktake (domain_stocktake.go)
// — which both carry a `< 0` guard that catches every
// positive-operand wrap; the tests below pin that behaviour too.

const fix22TopBandQty = money.Qty(9223372036854775000)

func fix22SetQty(t *testing.T, s *Store, pid int64, q money.Qty) {
	t.Helper()
	if _, err := s.DB.Exec(`UPDATE Products SET Quantity=? WHERE ProductID=?`, int64(q), pid); err != nil {
		t.Fatalf("set qty: %v", err)
	}
}

func fix22CountInt(t *testing.T, s *Store, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow(sql, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", sql, err)
	}
	return n
}

func TestFix22_AddStockRefusesTopBand(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	wh := b2TestUser(t, s, "f22wh", "warehouse")
	pid := testProductB2(t, s, "أرز", 1, 100, 97000)
	fix22SetQty(t, s, pid, fix22TopBandQty)
	mov0 := fix22CountInt(t, s, `SELECT COUNT(*) FROM StockMovements WHERE ProductID=?`, pid)
	aud0 := fix22CountInt(t, s, `SELECT COUNT(*) FROM AuditLog`)
	if _, err := s.AddStock(ctx, wh, pid, money.Qty(1000)); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("AddStock(top-band, 1 unit): err = %v, want money.ErrOverflow", err)
	}
	if got := productQty(t, s, pid); got != fix22TopBandQty {
		t.Fatalf("stock after refused AddStock = %d, want %d (unchanged)", got, fix22TopBandQty)
	}
	if n := fix22CountInt(t, s, `SELECT COUNT(*) FROM StockMovements WHERE ProductID=?`, pid); n != mov0 {
		t.Fatalf("StockMovements rows after refused AddStock = %d, want %d", n, mov0)
	}
	if n := fix22CountInt(t, s, `SELECT COUNT(*) FROM AuditLog`); n != aud0 {
		t.Fatalf("AuditLog rows after refused AddStock = %d, want %d", n, aud0)
	}
}

func TestFix22_AddStockNormalWorks(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	wh := b2TestUser(t, s, "f22wh2", "warehouse")
	pid := testProductB2(t, s, "أرز", 1, 100, 97000)
	mov0 := fix22CountInt(t, s, `SELECT COUNT(*) FROM StockMovements WHERE ProductID=?`, pid)
	aud0 := fix22CountInt(t, s, `SELECT COUNT(*) FROM AuditLog`)
	if _, err := s.AddStock(ctx, wh, pid, money.Qty(1000)); err != nil {
		t.Fatalf("AddStock(97000, 1 unit): %v", err)
	}
	if got := productQty(t, s, pid); got != 98000 {
		t.Fatalf("stock after normal AddStock = %d, want 98000", got)
	}
	if n := fix22CountInt(t, s, `SELECT COUNT(*) FROM StockMovements WHERE ProductID=?`, pid); n != mov0+1 {
		t.Fatalf("StockMovements rows after normal AddStock = %d, want %d", n, mov0+1)
	}
	if n := fix22CountInt(t, s, `SELECT COUNT(*) FROM AuditLog`); n != aud0+1 {
		t.Fatalf("AuditLog rows after normal AddStock = %d, want %d", n, aud0+1)
	}
}

func TestFix22_PurchaseRefusesTopBandWrap(t *testing.T) {
	// Sibling guard (domain_vendors.go:540-542): curQty + line qty
	// wraps to a negative, so the `< 0` guard must refuse the whole
	// purchase and store nothing.
	s := testStore(t)
	ctx := context.Background()
	admin := b2TestUser(t, s, "f22adm", "admin")
	vid, err := s.CreateVendor(ctx, admin, VendorInput{Name: "مورد اختبار"})
	if err != nil {
		t.Fatalf("create vendor: %v", err)
	}
	pid := testProductB2(t, s, "أرز", 1, 100, 97000)
	fix22SetQty(t, s, pid, fix22TopBandQty)
	pur0 := fix22CountInt(t, s, `SELECT COUNT(*) FROM PurchaseInvoices`)
	mov0 := fix22CountInt(t, s, `SELECT COUNT(*) FROM StockMovements WHERE ProductID=?`, pid)
	_, _, err = s.CreatePurchase(ctx, admin, PurchaseInput{
		VendorID: vid,
		Lines:    []PurchaseLineInput{{ProductID: pid, Qty: money.Qty(1000), PurchasePrice: money.Cents(100)}},
		Paid:     0, PaymentMethod: "Cash",
	})
	if err == nil || !strings.Contains(err.Error(), "السالب ممنوع") {
		t.Fatalf("CreatePurchase(top-band): err = %v, want السالب ممنوع refusal", err)
	}
	if got := productQty(t, s, pid); got != fix22TopBandQty {
		t.Fatalf("stock after refused purchase = %d, want %d (unchanged)", got, fix22TopBandQty)
	}
	if n := fix22CountInt(t, s, `SELECT COUNT(*) FROM PurchaseInvoices`); n != pur0 {
		t.Fatalf("PurchaseInvoices rows after refused purchase = %d, want %d", n, pur0)
	}
	if n := fix22CountInt(t, s, `SELECT COUNT(*) FROM StockMovements WHERE ProductID=?`, pid); n != mov0 {
		t.Fatalf("StockMovements rows after refused purchase = %d, want %d", n, mov0)
	}
}

func TestFix22_StocktakeRefusesTopBandWrap(t *testing.T) {
	// Sibling guard (domain_stocktake.go:474-476, plus the pre-check
	// at :431-435): a positive delta that wraps the stored stock must
	// be refused and the session left unapproved, nothing applied.
	// Snapshot system qty small (1000 milli), counted 100000 units,
	// so delta = +99999000 milli; the stored stock is then moved to
	// the top band before approval.
	s := testStore(t)
	ctx := context.Background()
	admin := b2TestUser(t, s, "f22adm2", "admin")
	wh := b2TestUser(t, s, "f22wh3", "warehouse")
	pid := testProductB2(t, s, "أرز", 1, 100, 1000)
	st, err := s.CreateStocktake(ctx, admin, "اختبار الجرد")
	if err != nil {
		t.Fatalf("create stocktake: %v", err)
	}
	if _, err := s.SetStocktakeCounted(ctx, wh, st.ID, pid, money.Qty(100000000)); err != nil {
		t.Fatalf("set counted: %v", err)
	}
	fix22SetQty(t, s, pid, fix22TopBandQty)
	if _, err := s.CloseStocktake(ctx, admin, st.ID); err != nil {
		t.Fatalf("close stocktake: %v", err)
	}
	mov0 := fix22CountInt(t, s, `SELECT COUNT(*) FROM StockMovements WHERE ProductID=?`, pid)
	_, err = s.ApproveStocktake(ctx, admin, st.ID, "اعتماد اختبار")
	if err == nil || !strings.Contains(err.Error(), "سالبة") {
		t.Fatalf("ApproveStocktake(top-band): err = %v, want سالبة refusal", err)
	}
	if got := productQty(t, s, pid); got != fix22TopBandQty {
		t.Fatalf("stock after refused stocktake = %d, want %d (unchanged)", got, fix22TopBandQty)
	}
	if n := fix22CountInt(t, s, `SELECT COUNT(*) FROM StockMovements WHERE ProductID=?`, pid); n != mov0 {
		t.Fatalf("StockMovements rows after refused stocktake = %d, want %d", n, mov0)
	}
	got, err := s.GetStocktake(ctx, admin, st.ID)
	if err != nil {
		t.Fatalf("get stocktake: %v", err)
	}
	if got.ApprovedAt != "" {
		t.Fatalf("stocktake was approved despite the refusal")
	}
}
