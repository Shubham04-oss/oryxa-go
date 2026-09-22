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
// RAG_EVAL_RERANK_MODEL adds a pass reranked by that Vertex ranking model,
// with RAG_EVAL_MIN_SCORE as its relevance floor,
// and RAG_EVAL_OUT names a file for the results as JSON.
//
// The AWS stack a deployment in us-west-2 runs is measured the same way:
// RAG_EVAL_EMBEDDING=bedrock embeds with Titan v2, RAG_EVAL_BACKEND=s3vectors
// with RAG_EVAL_VECTOR_BUCKET keeps the vectors in S3 Vectors, and
// RAG_EVAL_RERANK_ARN reranks with a Bedrock model; AWS_REGION says where. The corpus is indexed
// into an organization of its own, which is deleted when the run ends.
//
// A Bedrock knowledge base is measured the same way, through the clients the
// service uses: RAG_EVAL_BACKEND=bedrock-kb with RAG_EVAL_KNOWLEDGE_BASE_ID,
// RAG_EVAL_KNOWLEDGE_BASE_DATA_SOURCE_ID and RAG_EVAL_KNOWLEDGE_BASE_TYPE
// (VECTOR, the default, or MANAGED). The run's documents are deleted from it
// when the run ends, and searching the corpus as another organization must
// find nothing. Its passages carry no byte range, so each is placed in its
// document by its text before it can count as a section hit.
package eval
