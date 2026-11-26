// Package money implements money and quantity arithmetic for CashierPOS.
//
// Money is int64 cents (qirsh): 1 EGP = 100. Quantities are int64 milliunits
// (x1000, max 3 decimal places). No float is ever used on a money path.
package money

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Cents is an amount of money in qirsh (1 EGP = 100 qirsh).
type Cents int64

// Qty is a quantity in milliunits (1 unit = 1000).
type Qty int64

const (
	// OneUnit is one whole unit in milliunits.
	OneUnit Qty = 1000
	// MaxQty is the cart quantity cap (G4): 100000 units.
	MaxQty Qty = 100000 * OneUnit
)

// ErrNegative is returned when an operation would take a balance below zero.
var ErrNegative = errors.New("القيمة لا يمكن أن تكون سالبة")

// ParseMoney parses a user-entered decimal amount (e.g. "12.50") into cents.
// At most 2 decimal places are allowed — more than 2 is refused (FIX-1 L2:
// "10.005" used to round silently to 10.01 instead of being rejected).
func ParseMoney(s string) (Cents, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "ج.م", ""))
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("المبلغ مطلوب")
	}
	if i := strings.Index(s, "."); i >= 0 && len(s)-i-1 > 2 {
		return 0, errors.New("المبلغ بحد أقصى خانتين عشريتين")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, errors.New("مبلغ غير صحيح")
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, errors.New("مبلغ غير صحيح")
	}
	c := Cents(math.Round(f * 100))
	if c < 0 {
		return 0, errors.New("المبلغ لا يمكن أن يكون سالباً")
	}
	return c, nil
}

// ParseQty parses a quantity with at most 3 decimal places into milliunits.
func ParseQty(s string) (Qty, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("الكمية مطلوبة")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, errors.New("كمية غير صحيحة")
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, errors.New("كمية غير صحيحة")
	}
	q := Qty(math.Round(f * 1000))
	if q < 0 {
		return 0, errors.New("الكمية لا يمكن أن تكون سالبة")
	}
	if q > MaxQty {
		return 0, errors.New("الكمية تتجاوز الحد الأقصى المسموح (100000)")
	}
	// Enforce max 3 decimals: the input must round-trip through 3 decimals.
	if math.Abs(f*1000-math.Round(f*1000)) > 1e-6 {
		return 0, errors.New("الكمية بحد أقصى 3 خانات عشرية")
	}
	return q, nil
}

// FormatEGP renders cents as "12.50 ج.م".
func FormatEGP(c Cents) string {
	neg := c < 0
	if neg {
		c = -c
	}
	s := fmt.Sprintf("%d.%02d ج.م", int64(c)/100, int64(c)%100)
	if neg {
		return "-" + s
	}
	return s
}

// FormatQty renders milliunits with up to 3 decimals, trimming trailing zeros.
func FormatQty(q Qty) string {
	neg := q < 0
	if neg {
		q = -q
	}
	whole := int64(q) / 1000
	frac := int64(q) % 1000
	if frac == 0 {
		if neg {
			return fmt.Sprintf("-%d", whole)
		}
		return fmt.Sprintf("%d", whole)
	}
	s := fmt.Sprintf("%d.%03d", whole, frac)
	s = strings.TrimRight(s, "0")
	if neg {
		return "-" + s
	}
	return s
}

// MulQty multiplies a unit price (cents) by a quantity (milliunits),
// rounding half away from zero to the nearest qirsh.
func MulQty(unitPrice Cents, q Qty) Cents {
	// price * qty / 1000, rounded.
	v := int64(unitPrice) * int64(q)
	if v >= 0 {
		return Cents((v + 500) / 1000)
	}
	return Cents((v - 500) / 1000)
}

// RequireNonNegative refuses a value that would go below zero.
func RequireNonNegative(v Cents, what string) error {
	if v < 0 {
		return fmt.Errorf("%s لا يمكن أن يكون سالباً — السالب ممنوع", what)
	}
	return nil
}
