#!/bin/bash
set -e

cd "$(dirname "$0")"

echo "=== Claude Code Monitor ==="
echo ""

# Detect Claude data directory
CLAUDE_HOME="${CLAUDE_HOME:-$HOME/.claude}"
if [ ! -d "$CLAUDE_HOME" ]; then
  echo "Error: Claude data directory not found: $CLAUDE_HOME"
  echo "Make sure Claude Code is installed and has been used at least once."
  echo "Or set CLAUDE_HOME to the correct path: export CLAUDE_HOME=/path/to/.claude"
  exit 1
fi
export CLAUDE_HOME
echo "Using Claude data: $CLAUDE_HOME"

# Create data directories
mkdir -p prometheus-data grafana-data
chmod 777 prometheus-data grafana-data

# Build and start
echo "[1/2] Building exporter..."
docker compose build --quiet claude-exporter

echo "[2/2] Starting services..."
docker compose up -d

echo ""
echo "=== All services started ==="
echo ""
echo "  Grafana:    http://localhost:3000  (admin / admin)"
echo "  Prometheus: http://localhost:9099"
echo "  Exporter:   http://localhost:9101/metrics"
echo ""
echo "  Dashboard:  http://localhost:3000/d/claude-token-monitor"
echo ""
echo "To stop:  docker compose -f $(pwd)/docker-compose.yml down"
echo "To logs:  docker compose -f $(pwd)/docker-compose.yml logs -f"
