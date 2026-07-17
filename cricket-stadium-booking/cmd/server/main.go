package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"stadiumbooking/internal/booking"
	"stadiumbooking/internal/config"
	"stadiumbooking/internal/httpapi"
	"stadiumbooking/internal/store"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	pool, err := store.NewPool(ctx, cfg)
	if err != nil {
		log.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	st := store.New(pool)
	svc := booking.NewService(st, cfg.HoldTTL, cfg.RequestTimeout, cfg.MaxRetries)
	handler := httpapi.NewServer(svc, cfg.RateLimitEnabled)

	srv := &http.Server{Addr: ":8080", Handler: handler}

	go func() {
		log.Printf("listening on %s", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}
