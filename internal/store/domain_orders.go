// domain_orders.go — local online-orders store (FIX-3 H1, web bell).
//
// Jony's decision 2026-11-25: the Go build gets the web bell for real,
// backed by a LOCAL SQLite table (OnlineOrders) fed by a local intake API
// with the CloudApi shape. The bell reads this local store; when CloudApi
// sync lands, the intake moves to the server. Nothing here claims a
// server connection that does not exist.
//
// Pending (CloudApi semantics, literal): Status CREATED or PREPARED, or
// PaymentStatus AWAITING_VERIFICATION. Pending evidence: PaymentStatus
// AWAITING_VERIFICATION (an order waiting for its payment proof to be
// reviewed). Statuses: CREATED → PREPARED → READY → DELIVERED →
// COMPLETED, CANCELED from CREATED/PREPARED. Payment statuses: PENDING,
// AWAITING_VERIFICATION, VERIFIED, PAID.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/money"
	"cashierpos-go/internal/rbac"
)

// Online order statuses.
const (
	OrderCreated   = "CREATED"
	OrderPrepared  = "PREPARED"
	OrderReady     = "READY"
	OrderDelivered = "DELIVERED"
	OrderCompleted = "COMPLETED"
	OrderCanceled  = "CANCELED"
)

// Online payment statuses.
const (
	PayPending              = "PENDING"
	PayAwaitingVerification = "AWAITING_VERIFICATION"
	PayVerified             = "VERIFIED"
	PayPaid                 = "PAID"
)

var orderStatuses = map[string]bool{
	OrderCreated: true, OrderPrepared: true, OrderReady: true,
	OrderDelivered: true, OrderCompleted: true, OrderCanceled: true,
}

var orderPayStatuses = map[string]bool{
	PayPending: true, PayAwaitingVerification: true, PayVerified: true, PayPaid: true,
}

// orderNextStatus lists the statuses an order may move to from each state.
var orderNextStatus = map[string][]string{
	OrderCreated:   {OrderPrepared, OrderCanceled},
	OrderPrepared:  {OrderReady, OrderCanceled},
	OrderReady:     {OrderDelivered},
	OrderDelivered: {OrderCompleted},
	OrderCompleted: {},
	OrderCanceled:  {},
}

// OnlineOrderItem is one line of an online order (stored as ItemsJSON).
type OnlineOrderItem struct {
	ProductID int64       `json:"product_id"`
	Name      string      `json:"name"`
	Qty       money.Qty   `json:"qty"` // milliunits
	UnitPrice money.Cents `json:"unit_price"`
}

// OnlineOrder is one row of OnlineOrders.
type OnlineOrder struct {
	ID            int64
	CustomerName  string
	CustomerPhone string
	Items         []OnlineOrderItem
	Total         money.Cents
	Status        string
	PaymentStatus string
	PaymentMethod string
	EvidenceRef   string
	Notes         string
	CreatedBy     string
	CreatedAt     string
	UpdatedAt     string
}

// OnlineOrderInput is the intake payload (CloudApi shape).
type OnlineOrderInput struct {
	CustomerName  string
	CustomerPhone string
	Items         []OnlineOrderItem
	Total         money.Cents // 0 = compute from items; otherwise must match
	Status        string      // "" = CREATED
	PaymentStatus string      // "" = PENDING
	PaymentMethod string
	EvidenceRef   string // optional payment-proof reference
	Notes         string
}

func scanOnlineOrder(row *sql.Row) (*OnlineOrder, error) {
	var o OnlineOrder
	var itemsJSON, createdAt, updatedAt sql.NullString
	var total int64
	if err := row.Scan(&o.ID, &o.CustomerName, &o.CustomerPhone, &itemsJSON, &total,
		&o.Status, &o.PaymentStatus, &o.PaymentMethod, &o.EvidenceRef, &o.Notes,
		&o.CreatedBy, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	o.Total = money.Cents(total)
	o.CreatedAt = createdAt.String
	o.UpdatedAt = updatedAt.String
	if itemsJSON.String != "" {
		if err := json.Unmarshal([]byte(itemsJSON.String), &o.Items); err != nil {
			return nil, fmt.Errorf("طلب أونلاين %d: أصناف غير صالحة", o.ID)
		}
	}
	return &o, nil
}

const onlineOrderCols = `OrderID,CustomerName,CustomerPhone,ItemsJSON,TotalAmount,
	Status,PaymentStatus,PaymentMethod,EvidenceRef,Notes,CreatedBy,CreatedAt,UpdatedAt`

// CreateOnlineOrder records an online order from the local intake API
// (admin/manager only — the public storefront will call CloudApi when
// sync lands; until then intake is staff-gated).
func (s *Store) CreateOnlineOrder(ctx context.Context, user *auth.User, in OnlineOrderInput) (*OnlineOrder, error) {
	if err := rbac.Require(user.Role, rbac.PermManageOrders); err != nil {
		return nil, err
	}
	in.CustomerName = strings.TrimSpace(in.CustomerName)
	if in.CustomerName == "" {
		return nil, errors.New("اسم عميل الطلب الأونلاين مطلوب")
	}
	if len(in.Items) == 0 {
		return nil, errors.New("طلب الأونلاين يحتاج صنفاً واحداً على الأقل")
	}
	var computed money.Cents
	for i, it := range in.Items {
		if strings.TrimSpace(it.Name) == "" {
			return nil, fmt.Errorf("صنف %d: الاسم مطلوب", i+1)
		}
		if it.Qty <= 0 {
			return nil, fmt.Errorf("صنف %d: الكمية يجب أن تكون أكبر من صفر", i+1)
		}
		if it.UnitPrice < 0 {
			return nil, fmt.Errorf("صنف %d: سعر الوحدة لا يمكن أن يكون سالباً", i+1)
		}
		computed += money.MulQty(it.UnitPrice, it.Qty)
	}
	if computed <= 0 {
		return nil, errors.New("إجمالي الطلب الأونلاين يجب أن يكون أكبر من صفر")
	}
	if in.Total == 0 {
		in.Total = computed
	} else if in.Total != computed {
		return nil, errors.New("إجمالي الطلب لا يطابق مجموع الأصناف")
	}
	status := strings.ToUpper(strings.TrimSpace(in.Status))
	if status == "" {
		status = OrderCreated
	}
	if !orderStatuses[status] {
		return nil, errors.New("حالة الطلب غير صحيحة")
	}
	pay := strings.ToUpper(strings.TrimSpace(in.PaymentStatus))
	if pay == "" {
		pay = PayPending
	}
	if !orderPayStatuses[pay] {
		return nil, errors.New("حالة الدفع غير صحيحة")
	}
	itemsJSON, err := json.Marshal(in.Items)
	if err != nil {
		return nil, err
	}
	now := Now()
	var o *OnlineOrder
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO OnlineOrders(CustomerName,CustomerPhone,ItemsJSON,
			TotalAmount,Status,PaymentStatus,PaymentMethod,EvidenceRef,Notes,CreatedBy,CreatedAt,UpdatedAt)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			in.CustomerName, strings.TrimSpace(in.CustomerPhone), string(itemsJSON),
			int64(in.Total), status, pay, strings.TrimSpace(in.PaymentMethod),
			strings.TrimSpace(in.EvidenceRef), strings.TrimSpace(in.Notes),
			user.UserName, now, now)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		o = &OnlineOrder{ID: id, CustomerName: in.CustomerName,
			CustomerPhone: strings.TrimSpace(in.CustomerPhone), Items: in.Items,
			Total: in.Total, Status: status, PaymentStatus: pay,
			PaymentMethod: strings.TrimSpace(in.PaymentMethod),
			EvidenceRef: strings.TrimSpace(in.EvidenceRef),
			Notes: strings.TrimSpace(in.Notes), CreatedBy: user.UserName,
			CreatedAt: now, UpdatedAt: now}
		return AuditTx(tx, user.ID, user.UserName, "إنشاء طلب أونلاين", "طلبات المتجر",
			nil, 0, 0, in.CustomerName,
			fmt.Sprintf("طلب أونلاين #%d — %s — الإجمالي %s — الحالة %s", id, in.CustomerName, money.FormatEGP(in.Total), status))
	})
	if err != nil {
		return nil, err
	}
	return o, nil
}

// PendingOrdersCount returns the web-bell pending count (any logged-in
// staff): Status CREATED or PREPARED, or PaymentStatus
// AWAITING_VERIFICATION. A CANCELED order never counts, even when its
// payment is still awaiting verification (FIX-5 R5-L3: a cancelled order
// owes no delivery and its proof is moot). A COMPLETED order with
// awaiting payment still counts — payment review is still owed there.
func (s *Store) PendingOrdersCount(ctx context.Context, user *auth.User) (int, error) {
	if user == nil {
		return 0, errors.New("يلزم تسجيل الدخول")
	}
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM OnlineOrders
		WHERE Status <> 'CANCELED' AND
			(Status IN ('CREATED','PREPARED') OR PaymentStatus='AWAITING_VERIFICATION')`).Scan(&n)
	return n, err
}

// PendingEvidenceCount returns the orders waiting for payment-proof
// review (any logged-in staff). A CANCELED order never counts
// (FIX-5 R5-L3).
func (s *Store) PendingEvidenceCount(ctx context.Context, user *auth.User) (int, error) {
	if user == nil {
		return 0, errors.New("يلزم تسجيل الدخول")
	}
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM OnlineOrders
		WHERE PaymentStatus='AWAITING_VERIFICATION' AND Status <> 'CANCELED'`).Scan(&n)
	return n, err
}

// ListOnlineOrders lists online orders, newest first (any logged-in
// staff). status "" lists all.
func (s *Store) ListOnlineOrders(ctx context.Context, user *auth.User, status string) ([]*OnlineOrder, error) {
	if user == nil {
		return nil, errors.New("يلزم تسجيل الدخول")
	}
	q := `SELECT ` + onlineOrderCols + ` FROM OnlineOrders`
	var args []any
	if status != "" {
		if !orderStatuses[strings.ToUpper(status)] {
			return nil, errors.New("حالة الطلب غير صحيحة")
		}
		q += ` WHERE Status=?`
		args = append(args, strings.ToUpper(status))
	}
	q += ` ORDER BY OrderID DESC`
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*OnlineOrder
	for rows.Next() {
		var o OnlineOrder
		var itemsJSON, createdAt, updatedAt sql.NullString
		var total int64
		if err := rows.Scan(&o.ID, &o.CustomerName, &o.CustomerPhone, &itemsJSON, &total,
			&o.Status, &o.PaymentStatus, &o.PaymentMethod, &o.EvidenceRef, &o.Notes,
			&o.CreatedBy, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		o.Total = money.Cents(total)
		o.CreatedAt = createdAt.String
		o.UpdatedAt = updatedAt.String
		if itemsJSON.String != "" {
			if err := json.Unmarshal([]byte(itemsJSON.String), &o.Items); err != nil {
				return nil, err
			}
		}
		out = append(out, &o)
	}
	return out, rows.Err()
}

// GetOnlineOrder returns one online order (any logged-in staff).
func (s *Store) GetOnlineOrder(ctx context.Context, user *auth.User, id int64) (*OnlineOrder, error) {
	if user == nil {
		return nil, errors.New("يلزم تسجيل الدخول")
	}
	o, err := scanOnlineOrder(s.DB.QueryRowContext(ctx, `SELECT `+onlineOrderCols+`
		FROM OnlineOrders WHERE OrderID=?`, id))
	if err == sql.ErrNoRows {
		return nil, errors.New("الطلب غير موجود")
	}
	return o, err
}

// ResolveOrderEvidence marks an awaiting-verification order's payment
// proof as verified (admin/manager only — "الأدمن يحل").
func (s *Store) ResolveOrderEvidence(ctx context.Context, user *auth.User, id int64) (*OnlineOrder, error) {
	if err := rbac.Require(user.Role, rbac.PermManageOrders); err != nil {
		return nil, err
	}
	var o *OnlineOrder
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		cur, err := scanOnlineOrder(tx.QueryRow(`SELECT ` + onlineOrderCols + `
			FROM OnlineOrders WHERE OrderID=?`, id))
		if err == sql.ErrNoRows {
			return errors.New("الطلب غير موجود")
		}
		if err != nil {
			return err
		}
		if cur.PaymentStatus != PayAwaitingVerification {
			return errors.New("هذا الطلب ليس في انتظار مراجعة دليل الدفع")
		}
		if cur.Status == OrderCanceled {
			return errors.New("لا يمكن مراجعة دليل دفع طلب ملغى")
		}
		if _, err := tx.Exec(`UPDATE OnlineOrders SET PaymentStatus=?, UpdatedAt=?
			WHERE OrderID=?`, PayVerified, Now(), id); err != nil {
			return err
		}
		cur.PaymentStatus = PayVerified
		o = cur
		return AuditTx(tx, user.ID, user.UserName, "مراجعة دليل دفع طلب أونلاين", "طلبات المتجر",
			nil, 0, 0, cur.CustomerName,
			fmt.Sprintf("طلب أونلاين #%d — تم التحقق من دليل الدفع", id))
	})
	if err != nil {
		return nil, err
	}
	return o, nil
}

// SetOnlineOrderStatus advances an order to a valid next status
// (admin/manager only).
func (s *Store) SetOnlineOrderStatus(ctx context.Context, user *auth.User, id int64, status string) (*OnlineOrder, error) {
	if err := rbac.Require(user.Role, rbac.PermManageOrders); err != nil {
		return nil, err
	}
	status = strings.ToUpper(strings.TrimSpace(status))
	if !orderStatuses[status] {
		return nil, errors.New("حالة الطلب غير صحيحة")
	}
	var o *OnlineOrder
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		cur, err := scanOnlineOrder(tx.QueryRow(`SELECT ` + onlineOrderCols + `
			FROM OnlineOrders WHERE OrderID=?`, id))
		if err == sql.ErrNoRows {
			return errors.New("الطلب غير موجود")
		}
		if err != nil {
			return err
		}
		allowed := false
		for _, next := range orderNextStatus[cur.Status] {
			if next == status {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("لا يمكن نقل الطلب من %s إلى %s", cur.Status, status)
		}
		if _, err := tx.Exec(`UPDATE OnlineOrders SET Status=?, UpdatedAt=?
			WHERE OrderID=?`, status, Now(), id); err != nil {
			return err
		}
		cur.Status = status
		o = cur
		return AuditTx(tx, user.ID, user.UserName, "تغيير حالة طلب أونلاين", "طلبات المتجر",
			nil, 0, 0, cur.CustomerName,
			fmt.Sprintf("طلب أونلاين #%d — الحالة الجديدة %s", id, status))
	})
	if err != nil {
		return nil, err
	}
	return o, nil
}
