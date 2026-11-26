// Package store — B3: customers & debts (FEATURE_MAP §5 U-01..U-13).
//
// Rules enforced here (service layer, Arabic errors):
//   - Customer CRUD is open to all four roles (matrix row 26: Cashier,
//     Manager, Admin, Supervisor may manage customers; Warehouse may not).
//   - CustomerName is required; a negative CreditLimit is refused.
//   - The default cash customer (CustomerID 1) can never be deleted (U-02).
//   - A customer with invoices can never be deleted (U-03).
//   - The displayed balance is ALWAYS the live sum (LiveCustomerBalance:
//     SUM of RemainingAmount over non-canceled invoices); the stored
//     Customers.Balance column is re-synced after every write (U-09,
//     GAP-REPORT §3.10).
//   - Debt payment: amount > 0 and never above the remaining/balance —
//     overpayment is refused explicitly (task decision; FEATURE_MAP U-09
//     left this open, .NET refuses amount > remaining, D1-#25).
//   - Cash payment requires an open shift for the same user today and
//     writes a CashBox 'in' row; non-cash never touches the drawer.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/money"
	"cashierpos-go/internal/rbac"
)

// Customer is one row of Customers. Balance is the LIVE balance
// (sum of open invoice remainings); StoredBalance is the synced column.
type Customer struct {
	ID            int64
	Name          string
	Phone         string
	Address       string
	Balance       money.Cents // live balance
	StoredBalance money.Cents // Customers.Balance column (kept in sync)
	CreditLimit   money.Cents
	IsActive      bool
	CreatedDate   string
}

// CustomerInput carries create/update fields.
type CustomerInput struct {
	Name        string
	Phone       string
	Address     string
	CreditLimit money.Cents
	IsActive    bool
}

// NormalizePaymentMethod is defined by B2 in domain_shifts.go (shared
// helper for the whole store package): Cash / Visa / Vodafone Cash.
// This file uses it plus requireKnownPaymentMethod from the same file.

// CreateCustomer inserts a customer. Name is required; a negative credit
// limit is refused. Perm: manage_customers.
func (s *Store) CreateCustomer(ctx context.Context, user *auth.User, in CustomerInput) (int64, error) {
	if err := rbac.Require(user.Role, rbac.PermManageCustomers); err != nil {
		return 0, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return 0, errors.New("اسم العميل مطلوب")
	}
	if in.CreditLimit < 0 {
		return 0, errors.New("حد الائتمان لا يمكن أن يكون سالباً — السالب ممنوع")
	}
	var id int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO Customers(CustomerName,Phone,Address,Balance,CreditLimit,IsActive,CreatedDate)
			VALUES(?,?,?,0,?,1,?)`, name, strings.TrimSpace(in.Phone), strings.TrimSpace(in.Address),
			int64(in.CreditLimit), Today())
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "إضافة عميل", "customers", nil, 0, id, name,
			fmt.Sprintf("إضافة عميل جديد — حد الائتمان %s", money.FormatEGP(in.CreditLimit)))
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// UpdateCustomer edits a customer. Name is required; a negative credit
// limit is refused. Perm: manage_customers.
func (s *Store) UpdateCustomer(ctx context.Context, user *auth.User, id int64, in CustomerInput) error {
	if err := rbac.Require(user.Role, rbac.PermManageCustomers); err != nil {
		return err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return errors.New("اسم العميل مطلوب")
	}
	if in.CreditLimit < 0 {
		return errors.New("حد الائتمان لا يمكن أن يكون سالباً — السالب ممنوع")
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM Customers WHERE CustomerID=?`, id).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return errors.New("العميل غير موجود")
		}
		active := 1
		if !in.IsActive {
			active = 0
		}
		if _, err := tx.Exec(`UPDATE Customers SET CustomerName=?,Phone=?,Address=?,CreditLimit=?,IsActive=?
			WHERE CustomerID=?`, name, strings.TrimSpace(in.Phone), strings.TrimSpace(in.Address),
			int64(in.CreditLimit), active, id); err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "تعديل عميل", "customers", nil, 0, id, name, "تعديل بيانات العميل")
	})
}

// DeleteCustomer removes a customer. Refused for the default cash customer
// (ID 1, U-02) and for any customer with invoices (U-03).
// Perm: manage_customers.
func (s *Store) DeleteCustomer(ctx context.Context, user *auth.User, id int64) error {
	if err := rbac.Require(user.Role, rbac.PermManageCustomers); err != nil {
		return err
	}
	if id == 1 {
		return errors.New("لا يمكن حذف العميل النقدي الافتراضي")
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		var name string
		if err := tx.QueryRow(`SELECT CustomerName FROM Customers WHERE CustomerID=?`, id).Scan(&name); err == sql.ErrNoRows {
			return errors.New("العميل غير موجود")
		} else if err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM Invoices WHERE CustomerID=?`, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("لا يمكن حذف عميل لديه فواتير مسجلة (%d فاتورة)", n)
		}
		if _, err := tx.Exec(`DELETE FROM Customers WHERE CustomerID=?`, id); err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "حذف عميل", "customers", nil, 0, id, name, "حذف عميل")
	})
}

// GetCustomer returns one customer with its LIVE balance.
// FIX-1 M7: service permission check (manage_customers).
func (s *Store) GetCustomer(ctx context.Context, user *auth.User, id int64) (*Customer, error) {
	if err := requireUser(user, rbac.PermManageCustomers); err != nil {
		return nil, err
	}
	var c Customer
	var stored, limit int64
	var active int
	err := s.DB.QueryRow(`SELECT CustomerID,CustomerName,Phone,Address,Balance,CreditLimit,IsActive,CreatedDate
		FROM Customers WHERE CustomerID=?`, id).
		Scan(&c.ID, &c.Name, &c.Phone, &c.Address, &stored, &limit, &active, &c.CreatedDate)
	if err == sql.ErrNoRows {
		return nil, errors.New("العميل غير موجود")
	}
	if err != nil {
		return nil, err
	}
	c.StoredBalance = money.Cents(stored)
	c.CreditLimit = money.Cents(limit)
	c.IsActive = active == 1
	live, err := s.LiveCustomerBalanceDB(id)
	if err != nil {
		return nil, err
	}
	c.Balance = live
	return &c, nil
}

// ListCustomers returns customers (live balances), optionally filtered by
// name/phone substring (U-04). Empty query returns all, cash customer first.
// FIX-1 M7: service permission check (manage_customers).
func (s *Store) ListCustomers(ctx context.Context, user *auth.User, query string) ([]*Customer, error) {
	if err := requireUser(user, rbac.PermManageCustomers); err != nil {
		return nil, err
	}
	q := strings.TrimSpace(query)
	var rows *sql.Rows
	var err error
	if q == "" {
		rows, err = s.DB.Query(`SELECT CustomerID,CustomerName,Phone,Address,Balance,CreditLimit,IsActive,CreatedDate
			FROM Customers ORDER BY CustomerID`)
	} else {
		like := "%" + q + "%"
		rows, err = s.DB.Query(`SELECT CustomerID,CustomerName,Phone,Address,Balance,CreditLimit,IsActive,CreatedDate
			FROM Customers WHERE CustomerName LIKE ? OR Phone LIKE ? ORDER BY CustomerID`, like, like)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Collect rows first, THEN compute live balances: MaxOpenConns(1)
	// means a query inside the rows loop would deadlock waiting for the
	// single connection held by the open rows.
	var out []*Customer
	for rows.Next() {
		var c Customer
		var stored, limit int64
		var active int
		if err := rows.Scan(&c.ID, &c.Name, &c.Phone, &c.Address, &stored, &limit, &active, &c.CreatedDate); err != nil {
			return nil, err
		}
		c.StoredBalance = money.Cents(stored)
		c.CreditLimit = money.Cents(limit)
		c.IsActive = active == 1
		out = append(out, &c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for _, c := range out {
		live, err := s.LiveCustomerBalanceDB(c.ID)
		if err != nil {
			return nil, err
		}
		c.Balance = live
	}
	return out, nil
}

// DebtorRow is one row of the debtors list (U-10).
type DebtorRow struct {
	Customer     *Customer
	OpenInvoices int
	LastPurchase string // YYYY-MM-DD or ""
	LastPayment  string // YYYY-MM-DD or ""
}

// ListDebtors returns customers with a live balance > 0: name, live balance,
// open-invoice count, last purchase date, last payment date (U-10).
func (s *Store) ListDebtors(ctx context.Context, user *auth.User) ([]*DebtorRow, error) {
	if err := requireUser(user, rbac.PermManageCustomers); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(`SELECT c.CustomerID,c.CustomerName,c.Phone,c.Address,c.Balance,c.CreditLimit,c.IsActive,c.CreatedDate,
		(SELECT COUNT(*) FROM Invoices i WHERE i.CustomerID=c.CustomerID AND i.IsCanceled=0 AND i.RemainingAmount>0),
		(SELECT MAX(i.InvoiceDate) FROM Invoices i WHERE i.CustomerID=c.CustomerID),
		(SELECT MAX(cp.PaymentDate) FROM CustomerPayments cp WHERE cp.CustomerID=c.CustomerID)
		FROM Customers c ORDER BY c.CustomerID`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Collect first (see ListCustomers: no queries inside a rows loop
	// with MaxOpenConns(1)).
	type rawDebtor struct {
		c            Customer
		openCount    int
		lastPurchase string
		lastPayment  string
	}
	var raw []rawDebtor
	for rows.Next() {
		var c Customer
		var stored, limit int64
		var active, openCount int
		var lastPurchase, lastPayment sql.NullString
		if err := rows.Scan(&c.ID, &c.Name, &c.Phone, &c.Address, &stored, &limit, &active, &c.CreatedDate,
			&openCount, &lastPurchase, &lastPayment); err != nil {
			return nil, err
		}
		c.StoredBalance = money.Cents(stored)
		c.CreditLimit = money.Cents(limit)
		c.IsActive = active == 1
		raw = append(raw, rawDebtor{c: c, openCount: openCount, lastPurchase: lastPurchase.String, lastPayment: lastPayment.String})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	var out []*DebtorRow
	for _, r := range raw {
		live, err := s.LiveCustomerBalanceDB(r.c.ID)
		if err != nil {
			return nil, err
		}
		r.c.Balance = live
		if live <= 0 {
			continue
		}
		c := r.c
		out = append(out, &DebtorRow{Customer: &c, OpenInvoices: r.openCount,
			LastPurchase: r.lastPurchase, LastPayment: r.lastPayment})
	}
	return out, nil
}

// StatementInvoice is one invoice line of a customer statement (U-05).
type StatementInvoice struct {
	InvoiceID  int64
	InvoiceNo  int64
	Date       string
	NetAmount  money.Cents
	PaidAmount money.Cents
	Remaining  money.Cents
	IsCanceled bool
}

// StatementPayment is one payment line of a customer statement (U-05).
type StatementPayment struct {
	Date      string
	Amount    money.Cents
	Method    string
	InvoiceNo sql.NullInt64
	Notes     string
}

// CustomerStatement is a printable statement: invoices + payments + totals.
type CustomerStatement struct {
	Customer       *Customer
	Invoices       []*StatementInvoice
	Payments       []*StatementPayment
	TotalPurchases money.Cents
	TotalPaid      money.Cents
	TotalRemaining money.Cents // live balance
}

// CustomerStatement builds the statement for a customer (U-05).
func (s *Store) CustomerStatement(ctx context.Context, user *auth.User, id int64) (*CustomerStatement, error) {
	c, err := s.GetCustomer(ctx, user, id)
	if err != nil {
		return nil, err
	}
	st := &CustomerStatement{Customer: c}
	rows, err := s.DB.Query(`SELECT InvoiceID,InvoiceNumber,InvoiceDate,NetAmount,PaidAmount,RemainingAmount,IsCanceled
		FROM Invoices WHERE CustomerID=? ORDER BY InvoiceDate, InvoiceNumber`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var inv StatementInvoice
		var net, paid, rem int64
		var canceled int
		if err := rows.Scan(&inv.InvoiceID, &inv.InvoiceNo, &inv.Date, &net, &paid, &rem, &canceled); err != nil {
			rows.Close()
			return nil, err
		}
		inv.NetAmount = money.Cents(net)
		inv.PaidAmount = money.Cents(paid)
		inv.Remaining = money.Cents(rem)
		inv.IsCanceled = canceled == 1
		st.Invoices = append(st.Invoices, &inv)
		if !inv.IsCanceled {
			st.TotalPurchases += inv.NetAmount
			st.TotalRemaining += inv.Remaining
		}
	}
	rows.Close()
	// The method lives on CustomerPayments itself (FIX-2 M-2: the old
	// amount-matching subquery against Payments was fragile — two
	// payments of the same amount could return the wrong method — and
	// its comment claiming the column did not exist was wrong).
	prows, err := s.DB.Query(`SELECT cp.PaymentDate,cp.Amount,
		COALESCE(cp.PaymentMethod,''),
		i.InvoiceNumber,cp.Notes
		FROM CustomerPayments cp LEFT JOIN Invoices i ON i.InvoiceID=cp.InvoiceID
		WHERE cp.CustomerID=? ORDER BY cp.PaymentDate, cp.CPaymentID`, id)
	if err != nil {
		return nil, err
	}
	defer prows.Close()
	for prows.Next() {
		var p StatementPayment
		var amt int64
		if err := prows.Scan(&p.Date, &amt, &p.Method, &p.InvoiceNo, &p.Notes); err != nil {
			return nil, err
		}
		p.Amount = money.Cents(amt)
		st.Payments = append(st.Payments, &p)
		st.TotalPaid += p.Amount
	}
	return st, prows.Err()
}

// applyDebtPaymentTx allocates amount across open invoices oldest-first
// inside tx and posts the payment rows. Caller has validated amount and
// holds the shift (for cash). Returns the new live balance.
func applyDebtPaymentTx(tx *sql.Tx, user *auth.User, customerID int64, amount money.Cents, method, notes string, shiftID int64, invoiceIDFilter int64) (money.Cents, error) {
	var name string
	if err := tx.QueryRow(`SELECT CustomerName FROM Customers WHERE CustomerID=?`, customerID).Scan(&name); err == sql.ErrNoRows {
		return 0, errors.New("العميل غير موجود")
	} else if err != nil {
		return 0, err
	}
	remaining := amount
	now := Now()
	// Oldest open invoices first (InvoiceDate, then InvoiceNumber).
	var rows *sql.Rows
	var err error
	if invoiceIDFilter != 0 {
		rows, err = tx.Query(`SELECT InvoiceID,InvoiceNumber,RemainingAmount FROM Invoices
			WHERE InvoiceID=? AND CustomerID=? AND IsCanceled=0 AND RemainingAmount>0`, invoiceIDFilter, customerID)
	} else {
		rows, err = tx.Query(`SELECT InvoiceID,InvoiceNumber,RemainingAmount FROM Invoices
			WHERE CustomerID=? AND IsCanceled=0 AND RemainingAmount>0
			ORDER BY InvoiceDate, InvoiceNumber`, customerID)
	}
	if err != nil {
		return 0, err
	}
	type alloc struct {
		invoiceID int64
		invoiceNo int64
		amt       money.Cents
	}
	var allocs []alloc
	for rows.Next() {
		var invID, invNo, rem int64
		if err := rows.Scan(&invID, &invNo, &rem); err != nil {
			rows.Close()
			return 0, err
		}
		if remaining <= 0 {
			break
		}
		take := remaining
		if take > money.Cents(rem) {
			take = money.Cents(rem)
		}
		allocs = append(allocs, alloc{invID, invNo, take})
		remaining -= take
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(allocs) == 0 {
		return 0, errors.New("لا توجد فواتير مفتوحة عليها متبقٍّ لهذا العميل")
	}
	if remaining > 0 {
		// Caller guarantees amount <= live balance, so this is unreachable
		// unless invoices changed mid-transaction; refuse rather than overpay.
		return 0, errors.New("مبلغ السداد يتجاوز المتبقي المستحق — الدفع الزائد مرفوض")
	}
	for _, a := range allocs {
		if _, err := tx.Exec(`UPDATE Invoices SET RemainingAmount=RemainingAmount-?, PaidAmount=PaidAmount+?
			WHERE InvoiceID=?`, int64(a.amt), int64(a.amt), a.invoiceID); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(`INSERT INTO CustomerPayments(CustomerID,InvoiceID,Amount,PaymentDate,PaymentMethod,ReceivedBy,UserID,Notes,ShiftID)
			VALUES(?,?,?,?,?,?,?,?,?)`, customerID, a.invoiceID, int64(a.amt), now, method, user.UserName, user.ID, notes, nullableShift(shiftID)); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(`INSERT INTO Payments(InvoiceID,CustomerID,Amount,PaymentDate,PaymentMethod,Notes,UserID)
			VALUES(?,?,?,?,?,?,?)`, a.invoiceID, customerID, int64(a.amt), now, method, notes, user.ID); err != nil {
			return 0, err
		}
	}
	if method == "Cash" {
		if _, err := tx.Exec(`INSERT INTO CashBox(UserID,UserName,ShiftID,MovementType,Amount,Reason,MovementDate,AutoKind)
			VALUES(?,?,?,'in',?, ?,?,'customer_collection')`, user.ID, user.UserName, shiftID, int64(amount),
			fmt.Sprintf("سداد دين عميل — %s", name), now); err != nil {
			return 0, err
		}
	}
	if err := SyncCustomerBalance(tx, customerID); err != nil {
		return 0, err
	}
	live, err := LiveCustomerBalance(tx, customerID)
	if err != nil {
		return 0, err
	}
	return live, AuditTx(tx, user.ID, user.UserName, "سداد دين عميل", "customers", nil, 0, customerID, name,
		fmt.Sprintf("سداد %s (%s) — الرصيد المتبقي %s", money.FormatEGP(amount), method, money.FormatEGP(live)))
}

func nullableShift(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// PayCustomerDebtGeneral pays a general debt amount, allocated to the
// oldest open invoices first (U-06/U-08). Amount must be > 0 and must not
// exceed the live balance — overpayment is refused. Cash requires an open
// shift. Perm: collect_payment.
func (s *Store) PayCustomerDebtGeneral(ctx context.Context, user *auth.User, customerID int64, amount money.Cents, method, notes string) (money.Cents, error) {
	if err := rbac.Require(user.Role, rbac.PermCollectPayment); err != nil {
		return 0, err
	}
	if amount <= 0 {
		return 0, errors.New("مبلغ السداد يجب أن يكون أكبر من صفر")
	}
	method, err := requireKnownPaymentMethod(method)
	if err != nil {
		return 0, err
	}
	var newBalance money.Cents
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		live, err := LiveCustomerBalance(tx, customerID)
		if err != nil {
			return err
		}
		if live <= 0 {
			return errors.New("هذا العميل ليس لديه مديونية")
		}
		if amount > live {
			return fmt.Errorf("مبلغ السداد (%s) يتجاوز مديونية العميل (%s) — الدفع الزائد مرفوض",
				money.FormatEGP(amount), money.FormatEGP(live))
		}
		var shiftID int64
		if method == "Cash" {
			shiftID, err = OpenShiftID(tx, user.ID)
			if err != nil {
				return err
			}
		} else if sid, err := OpenShiftID(tx, user.ID); err == nil {
			// FIX-1 M2: a non-cash collection is stamped with the
			// caller's open shift too (when one exists) so the shift
			// summary can split collections by method; no shift is
			// fine for non-cash (ShiftID stays NULL).
			shiftID = sid
		}
		newBalance, err = applyDebtPaymentTx(tx, user, customerID, amount, method, notes, shiftID, 0)
		return err
	})
	if err != nil {
		return 0, err
	}
	return newBalance, nil
}

// PayCustomerInvoice pays a specific invoice (U-07). Amount must be > 0 and
// must not exceed that invoice's remaining — overpayment is refused.
// Cash requires an open shift. Perm: collect_payment.
func (s *Store) PayCustomerInvoice(ctx context.Context, user *auth.User, invoiceID int64, amount money.Cents, method, notes string) (money.Cents, error) {
	if err := rbac.Require(user.Role, rbac.PermCollectPayment); err != nil {
		return 0, err
	}
	if amount <= 0 {
		return 0, errors.New("مبلغ السداد يجب أن يكون أكبر من صفر")
	}
	method, err := requireKnownPaymentMethod(method)
	if err != nil {
		return 0, err
	}
	var newRemaining money.Cents
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		var customerID int64
		var rem int64
		var canceled int
		if err := tx.QueryRow(`SELECT CustomerID,RemainingAmount,IsCanceled FROM Invoices WHERE InvoiceID=?`,
			invoiceID).Scan(&customerID, &rem, &canceled); err == sql.ErrNoRows {
			return errors.New("الفاتورة غير موجودة")
		} else if err != nil {
			return err
		}
		if canceled == 1 {
			return errors.New("هذه الفاتورة ملغاة")
		}
		if rem <= 0 {
			return errors.New("هذه الفاتورة لا يوجد عليها متبقي للسداد")
		}
		if amount > money.Cents(rem) {
			return fmt.Errorf("مبلغ السداد (%s) يتجاوز المتبقي على الفاتورة (%s) — الدفع الزائد مرفوض",
				money.FormatEGP(amount), money.FormatEGP(money.Cents(rem)))
		}
		var shiftID int64
		if method == "Cash" {
			var err error
			shiftID, err = OpenShiftID(tx, user.ID)
			if err != nil {
				return err
			}
		} else if sid, err := OpenShiftID(tx, user.ID); err == nil {
			// FIX-1 M2: stamp the open shift on non-cash collections
			// too (see PayCustomerDebtGeneral).
			shiftID = sid
		}
		_, err := applyDebtPaymentTx(tx, user, customerID, amount, method, notes, shiftID, invoiceID)
		if err != nil {
			return err
		}
		newRemaining = money.Cents(rem) - amount
		return nil
	})
	if err != nil {
		return 0, err
	}
	return newRemaining, nil
}

// CheckCreditLimit reports whether adding newRemaining (the remaining of a
// sale about to be posted) to the customer's LIVE balance would exceed the
// credit limit. Limit 0 means no limit. It is a helper for the sales
// builder (B1): call it inside the sale transaction before posting, and
// require ApproveCreditOverride when it returns overLimit=true.
//
// Signature: (overLimit bool, liveBalance money.Cents, limit money.Cents, err error)
func (s *Store) CheckCreditLimit(tx *sql.Tx, customerID int64, newRemaining money.Cents) (bool, money.Cents, money.Cents, error) {
	var limit int64
	if err := tx.QueryRow(`SELECT CreditLimit FROM Customers WHERE CustomerID=?`, customerID).Scan(&limit); err == sql.ErrNoRows {
		return false, 0, 0, errors.New("العميل غير موجود")
	} else if err != nil {
		return false, 0, 0, err
	}
	if newRemaining < 0 {
		return false, 0, money.Cents(limit), errors.New("المتبقي الجديد لا يمكن أن يكون سالباً — السالب ممنوع")
	}
	live, err := LiveCustomerBalance(tx, customerID)
	if err != nil {
		return false, 0, money.Cents(limit), err
	}
	if limit <= 0 {
		return false, live, money.Cents(limit), nil
	}
	return live+newRemaining > money.Cents(limit), live, money.Cents(limit), nil
}

// ApproveCreditOverride records a manager-approved credit-limit override.
// Only Manager/Admin hold approve_credit_override (matrix row 76).
// It writes the AuditLog row "تجاوز حد الائتمان بموافقة المدير" with the
// limit, live balance and new remaining. When there is no actual over-limit
// it is a no-op (no audit row). B1 calls this inside the sale transaction
// after the approver confirms, before posting the invoice.
func (s *Store) ApproveCreditOverride(ctx context.Context, user *auth.User, customerID int64, newRemaining money.Cents) error {
	if err := rbac.Require(user.Role, rbac.PermApproveCreditOverride); err != nil {
		return err
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		over, live, limit, err := s.CheckCreditLimit(tx, customerID, newRemaining)
		if err != nil {
			return err
		}
		if !over {
			return nil
		}
		var name string
		_ = tx.QueryRow(`SELECT CustomerName FROM Customers WHERE CustomerID=?`, customerID).Scan(&name)
		return AuditTx(tx, user.ID, user.UserName, "تجاوز حد الائتمان بموافقة المدير", "customers", nil, 0, customerID, name,
			fmt.Sprintf("تجاوز حد الائتمان بموافقة المدير — حد %s، الرصيد الحي %s، المتبقي الجديد %s، الإجمالي %s",
				money.FormatEGP(limit), money.FormatEGP(live), money.FormatEGP(newRemaining), money.FormatEGP(live+newRemaining)))
	})
}
