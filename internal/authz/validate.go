package authz

import (
	"fmt"
	"regexp"
	"strings"

	cedar "github.com/cedar-policy/cedar-go"
)

// maxPolicyBytes bounds a stored Policy's Text: generous enough for several
// forbid statements and their comments, tight enough to stay something an
// admin can read in one screen.
const maxPolicyBytes = 8 * 1024

// oryxaReferencePattern finds every Oryxa:: reference in a policy's text:
// either an entity type (Oryxa::KnowledgeBase) or an action
// (Oryxa::Action::"read"). This is a textual scan of the source, not a walk
// of the parsed policy, so a match inside a string literal — a metadata
// comparison that happens to contain the text "Oryxa::" — would be misread
// as a reference to the model; the model's five entity types and three
// actions make that unlikely enough to accept for working from the source
// text directly, the way ValidatePolicy's other checks do.
var oryxaReferencePattern = regexp.MustCompile(`Oryxa::([A-Za-z][A-Za-z0-9_]*)(?:::"([^"]*)")?`)

var knownEntityTypeNames = map[string]bool{
	"Organization":  true,
	"Member":        true,
	"Agent":         true,
	"KnowledgeBase": true,
	"Document":      true,
}

var knownActionNames = map[string]bool{
	string(Read):       true,
	string(Search):     true,
	string(Administer): true,
}

// ValidatePolicy reports whether text is fit to store as an organization's
// Policy: it must parse as Cedar, forbid rather than permit, name only the
// entity types and actions schema.cedarschema documents, and evaluate
// without error against a battery of fixture requests. Store.CreatePolicy
// runs this before it writes anything, and nothing else in this package
// compiles a Policy's Text without it having passed here first.
//
// Every message names what is wrong in words an admin reading it in the UI
// can act on, quoting Cedar's own message where the problem is Cedar's to
// describe.
func ValidatePolicy(text string) error {
	if len(text) > maxPolicyBytes {
		return fmt.Errorf("%w: policy text is %d bytes, over the %d byte limit", ErrInvalid, len(text), maxPolicyBytes)
	}
	statements, err := cedar.NewPolicyListFromBytes("policy", []byte(text))
	if err != nil {
		return fmt.Errorf("%w: does not parse as Cedar: %v", ErrInvalid, err)
	}
	if len(statements) == 0 {
		return fmt.Errorf("%w: has no statements", ErrInvalid)
	}
	for n, statement := range statements {
		if statement.Effect() == cedar.Permit {
			return fmt.Errorf("%w: statement %d is a permit; stored policies may only forbid", ErrInvalid, n+1)
		}
	}
	if err := checkReferences(text); err != nil {
		return err
	}
	if err := smokeTest(statements); err != nil {
		return err
	}
	return nil
}

// checkReferences refuses a policy that names an entity type or action
// outside schema.cedarschema, so a typo or a reference to a type this
// package does not yet model reads as a clear refusal rather than a policy
// that silently never matches anything.
func checkReferences(text string) error {
	for _, match := range oryxaReferencePattern.FindAllStringSubmatch(stripCedarComments(text), -1) {
		typeName, actionName := match[1], match[2]
		if typeName == "Action" {
			if actionName != "" && !knownActionNames[actionName] {
				return fmt.Errorf("%w: unknown action %q", ErrInvalid, `Oryxa::Action::"`+actionName+`"`)
			}
			continue
		}
		if !knownEntityTypeNames[typeName] {
			return fmt.Errorf("%w: unknown entity type %q", ErrInvalid, "Oryxa::"+typeName)
		}
	}
	return nil
}

// stripCedarComments blanks `// ...` line comments so checkReferences never
// scans one, while leaving quoted strings alone so a value containing "//"
// is never mistaken for the start of one.
func stripCedarComments(text string) string {
	var out strings.Builder
	out.Grow(len(text))
	inString := false
	for i := 0; i < len(text); i++ {
		c := text[i]
		if inString {
			out.WriteByte(c)
			if c == '\\' && i+1 < len(text) {
				i++
				out.WriteByte(text[i])
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}
		switch {
		case c == '"':
			inString = true
			out.WriteByte(c)
		case c == '/' && i+1 < len(text) && text[i+1] == '/':
			for i < len(text) && text[i] != '\n' {
				i++
			}
			if i < len(text) {
				out.WriteByte('\n')
			}
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}

// smokeTest evaluates a candidate policy's statements, on their own, against
// every fixture request, and refuses the text if Cedar fails to evaluate any
// of them — quoting Cedar's own message, which names the attribute or
// operation that failed. See doc.go for why an evaluation error, not just an
// unwanted allow or deny, is what a stored forbid can least afford.
func smokeTest(statements cedar.PolicyList) error {
	candidate := cedar.NewPolicySet()
	for n, statement := range statements {
		candidate.Add(cedar.PolicyID(fmt.Sprintf("candidate/%d", n)), statement)
	}
	for _, fixture := range policyFixtures() {
		principalUID, principals := principalEntities(fixture.organizationID, fixture.principal)
		resources := fixture.resource.entities()
		entities := make(cedar.EntityMap, len(principals)+len(resources))
		for _, entity := range principals {
			entities[entity.UID] = entity
		}
		for _, entity := range resources {
			entities[entity.UID] = entity
		}
		request := cedar.Request{
			Principal: principalUID,
			Action:    actionUID(fixture.action),
			Resource:  fixture.resource.entityUID(),
			Context:   contextRecord(fixture.context),
		}
		_, diagnostic := cedar.Authorize(candidate, entities, request)
		if len(diagnostic.Errors) > 0 {
			return fmt.Errorf("%w: fails to evaluate: %s", ErrInvalid, diagnostic.Errors[0].Message)
		}
	}
	return nil
}

// policyFixture is one combination ValidatePolicy's smoke test evaluates a
// candidate policy against.
type policyFixture struct {
	organizationID string
	principal      Principal
	action         Action
	resource       Resource
	context        Context
}

// policyFixtures is member, admin and agent principals, each with and
// without team membership, over every action, against every knowledge base
// scope and every document access value with and without a metadata key —
// enough variety to exercise every attribute a stored policy can read,
// including the ones only present some of the time (has, member_id, team_id).
func policyFixtures() []policyFixture {
	const organizationID = "fixture-org"
	member := Principal{UserID: "fixture-member"}
	admin := Principal{UserID: "fixture-admin"}
	agent := Principal{UserID: "fixture-member", Agent: true}

	knowledgeBases := []KnowledgeBase{
		{ID: "fixture-kb-organization", OrganizationID: organizationID, Scope: "organization", CreatedBy: member.UserID},
		{ID: "fixture-kb-team", OrganizationID: organizationID, Scope: "team", TeamID: "fixture-team", CreatedBy: member.UserID},
		{ID: "fixture-kb-member", OrganizationID: organizationID, Scope: "member", MemberID: member.UserID, CreatedBy: member.UserID},
	}

	var resources []Resource
	for _, knowledgeBase := range knowledgeBases {
		resources = append(resources, knowledgeBase)
		for _, access := range []string{"", "members", "admins"} {
			resources = append(resources, Document{
				KnowledgeBase: knowledgeBase,
				ID:            "fixture-doc-" + knowledgeBase.ID + "-" + access,
				Access:        access,
			})
			resources = append(resources, Document{
				KnowledgeBase: knowledgeBase,
				ID:            "fixture-doc-" + knowledgeBase.ID + "-" + access + "-tagged",
				Access:        access,
				Metadata:      map[string]string{"confidential": "true"},
			})
		}
	}

	var fixtures []policyFixture
	for _, principal := range []Principal{member, admin, agent} {
		for _, teamMember := range []bool{false, true} {
			requestContext := Context{ActsAsAdmin: principal.UserID == admin.UserID && !principal.Agent, TeamMember: teamMember}
			for _, action := range []Action{Read, Search, Administer} {
				for _, resource := range resources {
					fixtures = append(fixtures, policyFixture{
						organizationID: organizationID,
						principal:      principal,
						action:         action,
						resource:       resource,
						context:        requestContext,
					})
				}
			}
		}
	}
	return fixtures
}
