package upstream

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dujiao-next/internal/constants"
	siteconnectioncontract "github.com/dujiao-next/internal/modules/siteconnection/contract"
	siteconnectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/shopspring/decimal"
)

type externalAdapter struct {
	*DujiaoNextAdapter         // Reuse the existing restricted image downloader only.
	connectionID               uint
	protocol                   string
	ycyDeliveryStatusConfirmed bool
	store                      siteconnectioncontract.RemoteRepository
}

var _ Adapter = (*externalAdapter)(nil)

func newExternalAdapter(conn *siteconnectiondomain.Connection, uploadsDir string, store siteconnectioncontract.RemoteRepository) *externalAdapter {
	a := &externalAdapter{DujiaoNextAdapter: NewDujiaoNextAdapter(conn, uploadsDir), connectionID: conn.ID, protocol: conn.Protocol, store: store, ycyDeliveryStatusConfirmed: conn.YcyDeliveryStatusConfirmed}
	// Never forward signed credentials or replay a purchase through redirects.
	a.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return a
}

// wireScalar accepts the number-or-string fields used by both upstream protocols.
type wireScalar string

func (s *wireScalar) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		*s = ""
		return nil
	}
	var value string
	if len(data) > 0 && data[0] == '"' {
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
	} else {
		var n json.Number
		if err := json.Unmarshal(data, &n); err != nil {
			return err
		}
		value = n.String()
	}
	*s = wireScalar(value)
	return nil
}

type externalAPIError struct{ message string }

func (e *externalAPIError) Error() string { return "upstream rejected request: " + e.message }

func (a *externalAdapter) namespace(kind string) string { return a.protocol + ":" + kind }
func (a *externalAdapter) reference(kind, externalID string) (uint, error) {
	return a.store.RegisterReference(a.connectionID, a.namespace(kind), externalID)
}
func (a *externalAdapter) resolve(kind string, id uint) (string, error) {
	return a.store.ResolveReference(a.connectionID, a.namespace(kind), id)
}

// The Yichiyuan protocol signs PHP's urldecode(http_build_query(...)). Only
// scalar fields are submitted here; complex SKU/widget products are fail-closed.
func signYichiyuan(params map[string]string, secret string) string {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if k != "sign" && v != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+params[k])
	}
	// PHP appends the raw key BEFORE urldecode, so a '+' or percent escape
	// inside the key is decoded too. Business values have already round-tripped
	// through http_build_query and therefore stay unchanged here.
	decodedSecret, err := url.QueryUnescape(secret)
	if err != nil {
		decodedSecret = secret
	}
	sum := md5.Sum([]byte(strings.Join(parts, "&") + "&key=" + decodedSecret))
	return hex.EncodeToString(sum[:])
}

func sign16688(endpoint string, params map[string]string, secret string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		if k != "sign" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+params[k])
	}
	segments := strings.Split(strings.Trim(endpoint, "/"), "/")
	plain := "POST\n" + strings.ToLower(segments[len(segments)-2]) + "\n" + strings.ToLower(segments[len(segments)-1]) + "\n" + strings.Join(parts, "&")
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(plain))
	return hex.EncodeToString(mac.Sum(nil))
}

func (a *externalAdapter) call(ctx context.Context, endpoint string, business map[string]string, out interface{}) error {
	params := make(map[string]string, len(business)+4)
	for k, v := range business {
		params[k] = v
	}
	params["app_id"] = a.apiKey
	success := "200"
	if a.protocol == constants.ConnectionProtocol16688 {
		nonce := make([]byte, 16)
		if _, err := rand.Read(nonce); err != nil {
			return fmt.Errorf("generate nonce: %w", err)
		}
		params["timestamp"] = strconv.FormatInt(time.Now().Unix(), 10)
		params["nonce"] = hex.EncodeToString(nonce)
		params["sign"] = sign16688(endpoint, params, a.apiSecret)
		success = "1"
	} else {
		params["sign"] = signYichiyuan(params, a.apiSecret)
	}
	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("build upstream request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("upstream transport failed")
	} // Do not expose signed request URLs.
	defer resp.Body.Close()
	const limit = 16 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(body) > limit {
		return fmt.Errorf("invalid or oversized upstream response")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("upstream HTTP status %d", resp.StatusCode)
	}
	var envelope struct {
		Code wireScalar      `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("invalid upstream response envelope")
	}
	if string(envelope.Code) != success {
		if envelope.Code == "" {
			return fmt.Errorf("upstream response missing code")
		}
		// Both documents use zero for failure, but do not guarantee accounting
		// rollback. Purchase callers keep their durable barrier on this error.
		if envelope.Code != "0" {
			return fmt.Errorf("unexpected upstream response code")
		}
		message := envelope.Msg
		for _, sensitive := range []string{a.apiSecret, params["sign"]} {
			if sensitive != "" {
				message = strings.ReplaceAll(message, sensitive, "[redacted]")
			}
		}
		return &externalAPIError{message: message}
	}
	if out == nil {
		return nil
	}
	if len(envelope.Data) == 0 || bytes.Equal(envelope.Data, []byte("null")) {
		return fmt.Errorf("upstream response missing data")
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("invalid upstream response data")
	}
	return nil
}

func locale(value string) jsonmap.JSON {
	return jsonmap.JSON{"zh-CN": value, "zh-TW": value, "en-US": value}
}

func positiveMoney(raw string) (string, error) {
	v, err := decimal.NewFromString(raw)
	if err != nil || !v.IsPositive() {
		return "", fmt.Errorf("upstream price must be positive")
	}
	return v.String(), nil
}

func stockQuantity(raw wireScalar) (int, error) {
	v, err := strconv.Atoi(string(raw))
	if err != nil || v < -1 {
		return 0, fmt.Errorf("invalid upstream stock")
	}
	return v, nil
}

func stockStatus(stock int) string {
	if stock == 0 {
		return "out_of_stock"
	}
	return "in_stock"
}

func (a *externalAdapter) Ping(ctx context.Context) (*PingResult, error) {
	result := &PingResult{SiteName: a.baseURL, ProtocolVersion: a.protocol, Currency: "CNY"}
	if a.protocol == constants.ConnectionProtocol16688 {
		var balance struct {
			OperateMoney wireScalar `json:"operate_money"`
		}
		if err := a.call(ctx, "/openApi/merchant/balance", nil, &balance); err != nil {
			return nil, err
		}
		v, err := decimal.NewFromString(string(balance.OperateMoney))
		if err != nil || v.IsNegative() {
			return nil, fmt.Errorf("invalid upstream balance")
		}
		result.Balance = v.String()
	} else if err := a.call(ctx, "/shared/authentication/connect", nil, nil); err != nil {
		return nil, err
	}
	return result, nil
}

func (a *externalAdapter) CancelOrder(context.Context, uint) error {
	return fmt.Errorf("%w: this upstream has no purchase cancellation API; handle it on the upstream site", ErrUpstreamCancelNotAllowed)
}

func externalFailure(err error) *CreateUpstreamOrderResp {
	var apiErr *externalAPIError
	if errors.As(err, &apiErr) {
		return &CreateUpstreamOrderResp{ErrorCode: "upstream_rejected", ErrorMessage: apiErr.Error()}
	}
	return &CreateUpstreamOrderResp{ErrorCode: "purchase_uncertain", ErrorMessage: "Upstream purchase outcome is uncertain. Automatic and manual resubmission are blocked; reconcile on the upstream site before further action."}
}
