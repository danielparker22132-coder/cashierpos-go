// Package web_test — FIX-6 tests (Review-6, 0H / 0M / 4L).
//
// R6-L1: the two missed "new" CTAs render as btn-primary.
// R6-L2: no trailing period in any confirm()/alert() dialog string.
// R6-L4: the HTML resolve-evidence refusal on a CANCELLED order
// answers 400 like the API path, and the order stays unresolved.
// (R6-L3 is JS-only; its parsers are verified by node against the
// extracted functions — see ~/workspace/go-cashierpos-fix6/REPORT.md.)
package web_test

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestFix6_HTMLResolveCanceledOrderReturns400(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	mgr := e.managerClient()

	// Intake an evidence-awaiting order, then cancel it.
	code, data := e.postJSON(admin, "/api/staff/orders", orderBody("عميل دليل ملغى", "AWAITING_VERIFICATION"))
	if code != http.StatusCreated {
		t.Fatalf("intake evidence order: want 201, got %d %v", code, data)
	}
	order, _ := data["order"].(map[string]any)
	oid := itoa(int64(order["order_id"].(float64)))
	if code, data := e.postJSON(admin, "/api/staff/orders/"+oid+"/status", map[string]any{"status": "CANCELED"}); code != http.StatusOK {
		t.Fatalf("cancel: want 200, got %d %v", code, data)
	}

	// The HTML resolve form must refuse with 400 (FIX-6 R6-L4), not 200.
	if code, body := e.postForm(mgr, "/orders/"+oid+"/resolve-evidence", nil); code != http.StatusBadRequest {
		t.Fatalf("HTML resolve on cancelled order: want 400, got %d body=%q", code, truncate(body, 300))
	} else if !strings.Contains(body, "لا يمكن مراجعة دليل دفع طلب ملغى") {
		t.Fatalf("HTML resolve 400 body lacks the refusal message: %q", truncate(body, 300))
	}

	// The order stays unresolved in the DB: still CANCELED and still
	// awaiting payment review.
	if code, data := e.getJSON(admin, "/api/staff/orders/"+oid); code != http.StatusOK {
		t.Fatalf("GET order after refused resolve: want 200, got %d %v", code, data)
	} else if o, _ := data["order"].(map[string]any); o["status"] != "CANCELED" || o["payment_status"] != "AWAITING_VERIFICATION" {
		t.Fatalf("order after refused HTML resolve: want CANCELED/AWAITING_VERIFICATION, got %v", o)
	}
}

func TestFix6_NewCTAsArePrimaryButtons(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()

	if code, body := e.get(admin, "/users"); code != http.StatusOK {
		t.Fatalf("GET /users: want 200, got %d", code)
	} else if !strings.Contains(body, `<a class="btn btn-primary" href="/users/new">+ مستخدم جديد</a>`) {
		t.Fatalf("GET /users: '+ مستخدم جديد' is not a btn-primary button")
	}
	if code, body := e.get(admin, "/notifications/manage"); code != http.StatusOK {
		t.Fatalf("GET /notifications/manage: want 200, got %d", code)
	} else if !strings.Contains(body, `<a class="btn btn-primary" href="/notifications/new">+ إنشاء تنبيه</a>`) {
		t.Fatalf("GET /notifications/manage: '+ إنشاء تنبيه' is not a btn-primary button")
	}
}

// TestFix6_DialogStringsHaveNoTrailingPeriod guards R6-L2: every
// confirm()/alert() string literal in every template must not end with
// a period. Dialog strings are visible UI text, not exempt paragraphs.
func TestFix6_DialogStringsHaveNoTrailingPeriod(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Join(filepath.Dir(file), "templates")
	re := regexp.MustCompile(`(?:confirm|alert)\('([^']*)'\)`)
	var checked int
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".html") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			checked++
			if strings.HasSuffix(strings.TrimSpace(m[1]), ".") {
				t.Errorf("%s: dialog string ends with a period: %q", path, m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk templates: %v", err)
	}
	if checked == 0 {
		t.Fatal("no confirm()/alert() strings found — scan is vacuous")
	}
	t.Logf("checked %d dialog strings", checked)
}
