# stackstead

A lightweight Docker Compose management platform for home labs and small teams:
a single Go binary with a web UI for creating, running and debugging stacks.

Design: `docs/superpowers/specs/2026-10-14-stackstead-v1-design.md`

## Prerequisites

Docker must be installed on the host; the systemd unit runs the service with
the `docker` group.

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
STACKSTEAD_DATA_DIR=$PWD/.dev/data STACKSTEAD_STACKS_DIR=$PWD/.dev/stacks go run ./cmd/stackstead
```
