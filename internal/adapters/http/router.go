package http

import (
	"net/http"

	"github.com/thesayfulla/cinema-booking-system/internal/domain"
)

// Router sets up all HTTP routes and handlers.
type Router struct {
	mux             *http.ServeMux
	bookingHandler  *BookingHandler
	moviesHandler   *MoviesHandler
}

// NewRouter creates a new HTTP router with all routes registered.
func NewRouter(bookingHandler *BookingHandler, moviesHandler *MoviesHandler) *Router {
	mux := http.NewServeMux()

	router := &Router{
		mux:            mux,
		bookingHandler: bookingHandler,
		moviesHandler:  moviesHandler,
	}

	router.registerRoutes()

	return router
}

// registerRoutes registers all HTTP endpoints.
func (r *Router) registerRoutes() {
	// Movie routes
	r.mux.HandleFunc("GET /movies", r.moviesHandler.ListMovies)

	// Booking routes
	r.mux.HandleFunc("GET /movies/{movieID}/seats", r.bookingHandler.ListSeats)
	r.mux.HandleFunc("POST /movies/{movieID}/seats/{seatID}/hold", r.bookingHandler.HoldSeat)

	// Session routes
	r.mux.HandleFunc("PUT /sessions/{sessionID}/confirm", r.bookingHandler.ConfirmSession)
	r.mux.HandleFunc("DELETE /sessions/{sessionID}", r.bookingHandler.ReleaseSession)

	// Static files
	r.mux.Handle("GET /", http.FileServer(http.Dir("static")))
}

// Handler returns the underlying HTTP handler (mux).
func (r *Router) Handler() http.Handler {
	return r.mux
}

// MoviesHandler handles movie-related HTTP requests.
type MoviesHandler struct{}

// NewMoviesHandler creates a new movies HTTP handler.
func NewMoviesHandler() *MoviesHandler {
	return &MoviesHandler{}
}

// ListMovies handles GET /movies
// Returns all available movies with their seat configuration
func (h *MoviesHandler) ListMovies(w http.ResponseWriter, r *http.Request) {
	movies := domain.Movies

	response := make([]MovieResponse, len(movies))
	for i, movie := range movies {
		response[i] = MovieResponse{
			ID:          movie.ID,
			Title:       movie.Title,
			Rows:        movie.Rows,
			SeatsPerRow: movie.SeatsPerRow,
		}
	}

	writeJSON(w, http.StatusOK, response)
}
