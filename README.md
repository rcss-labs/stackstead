# stackstead

A lightweight Docker Compose management platform for home labs and small teams:
a single Go binary with a web UI for creating, running and debugging stacks.

Design: `docs/superpowers/specs/2026-10-14-stackstead-v1-design.md`

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
printed in `journalctl -u stackstead`.

## Develop

```bash
go test ./...
STACKSTEAD_DATA_DIR=$PWD/.dev/data STACKSTEAD_STACKS_DIR=$PWD/.dev/stacks go run ./cmd/stackstead
```
