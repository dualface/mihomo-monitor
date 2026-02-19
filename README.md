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

HTTP_PROXY=http://127.0.0.1:8080
```

Optional settings:

- `TEST_URL` (default: `https://google.com`)
- `DELAY_TIMEOUT_MS` (default: `3000`)
- `AUTO_SELECT_DIFF_MS` (default: `300`)
- `MONITOR_INTERVAL_S` (default: `60`)
- `FILTER_HK_NODES` (default: `true`, filters `香港` / `HK` / `Hong Kong` nodes)

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

Build binary:

```bash
go build -o mihomo-monitor .
./mihomo-monitor --monitor
```

Notes:

- Non-JSON output sanitizes proxy names by removing symbols.
- `--print-delays` outputs only the 10 fastest nodes.

## Docker

Build:

```bash
docker build -t mihomo-monitor .
```

Run (monitor mode by default):

```bash
docker run --rm --env-file .env mihomo-monitor
```

If your `.env` uses `MIHOMO_CONTROLLER_URL` pointing to localhost on the host,
containers cannot reach it by default. Use one of these options:

- Linux: `--network host` and keep `MIHOMO_CONTROLLER_URL=http://127.0.0.1:9090`
- macOS/Windows: use `http://host.docker.internal:9090` in `.env`

Override command:

```bash
docker run --rm --env-file .env mihomo-monitor --print-delays --json
```
