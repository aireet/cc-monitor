# Changelog

## [1.0.0] - 2025-02-12

### Added
- Prometheus exporter for Claude Code usage metrics
- Token consumption tracking (input / output / cache read / cache creation) by model
- Real-time active session monitoring via JSONL scanning
- Daily and hourly activity trends
- Tool usage and stop reason breakdown
- API error and retry tracking
- Context compaction event counting
- Web search/fetch request counting
- Pre-configured Grafana dashboard with 3-panel layout
- Prometheus config with 30s scrape interval and 90-day retention
- Standalone exporter mode (`./start.sh --exporter`)
- Full stack mode with Exporter + Prometheus + Grafana (`./start.sh`)
- Bilingual documentation (English + Chinese)
