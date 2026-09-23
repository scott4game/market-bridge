package localclient

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
)

const maxFlowBaskets = 50
const maxFlowBasketSymbols = 200

var (
	errFlowBasketNotFound = errors.New("flow basket not found")
	errFlowBasketConflict = errors.New("flow basket revision conflict")
	errFlowBasketName     = errors.New("flow basket name already exists")
	errFlowBasketLimit    = errors.New("flow basket limit reached")
)

type flowBasket struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Symbols   []string  `json:"symbols"`
	Revision  int       `json:"revision"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type flowBasketMutation struct {
	Name     string   `json:"name"`
	Symbols  []string `json:"symbols"`
	Revision int      `json:"revision,omitempty"`
}

func (c *Cache) FlowBaskets(ctx context.Context) ([]flowBasket, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT id,name,symbols_json,revision,created_at,updated_at FROM flow_baskets ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []flowBasket{}
	for rows.Next() {
		item, err := scanFlowBasket(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (c *Cache) FlowBasket(ctx context.Context, id string) (flowBasket, error) {
	item, err := scanFlowBasket(c.db.QueryRowContext(ctx, `SELECT id,name,symbols_json,revision,created_at,updated_at FROM flow_baskets WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return item, errFlowBasketNotFound
	}
	return item, err
}

func (c *Cache) CreateFlowBasket(ctx context.Context, mutation flowBasketMutation) (flowBasket, error) {
	name, symbols, err := validateFlowBasketMutation(mutation)
	if err != nil {
		return flowBasket{}, err
	}
	var count int
	if err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM flow_baskets`).Scan(&count); err != nil {
		return flowBasket{}, err
	}
	if count >= maxFlowBaskets {
		return flowBasket{}, errFlowBasketLimit
	}
	id, err := localIndicatorID()
	if err != nil {
		return flowBasket{}, err
	}
	raw, _ := json.Marshal(symbols)
	now := time.Now().Unix()
	if _, err := c.db.ExecContext(ctx, `INSERT INTO flow_baskets(id,name,symbols_json,revision,created_at,updated_at) VALUES(?,?,?,1,?,?)`, id, name, raw, now, now); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return flowBasket{}, errFlowBasketName
		}
		return flowBasket{}, err
	}
	return c.FlowBasket(ctx, id)
}

func (c *Cache) UpdateFlowBasket(ctx context.Context, id string, mutation flowBasketMutation) (flowBasket, error) {
	if mutation.Revision < 1 {
		return flowBasket{}, errors.New("revision is required")
	}
	name, symbols, err := validateFlowBasketMutation(mutation)
	if err != nil {
		return flowBasket{}, err
	}
	raw, _ := json.Marshal(symbols)
	result, err := c.db.ExecContext(ctx, `UPDATE flow_baskets SET name=?,symbols_json=?,revision=revision+1,updated_at=? WHERE id=? AND revision=?`, name, raw, time.Now().Unix(), id, mutation.Revision)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return flowBasket{}, errFlowBasketName
		}
		return flowBasket{}, err
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		if _, err := c.FlowBasket(ctx, id); errors.Is(err, errFlowBasketNotFound) {
			return flowBasket{}, errFlowBasketNotFound
		}
		return flowBasket{}, errFlowBasketConflict
	}
	_, _ = c.db.ExecContext(ctx, `DELETE FROM flow_basket_results WHERE basket_id=?`, id)
	return c.FlowBasket(ctx, id)
}

func (c *Cache) DeleteFlowBasket(ctx context.Context, id string, revision int) error {
	if revision < 1 {
		return errors.New("revision is required")
	}
	result, err := c.db.ExecContext(ctx, `DELETE FROM flow_baskets WHERE id=? AND revision=?`, id, revision)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed > 0 {
		_, _ = c.db.ExecContext(ctx, `DELETE FROM flow_basket_results WHERE basket_id=?`, id)
		return nil
	}
	if _, err := c.FlowBasket(ctx, id); errors.Is(err, errFlowBasketNotFound) {
		return errFlowBasketNotFound
	}
	return errFlowBasketConflict
}

func validateFlowBasketMutation(mutation flowBasketMutation) (string, []string, error) {
	name := strings.TrimSpace(mutation.Name)
	if name == "" || len(name) > 80 {
		return "", nil, errors.New("name must contain 1 to 80 bytes")
	}
	if len(mutation.Symbols) == 0 || len(mutation.Symbols) > maxFlowBasketSymbols {
		return "", nil, fmt.Errorf("symbols must contain 1 to %d US stocks", maxFlowBasketSymbols)
	}
	seen := map[string]struct{}{}
	symbols := make([]string, 0, len(mutation.Symbols))
	for _, raw := range mutation.Symbols {
		symbol, venue, err := market.NormalizeSymbol(raw)
		if err != nil {
			return "", nil, err
		}
		if venue != market.VenueUS {
			return "", nil, errors.New("flow baskets currently support US stocks only")
		}
		if _, ok := seen[symbol]; !ok {
			seen[symbol] = struct{}{}
			symbols = append(symbols, symbol)
		}
	}
	sort.Strings(symbols)
	return name, symbols, nil
}

func scanFlowBasket(scanner interface{ Scan(...any) error }) (flowBasket, error) {
	var item flowBasket
	var symbols string
	var created, updated int64
	if err := scanner.Scan(&item.ID, &item.Name, &symbols, &item.Revision, &created, &updated); err != nil {
		return item, err
	}
	if err := json.Unmarshal([]byte(symbols), &item.Symbols); err != nil {
		return item, err
	}
	item.CreatedAt = time.Unix(created, 0).UTC()
	item.UpdatedAt = time.Unix(updated, 0).UTC()
	return item, nil
}

func flowBasketCacheKey(basket flowBasket, spec market.DatasetSpec, identity string) (string, error) {
	raw, err := json.Marshal(struct {
		Version  string             `json:"version"`
		BasketID string             `json:"basket_id"`
		Revision int                `json:"revision"`
		Identity string             `json:"identity"`
		Spec     market.DatasetSpec `json:"spec"`
	}{Version: "massive-flow-basket-v1", BasketID: basket.ID, Revision: basket.Revision, Identity: identity, Spec: spec})
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]), nil
}

func (c *Cache) flowBasketResult(ctx context.Context, key string) (json.RawMessage, bool, bool, time.Time, error) {
	var raw []byte
	var complete int
	var updated int64
	err := c.db.QueryRowContext(ctx, `SELECT payload,complete,updated_at FROM flow_basket_results WHERE cache_key=?`, key).Scan(&raw, &complete, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, false, time.Time{}, nil
	}
	return json.RawMessage(raw), true, complete == 1, time.Unix(updated, 0), err
}

func (c *Cache) storeFlowBasketResult(ctx context.Context, key, basketID string, payload json.RawMessage, complete bool) error {
	_, err := c.db.ExecContext(ctx, `INSERT INTO flow_basket_results(cache_key,basket_id,payload,complete,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(cache_key) DO UPDATE SET basket_id=excluded.basket_id,payload=excluded.payload,complete=excluded.complete,updated_at=excluded.updated_at`, key, basketID, []byte(payload), boolNumber(complete), time.Now().Unix())
	return err
}

func (c *Cache) beginFlowBuild(key string) bool {
	c.flowMu.Lock()
	defer c.flowMu.Unlock()
	if _, ok := c.flowBuilds[key]; ok {
		return false
	}
	c.flowBuilds[key] = struct{}{}
	return true
}

func (c *Cache) endFlowBuild(key string) {
	c.flowMu.Lock()
	delete(c.flowBuilds, key)
	c.flowMu.Unlock()
}
