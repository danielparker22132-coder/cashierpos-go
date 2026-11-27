// Package rbac — FIX-12 lane E tests.
//
// SW-M10 (§15 row 17): only admin/manager may approve a below-cost sale;
// supervisor, cashier and warehouse may not.
// SW-M11 (§15 row 64): warehouse may VIEW delivery zones (view-only).
package rbac

import "testing"

func TestFix12_BelowCostMatrix(t *testing.T) {
	// §15 row 17: C ❌ M ✅ A ✅ S ❌ (warehouse has no sale perm at all).
	cases := []struct {
		role string
		want bool
	}{
		{RoleAdmin, true},
		{RoleManager, true}, // manager == admin in all permissions
		{RoleSupervisor, false},
		{RoleCashier, false},
		{RoleWarehouse, false},
	}
	for _, c := range cases {
		if got := Can(c.role, PermApproveBelowCost); got != c.want {
			t.Errorf("Can(%q, PermApproveBelowCost) = %v, want %v", c.role, got, c.want)
		}
	}
	// Require must refuse the supervisor with the uniform denial.
	if err := Require(RoleSupervisor, PermApproveBelowCost); err == nil {
		t.Error("Require(supervisor, PermApproveBelowCost) = nil, want a denial")
	}
	if err := Require(RoleAdmin, PermApproveBelowCost); err != nil {
		t.Errorf("Require(admin, PermApproveBelowCost) = %v, want nil", err)
	}
}

func TestFix12_WarehouseViewZones(t *testing.T) {
	// §15 row 64: warehouse 👁 view-only for delivery zones.
	if !Can(RoleWarehouse, PermViewZones) {
		t.Error("Can(warehouse, PermViewZones) = false, want true (SW-M11)")
	}
	// Zone MANAGEMENT stays admin-only: warehouse must not hold it.
	if Can(RoleWarehouse, PermManageZones) {
		t.Error("Can(warehouse, PermManageZones) = true, want false (view-only, SW-M11)")
	}
	// Other roles keep their view-zones access.
	for _, role := range []string{RoleAdmin, RoleManager, RoleSupervisor, RoleCashier} {
		if !Can(role, PermViewZones) {
			t.Errorf("Can(%q, PermViewZones) = false, want true", role)
		}
	}
}
