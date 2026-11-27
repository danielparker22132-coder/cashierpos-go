package money

import (
	"math"
	"testing"
)

func TestFix1_ParseMoneyRejectsMoreThanTwoDecimals(t *testing.T) {
	if _, err := ParseMoney("10.005"); err == nil {
		t.Fatal("ParseMoney(\"10.005\") should be refused (L2)")
	}
	if got, err := ParseMoney("10.00"); err != nil || got != 1000 {
		t.Fatalf("ParseMoney(\"10.00\") = %d, %v, want 1000", got, err)
	}
	if got, err := ParseMoney("10.99"); err != nil || got != 1099 {
		t.Fatalf("ParseMoney(\"10.99\") = %d, %v, want 1099", got, err)
	}
}

// FIX-19 R19-B-F2: MulQty is checked — the raw price*qty product must fit
// int64, or ErrOverflow is returned instead of a wrapped total.
func TestFix19_MulQtyChecked(t *testing.T) {
	// Exact values, bit-identical to the old unchecked computation.
	if got, err := MulQty(100, 1500); err != nil || got != 150 {
		t.Fatalf("MulQty(100, 1500) = %d, %v, want 150", got, err)
	}
	if got, err := MulQty(101, 1500); err != nil || got != 152 {
		t.Fatalf("MulQty(101, 1500) = %d, %v, want 152 (round half up)", got, err)
	}
	if got, err := MulQty(-100, 1500); err != nil || got != -150 {
		t.Fatalf("MulQty(-100, 1500) = %d, %v, want -150", got, err)
	}
	if got, err := MulQty(0, 1000); err != nil || got != 0 {
		t.Fatalf("MulQty(0, 1000) = %d, %v, want 0", got, err)
	}
	// The review repros: 1e17¢ at qty 100 and at qty 1 both overflow the
	// raw product (1e22 and 1e20) and must be refused.
	if _, err := MulQty(100000000000000000, 100000); err != ErrOverflow {
		t.Fatalf("MulQty(1e17, 100 units) err = %v, want ErrOverflow", err)
	}
	if _, err := MulQty(100000000000000000, 1000); err != ErrOverflow {
		t.Fatalf("MulQty(1e17, 1 unit) err = %v, want ErrOverflow", err)
	}
	// Just below the boundary: 9e15¢ at qty 1 has raw product 9e18, which
	// fits — it must store exactly.
	if got, err := MulQty(9000000000000000, 1000); err != nil || got != 9000000000000000 {
		t.Fatalf("MulQty(9e15, 1 unit) = %d, %v, want 9000000000000000", got, err)
	}
	// The exact boundary: 9223372036854775 * 1000 fits, one cent more does not.
	if got, err := MulQty(9223372036854775, 1000); err != nil || got != 9223372036854775 {
		t.Fatalf("MulQty(9223372036854775, 1 unit) = %d, %v, want 9223372036854775", got, err)
	}
	if _, err := MulQty(9223372036854776, 1000); err != ErrOverflow {
		t.Fatalf("MulQty(9223372036854776, 1 unit) err = %v, want ErrOverflow", err)
	}
	// MaxInt64 at 1 milliunit: product fits, result is the rounded value.
	if got, err := MulQty(math.MaxInt64, 1); err != nil || got != 9223372036854776 {
		t.Fatalf("MulQty(MaxInt64, 1 milliunit) = %d, %v, want 9223372036854776", got, err)
	}
}

func TestFix19_AddSubChecked(t *testing.T) {
	if _, err := AddChecked(math.MaxInt64, 1); err != ErrOverflow {
		t.Fatalf("AddChecked(MaxInt64, 1) err = %v, want ErrOverflow", err)
	}
	if got, err := AddChecked(math.MaxInt64, -1); err != nil || got != math.MaxInt64-1 {
		t.Fatalf("AddChecked(MaxInt64, -1) = %d, %v, want MaxInt64-1", got, err)
	}
	if _, err := AddChecked(math.MinInt64, -1); err != ErrOverflow {
		t.Fatalf("AddChecked(MinInt64, -1) err = %v, want ErrOverflow", err)
	}
	if _, err := SubChecked(0, math.MinInt64); err != ErrOverflow {
		t.Fatalf("SubChecked(0, MinInt64) err = %v, want ErrOverflow", err)
	}
	if got, err := SubChecked(math.MinInt64, math.MinInt64); err != nil || got != 0 {
		t.Fatalf("SubChecked(MinInt64, MinInt64) = %d, %v, want 0", got, err)
	}
}

// FIX-19 R19-B-F2: MulDiv/MulDivRound use an exact intermediate, so a
// product that would wrap int64 still divides exactly.
func TestFix19_MulDiv(t *testing.T) {
	if got, err := MulDiv(9000000000000000000, 9000000000000000000, 9000000000000000000); err != nil || got != 9000000000000000000 {
		t.Fatalf("MulDiv(9e18, 9e18, 9e18) = %d, %v, want 9e18", got, err)
	}
	if got, err := MulDiv(100, 3, 7); err != nil || got != 42 {
		t.Fatalf("MulDiv(100, 3, 7) = %d, %v, want 42 (truncated)", got, err)
	}
	if _, err := MulDiv(1, 1, 0); err == nil {
		t.Fatal("MulDiv(1, 1, 0) should refuse division by zero")
	}
	if got, err := MulDivRound(101, 1500, 1000); err != nil || got != 152 {
		t.Fatalf("MulDivRound(101, 1500, 1000) = %d, %v, want 152", got, err)
	}
	if _, err := MulDiv(math.MaxInt64, 2, 1); err != ErrOverflow {
		t.Fatalf("MulDiv(MaxInt64, 2, 1) err = %v, want ErrOverflow", err)
	}
}

// FIX-19 R19-B-F3: out-of-range floats are refused at parse time.
func TestFix19_CentsFromFloat(t *testing.T) {
	if _, err := CentsFromFloat(1e19); err != ErrOverflow {
		t.Fatalf("CentsFromFloat(1e19) err = %v, want ErrOverflow", err)
	}
	if _, err := CentsFromFloat(9223372036854775808.0); err != ErrOverflow {
		t.Fatalf("CentsFromFloat(2^63) err = %v, want ErrOverflow", err)
	}
	if _, err := CentsFromFloat(-1e19); err != ErrOverflow {
		t.Fatalf("CentsFromFloat(-1e19) err = %v, want ErrOverflow", err)
	}
	if _, err := CentsFromFloat(math.NaN()); err == nil {
		t.Fatal("CentsFromFloat(NaN) should be refused")
	}
	if _, err := CentsFromFloat(math.Inf(1)); err == nil {
		t.Fatal("CentsFromFloat(+Inf) should be refused")
	}
	// In-range values are exact, including the benign 1e17 credit limit.
	if got, err := CentsFromFloat(1e17); err != nil || got != 100000000000000000 {
		t.Fatalf("CentsFromFloat(1e17) = %d, %v, want 100000000000000000", got, err)
	}
	if got, err := CentsFromFloat(100.50); err != nil || got != 100 {
		t.Fatalf("CentsFromFloat(100.50) = %d, %v, want 100 (truncated)", got, err)
	}
}

func TestFix19_ParseMoneyQtyOverflow(t *testing.T) {
	if _, err := ParseMoney("1e17"); err != ErrOverflow {
		t.Fatalf("ParseMoney(\"1e17\") err = %v, want ErrOverflow", err)
	}
	if _, err := ParseMoney("1e19"); err != ErrOverflow {
		t.Fatalf("ParseMoney(\"1e19\") err = %v, want ErrOverflow", err)
	}
	if got, err := ParseMoney("10.00"); err != nil || got != 1000 {
		t.Fatalf("ParseMoney(\"10.00\") = %d, %v, want 1000", got, err)
	}
	if _, err := ParseQty("1e16"); err == nil {
		t.Fatal("ParseQty(\"1e16\") should be refused (1e19 milliunits)")
	}
	if got, err := ParseQty("1.5"); err != nil || got != 1500 {
		t.Fatalf("ParseQty(\"1.5\") = %d, %v, want 1500", got, err)
	}
}
