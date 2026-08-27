package news

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
)

type FMP struct {
	APIKey  string
	BaseURL string
	Client  *http.Client
}

type UpstreamError struct {
	Status     int
	RetryAfter time.Duration
}

func (e *UpstreamError) Error() string { return fmt.Sprintf("fmp news status %d", e.Status) }

func (f *FMP) Name() string { return "fmp" }

type fmpArticle struct {
	Symbol        string `json:"symbol"`
	PublishedDate string `json:"publishedDate"`
	Publisher     string `json:"publisher"`
	Title         string `json:"title"`
	Image         string `json:"image"`
	Site          string `json:"site"`
	Text          string `json:"text"`
	URL           string `json:"url"`
}

func (f *FMP) Latest(ctx context.Context, kind Kind, page, limit int) ([]Article, error) {
	base := strings.TrimRight(f.BaseURL, "/")
	if base == "" {
		base = "https://financialmodelingprep.com"
	}
	path := "/stable/news/stock-latest"
	if kind == PressRelease {
		path = "/stable/news/press-releases-latest"
	}
	u, err := url.Parse(base + path)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("page", fmt.Sprint(page))
	q.Set("limit", fmt.Sprint(limit))
	q.Set("apikey", f.APIKey)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "market-bridge")
	client := f.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return nil, fmt.Errorf("fmp news request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		upstreamErr := &UpstreamError{Status: resp.StatusCode}
		if seconds, parseErr := strconv.Atoi(resp.Header.Get("Retry-After")); parseErr == nil && seconds > 0 {
			upstreamErr.RetryAfter = time.Duration(seconds) * time.Second
		}
		return nil, upstreamErr
	}
	var rows []fmpArticle
	if err := json.NewDecoder(io.LimitReader(resp.Body, 10<<20)).Decode(&rows); err != nil {
		return nil, fmt.Errorf("decode fmp news: %w", err)
	}
	now := time.Now().UTC()
	out := make([]Article, 0, len(rows))
	for _, row := range rows {
		published := parseFMPTime(row.PublishedDate)
		if published.IsZero() {
			published = now
		}
		publisher := strings.TrimSpace(row.Publisher)
		if publisher == "" {
			publisher = strings.TrimSpace(row.Site)
		}
		article := Article{Kind: kind, Symbols: []string{}, Title: truncateText(row.Title, 500), Summary: truncateText(row.Text, 4000), URL: safeHTTPURL(row.URL), ImageURL: safeHTTPURL(row.Image), Publisher: truncateText(publisher, 200), PublishedAt: published, ReceivedAt: now, Provider: f.Name()}
		for _, raw := range strings.FieldsFunc(row.Symbol, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
			if symbol, venue, e := market.NormalizeSymbol(raw); e == nil && venue == market.VenueUS {
				article.Symbols = append(article.Symbols, symbol)
			}
		}
		article.ID = articleID(article)
		if article.Title != "" && article.ID != "" {
			out = append(out, article)
		}
	}
	return out, nil
}

func articleID(a Article) string {
	identity := strings.TrimSpace(a.URL)
	if u, err := url.Parse(identity); err == nil && u.Host != "" {
		u.Scheme = strings.ToLower(u.Scheme)
		u.Host = strings.ToLower(u.Host)
		u.Fragment = ""
		q := u.Query()
		for key := range q {
			if strings.HasPrefix(strings.ToLower(key), "utm_") {
				q.Del(key)
			}
		}
		u.RawQuery = q.Encode()
		identity = u.String()
	}
	if identity == "" {
		identity = strings.Join([]string{a.Provider, a.Publisher, a.Title, a.PublishedAt.UTC().Format(time.RFC3339)}, "|")
	}
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:16])
}

func parseFMPTime(v string) time.Time {
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339, "2006-01-02T15:04:05.000Z"} {
		if parsed, err := time.Parse(layout, strings.TrimSpace(v)); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func truncateText(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes)
}

func safeHTTPURL(value string) string {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return u.String()
}
