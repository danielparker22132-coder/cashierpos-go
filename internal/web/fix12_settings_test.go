// Package web_test — FIX-12 lane F (settings / notifications) HTTP tests.
package web_test

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/store"
)

func fix12Setting(t *testing.T, e *e2eEnv, key string) string {
	t.Helper()
	var v string
	if err := e.store.DB.QueryRow(`SELECT Value FROM AppSettings WHERE Key=?`, key).Scan(&v); err != nil {
		t.Fatalf("setting %s: %v", key, err)
	}
	return v
}

// --- SW-H12: zones save via HTTP must persist the entered zones ---

func TestFix12ZonesSaveViaHTTP(t *testing.T) {
	e := newEnv(t, false)
	e.seedUsers()
	e.changeAdminPassword()
	c := e.adminClient()

	code, body := e.postForm(c, "/settings/zones", url.Values{
		"zone_name":   {"TestZone"},
		"zone_fee":    {"9.50"},
		"zone_active": {"0"},
	})
	if code != 200 || !strings.Contains(body, "تم حفظ مناطق التوصيل") {
		t.Fatalf("POST /settings/zones: code %d body=%q", code, truncate(body, 300))
	}
	zones := e.store.GetDeliveryZones()
	found := false
	for _, z := range zones {
		if z.Name == "TestZone" {
			found = true
			if z.Fee != 950 || !z.Active {
				t.Fatalf("TestZone = fee %d active %v, want 950 true", z.Fee, z.Active)
			}
		}
	}
	if !found {
		t.Fatalf("TestZone not saved; zones = %+v", zones)
	}

	// Duplicate name is refused through HTTP (validation now reachable).
	code, body = e.postForm(c, "/settings/zones", url.Values{
		"zone_name":   {"DupZone", "DupZone"},
		"zone_fee":    {"5.00", "6.00"},
		"zone_active": {"0", "1"},
	})
	if !strings.Contains(body, "منطقة مكررة") {
		t.Fatalf("duplicate zones: want refusal, got code %d body=%q", code, truncate(body, 300))
	}

	// Negative fee is refused, stored zones unchanged.
	before := e.store.GetDeliveryZones()
	code, body = e.postForm(c, "/settings/zones", url.Values{
		"zone_name":   {"NegZone"},
		"zone_fee":    {"-5.00"},
		"zone_active": {"0"},
	})
	if strings.Contains(body, "تم حفظ مناطق التوصيل") {
		t.Fatalf("negative fee: want refusal, got success (code %d)", code)
	}
	after := e.store.GetDeliveryZones()
	if len(after) != len(before) {
		t.Fatalf("after refused negative fee, zones changed: %+v", after)
	}
}

// --- SW-M07: negative / invalid monitor thresholds refused, value kept ---

func TestFix12MonitorNegativeRefused(t *testing.T) {
	e := newEnv(t, false)
	e.seedUsers()
	e.changeAdminPassword()
	c := e.adminClient()

	// Save a known-good threshold first.
	code, body := e.postForm(c, "/settings/monitor", url.Values{
		"alert_threshold_cancel_amount": {"100.00"},
		"alert_threshold_discount_pct":  {"20"},
		"alert_threshold_return_amount": {"50.00"},
	})
	if code != 200 || !strings.Contains(body, "تم حفظ عتبات المراقبة الذكية") {
		t.Fatalf("valid monitor save: code %d body=%q", code, truncate(body, 300))
	}
	if v := fix12Setting(t, e, "alert_threshold_cancel_amount"); v != "10000" {
		t.Fatalf("cancel threshold stored = %q, want 10000", v)
	}

	// Negative cancel amount: refused, stored value unchanged.
	code, body = e.postForm(c, "/settings/monitor", url.Values{
		"alert_threshold_cancel_amount": {"-5.00"},
		"alert_threshold_discount_pct":  {"20"},
		"alert_threshold_return_amount": {"50.00"},
	})
	if strings.Contains(body, "تم حفظ عتبات المراقبة الذكية") {
		t.Fatalf("negative threshold: want refusal, got success (code %d)", code)
	}
	if v := fix12Setting(t, e, "alert_threshold_cancel_amount"); v != "10000" {
		t.Fatalf("after refused save, cancel threshold = %q, want 10000 (unchanged)", v)
	}

	// Garbage discount pct: refused, stored value unchanged.
	code, body = e.postForm(c, "/settings/monitor", url.Values{
		"alert_threshold_cancel_amount": {"100.00"},
		"alert_threshold_discount_pct":  {"abc"},
		"alert_threshold_return_amount": {"50.00"},
	})
	if strings.Contains(body, "تم حفظ عتبات المراقبة الذكية") {
		t.Fatalf("garbage discount pct: want refusal, got success (code %d)", code)
	}
	if v := fix12Setting(t, e, "alert_threshold_discount_pct"); v != "20" {
		t.Fatalf("after refused save, discount pct = %q, want 20 (unchanged)", v)
	}

	// Out-of-range discount pct: refused by the service layer.
	code, body = e.postForm(c, "/settings/monitor", url.Values{
		"alert_threshold_cancel_amount": {"100.00"},
		"alert_threshold_discount_pct":  {"150"},
		"alert_threshold_return_amount": {"50.00"},
	})
	if strings.Contains(body, "تم حفظ عتبات المراقبة الذكية") {
		t.Fatalf("discount pct 150: want refusal, got success (code %d)", code)
	}
	if v := fix12Setting(t, e, "alert_threshold_discount_pct"); v != "20" {
		t.Fatalf("after refused save, discount pct = %q, want 20 (unchanged)", v)
	}
}

// --- SW-L14: restore requires a server-side confirmation ---

func TestFix12RestoreRequiresConfirmation(t *testing.T) {
	e := newEnv(t, true) // file DB: restore refuses :memory:
	e.seedUsers()
	e.changeAdminPassword()
	c := e.adminClient()

	code, body := e.postForm(c, "/backup/now", url.Values{})
	if code != 200 || !strings.Contains(body, "تم إنشاء النسخة") {
		t.Fatalf("POST /backup/now: code %d body=%q", code, truncate(body, 300))
	}
	admin := &auth.User{ID: 1, UserName: "admin", Role: "admin"}
	list, err := e.store.ListBackups(context.Background(), admin)
	if err != nil || len(list) == 0 {
		t.Fatalf("ListBackups: %v %+v", err, list)
	}
	name := list[0].Name

	// Marker created after the backup.
	if _, err := e.store.DB.Exec(`INSERT INTO Products(ProductName,BuyPrice,RetailPrice,Quantity,MinQuantity,Unit,IsActive,CreatedDate)
		VALUES('marker-fix12',100,200,10,5,'قطعة',1,?)`, store.Today()); err != nil {
		t.Fatalf("insert marker: %v", err)
	}

	// Bare POST without confirmation: refused, marker survives.
	code, body = e.postForm(c, "/backup/restore", url.Values{
		"location": {"local"}, "name": {name},
	})
	if strings.Contains(body, "تمت الاستعادة") {
		t.Fatalf("bare restore: want refusal, got restore (code %d)", code)
	}
	if !strings.Contains(body, "استعادة") {
		t.Fatalf("bare restore: want confirmation error, got code %d body=%q", code, truncate(body, 300))
	}
	var n int
	if err := e.store.DB.QueryRow(`SELECT COUNT(*) FROM Products WHERE ProductName='marker-fix12'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("after refused restore, marker count = %d, err %v (want 1)", n, err)
	}

	// With the confirmation value: restores, marker is gone.
	code, body = e.postForm(c, "/backup/restore", url.Values{
		"location": {"local"}, "name": {name}, "confirm": {"استعادة"},
	})
	if code != 200 || !strings.Contains(body, "تمت الاستعادة") {
		t.Fatalf("confirmed restore: code %d body=%q", code, truncate(body, 300))
	}
	if err := e.store.DB.QueryRow(`SELECT COUNT(*) FROM Products WHERE ProductName='marker-fix12'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("after confirmed restore, marker count = %d, err %v (want 0)", n, err)
	}
}

// --- SW-L15: marking another user's notification read must not fake success ---

func TestFix12MarkReadCrossUserRefused(t *testing.T) {
	e := newEnv(t, false)
	e.seedUsers()
	e.changeAdminPassword()
	admin := e.adminClient()
	cashier := e.cashierClient()

	// Admin sends a custom notification to everyone.
	code, _, _ := e.postFormLoc(admin, "/notifications/new", url.Values{
		"type": {"custom"}, "title": {"للجميع"}, "body": {"نص"}, "audience": {"all"},
	})
	if code != 303 {
		t.Fatalf("create notification: want 303, got %d", code)
	}
	var supRow int64
	if err := e.store.DB.QueryRow(`SELECT NotificationID FROM Notifications WHERE UserName='supervisor' AND Status='sent' ORDER BY NotificationID DESC LIMIT 1`).Scan(&supRow); err != nil {
		t.Fatalf("supervisor inbox row: %v", err)
	}

	// Cashier posts the supervisor's row read: must surface an error, and
	// the row must stay unread.
	code, body := e.postForm(cashier, "/notifications/"+itoa64(supRow)+"/read", url.Values{})
	if code == 303 {
		t.Fatalf("cross-user mark-read: want an error page, got silent 303")
	}
	if !strings.Contains(body, "التنبيه غير موجود") {
		t.Fatalf("cross-user mark-read: want refusal text, got code %d body=%q", code, truncate(body, 300))
	}
	var isRead int
	if err := e.store.DB.QueryRow(`SELECT IsRead FROM Notifications WHERE NotificationID=?`, supRow).Scan(&isRead); err != nil || isRead != 0 {
		t.Fatalf("supervisor row IsRead = %d, err %v (want 0, still unread)", isRead, err)
	}

	// Own row still marks read fine.
	var ownRow int64
	if err := e.store.DB.QueryRow(`SELECT NotificationID FROM Notifications WHERE UserName='cashier' AND Status='sent' ORDER BY NotificationID DESC LIMIT 1`).Scan(&ownRow); err != nil {
		t.Fatalf("cashier inbox row: %v", err)
	}
	code, _, _ = e.postFormLoc(cashier, "/notifications/"+itoa64(ownRow)+"/read", url.Values{})
	if code != 303 {
		t.Fatalf("own mark-read: want 303, got %d", code)
	}
	if err := e.store.DB.QueryRow(`SELECT IsRead FROM Notifications WHERE NotificationID=?`, ownRow).Scan(&isRead); err != nil || isRead != 1 {
		t.Fatalf("own row IsRead = %d, err %v (want 1)", isRead, err)
	}
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// --- SW-L02: deny() answers 403 with the error page, no double WriteHeader ---

func TestFix12DenyStill403(t *testing.T) {
	e := newEnv(t, false)
	e.seedUsers()
	e.changeAdminPassword()
	cashier := e.cashierClient()
	code, body := e.get(cashier, "/settings")
	if code != 403 {
		t.Fatalf("cashier GET /settings: want 403, got %d", code)
	}
	if !strings.Contains(body, "غير مسموح") {
		t.Fatalf("403 page should carry the denial text, body=%q", truncate(body, 300))
	}
}

// --- SW-L13: form hint describes the implemented audience rule ---

func TestFix12NotifFormHint(t *testing.T) {
	e := newEnv(t, false)
	e.seedUsers()
	e.changeAdminPassword()
	c := e.adminClient()
	_, body := e.get(c, "/notifications/new")
	if strings.Contains(body, "العميل النقدي مستثنَد") {
		t.Fatal("form still carries the stale cash-customer-excluded hint")
	}
	if !strings.Contains(body, "العميل النقدي") {
		t.Fatalf("form hint should describe the cash-customer rule, body=%q", truncate(body, 500))
	}
}

// --- SW-L11 / SW-L12 via HTTP: linked product + body validation ---

func TestFix12NotifCreateValidationHTTP(t *testing.T) {
	e := newEnv(t, false)
	e.seedUsers()
	e.changeAdminPassword()
	c := e.adminClient()

	// price_drop without a linked product: refused.
	code, body := e.postForm(c, "/notifications/new", url.Values{
		"type": {"price_drop"}, "title": {"انخفاض"}, "body": {"نص"}, "audience": {"all"},
	})
	if code == 303 || !strings.Contains(body, "المنتج المرتبط") {
		t.Fatalf("price_drop without product: want refusal, got code %d body=%q", code, truncate(body, 300))
	}
	// Custom with an empty body: refused.
	code, body = e.postForm(c, "/notifications/new", url.Values{
		"type": {"custom"}, "title": {"بلا نص"}, "body": {""}, "audience": {"all"},
	})
	if code == 303 || !strings.Contains(body, "نص التنبيه") {
		t.Fatalf("custom without body: want refusal, got code %d body=%q", code, truncate(body, 300))
	}
	// Valid custom: 303.
	code, _, _ = e.postFormLoc(c, "/notifications/new", url.Values{
		"type": {"custom"}, "title": {"صحيح"}, "body": {"نص"}, "audience": {"all"},
	})
	if code != 303 {
		t.Fatalf("valid custom: want 303, got %d", code)
	}
}
