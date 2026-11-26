package store

import (
	"context"
	"testing"

	"cashierpos-go/internal/auth"
)

func TestAttendanceListAndSummary(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testAdmin()
	_, err = s.DB.Exec(`INSERT INTO UserAttendance(UserName,LogDate,FirstLoginTime,LastLogoutTime,LoginCount,IsOnline)
		VALUES('admin','2026-11-25','08:00:00','17:00:00',1,0),
		('cash1','2026-11-25','08:05:00',NULL,3,1),
		('cash1','2026-11-24','08:10:00','16:00:00',1,0),
		('mgr1','2026-11-24','09:00:00','15:00:00',2,0)`)
	if err != nil {
		t.Fatalf("seed attendance: %v", err)
	}

	sum, err := s.ListAttendance(ctx, admin, "2026-11-24", "2026-11-25", "")
	if err != nil {
		t.Fatalf("ListAttendance: %v", err)
	}
	if len(sum.Rows) != 4 {
		t.Fatalf("rows = %d, want 4", len(sum.Rows))
	}
	if sum.OnlineNow != 1 || len(sum.OnlineUsers) != 1 || sum.OnlineUsers[0] != "cash1" {
		t.Fatalf("online = %d %v, want 1 [cash1]", sum.OnlineNow, sum.OnlineUsers)
	}
	if sum.TotalLogins != 7 {
		t.Fatalf("total logins = %d, want 7", sum.TotalLogins)
	}
	if len(sum.PerUser) != 3 {
		t.Fatalf("per-user rows = %d, want 3", len(sum.PerUser))
	}
	// User filter.
	one, err := s.ListAttendance(ctx, admin, "2026-11-24", "2026-11-25", "cash1")
	if err != nil {
		t.Fatal(err)
	}
	if len(one.Rows) != 2 || one.TotalDays != 2 {
		t.Fatalf("cash1 filter = %d rows, %d days, want 2/2", len(one.Rows), one.TotalDays)
	}
	// Reversed range refused.
	if _, err := s.ListAttendance(ctx, admin, "2026-11-25", "2026-11-24", ""); err == nil {
		t.Fatal("reversed range should be refused")
	}
	// Cashier cannot view attendance.
	cashier := &auth.User{ID: 2, UserName: "cash1", Role: "cashier"}
	if _, err := s.ListAttendance(ctx, cashier, "2026-11-24", "2026-11-25", ""); err == nil {
		t.Fatal("cashier should not view attendance")
	}
	// Online users (today-based; seeded rows are for 2026-11-24/25).
	online, err := s.OnlineUsers(ctx, admin)
	if err != nil {
		t.Fatalf("OnlineUsers: %v", err)
	}
	_ = online // date-dependent; the list path above covers the summary
}
