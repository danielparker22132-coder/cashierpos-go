// Package web — B3: customers / vendors / purchases / payables /
// courier debts / electronic transfers handlers.
//
// All routes are registered by the single function
// registerCustomersRoutes, which routes.go (B5) calls from Server.routes().
// Every mutating service call re-checks permissions in the service layer;
// handlers add RequireLogin / RequirePerm as a UI gate only.
// Amounts in HTML forms are EGP decimals ("12.50"); JSON APIs accept
// either "amount": "12.50" (EGP string) or "amount_cents": 1250.
package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"cashierpos-go/internal/money"
	"cashierpos-go/internal/rbac"
	"cashierpos-go/internal/store"
)

// registerCustomersRoutes registers every B3 HTML page and JSON API route.
// It is called from routes.go (B5 wiring); do not call it twice.
func (s *Server) registerCustomersRoutes(m *http.ServeMux) {
	// --- Customers (HTML) ---
	m.HandleFunc("GET /customers", s.RequirePerm("manage_customers", s.customersList))
	m.HandleFunc("GET /customers/new", s.RequirePerm("manage_customers", s.customerNewForm))
	m.HandleFunc("POST /customers/new", s.RequirePerm("manage_customers", s.customerNewSubmit))
	m.HandleFunc("GET /customers/{id}", s.RequirePerm("manage_customers", s.customerDetail))
	m.HandleFunc("GET /customers/{id}/edit", s.RequirePerm("manage_customers", s.customerEditForm))
	m.HandleFunc("POST /customers/{id}/edit", s.RequirePerm("manage_customers", s.customerEditSubmit))
	m.HandleFunc("POST /customers/{id}/delete", s.RequirePerm("manage_customers", s.customerDelete))
	m.HandleFunc("POST /customers/{id}/pay", s.RequirePerm("collect_payment", s.customerPaySubmit))
	m.HandleFunc("POST /customers/{id}/pay-invoice", s.RequirePerm("collect_payment", s.customerPayInvoiceSubmit))
	m.HandleFunc("GET /customers/{id}/statement", s.RequirePerm("manage_customers", s.customerStatement))
	m.HandleFunc("GET /debtors", s.RequirePerm("manage_customers", s.debtorsList))
	// --- Vendors + purchases (HTML) ---
	m.HandleFunc("GET /vendors", s.RequireLogin(s.vendorsList))
	m.HandleFunc("GET /vendors/new", s.RequirePerm("create_vendor", s.vendorNewForm))
	m.HandleFunc("POST /vendors/new", s.RequirePerm("create_vendor", s.vendorNewSubmit))
	m.HandleFunc("GET /vendors/{id}", s.RequireLogin(s.vendorDetail))
	m.HandleFunc("GET /vendors/{id}/edit", s.RequirePerm("manage_vendors", s.vendorEditForm))
	m.HandleFunc("POST /vendors/{id}/edit", s.RequirePerm("manage_vendors", s.vendorEditSubmit))
	m.HandleFunc("POST /vendors/{id}/delete", s.RequirePerm("manage_vendors", s.vendorDelete))
	m.HandleFunc("GET /vendors/{id}/ledger", s.RequireLogin(s.vendorLedger))
	m.HandleFunc("POST /vendors/{id}/pay", s.RequirePerm("pay_vendors", s.vendorPaySubmit))
	m.HandleFunc("GET /purchases/new", s.RequirePerm("receive_purchase", s.purchaseNewForm))
	m.HandleFunc("POST /purchases/new", s.RequirePerm("receive_purchase", s.purchaseNewSubmit))
	m.HandleFunc("GET /purchases/{id}", s.RequireLogin(s.purchaseDetail))
	// --- Manual payables (HTML) ---
	// FIX-12 SW-H08 (§15 row 79): the payables views are NOT open to
	// every login — warehouse is denied; admin/manager/supervisor/
	// cashier keep access (requirePayablesView).
	m.HandleFunc("GET /payables", s.requirePayablesView(s.payablesList))
	m.HandleFunc("GET /payables/new", s.RequirePerm("manage_payables", s.payableNewForm))
	m.HandleFunc("POST /payables/new", s.RequirePerm("manage_payables", s.payableNewSubmit))
	m.HandleFunc("GET /payables/alerts", s.requirePayablesView(s.payablesAlerts))
	m.HandleFunc("POST /payables/{id}/settle", s.RequirePerm("settle_payable", s.payableSettleSubmit))
	m.HandleFunc("POST /payables/{id}/cancel", s.RequirePerm("manage_payables", s.payableCancelSubmit))
	// --- Courier debts (HTML) ---
	m.HandleFunc("GET /courier-debts", s.RequirePerm("view_courier_debts", s.courierList))
	m.HandleFunc("GET /courier-debts/new", s.RequirePerm("edit_courier_debts", s.courierNewForm))
	m.HandleFunc("POST /courier-debts/new", s.RequirePerm("edit_courier_debts", s.courierNewSubmit))
	m.HandleFunc("GET /courier-debts/{id}/edit", s.RequirePerm("edit_courier_debts", s.courierEditForm))
	m.HandleFunc("POST /courier-debts/{id}/edit", s.RequirePerm("edit_courier_debts", s.courierEditSubmit))
	// FIX-12 SW-H03: settling a courier debt is open to any cashier
	// receiving cash as well as to edit_courier_debts holders —
	// requireCourierSettle, not the edit-only gate. Create/edit/delete
	// stay edit-only.
	m.HandleFunc("POST /courier-debts/{id}/settle", s.requireCourierSettle(s.courierSettleSubmit))
	m.HandleFunc("POST /courier-debts/{id}/delete", s.RequirePerm("edit_courier_debts", s.courierDelete))
	// --- Electronic transfers (HTML) ---
	m.HandleFunc("GET /transfers", s.RequirePerm("view_transfers", s.transfersList))
	m.HandleFunc("GET /transfers/new", s.RequirePerm("view_transfers", s.transferNewForm))
	m.HandleFunc("POST /transfers/new", s.RequirePerm("view_transfers", s.transferNewSubmit))
	m.HandleFunc("POST /transfers/{id}/approve", s.RequirePerm("approve_transfer", s.transferApproveSubmit))
	m.HandleFunc("POST /transfers/{id}/reject", s.RequirePerm("reject_transfer", s.transferRejectSubmit))

	// --- JSON API ---
	m.HandleFunc("GET /api/customers", s.RequireLogin(s.apiCustomersList))
	m.HandleFunc("POST /api/customers", s.RequirePerm("manage_customers", s.apiCustomerCreate))
	m.HandleFunc("GET /api/customers/{id}", s.RequireLogin(s.apiCustomerGet))
	m.HandleFunc("POST /api/customers/{id}/pay", s.RequirePerm("collect_payment", s.apiCustomerPay))
	m.HandleFunc("POST /api/customers/{id}/pay-invoice", s.RequirePerm("collect_payment", s.apiCustomerPayInvoice))
	m.HandleFunc("GET /api/debtors", s.RequireLogin(s.apiDebtorsList))
	m.HandleFunc("GET /api/vendors", s.RequireLogin(s.apiVendorsList))
	m.HandleFunc("POST /api/vendors", s.RequirePerm("create_vendor", s.apiVendorCreate))
	m.HandleFunc("POST /api/vendors/{id}/pay", s.RequirePerm("pay_vendors", s.apiVendorPay))
	m.HandleFunc("POST /api/purchases", s.RequirePerm("receive_purchase", s.apiPurchaseCreate))
	m.HandleFunc("GET /api/purchases/{id}", s.RequireLogin(s.apiPurchaseGet))
	m.HandleFunc("GET /api/payables", s.requirePayablesView(s.apiPayablesList))
	m.HandleFunc("POST /api/payables", s.RequirePerm("manage_payables", s.apiPayableCreate))
	m.HandleFunc("GET /api/payables/due", s.requirePayablesView(s.apiPayablesDue))
	m.HandleFunc("POST /api/payables/{id}/settle", s.RequirePerm("settle_payable", s.apiPayableSettle))
	m.HandleFunc("POST /api/payables/{id}/cancel", s.RequirePerm("manage_payables", s.apiPayableCancel))
	m.HandleFunc("GET /api/courier-debts", s.RequirePerm("view_courier_debts", s.apiCourierList))
	m.HandleFunc("POST /api/courier-debts", s.RequirePerm("edit_courier_debts", s.apiCourierCreate))
	m.HandleFunc("POST /api/courier-debts/{id}/settle", s.requireCourierSettle(s.apiCourierSettle))
	m.HandleFunc("GET /api/transfers", s.RequirePerm("view_transfers", s.apiTransfersList))
	m.HandleFunc("POST /api/transfers", s.RequirePerm("view_transfers", s.apiTransferCreate))
	m.HandleFunc("POST /api/transfers/{id}/approve", s.RequirePerm("approve_transfer", s.apiTransferApprove))
	m.HandleFunc("POST /api/transfers/{id}/reject", s.RequirePerm("reject_transfer", s.apiTransferReject))
}

// --- small helpers ---

func (s *Server) redirectWithNotice(w http.ResponseWriter, r *http.Request, dest, notice string) {
	if notice != "" {
		sep := "?"
		if strings.Contains(dest, "?") {
			sep = "&"
		}
		dest += sep + "notice=" + url.QueryEscape(notice)
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

func noticeFromQuery(r *http.Request) string { return r.URL.Query().Get("notice") }

// requirePayablesView gates payables reads (FIX-12 SW-H08, §15 row 79):
// admin/manager (manage_payables), supervisor (view) and cashier
// (settle_payable) may view; warehouse may not. The service layer
// (ListPayables/DuePayables) denies warehouse too, so the two gates
// cannot drift apart.
func (s *Server) requirePayablesView(next http.HandlerFunc) http.HandlerFunc {
	// FIX-12 SW-H08 + hub-link follow-up: a single view permission —
	// admin/manager/supervisor/cashier hold it, warehouse does not.
	return s.RequirePerm(rbac.PermViewPayables, next)
}

// requireCourierSettle gates courier-debt settlement (FIX-12 SW-H03):
// edit_courier_debts holders plus ANY cashier receiving cash (the
// service layer uses the same store.CanSettleCourierDebt helper).
// Create/edit/delete stay on the edit-only gate.
func (s *Server) requireCourierSettle(next http.HandlerFunc) http.HandlerFunc {
	return s.RequireLogin(func(w http.ResponseWriter, r *http.Request) {
		u := CurrentUser(r)
		if store.CanSettleCourierDebt(u.Role) {
			next(w, r)
			return
		}
		s.deny(w, r, rbac.Require(u.Role, rbac.PermEditCourierDebts).Error())
	})
}

// isDenial reports whether err is a uniform RBAC denial (FIX-12 SW-M12):
// service-layer permission denials must surface as HTTP 403, not 500.
func isDenial(err error) bool {
	return err != nil && strings.Contains(err.Error(), "غير مسموح")
}

// htmlStoreErrMsg maps a store error to the message an HTML page may show
// (FIX-26 R26-B-O1): a SQLite driver error becomes the single generic
// Arabic message — the raw driver string ("SQL logic error: no such
// table: X (1)") must never reach the alert div — while every other
// error keeps its own (safe Arabic) text and the page keeps its 200
// status. It is the HTML counterpart of the apiErrForStore driver leg;
// it uses the unified detector (isDriverErr) and does not
// change it.
func htmlStoreErrMsg(err error) string {
	if err == nil {
		return ""
	}
	if isDriverErr(err) {
		return "حدث خطأ داخلي، حاول مرة أخرى"
	}
	return err.Error()
}

// apiErrForStore maps a Store-service error to its HTTP answer
// (FIX-25 Observation E — the GetCustomer trichotomy of FIX-24
// R24-E-N1, generalised):
//   - permission denial -> 403 with the service's own text;
//   - money.ErrOverflow -> 400 with its Arabic message;
//   - an exact "…غير موجود" / "…غير موجودة" sentinel -> notFoundStatus
//     with that text (isNotFoundSentinel covers both genders)
//     (404 for the customers/admin JSON surface; the staff orders API
//     keeps its established 400 for a missing order, so callers there
//     pass http.StatusBadRequest);
//   - a SQLite driver error -> 500 with the single generic Arabic
//     message via apiErrInternal (logged server-side, never echoed);
//   - any other store error -> 400 with its text. Those are genuine
//     business/validation refusals: every error these store functions
//     return besides a driver error is a static Arabic literal or a
//     fmt with safe values only (counts, formatted money, status enums,
//     ids) — re-derived by reading each function; no %v/%w wrapping of
//     a driver error exists in them. Keeping the text preserves the
//     refused-with-a-message behaviour the suite asserts (e.g. the
//     HTML resolve refusal "لا يمكن مراجعة دليل دفع طلب ملغى").
func apiErrForStore(w http.ResponseWriter, r *http.Request, err error, notFoundStatus int) {
	if isDenial(err) {
		apiErr(w, http.StatusForbidden, err)
		return
	}
	if errors.Is(err, money.ErrOverflow) {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	if isNotFoundSentinel(err) {
		apiErr(w, notFoundStatus, err)
		return
	}
	if isDriverErr(err) {
		apiErrInternal(w, r, err)
		return
	}
	apiErr(w, http.StatusBadRequest, err)
}

// isNotFoundSentinel reports whether err is an exact "…غير موجود"
// missing-entity sentinel. The feminine form matters: GetPurchase
// returns "فاتورة الشراء غير موجودة" (with ة), which a bare
// "غير موجود" suffix check misses — that 404 leg silently fell
// through to 400 until this was caught by the lane tests.
func isNotFoundSentinel(err error) bool {
	msg := err.Error()
	return strings.HasSuffix(msg, "غير موجود") || strings.HasSuffix(msg, "غير موجودة")
}

// --- FIX-12 SW-M04 error-render helpers ---
//
// These templates deref .Data.* unconditionally, so an error render
// with no Data crashes the render itself (HTTP 500) and the guard's
// Arabic message never reaches the user. Every helper below supplies
// the zero Data the template needs, so the message renders with 200.

// renderCustomersListErr renders customers_list.html with an error.
func (s *Server) renderCustomersListErr(w http.ResponseWriter, r *http.Request, msg string) {
	_ = s.render(w, r, "customers_list.html", Page{Title: "العملاء", Error: msg,
		Data: map[string]any{"Q": "", "Customers": ([]*store.Customer)(nil)}})
}

// renderCustomerDetailErr renders customers_detail.html with an error.
// The template derefs .Data.Customer, so when the customer cannot be
// loaded at all the list page is rendered instead.
func (s *Server) renderCustomerDetailErr(w http.ResponseWriter, r *http.Request, id int64, msg string) {
	if c, err := s.Store.GetCustomer(r.Context(), CurrentUser(r), id); err == nil {
		st, _ := s.Store.CustomerStatement(r.Context(), CurrentUser(r), id)
		_ = s.render(w, r, "customers_detail.html", Page{Title: "العميل", Error: msg,
			Data: map[string]any{"Customer": c, "Statement": st, "BalanceEGP": centsToEGP(c.Balance)}})
		return
	}
	s.renderCustomersListErr(w, r, msg)
}

// renderVendorsListErr renders vendors_list.html with an error.
func (s *Server) renderVendorsListErr(w http.ResponseWriter, r *http.Request, msg string) {
	list, _ := s.Store.ListVendors(r.Context(), CurrentUser(r), "")
	_ = s.render(w, r, "vendors_list.html", Page{Title: "الموردون", Error: msg,
		Data: map[string]any{"Vendors": list, "Q": "", "Alerts": []*store.VendorDueAlert(nil),
			// FIX-15 R15-M1: the list template gates per-invoice alert
			// amounts on canViewProfit — pass the flag here too.
			// FIX-16 R16-M1: the list template also gates the
			// CurrentBalance column on canViewVendorMoney.
			"canViewProfit": rbac.Can(CurrentUser(r).Role, rbac.PermViewProfit),
			"canViewVendorMoney": canViewVendorMoney(CurrentUser(r).Role)}})
}

// renderPurchaseFormErr renders vendors_purchase_form.html with an error.
func (s *Server) renderPurchaseFormErr(w http.ResponseWriter, r *http.Request, msg string) {
	vendors, _ := s.Store.ListVendors(r.Context(), CurrentUser(r), "")
	products, _ := s.Store.ListProducts(r.Context(), CurrentUser(r), false)
	_ = s.render(w, r, "vendors_purchase_form.html", Page{Title: "فاتورة شراء جديدة", Error: msg,
		Data: map[string]any{"Vendors": vendors, "Products": products}})
}

// renderPayablesListErr renders payables_list.html with an error.
func (s *Server) renderPayablesListErr(w http.ResponseWriter, r *http.Request, msg string) {
	list, _ := s.Store.ListPayables(r.Context(), CurrentUser(r), "", "")
	_ = s.render(w, r, "payables_list.html", Page{Title: "المستحقات اليدوية", Error: msg,
		Data: map[string]any{"Payables": list}})
}

// renderCourierListErr renders courier_list.html with an error.
func (s *Server) renderCourierListErr(w http.ResponseWriter, r *http.Request, msg string) {
	list, _ := s.Store.ListCourierDebts(r.Context(), CurrentUser(r), "")
	_ = s.render(w, r, "courier_list.html", Page{Title: "ديون المناديب", Error: msg,
		Data: map[string]any{"Debts": list, "CanSettle": store.CanSettleCourierDebt(CurrentUser(r).Role)}})
}

// centsToEGP formats cents as a plain "12.50" decimal for <input> values
// (the egp template func appends " ج.م" and cannot be used in inputs).
func centsToEGP(c money.Cents) string {
	if c < 0 {
		return "-" + centsToEGP(-c)
	}
	return fmt.Sprintf("%d.%02d", int64(c)/100, int64(c)%100)
}

// apiAmount parses a JSON amount: "amount_cents" (int64) wins, else
// "amount" as an EGP decimal string.
func apiAmount(body map[string]any) (money.Cents, error) {
	if v, ok := body["amount_cents"]; ok {
		switch n := v.(type) {
		case float64:
			if n < 0 {
				return 0, errNegativeAmount
			}
			// R19-B-F3: refuse out-of-range floats here, at parse
			// time, instead of letting int64(n) wrap to min-int64
			// and relying on a downstream negative check.
			return money.CentsFromFloat(n)
		}
	}
	if v, ok := body["amount"]; ok {
		switch n := v.(type) {
		case string:
			return money.ParseMoney(n)
		case float64:
			return money.ParseMoney(strconv.FormatFloat(n, 'f', 2, 64))
		}
	}
	return 0, errMissingAmount
}

var (
	errNegativeAmount = errString("المبلغ لا يمكن أن يكون سالباً")
	errMissingAmount  = errString("المبلغ مطلوب")
)

type errString string

func (e errString) Error() string { return string(e) }

func readJSONBody(r *http.Request) (map[string]any, error) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return nil, errString("طلب JSON غير صحيح")
	}
	return body, nil
}

func strVal(body map[string]any, key string) string {
	if v, ok := body[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func intVal(body map[string]any, key string) int64 {
	if v, ok := body[key]; ok {
		if f, ok := v.(float64); ok {
			return int64(f)
		}
	}
	return 0
}

// FIX-17 SW2-M03: strVal returns "" for any non-string JSON value, so a
// numeric credit_limit / paid / price was silently dropped — a numeric
// credit_limit became 0, i.e. unlimited credit. amountFromValue parses a
// JSON value the way the core typed-struct money APIs read bare numbers:
// a string is an EGP decimal ("50.00" -> 5000) and a JSON number is
// CENTS (100 -> 100). FIX-18 R18-B-01: the fix-17 reading (number = EGP
// decimal, 100 -> 10000) contradicted the rest of the API — POST
// /api/sales/create and POST /api/staff/orders read bare int64 numbers
// as cents — so the number branch now mirrors them. A missing, null or
// empty-string value is absent (not zero); any other type is refused
// instead of silently dropped. Returns (value, present, error).
func amountFromValue(v any) (money.Cents, bool, error) {
	if v == nil {
		return 0, false, nil
	}
	switch n := v.(type) {
	case string:
		if strings.TrimSpace(n) == "" {
			return 0, false, nil
		}
		c, err := money.ParseMoney(n)
		return c, true, err
	case float64:
		// FIX-18 R18-B-01: bare JSON number = cents. Fractional
		// cents truncate (100.50 -> 100), exactly like int64(n) in
		// the typed-struct APIs; negatives are refused here so the
		// refusal message matches the string branch.
		if n < 0 {
			return 0, true, errNegativeAmount
		}
		// R19-B-F3: refuse out-of-range floats here, at parse time,
		// instead of letting int64(n) wrap to min-int64.
		c, cerr := money.CentsFromFloat(n)
		if cerr != nil {
			return 0, true, cerr
		}
		return c, true, nil
	default:
		return 0, true, errString("مبلغ غير صحيح")
	}
}

// strFromValue renders a JSON value as the string its parser expects:
// strings pass through, numbers render in full ("2", "2.5"). Used for
// quantity fields (ParseQty), where a numeric JSON value was refused
// outright because strVal dropped it to "".
func strFromValue(v any) string {
	if v == nil {
		return ""
	}
	switch n := v.(type) {
	case string:
		return n
	case float64:
		return strconv.FormatFloat(n, 'f', -1, 64)
	default:
		return ""
	}
}

func apiOK(w http.ResponseWriter, data map[string]any) {
	data["ok"] = true
	WriteJSON(w, http.StatusOK, data)
}

// --- Customers: HTML ---

func (s *Server) customersList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListCustomers(r.Context(), CurrentUser(r), r.URL.Query().Get("q"))
	if err != nil {
		LogErr(r, err)
		// FIX-12 SW-M04: the template derefs .Data.Q — render with Data.
		_ = s.render(w, r, "customers_list.html", Page{Title: "العملاء", Error: "تعذّر تحميل العملاء",
			Data: map[string]any{"Customers": list, "Q": r.URL.Query().Get("q")}})
		return
	}
	_ = s.render(w, r, "customers_list.html", Page{Title: "العملاء", ActiveNav: "customers",
		Notice: noticeFromQuery(r), Data: map[string]any{"Customers": list, "Q": r.URL.Query().Get("q")}})
}

func (s *Server) customerNewForm(w http.ResponseWriter, r *http.Request) {
	_ = s.render(w, r, "customers_form.html", Page{Title: "عميل جديد", Data: map[string]any{"IsNew": true, "CreditLimitEGP": "0.00"}})
}

func (s *Server) customerNewSubmit(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	limit, err := formMoney(r, "credit_limit")
	if err != nil {
		_ = s.render(w, r, "customers_form.html", Page{Title: "عميل جديد", Error: htmlStoreErrMsg(err), Data: map[string]any{"IsNew": true, "CreditLimitEGP": "0.00"}})
		return
	}
	id, err := s.Store.CreateCustomer(r.Context(), u, store.CustomerInput{
		Name: r.FormValue("name"), Phone: r.FormValue("phone"),
		Address: r.FormValue("address"), CreditLimit: limit, IsActive: true,
	})
	if err != nil {
		_ = s.render(w, r, "customers_form.html", Page{Title: "عميل جديد", Error: htmlStoreErrMsg(err), Data: map[string]any{"IsNew": true, "CreditLimitEGP": "0.00"}})
		return
	}
	s.redirectWithNotice(w, r, "/customers/"+strconv.FormatInt(id, 10), "تمت إضافة العميل")
}

func (s *Server) customerDetail(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	c, err := s.Store.GetCustomer(r.Context(), CurrentUser(r), id)
	if err != nil {
		s.renderCustomersListErr(w, r, htmlStoreErrMsg(err))
		return
	}
	st, err := s.Store.CustomerStatement(r.Context(), CurrentUser(r), id)
	if err != nil {
		LogErr(r, err)
	}
	_ = s.render(w, r, "customers_detail.html", Page{Title: "العميل: " + c.Name,
		Notice: noticeFromQuery(r), Data: map[string]any{"Customer": c, "Statement": st, "BalanceEGP": centsToEGP(c.Balance)}})
}

func (s *Server) customerEditForm(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	c, err := s.Store.GetCustomer(r.Context(), CurrentUser(r), id)
	if err != nil {
		s.renderCustomersListErr(w, r, htmlStoreErrMsg(err))
		return
	}
	_ = s.render(w, r, "customers_form.html", Page{Title: "تعديل العميل", Data: map[string]any{"IsNew": false, "Customer": c, "CreditLimitEGP": centsToEGP(c.CreditLimit)}})
}

func (s *Server) customerEditSubmit(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	limit, err := formMoney(r, "credit_limit")
	if err != nil {
		// FIX-12 SW-M04: the form derefs .Data.Customer.IsActive when
		// IsNew=false — supply the submitted values, not a bare IsNew.
		_ = s.render(w, r, "customers_form.html", Page{Title: "تعديل العميل", Error: htmlStoreErrMsg(err), Data: map[string]any{"IsNew": false,
			"Customer": &store.Customer{Name: r.FormValue("name"), Phone: r.FormValue("phone"), Address: r.FormValue("address"), IsActive: r.FormValue("is_active") != "0"},
			"CreditLimitEGP": "0.00"}})
		return
	}
	err = s.Store.UpdateCustomer(r.Context(), u, id, store.CustomerInput{
		Name: r.FormValue("name"), Phone: r.FormValue("phone"),
		Address: r.FormValue("address"), CreditLimit: limit, IsActive: r.FormValue("is_active") != "0",
	})
	if err != nil {
		_ = s.render(w, r, "customers_form.html", Page{Title: "تعديل العميل", Error: htmlStoreErrMsg(err), Data: map[string]any{"IsNew": false,
			"Customer": &store.Customer{Name: r.FormValue("name"), Phone: r.FormValue("phone"), Address: r.FormValue("address"), CreditLimit: limit, IsActive: r.FormValue("is_active") != "0"},
			"CreditLimitEGP": centsToEGP(limit)}})
		return
	}
	s.redirectWithNotice(w, r, "/customers/"+strconv.FormatInt(id, 10), "تم حفظ التعديلات")
}

func (s *Server) customerDelete(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.Store.DeleteCustomer(r.Context(), u, id); err != nil {
		s.renderCustomersListErr(w, r, htmlStoreErrMsg(err))
		return
	}
	s.redirectWithNotice(w, r, "/customers", "تم حذف العميل")
}

func (s *Server) customerPaySubmit(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	amount, err := formMoney(r, "amount")
	if err != nil {
		s.renderCustomerDetailErr(w, r, id, htmlStoreErrMsg(err))
		return
	}
	_, err = s.Store.PayCustomerDebtGeneral(r.Context(), u, id, amount, r.FormValue("method"), r.FormValue("notes"))
	if err != nil {
		s.renderCustomerDetailErr(w, r, id, htmlStoreErrMsg(err))
		return
	}
	s.redirectWithNotice(w, r, "/customers/"+strconv.FormatInt(id, 10)+"/statement", "تم تسجيل السداد")
}

func (s *Server) customerPayInvoiceSubmit(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	invID, err := strconv.ParseInt(r.FormValue("invoice_id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	custID, _ := pathID(r, "id")
	amount, err := formMoney(r, "amount")
	if err != nil {
		s.renderCustomerDetailErr(w, r, custID, htmlStoreErrMsg(err))
		return
	}
	// FIX-17 SW2-M02: the payment must land on the URL customer's own
	// invoice — PayCustomerInvoiceForCustomer refuses when the invoice
	// belongs to another customer, instead of silently paying it while
	// the cashier is told it was recorded on this customer's page.
	_, err = s.Store.PayCustomerInvoiceForCustomer(r.Context(), u, custID, invID, amount, r.FormValue("method"), r.FormValue("notes"))
	if err != nil {
		s.renderCustomerDetailErr(w, r, custID, htmlStoreErrMsg(err))
		return
	}
	s.redirectWithNotice(w, r, "/customers/"+strconv.FormatInt(custID, 10)+"/statement", "تم تسجيل سداد الفاتورة")
}

func (s *Server) customerStatement(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	st, err := s.Store.CustomerStatement(r.Context(), CurrentUser(r), id)
	if err != nil {
		s.renderCustomersListErr(w, r, htmlStoreErrMsg(err))
		return
	}
	_ = s.render(w, r, "customers_statement.html", Page{Title: "كشف حساب: " + st.Customer.Name,
		Notice: noticeFromQuery(r), Data: map[string]any{"Statement": st}})
}

func (s *Server) debtorsList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListDebtors(r.Context(), CurrentUser(r))
	if err != nil {
		LogErr(r, err)
		_ = s.render(w, r, "customers_debtors.html", Page{Title: "قائمة المدينين", Error: "تعذّر تحميل المدينين",
			Data: map[string]any{"Debtors": list}})
		return
	}
	_ = s.render(w, r, "customers_debtors.html", Page{Title: "قائمة المدينين", Data: map[string]any{"Debtors": list}})
}

// FIX-16 R16-M1: vendor money figures are visible only to roles that
// can view profit OR settle vendor debts (view_profit OR pay_vendors).
// Warehouse holds neither, so it sees no vendor money at all. The
// ledger payments-table money columns (Amount/BalanceBefore/After)
// need view_profit alone — they are a historical record that survives
// settlement, not the settlement surface. Cashier (pay_vendors, no
// view_profit) keeps the settlement surface: the pay-form dropdown /
// prefill and the CurrentBalance that IS the amount owed.
func canViewVendorMoney(role string) bool {
	return rbac.Can(role, rbac.PermViewProfit) || rbac.Can(role, rbac.PermPayVendors)
}

// --- Vendors + purchases: HTML ---

func (s *Server) vendorsList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListVendors(r.Context(), CurrentUser(r), r.URL.Query().Get("q"))
	if err != nil {
		LogErr(r, err)
		// FIX-12 SW-M04: the template derefs .Data.Alerts — render with Data.
		// FIX-15 R15-M1: pass canViewProfit (alert amounts are gated on it).
		// FIX-16 R16-M1: pass canViewVendorMoney (CurrentBalance column).
		_ = s.render(w, r, "vendors_list.html", Page{Title: "الموردون", Error: "تعذّر تحميل الموردين",
			Data: map[string]any{"Vendors": list, "Q": r.URL.Query().Get("q"), "Alerts": []*store.VendorDueAlert(nil),
				"canViewProfit": rbac.Can(CurrentUser(r).Role, rbac.PermViewProfit),
				"canViewVendorMoney": canViewVendorMoney(CurrentUser(r).Role)}})
		return
	}
	alerts, _ := s.Store.VendorDueAlerts(r.Context())
	_ = s.render(w, r, "vendors_list.html", Page{Title: "الموردون", ActiveNav: "vendors",
		Notice: noticeFromQuery(r), Data: map[string]any{"Vendors": list, "Q": r.URL.Query().Get("q"), "Alerts": alerts,
			// FIX-15 R15-M1: gate the per-invoice amounts in the due
			// alerts on view_profit, like the purchase detail page.
			// FIX-16 R16-M1: gate the CurrentBalance column on
			// canViewVendorMoney (view_profit OR pay_vendors).
			"canViewProfit": rbac.Can(CurrentUser(r).Role, rbac.PermViewProfit),
			"canViewVendorMoney": canViewVendorMoney(CurrentUser(r).Role)}})
}

func (s *Server) vendorNewForm(w http.ResponseWriter, r *http.Request) {
	_ = s.render(w, r, "vendors_form.html", Page{Title: "مورد جديد", Data: map[string]any{"IsNew": true, "CreditLimitEGP": "0.00"}})
}

func (s *Server) vendorNewSubmit(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	limit, err := formMoney(r, "credit_limit")
	if err != nil {
		_ = s.render(w, r, "vendors_form.html", Page{Title: "مورد جديد", Error: htmlStoreErrMsg(err), Data: map[string]any{"IsNew": true, "CreditLimitEGP": "0.00"}})
		return
	}
	id, err := s.Store.CreateVendor(r.Context(), u, store.VendorInput{
		Name: r.FormValue("name"), Phone: r.FormValue("phone"),
		Address: r.FormValue("address"), Notes: r.FormValue("notes"), CreditLimit: limit, IsActive: true,
	})
	if err != nil {
		_ = s.render(w, r, "vendors_form.html", Page{Title: "مورد جديد", Error: htmlStoreErrMsg(err), Data: map[string]any{"IsNew": true, "CreditLimitEGP": "0.00"}})
		return
	}
	s.redirectWithNotice(w, r, "/vendors/"+strconv.FormatInt(id, 10), "تمت إضافة المورد")
}

func (s *Server) vendorDetail(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	led, err := s.Store.VendorLedger(r.Context(), CurrentUser(r), id)
	if err != nil {
		s.renderVendorsListErr(w, r, htmlStoreErrMsg(err))
		return
	}
	// FIX-15 R15-M1: the detail template gates the per-purchase money
	// columns (Total/Paid/Remaining) on canViewProfit — same pattern as
	// purchaseDetail (FIX-13/14). The header vendor balance and the
	// pay-form dropdown/prefill stay visible to roles that may view
	// profit or settle (canViewVendorMoney) — they are the settlement
	// surface (see the template comment). Warehouse holds neither
	// permission and sees no vendor money at all (FIX-16 R16-M1).
	_ = s.render(w, r, "vendors_detail.html", Page{Title: "المورد: " + led.Vendor.Name,
		Notice: noticeFromQuery(r), Data: map[string]any{"Ledger": led, "BalanceEGP": centsToEGP(led.Vendor.CurrentBalance),
			"canViewProfit": rbac.Can(CurrentUser(r).Role, rbac.PermViewProfit),
			"canViewVendorMoney": canViewVendorMoney(CurrentUser(r).Role)}})
}

func (s *Server) vendorEditForm(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	v, err := s.Store.GetVendor(r.Context(), CurrentUser(r), id)
	if err != nil {
		s.renderVendorsListErr(w, r, htmlStoreErrMsg(err))
		return
	}
	_ = s.render(w, r, "vendors_form.html", Page{Title: "تعديل المورد", Data: map[string]any{"IsNew": false, "Vendor": v, "CreditLimitEGP": centsToEGP(v.CreditLimit)}})
}

func (s *Server) vendorEditSubmit(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	limit, err := formMoney(r, "credit_limit")
	if err != nil {
		// FIX-12 SW-M04: the form derefs .Data.Vendor.IsActive when
		// IsNew=false — supply the submitted values, not a bare IsNew.
		_ = s.render(w, r, "vendors_form.html", Page{Title: "تعديل المورد", Error: htmlStoreErrMsg(err), Data: map[string]any{"IsNew": false,
			"Vendor": &store.Vendor{Name: r.FormValue("name"), Phone: r.FormValue("phone"), Address: r.FormValue("address"), Notes: r.FormValue("notes"), IsActive: r.FormValue("is_active") != "0"},
			"CreditLimitEGP": "0.00"}})
		return
	}
	err = s.Store.UpdateVendor(r.Context(), u, id, store.VendorInput{
		Name: r.FormValue("name"), Phone: r.FormValue("phone"),
		Address: r.FormValue("address"), Notes: r.FormValue("notes"), CreditLimit: limit,
		IsActive: r.FormValue("is_active") != "0",
	})
	if err != nil {
		_ = s.render(w, r, "vendors_form.html", Page{Title: "تعديل المورد", Error: htmlStoreErrMsg(err), Data: map[string]any{"IsNew": false,
			"Vendor": &store.Vendor{Name: r.FormValue("name"), Phone: r.FormValue("phone"), Address: r.FormValue("address"), Notes: r.FormValue("notes"), CreditLimit: limit, IsActive: r.FormValue("is_active") != "0"},
			"CreditLimitEGP": centsToEGP(limit)}})
		return
	}
	s.redirectWithNotice(w, r, "/vendors/"+strconv.FormatInt(id, 10), "تم حفظ التعديلات")
}

func (s *Server) vendorDelete(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.Store.DeleteVendor(r.Context(), u, id); err != nil {
		s.renderVendorsListErr(w, r, htmlStoreErrMsg(err))
		return
	}
	s.redirectWithNotice(w, r, "/vendors", "تم حذف المورد")
}

func (s *Server) vendorLedger(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	led, err := s.Store.VendorLedger(r.Context(), CurrentUser(r), id)
	if err != nil {
		s.renderVendorsListErr(w, r, htmlStoreErrMsg(err))
		return
	}
	// FIX-15 R15-M1: same gate as vendorDetail — the ledger template
	// gates the purchases-table money columns on canViewProfit. The
	// header balance is gated on canViewVendorMoney, and the payments
	// table money columns (Amount/BalanceBefore/After) on canViewProfit
	// alone — they are a historical record that survives settlement,
	// not the settlement surface (FIX-16 R16-M1).
	_ = s.render(w, r, "vendors_ledger.html", Page{Title: "دفتر أستاذ: " + led.Vendor.Name, Data: map[string]any{"Ledger": led,
		"canViewProfit": rbac.Can(CurrentUser(r).Role, rbac.PermViewProfit),
		"canViewVendorMoney": canViewVendorMoney(CurrentUser(r).Role)}})
}

func (s *Server) vendorPaySubmit(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	amount, err := formMoney(r, "amount")
	if err != nil {
		s.renderVendorsListErr(w, r, htmlStoreErrMsg(err))
		return
	}
	var purchaseID int64
	if v := strings.TrimSpace(r.FormValue("purchase_id")); v != "" {
		purchaseID, _ = strconv.ParseInt(v, 10, 64)
	}
	_, err = s.Store.PayVendor(r.Context(), u, id, purchaseID, amount, r.FormValue("method"), r.FormValue("notes"))
	if err != nil {
		// FIX-12 SW-M04: the detail template derefs .Data.Ledger —
		// a nil ledger would 500 instead of showing the message.
		led, lerr := s.Store.VendorLedger(r.Context(), CurrentUser(r), id)
		if lerr != nil {
			s.renderVendorsListErr(w, r, htmlStoreErrMsg(err))
			return
		}
		// FIX-15 R15-M1: the detail template gates the per-purchase
		// money columns on canViewProfit — pass it on this error
		// re-render too, or the flag is absent (falsy) and an admin
		// re-render would hide totals it should show. FIX-16 R16-M1:
		// same for canViewVendorMoney (header balance + pay form).
		_ = s.render(w, r, "vendors_detail.html", Page{Title: "المورد", Error: htmlStoreErrMsg(err),
			Data: map[string]any{"Ledger": led, "BalanceEGP": centsToEGP(led.Vendor.CurrentBalance),
				"canViewProfit": rbac.Can(CurrentUser(r).Role, rbac.PermViewProfit),
				"canViewVendorMoney": canViewVendorMoney(CurrentUser(r).Role)}})
		return
	}
	s.redirectWithNotice(w, r, "/vendors/"+strconv.FormatInt(id, 10), "تم تسجيل سداد المورد")
}

func (s *Server) purchaseNewForm(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	vendors, _ := s.Store.ListVendors(r.Context(), CurrentUser(r), "")
	products, _ := s.Store.ListProducts(r.Context(), u, false)
	_ = s.render(w, r, "vendors_purchase_form.html", Page{Title: "فاتورة شراء جديدة",
		Data: map[string]any{"Vendors": vendors, "Products": products}})
}

func (s *Server) purchaseNewSubmit(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	vid, err := strconv.ParseInt(r.FormValue("vendor_id"), 10, 64)
	if err != nil {
		s.renderPurchaseFormErr(w, r, "برجاء اختيار المورد")
		return
	}
	paid, err := formMoney(r, "paid")
	if err != nil {
		s.renderPurchaseFormErr(w, r, htmlStoreErrMsg(err))
		return
	}
	_ = r.ParseForm()
	prodIDs := r.Form["product_id"]
	qtys := r.Form["qty"]
	prices := r.Form["price"]
	batches := r.Form["batch"]
	expiries := r.Form["expiry"]
	var lines []store.PurchaseLineInput
	for i := range prodIDs {
		pid, err := strconv.ParseInt(strings.TrimSpace(prodIDs[i]), 10, 64)
		if err != nil || pid == 0 {
			continue
		}
		var q money.Qty
		var pr money.Cents
		if i < len(qtys) {
			q, err = money.ParseQty(qtys[i])
			if err != nil {
				s.renderPurchaseFormErr(w, r, htmlStoreErrMsg(err))
				return
			}
		}
		if i < len(prices) {
			pr, err = money.ParseMoney(prices[i])
			if err != nil {
				s.renderPurchaseFormErr(w, r, htmlStoreErrMsg(err))
				return
			}
		}
		ln := store.PurchaseLineInput{ProductID: pid, Qty: q, PurchasePrice: pr}
		if i < len(batches) {
			ln.BatchNumber = strings.TrimSpace(batches[i])
		}
		if i < len(expiries) {
			ln.ExpiryDate = strings.TrimSpace(expiries[i])
		}
		lines = append(lines, ln)
	}
	pid, invNo, err := s.Store.CreatePurchase(r.Context(), u, store.PurchaseInput{
		VendorID: vid, Lines: lines, Paid: paid,
		PaymentMethod: r.FormValue("method"), DueDate: strings.TrimSpace(r.FormValue("due_date")),
		Notes: r.FormValue("notes"),
	})
	if err != nil {
		vendors, _ := s.Store.ListVendors(r.Context(), CurrentUser(r), "")
		products, _ := s.Store.ListProducts(r.Context(), u, false)
		_ = s.render(w, r, "vendors_purchase_form.html", Page{Title: "فاتورة شراء جديدة", Error: htmlStoreErrMsg(err),
			Data: map[string]any{"Vendors": vendors, "Products": products}})
		return
	}
	s.redirectWithNotice(w, r, "/purchases/"+strconv.FormatInt(pid, 10), "تم حفظ فاتورة الشراء "+invNo)
}

func (s *Server) purchaseDetail(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	d, err := s.Store.GetPurchase(r.Context(), id)
	if err != nil {
		s.renderVendorsListErr(w, r, htmlStoreErrMsg(err))
		return
	}
	// FIX-13 R13-M1: the buy-price column renders only for roles with
	// view_profit (admin/supervisor) — same pattern as the products
	// list/detail (FIX-12 SW-H09). Cashier/warehouse see the page
	// with no buy price.
	// FIX-14 R14-L1: the header money block (Total/Paid/Remaining) is
	// likewise hidden in the template for single-line purchases for
	// roles without view_profit — Total = Paid + Remaining exactly, so
	// the block as a whole leaks the unit price by division.
	_ = s.render(w, r, "vendors_purchase_detail.html", Page{Title: "فاتورة شراء " + d.Purchase.InvoiceNo,
		Notice: noticeFromQuery(r), Data: map[string]any{"Purchase": d,
			"canViewProfit": rbac.Can(CurrentUser(r).Role, rbac.PermViewProfit)}})
}

// --- Manual payables: HTML ---

func (s *Server) payablesList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListPayables(r.Context(), CurrentUser(r), r.URL.Query().Get("status"), r.URL.Query().Get("kind"))
	if err != nil {
		LogErr(r, err)
		// FIX-12 SW-M04: the template ranges .Data.Payables.
		_ = s.render(w, r, "payables_list.html", Page{Title: "المستحقات اليدوية", Error: "تعذّر تحميل المستحقات",
			Data: map[string]any{"Payables": list}})
		return
	}
	_ = s.render(w, r, "payables_list.html", Page{Title: "المستحقات اليدوية",
		Notice: noticeFromQuery(r), Data: map[string]any{"Payables": list}})
}

func (s *Server) payableNewForm(w http.ResponseWriter, r *http.Request) {
	vendors, _ := s.Store.ListVendors(r.Context(), CurrentUser(r), "")
	_ = s.render(w, r, "payables_form.html", Page{Title: "مستحق يدوي جديد", Data: map[string]any{"Vendors": vendors}})
}

func (s *Server) payableNewSubmit(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	amount, err := formMoney(r, "amount")
	if err != nil {
		// FIX-12 SW-M04: the form ranges .Data.Vendors.
		vendors, _ := s.Store.ListVendors(r.Context(), CurrentUser(r), "")
		_ = s.render(w, r, "payables_form.html", Page{Title: "مستحق يدوي جديد", Error: htmlStoreErrMsg(err),
			Data: map[string]any{"Vendors": vendors}})
		return
	}
	var partyID int64
	if v := strings.TrimSpace(r.FormValue("vendor_id")); v != "" {
		partyID, _ = strconv.ParseInt(v, 10, 64)
	}
	_, err = s.Store.CreatePayable(r.Context(), u, store.PayableInput{
		Kind: r.FormValue("kind"), PartyID: partyID, PartyName: r.FormValue("party_name"),
		Amount: amount, DueDate: strings.TrimSpace(r.FormValue("due_date")),
		Description: r.FormValue("description"),
	})
	if err != nil {
		vendors, _ := s.Store.ListVendors(r.Context(), CurrentUser(r), "")
		_ = s.render(w, r, "payables_form.html", Page{Title: "مستحق يدوي جديد", Error: htmlStoreErrMsg(err),
			Data: map[string]any{"Vendors": vendors}})
		return
	}
	s.redirectWithNotice(w, r, "/payables", "تمت إضافة المستحق")
}

func (s *Server) payablesAlerts(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	due, err := s.Store.DuePayables(r.Context(), u)
	if err != nil {
		LogErr(r, err)
		// FIX-12 SW-M04: the template ranges .Data.Payables.
		_ = s.render(w, r, "payables_alerts.html", Page{Title: "تنبيهات المستحقات", Error: "تعذّر تحميل التنبيهات",
			Data: map[string]any{"Payables": due}})
		return
	}
	_ = s.render(w, r, "payables_alerts.html", Page{Title: "تنبيهات المستحقات — مستحقة خلال 30 يوماً أو متأخرة",
		Data: map[string]any{"Payables": due}})
}

func (s *Server) payableSettleSubmit(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	amount, err := formMoney(r, "amount")
	if err != nil {
		s.renderPayablesListErr(w, r, htmlStoreErrMsg(err))
		return
	}
	_, err = s.Store.SettlePayable(r.Context(), u, id, amount, r.FormValue("method"), r.FormValue("notes"))
	if err != nil {
		list, _ := s.Store.ListPayables(r.Context(), CurrentUser(r), "", "")
		_ = s.render(w, r, "payables_list.html", Page{Title: "المستحقات اليدوية", Error: htmlStoreErrMsg(err),
			Data: map[string]any{"Payables": list}})
		return
	}
	s.redirectWithNotice(w, r, "/payables", "تم تسجيل السداد")
}

// payableCancelSubmit cancels a manual payable from the list page
// (FIX-12 SW-H02). Only Admin/Manager reach here (route gate +
// service layer). A refusal re-renders the list with its message.
func (s *Server) payableCancelSubmit(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.Store.CancelPayable(r.Context(), u, id, r.FormValue("reason")); err != nil {
		s.renderPayablesListErr(w, r, htmlStoreErrMsg(err))
		return
	}
	s.redirectWithNotice(w, r, "/payables", "تم إلغاء المستحق")
}

// --- Courier debts: HTML ---

func (s *Server) courierList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListCourierDebts(r.Context(), CurrentUser(r), r.URL.Query().Get("status"))
	if err != nil {
		LogErr(r, err)
		// FIX-12 SW-M04: the template ranges .Data.Debts.
		_ = s.render(w, r, "courier_list.html", Page{Title: "ديون المناديب", Error: "تعذّر تحميل الديون",
			Data: map[string]any{"Debts": list, "CanSettle": store.CanSettleCourierDebt(CurrentUser(r).Role)}})
		return
	}
	_ = s.render(w, r, "courier_list.html", Page{Title: "ديون المناديب",
		Notice: noticeFromQuery(r), Data: map[string]any{"Debts": list, "CanSettle": store.CanSettleCourierDebt(CurrentUser(r).Role)}})
}

func (s *Server) courierNewForm(w http.ResponseWriter, r *http.Request) {
	_ = s.render(w, r, "courier_form.html", Page{Title: "دين مندوب جديد", Data: map[string]any{"IsNew": true}})
}

func (s *Server) courierNewSubmit(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	amount, err := formMoney(r, "amount")
	if err != nil {
		_ = s.render(w, r, "courier_form.html", Page{Title: "دين مندوب جديد", Error: htmlStoreErrMsg(err), Data: map[string]any{"IsNew": true}})
		return
	}
	var invNo int64
	if v := strings.TrimSpace(r.FormValue("invoice_number")); v != "" {
		invNo, _ = strconv.ParseInt(v, 10, 64)
	}
	_, err = s.Store.CreateCourierDebt(r.Context(), u, store.CourierDebtInput{
		CourierName: r.FormValue("courier_name"), Amount: amount,
		OrderRef: r.FormValue("order_ref"), InvoiceNumber: invNo,
	})
	if err != nil {
		_ = s.render(w, r, "courier_form.html", Page{Title: "دين مندوب جديد", Error: htmlStoreErrMsg(err), Data: map[string]any{"IsNew": true}})
		return
	}
	s.redirectWithNotice(w, r, "/courier-debts", "تمت إضافة دين المندوب")
}

func (s *Server) courierEditForm(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	d, err := s.Store.GetCourierDebt(r.Context(), id)
	if err != nil {
		s.renderCourierListErr(w, r, htmlStoreErrMsg(err))
		return
	}
	_ = s.render(w, r, "courier_form.html", Page{Title: "تعديل دين مندوب", Data: map[string]any{"IsNew": false, "Debt": d, "AmountEGP": centsToEGP(d.Amount)}})
}

func (s *Server) courierEditSubmit(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	amount, err := formMoney(r, "amount")
	if err != nil {
		// FIX-12 SW-M04: the form derefs .Data.Debt.ID when IsNew=false.
		_ = s.render(w, r, "courier_form.html", Page{Title: "تعديل دين مندوب", Error: htmlStoreErrMsg(err), Data: map[string]any{"IsNew": false,
			"Debt": &store.CourierDebt{ID: id, CourierName: r.FormValue("courier_name"), OrderRef: r.FormValue("order_ref")},
			"AmountEGP": r.FormValue("amount")}})
		return
	}
	var invNo int64
	if v := strings.TrimSpace(r.FormValue("invoice_number")); v != "" {
		invNo, _ = strconv.ParseInt(v, 10, 64)
	}
	err = s.Store.UpdateCourierDebt(r.Context(), u, id, store.CourierDebtInput{
		CourierName: r.FormValue("courier_name"), Amount: amount,
		OrderRef: r.FormValue("order_ref"), InvoiceNumber: invNo,
	})
	if err != nil {
		_ = s.render(w, r, "courier_form.html", Page{Title: "تعديل دين مندوب", Error: htmlStoreErrMsg(err), Data: map[string]any{"IsNew": false,
			"Debt": &store.CourierDebt{ID: id, CourierName: r.FormValue("courier_name"), OrderRef: r.FormValue("order_ref")},
			"AmountEGP": centsToEGP(amount)}})
		return
	}
	s.redirectWithNotice(w, r, "/courier-debts", "تم حفظ التعديلات")
}

func (s *Server) courierSettleSubmit(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	amount, err := formMoney(r, "amount")
	if err != nil {
		s.renderCourierListErr(w, r, htmlStoreErrMsg(err))
		return
	}
	_, err = s.Store.SettleCourierDebt(r.Context(), u, id, amount, r.FormValue("method"), r.FormValue("notes"))
	if err != nil {
		list, _ := s.Store.ListCourierDebts(r.Context(), CurrentUser(r), "")
		_ = s.render(w, r, "courier_list.html", Page{Title: "ديون المناديب", Error: htmlStoreErrMsg(err),
			Data: map[string]any{"Debts": list, "CanSettle": store.CanSettleCourierDebt(CurrentUser(r).Role)}})
		return
	}
	s.redirectWithNotice(w, r, "/courier-debts", "تم تسجيل سداد دين المندوب")
}

func (s *Server) courierDelete(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.Store.DeleteCourierDebt(r.Context(), u, id); err != nil {
		list, _ := s.Store.ListCourierDebts(r.Context(), CurrentUser(r), "")
		_ = s.render(w, r, "courier_list.html", Page{Title: "ديون المناديب", Error: htmlStoreErrMsg(err),
			Data: map[string]any{"Debts": list, "CanSettle": store.CanSettleCourierDebt(CurrentUser(r).Role)}})
		return
	}
	s.redirectWithNotice(w, r, "/courier-debts", "تم حذف دين المندوب")
}

// --- Electronic transfers: HTML ---

func (s *Server) transfersList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListTransfers(r.Context(), CurrentUser(r), r.URL.Query().Get("status"))
	if err != nil {
		LogErr(r, err)
		// FIX-12 SW-M04: the template ranges .Data.Transfers.
		_ = s.render(w, r, "transfers_list.html", Page{Title: "التحويلات الإلكترونية", Error: "تعذّر تحميل التحويلات",
			Data: map[string]any{"Transfers": list}})
		return
	}
	_ = s.render(w, r, "transfers_list.html", Page{Title: "التحويلات الإلكترونية",
		Notice: noticeFromQuery(r), Data: map[string]any{"Transfers": list}})
}

func (s *Server) transferNewForm(w http.ResponseWriter, r *http.Request) {
	_ = s.render(w, r, "transfers_form.html", Page{Title: "تسجيل تحويل إلكتروني"})
}

func (s *Server) transferNewSubmit(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	amount, err := formMoney(r, "amount")
	if err != nil {
		_ = s.render(w, r, "transfers_form.html", Page{Title: "تسجيل تحويل إلكتروني", Error: htmlStoreErrMsg(err)})
		return
	}
	_, err = s.Store.CreateTransfer(r.Context(), u, store.TransferInput{
		SenderName: r.FormValue("sender_name"), Amount: amount,
		Method: r.FormValue("method"), Reference: r.FormValue("reference"),
	})
	if err != nil {
		_ = s.render(w, r, "transfers_form.html", Page{Title: "تسجيل تحويل إلكتروني", Error: htmlStoreErrMsg(err)})
		return
	}
	s.redirectWithNotice(w, r, "/transfers", "تم تسجيل التحويل (قيد المراجعة)")
}

func (s *Server) transferApproveSubmit(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.Store.ApproveTransfer(r.Context(), u, id, r.FormValue("note")); err != nil {
		list, _ := s.Store.ListTransfers(r.Context(), CurrentUser(r), "")
		_ = s.render(w, r, "transfers_list.html", Page{Title: "التحويلات الإلكترونية", Error: htmlStoreErrMsg(err),
			Data: map[string]any{"Transfers": list}})
		return
	}
	s.redirectWithNotice(w, r, "/transfers", "تم اعتماد التحويل")
}

func (s *Server) transferRejectSubmit(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.Store.RejectTransfer(r.Context(), u, id, r.FormValue("note")); err != nil {
		list, _ := s.Store.ListTransfers(r.Context(), CurrentUser(r), "")
		_ = s.render(w, r, "transfers_list.html", Page{Title: "التحويلات الإلكترونية", Error: htmlStoreErrMsg(err),
			Data: map[string]any{"Transfers": list}})
		return
	}
	s.redirectWithNotice(w, r, "/transfers", "تم رفض التحويل")
}

// --- JSON API ---

func (s *Server) apiCustomersList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListCustomers(r.Context(), CurrentUser(r), r.URL.Query().Get("q"))
	if err != nil {
		// FIX-12 SW-M12: a service-layer permission denial is a 403,
		// not a 500 — the denial works, only the status was wrong.
		if isDenial(err) {
			apiErr(w, http.StatusForbidden, err)
			return
		}
		apiErrInternal(w, r, err)
		return
	}
	apiOK(w, map[string]any{"customers": list})
}

func (s *Server) apiCustomerCreate(w http.ResponseWriter, r *http.Request) {
	body, err := readJSONBody(r)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	// FIX-17 SW2-M03: a JSON-number credit_limit was silently dropped
	// to 0 (unlimited) by strVal — parse it with amountFromValue
	// (FIX-18 R18-B-01: number = cents, string = EGP decimal).
	var limit money.Cents
	if v, ok := body["credit_limit"]; ok {
		var lerr error
		limit, _, lerr = amountFromValue(v)
		if lerr != nil {
			apiErr(w, http.StatusBadRequest, lerr)
			return
		}
	}
	id, err := s.Store.CreateCustomer(r.Context(), CurrentUser(r), store.CustomerInput{
		Name: strVal(body, "name"), Phone: strVal(body, "phone"),
		Address: strVal(body, "address"), CreditLimit: limit, IsActive: true,
	})
	if err != nil {
		apiErrForStore(w, r, err, http.StatusNotFound)
		return
	}
	apiOK(w, map[string]any{"customer_id": id})
}

func (s *Server) apiCustomerGet(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		apiErr(w, http.StatusBadRequest, errString("معرّف غير صحيح"))
		return
	}
	c, err := s.Store.GetCustomer(r.Context(), CurrentUser(r), id)
	if err != nil {
		// FIX-17 SW2-L08: a service-layer permission denial is a 403
		// with the uniform text (SW-M12 pattern, as on the list
		// endpoints) — not a 404. Only a genuinely missing customer
		// stays 404.
		if isDenial(err) {
			apiErr(w, http.StatusForbidden, err)
			return
		}
		// FIX-24 R24-E-N1: the live-balance SUM can overflow on absurd
		// stored rows (LiveCustomerBalanceDB now maps it to
		// money.ErrOverflow) — that is a 400 with the Arabic message,
		// not a 404, and never the raw driver string. Any other
		// error (e.g. a DB failure) is a 500 with a generic message.
		if errors.Is(err, money.ErrOverflow) {
			apiErr(w, http.StatusBadRequest, err)
			return
		}
		if err.Error() == "العميل غير موجود" {
			apiErr(w, http.StatusNotFound, err)
			return
		}
		apiErr(w, http.StatusInternalServerError, errString("تعذّر تحميل العميل"))
		return
	}
	apiOK(w, map[string]any{"customer": c})
}

func (s *Server) apiCustomerPay(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		apiErr(w, http.StatusBadRequest, errString("معرّف غير صحيح"))
		return
	}
	body, err := readJSONBody(r)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	amount, err := apiAmount(body)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	newBal, err := s.Store.PayCustomerDebtGeneral(r.Context(), CurrentUser(r), id, amount, strVal(body, "method"), strVal(body, "notes"))
	if err != nil {
		apiErrForStore(w, r, err, http.StatusNotFound)
		return
	}
	apiOK(w, map[string]any{"new_balance_cents": int64(newBal)})
}

func (s *Server) apiCustomerPayInvoice(w http.ResponseWriter, r *http.Request) {
	// FIX-17 SW2-M02: the route carries the customer id
	// (/api/customers/{id}/pay-invoice) and it must be honoured — the
	// old handler never read it, so any invoice could be paid from
	// any customer's URL. (The leg's "takes no customer id at all"
	// describes the handler, not the route: the {id} segment was
	// always there, just ignored.)
	custID, err := pathID(r, "id")
	if err != nil {
		apiErr(w, http.StatusBadRequest, errString("معرّف غير صحيح"))
		return
	}
	body, err := readJSONBody(r)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	amount, err := apiAmount(body)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	invID := intVal(body, "invoice_id")
	if invID == 0 {
		apiErr(w, http.StatusBadRequest, errString("رقم الفاتورة مطلوب"))
		return
	}
	rem, err := s.Store.PayCustomerInvoiceForCustomer(r.Context(), CurrentUser(r), custID, invID, amount, strVal(body, "method"), strVal(body, "notes"))
	if err != nil {
		apiErrForStore(w, r, err, http.StatusNotFound)
		return
	}
	apiOK(w, map[string]any{"new_remaining_cents": int64(rem)})
}

func (s *Server) apiDebtorsList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListDebtors(r.Context(), CurrentUser(r))
	if err != nil {
		// FIX-12 SW-M12: see apiCustomersList.
		if isDenial(err) {
			apiErr(w, http.StatusForbidden, err)
			return
		}
		apiErrInternal(w, r, err)
		return
	}
	apiOK(w, map[string]any{"debtors": list})
}

func (s *Server) apiVendorsList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListVendors(r.Context(), CurrentUser(r), r.URL.Query().Get("q"))
	if err != nil {
		apiErrInternal(w, r, err)
		return
	}
	// FIX-16 R16-M1: the full Vendor struct carries CurrentBalance —
	// for a single-purchase vendor that IS the invoice total, so it
	// leaks the unit price by division with the Qty on the purchase
	// detail page. Roles that can neither view profit nor settle
	// (warehouse) get the balance zeroed. No shipped client reads
	// /api/vendors (no JS or template reference); the pages that need
	// the balance use the HTML renders, which gate it in the template.
	if !canViewVendorMoney(CurrentUser(r).Role) {
		stripped := make([]*store.Vendor, len(list))
		for i, v := range list {
			c := *v
			c.CurrentBalance = 0
			stripped[i] = &c
		}
		list = stripped
	}
	apiOK(w, map[string]any{"vendors": list})
}

func (s *Server) apiVendorCreate(w http.ResponseWriter, r *http.Request) {
	body, err := readJSONBody(r)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	// FIX-17 SW2-M03: same numeric-credit_limit hole as
	// apiCustomerCreate — a JSON number was silently stored as 0.
	var limit money.Cents
	if v, ok := body["credit_limit"]; ok {
		var lerr error
		limit, _, lerr = amountFromValue(v)
		if lerr != nil {
			apiErr(w, http.StatusBadRequest, lerr)
			return
		}
	}
	id, err := s.Store.CreateVendor(r.Context(), CurrentUser(r), store.VendorInput{
		Name: strVal(body, "name"), Phone: strVal(body, "phone"),
		Address: strVal(body, "address"), Notes: strVal(body, "notes"), CreditLimit: limit, IsActive: true,
	})
	if err != nil {
		apiErrForStore(w, r, err, http.StatusNotFound)
		return
	}
	apiOK(w, map[string]any{"vendor_id": id})
}

func (s *Server) apiVendorPay(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		apiErr(w, http.StatusBadRequest, errString("معرّف غير صحيح"))
		return
	}
	body, err := readJSONBody(r)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	amount, err := apiAmount(body)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	after, err := s.Store.PayVendor(r.Context(), CurrentUser(r), id, intVal(body, "purchase_id"), amount, strVal(body, "method"), strVal(body, "notes"))
	if err != nil {
		apiErrForStore(w, r, err, http.StatusNotFound)
		return
	}
	apiOK(w, map[string]any{"balance_after_cents": int64(after)})
}

func (s *Server) apiPurchaseCreate(w http.ResponseWriter, r *http.Request) {
	body, err := readJSONBody(r)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	// FIX-17 SW2-M03: a numeric "paid" was silently dropped to 0 by
	// strVal (same hole as credit_limit). Parse it with
	// amountFromValue (FIX-18 R18-B-01: number = cents); the
	// paid_cents fallback is kept for callers that send cents, and
	// still applies when "paid" is absent/empty.
	var paid money.Cents
	if v, ok := body["paid"]; ok {
		p, present, perr := amountFromValue(v)
		if perr != nil {
			apiErr(w, http.StatusBadRequest, perr)
			return
		}
		paid = p
		if !present {
			if v, ok := body["paid_cents"]; ok {
				if f, ok := v.(float64); ok {
					// R19-B-F3: checked conversion.
					pf, perr := money.CentsFromFloat(f)
					if perr != nil {
						apiErr(w, http.StatusBadRequest, perr)
						return
					}
					paid = pf
				}
			}
		}
	} else if v, ok := body["paid_cents"]; ok {
		if f, ok := v.(float64); ok {
			// R19-B-F3: checked conversion.
			pf, perr := money.CentsFromFloat(f)
			if perr != nil {
				apiErr(w, http.StatusBadRequest, perr)
				return
			}
			paid = pf
		}
	}
	var lines []store.PurchaseLineInput
	if raw, ok := body["lines"].([]any); ok {
		for _, item := range raw {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			// FIX-17 SW2-M03: numeric qty/price in a line were refused
			// outright (strVal dropped them to "" and ParseQty /
			// ParseMoney errored) — accept JSON numbers the same way.
			qty, err := money.ParseQty(strFromValue(m["qty"]))
			if err != nil {
				apiErr(w, http.StatusBadRequest, err)
				return
			}
			price, present, err := amountFromValue(m["price"])
			if err != nil {
				apiErr(w, http.StatusBadRequest, err)
				return
			}
			if !present {
				apiErr(w, http.StatusBadRequest, errMissingAmount)
				return
			}
			lines = append(lines, store.PurchaseLineInput{
				ProductID: intVal(m, "product_id"), Qty: qty, PurchasePrice: price,
				BatchNumber: strVal(m, "batch"), ExpiryDate: strVal(m, "expiry"),
			})
		}
	}
	pid, invNo, err := s.Store.CreatePurchase(r.Context(), CurrentUser(r), store.PurchaseInput{
		VendorID: intVal(body, "vendor_id"), Lines: lines, Paid: paid,
		PaymentMethod: strVal(body, "method"), DueDate: strVal(body, "due_date"), Notes: strVal(body, "notes"),
	})
	if err != nil {
		apiErrForStore(w, r, err, http.StatusNotFound)
		return
	}
	apiOK(w, map[string]any{"purchase_id": pid, "invoice_number": invNo})
}

func (s *Server) apiPurchaseGet(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		apiErr(w, http.StatusBadRequest, errString("معرّف غير صحيح"))
		return
	}
	d, err := s.Store.GetPurchase(r.Context(), id)
	if err != nil {
		apiErrForStore(w, r, err, http.StatusNotFound)
		return
	}
	// FIX-13 (coordinator, R13-M1 extension): the purchase API returns
	// the same buy prices the detail page hides — strip the per-line
	// buy price (and the line total, qty × price, which leaks the unit
	// price by division) for roles without view_profit, so the M1 rule
	// (cashier/warehouse never see buy prices) holds on every surface.
	// No shipped client calls this endpoint.
	// FIX-14 R14-L1: on a single-line purchase the header totals leak
	// the unit price the same way — Total = Paid + Remaining exactly
	// (creation sets remaining = total - paid; every payment moves
	// both together), so hiding Total alone leaves
	// (Paid+Remaining)/Qty, and with Paid=0 Remaining IS the total.
	// Zero the whole header money block for single-line purchases.
	// Multi-line header totals stay visible, as on the HTML page
	// (payables settlement; derivation ambiguous).
	if !rbac.Can(CurrentUser(r).Role, rbac.PermViewProfit) {
		for _, ln := range d.Lines {
			ln.PurchasePrice = 0
			ln.Total = 0
		}
		if len(d.Lines) == 1 {
			d.Purchase.Total = 0
			d.Purchase.Paid = 0
			d.Purchase.Remaining = 0
		}
	}
	apiOK(w, map[string]any{"purchase": d})
}

func (s *Server) apiPayablesList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListPayables(r.Context(), CurrentUser(r), r.URL.Query().Get("status"), r.URL.Query().Get("kind"))
	if err != nil {
		apiErrInternal(w, r, err)
		return
	}
	apiOK(w, map[string]any{"payables": list})
}

func (s *Server) apiPayableCreate(w http.ResponseWriter, r *http.Request) {
	body, err := readJSONBody(r)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	amount, err := apiAmount(body)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	id, err := s.Store.CreatePayable(r.Context(), CurrentUser(r), store.PayableInput{
		Kind: strVal(body, "kind"), PartyID: intVal(body, "party_id"), PartyName: strVal(body, "party_name"),
		Amount: amount, DueDate: strVal(body, "due_date"), Description: strVal(body, "description"),
	})
	if err != nil {
		apiErrForStore(w, r, err, http.StatusNotFound)
		return
	}
	apiOK(w, map[string]any{"payable_id": id})
}

func (s *Server) apiPayablesDue(w http.ResponseWriter, r *http.Request) {
	due, err := s.Store.DuePayables(r.Context(), CurrentUser(r))
	if err != nil {
		apiErrInternal(w, r, err)
		return
	}
	apiOK(w, map[string]any{"due": due})
}

func (s *Server) apiPayableSettle(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		apiErr(w, http.StatusBadRequest, errString("معرّف غير صحيح"))
		return
	}
	body, err := readJSONBody(r)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	amount, err := apiAmount(body)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	rem, err := s.Store.SettlePayable(r.Context(), CurrentUser(r), id, amount, strVal(body, "method"), strVal(body, "notes"))
	if err != nil {
		apiErrForStore(w, r, err, http.StatusNotFound)
		return
	}
	apiOK(w, map[string]any{"remaining_cents": int64(rem)})
}

// apiPayableCancel cancels a manual payable (FIX-12 SW-H02).
func (s *Server) apiPayableCancel(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		apiErr(w, http.StatusBadRequest, errString("معرّف غير صحيح"))
		return
	}
	body, _ := readJSONBody(r)
	reason := ""
	if body != nil {
		reason = strVal(body, "reason")
	}
	if err := s.Store.CancelPayable(r.Context(), CurrentUser(r), id, reason); err != nil {
		apiErrForStore(w, r, err, http.StatusNotFound)
		return
	}
	apiOK(w, map[string]any{"payable_id": id, "status": "cancelled"})
}

func (s *Server) apiCourierList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListCourierDebts(r.Context(), CurrentUser(r), r.URL.Query().Get("status"))
	if err != nil {
		apiErrInternal(w, r, err)
		return
	}
	apiOK(w, map[string]any{"debts": list})
}

func (s *Server) apiCourierCreate(w http.ResponseWriter, r *http.Request) {
	body, err := readJSONBody(r)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	amount, err := apiAmount(body)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	id, err := s.Store.CreateCourierDebt(r.Context(), CurrentUser(r), store.CourierDebtInput{
		CourierName: strVal(body, "courier_name"), Amount: amount,
		OrderRef: strVal(body, "order_ref"), InvoiceNumber: intVal(body, "invoice_number"),
	})
	if err != nil {
		apiErrForStore(w, r, err, http.StatusNotFound)
		return
	}
	apiOK(w, map[string]any{"debt_id": id})
}

func (s *Server) apiCourierSettle(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		apiErr(w, http.StatusBadRequest, errString("معرّف غير صحيح"))
		return
	}
	body, err := readJSONBody(r)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	amount, err := apiAmount(body)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	rem, err := s.Store.SettleCourierDebt(r.Context(), CurrentUser(r), id, amount, strVal(body, "method"), strVal(body, "notes"))
	if err != nil {
		apiErrForStore(w, r, err, http.StatusNotFound)
		return
	}
	apiOK(w, map[string]any{"remaining_cents": int64(rem)})
}

func (s *Server) apiTransfersList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListTransfers(r.Context(), CurrentUser(r), r.URL.Query().Get("status"))
	if err != nil {
		apiErrInternal(w, r, err)
		return
	}
	apiOK(w, map[string]any{"transfers": list})
}

func (s *Server) apiTransferCreate(w http.ResponseWriter, r *http.Request) {
	body, err := readJSONBody(r)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	amount, err := apiAmount(body)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	id, err := s.Store.CreateTransfer(r.Context(), CurrentUser(r), store.TransferInput{
		SenderName: strVal(body, "sender_name"), Amount: amount,
		Method: strVal(body, "method"), Reference: strVal(body, "reference"),
	})
	if err != nil {
		apiErrForStore(w, r, err, http.StatusNotFound)
		return
	}
	apiOK(w, map[string]any{"transfer_id": id})
}

func (s *Server) apiTransferApprove(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		apiErr(w, http.StatusBadRequest, errString("معرّف غير صحيح"))
		return
	}
	body, _ := readJSONBody(r)
	note := ""
	if body != nil {
		note = strVal(body, "note")
	}
	if err := s.Store.ApproveTransfer(r.Context(), CurrentUser(r), id, note); err != nil {
		apiErrForStore(w, r, err, http.StatusNotFound)
		return
	}
	apiOK(w, map[string]any{"transfer_id": id, "status": "approved"})
}

func (s *Server) apiTransferReject(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		apiErr(w, http.StatusBadRequest, errString("معرّف غير صحيح"))
		return
	}
	body, _ := readJSONBody(r)
	note := ""
	if body != nil {
		note = strVal(body, "note")
	}
	if err := s.Store.RejectTransfer(r.Context(), CurrentUser(r), id, note); err != nil {
		apiErrForStore(w, r, err, http.StatusNotFound)
		return
	}
	apiOK(w, map[string]any{"transfer_id": id, "status": "rejected"})
}
