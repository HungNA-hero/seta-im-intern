package kafka

import (
	"testing"
)

func TestPlaintextNeedsNoTLSOrSASL(t *testing.T) {
	transport, err := transportFor(SecurityOptions{Protocol: "PLAINTEXT"})
	if err != nil {
		t.Fatalf("building plaintext transport: %v", err)
	}
	if transport.TLS != nil {
		t.Fatal("plaintext transport carries a TLS config")
	}
	if transport.SASL != nil {
		t.Fatal("plaintext transport carries a SASL mechanism")
	}
}

func TestSASLSSLAppliesBothTLSAndTheConfiguredMechanism(t *testing.T) {
	for _, mechanism := range []string{"PLAIN", "SCRAM-SHA-256", "SCRAM-SHA-512"} {
		t.Run(mechanism, func(t *testing.T) {
			transport, err := transportFor(SecurityOptions{
				Protocol:  "SASL_SSL",
				Mechanism: mechanism,
				Username:  "media",
				Password:  "secret",
			})
			if err != nil {
				t.Fatalf("building %s transport: %v", mechanism, err)
			}
			if transport.TLS == nil {
				t.Fatal("SASL_SSL transport has no TLS config, so credentials would cross the wire in the clear")
			}
			if transport.SASL == nil {
				t.Fatalf("SASL_SSL transport has no SASL mechanism configured")
			}
		})
	}
}

func TestSSLAppliesTLSWithoutSASL(t *testing.T) {
	transport, err := transportFor(SecurityOptions{Protocol: "SSL"})
	if err != nil {
		t.Fatalf("building SSL transport: %v", err)
	}
	if transport.TLS == nil {
		t.Fatal("SSL transport has no TLS config")
	}
	if transport.SASL != nil {
		t.Fatal("SSL transport should not configure SASL")
	}
}

func TestSecurityConfigurationIsRejectedWhenIncomplete(t *testing.T) {
	for name, options := range map[string]SecurityOptions{
		"unknown protocol":  {Protocol: "SASL_CARRIER_PIGEON"},
		"unknown mechanism": {Protocol: "SASL_SSL", Mechanism: "MAGIC", Username: "u", Password: "p"},
		"missing username":  {Protocol: "SASL_SSL", Mechanism: "PLAIN", Password: "p"},
		"missing password":  {Protocol: "SASL_SSL", Mechanism: "PLAIN", Username: "u"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := transportFor(options); err == nil {
				t.Fatalf("transportFor(%+v) returned no error; a silent fallback to plaintext must not be possible", options)
			}
		})
	}
}
