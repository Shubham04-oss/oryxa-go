package slackrouter

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// maxQuestionRunes is the most of a message that becomes the question.
	// interactions holds a request's task to ten thousand; a Slack message
	// may be longer still, and the owner reads it in a notice.
	maxQuestionRunes = 4_000
	// maxNameRunes is the most of a person's name that goes into a message
	// about them.
	maxNameRunes = 40
	// unknownName stands for a person Slack could not name.
	unknownName = "someone"
)

var (
	// userMention is how Slack writes a mention in a message's text:
	// <@U123>, or <@U123|name> in older messages. Enterprise users' ids start
	// with W.
	userMention = regexp.MustCompile(`<@([UW][A-Z0-9]+)(?:\|[^<>]*)?>`)
	// token is any of Slack's angle-bracket forms: a mention, a channel, a
	// broadcast, a link.
	token          = regexp.MustCompile(`<([^<>]*)>`)
	blankLineRuns  = regexp.MustCompile(`\n{3,}`)
	slackEscapings = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&")
)

// mentionedUsers returns the distinct Slack users a message's text mentions,
// in the order it first mentions them. Only <@…> mentions count: a user
// group, @channel and @here name no one.
func mentionedUsers(text string) []string {
	seen := map[string]bool{}
	var users []string
	for _, match := range userMention.FindAllStringSubmatch(text, -1) {
		if !seen[match[1]] {
			seen[match[1]] = true
			users = append(users, match[1])
		}
	}
	return users
}

// cleanQuestion turns a message's text into the question asked: the mention
// of the bot is dropped, the other mentions, channels and links are written as
// the words a person would read, and Slack's escapes are undone. names says
// what to call each mentioned user.
//
// What comes out is plain text with nothing in it that is Slack's syntax: it
// travels to the owner's agent and into a notice apps renders, and neither
// needs to know that <@U123> is a person.
func cleanQuestion(text, botUserID string, names map[string]string) string {
	text = strings.ToValidUTF8(text, "")
	text = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			return r
		}
		return -1
	}, text)
	text = token.ReplaceAllStringFunc(text, func(match string) string {
		return plainToken(match[1:len(match)-1], botUserID, names)
	})
	text = slackEscapings.Replace(text)
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		lines[index] = strings.Join(strings.Fields(line), " ")
	}
	text = blankLineRuns.ReplaceAllString(strings.TrimSpace(strings.Join(lines, "\n")), "\n\n")
	if utf8.RuneCountInString(text) > maxQuestionRunes {
		text = strings.TrimSpace(string([]rune(text)[:maxQuestionRunes])) + "…"
	}
	return text
}

// plainToken writes one of Slack's angle-bracket forms, without its brackets,
// as text.
func plainToken(inner, botUserID string, names map[string]string) string {
	switch {
	case strings.HasPrefix(inner, "@"):
		id, _, _ := strings.Cut(inner[1:], "|")
		if id == botUserID {
			return ""
		}
		if name := names[id]; name != "" {
			return "@" + name
		}
		return "@" + unknownName
	case strings.HasPrefix(inner, "#"):
		if _, label, found := strings.Cut(inner[1:], "|"); found && label != "" {
			return "#" + label
		}
		return "#channel"
	case strings.HasPrefix(inner, "!"):
		target, label, found := strings.Cut(inner[1:], "|")
		if found && label != "" {
			return label
		}
		switch target {
		case "here", "channel", "everyone":
			return "@" + target
		}
		return ""
	}
	target, label, found := strings.Cut(inner, "|")
	switch {
	case !found || label == "":
		return strings.TrimPrefix(target, "mailto:")
	case label == strings.TrimPrefix(target, "mailto:"):
		return label
	default:
		return label + " (" + target + ")"
	}
}

// cleanName makes a name from Slack fit to go into a message about someone.
// It is a person's own choice of words, so it can carry anything; the markup
// Slack reads in a message (<, > and & begin every form of it) is removed, and
// the rest is shortened and put on one line. apps escapes it again when it
// renders, so this is the second lock, not the only one.
func cleanName(name string) string {
	name = strings.Map(func(r rune) rune {
		switch {
		case r == '<', r == '>', r == '&', r == '|':
			return -1
		case unicode.Is(unicode.Cf, r):
			// Invisible formatting, such as the marks that reverse the
			// direction of the text after them.
			return -1
		case unicode.IsControl(r):
			return ' '
		}
		return r
	}, strings.ToValidUTF8(name, ""))
	name = strings.Join(strings.Fields(name), " ")
	if utf8.RuneCountInString(name) > maxNameRunes {
		name = strings.TrimSpace(string([]rune(name)[:maxNameRunes]))
	}
	if name == "" {
		return unknownName
	}
	return name
}

// nameOf is what to call a person Slack described.
func nameOf(profile Profile) string {
	if name := cleanName(profile.DisplayName); name != unknownName {
		return name
	}
	return cleanName(profile.RealName)
}
