package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"personalrouter/internal/runtime"
)

func main() {
	configPath := flag.String("config", "", "absolute path to the non-secret PersonalRouter JSON config")
	flag.Parse()
	config, err := runtime.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration unavailable")
		os.Exit(2)
	}
	service, err := runtime.New(config)
	if err != nil {
		fmt.Fprintln(os.Stderr, "runtime unavailable")
		os.Exit(2)
	}
	if err := service.Start(); err != nil {
		_ = service.Shutdown(context.Background())
		fmt.Fprintln(os.Stderr, "listeners unavailable")
		os.Exit(2)
	}
	watchdog, err := runtime.StartWatchdog(context.Background(), service.CheckHealth)
	if err != nil {
		_ = service.Shutdown(context.Background())
		fmt.Fprintln(os.Stderr, "watchdog unavailable")
		os.Exit(1)
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	select {
	case <-signals:
		watchdog.Stop(true)
		if err := service.Shutdown(context.Background()); err != nil {
			fmt.Fprintln(os.Stderr, "runtime shutdown failed")
			os.Exit(1)
		}
	case <-service.Errors():
		watchdog.Stop(true)
		fmt.Fprintln(os.Stderr, "runtime failure")
		_ = service.Shutdown(context.Background())
		os.Exit(1)
	}
}
