package upstream

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/dujiao-next/internal/constants"
	"github.com/shopspring/decimal"
)

type goods16688 struct {
	GoodsNo           string     `json:"goods_no"`
	Source            string     `json:"source"`
	Name              string     `json:"name"`
	Description       string     `json:"description"`
	Image             string     `json:"image"`
	DeliveryMethod    wireScalar `json:"delivery_method"`
	AvailableQuantity wireScalar `json:"available_quantity"`
	LimitQuantity     wireScalar `json:"limit_quantity"`
	UnitPrice         wireScalar `json:"unit_price"`
}

type quote16688 struct {
	TotalAmount   wireScalar `json:"total_amount"`
	BalanceEnough *bool      `json:"balance_enough"`
}

func (a *externalAdapter) list16688(ctx context.Context, opts ListProductsOpts) (*ProductListResult, error) {
	page, size := externalPage(opts)
	// Local sync allows 200 rows but the upstream caps pages at 100. Translate
	// offsets, rather than silently changing the caller's pagination contract.
	const upstreamSize = 100
	offset := (page - 1) * size
	out := &ProductListResult{Items: []UpstreamProduct{}}
	items := []goods16688{}
	for remotePage := offset/upstreamSize + 1; len(items) < size; remotePage++ {
		var result struct {
			Total int          `json:"total"`
			List  []goods16688 `json:"list"`
		}
		if err := a.call(ctx, "/openApi/goods/list", map[string]string{"page_no": strconv.Itoa(remotePage), "page_size": strconv.Itoa(upstreamSize), "source": "supply"}, &result); err != nil {
			return nil, err
		}
		if result.Total < 0 || len(result.List) > upstreamSize {
			return nil, fmt.Errorf("invalid upstream catalog pagination")
		}
		out.Total = result.Total
		skip := offset % upstreamSize
		if len(items) > 0 {
			skip = 0
		}
		if skip > len(result.List) {
			skip = len(result.List)
		}
		remaining := size - len(items)
		tail := result.List[skip:]
		if len(tail) > remaining {
			tail = tail[:remaining]
		}
		items = append(items, tail...)
		if len(result.List) < upstreamSize || remotePage*upstreamSize >= result.Total {
			break
		}
	}
	// Catalog prices omit fees. Quote each item with bounded concurrency so a
	// normal 50-row page can complete within the caller's request deadline.
	out.Items = make([]UpstreamProduct, len(items))
	errors := make([]error, len(items))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for n := 0; n < 4; n++ {
		workers.Go(func() {
			for i := range jobs {
				product, err := a.priced16688(ctx, items[i])
				errors[i] = err
				if err == nil {
					out.Items[i] = *product
				}
			}
		})
	}
	for i := range items {
		jobs <- i
	}
	close(jobs)
	workers.Wait()
	for _, err := range errors {
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (a *externalAdapter) detail16688(ctx context.Context, code string) (*goods16688, error) {
	var item goods16688
	if err := a.call(ctx, "/openApi/goods/detail", map[string]string{"goods_no": code}, &item); err != nil {
		return nil, err
	}
	if item.GoodsNo != code || item.Source != "supply" {
		return nil, fmt.Errorf("16688 only supports procurement of already-agented supply goods")
	}
	return &item, nil
}

func (a *externalAdapter) quote16688(ctx context.Context, code string, quantity int) (*quote16688, error) {
	var quote quote16688
	if err := a.call(ctx, "/openApi/purchase/quotePrice", map[string]string{"goods_no": code, "quantity": strconv.Itoa(quantity)}, &quote); err != nil {
		return nil, err
	}
	if _, err := positiveMoney(string(quote.TotalAmount)); err != nil {
		return nil, err
	}
	return &quote, nil
}

func (a *externalAdapter) get16688(ctx context.Context, code string) (*UpstreamProduct, error) {
	item, err := a.detail16688(ctx, code)
	if err != nil {
		return nil, err
	}
	return a.priced16688(ctx, *item)
}

func (a *externalAdapter) priced16688(ctx context.Context, item goods16688) (*UpstreamProduct, error) {
	if item.DeliveryMethod != "3" && item.AvailableQuantity == "0" {
		return a.unavailable16688(item)
	}
	quantity := 1
	if item.LimitQuantity != "" {
		n, err := strconv.Atoi(string(item.LimitQuantity))
		if err != nil || n < 1 {
			return nil, fmt.Errorf("invalid 16688 minimum quantity")
		}
		quantity = n
	}
	quote, err := a.quote16688(ctx, item.GoodsNo, quantity)
	if err != nil {
		if is16688SoldOut(err) {
			return a.unavailable16688(item)
		}
		return nil, err
	}
	amount, _ := decimal.NewFromString(string(quote.TotalAmount))
	// Include procurement fees in the imported cost, rounding up to a cent.
	item.UnitPrice = wireScalar(amount.Div(decimal.NewFromInt(int64(quantity))).RoundCeil(2).String())
	return a.map16688(item)
}

func (a *externalAdapter) unavailable16688(item goods16688) (*UpstreamProduct, error) {
	product, err := a.map16688(item)
	if err != nil {
		return nil, err
	}
	product.IsActive = false
	for i := range product.SKUs {
		product.SKUs[i].IsActive = false
		product.SKUs[i].StockQuantity = 0
		product.SKUs[i].StockStatus = "out_of_stock"
	}
	return product, nil
}

func (a *externalAdapter) map16688(item goods16688) (*UpstreamProduct, error) {
	if item.Source != "supply" {
		return nil, fmt.Errorf("16688 returned non-supply goods")
	}
	if item.DeliveryMethod != "1" && item.DeliveryMethod != "2" && item.DeliveryMethod != "3" {
		return nil, fmt.Errorf("invalid 16688 delivery method")
	}
	id, err := a.reference("product", item.GoodsNo)
	if err != nil {
		return nil, err
	}
	amount, err := positiveMoney(string(item.UnitPrice))
	if err != nil {
		return nil, err
	}
	stock := -1
	if item.DeliveryMethod != "3" {
		stock, err = stockQuantity(item.AvailableQuantity)
		if err != nil {
			return nil, err
		}
	}
	fulfillment := constants.FulfillmentTypeAuto
	if item.DeliveryMethod == "3" {
		fulfillment = constants.FulfillmentTypeManual
	}
	images := []string{}
	if item.Image != "" {
		images = append(images, item.Image)
	}
	minimum, maximum := 0, 0
	if item.LimitQuantity != "" {
		minimum, err = strconv.Atoi(string(item.LimitQuantity))
		if err != nil || minimum < 1 {
			return nil, fmt.Errorf("invalid 16688 minimum quantity")
		}
	}
	if item.DeliveryMethod == "2" {
		minimum, maximum = 1, 1
	}
	return &UpstreamProduct{
		ID: id, Slug: fmt.Sprintf("16688-%d", id), Title: locale(item.Name), Description: locale(item.Description), Content: locale(item.Description), Images: images,
		PriceAmount: amount, Currency: "CNY", FulfillmentType: fulfillment, IsActive: true,
		MinPurchaseQuantity: minimum, MaxPurchaseQuantity: maximum,
		SKUs: []UpstreamSKU{{ID: id, SKUCode: item.GoodsNo, PriceAmount: amount, StockQuantity: stock, StockStatus: stockStatus(stock), IsActive: true}},
	}, nil
}
