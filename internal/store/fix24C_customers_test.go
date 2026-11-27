package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"cashierpos-go/internal/money"
)

// --- FIX-24 lane C: R24-E-N1 (GetCustomer leaks a raw SQLite overflow as 404) ---
//
// GetCustomer calls LiveCustomerBalanceDB, whose SUM(RemainingAmount) had no
// sumScanErr, and apiCustomerGet mapped ANY non-denial error to 404 verbatim:
// a customer with two 5e18-remaining invoices answered
// 404 {"error": "SQL logic error: integer overflow (1)"}.
// The fix maps the SUM scan through sumScanErr in both LiveCustomerBalance
// variants (money.ErrOverflow, Arabic) and gives apiCustomerGet the
// R23-E-F1 trichotomy: denial -> 403, overflow -> 400, "العميل غير موجود"
// -> 404, anything else -> 500 generic.
//
// Poison values are written by direct DB insert only (legacy poison);
// no app path is used to store them.

const fix24CBig = int64(5000000000000000000) // 5e18

// fix24CPoisonInvoices plants two 5e18-remaining invoices for one customer.
func fix24CPoisonInvoices(t *testing.T, s *Store, custID, shiftID int64, userID int64) {
	t.Helper()
	for n := int64(9101); n <= 9102; n++ {
		if _, err := s.DB.Exec(`INSERT INTO Invoices(InvoiceNumber,CustomerID,ShiftID,TotalAmount,Discount,
			PromoDiscount,NetAmount,PaidAmount,RemainingAmount,PaymentMethod,UserName,UserID,InvoiceDate,IsCanceled)
			VALUES(?,?,?,?,0,0,?,?,?,'Cash','admin',?,?,0)`,
			n, custID, shiftID, fix24CBig, fix24CBig, 0, fix24CBig, userID, Today()); err != nil {
			t.Fatalf("insert poisoned invoice: %v", err)
		}
	}
}

func TestFix24C_LiveCustomerBalanceDB_Overflow(t *testing.T) {
	s := testStore(t)
	u := b2TestUser(t, s, "fix24c-admin", "admin")
	shiftID, err := s.OpenShift(context.Background(), u, money.Cents(100000), false)
	if err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	cust := testCustomer(t, s, "عميل مسموم")
	fix24CPoisonInvoices(t, s, cust, shiftID, u.ID)
	_, err = s.LiveCustomerBalanceDB(cust)
	if err == nil {
		t.Fatalf("expected LiveCustomerBalanceDB to refuse on SUM overflow, got nil error")
	}
	if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("balance err = %v, want money.ErrOverflow", err)
	}
	if strings.Contains(err.Error(), "SQL logic error") || strings.Contains(err.Error(), "integer overflow") {
		t.Fatalf("balance err leaks raw SQLite text: %v", err)
	}
}

func TestFix24C_LiveCustomerBalanceTx_Overflow(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "fix24c-admin", "admin")
	shiftID, err := s.OpenShift(ctx, u, money.Cents(100000), false)
	if err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	cust := testCustomer(t, s, "عميل مسموم")
	fix24CPoisonInvoices(t, s, cust, shiftID, u.ID)
	var got error
	if err := s.Tx(ctx, func(tx *sql.Tx) error {
		_, got = LiveCustomerBalance(tx, cust)
		return nil
	}); err != nil {
		t.Fatalf("Tx: %v", err)
	}
	if got == nil {
		t.Fatalf("expected LiveCustomerBalance(tx) to refuse on SUM overflow, got nil error")
	}
	if !errors.Is(got, money.ErrOverflow) {
		t.Fatalf("balance(tx) err = %v, want money.ErrOverflow", got)
	}
	if strings.Contains(got.Error(), "SQL logic error") || strings.Contains(got.Error(), "integer overflow") {
		t.Fatalf("balance(tx) err leaks raw SQLite text: %v", got)
	}
}

func TestFix24C_GetCustomer_OverflowRefuses(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "fix24c-admin", "admin")
	shiftID, err := s.OpenShift(ctx, u, money.Cents(100000), false)
	if err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	cust := testCustomer(t, s, "عميل مسموم")
	fix24CPoisonInvoices(t, s, cust, shiftID, u.ID)
	_, err = s.GetCustomer(ctx, u, cust)
	if err == nil {
		t.Fatalf("expected GetCustomer to refuse on poisoned balance, got nil error")
	}
	if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("GetCustomer err = %v, want money.ErrOverflow", err)
	}
}

func TestFix24C_GetCustomer_NormalAndMissing(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "fix24c-admin", "admin")
	shiftID, err := s.OpenShift(ctx, u, money.Cents(100000), false)
	if err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	cust := testCustomer(t, s, "عميل عادي")
	invID := testSaleInvoice(t, s, u, shiftID, 9201, testProductB2(t, s, "أرز", 1, 100, 100000),
		2000, 100, 1, 0, 2500, "Cash", cust)
	_ = invID
	c, err := s.GetCustomer(ctx, u, cust)
	if err != nil {
		t.Fatalf("GetCustomer normal: %v", err)
	}
	if c.Balance != money.Cents(2500) {
		t.Fatalf("normal customer Balance = %d, want 2500", int64(c.Balance))
	}
	_, err = s.GetCustomer(ctx, u, 999999)
	if err == nil || err.Error() != "العميل غير موجود" {
		t.Fatalf("GetCustomer missing: err = %v, want العميل غير موجود", err)
	}
}

// --- Re-audit guards: the other single-entity getters carry no SUM, so
// absurd STORED values must still load fine (no overflow refusal, no 404).
// Each test re-derives the getter's queries from code (plain SELECTs only).

func TestFix24C_ReAudit_GetInvoiceHugeStoredOK(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "fix24c-admin", "admin")
	shiftID, err := s.OpenShift(ctx, u, money.Cents(100000), false)
	if err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	cust := testCustomer(t, s, "عميل عادي")
	pid := testProductB2(t, s, "أرز", 1, 100, 100000)
	invID := testSaleInvoice(t, s, u, shiftID, 9301, pid, 2000, 100, 1, 200, 0, "Cash", cust)
	if _, err := s.DB.Exec(`UPDATE Invoices SET TotalAmount=?,NetAmount=?,PaidAmount=?,RemainingAmount=0 WHERE InvoiceID=?`,
		fix24CBig, fix24CBig, fix24CBig, invID); err != nil {
		t.Fatalf("plant huge invoice: %v", err)
	}
	d, err := s.GetInvoice(ctx, u, invID)
	if err != nil {
		t.Fatalf("GetInvoice huge stored: %v", err)
	}
	if d.Summary.Net != money.Cents(fix24CBig) {
		t.Fatalf("invoice Net = %d, want %d", int64(d.Summary.Net), fix24CBig)
	}
}

func TestFix24C_ReAudit_GetProductHugeStoredOK(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "fix24c-admin", "admin")
	pid := testProductB2(t, s, "أرز", 1, 100, 100000)
	if _, err := s.DB.Exec(`UPDATE Products SET BuyPrice=?,RetailPrice=?,WholesalePrice=?,Barcode='C24BIG' WHERE ProductID=?`,
		fix24CBig, fix24CBig, fix24CBig, pid); err != nil {
		t.Fatalf("plant huge product: %v", err)
	}
	p, err := s.GetProduct(ctx, u, pid)
	if err != nil {
		t.Fatalf("GetProduct huge stored: %v", err)
	}
	if p.RetailPrice != money.Cents(fix24CBig) {
		t.Fatalf("product RetailPrice = %d, want %d", int64(p.RetailPrice), fix24CBig)
	}
	_, dec, err := s.GetProductByBarcode(ctx, u, "C24BIG")
	if err != nil {
		t.Fatalf("GetProductByBarcode huge stored: %v", err)
	}
	if dec != nil {
		t.Fatalf("plain barcode must not decode as scale, got %+v", dec)
	}
}

func TestFix24C_ReAudit_GetPurchaseHugeStoredOK(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	vid, err := s.CreateVendor(ctx, admin, VendorInput{Name: "مورد الشراء"})
	if err != nil {
		t.Fatalf("CreateVendor: %v", err)
	}
	prod := testProductB2(t, s, "سكر", 1000, 2000, 5000)
	pid, _, err := s.CreatePurchase(ctx, admin, PurchaseInput{
		VendorID: vid,
		Lines:    []PurchaseLineInput{{ProductID: prod, Qty: money.Qty(2500), PurchasePrice: money.Cents(1200)}},
		Paid:     money.Cents(1000), PaymentMethod: "Visa", DueDate: "2026-12-01",
	})
	if err != nil {
		t.Fatalf("CreatePurchase: %v", err)
	}
	if _, err := s.DB.Exec(`UPDATE PurchaseInvoices SET TotalAmount=?,PaidAmount=?,RemainingAmount=? WHERE PurchaseID=?`,
		fix24CBig, fix24CBig, fix24CBig, pid); err != nil {
		t.Fatalf("plant huge purchase: %v", err)
	}
	d, err := s.GetPurchase(ctx, pid)
	if err != nil {
		t.Fatalf("GetPurchase huge stored: %v", err)
	}
	if d.Purchase.Total != money.Cents(fix24CBig) {
		t.Fatalf("purchase Total = %d, want %d", int64(d.Purchase.Total), fix24CBig)
	}
}

func TestFix24C_ReAudit_GetStocktakeHugeCountedOK(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	st, err := s.CreateStocktake(ctx, admin, "جرد اختبار")
	if err != nil {
		t.Fatalf("CreateStocktake: %v", err)
	}
	pid := testProductB2(t, s, "أرز", 1, 100, 100000)
	if _, err := s.DB.Exec(`INSERT INTO StocktakeLines(StocktakeID,ProductID,ProductName,SystemQty,CountedQty,CreatedAt)
		VALUES(?,?,?,?,?,?)`, st.ID, pid, "أرز", 0, fix24CBig, Today()); err != nil {
		t.Fatalf("plant huge stocktake line: %v", err)
	}
	got, err := s.GetStocktake(ctx, admin, st.ID)
	if err != nil {
		t.Fatalf("GetStocktake huge counted: %v", err)
	}
	if len(got.Lines) != 1 || got.Lines[0].Difference != money.Qty(fix24CBig) {
		t.Fatalf("stocktake lines = %+v, want one line Difference %d", got.Lines, fix24CBig)
	}
}

func TestFix24C_ReAudit_GetShiftAndNotifRead(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	shiftID, err := s.OpenShift(ctx, admin, money.Cents(100000), false)
	if err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	// GetShift is a plain SELECT of stored columns (getShiftTx): no SUM,
	// so it cannot carry an overflow error.
	if _, err := s.GetShift(ctx, shiftID); err != nil {
		t.Fatalf("GetShift: %v", err)
	}
	if _, err := s.GetShift(ctx, 999999); err == nil || err.Error() != "الشيفت غير موجود" {
		t.Fatalf("GetShift missing: err = %v, want الشيفت غير موجود", err)
	}
	// MarkNotificationRead is an UPDATE + RowsAffected check: no SUM,
	// no arithmetic; a missing row is the Arabic not-found error.
	err = s.MarkNotificationRead(ctx, admin, 999999)
	if err == nil || err.Error() != "التنبيه غير موجود" {
		t.Fatalf("MarkNotificationRead missing: err = %v, want التنبيه غير موجود", err)
	}
}
