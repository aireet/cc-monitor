# CC Monitor

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

[English](README.md)

开箱即用的 [Claude Code](https://docs.anthropic.com/en/docs/claude-code) 用量监控面板。

自动采集 Token 消耗、会话数、工具调用等数据，通过 Grafana 可视化展示。支持实时监控活跃会话。

## 截图

![总览](screenshots/cc-grafana-001.jpg)
![趋势](screenshots/cc-grafana-002.jpg)
![请求明细](screenshots/cc-grafana-003.jpg)

## 安装

### 方案一：全套部署（推荐）

一键部署 Exporter + Prometheus + Grafana，含预配置 Dashboard。

```bash
git clone https://github.com/aireet/cc-monitor.git
cd cc-monitor
./start.sh
```

打开浏览器访问 **http://localhost:3000/d/claude-token-monitor**，用户名密码均为 `admin`。

| 服务 | 地址 |
|------|------|
| Grafana | http://localhost:3000 |
| Prometheus | http://localhost:9099 |
| Exporter | http://localhost:9101/metrics |

### 方案二：仅 Exporter

适合已有 Prometheus 和 Grafana 的用户，只部署指标采集器。

```bash
git clone https://github.com/aireet/cc-monitor.git
cd cc-monitor
./start.sh --exporter
```

验证是否正常运行：

```bash
curl http://localhost:9101/metrics
```

#### 配置 Prometheus 采集

在你的 Prometheus 配置中添加：

```yaml
scrape_configs:
  - job_name: "claude-exporter"
    static_configs:
      - targets: ["<exporter-host>:9101"]
```

#### 导入 Grafana Dashboard

1. 在 Grafana 中进入 **Dashboards > Import**
2. 上传本仓库中的 `grafana/dashboards/claude-tokens.json` 文件
3. 选择你的 Prometheus 数据源
4. 点击 **Import**

## 架构

```
~/.claude (只读)
    |
    +-- stats-cache.json ----> +----------------+     +--------------+     +-----------+
    +-- projects/*/?.jsonl --> |    Exporter    +---->|  Prometheus  +---->|  Grafana  |
                               |  (Go, :9101)  |     |   (:9099)    |     |  (:3000)  |
                               +----------------+     +--------------+     +-----------+
```

- **Exporter** -- 读取 `stats-cache.json`（历史数据）+ 扫描活跃会话 JSONL 文件（实时数据），暴露 Prometheus 指标
- **Prometheus** -- 每 30s 采集，数据保留 90 天
- **Grafana** -- 预配置数据源和 Dashboard，开箱即用

## 监控指标

### Token 用量

| 指标 | 类型 | 标签 | 说明 |
|------|------|------|------|
| `claude_model_input_tokens` | Gauge | model | 各模型输入 Token |
| `claude_model_output_tokens` | Gauge | model | 各模型输出 Token |
| `claude_model_cache_read_tokens` | Gauge | model | 各模型缓存读取 Token |
| `claude_model_cache_create_tokens` | Gauge | model | 各模型缓存创建 Token |

### 实时会话

| 指标 | 类型 | 标签 | 说明 |
|------|------|------|------|
| `claude_live_input_tokens` | Gauge | model | 活跃会话输入 Token |
| `claude_live_output_tokens` | Gauge | model | 活跃会话输出 Token |
| `claude_live_sessions` | Gauge | -- | 活跃会话数 |
| `claude_live_messages` | Gauge | -- | 活跃会话消息数 |

### 汇总

| 指标 | 类型 | 标签 | 说明 |
|------|------|------|------|
| `claude_total_sessions` | Gauge | -- | 总会话数（历史） |
| `claude_total_messages` | Gauge | -- | 总消息数（历史） |
| `claude_today_messages` | Gauge | -- | 今日消息数 |
| `claude_today_sessions` | Gauge | -- | 今日会话数 |
| `claude_today_tool_calls` | Gauge | -- | 今日工具调用数 |
| `claude_today_tokens` | Gauge | type | 今日 Token（input/output） |

### 趋势

| 指标 | 类型 | 标签 | 说明 |
|------|------|------|------|
| `claude_daily_messages` | Gauge | date | 每日消息数 |
| `claude_daily_sessions` | Gauge | date | 每日会话数 |
| `claude_daily_tool_calls` | Gauge | date | 每日工具调用数 |
| `claude_daily_tokens` | Gauge | date, type | 每日 Token 用量 |
| `claude_hour_activity` | Gauge | hour, type | 按小时活跃度分布 |

### 工具与错误

| 指标 | 类型 | 标签 | 说明 |
|------|------|------|------|
| `claude_tool_use_total` | Gauge | tool | 各工具使用次数 |
| `claude_stop_reason_total` | Gauge | reason | 停止原因统计 |
| `claude_api_errors_total` | Gauge | -- | API 错误总数 |
| `claude_api_retries_total` | Gauge | -- | API 重试总数 |
| `claude_compact_events_total` | Gauge | -- | 上下文压缩事件数 |
| `claude_web_search_total` | Gauge | -- | Web 搜索请求数 |
| `claude_web_fetch_total` | Gauge | -- | Web 抓取请求数 |

## 停止 / 重启

```bash
# 停止（全套）
docker compose down

# 停止（仅 exporter）
docker compose -f docker-compose.exporter.yml down

# 重启
./start.sh            # 或 ./start.sh --exporter
```

## 自定义

### Claude 数据路径

默认读取 `~/.claude`。如果路径不同：

```bash
export CLAUDE_HOME=/path/to/.claude
./start.sh
```

### 端口

修改对应 `docker-compose*.yml` 中的端口映射：

| 默认端口 | 服务 |
|----------|------|
| 3000 | Grafana（仅全套模式） |
| 9099 | Prometheus（仅全套模式） |
| 9101 | Exporter |

## 数据安全

- 所有 Claude 数据以**只读**方式挂载
- 所有数据存储在本地，不会上传到任何外部服务

## License

MIT
