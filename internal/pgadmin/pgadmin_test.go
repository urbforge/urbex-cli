package pgadmin

import (
	"context"
	"encoding/base64"
	"regexp"
	"strings"
	"testing"
)

// shell records the commands and answers psql with output.
type shell struct {
	commands []string
	output   string
	code     int
}

func (s *shell) Run(ctx context.Context, command string) (string, int, error) {
	s.commands = append(s.commands, command)
	if strings.Contains(command, "psql") {
		return s.output, s.code, nil
	}
	return "", 0, nil
}

func (s *shell) Close(ctx context.Context) error { return nil }

// sent is the script the commands carried, decoded.
func (s *shell) sent(t *testing.T) string {
	t.Helper()
	var enc string
	for _, c := range s.commands {
		switch {
		case strings.HasPrefix(c, `URBEX_SQL="$URBEX_SQL"`):
			enc += strings.TrimPrefix(c, `URBEX_SQL="$URBEX_SQL"`)
		case strings.HasPrefix(c, "URBEX_SQL="):
			enc = strings.TrimPrefix(c, "URBEX_SQL=")
		}
	}
	data, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestEnsureRoles_SendsTheScriptInShortLinesWithoutThePasswords(t *testing.T) {
	sh := &shell{output: "psql:<stdin>:12: NOTICE:  urbex: created the role api\nDO\npsql:<stdin>:30: NOTICE:  urbex: set the password of worker"}
	roles := []Role{{"api", "pw-api-0123456789"}, {"worker", "pw-worker-0123456789"}, {"admin_ui", "pw-admin-0123456789"}}
	changed, err := Server{Shell: sh}.EnsureRoles(context.Background(), roles)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(changed, "; ") != "created the role api; set the password of worker" {
		t.Errorf("changed = %q", changed)
	}
	if len(sh.commands) < 3 {
		t.Errorf("a long script goes in several commands, got %d", len(sh.commands))
	}
	for _, c := range sh.commands {
		if len(c) > 700 || strings.ContainsAny(c, "\n'") && !strings.Contains(c, "psql") {
			t.Errorf("a command too long or quoted for a terminal: %q", c)
		}
		for _, r := range roles {
			if strings.Contains(c, r.Password) {
				t.Errorf("a password on a command line: %q", c)
			}
		}
	}
	sql := sh.sent(t)
	for _, r := range roles {
		if strings.Contains(sql, r.Password) || !strings.Contains(sql, Verifier(r.Name, r.Password)) {
			t.Errorf("%s: the script should carry its verifier, never its password", r.Name)
		}
		for _, want := range []string{`CREATE SCHEMA "` + r.Name + `" AUTHORIZATION "` + r.Name + `"`, `SET search_path = "` + r.Name + `"`} {
			if !strings.Contains(sql, want) {
				t.Errorf("the script lacks %s", want)
			}
		}
	}
	if !strings.Contains(sql, "REVOKE ALL ON SCHEMA public FROM PUBLIC") {
		t.Error("the script should take the rights on public")
	}
	if !strings.Contains(sh.commands[len(sh.commands)-1], `-U "$POSTGRES_USER"`) {
		t.Errorf("psql should run as the admin, from the container's variables: %s", sh.commands[len(sh.commands)-1])
	}
}

func TestEnsureRoles_RefusesANameThatIsNoIdentifier(t *testing.T) {
	if _, err := (Server{Shell: &shell{}}).EnsureRoles(context.Background(), []Role{{`x"; DROP`, "pw"}}); err == nil {
		t.Error("expected a refusal")
	}
}

func TestScript_FailsWithPsqlsError(t *testing.T) {
	sh := &shell{output: "ERROR:  permission denied", code: 3}
	_, err := Server{Shell: sh}.Spaces(context.Background())
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("err = %v", err)
	}
}

func TestVerifier_IsPostgresFormatAndStable(t *testing.T) {
	v := Verifier("api", "secret")
	if !regexp.MustCompile(`^SCRAM-SHA-256\$4096:[A-Za-z0-9+/=]{24}\$[A-Za-z0-9+/=]{44}:[A-Za-z0-9+/=]{44}$`).MatchString(v) {
		t.Errorf("not a SCRAM-SHA-256 verifier: %s", v)
	}
	if Verifier("api", "secret") != v {
		t.Error("the same password should give the same verifier")
	}
	if Verifier("api", "other") == v || Verifier("worker", "secret") == v {
		t.Error("another password or role should give another verifier")
	}
}

func TestSpacesAndUnused(t *testing.T) {
	sh := &shell{output: "admin_ui|admin_ui\napi|api\nreporting|\n"}
	spaces, err := Server{Shell: sh}.Spaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(spaces) != 3 {
		t.Fatalf("spaces = %v", spaces)
	}
	// reporting has no schema of its own: no role urbex made.
	if got := Unused(spaces, map[string]bool{"api": true}); strings.Join(got, ",") != "admin_ui" {
		t.Errorf("unused = %v", got)
	}
}

func TestDataVersion(t *testing.T) {
	for out, want := range map[string]string{"17\n": "17", "none": ""} {
		sh := &versionShell{out: out}
		if got, err := (Server{Shell: sh}).DataVersion(context.Background()); err != nil || got != want {
			t.Errorf("%q: got %q, %v", out, got, err)
		}
	}
}

type versionShell struct{ out string }

func (s *versionShell) Run(ctx context.Context, command string) (string, int, error) {
	return s.out, 0, nil
}
func (s *versionShell) Close(ctx context.Context) error { return nil }
