package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dujiao-next/internal/constants"
	siteconnectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"
)

type remoteMemory struct {
	sync.Mutex
	next         uint
	refs         map[string]uint
	amounts      map[uint]string
	attempts     map[string]string
	fingerprints map[string]string
}

func newRemoteMemory() *remoteMemory {
	return &remoteMemory{refs: map[string]uint{}, amounts: map[uint]string{}, attempts: map[string]string{}, fingerprints: map[string]string{}}
}
func (s *remoteMemory) RegisterReference(c uint, ns, external string) (uint, error) {
	s.Lock()
	defer s.Unlock()
	if external == "" {
		return 0, fmt.Errorf("empty reference")
	}
	k := fmt.Sprintf("%d:%s:%s", c, ns, external)
	if id := s.refs[k]; id != 0 {
		return id, nil
	}
	s.next++
	s.refs[k] = s.next
	return s.next, nil
}
func (s *remoteMemory) ResolveReference(c uint, ns string, id uint) (string, error) {
	s.Lock()
	defer s.Unlock()
	prefix := fmt.Sprintf("%d:%s:", c, ns)
	for key, value := range s.refs {
		if value == id && len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			return key[len(prefix):], nil
		}
	}
	return "", fmt.Errorf("reference missing")
}
func (s *remoteMemory) SaveReferenceAmount(c uint, ns string, id uint, amount string) error {
	s.Lock()
	defer s.Unlock()
	s.amounts[id] = amount
	return nil
}
func (s *remoteMemory) ReferenceAmount(c uint, ns string, id uint) (string, error) {
	s.Lock()
	defer s.Unlock()
	return s.amounts[id], nil
}
func (s *remoteMemory) BeginPurchase(c uint, key, fingerprint string) (bool, string, error) {
	s.Lock()
	defer s.Unlock()
	key = fmt.Sprintf("%d:%s", c, key)
	if result, ok := s.attempts[key]; ok {
		if s.fingerprints[key] != fingerprint {
			return false, "", fmt.Errorf("fingerprint mismatch")
		}
		return false, result, nil
	}
	s.attempts[key] = ""
	s.fingerprints[key] = fingerprint
	return true, "", nil
}
func (s *remoteMemory) FinishPurchase(c uint, key, result string) error {
	s.Lock()
	defer s.Unlock()
	s.attempts[fmt.Sprintf("%d:%s", c, key)] = result
	return nil
}
func (s *remoteMemory) ReleasePurchase(c uint, key string) error {
	s.Lock()
	defer s.Unlock()
	delete(s.attempts, fmt.Sprintf("%d:%s", c, key))
	return nil
}

func TestExternalSignatureVectors(t *testing.T) {
	params := map[string]string{"sign": "ignored", "app_id": "42", "empty": "", "contact": "中文 +", "num": "2", "shared_code": "ABC"}
	if got := signYichiyuan(params, "secret+键"); got != "7e98db7352648c89b5b94768150e24df" {
		t.Fatal(got)
	}
	params = map[string]string{"app_id": "42", "empty": "", "nonce": "0123456789abcdef", "remark": "中文+/", "timestamp": "1710000000"}
	if got := sign16688("/openApi/stockSecret/invalidateAll", params, "secret"); got != "8b64aa2ae68d874943cdc6f669a9bf24851ab7ad12d42ba3a734808a76bb475b" {
		t.Fatal(got)
	}
}

func externalTestAdapter(t *testing.T, protocol string, store *remoteMemory, handler http.HandlerFunc) (*externalAdapter, func()) {
	t.Helper()
	server := httptest.NewServer(handler)
	conn := &siteconnectiondomain.Connection{ID: 1, Protocol: protocol, BaseURL: server.URL, ApiKey: "42", ApiSecret: "secret", YcyDeliveryStatusConfirmed: true}
	return newExternalAdapter(conn, t.TempDir(), store), server.Close
}

func assertSigned(t *testing.T, protocol string, r *http.Request) {
	t.Helper()
	if err := r.ParseForm(); err != nil {
		t.Error(err)
	}
	params := map[string]string{}
	for k, values := range r.PostForm {
		params[k] = values[0]
	}
	expected := signYichiyuan(params, "secret")
	if protocol == constants.ConnectionProtocol16688 {
		expected = sign16688(r.URL.Path, params, "secret")
		if len(params["nonce"]) != 32 {
			t.Error("invalid nonce")
		}
	}
	if params["app_id"] != "42" || params["sign"] != expected || params["app_key"] != "" {
		t.Error("invalid signed request")
	}
}

func TestExternal16688PurchaseReceiptAndPolling(t *testing.T) {
	store := newRemoteMemory()
	var purchases atomic.Int32
	a, closeServer := externalTestAdapter(t, constants.ConnectionProtocol16688, store, func(w http.ResponseWriter, r *http.Request) {
		assertSigned(t, constants.ConnectionProtocol16688, r)
		var data any
		switch r.URL.Path {
		case "/openApi/merchant/balance":
			data = map[string]any{"operate_money": "100.25"}
		case "/openApi/goods/detail":
			data = map[string]any{"goods_no": "SG20001", "source": "supply", "name": "Card", "unit_price": 9.5, "delivery_method": 1, "available_quantity": 10, "limit_quantity": 1}
		case "/openApi/purchase/quotePrice":
			data = map[string]any{"total_amount": "9.56", "balance_enough": true}
		case "/openApi/purchase/create":
			purchases.Add(1)
			data = map[string]any{"trade_no": "T2603161200001", "total_amount": "9.56"}
		case "/openApi/purchase/query":
			if r.PostForm.Get("trade_no") != "T2603161200001" {
				t.Error("lost opaque order identifier")
			}
			data = map[string]any{"trade_no": "T2603161200001", "total_amount": 9.56, "status": 4, "cards": []any{map[string]any{"code": "TEST-CARD"}}, "instruction": "Instructions"}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "data": data})
	})
	defer closeServer()
	ctx := context.Background()
	id, _ := a.reference("product", "SG20001")
	ping, err := a.Ping(ctx)
	if err != nil || ping.Balance != "100.25" {
		t.Fatalf("ping %v %v", ping, err)
	}
	p, err := a.GetProduct(ctx, id)
	if err != nil || p.PriceAmount != "9.56" {
		t.Fatalf("product %v %v", p, err)
	}
	req := CreateUpstreamOrderReq{SKUID: id, Quantity: 1, DownstreamOrderNo: "LOCAL-1", TraceID: "trace-1"}
	result, err := a.CreateOrder(ctx, req)
	if err != nil || !result.OK {
		t.Fatalf("create %v %v", result, err)
	}
	// A fresh adapter simulates a different worker/process sharing persisted state.
	b := *a
	replay, err := b.CreateOrder(ctx, req)
	if err != nil || !replay.OK || replay.OrderID != result.OrderID || purchases.Load() != 1 {
		t.Fatalf("unsafe replay %v %v", replay, err)
	}
	detail, err := b.GetOrder(ctx, result.OrderID)
	if err != nil || detail.Status != "delivered" || detail.Fulfillment.Payload != "TEST-CARD\nInstructions" {
		t.Fatalf("poll %v %v", detail, err)
	}
	req.Quantity = 2
	changed, err := b.CreateOrder(ctx, req)
	if err != nil || changed.OK || purchases.Load() != 1 {
		t.Fatal("changed replay sent another purchase")
	}
}

func TestExternalUncertainPurchaseBlocksConcurrentAndManualRetry(t *testing.T) {
	store := newRemoteMemory()
	var purchases atomic.Int32
	a, closeServer := externalTestAdapter(t, constants.ConnectionProtocol16688, store, func(w http.ResponseWriter, r *http.Request) {
		assertSigned(t, constants.ConnectionProtocol16688, r)
		switch r.URL.Path {
		case "/openApi/goods/detail":
			_, _ = w.Write([]byte(`{"code":1,"data":{"goods_no":"SG1","source":"supply","delivery_method":1,"limit_quantity":1}}`))
		case "/openApi/purchase/quotePrice":
			_, _ = w.Write([]byte(`{"code":1,"data":{"total_amount":1,"balance_enough":true}}`))
		case "/openApi/purchase/create":
			purchases.Add(1)
			w.WriteHeader(http.StatusBadGateway)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	defer closeServer()
	id, _ := a.reference("product", "SG1")
	req := CreateUpstreamOrderReq{SKUID: id, Quantity: 1, TraceID: "stable", DownstreamOrderNo: "LOCAL"}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			result, err := a.CreateOrder(context.Background(), req)
			if err != nil || result.ErrorCode != "purchase_uncertain" {
				t.Errorf("unsafe outcome %v %v", result, err)
			}
		})
	}
	wg.Wait()
	if purchases.Load() != 1 {
		t.Fatalf("purchase count %d", purchases.Load())
	}
	result, err := a.CreateOrder(context.Background(), req)
	if err != nil || result.OK || purchases.Load() != 1 {
		t.Fatal("manual retry resent uncertain purchase")
	}
}

func TestExternalYcyRoundTripAndUnsupportedVariants(t *testing.T) {
	store := newRemoteMemory()
	variant := false
	secret := "TEST-SECRET"
	deliveryStatus := "1"
	var purchases atomic.Int32
	a, closeServer := externalTestAdapter(t, constants.ConnectionProtocolYichiyuan, store, func(w http.ResponseWriter, r *http.Request) {
		assertSigned(t, constants.ConnectionProtocolYichiyuan, r)
		var data any
		switch r.URL.Path {
		case "/shared/authentication/connect":
			data = []any{}
		case "/shared/commodity/items":
			data = []any{map[string]any{"id": 10, "name": "Cards", "children": []any{map[string]any{"code": "OPAQUE-CODE", "name": "Card", "user_price": "2.50", "stock": "5", "delivery_way": 0}}}}
		case "/shared/commodity/item":
			config := map[string]any{}
			if variant {
				config["sku"] = map[string]any{"size": "large"}
			}
			data = map[string]any{"code": "OPAQUE-CODE", "name": "Card", "user_price": "2.50", "stock": 5, "delivery_way": 0, "config": config, "minimum": 1, "maximum": 3}
		case "/shared/commodity/stock":
			data = map[string]any{"stock": "5"}
		case "/shared/commodity/valuation":
			data = map[string]any{"price": "2.50"}
		case "/shared/commodity/inventoryState":
			data = []any{}
		case "/shared/commodity/trade":
			purchases.Add(1)
			if r.PostForm.Get("request_no") == "" || r.PostForm.Get("shared_code") != "OPAQUE-CODE" {
				t.Error("missing idempotency/code")
			}
			data = map[string]any{"tradeNo": "123260422101010888", "amount": "2.50"}
		case "/shared/commodity/query":
			if r.PostForm.Get("tradeNo") != "123260422101010888" {
				t.Error("wrong tradeNo")
			}
			data = map[string]any{"status": 1, "secret": secret}
			if deliveryStatus != "" {
				data.(map[string]any)["delivery_status"] = deliveryStatus
			}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": data})
	})
	defer closeServer()
	ctx := context.Background()
	list, err := a.ListProducts(ctx, ListProductsOpts{Page: 1, PageSize: 10})
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("list %v %v", list, err)
	}
	p, err := a.GetProduct(ctx, list.Items[0].ID)
	if err != nil || p.CategoryID == 0 || p.MaxPurchaseQuantity != 3 {
		t.Fatalf("get %v %v", p, err)
	}
	result, err := a.CreateOrder(ctx, CreateUpstreamOrderReq{SKUID: p.SKUs[0].ID, Quantity: 1, TraceID: "trace", DownstreamOrderNo: "LOCAL"})
	if err != nil || !result.OK {
		t.Fatalf("create %v %v", result, err)
	}
	detail, err := a.GetOrder(ctx, result.OrderID)
	if err != nil || detail.Status != "delivered" || detail.Amount != "2.5" {
		t.Fatalf("order %v %v", detail, err)
	}
	secret = ""
	detail, err = a.GetOrder(ctx, result.OrderID)
	if err != nil || detail.Status != "accepted" || detail.Fulfillment != nil {
		t.Fatal("empty payload delivered")
	}
	secret = "Waiting for manual review"
	deliveryStatus = "0"
	detail, err = a.GetOrder(ctx, result.OrderID)
	if err != nil || detail.Status != "accepted" {
		t.Fatal("payment/review text treated as delivery")
	}
	deliveryStatus = ""
	if _, err = a.GetOrder(ctx, result.OrderID); err == nil {
		t.Fatal("missing delivery proof accepted")
	}
	a.ycyDeliveryStatusConfirmed = false
	blocked, err := a.CreateOrder(ctx, CreateUpstreamOrderReq{SKUID: p.ID, Quantity: 1, TraceID: "blocked", DownstreamOrderNo: "LOCAL-2"})
	if err != nil || blocked.OK || blocked.ErrorCode != "invalid_request" {
		t.Fatal("unverified upstream purchased")
	}
	variant = true
	if _, err = a.GetProduct(ctx, p.ID); err == nil {
		t.Fatal("unsupported variant imported")
	}
	if purchases.Load() != 1 {
		t.Fatal("unexpected purchase")
	}
}

func TestExternal16688PaginationKeepsCallerPageSize(t *testing.T) {
	store := newRemoteMemory()
	a, closeServer := externalTestAdapter(t, constants.ConnectionProtocol16688, store, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		var data any
		switch r.URL.Path {
		case "/openApi/goods/list":
			if r.PostForm.Get("page_size") != "100" {
				t.Error("upstream page size exceeds documented bound")
			}
			page, _ := strconv.Atoi(r.PostForm.Get("page_no"))
			items := []any{}
			for n := (page-1)*100 + 1; n <= page*100 && n <= 235; n++ {
				items = append(items, map[string]any{"goods_no": fmt.Sprintf("SG%d", n), "source": "supply", "delivery_method": 1, "available_quantity": 1, "unit_price": 1})
			}
			data = map[string]any{"total": 235, "list": items}
		case "/openApi/purchase/quotePrice":
			data = map[string]any{"total_amount": 1, "balance_enough": true}
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "data": data})
	})
	defer closeServer()
	for _, test := range []struct {
		page, size, count int
		first, last       string
	}{{1, 200, 200, "SG1", "SG200"}, {2, 200, 35, "SG201", "SG235"}, {2, 50, 50, "SG51", "SG100"}, {3, 70, 70, "SG141", "SG210"}} {
		result, err := a.ListProducts(context.Background(), ListProductsOpts{Page: test.page, PageSize: test.size})
		if err != nil || len(result.Items) != test.count || result.Total != 235 {
			t.Fatalf("pagination %v %v", result, err)
		}
		if result.Items[0].SKUs[0].SKUCode != test.first || result.Items[len(result.Items)-1].SKUs[0].SKUCode != test.last {
			t.Fatal("pagination skipped or duplicated products")
		}
	}
}

func TestExternalPurchaseNeverFollowsRedirect(t *testing.T) {
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
	defer target.Close()
	a, closeServer := externalTestAdapter(t, constants.ConnectionProtocol16688, newRemoteMemory(), func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	})
	defer closeServer()
	if err := a.call(context.Background(), "/openApi/purchase/create", nil, nil); err == nil {
		t.Fatal("redirect accepted")
	}
	if received.Load() != 0 {
		t.Fatal("signed credentials forwarded across hosts")
	}
}
