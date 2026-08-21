# Phase 1.3 Demo Log Format Design

## Goal

Make the complete Phase 1.3 interactive demo transcript easy for a person to scan while preserving
the existing machine-readable evidence artifacts and their security properties.

The change covers startup, preflight, both scenario groups, every numbered case, API evidence,
database evidence, bounded service logs, failures, and the final run summary. It does not change the
demo scenarios, service logging contracts, API behavior, database behavior, or evidence schemas.

## Chosen Approach

Add a structured terminal renderer to `scripts/demo/phase1.3-common.sh` and route existing demo
messages through its small set of presentation helpers. Keep JSONL evidence generation independent
from presentation so automation continues to consume the same files.

This approach is preferred over spacing/color-only changes because it makes dense API and service
records meaningfully easier to understand. It is preferred over a separate formatter executable
because Bash and `jq` already provide everything the demo needs, avoiding another runtime
dependency and failure mode.

## Terminal Structure

The run begins with a compact banner and aligned metadata:

```text
PHASE 1.3 · Interactive demo
Run       phase13-...
Mode      all
Evidence  /.../demo-logs/phase13-...
```

Each numbered scenario is rendered as one card:

```text
╭─ CASE 2.4 · Commit once and wait for authoritative completion
│
│ INFO  Happy path completed with one version, one job, and two outputs.
│
├─ API
│ ✓ commit-happy                         HTTP 202
│   jobId=...  status=QUEUED
│
│ ✓ happy-status-completed               HTTP 200
│   status=COMPLETED  outputs=thumbnail,web
│
├─ DATABASE
│ ✓ happy-durable-state
│   jobs=1  versions=1  outputs=2  active=true  pending=false  attempts=1
│
├─ SERVICES
│ media-worker
│   INFO  media job completed             jobId=...
│
╰─ PASS 2.4 · 1.8s
```

Empty evidence categories are omitted. A case with no captured evidence still ends with a visible
result line. Preflight uses the same visual vocabulary but is not assigned a numbered case.

The final summary reports the run result and evidence location without repeating all case details.

## Log Levels and Messages

Demo-authored messages use three presentation levels:

- `INFO` for normal progress and successful facts.
- `WARN` for recoverable or deliberately induced adverse conditions.
- `ERROR` for the reason a command or case failed.

Existing calls to `phase13_log` remain valid and map to `INFO`. A warning helper is added for cases
that intentionally interrupt services or observe retries. `phase13_die` maps to `ERROR` and retains
its nonzero-return behavior. Levels describe severity; PASS and FAIL remain case outcomes rather
than log levels.

Messages should state the observed result first and put identifiers or measurements in aligned
`key=value` fields when practical. The renderer must not guess a service log's level: recognized
structured levels are normalized for display, while an absent or unrecognized level is displayed
without inventing severity.

## Evidence Rendering

### API evidence

For each new JSONL record in the case, display the step name and HTTP status first. Show useful
top-level response facts such as status, identifiers, output names, and idempotency replay state on
indented lines. If a response does not match a recognized shape, render sanitized pretty JSON so
information is never silently discarded.

### Database evidence

Display the query step name followed by compact aligned fields for object-shaped rows. Arrays and
unknown shapes fall back to sanitized pretty JSON. Booleans, counts, and identifiers must remain
unambiguous.

### Service logs

Keep the current per-case time window, container list, tail bounds, and archival behavior. For a
JSON log line, display its timestamp when present, normalized level, message, and remaining scalar
fields. Nested or unfamiliar data falls back to compact JSON. Plain-text lines are indented under
their container without alteration beyond sanitization and length limits.

The display renderer may shorten very long values for readability, but the separately persisted
sanitized evidence retains its current fidelity.

## Compatibility and Security

The following artifacts retain their current paths and schemas:

- `transcript.log`
- `summary.json`
- `soft-delete/api-responses.jsonl`
- `soft-delete/database-snapshots.jsonl`
- `upload/api-responses.jsonl`
- `upload/database-snapshots.jsonl`
- Per-service log files under each scenario's `service-logs/` directory

The existing URL query redaction, sensitive-key redaction, service-log sanitization, 900-character
line bound, bounded Docker log reads, and refusal to print malformed evidence remain mandatory.
Formatting always occurs after sanitization. No signed URL, credential, token, cookie, password, or
secret may appear in the terminal or transcript.

Terminal decoration must degrade safely when color is disabled. Meaning cannot depend on color:
section names, levels, and PASS/FAIL labels remain explicit in plain text. Color is enabled by
default for the interactive terminal and disabled when `NO_COLOR` is nonempty. The persisted
`transcript.log` is always stripped of ANSI escape sequences. Unicode box drawing is the default;
setting `PHASE13_ASCII=1`, or running under a locale whose name does not contain `UTF-8` or `utf8`,
selects equivalent ASCII borders.

## Components and Boundaries

`phase1.3-common.sh` owns presentation because both scenario scripts already depend on it. Its
renderer is split into narrow helpers for:

- Run banner and summary.
- Case start and elapsed-time tracking.
- Leveled demo messages.
- API and database record rendering.
- Structured and plain-text service-log rendering.
- PASS and FAIL footers.

Scenario scripts continue to own scenario semantics and assertions. They may supply clearer
messages or use the warning helper, but they do not parse evidence or implement layout.

Evidence recording and sanitization remain separate from rendering. A formatting failure must fail
the demo clearly rather than conceal malformed evidence or incorrectly report a passing case.

## Testing

Extend `scripts/demo/phase1.3-common.test.sh` to verify:

- Run, case, section, level, and final-result layout.
- API, database, structured service-log, and plain-text fallback rendering.
- Stable ordering of API, database, services, and result sections.
- Omission of empty categories.
- PASS and FAIL output, including failure messages and elapsed-time syntax.
- Color-disabled and ASCII-safe output remains understandable.
- Long displayed lines remain bounded.
- Signed query strings and sensitive values are redacted from terminal and saved service logs.
- Malformed evidence is rejected.
- Existing JSONL and summary schemas are unchanged.

The renderer test remains self-contained and does not require live Compose services. A final shell
syntax check and the repository's existing demo helper test provide proportional verification.

## Non-Goals

- Changing application-service log schemas or log levels.
- Replacing JSONL evidence with presentation text.
- Adding a logging framework or external formatting dependency.
- Changing demo scenario behavior, timing, data ownership, or assertions.
- Removing bounded output or exposing additional service logs.
