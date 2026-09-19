---
name: docker-dev-stack
description: >
  How the evercall repo is containerised for development and production: the
  multistage Dockerfile, the docker-compose service graph, the Traefik routing
  for the public webhook endpoint, and the dev-loop init script. Apply this
  skill when adding/editing services, build stages, environment variables,
  Traefik labels, volumes, or the dev hot-reload setup. Trigger phrases:
  "docker-compose", "Dockerfile", "build target", "add a service", "Traefik",
  "routing", "hot reload", "dev container", "prod build", "compose override".
---

# Docker Dev Stack (evercall)

Everything runs in containers. Nothing — Go, nats — is expected to be installed
on the host. Org-wide rules (Traefik networks, label patterns, env handling)
live in `.agents/rules/rules.md`; this skill describes the *concrete shape* of
this repo's Docker setup.

This repo is a **single headless Go service**. There is no `ui`, no `db`, no
`phpmyadmin` and no `mail` service — if you are copying a compose file from
another Nathejk repo, delete those first.

> **Greenfield.** None of these files exist yet; this is the target shape.

---

## Files

```
docker-compose.yml                   # service graph (committed)
docker-compose.override.yml           # local secrets / dev overrides (gitignored)
docker-compose.override.yml.example    # template for the above (committed)
docker/
├── Dockerfile                  # multistage: api-dev → base → build, prod (prod last)
├── init/api-dev                # dev entrypoint: test/vet/build + restart loop
└── bin/init                    # prod entrypoint (exec's the binary as PID 1)
```

---

## Multistage Dockerfile

```
golang:1.24 ──► api-dev ──► base ──► build ──┐
                                             ▼
                                  alpine ──► prod
```

| Stage     | Used by                        | Notes                                                |
|-----------|--------------------------------|------------------------------------------------------|
| `api-dev` | intermediate (base for `base`) | Go toolchain + `inotify-tools` + `GOCACHE=/go/.cache` |
| `base`    | `api` service in compose       | `api-dev` + `go mod download` + source copied        |
| `build`   | intermediate, prod build only  | runs `go vet` + `go test`, builds the static binary  |
| `prod`    | the production image           | alpine + static binary + `docker/bin/init`, non-root |

Rules:

- **`prod` must stay the last stage.** The workflow builds with `file:
  docker/Dockerfile` and **no `target:`**, so Docker uses the final stage. Add
  new stages *above* `prod`, never below it, or CI will publish the wrong thing.
- The build context is the **repo root**, not `go/` — paths in `COPY` are
  `go/...` and `docker/bin/...`. `.dockerignore` trims the rest.
- `go.sum` is copied via the glob `go/go.sum*` so it stays optional while the
  module has no third-party dependencies. `COPY` fails on a missing literal path.
- Dev `up` only ever needs `api-dev`/`base`. **Never** target `prod` (or
  `build`) for `docker compose up` — no source mounts, no hot reload.
- `prod` contains nothing but the binary, `/init`, and ca-certificates/tzdata.
  There is no SPA to copy in and no `/www`.
- The binary is built with **`CGO_ENABLED=0`** plus `-trimpath` and `-w -s`,
  giving a genuinely static, libc-free binary — that is what lets `prod` be bare
  alpine. The module is stdlib-only, so nothing needs cgo. (Don't copy hq's
  `CGO_ENABLED=1 -extldflags "-static"` incantation here; it solves a problem
  this repo doesn't have.)
- `prod` runs as the unprivileged user `evercall` (uid 10001), so it **cannot
  bind a port below 1024**. Hence `PORT=8080` / `EXPOSE 8080`, and hence Traefik
  needs an explicit `loadbalancer.server.port=8080` on this service.
- Adding a stage: chain `FROM <existing-stage> AS <new-stage>` in the same
  Dockerfile — never add a sibling Dockerfile.

### Build args → binary

The workflow passes `GIT_COMMIT`, `GIT_BRANCH`, `BUILD_NUMBER` and
`BUILD_VERSION`. The `build` stage declares them as `ARG`s and injects them with
`-ldflags "-X github.com/nathejk/evercall/internal/vcs.<Field>=..."`, so a
running container can be traced back to a commit:

```sh
docker run --rm nathejk/evercall -version
# version=main.42 commit=abc123 branch=main build=42
curl -s http://.../healthcheck
# {"status":"available","version":"main.42","commit":"abc123"}
```

Keep the arg names in sync across the workflow, the Dockerfile and
`internal/vcs`. A new `ARG` that isn't threaded through to `vcs` is dead weight —
if you add one, wire it or drop it.

---

## docker-compose.yml — service graph

| Service | Image / build       | Role                                                    |
|---------|---------------------|---------------------------------------------------------|
| `api`   | build target `base` | the evercall binary — webhook receiver + fetcher        |

That is the whole graph. Shared infrastructure (Traefik, NATS JetStream) is
owned by the org infra repo and reached over external networks.

The dev entrypoint is **mounted**, not baked in
(`./docker/init/api-dev:/init-dev:ro` + `entrypoint: /init-dev`), so the dev loop
can be edited without rebuilding the image. The `base` stage doesn't copy
`docker/` at all.

### Networks

- `local` — created by this repo.
- `traefik` — **external**, from the infra repo. `api` joins it because the
  webhook must be routable.
- `jetstream` — **external**, from the infra repo. `api` joins it to reach NATS
  at `nats://jetstream:4222`.

Never define `traefik` or `jetstream` locally; declare them `external: true`.

### Routing

Unlike other Nathejk repos, the web-exposed service here is not a browser SPA —
it is a machine-to-machine webhook. It still gets Traefik labels, over **HTTPS
with an HTTP→HTTPS redirect**: the provider posts credentials/signatures, so
plain HTTP is not acceptable even in dev, and matching dev to prod means the
signature-verification path is exercised the same way in both.

```yaml
api:
  networks:
    - local
    - traefik
    - jetstream
  labels:
    traefik.enable: true
    traefik.docker.network: traefik
    traefik.http.services.evercall.loadbalancer.server.port: 8080
    # Own redirect middleware — see the warning below.
    traefik.http.middlewares.evercall-redirect-to-https.redirectscheme.scheme: https
    traefik.http.middlewares.evercall-redirect-to-https.redirectscheme.permanent: true
    traefik.http.routers.evercall.rule: Host(`evercall.local.nathejk.dk`)
    traefik.http.routers.evercall.entrypoints: web
    traefik.http.routers.evercall.middlewares: evercall-redirect-to-https
    traefik.http.routers.evercall-secure.rule: Host(`evercall.local.nathejk.dk`)
    traefik.http.routers.evercall-secure.entrypoints: websecure
    traefik.http.routers.evercall-secure.tls.certresolver: desec
```

**There is no shared `redirect-to-https@docker` middleware**, despite what the
org-wide rules doc implies. A middleware declared through docker labels is
scoped to the container that declares it, so referencing another repo's leaves
your router `status: disabled` and Traefik answers a bare **404** on the HTTP
entrypoint while HTTPS works fine — a confusing failure worth recognising.
Declare your own, repo-prefixed, as above. (hq does the same with
`hq-redirect-to-https`.) Only the `desec` cert resolver is genuinely shared.

To diagnose routing, ask Traefik what it actually loaded:

```sh
curl -s http://local.nathejk.dk/api/http/routers     | python3 -m json.tool
curl -s http://local.nathejk.dk/api/http/middlewares | python3 -m json.tool
```

A `"status":"disabled"` router is almost always a missing middleware reference.

The `loadbalancer.server.port=8080` label is **required** here: Traefik defaults
to 80, and this container listens on 8080 because it runs unprivileged (see the
Dockerfile rules above).

Use repo-scoped router/service names (`evercall`, `evercall-<service>`) so they
don't collide with other repos on the shared Traefik.

Note that `evercall.local.nathejk.dk` is **not reachable from the internet**, so
the real provider cannot deliver to it. Test locally by replaying captured
payloads with `curl`; do not open the dev container to the internet.

### Volumes

- `./go:/app` — source mount for hot reload. Keep it.
- `./docker/init/api-dev:/init-dev:ro` — the dev entrypoint.
- `api:/go` — named volume for the Go module/build cache; speeds up the restart
  loop.
- `../shared-go:/shared-go` — **not mounted yet.** Add it (plus a `go/go.work`)
  when event payloads move to `github.com/nathejk/shared-go`. Don't add it
  early: Docker would silently create an empty `../shared-go` directory on the
  host.
- A recordings volume (path TBC) for downloaded audio, once the fetcher exists,
  so a container restart doesn't re-download everything. Prod uses object
  storage instead.

### Environment variables

Committed dev defaults in `docker-compose.yml`:

| Var | Purpose |
|---|---|
| `PORT` | HTTP listen port (`8080`; the image sets this) |
| `MAX_BODY_BYTES` | cap on the request body the service reads (default 1 MiB) |
| `JETSTREAM_DSN` | `nats://jetstream:4222` |
| `EVERCALL_BASEURL` | Evercall REST API base (TBC) |
| `RECORDINGS_DSN` | where fetched audio is written (TBC) |

Secrets — `EVERCALL_TOKEN`, `EVERCALL_WEBHOOK_SECRET` — go **only** in
`docker-compose.override.yml`, which is gitignored. Copy
`docker-compose.override.yml.example` to start, and keep that example in sync
when you add a credential, since it is the only committed record that the var
exists. Read env at the binary's entrypoint
(`flag.StringVar(..., os.Getenv(...), ...)`), never deep in a call tree.

---

## Dev loop

`docker/init/api-dev` runs an `inotifywait` loop:

1. `go mod download` once at startup.
2. `go test -timeout 10s ./...` → `go vet ./...` → `go build -o /tmp/evercall
   ./cmd/evercall`.
3. If all pass, run `/tmp/evercall &`; otherwise log the failure and **leave the
   service down**, so a broken save is never mistaken for a working service.
   (Traefik then returns 502 — that is the intended signal.)
4. `inotifywait -r --include '\.go$'` blocks until a Go file changes.
5. On change: `kill -TERM` the binary, `wait` for its graceful shutdown, loop.

It compiles and runs a **binary** rather than using `go run`. `go run` starts the
program as a *grandchild*, so killing the `go run` pid leaves the old server
holding port 8080 and the next restart dies with "address already in use". A
direct child can be signalled reliably. Don't switch this back.

Add `go tool staticcheck ./...` to the gate once it is registered as a `tool`
directive in `go.mod` — it isn't yet. Ditto the report-only `gosec` /
`govulncheck` startup pass.

Dev tools are `tool` directives in `go/go.mod`, run via `go tool`, so they match
the current toolchain. The build cache lives in the `api:/go` volume
(`GOCACHE=/go/.cache/go-build`, set in the Dockerfile) so tools aren't
recompiled on every start.

`GO_BUILD_FLAGS` (e.g. `-race`) is passed to `go build` and is set via env in
compose.

---

## Common commands

```sh
docker compose up -d
docker compose build api            # after Dockerfile / go.mod change
docker compose logs -f api
docker compose run --rm api go test ./...

# the service, through Traefik
curl -sk https://evercall.local.nathejk.dk/healthcheck

# replay a captured webhook payload
curl -sk -X POST -H 'Content-Type: application/json' \
  --data @go/cmd/evercall/testdata/call-received.json \
  https://evercall.local.nathejk.dk/webhook/evercall

docker compose down -v              # nuke local volumes (go cache, recordings)
```

Production image locally — build it the way CI does, with no `--target`:

```sh
docker build -f docker/Dockerfile \
  --build-arg GIT_COMMIT=local --build-arg BUILD_VERSION=local.0 \
  -t evercall:local .

docker run --rm -p 18080:8080 evercall:local
docker run --rm evercall:local -version

# /init execs the binary with any extra args, so inspecting the image needs
# an explicit entrypoint override:
docker run --rm --entrypoint sh evercall:local -c 'id'
```

---

## Adding a service

1. Add the service block to `docker-compose.yml`.
2. Put it on `local`. Add `traefik` / `jetstream` only if it actually needs the
   shared external networks.
3. If it must be reachable from outside, join `traefik` and add its own labels:
   `traefik.enable`, `traefik.docker.network=traefik`, and a
   ``traefik.http.routers.evercall-<service>.rule=Host(`<sub>.local.nathejk.dk`)``.
   Add `loadbalancer.server.port` only for a non-80 container port.
4. If it needs a build, extend `docker/Dockerfile` with a new stage.
5. Document required env vars in `docker-compose.yml` with a sane dev default;
   real secrets in `docker-compose.override.yml`.

Before adding a datastore service, check `.rules`: this service deliberately has
no SQL read model, and adding one is a PRD-level decision, not a compose edit.

---

## Don'ts

- Don't run Go directly on the host.
- Don't add a stage below `prod` in the Dockerfile — CI builds without
  `--target` and takes the last stage.
- Don't change `docker/bin/init` to launch the binary without `exec`. Without
  it the shell stays PID 1, SIGTERM never reaches the server, graceful shutdown
  is skipped and Docker kills the container on the stop timeout.
- Don't `docker compose up` the `prod` target.
- Don't define a local `traefik` or `jetstream` network — they are `external`.
- Don't reuse another repo's router/service names on the shared Traefik; scope
  them with the `evercall` prefix. The same goes for middlewares, which are not
  shared between containers at all.
- Don't replace the dev loop's `go build` + run with `go run` (orphaned
  grandchild keeps the port).
- Don't commit `docker-compose.override.yml`, and don't move secrets out of it
  into `docker-compose.yml`.
- Don't publish container ports with `ports:` in dev — Traefik handles routing.
  (`EXPOSE` in the Dockerfile is fine and expected; Traefik's docker provider
  uses it to discover the port.)
- Don't copy the `ui`, `db`, `phpmyadmin` or `mail` services in from another
  Nathejk repo. This service has no frontend and no database.
