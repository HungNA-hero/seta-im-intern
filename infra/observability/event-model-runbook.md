# Event model observability and recovery

The provisioned **SETA DAM - Event Model** Grafana dashboard separates producer
outcomes from Redis Stream state. Asset Core and the asset deletion worker each
expose `seta_asset_lifecycle_event_publish_total{outcome="success|failure"}`;
the independent, pinned `redis_exporter:v1.87.0` observes only
`stream:asset-events` and `stream:asset-events:dlq`. Per-consumer metrics are
disabled so random Access Core consumer names cannot create unbounded series.

## What the metrics mean

| Metric | Meaning |
|---|---|
| `seta_asset_lifecycle_event_publish_total{outcome="success"}` | Redis `XADD` returned a non-empty stream ID. |
| `seta_asset_lifecycle_event_publish_total{outcome="failure"}` | Redis `XADD` returned an error. Final Redis state may be unknown, and the producer does not retry. |
| `seta_asset_event_stream_observer_up` | `1` only when Prometheus can scrape the exporter and the exporter can reach Redis. |
| `seta_asset_event_consumer_group_present` | `1` when the `cache-invalidator` group is visible on the event stream, `0` when the observer is healthy but the group is absent. |
| `seta_asset_event_consumer_group_lag` | Entries that have never been delivered to a consumer in the group. |
| `seta_asset_event_consumer_group_pending` | Entries delivered to a consumer but not acknowledged. |
| `seta_asset_event_dlq_depth` | Retained entries in `stream:asset-events:dlq`. |

Total outstanding group work is:

```promql
seta_asset_event_consumer_group_lag
+
seta_asset_event_consumer_group_pending
```

Redis stream length is deliberately not used as backlog. Acknowledged entries
are not trimmed from the event stream, so stream length includes completed
history. Likewise, the DLQ depth is retained history until an operator
explicitly removes entries.

The normalized stream gauges have no labels. Before normalization, only fixed
`db`, stream, group, service, and publish-outcome dimensions are used. Never add
organization, user, resource, trace, payload, event, message, or consumer
identifiers to these metrics.

## Current guarantees and limitations

- The Asset database transaction commits before publication. A publish failure
  does not roll back a move or delete, and failed publications are not replayed.
- Redis-backed authorization caching fails open to the authoritative decision
  path. It never turns a Redis error directly into an `allow`.
- Decision and fact cache TTLs are capped at four seconds. This bounds stale
  cache exposure only; it does not guarantee message delivery or Redis
  durability.
- A published processing failure remains pending and becomes reclaimable after
  30 seconds. At five failed processing attempts it is moved to the DLQ and
  acknowledged, subject to Access Core and Redis being available.
- The DLQ has no automatic replay and no retention policy.
- Access Core Redis clients disable reconnection. After Redis is restored,
  restart Access Core to restore its cache and consumer connections.
- Malformed envelopes and otherwise valid envelopes with an unsupported `eventType`
  are currently acknowledged without an effect and without a DLQ entry. This is
  a known limitation, not successful processing.
- When the exporter is down or cannot reach Redis,
  `seta_asset_event_stream_observer_up` is `0` and stream/group gauges are
  unavailable. Do not interpret a missing gauge, or a previous displayed zero,
  as an empty queue while observer health is down.

## Dashboard queries

Producer outcome rate by process:

```promql
sum by (service, outcome) (
  rate(seta_asset_lifecycle_event_publish_total[5m])
)
```

Observer and group health:

```promql
seta_asset_event_stream_observer_up
seta_asset_event_consumer_group_present
```

Outstanding work and DLQ:

```promql
seta_asset_event_consumer_group_lag
seta_asset_event_consumer_group_pending
seta_asset_event_consumer_group_lag + seta_asset_event_consumer_group_pending
seta_asset_event_dlq_depth
```

## Diagnosis

The following commands assume the observability stack was started with the
three Compose files used by the local observability guide:

```bash
docker compose \
  -f infra/docker-compose.yml \
  -f infra/docker-compose.override.yml \
  -f infra/docker-compose.observability.yml \
  --profile observability ps

curl -fsS http://localhost:9121/metrics |
  grep -E '^(redis_up|redis_stream_(length|group_lag|group_messages_pending))'

curl -fsSG http://localhost:9090/api/v1/query \
  --data-urlencode 'query=seta_asset_event_stream_observer_up'

docker compose -f infra/docker-compose.yml exec redis \
  redis-cli XINFO GROUPS stream:asset-events

docker compose -f infra/docker-compose.yml exec redis \
  redis-cli XPENDING stream:asset-events cache-invalidator

docker compose -f infra/docker-compose.yml exec redis \
  redis-cli XRANGE stream:asset-events:dlq - + COUNT 20
```

Use service logs to correlate a counter increase with the operation, but do not
copy correlation identifiers into metric labels:

```bash
docker compose \
  -f infra/docker-compose.yml \
  -f infra/docker-compose.override.yml \
  -f infra/docker-compose.observability.yml \
  logs --since 15m asset-core asset-delete-worker access-core redis-exporter
```

## Recovery

1. If observer health is down, restore Redis and the exporter first. Confirm
   `redis-cli PING` and `redis_up 1`.
2. Restart Access Core after Redis is reachable because its existing clients do
   not reconnect:

   ```bash
   docker compose -f infra/docker-compose.yml restart access-core
   ```

3. Confirm the consumer group is present. Watch lag and pending separately.
   Newly delivered work reduces lag; successful acknowledgement reduces
   pending. Stale pending work is reclaimed after 30 seconds while the service
   and Redis remain available.
4. Inspect every DLQ entry before any manual replay. There is no safe generic
   replay command: determine why processing failed, correct that cause, and
   explicitly choose whether to publish a repaired event. Removing or replaying
   a DLQ entry is an operator-controlled data change.
5. A producer `failure` cannot be recovered from the stream because no durable
   publication record exists. Verify the committed Asset state directly and
   allow the four-second cache ceiling to expire; any business-level
   reconciliation requires an explicit, case-specific action.

No alert rules, automatic replay, stream trimming, or stronger delivery
mechanism are part of this observability setup.
