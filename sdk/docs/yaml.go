package docs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The files of docs/ are YAML, but a small part of it, which the SDKs of the other languages read without a library
// and read the same: block mappings and lists of scalars, two spaces deeper per level; a scalar is plain (the rest of
// the line, a " #" ends it), "double-quoted" (JSON escapes), 'single-quoted' ('' is a quote) or a | block (|- too;
// either way without the newline at its end). Every scalar is a string: nothing is a number, a boolean or null here.
// Flow collections, anchors, tags, folded and multi-line plain scalars are refused, so a file means one thing to all.

// Map is a mapping in the order of the file. A value is a string, a []string or a *Map.
type Map struct {
	Keys   []string
	Values map[string]any
}

func (m *Map) set(k string, v any) {
	if m.Values == nil {
		m.Values = map[string]any{}
	}
	m.Keys = append(m.Keys, k)
	m.Values[k] = v
}

type parser struct {
	lines []string
	i     int
}

// Parse reads a document of the subset: a mapping at its top.
func Parse(src []byte) (*Map, error) {
	text := strings.ReplaceAll(string(src), "\r\n", "\n")
	p := &parser{lines: strings.Split(text, "\n")}
	m, err := p.mapping(0)
	if err != nil {
		return nil, err
	}
	if p.next() {
		return nil, p.errf("unexpected indentation")
	}
	return m, nil
}

func (p *parser) errf(format string, args ...any) error {
	return fmt.Errorf("line %d: %s", p.i+1, fmt.Sprintf(format, args...))
}

// next skips blank and comment lines and says whether a line is left.
func (p *parser) next() bool {
	for ; p.i < len(p.lines); p.i++ {
		t := strings.TrimLeft(p.lines[p.i], " ")
		if len(t) > 0 && t[0] != '#' {
			return true
		}
	}
	return false
}

// indent is the depth of the current line; a tab in it is refused.
func (p *parser) indent() (int, error) {
	l := p.lines[p.i]
	n := len(l) - len(strings.TrimLeft(l, " "))
	if n < len(l) && l[n] == '\t' {
		return 0, p.errf("a tab in the indentation")
	}
	return n, nil
}

func (p *parser) mapping(indent int) (*Map, error) {
	m := &Map{Values: map[string]any{}}
	for p.next() {
		n, err := p.indent()
		if err != nil {
			return nil, err
		}
		if n < indent {
			break
		}
		if n > indent {
			return nil, p.errf("unexpected indentation")
		}
		line := p.lines[p.i][n:]
		if line == "-" || strings.HasPrefix(line, "- ") {
			return nil, p.errf("a list item where a key was expected")
		}
		key, rest, err := p.key(line)
		if err != nil {
			return nil, err
		}
		if _, dup := m.Values[key]; dup {
			return nil, p.errf("%q twice", key)
		}
		var v any
		switch rest {
		case "":
			p.i++
			if !p.next() {
				return nil, fmt.Errorf("line %d: %q has no value", p.i, key)
			}
			child, err := p.indent()
			if err != nil {
				return nil, err
			}
			if child <= indent {
				return nil, p.errf("%q has no value", key)
			}
			if l := p.lines[p.i][child:]; l == "-" || strings.HasPrefix(l, "- ") {
				v, err = p.list(child)
			} else {
				v, err = p.mapping(child)
			}
			if err != nil {
				return nil, err
			}
		case "|", "|-":
			p.i++
			v = p.block(indent)
		default:
			s, err := p.scalar(rest)
			if err != nil {
				return nil, err
			}
			v = s
			p.i++
		}
		m.set(key, v)
	}
	return m, nil
}

func (p *parser) list(indent int) ([]string, error) {
	var out []string
	for p.next() {
		n, err := p.indent()
		if err != nil {
			return nil, err
		}
		if n < indent {
			break
		}
		if n > indent {
			return nil, p.errf("unexpected indentation")
		}
		line := p.lines[p.i][n:]
		if !strings.HasPrefix(line, "- ") {
			if line == "-" {
				return nil, p.errf("a list item without a value")
			}
			return nil, p.errf("a key where a list item was expected")
		}
		rest := strings.TrimLeft(line[2:], " ")
		if rest == "|" || rest == "|-" {
			return nil, p.errf("a block in a list: write the item on its line")
		}
		s, err := p.scalar(rest)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
		p.i++
	}
	return out, nil
}

// block reads the lines of a | scalar under a key at indent: they are deeper than the key, at the depth of the first.
func (p *parser) block(indent int) string {
	var out []string
	depth := -1
	for ; p.i < len(p.lines); p.i++ {
		l := p.lines[p.i]
		if len(strings.TrimLeft(l, " ")) == 0 {
			out = append(out, "")
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " "))
		if depth < 0 {
			if n <= indent {
				break
			}
			depth = n
		}
		if n < depth {
			break
		}
		out = append(out, l[depth:])
	}
	for len(out) > 0 && len(out[len(out)-1]) == 0 {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

var plainKey = regexp.MustCompile(`^[^\s\-?:,\[\]{}#&*!|>'"%@` + "`" + `]`)

// key splits "key: rest" (rest trimmed, "" when the value is on the next lines).
func (p *parser) key(line string) (string, string, error) {
	if line[0] == '"' {
		k, after, err := doubleQuoted(line)
		if err != nil {
			return "", "", p.errf("%v", err)
		}
		if !strings.HasPrefix(after, ":") {
			return "", "", p.errf("a quoted key without ':'")
		}
		return k, value(after[1:]), nil
	}
	if !plainKey.MatchString(line) && !strings.HasPrefix(line, "/") {
		return "", "", p.errf("a key that is not plain: quote it")
	}
	if i := strings.Index(line, ": "); i >= 0 {
		return strings.TrimRight(line[:i], " "), value(line[i+1:]), nil
	}
	if strings.HasSuffix(line, ":") {
		return strings.TrimRight(line[:len(line)-1], " "), "", nil
	}
	return "", "", p.errf("no ':' after the key")
}

// value is what follows a key's ':' — a comment alone is no value.
func value(s string) string {
	s = strings.Trim(s, " ")
	if strings.HasPrefix(s, "#") {
		return ""
	}
	return s
}

func (p *parser) scalar(s string) (string, error) {
	switch s[0] {
	case '"':
		v, after, err := doubleQuoted(s)
		if err != nil {
			return "", p.errf("%v", err)
		}
		return v, p.tail(after)
	case '\'':
		v, after, err := singleQuoted(s)
		if err != nil {
			return "", p.errf("%v", err)
		}
		return v, p.tail(after)
	case '[', '{', '&', '*', '!', '>', '|', '%', '@', '`':
		return "", p.errf("%q: not in the YAML of docs/ (block style and plain or quoted scalars only)", s)
	}
	if i := strings.Index(s, " #"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimRight(s, " ")
	if strings.Contains(s, ": ") || strings.HasSuffix(s, ":") {
		return "", p.errf("%q: a ':' in a plain scalar — quote it", s)
	}
	return s, nil
}

// tail is what may follow a quoted scalar: nothing, or a comment.
func (p *parser) tail(after string) error {
	after = strings.TrimLeft(after, " ")
	if len(after) > 0 && after[0] != '#' {
		return p.errf("%q after a quoted scalar", after)
	}
	return nil
}

func doubleQuoted(s string) (string, string, error) {
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			return b.String(), s[i+1:], nil
		case '\\':
			if i+1 >= len(s) {
				return "", "", fmt.Errorf("an escape at the end of the line")
			}
			i++
			switch s[i] {
			case '"', '\\', '/':
				b.WriteByte(s[i])
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case 'u':
				if i+4 >= len(s) {
					return "", "", fmt.Errorf("a short \\u escape")
				}
				r, err := strconv.ParseUint(s[i+1:i+5], 16, 32)
				if err != nil {
					return "", "", fmt.Errorf("\\u%s: not hex", s[i+1:i+5])
				}
				b.WriteRune(rune(r))
				i += 4
			default:
				return "", "", fmt.Errorf("\\%c: not an escape of docs/ (\\\" \\\\ \\/ \\n \\t \\r \\uXXXX)", s[i])
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", "", fmt.Errorf("a double-quoted scalar ends on its line")
}

func singleQuoted(s string) (string, string, error) {
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		if s[i] == '\'' {
			if i+1 < len(s) && s[i+1] == '\'' {
				b.WriteByte('\'')
				i++
				continue
			}
			return b.String(), s[i+1:], nil
		}
		b.WriteByte(s[i])
	}
	return "", "", fmt.Errorf("a single-quoted scalar ends on its line")
}

// ---- writing

var plainValue = regexp.MustCompile(`^[^\s\-?:,\[\]{}#&*!|>'"%@` + "`" + `]`)

// writeScalar writes s after "key:" (or "- ") at indent: plain when it reads back as itself, a | block when it has
// lines, double-quoted otherwise.
func writeScalar(b *bytes.Buffer, s string, indent int) {
	switch {
	case strings.Contains(s, "\n"):
		b.WriteString(" |\n")
		for l := range strings.SplitSeq(s, "\n") {
			if len(l) > 0 {
				b.WriteString(strings.Repeat(" ", indent+2) + l)
			}
			b.WriteByte('\n')
		}
		return
	case len(s) > 0 && plainValue.MatchString(s) && !strings.Contains(s, ": ") && !strings.Contains(s, " #") &&
		!strings.HasSuffix(s, ":") && !strings.HasSuffix(s, " "):
		b.WriteString(" " + s)
	default:
		b.WriteString(" " + quote(s))
	}
	b.WriteByte('\n')
}

func quote(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

// writeKey writes "key:" at indent.
func writeKey(b *bytes.Buffer, k string, indent int) {
	b.WriteString(strings.Repeat(" ", indent))
	if (plainKey.MatchString(k) || strings.HasPrefix(k, "/")) && !strings.Contains(k, ": ") && !strings.Contains(k, " #") && !strings.HasSuffix(k, ":") {
		b.WriteString(k)
	} else {
		b.WriteString(quote(k))
	}
	b.WriteByte(':')
}

// writeComment writes text as comment lines at indent.
func writeComment(b *bytes.Buffer, text string, indent int) {
	for l := range strings.SplitSeq(text, "\n") {
		b.WriteString(strings.Repeat(" ", indent) + "#")
		if len(l) > 0 {
			b.WriteString(" " + l)
		}
		b.WriteByte('\n')
	}
}
