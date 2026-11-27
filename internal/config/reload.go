package config

import (
	"reflect"
	"sort"
	"strings"
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

// IdentityChanges summarises how next differs from c in who may do what, for
// the audit trail: identities added, removed, or given a different action list.
// It returns "no identity changes" when the allowlists are identical.
func (c *Config) IdentityChanges(next *Config) string {
	var added, removed, changed []string
	for name, id := range next.Identities {
		old, ok := c.Identities[name]
		switch {
		case !ok:
			added = append(added, name)
		case !sameActionSet(old.Actions, id.Actions):
			changed = append(changed, name)
		}
	}
	for name := range c.Identities {
		if _, ok := next.Identities[name]; !ok {
			removed = append(removed, name)
		}
	}

	var parts []string
	for _, group := range []struct {
		label string
		names []string
	}{{"added", added}, {"removed", removed}, {"changed", changed}} {
		if len(group.names) > 0 {
			sort.Strings(group.names)
			parts = append(parts, group.label+": "+strings.Join(group.names, ", "))
		}
	}
	if len(parts) == 0 {
		return "no identity changes"
	}
	return strings.Join(parts, "; ")
}

func sameActionSet(a, b []string) bool {
	set := make(map[string]struct{}, len(a))
	for _, x := range a {
		set[x] = struct{}{}
	}
	other := make(map[string]struct{}, len(b))
	for _, x := range b {
		other[x] = struct{}{}
	}
	if len(set) != len(other) {
		return false
	}
	for x := range set {
		if _, ok := other[x]; !ok {
			return false
		}
	}
	return true
}

// actionsEqual compares every field of every action, so a field added to
// Action or Parameter later is restart-only without touching this function.
// Only the compiled regexp is left out: its internal state is not a stable
// basis for comparison, and Pattern is its source.
func actionsEqual(a, b map[string]*Action) bool {
	if len(a) != len(b) {
		return false
	}
	for name, x := range a {
		y, ok := b[name]
		if !ok || len(x.Parameters) != len(y.Parameters) {
			return false
		}
		xa, ya := *x, *y
		xa.Parameters, ya.Parameters = nil, nil
		if !reflect.DeepEqual(xa, ya) {
			return false
		}
		for pn, px := range x.Parameters {
			py, ok := y.Parameters[pn]
			px.compiled, py.compiled = nil, nil
			if !ok || px != py {
				return false
			}
		}
	}
	return true
}
