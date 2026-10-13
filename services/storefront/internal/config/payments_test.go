package config

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"strings"
	"testing"
)

func TestPaymentProvidersRequireKeysAndKeepOfficialDefault(t *testing.T) {
	t.Setenv("GATEWAYB_ENABLED", "false")
	for _, name := range []string{"ALIPAY_ENABLED", "ALIPAY_APP_ID", "ALIPAY_SELLER_ID", "ALIPAY_APP_PRIVATE_KEY", "ALIPAY_PUBLIC_KEY", "ALIPAY_APP_PRIVATE_KEY_FILE", "ALIPAY_PUBLIC_KEY_FILE", "ALIPAY_GATEWAY", "GATEWAYA_ENABLED", "GATEWAYA_PID", "GATEWAYA_PRIVATE_KEY", "GATEWAYA_PUBLIC_KEY", "GATEWAYA_PRIVATE_KEY_FILE", "GATEWAYA_PUBLIC_KEY_FILE", "PAYMENT_CHECKOUT_ENABLED"} {
		t.Setenv(name, "")
	}
	s, e := LoadPayments()
	if e != nil || s.Provider != nil {
		t.Fatal("unsafe default", e)
	}
	t.Setenv("PAYMENT_CHECKOUT_ENABLED", "true")
	if _, e = LoadPayments(); e == nil {
		t.Fatal("checkout without credentials")
	}
	t.Setenv("GATEWAYA_ENABLED", "true")
	if _, e = LoadPayments(); e == nil {
		t.Fatal("GATEWAYA enabled without credentials")
	}
	k, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	priv, _ := x509.MarshalPKCS8PrivateKey(k)
	pub, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	t.Setenv("GATEWAYA_PID", "12345")
	t.Setenv("GATEWAYA_PRIVATE_KEY", base64.StdEncoding.EncodeToString(priv))
	t.Setenv("GATEWAYA_PUBLIC_KEY", base64.StdEncoding.EncodeToString(pub))
	s, e = LoadPayments()
	if e != nil || s.Provider.ID() != "gatewaya-alipay" || s.Lookup("gatewaya-wxpay") == nil {
		t.Fatal("GATEWAYA-only configuration failed", e)
	}
	t.Setenv("ALIPAY_ENABLED", "true")
	t.Setenv("ALIPAY_APP_ID", "12345")
	t.Setenv("ALIPAY_SELLER_ID", "67890")
	t.Setenv("ALIPAY_APP_PRIVATE_KEY", base64.StdEncoding.EncodeToString(priv))
	t.Setenv("ALIPAY_PUBLIC_KEY", base64.StdEncoding.EncodeToString(pub))
	s, e = LoadPayments()
	if e != nil || s.Provider.ID() != "alipay" || len(s.Providers) != 3 {
		t.Fatal("configuring GATEWAYA changed default", e)
	}
}

func TestGATEWAYBConfigurationIsOptInAndLoadsBothMethods(t *testing.T) {
	t.Setenv("MIGRATION_REHEARSAL", "false")
	for _, name := range []string{"ALIPAY_ENABLED", "GATEWAYA_ENABLED", "PAYMENT_CHECKOUT_ENABLED", "GATEWAYB_ENABLED", "GATEWAYB_PID", "GATEWAYB_KEY", "GATEWAYB_KEY_FILE", "GATEWAYB_ALIPAY_CID", "GATEWAYB_WXPAY_CID"} {
		t.Setenv(name, "")
	}
	s, e := LoadPayments()
	if e != nil || len(s.Providers) != 0 {
		t.Fatal(s, e)
	}
	t.Setenv("GATEWAYB_ENABLED", "true")
	if _, e = LoadPayments(); e == nil {
		t.Fatal("enabled without merchant credentials")
	}
	t.Setenv("GATEWAYB_PID", "1234")
	t.Setenv("GATEWAYB_KEY", strings.Repeat("test", 8))
	t.Setenv("PAYMENT_CHECKOUT_ENABLED", "true")
	s, e = LoadPayments()
	if e != nil || s.Lookup("gatewayb-alipay") == nil || s.Lookup("gatewayb-wxpay") == nil || len(s.Providers) != 2 || s.Provider.ID() != "gatewayb-alipay" {
		t.Fatal(s, e)
	}
	t.Setenv("GATEWAYB_ALIPAY_CID", "12&key=wrong")
	if _, e = LoadPayments(); e == nil {
		t.Fatal("invalid channel accepted")
	}
}
