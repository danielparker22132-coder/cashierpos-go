// Package store — B4: local + cloud-folder backup (§18).
//
// The "cloud" is a local folder the manager chooses (a Google Drive /
// OneDrive / Dropbox synced folder). No OAuth, no network. A cloud copy is
// only reported as successful after on-disk verification: file exists,
// size > 0, size equals the local copy, and it opens as SQLite containing
// a Users table. A missing cloud folder yields status "cloud_missing" and
// the local backup still succeeds; any other cloud failure is "cloud_error".
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/rbac"
)

// Cloud backup statuses.
const (
	CloudOK       = "ok"
	CloudMissing  = "cloud_missing"
	CloudError    = "cloud_error"
	CloudDisabled = "disabled"
)

// BackupInfo describes one backup file in either location.
type BackupInfo struct {
	Name     string
	Location string // "local" | "cloud"
	Path     string
	Size     int64
	ModTime  string
}

// BackupResult is the outcome of CreateBackup.
type BackupResult struct {
	FileName    string
	LocalPath   string
	LocalSize   int64
	CloudStatus string // ok | cloud_missing | cloud_error | disabled
	CloudPath   string
	CloudSize   int64
	Error       string // cloud error detail, if any
}

// backupFileName returns CashierDB_yyyyMMdd_HHmmss.db for now.
func backupFileName() string {
	return "CashierDB_" + Now()[:10] + "_" + strings.ReplaceAll(Now()[11:], ":", "") + ".db"
}

// R26-D-F2: backup errors reach the HTML handlers verbatim
// (renderAdminErr sets p.Error = err.Error()), and the raw OS/driver
// error carries a server filesystem path ("mkdir /…/backups: not a
// directory"). backupFail logs the raw error server-side and returns
// only the static Arabic message the client may see, mirroring
// apiBackupNow (500 generic). Callers must never wrap the raw error
// with %v/%w into a returned error or into BackupResult.Error.
func backupFail(raw error, msg string) error {
	log.Printf("backup: %s: %v", msg, raw)
	return errors.New(msg)
}

// BackupDir returns the local backups folder (beside the database file).
func (s *Store) BackupDir() (string, error) {
	if s.Path == "" || s.Path == ":memory:" {
		return "", errors.New("لا يمكن النسخ الاحتياطي لقاعدة بيانات مؤقتة (في الذاكرة)")
	}
	return filepath.Join(filepath.Dir(s.Path), "backups"), nil
}

// VerifyBackupFile checks a backup file: exists, size > 0, opens as SQLite
// and contains a Users table.
func VerifyBackupFile(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("ملف النسخة غير موجود: %s", filepath.Base(path))
	}
	if st.Size() == 0 {
		return fmt.Errorf("ملف النسخة فارغ: %s", filepath.Base(path))
	}
	db, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		return backupFail(err, "تعذّر فتح النسخة للتحقق")
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='Users'`).Scan(&n); err != nil {
		return backupFail(err, "النسخة ليست قاعدة SQLite صالحة")
	}
	if n == 0 {
		return errors.New("النسخة لا تحتوي جدول Users")
	}
	// FIX-1 L12: run PRAGMA integrity_check on a read-only copy and
	// require the single result row to be "ok".
	var result string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil {
		return backupFail(err, "تعذّر فحص سلامة النسخة")
	}
	if result != "ok" {
		return backupFail(errors.New(result), "النسخة تالفة — فحص السلامة فشل")
	}
	return nil
}

// verifyCloudCopy checks the cloud copy against the local copy.
func verifyCloudCopy(cloudPath string, localSize int64) error {
	st, err := os.Stat(cloudPath)
	if err != nil {
		return fmt.Errorf("النسخة السحابية غير موجودة بعد الكتابة")
	}
	if st.Size() == 0 {
		return errors.New("حجم النسخة السحابية صفر")
	}
	if st.Size() != localSize {
		return fmt.Errorf("حجم النسخة السحابية (%d) لا يساوي المحلية (%d)", st.Size(), localSize)
	}
	return VerifyBackupFile(cloudPath)
}

// pruneBackups keeps only the newest keep files matching CashierDB_*.db.
func pruneBackups(dir string, keep int) {
	if keep < 1 {
		keep = 5
	}
	entries, err := filepath.Glob(filepath.Join(dir, "CashierDB_*.db"))
	if err != nil {
		return
	}
	sort.Strings(entries) // timestamped names sort chronologically
	for len(entries) > keep {
		_ = os.Remove(entries[0])
		entries = entries[1:]
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// CreateBackup writes a local backup (VACUUM INTO — a consistent snapshot)
// and, when cloud backup is enabled, a copy into the manager-chosen folder
// with on-disk verification. Retention (keep N) applies to both locations.
func (s *Store) CreateBackup(ctx context.Context, user *auth.User) (BackupResult, error) {
	var res BackupResult
	if err := rbac.Require(user.Role, rbac.PermBackup); err != nil {
		return res, err
	}
	dir, err := s.BackupDir()
	if err != nil {
		return res, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return res, backupFail(err, "تعذّر إنشاء مجلد النسخ المحلي")
	}
	// Same-second runs would collide on the timestamped name (VACUUM INTO
	// refuses an existing target), so suffix _2, _3, ... when needed.
	res.FileName = backupFileName()
	for i := 2; ; i++ {
		res.LocalPath = filepath.Join(dir, res.FileName)
		if _, err := os.Stat(res.LocalPath); os.IsNotExist(err) {
			break
		}
		res.FileName = strings.TrimSuffix(backupFileName(), ".db") + "_" + strconv.Itoa(i) + ".db"
	}
	if _, err := s.DB.Exec(`VACUUM INTO ?`, res.LocalPath); err != nil {
		return res, backupFail(err, "فشل النسخ المحلي")
	}
	if err := VerifyBackupFile(res.LocalPath); err != nil {
		return res, backupFail(err, "فشل التحقق من النسخة المحلية")
	}
	st, _ := os.Stat(res.LocalPath)
	res.LocalSize = st.Size()

	cfg := s.GetCloudBackupSettings()
	keep := cfg.Keep
	if keep < 1 {
		keep = 5
	}
	pruneBackups(dir, keep)

	if !cfg.Enabled || strings.TrimSpace(cfg.Dir) == "" {
		res.CloudStatus = CloudDisabled
		_ = s.SetSetting("cloud_backup_last_status", "disabled")
	} else if fi, err := os.Stat(cfg.Dir); err != nil || !fi.IsDir() {
		// Cloud folder missing: local backup stands, status cloud_missing.
		res.CloudStatus = CloudMissing
		res.Error = "مجلد النسخ السحابي غير موجود"
		_ = s.SetSetting("cloud_backup_last_status", "missing")
	} else {
		res.CloudPath = filepath.Join(cfg.Dir, res.FileName)
		if err := copyFile(res.LocalPath, res.CloudPath); err != nil {
			res.CloudStatus = CloudError
			res.Error = backupFail(err, "فشل كتابة النسخة السحابية").Error()
			_ = s.SetSetting("cloud_backup_last_status", "error")
		} else if err := verifyCloudCopy(res.CloudPath, res.LocalSize); err != nil {
			res.CloudStatus = CloudError
			res.Error = backupFail(err, "فشل التحقق من النسخة السحابية").Error()
			_ = s.SetSetting("cloud_backup_last_status", "error")
		} else {
			cst, _ := os.Stat(res.CloudPath)
			res.CloudSize = cst.Size()
			res.CloudStatus = CloudOK
			_ = s.SetSetting("cloud_backup_last", Now())
			_ = s.SetSetting("cloud_backup_last_size", strconv.FormatInt(res.CloudSize, 10))
			_ = s.SetSetting("cloud_backup_last_status", "ok")
		}
		pruneBackups(cfg.Dir, keep)
	}
	_ = s.Audit(user.ID, user.UserName, "backup_database", "backup", nil, 0, 0, "",
		fmt.Sprintf("نسخة احتياطية %s — محلية %d بايت، سحابية: %s", res.FileName, res.LocalSize, res.CloudStatus))
	return res, nil
}

// ListBackups lists local backups and, when a cloud folder is configured,
// cloud backups (cloud first on a name tie, per §18).
func (s *Store) ListBackups(ctx context.Context, user *auth.User) ([]BackupInfo, error) {
	if err := rbac.Require(user.Role, rbac.PermBackup); err != nil {
		return nil, err
	}
	var out []BackupInfo
	if dir, err := s.BackupDir(); err == nil {
		out = append(out, listBackupDir(dir, "local")...)
	}
	if cfg := s.GetCloudBackupSettings(); strings.TrimSpace(cfg.Dir) != "" {
		out = append(out, listBackupDir(cfg.Dir, "cloud")...)
	}
	// Cloud first on a name tie, then newest first.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].Location == "cloud" && out[j].Location != "cloud"
		}
		return out[i].Name > out[j].Name
	})
	return out, nil
}

func listBackupDir(dir, location string) []BackupInfo {
	entries, err := filepath.Glob(filepath.Join(dir, "CashierDB_*.db"))
	if err != nil {
		return nil
	}
	var out []BackupInfo
	for _, p := range entries {
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		out = append(out, BackupInfo{
			Name: filepath.Base(p), Location: location, Path: p,
			Size: st.Size(), ModTime: st.ModTime().Format("2006-01-02 15:04:05"),
		})
	}
	return out
}

// RunDailyBackupIfDue performs the once-a-day automatic backup at the first
// run of the day (when cloud backup is enabled and the last successful cloud
// backup predates today). Cloud failure never blocks startup: it is reported
// as the result status. The stored cloud_backup_time is a preference only.
func (s *Store) RunDailyBackupIfDue(ctx context.Context, user *auth.User) (BackupResult, bool, error) {
	var res BackupResult
	cfg := s.GetCloudBackupSettings()
	if !cfg.Enabled {
		return res, false, nil
	}
	last := cfg.Last
	if last != "" && strings.HasPrefix(last, Today()) {
		return res, false, nil
	}
	res, err := s.CreateBackup(ctx, user)
	if err != nil {
		return res, false, err
	}
	return res, true, nil
}

// RestoreBackup verifies a backup file first, then replaces the live database
// file with it and reopens the connection. If reopening fails the caller is
// told to restart the program (the file on disk is already replaced).
func (s *Store) RestoreBackup(ctx context.Context, user *auth.User, location, name string) error {
	if err := rbac.Require(user.Role, rbac.PermRestore); err != nil {
		return err
	}
	if s.Path == "" || s.Path == ":memory:" {
		return errors.New("لا يمكن الاستعادة على قاعدة بيانات مؤقتة (في الذاكرة)")
	}
	if strings.Contains(name, "/") || strings.Contains(name, `\`) || strings.Contains(name, "..") {
		return errors.New("اسم النسخة غير صحيح")
	}
	if !strings.HasPrefix(name, "CashierDB_") || !strings.HasSuffix(name, ".db") {
		return errors.New("اسم النسخة غير صحيح")
	}
	var src string
	switch location {
	case "local":
		dir, err := s.BackupDir()
		if err != nil {
			return err
		}
		src = filepath.Join(dir, name)
	case "cloud":
		cfg := s.GetCloudBackupSettings()
		if strings.TrimSpace(cfg.Dir) == "" {
			return errors.New("مجلد النسخ السحابي غير مضبوط")
		}
		src = filepath.Join(cfg.Dir, name)
	default:
		return errors.New("موقع النسخة غير معروف (local أو cloud)")
	}
	if err := VerifyBackupFile(src); err != nil {
		return backupFail(err, "التحقق من النسخة فشل — لن تتم الاستعادة")
	}
	if err := s.DB.Close(); err != nil {
		return backupFail(err, "تعذّر إغلاق الاتصال الحالي")
	}
	// FIX-1 M8: atomic replacement — copy to a temp file in the same
	// directory, then rename over the live file, so a crash mid-copy
	// can never leave a half-written live database.
	tmpPath := s.Path + ".restore-tmp"
	if err := copyFile(src, tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		// Connection is closed; reopen against whatever is on disk so the
		// store stays usable, then report the failure.
		_ = s.reopen()
		return backupFail(err, "فشل استبدال ملف قاعدة البيانات — أعد تشغيل البرنامج")
	}
	if err := os.Rename(tmpPath, s.Path); err != nil {
		_ = os.Remove(tmpPath)
		_ = s.reopen()
		return backupFail(err, "فشل استبدال ملف قاعدة البيانات — أعد تشغيل البرنامج")
	}
	if err := s.reopen(); err != nil {
		return errors.New("تم استبدال قاعدة البيانات لكن تعذّر إعادة فتح الاتصال — أعد تشغيل البرنامج")
	}
	// The restored DB carries its own invoice high-water mark (D-06).
	_ = s.Audit(user.ID, user.UserName, "restore_database", "backup", nil, 0, 0, "",
		"استعادة نسخة احتياطية: "+name+" ("+location+")")
	return nil
}

// reopen reopens the SQLite connection after a restore.
// FIX-1 M8: run migrate() + seed() after reopening — a backup from an
// older version may lack a newer column, which would leave the schema
// incomplete (migrate adds missing columns; seed restores the first-run
// admin + cash customer when absent).
func (s *Store) reopen() error {
	db, err := sql.Open("sqlite", s.Path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return err
	}
	s.DB = db
	if err := s.migrate(); err != nil {
		return err
	}
	return s.seed()
}
