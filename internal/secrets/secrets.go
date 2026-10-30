// Package secrets fills in references like ${DB_PASSWORD} in a service's
// committed env files at deploy time. The repository holds only the
// references; the values come from the deploying machine's environment, from
// setec (see Resolver) or, in GitHub Actions, from repository secrets passed
// as ROLLOUT_SECRETS.
//
// Env files are files named ".env" or "*.env" anywhere in the service
// directory. Inside them, in unquoted and double-quoted values:
//
//	${NAME}           required: the deploy fails if NAME is not set
//	$NAME             same as ${NAME}
//	${NAME:-default}  default if NAME is unset or empty
//	${NAME-default}   default if NAME is unset
//	$$                a literal $
//
// Single-quoted values are literal, as in Docker Compose. A line with
// references is rewritten as a double-quoted value that Compose reads back
// exactly, whatever characters the secret contains.
package secrets

import (
	"bytes"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// EnvVar holds secrets as a JSON object, e.g. ${{ toJSON(secrets) }}.
const EnvVar = "ROLLOUT_SECRETS"

// Lookup returns the value of a secret and whether it is set.
type Lookup func(name string) (string, bool)

// FromEnv looks names up in the process environment, then in the JSON object
// in ROLLOUT_SECRETS.
func FromEnv() (Lookup, error) {
	r, err := NewResolver(nil, nil)
	if err != nil {
		return nil, err
	}
	return r.Lookup, nil
}

// IsEnvFile reports whether a path in a service directory is an env file.
func IsEnvFile(rel string) bool {
	base := path.Base(filepath.ToSlash(rel))
	return base == ".env" || strings.HasSuffix(base, ".env")
}

// Ref is one reference in an env file.
type Ref struct {
	File       string `json:"file"` // relative to the service directory
	Line       int    `json:"line"`
	Name       string `json:"name"`
	HasDefault bool   `json:"hasDefault,omitempty"`
	Set        bool   `json:"set"`
}

// Rendered holds a service's env files with references filled in.
type Rendered struct {
	// Files maps paths (relative, slash-separated) of env files that had
	// references to their rendered contents.
	Files map[string][]byte
	// Refs lists every reference, in file and line order.
	Refs []Ref
	// Digest identifies the values used, so a changed secret redeploys the
	// service. It is a salted, slow hash: it is stored in the deploy record
	// and must not make guessing secrets easy. Empty without references.
	Digest string
}

// Missing returns the names of required references that are not set.
func (r *Rendered) Missing() []string {
	var names []string
	for _, ref := range r.Refs {
		if !ref.Set && !ref.HasDefault && !slices.Contains(names, ref.Name) {
			names = append(names, ref.Name)
		}
	}
	return names
}

// Render renders every env file in dir. service salts the digest. It fails
// on malformed references; missing secrets are reported by Missing.
func Render(dir, service string, lookup Lookup) (*Rendered, error) {
	r := &Rendered{Files: map[string][]byte{}}
	values := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !d.Type().IsRegular() || !IsEnvFile(rel) {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out, refs, err := renderFile(src, func(name string) (string, bool) {
			v, ok := lookup(name)
			if ok {
				values[name] = v
			}
			return v, ok
		})
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		for i := range refs {
			refs[i].File = rel
		}
		if len(refs) > 0 {
			r.Files[rel] = out
			r.Refs = append(r.Refs, refs...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(values) > 0 {
		r.Digest = digest(service, values)
	}
	return r, nil
}

func digest(service string, values map[string]string) string {
	names := make([]string, 0, len(values))
	for n := range values {
		names = append(names, n)
	}
	slices.Sort(names)
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "%q=%q\n", n, values[n])
	}
	key, err := pbkdf2.Key(sha256.New, b.String(), []byte("rollout-secrets:"+service), 100_000, 32)
	if err != nil {
		panic(err) // only fails for invalid parameters
	}
	return "pbkdf2-sha256:" + hex.EncodeToString(key)
}

var assignment = regexp.MustCompile(`^(\s*(?:export\s+)?[A-Za-z_][A-Za-z0-9_.-]*\s*=\s*)(.*)$`)

// renderFile rewrites the lines of an env file that contain references.
// Other lines are kept byte for byte.
func renderFile(src []byte, lookup Lookup) ([]byte, []Ref, error) {
	var out bytes.Buffer
	var refs []Ref
	lines := strings.SplitAfter(string(src), "\n")
	for i, line := range lines {
		body := strings.TrimRight(line, "\r\n")
		eol := line[len(body):]
		m := assignment.FindStringSubmatch(body)
		if m == nil {
			out.WriteString(line)
			continue
		}
		head, value := m[1], m[2]
		var inner, tail string
		quoted := false
		switch {
		case strings.HasPrefix(value, "'"):
			out.WriteString(line)
			continue
		case strings.HasPrefix(value, `"`):
			end := closingQuote(value)
			if end < 0 {
				if hasRef(value) {
					return nil, nil, fmt.Errorf("line %d: references in multi-line values are not supported; write line breaks as \\n", i+1)
				}
				out.WriteString(line)
				continue
			}
			inner, tail, quoted = value[1:end], value[end+1:], true
		default:
			inner, tail = value, ""
			if j := commentStart(value); j >= 0 {
				inner, tail = value[:j], value[j:]
			}
			inner = strings.TrimRight(inner, " \t")
			if tail != "" {
				tail = " " + tail
			}
		}
		rendered, lineRefs, err := expand(inner, quoted, lookup)
		if err != nil {
			return nil, nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		if len(lineRefs) == 0 {
			out.WriteString(line)
			continue
		}
		for _, r := range lineRefs {
			r.Line = i + 1
			refs = append(refs, r)
		}
		out.WriteString(head + `"` + rendered + `"` + tail + eol)
	}
	return out.Bytes(), refs, nil
}

// expand replaces references in a value and returns it escaped for a
// double-quoted Compose value. A value that was already double-quoted keeps
// its own escapes; an unquoted one is escaped in full.
func expand(s string, quoted bool, lookup Lookup) (string, []Ref, error) {
	var b strings.Builder
	var refs []Ref
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quoted && c == '\\' && i+1 < len(s):
			b.WriteString(s[i : i+2])
			i++
		case c == '$' && i+1 < len(s) && s[i+1] == '$':
			b.WriteString("$$")
			i++
		case c == '$' && i+1 < len(s) && s[i+1] == '{':
			end := strings.IndexByte(s[i:], '}')
			if end < 0 {
				return "", nil, fmt.Errorf("unterminated ${ in %q", s)
			}
			expr := s[i+2 : i+end]
			name, def, hasDef, emptyIsUnset, err := parseExpr(expr)
			if err != nil {
				return "", nil, err
			}
			v, ok := lookup(name)
			refs = append(refs, Ref{Name: name, HasDefault: hasDef, Set: ok})
			switch {
			case hasDef && (!ok || (emptyIsUnset && v == "")):
				if quoted {
					b.WriteString(def)
				} else {
					b.WriteString(escape(def))
				}
			default:
				b.WriteString(escape(v))
			}
			i += end
		case c == '$' && i+1 < len(s) && isNameStart(s[i+1]):
			j := i + 1
			for j < len(s) && isNameChar(s[j]) {
				j++
			}
			name := s[i+1 : j]
			v, ok := lookup(name)
			refs = append(refs, Ref{Name: name, Set: ok})
			b.WriteString(escape(v))
			i = j - 1
		case c == '$':
			b.WriteString("$$")
		case quoted:
			b.WriteByte(c)
		default:
			b.WriteString(escape(string(c)))
		}
	}
	return b.String(), refs, nil
}

// parseExpr splits the inside of ${...} into a name and optional default.
func parseExpr(expr string) (name, def string, hasDef, emptyIsUnset bool, err error) {
	j := 0
	for j < len(expr) && isNameChar(expr[j]) {
		j++
	}
	name, rest := expr[:j], expr[j:]
	if name == "" || !isNameStart(name[0]) {
		return "", "", false, false, fmt.Errorf("invalid reference ${%s}", expr)
	}
	switch {
	case rest == "":
	case strings.HasPrefix(rest, ":-"):
		def, hasDef, emptyIsUnset = rest[2:], true, true
	case strings.HasPrefix(rest, "-"):
		def, hasDef = rest[1:], true
	default:
		return "", "", false, false, fmt.Errorf("unsupported reference ${%s}: use ${NAME}, ${NAME:-default} or ${NAME-default}", expr)
	}
	return name, def, hasDef, emptyIsUnset, nil
}

// escape makes s literal inside a double-quoted Compose value.
func escape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", "$$", "\n", `\n`, "\r", `\r`).Replace(s)
}

func closingQuote(v string) int {
	for i := 1; i < len(v); i++ {
		switch v[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return -1
}

// commentStart finds an inline comment in an unquoted value: a # after
// whitespace.
func commentStart(v string) int {
	for i := 1; i < len(v); i++ {
		if v[i] == '#' && (v[i-1] == ' ' || v[i-1] == '\t') {
			return i
		}
	}
	return -1
}

func hasRef(s string) bool {
	_, refs, _ := expand(s, true, func(string) (string, bool) { return "", true })
	return len(refs) > 0
}

func isNameStart(c byte) bool {
	return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func isNameChar(c byte) bool { return isNameStart(c) || (c >= '0' && c <= '9') }
