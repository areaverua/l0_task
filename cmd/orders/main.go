package main

import (
	"context"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/nats-io/stan.go"
	"github.com/areaverua/l0_task/internal/config"
	"github.com/areaverua/l0_task/internal/net/http/get_order"
	get_time "github.com/areaverua/l0_task/internal/net/http/time"
	"github.com/areaverua/l0_task/internal/net/nats"
	"github.com/areaverua/l0_task/internal/repository"
	postgresql "github.com/areaverua/l0_task/internal/repository/client/postgres"
	"github.com/areaverua/l0_task/internal/service"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	envLocal      = "local"
	envDev        = "dev"
	envProd       = "prod"
	retryAttempts = 3
	retryDelay    = 1 * time.Second
	ackWait       = 60 * time.Second
	durableName   = "simple-cluster-dur"
	maxInflight   = 25
)

func main() {
	cfg := config.MustLoad()
	log := setupLogger(cfg.Service.Env)
	log.Info("Orders service is starting!", slog.String("env", cfg.Service.Env))
	service := service.NewService()
	postgresqlClient, err := postgresql.NewClient(context.TODO(), cfg.DB, 3)
	if err != nil {
		log.Error("Failed connection to storage!: %s", err)
		os.Exit(1)
	}
	repo := repository.NewRepository(postgresqlClient, log)
	cache := repository.NewCache(10)
	cache.GetFromDB(context.TODO(), repo)
	sc, err := nats.NewNatsConnect(cfg, log)
	if err != nil {
		log.Error("NATS: ", err)
		os.Exit(1)
	}
	defer sc.Close()
	sub, err := sc.QueueSubscribe(
		"orders",
		"oders_group",
		nats.GetOrder(context.TODO(), cfg, log, service, repo, cache),
		stan.SetManualAckMode(),
		stan.AckWait(ackWait),
		stan.DurableName(durableName),
		stan.MaxInflight(maxInflight),
		stan.DeliverAllAvailable(),
	)
	if err != nil {
		log.Error("Nats: ", err)
	}
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.URLFormat)
	r.Get("/time", get_time.Time(log))
	r.Get("/order={id}", get_order.GetOrder(context.TODO(), log, repo, cache))
	log.Info("Starting the http server on", slog.Int("port", cfg.Service.HTTPServer.Port))
	srv := &http.Server{
		Addr:         cfg.Service.HTTPServer.Address,
		Handler:      r,
		IdleTimeout:  cfg.Service.HTTPServer.IddleTimeout,
		ReadTimeout:  cfg.Service.HTTPServer.Timeout,
		WriteTimeout: cfg.Service.HTTPServer.Timeout,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil {
			log.Error("Failed to start http server!")
		}
	}()
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGABRT, syscall.SIGTERM)
	<-signals
	sub.Unsubscribe()
	log.Error("Server stoped!")
	srv.Shutdown(context.Background())
}

func setupLogger(env string) *slog.Logger {
	var log *slog.Logger
	switch env {
	case envLocal:
		log = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	case envDev:
		log = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	case envProd:
		log = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	return log
}
