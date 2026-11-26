package format

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestCorpus reports how much formatting changes the Kotlin files under
// $KCLS_FORMAT_CORPUS, which should be formatted already, as the
// stdlib's sources are: the lines it changes, blank lines aside, the
// most common first. $KCLS_FORMAT_OUT, if set, names a file to write the
// changed lines to.
func TestCorpus(t *testing.T) {
	dir := os.Getenv("KCLS_FORMAT_CORPUS")
	if dir == "" {
		t.Skip("KCLS_FORMAT_CORPUS names no directory")
	}
	var files []string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if strings.HasSuffix(p, ".kt") {
			files = append(files, p)
		}
		return nil
	})
	total, changed := 0, 0
	samples := map[string]int{}
	var ex []string
	for _, p := range files {
		src, _ := os.ReadFile(p)
		got := formatString(p, string(src), Default())
		a, b := nonBlank(string(src)), nonBlank(got)
		total += len(a)
		if len(a) != len(b) {
			t.Logf("%s: %d lines -> %d", p[len(dir):], len(a), len(b))
			continue
		}
		for i := range a {
			if a[i] != b[i] {
				changed++
				key := strings.TrimSpace(a[i])
				if len(key) > 50 {
					key = key[:50]
				}
				samples[key]++
				if len(ex) < 400 {
					ex = append(ex, fmt.Sprintf("%s:%d\n  - %s\n  + %s", p[len(dir):], i+1, a[i], b[i]))
				}
			}
		}
	}
	t.Logf("%d of %d lines changed", changed, total)
	type kv struct {
		k string
		v int
	}
	var kvs []kv
	for k, v := range samples {
		kvs = append(kvs, kv{k, v})
	}
	sort.Slice(kvs, func(i, j int) bool { return kvs[i].v > kvs[j].v })
	for i, e := range kvs {
		if i > 30 {
			break
		}
		t.Logf("%5d %s", e.v, e.k)
	}
	if out := os.Getenv("KCLS_FORMAT_OUT"); out != "" {
		os.WriteFile(out, []byte(strings.Join(ex, "\n")), 0o644)
	}
}

func nonBlank(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}
