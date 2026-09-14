package serviceauth

import "testing"

func TestParseCallersEmpty(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"", "   ", "\n\t"} {
		callers, err := ParseCallers(raw)
		if err != nil {
			t.Fatalf("ParseCallers(%q) error = %v", raw, err)
		}
		if len(callers) != 0 {
			t.Fatalf("ParseCallers(%q) = %v, want empty", raw, callers)
		}
	}
}

func TestParseCallersValid(t *testing.T) {
	t.Parallel()
	const raw = `[
		{"service":"integrations","key_id":"arn:aws:kms:us-east-1:1:key/a","purposes":["action","trigger","cleanup"]},
		{"service":"files","key_id":"arn:aws:kms:us-east-1:1:key/b","purposes":["read"]}
	]`
	callers, err := ParseCallers(raw)
	if err != nil {
		t.Fatalf("ParseCallers: %v", err)
	}
	if len(callers) != 2 {
		t.Fatalf("len(callers) = %d, want 2", len(callers))
	}
	if callers[0].Service != "integrations" || callers[0].KeyID != "arn:aws:kms:us-east-1:1:key/a" {
		t.Fatalf("callers[0] = %+v", callers[0])
	}
	if len(callers[0].Purposes) != 3 {
		t.Fatalf("callers[0].Purposes = %v", callers[0].Purposes)
	}
	if callers[1].Service != "files" || len(callers[1].Purposes) != 1 {
		t.Fatalf("callers[1] = %+v", callers[1])
	}
}

func TestParseCallersInvalid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
	}{
		{"not json", `not json`},
		{"object instead of array", `{"service":"a","key_id":"k","purposes":["x"]}`},
		{"service name uppercase", `[{"service":"Integrations","key_id":"k","purposes":["action"]}]`},
		{"service name empty", `[{"service":"","key_id":"k","purposes":["action"]}]`},
		{"service name starts with symbol", `[{"service":"-integrations","key_id":"k","purposes":["action"]}]`},
		{"service name too long", `[{"service":"` + longName() + `","key_id":"k","purposes":["action"]}]`},
		{"duplicate service", `[{"service":"a","key_id":"k1","purposes":["x"]},{"service":"a","key_id":"k2","purposes":["x"]}]`},
		{"missing key_id", `[{"service":"a","key_id":"","purposes":["action"]}]`},
		{"no purposes", `[{"service":"a","key_id":"k","purposes":[]}]`},
		{"purpose name invalid", `[{"service":"a","key_id":"k","purposes":["Action!"]}]`},
		{"duplicate purpose", `[{"service":"a","key_id":"k","purposes":["action","action"]}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseCallers(tc.raw); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

// longName returns a 65-character string, one past namePattern's limit.
func longName() string {
	name := make([]byte, 65)
	for i := range name {
		name[i] = 'a'
	}
	return string(name)
}
