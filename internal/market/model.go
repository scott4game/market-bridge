package market

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	shopdecimal "github.com/shopspring/decimal"
)

type AdjustmentMode string

const (
	AutoAdjusted    AdjustmentMode = "auto"
	Raw             AdjustmentMode = "raw"
	SplitAdjusted   AdjustmentMode = "split_adjusted"
	ForwardAdjusted AdjustmentMode = "forward_adjusted"
)

type Session string

const (
	RegularSession    Session = "regular"
	ExtendedSession   Session = "extended"
	ContinuousSession Session = "continuous"
)

type Venue string

const (
	VenueUS      Venue = "US"
	VenueHK      Venue = "HK"
	VenueSH      Venue = "SH"
	VenueSZ      Venue = "SZ"
	VenueBinance Venue = "BINANCE"
)

func NormalizeSymbol(symbol string) (string, Venue, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return "", "", errors.New("symbol is empty")
	}
	if strings.HasPrefix(symbol, ".") || strings.HasSuffix(symbol, ".") || strings.Contains(symbol, "..") {
		return "", "", fmt.Errorf("invalid symbol %q", symbol)
	}
	for _, venue := range []Venue{VenueUS, VenueHK, VenueSH, VenueSZ, VenueBinance} {
		suffix := "." + string(venue)
		if !strings.HasSuffix(symbol, suffix) {
			continue
		}
		base := strings.TrimSuffix(symbol, suffix)
		if base == "" {
			return "", "", fmt.Errorf("invalid symbol %q", symbol)
		}
		if venue == VenueUS {
			return base, VenueUS, nil
		}
		return base + suffix, venue, nil
	}
	if dot := strings.LastIndexByte(symbol, '.'); dot >= 0 {
		class := symbol[dot+1:]
		if len(class) != 1 || class[0] < 'A' || class[0] > 'Z' {
			return "", "", fmt.Errorf("unsupported market suffix %q", class)
		}
	}
	return symbol, VenueUS, nil
}

func VenueOf(symbol string) (Venue, error) {
	_, venue, err := NormalizeSymbol(symbol)
	return venue, err
}

type DatasetSpec struct {
	Symbols    []string       `json:"symbols"`
	Interval   string         `json:"interval"`
	From       time.Time      `json:"from"`
	To         time.Time      `json:"to"`
	Session    Session        `json:"session"`
	Adjustment AdjustmentMode `json:"adjustment"`
}

func (s DatasetSpec) Normalize() (DatasetSpec, error) {
	if len(s.Symbols) == 0 {
		return s, errors.New("at least one symbol is required")
	}
	if !s.From.Before(s.To) {
		return s, errors.New("from must be before to")
	}
	if s.Interval == "" {
		s.Interval = "1m"
	}
	if !validInterval(s.Interval) {
		return s, fmt.Errorf("unsupported interval %q", s.Interval)
	}
	seen := make(map[string]struct{}, len(s.Symbols))
	symbols := make([]string, 0, len(s.Symbols))
	venues := map[Venue]struct{}{}
	for _, symbol := range s.Symbols {
		normalized, venue, err := NormalizeSymbol(symbol)
		if err != nil {
			if strings.TrimSpace(symbol) == "" {
				continue
			}
			return s, err
		}
		if normalized == "" {
			continue
		}
		venues[venue] = struct{}{}
		if _, ok := seen[normalized]; !ok {
			seen[normalized] = struct{}{}
			symbols = append(symbols, normalized)
		}
	}
	if len(symbols) == 0 {
		return s, errors.New("at least one non-empty symbol is required")
	}
	sort.Strings(symbols)
	s.Symbols = symbols
	_, hasCrypto := venues[VenueBinance]
	if hasCrypto && len(venues) > 1 {
		return s, errors.New("crypto and securities cannot be mixed in one dataset")
	}
	if s.Session == "" {
		if hasCrypto {
			s.Session = ContinuousSession
		} else {
			s.Session = RegularSession
		}
	}
	if s.Session != RegularSession && s.Session != ExtendedSession && s.Session != ContinuousSession {
		return s, fmt.Errorf("unsupported session %q", s.Session)
	}
	if hasCrypto && s.Session != ContinuousSession {
		return s, errors.New("binance symbols require session continuous")
	}
	if !hasCrypto && s.Session == ContinuousSession {
		return s, errors.New("continuous session is only valid for crypto symbols")
	}
	if s.Session == ExtendedSession {
		for venue := range venues {
			if venue != VenueUS {
				return s, errors.New("extended session is only valid for US symbols")
			}
		}
	}
	if s.Adjustment == "" || s.Adjustment == AutoAdjusted {
		s.Adjustment = Raw
		if hasCrypto {
			s.Adjustment = Raw
		} else if len(venues) == 1 {
			for venue := range venues {
				if venue == VenueUS {
					s.Adjustment = SplitAdjusted
				} else {
					s.Adjustment = ForwardAdjusted
				}
			}
		}
	}
	if s.Adjustment != Raw && s.Adjustment != SplitAdjusted && s.Adjustment != ForwardAdjusted {
		return s, fmt.Errorf("unsupported adjustment %q", s.Adjustment)
	}
	for venue := range venues {
		switch venue {
		case VenueUS:
			if s.Adjustment == ForwardAdjusted {
				return s, errors.New("us symbols do not support forward_adjusted")
			}
		case VenueHK, VenueSH, VenueSZ:
			if s.Adjustment == SplitAdjusted {
				return s, errors.New("hk/cn symbols use forward_adjusted instead of split_adjusted")
			}
		case VenueBinance:
			if s.Adjustment != Raw {
				return s, errors.New("binance symbols only support raw adjustment")
			}
		}
	}
	s.From = s.From.UTC()
	s.To = s.To.UTC()
	return s, nil
}

func (s DatasetSpec) Hash(schemaVersion, dataVersion string) (string, error) {
	n, err := s.Normalize()
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(struct {
		Spec          DatasetSpec `json:"spec"`
		SchemaVersion string      `json:"schema_version"`
		DataVersion   string      `json:"data_version"`
	}{n, schemaVersion, dataVersion})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

type Decimal int64

const decimalScale = 1_000_000

func DecimalFromFloat(v float64) Decimal { return Decimal(math.Round(v * decimalScale)) }
func DecimalFromString(v string) (Decimal, error) {
	parsed, err := shopdecimal.NewFromString(v)
	if err != nil {
		return 0, err
	}
	scaled := parsed.Shift(6).Round(0)
	max := shopdecimal.NewFromInt(math.MaxInt64)
	min := shopdecimal.NewFromInt(math.MinInt64)
	if scaled.GreaterThan(max) || scaled.LessThan(min) {
		return 0, fmt.Errorf("decimal %q exceeds Decimal64(6) range", v)
	}
	return Decimal(scaled.IntPart()), nil
}
func (d Decimal) Float64() float64             { return float64(d) / decimalScale }
func (d Decimal) String() string               { return strconv.FormatFloat(d.Float64(), 'f', 6, 64) }
func (d Decimal) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }
func (d *Decimal) UnmarshalJSON(b []byte) error {
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	var v float64
	var err error
	switch x := raw.(type) {
	case string:
		v, err = strconv.ParseFloat(x, 64)
	case float64:
		v = x
	default:
		err = fmt.Errorf("invalid decimal %s", b)
	}
	if err == nil {
		*d = DecimalFromFloat(v)
	}
	return err
}

type Bar struct {
	Symbol        string    `json:"symbol"`
	Timestamp     time.Time `json:"timestamp"`
	Open          Decimal   `json:"open"`
	High          Decimal   `json:"high"`
	Low           Decimal   `json:"low"`
	Close         Decimal   `json:"close"`
	Volume        int64     `json:"volume"`
	VolumeDecimal string    `json:"volume_decimal,omitempty"`
	Turnover      *Decimal  `json:"turnover,omitempty"`
	Session       Session   `json:"session"`
	Source        string    `json:"source"`
	Completed     bool      `json:"completed"`
}

type EventType string

const (
	BarEvent   EventType = "bar"
	TradeEvent EventType = "trade"
	DepthEvent EventType = "depth"
	GapEvent   EventType = "gap"
)

type LiveCursor struct {
	StreamEpoch string    `json:"stream_epoch"`
	EventType   EventType `json:"event_type"`
	Symbol      string    `json:"symbol"`
	Sequence    int64     `json:"sequence"`
}
type LiveEvent struct {
	Type      EventType       `json:"type"`
	Symbol    string          `json:"symbol"`
	Timestamp time.Time       `json:"timestamp"`
	Cursor    LiveCursor      `json:"cursor"`
	Bar       *Bar            `json:"bar,omitempty"`
	Trade     json.RawMessage `json:"trade,omitempty"`
	Depth     json.RawMessage `json:"depth,omitempty"`
	Reason    string          `json:"reason,omitempty"`
}

type ParquetBar struct {
	Symbol        string `parquet:"symbol"`
	TimestampMS   int64  `parquet:"timestamp_ms,timestamp(millisecond:utc)"`
	Open          int64  `parquet:"open_micros"`
	High          int64  `parquet:"high_micros"`
	Low           int64  `parquet:"low_micros"`
	Close         int64  `parquet:"close_micros"`
	Volume        int64  `parquet:"volume"`
	VolumeDecimal string `parquet:"volume_decimal,optional"`
	Turnover      int64  `parquet:"turnover_micros"`
	HasTurnover   bool   `parquet:"has_turnover"`
	Session       string `parquet:"session"`
	Source        string `parquet:"source"`
	Completed     bool   `parquet:"completed"`
}

func ToParquetBar(b Bar) ParquetBar {
	p := ParquetBar{Symbol: b.Symbol, TimestampMS: b.Timestamp.UnixMilli(), Open: int64(b.Open), High: int64(b.High), Low: int64(b.Low), Close: int64(b.Close), Volume: b.Volume, VolumeDecimal: b.VolumeDecimal, Session: string(b.Session), Source: b.Source, Completed: b.Completed}
	if b.Turnover != nil {
		p.Turnover, p.HasTurnover = int64(*b.Turnover), true
	}
	return p
}

func FromParquetBar(p ParquetBar) Bar {
	b := Bar{Symbol: p.Symbol, Timestamp: time.UnixMilli(p.TimestampMS).UTC(), Open: Decimal(p.Open), High: Decimal(p.High), Low: Decimal(p.Low), Close: Decimal(p.Close), Volume: p.Volume, VolumeDecimal: p.VolumeDecimal, Session: Session(p.Session), Source: p.Source, Completed: p.Completed}
	if p.HasTurnover {
		v := Decimal(p.Turnover)
		b.Turnover = &v
	}
	return b
}

func SortBars(bars []Bar) {
	sort.SliceStable(bars, func(i, j int) bool {
		if bars[i].Timestamp.Equal(bars[j].Timestamp) {
			return bars[i].Symbol < bars[j].Symbol
		}
		return bars[i].Timestamp.Before(bars[j].Timestamp)
	})
}

func validInterval(v string) bool {
	switch v {
	case "1m", "3m", "5m", "10m", "15m", "30m", "1h", "2h", "3h", "4h", "1d", "1w", "1mo", "1y":
		return true
	default:
		return false
	}
}

func IntervalDuration(v string) time.Duration {
	switch v {
	case "1m":
		return time.Minute
	case "3m":
		return 3 * time.Minute
	case "5m":
		return 5 * time.Minute
	case "10m":
		return 10 * time.Minute
	case "15m":
		return 15 * time.Minute
	case "30m":
		return 30 * time.Minute
	case "1h":
		return time.Hour
	case "2h":
		return 2 * time.Hour
	case "3h":
		return 3 * time.Hour
	case "4h":
		return 4 * time.Hour
	case "1d":
		return 24 * time.Hour
	case "1w":
		return 7 * 24 * time.Hour
	case "1mo":
		return 30 * 24 * time.Hour
	case "1y":
		return 365 * 24 * time.Hour
	default:
		return time.Minute
	}
}
