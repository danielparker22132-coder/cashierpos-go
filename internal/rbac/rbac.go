// Package rbac implements the role permission matrix (FEATURE_MAP §15).
// Manager == Admin in ALL permissions (jony's ruling 2026-11-21).
// Checks happen in the service layer, not only in the UI.
package rbac

import "fmt"

// Roles.
const (
	RoleAdmin      = "admin"
	RoleManager    = "manager"
	RoleCashier    = "cashier"
	RoleSupervisor = "supervisor"
	RoleWarehouse  = "warehouse"
)

// RoleDisplayName returns the Arabic display name of a role.
func RoleDisplayName(role string) string {
	switch role {
	case RoleAdmin:
		return "مدير النظام"
	case RoleManager:
		return "مدير"
	case RoleCashier:
		return "كاشير"
	case RoleSupervisor:
		return "مشرف"
	case RoleWarehouse:
		return "أمين مخزن"
	default:
		return role
	}
}

// EffectiveRole maps manager to admin for every permission check.
func EffectiveRole(role string) string {
	if role == RoleManager {
		return RoleAdmin
	}
	return role
}

// Permission names (one per §15 action family).
const (
	PermOpenShift              = "open_shift"
	PermCloseOwnShift          = "close_own_shift"
	PermCloseAnyShift          = "close_any_shift"
	PermMoveCashBox            = "move_cashbox"
	PermViewAllCashBox         = "view_all_cashbox"
	PermCreateInvoice          = "create_invoice"
	PermGiveDiscount           = "give_discount"
	PermEditPrice              = "edit_price"
	PermViewProfit             = "view_profit"
	PermDoReturn               = "do_return"
	PermCancelInvoice          = "cancel_invoice"
	PermManageCustomers        = "manage_customers"
	PermCollectPayment         = "collect_payment"
	PermRegisterExpense        = "register_expense"
	PermManageProducts         = "manage_products"
	PermViewProducts           = "view_products"
	PermAddStock               = "add_stock"
	PermAdjustStock            = "adjust_stock"
	PermViewStock              = "view_stock"
	PermReceivePurchase        = "receive_purchase"
	PermEditPurchasePrice      = "edit_purchase_price"
	PermManageVendors          = "manage_vendors"
	PermCreateVendor           = "create_vendor"
	PermPayVendors             = "pay_vendors"
	PermViewVendorReports      = "view_vendor_reports"
	PermViewReports            = "view_reports"
	PermViewPaymentReport      = "view_payment_report"
	PermViewCustomerReport     = "view_customer_report"
	PermViewProfitAnalysis     = "view_profit_analysis"
	PermViewReturnAnalysis     = "view_return_analysis"
	PermAccessDailyClose       = "access_daily_close"
	PermAccessDashboard        = "access_dashboard"
	PermShiftMonitor           = "shift_monitor"
	PermViewAudit              = "view_audit"
	PermManageMonitor          = "manage_monitor"
	PermViewAttendance         = "view_attendance"
	PermManageUsers            = "manage_users"
	PermAccessSettings         = "access_settings"
	PermBackup                 = "backup_database"
	PermRestore                = "restore_database"
	PermResetDB                = "reset_database"
	PermScaleSettings          = "scale_settings"
	PermManageZones            = "manage_zones"
	PermViewZones              = "view_zones"
	PermViewCourierDebts       = "view_courier_debts"
	PermEditCourierDebts       = "edit_courier_debts"
	PermViewTransfers          = "view_transfers"
	PermCreateTransfer         = "create_transfer"
	PermApproveTransfer        = "approve_transfer"
	PermRejectTransfer         = "reject_transfer"
	PermEnterCashCount         = "enter_cash_count"
	PermReviewCashCount        = "review_cash_count"
	PermViewCashcountApprovals = "view_cashcount_approvals"
	PermChangeOwnPassword      = "change_own_password"
	PermExportCSV              = "export_csv"
	PermApproveCreditOverride  = "approve_credit_override"
	// PermApproveBelowCost gates the below-cost confirmation (FIX-3 H2,
	// CORRECTED by FIX-12 SW-M10 per §15 row 17): only admin/manager
	// may confirm a below-cost sale; a supervisor or cashier may never
	// confirm their own. Like AllowOverLimit, the flag is only an
	// approval when the caller holds the permission — the service
	// layer refuses it otherwise.
	PermApproveBelowCost = "approve_below_cost"
	PermManagePromotions       = "manage_promotions"
	PermStocktakeCount         = "stocktake_count"
	PermManageStocktakes       = "manage_stocktakes"
	PermApproveStocktake       = "approve_stocktake"
	PermManageNotifications    = "manage_notifications"
	PermManagePayables         = "manage_payables"
	PermViewPayables           = "view_payables"
	PermSettlePayable          = "settle_payable"
	// PermManageOrders is the staff online-orders permission (FIX-3 H1):
	// intake + evidence resolve + status advance. Admin/Manager only.
	PermManageOrders = "manage_orders"
)

// matrix maps effective role -> permission -> allowed.
// Warehouse is view-only for products/stock; cashier/supervisor per §15.
var matrix = map[string]map[string]bool{
	RoleAdmin: {
		PermOpenShift: true, PermCloseOwnShift: true, PermCloseAnyShift: true,
		PermMoveCashBox: true, PermViewAllCashBox: true, PermCreateInvoice: true,
		PermGiveDiscount: true, PermEditPrice: true, PermViewProfit: true,
		PermDoReturn: true, PermCancelInvoice: true, PermManageCustomers: true,
		PermCollectPayment: true, PermRegisterExpense: true, PermManageProducts: true,
		PermViewProducts: true, PermAddStock: true, PermAdjustStock: true,
		PermViewStock: true, PermReceivePurchase: true, PermEditPurchasePrice: true,
		PermManageVendors: true, PermCreateVendor: true, PermPayVendors: true,
		PermViewVendorReports: true, PermViewReports: true, PermViewPaymentReport: true,
		PermViewCustomerReport: true, PermViewProfitAnalysis: true, PermViewReturnAnalysis: true,
		PermAccessDailyClose: true, PermAccessDashboard: true, PermShiftMonitor: true,
		PermViewAudit: true, PermManageMonitor: true, PermViewAttendance: true,
		PermManageUsers: true, PermAccessSettings: true, PermBackup: true,
		PermRestore: true, PermResetDB: true, PermScaleSettings: true,
		PermManageZones: true, PermViewZones: true, PermViewCourierDebts: true,
		PermEditCourierDebts: true, PermViewTransfers: true, PermCreateTransfer: true, PermApproveTransfer: true,
		PermRejectTransfer: true, PermEnterCashCount: true, PermReviewCashCount: true,
		PermViewCashcountApprovals: true,
		PermChangeOwnPassword:      true, PermExportCSV: true, PermApproveCreditOverride: true,
		PermApproveBelowCost: true,
		PermManagePromotions: true, PermStocktakeCount: true, PermManageStocktakes: true,
		PermApproveStocktake: true, PermManageNotifications: true, PermManagePayables: true,
		PermViewPayables: true,
		PermSettlePayable: true, PermManageOrders: true,
	},
	RoleSupervisor: {
		PermOpenShift: true, PermCloseOwnShift: true, PermCloseAnyShift: true,
		PermMoveCashBox: true, PermCreateInvoice: true,
		PermGiveDiscount: true, PermViewProfit: true,
		PermDoReturn: true, PermManageCustomers: true,
		PermCollectPayment: true, PermRegisterExpense: true,
		PermViewProducts: true, PermAddStock: true,
		PermViewStock:   true,
		PermViewReports: true, PermViewCustomerReport: true,
		PermAccessDailyClose: true, PermAccessDashboard: true,
		PermViewAttendance: true,
		PermViewZones:      true, PermViewCourierDebts: true,
		PermEditCourierDebts: true, PermViewTransfers: true, PermCreateTransfer: true, PermApproveTransfer: true,
		PermRejectTransfer: true, PermEnterCashCount: true,
		PermViewPayables: true,
		PermChangeOwnPassword: true, PermApproveCreditOverride: false,
		// FIX-12 SW-M10 (§15 row 17): supervisor ❌ for selling below
		// buy price — no below-cost approval for this role.
		PermApproveBelowCost: false,
		PermStocktakeCount: true,
	},
	RoleCashier: {
		PermOpenShift: true, PermCloseOwnShift: true,
		PermMoveCashBox: true, PermCreateInvoice: true,
		PermManageCustomers: true,
		PermCollectPayment:  true, PermRegisterExpense: true,
		PermViewProducts: true,
		PermViewStock:    true, PermReceivePurchase: true,
		PermCreateVendor: true, PermPayVendors: true,
		PermViewCourierDebts: true,
		PermViewTransfers:    true, PermCreateTransfer: true, PermApproveTransfer: true,
		PermViewZones:      true,
		PermEnterCashCount: true,
		// PEER APPROVAL (jony 2026-11-25): a cashier may review ANOTHER
		// cashier's count (review_cash_count) but never their own — the
		// service layer refuses self-review. Supervisor still cannot
		// review, and the approvals log stays Admin/Manager only.
		PermReviewCashCount:   true,
		PermChangeOwnPassword: true,
		PermViewPayables:      true,
		PermSettlePayable:     true, // jony 2026-11-24: cashier may settle payables, drawer-gated, recorded
		PermStocktakeCount:    true,
	},
	RoleWarehouse: {
		PermViewProducts: true, PermAddStock: true, PermAdjustStock: true,
		PermViewStock: true, PermReceivePurchase: true,
		// FIX-12 SW-M11 (§15 row 64): warehouse views delivery zones
		// (view-only). Zone management stays admin-only
		// (PermManageZones is not granted here).
		PermViewZones:         true,
		PermChangeOwnPassword: true,
		PermStocktakeCount:    true,
	},
}

// Can reports whether role holds perm (manager counts as admin).
func Can(role, perm string) bool {
	role = EffectiveRole(role)
	if perms, ok := matrix[role]; ok {
		return perms[perm]
	}
	return false
}

// Require returns a uniform Arabic denial error when the role lacks perm.
func Require(role, perm string) error {
	if Can(role, perm) {
		return nil
	}
	return fmt.Errorf("غير مسموح — هذا الإجراء غير متاح لدورك الحالي. الدور: %s. تواصل مع المدير.", RoleDisplayName(role))
}

// CanCloseShift reports whether role may close a shift owned by ownerUserID.
func CanCloseShift(role string, selfID, ownerID int64) bool {
	if EffectiveRole(role) == RoleAdmin || role == RoleSupervisor {
		return true
	}
	return selfID == ownerID
}
