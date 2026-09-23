# RAG and Knowledge HTTP contracts

- [`rag/v1/openapi.yaml`](rag/v1/openapi.yaml) describes member-authenticated corpus search and grounded answering, including filters, provenance and citations.
- [`knowledge/v1/openapi.yaml`](knowledge/v1/openapi.yaml) describes the service-authenticated body fetch used when a Knowledge publication event omits large text.

These contracts cover the retrieval pipeline boundary. The versioned bus payloads are Go types in [`contracts/events/knowledge/v1`](../events/knowledge/v1/events.go) and [`contracts/events/rag/v1`](../events/rag/v1/events.go).
