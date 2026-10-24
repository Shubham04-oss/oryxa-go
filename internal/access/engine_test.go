package access

import "testing"

func TestEngine(t *testing.T) {
	env := map[string]string{"ACCESS_ENGINE_FILES": "spicedb", "ACCESS_ENGINE_RAG": "cedar"}
	getenv := func(name string) string { return env[name] }
	for service, want := range map[string]string{"files": EngineSpiceDB, "teams": EngineCode} {
		if got, err := Engine(getenv, service); err != nil || got != want {
			t.Errorf("%s: %q, %v; want %q", service, got, err, want)
		}
	}
	if _, err := Engine(getenv, "rag"); err == nil {
		t.Error("an unknown engine was accepted")
	}
}
