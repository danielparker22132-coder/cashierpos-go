// Package web_test — FIX-26 lane D (R26-D-F2): the HTML backup handlers
// used to echo CreateBackup/RestoreBackup errors verbatim, including a
// raw OS error with a server filesystem path ("تعذّر إنشاء مجلد النسخ
// المحلي: mkdir /…/backups: not a directory"). The store now returns
// static Arabic on OS/driver failure (raw error logged server-side),
// so every HTML backup shape must render the static text only — no
// server path, no OS error text — while a control backup still
// succeeds normally.
package web_test

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/store"
)

// fix26DBackupLeaks fails if the HTML body carries any raw OS/driver
// text or server path. The scratch DB directory is passed in so the
// exact temp path is a checked leak marker, not just a generic one.
func fix26DBackupLeaks(t *testing.T, what, body, dbDir string) {
	t.Helper()
	for _, leak := range []string{dbDir, "mkdir", "not a directory", "is a directory",
		"SQL logic error", "file is not a database", "database disk image is malformed",
		"no such file or directory", "/backups/", "/cloud/"} {
		if strings.Contains(body, leak) {
			t.Fatalf("%s: HTML leaks %q in body %q", what, leak, truncate(body, 600))
		}
	}
}

// TestFix26D_BackupNowBlockedDirHTML is the R26-D-F2 live repro: a file
// blocking the backups dir, then POST /backup/now as manager. The 200
// page must carry exactly the static Arabic text; the control (blocker
// removed) must create a real backup file and list it.
func TestFix26D_BackupNowBlockedDirHTML(t *testing.T) {
	e := newEnv(t, true)
	e.seedUsers()
	mgr := e.managerClient()
	dir := filepath.Dir(e.store.Path)
	if err := os.WriteFile(filepath.Join(dir, "backups"), []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("plant backups file: %v", err)
	}
	code, body := e.postForm(mgr, "/backup/now", url.Values{})
	if code != http.StatusOK {
		t.Fatalf("POST /backup/now blocked: status = %d, want 200 (body %q)", code, truncate(body, 300))
	}
	if !strings.Contains(body, "تعذّر إنشاء مجلد النسخ المحلي") {
		t.Fatalf("POST /backup/now blocked: body lacks the static Arabic error (body %q)", truncate(body, 600))
	}
	fix26DBackupLeaks(t, "POST /backup/now blocked", body, dir)

	// Control: remove the blocker, backup succeeds and is listed.
	if err := os.Remove(filepath.Join(dir, "backups")); err != nil {
		t.Fatalf("remove blocker: %v", err)
	}
	code, body = e.postForm(mgr, "/backup/now", url.Values{})
	if code != http.StatusOK {
		t.Fatalf("POST /backup/now control: status = %d, want 200 (body %q)", code, truncate(body, 300))
	}
	if !strings.Contains(body, "تم إنشاء النسخة CashierDB_") {
		t.Fatalf("POST /backup/now control: body lacks the success notice (body %q)", truncate(body, 600))
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "backups", "CashierDB_*.db"))
	if len(matches) == 0 {
		t.Fatal("POST /backup/now control: no CashierDB_*.db file under backups/")
	}
	code, body = e.get(mgr, "/backup")
	if code != http.StatusOK || !strings.Contains(body, filepath.Base(matches[0])) {
		t.Fatalf("GET /backup control: status = %d, body lists backup = %v (body %q)",
			code, strings.Contains(body, filepath.Base(matches[0])), truncate(body, 300))
	}
}

// TestFix26D_BackupRestoreCorruptHTML covers the restore leg: a corrupt
// backup file, then POST /backup/restore as manager. The 200 page must
// carry exactly the static Arabic text, no path, no OS/driver text.
// A good backup restores fine first (control).
func TestFix26D_BackupRestoreCorruptHTML(t *testing.T) {
	e := newEnv(t, true)
	e.seedUsers()
	mgr := e.managerClient()
	dir := filepath.Dir(e.store.Path)
	// Create a real backup over HTTP.
	code, body := e.postForm(mgr, "/backup/now", url.Values{})
	if code != http.StatusOK || !strings.Contains(body, "تم إنشاء النسخة CashierDB_") {
		t.Fatalf("setup backup: status = %d (body %q)", code, truncate(body, 300))
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "backups", "CashierDB_*.db"))
	if len(matches) == 0 {
		t.Fatal("setup backup: no CashierDB_*.db file under backups/")
	}
	name := filepath.Base(matches[0])
	// Control: restoring the good backup succeeds.
	code, body = e.postForm(mgr, "/backup/restore", url.Values{
		"confirm": {"استعادة"}, "location": {"local"}, "name": {name},
	})
	if code != http.StatusOK || !strings.Contains(body, "تمت الاستعادة من "+name) {
		t.Fatalf("restore good backup: status = %d, want 200 with success notice (body %q)", code, truncate(body, 600))
	}
	// Corrupt the backup file, then restore it.
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	data[100] ^= 0xFF
	if err := os.WriteFile(matches[0], data, 0o600); err != nil {
		t.Fatal(err)
	}
	code, body = e.postForm(mgr, "/backup/restore", url.Values{
		"confirm": {"استعادة"}, "location": {"local"}, "name": {name},
	})
	if code != http.StatusOK {
		t.Fatalf("POST /backup/restore corrupt: status = %d, want 200 (body %q)", code, truncate(body, 300))
	}
	if !strings.Contains(body, "التحقق من النسخة فشل — لن تتم الاستعادة") {
		t.Fatalf("POST /backup/restore corrupt: body lacks the static Arabic error (body %q)", truncate(body, 600))
	}
	fix26DBackupLeaks(t, "POST /backup/restore corrupt", body, dir)
}

// TestFix26D_BackupCloudCopyFailureNoticeHTML covers the cloud-copy leg:
// the cloud copy fails (a directory stands at the copy target path),
// so the success Notice carries res.Error — it must be the static
// Arabic text only, never the raw copy error with its cloud path.
func TestFix26D_BackupCloudCopyFailureNoticeHTML(t *testing.T) {
	e := newEnv(t, true)
	e.seedUsers()
	mgr := e.managerClient()
	dir := filepath.Dir(e.store.Path)
	cloudDir := filepath.Join(dir, "cloud")
	if err := os.MkdirAll(cloudDir, 0o755); err != nil {
		t.Fatal(err)
	}
	admin := &auth.User{ID: 1, UserName: "admin", Role: "admin"}
	if err := e.store.SaveCloudBackupSettings(context.Background(), admin, store.CloudBackupSettings{
		Enabled: true, Dir: cloudDir, Time: "03:00", Keep: 5,
	}); err != nil {
		t.Fatal(err)
	}
	// Plant a directory at the exact cloud copy target path. The name
	// is second-precision (Africa/Cairo, like store.Now()), so retry
	// with a fresh name if the second rolls over between planting and
	// the POST (a miss only answers cloud ok for that attempt).
	loc, err := time.LoadLocation("Africa/Cairo")
	if err != nil {
		loc = time.Local
	}
	var body string
	hit := false
	for attempt := 0; attempt < 8 && !hit; attempt++ {
		now := time.Now().In(loc).Format("2006-01-02 15:04:05")
		name := "CashierDB_" + now[:10] + "_" + strings.ReplaceAll(now[11:], ":", "") + ".db"
		_ = os.Mkdir(filepath.Join(cloudDir, name), 0o755)
		code, b := e.postForm(mgr, "/backup/now", url.Values{})
		if code != http.StatusOK {
			t.Fatalf("POST /backup/now cloud attempt: status = %d, want 200 (body %q)", code, truncate(b, 300))
		}
		body = b
		if strings.Contains(b, "فشل كتابة النسخة السحابية") {
			hit = true
		}
	}
	if !hit {
		t.Fatal("could not land a cloud-copy failure in 8 attempts (second kept rolling over)")
	}
	if !strings.Contains(body, "تم إنشاء النسخة CashierDB_") {
		t.Fatalf("cloud failure: body lacks the local-success notice (body %q)", truncate(body, 600))
	}
	// The Notice must carry exactly the static text in parentheses.
	// NOTE: the page also renders the manager's own configured cloud
	// folder ({{.Data.Cloud.Dir}} in backup_list.html) — that is the
	// folder the manager configured himself, shown back to him, not a
	// raw OS error; it is out of scope for R26-D-F2, so the leak check
	// here targets the raw copy error only, not the bare temp dir.
	if !strings.Contains(body, "(فشل كتابة النسخة السحابية)") {
		t.Fatalf("cloud failure: body lacks the static Notice (body %q)", truncate(body, 600))
	}
	for _, leak := range []string{"فشل كتابة النسخة السحابية: ", "is a directory", "not a directory"} {
		if strings.Contains(body, leak) {
			t.Fatalf("cloud failure: Notice leaks raw copy error %q in body %q", leak, truncate(body, 600))
		}
	}
}
