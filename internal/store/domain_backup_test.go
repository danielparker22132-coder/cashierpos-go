package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
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
