# CC Monitor

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

[中文文档](README_zh.md)

Out-of-the-box usage monitoring dashboard for [Claude Code](https://docs.anthropic.com/en/docs/claude-code).

Automatically collects token consumption, sessions, costs and more, visualized through Grafana. Supports real-time active session monitoring.

## Screenshots

![Overview](screenshots/cc-grafana-001.jpg)
![Trends](screenshots/cc-grafana-002.jpg)
![Per-Request Metrics](screenshots/cc-grafana-003.jpg)

## Quick Start

Make sure [Docker](https://docs.docker.com/get-docker/) (with Docker Compose) is installed, then:

```bash
git clone https://github.com/aireet/cc-monitor.git
cd cc-monitor
./start.sh
```

Open **http://localhost:3000/d/claude-token-monitor** in your browser. Default credentials: `admin` / `admin`.

## Architecture

```
~/.claude (read-only)
    │
    ├── stats-cache.json ──► ┌──────────────┐     ┌────────────┐     ┌─────────┐
    └── projects/*/?.jsonl ─► │   Exporter   ├────►│ Prometheus ├────►│ Grafana │
                              │  (Go, :9101) │     │   (:9099)  │     │ (:3000) │
                              └──────────────┘     └────────────┘     └─────────┘
```

- **Exporter** — Reads `stats-cache.json` (historical data) + scans active session JSONL files (real-time data), exposes Prometheus metrics
- **Prometheus** — Scrapes every 30s, retains data for 90 days
- **Grafana** — Pre-configured datasource and dashboard, ready to use

## Metrics

- Total token consumption (input / output / cache read / cache creation), by model
- Cost statistics (USD), by model
- Daily message count, session count, and tool call trends
- Daily token usage trends by model
- Hourly activity distribution
- Real-time active sessions and message counts

## Stop / Restart

```bash
# Stop
docker compose down

# Restart
./start.sh
```

## Configuration

### Claude Data Path

Reads from `~/.claude` by default. To use a different path:

```bash
export CLAUDE_HOME=/path/to/.claude
./start.sh
```

### Ports

Edit the port mappings in `docker-compose.yml`:

| Default Port | Service |
|--------------|---------|
| 3000 | Grafana |
| 9099 | Prometheus |
| 9101 | Exporter |

## Data Safety

- All Claude data is mounted as **read-only**
- All data is stored locally and never uploaded to any external service

## License

MIT
