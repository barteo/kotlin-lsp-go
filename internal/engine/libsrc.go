package engine

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/barteo/kotlin-compiler-go/psi"
	"github.com/barteo/kotlin-lsp-go/internal/features"
)

// librarySources finds library declarations in sources jars, which it
// indexes by package on first use and extracts a file of at a time into
// its directory.
type librarySources struct {
	dir  string
	logf func(string, ...any)

	mu   sync.Mutex
	jars map[string]*jarIndex
}

// jarIndex is a sources jar's files by package.
type jarIndex struct {
	path string
	once sync.Once
	pkgs map[string][]string // package → entry names
	err  error
}

func newLibrarySources(dir string, logf func(string, ...any)) *librarySources {
	return &librarySources{dir: dir, logf: logf, jars: map[string]*jarIndex{}}
}

func (l *librarySources) clear() {
	l.mu.Lock()
	l.jars = map[string]*jarIndex{}
	l.mu.Unlock()
	os.RemoveAll(l.dir)
}

func (l *librarySources) index(jar string) *jarIndex {
	l.mu.Lock()
	ix := l.jars[jar]
	if ix == nil {
		ix = &jarIndex{path: jar}
		l.jars[jar] = ix
	}
	l.mu.Unlock()
	ix.once.Do(func() { ix.pkgs, ix.err = indexJar(jar) })
	return ix
}

var packageLine = regexp.MustCompile(`(?m)^[ \t]*package[ \t]+([\w.` + "`" + `]+)`)

// indexJar reads the package of each Kotlin and Java file in a sources
// jar: a Kotlin file's from its package directive, as the standard
// library's jar is laid out by source set, a Java file's from its path,
// less a JDK src.zip's module directory.
func indexJar(jar string) (map[string][]string, error) {
	z, err := zip.OpenReader(jar)
	if err != nil {
		return nil, err
	}
	defer z.Close()
	pkgs := map[string][]string{}
	buf := make([]byte, 8<<10)
	for _, f := range z.File {
		name := f.Name
		switch {
		case strings.HasSuffix(name, ".java"):
			dir := filepath.ToSlash(filepath.Dir(name))
			if i := strings.IndexByte(dir, '/'); i > 0 && strings.Contains(dir[:i], ".") {
				dir = dir[i+1:] // java.base/java/lang
			} else if strings.Contains(dir, ".") && !strings.Contains(dir, "/") {
				dir = ""
			}
			if dir == "." {
				dir = ""
			}
			pkg := strings.ReplaceAll(dir, "/", ".")
			pkgs[pkg] = append(pkgs[pkg], name)
		case strings.HasSuffix(name, ".kt"):
			rc, err := f.Open()
			if err != nil {
				continue
			}
			n, _ := io.ReadFull(rc, buf)
			rc.Close()
			pkg := ""
			if m := packageLine.FindSubmatch(buf[:n]); m != nil {
				pkg = strings.ReplaceAll(string(m[1]), "`", "")
			}
			pkgs[pkg] = append(pkgs[pkg], name)
		}
	}
	return pkgs, nil
}

// extract writes a jar's entry into the cache directory, once, and
// returns its path.
func (l *librarySources) extract(jar, entry string) (string, error) {
	h := sha256.Sum256([]byte(jar))
	dest := filepath.Join(l.dir, strings.TrimSuffix(filepath.Base(jar), filepath.Ext(jar))+"-"+hex.EncodeToString(h[:4]), filepath.FromSlash(entry))
	if _, err := os.Stat(dest); err == nil {
		return dest, nil
	}
	z, err := zip.OpenReader(jar)
	if err != nil {
		return "", err
	}
	defer z.Close()
	for _, f := range z.File {
		if f.Name != entry {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		defer rc.Close()
		b, err := io.ReadAll(rc)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return "", err
		}
		tmp := dest + ".tmp"
		if err := os.WriteFile(tmp, b, 0o444); err != nil {
			return "", err
		}
		return dest, os.Rename(tmp, dest)
	}
	return "", os.ErrNotExist
}

// jarSet is the sources of one module's libraries.
type jarSet struct {
	l     *librarySources
	jars  []string
	files Files
}

func (l *librarySources) forJars(jars []string, files Files) features.Libraries {
	return &jarSet{l: l, jars: jars, files: files}
}

// Find finds a library declaration in the module's sources jars.
func (s *jarSet) Find(ctx context.Context, d features.LibraryDecl) (features.LibraryTarget, bool) {
	pkg, class, nested, member := splitDecl(d)
	if class == "" && member == "" {
		return features.LibraryTarget{}, false
	}
	var expect fallback
	for _, jar := range s.jars {
		if ctx.Err() != nil {
			break
		}
		ix := s.l.index(jar)
		if ix.err != nil {
			continue
		}
		entries := ix.pkgs[pkg]
		if len(entries) == 0 {
			continue
		}
		// A class is most likely in the file of its name.
		if class != "" {
			slices.SortStableFunc(entries, func(a, b string) int {
				return boolCmp(baseName(b) == class, baseName(a) == class)
			})
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry, ".java") && class != "" && baseName(entry) != class {
				continue
			}
			path, err := s.l.extract(jar, entry)
			if err != nil {
				continue
			}
			f, err := s.files.File(path)
			if err != nil {
				continue
			}
			var t features.LibraryTarget
			var ok bool
			if strings.HasSuffix(entry, ".java") {
				t, ok = findJava(f, class, nested, member)
			} else {
				t, ok = findKotlin(f, class, nested, member, d)
			}
			if !ok {
				continue
			}
			// A multiplatform library's common expect declaration is a
			// fallback for the JVM's actual one.
			if t.Node.Valid() && t.Node.ChildKind(psi.KindMODIFIER_LIST).ChildKind(psi.KindEXPECT_KEYWORD).Valid() {
				if expect.File == nil {
					expect = fallback{t}
				}
				continue
			}
			return t, true
		}
	}
	return expect.target()
}

// fallback is a target to return if no better one is found.
type fallback struct{ features.LibraryTarget }

func (f fallback) target() (features.LibraryTarget, bool) {
	return f.LibraryTarget, f.File != nil
}

func boolCmp(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}

func baseName(entry string) string {
	b := entry[strings.LastIndexByte(entry, '/')+1:]
	return strings.TrimSuffix(b, filepath.Ext(b))
}

// splitDecl returns the package of a declaration, its class's simple
// name and nested classes' names, and a member's name.
func splitDecl(d features.LibraryDecl) (pkg, class string, nested []string, member string) {
	bin := d.Class
	switch d.Kind {
	case "class":
	case "constructor":
		bin = d.Owner
	default:
		if d.Owner != "" {
			bin = d.Owner
			member = d.QualifiedName[strings.LastIndexByte(d.QualifiedName, '.')+1:]
		} else {
			i := strings.LastIndexByte(d.QualifiedName, '.')
			return d.QualifiedName[:max(i, 0)], "", nil, d.QualifiedName[i+1:]
		}
	}
	if bin == "" {
		return "", "", nil, ""
	}
	if i := strings.LastIndexByte(bin, '/'); i >= 0 {
		pkg, bin = strings.ReplaceAll(bin[:i], "/", "."), bin[i+1:]
	}
	parts := strings.Split(bin, "$")
	return pkg, parts[0], parts[1:], member
}

// findKotlin finds a declaration in a Kotlin file.
func findKotlin(f *features.File, class string, nested []string, member string, d features.LibraryDecl) (features.LibraryTarget, bool) {
	scope := f.Snapshot.RootNode()
	if class != "" {
		for i, name := range append([]string{class}, nested...) {
			if i > 0 {
				scope = scope.ChildKind(psi.KindCLASS_BODY)
			}
			c := findChildDecl(scope, name, true, -1)
			if !c.Valid() {
				return features.LibraryTarget{}, false
			}
			scope = c
		}
		if member == "" {
			return features.LibraryTarget{File: f, Node: scope}, true
		}
		scope = scope.ChildKind(psi.KindCLASS_BODY)
		if !scope.Valid() {
			return features.LibraryTarget{}, false
		}
	}
	n := findChildDecl(scope, member, false, d.Parameters)
	if !n.Valid() {
		return features.LibraryTarget{}, false
	}
	return features.LibraryTarget{File: f, Node: n}, true
}

// findChildDecl finds a declaration named name among scope's children:
// a class if class is set, else a function or property, preferring a
// function of params parameters.
func findChildDecl(scope psi.NodeRef, name string, class bool, params int) psi.NodeRef {
	var first psi.NodeRef
	for c := scope.FirstChild(); c.Valid(); c = c.NextSibling() {
		k := c.NumericKind()
		isClass := k == psi.KindCLASS || k == psi.KindOBJECT_DECLARATION || k == psi.KindTYPEALIAS
		if class != isClass || class && c.Name() != name {
			continue
		}
		if class {
			return c
		}
		if (k == psi.KindFUN || k == psi.KindPROPERTY) && c.Name() == name {
			if k == psi.KindFUN && params >= 0 && len(c.Parameters()) == params {
				return c
			}
			if !first.Valid() {
				first = c
			}
		}
	}
	if !first.Valid() && !class && scope.NumericKind() == psi.KindCLASS_BODY {
		// A property of the primary constructor.
		ctor := scope.Parent().ChildKind(psi.KindPRIMARY_CONSTRUCTOR)
		for _, p := range ctor.Parameters() {
			if p.Name() == name {
				return p
			}
		}
	}
	return first
}

// findJava finds a declaration in a Java file by its text: kcls has no
// Java parser.
func findJava(f *features.File, class string, nested []string, member string) (features.LibraryTarget, bool) {
	src := f.Mapper.Text()
	off := 0
	for _, name := range append([]string{class}, nested...) {
		re := regexp.MustCompile(`\b(?:class|interface|enum|record|@interface)\s+(` + regexp.QuoteMeta(name) + `)\b`)
		m := re.FindStringSubmatchIndex(src[off:])
		if m == nil {
			return features.LibraryTarget{}, false
		}
		off += m[2]
	}
	start, end := off, off+len(class)
	if len(nested) > 0 {
		end = off + len(nested[len(nested)-1])
	}
	if member != "" {
		get := "get" + strings.ToUpper(member[:1]) + member[1:]
		re := regexp.MustCompile(`\b(` + regexp.QuoteMeta(member) + `|` + regexp.QuoteMeta(get) + `)\s*\(|\b(` + regexp.QuoteMeta(member) + `)\s*[;=]`)
		m := re.FindStringSubmatchIndex(src[off:])
		if m == nil {
			return features.LibraryTarget{}, false
		}
		g := 2
		if m[2] < 0 {
			g = 4
		}
		start, end = off+m[g], off+m[g+1]
	}
	return features.LibraryTarget{File: f, Start: start, End: end, Doc: javadocBefore(src, start)}, true
}

// javadocBefore returns the text of the Javadoc comment before the
// declaration whose name is at off, or "".
func javadocBefore(src string, off int) string {
	lineStart := strings.LastIndexByte(src[:off], '\n') + 1
	before := strings.TrimRight(src[:lineStart], " \t\r\n")
	// Skip annotations on the lines above.
	for {
		nl := strings.LastIndexByte(before, '\n')
		line := strings.TrimSpace(before[nl+1:])
		if !strings.HasPrefix(line, "@") {
			break
		}
		before = strings.TrimRight(before[:nl+1], " \t\r\n")
	}
	if !strings.HasSuffix(before, "*/") {
		return ""
	}
	i := strings.LastIndex(before, "/**")
	if i < 0 {
		return ""
	}
	return before[i:]
}
