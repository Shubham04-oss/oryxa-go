package probe

import "strings"

// ServiceOps is one service's operations, in the order they must run: what
// creates objects comes before what reads or changes them, and what consumes
// them comes last.
type ServiceOps struct {
	Name string
	Ops  []Op
}

// Catalog lists every service's operations, in dependency order across
// services too (teams before files, since a file can hang off a team).
func Catalog() []ServiceOps {
	return []ServiceOps{
		{"teams", TeamsOps()},
		{"files", FilesOps()},
		{"billing", BillingOps()},
		{"rag", RAGOps()},
		{"knowledge", KnowledgeOps()},
		{"apps", AppsOps()},
		{"agents", AgentsOps()},
	}
}

// Select returns the operations of the services named, or of all when only is
// empty, with consuming operations moved after everything else in their service.
func Select(only []string) []ServiceOps {
	want := map[string]bool{}
	for _, name := range only {
		if name = strings.TrimSpace(name); name != "" {
			want[name] = true
		}
	}
	var out []ServiceOps
	for _, service := range Catalog() {
		if len(want) > 0 && !want[service.Name] {
			continue
		}
		var keep, consume []Op
		for _, op := range service.Ops {
			if op.Consumes {
				consume = append(consume, op)
			} else {
				keep = append(keep, op)
			}
		}
		out = append(out, ServiceOps{service.Name, append(keep, consume...)})
	}
	return out
}

// Flatten joins services' operations into one ordered list. Operations that
// consume what they act on go after every other service's, because a file can
// hang off a team and must be made before the team is deleted.
func Flatten(services []ServiceOps) []Op {
	var keep, consume []Op
	for _, service := range services {
		for _, op := range service.Ops {
			if op.Consumes {
				consume = append(consume, op)
			} else {
				keep = append(keep, op)
			}
		}
	}
	return append(keep, consume...)
}
