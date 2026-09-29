package httpserver

import "github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"

// Omitted organization settings in a historical file mean auth's creation
// defaults. Materialize them so PATCH does not retain later restrictions.
func rollbackOrganizationDefaults(document spec.Document) spec.Document {
	document = document.Normalize()
	if document.Metadata.Domains == nil {
		document.Metadata.Domains = []string{}
	}
	if document.Organization == nil {
		document.Organization = &spec.Organization{}
	}
	if document.Organization.Timezone == nil {
		timezone := "UTC"
		document.Organization.Timezone = &timezone
	}
	if document.People == nil {
		document.People = &spec.People{}
	} else {
		people := *document.People
		document.People = &people
	}
	if document.People.Guests == nil {
		guests := false
		document.People.Guests = &guests
	}
	return document
}
