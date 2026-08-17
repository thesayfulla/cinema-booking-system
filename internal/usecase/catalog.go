package usecase

import (
	"context"
	"time"

	"github.com/thesayfulla/cinema-booking-system/internal/domain"
)

// Catalog serves the browsing side of the system: what is playing, when, and
// which seats are free.
type Catalog struct {
	repo domain.CatalogRepository
}

// NewCatalog builds the catalog use case.
func NewCatalog(repo domain.CatalogRepository) *Catalog {
	return &Catalog{repo: repo}
}

func (c *Catalog) ListMovies(ctx context.Context) ([]domain.Movie, error) {
	return c.repo.ListMovies(ctx)
}

func (c *Catalog) GetMovie(ctx context.Context, idOrSlug string) (domain.Movie, error) {
	if idOrSlug == "" {
		return domain.Movie{}, domain.Invalid("movie", "is required")
	}
	return c.repo.GetMovie(ctx, idOrSlug)
}

// ListShowtimes returns upcoming screenings. Past screenings are never listed:
// they cannot be booked, so showing them would only produce dead ends.
func (c *Catalog) ListShowtimes(ctx context.Context, movieIDOrSlug string) ([]domain.Showtime, error) {
	movieID := ""
	if movieIDOrSlug != "" {
		movie, err := c.repo.GetMovie(ctx, movieIDOrSlug)
		if err != nil {
			return nil, err
		}
		movieID = movie.ID
	}
	return c.repo.ListShowtimes(ctx, movieID, time.Now())
}

// SeatMap returns the seat layout of a showtime with each seat's status.
// userID may be empty; when set, the caller's own holds are marked.
func (c *Catalog) SeatMap(ctx context.Context, showtimeID, userID string) (domain.Showtime, []domain.SeatAvailability, error) {
	showtime, err := c.repo.GetShowtime(ctx, showtimeID)
	if err != nil {
		return domain.Showtime{}, nil, err
	}

	seats, err := c.repo.SeatMap(ctx, showtimeID, userID)
	if err != nil {
		return domain.Showtime{}, nil, err
	}
	return showtime, seats, nil
}
