// Package store owns the SQLite database: schema, seeding, settings,
// audit logging and transaction helpers. Domain logic lives in
// domain_*.go files as methods on *Store.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Store wraps the single SQLite connection (MaxOpenConns(1) serialises writes).
type Store struct {
	DB   *sql.DB
	Path string
}

// Open opens (creating if needed) the database at path, applies the schema
// and seeds the first-run admin + cash customer.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{DB: db, Path: path}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.seed(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// OpenTest opens an in-memory database for tests.
func OpenTest() (*Store, error) {
	return Open(":memory:")
}

func (s *Store) Close() error { return s.DB.Close() }

// Tx runs fn inside a single transaction (rollback on error).
func (s *Store) Tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Today returns the current date in Africa/Cairo as YYYY-MM-DD.
func Today() string { return time.Now().In(cairoLoc()).Format("2006-01-02") }

// Now returns the current local timestamp as "2006-01-02 15:04:05".
func Now() string { return time.Now().In(cairoLoc()).Format("2006-01-02 15:04:05") }

func cairoLoc() *time.Location {
	loc, err := time.LoadLocation("Africa/Cairo")
	if err != nil {
		return time.Local
	}
	return loc
}

// GetSetting reads a key from AppSettings ("" if missing).
func (s *Store) GetSetting(key string) string {
	var v string
	err := s.DB.QueryRow(`SELECT Value FROM AppSettings WHERE Key=?`, key).Scan(&v)
	if err != nil {
		return ""
	}
	return v
}

// GetSettingTx is GetSetting inside a transaction.
func GetSettingTx(tx *sql.Tx, key string) string {
	var v string
	err := tx.QueryRow(`SELECT Value FROM AppSettings WHERE Key=?`, key).Scan(&v)
	if err != nil {
		return ""
	}
	return v
}

// SetSetting upserts a key in AppSettings.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.DB.Exec(`INSERT INTO AppSettings(Key,Value) VALUES(?,?)
		ON CONFLICT(Key) DO UPDATE SET Value=excluded.Value`, key, value)
	return err
}

// SetSettingTx is SetSetting inside a transaction.
func SetSettingTx(tx *sql.Tx, key, value string) error {
	_, err := tx.Exec(`INSERT INTO AppSettings(Key,Value) VALUES(?,?)
		ON CONFLICT(Key) DO UPDATE SET Value=excluded.Value`, key, value)
	return err
}

// Audit writes one AuditLog row. Call inside the same transaction as the
// action it records whenever possible.
func AuditTx(tx *sql.Tx, userID int64, userName, actionType, category string, invoiceNumber *int64, invoiceID, customerID int64, customerName, notes string) error {
	_, err := tx.Exec(`INSERT INTO AuditLog(UserID,UserName,ActionType,ActionCategory,
		InvoiceNumber,InvoiceID,CustomerID,CustomerName,Notes,LogDate)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		userID, userName, actionType, category, invoiceNumber, nullableInt(invoiceID),
		nullableInt(customerID), customerName, notes, Now())
	return err
}

func nullableInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// Audit is the non-transactional convenience wrapper.
func (s *Store) Audit(userID int64, userName, actionType, category string, invoiceNumber *int64, invoiceID, customerID int64, customerName, notes string) error {
	return s.Tx(context.Background(), func(tx *sql.Tx) error {
		return AuditTx(tx, userID, userName, actionType, category, invoiceNumber, invoiceID, customerID, customerName, notes)
	})
}

func sha256Hex(pw string) string {
	h := sha256.Sum256([]byte(pw))
	return hex.EncodeToString(h[:])
}

// seed creates the first-run admin (admin/admin123, must change password),
// the default cash customer, and default settings. Idempotent.
func (s *Store) seed() error {
	return s.Tx(context.Background(), func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM Users`).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := tx.Exec(`INSERT INTO Users(UserName,Password,FullName,Role,IsActive,MustChangePassword,CreatedDate)
				VALUES('admin',?, 'مدير النظام','admin',1,1,?)`, sha256Hex("admin123"), Today()); err != nil {
				return fmt.Errorf("seed admin: %w", err)
			}
		}
		if err := tx.QueryRow(`SELECT COUNT(*) FROM Customers WHERE CustomerID=1`).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := tx.Exec(`INSERT INTO Customers(CustomerID,CustomerName,Phone,Address,Balance,CreditLimit,IsActive,CreatedDate)
				VALUES(1,'عميل نقدي','0000000000','',0,0,1,?)`, Today()); err != nil {
				return fmt.Errorf("seed cash customer: %w", err)
			}
		}
		defaults := map[string]string{
			"store_name":                    "كاشير بلس",
			"store_phone":                   "",
			"store_address":                 "ناجي حمادي",
			"auto_lock_minutes":             "15",
			"dark_mode":                     "0",
			"invoice_number_high_water":     "0",
			"delivery_zones":                `[{"name":"ناجي حمادي","fee":0,"active":true}]`,
			"cloud_backup_enabled":          "0",
			"cloud_backup_dir":              "",
			"cloud_backup_time":             "03:00",
			"cloud_backup_keep":             "5",
			"cloud_backup_last":             "",
			"cloud_backup_last_size":        "",
			"cloud_backup_last_status":      "",
			"alert_threshold_cancel_amount": "0",
			"alert_threshold_discount_pct":  "0",
			"alert_threshold_return_amount": "0",
		}
		for k, v := range defaults {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO AppSettings(Key,Value) VALUES(?,?)`, k, v); err != nil {
				return err
			}
		}
		return nil
	})
}
