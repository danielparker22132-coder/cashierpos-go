// Package web_test — end-to-end tests for CashierPOS Go.
//
// Every test here drives the real HTTP server (httptest) with a real
// http.Client + cookie jar against a fresh database per test. No store
// service function is called directly: setup data (users, a stale shift)
// is inserted with plain SQL, and verifications that the API cannot
// express (audit rows, auto-closed shifts) read the test database with
// plain SQL. Product/customer/vendor/transfer creation all go through
// HTTP, exactly like a real client.
//
// Money is int64 cents in JSON (amount_cents / unit_price), quantities
// are "1.000"-style strings parsed by money.ParseQty ("2" == 2 units).
// Form amounts are EGP decimals ("10.00").
//
// Run: go test -timeout 30m -count=1 ./internal/web/ -v
package web_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cashierpos-go/internal/store"
	"cashierpos-go/internal/web"
)

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// Known test passwords (seeded users are inserted by SQL, see seedUsers).
const (
	adminNewPass = "AdminNew123"
	managerPass  = "MgrPass123"
	cashierPass  = "CashPass123"
	supPass      = "SupPass123"
	whPass       = "WhPass123"
)

// e2eEnv is one isolated server + database.
type e2eEnv struct {
	t      *testing.T
	srv    *httptest.Server
	store  *store.Store
	base   string
	userID map[string]int64 // role -> Users.UserID
}

func sha256Hex(pw string) string {
	h := sha256.Sum256([]byte(pw))
	return hex.EncodeToString(h[:])
}

// newEnv builds a fresh server. When fileDB is true the database is a real
// file under t.TempDir() (required for backup tests: :memory: cannot be
// backed up); otherwise it is store.OpenTest() (:memory:).
func newEnv(t *testing.T, fileDB bool) *e2eEnv {
	t.Helper()
	var st *store.Store
	var err error
	if fileDB {
		path := filepath.Join(t.TempDir(), "cashierpos.db")
		st, err = store.Open(path)
	} else {
		st, err = store.OpenTest()
	}
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv, err := web.New(st)
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &e2eEnv{t: t, srv: ts, store: st, base: ts.URL, userID: map[string]int64{}}
}

// seedUsers inserts manager/cashier/supervisor/warehouse directly with SQL
// (SHA-256 hex passwords, the same scheme auth uses). The seeded admin
// (admin/admin123, MustChangePassword=1) is left untouched here; call
// changeAdminPassword first when the test needs a working admin login.
func (e *e2eEnv) seedUsers() {
	e.t.Helper()
	users := []struct{ name, pass, role string }{
		{"manager", managerPass, "manager"},
		{"cashier", cashierPass, "cashier"},
		{"supervisor", supPass, "supervisor"},
		{"warehouse", whPass, "warehouse"},
	}
	for _, u := range users {
		res, err := e.store.DB.Exec(
			`INSERT INTO Users(UserName,Password,FullName,Role,IsActive,MustChangePassword,CreatedDate)
			 VALUES(?,?,?, ?,1,0,?)`,
			u.name, sha256Hex(u.pass), u.name, u.role, store.Today())
		if err != nil {
			e.t.Fatalf("seed user %s: %v", u.name, err)
		}
		id, _ := res.LastInsertId()
		e.userID[u.role] = id
	}
}

// newClient returns an http.Client with a cookie jar. When follow is false
// redirects are not followed, so tests can assert the Location header.
func newClient(t *testing.T, follow bool) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	c := &http.Client{Jar: jar}
	if !follow {
		c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}
	return c
}

// loginPOST posts the login form with a no-redirect client and returns the
// status, Location header and response body (the session cookie lands in
// the client's jar). Callers must pass a no-redirect client: a following
// client would chase the 303 and report 200 with the hub body instead.
func (e *e2eEnv) loginPOST(c *http.Client, username, password string) (int, string, string) {
	e.t.Helper()
	resp, err := c.PostForm(e.base+"/login", url.Values{
		"username": {username}, "password": {password},
	})
	if err != nil {
		e.t.Fatalf("POST /login: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Location"), string(b)
}

// loginAs logs in and returns a following client holding the session.
// It fails the test unless the hub (GET /) answers 200 afterwards.
func (e *e2eEnv) loginAs(username, password string) *http.Client {
	e.t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.t.Fatalf("cookiejar: %v", err)
	}
	probe := &http.Client{Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	st, loc, body := e.loginPOST(probe, username, password)
	if st != http.StatusSeeOther {
		e.t.Fatalf("login %s: want 303, got %d (loc %q) body=%q", username, st, loc, truncate(body, 300))
	}
	c := &http.Client{Jar: jar}
	code, body := e.get(c, "/")
	if code != http.StatusOK {
		e.t.Fatalf("login %s: GET / after login = %d body=%q", username, code, truncate(body, 300))
	}
	return c
}

func (e *e2eEnv) adminClient() *http.Client     { return e.loginAs("admin", adminNewPass) }
func (e *e2eEnv) managerClient() *http.Client   { return e.loginAs("manager", managerPass) }
func (e *e2eEnv) cashierClient() *http.Client   { return e.loginAs("cashier", cashierPass) }
func (e *e2eEnv) supClient() *http.Client       { return e.loginAs("supervisor", supPass) }
func (e *e2eEnv) warehouseClient() *http.Client { return e.loginAs("warehouse", whPass) }

// changeAdminPassword performs the forced admin/admin123 -> new password
// flow over HTTP and asserts every step. After it returns, admin logs in
// with adminNewPass.
func (e *e2eEnv) changeAdminPassword() {
	e.t.Helper()
	c := newClient(e.t, false)
	st, loc, _ := e.loginPOST(c, "admin", "admin123")
	if st != http.StatusSeeOther || loc != "/change-password?forced=1" {
		e.t.Fatalf("admin/admin123 login: want 303 to /change-password?forced=1, got %d loc %q", st, loc)
	}
	// Before the change, every protected page redirects to change-password.
	resp, err := c.Get(e.base + "/sales")
	if err != nil {
		e.t.Fatalf("GET /sales pre-change: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/change-password?forced=1" {
		e.t.Fatalf("GET /sales pre-change: want 303 to /change-password, got %d loc %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	code, body := e.postForm(c, "/change-password", url.Values{
		"current": {"admin123"}, "new": {adminNewPass}, "confirm": {adminNewPass},
	})
	if code != http.StatusSeeOther {
		e.t.Fatalf("POST /change-password: want 303, got %d body=%q", code, truncate(body, 300))
	}
	// The admin row must no longer force a change.
	var must int
	if err := e.store.DB.QueryRow(`SELECT MustChangePassword FROM Users WHERE UserName='admin'`).Scan(&must); err != nil || must != 0 {
		e.t.Fatalf("MustChangePassword after change = %d, err %v", must, err)
	}
}

// get returns status + body for a GET with a following client.
func (e *e2eEnv) get(c *http.Client, path string) (int, string) {
	e.t.Helper()
	resp, err := c.Get(e.base + path)
	if err != nil {
		e.t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (e *e2eEnv) getBody(c *http.Client, path string) string {
	e.t.Helper()
	_, b := e.get(c, path)
	return b
}

// noRedirect returns a client sharing c's cookie jar that does NOT follow
// redirects, so form POSTs report their real 303 + Location instead of
// the page the redirect lands on.
func noRedirect(c *http.Client) *http.Client {
	return &http.Client{Jar: c.Jar, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

// postForm posts a form with a no-redirect client; returns status + body.
// (Most mutating HTML handlers answer 303; the Location is in the body
// only via the caller reading resp headers — see postFormLoc.)
func (e *e2eEnv) postForm(c *http.Client, path string, v url.Values) (int, string) {
	e.t.Helper()
	resp, err := noRedirect(c).PostForm(e.base+path, v)
	if err != nil {
		e.t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// postFormLoc is postForm that also returns the redirect Location.
func (e *e2eEnv) postFormLoc(c *http.Client, path string, v url.Values) (int, string, string) {
	e.t.Helper()
	resp, err := noRedirect(c).PostForm(e.base+path, v)
	if err != nil {
		e.t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Location"), string(b)
}

// postJSON posts a JSON body; returns status + decoded object.
func (e *e2eEnv) postJSON(c *http.Client, path string, body any) (int, map[string]any) {
	e.t.Helper()
	var buf bytes.Buffer
	if body == nil {
		buf.WriteString("{}")
	} else if err := json.NewEncoder(&buf).Encode(body); err != nil {
		e.t.Fatalf("encode JSON for %s: %v", path, err)
	}
	req, err := http.NewRequest(http.MethodPost, e.base+path, &buf)
	if err != nil {
		e.t.Fatalf("new request %s: %v", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		e.t.Fatalf("POST %s: status %d, body is not JSON: %q", path, resp.StatusCode, truncate(string(b), 300))
	}
	return resp.StatusCode, out
}

// getJSON GETs a JSON API endpoint.
func (e *e2eEnv) getJSON(c *http.Client, path string) (int, map[string]any) {
	e.t.Helper()
	resp, err := c.Get(e.base + path)
	if err != nil {
		e.t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		e.t.Fatalf("GET %s: status %d, body is not JSON: %q", path, resp.StatusCode, truncate(string(b), 300))
	}
	return resp.StatusCode, out
}

// expectOK fails unless the API call succeeded (ok=true, 200).
func expectOK(t *testing.T, what string, code int, body map[string]any) {
	t.Helper()
	if code != http.StatusOK || body["ok"] != true {
		t.Fatalf("%s: want 200 ok=true, got %d body=%v", what, code, body)
	}
}

// expectDenied fails unless the call was refused: API 400/403 with
// ok=false, or an HTML page answering 403.
func expectDenied(t *testing.T, what string, code int, body map[string]any) {
	t.Helper()
	if code != http.StatusBadRequest && code != http.StatusForbidden {
		t.Fatalf("%s: want denial (400/403), got %d body=%v", what, code, body)
	}
	if body["ok"] != false {
		t.Fatalf("%s: want ok=false, got body=%v", what, body)
	}
}

// queryInt runs a scalar SQL query against the test database.
func (e *e2eEnv) queryInt(query string, args ...any) int64 {
	e.t.Helper()
	var n int64
	if err := e.store.DB.QueryRow(query, args...).Scan(&n); err != nil {
		e.t.Fatalf("query %q: %v", query, err)
	}
	return n
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func cairoNow() time.Time {
	loc, err := time.LoadLocation("Africa/Cairo")
	if err != nil {
		return time.Now()
	}
	return time.Now().In(loc)
}

// ---------------------------------------------------------------------------
// shared builders (all over HTTP)
// ---------------------------------------------------------------------------

// createProduct creates a product through the HTML form as the given client.
// Returns the product id parsed from the redirect Location /products/{id}.
func (e *e2eEnv) createProduct(c *http.Client, barcode, name, retail, qty string) int64 {
	e.t.Helper()
	code, loc, body := e.postFormLoc(c, "/products/new", url.Values{
		"name":            {name},
		"barcode":         {barcode},
		"buy_price":       {"5.00"},
		"bulk_price":      {"7.00"},
		"wholesale_price": {"8.00"},
		"retail_price":    {retail},
		"quantity":        {qty},
		"min_quantity":    {"1"},
		"unit":            {"قطعة"},
	})
	if code != http.StatusSeeOther {
		e.t.Fatalf("create product %s: want 303, got %d body=%q", name, code, truncate(body, 300))
	}
	idStr := strings.TrimPrefix(loc, "/products/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id == 0 {
		e.t.Fatalf("create product %s: bad redirect %q", name, loc)
	}
	return id
}

// openShift opens a shift through the API; returns the shift id.
func (e *e2eEnv) openShift(c *http.Client, openingCents int64) int64 {
	e.t.Helper()
	code, body := e.postJSON(c, "/api/shifts/open", map[string]any{"opening_cents": openingCents})
	expectOK(e.t, "open shift", code, body)
	id, ok := body["shift_id"].(float64)
	if !ok || int64(id) == 0 {
		e.t.Fatalf("open shift: no shift_id in %v", body)
	}
	return int64(id)
}

// sell posts one invoice through the API. Returns status + body.
func (e *e2eEnv) sell(c *http.Client, req map[string]any) (int, map[string]any) {
	e.t.Helper()
	return e.postJSON(c, "/api/sales/create", req)
}

// cashSale is a one-line cash sale of qty units of productID at its default
// retail price; returns the invoice id + number on success.
func (e *e2eEnv) cashSale(c *http.Client, productID int64, qty string, paidCents int64) (int64, int64) {
	e.t.Helper()
	code, body := e.sell(c, map[string]any{
		"sale_type": "retail",
		"lines": []map[string]any{
			{"product_id": productID, "qty": qty},
		},
		"paid":           paidCents,
		"payment_method": "Cash",
	})
	expectOK(e.t, "cash sale", code, body)
	invID, _ := body["invoice_id"].(float64)
	num, _ := body["invoice_number"].(float64)
	return int64(invID), int64(num)
}

// readyEnv is the common starting point for most tests: fresh server,
// users seeded, admin password changed.
func readyEnv(t *testing.T, fileDB bool) *e2eEnv {
	t.Helper()
	e := newEnv(t, fileDB)
	e.changeAdminPassword()
	e.seedUsers()
	return e
}

// ---------------------------------------------------------------------------
// 1. Login — all roles, wrong password, anonymous, forced change
// ---------------------------------------------------------------------------

func TestE2E_LoginAllRoles(t *testing.T) {
	e := readyEnv(t, false)
	roles := []struct{ user, pass string }{
		{"admin", adminNewPass},
		{"manager", managerPass},
		{"cashier", cashierPass},
		{"supervisor", supPass},
		{"warehouse", whPass},
	}
	for _, r := range roles {
		t.Run(r.user, func(t *testing.T) {
			c := e.loginAs(r.user, r.pass)
			code, body := e.get(c, "/")
			if code != http.StatusOK {
				t.Fatalf("GET / as %s: want 200, got %d", r.user, code)
			}
			if !strings.Contains(body, r.user) && !strings.Contains(body, "الرئيسية") {
				t.Fatalf("GET / as %s: hub body does not look like the hub: %q", r.user, truncate(body, 200))
			}
		})
	}
}

func TestE2E_LoginWrongPasswordRejected(t *testing.T) {
	e := readyEnv(t, false)
	c := newClient(t, false)
	code, _, body := e.loginPOST(c, "cashier", "wrong-password")
	if code != http.StatusOK {
		t.Fatalf("wrong password: want 200 (login page re-rendered with error), got %d", code)
	}
	// Generic failure message (L-01: no user enumeration), rendered in the
	// POST response itself — a fresh GET /login shows an empty form.
	if !strings.Contains(body, "اسم المستخدم أو كلمة المرور غير صحيحة") {
		t.Fatalf("wrong password: generic error message missing: %q", truncate(body, 300))
	}
	// No session was created: the hub still redirects to /login.
	code, _, _ = e.loginPOST(newClient(t, false), "no-such-user", "whatever123")
	if code != http.StatusOK {
		t.Fatalf("unknown user: want 200 login page, got %d", code)
	}
	anon := newClient(t, false)
	resp, err := anon.Get(e.base + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
		t.Fatalf("after failed login, GET /: want 303 to /login, got %d loc %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestE2E_AnonymousRedirectedToLogin(t *testing.T) {
	e := newEnv(t, false)
	anon := newClient(t, false)
	for _, p := range []string{"/", "/sales", "/products", "/shifts"} {
		resp, err := anon.Get(e.base + p)
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
			t.Fatalf("anonymous GET %s: want 303 to /login, got %d loc %q", p, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	// Anonymous API calls get 401 JSON, not a redirect.
	resp, err := anon.Get(e.base + "/api/shifts/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous GET /api/shifts/status: want 401, got %d body=%q", resp.StatusCode, truncate(string(b), 200))
	}
	if !strings.Contains(string(b), "يلزم تسجيل الدخول") {
		t.Fatalf("anonymous API body: %q", truncate(string(b), 200))
	}
}

func TestE2E_AdminForcedPasswordChange(t *testing.T) {
	e := newEnv(t, false) // do NOT change the password yet
	c := newClient(t, false)
	// 1. admin/admin123 logs in but is forced to change-password.
	st, loc, _ := e.loginPOST(c, "admin", "admin123")
	if st != http.StatusSeeOther || loc != "/change-password?forced=1" {
		t.Fatalf("admin/admin123: want 303 to /change-password?forced=1, got %d loc %q", st, loc)
	}
	// 2. No protected page opens before the change (hub + sales).
	for _, p := range []string{"/", "/sales", "/products"} {
		resp, err := c.Get(e.base + p)
		if err != nil {
			t.Fatalf("GET %s pre-change: %v", p, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/change-password?forced=1" {
			t.Fatalf("GET %s pre-change: want 303 to /change-password, got %d loc %q", p, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	// 3. The change-password page itself opens and explains the force.
	// (The same no-redirect client carries the session cookie; the
	// change-password page is exempt from the forced redirect.)
	code, body := e.get(c, "/change-password?forced=1")
	if code != http.StatusOK || !strings.Contains(body, "يجب تغيير كلمة السر") {
		t.Fatalf("GET /change-password: want 200 with forced-change notice, got %d body=%q", code, truncate(body, 300))
	}
	// 4. Wrong current password is refused.
	code, body = e.postForm(c, "/change-password", url.Values{
		"current": {"not-the-password"}, "new": {"Xyz12345"}, "confirm": {"Xyz12345"},
	})
	if code != http.StatusOK || !strings.Contains(body, "كلمة السر الحالية غير صحيحة") {
		t.Fatalf("change with wrong current: want 200 + error, got %d body=%q", code, truncate(body, 300))
	}
	// 5. Correct change succeeds; the hub opens afterwards.
	code, _, _ = e.postFormLoc(c, "/change-password", url.Values{
		"current": {"admin123"}, "new": {adminNewPass}, "confirm": {adminNewPass},
	})
	if code != http.StatusSeeOther {
		t.Fatalf("correct change: want 303, got %d", code)
	}
	code, _ = e.get(c, "/")
	if code != http.StatusOK {
		t.Fatalf("GET / after change: want 200, got %d", code)
	}
	// 6. The old password no longer works.
	st, _, _ = e.loginPOST(newClient(t, false), "admin", "admin123")
	if st == http.StatusSeeOther {
		t.Fatal("old password admin123 still logs in after the change")
	}
}

// ---------------------------------------------------------------------------
// 2. Permission denials (HTTP) + Manager = Admin
// ---------------------------------------------------------------------------

func TestE2E_CashierDeniedPages(t *testing.T) {
	e := readyEnv(t, false)
	c := e.cashierClient()
	// §15: the cashier holds none of these. Actual behaviour (documented):
	// RequirePerm -> deny() renders error.html with HTTP 403.
	denied := []string{"/users", "/reports", "/audit", "/settings", "/promotions/new"}
	for _, p := range denied {
		code, body := e.get(c, p)
		if code != http.StatusForbidden {
			t.Errorf("cashier GET %s: want 403, got %d body=%q", p, code, truncate(body, 200))
			continue
		}
		if !strings.Contains(body, "غير مسموح") {
			t.Errorf("cashier GET %s: 403 body lacks the denial message: %q", p, truncate(body, 200))
		}
	}
}

func TestE2E_CashierCannotRejectTransfer(t *testing.T) {
	e := readyEnv(t, false)
	mgr := e.managerClient()
	// Manager records a transfer (view_transfers covers creation).
	code, body := e.postJSON(mgr, "/api/transfers", map[string]any{
		"sender_name": "عميل تحويل", "amount_cents": 5000,
		"method": "Vodafone Cash", "reference": "E2E-1",
	})
	expectOK(t, "manager create transfer", code, body)
	tid, _ := body["transfer_id"].(float64)
	if tid == 0 {
		t.Fatalf("no transfer_id in %v", body)
	}
	// Cashier may approve transfers (§15 row 69) but must NOT reject them.
	cash := e.cashierClient()
	code, body = e.postJSON(cash, fmt.Sprintf("/api/transfers/%d/reject", int64(tid)), map[string]any{"note": "رفض تجريبي"})
	if code != http.StatusForbidden || body["ok"] != false {
		t.Fatalf("cashier reject transfer: want 403 ok=false, got %d body=%v", code, body)
	}
	// The transfer is still pending (the rejection did not land).
	code, body = e.getJSON(mgr, "/api/transfers")
	expectOK(t, "list transfers", code, body)
	found := false
	if list, ok := body["transfers"].([]any); ok {
		for _, item := range list {
			if m, ok := item.(map[string]any); ok && int64(m["ID"].(float64)) == int64(tid) {
				found = true
				if m["Status"] == "rejected" {
					t.Fatal("transfer shows rejected after the refused cashier attempt")
				}
			}
		}
	}
	if !found {
		t.Logf("note: transfer %d not found in list payload shape %v (rejection itself was still refused)", int64(tid), body["transfers"])
	}
}

func TestE2E_SupervisorCannotCancelInvoice(t *testing.T) {
	e := readyEnv(t, false)
	mgr := e.managerClient()
	pid := e.createProduct(mgr, "E2E-SUP-1", "منتج مشرف", "10.00", "10")
	// Supervisor CAN sell (create_invoice) but must NOT cancel.
	sup := e.supClient()
	e.openShift(sup, 10000)
	invID, _ := e.cashSale(sup, pid, "1", 1000)
	code, body := e.postJSON(sup, fmt.Sprintf("/api/sales/%d/cancel", invID), map[string]any{"reason": "إلغاء تجريبي"})
	if code != http.StatusForbidden || body["ok"] != false {
		t.Fatalf("supervisor cancel invoice: want 403 ok=false, got %d body=%v", code, body)
	}
	// The invoice is still live.
	code, body = e.getJSON(sup, fmt.Sprintf("/api/sales/%d", invID))
	expectOK(t, "get invoice after refused cancel", code, body)
	if body["is_canceled"] == true {
		t.Fatal("invoice was canceled despite the supervisor refusal")
	}
}

func TestE2E_WarehouseCannotCreateInvoice(t *testing.T) {
	e := readyEnv(t, false)
	mgr := e.managerClient()
	pid := e.createProduct(mgr, "E2E-WH-1", "منتج مخزن", "10.00", "10")
	wh := e.warehouseClient()
	// Warehouse has no create_invoice and no open_shift.
	code, body := e.sell(wh, map[string]any{
		"sale_type": "retail",
		"lines":     []map[string]any{{"product_id": pid, "qty": "1"}},
		"paid":      1000, "payment_method": "Cash",
	})
	if code != http.StatusForbidden || body["ok"] != false {
		t.Fatalf("warehouse create invoice: want 403 ok=false, got %d body=%v", code, body)
	}
	// FIX-17 SW2-L10 (§15 row 25): warehouse now reads the invoice list
	// (read-only, no POS form) — it still cannot CREATE (403 above).
	code, html := e.get(wh, "/sales")
	if code != http.StatusOK {
		t.Fatalf("warehouse GET /sales: want 200 (read-only list), got %d body=%q", code, truncate(html, 200))
	}
	if !strings.Contains(html, "فواتير اليوم") || strings.Contains(html, "فاتورة جديدة") {
		t.Fatalf("warehouse GET /sales: want read-only invoice list, got body=%q", truncate(html, 300))
	}
	// Warehouse CAN view products and count stocktake (positive control).
	code, _ = e.get(wh, "/products")
	if code != http.StatusOK {
		t.Fatalf("warehouse GET /products: want 200, got %d", code)
	}
}

func TestE2E_ManagerEqualsAdmin(t *testing.T) {
	e := readyEnv(t, false)
	mgr := e.managerClient()
	// (a) Manager creates a product (manage_products — admin-only in §15
	// except for the Manager=Admin rule).
	pid := e.createProduct(mgr, "E2E-MGR-1", "منتج مدير", "12.00", "10")
	// (b) Manager sells and cancels an invoice (cancel_invoice).
	e.openShift(mgr, 10000)
	invID, num := e.cashSale(mgr, pid, "1", 1200)
	code, body := e.postJSON(mgr, fmt.Sprintf("/api/sales/%d/cancel", invID), map[string]any{"reason": "إلغاء من المدير"})
	expectOK(t, "manager cancel invoice", code, body)
	if got := e.queryInt(`SELECT IsCanceled FROM Invoices WHERE InvoiceID=?`, invID); got != 1 {
		t.Fatalf("invoice %d (number %d) not canceled in DB after manager cancel", invID, num)
	}
	// (c) Manager manages users (manage_users).
	code, loc, htmlBody := e.postFormLoc(mgr, "/users/new", url.Values{
		"username": {"e2e_newcash"}, "password": {"NewCash123"},
		"full_name": {"كاشير جديد"}, "role": {"cashier"}, "is_active": {"1"},
	})
	if code != http.StatusSeeOther || loc != "/users" {
		t.Fatalf("manager create user: want 303 to /users, got %d loc %q body=%q", code, loc, truncate(htmlBody, 300))
	}
	if got := e.queryInt(`SELECT COUNT(*) FROM Users WHERE UserName='e2e_newcash' AND Role='cashier'`); got != 1 {
		t.Fatal("user e2e_newcash not in DB after manager create")
	}
	// The user the manager created can actually log in.
	_ = e.loginAs("e2e_newcash", "NewCash123")
	// The manager can open the users page (200) and the created user
	// must show in the table (FIX-1: users_list.html now ranges over
	// .Data, so the table renders).
	codeU, bodyU := e.get(mgr, "/users")
	if codeU != http.StatusOK {
		t.Fatalf("manager GET /users: want 200, got %d", codeU)
	}
	if !strings.Contains(bodyU, "e2e_newcash") {
		t.Fatalf("manager GET /users: body lacks the created user %q: %q", "e2e_newcash", truncate(bodyU, 300))
	}
}

// ---------------------------------------------------------------------------
// 3. Full sale cycle
// ---------------------------------------------------------------------------

func TestE2E_FullSaleCycle(t *testing.T) {
	e := readyEnv(t, false)
	mgr := e.managerClient()
	cash := e.cashierClient()

	// Manager creates the product (barcode search target).
	pid := e.createProduct(mgr, "E2E-100", "منتج دورة البيع", "10.00", "20")

	// Cashier opens a shift with an opening balance.
	e.openShift(cash, 10000)

	// Barcode lookup finds the product.
	code, body := e.getJSON(cash, "/api/products/barcode/E2E-100")
	expectOK(t, "barcode lookup", code, body)
	prod, _ := body["product"].(map[string]any)
	if prod == nil || prod["name"] != "منتج دورة البيع" {
		t.Fatalf("barcode lookup: unexpected product %v", body)
	}
	if prod["quantity"] != float64(20000) { // 20 units x 1000 milli
		t.Fatalf("barcode lookup: quantity = %v, want 20000 milli", prod["quantity"])
	}

	// Invoice #1 (cash) then #2 — sequential invoice numbers 1, 2.
	inv1, num1 := e.cashSale(cash, pid, "2", 2000)
	if num1 != 1 {
		t.Fatalf("first invoice number = %d, want 1", num1)
	}
	inv2, num2 := e.cashSale(cash, pid, "1", 1000)
	if num2 != 2 {
		t.Fatalf("second invoice number = %d, want 2", num2)
	}
	// Stock dropped by 3 units (20 -> 17).
	code, body = e.getJSON(cash, fmt.Sprintf("/api/products/%d", pid))
	expectOK(t, "get product after sales", code, body)
	if p, _ := body["product"].(map[string]any); p["quantity"] != float64(17000) {
		t.Fatalf("stock after 2 sales: quantity = %v, want 17000 milli", p["quantity"])
	}

	// Manager creates a customer with a 500 EGP credit limit.
	code, body = e.postJSON(mgr, "/api/customers", map[string]any{
		"name": "عميل آجل", "phone": "01000000001", "credit_limit": "500.00",
	})
	expectOK(t, "create customer", code, body)
	custID, _ := body["customer_id"].(float64)
	if custID == 0 {
		t.Fatalf("no customer_id in %v", body)
	}

	// Credit sale: 3 units, paid 0 -> remaining 3000 cents = customer debt.
	code, body = e.sell(cash, map[string]any{
		"sale_type": "retail", "customer_id": int64(custID),
		"lines": []map[string]any{{"product_id": pid, "qty": "3"}},
		"paid":  0, "payment_method": "Cash",
	})
	expectOK(t, "credit sale", code, body)
	inv3, _ := body["invoice_id"].(float64)
	if body["remaining"] != float64(3000) {
		t.Fatalf("credit sale remaining = %v, want 3000", body["remaining"])
	}
	// Customer balance reflects the debt (live balance via API).
	code, body = e.getJSON(mgr, fmt.Sprintf("/api/customers/%d", int64(custID)))
	expectOK(t, "get customer", code, body)
	cust, _ := body["customer"].(map[string]any)
	if cust == nil || cust["Balance"] != float64(3000) {
		t.Fatalf("customer balance = %v, want 3000", cust)
	}

	// Invoice discount: allowed for the manager, refused for the cashier.
	e.openShift(mgr, 10000)
	code, body = e.sell(mgr, map[string]any{
		"sale_type":        "retail",
		"lines":            []map[string]any{{"product_id": pid, "qty": "1"}},
		"invoice_discount": 500, "paid": 500, "payment_method": "Cash",
	})
	expectOK(t, "manager discounted sale", code, body)
	if body["net"] != float64(500) {
		t.Fatalf("manager discounted sale net = %v, want 500", body["net"])
	}
	code, body = e.sell(cash, map[string]any{
		"sale_type":        "retail",
		"lines":            []map[string]any{{"product_id": pid, "qty": "1"}},
		"invoice_discount": 500, "paid": 500, "payment_method": "Cash",
	})
	expectDenied(t, "cashier invoice discount", code, body)

	// Partial return: 1 unit of the credit invoice, cash refund 1000.
	// (Returns need do_return — the manager does it on his own shift.)
	code, body = e.postJSON(mgr, "/api/returns", map[string]any{
		"invoice_id": int64(inv3),
		"lines":      []map[string]any{{"product_id": pid, "qty": "1"}},
		"reason":     "مرتجع جزئي تجريبي", "method": "Cash",
	})
	expectOK(t, "partial return", code, body)
	if body["total_refund_cents"] != float64(1000) {
		t.Fatalf("partial return refund = %v, want 1000", body["total_refund_cents"])
	}
	// Stock is back up by the returned unit (17 - 1 manager sale - 1
	// discounted... recount: 20 -2 -1 -3 -1(mgr) = 13, +1 return = 14).
	code, body = e.getJSON(cash, fmt.Sprintf("/api/products/%d", pid))
	expectOK(t, "get product after return", code, body)
	if p, _ := body["product"].(map[string]any); p["quantity"] != float64(14000) {
		t.Fatalf("stock after return: quantity = %v, want 14000 milli", p["quantity"])
	}

	// Cancel: reason is mandatory; a canceled invoice cannot cancel again.
	code, body = e.postJSON(mgr, fmt.Sprintf("/api/sales/%d/cancel", inv2), map[string]any{"reason": ""})
	expectDenied(t, "cancel without reason", code, body)
	code, body = e.postJSON(mgr, fmt.Sprintf("/api/sales/%d/cancel", inv2), map[string]any{"reason": "إلغاء تجريبي"})
	expectOK(t, "manager cancel invoice", code, body)
	if got := e.queryInt(`SELECT IsCanceled FROM Invoices WHERE InvoiceID=?`, inv2); got != 1 {
		t.Fatal("invoice 2 not canceled in DB")
	}
	code, body = e.postJSON(mgr, fmt.Sprintf("/api/sales/%d/cancel", inv2), map[string]any{"reason": "مرة ثانية"})
	expectDenied(t, "cancel already-canceled invoice", code, body)
	// The untouched first invoice is still live.
	if got := e.queryInt(`SELECT IsCanceled FROM Invoices WHERE InvoiceID=?`, inv1); got != 0 {
		t.Fatal("invoice 1 was unexpectedly canceled")
	}
}

// ---------------------------------------------------------------------------
// 4. No negatives anywhere
// ---------------------------------------------------------------------------

func TestE2E_NegativeBlocked(t *testing.T) {
	e := readyEnv(t, false)
	mgr := e.managerClient()
	cash := e.cashierClient()
	pid := e.createProduct(mgr, "E2E-NEG-1", "منتج سالب", "10.00", "10")
	e.openShift(cash, 10000) // drawer = 100.00 EGP

	t.Run("cashbox-out-over-drawer", func(t *testing.T) {
		code, body := e.postJSON(cash, "/api/cashbox/out", map[string]any{
			"amount_cents": 20000, "reason": "سحب يتجاوز الدرج",
		})
		expectDenied(t, "cashbox out over drawer", code, body)
		// Drawer is untouched: still exactly the opening balance.
		if got := e.queryInt(`SELECT COALESCE(SUM(CASE WHEN MovementType='in' THEN Amount WHEN MovementType='out' THEN -Amount ELSE 0 END),0) FROM CashBox WHERE ShiftID IN (SELECT ShiftID FROM Shifts WHERE UserID=? AND IsOpen=1)`, e.userID["cashier"]); got != 0 {
			t.Fatalf("cashbox movements after refused out = %d, want 0", got)
		}
	})

	t.Run("sell-over-stock", func(t *testing.T) {
		code, body := e.sell(cash, map[string]any{
			"sale_type": "retail",
			"lines":     []map[string]any{{"product_id": pid, "qty": "11"}},
			"paid":      11000, "payment_method": "Cash",
		})
		expectDenied(t, "sell 11 of 10 in stock", code, body)
		if got := e.queryInt(`SELECT Quantity FROM Products WHERE ProductID=?`, pid); got != 10000 {
			t.Fatalf("stock after refused oversell = %d milli, want 10000", got)
		}
	})

	t.Run("expense-over-drawer", func(t *testing.T) {
		code, body := e.postJSON(cash, "/api/expenses", map[string]any{
			"amount_cents": 20000, "description": "مصروف يتجاوز الدرج", "category": "عام",
		})
		expectDenied(t, "expense over drawer", code, body)
		if got := e.queryInt(`SELECT COUNT(*) FROM Expenses`); got != 0 {
			t.Fatalf("expenses after refused expense = %d, want 0", got)
		}
	})

	t.Run("vendor-pay-over-balance", func(t *testing.T) {
		// Vendor payments post cash to the drawer, so the manager needs
		// his own open shift first — otherwise the refusal would be the
		// missing-shift error, not the over-balance rule.
		e.openShift(mgr, 10000)
		// Vendor with a 25.00 EGP purchase on credit (paid 0).
		code, body := e.postJSON(mgr, "/api/vendors", map[string]any{"name": "مورد تجريبي"})
		expectOK(t, "create vendor", code, body)
		vid, _ := body["vendor_id"].(float64)
		code, body = e.postJSON(mgr, "/api/purchases", map[string]any{
			"vendor_id":  int64(vid),
			"lines":      []map[string]any{{"product_id": pid, "qty": "5", "price": "5.00"}},
			"paid_cents": 0, "method": "Cash",
		})
		expectOK(t, "create purchase on credit", code, body)
		if got := e.queryInt(`SELECT CurrentBalance FROM Vendors WHERE VendorID=?`, int64(vid)); got != 2500 {
			t.Fatalf("vendor balance after purchase = %d, want 2500", got)
		}
		// Paying 30.00 against a 25.00 debt is refused — and the refusal
		// must cite the vendor balance, not the missing shift.
		code, body = e.postJSON(mgr, fmt.Sprintf("/api/vendors/%d/pay", int64(vid)), map[string]any{
			"amount_cents": 3000, "method": "Cash",
		})
		expectDenied(t, "vendor pay over balance", code, body)
		if !strings.Contains(body["error"].(string), "مديونية المورد") {
			t.Fatalf("vendor over-pay error does not cite the vendor debt: %v", body)
		}
		// Paying the exact balance succeeds and zeroes the debt.
		code, body = e.postJSON(mgr, fmt.Sprintf("/api/vendors/%d/pay", int64(vid)), map[string]any{
			"amount_cents": 2500, "method": "Cash",
		})
		expectOK(t, "vendor pay exact balance", code, body)
		if body["balance_after_cents"] != float64(0) {
			t.Fatalf("vendor balance after exact pay = %v, want 0", body["balance_after_cents"])
		}
	})
}

// ---------------------------------------------------------------------------
// 5. Credit limit
// ---------------------------------------------------------------------------

func TestE2E_CreditLimit(t *testing.T) {
	e := readyEnv(t, false)
	mgr := e.managerClient()
	cash := e.cashierClient()
	pid := e.createProduct(mgr, "E2E-CR-1", "منتج ائتمان", "10.00", "100")

	code, body := e.postJSON(mgr, "/api/customers", map[string]any{
		"name": "عميل حد 100", "phone": "01000000002", "credit_limit": "100.00",
	})
	expectOK(t, "create customer with 100 EGP limit", code, body)
	custID, _ := body["customer_id"].(float64)

	e.openShift(cash, 10000)
	creditReq := func(allow bool) (int, map[string]any) {
		return e.sell(cash, map[string]any{
			"sale_type": "retail", "customer_id": int64(custID),
			"lines": []map[string]any{{"product_id": pid, "qty": "15"}},
			"paid":  0, "payment_method": "Cash", "allow_over_limit": allow,
		})
	}
	// 15 x 10.00 = 150.00 EGP credit against a 100.00 limit.
	code, body = creditReq(false)
	expectDenied(t, "cashier credit sale over limit", code, body)
	if !strings.Contains(body["error"].(string), "حد الائتمان") {
		t.Fatalf("over-limit error does not mention the credit limit: %v", body)
	}
	// The cashier cannot approve his own override either.
	code, body = creditReq(true)
	expectDenied(t, "cashier over-limit override", code, body)

	// The manager CAN approve the override — and an audit row must exist.
	e.openShift(mgr, 10000)
	code, body = e.sell(mgr, map[string]any{
		"sale_type": "retail", "customer_id": int64(custID),
		"lines": []map[string]any{{"product_id": pid, "qty": "15"}},
		"paid":  0, "payment_method": "Cash", "allow_over_limit": true,
	})
	expectOK(t, "manager over-limit sale", code, body)
	if body["remaining"] != float64(15000) {
		t.Fatalf("manager over-limit sale remaining = %v, want 15000", body["remaining"])
	}
	if got := e.queryInt(`SELECT COUNT(*) FROM AuditLog WHERE ActionType='تجاوز حد الائتمان' AND UserName='manager'`); got != 1 {
		t.Fatalf("over-limit audit rows = %d, want 1", got)
	}
	// No invoice was created by the two refused cashier attempts.
	if got := e.queryInt(`SELECT COUNT(*) FROM Invoices WHERE CustomerID=?`, int64(custID)); got != 1 {
		t.Fatalf("invoices for the customer = %d, want 1 (only the approved one)", got)
	}
}

// ---------------------------------------------------------------------------
// 6. Shifts
// ---------------------------------------------------------------------------

func TestE2E_ShiftRules(t *testing.T) {
	e := readyEnv(t, false)
	mgr := e.managerClient()
	cash := e.cashierClient()
	pid := e.createProduct(mgr, "E2E-SH-1", "منتج شيفت", "10.00", "10")

	t.Run("sale-without-open-shift", func(t *testing.T) {
		code, body := e.sell(cash, map[string]any{
			"sale_type": "retail",
			"lines":     []map[string]any{{"product_id": pid, "qty": "1"}},
			"paid":      1000, "payment_method": "Cash",
		})
		expectDenied(t, "sale without open shift", code, body)
	})

	t.Run("negative-opening", func(t *testing.T) {
		code, body := e.postJSON(cash, "/api/shifts/open", map[string]any{"opening_cents": -100})
		expectDenied(t, "open shift with negative opening", code, body)
	})

	t.Run("close-uses-expected-as-actual", func(t *testing.T) {
		e.openShift(cash, 10000)
		_, _ = e.cashSale(cash, pid, "2", 2000) // +20.00 cash
		code, body := e.postJSON(cash, "/api/shifts/close", map[string]any{})
		expectOK(t, "close shift", code, body)
		if body["difference_cents"] != float64(0) {
			t.Fatalf("close difference_cents = %v, want 0", body["difference_cents"])
		}
		if body["expected_cents"] != float64(12000) || body["actual_cents"] != float64(12000) {
			t.Fatalf("close expected/actual = %v/%v, want 12000/12000", body["expected_cents"], body["actual_cents"])
		}
		if got := e.queryInt(`SELECT Difference FROM Shifts WHERE UserID=? ORDER BY ShiftID DESC LIMIT 1`, e.userID["cashier"]); got != 0 {
			t.Fatalf("stored shift Difference = %d, want 0", got)
		}
		// A closed shift cannot close again.
		code, body = e.postJSON(cash, "/api/shifts/close", map[string]any{})
		expectDenied(t, "close with no open shift", code, body)
	})
}

func TestE2E_YesterdayShiftAutoClosesOnConsult(t *testing.T) {
	e := readyEnv(t, false)
	cash := e.cashierClient()
	yesterday := cairoNow().AddDate(0, 0, -1).Format("2006-01-02")
	// A stale open shift from yesterday, inserted directly (no HTTP path
	// can create one — OpenShift always dates today).
	res, err := e.store.DB.Exec(
		`INSERT INTO Shifts(UserID,UserName,ShiftDate,OpenTime,Status,OpeningBalance,IsOpen,SessionCount)
		 VALUES(?,?,?,?,'Open',5000,1,1)`,
		e.userID["cashier"], "cashier", yesterday, yesterday+" 09:00:00")
	if err != nil {
		t.Fatalf("insert stale shift: %v", err)
	}
	staleID, _ := res.LastInsertId()
	if _, err := e.store.DB.Exec(
		`INSERT INTO ShiftSessions(ShiftID,UserID,UserName,SessionDate,OpenTime,IsOpen)
		 VALUES(?,?,?,?,?,1)`, staleID, e.userID["cashier"], "cashier", yesterday, yesterday+" 09:00:00"); err != nil {
		t.Fatalf("insert stale session: %v", err)
	}
	// Consulting the status auto-closes the stale shift (F32-4) and
	// reports the just-auto-closed shift (FIX-3 L17: state "closed",
	// ShiftID, StatusText naming the auto-close); it must NOT open a
	// new one.
	code, body := e.getJSON(cash, "/api/shifts/status")
	expectOK(t, "shift status consult", code, body)
	if body["state"] != "closed" || body["has_open"] != false {
		t.Fatalf("status after consult = %v, want state=closed has_open=false", body)
	}
	if body["shift_id"] != float64(staleID) {
		t.Fatalf("status shift_id = %v, want %d (the just-auto-closed shift)", body["shift_id"], staleID)
	}
	if st, _ := body["status_text"].(string); !strings.Contains(st, "تلقائي") {
		t.Fatalf("status_text = %q, want it to name the auto-close", st)
	}
	if got := e.queryInt(`SELECT IsOpen FROM Shifts WHERE ShiftID=?`, staleID); got != 0 {
		t.Fatal("yesterday's shift is still open after the consult")
	}
	if got := e.queryInt(`SELECT COUNT(*) FROM Shifts WHERE UserID=? AND ShiftDate=?`, e.userID["cashier"], store.Today()); got != 0 {
		t.Fatalf("a new shift was auto-opened for today (%d rows), want 0", got)
	}
	// The auto-close is audited.
	if got := e.queryInt(`SELECT COUNT(*) FROM AuditLog WHERE ActionType='إغلاق شيفت' AND Notes LIKE '%إغلاق تلقائي%'`); got < 1 {
		t.Fatalf("auto-close audit rows = %d, want >= 1", got)
	}
}

// ---------------------------------------------------------------------------
// 7. Stocktake
// ---------------------------------------------------------------------------

func TestE2E_Stocktake(t *testing.T) {
	e := readyEnv(t, false)
	mgr := e.managerClient()
	cash := e.cashierClient()
	pid := e.createProduct(mgr, "E2E-ST-1", "منتج جرد", "10.00", "10")

	// The cashier may count but may NOT create a session (manage_stocktakes).
	code, _, _ := e.postFormLoc(cash, "/stocktake/new", url.Values{"notes": {"جرد كاشير"}})
	if code != http.StatusForbidden {
		t.Fatalf("cashier create stocktake session: want 403, got %d", code)
	}
	// The manager creates the session.
	code, loc, body := e.postFormLoc(mgr, "/stocktake/new", url.Values{"notes": {"جرد تجريبي E2E"}})
	if code != http.StatusSeeOther {
		t.Fatalf("manager create stocktake: want 303, got %d body=%q", code, truncate(body, 300))
	}
	stID, err := strconv.ParseInt(strings.TrimPrefix(loc, "/stocktake/"), 10, 64)
	if err != nil || stID == 0 {
		t.Fatalf("stocktake redirect %q has no id", loc)
	}
	// Cashier counts by barcode scan: 10 on the shelf -> counted 8.
	code, _, body = e.postFormLoc(cash, fmt.Sprintf("/stocktake/%d/scan", stID), url.Values{
		"barcode": {"E2E-ST-1"}, "qty": {"8"},
	})
	if code != http.StatusSeeOther {
		t.Fatalf("cashier scan: want 303, got %d body=%q", code, truncate(body, 300))
	}
	// The cashier may NOT approve (approve_stocktake).
	code, _, _ = e.postFormLoc(cash, fmt.Sprintf("/stocktake/%d/approve", stID), url.Values{"reason": {"اعتماد كاشير"}})
	if code != http.StatusForbidden {
		t.Fatalf("cashier approve stocktake: want 403, got %d", code)
	}
	// Approval before close is refused even for the manager.
	code, body = e.postForm(mgr, fmt.Sprintf("/stocktake/%d/approve", stID), url.Values{"reason": {"مبكر"}})
	if code != http.StatusOK || !strings.Contains(body, "لم تُغلق بعد") {
		t.Fatalf("manager approve open stocktake: want 200 + 'لم تُغلق بعد', got %d body=%q", code, truncate(body, 300))
	}
	// Manager closes, then approves: the counted quantity applies.
	code, _, body = e.postFormLoc(mgr, fmt.Sprintf("/stocktake/%d/close", stID), url.Values{})
	if code != http.StatusSeeOther {
		t.Fatalf("manager close stocktake: want 303, got %d body=%q", code, truncate(body, 300))
	}
	code, _, body = e.postFormLoc(mgr, fmt.Sprintf("/stocktake/%d/approve", stID), url.Values{"reason": {"تصحيح جرد تجريبي"}})
	if code != http.StatusSeeOther {
		t.Fatalf("manager approve stocktake: want 303, got %d body=%q", code, truncate(body, 300))
	}
	code, body2 := e.getJSON(mgr, fmt.Sprintf("/api/stocktake/%d", stID))
	expectOK(t, "get stocktake", code, body2)
	if body2["status"] != "closed" {
		t.Fatalf("stocktake status = %v, want closed", body2["status"])
	}
	if got := e.queryInt(`SELECT Quantity FROM Products WHERE ProductID=?`, pid); got != 8000 {
		t.Fatalf("product quantity after approve = %d milli, want 8000", got)
	}
	// A second approval is a no-op: the quantity does not move again.
	code, _, body = e.postFormLoc(mgr, fmt.Sprintf("/stocktake/%d/approve", stID), url.Values{"reason": {"اعتماد مكرر"}})
	if code != http.StatusSeeOther {
		t.Fatalf("second approve: want 303 (no-op), got %d body=%q", code, truncate(body, 300))
	}
	if got := e.queryInt(`SELECT Quantity FROM Products WHERE ProductID=?`, pid); got != 8000 {
		t.Fatalf("product quantity after second approve = %d milli, want still 8000", got)
	}
}

// ---------------------------------------------------------------------------
// 8. Notifications
// ---------------------------------------------------------------------------

func TestE2E_Notifications(t *testing.T) {
	e := readyEnv(t, false)
	mgr := e.managerClient()
	cash := e.cashierClient()

	// The cashier cannot manage notifications.
	code, _ := e.get(cash, "/notifications/manage")
	if code != http.StatusForbidden {
		t.Fatalf("cashier GET /notifications/manage: want 403, got %d", code)
	}
	code, _, _ = e.postFormLoc(cash, "/notifications/new", url.Values{
		"type": {"custom"}, "title": {"x"}, "body": {"y"}, "audience": {"all"},
	})
	if code != http.StatusForbidden {
		t.Fatalf("cashier POST /notifications/new: want 403, got %d", code)
	}

	// The manager creates a custom notification for everyone.
	code, loc, body := e.postFormLoc(mgr, "/notifications/new", url.Values{
		"type": {"custom"}, "title": {"تنبيه E2E"}, "body": {"نص التنبيه التجريبي"}, "audience": {"all"},
	})
	if code != http.StatusSeeOther || loc != "/notifications/manage" {
		t.Fatalf("manager create notification: want 303 to /notifications/manage, got %d loc %q body=%q", code, loc, truncate(body, 300))
	}
	// The cashier's unread count goes above zero.
	code, body2 := e.getJSON(cash, "/api/notifications/unread-count")
	expectOK(t, "unread count after create", code, body2)
	if body2["unread"] != float64(1) {
		t.Fatalf("cashier unread = %v, want 1", body2["unread"])
	}
	// Find the cashier's inbox row and mark it read.
	code, body2 = e.getJSON(cash, "/api/notifications")
	expectOK(t, "list inbox", code, body2)
	var notifID int64
	if list, ok := body2["notifications"].([]any); ok {
		for _, item := range list {
			if m, ok := item.(map[string]any); ok && m["Title"] == "تنبيه E2E" {
				if f, ok := m["ID"].(float64); ok {
					notifID = int64(f)
				}
			}
		}
	}
	if notifID == 0 {
		t.Fatalf("notification not found in cashier inbox: %v", body2)
	}
	code, body2 = e.postJSON(cash, fmt.Sprintf("/api/notifications/%d/read", notifID), map[string]any{})
	expectOK(t, "mark notification read", code, body2)
	code, body2 = e.getJSON(cash, "/api/notifications/unread-count")
	expectOK(t, "unread count after read", code, body2)
	if body2["unread"] != float64(0) {
		t.Fatalf("cashier unread after read = %v, want 0", body2["unread"])
	}
	// The manager's own inbox also received it (audience=all).
	code, body2 = e.getJSON(mgr, "/api/notifications/unread-count")
	expectOK(t, "manager unread count", code, body2)
	if body2["unread"] != float64(1) {
		t.Fatalf("manager unread = %v, want 1", body2["unread"])
	}
}

// ---------------------------------------------------------------------------
// 9. Backup (file-backed database only)
// ---------------------------------------------------------------------------

func TestE2E_Backup(t *testing.T) {
	e := readyEnv(t, true) // file DB under t.TempDir()
	mgr := e.managerClient()
	cash := e.cashierClient()

	// The cashier is refused.
	code, body := e.postJSON(cash, "/api/backup/now", map[string]any{})
	if code != http.StatusForbidden || body["ok"] != false {
		t.Fatalf("cashier backup: want 403 ok=false, got %d body=%v", code, body)
	}
	// The manager's backup succeeds locally (cloud disabled by default).
	code, body = e.postJSON(mgr, "/api/backup/now", map[string]any{})
	expectOK(t, "manager backup now", code, body)
	file, _ := body["file"].(string)
	if file == "" {
		t.Fatalf("backup response has no file: %v", body)
	}
	if body["local_size"] == float64(0) || body["local_size"] == nil {
		t.Fatalf("backup local_size = %v, want > 0", body["local_size"])
	}
	if body["cloud_status"] != "disabled" {
		t.Fatalf("backup cloud_status = %v, want disabled (cloud not configured)", body["cloud_status"])
	}
	// The backup file exists on disk and is a valid SQLite database.
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(e.store.Path), "backups", "CashierDB_*.db"))
	if len(matches) == 0 {
		t.Fatal("no CashierDB_*.db file under backups/")
	}
	if err := store.VerifyBackupFile(matches[0]); err != nil {
		t.Fatalf("VerifyBackupFile: %v", err)
	}
	// The backup list shows the new backup.
	code, body = e.getJSON(mgr, "/api/backup/list")
	expectOK(t, "backup list", code, body)
	found := false
	if list, ok := body["backups"].([]any); ok {
		for _, item := range list {
			if m, ok := item.(map[string]any); ok && m["Name"] == file {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("backup %q not in list %v", file, body["backups"])
	}
	// The HTML backup page opens for the manager too.
	code, html := e.get(mgr, "/backup")
	if code != http.StatusOK || !strings.Contains(html, file) {
		t.Fatalf("GET /backup: want 200 listing %q, got %d body=%q", file, code, truncate(html, 300))
	}
}

// ---------------------------------------------------------------------------
// 10. HTML pages per role
// ---------------------------------------------------------------------------

func TestE2E_HTMLPages(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	cash := e.cashierClient()
	sup := e.supClient()

	// Pages every logged-in role in the table may open (200).
	// FIX-1: the KNOWN DEFECT pins are flipped — the B1 templates
	// (sales/products/promos/stocktake) used to call the missing
	// {{template "layout"}} and answered 200 with an EMPTY body, and
	// users_list.html used {{range .}} over the Page struct so the
	// users table never rendered. Both are fixed; the tests below now
	// assert non-empty HTML containing the page's own content, and
	// /users must show a seeded username ("cashier").
	allowed := []struct {
		path string
		c    *http.Client
		role string
		want string // page content that must appear in the body
	}{
		{"/sales", cash, "cashier", "المبيعات"}, {"/sales", admin, "admin", "المبيعات"},
		{"/products", cash, "cashier", "المنتجات"},
		{"/customers", cash, "cashier", ""}, // cashier holds manage_customers (§15)
		{"/vendors", cash, "cashier", ""},
		{"/shifts", cash, "cashier", ""},
		{"/returns", sup, "supervisor", ""}, // cashier does NOT hold do_return
		{"/expenses", cash, "cashier", ""},
		{"/promotions", cash, "cashier", "العروض"},
		{"/stocktake", cash, "cashier", "الجرد"},
		{"/reports", admin, "admin", ""},
		{"/audit", admin, "admin", ""},
		{"/notifications", cash, "cashier", ""},
		{"/users", admin, "admin", "cashier"},
		{"/settings", admin, "admin", ""},
		{"/dailyclose", admin, "admin", ""},
		{"/dailyclose", sup, "supervisor", ""},
	}
	for _, tc := range allowed {
		t.Run(fmt.Sprintf("GET %s as %s", tc.path, tc.role), func(t *testing.T) {
			code, body := e.get(tc.c, tc.path)
			if code != http.StatusOK {
				t.Fatalf("GET %s as %s: want 200, got %d body=%q", tc.path, tc.role, code, truncate(body, 300))
			}
			if !strings.Contains(body, "<html") && !strings.Contains(body, "<!DOCTYPE") {
				t.Fatalf("GET %s as %s: 200 body is not HTML: %q", tc.path, tc.role, truncate(body, 200))
			}
			if tc.want != "" && !strings.Contains(body, tc.want) {
				t.Fatalf("GET %s as %s: body lacks %q (page content missing): %q", tc.path, tc.role, tc.want, truncate(body, 300))
			}
		})
	}

	// Pages the cashier must NOT open (403, documented behaviour).
	denied := []string{"/reports", "/audit", "/users", "/settings", "/dailyclose", "/returns"}
	for _, p := range denied {
		t.Run(fmt.Sprintf("cashier denied %s", p), func(t *testing.T) {
			code, body := e.get(cash, p)
			if code != http.StatusForbidden {
				t.Fatalf("cashier GET %s: want 403, got %d body=%q", p, code, truncate(body, 200))
			}
			if !strings.Contains(body, "غير مسموح") {
				t.Fatalf("cashier GET %s: 403 body lacks denial message: %q", p, truncate(body, 200))
			}
		})
	}
}
