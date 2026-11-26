// Package store — B1 (sales / products / promos / stocktake).
//
// domain_products.go: product CRUD, stock add/adjust, stock ledger,
// low-stock / expiry alerts, and scale (weighing) barcode decoding.
//
// Money is integer cents (money.Cents), quantities integer milliunits
// (money.Qty, 1 unit = 1000). No float on any money path. Negative
// stock is refused everywhere, never clamped.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/money"
	"cashierpos-go/internal/rbac"
)

// Product is one catalog row.
type Product struct {
	ID                  int64
	Barcode             string
	Name                string
	Category            string
	Supplier            string
	BuyPrice            money.Cents
	RetailPrice         money.Cents
	WholesalePrice      money.Cents
	BulkPrice           money.Cents
	Quantity            money.Qty
	MinQuantity         money.Qty
	Unit                string
	ExpiryDate          string // YYYY-MM-DD or ""
	BatchNumber         string
	ExpiryAlertDays     int
	IsExpiryTracked     bool
	IsWeighted          bool
	ScalePLU            string
	PricePerKg          money.Cents
	ScaleBarcodeType    string
	ScaleEnabled        bool
	ScalePrefixOverride string
	IsActive            bool
	CreatedDate         string
}

// ProductInput carries the fields a create/update call may set.
type ProductInput struct {
	Barcode             string
	Name                string
	Category            string
	Supplier            string
	BuyPrice            money.Cents
	RetailPrice         money.Cents
	WholesalePrice      money.Cents
	BulkPrice           money.Cents
	Quantity            money.Qty
	MinQuantity         money.Qty
	Unit                string
	ExpiryDate          string
	BatchNumber         string
	ExpiryAlertDays     int
	IsExpiryTracked     bool
	IsWeighted          bool
	ScalePLU            string
	PricePerKg          money.Cents
	ScaleBarcodeType    string
	ScaleEnabled        bool
	ScalePrefixOverride string
	IsActive            bool
}

// StockMovement is one StockMovements row.
type StockMovement struct {
	ID          int64
	ProductID   int64
	ProductName string
	Type        string
	Quantity    money.Qty // signed milliunits (sale rows are negative)
	BuyPrice    money.Cents
	Notes       string
	UserID      int64
	Date        string
}

// ScaleSettings is the ScaleSettings row (ID=1), defaults when absent.
type ScaleSettings struct {
	IsEnabled          bool
	Prefix             string
	ProductCodeLength  int
	ValueLength        int
	Mode               string // WeightEmbedded | PriceEmbedded
	HasCheckDigit      bool
	WeightUnit         string // Kilogram | Gram
	ValueDecimalPlaces int
	Notes              string
}

// ScaleDecode is a decoded scale barcode.
type ScaleDecode struct {
	RawBarcode  string
	Prefix      string
	PLU         string
	ProductID   int64
	ProductName string
	Weight      money.Qty // milli-kg (weight mode; price mode: derived weight)
	TotalPrice  money.Cents
	Mode        string
}

// --- shared private helpers (used by all B1 domain files) ---

func scanProduct(row *sql.Row) (*Product, error) {
	var p Product
	var barcode, category, supplier, unit, expiry, batch, plu, scaleType, scalePrefix, created sql.NullString
	var isExp, isW, scaleEn, isActive int
	err := row.Scan(&p.ID, &barcode, &p.Name, &category, &supplier,
		&p.BuyPrice, &p.RetailPrice, &p.WholesalePrice, &p.BulkPrice,
		&p.Quantity, &p.MinQuantity, &unit, &expiry, &batch,
		&p.ExpiryAlertDays, &isExp, &isW, &plu, &p.PricePerKg,
		&scaleType, &scaleEn, &scalePrefix, &isActive, &created)
	if err != nil {
		return nil, err
	}
	p.Barcode = barcode.String
	p.Category = category.String
	p.Supplier = supplier.String
	p.Unit = unit.String
	p.ExpiryDate = expiry.String
	p.BatchNumber = batch.String
	p.IsExpiryTracked = isExp == 1
	p.IsWeighted = isW == 1
	p.ScalePLU = plu.String
	p.ScaleBarcodeType = scaleType.String
	p.ScaleEnabled = scaleEn == 1
	p.ScalePrefixOverride = scalePrefix.String
	p.IsActive = isActive == 1
	p.CreatedDate = created.String
	return &p, nil
}

func scanProductRows(rows *sql.Rows) (*Product, error) {
	var p Product
	var barcode, category, supplier, unit, expiry, batch, plu, scaleType, scalePrefix, created sql.NullString
	var isExp, isW, scaleEn, isActive int
	err := rows.Scan(&p.ID, &barcode, &p.Name, &category, &supplier,
		&p.BuyPrice, &p.RetailPrice, &p.WholesalePrice, &p.BulkPrice,
		&p.Quantity, &p.MinQuantity, &unit, &expiry, &batch,
		&p.ExpiryAlertDays, &isExp, &isW, &plu, &p.PricePerKg,
		&scaleType, &scaleEn, &scalePrefix, &isActive, &created)
	if err != nil {
		return nil, err
	}
	p.Barcode = barcode.String
	p.Category = category.String
	p.Supplier = supplier.String
	p.Unit = unit.String
	p.ExpiryDate = expiry.String
	p.BatchNumber = batch.String
	p.IsExpiryTracked = isExp == 1
	p.IsWeighted = isW == 1
	p.ScalePLU = plu.String
	p.ScaleBarcodeType = scaleType.String
	p.ScaleEnabled = scaleEn == 1
	p.ScalePrefixOverride = scalePrefix.String
	p.IsActive = isActive == 1
	p.CreatedDate = created.String
	return &p, nil
}

const productColumns = `ProductID,Barcode,ProductName,Category,Supplier,BuyPrice,
	RetailPrice,WholesalePrice,BulkPrice,Quantity,MinQuantity,Unit,ExpiryDate,
	BatchNumber,ExpiryAlertDays,IsExpiryTracked,IsWeighted,ScalePLU,PricePerKg,
	ScaleBarcodeType,ScaleEnabled,ScalePrefixOverride,IsActive,CreatedDate`

func loadProductTx(tx *sql.Tx, id int64) (*Product, error) {
	p, err := scanProduct(tx.QueryRow(`SELECT `+productColumns+` FROM Products WHERE ProductID=?`, id))
	if err == sql.ErrNoRows {
		return nil, errors.New("المنتج غير موجود")
	}
	return p, err
}

func loadProductDB(db *sql.DB, id int64) (*Product, error) {
	p, err := scanProduct(db.QueryRow(`SELECT `+productColumns+` FROM Products WHERE ProductID=?`, id))
	if err == sql.ErrNoRows {
		return nil, errors.New("المنتج غير موجود")
	}
	return p, err
}

func insertMovementTx(tx *sql.Tx, productID int64, movType string, qty money.Qty, buyPrice money.Cents, notes string, userID int64) error {
	_, err := tx.Exec(`INSERT INTO StockMovements(ProductID,MovementType,Quantity,BuyPrice,Notes,UserID,MovementDate)
		VALUES(?,?,?,?,?,?,?)`, productID, movType, int64(qty), int64(buyPrice), notes, userID, Now())
	return err
}

// validateProductPrices enforces P-04: every price > 0 and
// BuyPrice <= BulkPrice <= WholesalePrice <= RetailPrice.
func validateProductPrices(buy, bulk, wholesale, retail money.Cents) error {
	if buy <= 0 {
		return errors.New("سعر الشراء يجب أن يكون أكبر من صفر")
	}
	if bulk <= 0 {
		return errors.New("سعر جملة الجملة يجب أن يكون أكبر من صفر")
	}
	if wholesale <= 0 {
		return errors.New("سعر الجملة يجب أن يكون أكبر من صفر")
	}
	if retail <= 0 {
		return errors.New("سعر القطاعي يجب أن يكون أكبر من صفر")
	}
	if !(buy <= bulk && bulk <= wholesale && wholesale <= retail) {
		return errors.New("ترتيب الأسعار غير صحيح. يجب أن يكون: سعر الشراء ≤ جملة الجملة ≤ الجملة ≤ القطاعي")
	}
	return nil
}

func pluTrimmed(plu string) string {
	return strings.TrimLeft(strings.TrimSpace(plu), "0")
}

// pluExistsTx reports whether a PLU is used by another product.
func pluExistsTx(tx *sql.Tx, plu string, excludeID int64) (bool, error) {
	plu = strings.TrimSpace(plu)
	if plu == "" {
		return false, nil
	}
	var n int
	err := tx.QueryRow(`SELECT COUNT(*) FROM Products
		WHERE ScalePLU<>'' AND (ScalePLU=? OR ScalePLU=?) AND ProductID<>?`,
		plu, pluTrimmed(plu), excludeID).Scan(&n)
	return n > 0, err
}

// validateProductInput checks the field rules that need no DB except
// uniqueness (barcode / PLU), which is checked by the caller in-tx.
func validateProductInput(in ProductInput) error {
	if strings.TrimSpace(in.Name) == "" {
		return errors.New("اسم المنتج مطلوب")
	}
	if in.Quantity < 0 || in.MinQuantity < 0 {
		return errors.New("كمية غير صحيحة")
	}
	if err := validateProductPrices(in.BuyPrice, in.BulkPrice, in.WholesalePrice, in.RetailPrice); err != nil {
		return err
	}
	if in.ExpiryDate != "" {
		if _, err := time.Parse("2006-01-02", in.ExpiryDate); err != nil {
			return errors.New("تاريخ الصلاحية غير صحيح — استخدم الصيغة YYYY-MM-DD")
		}
	}
	if in.ExpiryAlertDays < 0 {
		return errors.New("عدد أيام تنبيه الصلاحية لا يمكن أن يكون سالباً")
	}
	if in.IsWeighted {
		if strings.TrimSpace(in.ScalePLU) == "" {
			return errors.New("كود الصنف على الميزان PLU مطلوب للصنف الوزني")
		}
		for _, r := range in.ScalePLU {
			if r < '0' || r > '9' {
				return errors.New("كود الميزان PLU يجب أن يكون أرقامًا فقط")
			}
		}
		if in.PricePerKg <= 0 {
			return errors.New("سعر الكيلو للصنف الوزني يجب أن يكون أكبر من صفر")
		}
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// CreateProduct creates a product (Admin/Manager only, P-01..P-05).
func (s *Store) CreateProduct(ctx context.Context, user *auth.User, in ProductInput) (*Product, error) {
	if err := rbac.Require(user.Role, rbac.PermManageProducts); err != nil {
		return nil, err
	}
	if err := validateProductInput(in); err != nil {
		return nil, err
	}
	if in.Unit == "" {
		in.Unit = "قطعة"
	}
	if in.ExpiryAlertDays == 0 {
		in.ExpiryAlertDays = 30
	}
	if in.ScaleBarcodeType == "" {
		in.ScaleBarcodeType = "WeightEmbedded"
	}
	var id int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		if strings.TrimSpace(in.Barcode) != "" {
			var n int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM Products WHERE Barcode=?`, strings.TrimSpace(in.Barcode)).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return errors.New("الباركود موجود مسبقًا")
			}
		}
		if in.IsWeighted {
			exists, err := pluExistsTx(tx, in.ScalePLU, 0)
			if err != nil {
				return err
			}
			if exists {
				return errors.New("كود الميزان PLU مستخدم مع صنف آخر")
			}
		}
		res, err := tx.Exec(`INSERT INTO Products(Barcode,ProductName,Category,Supplier,
			BuyPrice,RetailPrice,WholesalePrice,BulkPrice,Quantity,MinQuantity,Unit,
			ExpiryDate,BatchNumber,ExpiryAlertDays,IsExpiryTracked,IsWeighted,ScalePLU,
			PricePerKg,ScaleBarcodeType,ScaleEnabled,ScalePrefixOverride,IsActive,CreatedDate)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			nullStr(strings.TrimSpace(in.Barcode)), strings.TrimSpace(in.Name),
			strings.TrimSpace(in.Category), in.Supplier,
			int64(in.BuyPrice), int64(in.RetailPrice), int64(in.WholesalePrice), int64(in.BulkPrice),
			int64(in.Quantity), int64(in.MinQuantity), in.Unit,
			nullStr(in.ExpiryDate), in.BatchNumber, in.ExpiryAlertDays,
			boolInt(in.IsExpiryTracked), boolInt(in.IsWeighted),
			nullStr(strings.TrimSpace(in.ScalePLU)), int64(in.PricePerKg),
			in.ScaleBarcodeType, boolInt(in.ScaleEnabled),
			nullStr(strings.TrimSpace(in.ScalePrefixOverride)), 1, Today())
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "إضافة منتج", "مخزون", nil, 0, 0, "",
			fmt.Sprintf("إضافة منتج: %s", strings.TrimSpace(in.Name)))
	})
	if err != nil {
		return nil, err
	}
	return s.GetProduct(ctx, user, id)
}

// UpdateProduct edits a product (Admin/Manager only). All fields are
// replaced from in; pass the current values for fields that do not change.
func (s *Store) UpdateProduct(ctx context.Context, user *auth.User, id int64, in ProductInput) (*Product, error) {
	if err := rbac.Require(user.Role, rbac.PermManageProducts); err != nil {
		return nil, err
	}
	if err := validateProductInput(in); err != nil {
		return nil, err
	}
	// G-8: the pre-update retail price, for the price-drop trigger below.
	var oldRetail money.Cents
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		old, err := loadProductTx(tx, id)
		if err != nil {
			return err
		}
		oldRetail = old.RetailPrice
		if strings.TrimSpace(in.Barcode) != "" {
			var n int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM Products WHERE Barcode=? AND ProductID<>?`,
				strings.TrimSpace(in.Barcode), id).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return errors.New("الباركود موجود مسبقًا")
			}
		}
		if in.IsWeighted {
			exists, err := pluExistsTx(tx, in.ScalePLU, id)
			if err != nil {
				return err
			}
			if exists {
				return errors.New("كود الميزان PLU مستخدم مع صنف آخر")
			}
		}
		_, err = tx.Exec(`UPDATE Products SET Barcode=?,ProductName=?,Category=?,Supplier=?,
			BuyPrice=?,RetailPrice=?,WholesalePrice=?,BulkPrice=?,Quantity=?,MinQuantity=?,Unit=?,
			ExpiryDate=?,BatchNumber=?,ExpiryAlertDays=?,IsExpiryTracked=?,IsWeighted=?,ScalePLU=?,
			PricePerKg=?,ScaleBarcodeType=?,ScaleEnabled=?,ScalePrefixOverride=?,IsActive=?
			WHERE ProductID=?`,
			nullStr(strings.TrimSpace(in.Barcode)), strings.TrimSpace(in.Name),
			strings.TrimSpace(in.Category), in.Supplier,
			int64(in.BuyPrice), int64(in.RetailPrice), int64(in.WholesalePrice), int64(in.BulkPrice),
			int64(in.Quantity), int64(in.MinQuantity), in.Unit,
			nullStr(in.ExpiryDate), in.BatchNumber, in.ExpiryAlertDays,
			boolInt(in.IsExpiryTracked), boolInt(in.IsWeighted),
			nullStr(strings.TrimSpace(in.ScalePLU)), int64(in.PricePerKg),
			in.ScaleBarcodeType, boolInt(in.ScaleEnabled),
			nullStr(strings.TrimSpace(in.ScalePrefixOverride)), boolInt(in.IsActive), id)
		if err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "تعديل منتج", "مخزون", nil, 0, 0, "",
			fmt.Sprintf("تعديل منتج: %s", strings.TrimSpace(in.Name)))
	})
	if err != nil {
		return nil, err
	}
	prod, err := s.GetProduct(ctx, user, id)
	if err != nil {
		return nil, err
	}
	// G-8: price drop — fire once per lowering of the retail price, to
	// staff who previously sold the product (NotifyPriceDrop's own
	// targeting + per-product+price dedupe). Best effort: a notification
	// failure must never fail the product update itself. A raise or an
	// unchanged price never fires (NotifyPriceDrop refuses new >= old).
	if prod.RetailPrice < oldRetail {
		_, _ = s.NotifyPriceDrop(ctx, user, id, prod.Name, int64(oldRetail), int64(prod.RetailPrice))
	}
	return prod, nil
}

// DeleteProduct deactivates a product (Admin/Manager only). The row is
// kept so invoices and movements that reference it stay readable.
func (s *Store) DeleteProduct(ctx context.Context, user *auth.User, id int64) error {
	if err := rbac.Require(user.Role, rbac.PermManageProducts); err != nil {
		return err
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		p, err := loadProductTx(tx, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE Products SET IsActive=0 WHERE ProductID=?`, id); err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "حذف منتج", "مخزون", nil, 0, 0, "",
			fmt.Sprintf("حذف منتج: %s", p.Name))
	})
}

// GetProduct returns one product by id (any role with view_products).
func (s *Store) GetProduct(ctx context.Context, user *auth.User, id int64) (*Product, error) {
	if err := rbac.Require(user.Role, rbac.PermViewProducts); err != nil {
		return nil, err
	}
	p, err := loadProductDB(s.DB, id)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// ListProducts lists products (active only unless includeInactive).
func (s *Store) ListProducts(ctx context.Context, user *auth.User, includeInactive bool) ([]*Product, error) {
	if err := rbac.Require(user.Role, rbac.PermViewProducts); err != nil {
		return nil, err
	}
	q := `SELECT ` + productColumns + ` FROM Products`
	if !includeInactive {
		q += ` WHERE IsActive=1`
	}
	q += ` ORDER BY ProductName`
	rows, err := s.DB.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Product
	for rows.Next() {
		p, err := scanProductRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SearchProducts searches active products by name or barcode (P-07).
func (s *Store) SearchProducts(ctx context.Context, user *auth.User, q string) ([]*Product, error) {
	if err := rbac.Require(user.Role, rbac.PermViewProducts); err != nil {
		return nil, err
	}
	q = strings.TrimSpace(q)
	if q == "" {
		return []*Product{}, nil
	}
	like := "%" + q + "%"
	rows, err := s.DB.QueryContext(ctx, `SELECT `+productColumns+` FROM Products
		WHERE IsActive=1 AND (ProductName LIKE ? OR Barcode LIKE ? OR ScalePLU LIKE ?)
		ORDER BY ProductName LIMIT 50`, like, like, like)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Product
	for rows.Next() {
		p, err := scanProductRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetProductByBarcode finds an active product by exact barcode, then by
// scale-barcode decode (S-03/S-31). Returns the product and, for a scale
// barcode, the decoded weight/price; decoded is nil for a plain barcode.
func (s *Store) GetProductByBarcode(ctx context.Context, user *auth.User, code string) (*Product, *ScaleDecode, error) {
	if err := rbac.Require(user.Role, rbac.PermViewProducts); err != nil {
		return nil, nil, err
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, nil, errors.New("لم يتم العثور على المنتج: ")
	}
	p, err := scanProduct(s.DB.QueryRow(`SELECT `+productColumns+` FROM Products WHERE Barcode=? AND IsActive=1`, code))
	if err == nil {
		return p, nil, nil
	}
	if err != sql.ErrNoRows {
		return nil, nil, err
	}
	dec, err := s.DecodeScaleBarcode(ctx, code)
	if err != nil {
		return nil, nil, fmt.Errorf("لم يتم العثور على المنتج: %s", code)
	}
	p, err = loadProductDB(s.DB, dec.ProductID)
	if err != nil {
		return nil, nil, fmt.Errorf("لم يتم العثور على المنتج: %s", code)
	}
	return p, dec, nil
}

// LowStockProducts returns active products at or below MinQuantity (P-06).
func (s *Store) LowStockProducts(ctx context.Context, user *auth.User) ([]*Product, error) {
	if err := rbac.Require(user.Role, rbac.PermViewStock); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT `+productColumns+` FROM Products
		WHERE IsActive=1 AND Quantity<=MinQuantity ORDER BY Quantity, ProductName`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Product
	for rows.Next() {
		p, err := scanProductRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// LowStockReport returns low-stock rows in the report shape used by
// the dashboard (B4). Thin adapter over LowStockProducts (P-06).
func (s *Store) LowStockReport(ctx context.Context, user *auth.User) ([]LowStockRow, error) {
	prods, err := s.LowStockProducts(ctx, user)
	if err != nil {
		return nil, err
	}
	out := make([]LowStockRow, 0, len(prods))
	for _, p := range prods {
		out = append(out, LowStockRow{ProductID: p.ID, ProductName: p.Name,
			Barcode: p.Barcode, Quantity: p.Quantity, MinQuantity: p.MinQuantity})
	}
	return out, nil
}

// ExpiringProducts returns active products whose expiry falls within
// their own ExpiryAlertDays (expired ones included, earliest first).
func (s *Store) ExpiringProducts(ctx context.Context, user *auth.User) ([]*Product, error) {
	if err := rbac.Require(user.Role, rbac.PermViewStock); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT `+productColumns+` FROM Products
		WHERE IsActive=1 AND ExpiryDate IS NOT NULL AND ExpiryDate<>''
		AND ExpiryDate<=date(?, '+' || ExpiryAlertDays || ' days')
		ORDER BY ExpiryDate`, Today())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Product
	for rows.Next() {
		p, err := scanProductRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListCategories returns distinct non-empty categories (products + Categories table).
func (s *Store) ListCategories(ctx context.Context, user *auth.User) ([]string, error) {
	if err := rbac.Require(user.Role, rbac.PermViewProducts); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT Category FROM Products WHERE Category<>'' 
		UNION SELECT Name FROM Categories ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AddStock increases stock (P-09: Admin/Supervisor/Warehouse).
func (s *Store) AddStock(ctx context.Context, user *auth.User, productID int64, qty money.Qty) (*Product, error) {
	if err := rbac.Require(user.Role, rbac.PermAddStock); err != nil {
		return nil, err
	}
	if qty <= 0 || qty > money.MaxQty {
		return nil, errors.New("كمية غير صحيحة")
	}
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		p, err := loadProductTx(tx, productID)
		if err != nil {
			return err
		}
		if !p.IsActive {
			return errors.New("المنتج غير موجود")
		}
		newQty := p.Quantity + qty
		if _, err := tx.Exec(`UPDATE Products SET Quantity=? WHERE ProductID=?`, int64(newQty), productID); err != nil {
			return err
		}
		if err := insertMovementTx(tx, productID, "purchase", qty, 0, "إضافة مخزون", user.ID); err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "إضافة مخزون", "مخزون", nil, 0, 0, "",
			fmt.Sprintf("%s | +%s %s | قبل: %s | بعد: %s", p.Name, money.FormatQty(qty), p.Unit,
				money.FormatQty(p.Quantity), money.FormatQty(newQty)))
	})
	if err != nil {
		return nil, err
	}
	return s.GetProduct(ctx, user, productID)
}

// AdjustStock sets an ABSOLUTE quantity (P-10: Admin/Warehouse).
// Reason is mandatory. The movement stores |diff|.
func (s *Store) AdjustStock(ctx context.Context, user *auth.User, productID int64, newQty money.Qty, reason string) (*Product, error) {
	if err := rbac.Require(user.Role, rbac.PermAdjustStock); err != nil {
		return nil, err
	}
	if newQty < 0 || newQty > money.MaxQty {
		return nil, errors.New("كمية غير صحيحة")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, errors.New("سبب التعديل مطلوب")
	}
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		p, err := loadProductTx(tx, productID)
		if err != nil {
			return err
		}
		if !p.IsActive {
			return errors.New("المنتج غير موجود")
		}
		diff := newQty - p.Quantity
		absDiff := diff
		if absDiff < 0 {
			absDiff = -absDiff
		}
		if _, err := tx.Exec(`UPDATE Products SET Quantity=? WHERE ProductID=?`, int64(newQty), productID); err != nil {
			return err
		}
		notes := fmt.Sprintf("تعديل جرد: %s | من %s إلى %s", reason, money.FormatQty(p.Quantity), money.FormatQty(newQty))
		if err := insertMovementTx(tx, productID, "adjust", absDiff, 0, notes, user.ID); err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "تعديل مخزون", "مخزون", nil, 0, 0, "",
			fmt.Sprintf("جرد %s: من %s إلى %s | السبب: %s", p.Name, money.FormatQty(p.Quantity), money.FormatQty(newQty), reason))
	})
	if err != nil {
		return nil, err
	}
	return s.GetProduct(ctx, user, productID)
}

// ListStockMovements returns the movement ledger, newest first (P-11).
// productID 0 = all products. At most limit rows (default 200).
func (s *Store) ListStockMovements(ctx context.Context, user *auth.User, productID int64, limit int) ([]*StockMovement, error) {
	if err := rbac.Require(user.Role, rbac.PermViewStock); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 200
	}
	var rows *sql.Rows
	var err error
	if productID > 0 {
		rows, err = s.DB.QueryContext(ctx, `SELECT m.MovementID,m.ProductID,COALESCE(p.ProductName,''),
			m.MovementType,m.Quantity,m.BuyPrice,m.Notes,m.UserID,m.MovementDate
			FROM StockMovements m LEFT JOIN Products p ON p.ProductID=m.ProductID
			WHERE m.ProductID=? ORDER BY m.MovementID DESC LIMIT ?`, productID, limit)
	} else {
		rows, err = s.DB.QueryContext(ctx, `SELECT m.MovementID,m.ProductID,COALESCE(p.ProductName,''),
			m.MovementType,m.Quantity,m.BuyPrice,m.Notes,m.UserID,m.MovementDate
			FROM StockMovements m LEFT JOIN Products p ON p.ProductID=m.ProductID
			ORDER BY m.MovementID DESC LIMIT ?`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*StockMovement
	for rows.Next() {
		var m StockMovement
		var notes sql.NullString
		if err := rows.Scan(&m.ID, &m.ProductID, &m.ProductName, &m.Type, &m.Quantity, &m.BuyPrice, &notes, &m.UserID, &m.Date); err != nil {
			return nil, err
		}
		m.Notes = notes.String
		out = append(out, &m)
	}
	return out, rows.Err()
}

// GetStockLedger is the per-product ledger (P-08), newest first.
func (s *Store) GetStockLedger(ctx context.Context, user *auth.User, productID int64) ([]*StockMovement, error) {
	return s.ListStockMovements(ctx, user, productID, 200)
}

// --- scale settings + barcode decoding (P-13/P-15, S-31) ---

// GetScaleSettings reads ScaleSettings (ID=1); defaults when no row exists.
func (s *Store) GetScaleSettings(ctx context.Context) (ScaleSettings, error) {
	def := ScaleSettings{IsEnabled: true, Prefix: "20", ProductCodeLength: 5,
		ValueLength: 5, Mode: "WeightEmbedded", HasCheckDigit: true,
		WeightUnit: "Kilogram", ValueDecimalPlaces: 3}
	var row struct {
		enabled, hasCheck         int
		prefix, mode, unit, notes sql.NullString
		codeLen, valLen, decimals int
	}
	err := s.DB.QueryRowContext(ctx, `SELECT IsEnabled,BarcodePrefix,ProductCodeLength,
		ValueLength,BarcodeMode,HasCheckDigit,WeightUnit,ValueDecimalPlaces,Notes
		FROM ScaleSettings WHERE ID=1`).Scan(&row.enabled, &row.prefix, &row.codeLen,
		&row.valLen, &row.mode, &row.hasCheck, &row.unit, &row.decimals, &row.notes)
	if err == sql.ErrNoRows {
		return def, nil
	}
	if err != nil {
		return def, err
	}
	def.IsEnabled = row.enabled == 1
	if row.prefix.String != "" {
		def.Prefix = row.prefix.String
	}
	if row.codeLen > 0 {
		def.ProductCodeLength = row.codeLen
	}
	if row.valLen > 0 {
		def.ValueLength = row.valLen
	}
	if row.mode.String != "" {
		def.Mode = normalizeScaleMode(row.mode.String)
	}
	def.HasCheckDigit = row.hasCheck == 1
	if row.unit.String != "" {
		def.WeightUnit = row.unit.String
	}
	def.ValueDecimalPlaces = row.decimals
	def.Notes = row.notes.String
	return def, nil
}

func normalizeScaleMode(v string) string {
	n := strings.ToLower(strings.TrimSpace(v))
	n = strings.ReplaceAll(n, "_", "")
	n = strings.ReplaceAll(n, "-", "")
	n = strings.ReplaceAll(n, " ", "")
	switch n {
	case "priceembedded", "price", "amount", "السعر":
		return "PriceEmbedded"
	default:
		return "WeightEmbedded"
	}
}

// scaleCheckDigitValid implements the EAN 3/1 check digit (weights 3/1
// from the right, expected = (10 - sum%10)%10).
func scaleCheckDigitValid(code string) bool {
	if len(code) < 2 {
		return false
	}
	total := 0
	weight := 3
	for i := len(code) - 2; i >= 0; i-- {
		d := int(code[i] - '0')
		if d < 0 || d > 9 {
			return false
		}
		total += d * weight
		if weight == 3 {
			weight = 1
		} else {
			weight = 3
		}
	}
	return (10-(total%10))%10 == int(code[len(code)-1]-'0')
}

func pow10(n int) int64 {
	v := int64(1)
	for i := 0; i < n; i++ {
		v *= 10
	}
	return v
}

// DecodeScaleBarcode decodes a scale barcode:
// <prefix><PLU code><value><check digit?>. Value is divided by
// 10^ValueDecimalPlaces; WeightEmbedded = kg (Gram divides by 1000),
// PriceEmbedded = total price (weight derived from PricePerKg).
func (s *Store) DecodeScaleBarcode(ctx context.Context, code string) (*ScaleDecode, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, errors.New("باركود الميزان فارغ")
	}
	settings, err := s.GetScaleSettings(ctx)
	if err != nil {
		return nil, err
	}
	if !settings.IsEnabled {
		return nil, errors.New("باركود الميزان غير مفعّل في الإعدادات")
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return nil, errors.New("باركود الميزان يجب أن يكون أرقامًا فقط")
		}
	}
	// Longest matching prefix: global prefix + per-product overrides.
	prefix := ""
	if strings.HasPrefix(code, settings.Prefix) {
		prefix = settings.Prefix
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT DISTINCT ScalePrefixOverride FROM Products
		WHERE IsActive=1 AND IsWeighted=1 AND ScaleEnabled=1
		AND ScalePrefixOverride IS NOT NULL AND ScalePrefixOverride<>''`)
	if err == nil {
		var overrides []string
		for rows.Next() {
			var ov sql.NullString
			if err := rows.Scan(&ov); err != nil {
				continue
			}
			if ov.String != "" {
				overrides = append(overrides, ov.String)
			}
		}
		rows.Close() // close before the PLU lookup below (MaxOpenConns(1))
		for _, ov := range overrides {
			if strings.HasPrefix(code, ov) && len(ov) > len(prefix) {
				prefix = ov
			}
		}
	}
	if prefix == "" {
		return nil, errors.New("باركود الميزان غير صحيح — البادئة غير مطابقة")
	}
	checkLen := 0
	if settings.HasCheckDigit {
		checkLen = 1
	}
	expected := len(prefix) + settings.ProductCodeLength + settings.ValueLength + checkLen
	if len(code) != expected {
		return nil, fmt.Errorf("طول باركود الميزان غير صحيح. الطول المتوقع: %d", expected)
	}
	if settings.HasCheckDigit && !scaleCheckDigitValid(code) {
		return nil, errors.New("رقم التحقق في باركود الميزان غير صحيح.")
	}
	pluRaw := code[len(prefix) : len(prefix)+settings.ProductCodeLength]
	valueRaw := code[len(prefix)+settings.ProductCodeLength : len(prefix)+settings.ProductCodeLength+settings.ValueLength]
	valueNum, err := strconv.ParseInt(valueRaw, 10, 64)
	if err != nil || valueNum <= 0 {
		return nil, errors.New("قيمة الوزن أو السعر داخل باركود الميزان غير صحيحة.")
	}
	// PLU lookup: active + weighted + scale-enabled, raw or zero-trimmed.
	var prodID int64
	var prodName string
	var pricePerKg money.Cents
	var prodMode sql.NullString
	err = s.DB.QueryRowContext(ctx, `SELECT ProductID,ProductName,PricePerKg,ScaleBarcodeType FROM Products
		WHERE IsActive=1 AND IsWeighted=1 AND ScaleEnabled=1 AND (ScalePLU=? OR ScalePLU=?) LIMIT 1`,
		pluRaw, pluTrimmed(pluRaw)).Scan(&prodID, &prodName, &pricePerKg, &prodMode)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("لم يتم العثور على صنف ميزان بالكود PLU: %s", pluTrimmed(pluRaw))
	}
	if err != nil {
		return nil, err
	}
	if pricePerKg <= 0 {
		return nil, fmt.Errorf("سعر الكيلو غير مضبوط للصنف: %s", prodName)
	}
	mode := normalizeScaleMode(prodMode.String)
	if prodMode.String == "" {
		mode = settings.Mode
	}
	divisor := pow10(settings.ValueDecimalPlaces)
	dec := &ScaleDecode{RawBarcode: code, Prefix: prefix, PLU: pluTrimmed(pluRaw),
		ProductID: prodID, ProductName: prodName, Mode: mode}
	if mode == "PriceEmbedded" {
		// value is the total price in EGP with ValueDecimalPlaces decimals.
		// FIX-1 L3: round instead of truncating (valueNum*100/divisor
		// dropped up to 0.99 of a qirsh).
		totalCents := money.Cents((valueNum*100 + divisor/2) / divisor)
		dec.TotalPrice = totalCents
		// weight (milli-kg) = total / pricePerKg, rounded.
		dec.Weight = money.Qty((int64(totalCents)*1000 + int64(pricePerKg)/2) / int64(pricePerKg))
	} else {
		// value is the weight; divisor gives kg with 3 decimals -> milli-kg.
		// FIX-2 L-5: round instead of truncating, as in the
		// PriceEmbedded branch above.
		weightMilli := (valueNum*1000 + divisor/2) / divisor
		if strings.EqualFold(settings.WeightUnit, "gram") {
			weightMilli = (weightMilli + 500) / 1000
		}
		dec.Weight = money.Qty(weightMilli)
		dec.TotalPrice = money.MulQty(pricePerKg, dec.Weight)
	}
	if dec.Weight <= 0 || dec.TotalPrice <= 0 {
		return nil, errors.New("الوزن أو الإجمالي الناتج من باركود الميزان غير صحيح.")
	}
	return dec, nil
}

// PriceForSaleType returns the catalog price for a sale type.
func PriceForSaleType(p *Product, saleType string) money.Cents {
	switch saleType {
	case "wholesale":
		return p.WholesalePrice
	case "bulk":
		return p.BulkPrice
	default:
		return p.RetailPrice
	}
}
