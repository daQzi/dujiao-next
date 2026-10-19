package liandongadapter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dujiao-next/internal/constants"
	paymentcontract "github.com/dujiao-next/internal/modules/payment/contract"
	"github.com/dujiao-next/internal/modules/payment/infrastructure/gateway/liandong"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
)

type adapter struct{}

func NewLiandongAdapter() paymentcontract.GatewayProvider { return &adapter{} }

var (
	_ paymentcontract.GatewayProvider               = (*adapter)(nil)
	_ paymentcontract.GatewayCallbackVerifier       = (*adapter)(nil)
	_ paymentcontract.GatewayHeaderCallbackVerifier = (*adapter)(nil)
)

func (a *adapter) Type() string { return constants.PaymentProviderLiandong + ":" }

func (a *adapter) parseConfig(raw jsonmap.JSON) (*liandong.Config, error) {
	cfg, err := liandong.ParseConfig(raw)
	if err != nil {
		return nil, mapError(err)
	}
	if err := liandong.ValidateConfig(cfg); err != nil {
		return nil, mapError(err)
	}
	return cfg, nil
}

func (a *adapter) ValidateConfig(raw jsonmap.JSON, channelType string) error {
	channelType = strings.ToLower(strings.TrimSpace(channelType))
	if channelType != "" && channelType != constants.PaymentProviderLiandong {
		return fmt.Errorf("%w: liandong channel_type %s", paymentcontract.ErrGatewayUnsupportedChannel, channelType)
	}
	_, err := a.parseConfig(raw)
	return err
}

func (a *adapter) CreatePayment(ctx context.Context, raw jsonmap.JSON, input paymentcontract.GatewayCreateInput) (*paymentcontract.GatewayCreateResult, error) {
	if channelType := strings.ToLower(strings.TrimSpace(input.ChannelType)); channelType != "" && channelType != constants.PaymentProviderLiandong {
		return nil, fmt.Errorf("%w: liandong channel_type %s", paymentcontract.ErrGatewayUnsupportedChannel, input.ChannelType)
	}
	// faka2api 的 amount_fen 固定为人民币分，不接受其他币种。
	if currency := strings.ToUpper(strings.TrimSpace(input.Currency)); currency != "" && currency != "CNY" {
		return nil, fmt.Errorf("%w: liandong only supports CNY, got %s", paymentcontract.ErrGatewayConfigInvalid, currency)
	}
	mode, _ := input.Extra["interaction_mode"].(string)
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "" && mode != constants.PaymentInteractionQR {
		return nil, fmt.Errorf("%w: liandong only supports qr interaction_mode, got %s", paymentcontract.ErrGatewayConfigInvalid, mode)
	}
	cfg, err := a.parseConfig(raw)
	if err != nil {
		return nil, err
	}
	result, err := liandong.CreatePayment(ctx, cfg, input.OrderNo, input.Subject, input.Amount.Decimal)
	if err != nil {
		return nil, mapError(err)
	}
	payload := jsonmap.JSON{}
	for key, value := range result.Raw {
		payload[key] = value
	}
	if _, ok := payload["client_bootstrap"].(map[string]interface{}); !ok {
		return nil, fmt.Errorf("%w: missing client_bootstrap", paymentcontract.ErrGatewayResponseInvalid)
	}
	return &paymentcontract.GatewayCreateResult{
		ProviderRef:  input.OrderNo,
		Payload:      payload,
		AmountSent:   input.Amount.Decimal.String(),
		CurrencySent: strings.ToUpper(strings.TrimSpace(input.Currency)),
	}, nil
}

// VerifyCallback 兼容不支持读取请求头的旧调用路径：只验 body 内签名。
func (a *adapter) VerifyCallback(raw jsonmap.JSON, _ map[string][]string, body []byte) (*paymentcontract.GatewayCallbackResult, error) {
	cfg, err := liandong.ParseConfig(raw)
	if err != nil {
		return nil, mapError(err)
	}
	callback, err := liandong.ParseCallbackBody(cfg, body)
	if err != nil {
		return nil, mapError(err)
	}
	return buildCallbackResult(callback, callbackStatus(callback))
}

// VerifyCallbackWithHeaders 校验 body 签名 + HTTP 头签名，生产路径必须走这里。
func (a *adapter) VerifyCallbackWithHeaders(raw jsonmap.JSON, _ map[string][]string, headers map[string]string, body []byte) (*paymentcontract.GatewayCallbackResult, error) {
	cfg, err := liandong.ParseConfig(raw)
	if err != nil {
		return nil, mapError(err)
	}
	callback, status, err := liandong.VerifyCallback(cfg, headers, body, time.Now())
	if err != nil {
		return nil, mapError(err)
	}
	return buildCallbackResult(callback, status)
}

func callbackStatus(callback *liandong.Callback) string {
	if callback == nil {
		return ""
	}
	switch callback.Event {
	case constants.LiandongCallbackEventSuccess:
		if callback.Verify == constants.LiandongCallbackVerifyServer || callback.Verify == "admin_verified" {
			return constants.PaymentStatusSuccess
		}
		return ""
	case constants.LiandongCallbackEventExpired:
		return constants.PaymentStatusExpired
	default:
		return ""
	}
}

func buildCallbackResult(callback *liandong.Callback, status string) (*paymentcontract.GatewayCallbackResult, error) {
	if status == "" {
		return &paymentcontract.GatewayCallbackResult{}, nil
	}
	amount := money.FromDecimal(decimal.NewFromInt(callback.AmountFen).Div(decimal.NewFromInt(100)))
	var paidAt *time.Time
	if callback.PaidAt != "" {
		parsed, parseErr := time.Parse(time.RFC3339, callback.PaidAt)
		if parseErr != nil {
			return nil, fmt.Errorf("%w: invalid paid_at", paymentcontract.ErrGatewayResponseInvalid)
		}
		paidAt = &parsed
	}
	return &paymentcontract.GatewayCallbackResult{
		OrderNo:     callback.OutTradeNo,
		ProviderRef: callback.TradeNo,
		Status:      status,
		Amount:      amount,
		Currency:    "CNY",
		PaidAt:      paidAt,
		Payload: jsonmap.JSON{
			"event":        callback.Event,
			"verify":       callback.Verify,
			"amount_fen":   callback.AmountFen,
			"trade_no":     callback.TradeNo,
			"out_trade_no": callback.OutTradeNo,
		},
	}, nil
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, liandong.ErrConfigInvalid):
		return fmt.Errorf("%w: %v", paymentcontract.ErrGatewayConfigInvalid, err)
	case errors.Is(err, liandong.ErrRequestFailed):
		return fmt.Errorf("%w: %v", paymentcontract.ErrGatewayRequestFailed, err)
	case errors.Is(err, liandong.ErrResponseInvalid), errors.Is(err, liandong.ErrSignatureInvalid):
		return fmt.Errorf("%w: %v", paymentcontract.ErrGatewayResponseInvalid, err)
	default:
		return err
	}
}
