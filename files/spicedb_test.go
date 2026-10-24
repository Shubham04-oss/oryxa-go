package files

import (
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/host"
)

func TestTheEngineSwitch(t *testing.T) {
	platform := func(value string) host.Platform {
		return host.Platform{Getenv: func(name string) string {
			if name == "ACCESS_ENGINE_FILES" {
				return value
			}
			return ""
		}}
	}
	if closer, err := withEngine(platform(""), nil); err != nil || closer != nil {
		t.Fatalf("unset: closer %t, %v; want the code", closer != nil, err)
	}
	if _, err := withEngine(platform("spicedb"), nil); err == nil {
		t.Fatal("spicedb with no SpiceDB configured was accepted")
	}
	if _, err := withEngine(platform("cedar"), nil); err == nil {
		t.Fatal("an unknown engine was accepted")
	}
}
