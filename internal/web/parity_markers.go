// Parity ≈ markers (GAP-REPORT §C, G-1..G-5).
//
// THE RULE (PyQt fix-4, print_invoice.py:58-82): show the average unit
// price and mark it ≈ exactly when the DISPLAYED average does not
// multiply back to the exact gross; a terminating average stays
// unmarked. Go money is integer cents, so the displayed average is the
// stored 2-decimal unit price itself (53.33 for the 16000/3 case).
//
// GROSS RECONSTRUCTION — CORRECTED FORMULA: the gap report states
// gross = TotalPrice + ItemDiscount + PromoDiscount, but that is wrong
// against the stored data. CreateInvoice stores
// ln.lineTotal = lineGross − itemDiscount (domain_sales.go:295) and
// writes it as TotalPrice (:714), with PromoDiscount in its own
// column; the invoice net is TotalPrice − PromoDiscount
// (domain_reports.go:155, domain_returns.go:177). Pinned by
// internal/store/fix3_lane1_test.go:273-278: a line with gross 4000,
// and promo 400 stores UnitPrice 2000 / TotalPrice 400 /
// PromoDiscount 400 — TotalPrice + ItemDiscount + PromoDiscount
// would give 4400 and falsely mark ≈ on an exact price. The correct
// gross is TotalPrice + ItemDiscount, i.e. the PyQt rule itself:
// money(price × qty) − disc == TotalPrice.
package web

import "cashierpos-go/internal/money"

// approxSaleLine reports whether a stored invoice line's unit price is
// a rounded average that does not multiply back to the line gross:
// MulQty(UnitPrice, Qty) != TotalPrice + ItemDiscount. A plain promo
// or item discount alone never triggers it — only the rounding does.
func approxSaleLine(unit money.Cents, q money.Qty, total, itemDisc money.Cents) bool {
	return money.MulQty(unit, q) != total+itemDisc
}

// approxReturn reports whether a stored return row's unit price is a
// truncated average that does not multiply back to the refund:
// MulQty(UnitPrice, Qty) != TotalRefund. E.g. refund 100¢ over qty 3
// stores unit 33¢, and 33×3=99≠100, so the column shows ≈0.33.
func approxReturn(unit money.Cents, q money.Qty, refund money.Cents) bool {
	return money.MulQty(unit, q) != refund
}

// avgUnit returns the truncated per-unit average of an aggregate
// (total over qty), in cents; avgApprox reports whether that displayed
// average does not multiply back to the exact total. Used for the
// returns-analysis by-product rows, which carry aggregates only (no
// stored per-return unit price): the average is marked ≈ exactly when
// it does not multiply back, like any other merged-line average.
func avgUnit(total money.Cents, q money.Qty) money.Cents {
	if q <= 0 {
		return 0
	}
	return money.Cents(int64(total) * 1000 / int64(q))
}

func avgApprox(total money.Cents, q money.Qty) bool {
	if q <= 0 {
		return false
	}
	return money.MulQty(avgUnit(total, q), q) != total
}
