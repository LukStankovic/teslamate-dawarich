package config

import (
	"strings"
	"testing"
	"time"
)

func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("DAWARICH_URL", "http://dawarich:3000/")
	t.Setenv("DAWARICH_API_KEY", "key")
	t.Setenv("TESLAMATE_DB_URL", "postgres://teslamate@database:5432/teslamate")
}

func TestLoadDefaults(t *testing.T) {
	setRequired(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DawarichURL != "http://dawarich:3000" {
		t.Errorf("DawarichURL = %q, want the trailing slash trimmed", cfg.DawarichURL)
	}
	if cfg.PollInterval != 15*time.Second {
		t.Errorf("PollInterval = %v, want 15s", cfg.PollInterval)
	}
	if !cfg.DrivesOnly {
		t.Error("DrivesOnly = false, want true: parked positions flood Dawarich by default")
	}
	if cfg.MQTT.Enabled {
		t.Error("MQTT.Enabled = true, want false without MQTT_HOST")
	}
}

func TestLoadMissingRequiredReportsEveryField(t *testing.T) {
	t.Setenv("DAWARICH_URL", "")
	t.Setenv("DAWARICH_API_KEY", "")
	t.Setenv("TESLAMATE_DB_URL", "")

	_, err := Load()
	if err == nil {
		t.Fatal("Load succeeded, want an error")
	}
	for _, want := range []string{"DAWARICH_URL", "DAWARICH_API_KEY", "TESLAMATE_DB_URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

func TestLoadMQTTTLSSwitchesDefaultPort(t *testing.T) {
	setRequired(t)
	t.Setenv("MQTT_HOST", "mosquitto")
	t.Setenv("MQTT_TLS", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.MQTT.Enabled {
		t.Error("MQTT.Enabled = false, want true when MQTT_HOST is set")
	}
	if cfg.MQTT.Port != "8883" {
		t.Errorf("MQTT.Port = %q, want 8883 for TLS", cfg.MQTT.Port)
	}
}

func TestLoadMQTTExplicitPortWinsOverTLSDefault(t *testing.T) {
	setRequired(t)
	t.Setenv("MQTT_HOST", "mosquitto")
	t.Setenv("MQTT_TLS", "true")
	t.Setenv("MQTT_PORT", "1884")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MQTT.Port != "1884" {
		t.Errorf("MQTT.Port = %q, want the explicit 1884", cfg.MQTT.Port)
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	tests := map[string]struct{ key, value string }{
		"unparsable interval": {"POLL_INTERVAL", "15"},
		"interval too short":  {"POLL_INTERVAL", "100ms"},
		"batch out of range":  {"BATCH_SIZE", "0"},
		"non-numeric batch":   {"BATCH_SIZE", "many"},
		"non-boolean flag":    {"DRIVES_ONLY", "yes please"},
		"negative car id":     {"CAR_IDS", "1,-2"},
		"non-numeric car id":  {"CAR_IDS", "model-y"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			setRequired(t)
			t.Setenv(tc.key, tc.value)

			if _, err := Load(); err == nil {
				t.Fatalf("Load succeeded with %s=%q, want an error", tc.key, tc.value)
			}
		})
	}
}

func TestLoadCarIDs(t *testing.T) {
	setRequired(t)
	t.Setenv("CAR_IDS", " 1, 3 ")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.CarIDs) != 2 || cfg.CarIDs[0] != 1 || cfg.CarIDs[1] != 3 {
		t.Errorf("CarIDs = %v, want [1 3]", cfg.CarIDs)
	}
}

func TestLoadPresenceIsOffWithoutASource(t *testing.T) {
	setRequired(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Presence.Enabled() {
		t.Errorf("Presence = %+v, want it off when no source is configured", cfg.Presence)
	}
}

func TestLoadPresenceFromPhoneTracker(t *testing.T) {
	setRequired(t)
	t.Setenv("PRESENCE_TRACKER_ID", "iphone")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Presence{TrackerID: "iphone", RadiusMeters: 500, SettleDelay: 15 * time.Minute}
	if cfg.Presence != want || !cfg.Presence.Enabled() {
		t.Errorf("Presence = %+v, want %+v and enabled", cfg.Presence, want)
	}
}

func TestLoadPresenceFromHomeAssistant(t *testing.T) {
	setRequired(t)
	t.Setenv("PRESENCE_HA_URL", "http://homeassistant:8123/")
	t.Setenv("PRESENCE_HA_TOKEN", "token")
	t.Setenv("PRESENCE_HA_ENTITY", "binary_sensor.in_tesla")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := HomeAssistant{URL: "http://homeassistant:8123", Token: "token", EntityID: "binary_sensor.in_tesla"}
	if cfg.Presence.HomeAssistant != want || !cfg.Presence.Enabled() {
		t.Errorf("HomeAssistant = %+v, want %+v and enabled", cfg.Presence.HomeAssistant, want)
	}
}

func TestLoadRejectsBadPresenceValues(t *testing.T) {
	tests := map[string]struct{ key, value string }{
		"zero radius":        {"PRESENCE_RADIUS", "0"},
		"radius with unit":   {"PRESENCE_RADIUS", "500m"},
		"negative settle":    {"PRESENCE_SETTLE", "-1m"},
		"non-boolean switch": {"PRESENCE_SYNC_UNKNOWN", "maybe"},
		"entity without url": {"PRESENCE_HA_ENTITY", "binary_sensor.in_tesla"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			setRequired(t)
			t.Setenv(tc.key, tc.value)

			if _, err := Load(); err == nil {
				t.Fatalf("Load succeeded with %s=%q, want an error", tc.key, tc.value)
			}
		})
	}
}
