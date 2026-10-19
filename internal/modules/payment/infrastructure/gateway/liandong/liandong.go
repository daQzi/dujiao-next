package liandong

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/modules/payment/infrastructure/gateway/common"
	"github.com/shopspring/decimal"
)

var (
	ErrConfigInvalid    = errors.New("liandong config invalid")
	ErrRequestFailed    = errors.New("liandong request failed")
	ErrResponseInvalid  = errors.New("liandong response invalid")
	ErrSignatureInvalid = errors.New("liandong signature invalid")
)

const (
	createOrderPath         = "/api/order/create"
	callbackMaxAge          = 5 * time.Minute
	maxResponseBytes        = 1 << 20
	trustedServerVerified   = "server_verified"
	trustedAdminVerified    = "admin_verified"
	untrustedClientVerified = "client_confirmed"
)

type GoodsMapping struct {
	AmountFen int64  `json:"amount_fen"`
	GoodsKey  string `json:"goods_key"`
}

type Config struct {
	GatewayURL       string         `json:"gateway_url"`
	ClientGatewayURL string         `json:"client_gateway_url"`
	MerchantKey      string         `json:"merchant_key"`
	NotifyURL        string         `json:"notify_url"`
	ReturnURL        string         `json:"return_url"`
	Goods            []GoodsMapping `json:"goods"`
}

type ClientBootstrap struct {
	GatewayBase    string `json:"gateway_base"`
	GoodsKey       string `json:"goods_key"`
	ChannelID      string `json:"channel_id"`
	Contact        string `json:"contact"`
	QueryPassword  string `json:"query_password"`
	AttachToken    string `json:"attach_token"`
	OrderNo        string `json:"order_no"`
	AmountFen      int64  `json:"amount_fen"`
	ExpiresAt      string `json:"expires_at"`
	TradeNo        string `json:"trade_no,omitempty"`
	Status         string `json:"status,omitempty"`
	VerifyState    string `json:"verify_state,omitempty"`
	CheckoutAction string `json:"checkout_action,omitempty"`
}

type CreateResult struct {
	Bootstrap ClientBootstrap
	Raw       map[string]interface{}
}

type Callback struct {
	Event      string `json:"event"`
	OutTradeNo string `json:"out_trade_no"`
	TradeNo    string `json:"trade_no"`
	AmountFen  int64  `json:"amount_fen"`
	PaidAt     string `json:"paid_at"`
	Verify     string `json:"verify"`
	Sign       string `json:"sign"`
}

func ParseConfig(raw map[string]interface{}) (*Config, error) {
	return common.ParseConfig[Config](raw, ErrConfigInvalid)
}

func (c *Config) Normalize() {
	if c == nil {
		return
	}
	c.GatewayURL = strings.TrimRight(strings.TrimSpace(c.GatewayURL), "/")
	c.ClientGatewayURL = strings.TrimRight(strings.TrimSpace(c.ClientGatewayURL), "/")
	if c.ClientGatewayURL == "" {
		c.ClientGatewayURL = c.GatewayURL
	}
	c.MerchantKey = strings.TrimSpace(c.MerchantKey)
	c.NotifyURL = strings.TrimSpace(c.NotifyURL)
	c.ReturnURL = strings.TrimSpace(c.ReturnURL)
	for i := range c.Goods {
		c.Goods[i].GoodsKey = strings.TrimSpace(c.Goods[i].GoodsKey)
	}
}

func ValidateConfig(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("%w: config is nil", ErrConfigInvalid)
	}
	cfg.Normalize()
	if err := validateHTTPURL(cfg.GatewayURL, true); err != nil {
		return fmt.Errorf("%w: gateway_url is invalid", ErrConfigInvalid)
	}
	if err := validateHTTPURL(cfg.ClientGatewayURL, true); err != nil {
		return fmt.Errorf("%w: client_gateway_url is invalid", ErrConfigInvalid)
	}
	if len(cfg.MerchantKey) < 16 {
		return fmt.Errorf("%w: merchant_key must be at least 16 characters", ErrConfigInvalid)
	}
	if err := validateHTTPURL(cfg.NotifyURL, false); err != nil {
		return fmt.Errorf("%w: notify_url is invalid", ErrConfigInvalid)
	}
	if cfg.ReturnURL != "" {
		if err := validateHTTPURL(cfg.ReturnURL, false); err != nil {
			return fmt.Errorf("%w: return_url is invalid", ErrConfigInvalid)
		}
	}
	if len(cfg.Goods) == 0 || len(cfg.Goods) > 12 {
		return fmt.Errorf("%w: goods must have 1-12 amount mappings", ErrConfigInvalid)
	}
	seen := make(map[int64]struct{}, len(cfg.Goods))
	for _, item := range cfg.Goods {
		if item.AmountFen <= 0 || item.GoodsKey == "" {
			return fmt.Errorf("%w: goods contains an empty key or invalid amount_fen", ErrConfigInvalid)
		}
		if _, exists := seen[item.AmountFen]; exists {
			return fmt.Errorf("%w: goods amount_fen must be unique", ErrConfigInvalid)
		}
		seen[item.AmountFen] = struct{}{}
	}
	return nil
}

func validateHTTPURL(raw string, allowLocalHTTP bool) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return errors.New("invalid URL")
	}
	if u.Scheme != "https" && !(allowLocalHTTP && u.Scheme == "http") {
		return errors.New("URL must use https")
	}
	return nil
}

func (c *Config) GoodsForAmount(amount decimal.Decimal) (GoodsMapping, bool) {
	if c == nil || amount.LessThanOrEqual(decimal.Zero) || !amount.Equal(amount.Round(2)) {
		return GoodsMapping{}, false
	}
	f := amount.Mul(decimal.NewFromInt(100))
	if !f.Equal(f.Truncate(0)) {
		return GoodsMapping{}, false
	}
	fen := f.IntPart()
	for _, mapping := range c.Goods {
		if mapping.AmountFen == fen && strings.TrimSpace(mapping.GoodsKey) != "" {
			return mapping, true
		}
	}
	return GoodsMapping{}, false
}

func CreatePayment(ctx context.Context, cfg *Config, orderNo, subject string, amount decimal.Decimal) (*CreateResult, error) {
	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}
	orderNo = strings.TrimSpace(orderNo)
	if len(orderNo) < 6 || len(orderNo) > 64 {
		return nil, fmt.Errorf("%w: out_trade_no length must be 6-64", ErrConfigInvalid)
	}
	if _, ok := cfg.GoodsForAmount(amount); !ok {
		return nil, fmt.Errorf("%w: payment amount does not match a configured goods tier", ErrConfigInvalid)
	}
	amountFen := amount.Mul(decimal.NewFromInt(100)).IntPart()
	body, err := json.Marshal(map[string]interface{}{
		"out_trade_no": orderNo,
		"amount_fen":   amountFen,
		"subject":      strings.TrimSpace(subject),
		"notify_url":   cfg.NotifyURL,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: marshal request", ErrConfigInvalid)
	}
	ctx, cancel := common.WithDefaultTimeout(ctx)
	defer cancel()
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.GatewayURL+createOrderPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: build request", ErrRequestFailed)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Faka-Timestamp", timestamp)
	req.Header.Set("X-Faka-Signature", signHex(cfg.MerchantKey, timestamp+"."+string(body)))
	resp, err := (&http.Client{Timeout: common.DefaultTimeout}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRequestFailed, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: read response", ErrRequestFailed)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%w: status=%d", ErrRequestFailed, resp.StatusCode)
	}
	var response struct {
		Order           map[string]interface{} `json:"order"`
		ClientBootstrap ClientBootstrap        `json:"client_bootstrap"`
	}
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("%w: decode response", ErrResponseInvalid)
	}
	b := response.ClientBootstrap
	if b.OrderNo != orderNo || b.AmountFen != amountFen || b.GatewayBase == "" || b.GoodsKey == "" ||
		b.ChannelID == "" || b.Contact == "" || b.QueryPassword == "" || b.AttachToken == "" {
		return nil, fmt.Errorf("%w: incomplete or mismatched client_bootstrap", ErrResponseInvalid)
	}
	if err := validateHTTPURL(strings.TrimRight(b.GatewayBase, "/"), true); err != nil {
		return nil, fmt.Errorf("%w: invalid client gateway", ErrResponseInvalid)
	}
	if _, err := time.Parse(time.RFC3339, b.ExpiresAt); err != nil {
		return nil, fmt.Errorf("%w: invalid expires_at", ErrResponseInvalid)
	}
	if b.TradeNo != "" && b.CheckoutAction != "qrcode" {
		return nil, fmt.Errorf("%w: trade_no without qrcode checkout", ErrResponseInvalid)
	}
	return &CreateResult{Bootstrap: b, Raw: map[string]interface{}{
		"client_bootstrap":   bootstrapPayload(b),
		"client_gateway_url": cfg.ClientGatewayURL,
	}}, nil
}

func bootstrapPayload(b ClientBootstrap) map[string]interface{} {
	data, _ := json.Marshal(b)
	var payload map[string]interface{}
	_ = json.Unmarshal(data, &payload)
	return payload
}

// ParseCallbackBody 只校验回调 body 内的业务签名，不依赖 HTTP 头。
// 供无法读取请求头的旧调用路径使用；新代码应优先使用 VerifyCallback。
func ParseCallbackBody(cfg *Config, body []byte) (*Callback, error) {
	if cfg == nil || len(body) == 0 {
		return nil, fmt.Errorf("%w: empty callback", ErrResponseInvalid)
	}
	var callback Callback
	if err := json.Unmarshal(body, &callback); err != nil {
		return nil, fmt.Errorf("%w: decode callback", ErrResponseInvalid)
	}
	callback.OutTradeNo = strings.TrimSpace(callback.OutTradeNo)
	callback.TradeNo = strings.TrimSpace(callback.TradeNo)
	callback.Event = strings.TrimSpace(callback.Event)
	callback.Verify = strings.TrimSpace(callback.Verify)
	if callback.OutTradeNo == "" || callback.AmountFen <= 0 || callback.Sign == "" {
		return nil, fmt.Errorf("%w: missing callback fields", ErrResponseInvalid)
	}
	expected := signHex(cfg.MerchantKey, callback.OutTradeNo+"|"+callback.TradeNo+"|"+strconv.FormatInt(callback.AmountFen, 10))
	if !hmac.Equal([]byte(strings.ToLower(strings.TrimSpace(callback.Sign))), []byte(expected)) {
		return nil, ErrSignatureInvalid
	}
	return &callback, nil
}

// VerifyCallback 校验 body 业务签名和 HTTP 头签名，并返回支付状态。
func VerifyCallback(cfg *Config, headers map[string]string, body []byte, now time.Time) (*Callback, string, error) {
	callback, err := ParseCallbackBody(cfg, body)
	if err != nil {
		return nil, "", err
	}
	timestamp := headerValue(headers, "X-Faka-Timestamp")
	signature := headerValue(headers, "X-Faka-Signature")
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || signature == "" || now.IsZero() || absDuration(now.Sub(time.Unix(ts, 0))) > callbackMaxAge {
		return nil, "", ErrSignatureInvalid
	}
	if !hmac.Equal([]byte(signature), []byte("sha256="+signHex(cfg.MerchantKey, string(body)))) {
		return nil, "", ErrSignatureInvalid
	}
	switch callback.Event {
	case constants.LiandongCallbackEventSuccess:
		if callback.TradeNo == "" {
			return nil, "", fmt.Errorf("%w: missing trade_no", ErrResponseInvalid)
		}
		if callback.Verify != trustedServerVerified && callback.Verify != trustedAdminVerified {
			// The gateway may report a client-side confirmation; it is not evidence of payment.
			if callback.Verify == untrustedClientVerified || callback.Verify == "review" {
				return callback, "", nil
			}
			return nil, "", ErrSignatureInvalid
		}
		return callback, constants.PaymentStatusSuccess, nil
	case constants.LiandongCallbackEventExpired:
		return callback, constants.PaymentStatusExpired, nil
	case "payment.pending_review":
		return callback, "", nil
	default:
		return nil, "", fmt.Errorf("%w: unsupported callback event", ErrResponseInvalid)
	}
}

func signHex(key, message string) string {
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

func headerValue(headers map[string]string, key string) string {
	for k, v := range headers {
		if strings.EqualFold(strings.TrimSpace(k), key) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
