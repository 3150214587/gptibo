package config

import (
	"errors"
	"gptibo/storefront/internal/procurement"
	"os"
	"strconv"
)

func LoadProcurement() (*procurement.Service, error) {
	s := &procurement.Service{}
	var e error
	s.EmailEnabled, e = flag("EMAIL_PRODUCTS_ENABLED")
	if e != nil {
		return nil, e
	}
	s.GmailEnabled, e = flag("ACCOUNT_SUPPLIER_CHECKOUT_ENABLED")
	if e != nil {
		return nil, e
	}
	s.NYEnabled, e = flag("DELIVERY_SUPPLIER_CHECKOUT_ENABLED")
	if e != nil {
		return nil, e
	}
	if os.Getenv("ORDER_TESTING_ENABLED") == "true" {
		s.EmailEnabled = false
	}
	enabled, e := flag("ACCOUNT_SUPPLIER_ENABLED")
	if e != nil {
		return nil, e
	}
	if enabled {
		secret, e := Secret("ACCOUNT_SUPPLIER_APP_SECRET")
		if e != nil {
			return nil, e
		}
		s.AccountSupplier, e = procurement.NewAccountSupplier(os.Getenv("ACCOUNT_SUPPLIER_API_BASE"), os.Getenv("ACCOUNT_SUPPLIER_APP_ID"), secret)
		if e != nil {
			return nil, e
		}
		s.GoodsID, _ = strconv.ParseInt(os.Getenv("ACCOUNT_SUPPLIER_GOODS_ID"), 10, 64)
		s.MaxCents, _ = strconv.ParseInt(os.Getenv("ACCOUNT_SUPPLIER_MAX_COST_CENTS"), 10, 64)
		if s.GmailEnabled && (s.GoodsID < 1 || s.MaxCents < 1 || s.MaxCents > 10000000) {
			return nil, errors.New("invalid Gmail goods or cost ceiling")
		}
	}
	enabled, e = flag("DELIVERY_SUPPLIER_ENABLED")
	if e != nil {
		return nil, e
	}
	if enabled {
		key, e := Secret("DELIVERY_SUPPLIER_API_KEY")
		if e != nil {
			return nil, e
		}
		s.DeliverySupplier, e = procurement.NewDeliverySupplier(key)
		if e != nil {
			return nil, e
		}
		s.FX, e = strconv.ParseFloat(os.Getenv("DELIVERY_SUPPLIER_CNY_PER_USD"), 64)
		if e != nil || !(s.FX > 0 && s.FX <= 100) {
			return nil, errors.New("invalid DELIVERY_SUPPLIER_CNY_PER_USD")
		}
	}
	return s, nil
}
