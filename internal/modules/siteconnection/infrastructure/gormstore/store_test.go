package gormstore

import (
	"fmt"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	siteconnectioncontract "github.com/dujiao-next/internal/modules/siteconnection/contract"
	siteconnectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func setupSiteConnectionStoreTest(t *testing.T) (*gorm.DB, *Store) {
	t.Helper()
	dsn := fmt.Sprintf("file:siteconnection_store_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&siteconnectiondomain.Connection{}); err != nil {
		t.Fatalf("migrate site connection: %v", err)
	}
	return db, New(db)
}

func TestStoreDeleteHidesConnectionFromEveryReadPath(t *testing.T) {
	db, store := setupSiteConnectionStoreTest(t)
	connection := &siteconnectiondomain.Connection{
		Name:               "primary upstream",
		BaseURL:            "https://upstream.example.com",
		ApiKey:             "upstream-key",
		ApiSecret:          "encrypted-secret",
		Protocol:           constants.ConnectionProtocolDujiaoNext,
		Status:             constants.ConnectionStatusActive,
		RetryMax:           5,
		RetryIntervals:     "[30,60,300]",
		ExchangeRate:       decimal.NewFromInt(1),
		PriceMarkupPercent: decimal.Zero,
		PriceRoundingMode:  "none",
	}
	if err := store.Create(connection); err != nil {
		t.Fatalf("create connection: %v", err)
	}

	if err := store.Delete(connection.ID); err != nil {
		t.Fatalf("delete connection: %v", err)
	}

	if got, err := store.GetByID(connection.ID); err != nil || got != nil {
		t.Fatalf("GetByID after delete = %#v, %v; want nil, nil", got, err)
	}
	if got, err := store.GetByApiKey(connection.ApiKey); err != nil || got != nil {
		t.Fatalf("GetByApiKey after delete = %#v, %v; want nil, nil", got, err)
	}
	listed, total, err := store.List(siteconnectioncontract.ListFilter{})
	if err != nil {
		t.Fatalf("list connections: %v", err)
	}
	if total != 0 || len(listed) != 0 {
		t.Fatalf("list after delete = total %d rows %d; want empty", total, len(listed))
	}
	active, err := store.ListActive()
	if err != nil {
		t.Fatalf("list active connections: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active list after delete has %d rows; want empty", len(active))
	}

	var persisted siteconnectiondomain.Connection
	if err := db.Where("id = ?", connection.ID).First(&persisted).Error; err != nil {
		t.Fatalf("load persisted deleted connection: %v", err)
	}
	if persisted.DeletedAt == nil {
		t.Fatal("delete must persist deleted_at instead of physically removing the row")
	}
}

func TestRemoteReferencesAndPurchaseBarrierSurviveReopen(t *testing.T) {
	db, store := setupSiteConnectionStoreTest(t)
	if err := db.AutoMigrate(&siteconnectiondomain.RemoteReference{}, &siteconnectiondomain.PurchaseAttempt{}); err != nil {
		t.Fatal(err)
	}
	id, err := store.RegisterReference(1, "16688:product", "SG20001")
	if err != nil || id == 0 {
		t.Fatalf("register %d %v", id, err)
	}
	other := New(db)
	again, err := other.RegisterReference(1, "16688:product", "SG20001")
	if err != nil || again != id {
		t.Fatalf("reference changed %d %v", again, err)
	}
	if _, err := other.ResolveReference(2, "16688:product", id); err == nil {
		t.Fatal("cross-connection reference resolved")
	}
	if _, err := other.ResolveReference(1, "16688:order", id); err == nil {
		t.Fatal("cross-namespace reference resolved")
	}
	if err := other.SaveReferenceAmount(1, "16688:product", id, "9.56"); err != nil {
		t.Fatal(err)
	}
	if amount, err := store.ReferenceAmount(1, "16688:product", id); err != nil || amount != "9.56" {
		t.Fatal(amount, err)
	}
	claimed, receipt, err := store.BeginPurchase(1, "trace", "payload")
	if err != nil || !claimed || receipt != "" {
		t.Fatal("first claim failed", err)
	}
	claimed, receipt, err = other.BeginPurchase(1, "trace", "payload")
	if err != nil || claimed || receipt != "" {
		t.Fatal("uncertain purchase was claimed again", err)
	}
	if _, _, err := other.BeginPurchase(1, "trace", "changed"); err == nil {
		t.Fatal("changed purchase replay permitted")
	}
	if err := store.FinishPurchase(1, "trace", `{"ok":true,"order_no":"T1"}`); err != nil {
		t.Fatal(err)
	}
	claimed, receipt, err = other.BeginPurchase(1, "trace", "payload")
	if err != nil || claimed || receipt == "" {
		t.Fatal("saved receipt lost", err)
	}
	if err := store.ReleasePurchase(1, "trace"); err != nil {
		t.Fatal(err)
	}
	claimed, receipt, err = other.BeginPurchase(1, "trace", "payload")
	if err != nil || claimed || receipt == "" {
		t.Fatal("completed receipt was deleted", err)
	}
	if claimed, _, err := other.BeginPurchase(2, "trace", "payload"); err != nil || !claimed {
		t.Fatal("another connection blocked", err)
	}
	if err := other.ReleasePurchase(2, "trace"); err != nil {
		t.Fatal(err)
	}
	if claimed, _, err := other.BeginPurchase(2, "trace", "payload"); err != nil || !claimed {
		t.Fatal("safe rejection could not be retried", err)
	}
}

func TestExternalAppIDCannotShadowNativeCallbackKey(t *testing.T) {
	_, store := setupSiteConnectionStoreTest(t)
	for _, protocol := range []string{constants.ConnectionProtocol16688, constants.ConnectionProtocolYichiyuan, constants.ConnectionProtocolDujiaoNext} {
		if err := store.Create(&siteconnectiondomain.Connection{Name: protocol, BaseURL: "https://example.com", ApiKey: "same-key", ApiSecret: "encrypted", Protocol: protocol}); err != nil {
			t.Fatal(err)
		}
	}
	conn, err := store.GetByApiKey("same-key")
	if err != nil || conn == nil || conn.Protocol != constants.ConnectionProtocolDujiaoNext {
		t.Fatal("external application ID shadowed callback key", err)
	}
}
