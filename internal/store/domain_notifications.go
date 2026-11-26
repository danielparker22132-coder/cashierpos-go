// Package store — B4: notifications domain.
//
// Internal (polling-only) notifications. No FCM / no cloud push.
// One Notifications row is the admin "definition" (UserName=”); delivery
// creates one row per recipient (UserName=recipient) so IsRead is per user.
// Dedupe is enforced by NotificationSendLog UNIQUE(UserName, EventKey).
// A trigger that reaches nobody leaves no definition row behind: a 'sent'
// row with zero recipients would read as delivered in the admin manage
// view (PR-L2) — see dropEmptyDefinitionTx.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/rbac"
)

// Notification types.
const (
	NotifPriceDrop  = "price_drop"
	NotifOffer      = "offer"
	NotifCustom     = "custom"
	NotifScheduled  = "scheduled"
	NotifLowStock   = "low_stock"
	NotifAuditAlert = "audit_alert"
)

// NotifTypeNames are the Arabic labels for notification types.
var NotifTypeNames = map[string]string{
	NotifPriceDrop:  "انخفاض سعر",
	NotifOffer:      "عرض",
	NotifCustom:     "مخصص",
	NotifScheduled:  "مجدول",
	NotifLowStock:   "مخزون منخفض",
	NotifAuditAlert: "تنبيه تدقيق",
}

// ValidNotifTypes lists every supported notification type.
var ValidNotifTypes = []string{NotifPriceDrop, NotifOffer, NotifCustom, NotifScheduled, NotifLowStock, NotifAuditAlert}

// Notification is one Notifications row.
type Notification struct {
	ID              int64
	Type            string
	Title           string
	Body            string
	LinkedProductID int64
	Audience        string // "all" | "user:<name>" | "product:<id>"
	UserName        string // "" = admin definition row; otherwise recipient
	IsRead          bool
	CreatedAt       string
	SendAt          string
	Status          string // sent | scheduled | disabled
}

// NotificationInput is the admin form for creating/editing a notification.
type NotificationInput struct {
	Type            string
	Title           string
	Body            string
	LinkedProductID int64
	Audience        string
	SendAt          string // "" = send now; otherwise "2006-01-02 15:04:05"
	EventKey        string // "" = no dedupe (custom one-off)
}

// NotificationSetting is one per-user per-type toggle.
type NotificationSetting struct {
	UserName string
	Type     string
	Enabled  bool
}

// SendLogEntry is one NotificationSendLog row.
type SendLogEntry struct {
	ID             int64
	NotificationID int64
	UserName       string
	EventKey       string
	SentAt         string
}

// validRole reports whether role is one of the five known roles.
func validRole(role string) bool {
	switch role {
	case rbac.RoleAdmin, rbac.RoleManager, rbac.RoleCashier, rbac.RoleSupervisor, rbac.RoleWarehouse:
		return true
	}
	return false
}

// validateRange checks a YYYY-MM-DD range (from <= to).
func validateRange(from, to string) error {
	if from == "" || to == "" {
		return errors.New("تاريخ البداية والنهاية مطلوبان")
	}
	f, err1 := time.Parse("2006-01-02", from)
	t, err2 := time.Parse("2006-01-02", to)
	if err1 != nil || err2 != nil {
		return errors.New("صيغة التاريخ غير صحيحة (YYYY-MM-DD)")
	}
	if f.After(t) {
		return errors.New("تاريخ البداية يجب أن يكون قبل تاريخ النهاية")
	}
	return nil
}

// notifTypeLabel returns the Arabic label for a type.
func notifTypeLabel(t string) string {
	if l, ok := NotifTypeNames[t]; ok {
		return l
	}
	return t
}

// isNotifTypeValid reports whether t is a known notification type.
func isNotifTypeValid(t string) bool {
	for _, v := range ValidNotifTypes {
		if v == t {
			return true
		}
	}
	return false
}

// audienceRecipients resolves an audience string to recipient usernames
// (active users only). Audience forms:
//   - "all": every active user.
//   - "user:<name>": that one active user.
//   - "product:<id>": staff users who previously sold that product.
//     The recipient is the staff seller (Invoices.UserName, joined to
//     active Users); the buyer on the invoice is irrelevant — a sale to
//     the seeded cash customer (Customers ID 1) still has a staff seller,
//     so there is no CustomerID filter here. (PR-H2: the old
//     CustomerID<>1 filter wrongly excluded every cash-customer sale,
//     so price-drop and offer alerts never fired for cash sellers.)
func audienceRecipients(tx *sql.Tx, audience string) ([]string, error) {
	audience = strings.TrimSpace(audience)
	if audience == "" || audience == "all" {
		return activeUserNames(tx)
	}
	if strings.HasPrefix(audience, "user:") {
		name := strings.TrimSpace(strings.TrimPrefix(audience, "user:"))
		if name == "" {
			return nil, errors.New("اسم المستخدم المستهدف مطلوب")
		}
		var n int
		err := tx.QueryRow(`SELECT COUNT(*) FROM Users WHERE UserName=? AND IsActive=1`, name).Scan(&n)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, fmt.Errorf("المستخدم %q غير موجود أو غير مفعّل", name)
		}
		return []string{name}, nil
	}
	if strings.HasPrefix(audience, "product:") {
		var pid int64
		if _, err := fmt.Sscanf(strings.TrimPrefix(audience, "product:"), "%d", &pid); err != nil || pid <= 0 {
			return nil, errors.New("معرّف المنتج المستهدف غير صحيح")
		}
		rows, err := tx.Query(`SELECT DISTINCT i.UserName FROM Invoices i
			JOIN InvoiceDetails d ON d.InvoiceID=i.InvoiceID
			JOIN Users u ON u.UserName=i.UserName AND u.IsActive=1
			WHERE d.ProductID=? AND i.IsCanceled=0`, pid)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return nil, err
			}
			out = append(out, name)
		}
		return out, rows.Err()
	}
	return nil, fmt.Errorf("استهداف غير معروف: %q", audience)
}

// dropEmptyDefinitionTx deletes a definition row that reached nobody.
// Call inside the same Tx right after delivery when sent == 0: a 'sent'
// row with zero recipients would be listed by ListNotificationsAdmin as
// a delivered notification (PR-L2). Safe for dedupe: dedupe lives in
// NotificationSendLog UNIQUE(UserName, EventKey), not in the definition
// row — and sent == 0 implies zero new log rows were written for this
// definition (every counted send writes exactly one log row first), so
// nothing references it, while pre-existing log rows (same event key,
// older definition) keep blocking re-fires. Conversely, when sent == 0
// because the audience was empty or every recipient had the type
// disabled, no log row exists, so a later trigger with the same event
// key correctly fires once an audience appears or the setting is back on.
func dropEmptyDefinitionTx(tx *sql.Tx, defID int64) error {
	_, err := tx.Exec(`DELETE FROM Notifications WHERE NotificationID=? AND UserName=''`, defID)
	return err
}

func activeUserNames(tx *sql.Tx) ([]string, error) {
	rows, err := tx.Query(`SELECT UserName FROM Users WHERE IsActive=1 ORDER BY UserName`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// userNotifEnabled reports whether user has this type enabled.
// No row = enabled (default on).
func userNotifEnabled(tx *sql.Tx, userName, notifType string) (bool, error) {
	var enabled int
	err := tx.QueryRow(`SELECT Enabled FROM NotificationSettings WHERE UserName=? AND NotifType=?`,
		userName, notifType).Scan(&enabled)
	if err == sql.ErrNoRows {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return enabled == 1, nil
}

// deliverNotificationTx delivers an already-stored definition row (id) to its
// audience: one Notifications row per recipient, one NotificationSendLog row
// per (recipient, eventKey). Recipients with the type disabled in their
// settings are skipped; an existing send-log row for (user, eventKey)
// skips that user (dedupe). Returns the number of recipients actually sent.
func deliverNotificationTx(tx *sql.Tx, defID int64, notifType, title, body string, productID int64, audience, eventKey string) (int, error) {
	recipients, err := audienceRecipients(tx, audience)
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, name := range recipients {
		enabled, err := userNotifEnabled(tx, name, notifType)
		if err != nil {
			return sent, err
		}
		if !enabled {
			continue
		}
		if eventKey != "" {
			res, err := tx.Exec(`INSERT OR IGNORE INTO NotificationSendLog
				(NotificationID,UserName,EventKey,SentAt) VALUES(?,?,?,?)`,
				defID, name, eventKey, Now())
			if err != nil {
				return sent, err
			}
			n, _ := res.RowsAffected()
			if n == 0 {
				continue // duplicate (user + event) — already sent
			}
		} else {
			if _, err := tx.Exec(`INSERT INTO NotificationSendLog
				(NotificationID,UserName,EventKey,SentAt) VALUES(?,?,?,?)`,
				defID, name, "", Now()); err != nil {
				return sent, err
			}
		}
		if _, err := tx.Exec(`INSERT INTO Notifications
			(Type,Title,Body,LinkedProductID,Audience,UserName,IsRead,CreatedAt,SendAt,Status)
			VALUES(?,?,?,?,?,?,0,?,?, 'sent')`,
			notifType, title, body, nullableInt(productID), audience, name, Now(), Now()); err != nil {
			return sent, err
		}
		sent++
	}
	return sent, nil
}

// CreateNotification creates a notification definition (admin) and, unless
// scheduled for the future, delivers it immediately. Returns the definition id.
// When immediate delivery reaches nobody, no definition row is kept
// (PR-L2) and 0 is returned.
func (s *Store) CreateNotification(ctx context.Context, user *auth.User, in NotificationInput) (int64, error) {
	if err := rbac.Require(user.Role, rbac.PermManageNotifications); err != nil {
		return 0, err
	}
	if !isNotifTypeValid(in.Type) {
		return 0, errors.New("نوع التنبيه غير معروف")
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return 0, errors.New("عنوان التنبيه مطلوب")
	}
	if in.Audience == "" {
		in.Audience = "all"
	}
	status := "sent"
	sendAt := in.SendAt
	if sendAt != "" {
		if _, err := time.Parse("2006-01-02 15:04:05", sendAt); err != nil {
			return 0, errors.New("صيغة وقت الإرسال غير صحيحة (YYYY-MM-DD HH:MM:SS)")
		}
		if sendAt > Now() {
			status = "scheduled"
		} else {
			sendAt = Now()
		}
	} else {
		sendAt = Now()
	}
	var defID int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO Notifications
			(Type,Title,Body,LinkedProductID,Audience,UserName,IsRead,CreatedAt,SendAt,Status)
			VALUES(?,?,?,?,?,'',0,?,?,?)`,
			in.Type, in.Title, in.Body, nullableInt(in.LinkedProductID), in.Audience, Now(), sendAt, status)
		if err != nil {
			return err
		}
		defID, err = res.LastInsertId()
		if err != nil {
			return err
		}
		if status == "sent" {
			eventKey := in.EventKey
			if eventKey == "" && in.Type != NotifCustom {
				eventKey = fmt.Sprintf("%s:%d", in.Type, defID)
			}
			sent, err := deliverNotificationTx(tx, defID, in.Type, in.Title, in.Body, in.LinkedProductID, in.Audience, eventKey)
			if err != nil {
				return err
			}
			if sent == 0 {
				// PR-L2: reached nobody — leave no 'sent' definition row
				// behind (it would read as delivered). Dedupe is
				// unaffected (see dropEmptyDefinitionTx).
				if err := dropEmptyDefinitionTx(tx, defID); err != nil {
					return err
				}
				defID = 0
			}
		}
		return AuditTx(tx, user.ID, user.UserName, "create_notification", "notifications", nil, 0, 0, "",
			fmt.Sprintf("إنشاء تنبيه %s: %s", notifTypeLabel(in.Type), in.Title))
	})
	if err != nil {
		return 0, err
	}
	return defID, nil
}

// UpdateNotification edits a definition row (admin). Scheduled rows keep
// their schedule; sent rows are not re-delivered.
func (s *Store) UpdateNotification(ctx context.Context, user *auth.User, id int64, in NotificationInput) error {
	if err := rbac.Require(user.Role, rbac.PermManageNotifications); err != nil {
		return err
	}
	if !isNotifTypeValid(in.Type) {
		return errors.New("نوع التنبيه غير معروف")
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return errors.New("عنوان التنبيه مطلوب")
	}
	if in.Audience == "" {
		in.Audience = "all"
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		var status string
		if err := tx.QueryRow(`SELECT Status FROM Notifications WHERE NotificationID=? AND UserName=''`, id).Scan(&status); err == sql.ErrNoRows {
			return errors.New("التنبيه غير موجود")
		} else if err != nil {
			return err
		}
		sendAt := in.SendAt
		if sendAt == "" {
			sendAt = Now()
		} else if _, err := time.Parse("2006-01-02 15:04:05", sendAt); err != nil {
			return errors.New("صيغة وقت الإرسال غير صحيحة (YYYY-MM-DD HH:MM:SS)")
		}
		if status == "scheduled" && sendAt > Now() {
			// stays scheduled
		} else if status == "scheduled" {
			status = "sent"
		}
		_, err := tx.Exec(`UPDATE Notifications SET Type=?,Title=?,Body=?,LinkedProductID=?,
			Audience=?,SendAt=?,Status=? WHERE NotificationID=? AND UserName=''`,
			in.Type, in.Title, in.Body, nullableInt(in.LinkedProductID), in.Audience, sendAt, status, id)
		if err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "update_notification", "notifications", nil, 0, 0, "",
			fmt.Sprintf("تعديل تنبيه: %s", in.Title))
	})
}

// SetNotificationStatus enables/disables a definition row (admin).
// Disabling stops future scheduled delivery; already-sent rows are untouched.
func (s *Store) SetNotificationStatus(ctx context.Context, user *auth.User, id int64, enabled bool) error {
	if err := rbac.Require(user.Role, rbac.PermManageNotifications); err != nil {
		return err
	}
	status := "sent"
	if !enabled {
		status = "disabled"
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE Notifications SET Status=? WHERE NotificationID=? AND UserName='' AND Status IN ('sent','scheduled','disabled')`,
			status, id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return errors.New("التنبيه غير موجود")
		}
		return AuditTx(tx, user.ID, user.UserName, "toggle_notification", "notifications", nil, 0, 0, "",
			fmt.Sprintf("تفعيل/تعطيل تنبيه %d: %v", id, enabled))
	})
}

// PublishDueNotifications delivers scheduled definitions whose SendAt has
// arrived. Safe to call on read paths and from a future ticker; idempotent
// per (user, eventKey). Returns the number of recipients sent.
func (s *Store) PublishDueNotifications(ctx context.Context) (int, error) {
	var due []Notification
	rows, err := s.DB.Query(`SELECT NotificationID,Type,Title,Body,
		COALESCE(NULLIF(LinkedProductID,''),0),Audience,SendAt FROM Notifications
		WHERE UserName='' AND Status='scheduled' AND SendAt<=?`, Now())
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var n Notification
		if err := rows.Scan(&n.ID, &n.Type, &n.Title, &n.Body, &n.LinkedProductID, &n.Audience, &n.SendAt); err != nil {
			rows.Close()
			return 0, err
		}
		due = append(due, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	total := 0
	for _, n := range due {
		err := s.Tx(ctx, func(tx *sql.Tx) error {
			sent, err := deliverNotificationTx(tx, n.ID, n.Type, n.Title, n.Body, n.LinkedProductID, n.Audience,
				fmt.Sprintf("scheduled:%d", n.ID))
			if err != nil {
				return err
			}
			total += sent
			if sent == 0 {
				// PR-L2: reached nobody — do not flip it to 'sent'
				// (it would read as delivered). Leave it scheduled so
				// a later publish pass retries once an audience exists
				// or a recipient re-enables the type.
				return nil
			}
			_, err = tx.Exec(`UPDATE Notifications SET Status='sent' WHERE NotificationID=? AND UserName=''`, n.ID)
			return err
		})
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// ListNotificationsAdmin lists definition rows (admin manage view).
func (s *Store) ListNotificationsAdmin(ctx context.Context, user *auth.User) ([]Notification, error) {
	if err := rbac.Require(user.Role, rbac.PermManageNotifications); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(`SELECT NotificationID,Type,Title,Body,
		COALESCE(NULLIF(LinkedProductID,''),0),Audience,CreatedAt,COALESCE(SendAt,''),Status
		FROM Notifications WHERE UserName='' ORDER BY NotificationID DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		var n Notification
		if err := rows.Scan(&n.ID, &n.Type, &n.Title, &n.Body, &n.LinkedProductID, &n.Audience, &n.CreatedAt, &n.SendAt, &n.Status); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// GetNotification returns one definition row (admin).
func (s *Store) GetNotification(ctx context.Context, user *auth.User, id int64) (Notification, error) {
	var n Notification
	if err := rbac.Require(user.Role, rbac.PermManageNotifications); err != nil {
		return n, err
	}
	err := s.DB.QueryRow(`SELECT NotificationID,Type,Title,Body,
		COALESCE(NULLIF(LinkedProductID,''),0),Audience,CreatedAt,COALESCE(SendAt,''),Status
		FROM Notifications WHERE NotificationID=? AND UserName=''`, id).
		Scan(&n.ID, &n.Type, &n.Title, &n.Body, &n.LinkedProductID, &n.Audience, &n.CreatedAt, &n.SendAt, &n.Status)
	if err == sql.ErrNoRows {
		return n, errors.New("التنبيه غير موجود")
	}
	return n, err
}

// ListInbox lists the recipient's delivered notifications (newest first).
// Publishing of due scheduled notifications runs first (best effort).
func (s *Store) ListInbox(ctx context.Context, user *auth.User, limit int) ([]Notification, error) {
	_, _ = s.PublishDueNotifications(ctx)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.DB.Query(`SELECT NotificationID,Type,Title,Body,
		COALESCE(NULLIF(LinkedProductID,''),0),Audience,IsRead,CreatedAt,COALESCE(SendAt,''),Status
		FROM Notifications WHERE UserName=? AND Status='sent' ORDER BY NotificationID DESC LIMIT ?`,
		user.UserName, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		var n Notification
		var isRead int
		if err := rows.Scan(&n.ID, &n.Type, &n.Title, &n.Body, &n.LinkedProductID, &n.Audience, &isRead, &n.CreatedAt, &n.SendAt, &n.Status); err != nil {
			return nil, err
		}
		n.UserName = user.UserName
		n.IsRead = isRead == 1
		out = append(out, n)
	}
	return out, rows.Err()
}

// UnreadNotificationCount returns the recipient's unread count (bell badge).
func (s *Store) UnreadNotificationCount(ctx context.Context, user *auth.User) (int, error) {
	_, _ = s.PublishDueNotifications(ctx)
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM Notifications
		WHERE UserName=? AND Status='sent' AND IsRead=0`, user.UserName).Scan(&n)
	return n, err
}

// MarkNotificationRead marks one inbox row read (only its owner).
func (s *Store) MarkNotificationRead(ctx context.Context, user *auth.User, id int64) error {
	res, err := s.DB.Exec(`UPDATE Notifications SET IsRead=1
		WHERE NotificationID=? AND UserName=? AND Status='sent'`, id, user.UserName)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("التنبيه غير موجود")
	}
	return nil
}

// MarkAllNotificationsRead marks the whole inbox read.
func (s *Store) MarkAllNotificationsRead(ctx context.Context, user *auth.User) error {
	_, err := s.DB.Exec(`UPDATE Notifications SET IsRead=1 WHERE UserName=? AND Status='sent'`, user.UserName)
	return err
}

// GetUserNotificationSettings returns all per-type toggles for a user
// (missing rows default to enabled).
func (s *Store) GetUserNotificationSettings(ctx context.Context, user *auth.User) ([]NotificationSetting, error) {
	rows, err := s.DB.Query(`SELECT NotifType,Enabled FROM NotificationSettings WHERE UserName=?`, user.UserName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]bool{}
	for rows.Next() {
		var t string
		var e int
		if err := rows.Scan(&t, &e); err != nil {
			return nil, err
		}
		m[t] = e == 1
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]NotificationSetting, 0, len(ValidNotifTypes))
	for _, t := range ValidNotifTypes {
		enabled, ok := m[t]
		if !ok {
			enabled = true
		}
		out = append(out, NotificationSetting{UserName: user.UserName, Type: t, Enabled: enabled})
	}
	return out, nil
}

// SetUserNotificationSetting sets one per-user per-type toggle.
// A user edits their own settings; an admin may edit anyone's.
func (s *Store) SetUserNotificationSetting(ctx context.Context, user *auth.User, targetUser, notifType string, enabled bool) error {
	if !isNotifTypeValid(notifType) {
		return errors.New("نوع التنبيه غير معروف")
	}
	targetUser = strings.TrimSpace(targetUser)
	if targetUser == "" {
		return errors.New("اسم المستخدم مطلوب")
	}
	if targetUser != user.UserName {
		if err := rbac.Require(user.Role, rbac.PermManageNotifications); err != nil {
			return err
		}
	}
	e := 0
	if enabled {
		e = 1
	}
	_, err := s.DB.Exec(`INSERT INTO NotificationSettings(UserName,NotifType,Enabled) VALUES(?,?,?)
		ON CONFLICT(UserName,NotifType) DO UPDATE SET Enabled=excluded.Enabled`,
		targetUser, notifType, e)
	return err
}

// ListSendLog returns the full send log for a definition (admin), newest first.
func (s *Store) ListSendLog(ctx context.Context, user *auth.User, notificationID int64, limit int) ([]SendLogEntry, error) {
	if err := rbac.Require(user.Role, rbac.PermManageNotifications); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.DB.Query(`SELECT LogID,NotificationID,UserName,EventKey,SentAt
		FROM NotificationSendLog WHERE NotificationID=? ORDER BY LogID DESC LIMIT ?`, notificationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SendLogEntry
	for rows.Next() {
		var e SendLogEntry
		if err := rows.Scan(&e.ID, &e.NotificationID, &e.UserName, &e.EventKey, &e.SentAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// --- Automatic triggers (called by other domain builders) ---

// NotifyPriceDrop sends a price-drop alert for a product to staff users who
// previously sold it (dedupe per product + new price). Returns recipients sent.
func (s *Store) NotifyPriceDrop(ctx context.Context, actor *auth.User, productID int64, productName string, oldPrice, newPrice int64) (int, error) {
	if newPrice >= oldPrice {
		return 0, errors.New("السعر الجديد ليس أقل من السعر القديم")
	}
	var defID int64
	sent := 0
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO Notifications
			(Type,Title,Body,LinkedProductID,Audience,UserName,IsRead,CreatedAt,SendAt,Status)
			VALUES('price_drop',?,?,?,'product:'||?, '',0,?,?,'sent')`,
			"انخفاض سعر: "+productName,
			fmt.Sprintf("انخفض سعر %s من %d إلى %d قرشاً", productName, oldPrice, newPrice),
			nullableInt(productID), productID, Now(), Now())
		if err != nil {
			return err
		}
		defID, err = res.LastInsertId()
		if err != nil {
			return err
		}
		n, err := deliverNotificationTx(tx, defID, NotifPriceDrop,
			"انخفاض سعر: "+productName,
			fmt.Sprintf("انخفض سعر %s من %d إلى %d قرشاً", productName, oldPrice, newPrice),
			productID, fmt.Sprintf("product:%d", productID),
			fmt.Sprintf("price_drop:%d:%d", productID, newPrice))
		if err != nil {
			return err
		}
		sent = n
		if sent == 0 {
			// PR-L2: reached nobody (no prior sellers, or all deduped /
			// disabled) — leave no 'sent' definition row behind.
			if err := dropEmptyDefinitionTx(tx, defID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return sent, nil
}

// NotifyOffer sends an offer alert. If the offer targets specific products,
// only staff who previously sold those products are targeted; otherwise all
// active users. Dedupe per offer id.
func (s *Store) NotifyOffer(ctx context.Context, actor *auth.User, offerID int64, offerName string) (int, error) {
	audience := "all"
	var productIDs []int64
	rows, err := s.DB.Query(`SELECT TargetID FROM OfferTargets WHERE OfferID=? AND TargetType='product'`, offerID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var pid sql.NullInt64
			if err := rows.Scan(&pid); err == nil && pid.Valid && pid.Int64 > 0 {
				productIDs = append(productIDs, pid.Int64)
			}
		}
		rows.Close()
	}
	if len(productIDs) > 0 {
		audience = "product"
	}
	sent := 0
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO Notifications
			(Type,Title,Body,LinkedProductID,Audience,UserName,IsRead,CreatedAt,SendAt,Status)
			VALUES('offer',?,?,NULL,?,'',0,?,?,'sent')`,
			"عرض جديد: "+offerName, "تم إنشاء عرض جديد: "+offerName, audience, Now(), Now())
		if err != nil {
			return err
		}
		defID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		total := 0
		if len(productIDs) == 0 {
			n, err := deliverNotificationTx(tx, defID, NotifOffer, "عرض جديد: "+offerName,
				"تم إنشاء عرض جديد: "+offerName, 0, "all", fmt.Sprintf("offer:%d", offerID))
			if err != nil {
				return err
			}
			total = n
		} else {
			for _, pid := range productIDs {
				n, err := deliverNotificationTx(tx, defID, NotifOffer, "عرض جديد: "+offerName,
					"تم إنشاء عرض جديد: "+offerName, pid, fmt.Sprintf("product:%d", pid),
					fmt.Sprintf("offer:%d", offerID))
				if err != nil {
					return err
				}
				total += n
			}
		}
		sent = total
		if total == 0 {
			// PR-L2: reached nobody (empty product audience, or all
			// deduped / disabled) — leave no 'sent' definition row behind.
			if err := dropEmptyDefinitionTx(tx, defID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return sent, nil
}

// NotifyLowStock sends a low-stock alert to admin/manager users only.
// Dedupe per product + current quantity, so it re-fires when stock drops further.
func (s *Store) NotifyLowStock(ctx context.Context, actor *auth.User, productID int64, productName string, qtyMilli, minQtyMilli int64) (int, error) {
	sent := 0
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO Notifications
			(Type,Title,Body,LinkedProductID,Audience,UserName,IsRead,CreatedAt,SendAt,Status)
			VALUES('low_stock',?,?,?,'admins','',0,?,?,'sent')`,
			"مخزون منخفض: "+productName,
			fmt.Sprintf("وصل مخزون %s إلى الحد الأدنى", productName),
			nullableInt(productID), Now(), Now())
		if err != nil {
			return err
		}
		defID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		admins, err := tx.Query(`SELECT UserName FROM Users WHERE IsActive=1 AND Role IN ('admin','manager')`)
		if err != nil {
			return err
		}
		defer admins.Close()
		var names []string
		for admins.Next() {
			var nm string
			if err := admins.Scan(&nm); err != nil {
				return err
			}
			names = append(names, nm)
		}
		admins.Close()
		eventKey := fmt.Sprintf("low_stock:%d:%d", productID, qtyMilli)
		for _, name := range names {
			enabled, err := userNotifEnabled(tx, name, NotifLowStock)
			if err != nil {
				return err
			}
			if !enabled {
				continue
			}
			res, err := tx.Exec(`INSERT OR IGNORE INTO NotificationSendLog
				(NotificationID,UserName,EventKey,SentAt) VALUES(?,?,?,?)`, defID, name, eventKey, Now())
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			if n == 0 {
				continue
			}
			if _, err := tx.Exec(`INSERT INTO Notifications
				(Type,Title,Body,LinkedProductID,Audience,UserName,IsRead,CreatedAt,SendAt,Status)
				VALUES('low_stock',?,?,?,'admins',?,0,?,?,'sent')`,
				"مخزون منخفض: "+productName,
				fmt.Sprintf("وصل مخزون %s إلى الحد الأدنى", productName),
				nullableInt(productID), name, Now(), Now()); err != nil {
				return err
			}
			sent++
		}
		if sent == 0 {
			// PR-L2: reached nobody (all deduped / disabled) — leave no
			// 'sent' definition row behind.
			if err := dropEmptyDefinitionTx(tx, defID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return sent, nil
}
