// Command teslamate-dawarich streams TeslaMate positions into Dawarich.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/LukStankovic/teslamate-dawarich/internal/config"
	"github.com/LukStankovic/teslamate-dawarich/internal/dawarich"
	"github.com/LukStankovic/teslamate-dawarich/internal/homeassistant"
	"github.com/LukStankovic/teslamate-dawarich/internal/mqtt"
	"github.com/LukStankovic/teslamate-dawarich/internal/presence"
	"github.com/LukStankovic/teslamate-dawarich/internal/state"
	"github.com/LukStankovic/teslamate-dawarich/internal/syncer"
	"github.com/LukStankovic/teslamate-dawarich/internal/teslamate"
)

var version = "dev"

var beginningOfTeslaTime = time.Date(2012, time.January, 1, 0, 0, 0, 0, time.UTC)

func main() {
	if err := run(); err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		fmt.Fprintln(os.Stderr, "error: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	var (
		from        = flag.String("from", "", "backfill from this time (RFC3339 or YYYY-MM-DD) instead of the stored cursor")
		full        = flag.Bool("full", false, "backfill the entire TeslaMate history")
		once        = flag.Bool("once", false, "sync everything pending, then exit")
		showVersion = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := newLogger(cfg.LogLevel)
	logger.Info("teslamate-dawarich starting",
		"version", version,
		"dawarich_url", cfg.DawarichURL,
		"poll_interval", cfg.PollInterval,
		"overlap_window", cfg.OverlapWindow,
		"batch_size", cfg.BatchSize,
		"drives_only", cfg.DrivesOnly,
		"car_ids", cfg.CarIDs,
		"mqtt_nudge", cfg.MQTT.Enabled,
		"presence_check", cfg.Presence.Enabled(),
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := teslamate.Open(ctx, cfg.TeslaMateDSN)
	if err != nil {
		return err
	}
	defer store.Close()

	bookmark := state.NewBookmark(state.DefaultDir())
	if err := applyBackfillFlags(bookmark, *from, *full, logger); err != nil {
		return err
	}

	client := dawarich.New(cfg.DawarichURL, cfg.DawarichAPIKey, logger,
		dawarich.WithForwardedProto(cfg.DawarichForwardedProto))

	sync := syncer.New(store, client, bookmark, syncer.Options{
		PollInterval:    cfg.PollInterval,
		OverlapWindow:   cfg.OverlapWindow,
		InitialLookback: cfg.InitialLookback,
		BatchSize:       cfg.BatchSize,
		DrivesOnly:      cfg.DrivesOnly,
		CarIDs:          cfg.CarIDs,
		TrackerPrefix:   cfg.TrackerPrefix,
		Presence:        presenceGate(cfg, store, client, logger),
	}, logger)

	if *once {
		synced, err := sync.SyncOnce(ctx)
		if err != nil {
			return err
		}
		logger.Info("sync complete", "points", synced)
		return nil
	}

	nudge, stopNudger, err := startNudger(cfg, logger)
	if err != nil {
		return err
	}
	defer stopNudger()

	logger.Info("running")
	err = sync.Run(ctx, nudge)
	logger.Info("stopped")
	return err
}

func presenceGate(cfg config.Config, store *teslamate.Store, client *dawarich.Client, logger *slog.Logger) syncer.PresenceGate {
	if !cfg.Presence.Enabled() {
		return nil
	}

	var signals []presence.Signal
	if ha := cfg.Presence.HomeAssistant; ha.Enabled() {
		signals = append(signals, presence.NewEntitySignal(homeassistant.New(ha.URL, ha.Token), ha.EntityID))
	}
	if cfg.Presence.TrackerID != "" {
		signals = append(signals, presence.NewPhoneSignal(store, client,
			cfg.Presence.TrackerID, float64(cfg.Presence.RadiusMeters), cfg.Presence.PhoneWait))
	}

	logger.Info("presence check enabled",
		"home_assistant_entity", cfg.Presence.HomeAssistant.EntityID,
		"phone_tracker", cfg.Presence.TrackerID,
		"radius_m", cfg.Presence.RadiusMeters,
		"phone_wait", cfg.Presence.PhoneWait,
		"settle", cfg.Presence.SettleDelay,
		"sync_unknown", cfg.Presence.SyncWhenUnknown,
	)
	return presence.NewChecker(store, signals, presence.Options{
		SettleDelay:     cfg.Presence.SettleDelay,
		SyncWhenUnknown: cfg.Presence.SyncWhenUnknown,
	}, logger)
}

func startNudger(cfg config.Config, logger *slog.Logger) (<-chan struct{}, func(), error) {
	if !cfg.MQTT.Enabled {
		return nil, func() {}, nil
	}

	nudger := mqtt.NewNudger(mqtt.Config{
		Host:      cfg.MQTT.Host,
		Port:      cfg.MQTT.Port,
		Username:  cfg.MQTT.Username,
		Password:  cfg.MQTT.Password,
		ClientID:  cfg.MQTT.ClientID,
		TLS:       cfg.MQTT.TLS,
		Namespace: cfg.MQTT.Namespace,
	}, logger)
	if err := nudger.Connect(); err != nil {
		return nil, func() {}, err
	}
	return nudger.Signals(), nudger.Disconnect, nil
}

func applyBackfillFlags(bookmark *state.Bookmark, from string, full bool, logger *slog.Logger) error {
	start, ok, err := backfillStart(from, full)
	if err != nil || !ok {
		return err
	}
	if err := bookmark.Save(state.Checkpoint{LastPositionAt: start}); err != nil {
		return err
	}
	logger.Info("cursor reset for backfill", "from", start)
	return nil
}

func backfillStart(from string, full bool) (time.Time, bool, error) {
	switch {
	case full && from != "":
		return time.Time{}, false, errors.New("-full and -from are mutually exclusive")
	case full:
		return beginningOfTeslaTime, true, nil
	case from == "":
		return time.Time{}, false, nil
	}

	for _, layout := range []string{time.RFC3339, time.DateOnly} {
		if at, err := time.Parse(layout, from); err == nil {
			return at, true, nil
		}
	}
	return time.Time{}, false, fmt.Errorf("invalid -from %q: want RFC3339 or YYYY-MM-DD", from)
}

func newLogger(level string) *slog.Logger {
	var parsed slog.Level
	switch strings.ToLower(level) {
	case "debug":
		parsed = slog.LevelDebug
	case "warn", "warning":
		parsed = slog.LevelWarn
	case "error":
		parsed = slog.LevelError
	default:
		parsed = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: parsed}))
}
