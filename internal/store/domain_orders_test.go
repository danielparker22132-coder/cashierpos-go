// Package store — online-orders store tests (FIX-3 H1, web bell).
package store

import (
	"context"
	"testing"
)

func onlineOrderInput() OnlineOrderInput {
	return OnlineOrderInput{
		CustomerName: "عميل أونلاين", CustomerPhone: "01000000000",
		Items: []OnlineOrderItem{
			{ProductID: 1, Name: "سكر", Qty: 2000, UnitPrice: 2000},
			{ProductID: 2, Name: "زيت", Qty: 1000, UnitPrice: 5000},
		},
		PaymentMethod: "Vodafone Cash",
	}
}

func TestOnlineOrders_PendingCounts(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	cashier := b2TestUser(t, s, "ordcash", "cashier")

	// Fresh store: zero pending, zero evidence.
	if n, _ := s.PendingOrdersCount(ctx, admin); n != 0 {
		t.Fatalf("pending on empty store = %d, want 0", n)
	}
	if n, _ := s.PendingEvidenceCount(ctx, admin); n != 0 {
		t.Fatalf("evidence on empty store = %d, want 0", n)
	}

	// One CREATED order: pending 1, evidence 0.
	o1, err := s.CreateOnlineOrder(ctx, admin, onlineOrderInput())
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	if o1.Total != 9000 {
		t.Fatalf("computed total = %d, want 9000", o1.Total)
	}
	if n, _ := s.PendingOrdersCount(ctx, cashier); n != 1 {
		t.Fatalf("pending after 1 CREATED = %d, want 1", n)
	}

	// A second order awaiting evidence: pending 2, evidence 1.
	in2 := onlineOrderInput()
	in2.CustomerName = "عميل دليل"
	in2.PaymentStatus = PayAwaitingVerification
	in2.EvidenceRef = "pay-proof-001.jpg"
	o2, err := s.CreateOnlineOrder(ctx, admin, in2)
	if err != nil {
		t.Fatalf("create evidence order: %v", err)
	}
	if n, _ := s.PendingOrdersCount(ctx, admin); n != 2 {
		t.Fatalf("pending = %d, want 2", n)
	}
	if n, _ := s.PendingEvidenceCount(ctx, admin); n != 1 {
		t.Fatalf("evidence = %d, want 1", n)
	}

	// Completing the first order removes it from pending (evidence stays).
	if _, err := s.SetOnlineOrderStatus(ctx, admin, o1.ID, OrderPrepared); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if n, _ := s.PendingOrdersCount(ctx, admin); n != 2 {
		t.Fatalf("pending after PREPARED = %d, want 2 (PREPARED still counts)", n)
	}
	if _, err := s.SetOnlineOrderStatus(ctx, admin, o1.ID, OrderReady); err != nil {
		t.Fatalf("ready: %v", err)
	}
	if n, _ := s.PendingOrdersCount(ctx, admin); n != 1 {
		t.Fatalf("pending after READY = %d, want 1", n)
	}
	if _, err := s.SetOnlineOrderStatus(ctx, admin, o1.ID, OrderDelivered); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if _, err := s.SetOnlineOrderStatus(ctx, admin, o1.ID, OrderCompleted); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if n, _ := s.PendingOrdersCount(ctx, admin); n != 1 {
		t.Fatalf("pending after COMPLETED = %d, want 1 (only the evidence order)", n)
	}

	// Resolving the evidence clears the evidence count (order still pending).
	if _, err := s.ResolveOrderEvidence(ctx, admin, o2.ID); err != nil {
		t.Fatalf("resolve evidence: %v", err)
	}
	if n, _ := s.PendingEvidenceCount(ctx, admin); n != 0 {
		t.Fatalf("evidence after resolve = %d, want 0", n)
	}
	if n, _ := s.PendingOrdersCount(ctx, admin); n != 1 {
		t.Fatalf("pending after resolve = %d, want 1 (CREATED still counts)", n)
	}
}

func TestOnlineOrders_RolesAndValidation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	cashier := b2TestUser(t, s, "ordcash2", "cashier")
	sup := b2TestUser(t, s, "ordsup", "supervisor")

	// Only admin/manager may create orders through intake.
	if _, err := s.CreateOnlineOrder(ctx, cashier, onlineOrderInput()); err == nil {
		t.Fatal("cashier intake should be denied")
	}
	if _, err := s.CreateOnlineOrder(ctx, sup, onlineOrderInput()); err == nil {
		t.Fatal("supervisor intake should be denied")
	}
	o, err := s.CreateOnlineOrder(ctx, admin, onlineOrderInput())
	if err != nil {
		t.Fatalf("admin create: %v", err)
	}

	// Validation: empty customer, no items, mismatched total, bad status.
	for i, in := range []OnlineOrderInput{
		{CustomerName: "", Items: onlineOrderInput().Items},
		{CustomerName: "x"},
		{CustomerName: "x", Items: onlineOrderInput().Items, Total: 1},
		{CustomerName: "x", Items: onlineOrderInput().Items, Status: "NOPE"},
		{CustomerName: "x", Items: onlineOrderInput().Items, PaymentStatus: "NOPE"},
	} {
		if _, err := s.CreateOnlineOrder(ctx, admin, in); err == nil {
			t.Fatalf("case %d: invalid input accepted", i)
		}
	}

	// Illegal transition: CREATED straight to COMPLETED.
	if _, err := s.SetOnlineOrderStatus(ctx, admin, o.ID, OrderCompleted); err == nil {
		t.Fatal("CREATED -> COMPLETED should be refused")
	}
	// Resolving evidence on an order that is not awaiting it.
	if _, err := s.ResolveOrderEvidence(ctx, admin, o.ID); err == nil {
		t.Fatal("resolve on PENDING order should be refused")
	}
	// Cashier may not resolve or advance, even on a real awaiting order.
	in2 := onlineOrderInput()
	in2.PaymentStatus = PayAwaitingVerification
	o2, err := s.CreateOnlineOrder(ctx, admin, in2)
	if err != nil {
		t.Fatalf("create awaiting order: %v", err)
	}
	if _, err := s.ResolveOrderEvidence(ctx, cashier, o2.ID); err == nil {
		t.Fatal("cashier resolve should be denied")
	}
	if _, err := s.SetOnlineOrderStatus(ctx, cashier, o2.ID, OrderPrepared); err == nil {
		t.Fatal("cashier advance should be denied")
	}
	// Unknown order id.
	if _, err := s.GetOnlineOrder(ctx, admin, 99999); err == nil {
		t.Fatal("unknown order should error")
	}
	// List + get round-trip keeps items.
	got, err := s.GetOnlineOrder(ctx, cashier, o.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Items) != 2 || got.Items[0].Name != "سكر" {
		t.Fatalf("items round-trip wrong: %+v", got.Items)
	}
	// Awaiting-verification text survives in the stored row.
	if got2, _ := s.GetOnlineOrder(ctx, admin, o2.ID); got2.PaymentStatus != PayAwaitingVerification {
		t.Fatalf("payment status = %q, want AWAITING_VERIFICATION", got2.PaymentStatus)
	}
}
