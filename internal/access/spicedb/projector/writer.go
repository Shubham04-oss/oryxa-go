package projector

import (
	"context"
	"errors"
	"fmt"
	"io"

	v1 "github.com/authzed/authzed-go/proto/authzed/api/v1"

	"github.com/Shubham04-oss/oryxa-go/internal/access/spicedb/projector/tokens"
)

// batchSize stays under SpiceDB's default 1,000 updates a write.
const batchSize = 500

// Writer applies changes to SpiceDB and keeps each org's latest ZedToken.
type Writer struct {
	client v1.PermissionsServiceClient
	tokens tokens.Store
}

func NewWriter(client v1.PermissionsServiceClient, store tokens.Store) *Writer {
	return &Writer{client: client, tokens: store}
}

func objectRef(objectType, id string) *v1.ObjectReference {
	return &v1.ObjectReference{ObjectType: objectType, ObjectId: id}
}

func (rel Rel) proto() *v1.Relationship {
	return &v1.Relationship{
		Resource: objectRef(rel.ResourceType, rel.ResourceID),
		Relation: rel.Relation,
		Subject:  &v1.SubjectReference{Object: objectRef(rel.SubjectType, rel.SubjectID)},
	}
}

func (filter Filter) proto() *v1.RelationshipFilter {
	result := &v1.RelationshipFilter{ResourceType: filter.ResourceType, OptionalResourceId: filter.ResourceID, OptionalRelation: filter.Relation}
	if filter.SubjectType != "" {
		result.OptionalSubjectFilter = &v1.SubjectFilter{SubjectType: filter.SubjectType, OptionalSubjectId: filter.SubjectID}
	}
	return result
}

// Apply deletes, then writes. A filter that names one relationship goes in
// the same atomic write as the touches; a wider one is deleted on its own
// first. The token returned is the last write's.
func (writer *Writer) Apply(ctx context.Context, change Change) (*v1.ZedToken, error) {
	var token *v1.ZedToken
	var updates []*v1.RelationshipUpdate
	for _, filter := range change.Delete {
		if filter.exact() {
			updates = append(updates, &v1.RelationshipUpdate{Operation: v1.RelationshipUpdate_OPERATION_DELETE, Relationship: Rel(filter).proto()})
			continue
		}
		deleted, err := writer.client.DeleteRelationships(ctx, &v1.DeleteRelationshipsRequest{RelationshipFilter: filter.proto()})
		if err != nil {
			return nil, fmt.Errorf("delete %s: %w", Rel(filter), err)
		}
		token = deleted.GetDeletedAt()
	}
	for _, rel := range change.Touch {
		updates = append(updates, &v1.RelationshipUpdate{Operation: v1.RelationshipUpdate_OPERATION_TOUCH, Relationship: rel.proto()})
	}
	if len(updates) > 0 {
		written, err := writer.client.WriteRelationships(ctx, &v1.WriteRelationshipsRequest{Updates: updates})
		if err != nil {
			return nil, fmt.Errorf("write relationships: %w", err)
		}
		token = written.GetWrittenAt()
	}
	if token != nil && change.Organization != "" {
		if err := writer.tokens.Save(ctx, change.Organization, token); err != nil {
			return token, err
		}
	}
	return token, nil
}

// write sends one operation over many relationships, a batch at a time.
func (writer *Writer) write(ctx context.Context, operation v1.RelationshipUpdate_Operation, rels []Rel) (*v1.ZedToken, error) {
	var token *v1.ZedToken
	for start := 0; start < len(rels); start += batchSize {
		batch := rels[start:min(start+batchSize, len(rels))]
		updates := make([]*v1.RelationshipUpdate, len(batch))
		for index, rel := range batch {
			updates[index] = &v1.RelationshipUpdate{Operation: operation, Relationship: rel.proto()}
		}
		written, err := writer.client.WriteRelationships(ctx, &v1.WriteRelationshipsRequest{Updates: updates})
		if err != nil {
			return token, err
		}
		token = written.GetWrittenAt()
	}
	return token, nil
}

// Read lists every relationship the filter matches, at full consistency so
// the reconcile compares against what SpiceDB holds now.
func (writer *Writer) Read(ctx context.Context, filter Filter) ([]Rel, error) {
	var rels []Rel
	var cursor *v1.Cursor
	for {
		stream, err := writer.client.ReadRelationships(ctx, &v1.ReadRelationshipsRequest{
			Consistency:        &v1.Consistency{Requirement: &v1.Consistency_FullyConsistent{FullyConsistent: true}},
			RelationshipFilter: filter.proto(),
			OptionalLimit:      1000,
			OptionalCursor:     cursor,
		})
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", Rel(filter), err)
		}
		page := 0
		for {
			response, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", Rel(filter), err)
			}
			relationship := response.GetRelationship()
			rels = append(rels, Rel{
				relationship.GetResource().GetObjectType(), relationship.GetResource().GetObjectId(), relationship.GetRelation(),
				relationship.GetSubject().GetObject().GetObjectType(), relationship.GetSubject().GetObject().GetObjectId(),
			})
			cursor = response.GetAfterResultCursor()
			page++
		}
		if page < 1000 {
			return rels, nil
		}
	}
}
