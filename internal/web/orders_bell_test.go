// Package web_test — staff online-orders bell (FIX-3 H1) end-to-end.
//
// Drives the real HTTP server: intake through POST /api/staff/orders,
// pending-count / pending-evidence as cashier (cookie) and as a JWT
// bearer, resolve roles, and 401 with no token at all.
package web_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (b *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req2 := req.Clone(req.Context())
	req2.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(req2)
}

// jwtClient logs in through POST /api/auth/login and returns a client
// that sends the issued token as a Bearer header (no cookie).
func jwtClient(t *testing.T, e *e2eEnv, username, password string) *http.Client {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	resp, err := http.Post(e.base+"/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/auth/login: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("api login %s: want 200, got %d body=%q", username, resp.StatusCode, truncate(string(b), 300))
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("api login: bad JSON: %v", err)
	}
	token, _ := out["token"].(string)
	if token == "" {
		t.Fatalf("api login: empty token in %v", out)
	}
	return &http.Client{Transport: &bearerTransport{token: token, base: http.DefaultTransport}}
}

// orderBody builds an intake payload: 2× سكر 2000 + 1× زيت 5000 = 9000.
func orderBody(customer, payStatus string) map[string]any {
	b := map[string]any{
		"customer_name": customer, "customer_phone": "01000000000",
		"items": []map[string]any{
			{"product_id": 1, "name": "سكر", "qty": "2", "unit_price": 2000},
			{"product_id": 2, "name": "زيت", "qty": "1", "unit_price": 5000},
		},
		"total":          9000,
		"payment_method": "Vodafone Cash",
	}
	if payStatus != "" {
		b["payment_status"] = payStatus
	}
	return b
}

func TestOrdersBell_CountsAndEvidence(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	cash := e.cashierClient()

	// Fresh store: zero pending, zero evidence (cookie session).
	if code, data := e.getJSON(cash, "/api/staff/orders/pending-count"); code != http.StatusOK || data["pendingCount"] != float64(0) {
		t.Fatalf("fresh pending-count: want 200 {0}, got %d %v", code, data)
	}
	if code, data := e.getJSON(cash, "/api/staff/orders/pending-evidence"); code != http.StatusOK || data["pendingEvidence"] != float64(0) {
		t.Fatalf("fresh pending-evidence: want 200 {0}, got %d %v", code, data)
	}

	// Intake one CREATED order as admin; cashier sees pending 1.
	code, data := e.postJSON(admin, "/api/staff/orders", orderBody("عميل أونلاين", ""))
	if code != http.StatusCreated || data["ok"] != true {
		t.Fatalf("intake: want 201 ok, got %d %v", code, data)
	}
	order, _ := data["order"].(map[string]any)
	orderID := int64(order["order_id"].(float64))
	if code, data := e.getJSON(cash, "/api/staff/orders/pending-count"); code != http.StatusOK || data["pendingCount"] != float64(1) {
		t.Fatalf("pending after 1 CREATED: want 1, got %d %v", code, data)
	}

	// A second order awaiting evidence: pending 2, evidence 1.
	code, data = e.postJSON(admin, "/api/staff/orders", orderBody("عميل دليل", "AWAITING_VERIFICATION"))
	if code != http.StatusCreated {
		t.Fatalf("intake evidence order: want 201, got %d %v", code, data)
	}
	if code, data := e.getJSON(cash, "/api/staff/orders/pending-count"); data["pendingCount"] != float64(2) {
		t.Fatalf("pending after evidence order: want 2, got %d %v", code, data)
	}
	if code, data := e.getJSON(cash, "/api/staff/orders/pending-evidence"); data["pendingEvidence"] != float64(1) {
		t.Fatalf("evidence count: want 1, got %d %v", code, data)
	}

	// Completing the first order drops it from pending (JWT read agrees).
	jwtCash := jwtClient(t, e, "cashier", cashierPass)
	code, data = e.postJSON(admin, "/api/staff/orders/"+itoa(orderID)+"/status", map[string]any{"status": "PREPARED"})
	if code != http.StatusOK {
		t.Fatalf("advance to PREPARED: want 200, got %d %v", code, data)
	}
	if code, data := e.getJSON(jwtCash, "/api/staff/orders/pending-count"); code != http.StatusOK || data["pendingCount"] != float64(2) {
		t.Fatalf("JWT pending after PREPARED: want 2, got %d %v", code, data)
	}
	for _, st := range []string{"READY", "DELIVERED", "COMPLETED"} {
		if code, data := e.postJSON(admin, "/api/staff/orders/"+itoa(orderID)+"/status", map[string]any{"status": st}); code != http.StatusOK {
			t.Fatalf("advance to %s: want 200, got %d %v", st, code, data)
		}
	}
	if code, data := e.getJSON(cash, "/api/staff/orders/pending-count"); data["pendingCount"] != float64(1) {
		t.Fatalf("pending after COMPLETED: want 1, got %d %v", code, data)
	}

	// The orders list shows both orders to the cashier.
	if code, data := e.getJSON(cash, "/api/staff/orders/all"); code != http.StatusOK {
		t.Fatalf("orders/all: want 200, got %d", code)
	} else if orders, _ := data["orders"].([]any); len(orders) != 2 {
		t.Fatalf("orders/all: want 2 orders, got %v", data)
	}
}

func TestOrdersBell_RolesAndAuth(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	cash := e.cashierClient()
	sup := e.supClient()
	anon := newClient(t, false)

	// No token at all -> 401 on every staff endpoint.
	for _, p := range []string{"/api/staff/orders/pending-count", "/api/staff/orders/pending-evidence", "/api/staff/orders/all"} {
		resp, err := anon.Get(e.base + p)
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("anonymous %s: want 401, got %d", p, resp.StatusCode)
		}
	}
	// A forged JWT is 401 too.
	forged := &http.Client{Transport: &bearerTransport{token: "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhZG1pbiJ9.bogus", base: http.DefaultTransport}}
	if code, _ := e.getJSON(forged, "/api/staff/orders/pending-count"); code != http.StatusUnauthorized {
		t.Fatalf("forged JWT: want 401, got %d", code)
	}
	// Wrong password -> 401 from the login endpoint.
	body, _ := json.Marshal(map[string]string{"username": "cashier", "password": "wrong"})
	resp, err := http.Post(e.base+"/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("api login wrong password: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("api login wrong password: want 401, got %d", resp.StatusCode)
	}

	// Cashier and supervisor may NOT create orders or resolve evidence.
	if code, data := e.postJSON(cash, "/api/staff/orders", orderBody("x", "")); code != http.StatusForbidden {
		t.Fatalf("cashier intake: want 403, got %d %v", code, data)
	}
	if code, data := e.postJSON(sup, "/api/staff/orders", orderBody("x", "")); code != http.StatusForbidden {
		t.Fatalf("supervisor intake: want 403, got %d %v", code, data)
	}

	// Evidence resolve: admin resolves, cashier gets 403, double resolve 400.
	code, data := e.postJSON(admin, "/api/staff/orders", orderBody("عميل دليل 2", "AWAITING_VERIFICATION"))
	if code != http.StatusCreated {
		t.Fatalf("setup evidence order: want 201, got %d %v", code, data)
	}
	order, _ := data["order"].(map[string]any)
	oid := itoa(int64(order["order_id"].(float64)))
	if code, data := e.postJSON(cash, "/api/staff/orders/"+oid+"/resolve-evidence", map[string]any{}); code != http.StatusForbidden {
		t.Fatalf("cashier resolve: want 403, got %d %v", code, data)
	}
	if code, data := e.postJSON(admin, "/api/staff/orders/"+oid+"/resolve-evidence", map[string]any{}); code != http.StatusOK {
		t.Fatalf("admin resolve: want 200, got %d %v", code, data)
	} else if o, _ := data["order"].(map[string]any); o["payment_status"] != "VERIFIED" {
		t.Fatalf("resolved order payment_status = %v, want VERIFIED", o["payment_status"])
	}
	if code, data := e.getJSON(cash, "/api/staff/orders/pending-evidence"); data["pendingEvidence"] != float64(0) {
		t.Fatalf("evidence after resolve: want 0, got %d %v", code, data)
	}
	if code, data := e.postJSON(admin, "/api/staff/orders/"+oid+"/resolve-evidence", map[string]any{}); code != http.StatusBadRequest {
		t.Fatalf("double resolve: want 400, got %d %v", code, data)
	}
	// HTML resolve form works for the manager too (303 back to /orders).
	mgr := e.managerClient()
	code2, data2 := e.postJSON(admin, "/api/staff/orders", orderBody("عميل دليل 3", "AWAITING_VERIFICATION"))
	if code2 != http.StatusCreated {
		t.Fatalf("setup third evidence order: want 201, got %d %v", code2, data2)
	}
	order3, _ := data2["order"].(map[string]any)
	oid3 := itoa(int64(order3["order_id"].(float64)))
	if code, loc, _ := e.postFormLoc(mgr, "/orders/"+oid3+"/resolve-evidence", nil); code != http.StatusSeeOther || !strings.HasPrefix(loc, "/orders") {
		t.Fatalf("manager HTML resolve: want 303 to /orders, got %d loc %q", code, loc)
	}
	if code, data := e.getJSON(cash, "/api/staff/orders/pending-evidence"); data["pendingEvidence"] != float64(0) {
		t.Fatalf("evidence after manager resolve: want 0, got %d %v", code, data)
	}
}

func TestOrdersBell_CanceledEvidenceOrderDropsOut(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	cash := e.cashierClient()

	// Intake an evidence-awaiting order: pending 1, evidence 1.
	code, data := e.postJSON(admin, "/api/staff/orders", orderBody("عميل دليل ملغى", "AWAITING_VERIFICATION"))
	if code != http.StatusCreated {
		t.Fatalf("intake evidence order: want 201, got %d %v", code, data)
	}
	order, _ := data["order"].(map[string]any)
	oid := itoa(int64(order["order_id"].(float64)))
	if code, data := e.getJSON(cash, "/api/staff/orders/pending-count"); data["pendingCount"] != float64(1) {
		t.Fatalf("pending before cancel: want 1, got %d %v", code, data)
	}
	if code, data := e.getJSON(cash, "/api/staff/orders/pending-evidence"); data["pendingEvidence"] != float64(1) {
		t.Fatalf("evidence before cancel: want 1, got %d %v", code, data)
	}

	// Cancel it: both bell counts must drop to 0 (FIX-5 R5-L3).
	if code, data := e.postJSON(admin, "/api/staff/orders/"+oid+"/status", map[string]any{"status": "CANCELED"}); code != http.StatusOK {
		t.Fatalf("cancel: want 200, got %d %v", code, data)
	}
	if code, data := e.getJSON(cash, "/api/staff/orders/pending-count"); data["pendingCount"] != float64(0) {
		t.Fatalf("pending after cancel: want 0, got %d %v", code, data)
	}
	if code, data := e.getJSON(cash, "/api/staff/orders/pending-evidence"); data["pendingEvidence"] != float64(0) {
		t.Fatalf("evidence after cancel: want 0, got %d %v", code, data)
	}

	// Resolving evidence on the cancelled order must refuse (400), not 200.
	if code, data := e.postJSON(admin, "/api/staff/orders/"+oid+"/resolve-evidence", map[string]any{}); code != http.StatusBadRequest {
		t.Fatalf("resolve on canceled order: want 400, got %d %v", code, data)
	}

	// Contrast: a COMPLETED order with awaiting payment still counts —
	// payment review is still owed on a delivered order.
	code, data = e.postJSON(admin, "/api/staff/orders", orderBody("عميل مكتمل بدليل", "AWAITING_VERIFICATION"))
	if code != http.StatusCreated {
		t.Fatalf("intake completed-candidate: want 201, got %d %v", code, data)
	}
	order2, _ := data["order"].(map[string]any)
	oid2 := itoa(int64(order2["order_id"].(float64)))
	for _, st := range []string{"PREPARED", "READY", "DELIVERED", "COMPLETED"} {
		if code, data := e.postJSON(admin, "/api/staff/orders/"+oid2+"/status", map[string]any{"status": st}); code != http.StatusOK {
			t.Fatalf("advance to %s: want 200, got %d %v", st, code, data)
		}
	}
	if code, data := e.getJSON(cash, "/api/staff/orders/pending-count"); data["pendingCount"] != float64(1) {
		t.Fatalf("pending after COMPLETED+awaiting: want 1, got %d %v", code, data)
	}
	if code, data := e.getJSON(cash, "/api/staff/orders/pending-evidence"); data["pendingEvidence"] != float64(1) {
		t.Fatalf("evidence after COMPLETED+awaiting: want 1, got %d %v", code, data)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
