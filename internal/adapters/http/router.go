package http

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/thesayfulla/cinema-booking-system/internal/config"
	"github.com/thesayfulla/cinema-booking-system/internal/metrics"
)

// RouterDeps is everything the router needs to serve the API.
type RouterDeps struct {
	Config  *config.Config
	Log     *slog.Logger
	Metrics *metrics.Collector
	Catalog *CatalogHandler
	Booking *BookingHandler
	Payment *PaymentHandler
	// Health reports whether dependencies are reachable, for readiness probes.
	Health func(ctx context.Context) error
	// Version is reported by the health endpoints.
	Version string
}

// NewRouter builds the full HTTP handler: routes plus the middleware chain.
//
// Route patterns use Go 1.22 method-and-wildcard matching, so the mux itself
// enforces method routing and 405s.
func NewRouter(d RouterDeps) http.Handler {
	mux := http.NewServeMux()

	// Public catalog: browsable without identifying yourself.
	mux.HandleFunc("GET /api/v1/movies", d.Catalog.ListMovies)
	mux.HandleFunc("GET /api/v1/movies/{movieID}", d.Catalog.GetMovie)
	mux.HandleFunc("GET /api/v1/movies/{movieID}/showtimes", d.Catalog.ListShowtimes)
	mux.HandleFunc("GET /api/v1/showtimes", d.Catalog.ListAllShowtimes)
	mux.HandleFunc("GET /api/v1/showtimes/{showtimeID}/seats", d.Catalog.SeatMap)

	// Bookings and checkout: the caller must identify itself.
	authed := RequireUser()
	mux.Handle("POST /api/v1/bookings", authed(http.HandlerFunc(d.Booking.Create)))
	mux.Handle("GET /api/v1/bookings", authed(http.HandlerFunc(d.Booking.List)))
	mux.Handle("GET /api/v1/bookings/{bookingID}", authed(http.HandlerFunc(d.Booking.Get)))
	mux.Handle("DELETE /api/v1/bookings/{bookingID}", authed(http.HandlerFunc(d.Booking.Cancel)))
	mux.Handle("POST /api/v1/bookings/{bookingID}/checkout", authed(http.HandlerFunc(d.Payment.StartCheckout)))
	mux.Handle("GET /api/v1/payments/{paymentID}", authed(http.HandlerFunc(d.Payment.GetPayment)))

	// The provider calls this one; it authenticates by signature, not by header.
	mux.HandleFunc("POST /api/v1/payments/webhook", d.Payment.Webhook)

	if d.Config.Pay.EnableTestEndpoints {
		d.Log.Warn("payment test endpoints are enabled; do not run this way in production")
		mux.Handle("POST /api/v1/payments/{paymentID}/simulate",
			authed(http.HandlerFunc(d.Payment.SimulateProviderCallback)))
	}

	// Operations.
	health := &HealthHandler{check: d.Health, version: d.Version, started: time.Now()}
	mux.HandleFunc("GET /healthz", health.Live)
	mux.HandleFunc("GET /readyz", health.Ready)
	mux.Handle("GET /metrics", promhttp.HandlerFor(d.Metrics.Gatherer(), promhttp.HandlerOpts{}))

	// The demo UI. Served last so it only catches paths no API route claimed.
	if d.Config.HTTP.StaticDir != "" {
		mux.Handle("GET /", http.FileServer(http.Dir(d.Config.HTTP.StaticDir)))
	}

	// The mux resolves a request to the route template that will serve it, which
	// is what the metrics layer labels by.
	routeOf := func(r *http.Request) string {
		_, pattern := mux.Handler(r)
		return pattern
	}

	// Outermost first: a panic in any layer below is still caught, and every
	// request gets an id before anything logs.
	return Chain(mux,
		RequestID(),
		Recover(d.Log),
		AccessLog(d.Log),
		Metrics(d.Metrics, routeOf),
		SecurityHeaders(),
		CORS(d.Config.HTTP.CORSOrigins),
		RateLimit(d.Config.HTTP.RateLimitRPS, d.Config.HTTP.RateLimitBurst),
		MaxBody(d.Config.HTTP.MaxBodyBytes),
		Timeout(d.Config.HTTP.RequestTimeout),
		Identify(),
	)
}
