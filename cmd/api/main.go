package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"be-miawai/internal/config"
	"be-miawai/internal/database"
	"be-miawai/internal/handlers"
	"be-miawai/internal/middleware"
)

func main() {
	cfg := config.Load()
	ctx := context.Background()

	store, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer store.Close()

	if err := store.RunMigrations(ctx, "migrations"); err != nil {
		log.Fatalf("run migrations: %v", err)
	}

	api := handlers.NewServer(cfg, store)
	handler := middleware.CORS(cfg.CORSOrigins)(api.Routes())

	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("be-miawai listening on :%s", cfg.Port)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}
