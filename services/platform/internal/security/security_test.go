package security

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
)

func TestVaultAuthenticatesPurposeAndTampering(t *testing.T) {
	v, e := NewVault(strings.Repeat("01", 32))
	if e != nil {
		t.Fatal(e)
	}
	sealed := v.Seal("order:a", []byte("private credential"))
	if strings.Contains(sealed, "private") {
		t.Fatal("plaintext leaked")
	}
	if _, e = v.Open("order:b", sealed); e == nil {
		t.Fatal("cross-record decrypt succeeded")
	}
	got, e := v.Open("order:a", sealed)
	if e != nil || string(got) != "private credential" {
		t.Fatal("roundtrip failed")
	}
	bytes, _ := base64.RawURLEncoding.DecodeString(sealed)
	bytes[len(bytes)-1] ^= 1
	if _, e = v.Open("order:a", base64.RawURLEncoding.EncodeToString(bytes)); e == nil {
		t.Fatal("tampered ciphertext accepted")
	}
}
func TestRequestDigestBindsMasterKeyAndPurpose(t *testing.T) {
	a, err := NewVault(strings.Repeat("01", 32))
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewVault(strings.Repeat("02", 32))
	if err != nil {
		t.Fatal(err)
	}
	value := `{"password":"synthetic"}`
	digest := a.Digest("order-request", value)
	if digest != a.Digest("order-request", value) || digest == Hash(value) || digest == b.Digest("order-request", value) || digest == a.Digest("other", value) {
		t.Fatal("request digest is not stable and secret-bound")
	}
}
func TestCanonicalQueryAndSignatureBinding(t *testing.T) {
	q := url.Values{"z": {"中文"}, "a": {"!", "a b", "~"}}
	if got := CanonicalQuery(q); got != "a=%21&a=a%20b&a=~&z=%E4%B8%AD%E6%96%87" {
		t.Fatalf("canonical query: %s", got)
	}
	if got := CanonicalQuery(url.Values{"a-": {"2"}, "a": {"1"}}); got != "a=1&a-=2" {
		t.Fatalf("prefix key sorting: %s", got)
	}
	one := Signature("secret", "POST", "/v1/orders", q, "1", "nonce", "key", "idem", []byte(`{"a":1}`))
	two := Signature("secret", "POST", "/v1/orders", q, "1", "nonce", "key", "changed", []byte(`{"a":1}`))
	if one == two {
		t.Fatal("idempotency not bound")
	}
	three := Signature("secret", "POST", "/v1/orders", q, "1", "nonce", "key", "idem", []byte(`{ "a":1}`))
	if one == three {
		t.Fatal("raw body not bound")
	}
}
