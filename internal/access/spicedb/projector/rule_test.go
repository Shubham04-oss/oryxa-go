package projector

import (
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"testing"
)

// TestRuleMatchesSubscriptions keeps the EventBridge rule in
// deploy/production/spicedb.tf in step with Subscriptions: an event the
// projector maps but the rule leaves out is only reconciled at night.
func TestRuleMatchesSubscriptions(t *testing.T) {
	tf, err := os.ReadFile("../../../../deploy/production/spicedb.tf")
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)spicedb_subscriptions = \{\n(.*?)\n  \}`).FindSubmatch(tf)
	if block == nil {
		t.Fatal("local.spicedb_subscriptions not found in spicedb.tf")
	}
	rule := map[string][]string{}
	for _, line := range regexp.MustCompile(`(?m)^\s*("[^"]+")\s*=\s*(\[.*\])\s*$`).FindAllSubmatch(block[1], -1) {
		var source string
		var types []string
		if err := json.Unmarshal(line[1], &source); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(line[2], &types); err != nil {
			t.Fatalf("%s: %v", source, err)
		}
		rule[source] = types
	}
	if want := Subscriptions(); !reflect.DeepEqual(rule, want) {
		t.Fatalf("spicedb.tf's spicedb_subscriptions drifted from projector.Subscriptions():\n got %v\nwant %v", rule, want)
	}
}
