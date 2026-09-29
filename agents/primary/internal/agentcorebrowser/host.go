package agentcorebrowser

// This file decides which hosts the browser may be sent to.

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/idna"

	"github.com/Shubham04-oss/oryxa-go/internal/safehttp"
)

// resolveTimeout bounds looking a host up, so a name server that never
// answers does not hold a tool call.
const resolveTimeout = 10 * time.Second

// checkHost refuses a URL's host that is not on the public internet: an IP
// address that is not, and a name that resolves to any address that is not,
// since the browser may connect to whichever the answer lists. The rule for
// an address is safehttp.IsPublic, the one every connection to a host nobody
// here chose answers to. A name that cannot be looked up, or that answers no
// address, is refused too: there is nothing to check it against.
//
// The host is judged as the browser will read it, not as it was typed. A name
// with anything but ASCII in it is mapped the way a browser maps it, which
// turns a fullwidth "１２７.０.０.１" into 127.0.0.1, and the dot that ends a
// fully qualified name is dropped. A host that is not a plain IP address but
// ends in a number ("2130706433", "0x7f.1", "0177.0.0.1") is refused
// outright: a browser reads it as an IPv4 address in decimal, octal or hex,
// and no public name looks like it.
//
// It is a check made ahead of the navigation, and no more. The browser runs
// in AWS and looks the name up itself, so an answer that changes in between,
// and a redirect to another host, are beyond it.
func checkHost(ctx context.Context, resolver safehttp.Resolver, hostname string) error {
	host, err := browserHost(hostname)
	if err != nil {
		return errors.New("browser URLs must name a valid host")
	}
	switch {
	case host == "":
		return errors.New("browser URLs must name a host")
	case host == "localhost" || strings.HasSuffix(host, ".localhost"):
		return errors.New("browser URLs cannot target localhost")
	}
	if address, err := netip.ParseAddr(host); err == nil {
		if !safehttp.IsPublic(address) {
			return errors.New("browser URLs cannot target a non-public IP address")
		}
		return nil
	}
	if endsInNumber(host) {
		return errors.New("browser URLs cannot use a number for a host, only a host name or a plain IP address")
	}

	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	addresses, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return fmt.Errorf("browser URL host %s could not be resolved", host)
	}
	for _, address := range addresses {
		// The host, never the address: a name that resolves inside the VPC is
		// not for the model to learn about.
		if !safehttp.IsPublic(address) {
			return fmt.Errorf("browser URL host %s resolves to a non-public address", host)
		}
	}
	return nil
}

// browserHost is host in lower case and ASCII, without the dot that ends a
// fully qualified name. A name with anything but ASCII in it goes through the
// mapping a browser applies (UTS 46), as the standard library's own HTTP
// transports do. An ASCII name is left as it is, underscores and all, which
// the mapping would refuse.
func browserHost(host string) (string, error) {
	if isASCII(host) {
		host = strings.ToLower(host)
	} else {
		var err error
		if host, err = idna.Lookup.ToASCII(host); err != nil {
			return "", err
		}
	}
	return strings.TrimSuffix(host, "."), nil
}

func isASCII(text string) bool {
	for index := 0; index < len(text); index++ {
		if text[index] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// endsInNumber reports whether host's last label is a number as a browser
// counts one: decimal digits, or 0x and hex digits. host is lower case.
func endsInNumber(host string) bool {
	last := host[strings.LastIndexByte(host, '.')+1:]
	if digits, ok := strings.CutPrefix(last, "0x"); ok {
		return strings.Trim(digits, "0123456789abcdef") == ""
	}
	return last != "" && strings.Trim(last, "0123456789") == ""
}
