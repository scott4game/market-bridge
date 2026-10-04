// options-probe performs read-only market-data requests. It never creates a TradeContext.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/joho/godotenv"
	"os"
	"time"

	lbconfig "github.com/longbridge/openapi-go/config"
	lbquote "github.com/longbridge/openapi-go/quote"
	"github.com/scott4game/market-bridge/internal/provider"
)

func main() {
	underlying := flag.String("underlying", "AAPL.US", "US underlying symbol")
	envFile := flag.String("env-file", "", "Optional environment file; overrides process values for this probe only")
	flag.Parse()
	if *envFile != "" {
		if err := godotenv.Overload(*envFile); err != nil {
			fmt.Fprintln(os.Stderr, "Unable to load environment file")
			os.Exit(1)
		}
	}
	if err := run(*underlying); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(raw string) error {
	underlying, err := provider.NormalizeOptionUnderlying(raw)
	if err != nil {
		return err
	}
	for _, key := range []string{"LONGBRIDGE_APP_KEY", "LONGBRIDGE_APP_SECRET", "LONGBRIDGE_ACCESS_TOKEN"} {
		if os.Getenv(key) == "" {
			return fmt.Errorf("%s is required", key)
		}
	}
	cfg, err := lbconfig.New()
	if err != nil {
		return fmt.Errorf("Longbridge configuration could not be loaded")
	}
	cfg.SetLogger(quietLogger{})
	q, err := lbquote.NewFromCfg(cfg)
	if err != nil {
		return fmt.Errorf("Longbridge quote connection failed; verify credentials and connectivity")
	}
	defer q.Close()
	p := &provider.LongbridgeOptions{Quote: q}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	printStage := func(stage string, data any) {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"stage": stage, "fetched_at": time.Now().UTC(), "data": data})
	}
	dates, err := p.Expirations(ctx, underlying)
	if err != nil {
		return err
	}
	printStage("expirations", dates)
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		return err
	}
	today := time.Now().In(ny).Format("2006-01-02")
	expiry := ""
	for _, d := range dates {
		if d >= today {
			expiry = d
			break
		}
	}
	if expiry == "" {
		return fmt.Errorf("no unexpired option dates returned")
	}
	date, _ := time.Parse("2006-01-02", expiry)
	chain, err := p.Chain(ctx, underlying, date)
	if err != nil {
		return err
	}
	// Choose a standard call/put pair at the same strike, using returned symbols only.
	symbols := []string{}
	for i := 0; i+1 < len(chain); i++ {
		a, b := chain[i], chain[i+1]
		if a.Standard && b.Standard && a.Type == "call" && b.Type == "put" && a.StrikePrice != nil && b.StrikePrice != nil && *a.StrikePrice == *b.StrikePrice {
			symbols = []string{a.Symbol, b.Symbol}
			break
		}
	}
	printStage("chain", map[string]any{"expiration": expiry, "count": len(chain), "selected_symbols": symbols})
	if len(symbols) != 2 {
		return fmt.Errorf("no standard call/put pair returned")
	}
	quotes, quoteErr := p.Quotes(ctx, symbols)
	if quoteErr != nil {
		printStage("quotes", map[string]any{"error": quoteErr.Error()})
	} else {
		printStage("quotes", quotes)
	}
	greeks, greekErr := p.Greeks(ctx, symbols)
	if greekErr != nil {
		printStage("greeks", map[string]any{"error": greekErr.Error()})
	} else {
		printStage("greeks", greeks)
	}
	if quoteErr != nil || greekErr != nil {
		return fmt.Errorf("option probe failed; see stage errors")
	}
	seenQuotes := map[string]bool{}
	for _, q := range quotes {
		seenQuotes[q.Symbol] = q.Last != nil && q.Timestamp != nil
	}
	seenGreeks := map[string]bool{}
	for _, g := range greeks {
		seenGreeks[g.Symbol] = g.Delta != nil && g.Gamma != nil && g.Theta != nil && g.Vega != nil && g.Rho != nil
	}
	complete := true
	for _, s := range symbols {
		complete = complete && seenQuotes[s] && seenGreeks[s]
	}
	printStage("validation", map[string]any{"complete": complete, "quote_fields": seenQuotes, "greeks_fields": seenGreeks, "note": "Market-data access only; validate quote freshness during trading hours. Does not verify trading permissions."})
	if !complete {
		return fmt.Errorf("probe returned incomplete market data; inspect missing fields")
	}
	return nil
}

type quietLogger struct{}

func (quietLogger) SetLevel(string)               {}
func (quietLogger) Info(string)                   {}
func (quietLogger) Error(string)                  {}
func (quietLogger) Warn(string)                   {}
func (quietLogger) Debug(string)                  {}
func (quietLogger) Infof(string, ...interface{})  {}
func (quietLogger) Errorf(string, ...interface{}) {}
func (quietLogger) Warnf(string, ...interface{})  {}
func (quietLogger) Debugf(string, ...interface{}) {}
