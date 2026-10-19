package liandong

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

const testMerchantKey = "0123456789abcdef0123456789abcdef"

func testConfig(gatewayURL string) *Config {
	return &Config{
		GatewayURL:       gatewayURL,
		ClientGatewayURL: gatewayURL,
		MerchantKey:      testMerchantKey,
		NotifyURL:        "https://shop.example.com/api/v1/payments/callback",
		Goods: []GoodsMapping{
			{AmountFen: 1000, GoodsKey: "goods-10"},
			{AmountFen: 5000, GoodsKey: "goods-50"},
		},
	}
}

func sign(key, message string) string {
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestValidateConfigRejectsInvalidTiers(t *testing.T) {
	cfg := testConfig("https://faka.example.com")
	cfg.Goods = append(cfg.Goods, GoodsMapping{AmountFen: 1000, GoodsKey: "dup"})
	if err := ValidateConfig(cfg); err == nil {
		t.Fatal("duplicate amount_fen must be rejected")
	}

	cfg = testConfig("https://faka.example.com")
	cfg.Goods = nil
	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("empty goods must defer tier matching to faka2api: %v", err)
	}
}

func TestGoodsForAmountRequiresExactTier(t *testing.T) {
	cfg := testConfig("https://faka.example.com")
	if _, ok := cfg.GoodsForAmount(decimal.NewFromInt(10)); !ok {
		t.Fatal("10 CNY must match 1000 fen tier")
	}
	if _, ok := cfg.GoodsForAmount(decimal.RequireFromString("9.99")); ok {
		t.Fatal("9.99 CNY must not match any tier")
	}
}

func TestCreatePaymentSendsSignedRequestAndParsesBootstrap(t *testing.T) {
	var gotSignature string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != createOrderPath {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		gotSignature = r.Header.Get("X-Faka-Signature")
		wantSignature := sign(testMerchantKey, r.Header.Get("X-Faka-Timestamp")+"."+string(body))
		if gotSignature != wantSignature {
			t.Errorf("signature mismatch")
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"order": map[string]interface{}{"order_no": "DJP123456"},
			"client_bootstrap": map[string]interface{}{
				"gateway_base":   "https://faka.example.com/",
				"goods_key":      "goods-10",
				"channel_id":     "wx",
				"contact":        "buyer@example.com",
				"query_password": "pwd",
				"attach_token":   "token",
				"order_no":       "DJP123456",
				"amount_fen":     1000,
				"expires_at":     time.Now().Add(10 * time.Minute).Format(time.RFC3339),
			},
		})
	}))
	defer server.Close()

	cfg := testConfig(server.URL)
	result, err := CreatePayment(context.Background(), cfg, "DJP123456", "test order", decimal.NewFromInt(10))
	if err != nil {
		t.Fatalf("create payment failed: %v", err)
	}
	if gotSignature == "" {
		t.Fatal("request must carry X-Faka-Signature")
	}
	if got, _ := result.Raw["client_gateway_url"].(string); got != server.URL {
		t.Fatalf("client_gateway_url = %q, want %q", got, server.URL)
	}
	bootstrap, _ := result.Raw["client_bootstrap"].(map[string]interface{})
	if bootstrap == nil || bootstrap["attach_token"] != "token" {
		t.Fatalf("unexpected bootstrap: %#v", result.Raw)
	}
}

func TestCreatePaymentRejectsUnmatchedAmount(t *testing.T) {
	cfg := testConfig("https://faka.example.com")
	if _, err := CreatePayment(context.Background(), cfg, "DJP123456", "x", decimal.RequireFromString("12.34")); err == nil {
		t.Fatal("unmatched amount must be rejected")
	}
}

func TestVerifyCallbackRequiresHeaderSignature(t *testing.T) {
	cfg := testConfig("https://faka.example.com")
	body := []byte(`{"event":"payment.succeeded","out_trade_no":"DJP123456","trade_no":"LD2610","amount_fen":1000,"paid_at":"2026-10-19T12:00:00Z","verify":"server_verified","sign":"` +
		sign(testMerchantKey, "DJP123456|LD2610|1000") + `"}`)
	now := time.Now()
	headers := map[string]string{
		"X-Faka-Timestamp": strconv.FormatInt(now.Unix(), 10),
		"X-Faka-Signature": "sha256=" + sign(testMerchantKey, string(body)),
	}
	callback, status, err := VerifyCallback(cfg, headers, body, now)
	if err != nil {
		t.Fatalf("verify callback failed: %v", err)
	}
	if status != "success" || callback.TradeNo != "LD2610" {
		t.Fatalf("unexpected callback result: status=%q callback=%+v", status, callback)
	}

	headers["X-Faka-Signature"] = "sha256=" + strings.Repeat("0", 64)
	if _, _, err := VerifyCallback(cfg, headers, body, now); err == nil {
		t.Fatal("bad header signature must be rejected")
	}
}

func TestVerifyCallbackIgnoresUntrustedVerifyState(t *testing.T) {
	cfg := testConfig("https://faka.example.com")
	body := []byte(`{"event":"payment.succeeded","out_trade_no":"DJP123456","trade_no":"LD2610","amount_fen":1000,"verify":"client_confirmed","sign":"` +
		sign(testMerchantKey, "DJP123456|LD2610|1000") + `"}`)
	now := time.Now()
	headers := map[string]string{
		"X-Faka-Timestamp": strconv.FormatInt(now.Unix(), 10),
		"X-Faka-Signature": "sha256=" + sign(testMerchantKey, string(body)),
	}
	_, status, err := VerifyCallback(cfg, headers, body, now)
	if err != nil {
		t.Fatalf("verify callback failed: %v", err)
	}
	if status != "" {
		t.Fatalf("client_confirmed must not settle payment, got status %q", status)
	}
}
