// Package mqtt watches TeslaMate's MQTT topics and signals that fresh
// positions are available.
package mqtt

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

type Config struct {
	Host      string
	Port      string
	Username  string
	Password  string
	ClientID  string
	TLS       bool
	Namespace string
}

type Nudger struct {
	client paho.Client
	topic  string
	signal chan struct{}
	logger *slog.Logger
}

const (
	connectTimeout       = 30 * time.Second
	connectRetryInterval = 5 * time.Second
	maxReconnectInterval = 30 * time.Second
	keepAlive            = 60 * time.Second
	disconnectQuiesce    = 1000
)

func NewNudger(cfg Config, logger *slog.Logger) *Nudger {
	nudger := &Nudger{
		signal: make(chan struct{}, 1),
		topic:  positionTopic(cfg.Namespace),
		logger: logger,
	}

	opts := paho.NewClientOptions().
		AddBroker(brokerURL(cfg)).
		SetClientID(cfg.ClientID).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(connectRetryInterval).
		SetMaxReconnectInterval(maxReconnectInterval).
		SetKeepAlive(keepAlive).
		SetCleanSession(true).
		SetOrderMatters(false).
		SetOnConnectHandler(nudger.subscribe).
		SetConnectionLostHandler(func(_ paho.Client, err error) {
			logger.Warn("mqtt connection lost", "error", err)
		})

	if cfg.TLS {
		opts.SetTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12})
	}
	if cfg.Username != "" {
		opts.SetUsername(cfg.Username)
		opts.SetPassword(cfg.Password)
	}

	nudger.client = paho.NewClient(opts)
	return nudger
}

func (n *Nudger) Signals() <-chan struct{} { return n.signal }

func (n *Nudger) Connect() error {
	token := n.client.Connect()
	if !token.WaitTimeout(connectTimeout) {
		return fmt.Errorf("connect to mqtt broker: timeout after %s", connectTimeout)
	}
	if err := token.Error(); err != nil {
		return fmt.Errorf("connect to mqtt broker: %w", err)
	}
	return nil
}

func (n *Nudger) Disconnect() { n.client.Disconnect(disconnectQuiesce) }

func (n *Nudger) subscribe(client paho.Client) {
	token := client.Subscribe(n.topic, 0, n.onMessage)
	token.Wait()
	if err := token.Error(); err != nil {
		n.logger.Error("mqtt subscribe failed", "topic", n.topic, "error", err)
		return
	}
	n.logger.Info("mqtt subscribed", "topic", n.topic)
}

func (n *Nudger) onMessage(_ paho.Client, message paho.Message) {
	if message.Retained() {
		return
	}
	select {
	case n.signal <- struct{}{}:
	default:
	}
}

func brokerURL(cfg Config) string {
	scheme := "tcp"
	if cfg.TLS {
		scheme = "tls"
	}
	return fmt.Sprintf("%s://%s:%s", scheme, cfg.Host, cfg.Port)
}

func positionTopic(namespace string) string {
	if namespace == "" {
		return "teslamate/cars/+/latitude"
	}
	return fmt.Sprintf("teslamate/%s/cars/+/latitude", namespace)
}
