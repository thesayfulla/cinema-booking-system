package http

import (
	"log/slog"
	"net/http"

	"github.com/thesayfulla/cinema-booking-system/internal/usecase"
)

// CatalogHandler serves the browsing endpoints.
type CatalogHandler struct {
	catalog *usecase.Catalog
	log     *slog.Logger
}

// NewCatalogHandler builds the catalog handler.
func NewCatalogHandler(catalog *usecase.Catalog, log *slog.Logger) *CatalogHandler {
	return &CatalogHandler{catalog: catalog, log: log}
}

// ListMovies handles GET /api/v1/movies.
func (h *CatalogHandler) ListMovies(w http.ResponseWriter, r *http.Request) {
	movies, err := h.catalog.ListMovies(r.Context())
	if err != nil {
		writeDomainError(w, r, h.log, err)
		return
	}

	out := make([]MovieResponse, 0, len(movies))
	for _, m := range movies {
		out = append(out, toMovieResponse(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"movies": out})
}

// GetMovie handles GET /api/v1/movies/{movieID}, accepting an id or a slug.
func (h *CatalogHandler) GetMovie(w http.ResponseWriter, r *http.Request) {
	movie, err := h.catalog.GetMovie(r.Context(), r.PathValue("movieID"))
	if err != nil {
		writeDomainError(w, r, h.log, err)
		return
	}
	writeJSON(w, http.StatusOK, toMovieResponse(movie))
}

// ListShowtimes handles GET /api/v1/movies/{movieID}/showtimes.
func (h *CatalogHandler) ListShowtimes(w http.ResponseWriter, r *http.Request) {
	h.writeShowtimes(w, r, r.PathValue("movieID"))
}

// ListAllShowtimes handles GET /api/v1/showtimes, optionally filtered by
// ?movie_id=.
func (h *CatalogHandler) ListAllShowtimes(w http.ResponseWriter, r *http.Request) {
	h.writeShowtimes(w, r, r.URL.Query().Get("movie_id"))
}

func (h *CatalogHandler) writeShowtimes(w http.ResponseWriter, r *http.Request, movieIDOrSlug string) {
	showtimes, err := h.catalog.ListShowtimes(r.Context(), movieIDOrSlug)
	if err != nil {
		writeDomainError(w, r, h.log, err)
		return
	}

	out := make([]ShowtimeResponse, 0, len(showtimes))
	for _, s := range showtimes {
		out = append(out, toShowtimeResponse(s))
	}
	writeJSON(w, http.StatusOK, map[string]any{"showtimes": out})
}

// SeatMap handles GET /api/v1/showtimes/{showtimeID}/seats.
//
// The caller's identity is optional here: browsing works anonymously, and when
// a user id is present their own held seats are flagged.
func (h *CatalogHandler) SeatMap(w http.ResponseWriter, r *http.Request) {
	showtime, seats, err := h.catalog.SeatMap(r.Context(), r.PathValue("showtimeID"), UserID(r.Context()))
	if err != nil {
		writeDomainError(w, r, h.log, err)
		return
	}
	writeJSON(w, http.StatusOK, toSeatMapResponse(showtime, seats))
}
