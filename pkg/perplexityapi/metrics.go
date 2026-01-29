// Package perplexityapi provides metrics tracking for the Perplexity API.
package perplexityapi

import (
	"sync"
	"sync/atomic"
	"time"
)

// Metrics tracks API usage and performance metrics.
type Metrics struct {
	// Request counts
	TotalRequests      atomic.Int64
	SuccessfulRequests atomic.Int64
	FailedRequests     atomic.Int64

	// Token usage
	TotalInputTokens  atomic.Int64
	TotalOutputTokens atomic.Int64

	// Error counts by type
	RateLimitErrors      atomic.Int64
	AuthErrors           atomic.Int64
	ServerErrors         atomic.Int64
	InsufficientCredits  atomic.Int64
	OtherErrors          atomic.Int64

	// Local rate limiting metrics (bridge-side rate limiter)
	LocalRateLimitRejects atomic.Int64

	// Circuit breaker metrics (for sidecar client)
	CircuitBreakerRejects atomic.Int64
	CircuitBreakerOpens   atomic.Int64

	// Timing
	totalDuration atomic.Int64 // stored as nanoseconds
	requestCount  atomic.Int64 // for average calculation

	// Per-model tracking
	modelMetrics map[string]*ModelMetrics
	modelMu      sync.RWMutex
}

// ModelMetrics tracks metrics for a specific model.
type ModelMetrics struct {
	Requests      atomic.Int64
	InputTokens   atomic.Int64
	OutputTokens  atomic.Int64
	TotalDuration atomic.Int64 // nanoseconds
}

// NewMetrics creates a new Metrics instance.
func NewMetrics() *Metrics {
	return &Metrics{
		modelMetrics: make(map[string]*ModelMetrics),
	}
}

// RecordRequest records a successful request.
func (m *Metrics) RecordRequest(model string, duration time.Duration, inputTokens, outputTokens int) {
	m.TotalRequests.Add(1)
	m.SuccessfulRequests.Add(1)
	m.TotalInputTokens.Add(int64(inputTokens))
	m.TotalOutputTokens.Add(int64(outputTokens))
	m.totalDuration.Add(int64(duration))
	m.requestCount.Add(1)

	// Record per-model metrics
	mm := m.getOrCreateModelMetrics(model)
	mm.Requests.Add(1)
	mm.InputTokens.Add(int64(inputTokens))
	mm.OutputTokens.Add(int64(outputTokens))
	mm.TotalDuration.Add(int64(duration))
}

// RecordError records an error.
func (m *Metrics) RecordError(err error) {
	m.TotalRequests.Add(1)
	m.FailedRequests.Add(1)

	if IsRateLimitError(err) {
		m.RateLimitErrors.Add(1)
	} else if IsAuthError(err) {
		m.AuthErrors.Add(1)
	} else if IsInsufficientCreditsError(err) {
		m.InsufficientCredits.Add(1)
	} else if IsOverloadedError(err) {
		m.ServerErrors.Add(1)
	} else {
		m.OtherErrors.Add(1)
	}
}

// RecordLocalRateLimitReject records when the local rate limiter rejects a request.
func (m *Metrics) RecordLocalRateLimitReject() {
	m.LocalRateLimitRejects.Add(1)
}

// RecordCircuitBreakerReject records when the circuit breaker rejects a request.
func (m *Metrics) RecordCircuitBreakerReject() {
	m.CircuitBreakerRejects.Add(1)
}

// RecordCircuitBreakerOpen records when the circuit breaker opens due to failures.
func (m *Metrics) RecordCircuitBreakerOpen() {
	m.CircuitBreakerOpens.Add(1)
}

// getOrCreateModelMetrics gets or creates metrics for a model.
func (m *Metrics) getOrCreateModelMetrics(model string) *ModelMetrics {
	m.modelMu.RLock()
	mm, ok := m.modelMetrics[model]
	m.modelMu.RUnlock()

	if ok {
		return mm
	}

	m.modelMu.Lock()
	defer m.modelMu.Unlock()

	// Double-check after acquiring write lock
	if mm, ok = m.modelMetrics[model]; ok {
		return mm
	}

	mm = &ModelMetrics{}
	m.modelMetrics[model] = mm
	return mm
}

// GetAverageRequestDuration returns the average request duration.
func (m *Metrics) GetAverageRequestDuration() time.Duration {
	count := m.requestCount.Load()
	if count == 0 {
		return 0
	}
	return time.Duration(m.totalDuration.Load() / count)
}

// GetTotalTokens returns the total tokens used (input + output).
func (m *Metrics) GetTotalTokens() int64 {
	return m.TotalInputTokens.Load() + m.TotalOutputTokens.Load()
}

// GetErrorRate returns the error rate as a percentage (0-100).
func (m *Metrics) GetErrorRate() float64 {
	total := m.TotalRequests.Load()
	if total == 0 {
		return 0
	}
	return float64(m.FailedRequests.Load()) / float64(total) * 100
}

// GetModelStats returns stats for a specific model.
func (m *Metrics) GetModelStats(model string) (requests, inputTokens, outputTokens int64, avgDuration time.Duration) {
	m.modelMu.RLock()
	mm, ok := m.modelMetrics[model]
	m.modelMu.RUnlock()

	if !ok {
		return 0, 0, 0, 0
	}

	requests = mm.Requests.Load()
	inputTokens = mm.InputTokens.Load()
	outputTokens = mm.OutputTokens.Load()

	if requests > 0 {
		avgDuration = time.Duration(mm.TotalDuration.Load() / requests)
	}

	return
}

// GetAllModelNames returns all model names that have been used.
func (m *Metrics) GetAllModelNames() []string {
	m.modelMu.RLock()
	defer m.modelMu.RUnlock()

	names := make([]string, 0, len(m.modelMetrics))
	for name := range m.modelMetrics {
		names = append(names, name)
	}
	return names
}

// Snapshot returns a snapshot of the current metrics as a map.
func (m *Metrics) Snapshot() map[string]interface{} {
	return map[string]interface{}{
		"total_requests":           m.TotalRequests.Load(),
		"successful_requests":      m.SuccessfulRequests.Load(),
		"failed_requests":          m.FailedRequests.Load(),
		"total_input_tokens":       m.TotalInputTokens.Load(),
		"total_output_tokens":      m.TotalOutputTokens.Load(),
		"total_tokens":             m.GetTotalTokens(),
		"rate_limit_errors":        m.RateLimitErrors.Load(),
		"auth_errors":              m.AuthErrors.Load(),
		"server_errors":            m.ServerErrors.Load(),
		"insufficient_credits":     m.InsufficientCredits.Load(),
		"other_errors":             m.OtherErrors.Load(),
		"local_rate_limit_rejects": m.LocalRateLimitRejects.Load(),
		"circuit_breaker_rejects":  m.CircuitBreakerRejects.Load(),
		"circuit_breaker_opens":    m.CircuitBreakerOpens.Load(),
		"average_duration_ms":      m.GetAverageRequestDuration().Milliseconds(),
		"error_rate_percent":       m.GetErrorRate(),
	}
}

// Reset resets all metrics to zero.
func (m *Metrics) Reset() {
	m.TotalRequests.Store(0)
	m.SuccessfulRequests.Store(0)
	m.FailedRequests.Store(0)
	m.TotalInputTokens.Store(0)
	m.TotalOutputTokens.Store(0)
	m.RateLimitErrors.Store(0)
	m.AuthErrors.Store(0)
	m.ServerErrors.Store(0)
	m.InsufficientCredits.Store(0)
	m.OtherErrors.Store(0)
	m.LocalRateLimitRejects.Store(0)
	m.CircuitBreakerRejects.Store(0)
	m.CircuitBreakerOpens.Store(0)
	m.totalDuration.Store(0)
	m.requestCount.Store(0)

	m.modelMu.Lock()
	m.modelMetrics = make(map[string]*ModelMetrics)
	m.modelMu.Unlock()
}
