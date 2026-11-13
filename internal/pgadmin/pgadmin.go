// Package pgadmin is what urbex asks a postgres provider (ADR-0030):
// whether it takes connections, its data directory's major version, and
// the role and schema of each application that uses it - created, listed
// and dropped. It runs everything in the provider's own container, as
// the server's admin user over the local socket, through a Shell: no
// port, no SQL client where urbex runs, no container of its own.
package pgadmin

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Shell runs one-line commands in the provider's container, one after
// the other in the same shell, until Close.
type Shell interface {
	Run(ctx context.Context, command string) (output string, exitCode int, err error)
	Close(ctx context.Context) error
}

// Server is a postgres provider, reached through its container's shell.
type Server struct {
	Shell Shell
}

// Role is an application's space on the server: a role and a schema of
// that name, and the role's password.
type Role struct {
	Name, Password string
}

// Space is a role the server has and its schema of the same name, ""
// when it has none.
type Space struct {
	Role, Schema string
}

var identRE = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// chunk is how much of a script one command carries: the shell reads a
// command from a terminal, whose line editing caps a line (busybox ash
// at 1024 bytes), and Periphery wraps it in its own marks.
const chunk = 600

// psql runs the script on stdin as the server's admin user, on its
// local socket, stopping at the first error. The variables are the
// container's: the command line holds no password.
const psql = `PGPASSWORD="$POSTGRES_PASSWORD" psql -X -q -A -t -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB"`

// Ready reports whether the server takes connections on TCP: initdb's
// temporary server listens only on the socket.
func (s Server) Ready(ctx context.Context) (bool, error) {
	_, code, err := s.Shell.Run(ctx, `pg_isready -q -h 127.0.0.1 -U "$POSTGRES_USER" -d "$POSTGRES_DB"`)
	return err == nil && code == 0, err
}

// DataVersion is the major version of the data directory (PG_VERSION),
// "" when it has none yet.
func (s Server) DataVersion(ctx context.Context) (string, error) {
	out, code, err := s.Shell.Run(ctx, `if [ -f "$PGDATA/PG_VERSION" ]; then cat "$PGDATA/PG_VERSION"; else echo none; fi`)
	if err != nil {
		return "", err
	}
	out = strings.TrimSpace(out)
	if code != 0 || out == "" {
		return "", fmt.Errorf("reading PG_VERSION: %s", out)
	}
	if out == "none" {
		return "", nil
	}
	return out, nil
}

// EnsureRoles makes sure each role exists with its password, its schema
// and its search_path, and that no one but the admin has rights on
// public; it changes only what isn't so, and returns what it changed.
func (s Server) EnsureRoles(ctx context.Context, roles []Role) ([]string, error) {
	sql := []string{IsolationSQL()}
	for _, r := range roles {
		q, err := RoleSQL(r)
		if err != nil {
			return nil, err
		}
		sql = append(sql, q)
	}
	out, err := s.script(ctx, strings.Join(sql, "\n"), false)
	if err != nil {
		return nil, err
	}
	return notices(out), nil
}

// Spaces are the roles that can log in, but the admin, with their schema
// of the same name and owned by them.
func (s Server) Spaces(ctx context.Context) ([]Space, error) {
	out, err := s.script(ctx, SpacesSQL, false)
	if err != nil {
		return nil, err
	}
	var spaces []Space
	for _, line := range strings.Split(out, "\n") {
		role, schema, ok := strings.Cut(strings.TrimSpace(line), "|")
		if ok && identRE.MatchString(role) {
			spaces = append(spaces, Space{Role: role, Schema: schema})
		}
	}
	return spaces, nil
}

// Drop deletes each role, its schema and everything they hold, in one
// transaction.
func (s Server) Drop(ctx context.Context, roles []string) error {
	var sql []string
	for _, r := range roles {
		if !identRE.MatchString(r) {
			return fmt.Errorf("%q is no role urbex made", r)
		}
		sql = append(sql, fmt.Sprintf(`DROP SCHEMA IF EXISTS "%[1]s" CASCADE; DROP OWNED BY "%[1]s"; DROP ROLE "%[1]s";`, r))
	}
	_, err := s.script(ctx, strings.Join(sql, "\n"), true)
	return err
}

// script runs sql through psql: in chunks of base64 kept in a shell
// variable, then decoded into psql's stdin - so that neither its length
// nor its quotes meet the terminal.
func (s Server) script(ctx context.Context, sql string, transaction bool) (string, error) {
	enc := base64.StdEncoding.EncodeToString([]byte(sql))
	for i, op := 0, "URBEX_SQL="; i < len(enc); i, op = i+chunk, `URBEX_SQL="$URBEX_SQL"` {
		end := min(i+chunk, len(enc))
		if _, code, err := s.Shell.Run(ctx, op+enc[i:end]); err != nil || code != 0 {
			return "", fmt.Errorf("sending SQL to the server's container: exit %d: %v", code, err)
		}
	}
	single := ""
	if transaction {
		single = " -1"
	}
	out, code, err := s.Shell.Run(ctx, `printf %s "$URBEX_SQL" | base64 -d | `+psql+single+` 2>&1`)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("psql failed (exit %d): %s", code, strings.TrimSpace(out))
	}
	return out, nil
}

// noticeMark starts what the SQL reports it changed.
const noticeMark = "urbex: "

func notices(out string) []string {
	var changed []string
	for _, line := range strings.Split(out, "\n") {
		if _, rest, ok := strings.Cut(line, "NOTICE:  "+noticeMark); ok {
			changed = append(changed, strings.TrimSpace(rest))
		}
	}
	return changed
}

// IsolationSQL takes from everyone the rights PostgreSQL gives on the
// schema public: the applications keep to their own schemas.
func IsolationSQL() string {
	return `DO $urbex$
BEGIN
  IF EXISTS (SELECT FROM pg_namespace n, aclexplode(COALESCE(n.nspacl, acldefault('n', n.nspowner))) a
             WHERE n.nspname = 'public' AND a.grantee = 0) THEN
    REVOKE ALL ON SCHEMA public FROM PUBLIC;
    RAISE NOTICE '` + noticeMark + `took the rights on public from everyone';
  END IF;
END
$urbex$;`
}

// RoleSQL makes sure r's role exists, logs in with its password, owns
// its schema and searches it first; each step only when it isn't so.
// The password is set as a SCRAM-SHA-256 verifier: the password itself
// is never sent.
func RoleSQL(r Role) (string, error) {
	if !identRE.MatchString(r.Name) {
		return "", fmt.Errorf("%q can't be a role's name", r.Name)
	}
	if r.Password == "" {
		return "", fmt.Errorf("%s has no password", r.Name)
	}
	v := Verifier(r.Name, r.Password)
	return fmt.Sprintf(`DO $urbex$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '%[1]s') THEN
    CREATE ROLE "%[1]s" LOGIN;
    RAISE NOTICE '%[3]screated the role %[1]s';
  END IF;
  IF (SELECT rolpassword FROM pg_authid WHERE rolname = '%[1]s') IS DISTINCT FROM '%[2]s'
     OR NOT (SELECT rolcanlogin FROM pg_roles WHERE rolname = '%[1]s') THEN
    ALTER ROLE "%[1]s" LOGIN PASSWORD '%[2]s';
    RAISE NOTICE '%[3]sset the password of %[1]s';
  END IF;
  IF NOT EXISTS (SELECT FROM pg_namespace WHERE nspname = '%[1]s') THEN
    CREATE SCHEMA "%[1]s" AUTHORIZATION "%[1]s";
    RAISE NOTICE '%[3]screated the schema %[1]s';
  ELSIF (SELECT nspowner FROM pg_namespace WHERE nspname = '%[1]s') <> (SELECT oid FROM pg_roles WHERE rolname = '%[1]s') THEN
    ALTER SCHEMA "%[1]s" OWNER TO "%[1]s";
    RAISE NOTICE '%[3]sgave the schema %[1]s to its role';
  END IF;
  IF NOT EXISTS (SELECT FROM pg_db_role_setting s JOIN pg_roles r ON r.oid = s.setrole
                 WHERE r.rolname = '%[1]s' AND s.setdatabase = 0 AND 'search_path=%[1]s' = ANY (s.setconfig)) THEN
    ALTER ROLE "%[1]s" SET search_path = "%[1]s";
    RAISE NOTICE '%[3]sset the search_path of %[1]s';
  END IF;
END
$urbex$;`, r.Name, v, noticeMark), nil
}

// SpacesSQL lists the roles that log in, but superusers, each with its
// schema of the same name if it owns one.
const SpacesSQL = `SELECT r.rolname || '|' || COALESCE(n.nspname, '')
FROM pg_roles r LEFT JOIN pg_namespace n ON n.nspname = r.rolname AND n.nspowner = r.oid
WHERE r.rolcanlogin AND NOT r.rolsuper AND r.rolname !~ '^pg_'
ORDER BY r.rolname;`

// Verifier is password's SCRAM-SHA-256 verifier, as PostgreSQL stores
// it (4096 iterations). Its salt comes from the role and the password, so
// that the same password gives the same verifier: an apply that changes
// nothing sets nothing.
func Verifier(role, password string) string {
	mac := hmac.New(sha256.New, []byte(password))
	mac.Write([]byte("urbex scram salt " + role))
	salt := mac.Sum(nil)[:16]
	const iterations = 4096
	salted, err := pbkdf2.Key(sha256.New, password, salt, iterations, sha256.Size)
	if err != nil {
		panic(err) // only for a key length FIPS mode refuses
	}
	clientKey := hmacSum(salted, "Client Key")
	storedKey := sha256.Sum256(clientKey)
	serverKey := hmacSum(salted, "Server Key")
	b64 := base64.StdEncoding.EncodeToString
	return fmt.Sprintf("SCRAM-SHA-256$%d:%s$%s:%s", iterations, b64(salt), b64(storedKey[:]), b64(serverKey))
}

func hmacSum(key []byte, msg string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(msg))
	return mac.Sum(nil)
}

// Unused are the spaces no role among used names: roles with their own
// schema, as urbex makes them.
func Unused(spaces []Space, used map[string]bool) []string {
	var out []string
	for _, s := range spaces {
		if s.Schema == s.Role && !used[s.Role] {
			out = append(out, s.Role)
		}
	}
	sort.Strings(out)
	return out
}
