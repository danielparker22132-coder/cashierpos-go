// domain_promos.go — offers / promotions (FEATURE_MAP §19, R-P1..R-P9).
//
// Offer types: product_pct, category_pct, invoice_fixed, buy_x_get_y,
// bundle_fixed, threshold_pct, plus the coordinator-required 8th type
// product_fixed (fixed amount off a product's price, per unit) and the 9th type
// product_price (promo price: the final sale price per unit, R-P11).
//
// Storage (schema Offers/ OfferTargets / InvoiceOffers):
//
//	Value: pct (1..100) for *_pct; cents for *_fixed / bundle_fixed
//	(bundle price) / product_price (final promo price per unit);
//	product_fixed is a per-unit amount: the saving on a line is
//	Value × qty, capped at the line total (R-P10).
//	X (paid units per group) for buy_x_get_y.
//	Threshold: cents for threshold_pct. FreeQty: Y (free units per
//	group) for buy_x_get_y. OfferTargets.Qty is milliunits.
//
// Money rules R-P1..R-P9 are implemented in EvaluateOffers; see its doc.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/money"
	"cashierpos-go/internal/rbac"
)

// Offer types.
const (
	OfferProductPct   = "product_pct"
	OfferCategoryPct  = "category_pct"
	OfferInvoiceFixed = "invoice_fixed"
	OfferBuyXGetY     = "buy_x_get_y"
	OfferBundleFixed  = "bundle_fixed"
	OfferThresholdPct = "threshold_pct"
	OfferProductFixed = "product_fixed"
	OfferProductPrice = "product_price"
)

// OfferTypeArabic renders an offer type in Arabic.
func OfferTypeArabic(t string) string {
	switch t {
	case OfferProductPct:
		return "خصم % على منتج"
	case OfferCategoryPct:
		return "خصم % على فئة"
	case OfferInvoiceFixed:
		return "خصم ثابت على الفاتورة"
	case OfferBuyXGetY:
		return "اشترِ واحصل مجانًا"
	case OfferBundleFixed:
		return "سعر ثابت لحزمة"
	case OfferThresholdPct:
		return "خصم % عند حد أدنى"
	case OfferProductFixed:
		return "خصم مبلغ ثابت على منتج"
	case OfferProductPrice:
		return "سعر ترويجي على منتج"
	default:
		return t
	}
}

// Offer is one Offers row with its targets.
type Offer struct {
	ID        int64
	Name      string
	Type      string
	Value     string // raw stored value (see file doc)
	Threshold string // raw stored threshold (cents for threshold_pct)
	FreeQty   string // raw stored free qty (Y units for buy_x_get_y)
	StartAt   string
	EndAt     string
	IsActive  bool
	CreatedBy string
	CreatedAt string
	Targets   []OfferTarget
}

// OfferTarget is one OfferTargets row.
type OfferTarget struct {
	TargetType string // product | category
	TargetID   int64
	TargetName string // category NAME for category targets
	Qty        money.Qty
}

// OfferInput carries create/update fields.
// Percent: for *_pct types. Amount: cents for *_fixed / bundle price /
// product_price (the final promo price per unit, R-P11).
// BuyX / FreeY: units (not milli) for buy_x_get_y.
type OfferInput struct {
	Name      string
	Type      string
	Percent   int64
	Amount    money.Cents
	Threshold money.Cents
	BuyX      int64
	FreeY     int64
	StartAt   string // "2006-01-02" or "2006-01-02 15:04:05" or ""
	EndAt     string
	IsActive  bool
	Targets   []OfferTarget
}

// AppliedOffer is one offer applied to an invoice.
type AppliedOffer struct {
	OfferID   int64
	Name      string
	Saved     money.Cents
	ProductID int64
	BuyQty    money.Qty // X per group (bxgy only)
	FreeQty   money.Qty // Y per group (bxgy only)
}

// BxgyInfo is the persisted buy_x_get_y grant for one product.
type BxgyInfo struct {
	OfferID int64
	Name    string
	FreeQty money.Qty // Y per group
	BuyX    money.Qty // X per group
}

// EarnedFreeUnits returns floor(keptPaidQty / X) * Y (R-P9).
// All quantities are milliunits. Exported for the returns builder (B2).
func EarnedFreeUnits(paidQty, x, y money.Qty) money.Qty {
	if paidQty <= 0 || x <= 0 || y <= 0 {
		return 0
	}
	return (paidQty / x) * y
}

// --- value parsing helpers (stored TEXT is tolerant: int cents, or
// a decimal EGP string from a legacy PyQt row) ---

func parseCentsText(s string) (money.Cents, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return money.Cents(n), nil
	}
	return money.ParseMoney(s)
}

func parseQtyText(s string) (money.Qty, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return money.Qty(n), nil
	}
	return money.ParseQty(s)
}

func parsePctText(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	return int64(f), nil
}

// offerValue returns the parsed Value: pct for *_pct, cents for
// *_fixed/bundle, X units for buy_x_get_y.
func (o *Offer) offerValue() (pct int64, amount money.Cents, x int64, err error) {
	switch o.Type {
	case OfferProductPct, OfferCategoryPct, OfferThresholdPct:
		pct, err = parsePctText(o.Value)
	case OfferInvoiceFixed, OfferBundleFixed, OfferProductFixed, OfferProductPrice:
		amount, err = parseCentsText(o.Value)
	case OfferBuyXGetY:
		var n int64
		n, err = strconv.ParseInt(strings.TrimSpace(o.Value), 10, 64)
		x = n
	}
	return pct, amount, x, err
}

func (o *Offer) offerFreeY() (int64, error) {
	s := strings.TrimSpace(o.FreeQty)
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		q, qerr := parseQtyText(s)
		if qerr != nil {
			return 0, qerr
		}
		return int64(q / 1000), nil
	}
	return n, nil
}

func (o *Offer) offerThreshold() (money.Cents, error) {
	return parseCentsText(o.Threshold)
}

// IsOfferLive reports whether the offer is active and now is inside
// [StartAt, EndAt]. A date-only EndAt covers the whole day.
func IsOfferLive(o *Offer, now string) bool {
	if o == nil || !o.IsActive {
		return false
	}
	if o.StartAt != "" && now < normalizeStart(o.StartAt) {
		return false
	}
	if o.EndAt != "" && now > normalizeEnd(o.EndAt) {
		return false
	}
	return true
}

// OfferStatus returns نشط / معطّل / قادم / منتهٍ.
func OfferStatus(o *Offer, now string) string {
	if o == nil {
		return ""
	}
	if !o.IsActive {
		return "معطّل"
	}
	if o.StartAt != "" && now < normalizeStart(o.StartAt) {
		return "قادم"
	}
	if o.EndAt != "" && now > normalizeEnd(o.EndAt) {
		return "منتهٍ"
	}
	return "نشط"
}

func normalizeStart(v string) string {
	v = strings.TrimSpace(v)
	if len(v) == 10 {
		return v + " 00:00:00"
	}
	return v
}

func normalizeEnd(v string) string {
	v = strings.TrimSpace(v)
	if len(v) == 10 {
		return v + " 23:59:59"
	}
	return v
}

func validateOfferWindow(startAt, endAt string) error {
	if startAt != "" {
		if _, err := time.Parse("2006-01-02 15:04:05", normalizeStart(startAt)); err != nil {
			if _, err2 := time.Parse("2006-01-02", startAt); err2 != nil {
				return errors.New("تاريخ بداية العرض غير صحيح — استخدم الصيغة YYYY-MM-DD")
			}
		}
	}
	if endAt != "" {
		if _, err := time.Parse("2006-01-02 15:04:05", normalizeEnd(endAt)); err != nil {
			if _, err2 := time.Parse("2006-01-02", endAt); err2 != nil {
				return errors.New("تاريخ نهاية العرض غير صحيح — استخدم الصيغة YYYY-MM-DD")
			}
		}
	}
	if startAt != "" && endAt != "" && normalizeStart(startAt) > normalizeEnd(endAt) {
		return errors.New("تاريخ بداية العرض يجب أن يكون قبل تاريخ النهاية")
	}
	return nil
}

func validateOfferInput(in OfferInput) error {
	if strings.TrimSpace(in.Name) == "" {
		return errors.New("اسم العرض مطلوب")
	}
	switch in.Type {
	case OfferProductPct, OfferCategoryPct, OfferThresholdPct:
		if in.Percent <= 0 || in.Percent > 100 {
			return errors.New("نسبة الخصم يجب أن تكون بين 1 و 100")
		}
	case OfferInvoiceFixed, OfferProductFixed:
		if in.Amount <= 0 {
			return errors.New("مبلغ الخصم الثابت يجب أن يكون أكبر من صفر")
		}
	case OfferProductPrice:
		// R-P11: 0 is allowed (free); the < retail-price check needs
		// the DB and lives in checkProductPriceTx.
		if in.Amount < 0 {
			return errors.New("سعر العرض الترويجي لا يمكن أن يكون سالبًا")
		}
	case OfferBundleFixed:
		if in.Amount < 0 {
			return errors.New("سعر الحزمة غير صحيح")
		}
	case OfferBuyXGetY:
		if in.BuyX < 1 {
			return errors.New("عدد الوحدات المدفوعة يجب أن يكون عددًا صحيحًا أكبر من صفر")
		}
		if in.FreeY < 1 {
			return errors.New("عدد الوحدات المجانية يجب أن يكون عددًا صحيحًا أكبر من صفر")
		}
	default:
		return errors.New("نوع العرض غير صحيح")
	}
	if in.Type == OfferThresholdPct && in.Threshold <= 0 {
		return errors.New("الحد الأدنى للفاتورة يجب أن يكون أكبر من صفر")
	}
	if err := validateOfferWindow(in.StartAt, in.EndAt); err != nil {
		return err
	}
	prodTargets := 0
	catTargets := 0
	for _, t := range in.Targets {
		if t.TargetType == "product" {
			prodTargets++
		}
		if t.TargetType == "category" && strings.TrimSpace(t.TargetName) != "" {
			catTargets++
		}
	}
	switch in.Type {
	case OfferProductPct, OfferProductFixed, OfferProductPrice:
		if prodTargets == 0 {
			return errors.New("اختر منتجًا واحدًا على الأقل للعرض")
		}
	case OfferCategoryPct:
		if catTargets == 0 {
			return errors.New("اختر فئة واحدة على الأقل للعرض")
		}
	case OfferBuyXGetY:
		if prodTargets != 1 {
			return errors.New("عرض «اشترِ واحصل مجانًا» يحتاج منتجًا واحدًا فقط")
		}
	case OfferBundleFixed:
		if prodTargets < 2 {
			return errors.New("الحزمة تحتاج منتجين على الأقل")
		}
		for _, t := range in.Targets {
			if t.TargetType == "product" && t.Qty < 1000 {
				return errors.New("كمية منتج الحزمة يجب أن تكون أكبر من صفر")
			}
		}
	}
	return nil
}

// offerStoredValue renders the Value column for an input.
func offerStoredValue(in OfferInput) string {
	switch in.Type {
	case OfferProductPct, OfferCategoryPct, OfferThresholdPct:
		return strconv.FormatInt(in.Percent, 10)
	case OfferInvoiceFixed, OfferBundleFixed, OfferProductFixed, OfferProductPrice:
		return strconv.FormatInt(int64(in.Amount), 10)
	case OfferBuyXGetY:
		return strconv.FormatInt(in.BuyX, 10)
	}
	return "0"
}

func offerStoredThreshold(in OfferInput) string {
	return strconv.FormatInt(int64(in.Threshold), 10)
}

func offerStoredFreeQty(in OfferInput) string {
	if in.Type == OfferBuyXGetY {
		return strconv.FormatInt(in.FreeY, 10)
	}
	return "0"
}

func scanOffer(row *sql.Row) (*Offer, error) {
	var o Offer
	var value, threshold, freeQty, startAt, endAt, createdBy, createdAt sql.NullString
	var isActive int
	err := row.Scan(&o.ID, &o.Name, &o.Type, &value, &threshold, &freeQty,
		&startAt, &endAt, &isActive, &createdBy, &createdAt)
	if err != nil {
		return nil, err
	}
	o.Value = value.String
	o.Threshold = threshold.String
	o.FreeQty = freeQty.String
	o.StartAt = startAt.String
	o.EndAt = endAt.String
	o.IsActive = isActive == 1
	o.CreatedBy = createdBy.String
	o.CreatedAt = createdAt.String
	return &o, nil
}

func loadOfferTargets(db *sql.DB, offerID int64) ([]OfferTarget, error) {
	rows, err := db.Query(`SELECT TargetType,TargetID,TargetName,Qty FROM OfferTargets WHERE OfferID=?`, offerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OfferTarget
	for rows.Next() {
		var t OfferTarget
		var name, qtyStr sql.NullString
		var targetID sql.NullInt64
		if err := rows.Scan(&t.TargetType, &targetID, &name, &qtyStr); err != nil {
			return nil, err
		}
		t.TargetID = targetID.Int64
		t.TargetName = name.String
		q, err := parseQtyText(qtyStr.String)
		if err != nil {
			return nil, err
		}
		t.Qty = q
		out = append(out, t)
	}
	return out, rows.Err()
}

func loadOfferTargetsTx(tx *sql.Tx, offerID int64) ([]OfferTarget, error) {
	rows, err := tx.Query(`SELECT TargetType,TargetID,TargetName,Qty FROM OfferTargets WHERE OfferID=?`, offerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OfferTarget
	for rows.Next() {
		var t OfferTarget
		var name, qtyStr sql.NullString
		var targetID sql.NullInt64
		if err := rows.Scan(&t.TargetType, &targetID, &name, &qtyStr); err != nil {
			return nil, err
		}
		t.TargetID = targetID.Int64
		t.TargetName = name.String
		q, err := parseQtyText(qtyStr.String)
		if err != nil {
			return nil, err
		}
		t.Qty = q
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) loadOffer(ctx context.Context, id int64) (*Offer, error) {
	o, err := scanOffer(s.DB.QueryRowContext(ctx, `SELECT OfferID,Name,Type,Value,Threshold,FreeQty,
		StartAt,EndAt,IsActive,CreatedBy,CreatedAt FROM Offers WHERE OfferID=?`, id))
	if err == sql.ErrNoRows {
		return nil, errors.New("العرض غير موجود")
	}
	if err != nil {
		return nil, err
	}
	targets, err := loadOfferTargets(s.DB, id)
	if err != nil {
		return nil, err
	}
	o.Targets = targets
	return o, nil
}

// CreateOffer creates an offer (Admin/Manager only).
func (s *Store) CreateOffer(ctx context.Context, user *auth.User, in OfferInput) (*Offer, error) {
	if err := rbac.Require(user.Role, rbac.PermManagePromotions); err != nil {
		return nil, err
	}
	if err := validateOfferInput(in); err != nil {
		return nil, err
	}
	var id int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		if err := checkOfferTargetsTx(tx, in.Targets); err != nil {
			return err
		}
		if err := checkProductPriceTx(tx, in); err != nil {
			return err
		}
		res, err := tx.Exec(`INSERT INTO Offers(Name,Type,Value,Threshold,FreeQty,StartAt,EndAt,IsActive,CreatedBy,CreatedAt)
			VALUES(?,?,?,?,?,?,?,?,?,?)`, strings.TrimSpace(in.Name), in.Type,
			offerStoredValue(in), offerStoredThreshold(in), offerStoredFreeQty(in),
			nullableStr(normalizeStart(in.StartAt)), nullableStr(normalizeEnd(in.EndAt)),
			boolInt(in.IsActive), user.UserName, Now())
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return err
		}
		if err := writeOfferTargetsTx(tx, id, in.Targets); err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "إنشاء عرض", "عروض", nil, 0, 0, "",
			fmt.Sprintf("إنشاء عرض: %s (%s)", strings.TrimSpace(in.Name), OfferTypeArabic(in.Type)))
	})
	if err != nil {
		return nil, err
	}
	offer, err := s.GetOffer(ctx, user, id)
	if err != nil {
		return nil, err
	}
	// G-8: offer created live — fire once (NotifyOffer dedupes per offer
	// id). An inactive or not-yet-started offer fires nothing now; it
	// fires when it newly becomes live (UpdateOffer / SetOfferActive).
	// Best effort: notification failure must never fail the create.
	if IsOfferLive(offer, Now()) {
		_, _ = s.NotifyOffer(ctx, user, offer.ID, offer.Name)
	}
	return offer, nil
}

// nullableStr is defined in domain_payables.go (shared helper).

func checkOfferTargetsTx(tx *sql.Tx, targets []OfferTarget) error {
	for _, t := range targets {
		if t.TargetType == "product" && t.TargetID > 0 {
			var n int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM Products WHERE ProductID=?`, t.TargetID).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return errors.New("أحد منتجات العرض غير موجود")
			}
		}
	}
	return nil
}

// checkProductPriceTx enforces R-P11: a product_price promo price must
// be strictly below the CURRENT retail price of EVERY targeted product
// (0 = free is allowed). FIX-3 L5: a non-zero promo price must also not
// fall below the product's BuyPrice (0 = free stays exempt — the
// "0 = free" test and bxgy-free semantics rely on it). Called after
// checkOfferTargetsTx, so every target exists.
func checkProductPriceTx(tx *sql.Tx, in OfferInput) error {
	if in.Type != OfferProductPrice {
		return nil
	}
	for _, t := range in.Targets {
		if t.TargetType != "product" || t.TargetID <= 0 {
			continue
		}
		var retail, buy int64
		if err := tx.QueryRow(`SELECT RetailPrice, BuyPrice FROM Products WHERE ProductID=?`, t.TargetID).Scan(&retail, &buy); err != nil {
			return err
		}
		if int64(in.Amount) >= retail {
			return errors.New("سعر العرض الترويجي يجب أن يكون أقل من سعر البيع الحالي للمنتج")
		}
		if in.Amount > 0 && int64(in.Amount) < buy {
			return errors.New("سعر العرض الترويجي لا يمكن أن يكون أقل من سعر الشراء للمنتج")
		}
	}
	return nil
}

func writeOfferTargetsTx(tx *sql.Tx, offerID int64, targets []OfferTarget) error {
	if _, err := tx.Exec(`DELETE FROM OfferTargets WHERE OfferID=?`, offerID); err != nil {
		return err
	}
	for _, t := range targets {
		qty := t.Qty
		if qty == 0 {
			qty = 1000
		}
		if _, err := tx.Exec(`INSERT INTO OfferTargets(OfferID,TargetType,TargetID,TargetName,Qty)
			VALUES(?,?,?,?,?)`, offerID, t.TargetType, t.TargetID, t.TargetName, strconv.FormatInt(int64(qty), 10)); err != nil {
			return err
		}
	}
	return nil
}

// UpdateOffer edits an offer (Admin/Manager only). Targets replace.
func (s *Store) UpdateOffer(ctx context.Context, user *auth.User, id int64, in OfferInput) (*Offer, error) {
	if err := rbac.Require(user.Role, rbac.PermManagePromotions); err != nil {
		return nil, err
	}
	if err := validateOfferInput(in); err != nil {
		return nil, err
	}
	// G-8: liveness before the edit — only a newly-live offer fires
	// (an edit that only changes dates on an already-live offer must
	// not re-fire; NotifyOffer's per-offer dedupe is the backstop).
	before, err := s.loadOffer(ctx, id)
	if err != nil {
		return nil, err
	}
	wasLive := IsOfferLive(before, Now())
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM Offers WHERE OfferID=?`, id).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return errors.New("العرض غير موجود")
		}
		if err := checkOfferTargetsTx(tx, in.Targets); err != nil {
			return err
		}
		if err := checkProductPriceTx(tx, in); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE Offers SET Name=?,Type=?,Value=?,Threshold=?,FreeQty=?,
			StartAt=?,EndAt=?,IsActive=? WHERE OfferID=?`, strings.TrimSpace(in.Name), in.Type,
			offerStoredValue(in), offerStoredThreshold(in), offerStoredFreeQty(in),
			nullableStr(normalizeStart(in.StartAt)), nullableStr(normalizeEnd(in.EndAt)),
			boolInt(in.IsActive), id); err != nil {
			return err
		}
		if err := writeOfferTargetsTx(tx, id, in.Targets); err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "تعديل عرض", "عروض", nil, 0, 0, "",
			fmt.Sprintf("تعديل عرض: %s", strings.TrimSpace(in.Name)))
	})
	if err != nil {
		return nil, err
	}
	after, err := s.GetOffer(ctx, user, id)
	if err != nil {
		return nil, err
	}
	// G-8: fire only when the edit newly activates the offer.
	if !wasLive && IsOfferLive(after, Now()) {
		_, _ = s.NotifyOffer(ctx, user, after.ID, after.Name)
	}
	return after, nil
}

// SetOfferActive activates / deactivates an offer (Admin/Manager only).
func (s *Store) SetOfferActive(ctx context.Context, user *auth.User, id int64, active bool) (*Offer, error) {
	if err := rbac.Require(user.Role, rbac.PermManagePromotions); err != nil {
		return nil, err
	}
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		var name string
		err := tx.QueryRow(`SELECT Name FROM Offers WHERE OfferID=?`, id).Scan(&name)
		if err == sql.ErrNoRows {
			return errors.New("العرض غير موجود")
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE Offers SET IsActive=? WHERE OfferID=?`, boolInt(active), id); err != nil {
			return err
		}
		action := "تعطيل عرض"
		if active {
			action = "تفعيل عرض"
		}
		return AuditTx(tx, user.ID, user.UserName, action, "عروض", nil, 0, 0, "",
			fmt.Sprintf("%s عرض: %s", map[bool]string{true: "تفعيل", false: "تعطيل"}[active], name))
	})
	if err != nil {
		return nil, err
	}
	offer, err := s.GetOffer(ctx, user, id)
	if err != nil {
		return nil, err
	}
	// G-8: activation of a live offer fires once (NotifyOffer dedupes
	// per offer id, so re-activating an already-announced offer sends
	// nothing new). Activating outside its window fires nothing.
	if active && IsOfferLive(offer, Now()) {
		_, _ = s.NotifyOffer(ctx, user, offer.ID, offer.Name)
	}
	return offer, nil
}

// DeleteOffer hard-deletes ONLY an offer never used in any invoice.
// A used offer raises the disable-instead message (§19 delete rule).
func (s *Store) DeleteOffer(ctx context.Context, user *auth.User, id int64) error {
	if err := rbac.Require(user.Role, rbac.PermManagePromotions); err != nil {
		return err
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		var name string
		err := tx.QueryRow(`SELECT Name FROM Offers WHERE OfferID=?`, id).Scan(&name)
		if err == sql.ErrNoRows {
			return errors.New("العرض غير موجود")
		}
		if err != nil {
			return err
		}
		var used int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM InvoiceOffers WHERE OfferID=?`, id).Scan(&used); err != nil {
			return err
		}
		if used > 0 {
			return errors.New("لا يمكن حذف العرض لأنه مستخدم في فواتير سابقة — عطّل العرض بدلًا من حذفه")
		}
		if _, err := tx.Exec(`DELETE FROM OfferTargets WHERE OfferID=?`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM Offers WHERE OfferID=?`, id); err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "حذف عرض", "عروض", nil, 0, 0, "",
			fmt.Sprintf("حذف عرض: %s", name))
	})
}

// GetOffer returns one offer with targets (any role may view offers
// that apply at sale time; management is gated in the mutating calls).
func (s *Store) GetOffer(ctx context.Context, user *auth.User, id int64) (*Offer, error) {
	return s.loadOffer(ctx, id)
}

// ListOffers lists offers, newest first. status filter: all | active
// (نشط) | expired (منتهٍ) | upcoming (قادم) | inactive (معطّل).
// FIX-1 M7: login is enforced in the service layer; offers are viewable
// by any role at sale time (management is gated in the mutating calls).
func (s *Store) ListOffers(ctx context.Context, user *auth.User, status string) ([]*Offer, error) {
	if err := requireUser(user, ""); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT OfferID,Name,Type,Value,Threshold,FreeQty,
		StartAt,EndAt,IsActive,CreatedBy,CreatedAt FROM Offers ORDER BY OfferID DESC`)
	if err != nil {
		return nil, err
	}
	// Collect all rows first, then close before loading targets:
	// MaxOpenConns(1) deadlocks on a nested query while Rows is open.
	type rawOffer struct {
		o         Offer
		value     string
		threshold string
		freeQty   string
		startAt   string
		endAt     string
		createdBy string
		createdAt string
		isActive  int
	}
	var raw []rawOffer
	for rows.Next() {
		var r rawOffer
		var value, threshold, freeQty, startAt, endAt, createdBy, createdAt sql.NullString
		if err := rows.Scan(&r.o.ID, &r.o.Name, &r.o.Type, &value, &threshold, &freeQty,
			&startAt, &endAt, &r.isActive, &createdBy, &createdAt); err != nil {
			rows.Close()
			return nil, err
		}
		r.value = value.String
		r.threshold = threshold.String
		r.freeQty = freeQty.String
		r.startAt = startAt.String
		r.endAt = endAt.String
		r.createdBy = createdBy.String
		r.createdAt = createdAt.String
		raw = append(raw, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	now := Now()
	var out []*Offer
	for _, r := range raw {
		o := r.o
		o.Value = r.value
		o.Threshold = r.threshold
		o.FreeQty = r.freeQty
		o.StartAt = r.startAt
		o.EndAt = r.endAt
		o.IsActive = r.isActive == 1
		o.CreatedBy = r.createdBy
		o.CreatedAt = r.createdAt
		st := OfferStatus(&o, now)
		switch status {
		case "", "all":
		case "active":
			if st != "نشط" {
				continue
			}
		case "expired":
			if st != "منتهٍ" {
				continue
			}
		case "upcoming":
			if st != "قادم" {
				continue
			}
		case "inactive":
			if st != "معطّل" {
				continue
			}
		default:
			return nil, errors.New("حالة العرض غير صحيحة")
		}
		targets, err := loadOfferTargets(s.DB, o.ID)
		if err != nil {
			return nil, err
		}
		o.Targets = targets
		out = append(out, &o)
	}
	return out, nil
}

// GetInvoiceOffers returns the applied offers of one invoice.
func (s *Store) GetInvoiceOffers(ctx context.Context, invoiceID int64) ([]AppliedOffer, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT OfferID,OfferName,SavedAmount,ProductID,BuyQty,FreeQty
		FROM InvoiceOffers WHERE InvoiceID=? ORDER BY rowid`, invoiceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppliedOffer
	for rows.Next() {
		var a AppliedOffer
		var pid, buyQty, freeQty sql.NullInt64
		if err := rows.Scan(&a.OfferID, &a.Name, &a.Saved, &pid, &buyQty, &freeQty); err != nil {
			return nil, err
		}
		a.ProductID = pid.Int64
		a.BuyQty = money.Qty(buyQty.Int64)
		a.FreeQty = money.Qty(freeQty.Int64)
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetInvoiceBxgy returns the granted buy_x_get_y per product on one
// invoice: {product_id: {offer_id, name, free_qty (Y per group), buy_x}}.
func (s *Store) GetInvoiceBxgy(ctx context.Context, invoiceID int64) (map[int64]BxgyInfo, error) {
	offers, err := s.GetInvoiceOffers(ctx, invoiceID)
	if err != nil {
		return nil, err
	}
	out := map[int64]BxgyInfo{}
	for _, a := range offers {
		if a.ProductID <= 0 || a.FreeQty <= 0 || a.BuyQty <= 0 {
			continue
		}
		prev, ok := out[a.ProductID]
		if !ok || a.FreeQty > prev.FreeQty {
			out[a.ProductID] = BxgyInfo{OfferID: a.OfferID, Name: a.Name, FreeQty: a.FreeQty, BuyX: a.BuyQty}
		}
	}
	return out, nil
}

// --- evaluation engine (R-P1..R-P9) ---

// PromoLine is one prepared sale line for evaluation.
type PromoLine struct {
	ProductID    int64
	Category     string
	Qty          money.Qty
	UnitPrice    money.Cents
	LineGross    money.Cents
	ItemDiscount money.Cents
	LineTotal    money.Cents // after manual item discount
	BuyPrice     money.Cents
}

// MergePromoLines merges duplicate-product promo lines into one line
// per product (FIX-3 duplicate rule, jony 2026-11-25) — the same rule
// prepareSaleLines applies at creation, so the preview and the saved
// invoice agree. Qty, gross, item discount and line total are summed
// exactly; the unit price becomes the weighted average when the
// additions had different prices.
func MergePromoLines(lines []PromoLine) []PromoLine {
	merged := map[int64]*PromoLine{}
	var order []int64
	for _, ln := range lines {
		if m, ok := merged[ln.ProductID]; ok {
			m.Qty += ln.Qty
			m.LineGross += ln.LineGross
			m.ItemDiscount += ln.ItemDiscount
			m.LineTotal += ln.LineTotal
			if ln.UnitPrice != m.UnitPrice {
				m.UnitPrice = avgUnitPrice(m.LineGross, m.Qty)
			}
		} else {
			cp := ln
			merged[ln.ProductID] = &cp
			order = append(order, ln.ProductID)
		}
	}
	out := make([]PromoLine, 0, len(order))
	for _, id := range order {
		out = append(out, *merged[id])
	}
	return out
}
// invoice-level offer BEFORE the R-P3 cap (the caller caps it against
// the remainder after the manual invoice discount).
type PromoResult struct {
	LinePromoTotal   money.Cents
	InvoicePromo     money.Cents // 0 here; caller sets it after capping InvoiceBest
	Applied          []AppliedOffer
	FreeUnits        map[int64]money.Qty
	FreeCost         money.Cents
	TotalSaved       money.Cents
	LinePromos       map[int]money.Cents
	// LinePrice (FIX-3 L6): per-line winning product_price promo price,
	// by line index — set only when a product_price offer is the final
	// winner on that line AND its saving is uncapped (saved == the raw
	// (unitPrice − promoPrice) × qty). A capped saving keeps the
	// discount form, so repricing can never change the net. Lines
	// displaced by a bundle or bxgy are never listed here.
	LinePrice        map[int]money.Cents
	Bxgy             map[int64]BxgyInfo
	InvoiceBest      money.Cents
	InvoiceBestOffer *Offer
}

func pctOf(amount money.Cents, pct int64) money.Cents {
	// round half away from zero (amounts are >= 0 here)
	return money.Cents((int64(amount)*pct + 50) / 100)
}

// EvaluateOffers picks the best offers for prepared lines (no DB writes).
// Lines must already be merged and carry LineTotal after manual discounts.
func (s *Store) EvaluateOffers(ctx context.Context, lines []PromoLine) (*PromoResult, error) {
	now := Now()
	all, err := s.ListOffers(ctx, &auth.User{Role: "admin"}, "all")
	if err != nil {
		return nil, err
	}
	var offers []*Offer
	for _, o := range all {
		if IsOfferLive(o, now) {
			offers = append(offers, o)
		}
	}
	res := &PromoResult{
		FreeUnits:  map[int64]money.Qty{},
		LinePromos: map[int]money.Cents{},
		Bxgy:       map[int64]BxgyInfo{},
	}
	if len(offers) == 0 || len(lines) == 0 {
		return res, nil
	}
	paidQty := map[int64]money.Qty{}
	lowPrice := map[int64]money.Cents{}
	buyPrice := map[int64]money.Cents{}
	for _, ln := range lines {
		paidQty[ln.ProductID] += ln.Qty
		if p, ok := lowPrice[ln.ProductID]; !ok || ln.UnitPrice < p {
			lowPrice[ln.ProductID] = ln.UnitPrice
		}
		buyPrice[ln.ProductID] = ln.BuyPrice
	}
	// Best single line promo per line (R-P1/R-P2).
	bestLine := map[int]struct {
		saved money.Cents
		offer *Offer
	}{}
	for idx, ln := range lines {
		var bestSaved money.Cents
		var bestOffer *Offer
		for _, o := range offers {
			var saved money.Cents
			switch o.Type {
			case OfferProductPct:
				if !offerTargetsProduct(o, ln.ProductID) {
					continue
				}
				pct, _, _, err := o.offerValue()
				if err != nil {
					continue
				}
				saved = pctOf(ln.LineTotal, pct)
			case OfferCategoryPct:
				if ln.Category == "" || !offerTargetsCategory(o, ln.Category) {
					continue
				}
				pct, _, _, err := o.offerValue()
				if err != nil {
					continue
				}
				saved = pctOf(ln.LineTotal, pct)
			case OfferProductFixed:
				// R-P10: fixed amount off the product's PRICE, PER
				// UNIT — the saving on a line is amount × qty,
				// capped at the line total below.
				if !offerTargetsProduct(o, ln.ProductID) {
					continue
				}
				_, amount, _, err := o.offerValue()
				if err != nil {
					continue
				}
				saved = money.MulQty(amount, ln.Qty)
			case OfferProductPrice:
				// R-P11: the promo price is the FINAL sale price per
				// unit — per-unit saving = current unit price −
				// promo price, floored at 0 (a later price drop to
				// or below the promo price saves nothing), times
				// the qty, capped at the line total below.
				if !offerTargetsProduct(o, ln.ProductID) {
					continue
				}
				_, amount, _, err := o.offerValue()
				if err != nil {
					continue
				}
				unitSaved := ln.UnitPrice - amount
				if unitSaved < 0 {
					unitSaved = 0
				}
				saved = money.MulQty(unitSaved, ln.Qty)
			default:
				continue
			}
			if saved > ln.LineTotal {
				saved = ln.LineTotal
			}
			if saved > bestSaved {
				bestSaved, bestOffer = saved, o
			}
		}
		if bestOffer != nil && bestSaved > 0 {
			bestLine[idx] = struct {
				saved money.Cents
				offer *Offer
			}{bestSaved, bestOffer}
		}
	}
	// Bundles (R-P5): compete with the line promos on their lines.
	type bundleWin struct {
		saved    money.Cents
		offer    *Offer
		lineIdxs map[int]bool
		portions map[int]money.Cents
		total    money.Cents
	}
	var bundleWinners []bundleWin
	for _, o := range offers {
		if o.Type != OfferBundleFixed {
			continue
		}
		var targets []OfferTarget
		for _, t := range o.Targets {
			if t.TargetType == "product" {
				targets = append(targets, t)
			}
		}
		if len(targets) < 2 {
			continue
		}
		ok := true
		for _, t := range targets {
			if paidQty[t.TargetID] < t.Qty {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		bundleTotal := money.Cents(0)
		lineIdxs := map[int]bool{}
		portions := map[int]money.Cents{}
		for _, t := range targets {
			need := t.Qty
			for idx, ln := range lines {
				if need <= 0 {
					break
				}
				if ln.ProductID != t.TargetID {
					continue
				}
				take := ln.Qty
				if take > need {
					take = need
				}
				portion := money.Cents(int64(ln.LineTotal) * int64(take) / int64(ln.Qty))
				bundleTotal += portion
				lineIdxs[idx] = true
				portions[idx] += portion
				need -= take
			}
		}
		_, bundlePrice, _, err := o.offerValue()
		if err != nil {
			continue
		}
		saved := bundleTotal - bundlePrice
		if saved <= 0 {
			continue
		}
		var displaced money.Cents
		for idx := range lineIdxs {
			if b, ok := bestLine[idx]; ok {
				displaced += b.saved
			}
		}
		if saved > displaced {
			bundleWinners = append(bundleWinners, bundleWin{saved, o, lineIdxs, portions, bundleTotal})
		}
	}
	sort.Slice(bundleWinners, func(i, j int) bool { return bundleWinners[i].saved > bundleWinners[j].saved })
	finalPromo := map[int]money.Cents{}
	for idx, b := range bestLine {
		finalPromo[idx] = b.saved
	}
	bestSnapshot := map[int]money.Cents{}
	for idx, v := range finalPromo {
		bestSnapshot[idx] = v
	}
	winningBundles := map[int64]bundleWin{}
	usedLines := map[int]bool{}
	for _, bw := range bundleWinners {
		overlap := false
		for idx := range bw.lineIdxs {
			if usedLines[idx] {
				overlap = true
			}
		}
		if overlap {
			continue
		}
		for idx := range bw.lineIdxs {
			usedLines[idx] = true
		}
		// Allocate the saving pro-rata; remainder on the last line.
		alloc := map[int]money.Cents{}
		remainder := bw.saved
		var ordered []int
		for idx := range bw.lineIdxs {
			ordered = append(ordered, idx)
		}
		sort.Slice(ordered, func(i, j int) bool { return bw.portions[ordered[i]] > bw.portions[ordered[j]] })
		for pos, idx := range ordered {
			if pos == len(ordered)-1 {
				alloc[idx] = remainder
			} else {
				share := money.Cents(0)
				if bw.total > 0 {
					share = money.Cents(int64(bw.saved) * int64(bw.portions[idx]) / int64(bw.total))
				}
				alloc[idx] = share
				remainder -= share
			}
		}
		for idx, amount := range alloc {
			finalPromo[idx] = amount
		}
		winningBundles[bw.offer.ID] = bw
	}
	// buy_x_get_y (R-P1/R-P4/R-P6): competes with line/bundle promo.
	type bxgyCand struct {
		free  money.Qty
		saved money.Cents
		offer *Offer
		x     int64
		y     int64
	}
	bxgyBest := map[int64]bxgyCand{}
	for _, o := range offers {
		if o.Type != OfferBuyXGetY {
			continue
		}
		var target *OfferTarget
		for i := range o.Targets {
			if o.Targets[i].TargetType == "product" {
				target = &o.Targets[i]
				break
			}
		}
		if target == nil {
			continue
		}
		_, _, x, err := o.offerValue()
		if err != nil || x < 1 {
			continue
		}
		y, err := o.offerFreeY()
		if err != nil || y < 1 {
			continue
		}
		groups := int64(paidQty[target.TargetID]) / (x * 1000)
		if groups <= 0 {
			continue
		}
		free := money.Qty(groups * y * 1000)
		saved := money.MulQty(lowPrice[target.TargetID], free)
		if prev, ok := bxgyBest[target.TargetID]; !ok || free > prev.free {
			bxgyBest[target.TargetID] = bxgyCand{free, saved, o, x, y}
		}
	}
	for pid, cand := range bxgyBest {
		var pidLines []int
		for idx, ln := range lines {
			if ln.ProductID == pid {
				pidLines = append(pidLines, idx)
			}
		}
		var displaced money.Cents
		for _, idx := range pidLines {
			displaced += finalPromo[idx]
		}
		if cand.saved <= displaced {
			continue // line/bundle promo wins; ties go to it
		}
		// Drop any winning bundle touching this product; its lines
		// fall back to their best line promos.
		for oid, bw := range winningBundles {
			touches := false
			for _, idx := range pidLines {
				if bw.lineIdxs[idx] {
					touches = true
				}
			}
			if !touches {
				continue
			}
			delete(winningBundles, oid)
			for idx := range bw.lineIdxs {
				if v, ok := bestSnapshot[idx]; ok {
					finalPromo[idx] = v
				} else {
					delete(finalPromo, idx)
				}
			}
		}
		for _, idx := range pidLines {
			delete(finalPromo, idx)
		}
		res.FreeUnits[pid] = cand.free
		res.FreeCost += money.MulQty(buyPrice[pid], cand.free)
		res.Bxgy[pid] = BxgyInfo{OfferID: cand.offer.ID, Name: cand.offer.Name,
			FreeQty: money.Qty(cand.y * 1000), BuyX: money.Qty(cand.x * 1000)}
	}
	res.LinePromos = map[int]money.Cents{}
	for idx, v := range finalPromo {
		if v > 0 {
			res.LinePromos[idx] = v
			res.LinePromoTotal += v
		}
	}
	// Applied entries: bundles, then line promos grouped by offer, then bxgy.
	bundleLineIdxs := map[int]bool{}
	for _, bw := range winningBundles {
		for idx := range bw.lineIdxs {
			bundleLineIdxs[idx] = true
		}
		res.Applied = append(res.Applied, AppliedOffer{OfferID: bw.offer.ID, Name: bw.offer.Name, Saved: bw.saved})
	}
	// FIX-3 L6: per-line winning product_price promo price. Only when
	// the product_price offer is the final winner (not displaced by a
	// bundle — bxgy lines are already dropped from finalPromo) and its
	// saving is the full raw saving (uncapped); a capped saving keeps
	// the discount representation so the net can never change.
	res.LinePrice = map[int]money.Cents{}
	for idx, saved := range finalPromo {
		if bundleLineIdxs[idx] || saved <= 0 {
			continue
		}
		b, ok := bestLine[idx]
		if !ok || b.offer.Type != OfferProductPrice {
			continue
		}
		_, promoPrice, _, err := b.offer.offerValue()
		if err != nil || promoPrice < 0 {
			continue
		}
		unitSaved := lines[idx].UnitPrice - promoPrice
		if unitSaved < 0 {
			unitSaved = 0
		}
		if saved != money.MulQty(unitSaved, lines[idx].Qty) {
			continue // capped: keep the discount form
		}
		res.LinePrice[idx] = promoPrice
	}
	lineApplied := map[int64]*AppliedOffer{}
	for idx, saved := range finalPromo {
		if bundleLineIdxs[idx] || saved <= 0 {
			continue
		}
		b, ok := bestLine[idx]
		if !ok {
			continue
		}
		if a, ok := lineApplied[b.offer.ID]; ok {
			a.Saved += saved
		} else {
			lineApplied[b.offer.ID] = &AppliedOffer{OfferID: b.offer.ID, Name: b.offer.Name, Saved: saved}
		}
	}
	for _, a := range lineApplied {
		res.Applied = append(res.Applied, *a)
	}
	for pid, info := range res.Bxgy {
		res.Applied = append(res.Applied, AppliedOffer{OfferID: info.OfferID, Name: info.Name,
			Saved: money.MulQty(lowPrice[pid], res.FreeUnits[pid]), ProductID: pid,
			BuyQty: info.BuyX, FreeQty: info.FreeQty})
	}
	// Invoice-level offers: best single, once (R-P1/R-P3).
	var subtotal money.Cents
	for _, ln := range lines {
		subtotal += ln.LineTotal
	}
	subtotal -= res.LinePromoTotal
	var bestInv money.Cents
	var bestInvOffer *Offer
	for _, o := range offers {
		var saved money.Cents
		switch o.Type {
		case OfferInvoiceFixed:
			_, amount, _, err := o.offerValue()
			if err != nil {
				continue
			}
			saved = amount
		case OfferThresholdPct:
			thr, err := o.offerThreshold()
			if err != nil || subtotal < thr {
				continue
			}
			pct, _, _, err := o.offerValue()
			if err != nil {
				continue
			}
			saved = pctOf(subtotal, pct)
		default:
			continue
		}
		if saved > subtotal {
			saved = subtotal
		}
		if saved > bestInv {
			bestInv, bestInvOffer = saved, o
		}
	}
	res.InvoiceBest = bestInv
	res.InvoiceBestOffer = bestInvOffer
	var totalSaved money.Cents
	for _, a := range res.Applied {
		totalSaved += a.Saved
	}
	res.TotalSaved = totalSaved
	return res, nil
}

func offerTargetsProduct(o *Offer, productID int64) bool {
	for _, t := range o.Targets {
		if t.TargetType == "product" && t.TargetID == productID {
			return true
		}
	}
	return false
}

func offerTargetsCategory(o *Offer, category string) bool {
	for _, t := range o.Targets {
		if t.TargetType == "category" && strings.TrimSpace(t.TargetName) == strings.TrimSpace(category) {
			return true
		}
	}
	return false
}

// recordInvoiceOffersTx writes InvoiceOffers rows inside the posting tx.
// bxgy rows also carry ProductID / BuyQty (X per group) / FreeQty (Y per group).
func recordInvoiceOffersTx(tx *sql.Tx, invoiceID int64, applied []AppliedOffer, bxgy map[int64]BxgyInfo) error {
	for _, a := range applied {
		prodID := a.ProductID
		var buyQty, freeQty money.Qty
		if info, ok := bxgy[a.ProductID]; ok && a.ProductID > 0 {
			buyQty = info.BuyX
			freeQty = info.FreeQty
		}
		_, err := tx.Exec(`INSERT INTO InvoiceOffers(InvoiceID,OfferID,OfferName,SavedAmount,ProductID,BuyQty,FreeQty)
			VALUES(?,?,?,?,?,?,?)`, invoiceID, a.OfferID, a.Name, int64(a.Saved), nullableInt(prodID), int64(buyQty), int64(freeQty))
		if err != nil {
			return err
		}
	}
	return nil
}

// auditInvoiceOffersTx writes the single per-invoice promo audit row.
func auditInvoiceOffersTx(tx *sql.Tx, user *auth.User, invoiceNumber, invoiceID int64, applied []AppliedOffer) error {
	if len(applied) == 0 {
		return nil
	}
	parts := make([]string, 0, len(applied))
	for _, a := range applied {
		parts = append(parts, fmt.Sprintf("%s (وفّر %s)", a.Name, money.FormatEGP(a.Saved)))
	}
	return AuditTx(tx, user.ID, user.UserName, "تطبيق عرض ترويجي", "عروض", &invoiceNumber, invoiceID, 0, "",
		fmt.Sprintf("عروض مطبقة على الفاتورة %d: %s", invoiceNumber, strings.Join(parts, "، ")))
}
