package spec

import (
	"bytes"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/internal/cleantext"
	"go.yaml.in/yaml/v3"
)

// ParseStored decodes persisted versions without applying today's write
// constraints. A historical value must remain readable so a submitted change
// can repair or unset it. The merged effective document is still validated
// before mutation. Submitted documents must use Parse.
func ParseStored(data []byte) (Document, error) {
	if len(data) > 1<<20 {
		return Document{}, fmt.Errorf("workspace file exceeds 1 MiB")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var root, extra yaml.Node
	if err := decoder.Decode(&root); err != nil {
		return Document{}, fmt.Errorf("read stored workspace: invalid YAML")
	}
	if err := decoder.Decode(&extra); err != io.EOF {
		return Document{}, fmt.Errorf("stored workspace must contain one document")
	}
	if err := rejectAliases(&root); err != nil {
		return Document{}, err
	}
	if len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return Document{}, fmt.Errorf("stored workspace must be a mapping")
	}
	if err := cleantext.Check(&root); err != nil {
		return Document{}, fmt.Errorf("stored workspace text is invalid: %w", err)
	}
	filterStoredNode(root.Content[0], reflect.TypeOf(Document{}))
	var document Document
	if err := root.Decode(&document); err != nil {
		return Document{}, fmt.Errorf("stored workspace contains an invalid field type")
	}
	return document.Normalize(), nil
}

func filterStoredNode(node *yaml.Node, kind reflect.Type) {
	for kind.Kind() == reflect.Pointer {
		kind = kind.Elem()
	}
	if node.Kind == yaml.SequenceNode {
		if kind == reflect.TypeOf(Teams{}) {
			kind = reflect.TypeOf([]Team{})
		}
		if kind == reflect.TypeOf(Ownership{}) {
			kind = reflect.TypeOf([]OwnerHint{})
		}
		if kind.Kind() == reflect.Slice {
			for _, child := range node.Content {
				filterStoredNode(child, kind.Elem())
			}
		}
		return
	}
	if node.Kind != yaml.MappingNode || kind.Kind() != reflect.Struct || kind == reflect.TypeOf(yaml.Node{}) {
		return
	}
	fields := map[string]reflect.Type{}
	for index := 0; index < kind.NumField(); index++ {
		field := kind.Field(index)
		name := strings.Split(field.Tag.Get("yaml"), ",")[0]
		if name != "" && name != "-" && field.Type != reflect.TypeOf((*yaml.Node)(nil)) {
			fields[name] = field.Type
		}
	}
	content := make([]*yaml.Node, 0, len(node.Content))
	for index := 0; index+1 < len(node.Content); index += 2 {
		field, known := fields[node.Content[index].Value]
		if !known {
			continue
		}
		filterStoredNode(node.Content[index+1], field)
		content = append(content, node.Content[index], node.Content[index+1])
	}
	node.Content = content
}
