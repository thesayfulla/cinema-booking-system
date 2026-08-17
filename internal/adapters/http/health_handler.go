package http

import (
	"context"
	"net/http"
	"time"
)

// HealthHandler answers the liveness and readiness probes.
type HealthHandler struct {
	check   func(ctx context.Context) error
	version string
	started time.Time
}

// Live handles GET /healthz: the process is up. It touches no dependency, so
// a database outage never causes the orchestrator to restart healthy pods.
func (h *HealthHandler) Live(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"version":        h.version,
		"uptime_seconds": int(time.Since(h.started).Seconds()),
	})
}

// Ready handles GET /readyz: the service can serve traffic. It checks the
// database, so an instance that cannot reach Postgres is taken out of the load
// balancer instead of failing requests.
func (h *HealthHandler) Ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	if h.check != nil {
		if err := h.check(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status":  "unavailable",
				"version": h.version,
				"reason":  "database unreachable",
			})
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "version": h.version})
}
