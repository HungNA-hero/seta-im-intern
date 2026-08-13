package kafka

import (
	"crypto/tls"
	"fmt"
	"strings"
	"time"

	"github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/sasl"
	"github.com/segmentio/kafka-go/sasl/plain"
	"github.com/segmentio/kafka-go/sasl/scram"
)

// SecurityOptions is the broker security policy, sourced from
// ASSET_KAFKA_SECURITY_PROTOCOL and the ASSET_KAFKA_SASL_* variables.
type SecurityOptions struct {
	Protocol           string
	Mechanism          string
	Username           string
	Password           string
	InsecureSkipVerify bool
}

// transportFor builds the transport for a security policy. An unrecognised or
// incomplete policy is an error rather than a silent fall back to plaintext,
// because a connection that quietly downgrades is worse than one that fails.
func transportFor(options SecurityOptions) (*kafka.Transport, error) {
	protocol := strings.ToUpper(strings.TrimSpace(options.Protocol))
	if protocol == "" {
		protocol = "PLAINTEXT"
	}

	transport := &kafka.Transport{}
	switch protocol {
	case "PLAINTEXT":
		return transport, nil
	case "SSL":
		transport.TLS = tlsConfig(options)
		return transport, nil
	case "SASL_PLAINTEXT", "SASL_SSL":
		mechanism, err := saslMechanism(options)
		if err != nil {
			return nil, err
		}
		transport.SASL = mechanism
		if protocol == "SASL_SSL" {
			transport.TLS = tlsConfig(options)
		}
		return transport, nil
	default:
		return nil, fmt.Errorf("unsupported ASSET_KAFKA_SECURITY_PROTOCOL %q", options.Protocol)
	}
}

func tlsConfig(options SecurityOptions) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: options.InsecureSkipVerify,
	}
}

func saslMechanism(options SecurityOptions) (sasl.Mechanism, error) {
	if options.Username == "" || options.Password == "" {
		return nil, fmt.Errorf("SASL requires ASSET_KAFKA_SASL_USERNAME and ASSET_KAFKA_SASL_PASSWORD")
	}

	switch strings.ToUpper(strings.TrimSpace(options.Mechanism)) {
	case "PLAIN":
		return plain.Mechanism{Username: options.Username, Password: options.Password}, nil
	case "SCRAM-SHA-256":
		return scram.Mechanism(scram.SHA256, options.Username, options.Password)
	case "SCRAM-SHA-512":
		return scram.Mechanism(scram.SHA512, options.Username, options.Password)
	default:
		return nil, fmt.Errorf("unsupported ASSET_KAFKA_SASL_MECHANISM %q", options.Mechanism)
	}
}

// dialerFor mirrors transportFor for the consumer, which kafka-go configures
// through a Dialer rather than a Transport.
func dialerFor(options SecurityOptions) (*kafka.Dialer, error) {
	transport, err := transportFor(options)
	if err != nil {
		return nil, err
	}
	return &kafka.Dialer{
		Timeout:       10 * time.Second,
		DualStack:     true,
		TLS:           transport.TLS,
		SASLMechanism: transport.SASL,
	}, nil
}
