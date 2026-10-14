package main

import (
	"context"
	"github.com/scott4game/market-bridge/internal/config"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestRemotePortCLI(t *testing.T) {
	base := config.Client{Listen: "127.0.0.1:17600", RemoteToken: "fixture-remote-01234567890123456789"}
	_, cfg, err := parseClientCommand(nil, base)
	if err != nil || cfg.RemotePortConfigured {
		t.Fatal("default changed", err)
	}
	base.RemotePort = ""
	base.RemotePortConfigured = true
	_, cfg, err = parseClientCommand([]string{"serve", "--remote-port", "17602"}, base)
	if err != nil || cfg.RemotePort != "17602" {
		t.Fatal("CLI must override env", err)
	}
	for _, v := range []string{"", "0", "abc", "65536", "17600", "-1"} {
		if _, _, err := parseClientCommand([]string{"serve", "--remote-port", v}, base); err == nil {
			t.Fatalf("accepted %q", v)
		}
	}
}
func TestBindFailureReleasesFirstListener(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	first, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := first.Addr().String()
	first.Close()
	err = serveListeners(context.Background(), []string{addr, occupied.Addr().String()}, []http.Handler{http.NotFoundHandler(), http.NotFoundHandler()})
	if err == nil {
		t.Fatal("expected bind failure")
	}
	reopened, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal("first listener leaked", err)
	}
	reopened.Close()
}
func TestListenersStopTogether(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- serveListeners(ctx, []string{"127.0.0.1:0", "127.0.0.1:0"}, []http.Handler{http.NotFoundHandler(), http.NotFoundHandler()})
	}()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("shutdown blocked")
	}
}
