package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/scott4game/market-bridge/internal/access"
	"github.com/scott4game/market-bridge/internal/config"
)

func adminCommand(cfg config.Server) {
	if err := os.MkdirAll(filepath.Dir(cfg.AuthDB), 0o755); err != nil {
		log.Fatal(err)
	}
	store, err := access.Open(cfg.AuthDB, cfg.BearerToken)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	args := os.Args[2:]
	if len(args) < 2 {
		adminUsage()
	}
	ctx := context.Background()
	switch args[0] + " " + args[1] {
	case "user create":
		fs := flag.NewFlagSet("user create", flag.ExitOnError)
		name := fs.String("name", "", "user name")
		role := fs.String("role", "member", "member or admin")
		_ = fs.Parse(args[2:])
		u, err := store.CreateUser(ctx, *name, *role)
		fatalIf(err)
		printJSON(u)
	case "user list":
		users, err := store.ListUsers(ctx)
		fatalIf(err)
		printJSON(users)
	case "user enable", "user disable":
		fs := flag.NewFlagSet(args[1], flag.ExitOnError)
		name := fs.String("name", "", "user name")
		_ = fs.Parse(args[2:])
		fatalIf(store.SetUserEnabled(ctx, *name, args[1] == "enable"))
		fmt.Println("user updated")
	case "key create":
		fs := flag.NewFlagSet("key create", flag.ExitOnError)
		user := fs.String("user", "", "user name")
		name := fs.String("name", "default", "key name")
		valid := fs.Duration("expires-in", 365*24*time.Hour, "valid duration")
		_ = fs.Parse(args[2:])
		token, key, err := store.CreateKey(ctx, *user, *name, *valid)
		fatalIf(err)
		fmt.Printf("API key (shown once): %s\n", token)
		printJSON(key)
	case "key list":
		fs := flag.NewFlagSet("key list", flag.ExitOnError)
		user := fs.String("user", "", "user name")
		_ = fs.Parse(args[2:])
		keys, err := store.ListKeys(ctx, *user)
		fatalIf(err)
		printJSON(keys)
	case "key revoke":
		fs := flag.NewFlagSet("key revoke", flag.ExitOnError)
		prefix := fs.String("prefix", "", "key prefix")
		_ = fs.Parse(args[2:])
		fatalIf(store.RevokeKey(ctx, *prefix))
		fmt.Println("key revoked")
	case "quota set":
		fs := flag.NewFlagSet("quota set", flag.ExitOnError)
		user := fs.String("user", "", "user name")
		requests := fs.Int("requests-per-minute", 600, "request limit")
		datasets := fs.Int("datasets-per-minute", 20, "dataset creation limit")
		builds := fs.Int("concurrent-builds", 2, "build limit")
		connections := fs.Int("live-connections", 3, "websocket limit")
		symbols := fs.Int("live-symbols", 20, "symbol limit")
		_ = fs.Parse(args[2:])
		fatalIf(store.SetQuotas(ctx, *user, access.Quotas{RequestsPerMinute: *requests, DatasetsPerMinute: *datasets, ConcurrentBuilds: *builds, LiveConnections: *connections, LiveSymbols: *symbols}))
		fmt.Println("quota updated")
	case "quota clear":
		fs := flag.NewFlagSet("quota clear", flag.ExitOnError)
		user := fs.String("user", "", "user name")
		_ = fs.Parse(args[2:])
		fatalIf(store.ClearQuotas(ctx, *user))
		fmt.Println("quota override cleared")
	default:
		adminUsage()
	}
}

func adminUsage() {
	fmt.Fprintln(os.Stderr, "usage: go-server admin user|key|quota <command> [flags]")
	os.Exit(2)
}
func fatalIf(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
func printJSON(v any) { b, _ := json.MarshalIndent(v, "", "  "); fmt.Println(string(b)) }
