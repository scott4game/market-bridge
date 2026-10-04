package provider

import (
	"context"
	"errors"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"

	lbquote "github.com/longbridge/openapi-go/quote"
	protocol "github.com/longbridge/openapi-protocol/go"
	"github.com/shopspring/decimal"
)

// OptionsLiveProvider deliberately excludes historical contracts and bars.
type OptionsLiveProvider interface {
	Expirations(context.Context, string) ([]string, error)
	Chain(context.Context, string, time.Time) ([]LiveOptionContract, error)
	Quotes(context.Context, []string) ([]LiveOptionQuote, error)
	Greeks(context.Context, []string) ([]LiveOptionGreeks, error)
}

type LongbridgeOptionsClient interface {
	OptionChainExpiryDateList(context.Context, string) ([]time.Time, error)
	OptionChainInfoByDate(context.Context, string, *time.Time) ([]*lbquote.StrikePriceInfo, error)
	OptionQuote(context.Context, []string) ([]*lbquote.OptionQuote, error)
	CalcIndex(context.Context, []string, []lbquote.CalcIndex) ([]*lbquote.SecurityCalcIndex, error)
}
type LongbridgeOptions struct{ Quote LongbridgeOptionsClient }

type LiveOptionContract struct {
	Symbol      string  `json:"symbol"`
	Underlying  string  `json:"underlying"`
	Expiration  string  `json:"expiration"`
	Type        string  `json:"type"`
	StrikePrice *string `json:"strike_price"`
	Standard    bool    `json:"standard"`
}
type LiveOptionQuote struct {
	Symbol             string              `json:"symbol"`
	Timestamp          *time.Time          `json:"timestamp"`
	Last               *string             `json:"last"`
	PrevClose          *string             `json:"prev_close"`
	Open               *string             `json:"open"`
	High               *string             `json:"high"`
	Low                *string             `json:"low"`
	Volume             int64               `json:"volume"`
	Turnover           *string             `json:"turnover"`
	TradeStatus        lbquote.TradeStatus `json:"trade_status"`
	ImpliedVolatility  *string             `json:"implied_volatility"`
	OpenInterest       *int64              `json:"open_interest"`
	Underlying         string              `json:"underlying,omitempty"`
	Expiration         string              `json:"expiration,omitempty"`
	StrikePrice        *string             `json:"strike_price"`
	ContractMultiplier *string             `json:"contract_multiplier"`
	ContractSize       *string             `json:"contract_size"`
	ExerciseStyle      string              `json:"exercise_style,omitempty"`
	Direction          string              `json:"direction,omitempty"`
}
type LiveOptionGreeks struct {
	Symbol string  `json:"symbol"`
	Delta  *string `json:"delta"`
	Gamma  *string `json:"gamma"`
	Theta  *string `json:"theta"`
	Vega   *string `json:"vega"`
	Rho    *string `json:"rho"`
}

var optionUnderlyingPattern = regexp.MustCompile(`^[A-Z][A-Z0-9.\-]{0,19}$`)
var optionSymbolPattern = regexp.MustCompile(`^[A-Z][A-Z0-9.\-]{0,19}([0-9]{6})[CP][0-9]{1,12}\.US$`)

func NormalizeOptionUnderlying(raw string) (string, error) {
	s := strings.TrimSuffix(strings.ToUpper(strings.TrimSpace(raw)), ".US")
	if !optionUnderlyingPattern.MatchString(s) || strings.HasSuffix(s, ".HK") || strings.HasSuffix(s, ".SH") || strings.HasSuffix(s, ".SZ") {
		return "", errors.New("underlying must be a US stock symbol")
	}
	return s + ".US", nil
}
func NormalizeOptionSymbols(raw []string) ([]string, error) {
	if len(raw) == 0 || len(raw) > 100 {
		return nil, errors.New("symbols must contain 1..100 native Longbridge option symbols")
	}
	set := map[string]bool{}
	out := []string{}
	for _, s := range raw {
		s = strings.ToUpper(strings.TrimSpace(s))
		match := optionSymbolPattern.FindStringSubmatch(s)
		if match == nil {
			return nil, errors.New("invalid native Longbridge option symbol")
		}
		if _, err := time.Parse("060102", match[1]); err != nil {
			return nil, errors.New("invalid option symbol expiration date")
		}
		if !set[s] {
			set[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out, nil
}
func optionDecimal(d *decimal.Decimal) *string {
	if d == nil {
		return nil
	}
	s := d.String()
	return &s
}
func optionDecimalString(s string) *string {
	d, err := decimal.NewFromString(s)
	if err != nil {
		return nil
	}
	return optionDecimal(&d)
}

func (p *LongbridgeOptions) Expirations(ctx context.Context, underlying string) ([]string, error) {
	underlying, err := NormalizeOptionUnderlying(underlying)
	if err != nil {
		return nil, err
	}
	dates, err := p.Quote.OptionChainExpiryDateList(ctx, underlying)
	if err != nil {
		return nil, SafeOptionLiveError(err)
	}
	result := []string{}
	seen := map[string]bool{}
	for _, d := range dates {
		s := d.Format("2006-01-02")
		if !seen[s] {
			result = append(result, s)
			seen[s] = true
		}
	}
	sort.Strings(result)
	return result, nil
}
func (p *LongbridgeOptions) Chain(ctx context.Context, underlying string, expiry time.Time) ([]LiveOptionContract, error) {
	underlying, err := NormalizeOptionUnderlying(underlying)
	if err != nil {
		return nil, err
	}
	rows, err := p.Quote.OptionChainInfoByDate(ctx, underlying, &expiry)
	if err != nil {
		return nil, SafeOptionLiveError(err)
	}
	result := []LiveOptionContract{}
	for _, r := range rows {
		if r == nil {
			continue
		}
		for _, item := range []struct{ symbol, direction string }{{r.CallSymbol, "call"}, {r.PutSymbol, "put"}} {
			if item.symbol != "" {
				result = append(result, LiveOptionContract{Symbol: item.symbol, Underlying: underlying, Expiration: expiry.Format("2006-01-02"), Type: item.direction, StrikePrice: optionDecimal(r.Price), Standard: r.Standard})
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if a.StrikePrice != nil && b.StrikePrice != nil {
			x, _ := decimal.NewFromString(*a.StrikePrice)
			y, _ := decimal.NewFromString(*b.StrikePrice)
			if !x.Equal(y) {
				return x.LessThan(y)
			}
		} else if (a.StrikePrice == nil) != (b.StrikePrice == nil) {
			return b.StrikePrice == nil
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		return a.Symbol < b.Symbol
	})
	return result, nil
}
func (p *LongbridgeOptions) Quotes(ctx context.Context, symbols []string) ([]LiveOptionQuote, error) {
	symbols, err := NormalizeOptionSymbols(symbols)
	if err != nil {
		return nil, err
	}
	rows, err := p.Quote.OptionQuote(ctx, symbols)
	if err != nil {
		return nil, SafeOptionLiveError(err)
	}
	out := []LiveOptionQuote{}
	for _, r := range rows {
		if r == nil {
			continue
		}
		q := LiveOptionQuote{Symbol: r.Symbol, Last: optionDecimal(r.LastDone), PrevClose: optionDecimal(r.PrevClose), Open: optionDecimal(r.Open), High: optionDecimal(r.High), Low: optionDecimal(r.Low), Volume: r.Volume, Turnover: optionDecimal(r.Turnover), TradeStatus: r.TradeStatus}
		if r.Timestamp > 0 {
			ts := time.Unix(r.Timestamp, 0).UTC()
			q.Timestamp = &ts
		}
		if e := r.OptionExtend; e != nil {
			q.ImpliedVolatility = optionDecimalString(e.ImpliedVolatility)
			oi := e.OpenInterest
			q.OpenInterest = &oi
			q.Underlying = e.UnderlyingSymbol
			q.StrikePrice = optionDecimal(e.StrikePrice)
			q.ContractMultiplier = optionDecimalString(e.ContractMultiplier)
			q.ContractSize = optionDecimalString(e.ContractSize)
			switch e.ContractType {
			case "A":
				q.ExerciseStyle = "american"
			case "U":
				q.ExerciseStyle = "european"
			}
			switch e.Direction {
			case "C":
				q.Direction = "call"
			case "P":
				q.Direction = "put"
			}
			for _, layout := range []string{"20060102", "060102", "2006-01-02"} {
				if d, err := time.Parse(layout, e.ExpiryDate); err == nil {
					q.Expiration = d.Format("2006-01-02")
					break
				}
			}
		}
		out = append(out, q)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Symbol < out[j].Symbol })
	return out, nil
}
func (p *LongbridgeOptions) Greeks(ctx context.Context, symbols []string) ([]LiveOptionGreeks, error) {
	symbols, err := NormalizeOptionSymbols(symbols)
	if err != nil {
		return nil, err
	}
	rows, err := p.Quote.CalcIndex(ctx, symbols, []lbquote.CalcIndex{lbquote.CalcIndexDELTA, lbquote.CalcIndexGAMMA, lbquote.CalcIndexTHETA, lbquote.CalcIndexVEGA, lbquote.CalcIndexRHO})
	if err != nil {
		return nil, SafeOptionLiveError(err)
	}
	out := []LiveOptionGreeks{}
	for _, r := range rows {
		if r != nil {
			out = append(out, LiveOptionGreeks{Symbol: r.Symbol, Delta: optionDecimal(r.Delta), Gamma: optionDecimal(r.Gamma), Theta: optionDecimal(r.Theta), Vega: optionDecimal(r.Vega), Rho: optionDecimal(r.Rho)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Symbol < out[j].Symbol })
	return out, nil
}

// Error text is deliberately independent of upstream messages, which may contain credentials.
type OptionLiveError struct {
	Code   string
	Status int
}

func (e *OptionLiveError) Error() string { return "longbridge options: " + e.Code }
func SafeOptionLiveError(err error) *OptionLiveError {
	var safe *OptionLiveError
	if errors.As(err, &safe) {
		return safe
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &OptionLiveError{"upstream_timeout", 504}
	}
	if errors.Is(err, context.Canceled) {
		return &OptionLiveError{"request_canceled", 408}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return &OptionLiveError{"upstream_timeout", 504}
	}
	var lb *protocol.LBError
	if errors.As(err, &lb) {
		switch lb.Code {
		case 301604:
			return &OptionLiveError{"quote_permission_denied", 403}
		case 301606:
			return &OptionLiveError{"upstream_rate_limited", 429}
		case 301603:
			return &OptionLiveError{"quote_unavailable", 502}
		}
	}
	return &OptionLiveError{"upstream_failure", 502}
}
