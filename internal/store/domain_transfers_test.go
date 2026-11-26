package store

import (
	"context"
	"testing"

	"cashierpos-go/internal/money"
)

func TestTransferApproveByCashierRejectByCashierRefused(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	cashier := testUser(t, s, "cash1", "cashier")
	sup := testUser(t, s, "sup1", "supervisor")

	// Validation.
	mustErr(t, func() error {
		_, err := s.CreateTransfer(ctx, admin, TransferInput{SenderName: "", Amount: money.Cents(100), Method: "فودافون كاش"})
		return err
	}(), "transfer without sender")
	mustErr(t, func() error {
		_, err := s.CreateTransfer(ctx, admin, TransferInput{SenderName: "س", Amount: 0, Method: "فودافون كاش"})
		return err
	}(), "transfer zero amount")

	id, err := s.CreateTransfer(ctx, admin, TransferInput{SenderName: "عميل أ", Amount: money.Cents(25000), Method: "فودافون كاش", Reference: "TX-100"})
	if err != nil {
		t.Fatalf("create transfer: %v", err)
	}
	// Cashier CAN approve (C-06).
	if err := s.ApproveTransfer(ctx, cashier, id, "تم التأكيد من الكاشير"); err != nil {
		t.Fatalf("cashier approve: %v", err)
	}
	tr, _ := s.GetTransfer(ctx, admin, id)
	if tr.Status != "approved" || tr.ReviewedBy != "cash1" || tr.ReviewedAt == "" || tr.ReviewNote != "تم التأكيد من الكاشير" {
		t.Fatalf("approved transfer wrong: %+v", tr)
	}
	// Re-review of a decided transfer is refused.
	mustErr(t, s.RejectTransfer(ctx, sup, id, "متأخر"), "re-review approved transfer")
	mustErr(t, s.ApproveTransfer(ctx, admin, id, "مرة أخرى"), "re-approve approved transfer")

	// Cashier is explicitly refused from REJECT (C-07).
	id2, _ := s.CreateTransfer(ctx, admin, TransferInput{SenderName: "عميل ب", Amount: money.Cents(10000), Method: "إنستاباي", Reference: "TX-101"})
	mustErr(t, s.RejectTransfer(ctx, cashier, id2, "رفض الكاشير"), "cashier reject transfer")
	// Supervisor MAY reject.
	if err := s.RejectTransfer(ctx, sup, id2, "المرجع غير صحيح"); err != nil {
		t.Fatalf("supervisor reject: %v", err)
	}
	tr, _ = s.GetTransfer(ctx, admin, id2)
	if tr.Status != "rejected" || tr.ReviewedBy != "sup1" {
		t.Fatalf("rejected transfer wrong: %+v", tr)
	}
	// List filters.
	pending, err := s.ListTransfers(ctx, admin, "pending")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending = %d, want 0", len(pending))
	}
	all, err := s.ListTransfers(ctx, admin, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("all transfers = %d, want 2", len(all))
	}
	// Warehouse may not even view transfers.
	wh := testUser(t, s, "wh1", "warehouse")
	mustErr(t, func() error {
		_, err := s.CreateTransfer(ctx, wh, TransferInput{SenderName: "س", Amount: money.Cents(100), Method: "فيزا"})
		return err
	}(), "warehouse create transfer")
}

// --- FIX-1 regression tests ---

func TestFix1_CreateTransferNeedsCreatePerm(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	cashier := testUser(t, s, "cash1", "cashier")
	wh := testUser(t, s, "wh1", "warehouse")
	// Warehouse holds neither view nor create: refused (M7a).
	if _, err := s.CreateTransfer(ctx, wh, TransferInput{SenderName: "س", Amount: money.Cents(100), Method: "فودافون كاش"}); err == nil {
		t.Fatal("warehouse must not record transfers (M7a)")
	}
	// Cashier holds the dedicated create permission.
	if _, err := s.CreateTransfer(ctx, cashier, TransferInput{SenderName: "س", Amount: money.Cents(100), Method: "فودافون كاش"}); err != nil {
		t.Fatalf("cashier create transfer: %v", err)
	}
	_ = admin
}
