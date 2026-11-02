package envrepo

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/manifest"
)

// waitFor runs the wait_for the wait generates against a provider whose
// answer at each moment is answers (a shell condition on $s, the seconds
// since the start, or $t, the quarter seconds), on a fake clock: sleep,
// date and getent are shell functions. It returns "ready <s>" or
// "gaveup <s>".
func waitFor(t *testing.T, budget int, answers string) string {
	t.Helper()
	var b strings.Builder
	writeWait(&b, &Links{Waits: []Wait{{Name: "p", Host: "p", Port: 1, Healthcheck: "/"}}, WaitSeconds: budget})
	compose := b.String()
	start := strings.Index(compose, "        wait_for() {\n")
	stop := strings.Index(compose[start:], "\n        }\n")
	if start < 0 || stop < 0 {
		t.Fatalf("no wait_for in:\n%s", compose)
	}
	fn := strings.ReplaceAll(compose[start:start+stop+len("\n        }")], "$$", "$")
	fn = strings.ReplaceAll(fn, "\n        ", "\n")
	script := fmt.Sprintf(`t=0
date() { echo $((t / 4)); }
sleep() { case $1 in %s) t=$((t + 1)) ;; *) t=$((t + 4 * $1)) ;; esac; }
getent() { echo "10.0.0.1 $2"; }
probe() { [ "$1" = 10.0.0.1 ] || exit 2; s=$((t / 4)); %s; }
end=$(( $(date +%%s) + %d ))
name=p host=p
%s
if wait_for; then echo "ready $(date +%%s)"; else echo "gaveup $(date +%%s)"; fi
`, WaitProbeInterval, answers, budget, strings.TrimSpace(fn))
	out, err := exec.Command("sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s\n%s", err, out, script)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return lines[len(lines)-1]
}

func TestWait_ASteadyProviderIsReadyAfterTheStableWindow(t *testing.T) {
	if got, want := waitFor(t, 120, "true"), fmt.Sprintf("ready %d", manifest.ReadyStableSeconds); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestWait_AProviderInACrashLoopShorterThanTheWindowNeverPasses(t *testing.T) {
	// Up for u seconds, down for one, again and again: the benchmark's
	// crash loops (urbforge/urbex-cli#43, D3-D5).
	for _, up := range []int{3, 6, 12} {
		cycle := 4 * (up + 1)
		if got := waitFor(t, 180, fmt.Sprintf("[ $((t %% %d)) -lt %d ]", cycle, 4*up)); !strings.HasPrefix(got, "gaveup") {
			t.Errorf("up %d s, down 1 s: %s, want it never ready", up, got)
		}
	}
}

func TestWait_EveryFailureStartsTheWindowOver(t *testing.T) {
	// A single failed answer at 10 s: ready only a full window after it.
	got := waitFor(t, 120, "[ $s -ne 10 ]")
	var at int
	if _, err := fmt.Sscanf(got, "ready %d", &at); err != nil || at < 10+manifest.ReadyStableSeconds {
		t.Errorf("got %q, want ready no earlier than %d", got, 10+manifest.ReadyStableSeconds)
	}
}

func TestWait_ARestartShorterThanASecondStartsTheWindowOver(t *testing.T) {
	// Down for a quarter of a second at 10 s: probes a second apart could
	// step over it.
	got := waitFor(t, 120, "[ $t -ne 41 ]")
	var at int
	if _, err := fmt.Sscanf(got, "ready %d", &at); err != nil || at < 10+manifest.ReadyStableSeconds {
		t.Errorf("got %q, want ready no earlier than %d", got, 10+manifest.ReadyStableSeconds)
	}
}

func TestWait_AProviderThatNeverAnswersIsGivenUpOnAtTheBudget(t *testing.T) {
	got := waitFor(t, 60, "false")
	var at int
	if _, err := fmt.Sscanf(got, "gaveup %d", &at); err != nil || at < 60 || at > 60+2*WaitMaxBackoffSeconds {
		t.Errorf("got %q, want it given up at about 60 s", got)
	}
}
