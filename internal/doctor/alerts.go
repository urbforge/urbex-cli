package doctor

import (
	"context"
	"strings"

	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/grafana"
	"github.com/urbforge/urbex-cli/internal/platform"
)

// AlertsTester sends a test notification through a Grafana contact point
// (grafana.Client).
type AlertsTester interface {
	TestContactPoint(ctx context.Context, title, summary string) ([]grafana.TestResult, error)
}

// AlertsChecks checks the channels of observability.alerts: their
// credentials and, with t (urbex doctor --test-alerts), a test message
// through each of them. Without channels it reports that the alerts stay
// in Grafana.
func AlertsChecks(ctx context.Context, t AlertsTester, cfg *platform.Config, creds *credentials.Credentials, r *Report) {
	add := func(name, status, detail, fix string) {
		r.add(Check{Group: "alerts", Name: name, Status: status, Detail: detail, Fix: fix})
	}
	if cfg == nil {
		return
	}
	a := cfg.Observability.Alerts
	if !a.Any() {
		add("channels", Skip, "none in observability.alerts: the alerts stay in Grafana's Alerting page", "")
		return
	}
	creds2 := []struct {
		on        bool
		name, key string
		value     string
	}{
		{a.Telegram != nil, "telegram", "telegramBotToken", creds.TelegramBotToken},
		{a.Discord, "discord", "discordWebhookUrl", creds.DiscordWebhookURL},
		{a.Webhook, "webhook", "alertsWebhookUrl", creds.AlertsWebhookURL},
	}
	missing := false
	for _, c := range creds2 {
		if !c.on {
			continue
		}
		if c.value == "" {
			missing = true
			add(c.name, Fail, "no "+c.key+" in the credentials",
				"store it with 'urbex-op set "+c.key+"' (docs/reference/credentials.md), then 'urbex bootstrap'")
			continue
		}
		if t == nil {
			add(c.name, OK, c.key+" set; 'urbex doctor --test-alerts' sends a test message", "")
		}
	}
	if t == nil || missing {
		return
	}
	results, err := t.TestContactPoint(ctx, "urbex", "A test from urbex doctor --test-alerts: this channel gets Urbex's alerts.")
	if err != nil {
		add("test", Fail, firstLine(err.Error()), "run 'urbex bootstrap' after adding observability.alerts, and check that Grafana is up ('urbex status')")
		return
	}
	for _, res := range results {
		if res.OK {
			add(res.Type, OK, "test message sent", "")
			continue
		}
		add(res.Type, Fail, "test message failed: "+firstLine(res.Error), alertsFix(res.Type, res.Error))
	}
}

func alertsFix(kind, err string) string {
	switch {
	case kind == "telegram" && strings.Contains(err, "401"):
		return "the bot token is wrong: copy it again from BotFather, 'urbex-op set telegramBotToken', then 'urbex bootstrap'"
	case kind == "telegram" && (strings.Contains(err, "chat not found") || strings.Contains(err, "400")):
		return "check observability.alerts.telegram.chatId, and that the bot is a member of that chat"
	case kind == "discord":
		return "check the channel's webhook (Server Settings > Integrations > Webhooks), 'urbex-op set discordWebhookUrl', then 'urbex bootstrap'"
	}
	return "check the channel's address, and that Grafana's LXC reaches it"
}
