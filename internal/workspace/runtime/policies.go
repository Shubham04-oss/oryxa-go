package runtime

import (
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
	"reflect"
	"slices"
)

// MergePolicies uses the shared catalog so introducing a policy field cannot
// silently drop it when another policy is changed. Unset resets only named fields.
func MergePolicies(previous, submitted spec.Document, replace bool) (spec.Document, Plan, error) {
	if err := submitted.Validate(); err != nil {
		return spec.Document{}, Plan{}, err
	}
	effective := submitted.Clone()
	effective.Unset = nil
	plan := Plan{Changes: []Change{}, NeedsHuman: []HumanStep{}}
	for _, setting := range spec.SettingsCatalog() {
		old, oldPresent := setting.Get(previous)
		next, nextPresent := setting.Get(submitted)
		reset := slices.Contains(submitted.Unset, setting.Path)
		if reset {
			next, nextPresent = nil, false
		} else if !replace && !nextPresent {
			next, nextPresent = old, oldPresent
		}
		if setting.Path == "safety.guard.holdForReview" && !replace && !reset && !nextPresent && submitted.Preset == "regulated" {
			next, nextPresent = true, true
		}
		if nextPresent {
			setting.Set(&effective, next)
		} else {
			setting.Set(&effective, nil)
		}
		if oldPresent != nextPresent || !reflect.DeepEqual(old, next) {
			plan.Changes = append(plan.Changes, Change{Op: "update", Kind: "policy", Name: setting.Path})
		}
	}
	return effective, plan, effective.Validate()
}
