// Package knowledgebase is rag's one Amazon Bedrock knowledge base: a fully
// managed, serverless index that parses (including PDF and Office files),
// chunks, embeds and indexes a document on rag's behalf, for every corpus on
// the bedrock-kb backend.
//
// One knowledge base, with one data source of its own, serves the whole
// deployment — OpenTofu creates both and hands rag only their ids — so what
// tells two corpora's documents apart inside it is metadata written onto
// every document (organization_id, corpus_id), not separate infrastructure.
// A document's own identity there is rag's own document id, so ingesting the
// same id again replaces it rather than duplicating it, and deleting it needs
// nothing but that id.
//
// Nothing here depends on which kind of knowledge base it is. A VECTOR one
// with a CUSTOM data source and a MANAGED one with its custom connector take,
// report on and delete documents through the same three calls; only
// searching them differs, and that is rag/internal/retrieval's.
//
// This package only submits, polls and deletes whole documents. It never
// searches: retrieving from a knowledge base is a different shape of call
// (bedrockagentruntime.Retrieve, not this package's bedrockagent client) and
// lives in rag/internal/retrieval, next to the dense and lexical retrievers
// it stands in for.
package knowledgebase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagent"
	agenttypes "github.com/aws/aws-sdk-go-v2/service/bedrockagent/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// IngestAPI is the part of Bedrock Agent this package uses to manage a
// knowledge base's documents directly, bypassing a crawl of its data source.
type IngestAPI interface {
	IngestKnowledgeBaseDocuments(context.Context, *bedrockagent.IngestKnowledgeBaseDocumentsInput, ...func(*bedrockagent.Options)) (*bedrockagent.IngestKnowledgeBaseDocumentsOutput, error)
	GetKnowledgeBaseDocuments(context.Context, *bedrockagent.GetKnowledgeBaseDocumentsInput, ...func(*bedrockagent.Options)) (*bedrockagent.GetKnowledgeBaseDocumentsOutput, error)
	DeleteKnowledgeBaseDocuments(context.Context, *bedrockagent.DeleteKnowledgeBaseDocumentsInput, ...func(*bedrockagent.Options)) (*bedrockagent.DeleteKnowledgeBaseDocumentsOutput, error)
}

// Objects is the part of Amazon S3 this package uses to stage text too large
// to send inline, and to remove it again once the knowledge base has it or
// the document goes.
type Objects interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

// inlineLimit is the most document text this package will send in line with
// the ingest request rather than staging to S3 first.
//
// Bedrock bounds one IngestKnowledgeBaseDocuments call, across up to 25
// documents, well above this; the tighter bound here is a judgment call
// rather than a measured ceiling — nothing in this change was run against a
// real knowledge base with a document near it — chosen so a request stays
// comfortably small and staging is the path exercised for anything that
// might otherwise be marginal.
const inlineLimit = 512 << 10

// Config is what this package needs to reach one knowledge base.
type Config struct {
	// KnowledgeBaseID and DataSourceID name what OpenTofu created: one
	// knowledge base per deployment, VECTOR or MANAGED, and its one data
	// source that takes documents directly (CUSTOM, or a managed knowledge
	// base's custom connector). Rag never creates or configures either.
	KnowledgeBaseID string
	DataSourceID    string
	// Bucket is where text too large for inlineLimit is staged before it is
	// ingested. Empty means staging is unavailable, so a document that needs
	// it is refused by name rather than failing at the ingest call.
	Bucket string
}

func (config Config) validate() error {
	if strings.TrimSpace(config.KnowledgeBaseID) == "" {
		return errors.New("a knowledge base id is required")
	}
	if strings.TrimSpace(config.DataSourceID) == "" {
		return errors.New("a knowledge base data source id is required")
	}
	return nil
}

// Client ingests, polls and deletes documents in one Bedrock knowledge base.
type Client struct {
	ingest  IngestAPI
	objects Objects
	config  Config
}

// New builds a Client. objects may be nil when no staging bucket is
// configured, in which case Submit refuses a document too large to send
// inline rather than trying to stage it.
func New(ingest IngestAPI, objects Objects, config Config) (*Client, error) {
	if ingest == nil {
		return nil, errors.New("a Bedrock Agent client is required")
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	return &Client{ingest: ingest, objects: objects, config: config}, nil
}

// StagingKey is where a document's text is staged when it is too large to
// send inline. It is content addressed and deterministic from facts every
// caller already has, so nothing needs to remember it was written in order
// to find it again — to check it, or to delete it once the document is
// indexed or gone.
func StagingKey(organizationID, corpusID, documentID, contentSHA256 string) string {
	return fmt.Sprintf("rag-staging/%s/%s/%s/%s.txt", organizationID, corpusID, documentID, contentSHA256)
}

// Submit hands one document's content to the knowledge base, replacing
// whatever was indexed under this document id before.
//
// text is the document's body, as rag itself would chunk and embed it for
// any other backend; it is empty exactly when document.HasOriginal() is
// true, meaning the document has no extracted text at all and its own bytes
// — a PDF, an Office file — are what the knowledge base must parse instead.
//
// What it writes is what SyncMarker describes, so a caller that records
// SyncMarker once Submit returns has recorded exactly this submission.
func (client *Client) Submit(ctx context.Context, corpus domain.Corpus, document domain.Document, text string) error {
	if document.Access != "" && !document.Access.Valid() {
		return fmt.Errorf("document access is not allowed: %w", domain.ErrInvalid)
	}
	content, err := client.documentContent(ctx, corpus, document, text)
	if err != nil {
		return err
	}
	pairs := documentAttributes(corpus, document)
	attributes := make([]agenttypes.MetadataAttribute, 0, len(pairs))
	for _, pair := range pairs {
		attributes = append(attributes, stringAttribute(pair.key, pair.value))
	}
	_, err = client.ingest.IngestKnowledgeBaseDocuments(ctx, &bedrockagent.IngestKnowledgeBaseDocumentsInput{
		KnowledgeBaseId: aws.String(client.config.KnowledgeBaseID),
		DataSourceId:    aws.String(client.config.DataSourceID),
		Documents: []agenttypes.KnowledgeBaseDocument{{
			Content: content,
			Metadata: &agenttypes.DocumentMetadata{
				Type:             agenttypes.MetadataSourceTypeInLineAttribute,
				InlineAttributes: attributes,
			},
		}},
	})
	if err != nil {
		return fmt.Errorf("ingest document %s into the knowledge base: %w", document.ID, err)
	}
	return nil
}

// submittedAttribute is one metadata attribute Submit writes onto a document
// in the knowledge base.
type submittedAttribute struct {
	key, value string
}

// documentAttributes is every attribute Submit writes for a document, in the
// order it writes them. A document with no access recorded is written as
// member-visible, the same default rag applies everywhere else.
func documentAttributes(corpus domain.Corpus, document domain.Document) []submittedAttribute {
	access := document.Access
	if access == "" {
		access = domain.AccessMembers
	}
	attributes := []submittedAttribute{
		{"organization_id", corpus.OrganizationID},
		{"corpus_id", corpus.ID},
		{"document_id", document.ID},
		{"access", string(access)},
		{"source_kind", string(document.Source.Kind)},
	}
	if document.Title != "" {
		attributes = append(attributes, submittedAttribute{"title", document.Title})
	}
	if document.Source.Ref != "" {
		attributes = append(attributes, submittedAttribute{"source_ref", document.Source.Ref})
	}
	// These are stable, bounded classification fields supplied by Knowledge.
	// Provider-specific details such as Gmail addresses and labels stay in
	// Knowledge; they are not copied into the shared Bedrock index.
	for _, key := range []string{"provider", "knowledge_source_id", "document_kind", "category"} {
		if value := document.Metadata[key]; value != "" {
			attributes = append(attributes, submittedAttribute{key, value})
		}
	}
	return attributes
}

// SyncMarkerPrefix begins every SyncMarker this build writes. It changes
// whenever what Submit writes does — a new attribute, a different default —
// so that no document submitted under the old attribute set can look synced
// to this one. A marker without it, such as the bare content hash rag
// recorded before markers existed, never matches.
const SyncMarkerPrefix = "v2:"

// SyncMarker is what a document's kb_synced_hash records once Submit has
// handed it over: a digest of its content hash and every attribute Submit
// wrote with it, access and provenance included.
//
// Comparing it with the marker for the document as it is now is how the
// ingest worker knows whether the knowledge base holds this document as it
// is: a change to the content, or to any attribute a search filters on,
// gives a different marker and the document is submitted again, while an
// unchanged one is only polled.
func SyncMarker(corpus domain.Corpus, document domain.Document) string {
	attributes := documentAttributes(corpus, document)
	pairs := make([][2]string, 0, len(attributes))
	for _, attribute := range attributes {
		pairs = append(pairs, [2]string{attribute.key, attribute.value})
	}
	slices.SortFunc(pairs, func(a, b [2]string) int { return strings.Compare(a[0], b[0]) })
	// JSON keeps the encoding unambiguous whatever a title or a reference
	// contains.
	encoded, err := json.Marshal(struct {
		Content    string      `json:"content"`
		Attributes [][2]string `json:"attributes"`
	}{document.ContentSHA256, pairs})
	if err != nil {
		// Strings and slices of them always encode; this cannot happen.
		panic(fmt.Sprintf("encode a knowledge base sync marker: %v", err))
	}
	sum := sha256.Sum256(encoded)
	return SyncMarkerPrefix + hex.EncodeToString(sum[:])
}

// documentContent decides how a document's bytes reach the knowledge base:
// pointed at its own original object when it has one, sent in line when its
// text is small enough, or staged to S3 first when it is not.
func (client *Client) documentContent(ctx context.Context, corpus domain.Corpus, document domain.Document, text string) (*agenttypes.DocumentContent, error) {
	identifier := &agenttypes.CustomDocumentIdentifier{Id: aws.String(document.ID)}
	if document.HasOriginal() {
		return &agenttypes.DocumentContent{
			DataSourceType: agenttypes.ContentDataSourceTypeCustom,
			Custom: &agenttypes.CustomContent{
				CustomDocumentIdentifier: identifier,
				SourceType:               agenttypes.CustomSourceTypeS3Location,
				S3Location:               &agenttypes.CustomS3Location{Uri: aws.String("s3://" + document.OriginalBucket + "/" + document.OriginalKey)},
			},
		}, nil
	}
	if len(text) <= inlineLimit {
		return &agenttypes.DocumentContent{
			DataSourceType: agenttypes.ContentDataSourceTypeCustom,
			Custom: &agenttypes.CustomContent{
				CustomDocumentIdentifier: identifier,
				SourceType:               agenttypes.CustomSourceTypeInLine,
				InlineContent: &agenttypes.InlineContent{
					Type:        agenttypes.InlineContentTypeText,
					TextContent: &agenttypes.TextContentDoc{Data: aws.String(text)},
				},
			},
		}, nil
	}
	uri, err := client.stage(ctx, corpus, document, text)
	if err != nil {
		return nil, err
	}
	return &agenttypes.DocumentContent{
		DataSourceType: agenttypes.ContentDataSourceTypeCustom,
		Custom: &agenttypes.CustomContent{
			CustomDocumentIdentifier: identifier,
			SourceType:               agenttypes.CustomSourceTypeS3Location,
			S3Location:               &agenttypes.CustomS3Location{Uri: aws.String(uri)},
		},
	}, nil
}

// stage writes text too large for inlineLimit to rag's own staging bucket and
// returns its s3:// URI.
func (client *Client) stage(ctx context.Context, corpus domain.Corpus, document domain.Document, text string) (string, error) {
	if client.objects == nil || strings.TrimSpace(client.config.Bucket) == "" {
		return "", fmt.Errorf("document %s is %d bytes, over the %d byte inline limit, and no staging bucket is configured: %w",
			document.ID, len(text), inlineLimit, domain.ErrInvalid)
	}
	key := StagingKey(corpus.OrganizationID, corpus.ID, document.ID, document.ContentSHA256)
	if _, err := client.objects.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(client.config.Bucket),
		Key:         aws.String(key),
		Body:        strings.NewReader(text),
		ContentType: aws.String("text/plain; charset=utf-8"),
	}); err != nil {
		return "", fmt.Errorf("stage document %s for the knowledge base: %w", document.ID, err)
	}
	return "s3://" + client.config.Bucket + "/" + key, nil
}

// DeleteStaged removes a document's staged text, once the knowledge base has
// indexed it or the document itself is gone. Deleting an object that was
// never staged — the ordinary case, since most documents fit inline — costs
// one call and is not an error.
func (client *Client) DeleteStaged(ctx context.Context, corpus domain.Corpus, document domain.Document) error {
	if client.objects == nil || strings.TrimSpace(client.config.Bucket) == "" {
		return nil
	}
	key := StagingKey(corpus.OrganizationID, corpus.ID, document.ID, document.ContentSHA256)
	if _, err := client.objects.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(client.config.Bucket), Key: aws.String(key),
	}); err != nil {
		return fmt.Errorf("delete staged text for document %s: %w", document.ID, err)
	}
	return nil
}

// DocumentStatus is one document's state in the knowledge base, in Bedrock's
// own words: what the ingest pipeline reports and reasons about is this, not
// an approximation of it.
type DocumentStatus struct {
	// Code is one of Bedrock's own status words: STARTING, PENDING,
	// IN_PROGRESS, INDEXED, PARTIALLY_INDEXED, METADATA_PARTIALLY_INDEXED,
	// METADATA_UPDATE_FAILED, FAILED, NOT_FOUND, IGNORED, DELETING or
	// DELETE_IN_PROGRESS, or TEXT_INDEXED, which a managed knowledge base
	// reports on the way to INDEXED and Bedrock does not document.
	Code string
	// Reason is set alongside IGNORED, and sometimes alongside FAILED.
	Reason string
}

// Statuses reports what the knowledge base currently holds for the given
// document ids (rag's own document ids, which are also the custom document
// ids they were ingested under). An id Bedrock does not mention in its
// answer is reported back as NOT_FOUND, which is what a document never
// submitted, or already deleted, looks like.
func (client *Client) Statuses(ctx context.Context, documentIDs []string) (map[string]DocumentStatus, error) {
	statuses := make(map[string]DocumentStatus, len(documentIDs))
	for _, id := range documentIDs {
		statuses[id] = DocumentStatus{Code: string(agenttypes.DocumentStatusNotFound)}
	}
	if len(documentIDs) == 0 {
		return statuses, nil
	}
	output, err := client.ingest.GetKnowledgeBaseDocuments(ctx, &bedrockagent.GetKnowledgeBaseDocumentsInput{
		KnowledgeBaseId:     aws.String(client.config.KnowledgeBaseID),
		DataSourceId:        aws.String(client.config.DataSourceID),
		DocumentIdentifiers: identifiers(documentIDs),
	})
	if err != nil {
		return nil, fmt.Errorf("read document status from the knowledge base: %w", err)
	}
	for _, detail := range output.DocumentDetails {
		id := customID(detail.Identifier)
		if id == "" {
			continue
		}
		statuses[id] = DocumentStatus{Code: string(detail.Status), Reason: aws.ToString(detail.StatusReason)}
	}
	return statuses, nil
}

// deleteBatch is the most document identifiers one Ingest or Delete call
// takes; Delete is assumed to share Ingest's stated 25-document ceiling,
// since Bedrock's own documentation for it does not state one separately.
const deleteBatch = 25

// Delete removes documents from the knowledge base by id. Deleting an id the
// knowledge base never held, or has already deleted, is not an error: it is
// exactly the outcome this wanted.
func (client *Client) Delete(ctx context.Context, documentIDs []string) error {
	for start := 0; start < len(documentIDs); start += deleteBatch {
		batch := documentIDs[start:min(start+deleteBatch, len(documentIDs))]
		if _, err := client.ingest.DeleteKnowledgeBaseDocuments(ctx, &bedrockagent.DeleteKnowledgeBaseDocumentsInput{
			KnowledgeBaseId:     aws.String(client.config.KnowledgeBaseID),
			DataSourceId:        aws.String(client.config.DataSourceID),
			DocumentIdentifiers: identifiers(batch),
		}); err != nil {
			return fmt.Errorf("delete %d documents from the knowledge base: %w", len(batch), err)
		}
	}
	return nil
}

func identifiers(documentIDs []string) []agenttypes.DocumentIdentifier {
	result := make([]agenttypes.DocumentIdentifier, 0, len(documentIDs))
	for _, id := range documentIDs {
		result = append(result, agenttypes.DocumentIdentifier{
			DataSourceType: agenttypes.ContentDataSourceTypeCustom,
			Custom:         &agenttypes.CustomDocumentIdentifier{Id: aws.String(id)},
		})
	}
	return result
}

// customID reads the custom document id back out of an identifier Bedrock
// handed back, which is rag's own document id (see Submit).
func customID(identifier *agenttypes.DocumentIdentifier) string {
	if identifier == nil || identifier.Custom == nil {
		return ""
	}
	return aws.ToString(identifier.Custom.Id)
}

func stringAttribute(key, value string) agenttypes.MetadataAttribute {
	return agenttypes.MetadataAttribute{
		Key:   aws.String(key),
		Value: &agenttypes.MetadataAttributeValue{Type: agenttypes.MetadataValueTypeString, StringValue: aws.String(value)},
	}
}
