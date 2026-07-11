package domain

// Movie represents a cinema movie with available seats.
type Movie struct {
	ID          string // Unique movie identifier
	Title       string // Movie title
	Rows        int    // Number of rows
	SeatsPerRow int    // Number of seats per row
}

// Movies is a predefined list of available movies.
var Movies = []Movie{
	{ID: "inception", Title: "Inception", Rows: 5, SeatsPerRow: 8},
	{ID: "dune", Title: "Dune: Part Two", Rows: 4, SeatsPerRow: 6},
}
