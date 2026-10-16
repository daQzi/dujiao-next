package contract

import siteconnectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"

// ListFilter 是连接列表查询条件。
type ListFilter struct {
	Status   string
	Page     int
	PageSize int
}

// Repository 是连接持久化端口。
type Repository interface {
	GetByID(id uint) (*siteconnectiondomain.Connection, error)
	GetByApiKey(apiKey string) (*siteconnectiondomain.Connection, error)
	Create(conn *siteconnectiondomain.Connection) error
	Update(conn *siteconnectiondomain.Connection) error
	Delete(id uint) error
	List(filter ListFilter) ([]siteconnectiondomain.Connection, int64, error)
	ListActive() ([]siteconnectiondomain.Connection, error)
}

// RemoteRepository is scoped by connection and protocol, not by credentials or process memory.
type RemoteRepository interface {
	RegisterReference(connectionID uint, namespace, externalID string) (uint, error)
	ResolveReference(connectionID uint, namespace string, id uint) (string, error)
	SaveReferenceAmount(connectionID uint, namespace string, id uint, amount string) error
	ReferenceAmount(connectionID uint, namespace string, id uint) (string, error)
	BeginPurchase(connectionID uint, key, fingerprint string) (claimed bool, result string, err error)
	FinishPurchase(connectionID uint, key, result string) error
	ReleasePurchase(connectionID uint, key string) error
}
