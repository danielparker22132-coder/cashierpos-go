# CashierPOS Go — عقد البناء (ARCHITECTURE)

**اقرأ هذا الملف أولاً قبل كتابة أي كود. أي مخالفة له تُرفض في المراجعة.**

## البيئة
```sh
export PATH=$HOME/go-install/go/bin:$PATH
export GOCACHE=/dev/shm/gocache GOPATH=$HOME/go-pkg GOMODCACHE=$HOME/go-pkg/pkg/mod GOTMPDIR=/dev/shm
export GOTOOLCHAIN=local GOFLAGS=-mod=mod
```
- Go 1.24.4. `modernc.org/sqlite v1.39.0` فقط (pure Go، بدون CGO).
  **لا ترفّع الإصدار**: v1.63.0 يتطلب go 1.26. `go mod tidy` بدون require مثبّت
  سيحاول جلب الأحدث ويفشل — الـ require مثبّت في go.mod، لا تغيّره.
- التشغيل: `go run ./cmd/cashierpos` ثم http://localhost:8080
- قاعدة البيانات: ملف SQLite `cashierpos.db` بجانب التشغيل
  (أو `$CASHIERPOS_DB`). `PRAGMA foreign_keys=ON`، `MaxOpenConns(1)`.

## الفلوس والكميات (قاعدة صارمة)
- الفلوس: `int64` **بالقرش** (1 ج.م = 100). لا float أبداً في أي مسار مالي.
  استخدم `money` package: `Cents(int64)`، `FromFloat` للعرض فقط، `FormatEGP`
  يعرض `12.50 ج.م`.
- الكميات: `int64` **بالملي** (×1000، 3 خانات عشرية كحد أقصى).
  `Qty(1000)` = قطعة واحدة. الكمية الوزنية بالكيلو ×1000 أيضاً.
- التواريخ: `TEXT` بصيغة `YYYY-MM-DD`؛ الطوابع الزمنية ISO محلي
  `2006-01-02 15:04:05` بتوقيت Africa/Cairo.

## الأدوار والصلاحيات (§15 من FEATURE_MAP)
- الأدوار: `admin`, `manager`, `cashier`, `supervisor`, `warehouse`.
- **Manager = Admin في كل الصلاحيات** (قرار جوني 2026-11-21). التطبيق:
  `rbac.EffectiveRole("manager")` ترجع `"admin"` عند فحص أي صلاحية.
  الدور الأصلي يبقى محفوظاً في الجلسة/التدقيق للعرض فقط.
- كل دالة خدمة حساسة تتحقق من الصلاحية **داخلها** (service layer)،
  وليس في الـ handler فقط. أسماء الصلاحيات في `internal/rbac/rbac.go`
  (مطابقة لجدول §15). رسالة الرفض الموحدة:
  `غير مسموح — هذا الإجراء غير متاح لدورك الحالي. الدور: <role>. تواصل مع المدير.`

## القواعد الصارمة (تُرفض أي مخالفة)
1. **السالب ممنوع في كل مكان**: درج، مخزون، أرصدة عملاء/موردين/مندوبين،
   مصروفات، مرتجعات، خصومات، حدود ائتمان. الرفض برسالة عربية واضحة
   **قبل** أي كتابة، والصفر مسموح. لا clamp صامت — ارفض بخطأ.
   الاستثناء الوحيد المسموح: `Difference` في إغلاق الشيفت قد يكون
   سالباً (عجز) كقيمة تقرير فقط، ولا يُخصم من أي رصيد.
2. البيع يتطلب شيفت مفتوح لنفس المستخدم في تاريخ اليوم.
3. رقم الفاتورة: عدد صحيح متسلسل عالمي 1,2,3… لا يتكرر أبداً.
   يُصدر داخل معاملة النشر من `invoice_number_high_water` في AppSettings
   (يُرفع ولا ينزل أبداً — حتى بعد حذف/إلغاء فاتورة). الاستعادة من نسخة
   احتياطية تعيد الـ high-water لقيمة النسخة (سلوك موثّق، ليس عيباً).
4. كلمة السر: SHA-256 hex بدون salt (توافق PyQt). الدخول يقبل أيضاً
   MD5 hex أو plaintext القديم ويرقّي المخزّن إلى SHA-256 فور نجاح الدخول.
   أول تشغيل ينشئ `admin/admin123` مع `MustChangePassword=1` وإجبار
   تغيير كلمة السر عند أول دخول.
5. كل الفلوس Decimal/قرش؛ الواجهة عربية RTL.
6. لا مزامنة سحابية حقيقية — CloudApi خارج النطاق. النسخ السحابي =
   مجلد يختاره المدير (§18). أي ذكر لـ sync في الكود/الواجهة يجب أن
   يكون TODO صريحاً، لا حالة مزيفة.

## هيكل الحزم (ملكية الملفات — لا تعدّل ملفاً يملكه عامل آخر)
```
cmd/cashierpos/main.go            — المنسق (نقطة الدخول فقط)
internal/money/                  — قرش/ملي/تنسيق (مكتمل)
internal/store/                  — Store + schema + seed (مكتمل؛ لا تعدّل schema.go
                                   إلا عبر المنسق؛ دوال الخدمة في ملفات domain_*.go)
internal/auth/                   — hash/verify/session (مكتمل)
internal/rbac/                   — خريطة الصلاحيات (مكتمل)
internal/web/                    — HTTP server + templates + handlers
  server.go, middleware.go       — المنسق/عامل الواجهة
  templates/*.html               — قوالب مضمّنة (embed)
internal/service/...             — منطق المجال يُكتب كدوال على *store.Store
                                   في internal/store/domain_*.go (كل عامل في ملفه)
```
- دوال الخدمة: methods على `*store.Store`، توقيعها
  `func (s *Store) Xxx(ctx, user *auth.User, args...) (..., error)`.
  الخطأ العربي يُعرض للمستخدم كما هو.
- كل كتابة مالية داخل `sql.Tx` واحدة (ذريّة). استخدم `s.Tx(ctx, func(tx) error)`.
- التدقيق: كل إجراء حساس يكتب صفاً في AuditLog عبر `s.Audit(...)`
  داخل نفس المعاملة.
- الاختبارات: `*_test.go` بجانب الكود، قاعدة بيانات `:memory:` عبر
  `store.OpenTest(t)`. اختبارات نهاية-ل-نهاية عبر HTTP في
  `internal/web/e2e_test.go` (عامل الاختبار).

## تقسيم العمل
| العامل | الملكية |
|---|---|
| B1 مبيعات | store/domain_sales.go, domain_products.go, domain_promos.go, domain_stocktake.go + web/handlers_sales.go + templates/sales,products,promos,stocktake |
| B2 شيفت/درج | store/domain_shifts.go, domain_cashbox.go, domain_returns.go, domain_expenses.go + web/handlers_shifts.go + templates/shifts,returns,expenses,dailyclose |
| B3 عملاء/موردون | store/domain_customers.go, domain_vendors.go, domain_payables.go, domain_courier.go, domain_transfers.go + web/handlers_*.go + templates |
| B4 تنبيهات/إدارة | store/domain_notifications.go, domain_audit.go, domain_users.go, domain_settings.go, domain_backup.go, domain_reports.go, domain_attendance.go + web/handlers_admin.go + templates |
| B5 واجهة | web/server.go, middleware.go, templates/layout.html, login, hub, CSS — يربط كل الـ handlers |
| Tester | internal/web/e2e_test.go + تقارير اختبار |
| Reviewer | مراجعة معارضة من الكود فقط |

## قواعد العمل المشتركة
- لا تعدّل `go.mod/go.sum` (إلا المنسق)، لا تعدّل `internal/store/schema.go`
  ولا `internal/store/store.go` ولا `internal/money` ولا `internal/auth`
  ولا `internal/rbac` — إن احتجت جدولاً/عموداً ناقصاً أبلغ المنسق.
- كل handler جديد يُسجَّل في `web/routes.go` (يملكه B5؛ أرسل له
  المسار + الدالة + الصلاحية المطلوبة في تقريرك).
- شغّل `gofmt -l .` و `go vet ./...` و `go test ./...` قبل تسليم أي ملف.
- رسائل الخطأ للمستخدم عربية؛ سجلات الخادم (log) إنجليزية مختصرة.
