package secrets

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func lookupMap(m map[string]string) Lookup {
	return func(name string) (string, bool) {
		v, ok := m[name]
		return v, ok
	}
}

func TestRenderFile(t *testing.T) {
	secrets := lookupMap(map[string]string{
		"PASS":  `p$w"d\x` + "\nline2",
		"USER":  "app",
		"EMPTY": "",
	})
	tests := []struct {
		in, want string
	}{
		{"# comment ${PASS}\n", "# comment ${PASS}\n"},
		{"PLAIN=value\n", "PLAIN=value\n"},
		{"A=${PASS}\n", `A="p$$w\"d\\x\nline2"` + "\n"},
		{"A=$USER\n", `A="app"` + "\n"},
		{`A="${USER}:\n${PASS}"` + "\n", `A="app:\n` + `p$$w\"d\\x\nline2"` + "\n"},
		{"export URL=postgres://${USER}:${PASS}@db/app # note\n", `export URL="postgres://app:p$$w\"d\\x\nline2@db/app" # note` + "\n"},
		{"A='${PASS}'\n", "A='${PASS}'\n"},
		{"A=${MISSING:-fallback}\n", `A="fallback"` + "\n"},
		{"A=${EMPTY:-fallback}\n", `A="fallback"` + "\n"},
		{"A=${EMPTY-fallback}\n", `A=""` + "\n"},
		{"A=cost$$ ${USER}\n", `A="cost$$ app"` + "\n"},
		{"A=${USER}\r\n", "A=\"app\"\r\n"},
	}
	for _, tt := range tests {
		got, _, err := renderFile([]byte(tt.in), secrets)
		if err != nil {
			t.Errorf("renderFile(%q): %v", tt.in, err)
			continue
		}
		if string(got) != tt.want {
			t.Errorf("renderFile(%q)\n got %q\nwant %q", tt.in, got, tt.want)
		}
	}
}

func TestRenderFileErrors(t *testing.T) {
	for _, in := range []string{"A=${PASS\n", "A=${PASS:?required}\n", "A=${1X}\n", "A=\"multi ${PASS}\n"} {
		if _, _, err := renderFile([]byte(in), lookupMap(nil)); err == nil {
			t.Errorf("renderFile(%q): want error", in)
		}
	}
}

func TestRender(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".env", "DB_PASSWORD=${DB_PASSWORD}\nLEVEL=${LEVEL:-info}\n")
	write("config/app.env", "TOKEN=$TOKEN\nTOKEN2=${TOKEN}\n")
	write("config/plain.env", "A=1\n")
	write("compose.yaml", "x: ${NOT_AN_ENV_FILE}\n")

	r, err := Render(dir, "api", lookupMap(map[string]string{"DB_PASSWORD": "s3cret"}))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := r.Missing(), []string{"TOKEN"}; !slices.Equal(got, want) {
		t.Errorf("Missing() = %v, want %v", got, want)
	}
	if got := strings.Join(sortedKeys(r.Files), ","); got != ".env,config/app.env" {
		t.Errorf("rendered files = %s", got)
	}
	if got := string(r.Files[".env"]); got != "DB_PASSWORD=\"s3cret\"\nLEVEL=\"info\"\n" {
		t.Errorf(".env = %q", got)
	}
	if !strings.HasPrefix(r.Digest, "pbkdf2-sha256:") || strings.Contains(r.Digest, "s3cret") {
		t.Errorf("Digest = %q", r.Digest)
	}

	other, _ := Render(dir, "api", lookupMap(map[string]string{"DB_PASSWORD": "rotated"}))
	again, _ := Render(dir, "api", lookupMap(map[string]string{"DB_PASSWORD": "s3cret"}))
	if other.Digest == r.Digest || again.Digest != r.Digest {
		t.Errorf("digest must change with the values and only with them")
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv(EnvVar, `{"FROM_JSON":"j","BOTH":"json"}`)
	t.Setenv("BOTH", "env")
	lookup, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := lookup("FROM_JSON"); v != "j" {
		t.Errorf("FROM_JSON = %q", v)
	}
	if v, _ := lookup("BOTH"); v != "env" {
		t.Errorf("BOTH = %q, want the environment to win", v)
	}
	if _, ok := lookup("NOPE_NOT_SET"); ok {
		t.Errorf("NOPE_NOT_SET should be unset")
	}
	t.Setenv(EnvVar, "not json")
	if _, err := FromEnv(); err == nil {
		t.Errorf("want error for invalid %s", EnvVar)
	}
}

func sortedKeys(m map[string][]byte) []string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
