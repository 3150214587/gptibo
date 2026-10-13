package config

import (
	"errors"
	"gptibo/storefront/internal/payments"
	"os"
)

func flag(name string) (bool, error) {
	if name != "MIGRATION_REHEARSAL" {
		rehearsal, err := flag("MIGRATION_REHEARSAL")
		if err != nil {
			return false, err
		}
		if rehearsal {
			return false, nil
		}
	}
	v := os.Getenv(name)
	if v != "" && v != "false" && v != "true" {
		return false, errors.New(name + " must be true or false")
	}
	return v == "true", nil
}
func LoadPayments() (payments.Service, error) {
	s := payments.Service{Providers: map[string]payments.Provider{}}
	enabled, e := flag("ALIPAY_ENABLED")
	if e != nil {
		return s, e
	}
	s.Checkout, e = flag("PAYMENT_CHECKOUT_ENABLED")
	if e != nil {
		return s, e
	}
	if enabled {
		private, e := Secret("ALIPAY_APP_PRIVATE_KEY")
		if e != nil {
			return s, e
		}
		public, e := Secret("ALIPAY_PUBLIC_KEY")
		if e != nil {
			return s, e
		}
		s.Provider, e = payments.NewAlipay(payments.AlipayConfig{AppID: os.Getenv("ALIPAY_APP_ID"), SellerID: os.Getenv("ALIPAY_SELLER_ID"), PrivateKey: private, PublicKey: public, Gateway: os.Getenv("ALIPAY_GATEWAY")})
		if e != nil {
			return s, e
		}
		s.Providers[s.Provider.ID()] = s.Provider
	}
	gatewayaEnabled, e := flag("GATEWAYA_ENABLED")
	if e != nil {
		return s, e
	}
	if gatewayaEnabled {
		private, e := Secret("GATEWAYA_PRIVATE_KEY")
		if e != nil {
			return s, e
		}
		public, e := Secret("GATEWAYA_PUBLIC_KEY")
		if e != nil {
			return s, e
		}
		for _, kind := range []string{"alipay", "wxpay"} {
			provider, e := payments.NewGATEWAYA(payments.GATEWAYAConfig{PID: os.Getenv("GATEWAYA_PID"), PrivateKey: private, PublicKey: public, Type: kind})
			if e != nil {
				return s, e
			}
			s.Providers[provider.ID()] = provider
			if s.Provider == nil {
				s.Provider = provider
			}
		}
	}
	gatewaybEnabled, e := flag("GATEWAYB_ENABLED")
	if e != nil {
		return s, e
	}
	if gatewaybEnabled {
		key, e := Secret("GATEWAYB_KEY")
		if e != nil {
			return s, e
		}
		for _, kind := range []string{"alipay", "wxpay"} {
			channel := os.Getenv("GATEWAYB_ALIPAY_CID")
			if kind == "wxpay" {
				channel = os.Getenv("GATEWAYB_WXPAY_CID")
			}
			p, e := payments.NewGATEWAYB(payments.GATEWAYBConfig{PID: os.Getenv("GATEWAYB_PID"), Key: key, Type: kind, ChannelID: channel})
			if e != nil {
				return s, e
			}
			s.Providers[p.ID()] = p
			if s.Provider == nil {
				s.Provider = p
			}
		}
	}
	if s.Checkout && s.Provider == nil {
		return s, errors.New("a payment provider must be configured before enabling checkout")
	}
	s.NotifyOrigin = os.Getenv("PAYMENT_NOTIFY_ORIGIN")
	return s, nil
}
