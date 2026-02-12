# Contributing to CC Exporter

Thanks for your interest in contributing!

## Development Setup

**Prerequisites:** Go 1.23+, Docker with Compose

```bash
git clone https://github.com/aireet/cc-exporter.git
cd cc-exporter
```

### Run the exporter locally (without Docker)

```bash
cd exporter
export CLAUDE_STATS_FILE=$HOME/.claude/stats-cache.json
export CLAUDE_DIR=$HOME/.claude
go run main.go
```

Metrics will be available at `http://localhost:9101/metrics`.

### Run with Docker

```bash
./start.sh
```

## Making Changes

1. Fork the repo and create a branch from `main`
2. Make your changes
3. Ensure `go build ./...` and `go vet ./...` pass in the `exporter/` directory
4. Test locally with `docker compose up --build`
5. Submit a pull request

## Adding New Metrics

1. Define the metric in the collector struct in `exporter/main.go`
2. Register it in `NewClaudeCollector()`
3. Set the value in the `Collect()` method
4. Update the Metrics table in `README.md` and `README_zh.md`
5. Optionally add a panel in `grafana/dashboards/claude-tokens.json`

## Reporting Bugs

Use the [Bug Report](https://github.com/aireet/cc-exporter/issues/new?template=bug_report.md) template.

## License

By contributing, you agree that your contributions will be licensed under the MIT License.
