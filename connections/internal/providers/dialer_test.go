package providers

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

func TestIsPublicAddress(t *testing.T) {
	for _, tc := range []struct {
		ip   string
		want bool
	}{
		{"93.184.216.34", true},                      // public IPv4
		{"2606:2800:220:1:248:1893:25c8:1946", true}, // public IPv6
		{"127.0.0.1", false},                         // loopback
		{"127.53.0.1", false},                        // loopback range
		{"::1", false},                               // loopback IPv6
		{"10.0.0.1", false},                          // private, RFC 1918
		{"172.16.0.1", false},                        // private, RFC 1918
		{"172.31.255.255", false},                    // private, RFC 1918
		{"172.32.0.1", true},                         // just outside the 172.16/12 block
		{"192.168.1.1", false},                       // private, RFC 1918
		{"169.254.169.254", false},                   // link-local (cloud metadata)
		{"fe80::1", false},                           // link-local IPv6
		{"fc00::1", false},                           // unique local, RFC 4193
		{"fd12:3456:789a::1", false},                 // unique local, RFC 4193
		{"100.64.0.1", false},                        // carrier-grade NAT, RFC 6598
		{"100.127.255.255", false},                   // carrier-grade NAT, RFC 6598
		{"100.63.255.255", true},                     // just outside the CGNAT block
		{"0.0.0.0", false},                           // unspecified
		{"::", false},                                // unspecified
		{"224.0.0.1", false},                         // multicast
		{"ff02::1", false},                           // multicast
		{"255.255.255.255", false},                   // limited broadcast
		{"::ffff:127.0.0.1", false},                  // IPv4-mapped loopback
		{"::ffff:10.0.0.1", false},                   // IPv4-mapped private
		{"::ffff:169.254.169.254", false},            // IPv4-mapped link-local
		{"::ffff:100.64.0.1", false},                 // IPv4-mapped CGNAT
		{"::ffff:255.255.255.255", false},            // IPv4-mapped broadcast
		{"::ffff:93.184.216.34", true},               // IPv4-mapped public
		{"0.1.2.3", false},                           // 0.0.0.0/8, dialed as the local host
		{"64:ff9b::a9fe:a9fe", false},                // NAT64 to link-local (cloud metadata)
		{"64:ff9b::a00:1", false},                    // NAT64 to private
		{"64:ff9b::5db8:d822", true},                 // NAT64 to public
		{"2002:a9fe:a9fe::1", false},                 // 6to4 around link-local
		{"2002:c0a8:101::1", false},                  // 6to4 around private
		{"2002:5db8:d822::1", true},                  // 6to4 around public
		{"::a00:1", false},                           // IPv4-compatible private
		{"::7f00:1", false},                          // IPv4-compatible loopback
	} {
		ip := net.ParseIP(tc.ip)
		if ip == nil {
			t.Fatalf("net.ParseIP(%q) failed", tc.ip)
		}
		if got := isPublicAddress(ip); got != tc.want {
			t.Errorf("isPublicAddress(%q) = %v, want %v", tc.ip, got, tc.want)
		}
	}
}

// TestNewHTTPClientRefusesLoopback is the required proof that a call through
// Client{HTTP: NewHTTPClient()} to a server on 127.0.0.1 -- what every
// httptest server listens on -- is refused rather than reaching it.
func TestNewHTTPClientRefusesLoopback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the loopback server was dialed; the address check did not run")
	}))
	defer server.Close()

	client := Client{HTTP: NewHTTPClient()}
	_, err := client.Exchange(context.Background(), OAuth2{TokenURL: server.URL + "/token"}, exchangeRequest("code-value"))
	if !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("Exchange() over loopback error = %v, want a wrapped %v", err, domain.ErrProviderUnavailable)
	}
	if strings.Contains(err.Error(), "code-value") {
		t.Fatalf("Exchange() error leaked the request: %v", err)
	}
}
