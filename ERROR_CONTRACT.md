# Error Contract (P0 Decision)

**Status:** Frozen on 15 July 2026 for Sprint 5 implementation.

This document fixes the public-safe error shape before the wider error,
tracing, and service-tag work starts. It does not convert every existing error
path in P0; that implementation is Sprint 5 scope.

## REST envelope

Non-success REST responses will use the following envelope:

```json
{
  "error": {
    "code": "FORBIDDEN",
    "number": 2003,
    "message": "The requested action is not permitted",
    "traceId": "01J...",
    "service": "asset-core"
  }
}
```

`code` is a stable symbolic identifier. `number` is a stable numeric identifier
from the shared registry. `message` is safe for clients and must not expose
stack traces, database details, credentials, or another organization's data.

## GraphQL extension

GraphQL keeps its standard top-level `message` and exposes matching safe fields:

```json
{
  "errors": [
    {
      "message": "The requested action is not permitted",
      "extensions": {
        "code": "FORBIDDEN",
        "number": 2003,
        "traceId": "01J...",
        "service": "access-core"
      }
    }
  ]
}
```

## P0 security codes

| Symbolic code | Number | Meaning |
|---|---:|---|
| `UNAUTHENTICATED` | 2001 | Missing or invalid actor or service credential. |
| `FORBIDDEN` | 2003 | The authenticated actor cannot perform the action. |
| `TRAINER_BYPASS_DISABLED` | 2006 | Trainer bypass is not enabled or its configuration is incomplete. |
| `TRAINER_BYPASS_EXPIRED` | 2007 | Trainer bypass expiry is not in the future. |
| `RESERVED_ROLE_CODE` | 2008 | A reserved administrative role was targeted by an ordinary lifecycle operation. |

The detailed registry and all REST/GraphQL implementation work are Sprint 5
scope. Every implementation must preserve the shape above and add regression
tests for both safe output and internal-detail masking.
