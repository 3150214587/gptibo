package delivery

import (
	"net"
	"testing"
)

func TestWebhookRejectsPrivateDestinations(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.169.254", "::1", "fc00::1", "0.0.0.0", "0.1.2.3", "100.64.0.1", "100.127.255.254", "192.0.0.8", "192.0.2.1", "198.18.0.1", "198.51.100.1", "203.0.113.1", "240.0.0.1", "::ffff:127.0.0.1", "::ffff:100.64.0.1", "64:ff9b::7f00:1", "2002:7f00:1::", "2001:db8::1", "2001::1", "3fff::1", "ff02::1"} {
		if PublicIP(net.ParseIP(s)) {
			t.Fatalf("private destination allowed: %s", s)
		}
	}
	for _, address := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111", "2001:4860:4860::8888", "::ffff:1.1.1.1"} {
		if !PublicIP(net.ParseIP(address)) {
			t.Fatal("public destination rejected", address)
		}
	}
}
