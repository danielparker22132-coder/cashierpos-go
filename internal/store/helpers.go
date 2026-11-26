package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/money"
	"cashierpos-go/internal/rbac"
)

// requireUser refuses a nil user and enforces one permission (FIX-1 M7).
// perm == "" means "any logged-in user" — used for reads the routes
// intentionally open to every role (vendors, payables, invoice list).
func requireUser(user *auth.User, perm string) error {
	if user == nil {
		return errors.New("يلزم تسجيل الدخول")
	}
	if perm == "" {
		return nil
	}
	return rbac.Require(user.Role, perm)
}

// Shared helpers used by several domain builders. All take a transaction so
// they run inside the caller's atomic write.

// OpenShiftID returns the open shift id for userID today, or an error
// (Arabic) when there is none. Sale/payment paths must call this.
func OpenShiftID(tx *sql.Tx, userID int64) (int64, error) {
	var id int64
	err := tx.QueryRow(`SELECT ShiftID FROM Shifts
		WHERE UserID=? AND ShiftDate=? AND IsOpen=1 ORDER BY ShiftID DESC LIMIT 1`,
		userID, Today()).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, errors.New("لا يمكن تنفيذ هذه العملية بدون شيفت مفتوح لنفس المستخدم في تاريخ اليوم")
	}
	if err != nil {
		return 0, err
	}
	return id, nil
}

// DrawerExpectedBalance computes the expected drawer balance of a shift:
// Opening + CashSales + RetainedCanceledCash + CashDebtCollections + CashIn - CashReturns - CashOut - Expenses.
// Cash sales/collections are the CashBox 'in' rows the sale/payment wrote
// (auto rows included once here); manual CashIn excludes nothing here
// because shiftSummaryTx counts the same cash via Payments/CustomerPayments
// and excludes the auto rows from its manual CashIn — the two definitions
// must agree (FIX-1 H1 regression test). Expenses never write CashBox rows,
// so they are subtracted from the Expenses table. 'close'/'resume' rows are
// snapshots, not cash movements, and are excluded from both sums (FIX-1 H4:
// a 'close' row left behind after a resume would otherwise poison the gate
// with a phantom outflow).
func DrawerExpectedBalance(tx *sql.Tx, shiftID int64) (money.Cents, error) {
	var opening int64
	err := tx.QueryRow(`SELECT OpeningBalance FROM Shifts WHERE ShiftID=?`, shiftID).Scan(&opening)
	if err != nil {
		return 0, err
	}
	var inSum, outSum sql.NullInt64
	err = tx.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN MovementType IN ('open','in') THEN Amount ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN MovementType='out' THEN Amount ELSE 0 END),0)
		FROM CashBox WHERE ShiftID=?`, shiftID).Scan(&inSum, &outSum)
	if err != nil {
		return 0, err
	}
	// 'open' movement equals the opening balance and is counted once via
	// OpeningBalance; exclude it from the in-sum to avoid double counting.
	var openAmt sql.NullInt64
	_ = tx.QueryRow(`SELECT COALESCE(SUM(Amount),0) FROM CashBox
		WHERE ShiftID=? AND MovementType='open'`, shiftID).Scan(&openAmt)
	var expenses sql.NullInt64
	_ = tx.QueryRow(`SELECT COALESCE(SUM(Amount),0) FROM Expenses WHERE ShiftID=?`, shiftID).Scan(&expenses)
	expected := money.Cents(opening) + money.Cents(inSum.Int64-openAmt.Int64) - money.Cents(outSum.Int64) - money.Cents(expenses.Int64)
	return expected, nil
}

// RequireDrawerBalance refuses a discretionary cash-out that would take the
// drawer below zero. Exact-balance withdrawal is allowed.
func RequireDrawerBalance(tx *sql.Tx, shiftID int64, amount money.Cents, action string) error {
	if amount < 0 {
		return fmt.Errorf("%s: المبلغ لا يمكن أن يكون سالباً", action)
	}
	if amount == 0 {
		return nil
	}
	expected, err := DrawerExpectedBalance(tx, shiftID)
	if err != nil {
		return err
	}
	if amount > expected {
		return fmt.Errorf("%s: المبلغ (%s) يتجاوز رصيد الدرج المتاح (%s) — السالب ممنوع",
			action, money.FormatEGP(amount), money.FormatEGP(expected))
	}
	return nil
}

// NextInvoiceNumber mints the next global running invoice number inside the
// posting transaction, bumping the persistent high-water mark (FIX-6).
func NextInvoiceNumber(tx *sql.Tx) (int64, error) {
	raw := GetSettingTx(tx, "invoice_number_high_water")
	var hw int64
	if _, err := fmt.Sscanf(raw, "%d", &hw); err != nil {
		hw = 0
	}
	var maxExisting int64
	_ = tx.QueryRow(`SELECT COALESCE(MAX(InvoiceNumber),0) FROM Invoices`).Scan(&maxExisting)
	next := hw + 1
	if maxExisting >= next {
		next = maxExisting + 1
	}
	if err := SetSettingTx(tx, "invoice_number_high_water", fmt.Sprintf("%d", next)); err != nil {
		return 0, err
	}
	return next, nil
}

// NextPurchaseNumber mints the next sequential purchase invoice number
// ("PI-%06d") inside the posting transaction, bumping the persistent
// high-water mark (FIX-3 L7 — timestamps could collide within a second).
// Legacy timestamp numbers ("PI-YYYYMMDDHHMMSS") are ignored: only
// six-digit sequence numbers count toward the max.
func NextPurchaseNumber(tx *sql.Tx) (string, error) {
	raw := GetSettingTx(tx, "purchase_number_high_water")
	var hw int64
	if _, err := fmt.Sscanf(raw, "%d", &hw); err != nil {
		hw = 0
	}
	rows, err := tx.Query(`SELECT InvoiceNumber FROM PurchaseInvoices`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var maxSeq int64
	for rows.Next() {
		var num string
		if err := rows.Scan(&num); err != nil {
			return "", err
		}
		// Only the six-digit sequence form counts ("PI-000001");
		// legacy timestamp numbers ("PI-YYYYMMDDHHMMSS") are longer
		// and are ignored.
		if len(num) == 9 && strings.HasPrefix(num, "PI-") {
			if seq, err := strconv.ParseInt(num[3:], 10, 64); err == nil && seq > maxSeq {
				maxSeq = seq
			}
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	next := hw + 1
	if maxSeq >= next {
		next = maxSeq + 1
	}
	if err := SetSettingTx(tx, "purchase_number_high_water", fmt.Sprintf("%d", next)); err != nil {
		return "", err
	}
	return fmt.Sprintf("PI-%06d", next), nil
}

// LiveCustomerBalance returns the live sum of open remainings
// (non-canceled invoices with RemainingAmount > 0), never the stored column.
func LiveCustomerBalance(tx *sql.Tx, customerID int64) (money.Cents, error) {
	var bal sql.NullInt64
	err := tx.QueryRow(`SELECT COALESCE(SUM(RemainingAmount),0) FROM Invoices
		WHERE CustomerID=? AND IsCanceled=0 AND RemainingAmount>0`, customerID).Scan(&bal)
	if err != nil {
		return 0, err
	}
	return money.Cents(bal.Int64), nil
}

// LiveCustomerBalanceDB is the non-transactional variant.
func (s *Store) LiveCustomerBalanceDB(customerID int64) (money.Cents, error) {
	var bal sql.NullInt64
	err := s.DB.QueryRow(`SELECT COALESCE(SUM(RemainingAmount),0) FROM Invoices
		WHERE CustomerID=? AND IsCanceled=0 AND RemainingAmount>0`, customerID).Scan(&bal)
	if err != nil {
		return 0, err
	}
	return money.Cents(bal.Int64), nil
}

// SyncCustomerBalance recomputes and stores Customers.Balance from the live sum.
func SyncCustomerBalance(tx *sql.Tx, customerID int64) error {
	bal, err := LiveCustomerBalance(tx, customerID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE Customers SET Balance=? WHERE CustomerID=?`, int64(bal), customerID)
	return err
}
