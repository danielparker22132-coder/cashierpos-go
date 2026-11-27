// domain_stocktake.go — session-based barcode stocktake (FEATURE_MAP §21).
//
// Lifecycle: open -> closed (counts frozen) -> approved (stamped,
// status stays closed) ; open -> cancelled. Approval applies each
// counted line's SNAPSHOT-relative delta (counted - SystemQty) onto
// live stock; products never counted are never touched (ST-09).
// Approving an already-approved session is a true no-op (S-M1).
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

// Stocktake is one Stocktakes row with its lines.
type Stocktake struct {
	ID            int64
	CreatedBy     string
	CreatedAt     string
	Status        string // open | closed | cancelled
	ClosedAt      string
	ClosedBy      string
	ApprovedBy    string
	ApprovedAt    string
	ApproveReason string
	Notes         string
	Lines         []*StocktakeLine
}

// StocktakeLine is one StocktakeLines row; Difference is computed.
type StocktakeLine struct {
	ID          int64
	StocktakeID int64
	ProductID   int64
	ProductName string
	SystemQty   money.Qty // snapshot at first scan (ST-07)
	CountedQty  money.Qty
	Difference  money.Qty // CountedQty - SystemQty
	CreatedAt   string
}

func scanStocktake(row *sql.Row) (*Stocktake, error) {
	var st Stocktake
	var createdBy, createdAt, closedAt, closedBy, approvedBy, approvedAt, approveReason, notes sql.NullString
	err := row.Scan(&st.ID, &createdBy, &createdAt, &st.Status, &closedAt, &closedBy,
		&approvedBy, &approvedAt, &approveReason, &notes)
	if err != nil {
		return nil, err
	}
	st.CreatedBy = createdBy.String
	st.CreatedAt = createdAt.String
	st.ClosedAt = closedAt.String
	st.ClosedBy = closedBy.String
	st.ApprovedBy = approvedBy.String
	st.ApprovedAt = approvedAt.String
	st.ApproveReason = approveReason.String
	st.Notes = notes.String
	return &st, nil
}

const stocktakeColumns = `StocktakeID,CreatedBy,CreatedAt,Status,ClosedAt,ClosedBy,
	ApprovedBy,ApprovedAt,ApproveReason,Notes`

func (s *Store) loadStocktakeTx(tx *sql.Tx, id int64) (*Stocktake, error) {
	st, err := scanStocktake(tx.QueryRow(`SELECT `+stocktakeColumns+` FROM Stocktakes WHERE StocktakeID=?`, id))
	if err == sql.ErrNoRows {
		return nil, errors.New("جلسة الجرد غير موجودة")
	}
	return st, err
}

func (s *Store) loadStocktakeLinesTx(tx *sql.Tx, id int64) ([]*StocktakeLine, error) {
	rows, err := tx.Query(`SELECT LineID,StocktakeID,ProductID,ProductName,SystemQty,CountedQty,CreatedAt
		FROM StocktakeLines WHERE StocktakeID=? ORDER BY LineID`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*StocktakeLine
	for rows.Next() {
		var ln StocktakeLine
		var counted sql.NullInt64
		if err := rows.Scan(&ln.ID, &ln.StocktakeID, &ln.ProductID, &ln.ProductName,
			&ln.SystemQty, &counted, &ln.CreatedAt); err != nil {
			return nil, err
		}
		ln.CountedQty = money.Qty(counted.Int64)
		ln.Difference = ln.CountedQty - ln.SystemQty
		out = append(out, &ln)
	}
	return out, rows.Err()
}

func (s *Store) loadStocktake(ctx context.Context, id int64) (*Stocktake, error) {
	st, err := scanStocktake(s.DB.QueryRowContext(ctx, `SELECT `+stocktakeColumns+` FROM Stocktakes WHERE StocktakeID=?`, id))
	if err == sql.ErrNoRows {
		return nil, errors.New("جلسة الجرد غير موجودة")
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT LineID,StocktakeID,ProductID,ProductName,SystemQty,CountedQty,CreatedAt
		FROM StocktakeLines WHERE StocktakeID=? ORDER BY LineID`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ln StocktakeLine
		var counted sql.NullInt64
		if err := rows.Scan(&ln.ID, &ln.StocktakeID, &ln.ProductID, &ln.ProductName,
			&ln.SystemQty, &counted, &ln.CreatedAt); err != nil {
			return nil, err
		}
		ln.CountedQty = money.Qty(counted.Int64)
		ln.Difference = ln.CountedQty - ln.SystemQty
		st.Lines = append(st.Lines, &ln)
	}
	return st, rows.Err()
}

// CreateStocktake opens a session (Admin/Manager only, ST-01).
func (s *Store) CreateStocktake(ctx context.Context, user *auth.User, notes string) (*Stocktake, error) {
	if err := rbac.Require(user.Role, rbac.PermManageStocktakes); err != nil {
		return nil, err
	}
	var id int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO Stocktakes(CreatedBy,CreatedAt,Status,Notes)
			VALUES(?,?, 'open', ?)`, user.UserName, Now(), strings.TrimSpace(notes))
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "إنشاء جلسة جرد", "مخزون", nil, 0, 0, "",
			fmt.Sprintf("إنشاء جلسة جرد رقم %d", id))
	})
	if err != nil {
		return nil, err
	}
	return s.GetStocktake(ctx, user, id)
}

// ListStocktakes lists sessions, newest first (view_stock; all roles).
func (s *Store) ListStocktakes(ctx context.Context, user *auth.User, status string) ([]*Stocktake, error) {
	if err := rbac.Require(user.Role, rbac.PermViewStock); err != nil {
		return nil, err
	}
	var rows *sql.Rows
	var err error
	switch status {
	case "", "all":
		rows, err = s.DB.QueryContext(ctx, `SELECT `+stocktakeColumns+` FROM Stocktakes ORDER BY StocktakeID DESC`)
	case "open", "closed", "cancelled":
		rows, err = s.DB.QueryContext(ctx, `SELECT `+stocktakeColumns+` FROM Stocktakes WHERE Status=? ORDER BY StocktakeID DESC`, status)
	default:
		return nil, errors.New("حالة الجرد غير صحيحة")
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Stocktake
	for rows.Next() {
		var st Stocktake
		var createdBy, createdAt, closedAt, closedBy, approvedBy, approvedAt, approveReason, notes sql.NullString
		if err := rows.Scan(&st.ID, &createdBy, &createdAt, &st.Status, &closedAt, &closedBy,
			&approvedBy, &approvedAt, &approveReason, &notes); err != nil {
			return nil, err
		}
		st.CreatedBy = createdBy.String
		st.CreatedAt = createdAt.String
		st.ClosedAt = closedAt.String
		st.ClosedBy = closedBy.String
		st.ApprovedBy = approvedBy.String
		st.ApprovedAt = approvedAt.String
		st.ApproveReason = approveReason.String
		st.Notes = notes.String
		out = append(out, &st)
	}
	return out, rows.Err()
}

// GetStocktake returns one session with its lines (view_stock).
func (s *Store) GetStocktake(ctx context.Context, user *auth.User, id int64) (*Stocktake, error) {
	if err := rbac.Require(user.Role, rbac.PermViewStock); err != nil {
		return nil, err
	}
	return s.loadStocktake(ctx, id)
}

func validateCountedQty(q money.Qty) error {
	if q < 0 || q > money.MaxQty {
		return errors.New("كمية غير صحيحة")
	}
	return nil
}

// upsertStocktakeLineTx creates or updates a line; the FIRST scan
// snapshots SystemQty from live Products.Quantity (ST-07).
func upsertStocktakeLineTx(tx *sql.Tx, stocktakeID, productID int64, counted money.Qty) (*StocktakeLine, error) {
	var lineID int64
	var systemQty money.Qty
	var productName string
	err := tx.QueryRow(`SELECT LineID,SystemQty FROM StocktakeLines WHERE StocktakeID=? AND ProductID=?`,
		stocktakeID, productID).Scan(&lineID, &systemQty)
	if err == sql.ErrNoRows {
		p, err := loadProductTx(tx, productID)
		if err != nil {
			return nil, err
		}
		productName = p.Name
		systemQty = p.Quantity
		res, err := tx.Exec(`INSERT INTO StocktakeLines(StocktakeID,ProductID,ProductName,SystemQty,CountedQty,CreatedAt)
			VALUES(?,?,?,?,?,?)`, stocktakeID, productID, productName, int64(systemQty), int64(counted), Now())
		if err != nil {
			return nil, err
		}
		lineID, err = res.LastInsertId()
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else {
		if _, err := tx.Exec(`UPDATE StocktakeLines SET CountedQty=? WHERE LineID=?`, int64(counted), lineID); err != nil {
			return nil, err
		}
		_ = tx.QueryRow(`SELECT ProductName FROM StocktakeLines WHERE LineID=?`, lineID).Scan(&productName)
	}
	return &StocktakeLine{ID: lineID, StocktakeID: stocktakeID, ProductID: productID,
		ProductName: productName, SystemQty: systemQty, CountedQty: counted,
		Difference: counted - systemQty}, nil
}

// ScanStocktakeLine counts one scan in an OPEN session (ST-02/ST-07).
// Piece item, no explicit qty -> counted += 1. Scale barcode ->
// counted += decoded weight. Explicit qty SETS the counted qty.
// qty == nil means "add one scan".
func (s *Store) ScanStocktakeLine(ctx context.Context, user *auth.User, stocktakeID int64, barcode string, qty *money.Qty) (*StocktakeLine, error) {
	if err := rbac.Require(user.Role, rbac.PermStocktakeCount); err != nil {
		return nil, err
	}
	barcode = strings.TrimSpace(barcode)
	if barcode == "" {
		return nil, errors.New("الباركود غير موجود")
	}
	// Resolve the product first (exact barcode, then scale decode).
	prod, dec, err := s.GetProductByBarcode(ctx, user, barcode)
	if err != nil {
		return nil, errors.New("الباركود غير موجود")
	}
	var line *StocktakeLine
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		st, err := s.loadStocktakeTx(tx, stocktakeID)
		if err != nil {
			return err
		}
		if st.Status != "open" {
			return errors.New("جلسة الجرد مغلقة ولا يمكن العد فيها")
		}
		var existing money.Qty
		_ = tx.QueryRow(`SELECT CountedQty FROM StocktakeLines WHERE StocktakeID=? AND ProductID=?`,
			stocktakeID, prod.ID).Scan(&existing)
		var newCounted money.Qty
		switch {
		case qty != nil:
			if err := validateCountedQty(*qty); err != nil {
				return err
			}
			newCounted = *qty
		case dec != nil:
			newCounted = existing + dec.Weight
		default:
			newCounted = existing + 1000
		}
		if err := validateCountedQty(newCounted); err != nil {
			return err
		}
		line, err = upsertStocktakeLineTx(tx, stocktakeID, prod.ID, newCounted)
		return err
	})
	if err != nil {
		return nil, err
	}
	return line, nil
}

// SetStocktakeCounted manually SETS a counted qty on an OPEN session
// (creates the line — snapshotting SystemQty — if not scanned yet).
func (s *Store) SetStocktakeCounted(ctx context.Context, user *auth.User, stocktakeID, productID int64, qty money.Qty) (*StocktakeLine, error) {
	if err := rbac.Require(user.Role, rbac.PermStocktakeCount); err != nil {
		return nil, err
	}
	if err := validateCountedQty(qty); err != nil {
		return nil, err
	}
	var line *StocktakeLine
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		st, err := s.loadStocktakeTx(tx, stocktakeID)
		if err != nil {
			return err
		}
		if st.Status != "open" {
			return errors.New("جلسة الجرد مغلقة ولا يمكن العد فيها")
		}
		// FIX-17 SW2-L03: refuse an inactive product like scan does
		// (scan resolves via GetProductByBarcode, WHERE IsActive=1).
		p, err := loadProductTx(tx, productID)
		if err != nil {
			return err
		}
		if !p.IsActive {
			return errors.New("المنتج غير مفعّل")
		}
		var err2 error
		line, err2 = upsertStocktakeLineTx(tx, stocktakeID, productID, qty)
		return err2
	})
	if err != nil {
		return nil, err
	}
	return line, nil
}

// CloseStocktake closes an OPEN session (Admin/Manager only, ST-03).
// Close freezes the counts; it changes no stock.
func (s *Store) CloseStocktake(ctx context.Context, user *auth.User, stocktakeID int64) (*Stocktake, error) {
	if err := rbac.Require(user.Role, rbac.PermManageStocktakes); err != nil {
		return nil, err
	}
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		st, err := s.loadStocktakeTx(tx, stocktakeID)
		if err != nil {
			return err
		}
		switch st.Status {
		case "closed":
			return errors.New("جلسة الجرد مغلقة بالفعل")
		case "cancelled":
			return errors.New("جلسة الجرد ملغاة")
		}
		if _, err := tx.Exec(`UPDATE Stocktakes SET Status='closed',ClosedAt=?,ClosedBy=? WHERE StocktakeID=?`,
			Now(), user.UserName, stocktakeID); err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "إغلاق جلسة جرد", "مخزون", nil, 0, 0, "",
			fmt.Sprintf("إغلاق جلسة جرد رقم %d", stocktakeID))
	})
	if err != nil {
		return nil, err
	}
	return s.GetStocktake(ctx, user, stocktakeID)
}

// CancelStocktake cancels an OPEN session only (Admin/Manager, ST-05).
func (s *Store) CancelStocktake(ctx context.Context, user *auth.User, stocktakeID int64) (*Stocktake, error) {
	if err := rbac.Require(user.Role, rbac.PermManageStocktakes); err != nil {
		return nil, err
	}
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		st, err := s.loadStocktakeTx(tx, stocktakeID)
		if err != nil {
			return err
		}
		switch st.Status {
		case "closed":
			return errors.New("جلسة الجرد مغلقة ولا يمكن إلغاؤها")
		case "cancelled":
			return errors.New("جلسة الجرد ملغاة بالفعل")
		}
		if _, err := tx.Exec(`UPDATE Stocktakes SET Status='cancelled' WHERE StocktakeID=?`, stocktakeID); err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "إلغاء جلسة جرد", "مخزون", nil, 0, 0, "",
			fmt.Sprintf("إلغاء جلسة جرد رقم %d", stocktakeID))
	})
	if err != nil {
		return nil, err
	}
	return s.GetStocktake(ctx, user, stocktakeID)
}

// ApproveStocktake approves a CLOSED session and posts its counts
// (Admin/Manager only, ST-04/ST-08). Each line applies its
// snapshot-relative delta onto live stock; a line with zero difference
// writes nothing. Already-approved sessions are a true no-op.
// Approval is refused as a whole — naming the product, nothing applied —
// when a counted line's product is inactive at approval time (FIX-17
// SW2-L03) or its delta would drive stock negative.
func (s *Store) ApproveStocktake(ctx context.Context, user *auth.User, stocktakeID int64, reason string) (*Stocktake, error) {
	if err := rbac.Require(user.Role, rbac.PermApproveStocktake); err != nil {
		return nil, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, errors.New("سبب التعديل مطلوب")
	}
	// No-op check before the tx (S-M1).
	st0, err := s.loadStocktake(ctx, stocktakeID)
	if err != nil {
		return nil, err
	}
	if st0.Status == "open" {
		return nil, errors.New("جلسة الجرد لم تُغلق بعد — أغلقها قبل الاعتماد")
	}
	if st0.Status == "cancelled" {
		return nil, errors.New("جلسة الجرد ملغاة ولا يمكن اعتمادها")
	}
	if st0.Status != "closed" {
		return nil, errors.New("جلسة الجرد غير مغلقة")
	}
	if st0.ApprovedAt != "" {
		return st0, nil
	}
	// Pre-check: no delta may drive a product negative, and no delta may
	// apply to a product deactivated after the count (FIX-17 SW2-L03).
	// Zero-delta lines apply nothing, so they are skipped here exactly
	// like in the negative check below.
	for _, ln := range st0.Lines {
		delta := ln.Difference
		if delta == 0 {
			continue
		}
		p, err := s.GetProduct(ctx, user, ln.ProductID)
		if err != nil {
			return nil, err
		}
		if !p.IsActive {
			return nil, fmt.Errorf("لا يمكن اعتماد الجرد: المنتج %s غير مفعّل", p.Name)
		}
		if p.Quantity+delta < 0 {
			return nil, fmt.Errorf("لا يمكن اعتماد الجرد: فرق الجرد للمنتج %s يجعل الكمية سالبة", p.Name)
		}
	}
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		st, err := s.loadStocktakeTx(tx, stocktakeID)
		if err != nil {
			return err
		}
		if st.ApprovedAt != "" {
			return nil // already approved inside the tx race: no-op
		}
		lines, err := s.loadStocktakeLinesTx(tx, stocktakeID)
		if err != nil {
			return err
		}
		applied := 0
		for _, ln := range lines {
			delta := ln.Difference
			if delta == 0 {
				continue
			}
			p, err := loadProductTx(tx, ln.ProductID)
			if err != nil {
				return err
			}
			// FIX-17 SW2-L03: in-tx guard against a product deactivated
			// between the pre-check and this tx; the whole approval is
			// refused, nothing is applied (same as the negative check).
			if !p.IsActive {
				return fmt.Errorf("لا يمكن اعتماد الجرد: المنتج %s غير مفعّل", p.Name)
			}
			newQty := p.Quantity + delta
			if newQty < 0 {
				return fmt.Errorf("لا يمكن اعتماد الجرد: فرق الجرد للمنتج %s يجعل الكمية سالبة", p.Name)
			}
			if _, err := tx.Exec(`UPDATE Products SET Quantity=? WHERE ProductID=?`, int64(newQty), ln.ProductID); err != nil {
				return err
			}
			absDelta := delta
			if absDelta < 0 {
				absDelta = -absDelta
			}
			notes := fmt.Sprintf("تعديل جرد: %s | من %s إلى %s", reason, money.FormatQty(p.Quantity), money.FormatQty(newQty))
			if err := insertMovementTx(tx, ln.ProductID, "adjust", absDelta, 0, notes, user.ID); err != nil {
				return err
			}
			if err := AuditTx(tx, user.ID, user.UserName, "تعديل مخزون", "مخزون", nil, 0, 0, "",
				fmt.Sprintf("جرد %s: من %s إلى %s | السبب: %s", ln.ProductName, money.FormatQty(p.Quantity), money.FormatQty(newQty), reason)); err != nil {
				return err
			}
			applied++
		}
		if _, err := tx.Exec(`UPDATE Stocktakes SET ApprovedAt=?,ApprovedBy=?,ApproveReason=? WHERE StocktakeID=?`,
			Now(), user.UserName, reason, stocktakeID); err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "اعتماد جرد", "مخزون", nil, 0, 0, "",
			fmt.Sprintf("اعتماد جلسة جرد رقم %d | عدد الأصناف المطبقة: %d | السبب: %s", stocktakeID, applied, reason))
	})
	if err != nil {
		return nil, err
	}
	return s.GetStocktake(ctx, user, stocktakeID)
}
