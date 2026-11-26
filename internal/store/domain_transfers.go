// Package store — B3: electronic transfers (FEATURE_MAP §4 C-06/C-07,
// matrix rows 67-69, GAP-REPORT §3.8).
//
// An electronic transfer is a record of money received electronically
// (InstaPay / Vodafone Cash / Visa / card): sender, amount, method,
// reference, status pending|approved|rejected, reviewer + time + note.
//   - VIEW: everyone (view_transfers; all four roles).
//   - APPROVE: everyone including the CASHIER (approve_transfer, C-06).
//   - REJECT: Manager/Admin/Supervisor ONLY (reject_transfer, C-07) —
//     the cashier is explicitly refused in the service layer.
//   - Only a pending transfer can be approved/rejected; re-review is
//     refused. Reviewer, time and note are recorded + an audit row.
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

// Transfer is one row of ElectronicTransfers.
type Transfer struct {
	ID         int64
	SenderName string
	Amount     money.Cents
	Method     string
	Reference  string
	Status     string // pending | approved | rejected
	CreatedAt  string
	ReviewedBy string
	ReviewedAt string
	ReviewNote string
}

// TransferInput carries create fields.
type TransferInput struct {
	SenderName string
	Amount     money.Cents
	Method     string
	Reference  string
}

// CreateTransfer records an incoming electronic transfer as pending.
// FIX-1 M7: creating a transfer is a write — it needs the dedicated
// PermCreateTransfer (all four sales roles), not the read-only
// PermViewTransfers.
func (s *Store) CreateTransfer(ctx context.Context, user *auth.User, in TransferInput) (int64, error) {
	if err := rbac.Require(user.Role, rbac.PermCreateTransfer); err != nil {
		return 0, err
	}
	sender := strings.TrimSpace(in.SenderName)
	if sender == "" {
		return 0, errors.New("اسم المرسل مطلوب")
	}
	if in.Amount <= 0 {
		return 0, errors.New("مبلغ التحويل يجب أن يكون أكبر من صفر")
	}
	method := strings.TrimSpace(in.Method)
	if method == "" {
		return 0, errors.New("طريقة التحويل مطلوبة (مثال: فودافون كاش، إنستاباي، فيزا)")
	}
	var id int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO ElectronicTransfers(SenderName,Amount,Method,Reference,Status,CreatedAt)
			VALUES(?,?,?,?, 'pending',?)`, sender, int64(in.Amount), method, strings.TrimSpace(in.Reference), Now())
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "تسجيل تحويل إلكتروني", "transfers", nil, 0, 0, "",
			fmt.Sprintf("تحويل إلكتروني — %s — %s (%s) — مرجع %s", sender, money.FormatEGP(in.Amount), method, strings.TrimSpace(in.Reference)))
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// GetTransfer returns one transfer.
// FIX-1 M7: service permission check (view_transfers).
func (s *Store) GetTransfer(ctx context.Context, user *auth.User, id int64) (*Transfer, error) {
	if err := requireUser(user, rbac.PermViewTransfers); err != nil {
		return nil, err
	}
	var t Transfer
	var amt int64
	var reviewedBy, reviewedAt, note sql.NullString
	err := s.DB.QueryRow(`SELECT TransferID,SenderName,Amount,Method,Reference,Status,CreatedAt,ReviewedBy,ReviewedAt,ReviewNote
		FROM ElectronicTransfers WHERE TransferID=?`, id).
		Scan(&t.ID, &t.SenderName, &amt, &t.Method, &t.Reference, &t.Status, &t.CreatedAt, &reviewedBy, &reviewedAt, &note)
	if err == sql.ErrNoRows {
		return nil, errors.New("التحويل غير موجود")
	}
	if err != nil {
		return nil, err
	}
	t.Amount = money.Cents(amt)
	t.ReviewedBy = reviewedBy.String
	t.ReviewedAt = reviewedAt.String
	t.ReviewNote = note.String
	return &t, nil
}

// ListTransfers returns transfers, optionally filtered by status
// (pending|approved|rejected).
// FIX-1 M7: service permission check (view_transfers).
func (s *Store) ListTransfers(ctx context.Context, user *auth.User, status string) ([]*Transfer, error) {
	if err := requireUser(user, rbac.PermViewTransfers); err != nil {
		return nil, err
	}
	var rows *sql.Rows
	var err error
	if strings.TrimSpace(status) == "" {
		rows, err = s.DB.Query(`SELECT TransferID,SenderName,Amount,Method,Reference,Status,CreatedAt,ReviewedBy,ReviewedAt,ReviewNote
			FROM ElectronicTransfers ORDER BY CreatedAt DESC, TransferID DESC`)
	} else {
		rows, err = s.DB.Query(`SELECT TransferID,SenderName,Amount,Method,Reference,Status,CreatedAt,ReviewedBy,ReviewedAt,ReviewNote
			FROM ElectronicTransfers WHERE Status=? ORDER BY CreatedAt DESC, TransferID DESC`, strings.TrimSpace(status))
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Transfer
	for rows.Next() {
		var t Transfer
		var amt int64
		var reviewedBy, reviewedAt, note sql.NullString
		if err := rows.Scan(&t.ID, &t.SenderName, &amt, &t.Method, &t.Reference, &t.Status, &t.CreatedAt,
			&reviewedBy, &reviewedAt, &note); err != nil {
			return nil, err
		}
		t.Amount = money.Cents(amt)
		t.ReviewedBy = reviewedBy.String
		t.ReviewedAt = reviewedAt.String
		t.ReviewNote = note.String
		out = append(out, &t)
	}
	return out, rows.Err()
}

// reviewTransferTx is the shared approve/reject core: pending-only,
// records reviewer + time + note + audit.
func reviewTransferTx(tx *sql.Tx, user *auth.User, id int64, status, action, note string) error {
	var cur string
	var sender, method, ref string
	var amt int64
	if err := tx.QueryRow(`SELECT SenderName,Amount,Method,Reference,Status FROM ElectronicTransfers WHERE TransferID=?`, id).
		Scan(&sender, &amt, &method, &ref, &cur); err == sql.ErrNoRows {
		return errors.New("التحويل غير موجود")
	} else if err != nil {
		return err
	}
	if cur != "pending" {
		return fmt.Errorf("لا يمكن مراجعة هذا التحويل — حالته الحالية: %s", transferStatusAr(cur))
	}
	now := Now()
	if _, err := tx.Exec(`UPDATE ElectronicTransfers SET Status=?,ReviewedBy=?,ReviewedAt=?,ReviewNote=? WHERE TransferID=?`,
		status, user.UserName, now, strings.TrimSpace(note), id); err != nil {
		return err
	}
	return AuditTx(tx, user.ID, user.UserName, action, "transfers", nil, 0, 0, "",
		fmt.Sprintf("%s — %s — %s (%s) — مرجع %s — راجعها %s في %s — ملاحظة: %s",
			action, sender, money.FormatEGP(money.Cents(amt)), method, ref, user.UserName, now, strings.TrimSpace(note)))
}

func transferStatusAr(s string) string {
	switch s {
	case "pending":
		return "قيد المراجعة"
	case "approved":
		return "معتمد"
	case "rejected":
		return "مرفوض"
	default:
		return s
	}
}

// TransferStatusAr is the exported Arabic label for a transfer status.
func TransferStatusAr(s string) string { return transferStatusAr(s) }

// ApproveTransfer approves a pending transfer. The CASHIER may approve
// too (C-06, matrix row 68). Perm: approve_transfer.
func (s *Store) ApproveTransfer(ctx context.Context, user *auth.User, id int64, note string) error {
	if err := rbac.Require(user.Role, rbac.PermApproveTransfer); err != nil {
		return err
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		return reviewTransferTx(tx, user, id, "approved", "اعتماد تحويل إلكتروني", note)
	})
}

// RejectTransfer rejects a pending transfer. Manager/Admin/Supervisor
// ONLY — the cashier is explicitly refused (C-07, matrix row 69).
// Perm: reject_transfer.
func (s *Store) RejectTransfer(ctx context.Context, user *auth.User, id int64, note string) error {
	if err := rbac.Require(user.Role, rbac.PermRejectTransfer); err != nil {
		return err
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		return reviewTransferTx(tx, user, id, "rejected", "رفض تحويل إلكتروني", note)
	})
}
