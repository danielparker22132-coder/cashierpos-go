package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cashierpos-go/internal/auth"
)

func openFileStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "cashierpos.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dir
}

func TestBackupLocalAndCloudMissing(t *testing.T) {
	s, dir := openFileStore(t)
	ctx := context.Background()
	admin := testAdmin()

	// Cloud disabled: local backup succeeds, cloud status disabled.
	res, err := s.CreateBackup(ctx, admin)
	if err != nil {
		t.Fatalf("CreateBackup (cloud disabled): %v", err)
	}
	if res.CloudStatus != CloudDisabled {
		t.Fatalf("cloud status = %q, want disabled", res.CloudStatus)
	}
	if res.LocalSize <= 0 {
		t.Fatal("local backup size should be > 0")
	}
	if _, err := os.Stat(res.LocalPath); err != nil {
		t.Fatalf("local backup file missing: %v", err)
	}
	if err := VerifyBackupFile(res.LocalPath); err != nil {
		t.Fatalf("VerifyBackupFile local: %v", err)
	}

	// Cloud enabled but folder missing: local stands, cloud_missing.
	if err := s.SaveCloudBackupSettings(ctx, admin, CloudBackupSettings{
		Enabled: true, Dir: filepath.Join(dir, "no-such-cloud"), Time: "03:00", Keep: 5,
	}); err != nil {
		t.Fatal(err)
	}
	res, err = s.CreateBackup(ctx, admin)
	if err != nil {
		t.Fatalf("CreateBackup (cloud missing): %v", err)
	}
	if res.CloudStatus != CloudMissing {
		t.Fatalf("cloud status = %q, want cloud_missing", res.CloudStatus)
	}
	if got := s.GetCloudBackupSettings().LastStatus; got != "missing" {
		t.Fatalf("last_status = %q, want missing", got)
	}
	if _, err := os.Stat(res.LocalPath); err != nil {
		t.Fatal("local backup must still exist when cloud is missing")
	}
}

func TestBackupCloudVerifyListRestore(t *testing.T) {
	s, dir := openFileStore(t)
	ctx := context.Background()
	admin := testAdmin()
	cloudDir := filepath.Join(dir, "cloud")
	if err := os.MkdirAll(cloudDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCloudBackupSettings(ctx, admin, CloudBackupSettings{
		Enabled: true, Dir: cloudDir, Time: "03:00", Keep: 5,
	}); err != nil {
		t.Fatal(err)
	}
	res, err := s.CreateBackup(ctx, admin)
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	if res.CloudStatus != CloudOK {
		t.Fatalf("cloud status = %q (%s), want ok", res.CloudStatus, res.Error)
	}
	if res.CloudSize != res.LocalSize || res.CloudSize <= 0 {
		t.Fatalf("cloud size %d vs local %d", res.CloudSize, res.LocalSize)
	}
	if err := VerifyBackupFile(res.CloudPath); err != nil {
		t.Fatalf("VerifyBackupFile cloud: %v", err)
	}
	if got := s.GetCloudBackupSettings(); got.Last == "" || got.LastStatus != "ok" || got.LastSize != res.CloudSize {
		t.Fatalf("cloud last settings = %+v", got)
	}

	// List shows both locations (cloud first on a name tie).
	list, err := s.ListBackups(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	var localN, cloudN int
	for _, b := range list {
		if b.Name == res.FileName {
			if b.Location == "local" {
				localN++
			} else if b.Location == "cloud" {
				cloudN++
			}
		}
	}
	if localN != 1 || cloudN != 1 {
		t.Fatalf("backup list for %s: local=%d cloud=%d", res.FileName, localN, cloudN)
	}

	// Restore: add a user after the backup, restore, user is gone.
	mustCreateUser(t, s, "tempuser", "cashier")
	if err := s.RestoreBackup(ctx, admin, "local", res.FileName); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM Users WHERE UserName='tempuser'`).Scan(&n)
	if n != 0 {
		t.Fatal("restored database must not contain the post-backup user")
	}
	// Restore refuses a bad name / missing file.
	if err := s.RestoreBackup(ctx, admin, "local", "../evil.db"); err == nil {
		t.Fatal("path traversal in restore name should be refused")
	}
	if err := s.RestoreBackup(ctx, admin, "local", "CashierDB_19990101_000000.db"); err == nil {
		t.Fatal("restoring a missing backup should be refused")
	}
	// Cashier cannot back up or restore.
	cashier := &auth.User{ID: 2, UserName: "cash1", Role: "cashier"}
	if _, err := s.CreateBackup(ctx, cashier); err == nil {
		t.Fatal("cashier should not create backups")
	}
	if _, err := s.ListBackups(ctx, cashier); err == nil {
		t.Fatal("cashier should not list backups")
	}
}

func TestBackupDailyIfDue(t *testing.T) {
	s, dir := openFileStore(t)
	ctx := context.Background()
	admin := testAdmin()
	cloudDir := filepath.Join(dir, "cloud")
	_ = os.MkdirAll(cloudDir, 0o755)
	if err := s.SaveCloudBackupSettings(ctx, admin, CloudBackupSettings{
		Enabled: true, Dir: cloudDir, Time: "03:00", Keep: 5,
	}); err != nil {
		t.Fatal(err)
	}
	res, ran, err := s.RunDailyBackupIfDue(ctx, admin)
	if err != nil || !ran {
		t.Fatalf("first daily run: ran=%v err=%v", ran, err)
	}
	if res.CloudStatus != CloudOK {
		t.Fatalf("daily cloud status = %q", res.CloudStatus)
	}
	// Second call the same day: not due.
	_, ran, err = s.RunDailyBackupIfDue(ctx, admin)
	if err != nil || ran {
		t.Fatalf("second daily run same day: ran=%v err=%v", ran, err)
	}
}

// --- FIX-1 regression tests ---

func TestFix1_VerifyBackupIntegrity(t *testing.T) {
	s, _ := openFileStore(t)
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	res, err := s.CreateBackup(ctx, admin)
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	path := res.LocalPath
	if err := VerifyBackupFile(path); err != nil {
		t.Fatalf("valid backup should verify: %v", err)
	}
	// Corrupt a byte in the copy.
	corrupt := path + ".corrupt"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[100] ^= 0xFF
	if err := os.WriteFile(corrupt, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBackupFile(corrupt); err == nil {
		t.Fatal("corrupted backup must fail integrity_check (L12)")
	}
}

func TestFix1_ReopenMigratesOldSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cashierpos.db")
	fs, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an old backup: drop the M2 column.
	if _, err := fs.DB.Exec(`ALTER TABLE CustomerPayments DROP COLUMN PaymentMethod`); err != nil {
		t.Fatalf("drop column: %v", err)
	}
	fs.reopen()
	var name string
	err = fs.DB.QueryRow(`SELECT name FROM pragma_table_info('CustomerPayments')
		WHERE name='PaymentMethod'`).Scan(&name)
	if err != nil {
		t.Fatalf("PaymentMethod missing after reopen — migrate() was not called (M8): %v", err)
	}
	_ = fs.Close()
}

// --- FIX-2 regression tests ---

func TestFix2_ReopenAddsPurchaseFKs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cashierpos.db")
	// Build a pre-FIX-2 database by hand: PurchaseInvoices and
	// ProductBatches without any FK, holding one vendor, one purchase
	// and one batch that reference it.
	raw, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`CREATE TABLE Vendors (VendorID INTEGER PRIMARY KEY AUTOINCREMENT, VendorName TEXT NOT NULL, Phone TEXT DEFAULT '', Address TEXT DEFAULT '', Notes TEXT DEFAULT '', CreditLimit INTEGER NOT NULL DEFAULT 0, CurrentBalance INTEGER NOT NULL DEFAULT 0, IsActive INTEGER DEFAULT 1, CreatedDate TEXT)`,
		`CREATE TABLE PurchaseInvoices (PurchaseID INTEGER PRIMARY KEY AUTOINCREMENT, InvoiceNumber TEXT, VendorID INTEGER, PurchaseDate TEXT, TotalAmount INTEGER NOT NULL DEFAULT 0, PaidAmount INTEGER NOT NULL DEFAULT 0, RemainingAmount INTEGER NOT NULL DEFAULT 0, DueDate TEXT, PaymentMethod TEXT DEFAULT 'Cash', UserID INTEGER, UserName TEXT DEFAULT '', ShiftID INTEGER, Notes TEXT DEFAULT '', IsCanceled INTEGER DEFAULT 0)`,
		`CREATE TABLE ProductBatches (BatchID INTEGER PRIMARY KEY AUTOINCREMENT, ProductID INTEGER, BatchNumber TEXT DEFAULT '', Quantity INTEGER NOT NULL DEFAULT 0, ExpiryDate TEXT, PurchasePrice INTEGER DEFAULT 0, VendorID INTEGER, PurchaseID INTEGER, CreatedDate TEXT)`,
		`INSERT INTO Vendors(VendorID,VendorName) VALUES(1,'مورد قديم')`,
		`INSERT INTO PurchaseInvoices(PurchaseID,InvoiceNumber,VendorID,TotalAmount) VALUES(1,'P-1',1,5000)`,
		`INSERT INTO ProductBatches(BatchID,ProductID,VendorID,PurchaseID,Quantity) VALUES(1,7,1,1,1000)`,
	}
	for _, q := range stmts {
		if _, err := raw.Exec(q); err != nil {
			raw.Close()
			t.Fatalf("old schema setup: %v", err)
		}
	}
	raw.Close()
	// Opening through the store migrates: the tables are rebuilt with
	// FKs, data intact.
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open old DB: %v", err)
	}
	defer s.Close()
	// FIX-13 (coordinator, R13-M3 extension): the rebuild must leave FK
	// enforcement ON — the same guarantee ResetDatabase now gives.
	if on := fkOn(t, s); on != 1 {
		t.Fatalf("foreign_keys after rebuild = %d, want 1", on)
	}
	hasFK := func(table, from, ref string) bool {
		rows, err := s.DB.Query(fmt.Sprintf(`PRAGMA foreign_key_list(%s)`, table))
		if err != nil {
			t.Fatalf("fk list %s: %v", table, err)
		}
		defer rows.Close()
		for rows.Next() {
			var id, seq int
			var tbl, fromCol, toCol, onUpd, onDel, match string
			if err := rows.Scan(&id, &seq, &tbl, &fromCol, &toCol, &onUpd, &onDel, &match); err != nil {
				t.Fatal(err)
			}
			if fromCol == from && tbl == ref {
				return true
			}
		}
		return false
	}
	if !hasFK("PurchaseInvoices", "VendorID", "Vendors") {
		t.Fatal("L-3: PurchaseInvoices.VendorID FK missing after reopen")
	}
	if !hasFK("ProductBatches", "VendorID", "Vendors") {
		t.Fatal("L-3: ProductBatches.VendorID FK missing after reopen")
	}
	if !hasFK("ProductBatches", "PurchaseID", "PurchaseInvoices") {
		t.Fatal("L-3: ProductBatches.PurchaseID FK missing after reopen")
	}
	// Data survived the rebuild.
	var vName string
	var total, qty int64
	if err := s.DB.QueryRow(`SELECT VendorName FROM Vendors WHERE VendorID=1`).Scan(&vName); err != nil || vName != "مورد قديم" {
		t.Fatalf("vendor after rebuild: %q, err %v", vName, err)
	}
	if err := s.DB.QueryRow(`SELECT TotalAmount FROM PurchaseInvoices WHERE PurchaseID=1`).Scan(&total); err != nil || total != 5000 {
		t.Fatalf("purchase after rebuild: %d, err %v", total, err)
	}
	if err := s.DB.QueryRow(`SELECT Quantity FROM ProductBatches WHERE BatchID=1`).Scan(&qty); err != nil || qty != 1000 {
		t.Fatalf("batch after rebuild: %d, err %v", qty, err)
	}
	var bad int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&bad); err != nil || bad != 0 {
		t.Fatalf("foreign_key_check = %d, err %v", bad, err)
	}
	// The new FK actually enforces: an orphan purchase is refused.
	if _, err := s.DB.Exec(`INSERT INTO PurchaseInvoices(InvoiceNumber,VendorID,TotalAmount) VALUES('P-2',999,100)`); err == nil {
		t.Fatal("orphan purchase invoice (VendorID=999) must be refused by the FK")
	}
}

// --- FIX-26 lane D (R26-D-F2): backup failures must return static
// Arabic only — the HTML handlers render the store error verbatim, so
// a raw OS/driver error would leak a server filesystem path
// ("mkdir /…/backups: not a directory"). The raw error is logged
// server-side by backupFail; nothing raw may reach the caller.

func TestFix26D_BackupBlockedDirStaticArabic(t *testing.T) {
	s, dir := openFileStore(t)
	ctx := context.Background()
	admin := testAdmin()
	// A regular file standing where the backups directory must be.
	if err := os.WriteFile(filepath.Join(dir, "backups"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := s.CreateBackup(ctx, admin)
	if err == nil {
		t.Fatal("CreateBackup with a blocked backups dir must fail")
	}
	if got := err.Error(); got != "تعذّر إنشاء مجلد النسخ المحلي" {
		t.Fatalf("CreateBackup blocked-dir error = %q, want exactly the static Arabic text", got)
	}
	for _, leak := range []string{dir, "mkdir", "not a directory", "/backups"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("CreateBackup blocked-dir error leaks %q in %q", leak, err.Error())
		}
	}
}

func TestFix26D_RestoreCorruptStaticArabic(t *testing.T) {
	s, _ := openFileStore(t)
	ctx := context.Background()
	admin := testAdmin()
	res, err := s.CreateBackup(ctx, admin)
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	// Corrupt a byte in the backup file (same corruption as
	// TestFix1_VerifyBackupIntegrity).
	data, err := os.ReadFile(res.LocalPath)
	if err != nil {
		t.Fatal(err)
	}
	data[100] ^= 0xFF
	if err := os.WriteFile(res.LocalPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	err = s.RestoreBackup(ctx, admin, "local", res.FileName)
	if err == nil {
		t.Fatal("RestoreBackup of a corrupt backup must fail")
	}
	if got := err.Error(); got != "التحقق من النسخة فشل — لن تتم الاستعادة" {
		t.Fatalf("RestoreBackup corrupt error = %q, want exactly the static Arabic text", got)
	}
	for _, leak := range []string{res.LocalPath, "SQL logic error", "file is not a database",
		"database disk image is malformed", "integrity", "not a directory"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("RestoreBackup corrupt error leaks %q in %q", leak, err.Error())
		}
	}
}

func TestFix26D_VerifyBackupFileStaticArabic(t *testing.T) {
	s, _ := openFileStore(t)
	ctx := context.Background()
	admin := testAdmin()
	res, err := s.CreateBackup(ctx, admin)
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	data, err := os.ReadFile(res.LocalPath)
	if err != nil {
		t.Fatal(err)
	}
	data[100] ^= 0xFF
	corrupt := res.LocalPath + ".corrupt"
	if err := os.WriteFile(corrupt, data, 0o600); err != nil {
		t.Fatal(err)
	}
	err = VerifyBackupFile(corrupt)
	if err == nil {
		t.Fatal("VerifyBackupFile of a corrupt backup must fail")
	}
	for _, leak := range []string{corrupt, res.LocalPath, "SQL logic error",
		"file is not a database", "database disk image is malformed"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("VerifyBackupFile corrupt error leaks %q in %q", leak, err.Error())
		}
	}
	if strings.Contains(err.Error(), "فحص السلامة: ") {
		t.Fatalf("VerifyBackupFile corrupt error leaks integrity detail in %q", err.Error())
	}
}

func TestFix26D_CloudCopyFailureStaticArabic(t *testing.T) {
	s, dir := openFileStore(t)
	ctx := context.Background()
	admin := testAdmin()
	cloudDir := filepath.Join(dir, "cloud")
	if err := os.MkdirAll(cloudDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCloudBackupSettings(ctx, admin, CloudBackupSettings{
		Enabled: true, Dir: cloudDir, Time: "03:00", Keep: 5,
	}); err != nil {
		t.Fatal(err)
	}
	// Force copyFile to fail deterministically: plant a directory at
	// the exact path the cloud copy will be written to. The name is
	// second-precision, so retry with a fresh name if the second rolls
	// over between planting and CreateBackup (each miss only costs a
	// local backup + a planted dir in the temp tree).
	var res BackupResult
	var err error
	hit := false
	for attempt := 0; attempt < 8 && !hit; attempt++ {
		name := backupFileName()
		_ = os.Mkdir(filepath.Join(cloudDir, name), 0o755)
		res, err = s.CreateBackup(ctx, admin)
		if err != nil {
			t.Fatalf("CreateBackup (cloud copy blocked): %v", err)
		}
		if res.CloudStatus == CloudError {
			hit = true
		}
	}
	if !hit {
		t.Fatal("could not land a cloud-copy failure in 8 attempts (second kept rolling over)")
	}
	if got := res.Error; got != "فشل كتابة النسخة السحابية" {
		t.Fatalf("cloud copy res.Error = %q, want exactly the static Arabic text", got)
	}
	for _, leak := range []string{cloudDir, "is a directory", "not a directory"} {
		if strings.Contains(res.Error, leak) {
			t.Fatalf("cloud copy res.Error leaks %q in %q", leak, res.Error)
		}
	}
}
