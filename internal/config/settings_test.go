package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2017fighting/mdc-lite/internal/config"
)

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func env(m map[string]string) config.Env {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func load(t *testing.T, body string, opts config.LoadOptions) (config.Settings, error) {
	t.Helper()
	dir := t.TempDir()
	p := writeFile(t, dir, "config.yaml", body)
	return config.Load(p, opts)
}

func TestLoad_NoFileUsesDefaults(t *testing.T) {
	s, err := config.Load(filepath.Join(t.TempDir(), "missing.yaml"), config.LoadOptions{})
	if err != nil {
		t.Fatalf("a missing config file must not be an error: %v", err)
	}
	if s.Listen != ":9208" {
		t.Errorf("listen = %q, want :9208", s.Listen)
	}
	if s.DBPath != "/config/mdc.db" {
		t.Errorf("db_path = %q", s.DBPath)
	}
	if s.PipelinesDir != "/config/pipelines" || s.TemplatesDir != "/config/templates" {
		t.Errorf("dirs = %q %q", s.PipelinesDir, s.TemplatesDir)
	}
	if s.Log.Level != "info" || s.Log.Format != "text" {
		t.Errorf("log = %+v", s.Log)
	}
	if len(s.BrowseRoots) != 1 || s.BrowseRoots[0].Path != "/media" || s.BrowseRoots[0].Label != "/media" {
		t.Errorf("browse_roots = %+v", s.BrowseRoots)
	}
	if s.Concurrency != (config.Concurrency{Tasks: 4, Requests: 8, Images: 4}) {
		t.Errorf("concurrency = %+v", s.Concurrency)
	}
	if s.RetryLimit != 5 {
		t.Errorf("retry_limit = %d", s.RetryLimit)
	}
	if s.Proxy != "" || s.Webhook.Enabled || s.Webhook.Token != "" {
		t.Errorf("proxy/webhook = %q %+v", s.Proxy, s.Webhook)
	}
	if got := s.SourceOf("listen"); got != config.SourceDefault {
		t.Errorf("listen source = %q, want default", got)
	}
}

func TestLoad_UnknownTopLevelKeyIsRefusedWithPosition(t *testing.T) {
	_, err := load(t, "listen: \":1\"\nlistne: \":2\"\n", config.LoadOptions{})
	if err == nil {
		t.Fatal("an unknown top-level key must be refused")
	}
	if !strings.Contains(err.Error(), ":2:1:") || !strings.Contains(err.Error(), "listne") {
		t.Errorf("error should name file:line:col and the key, got: %v", err)
	}
}

func TestLoad_UnknownNestedKeyIsRefusedWithPosition(t *testing.T) {
	_, err := load(t, "log:\n  level: info\n  fromat: json\n", config.LoadOptions{})
	if err == nil {
		t.Fatal("an unknown key under log must be refused")
	}
	if !strings.Contains(err.Error(), ":3:3:") || !strings.Contains(err.Error(), "log.fromat") {
		t.Errorf("got: %v", err)
	}
}

func TestLoad_UnknownKeyInsideListElementIsRefused(t *testing.T) {
	_, err := load(t, "browse_roots:\n  - path: /a\n    labl: A\n", config.LoadOptions{})
	if err == nil {
		t.Fatal("an unknown key inside a list element must be refused")
	}
	if !strings.Contains(err.Error(), "browse_roots[0].labl") {
		t.Errorf("got: %v", err)
	}
}

func TestLoad_OverrideOrder(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "config.yaml", "listen: \"${MDC_TEST_ADDR}\"\ndb_path: /from-file.db\n")
	p := filepath.Join(dir, "config.yaml")

	s, err := config.Load(p, config.LoadOptions{LookupEnv: env(map[string]string{"MDC_TEST_ADDR": ":1111"})})
	if err != nil {
		t.Fatal(err)
	}
	if s.Listen != ":1111" {
		t.Errorf("env interpolation did not reach the value: %q", s.Listen)
	}
	if s.DBPath != "/from-file.db" {
		t.Errorf("file value lost: %q", s.DBPath)
	}
	if got := s.SourceOf("listen"); got != config.SourceEnv {
		t.Errorf("listen source = %q, want env", got)
	}
	if got := s.SourceOf("db_path"); got != config.SourceFile {
		t.Errorf("db_path source = %q, want file", got)
	}

	flag := "/flag.db"
	s, err = config.Load(p, config.LoadOptions{
		LookupEnv: env(map[string]string{"MDC_TEST_ADDR": ":1111"}),
		Flags:     config.Flags{DBPath: &flag},
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.DBPath != "/flag.db" {
		t.Errorf("flag must win over the file: %q", s.DBPath)
	}
	if got := s.SourceOf("db_path"); got != config.SourceFlag {
		t.Errorf("db_path source = %q, want flag", got)
	}
}

func TestLoad_MissingEnvVarRefusesToStart(t *testing.T) {
	_, err := load(t, "listen: \"${MDC_NOT_SET_ANYWHERE}\"\n", config.LoadOptions{LookupEnv: env(nil)})
	if err == nil {
		t.Fatal("a ${VAR} with no value must be an error")
	}
	if !strings.Contains(err.Error(), "MDC_NOT_SET_ANYWHERE") {
		t.Errorf("error should name the variable: %v", err)
	}
}

func TestLoad_EnvDefaultForm(t *testing.T) {
	s, err := load(t, "listen: \"${MDC_UNSET_ADDR:-:7000}\"\n", config.LoadOptions{LookupEnv: env(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if s.Listen != ":7000" {
		t.Errorf("listen = %q", s.Listen)
	}
}

func TestLoad_ConcurrencyMustBePositive(t *testing.T) {
	_, err := load(t, "concurrency:\n  tasks: 0\n", config.LoadOptions{})
	if err == nil {
		t.Fatal("concurrency.tasks: 0 must be refused")
	}
	if !strings.Contains(err.Error(), "concurrency.tasks") {
		t.Errorf("got: %v", err)
	}
}

func TestLoad_LogLevelEnum(t *testing.T) {
	_, err := load(t, "log:\n  level: loud\n", config.LoadOptions{})
	if err == nil {
		t.Fatal("an unknown log level must be refused")
	}
}

func TestLoad_BrowseRootsAreObjects(t *testing.T) {
	s, err := load(t, "browse_roots:\n  - path: /media/a\n  - path: /media/b\n    label: B\n", config.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.BrowseRoots) != 2 {
		t.Fatalf("browse_roots = %+v", s.BrowseRoots)
	}
	if s.BrowseRoots[0].Label != "/media/a" {
		t.Errorf("a missing label must default to the path, got %q", s.BrowseRoots[0].Label)
	}
	if s.BrowseRoots[1].Label != "B" {
		t.Errorf("label = %q", s.BrowseRoots[1].Label)
	}
}

func TestLoad_BrowseRootsStringIsRefused(t *testing.T) {
	_, err := load(t, "browse_roots: [\"/media\"]\n", config.LoadOptions{})
	if err == nil {
		t.Fatal("browse_roots is an object array; a string array must be refused")
	}
}

func TestEffective_NeverPrintsSecrets(t *testing.T) {
	s, err := load(t, "webhook:\n  enabled: true\n  token: hunter2\nproxy: http://user:pw@proxy:8080\n", config.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, kv := range s.Effective() {
		all.WriteString(kv.Key + "=" + kv.Value + "\n")
	}
	out := all.String()
	if strings.Contains(out, "hunter2") {
		t.Errorf("the webhook token leaked:\n%s", out)
	}
	if strings.Contains(out, "pw@") {
		t.Errorf("the proxy password leaked:\n%s", out)
	}
	if !strings.Contains(out, "webhook.token=(set)") {
		t.Errorf("a set secret must still be reported as set:\n%s", out)
	}
}

func TestRedactURL(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", "(none)"},
		{"http://proxy:8080", "http://proxy:8080"},
		{"http://u:p@proxy:8080", "http://u:***@proxy:8080"},
		{"socks5h://u:p@proxy:1080", "socks5h://u:***@proxy:1080"},
	} {
		if got := config.RedactURL(tc.in); got != tc.want {
			t.Errorf("RedactURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestExpandEnv(t *testing.T) {
	lookup := func(k string) (string, bool) {
		if k == "SET" {
			return "value", true
		}
		return "", false
	}
	for _, tc := range []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"plain", "plain", false},
		{"${SET}", "value", false},
		{"a${SET}b", "avalueb", false},
		{"${UNSET:-fallback}", "fallback", false},
		{"${UNSET}", "", true},
		{"${}", "${}", false},
		{"$SET", "$SET", false},
		{"${NOT-VALID}", "${NOT-VALID}", false},
	} {
		got, _, err := config.ExpandEnv(tc.in, lookup)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%q: expected an error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%q = %q, want %q", tc.in, got, tc.want)
		}
	}
}
