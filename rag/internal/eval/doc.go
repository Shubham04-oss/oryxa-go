// Package eval measures retrieval quality and what it costs, against a real
// embedding provider and a real PostgreSQL, over a frozen corpus of this
// repository's own documentation and questions whose answers are known.
//
// It is a measurement, not a gate: it runs only when pointed at a database and
// a Google Cloud project, and it reports rather than asserts, so a change to
// chunking, embedding or retrieval can be compared run against run:
//
//	RAG_EVAL_DATABASE_URL=postgres://… GOOGLE_CLOUD_PROJECT=… \
//	  go test ./rag/internal/eval -run TestRetrievalQuality -v -timeout 30m
//
// RAG_EVAL_MAX_TOKENS and RAG_EVAL_OVERLAP_TOKENS override the chunk policy,
// and RAG_EVAL_OUT names a file for the results as JSON. The corpus is indexed
// into an organization of its own, which is deleted when the run ends.
package eval
