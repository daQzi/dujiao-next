package paymentcallback_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	liandongadapter "github.com/dujiao-next/internal/modules/payment/infrastructure/gateway/adapters/liandong"

	"github.com/dujiao-next/internal/constants"
	productdomain "github.com/dujiao-next/internal/modules/catalog/product/domain"
	productgormstore "github.com/dujiao-next/internal/modules/catalog/product/store/gormstore"
	fulfillmentdomain "github.com/dujiao-next/internal/modules/fulfillment/domain"
	userdomain "github.com/dujiao-next/internal/modules/identity/user/domain"
	ordercontract "github.com/dujiao-next/internal/modules/order/contract"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	ordergormstore "github.com/dujiao-next/internal/modules/order/infrastructure/gormstore"
	paymentapp "github.com/dujiao-next/internal/modules/payment/application"
	paymentcontract "github.com/dujiao-next/internal/modules/payment/contract"
	paymentdomain "github.com/dujiao-next/internal/modules/payment/domain"
	paymentprovider "github.com/dujiao-next/internal/modules/payment/infrastructure/gateway/provider"
	paymentgormstore "github.com/dujiao-next/internal/modules/payment/infrastructure/gormstore"
	paymentcallback "github.com/dujiao-next/internal/modules/payment/transport/http/callback"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/shared/money"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

const liandongTestMerchantKey = "0123456789abcdef0123456789abcdef"

type liandongCallbackFixture struct {
	paymentRepo paymentcontract.Store
	orderRepo   ordercontract.Store
	handler     *paymentcallback.Handler
	order       *orderdomain.Order
	payment     *paymentdomain.Payment
}

func liandongSign(key, message string) string {
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

func newLiandongCallbackFixture(t *testing.T) *liandongCallbackFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(
		&userdomain.User{},
		&productdomain.Product{},
		&productdomain.ProductSKU{},
		&orderdomain.Order{},
		&orderdomain.OrderItem{},
		&fulfillmentdomain.Fulfillment{},
		&paymentdomain.PaymentChannel{},
		&paymentdomain.Payment{},
	); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	now := time.Now()
	user := &userdomain.User{
		Email:        "liandong@example.com",
		PasswordHash: "x",
		Status:       constants.UserStatusActive,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user failed: %v", err)
	}
	order := &orderdomain.Order{
		OrderNo:                 "DJLIANDONGCALLBACK001",
		UserID:                  user.ID,
		Status:                  constants.OrderStatusPendingPayment,
		Currency:                "CNY",
		OriginalAmount:          money.FromDecimal(decimal.NewFromInt(10)),
		DiscountAmount:          money.FromDecimal(decimal.Zero),
		PromotionDiscountAmount: money.FromDecimal(decimal.Zero),
		TotalAmount:             money.FromDecimal(decimal.NewFromInt(10)),
		WalletPaidAmount:        money.FromDecimal(decimal.Zero),
		OnlinePaidAmount:        money.FromDecimal(decimal.NewFromInt(10)),
		RefundedAmount:          money.FromDecimal(decimal.Zero),
		CreatedAt:               now,
		UpdatedAt:               now,
	}
	if err := db.Create(order).Error; err != nil {
		t.Fatalf("create order failed: %v", err)
	}
	channel := &paymentdomain.PaymentChannel{
		Name:            "链动支付",
		ProviderType:    constants.PaymentProviderLiandong,
		ChannelType:     constants.PaymentProviderLiandong,
		InteractionMode: constants.PaymentInteractionQR,
		FeeRate:         money.FromDecimal(decimal.Zero),
		ConfigJSON: jsonmap.JSON{
			"gateway_url":        "http://127.0.0.1:8787",
			"client_gateway_url": "https://shop.example.com/faka2api",
			"merchant_key":       liandongTestMerchantKey,
			"notify_url":         "https://shop.example.com/api/v1/payments/callback",
			"goods": []interface{}{
				map[string]interface{}{"amount_fen": 1000, "goods_key": "goods-10"},
			},
		},
		IsActive:  true,
		SortOrder: 10,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(channel).Error; err != nil {
		t.Fatalf("create channel failed: %v", err)
	}
	payment := &paymentdomain.Payment{
		OrderID:         order.ID,
		ChannelID:       channel.ID,
		ProviderType:    channel.ProviderType,
		ChannelType:     channel.ChannelType,
		InteractionMode: channel.InteractionMode,
		Amount:          money.FromDecimal(decimal.NewFromInt(10)),
		FeeRate:         money.FromDecimal(decimal.Zero),
		FeeAmount:       money.FromDecimal(decimal.Zero),
		Currency:        "CNY",
		Status:          constants.PaymentStatusPending,
		ProviderRef:     "DJP123456",
		GatewayOrderNo:  "DJP123456",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(payment).Error; err != nil {
		t.Fatalf("create payment failed: %v", err)
	}

	orderRepo := ordergormstore.New(db, "test-guest-credential-secret-with-32-bytes")
	paymentRepo := paymentgormstore.New(db, "test-guest-credential-secret-with-32-bytes")
	channelRepo := paymentgormstore.NewChannelStore(db)
	productRepo := productgormstore.NewProductStore(db)
	productSKURepo := productgormstore.NewSKUStore(db)

	registry := paymentprovider.NewRegistry()
	registry.Register(constants.PaymentProviderLiandong, "", liandongadapter.NewLiandongAdapter())
	paymentService := paymentapp.NewPaymentService(paymentapp.PaymentServiceOptions{
		OrderStore:              orderRepo,
		ProductRepo:             productRepo,
		ProductSKURepo:          productSKURepo,
		PaymentStore:            paymentRepo,
		ChannelStore:            channelRepo,
		ExpireMinutes:           15,
		PaymentProviderRegistry: registry,
	})

	return &liandongCallbackFixture{
		paymentRepo: paymentRepo,
		orderRepo:   orderRepo,
		handler:     paymentcallback.NewHandler(callbackServiceTestAdapter{payments: paymentService}, paymentRepo, channelRepo, nil),
		order:       order,
		payment:     payment,
	}
}

func performLiandongCallback(t *testing.T, fixture *liandongCallbackFixture, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/payments/callback", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	fixture.handler.PaymentCallback(c)
	return w
}

func signedLiandongBody(verify string) string {
	return `{"event":"payment.succeeded","out_trade_no":"DJP123456","trade_no":"LD2610ABC","amount_fen":1000,` +
		`"paid_at":"2026-10-19T12:00:00Z","verify":"` + verify + `","sign":"` +
		liandongSign(liandongTestMerchantKey, "DJP123456|LD2610ABC|1000") + `"}`
}

func liandongHeaders(body string) map[string]string {
	return map[string]string{
		"X-Faka-Timestamp": strconv.FormatInt(time.Now().Unix(), 10),
		"X-Faka-Signature": "sha256=" + liandongSign(liandongTestMerchantKey, body),
	}
}

func TestLiandongCallbackSettlesPayment(t *testing.T) {
	fixture := newLiandongCallbackFixture(t)
	body := signedLiandongBody("server_verified")
	w := performLiandongCallback(t, fixture, body, liandongHeaders(body))

	if strings.TrimSpace(w.Body.String()) != constants.LiandongCallbackSuccess {
		t.Fatalf("unexpected response body: %s", w.Body.String())
	}
	payment, err := fixture.paymentRepo.GetByID(fixture.payment.ID)
	if err != nil || payment == nil {
		t.Fatalf("reload payment failed: %v", err)
	}
	if payment.Status != constants.PaymentStatusSuccess {
		t.Fatalf("payment status = %s, want success", payment.Status)
	}
	order, err := fixture.orderRepo.GetByID(fixture.order.ID)
	if err != nil || order == nil {
		t.Fatalf("reload order failed: %v", err)
	}
	if order.Status != constants.OrderStatusPaid {
		t.Fatalf("order status = %s, want paid", order.Status)
	}
}

func TestLiandongCallbackRejectsMissingHeaderSignature(t *testing.T) {
	fixture := newLiandongCallbackFixture(t)
	body := signedLiandongBody("server_verified")
	w := performLiandongCallback(t, fixture, body, nil)

	if strings.TrimSpace(w.Body.String()) != constants.LiandongCallbackFail {
		t.Fatalf("unexpected response body: %s", w.Body.String())
	}
	payment, err := fixture.paymentRepo.GetByID(fixture.payment.ID)
	if err != nil || payment == nil {
		t.Fatalf("reload payment failed: %v", err)
	}
	if payment.Status != constants.PaymentStatusPending {
		t.Fatalf("payment must stay pending, got %s", payment.Status)
	}
}

func TestLiandongCallbackDoesNotCreditClientConfirmed(t *testing.T) {
	fixture := newLiandongCallbackFixture(t)
	body := signedLiandongBody("client_confirmed")
	w := performLiandongCallback(t, fixture, body, liandongHeaders(body))

	if strings.TrimSpace(w.Body.String()) != constants.LiandongCallbackSuccess {
		t.Fatalf("unexpected response body: %s", w.Body.String())
	}
	payment, err := fixture.paymentRepo.GetByID(fixture.payment.ID)
	if err != nil || payment == nil {
		t.Fatalf("reload payment failed: %v", err)
	}
	if payment.Status != constants.PaymentStatusPending {
		t.Fatalf("client_confirmed must not settle, got %s", payment.Status)
	}
}
