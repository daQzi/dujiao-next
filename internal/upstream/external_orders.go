package upstream

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/shared/jsonmap"
)

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (a *externalAdapter) CreateOrder(ctx context.Context, req CreateUpstreamOrderReq) (*CreateUpstreamOrderResp, error) {
	if a.protocol == constants.ConnectionProtocolYichiyuan && !a.ycyDeliveryStatusConfirmed {
		return &CreateUpstreamOrderResp{ErrorCode: "invalid_request", ErrorMessage: "Yichiyuan procurement is disabled until the upstream query delivery_status extension is confirmed."}, nil
	}
	if req.Quantity < 1 || req.SKUID == 0 || req.TraceID == "" || req.DownstreamOrderNo == "" || len(req.ManualFormData) != 0 {
		return &CreateUpstreamOrderResp{ErrorCode: "invalid_request", ErrorMessage: "A stable procurement trace, order number, SKU and quantity are required; custom forms are not supported."}, nil
	}
	code, err := a.resolve("product", req.SKUID)
	if err != nil {
		return nil, err
	}
	key := digest(a.protocol + ":" + req.TraceID)
	fingerprint := digest(fmt.Sprintf("%s:%d:%s", code, req.Quantity, req.DownstreamOrderNo))
	claimed, receipt, err := a.store.BeginPurchase(a.connectionID, key, fingerprint)
	if err != nil {
		return &CreateUpstreamOrderResp{ErrorCode: "purchase_uncertain", ErrorMessage: "Cannot claim purchase safely; reconcile existing purchase before retrying."}, nil
	}
	if !claimed {
		if receipt == "" {
			return externalFailure(fmt.Errorf("purchase already sent")), nil
		}
		var result CreateUpstreamOrderResp
		if err := json.Unmarshal([]byte(receipt), &result); err != nil || !result.OK || result.OrderID == 0 {
			return externalFailure(fmt.Errorf("invalid stored receipt")), nil
		}
		return &result, nil
	}
	// No transaction is held during HTTP. The committed unique row is a durable
	// at-most-once send barrier, including process crashes and manual retries.
	if err := a.preflight(ctx, code, req.Quantity); err != nil {
		if releaseErr := a.store.ReleasePurchase(a.connectionID, key); releaseErr != nil {
			return externalFailure(releaseErr), nil
		}
		return &CreateUpstreamOrderResp{ErrorCode: "invalid_request", ErrorMessage: err.Error()}, nil
	}
	endpoint := "/shared/commodity/trade"
	params := map[string]string{"shared_code": code, "num": strconv.Itoa(req.Quantity), "request_no": key, "contact": "API-" + key[:16]}
	var response struct {
		TradeNo     string     `json:"trade_no"`
		YcyTradeNo  string     `json:"tradeNo"`
		Amount      wireScalar `json:"amount"`
		TotalAmount wireScalar `json:"total_amount"`
	}
	if a.protocol == constants.ConnectionProtocol16688 {
		endpoint = "/openApi/purchase/create"
		params = map[string]string{"goods_no": code, "quantity": strconv.Itoa(req.Quantity), "remark": "dujiao:" + key}
	}
	if err := a.call(ctx, endpoint, params, &response); err != nil {
		// A business error does not document whether upstream accounting was
		// rolled back. Keep the send barrier even for code=0.
		return externalFailure(err), nil
	}
	orderNo, amount := response.TradeNo, response.TotalAmount
	if a.protocol == constants.ConnectionProtocolYichiyuan {
		orderNo, amount = response.YcyTradeNo, response.Amount
	}
	if orderNo == "" {
		return externalFailure(fmt.Errorf("missing upstream order number")), nil
	}
	paid, err := positiveMoney(string(amount))
	if err != nil {
		return externalFailure(err), nil
	}
	id, err := a.reference("order", orderNo)
	if err != nil {
		return externalFailure(err), nil
	}
	if err := a.store.SaveReferenceAmount(a.connectionID, a.namespace("order"), id, paid); err != nil {
		return externalFailure(err), nil
	}
	result := &CreateUpstreamOrderResp{OK: true, OrderID: id, OrderNo: orderNo, Status: "accepted", Amount: paid, Currency: "CNY"}
	encoded, err := json.Marshal(result)
	if err != nil {
		return externalFailure(err), nil
	}
	if err := a.store.FinishPurchase(a.connectionID, key, string(encoded)); err != nil {
		return externalFailure(err), nil
	}
	return result, nil
}

func (a *externalAdapter) preflight(ctx context.Context, code string, quantity int) error {
	if a.protocol == constants.ConnectionProtocolYichiyuan {
		item, err := a.ycyDetail(ctx, code)
		if err != nil {
			return err
		}
		if err := validateYcy(*item, quantity); err != nil {
			return err
		}
		var quote struct {
			Price        wireScalar `json:"price"`
			CurrencyCode string     `json:"currency_code"`
		}
		if err := a.call(ctx, "/shared/commodity/valuation", map[string]string{"code": code, "num": strconv.Itoa(quantity)}, &quote); err != nil {
			return err
		}
		if _, err := positiveMoney(string(quote.Price)); err != nil {
			return err
		}
		if quote.CurrencyCode != "" && quote.CurrencyCode != "CNY" {
			return fmt.Errorf("this Yichiyuan adapter requires a CNY upstream")
		}
		return a.call(ctx, "/shared/commodity/inventoryState", map[string]string{"shared_code": code, "num": strconv.Itoa(quantity)}, nil)
	}
	item, err := a.detail16688(ctx, code)
	if err != nil {
		return err
	}
	if item.DeliveryMethod == "2" && quantity != 1 {
		return fmt.Errorf("16688 content goods must be purchased one at a time")
	}
	if item.DeliveryMethod != "1" && item.DeliveryMethod != "2" && item.DeliveryMethod != "3" {
		return fmt.Errorf("unsupported 16688 delivery method")
	}
	if item.LimitQuantity != "" && item.DeliveryMethod == "1" {
		minimum, err := strconv.Atoi(string(item.LimitQuantity))
		if err != nil || minimum < 1 {
			return fmt.Errorf("invalid upstream quantity limit")
		}
		if quantity < minimum {
			return fmt.Errorf("purchase quantity is below upstream minimum")
		}
	}
	quote, err := a.quote16688(ctx, code, quantity)
	if err != nil {
		return err
	}
	if quote.BalanceEnough == nil || !*quote.BalanceEnough {
		return fmt.Errorf("16688 operating wallet has insufficient balance or quote is incomplete")
	}
	return nil
}

func (a *externalAdapter) GetOrder(ctx context.Context, orderID uint) (*UpstreamOrderDetail, error) {
	orderNo, err := a.resolve("order", orderID)
	if err != nil {
		return nil, err
	}
	detail := &UpstreamOrderDetail{OrderID: orderID, OrderNo: orderNo, Currency: "CNY"}
	if a.protocol == constants.ConnectionProtocolYichiyuan {
		detail.Amount, err = a.store.ReferenceAmount(a.connectionID, a.namespace("order"), orderID)
		if err != nil {
			return nil, err
		}
		var result struct {
			Secret         string     `json:"secret"`
			Status         wireScalar `json:"status"`
			DeliveryStatus wireScalar `json:"delivery_status"`
		}
		if err := a.call(ctx, "/shared/commodity/query", map[string]string{"tradeNo": orderNo}, &result); err != nil {
			return nil, err
		}
		detail.Status = "accepted"
		// The documented status is PAYMENT, not delivery. Even automatic goods
		// can be held for review or resold from a delayed upstream. Require an
		// explicit delivery_status extension, never guess from secret text.
		if result.DeliveryStatus != "0" && result.DeliveryStatus != "1" {
			return nil, fmt.Errorf("Yichiyuan query must expose delivery_status; payment status and secret text cannot prove delivery")
		}
		if result.Status == "1" && result.DeliveryStatus == "1" && strings.TrimSpace(result.Secret) != "" {
			detail.Status = "delivered"
			detail.Fulfillment = externalFulfillment(result.Secret)
		}
		return detail, nil
	}
	var result struct {
		TradeNo     string     `json:"trade_no"`
		Status      wireScalar `json:"status"`
		TotalAmount wireScalar `json:"total_amount"`
		Cards       []struct {
			Code string `json:"code"`
		} `json:"cards"`
		Content           string `json:"content"`
		Instruction       string `json:"instruction"`
		ManualInstruction string `json:"manual_instruction"`
	}
	if err := a.call(ctx, "/openApi/purchase/query", map[string]string{"trade_no": orderNo}, &result); err != nil {
		return nil, err
	}
	if result.TradeNo != orderNo {
		return nil, fmt.Errorf("upstream order number mismatch")
	}
	amount, err := positiveMoney(string(result.TotalAmount))
	if err != nil {
		return nil, err
	}
	detail.Amount = amount
	switch result.Status {
	case "0", "1":
		detail.Status = "accepted"
	case "2":
		detail.Status = "canceled"
	case "3":
		detail.Status = "refunded"
		// The documented query has no actual refund amount. Do not assume
		// procurement fees were returned along with the goods payment.
	case "4", "5":
		parts := []string{}
		for _, card := range result.Cards {
			if strings.TrimSpace(card.Code) != "" {
				parts = append(parts, card.Code)
			}
		}
		if strings.TrimSpace(result.Content) != "" {
			parts = append(parts, result.Content)
		}
		if strings.TrimSpace(result.ManualInstruction) != "" {
			parts = append(parts, result.ManualInstruction)
		}
		if len(parts) == 0 {
			return nil, fmt.Errorf("upstream reports delivery without delivery content")
		}
		if strings.TrimSpace(result.Instruction) != "" {
			parts = append(parts, result.Instruction)
		}
		detail.Status = "delivered"
		detail.Fulfillment = externalFulfillment(strings.Join(parts, "\n"))
	default:
		return nil, fmt.Errorf("unknown 16688 order status")
	}
	return detail, nil
}

func externalFulfillment(payload string) *UpstreamFulfillment {
	return &UpstreamFulfillment{Type: constants.FulfillmentTypeAuto, Status: "delivered", Payload: payload, DeliveryData: jsonmap.JSON{}}
}
