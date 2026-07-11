package main

import (
	"context"
	"log"
)

func main() {
	ctx := context.Background()

	// Bootstrap the application with all dependencies wired
	container, err := NewContainer(ctx)
	if err != nil {
		log.Fatalf("failed to create container: %v", err)
	}

	// Start the HTTP server (blocking)
	if err := container.Server.Start(); err != nil {
		container.Logger.Fatal("server error: %v", err)
	}
}
