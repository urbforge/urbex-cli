package komodo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStackShell_RunsInTheStacksContainerAndReadsTheExitCode(t *testing.T) {
	var bodies []map[string]any
	var deleted map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case "/terminal/execute":
			bodies = append(bodies, body)
			w.Write([]byte("\n17\r\nline two\r\n\n__KOMODO_EXIT_CODE:3\n__KOMODO_END_OF_OUTPUT__"))
		case "/write/DeleteTerminal":
			deleted = body
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	sh := New(server.URL, "k", "s").Shell("shop-db-prod", "app", "urbex")
	for i := 0; i < 2; i++ {
		out, code, err := sh.Run(context.Background(), "cat $PGDATA/PG_VERSION")
		if err != nil || code != 3 || out != "17\nline two" {
			t.Fatalf("got %q, %d, %v", out, code, err)
		}
	}
	target := bodies[0]["target"].(map[string]any)
	if target["type"] != "Stack" || target["params"].(map[string]any)["stack"] != "shop-db-prod" || bodies[0]["terminal"] != "urbex" {
		t.Errorf("body = %v", bodies[0])
	}
	if bodies[0]["init"].(map[string]any)["recreate"] != "Always" || bodies[1]["init"].(map[string]any)["recreate"] != "Never" {
		t.Errorf("the first command starts a fresh shell, the next ones keep it: %v / %v", bodies[0]["init"], bodies[1]["init"])
	}
	if _, _, err := sh.Run(context.Background(), "a\nb"); err == nil {
		t.Error("a command of two lines should be refused")
	}
	if err := sh.Close(context.Background()); err != nil || deleted["terminal"] != "urbex" {
		t.Errorf("Close should delete the terminal: %v, %v", err, deleted)
	}
}

func TestParseTerminalOutput_WithoutExitCodeFails(t *testing.T) {
	if _, _, err := parseTerminalOutput("\npartial"); err == nil {
		t.Error("expected an error")
	}
}
