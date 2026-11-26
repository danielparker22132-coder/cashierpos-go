package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/store"
	"cashierpos-go/internal/web"
)

func main() {
	addr := flag.String("addr", ":8080", "عنوان الاستماع (مثال :8080)")
	dbPath := flag.String("db", "", "مسار ملف قاعدة البيانات (افتراضي cashierpos.db)")
	flag.Parse()
	if *dbPath == "" {
		if v := os.Getenv("CASHIERPOS_DB"); v != "" {
			*dbPath = v
		} else {
			*dbPath = "cashierpos.db"
		}
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer st.Close()
	// Startup jobs (never block startup on failure):
	// 1. publish due scheduled notifications, 2. daily cloud/local backup
	// once per day at first startup (cloud_backup_time is a stored
	// preference only; see FEATURE_MAP T-11/§18).
	ctx := context.Background()
	if n, err := st.PublishDueNotifications(ctx); err != nil {
		log.Printf("publish due notifications: %v", err)
	} else if n > 0 {
		log.Printf("published %d due notifications", n)
	}
	sysUser := &auth.User{ID: 0, UserName: "system", FullName: "النظام", Role: "admin"}
	if res, ran, err := st.RunDailyBackupIfDue(ctx, sysUser); err != nil {
		log.Printf("daily backup: %v", err)
	} else if ran {
		log.Printf("daily backup: %s (cloud %s)", res.FileName, res.CloudStatus)
	}
	srv, err := web.New(st)
	if err != nil {
		log.Fatalf("web init: %v", err)
	}
	log.Printf("CashierPOS Go listening on http://localhost%s (db=%s)", *addr, *dbPath)
	log.Printf("first-run admin: admin / admin123 (must change password on first login)")
	if err := http.ListenAndServe(*addr, srv.Handler()); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
