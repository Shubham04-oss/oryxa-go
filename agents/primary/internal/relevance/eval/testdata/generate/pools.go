package main

import (
	"sort"
	"strings"
)

// person is somebody in a template, with the pronouns the sentence around
// them needs.
type person struct{ name, he, He, his, him string }

// people alternate, so any run of them mixes he and she. The names are the
// kind a startup's Slack has in it.
var people = []person{
	{"Maya", "she", "She", "her", "her"},
	{"Rahul", "he", "He", "his", "him"},
	{"Priya", "she", "She", "her", "her"},
	{"Arjun", "he", "He", "his", "him"},
	{"Neha", "she", "She", "her", "her"},
	{"Sam", "he", "He", "his", "him"},
	{"Isha", "she", "She", "her", "her"},
	{"Dev", "he", "He", "his", "him"},
	{"Meera", "she", "She", "her", "her"},
	{"Karan", "he", "He", "his", "him"},
	{"Kavya", "she", "She", "her", "her"},
	{"Vikram", "he", "He", "his", "him"},
	{"Ananya", "she", "She", "her", "her"},
	{"Rohan", "he", "He", "his", "him"},
	{"Lena", "she", "She", "her", "her"},
	{"Marcus", "he", "He", "his", "him"},
	{"Aisha", "she", "She", "her", "her"},
	{"Tariq", "he", "He", "his", "him"},
	{"Sofia", "she", "She", "her", "her"},
	{"Jordan", "he", "He", "his", "him"},
}

// projects are named after the work; a template says "the {proj}".
var projects = []string{
	"billing migration", "Q3 launch", "onboarding revamp", "SOC 2 audit", "mobile app v2",
	"pricing page redesign", "data pipeline rewrite", "analytics pilot", "Series A data room",
	"checkout latency fix", "Postgres upgrade", "SSO rollout", "referral program", "API v3 release",
	"docs overhaul", "Slack integration",
}

var customers = []string{
	"Acme Corp", "Globex", "Initech", "Umbrella Logistics", "Hooli", "Stark Retail", "Wayne Freight",
	"Northwind", "Contoso", "Vandelay Imports",
}

var vendors = []string{"AWS", "Datadog", "Notion", "Figma", "Linear", "Vercel", "Stripe", "Sentry", "Segment", "Zendesk"}

var days = []string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday"}

var slots = []string{"2-3pm", "10:30-11:30am", "9-9:30am", "4-5pm", "1:30-2:30pm", "11am-12pm", "3-3:45pm", "5-5:30pm"}

var alternatives = []string{"3:30-4:30pm", "after 4pm", "at 11am", "before 10am", "at 1pm", "after lunch"}

var dates = []string{"Oct 12", "Nov 3", "Oct 28", "Dec 1", "Nov 15", "Oct 20", "Jan 9", "Nov 22", "Oct 6", "Dec 12"}

var amounts = []string{"$14,320", "$2,000", "$48,000", "$12,400", "$7,850", "$310,000", "$96,000", "$3,450", "$21,700", "$5,600"}

var percents = []string{"80%", "65%", "90%", "40%", "72%", "55%", "85%"}

// Amounts by what they are, so a figure is a size such a thing has: a vendor
// bill is not a burn rate.
var (
	spends    = []string{"$1,420", "$3,900", "$6,750", "$11,200", "$18,300"}
	burns     = []string{"$198,000", "$245,000", "$172,000", "$310,000", "$221,000"}
	monthlies = []string{"$2,000", "$3,450", "$5,600", "$7,850", "$12,500"}
	invoices  = []string{"$12,400", "$21,700", "$48,000", "$9,600", "$36,000"}
	smalls    = []string{"$4,350", "$6,200", "$7,850", "$9,100", "$11,400"}
	mids      = []string{"$18,400", "$27,900", "$34,200", "$41,500", "$22,600"}
	bumps     = []string{"$180", "$340", "$420", "$650", "$900"}
	credits   = []string{"$800", "$1,200", "$2,000", "$3,500", "$5,000"}
	annuals   = []string{"$9,600", "$12,000", "$18,000", "$24,000", "$36,000"}
	deals     = []string{"$36,000", "$62,000", "$84,000", "$120,000", "$48,000"}
	pipelines = []string{"$310,000", "$420,000", "$185,000", "$260,000", "$540,000"}
)

// vars are one variation's values, filled into a template's {placeholders}.
type vars map[string]string

// fill replaces every {placeholder} in a template.
func (v vars) fill(template string) string {
	keys := make([]string, 0, len(v))
	for key := range v {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys)*2)
	for _, key := range keys {
		pairs = append(pairs, "{"+key+"}", v[key])
	}
	return strings.NewReplacer(pairs...).Replace(template)
}

// at is the element of list for the k'th instance of a scenario: it steps
// through the list by stride, and steps one further each time k has gone once
// round it, so that two instances a whole list apart do not choose alike.
func at[T any](list []T, k, stride, offset int) T {
	return list[(k*stride+k/len(list)+offset)%len(list)]
}

// variation is the values of the k'th instance of a scenario. salt keeps two
// scenarios that meet at the same k from choosing the same things.
func variation(k, salt int) vars {
	n := at(people, k, 7, salt*3)
	n2 := at(people, k, 11, salt*5+4)
	for n2.name == n.name {
		n2 = people[(indexOf(n2)+1)%len(people)]
	}
	n3 := at(people, k, 13, salt*7+9)
	for n3.name == n.name || n3.name == n2.name {
		n3 = people[(indexOf(n3)+1)%len(people)]
	}
	proj := at(projects, k, 5, salt*3)
	proj2 := at(projects, k, 7, salt*5+3)
	if proj2 == proj {
		proj2 = at(projects, k, 7, salt*5+4)
	}
	day := at(days, k, 2, salt)
	day2 := at(days, k, 3, salt+2)
	if day2 == day {
		day2 = at(days, k, 3, salt+3)
	}
	return vars{
		"n": n.name, "he": n.he, "He": n.He, "his": n.his, "him": n.him,
		"n2": n2.name, "he2": n2.he, "his2": n2.his, "him2": n2.him,
		"n3": n3.name, "he3": n3.he, "his3": n3.his,
		"proj": proj, "Proj": capitalize(proj), "proj2": proj2,
		"cust": at(customers, k, 3, salt*7), "cust2": at(customers, k, 3, salt*7+4),
		"vendor": at(vendors, k, 7, salt),
		"day":    day, "day2": day2,
		"slot": at(slots, k, 3, salt), "alt": at(alternatives, k, 5, salt),
		"date": at(dates, k, 7, salt*2), "date2": at(dates, k, 7, salt*2+3),
		"amt": at(amounts, k, 3, salt*5), "amt2": at(amounts, k, 3, salt*5+4),
		"spend": at(spends, k, 3, salt), "burn": at(burns, k, 3, salt*2), "mrr": at(monthlies, k, 3, salt*3),
		"inv": at(invoices, k, 3, salt*4), "small": at(smalls, k, 3, salt*5), "mid": at(mids, k, 3, salt*6),
		"bump": at(bumps, k, 3, salt*7), "credit": at(credits, k, 3, salt*8), "annual": at(annuals, k, 3, salt*9),
		"deal": at(deals, k, 3, salt*10), "pipeline": at(pipelines, k, 3, salt*11),
		"pct": at(percents, k, 3, salt),
	}
}

func indexOf(p person) int {
	for index, candidate := range people {
		if candidate.name == p.name {
			return index
		}
	}
	return 0
}

func capitalize(text string) string {
	if text == "" {
		return text
	}
	return strings.ToUpper(text[:1]) + text[1:]
}

// leakSubject are the values of the person a leaked sentence is about, kept
// apart from the answer's own people so that their pronouns do not mix.
func leakSubject(k, salt int) vars {
	p := at(people, k, 9, salt*4+2)
	q := at(people, k, 4, salt*6+7)
	if q.name == p.name {
		q = people[(indexOf(q)+1)%len(people)]
	}
	return vars{"p": p.name, "phe": p.he, "pHe": p.He, "phis": p.his, "phim": p.him, "q": q.name}
}

// merge is the union of value sets.
func merge(sets ...vars) vars {
	merged := vars{}
	for _, set := range sets {
		for key, value := range set {
			merged[key] = value
		}
	}
	return merged
}

// pick is list's element for k, wrapping.
func pick[T any](list []T, k int) T { return list[k%len(list)] }
