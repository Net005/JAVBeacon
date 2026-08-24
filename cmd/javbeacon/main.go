package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Net005/JAVBeacon/internal/app"
	"github.com/Net005/JAVBeacon/internal/logging"
)

func main() {
	resetUsername := flag.String("reset-username", "", "reset the single-user login name")
	resetPassword := flag.String("reset-password", "", "reset the single-user password (minimum 8 characters)")
	flag.Parse()
	if *resetUsername != "" || *resetPassword != "" {
		if err := app.ResetCredentials(*resetUsername, *resetPassword); err != nil {
			fmt.Fprintln(os.Stderr, "credential reset failed:", err)
			os.Exit(1)
		}
		fmt.Println("JAVBeacon credentials reset")
		return
	}
	ring := logging.NewRing(slog.NewTextHandler(os.Stdout, nil), logging.DefaultCapacity)
	logger := slog.New(ring)
	application, err := app.New(logger, ring)
	if err != nil {
		logger.Error("startup failed", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := application.Run(ctx); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
