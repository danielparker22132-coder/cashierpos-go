package store

import (
	"context"
	"fmt"
	"log"
)

// Schema: all tables from FEATURE_MAP §0 + §18/§19 and the GAP-REPORT
// additions (Offers, Stocktakes, CashCounts, Payables, CourierDebts,
// ElectronicTransfers, Notifications). Money is INTEGER cents, quantities
// are INTEGER milliunits (x1000). Dates TEXT YYYY-MM-DD, timestamps
// TEXT "2006-01-02 15:04:05" Africa/Cairo.
const schema = `
CREATE TABLE IF NOT EXISTS Users (
	UserID INTEGER PRIMARY KEY AUTOINCREMENT,
	UserName TEXT UNIQUE NOT NULL,
	Password TEXT NOT NULL,
	FullName TEXT DEFAULT '',
	Role TEXT DEFAULT 'cashier',
	IsActive INTEGER DEFAULT 1,
	MustChangePassword INTEGER DEFAULT 0,
	CreatedDate TEXT
);
CREATE TABLE IF NOT EXISTS Products (
	ProductID INTEGER PRIMARY KEY AUTOINCREMENT,
	Barcode TEXT UNIQUE,
	ProductName TEXT NOT NULL,
	Category TEXT DEFAULT '',
	Supplier TEXT DEFAULT '',
	BuyPrice INTEGER NOT NULL DEFAULT 0,
	RetailPrice INTEGER NOT NULL DEFAULT 0,
	WholesalePrice INTEGER NOT NULL DEFAULT 0,
	BulkPrice INTEGER NOT NULL DEFAULT 0,
	Quantity INTEGER NOT NULL DEFAULT 0,
	MinQuantity INTEGER NOT NULL DEFAULT 5,
	Unit TEXT DEFAULT 'قطعة',
	ExpiryDate TEXT,
	BatchNumber TEXT DEFAULT '',
	ExpiryAlertDays INTEGER DEFAULT 30,
	IsExpiryTracked INTEGER DEFAULT 0,
	IsWeighted INTEGER DEFAULT 0,
	ScalePLU TEXT,
	PricePerKg INTEGER DEFAULT 0,
	ScaleBarcodeType TEXT DEFAULT 'WeightEmbedded',
	ScaleEnabled INTEGER DEFAULT 0,
	ScalePrefixOverride TEXT,
	IsActive INTEGER DEFAULT 1,
	CreatedDate TEXT
);
CREATE TABLE IF NOT EXISTS Customers (
	CustomerID INTEGER PRIMARY KEY AUTOINCREMENT,
	CustomerName TEXT NOT NULL,
	Phone TEXT DEFAULT '',
	Address TEXT DEFAULT '',
	Balance INTEGER NOT NULL DEFAULT 0,
	CreditLimit INTEGER NOT NULL DEFAULT 0,
	IsActive INTEGER DEFAULT 1,
	CreatedDate TEXT
);
CREATE TABLE IF NOT EXISTS Invoices (
	InvoiceID INTEGER PRIMARY KEY AUTOINCREMENT,
	InvoiceNumber INTEGER UNIQUE NOT NULL,
	CustomerID INTEGER DEFAULT 1,
	SaleType TEXT DEFAULT 'retail',
	TotalAmount INTEGER NOT NULL DEFAULT 0,
	Discount INTEGER NOT NULL DEFAULT 0,
	PromoDiscount INTEGER NOT NULL DEFAULT 0,
	FreeCost INTEGER NOT NULL DEFAULT 0,
	NetAmount INTEGER NOT NULL DEFAULT 0,
	PaidAmount INTEGER NOT NULL DEFAULT 0,
	RemainingAmount INTEGER NOT NULL DEFAULT 0,
	PaymentMethod TEXT DEFAULT 'Cash',
	Notes TEXT DEFAULT '',
	UserID INTEGER,
	UserName TEXT DEFAULT '',
	ShiftID INTEGER,
	InvoiceDate TEXT,
	IsCanceled INTEGER DEFAULT 0,
	CanceledBy TEXT DEFAULT '',
	CanceledAt TEXT,
	CancelReason TEXT DEFAULT ''
);
CREATE TABLE IF NOT EXISTS InvoiceDetails (
	DetailID INTEGER PRIMARY KEY AUTOINCREMENT,
	InvoiceID INTEGER NOT NULL,
	ProductID INTEGER,
	ProductName TEXT,
	Quantity INTEGER NOT NULL DEFAULT 1000,
	UnitPrice INTEGER NOT NULL DEFAULT 0,
	BuyPrice INTEGER NOT NULL DEFAULT 0,
	ItemDiscount INTEGER NOT NULL DEFAULT 0,
	PromoDiscount INTEGER NOT NULL DEFAULT 0,
	FreeQty INTEGER NOT NULL DEFAULT 0,
	TotalPrice INTEGER NOT NULL DEFAULT 0,
	IsWeighted INTEGER DEFAULT 0,
	ScaleBarcode TEXT DEFAULT '',
	ScalePLU TEXT DEFAULT '',
	ScaleBarcodeType TEXT DEFAULT '',
	FOREIGN KEY(InvoiceID) REFERENCES Invoices(InvoiceID),
	FOREIGN KEY(ProductID) REFERENCES Products(ProductID)
);
CREATE TABLE IF NOT EXISTS Payments (
	PaymentID INTEGER PRIMARY KEY AUTOINCREMENT,
	InvoiceID INTEGER,
	CustomerID INTEGER,
	Amount INTEGER NOT NULL,
	PaymentDate TEXT,
	PaymentMethod TEXT DEFAULT 'Cash',
	Notes TEXT DEFAULT '',
	UserID INTEGER,
	FOREIGN KEY(InvoiceID) REFERENCES Invoices(InvoiceID),
	FOREIGN KEY(CustomerID) REFERENCES Customers(CustomerID)
);
CREATE TABLE IF NOT EXISTS CustomerPayments (
	CPaymentID INTEGER PRIMARY KEY AUTOINCREMENT,
	CustomerID INTEGER NOT NULL,
	InvoiceID INTEGER,
	Amount INTEGER NOT NULL,
	PaymentDate TEXT,
	PaymentMethod TEXT DEFAULT 'Cash',
	ReceivedBy TEXT DEFAULT '',
	UserID INTEGER,
	Notes TEXT DEFAULT '',
	ShiftID INTEGER,
	FOREIGN KEY(CustomerID) REFERENCES Customers(CustomerID),
	FOREIGN KEY(InvoiceID) REFERENCES Invoices(InvoiceID)
);
CREATE TABLE IF NOT EXISTS StockMovements (
	MovementID INTEGER PRIMARY KEY AUTOINCREMENT,
	ProductID INTEGER,
	MovementType TEXT,
	Quantity INTEGER NOT NULL DEFAULT 0,
	BuyPrice INTEGER DEFAULT 0,
	Notes TEXT DEFAULT '',
	UserID INTEGER,
	MovementDate TEXT,
	FOREIGN KEY(ProductID) REFERENCES Products(ProductID)
);
CREATE TABLE IF NOT EXISTS Returns (
	ReturnID INTEGER PRIMARY KEY AUTOINCREMENT,
	InvoiceID INTEGER,
	ProductID INTEGER,
	ProductName TEXT,
	Quantity INTEGER NOT NULL DEFAULT 0,
	UnitPrice INTEGER NOT NULL DEFAULT 0,
	BuyPrice INTEGER NOT NULL DEFAULT 0,
	TotalRefund INTEGER NOT NULL DEFAULT 0,
	VoidedFreeQty INTEGER NOT NULL DEFAULT 0,
	VoidedFreeCost INTEGER NOT NULL DEFAULT 0,
	Reason TEXT DEFAULT '',
	UserID INTEGER,
	UserName TEXT DEFAULT '',
	ReturnDate TEXT,
	ReturnPaymentMethod TEXT DEFAULT 'Cash',
	ShiftID INTEGER,
	-- PR10-L1: the event id of the return event this row belongs
	-- to — the first Returns row's ID of the ProcessReturn call
	-- that wrote it (stamped by the writer; 0 = legacy row
	-- written before fix-10). One cash leg covers a whole return
	-- event, so the leg names the event, not a single row: the
	-- leg's ReturnID IS this id, and returnLegDay/returnsRowDay
	-- resolve the event's day through any of its rows.
	ReturnEventID INTEGER NOT NULL DEFAULT 0,
	IsProcessed INTEGER DEFAULT 1,
	FOREIGN KEY(InvoiceID) REFERENCES Invoices(InvoiceID),
	FOREIGN KEY(ProductID) REFERENCES Products(ProductID)
);
CREATE TABLE IF NOT EXISTS Expenses (
	ExpenseID INTEGER PRIMARY KEY AUTOINCREMENT,
	Description TEXT NOT NULL,
	Amount INTEGER NOT NULL,
	Category TEXT DEFAULT '',
	UserID INTEGER,
	ShiftID INTEGER,
	ExpenseDate TEXT
);
CREATE TABLE IF NOT EXISTS UserAttendance (
	ID INTEGER PRIMARY KEY AUTOINCREMENT,
	UserName TEXT,
	LogDate TEXT,
	FirstLoginTime TEXT NOT NULL,
	LastLogoutTime TEXT,
	LoginCount INTEGER DEFAULT 1,
	IsOnline INTEGER DEFAULT 0,
	UNIQUE(UserName, LogDate)
);
CREATE TABLE IF NOT EXISTS Shifts (
	ShiftID INTEGER PRIMARY KEY AUTOINCREMENT,
	UserID INTEGER,
	UserName TEXT DEFAULT '',
	ShiftDate TEXT,
	OpenTime TEXT,
	CloseTime TEXT,
	CloseDateTime TEXT,
	Status TEXT DEFAULT 'Open',
	OpeningBalance INTEGER NOT NULL DEFAULT 0,
	ExpectedBalance INTEGER,
	ActualBalance INTEGER,
	Difference INTEGER,
	TotalSalesCash INTEGER DEFAULT 0,
	TotalSalesCredit INTEGER DEFAULT 0,
	TotalReturns INTEGER DEFAULT 0,
	TotalExpenses INTEGER DEFAULT 0,
	Notes TEXT DEFAULT '',
	IsOpen INTEGER DEFAULT 1,
	SessionCount INTEGER DEFAULT 1,
	CashSalesTotal INTEGER DEFAULT 0,
	VisaSalesTotal INTEGER DEFAULT 0,
	VodafoneCashSalesTotal INTEGER DEFAULT 0,
	CashDebtCollectionTotal INTEGER DEFAULT 0,
	VisaDebtCollectionTotal INTEGER DEFAULT 0,
	VodafoneDebtCollectionTotal INTEGER DEFAULT 0,
	CashInTotal INTEGER DEFAULT 0,
	CashOutTotal INTEGER DEFAULT 0,
	ExpenseTotal INTEGER DEFAULT 0,
	CashReturnTotal INTEGER DEFAULT 0,
	RetainedCanceledCashTotal INTEGER DEFAULT 0,
	ExpectedDrawerBalance INTEGER DEFAULT 0,
	ActualDrawerBalance INTEGER DEFAULT 0,
	DifferenceAmount INTEGER DEFAULT 0,
	DifferenceStatus TEXT DEFAULT ''
);
CREATE TABLE IF NOT EXISTS ShiftSessions (
	SessionID INTEGER PRIMARY KEY AUTOINCREMENT,
	ShiftID INTEGER,
	UserID INTEGER,
	UserName TEXT DEFAULT '',
	SessionDate TEXT,
	OpenTime TEXT,
	CloseTime TEXT,
	IsOpen INTEGER DEFAULT 1
);
CREATE TABLE IF NOT EXISTS CashBox (
	CashBoxID INTEGER PRIMARY KEY AUTOINCREMENT,
	UserID INTEGER,
	UserName TEXT DEFAULT '',
	ShiftID INTEGER,
	MovementType TEXT,
	Amount INTEGER NOT NULL,
	Reason TEXT DEFAULT '',
	MovementDate TEXT,
	-- FIX-2 L1: structural classification of system-written rows.
	-- AutoKind is '' for manual rows and legacy rows (backfilled by
	-- migrate()); 'sale' | 'customer_collection' | 'courier_collection'
	-- | 'return' | 'cancel_refund' for automatic rows. RefInvoiceID is
	-- the invoice a return / cancel-refund row refers to (0 otherwise);
	-- it lets the shift summary tell a same-shift cancel refund (sale
	-- already excluded from CashSales) from a cross-shift one (FIX-2 H-1).
	-- PR9-L1: ReturnID is the Returns row a 'return' leg belongs to
	-- (0 = legacy/manual row with no link). One cash leg covers a
	-- whole return event and the writer stamps the event's first
	-- Returns row; every row of the event shares ReturnDate, ShiftID
	-- and InvoiceID, so that ID identifies the event (see
	-- Returns.ReturnEventID, PR10-L1) and lets returnLegDay read
	-- the event's day through any of its rows — and lets
	-- returnsRowDay read its own leg's date back. A leg with no
	-- Returns rows (cannot happen when cashRefund > 0) keeps
	-- ReturnID 0 and keeps the legacy fallback.
	AutoKind TEXT NOT NULL DEFAULT '',
	RefInvoiceID INTEGER NOT NULL DEFAULT 0,
	ReturnID INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS AuditLog (
	LogID INTEGER PRIMARY KEY AUTOINCREMENT,
	UserID INTEGER,
	UserName TEXT DEFAULT '',
	ActionType TEXT NOT NULL,
	ActionCategory TEXT DEFAULT '',
	InvoiceNumber INTEGER,
	InvoiceID INTEGER,
	CustomerID INTEGER,
	CustomerName TEXT DEFAULT '',
	Notes TEXT DEFAULT '',
	LogDate TEXT
);
CREATE TABLE IF NOT EXISTS AuditLogDetails (
	DetailID INTEGER PRIMARY KEY AUTOINCREMENT,
	LogID INTEGER,
	ProductID INTEGER,
	ProductName TEXT DEFAULT '',
	QuantityBefore INTEGER,
	QuantityAfter INTEGER,
	QuantityChange INTEGER,
	PriceBefore INTEGER,
	PriceAfter INTEGER,
	LineTotal INTEGER,
	Notes TEXT DEFAULT ''
);
CREATE TABLE IF NOT EXISTS AuditAlerts (
	AlertID INTEGER PRIMARY KEY AUTOINCREMENT,
	LogID INTEGER,
	UserName TEXT DEFAULT '',
	AlertType TEXT NOT NULL,
	AlertMessage TEXT NOT NULL,
	AlertValue INTEGER,
	Threshold INTEGER,
	IsRead INTEGER DEFAULT 0,
	IsResolved INTEGER DEFAULT 0,
	ResolvedBy TEXT DEFAULT '',
	ResolvedNote TEXT DEFAULT '',
	AlertDate TEXT
);
CREATE TABLE IF NOT EXISTS AppSettings (
	Key TEXT PRIMARY KEY,
	Value TEXT
);
CREATE TABLE IF NOT EXISTS ScaleSettings (
	ID INTEGER PRIMARY KEY,
	IsEnabled INTEGER DEFAULT 1,
	BarcodePrefix TEXT DEFAULT '20',
	ProductCodeLength INTEGER DEFAULT 5,
	ValueLength INTEGER DEFAULT 5,
	BarcodeMode TEXT DEFAULT 'WeightEmbedded',
	HasCheckDigit INTEGER DEFAULT 1,
	WeightUnit TEXT DEFAULT 'Kilogram',
	ValueDecimalPlaces INTEGER DEFAULT 3,
	Notes TEXT DEFAULT ''
);
CREATE TABLE IF NOT EXISTS Vendors (
	VendorID INTEGER PRIMARY KEY AUTOINCREMENT,
	VendorName TEXT NOT NULL,
	Phone TEXT DEFAULT '',
	Address TEXT DEFAULT '',
	Notes TEXT DEFAULT '',
	CreditLimit INTEGER NOT NULL DEFAULT 0,
	CurrentBalance INTEGER NOT NULL DEFAULT 0,
	IsActive INTEGER DEFAULT 1,
	CreatedDate TEXT
);
CREATE TABLE IF NOT EXISTS PurchaseInvoices (
	PurchaseID INTEGER PRIMARY KEY AUTOINCREMENT,
	InvoiceNumber TEXT,
	VendorID INTEGER,
	PurchaseDate TEXT,
	TotalAmount INTEGER NOT NULL DEFAULT 0,
	PaidAmount INTEGER NOT NULL DEFAULT 0,
	RemainingAmount INTEGER NOT NULL DEFAULT 0,
	DueDate TEXT,
	PaymentMethod TEXT DEFAULT 'Cash',
	UserID INTEGER,
	UserName TEXT DEFAULT '',
	ShiftID INTEGER,
	Notes TEXT DEFAULT '',
	IsCanceled INTEGER DEFAULT 0,
	-- FIX-2 L3: vendor link (was unconstrained; old databases gain it
	-- via the migrate() table rebuild below).
	FOREIGN KEY(VendorID) REFERENCES Vendors(VendorID)
);
CREATE TABLE IF NOT EXISTS PurchaseInvoiceDetails (
	DetailID INTEGER PRIMARY KEY AUTOINCREMENT,
	PurchaseID INTEGER NOT NULL,
	ProductID INTEGER,
	ProductName TEXT,
	Quantity INTEGER NOT NULL DEFAULT 0,
	PurchasePrice INTEGER NOT NULL DEFAULT 0,
	TotalPrice INTEGER NOT NULL DEFAULT 0,
	BatchNumber TEXT DEFAULT '',
	ExpiryDate TEXT,
	FOREIGN KEY(PurchaseID) REFERENCES PurchaseInvoices(PurchaseID),
	FOREIGN KEY(ProductID) REFERENCES Products(ProductID)
);
CREATE TABLE IF NOT EXISTS VendorPayments (
	VendorPaymentID INTEGER PRIMARY KEY AUTOINCREMENT,
	VendorID INTEGER NOT NULL,
	PurchaseID INTEGER,
	Amount INTEGER NOT NULL,
	PaymentMethod TEXT DEFAULT 'Cash',
	PaymentDate TEXT,
	UserID INTEGER,
	UserName TEXT DEFAULT '',
	ShiftID INTEGER,
	BalanceBefore INTEGER DEFAULT 0,
	BalanceAfter INTEGER DEFAULT 0,
	Notes TEXT DEFAULT '',
	FOREIGN KEY(VendorID) REFERENCES Vendors(VendorID),
	FOREIGN KEY(PurchaseID) REFERENCES PurchaseInvoices(PurchaseID)
);
CREATE TABLE IF NOT EXISTS ProductBatches (
	BatchID INTEGER PRIMARY KEY AUTOINCREMENT,
	ProductID INTEGER,
	BatchNumber TEXT DEFAULT '',
	Quantity INTEGER NOT NULL DEFAULT 0,
	ExpiryDate TEXT,
	PurchasePrice INTEGER DEFAULT 0,
	VendorID INTEGER,
	PurchaseID INTEGER,
	CreatedDate TEXT,
	-- FIX-2 L3: vendor / purchase links (were unconstrained; old
	-- databases gain them via the migrate() table rebuild below).
	FOREIGN KEY(VendorID) REFERENCES Vendors(VendorID),
	FOREIGN KEY(PurchaseID) REFERENCES PurchaseInvoices(PurchaseID)
);
CREATE TABLE IF NOT EXISTS Offers (
	OfferID INTEGER PRIMARY KEY AUTOINCREMENT,
	Name TEXT NOT NULL,
	Type TEXT NOT NULL,
	Value TEXT NOT NULL DEFAULT '0',
	Threshold TEXT NOT NULL DEFAULT '0',
	FreeQty TEXT NOT NULL DEFAULT '0',
	StartAt TEXT,
	EndAt TEXT,
	IsActive INTEGER DEFAULT 1,
	CreatedBy TEXT DEFAULT '',
	CreatedAt TEXT
);
CREATE TABLE IF NOT EXISTS OfferTargets (
	OfferID INTEGER NOT NULL,
	TargetType TEXT NOT NULL,
	TargetID INTEGER,
	TargetName TEXT DEFAULT '',
	Qty TEXT NOT NULL DEFAULT '0'
);
CREATE TABLE IF NOT EXISTS InvoiceOffers (
	InvoiceID INTEGER NOT NULL,
	OfferID INTEGER,
	OfferName TEXT DEFAULT '',
	SavedAmount INTEGER NOT NULL DEFAULT 0,
	ProductID INTEGER,
	BuyQty INTEGER,
	FreeQty INTEGER,
	FOREIGN KEY(InvoiceID) REFERENCES Invoices(InvoiceID),
	FOREIGN KEY(OfferID) REFERENCES Offers(OfferID),
	FOREIGN KEY(ProductID) REFERENCES Products(ProductID)
);
CREATE TABLE IF NOT EXISTS Stocktakes (
	StocktakeID INTEGER PRIMARY KEY AUTOINCREMENT,
	CreatedBy TEXT DEFAULT '',
	CreatedAt TEXT,
	Status TEXT DEFAULT 'open',
	ClosedAt TEXT,
	ClosedBy TEXT DEFAULT '',
	ApprovedBy TEXT DEFAULT '',
	ApprovedAt TEXT,
	ApproveReason TEXT DEFAULT '',
	Notes TEXT DEFAULT ''
);
CREATE TABLE IF NOT EXISTS StocktakeLines (
	LineID INTEGER PRIMARY KEY AUTOINCREMENT,
	StocktakeID INTEGER NOT NULL,
	ProductID INTEGER NOT NULL,
	ProductName TEXT DEFAULT '',
	SystemQty INTEGER NOT NULL DEFAULT 0,
	CountedQty INTEGER,
	CreatedAt TEXT
);
CREATE TABLE IF NOT EXISTS CashCounts (
	CountID INTEGER PRIMARY KEY AUTOINCREMENT,
	ShiftID INTEGER,
	CountedBy TEXT DEFAULT '',
	CountedAt TEXT,
	AmountCounted INTEGER NOT NULL DEFAULT 0,
	ExpectedAmount INTEGER NOT NULL DEFAULT 0,
	Difference INTEGER NOT NULL DEFAULT 0,
	Denominations TEXT DEFAULT '',
	Notes TEXT DEFAULT '',
	ReviewStatus TEXT DEFAULT 'pending',
	ReviewedBy TEXT DEFAULT '',
	ReviewedAt TEXT,
	ReviewNote TEXT DEFAULT ''
);
CREATE TABLE IF NOT EXISTS Payables (
	PayableID INTEGER PRIMARY KEY AUTOINCREMENT,
	Kind TEXT NOT NULL,
	PartyID INTEGER NOT NULL,
	PartyName TEXT DEFAULT '',
	Amount INTEGER NOT NULL DEFAULT 0,
	Remaining INTEGER NOT NULL DEFAULT 0,
	DueDate TEXT,
	Description TEXT DEFAULT '',
	Status TEXT DEFAULT 'open',
	CreatedBy TEXT DEFAULT '',
	CreatedAt TEXT,
	PaidAt TEXT
);
CREATE TABLE IF NOT EXISTS CourierDebts (
	DebtID INTEGER PRIMARY KEY AUTOINCREMENT,
	CourierName TEXT NOT NULL,
	Amount INTEGER NOT NULL DEFAULT 0,
	OrderRef TEXT DEFAULT '',
	InvoiceNumber INTEGER,
	Status TEXT DEFAULT 'open',
	CreatedAt TEXT,
	PaidAt TEXT,
	PaidBy TEXT DEFAULT ''
);
CREATE TABLE IF NOT EXISTS ElectronicTransfers (
	TransferID INTEGER PRIMARY KEY AUTOINCREMENT,
	SenderName TEXT DEFAULT '',
	Amount INTEGER NOT NULL DEFAULT 0,
	Method TEXT DEFAULT '',
	Reference TEXT DEFAULT '',
	Status TEXT DEFAULT 'pending',
	CreatedAt TEXT,
	ReviewedBy TEXT DEFAULT '',
	ReviewedAt TEXT,
	ReviewNote TEXT DEFAULT ''
);
CREATE TABLE IF NOT EXISTS Notifications (
	NotificationID INTEGER PRIMARY KEY AUTOINCREMENT,
	Type TEXT NOT NULL,
	Title TEXT NOT NULL,
	Body TEXT DEFAULT '',
	LinkedProductID INTEGER,
	Audience TEXT DEFAULT 'all',
	UserName TEXT DEFAULT '',
	IsRead INTEGER DEFAULT 0,
	CreatedAt TEXT,
	SendAt TEXT,
	Status TEXT DEFAULT 'sent'
);
CREATE TABLE IF NOT EXISTS NotificationSettings (
	UserName TEXT NOT NULL,
	NotifType TEXT NOT NULL,
	Enabled INTEGER DEFAULT 1,
	PRIMARY KEY(UserName, NotifType)
);
CREATE TABLE IF NOT EXISTS NotificationSendLog (
	LogID INTEGER PRIMARY KEY AUTOINCREMENT,
	NotificationID INTEGER,
	UserName TEXT DEFAULT '',
	EventKey TEXT DEFAULT '',
	SentAt TEXT,
	Status TEXT NOT NULL DEFAULT 'sent',
	UNIQUE(UserName, EventKey)
);
CREATE TABLE IF NOT EXISTS Sessions (
	Token TEXT PRIMARY KEY,
	UserID INTEGER NOT NULL,
	UserName TEXT NOT NULL,
	Role TEXT NOT NULL,
	CreatedAt TEXT,
	ExpiresAt TEXT
);
CREATE TABLE IF NOT EXISTS OnlineOrders (
	OrderID INTEGER PRIMARY KEY AUTOINCREMENT,
	CustomerName TEXT NOT NULL DEFAULT '',
	CustomerPhone TEXT DEFAULT '',
	ItemsJSON TEXT NOT NULL DEFAULT '[]',
	TotalAmount INTEGER NOT NULL DEFAULT 0,
	Status TEXT NOT NULL DEFAULT 'CREATED',
	PaymentStatus TEXT NOT NULL DEFAULT 'PENDING',
	PaymentMethod TEXT DEFAULT '',
	EvidenceRef TEXT DEFAULT '',
	Notes TEXT DEFAULT '',
	CreatedBy TEXT DEFAULT '',
	CreatedAt TEXT,
	UpdatedAt TEXT
);
CREATE INDEX IF NOT EXISTS idx_online_orders_status ON OnlineOrders(Status);
CREATE INDEX IF NOT EXISTS idx_online_orders_paystatus ON OnlineOrders(PaymentStatus);
CREATE TABLE IF NOT EXISTS Categories (
	CategoryID INTEGER PRIMARY KEY AUTOINCREMENT,
	Name TEXT UNIQUE NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_invoices_date ON Invoices(InvoiceDate);
CREATE INDEX IF NOT EXISTS idx_invoices_customer ON Invoices(CustomerID);
CREATE INDEX IF NOT EXISTS idx_cashbox_shift ON CashBox(ShiftID);
-- PR6-L2: DailyClose's per-orphan InvoiceNumber lookups
-- (cancelRefundInvoiceID / cancelRefundInvoiceShift) and its
-- AutoKind scans were full table scans — the fix-5 count/fallback
-- work made DailyClose O(total history) per call. The schema string
-- is re-executed by migrate() on every open, so CREATE INDEX IF NOT
-- EXISTS also backfills databases created before the index existed.
-- (Measured: the InvoiceNumber lookups already used SQLite's UNIQUE
-- autoindex, so idx_invoices_number changes no plan — it is kept as
-- the explicit, named index for that lookup. The real win is the
-- CashBox composite: refundTodayTx runs once per canceled invoice
-- sold today with WHERE RefInvoiceID=? AND AutoKind='cancel_refund',
-- which scanned every cancel_refund row per invoice; the composite
-- jumps straight to the invoice's rows.)
CREATE INDEX IF NOT EXISTS idx_invoices_number ON Invoices(InvoiceNumber);
-- PR10-L3: idx_cashbox_autokind_ref used to live here, in the schema
-- string — but AutoKind/RefInvoiceID are added by ALTERs in
-- migrate(), which runs AFTER the schema string, so creating the
-- index here aborted reopening a database created before fix-2
-- ("no such column: AutoKind"). It is created in migrate() after
-- the ALTERs now, the same pattern fix-9 used for
-- idx_cashbox_returnid. (PR6-L2 comment above still describes why
-- the composite exists; only its home moved.)
CREATE INDEX IF NOT EXISTS idx_audit_date ON AuditLog(LogDate);
CREATE INDEX IF NOT EXISTS idx_products_barcode ON Products(Barcode);
`

func (s *Store) migrate() error {
	if _, err := s.DB.Exec(schema); err != nil {
		return err
	}
	// Additive migrations for databases created before a column existed.
	// Errors are ignored when the column already exists.
	_, _ = s.DB.Exec(`ALTER TABLE CustomerPayments ADD COLUMN PaymentMethod TEXT DEFAULT 'Cash'`)
	// FIX-2 L1: structural CashBox classification (see the CashBox DDL).
	_, _ = s.DB.Exec(`ALTER TABLE CashBox ADD COLUMN AutoKind TEXT NOT NULL DEFAULT ''`)
	_, _ = s.DB.Exec(`ALTER TABLE CashBox ADD COLUMN RefInvoiceID INTEGER NOT NULL DEFAULT 0`)
	// PR10-L3: created HERE, after the ALTERs above — not in the
	// schema string, which runs before them: a database created
	// before fix-2 has no AutoKind/RefInvoiceID columns, so
	// creating the index there aborted the server on reopen ("no
	// such column: AutoKind"). Same pattern as idx_cashbox_returnid.
	_, _ = s.DB.Exec(`CREATE INDEX IF NOT EXISTS idx_cashbox_autokind_ref ON CashBox(AutoKind, RefInvoiceID)`)
	// PR9-L1: leg → Returns-row link for 'return' legs (see the
	// CashBox DDL). Legacy rows keep ReturnID 0 and keep the
	// sibling/guard fallback in returnLegDay.
	_, _ = s.DB.Exec(`ALTER TABLE CashBox ADD COLUMN ReturnID INTEGER NOT NULL DEFAULT 0`)
	// PR9-L1: the row-side reverse lookup (returnsRowDay reads its
	// own leg's MovementDate via ReturnID) must not scan CashBox per
	// row. Created here — AFTER the ALTER above — not in the schema
	// string: the schema string runs before the ALTERs, so an index
	// on the new column there would fail reopening a database
	// created before the column existed ("no such column:
	// ReturnID").
	_, _ = s.DB.Exec(`CREATE INDEX IF NOT EXISTS idx_cashbox_returnid ON CashBox(ReturnID)`)
	// PR10-L1: the event id of the return event a Returns row
	// belongs to (the first row's ID of its ProcessReturn call;
	// 0 = legacy row written before fix-10). Stamped by the
	// writer on every row of the event; the CashBox leg already
	// stamps the same id as its ReturnID. The index lives here —
	// after the ALTER — not in the schema string, for the same
	// reason as idx_cashbox_returnid above.
	_, _ = s.DB.Exec(`ALTER TABLE Returns ADD COLUMN ReturnEventID INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.DB.Exec(`CREATE INDEX IF NOT EXISTS idx_returns_event ON Returns(ReturnEventID)`)
	// FIX-3 L8b: the counting user's id (immune to username reuse).
	_, _ = s.DB.Exec(`ALTER TABLE CashCounts ADD COLUMN CountedByUserID INTEGER NOT NULL DEFAULT 0`)
	// FIX-3 M2: persisted last activity for the idle clock (survives restart).
	_, _ = s.DB.Exec(`ALTER TABLE Sessions ADD COLUMN LastActivity TEXT`)
	// FIX-12 SW-M13: send-log status (sent | skipped_disabled |
	// skipped_duplicate) so a skip is never silent. Legacy rows default
	// to 'sent'.
	_, _ = s.DB.Exec(`ALTER TABLE NotificationSendLog ADD COLUMN Status TEXT NOT NULL DEFAULT 'sent'`)
	// PR4-L3: stored total for sale cash retained from canceled
	// invoices (the RetainedCanceledCash bucket).
	_, _ = s.DB.Exec(`ALTER TABLE Shifts ADD COLUMN RetainedCanceledCashTotal INTEGER DEFAULT 0`)
	if err := s.backfillCashBoxKinds(); err != nil {
		return err
	}
	// FIX-2 L3: FKs on PurchaseInvoices / ProductBatches for old DBs.
	if err := s.ensurePurchaseFKs(); err != nil {
		return err
	}
	return nil
}

// backfillCashBoxKinds classifies pre-FIX-2 CashBox rows structurally from
// their (writer-produced, fixed-format) reasons, and links return /
// cancel-refund rows to their invoice. Idempotent: only rows with
// AutoKind=” are touched, so a second run is a no-op. Rows whose reason
// matches no known automatic pattern stay ” (manual).
func (s *Store) backfillCashBoxKinds() error {
	_, _ = s.DB.Exec(`UPDATE CashBox SET AutoKind='sale' WHERE AutoKind='' AND Reason LIKE 'مبيعات - %'`)
	_, _ = s.DB.Exec(`UPDATE CashBox SET AutoKind='customer_collection' WHERE AutoKind='' AND (Reason='تحصيل مديونية' OR Reason LIKE 'سداد دين عميل%')`)
	_, _ = s.DB.Exec(`UPDATE CashBox SET AutoKind='courier_collection' WHERE AutoKind='' AND Reason LIKE 'سداد دين مندوب%'`)
	_, _ = s.DB.Exec(`UPDATE CashBox SET AutoKind='return' WHERE AutoKind='' AND Reason LIKE 'مرتجع فاتورة %'`)
	_, _ = s.DB.Exec(`UPDATE CashBox SET AutoKind='cancel_refund' WHERE AutoKind='' AND Reason LIKE 'استرداد إلغاء فاتورة %'`)
	// Link return / cancel-refund rows to their invoice: the number is
	// embedded in the reason ("مرتجع فاتورة %d - …" /
	// "استرداد إلغاء فاتورة %d - كاش"). No query runs inside the rows
	// loop (MaxOpenConns(1) deadlock): collect first, resolve after.
	rows, err := s.DB.Query(`SELECT CashBoxID,Reason FROM CashBox
		WHERE RefInvoiceID=0 AND AutoKind IN ('return','cancel_refund')`)
	if err != nil {
		return err
	}
	type link struct{ id, num int64 }
	var links []link
	for rows.Next() {
		var id, num int64
		var reason string
		if err := rows.Scan(&id, &reason); err != nil {
			rows.Close()
			return err
		}
		if _, err := fmt.Sscanf(reason, "مرتجع فاتورة %d", &num); err != nil {
			if _, err := fmt.Sscanf(reason, "استرداد إلغاء فاتورة %d", &num); err != nil {
				continue
			}
		}
		links = append(links, link{id, num})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, l := range links {
		var invID int64
		if err := s.DB.QueryRow(`SELECT InvoiceID FROM Invoices WHERE InvoiceNumber=?`, l.num).Scan(&invID); err != nil {
			continue // invoice number unknown: leave RefInvoiceID 0
		}
		if _, err := s.DB.Exec(`UPDATE CashBox SET RefInvoiceID=? WHERE CashBoxID=?`, invID, l.id); err != nil {
			return err
		}
	}
	return nil
}

// fkRebuild describes one table rebuild that adds foreign keys.
type fkRebuild struct {
	table      string
	fks        [][2]string // {fromCol, refTable} pairs the rebuild adds
	orphanSQLs []string    // COUNT(*) of rows that would violate any new FK
	newDDL     string      // CREATE TABLE <table>_new (...) — must list every column
	columns    string      // column list for the copy, same order as newDDL
}

// ensurePurchaseFKs adds the FIX-2 L3 foreign keys to databases created
// before them. SQLite cannot ADD a FK with ALTER, so each table is
// rebuilt (new table with the FK, copy, drop, rename) inside one
// transaction with foreign_keys OFF, then verified with
// foreign_key_check. A table is skipped when its FK already exists, or
// when orphan rows would violate the new FK — the orphans are kept and
// the COUNT guards in DeleteVendor stay the enforcement for that table
// (the skip is deliberate, not a silent drop).
func (s *Store) ensurePurchaseFKs() error {
	jobs := []fkRebuild{
		{
			table: "PurchaseInvoices",
			fks:   [][2]string{{"VendorID", "Vendors"}},
			orphanSQLs: []string{
				`SELECT COUNT(*) FROM PurchaseInvoices WHERE VendorID IS NOT NULL AND VendorID NOT IN (SELECT VendorID FROM Vendors)`,
			},
			newDDL: `CREATE TABLE PurchaseInvoices_new (
				PurchaseID INTEGER PRIMARY KEY AUTOINCREMENT,
				InvoiceNumber TEXT,
				VendorID INTEGER,
				PurchaseDate TEXT,
				TotalAmount INTEGER NOT NULL DEFAULT 0,
				PaidAmount INTEGER NOT NULL DEFAULT 0,
				RemainingAmount INTEGER NOT NULL DEFAULT 0,
				DueDate TEXT,
				PaymentMethod TEXT DEFAULT 'Cash',
				UserID INTEGER,
				UserName TEXT DEFAULT '',
				ShiftID INTEGER,
				Notes TEXT DEFAULT '',
				IsCanceled INTEGER DEFAULT 0,
				FOREIGN KEY(VendorID) REFERENCES Vendors(VendorID)
			)`,
			columns: `PurchaseID,InvoiceNumber,VendorID,PurchaseDate,TotalAmount,PaidAmount,RemainingAmount,DueDate,PaymentMethod,UserID,UserName,ShiftID,Notes,IsCanceled`,
		},
		{
			table: "ProductBatches",
			fks:   [][2]string{{"VendorID", "Vendors"}, {"PurchaseID", "PurchaseInvoices"}},
			orphanSQLs: []string{
				`SELECT COUNT(*) FROM ProductBatches WHERE VendorID IS NOT NULL AND VendorID NOT IN (SELECT VendorID FROM Vendors)`,
				`SELECT COUNT(*) FROM ProductBatches WHERE PurchaseID IS NOT NULL AND PurchaseID NOT IN (SELECT PurchaseID FROM PurchaseInvoices)`,
			},
			newDDL: `CREATE TABLE ProductBatches_new (
				BatchID INTEGER PRIMARY KEY AUTOINCREMENT,
				ProductID INTEGER,
				BatchNumber TEXT DEFAULT '',
				Quantity INTEGER NOT NULL DEFAULT 0,
				ExpiryDate TEXT,
				PurchasePrice INTEGER DEFAULT 0,
				VendorID INTEGER,
				PurchaseID INTEGER,
				CreatedDate TEXT,
				FOREIGN KEY(VendorID) REFERENCES Vendors(VendorID),
				FOREIGN KEY(PurchaseID) REFERENCES PurchaseInvoices(PurchaseID)
			)`,
			columns: `BatchID,ProductID,BatchNumber,Quantity,ExpiryDate,PurchasePrice,VendorID,PurchaseID,CreatedDate`,
		},
	}
	for _, j := range jobs {
		// Skip when every FK of this rebuild is already present.
		pending := false
		for _, fk := range j.fks {
			if s.tableMissingFK(j.table, fk[0], fk[1]) {
				pending = true
				break
			}
		}
		if !pending {
			continue
		}
		// Skip the whole rebuild when ANY new FK would be violated —
		// the orphans are kept and the COUNT guards in DeleteVendor
		// stay the enforcement for that table.
		skip := false
		for _, q := range j.orphanSQLs {
			var orphans int
			if err := s.DB.QueryRow(q).Scan(&orphans); err != nil {
				return err
			}
			if orphans > 0 {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		if err := s.rebuildTable(j); err != nil {
			return err
		}
	}
	return nil
}

// tableMissingFK reports whether table lacks a FK from fromCol to refTable.
func (s *Store) tableMissingFK(table, fromCol, refTable string) bool {
	rows, err := s.DB.Query(fmt.Sprintf(`PRAGMA foreign_key_list(%s)`, table))
	if err != nil {
		return true
	}
	defer rows.Close()
	for rows.Next() {
		var id, seq int
		var tbl, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &tbl, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			continue
		}
		if tbl == refTable && from == fromCol {
			return false
		}
	}
	return true
}

// rebuildTable rebuilds one table with its new DDL (see ensurePurchaseFKs).
// foreign_keys is a no-op inside a transaction, so it is disabled before
// the transaction and re-enabled right after, even on failure.
//
// FIX-13 (coordinator, R13-M3 extension): the whole OFF → rebuild → ON
// sequence holds ONE dedicated connection, like ResetDatabase — the
// re-enable runs in a defer on that same connection, so every exit path
// restores FK, and FK is VERIFIED actually ON afterwards. (The race window
// the reset had cannot occur here — rebuildTable runs inside migrate()
// at Open, before the server listens — but the ON-exec-failure exit path
// is the same shape, so it gets the same guarantee. The
// foreign_key_check runs on this same connection: the pool has
// MaxOpenConns(1) and this function holds it.)
func (s *Store) rebuildTable(j fkRebuild) error {
	ctx := context.Background()
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return err
	}
	fkRestored := false
	defer func() {
		if !fkRestored {
			if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
				log.Printf("MIGRATE: FAILED to re-enable foreign_keys after rebuild error: %v — FK enforcement may be OFF", err)
			} else if on, verr := fkEnabled(ctx, conn); verr != nil || on != 1 {
				log.Printf("MIGRATE: foreign_keys NOT verified ON after rebuild error (value=%d, err=%v) — FK enforcement may be OFF", on, verr)
			}
		}
		_ = conn.Close()
	}()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(j.newDDL); err != nil {
		return err
	}
	if _, err := tx.Exec(fmt.Sprintf(`INSERT INTO %s_new(%s) SELECT %s FROM %s`,
		j.table, j.columns, j.columns, j.table)); err != nil {
		return err
	}
	if _, err := tx.Exec(fmt.Sprintf(`DROP TABLE %s`, j.table)); err != nil {
		return err
	}
	if _, err := tx.Exec(fmt.Sprintf(`ALTER TABLE %s_new RENAME TO %s`, j.table, j.table)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// Re-enable FK on this same connection and VERIFY it is really ON.
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		log.Printf("MIGRATE: FAILED to re-enable foreign_keys after rebuild: %v — FK enforcement may be OFF", err)
		return err
	}
	if on, err := fkEnabled(ctx, conn); err != nil || on != 1 {
		log.Printf("MIGRATE: foreign_keys NOT verified ON after rebuild (value=%d, err=%v)", on, err)
		return fmt.Errorf("إعادة بناء الجدول %s لم تُعِد تفعيل المفاتيح الخارجية", j.table)
	}
	fkRestored = true
	var violations int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil {
		return err
	}
	if violations > 0 {
		return fmt.Errorf("ترحيل المفاتيح الخارجية ترك %d مخالفة في %s", violations, j.table)
	}
	return nil
}
