package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/scott4game/market-bridge/internal/config"
)

func parseClientCommand(args []string, cfg config.Client) (string, config.Client, error) {
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd = args[0]
		args = args[1:]
	}
	if cmd == "serve" {
		set := flag.NewFlagSet("serve", flag.ContinueOnError)
		set.SetOutput(io.Discard)
		set.StringVar(&cfg.RemotePort, "remote-port", cfg.RemotePort, "enable authenticated remote listener on port 1..65535; absent disables")
		if err := set.Parse(args); err != nil {
			return cmd, cfg, err
		}
		if set.NArg() != 0 {
			return cmd, cfg, fmt.Errorf("unexpected serve arguments")
		}
		set.Visit(func(f *flag.Flag) {
			if f.Name == "remote-port" {
				cfg.RemotePortConfigured = true
			}
		})
	}
	return cmd, cfg, cfg.Validate()
}

// Bind every listener before serving any traffic. Partial bind failures close all sockets.
func serveListeners(ctx context.Context, addresses []string, handlers []http.Handler) error {
	if len(addresses) != len(handlers) || len(addresses) == 0 {
		return fmt.Errorf("invalid listeners")
	}
	listeners := make([]net.Listener, 0, len(addresses))
	defer func() {
		for _, l := range listeners {
			_ = l.Close()
		}
	}()
	for _, address := range addresses {
		l, err := net.Listen("tcp", address)
		if err != nil {
			return fmt.Errorf("bind client listener: %w", err)
		}
		listeners = append(listeners, l)
	}
	servers := make([]*http.Server, len(listeners))
	errs := make(chan error, len(listeners))
	for i, l := range listeners {
		servers[i] = &http.Server{Handler: handlers[i], ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 1 << 20}
		log.Printf("go-client listener ready at %s (remote=%t)", l.Addr(), i > 0)
		go func(s *http.Server, l net.Listener) { errs <- s.Serve(l) }(servers[i], l)
	}
	var err error
	select {
	case <-ctx.Done():
	case err = <-errs:
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{}, len(servers))
	for _, s := range servers {
		go func(s *http.Server) {
			if s.Shutdown(shutdown) != nil {
				_ = s.Close()
			}
			done <- struct{}{}
		}(s)
	}
	for range servers {
		<-done
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
