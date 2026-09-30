package consentguard

import (
	"regexp"
	"strings"
)

// A summary has no link, since a link is a way to the source. What counts as
// one is what would take a reader somewhere: an address with a scheme, a "www."
// host, a host under a top-level domain that is no common word or file type,
// and any dotted host followed by a path.

// schemeLinks are the addresses that say where they go: http://…, s3://…,
// slack://…, and the schemes that name a place without slashes.
var schemeLinks = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.\-]{1,30}://[^\s]`),
	regexp.MustCompile(`(?i)\b(?:mailto|tel|sms|callto|javascript|vbscript|data|file|magnet):[^\s]`),
}

// hostLinks are the addresses that are only a host and, perhaps, a path. The
// top-level domains in the third are the ones that sites are on and that no
// file type or English word ends a sentence with: "deploy.sh", "notes.md",
// "archive.zip" and "made.So" are no links, and "docs.example.io" is.
var hostLinks = []*regexp.Regexp{
	// www.example.com
	regexp.MustCompile(`(?i)\bwww\.[a-z0-9-]+\.[a-z0-9.\-]+`),
	// example.com, docs.example.io, sub.example.co.uk
	regexp.MustCompile(`(?i)\b(?:[a-z0-9-]+\.)+(?:com|org|net|edu|gov|mil|io|ai|dev|app|info|biz|xyz|cloud|tech)\b`),
	// Any dotted host with a path: notion.so/…, acme.co/…
	regexp.MustCompile(`(?i)\b(?:[a-z0-9-]+\.)+[a-z]{2,10}/[^\s]`),
}

// emailAddress is an address that is no link, though its host looks like one.
var emailAddress = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)+`)

// defanging is how a link is written so that it will not be followed, which a
// reader undoes to follow it, so it is a link all the same.
var defanging = strings.NewReplacer(
	"[.]", ".", "(.)", ".", "{.}", ".", "[dot]", ".", "(dot)", ".",
	"[://]", "://", "[:]", ":", "hxxp", "http",
)

// hasLink reports whether text has a link. Format characters are taken out
// first, so that a zero-width space in the middle of "https://" hides nothing,
// and so is the way a link is defanged. An email address is no link: what is
// done about one is the answer guard's.
func hasLink(text string) bool {
	visible := strings.Map(func(char rune) rune {
		if isFormat(char) {
			return -1
		}
		return char
	}, text)
	visible = defanging.Replace(visible)
	for _, pattern := range schemeLinks {
		if pattern.MatchString(visible) {
			return true
		}
	}
	visible = emailAddress.ReplaceAllString(visible, " ")
	for _, pattern := range hostLinks {
		if pattern.MatchString(visible) {
			return true
		}
	}
	return false
}
