package features

import (
	"regexp"
	"strings"

	"github.com/barteo/kotlin-compiler-go/psi"
)

// kdoc returns a declaration's KDoc as markdown, or "".
func kdoc(decl psi.NodeRef) string {
	d := decl.ChildKind(psi.KindKDOC)
	if !d.Valid() {
		// A KDoc before the modifiers is still the declaration's child in
		// psi; one before an annotated declaration may be its sibling.
		for p := decl.PreviousSibling(); p.Valid() && p.IsTrivia(); p = p.PreviousSibling() {
			if p.NumericKind() == psi.KindKDOC {
				d = p
				break
			}
		}
	}
	if !d.Valid() {
		return ""
	}
	return KDocMarkdown(d.Text())
}

var kdocLink = regexp.MustCompile(`\[([^\]\[]+)\](?:\(|\[)?`)

// KDocMarkdown renders a KDoc comment as markdown: its text, then its
// block tags as sections.
func KDocMarkdown(comment string) string {
	lines := commentLines(comment)
	var body []string
	type tag struct{ name, subject, text string }
	var tags []tag
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "@") {
			name, rest, _ := strings.Cut(t[1:], " ")
			rest = strings.TrimSpace(rest)
			x := tag{name: name}
			switch name {
			case "param", "property", "throws", "exception", "see", "sample":
				x.subject, x.text, _ = strings.Cut(rest, " ")
				x.subject = strings.Trim(x.subject, "[]")
			default:
				x.text = rest
			}
			tags = append(tags, x)
			continue
		}
		if len(tags) > 0 && t != "" {
			last := &tags[len(tags)-1]
			last.text = strings.TrimSpace(last.text + " " + t)
			continue
		}
		body = append(body, l)
	}
	var b strings.Builder
	b.WriteString(strings.TrimSpace(links(strings.Join(body, "\n"))))
	section := func(title string, names ...string) {
		first := true
		for _, t := range tags {
			if !slicesContains(names, t.name) {
				continue
			}
			if first {
				b.WriteString("\n\n**" + title + "**")
				first = false
			}
			b.WriteString("\n- ")
			if t.subject != "" {
				b.WriteString("`" + t.subject + "`")
				if t.text != "" {
					b.WriteString(" — ")
				}
			}
			b.WriteString(links(t.text))
		}
	}
	section("Parameters", "param")
	section("Properties", "property")
	section("Returns", "return")
	section("Receiver", "receiver")
	section("Throws", "throws", "exception")
	section("Since", "since")
	section("See also", "see")
	return strings.TrimSpace(b.String())
}

func slicesContains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// links renders KDoc links, [name], as code.
func links(s string) string {
	return kdocLink.ReplaceAllStringFunc(s, func(m string) string {
		if strings.HasSuffix(m, "(") || strings.HasSuffix(m, "[") {
			return m // a markdown link
		}
		return "`" + strings.Trim(m, "[]") + "`"
	})
}

// commentLines returns the lines of a /** */ comment without the
// delimiters and the leading asterisks.
func commentLines(comment string) []string {
	c := strings.TrimSpace(comment)
	c = strings.TrimPrefix(c, "/**")
	c = strings.TrimSuffix(c, "*/")
	var out []string
	for _, l := range strings.Split(c, "\n") {
		t := strings.TrimLeft(l, " \t")
		if strings.HasPrefix(t, "*") {
			t = strings.TrimPrefix(t[1:], " ")
		} else {
			t = strings.TrimSpace(l)
		}
		out = append(out, strings.TrimRight(t, " \t\r"))
	}
	for len(out) > 0 && out[0] == "" {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

var (
	javadocInline = regexp.MustCompile(`\{@(?:code|literal)\s+([^}]*)\}`)
	javadocLink   = regexp.MustCompile(`\{@(?:link|linkplain)\s+([^}\s]*)(?:\s+([^}]*))?\}`)
	htmlTag       = regexp.MustCompile(`</?([a-zA-Z]+)[^>]*>`)
)

// javadoc renders a Javadoc comment as markdown.
func javadoc(comment string) string {
	s := strings.Join(commentLines(comment), "\n")
	s = javadocInline.ReplaceAllString(s, "`$1`")
	s = javadocLink.ReplaceAllStringFunc(s, func(m string) string {
		sub := javadocLink.FindStringSubmatch(m)
		if sub[2] != "" {
			return sub[2]
		}
		return "`" + strings.ReplaceAll(strings.TrimPrefix(sub[1], "#"), "#", ".") + "`"
	})
	s = htmlTag.ReplaceAllStringFunc(s, func(m string) string {
		switch strings.ToLower(htmlTag.FindStringSubmatch(m)[1]) {
		case "p", "br":
			return "\n\n"
		case "code", "tt":
			return "`"
		case "li":
			return "\n- "
		case "b", "strong":
			return "**"
		case "i", "em":
			return "*"
		}
		return ""
	})
	s = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&", "&nbsp;", " ").Replace(s)
	// The block tags, as KDoc's.
	return KDocMarkdown("/**\n" + s + "\n*/")
}
