// Package money implements money and quantity arithmetic for CashierPOS.
//
// Money is int64 cents (qirsh): 1 EGP = 100. Quantities are int64 milliunits
// (x1000, max 3 decimal places). No float is ever used on a money path.
package money

import (
	"errors"
	"fmt"
	"math"
	"math/big"
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
	// MaxOpeningCents is the shift opening-balance cap (R22-E-F2):
	// 10,000,000,000 cents = 100,000,000 EGP. No shop drawer can
	// hold a hundred million EGP in cash, so no legitimate opening
	// can ever reach it — while the bound sits ~9 orders of
	// magnitude inside int64, so opening + any plausible day's cash
	// flow can never wrap the expected balance (the pre-fix
	// near-MaxInt64 opening wrapped ExpectedBalance negative and
	// close persisted it as متوازن).
	MaxOpeningCents Cents = 10000000000
)

// ErrNegative is returned when an operation would take a balance below zero.
var ErrNegative = errors.New("القيمة لا يمكن أن تكون سالبة")

// ErrOverflow is returned when a money computation would exceed the int64
// range. Callers must refuse the request (400, nothing stored) instead of
// storing the wrapped value — see R19-B-F2: a wrapped total used to be
// stored on HTTP 200.
var ErrOverflow = errors.New("المبلغ كبير جداً — تجاوز الحد الأقصى المسموح به")

// floatInt64Max is 2^63 as a float64: the first float64 value that does not
// fit in an int64. Floats in [-2^63, 2^63) convert exactly.
const floatInt64Max = 9223372036854775808.0

// FloatToInt64 converts a float64 to int64, refusing values outside the
// int64 range instead of wrapping them to min-int64 (the Go conversion of
// an out-of-range float is implementation-defined and on amd64 yields
// min-int64 — R19-B-F3). NaN and infinities are refused too.
func FloatToInt64(f float64) (int64, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, errors.New("مبلغ غير صحيح")
	}
	if f < -floatInt64Max || f >= floatInt64Max {
		return 0, ErrOverflow
	}
	return int64(f), nil
}

// CentsFromFloat converts a float64 amount in cents to Cents, refusing
// out-of-range values at parse time (R19-B-F3).
func CentsFromFloat(f float64) (Cents, error) {
	v, err := FloatToInt64(f)
	return Cents(v), err
}

// QtyFromFloat converts a float64 quantity in milliunits to Qty, refusing
// out-of-range values at parse time (R19-B-F3).
func QtyFromFloat(f float64) (Qty, error) {
	v, err := FloatToInt64(f)
	return Qty(v), err
}

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
	// R19-B-F3: f*100 can exceed int64 (e.g. "1e17") — the raw float to
	// int conversion would wrap to min-int64. Refuse before converting.
	r := math.Round(f * 100)
	if math.IsNaN(r) || math.IsInf(r, 0) || r < -floatInt64Max || r >= floatInt64Max {
		return 0, ErrOverflow
	}
	c := Cents(int64(r))
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
	// R19-B-F3: refuse before converting — f*1000 can exceed int64.
	r := math.Round(f * 1000)
	if math.IsNaN(r) || math.IsInf(r, 0) || r < -floatInt64Max || r >= floatInt64Max {
		return 0, errors.New("الكمية كبيرة جداً — تجاوزت الحد الأقصى المسموح به")
	}
	q := Qty(int64(r))
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
//
// R19-B-F2: the multiplication is CHECKED — if the raw product
// price*qty overflows int64, ErrOverflow is returned and the caller must
// refuse the request instead of storing the wrapped value. The check is
// on the raw product, not the divided result: a price of 1e17¢ at qty 1
// has a true total of 1e17¢ (fits int64) but its raw product is 1e20,
// so it is refused — the same boundary the old wrapping code crossed.
// Normal values are bit-identical to the old unchecked computation.
func MulQty(unitPrice Cents, q Qty) (Cents, error) {
	p := new(big.Int).Mul(big.NewInt(int64(unitPrice)), big.NewInt(int64(q)))
	if !p.IsInt64() {
		return 0, ErrOverflow
	}
	// price * qty / 1000, rounded half away from zero.
	adj := big.NewInt(500)
	if p.Sign() < 0 {
		adj.Neg(adj)
	}
	p.Add(p, adj)
	p.Quo(p, big.NewInt(1000))
	if !p.IsInt64() {
		return 0, ErrOverflow
	}
	return Cents(p.Int64()), nil
}

// AddChecked adds two cent amounts, refusing on int64 overflow instead of
// wrapping (R19-B-F2: multi-line totals must not wrap either).
func AddChecked(a, b Cents) (Cents, error) {
	if (b > 0 && a > Cents(math.MaxInt64)-b) || (b < 0 && a < Cents(math.MinInt64)-b) {
		return 0, ErrOverflow
	}
	return a + b, nil
}

// AddQtyChecked adds two quantities (milliunits), refusing on int64
// overflow instead of wrapping (R21-C-F1/F2: a stock-restore addition
// fed by a stored near-MaxInt64 grant wrapped to a negative stock).
// Same refusal contract as AddChecked: the caller must refuse the
// operation (400, nothing stored) instead of storing the wrapped value.
func AddQtyChecked(a, b Qty) (Qty, error) {
	if (b > 0 && a > Qty(math.MaxInt64)-b) || (b < 0 && a < Qty(math.MinInt64)-b) {
		return 0, ErrOverflow
	}
	return a + b, nil
}

// SubChecked subtracts two cent amounts, refusing on int64 overflow
// instead of wrapping (R19-B-F2).
func SubChecked(a, b Cents) (Cents, error) {
	if b == Cents(math.MinInt64) {
		if a == Cents(math.MinInt64) {
			return 0, nil
		}
		return 0, ErrOverflow
	}
	return AddChecked(a, -b)
}

// MulDiv computes a*b/divisor, truncated toward zero like Go integer
// division, refusing when the result does not fit int64 (R19-B-F2 — the
// same overflow class as MulQty, in the pro-rata / unit-price paths).
// divisor must not be zero.
func MulDiv(a, b, divisor int64) (int64, error) {
	if divisor == 0 {
		return 0, errors.New("القسمة على صفر")
	}
	p := new(big.Int).Mul(big.NewInt(a), big.NewInt(b))
	p.Quo(p, big.NewInt(divisor))
	if !p.IsInt64() {
		return 0, ErrOverflow
	}
	return p.Int64(), nil
}

// MulDivRound computes (a*b + divisor/2)/divisor, i.e. a*b/divisor rounded
// half away from zero (for non-negative inputs: rounded half up), refusing
// when the result does not fit int64 (R19-B-F2). divisor must not be zero.
func MulDivRound(a, b, divisor int64) (int64, error) {
	if divisor == 0 {
		return 0, errors.New("القسمة على صفر")
	}
	p := new(big.Int).Mul(big.NewInt(a), big.NewInt(b))
	half := big.NewInt(divisor / 2)
	if p.Sign() < 0 {
		p.Sub(p, half)
	} else {
		p.Add(p, half)
	}
	p.Quo(p, big.NewInt(divisor))
	if !p.IsInt64() {
		return 0, ErrOverflow
	}
	return p.Int64(), nil
}

// RequireNonNegative refuses a value that would go below zero.
func RequireNonNegative(v Cents, what string) error {
	if v < 0 {
		return fmt.Errorf("%s لا يمكن أن يكون سالباً — السالب ممنوع", what)
	}
	return nil
}
