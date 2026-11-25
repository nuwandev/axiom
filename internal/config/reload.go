package config

import (
	"reflect"
)

// reloadableFields are the only Config fields a running agent may change
// without a restart. Every other field, including any added later, is treated
// as restart-only, so a new setting can never be silently ignored by a reload.
var reloadableFields = map[string]bool{
	"Identities": true,
	"CAFile":     true,
	"CertFile":   true,
	"KeyFile":    true,
	"CRLFile":    true,
}

// NonReloadableChanges returns the names of the Config fields that differ
// between c and next and cannot be applied to a running agent.
func (c *Config) NonReloadableChanges(next *Config) []string {
	var changed []string
	t := reflect.TypeOf(*c)
	a, b := reflect.ValueOf(*c), reflect.ValueOf(*next)
	for i := 0; i < t.NumField(); i++ {
		name := t.Field(i).Name
		if reloadableFields[name] {
			continue
		}
		if name == "Actions" {
			if !actionsEqual(c.Actions, next.Actions) {
				changed = append(changed, name)
			}
			continue
		}
		if !reflect.DeepEqual(a.Field(i).Interface(), b.Field(i).Interface()) {
			changed = append(changed, name)
		}
	}
	return changed
}

// actionsEqual compares the configured fields of every action. It does not use
// reflect.DeepEqual because Parameter carries a compiled regexp whose internal
// state is not a stable basis for comparison; Pattern is its source.
func actionsEqual(a, b map[string]*Action) bool {
	if len(a) != len(b) {
		return false
	}
	for name, x := range a {
		y, ok := b[name]
		if !ok || x.Command != y.Command || x.Timeout != y.Timeout ||
			x.Concurrency != y.Concurrency || len(x.Parameters) != len(y.Parameters) {
			return false
		}
		for pn, px := range x.Parameters {
			py, ok := y.Parameters[pn]
			if !ok || px.Type != py.Type || px.Pattern != py.Pattern || px.Required != py.Required {
				return false
			}
		}
	}
	return true
}
