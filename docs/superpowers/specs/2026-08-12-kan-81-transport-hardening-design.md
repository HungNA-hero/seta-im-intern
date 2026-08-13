# KAN-81 Transport Hardening Design

## Goal

Close the confirmed correctness and operability gaps in the reusable Kafka/outbox foundation
without adding media-domain persistence, worker wiring, or deployment dependencies owned by
KAN-85.

## Scope

Implement these changes:

1. Give `RelayOptions.LeaseMargin` a safe five-second default so lease-bounded publication always
   reserves settlement time unless a positive value is supplied.
2. Make `NewReader` validate its required consumer-group configuration before calling kafka-go's
   panic-based constructor. A consumer, at least one non-empty broker, a topic, and a group ID are
   mandatory; invalid byte ranges also return errors.
3. Treat cancellation of the `Run` context as graceful shutdown at fetch, retry, and commit
   boundaries. Unrelated errors continue to propagate.
4. Salvage `eventId` and the configured aggregate identifier independently from rejected JSON
   envelopes. Only individually valid UUID values reach quarantine metadata.
5. Reject empty Kafka record keys on publication. KAN-81 event families require a key, so silently
   pinning empty keys to one partition is invalid.
6. Preserve safe publish diagnostics through structured relay logging and distinguish caller
   cancellation from deadline expiry in persisted transport error codes.
7. Document that unlimited publish retries plus per-key ordering can permanently block one key
   behind an unpublishable head row. Existing counters indicate failures, while a feature store's
   oldest-due-row age is the future operational signal for persistent blockage.

Defer these changes:

- Do not tune the reader retry budget until KAN-85 defines worker supervision and outage
  tolerance. The current five attempts contain four default 500 ms waits.
- Do not wire `ASSET_OUTBOX_RELAY_*`, add Kafka dependencies to `asset-core`, or create the media
  worker. KAN-85 owns that composition. Clarify in documentation that these variables are reserved
  for that worker.

## Design

### Relay safety and diagnostics

`RelayOptions.withDefaults` supplies a five-second lease margin and a default logger. The relay
logs one structured warning for each failed publish using bounded fields it already owns: event
ID, topic, transport error code, and the returned error. Logging lives at the retry owner rather
than inside the Kafka adapter, avoiding duplicate logs and keeping the `Publisher` seam generic.

`transportErrorCode` maps only `context.DeadlineExceeded` to `PUBLISH_TIMEOUT`, maps
`context.Canceled` to `PUBLISH_CANCELED`, and maps all other errors to `PUBLISH_FAILED`.

### Reader construction and shutdown

`NewReader` builds one `kafka.ReaderConfig`, performs application-required checks, calls its
exported `Validate` method, and only then calls `kafka.NewReader`. Application validation is
stricter than kafka-go: KAN-81 requires consumer-group mode, so an empty group ID is invalid even
though kafka-go supports direct partition reading.

`Run` returns `nil` only when its own context is done. This normalization is applied after fetch,
delivery retry, and offset commit errors. A context-shaped error from another source is not
silently treated as shutdown when the run context remains active.

### Producer keys and quarantine salvage

`Producer.Publish` returns a stable validation error before constructing a writer when the topic
or key is empty. The key check protects the per-aggregate ordering contract; it does not invent a
round-robin fallback.

`salvageIdentity` decodes the top-level object as `map[string]json.RawMessage`, retains the raw
object for aggregate extraction, and parses `eventId` independently. A malformed or non-string
event ID therefore cannot suppress a valid aggregate UUID, and neither field is copied unless it
is a valid UUID.

## Testing

Add network-free regression tests for:

- the default five-second publish deadline and explicit positive margin override;
- every invalid `NewReader` input returning an error without panic;
- graceful cancellation during fetch, retry, and commit, plus propagation of unrelated errors;
- independent salvage for invalid event ID with valid aggregate ID and the inverse case;
- empty topic/key publication rejection before broker I/O;
- distinct timeout, cancellation, and general publish error codes and relay log fields.

Run formatting, focused race tests, vet, the full Asset Core suite, and Compose configuration
validation. Live Kafka tests remain optional and are unchanged by these network-free fixes.

## Completion Criteria

- All new regression tests pass without a broker.
- Existing eventing and Asset Core tests pass.
- No KAN-85 database adapter, worker command, or Compose runtime dependency is introduced.
- The known per-key blockage limitation and deferred configuration ownership are explicit in the
  KAN-81 documentation.
