#!/bin/bash
set -e

cd "$(dirname "$0")"

echo "=== CC Exporter ==="
echo ""

# Parse arguments
MODE="full"
for arg in "$@"; do
  case $arg in
    --exporter)
      MODE="exporter"
      ;;
    *)
      echo "Unknown option: $arg"
      echo "Usage: ./start.sh [--exporter]"
      echo ""
      echo "  (no args)     Full stack: Exporter + Prometheus + Grafana"
      echo "  --exporter    Exporter only (bring your own Prometheus/Grafana)"
      exit 1
      ;;
  esac
done

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

if [ "$MODE" = "exporter" ]; then
  # Exporter-only mode
  COMPOSE_FILE="docker-compose.exporter.yml"

  echo ""
  echo "[1/2] Pulling exporter..."
  docker compose -f "$COMPOSE_FILE" pull

  echo "[2/2] Starting exporter..."
  docker compose -f "$COMPOSE_FILE" up -d

  echo ""
  echo "=== Exporter started ==="
  echo ""
  echo "  Metrics:  http://localhost:9101/metrics"
  echo ""
  echo "Add this to your Prometheus config:"
  echo ""
  echo "  scrape_configs:"
  echo "    - job_name: \"claude-exporter\""
  echo "      static_configs:"
  echo "        - targets: [\"<this-host>:9101\"]"
  echo ""
  echo "To stop:  docker compose -f $(pwd)/$COMPOSE_FILE down"
  echo "To logs:  docker compose -f $(pwd)/$COMPOSE_FILE logs -f"
else
  # Full stack mode
  COMPOSE_FILE="docker-compose.yml"

  # Create data directories
  mkdir -p prometheus-data grafana-data
  chmod 777 prometheus-data grafana-data

  echo ""
  echo "[1/2] Pulling images..."
  docker compose -f "$COMPOSE_FILE" pull

  echo "[2/2] Starting services..."
  docker compose -f "$COMPOSE_FILE" up -d

  echo ""
  echo "=== All services started ==="
  echo ""
  echo "  Grafana:    http://localhost:3000  (admin / admin)"
  echo "  Prometheus: http://localhost:9099"
  echo "  Exporter:   http://localhost:9101/metrics"
  echo ""
  echo "  Dashboard:  http://localhost:3000/d/claude-token-monitor"
  echo ""
  echo "To stop:  docker compose -f $(pwd)/$COMPOSE_FILE down"
  echo "To logs:  docker compose -f $(pwd)/$COMPOSE_FILE logs -f"
fi
