// Command server runs the HeartBridge matchmaking API together with its
// background workers. It is the only entry point of the deployment image.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/vance1852/heartbridge-matchmaking/internal/app"
	"github.com/vance1852/heartbridge-matchmaking/internal/config"
	"github.com/vance1852/heartbridge-matchmaking/internal/logging"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "heartbridge: %v\n", err)
		os.Exit(1)
	}
}

// run resolves the configuration, wires the application and serves until the
// process receives an interrupt or termination signal.
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := logging.New(os.Stdout, cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	instance, err := app.New(ctx, cfg, app.Options{Logger: logger})
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := instance.Close(); closeErr != nil {
			logger.Error("could not close database", "error", closeErr.Error())
		}
	}()

	return instance.Run(ctx)
}
