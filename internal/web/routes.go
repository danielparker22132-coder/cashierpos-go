package web

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"time"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/money"
	"cashierpos-go/internal/rbac"
	"cashierpos-go/internal/store"
)

// routes registers the core routes plus all four domain route groups.
// (Coordinator wiring 2026-11-25: all four register*Routes delivered.)
func (s *Server) routes() {
	s.Mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "app": "cashierpos-go"})
	})
	// B5 fix 2026-11-25: //go:embed static keeps the "static/" prefix inside
	// the FS, so FileServer must see the full "/static/..." path. The old
	// StripPrefix made every static file 404 (it looked up "style.css"
	// while the FS holds "static/style.css").
	s.Mux.Handle("/static/", http.FileServer(http.FS(staticFS)))
	s.Mux.HandleFunc("/login", s.handleLogin)
	s.Mux.HandleFunc("/logout", s.RequireLogin(s.handleLogout))
	s.Mux.HandleFunc("/change-password", s.RequireLogin(s.handleChangePassword))
	s.registerSalesRoutes(s.Mux)
	s.registerShiftsRoutes(s.Mux)
	s.registerCustomersRoutes(s.Mux)
	s.registerAdminRoutes(s.Mux)
	s.registerLockRoutes(s.Mux) // G4 auto-lock (coordinator wiring 2026-11-25)
	s.registerOrdersRoutes(s.Mux) // FIX-3 H1: staff online orders (web bell)
	s.Mux.HandleFunc("/", s.RequireLogin(s.handleHub))
}

// LoginForm carries the login page data (username is echoed back on failure
// so the user does not have to retype it; the password never is).
type LoginForm struct {
	Username string
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if CurrentUser(r) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if r.Method == http.MethodPost {
		username := strings.TrimSpace(r.FormValue("username"))
		password := r.FormValue("password")
		u, err := auth.Authenticate(s.Store.DB, username, password)
		if err != nil {
			// Generic failure for unknown user / wrong password (L-01: no
			// user enumeration); empty-field messages come from
			// auth.Authenticate (L-02). FIX-27 R27-C-F2/E-F1: gate
			// through safeHTMLErr — Authenticate returns the raw
			// driver error for a DB failure (e.g. "no such table:
			// Users"), which must never reach the login page.
			// Safe Arabic messages (wrong password, empty fields,
			// inactive account) pass through verbatim.
			LogErr(r, err)
			_ = s.render(w, r, "login.html", Page{Title: "تسجيل الدخول", Error: safeHTMLErr(err), Data: LoginForm{Username: username}})
			return
		}
		token, err := auth.CreateSession(s.Store.DB, u)
		if err != nil {
			LogErr(r, err)
			_ = s.render(w, r, "login.html", Page{Title: "تسجيل الدخول", Error: "تعذّر إنشاء الجلسة", Data: LoginForm{Username: username}})
			return
		}
		auth.RecordLogin(s.Store.DB, u.UserName)
		http.SetCookie(w, &http.Cookie{Name: "cashierpos_session", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Expires: time.Now().Add(12 * time.Hour)})
		if u.MustChangePassword {
			http.Redirect(w, r, "/change-password?forced=1", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	_ = s.render(w, r, "login.html", Page{Title: "تسجيل الدخول"})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("cashierpos_session"); err == nil {
		auth.DeleteSession(s.Store.DB, c.Value)
	}
	if u := CurrentUser(r); u != nil {
		auth.RecordLogout(s.Store.DB, u.UserName)
	}
	http.SetCookie(w, &http.Cookie{Name: "cashierpos_session", Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	forced := r.URL.Query().Get("forced") == "1" || u.MustChangePassword
	if r.Method == http.MethodPost {
		cur := r.FormValue("current")
		nw := r.FormValue("new")
		confirm := r.FormValue("confirm")
		if nw != confirm {
			_ = s.render(w, r, "change_password.html", Page{Title: "تغيير كلمة السر", Error: "كلمة السر الجديدة وتأكيدها غير متطابقين"})
			return
		}
		// Verify current password against the stored hash.
		var stored string
		_ = s.Store.DB.QueryRow(`SELECT Password FROM Users WHERE UserID=?`, u.ID).Scan(&stored)
		if ok, _ := auth.VerifyPassword(stored, cur); !ok {
			_ = s.render(w, r, "change_password.html", Page{Title: "تغيير كلمة السر", Error: "كلمة السر الحالية غير صحيحة"})
			return
		}
		if err := auth.ChangePassword(s.Store.DB, u.ID, nw); err != nil {
			// FIX-27 R27-E-F3: gate through safeHTMLErr —
			// ChangePassword returns the raw driver Exec error
			// (e.g. a constraint failure), which must never reach
			// the page. The short-password Arabic message passes
			// through verbatim.
			LogErr(r, err)
			_ = s.render(w, r, "change_password.html", Page{Title: "تغيير كلمة السر", Error: safeHTMLErr(err)})
			return
		}
		u.MustChangePassword = false
		http.Redirect(w, r, "/?changed=1", http.StatusSeeOther)
		return
	}
	p := Page{Title: "تغيير كلمة السر"}
	if forced {
		p.Notice = "يجب تغيير كلمة السر قبل المتابعة — كلمة السر الحالية مؤقتة ويجب استبدالها الآن."
	}
	_ = s.render(w, r, "change_password.html", p)
}

// LatestInvoice is one display-only row of the hub latest-3 card
// (H-5): newest of today's non-canceled invoices. Method is the Arabic
// display label (كاش / فيزا / فودافون كاش), like PyQt _METHOD_AR.
type LatestInvoice struct {
	Number int64
	Total  money.Cents
	Method string
}

// methodAr renders a stored payment method in Arabic.
func methodAr(m string) string {
	switch strings.TrimSpace(m) {
	case "Cash":
		return "كاش"
	case "Visa":
		return "فيزا"
	case "Vodafone Cash":
		return "فودافون كاش"
	default:
		if strings.TrimSpace(m) == "" {
			return "—"
		}
		return strings.TrimSpace(m)
	}
}

// HubData is the hub page data (Page.Data): shift status, low-stock count
// and the sales summary. Summary scope follows §15 row 44 (M4): roles with
// view_reports see the whole day; anyone else sees only their own shift.
type HubData struct {
	ShiftState    string // "open" | "closed_today" | "none"
	ShiftID       int64
	ClosedShiftID int64
	LowStockCount int
	SummaryScope  string // "day" | "shift" (shift also covers a shift-less
	// user without view_reports: shift-scope values stay 0 — no day
	// totals ever leak to them, §15 row 6 / M4)
	TodaySales    money.Cents
	TodayInvoices int64
	ShiftSales    money.Cents
	ShiftInvoices int64
	// FIX-24 R24-C-N1: the sales-summary SUM error, if the day/shift
	// SUM(NetAmount) scan failed (e.g. SQLite integer overflow on
	// absurd stored rows). Empty on the normal path. When set, the
	// hub shows this message instead of a silent 0.00 total, like
	// Dashboard surfaces the same error via renderAdminErr
	// (domain_reports.go Dashboard + sumScanErr).
	SummaryError string
	// H-3: expected drawer balance of the open shift (HasDrawer false
	// with no open shift — the card shows "—").
	DrawerBalance money.Cents
	HasDrawer     bool
	// H-3: pending web-store orders (PendingOrdersKnown false when the
	// feed cannot be read — the card shows "غير متصل", never a stale 0).
	PendingOrders      int
	PendingOrdersKnown bool
	// H-6: pending payment-evidence count, for the hub alerts strip.
	PendingEvidence int
	// H-5: newest 3 non-canceled invoices in scope (day scope: today;
	// shift scope: this shift only; shift-less without view_reports:
	// the caller's own invoices today only), display-only.
	LatestInvoices []LatestInvoice
	// H-6 startup inventory popup (Admin/Manager only — filled only
	// when the caller's effective role is admin): expired products,
	// products expiring within 7 days, vendors with a balance due.
	ExpiredCount    int
	NearExpiryCount int
	VendorDueCount  int
	// FIX-12 SW-M03: due-payables reminder (count of open/partial
	// payables due within 30 days or overdue, from DuePayables).
	// ShowDuePayablesReminder is true only on the first hub render
	// of the day for this user — the once-per-day dedupe (see
	// hubData); later hub loads the same day hide it.
	DuePayablesCount        int
	ShowDuePayablesReminder bool
}

func (s *Server) handleHub(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	u := CurrentUser(r)
	// Forced password change: a user with MustChangePassword may not use
	// the hub (or anything behind login) until the password is changed.
	// Full enforcement on domain handlers belongs to RequireLogin
	// (coordinator-owned server.go); the hub enforces it here.
	if u.MustChangePassword {
		http.Redirect(w, r, "/change-password?forced=1", http.StatusSeeOther)
		return
	}
	d := s.hubData(u)
	p := Page{Title: "الرئيسية", ActiveNav: "hub", Data: d}
	if d.SummaryError != "" {
		// FIX-24 R24-C-N1: surface the summary SUM error as the page
		// error too, exactly like Dashboard does via renderAdminErr
		// (layout.html renders .Error as the top alert banner).
		p.Error = d.SummaryError
	}
	if r.URL.Query().Get("changed") == "1" {
		p.Notice = "تم تغيير كلمة السر بنجاح."
	}
	// Unread bells are filled centrally by render() (FIX-3 L13: the
	// audit count is IsRead=0 AND IsResolved=0, from
	// UnreadAuditAlertsCount — no hub-local audit query here).
	_ = s.Store.DB.QueryRow(`SELECT COUNT(*) FROM Notifications
		WHERE IsRead=0 AND (UserName=? OR UserName='')`, u.UserName).Scan(&p.UnreadNotif)
	_ = s.render(w, r, "hub.html", p)
}

// hubSummaryErr mirrors store.sumScanErr (domain_reports.go) for the
// hub's sales-summary SUM: a SQLite integer overflow becomes the
// Arabic money.ErrOverflow message; any other scan error is returned
// verbatim, exactly like Dashboard returns it via renderAdminErr.
// A failed SUM must never surface as a silent 0.00 (R24-C-N1).
func hubSummaryErr(err error) string {
	if err == nil {
		return ""
	}
	if strings.Contains(err.Error(), "integer overflow") {
		return money.ErrOverflow.Error()
	}
	return err.Error()
}

// hubData loads the hub panels. Every query tolerates an empty database
// (fresh install: no shifts, no invoices, no products yet).
func (s *Server) hubData(u *auth.User) HubData {
	today := store.Today()
	d := HubData{ShiftState: "none", SummaryScope: "none"}

	// Shift status for this user today (L-11/L-12): open shift first, then
	// a closed shift from today. Never auto-opens (G1).
	_ = s.Store.DB.QueryRow(`SELECT ShiftID FROM Shifts
		WHERE UserID=? AND ShiftDate=? AND IsOpen=1 ORDER BY ShiftID DESC LIMIT 1`,
		u.ID, today).Scan(&d.ShiftID)
	if d.ShiftID != 0 {
		d.ShiftState = "open"
	} else {
		_ = s.Store.DB.QueryRow(`SELECT ShiftID FROM Shifts
			WHERE UserID=? AND ShiftDate=? AND IsOpen=0 ORDER BY ShiftID DESC LIMIT 1`,
			u.ID, today).Scan(&d.ClosedShiftID)
		if d.ClosedShiftID != 0 {
			d.ShiftState = "closed_today"
		}
	}

	// Low stock (L-13): active products at or below their minimum.
	_ = s.Store.DB.QueryRow(`SELECT COUNT(*) FROM Products
		WHERE IsActive=1 AND Quantity <= MinQuantity`).Scan(&d.LowStockCount)

	// H-3 drawer balance: the open shift's expected balance (reuses the
	// shifts-page query, ShiftSummary). No open shift: HasDrawer stays
	// false and the card shows "—".
	if d.ShiftID != 0 {
		if sum, err := s.Store.ShiftSummary(context.Background(), d.ShiftID); err == nil && sum != nil {
			d.HasDrawer = true
			d.DrawerBalance = sum.ExpectedBalance
		}
	}

	// H-3 pending web-store orders + H-6 pending payment evidence.
	// The counts are read from the local orders feed; a read error
	// means the feed is unreachable, so Known stays false and the card
	// shows "غير متصل" instead of a stale zero.
	if n, err := s.Store.PendingOrdersCount(context.Background(), u); err == nil {
		d.PendingOrders = n
		d.PendingOrdersKnown = true
	}
	if n, err := s.Store.PendingEvidenceCount(context.Background(), u); err == nil {
		d.PendingEvidence = n
	}

	// H-6 startup inventory popup (Admin/Manager only, like PyQt):
	// expired products, products expiring within 7 days, vendors with
	// a balance due within 30 days (VendorDueAlerts window).
	if rbac.EffectiveRole(u.Role) == rbac.RoleAdmin {
		_ = s.Store.DB.QueryRow(`SELECT COUNT(*) FROM Products
			WHERE IsActive=1 AND IsExpiryTracked=1 AND ExpiryDate IS NOT NULL AND ExpiryDate<>'' AND ExpiryDate<?`,
			today).Scan(&d.ExpiredCount)
		_ = s.Store.DB.QueryRow(`SELECT COUNT(*) FROM Products
			WHERE IsActive=1 AND IsExpiryTracked=1 AND ExpiryDate IS NOT NULL AND ExpiryDate<>'' AND ExpiryDate>=? AND ExpiryDate<=date(?,'+7 days')`,
			today, today).Scan(&d.NearExpiryCount)
		if alerts, err := s.Store.VendorDueAlerts(context.Background()); err == nil {
			d.VendorDueCount = len(alerts)
		}
	}

	// FIX-12 SW-M03: due-payables reminder on the hub, once per day
	// per user. Audience follows the DuePayables contract (owner =
	// Admin/Manager, plus the on-shift user — the cashier/supervisor
	// working a shift today); warehouse never qualifies (no shift,
	// not admin). Dedupe is a lightweight stored flag in AppSettings,
	// keyed by user + date: the first hub render of the day with due
	// payables shows the reminder and records the flag; later hub
	// renders the same day see the flag and hide it. A new day (new
	// key) shows it again; a payable that becomes due later the same
	// day does NOT — the flag is already set and is never cleared
	// within the day. The flag is render-only state — it never
	// touches Payables rows (read-only use of DuePayables;
	// domain_payables.go untouched).
	if rbac.EffectiveRole(u.Role) == rbac.RoleAdmin || d.ShiftID != 0 {
		if due, err := s.Store.DuePayables(context.Background(), u); err == nil && len(due) > 0 {
			flagKey := "payables_reminder_shown:" + u.UserName + ":" + today
			if s.Store.GetSetting(flagKey) == "" {
				d.DuePayablesCount = len(due)
				d.ShowDuePayablesReminder = true
				_ = s.Store.SetSetting(flagKey, "1")
			}
		}
	}

	// Sales summary. InvoiceDate is stored as a timestamp string, so match
	// on its date prefix.
	// FIX-24 R24-C-N1: the SUM scan error must surface, never silently
	// zero the totals (the Dashboard path maps it through sumScanErr in
	// domain_reports.go; the same mapping is applied here — see
	// hubSummaryErr below).
	if rbac.Can(u.Role, rbac.PermViewReports) {
		d.SummaryScope = "day"
		var total sql.NullInt64
		if err := s.Store.DB.QueryRow(`SELECT COUNT(*), COALESCE(SUM(NetAmount),0) FROM Invoices
			WHERE IsCanceled=0 AND substr(InvoiceDate,1,10)=?`, today).Scan(&d.TodayInvoices, &total); err != nil {
			d.SummaryError = hubSummaryErr(err)
		}
		d.TodaySales = money.Cents(total.Int64)
	} else if d.ShiftID != 0 {
		// Cashier (and anyone without view_reports) sees only their own
		// shift's totals (§15 row 6 / M4).
		d.SummaryScope = "shift"
		var total sql.NullInt64
		if err := s.Store.DB.QueryRow(`SELECT COUNT(*), COALESCE(SUM(NetAmount),0) FROM Invoices
			WHERE IsCanceled=0 AND ShiftID=?`, d.ShiftID).Scan(&d.ShiftInvoices, &total); err != nil {
			d.SummaryError = hubSummaryErr(err)
		}
		d.ShiftSales = money.Cents(total.Int64)
	} else {
		// PR-M1: shift-less and without view_reports — the shift-scope
		// cards still render (all 4 cards, like PyQt design D) but at
		// 0.00 / 0: no open shift means no shift sales yet, and day
		// totals must NOT leak to a user without view_reports.
		d.SummaryScope = "shift"
	}

	// H-5 latest-3 invoices: newest non-canceled invoices in scope
	// (day scope: today; shift scope: this shift only), display-only.
	// Reuses the invoices query above; a canceled invoice never shows.
	var invRows *sql.Rows
	var invErr error
	if d.SummaryScope == "day" {
		invRows, invErr = s.Store.DB.Query(`SELECT InvoiceNumber, NetAmount, PaymentMethod FROM Invoices
			WHERE IsCanceled=0 AND substr(InvoiceDate,1,10)=? ORDER BY InvoiceID DESC LIMIT 3`, today)
	} else if d.ShiftID != 0 {
		invRows, invErr = s.Store.DB.Query(`SELECT InvoiceNumber, NetAmount, PaymentMethod FROM Invoices
			WHERE IsCanceled=0 AND ShiftID=? ORDER BY InvoiceID DESC LIMIT 3`, d.ShiftID)
	} else {
		// PR-M1: shift-less without view_reports — the caller's OWN
		// invoices today only (UserID is set at sale creation). Day
		// totals / other cashiers' invoices must not leak (§15 row 6).
		invRows, invErr = s.Store.DB.Query(`SELECT InvoiceNumber, NetAmount, PaymentMethod FROM Invoices
			WHERE IsCanceled=0 AND UserID=? AND substr(InvoiceDate,1,10)=? ORDER BY InvoiceID DESC LIMIT 3`, u.ID, today)
	}
	if invErr == nil && invRows != nil {
		defer invRows.Close()
		for invRows.Next() {
			var inv LatestInvoice
			var total int64
			var method string
			if err := invRows.Scan(&inv.Number, &total, &method); err != nil {
				break
			}
			inv.Total = money.Cents(total)
			inv.Method = methodAr(method)
			d.LatestInvoices = append(d.LatestInvoices, inv)
		}
	}
	return d
}
