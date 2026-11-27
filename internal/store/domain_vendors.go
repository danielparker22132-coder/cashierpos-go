// Package store — B3: vendors & purchases (FEATURE_MAP §6 V-01..V-10).
//
// Role rules (matrix rows 37-43):
//   - Create a NEW vendor: Admin/Manager/Cashier (create_vendor, V-01).
//   - Edit / delete a vendor: Admin/Manager only (manage_vendors).
//   - Receive a purchase invoice: Admin/Manager/Cashier/Warehouse
//     (receive_purchase); Supervisor may NOT receive.
//   - Update the product BuyPrice on receipt: Admin/Manager only
//     (edit_purchase_price, V-06).
//   - Pay a vendor: Admin/Manager/Cashier (pay_vendors, V-08);
//     Supervisor may NOT pay.
//   - Vendor reports/ledger view for reports: Admin/Manager only
//     (view_vendor_reports); the ledger screen itself is open to the
//     entry gate (manage_vendors OR receive_purchase OR pay_vendors, V-02).
//
// Money rules:
//   - Negative vendor balance / negative credit limit refused (no clamp).
//   - Purchase posting is atomic: vendor required, >= 1 line, qty > 0 and
//     purchase price > 0 per line, total > 0, 0 <= paid <= total.
//     Remaining increases the vendor balance; stock increases (milliunits);
//     one StockMovements row per line; batch/expiry -> ProductBatches.
//     Cash paid requires an open shift + the drawer gate, and writes a
//     CashBox 'out' row ("سداد مورد نقدي" at purchase time, V-06 tail
//     resolved as: yes, cash out, consistent with C-03/V-08).
//     Purchase invoice number: PI-YYYYMMDDHHMMSS.
//   - Vendor payment: amount > 0, never above the vendor balance
//     plus a 1-qirsh tolerance (FIX-12 SW-L01: amount <= balance + 0.01
//     is allowed; the 1-cent excess is absorbed and the balance clamps
//     at 0, never negative), BalanceBefore/After recorded, cash ->
//     CashBox 'out' + drawer gate, non-cash never touches the drawer
//     (V-08).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/money"
	"cashierpos-go/internal/rbac"
)

// Vendor is one row of Vendors.
type Vendor struct {
	ID             int64
	Name           string
	Phone          string
	Address        string
	Notes          string
	CreditLimit    money.Cents
	CurrentBalance money.Cents
	IsActive       bool
	CreatedDate    string
}

// VendorInput carries create/update fields.
type VendorInput struct {
	Name        string
	Phone       string
	Address     string
	Notes       string
	CreditLimit money.Cents
	// Balance is only honoured on create (opening balance); updates keep
	// the balance driven by purchases/payments.
	Balance  money.Cents
	IsActive bool
}

// CreateVendor inserts a vendor. Cashier may create a NEW vendor (V-01).
// Name is required; negative credit limit / negative balance refused.
func (s *Store) CreateVendor(ctx context.Context, user *auth.User, in VendorInput) (int64, error) {
	if err := rbac.Require(user.Role, rbac.PermCreateVendor); err != nil {
		return 0, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return 0, errors.New("اسم المورد مطلوب")
	}
	if in.CreditLimit < 0 {
		return 0, errors.New("حد الائتمان لا يمكن أن يكون سالباً — السالب ممنوع")
	}
	if in.Balance < 0 {
		return 0, errors.New("رصيد المورد لا يمكن أن يكون سالباً — السالب ممنوع")
	}
	var id int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO Vendors(VendorName,Phone,Address,Notes,CreditLimit,CurrentBalance,IsActive,CreatedDate)
			VALUES(?,?,?,?,?,?,1,?)`, name, strings.TrimSpace(in.Phone), strings.TrimSpace(in.Address),
			strings.TrimSpace(in.Notes), int64(in.CreditLimit), int64(in.Balance), Today())
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "إضافة مورد", "vendors", nil, 0, 0, "",
			fmt.Sprintf("إضافة مورد جديد: %s", name))
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// UpdateVendor edits a vendor. Admin/Manager only (V-01: editing existing
// records is Admin only; Manager = Admin). Balance is NOT editable here —
// it is driven by purchases and payments only.
func (s *Store) UpdateVendor(ctx context.Context, user *auth.User, id int64, in VendorInput) error {
	if err := rbac.Require(user.Role, rbac.PermManageVendors); err != nil {
		return err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return errors.New("اسم المورد مطلوب")
	}
	if in.CreditLimit < 0 {
		return errors.New("حد الائتمان لا يمكن أن يكون سالباً — السالب ممنوع")
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM Vendors WHERE VendorID=?`, id).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return errors.New("المورد غير موجود")
		}
		active := 1
		if !in.IsActive {
			active = 0
		}
		if _, err := tx.Exec(`UPDATE Vendors SET VendorName=?,Phone=?,Address=?,Notes=?,CreditLimit=?,IsActive=?
			WHERE VendorID=?`, name, strings.TrimSpace(in.Phone), strings.TrimSpace(in.Address),
			strings.TrimSpace(in.Notes), int64(in.CreditLimit), active, id); err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "تعديل مورد", "vendors", nil, 0, 0, "",
			fmt.Sprintf("تعديل بيانات المورد: %s", name))
	})
}

// DeleteVendor removes a vendor. Admin/Manager only, with a UI confirm.
// Refused when the vendor has purchase invoices (they are the ledger).
func (s *Store) DeleteVendor(ctx context.Context, user *auth.User, id int64) error {
	if err := rbac.Require(user.Role, rbac.PermManageVendors); err != nil {
		return err
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		var name string
		if err := tx.QueryRow(`SELECT VendorName FROM Vendors WHERE VendorID=?`, id).Scan(&name); err == sql.ErrNoRows {
			return errors.New("المورد غير موجود")
		} else if err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM PurchaseInvoices WHERE VendorID=?`, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("لا يمكن حذف مورد لديه فواتير شراء مسجلة (%d فاتورة)", n)
		}
		// FIX-1 L7: vendor payments are part of the ledger too.
		var np int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM VendorPayments WHERE VendorID=?`, id).Scan(&np); err != nil {
			return err
		}
		if np > 0 {
			return fmt.Errorf("لا يمكن حذف مورد لديه مدفوعات موردين مسجلة (%d دفعة)", np)
		}
		// FIX-2 L3: batches also reference the vendor now (ProductBatches
		// has a VendorID FK); refuse with the same clear message instead
		// of surfacing a raw FK error.
		var nb int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM ProductBatches WHERE VendorID=?`, id).Scan(&nb); err != nil {
			return err
		}
		if nb > 0 {
			return fmt.Errorf("لا يمكن حذف مورد لديه دفعات مخزون مسجلة (%d دفعة)", nb)
		}
		// FIX-12 SW-H01: open payables also reference the vendor —
		// refuse while any payable is open/partial, or the payable
		// would be orphaned with a dangling PartyID. Paid/cancelled
		// payables are history and do not block the delete.
		var npay int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM Payables WHERE Kind='vendor' AND PartyID=? AND Status IN ('open','partial')`, id).Scan(&npay); err != nil {
			return err
		}
		if npay > 0 {
			return fmt.Errorf("لا يمكن حذف مورد لديه مستحقات مفتوحة (%d مستحق)", npay)
		}
		if _, err := tx.Exec(`DELETE FROM Vendors WHERE VendorID=?`, id); err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "حذف مورد", "vendors", nil, 0, 0, "",
			fmt.Sprintf("حذف المورد: %s", name))
	})
}

// GetVendor returns one vendor.
// FIX-1 M7 (documented exception): vendor reads take a user and require
// login, but no role permission — the routes intentionally open vendor
// viewing to every logged-in role (warehouse needs vendor data to
// receive purchases), so no narrower perm fits all legitimate callers.
func (s *Store) GetVendor(ctx context.Context, user *auth.User, id int64) (*Vendor, error) {
	if err := requireUser(user, ""); err != nil {
		return nil, err
	}
	var v Vendor
	var limit, bal int64
	var active int
	err := s.DB.QueryRow(`SELECT VendorID,VendorName,Phone,Address,Notes,CreditLimit,CurrentBalance,IsActive,CreatedDate
		FROM Vendors WHERE VendorID=?`, id).
		Scan(&v.ID, &v.Name, &v.Phone, &v.Address, &v.Notes, &limit, &bal, &active, &v.CreatedDate)
	if err == sql.ErrNoRows {
		return nil, errors.New("المورد غير موجود")
	}
	if err != nil {
		return nil, err
	}
	v.CreditLimit = money.Cents(limit)
	v.CurrentBalance = money.Cents(bal)
	v.IsActive = active == 1
	return &v, nil
}

// ListVendors returns vendors, optionally filtered by name/phone.
func (s *Store) ListVendors(ctx context.Context, user *auth.User, query string) ([]*Vendor, error) {
	if err := requireUser(user, ""); err != nil {
		return nil, err
	}
	q := strings.TrimSpace(query)
	var rows *sql.Rows
	var err error
	if q == "" {
		rows, err = s.DB.Query(`SELECT VendorID,VendorName,Phone,Address,Notes,CreditLimit,CurrentBalance,IsActive,CreatedDate
			FROM Vendors ORDER BY VendorID`)
	} else {
		like := "%" + q + "%"
		rows, err = s.DB.Query(`SELECT VendorID,VendorName,Phone,Address,Notes,CreditLimit,CurrentBalance,IsActive,CreatedDate
			FROM Vendors WHERE VendorName LIKE ? OR Phone LIKE ? ORDER BY VendorID`, like, like)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Vendor
	for rows.Next() {
		var v Vendor
		var limit, bal int64
		var active int
		if err := rows.Scan(&v.ID, &v.Name, &v.Phone, &v.Address, &v.Notes, &limit, &bal, &active, &v.CreatedDate); err != nil {
			return nil, err
		}
		v.CreditLimit = money.Cents(limit)
		v.CurrentBalance = money.Cents(bal)
		v.IsActive = active == 1
		out = append(out, &v)
	}
	return out, rows.Err()
}

// VendorDueAlert is one vendor with amounts due soon (V-03).
type VendorDueAlert struct {
	VendorID   int64
	VendorName string
	Balance    money.Cents
	DueDate    string
	Remaining  money.Cents
	PurchaseNo string
}

// VendorDueAlerts returns vendors with open purchase remainings whose due
// date falls within the next 30 days (including overdue), following the
// .NET ShowStartupInventoryAlerts 30-day window (GAP-REPORT §3.2).
func (s *Store) VendorDueAlerts(ctx context.Context) ([]*VendorDueAlert, error) {
	limit := time.Now().In(cairoLoc()).AddDate(0, 0, 30).Format("2006-01-02")
	rows, err := s.DB.Query(`SELECT v.VendorID,v.VendorName,v.CurrentBalance,
		p.DueDate,p.RemainingAmount,p.InvoiceNumber
		FROM PurchaseInvoices p JOIN Vendors v ON v.VendorID=p.VendorID
		WHERE p.IsCanceled=0 AND p.RemainingAmount>0 AND p.DueDate<>'' AND p.DueDate<=?
		ORDER BY p.DueDate`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*VendorDueAlert
	for rows.Next() {
		var a VendorDueAlert
		var bal, rem int64
		if err := rows.Scan(&a.VendorID, &a.VendorName, &bal, &a.DueDate, &rem, &a.PurchaseNo); err != nil {
			return nil, err
		}
		a.Balance = money.Cents(bal)
		a.Remaining = money.Cents(rem)
		out = append(out, &a)
	}
	return out, rows.Err()
}

// VendorLedgerPurchase is one purchase row of a vendor ledger (V-04).
type VendorLedgerPurchase struct {
	PurchaseID int64
	InvoiceNo  string
	Date       string
	Total      money.Cents
	Paid       money.Cents
	Remaining  money.Cents
	DueDate    string
}

// VendorLedgerPayment is one payment row of a vendor ledger (V-04).
type VendorLedgerPayment struct {
	ID            int64
	Date          string
	Amount        money.Cents
	Method        string
	PurchaseID    sql.NullInt64
	BalanceBefore money.Cents
	BalanceAfter  money.Cents
	Notes         string
}

// VendorLedger is the per-vendor ledger: purchases + payments + balance.
type VendorLedger struct {
	Vendor    *Vendor
	Purchases []*VendorLedgerPurchase
	Payments  []*VendorLedgerPayment
}

// VendorLedger builds the ledger for a vendor (V-04).
func (s *Store) VendorLedger(ctx context.Context, user *auth.User, id int64) (*VendorLedger, error) {
	if err := requireUser(user, ""); err != nil {
		return nil, err
	}
	v, err := s.GetVendor(ctx, user, id)
	if err != nil {
		return nil, err
	}
	led := &VendorLedger{Vendor: v}
	rows, err := s.DB.Query(`SELECT PurchaseID,InvoiceNumber,PurchaseDate,TotalAmount,PaidAmount,RemainingAmount,DueDate
		FROM PurchaseInvoices WHERE VendorID=? AND IsCanceled=0 ORDER BY PurchaseDate, PurchaseID`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p VendorLedgerPurchase
		var total, paid, rem int64
		var due sql.NullString
		if err := rows.Scan(&p.PurchaseID, &p.InvoiceNo, &p.Date, &total, &paid, &rem, &due); err != nil {
			rows.Close()
			return nil, err
		}
		p.Total = money.Cents(total)
		p.Paid = money.Cents(paid)
		p.Remaining = money.Cents(rem)
		p.DueDate = due.String
		led.Purchases = append(led.Purchases, &p)
	}
	rows.Close()
	prows, err := s.DB.Query(`SELECT VendorPaymentID,PaymentDate,Amount,PaymentMethod,PurchaseID,BalanceBefore,BalanceAfter,Notes
		FROM VendorPayments WHERE VendorID=? ORDER BY PaymentDate, VendorPaymentID`, id)
	if err != nil {
		return nil, err
	}
	defer prows.Close()
	for prows.Next() {
		var p VendorLedgerPayment
		var amt, before, after int64
		if err := prows.Scan(&p.ID, &p.Date, &amt, &p.Method, &p.PurchaseID, &before, &after, &p.Notes); err != nil {
			return nil, err
		}
		p.Amount = money.Cents(amt)
		p.BalanceBefore = money.Cents(before)
		p.BalanceAfter = money.Cents(after)
		led.Payments = append(led.Payments, &p)
	}
	return led, prows.Err()
}

// PurchaseLineInput is one line of a purchase invoice.
type PurchaseLineInput struct {
	ProductID     int64
	Qty           money.Qty
	PurchasePrice money.Cents
	BatchNumber   string
	ExpiryDate    string // YYYY-MM-DD or ""
	ValidFrom     string // YYYY-MM-DD or "" (from-date of the from-to expiry range)
}

// PurchaseInput carries a purchase invoice.
type PurchaseInput struct {
	VendorID      int64
	Lines         []PurchaseLineInput
	Paid          money.Cents
	PaymentMethod string
	DueDate       string // YYYY-MM-DD or ""
	Notes         string
}

// Purchase is a saved purchase invoice header.
type Purchase struct {
	ID            int64
	InvoiceNo     string
	VendorID      int64
	VendorName    string
	Date          string
	Total         money.Cents
	Paid          money.Cents
	Remaining     money.Cents
	DueDate       string
	PaymentMethod string
	UserName      string
	Notes         string
}

// PurchaseLine is one saved purchase line (with product name).
type PurchaseLine struct {
	ProductID     int64
	ProductName   string
	Qty           money.Qty
	PurchasePrice money.Cents
	Total         money.Cents
	BatchNumber   string
	ExpiryDate    string
	ValidFrom     string
}

// PurchaseDetail is a purchase invoice for display/print (V-07).
type PurchaseDetail struct {
	Purchase *Purchase
	Lines    []*PurchaseLine
}

// CreatePurchase posts a purchase invoice atomically (V-05/V-06).
// Returns the new purchase id and its PI- number.
func (s *Store) CreatePurchase(ctx context.Context, user *auth.User, in PurchaseInput) (int64, string, error) {
	if err := rbac.Require(user.Role, rbac.PermReceivePurchase); err != nil {
		return 0, "", err
	}
	if in.VendorID == 0 {
		return 0, "", errors.New("برجاء اختيار المورد")
	}
	if len(in.Lines) == 0 {
		return 0, "", errors.New("لا يوجد أصناف في الفاتورة — أضف صنفاً واحداً على الأقل")
	}
	if in.Paid < 0 {
		return 0, "", errors.New("المبلغ المدفوع لا يمكن أن يكون سالباً — السالب ممنوع")
	}
	method, err := requireKnownPaymentMethod(in.PaymentMethod)
	if err != nil {
		return 0, "", err
	}
	var total money.Cents
	for i, ln := range in.Lines {
		if ln.ProductID == 0 {
			return 0, "", fmt.Errorf("سطر %d: برجاء اختيار الصنف", i+1)
		}
		if ln.Qty <= 0 {
			return 0, "", fmt.Errorf("سطر %d: الكمية وسعر الشراء يجب أن يكونا أكبر من صفر", i+1)
		}
		if ln.PurchasePrice <= 0 {
			return 0, "", fmt.Errorf("سطر %d: الكمية وسعر الشراء يجب أن يكونا أكبر من صفر", i+1)
		}
		lineTotal, err := money.MulQty(ln.PurchasePrice, ln.Qty)
		if err != nil {
			return 0, "", err
		}
		total, err = money.AddChecked(total, lineTotal)
		if err != nil {
			return 0, "", err
		}
	}
	if total <= 0 {
		return 0, "", errors.New("إجمالي فاتورة الشراء يجب أن يكون أكبر من صفر")
	}
	if in.Paid > total {
		return 0, "", errors.New("المبلغ المدفوع غير صحيح — لا يمكن أن يتجاوز إجمالي الفاتورة")
	}
	remaining := total - in.Paid
	var purchaseID int64
	var invNo string
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		// FIX-3 L7: sequential purchase numbers from NextPurchaseNumber
		// (high-water mark in the posting tx) — timestamps could
		// collide within a second.
		n, err := NextPurchaseNumber(tx)
		if err != nil {
			return err
		}
		invNo = n
		var vendorName string
		if err := tx.QueryRow(`SELECT VendorName FROM Vendors WHERE VendorID=?`, in.VendorID).Scan(&vendorName); err == sql.ErrNoRows {
			return errors.New("المورد غير موجود")
		} else if err != nil {
			return err
		}
		var shiftID int64
		if method == "Cash" && in.Paid > 0 {
			var err error
			shiftID, err = OpenShiftID(tx, user.ID)
			if err != nil {
				return err
			}
			if err := RequireDrawerBalance(tx, shiftID, in.Paid, "الدفع النقدي عند الشراء"); err != nil {
				return err
			}
		}
		now := Now()
		res, err := tx.Exec(`INSERT INTO PurchaseInvoices(InvoiceNumber,VendorID,PurchaseDate,TotalAmount,PaidAmount,RemainingAmount,DueDate,PaymentMethod,UserID,UserName,ShiftID,Notes,IsCanceled)
			VALUES(?,?,?,?,?,?,?,?,?,?,?, ?,0)`, invNo, in.VendorID, Today(), int64(total), int64(in.Paid), int64(remaining),
			in.DueDate, method, user.ID, user.UserName, nullableShift(shiftID), strings.TrimSpace(in.Notes))
		if err != nil {
			return err
		}
		purchaseID, err = res.LastInsertId()
		if err != nil {
			return err
		}
		canEditPrice := rbac.Can(user.Role, rbac.PermEditPurchasePrice)
		for _, ln := range in.Lines {
			var prodName string
			var curQty int64
			if err := tx.QueryRow(`SELECT ProductName,Quantity FROM Products WHERE ProductID=?`, ln.ProductID).
				Scan(&prodName, &curQty); err == sql.ErrNoRows {
				return fmt.Errorf("الصنف رقم %d غير موجود", ln.ProductID)
			} else if err != nil {
				return err
			}
			lineTotal, err := money.MulQty(ln.PurchasePrice, ln.Qty)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO PurchaseInvoiceDetails(PurchaseID,ProductID,ProductName,Quantity,PurchasePrice,TotalPrice,BatchNumber,ExpiryDate,ValidFrom)
				VALUES(?,?,?,?,?,?,?,?,?)`, purchaseID, ln.ProductID, prodName, int64(ln.Qty), int64(ln.PurchasePrice), int64(lineTotal),
				strings.TrimSpace(ln.BatchNumber), strings.TrimSpace(ln.ExpiryDate), strings.TrimSpace(ln.ValidFrom)); err != nil {
				return err
			}
			newQty := curQty + int64(ln.Qty)
			if newQty < 0 {
				return errors.New("كمية المخزون لا يمكن أن تكون سالبة — السالب ممنوع")
			}
			if _, err := tx.Exec(`UPDATE Products SET Quantity=? WHERE ProductID=?`, newQty, ln.ProductID); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO StockMovements(ProductID,MovementType,Quantity,BuyPrice,Notes,UserID,MovementDate)
				VALUES(?, 'purchase', ?, ?, ?, ?, ?)`, ln.ProductID, int64(ln.Qty), int64(ln.PurchasePrice),
				fmt.Sprintf("فاتورة شراء %s — %s", invNo, vendorName), user.ID, now); err != nil {
				return err
			}
			if canEditPrice {
				if _, err := tx.Exec(`UPDATE Products SET BuyPrice=? WHERE ProductID=?`, int64(ln.PurchasePrice), ln.ProductID); err != nil {
					return err
				}
			}
			if strings.TrimSpace(ln.BatchNumber) != "" || strings.TrimSpace(ln.ExpiryDate) != "" || strings.TrimSpace(ln.ValidFrom) != "" {
				if _, err := tx.Exec(`INSERT INTO ProductBatches(ProductID,BatchNumber,Quantity,ExpiryDate,ValidFrom,PurchasePrice,VendorID,PurchaseID,CreatedDate)
					VALUES(?,?,?,?,?,?,?,?,?)`, ln.ProductID, strings.TrimSpace(ln.BatchNumber), int64(ln.Qty),
					strings.TrimSpace(ln.ExpiryDate), strings.TrimSpace(ln.ValidFrom), int64(ln.PurchasePrice), in.VendorID, purchaseID, Today()); err != nil {
					return err
				}
				if _, err := tx.Exec(`UPDATE Products SET BatchNumber=?,ExpiryDate=?,IsExpiryTracked=1 WHERE ProductID=?`,
					strings.TrimSpace(ln.BatchNumber), strings.TrimSpace(ln.ExpiryDate), ln.ProductID); err != nil {
					return err
				}
			}
		}
		if _, err := tx.Exec(`UPDATE Vendors SET CurrentBalance=CurrentBalance+? WHERE VendorID=?`, int64(remaining), in.VendorID); err != nil {
			return err
		}
		if method == "Cash" && in.Paid > 0 {
			if _, err := tx.Exec(`INSERT INTO CashBox(UserID,UserName,ShiftID,MovementType,Amount,Reason,MovementDate)
				VALUES(?,?,?,'out',?, ?,?)`, user.ID, user.UserName, shiftID, int64(in.Paid),
				fmt.Sprintf("سداد مورد نقدي عند الشراء %s — %s", invNo, vendorName), now); err != nil {
				return err
			}
		}
		return AuditTx(tx, user.ID, user.UserName, "فاتورة شراء", "vendors", nil, 0, 0, "",
			fmt.Sprintf("فاتورة شراء %s — المورد %s — الإجمالي %s — المدفوع %s — المتبقي %s",
				invNo, vendorName, money.FormatEGP(total), money.FormatEGP(in.Paid), money.FormatEGP(remaining)))
	})
	if err != nil {
		return 0, "", err
	}
	return purchaseID, invNo, nil
}

// GetPurchase returns a purchase invoice with its lines (V-07).
func (s *Store) GetPurchase(ctx context.Context, id int64) (*PurchaseDetail, error) {
	var p Purchase
	var total, paid, rem int64
	var due sql.NullString
	err := s.DB.QueryRow(`SELECT p.PurchaseID,p.InvoiceNumber,p.VendorID,v.VendorName,p.PurchaseDate,
		p.TotalAmount,p.PaidAmount,p.RemainingAmount,p.DueDate,p.PaymentMethod,p.UserName,p.Notes
		FROM PurchaseInvoices p JOIN Vendors v ON v.VendorID=p.VendorID WHERE p.PurchaseID=?`, id).
		Scan(&p.ID, &p.InvoiceNo, &p.VendorID, &p.VendorName, &p.Date, &total, &paid, &rem, &due, &p.PaymentMethod, &p.UserName, &p.Notes)
	if err == sql.ErrNoRows {
		return nil, errors.New("فاتورة الشراء غير موجودة")
	}
	if err != nil {
		return nil, err
	}
	p.Total = money.Cents(total)
	p.Paid = money.Cents(paid)
	p.Remaining = money.Cents(rem)
	p.DueDate = due.String
	d := &PurchaseDetail{Purchase: &p}
	rows, err := s.DB.Query(`SELECT ProductID,ProductName,Quantity,PurchasePrice,TotalPrice,BatchNumber,ExpiryDate,ValidFrom
		FROM PurchaseInvoiceDetails WHERE PurchaseID=? ORDER BY DetailID`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ln PurchaseLine
		var qty, price, tot int64
		var batch, exp, vf sql.NullString
		if err := rows.Scan(&ln.ProductID, &ln.ProductName, &qty, &price, &tot, &batch, &exp, &vf); err != nil {
			return nil, err
		}
		ln.Qty = money.Qty(qty)
		ln.PurchasePrice = money.Cents(price)
		ln.Total = money.Cents(tot)
		ln.BatchNumber = batch.String
		ln.ExpiryDate = exp.String
		ln.ValidFrom = vf.String
		d.Lines = append(d.Lines, &ln)
	}
	return d, rows.Err()
}

// PayVendor pays a vendor (V-08/V-09). purchaseID 0 = general payment
// against the running balance; > 0 targets that purchase invoice.
// Amount must be > 0 and must not exceed the vendor balance (and, when a
// purchase is targeted, its remaining). Cash requires an open shift +
// drawer gate and writes CashBox 'out'; non-cash never touches the drawer.
// BalanceBefore/After are recorded. Perm: pay_vendors.
func (s *Store) PayVendor(ctx context.Context, user *auth.User, vendorID int64, purchaseID int64, amount money.Cents, method, notes string) (money.Cents, error) {
	if err := rbac.Require(user.Role, rbac.PermPayVendors); err != nil {
		return 0, err
	}
	if amount <= 0 {
		return 0, errors.New("قيمة السداد يجب أن تكون أكبر من صفر")
	}
	method, err := requireKnownPaymentMethod(method)
	if err != nil {
		return 0, err
	}
	var balanceAfter money.Cents
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		var vendorName string
		var balance int64
		if err := tx.QueryRow(`SELECT VendorName,CurrentBalance FROM Vendors WHERE VendorID=?`, vendorID).
			Scan(&vendorName, &balance); err == sql.ErrNoRows {
			return errors.New("المورد غير موجود")
		} else if err != nil {
			return err
		}
		// FIX-12 SW-L01: 1-qirsh tolerance — a payment up to balance +
		// 0.01 is allowed (the 1-cent excess is absorbed; the balance
		// clamps at 0 and never goes negative). Paying more than
		// balance + 0.01 is still refused.
		if amount > money.Cents(balance)+money.Cents(1) {
			return fmt.Errorf("لا يمكن سداد مبلغ أكبر من مديونية المورد (%s) — الدفع الزائد مرفوض",
				money.FormatEGP(money.Cents(balance)))
		}
		if purchaseID != 0 {
			var pv, pr int64
			if err := tx.QueryRow(`SELECT VendorID,RemainingAmount FROM PurchaseInvoices WHERE PurchaseID=? AND IsCanceled=0`,
				purchaseID).Scan(&pv, &pr); err == sql.ErrNoRows {
				return errors.New("فاتورة الشراء غير موجودة")
			} else if err != nil {
				return err
			}
			if pv != vendorID {
				return errors.New("فاتورة الشراء لا تخص هذا المورد")
			}
			if amount > money.Cents(pr) {
				return fmt.Errorf("مبلغ السداد يتجاوز المتبقي على فاتورة الشراء (%s) — الدفع الزائد مرفوض",
					money.FormatEGP(money.Cents(pr)))
			}
		}
		var shiftID int64
		if method == "Cash" {
			var err error
			shiftID, err = OpenShiftID(tx, user.ID)
			if err != nil {
				return err
			}
			if err := RequireDrawerBalance(tx, shiftID, amount, "سداد مورد نقدي"); err != nil {
				return err
			}
		}
		balanceAfter = money.Cents(balance) - amount
		if balanceAfter < 0 {
			// SW-L01 tolerance: the 1-cent excess over the balance is
			// absorbed — the balance clamps at 0, never negative.
			balanceAfter = 0
		}
		now := Now()
		if _, err := tx.Exec(`INSERT INTO VendorPayments(VendorID,PurchaseID,Amount,PaymentMethod,PaymentDate,UserID,UserName,ShiftID,BalanceBefore,BalanceAfter,Notes)
			VALUES(?,?,?,?,?,?,?, ?,?,?,?)`, vendorID, nullablePurchase(purchaseID), int64(amount), method, now,
			user.ID, user.UserName, nullableShift(shiftID), balance, int64(balanceAfter), strings.TrimSpace(notes)); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE Vendors SET CurrentBalance=? WHERE VendorID=?`, int64(balanceAfter), vendorID); err != nil {
			return err
		}
		if purchaseID != 0 {
			if _, err := tx.Exec(`UPDATE PurchaseInvoices SET RemainingAmount=RemainingAmount-?, PaidAmount=PaidAmount+? WHERE PurchaseID=?`,
				int64(amount), int64(amount), purchaseID); err != nil {
				return err
			}
		}
		if method == "Cash" {
			if _, err := tx.Exec(`INSERT INTO CashBox(UserID,UserName,ShiftID,MovementType,Amount,Reason,MovementDate)
				VALUES(?,?,?,'out',?, ?,?)`, user.ID, user.UserName, shiftID, int64(amount),
				fmt.Sprintf("سداد مورد نقدي — %s", vendorName), now); err != nil {
				return err
			}
		}
		action := "سداد مورد"
		if method == "Cash" {
			action = "سداد مورد نقدي"
		}
		return AuditTx(tx, user.ID, user.UserName, action, "vendors", nil, 0, 0, "",
			fmt.Sprintf("سداد مورد %s — %s (%s) — الرصيد قبل %s — بعد %s",
				vendorName, money.FormatEGP(amount), method, money.FormatEGP(money.Cents(balance)), money.FormatEGP(balanceAfter)))
	})
	if err != nil {
		return 0, err
	}
	return balanceAfter, nil
}

func nullablePurchase(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}
