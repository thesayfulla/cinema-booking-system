// Package metrics holds the service's Prometheus instrumentation. It sits in
// its own package so both the HTTP layer and background workers can record to
// the same registry.
package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Collector holds the service's Prometheus metrics. Business counters live
// next to the HTTP ones so an operator can alert on "seat conflicts spiking"
// as easily as on "5xx rate".
type Collector struct {
	requests    *prometheus.CounterVec
	duration    *prometheus.HistogramVec
	bookings    *prometheus.CounterVec
	payments    *prometheus.CounterVec
	seatConfl   prometheus.Counter
	holdsSwept  prometheus.Counter
	gathererRef prometheus.Gatherer
}

// New registers the metrics on a dedicated registry, which keeps the exported
// set explicit instead of whatever happens to be in the default one.
func New() *Collector {
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	m := &Collector{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total HTTP requests by method, route and status.",
		}, []string{"method", "route", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency in seconds.",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		}, []string{"method", "route"}),
		bookings: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "bookings_total",
			Help: "Booking outcomes by result.",
		}, []string{"result"}),
		payments: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "payments_total",
			Help: "Payment outcomes by status.",
		}, []string{"status"}),
		seatConfl: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "seat_conflicts_total",
			Help: "Hold attempts rejected because a seat was already claimed.",
		}),
		holdsSwept: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "expired_holds_released_total",
			Help: "Seat claims released by the hold expiry sweeper.",
		}),
		gathererRef: registry,
	}

	registry.MustRegister(m.requests, m.duration, m.bookings, m.payments, m.seatConfl, m.holdsSwept)
	return m
}

// Gatherer exposes the registry for the /metrics handler.
func (m *Collector) Gatherer() prometheus.Gatherer { return m.gathererRef }

// ObserveRequest records one HTTP request.
func (m *Collector) ObserveRequest(method, route, status string, d time.Duration) {
	m.requests.WithLabelValues(method, route, status).Inc()
	m.duration.WithLabelValues(method, route).Observe(d.Seconds())
}

// BookingResult records the outcome of a hold attempt.
func (m *Collector) BookingResult(result string) {
	m.bookings.WithLabelValues(result).Inc()
}

// SeatConflict records a lost race for a seat.
func (m *Collector) SeatConflict() { m.seatConfl.Inc() }

// PaymentStatus records a payment reaching a status.
func (m *Collector) PaymentStatus(status string) {
	m.payments.WithLabelValues(status).Inc()
}

// HoldsReleased records seats freed by the sweeper.
func (m *Collector) HoldsReleased(n int) { m.holdsSwept.Add(float64(n)) }
