package localclient

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	localIndicatorTemplateVersion = 2
	maxLocalIndicators            = 50
	maxLocalEnabledIndicators     = 18
)

var (
	errLocalIndicatorNotFound = errors.New("indicator not found")
	errLocalIndicatorConflict = errors.New("indicator revision conflict")
	errLocalIndicatorLimit    = errors.New("local indicator limit reached")
	errLocalIndicatorEnabled  = errors.New("enabled indicator limit reached")
	errLocalIndicatorTemplate = errors.New("built-in template cannot be deleted")
	errLocalIndicatorName     = errors.New("indicator name already exists")
)

type localIndicatorParameter struct {
	Name    string  `json:"name"`
	Default float64 `json:"default"`
	Min     float64 `json:"min"`
	Max     float64 `json:"max"`
	Step    float64 `json:"step"`
	Value   float64 `json:"value"`
}

type localIndicator struct {
	ID          string                    `json:"id"`
	Kind        string                    `json:"kind"`
	TemplateKey string                    `json:"template_key,omitempty"`
	Name        string                    `json:"name"`
	Pane        string                    `json:"pane"`
	Formula     string                    `json:"formula"`
	Parameters  []localIndicatorParameter `json:"parameters"`
	Warnings    []string                  `json:"warnings,omitempty"`
	Enabled     bool                      `json:"enabled"`
	SortOrder   int                       `json:"sort_order"`
	Revision    int                       `json:"revision"`
	CreatedAt   time.Time                 `json:"created_at"`
	UpdatedAt   time.Time                 `json:"updated_at"`
}

type localIndicatorMutation struct {
	Name       string                    `json:"name"`
	Pane       string                    `json:"pane"`
	Formula    string                    `json:"formula"`
	Parameters []localIndicatorParameter `json:"parameters"`
	Warnings   []string                  `json:"warnings,omitempty"`
	Enabled    bool                      `json:"enabled"`
	SortOrder  int                       `json:"sort_order"`
	Revision   int                       `json:"revision,omitempty"`
}

var localDefaultIndicators = []struct {
	key, name, pane, formula string
	params                   []localIndicatorParameter
	sortOrder                int
}{
	{key: "ma-v1", name: "MA 均线", pane: "main", sortOrder: 10, formula: `MA5:MA(CLOSE,M1),COLORWHITE;
MA10:MA(CLOSE,M2),COLORYELLOW;
MA20:MA(CLOSE,M3),COLORMAGENTA;
MA60:MA(CLOSE,M4),COLORBLUE;`, params: []localIndicatorParameter{
		{Name: "M1", Default: 5, Min: 1, Max: 500, Step: 1, Value: 5}, {Name: "M2", Default: 10, Min: 1, Max: 500, Step: 1, Value: 10},
		{Name: "M3", Default: 20, Min: 1, Max: 500, Step: 1, Value: 20}, {Name: "M4", Default: 60, Min: 1, Max: 500, Step: 1, Value: 60},
	}},
	{key: "ema-v1", name: "EMA 均线", pane: "main", sortOrder: 20, formula: `EMA5:EMA(CLOSE,M1),COLORWHITE;
EMA10:EMA(CLOSE,M2),COLORYELLOW;
EMA20:EMA(CLOSE,M3),COLORCYAN;
EMA60:EMA(CLOSE,M4),COLORBLUE;`, params: []localIndicatorParameter{
		{Name: "M1", Default: 5, Min: 1, Max: 500, Step: 1, Value: 5}, {Name: "M2", Default: 10, Min: 1, Max: 500, Step: 1, Value: 10},
		{Name: "M3", Default: 20, Min: 1, Max: 500, Step: 1, Value: 20}, {Name: "M4", Default: 60, Min: 1, Max: 500, Step: 1, Value: 60},
	}},
	{key: "boll-v1", name: "BOLL 布林带", pane: "main", sortOrder: 30, formula: `MID:MA(CLOSE,N),COLORWHITE;
UPPER:MID+P*STD(CLOSE,N),COLORYELLOW;
LOWER:MID-P*STD(CLOSE,N),COLORBLUE;`, params: []localIndicatorParameter{
		{Name: "N", Default: 20, Min: 1, Max: 500, Step: 1, Value: 20}, {Name: "P", Default: 2, Min: .1, Max: 10, Step: .1, Value: 2},
	}},
	{key: "vol-v1", name: "VOL 成交量", pane: "sub", sortOrder: 40, formula: `VOLUME:VOL,VOLSTICK;
MAVOL5:MA(VOL,M1),COLORYELLOW;
MAVOL10:MA(VOL,M2),COLORCYAN;`, params: []localIndicatorParameter{
		{Name: "M1", Default: 5, Min: 1, Max: 500, Step: 1, Value: 5}, {Name: "M2", Default: 10, Min: 1, Max: 500, Step: 1, Value: 10},
	}},
	{key: "rsi-v1", name: "RSI 相对强弱", pane: "sub", sortOrder: 50, formula: `RSI6:RSI(CLOSE,N1),COLORWHITE;
RSI12:RSI(CLOSE,N2),COLORYELLOW;
RSI24:RSI(CLOSE,N3),COLORBLUE;`, params: []localIndicatorParameter{
		{Name: "N1", Default: 6, Min: 1, Max: 500, Step: 1, Value: 6}, {Name: "N2", Default: 12, Min: 1, Max: 500, Step: 1, Value: 12},
		{Name: "N3", Default: 24, Min: 1, Max: 500, Step: 1, Value: 24},
	}},
	{key: "kdj-v1", name: "KDJ 随机指标", pane: "sub", sortOrder: 60, formula: `RSV:=(CLOSE-LLV(LOW,N))/(HHV(HIGH,N)-LLV(LOW,N))*100;
K:SMA(RSV,M1,1),COLORWHITE;
D:SMA(K,M2,1),COLORYELLOW;
J:3*K-2*D,COLORMAGENTA;`, params: []localIndicatorParameter{
		{Name: "N", Default: 9, Min: 1, Max: 500, Step: 1, Value: 9}, {Name: "M1", Default: 3, Min: 1, Max: 500, Step: 1, Value: 3},
		{Name: "M2", Default: 3, Min: 1, Max: 500, Step: 1, Value: 3},
	}},
}

func (c *Cache) ensureLocalIndicators(ctx context.Context) error {
	var version int
	if err := c.db.QueryRowContext(ctx, `SELECT version FROM local_indicator_state WHERE id=1`).Scan(&version); err == nil && version == localIndicatorTemplateVersion {
		return nil
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	for _, template := range localDefaultIndicators {
		id, err := localIndicatorID()
		if err != nil {
			return err
		}
		params, _ := json.Marshal(template.params)
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO local_indicators(id,kind,template_key,name,pane,formula,parameters_json,enabled,sort_order,revision,created_at,updated_at) VALUES(?,'template',?,?,?,?,?,0,?,1,?,?)`, id, template.key, template.name, template.pane, template.formula, string(params), template.sortOrder, now, now); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE local_indicators SET name=?,pane=?,formula=? WHERE template_key=?`, template.name, template.pane, template.formula, template.key); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO local_indicator_state(id,version) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET version=excluded.version`, localIndicatorTemplateVersion); err != nil {
		return err
	}
	return tx.Commit()
}

func (c *Cache) importPrivateIndicators(ctx context.Context) error {
	path := filepath.Join(c.cfg.CacheDir, "private-indicators.json")
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(raw) > 1<<20 {
		return errors.New("private-indicators.json exceeds 1 MiB")
	}
	var document struct {
		Indicators []localIndicatorMutation `json:"indicators"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return fmt.Errorf("parse private-indicators.json: %w", err)
	}
	if err := c.ensureLocalIndicators(ctx); err != nil {
		return err
	}
	for _, mutation := range document.Indicators {
		if err := validateLocalIndicator(mutation); err != nil {
			return fmt.Errorf("private indicator %q: %w", mutation.Name, err)
		}
		params, _ := json.Marshal(mutation.Parameters)
		warnings, _ := json.Marshal(mutation.Warnings)
		id, err := localIndicatorID()
		if err != nil {
			return err
		}
		now := time.Now().Unix()
		if _, err := c.db.ExecContext(ctx, `INSERT INTO local_indicators(id,kind,name,pane,formula,parameters_json,warnings_json,enabled,sort_order,revision,created_at,updated_at) VALUES(?,'personal',?,?,?,?,?,?,?,1,?,?) ON CONFLICT(name) DO NOTHING`, id, strings.TrimSpace(mutation.Name), mutation.Pane, mutation.Formula, string(params), string(warnings), boolNumber(mutation.Enabled), mutation.SortOrder, now, now); err != nil {
			return err
		}
	}
	return nil
}

const localIndicatorColumns = `id,kind,template_key,name,pane,formula,parameters_json,warnings_json,enabled,sort_order,revision,created_at,updated_at`

func scanLocalIndicator(scanner interface{ Scan(...any) error }) (localIndicator, error) {
	var result localIndicator
	var templateKey sql.NullString
	var params, warnings string
	var enabled int
	var created, updated int64
	err := scanner.Scan(&result.ID, &result.Kind, &templateKey, &result.Name, &result.Pane, &result.Formula, &params, &warnings, &enabled, &result.SortOrder, &result.Revision, &created, &updated)
	if err != nil {
		return result, err
	}
	result.TemplateKey, result.Enabled = templateKey.String, enabled == 1
	result.CreatedAt, result.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	if err := json.Unmarshal([]byte(params), &result.Parameters); err != nil {
		return result, err
	}
	if err := json.Unmarshal([]byte(warnings), &result.Warnings); err != nil {
		return result, err
	}
	if result.Parameters == nil {
		result.Parameters = []localIndicatorParameter{}
	}
	return result, nil
}

func (c *Cache) LocalIndicators(ctx context.Context) ([]localIndicator, error) {
	if err := c.ensureLocalIndicators(ctx); err != nil {
		return nil, err
	}
	rows, err := c.db.QueryContext(ctx, `SELECT `+localIndicatorColumns+` FROM local_indicators ORDER BY sort_order,name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []localIndicator{}
	for rows.Next() {
		item, err := scanLocalIndicator(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (c *Cache) ResetLocalIndicatorDisplay(ctx context.Context) ([]localIndicator, error) {
	if err := c.ensureLocalIndicators(ctx); err != nil {
		return nil, err
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE local_indicators SET enabled=0,revision=revision+1,updated_at=? WHERE enabled=1`, time.Now().Unix()); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return c.LocalIndicators(ctx)
}

func (c *Cache) localIndicator(ctx context.Context, id string) (localIndicator, error) {
	item, err := scanLocalIndicator(c.db.QueryRowContext(ctx, `SELECT `+localIndicatorColumns+` FROM local_indicators WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return item, errLocalIndicatorNotFound
	}
	return item, err
}

func (c *Cache) CreateLocalIndicator(ctx context.Context, mutation localIndicatorMutation) (localIndicator, error) {
	if err := c.ensureLocalIndicators(ctx); err != nil {
		return localIndicator{}, err
	}
	if err := validateLocalIndicator(mutation); err != nil {
		return localIndicator{}, err
	}
	var count int
	if err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM local_indicators WHERE name=? COLLATE NOCASE`, strings.TrimSpace(mutation.Name)).Scan(&count); err != nil {
		return localIndicator{}, err
	}
	if count > 0 {
		return localIndicator{}, errLocalIndicatorName
	}
	if err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM local_indicators WHERE kind='personal'`).Scan(&count); err != nil {
		return localIndicator{}, err
	}
	if count >= maxLocalIndicators {
		return localIndicator{}, errLocalIndicatorLimit
	}
	if mutation.Enabled {
		if err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM local_indicators WHERE enabled=1`).Scan(&count); err != nil {
			return localIndicator{}, err
		}
		if count >= maxLocalEnabledIndicators {
			return localIndicator{}, errLocalIndicatorEnabled
		}
	}
	id, err := localIndicatorID()
	if err != nil {
		return localIndicator{}, err
	}
	params, _ := json.Marshal(mutation.Parameters)
	warnings, _ := json.Marshal(mutation.Warnings)
	now := time.Now().Unix()
	if _, err = c.db.ExecContext(ctx, `INSERT INTO local_indicators(id,kind,name,pane,formula,parameters_json,warnings_json,enabled,sort_order,revision,created_at,updated_at) VALUES(?,'personal',?,?,?,?,?,?,?,1,?,?)`, id, strings.TrimSpace(mutation.Name), mutation.Pane, mutation.Formula, string(params), string(warnings), boolNumber(mutation.Enabled), mutation.SortOrder, now, now); err != nil {
		return localIndicator{}, err
	}
	return c.localIndicator(ctx, id)
}

func (c *Cache) UpdateLocalIndicator(ctx context.Context, id string, mutation localIndicatorMutation) (localIndicator, error) {
	current, err := c.localIndicator(ctx, id)
	if err != nil {
		return localIndicator{}, err
	}
	if mutation.Revision != current.Revision {
		return localIndicator{}, errLocalIndicatorConflict
	}
	if current.Kind == "template" {
		mutation.Name, mutation.Pane, mutation.Formula = current.Name, current.Pane, current.Formula
	}
	if err := validateLocalIndicator(mutation); err != nil {
		return localIndicator{}, err
	}
	var count int
	if err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM local_indicators WHERE name=? COLLATE NOCASE AND id<>?`, strings.TrimSpace(mutation.Name), id).Scan(&count); err != nil {
		return localIndicator{}, err
	}
	if count > 0 {
		return localIndicator{}, errLocalIndicatorName
	}
	if mutation.Enabled && !current.Enabled {
		if err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM local_indicators WHERE enabled=1 AND id<>?`, id).Scan(&count); err != nil {
			return localIndicator{}, err
		}
		if count >= maxLocalEnabledIndicators {
			return localIndicator{}, errLocalIndicatorEnabled
		}
	}
	params, _ := json.Marshal(mutation.Parameters)
	warnings, _ := json.Marshal(mutation.Warnings)
	result, err := c.db.ExecContext(ctx, `UPDATE local_indicators SET name=?,pane=?,formula=?,parameters_json=?,warnings_json=?,enabled=?,sort_order=?,revision=revision+1,updated_at=? WHERE id=? AND revision=?`, strings.TrimSpace(mutation.Name), mutation.Pane, mutation.Formula, string(params), string(warnings), boolNumber(mutation.Enabled), mutation.SortOrder, time.Now().Unix(), id, mutation.Revision)
	if err != nil {
		return localIndicator{}, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return localIndicator{}, errLocalIndicatorConflict
	}
	return c.localIndicator(ctx, id)
}

func (c *Cache) DeleteLocalIndicator(ctx context.Context, id string, revision int) error {
	current, err := c.localIndicator(ctx, id)
	if err != nil {
		return err
	}
	if current.Kind == "template" {
		return errLocalIndicatorTemplate
	}
	result, err := c.db.ExecContext(ctx, `DELETE FROM local_indicators WHERE id=? AND revision=?`, id, revision)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return errLocalIndicatorConflict
	}
	return nil
}

func (c *Cache) CopyLocalIndicator(ctx context.Context, id, name string) (localIndicator, error) {
	current, err := c.localIndicator(ctx, id)
	if err != nil {
		return localIndicator{}, err
	}
	if strings.TrimSpace(name) == "" {
		name = current.Name + " 副本"
	}
	return c.CreateLocalIndicator(ctx, localIndicatorMutation{Name: name, Pane: current.Pane, Formula: current.Formula, Parameters: current.Parameters, Warnings: current.Warnings, Enabled: false, SortOrder: current.SortOrder + 1})
}

func validateLocalIndicator(m localIndicatorMutation) error {
	if strings.TrimSpace(m.Name) == "" || utf8.RuneCountInString(strings.TrimSpace(m.Name)) > 64 {
		return errors.New("indicator name must contain 1-64 characters")
	}
	if m.Pane != "main" && m.Pane != "sub" {
		return errors.New("indicator pane must be main or sub")
	}
	if strings.TrimSpace(m.Formula) == "" || len(m.Formula) > 64<<10 {
		return errors.New("indicator formula must contain 1-65536 bytes")
	}
	if len(m.Parameters) > 32 || len(m.Warnings) > 32 || m.SortOrder < 0 || m.SortOrder > 10000 {
		return errors.New("indicator limits are invalid")
	}
	seen := map[string]struct{}{}
	for _, parameter := range m.Parameters {
		name := strings.ToUpper(strings.TrimSpace(parameter.Name))
		if name == "" || utf8.RuneCountInString(name) > 64 {
			return errors.New("indicator parameter name is invalid")
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("duplicate indicator parameter %s", name)
		}
		seen[name] = struct{}{}
		if !finiteLocal(parameter.Default) || !finiteLocal(parameter.Min) || !finiteLocal(parameter.Max) || !finiteLocal(parameter.Step) || !finiteLocal(parameter.Value) || parameter.Min > parameter.Max || parameter.Step <= 0 || parameter.Default < parameter.Min || parameter.Default > parameter.Max || parameter.Value < parameter.Min || parameter.Value > parameter.Max {
			return fmt.Errorf("indicator parameter %s has an invalid range", name)
		}
	}
	return nil
}

func finiteLocal(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
func boolNumber(value bool) int {
	if value {
		return 1
	}
	return 0
}
func localIndicatorID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
