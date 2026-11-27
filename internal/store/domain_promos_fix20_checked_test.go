package store

import (
	"errors"
	"math"
	"testing"

	"cashierpos-go/internal/money"
)

// fix-20 lane B (R20-B-F2b): direct unit tests for the new checked
// bxgy helpers. These reference post-fix symbols, so their mutation
// proof is the temporary-revert run (see lane REPORT.md): reverting
// domain_promos.go to the pre-fix version makes this file fail to
// compile, and the behavioural tests in domain_promos_fix20_test.go
// fail on the pre-fix tree.

func TestFix20_EarnedFreeUnitsChecked(t *testing.T) {
	// Overflow is reported, not wrapped: 2 groups * MaxInt64.
	if _, err := EarnedFreeUnitsChecked(2000, 1000, math.MaxInt64); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("EarnedFreeUnitsChecked overflow: err = %v, want money.ErrOverflow", err)
	}
	// Sane values are exact.
	if got, err := EarnedFreeUnitsChecked(4000, 2000, 1000); err != nil || got != 2000 {
		t.Fatalf("EarnedFreeUnitsChecked(4, 2, 1) = %d, %v; want 2000, nil", got, err)
	}
	if got, err := EarnedFreeUnitsChecked(1000, 2000, 1000); err != nil || got != 0 {
		t.Fatalf("EarnedFreeUnitsChecked(1, 2, 1) = %d, %v; want 0, nil", got, err)
	}
	if got, err := EarnedFreeUnitsChecked(0, 1000, 1000); err != nil || got != 0 {
		t.Fatalf("EarnedFreeUnitsChecked(0, 1, 1) = %d, %v; want 0, nil", got, err)
	}
}

func TestFix20_UnitsToMilliChecked(t *testing.T) {
	// x = 2^61: x*1000 wraps to 0 unchecked — the divide-by-zero root cause.
	if _, err := unitsToMilliChecked(1 << 61); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("unitsToMilliChecked(2^61): err = %v, want money.ErrOverflow", err)
	}
	if _, err := unitsToMilliChecked(math.MaxInt64/1000 + 1); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("unitsToMilliChecked(MaxInt64/1000+1): err = %v, want money.ErrOverflow", err)
	}
	if got, err := unitsToMilliChecked(100000); err != nil || got != 100000000 {
		t.Fatalf("unitsToMilliChecked(100000) = %d, %v; want 100000000, nil", got, err)
	}
	if got, err := unitsToMilliChecked(1); err != nil || got != 1000 {
		t.Fatalf("unitsToMilliChecked(1) = %d, %v; want 1000, nil", got, err)
	}
}

func TestFix20_BxgyFreeQtyChecked(t *testing.T) {
	// 1 group * (2^61+500000) * 1000 overflows int64 — the wrapped-grant root cause.
	if _, err := bxgyFreeQtyChecked(1, (1<<61)+500000); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("bxgyFreeQtyChecked(1, 2^61+500000): err = %v, want money.ErrOverflow", err)
	}
	// Cap-boundary maximum still fits: 100000 groups * 100000 Y * 1000 = 10^13 milliunits.
	if got, err := bxgyFreeQtyChecked(100000, 100000); err != nil || got != 10000000000000 {
		t.Fatalf("bxgyFreeQtyChecked(100000, 100000) = %d, %v; want 10000000000000, nil", got, err)
	}
	if got, err := bxgyFreeQtyChecked(2, 1); err != nil || got != 2000 {
		t.Fatalf("bxgyFreeQtyChecked(2, 1) = %d, %v; want 2000, nil", got, err)
	}
}

func TestFix20_MaxBxgyUnitsBound(t *testing.T) {
	// The cap is exactly the per-line cart cap in whole units: nothing
	// grantable is ever refused, by construction (see the const doc).
	if MaxBxgyUnits != int64(money.MaxQty/1000) {
		t.Fatalf("MaxBxgyUnits = %d, want money.MaxQty/1000 = %d", MaxBxgyUnits, int64(money.MaxQty/1000))
	}
}
