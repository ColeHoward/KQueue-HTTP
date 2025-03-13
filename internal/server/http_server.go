package server

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// StartHTTPServer runs a basic HTTP server for benchmarking.

func StartHTTPServer(ctx context.Context, port int) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/process", func(w http.ResponseWriter, r *http.Request) {
		// Simulate processing
		time.Sleep(1 * time.Millisecond)
		fmt.Fprintln(w, "Process complete")
	})

	server := &http.Server{
		Addr:           fmt.Sprintf(":%d", port),
		Handler:        mux,
		ReadTimeout:    5 * time.Second,
		WriteTimeout:   5 * time.Second,
		MaxHeaderBytes: 1 << 20, // 1MB
	}

	// Start server in a goroutine
	go func() {
		fmt.Println("Starting HTTP server 🔥")
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			fmt.Printf("❌ net/http server error: %v\n", err)
		}
	}()

	// Wait for the context to be cancelled, then shut down gracefully.
	<-ctx.Done()
	return server.Shutdown(context.Background())
}
