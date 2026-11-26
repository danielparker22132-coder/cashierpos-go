// Package store — B4: attendance viewing (recording happens on
// login/logout in routes.go via auth.RecordLogin/RecordLogout).
package store

import (
	"context"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/rbac"
)

// AttendanceRow is one UserAttendance row.
type AttendanceRow struct {
	ID             int64
	UserName       string
	LogDate        string
	FirstLoginTime string
	LastLogoutTime string
	LoginCount     int
	IsOnline       bool
}

// AttendanceUserSummary aggregates one user's attendance in a range.
type AttendanceUserSummary struct {
	UserName   string
	Days       int64
	Logins     int64
	OnlineDays int64
}

// AttendanceSummary is the attendance list + summary.
type AttendanceSummary struct {
	From        string
	To          string
	Rows        []AttendanceRow
	OnlineNow   int
	OnlineUsers []string
	PerUser     []AttendanceUserSummary
	TotalDays   int64
	TotalLogins int64
}

// ListAttendance returns attendance rows for a range, optionally for one
// user, plus the summary (online users highlighted in the UI).
func (s *Store) ListAttendance(ctx context.Context, user *auth.User, from, to, userName string) (AttendanceSummary, error) {
	var out AttendanceSummary
	if err := rbac.Require(user.Role, rbac.PermViewAttendance); err != nil {
		return out, err
	}
	if err := validateRange(from, to); err != nil {
		return out, err
	}
	out.From, out.To = from, to
	q := `SELECT ID,UserName,LogDate,FirstLoginTime,COALESCE(LastLogoutTime,''),LoginCount,IsOnline
		FROM UserAttendance WHERE LogDate BETWEEN ? AND ?`
	args := []any{from, to}
	if userName != "" {
		q += " AND UserName=?"
		args = append(args, userName)
	}
	q += " ORDER BY LogDate DESC, UserName"
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var r AttendanceRow
		var online int
		if err := rows.Scan(&r.ID, &r.UserName, &r.LogDate, &r.FirstLoginTime, &r.LastLogoutTime, &r.LoginCount, &online); err != nil {
			rows.Close()
			return out, err
		}
		r.IsOnline = online == 1
		out.Rows = append(out.Rows, r)
		if r.IsOnline {
			out.OnlineNow++
			out.OnlineUsers = append(out.OnlineUsers, r.UserName)
		}
		out.TotalLogins += int64(r.LoginCount)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	// Per-user summary.
	uq := `SELECT UserName, COUNT(DISTINCT LogDate), COALESCE(SUM(LoginCount),0),
		COALESCE(SUM(IsOnline),0) FROM UserAttendance WHERE LogDate BETWEEN ? AND ?`
	uargs := []any{from, to}
	if userName != "" {
		uq += " AND UserName=?"
		uargs = append(uargs, userName)
	}
	uq += " GROUP BY UserName ORDER BY COUNT(DISTINCT LogDate) DESC"
	urows, err := s.DB.QueryContext(ctx, uq, uargs...)
	if err != nil {
		return out, err
	}
	defer urows.Close()
	for urows.Next() {
		var u AttendanceUserSummary
		if err := urows.Scan(&u.UserName, &u.Days, &u.Logins, &u.OnlineDays); err != nil {
			return out, err
		}
		out.PerUser = append(out.PerUser, u)
		out.TotalDays += u.Days
	}
	return out, urows.Err()
}

// OnlineUsers returns the usernames currently online (IsOnline=1 today).
func (s *Store) OnlineUsers(ctx context.Context, user *auth.User) ([]string, error) {
	if err := rbac.Require(user.Role, rbac.PermViewAttendance); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(`SELECT UserName FROM UserAttendance WHERE LogDate=? AND IsOnline=1 ORDER BY UserName`, Today())
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
