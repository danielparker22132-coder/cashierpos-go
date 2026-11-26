package store

import (
	"context"
	"testing"

	"cashierpos-go/internal/auth"
)

func seedAuditData(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	_, err := s.DB.Exec(`INSERT INTO AuditLog(UserID,UserName,ActionType,ActionCategory,InvoiceNumber,Notes,LogDate)
		VALUES(1,'admin','create_invoice','sales',1,'بيع','2026-11-25 10:00:00'),
		(1,'admin','cancel_invoice','sales',2,'إلغاء','2026-11-25 11:00:00'),
		(2,'cash1','cancel_invoice','sales',3,'إلغاء','2026-11-24 09:00:00'),
		(2,'cash1','delete_user','users',NULL,'حذف','2026-11-25 12:00:00')`)
	if err != nil {
		t.Fatalf("seed audit: %v", err)
	}
	_, err = s.DB.Exec(`INSERT INTO AuditLogDetails(LogID,ProductID,ProductName,QuantityBefore,QuantityAfter,QuantityChange,PriceBefore,PriceAfter,LineTotal,Notes)
		VALUES(1,5,'سكر',10000,9000,-1000,5000,5000,5000,'بيع')`)
	if err != nil {
		t.Fatalf("seed audit details: %v", err)
	}
	_, err = s.DB.Exec(`INSERT INTO AuditAlerts(LogID,UserName,AlertType,AlertMessage,AlertValue,Threshold,IsRead,IsResolved,AlertDate)
		VALUES(2,'admin','cancel_amount','إلغاء فاتورة 2',20000,10000,0,0,'2026-11-25 11:00:00'),
		(3,'cash1','cancel_amount','إلغاء فاتورة 3',5000,10000,1,0,'2026-11-24 09:00:00')`)
	if err != nil {
		t.Fatalf("seed alerts: %v", err)
	}
	_ = ctx
}

func TestAuditListAndDetails(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testAdmin()
	seedAuditData(t, s)

	all, err := s.ListAuditLog(ctx, admin, AuditFilter{From: "2026-11-24", To: "2026-11-25"})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("audit rows = %d, want 4", len(all))
	}
	byUser, err := s.ListAuditLog(ctx, admin, AuditFilter{From: "2026-11-24", To: "2026-11-25", UserName: "cash1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byUser) != 2 {
		t.Fatalf("cash1 rows = %d, want 2", len(byUser))
	}
	byInv, err := s.ListAuditLog(ctx, admin, AuditFilter{From: "2026-11-24", To: "2026-11-25", InvoiceNumber: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(byInv) != 1 || byInv[0].ActionType != "cancel_invoice" {
		t.Fatalf("invoice filter = %+v, want 1 cancel", byInv)
	}
	susp, err := s.ListAuditLog(ctx, admin, AuditFilter{From: "2026-11-24", To: "2026-11-25", SuspiciousOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(susp) != 3 { // 2 cancels + 1 delete_user
		t.Fatalf("suspicious rows = %d, want 3", len(susp))
	}
	if _, err := s.ListAuditLog(ctx, admin, AuditFilter{From: "2026-11-25", To: "2026-11-24"}); err == nil {
		t.Fatal("expected error for reversed date range")
	}
	cashier := &auth.User{ID: 2, UserName: "cash1", Role: "cashier"}
	if _, err := s.ListAuditLog(ctx, cashier, AuditFilter{}); err == nil {
		t.Fatal("cashier should not list the audit log")
	}

	entry, details, err := s.GetAuditLogDetails(ctx, admin, 1)
	if err != nil {
		t.Fatal(err)
	}
	if entry.ActionType != "create_invoice" || len(details) != 1 || details[0].ProductName != "سكر" {
		t.Fatalf("details = %+v / %+v", entry, details)
	}
	if _, _, err := s.GetAuditLogDetails(ctx, admin, 9999); err == nil {
		t.Fatal("expected error for missing log")
	}
}

func TestAuditAlertsUnreadAndResolve(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testAdmin()
	seedAuditData(t, s)

	n, err := s.UnreadAuditAlertsCount(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("unread audit alerts = %d, want 1", n)
	}
	cashier := &auth.User{ID: 2, UserName: "cash1", Role: "cashier"}
	if _, err := s.UnreadAuditAlertsCount(ctx, cashier); err == nil {
		t.Fatal("cashier should not get the audit alert count")
	}
	alerts, err := s.ListAuditAlerts(ctx, admin, true, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 1 {
		t.Fatalf("unread alerts = %d, want 1", len(alerts))
	}
	if err := s.MarkAuditAlertRead(ctx, admin, alerts[0].ID); err != nil {
		t.Fatalf("MarkAuditAlertRead: %v", err)
	}
	if n, _ := s.UnreadAuditAlertsCount(ctx, admin); n != 0 {
		t.Fatalf("after read, unread = %d, want 0", n)
	}
	if err := s.ResolveAuditAlert(ctx, admin, alerts[0].ID, "تمت المراجعة"); err != nil {
		t.Fatalf("ResolveAuditAlert: %v", err)
	}
	all, _ := s.ListAuditAlerts(ctx, admin, false, 10)
	for _, a := range all {
		if a.ID == alerts[0].ID && (!a.IsResolved || a.ResolvedBy != "admin") {
			t.Fatalf("alert not resolved: %+v", a)
		}
	}
	if err := s.ResolveAuditAlert(ctx, admin, 9999, "x"); err == nil {
		t.Fatal("expected error resolving missing alert")
	}
}

func TestSmartMonitorThresholds(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testAdmin()
	// Canceled invoice above the threshold, one below it.
	_, err = s.DB.Exec(`INSERT INTO Invoices(InvoiceNumber,CustomerID,SaleType,TotalAmount,Discount,NetAmount,PaidAmount,RemainingAmount,PaymentMethod,UserName,InvoiceDate,IsCanceled,CanceledBy,CanceledAt,CancelReason)
		VALUES(1,1,'retail',20000,0,20000,0,0,'Cash','admin','2026-11-25 10:00:00',1,'admin','2026-11-25 10:00:00','خطأ'),
		(2,1,'retail',1000,0,1000,0,0,'Cash','admin','2026-11-25 10:00:00',1,'admin','2026-11-25 10:00:00','خطأ'),
		(3,1,'retail',10000,3000,7000,7000,0,'Cash','cash1','2026-11-25 10:00:00',0,'','','')`)
	if err != nil {
		t.Fatalf("seed invoices: %v", err)
	}
	if err := s.SaveMonitorThresholds(ctx, admin, MonitorThresholds{CancelAmount: 10000, DiscountPct: 20, ReturnAmount: 5000}); err != nil {
		t.Fatalf("SaveMonitorThresholds: %v", err)
	}
	th, err := s.GetMonitorThresholds(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if th.CancelAmount != 10000 || th.DiscountPct != 20 || th.ReturnAmount != 5000 {
		t.Fatalf("thresholds = %+v", th)
	}
	if err := s.SaveMonitorThresholds(ctx, admin, MonitorThresholds{CancelAmount: -1}); err == nil {
		t.Fatal("negative threshold should be refused")
	}
	created, err := s.RunSmartMonitor(ctx, admin)
	if err != nil {
		t.Fatalf("RunSmartMonitor: %v", err)
	}
	// 1 cancel above threshold + 1 discount 30% above 20% = 2 alerts.
	if created != 2 {
		t.Fatalf("RunSmartMonitor created = %d, want 2", created)
	}
	// Second run: dedupe — no new alerts.
	created, err = s.RunSmartMonitor(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if created != 0 {
		t.Fatalf("second RunSmartMonitor created = %d, want 0 (dedupe)", created)
	}
	// Smart reports.
	rows, err := s.SmartReport(ctx, admin, "cancels", "2026-11-25", "2026-11-25")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Count != 2 {
		t.Fatalf("smart cancels = %+v, want 1 row count 2", rows)
	}
	rows, err = s.SmartReport(ctx, admin, "cashier_compare", "2026-11-25", "2026-11-25")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Label != "cash1" {
		t.Fatalf("cashier compare = %+v", rows)
	}
	if _, err := s.SmartReport(ctx, admin, "nope", "2026-11-25", "2026-11-25"); err == nil {
		t.Fatal("expected error for unknown smart report kind")
	}
}
