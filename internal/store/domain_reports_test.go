package store

import (
	"context"
	"strings"
	"testing"

	"cashierpos-go/internal/auth"
)

func seedReportData(t *testing.T, s *Store) {
	t.Helper()
	_, err := s.DB.Exec(`INSERT INTO Products(ProductID,Barcode,ProductName,BuyPrice,RetailPrice,Quantity,MinQuantity,IsActive)
		VALUES(1,'111','سكر',5000,8000,2000,5000,1),
		(2,'222','أرز',4000,6000,50000,5000,1),
		(3,'333','زيت',10000,15000,3000,5000,1)`)
	if err != nil {
		t.Fatalf("seed products: %v", err)
	}
	_, err = s.DB.Exec(`INSERT INTO Customers(CustomerID,CustomerName,Phone,Balance,CreditLimit,IsActive)
		VALUES(2,'عميل آجل','01111111111',0,100000,1)`)
	if err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	// Invoice 1: retail, cash, 2x sugar + 1x rice; invoice 2: credit customer, wholesale-ish.
	_, err = s.DB.Exec(`INSERT INTO Invoices(InvoiceNumber,CustomerID,SaleType,TotalAmount,Discount,NetAmount,PaidAmount,RemainingAmount,PaymentMethod,UserName,InvoiceDate,IsCanceled)
		VALUES(1,1,'retail',22000,2000,20000,20000,0,'Cash','admin','2026-11-25 10:00:00',0),
		(2,2,'wholesale',12000,0,12000,5000,7000,'Cash','cash1','2026-11-25 11:00:00',0),
		(3,1,'retail',15000,0,15000,15000,0,'Visa','admin','2026-11-25 12:00:00',1)`)
	if err != nil {
		t.Fatalf("seed invoices: %v", err)
	}
	// Details: qty in milliunits; BuyPrice in cents.
	_, err = s.DB.Exec(`INSERT INTO InvoiceDetails(InvoiceID,ProductID,ProductName,Quantity,UnitPrice,BuyPrice,TotalPrice)
		VALUES(1,1,'سكر',2000,8000,5000,16000),
		(1,2,'أرز',1000,6000,4000,6000),
		(2,2,'أرز',2000,6000,4000,12000),
		(3,3,'زيت',1000,15000,10000,15000)`)
	if err != nil {
		t.Fatalf("seed details: %v", err)
	}
	_, err = s.DB.Exec(`INSERT INTO Returns(InvoiceID,ProductID,ProductName,Quantity,UnitPrice,BuyPrice,TotalRefund,Reason,UserID,UserName,ReturnDate,ReturnPaymentMethod,ShiftID)
		VALUES(1,1,'سكر',1000,8000,5000,8000,'تالف',1,'admin','2026-11-25 13:00:00','Cash',0)`)
	if err != nil {
		t.Fatalf("seed returns: %v", err)
	}
	_, err = s.DB.Exec(`INSERT INTO Expenses(Description,Amount,Category,UserID,ExpenseDate)
		VALUES('كهرباء',2000,'تشغيل',1,'2026-11-25 09:00:00')`)
	if err != nil {
		t.Fatalf("seed expenses: %v", err)
	}
	_, err = s.DB.Exec(`INSERT INTO CashBox(UserName,ShiftID,MovementType,Amount,Reason,MovementDate)
		VALUES('admin',1,'in',5000,'إيداع','2026-11-25 09:30:00')`)
	if err != nil {
		t.Fatalf("seed cashbox: %v", err)
	}
	_, err = s.DB.Exec(`INSERT INTO Shifts(UserID,UserName,ShiftDate,OpenTime,Status,OpeningBalance,IsOpen)
		VALUES(1,'admin','2026-11-25','08:00:00','Open',10000,1)`)
	if err != nil {
		t.Fatalf("seed shift: %v", err)
	}
	_, err = s.DB.Exec(`INSERT INTO Vendors(VendorID,VendorName,CurrentBalance,IsActive)
		VALUES(1,'مورد 1',30000,1)`)
	if err != nil {
		t.Fatalf("seed vendor: %v", err)
	}
}

func TestReportsBasic(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testAdmin()
	mustCreateUser(t, s, "cash1", "cashier")
	seedReportData(t, s)
	from, to := "2026-11-25", "2026-11-25"

	sales, err := s.SalesReportFor(ctx, admin, from, to)
	if err != nil {
		t.Fatalf("SalesReportFor: %v", err)
	}
	if sales.InvoiceCount != 2 || int64(sales.TotalSales) != 32000 || int64(sales.TotalDiscount) != 2000 {
		t.Fatalf("sales = %+v, want 2 invoices / 32000 / discount 2000", sales)
	}
	// Profit: inv1 lines (16000-2*5000)+(6000-1*4000)=6000+2000=8000; inv2 (12000-2*4000)=4000 → 12000
	// minus inv1's manual invoice discount 2000 (FIX-1 M1: unified with
	// InvoiceProfit, which subtracts PromoDiscount, the manual invoice
	// discount and FreeCost) → 10000.
	if int64(sales.TotalProfit) != 10000 {
		t.Fatalf("sales profit = %d, want 10000", sales.TotalProfit)
	}
	if len(sales.ByDay) != 1 || len(sales.ByPayment) != 1 || sales.ByPayment[0].Method != "Cash" {
		t.Fatalf("sales breakdown = %+v / %+v", sales.ByDay, sales.ByPayment)
	}
	if _, err := s.SalesReportFor(ctx, admin, "2026-11-26", "2026-11-25"); err == nil {
		t.Fatal("reversed range should be refused")
	}

	profit, err := s.ProfitReportFor(ctx, admin, from, to, "")
	if err != nil {
		t.Fatalf("ProfitReportFor: %v", err)
	}
	if int64(profit.TotalProfit) != 10000 || int64(profit.TotalSales) != 34000 {
		t.Fatalf("profit = %+v, want sales 34000 profit 10000", profit)
	}
	if len(profit.BySaleType) != 2 {
		t.Fatalf("profit by sale type = %+v, want 2", profit.BySaleType)
	}
	if len(profit.Top10) == 0 {
		t.Fatal("profit top10 should not be empty")
	}
	retail, err := s.ProfitReportFor(ctx, admin, from, to, "retail")
	if err != nil {
		t.Fatal(err)
	}
	if int64(retail.TotalSales) != 22000 {
		t.Fatalf("retail profit sales = %d, want 22000", retail.TotalSales)
	}

	debts, total, err := s.DebtsReportFor(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(debts) != 1 || int64(total) != 7000 || debts[0].CustomerName != "عميل آجل" {
		t.Fatalf("debts = %+v total %d", debts, total)
	}

	top, err := s.TopProducts(ctx, admin, from, to, 10, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) == 0 || top[0].ProductName != "أرز" {
		t.Fatalf("top products = %+v, want أرز first", top)
	}

	low, err := s.LowStockReport(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(low) != 2 { // sugar 2000<=5000, oil 3000<=5000
		t.Fatalf("low stock = %+v, want 2", low)
	}

	canceled, err := s.CanceledInvoicesFor(ctx, admin, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(canceled) != 1 || canceled[0].InvoiceNumber != 3 {
		t.Fatalf("canceled = %+v, want invoice 3", canceled)
	}

	pay, err := s.PaymentReportFor(ctx, admin, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(pay.ByMethod) != 1 || int64(pay.CashIn) != 5000 || int64(pay.Expenses) != 2000 || int64(pay.Returns) != 8000 {
		t.Fatalf("payment report = %+v", pay)
	}

	cust, err := s.CustomerReportFor(ctx, admin, 2, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if int64(cust.Balance) != 7000 || cust.InvoiceCount != 1 || int64(cust.TotalSales) != 12000 {
		t.Fatalf("customer report = %+v", cust)
	}
	if _, err := s.CustomerReportFor(ctx, admin, 999, from, to); err == nil {
		t.Fatal("missing customer should error")
	}

	// B2's return analysis (built by B2 — used, not rebuilt here).
	ra, err := s.GetReturnsAnalytics(ctx, admin, from, to)
	if err != nil {
		t.Fatalf("GetReturnAnalysis: %v", err)
	}
	if ra.Count != 1 || int64(ra.TotalValue) != 8000 || len(ra.ByProduct) != 1 {
		t.Fatalf("return analysis = %+v", ra)
	}

	// Permissions: cashier cannot view reports.
	cashier := &auth.User{ID: 2, UserName: "cash1", Role: "cashier"}
	if _, err := s.SalesReportFor(ctx, cashier, from, to); err == nil {
		t.Fatal("cashier should not view sales reports")
	}
}

func TestReportsCSVAndDashboard(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testAdmin()
	mustCreateUser(t, s, "cash1", "cashier")
	seedReportData(t, s)
	from, to := "2026-11-25", "2026-11-25"

	csvBytes, name, err := s.ExportReportCSV(ctx, admin, "sales", from, to)
	if err != nil {
		t.Fatalf("ExportReportCSV sales: %v", err)
	}
	if !strings.Contains(string(csvBytes), "2026-11-25") || !strings.Contains(string(csvBytes), "32000") {
		t.Fatalf("sales CSV missing data: %s", string(csvBytes))
	}
	if !strings.HasSuffix(name, ".csv") {
		t.Fatalf("csv filename = %q", name)
	}
	for _, kind := range []string{"profit", "debts", "top_products", "low_stock", "canceled", "payments", "returns"} {
		if _, _, err := s.ExportReportCSV(ctx, admin, kind, from, to); err != nil {
			t.Fatalf("ExportReportCSV %s: %v", kind, err)
		}
	}
	if _, _, err := s.ExportReportCSV(ctx, admin, "nope", from, to); err == nil {
		t.Fatal("unknown CSV kind should error")
	}
	cashier := &auth.User{ID: 2, UserName: "cash1", Role: "cashier"}
	if _, _, err := s.ExportReportCSV(ctx, cashier, "sales", from, to); err == nil {
		t.Fatal("cashier should not export CSV")
	}

	// Dashboard + shift monitor (today-based values use seeded 2026-11-25 rows,
	// so only structural assertions here).
	d, err := s.Dashboard(ctx, admin)
	if err != nil {
		t.Fatalf("Dashboard: %v", err)
	}
	if len(d.RecentInvoices) != 3 || len(d.TopDebts) != 1 || len(d.VendorDebts) != 1 || len(d.Shifts) != 1 {
		t.Fatalf("dashboard = invoices %d debts %d vendors %d shifts %d",
			len(d.RecentInvoices), len(d.TopDebts), len(d.VendorDebts), len(d.Shifts))
	}
	if len(d.LowStock) != 2 {
		t.Fatalf("dashboard low stock = %d, want 2", len(d.LowStock))
	}
	if d.Shifts[0].ExpectedDrawer == 0 {
		t.Fatal("expected drawer should be computed (opening 10000 + cash-in 5000)")
	}
	m, err := s.ShiftMonitorFor(ctx, admin, from, to)
	if err != nil {
		t.Fatalf("ShiftMonitorFor: %v", err)
	}
	if len(m.OpenShifts) != 1 {
		t.Fatalf("open shifts = %d, want 1", len(m.OpenShifts))
	}
	if len(m.Performance) != 2 {
		t.Fatalf("cashier performance rows = %d, want 2", len(m.Performance))
	}
}

// --- FIX-1 regression tests ---

func TestFix1_ProfitSubtractsDiscountsAndFreeCost(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	// Invoice: TotalPrice 10000, PromoDiscount 2000, manual Discount
	// 1000, FreeCost 500; line 2 units, BuyPrice 3000.
	// Profit = 10000 − 2000 − 1000 − 500 − 2×3000 = 500 (M1).
	if _, err := s.DB.Exec(`INSERT INTO Invoices(InvoiceNumber,InvoiceDate,SaleType,PaymentMethod,CustomerID,
		TotalAmount,Discount,PromoDiscount,FreeCost,NetAmount,PaidAmount,RemainingAmount,IsCanceled)
		VALUES('FIX1-1', ?, 'retail','Cash',1, 11000, 1000, 2000, 500, 8000, 8000, 0, 0)`, Today()); err != nil {
		t.Fatal(err)
	}
	invID, _ := s.DB.Exec(`SELECT last_insert_rowid()`)
	_ = invID
	var iid int64
	_ = s.DB.QueryRow(`SELECT max(InvoiceID) FROM Invoices`).Scan(&iid)
	if _, err := s.DB.Exec(`INSERT INTO Products(Barcode,ProductName,BuyPrice,RetailPrice,Quantity)
		VALUES('FIX1P','سكر',3000,5000,100)`); err != nil {
		t.Fatal(err)
	}
	var pid int64
	_ = s.DB.QueryRow(`SELECT max(ProductID) FROM Products`).Scan(&pid)
	if _, err := s.DB.Exec(`INSERT INTO InvoiceDetails(InvoiceID,ProductID,ProductName,Quantity,UnitPrice,TotalPrice,BuyPrice,PromoDiscount)
		VALUES(?,?, 'سكر', 2000, 5000, 10000, 3000, 2000)`, iid, pid); err != nil {
		t.Fatal(err)
	}
	rep, err := s.SalesReportFor(ctx, admin, Today(), Today())
	if err != nil {
		t.Fatal(err)
	}
	if rep.TotalProfit != 500 {
		t.Fatalf("sales profit = %d, want 500", rep.TotalProfit)
	}
	if rep.TotalGross != 11000 || rep.TotalSales != 8000 {
		t.Fatalf("gross=%d sales(net)=%d, want 11000/8000 (L6)", rep.TotalGross, rep.TotalSales)
	}
	prep, err := s.ProfitReportFor(ctx, admin, Today(), Today(), "")
	if err != nil {
		t.Fatal(err)
	}
	if prep.TotalProfit != 500 {
		t.Fatalf("profit report = %d, want 500", prep.TotalProfit)
	}
	top, err := s.TopProducts(ctx, admin, Today(), Today(), 10, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 1 || top[0].Profit != 500 {
		t.Fatalf("top products = %+v, want one row with profit 500", top)
	}
}

func TestFix1_TopAndBottomDoNotShareSlice(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	// Three products with distinct profits: 10000, 5000, 1000.
	profits := []struct {
		name string
		buy  int64
		prof int64
	}{{"أ", 0, 10000}, {"ب", 5000, 5000}, {"ج", 9000, 1000}}
	for _, p := range profits {
		if _, err := s.DB.Exec(`INSERT INTO Invoices(InvoiceNumber,InvoiceDate,SaleType,PaymentMethod,CustomerID,
			TotalAmount,Discount,PromoDiscount,FreeCost,NetAmount,PaidAmount,RemainingAmount,IsCanceled)
			VALUES(?, ?, 'retail','Cash',1, 10000, 0, 0, 0, 10000, 10000, 0, 0)`, "FIX1-"+p.name, Today()); err != nil {
			t.Fatal(err)
		}
		var iid int64
		_ = s.DB.QueryRow(`SELECT max(InvoiceID) FROM Invoices`).Scan(&iid)
		if _, err := s.DB.Exec(`INSERT INTO Products(Barcode,ProductName,BuyPrice,RetailPrice,Quantity)
			VALUES(?, ?, ?, 10000, 100)`, "FIX1"+p.name, p.name, p.buy); err != nil {
			t.Fatal(err)
		}
		var pid int64
		_ = s.DB.QueryRow(`SELECT max(ProductID) FROM Products`).Scan(&pid)
		if _, err := s.DB.Exec(`INSERT INTO InvoiceDetails(InvoiceID,ProductID,ProductName,Quantity,UnitPrice,TotalPrice,BuyPrice,PromoDiscount)
			VALUES(?,?, ?, 1000, 10000, 10000, ?, 0)`, iid, pid, p.name, p.buy); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := s.ProfitReportFor(ctx, admin, Today(), Today(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Top10) != 3 || len(rep.Bottom10) != 3 {
		t.Fatalf("top/bottom = %d/%d, want 3/3", len(rep.Top10), len(rep.Bottom10))
	}
	// L5: Bottom10 is worst-first and Top10 keeps its own order.
	if rep.Top10[0].ProductName != "أ" || rep.Top10[2].ProductName != "ج" {
		t.Fatalf("Top10 = %v, want أ,ب,ج", rep.Top10)
	}
	if rep.Bottom10[0].ProductName != "ج" || rep.Bottom10[2].ProductName != "أ" {
		t.Fatalf("Bottom10 = %v, want ج,ب,أ", rep.Bottom10)
	}
}

// --- FIX-2 regression tests ---

func TestFix2_PaymentReportExcludesOpenCloseRows(t *testing.T) {
	s := testStore(t)
	admin, shiftID, _, _ := fix1Scenario(t, s)
	ctx := context.Background()
	if _, err := s.CloseShift(ctx, admin, shiftID); err != nil {
		t.Fatal(err)
	}
	pay, err := s.PaymentReportFor(ctx, admin, Today(), Today())
	if err != nil {
		t.Fatal(err)
	}
	// M-1: the 'open' row (10000) and the 'close' row (14500) are
	// snapshots, not money in/out. Only real 'in'/'out' rows count:
	// CashIn = 4000 at-sale cash + 1000 cash collection; CashOut = 0.
	if int64(pay.CashIn) != 5000 {
		t.Fatalf("CashIn = %d, want 5000 (open row must not inflate it)", pay.CashIn)
	}
	if int64(pay.CashOut) != 0 {
		t.Fatalf("CashOut = %d, want 0 (close row must not inflate it)", pay.CashOut)
	}
}
