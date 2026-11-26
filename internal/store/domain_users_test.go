package store

import (
	"context"
	"testing"

	"cashierpos-go/internal/auth"
)

func TestUsersCRUD(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testAdmin()

	// Create.
	id, err := s.CreateUser(ctx, admin, UserInput{UserName: "cash1", Password: "pass1234", FullName: "كاشير 1", Role: "cashier", IsActive: true})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	// Duplicate username.
	if _, err := s.CreateUser(ctx, admin, UserInput{UserName: "cash1", Password: "pass1234", Role: "cashier", IsActive: true}); err == nil {
		t.Fatal("duplicate username should be refused")
	}
	// Missing username / short password / bad role.
	if _, err := s.CreateUser(ctx, admin, UserInput{UserName: "", Password: "pass1234", Role: "cashier"}); err == nil {
		t.Fatal("empty username should be refused")
	}
	if _, err := s.CreateUser(ctx, admin, UserInput{UserName: "x", Password: "12", Role: "cashier"}); err == nil {
		t.Fatal("short password should be refused")
	}
	if _, err := s.CreateUser(ctx, admin, UserInput{UserName: "x", Password: "pass1234", Role: "boss"}); err == nil {
		t.Fatal("bad role should be refused")
	}
	// Cashier cannot manage users.
	cashier := &auth.User{ID: id, UserName: "cash1", Role: "cashier"}
	if _, err := s.CreateUser(ctx, cashier, UserInput{UserName: "y", Password: "pass1234", Role: "cashier"}); err == nil {
		t.Fatal("cashier should not create users")
	}

	// List + get.
	users, err := s.ListUsers(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 { // seeded admin + cash1
		t.Fatalf("users = %d, want 2", len(users))
	}
	got, err := s.GetUser(ctx, admin, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.FullName != "كاشير 1" || got.Role != "cashier" || !got.IsActive {
		t.Fatalf("GetUser = %+v", got)
	}

	// Update without password keeps the old hash.
	var hashBefore string
	_ = s.DB.QueryRow(`SELECT Password FROM Users WHERE UserID=?`, id).Scan(&hashBefore)
	if err := s.UpdateUser(ctx, admin, id, UserInput{FullName: "كاشير واحد", Role: "supervisor", IsActive: true}); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	var hashAfter string
	_ = s.DB.QueryRow(`SELECT Password FROM Users WHERE UserID=?`, id).Scan(&hashAfter)
	if hashBefore != hashAfter {
		t.Fatal("empty password on edit must keep the existing hash")
	}
	got, _ = s.GetUser(ctx, admin, id)
	if got.FullName != "كاشير واحد" || got.Role != "supervisor" {
		t.Fatalf("after update: %+v", got)
	}
	// Update with a new password changes the hash.
	if err := s.UpdateUser(ctx, admin, id, UserInput{Password: "newpass99", FullName: "كاشير واحد", Role: "supervisor", IsActive: true}); err != nil {
		t.Fatalf("UpdateUser with password: %v", err)
	}
	var hashNew string
	_ = s.DB.QueryRow(`SELECT Password FROM Users WHERE UserID=?`, id).Scan(&hashNew)
	if hashNew == hashBefore {
		t.Fatal("password change must change the stored hash")
	}
	if _, err := auth.Authenticate(s.DB, "cash1", "newpass99"); err != nil {
		t.Fatalf("login with new password: %v", err)
	}

	// Delete self is refused.
	if err := s.DeleteUser(ctx, admin, admin.ID); err == nil {
		t.Fatal("deleting the logged-in user should be refused")
	}
	// Delete the other user works.
	if err := s.DeleteUser(ctx, admin, id); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, err := s.GetUser(ctx, admin, id); err == nil {
		t.Fatal("deleted user should be gone")
	}
	// Deleting a missing user errors.
	if err := s.DeleteUser(ctx, admin, 9999); err == nil {
		t.Fatal("expected error deleting missing user")
	}
}

func TestUsersLastAdminGuardAndPermissions(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testAdmin()

	// Demoting the only admin is refused (defense in depth).
	if err := s.UpdateUser(ctx, admin, admin.ID, UserInput{FullName: "مدير النظام", Role: "cashier", IsActive: true}); err == nil {
		t.Fatal("demoting the last admin should be refused")
	}
	// Disabling the only admin is refused.
	if err := s.UpdateUser(ctx, admin, admin.ID, UserInput{FullName: "مدير النظام", Role: "admin", IsActive: false}); err == nil {
		t.Fatal("disabling the last admin should be refused")
	}
	// With a second admin present, demoting the first is allowed.
	mustCreateUser(t, s, "admin2", "admin")
	if err := s.UpdateUser(ctx, admin, admin.ID, UserInput{FullName: "مدير النظام", Role: "cashier", IsActive: true}); err != nil {
		t.Fatalf("demoting admin with a second admin present: %v", err)
	}
	// Restore: admin2 (now the actor) promotes admin back is not needed; check summary.
	summary := RolePermissionsSummary("cashier")
	found := map[string]bool{}
	for _, p := range summary {
		found[p.Perm] = p.Allowed
	}
	if found["manage_users"] {
		t.Fatal("cashier must not have manage_users")
	}
	if !found["create_invoice"] {
		t.Fatal("cashier must have create_invoice")
	}
	adminSummary := RolePermissionsSummary("admin")
	for _, p := range adminSummary {
		if !p.Allowed {
			t.Fatalf("admin must have every permission, missing %s", p.Perm)
		}
	}
	mgrSummary := RolePermissionsSummary("manager")
	for _, p := range mgrSummary {
		if !p.Allowed {
			t.Fatalf("manager (=admin) must have every permission, missing %s", p.Perm)
		}
	}
}
