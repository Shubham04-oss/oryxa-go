// Command openapi-slice splits a provider's OpenAPI 3 description into one
// description per tag, so a Go client is generated per API area instead of
// for the whole API. Each slice holds the tag's operations and every component
// they reference.
//
//	openapi-slice -spec api.json -tags actions,repos,orgs -out dir
//
// writes dir/actions/openapi.json, dir/repos/openapi.json and
// dir/orgs/openapi.json. -spec is a file path or an https URL, read once; only
// JSON descriptions are read.
//
// Within a slice, an operationId that starts with the tag and a slash loses
// that prefix, and any other slash becomes a hyphen, so generated names read
// repos.Get rather than repos.Reposget. The original id stays in
// x-original-operation-id. Each slice is then adjusted so oapi-codegen
// generates a client from it that compiles, without changing what the client
// sends or accepts; normalize lists the adjustments.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxSpecBytes = 64 << 20
	fetchTimeout = 2 * time.Minute
	sliceFile    = "openapi.json"
)

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "openapi-slice:", err)
		os.Exit(1)
	}
}

func run(args []string, log io.Writer) error {
	flags := flag.NewFlagSet("openapi-slice", flag.ContinueOnError)
	flags.SetOutput(log)
	spec := flags.String("spec", "", "OpenAPI 3 JSON description: a file path or an https URL")
	tags := flags.String("tags", "", "comma-separated tags; each becomes one slice")
	out := flags.String("out", "", "directory to write each slice under, as <package>/"+sliceFile)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *spec == "" || *tags == "" || *out == "" {
		return errors.New("-spec, -tags and -out are required")
	}
	raw, err := read(*spec)
	if err != nil {
		return err
	}
	var document map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	// Numbers stay as written, so examples and limits are not rounded.
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("decode %s as JSON: %w", *spec, err)
	}
	for _, tag := range strings.Split(*tags, ",") {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		pkg, err := packageName(tag)
		if err != nil {
			return err
		}
		slice, err := Slice(document, tag)
		if err != nil {
			return err
		}
		if err := write(filepath.Join(*out, pkg, sliceFile), slice); err != nil {
			return err
		}
	}
	return nil
}

// read returns a description from a file, or from an https URL.
func read(location string) ([]byte, error) {
	if !strings.HasPrefix(location, "https://") {
		raw, err := os.ReadFile(location)
		if err != nil {
			return nil, fmt.Errorf("read description: %w", err)
		}
		return raw, nil
	}
	client := http.Client{Timeout: fetchTimeout}
	response, err := client.Get(location)
	if err != nil {
		return nil, fmt.Errorf("fetch description: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch description: %s answered %d", location, response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxSpecBytes+1))
	if err != nil {
		return nil, fmt.Errorf("fetch description: %w", err)
	}
	if len(raw) > maxSpecBytes {
		return nil, fmt.Errorf("description is larger than %d MiB", maxSpecBytes>>20)
	}
	return raw, nil
}

func write(path string, slice map[string]any) error {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	// Descriptions are Markdown; keep their < > & readable.
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(slice); err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, encoded.Bytes(), 0o644)
}

// packageName is the Go package a tag's slice is generated into: the tag's
// letters and digits, lowercased.
func packageName(tag string) (string, error) {
	var name strings.Builder
	for _, character := range strings.ToLower(tag) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			name.WriteRune(character)
		}
	}
	if name.Len() == 0 || name.String()[0] >= '0' && name.String()[0] <= '9' {
		return "", fmt.Errorf("tag %q does not make a Go package name", tag)
	}
	return name.String(), nil
}
