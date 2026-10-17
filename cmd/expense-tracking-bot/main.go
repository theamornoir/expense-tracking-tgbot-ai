package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/theamornoir/expense-tracking-bot/internal/config"
	"github.com/theamornoir/expense-tracking-bot/internal/db"
	"github.com/theamornoir/expense-tracking-bot/internal/llm"
	"github.com/theamornoir/expense-tracking-bot/internal/repository"
	"github.com/theamornoir/expense-tracking-bot/internal/rest"
	"github.com/theamornoir/expense-tracking-bot/internal/service"
	"github.com/theamornoir/expense-tracking-bot/internal/telegram"
	"github.com/theamornoir/expense-tracking-bot/internal/web"
)

func main() {
	slog.Info("starting expense-tracking-bot")

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config load failed", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.New(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("database init failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	repo := repository.New(pool)
	svc := service.New(repo, cfg)

	var llmClient llm.LLMClient
	if cfg.MOCK_LLM {
		llmClient = llm.NewMockClient()
		slog.Info("starting in mock mode", "MOCK_LLM", true)
	} else {
		llmClient = llm.New(cfg.LLMBaseURL, cfg.LLMAPIKey, cfg.LLMModel)
		slog.Info("starting in normal mode", "model", cfg.LLMModel)
	}

	agent := telegram.NewAgent(svc, llmClient, cfg)
	bot, err := telegram.NewBot(cfg.TelegramToken, agent, cfg)
	if err != nil {
		slog.Error("telegram bot init failed", "err", err)
		os.Exit(1)
	}

	// REST API handler mounted under /api
	apiHandler := rest.NewExpenseHandler(svc)

	mux := http.NewServeMux()
	mux.Handle("/api/", http.StripPrefix("/api", apiHandler.Routes()))
	mux.Handle("/", http.FileServer(http.FS(web.FS)))

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", 8080),
		Handler: mux,
	}

	go func() {
		slog.Info("HTTP server listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP server failed", "err", err)
			os.Exit(1)
		}
	}()

	go func() {
		slog.Info("Telegram bot polling started")
		if err := bot.Run(ctx); err != nil && err != context.Canceled {
			slog.Error("telegram bot stopped", "err", err)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("HTTP shutdown failed", "err", err)
	}
}
