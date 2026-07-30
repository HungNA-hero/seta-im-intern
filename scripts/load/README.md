# Local load testing and observability

These scenarios target the seeded local stack. A healthy `/health` endpoint alone
is not enough: the runner also performs a real `folderTree` GraphQL preflight and
stops with an actionable error if the fixtures or application path are unavailable.

## Prepare a clean stack

From the repository root:

```bash
make setup
make migrate
docker exec -i seta-access-db psql -U access_user -d access_db < infra/db/access/seed/demo_fixtures.sql
docker exec -i seta-asset-db psql -U asset_user -d asset_db < infra/db/asset/seed/demo_fixtures.sql
docker compose \
  -f infra/docker-compose.yml \
  -f infra/docker-compose.override.yml \
  -f infra/docker-compose.observability.yml \
  --profile observability up -d --build
```

The demo IDs used by both runners are:

- user: `00000000-0000-0000-0000-000000000001`
- organization: `00000000-0000-0000-0000-000000000010`
- folder: `10000000-0000-0000-0000-000000000002`

Open Grafana at `http://localhost:3000` and sign in with `admin` / `admin`
for local use. The provisioned **SETA DAM - Load Testing** dashboard reads
Prometheus at `http://localhost:9090` and Loki at `http://localhost:3100`.
Grafana Alloy's local status UI is at `http://localhost:12345`.

The separate **SETA DAM - Event Model** dashboard covers Asset producer
outcomes, Redis Stream group lag/pending work, and DLQ depth. See the
[event model observability runbook](../../infra/observability/event-model-runbook.md)
before interpreting or recovering those signals.

## Run a scenario

Linux or WSL2:

```bash
bash scripts/load/run-k6.sh \
  --scenario read-folder-tree \
  --max-vus 25 \
  --hold-duration 3m
```

PowerShell:

```powershell
.\scripts\load\run-k6.ps1 `
  -Scenario read-folder-tree `
  -MaxVUs 25 `
  -HoldDuration 3m
```

Each runner generates a unique `RUN_ID`, sends it in every request ID/trace ID,
and includes it in `.cache/load/*-summary.json`. Pass `--run-id` or `-RunId`
only when a stable external run identifier is required. Direct `k6 run` calls
must provide a valid, execution-unique `RUN_ID`; scripts fail fast when it is
missing rather than silently reusing correlation IDs from an earlier run.

Available scenarios:

- `read-folder-tree`: sustained folder-tree reads.
- `cursor-search`: cursor-based metadata search.
- `fixed-request-count`: exactly `TOTAL_REQUESTS` HTTP requests at the requested
  arrival rate; the run fails if the observed request count differs.
- `breaker-recovery`: exploratory sustained-load recovery check; it is expected
  to fail unless Asset Core is interrupted and restored during the run.

The k6 scenario is not the regression proof for the breaker state machine.
`bash scripts/ci/circuit-breaker-scenario.sh` uses the fault-only Toxiproxy
fixture to deterministically exercise timeout trip, open fail-closed/no-I/O,
one half-open probe, recovery, and reset while writing evidence under
`.cache/ci/circuit-breaker/`.

## Exercise circuit-breaker recovery

Use a hold duration long enough to perform both actions:

```bash
bash scripts/load/run-k6.sh \
  --scenario breaker-recovery \
  --ramp-up 15s \
  --hold-duration 2m \
  --ramp-down 15s
```

While it is running, use another terminal:

```bash
docker compose -f infra/docker-compose.yml stop asset-core
# Wait until Access Core has emitted safe INTERNAL_ERROR responses and opened the breaker.
docker compose -f infra/docker-compose.yml start asset-core
```

The scenario passes only if it observes at least one safe `INTERNAL_ERROR` and
then a successful response from the same virtual user. A fully healthy run and
an all-error run both fail their thresholds, preventing false recovery claims.
Stopping the container produces a fast connection refusal, so use the automated
Toxiproxy scenario when validating the three-second dependency deadline or exact
breaker state transitions.

## Qualify the deterministic fault scenario

The `Circuit-breaker fault candidate` CI job runs only on `main` and manual CI
dispatches and is non-blocking. The manual `Circuit-breaker fault qualification`
workflow runs 20 isolated copies and aggregates their JSON evidence.

Promotion to the fast pull-request gate requires all of the following from one
qualification workflow:

- 20 first-attempt passes against one commit;
- all seven named paths passing in every report;
- no scenario timeout or retry;
- total-runtime p95 at or below five minutes.

If any condition fails, keep the check in the non-blocking main/manual tier,
correct the fault fixture or orchestration, and restart qualification from zero.

## Stop the stack

```bash
docker compose \
  -f infra/docker-compose.yml \
  -f infra/docker-compose.override.yml \
  -f infra/docker-compose.observability.yml \
  --profile observability down
```
