// Package web — B4: admin handlers (notifications, audit, users, settings,
// backup, reports, dashboard, shift monitor, attendance) + JSON API.
//
// All routes are registered by the single function registerAdminRoutes,
// which routes.go (B5) calls from Server.routes(). Service-layer permission
// checks run inside every store call as well.
//
// NOTE on templates: these pages are self-contained full HTML documents
// (same header/nav/footer as layout.html) instead of
// {{template "layout" .}} + {{define "content"}}. The layout pattern cannot
// work as specified: every page defines a template named "content", and
// text/template keeps only the LAST definition, so every page would render
// the same content (verified empirically). In addition the current glob in
// server.go ("templates/**/*.html") does not match top-level templates at
// all. B5 owns server.go/layout.html; once the glob and the shared
// "content" name are fixed, these pages can switch to layout inheritance.
package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"cashierpos-go/internal/money"
	"cashierpos-go/internal/rbac"
	"cashierpos-go/internal/store"
)

// registerAdminRoutes registers every B4 route on m. Called from routes.go.
func (s *Server) registerAdminRoutes(m *http.ServeMux) {
	// --- Notifications (inbox for every logged-in user; manage = admin) ---
	m.HandleFunc("GET /notifications", s.RequireLogin(s.handleNotifInbox))
	m.HandleFunc("POST /notifications/{id}/read", s.RequireLogin(s.handleNotifRead))
	m.HandleFunc("POST /notifications/read-all", s.RequireLogin(s.handleNotifReadAll))
	m.HandleFunc("GET /notifications/manage", s.RequirePerm(rbac.PermManageNotifications, s.handleNotifManage))
	m.HandleFunc("GET /notifications/new", s.RequirePerm(rbac.PermManageNotifications, s.handleNotifForm))
	m.HandleFunc("POST /notifications/new", s.RequirePerm(rbac.PermManageNotifications, s.handleNotifCreate))
	m.HandleFunc("GET /notifications/{id}/edit", s.RequirePerm(rbac.PermManageNotifications, s.handleNotifForm))
	m.HandleFunc("POST /notifications/{id}/edit", s.RequirePerm(rbac.PermManageNotifications, s.handleNotifUpdate))
	m.HandleFunc("POST /notifications/{id}/toggle", s.RequirePerm(rbac.PermManageNotifications, s.handleNotifToggle))
	m.HandleFunc("GET /notifications/{id}/preview", s.RequirePerm(rbac.PermManageNotifications, s.handleNotifPreview))
	m.HandleFunc("GET /notifications/{id}/log", s.RequirePerm(rbac.PermManageNotifications, s.handleNotifLog))
	m.HandleFunc("GET /notifications/settings", s.RequireLogin(s.handleNotifSettings))
	m.HandleFunc("POST /notifications/settings", s.RequireLogin(s.handleNotifSettingsSave))

	// --- Audit ---
	m.HandleFunc("GET /audit", s.RequirePerm(rbac.PermViewAudit, s.handleAuditList))
	m.HandleFunc("GET /audit/{id}", s.RequirePerm(rbac.PermViewAudit, s.handleAuditDetail))
	m.HandleFunc("POST /audit/alerts/{id}/read", s.RequirePerm(rbac.PermViewAudit, s.handleAuditAlertRead))
	m.HandleFunc("POST /audit/alerts/{id}/resolve", s.RequirePerm(rbac.PermViewAudit, s.handleAuditAlertResolve))
	m.HandleFunc("GET /audit/smart", s.RequirePerm(rbac.PermViewAudit, s.handleAuditSmart))
	m.HandleFunc("POST /audit/monitor/run", s.RequirePerm(rbac.PermManageMonitor, s.handleAuditMonitorRun))

	// --- Users ---
	m.HandleFunc("GET /users", s.RequirePerm(rbac.PermManageUsers, s.handleUsersList))
	m.HandleFunc("GET /users/new", s.RequirePerm(rbac.PermManageUsers, s.handleUserForm))
	m.HandleFunc("POST /users/new", s.RequirePerm(rbac.PermManageUsers, s.handleUserCreate))
	m.HandleFunc("GET /users/{id}/edit", s.RequirePerm(rbac.PermManageUsers, s.handleUserForm))
	m.HandleFunc("POST /users/{id}/edit", s.RequirePerm(rbac.PermManageUsers, s.handleUserUpdate))
	m.HandleFunc("POST /users/{id}/delete", s.RequirePerm(rbac.PermManageUsers, s.handleUserDelete))
	m.HandleFunc("GET /users/{id}/permissions", s.RequirePerm(rbac.PermManageUsers, s.handleUserPermissions))

	// --- Settings ---
	// FIX-1 incidental: the GET /settings route was missing although
	// handleSettings + settings_page.html exist and the nav links to
	// it — the page answered 404 for every role.
	m.HandleFunc("GET /settings", s.RequirePerm(rbac.PermAccessSettings, s.handleSettings))
	m.HandleFunc("GET /zones", s.RequirePerm(rbac.PermViewZones, s.handleZonesPage))
	m.HandleFunc("POST /settings/profile", s.RequirePerm(rbac.PermAccessSettings, s.handleSettingsProfile))
	m.HandleFunc("POST /settings/general", s.RequirePerm(rbac.PermAccessSettings, s.handleSettingsGeneral))
	m.HandleFunc("POST /settings/zones", s.RequirePerm(rbac.PermManageZones, s.handleSettingsZones))
	m.HandleFunc("POST /settings/scale", s.RequirePerm(rbac.PermScaleSettings, s.handleSettingsScale))
	m.HandleFunc("POST /settings/scale/test", s.RequirePerm(rbac.PermScaleSettings, s.handleSettingsScaleTest))
	m.HandleFunc("POST /settings/cloud", s.RequirePerm(rbac.PermBackup, s.handleSettingsCloud))
	m.HandleFunc("POST /settings/monitor", s.RequirePerm(rbac.PermManageMonitor, s.handleSettingsMonitor))
	m.HandleFunc("POST /settings/reset", s.RequirePerm(rbac.PermResetDB, s.handleSettingsReset))

	// --- Backup ---
	m.HandleFunc("GET /backup", s.RequirePerm(rbac.PermBackup, s.handleBackupList))
	m.HandleFunc("POST /backup/now", s.RequirePerm(rbac.PermBackup, s.handleBackupNow))
	m.HandleFunc("POST /backup/restore", s.RequirePerm(rbac.PermRestore, s.handleBackupRestore))

	// --- Reports / dashboard / shift monitor ---
	m.HandleFunc("GET /reports", s.RequirePerm(rbac.PermViewReports, s.handleReportsIndex))
	m.HandleFunc("GET /reports/sales", s.RequirePerm(rbac.PermViewReports, s.handleReportSales))
	m.HandleFunc("GET /reports/profit", s.RequirePerm(rbac.PermViewProfitAnalysis, s.handleReportProfit))
	m.HandleFunc("GET /reports/debts", s.RequirePerm(rbac.PermViewReports, s.handleReportDebts))
	m.HandleFunc("GET /reports/top-products", s.RequirePerm(rbac.PermViewReports, s.handleReportTopProducts))
	m.HandleFunc("GET /reports/low-stock", s.RequirePerm(rbac.PermViewReports, s.handleReportLowStock))
	m.HandleFunc("GET /reports/canceled", s.RequirePerm(rbac.PermViewReports, s.handleReportCanceled))
	m.HandleFunc("GET /reports/payments", s.RequirePerm(rbac.PermViewPaymentReport, s.handleReportPayments))
	m.HandleFunc("GET /reports/customer", s.RequirePerm(rbac.PermViewCustomerReport, s.handleReportCustomer))
	m.HandleFunc("GET /reports/profit-analysis", s.RequirePerm(rbac.PermViewProfitAnalysis, s.handleReportProfitAnalysis))
	m.HandleFunc("GET /reports/returns", s.RequirePerm(rbac.PermViewReturnAnalysis, s.handleReportReturns))
	m.HandleFunc("GET /reports/export", s.RequirePerm(rbac.PermExportCSV, s.handleReportExport))
	m.HandleFunc("GET /dashboard", s.RequirePerm(rbac.PermAccessDashboard, s.handleDashboard))
	m.HandleFunc("GET /shifts/monitor", s.RequirePerm(rbac.PermShiftMonitor, s.handleShiftMonitor))

	// --- Attendance ---
	m.HandleFunc("GET /attendance", s.RequirePerm(rbac.PermViewAttendance, s.handleAttendance))

	// --- JSON API ---
	m.HandleFunc("GET /api/notifications/unread-count", s.RequireLogin(s.apiNotifUnreadCount))
	m.HandleFunc("GET /api/notifications", s.RequireLogin(s.apiNotifList))
	m.HandleFunc("POST /api/notifications/{id}/read", s.RequireLogin(s.apiNotifRead))
	m.HandleFunc("POST /api/backup/now", s.RequirePerm(rbac.PermBackup, s.apiBackupNow))
	m.HandleFunc("GET /api/reports/sales", s.RequirePerm(rbac.PermViewReports, s.apiReportSales))
	m.HandleFunc("GET /api/audit/unread-count", s.RequirePerm(rbac.PermViewAudit, s.apiAuditUnreadCount))
	m.HandleFunc("GET /api/backup/list", s.RequirePerm(rbac.PermBackup, s.apiBackupList))
}

// --- shared helpers ---

// dateRange reads from/to query params; defaults to this month so far.
func dateRange(r *http.Request) (string, string) {
	today := store.Today()
	from := strings.TrimSpace(r.URL.Query().Get("from"))
	to := strings.TrimSpace(r.URL.Query().Get("to"))
	if from == "" {
		from = today[:8] + "01"
	}
	if to == "" {
		to = today
	}
	return from, to
}

func (s *Server) renderAdminErr(w http.ResponseWriter, r *http.Request, tmpl string, p Page, err error) {
	p.Error = htmlStoreErrMsg(err)
	// R23-C-F2: most admin templates dereference `.Data` unguarded
	// (e.g. reports_dashboard.html:4 `.Data.TodaySales`), so rendering
	// one with a nil Data turns the Arabic error into a generic 500
	// `خطأ في عرض الصفحة`. Supply a zero-value Data of the template's
	// expected type when the caller has none, and fall back to the
	// generic error page — which needs no Data and always shows
	// p.Error — if the template still fails to execute.
	if p.Data == nil {
		p.Data = adminErrZeroData(tmpl)
	}
	if buf, rerr := s.renderToBuffer(r, tmpl, p); rerr == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(buf)
		return
	} else {
		LogErr(r, rerr)
	}
	if buf, rerr := s.renderToBuffer(r, "error.html", Page{Title: p.Title, Error: p.Error}); rerr == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(buf)
		return
	} else {
		LogErr(r, rerr)
	}
	http.Error(w, "خطأ في عرض الصفحة", http.StatusInternalServerError)
}

// adminErrZeroData returns a zero-value Data of the type the named
// admin template expects, so renderAdminErr can render the error page
// with p.Error instead of failing on a nil-Data dereference (R23-C-F2).
// Templates whose body is guarded by `{{if .Data}}` (notifications_manage,
// users_list) need no Data, but a typed empty value is harmless there.
// Unknown templates return nil: the error.html fallback still shows
// the message.
func adminErrZeroData(tmpl string) any {
	switch tmpl {
	case "notifications_inbox.html":
		return notifInboxData{}
	case "notifications_manage.html":
		return []store.Notification{}
	case "notifications_form.html":
		return notifFormData{Types: store.ValidNotifTypes}
	case "notifications_preview.html":
		return struct {
			Notif store.Notification
			Sent  int
		}{}
	case "notifications_log.html":
		return struct {
			Notif store.Notification
			Logs  []store.SendLogEntry
		}{}
	case "notifications_settings.html":
		return []store.NotificationSetting{}
	case "audit_list.html":
		return auditListData{}
	case "audit_detail.html":
		return struct {
			Entry   store.AuditEntry
			Details []store.AuditDetail
		}{}
	case "audit_smart.html":
		return struct {
			Kind string
			From string
			To   string
			Rows []store.SmartReportRow
		}{}
	case "users_list.html":
		return []store.UserRecord{}
	case "users_form.html":
		return userFormData{Roles: []string{"admin", "manager", "cashier", "supervisor", "warehouse"}}
	case "users_permissions.html":
		return struct {
			User    store.UserRecord
			Summary []store.PermissionSummary
		}{}
	case "settings_page.html":
		return settingsView{}
	case "backup_list.html":
		return struct {
			Backups []store.BackupInfo
			Cloud   store.CloudBackupSettings
		}{}
	case "reports_page.html":
		return reportView{}
	case "reports_dashboard.html":
		return store.AdminDashboard{}
	case "shifts_monitor.html":
		return store.ShiftMonitor{}
	case "attendance_list.html":
		return store.AttendanceSummary{}
	default:
		return nil
	}
}

// --- Notifications ---

type notifInboxData struct {
	Items  []store.Notification
	Unread int
}

func (s *Server) handleNotifInbox(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	items, err := s.Store.ListInbox(r.Context(), u, 50)
	if err != nil {
		s.renderAdminErr(w, r, "notifications_inbox.html", Page{Title: "التنبيهات"}, err)
		return
	}
	unread, _ := s.Store.UnreadNotificationCount(r.Context(), u)
	_ = s.render(w, r, "notifications_inbox.html", Page{Title: "التنبيهات", ActiveNav: "notifications", UnreadNotif: unread, Data: notifInboxData{Items: items, Unread: unread}})
}

func (s *Server) handleNotifRead(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		http.Error(w, "معرّف غير صحيح", http.StatusBadRequest)
		return
	}
	// FIX-12 SW-L15: a failed mark-read (e.g. another user's row) must not
	// pretend success — re-render the inbox with the error instead of a
	// silent 303.
	if err := s.Store.MarkNotificationRead(r.Context(), CurrentUser(r), id); err != nil {
		u := CurrentUser(r)
		items, _ := s.Store.ListInbox(r.Context(), u, 50)
		unread, _ := s.Store.UnreadNotificationCount(r.Context(), u)
		s.renderAdminErr(w, r, "notifications_inbox.html", Page{Title: "التنبيهات", ActiveNav: "notifications", UnreadNotif: unread, Data: notifInboxData{Items: items, Unread: unread}}, err)
		return
	}
	http.Redirect(w, r, "/notifications", http.StatusSeeOther)
}

func (s *Server) handleNotifReadAll(w http.ResponseWriter, r *http.Request) {
	_ = s.Store.MarkAllNotificationsRead(r.Context(), CurrentUser(r))
	http.Redirect(w, r, "/notifications", http.StatusSeeOther)
}

func (s *Server) handleNotifManage(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	list, err := s.Store.ListNotificationsAdmin(r.Context(), u)
	if err != nil {
		s.renderAdminErr(w, r, "notifications_manage.html", Page{Title: "إدارة التنبيهات"}, err)
		return
	}
	_ = s.render(w, r, "notifications_manage.html", Page{Title: "إدارة التنبيهات", Data: list})
}

type notifFormData struct {
	Notif store.Notification
	IsNew bool
	Types []string
}

func (s *Server) handleNotifForm(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	data := notifFormData{IsNew: true, Types: store.ValidNotifTypes, Notif: store.Notification{Audience: "all", Type: store.NotifCustom}}
	if idStr := r.PathValue("id"); idStr != "" {
		id, err := pathID(r, "id")
		if err != nil {
			s.renderAdminErr(w, r, "notifications_form.html", Page{Title: "تعديل تنبيه"}, err)
			return
		}
		n, err := s.Store.GetNotification(r.Context(), u, id)
		if err != nil {
			s.renderAdminErr(w, r, "notifications_form.html", Page{Title: "تعديل تنبيه"}, err)
			return
		}
		data.IsNew = false
		data.Notif = n
	}
	_ = s.render(w, r, "notifications_form.html", Page{Title: "تنبيه", Data: data})
}

func notifInputFromForm(r *http.Request) store.NotificationInput {
	var pid int64
	if v := strings.TrimSpace(r.FormValue("linked_product_id")); v != "" {
		pid, _ = strconv.ParseInt(v, 10, 64)
	}
	return store.NotificationInput{
		Type:            strings.TrimSpace(r.FormValue("type")),
		Title:           strings.TrimSpace(r.FormValue("title")),
		Body:            strings.TrimSpace(r.FormValue("body")),
		LinkedProductID: pid,
		Audience:        strings.TrimSpace(r.FormValue("audience")),
		SendAt:          strings.TrimSpace(r.FormValue("send_at")),
		EventKey:        strings.TrimSpace(r.FormValue("event_key")),
	}
}

func (s *Server) handleNotifCreate(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	if _, err := s.Store.CreateNotification(r.Context(), u, notifInputFromForm(r)); err != nil {
		s.renderAdminErr(w, r, "notifications_form.html", Page{Title: "تنبيه جديد", Data: notifFormData{IsNew: true, Types: store.ValidNotifTypes}}, err)
		return
	}
	http.Redirect(w, r, "/notifications/manage", http.StatusSeeOther)
}

func (s *Server) handleNotifUpdate(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		s.renderAdminErr(w, r, "notifications_form.html", Page{Title: "تعديل تنبيه"}, err)
		return
	}
	if err := s.Store.UpdateNotification(r.Context(), u, id, notifInputFromForm(r)); err != nil {
		s.renderAdminErr(w, r, "notifications_form.html", Page{Title: "تعديل تنبيه", Data: notifFormData{IsNew: false, Types: store.ValidNotifTypes}}, err)
		return
	}
	http.Redirect(w, r, "/notifications/manage", http.StatusSeeOther)
}

func (s *Server) handleNotifToggle(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err == nil {
		_ = s.Store.SetNotificationStatus(r.Context(), u, id, r.FormValue("enabled") == "1")
	}
	http.Redirect(w, r, "/notifications/manage", http.StatusSeeOther)
}

func (s *Server) handleNotifPreview(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		s.renderAdminErr(w, r, "notifications_preview.html", Page{Title: "معاينة تنبيه"}, err)
		return
	}
	n, err := s.Store.GetNotification(r.Context(), u, id)
	if err != nil {
		s.renderAdminErr(w, r, "notifications_preview.html", Page{Title: "معاينة تنبيه"}, err)
		return
	}
	logs, _ := s.Store.ListSendLog(r.Context(), u, id, 10)
	_ = s.render(w, r, "notifications_preview.html", Page{Title: "معاينة تنبيه", Data: struct {
		Notif store.Notification
		Sent  int
	}{Notif: n, Sent: len(logs)}})
}

func (s *Server) handleNotifLog(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		s.renderAdminErr(w, r, "notifications_log.html", Page{Title: "سجل الإرسال"}, err)
		return
	}
	logs, err := s.Store.ListSendLog(r.Context(), u, id, 200)
	if err != nil {
		s.renderAdminErr(w, r, "notifications_log.html", Page{Title: "سجل الإرسال"}, err)
		return
	}
	n, _ := s.Store.GetNotification(r.Context(), u, id)
	_ = s.render(w, r, "notifications_log.html", Page{Title: "سجل الإرسال", Data: struct {
		Notif store.Notification
		Logs  []store.SendLogEntry
	}{Notif: n, Logs: logs}})
}

func (s *Server) handleNotifSettings(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	settings, err := s.Store.GetUserNotificationSettings(r.Context(), u)
	if err != nil {
		s.renderAdminErr(w, r, "notifications_settings.html", Page{Title: "إعدادات التنبيهات"}, err)
		return
	}
	_ = s.render(w, r, "notifications_settings.html", Page{Title: "إعدادات التنبيهات", Data: settings})
}

func (s *Server) handleNotifSettingsSave(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	for _, t := range store.ValidNotifTypes {
		enabled := r.FormValue("type_"+t) == "1"
		if err := s.Store.SetUserNotificationSetting(r.Context(), u, u.UserName, t, enabled); err != nil {
			s.renderAdminErr(w, r, "notifications_settings.html", Page{Title: "إعدادات التنبيهات"}, err)
			return
		}
	}
	settings, _ := s.Store.GetUserNotificationSettings(r.Context(), u)
	_ = s.render(w, r, "notifications_settings.html", Page{Title: "إعدادات التنبيهات", Notice: "تم حفظ إعدادات التنبيهات", Data: settings})
}

// --- Audit ---

type auditListData struct {
	Entries []store.AuditEntry
	Alerts  []store.AuditAlert
	Unread  int
	Filter  store.AuditFilter
}

func (s *Server) handleAuditList(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	from, to := dateRange(r)
	var invNum int64
	if v := r.URL.Query().Get("invoice"); v != "" {
		invNum, _ = strconv.ParseInt(v, 10, 64)
	}
	f := store.AuditFilter{
		From: from, To: to,
		UserName:       strings.TrimSpace(r.URL.Query().Get("user")),
		InvoiceNumber:  invNum,
		ActionType:     strings.TrimSpace(r.URL.Query().Get("action")),
		SuspiciousOnly: r.URL.Query().Get("suspicious") == "1",
		Limit:          200,
	}
	entries, err := s.Store.ListAuditLog(r.Context(), u, f)
	if err != nil {
		s.renderAdminErr(w, r, "audit_list.html", Page{Title: "سجل التدقيق"}, err)
		return
	}
	alerts, _ := s.Store.ListAuditAlerts(r.Context(), u, false, 50)
	unread, _ := s.Store.UnreadAuditAlertsCount(r.Context(), u)
	_ = s.render(w, r, "audit_list.html", Page{Title: "سجل التدقيق", UnreadAudit: unread, Data: auditListData{Entries: entries, Alerts: alerts, Unread: unread, Filter: f}})
}

func (s *Server) handleAuditDetail(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		s.renderAdminErr(w, r, "audit_detail.html", Page{Title: "تفاصيل سجل"}, err)
		return
	}
	entry, details, err := s.Store.GetAuditLogDetails(r.Context(), u, id)
	if err != nil {
		s.renderAdminErr(w, r, "audit_detail.html", Page{Title: "تفاصيل سجل"}, err)
		return
	}
	_ = s.render(w, r, "audit_detail.html", Page{Title: "تفاصيل سجل", Data: struct {
		Entry   store.AuditEntry
		Details []store.AuditDetail
	}{Entry: entry, Details: details}})
}

func (s *Server) handleAuditAlertRead(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	if id, err := pathID(r, "id"); err == nil {
		_ = s.Store.MarkAuditAlertRead(r.Context(), u, id)
	}
	http.Redirect(w, r, "/audit", http.StatusSeeOther)
}

func (s *Server) handleAuditAlertResolve(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	if id, err := pathID(r, "id"); err == nil {
		_ = s.Store.ResolveAuditAlert(r.Context(), u, id, strings.TrimSpace(r.FormValue("note")))
	}
	http.Redirect(w, r, "/audit", http.StatusSeeOther)
}

func (s *Server) handleAuditSmart(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	from, to := dateRange(r)
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind == "" {
		kind = "cancels"
	}
	rows, err := s.Store.SmartReport(r.Context(), u, kind, from, to)
	if err != nil {
		s.renderAdminErr(w, r, "audit_smart.html", Page{Title: "تقارير التدقيق الذكية"}, err)
		return
	}
	_ = s.render(w, r, "audit_smart.html", Page{Title: "تقارير التدقيق الذكية", Data: struct {
		Kind string
		From string
		To   string
		Rows []store.SmartReportRow
	}{Kind: kind, From: from, To: to, Rows: rows}})
}

func (s *Server) handleAuditMonitorRun(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	n, err := s.Store.RunSmartMonitor(r.Context(), u)
	if err != nil {
		s.renderAdminErr(w, r, "audit_list.html", Page{Title: "سجل التدقيق"}, err)
		return
	}
	entries, _ := s.Store.ListAuditLog(r.Context(), u, store.AuditFilter{Limit: 50})
	alerts, _ := s.Store.ListAuditAlerts(r.Context(), u, false, 50)
	unread, _ := s.Store.UnreadAuditAlertsCount(r.Context(), u)
	from, to := dateRange(r)
	_ = s.render(w, r, "audit_list.html", Page{Title: "سجل التدقيق", Notice: "تم تشغيل المراقبة الذكية — تنبيهات جديدة: " + strconv.Itoa(n), UnreadAudit: unread,
		Data: auditListData{Entries: entries, Alerts: alerts, Unread: unread, Filter: store.AuditFilter{From: from, To: to, Limit: 200}}})
}

// --- Users ---

func (s *Server) handleUsersList(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	users, err := s.Store.ListUsers(r.Context(), u)
	if err != nil {
		s.renderAdminErr(w, r, "users_list.html", Page{Title: "المستخدمون"}, err)
		return
	}
	_ = s.render(w, r, "users_list.html", Page{Title: "المستخدمون", Data: users})
}

type userFormData struct {
	User  store.UserRecord
	IsNew bool
	Roles []string
}

func (s *Server) handleUserForm(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	data := userFormData{IsNew: true, Roles: []string{"admin", "manager", "cashier", "supervisor", "warehouse"}, User: store.UserRecord{Role: "cashier", IsActive: true}}
	if idStr := r.PathValue("id"); idStr != "" {
		id, err := pathID(r, "id")
		if err != nil {
			s.renderAdminErr(w, r, "users_form.html", Page{Title: "مستخدم"}, err)
			return
		}
		rec, err := s.Store.GetUser(r.Context(), u, id)
		if err != nil {
			s.renderAdminErr(w, r, "users_form.html", Page{Title: "مستخدم"}, err)
			return
		}
		data.IsNew = false
		data.User = rec
	}
	_ = s.render(w, r, "users_form.html", Page{Title: "مستخدم", Data: data})
}

func userInputFromForm(r *http.Request, isNew bool) store.UserInput {
	in := store.UserInput{
		UserName: strings.TrimSpace(r.FormValue("username")),
		Password: r.FormValue("password"),
		FullName: strings.TrimSpace(r.FormValue("full_name")),
		Role:     strings.TrimSpace(r.FormValue("role")),
		IsActive: r.FormValue("is_active") == "1",
	}
	_ = isNew
	return in
}

func (s *Server) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	if _, err := s.Store.CreateUser(r.Context(), u, userInputFromForm(r, true)); err != nil {
		s.renderAdminErr(w, r, "users_form.html", Page{Title: "مستخدم جديد", Data: userFormData{IsNew: true, Roles: []string{"admin", "manager", "cashier", "supervisor", "warehouse"}}}, err)
		return
	}
	http.Redirect(w, r, "/users", http.StatusSeeOther)
}

func (s *Server) handleUserUpdate(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		s.renderAdminErr(w, r, "users_form.html", Page{Title: "تعديل مستخدم"}, err)
		return
	}
	if err := s.Store.UpdateUser(r.Context(), u, id, userInputFromForm(r, false)); err != nil {
		rec, _ := s.Store.GetUser(r.Context(), u, id)
		s.renderAdminErr(w, r, "users_form.html", Page{Title: "تعديل مستخدم", Data: userFormData{IsNew: false, User: rec, Roles: []string{"admin", "manager", "cashier", "supervisor", "warehouse"}}}, err)
		return
	}
	http.Redirect(w, r, "/users", http.StatusSeeOther)
}

func (s *Server) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	if id, err := pathID(r, "id"); err == nil {
		if err := s.Store.DeleteUser(r.Context(), u, id); err != nil {
			users, _ := s.Store.ListUsers(r.Context(), u)
			s.renderAdminErr(w, r, "users_list.html", Page{Title: "المستخدمون", Data: users}, err)
			return
		}
	}
	http.Redirect(w, r, "/users", http.StatusSeeOther)
}

func (s *Server) handleUserPermissions(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		s.renderAdminErr(w, r, "users_permissions.html", Page{Title: "صلاحيات الدور"}, err)
		return
	}
	rec, err := s.Store.GetUser(r.Context(), u, id)
	if err != nil {
		s.renderAdminErr(w, r, "users_permissions.html", Page{Title: "صلاحيات الدور"}, err)
		return
	}
	_ = s.render(w, r, "users_permissions.html", Page{Title: "صلاحيات الدور", Data: struct {
		User    store.UserRecord
		Summary []store.PermissionSummary
	}{User: rec, Summary: store.RolePermissionsSummary(rec.Role)}})
}

// --- Settings ---

type settingsView struct {
	Profile      store.StoreProfile
	AutoLock     int
	DarkMode     bool
	Zones        []store.DeliveryZone
	Scale        store.ScaleSettings
	Cloud        store.CloudBackupSettings
	Thresholds   store.MonitorThresholds
	ScaleTest    *store.ScaleDecode
	ScaleTestErr string
}

func (s *Server) settingsData(r *http.Request) (settingsView, error) {
	u := CurrentUser(r)
	v := settingsView{
		Profile:  s.Store.GetStoreProfile(),
		AutoLock: s.Store.GetAutoLockMinutes(),
		DarkMode: s.Store.GetDarkMode(),
		Zones:    s.Store.GetDeliveryZones(),
		Cloud:    s.Store.GetCloudBackupSettings(),
	}
	scale, err := s.Store.GetScaleSettings(r.Context())
	if err != nil {
		return v, err
	}
	v.Scale = scale
	th, err := s.Store.GetMonitorThresholds(r.Context(), u)
	if err != nil {
		// Non-admins never reach here (route gate), but keep the page usable.
		th = store.MonitorThresholds{}
	}
	v.Thresholds = th
	return v, nil
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	v, err := s.settingsData(r)
	if err != nil {
		s.renderAdminErr(w, r, "settings_page.html", Page{Title: "الإعدادات"}, err)
		return
	}
	_ = s.render(w, r, "settings_page.html", Page{Title: "الإعدادات", Data: v})
}

func (s *Server) settingsReload(w http.ResponseWriter, r *http.Request, notice string, err error) {
	v, _ := s.settingsData(r)
	p := Page{Title: "الإعدادات", Data: v, Notice: notice}
	if err != nil {
		p.Error = htmlStoreErrMsg(err)
		p.Notice = ""
	}
	_ = s.render(w, r, "settings_page.html", p)
}

func (s *Server) handleSettingsProfile(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	err := s.Store.SaveStoreProfile(r.Context(), u, store.StoreProfile{
		Name:    strings.TrimSpace(r.FormValue("store_name")),
		Phone:   strings.TrimSpace(r.FormValue("store_phone")),
		Address: strings.TrimSpace(r.FormValue("store_address")),
	})
	s.settingsReload(w, r, "تم حفظ ملف المحل", err)
}

func (s *Server) handleSettingsGeneral(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	minutes, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("auto_lock_minutes")))
	if err := s.Store.SaveAutoLockMinutes(r.Context(), u, minutes); err != nil {
		s.settingsReload(w, r, "", err)
		return
	}
	if err := s.Store.SetDarkMode(r.Context(), u, r.FormValue("dark_mode") == "1"); err != nil {
		s.settingsReload(w, r, "", err)
		return
	}
	s.settingsReload(w, r, "تم حفظ الإعدادات العامة (الوضع الداكن يُطبَّق فوراً)", nil)
}

// handleZonesPage renders the read-only delivery-zones list (FIX-1 M7):
// active zones for every role that holds view_zones (admin, supervisor,
// cashier). Zone editing stays in Settings (manage_zones).
func (s *Server) handleZonesPage(w http.ResponseWriter, r *http.Request) {
	zones := s.Store.ActiveDeliveryZones()
	_ = s.render(w, r, "zones.html", Page{Title: "مناطق التوصيل", ActiveNav: "zones",
		Data: map[string]any{"Zones": zones}})
}

func (s *Server) handleSettingsZones(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	// FIX-12 SW-H12: r.Form is nil until ParseForm runs — without it every
	// zone the admin entered was silently discarded.
	if err := r.ParseForm(); err != nil {
		s.settingsReload(w, r, "", err)
		return
	}
	names := r.Form["zone_name"]
	fees := r.Form["zone_fee"]
	checked := map[string]bool{}
	for _, idx := range r.Form["zone_active"] {
		checked[idx] = true
	}
	var zones []store.DeliveryZone
	for i, name := range names {
		var fee money.Cents
		if i < len(fees) {
			var err error
			fee, err = money.ParseMoney(fees[i])
			if err != nil {
				// Surface the parse error (e.g. a negative fee) instead
				// of coercing it to 0 — the service-layer negative
				// check must stay reachable.
				s.settingsReload(w, r, "", err)
				return
			}
		}
		zones = append(zones, store.DeliveryZone{Name: strings.TrimSpace(name), Fee: fee, Active: checked[strconv.Itoa(i)]})
	}
	err := s.Store.SaveDeliveryZones(r.Context(), u, zones)
	s.settingsReload(w, r, "تم حفظ مناطق التوصيل", err)
}

func (s *Server) handleSettingsScale(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	codeLen, _ := strconv.Atoi(r.FormValue("product_code_length"))
	valLen, _ := strconv.Atoi(r.FormValue("value_length"))
	decimals, _ := strconv.Atoi(r.FormValue("value_decimal_places"))
	cfg := store.ScaleConfig{
		IsEnabled:          r.FormValue("is_enabled") == "1",
		BarcodePrefix:      strings.TrimSpace(r.FormValue("barcode_prefix")),
		ProductCodeLength:  codeLen,
		ValueLength:        valLen,
		BarcodeMode:        strings.TrimSpace(r.FormValue("barcode_mode")),
		HasCheckDigit:      r.FormValue("has_check_digit") == "1",
		WeightUnit:         strings.TrimSpace(r.FormValue("weight_unit")),
		ValueDecimalPlaces: decimals,
		Notes:              strings.TrimSpace(r.FormValue("notes")),
	}
	err := s.Store.SaveScaleConfig(r.Context(), u, cfg)
	s.settingsReload(w, r, "تم حفظ إعدادات الميزان", err)
}

func (s *Server) handleSettingsScaleTest(w http.ResponseWriter, r *http.Request) {
	barcode := strings.TrimSpace(r.FormValue("barcode"))
	dec, err := s.Store.DecodeScaleBarcode(r.Context(), barcode)
	v, _ := s.settingsData(r)
	v.ScaleTest = dec
	if err != nil {
		v.ScaleTestErr = htmlStoreErrMsg(err)
	}
	_ = s.render(w, r, "settings_page.html", Page{Title: "الإعدادات", Data: v})
}

func (s *Server) handleSettingsCloud(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	keep, _ := strconv.Atoi(r.FormValue("cloud_backup_keep"))
	err := s.Store.SaveCloudBackupSettings(r.Context(), u, store.CloudBackupSettings{
		Enabled: r.FormValue("cloud_backup_enabled") == "1",
		Dir:     strings.TrimSpace(r.FormValue("cloud_backup_dir")),
		Time:    strings.TrimSpace(r.FormValue("cloud_backup_time")),
		Keep:    keep,
	})
	s.settingsReload(w, r, "تم حفظ إعدادات النسخ السحابي", err)
}

func (s *Server) handleSettingsMonitor(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	// FIX-12 SW-M07: surface parse errors instead of discarding them — a
	// negative/invalid value used to coerce silently to 0 (disabled) with
	// a false success notice, zeroing a previously saved threshold.
	cancelAmt, err := money.ParseMoney(r.FormValue("alert_threshold_cancel_amount"))
	if err != nil {
		s.settingsReload(w, r, "", err)
		return
	}
	returnAmt, err := money.ParseMoney(r.FormValue("alert_threshold_return_amount"))
	if err != nil {
		s.settingsReload(w, r, "", err)
		return
	}
	discPct, err := strconv.Atoi(strings.TrimSpace(r.FormValue("alert_threshold_discount_pct")))
	if err != nil {
		s.settingsReload(w, r, "", errors.New("نسبة الخصم غير صحيحة"))
		return
	}
	err = s.Store.SaveMonitorThresholds(r.Context(), u, store.MonitorThresholds{
		CancelAmount: cancelAmt, DiscountPct: discPct, ReturnAmount: returnAmt,
	})
	s.settingsReload(w, r, "تم حفظ عتبات المراقبة الذكية", err)
}

func (s *Server) handleSettingsReset(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	if err := s.Store.ResetDatabase(r.Context(), u, strings.TrimSpace(r.FormValue("confirm"))); err != nil {
		s.settingsReload(w, r, "", err)
		return
	}
	// Sessions were wiped with the database; the admin must log in again.
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// --- Backup ---

func (s *Server) handleBackupList(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	list, err := s.Store.ListBackups(r.Context(), u)
	if err != nil {
		s.renderAdminErr(w, r, "backup_list.html", Page{Title: "النسخ الاحتياطي"}, err)
		return
	}
	_ = s.render(w, r, "backup_list.html", Page{Title: "النسخ الاحتياطي", Data: struct {
		Backups []store.BackupInfo
		Cloud   store.CloudBackupSettings
	}{Backups: list, Cloud: s.Store.GetCloudBackupSettings()}})
}

func (s *Server) handleBackupNow(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	res, err := s.Store.CreateBackup(r.Context(), u)
	if err != nil {
		s.renderAdminErr(w, r, "backup_list.html", Page{Title: "النسخ الاحتياطي"}, err)
		return
	}
	list, _ := s.Store.ListBackups(r.Context(), u)
	notice := "تم إنشاء النسخة " + res.FileName + " — السحابة: " + res.CloudStatus
	if res.Error != "" {
		notice += " (" + res.Error + ")"
	}
	_ = s.render(w, r, "backup_list.html", Page{Title: "النسخ الاحتياطي", Notice: notice, Data: struct {
		Backups []store.BackupInfo
		Cloud   store.CloudBackupSettings
	}{Backups: list, Cloud: s.Store.GetCloudBackupSettings()}})
}

// restoreConfirmation is the exact value the restore form must submit in
// its "confirm" field (FIX-12 SW-L14): the JS confirm() alone is not a
// server-side confirmation, so a bare crafted POST must be refused.
const restoreConfirmation = "استعادة"

func (s *Server) handleBackupRestore(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	if strings.TrimSpace(r.FormValue("confirm")) != restoreConfirmation {
		list, _ := s.Store.ListBackups(r.Context(), u)
		s.renderAdminErr(w, r, "backup_list.html", Page{Title: "النسخ الاحتياطي", Data: struct {
			Backups []store.BackupInfo
			Cloud   store.CloudBackupSettings
		}{Backups: list, Cloud: s.Store.GetCloudBackupSettings()}},
			errors.New(`للتأكيد اكتب "استعادة" في حقل التأكيد`))
		return
	}
	location := strings.TrimSpace(r.FormValue("location"))
	name := strings.TrimSpace(r.FormValue("name"))
	if err := s.Store.RestoreBackup(r.Context(), u, location, name); err != nil {
		list, _ := s.Store.ListBackups(r.Context(), u)
		s.renderAdminErr(w, r, "backup_list.html", Page{Title: "النسخ الاحتياطي", Data: struct {
			Backups []store.BackupInfo
			Cloud   store.CloudBackupSettings
		}{Backups: list, Cloud: s.Store.GetCloudBackupSettings()}}, err)
		return
	}
	list, _ := s.Store.ListBackups(r.Context(), u)
	_ = s.render(w, r, "backup_list.html", Page{Title: "النسخ الاحتياطي", Notice: "تمت الاستعادة من " + name, Data: struct {
		Backups []store.BackupInfo
		Cloud   store.CloudBackupSettings
	}{Backups: list, Cloud: s.Store.GetCloudBackupSettings()}})
}

// --- Reports ---

type reportView struct {
	Kind      string
	From      string
	To        string
	Sales     *store.SalesReport
	Profit    *store.ProfitReport
	Debts     []store.DebtRow
	DebtTotal money.Cents
	Top       []*store.DailyTopProductRow
	Low       []store.LowStockRow
	Canceled  []store.CanceledInvoiceRow
	Payment   *store.PaymentReport
	Customer  *store.CustomerReport
	Returns   *store.ReturnsAnalytics
}

func (s *Server) handleReportsIndex(w http.ResponseWriter, r *http.Request) {
	from, to := dateRange(r)
	_ = s.render(w, r, "reports_page.html", Page{Title: "التقارير", Data: reportView{Kind: "index", From: from, To: to}})
}

func (s *Server) renderReport(w http.ResponseWriter, r *http.Request, kind string, v reportView, err error) {
	if err != nil {
		s.renderAdminErr(w, r, "reports_page.html", Page{Title: "التقارير", Data: reportView{Kind: kind, From: v.From, To: v.To}}, err)
		return
	}
	v.Kind = kind
	_ = s.render(w, r, "reports_page.html", Page{Title: "التقارير", Data: v})
}

func (s *Server) handleReportSales(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	from, to := dateRange(r)
	rep, err := s.Store.SalesReportFor(r.Context(), u, from, to)
	s.renderReport(w, r, "sales", reportView{From: from, To: to, Sales: &rep}, err)
}

func (s *Server) handleReportProfit(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	from, to := dateRange(r)
	rep, err := s.Store.ProfitReportFor(r.Context(), u, from, to, strings.TrimSpace(r.URL.Query().Get("sale_type")))
	s.renderReport(w, r, "profit", reportView{From: from, To: to, Profit: &rep}, err)
}

func (s *Server) handleReportDebts(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	from, to := dateRange(r)
	debts, total, err := s.Store.DebtsReportFor(r.Context(), u)
	s.renderReport(w, r, "debts", reportView{From: from, To: to, Debts: debts, DebtTotal: total}, err)
}

func (s *Server) handleReportTopProducts(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	from, to := dateRange(r)
	top, err := s.Store.TopProducts(r.Context(), u, from, to, 20, false)
	s.renderReport(w, r, "top-products", reportView{From: from, To: to, Top: top}, err)
}

func (s *Server) handleReportLowStock(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	from, to := dateRange(r)
	low, err := s.Store.LowStockReport(r.Context(), u)
	s.renderReport(w, r, "low-stock", reportView{From: from, To: to, Low: low}, err)
}

func (s *Server) handleReportCanceled(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	from, to := dateRange(r)
	rows, err := s.Store.CanceledInvoicesFor(r.Context(), u, from, to)
	s.renderReport(w, r, "canceled", reportView{From: from, To: to, Canceled: rows}, err)
}

func (s *Server) handleReportPayments(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	from, to := dateRange(r)
	rep, err := s.Store.PaymentReportFor(r.Context(), u, from, to)
	s.renderReport(w, r, "payments", reportView{From: from, To: to, Payment: &rep}, err)
}

func (s *Server) handleReportCustomer(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	from, to := dateRange(r)
	cid, _ := strconv.ParseInt(r.URL.Query().Get("customer_id"), 10, 64)
	if cid == 0 {
		s.renderReport(w, r, "customer", reportView{From: from, To: to}, nil)
		return
	}
	rep, err := s.Store.CustomerReportFor(r.Context(), u, cid, from, to)
	s.renderReport(w, r, "customer", reportView{From: from, To: to, Customer: &rep}, err)
}

func (s *Server) handleReportProfitAnalysis(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	from, to := dateRange(r)
	rep, err := s.Store.ProfitReportFor(r.Context(), u, from, to, strings.TrimSpace(r.URL.Query().Get("sale_type")))
	s.renderReport(w, r, "profit-analysis", reportView{From: from, To: to, Profit: &rep}, err)
}

func (s *Server) handleReportReturns(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	from, to := dateRange(r)
	rep, err := s.Store.GetReturnsAnalytics(r.Context(), u, from, to)
	s.renderReport(w, r, "returns", reportView{From: from, To: to, Returns: rep}, err)
}

// apiErrForStoreCSV is apiErrForStore for the CSV download endpoint,
// which answers with plain http.Error text instead of JSON (FIX-25
// Observation E): denial -> 403, overflow -> 400 Arabic, "…غير موجود"
// -> 404, driver error -> 500 generic (logged server-side), any other
// store error -> 400 with its safe Arabic text.
func apiErrForStoreCSV(w http.ResponseWriter, r *http.Request, err error) {
	if isDenial(err) {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if errors.Is(err, money.ErrOverflow) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if isNotFoundSentinel(err) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if isDriverErr(err) {
		LogErr(r, err)
		http.Error(w, "حدث خطأ داخلي، حاول مرة أخرى", http.StatusInternalServerError)
		return
	}
	http.Error(w, err.Error(), http.StatusBadRequest)
}

func (s *Server) handleReportExport(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	from, to := dateRange(r)
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind == "" {
		kind = "sales"
	}
	data, filename, err := s.Store.ExportReportCSV(r.Context(), u, kind, from, to)
	if err != nil {
		apiErrForStoreCSV(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	_, _ = w.Write(data)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	d, err := s.Store.Dashboard(r.Context(), u)
	if err != nil {
		s.renderAdminErr(w, r, "reports_dashboard.html", Page{Title: "لوحة المدير"}, err)
		return
	}
	_ = s.render(w, r, "reports_dashboard.html", Page{Title: "لوحة المدير", Data: d})
}

func (s *Server) handleShiftMonitor(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	from, to := dateRange(r)
	m, err := s.Store.ShiftMonitorFor(r.Context(), u, from, to)
	if err != nil {
		s.renderAdminErr(w, r, "shifts_monitor.html", Page{Title: "مراقب الشيفتات"}, err)
		return
	}
	_ = s.render(w, r, "shifts_monitor.html", Page{Title: "مراقب الشيفتات", Data: m})
}

// --- Attendance ---

func (s *Server) handleAttendance(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	from, to := dateRange(r)
	sum, err := s.Store.ListAttendance(r.Context(), u, from, to, strings.TrimSpace(r.URL.Query().Get("user")))
	if err != nil {
		s.renderAdminErr(w, r, "attendance_list.html", Page{Title: "الحضور"}, err)
		return
	}
	_ = s.render(w, r, "attendance_list.html", Page{Title: "الحضور", Data: sum})
}

// --- JSON API ---

func (s *Server) apiNotifUnreadCount(w http.ResponseWriter, r *http.Request) {
	n, err := s.Store.UnreadNotificationCount(r.Context(), CurrentUser(r))
	if err != nil {
		apiErrInternal(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "unread": n})
}

func (s *Server) apiNotifList(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.ListInbox(r.Context(), CurrentUser(r), 50)
	if err != nil {
		apiErrInternal(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "notifications": items})
}

func (s *Server) apiNotifRead(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		WriteJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "معرّف غير صحيح"})
		return
	}
	if err := s.Store.MarkNotificationRead(r.Context(), CurrentUser(r), id); err != nil {
		apiErrForStore(w, r, err, http.StatusNotFound)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) apiBackupNow(w http.ResponseWriter, r *http.Request) {
	res, err := s.Store.CreateBackup(r.Context(), CurrentUser(r))
	if err != nil {
		apiErrInternal(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"ok": true, "file": res.FileName, "local_size": res.LocalSize,
		"cloud_status": res.CloudStatus, "cloud_size": res.CloudSize, "error": res.Error,
	})
}

func (s *Server) apiReportSales(w http.ResponseWriter, r *http.Request) {
	from, to := dateRange(r)
	if q := r.URL.Query(); q.Get("from") != "" || q.Get("to") != "" {
		from, to = q.Get("from"), q.Get("to")
		if to == "" {
			to = store.Today()
		}
		if from == "" {
			from = to
		}
	}
	rep, err := s.Store.SalesReportFor(r.Context(), CurrentUser(r), from, to)
	if err != nil {
		apiErrForStore(w, r, err, http.StatusNotFound)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "report": rep})
}

func (s *Server) apiAuditUnreadCount(w http.ResponseWriter, r *http.Request) {
	n, err := s.Store.UnreadAuditAlertsCount(r.Context(), CurrentUser(r))
	if err != nil {
		apiErrForStore(w, r, err, http.StatusNotFound)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "unread": n})
}

func (s *Server) apiBackupList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListBackups(r.Context(), CurrentUser(r))
	if err != nil {
		apiErrForStore(w, r, err, http.StatusNotFound)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "backups": list})
}
