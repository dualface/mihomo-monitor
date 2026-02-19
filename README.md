# Mihomo Monitor

CLI utility for Mihomo controller delay checks, current proxy inspection, auto selection, and monitor loop.

## Requirements

- Go 1.23+
- Mihomo controller API enabled

## Install

```bash
go mod download
```

## Configuration

Create a `.env` file in the project root:

```env
MIHOMO_CONTROLLER_URL=http://127.0.0.1:9090
MIHOMO_CONTROLLER_SECRET=your_secret
MIHOMO_PROXY_GROUP=GLOBAL

TEST_URL=https://google.com
DELAY_TIMEOUT_MS=3000
AUTO_SELECT_DIFF_MS=300
MONITOR_INTERVAL_S=300

ENDPOINT_URLS=https://example.com/health,https://1.1.1.1
MIHOMO_PROXY_ADDR=socks5://127.0.0.1:7890
KEEP_DELAY_THRESHOLD_MS=2000
FILTER_HK_NODES=true
```

Required settings:

- `MIHOMO_CONTROLLER_URL`

Optional settings:

- `MIHOMO_CONTROLLER_SECRET` (Bearer token)
- `MIHOMO_PROXY_GROUP` (default: `GLOBAL`)
- `TEST_URL` (default: `https://google.com`)
- `DELAY_TIMEOUT_MS` (default: `3000`)
- `AUTO_SELECT_DIFF_MS` (default: `300`)
- `MONITOR_INTERVAL_S` (default: `300`)
- `ENDPOINT_URLS` (comma-separated URLs; used only when `MIHOMO_PROXY_ADDR` is set)
- `MIHOMO_PROXY_ADDR` (supports `http`, `https`, `socks5`, `socks5h`)
- `KEEP_DELAY_THRESHOLD_MS` (default: `2000`)
- `FILTER_HK_NODES` (default: `true`, filters `香港` / `HK` / `Hong Kong` nodes)

Notes:

- Exactly one action flag is required: `--print-delays`, `--print-current`, `--auto-select`, `--monitor`, or `--check-endpoints`.
- `HTTP_PROXY`/`HTTPS_PROXY` are ignored by this program.

## Usage

Print top 10 fastest nodes:

```bash
go run . --print-delays
go run . --print-delays --json
```

Print current proxy delay:

```bash
go run . --print-current
go run . --print-current --json
```

Auto select faster proxy:

```bash
go run . --auto-select
go run . --auto-select --json
```

Monitor loop (auto select every interval):

```bash
go run . --monitor
go run . --monitor --json
```

Test `ENDPOINT_URLS` through current proxy:

```bash
go run . --check-endpoints
go run . --check-endpoints --json
```

Build binary:

```bash
go build -o mihomo-monitor .
./mihomo-monitor --monitor
```

Notes:

- Non-JSON output sanitizes proxy names by removing symbols.
- `--print-delays` outputs only the 10 fastest nodes.
- JSON output escapes non-ASCII as `\uXXXX`.

## Auto-select behavior

`--auto-select` and `--monitor` use this decision order:

1. Load current proxy and group delays.
2. If endpoint checks are enabled and any endpoint is unreachable, switch to fastest node.
3. If current delay is `<= KEEP_DELAY_THRESHOLD_MS`, keep current node.
4. Otherwise, switch only when best node is faster than current by more than `AUTO_SELECT_DIFF_MS`.

## Systemd service

Install and start service:

```bash
./install.sh
```

Default paths:

- Binary: `/usr/local/bin/mihomo-monitor`
- Service unit: `/etc/systemd/system/mihomo-monitor.service`
- Env file: `/etc/mihomo-monitor.env`

Common operations:

```bash
systemctl status mihomo-monitor.service
systemctl restart mihomo-monitor.service
journalctl -u mihomo-monitor.service -f
```
