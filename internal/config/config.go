// Package config loads the daemon's configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DawarichURL            string
	DawarichAPIKey         string
	DawarichForwardedProto string
	TeslaMateDSN           string

	PollInterval    time.Duration
	OverlapWindow   time.Duration
	InitialLookback time.Duration
	BatchSize       int
	DrivesOnly      bool
	CarIDs          []int32
	TrackerPrefix   string

	Presence Presence
	MQTT     MQTT
	LogLevel string
}

type Presence struct {
	TrackerID       string
	RadiusMeters    int
	PhoneWait       time.Duration
	SettleDelay     time.Duration
	SyncWhenUnknown bool
	HomeAssistant   HomeAssistant
}

type HomeAssistant struct {
	URL      string
	Token    string
	EntityID string
}

func (p Presence) Enabled() bool { return p.TrackerID != "" || p.HomeAssistant.Enabled() }

func (h HomeAssistant) Enabled() bool { return h.EntityID != "" }

type MQTT struct {
	Enabled   bool
	Host      string
	Port      string
	Username  string
	Password  string
	ClientID  string
	TLS       bool
	Namespace string
}

const (
	defaultPollInterval      = 15 * time.Second
	defaultOverlapWindow     = 5 * time.Minute
	defaultInitialLookback   = 24 * time.Hour
	defaultBatchSize         = 1000
	minPollInterval          = time.Second
	maxBatchSize             = 10000
	defaultPresenceRadius    = 500
	defaultPresenceSettle    = 15 * time.Minute
	defaultPresencePhoneWait = 6 * time.Hour
	defaultMQTTPort          = "1883"
	defaultMQTTTLSPort       = "8883"
)

func Load() (Config, error) {
	var errs []error
	fail := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	cfg := Config{
		DawarichURL:            strings.TrimRight(os.Getenv("DAWARICH_URL"), "/"),
		DawarichAPIKey:         os.Getenv("DAWARICH_API_KEY"),
		DawarichForwardedProto: strings.TrimSpace(os.Getenv("DAWARICH_FORWARDED_PROTO")),
		TeslaMateDSN:           os.Getenv("TESLAMATE_DB_URL"),
		TrackerPrefix:          os.Getenv("TRACKER_PREFIX"),
		LogLevel:               envOr("LOG_LEVEL", "info"),
	}

	if cfg.DawarichURL == "" {
		fail("DAWARICH_URL is required (e.g. http://dawarich_app:3000)")
	}
	if cfg.DawarichAPIKey == "" {
		fail("DAWARICH_API_KEY is required")
	}
	if cfg.TeslaMateDSN == "" {
		fail("TESLAMATE_DB_URL is required (e.g. postgres://teslamate:pass@database:5432/teslamate)")
	}

	var err error
	if cfg.PollInterval, err = durationOr("POLL_INTERVAL", defaultPollInterval); err != nil {
		fail("%w", err)
	} else if cfg.PollInterval < minPollInterval {
		fail("POLL_INTERVAL must be at least %s", minPollInterval)
	}
	if cfg.OverlapWindow, err = durationOr("OVERLAP_WINDOW", defaultOverlapWindow); err != nil {
		fail("%w", err)
	} else if cfg.OverlapWindow < 0 {
		fail("OVERLAP_WINDOW must not be negative")
	}
	if cfg.InitialLookback, err = durationOr("INITIAL_LOOKBACK", defaultInitialLookback); err != nil {
		fail("%w", err)
	} else if cfg.InitialLookback < 0 {
		fail("INITIAL_LOOKBACK must not be negative")
	}
	if cfg.BatchSize, err = intOr("BATCH_SIZE", defaultBatchSize); err != nil {
		fail("%w", err)
	} else if cfg.BatchSize < 1 || cfg.BatchSize > maxBatchSize {
		fail("BATCH_SIZE must be between 1 and %d", maxBatchSize)
	}
	if cfg.DrivesOnly, err = boolOr("DRIVES_ONLY", true); err != nil {
		fail("%w", err)
	}
	if cfg.CarIDs, err = carIDs(os.Getenv("CAR_IDS")); err != nil {
		fail("%w", err)
	}
	if cfg.Presence, err = loadPresence(); err != nil {
		fail("%w", err)
	}
	if cfg.MQTT, err = loadMQTT(); err != nil {
		fail("%w", err)
	}

	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return cfg, nil
}

func loadPresence() (Presence, error) {
	var (
		presence Presence
		err      error
	)
	presence.TrackerID = strings.TrimSpace(os.Getenv("PRESENCE_TRACKER_ID"))
	if presence.RadiusMeters, err = intOr("PRESENCE_RADIUS", defaultPresenceRadius); err != nil {
		return Presence{}, err
	}
	if presence.RadiusMeters < 1 {
		return Presence{}, errors.New("PRESENCE_RADIUS must be at least 1 meter")
	}
	if presence.PhoneWait, err = durationOr("PRESENCE_PHONE_WAIT", defaultPresencePhoneWait); err != nil {
		return Presence{}, err
	}
	if presence.PhoneWait < 0 {
		return Presence{}, errors.New("PRESENCE_PHONE_WAIT must not be negative")
	}
	if presence.SettleDelay, err = durationOr("PRESENCE_SETTLE", defaultPresenceSettle); err != nil {
		return Presence{}, err
	}
	if presence.SettleDelay < 0 {
		return Presence{}, errors.New("PRESENCE_SETTLE must not be negative")
	}
	if presence.SyncWhenUnknown, err = boolOr("PRESENCE_SYNC_UNKNOWN", false); err != nil {
		return Presence{}, err
	}
	if presence.HomeAssistant, err = loadHomeAssistant(); err != nil {
		return Presence{}, err
	}
	return presence, nil
}

func loadHomeAssistant() (HomeAssistant, error) {
	ha := HomeAssistant{
		URL:      strings.TrimRight(strings.TrimSpace(os.Getenv("PRESENCE_HA_URL")), "/"),
		Token:    strings.TrimSpace(os.Getenv("PRESENCE_HA_TOKEN")),
		EntityID: strings.TrimSpace(os.Getenv("PRESENCE_HA_ENTITY")),
	}

	if ha != (HomeAssistant{}) && (ha.URL == "" || ha.Token == "" || ha.EntityID == "") {
		return HomeAssistant{}, errors.New("PRESENCE_HA_URL, PRESENCE_HA_TOKEN and PRESENCE_HA_ENTITY must be set together")
	}
	return ha, nil
}

func loadMQTT() (MQTT, error) {
	host := os.Getenv("MQTT_HOST")

	useTLS, err := boolOr("MQTT_TLS", false)
	if err != nil {
		return MQTT{}, err
	}

	defaultPort := defaultMQTTPort
	if useTLS {
		defaultPort = defaultMQTTTLSPort
	}

	return MQTT{
		Enabled:   host != "",
		Host:      host,
		Port:      envOr("MQTT_PORT", defaultPort),
		Username:  os.Getenv("MQTT_USERNAME"),
		Password:  os.Getenv("MQTT_PASSWORD"),
		ClientID:  envOr("MQTT_CLIENT_ID", "teslamate-dawarich"),
		TLS:       useTLS,
		Namespace: strings.Trim(os.Getenv("MQTT_NAMESPACE"), "/"),
	}, nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func intOr(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: not an integer", key, raw)
	}
	return value, nil
}

func boolOr(key string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("invalid %s %q: want true or false", key, raw)
	}
	return value, nil
}

func durationOr(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: want a duration like 15s or 2m", key, raw)
	}
	return value, nil
}

func carIDs(raw string) ([]int32, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	var ids []int32
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 32)
		if err != nil || id < 1 {
			return nil, fmt.Errorf("invalid CAR_IDS entry %q: want a positive integer", part)
		}
		ids = append(ids, int32(id))
	}
	return ids, nil
}
