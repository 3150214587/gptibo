package orders

import "testing"

func TestOrderEmailDomains(t *testing.T) {
	for domain := range allowedEmailDomains {
		if !AllowedOrderEmail("buyer@" + domain) {
			t.Fatal("allowed domain rejected", domain)
		}
	}
	for _, email := range []string{" User+tag@GMAIL.COM ", "buyer@vip.qq.com"} {
		if !AllowedOrderEmail(email) {
			t.Fatal("normalization failed", email)
		}
	}
	for _, email := range []string{"buyer@example.com", "buyer@example.invalid", "buyer@evilgmail.com", "buyer@gmail.com.attacker.test", "buyer@sub.gmail.com", "buyer@gmail.com.", "buyer@@gmail.com", "@qq.com"} {
		if AllowedOrderEmail(email) {
			t.Fatal("domain bypass", email)
		}
	}
	for _, email := range []string{"first.last+tag@gmail.com", "firstlast@googlemail.com"} {
		if orderEmailBudget(email) != "firstlast@gmail.com" {
			t.Fatal("alias bypass", email)
		}
	}
}
