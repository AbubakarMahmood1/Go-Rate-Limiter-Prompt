# Admission semantics

## Common contract

For a policy with immediate capacity `L`, `AllowN(key, n)` requires `n > 0`.

- If `n` can never fit the configured policy, the call is invalid.
- If the current state can admit all `n`, the call returns allowed and atomically consumes exactly `n`.
- Otherwise it returns denied and commits no permit state.
- `Peek` asks whether one permit is currently available but commits no state and does not refresh expiry.
- `Reset` removes the selected algorithm/tier subject state.

Each algorithm prefixes its store key. The HTTP layer then length-frames `(tier, identifier, resource)`, making the complete namespace injective.

`Remaining` means immediately available whole permits after the operation. `ResetAt` means the time full capacity returns if no further permits are consumed. For a denied decision, `RetryAfter` is the earliest store-clock interval at which the same count would be admitted if no other request arrives.

Store clocks have microsecond resolution. HTTP retry/reset values round up to their whole-second representation.

## Token bucket

Parameters:

- refill amount `R = requests`;
- refill period `W = window`;
- capacity `C = burst`, or `requests` when burst is zero;
- refill rate `r = R / W` tokens per second.

Before a decision at store time `t`:

```text
tokens = min(C, committed_tokens + elapsed_seconds * r)
```

The request is allowed exactly when `n <= tokens`; only an allowed positive request writes `tokens - n` and `t`.

A denied request or peek calculates refill from the last committed timestamp but does not commit that calculation. A later allowed request calculates from the same committed base, so elapsed time is never credited twice.

`RetryAfter = ceil_microsecond((n - tokens) / r)` when denied. `ResetAt` uses the backend decision time plus the duration required to reach `C`.

Token state lives through at least one complete refill plus slack. Expiring it therefore has the same visible effect as a full bucket.

## Fixed window

Time is divided into aligned windows of length `W` using the backend clock:

```text
window_start = now - (now mod W)
```

The current counter is admitted exactly when `current + n <= limit`. An allowed request increments the current window atomically. A denial writes nothing.

The counter fully resets at the next boundary. This simplicity permits a boundary burst: traffic can consume one full window just before a boundary and another just after it.

## Weighted sliding-window counter

The store retains only current and previous aligned counters. At elapsed fraction `x` through the current window:

```text
weighted = current + previous * (1 - x)
```

Admission requires `weighted + n <= limit`. This is the standard two-counter approximation: it assumes requests in the previous window were spread evenly.

Retry calculation has two phases:

1. The previous counter decays through the remainder of the current window.
2. If reaching the boundary is still insufficient, the current counter becomes previous and decays through the next window.

The implementation lower-bound-searches integer microseconds using the same floating-point expression as the store. The returned instant is allowed, while the preceding microsecond is still denied under that model.

Current state can influence decisions for up to two windows, so retention covers that horizon plus slack.

## Clocks

- Memory uses the process clock and clamps it to the last permit-consuming timestamp for each committed key.
- Redis obtains time inside the atomic Lua script with `TIME` and similarly clamps against committed per-key time.
- Peeks and denials intentionally do not commit a new clock or refresh TTL, preserving their read-only contract.

Absolute memory and Redis timestamps can differ. Their allow/deny, limit, remaining, and retry-presence decisions are expected to match for deterministic scenarios away from boundaries.

## HTTP mapping

| Condition | Status |
|---|---:|
| Allowed policy decision | `200` |
| Normal policy denial | `429` |
| Invalid JSON, identity, policy selection, or count | `400` |
| Body over 8 KiB | `413` |
| Reset disabled | `404` |
| Reset token missing/wrong | `401` |
| Store-contract misconfiguration | `500` with generic message |
| Store error or decision timeout | `503` with `Retry-After: 1` |

A `503` is never converted into an allow. Callers should distinguish policy denial (`429`) from inability to decide (`503`).
