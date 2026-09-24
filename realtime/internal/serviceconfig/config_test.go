package serviceconfig

import (
	"slices"
	"strings"
	"testing"
)

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

const secret = "5f2b8e1c9a7d4036b1e8c2f94a6d0e3b7c1f5a9d2e8b4c6f0a3d7e1b9c5f2a84"

func TestFromEnvWithNothingSetIsOff(t *testing.T) {
	config, err := FromEnv(env(map[string]string{}))
	if err != nil {
		t.Fatal(err)
	}
	if config != (Config{}) {
		t.Fatalf("config = %+v", config)
	}
	want := []string{"REALTIME_WORKER_URL", "REALTIME_PURGE_SECRET", "REALTIME_EVENTS_QUEUE_URL"}
	if got := config.Missing(); !slices.Equal(got, want) {
		t.Fatalf("Missing() = %v, want %v", got, want)
	}
}

func TestFromEnvReadsAndTidiesEverySetting(t *testing.T) {
	config, err := FromEnv(env(map[string]string{
		"REALTIME_WORKER_URL":       " https://app.oryxa.in/ ",
		"REALTIME_PURGE_SECRET":     " " + secret + "\n",
		"REALTIME_EVENTS_QUEUE_URL": " http://localhost:4566/000000000000/realtime-events ",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		WorkerURL:      "https://app.oryxa.in",
		PurgeSecret:    secret,
		EventsQueueURL: "http://localhost:4566/000000000000/realtime-events",
	}
	if config != want {
		t.Fatalf("config = %+v, want %+v", config, want)
	}
	if missing := config.Missing(); len(missing) != 0 {
		t.Fatalf("Missing() = %v", missing)
	}
}

func TestMissingNamesOnlyWhatIsUnset(t *testing.T) {
	config, err := FromEnv(env(map[string]string{"REALTIME_WORKER_URL": "http://localhost:3001", "REALTIME_EVENTS_QUEUE_URL": "q"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := config.Missing(); !slices.Equal(got, []string{"REALTIME_PURGE_SECRET"}) {
		t.Fatalf("Missing() = %v", got)
	}
}

func TestFromEnvRefusesMalformedValues(t *testing.T) {
	tests := map[string]struct {
		values map[string]string
		names  string
	}{
		"relative worker URL": {map[string]string{"REALTIME_WORKER_URL": "app.oryxa.in"}, "REALTIME_WORKER_URL"},
		"another scheme":      {map[string]string{"REALTIME_WORKER_URL": "ws://app.oryxa.in"}, "REALTIME_WORKER_URL"},
		"a query":             {map[string]string{"REALTIME_WORKER_URL": "https://app.oryxa.in/?x=1"}, "REALTIME_WORKER_URL"},
		"a fragment":          {map[string]string{"REALTIME_WORKER_URL": "https://app.oryxa.in/#x"}, "REALTIME_WORKER_URL"},
		"credentials":         {map[string]string{"REALTIME_WORKER_URL": "https://user:pass@app.oryxa.in"}, "REALTIME_WORKER_URL"},
		"a short secret":      {map[string]string{"REALTIME_PURGE_SECRET": "too-short"}, "REALTIME_PURGE_SECRET"},
		"a padded short one":  {map[string]string{"REALTIME_PURGE_SECRET": "  " + secret[:31] + "  "}, "REALTIME_PURGE_SECRET"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := FromEnv(env(test.values)); err == nil || !strings.Contains(err.Error(), test.names) {
				t.Fatalf("FromEnv() error = %v, want one naming %s", err, test.names)
			}
		})
	}
}
