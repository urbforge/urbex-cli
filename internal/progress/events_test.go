package progress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func decode(t *testing.T, data []byte) []Event {
	t.Helper()
	var out []Event
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatalf("not a JSON line: %s", line)
		}
		out = append(out, e)
	}
	return out
}

func types(events []Event) string {
	var ts []string
	for _, e := range events {
		t := e.Type
		if e.Phase != "" {
			t += ":" + e.Phase
		}
		ts = append(ts, t)
	}
	return strings.Join(ts, " ")
}

// A run's events, live and in its events file, in order: the run, each
// phase's start and end, the steps from Ansible's and Terraform's
// output, a skipped phase, a failure and the run's end.
func TestEvents(t *testing.T) {
	dir, c := t.TempDir(), newClock()
	var live bytes.Buffer
	tr, err := start(dir, "bootstrap", "urbex bootstrap", specs, c.now, false, Options{Log: "/logs/bootstrap.log", Live: &live})
	if err != nil {
		t.Fatal(err)
	}
	tr.Skip("template", "the current one is there")
	tr.Begin("lxcs")
	w := tr.Watch(&bytes.Buffer{})
	fmt.Fprintln(w, `module.service["urbex-gitea"].proxmox_virtual_environment_container.this: Creation complete after 12s [id=9000]`)
	time.Sleep(50 * time.Millisecond) // Watch reads in a goroutine, and the fake clock isn't locked
	c.add(40 * time.Second)
	fmt.Fprintln(w, "PLAY [Configure all urbex base-service containers] ****")
	fmt.Fprintln(w, "TASK [common : Install Docker Engine] ****")
	time.Sleep(50 * time.Millisecond) // Watch reads in a goroutine
	c.add(10 * time.Second)
	tr.Finish(errors.New("ansible-playbook: exit status 2"))

	got := decode(t, live.Bytes())
	want := "run-start phase-skip:template phase-start:lxcs step:lxcs phase-end:lxcs phase-start:common step:common phase-fail:common run-end"
	if types(got) != want {
		t.Errorf("events =\n%s\nwant\n%s", types(got), want)
	}
	if e := got[0]; e.Command != "urbex bootstrap" || e.Log != "/logs/bootstrap.log" || e.Kind != "bootstrap" || e.Run == "" {
		t.Errorf("run-start = %+v", e)
	}
	if e := got[3]; e.Step != `module.service["urbex-gitea"].proxmox_virtual_environment_container.this: creation complete after 12s` {
		t.Errorf("terraform step = %q", e.Step)
	}
	if e := got[4]; e.Seconds == nil || time.Duration(*e.Seconds) != 40*time.Second {
		t.Errorf("phase-end = %+v", e)
	}
	if e := got[6]; e.Step != "common : Install Docker Engine" {
		t.Errorf("ansible step = %q", e.Step)
	}
	if e := got[len(got)-1]; e.Result != ResultFailed || !strings.Contains(e.Error, "exit status 2") {
		t.Errorf("run-end = %+v", e)
	}

	// The file has the same events, for Follow.
	file, _ := os.ReadFile(eventsPath(tr.path))
	if types(decode(t, file)) != want {
		t.Errorf("events file = %s", types(decode(t, file)))
	}
	if r, _ := Latest(dir); r.Log != "/logs/bootstrap.log" {
		t.Errorf("the record should name the log: %+v", r)
	}
}

// A reader that goes away doesn't stop the run.
func TestEvents_BrokenLiveWriter(t *testing.T) {
	dir, c := t.TempDir(), newClock()
	tr, err := start(dir, "apply", "urbex apply staging", specs, c.now, false, Options{Live: brokenWriter{}})
	if err != nil {
		t.Fatal(err)
	}
	tr.Begin("lxcs")
	tr.Finish(nil)
	file, _ := os.ReadFile(eventsPath(tr.path))
	if !strings.HasSuffix(types(decode(t, file)), "run-end") {
		t.Errorf("events file = %s", file)
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

// Follow replays a run from its first event; with follow, it waits for
// the next ones until the run ends.
func TestFollow(t *testing.T) {
	dir, c := t.TempDir(), newClock()
	c.add(time.Until(c.now()) * -1)
	tr := startAt(t, dir, c)
	tr.Begin("lxcs")

	var out bytes.Buffer
	if err := Follow(context.Background(), tr.path, &out, false, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if types(decode(t, out.Bytes())) != "run-start phase-start:lxcs" {
		t.Errorf("replay = %s", types(decode(t, out.Bytes())))
	}

	out.Reset()
	done := make(chan error)
	go func() { done <- Follow(context.Background(), tr.path, &syncBuffer{b: &out}, true, 5*time.Millisecond) }()
	time.Sleep(30 * time.Millisecond)
	tr.Finish(nil)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Follow didn't stop at the run's end")
	}
	if got := types(decode(t, out.Bytes())); got != "run-start phase-start:lxcs phase-end:lxcs run-end" {
		t.Errorf("followed = %s", got)
	}
}

// A run that stopped sending its heartbeat ends, for Follow, as stopped.
func TestFollow_Stale(t *testing.T) {
	dir, c := t.TempDir(), newClock()
	c.add(time.Since(c.now()) - time.Hour)
	tr := startAt(t, dir, c)
	var out bytes.Buffer
	if err := Follow(context.Background(), tr.path, &out, true, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	got := decode(t, out.Bytes())
	if last := got[len(got)-1]; last.Type != EventRunEnd || last.Result != "stopped" {
		t.Errorf("last = %+v", last)
	}
}

type syncBuffer struct{ b *bytes.Buffer }

func (s *syncBuffer) Write(p []byte) (int, error) { return s.b.Write(p) }

func TestStepOf(t *testing.T) {
	for line, want := range map[string]string{
		"TASK [periphery : Start Periphery via docker compose] ****":                                      "periphery : Start Periphery via docker compose",
		`module.vm["a-web-prod"].proxmox_virtual_environment_vm.this: Destruction complete after 7s`:      `module.vm["a-web-prod"].proxmox_virtual_environment_vm.this: destruction complete after 7s`,
		`module.service["a"].proxmox_virtual_environment_container.this: Still creating... [10s elapsed]`: "",
		"ok: [urbex-gitea]": "",
		// Terraform colours its output.
		"\x1b[0m\x1b[1mmodule.service[\"evtest-web-prod\"].proxmox_virtual_environment_container.this: Creation complete after 13s [id=9124]\x1b[0m": `module.service["evtest-web-prod"].proxmox_virtual_environment_container.this: creation complete after 13s`,
	} {
		if got := stepOf(line); got != want {
			t.Errorf("stepOf(%q) = %q, want %q", line, got, want)
		}
	}
}

// Old runs go with their events.
func TestPrune_Events(t *testing.T) {
	dir, c := t.TempDir(), newClock()
	for i := 0; i < history+2; i++ {
		tr := startAt(t, dir, c)
		tr.Finish(nil)
		c.add(time.Minute)
	}
	records, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	events, _ := filepath.Glob(filepath.Join(dir, "*.events"))
	if len(records) != history || len(events) != history {
		t.Errorf("%d records, %d events files, want %d each", len(records), len(events), history)
	}
}
