package serviceconfig

import (
	"testing"
	"time"
)

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestFromEnvDefaults(t *testing.T) {
	config, err := FromEnv(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if config.BatchSize != 5 || config.MaxWait != time.Hour || config.LargestBatch != 20 ||
		config.SweepInterval != time.Second || config.ClaimLease != time.Minute || config.GroupsPerSweep != 8 ||
		config.TypeSafeBaseURL != "https://api.typesafe.ai" || config.JevModel != "jev-latest" ||
		config.JevTimeout != 3*time.Second || config.JevImmediate != .85 || config.JevMinConfidence != .60 {
		t.Fatalf("config = %+v", config)
	}
}

func TestFromEnvOverrides(t *testing.T) {
	config, err := FromEnv(env(map[string]string{
		"BATCHING_EVENTS_QUEUE_URL": " queue ",
		"BATCHING_BATCH_SIZE":       "3", "BATCHING_MAX_WAIT": "30s", "BATCHING_LARGEST_BATCH": "12",
		"BATCHING_SWEEP_INTERVAL": "2s", "BATCHING_CLAIM_LEASE": "45s", "BATCHING_GROUPS_PER_SWEEP": "4",
		"AWS_ENDPOINT_URL": "http://localhost:4566/",
		"TYPESAFE_API_KEY": " secret ", "TYPESAFE_BASE_URL": "http://typesafe.test/",
		"BATCHING_JEV_MODEL": "jev-test", "BATCHING_JEV_TIMEOUT": "900ms",
		"BATCHING_JEV_IMMEDIATE_PROBABILITY": ".9", "BATCHING_JEV_MIN_CONFIDENCE": ".7",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if config.EventsQueueURL != "queue" || config.BatchSize != 3 || config.MaxWait != 30*time.Second ||
		config.LargestBatch != 12 || config.EventBridgeEndpoint != "http://localhost:4566" || config.SQSEndpoint != "http://localhost:4566" ||
		config.TypeSafeAPIKey != "secret" || config.TypeSafeBaseURL != "http://typesafe.test" || config.JevModel != "jev-test" ||
		config.JevTimeout != 900*time.Millisecond || config.JevImmediate != .9 || config.JevMinConfidence != .7 {
		t.Fatalf("config = %+v", config)
	}
}

func TestFromEnvRejectsInvalidPolicy(t *testing.T) {
	for name, values := range map[string]map[string]string{
		"zero size":       {"BATCHING_BATCH_SIZE": "0"},
		"largest smaller": {"BATCHING_BATCH_SIZE": "5", "BATCHING_LARGEST_BATCH": "4"},
		"default smaller": {"BATCHING_BATCH_SIZE": "30"},
		"bad duration":    {"BATCHING_MAX_WAIT": "later"},
		"wait too long":   {"BATCHING_MAX_WAIT": "91m"},
		"short lease":     {"BATCHING_SWEEP_INTERVAL": "2s", "BATCHING_CLAIM_LEASE": "2s"},
		"one group":       {"BATCHING_GROUPS_PER_SWEEP": "1"},
		"relative url":    {"EVENTBRIDGE_ENDPOINT": "localhost:4566"},
		"typesafe url":    {"TYPESAFE_BASE_URL": "typesafe.test"},
		"bad probability": {"BATCHING_JEV_IMMEDIATE_PROBABILITY": "1.1"},
		"slow classifier": {"BATCHING_JEV_TIMEOUT": "11s"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := FromEnv(env(values)); err == nil {
				t.Fatal("FromEnv() error = nil")
			}
		})
	}
}

func TestTheClassifierMayTakeUpToTenSeconds(t *testing.T) {
	config, err := FromEnv(env(map[string]string{"BATCHING_JEV_TIMEOUT": "10s"}))
	if err != nil || config.JevTimeout != MaxJevTimeout {
		t.Fatalf("FromEnv() = %s, %v", config.JevTimeout, err)
	}
}
