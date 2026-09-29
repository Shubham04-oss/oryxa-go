package runtime

import "testing"

func TestValidateAPIURLRejectsUnsafeOrigins(t *testing.T) {
	for _, value := range []string{"http://remote.example", "https://u:p@api.example", "https://api.example/path", "https://api.example?token=x", "https://api.example/#fragment", "https://api.example/%2f"} {
		if _, err := ValidateAPIURL(value); err == nil {
			t.Fatalf("unsafe API accepted: %s", value)
		}
	}
	for _, value := range []string{"https://api.example", "http://127.0.0.1:8080", "http://[::1]:8080"} {
		if _, err := ValidateAPIURL(value); err != nil {
			t.Fatalf("valid API rejected: %s: %v", value, err)
		}
	}
}
