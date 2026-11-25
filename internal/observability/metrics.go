package observability

import (
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Inventory func() (runs, jobs int)

type Metrics struct {
	registry          *prometheus.Registry
	completed         prometheus.Counter
	failed            prometheus.Counter
	retries           prometheus.Counter
	artifacts         prometheus.Counter
	executionDuration *prometheus.HistogramVec
	stepDuration      *prometheus.HistogramVec
	providers         *providerCollector
	mu                sync.RWMutex
	inventory         Inventory
	legacyActive      atomic.Int64
}

func NewMetrics() *Metrics {
	m := &Metrics{
		registry:          prometheus.NewRegistry(),
		completed:         counter("executions_completed_total", "Successful executions observed by this control plane."),
		failed:            counter("executions_failed_total", "Failed executions observed by this control plane."),
		retries:           counter("step_retries_total", "Additional step attempts reserved by this control plane."),
		artifacts:         counter("artifacts_generated_total", "Artifacts published by step attempts."),
		executionDuration: duration("execution_duration_seconds", "Duration of finished executions."),
		stepDuration:      duration("step_duration_seconds", "Duration of finished step attempts."),
		providers:         newProviderCollector(),
	}
	m.registry.MustRegister(m.completed, m.failed, m.retries, m.artifacts, m.executionDuration, m.stepDuration, m.providers,
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Namespace: "kubereel", Name: "active_executions", Help: "Active workflows and VideoRuns."}, func() float64 { runs, _ := m.counts(); return float64(runs) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Namespace: "kubereel", Name: "active_jobs", Help: "Nonterminal Jobs for this release."}, func() float64 { _, jobs := m.counts(); return float64(jobs) }),
	)
	return m
}

func counter(name, help string) prometheus.Counter {
	return prometheus.NewCounter(prometheus.CounterOpts{Namespace: "kubereel", Name: name, Help: help})
}

func duration(name, help string) *prometheus.HistogramVec {
	return prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: "kubereel", Name: name, Help: help, Buckets: []float64{0.1, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 900, 3600}}, []string{"outcome"})
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

func (m *Metrics) SetInventory(inventory Inventory) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inventory = inventory
}

func (m *Metrics) counts() (runs, jobs int) {
	m.mu.RLock()
	inventory := m.inventory
	m.mu.RUnlock()
	if inventory == nil {
		return int(m.legacyActive.Load()), 0
	}
	runs, jobs = inventory()
	return runs + int(m.legacyActive.Load()), jobs
}

func (m *Metrics) SetLegacyActive(count int) { m.legacyActive.Store(int64(count)) }

func (m *Metrics) RunFinished(phase string, elapsed time.Duration) {
	switch phase {
	case "Succeeded":
		m.completed.Inc()
	case "Failed":
		m.failed.Inc()
	case "Cancelled", "RequiresReview":
	default:
		return
	}
	if elapsed >= 0 {
		m.executionDuration.WithLabelValues(phase).Observe(elapsed.Seconds())
	}
}

func (m *Metrics) StepFinished(phase string, elapsed time.Duration) {
	switch phase {
	case "Pending", "Succeeded", "Failed", "Cancelled", "RequiresReview":
		if elapsed >= 0 {
			m.stepDuration.WithLabelValues(phase).Observe(elapsed.Seconds())
		}
	}
}

func (m *Metrics) StepRetried()                 { m.retries.Inc() }
func (m *Metrics) ArtifactsPublished(count int) { m.artifacts.Add(float64(count)) }

func (m *Metrics) RecordProviderSummary(raw string) {
	m.providers.add(decodeProviderSummary(raw))
}

type providerCollector struct {
	mu               sync.Mutex
	samples          map[string]ProviderSample
	duration, errors *prometheus.Desc
}

func newProviderCollector() *providerCollector {
	return &providerCollector{
		samples:  map[string]ProviderSample{"gemini": {}, "fal": {}},
		duration: prometheus.NewDesc("kubereel_provider_request_duration_seconds", "Provider HTTP attempt duration through receipt of headers.", []string{"provider"}, nil),
		errors:   prometheus.NewDesc("kubereel_provider_errors_total", "Provider HTTP attempts with a transport error or non-2xx response.", []string{"provider"}, nil),
	}
}

func (p *providerCollector) add(samples map[string]ProviderSample) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, sample := range samples {
		current := p.samples[key]
		current.Count += sample.Count
		current.Errors += sample.Errors
		current.Seconds += sample.Seconds
		for i, bucket := range sample.Buckets {
			current.Buckets[i] += bucket
		}
		p.samples[key] = current
	}
}

func (p *providerCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- p.duration
	ch <- p.errors
}

func (p *providerCollector) Collect(ch chan<- prometheus.Metric) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for provider, sample := range p.samples {
		buckets := make(map[float64]uint64, len(providerBuckets))
		for i, bound := range providerBuckets {
			buckets[bound] = sample.Buckets[i]
		}
		ch <- prometheus.MustNewConstHistogram(p.duration, sample.Count, sample.Seconds, buckets, provider)
		ch <- prometheus.MustNewConstMetric(p.errors, prometheus.CounterValue, float64(sample.Errors), provider)
	}
}
