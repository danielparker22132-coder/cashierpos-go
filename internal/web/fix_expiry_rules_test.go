// Package web_test — expiry rules (jony's order, 28 Nov 2026, expanded
// to EVERY expiry field): expiry is never required; picking a duration
// fills from with today when from is empty and sets to = from + duration,
// for all 8 durations, at all three entry points that accept an expiry
// date (purchase form, product form, promo form). The duration math
// lives in the template JS, so these tests EXECUTE the shipped script
// in node against a stub DOM and compare every result with an
// independent Go reference implementation — no text-matching of dates.
package web_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

var expiryDurations = []expiryDuration{
	{"7", 7, 0},
	{"14", 14, 0},
	{"1m", 0, 1},
	{"2m", 0, 2},
	{"3m", 0, 3},
	{"6m", 0, 6},
	{"12m", 0, 12},
	{"24m", 0, 24},
}

type expiryDuration struct {
	value  string
	days   int
	months int
}

var expiryDurationOptions = []string{
	`value="7">أسبوع`, `value="14">أسبوعين`, `value="1m">شهر`, `value="2m">شهرين`,
	`value="3m">3 شهور`, `value="6m">6 شهور`, `value="12m">سنة`, `value="24m">سنتين`,
}

// expiryRefTo is the independent reference: from + duration, months
// clamped to the target month's last day (31 Jan + 1m -> 28 Feb).
func expiryRefTo(from time.Time, d expiryDuration) string {
	if d.months > 0 {
		y, m, day := from.Date()
		total := int(m) - 1 + d.months
		ny := y + total/12
		nm := time.Month(total%12 + 1)
		last := time.Date(ny, nm+1, 0, 0, 0, 0, 0, time.Local).Day()
		if day > last {
			day = last
		}
		return time.Date(ny, nm, day, 0, 0, 0, 0, time.Local).Format("2006-01-02")
	}
	return from.AddDate(0, 0, d.days).Format("2006-01-02")
}

type expiryScenario struct {
	Duration   string `json:"duration"`
	From       string `json:"from"`
	FromSel    string `json:"fromSel"`
	ToSel      string `json:"toSel"`
	To         string `json:"to"`
	ToComputed bool   `json:"toComputed"`
	FireInput  bool   `json:"fireInput"`
	// Row, when set, runs the scenario against that purchase-form row
	// (row-scoped select + row-scoped from/to inputs). When nil the
	// scenario runs against the single-form inputs (product, promo).
	Row *int `json:"row,omitempty"`
}

type expiryResult struct {
	Duration   string         `json:"duration"`
	From       string         `json:"from"`
	To         string         `json:"to"`
	ToComputed bool           `json:"toComputed"`
	// Rows snapshots all 5 purchase rows' from/to/flag after the
	// scenario, so a test can prove one row's pick/clear left the
	// other rows untouched.
	Rows []expiryResult `json:"rows,omitempty"`
}

// expiryNodeHarness is the stub-DOM driver: it loads the extracted
// template script, captures the duration select's change handler, and
// runs each scenario through it, printing the resulting from/to values.
const expiryNodeHarness = `
var fs = require('fs');
var src = fs.readFileSync(process.argv[2], 'utf8');
var scenarios = JSON.parse(fs.readFileSync(process.argv[3], 'utf8'));
function makeInput() {
  var handlers = {};
  return {
    value: '',
    dataset: {},
    addEventListener: function (ev, fn) { handlers[ev] = fn; },
    _fire: function (ev) { if (handlers[ev]) { handlers[ev].call(this); } }
  };
}
// Form-level inputs, used by the single-form scripts (product, promo):
// their handler resolves start/end through sel.closest("form").
var inputs = {};
['input[name="expiry"]', 'input[name="valid_from"]', 'input[name="expiry_date"]', 'input[name="expiry_from"]', 'input[name="start_at"]', 'input[name="end_at"]'].forEach(function (n) { inputs[n] = makeInput(); });
var formContainer = { querySelector: function (sel) { return inputs[sel] || null; } };
// Row-scoped inputs for the purchase form: one from/to pair per row,
// resolved through sel.closest("tr"). The old harness stubbed
// querySelectorAll as a single select with one shared container, so
// only single-row logic was ever executed (fix-round-3 WEB LOW-1).
var rows = [];
for (var r = 0; r < 5; r++) {
  var rowInputs = {};
  ['input[name="valid_from"]', 'input[name="expiry"]'].forEach(function (n) { rowInputs[n] = makeInput(); });
  rows.push({ _inputs: rowInputs, querySelector: function (sel) { return this._inputs[sel] || null; } });
}
var sels = [];
function makeSel(row) {
  var s = {
    value: '',
    _handler: null,
    addEventListener: function (ev, fn) { if (ev === 'change') { this._handler = fn; } },
    closest: function () { return row == null ? formContainer : rows[row]; }
  };
  sels.push(s);
  return s;
}
var document = {
  querySelectorAll: function () {
    var out = [];
    for (var r = 0; r < 5; r++) { out.push(makeSel(r)); }
    return out;
  },
  querySelector: function (sel) {
    if (sel === '.expiry-duration' || sel === '.offer-duration') { return makeSel(null); }
    return inputs[sel] || null;
  },
  getElementById: function () { return null; }
};
eval(src);
if (!sels.some(function (s) { return s._handler; })) { console.log(JSON.stringify({ error: 'no change handler registered' })); process.exit(0); }
function rowState(r) {
  var fi = rows[r]._inputs['input[name="valid_from"]'];
  var ti = rows[r]._inputs['input[name="expiry"]'];
  return { duration: '', from: fi.value, to: ti.value, toComputed: !!(ti.dataset && ti.dataset.durationComputed) };
}
var out = scenarios.map(function (sc) {
  var fromInput, toInput, sel;
  if (sc.row != null) {
    fromInput = rows[sc.row].querySelector(sc.fromSel);
    toInput = rows[sc.row].querySelector(sc.toSel);
    sel = sels[sc.row];
  } else {
    fromInput = inputs[sc.fromSel];
    toInput = inputs[sc.toSel];
    sel = sels[0];
  }
  fromInput.value = sc.from || '';
  toInput.value = sc.to || '';
  toInput.dataset = sc.toComputed ? { durationComputed: '1' } : {};
  if (sc.fireInput) { toInput._fire('input'); }
  sel.value = sc.duration;
  sel._handler.call(sel);
  var res = { duration: sc.duration, from: fromInput.value, to: toInput.value, toComputed: !!(toInput.dataset && toInput.dataset.durationComputed) };
  res.rows = [];
  for (var r = 0; r < 5; r++) { res.rows.push(rowState(r)); }
  return res;
});
console.log(JSON.stringify(out));
`

// expiryExtractScript returns the <script> block of the rendered page
// that contains marker (the promo page has two script blocks).
func expiryExtractScript(t *testing.T, body, marker string) string {
	t.Helper()
	rest := body
	for {
		start := strings.Index(rest, "<script>")
		if start < 0 {
			t.Fatalf("no <script> block containing %q found", marker)
		}
		end := strings.Index(rest[start:], "</script>")
		if end < 0 {
			t.Fatalf("unterminated <script> block")
		}
		script := rest[start+len("<script>") : start+end]
		if strings.Contains(script, marker) {
			return script
		}
		rest = rest[start+end+len("</script>"):]
	}
}

// expiryRunJS executes the extracted template script in node for every
// scenario and returns the resulting from/to pairs.
func expiryRunJS(t *testing.T, script string, scenarios []expiryScenario) []expiryResult {
	t.Helper()
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "duration.js")
	scenPath := filepath.Join(dir, "scenarios.json")
	harnessPath := filepath.Join(dir, "harness.js")
	scenJSON, err := json.Marshal(scenarios)
	if err != nil {
		t.Fatalf("marshal scenarios: %v", err)
	}
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	if err := os.WriteFile(scenPath, scenJSON, 0o600); err != nil {
		t.Fatalf("write scenarios: %v", err)
	}
	if err := os.WriteFile(harnessPath, []byte(expiryNodeHarness), 0o600); err != nil {
		t.Fatalf("write harness: %v", err)
	}
	out, err := exec.Command("node", harnessPath, scriptPath, scenPath).CombinedOutput()
	if err != nil {
		t.Fatalf("node harness: %v (output %q)", err, string(out))
	}
	var results []expiryResult
	if err := json.Unmarshal(out, &results); err != nil {
		t.Fatalf("parse node output %q: %v", string(out), err)
	}
	if len(results) != len(scenarios) {
		t.Fatalf("node returned %d results, want %d (output %q)", len(results), len(scenarios), string(out))
	}
	return results
}

// expiryScenarios builds the full matrix for one form: all 8 durations
// with from set, all 8 with from empty (duration-only), plus the
// month-end edges.
func expiryScenarios(fromSel, toSel string) []expiryScenario {
	var scs []expiryScenario
	for _, d := range expiryDurations {
		scs = append(scs, expiryScenario{Duration: d.value, From: "2026-01-15", FromSel: fromSel, ToSel: toSel})
		scs = append(scs, expiryScenario{Duration: d.value, From: "", FromSel: fromSel, ToSel: toSel})
	}
	for _, e := range []struct{ from, dur string }{
		{"2026-01-31", "1m"},
		{"2026-03-31", "1m"},
		{"2026-01-31", "2m"},
		{"2026-01-31", "12m"},
	} {
		scs = append(scs, expiryScenario{Duration: e.dur, From: e.from, FromSel: fromSel, ToSel: toSel})
	}
	return scs
}

func expiryRowPtr(row int) *int { return &row }

// expiryScenariosRow is expiryScenarios with every scenario bound to
// one purchase-form row.
func expiryScenariosRow(fromSel, toSel string, row int) []expiryScenario {
	scs := expiryScenarios(fromSel, toSel)
	for i := range scs {
		scs[i].Row = expiryRowPtr(row)
	}
	return scs
}

// expiryCheckJSResults compares node results against the reference:
// from set -> to = from + duration, from kept; from empty -> from filled
// with today, to = today + duration.
func expiryCheckJSResults(t *testing.T, what string, scs []expiryScenario, results []expiryResult, today time.Time) {
	t.Helper()
	todayStr := today.Format("2006-01-02")
	for i, sc := range scs {
		got := results[i]
		var wantFrom, wantTo string
		if sc.From == "" {
			wantFrom = todayStr
			base := today
			for _, d := range expiryDurations {
				if d.value == sc.Duration {
					wantTo = expiryRefTo(base, d)
				}
			}
		} else {
			wantFrom = sc.From
			base, err := time.ParseInLocation("2006-01-02", sc.From, time.Local)
			if err != nil {
				t.Fatalf("%s scenario %d: parse from %q: %v", what, i, sc.From, err)
			}
			for _, d := range expiryDurations {
				if d.value == sc.Duration {
					wantTo = expiryRefTo(base, d)
				}
			}
		}
		if got.From != wantFrom || got.To != wantTo {
			t.Fatalf("%s duration %q from %q: got from=%q to=%q, want from=%q to=%q",
				what, sc.Duration, sc.From, got.From, got.To, wantFrom, wantTo)
		}
	}
	// Non-circular pins for the headline month-end edges.
	for i, sc := range scs {
		if sc.From == "2026-01-31" && sc.Duration == "1m" && results[i].To != "2026-02-28" {
			t.Fatalf("%s 31 Jan + 1m: to = %q, want 2026-02-28 (not 3 Mar)", what, results[i].To)
		}
		if sc.From == "2026-03-31" && sc.Duration == "1m" && results[i].To != "2026-04-30" {
			t.Fatalf("%s 31 Mar + 1m: to = %q, want 2026-04-30", what, results[i].To)
		}
	}
}

func TestExpiryRules_PurchaseDurationJS(t *testing.T) {
	e, admin, _, _ := fix25BEnv(t)
	e.createProduct(admin, "PF-EX1", "سكر", "15.00", "100")
	code, body := e.get(admin, "/purchases/new")
	if code != http.StatusOK {
		t.Fatalf("GET /purchases/new: status = %d, want 200", code)
	}
	if n := strings.Count(body, `class="expiry-duration"`); n != 5 {
		t.Fatalf("purchase duration selects = %d, want 5", n)
	}
	script := expiryExtractScript(t, body, "expiry-duration")
	if !strings.Contains(script, "fromInput.value = fmt(today)") {
		t.Fatalf("purchase duration script does not fill from with today when from is empty (rule 3)")
	}
	if strings.Contains(script, "//") {
		t.Fatalf("purchase duration script contains a // comment marker")
	}
	today := time.Now()
	scs := expiryScenariosRow(`input[name="valid_from"]`, `input[name="expiry"]`, 0)
	results := expiryRunJS(t, script, scs)
	expiryCheckJSResults(t, "purchase", scs, results, today)
}

func TestExpiryRules_ProductDurationJS(t *testing.T) {
	e, admin, _, _ := fix25BEnv(t)
	code, body := e.get(admin, "/products/new")
	if code != http.StatusOK {
		t.Fatalf("GET /products/new: status = %d, want 200", code)
	}
	for _, want := range []string{
		`type="date" name="expiry_from"`,
		`name="expiry_date" placeholder="YYYY-MM-DD"`,
		`class="expiry-duration"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("product form lacks %q", want)
		}
	}
	for _, want := range expiryDurationOptions {
		if !strings.Contains(body, want) {
			t.Fatalf("product form lacks duration option %q", want)
		}
	}
	script := expiryExtractScript(t, body, "expiry-duration")
	for _, want := range []string{"addMonths", "new Date(y, m + 1, 0)", "fromInput.value = fmt(today)", `input[name="expiry_from"]`, `input[name="expiry_date"]`} {
		if !strings.Contains(script, want) {
			t.Fatalf("product duration script lacks %q", want)
		}
	}
	if strings.Contains(script, "//") {
		t.Fatalf("product duration script contains a // comment marker")
	}
	today := time.Now()
	scs := expiryScenarios(`input[name="expiry_from"]`, `input[name="expiry_date"]`)
	results := expiryRunJS(t, script, scs)
	expiryCheckJSResults(t, "product", scs, results, today)
}

func TestExpiryRules_PromoDurationJS(t *testing.T) {
	e, admin, _, _ := fix25BEnv(t)
	code, body := e.get(admin, "/promotions/new")
	if code != http.StatusOK {
		t.Fatalf("GET /promotions/new: status = %d, want 200", code)
	}
	for _, want := range []string{
		`name="start_at"`,
		`name="end_at"`,
		`class="offer-duration"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("promo form lacks %q", want)
		}
	}
	for _, want := range expiryDurationOptions {
		if !strings.Contains(body, want) {
			t.Fatalf("promo form lacks duration option %q", want)
		}
	}
	script := expiryExtractScript(t, body, "offer-duration")
	for _, want := range []string{"addMonths", "new Date(y, m + 1, 0)", "startInput.value = fmt(today)", `input[name="start_at"]`, `input[name="end_at"]`} {
		if !strings.Contains(script, want) {
			t.Fatalf("promo duration script lacks %q", want)
		}
	}
	if strings.Contains(script, "//") {
		t.Fatalf("promo duration script contains a // comment marker")
	}
	today := time.Now()
	scs := expiryScenarios(`input[name="start_at"]`, `input[name="end_at"]`)
	results := expiryRunJS(t, script, scs)
	expiryCheckJSResults(t, "promo", scs, results, today)

	// Promo start may carry a time part ("YYYY-MM-DD HH:MM:SS"): the
	// end is still computed from the date part and start is left as typed.
	scs2 := []expiryScenario{{Duration: "1m", From: "2026-01-15 10:00:00", FromSel: `input[name="start_at"]`, ToSel: `input[name="end_at"]`}}
	res2 := expiryRunJS(t, script, scs2)
	if res2[0].From != "2026-01-15 10:00:00" || res2[0].To != "2026-02-15" {
		t.Fatalf("promo start with time part: got from=%q to=%q, want from=2026-01-15 10:00:00 to=2026-02-15", res2[0].From, res2[0].To)
	}
}

func expiryQueryString(t *testing.T, e *e2eEnv, query string, args ...any) string {
	t.Helper()
	var v *string
	if err := e.store.DB.QueryRow(query, args...).Scan(&v); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	if v == nil {
		return ""
	}
	return *v
}

// expiryCreateProduct posts the product form as the given client and
// returns the created product id parsed from /products/{id}.
func expiryCreateProduct(t *testing.T, e *e2eEnv, c *http.Client, barcode, name, expiry string) int64 {
	t.Helper()
	v := url.Values{
		"name": {name}, "barcode": {barcode}, "buy_price": {"5.00"},
		"bulk_price": {"7.00"}, "wholesale_price": {"8.00"},
		"retail_price": {"15.00"}, "quantity": {"100"},
		"min_quantity": {"1"}, "unit": {"قطعة"},
	}
	if expiry != "" {
		v.Set("expiry_date", expiry)
	}
	code, loc, body := e.postFormLoc(c, "/products/new", v)
	if code != http.StatusSeeOther {
		t.Fatalf("create product %s (expiry %q): want 303, got %d body=%q", name, expiry, code, truncate(body, 300))
	}
	idStr := strings.TrimPrefix(loc, "/products/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id == 0 {
		t.Fatalf("create product %s: bad redirect %q", name, loc)
	}
	return id
}

func expiryProductFormValues(name, barcode, expiry string) url.Values {
	return url.Values{
		"name": {name}, "barcode": {barcode}, "buy_price": {"5.00"},
		"bulk_price": {"7.00"}, "wholesale_price": {"8.00"},
		"retail_price": {"15.00"}, "quantity": {"100"},
		"min_quantity": {"1"}, "unit": {"قطعة"}, "expiry_date": {expiry},
	}
}

func TestExpiryRules_PurchaseEmptySavesAndFamiliesRoundTrip(t *testing.T) {
	e, admin, _, _ := fix25BEnv(t)
	vid := fix25BVendor(t, e, admin, "مورد صلاحية قواعد")
	e.createProduct(admin, "PF-EX2", "سكر", "15.00", "100")

	// Rule 1: no from, no to — saves fine, details empty, no batch.
	code, _, _ := e.postFormLoc(admin, "/purchases/new", url.Values{
		"vendor_id": {fmt.Sprint(vid)}, "paid": {"0"}, "method": {"Visa"},
		"product": {"سكر"}, "qty": {"1"}, "price": {"10.00"},
	})
	if code != http.StatusSeeOther {
		t.Fatalf("POST /purchases/new with no expiry: status = %d, want 303", code)
	}
	if got := expiryQueryString(t, e, `SELECT ExpiryDate FROM PurchaseInvoiceDetails WHERE PurchaseID=(SELECT MAX(PurchaseID) FROM PurchaseInvoices)`); got != "" {
		t.Fatalf("empty-expiry details ExpiryDate = %q, want empty", got)
	}
	if got := expiryQueryString(t, e, `SELECT ValidFrom FROM PurchaseInvoiceDetails WHERE PurchaseID=(SELECT MAX(PurchaseID) FROM PurchaseInvoices)`); got != "" {
		t.Fatalf("empty-expiry details ValidFrom = %q, want empty", got)
	}
	if n := e.queryInt(`SELECT COUNT(*) FROM ProductBatches WHERE PurchaseID=(SELECT MAX(PurchaseID) FROM PurchaseInvoices)`); n != 0 {
		t.Fatalf("empty-expiry ProductBatches rows = %d, want 0", n)
	}

	// Days family round-trip (what the JS computes for أسبوع):
	// from 2026-01-15 + 7d -> to 2026-01-22, stored and displayed.
	code, loc, _ := e.postFormLoc(admin, "/purchases/new", url.Values{
		"vendor_id": {fmt.Sprint(vid)}, "paid": {"0"}, "method": {"Visa"},
		"product": {"سكر"}, "qty": {"1"}, "price": {"10.00"},
		"batch": {"EX-D7"}, "valid_from": {"2026-01-15"}, "expiry": {"2026-01-22"},
	})
	if code != http.StatusSeeOther {
		t.Fatalf("POST /purchases/new days family: status = %d, want 303", code)
	}
	if got := expiryQueryString(t, e, `SELECT ExpiryDate FROM PurchaseInvoiceDetails WHERE PurchaseID=(SELECT MAX(PurchaseID) FROM PurchaseInvoices)`); got != "2026-01-22" {
		t.Fatalf("days-family ExpiryDate = %q, want 2026-01-22", got)
	}
	code, body := e.get(admin, loc)
	if code != http.StatusOK || !strings.Contains(body, "من 2026-01-15 إلى 2026-01-22") {
		t.Fatalf("days-family detail lacks from-to range (status %d): %q", code, truncate(body, 400))
	}

	// Months family round-trip (what the JS computes for شهر):
	// from 2026-01-15 + 1m -> to 2026-02-15, stored and displayed.
	code, loc, _ = e.postFormLoc(admin, "/purchases/new", url.Values{
		"vendor_id": {fmt.Sprint(vid)}, "paid": {"0"}, "method": {"Visa"},
		"product": {"سكر"}, "qty": {"1"}, "price": {"10.00"},
		"batch": {"EX-M1"}, "valid_from": {"2026-01-15"}, "expiry": {"2026-02-15"},
	})
	if code != http.StatusSeeOther {
		t.Fatalf("POST /purchases/new months family: status = %d, want 303", code)
	}
	if got := expiryQueryString(t, e, `SELECT ExpiryDate FROM PurchaseInvoiceDetails WHERE PurchaseID=(SELECT MAX(PurchaseID) FROM PurchaseInvoices)`); got != "2026-02-15" {
		t.Fatalf("months-family ExpiryDate = %q, want 2026-02-15", got)
	}
	code, body = e.get(admin, loc)
	if code != http.StatusOK || !strings.Contains(body, "من 2026-01-15 إلى 2026-02-15") {
		t.Fatalf("months-family detail lacks from-to range (status %d): %q", code, truncate(body, 400))
	}
}

func TestExpiryRules_ProductEmptyAndManualRoundTrip(t *testing.T) {
	e, admin, _, _ := fix25BEnv(t)

	// Rule 1: create with no expiry_date saves fine, stored empty.
	id := expiryCreateProduct(t, e, admin, "EX-P1", "منتج بلا صلاحية", "")
	if got := expiryQueryString(t, e, `SELECT COALESCE(ExpiryDate,'') FROM Products WHERE ProductID=?`, id); got != "" {
		t.Fatalf("created product ExpiryDate = %q, want empty", got)
	}

	// Manual expiry entry still works.
	id2 := expiryCreateProduct(t, e, admin, "EX-P2", "منتج صلاحية يدوية", "2027-05-01")
	if got := expiryQueryString(t, e, `SELECT COALESCE(ExpiryDate,'') FROM Products WHERE ProductID=?`, id2); got != "2027-05-01" {
		t.Fatalf("manual-expiry product ExpiryDate = %q, want 2027-05-01", got)
	}

	// The UI-only from helper field posts along harmlessly: it is not
	// a stored column and must not break the save.
	v := expiryProductFormValues("منتج مع من", "EX-P3", "2027-06-01")
	v.Set("expiry_from", "2026-06-01")
	code, loc, body := e.postFormLoc(admin, "/products/new", v)
	if code != http.StatusSeeOther {
		t.Fatalf("create product with expiry_from posted: want 303, got %d body=%q", code, truncate(body, 300))
	}
	id3, _ := strconv.ParseInt(strings.TrimPrefix(loc, "/products/"), 10, 64)
	if got := expiryQueryString(t, e, `SELECT COALESCE(ExpiryDate,'') FROM Products WHERE ProductID=?`, id3); got != "2027-06-01" {
		t.Fatalf("product with expiry_from ExpiryDate = %q, want 2027-06-01", got)
	}

	// Edit: clearing the expiry stores empty again.
	v = expiryProductFormValues("منتج صلاحية يدوية", "EX-P2", "")
	v.Set("is_active", "1")
	code, _, body = e.postFormLoc(admin, fmt.Sprintf("/products/%d/edit", id2), v)
	if code != http.StatusSeeOther {
		t.Fatalf("edit product clearing expiry: want 303, got %d body=%q", code, truncate(body, 300))
	}
	if got := expiryQueryString(t, e, `SELECT COALESCE(ExpiryDate,'') FROM Products WHERE ProductID=?`, id2); got != "" {
		t.Fatalf("edited product ExpiryDate = %q, want empty after clear", got)
	}
}

func expiryCreateOffer(t *testing.T, e *e2eEnv, c *http.Client, pid int64, name, startAt, endAt string) {
	t.Helper()
	v := url.Values{
		"name": {name}, "type": {"product_pct"}, "percent": {"10"},
		"is_active": {"1"}, "target_type": {"product"},
		"target_id": {strconv.FormatInt(pid, 10)}, "target_name": {""}, "target_qty": {"1"},
	}
	if startAt != "" {
		v.Set("start_at", startAt)
	}
	if endAt != "" {
		v.Set("end_at", endAt)
	}
	code, loc, body := e.postFormLoc(c, "/promotions/new", v)
	if code != http.StatusSeeOther || loc != "/promotions" {
		t.Fatalf("create offer %s (start %q end %q): want 303 to /promotions, got %d loc=%q body=%q",
			name, startAt, endAt, code, loc, truncate(body, 300))
	}
}

func TestExpiryRules_PromoEmptyAndWindowRoundTrip(t *testing.T) {
	e, admin, _, _ := fix25BEnv(t)
	pid := e.createProduct(admin, "EX-O1", "سكر عرض", "50.00", "100")

	// Rule 1: no start, no end — saves fine, window stored empty.
	expiryCreateOffer(t, e, admin, pid, "عرض بلا مدة", "", "")
	if got := expiryQueryString(t, e, `SELECT COALESCE(StartAt,'') FROM Offers WHERE Name=?`, "عرض بلا مدة"); got != "" {
		t.Fatalf("empty-window offer StartAt = %q, want empty", got)
	}
	if got := expiryQueryString(t, e, `SELECT COALESCE(EndAt,'') FROM Offers WHERE Name=?`, "عرض بلا مدة"); got != "" {
		t.Fatalf("empty-window offer EndAt = %q, want empty", got)
	}

	// Start + end round-trip (what the JS computes for شهر from today
	// is covered by the JS test; here the stored window is verified).
	// Stored values are normalized to datetimes: start 00:00:00, end
	// 23:59:59 of the given days.
	expiryCreateOffer(t, e, admin, pid, "عرض بمدة", "2026-01-15", "2026-02-15")
	if got := expiryQueryString(t, e, `SELECT COALESCE(StartAt,'') FROM Offers WHERE Name=?`, "عرض بمدة"); !strings.HasPrefix(got, "2026-01-15") {
		t.Fatalf("window offer StartAt = %q, want prefix 2026-01-15", got)
	}
	if got := expiryQueryString(t, e, `SELECT COALESCE(EndAt,'') FROM Offers WHERE Name=?`, "عرض بمدة"); !strings.HasPrefix(got, "2026-02-15") {
		t.Fatalf("window offer EndAt = %q, want prefix 2026-02-15", got)
	}
	if !strings.Contains(e.getBody(admin, "/promotions"), "عرض بمدة") {
		t.Fatalf("created offer missing from /promotions list")
	}

	// End-only (direct POST, JS bypassed) saves without error.
	expiryCreateOffer(t, e, admin, pid, "عرض نهاية فقط", "", "2027-01-15")
	if got := expiryQueryString(t, e, `SELECT COALESCE(StartAt,'') FROM Offers WHERE Name=?`, "عرض نهاية فقط"); got != "" {
		t.Fatalf("end-only offer StartAt = %q, want empty", got)
	}
	if got := expiryQueryString(t, e, `SELECT COALESCE(EndAt,'') FROM Offers WHERE Name=?`, "عرض نهاية فقط"); !strings.HasPrefix(got, "2027-01-15") {
		t.Fatalf("end-only offer EndAt = %q, want prefix 2027-01-15", got)
	}
}

func TestExpiryRules_ExpiredProductStillBlockedAtSale(t *testing.T) {
	e, admin, _, _ := fix25BEnv(t)
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	pid := expiryCreateProduct(t, e, admin, "EX-BLK", "منتج منتهٍ", yesterday)
	cash := e.cashierClient()
	e.openShift(cash, 100000)
	code, body := e.sell(cash, map[string]any{
		"sale_type": "retail",
		"lines":     []map[string]any{{"product_id": pid, "qty": "1"}},
		"paid":      1500, "payment_method": "Cash",
	})
	if code != http.StatusBadRequest || body["ok"] != false ||
		!strings.Contains(fmt.Sprint(body["error"]), "منتهي الصلاحية") {
		t.Fatalf("expired-product sale: want 400 ok=false + منتهي الصلاحية, got %d body=%v", code, body)
	}
	if n := e.queryInt(`SELECT COUNT(*) FROM Invoices`); n != 0 {
		t.Fatalf("expired-product sale created %d invoice(s), want 0", n)
	}
}

// Fix-round-2 L1 (web L1): clearing the duration clears the to/end
// field ONLY when that value was computed by a previous duration pick
// (tracked with a data-duration-computed flag); a manually typed date
// survives, and a manual edit after a pick un-marks the computed value.
func TestExpiryRules_DurationClearClearsOnlyComputedEnd(t *testing.T) {
	e, admin, _, _ := fix25BEnv(t)
	e.createProduct(admin, "PF-EX2", "سكر", "15.00", "100")
	forms := []struct {
		name    string
		url     string
		marker  string
		fromSel string
		toSel   string
		row     *int
	}{
		{"purchase", "/purchases/new", "expiry-duration", `input[name="valid_from"]`, `input[name="expiry"]`, expiryRowPtr(0)},
		{"product", "/products/new", "expiry-duration", `input[name="expiry_from"]`, `input[name="expiry_date"]`, nil},
		{"promo", "/promotions/new", "offer-duration", `input[name="start_at"]`, `input[name="end_at"]`, nil},
	}
	for _, f := range forms {
		code, body := e.get(admin, f.url)
		if code != http.StatusOK {
			t.Fatalf("%s form: GET %s status = %d, want 200", f.name, f.url, code)
		}
		script := expiryExtractScript(t, body, f.marker)
		scs := []expiryScenario{
			// Computed end + duration cleared -> end cleared.
			{Duration: "", From: "2026-01-15", FromSel: f.fromSel, ToSel: f.toSel, To: "2027-01-15", ToComputed: true, Row: f.row},
			// Manually typed end + duration cleared -> end preserved.
			{Duration: "", From: "2026-01-15", FromSel: f.fromSel, ToSel: f.toSel, To: "2027-01-15", ToComputed: false, Row: f.row},
			// Picking a duration computes the end and marks it computed.
			{Duration: "7", From: "2026-01-15", FromSel: f.fromSel, ToSel: f.toSel, Row: f.row},
			// Manual edit after a computed pick un-marks it, so clearing
			// the duration afterwards preserves the edited date.
			{Duration: "", From: "2026-01-15", FromSel: f.fromSel, ToSel: f.toSel, To: "2027-01-15", ToComputed: true, FireInput: true, Row: f.row},
		}
		results := expiryRunJS(t, script, scs)
		if results[0].To != "" || results[0].ToComputed {
			t.Fatalf("%s clear-computed: got to=%q computed=%v, want to empty + flag cleared", f.name, results[0].To, results[0].ToComputed)
		}
		if results[1].To != "2027-01-15" {
			t.Fatalf("%s clear-manual: got to=%q, want the manually typed 2027-01-15 preserved", f.name, results[1].To)
		}
		if results[2].To != "2026-01-22" || !results[2].ToComputed {
			t.Fatalf("%s pick: got to=%q computed=%v, want to=2026-01-22 computed=true", f.name, results[2].To, results[2].ToComputed)
		}
		if results[3].To != "2027-01-15" {
			t.Fatalf("%s clear-after-manual-edit: got to=%q, want the edited 2027-01-15 preserved", f.name, results[3].To)
		}
	}
}

// Fix-round-2 L2 (web L2): promo start is a free-text input, so an
// unparseable start must NOT fall back to today for the end — the end
// stays as-is. Product/purchase are immune: their from-inputs are
// type="date", which the browser only ever yields empty or valid.
func TestExpiryRules_PromoGarbageStartLeavesEndAlone(t *testing.T) {
	e, admin, _, _ := fix25BEnv(t)
	code, body := e.get(admin, "/promotions/new")
	if code != http.StatusOK {
		t.Fatalf("GET /promotions/new: status = %d, want 200", code)
	}
	script := expiryExtractScript(t, body, "offer-duration")
	fromSel := `input[name="start_at"]`
	toSel := `input[name="end_at"]`
	scs := []expiryScenario{
		{Duration: "7", From: "garbage", FromSel: fromSel, ToSel: toSel},
		{Duration: "7", From: "2026-02-30", FromSel: fromSel, ToSel: toSel},
		{Duration: "7", From: "2026-01-15", FromSel: fromSel, ToSel: toSel},
		{Duration: "7", From: "", FromSel: fromSel, ToSel: toSel},
	}
	results := expiryRunJS(t, script, scs)
	if results[0].From != "garbage" || results[0].To != "" {
		t.Fatalf("promo garbage start: got from=%q to=%q, want from=garbage to empty (no today fallback)", results[0].From, results[0].To)
	}
	if results[1].To != "" {
		t.Fatalf("promo 2026-02-30 start: got to=%q, want empty (not a real calendar date)", results[1].To)
	}
	if results[2].To != "2026-01-22" {
		t.Fatalf("promo valid start: got to=%q, want 2026-01-22", results[2].To)
	}
	todayStr := time.Now().Format("2006-01-02")
	wantTo := expiryRefTo(time.Now(), expiryDuration{value: "7", days: 7})
	if results[3].From != todayStr || results[3].To != wantTo {
		t.Fatalf("promo empty start: got from=%q to=%q, want from=%s to=%s (empty still means today)", results[3].From, results[3].To, todayStr, wantTo)
	}

	// Immunity check: product/purchase from-inputs are type="date", so
	// the browser can never hand the handler an unparseable value.
	for _, url := range []string{"/purchases/new", "/products/new"} {
		code, body := e.get(admin, url)
		if code != http.StatusOK {
			t.Fatalf("GET %s: status = %d, want 200", url, code)
		}
		want := `type="date" name="valid_from"`
		if url == "/products/new" {
			want = `type="date" name="expiry_from"`
		}
		if !strings.Contains(body, want) {
			t.Fatalf("%s lacks %q — from-input is not type=date, garbage-start fallback possible", url, want)
		}
	}
}

// Fix-round-3 WEB LOW-1: the purchase harness used to stub
// querySelectorAll as a single select with one shared container, so
// TestExpiryRules_PurchaseDurationJS executed single-row logic only.
// The harness now returns 5 row-scoped selects/inputs; this test
// proves per-row isolation across all 5 purchase rows.
func TestExpiryRules_PurchaseDurationJS_PerRowIsolation(t *testing.T) {
	e, admin, _, _ := fix25BEnv(t)
	e.createProduct(admin, "PF-EX1", "سكر", "15.00", "100")
	code, body := e.get(admin, "/purchases/new")
	if code != http.StatusOK {
		t.Fatalf("GET /purchases/new: status = %d, want 200", code)
	}
	script := expiryExtractScript(t, body, "expiry-duration")
	today := time.Now()
	todayStr := today.Format("2006-01-02")
	fromSel := `input[name="valid_from"]`
	toSel := `input[name="expiry"]`

	// 1. Duration-only fills from=today in ALL 5 rows.
	var scs []expiryScenario
	for r := 0; r < 5; r++ {
		scs = append(scs, expiryScenario{Duration: "7", From: "", FromSel: fromSel, ToSel: toSel, Row: expiryRowPtr(r)})
	}
	results := expiryRunJS(t, script, scs)
	wantTo := expiryRefTo(today, expiryDuration{value: "7", days: 7})
	for r := 0; r < 5; r++ {
		if got := results[r]; got.From != todayStr || got.To != wantTo {
			t.Fatalf("row %d duration-only: got from=%q to=%q, want from=%s to=%s", r, got.From, got.To, todayStr, wantTo)
		}
	}
	for r := 0; r < 5; r++ {
		if got := results[4].Rows[r]; got.From != todayStr || got.To != wantTo {
			t.Fatalf("after all 5 rows: row %d snapshot from=%q to=%q, want from=%s to=%s", r, got.From, got.To, todayStr, wantTo)
		}
	}

	// 2. Picking in one row leaves the other rows untouched.
	scs = []expiryScenario{
		{Duration: "7", From: "2026-01-15", FromSel: fromSel, ToSel: toSel, Row: expiryRowPtr(0)},
		{Duration: "1m", From: "2026-03-10", FromSel: fromSel, ToSel: toSel, Row: expiryRowPtr(2)},
	}
	results = expiryRunJS(t, script, scs)
	snap := results[1].Rows
	if snap[0].From != "2026-01-15" || snap[0].To != "2026-01-22" {
		t.Fatalf("pick in row 2 changed row 0: row 0 from=%q to=%q, want 2026-01-15/2026-01-22", snap[0].From, snap[0].To)
	}
	if snap[2].From != "2026-03-10" || snap[2].To != "2026-04-10" {
		t.Fatalf("row 2 pick: got from=%q to=%q, want 2026-03-10/2026-04-10", snap[2].From, snap[2].To)
	}
	for _, r := range []int{1, 3, 4} {
		if snap[r].From != "" || snap[r].To != "" {
			t.Fatalf("picks in rows 0/2 touched row %d: from=%q to=%q, want empty", r, snap[r].From, snap[r].To)
		}
	}

	// 3. Clearing in one row leaves the other rows' from/to/flag untouched.
	scs = []expiryScenario{
		{Duration: "7", From: "2026-01-15", FromSel: fromSel, ToSel: toSel, Row: expiryRowPtr(1)},
		{Duration: "14", From: "2026-02-01", FromSel: fromSel, ToSel: toSel, Row: expiryRowPtr(3)},
		{Duration: "", From: "2026-01-15", FromSel: fromSel, ToSel: toSel, To: "2026-01-22", ToComputed: true, Row: expiryRowPtr(1)},
	}
	results = expiryRunJS(t, script, scs)
	if got := results[2]; got.To != "" || got.ToComputed {
		t.Fatalf("clear row 1: got to=%q computed=%v, want to empty + flag cleared", got.To, got.ToComputed)
	}
	snap = results[2].Rows
	if snap[1].From != "2026-01-15" {
		t.Fatalf("clear row 1 also cleared its from: from=%q, want 2026-01-15", snap[1].From)
	}
	if snap[3].From != "2026-02-01" || snap[3].To != "2026-02-15" || !snap[3].ToComputed {
		t.Fatalf("clear row 1 touched row 3: from=%q to=%q computed=%v, want 2026-02-01/2026-02-15/true", snap[3].From, snap[3].To, snap[3].ToComputed)
	}

	// 4. A manual value in row 1 survives a clear in row 2.
	scs = []expiryScenario{
		{Duration: "", From: "2026-01-15", FromSel: fromSel, ToSel: toSel, To: "2027-05-05", ToComputed: false, Row: expiryRowPtr(1)},
		{Duration: "14", From: "2026-02-01", FromSel: fromSel, ToSel: toSel, Row: expiryRowPtr(2)},
		{Duration: "", From: "2026-02-01", FromSel: fromSel, ToSel: toSel, To: "2026-02-15", ToComputed: true, Row: expiryRowPtr(2)},
	}
	results = expiryRunJS(t, script, scs)
	if got := results[2]; got.To != "" {
		t.Fatalf("clear row 2: got to=%q, want empty", got.To)
	}
	if got := results[2].Rows[1].To; got != "2027-05-05" {
		t.Fatalf("manual to in row 1 lost after clearing row 2: got %q, want 2027-05-05", got)
	}
	if got := results[2].Rows[1].From; got != "2026-01-15" {
		t.Fatalf("manual row 1 from changed after clearing row 2: got %q, want 2026-01-15", got)
	}
}

// Fix-round-3 WEB LOW-2: the promo strict parse did not trim the start
// value, so a leading space (" 2026-01-15", e.g. pasted) yielded an
// empty date part, failed the strict check, and silently computed
// nothing. The value is now trimmed before splitting.
func TestExpiryRules_PromoTrimmedStartComputesEnd(t *testing.T) {
	e, admin, _, _ := fix25BEnv(t)
	code, body := e.get(admin, "/promotions/new")
	if code != http.StatusOK {
		t.Fatalf("GET /promotions/new: status = %d, want 200", code)
	}
	script := expiryExtractScript(t, body, "offer-duration")
	fromSel := `input[name="start_at"]`
	toSel := `input[name="end_at"]`
	scs := []expiryScenario{
		{Duration: "1m", From: " 2026-01-15", FromSel: fromSel, ToSel: toSel},
		{Duration: "7", From: "2026-01-15 ", FromSel: fromSel, ToSel: toSel},
		{Duration: "7", From: " garbage ", FromSel: fromSel, ToSel: toSel},
	}
	results := expiryRunJS(t, script, scs)
	if results[0].From != " 2026-01-15" || results[0].To != "2026-02-15" {
		t.Fatalf("promo leading-space start: got from=%q to=%q, want from=%q to=2026-02-15", results[0].From, results[0].To, " 2026-01-15")
	}
	if results[1].To != "2026-01-22" {
		t.Fatalf("promo trailing-space start: got to=%q, want 2026-01-22", results[1].To)
	}
	if results[2].From != " garbage " || results[2].To != "" {
		t.Fatalf("promo garbage start with spaces: got from=%q to=%q, want from unchanged to empty", results[2].From, results[2].To)
	}
}
