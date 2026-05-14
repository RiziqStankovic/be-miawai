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
	"be-miawai/internal/models"
	"be-miawai/internal/whatsapp"

	"golang.org/x/crypto/bcrypt"
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
	if cfg.AdminBootstrapEnabled {
		admin, err := bootstrapAdmin(ctx, store, cfg)
		if err != nil {
			log.Fatalf("bootstrap admin: %v", err)
		}
		if cfg.WhatsAppOwnerUserID == "" {
			cfg.WhatsAppOwnerUserID = admin.ID
		}
	}

	api := handlers.NewServer(cfg, store)
	handler := middleware.CORS(cfg.CORSOrigins)(api.Routes())
	waRunner := whatsapp.NewRunner(whatsapp.Config{
		Enabled:      cfg.WhatsAppEnabled,
		ListenGroups: cfg.WhatsAppListenGroups,
		SessionDB:    cfg.WhatsAppSessionDB,
	}, api)
	api.SetWhatsAppPairingRefresher(waRunner.RefreshPairing)
	if err := waRunner.Start(ctx); err != nil {
		log.Fatalf("start whatsapp: %v", err)
	}

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
	waRunner.Stop(shutdownCtx)
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

func bootstrapAdmin(ctx context.Context, store *database.Store, cfg config.Config) (models.User, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(cfg.AdminBootstrapPassword), bcrypt.DefaultCost)
	if err != nil {
		return models.User{}, err
	}
	user, err := store.EnsurePasswordUser(ctx, cfg.AdminBootstrapEmail, cfg.AdminBootstrapName, string(hash))
	if err != nil {
		return models.User{}, err
	}
	log.Printf("admin bootstrap ready email=%s user=%s", user.Email, user.ID)
	return user, nil
}
