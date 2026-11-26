// Package store — B4: settings (store profile, auto-lock, dark mode,
// delivery zones, scale settings, cloud-backup settings, DB reset).
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/money"
	"cashierpos-go/internal/rbac"
)

// StoreProfile is the store identity (T-02).
type StoreProfile struct {
	Name    string
	Phone   string
	Address string
}

// GetStoreProfile reads the store profile.
func (s *Store) GetStoreProfile() StoreProfile {
	return StoreProfile{
		Name:    s.GetSetting("store_name"),
		Phone:   s.GetSetting("store_phone"),
		Address: s.GetSetting("store_address"),
	}
}

// SaveStoreProfile saves the store profile. The store name is required.
func (s *Store) SaveStoreProfile(ctx context.Context, user *auth.User, p StoreProfile) error {
	if err := rbac.Require(user.Role, rbac.PermAccessSettings); err != nil {
		return err
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return errors.New("اسم المحل مطلوب")
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		if err := SetSettingTx(tx, "store_name", p.Name); err != nil {
			return err
		}
		if err := SetSettingTx(tx, "store_phone", strings.TrimSpace(p.Phone)); err != nil {
			return err
		}
		if err := SetSettingTx(tx, "store_address", strings.TrimSpace(p.Address)); err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "save_store_profile", "settings", nil, 0, 0, "",
			"حفظ ملف المحل: "+p.Name)
	})
}

// GetAutoLockMinutes reads the auto-lock timeout (0 = disabled, default 15).
func (s *Store) GetAutoLockMinutes() int {
	v, err := strconv.Atoi(strings.TrimSpace(s.GetSetting("auto_lock_minutes")))
	if err != nil {
		return 15
	}
	return v
}

// SaveAutoLockMinutes saves the auto-lock timeout (0 = disabled, max 600).
func (s *Store) SaveAutoLockMinutes(ctx context.Context, user *auth.User, minutes int) error {
	if err := rbac.Require(user.Role, rbac.PermAccessSettings); err != nil {
		return err
	}
	if minutes < 0 || minutes > 600 {
		return errors.New("مهلة القفل التلقائي بين 0 (معطل) و 600 دقيقة")
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		if err := SetSettingTx(tx, "auto_lock_minutes", strconv.Itoa(minutes)); err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "save_auto_lock", "settings", nil, 0, 0, "",
			fmt.Sprintf("مهلة القفل التلقائي: %d دقيقة", minutes))
	})
}

// GetDarkMode reports whether dark mode is on.
func (s *Store) GetDarkMode() bool { return s.GetSetting("dark_mode") == "1" }

// --- Auto-lock session activity (FIX-3 M2) ---

// sessionTimeFormat is the timestamp format used by Sessions.CreatedAt
// and Sessions.LastActivity ("2006-01-02 15:04:05", server-local time).
const sessionTimeFormat = "2006-01-02 15:04:05"

// SessionLastActivity returns the stored last-activity stamp for a session
// token, falling back to the session creation time. ok=false when the token
// has no session row or no usable timestamp.
func (s *Store) SessionLastActivity(token string) (time.Time, bool) {
	var lastAct, created sql.NullString
	err := s.DB.QueryRow(`SELECT LastActivity, CreatedAt FROM Sessions WHERE Token=?`, token).
		Scan(&lastAct, &created)
	if err != nil {
		return time.Time{}, false
	}
	for _, raw := range []sql.NullString{lastAct, created} {
		if !raw.Valid || strings.TrimSpace(raw.String) == "" {
			continue
		}
		if t, err := time.ParseInLocation(sessionTimeFormat, strings.TrimSpace(raw.String), time.Local); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// TouchSessionActivity records t as the session's last activity.
func (s *Store) TouchSessionActivity(token string, t time.Time) {
	_, _ = s.DB.Exec(`UPDATE Sessions SET LastActivity=? WHERE Token=?`,
		t.Format(sessionTimeFormat), token)
}

// AutoLockOverride is one user's per-user auto-lock override (FIX-3 M3).
type AutoLockOverride struct {
	UserName string
	Minutes  int
}

// ListAutoLockOverrides returns every valid per-user auto-lock override,
// ordered by username. Rows with unparseable or out-of-range values are
// skipped — the effective-timeout reader ignores them too.
func (s *Store) ListAutoLockOverrides() []AutoLockOverride {
	rows, err := s.DB.Query(`SELECT Key, Value FROM AppSettings
		WHERE Key LIKE 'auto_lock_minutes:%' ORDER BY Key`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []AutoLockOverride
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			continue
		}
		name := strings.TrimPrefix(k, "auto_lock_minutes:")
		if name == "" || name == k {
			continue
		}
		mins, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || mins < 0 || mins > 600 {
			continue
		}
		out = append(out, AutoLockOverride{UserName: name, Minutes: mins})
	}
	return out
}

// SetDarkMode saves the dark-mode flag (applies immediately on next render).
func (s *Store) SetDarkMode(ctx context.Context, user *auth.User, on bool) error {
	if err := rbac.Require(user.Role, rbac.PermAccessSettings); err != nil {
		return err
	}
	v := "0"
	if on {
		v = "1"
	}
	return s.SetSetting("dark_mode", v)
}

// --- Delivery zones (Z-01..Z-07) ---

// DeliveryZone is one zone. Fee is stored in the JSON as integer CENTS
// (Go money rule: all money is cents); "ناجي حمادي" is always fee 0.
type DeliveryZone struct {
	Name   string      `json:"name"`
	Fee    money.Cents `json:"fee"` // cents
	Active bool        `json:"active"`
}

// FeeEGP renders the fee in whole EGP for display.
func (z DeliveryZone) FeeEGP() money.Cents { return money.Cents(z.Fee) }

// NagHammadiZone is the free home zone (USER RULE G5 — never fee 20).
const NagHammadiZone = "ناجي حمادي"

// GetDeliveryZones parses the delivery_zones JSON setting.
// Malformed JSON is treated as no zones (delivery stays closed, never open-to-all).
func (s *Store) GetDeliveryZones() []DeliveryZone {
	raw := strings.TrimSpace(s.GetSetting("delivery_zones"))
	if raw == "" {
		return nil
	}
	var zones []DeliveryZone
	if err := json.Unmarshal([]byte(raw), &zones); err != nil {
		return nil
	}
	out := zones[:0]
	for _, z := range zones {
		z.Name = strings.TrimSpace(z.Name)
		if z.Name == "" {
			continue
		}
		if z.Fee < 0 {
			z.Fee = 0
		}
		out = append(out, z)
	}
	return out
}

// ActiveDeliveryZones returns only the active zones (public list, Z-04).
func (s *Store) ActiveDeliveryZones() []DeliveryZone {
	var out []DeliveryZone
	for _, z := range s.GetDeliveryZones() {
		if z.Active {
			out = append(out, z)
		}
	}
	return out
}

// ZoneFeeForAddress returns the fee (cents) for an address that contains an
// active zone name (case-insensitive substring, Z-03). ok=false means
// delivery checkout is closed for that address.
func (s *Store) ZoneFeeForAddress(address string) (fee money.Cents, ok bool) {
	address = strings.ToLower(strings.TrimSpace(address))
	for _, z := range s.ActiveDeliveryZones() {
		if strings.Contains(address, strings.ToLower(z.Name)) {
			return money.Cents(z.Fee), true
		}
	}
	return 0, false
}

// SaveDeliveryZones persists zones (Manager/Admin only, audited).
// The "ناجي حمادي" zone is always kept with fee 0 and active (G5):
// if present its fee is forced to 0; if absent it is added.
func (s *Store) SaveDeliveryZones(ctx context.Context, user *auth.User, zones []DeliveryZone) error {
	if err := rbac.Require(user.Role, rbac.PermManageZones); err != nil {
		return err
	}
	clean := make([]DeliveryZone, 0, len(zones)+1)
	seen := map[string]bool{}
	hasNag := false
	for _, z := range zones {
		z.Name = strings.TrimSpace(z.Name)
		if z.Name == "" {
			continue
		}
		if seen[z.Name] {
			return fmt.Errorf("منطقة مكررة: %s", z.Name)
		}
		seen[z.Name] = true
		if z.Fee < 0 {
			return fmt.Errorf("رسوم منطقة %s لا يمكن أن تكون سالبة", z.Name)
		}
		if z.Name == NagHammadiZone {
			z.Fee = 0
			z.Active = true
			hasNag = true
		}
		clean = append(clean, z)
	}
	if !hasNag {
		clean = append(clean, DeliveryZone{Name: NagHammadiZone, Fee: 0, Active: true})
	}
	raw, err := json.Marshal(clean)
	if err != nil {
		return err
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		if err := SetSettingTx(tx, "delivery_zones", string(raw)); err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "save_delivery_zones", "settings", nil, 0, 0, "",
			fmt.Sprintf("حفظ مناطق التوصيل (%d منطقة)", len(clean)))
	})
}

// --- Scale settings ---

// ScaleConfig is one ScaleSettings row (ID=1).
type ScaleConfig struct {
	IsEnabled          bool
	BarcodePrefix      string
	ProductCodeLength  int
	ValueLength        int
	BarcodeMode        string // WeightEmbedded | PriceEmbedded
	HasCheckDigit      bool
	WeightUnit         string // Kilogram | Gram
	ValueDecimalPlaces int
	Notes              string
}

// ValidateScaleConfig checks prefix (digits only), lengths 1-8, decimals 0-5,
// and the mode / weight-unit values.
func ValidateScaleConfig(cfg ScaleConfig) error {
	if cfg.BarcodePrefix == "" {
		return errors.New("بادئة باركود الميزان مطلوبة")
	}
	for _, r := range cfg.BarcodePrefix {
		if !unicode.IsDigit(r) {
			return errors.New("بادئة باركود الميزان أرقام فقط")
		}
	}
	if cfg.ProductCodeLength < 1 || cfg.ProductCodeLength > 8 {
		return errors.New("طول كود المنتج بين 1 و 8")
	}
	if cfg.ValueLength < 1 || cfg.ValueLength > 8 {
		return errors.New("طول القيمة بين 1 و 8")
	}
	if cfg.ValueDecimalPlaces < 0 || cfg.ValueDecimalPlaces > 5 {
		return errors.New("الخانات العشرية بين 0 و 5")
	}
	if cfg.BarcodeMode != "WeightEmbedded" && cfg.BarcodeMode != "PriceEmbedded" {
		return errors.New("وضع الميزان غير صحيح (وزن أو سعر)")
	}
	if cfg.WeightUnit != "Kilogram" && cfg.WeightUnit != "Gram" {
		return errors.New("وحدة الوزن غير صحيحة (كيلو أو جرام)")
	}
	return nil
}

// SaveScaleConfig validates and saves the scale config (ID=1 row).
// Reading uses B1's GetScaleSettings(ctx) (domain_products.go) and barcode
// testing uses B1's DecodeScaleBarcode(ctx, code); this is the write side.
func (s *Store) SaveScaleConfig(ctx context.Context, user *auth.User, cfg ScaleConfig) error {
	if err := rbac.Require(user.Role, rbac.PermScaleSettings); err != nil {
		return err
	}
	if err := ValidateScaleConfig(cfg); err != nil {
		return err
	}
	enabled, check := 0, 0
	if cfg.IsEnabled {
		enabled = 1
	}
	if cfg.HasCheckDigit {
		check = 1
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO ScaleSettings(ID,IsEnabled,BarcodePrefix,ProductCodeLength,
			ValueLength,BarcodeMode,HasCheckDigit,WeightUnit,ValueDecimalPlaces,Notes)
			VALUES(1,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(ID) DO UPDATE SET IsEnabled=excluded.IsEnabled,BarcodePrefix=excluded.BarcodePrefix,
			ProductCodeLength=excluded.ProductCodeLength,ValueLength=excluded.ValueLength,
			BarcodeMode=excluded.BarcodeMode,HasCheckDigit=excluded.HasCheckDigit,
			WeightUnit=excluded.WeightUnit,ValueDecimalPlaces=excluded.ValueDecimalPlaces,
			Notes=excluded.Notes`,
			enabled, cfg.BarcodePrefix, cfg.ProductCodeLength, cfg.ValueLength, cfg.BarcodeMode,
			check, cfg.WeightUnit, cfg.ValueDecimalPlaces, cfg.Notes)
		if err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "save_scale_settings", "settings", nil, 0, 0, "",
			"حفظ إعدادات الميزان")
	})
}

// --- Cloud backup settings (§18: stored preferences only) ---

// CloudBackupSettings are the §18 preferences.
type CloudBackupSettings struct {
	Enabled    bool
	Dir        string
	Time       string // "03:00" — stored preference only; the daily run fires at first startup of the day
	Keep       int    // default 5
	Last       string // ISO timestamp of last successful cloud backup
	LastSize   int64
	LastStatus string // ok | missing | error
}

// GetCloudBackupSettings reads the §18 preferences.
func (s *Store) GetCloudBackupSettings() CloudBackupSettings {
	c := CloudBackupSettings{Time: "03:00", Keep: 5}
	c.Enabled = s.GetSetting("cloud_backup_enabled") == "1"
	c.Dir = s.GetSetting("cloud_backup_dir")
	if t := strings.TrimSpace(s.GetSetting("cloud_backup_time")); t != "" {
		c.Time = t
	}
	if k, err := strconv.Atoi(strings.TrimSpace(s.GetSetting("cloud_backup_keep"))); err == nil && k >= 1 {
		c.Keep = k
	}
	c.Last = s.GetSetting("cloud_backup_last")
	if sz, err := strconv.ParseInt(strings.TrimSpace(s.GetSetting("cloud_backup_last_size")), 10, 64); err == nil {
		c.LastSize = sz
	}
	c.LastStatus = s.GetSetting("cloud_backup_last_status")
	return c
}

// SaveCloudBackupSettings saves the §18 preferences (time "HH:MM", keep >= 1).
func (s *Store) SaveCloudBackupSettings(ctx context.Context, user *auth.User, c CloudBackupSettings) error {
	if err := rbac.Require(user.Role, rbac.PermBackup); err != nil {
		return err
	}
	if _, err := parseHHMM(c.Time); err != nil {
		return err
	}
	if c.Keep < 1 || c.Keep > 100 {
		return errors.New("عدد النسخ المحتفظ بها بين 1 و 100")
	}
	enabled := "0"
	if c.Enabled {
		enabled = "1"
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		for k, v := range map[string]string{
			"cloud_backup_enabled": enabled,
			"cloud_backup_dir":     strings.TrimSpace(c.Dir),
			"cloud_backup_time":    c.Time,
			"cloud_backup_keep":    strconv.Itoa(c.Keep),
		} {
			if err := SetSettingTx(tx, k, v); err != nil {
				return err
			}
		}
		return AuditTx(tx, user.ID, user.UserName, "save_cloud_backup_settings", "settings", nil, 0, 0, "",
			"حفظ إعدادات النسخ السحابي")
	})
}

func parseHHMM(t string) (string, error) {
	t = strings.TrimSpace(t)
	if len(t) != 5 || t[2] != ':' {
		return "", errors.New("وقت النسخ اليومي بصيغة HH:MM (مثال 03:00)")
	}
	h, err1 := strconv.Atoi(t[:2])
	m, err2 := strconv.Atoi(t[3:])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return "", errors.New("وقت النسخ اليومي بصيغة HH:MM (مثال 03:00)")
	}
	return t, nil
}

// --- Database reset (T-06) ---

// ResetConfirmation is the exact text the admin must type to confirm a reset.
const ResetConfirmation = "إعادة تعيين"

// ResetDatabase drops every table and re-initialises the database
// (default admin + cash customer are re-seeded). The attempt is audited
// BEFORE the drop; the audit row itself is lost with the reset, which is
// inherent to a full reset. Sessions are wiped, so the caller must log in
// again as admin/admin123 afterwards.
func (s *Store) ResetDatabase(ctx context.Context, user *auth.User, confirmation string) error {
	if err := rbac.Require(user.Role, rbac.PermResetDB); err != nil {
		return err
	}
	if strings.TrimSpace(confirmation) != ResetConfirmation {
		return fmt.Errorf("للتأكيد اكتب %q في حقل التأكيد", ResetConfirmation)
	}
	// Audit first (best effort — the row is dropped with the tables).
	_ = s.Audit(user.ID, user.UserName, "reset_database", "settings", nil, 0, 0, "",
		"إعادة تعيين قاعدة البيانات بواسطة "+user.UserName)
	rows, err := s.DB.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return err
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		tables = append(tables, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, t := range tables {
		if _, err := s.DB.Exec(`DROP TABLE IF EXISTS "` + strings.ReplaceAll(t, `"`, `""`) + `"`); err != nil {
			return err
		}
	}
	if err := s.migrate(); err != nil {
		return err
	}
	return s.seed()
}
