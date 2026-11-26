// domain_sales.go — invoice creation / cancel / lists (FEATURE_MAP §2).
//
// One atomic transaction per invoice (S-22): open shift for the same
// user today is mandatory, duplicates merge, stock is checked twice
// (pre-check + fresh read inside the tx), expired products are blocked,
// promos auto-apply (R-P1..R-P9), credit limits are enforced live,
// and the invoice number comes from NextInvoiceNumber (global running
// integer, never reused).
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

// SaleLineInput is one cart line for CreateInvoice.
type SaleLineInput struct {
	ProductID    int64
	Qty          money.Qty
	UnitPrice    money.Cents // 0 = default price for the sale type (or PricePerKg when weighted)
	ItemDiscount money.Cents
	IsWeighted   bool
	ScaleBarcode string
	ScalePLU     string
}

// CreateInvoiceInput is the full sale request.
type CreateInvoiceInput struct {
	CustomerID      int64  // 0 = cash customer (ID 1)
	SaleType        string // retail | wholesale | bulk
	Lines           []SaleLineInput
	InvoiceDiscount money.Cents
	Paid            money.Cents
	PaymentMethod   string
	AllowOverLimit  bool // manager/admin approval flag; service-enforced
	AllowBelowCost  bool // explicit user confirmation to sell below BuyPrice (M9); service-enforced
	Notes           string
}

// InvoiceResult is the outcome of a successful sale.
type InvoiceResult struct {
	InvoiceID     int64
	InvoiceNumber int64
	Gross         money.Cents
	Discount      money.Cents // manual discounts only (item + invoice)
	PromoDiscount money.Cents // net-reducing promo (line + invoice)
	Net           money.Cents
	Paid          money.Cents
	Remaining     money.Cents
	Profit        money.Cents
	FreeCost      money.Cents
	Warnings      []string
	ShiftID       int64
	AppliedOffers []AppliedOffer
	PromoSaved    money.Cents // total customer saving incl. free units' retail value
	FreeUnits     map[int64]money.Qty
}

// InvoiceSummary is one row of the invoice list.
type InvoiceSummary struct {
	ID            int64
	Number        int64
	CustomerID    int64
	CustomerName  string
	SaleType      string
	Total         money.Cents
	Discount      money.Cents
	PromoDiscount money.Cents
	Net           money.Cents
	Paid          money.Cents
	Remaining     money.Cents
	PaymentMethod string
	UserName      string
	Date          string
	IsCanceled    bool
	HasReturns    bool
}

// InvoiceLine is one InvoiceDetails row.
type InvoiceLine struct {
	DetailID      int64
	ProductID     int64
	ProductName   string
	Qty           money.Qty
	UnitPrice     money.Cents
	BuyPrice      money.Cents
	ItemDiscount  money.Cents
	PromoDiscount money.Cents
	FreeQty       money.Qty
	TotalPrice    money.Cents
	IsWeighted    bool
	ScaleBarcode  string
	ScalePLU      string
}

// InvoiceDetail is a full invoice with lines, payments and offers.
type InvoiceDetail struct {
	Summary      InvoiceSummary
	Lines        []*InvoiceLine
	Payments     []*InvoicePayment
	Offers       []AppliedOffer
	Bxgy         map[int64]BxgyInfo
	Notes        string
	CancelReason string
	CanceledBy   string
	CanceledAt   string
}

// InvoicePayment is one Payments row.
type InvoicePayment struct {
	ID     int64
	Amount money.Cents
	Method string
	Date   string
	Notes  string
}

// NormalizePaymentMethod accepts Arabic aliases and English names and
// returns the stored value Cash | Visa | Vodafone Cash (S-17).
// Empty defaults to Cash.
func NormalizePaymentMethod(s string) (string, error) {
	v := strings.TrimSpace(s)
	switch v {
	case "", "Cash", "cash", "كاش", "نقدي", "نقداً", "نقدا":
		return "Cash", nil
	case "Visa", "visa", "فيزا", "card", "credit card", "debit card", "بطاقة":
		return "Visa", nil
	case "Vodafone Cash", "vodafone cash", "فودافون كاش", "فودافون", "VodafoneCash":
		return "Vodafone Cash", nil
	default:
		return "", errors.New("طريقة الدفع غير صحيحة")
	}
}

// requireKnownPaymentMethod is the shared alias other domain builders
// (B2/B3) call for payment-method normalization.
func requireKnownPaymentMethod(s string) (string, error) { return NormalizePaymentMethod(s) }

// PaymentMethodArabic renders a stored method in Arabic.
func PaymentMethodArabic(m string) string {
	switch m {
	case "Cash":
		return "كاش"
	case "Visa":
		return "فيزا"
	case "Vodafone Cash":
		return "فودافون كاش"
	default:
		return m
	}
}

// preparedLine is a merged, validated cart line.
type preparedLine struct {
	product       *Product
	productID     int64
	qty           money.Qty
	unitPrice     money.Cents
	itemDiscount  money.Cents
	isWeighted    bool
	scaleBarcode  string
	scalePLU      string
	lineGross     money.Cents
	lineTotal     money.Cents
	promoDiscount money.Cents
	freeQty       money.Qty
	// multiPrice is set when the line merged additions at different
	// unit prices (FIX-3 duplicate rule) — its unitPrice is then the
	// weighted average, while lineGross stays the exact sum.
	multiPrice bool
}

// avgUnitPrice is the weighted-average unit price of a merged line:
// round(gross * 1000 / qty). Gross and qty are always >= 0 here.
func avgUnitPrice(gross money.Cents, qty money.Qty) money.Cents {
	if qty <= 0 {
		return 0
	}
	return money.Cents((int64(gross)*1000 + int64(qty)/2) / int64(qty))
}

func (s *Store) prepareSaleLines(ctx context.Context, user *auth.User, in CreateInvoiceInput) ([]*preparedLine, []string, error) {
	if len(in.Lines) == 0 {
		return nil, nil, errors.New("لا يوجد أصناف في الفاتورة")
	}
	merged := map[int64]*preparedLine{}
	var order []int64
	for _, raw := range in.Lines {
		if raw.Qty <= 0 || raw.Qty > money.MaxQty {
			return nil, nil, errors.New("كمية غير صحيحة")
		}
		p, err := loadProductDB(s.DB, raw.ProductID)
		if err != nil {
			return nil, nil, errors.New("المنتج غير موجود")
		}
		if !p.IsActive {
			return nil, nil, errors.New("المنتج غير موجود")
		}
		isWeighted := raw.IsWeighted || p.IsWeighted
		unitPrice := raw.UnitPrice
		if unitPrice == 0 {
			if isWeighted {
				unitPrice = p.PricePerKg
			} else {
				unitPrice = PriceForSaleType(p, in.SaleType)
			}
		}
		if unitPrice <= 0 {
			return nil, nil, errors.New("سعر غير صحيح")
		}
		// FIX-1 M9 (S-14): selling below BuyPrice needs an explicit
		// user confirmation (allowBelowCost) — otherwise refuse with
		// the confirm prompt text. Weighted lines are priced by
		// PricePerKg, which is also checked here.
		if unitPrice < p.BuyPrice && !in.AllowBelowCost {
			return nil, nil, errors.New("السعر أقل من سعر الشراء — هل تريد البيع بخسارة؟")
		}
		// Any price that is not the sale-type price is a price edit
		// (S-14) — except a weighted line at exactly PricePerKg.
		if unitPrice != PriceForSaleType(p, in.SaleType) {
			if !(isWeighted && unitPrice == p.PricePerKg) {
				if err := rbac.Require(user.Role, rbac.PermEditPrice); err != nil {
					return nil, nil, err
				}
			}
		}
		if raw.ItemDiscount < 0 {
			return nil, nil, errors.New("خصم الصنف غير صحيح")
		}
		if raw.ItemDiscount > 0 {
			if err := rbac.Require(user.Role, rbac.PermGiveDiscount); err != nil {
				return nil, nil, err
			}
		}
		// FIX-3 (jony rule 2026-11-25): an invoice NEVER holds two
		// lines for the same product — re-adding a product merges into
		// the one line, EVEN at a different unit price. The merged
		// line keeps the EXACT gross: each addition contributes
		// MulQty(price, qty), the amount it would have contributed as
		// its own line, so the invoice total grows by the correct
		// amount. The displayed unit price becomes the weighted
		// average when prices differ.
		key := raw.ProductID
		addGross := money.MulQty(unitPrice, raw.Qty)
		if m, ok := merged[key]; ok {
			m.qty += raw.Qty
			m.lineGross += addGross
			m.itemDiscount += raw.ItemDiscount
			if unitPrice != m.unitPrice {
				m.multiPrice = true
			}
		} else {
			plu := raw.ScalePLU
			if plu == "" {
				plu = p.ScalePLU
			}
			merged[key] = &preparedLine{product: p, productID: raw.ProductID, qty: raw.Qty,
				unitPrice: unitPrice, lineGross: addGross, itemDiscount: raw.ItemDiscount, isWeighted: isWeighted,
				scaleBarcode: raw.ScaleBarcode, scalePLU: plu}
			order = append(order, key)
		}
	}
	var lines []*preparedLine
	for _, k := range order {
		ln := merged[k]
		if ln.multiPrice {
			ln.unitPrice = avgUnitPrice(ln.lineGross, ln.qty)
		}
		lines = append(lines, ln)
	}
	// Per-product totals, stock + expiry checks, totals.
	totalQty := map[int64]money.Qty{}
	for _, ln := range lines {
		totalQty[ln.productID] += ln.qty
	}
	var warnings []string
	var gross, totalItemDisc, profit money.Cents
	for _, ln := range lines {
		p := ln.product
		// lineGross is the exact accumulated gross from the merge loop
		// (FIX-3 duplicate rule) — it is NOT recomputed from the
		// (possibly averaged) unitPrice, so a mixed-price line keeps
		// the exact total of its additions.
		if ln.itemDiscount > ln.lineGross {
			ln.itemDiscount = ln.lineGross
		}
		ln.lineTotal = ln.lineGross - ln.itemDiscount
		if p.Quantity <= 0 {
			return nil, nil, errors.New("المنتج نفد من المخزون")
		}
		if totalQty[ln.productID] > p.Quantity {
			return nil, nil, fmt.Errorf("الكمية المطلوبة تتجاوز المخزون (%s)", money.FormatQty(p.Quantity))
		}
		if p.ExpiryDate != "" {
			exp := p.ExpiryDate
			if len(exp) > 10 {
				exp = exp[:10]
			}
			if exp < Today() {
				// Audit the blocked sale, then refuse (S-10).
				_ = s.Audit(user.ID, user.UserName, "منع بيع منتج منتهي", "مبيعات", nil, 0, 0, "",
					fmt.Sprintf("منع بيع منتج منتهي الصلاحية: %s — تاريخ الانتهاء: %s", p.Name, exp))
				return nil, nil, fmt.Errorf("لا يمكن بيع منتج منتهي الصلاحية %s — تاريخ الانتهاء: %s", p.Name, exp)
			}
			alertDays := p.ExpiryAlertDays
			if alertDays == 0 {
				alertDays = 30
			}
			// Expiry within the alert window -> warning, sale proceeds.
			if expDate, err := parseDate(exp); err == nil {
				today, _ := parseDate(Today())
				if !expDate.After(today.AddDate(0, 0, alertDays)) {
					warnings = append(warnings, fmt.Sprintf("تنبيه صلاحية: %s ينتهي في %s", p.Name, exp))
				}
			}
		}
		gross += ln.lineGross
		totalItemDisc += ln.itemDiscount
		profit += ln.lineTotal - money.MulQty(p.BuyPrice, ln.qty)
	}
	_ = gross
	_ = totalItemDisc
	_ = profit
	return lines, warnings, nil
}

func parseDate(s string) (time.Time, error) {
	return time.Parse("2006-01-02", s)
}

// AllocProRata splits total across lines in proportion to bases (exact
// cents; the rounding remainder goes to the last line with a positive
// base). Lines with a zero base get zero. A non-positive total or an
// all-zero base yields all zeros. Used for the invoice-level promo and
// the manual invoice discount, in creation and in the sale preview, so
// both compute the same per-line net (FIX-2 M-3).
func AllocProRata(total money.Cents, bases []money.Cents) []money.Cents {
	shares := make([]money.Cents, len(bases))
	if total <= 0 {
		return shares
	}
	var baseTotal money.Cents
	last := -1
	for i, b := range bases {
		if b > 0 {
			baseTotal += b
			last = i
		}
	}
	if baseTotal <= 0 || last < 0 {
		return shares
	}
	remainder := total
	for i, b := range bases {
		if b <= 0 {
			continue
		}
		if i == last {
			shares[i] = remainder
		} else {
			shares[i] = money.Cents(int64(total) * int64(b) / int64(baseTotal))
			remainder -= shares[i]
		}
	}
	return shares
}

// CreateInvoice posts a sale atomically (S-22).
func (s *Store) CreateInvoice(ctx context.Context, user *auth.User, in CreateInvoiceInput) (*InvoiceResult, error) {
	if err := rbac.Require(user.Role, rbac.PermCreateInvoice); err != nil {
		return nil, err
	}
	// The over-limit flag is itself the approval: passing it without
	// approve_credit_override is denied here, before any computation.
	if in.AllowOverLimit {
		if err := rbac.Require(user.Role, rbac.PermApproveCreditOverride); err != nil {
			return nil, err
		}
	}
	// FIX-3 H2: the below-cost flag is the same kind of approval — a
	// cashier passing allow_below_cost without approve_below_cost is
	// denied here, before any computation. The flag is never a
	// self-confirmation.
	if in.AllowBelowCost {
		if err := rbac.Require(user.Role, rbac.PermApproveBelowCost); err != nil {
			return nil, err
		}
	}
	switch in.SaleType {
	case "", "retail", "wholesale", "bulk":
		if in.SaleType == "" {
			in.SaleType = "retail"
		}
	default:
		return nil, errors.New("نوع البيع غير صحيح")
	}
	if in.InvoiceDiscount < 0 {
		return nil, errors.New("خصم الفاتورة غير صحيح")
	}
	if in.InvoiceDiscount > 0 {
		if err := rbac.Require(user.Role, rbac.PermGiveDiscount); err != nil {
			return nil, err
		}
	}
	method, err := NormalizePaymentMethod(in.PaymentMethod)
	if err != nil {
		return nil, err
	}
	customerID := in.CustomerID
	if customerID == 0 {
		customerID = 1
	}
	var custName string
	var creditLimit money.Cents
	var custActive int
	err = s.DB.QueryRow(`SELECT CustomerName,CreditLimit,IsActive FROM Customers WHERE CustomerID=?`,
		customerID).Scan(&custName, &creditLimit, &custActive)
	if err == sql.ErrNoRows {
		return nil, errors.New("العميل غير موجود")
	}
	if err != nil {
		return nil, err
	}
	if custActive != 1 {
		return nil, errors.New("العميل غير موجود")
	}

	// FIX-3 L16: the no-open-shift error comes before any product
	// lookup, matching the preview handler — a sale with no shift
	// reports the shift error, not "المنتج غير موجود". Same error
	// text as OpenShiftID; the posting transaction re-checks the
	// shift on fresh reads below.
	if open, err := s.OpenShiftForUser(ctx, user.ID); err != nil {
		return nil, err
	} else if open == nil {
		return nil, errors.New("لا يمكن تنفيذ هذه العملية بدون شيفت مفتوح لنفس المستخدم في تاريخ اليوم")
	}

	lines, warnings, err := s.prepareSaleLines(ctx, user, in)
	if err != nil {
		return nil, err
	}
	var gross, totalItemDisc, profit money.Cents
	totalQty := map[int64]money.Qty{}
	for _, ln := range lines {
		gross += ln.lineGross
		totalItemDisc += ln.itemDiscount
		profit += ln.lineTotal - money.MulQty(ln.product.BuyPrice, ln.qty)
		totalQty[ln.productID] += ln.qty
	}
	// Promos auto-apply after merge + manual discounts (R-P1..R-P6).
	var promoLines []PromoLine
	for _, ln := range lines {
		promoLines = append(promoLines, PromoLine{ProductID: ln.productID, Category: ln.product.Category,
			Qty: ln.qty, UnitPrice: ln.unitPrice, LineGross: ln.lineGross,
			ItemDiscount: ln.itemDiscount, LineTotal: ln.lineTotal, BuyPrice: ln.product.BuyPrice})
	}
	promo, err := s.EvaluateOffers(ctx, promoLines)
	if err != nil {
		return nil, err
	}
	for pid, free := range promo.FreeUnits {
		var stock money.Qty
		if err := s.DB.QueryRow(`SELECT Quantity FROM Products WHERE ProductID=?`, pid).Scan(&stock); err != nil {
			return nil, err
		}
		if totalQty[pid]+free > stock {
			return nil, fmt.Errorf("الكمية المطلوبة مع الوحدات المجانية تتجاوز المخزون (%s)", money.FormatQty(stock))
		}
	}
	linePromoTotal := promo.LinePromoTotal
	// FIX-3 L6: product_price repricing at insert — a line whose
	// winning offer is a product_price stores the promo price as its
	// unit price, and its LinePromos share is zeroed. Net-neutral by
	// construction: gross and the line-promo total both drop by the
	// same saving, so net, saved and InvoiceOffers are unchanged; the
	// invoice-level pro-rata bases are identical too
	// (lineTotal − saved == (lineTotal − saved) − 0), and profit is
	// unchanged (pre-promo profit drops by the saving, the subtracted
	// line-promo total drops by the same saving).
	for i, ln := range lines {
		pp, ok := promo.LinePrice[i]
		if !ok {
			continue
		}
		saved := promo.LinePromos[i]
		if saved <= 0 {
			continue
		}
		ln.unitPrice = pp
		ln.lineGross -= saved
		ln.lineTotal -= saved
		gross -= saved
		profit -= saved
		promo.LinePromos[i] = 0
		linePromoTotal -= saved
	}
	effInvDiscount := in.InvoiceDiscount
	if cap := gross - totalItemDisc - linePromoTotal; cap < 0 {
		cap = 0
	} else if effInvDiscount > cap {
		effInvDiscount = cap
	}
	// R-P3: invoice-level promo capped at the remainder after the
	// manual invoice discount.
	remainderForPromo := gross - totalItemDisc - linePromoTotal - effInvDiscount
	if remainderForPromo < 0 {
		remainderForPromo = 0
	}
	invoicePromo := promo.InvoiceBest
	if invoicePromo > remainderForPromo {
		invoicePromo = remainderForPromo
	}
	appliedOffers := append([]AppliedOffer{}, promo.Applied...)
	if promo.InvoiceBestOffer != nil && invoicePromo > 0 {
		appliedOffers = append(appliedOffers, AppliedOffer{
			OfferID: promo.InvoiceBestOffer.ID, Name: promo.InvoiceBestOffer.Name, Saved: invoicePromo})
	}
	promoSavedTotal := promo.TotalSaved + invoicePromo
	totalPromoDiscount := linePromoTotal + invoicePromo
	// Pro-rata the invoice-level promo and the manual invoice discount
	// across lines (remainder on the last line with a base), so stored
	// per-line promos sum exactly. Bases are the post-line-promo line
	// totals (floored at 0).
	bases := make([]money.Cents, len(lines))
	for i, ln := range lines {
		b := ln.lineTotal - promo.LinePromos[i]
		if b < 0 {
			b = 0
		}
		bases[i] = b
	}
	invoiceShares := AllocProRata(invoicePromo, bases)
	invDiscShares := AllocProRata(effInvDiscount, bases)
	invoiceShare := map[int]money.Cents{}
	for i, sh := range invoiceShares {
		invoiceShare[i] = sh
	}
	for i, ln := range lines {
		ln.promoDiscount = promo.LinePromos[i] + invoiceShare[i]
	}
	// FIX-2 M-3: net line totals after every reduction (the item
	// discount is already inside lineTotal; line promo + invoice promo
	// + the manual invoice discount allocated pro-rata). The
	// below-cost gate runs on these net totals, not on the nominal
	// unit price: a discount or an offer can push a line whose nominal
	// price is above BuyPrice below cost.
	netLineTotal := make([]money.Cents, len(lines))
	for i := range lines {
		n := bases[i] - invoiceShares[i] - invDiscShares[i]
		if n < 0 {
			n = 0
		}
		netLineTotal[i] = n
	}
	belowCostNet := false
	for i, ln := range lines {
		if netLineTotal[i] < money.MulQty(ln.product.BuyPrice, ln.qty) {
			belowCostNet = true
			break
		}
	}
	if belowCostNet && !in.AllowBelowCost {
		return nil, errors.New("السعر أقل من سعر الشراء — هل تريد البيع بخسارة؟")
	}
	net := gross - totalItemDisc - linePromoTotal - effInvDiscount - invoicePromo
	if net < 0 {
		net = 0
	}
	profit = profit - effInvDiscount - linePromoTotal - invoicePromo - promo.FreeCost

	paid := in.Paid
	if customerID == 1 {
		// The cash customer always pays the full net (S-19).
		paid = net
	}
	if paid < 0 || paid > net {
		return nil, errors.New("المبلغ المدفوع غير صحيح")
	}
	remaining := net - paid
	var overLimit *struct {
		limit, balance, amount money.Cents
	}
	if remaining > 0 {
		if customerID <= 1 {
			return nil, errors.New("يجب اختيار عميل مسجل لتسجيل المديونية")
		}
		// Live balance: sum of open remainings, never the stored column.
		var liveBal money.Cents
		if err := s.DB.QueryRow(`SELECT COALESCE(SUM(RemainingAmount),0) FROM Invoices
			WHERE CustomerID=? AND IsCanceled=0 AND RemainingAmount>0`, customerID).Scan(&liveBal); err != nil {
			return nil, err
		}
		if creditLimit > 0 && liveBal+remaining > creditLimit {
			if !in.AllowOverLimit {
				return nil, errors.New("تجاوز حد الائتمان يحتاج موافقة المدير")
			}
			overLimit = &struct {
				limit, balance, amount money.Cents
			}{creditLimit, liveBal, remaining}
		}
	}

	// Posting transaction (re-checks stock on fresh reads).
	var invoiceNumber, invoiceID, shiftID int64
	// FIX-2 M-3: whether any line sells below cost ON ITS NET total
	// (audited when allowed with AllowBelowCost).
	belowCost := belowCostNet
	// G-8 low-stock crossings, collected inside the posting tx and
	// notified after it commits (see below): a product counts only
	// when its stock crosses from above its minimum to at/below it.
	// A sale while already below minimum fires nothing — otherwise
	// every further sale would spam the inbox (NotifyLowStock dedupes
	// per product+quantity, so without this guard a lower quantity
	// would re-fire on every sale).
	type lowStockCrossing struct {
		productID int64
		name      string
		before    money.Qty
		after     money.Qty
		min       money.Qty
	}
	var crossings []lowStockCrossing
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		shift, err := OpenShiftID(tx, user.ID)
		if err != nil {
			return err
		}
		shiftID = shift
		// FIX-1 M6: the live credit balance is re-read INSIDE the
		// posting transaction (via tx) — the pre-transaction read
		// above is only an early hint; two interleaved sales must
		// not both pass on the same stale balance (TOCTOU).
		overLimit = nil // the in-transaction read is authoritative
		if remaining > 0 && customerID > 1 && creditLimit > 0 {
			var liveInTx money.Cents
			if err := tx.QueryRow(`SELECT COALESCE(SUM(RemainingAmount),0) FROM Invoices
				WHERE CustomerID=? AND IsCanceled=0 AND RemainingAmount>0`, customerID).Scan(&liveInTx); err != nil {
				return err
			}
			if liveInTx+remaining > creditLimit {
				if !in.AllowOverLimit {
					return errors.New("تجاوز حد الائتمان يحتاج موافقة المدير")
				}
				overLimit = &struct {
					limit, balance, amount money.Cents
				}{creditLimit, liveInTx, remaining}
			}
		}
		// Fresh stock re-check (two interleaved postings must not oversell).
		runningStock := map[int64]money.Qty{}
		// G-8: pre-sale quantity + minimum per product, for the
		// crossing check at the end of this tx.
		stockBefore := map[int64]lowStockCrossing{}
		noteStockBefore := func(p *Product) {
			if _, ok := stockBefore[p.ID]; !ok {
				stockBefore[p.ID] = lowStockCrossing{productID: p.ID, name: p.Name, before: p.Quantity, min: p.MinQuantity}
			}
		}
		for _, ln := range lines {
			if _, ok := runningStock[ln.productID]; !ok {
				fresh, err := loadProductTx(tx, ln.productID)
				if err != nil {
					return err
				}
				if !fresh.IsActive {
					return errors.New("المنتج غير موجود")
				}
				noteStockBefore(fresh)
				runningStock[ln.productID] = fresh.Quantity
			}
			if runningStock[ln.productID] <= 0 {
				return errors.New("المنتج نفد من المخزون")
			}
			if ln.qty > runningStock[ln.productID] {
				return fmt.Errorf("الكمية المطلوبة تتجاوز المخزون (%s)", money.FormatQty(runningStock[ln.productID]))
			}
			runningStock[ln.productID] -= ln.qty
		}
		for pid, free := range promo.FreeUnits {
			if free <= 0 {
				continue
			}
			if _, ok := runningStock[pid]; !ok {
				fresh, err := loadProductTx(tx, pid)
				if err != nil {
					return err
				}
				noteStockBefore(fresh)
				runningStock[pid] = fresh.Quantity
			}
			if free > runningStock[pid] {
				return fmt.Errorf("الكمية المطلوبة مع الوحدات المجانية تتجاوز المخزون (%s)", money.FormatQty(runningStock[pid]))
			}
			runningStock[pid] -= free
		}
		num, err := NextInvoiceNumber(tx)
		if err != nil {
			return err
		}
		invoiceNumber = num
		now := Now()
		res, err := tx.Exec(`INSERT INTO Invoices(InvoiceNumber,CustomerID,SaleType,TotalAmount,
			Discount,PromoDiscount,FreeCost,NetAmount,PaidAmount,RemainingAmount,PaymentMethod,
			Notes,UserID,UserName,ShiftID,InvoiceDate)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			num, customerID, in.SaleType, int64(gross), int64(totalItemDisc+effInvDiscount),
			int64(totalPromoDiscount), int64(promo.FreeCost), int64(net), int64(paid),
			int64(remaining), method, in.Notes, user.ID, user.UserName, shiftID, now)
		if err != nil {
			return err
		}
		invoiceID, err = res.LastInsertId()
		if err != nil {
			return err
		}
		freeSeen := map[int64]bool{}
		for _, ln := range lines {
			freeQty := money.Qty(0)
			if !freeSeen[ln.productID] {
				freeQty = promo.FreeUnits[ln.productID]
				freeSeen[ln.productID] = true
			}
			ln.freeQty = freeQty
			_, err := tx.Exec(`INSERT INTO InvoiceDetails(InvoiceID,ProductID,ProductName,Quantity,
				UnitPrice,BuyPrice,ItemDiscount,PromoDiscount,FreeQty,TotalPrice,IsWeighted,
				ScaleBarcode,ScalePLU,ScaleBarcodeType)
				VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				invoiceID, ln.productID, ln.product.Name, int64(ln.qty), int64(ln.unitPrice),
				int64(ln.product.BuyPrice), int64(ln.itemDiscount), int64(ln.promoDiscount),
				int64(freeQty), int64(ln.lineTotal), boolInt(ln.isWeighted),
				ln.scaleBarcode, ln.scalePLU, ln.product.ScaleBarcodeType)
			if err != nil {
				return err
			}
			newStock := runningStock[ln.productID]
			if _, err := tx.Exec(`UPDATE Products SET Quantity=? WHERE ProductID=?`,
				int64(newStock), ln.productID); err != nil {
				return err
			}
			if err := insertMovementTx(tx, ln.productID, "sale", -ln.qty, ln.product.BuyPrice,
				fmt.Sprintf("بيع - %d", num), user.ID); err != nil {
				return err
			}
		}
		// Free units: their own stock movement (same 'sale' type).
		for pid, free := range promo.FreeUnits {
			if free <= 0 {
				continue
			}
			p, err := loadProductTx(tx, pid)
			if err != nil {
				return err
			}
			offerName := ""
			if info, ok := promo.Bxgy[pid]; ok {
				offerName = info.Name
			}
			if err := insertMovementTx(tx, pid, "sale", -free, p.BuyPrice,
				fmt.Sprintf("وحدات مجانية — عرض %s - %d", offerName, num), user.ID); err != nil {
				return err
			}
		}
		if err := recordInvoiceOffersTx(tx, invoiceID, appliedOffers, promo.Bxgy); err != nil {
			return err
		}
		if paid > 0 {
			if _, err := tx.Exec(`INSERT INTO Payments(InvoiceID,CustomerID,Amount,PaymentDate,
				PaymentMethod,Notes,UserID) VALUES(?,?,?,?,?,'دفعة عند البيع',?)`,
				invoiceID, customerID, int64(paid), now, method, user.ID); err != nil {
				return err
			}
			if method == "Cash" {
				if _, err := tx.Exec(`INSERT INTO CashBox(UserID,UserName,ShiftID,MovementType,
					Amount,Reason,MovementDate,AutoKind,RefInvoiceID) VALUES(?,?,?,'in',?,?,?,'sale',?)`,
					user.ID, user.UserName, shiftID, int64(paid),
					fmt.Sprintf("مبيعات - %d - كاش", num), now, invoiceID); err != nil {
					return err
				}
			}
		}
		if customerID > 1 {
			if err := SyncCustomerBalance(tx, customerID); err != nil {
				return err
			}
		}
		notes := fmt.Sprintf("إنشاء فاتورة %d — الصافي %s، المدفوع %s، المتبقي %s",
			num, money.FormatEGP(net), money.FormatEGP(paid), money.FormatEGP(remaining))
		if len(appliedOffers) > 0 {
			parts := make([]string, 0, len(appliedOffers))
			for _, a := range appliedOffers {
				parts = append(parts, fmt.Sprintf("%s (وفّر %s)", a.Name, money.FormatEGP(a.Saved)))
			}
			notes += " — عروض مطبقة: " + strings.Join(parts, "، ")
		}
		if err := AuditTx(tx, user.ID, user.UserName, "إنشاء فاتورة", "مبيعات",
			&invoiceNumber, invoiceID, customerID, custName, notes); err != nil {
			return err
		}
		if err := auditInvoiceOffersTx(tx, user, invoiceNumber, invoiceID, appliedOffers); err != nil {
			return err
		}
		if overLimit != nil {
			if err := AuditTx(tx, user.ID, user.UserName, "تجاوز حد الائتمان", "مبيعات",
				&invoiceNumber, invoiceID, customerID, custName,
				fmt.Sprintf("تجاوز حد الائتمان بموافقة المدير — حد الائتمان %s، الرصيد الحالي %s، المبلغ الجديد %s",
					money.FormatEGP(overLimit.limit), money.FormatEGP(overLimit.balance), money.FormatEGP(overLimit.amount))); err != nil {
				return err
			}
		}
		if belowCost {
			if err := AuditTx(tx, user.ID, user.UserName, "بيع بخسارة", "مبيعات",
				&invoiceNumber, invoiceID, customerID, custName,
				"بيع بسعر أقل من سعر الشراء بتأكيد صريح من المستخدم"); err != nil {
				return err
			}
		}
		// G-8: record minimum crossings (above -> at/below) for the
		// post-commit low-stock notifications. runningStock holds the
		// final quantities after the sale + free units.
		for pid, after := range runningStock {
			b, ok := stockBefore[pid]
			if !ok {
				continue
			}
			if b.before > b.min && after <= b.min {
				crossings = append(crossings, lowStockCrossing{productID: pid, name: b.name, before: b.before, after: after, min: b.min})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// G-8: low-stock alerts for this sale's crossings only, to
	// admin/manager (NotifyLowStock's own targeting). Best effort: a
	// notification failure must never fail a completed sale.
	for _, c := range crossings {
		_, _ = s.NotifyLowStock(ctx, user, c.productID, c.name, int64(c.after), int64(c.min))
	}
	return &InvoiceResult{
		InvoiceID: invoiceID, InvoiceNumber: invoiceNumber,
		Gross: gross, Discount: totalItemDisc + effInvDiscount,
		PromoDiscount: totalPromoDiscount, Net: net, Paid: paid,
		Remaining: remaining, Profit: profit, FreeCost: promo.FreeCost,
		Warnings: warnings, ShiftID: shiftID,
		AppliedOffers: appliedOffers, PromoSaved: promoSavedTotal,
		FreeUnits: promo.FreeUnits,
	}, nil
}

// CancelCashRefundPreview returns the net cash a cancel would refund:
// min(cash collected net of returns, total collected net of all returns),
// floored at 0. Read-only.
//
// PR-H1: the refund is counted exactly once, from Payments alone.
// ProcessReturn posts every paid-out refund as a negative Payments row
// (domain_returns.go:353 paidOut = TotalRefund − debtReduction; :460-466
// Amount = −paidOut, in the return's payment method), and the
// debt-absorbed part of a return never touches Payments at all. Summing
// Payments therefore already nets every return — subtracting
// Returns.TotalRefund a second time under-refunded the customer (sell
// 8000, return 2000 cash, cancel previewed 4000 instead of 6000), and
// over-subtracted the debt-absorbed part even when nothing was paid out
// (credit sale, return absorbed by debt, cancel previewed 0 instead of
// the cash actually collected). The min() still matters when the return
// method differs from the sale method: a non-cash return leaves cashNet
// above totalNet (cap at totalNet — the customer already got part back
// off-drawer), and a cash return on a non-cash sale drives cashNet below
// totalNet (cap at the cash actually held).
func (s *Store) CancelCashRefundPreview(ctx context.Context, invoiceID int64) (money.Cents, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT Amount,PaymentMethod FROM Payments WHERE InvoiceID=?`, invoiceID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var cashNet, totalNet money.Cents
	for rows.Next() {
		var amt money.Cents
		var method string
		if err := rows.Scan(&amt, &method); err != nil {
			return 0, err
		}
		totalNet += amt
		if m, _ := NormalizePaymentMethod(method); m == "Cash" {
			cashNet += amt
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	refund := cashNet
	if totalNet < refund {
		refund = totalNet
	}
	if refund < 0 {
		refund = 0
	}
	return refund, nil
}

// CancelInvoice soft-cancels an invoice (Admin/Manager only, reason
// required): stock is restored (sold minus already returned, plus the
// free units still earned on the kept qty), the debt is reversed, and
// the net cash collected is refunded as one CashBox 'out' movement.
// Returns the cash refund posted (0 when nothing was collected in cash).
func (s *Store) CancelInvoice(ctx context.Context, user *auth.User, invoiceID int64, reason string) (money.Cents, error) {
	if err := rbac.Require(user.Role, rbac.PermCancelInvoice); err != nil {
		return 0, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return 0, errors.New("يجب إدخال سبب الإلغاء")
	}
	var inv InvoiceSummary
	var custName string
	err := s.DB.QueryRow(`SELECT i.InvoiceID,i.InvoiceNumber,i.CustomerID,COALESCE(c.CustomerName,''),
		i.SaleType,i.TotalAmount,i.Discount,i.PromoDiscount,i.NetAmount,i.PaidAmount,
		i.RemainingAmount,i.PaymentMethod,i.UserName,i.InvoiceDate,i.IsCanceled
		FROM Invoices i LEFT JOIN Customers c ON c.CustomerID=i.CustomerID WHERE i.InvoiceID=?`,
		invoiceID).Scan(&inv.ID, &inv.Number, &inv.CustomerID, &custName, &inv.SaleType,
		&inv.Total, &inv.Discount, &inv.PromoDiscount, &inv.Net, &inv.Paid,
		&inv.Remaining, &inv.PaymentMethod, &inv.UserName, &inv.Date, &inv.IsCanceled)
	if err == sql.ErrNoRows {
		return 0, errors.New("الفاتورة غير موجودة")
	}
	if err != nil {
		return 0, err
	}
	if inv.IsCanceled {
		return 0, errors.New("هذه الفاتورة ملغاة مسبقاً")
	}
	cashRefund, err := s.CancelCashRefundPreview(ctx, invoiceID)
	if err != nil {
		return 0, err
	}
	var paidCollected money.Cents
	_ = s.DB.QueryRow(`SELECT COALESCE(SUM(Amount),0) FROM Payments WHERE InvoiceID=?`, invoiceID).Scan(&paidCollected)
	// Sold / already-returned per product.
	detailRows, err := s.DB.Query(`SELECT ProductID,ProductName,Quantity,BuyPrice FROM InvoiceDetails WHERE InvoiceID=? ORDER BY DetailID`, invoiceID)
	if err != nil {
		return 0, err
	}
	type det struct {
		pid  int64
		name string
		qty  money.Qty
		buy  money.Cents
	}
	var dets []det
	soldPerProduct := map[int64]money.Qty{}
	for detailRows.Next() {
		var d det
		if err := detailRows.Scan(&d.pid, &d.name, &d.qty, &d.buy); err != nil {
			detailRows.Close()
			return 0, err
		}
		dets = append(dets, d)
		soldPerProduct[d.pid] += d.qty
	}
	detailRows.Close()
	returnedPerProduct := map[int64]money.Qty{}
	retRows, err := s.DB.Query(`SELECT ProductID,COALESCE(SUM(Quantity),0) FROM Returns WHERE InvoiceID=? GROUP BY ProductID`, invoiceID)
	if err != nil {
		return 0, err
	}
	for retRows.Next() {
		var pid int64
		var q money.Qty
		if err := retRows.Scan(&pid, &q); err != nil {
			retRows.Close()
			return 0, err
		}
		returnedPerProduct[pid] = q
	}
	retRows.Close()
	// Free units still earned on the kept paid qty (R-P9).
	bxgy, err := s.GetInvoiceBxgy(ctx, invoiceID)
	if err != nil {
		return 0, err
	}
	freeRestore := map[int64]money.Qty{}
	for pid, info := range bxgy {
		kept := soldPerProduct[pid] - returnedPerProduct[pid]
		if kept < 0 {
			kept = 0
		}
		if earned := EarnedFreeUnits(kept, info.BuyX, info.FreeQty); earned > 0 {
			freeRestore[pid] = earned
		}
	}
	restorePerProduct := map[int64]money.Qty{}
	for pid, sold := range soldPerProduct {
		restore := sold - returnedPerProduct[pid]
		if restore < 0 {
			restore = 0
		}
		restorePerProduct[pid] = restore + freeRestore[pid]
	}
	// Refund shift: the invoice's own shift when still open, else the
	// caller's open shift today; refused when no shift is open at all.
	var refundShiftID int64
	var invShiftID int64
	_ = s.DB.QueryRow(`SELECT ShiftID FROM Invoices WHERE InvoiceID=?`, invoiceID).Scan(&invShiftID)
	if cashRefund > 0 {
		var id int64
		err := s.DB.QueryRow(`SELECT ShiftID FROM Shifts WHERE ShiftID=? AND IsOpen=1`, invShiftID).Scan(&id)
		if err == sql.ErrNoRows {
			err = s.DB.QueryRow(`SELECT ShiftID FROM Shifts WHERE UserID=? AND ShiftDate=? AND IsOpen=1 ORDER BY ShiftID DESC LIMIT 1`,
				user.ID, Today()).Scan(&id)
		}
		if err != nil {
			return 0, errors.New("لا يمكن رد المبلغ النقدي بدون شيفت مفتوح — افتح شيفت أولًا")
		}
		refundShiftID = id
	}
	now := Now()
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE Invoices SET IsCanceled=1,CanceledBy=?,CanceledAt=?,CancelReason=? WHERE InvoiceID=?`,
			user.UserName, now, reason, invoiceID); err != nil {
			return err
		}
		restoredDone := map[int64]bool{}
		for _, d := range dets {
			restore := money.Qty(0)
			if !restoredDone[d.pid] {
				restore = restorePerProduct[d.pid]
				restoredDone[d.pid] = true
			}
			p, err := loadProductTx(tx, d.pid)
			if err == nil {
				if _, err := tx.Exec(`UPDATE Products SET Quantity=? WHERE ProductID=?`,
					int64(p.Quantity+restore), d.pid); err != nil {
					return err
				}
			}
			// FIX-1 M5: one stock movement per product for the whole
			// restore (sold minus returned, free units included) — the
			// old code wrote a second 'cancel_return' movement for the
			// free units, doubling the movement ledger even though
			// Products.Quantity was only restored once.
			if err := insertMovementTx(tx, d.pid, "cancel_return", restore, d.buy,
				fmt.Sprintf("إلغاء فاتورة %d", inv.Number), user.ID); err != nil {
				return err
			}
		}
		if inv.Remaining > 0 && inv.CustomerID > 1 {
			// IsCanceled=1 is already set, so the live sum excludes it.
			if err := SyncCustomerBalance(tx, inv.CustomerID); err != nil {
				return err
			}
		}
		if cashRefund > 0 {
			// FIX-1 H5: a cancel refund is a cash-out — refuse it when
			// the drawer cannot cover it (negative forbidden), exactly
			// like any other discretionary withdrawal.
			if err := RequireDrawerBalance(tx, refundShiftID, cashRefund, "استرداد إلغاء فاتورة"); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO CashBox(UserID,UserName,ShiftID,MovementType,Amount,Reason,MovementDate,AutoKind,RefInvoiceID)
				VALUES(?,?,?,'out',?,?,?,'cancel_refund',?)`, user.ID, user.UserName, refundShiftID,
				int64(cashRefund), fmt.Sprintf("استرداد إلغاء فاتورة %d - كاش", inv.Number), now, invoiceID); err != nil {
				return err
			}
		}
		details := fmt.Sprintf("إلغاء فاتورة %d — السبب: %s | الأصناف: %d", inv.Number, reason, len(dets))
		if len(freeRestore) > 0 {
			details += " — وحدات مجانية أُعيدت للمخزون"
		}
		if cashRefund > 0 {
			details += fmt.Sprintf(" — استرداد نقدي: %s (حركة صندوق 'استرداد إلغاء فاتورة')", money.FormatEGP(cashRefund))
		}
		if paidCollected > cashRefund {
			details += fmt.Sprintf(" — مبلغ محصّل غير نقدي قبل الإلغاء: %s — لا حركة صندوق له (لم يدخل الدرج)",
				money.FormatEGP(paidCollected-cashRefund))
		}
		return AuditTx(tx, user.ID, user.UserName, "إلغاء فاتورة", "إلغاء",
			&inv.Number, invoiceID, inv.CustomerID, custName, details)
	})
	if err != nil {
		return 0, err
	}
	return cashRefund, nil
}

// ListInvoices lists invoices in a date range (YYYY-MM-DD), newest first.
// userID 0 = all users. All roles may view (view-only for warehouse).
// FIX-1 M7: login is now enforced in the service layer (nil user refused);
// no narrower permission applies — invoice viewing is open to every
// logged-in role by design.
func (s *Store) ListInvoices(ctx context.Context, user *auth.User, fromDate, toDate string, userID int64) ([]*InvoiceSummary, error) {
	if err := requireUser(user, ""); err != nil {
		return nil, err
	}
	if fromDate != "" && toDate != "" && fromDate > toDate {
		return nil, errors.New("تاريخ البداية يجب أن يكون قبل تاريخ النهاية")
	}
	if fromDate == "" {
		fromDate = Today()
	}
	if toDate == "" {
		toDate = Today()
	}
	q := `SELECT i.InvoiceID,i.InvoiceNumber,i.CustomerID,COALESCE(c.CustomerName,''),
		i.SaleType,i.TotalAmount,i.Discount,i.PromoDiscount,i.NetAmount,i.PaidAmount,
		i.RemainingAmount,i.PaymentMethod,i.UserName,i.InvoiceDate,i.IsCanceled,
		EXISTS(SELECT 1 FROM Returns r WHERE r.InvoiceID=i.InvoiceID)
		FROM Invoices i LEFT JOIN Customers c ON c.CustomerID=i.CustomerID
		WHERE substr(i.InvoiceDate,1,10) BETWEEN ? AND ?`
	args := []any{fromDate, toDate}
	if userID > 0 {
		q += ` AND i.UserID=?`
		args = append(args, userID)
	}
	q += ` ORDER BY i.InvoiceID DESC`
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*InvoiceSummary
	for rows.Next() {
		var inv InvoiceSummary
		var hasRet int
		if err := rows.Scan(&inv.ID, &inv.Number, &inv.CustomerID, &inv.CustomerName,
			&inv.SaleType, &inv.Total, &inv.Discount, &inv.PromoDiscount, &inv.Net,
			&inv.Paid, &inv.Remaining, &inv.PaymentMethod, &inv.UserName, &inv.Date,
			&inv.IsCanceled, &hasRet); err != nil {
			return nil, err
		}
		inv.HasReturns = hasRet == 1
		out = append(out, &inv)
	}
	return out, rows.Err()
}

// ErrInvoiceForbidden is returned by GetInvoice when a cashier or
// warehouse user tries to read another user's invoice (FIX-3 M4).
// Handlers map it to HTTP 403.
var ErrInvoiceForbidden = errors.New("لا يمكنك عرض فاتورة مستخدم آخر")

// GetInvoice returns a full invoice (header + lines + payments + offers).
// FIX-3 M4: cashier/warehouse read only their own invoices;
// admin/manager/supervisor may read any invoice.
func (s *Store) GetInvoice(ctx context.Context, user *auth.User, id int64) (*InvoiceDetail, error) {
	if user == nil {
		return nil, errors.New("يلزم تسجيل الدخول")
	}
	var d InvoiceDetail
	var custName sql.NullString
	var notes, cancelReason, canceledBy, canceledAt sql.NullString
	var shiftID sql.NullInt64
	var ownerID int64
	err := s.DB.QueryRowContext(ctx, `SELECT i.InvoiceID,i.InvoiceNumber,i.CustomerID,c.CustomerName,
		i.SaleType,i.TotalAmount,i.Discount,i.PromoDiscount,i.NetAmount,i.PaidAmount,
		i.RemainingAmount,i.PaymentMethod,i.UserName,i.InvoiceDate,i.IsCanceled,i.Notes,
		i.CancelReason,i.CanceledBy,i.CanceledAt,i.ShiftID,i.UserID,
		EXISTS(SELECT 1 FROM Returns r WHERE r.InvoiceID=i.InvoiceID)
		FROM Invoices i LEFT JOIN Customers c ON c.CustomerID=i.CustomerID WHERE i.InvoiceID=?`, id).
		Scan(&d.Summary.ID, &d.Summary.Number, &d.Summary.CustomerID, &custName,
			&d.Summary.SaleType, &d.Summary.Total, &d.Summary.Discount, &d.Summary.PromoDiscount,
			&d.Summary.Net, &d.Summary.Paid, &d.Summary.Remaining, &d.Summary.PaymentMethod,
			&d.Summary.UserName, &d.Summary.Date, &d.Summary.IsCanceled, &notes,
			&cancelReason, &canceledBy, &canceledAt, &shiftID, &ownerID, &d.Summary.HasReturns)
	if err == sql.ErrNoRows {
		return nil, errors.New("الفاتورة غير موجودة")
	}
	if err != nil {
		return nil, err
	}
	if rbac.EffectiveRole(user.Role) != rbac.RoleAdmin && user.Role != rbac.RoleSupervisor && ownerID != user.ID {
		return nil, ErrInvoiceForbidden
	}
	d.Summary.CustomerName = custName.String
	d.Notes = notes.String
	d.CancelReason = cancelReason.String
	d.CanceledBy = canceledBy.String
	d.CanceledAt = canceledAt.String
	rows, err := s.DB.QueryContext(ctx, `SELECT DetailID,ProductID,ProductName,Quantity,UnitPrice,
		BuyPrice,ItemDiscount,PromoDiscount,FreeQty,TotalPrice,IsWeighted,ScaleBarcode,ScalePLU
		FROM InvoiceDetails WHERE InvoiceID=? ORDER BY DetailID`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var ln InvoiceLine
		var isW int
		var sb, splu sql.NullString
		if err := rows.Scan(&ln.DetailID, &ln.ProductID, &ln.ProductName, &ln.Qty,
			&ln.UnitPrice, &ln.BuyPrice, &ln.ItemDiscount, &ln.PromoDiscount, &ln.FreeQty,
			&ln.TotalPrice, &isW, &sb, &splu); err != nil {
			rows.Close()
			return nil, err
		}
		ln.IsWeighted = isW == 1
		ln.ScaleBarcode = sb.String
		ln.ScalePLU = splu.String
		d.Lines = append(d.Lines, &ln)
	}
	rows.Close()
	pRows, err := s.DB.QueryContext(ctx, `SELECT PaymentID,Amount,PaymentMethod,PaymentDate,Notes
		FROM Payments WHERE InvoiceID=? ORDER BY PaymentID`, id)
	if err != nil {
		return nil, err
	}
	for pRows.Next() {
		var pay InvoicePayment
		var nts sql.NullString
		if err := pRows.Scan(&pay.ID, &pay.Amount, &pay.Method, &pay.Date, &nts); err != nil {
			pRows.Close()
			return nil, err
		}
		pay.Notes = nts.String
		d.Payments = append(d.Payments, &pay)
	}
	pRows.Close()
	offers, err := s.GetInvoiceOffers(ctx, id)
	if err != nil {
		return nil, err
	}
	d.Offers = offers
	bxgy, err := s.GetInvoiceBxgy(ctx, id)
	if err != nil {
		return nil, err
	}
	d.Bxgy = bxgy
	return &d, nil
}

// InvoiceProfit computes the post-promo profit of an invoice
// (Admin/Supervisor/Manager only, view_profit).
func (s *Store) InvoiceProfit(ctx context.Context, user *auth.User, id int64) (money.Cents, error) {
	if err := rbac.Require(user.Role, rbac.PermViewProfit); err != nil {
		return 0, err
	}
	d, err := s.GetInvoice(ctx, user, id)
	if err != nil {
		return 0, err
	}
	if d.Summary.IsCanceled {
		return 0, nil
	}
	var profit money.Cents
	for _, ln := range d.Lines {
		profit += ln.TotalPrice - ln.PromoDiscount - money.MulQty(ln.BuyPrice, ln.Qty)
	}
	// Free units' buy cost rides on the invoice's FreeCost.
	var freeCost money.Cents
	_ = s.DB.QueryRowContext(ctx, `SELECT FreeCost FROM Invoices WHERE InvoiceID=?`, id).Scan(&freeCost)
	profit -= freeCost
	// Manual invoice discount = Discount - sum(item discounts).
	var itemDisc money.Cents
	for _, ln := range d.Lines {
		itemDisc += ln.ItemDiscount
	}
	manualInvDisc := d.Summary.Discount - itemDisc
	if manualInvDisc < 0 {
		manualInvDisc = 0
	}
	profit -= manualInvDisc
	return profit, nil
}
