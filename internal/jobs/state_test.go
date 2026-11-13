package jobs

import (
	"slices"
	"testing"
)

// AllStates and the transition table must list the same states, so nothing
// that iterates AllStates (such as the store's active-state query) misses one.
func TestAllStatesMatchesTheTable(t *testing.T) {
	all := AllStates()
	if len(all) != len(transitions) {
		t.Errorf("AllStates has %d states, the table %d", len(all), len(transitions))
	}
	for st := range transitions {
		if !slices.Contains(all, st) {
			t.Errorf("%s is in the table but not in AllStates", st)
		}
	}
	for _, st := range all {
		for _, to := range transitions[st] {
			if !to.Valid() {
				t.Errorf("%s moves to %s, which has no table entry", st, to)
			}
		}
	}
}
