package projector

// orderingKeys names what a change touches, for tokens.Seen: an event is
// skipped when a newer one already touched any key in check, and record is
// what this one marks. A key is an object ("team:t1"), or a resource and a
// subject ("team:t1@member:u1"); the relation is left out, so a role change
// and a removal of the same member order against each other.
//
// A touch checks its resource, its subject and the pair, so a late add
// can't outlive a newer delete of the pair, of the whole resource (a team
// deleted) or of everything pointing at the subject (an organization
// deleted). It records only the pair, so other subjects of the same resource
// keep their own order.
func orderingKeys(change Change) (check, record []string) {
	for _, filter := range change.Delete {
		key := filterKey(filter)
		if key == "" {
			continue
		}
		check = append(check, key)
		record = append(record, key)
	}
	for _, rel := range change.Touch {
		resource := rel.ResourceType + ":" + rel.ResourceID
		subject := rel.SubjectType + ":" + rel.SubjectID
		pair := resource + "@" + subject
		check = append(check, resource, subject, pair)
		record = append(record, pair)
	}
	return check, record
}

func filterKey(filter Filter) string {
	resource := filter.ResourceType + ":" + filter.ResourceID
	subject := filter.SubjectType + ":" + filter.SubjectID
	switch {
	case filter.ResourceID != "" && filter.SubjectID != "":
		return resource + "@" + subject
	case filter.ResourceID != "":
		return resource
	case filter.SubjectID != "":
		return subject
	}
	// a filter by type alone names nothing to order against
	return ""
}
