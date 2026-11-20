package bootstrap

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/platform"
)

func TestAlertsProvisioning_NoneConfigured(t *testing.T) {
	file, secrets, err := AlertsProvisioning(platform.AlertsConfig{}, &credentials.Credentials{})
	if file != "" || secrets != nil || err != nil {
		t.Fatalf("got %q %v %v, want nothing", file, secrets, err)
	}
}

func TestAlertsProvisioning_MissingCredentials(t *testing.T) {
	cfg := platform.AlertsConfig{Telegram: &platform.TelegramAlerts{ChatID: "-100123"}, Webhook: true}
	_, _, err := AlertsProvisioning(cfg, &credentials.Credentials{})
	if err == nil || !strings.Contains(err.Error(), "telegramBotToken, alertsWebhookUrl") {
		t.Fatalf("err = %v, want both credentials named", err)
	}
}

func TestAlertsProvisioning_AllChannels(t *testing.T) {
	cfg := platform.AlertsConfig{Telegram: &platform.TelegramAlerts{ChatID: "-100123"}, Discord: true, Webhook: true}
	creds := &credentials.Credentials{TelegramBotToken: "123:secret-token", DiscordWebhookURL: "https://discord.example.com/api/webhooks/1/secret", AlertsWebhookURL: "https://hooks.example.com/secret"}
	file, secrets, err := AlertsProvisioning(cfg, creds)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{creds.TelegramBotToken, creds.DiscordWebhookURL, creds.AlertsWebhookURL} {
		if strings.Contains(file, v) {
			t.Errorf("secret %q in clear in the provisioning file", v)
		}
	}
	if secrets[alertsTelegramTokenVar] != creds.TelegramBotToken || secrets[alertsDiscordURLVar] != creds.DiscordWebhookURL || secrets[alertsWebhookURLVar] != creds.AlertsWebhookURL {
		t.Errorf("secrets = %v", secrets)
	}
	var doc struct {
		APIVersion    int `yaml:"apiVersion"`
		ContactPoints []struct {
			Name      string
			Receivers []struct {
				UID, Type string
				Settings  map[string]any
			}
		} `yaml:"contactPoints"`
		Policies []struct {
			Receiver       string
			GroupBy        []string `yaml:"group_by"`
			RepeatInterval string   `yaml:"repeat_interval"`
		}
	}
	if err := yaml.Unmarshal([]byte(file), &doc); err != nil {
		t.Fatalf("not YAML: %v\n%s", err, file)
	}
	if doc.APIVersion != 1 || strings.Contains(file, "templates:") {
		t.Errorf("apiVersion %d, or a named template (loaded late by Grafana)", doc.APIVersion)
	}
	if len(doc.ContactPoints) != 1 || len(doc.ContactPoints[0].Receivers) != 3 {
		t.Fatalf("contact points: %+v", doc.ContactPoints)
	}
	tg := doc.ContactPoints[0].Receivers[0]
	if tg.Type != "telegram" || tg.Settings["bottoken"] != "$"+alertsTelegramTokenVar || tg.Settings["chatid"] != "-100123" || tg.Settings["message"] != alertsMessage {
		t.Errorf("telegram receiver: %+v", tg)
	}
	if doc.ContactPoints[0].Receivers[1].Settings["url"] != "$"+alertsDiscordURLVar {
		t.Errorf("discord receiver: %+v", doc.ContactPoints[0].Receivers[1])
	}
	r := doc.Policies[0]
	if r.Receiver != "urbex" || r.RepeatInterval != "4h" || strings.Join(r.GroupBy, ",") != "alertname,service,host" {
		t.Errorf("route: %+v", r)
	}
}
