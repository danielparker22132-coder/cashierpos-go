package store

import (
	"context"
	"testing"

	"cashierpos-go/internal/auth"
)

func testAdmin() *auth.User {
	return &auth.User{ID: 1, UserName: "admin", FullName: "مدير النظام", Role: "admin"}
}

func mustCreateUser(t *testing.T, s *Store, name, role string) int64 {
	t.Helper()
	id, err := s.CreateUser(context.Background(), testAdmin(), UserInput{
		UserName: name, Password: "pass1234", FullName: name, Role: role, IsActive: true,
	})
	if err != nil {
		t.Fatalf("CreateUser %s: %v", name, err)
	}
	return id
}

func inboxCount(t *testing.T, s *Store, user *auth.User) int {
	t.Helper()
	n, err := s.UnreadNotificationCount(context.Background(), user)
	if err != nil {
		t.Fatalf("UnreadNotificationCount: %v", err)
	}
	return n
}

func TestNotificationsCustomAndDedupe(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testAdmin()
	mustCreateUser(t, s, "cash1", "cashier")

	// Custom notification to everyone, with an event key.
	_, err = s.CreateNotification(ctx, admin, NotificationInput{
		Type: NotifCustom, Title: "تنبيه تجريبي", Body: "نص التنبيه",
		Audience: "all", EventKey: "evt-1",
	})
	if err != nil {
		t.Fatalf("CreateNotification: %v", err)
	}
	cashier := &auth.User{ID: 2, UserName: "cash1", Role: "cashier"}
	if n := inboxCount(t, s, cashier); n != 1 {
		t.Fatalf("cashier unread = %d, want 1", n)
	}
	if n := inboxCount(t, s, admin); n != 1 {
		t.Fatalf("admin unread = %d, want 1", n)
	}

	// Same event key again: dedupe per (user + event) — no new rows.
	_, err = s.CreateNotification(ctx, admin, NotificationInput{
		Type: NotifCustom, Title: "تنبيه مكرر", Body: "نفس الحدث",
		Audience: "all", EventKey: "evt-1",
	})
	if err != nil {
		t.Fatalf("CreateNotification duplicate: %v", err)
	}
	if n := inboxCount(t, s, cashier); n != 1 {
		t.Fatalf("after duplicate, cashier unread = %d, want 1 (dedupe)", n)
	}

	// Mark as read.
	inbox, err := s.ListInbox(ctx, cashier, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 {
		t.Fatalf("inbox len = %d, want 1", len(inbox))
	}
	if err := s.MarkNotificationRead(ctx, cashier, inbox[0].ID); err != nil {
		t.Fatalf("MarkNotificationRead: %v", err)
	}
	if n := inboxCount(t, s, cashier); n != 0 {
		t.Fatalf("after read, cashier unread = %d, want 0", n)
	}
	// Another user's row cannot be marked read by cash1.
	if err := s.MarkNotificationRead(ctx, cashier, inbox[0].ID+1000); err == nil {
		t.Fatal("expected error marking a nonexistent notification read")
	}
}

func TestNotificationsUserSettingBlocksSend(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testAdmin()
	mustCreateUser(t, s, "cash1", "cashier")
	cashier := &auth.User{ID: 2, UserName: "cash1", Role: "cashier"}

	// Cashier disables custom notifications.
	if err := s.SetUserNotificationSetting(ctx, cashier, "cash1", NotifCustom, false); err != nil {
		t.Fatalf("SetUserNotificationSetting: %v", err)
	}
	settings, err := s.GetUserNotificationSettings(ctx, cashier)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range settings {
		if st.Type == NotifCustom && st.Enabled {
			t.Fatal("custom should be disabled for cash1")
		}
		if st.Type == NotifOffer && !st.Enabled {
			t.Fatal("offer should default to enabled for cash1")
		}
	}
	_, err = s.CreateNotification(ctx, admin, NotificationInput{
		Type: NotifCustom, Title: "لن يصل", Body: "x", Audience: "all",
	})
	if err != nil {
		t.Fatalf("CreateNotification: %v", err)
	}
	if n := inboxCount(t, s, cashier); n != 0 {
		t.Fatalf("cashier unread = %d, want 0 (setting disabled)", n)
	}
	if n := inboxCount(t, s, admin); n != 1 {
		t.Fatalf("admin unread = %d, want 1", n)
	}
}

func TestNotificationsLowStockAndTargets(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testAdmin()
	mustCreateUser(t, s, "cash1", "cashier")
	mustCreateUser(t, s, "mgr1", "manager")
	cashier := &auth.User{ID: 2, UserName: "cash1", Role: "cashier"}
	manager := &auth.User{ID: 3, UserName: "mgr1", Role: "manager"}

	// Low stock goes to admin/manager only.
	sent, err := s.NotifyLowStock(ctx, admin, 7, "سكر", 1000, 5000)
	if err != nil {
		t.Fatalf("NotifyLowStock: %v", err)
	}
	if sent != 2 {
		t.Fatalf("NotifyLowStock sent = %d, want 2 (admin+manager)", sent)
	}
	if n := inboxCount(t, s, cashier); n != 0 {
		t.Fatalf("cashier unread = %d, want 0 for low-stock", n)
	}
	if n := inboxCount(t, s, manager); n != 1 {
		t.Fatalf("manager unread = %d, want 1", n)
	}
	// Same product + same quantity: dedupe, no re-fire.
	sent, err = s.NotifyLowStock(ctx, admin, 7, "سكر", 1000, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if sent != 0 {
		t.Fatalf("second NotifyLowStock sent = %d, want 0 (dedupe)", sent)
	}
	// Quantity dropped further: fires again.
	sent, err = s.NotifyLowStock(ctx, admin, 7, "سكر", 500, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if sent != 2 {
		t.Fatalf("lower-qty NotifyLowStock sent = %d, want 2", sent)
	}

	// Price drop with no previous sellers: no recipients, no error.
	sent, err = s.NotifyPriceDrop(ctx, admin, 7, "سكر", 5000, 4000)
	if err != nil {
		t.Fatalf("NotifyPriceDrop: %v", err)
	}
	if sent != 0 {
		t.Fatalf("NotifyPriceDrop sent = %d, want 0 (no previous sellers)", sent)
	}
	if _, err := s.NotifyPriceDrop(ctx, admin, 7, "سكر", 4000, 5000); err == nil {
		t.Fatal("expected error when new price is not lower")
	}

	// Offer with no product targets goes to all active users.
	sent, err = s.NotifyOffer(ctx, admin, 1, "عرض التوفير")
	if err != nil {
		t.Fatalf("NotifyOffer: %v", err)
	}
	if sent != 3 {
		t.Fatalf("NotifyOffer sent = %d, want 3", sent)
	}
	// Same offer again: dedupe.
	sent, err = s.NotifyOffer(ctx, admin, 1, "عرض التوفير")
	if err != nil {
		t.Fatal(err)
	}
	if sent != 0 {
		t.Fatalf("second NotifyOffer sent = %d, want 0 (dedupe)", sent)
	}
}

func TestNotificationsAdminManage(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testAdmin()
	cashier := &auth.User{ID: 2, UserName: "cash1", Role: "cashier"}
	mustCreateUser(t, s, "cash1", "cashier")

	// Cashier cannot manage notifications.
	if _, err := s.CreateNotification(ctx, cashier, NotificationInput{Type: NotifCustom, Title: "x"}); err == nil {
		t.Fatal("cashier should not create notifications")
	}
	// Scheduled notification stays scheduled and is not in the inbox yet.
	id, err := s.CreateNotification(ctx, admin, NotificationInput{
		Type: NotifScheduled, Title: "مجدول", Body: "لاحقاً", Audience: "all",
		SendAt: "2999-01-01 10:00:00",
	})
	if err != nil {
		t.Fatalf("CreateNotification scheduled: %v", err)
	}
	got, err := s.GetNotification(ctx, admin, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "scheduled" {
		t.Fatalf("status = %q, want scheduled", got.Status)
	}
	if n := inboxCount(t, s, admin); n != 0 {
		t.Fatalf("scheduled notification leaked to inbox: %d", n)
	}
	// Disable it, then list admin view + send log.
	if err := s.SetNotificationStatus(ctx, admin, id, false); err != nil {
		t.Fatalf("SetNotificationStatus: %v", err)
	}
	list, err := s.ListNotificationsAdmin(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Status != "disabled" {
		t.Fatalf("admin list = %+v, want 1 disabled row", list)
	}
	logs, err := s.ListSendLog(ctx, admin, id, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 0 {
		t.Fatalf("send log for unsent scheduled = %d, want 0", len(logs))
	}
	// Update the title.
	if err := s.UpdateNotification(ctx, admin, id, NotificationInput{
		Type: NotifScheduled, Title: "مجدول معدل", Body: "لاحقاً", Audience: "all",
		SendAt: "2999-01-01 10:00:00",
	}); err != nil {
		t.Fatalf("UpdateNotification: %v", err)
	}
	got, _ = s.GetNotification(ctx, admin, id)
	if got.Title != "مجدول معدل" {
		t.Fatalf("title = %q, want updated", got.Title)
	}
}
