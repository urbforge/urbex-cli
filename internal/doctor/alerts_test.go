package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/grafana"
	"github.com/urbforge/urbex-cli/internal/platform"
)

type fakeTester struct {
	res []grafana.TestResult
	err error
	n   int
}

func (f *fakeTester) TestContactPoint(context.Context, string, string) ([]grafana.TestResult, error) {
	f.n++
	return f.res, f.err
}

func alertsOf(r *Report) string {
	var b []string
	for _, c := range r.Checks {
		b = append(b, c.Group+"/"+c.Name+"="+c.Status)
	}
	return strings.Join(b, " ")
}

func TestAlertsChecks(t *testing.T) {
	ctx := context.Background()
	tg := &platform.Config{Observability: platform.ObservabilityConfig{Alerts: platform.AlertsConfig{Telegram: &platform.TelegramAlerts{ChatID: "-1"}, Discord: true}}}
	full := &credentials.Credentials{TelegramBotToken: "t", DiscordWebhookURL: "d"}

	r := &Report{}
	AlertsChecks(ctx, nil, &platform.Config{}, full, r)
	if alertsOf(r) != "alerts/channels=skip" {
		t.Errorf("none configured: %s", alertsOf(r))
	}

	r = &Report{}
	AlertsChecks(ctx, nil, tg, &credentials.Credentials{TelegramBotToken: "t"}, r)
	if alertsOf(r) != "alerts/telegram=ok alerts/discord=fail" || !strings.Contains(r.Checks[1].Fix, "urbex-op set discordWebhookUrl") {
		t.Errorf("missing credential: %s %+v", alertsOf(r), r.Checks)
	}

	// A missing credential: no test is sent.
	f := &fakeTester{}
	r = &Report{}
	AlertsChecks(ctx, f, tg, &credentials.Credentials{TelegramBotToken: "t"}, r)
	if f.n != 0 {
		t.Error("tested with a credential missing")
	}

	f = &fakeTester{res: []grafana.TestResult{
		{Integration: grafana.Integration{Type: "telegram"}, Error: "webhook response status 401 Unauthorized"},
		{Integration: grafana.Integration{Type: "discord"}, OK: true},
	}}
	r = &Report{}
	AlertsChecks(ctx, f, tg, full, r)
	if alertsOf(r) != "alerts/telegram=fail alerts/discord=ok" || !strings.Contains(r.Checks[0].Fix, "BotFather") {
		t.Errorf("test results: %s %+v", alertsOf(r), r.Checks)
	}

	r = &Report{}
	AlertsChecks(ctx, &fakeTester{err: errors.New("grafana has no contact point urbex")}, tg, full, r)
	if alertsOf(r) != "alerts/test=fail" || !strings.Contains(r.Checks[0].Fix, "urbex bootstrap") {
		t.Errorf("no contact point: %s", alertsOf(r))
	}
}
