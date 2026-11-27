// Package store — B4: reports, profit/return analysis, manager dashboard,
// shift monitor, CSV export.
package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/money"
	"cashierpos-go/internal/rbac"
)

// SalesReport is the Q-01 sales report for a date range.
type SalesReport struct {
	From           string
	To             string
	InvoiceCount   int64
	TotalSales     money.Cents // SUM(NetAmount), non-canceled
	TotalGross     money.Cents // FIX-1 L6: SUM(TotalAmount) — the gross before discounts (TotalSales == TotalNet was a duplicate)
	TotalDiscount  money.Cents
	TotalNet       money.Cents
	TotalPaid      money.Cents
	TotalRemaining money.Cents
	TotalProfit    money.Cents // unified profit (FIX-1 M1), same as InvoiceProfit
	ByDay          []DayRow
	ByPayment      []PaymentRow
}

// DayRow is one day of sales.
type DayRow struct {
	Day          string
	InvoiceCount int64
	Total        money.Cents
}

// PaymentRow is one payment method total.
type PaymentRow struct {
	Method string
	Count  int64
	Total  money.Cents
}

// ProfitReport is the Q-01 profit report + Q-02 analysis base.
type ProfitReport struct {
	From        string
	To          string
	TotalSales  money.Cents
	TotalCost   money.Cents
	TotalProfit money.Cents
	BySaleType  []SaleTypeRow
	Top10       []ProductProfitRow
	Bottom10    []ProductProfitRow
}

// SaleTypeRow is profit grouped by sale type.
type SaleTypeRow struct {
	SaleType     string
	InvoiceCount int64
	TotalSales   money.Cents
	TotalProfit  money.Cents
}

// ProductProfitRow is profit for one product.
type ProductProfitRow struct {
	ProductID   int64
	ProductName string
	QtySold     money.Qty
	TotalSales  money.Cents
	TotalProfit money.Cents
}

// DebtRow is one customer debt line.
type DebtRow struct {
	CustomerID   int64
	CustomerName string
	Phone        string
	Balance      money.Cents // live remaining
	InvoiceCount int64
}

// LowStockRow is one low-stock product.
type LowStockRow struct {
	ProductID   int64
	ProductName string
	Barcode     string
	Quantity    money.Qty
	MinQuantity money.Qty
}

// CanceledInvoiceRow is one canceled invoice.
type CanceledInvoiceRow struct {
	InvoiceNumber int64
	InvoiceDate   string
	UserName      string
	NetAmount     money.Cents
	CanceledBy    string
	CancelReason  string
}

// PaymentReport is the Q-03 payment-method + drawer report.
type PaymentReport struct {
	From     string
	To       string
	ByMethod []PaymentRow
	CashIn   money.Cents
	CashOut  money.Cents
	Expenses money.Cents
	Returns  money.Cents
}

// CustomerReportRow is one invoice line of a customer report (U-12 / Q-04).
type CustomerReport struct {
	CustomerID   int64
	CustomerName string
	Phone        string
	Balance      money.Cents // live remaining
	CreditLimit  money.Cents
	From         string
	To           string
	InvoiceCount int64
	TotalSales   money.Cents
	TotalPaid    money.Cents
	Invoices     []CustomerInvoiceRow
	Payments     []CustomerPaymentRow
}

// CustomerInvoiceRow is one invoice in a customer report.
type CustomerInvoiceRow struct {
	InvoiceID     int64
	InvoiceNumber int64
	InvoiceDate   string
	NetAmount     money.Cents
	PaidAmount    money.Cents
	Remaining     money.Cents
	Canceled      bool
}

// CustomerPaymentRow is one payment in a customer report.
type CustomerPaymentRow struct {
	PaymentDate string
	Amount      money.Cents
	Method      string
	ReceivedBy  string
}

// --- Unified profit (FIX-1 M1) ---
//
// InvoiceProfit is the canonical profit definition: per line
// TotalPrice − PromoDiscount − rounded qty×BuyPrice, minus the
// invoice's FreeCost and its manual invoice discount
// (Invoices.Discount − Σ item discounts, floored at 0). The reports
// used to compute only TotalPrice − qty×BuyPrice (truncated), ignoring
// promos, the manual invoice discount and free-unit cost. Every report
// below now loads the same lines through loadProfitLines and sums the
// same allocated per-line profit, so they agree with InvoiceProfit.

// profitQuerier is satisfied by *sql.DB and *sql.Tx.
type profitQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// profitLine is one invoice line with its allocated profit: line
// profit minus its pro-rata share of the invoice's FreeCost + manual
// invoice discount (same pro-rata rule as CreateInvoice's invoiceShare).
type profitLine struct {
	InvoiceID   int64
	SaleType    string
	ProductID   int64
	ProductName string
	Qty         money.Qty
	Total       money.Cents // TotalPrice (after item discount, before promo)
	Promo       money.Cents
	Buy         money.Cents
	FreeCost    money.Cents // invoice FreeCost (once per invoice)
	Profit      money.Cents // allocated profit of this line
}

// loadProfitLines loads non-canceled invoice lines in a date range
// (saleType "" = all) with allocated per-line profit.
func loadProfitLines(q profitQuerier, from, to, saleType string) ([]profitLine, error) {
	args := []any{from, to}
	filter := ""
	if saleType != "" {
		filter = " AND i.SaleType=?"
		args = append(args, saleType)
	}
	rows, err := q.Query(`SELECT i.InvoiceID,i.SaleType,d.ProductID,COALESCE(d.ProductName,''),
		d.Quantity,d.TotalPrice,COALESCE(d.PromoDiscount,0),d.BuyPrice,
		COALESCE(i.Discount,0),COALESCE(i.FreeCost,0),
		(SELECT COALESCE(SUM(ItemDiscount),0) FROM InvoiceDetails WHERE InvoiceID=i.InvoiceID)
		FROM InvoiceDetails d JOIN Invoices i ON i.InvoiceID=d.InvoiceID
		WHERE i.IsCanceled=0 AND substr(i.InvoiceDate,1,10) BETWEEN ? AND ?`+filter+`
		ORDER BY i.InvoiceID, d.DetailID`, args...)
	if err != nil {
		return nil, sumScanErr(err)
	}
	defer rows.Close()
	type raw struct {
		pl         profitLine
		discount   money.Cents
		itemDisc   money.Cents
		lineProfit money.Cents
		base       money.Cents
	}
	var raws []raw
	invOrder := map[int64]int{} // invoiceID -> index into invoice list
	var invIDs []int64
	invFree := map[int64]money.Cents{}
	invDeduct := map[int64]money.Cents{}
	for rows.Next() {
		var r raw
		var qty, total, promo, buy, disc, free, itemDisc int64
		if err := rows.Scan(&r.pl.InvoiceID, &r.pl.SaleType, &r.pl.ProductID, &r.pl.ProductName,
			&qty, &total, &promo, &buy, &disc, &free, &itemDisc); err != nil {
			return nil, sumScanErr(err)
		}
		r.pl.Qty = money.Qty(qty)
		r.pl.Total = money.Cents(total)
		r.pl.Promo = money.Cents(promo)
		r.pl.Buy = money.Cents(buy)
		r.discount = money.Cents(disc)
		r.itemDisc = money.Cents(itemDisc)
		cost, cerr := money.MulQty(r.pl.Buy, r.pl.Qty)
		if cerr != nil {
			return nil, cerr
		}
		r.lineProfit = r.pl.Total - r.pl.Promo - cost
		r.base = r.pl.Total - r.pl.Promo
		if r.base < 0 {
			r.base = 0
		}
		if _, ok := invOrder[r.pl.InvoiceID]; !ok {
			invOrder[r.pl.InvoiceID] = len(invIDs)
			invIDs = append(invIDs, r.pl.InvoiceID)
			invFree[r.pl.InvoiceID] = money.Cents(free)
			manual := r.discount - r.itemDisc
			if manual < 0 {
				manual = 0
			}
			invDeduct[r.pl.InvoiceID] = money.Cents(free) + manual
		}
		raws = append(raws, r)
	}
	if err := rows.Err(); err != nil {
		return nil, sumScanErr(err)
	}
	// Allocate each invoice's deduction pro-rata across its lines by
	// base (Total − Promo); the remainder lands on the last line with
	// a positive base (or the last line when no base is positive).
	for _, invID := range invIDs {
		deduct := invDeduct[invID]
		if deduct <= 0 {
			continue
		}
		var idxs []int
		var baseTotal money.Cents
		for i, r := range raws {
			if r.pl.InvoiceID != invID {
				continue
			}
			idxs = append(idxs, i)
			baseTotal += r.base
		}
		remain := deduct
		for pos, i := range idxs {
			share := money.Cents(0)
			if pos == len(idxs)-1 {
				share = remain
			} else if baseTotal > 0 {
				share64, serr := money.MulDiv(int64(deduct), int64(raws[i].base), int64(baseTotal))
				if serr != nil {
					return nil, serr
				}
				share = money.Cents(share64)
			}
			raws[i].lineProfit -= share
			remain -= share
		}
	}
	out := make([]profitLine, 0, len(raws))
	for _, r := range raws {
		r.pl.Profit = r.lineProfit
		if _, ok := invOrder[r.pl.InvoiceID]; ok {
			r.pl.FreeCost = invFree[r.pl.InvoiceID]
		}
		out = append(out, r.pl)
	}
	return out, nil
}

// reportRange validates a report date range.
func reportRange(from, to string) error { return validateRange(from, to) }

// returnProfitAdj is the profit to subtract for returns in a range,
// in total and broken down by product and by invoice sale type.
// FIX-17 SW2-L01: it also carries each product's returned quantity and
// refunded revenue, so TopProducts can put qty/revenue on the same NET
// basis as profit (the same rows, same day resolution, same filters).
type returnProfitAdj struct {
	total           money.Cents
	byProduct       map[int64]money.Cents
	bySaleType      map[string]money.Cents
	byProductQty    map[int64]money.Qty
	byProductRefund map[int64]money.Cents
}

// returnProfitAdjustments computes the profit to subtract for returns
// dated in [from,to] — the same rule DailyClose applies (FIX-12
// SW-H06): each return of a non-canceled invoice removes
// (refund − qty×buy) from profit, with the voided free-unit cost added
// back (the units are back in stock). Returns are dated by
// returnsRowDay, so a malformed/empty ReturnDate resolves through its
// CashBox leg / shift / invoice exactly like DailyClose does. When
// saleType != "" only returns of invoices of that sale type count.
func (s *Store) returnProfitAdjustments(ctx context.Context, from, to, saleType string) (returnProfitAdj, error) {
	adj := returnProfitAdj{
		byProduct:       map[int64]money.Cents{},
		bySaleType:      map[string]money.Cents{},
		byProductQty:    map[int64]money.Qty{},
		byProductRefund: map[int64]money.Cents{},
	}
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT r.InvoiceID, r.ProductID, r.TotalRefund, r.Quantity,
			COALESCE(NULLIF(r.BuyPrice,0),(SELECT d.BuyPrice FROM InvoiceDetails d
				WHERE d.InvoiceID=r.InvoiceID AND d.ProductID=r.ProductID LIMIT 1),0) AS BuyPrice,
			r.VoidedFreeCost, i.IsCanceled, COALESCE(r.ReturnDate,''), COALESCE(r.ShiftID,0),
			r.ReturnID, COALESCE(r.ReturnEventID,0), COALESCE(i.SaleType,'')
			FROM Returns r LEFT JOIN Invoices i ON i.InvoiceID=r.InvoiceID
			WHERE substr(r.ReturnDate,1,10) BETWEEN ? AND ? OR r.ReturnDate IS NULL
				OR substr(r.ReturnDate,1,10) NOT GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'`, from, to)
		if err != nil {
			return err
		}
		type retRow struct {
			invID      int64
			productID  int64
			refund     int64
			qty        int64
			buy        int64
			voidedCost int64
			canceled   sql.NullInt64
			retDate    string
			shiftID    int64
			retID      int64
			eventID    int64
			invSaleType string
		}
		var rets []retRow
		for rows.Next() {
			var pr retRow
			if err := rows.Scan(&pr.invID, &pr.productID, &pr.refund, &pr.qty, &pr.buy,
				&pr.voidedCost, &pr.canceled, &pr.retDate, &pr.shiftID, &pr.retID, &pr.eventID,
				&pr.invSaleType); err != nil {
				rows.Close()
				return err
			}
			rets = append(rets, pr)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, pr := range rets {
			day := returnsRowDay(tx, pr.invID, pr.shiftID, pr.retID, pr.eventID, pr.retDate)
			if day < from || day > to {
				continue
			}
			if pr.canceled.Valid && pr.canceled.Int64 != 0 {
				continue
			}
			if saleType != "" && pr.invSaleType != saleType {
				continue
			}
			// Same arithmetic as DailyClose: profit -= refund − qty×buy,
			// profit += voidedCost.
			cost, cerr := money.MulQty(money.Cents(pr.buy), money.Qty(pr.qty))
			if cerr != nil {
				return cerr
			}
			effect, cerr := money.SubChecked(money.Cents(pr.refund), cost)
			if cerr != nil {
				return cerr
			}
			effect, cerr = money.SubChecked(effect, money.Cents(pr.voidedCost))
			if cerr != nil {
				return cerr
			}
			adj.total += effect
			adj.byProduct[pr.productID] += effect
			adj.bySaleType[pr.invSaleType] += effect
			// FIX-17 SW2-L01: the same return also removes its qty
			// and its refunded revenue from the product row.
			adj.byProductQty[pr.productID] += money.Qty(pr.qty)
			adj.byProductRefund[pr.productID] += money.Cents(pr.refund)
		}
		return nil
	})
	return adj, err
}

// FIX-19 R19-B-F1: net-received paid amount of one invoice row.
//
// PaidAmount is the gross cash received on an invoice (at-sale payment
// + later collections) and is deliberately never zeroed by ProcessReturn
// (FIX-18 R18-B-02) — it is a historical fact. But the reports' TotalPaid
// must show the cash still held, so each invoice contributes
// PaidAmount minus the cash actually refunded on it. ProcessReturn posts
// every paid-out refund as a negative Payments row (domain_returns.go:
// the paid-out leg, Amount = -paidOut where paidOut = TotalRefund -
// debtReduction), while the debt-absorbed part of a return never touches
// Payments at all — so the negative-Payments sum is exactly the refunded
// payment: no more (subtracting Returns.TotalRefund would also remove
// the debt part, which was never paid) and no less (a partial return
// whose refund exceeds the remaining debt does pay cash out, and a
// fully-returned invoice nets to exactly 0 because paidOut then equals
// PaidAmount). This is the same predicate FIX-18 B-02 uses for
// LastPayment (PaidAmount > refunded), expressed here as a value.
//
// The refund is attributed to the invoice's own (sale) payment method:
// the at-sale payment and later collections are already counted under
// that method, so the refund reverses that same receipt. The total is
// unaffected by the attribution either way.
//
// The refund is netted per invoice regardless of the return's date
// (same as B-02): a sale-period report shows the net cash held for
// sales of that period. All four consumers below use this one
// expression, so the summary query, the per-method queries and the
// CustomerReportFor loop agree exactly. Profit netting is untouched —
// it already nets returns via returnProfitAdjustments.
const netPaidExpr = `i.PaidAmount - COALESCE((SELECT -SUM(p.Amount) FROM Payments p WHERE p.InvoiceID=i.InvoiceID AND p.Amount<0),0)`

// SalesReportFor returns the sales report for a range (Admin/Supervisor).
func (s *Store) SalesReportFor(ctx context.Context, user *auth.User, from, to string) (SalesReport, error) {
	var r SalesReport
	if err := rbac.Require(user.Role, rbac.PermViewReports); err != nil {
		return r, err
	}
	if err := reportRange(from, to); err != nil {
		return r, err
	}
	r.From, r.To = from, to
	err := s.DB.QueryRow(`SELECT COUNT(*), COALESCE(SUM(NetAmount),0), COALESCE(SUM(TotalAmount),0), COALESCE(SUM(Discount),0),
		COALESCE(SUM(NetAmount),0), COALESCE(SUM(`+netPaidExpr+`),0), COALESCE(SUM(RemainingAmount),0)
		FROM Invoices i WHERE i.IsCanceled=0 AND substr(i.InvoiceDate,1,10) BETWEEN ? AND ?`,
		from, to).Scan(&r.InvoiceCount, &r.TotalSales, &r.TotalGross, &r.TotalDiscount, &r.TotalNet, &r.TotalPaid, &r.TotalRemaining)
	if err != nil {
		return r, sumScanErr(err)
	}
	// Unified profit (FIX-1 M1): same definition as InvoiceProfit.
	plines, err := loadProfitLines(s.DB, from, to, "")
	if err != nil {
		return r, err
	}
	for _, ln := range plines {
		r.TotalProfit += ln.Profit
	}
	// FIX-12 SW-H06: returns remove their profit, exactly like
	// DailyClose — without this the report overstates profit by the
	// return profit.
	if adj, err := s.returnProfitAdjustments(ctx, from, to, ""); err != nil {
		return r, err
	} else {
		r.TotalProfit -= adj.total
	}
	dayRows, err := s.DB.Query(`SELECT substr(InvoiceDate,1,10), COUNT(*), COALESCE(SUM(NetAmount),0)
		FROM Invoices WHERE IsCanceled=0 AND substr(InvoiceDate,1,10) BETWEEN ? AND ?
		GROUP BY substr(InvoiceDate,1,10) ORDER BY 1`, from, to)
	if err != nil {
		return r, sumScanErr(err)
	}
	for dayRows.Next() {
		var d DayRow
		var tot int64
		if err := dayRows.Scan(&d.Day, &d.InvoiceCount, &tot); err != nil {
			dayRows.Close()
			return r, sumScanErr(err)
		}
		d.Total = money.Cents(tot)
		r.ByDay = append(r.ByDay, d)
	}
	dayRows.Close()
	payRows, err := s.DB.Query(`SELECT PaymentMethod, COUNT(*), COALESCE(SUM(`+netPaidExpr+`),0)
		FROM Invoices i WHERE i.IsCanceled=0 AND substr(i.InvoiceDate,1,10) BETWEEN ? AND ?
		GROUP BY PaymentMethod ORDER BY SUM(`+netPaidExpr+`) DESC`, from, to)
	if err != nil {
		return r, sumScanErr(err)
	}
	for payRows.Next() {
		var p PaymentRow
		var tot int64
		if err := payRows.Scan(&p.Method, &p.Count, &tot); err != nil {
			payRows.Close()
			return r, sumScanErr(err)
		}
		p.Total = money.Cents(tot)
		r.ByPayment = append(r.ByPayment, p)
	}
	payRows.Close()
	return r, sumScanErr(payRows.Err())
}

// ProfitReportFor returns the profit report / analysis for a range.
// saleType "" = all types; otherwise retail/wholesale/...
func (s *Store) ProfitReportFor(ctx context.Context, user *auth.User, from, to, saleType string) (ProfitReport, error) {
	var r ProfitReport
	if err := rbac.Require(user.Role, rbac.PermViewProfitAnalysis); err != nil {
		return r, err
	}
	if err := reportRange(from, to); err != nil {
		return r, err
	}
	r.From, r.To = from, to
	// Totals + groupings from the unified profit lines (FIX-1 M1):
	// TotalSales = Σ TotalPrice; TotalCost = Σ rounded qty×BuyPrice +
	// Σ FreeCost (once per invoice); TotalProfit = Σ allocated profit.
	plines, err := loadProfitLines(s.DB, from, to, saleType)
	if err != nil {
		return r, err
	}
	seenFree := map[int64]bool{}
	byType := map[string]*SaleTypeRow{}
	var typeOrder []string
	invPerType := map[string]map[int64]bool{}
	byProd := map[int64]*ProductProfitRow{}
	var prodOrder []int64
	for _, ln := range plines {
		var err error
		r.TotalSales, err = money.AddChecked(r.TotalSales, ln.Total)
		if err != nil {
			return ProfitReport{}, err
		}
		r.TotalProfit, err = money.AddChecked(r.TotalProfit, ln.Profit)
		if err != nil {
			return ProfitReport{}, err
		}
		cost, err := money.MulQty(ln.Buy, ln.Qty)
		if err != nil {
			return ProfitReport{}, err
		}
		r.TotalCost, err = money.AddChecked(r.TotalCost, cost)
		if err != nil {
			return ProfitReport{}, err
		}
		if !seenFree[ln.InvoiceID] {
			seenFree[ln.InvoiceID] = true
			r.TotalCost, err = money.AddChecked(r.TotalCost, ln.FreeCost)
			if err != nil {
				return ProfitReport{}, err
			}
		}
		st, ok := byType[ln.SaleType]
		if !ok {
			st = &SaleTypeRow{SaleType: ln.SaleType}
			byType[ln.SaleType] = st
			typeOrder = append(typeOrder, ln.SaleType)
			invPerType[ln.SaleType] = map[int64]bool{}
		}
		st.TotalSales += ln.Total
		st.TotalProfit += ln.Profit
		invPerType[ln.SaleType][ln.InvoiceID] = true
		pr, ok := byProd[ln.ProductID]
		if !ok {
			pr = &ProductProfitRow{ProductID: ln.ProductID, ProductName: ln.ProductName}
			byProd[ln.ProductID] = pr
			prodOrder = append(prodOrder, ln.ProductID)
		}
		pr.QtySold += ln.Qty
		pr.TotalSales += ln.Total
		pr.TotalProfit += ln.Profit
	}
	for _, stName := range typeOrder {
		st := byType[stName]
		st.InvoiceCount = int64(len(invPerType[stName]))
		r.BySaleType = append(r.BySaleType, *st)
	}
	// FIX-12 SW-H06: returns remove their profit, exactly like
	// DailyClose — per sale type and per product as well as in total.
	// byProd is adjusted here so the Top10/Bottom10 built below from
	// it already carry the corrected profit.
	if adj, err := s.returnProfitAdjustments(ctx, from, to, saleType); err != nil {
		return r, err
	} else {
		r.TotalProfit -= adj.total
		for i := range r.BySaleType {
			r.BySaleType[i].TotalProfit -= adj.bySaleType[r.BySaleType[i].SaleType]
		}
		for pid, effect := range adj.byProduct {
			if pr, ok := byProd[pid]; ok {
				pr.TotalProfit -= effect
			}
		}
	}
	// Products sorted by profit desc (same order as before).
	all := make([]ProductProfitRow, 0, len(prodOrder))
	for _, pid := range prodOrder {
		all = append(all, *byProd[pid])
	}
	sort.Slice(all, func(i, j int) bool { return all[i].TotalProfit > all[j].TotalProfit })
	if len(all) > 10 {
		r.Top10 = all[:10]
		// FIX-1 L5: copy before reversing — Top10 and Bottom10 used
		// to share the same backing slice, so reversing Bottom10
		// also reversed Top10.
		r.Bottom10 = append([]ProductProfitRow(nil), all[len(all)-10:]...)
	} else {
		r.Top10 = all
		r.Bottom10 = append([]ProductProfitRow(nil), all...)
	}
	// Bottom 10 shown worst-first.
	for i, j := 0, len(r.Bottom10)-1; i < j; i, j = i+1, j-1 {
		r.Bottom10[i], r.Bottom10[j] = r.Bottom10[j], r.Bottom10[i]
	}
	return r, nil
}

// DebtsReportFor returns customers with a live remaining balance.
func (s *Store) DebtsReportFor(ctx context.Context, user *auth.User) ([]DebtRow, money.Cents, error) {
	if err := rbac.Require(user.Role, rbac.PermViewReports); err != nil {
		return nil, 0, err
	}
	rows, err := s.DB.Query(`SELECT c.CustomerID,c.CustomerName,COALESCE(c.Phone,''),
		COALESCE(SUM(i.RemainingAmount),0), COUNT(i.InvoiceID)
		FROM Customers c JOIN Invoices i ON i.CustomerID=c.CustomerID
		WHERE i.IsCanceled=0 AND i.RemainingAmount>0
		GROUP BY c.CustomerID,c.CustomerName,c.Phone ORDER BY SUM(i.RemainingAmount) DESC`)
	if err != nil {
		return nil, 0, sumScanErr(err)
	}
	defer rows.Close()
	var out []DebtRow
	var total money.Cents
	for rows.Next() {
		var d DebtRow
		var bal int64
		if err := rows.Scan(&d.CustomerID, &d.CustomerName, &d.Phone, &bal, &d.InvoiceCount); err != nil {
			return nil, 0, sumScanErr(err)
		}
		d.Balance = money.Cents(bal)
		total += d.Balance
		out = append(out, d)
	}
	return out, total, sumScanErr(rows.Err())
}

// LowStockReport is provided by B1 (domain_products.go: thin adapter over
// LowStockProducts, returning []LowStockRow). Not duplicated here.

// CanceledInvoicesFor returns canceled invoices in a range.
func (s *Store) CanceledInvoicesFor(ctx context.Context, user *auth.User, from, to string) ([]CanceledInvoiceRow, error) {
	if err := rbac.Require(user.Role, rbac.PermViewReports); err != nil {
		return nil, err
	}
	if err := reportRange(from, to); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(`SELECT InvoiceNumber,substr(COALESCE(CanceledAt,InvoiceDate),1,10),
		COALESCE(UserName,''),NetAmount,COALESCE(CanceledBy,''),COALESCE(CancelReason,'')
		FROM Invoices WHERE IsCanceled=1 AND substr(COALESCE(CanceledAt,InvoiceDate),1,10) BETWEEN ? AND ?
		ORDER BY InvoiceNumber DESC`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CanceledInvoiceRow
	for rows.Next() {
		var c CanceledInvoiceRow
		var net int64
		if err := rows.Scan(&c.InvoiceNumber, &c.InvoiceDate, &c.UserName, &net, &c.CanceledBy, &c.CancelReason); err != nil {
			return nil, err
		}
		c.NetAmount = money.Cents(net)
		out = append(out, c)
	}
	return out, rows.Err()
}

// sumScanErr maps a SQL SUM scan failure to the Arabic overflow
// refusal when SQLite reports an integer overflow (absurd stored
// rows), and returns any other error unchanged. A failed SUM must
// never surface as a silent zero in a money total (R22-E-F1: the
// payments report showed المرتجعات: 0.00 while absurd Returns rows
// existed, because the Scan error was swallowed with `_ =`).
func sumScanErr(err error) error {
	if err != nil && strings.Contains(err.Error(), "integer overflow") {
		return money.ErrOverflow
	}
	return err
}

// PaymentReportFor returns the payment-method + drawer report (Q-03).
func (s *Store) PaymentReportFor(ctx context.Context, user *auth.User, from, to string) (PaymentReport, error) {
	var r PaymentReport
	if err := rbac.Require(user.Role, rbac.PermViewPaymentReport); err != nil {
		return r, err
	}
	if err := reportRange(from, to); err != nil {
		return r, err
	}
	r.From, r.To = from, to
	rows, err := s.DB.Query(`SELECT PaymentMethod, COUNT(*), COALESCE(SUM(`+netPaidExpr+`),0)
		FROM Invoices i WHERE i.IsCanceled=0 AND substr(i.InvoiceDate,1,10) BETWEEN ? AND ?
		GROUP BY PaymentMethod ORDER BY SUM(`+netPaidExpr+`) DESC`, from, to)
	if err != nil {
		return r, sumScanErr(err)
	}
	for rows.Next() {
		var p PaymentRow
		var tot int64
		if err := rows.Scan(&p.Method, &p.Count, &tot); err != nil {
			rows.Close()
			return r, sumScanErr(err)
		}
		p.Total = money.Cents(tot)
		r.ByMethod = append(r.ByMethod, p)
	}
	rows.Close()
	var inSum, outSum, expSum, retSum sql.NullInt64
	// FIX-2 M-1: only real cash movements count — 'open', 'resume' and
	// 'close' rows are snapshots, not money in/out (an open of 10000
	// plus a close of 14500 used to inflate CashIn/CashOut by 24500).
	// R22-E-F1: the SUM scans must surface instead of silently
	// zeroing the totals — a failed SUM is an error, not a 0.00.
	if err := sumScanErr(s.DB.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN MovementType='in' THEN Amount ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN MovementType='out' THEN Amount ELSE 0 END),0)
		FROM CashBox WHERE substr(MovementDate,1,10) BETWEEN ? AND ?`, from, to).Scan(&inSum, &outSum)); err != nil {
		return r, err
	}
	if err := sumScanErr(s.DB.QueryRow(`SELECT COALESCE(SUM(Amount),0) FROM Expenses
		WHERE substr(ExpenseDate,1,10) BETWEEN ? AND ?`, from, to).Scan(&expSum)); err != nil {
		return r, err
	}
	if err := sumScanErr(s.DB.QueryRow(`SELECT COALESCE(SUM(TotalRefund),0) FROM Returns
		WHERE substr(ReturnDate,1,10) BETWEEN ? AND ?`, from, to).Scan(&retSum)); err != nil {
		return r, err
	}
	r.CashIn = money.Cents(inSum.Int64)
	r.CashOut = money.Cents(outSum.Int64)
	r.Expenses = money.Cents(expSum.Int64)
	r.Returns = money.Cents(retSum.Int64)
	return r, nil
}

// CustomerReportFor returns the per-customer report (Q-04).
func (s *Store) CustomerReportFor(ctx context.Context, user *auth.User, customerID int64, from, to string) (CustomerReport, error) {
	var r CustomerReport
	if err := rbac.Require(user.Role, rbac.PermViewCustomerReport); err != nil {
		return r, err
	}
	if err := reportRange(from, to); err != nil {
		return r, err
	}
	var credit int64
	err := s.DB.QueryRow(`SELECT CustomerID,CustomerName,COALESCE(Phone,''),CreditLimit
		FROM Customers WHERE CustomerID=?`, customerID).Scan(&r.CustomerID, &r.CustomerName, &r.Phone, &credit)
	if err == sql.ErrNoRows {
		return r, errors.New("العميل غير موجود")
	}
	if err != nil {
		return r, err
	}
	r.CreditLimit = money.Cents(credit)
	r.From, r.To = from, to
	bal, err := s.LiveCustomerBalanceDB(customerID)
	if err != nil {
		return r, err
	}
	r.Balance = bal
	rows, err := s.DB.Query(`SELECT i.InvoiceID,i.InvoiceNumber,substr(i.InvoiceDate,1,10),i.NetAmount,i.PaidAmount,i.RemainingAmount,i.IsCanceled,
		COALESCE((SELECT -SUM(p.Amount) FROM Payments p WHERE p.InvoiceID=i.InvoiceID AND p.Amount<0),0)
		FROM Invoices i WHERE i.CustomerID=? AND substr(i.InvoiceDate,1,10) BETWEEN ? AND ?
		ORDER BY i.InvoiceDate DESC`, customerID, from, to)
	if err != nil {
		return r, err
	}
	for rows.Next() {
		var inv CustomerInvoiceRow
		var net, paid, rem int64
		var canceled int
		var refunded int64
		if err := rows.Scan(&inv.InvoiceID, &inv.InvoiceNumber, &inv.InvoiceDate, &net, &paid, &rem, &canceled, &refunded); err != nil {
			rows.Close()
			return r, err
		}
		inv.NetAmount, inv.PaidAmount, inv.Remaining = money.Cents(net), money.Cents(paid), money.Cents(rem)
		inv.Canceled = canceled == 1
		r.Invoices = append(r.Invoices, inv)
		if !inv.Canceled {
			r.InvoiceCount++
			r.TotalSales += inv.NetAmount
			// FIX-19 R19-B-F1: net the refunded payment out of
			// TotalPaid (same netPaidExpr as the SQL summaries);
			// the per-row PaidAmount above stays the gross
			// historical fact.
			r.TotalPaid += inv.PaidAmount - money.Cents(refunded)
		}
	}
	rows.Close()
	// CustomerPayments.PaymentMethod is stored now (FIX-1 M2) — read it
	// instead of hard-coding 'Cash'.
	// FIX-22 R22-A-F1: same class as FIX-21 R21-B-F1 on the statement —
	// a collection on an invoice that was later canceled is part of
	// that void, not a live payment, so it must not stay in the
	// report's "المدفوعات" table while the header total (computed
	// from the invoices loop above, non-canceled only) reads 0.00.
	// Scope mirrors CustomerStatement leg 1: a collection counts only
	// when its invoice is NULL/0 (walk-in, no invoice) or non-canceled.
	payRows, err := s.DB.Query(`SELECT substr(cp.PaymentDate,1,10),cp.Amount,COALESCE(cp.PaymentMethod,'Cash'),COALESCE(cp.ReceivedBy,''),cp.InvoiceID
		FROM CustomerPayments cp WHERE cp.CustomerID=? AND substr(cp.PaymentDate,1,10) BETWEEN ? AND ?
		AND (cp.InvoiceID IS NULL OR cp.InvoiceID=0 OR EXISTS (SELECT 1 FROM Invoices i
			WHERE i.InvoiceID=cp.InvoiceID AND i.IsCanceled=0))
		ORDER BY cp.PaymentDate DESC, cp.CPaymentID DESC`, customerID, from, to)
	if err != nil {
		return r, err
	}
	type rawPayment struct {
		row     CustomerPaymentRow
		invID   sql.NullInt64
	}
	var raw []rawPayment
	for payRows.Next() {
		var p CustomerPaymentRow
		var amt int64
		var invID sql.NullInt64
		if err := payRows.Scan(&p.PaymentDate, &amt, &p.Method, &p.ReceivedBy, &invID); err != nil {
			payRows.Close()
			return r, err
		}
		p.Amount = money.Cents(amt)
		raw = append(raw, rawPayment{row: p, invID: invID})
	}
	payRows.Close()
	if err := payRows.Err(); err != nil {
		return r, err
	}
	// FIX-23 R23-A-F1: the table was refund-blind — a collection fully
	// refunded by a return (no cancel) stayed listed at gross while
	// the header TotalPaid above nets the same refund per invoice
	// (PaidAmount - refunded). Mirror the ListDebtors net-received
	// gate, but keep the row (at its NET amount) instead of dropping
	// it outright: a partially-refunded collection is still held in
	// part, so the row must read the net received amount, and a
	// fully-refunded one nets to 0 and drops out, leaving the table
	// empty exactly as the header reads 0.00. Refunds are the
	// negative Payments rows on the invoice (same subquery the
	// header uses); the refund is applied to that invoice's rows
	// newest-first (the query order above is PaymentDate DESC with
	// CPaymentID DESC as the tie-breaker — FIX-24 R24-A-O1: Now() is
	// second-precision, so same-second collections need the ID tie
	// to make refund allocation deterministic), so
	// the rows still sum to the net received on the invoice and no
	// row ever goes negative. Walk-in rows (NULL/0 InvoiceID)
	// cannot be attributed to a refund and are kept as-is, exactly
	// as ListDebtors keeps them.
	refundedByInv := map[int64]money.Cents{}
	{
		var invIDs []int64
		seenInv := map[int64]bool{}
		for _, rp := range raw {
			if rp.invID.Valid && rp.invID.Int64 > 0 && !seenInv[rp.invID.Int64] {
				seenInv[rp.invID.Int64] = true
				invIDs = append(invIDs, rp.invID.Int64)
			}
		}
		if len(invIDs) > 0 {
			ph := strings.Repeat("?,", len(invIDs))
			ph = ph[:len(ph)-1]
			args := make([]any, len(invIDs))
			for i, id := range invIDs {
				args[i] = id
			}
			refRows, err := s.DB.Query(`SELECT InvoiceID,COALESCE(-SUM(Amount),0) FROM Payments
				WHERE InvoiceID IN (`+ph+`) AND Amount<0 GROUP BY InvoiceID`, args...)
			if err != nil {
				return r, err
			}
			for refRows.Next() {
				var id, refunded int64
				if err := refRows.Scan(&id, &refunded); err != nil {
					refRows.Close()
					return r, err
				}
				refundedByInv[id] = money.Cents(refunded)
			}
			refRows.Close()
			if err := refRows.Err(); err != nil {
				return r, err
			}
		}
	}
	remainingRefund := map[int64]money.Cents{}
	for id, v := range refundedByInv {
		remainingRefund[id] = v
	}
	for _, rp := range raw {
		if !rp.invID.Valid || rp.invID.Int64 <= 0 {
			r.Payments = append(r.Payments, rp.row)
			continue
		}
		net := rp.row.Amount
		if left := remainingRefund[rp.invID.Int64]; left > 0 {
			if left >= net {
				remainingRefund[rp.invID.Int64] = left - net
				net = 0
			} else {
				net -= left
				remainingRefund[rp.invID.Int64] = 0
			}
		}
		if net > 0 {
			rp.row.Amount = net
			r.Payments = append(r.Payments, rp.row)
		}
	}
	return r, nil
}

// Return analysis is owned by B2 (domain_returns.go: GetReturnsAnalytics +
// ReturnAnalysisCSV, with lost-profit). It is used directly by the reports
// handlers and the CSV exporter below — not rebuilt here.

// --- Manager dashboard (Q-05) ---

// DashboardInvoice is a recent invoice line for the dashboard.
type DashboardInvoice struct {
	InvoiceNumber int64
	InvoiceDate   string
	UserName      string
	CustomerName  string
	NetAmount     money.Cents
	Canceled      bool
}

// OpenShiftInfo is one open shift for the dashboard / monitor.
type OpenShiftInfo struct {
	ShiftID        int64
	UserName       string
	ShiftDate      string
	OpenTime       string
	OpeningBalance money.Cents
	ExpectedDrawer money.Cents
}

// VendorDebtRow is one vendor with an outstanding balance.
type VendorDebtRow struct {
	VendorID   int64
	VendorName string
	Balance    money.Cents
}

// AdminDashboard is the Q-05 manager dashboard payload.
type AdminDashboard struct {
	TodaySales     money.Cents
	TodayInvoices  int64
	TodayCanceled  int64
	OpenShifts     int
	LowStockCount  int
	TotalDebts     money.Cents
	UnreadAudit    int
	UnreadNotif    int
	RecentInvoices []DashboardInvoice
	LowStock       []LowStockRow
	TopDebts       []DebtRow
	Shifts         []OpenShiftInfo
	RecentAudit    []AuditEntry
	VendorDebts    []VendorDebtRow
}

// Dashboard returns the manager dashboard (Admin/Supervisor).
func (s *Store) Dashboard(ctx context.Context, user *auth.User) (AdminDashboard, error) {
	var d AdminDashboard
	if err := rbac.Require(user.Role, rbac.PermAccessDashboard); err != nil {
		return d, err
	}
	today := Today()
	var sales sql.NullInt64
	// R22-E-F1: a failed SUM must surface instead of silently
	// zeroing the dashboard totals (same `_ =` class as :722).
	if err := sumScanErr(s.DB.QueryRow(`SELECT COUNT(*), COALESCE(SUM(NetAmount),0) FROM Invoices
		WHERE IsCanceled=0 AND substr(InvoiceDate,1,10)=?`, today).Scan(&d.TodayInvoices, &sales)); err != nil {
		return d, err
	}
	d.TodaySales = money.Cents(sales.Int64)
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM Invoices WHERE IsCanceled=1 AND substr(COALESCE(CanceledAt,InvoiceDate),1,10)=?`, today).Scan(&d.TodayCanceled)
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM Shifts WHERE IsOpen=1`).Scan(&d.OpenShifts)
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM Products WHERE IsActive=1 AND Quantity<=MinQuantity`).Scan(&d.LowStockCount)
	// R22-E-F1: same `_ =` class as :722 — surface, never silent 0.00.
	if err := sumScanErr(s.DB.QueryRow(`SELECT COALESCE(SUM(RemainingAmount),0) FROM Invoices WHERE IsCanceled=0 AND RemainingAmount>0`).Scan(&sales)); err != nil {
		return d, err
	}
	d.TotalDebts = money.Cents(sales.Int64)
	if n, err := s.UnreadAuditAlertsCount(ctx, user); err == nil {
		d.UnreadAudit = n
	}
	if n, err := s.UnreadNotificationCount(ctx, user); err == nil {
		d.UnreadNotif = n
	}
	invRows, err := s.DB.Query(`SELECT i.InvoiceNumber, substr(i.InvoiceDate,1,16), COALESCE(i.UserName,''),
		COALESCE(c.CustomerName,''), i.NetAmount, i.IsCanceled
		FROM Invoices i LEFT JOIN Customers c ON c.CustomerID=i.CustomerID
		ORDER BY i.InvoiceID DESC LIMIT 10`)
	if err == nil {
		for invRows.Next() {
			var inv DashboardInvoice
			var net int64
			var canceled int
			if err := invRows.Scan(&inv.InvoiceNumber, &inv.InvoiceDate, &inv.UserName, &inv.CustomerName, &net, &canceled); err != nil {
				invRows.Close()
				return d, err
			}
			inv.NetAmount = money.Cents(net)
			inv.Canceled = canceled == 1
			d.RecentInvoices = append(d.RecentInvoices, inv)
		}
		invRows.Close()
	}
	if low, err := s.LowStockReport(ctx, user); err == nil {
		if len(low) > 10 {
			low = low[:10]
		}
		d.LowStock = low
	}
	if debts, _, err := s.DebtsReportFor(ctx, user); err == nil {
		if len(debts) > 10 {
			debts = debts[:10]
		}
		d.TopDebts = debts
	}
	shiftRows, err := s.DB.Query(`SELECT ShiftID,COALESCE(UserName,''),ShiftDate,COALESCE(OpenTime,''),
		OpeningBalance FROM Shifts WHERE IsOpen=1 ORDER BY ShiftID DESC`)
	if err == nil {
		for shiftRows.Next() {
			var sh OpenShiftInfo
			var opening int64
			if err := shiftRows.Scan(&sh.ShiftID, &sh.UserName, &sh.ShiftDate, &sh.OpenTime, &opening); err != nil {
				shiftRows.Close()
				return d, err
			}
			sh.OpeningBalance = money.Cents(opening)
			d.Shifts = append(d.Shifts, sh)
		}
		shiftRows.Close()
		// Expected drawer AFTER closing the rows: opening a transaction
		// while rows are open would deadlock on the single connection.
		// R23-C-F1: a DrawerExpectedBalance error (e.g. the SQLite SUM
		// overflowing on absurd CashBox rows) must surface, never be
		// swallowed into a silent 0.00 ExpectedDrawer — the same
		// silent-zero class R22-E-F1 eliminated for the SUMs above.
		for i := range d.Shifts {
			if err := s.Tx(ctx, func(tx *sql.Tx) error {
				exp, err := DrawerExpectedBalance(tx, d.Shifts[i].ShiftID)
				if err != nil {
					return err
				}
				d.Shifts[i].ExpectedDrawer = exp
				return nil
			}); err != nil {
				return d, err
			}
		}
	}
	if audit, err := s.ListAuditLog(ctx, user, AuditFilter{Limit: 10}); err == nil {
		d.RecentAudit = audit
	}
	vRows, err := s.DB.Query(`SELECT VendorID,VendorName,CurrentBalance FROM Vendors
		WHERE CurrentBalance<>0 ORDER BY CurrentBalance DESC LIMIT 10`)
	if err == nil {
		for vRows.Next() {
			var v VendorDebtRow
			var bal int64
			if err := vRows.Scan(&v.VendorID, &v.VendorName, &bal); err != nil {
				vRows.Close()
				return d, err
			}
			v.Balance = money.Cents(bal)
			d.VendorDebts = append(d.VendorDebts, v)
		}
		vRows.Close()
	}
	return d, nil
}

// --- Shift monitor ---

// CashierPerformance is one cashier's performance in a range.
type CashierPerformance struct {
	UserName      string
	InvoiceCount  int64
	TotalSales    money.Cents
	CanceledCount int64
	ReturnCount   int64
	ReturnTotal   money.Cents
}

// ShiftMonitor is the open-shifts + cashier-performance view.
type ShiftMonitor struct {
	From        string
	To          string
	OpenShifts  []OpenShiftInfo
	Performance []CashierPerformance
}

// ShiftMonitorFor returns the shift monitor for a range.
func (s *Store) ShiftMonitorFor(ctx context.Context, user *auth.User, from, to string) (ShiftMonitor, error) {
	var m ShiftMonitor
	if err := rbac.Require(user.Role, rbac.PermShiftMonitor); err != nil {
		return m, err
	}
	if err := reportRange(from, to); err != nil {
		return m, err
	}
	m.From, m.To = from, to
	shiftRows, err := s.DB.Query(`SELECT ShiftID,COALESCE(UserName,''),ShiftDate,COALESCE(OpenTime,''),
		OpeningBalance FROM Shifts WHERE IsOpen=1 ORDER BY ShiftID DESC`)
	if err != nil {
		return m, err
	}
	for shiftRows.Next() {
		var sh OpenShiftInfo
		var opening int64
		if err := shiftRows.Scan(&sh.ShiftID, &sh.UserName, &sh.ShiftDate, &sh.OpenTime, &opening); err != nil {
			shiftRows.Close()
			return m, err
		}
		sh.OpeningBalance = money.Cents(opening)
		m.OpenShifts = append(m.OpenShifts, sh)
	}
	shiftRows.Close()
	if err := shiftRows.Err(); err != nil {
		return m, err
	}
	// Expected drawer AFTER closing the rows (see Dashboard).
	// R23-C-F1: propagate a DrawerExpectedBalance error instead of
	// swallowing it into a silent 0.00 ExpectedDrawer.
	for i := range m.OpenShifts {
		if err := s.Tx(ctx, func(tx *sql.Tx) error {
			exp, err := DrawerExpectedBalance(tx, m.OpenShifts[i].ShiftID)
			if err != nil {
				return err
			}
			m.OpenShifts[i].ExpectedDrawer = exp
			return nil
		}); err != nil {
			return m, err
		}
	}
	rows, err := s.DB.Query(`SELECT COALESCE(UserName,'') u, COUNT(*), COALESCE(SUM(NetAmount),0)
		FROM Invoices WHERE IsCanceled=0 AND substr(InvoiceDate,1,10) BETWEEN ? AND ?
		GROUP BY u ORDER BY SUM(NetAmount) DESC`, from, to)
	if err != nil {
		return m, err
	}
	perfByUser := map[string]int{}
	for rows.Next() {
		var p CashierPerformance
		var tot int64
		if err := rows.Scan(&p.UserName, &p.InvoiceCount, &tot); err != nil {
			rows.Close()
			return m, err
		}
		p.TotalSales = money.Cents(tot)
		m.Performance = append(m.Performance, p)
		perfByUser[p.UserName] = len(m.Performance) - 1
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return m, err
	}
	cRows, err := s.DB.Query(`SELECT COALESCE(UserName,'') u, COUNT(*) FROM Invoices
		WHERE IsCanceled=1 AND substr(COALESCE(CanceledAt,InvoiceDate),1,10) BETWEEN ? AND ?
		GROUP BY u`, from, to)
	if err != nil {
		return m, err
	}
	for cRows.Next() {
		var uname string
		var cnt int64
		if err := cRows.Scan(&uname, &cnt); err != nil {
			cRows.Close()
			return m, err
		}
		// FIX-12 SW-L06: a user who only canceled (no live sales)
		// still gets a performance row — otherwise their cancels
		// vanish from the monitor entirely.
		idx, ok := perfByUser[uname]
		if !ok {
			m.Performance = append(m.Performance, CashierPerformance{UserName: uname})
			idx = len(m.Performance) - 1
			perfByUser[uname] = idx
		}
		m.Performance[idx].CanceledCount = cnt
	}
	cRows.Close()
	if err := cRows.Err(); err != nil {
		return m, err
	}
	rRows, err := s.DB.Query(`SELECT COALESCE(UserName,'') u, COUNT(*), COALESCE(SUM(TotalRefund),0)
		FROM Returns WHERE substr(ReturnDate,1,10) BETWEEN ? AND ? GROUP BY u`, from, to)
	if err != nil {
		return m, err
	}
	for rRows.Next() {
		var uname string
		var cnt, tot int64
		if err := rRows.Scan(&uname, &cnt, &tot); err != nil {
			rRows.Close()
			return m, err
		}
		// FIX-12 SW-L06: a user who only performed returns (no live
		// sales) still gets a performance row with their return
		// counts attached — returns are attributed to the performer.
		idx, ok := perfByUser[uname]
		if !ok {
			m.Performance = append(m.Performance, CashierPerformance{UserName: uname})
			idx = len(m.Performance) - 1
			perfByUser[uname] = idx
		}
		m.Performance[idx].ReturnCount = cnt
		m.Performance[idx].ReturnTotal = money.Cents(tot)
	}
	rRows.Close()
	return m, rRows.Err()
}

// --- CSV export ---

// ExportReportCSV builds a CSV for a report kind (Admin/Manager only).
// Kinds: sales, profit, debts, top_products, low_stock, canceled, payments, returns.
func (s *Store) ExportReportCSV(ctx context.Context, user *auth.User, kind, from, to string) ([]byte, string, error) {
	if err := rbac.Require(user.Role, rbac.PermExportCSV); err != nil {
		return nil, "", err
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	// BOM so Excel opens Arabic correctly.
	buf.WriteString("\ufeff")
	filename := "report_" + kind + "_" + from + "_to_" + to + ".csv"
	switch kind {
	case "sales":
		r, err := s.SalesReportFor(ctx, user, from, to)
		if err != nil {
			return nil, "", err
		}
		_ = w.Write([]string{"اليوم", "عدد الفواتير", "إجمالي المبيعات (قرش)"})
		for _, d := range r.ByDay {
			_ = w.Write([]string{d.Day, strconv.FormatInt(d.InvoiceCount, 10), strconv.FormatInt(int64(d.Total), 10)})
		}
		_ = w.Write([]string{"الإجمالي", strconv.FormatInt(r.InvoiceCount, 10), strconv.FormatInt(int64(r.TotalSales), 10)})
	case "profit":
		r, err := s.ProfitReportFor(ctx, user, from, to, "")
		if err != nil {
			return nil, "", err
		}
		_ = w.Write([]string{"المنتج", "الكمية (ملي)", "إجمالي المبيعات (قرش)", "الربح (قرش)"})
		for _, p := range r.Top10 {
			_ = w.Write([]string{p.ProductName, strconv.FormatInt(int64(p.QtySold), 10),
				strconv.FormatInt(int64(p.TotalSales), 10), strconv.FormatInt(int64(p.TotalProfit), 10)})
		}
		_ = w.Write([]string{"الإجمالي", "", strconv.FormatInt(int64(r.TotalSales), 10), strconv.FormatInt(int64(r.TotalProfit), 10)})
	case "debts":
		debts, total, err := s.DebtsReportFor(ctx, user)
		if err != nil {
			return nil, "", err
		}
		_ = w.Write([]string{"العميل", "الهاتف", "عدد الفواتير", "الرصيد (قرش)"})
		for _, d := range debts {
			_ = w.Write([]string{d.CustomerName, d.Phone, strconv.FormatInt(d.InvoiceCount, 10), strconv.FormatInt(int64(d.Balance), 10)})
		}
		_ = w.Write([]string{"الإجمالي", "", "", strconv.FormatInt(int64(total), 10)})
	case "top_products":
		rows, err := s.TopProducts(ctx, user, from, to, 50, false)
		if err != nil {
			return nil, "", err
		}
		_ = w.Write([]string{"المنتج", "الكمية (ملي)", "إجمالي المبيعات (قرش)", "الربح (قرش)"})
		for _, p := range rows {
			_ = w.Write([]string{p.ProductName, strconv.FormatInt(int64(p.Qty), 10),
				strconv.FormatInt(int64(p.Revenue), 10), strconv.FormatInt(int64(p.Profit), 10)})
		}
	case "low_stock":
		rows, err := s.LowStockReport(ctx, user)
		if err != nil {
			return nil, "", err
		}
		_ = w.Write([]string{"المنتج", "الباركود", "الكمية (ملي)", "الحد الأدنى (ملي)"})
		for _, p := range rows {
			_ = w.Write([]string{p.ProductName, p.Barcode, strconv.FormatInt(int64(p.Quantity), 10), strconv.FormatInt(int64(p.MinQuantity), 10)})
		}
	case "canceled":
		rows, err := s.CanceledInvoicesFor(ctx, user, from, to)
		if err != nil {
			return nil, "", err
		}
		_ = w.Write([]string{"رقم الفاتورة", "التاريخ", "الكاشير", "الصافي (قرش)", "ألغاها", "السبب"})
		for _, c := range rows {
			_ = w.Write([]string{strconv.FormatInt(c.InvoiceNumber, 10), c.InvoiceDate, c.UserName,
				strconv.FormatInt(int64(c.NetAmount), 10), c.CanceledBy, c.CancelReason})
		}
	case "payments":
		r, err := s.PaymentReportFor(ctx, user, from, to)
		if err != nil {
			return nil, "", err
		}
		_ = w.Write([]string{"طريقة الدفع", "عدد الفواتير", "الإجمالي (قرش)"})
		for _, p := range r.ByMethod {
			_ = w.Write([]string{p.Method, strconv.FormatInt(p.Count, 10), strconv.FormatInt(int64(p.Total), 10)})
		}
	case "returns":
		// B2's analysis CSV (includes lost profit).
		csvStr, err := s.ReturnAnalysisCSV(ctx, user, from, to)
		if err != nil {
			return nil, "", err
		}
		w.Flush()
		return []byte("\ufeff" + csvStr), filename, nil
	default:
		return nil, "", fmt.Errorf("نوع التقرير غير معروف: %q", kind)
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), filename, nil
}
