// Package store — B3: courier debts (FEATURE_MAP §4 C-05, matrix rows 65-66).
//
// A courier debt is an amount a courier owes the shop (e.g. cash received
// on a delivery that was not handed over). Fields: courier name, amount,
// order/invoice reference, date, status.
//   - VIEW: all four roles (view_courier_debts).
//   - CREATE / UPDATE / DELETE: Manager/Admin/Supervisor only
//     (edit_courier_debts). The CASHIER is READ-ONLY for those.
//   - SETTLE: Manager/Admin/Supervisor AND any cashier receiving cash
//     (FIX-12 SW-H03, parent ruling per jony's 2026-11-19 redesign: ANY
//     cashier receiving cash clears the debt, recorded as a COLLECTION
//     on the receiving cashier — courier + receiver recorded; §15 row 66
//     is stale for the settle action).
//   - DELIVERY (FIX-12 SW-H04): confirming an online order's delivery
//     books the order amount as a debt on the courier (one debt per
//     order — see BookCourierDebtForOrder).
//   - Cash settlement requires an open shift + the drawer gate and writes
//     a CashBox 'in' row? No — a courier debt settlement is money COMING
//     IN (the courier hands cash over), so cash writes CashBox 'in'.
//     (Direction: shop-owes-courier is a Payable; courier-owes-shop is
//     this table. See report.)
//   - Amounts are never negative; settling never takes the debt below zero.
//     Partial settlement reduces Amount and keeps Status 'open'; full
//     settlement sets Status 'paid', PaidAt and PaidBy.
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

// CourierDebt is one row of CourierDebts.
type CourierDebt struct {
	ID            int64
	CourierName   string
	Amount        money.Cents
	OrderRef      string
	InvoiceNumber sql.NullInt64
	Status        string // open | paid
	CreatedAt     string
	PaidAt        string
	PaidBy        string
}

// CourierDebtInput carries create/update fields.
type CourierDebtInput struct {
	CourierName   string
	Amount        money.Cents
	OrderRef      string
	InvoiceNumber int64 // 0 = none
}

// CreateCourierDebt records a new courier debt. Manager/Admin/Supervisor
// only (edit_courier_debts); cashier is read-only.
func (s *Store) CreateCourierDebt(ctx context.Context, user *auth.User, in CourierDebtInput) (int64, error) {
	if err := rbac.Require(user.Role, rbac.PermEditCourierDebts); err != nil {
		return 0, err
	}
	name := strings.TrimSpace(in.CourierName)
	if name == "" {
		return 0, errors.New("اسم المندوب مطلوب")
	}
	if in.Amount <= 0 {
		return 0, errors.New("مبلغ الدين يجب أن يكون أكبر من صفر")
	}
	var id int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO CourierDebts(CourierName,Amount,OrderRef,InvoiceNumber,Status,CreatedAt)
			VALUES(?,?,?,?,'open',?)`, name, int64(in.Amount), strings.TrimSpace(in.OrderRef),
			nullableInt64(in.InvoiceNumber), Now())
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "إضافة دين مندوب", "courier", nil, 0, 0, "",
			fmt.Sprintf("دين مندوب — %s — %s — مرجع %s", name, money.FormatEGP(in.Amount), strings.TrimSpace(in.OrderRef)))
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

func nullableInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// UpdateCourierDebt edits a courier debt (open debts only; a paid debt is
// history and cannot be edited). Manager/Admin/Supervisor only.
func (s *Store) UpdateCourierDebt(ctx context.Context, user *auth.User, id int64, in CourierDebtInput) error {
	if err := rbac.Require(user.Role, rbac.PermEditCourierDebts); err != nil {
		return err
	}
	name := strings.TrimSpace(in.CourierName)
	if name == "" {
		return errors.New("اسم المندوب مطلوب")
	}
	if in.Amount <= 0 {
		return errors.New("مبلغ الدين يجب أن يكون أكبر من صفر")
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		var status string
		if err := tx.QueryRow(`SELECT Status FROM CourierDebts WHERE DebtID=?`, id).Scan(&status); err == sql.ErrNoRows {
			return errors.New("دين المندوب غير موجود")
		} else if err != nil {
			return err
		}
		if status == "paid" {
			return errors.New("لا يمكن تعديل دين مسدَّد — إنه سجل تاريخي")
		}
		if _, err := tx.Exec(`UPDATE CourierDebts SET CourierName=?,Amount=?,OrderRef=?,InvoiceNumber=? WHERE DebtID=?`,
			name, int64(in.Amount), strings.TrimSpace(in.OrderRef), nullableInt64(in.InvoiceNumber), id); err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "تعديل دين مندوب", "courier", nil, 0, 0, "",
			fmt.Sprintf("تعديل دين مندوب — %s — المبلغ الجديد %s", name, money.FormatEGP(in.Amount)))
	})
}

// DeleteCourierDebt deletes a courier debt. Manager/Admin/Supervisor only.
// A paid debt cannot be deleted (audit history).
func (s *Store) DeleteCourierDebt(ctx context.Context, user *auth.User, id int64) error {
	if err := rbac.Require(user.Role, rbac.PermEditCourierDebts); err != nil {
		return err
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		var name string
		var amount int64
		var status string
		if err := tx.QueryRow(`SELECT CourierName,Amount,Status FROM CourierDebts WHERE DebtID=?`, id).
			Scan(&name, &amount, &status); err == sql.ErrNoRows {
			return errors.New("دين المندوب غير موجود")
		} else if err != nil {
			return err
		}
		if status == "paid" {
			return errors.New("لا يمكن حذف دين مسدَّد — إنه سجل تاريخي")
		}
		if _, err := tx.Exec(`DELETE FROM CourierDebts WHERE DebtID=?`, id); err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "حذف دين مندوب", "courier", nil, 0, 0, "",
			fmt.Sprintf("حذف دين مندوب — %s — %s", name, money.FormatEGP(money.Cents(amount))))
	})
}

// GetCourierDebt returns one courier debt.
func (s *Store) GetCourierDebt(ctx context.Context, id int64) (*CourierDebt, error) {
	var d CourierDebt
	var amt int64
	var paidAt, paidBy sql.NullString
	err := s.DB.QueryRow(`SELECT DebtID,CourierName,Amount,OrderRef,InvoiceNumber,Status,CreatedAt,PaidAt,PaidBy
		FROM CourierDebts WHERE DebtID=?`, id).
		Scan(&d.ID, &d.CourierName, &amt, &d.OrderRef, &d.InvoiceNumber, &d.Status, &d.CreatedAt, &paidAt, &paidBy)
	if err == sql.ErrNoRows {
		return nil, errors.New("دين المندوب غير موجود")
	}
	if err != nil {
		return nil, err
	}
	d.Amount = money.Cents(amt)
	d.PaidAt = paidAt.String
	d.PaidBy = paidBy.String
	return &d, nil
}

// ListCourierDebts returns courier debts, optionally filtered by status
// (open|paid). Readable by all four roles (view_courier_debts).
// FIX-1 M7: service permission check (view_courier_debts).
func (s *Store) ListCourierDebts(ctx context.Context, user *auth.User, status string) ([]*CourierDebt, error) {
	if err := requireUser(user, rbac.PermViewCourierDebts); err != nil {
		return nil, err
	}
	var rows *sql.Rows
	var err error
	if strings.TrimSpace(status) == "" {
		rows, err = s.DB.Query(`SELECT DebtID,CourierName,Amount,OrderRef,InvoiceNumber,Status,CreatedAt,PaidAt,PaidBy
			FROM CourierDebts ORDER BY CreatedAt DESC, DebtID DESC`)
	} else {
		rows, err = s.DB.Query(`SELECT DebtID,CourierName,Amount,OrderRef,InvoiceNumber,Status,CreatedAt,PaidAt,PaidBy
			FROM CourierDebts WHERE Status=? ORDER BY CreatedAt DESC, DebtID DESC`, strings.TrimSpace(status))
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CourierDebt
	for rows.Next() {
		var d CourierDebt
		var amt int64
		var paidAt, paidBy sql.NullString
		if err := rows.Scan(&d.ID, &d.CourierName, &amt, &d.OrderRef, &d.InvoiceNumber, &d.Status, &d.CreatedAt, &paidAt, &paidBy); err != nil {
			return nil, err
		}
		d.Amount = money.Cents(amt)
		d.PaidAt = paidAt.String
		d.PaidBy = paidBy.String
		out = append(out, &d)
	}
	return out, rows.Err()
}

// CanSettleCourierDebt reports whether a role may settle a courier debt:
// holders of edit_courier_debts (admin/manager/supervisor) plus ANY
// cashier (FIX-12 SW-H03 — jony 2026-11-19 redesign). Create/edit/delete
// stay edit-only; this helper is settle-only. The web layer uses the
// same helper for its route gate, so the two gates cannot drift apart.
func CanSettleCourierDebt(role string) bool {
	return rbac.Can(role, rbac.PermEditCourierDebts) || rbac.EffectiveRole(role) == rbac.RoleCashier
}

// BookCourierDebtForOrder books an online order's delivery amount as a
// courier debt (FIX-12 SW-H04 — delivery confirmation books the amount
// as DEBT on the courier, jony 2026-11-19 redesign). Idempotent: an
// order books at most one debt — a second call for the same order
// returns the existing debt's id and writes nothing. The debt's
// OrderRef is "ONLINE-<orderID>". Admin/manager hold edit_courier_debts,
// so the staff member confirming the delivery may book it.
func (s *Store) BookCourierDebtForOrder(ctx context.Context, user *auth.User, orderID int64, courierName string, amount money.Cents) (int64, error) {
	if err := rbac.Require(user.Role, rbac.PermEditCourierDebts); err != nil {
		return 0, err
	}
	name := strings.TrimSpace(courierName)
	if name == "" {
		return 0, errors.New("اسم المندوب مطلوب")
	}
	if amount <= 0 {
		return 0, errors.New("مبلغ الدين يجب أن يكون أكبر من صفر")
	}
	ref := fmt.Sprintf("ONLINE-%d", orderID)
	var id int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRow(`SELECT DebtID FROM CourierDebts WHERE OrderRef=?`, ref).Scan(&id); err == nil {
			return nil // already booked — confirming twice books once
		} else if err != sql.ErrNoRows {
			return err
		}
		res, err := tx.Exec(`INSERT INTO CourierDebts(CourierName,Amount,OrderRef,InvoiceNumber,Status,CreatedAt)
			VALUES(?,?,?,NULL,'open',?)`, name, int64(amount), ref, Now())
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "دين مندوب من تأكيد توصيل", "courier", nil, 0, 0, "",
			fmt.Sprintf("دين مندوب من تأكيد توصيل طلب أونلاين #%d — %s — %s", orderID, name, money.FormatEGP(amount)))
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// SettleCourierDebt settles a courier debt (fully or partially).
// The courier hands money OVER, so a cash settlement writes CashBox 'in'
// and requires an open shift; non-cash never touches the drawer.
// Settle is open to edit_courier_debts holders AND to any cashier
// receiving cash (FIX-12 SW-H03): the settlement is recorded as a
// COLLECTION on the receiving cashier (CashBox 'in' courier_collection
// under the receiver's name + audit row naming courier and receiver).
// Create/edit/delete stay edit-only — a cashier still cannot do those.
// Amount must be > 0 and never above the debt — the debt never goes
// below zero. Full settlement records PaidAt/PaidBy.
func (s *Store) SettleCourierDebt(ctx context.Context, user *auth.User, id int64, amount money.Cents, method, notes string) (money.Cents, error) {
	if !CanSettleCourierDebt(user.Role) {
		return 0, rbac.Require(user.Role, rbac.PermEditCourierDebts)
	}
	if amount <= 0 {
		return 0, errors.New("مبلغ السداد يجب أن يكون أكبر من صفر")
	}
	method, err := requireKnownPaymentMethod(method)
	if err != nil {
		return 0, err
	}
	var remaining money.Cents
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		var name, orderRef, status string
		var amt int64
		if err := tx.QueryRow(`SELECT CourierName,Amount,OrderRef,Status FROM CourierDebts WHERE DebtID=?`, id).
			Scan(&name, &amt, &orderRef, &status); err == sql.ErrNoRows {
			return errors.New("دين المندوب غير موجود")
		} else if err != nil {
			return err
		}
		if status == "paid" || amt <= 0 {
			return errors.New("هذا الدين مسدَّد بالكامل بالفعل")
		}
		if amount > money.Cents(amt) {
			return fmt.Errorf("مبلغ السداد (%s) يتجاوز دين المندوب (%s) — الدفع الزائد مرفوض",
				money.FormatEGP(amount), money.FormatEGP(money.Cents(amt)))
		}
		var shiftID int64
		if method == "Cash" {
			var err error
			shiftID, err = OpenShiftID(tx, user.ID)
			if err != nil {
				return err
			}
		}
		remaining = money.Cents(amt) - amount
		now := Now()
		if remaining == 0 {
			if _, err := tx.Exec(`UPDATE CourierDebts SET Amount=0, Status='paid', PaidAt=?, PaidBy=? WHERE DebtID=?`,
				now, user.UserName, id); err != nil {
				return err
			}
		} else {
			if _, err := tx.Exec(`UPDATE CourierDebts SET Amount=? WHERE DebtID=?`, int64(remaining), id); err != nil {
				return err
			}
		}
		if method == "Cash" {
			if _, err := tx.Exec(`INSERT INTO CashBox(UserID,UserName,ShiftID,MovementType,Amount,Reason,MovementDate,AutoKind)
				VALUES(?,?,?,'in',?, ?,?,'courier_collection')`, user.ID, user.UserName, shiftID, int64(amount),
				fmt.Sprintf("سداد دين مندوب — %s — مرجع %s", name, orderRef), now); err != nil {
				return err
			}
		}
		return AuditTx(tx, user.ID, user.UserName, "سداد دين مندوب", "courier", nil, 0, 0, "",
			fmt.Sprintf("سداد دين مندوب %s — %s (%s) — سدّدها %s في %s — المتبقي %s — ملاحظات: %s",
				name, money.FormatEGP(amount), method, user.UserName, now, money.FormatEGP(remaining), strings.TrimSpace(notes)))
	})
	if err != nil {
		return 0, err
	}
	return remaining, nil
}
