package lookup

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"
)

// properties is a parsed .properties document: the values by key, and the keys
// in the order they first appear, which is the order a bundle lists a
// function's parameters in.
type properties struct {
	keys   []string
	values map[string]string
}

func (p *properties) set(key, value string) {
	if _, ok := p.values[key]; !ok {
		p.keys = append(p.keys, key)
	}
	p.values[key] = value
}

// parseProperties reads a Java .properties document: `#` and `!` comments, `=`,
// `:` or whitespace separators, backslash continuation lines (whose leading
// whitespace is dropped), and the \t \n \uXXXX escapes. A key that repeats keeps
// its last value, as java.util.Properties does.
func parseProperties(raw []byte) *properties {
	out := &properties{values: map[string]string{}}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	var logical strings.Builder
	continuing := false
	for scanner.Scan() {
		line := scanner.Text()
		if continuing {
			line = strings.TrimLeft(line, " \t\f")
		} else {
			trimmed := strings.TrimLeft(line, " \t\f")
			if trimmed == "" || trimmed[0] == '#' || trimmed[0] == '!' {
				continue
			}
			line = trimmed
		}
		if trailingBackslashes(line)%2 == 1 {
			logical.WriteString(line[:len(line)-1])
			continuing = true
			continue
		}
		logical.WriteString(line)
		continuing = false
		key, value := splitProperty(logical.String())
		logical.Reset()
		if key != "" {
			out.set(key, value)
		}
	}
	if logical.Len() > 0 {
		if key, value := splitProperty(logical.String()); key != "" {
			out.set(key, value)
		}
	}
	return out
}

// trailingBackslashes counts the backslashes that end line: an odd count is a
// continuation, an even count is escaped backslashes.
func trailingBackslashes(line string) int {
	n := 0
	for i := len(line) - 1; i >= 0 && line[i] == '\\'; i-- {
		n++
	}
	return n
}

// splitProperty splits one logical line at its first unescaped separator.
func splitProperty(line string) (string, string) {
	end := len(line)
	for i := 0; i < len(line); i++ {
		c := line[i]
		if c == '\\' {
			i++
			continue
		}
		if c == '=' || c == ':' || c == ' ' || c == '\t' || c == '\f' {
			end = i
			break
		}
	}
	key := unescape(line[:end])
	rest := strings.TrimLeft(line[end:], " \t\f")
	if rest != "" && (rest[0] == '=' || rest[0] == ':') {
		rest = strings.TrimLeft(rest[1:], " \t\f")
	}
	return key, strings.TrimSpace(unescape(rest))
}

// unescape resolves the escapes a .properties value may carry.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i == len(s)-1 {
			b.WriteByte(c)
			continue
		}
		i++
		switch s[i] {
		case 't':
			b.WriteByte('\t')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 'f':
			b.WriteByte('\f')
		case 'u':
			if i+4 < len(s) {
				if r, err := strconv.ParseUint(s[i+1:i+5], 16, 32); err == nil {
					b.WriteRune(rune(r))
					i += 4
					continue
				}
			}
			b.WriteByte('u')
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
