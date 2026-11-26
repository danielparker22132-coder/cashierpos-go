// Package web_test — FIX-7 tests (Review-7, 0H / 0M / 2L).
//
// R7-L1: the error-page title on the malformed-id branch of the HTML
// resolve-evidence handler must be proper Arabic, and no Cyrillic
// homoglyph may appear anywhere in the tree's user-facing strings.
// R7-L2 is JS-only; its pro-rata scaler is verified by node against
// the extracted functions — see ~/workspace/go-cashierpos-fix7/REPORT.md.
package web_test

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// arabicOrdersTitle is the orders-page title built from explicit
// codepoints, so this test file itself can never carry a homoglyph:
// U+0637 U+0644 U+0628 U+0627 U+062A U+0020 U+0627 U+0644 U+0645
// U+062A U+062C U+0631.
var arabicOrdersTitle = string([]rune{
	0x0637, 0x0644, 0x0628, 0x0627, 0x062A, 0x0020,
	0x0627, 0x0644, 0x0645, 0x062A, 0x062C, 0x0631,
})

// hasCyrillic reports whether s contains any Cyrillic codepoint
// (U+0400–U+04FF). Written with rune-range comparisons only — no
// Cyrillic literal appears in this file.
func hasCyrillic(s string) bool {
	for _, r := range s {
		if r >= '\u0400' && r <= '\u04FF' {
			return true
		}
	}
	return false
}

func TestFix7_BadIDResolveEvidenceTitleIsArabic(t *testing.T) {
	e := readyEnv(t, false)
	mgr := e.managerClient()

	// A malformed id must refuse with 400 (FIX-6 R6-L4) and the error
	// page title must be proper Arabic (FIX-7 R7-L1), not the
	// Cyrillic homoglyph that fix-6 shipped.
	code, body := e.postForm(mgr, "/orders/abc/resolve-evidence", nil)
	if code != http.StatusBadRequest {
		t.Fatalf("HTML resolve on bad id: want 400, got %d body=%q", code, truncate(body, 300))
	}
	if !strings.Contains(body, "<h1>"+arabicOrdersTitle+"</h1>") {
		t.Fatalf("HTML resolve 400 body lacks the Arabic title <h1>%s</h1>: %q", arabicOrdersTitle, truncate(body, 300))
	}
	if hasCyrillic(body) {
		t.Fatalf("HTML resolve 400 body contains a Cyrillic codepoint: %q", truncate(body, 300))
	}
}

// TestFix7_NoCyrillicHomoglyphsInTree guards R7-L1 tree-wide: no
// Cyrillic codepoint may appear in any user-facing string in the
// module tree (Go sources, templates, styles, scripts, docs).
func TestFix7_NoCyrillicHomoglyphsInTree(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("go.mod not found above the test file")
		}
		root = parent
	}
	textExt := map[string]bool{
		".go": true, ".html": true, ".css": true, ".js": true,
		".md": true, ".sql": true, ".json": true, ".txt": true,
	}
	var checked, hits int
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !textExt[filepath.Ext(path)] {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		checked++
		for i, line := range strings.Split(string(b), "\n") {
			if hasCyrillic(line) {
				hits++
				t.Errorf("%s:%d: Cyrillic codepoint in user-facing text: %q", path, i+1, line)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk tree: %v", err)
	}
	if checked == 0 {
		t.Fatal("no text files scanned — scan is vacuous")
	}
	if hits != 0 {
		t.Fatalf("%d Cyrillic hit(s) in %d scanned files", hits, checked)
	}
	t.Logf("scanned %d text files, 0 Cyrillic codepoints", checked)
}
