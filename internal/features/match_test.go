package features

import "testing"

func TestMatchScore(t *testing.T) {
	for _, c := range []struct {
		query, name string
		ok          bool
	}{
		{"", "Any", true},
		{"StringBuilder", "StringBuilder", true},
		{"stringb", "StringBuilder", true},
		{"SB", "StringBuilder", true},
		{"StrBu", "StringBuilder", true},
		{"sbu", "StringBuilder", true},
		{"ilder", "StringBuilder", true},
		{"sgbr", "StringBuilder", true},
		{"BS", "StringBuilder", false},
		{"x", "StringBuilder", false},
		{"HM", "HashMap", true},
		{"get_v", "get_value", true},
	} {
		if _, ok := MatchScore(c.query, c.name); ok != c.ok {
			t.Errorf("MatchScore(%q, %q) matches %v, want %v", c.query, c.name, ok, c.ok)
		}
	}
	// Better matches score higher.
	order := []string{"Builder", "builder", "BuilderFactory", "StringBuilder", "Rebuilder", "BigUnusualIdeaLoader"}
	prev := 1 << 30
	for _, name := range order {
		s, ok := MatchScore("Builder", name)
		if !ok || s >= prev {
			t.Errorf("MatchScore(Builder, %s) = %d, %v; want a match below %d", name, s, ok, prev)
		}
		prev = s
	}
}
