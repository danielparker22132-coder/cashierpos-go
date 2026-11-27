// Package store — B3: manual payables (GAP-REPORT §3.2).
//
// A manual payable is a standalone amount the shop owes a vendor or a
// courier, with its own due date and description — separate from purchase
// invoices and from CourierDebts. Rules (jony 2026-11-24):
//   - Create: Admin/Manager only (manage_payables).
//   - Reminder: DuePayables returns open payables due within 30 days or
//     overdue; it is shown to the owner (Admin/Manager) AND to the
//     on-shift cashier — no role gate on reading.
//   - Settle: the CASHIER may settle too (settle_payable; Supervisor may
//     NOT — matrix has no settle_payable for Supervisor), but only up to
//     what the drawer covers (drawer gate for cash). Who paid and when is
//     recorded (audit row; PaidAt on full settlement). The remaining never
//     goes below zero — partial settlement keeps Status 'partial'.
//   - Cancel (FIX-12 SW-H02): an open/partial payable can be cancelled
//     with a reason + audit row; a paid payable is final and can never
//     be cancelled, and a cancelled payable cannot be settled or
//     cancelled again. Cancelling moves no money and never touches the
//     drawer. Cancelled payables are excluded from the default list
//     (only an explicit status=cancelled filter shows them).
//   - View (FIX-12 SW-H08, §15 row 79): warehouse may NOT view payables
//     at all; admin/manager/supervisor/cashier may.
//   - Due date is optional (FIX-12 SW-M01) and the reason/description
//     is required (FIX-12 SW-M02).
//   - Settling a manual payable does NOT change Vendors.CurrentBalance or
//     CourierDebts: it is its own ledger. (If jony wants it linked to the
//     vendor balance, that is a follow-up decision — see report.)
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/money"
	"cashierpos-go/internal/rbac"
)

// Payable is one row of Payables.
type Payable struct {
	ID          int64
	Kind        string // vendor | courier
	PartyID     int64
	PartyName   string
	Amount      money.Cents
	Remaining   money.Cents
	DueDate     string // YYYY-MM-DD
	Description string
	Status      string // open | partial | paid | cancelled
	CreatedBy   string
	CreatedAt   string
	PaidAt      string
}

// PayableInput carries create fields.
type PayableInput struct {
	Kind        string // vendor | courier
	PartyID     int64
	PartyName   string // courier name when Kind=courier (vendor name is derived)
	Amount      money.Cents
	DueDate     string
	Description string
}

// CreatePayable creates a manual payable. Admin/Manager only.
// Amount must be > 0, kind must be vendor|courier, and the
// reason/description is required (FIX-12 SW-M02). The due date is
// optional (FIX-12 SW-M01): when given it must be YYYY-MM-DD; when
// empty the payable simply has no due date and never appears in the
// due/alerts lists.
// For kind=vendor the vendor must exist and its name is stored.
func (s *Store) CreatePayable(ctx context.Context, user *auth.User, in PayableInput) (int64, error) {
	if err := rbac.Require(user.Role, rbac.PermManagePayables); err != nil {
		return 0, err
	}
	kind := strings.ToLower(strings.TrimSpace(in.Kind))
	if kind != "vendor" && kind != "courier" {
		return 0, errors.New("نوع المستحق غير صحيح — يجب أن يكون مورداً أو مندوباً")
	}
	if in.Amount <= 0 {
		return 0, errors.New("مبلغ المستحق يجب أن يكون أكبر من صفر")
	}
	// FIX-12 SW-M02: a reason is required — a reason-less payable must
	// not enter the ledger.
	if strings.TrimSpace(in.Description) == "" {
		return 0, errors.New("وصف المستحق مطلوب — اكتب سبب المستحق")
	}
	// FIX-12 SW-M01: the due date is optional; validate the format
	// only when one is given.
	dueDate := strings.TrimSpace(in.DueDate)
	if dueDate != "" {
		if _, err := time.Parse("2006-01-02", dueDate); err != nil {
			return 0, errors.New("تاريخ الاستحقاق غير صحيح — استخدم الصيغة YYYY-MM-DD")
		}
	}
	var id int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		partyName := strings.TrimSpace(in.PartyName)
		if kind == "vendor" {
			if in.PartyID == 0 {
				return errors.New("برجاء اختيار المورد")
			}
			if err := tx.QueryRow(`SELECT VendorName FROM Vendors WHERE VendorID=?`, in.PartyID).Scan(&partyName); err == sql.ErrNoRows {
				return errors.New("المورد غير موجود")
			} else if err != nil {
				return err
			}
		} else {
			if partyName == "" {
				return errors.New("اسم المندوب مطلوب")
			}
		}
		res, err := tx.Exec(`INSERT INTO Payables(Kind,PartyID,PartyName,Amount,Remaining,DueDate,Description,Status,CreatedBy,CreatedAt)
			VALUES(?,?,?,?,?,?,?,'open',?,?)`, kind, in.PartyID, partyName, int64(in.Amount), int64(in.Amount),
			strings.TrimSpace(in.DueDate), strings.TrimSpace(in.Description), user.UserName, Now())
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "إضافة مستحق يدوي", "payables", nil, 0, 0, "",
			fmt.Sprintf("مستحق يدوي (%s) — %s — %s — يستحق %s", kind, partyName,
				money.FormatEGP(in.Amount), strings.TrimSpace(in.DueDate)))
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// GetPayable returns one payable.
// FIX-1 M7 (documented exception): payable reads take a user and require
// login, but no role permission — the routes open the payables list to
// every logged-in role (the cashier settles, the supervisor reviews);
// the settle action itself is gated by settle_payable.
func (s *Store) GetPayable(ctx context.Context, user *auth.User, id int64) (*Payable, error) {
	if err := requireUser(user, ""); err != nil {
		return nil, err
	}
	var p Payable
	var amt, rem int64
	var paidAt sql.NullString
	err := s.DB.QueryRow(`SELECT PayableID,Kind,PartyID,PartyName,Amount,Remaining,DueDate,Description,Status,CreatedBy,CreatedAt,PaidAt
		FROM Payables WHERE PayableID=?`, id).
		Scan(&p.ID, &p.Kind, &p.PartyID, &p.PartyName, &amt, &rem, &p.DueDate, &p.Description, &p.Status, &p.CreatedBy, &p.CreatedAt, &paidAt)
	if err == sql.ErrNoRows {
		return nil, errors.New("المستحق غير موجود")
	}
	if err != nil {
		return nil, err
	}
	p.Amount = money.Cents(amt)
	p.Remaining = money.Cents(rem)
	p.PaidAt = paidAt.String
	return &p, nil
}

// payablesViewDenied is the uniform §15 row 79 denial (FIX-12 SW-H08):
// warehouse may not view payables at all.
func payablesViewDenied(user *auth.User) error {
	return fmt.Errorf("غير مسموح — هذا الإجراء غير متاح لدورك الحالي. الدور: %s. تواصل مع المدير.", rbac.RoleDisplayName(user.Role))
}

// ListPayables returns payables, optionally filtered by status
// (open|partial|paid|cancelled) and/or kind. With no status filter the
// default list shows open+partial+paid only — cancelled payables are
// excluded (FIX-12 SW-H02); pass status=cancelled to see them.
// Warehouse is denied (FIX-12 SW-H08, §15 row 79).
func (s *Store) ListPayables(ctx context.Context, user *auth.User, status, kind string) ([]*Payable, error) {
	if err := requireUser(user, ""); err != nil {
		return nil, err
	}
	if rbac.EffectiveRole(user.Role) == rbac.RoleWarehouse {
		return nil, payablesViewDenied(user)
	}
	q := `SELECT PayableID,Kind,PartyID,PartyName,Amount,Remaining,DueDate,Description,Status,CreatedBy,CreatedAt,PaidAt
		FROM Payables WHERE 1=1`
	var args []any
	if strings.TrimSpace(status) != "" {
		q += ` AND Status=?`
		args = append(args, strings.TrimSpace(status))
	} else {
		// Default list: cancelled payables are history, not live work.
		q += ` AND Status<>'cancelled'`
	}
	if strings.TrimSpace(kind) != "" {
		q += ` AND Kind=?`
		args = append(args, strings.ToLower(strings.TrimSpace(kind)))
	}
	q += ` ORDER BY DueDate, PayableID`
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Payable
	for rows.Next() {
		var p Payable
		var amt, rem int64
		var paidAt sql.NullString
		if err := rows.Scan(&p.ID, &p.Kind, &p.PartyID, &p.PartyName, &amt, &rem, &p.DueDate, &p.Description,
			&p.Status, &p.CreatedBy, &p.CreatedAt, &paidAt); err != nil {
			return nil, err
		}
		p.Amount = money.Cents(amt)
		p.Remaining = money.Cents(rem)
		p.PaidAt = paidAt.String
		out = append(out, &p)
	}
	return out, rows.Err()
}

// DuePayables returns open/partial payables due within the next 30 days or
// already overdue — the reminder shown to the owner AND to the on-shift
// cashier (GAP-REPORT §3.2). A payable without a due date never appears
// here (FIX-12 SW-M01). Warehouse is denied (FIX-12 SW-H08, §15 row 79);
// settling is gated separately.
func (s *Store) DuePayables(ctx context.Context, user *auth.User) ([]*Payable, error) {
	if err := requireUser(user, ""); err != nil {
		return nil, err
	}
	if rbac.EffectiveRole(user.Role) == rbac.RoleWarehouse {
		return nil, payablesViewDenied(user)
	}
	limit := time.Now().In(cairoLoc()).AddDate(0, 0, 30).Format("2006-01-02")
	rows, err := s.DB.Query(`SELECT PayableID,Kind,PartyID,PartyName,Amount,Remaining,DueDate,Description,Status,CreatedBy,CreatedAt,PaidAt
		FROM Payables WHERE Status IN ('open','partial') AND DueDate<>'' AND DueDate<=? ORDER BY DueDate, PayableID`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Payable
	for rows.Next() {
		var p Payable
		var amt, rem int64
		var paidAt sql.NullString
		if err := rows.Scan(&p.ID, &p.Kind, &p.PartyID, &p.PartyName, &amt, &rem, &p.DueDate, &p.Description,
			&p.Status, &p.CreatedBy, &p.CreatedAt, &paidAt); err != nil {
			return nil, err
		}
		p.Amount = money.Cents(amt)
		p.Remaining = money.Cents(rem)
		p.PaidAt = paidAt.String
		out = append(out, &p)
	}
	return out, rows.Err()
}

// SettlePayable settles (fully or partially) a manual payable.
// The cashier may settle too (jony 2026-11-24) but only with what the
// drawer covers: cash requires an open shift + the drawer gate, and writes
// a CashBox 'out' row; non-cash never touches the drawer. Who settled and
// when is recorded in the audit row (and PaidAt when fully settled).
// Amount must be > 0 and never above the remaining — the remaining never
// goes below zero. Perm: settle_payable.
func (s *Store) SettlePayable(ctx context.Context, user *auth.User, id int64, amount money.Cents, method, notes string) (money.Cents, error) {
	if err := rbac.Require(user.Role, rbac.PermSettlePayable); err != nil {
		return 0, err
	}
	if amount <= 0 {
		return 0, errors.New("مبلغ السداد يجب أن يكون أكبر من صفر")
	}
	method, err := requireKnownPaymentMethod(method)
	if err != nil {
		return 0, err
	}
	var remainingAfter money.Cents
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		var p Payable
		var amt, rem int64
		if err := tx.QueryRow(`SELECT Kind,PartyID,PartyName,Amount,Remaining,DueDate,Description,Status
			FROM Payables WHERE PayableID=?`, id).
			Scan(&p.Kind, &p.PartyID, &p.PartyName, &amt, &rem, &p.DueDate, &p.Description, &p.Status); err == sql.ErrNoRows {
			return errors.New("المستحق غير موجود")
		} else if err != nil {
			return err
		}
		p.Amount = money.Cents(amt)
		p.Remaining = money.Cents(rem)
		if p.Status == "cancelled" {
			return errors.New("هذا المستحق ملغى — لا يمكن سداده")
		}
		if p.Status == "paid" || p.Remaining <= 0 {
			return errors.New("هذا المستحق مسدَّد بالكامل بالفعل")
		}
		if amount > p.Remaining {
			return fmt.Errorf("مبلغ السداد (%s) يتجاوز المتبقي على المستحق (%s) — الدفع الزائد مرفوض",
				money.FormatEGP(amount), money.FormatEGP(p.Remaining))
		}
		var shiftID int64
		if method == "Cash" {
			var err error
			shiftID, err = OpenShiftID(tx, user.ID)
			if err != nil {
				return err
			}
			if err := RequireDrawerBalance(tx, shiftID, amount, "سداد مستحق نقدي"); err != nil {
				return err
			}
		}
		remainingAfter = p.Remaining - amount
		status := "partial"
		paidAt := ""
		if remainingAfter == 0 {
			status = "paid"
			paidAt = Now()
		}
		if _, err := tx.Exec(`UPDATE Payables SET Remaining=?, Status=?, PaidAt=? WHERE PayableID=?`,
			int64(remainingAfter), status, nullableStr(paidAt), id); err != nil {
			return err
		}
		if method == "Cash" {
			if _, err := tx.Exec(`INSERT INTO CashBox(UserID,UserName,ShiftID,MovementType,Amount,Reason,MovementDate)
				VALUES(?,?,?,'out',?, ?,?)`, user.ID, user.UserName, shiftID, int64(amount),
				fmt.Sprintf("سداد مستحق نقدي — %s (%s)", p.PartyName, p.Description), Now()); err != nil {
				return err
			}
		}
		return AuditTx(tx, user.ID, user.UserName, "سداد مستحق يدوي", "payables", nil, 0, 0, "",
			fmt.Sprintf("سداد مستحق (%s) — %s — %s (%s) — سدّدها %s في %s — المتبقي %s — ملاحظات: %s",
				p.Kind, p.PartyName, money.FormatEGP(amount), method, user.UserName, Now(),
				money.FormatEGP(remainingAfter), strings.TrimSpace(notes)))
	})
	if err != nil {
		return 0, err
	}
	return remainingAfter, nil
}

// CancelPayable cancels a manual payable (FIX-12 SW-H02). Allowed only
// from open/partial status and only for Admin/Manager (manage_payables);
// a paid payable is final and can never be cancelled, and an already
// cancelled payable cannot be cancelled again. A reason is required and
// is recorded in the audit row. Cancelling moves NO money and never
// touches the drawer; the remaining is kept as the record of what was
// cancelled, and the row leaves the default list.
func (s *Store) CancelPayable(ctx context.Context, user *auth.User, id int64, reason string) error {
	if err := rbac.Require(user.Role, rbac.PermManagePayables); err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.New("سبب الإلغاء مطلوب")
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		var p Payable
		var amt, rem int64
		if err := tx.QueryRow(`SELECT Kind,PartyID,PartyName,Amount,Remaining,DueDate,Description,Status
			FROM Payables WHERE PayableID=?`, id).
			Scan(&p.Kind, &p.PartyID, &p.PartyName, &amt, &rem, &p.DueDate, &p.Description, &p.Status); err == sql.ErrNoRows {
			return errors.New("المستحق غير موجود")
		} else if err != nil {
			return err
		}
		p.Amount = money.Cents(amt)
		p.Remaining = money.Cents(rem)
		switch p.Status {
		case "paid":
			return errors.New("لا يمكن إلغاء مستحق مسدَّد بالكامل — السداد نهائي")
		case "cancelled":
			return errors.New("هذا المستحق ملغى بالفعل")
		}
		if _, err := tx.Exec(`UPDATE Payables SET Status='cancelled' WHERE PayableID=?`, id); err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "إلغاء مستحق يدوي", "payables", nil, 0, 0, "",
			fmt.Sprintf("إلغاء مستحق (%s) — %s — %s — المتبقي %s — السبب: %s",
				p.Kind, p.PartyName, money.FormatEGP(p.Amount), money.FormatEGP(p.Remaining), reason))
	})
}

func nullableStr(v string) any {
	if v == "" {
		return nil
	}
	return v
}
