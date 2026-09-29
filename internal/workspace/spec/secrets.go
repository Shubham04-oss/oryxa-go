package spec

import (
	"fmt"
	"math"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

var credentialPattern = regexp.MustCompile(`(?:xox[baprs]-[A-Za-z0-9-]{8,}|ghp_[A-Za-z0-9]{8,}|sk_live_[A-Za-z0-9]{8,}|AKIA[0-9A-Z]{16}|eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,})`)
var opaqueCandidate = regexp.MustCompile(`[A-Za-z0-9_+/=-]{40,}`)
var ordinaryHexID = regexp.MustCompile(`^[a-fA-F0-9-]+$`)

// SecretError deliberately contains the field path, never the submitted value.
type SecretError struct{ Field string }

func (err *SecretError) Error() string {
	return "secret_in_manifest: " + err.Field + " must not contain credentials"
}

func scanManifestSecrets(node *yaml.Node, path string) error {
	switch node.Kind {
	case yaml.DocumentNode:
		for _, child := range node.Content {
			if err := scanManifestSecrets(child, path); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for index := 0; index+1 < len(node.Content); index += 2 {
			field := node.Content[index].Value
			// Unknown-key decoder errors quote field names. Inspect the key before
			// it can become part of either a diagnostic or a child field path.
			if credentialPattern.MatchString(field) {
				return &SecretError{Field: path + "[field-name]"}
			}
			if path != "" {
				field = path + "." + field
			}
			if err := scanManifestSecrets(node.Content[index+1], field); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for index, child := range node.Content {
			if err := scanManifestSecrets(child, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
		if node.Tag != "!!str" {
			return nil
		}
		if credentialPattern.MatchString(node.Value) {
			return &SecretError{Field: path}
		}
		// Stable external identifiers are opaque by design; detect known credential
		// formats there, but do not mistake a directory's random ID for a password.
		if strings.HasSuffix(path, ".externalId") {
			return nil
		}
		for _, candidate := range opaqueCandidate.FindAllString(node.Value, -1) {
			if !ordinaryHexID.MatchString(candidate) && credentialEntropy(candidate) >= 4.5 {
				return &SecretError{Field: path}
			}
		}
	}
	return nil
}

func credentialEntropy(value string) float64 {
	counts := map[rune]int{}
	for _, r := range value {
		counts[r]++
	}
	entropy := 0.0
	for _, count := range counts {
		p := float64(count) / float64(len(value))
		entropy -= p * math.Log2(p)
	}
	return entropy
}
