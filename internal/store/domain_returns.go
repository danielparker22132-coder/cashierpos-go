package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/money"
	"cashierpos-go/internal/rbac"
)

// ---------------------------------------------------------------- returns (R-01..R-08)

// ReturnLineInput is one line of a return request.
type ReturnLineInput struct {
	ProductID int64
	Qty       money.Qty // milliunits, > 0
}

// VoidedFreeLine is one bxgy free-units line voided by a return.
type VoidedFreeLine struct {
	ProductID   int64
	ProductName string
	OfferName   string
	Qty         money.Qty // milliunits
	VoidedCost  money.Cents
}

// ReturnResult is the outcome of ProcessReturn.
type ReturnResult struct {
	TotalRefund   money.Cents
	DebtReduction money.Cents
	CashRefund    money.Cents
	NonCashRefund money.Cents
	ReturnIDs     []int64
	InvoiceID     int64
	InvoiceNumber int64
	VoidedFree    []VoidedFreeLine
}

// alreadyReturnedQtyTx sums quantities already returned for one
// invoice+product.
func alreadyReturnedQtyTx(tx *sql.Tx, invoiceID, productID int64) (money.Qty, error) {
	var q sql.NullInt64
	err := tx.QueryRow(`SELECT COALESCE(SUM(Quantity),0) FROM Returns WHERE InvoiceID=? AND ProductID=?`,
		invoiceID, productID).Scan(&q)
	return money.Qty(q.Int64), err
}

type returnDetail struct {
	DetailID      int64
	ProductID     int64
	ProductName   string
	Quantity      money.Qty
	UnitPrice     money.Cents
	BuyPrice      money.Cents
	TotalPrice    money.Cents
	PromoDiscount money.Cents
	// effective total for the whole line after promo + invoice discount share
	effTotal money.Cents
}

type preparedReturn struct {
	ProductID   int64
	ProductName string
	Qty         money.Qty
	UnitPrice   money.Cents // effective unit price (refund/qty)
	BuyPrice    money.Cents
	Refund      money.Cents
	VoidedQty   money.Qty
	VoidedCost  money.Cents
}

// mergeReturnLines merges duplicate lines for the same product (FIX-1 M3:
// two rows for one product in a single return must not each pass the
// sold-minus-returned check against the DB state before either is posted).
func mergeReturnLines(lines []ReturnLineInput) []ReturnLineInput {
	merged := map[int64]money.Qty{}
	var order []int64
	for _, ln := range lines {
		if _, ok := merged[ln.ProductID]; !ok {
			order = append(order, ln.ProductID)
		}
		merged[ln.ProductID] += ln.Qty
	}
	out := make([]ReturnLineInput, 0, len(order))
	for _, pid := range order {
		out = append(out, ReturnLineInput{ProductID: pid, Qty: merged[pid]})
	}
	return out
}

// ProcessReturn posts a partial or full return (R-03/R-05), atomically:
// Returns rows + stock restock + StockMovements 'return' + debt
// reduction first (min(refund, remaining)) + the leftover paid out
// (CashBox 'out' when the refund method is Cash and a shift is open).
// FIX-1 H6: the cash leg IS gated by the drawer balance (coordinator
// ruling — this deliberately departs from PyQt, which exempts automatic
// returns): a cash refund above the expected drawer balance is refused,
// and a cash refund with no open shift is refused outright (no
// unrecorded cash leaves the drawer). A refund fully absorbed by debt
// (no cash leg) needs no shift and is allowed.
func (s *Store) ProcessReturn(ctx context.Context, user *auth.User, invoiceID int64, lines []ReturnLineInput, reason, paymentMethod string) (*ReturnResult, error) {
	if user == nil {
		return nil, errors.New("يلزم تسجيل الدخول")
	}
	if err := rbac.Require(user.Role, rbac.PermDoReturn); err != nil {
		return nil, err
	}
	if strings.TrimSpace(reason) == "" {
		return nil, errors.New("يجب كتابة سبب المرتجع")
	}
	lines = mergeReturnLines(lines)
	if len(lines) == 0 {
		return nil, errors.New("لا توجد كمية متاحة للمرتجع")
	}
	method, err := NormalizePaymentMethod(paymentMethod)
	if err != nil {
		return nil, err
	}
	var result *ReturnResult
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		// Invoice: must exist and not be canceled (R-01/R-02).
		var invNumber, customerID, remaining, netAmount int64
		var isCanceled int
		err := tx.QueryRow(`SELECT InvoiceNumber,CustomerID,RemainingAmount,NetAmount,IsCanceled
			FROM Invoices WHERE InvoiceID=?`, invoiceID).
			Scan(&invNumber, &customerID, &remaining, &netAmount, &isCanceled)
		if err == sql.ErrNoRows {
			return errors.New("الفاتورة غير موجودة")
		}
		if err != nil {
			return err
		}
		if isCanceled != 0 {
			return errors.New("هذه الفاتورة ملغاة، ولا يمكن تنفيذ مرتجع عليها")
		}
		// Invoice discount + lines total (for pro-rata allocation).
		var invDiscount int64
		_ = tx.QueryRow(`SELECT COALESCE(Discount,0)+COALESCE(PromoDiscount,0) FROM Invoices WHERE InvoiceID=?`,
			invoiceID).Scan(&invDiscount)
		detailRows, err := tx.Query(`SELECT DetailID,ProductID,ProductName,Quantity,UnitPrice,BuyPrice,TotalPrice,PromoDiscount
			FROM InvoiceDetails WHERE InvoiceID=? ORDER BY DetailID`, invoiceID)
		if err != nil {
			return err
		}
		var details []*returnDetail
		linesTotal := money.Cents(0)
		for detailRows.Next() {
			var d returnDetail
			var qty, unit, buy, total, promo int64
			if err := detailRows.Scan(&d.DetailID, &d.ProductID, &d.ProductName, &qty, &unit, &buy, &total, &promo); err != nil {
				detailRows.Close()
				return err
			}
			d.Quantity = money.Qty(qty)
			d.UnitPrice = money.Cents(unit)
			d.BuyPrice = money.Cents(buy)
			d.TotalPrice = money.Cents(total)
			d.PromoDiscount = money.Cents(promo)
			linesTotal += d.TotalPrice
			details = append(details, &d)
		}
		detailRows.Close()
		if len(details) == 0 {
			return errors.New("الفاتورة غير موجودة")
		}
		for _, d := range details {
			alloc := money.Cents(0)
			if linesTotal > 0 && invDiscount > 0 {
				alloc = money.Cents(int64(d.TotalPrice) * invDiscount / int64(linesTotal))
			}
			d.effTotal = d.TotalPrice - d.PromoDiscount - alloc
			if d.effTotal < 0 {
				d.effTotal = 0
			}
		}
		// Open shift of the caller today (may be absent).
		var shiftID int64
		var shiftOpen bool
		if sid, err := OpenShiftID(tx, user.ID); err == nil {
			shiftID, shiftOpen = sid, true
		}
		// Already-refunded total (refund cap).
		var alreadyRefunded int64
		_ = tx.QueryRow(`SELECT COALESCE(SUM(TotalRefund),0) FROM Returns WHERE InvoiceID=?`, invoiceID).Scan(&alreadyRefunded)
		refundCap := money.Cents(netAmount) - money.Cents(alreadyRefunded)
		if refundCap < 0 {
			refundCap = 0
		}
		// Prepare each requested line (cheapest effective unit first).
		var prepared []*preparedReturn
		totalRefund := money.Cents(0)
		returnedByProduct := map[int64]money.Qty{}
		for _, line := range lines {
			if line.Qty <= 0 {
				return errors.New("كمية غير صحيحة")
			}
			var sold money.Qty
			var prodName string
			var firstBuy money.Cents
			var prodDetails []*returnDetail
			for _, d := range details {
				if d.ProductID == line.ProductID {
					prodDetails = append(prodDetails, d)
					sold += d.Quantity
					if prodName == "" {
						prodName = d.ProductName
						firstBuy = d.BuyPrice
					}
				}
			}
			if len(prodDetails) == 0 {
				return errors.New("الصنف غير موجود في الفاتورة")
			}
			already, err := alreadyReturnedQtyTx(tx, invoiceID, line.ProductID)
			if err != nil {
				return err
			}
			available := sold - already
			if line.Qty > available {
				return fmt.Errorf("لا يمكن إرجاع كمية أكبر من %s", money.FormatQty(available))
			}
			// Cheapest-first walk over the product's lines.
			sorted := append([]*returnDetail(nil), prodDetails...)
			sort.Slice(sorted, func(i, j int) bool {
				ei, ej := money.Cents(0), money.Cents(0)
				if sorted[i].Quantity > 0 {
					ei = money.Cents(int64(sorted[i].effTotal) * 1000 / int64(sorted[i].Quantity))
				}
				if sorted[j].Quantity > 0 {
					ej = money.Cents(int64(sorted[j].effTotal) * 1000 / int64(sorted[j].Quantity))
				}
				return ei < ej
			})
			refund := money.Cents(0)
			consumed := already
			remainingQty := line.Qty
			for _, d := range sorted {
				eaten := consumed
				if eaten > d.Quantity {
					eaten = d.Quantity
				}
				consumed -= eaten
				rowAvail := d.Quantity - eaten
				if rowAvail <= 0 || remainingQty <= 0 {
					continue
				}
				take := remainingQty
				if take > rowAvail {
					take = rowAvail
				}
				// refund += effTotal * take / rowQty
				refund += money.Cents(int64(d.effTotal) * int64(take) / int64(d.Quantity))
				remainingQty -= take
				if remainingQty <= 0 {
					break
				}
			}
			unitPrice := money.Cents(0)
			if line.Qty > 0 {
				unitPrice = money.Cents(int64(refund) * 1000 / int64(line.Qty))
			}
			prepared = append(prepared, &preparedReturn{
				ProductID: line.ProductID, ProductName: prodName, Qty: line.Qty,
				UnitPrice: unitPrice, BuyPrice: firstBuy, Refund: refund,
			})
			totalRefund += refund
			returnedByProduct[line.ProductID] += line.Qty
		}
		// Cap the refund at the goods value still refundable.
		if totalRefund > refundCap {
			excess := totalRefund - refundCap
			for i := len(prepared) - 1; i >= 0 && excess > 0; i-- {
				cut := prepared[i].Refund
				if cut > excess {
					cut = excess
				}
				prepared[i].Refund -= cut
				if prepared[i].Qty > 0 {
					prepared[i].UnitPrice = money.Cents(int64(prepared[i].Refund) * 1000 / int64(prepared[i].Qty))
				}
				excess -= cut
			}
			totalRefund = refundCap
		}
		if totalRefund <= 0 {
			return errors.New("لا توجد كمية متاحة للمرتجع")
		}
		// bxgy: recompute earned free units per product; the voided
		// free qty goes back to stock and is named in the result.
		var voidedLines []VoidedFreeLine
		for pid, retQty := range returnedByProduct {
			var buyQty, freeQty sql.NullInt64
			var offerName string
			err := tx.QueryRow(`SELECT BuyQty,FreeQty,OfferName FROM InvoiceOffers
				WHERE InvoiceID=? AND ProductID=? ORDER BY OfferID LIMIT 1`,
				invoiceID, pid).Scan(&buyQty, &freeQty, &offerName)
			if err != nil {
				continue // no bxgy offer on this product
			}
			if !buyQty.Valid || !freeQty.Valid || buyQty.Int64 <= 0 || freeQty.Int64 <= 0 {
				continue
			}
			var sold money.Qty
			for _, d := range details {
				if d.ProductID == pid {
					sold += d.Quantity
				}
			}
			already, err := alreadyReturnedQtyTx(tx, invoiceID, pid)
			if err != nil {
				return err
			}
			keptBefore := sold - already
			keptAfter := keptBefore - retQty
			voided := EarnedFreeUnits(keptBefore, money.Qty(buyQty.Int64), money.Qty(freeQty.Int64)) -
				EarnedFreeUnits(keptAfter, money.Qty(buyQty.Int64), money.Qty(freeQty.Int64))
			if voided <= 0 {
				continue
			}
			var prodName string
			var buy money.Cents
			for _, d := range details {
				if d.ProductID == pid {
					prodName = d.ProductName
					buy = d.BuyPrice
					break
				}
			}
			voidedCost := money.MulQty(buy, voided)
			voidedLines = append(voidedLines, VoidedFreeLine{
				ProductID: pid, ProductName: prodName, OfferName: offerName,
				Qty: voided, VoidedCost: voidedCost,
			})
		}
		voidedByProduct := map[int64]VoidedFreeLine{}
		for _, v := range voidedLines {
			voidedByProduct[v.ProductID] = v
		}
		// Debt first, then cash out.
		debtReduction := money.Cents(0)
		if remaining > 0 && customerID > 1 {
			debtReduction = totalRefund
			if debtReduction > money.Cents(remaining) {
				debtReduction = money.Cents(remaining)
			}
		}
		paidOut := totalRefund - debtReduction
		cashRefund := money.Cents(0)
		nonCashRefund := money.Cents(0)
		if paidOut > 0 {
			if method == "Cash" {
				cashRefund = paidOut
			} else {
				nonCashRefund = paidOut
			}
		}
		// FIX-1 H6: gate the cash leg BEFORE posting anything (the
		// transaction rolls back on error, so no partial return lands).
		// A cash refund with no open shift is refused — cash must never
		// leave unrecorded. A refund fully absorbed by debt (cashRefund
		// == 0) needs no shift and proceeds.
		if cashRefund > 0 {
			if !shiftOpen {
				return errors.New("لا يمكن رد مبلغ نقدي بدون شيفت مفتوح — افتح شيفت أولًا")
			}
			if err := RequireDrawerBalance(tx, shiftID, cashRefund, "مرتجع"); err != nil {
				return err
			}
		}
		now := Now()
		// Posting (atomic).
		result = &ReturnResult{
			TotalRefund: totalRefund, DebtReduction: debtReduction,
			CashRefund: cashRefund, NonCashRefund: nonCashRefund,
			InvoiceID: invoiceID, InvoiceNumber: invNumber,
			VoidedFree: voidedLines,
		}
		attachedVoided := map[int64]bool{}
		for _, p := range prepared {
			vq, vc := money.Qty(0), money.Cents(0)
			if v, ok := voidedByProduct[p.ProductID]; ok && !attachedVoided[p.ProductID] {
				vq, vc = v.Qty, v.VoidedCost
				attachedVoided[p.ProductID] = true
				p.VoidedQty, p.VoidedCost = vq, vc
			}
			shiftArg := sql.NullInt64{Valid: shiftOpen}
			if shiftOpen {
				shiftArg.Int64 = shiftID
			}
			res, err := tx.Exec(`INSERT INTO Returns(InvoiceID,ProductID,ProductName,Quantity,UnitPrice,
				BuyPrice,TotalRefund,VoidedFreeQty,VoidedFreeCost,Reason,UserID,UserName,
				ReturnDate,ReturnPaymentMethod,ShiftID,IsProcessed)
				VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1)`,
				invoiceID, p.ProductID, p.ProductName, int64(p.Qty), int64(p.UnitPrice),
				int64(p.BuyPrice), int64(p.Refund), int64(vq), int64(vc),
				strings.TrimSpace(reason), user.ID, user.UserName, now, method, shiftArg)
			if err != nil {
				return err
			}
			rid, err := res.LastInsertId()
			if err != nil {
				return err
			}
			result.ReturnIDs = append(result.ReturnIDs, rid)
			// Restock + movement.
			var stockQty int64
			if err := tx.QueryRow(`SELECT Quantity FROM Products WHERE ProductID=?`, p.ProductID).Scan(&stockQty); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE Products SET Quantity=? WHERE ProductID=?`,
				stockQty+int64(p.Qty), p.ProductID); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO StockMovements(ProductID,MovementType,Quantity,BuyPrice,Notes,UserID,MovementDate)
				VALUES(?, 'return', ?, ?, ?, ?, ?)`,
				p.ProductID, int64(p.Qty), int64(p.BuyPrice),
				fmt.Sprintf("مرتجع فاتورة %d", invNumber), user.ID, now); err != nil {
				return err
			}
		}
		// PR10-L1: stamp the event id (the first Returns row's ID)
		// on every row of the event. One cash leg covers the whole
		// event and is stamped with the first row's ID, so the leg
		// names the event — but without this, later rows have no
		// leg naming them: the leg's own-row read could only see
		// row [0], and row [1]'s reverse lookup found no leg at
		// all, so a multi-line event still misdated in both
		// directions in a midnight shift when either was mangled.
		// All rows of one event share ReturnDate/ShiftID/InvoiceID
		// (same `now` above), so the first row's ID identifies the
		// event exactly; rows written before fix-10 keep
		// ReturnEventID 0 and keep the legacy fallback.
		if len(result.ReturnIDs) > 0 {
			eventID := result.ReturnIDs[0]
			args := make([]any, 0, len(result.ReturnIDs)+1)
			args = append(args, eventID)
			ph := make([]string, 0, len(result.ReturnIDs))
			for _, rid := range result.ReturnIDs {
				ph = append(ph, "?")
				args = append(args, rid)
			}
			if _, err := tx.Exec(`UPDATE Returns SET ReturnEventID=? WHERE ReturnID IN (`+strings.Join(ph, ",")+`)`, args...); err != nil {
				return err
			}
		}
		// Voided free units go back to stock with their own movement.
		for _, v := range voidedLines {
			var stockQty int64
			if err := tx.QueryRow(`SELECT Quantity FROM Products WHERE ProductID=?`, v.ProductID).Scan(&stockQty); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE Products SET Quantity=? WHERE ProductID=?`,
				stockQty+int64(v.Qty), v.ProductID); err != nil {
				return err
			}
			var buy int64
			_ = tx.QueryRow(`SELECT BuyPrice FROM Products WHERE ProductID=?`, v.ProductID).Scan(&buy)
			if _, err := tx.Exec(`INSERT INTO StockMovements(ProductID,MovementType,Quantity,BuyPrice,Notes,UserID,MovementDate)
				VALUES(?, 'return', ?, ?, ?, ?, ?)`,
				v.ProductID, int64(v.Qty), buy,
				fmt.Sprintf("وحدات مجانية مستردة — عرض %s - %d", v.OfferName, invNumber), user.ID, now); err != nil {
				return err
			}
		}
		// Debt reduction.
		if debtReduction > 0 {
			newRemaining := money.Cents(remaining) - debtReduction
			if newRemaining <= 0 {
				newRemaining = 0
			}
			if _, err := tx.Exec(`UPDATE Invoices SET RemainingAmount=? WHERE InvoiceID=?`,
				int64(newRemaining), invoiceID); err != nil {
				return err
			}
			if err := SyncCustomerBalance(tx, customerID); err != nil {
				return err
			}
		}
		// Paid-out leg: negative Payments row (the refund ledger).
		if paidOut > 0 {
			if _, err := tx.Exec(`INSERT INTO Payments(InvoiceID,CustomerID,Amount,PaymentDate,PaymentMethod,Notes,UserID)
				VALUES(?,?,?,?,?,'مرتجع',?)`,
				invoiceID, customerID, int64(-paidOut), now, method, user.ID); err != nil {
				return err
			}
		}
		// Cash leg: CashBox 'out'. Reached only when a shift is open
		// and the drawer gate above passed (FIX-1 H6).
		// PR9-L1/PR10-L1: the leg is stamped with the event's first
		// Returns row (ReturnID). One leg covers the whole return
		// event and every row of the event shares ReturnDate, ShiftID
		// and InvoiceID (same `now` above), so the first row's ID
		// identifies the event — and every row now carries it as
		// ReturnEventID, so returnLegDay can read the event's day
		// through any of its rows and returnsRowDay can read its own
		// leg's date back. A leg with no Returns rows (cannot happen
		// when cashRefund > 0) keeps ReturnID 0 and keeps the legacy
		// fallback.
		if cashRefund > 0 {
			firstReturnID := int64(0)
			if len(result.ReturnIDs) > 0 {
				firstReturnID = result.ReturnIDs[0]
			}
			if _, err := tx.Exec(`INSERT INTO CashBox(UserID,UserName,ShiftID,MovementType,Amount,Reason,MovementDate,AutoKind,RefInvoiceID,ReturnID)
				VALUES(?,?,?, 'out', ?, ?, ?,'return',?,?)`,
				user.ID, user.UserName, shiftID, int64(cashRefund),
				fmt.Sprintf("مرتجع فاتورة %d - %s", invNumber, PaymentMethodArabic(method)), now, invoiceID, firstReturnID); err != nil {
				return err
			}
		}
		// Drawer-negative alert: an automatic return never silently
		// drives the drawer below zero.
		if shiftOpen && cashRefund > 0 {
			sum, err := shiftSummaryTx(tx, shiftID)
			if err != nil {
				return err
			}
			if sum.ExpectedBalance < 0 {
				msg := fmt.Sprintf("تنبيه: رصيد الدرج أصبح سالباً (%s) بعد مرتجع فاتورة %d — المرتجع أعاد نقداً أكثر مما في الدرج",
					money.FormatEGP(sum.ExpectedBalance), invNumber)
				if _, err := tx.Exec(`INSERT INTO AuditAlerts(UserName,AlertType,AlertMessage,AlertValue,AlertDate)
					VALUES(?, 'تحذير', ?, ?, ?)`,
					user.UserName, msg, int64(sum.ExpectedBalance), now); err != nil {
					return err
				}
			}
		}
		// Audit.
		detail := fmt.Sprintf("مرتجع فاتورة %d: قيمة المرتجع %s", invNumber, money.FormatEGP(totalRefund))
		if len(voidedLines) > 0 {
			parts := make([]string, 0, len(voidedLines))
			for _, v := range voidedLines {
				parts = append(parts, fmt.Sprintf("%s ×%s (عرض %s)", v.ProductName, money.FormatQty(v.Qty), v.OfferName))
			}
			detail += " — وحدات مجانية مستردة (تُسلَّم للكاشير): " + strings.Join(parts, "، ")
		}
		invNum := invNumber
		return AuditTx(tx, user.ID, user.UserName, "مرتجع", "مبيعات", &invNum, invoiceID, customerID, "", detail)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ProcessReturnAll returns every line with available qty (R-04).
func (s *Store) ProcessReturnAll(ctx context.Context, user *auth.User, invoiceID int64, reason, paymentMethod string) (*ReturnResult, error) {
	var lines []ReturnLineInput
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT ProductID, Quantity FROM InvoiceDetails WHERE InvoiceID=?`, invoiceID)
		if err != nil {
			return err
		}
		defer rows.Close()
		seen := map[int64]money.Qty{}
		var order []int64
		for rows.Next() {
			var pid int64
			var qty int64
			if err := rows.Scan(&pid, &qty); err != nil {
				return err
			}
			if _, ok := seen[pid]; !ok {
				order = append(order, pid)
			}
			seen[pid] += money.Qty(qty)
		}
		for _, pid := range order {
			already, err := alreadyReturnedQtyTx(tx, invoiceID, pid)
			if err != nil {
				return err
			}
			if avail := seen[pid] - already; avail > 0 {
				lines = append(lines, ReturnLineInput{ProductID: pid, Qty: avail})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, errors.New("لا توجد كمية متاحة للمرتجع")
	}
	return s.ProcessReturn(ctx, user, invoiceID, lines, reason, paymentMethod)
}

// ---------------------------------------------------------------- return analytics (R-07)

// ReturnByProduct is one row of the by-product breakdown.
type ReturnByProduct struct {
	ProductID   int64
	ProductName string
	Count       int
	Qty         money.Qty
	TotalValue  money.Cents
	LostProfit  money.Cents
}

// ReturnByUser is one row of the by-user breakdown.
type ReturnByUser struct {
	UserName   string
	Count      int
	TotalValue money.Cents
	LostProfit money.Cents
}

// ReturnsAnalytics is the returns analytics report (Admin/Manager).
type ReturnsAnalytics struct {
	From       string
	To         string
	Count      int
	TotalValue money.Cents
	LostProfit money.Cents
	ByProduct  []*ReturnByProduct
	ByUser     []*ReturnByUser
}

func lostProfitOf(refund money.Cents, qty money.Qty, buy money.Cents) money.Cents {
	return refund - money.MulQty(buy, qty)
}

// GetReturnsAnalytics builds the returns analytics (counts, value,
// lost profit, by product and by user). Permission:
// view_return_analysis (Admin/Manager).
func (s *Store) GetReturnsAnalytics(ctx context.Context, user *auth.User, from, to string) (*ReturnsAnalytics, error) {
	if user == nil {
		return nil, errors.New("يلزم تسجيل الدخول")
	}
	if err := rbac.Require(user.Role, rbac.PermViewReturnAnalysis); err != nil {
		return nil, err
	}
	if strings.TrimSpace(from) == "" {
		from = Today()
	}
	if strings.TrimSpace(to) == "" {
		to = Today()
	}
	rep := &ReturnsAnalytics{From: from, To: to}
	rows, err := s.DB.Query(`SELECT ProductID,ProductName,Quantity,UnitPrice,BuyPrice,TotalRefund,UserName
		FROM Returns WHERE substr(ReturnDate,1,10) BETWEEN ? AND ?`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byProd := map[int64]*ReturnByProduct{}
	var prodOrder []int64
	byUser := map[string]*ReturnByUser{}
	var userOrder []string
	for rows.Next() {
		var pid int64
		var name, userName string
		var qty, unit, buy, refund int64
		if err := rows.Scan(&pid, &name, &qty, &unit, &buy, &refund, &userName); err != nil {
			return nil, err
		}
		lost := lostProfitOf(money.Cents(refund), money.Qty(qty), money.Cents(buy))
		rep.Count++
		rep.TotalValue += money.Cents(refund)
		rep.LostProfit += lost
		p, ok := byProd[pid]
		if !ok {
			p = &ReturnByProduct{ProductID: pid, ProductName: name}
			byProd[pid] = p
			prodOrder = append(prodOrder, pid)
		}
		p.Count++
		p.Qty += money.Qty(qty)
		p.TotalValue += money.Cents(refund)
		p.LostProfit += lost
		u, ok := byUser[userName]
		if !ok {
			u = &ReturnByUser{UserName: userName}
			byUser[userName] = u
			userOrder = append(userOrder, userName)
		}
		u.Count++
		u.TotalValue += money.Cents(refund)
		u.LostProfit += lost
	}
	for _, pid := range prodOrder {
		rep.ByProduct = append(rep.ByProduct, byProd[pid])
	}
	for _, name := range userOrder {
		rep.ByUser = append(rep.ByUser, byUser[name])
	}
	return rep, rows.Err()
}

// ReturnAnalysisCSV renders the returns analytics as CSV (Arabic
// headers). The name matches what B4's dashboard calls (reports.go).
func (s *Store) ReturnAnalysisCSV(ctx context.Context, user *auth.User, from, to string) (string, error) {
	rep, err := s.GetReturnsAnalytics(ctx, user, from, to)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("المنتج,عدد المرتجعات,الكمية,قيمة المرتجعات (قرش),الربح المفقود (قرش)\n")
	for _, p := range rep.ByProduct {
		fmt.Fprintf(&b, "%s,%d,%d,%d,%d\n", strings.ReplaceAll(p.ProductName, ",", "،"),
			p.Count, int64(p.Qty), int64(p.TotalValue), int64(p.LostProfit))
	}
	b.WriteString("الكاشير,عدد المرتجعات,قيمة المرتجعات (قرش),الربح المفقود (قرش)\n")
	for _, u := range rep.ByUser {
		fmt.Fprintf(&b, "%s,%d,%d,%d\n", strings.ReplaceAll(u.UserName, ",", "،"),
			u.Count, int64(u.TotalValue), int64(u.LostProfit))
	}
	fmt.Fprintf(&b, "الإجمالي,%d,,%d,%d\n", rep.Count, int64(rep.TotalValue), int64(rep.LostProfit))
	return b.String(), nil
}

// ReturnRow is one row of the returns list.
type ReturnRow struct {
	ReturnID            int64
	InvoiceID           int64
	InvoiceNumber       int64
	ProductID           int64
	ProductName         string
	Qty                 money.Qty
	UnitPrice           money.Cents
	TotalRefund         money.Cents
	VoidedFreeQty       money.Qty
	VoidedFreeCost      money.Cents
	Reason              string
	UserName            string
	ReturnDate          string
	ReturnPaymentMethod string
}

// ListReturns lists returns in a date range (newest first).
// FIX-2 M-4: reading the list requires do_return, the same permission
// the /returns page and /api/returns demand — the service layer and the
// HTTP layer no longer disagree about who may read returns.
func (s *Store) ListReturns(ctx context.Context, user *auth.User, from, to string) ([]*ReturnRow, error) {
	if err := requireUser(user, rbac.PermDoReturn); err != nil {
		return nil, err
	}
	if strings.TrimSpace(from) == "" {
		from = Today()
	}
	if strings.TrimSpace(to) == "" {
		to = Today()
	}
	rows, err := s.DB.Query(`SELECT r.ReturnID, r.InvoiceID, COALESCE(i.InvoiceNumber,0),
		r.ProductID, r.ProductName, r.Quantity, r.UnitPrice, r.TotalRefund,
		r.VoidedFreeQty, r.VoidedFreeCost, r.Reason, r.UserName, r.ReturnDate, r.ReturnPaymentMethod
		FROM Returns r LEFT JOIN Invoices i ON i.InvoiceID=r.InvoiceID
		WHERE substr(r.ReturnDate,1,10) BETWEEN ? AND ? ORDER BY r.ReturnDate DESC, r.ReturnID DESC`,
		from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ReturnRow
	for rows.Next() {
		var r ReturnRow
		var qty, unit, refund, vq, vc int64
		if err := rows.Scan(&r.ReturnID, &r.InvoiceID, &r.InvoiceNumber, &r.ProductID,
			&r.ProductName, &qty, &unit, &refund, &vq, &vc, &r.Reason, &r.UserName,
			&r.ReturnDate, &r.ReturnPaymentMethod); err != nil {
			return nil, err
		}
		r.Qty = money.Qty(qty)
		r.UnitPrice = money.Cents(unit)
		r.TotalRefund = money.Cents(refund)
		r.VoidedFreeQty = money.Qty(vq)
		r.VoidedFreeCost = money.Cents(vc)
		out = append(out, &r)
	}
	return out, rows.Err()
}
