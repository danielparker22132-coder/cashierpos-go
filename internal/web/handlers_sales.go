// handlers_sales.go — B1 HTTP handlers: sales POS, products, promos,
// stocktake (HTML pages + JSON API under /api).
//
// All routes are registered by registerSalesRoutes, which routes.go
// (B5) calls. Every mutating handler re-checks the permission in the
// service layer as well as in the handler.
package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/money"
	"cashierpos-go/internal/rbac"
	"cashierpos-go/internal/store"
)

// registerSalesRoutes registers every B1 route on m.
func (s *Server) registerSalesRoutes(m *http.ServeMux) {
	// --- Sales (HTML) ---
	// FIX-17 SW2-L10 (§15 row 25): /sales is login-open so warehouse can
	// read the invoice list; handleSalesIndex gates the POST and renders
	// a read-only list for warehouse (no POS form).
	m.HandleFunc("/sales", s.RequireLogin(s.handleSalesIndex))
	m.HandleFunc("/sales/{id}", s.RequireLogin(s.handleSaleDetail))
	// FIX-17 SW2-L10 follow-on: print uses RequireLogin, not
	// PermCreateInvoice — handleSalePrint enforces the same GetInvoice
	// visibility as the detail page (warehouse any invoice, cashier
	// own-only), and sale_print.html carries no profit/buy-price
	// fields, so a warehouse user who may view an invoice may print it.
	m.HandleFunc("/sales/{id}/print", s.RequireLogin(s.handleSalePrint))
	// --- Products (HTML) ---
	m.HandleFunc("/products", s.RequirePerm(rbac.PermViewProducts, s.handleProductsList))
	m.HandleFunc("/products/new", s.RequirePerm(rbac.PermManageProducts, s.handleProductForm))
	m.HandleFunc("/products/{id}", s.RequirePerm(rbac.PermViewProducts, s.handleProductDetail))
	m.HandleFunc("/products/{id}/edit", s.RequirePerm(rbac.PermManageProducts, s.handleProductForm))
	m.HandleFunc("/products/{id}/add-stock", s.RequirePerm(rbac.PermAddStock, s.handleProductAddStock))
	m.HandleFunc("/products/{id}/adjust-stock", s.RequirePerm(rbac.PermAdjustStock, s.handleProductAdjustStock))
	// --- Promotions (HTML) ---
	m.HandleFunc("/promotions", s.RequireLogin(s.handlePromosList))
	m.HandleFunc("/promotions/new", s.RequirePerm(rbac.PermManagePromotions, s.handlePromoForm))
	m.HandleFunc("/promotions/{id}/edit", s.RequirePerm(rbac.PermManagePromotions, s.handlePromoForm))
	m.HandleFunc("/promotions/{id}/toggle", s.RequirePerm(rbac.PermManagePromotions, s.handlePromoToggle))
	m.HandleFunc("/promotions/{id}/delete", s.RequirePerm(rbac.PermManagePromotions, s.handlePromoDelete))
	// --- Stocktake (HTML) ---
	m.HandleFunc("/stocktake", s.RequirePerm(rbac.PermViewStock, s.handleStocktakeList))
	m.HandleFunc("/stocktake/new", s.RequirePerm(rbac.PermManageStocktakes, s.handleStocktakeCreate))
	m.HandleFunc("/stocktake/{id}", s.RequirePerm(rbac.PermViewStock, s.handleStocktakeDetail))
	m.HandleFunc("/stocktake/{id}/scan", s.RequirePerm(rbac.PermStocktakeCount, s.handleStocktakeScan))
	m.HandleFunc("/stocktake/{id}/set", s.RequirePerm(rbac.PermStocktakeCount, s.handleStocktakeSet))
	m.HandleFunc("/stocktake/{id}/close", s.RequirePerm(rbac.PermManageStocktakes, s.handleStocktakeClose))
	m.HandleFunc("/stocktake/{id}/cancel", s.RequirePerm(rbac.PermManageStocktakes, s.handleStocktakeCancel))
	m.HandleFunc("/stocktake/{id}/approve", s.RequirePerm(rbac.PermApproveStocktake, s.handleStocktakeApprove))
	// --- JSON API ---
	m.HandleFunc("/api/sales/create", s.RequirePerm(rbac.PermCreateInvoice, s.handleAPISaleCreate))
	m.HandleFunc("/api/sales/preview", s.RequirePerm(rbac.PermCreateInvoice, s.handleAPISalePreview))
	m.HandleFunc("/api/sales/{id}", s.RequireLogin(s.handleAPISaleGet))
	m.HandleFunc("/api/sales/{id}/cancel", s.RequirePerm(rbac.PermCancelInvoice, s.handleAPISaleCancel))
	m.HandleFunc("/api/products/search", s.RequirePerm(rbac.PermViewProducts, s.handleAPIProductSearch))
	m.HandleFunc("/api/products/barcode/{code}", s.RequirePerm(rbac.PermViewProducts, s.handleAPIProductBarcode))
	m.HandleFunc("/api/products/{id}", s.RequirePerm(rbac.PermViewProducts, s.handleAPIProductGet))
	m.HandleFunc("/api/offers", s.RequireLogin(s.handleAPIOffersList))
	m.HandleFunc("/api/stocktake/{id}", s.RequirePerm(rbac.PermViewStock, s.handleAPIStocktakeGet))
}

// --- small helpers ---

func pathID(r *http.Request, key string) (int64, error) {
	return strconv.ParseInt(r.PathValue(key), 10, 64)
}

func formMoney(r *http.Request, name string) (money.Cents, error) {
	v := strings.TrimSpace(r.FormValue(name))
	if v == "" {
		return 0, nil
	}
	return money.ParseMoney(v)
}

func formQty(r *http.Request, name string) (money.Qty, error) {
	v := strings.TrimSpace(r.FormValue(name))
	if v == "" {
		return 0, nil
	}
	return money.ParseQty(v)
}

func formInt(r *http.Request, name string, def int) int {
	v := strings.TrimSpace(r.FormValue(name))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func formBool(r *http.Request, name string) bool {
	v := strings.TrimSpace(r.FormValue(name))
	return v == "1" || v == "on" || v == "true"
}

func (s *Server) renderErr(w http.ResponseWriter, r *http.Request, title string, err error) {
	s.renderErrStatus(w, r, title, err, http.StatusOK)
}

// safeHTMLErr returns the text safe to render into an HTML error page
// (FIX-26 R26-A-F1): a raw driver/library error becomes one generic
// Arabic message, while a safe Arabic business/validation error is kept
// verbatim. The HTTP status is unchanged; only the content is gated.
func safeHTMLErr(err error) string {
	if err == nil {
		return ""
	}
	if isDriverErr(err) {
		return "حدث خطأ داخلي، حاول مرة أخرى"
	}
	return err.Error()
}

// renderErrStatus renders the error page with an explicit HTTP status,
// so a refusal is a 400 to the client instead of a 200 page that only
// reads like an error (FIX-6 R6-L4).
func (s *Server) renderErrStatus(w http.ResponseWriter, r *http.Request, title string, err error, status int) {
	LogErr(r, err)
	_ = s.renderStatus(w, r, "error.html", Page{Title: title, Error: safeHTMLErr(err)}, status)
}

func apiErr(w http.ResponseWriter, status int, err error) {
	WriteJSON(w, status, map[string]any{"ok": false, "error": err.Error()})
}

// FIX-24 R24-E-N2: a 500 must never echo a raw driver/DB (or other
// library) error to the client ("SQL logic error: no such table:
// Expenses (1)"). Log the real error server-side and answer with one
// generic Arabic message, keeping the 500 status. All JSON-API 500
// branches must go through this helper, never apiErr(w, 500, err).
func apiErrInternal(w http.ResponseWriter, r *http.Request, err error) {
	LogErr(r, err)
	apiErr(w, http.StatusInternalServerError, errString("حدث خطأ داخلي، حاول مرة أخرى"))
}

// productJSON renders a product for the API. The buy price (cost
// basis) is omitted for roles without view_profit (FIX-12 SW-H09):
// cashier and warehouse must not see it, while admin/manager/
// supervisor keep it.
func productJSON(p *store.Product, u *auth.User) map[string]any {
	m := map[string]any{
		"id": p.ID, "barcode": p.Barcode, "name": p.Name, "category": p.Category,
		"retail_price": int64(p.RetailPrice),
		"wholesale_price": int64(p.WholesalePrice), "bulk_price": int64(p.BulkPrice),
		"quantity": int64(p.Quantity), "min_quantity": int64(p.MinQuantity),
		"unit": p.Unit, "expiry_date": p.ExpiryDate, "is_weighted": p.IsWeighted,
		"scale_plu": p.ScalePLU, "price_per_kg": int64(p.PricePerKg),
		"is_active": p.IsActive,
	}
	if u != nil && rbac.Can(u.Role, rbac.PermViewProfit) {
		m["buy_price"] = int64(p.BuyPrice)
	}
	return m
}

// customersForPicker lists active customers for the sale screen picker.
func (s *Server) customersForPicker() []map[string]any {
	rows, err := s.Store.DB.Query(`SELECT CustomerID,CustomerName,Phone,Balance,CreditLimit
		FROM Customers WHERE IsActive=1 ORDER BY CustomerID`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id int64
		var name, phone string
		var bal, limit int64
		if err := rows.Scan(&id, &name, &phone, &bal, &limit); err != nil {
			continue
		}
		out = append(out, map[string]any{"id": id, "name": name, "phone": phone,
			"balance": bal, "credit_limit": limit})
	}
	return out
}

// --- Sales HTML ---

func (s *Server) handleSalesIndex(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	ctx := r.Context()
	if r.Method == http.MethodPost {
		// FIX-17 SW2-L10: posting a sale still needs create_invoice
		// (the service layer re-checks it); only the GET list is open
		// to warehouse.
		if err := rbac.Require(u.Role, rbac.PermCreateInvoice); err != nil {
			s.deny(w, r, err.Error())
			return
		}
		s.handleSalePost(w, r, u)
		return
	}
	// FIX-19 R19-D-F1: a cashier sees only their own invoices in the
	// list — ListInvoices(userID=0) used to leak every cashier's
	// invoices with amounts, contradicting GetInvoice's cashier
	// own-only gate (FIX-3 M4). Admin/manager (effective admin) /
	// supervisor / warehouse keep 0 (all invoices); warehouse stays
	// view-any read-only (§15 row 25).
	listUserID := int64(0)
	if u.Role == rbac.RoleCashier {
		listUserID = u.ID
	}
	invoices, _ := s.Store.ListInvoices(ctx, u, store.Today(), store.Today(), listUserID)
	// FIX-17 SW2-L10 (§15 row 25): warehouse views invoices read-only —
	// today's list without the POS form. Any other role without
	// create_invoice has no invoice view here and is denied.
	if u.Role == rbac.RoleWarehouse {
		_ = s.render(w, r, "invoices.html", Page{Title: "الفواتير", ActiveNav: "sales", Data: map[string]any{
			"invoices": invoices, "today": store.Today(),
		}})
		return
	}
	if err := rbac.Require(u.Role, rbac.PermCreateInvoice); err != nil {
		s.deny(w, r, err.Error())
		return
	}
	products, _ := s.Store.ListProducts(ctx, u, false)
	_ = s.render(w, r, "sales.html", Page{Title: "المبيعات", ActiveNav: "sales", Data: map[string]any{
		"invoices": invoices, "products": products, "customers": s.customersForPicker(),
		"canDiscount":   rbac.Can(u.Role, rbac.PermGiveDiscount),
		"canEditPrice":  rbac.Can(u.Role, rbac.PermEditPrice),
		"canBelowCost":  rbac.Can(u.Role, rbac.PermApproveBelowCost),
		"canViewProfit": rbac.Can(u.Role, rbac.PermViewProfit),
		"today":         store.Today(),
	}})
}

// handleSalePost posts one invoice from the POS form.
// Form fields: customer_id, sale_type, payment_method, paid,
// invoice_discount, allow_over_limit, notes, and repeated
// line_product_id / line_qty / line_price / line_discount rows.
func (s *Server) handleSalePost(w http.ResponseWriter, r *http.Request, u *auth.User) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		s.renderErr(w, r, "المبيعات", err)
		return
	}
	customerID, _ := strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	paid, err := formMoney(r, "paid")
	if err != nil {
		s.renderErr(w, r, "المبيعات", err)
		return
	}
	invDisc, err := formMoney(r, "invoice_discount")
	if err != nil {
		s.renderErr(w, r, "المبيعات", err)
		return
	}
	var lines []store.SaleLineInput
	ids := r.Form["line_product_id"]
	qtys := r.Form["line_qty"]
	prices := r.Form["line_price"]
	discs := r.Form["line_discount"]
	for i, idStr := range ids {
		pid, err := strconv.ParseInt(strings.TrimSpace(idStr), 10, 64)
		if err != nil || pid == 0 {
			continue
		}
		var q money.Qty
		if i < len(qtys) {
			q, err = money.ParseQty(qtys[i])
			if err != nil {
				s.renderErr(w, r, "المبيعات", err)
				return
			}
		}
		var price money.Cents
		if i < len(prices) && strings.TrimSpace(prices[i]) != "" {
			price, err = money.ParseMoney(prices[i])
			if err != nil {
				s.renderErr(w, r, "المبيعات", err)
				return
			}
		}
		var disc money.Cents
		if i < len(discs) && strings.TrimSpace(discs[i]) != "" {
			disc, err = money.ParseMoney(discs[i])
			if err != nil {
				s.renderErr(w, r, "المبيعات", err)
				return
			}
		}
		lines = append(lines, store.SaleLineInput{ProductID: pid, Qty: q, UnitPrice: price, ItemDiscount: disc})
	}
	res, err := s.Store.CreateInvoice(ctx, u, store.CreateInvoiceInput{
		CustomerID: customerID, SaleType: r.FormValue("sale_type"),
		Lines: lines, InvoiceDiscount: invDisc, Paid: paid,
		PaymentMethod:  r.FormValue("payment_method"),
		AllowOverLimit: formBool(r, "allow_over_limit"),
		// FIX-3 H2: the checkbox tick is only a REQUEST — the service
		// layer checks approve_below_cost, so a forged form value from
		// a cashier is refused server-side (a UI tick is not authority).
		AllowBelowCost: formBool(r, "allow_below_cost"),
		Notes:          r.FormValue("notes"),
	})
	if err != nil {
		_ = s.render(w, r, "sales.html", Page{Title: "المبيعات", ActiveNav: "sales", Error: safeHTMLErr(err), Data: map[string]any{
			"customers":     s.customersForPicker(),
			"canDiscount":   rbac.Can(u.Role, rbac.PermGiveDiscount),
			"canEditPrice":  rbac.Can(u.Role, rbac.PermEditPrice),
			"canBelowCost":  rbac.Can(u.Role, rbac.PermApproveBelowCost),
			"canViewProfit": rbac.Can(u.Role, rbac.PermViewProfit),
		}})
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/sales/%d?created=1%s", res.InvoiceID, warnQuery(res.Warnings)), http.StatusSeeOther)
}

// warnQuery encodes sale warnings (e.g. near-expiry) into the redirect
// query so the detail page can show them after the POST redirect.
func warnQuery(warnings []string) string {
	if len(warnings) == 0 {
		return ""
	}
	return "&warn=" + url.QueryEscape(strings.Join(warnings, " | "))
}

func (s *Server) handleSaleDetail(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		s.renderErr(w, r, "الفاتورة", err)
		return
	}
	inv, err := s.Store.GetInvoice(r.Context(), u, id)
	if err != nil {
		// FIX-3 M4: another user's invoice is a 403, not an error page.
		if errors.Is(err, store.ErrInvoiceForbidden) {
			s.deny(w, r, err.Error())
			return
		}
		s.renderErr(w, r, "الفاتورة", err)
		return
	}
	profit, _ := s.Store.InvoiceProfit(r.Context(), u, id)
	notice := ""
	if r.URL.Query().Get("created") == "1" {
		notice = "تم إنشاء الفاتورة بنجاح — يمكنك طباعتها من هنا."
	}
	if w := strings.TrimSpace(r.URL.Query().Get("warn")); w != "" {
		notice += " تنبيه: " + w
	}
	_ = s.render(w, r, "sale_detail.html", Page{Title: fmt.Sprintf("فاتورة %d", inv.Summary.Number),
		ActiveNav: "sales", Notice: notice, Data: map[string]any{
			"invoice": inv, "profit": profit,
			"canViewProfit": rbac.Can(u.Role, rbac.PermViewProfit),
			"canCancel":     rbac.Can(u.Role, rbac.PermCancelInvoice),
		}})
}

func (s *Server) handleSalePrint(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		s.renderErr(w, r, "طباعة الفاتورة", err)
		return
	}
	inv, err := s.Store.GetInvoice(r.Context(), u, id)
	if err != nil {
		if errors.Is(err, store.ErrInvoiceForbidden) {
			s.deny(w, r, err.Error())
			return
		}
		s.renderErr(w, r, "طباعة الفاتورة", err)
		return
	}
	_ = s.render(w, r, "sale_print.html", Page{Title: fmt.Sprintf("طباعة فاتورة %d", inv.Summary.Number),
		Data: map[string]any{"invoice": inv}})
}

// --- Products HTML ---

func (s *Server) handleProductsList(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	ctx := r.Context()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	var products []*store.Product
	var err error
	if q != "" {
		products, err = s.Store.SearchProducts(ctx, u, q)
	} else {
		products, err = s.Store.ListProducts(ctx, u, true)
	}
	if err != nil {
		s.renderErr(w, r, "المنتجات", err)
		return
	}
	low, _ := s.Store.LowStockProducts(ctx, u)
	exp, _ := s.Store.ExpiringProducts(ctx, u)
	_ = s.render(w, r, "products.html", Page{Title: "المنتجات", ActiveNav: "products", Data: map[string]any{
		"products": products, "q": q, "low": low, "expiring": exp,
		"canManage":   rbac.Can(u.Role, rbac.PermManageProducts),
		"canAddStock": rbac.Can(u.Role, rbac.PermAddStock),
		"canAdjust":   rbac.Can(u.Role, rbac.PermAdjustStock),
		// FIX-12 SW-H09: the buy-price column renders only for roles
		// that may view profit (admin/manager/supervisor).
		"canViewProfit": rbac.Can(u.Role, rbac.PermViewProfit),
	}})
}

func productInputFromForm(r *http.Request, cur *store.Product) (store.ProductInput, error) {
	in := store.ProductInput{}
	if cur != nil {
		in = store.ProductInput{
			Barcode: cur.Barcode, Name: cur.Name, Category: cur.Category, Supplier: cur.Supplier,
			BuyPrice: cur.BuyPrice, RetailPrice: cur.RetailPrice, WholesalePrice: cur.WholesalePrice,
			BulkPrice: cur.BulkPrice, Quantity: cur.Quantity, MinQuantity: cur.MinQuantity,
			Unit: cur.Unit, ExpiryDate: cur.ExpiryDate, BatchNumber: cur.BatchNumber,
			ExpiryAlertDays: cur.ExpiryAlertDays, IsExpiryTracked: cur.IsExpiryTracked,
			IsWeighted: cur.IsWeighted, ScalePLU: cur.ScalePLU, PricePerKg: cur.PricePerKg,
			ScaleBarcodeType: cur.ScaleBarcodeType, ScaleEnabled: cur.ScaleEnabled,
			ScalePrefixOverride: cur.ScalePrefixOverride, IsActive: cur.IsActive,
		}
	}
	in.Name = strings.TrimSpace(r.FormValue("name"))
	in.Barcode = strings.TrimSpace(r.FormValue("barcode"))
	in.Category = strings.TrimSpace(r.FormValue("category"))
	in.Supplier = strings.TrimSpace(r.FormValue("supplier"))
	in.Unit = strings.TrimSpace(r.FormValue("unit"))
	if in.Unit == "" {
		in.Unit = "قطعة"
	}
	in.ExpiryDate = strings.TrimSpace(r.FormValue("expiry_date"))
	in.BatchNumber = strings.TrimSpace(r.FormValue("batch_number"))
	in.ScalePLU = strings.TrimSpace(r.FormValue("scale_plu"))
	in.ScaleBarcodeType = strings.TrimSpace(r.FormValue("scale_barcode_type"))
	if in.ScaleBarcodeType == "" {
		in.ScaleBarcodeType = "WeightEmbedded"
	}
	in.ScalePrefixOverride = strings.TrimSpace(r.FormValue("scale_prefix_override"))
	var err error
	if in.BuyPrice, err = formMoney(r, "buy_price"); err != nil {
		return in, err
	}
	if in.RetailPrice, err = formMoney(r, "retail_price"); err != nil {
		return in, err
	}
	if in.WholesalePrice, err = formMoney(r, "wholesale_price"); err != nil {
		return in, err
	}
	if in.BulkPrice, err = formMoney(r, "bulk_price"); err != nil {
		return in, err
	}
	if in.PricePerKg, err = formMoney(r, "price_per_kg"); err != nil {
		return in, err
	}
	if in.Quantity, err = formQty(r, "quantity"); err != nil {
		return in, err
	}
	if in.MinQuantity, err = formQty(r, "min_quantity"); err != nil {
		return in, err
	}
	in.ExpiryAlertDays = formInt(r, "expiry_alert_days", 30)
	in.IsExpiryTracked = formBool(r, "is_expiry_tracked")
	in.IsWeighted = formBool(r, "is_weighted")
	in.ScaleEnabled = formBool(r, "scale_enabled")
	in.IsActive = formBool(r, "is_active")
	if cur == nil {
		in.IsActive = true
	}
	return in, nil
}

func moneyInput(c money.Cents) string {
	return fmt.Sprintf("%d.%02d", int64(c)/100, int64(c)%100)
}

func (s *Server) handleProductForm(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	ctx := r.Context()
	idStr := r.PathValue("id")
	isNew := idStr == ""
	var cur *store.Product
	if !isNew {
		id, err := pathID(r, "id")
		if err != nil {
			s.renderErr(w, r, "المنتج", err)
			return
		}
		cur, err = s.Store.GetProduct(ctx, u, id)
		if err != nil {
			s.renderErr(w, r, "المنتج", err)
			return
		}
	}
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			s.renderErr(w, r, "المنتج", err)
			return
		}
		in, err := productInputFromForm(r, cur)
		if err != nil {
			_ = s.render(w, r, "product_form.html", Page{Title: "المنتج", Error: safeHTMLErr(err), Data: map[string]any{"product": cur, "isNew": isNew}})
			return
		}
		var err2 error
		var saved *store.Product
		if isNew {
			saved, err2 = s.Store.CreateProduct(ctx, u, in)
		} else {
			saved, err2 = s.Store.UpdateProduct(ctx, u, cur.ID, in)
		}
		if err2 != nil {
			_ = s.render(w, r, "product_form.html", Page{Title: "المنتج", Error: safeHTMLErr(err2), Data: map[string]any{"product": cur, "isNew": isNew}})
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/products/%d", saved.ID), http.StatusSeeOther)
		return
	}
	title := "منتج جديد"
	if !isNew {
		title = "تعديل منتج"
	}
	data := map[string]any{"product": cur, "isNew": isNew}
	if cur != nil {
		data["buy_price"] = moneyInput(cur.BuyPrice)
		data["bulk_price"] = moneyInput(cur.BulkPrice)
		data["wholesale_price"] = moneyInput(cur.WholesalePrice)
		data["retail_price"] = moneyInput(cur.RetailPrice)
		data["price_per_kg"] = moneyInput(cur.PricePerKg)
	}
	_ = s.render(w, r, "product_form.html", Page{Title: title, ActiveNav: "products", Data: data})
}

func (s *Server) handleProductDetail(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		s.renderErr(w, r, "المنتج", err)
		return
	}
	p, err := s.Store.GetProduct(r.Context(), u, id)
	if err != nil {
		s.renderErr(w, r, "المنتج", err)
		return
	}
	movs, _ := s.Store.GetStockLedger(r.Context(), u, id)
	_ = s.render(w, r, "product_detail.html", Page{Title: p.Name, ActiveNav: "products", Data: map[string]any{
		"product": p, "movements": movs,
		"canManage":   rbac.Can(u.Role, rbac.PermManageProducts),
		"canAddStock": rbac.Can(u.Role, rbac.PermAddStock),
		"canAdjust":   rbac.Can(u.Role, rbac.PermAdjustStock),
		"lowStock":    p.Quantity <= p.MinQuantity,
		// FIX-12 SW-H09: buy price (header + movement rows) renders
		// only for roles that may view profit.
		"canViewProfit": rbac.Can(u.Role, rbac.PermViewProfit),
	}})
}

func (s *Server) handleProductAddStock(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		s.renderErr(w, r, "إضافة مخزون", err)
		return
	}
	if r.Method != http.MethodPost {
		http.Redirect(w, r, fmt.Sprintf("/products/%d", id), http.StatusSeeOther)
		return
	}
	qty, err := formQty(r, "quantity")
	if err != nil {
		s.renderErr(w, r, "إضافة مخزون", err)
		return
	}
	if _, err := s.Store.AddStock(r.Context(), u, id, qty); err != nil {
		// FIX-22 R22-B-F1: a refused add (e.g. overflow) is a 400, not
		// a 200 error page (FIX-6 R6-L4 pattern via renderErrStatus).
		// FIX-25 Observation E: a raw driver error (dropped Products
		// table) must not render into the HTML error page — answer
		// the generic message instead, keeping the 400 status; safe
		// Arabic business errors (overflow, missing product) keep
		// their text.
		if isDriverErr(err) {
			s.renderErrStatus(w, r, "إضافة مخزون", errString("تعذّر إضافة المخزون"), http.StatusBadRequest)
			return
		}
		s.renderErrStatus(w, r, "إضافة مخزون", err, http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/products/%d", id), http.StatusSeeOther)
}

func (s *Server) handleProductAdjustStock(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		s.renderErr(w, r, "تعديل مخزون", err)
		return
	}
	if r.Method != http.MethodPost {
		http.Redirect(w, r, fmt.Sprintf("/products/%d", id), http.StatusSeeOther)
		return
	}
	qty, err := formQty(r, "quantity")
	if err != nil {
		s.renderErr(w, r, "تعديل مخزون", err)
		return
	}
	if _, err := s.Store.AdjustStock(r.Context(), u, id, qty, r.FormValue("reason")); err != nil {
		_ = s.render(w, r, "error.html", Page{Title: "تعديل مخزون", Error: safeHTMLErr(err)})
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/products/%d", id), http.StatusSeeOther)
}

// --- Promotions HTML ---

func (s *Server) handlePromosList(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	status := r.URL.Query().Get("status")
	offers, err := s.Store.ListOffers(r.Context(), u, status)
	if err != nil {
		s.renderErr(w, r, "العروض", err)
		return
	}
	_ = s.render(w, r, "promos.html", Page{Title: "العروض والترويج", ActiveNav: "promos", Data: map[string]any{
		"offers": offers, "status": status,
		"canManage": rbac.Can(u.Role, rbac.PermManagePromotions),
	}})
}

func offerInputFromForm(r *http.Request, cur *store.Offer) (store.OfferInput, error) {
	in := store.OfferInput{IsActive: true}
	if cur != nil {
		// Rebuild from the stored offer so unedited fields survive.
		pct, amount, x, _ := curValueParts(cur)
		freeY, _ := curFreeY(cur)
		thr, _ := curThreshold(cur)
		in = store.OfferInput{Name: cur.Name, Type: cur.Type, Percent: pct, Amount: amount,
			Threshold: thr, BuyX: x, FreeY: freeY, StartAt: cur.StartAt, EndAt: cur.EndAt,
			IsActive: cur.IsActive, Targets: cur.Targets}
	}
	in.Name = strings.TrimSpace(r.FormValue("name"))
	in.Type = strings.TrimSpace(r.FormValue("type"))
	if in.Type == "" {
		in.Type = store.OfferProductPct
	}
	in.StartAt = strings.TrimSpace(r.FormValue("start_at"))
	in.EndAt = strings.TrimSpace(r.FormValue("end_at"))
	in.IsActive = formBool(r, "is_active")
	var err error
	if pctStr := strings.TrimSpace(r.FormValue("percent")); pctStr != "" {
		var pct int64
		pct, err = strconv.ParseInt(pctStr, 10, 64)
		if err != nil {
			return in, fmt.Errorf("نسبة الخصم غير صحيحة")
		}
		in.Percent = pct
	}
	// Only overwrite the seeded (stored) Amount/Threshold when the
	// field was actually filled in — an empty field must NOT wipe the
	// stored value when editing an offer.
	if amtStr := strings.TrimSpace(r.FormValue("amount")); amtStr != "" {
		if in.Amount, err = money.ParseMoney(amtStr); err != nil {
			return in, err
		}
	}
	// P-6: on create, a blank amount for product_price is refused —
	// a blank field must not silently become a free (0) promo price
	// (PyQt R1-M1: "سعر العرض الترويجي مطلوب"). A typed 0 stays
	// allowed (free); on edit a blank preserves the stored value.
	if cur == nil && in.Type == store.OfferProductPrice && strings.TrimSpace(r.FormValue("amount")) == "" {
		return in, errors.New("سعر العرض الترويجي مطلوب")
	}
	if thrStr := strings.TrimSpace(r.FormValue("threshold")); thrStr != "" {
		if in.Threshold, err = money.ParseMoney(thrStr); err != nil {
			return in, err
		}
	}
	if bx := strings.TrimSpace(r.FormValue("buy_x")); bx != "" {
		if in.BuyX, err = strconv.ParseInt(bx, 10, 64); err != nil {
			return in, fmt.Errorf("عدد الوحدات المدفوعة غير صحيح")
		}
	}
	if fy := strings.TrimSpace(r.FormValue("free_y")); fy != "" {
		if in.FreeY, err = strconv.ParseInt(fy, 10, 64); err != nil {
			return in, fmt.Errorf("عدد الوحدات المجانية غير صحيح")
		}
	}
	// Targets from the form: repeated target_type / target_id /
	// target_name / target_qty rows.
	types := r.Form["target_type"]
	ids := r.Form["target_id"]
	names := r.Form["target_name"]
	qtys := r.Form["target_qty"]
	var targets []store.OfferTarget
	for i, tt := range types {
		tt = strings.TrimSpace(tt)
		if tt == "" {
			continue
		}
		var tid int64
		if i < len(ids) {
			tid, _ = strconv.ParseInt(strings.TrimSpace(ids[i]), 10, 64)
		}
		var tname string
		if i < len(names) {
			tname = strings.TrimSpace(names[i])
		}
		var tq money.Qty = 1000
		if i < len(qtys) && strings.TrimSpace(qtys[i]) != "" {
			tq, err = money.ParseQty(qtys[i])
			if err != nil {
				return in, err
			}
		}
		targets = append(targets, store.OfferTarget{TargetType: tt, TargetID: tid, TargetName: tname, Qty: tq})
	}
	if len(targets) > 0 {
		in.Targets = targets
	}
	return in, nil
}

// curValueParts / curFreeY / curThreshold decode a stored offer's
// Value/Threshold/FreeQty without re-exporting the private parsers.
func curValueParts(o *store.Offer) (int64, money.Cents, int64, error) {
	// Stored Value: pct int, cents int, or X int — decode by type.
	switch o.Type {
	case store.OfferProductPct, store.OfferCategoryPct, store.OfferThresholdPct:
		n, err := strconv.ParseInt(strings.TrimSpace(o.Value), 10, 64)
		return n, 0, 0, err
	case store.OfferInvoiceFixed, store.OfferBundleFixed, store.OfferProductFixed, store.OfferProductPrice:
		n, err := strconv.ParseInt(strings.TrimSpace(o.Value), 10, 64)
		return 0, money.Cents(n), 0, err
	case store.OfferBuyXGetY:
		n, err := strconv.ParseInt(strings.TrimSpace(o.Value), 10, 64)
		return 0, 0, n, err
	}
	return 0, 0, 0, nil
}

func curFreeY(o *store.Offer) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(o.FreeQty), 10, 64)
}

func curThreshold(o *store.Offer) (money.Cents, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(o.Threshold), 10, 64)
	return money.Cents(n), err
}

func (s *Server) handlePromoForm(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	ctx := r.Context()
	idStr := r.PathValue("id")
	isNew := idStr == ""
	var cur *store.Offer
	if !isNew {
		id, err := pathID(r, "id")
		if err != nil {
			s.renderErr(w, r, "العرض", err)
			return
		}
		cur, err = s.Store.GetOffer(ctx, u, id)
		if err != nil {
			s.renderErr(w, r, "العرض", err)
			return
		}
	}
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			s.renderErr(w, r, "العرض", err)
			return
		}
		in, err := offerInputFromForm(r, cur)
		if err != nil {
			_ = s.render(w, r, "promo_form.html", Page{Title: "العرض", Error: safeHTMLErr(err), Data: map[string]any{"offer": cur, "isNew": isNew}})
			return
		}
		var err2 error
		if isNew {
			_, err2 = s.Store.CreateOffer(ctx, u, in)
		} else {
			_, err2 = s.Store.UpdateOffer(ctx, u, cur.ID, in)
		}
		if err2 != nil {
			_ = s.render(w, r, "promo_form.html", Page{Title: "العرض", Error: safeHTMLErr(err2), Data: map[string]any{"offer": cur, "isNew": isNew}})
			return
		}
		http.Redirect(w, r, "/promotions", http.StatusSeeOther)
		return
	}
	products, _ := s.Store.ListProducts(ctx, u, false)
	cats, _ := s.Store.ListCategories(ctx, u)
	data := map[string]any{
		"offer": cur, "isNew": isNew, "products": products, "categories": cats,
		"percent": "", "amount": "", "threshold": "", "buy_x": "", "free_y": "",
	}
	if cur != nil {
		pct, amount, x, _ := curValueParts(cur)
		freeY, _ := curFreeY(cur)
		thr, _ := curThreshold(cur)
		switch cur.Type {
		case store.OfferProductPct, store.OfferCategoryPct, store.OfferThresholdPct:
			data["percent"] = pct
		case store.OfferInvoiceFixed, store.OfferBundleFixed, store.OfferProductFixed, store.OfferProductPrice:
			data["amount"] = moneyInput(amount)
		case store.OfferBuyXGetY:
			data["buy_x"] = x
			data["free_y"] = freeY
		}
		if cur.Type == store.OfferThresholdPct {
			data["threshold"] = moneyInput(thr)
		}
	}
	_ = s.render(w, r, "promo_form.html", Page{Title: "العرض", ActiveNav: "promos", Data: data})
}

func (s *Server) handlePromoToggle(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		s.renderErr(w, r, "العرض", err)
		return
	}
	// FIX-29 lane A: toggle is POST-only, same shape as handlePromoDelete
	// (fix-28 lane B). A state-changing GET link lets any prefetch or
	// crawler flip an offer; a non-POST request now redirects to the list
	// with no action.
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/promotions", http.StatusSeeOther)
		return
	}
	cur, err := s.Store.GetOffer(r.Context(), u, id)
	if err != nil {
		s.renderErr(w, r, "العرض", err)
		return
	}
	if _, err := s.Store.SetOfferActive(r.Context(), u, id, !cur.IsActive); err != nil {
		s.renderErr(w, r, "العرض", err)
		return
	}
	http.Redirect(w, r, "/promotions", http.StatusSeeOther)
}

func (s *Server) handlePromoDelete(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		s.renderErr(w, r, "العرض", err)
		return
	}
	// FIX-28 lane B: deletion is POST-only. The old GET link was gated on
	// confirm(), which headless/cloud browsers auto-dismiss (confirm()
	// returns false), so the click cancelled navigation and the link
	// no-op'd while the route itself still deleted on GET. A non-POST
	// request now redirects to the list with no action, the same shape
	// as the sibling HTML mutating handlers (add-stock, stocktake).
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/promotions", http.StatusSeeOther)
		return
	}
	if err := s.Store.DeleteOffer(r.Context(), u, id); err != nil {
		_ = s.render(w, r, "promos.html", Page{Title: "العروض والترويج", Error: safeHTMLErr(err), Data: map[string]any{
			"offers": []any{}, "canManage": true,
		}})
		return
	}
	http.Redirect(w, r, "/promotions", http.StatusSeeOther)
}

// --- Stocktake HTML ---

func (s *Server) handleStocktakeList(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	status := r.URL.Query().Get("status")
	list, err := s.Store.ListStocktakes(r.Context(), u, status)
	if err != nil {
		s.renderErr(w, r, "الجرد", err)
		return
	}
	_ = s.render(w, r, "stocktake.html", Page{Title: "الجرد الدوري", ActiveNav: "stocktake", Data: map[string]any{
		"sessions": list, "status": status,
		"canManage": rbac.Can(u.Role, rbac.PermManageStocktakes),
	}})
}

func (s *Server) handleStocktakeCreate(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/stocktake", http.StatusSeeOther)
		return
	}
	st, err := s.Store.CreateStocktake(r.Context(), u, r.FormValue("notes"))
	if err != nil {
		s.renderErr(w, r, "الجرد", err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/stocktake/%d", st.ID), http.StatusSeeOther)
}

func (s *Server) handleStocktakeDetail(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		s.renderErr(w, r, "الجرد", err)
		return
	}
	st, err := s.Store.GetStocktake(r.Context(), u, id)
	if err != nil {
		s.renderErr(w, r, "الجرد", err)
		return
	}
	_ = s.render(w, r, "stocktake_detail.html", Page{Title: fmt.Sprintf("جلسة جرد %d", st.ID),
		ActiveNav: "stocktake", Data: map[string]any{
			"session":    st,
			"canCount":   rbac.Can(u.Role, rbac.PermStocktakeCount),
			"canManage":  rbac.Can(u.Role, rbac.PermManageStocktakes),
			"canApprove": rbac.Can(u.Role, rbac.PermApproveStocktake),
		}})
}

func (s *Server) handleStocktakeScan(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		s.renderErr(w, r, "الجرد", err)
		return
	}
	if r.Method != http.MethodPost {
		http.Redirect(w, r, fmt.Sprintf("/stocktake/%d", id), http.StatusSeeOther)
		return
	}
	var qty *money.Qty
	if v := strings.TrimSpace(r.FormValue("qty")); v != "" {
		q, err := money.ParseQty(v)
		if err != nil {
			s.renderErr(w, r, "الجرد", err)
			return
		}
		qty = &q
	}
	if _, err := s.Store.ScanStocktakeLine(r.Context(), u, id, r.FormValue("barcode"), qty); err != nil {
		_ = s.render(w, r, "error.html", Page{Title: "الجرد", Error: safeHTMLErr(err)})
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/stocktake/%d", id), http.StatusSeeOther)
}

func (s *Server) handleStocktakeSet(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		s.renderErr(w, r, "الجرد", err)
		return
	}
	if r.Method != http.MethodPost {
		http.Redirect(w, r, fmt.Sprintf("/stocktake/%d", id), http.StatusSeeOther)
		return
	}
	pid, err := strconv.ParseInt(r.FormValue("product_id"), 10, 64)
	if err != nil {
		s.renderErr(w, r, "الجرد", err)
		return
	}
	qty, err := formQty(r, "qty")
	if err != nil {
		s.renderErr(w, r, "الجرد", err)
		return
	}
	if _, err := s.Store.SetStocktakeCounted(r.Context(), u, id, pid, qty); err != nil {
		_ = s.render(w, r, "error.html", Page{Title: "الجرد", Error: safeHTMLErr(err)})
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/stocktake/%d", id), http.StatusSeeOther)
}

func (s *Server) handleStocktakeClose(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		s.renderErr(w, r, "الجرد", err)
		return
	}
	// FIX-29 lane C (R29-C-F3): closing is POST-only. The route mutated
	// on GET, so a plain link/prefetch could close a session. A non-POST
	// request now redirects to the detail page with no action, the same
	// shape as the sibling HTML mutating handlers.
	if r.Method != http.MethodPost {
		http.Redirect(w, r, fmt.Sprintf("/stocktake/%d", id), http.StatusSeeOther)
		return
	}
	if _, err := s.Store.CloseStocktake(r.Context(), u, id); err != nil {
		s.renderErr(w, r, "الجرد", err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/stocktake/%d", id), http.StatusSeeOther)
}

func (s *Server) handleStocktakeCancel(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		s.renderErr(w, r, "الجرد", err)
		return
	}
	// FIX-29 lane C (R29-C-F3): cancelling is POST-only, same as close
	// above — a non-POST request redirects with no action.
	if r.Method != http.MethodPost {
		http.Redirect(w, r, fmt.Sprintf("/stocktake/%d", id), http.StatusSeeOther)
		return
	}
	if _, err := s.Store.CancelStocktake(r.Context(), u, id); err != nil {
		s.renderErr(w, r, "الجرد", err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/stocktake/%d", id), http.StatusSeeOther)
}

func (s *Server) handleStocktakeApprove(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		s.renderErr(w, r, "الجرد", err)
		return
	}
	// FIX-29 lane B: approval is POST-only. Approving applies the counted
	// stock diffs, so a GET must never trigger it (a prefetched/crawled
	// link would otherwise silently rewrite stock). A non-POST request
	// now redirects to the detail page with no action, the same shape as
	// the sibling HTML mutating handlers (promo delete, stocktake create).
	if r.Method != http.MethodPost {
		http.Redirect(w, r, fmt.Sprintf("/stocktake/%d", id), http.StatusSeeOther)
		return
	}
	if _, err := s.Store.ApproveStocktake(r.Context(), u, id, r.FormValue("reason")); err != nil {
		s.renderErr(w, r, "الجرد", err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/stocktake/%d", id), http.StatusSeeOther)
}

// --- JSON API ---

type apiSaleLine struct {
	ProductID    int64  `json:"product_id"`
	Qty          string `json:"qty"`
	UnitPrice    int64  `json:"unit_price"`
	ItemDiscount int64  `json:"item_discount"`
	IsWeighted   bool   `json:"is_weighted"`
	ScaleBarcode string `json:"scale_barcode"`
	ScalePLU     string `json:"scale_plu"`
}

type apiSaleRequest struct {
	CustomerID      int64         `json:"customer_id"`
	SaleType        string        `json:"sale_type"`
	Lines           []apiSaleLine `json:"lines"`
	InvoiceDiscount int64         `json:"invoice_discount"`
	Paid            int64         `json:"paid"`
	PaymentMethod   string        `json:"payment_method"`
	AllowOverLimit  bool          `json:"allow_over_limit"`
	AllowBelowCost  bool          `json:"allow_below_cost"`
	Notes           string        `json:"notes"`
}

func (s *Server) parseSaleRequest(r *http.Request) (store.CreateInvoiceInput, error) {
	var req apiSaleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return store.CreateInvoiceInput{}, fmt.Errorf("طلب غير صحيح")
	}
	in := store.CreateInvoiceInput{
		CustomerID: req.CustomerID, SaleType: req.SaleType,
		InvoiceDiscount: money.Cents(req.InvoiceDiscount), Paid: money.Cents(req.Paid),
		PaymentMethod: req.PaymentMethod, AllowOverLimit: req.AllowOverLimit, AllowBelowCost: req.AllowBelowCost, Notes: req.Notes,
	}
	for _, ln := range req.Lines {
		q, err := money.ParseQty(ln.Qty)
		if err != nil {
			return in, err
		}
		in.Lines = append(in.Lines, store.SaleLineInput{
			ProductID: ln.ProductID, Qty: q, UnitPrice: money.Cents(ln.UnitPrice),
			ItemDiscount: money.Cents(ln.ItemDiscount), IsWeighted: ln.IsWeighted,
			ScaleBarcode: ln.ScaleBarcode, ScalePLU: ln.ScalePLU,
		})
	}
	return in, nil
}

// FIX-15 L2: profit and free_cost are omitted for roles without
// view_profit — the profit leaks the unit buy price (net - profit =
// cost), and free_cost is the buy cost of the free units. Same
// omit-the-key pattern as the GET /api/sales/{id} buy_price strip
// below (:1283-1292); omitted, not zeroed, so the field is not even
// advertised to roles that may not see it.
func saleResultJSON(res *store.InvoiceResult, canViewProfit bool) map[string]any {
	offers := make([]map[string]any, 0, len(res.AppliedOffers))
	for _, o := range res.AppliedOffers {
		offers = append(offers, map[string]any{"offer_id": o.OfferID, "name": o.Name, "saved": int64(o.Saved)})
	}
	free := map[string]int64{}
	for pid, q := range res.FreeUnits {
		free[strconv.FormatInt(pid, 10)] = int64(q)
	}
	m := map[string]any{
		"ok": true, "invoice_id": res.InvoiceID, "invoice_number": res.InvoiceNumber,
		"gross": int64(res.Gross), "discount": int64(res.Discount),
		"promo_discount": int64(res.PromoDiscount), "net": int64(res.Net),
		"paid": int64(res.Paid), "remaining": int64(res.Remaining),
		"warnings": res.Warnings, "shift_id": res.ShiftID,
		"applied_offers": offers, "promo_saved": int64(res.PromoSaved), "free_units": free,
	}
	if canViewProfit {
		m["profit"] = int64(res.Profit)
		m["free_cost"] = int64(res.FreeCost)
	}
	return m
}

func (s *Server) handleAPISaleCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiErr(w, http.StatusMethodNotAllowed, fmt.Errorf("POST فقط"))
		return
	}
	in, err := s.parseSaleRequest(r)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.Store.CreateInvoice(r.Context(), CurrentUser(r), in)
	if err != nil {
		// FIX-12 SW-L08: a service-layer permission denial (discount /
		// price-edit / below-cost / credit-override gates) answers 403
		// with the uniform denial text, not 400.
		apiErrForService(w, r, err)
		return
	}
	// FIX-15 L2: profit/free_cost ride on view_profit — the creating
	// cashier must not get the profit back in the create response.
	u := CurrentUser(r)
	canViewProfit := u != nil && rbac.Can(u.Role, rbac.PermViewProfit)
	WriteJSON(w, http.StatusOK, saleResultJSON(res, canViewProfit))
}

// handleAPISalePreview evaluates promos for a cart without posting.
func (s *Server) handleAPISalePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiErr(w, http.StatusMethodNotAllowed, fmt.Errorf("POST فقط"))
		return
	}
	in, err := s.parseSaleRequest(r)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	u := CurrentUser(r)
	// FIX-3 H2: the preview applies the below-cost approval gate too —
	// a cashier must not get a green preview for a sale they cannot post.
	if in.AllowBelowCost {
		if err := rbac.Require(u.Role, rbac.PermApproveBelowCost); err != nil {
			apiErr(w, http.StatusForbidden, err)
			return
		}
	}
	// FIX-1 L13 / FIX-2 L-7: the preview applies the same guards as
	// creation — discount/price-edit permissions, below-cost
	// confirmation (nominal AND net of discounts/promos, FIX-2 M-3),
	// open shift, stock, expiry and the credit limit. A denied preview
	// returns an error instead of a discounted net the user could never
	// actually post.
	if in.InvoiceDiscount > 0 {
		if err := rbac.Require(u.Role, rbac.PermGiveDiscount); err != nil {
			apiErr(w, http.StatusForbidden, err)
			return
		}
	}
	if in.InvoiceDiscount < 0 {
		apiErr(w, http.StatusBadRequest, fmt.Errorf("خصم الفاتورة غير صحيح"))
		return
	}
	var openShift int64
	if err := s.Store.DB.QueryRow(`SELECT ShiftID FROM Shifts WHERE UserID=? AND ShiftDate=? AND IsOpen=1 ORDER BY ShiftID DESC LIMIT 1`,
		u.ID, store.Today()).Scan(&openShift); err != nil {
		apiErr(w, http.StatusBadRequest, fmt.Errorf("لا يمكن تنفيذ هذه العملية بدون شيفت مفتوح لنفس المستخدم في تاريخ اليوم"))
		return
	}
	// Build promo lines from the cart (prices default by sale type).
	// Per-addition checks (price edit, below cost, discount, stock,
	// expiry) run on each raw line, exactly like creation; the merge
	// below then applies the FIX-3 duplicate rule so the preview and
	// the saved invoice agree line for line.
	var lines []store.PromoLine
	stockByProduct := map[int64]money.Qty{}
	for _, raw := range in.Lines {
		p, err := s.Store.GetProduct(r.Context(), u, raw.ProductID)
		if err != nil {
			apiErrForService(w, r, err)
			return
		}
		stockByProduct[raw.ProductID] = p.Quantity
		unit := raw.UnitPrice
		if unit == 0 {
			if raw.IsWeighted || p.IsWeighted {
				unit = p.PricePerKg
			} else {
				unit = store.PriceForSaleType(p, in.SaleType)
			}
		}
		if unit != store.PriceForSaleType(p, in.SaleType) {
			if !(raw.IsWeighted || p.IsWeighted) || unit != p.PricePerKg {
				if err := rbac.Require(u.Role, rbac.PermEditPrice); err != nil {
					apiErr(w, http.StatusForbidden, err)
					return
				}
			}
		}
		if unit < p.BuyPrice && !in.AllowBelowCost {
			// FIX-17 SW2-L11: a role that can never confirm gets the
			// denial, not a dead-end prompt.
			if err := rbac.Require(u.Role, rbac.PermApproveBelowCost); err != nil {
				apiErr(w, http.StatusForbidden, err)
				return
			}
			apiErr(w, http.StatusBadRequest, fmt.Errorf("السعر أقل من سعر الشراء — هل تريد البيع بخسارة؟"))
			return
		}
		if raw.ItemDiscount > 0 {
			if err := rbac.Require(u.Role, rbac.PermGiveDiscount); err != nil {
				apiErr(w, http.StatusForbidden, err)
				return
			}
		}
		if raw.Qty > p.Quantity {
			apiErr(w, http.StatusBadRequest, fmt.Errorf("الكمية المطلوبة تتجاوز المخزون (%s)", money.FormatQty(p.Quantity)))
			return
		}
		if p.ExpiryDate != "" {
			exp := p.ExpiryDate
			if len(exp) > 10 {
				exp = exp[:10]
			}
			if exp < store.Today() {
				apiErr(w, http.StatusBadRequest, fmt.Errorf("لا يمكن بيع منتج منتهي الصلاحية %s — تاريخ الانتهاء: %s", p.Name, exp))
				return
			}
		}
		lineGross, lerr := money.MulQty(unit, raw.Qty)
		if lerr != nil {
			apiErr(w, http.StatusBadRequest, lerr)
			return
		}
		disc := raw.ItemDiscount
		if disc > lineGross {
			disc = lineGross
		}
		lines = append(lines, store.PromoLine{ProductID: raw.ProductID, Category: p.Category,
			Qty: raw.Qty, UnitPrice: unit, LineGross: lineGross, ItemDiscount: disc,
			LineTotal: lineGross - disc, BuyPrice: p.BuyPrice})
	}
	// FIX-3 duplicate rule: one line per product in the preview too.
	lines, merr := store.MergePromoLines(lines)
	if merr != nil {
		apiErr(w, http.StatusBadRequest, merr)
		return
	}
	var gross money.Cents
	for _, ln := range lines {
		var gerr error
		gross, gerr = money.AddChecked(gross, ln.LineTotal)
		if gerr != nil {
			apiErr(w, http.StatusBadRequest, gerr)
			return
		}
		// Total stock check on the MERGED quantity (a split addition
		// could pass the per-line check while exceeding stock in total).
		if ln.Qty > stockByProduct[ln.ProductID] {
			apiErr(w, http.StatusBadRequest, fmt.Errorf("الكمية المطلوبة تتجاوز المخزون (%s)", money.FormatQty(stockByProduct[ln.ProductID])))
			return
		}
	}
	promo, err := s.Store.EvaluateOffers(r.Context(), lines)
	if err != nil {
		apiErrForService(w, r, err)
		return
	}
	invPromo := promo.InvoiceBest
	rem := gross - promo.LinePromoTotal - in.InvoiceDiscount
	if rem < 0 {
		rem = 0
	}
	if invPromo > rem {
		invPromo = rem
	}
	// FIX-2 M-3: the below-cost gate runs on the NET line total here
	// too — an item discount, the invoice discount or a promo can push
	// a line whose nominal price is above BuyPrice below cost, and the
	// preview must refuse it exactly like creation does.
	{
		bases := make([]money.Cents, len(lines))
		for i, ln := range lines {
			b := ln.LineTotal - promo.LinePromos[i]
			if b < 0 {
				b = 0
			}
			bases[i] = b
		}
		effInvDiscount := in.InvoiceDiscount
		if cap := gross - promo.LinePromoTotal; cap < 0 {
			cap = 0
		} else if effInvDiscount > cap {
			effInvDiscount = cap
		}
		invPromoShares, aerr := store.AllocProRata(invPromo, bases)
		if aerr != nil {
			apiErr(w, http.StatusBadRequest, aerr)
			return
		}
		invDiscShares, aerr := store.AllocProRata(effInvDiscount, bases)
		if aerr != nil {
			apiErr(w, http.StatusBadRequest, aerr)
			return
		}
		for i, ln := range lines {
			netLine := bases[i] - invPromoShares[i] - invDiscShares[i]
			if netLine < 0 {
				netLine = 0
			}
			cost, cerr := money.MulQty(ln.BuyPrice, ln.Qty)
			if cerr != nil {
				apiErr(w, http.StatusBadRequest, cerr)
				return
			}
			if netLine < cost && !in.AllowBelowCost {
				// FIX-17 SW2-L11: same upfront denial as the unit-price
				// path — a role that can never confirm never sees the
				// prompt.
				if err := rbac.Require(u.Role, rbac.PermApproveBelowCost); err != nil {
					apiErr(w, http.StatusForbidden, err)
					return
				}
				apiErr(w, http.StatusBadRequest, fmt.Errorf("السعر أقل من سعر الشراء — هل تريد البيع بخسارة؟"))
				return
			}
		}
	}
	net := gross - promo.LinePromoTotal - invPromo - in.InvoiceDiscount
	if net < 0 {
		net = 0
	}
	// FIX-2 L-7: the preview applies the same credit-limit guard as
	// creation — a preview that could never post must not show a net.
	{
		customerID := in.CustomerID
		if customerID == 0 {
			customerID = 1
		}
		paid := in.Paid
		// FIX-12 SW-H05: same rule as CreateInvoice — a supplied cash
		// amount that is negative, above net, or below net (but
		// non-zero) is refused instead of silently rewritten to net.
		// paid == 0 keeps the S-19 auto-pay-full convention.
		if customerID == 1 && paid != 0 && paid != net {
			apiErr(w, http.StatusBadRequest, fmt.Errorf("المبلغ المدفوع غير صحيح"))
			return
		}
		if customerID == 1 {
			// The cash customer always pays the full net (S-19).
			paid = net
		}
		if paid < 0 || paid > net {
			apiErr(w, http.StatusBadRequest, fmt.Errorf("المبلغ المدفوع غير صحيح"))
			return
		}
		if remaining := net - paid; remaining > 0 {
			if customerID <= 1 {
				apiErr(w, http.StatusBadRequest, fmt.Errorf("يجب اختيار عميل مسجل لتسجيل المديونية"))
				return
			}
			// -1 = no such (or inactive) customer; a credit limit is
			// never negative, so it is a safe sentinel.
			var creditLimit money.Cents
			if err := s.Store.DB.QueryRow(`SELECT COALESCE((SELECT CreditLimit FROM Customers WHERE CustomerID=? AND IsActive=1),-1)`, customerID).Scan(&creditLimit); err != nil {
				apiErrForService(w, r, err)
				return
			}
			if creditLimit < 0 {
				apiErr(w, http.StatusBadRequest, fmt.Errorf("العميل غير موجود"))
				return
			}
			var liveBal money.Cents
			if err := s.Store.DB.QueryRow(`SELECT COALESCE(SUM(RemainingAmount),0) FROM Invoices
				WHERE CustomerID=? AND IsCanceled=0 AND RemainingAmount>0`, customerID).Scan(&liveBal); err != nil {
				apiErrForService(w, r, err)
				return
			}
			if creditLimit > 0 && liveBal+remaining > creditLimit && !in.AllowOverLimit {
				apiErr(w, http.StatusBadRequest, fmt.Errorf("تجاوز حد الائتمان يحتاج موافقة المدير"))
				return
			}
		}
	}
	offers := make([]map[string]any, 0, len(promo.Applied))
	for _, o := range promo.Applied {
		offers = append(offers, map[string]any{"offer_id": o.OfferID, "name": o.Name, "saved": int64(o.Saved)})
	}
	if promo.InvoiceBestOffer != nil && invPromo > 0 {
		offers = append(offers, map[string]any{"offer_id": promo.InvoiceBestOffer.ID,
			"name": promo.InvoiceBestOffer.Name, "saved": int64(invPromo)})
	}
	free := map[string]int64{}
	for pid, q := range promo.FreeUnits {
		free[strconv.FormatInt(pid, 10)] = int64(q)
	}
	// FIX-15 C1 (coordinator extension of R15-L2): free_cost is the buy
	// cost of the free units — with free_units (productID -> qty) in the
	// same response, free_cost / qty is the unit buy price. Omit it for
	// roles without view_profit, same rule as saleResultJSON. No shipped
	// client reads free_cost (no template/JS reference).
	resp := map[string]any{
		"ok": true, "gross": int64(gross), "line_promo": int64(promo.LinePromoTotal),
		"invoice_promo": int64(invPromo), "net": int64(net),
		"applied_offers": offers, "free_units": free,
	}
	if u != nil && rbac.Can(u.Role, rbac.PermViewProfit) {
		resp["free_cost"] = int64(promo.FreeCost)
	}
	WriteJSON(w, http.StatusOK, resp)
}

func (s *Server) handleAPISaleGet(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	inv, err := s.Store.GetInvoice(r.Context(), CurrentUser(r), id)
	if err != nil {
		// FIX-3 M4: another user's invoice is a 403, not a 404.
		if errors.Is(err, store.ErrInvoiceForbidden) {
			apiErr(w, http.StatusForbidden, err)
			return
		}
		apiErrForService(w, r, err)
		return
	}
	lines := make([]map[string]any, 0, len(inv.Lines))
	// FIX-12 SW-H09: the invoice-line buy price is omitted for roles
	// without view_profit (cashier/warehouse) — the sale-detail profit
	// line is already gated on the same permission.
	u := CurrentUser(r)
	canViewProfit := u != nil && rbac.Can(u.Role, rbac.PermViewProfit)
	for _, ln := range inv.Lines {
		lm := map[string]any{
			"product_id": ln.ProductID, "product_name": ln.ProductName,
			"quantity": int64(ln.Qty), "unit_price": int64(ln.UnitPrice),
			"item_discount": int64(ln.ItemDiscount),
			"promo_discount": int64(ln.PromoDiscount), "free_qty": int64(ln.FreeQty),
			"total": int64(ln.TotalPrice), "is_weighted": ln.IsWeighted,
		}
		if canViewProfit {
			lm["buy_price"] = int64(ln.BuyPrice)
		}
		lines = append(lines, lm)
	}
	offers := make([]map[string]any, 0, len(inv.Offers))
	for _, o := range inv.Offers {
		offers = append(offers, map[string]any{"offer_id": o.OfferID, "name": o.Name, "saved": int64(o.Saved)})
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"ok": true, "id": inv.Summary.ID, "number": inv.Summary.Number,
		"customer_id": inv.Summary.CustomerID, "customer_name": inv.Summary.CustomerName,
		"sale_type": inv.Summary.SaleType, "total": int64(inv.Summary.Total),
		"discount": int64(inv.Summary.Discount), "promo_discount": int64(inv.Summary.PromoDiscount),
		"net": int64(inv.Summary.Net), "paid": int64(inv.Summary.Paid),
		"remaining": int64(inv.Summary.Remaining), "payment_method": inv.Summary.PaymentMethod,
		"user": inv.Summary.UserName, "date": inv.Summary.Date,
		"is_canceled": inv.Summary.IsCanceled, "notes": inv.Notes,
		"lines": lines, "offers": offers,
	})
}

func (s *Server) handleAPISaleCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiErr(w, http.StatusMethodNotAllowed, fmt.Errorf("POST فقط"))
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if strings.TrimSpace(body.Reason) == "" {
		body.Reason = r.FormValue("reason")
	}
	refund, err := s.Store.CancelInvoice(r.Context(), CurrentUser(r), id, body.Reason)
	if err != nil {
		apiErrForService(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "invoice_id": id, "cash_refund": int64(refund)})
}

func (s *Server) handleAPIProductSearch(w http.ResponseWriter, r *http.Request) {
	prods, err := s.Store.SearchProducts(r.Context(), CurrentUser(r), r.URL.Query().Get("q"))
	if err != nil {
		apiErrForService(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(prods))
	for _, p := range prods {
		out = append(out, productJSON(p, CurrentUser(r)))
	}
	WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "products": out})
}

func (s *Server) handleAPIProductBarcode(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	p, dec, err := s.Store.GetProductByBarcode(r.Context(), CurrentUser(r), code)
	if err != nil {
		apiErrForService(w, r, err)
		return
	}
	res := map[string]any{"ok": true, "product": productJSON(p, CurrentUser(r))}
	if dec != nil {
		res["scale"] = map[string]any{
			"weight": int64(dec.Weight), "total_price": int64(dec.TotalPrice),
			"mode": dec.Mode, "plu": dec.PLU,
		}
	}
	WriteJSON(w, http.StatusOK, res)
}

func (s *Server) handleAPIProductGet(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	p, err := s.Store.GetProduct(r.Context(), CurrentUser(r), id)
	if err != nil {
		apiErrForService(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "product": productJSON(p, CurrentUser(r))})
}

func (s *Server) handleAPIOffersList(w http.ResponseWriter, r *http.Request) {
	offers, err := s.Store.ListOffers(r.Context(), CurrentUser(r), r.URL.Query().Get("status"))
	if err != nil {
		apiErrForService(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(offers))
	for _, o := range offers {
		out = append(out, map[string]any{
			"id": o.ID, "name": o.Name, "type": o.Type, "type_ar": store.OfferTypeArabic(o.Type),
			"value": o.Value, "threshold": o.Threshold, "free_qty": o.FreeQty,
			"start_at": o.StartAt, "end_at": o.EndAt, "is_active": o.IsActive,
			"status": store.OfferStatus(o, store.Now()),
		})
	}
	WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "offers": out})
}

func (s *Server) handleAPIStocktakeGet(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	st, err := s.Store.GetStocktake(r.Context(), CurrentUser(r), id)
	if err != nil {
		apiErrForService(w, r, err)
		return
	}
	lines := make([]map[string]any, 0, len(st.Lines))
	for _, ln := range st.Lines {
		lines = append(lines, map[string]any{
			"product_id": ln.ProductID, "product_name": ln.ProductName,
			"system_qty": int64(ln.SystemQty), "counted_qty": int64(ln.CountedQty),
			"difference": int64(ln.Difference),
		})
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"ok": true, "id": st.ID, "status": st.Status, "created_by": st.CreatedBy,
		"created_at": st.CreatedAt, "closed_at": st.ClosedAt, "approved_at": st.ApprovedAt,
		"notes": st.Notes, "lines": lines,
	})
}
