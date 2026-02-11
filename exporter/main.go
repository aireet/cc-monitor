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
	Type    string `json:"type"`
	Subtype string `json:"subtype,omitempty"`

	// For type=assistant or type=progress (nested)
	Message *JSONLMessage `json:"message,omitempty"`
	Data    *JSONLData    `json:"data,omitempty"`

	// For subtype=turn_duration
	DurationMs *float64 `json:"durationMs,omitempty"`

	// For subtype=api_error
	RetryAttempt *int     `json:"retryAttempt,omitempty"`
	MaxRetries   *int     `json:"maxRetries,omitempty"`
	RetryInMs    *float64 `json:"retryInMs,omitempty"`

	// For subtype=compact_boundary
	CompactMetadata *CompactMetadata `json:"compactMetadata,omitempty"`
}

type JSONLData struct {
	Message *JSONLDataMessage `json:"message,omitempty"`
}

type JSONLDataMessage struct {
	Message *JSONLMessage `json:"message,omitempty"`
}

type JSONLMessage struct {
	Model      string         `json:"model"`
	Role       string         `json:"role"`
	StopReason *string        `json:"stop_reason"`
	Content    []ContentBlock `json:"content"`
	Usage      JSONLUsage     `json:"usage"`
}

type ContentBlock struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"` // tool name for tool_use blocks
}

type JSONLUsage struct {
	InputTokens              *float64       `json:"input_tokens"`
	OutputTokens             *float64       `json:"output_tokens"`
	CacheReadInputTokens     *float64       `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *float64       `json:"cache_creation_input_tokens"`
	Cost                     *float64       `json:"cost"`
	CostDetails              *CostDetails   `json:"cost_details"`
	ServerToolUse            *ServerToolUse `json:"server_tool_use"`
	ServiceTier              *string        `json:"service_tier"`
	IsByok                   *bool          `json:"is_byok"`
}

type CostDetails struct {
	UpstreamInferenceCost            *float64 `json:"upstream_inference_cost"`
	UpstreamInferencePromptCost      *float64 `json:"upstream_inference_prompt_cost"`
	UpstreamInferenceCompletionsCost *float64 `json:"upstream_inference_completions_cost"`
}

type ServerToolUse struct {
	WebSearchRequests int `json:"web_search_requests"`
	WebFetchRequests  int `json:"web_fetch_requests"`
}

type CompactMetadata struct {
	Trigger   string `json:"trigger"`
	PreTokens int    `json:"preTokens"`
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

	// New per-request metrics from JSONL
	TotalCost        float64
	PromptCost       float64
	CompletionsCost  float64
	CostByModel      map[string]float64
	TurnDurations    []float64
	ToolUseCounts    map[string]int
	StopReasons      map[string]int
	APIErrors        int
	APIRetries       int
	CompactEvents    int
	CompactPreTokens []float64
	WebSearches      int
	WebFetches       int
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

	// --- NEW: per-request cost ---
	liveCostTotal       prometheus.Gauge
	liveCostPrompt      prometheus.Gauge
	liveCostCompletions prometheus.Gauge
	liveCostByModel     *prometheus.GaugeVec

	// --- NEW: turn duration ---
	turnDuration prometheus.Histogram

	// --- NEW: tool usage breakdown ---
	toolUseTotal *prometheus.GaugeVec

	// --- NEW: stop reason ---
	stopReasonTotal *prometheus.GaugeVec

	// --- NEW: API errors ---
	apiErrorsTotal  prometheus.Gauge
	apiRetriesTotal prometheus.Gauge

	// --- NEW: context compaction ---
	compactEventsTotal    prometheus.Gauge
	compactPreTokensTotal prometheus.Histogram

	// --- NEW: web search / fetch ---
	webSearchTotal prometheus.Gauge
	webFetchTotal  prometheus.Gauge
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

		// --- NEW metrics ---

		liveCostTotal: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "claude_live_cost_usd",
			Help: "Total cost in USD from active sessions",
		}),
		liveCostPrompt: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "claude_live_cost_prompt_usd",
			Help: "Prompt/input cost in USD from active sessions",
		}),
		liveCostCompletions: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "claude_live_cost_completions_usd",
			Help: "Completions/output cost in USD from active sessions",
		}),
		liveCostByModel: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "claude_live_cost_by_model_usd",
			Help: "Cost in USD from active sessions by model",
		}, []string{"model"}),

		turnDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "claude_turn_duration_seconds",
			Help:    "Distribution of assistant turn durations in seconds",
			Buckets: []float64{5, 10, 20, 30, 60, 120, 300, 600, 1800, 3600},
		}),

		toolUseTotal: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "claude_live_tool_use_total",
			Help: "Tool usage count from active sessions by tool name",
		}, []string{"tool"}),

		stopReasonTotal: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "claude_live_stop_reason_total",
			Help: "Stop reason count from active sessions",
		}, []string{"reason"}),

		apiErrorsTotal: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "claude_live_api_errors_total",
			Help: "API error count from active sessions",
		}),
		apiRetriesTotal: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "claude_live_api_retries_total",
			Help: "API retry count from active sessions",
		}),

		compactEventsTotal: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "claude_live_compact_events_total",
			Help: "Context compaction events from active sessions",
		}),
		compactPreTokensTotal: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "claude_compact_pre_tokens",
			Help:    "Distribution of token counts before context compaction",
			Buckets: []float64{50000, 100000, 150000, 200000, 300000, 500000},
		}),

		webSearchTotal: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "claude_live_web_search_total",
			Help: "Web search requests from active sessions",
		}),
		webFetchTotal: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "claude_live_web_fetch_total",
			Help: "Web fetch requests from active sessions",
		}),
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

	c.liveCostTotal.Describe(ch)
	c.liveCostPrompt.Describe(ch)
	c.liveCostCompletions.Describe(ch)
	c.liveCostByModel.Describe(ch)
	c.turnDuration.Describe(ch)
	c.toolUseTotal.Describe(ch)
	c.stopReasonTotal.Describe(ch)
	c.apiErrorsTotal.Describe(ch)
	c.apiRetriesTotal.Describe(ch)
	c.compactEventsTotal.Describe(ch)
	c.compactPreTokensTotal.Describe(ch)
	c.webSearchTotal.Describe(ch)
	c.webFetchTotal.Describe(ch)
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

	c.liveCostTotal.Collect(ch)
	c.liveCostPrompt.Collect(ch)
	c.liveCostCompletions.Collect(ch)
	c.liveCostByModel.Collect(ch)
	c.turnDuration.Collect(ch)
	c.toolUseTotal.Collect(ch)
	c.stopReasonTotal.Collect(ch)
	c.apiErrorsTotal.Collect(ch)
	c.apiRetriesTotal.Collect(ch)
	c.compactEventsTotal.Collect(ch)
	c.compactPreTokensTotal.Collect(ch)
	c.webSearchTotal.Collect(ch)
	c.webFetchTotal.Collect(ch)
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

// extractMessage resolves the message from either direct field or nested data.message.message
func (rec *JSONLRecord) extractMessage() *JSONLMessage {
	if rec.Message != nil {
		return rec.Message
	}
	if rec.Data != nil && rec.Data.Message != nil && rec.Data.Message.Message != nil {
		return rec.Data.Message.Message
	}
	return nil
}

func (c *claudeCollector) scanLiveSessions() *LiveResult {
	result := &LiveResult{
		ModelUsage:    make(map[string]*LiveModelUsage),
		CostByModel:   make(map[string]float64),
		ToolUseCounts: make(map[string]int),
		StopReasons:   make(map[string]int),
	}

	projectsDir := filepath.Join(c.claudeDir, "projects")
	if _, err := os.Stat(projectsDir); err != nil {
		return result
	}

	cacheMtime := c.cacheMtime()

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
			scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
			for scanner.Scan() {
				line := scanner.Bytes()
				if len(line) == 0 {
					continue
				}

				var rec JSONLRecord
				if err := json.Unmarshal(line, &rec); err != nil {
					continue
				}

				// Handle system subtypes
				if rec.Type == "system" {
					switch rec.Subtype {
					case "turn_duration":
						if rec.DurationMs != nil {
							result.TurnDurations = append(result.TurnDurations, *rec.DurationMs)
						}
					case "api_error":
						result.APIErrors++
						if rec.RetryAttempt != nil && *rec.RetryAttempt > 0 {
							result.APIRetries++
						}
					case "compact_boundary":
						if rec.CompactMetadata != nil {
							result.CompactEvents++
							if rec.CompactMetadata.PreTokens > 0 {
								result.CompactPreTokens = append(result.CompactPreTokens, float64(rec.CompactMetadata.PreTokens))
							}
						}
					}
					continue
				}

				// Handle message records (type=assistant or type=progress)
				msg := rec.extractMessage()
				if msg == nil {
					continue
				}

				inp := ptrVal(msg.Usage.InputTokens)
				out := ptrVal(msg.Usage.OutputTokens)

				model := shortModel(msg.Model)
				if model == "" {
					model = "unknown"
				}

				// Token usage
				if inp > 0 || out > 0 {
					mu, ok := result.ModelUsage[model]
					if !ok {
						mu = &LiveModelUsage{}
						result.ModelUsage[model] = mu
					}
					mu.Input += inp
					mu.Output += out
					mu.CacheRead += ptrVal(msg.Usage.CacheReadInputTokens)
					mu.CacheCreate += ptrVal(msg.Usage.CacheCreationInputTokens)
					result.MessageCount++
					sessionHasMessages = true
				}

				// Cost tracking
				if msg.Usage.Cost != nil && *msg.Usage.Cost > 0 {
					result.TotalCost += *msg.Usage.Cost
					result.CostByModel[model] += *msg.Usage.Cost
				}
				if msg.Usage.CostDetails != nil {
					if msg.Usage.CostDetails.UpstreamInferencePromptCost != nil {
						result.PromptCost += *msg.Usage.CostDetails.UpstreamInferencePromptCost
					}
					if msg.Usage.CostDetails.UpstreamInferenceCompletionsCost != nil {
						result.CompletionsCost += *msg.Usage.CostDetails.UpstreamInferenceCompletionsCost
					}
				}

				// Tool usage from content blocks
				for _, block := range msg.Content {
					if block.Type == "tool_use" && block.Name != "" {
						result.ToolUseCounts[block.Name]++
					}
				}

				// Stop reason
				if msg.StopReason != nil && *msg.StopReason != "" {
					result.StopReasons[*msg.StopReason]++
				}

				// Server tool use (web search/fetch)
				if msg.Usage.ServerToolUse != nil {
					result.WebSearches += msg.Usage.ServerToolUse.WebSearchRequests
					result.WebFetches += msg.Usage.ServerToolUse.WebFetchRequests
				}
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
	c.liveCostByModel.Reset()
	c.toolUseTotal.Reset()
	c.stopReasonTotal.Reset()

	stats, err := c.loadStats()
	if err != nil {
		log.Printf("failed to load stats: %v", err)
		return
	}

	today := time.Now().UTC().Format("2006-01-02")

	// Scan live sessions
	live := c.scanLiveSessions()
	log.Printf("live sessions: %d, live messages: %d, api_errors: %d, compactions: %d",
		live.SessionCount, live.MessageCount, live.APIErrors, live.CompactEvents)

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

		// Cost: cache base + live cost per model
		liveCostForModel := live.CostByModel[model]
		c.modelCostUSD.WithLabelValues(model).Set(base.CostUSD + liveCostForModel)

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

	// --- NEW: per-request cost ---
	c.liveCostTotal.Set(live.TotalCost)
	c.liveCostPrompt.Set(live.PromptCost)
	c.liveCostCompletions.Set(live.CompletionsCost)
	for model, cost := range live.CostByModel {
		c.liveCostByModel.WithLabelValues(model).Set(cost)
	}

	// --- NEW: turn duration histogram ---
	for _, durationMs := range live.TurnDurations {
		c.turnDuration.Observe(durationMs / 1000.0) // convert ms to seconds
	}

	// --- NEW: tool usage breakdown ---
	for tool, count := range live.ToolUseCounts {
		c.toolUseTotal.WithLabelValues(tool).Set(float64(count))
	}

	// --- NEW: stop reason ---
	for reason, count := range live.StopReasons {
		c.stopReasonTotal.WithLabelValues(reason).Set(float64(count))
	}

	// --- NEW: API errors ---
	c.apiErrorsTotal.Set(float64(live.APIErrors))
	c.apiRetriesTotal.Set(float64(live.APIRetries))

	// --- NEW: context compaction ---
	c.compactEventsTotal.Set(float64(live.CompactEvents))
	for _, preTokens := range live.CompactPreTokens {
		c.compactPreTokensTotal.Observe(preTokens)
	}

	// --- NEW: web search / fetch ---
	c.webSearchTotal.Set(float64(live.WebSearches))
	c.webFetchTotal.Set(float64(live.WebFetches))

	log.Printf("metrics updated (lastComputedDate=%s, live_sessions=%d, live_cost=$%.4f)",
		stats.LastComputedDate, live.SessionCount, live.TotalCost)
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
