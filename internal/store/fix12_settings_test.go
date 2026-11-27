package store

import (
	"context"
	"fmt"
	"testing"

	"cashierpos-go/internal/auth"
)

// FIX-12 lane F (settings / notifications) tests.

// --- SW-H10: reset must be atomic when the DB holds transactional data ---

func TestFix12ResetWithSaleIsAtomic(t *testing.T) {
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	pid := testProduct(t, s, "سكر", 1000, 2000, 10000)
	b1OpenShiftFor(t, s, env.cashier)
	b1CashCustomerSale(t, s, env, env.cashier, pid, 1000)

	var tablesBefore int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`).Scan(&tablesBefore); err != nil {
		t.Fatal(err)
	}

	// Wrong confirmation: refused, DB untouched.
	if err := s.ResetDatabase(env.ctx, env.admin, "خطأ"); err == nil {
		t.Fatal("reset with wrong confirmation should be refused")
	}
	var invoices int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM Invoices`).Scan(&invoices); err != nil || invoices != 1 {
		t.Fatalf("after refused reset, invoices = %d, err %v (want 1)", invoices, err)
	}

	// Exact confirmation: must succeed even with a sale in the DB.
	if err := s.ResetDatabase(env.ctx, env.admin, ResetConfirmation); err != nil {
		t.Fatalf("ResetDatabase with a sale in the DB: %v", err)
	}
	// Fresh admin/admin123 can log in again.
	var stored string
	if err := s.DB.QueryRow(`SELECT Password FROM Users WHERE UserName='admin'`).Scan(&stored); err != nil {
		t.Fatalf("admin row missing after reset: %v", err)
	}
	ok, _ := auth.VerifyPassword(stored, "admin123")
	if !ok {
		t.Fatal("admin/admin123 does not verify after reset")
	}
	users, err := s.ListUsers(env.ctx, env.admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].UserName != "admin" {
		t.Fatalf("after reset users = %+v, want only admin", users)
	}
	// Every table is back; nothing is half-dropped.
	var tablesAfter int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`).Scan(&tablesAfter); err != nil {
		t.Fatal(err)
	}
	if tablesAfter != tablesBefore {
		t.Fatalf("tables after reset = %d, want %d (pre-reset count)", tablesAfter, tablesBefore)
	}
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM Customers WHERE CustomerID=1`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("cash customer after reset = %d, err %v (want 1)", n, err)
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM Invoices`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("invoices after reset = %d, err %v (want 0)", n, err)
	}
}

// --- SW-H11: two custom notifications without event keys must both send ---

func TestFix12CustomNotificationNoKeyTwice(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := testAdmin()
	mustCreateUser(t, s, "cash1", "cashier")
	cashier := &auth.User{ID: 2, UserName: "cash1", Role: "cashier"}

	for i := 1; i <= 2; i++ {
		if _, err := s.CreateNotification(ctx, admin, NotificationInput{
			Type: NotifCustom, Title: fmt.Sprintf("تنبيه %d", i), Body: "نص التنبيه",
			Audience: "all",
		}); err != nil {
			t.Fatalf("custom notification #%d without event key: %v", i, err)
		}
	}
	if n := inboxCount(t, s, cashier); n != 2 {
		t.Fatalf("cashier unread = %d, want 2 (both customs delivered)", n)
	}
	if n := inboxCount(t, s, admin); n != 2 {
		t.Fatalf("admin unread = %d, want 2 (both customs delivered)", n)
	}
}

// --- SW-M13: skips leave a log row (skipped_disabled / skipped_duplicate) ---

func sendLogStatuses(t *testing.T, s *Store, userName, status string) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM NotificationSendLog WHERE UserName=? AND Status=?`,
		userName, status).Scan(&n); err != nil {
		t.Fatalf("send-log count query: %v", err)
	}
	return n
}

func TestFix12SendLogSkipRows(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := testAdmin()
	mustCreateUser(t, s, "cash1", "cashier")
	cashier := &auth.User{ID: 2, UserName: "cash1", Role: "cashier"}

	// Normal delivery writes Status='sent' rows.
	defID, err := s.CreateNotification(ctx, admin, NotificationInput{
		Type: NotifCustom, Title: "عادي", Body: "نص", Audience: "all", EventKey: "fix12-normal",
	})
	if err != nil {
		t.Fatalf("CreateNotification: %v", err)
	}
	if n := sendLogStatuses(t, s, "cash1", "sent"); n != 1 {
		t.Fatalf("sent rows for cash1 = %d, want 1", n)
	}
	logs, err := s.ListSendLog(ctx, admin, defID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 2 {
		t.Fatalf("ListSendLog = %d rows, want 2", len(logs))
	}
	// The two delivery rows carry Status='sent' (checked by SQL so the
	// test also compiles against pre-fix code, where the field is new).
	var sentRows int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM NotificationSendLog WHERE NotificationID=? AND Status='sent'`,
		defID).Scan(&sentRows); err != nil {
		t.Fatalf("sent-rows query: %v", err)
	}
	if sentRows != 2 {
		t.Fatalf("sent rows for def %d = %d, want 2", defID, sentRows)
	}

	// Duplicate dispatch of the same event key: no 2nd inbox row, but a
	// skipped_duplicate row per recipient.
	if _, err := s.CreateNotification(ctx, admin, NotificationInput{
		Type: NotifCustom, Title: "مكرر", Body: "نفس الحدث", Audience: "all", EventKey: "fix12-normal",
	}); err != nil {
		t.Fatalf("duplicate CreateNotification: %v", err)
	}
	if n := inboxCount(t, s, cashier); n != 1 {
		t.Fatalf("after duplicate, cashier unread = %d, want 1 (dedupe still holds)", n)
	}
	if n := sendLogStatuses(t, s, "cash1", "skipped_duplicate"); n != 1 {
		t.Fatalf("skipped_duplicate rows for cash1 = %d, want 1", n)
	}
	if n := sendLogStatuses(t, s, "admin", "skipped_duplicate"); n != 1 {
		t.Fatalf("skipped_duplicate rows for admin = %d, want 1", n)
	}

	// Disabled type: the trigger fires, the recipient gets nothing, but a
	// skipped_disabled row records it.
	env := b1SaleEnvSetup(t, s)
	pid := testProduct(t, s, "سكر", 1000, 2000, 10000)
	b1OpenShiftFor(t, s, env.cashier)
	b1CashCustomerSale(t, s, env, env.cashier, pid, 1000)
	// The seeded cashier for the sale is env.cashier (b1cashier); disable
	// price_drop for them and drop the price.
	if err := s.SetUserNotificationSetting(ctx, env.cashier, env.cashier.UserName, NotifPriceDrop, false); err != nil {
		t.Fatalf("SetUserNotificationSetting: %v", err)
	}
	sent, err := s.NotifyPriceDrop(ctx, admin, pid, "سكر", 2000, 1500)
	if err != nil {
		t.Fatalf("NotifyPriceDrop: %v", err)
	}
	if sent != 0 {
		t.Fatalf("NotifyPriceDrop sent = %d, want 0 (only seller disabled)", sent)
	}
	if n := sendLogStatuses(t, s, env.cashier.UserName, "skipped_disabled"); n != 1 {
		t.Fatalf("skipped_disabled rows for %s = %d, want 1", env.cashier.UserName, n)
	}
	// Re-enable and re-drop to a new price: delivery works again (the skip
	// row must not block the real dedupe key).
	if err := s.SetUserNotificationSetting(ctx, env.cashier, env.cashier.UserName, NotifPriceDrop, true); err != nil {
		t.Fatal(err)
	}
	sent, err = s.NotifyPriceDrop(ctx, admin, pid, "سكر", 1500, 1400)
	if err != nil {
		t.Fatalf("NotifyPriceDrop after re-enable: %v", err)
	}
	if sent != 1 {
		t.Fatalf("NotifyPriceDrop after re-enable sent = %d, want 1", sent)
	}
}

// --- SW-L11: price_drop / offer require a linked product ---

func TestFix12LinkedProductRequired(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := testAdmin()
	mustCreateUser(t, s, "cash1", "cashier")

	for _, typ := range []string{NotifPriceDrop, NotifOffer} {
		if _, err := s.CreateNotification(ctx, admin, NotificationInput{
			Type: typ, Title: "بلا منتج", Body: "نص", Audience: "all",
		}); err == nil {
			t.Fatalf("%s without a linked product should be refused", typ)
		}
		if _, err := s.CreateNotification(ctx, admin, NotificationInput{
			Type: typ, Title: "مع منتج", Body: "نص", Audience: "all", LinkedProductID: 1,
		}); err != nil {
			t.Fatalf("%s with a linked product: %v", typ, err)
		}
	}
	// Other types are unaffected: custom without a product is still fine.
	if _, err := s.CreateNotification(ctx, admin, NotificationInput{
		Type: NotifCustom, Title: "مخصص", Body: "نص", Audience: "all",
	}); err != nil {
		t.Fatalf("custom without a product: %v", err)
	}
}

// --- SW-L12: custom notifications require a non-empty body ---

func TestFix12CustomBodyRequired(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := testAdmin()

	if _, err := s.CreateNotification(ctx, admin, NotificationInput{
		Type: NotifCustom, Title: "بلا نص", Body: "   ", Audience: "all",
	}); err == nil {
		t.Fatal("custom notification with an empty body should be refused")
	}
	if _, err := s.CreateNotification(ctx, admin, NotificationInput{
		Type: NotifCustom, Title: "مع نص", Body: "نص التنبيه", Audience: "all",
	}); err != nil {
		t.Fatalf("custom notification with a body: %v", err)
	}
	// Non-custom types keep accepting an empty body (out of scope).
	if _, err := s.CreateNotification(ctx, admin, NotificationInput{
		Type: NotifOffer, Title: "عرض", Body: "", Audience: "all", LinkedProductID: 1,
	}); err != nil {
		t.Fatalf("offer with empty body: %v", err)
	}
}
