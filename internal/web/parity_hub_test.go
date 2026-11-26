// Package web_test — hub dashboard D parity tests (GAP-REPORT §A H-1..H-9, H-11).
//
// The hub is verified through live HTTP probes as all four roles
// (admin / manager / cashier / supervisor) plus warehouse where the
// role changes what the hub shows. Guard tests assert code markers
// only (hub.html / layout.html carry no JS "//" comments —
// html/template strips them from served pages, so a "//" assertion
// could never pass on served HTML).
package web_test

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"cashierpos-go/internal/money"
)

func hubBody(t *testing.T, e *e2eEnv, c *http.Client) string {
	t.Helper()
	code, body := e.get(c, "/")
	if code != http.StatusOK {
		t.Fatalf("GET /: want 200, got %d", code)
	}
	return body
}

// bellsSegment extracts the hub bells row (<div class="bells">...</div>)
// so zero-state assertions do not trip on the nav / audit links.
func bellsSegment(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, `<div class="bells">`)
	if start < 0 {
		t.Fatal("hub body has no bells div")
	}
	rest := body[start:]
	end := strings.Index(rest, "</div>")
	if end < 0 {
		t.Fatal("bells div never closes")
	}
	return rest[:end]
}

func TestParityHub_SearchBarAndPermissionFilter(t *testing.T) {
	e := readyEnv(t, false)
	admin := hubBody(t, e, e.adminClient())
	for _, want := range []string{
		`id="hub-search"`,
		`placeholder="ابحث عن شاشة... مثال: المخزون"`,
		`id="hub-search-results"`,
		`id="hub-search-data"`,
		`data-label="لوحة تحكم المدير"`,
		`data-label="المستخدمون"`,
		"لا توجد شاشة مطابقة",
	} {
		if !strings.Contains(admin, want) {
			t.Errorf("admin hub missing search marker %q", want)
		}
	}
	// A cashier must never find an admin-only screen in the search data.
	cash := hubBody(t, e, e.cashierClient())
	for _, forbidden := range []string{
		`data-label="لوحة تحكم المدير"`,
		`data-label="المستخدمون"`,
		`data-label="التدقيق"`,
		`data-label="تقرير طرق الدفع"`,
	} {
		if strings.Contains(cash, forbidden) {
			t.Errorf("cashier search data leaks %q", forbidden)
		}
	}
	for _, want := range []string{
		`data-label="المستحقات"`,
		`data-label="الشيفت"`,
		`data-label="المبيعات"`,
	} {
		if !strings.Contains(cash, want) {
			t.Errorf("cashier search data missing %q", want)
		}
	}
	// Search behaviour markers: Enter opens the first match, and an
	// empty query opens nothing (guarded in the served script).
	for _, want := range []string{
		`ev.key === "Enter"`,
		`if (!q) { return; }`,
	} {
		if !strings.Contains(admin, want) {
			t.Errorf("admin hub search script missing %q", want)
		}
	}
}

func TestParityHub_FiveSectionsAndThirteenDestinations(t *testing.T) {
	e := readyEnv(t, false)
	body := hubBody(t, e, e.adminClient())
	for _, title := range []string{
		"البيع اليومي", "المخزون والمنتجات", "العملاء والفواتير",
		"التقارير والتحليل", "الإدارة",
	} {
		if !strings.Contains(body, title) {
			t.Errorf("admin hub missing section %q", title)
		}
	}
	for _, accent := range []string{"#2563eb", "#059669", "#d97706", "#7c3aed", "#475569"} {
		if !strings.Contains(body, accent) {
			t.Errorf("admin hub missing section accent %q", accent)
		}
	}
	// The 13 PyQt destinations with no Go hub card before this build.
	for _, want := range []string{
		`card-title">الحضور<`, `card-title">حالة الشيفت<`, `card-title">النقدية والدرج<`,
		`card-title">المستحقات<`, `card-title">الفواتير<`, `card-title">مناطق التوصيل<`,
		`card-title">تحليل الربح<`, `card-title">تحليل المرتجعات<`, `card-title">تقرير طرق الدفع<`,
		`card-title">تقرير العملاء<`, `card-title">مراقبة الشيفتات<`, `card-title">لوحة تحكم المدير<`,
		`card-title">الإغلاق اليومي<`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("admin hub missing destination card %q", want)
		}
	}
	for _, href := range []string{
		`href="/attendance"`, `href="/payables"`, `href="/reports/sales"`,
		`href="/zones"`, `href="/reports/profit-analysis"`, `href="/reports/returns"`,
		`href="/reports/payments"`, `href="/reports/customer"`, `href="/shifts/monitor"`,
		`href="/dashboard"`, `href="/dailyclose"`,
	} {
		if !strings.Contains(body, href) {
			t.Errorf("admin hub missing destination link %q", href)
		}
	}
	// Sales stays out of the البيع اليومي panel: no sales card title.
	if strings.Contains(body, `card-title">المبيعات<`) {
		t.Error("sales must not sit in a section panel as a card")
	}
	// One panel at a time: five panels, first open, the rest hidden.
	if n := strings.Count(body, `class="hub-panel"`); n != 5 {
		t.Errorf("want 5 hub panels, got %d", n)
	}
	if !strings.Contains(body, `id="hub-panel-s1">`) {
		t.Error("first panel must be initially open")
	}
	for _, id := range []string{"s2", "s3", "s4", "s5"} {
		if !strings.Contains(body, `id="hub-panel-`+id+`" hidden`) {
			t.Errorf("panel %s must be initially hidden", id)
		}
	}
	// Empty-section line marker (filled by JS when a role has nothing
	// openable in a section — the warehouse has no S1/S3 card).
	if !strings.Contains(body, "لا توجد شاشات متاحة لك في هذا القسم") {
		t.Error("hub missing the empty-section line marker")
	}
	wh := hubBody(t, e, e.warehouseClient())
	if strings.Contains(wh, `card-title">الشيفت<`) {
		t.Error("warehouse hub must not show the الشيفت card")
	}
}

func TestParityHub_StatCardsDrawerAndPending(t *testing.T) {
	e := readyEnv(t, false)
	cash := e.cashierClient()

	// No open shift: drawer shows "—", pending orders 0 (feed readable).
	body := hubBody(t, e, cash)
	drawerDash := `<span class="summary-label">رصيد الدرج</span><span class="summary-value num">—</span>`
	if !strings.Contains(body, drawerDash) {
		t.Error("cashier with no open shift: drawer card must show —")
	}
	if !strings.Contains(body, `<span class="summary-label">طلبات متجر معلقة</span><span class="summary-value num">0</span>`) {
		t.Error("cashier hub: pending-orders card must show 0 with no orders")
	}
	if !strings.Contains(body, "غير متصل") {
		// Offline state only renders when the feed cannot be read, so
		// it is guarded at the template source, not the served body.
		if _, file, _, ok := runtime.Caller(0); !ok {
			t.Fatal("runtime.Caller failed")
		} else if b, err := os.ReadFile(filepath.Join(filepath.Dir(file), "templates", "hub.html")); err != nil {
			t.Fatalf("read hub.html: %v", err)
		} else if !strings.Contains(string(b), "غير متصل") {
			t.Error("hub template must carry the غير متصل offline state")
		}
	}

	// Open a shift with 500.00 opening: drawer shows the expected balance.
	e.openShift(cash, 50000)
	body = hubBody(t, e, cash)
	if !strings.Contains(body, `<span class="summary-label">رصيد الدرج</span><span class="summary-value num">`+money.FormatEGP(50000)+`</span>`) {
		t.Errorf("cashier with open shift: drawer card must show %s", money.FormatEGP(50000))
	}

	// One pending web order: the pending card shows 1.
	admin := e.adminClient()
	code, resp := e.postJSON(admin, "/api/staff/orders", map[string]any{
		"customer_name": "عميل معلق", "status": "CREATED", "payment_status": "PENDING",
		"total": 5000, "items": []map[string]any{{"name": "سكر", "qty": "1", "unit_price": 5000}},
	})
	if code != http.StatusCreated {
		t.Fatalf("intake pending order: want 201, got %d %v", code, resp)
	}
	body = hubBody(t, e, admin)
	if !strings.Contains(body, `<span class="summary-label">طلبات متجر معلقة</span><span class="summary-value num">1</span>`) {
		t.Error("admin hub: pending-orders card must show 1 after intake")
	}
	// Sales + invoices cards are kept.
	for _, want := range []string{"مبيعات اليوم", "عدد فواتير اليوم"} {
		if !strings.Contains(body, want) {
			t.Errorf("admin hub missing kept stat card %q", want)
		}
	}
}

func TestParityHub_BigSellButtonIsSinglePrimary(t *testing.T) {
	e := readyEnv(t, false)
	body := hubBody(t, e, e.adminClient())
	if n := strings.Count(body, "المبيعات — ابدأ البيع"); n != 1 {
		t.Errorf("want exactly 1 primary sell button, got %d", n)
	}
	if !strings.Contains(body, `<a class="btn btn-primary hub-sell" href="/sales">المبيعات — ابدأ البيع</a>`) {
		t.Error("sell button is not the single .btn-primary full-row link to /sales")
	}
	// No second sales destination: the old equal card is gone.
	if strings.Contains(body, `card-title">المبيعات<`) {
		t.Error("old equal sales card still present alongside the primary button")
	}
}

func TestParityHub_LatestThreeInvoices(t *testing.T) {
	e := readyEnv(t, false)
	cash := e.cashierClient()
	admin := e.adminClient()

	// Empty state on a fresh database.
	if body := hubBody(t, e, admin); !strings.Contains(body, "لا توجد فواتير اليوم") {
		t.Error("fresh hub must show the latest-invoices empty state")
	}

	// Four cash sales: newest 3 show, the oldest does not.
	pid := e.createProduct(admin, "HUB001", "صنف لوحة", "10.00", "100")
	e.openShift(cash, 0)
	var nums []int64
	for i := 0; i < 4; i++ {
		_, num := e.cashSale(cash, pid, "1", 1000)
		nums = append(nums, num)
	}
	body := hubBody(t, e, admin)
	for _, n := range nums[1:] {
		if !strings.Contains(body, fmt.Sprintf("فاتورة %d —", n)) {
			t.Errorf("latest-3 must show invoice %d", n)
		}
	}
	if strings.Contains(body, fmt.Sprintf("فاتورة %d —", nums[0])) {
		t.Errorf("latest-3 must not show the oldest invoice %d", nums[0])
	}
	// Display-only rows: no link to the sale from the card.
	if strings.Contains(body, fmt.Sprintf(`href="/sales/%d"`, nums[1])) {
		t.Error("latest-3 rows must be display-only, no invoice link")
	}
	// Cash method renders in Arabic, like PyQt _METHOD_AR.
	if !strings.Contains(body, "— كاش") {
		t.Error("latest-3 must render the Cash method as كاش")
	}
}

func TestParityHub_StockAlertAndStrip(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()

	// Quiet zero state: no strip at all.
	if body := hubBody(t, e, admin); strings.Contains(body, `id="hub-alerts-strip"`) {
		t.Error("hub strip must be hidden entirely at 0")
	}

	// Low stock: dismissible hub-load alert + strip line, both with the
	// عرض المنتجات action.
	pid := e.createProduct(admin, "HUBLOW", "صنف منخفض", "10.00", "5")
	if _, err := e.store.DB.Exec(`UPDATE Products SET MinQuantity = Quantity + 1000 WHERE ProductID=?`, pid); err != nil {
		t.Fatalf("force low stock: %v", err)
	}
	body := hubBody(t, e, admin)
	for _, want := range []string{
		`id="hub-lowstock-alert"`, "صنف وصل للحد الأدنى في المخزون",
		`href="/products">عرض المنتجات<`, `data-dismiss="hub-lowstock-alert"`,
		`id="hub-alerts-strip"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("low-stock hub missing %q", want)
		}
	}

	// Pending evidence adds the second strip line.
	code, resp := e.postJSON(admin, "/api/staff/orders", orderBody("عميل دليل", "AWAITING_VERIFICATION"))
	if code != http.StatusCreated {
		t.Fatalf("intake evidence order: want 201, got %d %v", code, resp)
	}
	body = hubBody(t, e, admin)
	if !strings.Contains(body, "بانتظار مراجعة دليل الدفع") {
		t.Error("hub strip must combine the pending-evidence count")
	}
}

func TestParityHub_StartupInventoryPopup(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	pid := e.createProduct(admin, "HUBEXP", "صنف منتهي", "10.00", "5")
	if _, err := e.store.DB.Exec(`UPDATE Products SET ExpiryDate='2026-01-01', IsExpiryTracked=1 WHERE ProductID=?`, pid); err != nil {
		t.Fatalf("set expired: %v", err)
	}
	near := e.createProduct(admin, "HUBNEAR", "صنف قريب", "10.00", "5")
	nearDate := time.Now().AddDate(0, 0, 3).Format("2006-01-02")
	if _, err := e.store.DB.Exec(`UPDATE Products SET ExpiryDate=?, IsExpiryTracked=1 WHERE ProductID=?`, nearDate, near); err != nil {
		t.Fatalf("set near-expiry: %v", err)
	}
	body := hubBody(t, e, admin)
	for _, want := range []string{
		`id="hub-inventory-alert"`, "صنف منتهي الصلاحية", "صنف ينتهي خلال 7 أيام",
		`data-dismiss="hub-inventory-alert"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("admin hub startup inventory popup missing %q", want)
		}
	}
	// Admin/Manager-only: a cashier never sees the startup popup.
	if body := hubBody(t, e, e.cashierClient()); strings.Contains(body, `id="hub-inventory-alert"`) {
		t.Error("cashier hub must not show the Admin/Manager startup inventory popup")
	}
}

func TestParityHub_ShiftPrompt(t *testing.T) {
	e := readyEnv(t, false)
	cash := e.cashierClient()

	body := hubBody(t, e, cash)
	for _, want := range []string{
		`id="hub-shift-prompt"`, "لا يوجد شيفت مفتوح",
		`href="/shifts">فتح شاشة الشيفت<`, `data-dismiss="hub-shift-prompt"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("no-shift hub prompt missing %q", want)
		}
	}
	// Warehouse has no shift concept: no prompt.
	if body := hubBody(t, e, e.warehouseClient()); strings.Contains(body, `id="hub-shift-prompt"`) {
		t.Error("warehouse hub must not show the shift prompt")
	}
	// Open shift: prompt gone, open-state line shown instead.
	e.openShift(cash, 0)
	body = hubBody(t, e, cash)
	if strings.Contains(body, `id="hub-shift-prompt"`) {
		t.Error("open-shift hub must not show the no-shift prompt")
	}
	if !strings.Contains(body, "شيفت مفتوح") {
		t.Error("open-shift hub must show the open shift line")
	}
}

func TestParityHub_BellsHiddenAtZero(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()

	// Fresh database: both hub bells hidden, header bells hidden too.
	body := hubBody(t, e, admin)
	if seg := bellsSegment(t, body); strings.Contains(seg, "<a") {
		t.Errorf("fresh hub bells row must render no bell link, got %q", seg)
	}
	for _, forbidden := range []string{
		`title="تنبيهات تدقيق غير مقروءة"`, `title="تنبيهات غير مقروءة"`,
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("fresh hub header must hide the bell %q at 0", forbidden)
		}
	}

	// One unread notification + one unread audit alert: both bells show.
	if _, err := e.store.DB.Exec(`INSERT INTO Notifications (Type, Title, UserName, IsRead, Status) VALUES ('custom','تنبيه اختبار','admin',0,'sent')`); err != nil {
		t.Fatalf("seed notification: %v", err)
	}
	if _, err := e.store.DB.Exec(`INSERT INTO AuditAlerts (AlertType, AlertMessage, IsRead, IsResolved) VALUES ('test','تنبيه تدقيق اختبار',0,0)`); err != nil {
		t.Fatalf("seed audit alert: %v", err)
	}
	body = hubBody(t, e, admin)
	seg := bellsSegment(t, body)
	for _, want := range []string{`href="/audit"`, `href="/notifications"`, `>1<`} {
		if !strings.Contains(seg, want) {
			t.Errorf("hub bells with unread items missing %q in %q", want, seg)
		}
	}
	// The audit bell stays Admin/Manager-only: a cashier with unread
	// audit alerts never sees it.
	if body := hubBody(t, e, e.cashierClient()); strings.Contains(bellsSegment(t, body), `href="/audit"`) {
		t.Error("cashier hub must not show the audit bell")
	}
}

func TestParityHub_BeforeUnloadAndPollKept(t *testing.T) {
	e := readyEnv(t, false)
	body := hubBody(t, e, e.adminClient())
	for _, want := range []string{
		"beforeunload", `e.returnValue = ""`,
		// The global 5 s orders poll is untouched.
		"setInterval(poll,5000)", "/api/staff/orders/pending-count",
		"/api/staff/orders/pending-evidence", `id="orders-badge"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("hub chrome missing %q", want)
		}
	}
	// The login page carries the handler too, so its "will be asked
	// on close" line is now true. Anonymous GET /login (fresh client,
	// no session).
	code, loginBody := e.get(&http.Client{}, "/login")
	if code != http.StatusOK {
		t.Fatalf("GET /login: want 200, got %d", code)
	}
	if !strings.Contains(loginBody, "beforeunload") {
		t.Error("login page must carry the beforeunload handler")
	}
	if !strings.Contains(loginBody, "سيُسأل") {
		t.Error("login page lost its exit-confirmation line")
	}
}

func TestParityHub_LockSettingsLink(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	code, body := e.get(admin, "/settings")
	if code != http.StatusOK {
		t.Fatalf("GET /settings: want 200, got %d", code)
	}
	if !strings.Contains(body, `<a href="/lock/settings">إعدادات القفل التلقائي</a>`) {
		t.Error("settings page must link إعدادات القفل التلقائي next to the global-minutes field")
	}
	// The route the link points at still answers.
	if code, _ := e.get(admin, "/lock/settings"); code != http.StatusOK {
		t.Fatalf("GET /lock/settings: want 200, got %d", code)
	}
}

func TestParityHub_AllRolesRender(t *testing.T) {
	e := readyEnv(t, false)
	for name, c := range map[string]*http.Client{
		"admin": e.adminClient(), "manager": e.managerClient(),
		"cashier": e.cashierClient(), "supervisor": e.supClient(),
		"warehouse": e.warehouseClient(),
	} {
		body := hubBody(t, e, c)
		for _, want := range []string{
			`id="hub-search"`, "كل الشاشات", "آخر الفواتير", "رصيد الدرج",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s hub missing %q", name, want)
			}
		}
	}
	// Manager = Admin: the manager sees the admin-only destinations.
	mgr := hubBody(t, e, e.managerClient())
	for _, want := range []string{
		`data-label="لوحة تحكم المدير"`, `data-label="المستخدمون"`,
		`card-title">لوحة تحكم المدير<`,
	} {
		if !strings.Contains(mgr, want) {
			t.Errorf("manager hub missing admin-equivalent %q", want)
		}
	}
}
