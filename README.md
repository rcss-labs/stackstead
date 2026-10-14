# stackstead

A lightweight Docker Compose management platform for home labs and small teams:
a single Go binary with a web UI for creating, running and debugging stacks.

Design: `docs/superpowers/specs/2026-10-14-stackstead-v1-design.md`

## Prerequisites

Docker Engine and the Docker Compose v2 plugin (`docker compose`) must be
installed on the host; the systemd unit runs the service with the `docker`
group. If Docker or the plugin is missing Stackstead still starts: read-only
views work and lifecycle endpoints answer 503 until it is available.

## API overview

All routes except setup and login need a session cookie. Errors are
`{"error": "..."}`, with `"fields": [{field, message}]` for validation.

| Method and path | Purpose |
| --- | --- |
| `GET /api/stacks`, `POST /api/stacks` | list stacks with status; create a stack from an image or Dockerfile |
| `POST /api/stacks/import` | register an existing compose file without modifying it |
| `GET /api/stacks/{name}` | detail with services and containers |
| `GET`/`PUT /api/stacks/{name}/compose` | raw YAML, saved with the hash from the read (409 if stale) |
| `PUT /api/stacks/{name}/services/{service}` | structured service edit |
| `POST /api/stacks/{name}/actions/{action}` | `up`, `down`, `restart`, `pull`, `build`; returns an operation (202) |
| `DELETE /api/stacks/{name}?volumes=true` | `compose down` then unregister (202) |
| `GET /api/stacks/{name}/operations` | recent operations |
| `GET /api/operations/{id}`, `/stream`, `POST .../cancel` | output, live SSE stream, cancel |
| `GET /api/fs?path=`, `GET /api/networks`, `GET /api/status` | file browser, Docker networks, Docker and compose status |

Notes on the stacks API:

- **Partial checks.** Creating or editing a stack checks new host ports and
  static IPs against the Docker engine. If the engine is unreachable only the
  ports declared by other stacks are checked, and the response carries the
  header `X-Stackstead-Checks: partial`.
- **POST needs JSON.** Every `POST` (including `actions/{action}` and
  `operations/{id}/cancel`) requires `Content-Type: application/json`, and a
  body, which may be just `{}`.
- **Operation stream.** `GET /api/operations/{id}/stream` is server-sent
  events: `line` (one output line, as a JSON string; earlier output is replayed
  first), `done` (the final operation as JSON, after which the stream ends) and
  `dropped` (the subscription ended without the operation finishing; fetch
  `GET /api/operations/{id}` instead).
- **Override files are not supported yet.** Stackstead runs compose with
  `-f <file>`, which turns off the automatic merge of `compose.override.yaml`
  (and `.yml`, `docker-compose.override.*`). Importing a file with an override
  next to it is refused, startup sync skips such folders with a warning, and
  lifecycle actions and edits are refused if one appears later. Merge or
  remove the override file.

Removing a stack never deletes files: the compose file and its folder stay on
disk, and volumes are removed only when `volumes=true`. Stopping Stackstead
never stops containers.

## Security model

Access to the UI is root-equivalent on the host (it controls the Docker
socket). By default stackstead binds to localhost only. For remote access put
a TLS reverse proxy in front and make it forward the original Host and client
address, for example with nginx:

```nginx
proxy_set_header Host $host;
proxy_set_header X-Forwarded-For $remote_addr;
```

If a proxy fronts an instance that has no admin yet, complete first-run setup
with the setup token from `journalctl -u stackstead`.

## Build

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/stackstead ./cmd/stackstead   # or GOARCH=arm64
```

## Install (systemd)

```bash
sudo useradd --system --no-create-home --shell /usr/sbin/nologin stackstead
sudo install -m 0755 bin/stackstead /usr/local/bin/stackstead
sudo install -d /etc/stackstead && sudo install -m 0644 deploy/config.example.yaml /etc/stackstead/config.yaml
sudo install -d -o stackstead -g stackstead /opt/stacks
sudo install -m 0644 deploy/stackstead.service /etc/systemd/system/stackstead.service
sudo systemctl daemon-reload && sudo systemctl enable --now stackstead
```

On first start, open the UI from the same machine, or use the setup token
printed in `journalctl -u stackstead`. Until the web UI exists (Plan 5), do
first-run setup with:

```bash
curl -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<12+ chars>","setupToken":"<token or empty on localhost>"}' \
  http://127.0.0.1:8420/api/setup
```

## Develop

```bash
go test ./...
go test -tags integration -race ./internal/stacks/   # needs a running Docker; creates and removes itest* resources
STACKSTEAD_DATA_DIR=$PWD/.dev/data STACKSTEAD_STACKS_DIR=$PWD/.dev/stacks go run ./cmd/stackstead
```
