// Package web_test — FIX-24 lane C: R24-E-N1.
//
// GetCustomer leaked a raw SQLite overflow as 404: a customer with two
// 5e18-remaining invoices answered GET /api/customers/{id} with
// 404 {"error": "SQL logic error: integer overflow (1)"}.
// The fix maps the balance SUM through sumScanErr (money.ErrOverflow,
// Arabic) and gives apiCustomerGet the R23-E-F1 trichotomy: denial -> 403,
// overflow -> 400, "العميل غير موجود" -> 404, anything else -> 500 generic.
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

const fix24CBIG = int64(5000000000000000000) // 5e18

// fix24CPlantInvoice inserts one invoice row by direct SQL.
func fix24CPlantInvoice(t *testing.T, e *e2eEnv, number, custID, shiftID, total, paid, remaining int64) int64 {
	t.Helper()
	res, err := e.store.DB.Exec(`INSERT INTO Invoices(InvoiceNumber,CustomerID,ShiftID,TotalAmount,Discount,
		PromoDiscount,NetAmount,PaidAmount,RemainingAmount,PaymentMethod,UserName,UserID,InvoiceDate,IsCanceled)
		VALUES(?,?,?, ?,0,0, ?,?,?, 'Cash','manager',?, ?,0)`,
		number, custID, shiftID, total, total, paid, remaining, e.userID["manager"], store.Today())
	if err != nil {
		t.Fatalf("plant invoice %d: %v", number, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("plant invoice %d id: %v", number, err)
	}
	return id
}

func TestFix24C_Web_CustomerGet_OverflowIs400(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	shiftID := e.openShift(admin, 100000)
	poison := fix17Customer(t, e, admin, map[string]any{"name": "عميل مسموم"})
	for _, n := range []int64{9401, 9402} {
		fix24CPlantInvoice(t, e, n, poison, shiftID, fix24CBIG, 0, fix24CBIG)
	}
	code, body := e.getJSON(admin, fmt.Sprintf("/api/customers/%d", poison))
	if code != http.StatusBadRequest {
		t.Fatalf("poisoned customer GET: want 400, got %d body=%v", code, body)
	}
	errMsg, _ := body["error"].(string)
	if !strings.Contains(errMsg, "تجاوز الحد") {
		t.Fatalf("poisoned customer GET error = %q, want the Arabic overflow message", errMsg)
	}
	if strings.Contains(errMsg, "SQL logic error") || strings.Contains(errMsg, "integer overflow") {
		t.Fatalf("poisoned customer GET error leaks raw SQLite text: %q", errMsg)
	}
}

func TestFix24C_Web_CustomerGet_Missing404Normal200Denied403(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	shiftID := e.openShift(admin, 100000)

	// Genuinely missing customer still 404 with the Arabic message.
	code, body := e.getJSON(admin, "/api/customers/999999")
	if code != http.StatusNotFound {
		t.Fatalf("missing customer GET: want 404, got %d body=%v", code, body)
	}
	if body["error"] != "العميل غير موجود" {
		t.Fatalf("missing customer GET error = %v, want العميل غير موجود", body["error"])
	}

	// Normal customer still 200 with the exact live balance.
	normal := fix17Customer(t, e, admin, map[string]any{"name": "عميل عادي"})
	fix24CPlantInvoice(t, e, 9403, normal, shiftID, 2500, 0, 2500)
	code, body = e.getJSON(admin, fmt.Sprintf("/api/customers/%d", normal))
	if code != http.StatusOK || body["ok"] != true {
		t.Fatalf("normal customer GET: want 200 ok, got %d body=%v", code, body)
	}
	cust, _ := body["customer"].(map[string]any)
	if cust == nil || cust["Balance"] != float64(2500) {
		t.Fatalf("normal customer Balance = %v, want 2500 (body=%v)", cust["Balance"], body)
	}

	// Warehouse denial still 403 (isDenial branch intact).
	wh := e.warehouseClient()
	code, body = e.getJSON(wh, fmt.Sprintf("/api/customers/%d", normal))
	if code != http.StatusForbidden {
		t.Fatalf("warehouse customer GET: want 403, got %d body=%v", code, body)
	}

	// Malformed id still 400.
	code, body = e.getJSON(admin, "/api/customers/abc")
	if code != http.StatusBadRequest {
		t.Fatalf("malformed id GET: want 400, got %d body=%v", code, body)
	}
}

// TestFix24C_Web_ReAudit_OtherGettersHugeStored re-derives the other
// single-entity getters: each runs plain SELECTs only (no SUM), so absurd
// STORED values must still load with 200 — and a genuinely missing row
// keeps its Arabic 404.
func TestFix24C_Web_ReAudit_OtherGettersHugeStored(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	shiftID := e.openShift(admin, 100000)
	cust := fix17Customer(t, e, admin, map[string]any{"name": "عميل عادي"})

	// GetInvoice: huge stored amounts, RemainingAmount=0 (no balance SUM touched).
	hugeInv := fix24CPlantInvoice(t, e, 9404, cust, shiftID, fix24CBIG, fix24CBIG, 0)
	code, body := e.getJSON(admin, fmt.Sprintf("/api/sales/%d", hugeInv))
	if code != http.StatusOK || body["ok"] != true {
		t.Fatalf("huge stored invoice GET: want 200 ok, got %d body=%v", code, body)
	}
	if body["net"] != float64(fix24CBIG) {
		t.Fatalf("huge stored invoice net = %v, want %d", body["net"], fix24CBIG)
	}
	code, body = e.getJSON(admin, "/api/sales/999999")
	if code != http.StatusNotFound || body["error"] != "الفاتورة غير موجودة" {
		t.Fatalf("missing invoice GET: want 404 الفاتورة غير موجودة, got %d body=%v", code, body)
	}

	// GetProduct + GetProductByBarcode: huge stored prices.
	prod := e.createProduct(admin, "C24BIG", "أرز", "10.00", "100")
	if _, err := e.store.DB.Exec(`UPDATE Products SET BuyPrice=?,RetailPrice=?,WholesalePrice=? WHERE ProductID=?`,
		fix24CBIG, fix24CBIG, fix24CBIG, prod); err != nil {
		t.Fatalf("plant huge product: %v", err)
	}
	code, body = e.getJSON(admin, fmt.Sprintf("/api/products/%d", prod))
	if code != http.StatusOK || body["ok"] != true {
		t.Fatalf("huge stored product GET: want 200 ok, got %d body=%v", code, body)
	}
	code, body = e.getJSON(admin, "/api/products/barcode/C24BIG")
	if code != http.StatusOK || body["ok"] != true {
		t.Fatalf("huge stored product barcode GET: want 200 ok, got %d body=%v", code, body)
	}
	code, body = e.getJSON(admin, "/api/products/999999")
	if code != http.StatusNotFound {
		t.Fatalf("missing product GET: want 404, got %d body=%v", code, body)
	}

	// GetPurchase: huge stored totals.
	res, err := e.store.DB.Exec(`INSERT INTO Vendors(VendorName,Phone,Address,Notes,CreditLimit,CurrentBalance,IsActive,CreatedDate)
		VALUES('مورد','01000000000','','',0,0,1,?)`, store.Today())
	if err != nil {
		t.Fatalf("plant vendor: %v", err)
	}
	vid, _ := res.LastInsertId()
	res, err = e.store.DB.Exec(`INSERT INTO PurchaseInvoices(InvoiceNumber,VendorID,PurchaseDate,TotalAmount,PaidAmount,RemainingAmount,DueDate,PaymentMethod,UserName,Notes)
		VALUES('PI-9405',?, ?, ?,?,?, '','Cash','admin','')`, vid, store.Today(), fix24CBIG, fix24CBIG, fix24CBIG)
	if err != nil {
		t.Fatalf("plant purchase: %v", err)
	}
	purID, _ := res.LastInsertId()
	if _, err := e.store.DB.Exec(`INSERT INTO PurchaseInvoiceDetails(PurchaseID,ProductID,ProductName,Quantity,PurchasePrice,TotalPrice,BatchNumber,ExpiryDate)
		VALUES(?,?,?,?,?,?, '','')`, purID, prod, "أرز", 1000, fix24CBIG, fix24CBIG); err != nil {
		t.Fatalf("plant purchase line: %v", err)
	}
	code, body = e.getJSON(admin, fmt.Sprintf("/api/purchases/%d", purID))
	if code != http.StatusOK || body["ok"] != true {
		t.Fatalf("huge stored purchase GET: want 200 ok, got %d body=%v", code, body)
	}
	code, body = e.getJSON(admin, "/api/purchases/999999")
	if code != http.StatusNotFound {
		t.Fatalf("missing purchase GET: want 404, got %d body=%v", code, body)
	}

	// GetStocktake: huge counted qty (Difference is qty arithmetic, not a SUM).
	res, err = e.store.DB.Exec(`INSERT INTO Stocktakes(CreatedBy,CreatedAt,Status,ClosedAt,ClosedBy,ApprovedBy,ApprovedAt,ApproveReason,Notes)
		VALUES('admin',?,'open','','','','','','')`, store.Today())
	if err != nil {
		t.Fatalf("plant stocktake: %v", err)
	}
	stID, _ := res.LastInsertId()
	if _, err := e.store.DB.Exec(`INSERT INTO StocktakeLines(StocktakeID,ProductID,ProductName,SystemQty,CountedQty,CreatedAt)
		VALUES(?,?,?,?,?,?)`, stID, prod, "أرز", 0, fix24CBIG, store.Today()); err != nil {
		t.Fatalf("plant stocktake line: %v", err)
	}
	code, body = e.getJSON(admin, fmt.Sprintf("/api/stocktake/%d", stID))
	if code != http.StatusOK || body["ok"] != true {
		t.Fatalf("huge stored stocktake GET: want 200 ok, got %d body=%v", code, body)
	}
	code, body = e.getJSON(admin, "/api/stocktake/999999")
	if code != http.StatusNotFound {
		t.Fatalf("missing stocktake GET: want 404, got %d body=%v", code, body)
	}

	// MarkNotificationRead: no SUM, no arithmetic; missing row is Arabic 404.
	code, body = e.postJSON(admin, "/api/notifications/999999/read", nil)
	if code != http.StatusNotFound || body["error"] != "التنبيه غير موجود" {
		t.Fatalf("missing notification read: want 404 التنبيه غير موجود, got %d body=%v", code, body)
	}
}
