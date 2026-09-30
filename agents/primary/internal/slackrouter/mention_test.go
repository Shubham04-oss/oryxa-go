package slackrouter

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMentionedUsers(t *testing.T) {
	for _, test := range []struct {
		name, text string
		want       []string
	}{
		{"none", "no mentions here", nil},
		{"one", "<@U123> hi", []string{"U123"}},
		{"in order, once each", "<@U222> and <@U111> and <@U222> again", []string{"U222", "U111"}},
		{"the older form with a label", "<@U123|raya> and <@U123>", []string{"U123"}},
		{"enterprise ids", "<@W0ABC123> hi", []string{"W0ABC123"}},
		{"a user group is nobody", "<!subteam^S123|@eng> <!channel> <!here> <!everyone>", nil},
		{"a channel is nobody", "see <#C123|general>", nil},
		{"a link is nobody", "<https://example.com/@U123>", nil},
		{"escaped text is not a mention", "&lt;@U123&gt;", nil},
		{"lower case is not an id", "<@u123>", nil},
		{"too short to be an id", "<@U1> <@UA>", nil},
		{"too long to be an id", "<@U" + strings.Repeat("A", 40) + ">", nil},
		{"a bare at sign", "@raya", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := mentionedUsers(test.text); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("mentionedUsers(%q) = %v, want %v", test.text, got, test.want)
			}
		})
	}
}

func TestCleanQuestion(t *testing.T) {
	names := map[string]string{"URAYA": "Raya", "UMAYA": "Maya"}
	for _, test := range []struct {
		name, text, want string
	}{
		{"plain", "what is the Q3 number?", "what is the Q3 number?"},
		{"the bot's mention is dropped", "<@UBOT> what is it", "what is it"},
		{"the bot's labelled mention is dropped", "<@UBOT|oryxa> what is it", "what is it"},
		{"a mention becomes a name", "ask <@URAYA> and <@UMAYA|maya>", "ask @Raya and @Maya"},
		{"an unknown user is someone", "ask <@UZZZ>", "ask @someone"},
		{"a channel", "in <#C1|billing> and <#C2>", "in #billing and #channel"},
		{"broadcasts", "<!here> <!channel> <!everyone>", "@here @channel @everyone"},
		{"a user group", "<!subteam^S1|@eng> please", "@eng please"},
		{"a date", "by <!date^1700000000^{date}|Nov 14>", "by Nov 14"},
		{"a link", "<https://example.com/a>", "https://example.com/a"},
		{"a labelled link", "<https://example.com/a|the doc>", "the doc (https://example.com/a)"},
		{"a mail link", "<mailto:a@b.example|a@b.example>", "a@b.example"},
		{"a mail link without a label", "<mailto:a@b.example>", "a@b.example"},
		{"escapes", "a &amp; b &lt;c&gt;", "a & b <c>"},
		{"no double unescape", "&amp;lt;", "&lt;"},
		{"an unknown form is emptied", "a <!unknown> b", "a b"},
		{"runs of space", "a   b\t\tc", "a b c"},
		{"lines are kept, blank runs shortened", "a\n\n\n\n\nb\n c ", "a\n\nb\nc"},
		{"control characters go, and separators are spaces", "a\x00b\x07c\u2028d", "abc d"},
		{"invalid UTF-8 goes", "a\xffb", "ab"},
		{"only the bot", "<@UBOT>", ""},
		{"nothing", "   ", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := cleanQuestion(test.text, "UBOT", names); got != test.want {
				t.Fatalf("cleanQuestion(%q) = %q, want %q", test.text, got, test.want)
			}
		})
	}
}

func TestCleanQuestionIsBounded(t *testing.T) {
	got := cleanQuestion(strings.Repeat("é", maxQuestionRunes*2), "UBOT", nil)
	if utf8.RuneCountInString(got) != maxQuestionRunes+1 || !strings.HasSuffix(got, "…") || !utf8.ValidString(got) {
		t.Fatalf("got %d runes, valid %v", utf8.RuneCountInString(got), utf8.ValidString(got))
	}
	if short := cleanQuestion(strings.Repeat("a", maxQuestionRunes), "UBOT", nil); strings.HasSuffix(short, "…") {
		t.Fatal("a question at the limit was cut")
	}
}

func TestCleanName(t *testing.T) {
	for _, test := range []struct{ name, in, want string }{
		{"plain", "Raya", "Raya"},
		{"spaces", "  Raya   Rao ", "Raya Rao"},
		{"a link", "<https://phish.example|Click>", "https://phish.exampleClick"},
		{"a mention", "<@U123>", "@U123"},
		{"an ampersand", "R&D", "RD"},
		{"a pipe", "a|b", "ab"},
		{"a newline", "Raya\nRao", "Raya Rao"},
		{"a direction override", "Raya\u202egnp.exe", "Rayagnp.exe"},
		{"a zero-width space", "Ra\u200bya", "Raya"},
		{"empty", "", unknownName},
		{"only markup", "<>&|", unknownName},
		{"invalid UTF-8", "Ra\xffya", "Raya"},
		{"emoji stay", "Raya 🌵", "Raya 🌵"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := cleanName(test.in); got != test.want {
				t.Fatalf("cleanName(%q) = %q, want %q", test.in, got, test.want)
			}
		})
	}
	long := cleanName(strings.Repeat("x", 500))
	if utf8.RuneCountInString(long) != maxNameRunes {
		t.Fatalf("a long name is %d runes, want %d", utf8.RuneCountInString(long), maxNameRunes)
	}
}

func TestNameOf(t *testing.T) {
	for _, test := range []struct {
		profile Profile
		want    string
	}{
		{Profile{Name: "Raya"}, "Raya"},
		{Profile{Name: "  Raya   Rao "}, "Raya Rao"},
		{Profile{Name: "<>"}, unknownName},
		{Profile{}, unknownName},
	} {
		if got := nameOf(test.profile); got != test.want {
			t.Errorf("nameOf(%+v) = %q, want %q", test.profile, got, test.want)
		}
	}
}

func TestCleanTitleAndTeam(t *testing.T) {
	for _, test := range []struct{ name, in, wantTitle, wantTeam string }{
		{"plain", "Staff Engineer", "Staff Engineer", "Staff Engineer"},
		{"markup and a link", "<https://x.example|Boss> & co", "https://x.exampleBoss co", "https://x.exampleBoss co"},
		{"a newline and a tab", "Head of\n\tPeople", "Head of People", "Head of People"},
		{"a direction override", "CEO\u202e", "CEO", "CEO"},
		{"nothing", "", "", ""},
		{"only markup", "<>&|", "", ""},
		{"invalid UTF-8", "Ops\xff", "Ops", "Ops"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := cleanTitle(test.in); got != test.wantTitle {
				t.Errorf("cleanTitle(%q) = %q, want %q", test.in, got, test.wantTitle)
			}
			if got := cleanTeam(test.in); got != test.wantTeam {
				t.Errorf("cleanTeam(%q) = %q, want %q", test.in, got, test.wantTeam)
			}
		})
	}
	if got := utf8.RuneCountInString(cleanTitle(strings.Repeat("x", 500))); got != maxTitleRunes {
		t.Fatalf("a long title is %d runes, want %d", got, maxTitleRunes)
	}
	if got := utf8.RuneCountInString(cleanTeam(strings.Repeat("x", 500))); got != maxTeamRunes {
		t.Fatalf("a long team name is %d runes, want %d", got, maxTeamRunes)
	}
}
