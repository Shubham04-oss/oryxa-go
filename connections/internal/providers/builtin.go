package providers

// Builtin returns the providers connections knows without a custom
// definition. Each call returns fresh values, so a caller may point one at a
// test server without affecting any other.
func Builtin() Registry {
	registry := Registry{}
	for _, provider := range []Provider{
		Airtable(), Asana(), Atlassian(), Box(), ClickUp(), Dropbox(), Figma(), GitHub(), Google(), Linear(), Microsoft(), Notion(), Slack(),
	} {
		registry[provider.Name] = provider
	}
	return registry
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
