// Command teamspacepreview serves a deterministic, read-only product workspace
// for local frontend QA. It intentionally contains no credentials or private
// agent context; every value represents a safe API projection.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"time"
)

const (
	address = "127.0.0.1:8080"
	orgID   = "org_demo"
)

var now = time.Date(2026, time.September, 19, 15, 30, 0, 0, time.UTC).Format(time.RFC3339)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/", serve)
	log.Printf("Frontend preview API listening on http://%s", address)
	log.Fatal(http.ListenAndServe(address, mux))
}

func serve(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Type", "application/json")
	if request.Method != http.MethodGet {
		http.Error(writer, `{"message":"the preview fixture is read-only"}`, http.StatusMethodNotAllowed)
		return
	}
	responses := map[string]any{
		"/v1/auth/me": map[string]any{
			"user":          map[string]any{"id": "user_ada", "email": "ada@oryxa.test", "display_name": "Ada", "created_at": now, "updated_at": now},
			"organizations": []any{organization()},
		},
		"/v1/organizations/" + orgID: map[string]any{
			"organization": organization(),
			"membership":   map[string]any{"user_id": "user_ada", "organization_id": orgID, "email": "ada@oryxa.test", "display_name": "Ada", "role": "owner", "joined_at": now},
		},
		"/v1/organizations/" + orgID + "/teams": map[string]any{"teams": []any{
			map[string]any{"id": "team_launch", "organization_id": orgID, "name": "Launch Council", "path": "Launch Council", "role": "admin", "can_manage": true, "created_by": "user_ada", "created_at": now, "updated_at": now},
		}},
		"/v1/organizations/" + orgID + "/agents": map[string]any{"agents": []any{
			agent("agt_ada", "primary", "Aster", "Ada's private primary collaborator."),
			agent("agt_research", "specialist", "Signal Scout", "Finds bounded market and customer evidence."),
			agent("agt_quality", "specialist", "Quality Guard", "Reviews launch readiness and operational risk."),
		}},
		"/v1/organizations/" + orgID + "/threads": map[string]any{"threads": threads()},
		"/v1/organizations/" + orgID + "/threads/thr_launch": map[string]any{
			"id": "thr_launch", "title": "Prepare the October launch review", "created_at": now, "updated_at": now,
			"pending_confirmations": []any{}, "items": []any{
				map[string]any{"id": "itm_1", "type": "message", "author": "member", "text": "Prepare the October launch review and surface only material risks.", "at": now},
				map[string]any{"id": "itm_2", "type": "message", "author": "Aster", "text": "The launch is bounded to twelve design partners. Platform headroom is the remaining material risk.", "at": now},
			},
		},
		"/v1/organizations/" + orgID + "/agent-teams":     map[string]any{"teams": []any{agentTeam()}},
		"/v1/organizations/" + orgID + "/agent-workflows": map[string]any{"workflows": []any{agentWorkflow()}},
		"/v1/organizations/" + orgID + "/agent-workflows/awf_launch/executions": map[string]any{"executions": []any{
			map[string]any{"id": "awx_1", "organization_id": orgID, "owner_user_id": "user_ada", "workflow_id": "awf_launch", "started_by": "schedule", "status": "succeeded", "created_at": now, "started_at": now, "completed_at": now},
		}},
		"/v1/organizations/" + orgID + "/agent-requests/inbox": map[string]any{"requests": []any{agentRequest()}},
		"/v1/organizations/" + orgID + "/agent-requests/sent":  map[string]any{"requests": []any{}},
		"/v1/organizations/" + orgID + "/organization-agent":   organizationAgent(),
		"/v1/organizations/" + orgID + "/organization-agent/context": map[string]any{
			"organization_id": orgID, "agent_id": "oag_atlas", "revision": 12,
			"summary":        "Launch scope is aligned. Customer timing is understood; engineering capacity remains the only unresolved constraint.",
			"decisions":      []string{"Keep the pilot to twelve design partners", "Gate expansion on error-budget recovery"},
			"risks":          []string{"Platform capacity during the October migration"},
			"open_questions": []string{"Who owns the migration fallback?"}, "source_entry_id": "tse_capacity", "updated_at": now,
		},
		"/v1/organizations/" + orgID + "/organization-agent/follow-ups": map[string]any{"follow_ups": []any{
			map[string]any{"id": "ofu_1", "organization_id": orgID, "synthesis_revision": 12, "source_entry_id": "tse_capacity", "teamspace_id": "tsp_launch", "target_agent_id": "agt_grace", "task": "Confirm the migration fallback owner", "rationale": "The current shared ledger names the risk but not its accountable owner", "status": "proposed", "created_at": now},
		}},
		"/v1/organizations/" + orgID + "/teamspaces":                    map[string]any{"teamspaces": []any{teamspace()}},
		"/v1/organizations/" + orgID + "/teamspaces/tsp_launch":         teamspace(),
		"/v1/organizations/" + orgID + "/teamspaces/tsp_launch/entries": map[string]any{"entries": entries()},
		"/v1/organizations/" + orgID + "/teams/team_launch": map[string]any{
			"team":       map[string]any{"id": "team_launch", "organization_id": orgID, "name": "Launch Council", "path": "launch", "created_by": "user_ada", "created_at": now, "updated_at": now},
			"can_manage": true,
			"membership": map[string]any{"user_id": "user_ada", "team_id": "team_launch", "organization_id": orgID, "email": "ada@oryxa.test", "display_name": "Ada", "role": "admin", "notify_email": true, "joined_at": now},
		},
		"/v1/organizations/" + orgID + "/teams/team_launch/members": map[string]any{"members": teamMembers()},
		"/v1/organizations/" + orgID + "/members":                   map[string]any{"members": organizationMembers()},
		"/v1/organizations/" + orgID + "/members/search":            map[string]any{"members": []any{}},
		"/v1/organizations/" + orgID + "/invitations": map[string]any{"invitations": []any{
			map[string]any{"id": "inv_1", "organization_id": orgID, "email": "maya@oryxa.test", "role": "member", "status": "pending", "invited_by": "user_ada", "created_at": now, "expires_at": time.Date(2026, time.September, 26, 15, 30, 0, 0, time.UTC).Format(time.RFC3339)},
		}},
		"/v1/organizations/" + orgID + "/files":                      map[string]any{"files": files()},
		"/v1/organizations/" + orgID + "/files/file_brief":           files()[0],
		"/v1/organizations/" + orgID + "/apps/connections":           map[string]any{"connections": []any{connection()}},
		"/v1/organizations/" + orgID + "/apps/connections/apc_gmail": connection(),
		"/v1/organizations/" + orgID + "/apps/connections/apc_gmail/targets": map[string]any{"targets": []any{
			map[string]any{"id": "inbox", "kind": "mailbox", "name": "Inbox", "description": "Primary mailbox", "target": map[string]any{"mailbox": "inbox"}},
		}},
		"/v1/organizations/" + orgID + "/apps/triggers": map[string]any{"triggers": []any{
			map[string]any{"id": "trg_email", "member_id": "user_ada", "connection_id": "apc_gmail", "toolkit": "gmail", "trigger": "NEW_EMAIL", "config": map[string]any{}, "status": "active", "created_at": now, "updated_at": now},
		}},
		"/v1/apps/catalog":       map[string]any{"toolkits": []any{toolkit()}, "next_cursor": ""},
		"/v1/apps/catalog/gmail": toolkit(),
		"/v1/apps/catalog/gmail/triggers": map[string]any{"triggers": []any{
			map[string]any{"toolkit": "gmail", "trigger": "NEW_EMAIL", "name": "New email", "description": "When a new message reaches the inbox", "kind": "webhook", "config_schema": map[string]any{"type": "object", "properties": map[string]any{}}},
		}},
		"/v1/sources/providers":                                           map[string]any{"providers": []any{"github", "notion", "slack"}},
		"/v1/organizations/" + orgID + "/sources":                         map[string]any{"sources": []any{}},
		"/v1/organizations/" + orgID + "/corpora":                         map[string]any{"corpora": []any{corpus()}},
		"/v1/organizations/" + orgID + "/corpora/corpus_launch":           corpus(),
		"/v1/organizations/" + orgID + "/corpora/corpus_launch/documents": map[string]any{"documents": documents()},
		"/v1/billing/plans": map[string]any{"unpaid": map[string]any{"member_limit": 3}, "plans": []any{
			map[string]any{"key": "team", "member_limit": 25},
			map[string]any{"key": "scale", "member_limit": nil, "member_limit_per_unit": 1, "min_quantity": 5},
		}},
		"/v1/billing/organizations/" + orgID: map[string]any{
			"organization_id": orgID, "can_manage": true,
			"entitlements": map[string]any{"plan": "team", "status": "active", "member_limit": 25, "grace_ends_at": nil},
			"subscription": map[string]any{"plan": "team", "status": "active", "quantity": 1, "renews_at": time.Date(2026, time.October, 19, 15, 30, 0, 0, time.UTC).Format(time.RFC3339), "ends_at": nil, "grace_ends_at": nil},
		},
		"/v1/agent-grant": map[string]any{"granted": true, "created_at": now},
	}
	response, ok := responses[request.URL.Path]
	if !ok {
		http.Error(writer, `{"message":"preview route not found"}`, http.StatusNotFound)
		return
	}
	if err := json.NewEncoder(writer).Encode(response); err != nil {
		log.Printf("encode %s: %v", request.URL.Path, err)
	}
}

func organization() map[string]any {
	return map[string]any{"id": orgID, "name": "Northstar Labs", "slug": "northstar-labs", "created_by": "user_ada", "created_at": now, "updated_at": now}
}

func organizationAgent() map[string]any {
	return map[string]any{
		"id": "oag_atlas", "organization_id": orgID, "name": "Atlas", "description": "Keeps the organization brief coherent from deliberately shared work.",
		"instructions": "Synthesize shared work, preserve attribution, and propose bounded follow-ups without starting them.",
		"status":       "active", "revision": 4, "can_manage": true, "created_at": now, "updated_at": now,
	}
}

func agent(id, kind, name, description string) map[string]any {
	return map[string]any{
		"id": id, "organization_id": orgID, "owner_user_id": "user_ada", "kind": kind, "name": name,
		"description": description, "instructions": "Private in the real system.", "status": "active", "revision": 1, "created_at": now, "updated_at": now,
	}
}

func threads() []any {
	return []any{
		map[string]any{"id": "thr_launch", "title": "Prepare the October launch review", "created_at": now, "updated_at": now},
		map[string]any{"id": "thr_research", "title": "Summarize design-partner feedback", "created_at": now, "updated_at": time.Date(2026, time.September, 19, 13, 10, 0, 0, time.UTC).Format(time.RFC3339)},
	}
}

func agentTeam() map[string]any {
	return map[string]any{
		"id": "atm_launch", "organization_id": orgID, "owner_user_id": "user_ada", "name": "Launch desk", "description": "Prepares a bounded launch-readiness brief.",
		"coordinator_agent_id": "agt_ada", "coordinator_name": "Aster", "status": "active", "revision": 3, "created_at": now, "updated_at": now,
		"members": []any{
			map[string]any{"agent_id": "agt_research", "name": "Signal Scout", "description": "Finds customer evidence", "role": "Validate customer timing", "position": 0, "status": "active"},
			map[string]any{"agent_id": "agt_quality", "name": "Quality Guard", "description": "Reviews operational risk", "role": "Challenge readiness claims", "position": 1, "status": "active"},
		},
	}
}

func agentWorkflow() map[string]any {
	return map[string]any{
		"id": "awf_launch", "organization_id": orgID, "owner_user_id": "user_ada", "team_id": "atm_launch", "name": "Weekly launch review", "description": "Produces the Monday readiness brief.",
		"status": "active", "revision": 2, "schedule": map[string]any{"expression": "cron(0 9 ? * MON *)", "timezone": "Asia/Kolkata", "state": "enabled"},
		"assignments": []any{
			map[string]any{"agent_id": "agt_research", "task": "Check customer timing", "context": "Use only approved CRM evidence"},
			map[string]any{"agent_id": "agt_quality", "task": "Review operational risk"},
		},
		"created_at": now, "updated_at": now,
	}
}

func agentRequest() map[string]any {
	return map[string]any{
		"id": "irq_1", "organization_id": orgID, "purpose": "Confirm a product constraint", "task": "Answer whether the migration fallback has an owner.",
		"requested_tools": []any{"project.read"}, "requested_permissions": []any{"projects:read"}, "context": []any{map[string]any{"label": "Launch", "value": "October pilot"}},
		"requester": map[string]any{"user_id": "user_grace", "user_display_name": "Grace", "user_email": "grace@oryxa.test", "agent_id": "agt_grace", "agent_name": "Cleo"},
		"target":    map[string]any{"user_id": "user_ada", "user_display_name": "Ada", "user_email": "ada@oryxa.test", "agent_id": "agt_ada", "agent_name": "Aster"},
		"status":    "pending", "revision": 1, "created_at": now, "updated_at": now, "expires_at": time.Date(2026, time.September, 20, 15, 30, 0, 0, time.UTC).Format(time.RFC3339),
	}
}

func teamMembers() []any {
	return []any{
		map[string]any{"user_id": "user_ada", "team_id": "team_launch", "organization_id": orgID, "email": "ada@oryxa.test", "display_name": "Ada", "role": "admin", "notify_email": true, "joined_at": now},
		map[string]any{"user_id": "user_grace", "team_id": "team_launch", "organization_id": orgID, "email": "grace@oryxa.test", "display_name": "Grace", "role": "member", "notify_email": true, "joined_at": now},
		map[string]any{"user_id": "user_lin", "team_id": "team_launch", "organization_id": orgID, "email": "lin@oryxa.test", "display_name": "Lin", "role": "member", "notify_email": false, "joined_at": now},
	}
}

func organizationMembers() []any {
	return []any{
		map[string]any{"user_id": "user_ada", "organization_id": orgID, "email": "ada@oryxa.test", "display_name": "Ada", "role": "owner", "joined_at": now},
		map[string]any{"user_id": "user_grace", "organization_id": orgID, "email": "grace@oryxa.test", "display_name": "Grace", "role": "admin", "joined_at": now},
		map[string]any{"user_id": "user_lin", "organization_id": orgID, "email": "lin@oryxa.test", "display_name": "Lin", "role": "member", "joined_at": now},
	}
}

func files() []any {
	return []any{
		map[string]any{"id": "file_brief", "organization_id": orgID, "member_id": "user_ada", "created_by": "user_ada", "filename": "launch-readiness.md", "content_type": "text/markdown", "purpose": "upload", "scope": "member", "size_bytes": 18342, "sha256": "preview", "created_at": now, "expires_at": time.Date(2026, time.October, 19, 15, 30, 0, 0, time.UTC).Format(time.RFC3339)},
		map[string]any{"id": "file_capacity", "organization_id": orgID, "team_id": "team_launch", "created_by": "user_grace", "filename": "capacity-forecast.pdf", "content_type": "application/pdf", "purpose": "upload", "scope": "team", "size_bytes": 243781, "sha256": "preview", "created_at": now, "expires_at": time.Date(2026, time.October, 19, 15, 30, 0, 0, time.UTC).Format(time.RFC3339)},
	}
}

func connection() map[string]any {
	return map[string]any{"id": "apc_gmail", "member_id": "user_ada", "toolkit": "gmail", "label": "Work Gmail", "status": "active", "composio_account_id": "preview", "connected_at": now, "created_at": now, "updated_at": now}
}

func toolkit() map[string]any {
	return map[string]any{"toolkit": "gmail", "name": "Gmail", "description": "Email, threads, labels, and inbox events.", "logo": "", "categories": []any{map[string]any{"id": "productivity", "name": "Productivity"}}, "auth_schemes": []any{"OAUTH2"}, "connectable": true, "tools_count": 28, "triggers_count": 5}
}

func corpus() map[string]any {
	return map[string]any{"id": "corpus_launch", "organization_id": orgID, "member_id": "user_ada", "name": "Launch intelligence", "slug": "launch-intelligence", "scope": "member", "document_count": 3, "chunk_count": 42, "metadata": map[string]any{"origin": "knowledge"}, "created_at": now, "updated_at": now}
}

func documents() []any {
	return []any{
		map[string]any{"id": "doc_1", "title": "Design partner feedback", "content_type": "text/markdown", "byte_size": 12420, "chunk_count": 18, "status": "ready", "source": map[string]any{"kind": "external", "ref": "https://example.test/design-partner-feedback"}, "created_at": now, "updated_at": now, "ingested_at": now},
		map[string]any{"id": "doc_2", "title": "October rollout plan", "content_type": "application/pdf", "byte_size": 35220, "chunk_count": 24, "status": "ready", "source": map[string]any{"kind": "file", "ref": "file_brief"}, "created_at": now, "updated_at": now, "ingested_at": now},
	}
}

func teamspace() map[string]any {
	return map[string]any{
		"id": "tsp_launch", "organization_id": orgID, "team_id": "team_launch", "team_name": "Launch Council", "name": "Launch readiness",
		"description":        "A deliberate shared surface for the October pilot. Private working context stays with each teammate.",
		"created_by_user_id": "user_ada", "organization_agent": organizationAgent(), "status": "active", "revision": 7,
		"can_manage": true, "is_member": true, "created_at": now, "updated_at": now,
		"whisperers": []any{
			map[string]any{"user_id": "user_ada", "user_display_name": "Ada", "agent_id": "agt_ada", "agent_name": "Aster", "mode": "whisperer"},
			map[string]any{"user_id": "user_grace", "user_display_name": "Grace", "agent_id": "agt_grace", "agent_name": "Cleo", "mode": "whisperer"},
			map[string]any{"user_id": "user_lin", "user_display_name": "Lin", "agent_id": "agt_lin", "agent_name": "Mori", "mode": "whisperer"},
		},
		"contributions": []any{
			map[string]any{"agent_id": "agt_research", "owner_user_id": "user_ada", "name": "Signal Scout", "description": "Finds customer evidence", "status": "active", "role": "Validate customer timing and cite the source", "delegation_mode": "request", "position": 0, "created_at": now},
			map[string]any{"agent_id": "agt_capacity", "owner_user_id": "user_grace", "name": "Capacity Planner", "description": "Models platform constraints", "status": "active", "role": "Quantify migration headroom", "delegation_mode": "automatic", "position": 1, "created_at": now},
		},
	}
}

func entries() []any {
	return []any{
		map[string]any{"id": "tse_capacity", "teamspace_id": "tsp_launch", "organization_id": orgID, "author_user_id": "user_grace", "author_agent_id": "agt_capacity", "author_agent_name": "Capacity Planner", "kind": "result", "title": "Migration headroom", "summary": "Current projections leave 18% headroom. Hold the pilot at twelve partners until the retry queue is below 2%. This is shared evidence, not Grace's private working context.", "tools_used": []string{"cloudwatch.query", "forecast.compare"}, "artifacts": []any{map[string]any{"name": "Capacity brief", "uri": "https://example.test/capacity-brief", "media_type": "text/markdown"}}, "created_at": now},
		map[string]any{"id": "tse_customer", "teamspace_id": "tsp_launch", "organization_id": orgID, "author_user_id": "user_ada", "author_agent_id": "agt_ada", "author_agent_name": "Aster", "kind": "decision", "title": "Pilot cohort bounded", "summary": "Sales and product agree on twelve design partners. Expansion requires an explicit readiness review; no follow-up has been auto-started.", "tools_used": []string{"crm.read"}, "artifacts": []any{}, "created_at": time.Date(2026, time.September, 19, 14, 20, 0, 0, time.UTC).Format(time.RFC3339)},
	}
}
