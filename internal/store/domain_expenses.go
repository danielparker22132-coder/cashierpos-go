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

// ---------------------------------------------------------------- expenses (E-01..E-03)

// Expense is one row of Expenses.
type Expense struct {
	ID          int64
	Description string
	Amount      money.Cents
	Category    string
	UserID      int64
	UserName    string
	ShiftID     int64
	ExpenseDate string
}

// RegisterExpense records an expense (E-01). All four roles may
// register (register_expense). It requires an open shift for the
// caller and is refused when it exceeds the expected drawer balance
// (RequireDrawerBalance — the drawer gate; negative is forbidden).
// Expenses reduce the expected drawer balance (H-09/E-02) and appear
// in the shift summary and the daily close.
func (s *Store) RegisterExpense(ctx context.Context, user *auth.User, description string, amount money.Cents, category string) (int64, error) {
	if user == nil {
		return 0, errors.New("يلزم تسجيل الدخول")
	}
	if err := rbac.Require(user.Role, rbac.PermRegisterExpense); err != nil {
		return 0, err
	}
	if strings.TrimSpace(description) == "" {
		return 0, errors.New("وصف المصروف مطلوب")
	}
	if amount <= 0 {
		return 0, errors.New("مبلغ غير صحيح")
	}
	var id int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		shiftID, err := OpenShiftID(tx, user.ID)
		if err != nil {
			return err
		}
		if err := RequireDrawerBalance(tx, shiftID, amount, "تسجيل هذا المصروف من الدرج"); err != nil {
			return err
		}
		res, err := tx.Exec(`INSERT INTO Expenses(Description,Amount,Category,UserID,ShiftID,ExpenseDate)
			VALUES(?,?,?,?,?,?)`, strings.TrimSpace(description), int64(amount),
			strings.TrimSpace(category), user.ID, shiftID, Now())
		if err != nil {
			return err
		}
		nid, err := res.LastInsertId()
		if err != nil {
			return err
		}
		id = nid
		detail := fmt.Sprintf("مصروف: %s — %s (شيفت %d)", strings.TrimSpace(description), money.FormatEGP(amount), shiftID)
		return AuditTx(tx, user.ID, user.UserName, "تسجيل مصروف", "مصروفات", nil, 0, 0, "", detail)
	})
	return id, err
}

// UpdateExpense edits an expense (E-03 list screen). The expense keeps
// its original UserID and ShiftID, so the correction stays inside the
// shift it was posted to. An amount change is refused once its shift
// is closed (the close snapshot would freeze while the live recompute
// moves); description/category-only edits stay allowed. A larger new
// amount is drawer-gated against the balance the old amount frees.
// FIX-18 O-C2 (parent ruling, 2026-11-26): updating an expense needs
// the create perm AND (the expense is the caller's own OR the caller
// is Manager/Admin — the view-all-cashbox holders, the same marker
// DeleteExpense uses), so a cashier can no longer rewrite another
// user's expense while being refused its delete.
func (s *Store) UpdateExpense(ctx context.Context, user *auth.User, expenseID int64, description string, amount money.Cents, category string) error {
	if user == nil {
		return errors.New("يلزم تسجيل الدخول")
	}
	if err := rbac.Require(user.Role, rbac.PermRegisterExpense); err != nil {
		return err
	}
	if strings.TrimSpace(description) == "" {
		return errors.New("وصف المصروف مطلوب")
	}
	if amount <= 0 {
		return errors.New("مبلغ غير صحيح")
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		var oldDesc, oldCat string
		var oldAmount, shiftID, ownerID int64
		err := tx.QueryRow(`SELECT Description,Amount,Category,ShiftID,UserID FROM Expenses WHERE ExpenseID=?`,
			expenseID).Scan(&oldDesc, &oldAmount, &oldCat, &shiftID, &ownerID)
		if err == sql.ErrNoRows {
			return errors.New("المصروف غير موجود")
		}
		if err != nil {
			return err
		}
		// FIX-18 O-C2: same ownership rule as DeleteExpense — a
		// non-owner who is not Manager/Admin may not rewrite
		// another user's expense. Checked before the closed-shift
		// gate, exactly like DeleteExpense.
		if ownerID != user.ID && !rbac.Can(user.Role, rbac.PermViewAllCashBox) {
			return errors.New("غير مسموح — لا يمكن تعديل مصروف مستخدم آخر")
		}
		if amount != money.Cents(oldAmount) {
			var isOpen int
			err := tx.QueryRow(`SELECT IsOpen FROM Shifts WHERE ShiftID=?`, shiftID).Scan(&isOpen)
			if err == nil && isOpen == 0 {
				return errors.New("لا يمكن تعديل مبلغ مصروف بعد إغلاق الشيفت")
			}
			if amount > money.Cents(oldAmount) {
				// The old amount is already inside the expected
				// balance; only the increase needs drawer room.
				if err := RequireDrawerBalance(tx, shiftID, amount-money.Cents(oldAmount),
					"تعديل هذا المصروف من الدرج"); err != nil {
					return err
				}
			}
		}
		if _, err := tx.Exec(`UPDATE Expenses SET Description=?, Amount=?, Category=? WHERE ExpenseID=?`,
			strings.TrimSpace(description), int64(amount), strings.TrimSpace(category), expenseID); err != nil {
			return err
		}
		detail := fmt.Sprintf("تعديل مصروف رقم %d: %s — من %s إلى %s",
			expenseID, strings.TrimSpace(description), money.FormatEGP(money.Cents(oldAmount)), money.FormatEGP(amount))
		return AuditTx(tx, user.ID, user.UserName, "تعديل مصروف", "مصروفات", nil, 0, 0, "", detail)
	})
}

// DeleteExpense deletes an expense (E-03). Refused once its shift is
// closed, so a frozen close snapshot can never disagree with the live
// recompute.
// FIX-17 expense-delete ruling (parent, 2026-11-26): deleting an
// expense needs the create perm AND (the expense is the caller's own
// OR the caller is Manager/Admin — the view-all-cashbox holders, the
// same marker ListExpenses uses for "sees all users"). A cashier could
// previously delete an admin-created expense.
func (s *Store) DeleteExpense(ctx context.Context, user *auth.User, expenseID int64) error {
	if user == nil {
		return errors.New("يلزم تسجيل الدخول")
	}
	if err := rbac.Require(user.Role, rbac.PermRegisterExpense); err != nil {
		return err
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		var desc string
		var amount, shiftID, ownerID int64
		err := tx.QueryRow(`SELECT Description,Amount,ShiftID,UserID FROM Expenses WHERE ExpenseID=?`,
			expenseID).Scan(&desc, &amount, &shiftID, &ownerID)
		if err == sql.ErrNoRows {
			return errors.New("المصروف غير موجود")
		}
		if err != nil {
			return err
		}
		if ownerID != user.ID && !rbac.Can(user.Role, rbac.PermViewAllCashBox) {
			return errors.New("غير مسموح — لا يمكن حذف مصروف مستخدم آخر")
		}
		var isOpen int
		if err := tx.QueryRow(`SELECT IsOpen FROM Shifts WHERE ShiftID=?`, shiftID).Scan(&isOpen); err == nil && isOpen == 0 {
			return errors.New("لا يمكن حذف مصروف بعد إغلاق الشيفت")
		}
		if _, err := tx.Exec(`DELETE FROM Expenses WHERE ExpenseID=?`, expenseID); err != nil {
			return err
		}
		detail := fmt.Sprintf("حذف مصروف رقم %d: %s — %s", expenseID, desc, money.FormatEGP(money.Cents(amount)))
		return AuditTx(tx, user.ID, user.UserName, "حذف مصروف", "مصروفات", nil, 0, 0, "", detail)
	})
}

// ListExpenses lists expenses in a date range. Admin/Manager
// (view_all_cashbox) see all users; anyone else sees only their own.
func (s *Store) ListExpenses(ctx context.Context, user *auth.User, from, to string) ([]*Expense, error) {
	if user == nil {
		return nil, errors.New("يلزم تسجيل الدخول")
	}
	if err := rbac.Require(user.Role, rbac.PermRegisterExpense); err != nil {
		return nil, err
	}
	if strings.TrimSpace(from) == "" {
		from = Today()
	}
	if strings.TrimSpace(to) == "" {
		to = Today()
	}
	canViewAll := rbac.Can(user.Role, rbac.PermViewAllCashBox)
	var rows *sql.Rows
	var err error
	if canViewAll {
		rows, err = s.DB.Query(`SELECT e.ExpenseID,e.Description,e.Amount,e.Category,e.UserID,
			COALESCE(u.UserName,''),e.ShiftID,e.ExpenseDate FROM Expenses e
			LEFT JOIN Users u ON u.UserID=e.UserID
			WHERE substr(e.ExpenseDate,1,10) BETWEEN ? AND ? ORDER BY e.ExpenseDate DESC, e.ExpenseID DESC`, from, to)
	} else {
		rows, err = s.DB.Query(`SELECT e.ExpenseID,e.Description,e.Amount,e.Category,e.UserID,
			COALESCE(u.UserName,''),e.ShiftID,e.ExpenseDate FROM Expenses e
			LEFT JOIN Users u ON u.UserID=e.UserID
			WHERE substr(e.ExpenseDate,1,10) BETWEEN ? AND ? AND e.UserID=?
			ORDER BY e.ExpenseDate DESC, e.ExpenseID DESC`, from, to, user.ID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Expense
	for rows.Next() {
		var e Expense
		var amt int64
		if err := rows.Scan(&e.ID, &e.Description, &amt, &e.Category, &e.UserID, &e.UserName, &e.ShiftID, &e.ExpenseDate); err != nil {
			return nil, err
		}
		e.Amount = money.Cents(amt)
		out = append(out, &e)
	}
	return out, rows.Err()
}

// GetExpense returns one expense by id.
func (s *Store) GetExpense(ctx context.Context, expenseID int64) (*Expense, error) {
	var e Expense
	var amt int64
	err := s.DB.QueryRow(`SELECT e.ExpenseID,e.Description,e.Amount,e.Category,e.UserID,
		COALESCE(u.UserName,''),e.ShiftID,e.ExpenseDate FROM Expenses e
		LEFT JOIN Users u ON u.UserID=e.UserID WHERE e.ExpenseID=?`, expenseID).
		Scan(&e.ID, &e.Description, &amt, &e.Category, &e.UserID, &e.UserName, &e.ShiftID, &e.ExpenseDate)
	if err == sql.ErrNoRows {
		return nil, errors.New("المصروف غير موجود")
	}
	if err != nil {
		return nil, err
	}
	e.Amount = money.Cents(amt)
	return &e, nil
}
