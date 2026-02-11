package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// --- config ---

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil {
			return n
		}
	}
	return fallback
}

// --- stats-cache.json structs ---

type StatsCache struct {
	ModelUsage       map[string]ModelUsage `json:"modelUsage"`
	TotalSessions    int                   `json:"totalSessions"`
	TotalMessages    int                   `json:"totalMessages"`
	DailyActivity    []DailyActivity       `json:"dailyActivity"`
	DailyModelTokens []DailyModelTokens    `json:"dailyModelTokens"`
	HourCounts       map[string]float64    `json:"hourCounts"`
	LastComputedDate string                `json:"lastComputedDate"`
	FirstSessionDate string                `json:"firstSessionDate"`
}

type ModelUsage struct {
	InputTokens              float64 `json:"inputTokens"`
	OutputTokens             float64 `json:"outputTokens"`
	CacheReadInputTokens     float64 `json:"cacheReadInputTokens"`
	CacheCreationInputTokens float64 `json:"cacheCreationInputTokens"`
	CostUSD                  float64 `json:"costUSD"`
}

type DailyActivity struct {
	Date          string `json:"date"`
	MessageCount  int    `json:"messageCount"`
	SessionCount  int    `json:"sessionCount"`
	ToolCallCount int    `json:"toolCallCount"`
}

type DailyModelTokens struct {
	Date          string             `json:"date"`
	TokensByModel map[string]float64 `json:"tokensByModel"`
}

// --- JSONL record structs ---

type JSONLRecord struct {
	Message *JSONLMessage `json:"message,omitempty"`
}

type JSONLMessage struct {
	Model string     `json:"model"`
	Usage JSONLUsage `json:"usage"`
}

type JSONLUsage struct {
	InputTokens              *float64 `json:"input_tokens"`
	OutputTokens             *float64 `json:"output_tokens"`
	CacheReadInputTokens     *float64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *float64 `json:"cache_creation_input_tokens"`
}

// --- live session aggregation ---

type LiveModelUsage struct {
	Input       float64
	Output      float64
	CacheRead   float64
	CacheCreate float64
}

type LiveResult struct {
	ModelUsage   map[string]*LiveModelUsage
	SessionCount int
	MessageCount int
}

// --- helper ---

func shortModel(name string) string {
	return strings.ReplaceAll(name, "anthropic/", "")
}

func ptrVal(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// --- collector ---

type claudeCollector struct {
	statsFile string
	claudeDir string

	// cumulative (cache + live)
	modelInputTokens       *prometheus.GaugeVec
	modelOutputTokens      *prometheus.GaugeVec
	modelCacheReadTokens   *prometheus.GaugeVec
	modelCacheCreateTokens *prometheus.GaugeVec
	modelCostUSD           *prometheus.GaugeVec

	// live only
	liveInputTokens  *prometheus.GaugeVec
	liveOutputTokens *prometheus.GaugeVec
	liveSessions     prometheus.Gauge
	liveMessages     prometheus.Gauge

	// totals
	totalSessions prometheus.Gauge
	totalMessages prometheus.Gauge

	// today
	todayMessages  prometheus.Gauge
	todaySessions  prometheus.Gauge
	todayToolCalls prometheus.Gauge
	todayTokens    *prometheus.GaugeVec

	// daily (last 30 days)
	dailyMessages  *prometheus.GaugeVec
	dailySessions  *prometheus.GaugeVec
	dailyToolCalls *prometheus.GaugeVec
	dailyTokens    *prometheus.GaugeVec

	// hour distribution
	hourActivity *prometheus.GaugeVec

	// info
	exporterInfo *prometheus.GaugeVec
}

func newCollector(statsFile, claudeDir string) *claudeCollector {
	return &claudeCollector{
		statsFile: statsFile,
		claudeDir: claudeDir,

		modelInputTokens: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "claude_model_input_tokens_total",
			Help: "Total input tokens by model",
		}, []string{"model"}),
		modelOutputTokens: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "claude_model_output_tokens_total",
			Help: "Total output tokens by model",
		}, []string{"model"}),
		modelCacheReadTokens: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "claude_model_cache_read_tokens_total",
			Help: "Total cache-read input tokens by model",
		}, []string{"model"}),
		modelCacheCreateTokens: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "claude_model_cache_creation_tokens_total",
			Help: "Total cache-creation input tokens by model",
		}, []string{"model"}),
		modelCostUSD: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "claude_model_cost_usd",
			Help: "Total cost in USD by model",
		}, []string{"model"}),

		liveInputTokens: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "claude_live_input_tokens",
			Help: "Input tokens from active sessions (not yet in cache)",
		}, []string{"model"}),
		liveOutputTokens: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "claude_live_output_tokens",
			Help: "Output tokens from active sessions (not yet in cache)",
		}, []string{"model"}),
		liveSessions: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "claude_live_sessions",
			Help: "Number of active sessions (not yet in cache)",
		}),
		liveMessages: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "claude_live_messages",
			Help: "Messages in active sessions (not yet in cache)",
		}),

		totalSessions: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "claude_sessions_total",
			Help: "Total number of sessions",
		}),
		totalMessages: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "claude_messages_total",
			Help: "Total number of messages",
		}),

		todayMessages: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "claude_today_messages",
			Help: "Messages sent today",
		}),
		todaySessions: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "claude_today_sessions",
			Help: "Sessions started today",
		}),
		todayToolCalls: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "claude_today_tool_calls",
			Help: "Tool calls today",
		}),
		todayTokens: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "claude_today_tokens",
			Help: "Tokens used today by model",
		}, []string{"model"}),

		dailyMessages: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "claude_daily_messages",
			Help: "Daily message count",
		}, []string{"date"}),
		dailySessions: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "claude_daily_sessions",
			Help: "Daily session count",
		}, []string{"date"}),
		dailyToolCalls: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "claude_daily_tool_calls",
			Help: "Daily tool call count",
		}, []string{"date"}),
		dailyTokens: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "claude_daily_tokens",
			Help: "Daily tokens by model",
		}, []string{"date", "model"}),

		hourActivity: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "claude_hour_sessions",
			Help: "Session count by hour of day",
		}, []string{"hour"}),

		exporterInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "claude_exporter_info",
			Help: "Claude Code exporter metadata",
		}, []string{"stats_file", "claude_dir", "last_computed_date", "first_session_date", "live_sessions"}),
	}
}

func (c *claudeCollector) Describe(ch chan<- *prometheus.Desc) {
	c.modelInputTokens.Describe(ch)
	c.modelOutputTokens.Describe(ch)
	c.modelCacheReadTokens.Describe(ch)
	c.modelCacheCreateTokens.Describe(ch)
	c.modelCostUSD.Describe(ch)
	c.liveInputTokens.Describe(ch)
	c.liveOutputTokens.Describe(ch)
	c.liveSessions.Describe(ch)
	c.liveMessages.Describe(ch)
	c.totalSessions.Describe(ch)
	c.totalMessages.Describe(ch)
	c.todayMessages.Describe(ch)
	c.todaySessions.Describe(ch)
	c.todayToolCalls.Describe(ch)
	c.todayTokens.Describe(ch)
	c.dailyMessages.Describe(ch)
	c.dailySessions.Describe(ch)
	c.dailyToolCalls.Describe(ch)
	c.dailyTokens.Describe(ch)
	c.hourActivity.Describe(ch)
	c.exporterInfo.Describe(ch)
}

func (c *claudeCollector) Collect(ch chan<- prometheus.Metric) {
	c.update()

	c.modelInputTokens.Collect(ch)
	c.modelOutputTokens.Collect(ch)
	c.modelCacheReadTokens.Collect(ch)
	c.modelCacheCreateTokens.Collect(ch)
	c.modelCostUSD.Collect(ch)
	c.liveInputTokens.Collect(ch)
	c.liveOutputTokens.Collect(ch)
	c.liveSessions.Collect(ch)
	c.liveMessages.Collect(ch)
	c.totalSessions.Collect(ch)
	c.totalMessages.Collect(ch)
	c.todayMessages.Collect(ch)
	c.todaySessions.Collect(ch)
	c.todayToolCalls.Collect(ch)
	c.todayTokens.Collect(ch)
	c.dailyMessages.Collect(ch)
	c.dailySessions.Collect(ch)
	c.dailyToolCalls.Collect(ch)
	c.dailyTokens.Collect(ch)
	c.hourActivity.Collect(ch)
	c.exporterInfo.Collect(ch)
}

func (c *claudeCollector) loadStats() (*StatsCache, error) {
	data, err := os.ReadFile(c.statsFile)
	if err != nil {
		return nil, err
	}
	var stats StatsCache
	if err := json.Unmarshal(data, &stats); err != nil {
		return nil, err
	}
	return &stats, nil
}

func (c *claudeCollector) cacheMtime() time.Time {
	info, err := os.Stat(c.statsFile)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func (c *claudeCollector) scanLiveSessions() *LiveResult {
	result := &LiveResult{
		ModelUsage: make(map[string]*LiveModelUsage),
	}

	projectsDir := filepath.Join(c.claudeDir, "projects")
	if _, err := os.Stat(projectsDir); err != nil {
		return result
	}

	cacheMtime := c.cacheMtime()

	// Glob: projects/*/?.jsonl (top-level session files only)
	pattern := filepath.Join(projectsDir, "*", "*.jsonl")
	files, err := filepath.Glob(pattern)
	if err != nil {
		log.Printf("glob error: %v", err)
		return result
	}

	for _, fpath := range files {
		info, err := os.Stat(fpath)
		if err != nil {
			continue
		}
		// Only files modified after cache
		if !info.ModTime().After(cacheMtime) {
			continue
		}

		sessionHasMessages := false
		func() {
			f, err := os.Open(fpath)
			if err != nil {
				return
			}
			defer f.Close()

			scanner := bufio.NewScanner(f)
			scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024) // up to 10MB per line
			for scanner.Scan() {
				line := scanner.Bytes()
				if len(line) == 0 {
					continue
				}

				var rec JSONLRecord
				if err := json.Unmarshal(line, &rec); err != nil {
					continue
				}
				if rec.Message == nil {
					continue
				}

				inp := ptrVal(rec.Message.Usage.InputTokens)
				out := ptrVal(rec.Message.Usage.OutputTokens)
				if inp == 0 && out == 0 {
					continue
				}

				model := shortModel(rec.Message.Model)
				if model == "" {
					model = "unknown"
				}

				mu, ok := result.ModelUsage[model]
				if !ok {
					mu = &LiveModelUsage{}
					result.ModelUsage[model] = mu
				}
				mu.Input += inp
				mu.Output += out
				mu.CacheRead += ptrVal(rec.Message.Usage.CacheReadInputTokens)
				mu.CacheCreate += ptrVal(rec.Message.Usage.CacheCreationInputTokens)
				result.MessageCount++
				sessionHasMessages = true
			}
		}()

		if sessionHasMessages {
			result.SessionCount++
		}
	}

	return result
}

func (c *claudeCollector) update() {
	// Reset vector metrics to avoid stale labels
	c.modelInputTokens.Reset()
	c.modelOutputTokens.Reset()
	c.modelCacheReadTokens.Reset()
	c.modelCacheCreateTokens.Reset()
	c.modelCostUSD.Reset()
	c.liveInputTokens.Reset()
	c.liveOutputTokens.Reset()
	c.todayTokens.Reset()
	c.dailyMessages.Reset()
	c.dailySessions.Reset()
	c.dailyToolCalls.Reset()
	c.dailyTokens.Reset()
	c.hourActivity.Reset()
	c.exporterInfo.Reset()

	stats, err := c.loadStats()
	if err != nil {
		log.Printf("failed to load stats: %v", err)
		return
	}

	today := time.Now().UTC().Format("2006-01-02")

	// Scan live sessions
	live := c.scanLiveSessions()
	log.Printf("live sessions: %d, live messages: %d", live.SessionCount, live.MessageCount)

	// Collect all models
	allModels := make(map[string]struct{})
	for m := range stats.ModelUsage {
		allModels[shortModel(m)] = struct{}{}
	}
	for m := range live.ModelUsage {
		allModels[m] = struct{}{}
	}

	// Model usage: cache + live
	for model := range allModels {
		// Find base from cache
		var base ModelUsage
		for raw, u := range stats.ModelUsage {
			if shortModel(raw) == model {
				base = u
				break
			}
		}

		lm := live.ModelUsage[model]
		var liveIn, liveOut, liveCR, liveCC float64
		if lm != nil {
			liveIn = lm.Input
			liveOut = lm.Output
			liveCR = lm.CacheRead
			liveCC = lm.CacheCreate
		}

		c.modelInputTokens.WithLabelValues(model).Set(base.InputTokens + liveIn)
		c.modelOutputTokens.WithLabelValues(model).Set(base.OutputTokens + liveOut)
		c.modelCacheReadTokens.WithLabelValues(model).Set(base.CacheReadInputTokens + liveCR)
		c.modelCacheCreateTokens.WithLabelValues(model).Set(base.CacheCreationInputTokens + liveCC)
		c.modelCostUSD.WithLabelValues(model).Set(base.CostUSD)

		if liveIn > 0 || liveOut > 0 {
			c.liveInputTokens.WithLabelValues(model).Set(liveIn)
			c.liveOutputTokens.WithLabelValues(model).Set(liveOut)
		}
	}

	c.liveSessions.Set(float64(live.SessionCount))
	c.liveMessages.Set(float64(live.MessageCount))

	// Totals
	c.totalSessions.Set(float64(stats.TotalSessions + live.SessionCount))
	c.totalMessages.Set(float64(stats.TotalMessages + live.MessageCount))

	// Daily activity (last 30)
	start := 0
	if len(stats.DailyActivity) > 30 {
		start = len(stats.DailyActivity) - 30
	}
	for _, entry := range stats.DailyActivity[start:] {
		c.dailyMessages.WithLabelValues(entry.Date).Set(float64(entry.MessageCount))
		c.dailySessions.WithLabelValues(entry.Date).Set(float64(entry.SessionCount))
		c.dailyToolCalls.WithLabelValues(entry.Date).Set(float64(entry.ToolCallCount))
	}

	// Today
	var todayEntry *DailyActivity
	for i := range stats.DailyActivity {
		if stats.DailyActivity[i].Date == today {
			todayEntry = &stats.DailyActivity[i]
			break
		}
	}
	if todayEntry != nil {
		c.todayMessages.Set(float64(todayEntry.MessageCount + live.MessageCount))
		c.todaySessions.Set(float64(todayEntry.SessionCount + live.SessionCount))
		c.todayToolCalls.Set(float64(todayEntry.ToolCallCount))
	} else {
		c.todayMessages.Set(float64(live.MessageCount))
		c.todaySessions.Set(float64(live.SessionCount))
		c.todayToolCalls.Set(0)
	}

	// Daily model tokens (last 30)
	start = 0
	if len(stats.DailyModelTokens) > 30 {
		start = len(stats.DailyModelTokens) - 30
	}
	for _, entry := range stats.DailyModelTokens[start:] {
		for rawModel, tokens := range entry.TokensByModel {
			model := shortModel(rawModel)
			c.dailyTokens.WithLabelValues(entry.Date, model).Set(tokens)
		}
	}

	// Today tokens
	var todayTokenEntry *DailyModelTokens
	for i := range stats.DailyModelTokens {
		if stats.DailyModelTokens[i].Date == today {
			todayTokenEntry = &stats.DailyModelTokens[i]
			break
		}
	}
	if todayTokenEntry != nil {
		for rawModel, tokens := range todayTokenEntry.TokensByModel {
			model := shortModel(rawModel)
			liveTok := float64(0)
			if lm, ok := live.ModelUsage[model]; ok {
				liveTok = lm.Input
			}
			c.dailyTokens.WithLabelValues(today, model).Set(tokens + liveTok)
			c.todayTokens.WithLabelValues(model).Set(tokens + liveTok)
		}
	} else {
		for model, mu := range live.ModelUsage {
			c.dailyTokens.WithLabelValues(today, model).Set(mu.Input)
			c.todayTokens.WithLabelValues(model).Set(mu.Input)
		}
	}

	// Hour distribution
	for hour, count := range stats.HourCounts {
		h := hour
		if len(h) == 1 {
			h = "0" + h
		}
		c.hourActivity.WithLabelValues(h).Set(count)
	}

	// Info
	c.exporterInfo.WithLabelValues(
		c.statsFile,
		c.claudeDir,
		stats.LastComputedDate,
		stats.FirstSessionDate,
		strconv.Itoa(live.SessionCount),
	).Set(1)

	log.Printf("metrics updated (lastComputedDate=%s, live_sessions=%d)", stats.LastComputedDate, live.SessionCount)
}

func main() {
	statsFile := envOr("CLAUDE_STATS_FILE", "/data/claude/stats-cache.json")
	claudeDir := envOr("CLAUDE_DIR", "/data/claude")
	port := envInt("EXPORTER_PORT", 9101)

	log.Printf("Starting Claude Code exporter on :%d", port)
	log.Printf("Stats file: %s", statsFile)
	log.Printf("Claude dir: %s", claudeDir)

	collector := newCollector(statsFile, claudeDir)

	reg := prometheus.NewRegistry()
	reg.MustRegister(collector)

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><body><h1>Claude Code Exporter</h1><p><a href="/metrics">Metrics</a></p></body></html>`))
	})

	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), mux))
}
