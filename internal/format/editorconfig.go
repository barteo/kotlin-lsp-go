package format

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// EditorConfig returns o with the settings that the .editorconfig files
// from the directory of the file at path up give it: indent_style,
// indent_size, tab_width, insert_final_newline, and IntelliJ's
// ij_kotlin_keep_blank_lines_in_code and
// ij_kotlin_keep_blank_lines_before_right_brace. A file nearer the path
// wins, and a file with root = true is the last read.
func EditorConfig(path string, o Options) Options {
	path, err := filepath.Abs(path)
	if err != nil {
		return o
	}
	type config struct {
		dir      string
		sections []section
	}
	var configs []config // nearest first
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		if sections, root, ok := readEditorConfig(filepath.Join(dir, ".editorconfig")); ok {
			configs = append(configs, config{dir, sections})
			if root {
				break
			}
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	props := map[string]string{}
	for i := len(configs) - 1; i >= 0; i-- {
		rel, err := filepath.Rel(configs[i].dir, path)
		if err != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		for _, s := range configs[i].sections {
			if s.re.MatchString(rel) {
				for k, v := range s.props {
					props[k] = v
				}
			}
		}
	}
	return withProps(o, props)
}

// withProps returns o with the EditorConfig properties props.
func withProps(o Options, props map[string]string) Options {
	d := Default()
	num := func(k string, def int) (int, bool) {
		v, ok := props[k]
		if !ok {
			return 0, false
		}
		if v == "unset" {
			return def, true
		}
		n, err := strconv.Atoi(v)
		return n, err == nil && n >= 0
	}
	switch props["indent_style"] {
	case "tab":
		o.UseTabs = true
	case "space":
		o.UseTabs = false
	case "unset":
		o.UseTabs = d.UseTabs
	}
	if n, ok := num("tab_width", d.TabWidth); ok && n > 0 {
		o.TabWidth = n
	}
	if props["indent_size"] == "tab" {
		o.IndentSize = o.TabWidth
	} else if n, ok := num("indent_size", d.IndentSize); ok && n > 0 {
		o.IndentSize = n
		if _, set := props["tab_width"]; !set {
			o.TabWidth = n
		}
	}
	switch props["insert_final_newline"] {
	case "true":
		o.InsertFinalNewline = true
	case "false":
		o.InsertFinalNewline = false
	case "unset":
		o.InsertFinalNewline = d.InsertFinalNewline
	}
	if n, ok := num("ij_kotlin_keep_blank_lines_in_code", d.MaxBlankLines); ok {
		o.MaxBlankLines = n
	}
	if n, ok := num("ij_kotlin_keep_blank_lines_before_right_brace", d.BlankLinesBeforeRBrace); ok {
		o.BlankLinesBeforeRBrace = n
	}
	return o
}

type section struct {
	re    *regexp.Regexp
	props map[string]string
}

// readEditorConfig reads an .editorconfig file: its sections, and
// whether it is the root one.
func readEditorConfig(path string) ([]section, bool, bool) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, false, false
	}
	defer fh.Close()
	var sections []section
	root := false
	sc := bufio.NewScanner(fh)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "" || line[0] == '#' || line[0] == ';':
		case line[0] == '[' && strings.HasSuffix(line, "]"):
			re, err := regexp.Compile(globRegexp(line[1 : len(line)-1]))
			if err != nil {
				re = regexp.MustCompile(`^$.`) // matches nothing
			}
			sections = append(sections, section{re: re, props: map[string]string{}})
		default:
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			k, v = strings.ToLower(strings.TrimSpace(k)), strings.ToLower(strings.TrimSpace(v))
			if len(sections) == 0 {
				if k == "root" {
					root = v == "true"
				}
				continue
			}
			sections[len(sections)-1].props[k] = v
		}
	}
	return sections, root, true
}

// globRegexp returns the regular expression of an EditorConfig glob,
// which matches paths relative to the .editorconfig's directory.
func globRegexp(glob string) string {
	if strings.HasPrefix(glob, "/") {
		glob = glob[1:]
	} else if !strings.Contains(glob, "/") {
		glob = "**/" + glob
	}
	var b strings.Builder
	b.WriteString("^")
	depth := 0 // of braces
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch {
		case strings.HasPrefix(glob[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(glob[i:], "**"):
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		case c == '[':
			j := strings.IndexByte(glob[i:], ']')
			if j < 0 {
				b.WriteString(`\[`)
				continue
			}
			class := glob[i+1 : i+j]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			b.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
			i += j
		case c == '{':
			if j := strings.IndexByte(glob[i:], '}'); j > 0 && regexp.MustCompile(`^-?\d+\.\.-?\d+$`).MatchString(glob[i+1:i+j]) {
				b.WriteString(`-?\d+`)
				i += j
				continue
			}
			depth++
			b.WriteString("(?:")
		case c == '}' && depth > 0:
			depth--
			b.WriteString(")")
		case c == ',' && depth > 0:
			b.WriteString("|")
		case c == '\\' && i+1 < len(glob):
			i++
			b.WriteString(regexp.QuoteMeta(glob[i : i+1]))
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	for ; depth > 0; depth-- {
		b.WriteString(")")
	}
	b.WriteString("$")
	return b.String()
}
