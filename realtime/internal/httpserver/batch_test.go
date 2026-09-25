package httpserver

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	realtimev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/realtime/v1"
)

// now is when these batches are read: an hour after the events in them.
var now = time.Date(2026, time.September, 24, 11, 0, 0, 0, time.UTC)

type object = map[string]any

func postedDetail() object {
	return object{
		"organization_id":      "org_1",
		"conversation":         object{"kind": "side", "id": "org_1~tsp_1~user-a~user-b", "teamspace_id": "tsp_1"},
		"participant_user_ids": []any{"user-a", "user-b"},
		"message":              object{"id": "sdi_1", "seq": 12},
		"author":               object{"kind": "agent", "user_id": "user-b", "agent_id": "agt_1"},
		"posted_at":            "2026-09-24T10:00:00.000Z",
	}
}

func readDetail() object {
	return object{
		"organization_id":  "org_1",
		"conversation":     object{"kind": "direct", "id": "org_1~user-a~user-b"},
		"reader_user_id":   "user-b",
		"read_through_seq": 7,
		"read_at":          "2026-09-24T10:05:00.123Z",
	}
}

func eventOf(id, detailType string, detail any) object {
	return object{"id": id, "type": detailType, "detail": detail}
}

func bodyOf(t *testing.T, events ...any) []byte {
	t.Helper()
	body, err := json.Marshal(object{"events": events})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// with is detail with one field, found by its dotted path, set to value, or
// removed when value is remove.
func with(detail object, path string, value any) object {
	parts := strings.Split(path, ".")
	node := detail
	for _, part := range parts[:len(parts)-1] {
		node = node[part].(object)
	}
	if value == remove {
		delete(node, parts[len(parts)-1])
	} else {
		node[parts[len(parts)-1]] = value
	}
	return detail
}

// remove, as with's value, removes the field.
var remove = &struct{}{}

func TestDecodeWritesEachEventAsTheContractSays(t *testing.T) {
	member := postedDetail()
	member["conversation"] = object{"kind": "direct", "id": "org_1~user-a~user-b"}
	member["author"] = object{"kind": "member", "user_id": "user-a"}
	events, err := decode(bodyOf(t,
		eventOf("evt_side", realtimev1.MessagePostedDetailType, postedDetail()),
		eventOf("evt_direct", realtimev1.MessagePostedDetailType, member),
		eventOf("evt_read", realtimev1.ConversationReadDetailType, readDetail()),
	), now)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ id, detailType, data, occurredAt string }{
		{"evt_side", realtimev1.MessagePostedDetailType,
			`{"organization_id":"org_1","conversation":{"kind":"side","id":"org_1~tsp_1~user-a~user-b","teamspace_id":"tsp_1"},` +
				`"participant_user_ids":["user-a","user-b"],"message":{"id":"sdi_1","seq":12},` +
				`"author":{"kind":"agent","user_id":"user-b","agent_id":"agt_1"},"posted_at":"2026-09-24T10:00:00Z"}`,
			"2026-09-24T10:00:00Z"},
		{"evt_direct", realtimev1.MessagePostedDetailType,
			`{"organization_id":"org_1","conversation":{"kind":"direct","id":"org_1~user-a~user-b"},` +
				`"participant_user_ids":["user-a","user-b"],"message":{"id":"sdi_1","seq":12},` +
				`"author":{"kind":"member","user_id":"user-a"},"posted_at":"2026-09-24T10:00:00Z"}`,
			"2026-09-24T10:00:00Z"},
		{"evt_read", realtimev1.ConversationReadDetailType,
			`{"organization_id":"org_1","conversation":{"kind":"direct","id":"org_1~user-a~user-b"},` +
				`"reader_user_id":"user-b","read_through_seq":7,"read_at":"2026-09-24T10:05:00.123Z"}`,
			"2026-09-24T10:05:00.123Z"},
	}
	if len(events) != len(want) {
		t.Fatalf("decode() = %d events, want %d", len(events), len(want))
	}
	for index, event := range events {
		expected := want[index]
		if event.WorkerID != expected.id || event.DetailType != expected.detailType || string(event.Data) != expected.data ||
			event.OccurredAt.Format(time.RFC3339Nano) != expected.occurredAt || event.OccurredAt.Location() != time.UTC {
			t.Errorf("event %d = %s %s %s at %s,\nwant %+v", index, event.WorkerID, event.DetailType, event.Data, event.OccurredAt, expected)
		}
	}
}

// What the contract does not name is ignored, so either side can deploy
// first, and it never reaches the bus, whatever it holds.
func TestDecodeIgnoresAndDropsWhatTheContractDoesNotName(t *testing.T) {
	detail := postedDetail()
	detail["body"] = "the words themselves"
	detail["message"].(object)["preview"] = "the words themselves"
	detail["conversation"].(object)["title"] = "the words themselves"
	event := eventOf("evt_1", realtimev1.MessagePostedDetailType, detail)
	event["attempt"] = 3
	body, err := json.Marshal(object{"events": []any{event}, "cursor": "abc"})
	if err != nil {
		t.Fatal(err)
	}
	events, err := decode(body, now)
	if err != nil || len(events) != 1 {
		t.Fatalf("decode() = %+v, %v", events, err)
	}
	if strings.Contains(string(events[0].Data), "the words themselves") {
		t.Fatalf("published data carries what the contract does not name: %s", events[0].Data)
	}
}

func TestDecodeTakesTimesAsRFC3339InUTC(t *testing.T) {
	for value, want := range map[string]string{
		"2026-09-24T10:00:00Z":           "2026-09-24T10:00:00Z",
		"2026-09-24T10:00:00.1Z":         "2026-09-24T10:00:00.1Z",
		"2026-09-24T10:00:00.120Z":       "2026-09-24T10:00:00.12Z",
		"2026-09-24T10:00:00.123456789Z": "2026-09-24T10:00:00.123456789Z",
		// A day ahead of this clock, and no further.
		"2026-09-25T11:00:00.000Z": "2026-09-25T11:00:00Z",
	} {
		events, err := decode(bodyOf(t, eventOf("evt_1", realtimev1.ConversationReadDetailType, with(readDetail(), "read_at", value))), now)
		if err != nil || events[0].OccurredAt.Format(time.RFC3339Nano) != want {
			t.Errorf("read_at %s: decode() = %+v, %v; want it read as %s", value, events, err, want)
		}
	}
}

// Every way an event can be one realtime cannot publish, and what it says.
func TestDecodeRefusesWhatItCannotPublish(t *testing.T) {
	posted := func(path string, value any) object {
		return eventOf("evt_1", realtimev1.MessagePostedDetailType, with(postedDetail(), path, value))
	}
	read := func(path string, value any) object {
		return eventOf("evt_1", realtimev1.ConversationReadDetailType, with(readDetail(), path, value))
	}
	direct := func(id string) object {
		detail := postedDetail()
		detail["conversation"] = object{"kind": "direct", "id": id}
		detail["author"] = object{"kind": "member", "user_id": "user-a"}
		return eventOf("evt_1", realtimev1.MessagePostedDetailType, detail)
	}
	tests := map[string]struct {
		body  string // the raw body, when it is not a batch of event
		event any
		says  string
	}{
		"not JSON":                   {body: `{"events":[`, says: "it is not JSON"},
		"an array":                   {body: `[]`, says: "it is a JSON array, not an object"},
		"no events":                  {body: `{}`, says: "events is required"},
		"null events":                {body: `{"events":null}`, says: "events is required"},
		"events not a list":          {body: `{"events":{}}`, says: "events must be an array, not a JSON object"},
		"no event in events":         {body: `{"events":[]}`, says: "events holds no event"},
		"an event not an object":     {event: "evt_1", says: "events[0]: an event must be an object with id, type and detail: it is a JSON string, not an object"},
		"no id":                      {event: object{"type": realtimev1.ConversationReadDetailType, "detail": readDetail()}, says: "id must be 1 to 200"},
		"an id too long":             {event: eventOf(strings.Repeat("a", 201), realtimev1.ConversationReadDetailType, readDetail()), says: "id must be 1 to 200"},
		"an id with a dot":           {event: eventOf("evt.1", realtimev1.ConversationReadDetailType, readDetail()), says: "id must be 1 to 200"},
		"an id a number":             {event: object{"id": 1, "type": realtimev1.ConversationReadDetailType, "detail": readDetail()}, says: "id must be a string, not a JSON number"},
		"no type":                    {event: object{"id": "evt_1", "detail": readDetail()}, says: "type must be realtime.message.posted.v1 or realtime.conversation.read.v1"},
		"a completed purge":          {event: eventOf("evt_1", realtimev1.PurgeCompletedDetailType, readDetail()), says: "type must be"},
		"another service's type":     {event: eventOf("evt_1", "agents.interaction.requested.v1", readDetail()), says: "type must be"},
		"no detail":                  {event: object{"id": "evt_1", "type": realtimev1.ConversationReadDetailType}, says: "detail is required"},
		"a null detail":              {event: eventOf("evt_1", realtimev1.ConversationReadDetailType, nil), says: "detail is required"},
		"a detail not an object":     {event: eventOf("evt_1", realtimev1.ConversationReadDetailType, []any{}), says: "detail: it is a JSON array, not an object"},
		"no organization":            {event: posted("organization_id", remove), says: "organization_id is required"},
		"no conversation":            {event: posted("conversation", remove), says: "conversation is required"},
		"a conversation of no kind":  {event: posted("conversation.kind", remove), says: "conversation.kind must be direct or side"},
		"a group conversation":       {event: posted("conversation.kind", "group"), says: "conversation.kind must be direct or side"},
		"a side id of three":         {event: posted("conversation.id", "org_1~user-a~user-b"), says: "{organization}~{teamspace}~{person}~{person}"},
		"a direct id of four":        {event: direct("org_1~tsp_1~user-a~user-b"), says: "{organization}~{person}~{person}"},
		"a direct in a teamspace":    {event: posted("conversation.kind", "direct"), says: "conversation.id must be {organization}~{person}~{person}"},
		"a direct with a teamspace":  {event: with(direct("org_1~user-a~user-b"), "detail.conversation.teamspace_id", "tsp_1"), says: "teamspace_id is only for a side conversation"},
		"a side without a teamspace": {event: posted("conversation.teamspace_id", remove), says: "teamspace_id is required for a side conversation"},
		"a side in another teamspace": {event: posted("conversation.teamspace_id", "tsp_2"),
			says: "teamspace_id must be the teamspace conversation.id names"},
		"an empty id in a conversation": {event: direct("org_1~~user-b"), says: "ids must each be 1 to 128"},
		"a space in a conversation id":  {event: direct("org_1~user a~user-b"), says: "ids must each be 1 to 128"},
		"an id of 129 in a conversation": {event: direct("org_1~" + strings.Repeat("a", 129) + "~user-b"),
			says: "ids must each be 1 to 128"},
		"another organization's conversation": {event: posted("organization_id", "org_2"), says: "conversation.id must start with organization_id"},
		"people out of order":                 {event: direct("org_1~user-b~user-a"), says: "two different people, sorted"},
		"one person twice":                    {event: direct("org_1~user-a~user-a"), says: "two different people, sorted"},
		"no participants":                     {event: posted("participant_user_ids", remove), says: "participant_user_ids must be the conversation's two people"},
		"one participant":                     {event: posted("participant_user_ids", []any{"user-a"}), says: "participant_user_ids must be"},
		"three participants":                  {event: posted("participant_user_ids", []any{"user-a", "user-b", "user-c"}), says: "participant_user_ids must be"},
		"participants out of order":           {event: posted("participant_user_ids", []any{"user-b", "user-a"}), says: "participant_user_ids must be"},
		"a participant from elsewhere":        {event: posted("participant_user_ids", []any{"user-a", "user-c"}), says: "participant_user_ids must be"},
		"participants not strings":            {event: posted("participant_user_ids", []any{1, 2}), says: "participant_user_ids.0 must be a string, not a JSON number"},
		"no message":                          {event: posted("message", remove), says: "message is required"},
		"a message without an id":             {event: posted("message.id", remove), says: "message.id must be 1 to 200 printable ASCII characters"},
		"a message id with a space":           {event: posted("message.id", "sdi 1"), says: "message.id must be"},
		"a message id beyond ASCII":           {event: posted("message.id", "sdi_é"), says: "message.id must be"},
		"a message without its seq":           {event: posted("message.seq", remove), says: "message.seq must be at least 1"},
		"a message at seq 0":                  {event: posted("message.seq", 0), says: "message.seq must be at least 1"},
		"a message at a negative seq":         {event: posted("message.seq", -1), says: "message.seq must be at least 1"},
		"a seq with a fraction":               {event: posted("message.seq", 1.5), says: "message.seq must be a whole number, not a JSON number"},
		"a seq as a string":                   {event: posted("message.seq", "1"), says: "message.seq must be a whole number, not a JSON string"},
		"a seq beyond int64":                  {body: `{"events":[{"id":"evt_1","type":"realtime.conversation.read.v1","detail":{"read_through_seq":9223372036854775808}}]}`, says: "read_through_seq must be a whole number"},
		"no author":                           {event: posted("author", remove), says: "author is required"},
		"an author of no kind":                {event: posted("author.kind", remove), says: "author.kind must be member or agent"},
		"an author of another kind":           {event: posted("author.kind", "system"), says: "author.kind must be member or agent"},
		"an author from elsewhere":            {event: posted("author.user_id", "user-c"), says: "author.user_id must be one of the conversation's two people"},
		"a member with an agent":              {event: posted("author.kind", "member"), says: "author.agent_id is only for an agent's message"},
		"an agent with none":                  {event: posted("author.agent_id", remove), says: "author.agent_id is required for an agent's message"},
		"no posted_at":                        {event: posted("posted_at", remove), says: "posted_at must be an RFC 3339 time in UTC"},
		"a posted_at with a space":            {event: posted("posted_at", "2026-09-24 10:00:00.000Z"), says: "posted_at must be an RFC 3339 time in UTC"},
		"a posted_at with an offset":          {event: posted("posted_at", "2026-09-24T15:30:00.000+05:30"), says: "posted_at must be an RFC 3339 time in UTC"},
		"a posted_at at +00:00":               {event: posted("posted_at", "2026-09-24T10:00:00.000+00:00"), says: "posted_at must be an RFC 3339 time in UTC"},
		"a posted_at in lower case":           {event: posted("posted_at", "2026-09-24t10:00:00.000z"), says: "posted_at must be an RFC 3339 time in UTC"},
		"a posted_at of ten digits":           {event: posted("posted_at", "2026-09-24T10:00:00.1234567890Z"), says: "posted_at must be an RFC 3339 time in UTC"},
		"a posted_at with an empty fraction":  {event: posted("posted_at", "2026-09-24T10:00:00.Z"), says: "posted_at must be an RFC 3339 time in UTC"},
		"a posted_at in month 13":             {event: posted("posted_at", "2026-13-24T10:00:00.000Z"), says: "posted_at must be an RFC 3339 time in UTC"},
		"a posted_at in ms":                   {event: posted("posted_at", 1790244000000), says: "posted_at must be a string, not a JSON number"},
		"a posted_at before 2020":             {event: posted("posted_at", "2019-12-31T23:59:59.999Z"), says: "posted_at is before 2020"},
		"a posted_at a day ahead":             {event: posted("posted_at", "2026-09-25T11:00:00.001Z"), says: "posted_at is more than a day ahead"},
		"a reader from elsewhere":             {event: read("reader_user_id", "user-c"), says: "reader_user_id must be one of the conversation's two people"},
		"no reader":                           {event: read("reader_user_id", remove), says: "reader_user_id must be one of"},
		"read through 0":                      {event: read("read_through_seq", 0), says: "read_through_seq must be at least 1"},
		"no read_through_seq":                 {event: read("read_through_seq", remove), says: "read_through_seq must be at least 1"},
		"no read_at":                          {event: read("read_at", remove), says: "read_at must be an RFC 3339 time in UTC"},
		"a read in another conversation kind": {event: read("conversation.kind", "side"), says: "{organization}~{teamspace}~{person}~{person}"},
	}
	for name, test := range tests {
		body := []byte(test.body)
		if test.body == "" {
			body = bodyOf(t, test.event)
		}
		events, err := decode(body, now)
		if err == nil || !strings.Contains(err.Error(), test.says) || errors.Is(err, errTooMany) {
			t.Errorf("%s: decode() = %d events, %v; want it refused, saying %q", name, len(events), err, test.says)
		}
	}
}

// A refusal names the event it is about, by its place and, when it has one
// that can be shown, its id.
func TestDecodeNamesTheEventItRefuses(t *testing.T) {
	for body, want := range map[string]string{
		`{"events":[{"id":"evt_ok","type":"realtime.conversation.read.v1","detail":{}},{}]}`:                          "events[0] (evt_ok): detail: organization_id is required",
		`{"events":[{"id":"evt one","type":"x"}]}`:                                                                    "events[0]: id must be",
		`{"events":[` + strings.TrimSuffix(strings.TrimPrefix(batchOf(2), `{"events":[`), `]}`) + `,{"id":"evt_3"}]}`: "events[2] (evt_3): type must be",
	} {
		if _, err := decode([]byte(body), now); err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("decode(%s) = %v, want it to start %q", body, err, want)
		}
	}
}
