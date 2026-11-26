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
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"cashierpos-go/internal/money"
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
	m.HandleFunc("GET /payables", s.RequireLogin(s.payablesList))
	m.HandleFunc("GET /payables/new", s.RequirePerm("manage_payables", s.payableNewForm))
	m.HandleFunc("POST /payables/new", s.RequirePerm("manage_payables", s.payableNewSubmit))
	m.HandleFunc("GET /payables/alerts", s.RequireLogin(s.payablesAlerts))
	m.HandleFunc("POST /payables/{id}/settle", s.RequirePerm("settle_payable", s.payableSettleSubmit))
	// --- Courier debts (HTML) ---
	m.HandleFunc("GET /courier-debts", s.RequirePerm("view_courier_debts", s.courierList))
	m.HandleFunc("GET /courier-debts/new", s.RequirePerm("edit_courier_debts", s.courierNewForm))
	m.HandleFunc("POST /courier-debts/new", s.RequirePerm("edit_courier_debts", s.courierNewSubmit))
	m.HandleFunc("GET /courier-debts/{id}/edit", s.RequirePerm("edit_courier_debts", s.courierEditForm))
	m.HandleFunc("POST /courier-debts/{id}/edit", s.RequirePerm("edit_courier_debts", s.courierEditSubmit))
	m.HandleFunc("POST /courier-debts/{id}/settle", s.RequirePerm("edit_courier_debts", s.courierSettleSubmit))
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
	m.HandleFunc("GET /api/payables", s.RequireLogin(s.apiPayablesList))
	m.HandleFunc("POST /api/payables", s.RequirePerm("manage_payables", s.apiPayableCreate))
	m.HandleFunc("GET /api/payables/due", s.RequireLogin(s.apiPayablesDue))
	m.HandleFunc("POST /api/payables/{id}/settle", s.RequirePerm("settle_payable", s.apiPayableSettle))
	m.HandleFunc("GET /api/courier-debts", s.RequirePerm("view_courier_debts", s.apiCourierList))
	m.HandleFunc("POST /api/courier-debts", s.RequirePerm("edit_courier_debts", s.apiCourierCreate))
	m.HandleFunc("POST /api/courier-debts/{id}/settle", s.RequirePerm("edit_courier_debts", s.apiCourierSettle))
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
			return money.Cents(int64(n)), nil
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

func apiOK(w http.ResponseWriter, data map[string]any) {
	data["ok"] = true
	WriteJSON(w, http.StatusOK, data)
}

// --- Customers: HTML ---

func (s *Server) customersList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListCustomers(r.Context(), CurrentUser(r), r.URL.Query().Get("q"))
	if err != nil {
		LogErr(r, err)
		_ = s.render(w, r, "customers_list.html", Page{Title: "العملاء", Error: "تعذّر تحميل العملاء"})
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
		_ = s.render(w, r, "customers_form.html", Page{Title: "عميل جديد", Error: err.Error(), Data: map[string]any{"IsNew": true}})
		return
	}
	id, err := s.Store.CreateCustomer(r.Context(), u, store.CustomerInput{
		Name: r.FormValue("name"), Phone: r.FormValue("phone"),
		Address: r.FormValue("address"), CreditLimit: limit, IsActive: true,
	})
	if err != nil {
		_ = s.render(w, r, "customers_form.html", Page{Title: "عميل جديد", Error: err.Error(), Data: map[string]any{"IsNew": true}})
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
		_ = s.render(w, r, "customers_list.html", Page{Title: "العملاء", Error: err.Error()})
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
		_ = s.render(w, r, "customers_list.html", Page{Title: "العملاء", Error: err.Error()})
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
		_ = s.render(w, r, "customers_form.html", Page{Title: "تعديل العميل", Error: err.Error(), Data: map[string]any{"IsNew": false}})
		return
	}
	err = s.Store.UpdateCustomer(r.Context(), u, id, store.CustomerInput{
		Name: r.FormValue("name"), Phone: r.FormValue("phone"),
		Address: r.FormValue("address"), CreditLimit: limit, IsActive: r.FormValue("is_active") != "0",
	})
	if err != nil {
		_ = s.render(w, r, "customers_form.html", Page{Title: "تعديل العميل", Error: err.Error(), Data: map[string]any{"IsNew": false}})
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
		_ = s.render(w, r, "customers_list.html", Page{Title: "العملاء", Error: err.Error()})
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
		_ = s.render(w, r, "customers_detail.html", Page{Title: "العميل", Error: err.Error()})
		return
	}
	_, err = s.Store.PayCustomerDebtGeneral(r.Context(), u, id, amount, r.FormValue("method"), r.FormValue("notes"))
	if err != nil {
		c, _ := s.Store.GetCustomer(r.Context(), CurrentUser(r), id)
		_ = s.render(w, r, "customers_detail.html", Page{Title: "العميل", Error: err.Error(),
			Data: map[string]any{"Customer": c}})
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
	amount, err := formMoney(r, "amount")
	if err != nil {
		_ = s.render(w, r, "customers_detail.html", Page{Title: "العميل", Error: err.Error()})
		return
	}
	custID, _ := pathID(r, "id")
	_, err = s.Store.PayCustomerInvoice(r.Context(), u, invID, amount, r.FormValue("method"), r.FormValue("notes"))
	if err != nil {
		c, _ := s.Store.GetCustomer(r.Context(), CurrentUser(r), custID)
		_ = s.render(w, r, "customers_detail.html", Page{Title: "العميل", Error: err.Error(),
			Data: map[string]any{"Customer": c}})
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
		_ = s.render(w, r, "customers_list.html", Page{Title: "العملاء", Error: err.Error()})
		return
	}
	_ = s.render(w, r, "customers_statement.html", Page{Title: "كشف حساب: " + st.Customer.Name,
		Notice: noticeFromQuery(r), Data: map[string]any{"Statement": st}})
}

func (s *Server) debtorsList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListDebtors(r.Context(), CurrentUser(r))
	if err != nil {
		LogErr(r, err)
		_ = s.render(w, r, "customers_debtors.html", Page{Title: "قائمة المدينين", Error: "تعذّر تحميل المدينين"})
		return
	}
	_ = s.render(w, r, "customers_debtors.html", Page{Title: "قائمة المدينين", Data: map[string]any{"Debtors": list}})
}

// --- Vendors + purchases: HTML ---

func (s *Server) vendorsList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListVendors(r.Context(), CurrentUser(r), r.URL.Query().Get("q"))
	if err != nil {
		LogErr(r, err)
		_ = s.render(w, r, "vendors_list.html", Page{Title: "الموردون", Error: "تعذّر تحميل الموردين"})
		return
	}
	alerts, _ := s.Store.VendorDueAlerts(r.Context())
	_ = s.render(w, r, "vendors_list.html", Page{Title: "الموردون", ActiveNav: "vendors",
		Notice: noticeFromQuery(r), Data: map[string]any{"Vendors": list, "Q": r.URL.Query().Get("q"), "Alerts": alerts}})
}

func (s *Server) vendorNewForm(w http.ResponseWriter, r *http.Request) {
	_ = s.render(w, r, "vendors_form.html", Page{Title: "مورد جديد", Data: map[string]any{"IsNew": true, "CreditLimitEGP": "0.00"}})
}

func (s *Server) vendorNewSubmit(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	limit, err := formMoney(r, "credit_limit")
	if err != nil {
		_ = s.render(w, r, "vendors_form.html", Page{Title: "مورد جديد", Error: err.Error(), Data: map[string]any{"IsNew": true}})
		return
	}
	id, err := s.Store.CreateVendor(r.Context(), u, store.VendorInput{
		Name: r.FormValue("name"), Phone: r.FormValue("phone"),
		Address: r.FormValue("address"), Notes: r.FormValue("notes"), CreditLimit: limit, IsActive: true,
	})
	if err != nil {
		_ = s.render(w, r, "vendors_form.html", Page{Title: "مورد جديد", Error: err.Error(), Data: map[string]any{"IsNew": true}})
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
		_ = s.render(w, r, "vendors_list.html", Page{Title: "الموردون", Error: err.Error()})
		return
	}
	_ = s.render(w, r, "vendors_detail.html", Page{Title: "المورد: " + led.Vendor.Name,
		Notice: noticeFromQuery(r), Data: map[string]any{"Ledger": led, "BalanceEGP": centsToEGP(led.Vendor.CurrentBalance)}})
}

func (s *Server) vendorEditForm(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	v, err := s.Store.GetVendor(r.Context(), CurrentUser(r), id)
	if err != nil {
		_ = s.render(w, r, "vendors_list.html", Page{Title: "الموردون", Error: err.Error()})
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
		_ = s.render(w, r, "vendors_form.html", Page{Title: "تعديل المورد", Error: err.Error(), Data: map[string]any{"IsNew": false}})
		return
	}
	err = s.Store.UpdateVendor(r.Context(), u, id, store.VendorInput{
		Name: r.FormValue("name"), Phone: r.FormValue("phone"),
		Address: r.FormValue("address"), Notes: r.FormValue("notes"), CreditLimit: limit,
		IsActive: r.FormValue("is_active") != "0",
	})
	if err != nil {
		_ = s.render(w, r, "vendors_form.html", Page{Title: "تعديل المورد", Error: err.Error(), Data: map[string]any{"IsNew": false}})
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
		_ = s.render(w, r, "vendors_list.html", Page{Title: "الموردون", Error: err.Error()})
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
		_ = s.render(w, r, "vendors_list.html", Page{Title: "الموردون", Error: err.Error()})
		return
	}
	_ = s.render(w, r, "vendors_ledger.html", Page{Title: "دفتر أستاذ: " + led.Vendor.Name, Data: map[string]any{"Ledger": led}})
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
		_ = s.render(w, r, "vendors_list.html", Page{Title: "الموردون", Error: err.Error()})
		return
	}
	var purchaseID int64
	if v := strings.TrimSpace(r.FormValue("purchase_id")); v != "" {
		purchaseID, _ = strconv.ParseInt(v, 10, 64)
	}
	_, err = s.Store.PayVendor(r.Context(), u, id, purchaseID, amount, r.FormValue("method"), r.FormValue("notes"))
	if err != nil {
		led, _ := s.Store.VendorLedger(r.Context(), CurrentUser(r), id)
		_ = s.render(w, r, "vendors_detail.html", Page{Title: "المورد", Error: err.Error(),
			Data: map[string]any{"Ledger": led}})
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
		_ = s.render(w, r, "vendors_purchase_form.html", Page{Title: "فاتورة شراء جديدة", Error: "برجاء اختيار المورد"})
		return
	}
	paid, err := formMoney(r, "paid")
	if err != nil {
		_ = s.render(w, r, "vendors_purchase_form.html", Page{Title: "فاتورة شراء جديدة", Error: err.Error()})
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
				_ = s.render(w, r, "vendors_purchase_form.html", Page{Title: "فاتورة شراء جديدة", Error: err.Error()})
				return
			}
		}
		if i < len(prices) {
			pr, err = money.ParseMoney(prices[i])
			if err != nil {
				_ = s.render(w, r, "vendors_purchase_form.html", Page{Title: "فاتورة شراء جديدة", Error: err.Error()})
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
		_ = s.render(w, r, "vendors_purchase_form.html", Page{Title: "فاتورة شراء جديدة", Error: err.Error(),
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
		_ = s.render(w, r, "vendors_list.html", Page{Title: "الموردون", Error: err.Error()})
		return
	}
	_ = s.render(w, r, "vendors_purchase_detail.html", Page{Title: "فاتورة شراء " + d.Purchase.InvoiceNo,
		Notice: noticeFromQuery(r), Data: map[string]any{"Purchase": d}})
}

// --- Manual payables: HTML ---

func (s *Server) payablesList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListPayables(r.Context(), CurrentUser(r), r.URL.Query().Get("status"), r.URL.Query().Get("kind"))
	if err != nil {
		LogErr(r, err)
		_ = s.render(w, r, "payables_list.html", Page{Title: "المستحقات اليدوية", Error: "تعذّر تحميل المستحقات"})
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
		_ = s.render(w, r, "payables_form.html", Page{Title: "مستحق يدوي جديد", Error: err.Error()})
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
		_ = s.render(w, r, "payables_form.html", Page{Title: "مستحق يدوي جديد", Error: err.Error(),
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
		_ = s.render(w, r, "payables_alerts.html", Page{Title: "تنبيهات المستحقات", Error: "تعذّر تحميل التنبيهات"})
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
		_ = s.render(w, r, "payables_list.html", Page{Title: "المستحقات اليدوية", Error: err.Error()})
		return
	}
	_, err = s.Store.SettlePayable(r.Context(), u, id, amount, r.FormValue("method"), r.FormValue("notes"))
	if err != nil {
		list, _ := s.Store.ListPayables(r.Context(), CurrentUser(r), "", "")
		_ = s.render(w, r, "payables_list.html", Page{Title: "المستحقات اليدوية", Error: err.Error(),
			Data: map[string]any{"Payables": list}})
		return
	}
	s.redirectWithNotice(w, r, "/payables", "تم تسجيل السداد")
}

// --- Courier debts: HTML ---

func (s *Server) courierList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListCourierDebts(r.Context(), CurrentUser(r), r.URL.Query().Get("status"))
	if err != nil {
		LogErr(r, err)
		_ = s.render(w, r, "courier_list.html", Page{Title: "ديون المناديب", Error: "تعذّر تحميل الديون"})
		return
	}
	_ = s.render(w, r, "courier_list.html", Page{Title: "ديون المناديب",
		Notice: noticeFromQuery(r), Data: map[string]any{"Debts": list}})
}

func (s *Server) courierNewForm(w http.ResponseWriter, r *http.Request) {
	_ = s.render(w, r, "courier_form.html", Page{Title: "دين مندوب جديد", Data: map[string]any{"IsNew": true}})
}

func (s *Server) courierNewSubmit(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	amount, err := formMoney(r, "amount")
	if err != nil {
		_ = s.render(w, r, "courier_form.html", Page{Title: "دين مندوب جديد", Error: err.Error(), Data: map[string]any{"IsNew": true}})
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
		_ = s.render(w, r, "courier_form.html", Page{Title: "دين مندوب جديد", Error: err.Error(), Data: map[string]any{"IsNew": true}})
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
		_ = s.render(w, r, "courier_list.html", Page{Title: "ديون المناديب", Error: err.Error()})
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
		_ = s.render(w, r, "courier_form.html", Page{Title: "تعديل دين مندوب", Error: err.Error(), Data: map[string]any{"IsNew": false}})
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
		_ = s.render(w, r, "courier_form.html", Page{Title: "تعديل دين مندوب", Error: err.Error(), Data: map[string]any{"IsNew": false}})
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
		_ = s.render(w, r, "courier_list.html", Page{Title: "ديون المناديب", Error: err.Error()})
		return
	}
	_, err = s.Store.SettleCourierDebt(r.Context(), u, id, amount, r.FormValue("method"), r.FormValue("notes"))
	if err != nil {
		list, _ := s.Store.ListCourierDebts(r.Context(), CurrentUser(r), "")
		_ = s.render(w, r, "courier_list.html", Page{Title: "ديون المناديب", Error: err.Error(),
			Data: map[string]any{"Debts": list}})
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
		_ = s.render(w, r, "courier_list.html", Page{Title: "ديون المناديب", Error: err.Error(),
			Data: map[string]any{"Debts": list}})
		return
	}
	s.redirectWithNotice(w, r, "/courier-debts", "تم حذف دين المندوب")
}

// --- Electronic transfers: HTML ---

func (s *Server) transfersList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListTransfers(r.Context(), CurrentUser(r), r.URL.Query().Get("status"))
	if err != nil {
		LogErr(r, err)
		_ = s.render(w, r, "transfers_list.html", Page{Title: "التحويلات الإلكترونية", Error: "تعذّر تحميل التحويلات"})
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
		_ = s.render(w, r, "transfers_form.html", Page{Title: "تسجيل تحويل إلكتروني", Error: err.Error()})
		return
	}
	_, err = s.Store.CreateTransfer(r.Context(), u, store.TransferInput{
		SenderName: r.FormValue("sender_name"), Amount: amount,
		Method: r.FormValue("method"), Reference: r.FormValue("reference"),
	})
	if err != nil {
		_ = s.render(w, r, "transfers_form.html", Page{Title: "تسجيل تحويل إلكتروني", Error: err.Error()})
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
		_ = s.render(w, r, "transfers_list.html", Page{Title: "التحويلات الإلكترونية", Error: err.Error(),
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
		_ = s.render(w, r, "transfers_list.html", Page{Title: "التحويلات الإلكترونية", Error: err.Error(),
			Data: map[string]any{"Transfers": list}})
		return
	}
	s.redirectWithNotice(w, r, "/transfers", "تم رفض التحويل")
}

// --- JSON API ---

func (s *Server) apiCustomersList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListCustomers(r.Context(), CurrentUser(r), r.URL.Query().Get("q"))
	if err != nil {
		apiErr(w, http.StatusInternalServerError, err)
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
	limit, err := apiAmount(map[string]any{"amount": strVal(body, "credit_limit")})
	if strVal(body, "credit_limit") == "" {
		limit, err = 0, nil
	}
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	id, err := s.Store.CreateCustomer(r.Context(), CurrentUser(r), store.CustomerInput{
		Name: strVal(body, "name"), Phone: strVal(body, "phone"),
		Address: strVal(body, "address"), CreditLimit: limit, IsActive: true,
	})
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
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
		apiErr(w, http.StatusNotFound, err)
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
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	apiOK(w, map[string]any{"new_balance_cents": int64(newBal)})
}

func (s *Server) apiCustomerPayInvoice(w http.ResponseWriter, r *http.Request) {
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
	rem, err := s.Store.PayCustomerInvoice(r.Context(), CurrentUser(r), invID, amount, strVal(body, "method"), strVal(body, "notes"))
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	apiOK(w, map[string]any{"new_remaining_cents": int64(rem)})
}

func (s *Server) apiDebtorsList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListDebtors(r.Context(), CurrentUser(r))
	if err != nil {
		apiErr(w, http.StatusInternalServerError, err)
		return
	}
	apiOK(w, map[string]any{"debtors": list})
}

func (s *Server) apiVendorsList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListVendors(r.Context(), CurrentUser(r), r.URL.Query().Get("q"))
	if err != nil {
		apiErr(w, http.StatusInternalServerError, err)
		return
	}
	apiOK(w, map[string]any{"vendors": list})
}

func (s *Server) apiVendorCreate(w http.ResponseWriter, r *http.Request) {
	body, err := readJSONBody(r)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	limit, err := apiAmount(map[string]any{"amount": strVal(body, "credit_limit")})
	if strVal(body, "credit_limit") == "" {
		limit, err = 0, nil
	}
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	id, err := s.Store.CreateVendor(r.Context(), CurrentUser(r), store.VendorInput{
		Name: strVal(body, "name"), Phone: strVal(body, "phone"),
		Address: strVal(body, "address"), Notes: strVal(body, "notes"), CreditLimit: limit, IsActive: true,
	})
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
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
		apiErr(w, http.StatusBadRequest, err)
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
	paid, err := apiAmount(map[string]any{"amount": strVal(body, "paid")})
	if strVal(body, "paid") == "" {
		paid, err = 0, nil
		if v, ok := body["paid_cents"]; ok {
			if f, ok := v.(float64); ok {
				paid = money.Cents(int64(f))
			}
		}
	}
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	var lines []store.PurchaseLineInput
	if raw, ok := body["lines"].([]any); ok {
		for _, item := range raw {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			qty, err := money.ParseQty(strVal(m, "qty"))
			if err != nil {
				apiErr(w, http.StatusBadRequest, err)
				return
			}
			price, err := apiAmount(map[string]any{"amount": strVal(m, "price")})
			if err != nil {
				apiErr(w, http.StatusBadRequest, err)
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
		apiErr(w, http.StatusBadRequest, err)
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
		apiErr(w, http.StatusNotFound, err)
		return
	}
	apiOK(w, map[string]any{"purchase": d})
}

func (s *Server) apiPayablesList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListPayables(r.Context(), CurrentUser(r), r.URL.Query().Get("status"), r.URL.Query().Get("kind"))
	if err != nil {
		apiErr(w, http.StatusInternalServerError, err)
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
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	apiOK(w, map[string]any{"payable_id": id})
}

func (s *Server) apiPayablesDue(w http.ResponseWriter, r *http.Request) {
	due, err := s.Store.DuePayables(r.Context(), CurrentUser(r))
	if err != nil {
		apiErr(w, http.StatusInternalServerError, err)
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
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	apiOK(w, map[string]any{"remaining_cents": int64(rem)})
}

func (s *Server) apiCourierList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListCourierDebts(r.Context(), CurrentUser(r), r.URL.Query().Get("status"))
	if err != nil {
		apiErr(w, http.StatusInternalServerError, err)
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
		apiErr(w, http.StatusBadRequest, err)
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
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	apiOK(w, map[string]any{"remaining_cents": int64(rem)})
}

func (s *Server) apiTransfersList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListTransfers(r.Context(), CurrentUser(r), r.URL.Query().Get("status"))
	if err != nil {
		apiErr(w, http.StatusInternalServerError, err)
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
		apiErr(w, http.StatusBadRequest, err)
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
		apiErr(w, http.StatusBadRequest, err)
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
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	apiOK(w, map[string]any{"transfer_id": id, "status": "rejected"})
}
