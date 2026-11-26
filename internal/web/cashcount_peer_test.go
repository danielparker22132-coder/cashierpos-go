// Package web_test — cash-count peer approval (jony 2026-11-25).
//
// End-to-end over HTTP, reusing the e2eEnv harness from e2e_test.go:
// a peer cashier approves/rejects another cashier's count, self-review
// is refused, the supervisor is forbidden, and the manager-only
// approvals log answers day/month/custom filters + CSV. Button states
// on /shifts are asserted from the rendered HTML.
package web_test

import (
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// seedCashier2 inserts a second cashier (cashier2 / CashPass123).
func seedCashier2(t *testing.T, e *e2eEnv) {
	t.Helper()
	res, err := e.store.DB.Exec(
		`INSERT INTO Users(UserName,Password,FullName,Role,IsActive,MustChangePassword,CreatedDate)
		 VALUES(?,?,?, 'cashier',1,0,?)`,
		"cashier2", sha256Hex(cashierPass), "cashier2", "2026-11-25")
	if err != nil {
		t.Fatalf("seed cashier2: %v", err)
	}
	id, _ := res.LastInsertId()
	e.userID["cashier2"] = id
}

// recordCount records a cash count through the API and returns its id.
func recordCount(t *testing.T, e *e2eEnv, c *http.Client, shiftID, amountCents int64) int64 {
	t.Helper()
	code, body := e.postJSON(c, "/api/cashbox/count", map[string]any{
		"shift_id": shiftID, "amount_cents": amountCents,
	})
	expectOK(t, "record cash count", code, body)
	var id int64
	if err := e.store.DB.QueryRow(`SELECT CountID FROM CashCounts
		ORDER BY CountID DESC LIMIT 1`).Scan(&id); err != nil {
		t.Fatalf("count id: %v", err)
	}
	return id
}

func reviewStatus(t *testing.T, e *e2eEnv, countID int64) string {
	t.Helper()
	var st string
	if err := e.store.DB.QueryRow(`SELECT ReviewStatus FROM CashCounts WHERE CountID=?`, countID).Scan(&st); err != nil {
		t.Fatalf("review status: %v", err)
	}
	return st
}

// peerEnv is a ready env with two cashiers and one recorded count owned
// by the first cashier (opening 100.00, counted 90.00).
func peerEnv(t *testing.T) (*e2eEnv, *http.Client, *http.Client, int64) {
	t.Helper()
	e := readyEnv(t, false)
	seedCashier2(t, e)
	c1 := e.cashierClient()
	c2 := e.loginAs("cashier2", cashierPass)
	shiftID := e.openShift(c1, 10000)
	countID := recordCount(t, e, c1, shiftID, 9000)
	return e, c1, c2, countID
}

func TestCashcountPeer_ApproveByPeerCashier(t *testing.T) {
	e, _, c2, countID := peerEnv(t)
	code, loc, body := e.postFormLoc(c2, "/cashbox/count/"+strconv.FormatInt(countID, 10)+"/review",
		url.Values{"approve": {"1"}, "note": {"مطابق"}})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/shifts") {
		t.Fatalf("peer approve: want 303 to /shifts, got %d loc %q body=%q", code, loc, truncate(body, 300))
	}
	if st := reviewStatus(t, e, countID); st != "approved" {
		t.Fatalf("count must be approved, got %q", st)
	}
	if n := e.queryInt(`SELECT COUNT(*) FROM AuditLog WHERE ActionType='اعتماد فرق عدّ النقدية'`); n != 1 {
		t.Fatalf("expected 1 approve audit row, got %d", n)
	}
}

func TestCashcountPeer_RejectByPeerCashier(t *testing.T) {
	e, _, c2, countID := peerEnv(t)
	code, _, body := e.postFormLoc(c2, "/cashbox/count/"+strconv.FormatInt(countID, 10)+"/review",
		url.Values{"approve": {"0"}, "note": {"فرق غير مبرر"}})
	if code != http.StatusSeeOther {
		t.Fatalf("peer reject: want 303, got %d body=%q", code, truncate(body, 300))
	}
	if st := reviewStatus(t, e, countID); st != "rejected" {
		t.Fatalf("count must be rejected, got %q", st)
	}
}

func TestCashcountPeer_SelfApproveRefused(t *testing.T) {
	e, c1, _, countID := peerEnv(t)
	code, body := e.postForm(c1, "/cashbox/count/"+strconv.FormatInt(countID, 10)+"/review",
		url.Values{"approve": {"1"}})
	if code != http.StatusOK || !strings.Contains(body, "عدّك الخاص") {
		t.Fatalf("self-approve: want 200 with the self-review message, got %d body=%q", code, truncate(body, 300))
	}
	if st := reviewStatus(t, e, countID); st != "pending" {
		t.Fatalf("self-reviewed count must stay pending, got %q", st)
	}
	if n := e.queryInt(`SELECT COUNT(*) FROM AuditLog WHERE ActionType IN ('اعتماد فرق عدّ النقدية','رفض فرق عدّ النقدية')`); n != 0 {
		t.Fatalf("self-review must write no audit rows, got %d", n)
	}
}

func TestCashcountPeer_SupervisorForbidden(t *testing.T) {
	e, _, _, countID := peerEnv(t)
	sup := e.supClient()
	code, body := e.postForm(sup, "/cashbox/count/"+strconv.FormatInt(countID, 10)+"/review",
		url.Values{"approve": {"1"}})
	if code != http.StatusForbidden {
		t.Fatalf("supervisor review: want 403, got %d body=%q", code, truncate(body, 300))
	}
	if st := reviewStatus(t, e, countID); st != "pending" {
		t.Fatalf("supervisor attempt must leave the count pending, got %q", st)
	}
}

func TestCashcountPeer_LogPage(t *testing.T) {
	e, _, c2, countID := peerEnv(t)
	// Peer approves so the log has one decision today.
	if code, _, _ := e.postFormLoc(c2, "/cashbox/count/"+strconv.FormatInt(countID, 10)+"/review",
		url.Values{"approve": {"1"}}); code != http.StatusSeeOther {
		t.Fatalf("setup approve: want 303, got %d", code)
	}
	mgr := e.managerClient()
	today := cairoNow().Format("2006-01-02")
	month := cairoNow().Format("2006-01")

	// Manager: default (today) shows the decision.
	code, body := e.get(mgr, "/cashcount-approvals")
	if code != http.StatusOK || !strings.Contains(body, "سجل اعتماد العدّات") ||
		!strings.Contains(body, "cashier") || !strings.Contains(body, "معتمد") {
		t.Fatalf("manager log: want 200 with the decision row, got %d body=%q", code, truncate(body, 500))
	}
	// Month filter shows it; custom period shows it; another day is empty.
	if code, body := e.get(mgr, "/cashcount-approvals?mode=month&month="+month); code != http.StatusOK ||
		!strings.Contains(body, "معتمد") {
		t.Fatalf("month filter: want the decision row, got %d body=%q", code, truncate(body, 300))
	}
	if code, body := e.get(mgr, "/cashcount-approvals?mode=custom&from="+today+"&to="+today); code != http.StatusOK ||
		!strings.Contains(body, "معتمد") {
		t.Fatalf("custom filter: want the decision row, got %d body=%q", code, truncate(body, 300))
	}
	if code, body := e.get(mgr, "/cashcount-approvals?mode=day&day=2000-01-01"); code != http.StatusOK ||
		!strings.Contains(body, "لا توجد قرارات اعتماد في هذه الفترة") {
		t.Fatalf("empty day: want the empty message, got %d body=%q", code, truncate(body, 300))
	}
	// Bad filter shows an Arabic error, not a 500.
	if code, body := e.get(mgr, "/cashcount-approvals?mode=custom&from=2026-11-21&to=2026-11-20"); code != http.StatusOK ||
		!strings.Contains(body, "تاريخ البداية يجب أن يكون قبل تاريخ النهاية أو مساويًا له") {
		t.Fatalf("reversed range: want the Arabic error, got %d body=%q", code, truncate(body, 300))
	}
	// CSV export.
	resp, err := mgr.Get(e.base + "/cashcount-approvals?format=csv")
	if err != nil {
		t.Fatalf("GET csv: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "text/csv") ||
		!strings.Contains(string(b), "الوقت,الكاشير,المعتمد,الشيفت") {
		t.Fatalf("csv: want 200 text/csv with headers, got %d %q body=%q",
			resp.StatusCode, resp.Header.Get("Content-Type"), truncate(string(b), 300))
	}
	// JSON API: same filters, exact keys.
	if code, data := e.getJSON(mgr, "/api/cashcount-approvals?mode=day&day="+today); code != http.StatusOK {
		t.Fatalf("api log: want 200, got %d %v", code, data)
	} else {
		reviews, _ := data["reviews"].([]any)
		if len(reviews) != 1 {
			t.Fatalf("api log: want 1 review, got %v", data)
		}
		row := reviews[0].(map[string]any)
		for _, k := range []string{"count_id", "reviewed_at", "owner", "approver", "shift_id", "expected", "counted", "difference", "decision"} {
			if _, ok := row[k]; !ok {
				t.Fatalf("api log row missing key %q: %v", k, row)
			}
		}
		if row["decision"] != "approved" || row["owner"] != "cashier" {
			t.Fatalf("api log row wrong: %v", row)
		}
	}
	// Cashier and supervisor are forbidden (403) from the log.
	cash := e.cashierClient()
	if code, _ := e.get(cash, "/cashcount-approvals"); code != http.StatusForbidden {
		t.Fatalf("cashier log: want 403, got %d", code)
	}
	sup := e.supClient()
	if code, _ := e.get(sup, "/cashcount-approvals"); code != http.StatusForbidden {
		t.Fatalf("supervisor log: want 403, got %d", code)
	}
	if code, _ := e.getJSON(cash, "/api/cashcount-approvals"); code != http.StatusForbidden {
		t.Fatalf("cashier api log: want 403, got %d", code)
	}
}

func TestCashcountPeer_ButtonStates(t *testing.T) {
	e, c1, c2, countID := peerEnv(t)
	enabled := `<button type="submit" name="approve" value="1" class="btn">اعتماد</button>`
	disabled := `<button type="submit" name="approve" value="1" class="btn" disabled>اعتماد</button>`
	selfHint := "لا يمكنك اعتماد عدّك الخاص"

	// The peer cashier sees the other's pending count with ENABLED buttons.
	if code, body := e.get(c2, "/shifts"); code != http.StatusOK || !strings.Contains(body, enabled) {
		t.Fatalf("peer shifts page: want enabled approve button, got %d body=%q", code, truncate(body, 500))
	}
	// The owner sees their own pending count with DISABLED buttons + hint.
	if code, body := e.get(c1, "/shifts"); code != http.StatusOK ||
		!strings.Contains(body, disabled) || !strings.Contains(body, selfHint) {
		t.Fatalf("owner shifts page: want disabled buttons + own-count hint, got %d body=%q", code, truncate(body, 500))
	}
	// After the peer approves, the manager's page shows the decision and
	// NO enabled approve button for that (now reviewed) count — no grey
	// pair left hanging on a reviewed row.
	if code, _, _ := e.postFormLoc(c2, "/cashbox/count/"+strconv.FormatInt(countID, 10)+"/review",
		url.Values{"approve": {"1"}}); code != http.StatusSeeOther {
		t.Fatalf("setup approve: want 303, got %d", code)
	}
	mgr := e.managerClient()
	if code, body := e.get(mgr, "/shifts"); code != http.StatusOK ||
		strings.Contains(body, enabled) || !strings.Contains(body, "لا توجد عدّات بانتظار المراجعة") {
		t.Fatalf("manager shifts after review: want no enabled buttons and empty pending list, got %d body=%q",
			code, truncate(body, 800))
	}
	// The log button is on the shifts page for the manager only.
	if code, body := e.get(mgr, "/shifts"); code != http.StatusOK ||
		!strings.Contains(body, `href="/cashcount-approvals"`) || !strings.Contains(body, "سجل اعتماد العدّات") {
		t.Fatalf("manager shifts: want the approvals-log button, got %d body=%q", code, truncate(body, 500))
	}
	if code, body := e.get(c1, "/shifts"); code != http.StatusOK ||
		strings.Contains(body, `href="/cashcount-approvals"`) {
		t.Fatalf("cashier shifts: must not see the approvals-log button, got %d", code)
	}
}

// --- C-3 (jony's ruling 2026-11-25): Manager/Admin may approve their
// own count; a cashier still may never approve their own. Live HTTP
// over the real server: the form POST that the /shifts buttons submit.
//
// NOTE: the /shifts template still renders the own-count buttons
// disabled + the "لا يمكنك اعتماد عدّك الخاص" hint for EVERY owner
// (shifts.html:83,137) — that template is outside this worker's file
// scope, so a manager approving their own count via the rendered
// button is still blocked in the UI; the server rule below is what
// this round changes, and the POST itself succeeds. The template
// gating needs a follow-up edit (manager/admin own counts enabled,
// cashier own counts still disabled).

func TestCashcountPeer_ManagerSelfApproveSucceeds(t *testing.T) {
	e := readyEnv(t, false)
	mgr := e.managerClient()
	shiftID := e.openShift(mgr, 10000)
	countID := recordCount(t, e, mgr, shiftID, 9000)
	code, loc, body := e.postFormLoc(mgr, "/cashbox/count/"+strconv.FormatInt(countID, 10)+"/review",
		url.Values{"approve": {"1"}, "note": {"اعتماد ذاتي"}})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/shifts") {
		t.Fatalf("manager self-approve: want 303 to /shifts, got %d loc %q body=%q", code, loc, truncate(body, 300))
	}
	if st := reviewStatus(t, e, countID); st != "approved" {
		t.Fatalf("manager self-approved count must be approved, got %q", st)
	}
	// Audited as a NORMAL approval: one approve row, zero attempt rows.
	if n := e.queryInt(`SELECT COUNT(*) FROM AuditLog WHERE ActionType='اعتماد فرق عدّ النقدية'`); n != 1 {
		t.Fatalf("expected 1 approve audit row, got %d", n)
	}
	if n := e.queryInt(`SELECT COUNT(*) FROM AuditLog WHERE ActionType='محاولة اعتماد عدّ خاص'`); n != 0 {
		t.Fatalf("manager self-approval must write no attempt audit row, got %d", n)
	}
	// It appears in the approvals log (day + month + custom period)
	// with owner == approver == manager.
	today := cairoNow().Format("2006-01-02")
	month := cairoNow().Format("2006-01")
	for _, q := range []string{
		"/api/cashcount-approvals?mode=day&day=" + today,
		"/api/cashcount-approvals?mode=month&month=" + month,
		"/api/cashcount-approvals?mode=custom&from=" + today + "&to=" + today,
	} {
		code, data := e.getJSON(mgr, q)
		if code != http.StatusOK {
			t.Fatalf("log %s: want 200, got %d %v", q, code, data)
		}
		reviews, _ := data["reviews"].([]any)
		if len(reviews) != 1 {
			t.Fatalf("log %s: want 1 review, got %v", q, data)
		}
		row := reviews[0].(map[string]any)
		if row["decision"] != "approved" || row["owner"] != "manager" || row["approver"] != "manager" {
			t.Fatalf("log %s: self-approval row wrong: %v", q, row)
		}
	}
}

func TestCashcountPeer_AdminSelfApproveSucceeds(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	shiftID := e.openShift(admin, 10000)
	countID := recordCount(t, e, admin, shiftID, 9000)
	code, loc, body := e.postFormLoc(admin, "/cashbox/count/"+strconv.FormatInt(countID, 10)+"/review",
		url.Values{"approve": {"1"}})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/shifts") {
		t.Fatalf("admin self-approve: want 303 to /shifts, got %d loc %q body=%q", code, loc, truncate(body, 300))
	}
	if st := reviewStatus(t, e, countID); st != "approved" {
		t.Fatalf("admin self-approved count must be approved, got %q", st)
	}
	if n := e.queryInt(`SELECT COUNT(*) FROM AuditLog WHERE ActionType='اعتماد فرق عدّ النقدية'`); n != 1 {
		t.Fatalf("expected 1 approve audit row, got %d", n)
	}
}

func TestCashcountPeer_ManagerSelfRejectSucceeds(t *testing.T) {
	e := readyEnv(t, false)
	mgr := e.managerClient()
	shiftID := e.openShift(mgr, 10000)
	countID := recordCount(t, e, mgr, shiftID, 9000)
	code, _, body := e.postFormLoc(mgr, "/cashbox/count/"+strconv.FormatInt(countID, 10)+"/review",
		url.Values{"approve": {"0"}, "note": {"فرق غير مبرر"}})
	if code != http.StatusSeeOther {
		t.Fatalf("manager self-reject: want 303, got %d body=%q", code, truncate(body, 300))
	}
	if st := reviewStatus(t, e, countID); st != "rejected" {
		t.Fatalf("manager self-rejected count must be rejected, got %q", st)
	}
	if n := e.queryInt(`SELECT COUNT(*) FROM AuditLog WHERE ActionType='رفض فرق عدّ النقدية'`); n != 1 {
		t.Fatalf("expected 1 reject audit row, got %d", n)
	}
}

func TestCashcountPeer_CashierSelfRejectStillRefused(t *testing.T) {
	e, c1, _, countID := peerEnv(t)
	code, body := e.postForm(c1, "/cashbox/count/"+strconv.FormatInt(countID, 10)+"/review",
		url.Values{"approve": {"0"}, "note": {"أرفض نفسي"}})
	if code != http.StatusOK || !strings.Contains(body, "عدّك الخاص") {
		t.Fatalf("cashier self-reject: want 200 with the self-review message, got %d body=%q", code, truncate(body, 300))
	}
	if st := reviewStatus(t, e, countID); st != "pending" {
		t.Fatalf("cashier self-rejected count must stay pending, got %q", st)
	}
	if n := e.queryInt(`SELECT COUNT(*) FROM AuditLog WHERE ActionType IN ('اعتماد فرق عدّ النقدية','رفض فرق عدّ النقدية')`); n != 0 {
		t.Fatalf("cashier self-reject must write no approve/reject rows, got %d", n)
	}
	if n := e.queryInt(`SELECT COUNT(*) FROM AuditLog WHERE ActionType='محاولة اعتماد عدّ خاص'`); n != 1 {
		t.Fatalf("expected 1 self-review-attempt audit row, got %d", n)
	}
}
