package integration

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
)

// Registry is the integrations a build carries, by toolkit. Only a manifest
// that validates gets in, so everything that reads one here can rely on it.
//
// Being registered is not being in use: a toolkit takes the Oryxa route only
// once APPS_ORYXA_TOOLKITS names it.
type Registry struct {
	mu           sync.RWMutex
	integrations map[string]Integration
}

// NewRegistry answers an empty registry.
func NewRegistry() *Registry {
	return &Registry{integrations: map[string]Integration{}}
}

// Register files an integration under its manifest's toolkit. It refuses a
// manifest that does not validate, a toolkit already registered, and tools
// that do not match their functions one for one.
func (r *Registry) Register(i Integration) error {
	if i == nil {
		return errors.New("integration: registering a nil integration")
	}
	manifest := i.Manifest()
	if err := manifest.Validate(); err != nil {
		return fmt.Errorf("integration: registering: %w", err)
	}
	if err := matchTools(manifest, i.Tools()); err != nil {
		return fmt.Errorf("integration: registering %s: %w", manifest.Toolkit, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, taken := r.integrations[manifest.Toolkit]; taken {
		return fmt.Errorf("integration: %s is already registered", manifest.Toolkit)
	}
	r.integrations[manifest.Toolkit] = i
	return nil
}

// Lookup answers the integration registered for a toolkit.
func (r *Registry) Lookup(toolkit string) (Integration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	i, ok := r.integrations[toolkit]
	return i, ok
}

// Toolkits answers every registered toolkit, in order.
func (r *Registry) Toolkits() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Sorted(maps.Keys(r.integrations))
}

// matchTools checks that every tool the manifest declares has a function, and
// every function a declared tool, so the core never finds a tool it cannot
// run or runs one nobody described.
func matchTools(manifest Manifest, functions map[string]ToolFunc) error {
	declared := map[string]bool{}
	for _, tool := range manifest.Tools {
		declared[tool.Name] = true
		if functions[tool.Name] == nil {
			return fmt.Errorf("the tool %s has no function", tool.Name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(functions)) {
		if !declared[name] {
			return fmt.Errorf("a function is registered for %q, which the manifest does not declare", name)
		}
	}
	return nil
}
