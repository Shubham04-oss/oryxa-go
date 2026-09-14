// Package v1 contains the stable version-one events emitted by files.
package v1

import "time"

const (
	Source = "oryxa.files"

	ObjectCreatedDetailType = "files.object.created.v1"
	ObjectDeletedDetailType = "files.object.deleted.v1"
)

type ObjectCreated struct {
	FileID         string    `json:"file_id"`
	OrganizationID string    `json:"organization_id"`
	Scope          string    `json:"scope"`
	TeamID         string    `json:"team_id,omitempty"`
	MemberID       string    `json:"member_id,omitempty"`
	Filename       string    `json:"filename"`
	ContentType    string    `json:"content_type"`
	Purpose        string    `json:"purpose"`
	SizeBytes      int64     `json:"size_bytes"`
	SHA256         string    `json:"sha256"`
	CreatedBy      string    `json:"created_by"`
	CreatedAt      time.Time `json:"created_at"`
	ExpiresAt      time.Time `json:"expires_at"`
}

type ObjectDeleted struct {
	FileID         string    `json:"file_id"`
	OrganizationID string    `json:"organization_id"`
	Scope          string    `json:"scope"`
	TeamID         string    `json:"team_id,omitempty"`
	MemberID       string    `json:"member_id,omitempty"`
	Purpose        string    `json:"purpose"`
	DeletedBy      string    `json:"deleted_by,omitempty"`
	Reason         string    `json:"reason"`
	DeletedAt      time.Time `json:"deleted_at"`
}

const (
	DeletedReasonDeleted                     = "deleted"
	DeletedReasonExpired                     = "expired"
	DeletedReasonOrganizationDeleted         = "organization_deleted"
	DeletedReasonOrganizationMembershipEnded = "organization_membership_ended"
	DeletedReasonTeamDeleted                 = "team_deleted"
)
