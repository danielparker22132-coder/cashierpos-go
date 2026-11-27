// Package web_test — FIX-25 lane C: R25-C-F1.
//
// DailyClose's Expenses SUM and SUM(DifferenceAmount), and BestCashiers'
// SUM(NetAmount), returned the scan error raw, and apiDailyClose /
// apiDailyCloseReport echoed ANY store error verbatim as 400: two 5e18
// expenses answered GET /api/dailyclose with
// 400 {"error":"SQL logic error: integer overflow (1)"}. The fix maps
// the SUM scans through sumScanErr (money.ErrOverflow, Arabic) and
// gives the two handlers the apiShiftSummary trichotomy (overflow ->
// 400 Arabic, "…غير موجود" -> 404, other -> 500 generic).
//
// Poison values are written by direct DB insert only (legacy poison);
// no app path is used to store them.
package web_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"cashierpos-go/internal/store"
)

const fix25CBIG = int64(5000000000000000000) // 5e18

func fix25CExpect400Arabic(t *testing.T, what string, code int, body map[string]any) {
	t.Helper()
	if code != http.StatusBadRequest {
		t.Fatalf("%s: want 400, got %d body=%v", what, code, body)
	}
	errMsg, _ := body["error"].(string)
	if !strings.Contains(errMsg, "تجاوز الحد") {
		t.Fatalf("%s: error = %q, want the Arabic overflow message", what, errMsg)
	}
	if strings.Contains(errMsg, "SQL logic error") || strings.Contains(errMsg, "integer overflow") {
		t.Fatalf("%s: error leaks raw SQLite text: %q", what, errMsg)
	}
}

func fix25CPlantExpenses(t *testing.T, e *e2eEnv, shiftID int64) {
	t.Helper()
	for i := 0; i < 2; i++ {
		if _, err := e.store.DB.Exec(`INSERT INTO Expenses(Description,Amount,Category,UserID,ShiftID,ExpenseDate)
			VALUES(?,?, 'عام', 1, ?, ?)`, "مصروف ضخم", fix25CBIG, shiftID, store.Today()+" 10:00:00"); err != nil {
			t.Fatalf("insert poisoned expense: %v", err)
		}
	}
}

func fix25CPlantDiffShifts(t *testing.T, e *e2eEnv, userID int64) {
	t.Helper()
	for i := 0; i < 2; i++ {
		if _, err := e.store.DB.Exec(`INSERT INTO Shifts(UserID,UserName,ShiftDate,OpenTime,Status,IsOpen,OpeningBalance,DifferenceAmount)
			VALUES(?, 'admin', ?, '10:00:00', 'Closed', 0, 0, ?)`, userID, store.Today(), fix25CBIG); err != nil {
			t.Fatalf("insert poisoned shift: %v", err)
		}
	}
}

func TestFix25C_Web_DailyClose_ExpensesOverflowIs400(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	shiftID := e.openShift(admin, 100000)
	fix25CPlantExpenses(t, e, shiftID)
	code, body := e.getJSON(admin, "/api/dailyclose?date="+store.Today())
	fix25CExpect400Arabic(t, "DailyClose poisoned expenses", code, body)
}

func TestFix25C_Web_DailyClose_DifferenceOverflowIs400(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	// No open shift: two closed shifts carry the poisoned differences.
	// (Admin's user id is looked up from the seeded users table.)
	var adminID int64
	if err := e.store.DB.QueryRow(`SELECT UserID FROM Users WHERE UserName='admin'`).Scan(&adminID); err != nil {
		t.Fatalf("admin id: %v", err)
	}
	fix25CPlantDiffShifts(t, e, adminID)
	code, body := e.getJSON(admin, "/api/dailyclose?date="+store.Today())
	fix25CExpect400Arabic(t, "DailyClose poisoned differences", code, body)
}

func TestFix25C_Web_DailyCloseReport_CashierOverflowIs400(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	shiftID := e.openShift(admin, 100000)
	poison := fix17Customer(t, e, admin, map[string]any{"name": "عميل مسموم"})
	for _, n := range []int64{9501, 9502} {
		fix24CPlantInvoice(t, e, n, poison, shiftID, fix25CBIG, 0, fix25CBIG)
	}
	code, body := e.getJSON(admin, fmt.Sprintf("/api/dailyclose/report?report=cashier&from=%s&to=%s", store.Today(), store.Today()))
	fix25CExpect400Arabic(t, "BestCashiers poisoned nets", code, body)
}

func TestFix25C_Web_DailyCloseReport_PeakOverflowIs400(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	shiftID := e.openShift(admin, 100000)
	poison := fix17Customer(t, e, admin, map[string]any{"name": "عميل مسموم"})
	for _, n := range []int64{9501, 9502} {
		fix24CPlantInvoice(t, e, n, poison, shiftID, fix25CBIG, 0, fix25CBIG)
	}
	code, body := e.getJSON(admin, fmt.Sprintf("/api/dailyclose/report?report=peak&from=%s&to=%s", store.Today(), store.Today()))
	fix25CExpect400Arabic(t, "PeakHours poisoned nets", code, body)
}

func TestFix25C_Web_DailyCloseReport_TopProfitLinesOverflowIs400(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	shiftID := e.openShift(admin, 100000)
	prodID := e.createProduct(admin, "777000999", "أرز", "10000", "100")
	res, err := e.store.DB.Exec(`INSERT INTO Invoices(InvoiceNumber,CustomerID,ShiftID,TotalAmount,Discount,
		PromoDiscount,NetAmount,PaidAmount,RemainingAmount,PaymentMethod,UserName,UserID,InvoiceDate,IsCanceled)
		VALUES(9503, 0, ?, 20000, 0, 0, 20000, 20000, 0, 'Cash', 'admin', ?, ?, 0)`,
		shiftID, e.userID["admin"], store.Today())
	if err != nil {
		t.Fatalf("insert profit-poison invoice: %v", err)
	}
	invID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("profit-poison invoice id: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := e.store.DB.Exec(`INSERT INTO InvoiceDetails(InvoiceID,ProductID,ProductName,Quantity,UnitPrice,BuyPrice,ItemDiscount,PromoDiscount,TotalPrice)
			VALUES(?, ?, 'أرز', 1000, 10000, 100, ?, 0, 10000)`, invID, prodID, fix25CBIG); err != nil {
			t.Fatalf("insert profit-poison detail: %v", err)
		}
	}
	code, body := e.getJSON(admin, fmt.Sprintf("/api/dailyclose/report?report=top&from=%s&to=%s", store.Today(), store.Today()))
	fix25CExpect400Arabic(t, "TopProducts via loadProfitLines", code, body)
}

func TestFix25C_Web_DailyClose_DroppedExpensesIs500Generic(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	_ = e.openShift(admin, 100000)
	if _, err := e.store.DB.Exec(`DROP TABLE Expenses`); err != nil {
		t.Fatalf("drop Expenses: %v", err)
	}
	code, body := e.getJSON(admin, "/api/dailyclose?date="+store.Today())
	if code != http.StatusInternalServerError {
		t.Fatalf("DailyClose dropped Expenses: want 500, got %d body=%v", code, body)
	}
	errMsg, _ := body["error"].(string)
	if errMsg != "حدث خطأ داخلي، حاول مرة أخرى" {
		t.Fatalf("DailyClose dropped Expenses error = %q, want the generic 500 message", errMsg)
	}
}

func TestFix25C_Web_NormalDailyCloseAndCashier(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	shiftID := e.openShift(admin, 100000)
	normal := fix17Customer(t, e, admin, map[string]any{"name": "عميل عادي"})
	fix24CPlantInvoice(t, e, 9504, normal, shiftID, 2500, 2500, 0)
	if _, err := e.store.DB.Exec(`INSERT INTO Expenses(Description,Amount,Category,UserID,ShiftID,ExpenseDate)
		VALUES('مصروف عادي', 1000, 'عام', 1, ?, ?)`, shiftID, store.Today()+" 10:00:00"); err != nil {
		t.Fatalf("insert normal expense: %v", err)
	}
	code, body := e.getJSON(admin, "/api/dailyclose?date="+store.Today())
	if code != http.StatusOK {
		t.Fatalf("normal DailyClose: want 200, got %d body=%v", code, body)
	}
	rep, _ := body["report"].(map[string]any)
	if rep == nil {
		t.Fatalf("normal DailyClose: no report in body=%v", body)
	}
	if rep["TotalExpenses"] != float64(1000) {
		t.Fatalf("normal DailyClose TotalExpenses = %v, want 1000", rep["TotalExpenses"])
	}
	code, body = e.getJSON(admin, fmt.Sprintf("/api/dailyclose/report?report=cashier&from=%s&to=%s", store.Today(), store.Today()))
	if code != http.StatusOK {
		t.Fatalf("normal cashier report: want 200, got %d body=%v", code, body)
	}
	rows, _ := body["cashiers"].([]any)
	if len(rows) != 1 {
		t.Fatalf("normal cashier report rows = %v, want 1 row", body["cashiers"])
	}
}
