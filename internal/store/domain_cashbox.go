package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/money"
	"cashierpos-go/internal/rbac"
)

// ---------------------------------------------------------------- CashBox

// CashBoxMovement is one row of CashBox.
type CashBoxMovement struct {
	ID           int64
	UserID       int64
	UserName     string
	ShiftID      int64
	MovementType string // open | in | out | close | resume
	Amount       money.Cents
	Reason       string
	MovementDate string
}

// CashBoxMovementArabic renders a movement type in Arabic.
func CashBoxMovementArabic(t string) string {
	switch t {
	case "open":
		return "فتح"
	case "in":
		return "إضافة"
	case "out":
		return "سحب"
	case "close":
		return "إغلاق"
	case "resume":
		return "استكمال"
	default:
		return t
	}
}

func cashBoxMoveTx(tx *sql.Tx, user *auth.User, shiftID int64, kind string, amount money.Cents, reason string) (int64, error) {
	if amount <= 0 {
		return 0, errors.New("مبلغ غير صحيح")
	}
	if strings.TrimSpace(reason) == "" {
		return 0, errors.New("يجب إدخال سبب")
	}
	// R23-D-F1: magnitude cap on manual 'in'. A manager could type
	// any positive int64 amount and it was stored verbatim; two 2^62
	// 'in' rows then wrapped CashIn to MinInt64 in shiftSummaryTx
	// and close persisted the wrapped negative as متوازن — the exact
	// falsified-record outcome the R22-E-F2 opening cap eliminated.
	// The cap is money.MaxOpeningCents (100,000,000 EGP), the same
	// bound as the opening: no legitimate drawer movement reaches
	// it, and it sits ~9 orders of magnitude inside int64. The cap
	// lives here, the single funnel for both entry points (HTML form
	// POST /cashbox/in and API POST /api/cashbox/in both end in
	// CashBoxIn → cashBoxMoveTx). Automatic 'in' rows (sales,
	// collections, courier) are direct INSERTs elsewhere and never
	// pass through here, so no legitimate flow is affected. The
	// 'out' branch needs no cap: it is drawer-gated below.
	if kind == "in" && amount > money.MaxOpeningCents {
		return 0, money.ErrOverflow
	}
	if kind == "out" {
		// Discretionary withdrawal: refused when it would take the
		// drawer below zero (RequireDrawerBalance — exact balance
		// is allowed, negative is never allowed).
		if err := RequireDrawerBalance(tx, shiftID, amount, "سحب من الدرج"); err != nil {
			return 0, err
		}
	}
	res, err := tx.Exec(`INSERT INTO CashBox(UserID,UserName,ShiftID,MovementType,Amount,Reason,MovementDate)
		VALUES(?,?,?,?,?,?,?)`, user.ID, user.UserName, shiftID, kind, int64(amount), strings.TrimSpace(reason), Now())
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	action := "إضافة للدرج"
	if kind == "out" {
		action = "سحب من الدرج"
	}
	detail := fmt.Sprintf("%s: %s — %s (شيفت %d)", action, money.FormatEGP(amount), strings.TrimSpace(reason), shiftID)
	return id, AuditTx(tx, user.ID, user.UserName, action, "درج", nil, 0, 0, "", detail)
}

// validateCashBoxShift checks a caller-supplied shift id before a drawer
// movement (FIX-1 M4): the shift must exist, be open, and belong to the
// caller — unless the caller is Admin/Manager/Supervisor, who may move
// cash on another user's open shift.
func validateCashBoxShift(tx *sql.Tx, user *auth.User, shiftID int64) error {
	var ownerID int64
	var isOpen int
	err := tx.QueryRow(`SELECT UserID,IsOpen FROM Shifts WHERE ShiftID=?`, shiftID).Scan(&ownerID, &isOpen)
	if err == sql.ErrNoRows {
		return errors.New("الشيفت غير موجود")
	}
	if err != nil {
		return err
	}
	if isOpen != 1 {
		return errors.New("الشيفت المحدد غير مفتوح")
	}
	if ownerID != user.ID && !rbac.Can(user.Role, rbac.PermCloseAnyShift) {
		return errors.New("لا يمكن تحريك درج شيفت مستخدم آخر")
	}
	return nil
}

// CashBoxIn adds cash to the drawer (C-01). shiftID=0 uses the caller's
// open shift today. Amount must be > 0 and a reason is required.
func (s *Store) CashBoxIn(ctx context.Context, user *auth.User, shiftID int64, amount money.Cents, reason string) (int64, error) {
	if user == nil {
		return 0, errors.New("يلزم تسجيل الدخول")
	}
	if err := rbac.Require(user.Role, rbac.PermMoveCashBox); err != nil {
		return 0, err
	}
	var id int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		if shiftID == 0 {
			sid, err := OpenShiftID(tx, user.ID)
			if err != nil {
				return err
			}
			shiftID = sid
		} else if err := validateCashBoxShift(tx, user, shiftID); err != nil {
			return err
		}
		var err error
		id, err = cashBoxMoveTx(tx, user, shiftID, "in", amount, reason)
		return err
	})
	return id, err
}

// CashBoxOut withdraws cash from the drawer (C-02). Refused when the
// amount exceeds the expected drawer balance (negative forbidden).
func (s *Store) CashBoxOut(ctx context.Context, user *auth.User, shiftID int64, amount money.Cents, reason string) (int64, error) {
	if user == nil {
		return 0, errors.New("يلزم تسجيل الدخول")
	}
	if err := rbac.Require(user.Role, rbac.PermMoveCashBox); err != nil {
		return 0, err
	}
	var id int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		if shiftID == 0 {
			sid, err := OpenShiftID(tx, user.ID)
			if err != nil {
				return err
			}
			shiftID = sid
		} else if err := validateCashBoxShift(tx, user, shiftID); err != nil {
			return err
		}
		var err error
		id, err = cashBoxMoveTx(tx, user, shiftID, "out", amount, reason)
		return err
	})
	return id, err
}

// ListCashBoxMovements lists CashBox movements of a date (C-04/C-15).
// Admin/Manager (view_all_cashbox) see all users; anyone else sees
// only their own movements. userIDFilter=0 means "no filter".
func (s *Store) ListCashBoxMovements(ctx context.Context, user *auth.User, date string, userIDFilter int64) ([]*CashBoxMovement, error) {
	if user == nil {
		return nil, errors.New("يلزم تسجيل الدخول")
	}
	if strings.TrimSpace(date) == "" {
		date = Today()
	}
	canViewAll := rbac.Can(user.Role, rbac.PermViewAllCashBox)
	target := userIDFilter
	if !canViewAll {
		target = user.ID // others see only themselves
	}
	var rows *sql.Rows
	var err error
	if target != 0 {
		rows, err = s.DB.Query(`SELECT CashBoxID,UserID,UserName,ShiftID,MovementType,Amount,Reason,MovementDate
			FROM CashBox WHERE substr(MovementDate,1,10)=? AND UserID=? ORDER BY MovementDate, CashBoxID`,
			date, target)
	} else {
		rows, err = s.DB.Query(`SELECT CashBoxID,UserID,UserName,ShiftID,MovementType,Amount,Reason,MovementDate
			FROM CashBox WHERE substr(MovementDate,1,10)=? ORDER BY MovementDate, CashBoxID`, date)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CashBoxMovement
	for rows.Next() {
		var m CashBoxMovement
		var amt int64
		if err := rows.Scan(&m.ID, &m.UserID, &m.UserName, &m.ShiftID, &m.MovementType, &amt, &m.Reason, &m.MovementDate); err != nil {
			return nil, err
		}
		m.Amount = money.Cents(amt)
		out = append(out, &m)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- cash count (H-18/C-09)

// CashCount is one row of CashCounts.
type CashCount struct {
	ID               int64
	ShiftID          int64
	CountedBy        string
	CountedByUserID  int64 // FIX-3 L8b: the counting user's id (immune to username reuse)
	CountedAt        string
	AmountCounted    money.Cents
	ExpectedAmount   money.Cents
	Difference       money.Cents
	DifferenceStatus string
	Denominations    string
	Notes            string
	ReviewStatus     string
	ReviewedBy       string
	ReviewedAt       string
	ReviewNote       string
}

// RecordCashCount records a counted drawer amount for a shift
// (all four roles may enter — enter_cash_count). The difference is
// counted − expected and never blocks closing. Only an open shift
// can be counted, and only by its owner unless the caller may view
// all cashboxes (Admin/Manager).
func (s *Store) RecordCashCount(ctx context.Context, user *auth.User, shiftID int64, amount money.Cents, denominations, notes string) (*CashCount, error) {
	if user == nil {
		return nil, errors.New("يلزم تسجيل الدخول")
	}
	if err := rbac.Require(user.Role, rbac.PermEnterCashCount); err != nil {
		return nil, err
	}
	if amount < 0 {
		return nil, errors.New("المبلغ المعدود غير صحيح")
	}
	var cc *CashCount
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		sh, err := getShiftTx(tx, shiftID)
		if err != nil {
			return err
		}
		if sh.UserID != user.ID && !rbac.Can(user.Role, rbac.PermViewAllCashBox) {
			// FIX-18 R18-C1: uniform denial prefix (غير مسموح — …) so
			// apiErrForService maps this cross-user denial to 403,
			// the same L09 recipe as ListCashCounts (FIX-17).
			return errors.New("غير مسموح — لا يمكن عدّ درج مستخدم آخر")
		}
		if !sh.IsOpen {
			return errors.New("لا يمكن عدّ درج شيفت مغلق")
		}
		sum, err := shiftSummaryTx(tx, shiftID)
		if err != nil {
			return err
		}
		expected := sum.ExpectedBalance
		diff := amount - expected
		status := DifferenceStatus(diff)
		now := Now()
		res, err := tx.Exec(`INSERT INTO CashCounts(ShiftID,CountedBy,CountedByUserID,CountedAt,AmountCounted,
			ExpectedAmount,Difference,Denominations,Notes,ReviewStatus)
			VALUES(?,?,?,?,?,?,?,?,?,'pending')`,
			shiftID, user.UserName, user.ID, now, int64(amount), int64(expected), int64(diff), denominations, notes)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		cc = &CashCount{ID: id, ShiftID: shiftID, CountedBy: user.UserName, CountedByUserID: user.ID, CountedAt: now,
			AmountCounted: amount, ExpectedAmount: expected, Difference: diff,
			DifferenceStatus: status, Denominations: denominations, Notes: notes, ReviewStatus: "pending"}
		detail := fmt.Sprintf("عدّ درج شيفت %d: المعدود %s مقابل المتوقع %s (%s)",
			shiftID, money.FormatEGP(amount), money.FormatEGP(expected), status)
		return AuditTx(tx, user.ID, user.UserName, "عدّ النقدية", "شيفت", nil, 0, 0, "", detail)
	})
	if err != nil {
		return nil, err
	}
	return cc, nil
}

// isCountOwnerTx reports whether user is the owner of a cash count.
// FIX-3 L8b: the stored CountedByUserID wins when present; legacy rows
// (CountedByUserID 0) fall back to resolving the counted-by username to
// a user id. A bare username comparison is never used: if the counting
// user cannot be resolved to an id, the answer is false — a newly
// created user reusing a deleted counter's username is not the owner
// of counts recorded under the stored id, and an unresolvable legacy
// row belongs to nobody.
func isCountOwnerTx(tx *sql.Tx, countedBy string, countedByUserID int64, user *auth.User) bool {
	if user == nil {
		return false
	}
	if countedByUserID != 0 {
		return countedByUserID == user.ID
	}
	if countedBy == "" {
		return false
	}
	var ownerID int64
	if err := tx.QueryRow(`SELECT UserID FROM Users WHERE UserName=?`, countedBy).Scan(&ownerID); err == nil && ownerID != 0 {
		return ownerID == user.ID
	}
	return false
}

// countOwnerRoleTx resolves a count owner's role: the stored
// CountedByUserID first, else the counted-by username resolved to an
// id. It reports false when the owner cannot be resolved (deleted
// user, legacy row with no matching Users row).
func countOwnerRoleTx(tx *sql.Tx, countedBy string, countedByUserID int64) (string, bool) {
	var ownerID int64
	if countedByUserID != 0 {
		ownerID = countedByUserID
	} else if countedBy != "" {
		if err := tx.QueryRow(`SELECT UserID FROM Users WHERE UserName=?`, countedBy).Scan(&ownerID); err != nil || ownerID == 0 {
			return "", false
		}
	} else {
		return "", false
	}
	var role string
	if err := tx.QueryRow(`SELECT Role FROM Users WHERE UserID=?`, ownerID).Scan(&role); err != nil {
		return "", false
	}
	return role, true
}

// ErrSelfReview is returned by ReviewCashCount when a CASHIER tries to
// review their own cash count (jony's ruling 2026-11-25: Manager/Admin
// are EXEMPT and may approve their own count; a cashier may NEVER
// approve their own). ReviewCashCount writes an audit row for the
// attempt AFTER the rolled-back transaction (FIX-3 L8a) and still
// returns this error. A Manager/Admin self-approval is NOT an error:
// it goes through the normal approval path and is audited as a normal
// approval (approve/reject audit row + approvals log entry, owner ==
// approver), exactly like any other approval.
// FIX-13 I2: the text carries the uniform denial prefix
// ("غير مسموح — …") like every other permission denial, while
// keeping the specific reason — a client keying denial detection
// on the uniform prefix must not miss this gate.
var ErrSelfReview = errors.New("غير مسموح — لا يمكنك اعتماد عدّك الخاص، يجب أن يعتمده مستخدم آخر")

// ReviewCashCount reviews a cash count (Manager/Admin, plus — PEER APPROVAL,
// jony 2026-11-25 — a Cashier reviewing ANOTHER cashier's count;
// review_cash_count). C-3 (jony's ruling 2026-11-25, reversing the old
// FIX-3 M1 "nobody self-reviews" rule for Manager/Admin only): a
// Manager or Admin MAY review their own count, for approve AND reject
// alike, and the approval is audited as a normal approval; a CASHIER
// still may NEVER review their own count — the self-review refusal
// fires before the already-reviewed / balanced-count checks and writes
// nothing to the count itself; the attempt is audited afterwards (L8a).
// FIX-3 L1: a cashier reviews only a fellow cashier's count, never a
// manager/admin/supervisor's count. A count already reviewed cannot be
// reviewed again (no double review). A balanced count (difference 0)
// may be approved but may NOT be rejected.
// FIX-3 L1: a cashier reviews only a fellow cashier's count, never a
// manager/admin/supervisor's count. A count already reviewed cannot be
// reviewed again (no double review). A balanced count (difference 0)
// may be approved but may NOT be rejected.
func (s *Store) ReviewCashCount(ctx context.Context, user *auth.User, countID int64, approve bool, note string) (*CashCount, error) {
	if user == nil {
		return nil, errors.New("يلزم تسجيل الدخول")
	}
	if err := rbac.Require(user.Role, rbac.PermReviewCashCount); err != nil {
		return nil, err
	}
	var cc *CashCount
	// FIX-3 L8a: details captured for the post-rollback audit row when
	// the attempt is a refused self-review.
	var attemptShiftID int64
	var attemptOwner string
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRow(`SELECT CountID,ShiftID,CountedBy,CountedByUserID,CountedAt,AmountCounted,ExpectedAmount,
			Difference,Denominations,Notes,ReviewStatus,ReviewedBy,ReviewedAt,ReviewNote
			FROM CashCounts WHERE CountID=?`, countID)
		var c CashCount
		var counted, expected, diff int64
		var reviewedBy, reviewedAt, reviewNote sql.NullString
		if err := row.Scan(&c.ID, &c.ShiftID, &c.CountedBy, &c.CountedByUserID, &c.CountedAt, &counted, &expected,
			&diff, &c.Denominations, &c.Notes, &c.ReviewStatus, &reviewedBy, &reviewedAt, &reviewNote); err != nil {
			if err == sql.ErrNoRows {
				return errors.New("العدّ غير موجود")
			}
			return err
		}
		c.AmountCounted = money.Cents(counted)
		c.ExpectedAmount = money.Cents(expected)
		c.Difference = money.Cents(diff)
		c.DifferenceStatus = DifferenceStatus(c.Difference)
		c.ReviewedBy = reviewedBy.String
		c.ReviewedAt = reviewedAt.String
		c.ReviewNote = reviewNote.String
		attemptShiftID, attemptOwner = c.ShiftID, c.CountedBy
		// C-3 (jony 2026-11-25): the self-review ban applies to
		// CASHIERS only. Manager/Admin (EffectiveRole == admin) are
		// exempt and may approve/reject their own count; the
		// approval then follows the normal path below and is
		// audited as a normal approval. The error aborts the
		// transaction, so the count itself is untouched; the
		// attempt is audited after the rollback.
		if rbac.EffectiveRole(user.Role) == rbac.RoleCashier && isCountOwnerTx(tx, c.CountedBy, c.CountedByUserID, user) {
			return ErrSelfReview
		}
		// FIX-3 L1: a cashier reviews only a fellow cashier's count.
		// An unresolvable owner (deleted user, legacy row) is not a
		// provable peer, so the cashier is refused; admin/manager
		// may still review it.
		if rbac.EffectiveRole(user.Role) == rbac.RoleCashier {
			if ownerRole, ok := countOwnerRoleTx(tx, c.CountedBy, c.CountedByUserID); !ok || ownerRole != rbac.RoleCashier {
				return errors.New("الكاشير يعتمد عدّ كاشير آخر فقط — لا يمكن اعتماد عدّ مدير أو مشرف")
			}
		}
		if c.ReviewStatus != "" && c.ReviewStatus != "pending" {
			return errors.New("هذا العدّ تمت مراجعته بالفعل")
		}
		if !approve && c.Difference == 0 {
			return errors.New("لا يمكن رفض عدّ متوازن — الفرق صفر")
		}
		newStatus := "approved"
		action := "اعتماد فرق عدّ النقدية"
		if !approve {
			newStatus = "rejected"
			action = "رفض فرق عدّ النقدية"
		}
		now := Now()
		if _, err := tx.Exec(`UPDATE CashCounts SET ReviewStatus=?, ReviewedBy=?, ReviewedAt=?, ReviewNote=?
			WHERE CountID=?`, newStatus, user.UserName, now, note, countID); err != nil {
			return err
		}
		c.ReviewStatus = newStatus
		c.ReviewedBy = user.UserName
		c.ReviewedAt = now
		c.ReviewNote = note
		cc = &c
		verdict := "معتمد"
		if !approve {
			verdict = "مرفوض"
		}
		detail := fmt.Sprintf("عدّ رقم %d — شيفت %d — صاحب العدّ %s — المتوقع %s — المعدود %s — الفرق %s (%s) — %s",
			countID, c.ShiftID, c.CountedBy, money.FormatEGP(c.ExpectedAmount), money.FormatEGP(c.AmountCounted),
			money.FormatEGP(c.Difference), c.DifferenceStatus, verdict)
		if note != "" {
			detail += " — ملاحظة: " + note
		}
		return AuditTx(tx, user.ID, user.UserName, action, "شيفت", nil, 0, 0, "", detail)
	})
	if err != nil {
		// FIX-3 L8a: a refused self-review (cashier only since C-3;
		// Manager/Admin self-review is allowed) writes an audit row
		// ATTEMPT, after the rolled-back transaction — the count
		// itself stays untouched and no approve/reject row is written.
		// The original refusal is still returned.
		if errors.Is(err, ErrSelfReview) {
			_ = s.Audit(user.ID, user.UserName, "محاولة اعتماد عدّ خاص", "شيفت", nil, 0, 0, "",
				fmt.Sprintf("محاولة اعتماد عدّ خاص — عدّ رقم %d — شيفت %d — صاحب العدّ %s",
					countID, attemptShiftID, attemptOwner))
		}
		return nil, err
	}
	return cc, nil
}

// ListCashCounts lists the counts of one shift. The shift owner or
// anyone with view_all_cashbox may list them.
func (s *Store) ListCashCounts(ctx context.Context, user *auth.User, shiftID int64) ([]*CashCount, error) {
	if user == nil {
		return nil, errors.New("يلزم تسجيل الدخول")
	}
	sh, err := s.GetShift(ctx, shiftID)
	if err != nil {
		return nil, err
	}
	if sh.UserID != user.ID && !rbac.Can(user.Role, rbac.PermViewAllCashBox) {
		// FIX-17 SW2-L09: uniform denial prefix (غير مسموح — …) so
		// apiErrForService maps this cross-user denial to 403, like
		// every other SW-L08-family denial. The specific reason is kept.
		return nil, errors.New("غير مسموح — لا يمكن عرض عدّ درج مستخدم آخر")
	}
	rows, err := s.DB.Query(`SELECT CountID,ShiftID,CountedBy,CountedAt,AmountCounted,ExpectedAmount,
		Difference,Denominations,Notes,ReviewStatus,ReviewedBy,ReviewedAt,ReviewNote
		FROM CashCounts WHERE ShiftID=? ORDER BY CountedAt, CountID`, shiftID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CashCount
	for rows.Next() {
		var c CashCount
		var counted, expected, diff int64
		var reviewedBy, reviewedAt, reviewNote sql.NullString
		if err := rows.Scan(&c.ID, &c.ShiftID, &c.CountedBy, &c.CountedAt, &counted, &expected,
			&diff, &c.Denominations, &c.Notes, &c.ReviewStatus, &reviewedBy, &reviewedAt, &reviewNote); err != nil {
			return nil, err
		}
		c.AmountCounted = money.Cents(counted)
		c.ExpectedAmount = money.Cents(expected)
		c.Difference = money.Cents(diff)
		c.DifferenceStatus = DifferenceStatus(c.Difference)
		c.ReviewedBy = reviewedBy.String
		c.ReviewedAt = reviewedAt.String
		c.ReviewNote = reviewNote.String
		out = append(out, &c)
	}
	return out, rows.Err()
}

// ListPendingCashCounts lists ALL pending cash counts (any shift, any
// user, newest first) for a reviewer — Manager/Admin, or a peer cashier
// reviewing another cashier's count. Non-reviewers see an empty list.
func (s *Store) ListPendingCashCounts(ctx context.Context, user *auth.User) ([]*CashCount, error) {
	if user == nil {
		return nil, errors.New("يلزم تسجيل الدخول")
	}
	if err := rbac.Require(user.Role, rbac.PermReviewCashCount); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(`SELECT CountID,ShiftID,CountedBy,CountedAt,AmountCounted,ExpectedAmount,
		Difference,Denominations,Notes,ReviewStatus,ReviewedBy,ReviewedAt,ReviewNote
		FROM CashCounts WHERE COALESCE(ReviewStatus,'pending')='pending' ORDER BY CountedAt DESC, CountID DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CashCount
	for rows.Next() {
		var c CashCount
		var counted, expected, diff int64
		var reviewedBy, reviewedAt, reviewNote sql.NullString
		if err := rows.Scan(&c.ID, &c.ShiftID, &c.CountedBy, &c.CountedAt, &counted, &expected,
			&diff, &c.Denominations, &c.Notes, &c.ReviewStatus, &reviewedBy, &reviewedAt, &reviewNote); err != nil {
			return nil, err
		}
		c.AmountCounted = money.Cents(counted)
		c.ExpectedAmount = money.Cents(expected)
		c.Difference = money.Cents(diff)
		c.DifferenceStatus = DifferenceStatus(c.Difference)
		c.ReviewedBy = reviewedBy.String
		c.ReviewedAt = reviewedAt.String
		c.ReviewNote = reviewNote.String
		out = append(out, &c)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- approvals log (peer approval, jony 2026-11-25)

// CashcountReview is one reviewed cash count for the approvals log
// (سجل اعتماد العدّات). Money is int64 cents; JSON keys match the
// Python service contract (count_id, reviewed_at, owner, approver,
// shift_id, expected, counted, difference, decision).
type CashcountReview struct {
	CountID    int64       `json:"count_id"`
	ReviewedAt string      `json:"reviewed_at"`
	Owner      string      `json:"owner"`
	Approver   string      `json:"approver"`
	ShiftID    int64       `json:"shift_id"`
	Expected   money.Cents `json:"expected"`
	Counted    money.Cents `json:"counted"`
	Difference money.Cents `json:"difference"`
	Decision   string      `json:"decision"` // approved | rejected
}

var dayPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
var monthPattern = regexp.MustCompile(`^\d{4}-\d{2}$`)

// validDay reports whether s is a real calendar day in YYYY-MM-DD form.
func validDay(s string) bool {
	if !dayPattern.MatchString(s) {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// validMonth reports whether s is a real calendar month in YYYY-MM form.
func validMonth(s string) bool {
	if !monthPattern.MatchString(s) {
		return false
	}
	_, err := time.Parse("2006-01", s)
	return err == nil
}

// ListCashcountReviews lists REVIEWED cash counts (approved/rejected,
// newest first) for the approvals log — Admin/Manager only
// (view_cashcount_approvals). Exactly ONE filter mode must be given:
// day ("YYYY-MM-DD", default: today when all four are empty), month
// ("YYYY-MM", the whole month), or a custom period (dateFrom+dateTo,
// inclusive on both ends — a single-day period works). Two modes at
// once, a lone bound, a bad format, or dateFrom > dateTo all raise an
// Arabic error.
func (s *Store) ListCashcountReviews(ctx context.Context, user *auth.User, day, month, dateFrom, dateTo string) ([]*CashcountReview, error) {
	if user == nil {
		return nil, errors.New("يلزم تسجيل الدخول")
	}
	if err := rbac.Require(user.Role, rbac.PermViewCashcountApprovals); err != nil {
		return nil, err
	}
	modes := 0
	if day != "" {
		modes++
	}
	if month != "" {
		modes++
	}
	if dateFrom != "" || dateTo != "" {
		modes++
	}
	if modes > 1 {
		return nil, errors.New("يجب اختيار وضع فلتر واحد فقط: يوم أو شهر أو مدة مخصصة")
	}
	var start, end string
	switch {
	case day != "":
		if !validDay(day) {
			return nil, errors.New("صيغة اليوم غير صحيحة — استخدم YYYY-MM-DD")
		}
		start, end = day, day
	case month != "":
		if !validMonth(month) {
			return nil, errors.New("صيغة الشهر غير صحيحة — استخدم YYYY-MM")
		}
		t, _ := time.Parse("2006-01", month)
		start = t.Format("2006-01-02")
		end = t.AddDate(0, 1, -1).Format("2006-01-02")
	case dateFrom != "" || dateTo != "":
		if dateFrom == "" || dateTo == "" {
			return nil, errors.New("يجب تحديد تاريخ البداية وتاريخ النهاية معاً")
		}
		if !validDay(dateFrom) || !validDay(dateTo) {
			return nil, errors.New("صيغة التاريخ غير صحيحة — استخدم YYYY-MM-DD")
		}
		if dateFrom > dateTo {
			return nil, errors.New("تاريخ البداية يجب أن يكون قبل تاريخ النهاية أو مساويًا له")
		}
		start, end = dateFrom, dateTo
	default:
		start, end = Today(), Today()
	}
	rows, err := s.DB.Query(`SELECT CountID,ReviewedAt,CountedBy,ReviewedBy,ShiftID,
		ExpectedAmount,AmountCounted,Difference,ReviewStatus
		FROM CashCounts WHERE ReviewStatus IN ('approved','rejected')
		AND substr(ReviewedAt,1,10) BETWEEN ? AND ?
		ORDER BY ReviewedAt DESC, CountID DESC`, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CashcountReview
	for rows.Next() {
		var r CashcountReview
		var expected, counted, diff int64
		var reviewedAt, reviewedBy sql.NullString
		if err := rows.Scan(&r.CountID, &reviewedAt, &r.Owner, &reviewedBy, &r.ShiftID,
			&expected, &counted, &diff, &r.Decision); err != nil {
			return nil, err
		}
		r.ReviewedAt = reviewedAt.String
		r.Approver = reviewedBy.String
		r.Expected = money.Cents(expected)
		r.Counted = money.Cents(counted)
		r.Difference = money.Cents(diff)
		out = append(out, &r)
	}
	return out, rows.Err()
}
