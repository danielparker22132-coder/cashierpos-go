// Package auth implements password verification (SHA-256, legacy MD5,
// legacy plaintext — with upgrade to SHA-256 on successful login) and
// session tokens. Password algorithm matches the PyQt build
// (app/auth.py: hash_password = sha256 hex, no salt).
package auth

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// User is a logged-in user (role as stored; use rbac.EffectiveRole for checks).
type User struct {
	ID                 int64
	UserName           string
	FullName           string
	Role               string
	IsActive           bool
	MustChangePassword bool
}

var (
	// ErrBadCredentials is the generic login failure (no user enumeration).
	ErrBadCredentials = errors.New("اسم المستخدم أو كلمة المرور غير صحيحة")
	// ErrInactive is returned for disabled accounts.
	ErrInactive = errors.New("هذا الحساب غير مفعّل — تواصل مع المدير")
)

// HashPassword returns the SHA-256 hex of the password (no salt, PyQt-compatible).
func HashPassword(password string) string {
	h := sha256.Sum256([]byte(password))
	return hex.EncodeToString(h[:])
}

func md5Hex(password string) string {
	h := md5.Sum([]byte(password))
	return hex.EncodeToString(h[:])
}

// VerifyPassword checks stored against the supplied password, accepting
// SHA-256 hex, legacy MD5 hex, or legacy plaintext. It reports whether the
// stored hash needs upgrading to SHA-256.
func VerifyPassword(stored, password string) (ok bool, needsUpgrade bool) {
	plain := password
	if stored == "" {
		return false, false
	}
	if strings.EqualFold(stored, HashPassword(plain)) {
		return true, false
	}
	if strings.EqualFold(stored, md5Hex(plain)) {
		return true, true
	}
	if stored == plain {
		return true, true
	}
	return false, false
}

// Authenticate verifies username/password against the Users table. On a
// legacy-hash success it upgrades the stored hash to SHA-256.
func Authenticate(db *sql.DB, username, password string) (*User, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, errors.New("برجاء إدخال اسم المستخدم")
	}
	if password == "" {
		return nil, errors.New("برجاء إدخال كلمة المرور")
	}
	var u User
	var stored string
	var isActive, mustChange int
	err := db.QueryRow(`SELECT UserID,UserName,FullName,Role,Password,IsActive,MustChangePassword
		FROM Users WHERE UserName=?`, username).
		Scan(&u.ID, &u.UserName, &u.FullName, &u.Role, &stored, &isActive, &mustChange)
	if err == sql.ErrNoRows {
		return nil, ErrBadCredentials
	}
	if err != nil {
		return nil, err
	}
	ok, upgrade := VerifyPassword(stored, password)
	if !ok {
		return nil, ErrBadCredentials
	}
	if isActive != 1 {
		return nil, ErrInactive
	}
	u.IsActive = true
	u.MustChangePassword = mustChange == 1
	if upgrade {
		_, _ = db.Exec(`UPDATE Users SET Password=? WHERE UserID=?`, HashPassword(password), u.ID)
	}
	return &u, nil
}

// ChangePassword sets a new password (SHA-256) for a user.
func ChangePassword(db *sql.DB, userID int64, newPassword string) error {
	if len(newPassword) < 4 {
		return errors.New("كلمة المرور يجب أن تكون 4 أحرف على الأقل")
	}
	_, err := db.Exec(`UPDATE Users SET Password=?, MustChangePassword=0 WHERE UserID=?`,
		HashPassword(newPassword), userID)
	return err
}

// NewSessionToken returns a random 64-hex-char session token.
func NewSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// CreateSession inserts a 12-hour session row.
func CreateSession(db *sql.DB, u *User) (string, error) {
	token, err := NewSessionToken()
	if err != nil {
		return "", err
	}
	now := time.Now()
	exp := now.Add(12 * time.Hour)
	_, err = db.Exec(`INSERT INTO Sessions(Token,UserID,UserName,Role,CreatedAt,ExpiresAt)
		VALUES(?,?,?,?,?,?)`, token, u.ID, u.UserName, u.Role,
		now.Format("2006-01-02 15:04:05"), exp.Format("2006-01-02 15:04:05"))
	if err != nil {
		return "", err
	}
	return token, nil
}

// UserFromSession validates a token and returns the user, or nil.
func UserFromSession(db *sql.DB, token string) *User {
	if token == "" {
		return nil
	}
	var u User
	var isActive, mustChange int
	err := db.QueryRow(`SELECT s.UserID,s.UserName,u.FullName,s.Role,u.IsActive,u.MustChangePassword
		FROM Sessions s JOIN Users u ON u.UserID=s.UserID
		WHERE s.Token=? AND s.ExpiresAt > datetime('now','localtime')`, token).
		Scan(&u.ID, &u.UserName, &u.FullName, &u.Role, &isActive, &mustChange)
	if err != nil {
		return nil
	}
	if isActive != 1 {
		return nil
	}
	u.IsActive = true
	u.MustChangePassword = mustChange == 1
	return &u
}

// DeleteSession removes a session (logout).
func DeleteSession(db *sql.DB, token string) {
	_, _ = db.Exec(`DELETE FROM Sessions WHERE Token=?`, token)
}

// cairoNow returns the current time in Africa/Cairo (FIX-1 L8: attendance
// dates must use Cairo time like store.Today(), not the server-local clock,
// or a login near midnight lands on the wrong attendance day).
func cairoNow() time.Time {
	loc, err := time.LoadLocation("Africa/Cairo")
	if err != nil {
		return time.Now()
	}
	return time.Now().In(loc)
}

// RecordLogin records attendance (first login of the day) and bumps LoginCount.
func RecordLogin(db *sql.DB, userName string) {
	now := cairoNow()
	today := now.Format("2006-01-02")
	clock := now.Format("15:04:05")
	_, _ = db.Exec(`INSERT INTO UserAttendance(UserName,LogDate,FirstLoginTime,LoginCount,IsOnline)
		VALUES(?,?,?,1,1)
		ON CONFLICT(UserName,LogDate) DO UPDATE SET LoginCount=LoginCount+1, IsOnline=1`,
		userName, today, clock)
}

// RecordLogout marks the user offline for today.
func RecordLogout(db *sql.DB, userName string) {
	now := cairoNow()
	today := now.Format("2006-01-02")
	clock := now.Format("15:04:05")
	_, _ = db.Exec(`UPDATE UserAttendance SET LastLogoutTime=?, IsOnline=0
		WHERE UserName=? AND LogDate=?`, clock, userName, today)
}
