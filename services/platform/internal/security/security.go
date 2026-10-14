package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/url"
	"sort"
	"strings"
)

func Token() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func ID(prefix string) string { return prefix + "_" + Token()[:32] }
func Hash(s string) string    { b := sha256.Sum256([]byte(s)); return hex.EncodeToString(b[:]) }
func HMAC(key, s string) string {
	h := hmac.New(sha256.New, []byte(key))
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}

type Vault struct {
	aead      cipher.AEAD
	digestKey string
}

func NewVault(key string) (*Vault, error) {
	b, e := hex.DecodeString(key)
	if e != nil || len(b) != 32 {
		return nil, errors.New("MASTER_KEY must contain 64 hex characters")
	}
	c, e := aes.NewCipher(b)
	if e != nil {
		return nil, e
	}
	a, e := cipher.NewGCM(c)
	return &Vault{aead: a, digestKey: HMAC(string(b), "gptibo:request-digest")}, e
}

// A database-only attacker must not be able to guess encrypted mailbox passwords using request hashes.
func (v *Vault) Digest(purpose, value string) string { return HMAC(v.digestKey, purpose+"\x00"+value) }
func (v *Vault) Seal(purpose string, b []byte) string {
	n := make([]byte, v.aead.NonceSize())
	if _, e := rand.Read(n); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(v.aead.Seal(n, n, b, []byte(purpose)))
}
func (v *Vault) Open(purpose, s string) ([]byte, error) {
	b, e := base64.RawURLEncoding.DecodeString(s)
	if e != nil || len(b) < v.aead.NonceSize() {
		return nil, errors.New("invalid ciphertext")
	}
	n := v.aead.NonceSize()
	return v.aead.Open(nil, b[:n], b[n:], []byte(purpose))
}
func encode(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }
func CanonicalQuery(q url.Values) string {
	var pairs [][2]string
	for k, vs := range q {
		for _, v := range vs {
			pairs = append(pairs, [2]string{encode(k), encode(v)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i][0] != pairs[j][0] {
			return pairs[i][0] < pairs[j][0]
		}
		return pairs[i][1] < pairs[j][1]
	})
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = p[0] + "=" + p[1]
	}
	return strings.Join(parts, "&")
}
func Signature(key, method, path string, q url.Values, ts, nonce, keyID, idem string, body []byte) string {
	return HMAC(key, strings.Join([]string{method, path, CanonicalQuery(q), ts, nonce, keyID, idem, Hash(string(body))}, "\n"))
}
