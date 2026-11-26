// Package web — staff online-orders (web bell, FIX-3 H1).
//
// The bell reads the LOCAL OnlineOrders store (see store/domain_orders.go).
// Endpoints mirror the CloudApi staff surface:
//   GET  /api/staff/orders/pending-count    {pendingCount}
//   GET  /api/staff/orders/pending-evidence {pendingEvidence}
//   GET  /api/staff/orders/all
//   GET  /api/staff/orders/{id}
//   POST /api/staff/orders                  (intake, admin/manager)
//   POST /api/staff/orders/{id}/resolve-evidence (admin/manager)
//   POST /api/staff/orders/{id}/status      (admin/manager)
//   GET  /orders                            (HTML list)
//   POST /api/auth/login                    {username,password} -> {token}
//
// Authentication: the cookie session OR a Bearer JWT issued by
// /api/auth/login. No token at all -> 401. A JWT names a user, but the
// role used for authorization is always re-read from the Users table, so
// a deactivated user or a role change takes effect immediately.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/money"
	"cashierpos-go/internal/rbac"
	"cashierpos-go/internal/store"
)

// staffUser resolves the caller for a staff API request: the cookie
// session first, then a Bearer JWT. Nil when neither authenticates.
func (s *Server) staffUser(r *http.Request) *auth.User {
	if u := CurrentUser(r); u != nil {
		return u
	}
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(h, "Bearer ") {
		return nil
	}
	claims, err := auth.VerifyJWT(s.jwtSecret, strings.TrimSpace(h[len("Bearer "):]))
	if err != nil || claims == nil {
		return nil
	}
	var u auth.User
	var isActive, mustChange int
	err = s.Store.DB.QueryRow(`SELECT UserID,UserName,FullName,Role,IsActive,MustChangePassword
		FROM Users WHERE UserName=?`, claims.Username).
		Scan(&u.ID, &u.UserName, &u.FullName, &u.Role, &isActive, &mustChange)
	if err != nil || isActive != 1 {
		return nil
	}
	u.IsActive = true
	u.MustChangePassword = mustChange == 1
	return &u
}

// RequireStaffAPI is the staff-API gate: 401 JSON without a cookie
// session or a valid Bearer JWT, 403 JSON when the password must change
// first (same rule as RequireLogin). The resolved user is put into the
// request context so handlers use CurrentUser as usual.
func (s *Server) RequireStaffAPI(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := s.staffUser(r)
		if u == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "يلزم تسجيل الدخول"})
			return
		}
		if u.MustChangePassword {
			writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "يجب تغيير كلمة السر أولاً"})
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), userKey, u))
		next(w, r)
	}
}

// RequireStaffPerm adds one permission check on top of RequireStaffAPI.
func (s *Server) RequireStaffPerm(perm string, next http.HandlerFunc) http.HandlerFunc {
	return s.RequireStaffAPI(func(w http.ResponseWriter, r *http.Request) {
		if err := rbac.Require(CurrentUser(r).Role, perm); err != nil {
			writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		next(w, r)
	})
}

func (s *Server) registerOrdersRoutes(m *http.ServeMux) {
	m.HandleFunc("POST /api/auth/login", s.handleAPIAuthLogin)
	m.HandleFunc("GET /api/staff/orders/pending-count", s.RequireStaffAPI(s.handleAPIOrdersPendingCount))
	m.HandleFunc("GET /api/staff/orders/pending-evidence", s.RequireStaffAPI(s.handleAPIOrdersPendingEvidence))
	m.HandleFunc("GET /api/staff/orders/all", s.RequireStaffAPI(s.handleAPIOrdersAll))
	m.HandleFunc("GET /api/staff/orders/{id}", s.RequireStaffAPI(s.handleAPIOrderGet))
	m.HandleFunc("POST /api/staff/orders", s.RequireStaffPerm(rbac.PermManageOrders, s.handleAPIOrderCreate))
	m.HandleFunc("POST /api/staff/orders/{id}/resolve-evidence", s.RequireStaffPerm(rbac.PermManageOrders, s.handleAPIOrderResolveEvidence))
	m.HandleFunc("POST /api/staff/orders/{id}/status", s.RequireStaffPerm(rbac.PermManageOrders, s.handleAPIOrderSetStatus))
	m.HandleFunc("GET /orders", s.RequireLogin(s.handleOrdersPage))
	m.HandleFunc("POST /orders/{id}/resolve-evidence", s.RequirePerm(rbac.PermManageOrders, s.handleOrdersResolveEvidence))
}

// handleAPIAuthLogin issues a 24h Bearer JWT for valid credentials.
func (s *Server) handleAPIAuthLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apiErr(w, http.StatusBadRequest, errors.New("طلب غير صحيح"))
		return
	}
	u, err := auth.Authenticate(s.Store.DB, req.Username, req.Password)
	if err != nil {
		apiErr(w, http.StatusUnauthorized, err)
		return
	}
	if u.MustChangePassword {
		apiErr(w, http.StatusForbidden, errors.New("يجب تغيير كلمة السر أولاً"))
		return
	}
	if s.jwtSecret == "" {
		apiErr(w, http.StatusInternalServerError, errors.New("التوكن غير مفعّل على هذا الخادم"))
		return
	}
	token, err := auth.SignJWT(s.jwtSecret, u.UserName, u.Role, 24*time.Hour)
	if err != nil {
		apiErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "token": token, "username": u.UserName, "role": u.Role,
	})
}

func (s *Server) handleAPIOrdersPendingCount(w http.ResponseWriter, r *http.Request) {
	n, err := s.Store.PendingOrdersCount(r.Context(), CurrentUser(r))
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pendingCount": n})
}

func (s *Server) handleAPIOrdersPendingEvidence(w http.ResponseWriter, r *http.Request) {
	n, err := s.Store.PendingEvidenceCount(r.Context(), CurrentUser(r))
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pendingEvidence": n})
}

func orderJSON(o *store.OnlineOrder) map[string]any {
	items := make([]map[string]any, 0, len(o.Items))
	for _, it := range o.Items {
		items = append(items, map[string]any{
			"product_id": it.ProductID, "name": it.Name,
			"qty": int64(it.Qty), "unit_price": int64(it.UnitPrice),
		})
	}
	return map[string]any{
		"order_id": o.ID, "customer_name": o.CustomerName, "customer_phone": o.CustomerPhone,
		"items": items, "total": int64(o.Total), "status": o.Status,
		"payment_status": o.PaymentStatus, "payment_method": o.PaymentMethod,
		"evidence_ref": o.EvidenceRef, "notes": o.Notes, "created_by": o.CreatedBy,
		"created_at": o.CreatedAt, "updated_at": o.UpdatedAt,
	}
}

func (s *Server) handleAPIOrdersAll(w http.ResponseWriter, r *http.Request) {
	orders, err := s.Store.ListOnlineOrders(r.Context(), CurrentUser(r), r.URL.Query().Get("status"))
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	out := make([]map[string]any, 0, len(orders))
	for _, o := range orders {
		out = append(out, orderJSON(o))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "orders": out})
}

func (s *Server) handleAPIOrderGet(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	o, err := s.Store.GetOnlineOrder(r.Context(), CurrentUser(r), id)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "order": orderJSON(o)})
}

type apiOrderItem struct {
	ProductID int64  `json:"product_id"`
	Name      string `json:"name"`
	Qty       string `json:"qty"`
	UnitPrice int64  `json:"unit_price"`
}

type apiOrderCreate struct {
	CustomerName  string         `json:"customer_name"`
	CustomerPhone string         `json:"customer_phone"`
	Items         []apiOrderItem `json:"items"`
	Total         int64          `json:"total"`
	Status        string         `json:"status"`
	PaymentStatus string         `json:"payment_status"`
	PaymentMethod string         `json:"payment_method"`
	EvidenceRef   string         `json:"evidence_ref"`
	Notes         string         `json:"notes"`
}

func (s *Server) handleAPIOrderCreate(w http.ResponseWriter, r *http.Request) {
	var req apiOrderCreate
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apiErr(w, http.StatusBadRequest, errors.New("طلب غير صحيح"))
		return
	}
	in := store.OnlineOrderInput{
		CustomerName: req.CustomerName, CustomerPhone: req.CustomerPhone,
		Total: money.Cents(req.Total), Status: req.Status,
		PaymentStatus: req.PaymentStatus, PaymentMethod: req.PaymentMethod,
		EvidenceRef: req.EvidenceRef, Notes: req.Notes,
	}
	for _, it := range req.Items {
		q, err := money.ParseQty(it.Qty)
		if err != nil {
			apiErr(w, http.StatusBadRequest, err)
			return
		}
		in.Items = append(in.Items, store.OnlineOrderItem{
			ProductID: it.ProductID, Name: it.Name, Qty: q, UnitPrice: money.Cents(it.UnitPrice),
		})
	}
	o, err := s.Store.CreateOnlineOrder(r.Context(), CurrentUser(r), in)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "order": orderJSON(o)})
}

func (s *Server) handleAPIOrderResolveEvidence(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	o, err := s.Store.ResolveOrderEvidence(r.Context(), CurrentUser(r), id)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "order": orderJSON(o)})
}

func (s *Server) handleAPIOrderSetStatus(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apiErr(w, http.StatusBadRequest, errors.New("طلب غير صحيح"))
		return
	}
	o, err := s.Store.SetOnlineOrderStatus(r.Context(), CurrentUser(r), id, req.Status)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "order": orderJSON(o)})
}

// handleOrdersResolveEvidence is the HTML form version of the API
// resolve: it redirects back to /orders with a notice. A refusal
// answers 400 like the API path (FIX-6 R6-L4), not a 200 error page.
func (s *Server) handleOrdersResolveEvidence(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.renderErrStatus(w, r, "طلبات المتجر", err, http.StatusBadRequest)
		return
	}
	if _, err := s.Store.ResolveOrderEvidence(r.Context(), CurrentUser(r), id); err != nil {
		s.renderErrStatus(w, r, "طلبات المتجر", err, http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/orders?resolved=1", http.StatusSeeOther)
}

// handleOrdersPage renders the staff orders list (bell destination).
func (s *Server) handleOrdersPage(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	orders, err := s.Store.ListOnlineOrders(r.Context(), u, r.URL.Query().Get("status"))
	if err != nil {
		s.renderErr(w, r, "طلبات المتجر", err)
		return
	}
	pending, _ := s.Store.PendingOrdersCount(r.Context(), u)
	evidence, _ := s.Store.PendingEvidenceCount(r.Context(), u)
	_ = s.render(w, r, "orders_list.html", Page{Title: "طلبات المتجر", ActiveNav: "orders", Data: map[string]any{
		"orders": orders, "pending": pending, "evidence": evidence,
		"status": r.URL.Query().Get("status"),
		"canManage": rbac.Can(u.Role, rbac.PermManageOrders),
	}})
}
