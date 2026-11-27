// Package store — B2: shifts, drawer, cash count, returns, expenses,
// daily close. See FEATURE_MAP §3, §4, §8, §10, §11 and ARCHITECTURE.md.
//
// Money rules: all amounts are money.Cents (qirsh); quantities are
// money.Qty (milliunits). Negative balances are refused everywhere
// (jony's program-wide rule); the only allowed negative figure is the
// Difference of a closed shift, reported only (never deducted).
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

// ---------------------------------------------------------------- shifts

// Shift is one row of Shifts.
type Shift struct {
	ID               int64
	UserID           int64
	UserName         string
	ShiftDate        string
	OpenTime         string
	CloseTime        string
	Status           string
	OpeningBalance   money.Cents
	ExpectedBalance  money.Cents
	ActualBalance    money.Cents
	Difference       money.Cents
	DifferenceStatus string
	TotalSalesCash   money.Cents
	TotalSalesCredit money.Cents
	TotalReturns     money.Cents
	TotalExpenses    money.Cents
	// PR5-L3: the retained-cash bucket persisted on close
	// (RetainedCanceledCashTotal) is read back like its sibling
	// stored totals — it must not be a write-only column.
	RetainedCanceledCashTotal money.Cents
	Notes                     string
	IsOpen                    bool
	SessionCount              int
}

// ShiftSummary is the live financial summary of a shift (H-08/H-09).
type ShiftSummary struct {
	ShiftID                int64
	UserID                 int64
	UserName               string
	ShiftDate              string
	IsOpen                 bool
	OpeningBalance         money.Cents
	CashSales              money.Cents
	// PR4-L3: sale cash a canceled invoice leaves in the drawer
	// (non-cash return, or return in another shift) — retained cash,
	// NOT a sale: the canceled invoice is excluded from TotalSales /
	// InvoiceCount, so booking it under CashSales mislabels it
	// "مبيعات نقدية" on the shift pages and in the stored totals.
	// It still enters ExpectedBalance exactly once.
	RetainedCanceledCash   money.Cents
	VisaSales              money.Cents
	VodafoneSales          money.Cents
	TotalSales             money.Cents
	CashDebtCollection     money.Cents
	VisaDebtCollection     money.Cents
	VodafoneDebtCollection money.Cents
	// FIX-2 L1: cash handed over by a courier settling a debt — an
	// automatic 'in' row, not a manual CashIn (its own bucket, but it
	// still enters ExpectedBalance exactly once).
	CourierCollection money.Cents
	CashIn            money.Cents
	CashOut           money.Cents
	CashReturns       money.Cents
	ReturnsTotal      money.Cents
	ExpensesTotal     money.Cents
	VendorCashPaid    money.Cents
	ExpectedBalance   money.Cents
	InvoiceCount      int
	SessionCount      int
}

// ShiftStatus is the three-state hub display (H-07).
type ShiftStatus struct {
	State      string // "open" | "closed" | "none"
	HasOpen    bool
	ShiftID    int64
	UserName   string
	Date       string
	StatusText string
}

// DifferenceStatus maps a difference to متوازن/عجز/زيادة (H-12).
// Exact zero (cents are integers) is متوازن.
func DifferenceStatus(diff money.Cents) string {
	if diff == 0 {
		return "متوازن"
	}
	if diff < 0 {
		return "عجز"
	}
	return "زيادة"
}

// normMethod maps a stored payment method to Cash / Visa /
// Vodafone Cash using B1's NormalizePaymentMethod. Unknown methods
// return "" and are counted nowhere (never silently Cash).
func normMethod(v string) string {
	m, err := NormalizePaymentMethod(v)
	if err != nil {
		return ""
	}
	return m
}

// atSaleCashTx returns the cash an invoice brought into the drawer
// at sale time: Payments rows with Notes='دفعة عند البيع' and method
// Cash. Debt collections are deliberately NOT part of it — they enter
// the summary via CashDebtCollection, and a refund that repays them
// is corrected separately (PR3-H1 pass below), not folded into the
// return-leg cap.
func atSaleCashTx(tx *sql.Tx, invID int64) money.Cents {
	var atSaleCash money.Cents
	payRows, err := tx.Query(`SELECT Amount,PaymentMethod FROM Payments
		WHERE InvoiceID=? AND Notes='دفعة عند البيع'`, invID)
	if err == nil {
		for payRows.Next() {
			var amt int64
			var method string
			if err := payRows.Scan(&amt, &method); err != nil {
				break
			}
			if normMethod(method) == "Cash" {
				atSaleCash += money.Cents(amt)
			}
		}
		payRows.Close()
	}
	return atSaleCash
}

// refundTodayTx returns the cancel refund of an invoice dated the
// report day: structural RefInvoiceID rows dated `date` PLUS
// reason-resolved legacy rows (RefInvoiceID=0) for the same invoice.
// (PR4-L1: both count — the shift summary resolves per-row, so a
// structural row must not shadow a coexisting legacy row. PR4-L2: the
// date comes from the refund rows' own MovementDate, not from the
// invoice's CanceledAt, which may be NULL on legacy rows — the
// callers no longer gate on CanceledAt at all. PR5-L2: when a row's
// MovementDate does not parse, refundDay falls back to the invoice's
// CanceledAt / sale date instead of dropping the row.)
// legacy maps invoice IDs to their legacy refund for the day.
func refundTodayTx(tx *sql.Tx, invID int64, legacy map[int64]money.Cents, date string) money.Cents {
	// PR5-L2: the date comes from each refund row's own MovementDate
	// via refundDay (falling back to the invoice's CanceledAt / sale
	// date when the row's date does not parse) — a malformed/empty
	// MovementDate must not silently drop the row: the shift summary
	// counts it with no date filter, so DailyClose must attribute it
	// to the invoice's cancel day instead. The rows are filtered in
	// Go (few per invoice) because the fallback needs the invoice's
	// own dates, which SQL cannot express.
	canceledAt, invDate := invoiceCancelDates(tx, invID)
	var total money.Cents
	rows, err := tx.Query(`SELECT Amount,MovementDate FROM CashBox
		WHERE RefInvoiceID=? AND AutoKind='cancel_refund'`, invID)
	if err == nil {
		for rows.Next() {
			var amt int64
			var mov string
			if err := rows.Scan(&amt, &mov); err != nil {
				break
			}
			if refundDay(mov, canceledAt, invDate) == date {
				total += money.Cents(amt)
			}
		}
		rows.Close()
	}
	return total + legacy[invID]
}

// cancelRefundInvoiceShift returns the ShiftID of the invoice a cancel
// refund row refers to. It prefers the structural RefInvoiceID and falls
// back to the invoice number embedded in the legacy reason. ok=false
// when the invoice cannot be identified (the caller then treats the
// refund as same-shift, preserving the pre-FIX-2 behaviour).
func cancelRefundInvoiceShift(tx *sql.Tx, refInvoiceID int64, reason string) (int64, bool) {
	var shiftID int64
	if refInvoiceID > 0 {
		if err := tx.QueryRow(`SELECT ShiftID FROM Invoices WHERE InvoiceID=?`, refInvoiceID).Scan(&shiftID); err == nil {
			return shiftID, true
		}
	}
	var num int64
	if _, err := fmt.Sscanf(reason, "استرداد إلغاء فاتورة %d", &num); err != nil || num == 0 {
		return 0, false
	}
	if err := tx.QueryRow(`SELECT ShiftID FROM Invoices WHERE InvoiceNumber=?`, num).Scan(&shiftID); err != nil {
		return 0, false
	}
	return shiftID, true
}

// cancelRefundInvoiceID resolves the invoice a cancel-refund row refers
// to (structural RefInvoiceID first, legacy reason fallback). It is the
// ID form of cancelRefundInvoiceShift, used by the PR2-H1 cap to
// attribute a same-shift refund to its invoice.
func cancelRefundInvoiceID(tx *sql.Tx, refInvoiceID int64, reason string) (int64, bool) {
	if refInvoiceID > 0 {
		return refInvoiceID, true
	}
	var num int64
	if _, err := fmt.Sscanf(reason, "استرداد إلغاء فاتورة %d", &num); err != nil || num == 0 {
		return 0, false
	}
	var id int64
	if err := tx.QueryRow(`SELECT InvoiceID FROM Invoices WHERE InvoiceNumber=?`, num).Scan(&id); err != nil {
		return 0, false
	}
	return id, true
}

// parseDay returns the YYYY-MM-DD prefix of s when it looks like a
// date, else "". MovementDate / CanceledAt are stored as
// "2006-01-02 15:04:05" (store.go Now()); a hand-edited or corrupt
// value ("", "garbage") has no parseable day.
func parseDay(s string) string {
	if len(s) < 10 {
		return ""
	}
	d := s[:10]
	if d[4] != '-' || d[7] != '-' {
		return ""
	}
	for i, c := range d {
		if i == 4 || i == 7 {
			continue
		}
		if c < '0' || c > '9' {
			return ""
		}
	}
	return d
}

// refundDay returns the day a cancel-refund row belongs to: its own
// MovementDate when that parses as YYYY-MM-DD, else the invoice's
// CanceledAt date, else the invoice's sale date (InvoiceDate).
// PR5-L2: a row with a malformed/empty MovementDate must not be
// silently dropped — the shift summary counts it with no date
// filter, so DailyClose attributes it to the invoice's cancel day
// instead of dropping it (a row with valid CanceledAt but malformed
// MovementDate counted before fix-4 and must count again).
func refundDay(movementDate, canceledAt, invoiceDate string) string {
	if d := parseDay(movementDate); d != "" {
		return d
	}
	if d := parseDay(canceledAt); d != "" {
		return d
	}
	return parseDay(invoiceDate)
}

// cashBoxShiftDay returns the ShiftDate of the shift a CashBox row
// belongs to, "" when the shift is unknown. A CashBox row's cash
// moved during that shift, so its date is the shift's date.
func cashBoxShiftDay(tx *sql.Tx, shiftID int64) string {
	if shiftID <= 0 {
		return ""
	}
	var d string
	_ = tx.QueryRow(`SELECT ShiftDate FROM Shifts WHERE ShiftID=?`, shiftID).Scan(&d)
	return parseDay(d)
}

// returnLegDay returns the day a CashBox return leg belongs to: its
// own MovementDate when that parses, else its OWN Returns row's
// ReturnDate via the ReturnID stamped on the leg (PR9-L1: the leg
// carries its row now, so the leg's own day is known directly — the
// sibling evidence can no longer confuse it with another return's
// day), else the event's day through ANY of its rows via
// ReturnEventID (PR10-L1), else the single distinct
// Returns.ReturnDate day among the leg's own shift's rows for its
// invoice (PR8-L1: the sibling evidence must come from the leg's
// own return event — the writer stamps the identical ShiftID on the
// Returns row and the CashBox leg of every return event — never from
// any other shift's return of the same invoice; and only when the
// same-shift set is complete: exactly one distinct parseable day
// and no unparseable row), else the shift the cash left in, else
// the invoice's sale date.
// (PR6-L1: a malformed/empty MovementDate must not drop the leg —
// the shift summary counts it with no date filter, so DailyClose
// must attribute it to a day instead of dropping it.) The sibling
// step and the guard below are the fallback for legacy legs whose
// ReturnID is 0 (pre-PR9-L1 rows) and for linked legs whose own row
// and event are missing or unparseable; they must NOT be removed
// (shape D).
func returnLegDay(tx *sql.Tx, mov string, refInvID, shiftID, returnID int64) string {
	if d := parseDay(mov); d != "" {
		return d
	}
	// PR9-L1: the leg's own row, via ReturnID. Exact — no sibling
	// inference involved, so the hunt shape (leg of return 1 mangled
	// while return 2's row is the mangled one) and shape D (the
	// garbage IS this leg's own row) can no longer be confused: the
	// leg reads its own row's day, whatever the siblings say.
	if returnID > 0 {
		var rd string
		_ = tx.QueryRow(`SELECT COALESCE(ReturnDate,'') FROM Returns WHERE ReturnID=?`, returnID).Scan(&rd)
		if d := parseDay(rd); d != "" {
			return d
		}
	}
	// PR10-L1: the event's day, through ANY of its rows. The leg
	// covers the whole return event, and every row of the event
	// shares ReturnDate (same `now` at write time), so when the
	// leg's own row (row [0]) is unparseable, a single distinct
	// parseable day among the event's rows IS the event's day —
	// unparseable rows do not invalidate it, because they are the
	// very rows whose day is being proven. Two distinct parseable
	// days mean the event was hand-edited inconsistently, so the
	// step yields nothing and the fallbacks below decide. This is
	// what fixes the multi-line cases: (a) leg + row [0] mangled
	// while row [1] proves today, and — via returnsRowDay — (b)
	// row [1] mangled while row [0] proves today. Legacy legs
	// (ReturnID 0) and pre-fix-10 rows (ReturnEventID 0) skip
	// this step: their event has no id to query by.
	if returnID > 0 {
		rows, err := tx.Query(`SELECT DISTINCT substr(COALESCE(ReturnDate,''),1,10) FROM Returns WHERE ReturnEventID=?`, returnID)
		if err == nil {
			day := ""
			ambiguous := false
			for rows.Next() {
				var rd string
				if err := rows.Scan(&rd); err != nil {
					break
				}
				if d := parseDay(rd); d != "" {
					if day == "" {
						day = d
					} else if day != d {
						ambiguous = true
					}
				}
			}
			rows.Close()
			if day != "" && !ambiguous {
				return day
			}
		}
	}
	if refInvID > 0 {
		rows, err := tx.Query(`SELECT DISTINCT substr(COALESCE(ReturnDate,''),1,10) FROM Returns WHERE InvoiceID=? AND ShiftID=?`, refInvID, shiftID)
		if err == nil {
			day := ""
			ambiguous := false
			incomplete := false
			for rows.Next() {
				var rd string
				if err := rows.Scan(&rd); err != nil {
					break
				}
				if d := parseDay(rd); d != "" {
					if day == "" {
						day = d
					} else if day != d {
						ambiguous = true
					}
				} else {
					// An unparseable sibling row: it might be this
					// leg's own row on a different day, so a lone
					// parseable day from this set proves nothing
					// about this leg (shape D: same-shift siblings
					// {garbage, yesterday} — the garbage IS today's
					// own row, yesterday is the other return).
					incomplete = true
				}
			}
			rows.Close()
			if day != "" && !ambiguous && !incomplete {
				return day
			}
		}
	}
	if d := cashBoxShiftDay(tx, shiftID); d != "" {
		return d
	}
	_, invDate := invoiceCancelDates(tx, refInvID)
	return parseDay(invDate)
}

// courierDay returns the day a courier-collection CashBox row belongs
// to: its own MovementDate when that parses, else the shift the cash
// entered in. (PR6-L1: same class as return legs — the shift summary
// counts the row with no date filter. CourierDebts.PaidAt is not a
// fallback: it is only stamped on full settlement, so a partial
// collection has no PaidAt to read.)
func courierDay(tx *sql.Tx, mov string, shiftID int64) string {
	if d := parseDay(mov); d != "" {
		return d
	}
	return cashBoxShiftDay(tx, shiftID)
}

// returnsRowDay returns the day a Returns row belongs to: its own
// ReturnDate when that parses, else its OWN CashBox leg's
// MovementDate via the ReturnID stamped on the leg (PR9-L1: the leg
// and the row are written with the same `now`, so the leg's date IS
// this row's true date when the row's own date is corrupt — this is
// what makes the hunt shape agree on TotalReturns/TotalProfit too,
// not just on NetCash), else the event's day through any of its rows
// (PR10-L1), else the ShiftDate of the shift the return was made
// in, else the invoice's sale date. (PR7-L1: a malformed/empty
// ReturnDate must not drop the row from DailyClose's TotalReturns
// or the profit adjustment — fix-6 counts the same return's
// CashBox leg in CashReturns, so the two halves must agree on the
// day. The shift fallback is exact for live rows: the writer stamps
// ReturnDate=Now() during the shift it records, so a row whose date
// is corrupt still belongs to its recorded shift. No
// sibling-Returns-day step: unlike returnLegDay's legs, a Returns
// row's siblings cannot prove its own day — the malformed row itself
// might be the one on a different day — so the row's own shift is
// the only sound fallback before the sale date. The leg lookup uses
// the row's EVENT id (eventID): the leg stamps the event's first
// row's ID, and every row of the event carries it as ReturnEventID,
// so row [1] of a multi-line event finds its leg too — the old
// lookup by the row's own ReturnID only ever named row [0].
// Legacy rows (eventID 0, written before fix-10) fall back to the
// row's own ReturnID, which names the leg for single-row legacy
// events exactly as before.)
func returnsRowDay(tx *sql.Tx, invID, shiftID, returnID, eventID int64, returnDate string) string {
	if d := parseDay(returnDate); d != "" {
		return d
	}
	// PR9-L1/PR10-L1: the row's own leg, via the event id
	// (indexed: idx_cashbox_returnid). The writer stamps the leg's
	// MovementDate with the same `now` as this row's ReturnDate.
	legID := eventID
	if legID == 0 {
		legID = returnID
	}
	if legID > 0 {
		var mov string
		_ = tx.QueryRow(`SELECT COALESCE(MovementDate,'') FROM CashBox
			WHERE ReturnID=? AND AutoKind='return'`, legID).Scan(&mov)
		if d := parseDay(mov); d != "" {
			return d
		}
	}
	// PR10-L1: the event's day, through any of its rows — the
	// mirror of returnLegDay's event step. When the leg itself is
	// mangled, a single distinct parseable day among the event's
	// rows IS the event's day (all rows share it at write time).
	// Legacy rows (eventID 0) skip this step.
	if eventID > 0 {
		rows, err := tx.Query(`SELECT DISTINCT substr(COALESCE(ReturnDate,''),1,10) FROM Returns WHERE ReturnEventID=?`, eventID)
		if err == nil {
			day := ""
			ambiguous := false
			for rows.Next() {
				var rd string
				if err := rows.Scan(&rd); err != nil {
					break
				}
				if d := parseDay(rd); d != "" {
					if day == "" {
						day = d
					} else if day != d {
						ambiguous = true
					}
				}
			}
			rows.Close()
			if day != "" && !ambiguous {
				return day
			}
		}
	}
	if d := cashBoxShiftDay(tx, shiftID); d != "" {
		return d
	}
	_, invDate := invoiceCancelDates(tx, invID)
	return parseDay(invDate)
}

// invoiceCancelDates returns an invoice's CanceledAt and sale-date
// prefix ("", "" when the invoice does not exist).
func invoiceCancelDates(tx *sql.Tx, invID int64) (canceledAt, invDate string) {
	_ = tx.QueryRow(`SELECT COALESCE(CanceledAt,''), substr(InvoiceDate,1,10)
		FROM Invoices WHERE InvoiceID=?`, invID).Scan(&canceledAt, &invDate)
	return canceledAt, invDate
}

// canceledCountTx counts invoices canceled on `date` (D18, PR5-L1).
// An invoice's cancel day is its CanceledAt date when that parses,
// else the effective day of its cancel-refund rows (PR5-L2's
// refundDay — earliest row, so each invoice counts on at most one
// day), else its sale date. A NULL CanceledAt must not hide a
// canceled invoice from the count while its money is reported — the
// count and the money path now derive the day from the same sources.
func canceledCountTx(tx *sql.Tx, date string) (int, error) {
	invRows, err := tx.Query(`SELECT InvoiceID, COALESCE(CanceledAt,''), substr(InvoiceDate,1,10)
		FROM Invoices WHERE IsCanceled=1`)
	if err != nil {
		return 0, err
	}
	type invDates struct {
		canceledAt string
		invDate    string
	}
	invs := map[int64]invDates{}
	var ids []int64
	for invRows.Next() {
		var id int64
		var d invDates
		if err := invRows.Scan(&id, &d.canceledAt, &d.invDate); err != nil {
			invRows.Close()
			return 0, err
		}
		invs[id] = d
		ids = append(ids, id)
	}
	invRows.Close()
	// Effective refund day per invoice (structural + legacy rows).
	refundDayByInv := map[int64]string{}
	consider := func(invID int64, mov string) {
		d, ok := invs[invID]
		if !ok {
			return
		}
		day := refundDay(mov, d.canceledAt, d.invDate)
		if day == "" {
			return
		}
		if cur, ok := refundDayByInv[invID]; !ok || day < cur {
			refundDayByInv[invID] = day
		}
	}
	if rows, err := tx.Query(`SELECT RefInvoiceID,MovementDate FROM CashBox
		WHERE AutoKind='cancel_refund' AND RefInvoiceID>0`); err == nil {
		for rows.Next() {
			var id int64
			var mov string
			if err := rows.Scan(&id, &mov); err != nil {
				break
			}
			consider(id, mov)
		}
		rows.Close()
	}
	if rows, err := tx.Query(`SELECT Reason,MovementDate FROM CashBox
		WHERE AutoKind='cancel_refund' AND RefInvoiceID=0`); err == nil {
		type legRow struct {
			reason string
			mov    string
		}
		var legList []legRow
		for rows.Next() {
			var lr legRow
			if err := rows.Scan(&lr.reason, &lr.mov); err != nil {
				break
			}
			legList = append(legList, lr)
		}
		rows.Close()
		for _, lr := range legList {
			if id, ok := cancelRefundInvoiceID(tx, 0, lr.reason); ok {
				consider(id, lr.mov)
			}
		}
	}
	count := 0
	for _, id := range ids {
		d := invs[id]
		day := parseDay(d.canceledAt)
		if day == "" {
			day = refundDayByInv[id]
		}
		if day == "" {
			day = parseDay(d.invDate)
		}
		if day == date {
			count++
		}
	}
	return count, nil
}

func scanShift(row *sql.Row) (*Shift, error) {
	var sh Shift
	var exp, act, diff sql.NullInt64
	var closeTime, notes, diffStatus sql.NullString
	var isOpen int
	err := row.Scan(&sh.ID, &sh.UserID, &sh.UserName, &sh.ShiftDate, &sh.OpenTime,
		&closeTime, &sh.Status, &sh.OpeningBalance, &exp, &act, &diff,
		&diffStatus, &sh.TotalSalesCash, &sh.TotalSalesCredit,
		&sh.TotalReturns, &sh.TotalExpenses, &sh.RetainedCanceledCashTotal, &notes, &isOpen, &sh.SessionCount)
	if err != nil {
		return nil, err
	}
	sh.CloseTime = closeTime.String
	sh.Notes = notes.String
	sh.DifferenceStatus = diffStatus.String
	sh.ExpectedBalance = money.Cents(exp.Int64)
	sh.ActualBalance = money.Cents(act.Int64)
	sh.Difference = money.Cents(diff.Int64)
	sh.IsOpen = isOpen == 1
	return &sh, nil
}

const shiftCols = `ShiftID,UserID,UserName,ShiftDate,OpenTime,CloseTime,Status,
	OpeningBalance,ExpectedBalance,ActualBalance,Difference,DifferenceStatus,
	TotalSalesCash,TotalSalesCredit,TotalReturns,TotalExpenses,RetainedCanceledCashTotal,Notes,IsOpen,SessionCount`

func getShiftTx(tx *sql.Tx, shiftID int64) (*Shift, error) {
	row := tx.QueryRow(`SELECT `+shiftCols+` FROM Shifts WHERE ShiftID=?`, shiftID)
	sh, err := scanShift(row)
	if err == sql.ErrNoRows {
		return nil, errors.New("الشيفت غير موجود")
	}
	return sh, err
}

// GetShift returns a shift by id (read-only).
func (s *Store) GetShift(ctx context.Context, shiftID int64) (*Shift, error) {
	var sh *Shift
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		sh, err = getShiftTx(tx, shiftID)
		return err
	})
	return sh, err
}

// shiftSummaryAdd adds v to *dst with checked arithmetic (R23-E-F2):
// a shift-summary total must refuse with the Arabic overflow message
// instead of silently wrapping — the downstream ExpectedBalance
// AddChecked chain cannot catch a wrap that already happened here.
func shiftSummaryAdd(dst *money.Cents, v money.Cents) error {
	nv, err := money.AddChecked(*dst, v)
	if err != nil {
		return err
	}
	*dst = nv
	return nil
}

// shiftSummaryTx computes the live H-09 summary inside a transaction.
// Cash sales come from at-sale Payments rows (Notes='دفعة عند البيع');
// an invoice with no such rows falls back to its PaidAmount under its
// own PaymentMethod, so directly-inserted invoices still reconcile.
// Automatic CashBox rows are classified by their structural AutoKind
// column (FIX-2 L1), never by matching the free-text reason, and are
// excluded from manual CashIn/CashOut so nothing is double-counted;
// cash returns are the CashBox 'out' rows the returns themselves wrote.
func shiftSummaryTx(tx *sql.Tx, shiftID int64) (*ShiftSummary, error) {
	sh, err := getShiftTx(tx, shiftID)
	if err != nil {
		return nil, err
	}
	sum := &ShiftSummary{
		ShiftID: shiftID, UserID: sh.UserID, UserName: sh.UserName,
		ShiftDate: sh.ShiftDate, IsOpen: sh.IsOpen,
		OpeningBalance: sh.OpeningBalance,
	}
	// Invoices of this shift (non-canceled).
	invRows, err := tx.Query(`SELECT InvoiceID,NetAmount,PaidAmount,PaymentMethod
		FROM Invoices WHERE ShiftID=? AND IsCanceled=0`, shiftID)
	if err != nil {
		return nil, err
	}
	var invIDs []int64
	invMethod := map[int64]string{}
	invPaid := map[int64]money.Cents{}
	for invRows.Next() {
		var id int64
		var net, paid int64
		var method string
		if err := invRows.Scan(&id, &net, &paid, &method); err != nil {
			invRows.Close()
			return nil, err
		}
		invIDs = append(invIDs, id)
		invMethod[id] = normMethod(method)
		invPaid[id] = money.Cents(paid)
		if err := shiftSummaryAdd(&sum.TotalSales, money.Cents(net)); err != nil {
			invRows.Close()
			return nil, err
		}
	}
	invRows.Close()
	sum.InvoiceCount = len(invIDs)
	// At-sale payments by method.
	seenAtSale := map[int64]bool{}
	if len(invIDs) > 0 {
		ph := strings.Repeat("?,", len(invIDs))
		ph = ph[:len(ph)-1]
		args := make([]any, len(invIDs))
		for i, id := range invIDs {
			args[i] = id
		}
		payRows, err := tx.Query(`SELECT InvoiceID,Amount,PaymentMethod FROM Payments
			WHERE InvoiceID IN (`+ph+`) AND Notes='دفعة عند البيع'`, args...)
		if err != nil {
			return nil, err
		}
		for payRows.Next() {
			var invID int64
			var amt int64
			var method string
			if err := payRows.Scan(&invID, &amt, &method); err != nil {
				payRows.Close()
				return nil, err
			}
			seenAtSale[invID] = true
			switch normMethod(method) {
			case "Cash":
				if err := shiftSummaryAdd(&sum.CashSales, money.Cents(amt)); err != nil {
					payRows.Close()
					return nil, err
				}
			case "Visa":
				if err := shiftSummaryAdd(&sum.VisaSales, money.Cents(amt)); err != nil {
					payRows.Close()
					return nil, err
				}
			case "Vodafone Cash":
				if err := shiftSummaryAdd(&sum.VodafoneSales, money.Cents(amt)); err != nil {
					payRows.Close()
					return nil, err
				}
			}
		}
		payRows.Close()
	}
	// Fallback: invoices with no Payments rows at all count their
	// PaidAmount under their own method. (FIX-1 H1: an invoice that
	// only has later debt-collection Payments rows must NOT fall
	// back — its PaidAmount already includes those collections, and
	// counting it as CashSales would double-count them.)
	// FIX-20 R20-A-F1: a return's negative Payments row is NOT a
	// payment — it must not kill the fallback, or the PaidAmount
	// is lost from CashSales while the return's CashBox out leg
	// still subtracts the same money (double subtraction).
	hasAnyPayment := map[int64]bool{}
	if len(invIDs) > 0 {
		ph := strings.Repeat("?,", len(invIDs))
		ph = ph[:len(ph)-1]
		args := make([]any, len(invIDs))
		for i, id := range invIDs {
			args[i] = id
		}
		anyRows, err := tx.Query(`SELECT DISTINCT InvoiceID FROM Payments
			WHERE InvoiceID IN (`+ph+`) AND Amount >= 0`, args...)
		if err != nil {
			return nil, err
		}
		for anyRows.Next() {
			var invID int64
			if err := anyRows.Scan(&invID); err != nil {
				anyRows.Close()
				return nil, err
			}
			hasAnyPayment[invID] = true
		}
		anyRows.Close()
	}
	for _, id := range invIDs {
		if seenAtSale[id] || hasAnyPayment[id] {
			continue
		}
		switch invMethod[id] {
		case "Cash":
			if err := shiftSummaryAdd(&sum.CashSales, invPaid[id]); err != nil {
				return nil, err
			}
		case "Visa":
			if err := shiftSummaryAdd(&sum.VisaSales, invPaid[id]); err != nil {
				return nil, err
			}
		case "Vodafone Cash":
			if err := shiftSummaryAdd(&sum.VodafoneSales, invPaid[id]); err != nil {
				return nil, err
			}
		}
	}
	// Debt collections (CustomerPayments stamped with this shift),
	// split by payment method (FIX-1 M2: the column exists now; a
	// NULL/empty method counts as Cash, per PyQt R23-P-2).
	collRows, err := tx.Query(`SELECT Amount,COALESCE(PaymentMethod,'Cash') FROM CustomerPayments WHERE ShiftID=?`, shiftID)
	if err != nil {
		return nil, err
	}
	for collRows.Next() {
		var amt int64
		var method string
		if err := collRows.Scan(&amt, &method); err != nil {
			collRows.Close()
			return nil, err
		}
		switch normMethod(method) {
		case "Visa":
			if err := shiftSummaryAdd(&sum.VisaDebtCollection, money.Cents(amt)); err != nil {
				collRows.Close()
				return nil, err
			}
		case "Vodafone Cash":
			if err := shiftSummaryAdd(&sum.VodafoneDebtCollection, money.Cents(amt)); err != nil {
				collRows.Close()
				return nil, err
			}
		default:
			if err := shiftSummaryAdd(&sum.CashDebtCollection, money.Cents(amt)); err != nil {
				collRows.Close()
				return nil, err
			}
		}
	}
	collRows.Close()
	// Returns total (processed returns of this shift).
	var retTotal sql.NullInt64
	if err := tx.QueryRow(`SELECT COALESCE(SUM(TotalRefund),0) FROM Returns
		WHERE ShiftID=? AND IsProcessed=1`, shiftID).Scan(&retTotal); err != nil {
		return nil, sumScanErr(err)
	}
	sum.ReturnsTotal = money.Cents(retTotal.Int64)
	// Expenses of this shift.
	var expTotal sql.NullInt64
	if err := tx.QueryRow(`SELECT COALESCE(SUM(Amount),0) FROM Expenses WHERE ShiftID=?`, shiftID).Scan(&expTotal); err != nil {
		return nil, sumScanErr(err)
	}
	sum.ExpensesTotal = money.Cents(expTotal.Int64)
	// CashBox movements, classified by structural AutoKind (FIX-2 L1).
	// Cancel refunds are resolved after the rows close (no query inside
	// the rows loop — MaxOpenConns(1)).
	cbRows, err := tx.Query(`SELECT MovementType,Amount,Reason,AutoKind,RefInvoiceID FROM CashBox WHERE ShiftID=?`, shiftID)
	if err != nil {
		return nil, err
	}
	var cancelRefunds []struct {
		amt      money.Cents
		refInvID int64
		reason   string
	}
	// PR2-H1: return legs are resolved after the rows close (same
	// reason as cancel refunds — no query inside the rows loop).
	var returnLegs []struct {
		amt      money.Cents
		refInvID int64
	}
	for cbRows.Next() {
		var mtype, reason, kind string
		var amt, refInvID int64
		if err := cbRows.Scan(&mtype, &amt, &reason, &kind, &refInvID); err != nil {
			cbRows.Close()
			return nil, err
		}
		switch mtype {
		case "open", "resume", "close":
			// Opening / resume / close rows never enter CashIn/CashOut.
		case "in":
			switch kind {
			case "sale", "customer_collection":
				// Automatic: counted once via CashSales /
				// CashDebtCollection, never as manual CashIn.
			case "courier_collection":
				// FIX-2 L1: courier cash is its own bucket (not
				// manual CashIn) and enters ExpectedBalance below.
				// R23-D-F1: checked accumulation — the CashBox-loop
				// terms feed the checked ExpectedBalance chain at
				// :995-1020, which cannot detect a wrap that already
				// happened here, so each term must refuse on
				// overflow instead of wrapping (legacy-poisoned
				// rows included: the cap in cashBoxMoveTx only
				// stops NEW absurd rows).
				var err error
				if sum.CourierCollection, err = money.AddChecked(sum.CourierCollection, money.Cents(amt)); err != nil {
					cbRows.Close()
					return nil, err
				}
			default:
				var err error
				if sum.CashIn, err = money.AddChecked(sum.CashIn, money.Cents(amt)); err != nil {
					cbRows.Close()
					return nil, err
				}
			}
		case "out":
			switch kind {
			case "return":
				returnLegs = append(returnLegs, struct {
					amt      money.Cents
					refInvID int64
				}{money.Cents(amt), refInvID})
			case "cancel_refund":
				cancelRefunds = append(cancelRefunds, struct {
					amt      money.Cents
					refInvID int64
					reason   string
				}{money.Cents(amt), refInvID, reason})
			default:
				// R23-D-F1: checked accumulation (see 'in' above).
				var err error
				if sum.CashOut, err = money.AddChecked(sum.CashOut, money.Cents(amt)); err != nil {
					cbRows.Close()
					return nil, err
				}
				// FIX-12 SW-M06: PayVendor writes "سداد مورد نقدي — <name>"
				// (and purchase-time cash "سداد مورد نقدي عند الشراء …"),
				// so match by prefix — exact equality never matched and
				// the sub-bucket was always 0. CashOut (and therefore
				// ExpectedBalance) is unchanged: the row already counted.
				if strings.HasPrefix(reason, "سداد مورد نقدي") {
					if sum.VendorCashPaid, err = money.AddChecked(sum.VendorCashPaid, money.Cents(amt)); err != nil {
						cbRows.Close()
						return nil, err
					}
				}
			}
		}
	}
	cbRows.Close()
	// FIX-2 H-1: a cancel refund of a SAME-shift sale is excluded — the
	// canceled sale is already excluded from CashSales, so subtracting
	// the refund again would double-count it. A CROSS-shift refund (the
	// invoice's shift is closed) is real cash out of THIS shift's
	// drawer, so it enters CashOut; without it DrawerExpectedBalance
	// and the summary disagree by the refund, and the close records a
	// wrong Actual. Same-shift refunds are also recorded per invoice
	// for the PR2-H1 return-leg cap below.
	sameShiftRefund := map[int64]money.Cents{}
	for _, cr := range cancelRefunds {
		if invShift, ok := cancelRefundInvoiceShift(tx, cr.refInvID, cr.reason); ok && invShift != shiftID {
			// R23-D-F1: checked accumulation (see CashBox loop above).
			var err error
			if sum.CashOut, err = money.AddChecked(sum.CashOut, cr.amt); err != nil {
				return nil, err
			}
		} else if ok {
			if invID, ok2 := cancelRefundInvoiceID(tx, cr.refInvID, cr.reason); ok2 {
				// R23-D-F1: the per-invoice refund total feeds the
				// PR2-H1 cap below; a wrapped total would corrupt
				// the cap, so it is accumulated checked too.
				var err error
				if sameShiftRefund[invID], err = money.AddChecked(sameShiftRefund[invID], cr.amt); err != nil {
					return nil, err
				}
			}
			// An unidentifiable refund stays out of CashOut (the
			// pre-FIX-2 behaviour) and contributes nothing to the cap.
		}
	}
	// PR2-H1: a return leg of an invoice canceled in THIS shift is
	// excluded from CashReturns — but only up to the cash that invoice
	// actually brought into this drawer and lost to the cancel. The
	// correction the summary needs for such an invoice is (at-sale cash
	// − same-shift cancel refund): the canceled sale is already
	// excluded from CashSales and the refund is excluded from CashOut
	// (FIX-2 H-1), so the return leg must be excluded exactly to that
	// extent (sell → return → cancel in one shift: Expected 8000 vs
	// true drawer 10000). A blanket exclusion would be wrong: a Visa
	// sale (at-sale cash 0) whose return is paid in cash keeps its
	// return legs counted — that cash really did leave the drawer.
	// (PR3-H1 correction of the old comment: debt collections do NOT
	// "cancel out of the correction" — a refund funded by collections
	// exceeds the at-sale cash, and the excess is corrected below
	// via CashOut; see the PR3-H1 pass after this loop.) A return leg
	// of an invoice from ANOTHER shift stays counted too: its sale
	// never entered this shift's CashSales. A leg with no invoice
	// reference is counted (safe default — the pre-fix behaviour).
	legsByInvoice := map[int64][]money.Cents{}
	for _, rl := range returnLegs {
		if rl.refInvID > 0 {
			legsByInvoice[rl.refInvID] = append(legsByInvoice[rl.refInvID], rl.amt)
		} else {
			// R23-D-F1: checked accumulation (see CashBox loop above).
			var err error
			if sum.CashReturns, err = money.AddChecked(sum.CashReturns, rl.amt); err != nil {
				return nil, err
			}
		}
	}
	for invID, legs := range legsByInvoice {
		var canceled int
		var invShift int64
		if err := tx.QueryRow(`SELECT IsCanceled,COALESCE(ShiftID,0) FROM Invoices WHERE InvoiceID=?`,
			invID).Scan(&canceled, &invShift); err != nil || canceled == 0 || invShift != shiftID {
			for _, amt := range legs {
				// R23-D-F1: checked accumulation (see CashBox loop above).
				var err error
				if sum.CashReturns, err = money.AddChecked(sum.CashReturns, amt); err != nil {
					return nil, err
				}
			}
			continue
		}
		// At-sale cash of this invoice — the cash that entered this
		// drawer at sale time (see atSaleCashTx).
		atSaleCash := atSaleCashTx(tx, invID)
		// R23-D-F1: checked subtraction — atSaleCash and the refund
		// are both CashBox-derived, so a poisoned pair could wrap
		// the plain subtraction; SubChecked refuses instead.
		cap, cerr := money.SubChecked(atSaleCash, sameShiftRefund[invID])
		if cerr != nil {
			return nil, cerr
		}
		if cap < 0 {
			cap = 0
		}
		for _, amt := range legs {
			if cap >= amt {
				cap -= amt // excluded: reverses excluded sale cash
			} else {
				// amt > cap >= 0, so amt-cap is in [1, amt] and
				// cannot overflow; the checked add below is the
				// guard that matters.
				var err error
				if sum.CashReturns, err = money.AddChecked(sum.CashReturns, amt-cap); err != nil {
					return nil, err
				}
				cap = 0
			}
		}
	}
	// PR3-H1: the cap above only fixes the return-leg side. A
	// same-shift cancel refund can exceed the at-sale cash when it
	// repays debt collections (the refund sums ALL Payments rows,
	// collections included), and then the summary overstates the
	// drawer by (refund − at-sale cash): the collection is counted
	// in full via CashDebtCollection while the refund that repaid
	// part of it is excluded from CashOut. Conversely a non-cash
	// return leaves sale cash in the drawer that the summary
	// excludes with the canceled sale — the retained part must be
	// added back, or the summary understates (and the same add-back
	// also repairs the return-in-another-shift shape: the sale cash
	// retained in this drawer is (at-sale cash − refund − legs)).
	// Both corrections are per canceled same-shift invoice, from
	// (at-sale cash, same-shift refund, cash return legs in this
	// shift):
	//   excess  = max(0, refund − atSaleCash) → CashOut
	//   addBack = max(0, (atSaleCash − refund) − legs) → RetainedCanceledCash
	// (PR4-L3: the add-back is retained sale cash, not a sale — it no
	// longer rides on CashSales.) They are mutually exclusive, and
	// addBack fires only when every leg was excluded. Invoices with
	// no return legs in this shift (return in another shift, or a
	// non-cash return) are covered too — the loop above never visits
	// them.
	canceledRows, err := tx.Query(`SELECT InvoiceID FROM Invoices WHERE ShiftID=? AND IsCanceled=1`, shiftID)
	if err == nil {
		var canceledIDs []int64
		for canceledRows.Next() {
			var id int64
			if err := canceledRows.Scan(&id); err != nil {
				break
			}
			canceledIDs = append(canceledIDs, id)
		}
		canceledRows.Close()
		for _, invID := range canceledIDs {
			atSaleCash := atSaleCashTx(tx, invID)
			refund := sameShiftRefund[invID]
			var legsTotal money.Cents
			for _, amt := range legsByInvoice[invID] {
				// R23-D-F1: checked accumulation — legsTotal feeds
				// the addBack subtraction below.
				var err error
				if legsTotal, err = money.AddChecked(legsTotal, amt); err != nil {
					return nil, err
				}
			}
			// R23-D-F1: checked subtractions — refund, atSaleCash
			// and legsTotal are all CashBox-derived; a poisoned
			// combination must refuse, not wrap the correction.
			excess, cerr := money.SubChecked(refund, atSaleCash)
			if cerr != nil {
				return nil, cerr
			}
			if excess > 0 {
				var err error
				if sum.CashOut, err = money.AddChecked(sum.CashOut, excess); err != nil {
					return nil, err
				}
			}
			addBack, cerr := money.SubChecked(atSaleCash, refund)
			if cerr != nil {
				return nil, cerr
			}
			if addBack, cerr = money.SubChecked(addBack, legsTotal); cerr != nil {
				return nil, cerr
			}
			if addBack > 0 {
				var err error
				if sum.RetainedCanceledCash, err = money.AddChecked(sum.RetainedCanceledCash, addBack); err != nil {
					return nil, err
				}
			}
		}
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM ShiftSessions WHERE ShiftID=?`, shiftID).Scan(&sum.SessionCount); err != nil {
		return nil, err
	}
	// H-09. RetainedCanceledCash enters exactly once: it is drawer
	// cash the canceled sale left behind, not a sale (PR4-L3).
	// R22-E-F2: checked addition — a stored (pre-cap) absurd opening
	// must refuse with the Arabic overflow message, never wrap to a
	// negative balance that closeShiftTx would persist as متوازن.
	expected, err := money.AddChecked(sum.OpeningBalance, sum.CashSales)
	if err != nil {
		return nil, err
	}
	if expected, err = money.AddChecked(expected, sum.RetainedCanceledCash); err != nil {
		return nil, err
	}
	if expected, err = money.AddChecked(expected, sum.CashDebtCollection); err != nil {
		return nil, err
	}
	if expected, err = money.AddChecked(expected, sum.CourierCollection); err != nil {
		return nil, err
	}
	if expected, err = money.AddChecked(expected, sum.CashIn); err != nil {
		return nil, err
	}
	if expected, err = money.SubChecked(expected, sum.CashReturns); err != nil {
		return nil, err
	}
	if expected, err = money.SubChecked(expected, sum.CashOut); err != nil {
		return nil, err
	}
	if expected, err = money.SubChecked(expected, sum.ExpensesTotal); err != nil {
		return nil, err
	}
	sum.ExpectedBalance = expected
	return sum, nil
}

// ShiftSummary returns the live financial summary of a shift.
func (s *Store) ShiftSummary(ctx context.Context, shiftID int64) (*ShiftSummary, error) {
	var sum *ShiftSummary
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		sum, err = shiftSummaryTx(tx, shiftID)
		return err
	})
	return sum, err
}

// closeShiftTx closes one open shift inside tx. actual is always the
// system-computed expected balance (H-10: the cashier never types an
// actual balance), so Difference is 0 and status is متوازن. When auto
// is true this is the day-rollover close (F32-4): no permission check,
// audit attributed to the shift owner, Notes flagged, and NO CashBox
// 'close' row is posted.
func closeShiftTx(tx *sql.Tx, actor *auth.User, shiftID int64, auto bool, notes string) (*ShiftSummary, error) {
	sh, err := getShiftTx(tx, shiftID)
	if err != nil {
		return nil, err
	}
	if !sh.IsOpen {
		return nil, errors.New("الشيفت المحدد غير مفتوح")
	}
	sum, err := shiftSummaryTx(tx, shiftID)
	if err != nil {
		return nil, err
	}
	expected := sum.ExpectedBalance
	actual := expected
	diff := money.Cents(0)
	status := DifferenceStatus(diff)
	now := Now()
	rolloverFlag := ""
	if auto {
		rolloverFlag = fmt.Sprintf("إغلاق تلقائي — يوم جديد — شيفت رقم %d بتاريخ %s", shiftID, sh.ShiftDate)
		if notes == "" {
			notes = rolloverFlag
		}
	}
	if _, err := tx.Exec(`UPDATE ShiftSessions SET IsOpen=0, CloseTime=? WHERE ShiftID=? AND IsOpen=1`, now, shiftID); err != nil {
		return nil, err
	}
	_, err = tx.Exec(`UPDATE Shifts SET IsOpen=0, Status='Closed', CloseTime=?, CloseDateTime=?,
		ExpectedBalance=?, ActualBalance=?, Difference=?, DifferenceAmount=?, DifferenceStatus=?,
		TotalSalesCash=?, TotalSalesCredit=?, TotalReturns=?, TotalExpenses=?,
		CashSalesTotal=?, VisaSalesTotal=?, VodafoneCashSalesTotal=?,
		CashDebtCollectionTotal=?, VisaDebtCollectionTotal=?, VodafoneDebtCollectionTotal=?,
		CashInTotal=?, CashOutTotal=?, ExpenseTotal=?, CashReturnTotal=?,
		RetainedCanceledCashTotal=?,
		ExpectedDrawerBalance=?, ActualDrawerBalance=?, Notes=? WHERE ShiftID=?`,
		now, now, int64(expected), int64(actual), int64(diff), int64(diff), status,
		int64(sum.CashSales),
		// FIX-2 L2: TotalSalesCredit is credit SALES only (Visa +
		// Vodafone Cash sales). Debt collections of any method are not
		// sales and are stored separately (Visa/VodafoneDebtCollection
		// totals); FIX-1 wrongly added them here.
		int64(sum.VisaSales+sum.VodafoneSales),
		int64(sum.ReturnsTotal), int64(sum.ExpensesTotal),
		int64(sum.CashSales), int64(sum.VisaSales), int64(sum.VodafoneSales),
		int64(sum.CashDebtCollection), int64(sum.VisaDebtCollection), int64(sum.VodafoneDebtCollection),
		int64(sum.CashIn), int64(sum.CashOut), int64(sum.ExpensesTotal), int64(sum.CashReturns),
		int64(sum.RetainedCanceledCash),
		int64(expected), int64(actual), notes, shiftID)
	if err != nil {
		return nil, err
	}
	if !auto {
		actorID, actorName := int64(0), ""
		if actor != nil {
			actorID, actorName = actor.ID, actor.UserName
		}
		if _, err := tx.Exec(`INSERT INTO CashBox(UserID,UserName,ShiftID,MovementType,Amount,Reason,MovementDate)
			VALUES(?,?,?,'close',?,'إغلاق جلسة الشيفت',?)`,
			actorID, actorName, shiftID, int64(actual), now); err != nil {
			return nil, err
		}
	}
	auditID, auditName := sh.UserID, sh.UserName
	action := "إغلاق شيفت"
	detail := fmt.Sprintf("إغلاق شيفت رقم %d — المتوقع %s، الفعلي %s، الفرق %s (%s)",
		shiftID, money.FormatEGP(expected), money.FormatEGP(actual), money.FormatEGP(diff), status)
	if auto {
		detail = fmt.Sprintf("%s — المتوقع %s، الفعلي %s، الفرق %s (%s)",
			rolloverFlag, money.FormatEGP(expected), money.FormatEGP(actual), money.FormatEGP(diff), status)
	} else if actor != nil {
		auditID, auditName = actor.ID, actor.UserName
	}
	if err := AuditTx(tx, auditID, auditName, action, "شيفت", nil, 0, 0, "", detail); err != nil {
		return nil, err
	}
	sum.ExpectedBalance = expected
	return sum, nil
}

// AutoCloseStaleShifts auto-closes every open shift of userID dated
// before today (F32-4). Actual = expected, no CashBox 'close' row, and
// it never opens a new shift. Returns the auto-closed shift ids.
func (s *Store) AutoCloseStaleShifts(ctx context.Context, userID int64) ([]int64, error) {
	var closed []int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT ShiftID FROM Shifts WHERE UserID=? AND IsOpen=1
			AND ShiftDate IS NOT NULL AND ShiftDate<? ORDER BY ShiftID`, userID, Today())
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		for _, id := range ids {
			if _, err := closeShiftTx(tx, nil, id, true, ""); err != nil {
				return err
			}
			closed = append(closed, id)
		}
		return nil
	})
	return closed, err
}

func autoCloseStaleShiftsTx(tx *sql.Tx, userID int64) ([]int64, error) {
	rows, err := tx.Query(`SELECT ShiftID FROM Shifts WHERE UserID=? AND IsOpen=1
		AND ShiftDate IS NOT NULL AND ShiftDate<? ORDER BY ShiftID`, userID, Today())
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	var closed []int64
	for _, id := range ids {
		if _, err := closeShiftTx(tx, nil, id, true, ""); err != nil {
			return nil, err
		}
		closed = append(closed, id)
	}
	return closed, nil
}

// OpenShift opens a shift for the user (H-01..H-06). Only on an
// explicit call — never automatic. A fresh day always starts from a
// freshly typed opening balance (default 0, never carried from a
// previous close). A closed shift from today resumes only with
// resume=true (same shift, SessionCount+1). An already-open shift is
// returned as-is. Returns the shift id.
func (s *Store) OpenShift(ctx context.Context, user *auth.User, opening money.Cents, resume bool) (int64, error) {
	if user == nil {
		return 0, errors.New("يلزم تسجيل الدخول")
	}
	if err := rbac.Require(user.Role, rbac.PermOpenShift); err != nil {
		return 0, err
	}
	if opening < 0 {
		return 0, errors.New("رصيد غير صحيح")
	}
	// R22-E-F2: magnitude cap. A near-MaxInt64 opening used to be
	// accepted and stored verbatim; the expected balance then wrapped
	// negative on the first cash sale and close persisted the wrapped
	// value as متوازن, while every cash drawer-gated operation on the
	// shift failed with a raw SQLite overflow error.
	if opening > money.MaxOpeningCents {
		return 0, errors.New("رصيد الافتتاح كبير جداً — تجاوز الحد الأقصى المسموح به (100,000,000 ج.م)")
	}
	var shiftID int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		// Rollover first: a previous-day open shift auto-closes here.
		if _, err := autoCloseStaleShiftsTx(tx, user.ID); err != nil {
			return err
		}
		today := Today()
		// Already open today (H-05)?
		var openID int64
		err := tx.QueryRow(`SELECT ShiftID FROM Shifts WHERE UserID=? AND ShiftDate=? AND IsOpen=1
			ORDER BY ShiftID DESC LIMIT 1`, user.ID, today).Scan(&openID)
		if err == nil {
			shiftID = openID
			return nil
		}
		if err != sql.ErrNoRows {
			return err
		}
		// Safety net: an orphan open shift from another date must
		// never be silently continued (normally unreachable: the
		// rollover above already closed it).
		var orphanID int64
		err = tx.QueryRow(`SELECT ShiftID FROM Shifts WHERE UserID=? AND IsOpen=1 AND ShiftDate<>?
			ORDER BY ShiftID DESC LIMIT 1`, user.ID, today).Scan(&orphanID)
		if err == nil {
			return fmt.Errorf("يوجد شيفت مفتوح من يوم سابق (رقم %d) — أغلقه أولاً قبل فتح شيفت جديد", orphanID)
		}
		if err != sql.ErrNoRows {
			return err
		}
		// Closed shift today?
		var closedID, closedOpening int64
		var closedSessions int
		err = tx.QueryRow(`SELECT ShiftID,OpeningBalance,SessionCount FROM Shifts
			WHERE UserID=? AND ShiftDate=? AND IsOpen=0 ORDER BY ShiftID DESC LIMIT 1`,
			user.ID, today).Scan(&closedID, &closedOpening, &closedSessions)
		closedToday := err == nil
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if closedToday && !resume {
			return errors.New("يوجد يوم عمل محفوظ لهذا المستخدم اليوم. سيتم استكمال نفس اليوم بنفس الرصيد والتجميعات السابقة بدون تصفير. هل تريد استكمال يوم العمل الآن؟ (اختر استكمال لاستئناف يوم العمل)")
		}
		now := Now()
		if closedToday {
			// R22-E-F2: a stored (pre-cap) absurd opening must not be
			// re-activated by resume — the resumed shift could never
			// summarise or close cleanly (its expected balance wraps),
			// so refuse here with the same Arabic message instead of
			// letting new sales land on a poisoned shift.
			if money.Cents(closedOpening) > money.MaxOpeningCents {
				return errors.New("رصيد الافتتاح المحفوظ كبير جداً — تجاوز الحد الأقصى المسموح به (100,000,000 ج.م)")
			}
			// Same-day resume (H-04): same shift, new session.
			shiftID = closedID
			if _, err := tx.Exec(`UPDATE Shifts SET Status='Open', IsOpen=1, CloseTime=NULL,
				CloseDateTime=NULL, SessionCount=SessionCount+1,
				ExpectedBalance=NULL, ActualBalance=NULL, Difference=NULL,
				DifferenceAmount=NULL, DifferenceStatus=NULL,
				ExpectedDrawerBalance=NULL, ActualDrawerBalance=NULL,
				TotalSalesCash=0, TotalSalesCredit=0, TotalReturns=0, TotalExpenses=0,
				CashSalesTotal=0, VisaSalesTotal=0, VodafoneCashSalesTotal=0,
				CashDebtCollectionTotal=0, VisaDebtCollectionTotal=0,
				VodafoneDebtCollectionTotal=0, CashInTotal=0, CashOutTotal=0,
				ExpenseTotal=0, CashReturnTotal=0, RetainedCanceledCashTotal=0 WHERE ShiftID=?`, shiftID); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO ShiftSessions(ShiftID,UserID,UserName,SessionDate,OpenTime,IsOpen)
				VALUES(?,?,?,?,?,1)`, shiftID, user.ID, user.UserName, today, now); err != nil {
				return err
			}
			// 'resume' (not 'open'): a resume is not a second opening
			// and must not double-count the opening balance.
			if _, err := tx.Exec(`INSERT INTO CashBox(UserID,UserName,ShiftID,MovementType,Amount,Reason,MovementDate)
				VALUES(?,?,?,'resume',?,'استكمال يوم العمل',?)`,
				user.ID, user.UserName, shiftID, closedOpening, now); err != nil {
				return err
			}
			detail := fmt.Sprintf("فتح شيفت رقم %d برصيد افتتاحي %s | الجلسة رقم: %d",
				shiftID, money.FormatEGP(money.Cents(closedOpening)), closedSessions+1)
			return AuditTx(tx, user.ID, user.UserName, "فتح شيفت", "شيفت", nil, 0, 0, "", detail)
		}
		// Fresh shift (H-03/H-06).
		res, err := tx.Exec(`INSERT INTO Shifts(UserID,UserName,ShiftDate,OpenTime,Status,OpeningBalance,IsOpen,SessionCount)
			VALUES(?,?,?,?,'Open',?,1,1)`, user.ID, user.UserName, today, now, int64(opening))
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		shiftID = id
		if _, err := tx.Exec(`INSERT INTO ShiftSessions(ShiftID,UserID,UserName,SessionDate,OpenTime,IsOpen)
			VALUES(?,?,?,?,?,1)`, shiftID, user.ID, user.UserName, today, now); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO CashBox(UserID,UserName,ShiftID,MovementType,Amount,Reason,MovementDate)
			VALUES(?,?,?,'open',?,'فتح يوم العمل',?)`,
			user.ID, user.UserName, shiftID, int64(opening), now); err != nil {
			return err
		}
		detail := fmt.Sprintf("فتح شيفت رقم %d برصيد افتتاحي %s", shiftID, money.FormatEGP(opening))
		return AuditTx(tx, user.ID, user.UserName, "فتح شيفت", "شيفت", nil, 0, 0, "", detail)
	})
	if err != nil {
		return 0, err
	}
	return shiftID, nil
}

// CloseShift closes a shift (H-10/H-11). With shiftID=0 it closes the
// caller's own open shift today. Closing someone else's shift needs
// close_any_shift (Admin/Supervisor/Manager). Actual is always the
// expected balance; Difference is 0, status متوازن.
func (s *Store) CloseShift(ctx context.Context, user *auth.User, shiftID int64) (*ShiftSummary, error) {
	if user == nil {
		return nil, errors.New("يلزم تسجيل الدخول")
	}
	var sum *ShiftSummary
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		// Rollover consult first (only when closing "my" shift).
		if shiftID == 0 {
			if _, err := autoCloseStaleShiftsTx(tx, user.ID); err != nil {
				return err
			}
		}
		var targetID int64
		if shiftID != 0 {
			sh, err := getShiftTx(tx, shiftID)
			if err != nil {
				return err
			}
			if !sh.IsOpen {
				return errors.New("الشيفت المحدد غير مفتوح")
			}
			if sh.UserID != user.ID {
				if err := rbac.Require(user.Role, rbac.PermCloseAnyShift); err != nil {
					return err
				}
			} else if err := rbac.Require(user.Role, rbac.PermCloseOwnShift); err != nil {
				return err
			}
			targetID = shiftID
		} else {
			if err := rbac.Require(user.Role, rbac.PermCloseOwnShift); err != nil {
				return err
			}
			id, err := OpenShiftID(tx, user.ID)
			if err != nil {
				return errors.New("لا يوجد شيفت مفتوح لإغلاقه")
			}
			targetID = id
		}
		var err error
		sum, err = closeShiftTx(tx, user, targetID, false, "")
		return err
	})
	if err != nil {
		return nil, err
	}
	return sum, nil
}

// GetShiftStatus returns the three-state status for one user (H-07).
// With a user id given, a previous-day open shift is auto-closed
// first (F32-4), so the display can never show a stale shift.
func (s *Store) GetShiftStatus(ctx context.Context, userID int64) (*ShiftStatus, error) {
	closed, err := s.AutoCloseStaleShifts(ctx, userID)
	if err != nil {
		return nil, err
	}
	today := Today()
	st := &ShiftStatus{Date: today, State: "none", StatusText: "لا يوجد شيفت مفتوح"}
	var openID int64
	var openName string
	err = s.DB.QueryRow(`SELECT ShiftID,UserName FROM Shifts WHERE ShiftDate=? AND IsOpen=1 AND UserID=?
		ORDER BY ShiftID DESC LIMIT 1`, today, userID).Scan(&openID, &openName)
	if err == nil {
		st.State = "open"
		st.HasOpen = true
		st.ShiftID = openID
		st.UserName = openName
		st.StatusText = fmt.Sprintf("شيفت مفتوح #%d", openID)
		return st, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	var closedID int64
	var closedName string
	err = s.DB.QueryRow(`SELECT ShiftID,UserName FROM Shifts WHERE ShiftDate=? AND IsOpen=0 AND UserID=?
		ORDER BY ShiftID DESC LIMIT 1`, today, userID).Scan(&closedID, &closedName)
	if err == nil {
		st.State = "closed"
		st.ShiftID = closedID
		st.UserName = closedName
		st.StatusText = "يوم العمل اليوم محفوظ - مغلق حاليًا"
		return st, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	// FIX-3 L17: a stale shift auto-closed by this very consult is
	// reported (State "closed", ShiftID, StatusText naming the
	// auto-close) instead of "none".
	if len(closed) > 0 {
		last := closed[len(closed)-1]
		var autoName string
		_ = s.DB.QueryRow(`SELECT UserName FROM Shifts WHERE ShiftID=?`, last).Scan(&autoName)
		st.State = "closed"
		st.ShiftID = last
		st.UserName = autoName
		st.StatusText = fmt.Sprintf("تم إغلاق الشيفت #%d تلقائيًا — يوم جديد", last)
		return st, nil
	}
	return st, nil
}

// ListShiftsByDate lists all shifts of a date (daily close / monitor).
func (s *Store) ListShiftsByDate(ctx context.Context, date string) ([]*Shift, error) {
	rows, err := s.DB.Query(`SELECT `+shiftCols+` FROM Shifts WHERE ShiftDate=? ORDER BY ShiftID`, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Shift
	for rows.Next() {
		var sh Shift
		var exp, act, diff sql.NullInt64
		var closeTime, notes, diffStatus sql.NullString
		var isOpen int
		if err := rows.Scan(&sh.ID, &sh.UserID, &sh.UserName, &sh.ShiftDate, &sh.OpenTime,
			&closeTime, &sh.Status, &sh.OpeningBalance, &exp, &act, &diff,
			&diffStatus, &sh.TotalSalesCash, &sh.TotalSalesCredit,
			&sh.TotalReturns, &sh.TotalExpenses, &sh.RetainedCanceledCashTotal, &notes, &isOpen, &sh.SessionCount); err != nil {
			return nil, err
		}
		sh.CloseTime = closeTime.String
		sh.Notes = notes.String
		sh.DifferenceStatus = diffStatus.String
		sh.ExpectedBalance = money.Cents(exp.Int64)
		sh.ActualBalance = money.Cents(act.Int64)
		sh.Difference = money.Cents(diff.Int64)
		sh.IsOpen = isOpen == 1
		out = append(out, &sh)
	}
	return out, rows.Err()
}

// ApplyLiveSummaryToOpenShifts replaces the stored
// Expected/Actual/Difference/Retained values of open shifts with
// the live summary — the same source the shift pages use. Stored
// columns are only written at close, so an open shift would
// otherwise render المتوقع 0.00 next to a real opening balance
// (PR9-L2: DailyClose; PR10-L2: the shifts page and its API).
// Expected is live, Actual is Expected (H-10: actual is always
// the system-computed expected, never typed), Difference 0 /
// متوازن, and the stored retained total (N3) is likewise replaced
// with the live bucket. Closed shifts keep their stored (closed)
// values. Shared by DailyClose, the shifts page and apiShiftsList
// so the three tables cannot drift apart again. It only rewrites
// the in-memory rows — it never writes stored values.
func (s *Store) ApplyLiveSummaryToOpenShifts(ctx context.Context, shifts []*Shift) error {
	for _, sh := range shifts {
		if !sh.IsOpen {
			continue
		}
		sum, err := s.ShiftSummary(ctx, sh.ID)
		if err != nil {
			return err
		}
		sh.ExpectedBalance = sum.ExpectedBalance
		sh.ActualBalance = sum.ExpectedBalance
		sh.Difference = 0
		sh.DifferenceStatus = "متوازن"
		sh.RetainedCanceledCashTotal = sum.RetainedCanceledCash
	}
	return nil
}

// OpenShiftForUser returns the user's open shift today, or nil.
// It auto-closes a previous-day open shift first (rollover consult).
func (s *Store) OpenShiftForUser(ctx context.Context, userID int64) (*Shift, error) {
	if _, err := s.AutoCloseStaleShifts(ctx, userID); err != nil {
		return nil, err
	}
	var sh *Shift
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		id, err := OpenShiftID(tx, userID)
		if err != nil {
			return nil // no open shift: not an error here
		}
		var err2 error
		sh, err2 = getShiftTx(tx, id)
		return err2
	})
	if err != nil {
		return nil, err
	}
	return sh, nil
}

// ---------------------------------------------------------------- daily close

// DailyCloseReport is the per-day report (D-01/D-02) — all shifts/users,
// not the same as a shift close (D-07).
type DailyCloseReport struct {
	Date               string
	TotalSales         money.Cents
	TotalCash          money.Cents
	CashDebtCollection money.Cents
	// FIX-1 M2: collections split by method (cash is the only part
	// that enters TotalCash / the drawer).
	VisaDebtCollection     money.Cents
	VodafoneDebtCollection money.Cents
	// FIX-2 L1: cash handed over by couriers settling debts — also
	// drawer cash, part of TotalCash.
	CourierCollection money.Cents
	TotalCredit       money.Cents
	TotalReturns      money.Cents
	CashReturns       money.Cents
	// PR3-H1: same-day cancel refunds funded by debt collections
	// exceed the invoice's at-sale cash — the excess left the
	// drawer today and must leave NetCash too.
	CancelRefundExcess money.Cents
	TotalExpenses     money.Cents
	TotalProfit       money.Cents
	NetCash           money.Cents
	InvoiceCount      int
	CanceledCount     int
	CashDifference    money.Cents
	Shifts            []*Shift
}

// DailyClose builds the daily close report for a date (default today).
// Permission: access_daily_close (Admin/Supervisor/Manager).
// netCash = totalCash − totalExpenses − cashReturns − cancelRefundExcess
// (H12: only the cash portion of returns leaves the drawer; a Visa
// return never touches cash).
func (s *Store) DailyClose(ctx context.Context, user *auth.User, date string) (*DailyCloseReport, error) {
	if user == nil {
		return nil, errors.New("يلزم تسجيل الدخول")
	}
	if err := rbac.Require(user.Role, rbac.PermAccessDailyClose); err != nil {
		return nil, err
	}
	if strings.TrimSpace(date) == "" {
		date = Today()
	}
	rep := &DailyCloseReport{Date: date}
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		// Active invoices of the day.
		invRows, err := tx.Query(`SELECT InvoiceID,NetAmount,PaidAmount,PaymentMethod,RemainingAmount
			FROM Invoices WHERE substr(InvoiceDate,1,10)=? AND IsCanceled=0`, date)
		if err != nil {
			return err
		}
		var invIDs []int64
		invMethod := map[int64]string{}
		invPaid := map[int64]money.Cents{}
		for invRows.Next() {
			var id int64
			var net, paid, remaining int64
			var method string
			if err := invRows.Scan(&id, &net, &paid, &method, &remaining); err != nil {
				invRows.Close()
				return err
			}
			invIDs = append(invIDs, id)
			invMethod[id] = normMethod(method)
			invPaid[id] = money.Cents(paid)
			rep.TotalSales += money.Cents(net)
			rep.TotalCredit += money.Cents(remaining)
		}
		invRows.Close()
		rep.InvoiceCount = len(invIDs)
		// At-sale cash (Payments rows, with PaidAmount fallback).
		seen := map[int64]bool{}
		if len(invIDs) > 0 {
			ph := strings.Repeat("?,", len(invIDs))
			ph = ph[:len(ph)-1]
			args := make([]any, len(invIDs))
			for i, id := range invIDs {
				args[i] = id
			}
			payRows, err := tx.Query(`SELECT InvoiceID,Amount,PaymentMethod FROM Payments
				WHERE InvoiceID IN (`+ph+`) AND Notes='دفعة عند البيع'`, args...)
			if err != nil {
				return err
			}
			for payRows.Next() {
				var invID int64
				var amt int64
				var method string
				if err := payRows.Scan(&invID, &amt, &method); err != nil {
					payRows.Close()
					return err
				}
				seen[invID] = true
				if normMethod(method) == "Cash" {
					rep.TotalCash += money.Cents(amt)
				}
			}
			payRows.Close()
		}
		// FIX-1 H1: the PaidAmount fallback applies only to invoices
		// with no Payments rows at all — an invoice whose PaidAmount
		// already includes later debt collections must not have it
		// counted again as at-sale cash.
		// FIX-20 R20-A-F1: a return's negative Payments row is NOT
		// a payment — it must not kill the fallback (same double
		// subtraction as the shift summary: the fallback PaidAmount
		// is lost from TotalCash while the return still subtracts).
		hasAnyPayment := map[int64]bool{}
		if len(invIDs) > 0 {
			ph := strings.Repeat("?,", len(invIDs))
			ph = ph[:len(ph)-1]
			args := make([]any, len(invIDs))
			for i, id := range invIDs {
				args[i] = id
			}
			anyRows, err := tx.Query(`SELECT DISTINCT InvoiceID FROM Payments
				WHERE InvoiceID IN (`+ph+`) AND Amount >= 0`, args...)
			if err != nil {
				return err
			}
			for anyRows.Next() {
				var invID int64
				if err := anyRows.Scan(&invID); err != nil {
					anyRows.Close()
					return err
				}
				hasAnyPayment[invID] = true
			}
			anyRows.Close()
		}
		for _, id := range invIDs {
			if !seen[id] && !hasAnyPayment[id] && invMethod[id] == "Cash" {
				rep.TotalCash += invPaid[id]
			}
		}
		// Debt collections received today, split by payment method
		// (FIX-1 M2; NULL/empty method counts as Cash).
		collRows, err := tx.Query(`SELECT Amount,COALESCE(PaymentMethod,'Cash') FROM CustomerPayments
			WHERE substr(PaymentDate,1,10)=?`, date)
		if err != nil {
			return err
		}
		for collRows.Next() {
			var amt int64
			var method string
			if err := collRows.Scan(&amt, &method); err != nil {
				collRows.Close()
				return err
			}
			switch normMethod(method) {
			case "Visa":
				rep.VisaDebtCollection += money.Cents(amt)
			case "Vodafone Cash":
				rep.VodafoneDebtCollection += money.Cents(amt)
			default:
				rep.CashDebtCollection += money.Cents(amt)
			}
		}
		collRows.Close()
		rep.TotalCash += rep.CashDebtCollection
		// FIX-2 L1: cash handed over by couriers settling debts enters
		// the drawer too (CashBox 'in', AutoKind 'courier_collection').
		// PR6-L1: the rows are dated in Go, not SQL — a malformed/
		// empty MovementDate must not drop the row (the shift summary
		// counts it with no date filter); the fallback is the shift
		// the cash entered in (see courierDay). The SQL predicate is
		// a superset: today's rows plus every row whose MovementDate
		// cannot parse (the GLOB mirrors parseDay exactly), so no row
		// DailyClose could attribute to this day is ever filtered out
		// in SQL. Rows are collected first and resolved after the rows
		// close (no query inside the rows loop: MaxOpenConns(1)).
		var courierRows []struct {
			amt     int64
			mov     string
			shiftID int64
		}
		if cRows, err := tx.Query(`SELECT Amount,COALESCE(MovementDate,''),ShiftID FROM CashBox
			WHERE MovementType='in' AND AutoKind='courier_collection' AND (
				substr(MovementDate,1,10)=? OR MovementDate IS NULL
				OR substr(MovementDate,1,10) NOT GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]')`, date); err != nil {
			return err
		} else {
			for cRows.Next() {
				var r struct {
					amt     int64
					mov     string
					shiftID int64
				}
				if err := cRows.Scan(&r.amt, &r.mov, &r.shiftID); err != nil {
					cRows.Close()
					return err
				}
				courierRows = append(courierRows, r)
			}
			cRows.Close()
		}
		for _, r := range courierRows {
			if courierDay(tx, r.mov, r.shiftID) == date {
				rep.CourierCollection += money.Cents(r.amt)
			}
		}
		rep.TotalCash += rep.CourierCollection
		// Returns (all of the day, no IsProcessed filter — FIX25-5).
		// PR7-L1: the rows are dated in Go, not SQL — a malformed/
		// empty ReturnDate must not drop the row from TotalReturns
		// (fix-6 counts the same return's CashBox leg in CashReturns,
		// so the two halves must agree on the day); the fallback is
		// returnsRowDay (own leg via ReturnID/event → event day →
		// shift day → sale day). The SQL predicate
		// is a superset: today's rows plus every row whose ReturnDate
		// cannot parse (the GLOB mirrors parseDay exactly), so no row
		// DailyClose could attribute to this day is ever filtered out
		// in SQL. Rows are collected first and resolved after the rows
		// close (no query inside the rows loop: MaxOpenConns(1)).
		var retRowsSum []struct {
			invID   int64
			refund  int64
			retDate string
			shiftID int64
			retID   int64
			eventID int64
		}
		if rRows, err := tx.Query(`SELECT InvoiceID,TotalRefund,COALESCE(ReturnDate,''),COALESCE(ShiftID,0),ReturnID,COALESCE(ReturnEventID,0) FROM Returns
			WHERE substr(ReturnDate,1,10)=? OR ReturnDate IS NULL
				OR substr(ReturnDate,1,10) NOT GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'`, date); err != nil {
			return err
		} else {
			for rRows.Next() {
				var r struct {
					invID   int64
					refund  int64
					retDate string
					shiftID int64
					retID   int64
					eventID int64
				}
				if err := rRows.Scan(&r.invID, &r.refund, &r.retDate, &r.shiftID, &r.retID, &r.eventID); err != nil {
					rRows.Close()
					return err
				}
				retRowsSum = append(retRowsSum, r)
			}
			rRows.Close()
		}
		for _, r := range retRowsSum {
			if returnsRowDay(tx, r.invID, r.shiftID, r.retID, r.eventID, r.retDate) == date {
				rep.TotalReturns += money.Cents(r.refund)
			}
		}
		// Expenses of the day.
		var expTotal sql.NullInt64
		if err := tx.QueryRow(`SELECT COALESCE(SUM(Amount),0) FROM Expenses
			WHERE substr(ExpenseDate,1,10)=?`, date).Scan(&expTotal); err != nil {
			return sumScanErr(err)
		}
		rep.TotalExpenses = money.Cents(expTotal.Int64)
		// Cash returns actually paid out (CashBox auto-return 'out',
		// classified by structural AutoKind — FIX-2 L1).
		// PR6-L1: the rows are dated in Go, not SQL — a malformed/
		// empty MovementDate must not drop a return leg (the shift
		// summary counts it with no date filter); the fallback is
		// returnLegDay (own row via ReturnID → same-shift sibling
		// (complete only) → shift day → sale day). The SQL
		// predicate is a superset: today's rows plus every row whose
		// predicate is a superset: today's rows plus every row whose
		// MovementDate cannot parse (the GLOB mirrors parseDay
		// exactly), so no leg DailyClose could attribute to this day
		// is ever filtered out in SQL. Rows are collected first and
		// resolved after the rows close (no query inside the rows
		// loop: MaxOpenConns(1)).
		cbRows, err := tx.Query(`SELECT Amount,AutoKind,RefInvoiceID,COALESCE(MovementDate,''),ShiftID,COALESCE(ReturnID,0) FROM CashBox
			WHERE MovementType='out' AND (
				substr(MovementDate,1,10)=? OR MovementDate IS NULL
				OR substr(MovementDate,1,10) NOT GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]')`, date)
		if err != nil {
			return err
		}
		type outRow struct {
			amt      int64
			kind     string
			refInvID int64
			mov      string
			shiftID  int64
			retID    int64
		}
		var outRows []outRow
		for cbRows.Next() {
			var r outRow
			if err := cbRows.Scan(&r.amt, &r.kind, &r.refInvID, &r.mov, &r.shiftID, &r.retID); err != nil {
				cbRows.Close()
				return err
			}
			outRows = append(outRows, r)
		}
		cbRows.Close()
		var dayReturnLegs []struct {
			amt      money.Cents
			refInvID int64
		}
		for _, r := range outRows {
			if r.kind != "return" {
				continue
			}
			if returnLegDay(tx, r.mov, r.refInvID, r.shiftID, r.retID) == date {
				dayReturnLegs = append(dayReturnLegs, struct {
					amt      money.Cents
					refInvID int64
				}{money.Cents(r.amt), r.refInvID})
			}
		}
		// PR2-H1 (daily close): a return leg is excluded from
		// CashReturns only up to the correction this day's TotalCash
		// needs for the invoice — (at-sale cash − cancel refund dated
		// this day) — and only when the sale it reverses was excluded
		// from this day's TotalCash (the invoice is canceled and its
		// sale date is this report date). Sell → return → cancel in
		// one day: NetCash −2000 vs true net 0. The cap matters: a
		// Visa sale (at-sale cash 0) whose return is paid in cash, or
		// a sale whose cash was already collected back via debt
		// collections, keeps its return legs counted. A return of an
		// invoice sold on another day stays counted: its sale never
		// entered this day's TotalCash. A leg with no invoice
		// reference is counted (safe default).
		dayLegsByInvoice := map[int64][]money.Cents{}
		for _, rl := range dayReturnLegs {
			if rl.refInvID > 0 {
				dayLegsByInvoice[rl.refInvID] = append(dayLegsByInvoice[rl.refInvID], rl.amt)
			} else {
				rep.CashReturns += rl.amt
			}
		}
		// PR3-L2: legacy cancel-refund rows (RefInvoiceID=0, invoice
		// number only in the reason) resolved the same way the shift
		// summary does — collect first, resolve after the rows close
		// (no query inside the rows loop: MaxOpenConns(1)).
		legacyRefundByInvoice := map[int64]money.Cents{}
		if legRows, err := tx.Query(`SELECT Amount,Reason,MovementDate FROM CashBox
			WHERE RefInvoiceID=0 AND AutoKind='cancel_refund'`); err == nil {
			type legRow struct {
				amt    int64
				reason string
				mov    string
			}
			var legList []legRow
			for legRows.Next() {
				var lr legRow
				if err := legRows.Scan(&lr.amt, &lr.reason, &lr.mov); err != nil {
					break
				}
				legList = append(legList, lr)
			}
			legRows.Close()
			for _, lr := range legList {
				if id, ok := cancelRefundInvoiceID(tx, 0, lr.reason); ok {
					// PR5-L2: the row belongs to this report day
					// when its effective day (MovementDate,
					// falling back to the invoice's cancel /
					// sale date) is `date` — a malformed
					// MovementDate must not drop it.
					canceledAt, invDate := invoiceCancelDates(tx, id)
					if refundDay(lr.mov, canceledAt, invDate) == date {
						legacyRefundByInvoice[id] += money.Cents(lr.amt)
					}
				}
			}
		}
		// handledDaily tracks invoices whose PR3-H1 correction was
		// applied in the loop below, so the legless pass after it
		// does not apply it twice.
		handledDaily := map[int64]bool{}
		for invID, legs := range dayLegsByInvoice {
			var canceled int
			var invDate string
			if err := tx.QueryRow(`SELECT IsCanceled,substr(InvoiceDate,1,10)
				FROM Invoices WHERE InvoiceID=?`, invID).Scan(&canceled, &invDate); err != nil ||
				canceled == 0 || invDate != date {
				for _, amt := range legs {
					rep.CashReturns += amt
				}
				continue
			}
			atSaleCash := atSaleCashTx(tx, invID)
			// The cancel refund dated this day. (PR4-L2: the date is
			// the refund rows' own MovementDate — refundTodayTx
			// answers 0 when no refund rows are dated this day, so
			// no CanceledAt gate is needed here; a canceled invoice
			// with NULL/malformed CanceledAt is handled the same
			// as a live one.)
			refundToday := refundTodayTx(tx, invID, legacyRefundByInvoice, date)
			cap := atSaleCash - refundToday
			if cap < 0 {
				cap = 0
			}
			var legsTotal money.Cents
			for _, amt := range legs {
				legsTotal += amt
				if cap >= amt {
					cap -= amt
				} else {
					rep.CashReturns += amt - cap
					cap = 0
				}
			}
			// PR3-H1 (daily close): the same two corrections as the
			// shift summary, for an invoice sold and canceled this
			// day. A refund funded by debt collections exceeds the
			// at-sale cash and must leave NetCash by that excess;
			// a non-cash return leaves sale cash in the drawer that
			// the canceled sale's exclusion hides, and it is added
			// back to TotalCash.
			if excess := refundToday - atSaleCash; excess > 0 {
				rep.CancelRefundExcess += excess
			}
			if addBack := atSaleCash - refundToday - legsTotal; addBack > 0 {
				rep.TotalCash += addBack
			}
			handledDaily[invID] = true
		}
		// PR3-H1 (legless invoices): a canceled invoice sold today
		// with no cash return legs today never enters the loop above
		// (a non-cash return leaves no CashBox leg), but its
		// correction is identical — computed here from the same
		// three numbers (legs total 0). Invoices already handled
		// above are skipped.
		canceledTodayRows, err := tx.Query(`SELECT InvoiceID FROM Invoices
			WHERE IsCanceled=1 AND substr(InvoiceDate,1,10)=?`, date)
		if err == nil {
			var canceledList []int64
			for canceledTodayRows.Next() {
				var id int64
				if err := canceledTodayRows.Scan(&id); err != nil {
					break
				}
				canceledList = append(canceledList, id)
			}
			canceledTodayRows.Close()
			for _, id := range canceledList {
				if handledDaily[id] {
					continue
				}
				atSaleCash := atSaleCashTx(tx, id)
				// PR4-L2: no CanceledAt gate — refundTodayTx answers
				// 0 when no refund rows are dated this day.
				refundToday := refundTodayTx(tx, id, legacyRefundByInvoice, date)
				if excess := refundToday - atSaleCash; excess > 0 {
					rep.CancelRefundExcess += excess
				}
				if addBack := atSaleCash - refundToday; addBack > 0 {
					rep.TotalCash += addBack
				}
			}
		}
		// FIX-23 R23-A-F2 (cross-day cancel): the two passes above
		// only ever visit invoices SOLD today (the return-leg loop
		// skips invDate != date, and canceledTodayRows selects
		// substr(InvoiceDate,1,10)=date) — so a cancel refund dated
		// this day for an invoice sold on ANOTHER day never entered
		// CancelRefundExcess, and NetCash overstated by exactly
		// that refund. The shift summary handles the analogous
		// cross-shift case explicitly (FIX-2 H-1: a cross-shift
		// refund is real cash out of THIS shift's drawer, so it
		// enters CashOut in full) — DailyClose needs the same
		// cross-day analogue, and the amount is the FULL refund
		// dated today, not the same-day excess over at-sale cash:
		// the at-sale cash was already counted on the SALE day
		// (yesterday's TotalCash), so none of it is in today's
		// TotalCash to net against — today's outflow is the whole
		// refund. (Same-day: excess = refund - atSaleCash, because
		// the sale is excluded from today's TotalCash and the
		// addBack restores the retained at-sale part; cross-day
		// there is no addBack to restore.) Return legs dated today
		// for the same invoice are already counted in CashReturns
		// by the invDate != date branch above, so adding the full
		// refund here cannot double-count them. Candidates are
		// the invoices behind this day's cancel_refund CashBox
		// rows (structural RefInvoiceID, plus the legacy
		// reason-resolved rows already in legacyRefundByInvoice);
		// refundTodayTx answers 0 for any invoice whose refund
		// rows are not dated this day, so no CanceledAt gate is
		// needed, and invoices already handled by the same-day
		// passes (handledDaily, or sold today) are skipped.
		crossDayIDs := map[int64]bool{}
		if crRows, err := tx.Query(`SELECT RefInvoiceID,Reason FROM CashBox
			WHERE AutoKind='cancel_refund'`); err == nil {
			type crRow struct {
				refInvID int64
				reason   string
			}
			var crList []crRow
			for crRows.Next() {
				var r crRow
				if err := crRows.Scan(&r.refInvID, &r.reason); err != nil {
					break
				}
				crList = append(crList, r)
			}
			crRows.Close()
			for _, r := range crList {
				if id, ok := cancelRefundInvoiceID(tx, r.refInvID, r.reason); ok {
					crossDayIDs[id] = true
				}
			}
		}
		for id := range crossDayIDs {
			if handledDaily[id] {
				continue
			}
			_, invDate := invoiceCancelDates(tx, id)
			if invDate == date {
				continue
			}
			if refundToday := refundTodayTx(tx, id, legacyRefundByInvoice, date); refundToday > 0 {
				rep.CancelRefundExcess += refundToday
			}
		}
		// Profit: unified definition (FIX-1 M1) — same allocated
		// per-line profit as InvoiceProfit (promo, manual invoice
		// discount and FreeCost subtracted), net of returns below.
		profit := money.Cents(0)
		plines, err := loadProfitLines(tx, date, date, "")
		if err != nil {
			return err
		}
		for _, ln := range plines {
			profit += ln.Profit
		}
		// Returns remove their profit (refund − qty×buy), except
		// returns of canceled invoices (their sale booked no profit).
		// Voided free-unit cost is added back (units are back in stock).
		// PR7-L1: the profit rows are dated in Go like the TotalReturns
		// sum above — a malformed/empty ReturnDate must not drop the
		// row from the profit adjustment either (the same return's
		// CashBox leg counts in CashReturns). Same superset predicate
		// as TotalReturns; rows are collected first and resolved after
		// the rows close (no query inside the rows loop:
		// MaxOpenConns(1)).
		retRows, err := tx.Query(`SELECT r.InvoiceID, r.TotalRefund, r.Quantity,
			COALESCE(NULLIF(r.BuyPrice,0),(SELECT d.BuyPrice FROM InvoiceDetails d
				WHERE d.InvoiceID=r.InvoiceID AND d.ProductID=r.ProductID LIMIT 1),0) AS BuyPrice,
			r.VoidedFreeCost, i.IsCanceled, COALESCE(r.ReturnDate,''), COALESCE(r.ShiftID,0), r.ReturnID, COALESCE(r.ReturnEventID,0)
			FROM Returns r LEFT JOIN Invoices i ON i.InvoiceID=r.InvoiceID
			WHERE substr(r.ReturnDate,1,10)=? OR r.ReturnDate IS NULL
				OR substr(r.ReturnDate,1,10) NOT GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'`, date)
		if err != nil {
			return err
		}
		type profitRetRow struct {
			invID      int64
			refund     int64
			qty        int64
			buy        int64
			voidedCost int64
			canceled   sql.NullInt64
			retDate    string
			shiftID    int64
			retID      int64
			eventID    int64
		}
		var profitRets []profitRetRow
		for retRows.Next() {
			var pr profitRetRow
			if err := retRows.Scan(&pr.invID, &pr.refund, &pr.qty, &pr.buy, &pr.voidedCost, &pr.canceled, &pr.retDate, &pr.shiftID, &pr.retID, &pr.eventID); err != nil {
				retRows.Close()
				return err
			}
			profitRets = append(profitRets, pr)
		}
		retRows.Close()
		for _, pr := range profitRets {
			if returnsRowDay(tx, pr.invID, pr.shiftID, pr.retID, pr.eventID, pr.retDate) != date {
				continue
			}
			if !pr.canceled.Valid || pr.canceled.Int64 == 0 {
				cost, cerr := money.MulQty(money.Cents(pr.buy), money.Qty(pr.qty))
				if cerr != nil {
					return cerr
				}
				term, cerr := money.SubChecked(money.Cents(pr.refund), cost)
				if cerr != nil {
					return cerr
				}
				profit, cerr = money.SubChecked(profit, term)
				if cerr != nil {
					return cerr
				}
				profit, cerr = money.AddChecked(profit, money.Cents(pr.voidedCost))
				if cerr != nil {
					return cerr
				}
			}
		}
		rep.TotalProfit = profit
		rep.NetCash = rep.TotalCash - rep.TotalExpenses - rep.CashReturns - rep.CancelRefundExcess
		// Canceled count by cancel day (D18, PR5-L1): see
		// canceledCountTx — a NULL CanceledAt must not hide a
		// canceled invoice from the count while its money is
		// reported.
		canceledCount, err := canceledCountTx(tx, date)
		if err != nil {
			return err
		}
		rep.CanceledCount = canceledCount
		// Cash difference: sum of the day's shifts' differences (M9).
		var diffSum sql.NullInt64
		if err := tx.QueryRow(`SELECT COALESCE(SUM(DifferenceAmount),0) FROM Shifts
			WHERE ShiftDate=? AND DifferenceAmount IS NOT NULL`, date).Scan(&diffSum); err != nil {
			return sumScanErr(err)
		}
		rep.CashDifference = money.Cents(diffSum.Int64)
		return nil
	})
	if err != nil {
		return nil, err
	}
	shifts, err := s.ListShiftsByDate(ctx, date)
	if err != nil {
		return nil, err
	}
	// PR9-L2/PR10-L2: open shifts render the live summary's
	// Expected (shared helper — the shifts page and its API apply
	// the same override, so the three tables cannot drift apart).
	// This fixes the rendered page and /api/dailyclose together —
	// both read rep.Shifts.
	if err := s.ApplyLiveSummaryToOpenShifts(ctx, shifts); err != nil {
		return nil, err
	}
	rep.Shifts = shifts
	return rep, nil
}

// ---------------------------------------------------------------- advanced reports (D-04)

// DailyTopProductRow is one row of the top/bottom products report.
type DailyTopProductRow struct {
	ProductID   int64
	ProductName string
	Qty         money.Qty
	Revenue     money.Cents
	Profit      money.Cents
}

// TopProducts returns the best (asc=false) or worst (asc=true) products
// by revenue over a date range. Profit uses the unified definition
// (FIX-1 M1/M10): per line TotalPrice − PromoDiscount − rounded
// qty×BuyPrice (money.MulQty), minus each invoice's FreeCost + manual
// invoice discount allocated pro-rata — never the truncated SQL
// `TotalPrice − Quantity*BuyPrice/1000`.
func (s *Store) TopProducts(ctx context.Context, user *auth.User, from, to string, limit int, asc bool) ([]*DailyTopProductRow, error) {
	if user == nil {
		return nil, errors.New("يلزم تسجيل الدخول")
	}
	if err := rbac.Require(user.Role, rbac.PermViewReports); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 10
	}
	plines, err := loadProfitLines(s.DB, from, to, "")
	if err != nil {
		return nil, err
	}
	byProd := map[int64]*DailyTopProductRow{}
	var order []int64
	for _, ln := range plines {
		row, ok := byProd[ln.ProductID]
		if !ok {
			row = &DailyTopProductRow{ProductID: ln.ProductID, ProductName: ln.ProductName}
			byProd[ln.ProductID] = row
			order = append(order, ln.ProductID)
		}
		row.Qty += ln.Qty
		row.Revenue += ln.Total
		row.Profit += ln.Profit
	}
	// FIX-12 SW-H06: returns remove their profit, exactly like
	// DailyClose — without this a returned product keeps its full
	// sale profit in the top-products ranking.
	// FIX-17 SW2-L01: returns remove their qty and revenue too, so
	// the row is NET on every column (gross qty/revenue beside net
	// profit implied two different unit counts).
	if adj, err := s.returnProfitAdjustments(ctx, from, to, ""); err != nil {
		return nil, err
	} else {
		for pid, effect := range adj.byProduct {
			if row, ok := byProd[pid]; ok {
				row.Profit -= effect
			}
		}
		for pid, qty := range adj.byProductQty {
			if row, ok := byProd[pid]; ok {
				row.Qty -= qty
			}
		}
		for pid, refund := range adj.byProductRefund {
			if row, ok := byProd[pid]; ok {
				row.Revenue -= refund
			}
		}
	}
	out := make([]*DailyTopProductRow, 0, len(order))
	for _, pid := range order {
		out = append(out, byProd[pid])
	}
	sort.Slice(out, func(i, j int) bool {
		if asc {
			return out[i].Revenue < out[j].Revenue
		}
		return out[i].Revenue > out[j].Revenue
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// CashierRow is one row of the best-cashier report.
type CashierRow struct {
	UserName     string
	InvoiceCount int
	TotalSales   money.Cents
}

// BestCashiers ranks cashiers by sales over a date range.
func (s *Store) BestCashiers(ctx context.Context, user *auth.User, from, to string) ([]*CashierRow, error) {
	if user == nil {
		return nil, errors.New("يلزم تسجيل الدخول")
	}
	if err := rbac.Require(user.Role, rbac.PermViewReports); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(`SELECT COALESCE(UserName,'') AS Name, COUNT(*),
		COALESCE(SUM(NetAmount),0) FROM Invoices
		WHERE IsCanceled=0 AND substr(InvoiceDate,1,10) BETWEEN ? AND ?
		GROUP BY UserName ORDER BY 3 DESC`, from, to)
	if err != nil {
		return nil, sumScanErr(err)
	}
	defer rows.Close()
	var out []*CashierRow
	for rows.Next() {
		var r CashierRow
		var total int64
		if err := rows.Scan(&r.UserName, &r.InvoiceCount, &total); err != nil {
			return nil, sumScanErr(err)
		}
		r.TotalSales = money.Cents(total)
		out = append(out, &r)
	}
	return out, sumScanErr(rows.Err())
}

// PeakHourRow is one row of the peak-hours report.
type PeakHourRow struct {
	Hour         int
	InvoiceCount int
	TotalSales   money.Cents
}

// PeakHours counts invoices/sales per hour of day over a range.
func (s *Store) PeakHours(ctx context.Context, user *auth.User, from, to string) ([]*PeakHourRow, error) {
	if user == nil {
		return nil, errors.New("يلزم تسجيل الدخول")
	}
	if err := rbac.Require(user.Role, rbac.PermViewReports); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(`SELECT CAST(strftime('%H',InvoiceDate) AS INTEGER) AS H,
		COUNT(*), COALESCE(SUM(NetAmount),0) FROM Invoices
		WHERE IsCanceled=0 AND substr(InvoiceDate,1,10) BETWEEN ? AND ?
		GROUP BY H ORDER BY H`, from, to)
	if err != nil {
		return nil, sumScanErr(err)
	}
	defer rows.Close()
	var out []*PeakHourRow
	for rows.Next() {
		var r PeakHourRow
		var total int64
		if err := rows.Scan(&r.Hour, &r.InvoiceCount, &total); err != nil {
			return nil, sumScanErr(err)
		}
		r.TotalSales = money.Cents(total)
		out = append(out, &r)
	}
	return out, sumScanErr(rows.Err())
}

// ExpiryAlertRow is one row of the expiry-alerts report.
type ExpiryAlertRow struct {
	ProductID   int64
	ProductName string
	ExpiryDate  string
	Quantity    money.Qty
	DaysLeft    int
}

// ExpiryAlerts lists expiry-tracked products expiring within days
// (or already expired — negative DaysLeft).
func (s *Store) ExpiryAlerts(ctx context.Context, user *auth.User, days int) ([]*ExpiryAlertRow, error) {
	if user == nil {
		return nil, errors.New("يلزم تسجيل الدخول")
	}
	if err := rbac.Require(user.Role, rbac.PermViewReports); err != nil {
		return nil, err
	}
	if days <= 0 {
		days = 30
	}
	rows, err := s.DB.Query(`SELECT ProductID, ProductName, ExpiryDate, Quantity,
		CAST(julianday(ExpiryDate)-julianday(?) AS INTEGER)
		FROM Products WHERE IsExpiryTracked=1 AND ExpiryDate IS NOT NULL AND ExpiryDate<>''
		AND julianday(ExpiryDate)-julianday(?)<=? ORDER BY ExpiryDate`, Today(), Today(), days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ExpiryAlertRow
	for rows.Next() {
		var r ExpiryAlertRow
		var qty int64
		if err := rows.Scan(&r.ProductID, &r.ProductName, &r.ExpiryDate, &qty, &r.DaysLeft); err != nil {
			return nil, err
		}
		r.Quantity = money.Qty(qty)
		out = append(out, &r)
	}
	return out, rows.Err()
}

// StockLedgerRow is one row of the per-product stock ledger.
type StockLedgerRow struct {
	MovementID   int64
	MovementType string
	Quantity     money.Qty
	BuyPrice     money.Cents
	Notes        string
	MovementDate string
	UserID       int64
}

// StockLedger returns the stock movements of one product over a range.
func (s *Store) StockLedger(ctx context.Context, user *auth.User, productID int64, from, to string) ([]*StockLedgerRow, error) {
	if user == nil {
		return nil, errors.New("يلزم تسجيل الدخول")
	}
	if err := rbac.Require(user.Role, rbac.PermViewStock); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(`SELECT MovementID,MovementType,Quantity,BuyPrice,Notes,MovementDate,UserID
		FROM StockMovements WHERE ProductID=? AND substr(MovementDate,1,10) BETWEEN ? AND ?
		ORDER BY MovementDate, MovementID`, productID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*StockLedgerRow
	for rows.Next() {
		var r StockLedgerRow
		var qty, buy int64
		if err := rows.Scan(&r.MovementID, &r.MovementType, &qty, &buy, &r.Notes, &r.MovementDate, &r.UserID); err != nil {
			return nil, err
		}
		r.Quantity = money.Qty(qty)
		r.BuyPrice = money.Cents(buy)
		out = append(out, &r)
	}
	return out, rows.Err()
}
