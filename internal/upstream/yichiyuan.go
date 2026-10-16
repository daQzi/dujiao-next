package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/dujiao-next/internal/constants"
	"github.com/shopspring/decimal"
)

type ycyProduct struct {
	ID             wireScalar      `json:"id"`
	Code           string          `json:"code"`
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	Cover          string          `json:"cover"`
	Price          wireScalar      `json:"price"`
	UserPrice      wireScalar      `json:"user_price"`
	Stock          wireScalar      `json:"stock"`
	DeliveryWay    wireScalar      `json:"delivery_way"`
	DraftStatus    wireScalar      `json:"draft_status"`
	Minimum        wireScalar      `json:"minimum"`
	Maximum        wireScalar      `json:"maximum"`
	ContactType    wireScalar      `json:"contact_type"`
	PasswordStatus wireScalar      `json:"password_status"`
	Config         json.RawMessage `json:"config"`
	Widget         json.RawMessage `json:"widget"`
}

type ycyCategory struct {
	ID       wireScalar   `json:"id"`
	Name     string       `json:"name"`
	Children []ycyProduct `json:"children"`
}

func (a *externalAdapter) ycyCatalog(ctx context.Context) ([]ycyCategory, error) {
	var items []ycyCategory
	err := a.call(ctx, "/shared/commodity/items", nil, &items)
	return items, err
}

func (a *externalAdapter) ListCategories(ctx context.Context) (*CategoryListResult, error) {
	if a.protocol == constants.ConnectionProtocol16688 {
		// The 16688 goods endpoint does not identify a source category. Do not
		// incorrectly associate a merchant's local categories with supply goods.
		return &CategoryListResult{Supported: false, Categories: []UpstreamCategory{}}, nil
	}
	items, err := a.ycyCatalog(ctx)
	if err != nil {
		return nil, err
	}
	result := &CategoryListResult{Supported: true, Categories: []UpstreamCategory{}}
	for _, category := range items {
		id, err := a.reference("category", string(category.ID))
		if err != nil {
			return nil, err
		}
		result.Categories = append(result.Categories, UpstreamCategory{ID: id, Name: locale(category.Name)})
	}
	return result, nil
}

func (a *externalAdapter) ListProducts(ctx context.Context, opts ListProductsOpts) (*ProductListResult, error) {
	if a.protocol == constants.ConnectionProtocol16688 {
		return a.list16688(ctx, opts)
	}
	categories, err := a.ycyCatalog(ctx)
	if err != nil {
		return nil, err
	}
	items := []UpstreamProduct{}
	for _, category := range categories {
		categoryID, err := a.reference("category", string(category.ID))
		if err != nil {
			return nil, err
		}
		for _, item := range category.Children {
			if item.DeliveryWay != "0" {
				continue
			}
			if unsupportedYcyOptions(item) {
				continue
			}
			p, err := a.mapYcy(item, categoryID)
			if err != nil {
				return nil, fmt.Errorf("invalid Yichiyuan catalog product: %w", err)
			}
			items = append(items, *p)
		}
	}
	page, size := externalPage(opts)
	start := (page - 1) * size
	if start > len(items) {
		start = len(items)
	}
	end := start + size
	if end > len(items) {
		end = len(items)
	}
	return &ProductListResult{Total: len(items), Items: items[start:end]}, nil
}

func externalPage(opts ListProductsOpts) (int, int) {
	page, size := opts.Page, opts.PageSize
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 50
	}
	if size > 200 {
		size = 200
	}
	return page, size
}

func (a *externalAdapter) mapYcy(item ycyProduct, categoryID uint) (*UpstreamProduct, error) {
	id, err := a.reference("product", item.Code)
	if err != nil {
		return nil, err
	}
	price := item.UserPrice
	if price == "" {
		price = item.Price
	}
	amount, err := positiveMoney(string(price))
	if err != nil {
		return nil, err
	}
	stock, err := stockQuantity(item.Stock)
	if err != nil {
		return nil, err
	}
	images := []string{}
	if item.Cover != "" {
		images = append(images, item.Cover)
	}
	return &UpstreamProduct{
		ID: id, Slug: fmt.Sprintf("ycy-%d", id), Title: locale(item.Name), Description: locale(item.Description), Content: locale(item.Description),
		CategoryID: categoryID, PriceAmount: amount, Currency: "CNY", FulfillmentType: constants.FulfillmentTypeAuto, IsActive: a.ycyDeliveryStatusConfirmed,
		Images: images,
		SKUs:   []UpstreamSKU{{ID: id, SKUCode: item.Code, PriceAmount: amount, StockQuantity: stock, StockStatus: stockStatus(stock), IsActive: a.ycyDeliveryStatusConfirmed}},
	}, nil
}

func nonemptyJSON(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null" && s != "[]" && s != "{}" && s != `""`
}

// Refuse unsupported variants instead of silently buying a default option.
func validateYcy(item ycyProduct, quantity int) error {
	if item.DeliveryWay != "0" {
		return fmt.Errorf("only automatic Yichiyuan card delivery is supported")
	}
	if unsupportedYcyOptions(item) {
		return fmt.Errorf("Yichiyuan category/SKU/widget products require a dedicated variant mapping and are not supported yet")
	}
	if item.ContactType != "" && item.ContactType != "0" {
		return fmt.Errorf("Yichiyuan product requires a typed contact; not supported yet")
	}
	if item.PasswordStatus != "" && item.PasswordStatus != "0" {
		return fmt.Errorf("Yichiyuan product requires an order password; not supported yet")
	}
	for _, bound := range []struct {
		raw     wireScalar
		minimum bool
	}{{item.Minimum, true}, {item.Maximum, false}} {
		if bound.raw == "" {
			continue
		}
		n, err := strconv.Atoi(string(bound.raw))
		if err != nil || n < 0 {
			return fmt.Errorf("invalid Yichiyuan quantity limit")
		}
		if quantity > 0 && n > 0 && ((bound.minimum && quantity < n) || (!bound.minimum && quantity > n)) {
			return fmt.Errorf("purchase quantity is outside the upstream limits")
		}
	}
	return nil
}

func unsupportedYcyOptions(item ycyProduct) bool {
	if nonemptyJSON(item.Widget) {
		return true
	}
	if !nonemptyJSON(item.Config) {
		return false
	}
	var config map[string]json.RawMessage
	if json.Unmarshal(item.Config, &config) == nil {
		return nonemptyJSON(config["category"]) || nonemptyJSON(config["sku"])
	}
	var ini string
	if json.Unmarshal(item.Config, &ini) == nil {
		// Catalogs can return INI text; product details are documented as objects.
		// Do not guess how a non-empty text configuration is structured.
		return strings.TrimSpace(ini) != ""
	}
	return true
}

func (a *externalAdapter) ycyDetail(ctx context.Context, code string) (*ycyProduct, error) {
	var item ycyProduct
	if err := a.call(ctx, "/shared/commodity/item", map[string]string{"code": code}, &item); err != nil {
		return nil, err
	}
	if item.Code != code {
		return nil, fmt.Errorf("upstream product code mismatch")
	}
	return &item, nil
}

func (a *externalAdapter) GetProduct(ctx context.Context, productID uint) (*UpstreamProduct, error) {
	code, err := a.resolve("product", productID)
	if err != nil {
		return nil, err
	}
	if a.protocol == constants.ConnectionProtocol16688 {
		return a.get16688(ctx, code)
	}
	item, err := a.ycyDetail(ctx, code)
	if err != nil {
		return nil, err
	}
	if err := validateYcy(*item, 0); err != nil {
		return nil, err
	}
	var inventory struct {
		Stock wireScalar `json:"stock"`
	}
	if err := a.call(ctx, "/shared/commodity/stock", map[string]string{"code": code}, &inventory); err != nil {
		return nil, err
	}
	item.Stock = inventory.Stock
	var quote struct {
		Price        wireScalar `json:"price"`
		CurrencyCode string     `json:"currency_code"`
	}
	quantity := 1
	if minimum, _ := strconv.Atoi(string(item.Minimum)); minimum > 1 {
		quantity = minimum
	}
	if err := a.call(ctx, "/shared/commodity/valuation", map[string]string{"code": code, "num": strconv.Itoa(quantity)}, &quote); err != nil {
		return nil, err
	}
	if _, err := positiveMoney(string(quote.Price)); err != nil {
		return nil, err
	}
	if quote.CurrencyCode != "" && quote.CurrencyCode != "CNY" {
		return nil, fmt.Errorf("this Yichiyuan adapter requires a CNY upstream")
	}
	quoted, _ := decimal.NewFromString(string(quote.Price))
	item.UserPrice = wireScalar(quoted.Div(decimal.NewFromInt(int64(quantity))).RoundCeil(2).String())
	product, err := a.mapYcy(*item, 0)
	if err != nil {
		return nil, err
	}
	if item.Minimum != "" {
		product.MinPurchaseQuantity, _ = strconv.Atoi(string(item.Minimum))
	}
	if item.Maximum != "" {
		product.MaxPurchaseQuantity, _ = strconv.Atoi(string(item.Maximum))
	}
	// Detail responses do not document a category identifier. Resolve it from
	// the same public catalog used by the import screen, not an invented field.
	categories, err := a.ycyCatalog(ctx)
	if err != nil {
		return nil, err
	}
	for _, category := range categories {
		for _, child := range category.Children {
			if child.Code == code {
				product.CategoryID, err = a.reference("category", string(category.ID))
				if err != nil {
					return nil, err
				}
				return product, nil
			}
		}
	}
	return product, nil
}
