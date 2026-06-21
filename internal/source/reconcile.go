package source

// ExistingTile is the minimal view of a tile already present in a source
// grid that Reconcile needs: its dedup key (source_key) and current label
// (alt_text). The store supplies these from the rows it already holds.
type ExistingTile struct {
	Key   string
	Label string
}

// Relabel is an in-place label refresh: the tile keeps its id and
// placement (identity is stable — editing never moves a tile), only
// alt_text changes.
type Relabel struct {
	Key   string
	Label string
}

// Plan is the diff Reconcile produces. The store applies it: create the
// Inserts, refresh the Relabels in place, sweep the Deletes. Nodes whose
// key is unchanged and whose label still matches are absent from the plan —
// they stay exactly as they were, which is the whole point.
type Plan struct {
	Insert  []Node
	Relabel []Relabel
	Delete  []string
}

// Reconcile diffs a source Listing against the tiles currently in the grid.
//
// Identity is preserved: a node whose key already exists is never
// re-created — at most its label is refreshed (Relabel), so its tile id and
// placement survive. New keys become Inserts.
//
// Sweep semantics follow the listing's authority:
//   - Authoritative (fs): any existing key absent from the listing is gone;
//     Delete it. probe is not consulted.
//   - Non-authoritative (proc): the listing may have skipped a key it
//     couldn't read this pass, so a missing key is Deleted only when probe
//     reports PresenceGone — never on PresenceUnknown. A failed read keeps
//     the tile (and its id/placement), per Gridwell's rule that reading
//     never destroys.
//
// probe may be nil for an authoritative listing.
func Reconcile(existing []ExistingTile, listing Listing, probe func(key string) Presence) Plan {
	have := make(map[string]string, len(existing)) // key -> current label
	for _, e := range existing {
		have[e.Key] = e.Label
	}
	listed := make(map[string]struct{}, len(listing.Nodes))

	var plan Plan
	for _, n := range listing.Nodes {
		listed[n.Key] = struct{}{}
		curLabel, ok := have[n.Key]
		if !ok {
			plan.Insert = append(plan.Insert, n)
			continue
		}
		if curLabel != n.Label {
			plan.Relabel = append(plan.Relabel, Relabel{Key: n.Key, Label: n.Label})
		}
	}

	for _, e := range existing {
		if _, stillThere := listed[e.Key]; stillThere {
			continue
		}
		if listing.Authoritative {
			plan.Delete = append(plan.Delete, e.Key)
			continue
		}
		// Non-authoritative: sweep only on a definitive "gone".
		if probe != nil && probe(e.Key) == PresenceGone {
			plan.Delete = append(plan.Delete, e.Key)
		}
	}
	return plan
}
