package spec

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
)

// Setting is the shared schema for policy validation, merging, plans, explicit
// reset, and operational projection. Projection names a projection.Settings field.
type Setting struct {
	Path, Kind, Projection string
	Values                 []string
	Min, Max               time.Duration
	Message                string
}

var settingsCatalog = []Setting{
	{Path: "rules.consent.expireAfter", Kind: "duration", Projection: "ExpireAfter", Min: 5 * time.Minute, Max: 7 * 24 * time.Hour},
	{Path: "safety.guard.holdForReview", Kind: "bool", Projection: "HoldForReview"},
	{Path: "safety.guard.pii.minimum.owner", Kind: "enum", Projection: "PIIMinimumOwner", Values: []string{"secrets", "strict"}},
	{Path: "safety.guard.pii.minimum.peer", Kind: "enum", Projection: "PIIMinimumPeer", Values: []string{"secrets", "strict"}},
	{Path: "safety.guard.relevance", Kind: "enum", Projection: "GuardRelevance", Values: []string{"hold", "shadow", "off"}},
	{Path: "agents.personal.proactive", Kind: "enum", Projection: "Proactive", Values: []string{"on", "off"}},
	{Path: "tools.provider", Kind: "enum", Values: []string{"composio"}},
	{Path: "tools.offer", Kind: "toolkits", Projection: "Offer"},
	{Path: "tools.employeeConnections", Kind: "bool", Projection: "EmployeeConnections"},
	{Path: "tools.autoSubscribe", Kind: "bool", Projection: "AutoSubscribe"},
	{Path: "tools.wakeUpWithin", Kind: "duration", Projection: "WakeUpWithin", Min: time.Minute, Max: 90 * time.Minute},
	{Path: "search.mode", Kind: "enum", Projection: "SearchMode", Values: []string{"native", "none"}, Message: "search.mode must be native or none; external search is not yet supported"},
}

func SettingsCatalog() []Setting { return slices.Clone(settingsCatalog) }

func (setting Setting) Get(document Document) (any, bool) {
	value := reflect.ValueOf(document)
	for _, name := range strings.Split(setting.Path, ".") {
		for value.Kind() == reflect.Pointer {
			if value.IsNil() {
				return nil, false
			}
			value = value.Elem()
		}
		value = taggedField(value, name)
		if !value.IsValid() {
			return nil, false
		}
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil, false
		}
		value = value.Elem()
	} else if value.Kind() == reflect.String && value.String() == "" {
		return nil, false
	}
	return cloneValue(value).Interface(), true
}
func (setting Setting) Set(document *Document, value any) {
	setSetting(reflect.ValueOf(document).Elem(), strings.Split(setting.Path, "."), value)
}
func taggedField(value reflect.Value, name string) reflect.Value {
	if value.Kind() != reflect.Struct {
		return reflect.Value{}
	}
	for index := 0; index < value.NumField(); index++ {
		if strings.Split(value.Type().Field(index).Tag.Get("yaml"), ",")[0] == name {
			return value.Field(index)
		}
	}
	return reflect.Value{}
}
func setSetting(value reflect.Value, path []string, input any) {
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			if input == nil {
				return
			}
			value.Set(reflect.New(value.Type().Elem()))
		}
		setSetting(value.Elem(), path, input)
		if value.Elem().IsZero() {
			value.SetZero()
		}
		return
	}
	field := taggedField(value, path[0])
	if !field.IsValid() {
		panic("unknown catalog setting " + strings.Join(path, "."))
	}
	if len(path) > 1 {
		setSetting(field, path[1:], input)
		return
	}
	if input == nil {
		field.SetZero()
		return
	}
	next := cloneValue(reflect.ValueOf(input))
	if field.Kind() == reflect.Pointer {
		field.Set(reflect.New(field.Type().Elem()))
		field.Elem().Set(next)
	} else {
		field.Set(next)
	}
}
func cloneValue(value reflect.Value) reflect.Value {
	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		next := reflect.New(value.Type().Elem())
		next.Elem().Set(cloneValue(value.Elem()))
		return next
	case reflect.Struct:
		next := reflect.New(value.Type()).Elem()
		for index := 0; index < value.NumField(); index++ {
			if next.Field(index).CanSet() {
				next.Field(index).Set(cloneValue(value.Field(index)))
			}
		}
		return next
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		next := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := 0; index < value.Len(); index++ {
			next.Index(index).Set(cloneValue(value.Index(index)))
		}
		return next
	default:
		return value
	}
}
func (document Document) Clone() Document {
	return cloneValue(reflect.ValueOf(document)).Interface().(Document)
}

func (document Document) validateSettings() error {
	available := map[string]Setting{}
	for _, setting := range settingsCatalog {
		available[setting.Path] = setting
		value, present := setting.Get(document)
		if !present {
			continue
		}
		switch setting.Kind {
		case "enum":
			if !slices.Contains(setting.Values, value.(string)) {
				if setting.Message != "" {
					return fmt.Errorf("%s", setting.Message)
				}
				return fmt.Errorf("%s must be %s", setting.Path, strings.Join(setting.Values, " or "))
			}
		case "duration":
			duration, err := ParseDuration(value.(string))
			if err != nil || duration < setting.Min || duration > setting.Max {
				return fmt.Errorf("%s is outside its supported duration range", setting.Path)
			}
		case "toolkits":
			seen := map[string]bool{}
			for _, toolkit := range value.([]string) {
				if !toolkitPattern.MatchString(toolkit) || seen[toolkit] {
					return fmt.Errorf("tools.offer must contain unique lowercase toolkit slugs")
				}
				seen[toolkit] = true
			}
		}
	}
	if document.Search != nil && document.Search.Mode == "" {
		return fmt.Errorf("search.mode must be native or none")
	}
	seen := map[string]bool{}
	for _, path := range document.Unset {
		setting, ok := available[path]
		if !ok {
			return fmt.Errorf("unset contains an unsupported setting path")
		}
		if seen[path] {
			return fmt.Errorf("unset contains a duplicate setting path")
		}
		seen[path] = true
		if _, present := setting.Get(document); present {
			return fmt.Errorf("%s cannot be both set and unset", path)
		}
	}
	return nil
}
