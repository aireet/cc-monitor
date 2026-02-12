# CC Exporter

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.23-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![GitHub stars](https://img.shields.io/github/stars/aireet/cc-exporter)](https://github.com/aireet/cc-exporter/stargazers)
[![GitHub last commit](https://img.shields.io/github/last-commit/aireet/cc-exporter)](https://github.com/aireet/cc-exporter/commits/main)

[中文文档](README_zh.md)

Out-of-the-box usage monitoring dashboard for [Claude Code](https://docs.anthropic.com/en/docs/claude-code).

Automatically collects token consumption, sessions, tool usage and more, visualized through Grafana. Supports real-time active session monitoring.

## Screenshots

![Overview](screenshots/cc-grafana-001.jpg)
![Trends](screenshots/cc-grafana-002.jpg)
![Per-Request Metrics](screenshots/cc-grafana-003.jpg)

## Installation

### Option 1: Full Stack (Recommended)

One-command deploy: Exporter + Prometheus + Grafana with pre-configured dashboard.

```bash
git clone https://github.com/aireet/cc-exporter.git
cd cc-exporter
./start.sh
```

Open **http://localhost:3000/d/claude-token-monitor** in your browser. Default credentials: `admin` / `admin`.

| Service | URL |
|---------|-----|
| Grafana | http://localhost:3000 |
| Prometheus | http://localhost:9099 |
| Exporter | http://localhost:9101/metrics |

### Option 2: Exporter Only

For users who already have Prometheus and Grafana. This deploys only the metrics exporter.

```bash
git clone https://github.com/aireet/cc-exporter.git
cd cc-exporter
./start.sh --exporter
```

Verify it works:

```bash
curl http://localhost:9101/metrics
```

#### Configure Prometheus

Add the following scrape config to your Prometheus configuration:

```yaml
scrape_configs:
  - job_name: "claude-exporter"
    static_configs:
      - targets: ["<exporter-host>:9101"]
```

#### Import Grafana Dashboard

1. In Grafana, go to **Dashboards > Import**
2. Upload the JSON file from `grafana/dashboards/claude-tokens.json` in this repo
3. Select your Prometheus data source
4. Click **Import**

## Architecture

```
~/.claude (read-only)
    |
    +-- stats-cache.json ----> +----------------+     +--------------+     +-----------+
    +-- projects/*/?.jsonl --> |    Exporter    +---->|  Prometheus  +---->|  Grafana  |
                               |  (Go, :9101)  |     |   (:9099)    |     |  (:3000)  |
                               +----------------+     +--------------+     +-----------+
```

- **Exporter** -- Reads `stats-cache.json` (historical data) + scans active session JSONL files (real-time data), exposes Prometheus metrics
- **Prometheus** -- Scrapes every 30s, retains data for 90 days
- **Grafana** -- Pre-configured datasource and dashboard, ready to use

## Metrics

### Token Usage

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `claude_model_input_tokens` | Gauge | model | Input tokens by model |
| `claude_model_output_tokens` | Gauge | model | Output tokens by model |
| `claude_model_cache_read_tokens` | Gauge | model | Cache read tokens by model |
| `claude_model_cache_create_tokens` | Gauge | model | Cache creation tokens by model |

### Live Sessions

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `claude_live_input_tokens` | Gauge | model | Input tokens from active sessions |
| `claude_live_output_tokens` | Gauge | model | Output tokens from active sessions |
| `claude_live_sessions` | Gauge | -- | Number of active sessions |
| `claude_live_messages` | Gauge | -- | Number of messages in active sessions |

### Aggregates

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `claude_total_sessions` | Gauge | -- | Total sessions (all time) |
| `claude_total_messages` | Gauge | -- | Total messages (all time) |
| `claude_today_messages` | Gauge | -- | Messages today |
| `claude_today_sessions` | Gauge | -- | Sessions today |
| `claude_today_tool_calls` | Gauge | -- | Tool calls today |
| `claude_today_tokens` | Gauge | type | Tokens today (input/output) |

### Trends

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `claude_daily_messages` | Gauge | date | Messages per day |
| `claude_daily_sessions` | Gauge | date | Sessions per day |
| `claude_daily_tool_calls` | Gauge | date | Tool calls per day |
| `claude_daily_tokens` | Gauge | date, type | Tokens per day |
| `claude_hour_activity` | Gauge | hour, type | Activity by hour of day |

### Tools & Errors

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `claude_tool_use_total` | Gauge | tool | Tool usage count by tool name |
| `claude_stop_reason_total` | Gauge | reason | Stop reasons count |
| `claude_api_errors_total` | Gauge | -- | Total API errors |
| `claude_api_retries_total` | Gauge | -- | Total API retries |
| `claude_compact_events_total` | Gauge | -- | Context compaction events |
| `claude_web_search_total` | Gauge | -- | Web search requests |
| `claude_web_fetch_total` | Gauge | -- | Web fetch requests |

## Stop / Restart

```bash
# Stop (full stack)
docker compose down

# Stop (exporter only)
docker compose -f docker-compose.exporter.yml down

# Restart
./start.sh            # or ./start.sh --exporter
```

## Configuration

### Claude Data Path

Reads from `~/.claude` by default. To use a different path:

```bash
export CLAUDE_HOME=/path/to/.claude
./start.sh
```

### Ports

Edit the port mappings in the corresponding `docker-compose*.yml`:

| Default Port | Service |
|--------------|---------|
| 3000 | Grafana (full stack only) |
| 9099 | Prometheus (full stack only) |
| 9101 | Exporter |

## Data Safety

- All Claude data is mounted as **read-only**
- All data is stored locally and never uploaded to any external service

## License

MIT
