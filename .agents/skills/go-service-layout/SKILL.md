---
name: go-service-layout
description: >
  Conventions and directory layout for the headless Go service in the evercall
  repo — the Evercall webhook ingest and the JetStream-driven recording
  fetcher. Apply this skill when adding a webhook handler or route, a JetStream
  consumer, an event subject, a call-payload normaliser, the Evercall REST
  client, the recording store, or when wiring a new dependency into
  `cmd/evercall`. Trigger phrases: "add a webhook", "new handler", "new route",
  "publish an event", "new subject", "add a consumer", "fetch the recording",
  "normalise the payload", "wire a dependency".
---

# Go Service Layout (evercall)

Evercall is a **single, headless Go binary**: it takes call information from the
Evercall VoIP platform over a webhook, publishes it to NATS JetStream, and later
fetches the call's audio recording from the Evercall REST API and stores it.

There is no frontend, no SPA bundle to serve, and no MariaDB read model. If a
change seems to need one of those, it needs a PRD first.

Everything is developed and run inside the `api` container — never on the host.

> **Greenfield.** The layout below is the target, not a description of existing
> code. Follow it when creating files; correct this document if the real shape
> diverges for a good reason.

---

## Top-level layout

```
go/
├── cmd/evercall/           # the only binary — wiring, HTTP server, mux startup
│   ├── main.go             # config from env, dependency wiring, graceful shutdown
│   ├── routes.go           # httprouter: /webhook/..., /healthcheck
│   ├── webhook.go          # one handler per provider callback
│   └── app/                # transport helpers (errors, json, middleware,
│                           #   server, healthcheck) — embed `app.JsonApi` on the
│                           #   application struct to inherit them
├── internal/
│   ├── evercall/           # Evercall REST client: auth + recording download
│   ├── ingest/             # provider payload → domain event normalisation
│   ├── fetcher/            # JetStream consumer: recording retrieval + retry
│   ├── recordings/         # recording store abstraction (filesystem / object)
│   ├── jsonlog/            # structured logger
│   └── vcs/                # build-time version embedding
└── go.mod
```

Rules of placement:

- **`cmd/evercall/`** — wiring and transport only. No normalisation logic, no
  provider HTTP calls.
- **`internal/`** — everything else. There is no `nathejk/` domain tree here:
  the domain aggregates (and their read models) live in hq and shared-go, not in
  an integration adapter.
- Do **not** create a `pkg/`. Generic, non-domain code belongs in an external
  module (`github.com/jrgensen/cqrs`, `github.com/jrgensen/stream`).
- Do **not** add a second `cmd/<binary>`. Extra work is a JetStream consumer in
  this process.

## External modules

Nothing streaming-related is vendored:

| Module | Provides |
|---|---|
| `github.com/jrgensen/stream` | `jetstream`, `xstream` (Mux), `subject`, `metatagger` |
| `github.com/jrgensen/cqrs` | `Publisher`, `Consumer`, `Message`, `Subject`, `cqrstest` fakes |
| `github.com/nathejk/shared-go` | shared domain types (`.../types`) and event payloads (`.../messages`) |

Take `cqrs.Publisher` / `cqrs.Consumer` in constructors — never a concrete
JetStream client. That is what makes the fetcher testable with `cqrstest` fakes
instead of a live broker.

**Event payload structs belong in shared-go**, not here. This repo is the
producer; hq is the consumer. A payload type declared locally will be
re-declared over there and the two will drift.

---

## The two flows

### 1. Ingest (HTTP in → JetStream)

```
Evercall POST /webhook/evercall
  → verify signature/secret          (reject unauthenticated senders)
  → app.ReadJSON into a provider DTO (internal/ingest)
  → normalise to a shared-go message, keeping the raw body
  → publisher.Publish(NATHEJK.call.{callId}.{event})
  → 2xx  (only after a successful publish)
```

Non-negotiables, because the provider retries and we cannot ask it to stop:

1. **No blocking I/O beyond the publish.** Never fetch a recording, hit another
   HTTP API, or wait on a slow store in the request path — a slow webhook is
   indistinguishable from a failed one and earns you duplicate deliveries.
2. **`2xx` means "durably published".** Returning 200 after a failed publish
   loses the call permanently, since the provider will not retry a success.
3. **Idempotent by call id.** Key every event on the provider's own call id so a
   redelivery overwrites rather than duplicates. Don't mint your own id.
4. **Unknown event type → log + `2xx`.** A new provider event we don't handle
   must not become an infinite retry loop.

### 2. Fetch (JetStream → Evercall REST → store → JetStream)

```
consume NATHEJK.call.{callId}.ended
  → internal/evercall: GET the recording
      ├─ not ready yet   → NAK with delay, bounded attempts
      ├─ transient error → NAK with backoff
      └─ permanent error → log, record the failure, ACK (don't wedge the stream)
  → internal/recordings: store the audio
  → publish NATHEJK.call.{callId}.recorded with the stored location
```

The recording is not available the instant the call ends, so "not ready" is a
normal outcome and must be distinguished from a real failure. Attempt counts and
"already fetched" bookkeeping go in JetStream KV — not a SQL table, and not
process memory (it is lost on the restart that the retry depends on).

Publish the **location** of the audio, never the bytes. Recordings are large and
the stream is replayed.

---

## Conventions

### Adding a webhook endpoint

1. Register the route in `cmd/evercall/routes.go`.
2. Add the handler to `cmd/evercall/webhook.go`, named
   `<provider><Event>WebhookHandler`.
3. Verify the sender first, then `app.ReadJSON`.
4. Normalise in `internal/ingest` — the handler stays a thin transport shim.
5. Respond with `app.WriteJSON` / `app.ServerErrorResponse`. Never bare
   `http.Error` or `json.NewEncoder`.

### Adding a consumer

1. Put it in its own `internal/<name>/` package with a
   `New(p cqrs.Publisher, …)` constructor.
2. Declare the subjects it consumes and a `Consume(msg cqrs.Message) error`.
3. Register it on the `xstream.Mux` in `main.go`.
4. Decide explicitly what ACK/NAK means for each failure class, and test all
   three (not-ready, transient, permanent).

### Configuration

Read every env var in `main.go` with
`flag.StringVar(..., os.Getenv("..."), ...)` and pass it down in a `config`
struct. Never read env deeper in the call tree. Add new vars to
`docker-compose.yml` with a sane dev default; real credentials go in the
gitignored `docker-compose.override.yml`.

### Logging

Structured logger only. Include the call id on every call-scoped line. **Never
log audio bytes or a full raw payload at info level** — call data is personal
data. Debug-level, with a deliberate decision behind it, or not at all.

### Tests and lint

The dev container re-runs these on every `.go` change (see
`docker/init/api-dev`) and will not restart the binary if any fail:

```sh
go test -timeout 10s ./...
go vet ./...                 # hard gate
go tool staticcheck ./...    # hard gate
go build ./...
```

Dev tools are `tool` directives in `go.mod`, run via `go tool`, so they are
pinned and always match the toolchain. Add one with `go get -tool <pkg>`
**inside the container** — never `go install` into the image, since the
`api:/go` volume would shadow the binary.

Two areas need tests before anything else:

- **Normalisation**, table-driven from captured real payloads in `testdata/`.
  Hand-written fixtures test our idea of the provider's schema, which is the
  thing most likely to be wrong.
- **Fetcher retry behaviour**, with `cqrs/cqrstest` fakes and a stubbed HTTP
  client.

CI has no separate test step: the Docker `build` stage runs
`go test -timeout 60s ./...` and staticcheck, so red tests fail the image build.

---

## Running things

```sh
docker compose build api                  # after a Dockerfile change
docker compose logs -f api
docker compose run --rm api go test ./...
docker compose run --rm --entrypoint go api tool staticcheck ./...
```

The `api` entrypoint already runs the test/lint/build loop and restarts on `.go`
changes via `inotifywait` — you rarely need to restart the container yourself.

To exercise the webhook locally, replay a captured payload with `curl` against
the container. The Evercall platform cannot reach `*.local.nathejk.dk`, so real
end-to-end delivery needs a tunnel or a deployed environment.

---

## Don'ts

- Don't do provider I/O in the webhook request path.
- Don't return `2xx` for an event you failed to publish.
- Don't declare event payload types locally instead of in shared-go.
- Don't add MariaDB, a read model, or an admin UI to this service.
- Don't add a second `cmd/` binary for background work.
- Don't import `cmd/` from `internal/` — dependencies flow inward.
- Don't take a concrete stream client where a `cqrs` interface would do.
- Don't run `go` on the host.
- Don't put recording bytes on an event, or a secret in `docker-compose.yml`.
