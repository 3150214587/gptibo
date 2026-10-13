package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"
)

func ID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func Hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func Token() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func MAC(key []byte, s string) string {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}
func Equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
func Code() string {
	n, e := rand.Int(rand.Reader, big.NewInt(1000000))
	if e != nil {
		panic(e)
	}
	return fmt.Sprintf("%06d", n.Int64())
}

var totpSecretPattern = regexp.MustCompile(`^[A-Z2-7]{32}$`)
var codePattern = regexp.MustCompile(`^[0-9]{6}$`)
var idPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func ValidID(id string) bool { return idPattern.MatchString(id) }

func ValidToken(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func ValidTOTP(secret string) bool { return totpSecretPattern.MatchString(secret) }
func ValidCode(code string) bool   { return codePattern.MatchString(code) }
func TOTP(secret string, step int64) string {
	if !ValidTOTP(secret) || step < 0 {
		return ""
	}
	key, e := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if e != nil {
		return ""
	}
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(step))
	h := hmac.New(sha1.New, key)
	h.Write(b)
	sum := h.Sum(nil)
	offset := sum[19] & 15
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[offset:offset+4])&0x7fffffff)%1000000)
}
func VerifyTOTP(secret, code string, now time.Time) int64 {
	if !ValidCode(code) || !ValidTOTP(secret) {
		return -1
	}
	current := now.Unix() / 30
	match := int64(-1)
	for step := current - 1; step <= current+1; step++ {
		if step >= 0 && Equal(TOTP(secret, step), code) {
			match = step
		}
	}
	return match
}
