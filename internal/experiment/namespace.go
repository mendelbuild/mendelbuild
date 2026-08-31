package experiment

import "strings"

// NamespacePrefix marks every object an experiment creates.
//
// It does two jobs. Concurrent Arms adding a field of the same name would
// otherwise collide, and additive changes are only genuinely commutative if
// their names cannot coincide. And a person reading the schema can tell at a
// glance which objects belong to an experiment in flight and which are
// permanent — every one of these is temporary by construction.
//
// Objects are renamed out of the namespace when a Variation is promoted.
const NamespacePrefix = "mendel_exp_"

// CheckNamespace reports objects that are not inside the experiment namespace.
// Concurrent Arms are only safe from each other while every object they create
// is namespaced.
func CheckNamespace(added []Object) []Object {
	// An object inside a collection this same change created inherits that
	// collection's namespace: the fields of mendel_exp_events are already
	// unreachable to anything but this Arm, and requiring each to repeat the
	// prefix would be noise with no safety in it.
	created := map[string]bool{}
	for _, o := range added {
		if o.Kind == ObjectCollection && strings.HasPrefix(strings.ToLower(o.Collection), NamespacePrefix) {
			created[o.Collection] = true
		}
	}

	var bad []Object
	for _, o := range added {
		// A constraint is named by the datastore when the migration does not
		// name it, so it is not held to the rule; it belongs to whatever
		// collection carries it, and that is checked.
		if o.Kind == ObjectConstraint || created[o.Collection] {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(o.Ident()), NamespacePrefix) {
			bad = append(bad, o)
		}
	}
	return bad
}

// TouchedCollections lists the pre-existing collections a change alters, which
// is what must be re-read before applying to detect drift. A collection the
// change creates is not touched in this sense: it did not exist to drift.
func TouchedCollections(added []Object) []string {
	created := map[string]bool{}
	for _, o := range added {
		if o.Kind == ObjectCollection {
			created[o.Collection] = true
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, o := range added {
		if o.Kind == ObjectCollection || created[o.Collection] || seen[o.Collection] || o.Collection == "" {
			continue
		}
		seen[o.Collection] = true
		out = append(out, o.Collection)
	}
	sortStrings(out)
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
