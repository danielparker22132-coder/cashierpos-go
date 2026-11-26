// Package store — B4: users management (Admin/Manager only).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/rbac"
)

// UserRecord is one Users row (no password hash exposed).
type UserRecord struct {
	ID                 int64
	UserName           string
	FullName           string
	Role               string
	IsActive           bool
	MustChangePassword bool
	CreatedDate        string
}

// UserInput is the create/edit form payload. Password is required on create
// and optional on edit (empty = keep the existing password).
type UserInput struct {
	UserName string
	Password string
	FullName string
	Role     string
	IsActive bool
}

// PermissionSummary is one row of the per-role permissions summary.
type PermissionSummary struct {
	Perm    string
	Label   string
	Allowed bool
}

// permissionCatalog lists the permissions shown in the per-role summary.
var permissionCatalog = []struct{ Perm, Label string }{
	{rbac.PermOpenShift, "فتح شيفت"},
	{rbac.PermCloseOwnShift, "إغلاق الشيفت الخاص"},
	{rbac.PermCloseAnyShift, "إغلاق أي شيفت"},
	{rbac.PermMoveCashBox, "حركة الدرج (إيداع/سحب)"},
	{rbac.PermViewAllCashBox, "عرض كل حركات الدرج"},
	{rbac.PermCreateInvoice, "إنشاء فاتورة بيع"},
	{rbac.PermGiveDiscount, "منح خصم"},
	{rbac.PermEditPrice, "تعديل سعر"},
	{rbac.PermViewProfit, "عرض الربح"},
	{rbac.PermDoReturn, "تنفيذ مرتجع"},
	{rbac.PermCancelInvoice, "إلغاء فاتورة"},
	{rbac.PermManageCustomers, "إدارة العملاء"},
	{rbac.PermCollectPayment, "تحصيل مدفوعات"},
	{rbac.PermRegisterExpense, "تسجيل مصروف"},
	{rbac.PermManageProducts, "إدارة المنتجات"},
	{rbac.PermViewProducts, "عرض المنتجات"},
	{rbac.PermAddStock, "إضافة مخزون"},
	{rbac.PermAdjustStock, "تعديل مخزون"},
	{rbac.PermViewStock, "عرض المخزون"},
	{rbac.PermReceivePurchase, "استلام مشتريات"},
	{rbac.PermManageVendors, "إدارة الموردين"},
	{rbac.PermPayVendors, "دفع للموردين"},
	{rbac.PermViewReports, "عرض التقارير"},
	{rbac.PermViewPaymentReport, "تقرير طرق الدفع"},
	{rbac.PermViewCustomerReport, "تقرير عميل"},
	{rbac.PermViewProfitAnalysis, "تحليل الربح"},
	{rbac.PermViewReturnAnalysis, "تحليل المرتجعات"},
	{rbac.PermAccessDailyClose, "الإغلاق اليومي"},
	{rbac.PermAccessDashboard, "لوحة المدير"},
	{rbac.PermShiftMonitor, "مراقب الشيفتات"},
	{rbac.PermViewAudit, "عرض سجل التدقيق"},
	{rbac.PermManageMonitor, "إدارة المراقبة الذكية"},
	{rbac.PermViewAttendance, "عرض الحضور"},
	{rbac.PermManageUsers, "إدارة المستخدمين"},
	{rbac.PermAccessSettings, "الوصول للإعدادات"},
	{rbac.PermBackup, "نسخ احتياطي"},
	{rbac.PermRestore, "استعادة نسخة"},
	{rbac.PermResetDB, "إعادة تعيين قاعدة البيانات"},
	{rbac.PermScaleSettings, "إعدادات الميزان"},
	{rbac.PermManageZones, "إدارة مناطق التوصيل"},
	{rbac.PermViewZones, "عرض مناطق التوصيل"},
	{rbac.PermViewCourierDebts, "عرض ديون الساعي"},
	{rbac.PermEditCourierDebts, "تعديل ديون الساعي"},
	{rbac.PermViewTransfers, "عرض التحويلات الإلكترونية"},
	{rbac.PermApproveTransfer, "اعتماد تحويل"},
	{rbac.PermRejectTransfer, "رفض تحويل"},
	{rbac.PermEnterCashCount, "إدخال عدّ نقدية"},
	{rbac.PermReviewCashCount, "مراجعة عدّ نقدية"},
	{rbac.PermViewCashcountApprovals, "عرض سجل اعتماد العدّات"},
	{rbac.PermChangeOwnPassword, "تغيير كلمة السر الخاصة"},
	{rbac.PermExportCSV, "تصدير CSV"},
	{rbac.PermManagePromotions, "إدارة العروض"},
	{rbac.PermStocktakeCount, "العدّ في الجرد"},
	{rbac.PermManageStocktakes, "إدارة الجرد"},
	{rbac.PermApproveStocktake, "اعتماد الجرد"},
	{rbac.PermManageNotifications, "إدارة التنبيهات"},
	{rbac.PermManagePayables, "إدارة المستحقات"},
	{rbac.PermSettlePayable, "تسوية مستحق"},
}

// RolePermissionsSummary returns the ✅/❌ summary for a role.
func RolePermissionsSummary(role string) []PermissionSummary {
	out := make([]PermissionSummary, 0, len(permissionCatalog))
	for _, p := range permissionCatalog {
		out = append(out, PermissionSummary{Perm: p.Perm, Label: p.Label, Allowed: rbac.Can(role, p.Perm)})
	}
	return out
}

func scanUserRecord(row *sql.Row) (UserRecord, error) {
	var u UserRecord
	var active, must int
	err := row.Scan(&u.ID, &u.UserName, &u.FullName, &u.Role, &active, &must, &u.CreatedDate)
	if err != nil {
		return u, err
	}
	u.IsActive = active == 1
	u.MustChangePassword = must == 1
	return u, nil
}

// ListUsers returns all users (admin).
func (s *Store) ListUsers(ctx context.Context, user *auth.User) ([]UserRecord, error) {
	if err := rbac.Require(user.Role, rbac.PermManageUsers); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(`SELECT UserID,UserName,FullName,Role,IsActive,MustChangePassword,
		COALESCE(CreatedDate,'') FROM Users ORDER BY UserID`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserRecord
	for rows.Next() {
		var u UserRecord
		var active, must int
		if err := rows.Scan(&u.ID, &u.UserName, &u.FullName, &u.Role, &active, &must, &u.CreatedDate); err != nil {
			return nil, err
		}
		u.IsActive = active == 1
		u.MustChangePassword = must == 1
		out = append(out, u)
	}
	return out, rows.Err()
}

// GetUser returns one user by id (admin).
func (s *Store) GetUser(ctx context.Context, user *auth.User, id int64) (UserRecord, error) {
	var u UserRecord
	if err := rbac.Require(user.Role, rbac.PermManageUsers); err != nil {
		return u, err
	}
	u, err := scanUserRecord(s.DB.QueryRow(`SELECT UserID,UserName,FullName,Role,IsActive,MustChangePassword,
		COALESCE(CreatedDate,'') FROM Users WHERE UserID=?`, id))
	if err == sql.ErrNoRows {
		return u, errors.New("المستخدم غير موجود")
	}
	return u, err
}

// CreateUser creates a user (admin). Username is required and unique;
// password is required (min 4 chars); role must be one of the five roles.
func (s *Store) CreateUser(ctx context.Context, user *auth.User, in UserInput) (int64, error) {
	if err := rbac.Require(user.Role, rbac.PermManageUsers); err != nil {
		return 0, err
	}
	in.UserName = strings.TrimSpace(in.UserName)
	in.FullName = strings.TrimSpace(in.FullName)
	if in.UserName == "" {
		return 0, errors.New("اسم المستخدم مطلوب")
	}
	if len(in.Password) < 4 {
		return 0, errors.New("كلمة المرور مطلوبة (4 أحرف على الأقل)")
	}
	if !validRole(in.Role) {
		return 0, errors.New("الدور غير صحيح")
	}
	var id int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM Users WHERE UserName=?`, in.UserName).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return errors.New("اسم المستخدم موجود مسبقًا")
		}
		active := 0
		if in.IsActive {
			active = 1
		}
		res, err := tx.Exec(`INSERT INTO Users(UserName,Password,FullName,Role,IsActive,MustChangePassword,CreatedDate)
			VALUES(?,?,?,?,?,0,?)`, in.UserName, auth.HashPassword(in.Password), in.FullName, in.Role, active, Today())
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "create_user", "users", nil, 0, 0, "",
			fmt.Sprintf("إنشاء مستخدم %s (%s)", in.UserName, rbac.RoleDisplayName(in.Role)))
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// UpdateUser edits a user (admin). Username cannot change; an empty password
// keeps the existing one.
func (s *Store) UpdateUser(ctx context.Context, user *auth.User, id int64, in UserInput) error {
	if err := rbac.Require(user.Role, rbac.PermManageUsers); err != nil {
		return err
	}
	in.FullName = strings.TrimSpace(in.FullName)
	if !validRole(in.Role) {
		return errors.New("الدور غير صحيح")
	}
	if in.Password != "" && len(in.Password) < 4 {
		return errors.New("كلمة المرور يجب أن تكون 4 أحرف على الأقل")
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		var curRole string
		var curActive int
		if err := tx.QueryRow(`SELECT Role,IsActive FROM Users WHERE UserID=?`, id).Scan(&curRole, &curActive); err == sql.ErrNoRows {
			return errors.New("المستخدم غير موجود")
		} else if err != nil {
			return err
		}
		// Refuse demoting/disabling the last admin-level account.
		if (curRole == "admin" || curRole == "manager") && (in.Role != "admin" && in.Role != "manager" || !in.IsActive) {
			var n int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM Users WHERE Role IN ('admin','manager') AND IsActive=1 AND UserID<>?`, id).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return errors.New("لا يمكن تعطيل أو تنزيل آخر مدير — يجب وجود مدير مفعّل واحد على الأقل")
			}
		}
		active := 0
		if in.IsActive {
			active = 1
		}
		if in.Password != "" {
			_, err := tx.Exec(`UPDATE Users SET Password=?,FullName=?,Role=?,IsActive=? WHERE UserID=?`,
				auth.HashPassword(in.Password), in.FullName, in.Role, active, id)
			if err != nil {
				return err
			}
		} else {
			_, err := tx.Exec(`UPDATE Users SET FullName=?,Role=?,IsActive=? WHERE UserID=?`,
				in.FullName, in.Role, active, id)
			if err != nil {
				return err
			}
		}
		return AuditTx(tx, user.ID, user.UserName, "update_user", "users", nil, 0, 0, "",
			fmt.Sprintf("تعديل مستخدم %d (%s)", id, rbac.RoleDisplayName(in.Role)))
	})
}

// DeleteUser deletes a user (admin). The currently logged-in user cannot be
// deleted, nor can the last admin-level account.
func (s *Store) DeleteUser(ctx context.Context, user *auth.User, id int64) error {
	if err := rbac.Require(user.Role, rbac.PermManageUsers); err != nil {
		return err
	}
	if id == user.ID {
		return errors.New("لا يمكن حذف المستخدم الحالي")
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		var uname, role string
		if err := tx.QueryRow(`SELECT UserName,Role FROM Users WHERE UserID=?`, id).Scan(&uname, &role); err == sql.ErrNoRows {
			return errors.New("المستخدم غير موجود")
		} else if err != nil {
			return err
		}
		if role == "admin" || role == "manager" {
			var n int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM Users WHERE Role IN ('admin','manager') AND IsActive=1 AND UserID<>?`, id).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return errors.New("لا يمكن حذف آخر مدير — يجب وجود مدير مفعّل واحد على الأقل")
			}
		}
		if _, err := tx.Exec(`DELETE FROM Users WHERE UserID=?`, id); err != nil {
			return err
		}
		return AuditTx(tx, user.ID, user.UserName, "delete_user", "users", nil, 0, 0, "",
			fmt.Sprintf("حذف مستخدم %s", uname))
	})
}
