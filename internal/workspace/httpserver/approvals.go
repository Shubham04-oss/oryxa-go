package httpserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/runtime"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/store"
)

var errStalePlan = errors.New("stale_plan: run plan and review the current changes again")

func documentHash(document spec.Document, rollback bool) (string, error) {
	encoded, err := document.YAML()
	if err != nil {
		return "", err
	}
	if rollback {
		encoded = append(encoded, []byte("\nrollback=true")...)
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}

func (server *Server) currentVersion(ctx context.Context, organizationID string) (int64, error) {
	version, err := server.repository.Latest(ctx, organizationID)
	if errors.Is(err, store.ErrVersionNotFound) {
		return 0, nil
	}
	return version.Number, err
}

func (server *Server) issuePlan(ctx context.Context, organizationID string, access actor, document spec.Document, plan runtime.Plan, rollback bool, version int64) (runtime.Plan, error) {
	hash, err := documentHash(document, rollback)
	if err != nil {
		return runtime.Plan{}, err
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return runtime.Plan{}, err
	}
	plan.PlanID, plan.BaseVersion = hex.EncodeToString(nonce), version
	encoded, err := json.Marshal(struct {
		DocumentHash string
		Plan         runtime.Plan
	}{hash, plan})
	if err != nil {
		return runtime.Plan{}, err
	}
	planHash := sha256.Sum256(encoded)
	plan.PlanHash = hex.EncodeToString(planHash[:])
	encoded, err = json.Marshal(plan)
	if err != nil {
		return runtime.Plan{}, err
	}
	if err := server.repository.SavePlan(ctx, store.ReviewedPlan{
		ID: plan.PlanID, OrganizationID: organizationID, ActorID: access.ID,
		DocumentHash: hash, BaseVersion: version, Plan: encoded, ExpiresAt: time.Now().Add(15 * time.Minute),
	}); err != nil {
		return runtime.Plan{}, err
	}
	return plan, nil
}

func (server *Server) verifyPlan(ctx context.Context, organizationID string, access actor, document spec.Document, current, presented runtime.Plan, rollback bool) (runtime.Plan, error) {
	if presented.PlanID == "" {
		return runtime.Plan{}, errStalePlan
	}
	reviewed, err := server.repository.ReviewedPlan(ctx, organizationID, access.ID, presented.PlanID)
	if errors.Is(err, store.ErrPlanNotFound) {
		return runtime.Plan{}, errStalePlan
	}
	if err != nil {
		return runtime.Plan{}, err
	}
	hash, err := documentHash(document, rollback)
	if err != nil {
		return runtime.Plan{}, err
	}
	version, err := server.currentVersion(ctx, organizationID)
	if err != nil {
		return runtime.Plan{}, err
	}
	var issued runtime.Plan
	if err := json.Unmarshal(reviewed.Plan, &issued); err != nil {
		return runtime.Plan{}, err
	}
	if reviewed.DocumentHash != hash || reviewed.BaseVersion != version || !reviewed.ExpiresAt.After(time.Now()) || issued.PlanHash != presented.PlanHash || issued.BaseVersion != presented.BaseVersion ||
		!slices.Equal(runtime.SensitiveChanges(issued), runtime.SensitiveChanges(current)) {
		return runtime.Plan{}, errStalePlan
	}
	return issued, nil
}
