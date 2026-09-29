package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"example.com/paleo-prep-station/internal/httpapi"
	"example.com/paleo-prep-station/internal/repository"
	"example.com/paleo-prep-station/internal/station"
)

func main() {
	os.Exit(run())
}

func run() int {
	addr := flag.String("addr", "127.0.0.1:18133", "local HTTP listen address")
	flag.Parse()

	repo := repository.NewMemory()
	service := station.New(repo)
	server := &http.Server{
		Addr:              *addr,
		Handler:           httpapi.NewRouter(service),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe()
	}()

	log.Printf("paleo prep station listening on %s", *addr)
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return 0
		}
		fmt.Fprintln(os.Stderr, "server stopped:", err)
		return 1
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintln(os.Stderr, "server shutdown:", err)
			return 1
		}
		return 0
	}
}
