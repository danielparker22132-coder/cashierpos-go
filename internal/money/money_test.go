package money

import "testing"

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
