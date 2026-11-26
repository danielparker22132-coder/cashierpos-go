// Package web is the single HTTP server: Arabic RTL HTML UI (embedded
// templates) plus a JSON API under /api. Handlers are methods on *Server;
// each domain registers its routes via registerXxxRoutes (see routes.go).
package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"strings"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/money"
	"cashierpos-go/internal/rbac"
	"cashierpos-go/internal/store"
)

//go:embed templates
var templatesFS embed.FS

//go:embed static
var staticFS embed.FS

// Server holds the store and parsed templates.
type Server struct {
	Store *store.Store
	Mux   *http.ServeMux
	tmpl  *template.Template
	// jwtSecret signs staff JWTs (FIX-3 H1); generated once per database
	// and persisted in AppSettings ("jwt_secret").
	jwtSecret string
}

type ctxKey int

const userKey ctxKey = 1

// New builds a Server and registers all routes.
func New(st *store.Store) (*Server, error) {
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"egp": func(c money.Cents) string { return money.FormatEGP(c) },
		"qty": func(q money.Qty) string { return money.FormatQty(q) },
		// approxSaleLine / approxReturn (parity ≈ markers, GAP-REPORT
		// §C G-2/G-3/G-4; see parity_markers.go): true exactly when
		// the displayed average unit price does not multiply back to
		// the exact line gross / refund. Render-time checks — the
		// multi-price flag is in-memory only and never persisted.
		"approxSaleLine": approxSaleLine,
		"approxReturn":   approxReturn,
		// avgUnit / avgApprox (parity ≈ markers, G-4 analysis):
		// truncated per-unit average of an aggregate and whether it
		// multiplies back to the exact total.
		"avgUnit":   avgUnit,
		"avgApprox": avgApprox,
		// can reports whether a user holds a permission (manager counts as
		// admin). Templates use it to hide links/cards the role lacks:
		//   {{if can .User "create_invoice"}} ... {{end}}
		// Nil user (login page) holds nothing.
		"can": func(u *auth.User, perm string) bool {
			if u == nil {
				return false
			}
			return rbac.Can(u.Role, perm)
		},
		// roleName renders the Arabic display name of a role.
		"roleName": func(role string) string { return rbac.RoleDisplayName(role) },
		// B5 fix 2026-11-25: "templates/**/*.html" matches NOTHING in Go 1.24
		// fs.Glob for files directly under templates/ (** needs a subdirectory
		// level) — web.New always failed with "pattern matches no files", so
		// the server could never start. ParseFS also errors when ANY pattern
		// matches nothing, so the subdirectory pattern is only added when
		// such files exist (domain subdirs B1-B4 will add later).
	}).ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	if matches, _ := fs.Glob(templatesFS, "templates/*/*.html"); len(matches) > 0 {
		if tmpl, err = tmpl.ParseFS(templatesFS, "templates/*/*.html"); err != nil {
			return nil, err
		}
	}
	if err != nil {
		return nil, err
	}
	s := &Server{Store: st, Mux: http.NewServeMux(), tmpl: tmpl}
	s.routes()
	// FIX-3 H1: the JWT signing secret is generated once per database
	// (a missing secret only disables token login, never the UI).
	s.jwtSecret = strings.TrimSpace(st.GetSetting("jwt_secret"))
	if s.jwtSecret == "" {
		if secret, err := auth.NewJWTSecret(); err == nil {
			if err := st.SetSetting("jwt_secret", secret); err == nil {
				s.jwtSecret = secret
			}
		}
	}
	return s, nil
}

// Handler returns the root handler (session loading + idle auto-lock
// on every request).
func (s *Server) Handler() http.Handler {
	return s.withSession(s.autoLock(s.Mux))
}

// CurrentUser returns the logged-in user for this request, or nil.
func CurrentUser(r *http.Request) *auth.User {
	u, _ := r.Context().Value(userKey).(*auth.User)
	return u
}

func (s *Server) withSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("cashierpos_session"); err == nil {
			if u := auth.UserFromSession(s.Store.DB, c.Value); u != nil {
				r = r.WithContext(context.WithValue(r.Context(), userKey, u))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// RequireLogin redirects anonymous users to /login (HTML) or 401 (API).
// A user flagged MustChangePassword is forced to /change-password on every
// protected page except the change-password page itself and /logout.
func (s *Server) RequireLogin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := CurrentUser(r)
		if u == nil {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "يلزم تسجيل الدخول"})
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if u.MustChangePassword && r.URL.Path != "/change-password" && r.URL.Path != "/logout" {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "يجب تغيير كلمة السر أولاً"})
				return
			}
			http.Redirect(w, r, "/change-password?forced=1", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// RequirePerm enforces a permission (service layer re-checks as well).
func (s *Server) RequirePerm(perm string, next http.HandlerFunc) http.HandlerFunc {
	return s.RequireLogin(func(w http.ResponseWriter, r *http.Request) {
		u := CurrentUser(r)
		if err := rbac.Require(u.Role, perm); err != nil {
			s.deny(w, r, err.Error())
			return
		}
		next(w, r)
	})
}

func (s *Server) deny(w http.ResponseWriter, r *http.Request, msg string) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": msg})
		return
	}
	w.WriteHeader(http.StatusForbidden)
	_ = s.render(w, r, "error.html", Page{Title: "غير مسموح", Error: msg})
}

// Page is the common template data.
type Page struct {
	Title       string
	User        *auth.User
	Error       string
	Notice      string
	Data        any
	DarkMode    bool
	StoreName   string
	ActiveNav   string
	MustChange  bool
	UnreadNotif int
	UnreadAudit int
	// FIX-3 H1: web-bell counts, filled centrally so every page shows them.
	PendingOrders   int
	PendingEvidence int
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, p Page) error {
	return s.renderStatus(w, r, name, p, http.StatusOK)
}

// renderStatus renders a page with an explicit HTTP status code.
func (s *Server) renderStatus(w http.ResponseWriter, r *http.Request, name string, p Page, status int) error {
	u := CurrentUser(r)
	p.User = u
	if p.StoreName == "" {
		p.StoreName = s.Store.GetSetting("store_name")
	}
	p.DarkMode = s.Store.GetSetting("dark_mode") == "1"
	if u != nil {
		p.MustChange = u.MustChangePassword
		// Bell counters, filled centrally so every page shows them.
		if n, err := s.Store.UnreadAuditAlertsCount(r.Context(), u); err == nil {
			p.UnreadAudit = n
		}
		if n, err := s.Store.UnreadNotificationCount(r.Context(), u); err == nil {
			p.UnreadNotif = n
		}
		if n, err := s.Store.PendingOrdersCount(r.Context(), u); err == nil {
			p.PendingOrders = n
		}
		if n, err := s.Store.PendingEvidenceCount(r.Context(), u); err == nil {
			p.PendingEvidence = n
		}
	}
	// FIX-1 H3: render into a buffer first. Executing straight to the
	// ResponseWriter sends a 200 header before a template error surfaces,
	// so a broken page used to answer 200 with an empty body and the error
	// was silently discarded by every `_ = s.render(...)` caller. On
	// failure log the error and answer 500 with no partial body.
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, p); err != nil {
		LogErr(r, err)
		http.Error(w, "خطأ في عرض الصفحة", http.StatusInternalServerError)
		return err
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, err := w.Write(buf.Bytes())
	return err
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteJSON is the exported helper for handlers.
func WriteJSON(w http.ResponseWriter, status int, v any) { writeJSON(w, status, v) }

// LogErr logs a server-side error (English, short).
func LogErr(r *http.Request, err error) {
	log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
}
