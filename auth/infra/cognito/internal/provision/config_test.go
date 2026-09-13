package provision

import "testing"

func TestValidateRedirectURL(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "https", value: "https://app.example.com/auth/callback"},
		{name: "localhost", value: "http://localhost:3000/auth/callback"},
		{name: "insecure remote", value: "http://app.example.com/auth/callback", wantErr: true},
		{name: "fragment", value: "https://app.example.com/auth/callback#token", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateRedirectURL(test.value)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateRedirectURL() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}
