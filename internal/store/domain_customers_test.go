package store

import (
	"context"
	"database/sql"
	"testing"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/money"
)

// Shared B3 test helpers (used by all domain_*_test.go files in this package).

func testOpenShift(t *testing.T, s *Store, user *auth.User, opening money.Cents) int64 {
	t.Helper()
	res, err := s.DB.Exec(`INSERT INTO Shifts(UserID,UserName,ShiftDate,OpenTime,Status,OpeningBalance,IsOpen)
		VALUES(?,?,?,?,'Open',?,1)`, user.ID, user.UserName, Today(), Now(), int64(opening))
	if err != nil {
		t.Fatalf("insert test shift: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// testInvoice inserts a sale invoice directly (B1 owns real sales posting;
// tests here need open invoices to pay against).
func b3Invoice(t *testing.T, s *Store, customerID int64, number int64, net, paid, remaining money.Cents, date string) int64 {
	t.Helper()
	res, err := s.DB.Exec(`INSERT INTO Invoices(InvoiceNumber,CustomerID,TotalAmount,NetAmount,PaidAmount,RemainingAmount,PaymentMethod,InvoiceDate,IsCanceled)
		VALUES(?,?,?,?,?,?,'Cash',?,0)`, number, customerID, int64(net), int64(net), int64(paid), int64(remaining), date)
	if err != nil {
		t.Fatalf("insert test invoice: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func mustErr(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected error, got nil", what)
	}
}

func auditCount(t *testing.T, s *Store, action string) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM AuditLog WHERE ActionType=?`, action).Scan(&n); err != nil {
		t.Fatalf("audit count: %v", err)
	}
	return n
}

func TestCustomerCRUD(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")

	id, err := s.CreateCustomer(ctx, admin, CustomerInput{Name: "عميل اختبار", Phone: "01000000001", CreditLimit: money.Cents(50000)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	c, err := s.GetCustomer(ctx, admin, id)
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "عميل اختبار" || c.CreditLimit != money.Cents(50000) || c.Balance != 0 {
		t.Fatalf("unexpected customer: %+v", c)
	}
	// Empty name refused.
	if _, err := s.CreateCustomer(ctx, admin, CustomerInput{Name: "  "}); err == nil {
		t.Fatal("empty name should be refused")
	}
	// Negative credit limit refused.
	if _, err := s.CreateCustomer(ctx, admin, CustomerInput{Name: "سالب", CreditLimit: -1}); err == nil {
		t.Fatal("negative credit limit should be refused")
	}
	// Update.
	if err := s.UpdateCustomer(ctx, admin, id, CustomerInput{Name: "عميل معدل", Phone: "01000000002", CreditLimit: money.Cents(10000), IsActive: true}); err != nil {
		t.Fatalf("update: %v", err)
	}
	c, _ = s.GetCustomer(ctx, admin, id)
	if c.Name != "عميل معدل" || c.Phone != "01000000002" {
		t.Fatalf("update not applied: %+v", c)
	}
	// Negative limit on update refused.
	if err := s.UpdateCustomer(ctx, admin, id, CustomerInput{Name: "عميل معدل", CreditLimit: -5, IsActive: true}); err == nil {
		t.Fatal("negative credit limit on update should be refused")
	}
	// Warehouse may not manage customers.
	wh := testUser(t, s, "wh1", "warehouse")
	if _, err := s.CreateCustomer(ctx, wh, CustomerInput{Name: "مخزن"}); err == nil {
		t.Fatal("warehouse must not create customers")
	}
	// Cashier MAY manage customers (matrix row 26).
	cashier := testUser(t, s, "cash1", "cashier")
	if _, err := s.CreateCustomer(ctx, cashier, CustomerInput{Name: "عميل الكاشير"}); err != nil {
		t.Fatalf("cashier should create customers: %v", err)
	}
	// Delete a clean customer works.
	if err := s.DeleteCustomer(ctx, admin, id); err != nil {
		t.Fatalf("delete clean customer: %v", err)
	}
	if _, err := s.GetCustomer(ctx, admin, id); err == nil {
		t.Fatal("deleted customer should be gone")
	}
}

func TestCustomerDeleteGuards(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")

	// Default cash customer (ID 1) can never be deleted.
	if err := s.DeleteCustomer(ctx, admin, 1); err == nil {
		t.Fatal("deleting cash customer must be refused")
	}
	// Customer with invoices cannot be deleted.
	id, _ := s.CreateCustomer(ctx, admin, CustomerInput{Name: "له فواتير"})
	b3Invoice(t, s, id, 1, 10000, 0, 10000, Today())
	if err := s.DeleteCustomer(ctx, admin, id); err == nil {
		t.Fatal("deleting customer with invoices must be refused")
	}
}

func TestCustomerSearchAndLiveBalance(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	id, _ := s.CreateCustomer(ctx, admin, CustomerInput{Name: "أحمد محمد", Phone: "01111111111"})
	_, _ = s.CreateCustomer(ctx, admin, CustomerInput{Name: "سارة علي", Phone: "01222222222"})
	b3Invoice(t, s, id, 1, 20000, 5000, 15000, Today())
	b3Invoice(t, s, id, 2, 10000, 10000, 0, Today()) // fully paid: not counted
	// Corrupt the stored column: live balance must still be right.
	if _, err := s.DB.Exec(`UPDATE Customers SET Balance=999999 WHERE CustomerID=?`, id); err != nil {
		t.Fatal(err)
	}
	c, err := s.GetCustomer(ctx, admin, id)
	if err != nil {
		t.Fatal(err)
	}
	if c.Balance != money.Cents(15000) {
		t.Fatalf("live balance = %v, want 150.00 EGP", c.Balance)
	}
	if c.StoredBalance != money.Cents(999999) {
		t.Fatalf("stored column should still be stale here, got %v", c.StoredBalance)
	}
	found, err := s.ListCustomers(ctx, admin, "أحمد")
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].ID != id {
		t.Fatalf("search by name: got %d rows", len(found))
	}
	found, err = s.ListCustomers(ctx, admin, "0122222")
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Name != "سارة علي" {
		t.Fatalf("search by phone: got %d rows", len(found))
	}
}

func TestPayCustomerDebtGeneralOldestFirst(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	id, _ := s.CreateCustomer(ctx, admin, CustomerInput{Name: "مدين"})
	inv1 := b3Invoice(t, s, id, 1, 10000, 0, 10000, "2026-11-20")
	inv2 := b3Invoice(t, s, id, 2, 20000, 0, 20000, "2026-11-21")
	inv3 := b3Invoice(t, s, id, 3, 5000, 0, 5000, "2026-11-22")

	// Non-cash payment needs no shift.
	newBal, err := s.PayCustomerDebtGeneral(ctx, admin, id, money.Cents(12000), "Visa", "سداد جزئي")
	if err != nil {
		t.Fatalf("general pay: %v", err)
	}
	if newBal != money.Cents(23000) {
		t.Fatalf("new balance = %v, want 230.00 EGP", newBal)
	}
	// Oldest invoice fully settled, second partially.
	var r1, r2, r3 int64
	_ = s.DB.QueryRow(`SELECT RemainingAmount FROM Invoices WHERE InvoiceID=?`, inv1).Scan(&r1)
	_ = s.DB.QueryRow(`SELECT RemainingAmount FROM Invoices WHERE InvoiceID=?`, inv2).Scan(&r2)
	_ = s.DB.QueryRow(`SELECT RemainingAmount FROM Invoices WHERE InvoiceID=?`, inv3).Scan(&r3)
	if r1 != 0 || r2 != 18000 || r3 != 5000 {
		t.Fatalf("oldest-first allocation wrong: %d %d %d", r1, r2, r3)
	}
	// Stored column re-synced.
	var stored int64
	_ = s.DB.QueryRow(`SELECT Balance FROM Customers WHERE CustomerID=?`, id).Scan(&stored)
	if stored != 23000 {
		t.Fatalf("stored balance not synced: %d", stored)
	}
	// CustomerPayments + Payments rows written.
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM CustomerPayments WHERE CustomerID=?`, id).Scan(&n)
	if n != 2 {
		t.Fatalf("CustomerPayments rows = %d, want 2", n)
	}
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM Payments WHERE CustomerID=?`, id).Scan(&n)
	if n != 2 {
		t.Fatalf("Payments rows = %d, want 2", n)
	}
}

func TestPayCustomerDebtOverpayRefused(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	id, _ := s.CreateCustomer(ctx, admin, CustomerInput{Name: "مدين"})
	inv := b3Invoice(t, s, id, 1, 10000, 0, 10000, Today())
	// General overpay refused.
	mustErr(t, func() error {
		_, err := s.PayCustomerDebtGeneral(ctx, admin, id, money.Cents(10001), "Cash", "")
		return err
	}(), "general overpay")
	// Per-invoice overpay refused.
	mustErr(t, func() error {
		_, err := s.PayCustomerInvoice(ctx, admin, inv, money.Cents(10001), "Visa", "")
		return err
	}(), "invoice overpay")
	// Zero refused.
	mustErr(t, func() error {
		_, err := s.PayCustomerDebtGeneral(ctx, admin, id, 0, "Visa", "")
		return err
	}(), "zero pay")
	// Balance untouched after refused attempts.
	c, _ := s.GetCustomer(ctx, admin, id)
	if c.Balance != money.Cents(10000) {
		t.Fatalf("balance changed after refused pays: %v", c.Balance)
	}
	// Exact-amount per-invoice pay succeeds.
	rem, err := s.PayCustomerInvoice(ctx, admin, inv, money.Cents(10000), "Visa", "")
	if err != nil {
		t.Fatalf("exact pay: %v", err)
	}
	if rem != 0 {
		t.Fatalf("remaining after exact pay = %v", rem)
	}
	// Paying a settled invoice is refused.
	mustErr(t, func() error {
		_, err := s.PayCustomerInvoice(ctx, admin, inv, money.Cents(100), "Visa", "")
		return err
	}(), "pay settled invoice")
}

func TestPayCustomerDebtCashRequiresShift(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	id, _ := s.CreateCustomer(ctx, admin, CustomerInput{Name: "مدين نقدي"})
	b3Invoice(t, s, id, 1, 10000, 0, 10000, Today())
	// No shift: cash refused.
	mustErr(t, func() error {
		_, err := s.PayCustomerDebtGeneral(ctx, admin, id, money.Cents(5000), "Cash", "")
		return err
	}(), "cash without shift")
	// With a shift: cash works and writes CashBox 'in'.
	testOpenShift(t, s, admin, money.Cents(0))
	newBal, err := s.PayCustomerDebtGeneral(ctx, admin, id, money.Cents(5000), "Cash", "")
	if err != nil {
		t.Fatalf("cash pay with shift: %v", err)
	}
	if newBal != money.Cents(5000) {
		t.Fatalf("new balance = %v", newBal)
	}
	var cb int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM CashBox WHERE MovementType='in' AND Amount=5000`).Scan(&cb)
	if cb != 1 {
		t.Fatalf("CashBox 'in' rows = %d, want 1", cb)
	}
}

func TestCustomerStatement(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	id, _ := s.CreateCustomer(ctx, admin, CustomerInput{Name: "كشف"})
	b3Invoice(t, s, id, 1, 10000, 2000, 8000, Today())
	b3Invoice(t, s, id, 2, 5000, 5000, 0, Today())
	st, err := s.CustomerStatement(ctx, admin, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Invoices) != 2 || len(st.Payments) != 0 {
		t.Fatalf("statement rows: %d invoices %d payments", len(st.Invoices), len(st.Payments))
	}
	if st.TotalPurchases != money.Cents(15000) || st.TotalRemaining != money.Cents(8000) {
		t.Fatalf("statement totals: purchases %v remaining %v", st.TotalPurchases, st.TotalRemaining)
	}
	// After a payment the statement shows it.
	if _, err := s.PayCustomerDebtGeneral(ctx, admin, id, money.Cents(3000), "Visa", ""); err != nil {
		t.Fatal(err)
	}
	st, _ = s.CustomerStatement(ctx, admin, id)
	if len(st.Payments) != 1 || st.TotalPaid != money.Cents(3000) || st.TotalRemaining != money.Cents(5000) {
		t.Fatalf("statement after pay: %+v", st)
	}
}

func TestDebtorsList(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	id, _ := s.CreateCustomer(ctx, admin, CustomerInput{Name: "مدين"})
	_, _ = s.CreateCustomer(ctx, admin, CustomerInput{Name: "غير مدين"})
	b3Invoice(t, s, id, 1, 10000, 0, 10000, "2026-11-20")
	b3Invoice(t, s, id, 2, 5000, 0, 5000, "2026-11-22")
	debtors, err := s.ListDebtors(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(debtors) != 1 {
		t.Fatalf("debtors = %d, want 1", len(debtors))
	}
	d := debtors[0]
	if d.OpenInvoices != 2 || d.Customer.Balance != money.Cents(15000) || d.LastPurchase != "2026-11-22" {
		t.Fatalf("debtor row wrong: %+v", d)
	}
}

func TestCheckCreditLimitAndOverride(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	cashier := testUser(t, s, "cash1", "cashier")
	id, _ := s.CreateCustomer(ctx, admin, CustomerInput{Name: "ائتمان", CreditLimit: money.Cents(10000)})
	b3Invoice(t, s, id, 1, 8000, 0, 8000, Today())
	// 8000 live + 3000 new = 11000 > 10000 limit -> over.
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		over, live, limit, err := s.CheckCreditLimit(tx, id, money.Cents(3000))
		if err != nil {
			return err
		}
		if !over || live != money.Cents(8000) || limit != money.Cents(10000) {
			t.Errorf("over=%v live=%v limit=%v", over, live, limit)
		}
		over, _, _, err = s.CheckCreditLimit(tx, id, money.Cents(2000))
		if err != nil {
			return err
		}
		if over {
			t.Errorf("8000+2000 must not exceed 10000 limit")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Cashier cannot approve an override.
	mustErr(t, s.ApproveCreditOverride(ctx, cashier, id, money.Cents(3000)), "cashier override")
	// Admin approves: audit row written.
	before := auditCount(t, s, "تجاوز حد الائتمان بموافقة المدير")
	if err := s.ApproveCreditOverride(ctx, admin, id, money.Cents(3000)); err != nil {
		t.Fatalf("admin override: %v", err)
	}
	if auditCount(t, s, "تجاوز حد الائتمان بموافقة المدير") != before+1 {
		t.Fatal("override audit row missing")
	}
	// No over-limit -> no-op, no audit row.
	before = auditCount(t, s, "تجاوز حد الائتمان بموافقة المدير")
	if err := s.ApproveCreditOverride(ctx, admin, id, money.Cents(1000)); err != nil {
		t.Fatalf("no-over override should be no-op: %v", err)
	}
	if auditCount(t, s, "تجاوز حد الائتمان بموافقة المدير") != before {
		t.Fatal("no-over override must not write an audit row")
	}
}

// --- FIX-1 regression tests ---

func TestFix1_DebtPaymentStoresMethod(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	id, _ := s.CreateCustomer(ctx, admin, CustomerInput{Name: "عميل"})
	b3Invoice(t, s, id, 1, 30000, 0, 30000, Today())
	if _, err := s.PayCustomerDebtGeneral(ctx, admin, id, money.Cents(1000), "Visa", ""); err != nil {
		t.Fatalf("visa pay: %v", err)
	}
	var method string
	if err := s.DB.QueryRow(`SELECT PaymentMethod FROM CustomerPayments WHERE CustomerID=?`, id).Scan(&method); err != nil {
		t.Fatal(err)
	}
	if method != "Visa" {
		t.Fatalf("stored method = %q, want Visa (M2)", method)
	}
}

func TestFix1_CustomerReadsRequirePerm(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	wh := testUser(t, s, "wh1", "warehouse")
	admin := testUser(t, s, "admin1", "admin")
	id, _ := s.CreateCustomer(ctx, admin, CustomerInput{Name: "عميل"})
	if _, err := s.ListCustomers(ctx, wh, ""); err == nil {
		t.Fatal("warehouse must not list customers (M7c)")
	}
	if _, err := s.GetCustomer(ctx, wh, id); err == nil {
		t.Fatal("warehouse must not read a customer (M7c)")
	}
	if _, err := s.ListDebtors(ctx, wh); err == nil {
		t.Fatal("warehouse must not list debtors (M7c)")
	}
	if _, err := s.CustomerStatement(ctx, wh, id); err == nil {
		t.Fatal("warehouse must not read a statement (M7c)")
	}
}

// --- FIX-2 regression tests ---

func TestFix2_CustomerStatementReadsOwnMethod(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	id, _ := s.CreateCustomer(ctx, admin, CustomerInput{Name: "كشف"})
	testOpenShift(t, s, admin, 10000)
	b3Invoice(t, s, id, 1, 10000, 2000, 8000, Today())
	// Two payments of the SAME amount, different methods: the old
	// amount-matching subquery returned the first method for both.
	if _, err := s.PayCustomerDebtGeneral(ctx, admin, id, money.Cents(3000), "Cash", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PayCustomerDebtGeneral(ctx, admin, id, money.Cents(3000), "Visa", ""); err != nil {
		t.Fatal(err)
	}
	st, err := s.CustomerStatement(ctx, admin, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Payments) != 2 {
		t.Fatalf("payments = %d, want 2", len(st.Payments))
	}
	if st.Payments[0].Method != "Cash" || st.Payments[1].Method != "Visa" {
		t.Fatalf("methods = %q/%q, want Cash/Visa (M-2: read the payment's own method)", st.Payments[0].Method, st.Payments[1].Method)
	}
}
