package features

import (
	"cmp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/barteo/kotlin-compiler-go/psi"
	"github.com/barteo/kotlin-lsp-go/internal/protocol"
)

// MatchScore scores how well name matches a workspace-symbol query, and
// reports whether it matches: the query's letters must appear in name in
// order, ignoring case. An exact name scores highest, then a prefix,
// then letters at word starts (camel humps: "SB" or "StrBu" for
// StringBuilder), then a substring, then letters anywhere.
func MatchScore(query, name string) (int, bool) {
	if query == "" {
		return 0, true
	}
	switch {
	case name == query:
		return 1000, true
	case strings.EqualFold(name, query):
		return 900, true
	case strings.HasPrefix(name, query):
		return 800 - len(name), true
	case len(name) >= len(query) && strings.EqualFold(name[:len(query)], query):
		return 700 - len(name), true
	}
	if humps(query, name) {
		return 600 - len(name), true
	}
	if strings.Contains(strings.ToLower(name), strings.ToLower(query)) {
		return 400 - len(name), true
	}
	if subsequence(strings.ToLower(query), strings.ToLower(name)) {
		return 200 - len(name), true
	}
	return 0, false
}

// humps reports whether each run of query's letters starts a word of
// name, in order: an upper-case letter, a letter after a lower-case one
// or an underscore, or the first.
func humps(query, name string) bool {
	q := []rune(query)
	n := []rune(name)
	var match func(qi, ni int) bool
	match = func(qi, ni int) bool {
		if qi == len(q) {
			return true
		}
		for j := ni; j < len(n); j++ {
			if !wordStart(n, j) || unicode.ToLower(n[j]) != unicode.ToLower(q[qi]) {
				continue
			}
			// Take as many of query's next letters as follow in this word.
			for k := 0; j+k < len(n) && qi+k < len(q) && unicode.ToLower(n[j+k]) == unicode.ToLower(q[qi+k]); k++ {
				if (k == 0 || !wordStart(n, j+k)) && match(qi+k+1, j+k+1) {
					return true
				}
			}
		}
		return false
	}
	return match(0, 0)
}

func wordStart(n []rune, i int) bool {
	if i == 0 {
		return true
	}
	p, c := n[i-1], n[i]
	return unicode.IsUpper(c) && !unicode.IsUpper(p) || p == '_' && c != '_' || unicode.IsDigit(c) && !unicode.IsDigit(p)
}

func subsequence(q, s string) bool {
	for _, r := range q {
		i := strings.IndexRune(s, r)
		if i < 0 {
			return false
		}
		s = s[i+utf8.RuneLen(r):]
	}
	return true
}

// Match is a workspace symbol and its score.
type Match struct {
	Symbol protocol.SymbolInformation
	Score  int
}

// BestMatches sorts matches best first, then by name and container, and
// keeps at most limit.
func BestMatches(ms []Match, limit int) []protocol.SymbolInformation {
	slices.SortStableFunc(ms, func(a, b Match) int {
		return cmp.Or(b.Score-a.Score, strings.Compare(a.Symbol.Name, b.Symbol.Name),
			strings.Compare(a.Symbol.ContainerName, b.Symbol.ContainerName))
	})
	out := []protocol.SymbolInformation{}
	for i, m := range ms {
		if i == limit {
			break
		}
		out = append(out, m.Symbol)
	}
	return out
}

// WorkspaceSymbols returns the declarations of a file for workspace/symbol:
// its outline flattened, each with the qualified name of its container.
func WorkspaceSymbols(f *File) []protocol.SymbolInformation {
	var prefix []string
	if d := f.Snapshot.RootNode().ChildKind(psi.KindPACKAGE_DIRECTIVE); d.Valid() && d.Path() != "" {
		prefix = []string{d.Path()}
	}
	out := []protocol.SymbolInformation{}
	var walk func(ss []protocol.DocumentSymbol, container []string)
	walk = func(ss []protocol.DocumentSymbol, container []string) {
		for _, s := range ss {
			out = append(out, protocol.SymbolInformation{
				Name: s.Name, Kind: s.Kind, Tags: s.Tags, ContainerName: strings.Join(container, "."),
				Location: protocol.Location{URI: f.URI, Range: s.SelectionRange},
			})
			walk(s.Children, append(slices.Clip(container), s.Name))
		}
	}
	walk(DocumentSymbols(f), prefix)
	return out
}
