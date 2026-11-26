// Package web — B2: shifts / drawer / cash count / returns / expenses /
// daily close handlers.
//
// All routes are registered by the single function
// registerShiftsRoutes, which routes.go (B5) calls from Server.routes().
// Every mutating service call re-checks permissions in the service layer;
// handlers add RequireLogin / RequirePerm as a UI gate only.
// Amounts in HTML forms are EGP decimals ("12.50"); JSON APIs accept
// either "amount": "12.50" (EGP string) or "amount_cents": 1250.
// Template convention (B5, current): each page file is its own
// top-level template wrapped in chrome-top / chrome-bottom — no shared
// {{define "content"}}.
package web

import (
	"bytes"
	"encoding/csv"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cashierpos-go/internal/money"
	"cashierpos-go/internal/rbac"
	"cashierpos-go/internal/store"
)

// registerShiftsRoutes registers every B2 HTML page and JSON API route.
// It is called from routes.go (B5 wiring); do not call it twice.
func (s *Server) registerShiftsRoutes(m *http.ServeMux) {
	// --- Shifts + drawer (HTML) ---
	m.HandleFunc("GET /shifts", s.RequireLogin(s.shiftsPage))
	m.HandleFunc("GET /shifts/{id}", s.RequireLogin(s.shiftDetailPage))
	m.HandleFunc("POST /shifts/open", s.RequirePerm("open_shift", s.shiftOpenSubmit))
	m.HandleFunc("POST /shifts/close", s.RequireLogin(s.shiftCloseSubmit))
	m.HandleFunc("POST /cashbox/in", s.RequirePerm("move_cashbox", s.cashBoxInSubmit))
	m.HandleFunc("POST /cashbox/out", s.RequirePerm("move_cashbox", s.cashBoxOutSubmit))
	m.HandleFunc("POST /cashbox/count", s.RequirePerm("enter_cash_count", s.cashCountSubmit))
	m.HandleFunc("POST /cashbox/count/{id}/review", s.RequirePerm("review_cash_count", s.cashCountReviewSubmit))
	m.HandleFunc("GET /cashcount-approvals", s.RequirePerm("view_cashcount_approvals", s.cashcountApprovalsPage))
	// --- Returns (HTML) ---
	m.HandleFunc("GET /returns", s.RequirePerm("do_return", s.returnsPage))
	m.HandleFunc("POST /returns", s.RequirePerm("do_return", s.returnSubmit))
	m.HandleFunc("POST /returns/all", s.RequirePerm("do_return", s.returnAllSubmit))
	m.HandleFunc("GET /returns/analysis", s.RequirePerm("view_return_analysis", s.returnsAnalysisPage))
	// --- Expenses (HTML) ---
	m.HandleFunc("GET /expenses", s.RequirePerm("register_expense", s.expensesPage))
	m.HandleFunc("POST /expenses", s.RequirePerm("register_expense", s.expenseCreateSubmit))
	m.HandleFunc("POST /expenses/{id}/edit", s.RequirePerm("register_expense", s.expenseEditSubmit))
	m.HandleFunc("POST /expenses/{id}/delete", s.RequirePerm("register_expense", s.expenseDeleteSubmit))
	m.HandleFunc("GET /expenses/export", s.RequirePerm("register_expense", s.expensesExport))
	// --- Daily close (HTML) ---
	m.HandleFunc("GET /dailyclose", s.RequirePerm("access_daily_close", s.dailyClosePage))

	// --- JSON API: shifts ---
	m.HandleFunc("POST /api/shifts/open", s.RequirePerm("open_shift", s.apiShiftOpen))
	m.HandleFunc("POST /api/shifts/close", s.RequireLogin(s.apiShiftClose))
	m.HandleFunc("GET /api/shifts/status", s.RequireLogin(s.apiShiftStatus))
	m.HandleFunc("GET /api/shifts/summary", s.RequireLogin(s.apiShiftSummary))
	m.HandleFunc("GET /api/shifts", s.RequireLogin(s.apiShiftsList))
	// --- JSON API: cashbox + cash count ---
	m.HandleFunc("POST /api/cashbox/in", s.RequirePerm("move_cashbox", s.apiCashBoxIn))
	m.HandleFunc("POST /api/cashbox/out", s.RequirePerm("move_cashbox", s.apiCashBoxOut))
	m.HandleFunc("GET /api/cashbox", s.RequirePerm("move_cashbox", s.apiCashBoxList))
	m.HandleFunc("POST /api/cashbox/count", s.RequirePerm("enter_cash_count", s.apiCashCountCreate))
	m.HandleFunc("POST /api/cashbox/count/review", s.RequirePerm("review_cash_count", s.apiCashCountReview))
	m.HandleFunc("GET /api/cashbox/counts", s.RequireLogin(s.apiCashCountsList))
	m.HandleFunc("GET /api/cashcount-approvals", s.RequirePerm("view_cashcount_approvals", s.apiCashcountApprovals))
	// --- JSON API: returns ---
	m.HandleFunc("POST /api/returns", s.RequirePerm("do_return", s.apiReturnCreate))
	m.HandleFunc("POST /api/returns/all", s.RequirePerm("do_return", s.apiReturnAll))
	m.HandleFunc("GET /api/returns", s.RequirePerm("do_return", s.apiReturnsList))
	m.HandleFunc("GET /api/returns/analysis", s.RequirePerm("view_return_analysis", s.apiReturnsAnalysis))
	// --- JSON API: expenses ---
	m.HandleFunc("POST /api/expenses", s.RequirePerm("register_expense", s.apiExpenseCreate))
	m.HandleFunc("GET /api/expenses", s.RequirePerm("register_expense", s.apiExpensesList))
	m.HandleFunc("PUT /api/expenses/{id}", s.RequirePerm("register_expense", s.apiExpenseUpdate))
	m.HandleFunc("DELETE /api/expenses/{id}", s.RequirePerm("register_expense", s.apiExpenseDelete))
	// --- JSON API: daily close + advanced reports ---
	m.HandleFunc("GET /api/dailyclose", s.RequirePerm("access_daily_close", s.apiDailyClose))
	m.HandleFunc("GET /api/dailyclose/report", s.RequirePerm("view_reports", s.apiDailyCloseReport))
}

// ---------------------------------------------------------------- helpers

func (s *Server) shiftsPageData(r *http.Request) (map[string]any, error) {
	u := CurrentUser(r)
	ctx := r.Context()
	data := map[string]any{"Date": store.Today()}
	st, err := s.Store.GetShiftStatus(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	data["Status"] = st
	open, _ := s.Store.OpenShiftForUser(ctx, u.ID)
	data["OpenShift"] = open
	if open != nil {
		sum, err := s.Store.ShiftSummary(ctx, open.ID)
		if err != nil {
			return nil, err
		}
		data["Summary"] = sum
		counts, _ := s.Store.ListCashCounts(ctx, u, open.ID)
		data["CashCounts"] = counts
	}
	movements, _ := s.Store.ListCashBoxMovements(ctx, u, "", 0)
	data["Movements"] = movements
	// PEER APPROVAL (jony 2026-11-25): reviewers see every pending
	// count (any shift, any user) so a peer cashier or a manager can
	// review a count that is not on their own shift.
	data["CanReviewCashCount"] = rbac.Can(u.Role, rbac.PermReviewCashCount)
	if rbac.Can(u.Role, rbac.PermReviewCashCount) {
		pending, _ := s.Store.ListPendingCashCounts(ctx, u)
		data["PendingCounts"] = pending
	}
	expenses, _ := s.Store.ListExpenses(ctx, u, "", "")
	data["Expenses"] = expenses
	shifts, _ := s.Store.ListShiftsByDate(ctx, store.Today())
	if !rbac.Can(u.Role, rbac.PermViewAllCashBox) {
		var own []*store.Shift
		for _, sh := range shifts {
			if sh.UserID == u.ID {
				own = append(own, sh)
			}
		}
		shifts = own
	}
	// PR10-L2: open shifts show the live summary's Expected, not
	// the stored 0.00 — the same override DailyClose applies
	// (shared helper, so the tables cannot drift apart). Best
	// effort on the page: a summary error leaves the stored
	// values in place and is logged, rather than failing the
	// whole page; the API below is strict instead.
	if err := s.Store.ApplyLiveSummaryToOpenShifts(ctx, shifts); err != nil {
		LogErr(r, err)
	}
	data["ShiftsToday"] = shifts
	return data, nil
}

// ---------------------------------------------------------------- shifts HTML

func (s *Server) shiftsPage(w http.ResponseWriter, r *http.Request) {
	data, err := s.shiftsPageData(r)
	if err != nil {
		LogErr(r, err)
		_ = s.render(w, r, "shifts.html", Page{Title: "الشيفت والدرج", Error: "تعذّر تحميل الشيفت"})
		return
	}
	_ = s.render(w, r, "shifts.html", Page{Title: "الشيفت والدرج", ActiveNav: "shifts",
		Notice: noticeFromQuery(r), Data: data})
}

func (s *Server) shiftDetailPage(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	u := CurrentUser(r)
	sh, err := s.Store.GetShift(r.Context(), id)
	if err != nil {
		_ = s.render(w, r, "shifts.html", Page{Title: "الشيفت والدرج", Error: err.Error()})
		return
	}
	if sh.UserID != u.ID && !rbac.Can(u.Role, rbac.PermViewAllCashBox) {
		_ = s.render(w, r, "shifts.html", Page{Title: "الشيفت والدرج", Error: "لا يمكن عرض شيفت مستخدم آخر"})
		return
	}
	sum, err := s.Store.ShiftSummary(r.Context(), id)
	if err != nil {
		LogErr(r, err)
		_ = s.render(w, r, "shifts.html", Page{Title: "الشيفت والدرج", Error: "تعذّر تحميل ملخص الشيفت"})
		return
	}
	movements, _ := s.Store.ListCashBoxMovements(r.Context(), u, sh.ShiftDate, sh.UserID)
	counts, _ := s.Store.ListCashCounts(r.Context(), u, id)
	_ = s.render(w, r, "shifts_detail.html", Page{Title: "شيفت #" + strconv.FormatInt(id, 10),
		Notice: noticeFromQuery(r), Data: map[string]any{
			"Shift": sh, "Summary": sum, "Movements": movements, "CashCounts": counts}})
}

func (s *Server) shiftOpenSubmit(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	opening := money.Cents(0)
	if v := strings.TrimSpace(r.FormValue("opening")); v != "" {
		amt, err := money.ParseMoney(v)
		if err != nil {
			_ = s.render(w, r, "shifts.html", Page{Title: "الشيفت والدرج", Error: err.Error()})
			return
		}
		opening = amt
	}
	resume := r.FormValue("resume") == "1" || r.FormValue("resume") == "on"
	id, err := s.Store.OpenShift(r.Context(), u, opening, resume)
	if err != nil {
		data, _ := s.shiftsPageData(r)
		if data == nil {
			data = map[string]any{}
		}
		data["ResumeOffer"] = strings.Contains(err.Error(), "استكمال")
		_ = s.render(w, r, "shifts.html", Page{Title: "الشيفت والدرج", Error: err.Error(), Data: data})
		return
	}
	notice := "تم فتح الشيفت"
	if resume {
		notice = "تم استكمال يوم العمل"
	}
	s.redirectWithNotice(w, r, "/shifts/"+strconv.FormatInt(id, 10), notice)
}

func (s *Server) shiftCloseSubmit(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	var shiftID int64
	if v := strings.TrimSpace(r.FormValue("shift_id")); v != "" {
		shiftID, _ = strconv.ParseInt(v, 10, 64)
	}
	sum, err := s.Store.CloseShift(r.Context(), u, shiftID)
	if err != nil {
		data, _ := s.shiftsPageData(r)
		if data == nil {
			data = map[string]any{}
		}
		_ = s.render(w, r, "shifts.html", Page{Title: "الشيفت والدرج", Error: err.Error(), Data: data})
		return
	}
	s.redirectWithNotice(w, r, "/shifts", "تم إغلاق الشيفت — المتوقع "+money.FormatEGP(sum.ExpectedBalance)+" (متوازن)")
}

func (s *Server) cashBoxInSubmit(w http.ResponseWriter, r *http.Request) {
	s.cashBoxSubmit(w, r, "in")
}

func (s *Server) cashBoxOutSubmit(w http.ResponseWriter, r *http.Request) {
	s.cashBoxSubmit(w, r, "out")
}

func (s *Server) cashBoxSubmit(w http.ResponseWriter, r *http.Request, kind string) {
	u := CurrentUser(r)
	amount, err := formMoney(r, "amount")
	if err != nil {
		_ = s.render(w, r, "shifts.html", Page{Title: "الشيفت والدرج", Error: err.Error()})
		return
	}
	reason := strings.TrimSpace(r.FormValue("reason"))
	var shiftID int64
	if v := strings.TrimSpace(r.FormValue("shift_id")); v != "" {
		shiftID, _ = strconv.ParseInt(v, 10, 64)
	}
	if kind == "in" {
		_, err = s.Store.CashBoxIn(r.Context(), u, shiftID, amount, reason)
	} else {
		_, err = s.Store.CashBoxOut(r.Context(), u, shiftID, amount, reason)
	}
	if err != nil {
		data, _ := s.shiftsPageData(r)
		if data == nil {
			data = map[string]any{}
		}
		_ = s.render(w, r, "shifts.html", Page{Title: "الشيفت والدرج", Error: err.Error(), Data: data})
		return
	}
	notice := "تمت الإضافة للدرج"
	if kind == "out" {
		notice = "تم السحب من الدرج"
	}
	s.redirectWithNotice(w, r, "/shifts", notice)
}

func (s *Server) cashCountSubmit(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	shiftID, _ := strconv.ParseInt(r.FormValue("shift_id"), 10, 64)
	amount, err := formMoney(r, "amount")
	if err != nil {
		_ = s.render(w, r, "shifts.html", Page{Title: "الشيفت والدرج", Error: err.Error()})
		return
	}
	cc, err := s.Store.RecordCashCount(r.Context(), u, shiftID, amount,
		strings.TrimSpace(r.FormValue("denominations")), strings.TrimSpace(r.FormValue("notes")))
	if err != nil {
		data, _ := s.shiftsPageData(r)
		if data == nil {
			data = map[string]any{}
		}
		_ = s.render(w, r, "shifts.html", Page{Title: "الشيفت والدرج", Error: err.Error(), Data: data})
		return
	}
	s.redirectWithNotice(w, r, "/shifts", "تم تسجيل العدّ — الفرق "+money.FormatEGP(cc.Difference)+" ("+cc.DifferenceStatus+")")
}

func (s *Server) cashCountReviewSubmit(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	approve := r.FormValue("approve") == "1" || r.FormValue("approve") == "on"
	_, err = s.Store.ReviewCashCount(r.Context(), u, id, approve, strings.TrimSpace(r.FormValue("note")))
	if err != nil {
		data, _ := s.shiftsPageData(r)
		if data == nil {
			data = map[string]any{}
		}
		_ = s.render(w, r, "shifts.html", Page{Title: "الشيفت والدرج", Error: err.Error(), Data: data})
		return
	}
	notice := "تم اعتماد العدّ"
	if !approve {
		notice = "تم رفض العدّ"
	}
	s.redirectWithNotice(w, r, "/shifts", notice)
}

// ---------------------------------------------------------------- cash-count approvals log (peer approval, jony 2026-11-25)

var arabicMonths = [...]string{"يناير", "فبراير", "مارس", "أبريل", "مايو", "يونيو",
	"يوليو", "أغسطس", "سبتمبر", "أكتوبر", "نوفمبر", "ديسمبر"}

// cashcountMonthOption is one entry of the "last 12 months" dropdown.
type cashcountMonthOption struct {
	Value string
	Label string
}

// last12Months returns (label, value) pairs for the last 12 months,
// newest first — label "نوفمبر 2026", value "2026-11".
func last12Months() []cashcountMonthOption {
	now := time.Now()
	year, month := now.Year(), int(now.Month())
	out := make([]cashcountMonthOption, 0, 12)
	for i := 0; i < 12; i++ {
		out = append(out, cashcountMonthOption{
			Value: time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC).Format("2006-01"),
			Label: arabicMonths[month-1] + " " + strconv.Itoa(year),
		})
		month--
		if month == 0 {
			month, year = 12, year-1
		}
	}
	return out
}

// cashcountFilterArgs reads the log filter from the query: mode is one
// of day (default) / month / custom, and only the active mode's values
// are returned as service args (the other three stay empty, so the
// service sees exactly one filter mode).
func cashcountFilterArgs(q url.Values) (mode, day, month, from, to string) {
	mode = q.Get("mode")
	day = strings.TrimSpace(q.Get("day"))
	month = strings.TrimSpace(q.Get("month"))
	from = strings.TrimSpace(q.Get("from"))
	to = strings.TrimSpace(q.Get("to"))
	switch mode {
	case "month":
		if month == "" {
			month = store.Today()[:7]
		}
		return mode, "", month, "", ""
	case "custom":
		// FIX-3 L4: missing bounds are passed through empty, so the
		// store's "both bounds required" error surfaces instead of
		// silently defaulting to today.
		return mode, "", "", from, to
	default:
		mode = "day"
		if day == "" {
			day = store.Today()
		}
		return mode, day, "", "", ""
	}
}

func (s *Server) cashcountApprovalsPage(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	mode, day, month, from, to := cashcountFilterArgs(r.URL.Query())
	reviews, err := s.Store.ListCashcountReviews(r.Context(), u, day, month, from, to)
	if err != nil {
		_ = s.render(w, r, "cashcount_log.html", Page{Title: "سجل اعتماد العدّات", Error: err.Error(),
			Data: map[string]any{"Mode": mode, "Day": day, "Month": month,
				"Months": last12Months(), "From": from, "To": to}})
		return
	}
	if r.URL.Query().Get("format") == "csv" {
		var buf bytes.Buffer
		buf.WriteString("\xef\xbb\xbf") // UTF-8 BOM for Excel
		cw := csv.NewWriter(&buf)
		_ = cw.Write([]string{"الوقت", "الكاشير", "المعتمد", "الشيفت", "المتوقع", "المعدود", "الفرق", "القرار"})
		for _, rev := range reviews {
			decision := "مرفوض"
			if rev.Decision == "approved" {
				decision = "معتمد"
			}
			_ = cw.Write([]string{rev.ReviewedAt, rev.Owner, rev.Approver, strconv.FormatInt(rev.ShiftID, 10),
				money.FormatEGP(rev.Expected), money.FormatEGP(rev.Counted), money.FormatEGP(rev.Difference), decision})
		}
		cw.Flush()
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=cashcount-approvals.csv")
		_, _ = w.Write(buf.Bytes())
		return
	}
	_ = s.render(w, r, "cashcount_log.html", Page{Title: "سجل اعتماد العدّات", ActiveNav: "shifts",
		Notice: noticeFromQuery(r), Data: map[string]any{
			"Reviews": reviews, "Mode": mode, "Day": day, "Month": month,
			"Months": last12Months(), "From": from, "To": to}})
}

func (s *Server) apiCashcountApprovals(w http.ResponseWriter, r *http.Request) {
	mode, day, month, from, to := cashcountFilterArgs(r.URL.Query())
	reviews, err := s.Store.ListCashcountReviews(r.Context(), CurrentUser(r), day, month, from, to)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	apiOK(w, map[string]any{"reviews": reviews, "mode": mode,
		"day": day, "month": month, "from": from, "to": to})
}

// ---------------------------------------------------------------- returns HTML

func (s *Server) returnsPage(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	list, err := s.Store.ListReturns(r.Context(), u, from, to)
	if err != nil {
		LogErr(r, err)
		_ = s.render(w, r, "returns.html", Page{Title: "المرتجعات", Error: "تعذّر تحميل المرتجعات"})
		return
	}
	_ = s.render(w, r, "returns.html", Page{Title: "المرتجعات", ActiveNav: "returns",
		Notice: noticeFromQuery(r), Data: map[string]any{"Returns": list, "From": from, "To": to}})
}

func parseReturnLines(r *http.Request) ([]store.ReturnLineInput, error) {
	prodIDs := r.Form["product_id"]
	qtys := r.Form["qty"]
	merged := map[int64]money.Qty{}
	var order []int64
	for i := range prodIDs {
		pid, err := strconv.ParseInt(strings.TrimSpace(prodIDs[i]), 10, 64)
		if err != nil || pid == 0 {
			continue
		}
		var q money.Qty
		if i < len(qtys) {
			q, err = money.ParseQty(qtys[i])
			if err != nil {
				return nil, err
			}
		}
		if q > 0 {
			// FIX-1 M3: merge duplicate rows for the same product
			// before the service check (sold − returned).
			if _, ok := merged[pid]; !ok {
				order = append(order, pid)
			}
			merged[pid] += q
		}
	}
	lines := make([]store.ReturnLineInput, 0, len(order))
	for _, pid := range order {
		lines = append(lines, store.ReturnLineInput{ProductID: pid, Qty: merged[pid]})
	}
	return lines, nil
}

func (s *Server) returnSubmit(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	invID, err := strconv.ParseInt(r.FormValue("invoice_id"), 10, 64)
	if err != nil {
		_ = s.render(w, r, "returns.html", Page{Title: "المرتجعات", Error: "رقم الفاتورة مطلوب"})
		return
	}
	lines, err := parseReturnLines(r)
	if err != nil {
		_ = s.render(w, r, "returns.html", Page{Title: "المرتجعات", Error: err.Error()})
		return
	}
	res, err := s.Store.ProcessReturn(r.Context(), u, invID, lines,
		r.FormValue("reason"), r.FormValue("method"))
	if err != nil {
		_ = s.render(w, r, "returns.html", Page{Title: "المرتجعات", Error: err.Error()})
		return
	}
	notice := "تم تنفيذ المرتجع — قيمة المرتجع " + money.FormatEGP(res.TotalRefund)
	for _, v := range res.VoidedFree {
		notice += " — وحدات مجانية مستردة (تُسلَّم للكاشير): " + v.ProductName + " ×" + money.FormatQty(v.Qty)
	}
	s.redirectWithNotice(w, r, "/returns", notice)
}

func (s *Server) returnAllSubmit(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	invID, err := strconv.ParseInt(r.FormValue("invoice_id"), 10, 64)
	if err != nil {
		_ = s.render(w, r, "returns.html", Page{Title: "المرتجعات", Error: "رقم الفاتورة مطلوب"})
		return
	}
	res, err := s.Store.ProcessReturnAll(r.Context(), u, invID, r.FormValue("reason"), r.FormValue("method"))
	if err != nil {
		_ = s.render(w, r, "returns.html", Page{Title: "المرتجعات", Error: err.Error()})
		return
	}
	s.redirectWithNotice(w, r, "/returns", "تم تنفيذ المرتجع الكامل — قيمة المرتجع "+money.FormatEGP(res.TotalRefund))
}

func (s *Server) returnsAnalysisPage(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if r.URL.Query().Get("format") == "csv" {
		csv, err := s.Store.ReturnAnalysisCSV(r.Context(), u, from, to)
		if err != nil {
			apiErr(w, http.StatusBadRequest, err)
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=returns-analysis.csv")
		_, _ = w.Write([]byte("\xef\xbb\xbf" + csv)) // UTF-8 BOM for Excel
		return
	}
	rep, err := s.Store.GetReturnsAnalytics(r.Context(), u, from, to)
	if err != nil {
		_ = s.render(w, r, "returns_analysis.html", Page{Title: "تحليلات المرتجعات", Error: err.Error()})
		return
	}
	_ = s.render(w, r, "returns_analysis.html", Page{Title: "تحليلات المرتجعات",
		Data: map[string]any{"Report": rep, "From": from, "To": to}})
}

// ---------------------------------------------------------------- expenses HTML

func (s *Server) expensesPage(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	list, err := s.Store.ListExpenses(r.Context(), u, from, to)
	if err != nil {
		LogErr(r, err)
		_ = s.render(w, r, "expenses.html", Page{Title: "المصروفات", Error: "تعذّر تحميل المصروفات"})
		return
	}
	_ = s.render(w, r, "expenses.html", Page{Title: "المصروفات", ActiveNav: "expenses",
		Notice: noticeFromQuery(r), Data: map[string]any{"Expenses": list, "From": from, "To": to}})
}

func (s *Server) expenseCreateSubmit(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	amount, err := formMoney(r, "amount")
	if err != nil {
		_ = s.render(w, r, "expenses.html", Page{Title: "المصروفات", Error: err.Error()})
		return
	}
	_, err = s.Store.RegisterExpense(r.Context(), u, r.FormValue("description"), amount, r.FormValue("category"))
	if err != nil {
		list, _ := s.Store.ListExpenses(r.Context(), u, "", "")
		_ = s.render(w, r, "expenses.html", Page{Title: "المصروفات", Error: err.Error(),
			Data: map[string]any{"Expenses": list}})
		return
	}
	s.redirectWithNotice(w, r, "/expenses", "تم تسجيل المصروف")
}

func (s *Server) expenseEditSubmit(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	amount, err := formMoney(r, "amount")
	if err != nil {
		_ = s.render(w, r, "expenses.html", Page{Title: "المصروفات", Error: err.Error()})
		return
	}
	err = s.Store.UpdateExpense(r.Context(), u, id, r.FormValue("description"), amount, r.FormValue("category"))
	if err != nil {
		list, _ := s.Store.ListExpenses(r.Context(), u, "", "")
		_ = s.render(w, r, "expenses.html", Page{Title: "المصروفات", Error: err.Error(),
			Data: map[string]any{"Expenses": list}})
		return
	}
	s.redirectWithNotice(w, r, "/expenses", "تم حفظ التعديلات")
}

func (s *Server) expenseDeleteSubmit(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	id, err := pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.Store.DeleteExpense(r.Context(), u, id); err != nil {
		list, _ := s.Store.ListExpenses(r.Context(), u, "", "")
		_ = s.render(w, r, "expenses.html", Page{Title: "المصروفات", Error: err.Error(),
			Data: map[string]any{"Expenses": list}})
		return
	}
	s.redirectWithNotice(w, r, "/expenses", "تم حذف المصروف")
}

func (s *Server) expensesExport(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	list, err := s.Store.ListExpenses(r.Context(), u, from, to)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	var b strings.Builder
	b.WriteString("التاريخ,الوصف,الفئة,المبلغ (قرش),المستخدم,الشيفت\n")
	for _, e := range list {
		b.WriteString(e.ExpenseDate + "," + strings.ReplaceAll(e.Description, ",", "،") + "," +
			strings.ReplaceAll(e.Category, ",", "،") + "," + strconv.FormatInt(int64(e.Amount), 10) + "," +
			e.UserName + "," + strconv.FormatInt(e.ShiftID, 10) + "\n")
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=expenses.csv")
	_, _ = w.Write([]byte("\xef\xbb\xbf" + b.String()))
}

// ---------------------------------------------------------------- daily close HTML

func (s *Server) dailyClosePage(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	date := r.URL.Query().Get("date")
	if date == "" {
		date = store.Today()
	}
	rep, err := s.Store.DailyClose(r.Context(), u, date)
	if err != nil {
		_ = s.render(w, r, "dailyclose.html", Page{Title: "الإغلاق اليومي", Error: err.Error()})
		return
	}
	data := map[string]any{"Report": rep, "Date": date}
	// Advanced report (D-04).
	report := r.URL.Query().Get("report")
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if from == "" {
		from = date
	}
	if to == "" {
		to = date
	}
	data["From"] = from
	data["To"] = to
	data["ReportKind"] = report
	switch report {
	case "top", "bottom", "profit":
		rows, err := s.Store.TopProducts(r.Context(), u, from, to, 10, report == "bottom")
		if err == nil {
			data["TopProducts"] = rows
		}
	case "cashier":
		rows, err := s.Store.BestCashiers(r.Context(), u, from, to)
		if err == nil {
			data["Cashiers"] = rows
		}
	case "peak":
		rows, err := s.Store.PeakHours(r.Context(), u, from, to)
		if err == nil {
			data["PeakHours"] = rows
		}
	case "expiry":
		days := 30
		if v := r.URL.Query().Get("days"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				days = n
			}
		}
		rows, err := s.Store.ExpiryAlerts(r.Context(), u, days)
		if err == nil {
			data["ExpiryAlerts"] = rows
		}
		data["Days"] = days
	case "ledger":
		if pid, err := strconv.ParseInt(r.URL.Query().Get("product_id"), 10, 64); err == nil && pid > 0 {
			rows, err := s.Store.StockLedger(r.Context(), u, pid, from, to)
			if err == nil {
				data["StockLedger"] = rows
			}
			data["ProductID"] = pid
		}
	}
	_ = s.render(w, r, "dailyclose.html", Page{Title: "الإغلاق اليومي", ActiveNav: "dailyclose", Data: data})
}

// ---------------------------------------------------------------- JSON API: shifts

func (s *Server) apiShiftOpen(w http.ResponseWriter, r *http.Request) {
	body, err := readJSONBody(r)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	// Opening balance: "opening" (EGP string/number) or
	// "opening_cents" wins; "amount"/"amount_cents" accepted too;
	// omitted means 0.
	amount := money.Cents(0)
	switch v := body["opening"].(type) {
	case string:
		if strings.TrimSpace(v) != "" {
			amt, err := money.ParseMoney(v)
			if err != nil {
				apiErr(w, http.StatusBadRequest, err)
				return
			}
			amount = amt
		}
	case float64:
		// FIX-1 L1: parse the raw number with rounding instead of
		// truncating (int64(v*100) drops 0.99 of a qirsh).
		amount = money.Cents(int64(v*100 + 0.5))
	}
	if v, ok := body["opening_cents"]; ok {
		if f, ok := v.(float64); ok {
			// FIX-2 L-4: round like the numeric "opening" branch above
			// (int64(f) truncated 0.99 of a qirsh).
			amount = money.Cents(int64(f + 0.5))
		}
	} else if _, hasOpening := body["opening"]; !hasOpening {
		if a, err := apiAmount(body); err == nil {
			amount = a
		}
	}
	resume := false
	if v, ok := body["resume"]; ok {
		if b, ok := v.(bool); ok {
			resume = b
		}
	}
	id, err := s.Store.OpenShift(r.Context(), CurrentUser(r), amount, resume)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	apiOK(w, map[string]any{"shift_id": id})
}

func (s *Server) apiShiftClose(w http.ResponseWriter, r *http.Request) {
	body, _ := readJSONBody(r)
	var shiftID int64
	if body != nil {
		shiftID = intVal(body, "shift_id")
	}
	sum, err := s.Store.CloseShift(r.Context(), CurrentUser(r), shiftID)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	apiOK(w, map[string]any{"shift_id": sum.ShiftID,
		"expected_cents": int64(sum.ExpectedBalance), "actual_cents": int64(sum.ExpectedBalance),
		"difference_cents": 0, "difference_status": "متوازن", "summary": sum})
}

func (s *Server) apiShiftStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.Store.GetShiftStatus(r.Context(), CurrentUser(r).ID)
	if err != nil {
		apiErr(w, http.StatusInternalServerError, err)
		return
	}
	apiOK(w, map[string]any{"state": st.State, "has_open": st.HasOpen,
		"shift_id": st.ShiftID, "user_name": st.UserName, "date": st.Date, "status_text": st.StatusText})
}

func (s *Server) apiShiftSummary(w http.ResponseWriter, r *http.Request) {
	shiftID, err := strconv.ParseInt(r.URL.Query().Get("shift_id"), 10, 64)
	if err != nil || shiftID == 0 {
		open, err := s.Store.OpenShiftForUser(r.Context(), CurrentUser(r).ID)
		if err != nil || open == nil {
			apiErr(w, http.StatusNotFound, errString("لا يوجد شيفت مفتوح"))
			return
		}
		shiftID = open.ID
	}
	sum, err := s.Store.ShiftSummary(r.Context(), shiftID)
	if err != nil {
		apiErr(w, http.StatusNotFound, err)
		return
	}
	apiOK(w, map[string]any{"summary": sum})
}

func (s *Server) apiShiftsList(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	if date == "" {
		date = store.Today()
	}
	shifts, err := s.Store.ListShiftsByDate(r.Context(), date)
	if err != nil {
		apiErr(w, http.StatusInternalServerError, err)
		return
	}
	u := CurrentUser(r)
	if !rbac.Can(u.Role, rbac.PermViewAllCashBox) {
		var own []*store.Shift
		for _, sh := range shifts {
			if sh.UserID == u.ID {
				own = append(own, sh)
			}
		}
		shifts = own
	}
	// PR10-L2: same live-summary override as DailyClose and the
	// shifts page — open shifts render the live Expected.
	if err := s.Store.ApplyLiveSummaryToOpenShifts(r.Context(), shifts); err != nil {
		apiErr(w, http.StatusInternalServerError, err)
		return
	}
	apiOK(w, map[string]any{"shifts": shifts, "date": date})
}

// ---------------------------------------------------------------- JSON API: cashbox

func (s *Server) apiCashBoxIn(w http.ResponseWriter, r *http.Request) {
	s.apiCashBoxMove(w, r, "in")
}

func (s *Server) apiCashBoxOut(w http.ResponseWriter, r *http.Request) {
	s.apiCashBoxMove(w, r, "out")
}

func (s *Server) apiCashBoxMove(w http.ResponseWriter, r *http.Request, kind string) {
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
	shiftID := intVal(body, "shift_id")
	if kind == "in" {
		id, err := s.Store.CashBoxIn(r.Context(), CurrentUser(r), shiftID, amount, strVal(body, "reason"))
		if err != nil {
			apiErr(w, http.StatusBadRequest, err)
			return
		}
		apiOK(w, map[string]any{"movement_id": id})
		return
	}
	id, err := s.Store.CashBoxOut(r.Context(), CurrentUser(r), shiftID, amount, strVal(body, "reason"))
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	apiOK(w, map[string]any{"movement_id": id})
}

func (s *Server) apiCashBoxList(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	var userID int64
	if v := r.URL.Query().Get("user_id"); v != "" {
		userID, _ = strconv.ParseInt(v, 10, 64)
	}
	list, err := s.Store.ListCashBoxMovements(r.Context(), CurrentUser(r), date, userID)
	if err != nil {
		apiErr(w, http.StatusInternalServerError, err)
		return
	}
	apiOK(w, map[string]any{"movements": list})
}

func (s *Server) apiCashCountCreate(w http.ResponseWriter, r *http.Request) {
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
	cc, err := s.Store.RecordCashCount(r.Context(), CurrentUser(r), intVal(body, "shift_id"), amount,
		strVal(body, "denominations"), strVal(body, "notes"))
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	apiOK(w, map[string]any{"count": cc})
}

func (s *Server) apiCashCountReview(w http.ResponseWriter, r *http.Request) {
	body, err := readJSONBody(r)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	approve := false
	if v, ok := body["approve"]; ok {
		if b, ok := v.(bool); ok {
			approve = b
		}
	}
	cc, err := s.Store.ReviewCashCount(r.Context(), CurrentUser(r), intVal(body, "count_id"), approve, strVal(body, "note"))
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	apiOK(w, map[string]any{"count": cc})
}

func (s *Server) apiCashCountsList(w http.ResponseWriter, r *http.Request) {
	shiftID, err := strconv.ParseInt(r.URL.Query().Get("shift_id"), 10, 64)
	if err != nil || shiftID == 0 {
		apiErr(w, http.StatusBadRequest, errString("رقم الشيفت مطلوب"))
		return
	}
	list, err := s.Store.ListCashCounts(r.Context(), CurrentUser(r), shiftID)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	apiOK(w, map[string]any{"counts": list})
}

// ---------------------------------------------------------------- JSON API: returns

func (s *Server) apiReturnCreate(w http.ResponseWriter, r *http.Request) {
	body, err := readJSONBody(r)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	lines, err := parseAPIReturnLines(body)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.Store.ProcessReturn(r.Context(), CurrentUser(r), intVal(body, "invoice_id"), lines,
		strVal(body, "reason"), strVal(body, "method"))
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	apiOK(w, map[string]any{
		"total_refund_cents": int64(res.TotalRefund), "debt_reduction_cents": int64(res.DebtReduction),
		"cash_refund_cents": int64(res.CashRefund), "non_cash_refund_cents": int64(res.NonCashRefund),
		"return_ids": res.ReturnIDs, "invoice_number": res.InvoiceNumber,
		"voided_free": res.VoidedFree})
}

func parseAPIReturnLines(body map[string]any) ([]store.ReturnLineInput, error) {
	raw, ok := body["lines"].([]any)
	if !ok {
		return nil, errString("سطور المرتجع مطلوبة")
	}
	var lines []store.ReturnLineInput
	merged := map[int64]money.Qty{}
	var order []int64
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		pid := intVal(m, "product_id")
		var q money.Qty
		switch v := m["qty"].(type) {
		case string:
			var err error
			q, err = money.ParseQty(v)
			if err != nil {
				return nil, err
			}
		case float64:
			// FIX-1 L1: parse the raw number with rounding instead
			// of truncating (int64(v*1000) drops 0.999 of a milli).
			q = money.Qty(int64(v*1000 + 0.5))
		}
		if pid > 0 && q > 0 {
			// FIX-1 M3: merge duplicate rows for the same product.
			if _, ok := merged[pid]; !ok {
				order = append(order, pid)
			}
			merged[pid] += q
		}
	}
	for _, pid := range order {
		lines = append(lines, store.ReturnLineInput{ProductID: pid, Qty: merged[pid]})
	}
	return lines, nil
}

func (s *Server) apiReturnAll(w http.ResponseWriter, r *http.Request) {
	body, err := readJSONBody(r)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.Store.ProcessReturnAll(r.Context(), CurrentUser(r), intVal(body, "invoice_id"),
		strVal(body, "reason"), strVal(body, "method"))
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	apiOK(w, map[string]any{
		"total_refund_cents": int64(res.TotalRefund), "debt_reduction_cents": int64(res.DebtReduction),
		"cash_refund_cents": int64(res.CashRefund), "return_ids": res.ReturnIDs,
		"invoice_number": res.InvoiceNumber, "voided_free": res.VoidedFree})
}

func (s *Server) apiReturnsList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListReturns(r.Context(), CurrentUser(r),
		r.URL.Query().Get("from"), r.URL.Query().Get("to"))
	if err != nil {
		apiErr(w, http.StatusInternalServerError, err)
		return
	}
	apiOK(w, map[string]any{"returns": list})
}

func (s *Server) apiReturnsAnalysis(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("format") == "csv" {
		csv, err := s.Store.ReturnAnalysisCSV(r.Context(), CurrentUser(r),
			r.URL.Query().Get("from"), r.URL.Query().Get("to"))
		if err != nil {
			apiErr(w, http.StatusBadRequest, err)
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=returns-analysis.csv")
		_, _ = w.Write([]byte("\xef\xbb\xbf" + csv))
		return
	}
	rep, err := s.Store.GetReturnsAnalytics(r.Context(), CurrentUser(r),
		r.URL.Query().Get("from"), r.URL.Query().Get("to"))
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	apiOK(w, map[string]any{"analysis": rep})
}

// ---------------------------------------------------------------- JSON API: expenses

func (s *Server) apiExpenseCreate(w http.ResponseWriter, r *http.Request) {
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
	id, err := s.Store.RegisterExpense(r.Context(), CurrentUser(r),
		strVal(body, "description"), amount, strVal(body, "category"))
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	apiOK(w, map[string]any{"expense_id": id})
}

func (s *Server) apiExpensesList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListExpenses(r.Context(), CurrentUser(r),
		r.URL.Query().Get("from"), r.URL.Query().Get("to"))
	if err != nil {
		apiErr(w, http.StatusInternalServerError, err)
		return
	}
	apiOK(w, map[string]any{"expenses": list})
}

func (s *Server) apiExpenseUpdate(w http.ResponseWriter, r *http.Request) {
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
	if err := s.Store.UpdateExpense(r.Context(), CurrentUser(r), id,
		strVal(body, "description"), amount, strVal(body, "category")); err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	apiOK(w, map[string]any{"expense_id": id})
}

func (s *Server) apiExpenseDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		apiErr(w, http.StatusBadRequest, errString("معرّف غير صحيح"))
		return
	}
	if err := s.Store.DeleteExpense(r.Context(), CurrentUser(r), id); err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	apiOK(w, map[string]any{"expense_id": id, "deleted": true})
}

// ---------------------------------------------------------------- JSON API: daily close

func (s *Server) apiDailyClose(w http.ResponseWriter, r *http.Request) {
	rep, err := s.Store.DailyClose(r.Context(), CurrentUser(r), r.URL.Query().Get("date"))
	if err != nil {
		apiErr(w, http.StatusBadRequest, err)
		return
	}
	apiOK(w, map[string]any{"report": rep})
}

func (s *Server) apiDailyCloseReport(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if from == "" {
		from = store.Today()
	}
	if to == "" {
		to = store.Today()
	}
	switch r.URL.Query().Get("report") {
	case "top":
		rows, err := s.Store.TopProducts(r.Context(), u, from, to, 10, false)
		if err != nil {
			apiErr(w, http.StatusBadRequest, err)
			return
		}
		apiOK(w, map[string]any{"top_products": rows})
	case "bottom":
		rows, err := s.Store.TopProducts(r.Context(), u, from, to, 10, true)
		if err != nil {
			apiErr(w, http.StatusBadRequest, err)
			return
		}
		apiOK(w, map[string]any{"bottom_products": rows})
	case "cashier":
		rows, err := s.Store.BestCashiers(r.Context(), u, from, to)
		if err != nil {
			apiErr(w, http.StatusBadRequest, err)
			return
		}
		apiOK(w, map[string]any{"cashiers": rows})
	case "peak":
		rows, err := s.Store.PeakHours(r.Context(), u, from, to)
		if err != nil {
			apiErr(w, http.StatusBadRequest, err)
			return
		}
		apiOK(w, map[string]any{"peak_hours": rows})
	case "profit":
		rows, err := s.Store.TopProducts(r.Context(), u, from, to, 10, false)
		if err != nil {
			apiErr(w, http.StatusBadRequest, err)
			return
		}
		apiOK(w, map[string]any{"profit_by_product": rows})
	case "expiry":
		days := 30
		if v := r.URL.Query().Get("days"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				days = n
			}
		}
		rows, err := s.Store.ExpiryAlerts(r.Context(), u, days)
		if err != nil {
			apiErr(w, http.StatusBadRequest, err)
			return
		}
		apiOK(w, map[string]any{"expiry_alerts": rows})
	case "ledger":
		pid, err := strconv.ParseInt(r.URL.Query().Get("product_id"), 10, 64)
		if err != nil || pid == 0 {
			apiErr(w, http.StatusBadRequest, errString("رقم المنتج مطلوب"))
			return
		}
		rows, err := s.Store.StockLedger(r.Context(), u, pid, from, to)
		if err != nil {
			apiErr(w, http.StatusBadRequest, err)
			return
		}
		apiOK(w, map[string]any{"stock_ledger": rows})
	default:
		apiErr(w, http.StatusBadRequest, errString("نوع التقرير غير معروف (top/bottom/cashier/peak/profit/expiry/ledger)"))
	}
}
