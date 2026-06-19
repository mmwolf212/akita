# Akita — Design Document

**Status:** Draft · **Owner:** _you_ · **Last updated:** 2026-06-17

---

## 1. Summary

`Akita` is a service that continuously monitors public Certificate
Transparency (CT) logs for newly issued certificates whose domains match a
configured watchlist — exact domains, subdomains, and typosquat/look-alike
patterns. On a match it emits a structured alert to one or more sinks
(stdout, file, webhook, message queue). The goal is **early detection of
phishing and brand-impersonation infrastructure**: a malicious look-alike
domain that obtains a TLS certificate becomes publicly visible in CT logs
within minutes of issuance, and `Akita` turns that public signal into an
actionable alert.

This document covers the production design: reliability, observability,
deployment, security, and testing — not just the feature set.

### Scope

- **In scope:** reading public CT logs; matching against a user-owned
  watchlist; structured alerting; resumable, observable, single-binary
  operation suitable for long-running deployment.
- **Out of scope (this version):** active probing of matched domains
  (resolution, screenshots, takedown), a web UI, and exhaustive historical
  search of the full CT corpus (that is what crt.sh / Censys exist for).

### Non-goals

- Not a general-purpose CT search engine.
- Not a TLS interception or MITM tool.
- Not a surveillance tool for arbitrary third-party domains — the watchlist is
  meant to be brands/domains the operator owns or is authorized to defend.

---

## 2. Background

Under [RFC 6962](https://datatracker.ietf.org/doc/html/rfc6962), CAs submit
every issued certificate (and precertificate) to append-only, publicly
auditable CT logs. Each log exposes an HTTP API; the two endpoints we rely on:

- `get-sth` — returns the Signed Tree Head, including the current `tree_size`
  (total number of entries).
- `get-entries?start=N&end=M` — returns a contiguous batch of entries; each
  decodes to an X.509 certificate or precertificate.

There is no server push. "Streaming" is implemented as poll-`get-sth` +
page-`get-entries` from our last processed index to the new head. The set of
active, trusted logs is published as a signed JSON list by Google and by the
[CT log list](https://www.gstatic.com/ct/log_list/v3/log_list.json). Logs are
sharded and rotated (commonly per-year), so the active set changes over time.

---

## 3. Requirements

### Functional

- F1. Stream entries from one or more CT logs concurrently.
- F2. Extract Subject CN and all `dNSName` SAN entries from each certificate.
- F3. Match domains against the watchlist via three rule classes: exact,
  suffix/subdomain, and fuzzy (typosquat).
- F4. Deduplicate alerts (precert + final cert, same domain across logs).
- F5. Emit structured alerts to pluggable sinks.
- F6. Resume from last processed position after restart, per log.

### Non-functional

- N1. **Reliability:** a single failing/slow log must not stall others; the
  process should run for weeks unattended and recover from transient errors.
- N2. **Correctness of resume:** no silent gaps; on restart we either continue
  exactly or record an explicit gap.
- N3. **Observability:** expose metrics (throughput, per-log lag, match rate,
  error counts), structured logs, and health endpoints.
- N4. **Performance:** sustain the aggregate issuance rate of the watched logs
  (tens of thousands of certs/min across busy logs) on a small VM; matching is
  the hot path and must be cheap.
- N5. **Operability:** single static binary, config via file + env + flags,
  graceful shutdown, clean container image.
- N6. **Security/supply chain:** pinned dependencies, no secrets in logs,
  least-privilege runtime.

---

## 4. Architecture

```
                         ┌────────────────────┐
   log_list.json ───────►│   Log Discovery     │  refreshes active log set
                         └─────────┬──────────┘
                                   │ []LogConfig
                                   ▼
                         ┌────────────────────┐
                         │    Coordinator      │  supervises one worker/log
                         └─────────┬──────────┘
              ┌────────────────────┼────────────────────┐
              ▼                    ▼                     ▼
        ┌───────────┐        ┌───────────┐         ┌───────────┐
        │  Watcher  │        │  Watcher  │   ...   │  Watcher  │  goroutine/log
        │  (log A)  │        │  (log B)  │         │  (log N)  │  owns its cursor
        └─────┬─────┘        └─────┬─────┘         └─────┬─────┘
              │  ParsedCert        │                     │
              └────────────────────┼─────────────────────┘
                                   ▼ (buffered channel)
                         ┌────────────────────┐
                         │      Matcher        │  worker pool; exact/suffix/fuzzy
                         └─────────┬──────────┘
                                   │ Match
                                   ▼
                         ┌────────────────────┐
                         │   Dedup + Fanout    │  LRU/TTL cache, then sinks
                         └─────────┬──────────┘
              ┌────────────────────┼────────────────────┐
              ▼                    ▼                     ▼
        ┌───────────┐        ┌───────────┐         ┌───────────┐
        │  stdout   │        │ JSONL file│         │  webhook  │   Alerter sinks
        └───────────┘        └───────────┘         └───────────┘

  Cross-cutting: State store (per-log cursors) · Metrics/HTTP server · Logger
```

### Concurrency model

- One **watcher goroutine per log**, each owning its cursor — no shared mutable
  paging state, so logs are fully isolated.
- A **bounded buffered channel** (`chan ParsedCert`) connects watchers to a
  **matcher worker pool**. Backpressure: if matching falls behind, watchers
  block on send rather than growing memory unbounded.
- Matcher output goes through a **dedup stage** (TTL cache keyed by
  normalized domain + matched rule) into the **fanout** that calls each sink.
- The **coordinator** supervises watchers: restarts a crashed watcher with
  backoff, and on log-list refresh starts/stops watchers as logs are
  added/retired.

### Why these boundaries

Each stage has one responsibility and a typed channel interface, which makes
units independently testable (feed a watcher a fake `LogClient`; feed the
matcher synthetic `ParsedCert`s; feed sinks synthetic `Match`es). It also lets
the hot path (matching) scale by pool size independently of network IO.

---

## 5. Component Design

### 5.1 Log discovery
Fetch and parse the signed CT log list; filter to usable (non-retired) logs.
Allow config to pin an explicit subset (watching every log is wasteful; a few
high-coverage logs catch the vast majority of issuance). Refresh on an interval;
diff against running watchers and reconcile.

### 5.2 Watcher
Per-log loop: load cursor → `get-sth` → if `head > cursor`, page `get-entries`
in `batch_size` chunks → decode entries → emit `ParsedCert` → persist cursor
after each batch. Respects context cancellation. On error: classify
(retryable vs. fatal), apply exponential backoff with jitter for retryable,
surface fatal to the coordinator. First run starts at current head (no full
backfill) unless `backfill_from` is configured.

### 5.3 Certificate parsing
Use `github.com/google/certificate-transparency-go` for the client and
leaf/cert decoding. Handle both `X509Cert` and `Precert` entries. Extract CN +
DNS SANs, normalize (lowercase, strip trailing dot, IDN/punycode-aware),
dedupe per cert.

### 5.4 Matcher
Three rule classes, cheapest first:
1. **Exact / suffix:** O(1) lookup of the registrable domain against a set;
   suffix handled by checking the domain and its parent labels.
2. **Fuzzy / typosquat:** at startup, generate permutations of each watched
   domain (character swap/omission/insertion/repetition, homoglyph
   substitution, adjacent-key typos, TLD swaps, combosquatting affixes) into a
   lookup set. Plus an optional **edit-distance** check (Levenshtein ≤ N) on
   the registrable domain to catch permutations the generator misses.
Public Suffix List is used to compute the registrable domain correctly
(`foo.co.uk` etc.). Every match records *which rule fired* for analyst triage.

### 5.5 Dedup + fanout
TTL/LRU cache keyed by `normalized_domain|rule`. Suppresses precert+cert pairs
and the same domain seen across multiple logs within the window. Fanout invokes
each configured sink; a slow/failing sink is isolated (its own goroutine +
timeout) so it cannot block others.

### 5.6 Alerter sinks (interface)
```go
type Sink interface {
    Notify(ctx context.Context, m Match) error
    Name() string
}
```
v1 sinks: `stdout` (human), `jsonl` (one JSON object per line, SIEM-friendly),
`webhook` (POST JSON, for Slack/Teams/generic). Each sink wraps retries +
timeout; failures increment a metric and log but never crash the pipeline.

### 5.7 State store
Per-log cursor persistence. v1: a single JSON file written atomically
(temp-file + rename). Interface allows swapping for BoltDB/SQLite if the
seen-cache or cursor set grows. Resume semantics: persisted cursor is the
**high-water mark of fully processed entries**; we persist only after a batch is
emitted, so at-least-once delivery (a crash may re-emit a batch — dedup absorbs
this).

---

## 6. Data Model

```go
type ParsedCert struct {
    LogURL    string
    Index     int64
    Domains   []string  // normalized CN + SANs
    NotBefore time.Time
    NotAfter  time.Time
    Issuer    string
    SerialHex string
}

type Match struct {
    Cert        ParsedCert
    Domain      string    // the specific domain that matched
    Watched     string    // the watchlist entry it matched against
    Rule        string    // "exact" | "suffix" | "homoglyph" | "editdist:1" ...
    DetectedAt  time.Time
}
```

Alert JSON (jsonl/webhook) is a stable, versioned schema (`schema_version`
field) so downstream consumers don't break when fields are added.

---

## 7. Configuration

Precedence: flags > environment (`AKITA_*`) > config file > defaults.

```yaml
logs:
  discovery: dynamic            # dynamic | static
  pin:                          # used when discovery=static, or to restrict dynamic
    - https://oak.ct.letsencrypt.org/2025h2/
  refresh_interval: 12h

watch:
  domains: [mycompany.com, mybank.example]
  typosquat:
    enabled: true
    homoglyphs: true
    tld_swaps: [com, net, org, co, io]
    max_edit_distance: 1

pipeline:
  batch_size: 256
  poll_interval: 10s
  matcher_workers: 4
  channel_buffer: 4096
  dedup_ttl: 24h

alerts:
  stdout: true
  jsonl: { enabled: true, path: ./matches.jsonl }
  webhook: { enabled: false, url: "", timeout: 5s, retries: 3 }

state:
  path: ./Akita.state

observability:
  http_addr: ":9090"            # /metrics, /healthz, /readyz
  log_level: info
  log_format: json
```

---

## 8. Observability

- **Metrics (Prometheus):** `certs_processed_total{log}`,
  `entries_lag{log}` (head − cursor), `matches_total{rule}`,
  `poll_errors_total{log}`, `sink_errors_total{sink}`,
  `batch_fetch_seconds` (histogram), `channel_fill_ratio`.
- **Logs:** structured JSON (slog), levelled; never log full webhook URLs or
  secrets.
- **Health:** `/healthz` (process up), `/readyz` (at least one watcher has a
  fresh STH within N intervals).
- **Tracing:** optional OpenTelemetry spans around fetch→parse→match→notify
  (deferred; hook points reserved).

---

## 9. Failure Modes & Mitigations

| Failure | Detection | Mitigation |
|---|---|---|
| Log returns 5xx / rate-limits | poll error metric | exp. backoff + jitter; isolate per log |
| Log goes read-only / retired | 4xx / discovery diff | coordinator stops that watcher |
| Long downtime → huge gap | lag metric on restart | configurable: skip-to-head + log gap, or bounded backfill |
| Sink down (webhook) | sink_errors metric | retries + timeout; never blocks pipeline |
| Matcher overloaded | channel_fill_ratio high | backpressure to watchers; scale matcher_workers |
| State file corrupt | parse error on load | atomic writes prevent it; on parse fail, fail closed with clear error |
| Malformed cert entry | parse returns !ok | skip + count, don't crash |
| Clock skew on dedup TTL | n/a | use monotonic time for TTL where possible |

---

## 10. Security & Supply Chain

- Reads only public data; no credentials needed for CT logs. Webhook secrets
  (if any) come from env, never logged.
- Dependencies pinned in `go.mod`/`go.sum`; CI runs `govulncheck` and
  `go vet`; dependable, minimal dependency surface.
- Container: distroless/static base, non-root user, read-only root FS, only the
  state path writable.
- Input safety: treat all cert fields as untrusted strings; bound sizes; the
  defanged form is used in any human-facing output to avoid accidental clicks.

---

## 11. Deployment

- **Artifact:** single static binary (`CGO_ENABLED=0`) + minimal container
  image. Cross-compiled for linux/amd64 + arm64.
- **Runtime:** systemd unit or a Kubernetes Deployment (1 replica;
  see scaling note). Liveness/readiness wired to the health endpoints.
- **Config & state:** config via ConfigMap/file; state on a persistent volume
  (so restarts resume).
- **Scaling note:** a single replica owns all watchers (cursors are local
  state). Horizontal scaling = shard logs across replicas by config, not
  auto-coordination (a leader-election/sharded design is a future item, §13).

---

## 12. Testing Strategy

- **Unit:** matcher rule classes (table-driven, incl. IDN/punycode and PSL edge
  cases); typosquat generator; state atomicity; config precedence.
- **Watcher with fakes:** inject a fake `LogClient` returning scripted STH +
  entries (including gaps, errors, empty batches) to assert paging/resume.
- **Golden tests:** alert JSON schema stability.
- **Integration:** run against a public log for a bounded window in CI (network
  gated/optional), assert it ingests and reports.
- **Load:** synthetic cert generator to push the matcher at target rate;
  assert backpressure works and memory stays bounded.
- **Race:** `go test -race` in CI across the pipeline.
- Coverage targets: matcher and state ≥ 90%; overall ≥ 75%.

---

## 13. Roadmap

Each milestone is independently shippable and leaves `main` green.

**M0 — Project skeleton & CI** _(foundation)_
- Module layout (`/cmd/Akita`, `/internal/...`), `Makefile`, `golangci-lint`,
  GitHub Actions (build, vet, test, `govulncheck`), `Dockerfile`.
- Config loader with flags/env/file precedence + tests.

**M1 — Single-log watcher (walking skeleton)**
- LogClient integration, `get-sth`/`get-entries` paging, cert/precert parsing,
  CN+SAN extraction, prints domains. State persistence + graceful shutdown.
- _(The current scaffold lands roughly here; it gets refactored into the
  module layout from M0.)_

**M2 — State & resume hardening**
- Atomic state, first-run-at-head vs. bounded backfill, gap detection/logging,
  watcher-with-fakes test suite (errors, empty batches, gaps).

**M3 — Exact & suffix matching + stdout alerts**
- PSL-based registrable-domain logic, normalization (IDN/punycode), the
  `Match` model, dedup cache, human-readable stdout sink. Table-driven tests.

**M4 — Typosquat engine**
- Permutation generator (swaps/omissions/insertions/homoglyphs/keyboard/TLD/
  combosquat) + Levenshtein fallback, tunable via config. Generator tests.

**M5 — Pluggable sinks**
- `Sink` interface, JSONL sink (versioned schema, golden tests), webhook sink
  (retries/timeout), fanout with per-sink isolation.

**M6 — Concurrency & coordinator**
- Multi-log fan-in, matcher worker pool, bounded channel + backpressure,
  coordinator with supervised restart + backoff. `-race` across pipeline.

**M7 — Observability**
- Prometheus metrics, slog structured logging, `/healthz` `/readyz`,
  dashboards/example alerts.

**M8 — Productionization**
- Dynamic log-list discovery + reconcile, distroless image, systemd unit +
  k8s manifests, load test, docs, `v1.0.0` release with cross-compiled binaries.

**Post-1.0 (deferred):** active enrichment module (resolve/title/screenshot),
STIX export, sharded multi-replica deployment with leader election, OTel
tracing.

---

## 14. Open Questions

- Which logs to pin by default for good coverage vs. volume? (Measure issuance
  rates during M1/M2.)
- Backfill policy after downtime: skip-to-head (lossy but cheap) vs. bounded
  backfill window — make configurable, choose a sane default.
- Dedup window default (24h?) — balance against memory for high-volume watch
  lists.
- Do we need persistent dedup (survives restart) or is in-memory acceptable?
  (In-memory for v1; revisit if restart-storm duplicate alerts annoy.)
