package catalog

import "testing"

func TestPublicHTTPSURLRejectsPrivateTargets(t *testing.T) {
	t.Parallel()
	if !publicHTTPSURL("https://example.com/file.pdf") {
		t.Fatal("public HTTPS URL was rejected")
	}
	for _, value := range []string{
		"http://example.com/file.pdf",
		"https://127.0.0.1/file.pdf",
		"https://localhost/file.pdf",
		"https://metadata.google.internal/",
		"https://user:pass@example.com/file.pdf",
		"https://169.254.169.254/latest/meta-data",
		"https://10.0.0.1/file.pdf",
		"https://evil.localhost/file.pdf",
		"https://file.internal/file.pdf",
	} {
		if publicHTTPSURL(value) {
			t.Fatalf("private URL was accepted: %s", value)
		}
	}
}

func TestCustomAPIURLAllowsRelativeAndPinnedHosts(t *testing.T) {
	t.Parallel()
	if !customAPIURL("/user", "api.github.com") || !customAPIURL("https://api.github.com/user", "api.github.com") {
		t.Fatal("valid custom API URL was rejected")
	}
	for _, value := range []string{
		"//evil.example/user",
		"/../admin",
		"https://evil.example/user",
		"http://api.github.com/user",
		"https://user:pass@api.github.com/user",
	} {
		if customAPIURL(value, "api.github.com") {
			t.Fatalf("unsafe custom API URL was accepted: %s", value)
		}
	}
}
