package store

import (
	"context"
	"strings"
	"testing"

	"cashierpos-go/internal/money"
)

func TestB1StocktakeLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	cashier := b2TestUser(t, s, "b1stcash", "cashier")
	p1 := b1MustProduct(t, s, admin, "سكر") // stock 10
	p2 := b1MustProduct(t, s, admin, "زيت") // stock 10

	// Cashier cannot create a session.
	if _, err := s.CreateStocktake(ctx, cashier, "x"); err == nil {
		t.Fatal("cashier create stocktake should be denied")
	}
	st, err := s.CreateStocktake(ctx, admin, "جرد شهري")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if st.Status != "open" || len(st.Lines) != 0 {
		t.Fatalf("new session = %+v", st)
	}
	// Count: scan p1 twice (+1 each), set p2 to 7 by hand.
	if _, err := s.ScanStocktakeLine(ctx, cashier, st.ID, "no-such-barcode", nil); err == nil {
		t.Fatal("unknown barcode should error")
	}
	// p1 has no barcode; count it by product id.
	if _, err := s.SetStocktakeCounted(ctx, cashier, st.ID, p1.ID, 2000); err != nil {
		t.Fatalf("set counted: %v", err)
	}
	if _, err := s.SetStocktakeCounted(ctx, cashier, st.ID, p2.ID, 7000); err != nil {
		t.Fatalf("set counted p2: %v", err)
	}
	// Negative count refused.
	if _, err := s.SetStocktakeCounted(ctx, cashier, st.ID, p2.ID, -1000); err == nil {
		t.Fatal("negative counted should fail")
	}
	got, err := s.GetStocktake(ctx, admin, st.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Lines) != 2 {
		t.Fatalf("lines = %+v", got.Lines)
	}
	for _, ln := range got.Lines {
		if ln.ProductID == p1.ID && (ln.SystemQty != 10000 || ln.CountedQty != 2000 || ln.Difference != -8000) {
			t.Fatalf("p1 line = %+v", ln)
		}
		if ln.ProductID == p2.ID && ln.Difference != -3000 {
			t.Fatalf("p2 line = %+v", ln)
		}
	}
	// Approve before close refused.
	if _, err := s.ApproveStocktake(ctx, admin, st.ID, "اعتماد"); err == nil {
		t.Fatal("approve open session should fail")
	}
	// Cashier cannot close.
	if _, err := s.CloseStocktake(ctx, cashier, st.ID); err == nil {
		t.Fatal("cashier close should be denied")
	}
	// Close freezes counting.
	closed, err := s.CloseStocktake(ctx, admin, st.ID)
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if closed.Status != "closed" {
		t.Fatalf("status = %s", closed.Status)
	}
	if _, err := s.SetStocktakeCounted(ctx, cashier, st.ID, p1.ID, 5000); err == nil {
		t.Fatal("counting a closed session should fail")
	}
	// Cancel of a closed session refused.
	if _, err := s.CancelStocktake(ctx, admin, st.ID); err == nil {
		t.Fatal("cancel closed session should fail")
	}
	// Approve without reason refused.
	if _, err := s.ApproveStocktake(ctx, admin, st.ID, ""); err == nil {
		t.Fatal("approve without reason should fail")
	}
	// Approve: p1 10 -> 2, p2 10 -> 7 (snapshot-relative deltas).
	approved, err := s.ApproveStocktake(ctx, admin, st.ID, "اعتماد الجرد الشهري")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if approved.ApprovedAt == "" || approved.ApprovedBy != admin.UserName {
		t.Fatalf("approval stamp = %+v", approved)
	}
	if approved.Status != "closed" {
		t.Fatalf("status after approval = %s (must stay closed)", approved.Status)
	}
	g1, _ := s.GetProduct(ctx, admin, p1.ID)
	g2, _ := s.GetProduct(ctx, admin, p2.ID)
	if g1.Quantity != 2000 || g2.Quantity != 7000 {
		t.Fatalf("stock after approve: p1=%d p2=%d", g1.Quantity, g2.Quantity)
	}
	// Movements written.
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM StockMovements WHERE MovementType='adjust'`).Scan(&n)
	if n != 2 {
		t.Fatalf("adjust movements = %d, want 2", n)
	}
	// Second approval is a true no-op (no duplicate movements).
	if _, err := s.ApproveStocktake(ctx, admin, st.ID, "اعتماد مرة أخرى"); err != nil {
		t.Fatalf("re-approve: %v", err)
	}
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM StockMovements WHERE MovementType='adjust'`).Scan(&n)
	if n != 2 {
		t.Fatalf("re-approve duplicated movements: %d", n)
	}
	g1, _ = s.GetProduct(ctx, admin, p1.ID)
	if g1.Quantity != 2000 {
		t.Fatalf("re-approve changed stock: %d", g1.Quantity)
	}
}

func TestB1StocktakeCancelAndNonCountedUntouched(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	cashier := b2TestUser(t, s, "b1stcash2", "cashier")
	p1 := b1MustProduct(t, s, admin, "سكر")
	p2 := b1MustProduct(t, s, admin, "زيت")
	p3 := b1MustProduct(t, s, admin, "أرز") // never counted

	st, _ := s.CreateStocktake(ctx, admin, "")
	// Cancel while open works and changes nothing.
	cancelled, err := s.CancelStocktake(ctx, admin, st.ID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled.Status != "cancelled" {
		t.Fatalf("status = %s", cancelled.Status)
	}
	if _, err := s.ApproveStocktake(ctx, admin, st.ID, "x"); err == nil {
		t.Fatal("approve cancelled should fail")
	}
	if _, err := s.CloseStocktake(ctx, admin, st.ID); err == nil {
		t.Fatal("close cancelled should fail")
	}

	// Partial count: only p1 counted; p2/p3 untouched by approval.
	st2, _ := s.CreateStocktake(ctx, admin, "جزئي")
	if _, err := s.SetStocktakeCounted(ctx, cashier, st2.ID, p1.ID, 5000); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseStocktake(ctx, admin, st2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApproveStocktake(ctx, admin, st2.ID, "اعتماد جزئي"); err != nil {
		t.Fatalf("approve partial: %v", err)
	}
	g1, _ := s.GetProduct(ctx, admin, p1.ID)
	g2, _ := s.GetProduct(ctx, admin, p2.ID)
	g3, _ := s.GetProduct(ctx, admin, p3.ID)
	if g1.Quantity != 5000 || g2.Quantity != 10000 || g3.Quantity != 10000 {
		t.Fatalf("partial approve: p1=%d p2=%d p3=%d", g1.Quantity, g2.Quantity, g3.Quantity)
	}
	// Zero-difference line writes no movement.
	st3, _ := s.CreateStocktake(ctx, admin, "بلا فرق")
	if _, err := s.SetStocktakeCounted(ctx, cashier, st3.ID, p2.ID, 10000); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseStocktake(ctx, admin, st3.ID); err != nil {
		t.Fatal(err)
	}
	var before int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM StockMovements WHERE ProductID=?`, p2.ID).Scan(&before)
	if _, err := s.ApproveStocktake(ctx, admin, st3.ID, "لا فرق"); err != nil {
		t.Fatalf("approve zero-diff: %v", err)
	}
	var after int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM StockMovements WHERE ProductID=?`, p2.ID).Scan(&after)
	if after != before {
		t.Fatalf("zero-diff wrote movements: %d -> %d", before, after)
	}
}

func TestB1StocktakeScanBarcodeAndNegativeApproval(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	cashier := b2TestUser(t, s, "b1stcash3", "cashier")
	// Piece product with a barcode: each scan adds 1.
	p := b1MustProduct(t, s, admin, "سكر")
	upd := b1ProductInput("سكر")
	upd.Barcode = "6221001001"
	upd.Quantity = 10000
	upd.MinQuantity = 5000
	if _, err := s.UpdateProduct(ctx, admin, p.ID, upd); err != nil {
		t.Fatalf("set barcode: %v", err)
	}
	st, _ := s.CreateStocktake(ctx, admin, "")
	ln, err := s.ScanStocktakeLine(ctx, cashier, st.ID, "6221001001", nil)
	if err != nil {
		t.Fatalf("scan 1: %v", err)
	}
	if ln.CountedQty != 1000 || ln.SystemQty != 10000 {
		t.Fatalf("scan line = %+v", ln)
	}
	ln, err = s.ScanStocktakeLine(ctx, cashier, st.ID, "6221001001", nil)
	if err != nil || ln.CountedQty != 2000 {
		t.Fatalf("scan 2: %+v %v", ln, err)
	}
	// Explicit qty SETS instead of adding.
	q := money.Qty(5000)
	ln, err = s.ScanStocktakeLine(ctx, cashier, st.ID, "6221001001", &q)
	if err != nil || ln.CountedQty != 5000 {
		t.Fatalf("scan set: %+v %v", ln, err)
	}
	// Scale barcode scan adds the decoded weight.
	win := b1ProductInput("جبنة رومي")
	win.IsWeighted = true
	win.ScalePLU = "123"
	win.PricePerKg = 2000
	win.ScaleEnabled = true
	win.Quantity = 50000
	wp, err := s.CreateProduct(ctx, admin, win)
	if err != nil {
		t.Fatalf("create weighted: %v", err)
	}
	code := b1ScaleBarcode("00123", "01500")
	ln, err = s.ScanStocktakeLine(ctx, cashier, st.ID, code, nil)
	if err != nil || ln.CountedQty != 1500 {
		t.Fatalf("scale scan: %+v %v", ln, err)
	}
	ln, err = s.ScanStocktakeLine(ctx, cashier, st.ID, code, nil)
	if err != nil || ln.CountedQty != 3000 {
		t.Fatalf("scale scan 2 (accumulates): %+v %v", ln, err)
	}
	_ = wp
	// Approval that would drive a product negative is refused outright.
	st2, _ := s.CreateStocktake(ctx, admin, "")
	if _, err := s.SetStocktakeCounted(ctx, cashier, st2.ID, p.ID, 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseStocktake(ctx, admin, st2.ID); err != nil {
		t.Fatal(err)
	}
	// Sell 9 of 10 units meanwhile: live stock 1000, delta -9000 -> negative.
	if _, err := s.DB.Exec(`UPDATE Products SET Quantity=1000 WHERE ProductID=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApproveStocktake(ctx, admin, st2.ID, "اعتماد"); err == nil || !strings.Contains(err.Error(), "سالبة") {
		t.Fatalf("negative approval err = %v", err)
	}
	got, _ := s.GetProduct(ctx, admin, p.ID)
	if got.Quantity != 1000 {
		t.Fatalf("refused approval changed stock: %d", got.Quantity)
	}
}
