package probe

import "encoding/base64"

// M is a JSON object.
type M = map[string]any

const orgPath = "/v1/organizations/{org}"

func embedding(seed float64) []float64 {
	values := make([]float64, 1536)
	for index := range values {
		values[index] = seed + float64(index%7)/100
	}
	return values
}

// TeamsOps is the teams service: teams, membership, search, embeddings and
// the AI tools.
func TeamsOps() []Op {
	teams := orgPath + "/teams"
	team := teams + "/{team}"
	return []Op{
		{Name: "teams.create", Method: "POST", Path: teams, Body: M{"name": "Platform"}, Capture: map[string]string{"team": "id"}},
		{Name: "teams.create-child", Method: "POST", Path: teams, Body: M{"name": "Platform Infra", "parent_id": "{team}"}, Capture: map[string]string{"child": "id"}},
		{Name: "teams.list", Method: "GET", Path: teams},
		{Name: "teams.get", Method: "GET", Path: team},
		{Name: "teams.search", Method: "GET", Path: teams + "/search", Query: []QP{{"q", "plat"}, {"limit", "5"}}},
		{Name: "teams.descendants", Method: "GET", Path: team + "/descendants"},
		{Name: "teams.embedding.set", Method: "PUT", Path: team + "/embedding", Body: M{"embedding": embedding(0.1)}},
		{Name: "teams.nearest", Method: "POST", Path: teams + "/nearest", Body: M{"embedding": embedding(0.1), "limit": 5}},
		{Name: "teams.update", Method: "PATCH", Path: team, Body: M{"name": "Platform Core"}},
		{Name: "teams.notification", Method: "PATCH", Path: team + "/notification", Body: M{"notify_email": true}},
		{Name: "teams.members.add", Method: "POST", Path: team + "/members", Body: M{"user_id": "{member_uid}", "role": "member"}},
		{Name: "teams.members.list", Method: "GET", Path: team + "/members"},
		{Name: "teams.members.descendants", Method: "GET", Path: team + "/members/descendants"},
		{Name: "teams.members.role", Method: "PATCH", Path: team + "/members/{member_uid}", Body: M{"role": "admin"}},
		{Name: "teams.directory", Method: "GET", Path: orgPath + "/directory"},
		{Name: "members.search", Method: "GET", Path: orgPath + "/members/search", Query: []QP{{"q", "chaos"}, {"limit", "5"}}},
		{Name: "tools.list", Method: "GET", Path: orgPath + "/tools"},
		{Name: "tools.list_teams", Method: "POST", Path: orgPath + "/tools/list_teams", Body: M{}},
		{Name: "tools.call_team", Method: "POST", Path: orgPath + "/tools/call_team", Body: M{"query": "{team}"}},
		{Name: "tools.call_team_members", Method: "POST", Path: orgPath + "/tools/call_team_members", Body: M{"query": "{team}"}},
		// Consuming operations last.
		{Name: "teams.members.remove", Method: "DELETE", Path: team + "/members/{member_uid}", Consumes: true},
		{Name: "teams.delete", Method: "DELETE", Path: team, Consumes: true},
	}
}

// FilesOps is the files service.
func FilesOps() []Op {
	files := orgPath + "/files"
	file := files + "/{file}"
	content := base64.StdEncoding.EncodeToString([]byte("hello, chaos\n"))
	return []Op{
		{Name: "files.create", Method: "POST", Path: files, Actor: "a.member", Capture: map[string]string{"file": "id"},
			Body: M{"filename": "notes.txt", "content_type": "text/plain", "purpose": "upload", "scope": "member", "retention_days": 7, "content": content}},
		{Name: "files.create-org", Method: "POST", Path: files, Capture: map[string]string{"file_org": "id"},
			Body: M{"filename": "policy.txt", "content_type": "text/plain", "purpose": "upload", "scope": "organization", "content": content}},
		{Name: "files.create-team", Method: "POST", Path: files, Capture: map[string]string{"file_team": "id"},
			Body: M{"filename": "team.txt", "content_type": "text/plain", "purpose": "upload", "scope": "team", "team_id": "{team}", "content": content}},
		{Name: "files.list", Method: "GET", Path: files, Actor: "a.member"},
		{Name: "files.list-filtered", Method: "GET", Path: files, Actor: "a.member", Query: []QP{{"scope", "member"}, {"purpose", "upload"}, {"year", "2026"}, {"month", "9"}, {"day", "20"}}},
		{Name: "files.get", Method: "GET", Path: file, Actor: "a.member"},
		{Name: "files.content", Method: "GET", Path: file + "/content", Actor: "a.member"},
		{Name: "files.download-url", Method: "POST", Path: file + "/download-url", Actor: "a.member"},
		{Name: "files.delete", Method: "DELETE", Path: file, Actor: "a.member", Consumes: true},
	}
}
