package spec

import (
	"reflect"
	"strings"
	"testing"
)

func TestSettingCatalogCoversEveryPolicyLeaf(t *testing.T) {
	paths := map[string]bool{}
	var walk func(reflect.Type, string)
	walk = func(kind reflect.Type, path string) {
		for kind.Kind() == reflect.Pointer {
			kind = kind.Elem()
		}
		if kind.Kind() != reflect.Struct {
			paths[path] = true
			return
		}
		for index := 0; index < kind.NumField(); index++ {
			field := kind.Field(index)
			walk(field.Type, path+"."+strings.Split(field.Tag.Get("yaml"), ",")[0])
		}
	}
	for path, kind := range map[string]reflect.Type{"rules": reflect.TypeOf(Rules{}), "safety": reflect.TypeOf(Safety{}), "tools": reflect.TypeOf(Tools{}), "search": reflect.TypeOf(Search{}), "agents.personal": reflect.TypeOf(PersonalAgent{})} {
		walk(kind, path)
	}
	for _, setting := range SettingsCatalog() {
		if !paths[setting.Path] {
			t.Fatalf("catalog contains missing or duplicate policy field %s", setting.Path)
		}
		delete(paths, setting.Path)
	}
	if len(paths) != 0 {
		t.Fatalf("new policies need catalog metadata: %v", paths)
	}
}
func TestUnsetRequiresKnownUniqueUnsubmittedSetting(t *testing.T) {
	for _, input := range []string{"unset: [tools.unknown]", "unset: [tools.offer, tools.offer]", "unset: [tools.offer]\ntools: {offer: []}"} {
		if _, err := Parse([]byte(example + input)); err == nil {
			t.Fatalf("invalid unset accepted: %s", input)
		}
	}
	document, err := Parse([]byte(example + "unset: [tools.offer, rules.consent.expireAfter, tools.wakeUpWithin]"))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := document.YAML()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(encoded)
	if err != nil || !reflect.DeepEqual(parsed.Unset, document.Unset) {
		t.Fatalf("unset did not round trip: %v", err)
	}
}
