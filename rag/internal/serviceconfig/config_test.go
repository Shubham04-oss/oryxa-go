package serviceconfig

import (
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

// minimal sets only what rag cannot start without, so each test can add the
// one variable it is about.
func minimal(extra map[string]string) map[string]string {
	values := map[string]string{"AUTH_URL": "http://localhost:8080"}
	for name, value := range extra {
		values[name] = value
	}
	return values
}

func TestDefaults(t *testing.T) {
	config, err := FromEnv(env(minimal(nil)))
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if config.DefaultProvider != domain.ProviderBedrock || config.DefaultBackend != domain.BackendPgvector {
		t.Fatalf("defaults = %+v", config)
	}
	if config.DefaultMetric != domain.MetricCosine {
		t.Fatalf("metric = %q", config.DefaultMetric)
	}
	// Answering is off by default, and searching still works. A service that
	// refuses to start without a model would be worse.
	if config.AnswerModel != "" {
		t.Fatalf("answer model = %q, want empty", config.AnswerModel)
	}
	if config.IngestBatch < 1 || config.IngestLease <= 0 || config.IngestInterval <= 0 {
		t.Fatalf("ingest settings = %+v", config)
	}
}

func TestAuthURLIsRequired(t *testing.T) {
	if _, err := FromEnv(env(nil)); err == nil {
		t.Fatal("rag started with no auth")
	}
}

// TestBadDefaultsFailTheDeploy is the point of validating here rather than at
// the first query: a corpus created under a bad default cannot be fixed by
// changing the default afterwards.
func TestBadDefaultsFailTheDeploy(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{{
		name:    "a model the catalog does not know, with no width",
		env:     map[string]string{"RAG_DEFAULT_EMBEDDING_MODEL": "vendor.something-new"},
		wantErr: "dimensions must be given",
	}, {
		name:    "an unknown provider",
		env:     map[string]string{"RAG_DEFAULT_EMBEDDING_PROVIDER": "azure"},
		wantErr: "provider",
	}, {
		name:    "an unknown backend",
		env:     map[string]string{"RAG_DEFAULT_BACKEND": "pinecone"},
		wantErr: "RAG_DEFAULT_BACKEND",
	}, {
		// S3 Vectors cannot index on inner product, so this default would
		// create corpora that accept documents and never answer.
		name: "a metric the default backend cannot index",
		env: map[string]string{
			"RAG_DEFAULT_BACKEND": "s3vectors", "RAG_DEFAULT_METRIC": "inner_product",
			"RAG_VECTOR_BUCKET_NAME": "oryxa-vectors",
		},
		wantErr: "S3 Vectors",
	}, {
		name:    "s3vectors with no bucket",
		env:     map[string]string{"RAG_DEFAULT_BACKEND": "s3vectors"},
		wantErr: "RAG_VECTOR_BUCKET_NAME",
	}, {
		name: "vertex with no project",
		env: map[string]string{
			"RAG_DEFAULT_EMBEDDING_PROVIDER": "vertex",
			"RAG_DEFAULT_EMBEDDING_MODEL":    "text-embedding-005",
		},
		wantErr: "GOOGLE_CLOUD_PROJECT",
	}, {
		name:    "a guardrail with no version",
		env:     map[string]string{"RAG_GUARDRAIL_ID": "gr-abc"},
		wantErr: "RAG_GUARDRAIL_VERSION",
	}, {
		// DRAFT means what runs is not what was reviewed.
		name:    "a DRAFT guardrail version",
		env:     map[string]string{"RAG_GUARDRAIL_ID": "gr-abc", "RAG_GUARDRAIL_VERSION": "DRAFT"},
		wantErr: "DRAFT",
	}, {
		name:    "a reranker that is not an ARN",
		env:     map[string]string{"RAG_RERANK_MODEL_ARN": "cohere.rerank-v3-5:0"},
		wantErr: "ARN",
	}, {
		name: "two rerankers",
		env: map[string]string{
			"RAG_RERANK_MODEL_ARN":    "arn:aws:bedrock:ap-south-1::foundation-model/cohere.rerank-v3-5:0",
			"RAG_RERANK_VERTEX_MODEL": "semantic-ranker-default@latest",
			"GOOGLE_CLOUD_PROJECT":    "p",
		},
		wantErr: "not both",
	}, {
		name:    "a Vertex reranker with no project",
		env:     map[string]string{"RAG_RERANK_VERTEX_MODEL": "semantic-ranker-default@latest"},
		wantErr: "GOOGLE_CLOUD_PROJECT",
	}, {
		name:    "an auth URL that is not absolute",
		env:     map[string]string{"AUTH_URL": "localhost:8080"},
		wantErr: "AUTH_URL",
	}, {
		name:    "a files URL that is not absolute",
		env:     map[string]string{"FILES_URL": "files"},
		wantErr: "FILES_URL",
	}, {
		name:    "an answering provider that does not exist",
		env:     map[string]string{"RAG_ANSWER_PROVIDER": "openai"},
		wantErr: "RAG_ANSWER_PROVIDER",
	}, {
		name:    "a concurrency outside the allowed range",
		env:     map[string]string{"RAG_EMBED_CONCURRENCY": "0"},
		wantErr: "RAG_EMBED_CONCURRENCY",
	}}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := FromEnv(env(minimal(testCase.env)))
			if err == nil {
				t.Fatalf("FromEnv accepted %v", testCase.env)
			}
			if !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("error = %v, want it to mention %q", err, testCase.wantErr)
			}
		})
	}
}

func TestAWorkableVertexConfiguration(t *testing.T) {
	config, err := FromEnv(env(minimal(map[string]string{
		"RAG_DEFAULT_EMBEDDING_PROVIDER": "vertex",
		"RAG_DEFAULT_EMBEDDING_MODEL":    "gemini-embedding-001",
		"GOOGLE_CLOUD_PROJECT":           "oryxa-dev",
		"GOOGLE_CLOUD_LOCATION":          "asia-south1",
		"RAG_ANSWER_PROVIDER":            "vertex",
		"RAG_ANSWER_MODEL":               "gemini-3.8-flash",
	})))
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if config.DefaultProvider != domain.ProviderVertex || config.GoogleCloudLocation != "asia-south1" {
		t.Fatalf("config = %+v", config)
	}
	if config.AnswerModel != "gemini-3.8-flash" {
		t.Fatalf("answer model = %q", config.AnswerModel)
	}
}

func TestAWorkableS3VectorsConfiguration(t *testing.T) {
	config, err := FromEnv(env(minimal(map[string]string{
		"RAG_DEFAULT_BACKEND":    "s3vectors",
		"RAG_VECTOR_BUCKET_NAME": "oryxa-vectors",
	})))
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if config.DefaultBackend != domain.BackendS3Vectors || config.VectorBucket != "oryxa-vectors" {
		t.Fatalf("config = %+v", config)
	}
}

func TestTrailingSlashesAreTrimmedFromURLs(t *testing.T) {
	config, err := FromEnv(env(minimal(map[string]string{
		"AUTH_URL":  "http://localhost:8080/",
		"TEAMS_URL": "http://localhost:8083/",
		"FILES_URL": "http://localhost:8085/",
	})))
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	for name, value := range map[string]string{"auth": config.AuthURL, "teams": config.TeamsURL, "files": config.FilesURL} {
		if strings.HasSuffix(value, "/") {
			t.Fatalf("the %s URL kept its trailing slash: %q", name, value)
		}
	}
}
