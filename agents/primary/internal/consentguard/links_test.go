package consentguard

import "testing"

func TestWhatTakesAReaderSomewhereIsALink(t *testing.T) {
	for name, text := range map[string]string{
		"an https address":                 "See https://docs.example.com/plan for it.",
		"an http address":                  "http://intranet/wiki",
		"in capitals":                      "HTTPS://EXAMPLE.COM",
		"a Slack link":                     "<https://acme.slack.com/archives/C1/p123|the thread>",
		"a Markdown link":                  "[the plan](https://example.org/plan)",
		"a scheme of another kind":         "it is in s3://bucket/key.csv",
		"a deep link":                      "open slack://channel?team=T1",
		"mailto":                           "write to mailto:raya@example.com",
		"tel":                              "call tel:+14155550100",
		"a data address":                   "data:text/html;base64,PGgxPg==",
		"www":                              "at www.example.com",
		"a bare domain":                    "the plan is at example.com now",
		"a subdomain":                      "docs.example.io is where it lives",
		"a country domain with a path":     "notion.so/team/plan",
		"a co domain with a path":          "acme.co/deals/2026",
		"a host of a code forge":           "github.com/acme/repo",
		"defanged with brackets":           "example[.]com",
		"defanged with a scheme":           "hxxps://example[.]com/plan",
		"defanged with words":              "example(dot)com",
		"a zero-width space in the scheme": "ht​tps://example.com",
		"one hidden in the host":           "example.co​m",
	} {
		if !hasLink(text) {
			t.Errorf("%s: no link found in %q", name, text)
		}
	}
}

func TestWhatIsNoAddressIsNoLink(t *testing.T) {
	for name, text := range map[string]string{
		"plain words":             "Deployments run every Tuesday at nine.",
		"a file name":             "The script is deploy.sh and the notes are notes.md.",
		"an archive":              "It is in archive.zip.",
		"a code file":             "See main.go and index.js for the handler.",
		"a version":               "It shipped in v1.2.3, after 3.14 releases.",
		"a sentence run together": "It is done.So we ship.",
		"an email address":        "Ask raya@example.com or maya@acme.io about it.",
		"a decimal and a range":   "About 1.5/2 of it, or 2020-2021.",
		"an abbreviation":         "e.g. the plan, i.e. the schedule, etc.",
		"a slash of two words":    "and/or, TCP/IP, w/o",
		"a mention":               "<@U123ABC> and <#C123|infra> are in it",
		"a path":                  "under internal/consentguard/guard.go",
		"a colon":                 "Note: it is done. Data: none. File: closed.",
		"nothing":                 "",
	} {
		if hasLink(text) {
			t.Errorf("%s: a link found in %q", name, text)
		}
	}
}
