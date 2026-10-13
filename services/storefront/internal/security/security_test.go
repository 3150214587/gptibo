package security

import (
	"testing"
	"time"
)

func TestTOTPReferenceAndWindow(t *testing.T) {
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	if got := TOTP(secret, 1); got != "287082" {
		t.Fatalf("RFC HOTP vector mismatch: %s", got)
	}
	if VerifyTOTP(secret, "287082", time.Unix(59, 0)) != 1 {
		t.Fatal("valid TOTP rejected")
	}
	if VerifyTOTP(secret, "287082", time.Unix(120, 0)) != -1 {
		t.Fatal("expired TOTP accepted")
	}
	if VerifyTOTP(secret, "", time.Now()) != -1 || ValidTOTP("invalid") {
		t.Fatal("invalid TOTP input accepted")
	}
}
