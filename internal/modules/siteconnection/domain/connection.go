package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// Connection 表示一个上游站点连接。
type Connection struct {
	ID                         uint            `gorm:"primarykey" json:"id"`
	Name                       string          `gorm:"type:varchar(100);not null" json:"name"`
	BaseURL                    string          `gorm:"type:varchar(500);not null" json:"base_url"`
	ApiKey                     string          `gorm:"type:varchar(64);not null" json:"api_key"`
	ApiSecret                  string          `gorm:"type:varchar(512);not null" json:"-"` // AES-256 加密存储
	Protocol                   string          `gorm:"type:varchar(20);not null;default:'dujiao-next'" json:"protocol"`
	YcyDeliveryStatusConfirmed bool            `gorm:"not null;default:false" json:"ycy_delivery_status_confirmed"`
	CallbackURL                string          `gorm:"type:varchar(500)" json:"callback_url"`
	Status                     string          `gorm:"type:varchar(20);not null;default:'pending'" json:"status"`
	LastPingAt                 *time.Time      `json:"last_ping_at,omitempty"`
	LastPingOK                 bool            `gorm:"not null;default:false" json:"last_ping_ok"`
	RetryMax                   int             `gorm:"not null;default:5" json:"retry_max"`
	RetryIntervals             string          `gorm:"type:varchar(200);not null;default:'[30,60,300]'" json:"retry_intervals"`
	ExchangeRate               decimal.Decimal `gorm:"type:decimal(16,6);not null;default:1" json:"exchange_rate"`          // 汇率，上游价格 × 汇率 = 本地价格，默认 1
	PriceMarkupPercent         decimal.Decimal `gorm:"type:decimal(10,4);not null;default:0" json:"price_markup_percent"`   // 加价百分比，如 100 = +100%（翻倍）
	PriceRoundingMode          string          `gorm:"type:varchar(20);not null;default:'none'" json:"price_rounding_mode"` // none / ceil_int / ceil_tenth
	AutoSyncPrice              bool            `gorm:"not null;default:false" json:"auto_sync_price"`                       // 同步时自动更新本地价格
	CreatedAt                  time.Time       `gorm:"index" json:"created_at"`
	UpdatedAt                  time.Time       `gorm:"index" json:"updated_at"`
	DeletedAt                  *time.Time      `gorm:"index" json:"-"`
}

// TableName 指定表名
func (Connection) TableName() string {
	return "site_connections"
}

// RemoteReference preserves opaque upstream identifiers across adapter instances and restarts.
type RemoteReference struct {
	ID           uint   `gorm:"primarykey"`
	ConnectionID uint   `gorm:"uniqueIndex:idx_remote_reference,priority:1;not null"`
	Namespace    string `gorm:"type:varchar(40);uniqueIndex:idx_remote_reference,priority:2;not null"`
	ExternalID   string `gorm:"type:varchar(200);uniqueIndex:idx_remote_reference,priority:3;not null"`
	Amount       string `gorm:"type:varchar(64);not null;default:''"`
}

func (RemoteReference) TableName() string { return "upstream_remote_references" }

// PurchaseAttempt is a durable send barrier for APIs without purchase idempotency.
// Result contains only the normalized order receipt, never delivered cards or credentials.
type PurchaseAttempt struct {
	ID           uint   `gorm:"primarykey"`
	ConnectionID uint   `gorm:"uniqueIndex:idx_purchase_attempt,priority:1;not null"`
	RequestKey   string `gorm:"type:varchar(64);uniqueIndex:idx_purchase_attempt,priority:2;not null"`
	Fingerprint  string `gorm:"type:varchar(64);not null"`
	Result       string `gorm:"type:text;not null"`
	CreatedAt    time.Time
}

func (PurchaseAttempt) TableName() string { return "upstream_purchase_attempts" }
