package gormstore

import (
	"errors"
	"fmt"
	"time"

	siteconnectioncontract "github.com/dujiao-next/internal/modules/siteconnection/contract"
	siteconnectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Store 是 SiteConnection 的 GORM 持久化实现。
type Store struct {
	db *gorm.DB
}

var _ siteconnectioncontract.Repository = (*Store)(nil)

// New 创建连接仓库。
func New(db *gorm.DB) *Store {
	return &Store{db: db}
}

// GetByID 根据 ID 获取
func (r *Store) GetByID(id uint) (*siteconnectiondomain.Connection, error) {
	var conn siteconnectiondomain.Connection
	if err := r.db.Where("deleted_at IS NULL").First(&conn, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &conn, nil
}

// GetByApiKey 根据 ApiKey 获取连接
func (r *Store) GetByApiKey(apiKey string) (*siteconnectiondomain.Connection, error) {
	var conn siteconnectiondomain.Connection
	// Only native Dujiao connections participate in its signed callback API.
	// Third-party app IDs are not globally unique and must not shadow a key.
	if err := r.db.Where("deleted_at IS NULL AND api_key = ? AND protocol = ?", apiKey, "dujiao-next").First(&conn).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &conn, nil
}

// Create 创建连接
func (r *Store) Create(conn *siteconnectiondomain.Connection) error {
	return r.db.Create(conn).Error
}

// Update 更新连接
func (r *Store) Update(conn *siteconnectiondomain.Connection) error {
	return r.db.Save(conn).Error
}

// Delete 软删除连接
func (r *Store) Delete(id uint) error {
	return r.db.Model(&siteconnectiondomain.Connection{}).
		Where("id = ? AND deleted_at IS NULL", id).
		Update("deleted_at", time.Now()).Error
}

// List 列表查询
func (r *Store) List(filter siteconnectioncontract.ListFilter) ([]siteconnectiondomain.Connection, int64, error) {
	var conns []siteconnectiondomain.Connection
	var total int64

	q := r.db.Model(&siteconnectiondomain.Connection{}).Where("deleted_at IS NULL")
	if filter.Status != "" {
		q = q.Where("status = ?", filter.Status)
	}

	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	q = q.Order("created_at DESC")
	if filter.Page > 0 && filter.PageSize > 0 {
		q = q.Offset((filter.Page - 1) * filter.PageSize).Limit(filter.PageSize)
	}

	if err := q.Find(&conns).Error; err != nil {
		return nil, 0, err
	}

	return conns, total, nil
}

// ListActive 获取所有启用的连接
func (r *Store) ListActive() ([]siteconnectiondomain.Connection, error) {
	var conns []siteconnectiondomain.Connection
	if err := r.db.Where("deleted_at IS NULL AND status = ?", "active").Find(&conns).Error; err != nil {
		return nil, err
	}
	return conns, nil
}

var _ siteconnectioncontract.RemoteRepository = (*Store)(nil)

func (r *Store) RegisterReference(connectionID uint, namespace, externalID string) (uint, error) {
	if connectionID == 0 || externalID == "" || len(externalID) > 200 {
		return 0, fmt.Errorf("invalid upstream reference")
	}
	ref := siteconnectiondomain.RemoteReference{ConnectionID: connectionID, Namespace: namespace, ExternalID: externalID}
	if err := r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&ref).Error; err != nil {
		return 0, err
	}
	if err := r.db.Where("connection_id = ? AND namespace = ? AND external_id = ?", connectionID, namespace, externalID).First(&ref).Error; err != nil {
		return 0, err
	}
	return ref.ID, nil
}

func (r *Store) ResolveReference(connectionID uint, namespace string, id uint) (string, error) {
	var ref siteconnectiondomain.RemoteReference
	if err := r.db.Where("id = ? AND connection_id = ? AND namespace = ?", id, connectionID, namespace).First(&ref).Error; err != nil {
		return "", err
	}
	return ref.ExternalID, nil
}

func (r *Store) BeginPurchase(connectionID uint, key, fingerprint string) (bool, string, error) {
	attempt := siteconnectiondomain.PurchaseAttempt{ConnectionID: connectionID, RequestKey: key, Fingerprint: fingerprint}
	result := r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&attempt)
	if result.Error != nil {
		return false, "", result.Error
	}
	if result.RowsAffected == 1 {
		return true, "", nil
	}
	if err := r.db.Where("connection_id = ? AND request_key = ?", connectionID, key).First(&attempt).Error; err != nil {
		return false, "", err
	}
	if attempt.Fingerprint != fingerprint {
		return false, "", fmt.Errorf("purchase request changed; reconcile the original purchase before retrying")
	}
	return false, attempt.Result, nil
}

func (r *Store) SaveReferenceAmount(connectionID uint, namespace string, id uint, amount string) error {
	result := r.db.Model(&siteconnectiondomain.RemoteReference{}).Where("id = ? AND connection_id = ? AND namespace = ?", id, connectionID, namespace).Update("amount", amount)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("order receipt reference not found")
	}
	return nil
}

func (r *Store) ReferenceAmount(connectionID uint, namespace string, id uint) (string, error) {
	var ref siteconnectiondomain.RemoteReference
	if err := r.db.Where("id = ? AND connection_id = ? AND namespace = ?", id, connectionID, namespace).First(&ref).Error; err != nil {
		return "", err
	}
	if ref.Amount == "" {
		return "", fmt.Errorf("order amount is unavailable")
	}
	return ref.Amount, nil
}

func (r *Store) FinishPurchase(connectionID uint, key, result string) error {
	updated := r.db.Model(&siteconnectiondomain.PurchaseAttempt{}).
		Where("connection_id = ? AND request_key = ? AND result = ?", connectionID, key, "").Update("result", result)
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return fmt.Errorf("purchase receipt was not saved")
	}
	return nil
}

// Only the claiming caller may release a barrier, before sending a purchase.
func (r *Store) ReleasePurchase(connectionID uint, key string) error {
	return r.db.Where("connection_id = ? AND request_key = ? AND result = ?", connectionID, key, "").Delete(&siteconnectiondomain.PurchaseAttempt{}).Error
}
