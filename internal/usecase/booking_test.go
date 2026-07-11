package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/thesayfulla/cinema-booking-system/internal/domain"
)

// MockRepository is a test double for domain.BookingRepository
type MockRepository struct {
	holdFunc           func(context.Context, domain.Booking) (domain.Booking, error)
	listByMovieFunc    func(context.Context, string) ([]domain.Booking, error)
	confirmFunc        func(context.Context, string, string) (domain.Booking, error)
	releaseFunc        func(context.Context, string, string) error
}

func (m *MockRepository) Hold(ctx context.Context, booking domain.Booking) (domain.Booking, error) {
	return m.holdFunc(ctx, booking)
}

func (m *MockRepository) ListByMovie(ctx context.Context, movieID string) ([]domain.Booking, error) {
	return m.listByMovieFunc(ctx, movieID)
}

func (m *MockRepository) Confirm(ctx context.Context, sessionID string, userID string) (domain.Booking, error) {
	return m.confirmFunc(ctx, sessionID, userID)
}

func (m *MockRepository) Release(ctx context.Context, sessionID string, userID string) error {
	return m.releaseFunc(ctx, sessionID, userID)
}

func TestHoldSeat_Success(t *testing.T) {
	mockRepo := &MockRepository{
		holdFunc: func(ctx context.Context, booking domain.Booking) (domain.Booking, error) {
			booking.ID = "session-123"
			booking.Status = "held"
			booking.ExpiresAt = time.Now().Add(2 * time.Minute)
			return booking, nil
		},
	}

	uc := NewBookingUsecase(mockRepo)
	booking, err := uc.HoldSeat(context.Background(), "inception", "A1", "user123")

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if booking.ID == "" {
		t.Error("expected session ID to be set")
	}
	if booking.Status != "held" {
		t.Errorf("expected status 'held', got %s", booking.Status)
	}
}

func TestHoldSeat_SeatAlreadyBooked(t *testing.T) {
	mockRepo := &MockRepository{
		holdFunc: func(ctx context.Context, booking domain.Booking) (domain.Booking, error) {
			return domain.Booking{}, domain.ErrSeatAlreadyBooked
		},
	}

	uc := NewBookingUsecase(mockRepo)
	_, err := uc.HoldSeat(context.Background(), "inception", "A1", "user123")

	if !errors.Is(err, domain.ErrSeatAlreadyBooked) {
		t.Errorf("expected ErrSeatAlreadyBooked, got %v", err)
	}
}

func TestConfirmSession_Success(t *testing.T) {
	mockRepo := &MockRepository{
		confirmFunc: func(ctx context.Context, sessionID string, userID string) (domain.Booking, error) {
			return domain.Booking{
				ID:      sessionID,
				MovieID: "inception",
				SeatID:  "A1",
				UserID:  userID,
				Status:  "confirmed",
			}, nil
		},
	}

	uc := NewBookingUsecase(mockRepo)
	booking, err := uc.ConfirmSession(context.Background(), "session-123", "user123")

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if booking.Status != "confirmed" {
		t.Errorf("expected status 'confirmed', got %s", booking.Status)
	}
}

func TestConfirmSession_NotFound(t *testing.T) {
	mockRepo := &MockRepository{
		confirmFunc: func(ctx context.Context, sessionID string, userID string) (domain.Booking, error) {
			return domain.Booking{}, domain.ErrSessionNotFound
		},
	}

	uc := NewBookingUsecase(mockRepo)
	_, err := uc.ConfirmSession(context.Background(), "nonexistent", "user123")

	if !errors.Is(err, domain.ErrSessionNotFound) {
		t.Errorf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestReleaseSession_Success(t *testing.T) {
	mockRepo := &MockRepository{
		releaseFunc: func(ctx context.Context, sessionID string, userID string) error {
			return nil
		},
	}

	uc := NewBookingUsecase(mockRepo)
	err := uc.ReleaseSession(context.Background(), "session-123", "user123")

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestListSeats_Success(t *testing.T) {
	mockRepo := &MockRepository{
		listByMovieFunc: func(ctx context.Context, movieID string) ([]domain.Booking, error) {
			return []domain.Booking{
				{ID: "s1", MovieID: movieID, SeatID: "A1", UserID: "user1", Status: "held"},
				{ID: "s2", MovieID: movieID, SeatID: "A2", UserID: "user2", Status: "confirmed"},
			}, nil
		},
	}

	uc := NewBookingUsecase(mockRepo)
	bookings, err := uc.ListSeats(context.Background(), "inception")

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(bookings) != 2 {
		t.Errorf("expected 2 bookings, got %d", len(bookings))
	}
}
