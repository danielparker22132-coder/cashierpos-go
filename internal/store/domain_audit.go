// Package store — B4: audit log, audit alerts, smart monitor, smart reports.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/money"
	"cashierpos-go/internal/rbac"
)

// AuditEntry is one AuditLog row.
type AuditEntry struct {
	ID             int64
	UserID         int64
	UserName       string
	ActionType     string
	ActionCategory string
	InvoiceNumber  int64
	InvoiceID      int64
	CustomerID     int64
	CustomerName   string
	Notes          string
	LogDate        string
}

// AuditDetail is one AuditLogDetails row.
type AuditDetail struct {
	ID             int64
	LogID          int64
	ProductID      int64
	ProductName    string
	QuantityBefore money.Qty
	QuantityAfter  money.Qty
	QuantityChange money.Qty
	PriceBefore    money.Cents
	PriceAfter     money.Cents
	LineTotal      money.Cents
	Notes          string
}

// AuditAlert is one AuditAlerts row.
type AuditAlert struct {
	ID           int64
	LogID        int64
	UserName     string
	AlertType    string
	AlertMessage string
	AlertValue   money.Cents
	Threshold    money.Cents
	IsRead       bool
	IsResolved   bool
	ResolvedBy   string
	ResolvedNote string
	AlertDate    string
}

// AuditFilter narrows the audit log list.
type AuditFilter struct {
	From           string // YYYY-MM-DD
	To             string // YYYY-MM-DD
	UserName       string
	InvoiceNumber  int64 // 0 = any
	ActionType     string
	SuspiciousOnly bool
	Limit          int
}

// dangerousActionTypes are the action types the smart monitor treats as risky.
// Production audit writes Arabic ActionType values for the riskiest actions
// (e.g. "إلغاء فاتورة" for a cancel), while a few actions are written in
// English snake_case (delete_user, reset_database, restore_database) — so
// the set must match BOTH spellings (FIX-12 SW-H07: the English-only list
// matched 0 of 72 live audit rows and left the dangerous report and the
// suspicious-only filter permanently blind for cancels and friends).
var dangerousActionTypes = []string{
	"cancel_invoice", "delete_user", "reset_database", "restore_database",
	"force_close_shift", "void_invoice", "delete_invoice", "edit_price",
	// Arabic spellings actually written by production AuditTx calls.
	"إلغاء فاتورة", "حذف فاتورة", "إبطال فاتورة", "تعديل سعر",
	"إغلاق شيفت قسري", "إغلاق قسري",
}

// ListAuditLog returns audit entries matching the filter (newest first).
func (s *Store) ListAuditLog(ctx context.Context, user *auth.User, f AuditFilter) ([]AuditEntry, error) {
	if err := rbac.Require(user.Role, rbac.PermViewAudit); err != nil {
		return nil, err
	}
	if f.From != "" || f.To != "" {
		if err := validateRange(f.From, f.To); err != nil {
			return nil, err
		}
	}
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 200
	}
	var where []string
	var args []any
	if f.From != "" {
		where = append(where, "substr(LogDate,1,10)>=?")
		args = append(args, f.From)
	}
	if f.To != "" {
		where = append(where, "substr(LogDate,1,10)<=?")
		args = append(args, f.To)
	}
	if f.UserName != "" {
		where = append(where, "UserName=?")
		args = append(args, f.UserName)
	}
	if f.InvoiceNumber != 0 {
		where = append(where, "InvoiceNumber=?")
		args = append(args, f.InvoiceNumber)
	}
	if f.ActionType != "" {
		where = append(where, "ActionType=?")
		args = append(args, f.ActionType)
	}
	if f.SuspiciousOnly {
		placeholders := strings.Repeat("?,", len(dangerousActionTypes))
		placeholders = strings.TrimSuffix(placeholders, ",")
		where = append(where, "ActionType IN ("+placeholders+")")
		for _, t := range dangerousActionTypes {
			args = append(args, t)
		}
	}
	q := `SELECT LogID,COALESCE(UserID,0),UserName,ActionType,COALESCE(ActionCategory,''),
		COALESCE(InvoiceNumber,0),COALESCE(InvoiceID,0),COALESCE(CustomerID,0),
		COALESCE(CustomerName,''),COALESCE(Notes,''),LogDate FROM AuditLog`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY LogID DESC LIMIT ?"
	args = append(args, f.Limit)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.UserID, &e.UserName, &e.ActionType, &e.ActionCategory,
			&e.InvoiceNumber, &e.InvoiceID, &e.CustomerID, &e.CustomerName, &e.Notes, &e.LogDate); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// insertAuditDetailTx records one AuditLogDetails line inside tx.
// FIX-12 SW-M09: the sale path calls this for every invoice line, so the
// audit detail page shows qty/price before-after instead of always
// "لا توجد بنود تفصيلية لهذا السجل".
func insertAuditDetailTx(tx *sql.Tx, logID int64, d AuditDetail) error {
	_, err := tx.Exec(`INSERT INTO AuditLogDetails(LogID,ProductID,ProductName,QuantityBefore,QuantityAfter,QuantityChange,PriceBefore,PriceAfter,LineTotal,Notes)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, logID, d.ProductID, d.ProductName,
		int64(d.QuantityBefore), int64(d.QuantityAfter), int64(d.QuantityChange),
		int64(d.PriceBefore), int64(d.PriceAfter), int64(d.LineTotal), d.Notes)
	return err
}

// GetAuditLogDetails returns one audit entry plus its detail lines.
func (s *Store) GetAuditLogDetails(ctx context.Context, user *auth.User, logID int64) (AuditEntry, []AuditDetail, error) {
	var e AuditEntry
	if err := rbac.Require(user.Role, rbac.PermViewAudit); err != nil {
		return e, nil, err
	}
	err := s.DB.QueryRow(`SELECT LogID,COALESCE(UserID,0),UserName,ActionType,COALESCE(ActionCategory,''),
		COALESCE(InvoiceNumber,0),COALESCE(InvoiceID,0),COALESCE(CustomerID,0),
		COALESCE(CustomerName,''),COALESCE(Notes,''),LogDate FROM AuditLog WHERE LogID=?`, logID).
		Scan(&e.ID, &e.UserID, &e.UserName, &e.ActionType, &e.ActionCategory,
			&e.InvoiceNumber, &e.InvoiceID, &e.CustomerID, &e.CustomerName, &e.Notes, &e.LogDate)
	if err == sql.ErrNoRows {
		return e, nil, errors.New("سجل التدقيق غير موجود")
	}
	if err != nil {
		return e, nil, err
	}
	rows, err := s.DB.Query(`SELECT DetailID,LogID,COALESCE(ProductID,0),COALESCE(ProductName,''),
		COALESCE(QuantityBefore,0),COALESCE(QuantityAfter,0),COALESCE(QuantityChange,0),
		COALESCE(PriceBefore,0),COALESCE(PriceAfter,0),COALESCE(LineTotal,0),COALESCE(Notes,'')
		FROM AuditLogDetails WHERE LogID=? ORDER BY DetailID`, logID)
	if err != nil {
		return e, nil, err
	}
	defer rows.Close()
	var details []AuditDetail
	for rows.Next() {
		var d AuditDetail
		var qb, qa, qc, pb, pa, lt int64
		if err := rows.Scan(&d.ID, &d.LogID, &d.ProductID, &d.ProductName, &qb, &qa, &qc, &pb, &pa, &lt, &d.Notes); err != nil {
			return e, nil, err
		}
		d.QuantityBefore, d.QuantityAfter, d.QuantityChange = money.Qty(qb), money.Qty(qa), money.Qty(qc)
		d.PriceBefore, d.PriceAfter, d.LineTotal = money.Cents(pb), money.Cents(pa), money.Cents(lt)
		details = append(details, d)
	}
	return e, details, rows.Err()
}

// ListAuditAlerts returns audit alerts (newest first). unreadOnly narrows to unread.
func (s *Store) ListAuditAlerts(ctx context.Context, user *auth.User, unreadOnly bool, limit int) ([]AuditAlert, error) {
	if err := rbac.Require(user.Role, rbac.PermViewAudit); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT AlertID,COALESCE(LogID,0),UserName,AlertType,AlertMessage,
		COALESCE(AlertValue,0),COALESCE(Threshold,0),IsRead,IsResolved,
		COALESCE(ResolvedBy,''),COALESCE(ResolvedNote,''),AlertDate FROM AuditAlerts`
	if unreadOnly {
		q += " WHERE IsRead=0"
	}
	q += " ORDER BY AlertID DESC LIMIT ?"
	rows, err := s.DB.Query(q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditAlert
	for rows.Next() {
		var a AuditAlert
		var rd, rs int
		var av, th int64
		if err := rows.Scan(&a.ID, &a.LogID, &a.UserName, &a.AlertType, &a.AlertMessage, &av, &th, &rd, &rs, &a.ResolvedBy, &a.ResolvedNote, &a.AlertDate); err != nil {
			return nil, err
		}
		a.AlertValue, a.Threshold = money.Cents(av), money.Cents(th)
		a.IsRead, a.IsResolved = rd == 1, rs == 1
		out = append(out, a)
	}
	return out, rows.Err()
}

// UnreadAuditAlertsCount returns the unread audit-alert count.
// Admin / Manager only (rbac maps manager to admin; view_audit is admin-only).
// FIX-3 L13: one definition everywhere — unread AND unresolved
// (IsRead=0 AND IsResolved=0), matching the hub's former query;
// render() is the single source for the bell.
func (s *Store) UnreadAuditAlertsCount(ctx context.Context, user *auth.User) (int, error) {
	if err := rbac.Require(user.Role, rbac.PermViewAudit); err != nil {
		return 0, err
	}
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM AuditAlerts WHERE IsRead=0 AND IsResolved=0`).Scan(&n)
	return n, err
}

// MarkAuditAlertRead marks one alert read (admin).
func (s *Store) MarkAuditAlertRead(ctx context.Context, user *auth.User, alertID int64) error {
	if err := rbac.Require(user.Role, rbac.PermViewAudit); err != nil {
		return err
	}
	res, err := s.DB.Exec(`UPDATE AuditAlerts SET IsRead=1 WHERE AlertID=?`, alertID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("التنبيه غير موجود")
	}
	return nil
}

// ResolveAuditAlert marks one alert resolved with a note (admin).
func (s *Store) ResolveAuditAlert(ctx context.Context, user *auth.User, alertID int64, note string) error {
	if err := rbac.Require(user.Role, rbac.PermViewAudit); err != nil {
		return err
	}
	res, err := s.DB.Exec(`UPDATE AuditAlerts SET IsResolved=1, IsRead=1, ResolvedBy=?, ResolvedNote=? WHERE AlertID=?`,
		user.UserName, strings.TrimSpace(note), alertID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("التنبيه غير موجود")
	}
	return nil
}

// MonitorThresholds are the smart-monitor limits from settings (0 = disabled).
type MonitorThresholds struct {
	CancelAmount money.Cents // alert when a canceled invoice net >= this
	DiscountPct  int         // alert when an invoice discount % >= this
	ReturnAmount money.Cents // alert when a return refund >= this
}

func parseCentsSetting(s *Store, key string) money.Cents {
	v, _ := strconv.ParseInt(strings.TrimSpace(s.GetSetting(key)), 10, 64)
	if v < 0 {
		return 0
	}
	return money.Cents(v)
}

// GetMonitorThresholds reads the three smart-monitor thresholds.
func (s *Store) GetMonitorThresholds(ctx context.Context, user *auth.User) (MonitorThresholds, error) {
	var t MonitorThresholds
	if err := rbac.Require(user.Role, rbac.PermManageMonitor); err != nil {
		return t, err
	}
	t.CancelAmount = parseCentsSetting(s, "alert_threshold_cancel_amount")
	t.ReturnAmount = parseCentsSetting(s, "alert_threshold_return_amount")
	if v, err := strconv.Atoi(strings.TrimSpace(s.GetSetting("alert_threshold_discount_pct"))); err == nil && v > 0 {
		t.DiscountPct = v
	}
	return t, nil
}

// SaveMonitorThresholds persists the three thresholds (all >= 0; 0 = disabled).
func (s *Store) SaveMonitorThresholds(ctx context.Context, user *auth.User, t MonitorThresholds) error {
	if err := rbac.Require(user.Role, rbac.PermManageMonitor); err != nil {
		return err
	}
	if t.CancelAmount < 0 || t.ReturnAmount < 0 || t.DiscountPct < 0 || t.DiscountPct > 100 {
		return errors.New("العتبات لا يمكن أن تكون سالبة ونسبة الخصم بين 0 و 100")
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		if err := SetSettingTx(tx, "alert_threshold_cancel_amount", strconv.FormatInt(int64(t.CancelAmount), 10)); err != nil {
			return err
		}
		if err := SetSettingTx(tx, "alert_threshold_return_amount", strconv.FormatInt(int64(t.ReturnAmount), 10)); err != nil {
			return err
		}
		if err := SetSettingTx(tx, "alert_threshold_discount_pct", strconv.Itoa(t.DiscountPct)); err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "save_monitor_settings", "settings", nil, 0, 0, "",
			"حفظ عتبات المراقبة الذكية")
	})
}

// createAuditAlertTx inserts an alert unless an identical unresolved alert
// (same type + same message) already exists. Reports whether it inserted.
func createAuditAlertTx(tx *sql.Tx, logID int64, userName, alertType, message string, value, threshold money.Cents) (bool, error) {
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM AuditAlerts
		WHERE AlertType=? AND AlertMessage=? AND IsResolved=0`, alertType, message).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	_, err := tx.Exec(`INSERT INTO AuditAlerts(LogID,UserName,AlertType,AlertMessage,AlertValue,Threshold,IsRead,IsResolved,AlertDate)
		VALUES(?,?,?,?,?,?,0,0,?)`, nullableInt(logID), userName, alertType, message, int64(value), int64(threshold), Now())
	if err != nil {
		return false, err
	}
	return true, nil
}

// RunSmartMonitor evaluates recent activity against the thresholds and
// creates audit alerts for breaches. Returns the number of new alerts.
func (s *Store) RunSmartMonitor(ctx context.Context, user *auth.User) (int, error) {
	if err := rbac.Require(user.Role, rbac.PermManageMonitor); err != nil {
		return 0, err
	}
	th := MonitorThresholds{
		CancelAmount: parseCentsSetting(s, "alert_threshold_cancel_amount"),
		ReturnAmount: parseCentsSetting(s, "alert_threshold_return_amount"),
	}
	if v, err := strconv.Atoi(strings.TrimSpace(s.GetSetting("alert_threshold_discount_pct"))); err == nil {
		th.DiscountPct = v
	}
	created := 0
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		if th.CancelAmount > 0 {
			// Collect first: writing alerts while the scan rows are open
			// would deadlock on the single connection (MaxOpenConns(1)).
			type cancelHit struct {
				num, net int64
				uname    string
			}
			var hits []cancelHit
			rows, err := tx.Query(`SELECT InvoiceNumber,COALESCE(UserName,''),NetAmount FROM Invoices
				WHERE IsCanceled=1 AND NetAmount>=?`, int64(th.CancelAmount))
			if err != nil {
				return err
			}
			for rows.Next() {
				var h cancelHit
				if err := rows.Scan(&h.num, &h.uname, &h.net); err != nil {
					rows.Close()
					return err
				}
				hits = append(hits, h)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			for _, h := range hits {
				var logID int64
				// FIX-12 SW-H07: cancels are written as Arabic "إلغاء فاتورة"
				// (domain_sales.go), so match that spelling too — the old
				// English-only query never matched a live row.
				_ = tx.QueryRow(`SELECT LogID FROM AuditLog WHERE InvoiceNumber=? AND ActionType IN ('cancel_invoice','إلغاء فاتورة') ORDER BY LogID DESC LIMIT 1`, h.num).Scan(&logID)
				msg := fmt.Sprintf("إلغاء فاتورة رقم %d بمبلغ %s يتجاوز العتبة", h.num, money.FormatEGP(money.Cents(h.net)))
				isNew, err := createAuditAlertTx(tx, logID, h.uname, "cancel_amount", msg, money.Cents(h.net), th.CancelAmount)
				if err != nil {
					return err
				}
				if isNew {
					created++
				}
			}
		}
		if th.ReturnAmount > 0 {
			type returnHit struct {
				rid, refund int64
				uname       string
			}
			var hits []returnHit
			rows, err := tx.Query(`SELECT ReturnID,COALESCE(UserName,''),TotalRefund FROM Returns
				WHERE TotalRefund>=?`, int64(th.ReturnAmount))
			if err != nil {
				return err
			}
			for rows.Next() {
				var h returnHit
				if err := rows.Scan(&h.rid, &h.uname, &h.refund); err != nil {
					rows.Close()
					return err
				}
				hits = append(hits, h)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			for _, h := range hits {
				msg := fmt.Sprintf("مرتجع رقم %d بمبلغ %s يتجاوز العتبة", h.rid, money.FormatEGP(money.Cents(h.refund)))
				isNew, err := createAuditAlertTx(tx, 0, h.uname, "return_amount", msg, money.Cents(h.refund), th.ReturnAmount)
				if err != nil {
					return err
				}
				if isNew {
					created++
				}
			}
		}
		if th.DiscountPct > 0 {
			type discHit struct {
				num, total, disc int64
				uname            string
			}
			var hits []discHit
			rows, err := tx.Query(`SELECT InvoiceNumber,COALESCE(UserName,''),TotalAmount,Discount FROM Invoices
				WHERE IsCanceled=0 AND TotalAmount>0 AND Discount>0`)
			if err != nil {
				return err
			}
			for rows.Next() {
				var h discHit
				if err := rows.Scan(&h.num, &h.uname, &h.total, &h.disc); err != nil {
					rows.Close()
					return err
				}
				hits = append(hits, h)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			for _, h := range hits {
				if h.total > 0 && h.disc*100/h.total >= int64(th.DiscountPct) {
					msg := fmt.Sprintf("خصم %d%% على فاتورة رقم %d يتجاوز العتبة", h.disc*100/h.total, h.num)
					isNew, err := createAuditAlertTx(tx, 0, h.uname, "discount_pct", msg, money.Cents(h.disc), money.Cents(th.DiscountPct))
					if err != nil {
						return err
					}
					if isNew {
						created++
					}
				}
			}
		}
		return AuditTx(tx, user.ID, user.UserName, "run_smart_monitor", "audit", nil, 0, 0, "",
			fmt.Sprintf("تشغيل المراقبة الذكية — تنبيهات جديدة: %d", created))
	})
	if err != nil {
		return 0, err
	}
	return created, nil
}

// SmartReportRow is one row of a smart audit report.
type SmartReportRow struct {
	Label  string
	Count  int64
	Amount money.Cents
}

// SmartReport kinds: cancels, returns, discounts, dangerous, debts, hourly,
// suspicious_users, cashier_compare.
func (s *Store) SmartReport(ctx context.Context, user *auth.User, kind, from, to string) ([]SmartReportRow, error) {
	if err := rbac.Require(user.Role, rbac.PermViewAudit); err != nil {
		return nil, err
	}
	if err := validateRange(from, to); err != nil {
		return nil, err
	}
	var rows *sql.Rows
	var err error
	switch kind {
	case "cancels":
		rows, err = s.DB.Query(`SELECT COALESCE(UserName,'') u, COUNT(*), COALESCE(SUM(NetAmount),0)
			FROM Invoices WHERE IsCanceled=1 AND substr(COALESCE(CanceledAt,InvoiceDate),1,10) BETWEEN ? AND ?
			GROUP BY u ORDER BY COUNT(*) DESC`, from, to)
	case "returns":
		rows, err = s.DB.Query(`SELECT COALESCE(UserName,'') u, COUNT(*), COALESCE(SUM(TotalRefund),0)
			FROM Returns WHERE substr(ReturnDate,1,10) BETWEEN ? AND ? GROUP BY u ORDER BY COUNT(*) DESC`, from, to)
	case "discounts":
		rows, err = s.DB.Query(`SELECT COALESCE(UserName,'') u, COUNT(*), COALESCE(SUM(Discount),0)
			FROM Invoices WHERE IsCanceled=0 AND Discount>0 AND substr(InvoiceDate,1,10) BETWEEN ? AND ?
			GROUP BY u ORDER BY SUM(Discount) DESC`, from, to)
	case "dangerous":
		placeholders := strings.Repeat("?,", len(dangerousActionTypes))
		placeholders = strings.TrimSuffix(placeholders, ",")
		args := []any{from, to}
		for _, t := range dangerousActionTypes {
			args = append(args, t)
		}
		rows, err = s.DB.Query(`SELECT ActionType, COUNT(*), 0 FROM AuditLog
			WHERE substr(LogDate,1,10) BETWEEN ? AND ? AND ActionType IN (`+placeholders+`)
			GROUP BY ActionType ORDER BY COUNT(*) DESC`, args...)
	case "debts":
		rows, err = s.DB.Query(`SELECT CustomerName, COUNT(*), COALESCE(SUM(RemainingAmount),0)
			FROM Invoices i JOIN Customers c ON c.CustomerID=i.CustomerID
			WHERE i.IsCanceled=0 AND i.RemainingAmount>0 AND substr(i.InvoiceDate,1,10) BETWEEN ? AND ?
			GROUP BY CustomerName ORDER BY SUM(RemainingAmount) DESC`, from, to)
	case "hourly":
		rows, err = s.DB.Query(`SELECT substr(InvoiceDate,12,2) h, COUNT(*), COALESCE(SUM(NetAmount),0)
			FROM Invoices WHERE IsCanceled=0 AND substr(InvoiceDate,1,10) BETWEEN ? AND ?
			GROUP BY h ORDER BY h`, from, to)
	case "suspicious_users":
		// FIX-12 SW-L07: exclude users with zero suspicious actions
		// (zero cancels+returns AND zero discount in range) — the old
		// query listed every user, including 0 / 0.00 rows. The WHERE
		// repeats the SELECT-list subqueries; its 6 params follow the
		// SELECT list's 6 in textual order.
		rows, err = s.DB.Query(`SELECT u.UserName,
			(SELECT COUNT(*) FROM Invoices WHERE IsCanceled=1 AND UserName=u.UserName AND substr(COALESCE(CanceledAt,InvoiceDate),1,10) BETWEEN ? AND ?)
			+ (SELECT COUNT(*) FROM Returns WHERE UserName=u.UserName AND substr(ReturnDate,1,10) BETWEEN ? AND ?),
			(SELECT COALESCE(SUM(Discount),0) FROM Invoices WHERE IsCanceled=0 AND UserName=u.UserName AND substr(InvoiceDate,1,10) BETWEEN ? AND ?)
			FROM Users u
			WHERE ((SELECT COUNT(*) FROM Invoices WHERE IsCanceled=1 AND UserName=u.UserName AND substr(COALESCE(CanceledAt,InvoiceDate),1,10) BETWEEN ? AND ?)
			+ (SELECT COUNT(*) FROM Returns WHERE UserName=u.UserName AND substr(ReturnDate,1,10) BETWEEN ? AND ?)) > 0
			OR (SELECT COALESCE(SUM(Discount),0) FROM Invoices WHERE IsCanceled=0 AND UserName=u.UserName AND substr(InvoiceDate,1,10) BETWEEN ? AND ?) > 0
			ORDER BY 2 DESC, 3 DESC LIMIT 20`,
			from, to, from, to, from, to, from, to, from, to, from, to)
	case "cashier_compare":
		rows, err = s.DB.Query(`SELECT COALESCE(UserName,'') u, COUNT(*), COALESCE(SUM(NetAmount),0)
			FROM Invoices WHERE IsCanceled=0 AND substr(InvoiceDate,1,10) BETWEEN ? AND ?
			GROUP BY u ORDER BY SUM(NetAmount) DESC`, from, to)
	default:
		return nil, fmt.Errorf("نوع التقرير الذكي غير معروف: %q", kind)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SmartReportRow
	for rows.Next() {
		var r SmartReportRow
		var amt int64
		if err := rows.Scan(&r.Label, &r.Count, &amt); err != nil {
			return nil, err
		}
		r.Amount = money.Cents(amt)
		out = append(out, r)
	}
	return out, rows.Err()
}
