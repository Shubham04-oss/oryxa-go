package assembly

import (
	"slices"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runprofile"
)

// TestASpecialistGetsTheAppsToolboxItsGrantReaches: a specialist granted no
// app has no apps toolbox; one granted apps but no write has the apps' reads
// alone, the default; one granted a write has the full toolbox, whose calls
// apps checks against the grant. Every other toolbox is untouched.
func TestASpecialistGetsTheAppsToolboxItsGrantReaches(t *testing.T) {
	full := agents.Toolbox{Name: agents.AppsToolboxName, Description: "full"}
	reads := agents.Toolbox{Name: agents.AppsToolboxName, Description: "reads"}
	krude := &Krude{readOnlyApps: &reads}
	toolboxes := []agents.Toolbox{{Name: "sandbox"}, full, {Name: agents.KnowledgeToolboxName}}

	for name, test := range map[string]struct {
		grant runprofile.Grant
		apps  string
	}{
		"no app":       {runprofile.Grant{}, ""},
		"the default":  {runprofile.DefaultGrant(), "reads"},
		"a write app":  {runprofile.Grant{Apps: []string{"gmail"}, WriteApps: []string{"gmail"}}, "full"},
		"a write tool": {runprofile.Grant{Apps: []string{"gmail"}, WriteTools: []string{"GMAIL_SEND_EMAIL"}}, "full"},
	} {
		got := krude.granted(toolboxes, test.grant)
		var names, apps []string
		for _, box := range got {
			names = append(names, box.Name)
			if box.Name == agents.AppsToolboxName {
				apps = append(apps, box.Description)
			}
		}
		if !slices.Contains(names, "sandbox") || !slices.Contains(names, agents.KnowledgeToolboxName) {
			t.Errorf("%s: lost a toolbox the grant does not govern: %v", name, names)
		}
		if want := []string{test.apps}; test.apps == "" && len(apps) != 0 || test.apps != "" && !slices.Equal(apps, want) {
			t.Errorf("%s: apps toolboxes %v, want %q", name, apps, test.apps)
		}
	}
	if len(toolboxes) != 3 || toolboxes[1].Description != "full" {
		t.Fatalf("granted changed the shared toolboxes: %+v", toolboxes)
	}

	// Without the apps service there is no read-only toolbox to give, so a
	// specialist without writes gets none rather than the full one.
	if got := (&Krude{}).granted(toolboxes, runprofile.DefaultGrant()); slices.ContainsFunc(got, func(box agents.Toolbox) bool { return box.Name == agents.AppsToolboxName }) {
		t.Fatalf("with no read-only apps toolbox, the default grant got %+v", got)
	}
}
