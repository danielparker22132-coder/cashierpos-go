// Package store — FIX-17 lane E tests (SW2-L02 cancel audit detail,
// SW2-L10 warehouse invoice view, SW2-L11 below-cost dead-end prompt).
package store

import (
	"errors"
	"strings"
	"testing"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/money"
)

// fix17Product returns the id of a product with buy 2000 / retail 3000, stock 10.
func fix17Product(t *testing.T, s *Store) int64 {
	t.Helper()
	return testProduct(t, s, "fix17-below", 2000, 3000, 10000)
}

func fix17InvoiceCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM Invoices`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestFix17BelowCostDeniedUpfront covers SW2-L11 at the service layer,
// both prompt paths: the nominal unit-price check (prepareSaleLines) and
// the net-of-discount check (CreateInvoice). A role that can never confirm
// (no PermApproveBelowCost) gets the uniform denial immediately; a role
// that can confirm still gets the 400 confirm prompt when unflagged.
func TestFix17BelowCostDeniedUpfront(t *testing.T) {
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	p := fix17Product(t, s)
	wh := b2TestUser(t, s, "fix17wh", "warehouse")
	_ = wh
	for _, u := range []*auth.User{env.cashier, env.sup, env.admin, env.manager} {
		b1OpenShiftFor(t, s, u)
	}

	sale := func(u *auth.User, unit money.Cents, disc money.Cents, flag bool) error {
		_, err := s.CreateInvoice(env.ctx, u, CreateInvoiceInput{
			CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
			Lines: []SaleLineInput{{ProductID: p, Qty: 1000, UnitPrice: unit, ItemDiscount: disc}},
			Paid: 1900, AllowBelowCost: flag,
		})
		return err
	}

	// --- Unit-price path: unit 1900 < buy 2000. ---
	before := fix17InvoiceCount(t, s)
	if err := sale(env.sup, 1900, 0, false); err == nil || !strings.HasPrefix(err.Error(), "غير مسموح") {
		t.Fatalf("supervisor unit-price unflagged: want denial, got %v", err)
	}
	if err := sale(env.cashier, 1900, 0, false); err == nil || !strings.HasPrefix(err.Error(), "غير مسموح") {
		t.Fatalf("cashier unit-price unflagged: want denial, got %v", err)
	}
	if err := sale(env.admin, 1900, 0, false); err == nil || !strings.Contains(err.Error(), "هل تريد البيع بخسارة") {
		t.Fatalf("admin unit-price unflagged: want confirm prompt, got %v", err)
	}
	if err := sale(env.manager, 1900, 0, false); err == nil || !strings.Contains(err.Error(), "هل تريد البيع بخسارة") {
		t.Fatalf("manager unit-price unflagged: want confirm prompt, got %v", err)
	}
	if err := sale(env.sup, 1900, 0, true); err == nil || !strings.HasPrefix(err.Error(), "غير مسموح") {
		t.Fatalf("supervisor unit-price flagged: want denial, got %v", err)
	}
	if _, err := s.CreateInvoice(env.ctx, env.admin, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
		Lines: []SaleLineInput{{ProductID: p, Qty: 1000, UnitPrice: 1900}},
		Paid: 1900, AllowBelowCost: true,
	}); err != nil {
		t.Fatalf("admin unit-price flagged: want success, got %v", err)
	}

	// --- Net-of-discount path: retail 3000, item discount 1100 -> net 1900 < buy 2000. ---
	if err := sale(env.sup, 0, 1100, false); err == nil || !strings.HasPrefix(err.Error(), "غير مسموح") {
		t.Fatalf("supervisor net unflagged: want denial, got %v", err)
	}
	// Cashier: the discount gate fires first (no discount perm) — still an
	// immediate denial, never the prompt.
	if err := sale(env.cashier, 0, 1100, false); err == nil || !strings.HasPrefix(err.Error(), "غير مسموح") {
		t.Fatalf("cashier net unflagged: want denial (discount gate), got %v", err)
	}
	if err := sale(env.admin, 0, 1100, false); err == nil || !strings.Contains(err.Error(), "هل تريد البيع بخسارة") {
		t.Fatalf("admin net unflagged: want confirm prompt, got %v", err)
	}
	if err := sale(env.sup, 0, 1100, true); err == nil || !strings.HasPrefix(err.Error(), "غير مسموح") {
		t.Fatalf("supervisor net flagged: want denial, got %v", err)
	}
	if _, err := s.CreateInvoice(env.ctx, env.admin, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
		Lines: []SaleLineInput{{ProductID: p, Qty: 1000, ItemDiscount: 1100}},
		Paid: 1900, AllowBelowCost: true,
	}); err != nil {
		t.Fatalf("admin net flagged: want success, got %v", err)
	}
	// Only the two admin flagged sales posted.
	if got := fix17InvoiceCount(t, s); got != before+2 {
		t.Fatalf("invoice count = %d, want %d (denials must post nothing)", got, before+2)
	}
}

// TestFix17WarehouseCanViewInvoice covers SW2-L10 at the service layer:
// warehouse reads any invoice; cashier stays own-only.
func TestFix17WarehouseCanViewInvoice(t *testing.T) {
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	p := b1MustProduct(t, s, env.admin, "سكر")
	b1OpenShiftFor(t, s, env.cashier)
	res := b1CashCustomerSale(t, s, env, env.cashier, p.ID, 1000)
	wh := b2TestUser(t, s, "fix17wh2", "warehouse")
	if _, err := s.GetInvoice(env.ctx, wh, res.InvoiceID); err != nil {
		t.Fatalf("warehouse GetInvoice: want success, got %v", err)
	}
	otherCash := b2TestUser(t, s, "fix17cash2", "cashier")
	if _, err := s.GetInvoice(env.ctx, otherCash, res.InvoiceID); !errors.Is(err, ErrInvoiceForbidden) {
		t.Fatalf("other cashier GetInvoice: want ErrInvoiceForbidden, got %v", err)
	}
}

// TestFix17CancelAuditDetails covers the SW2-L02 cancel half: per-line
// detail rows (product, qty/price before-after, line total) on the cancel
// audit entry, mirroring the sale path.
func TestFix17CancelAuditDetails(t *testing.T) {
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	p1 := testProduct(t, s, "fix17-c1", 1000, 2000, 10000)
	p2 := testProduct(t, s, "fix17-c2", 500, 1500, 10000)
	b1OpenShiftFor(t, s, env.cashier)
	res, err := s.CreateInvoice(env.ctx, env.cashier, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
		Lines: []SaleLineInput{
			{ProductID: p1, Qty: 2000},
			{ProductID: p2, Qty: 1000},
		},
		Paid: 5500,
	})
	if err != nil {
		t.Fatalf("sale: %v", err)
	}
	// Stock after sale: p1 8, p2 9.
	if _, err := s.CancelInvoice(env.ctx, env.admin, res.InvoiceID, "اختبار إلغاء"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	var logID int64
	if err := s.DB.QueryRow(`SELECT LogID FROM AuditLog WHERE ActionType='إلغاء فاتورة' AND InvoiceID=? ORDER BY LogID DESC LIMIT 1`,
		res.InvoiceID).Scan(&logID); err != nil {
		t.Fatalf("cancel audit log missing: %v", err)
	}
	rows, err := s.DB.Query(`SELECT ProductID,QuantityBefore,QuantityAfter,QuantityChange,
		PriceBefore,PriceAfter,LineTotal FROM AuditLogDetails WHERE LogID=? ORDER BY DetailID`, logID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type det struct {
		pid      int64
		qb, qa   money.Qty
		qc       money.Qty
		pb, pa   money.Cents
		lt       money.Cents
	}
	var got []det
	for rows.Next() {
		var d det
		var qb, qa, qc, pb, pa, lt int64
		if err := rows.Scan(&d.pid, &qb, &qa, &qc, &pb, &pa, &lt); err != nil {
			t.Fatal(err)
		}
		d.qb, d.qa, d.qc = money.Qty(qb), money.Qty(qa), money.Qty(qc)
		d.pb, d.pa, d.lt = money.Cents(pb), money.Cents(pa), money.Cents(lt)
		got = append(got, d)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []det{
		{pid: p1, qb: 8000, qa: 10000, qc: 2000, pb: 2000, pa: 2000, lt: 4000},
		{pid: p2, qb: 9000, qa: 10000, qc: 1000, pb: 1500, pa: 1500, lt: 1500},
	}
	if len(got) != len(want) {
		t.Fatalf("cancel audit details = %d rows %+v, want %d %+v", len(got), got, len(want), want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("detail row %d = %+v, want %+v", i, got[i], w)
		}
	}
}
