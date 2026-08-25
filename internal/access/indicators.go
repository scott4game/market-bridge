package access

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var indicatorParameterName = regexp.MustCompile(`^[\pL_][\pL\pN_]*$`)

const (
	maxPersonalIndicators = 50
	maxEnabledIndicators  = 12
	maxIndicatorFormula   = 64 << 10
	maxIndicatorParams    = 32
)

var (
	ErrIndicatorNotFound = errors.New("indicator not found")
	ErrIndicatorConflict = errors.New("indicator revision conflict")
	ErrIndicatorLimit    = errors.New("personal indicator limit reached")
	ErrIndicatorEnabled  = errors.New("enabled indicator limit reached")
	ErrIndicatorTemplate = errors.New("built-in template cannot be deleted")
	ErrIndicatorName     = errors.New("indicator name already exists")
)

type IndicatorParameter struct {
	Name    string  `json:"name"`
	Default float64 `json:"default"`
	Min     float64 `json:"min"`
	Max     float64 `json:"max"`
	Step    float64 `json:"step"`
	Value   float64 `json:"value"`
}

type IndicatorDefinition struct {
	ID          string               `json:"id"`
	Kind        string               `json:"kind"`
	TemplateKey string               `json:"template_key,omitempty"`
	Name        string               `json:"name"`
	Pane        string               `json:"pane"`
	Formula     string               `json:"formula"`
	Parameters  []IndicatorParameter `json:"parameters"`
	Warnings    []string             `json:"warnings,omitempty"`
	Enabled     bool                 `json:"enabled"`
	SortOrder   int                  `json:"sort_order"`
	Revision    int                  `json:"revision"`
	CreatedAt   time.Time            `json:"created_at"`
	UpdatedAt   time.Time            `json:"updated_at"`
}

type IndicatorMutation struct {
	Name       string               `json:"name"`
	Pane       string               `json:"pane"`
	Formula    string               `json:"formula"`
	Parameters []IndicatorParameter `json:"parameters"`
	Warnings   []string             `json:"warnings,omitempty"`
	Enabled    bool                 `json:"enabled"`
	SortOrder  int                  `json:"sort_order"`
	Revision   int                  `json:"revision,omitempty"`
}

var defaultIndicators = []struct {
	key, name, pane, formula string
	params                   []IndicatorParameter
	sortOrder                int
}{
	{
		key: "nx-v1", name: "NX 牛熊分界线", pane: "main", sortOrder: 10,
		formula: `{'【NX（牛熊分界线）指标】'}
A:EMA(HIGH,BLUE_HIGH),COLORBLUE;
B:EMA(LOW,BLUE_LOW),COLORBLUE;
STICKLINE(C>A,A,B,0.1,1),COLORBLUE;
STICKLINE(C<B,A,B,0.1,1),COLORBLUE;
A1:EMA(HIGH,YELLOW_HIGH),COLORYELLOW;
B1:EMA(LOW,YELLOW_LOW),COLORYELLOW;
STICKLINE(C>A1,A1,B1,0.1,1),COLORYELLOW;
STICKLINE(C<B1,A1,B1,0.1,1),COLORYELLOW;`,
		params: []IndicatorParameter{
			{Name: "BLUE_HIGH", Default: 24, Min: 1, Max: 500, Step: 1, Value: 24},
			{Name: "BLUE_LOW", Default: 23, Min: 1, Max: 500, Step: 1, Value: 23},
			{Name: "YELLOW_HIGH", Default: 89, Min: 1, Max: 500, Step: 1, Value: 89},
			{Name: "YELLOW_LOW", Default: 90, Min: 1, Max: 500, Step: 1, Value: 90},
		},
	},
	{
		key: "mx-macd-v1", name: "MX MACD 背离", pane: "sub", sortOrder: 20,
		formula: `DIFF:EMA(CLOSE,S)-EMA(CLOSE,P),COLORFF8D1E;
DEA:EMA(DIFF,M),COLOR0CAEE6;
MACD:(DIFF-DEA)*2,COLORSTICK,COLORE970DC;
N1:=BARSLAST(REF(MACD,1)>=0 AND MACD<0);
M1:=BARSLAST(REF(MACD,1)<=0 AND MACD>0);
CC1:=LLV(CLOSE,N1+1); CC2:=REF(CC1,M1+1); CC3:=REF(CC2,M1+1);
DIFL1:=LLV(DIFF,N1+1); DIFL2:=REF(DIFL1,M1+1); DIFL3:=REF(DIFL2,M1+1);
AAA:=CC1<CC2 AND DIFL1>DIFL2 AND REF(MACD,1)<0 AND DIFF<0;
BBB:=CC1<CC3 AND DIFL1<DIFL2 AND DIFL1>DIFL3 AND REF(MACD,1)<0 AND DIFF<0;
CCC:=(AAA OR BBB) AND DIFF<0; JJJ:=REF(CCC,1) AND ABS(REF(DIFF,1))>=ABS(DIFF)*1.01;
DXDX:=REF(JJJ,1)=0 AND JJJ; DRAWTEXT(DXDX,DIFF/0.81,'B'),COLORRED,LINETHICK3;
CH1:=HHV(CLOSE,M1+1); CH2:=REF(CH1,N1+1); CH3:=REF(CH2,N1+1);
DIFH1:=HHV(DIFF,M1+1); DIFH2:=REF(DIFH1,N1+1); DIFH3:=REF(DIFH2,N1+1);
ZJDBL:=CH1>CH2 AND DIFH1<DIFH2 AND REF(MACD,1)>0 AND DIFF>0;
GXDBL:=CH1>CH3 AND DIFH1>DIFH2 AND DIFH1<DIFH3 AND REF(MACD,1)>0 AND DIFF>0;
DBBL:=(ZJDBL OR GXDBL) AND DIFF>0; DBJG:=REF(DBBL,1) AND REF(DIFF,1)>=DIFF*1.01;
DBJGXC:=REF(NOT(DBJG),1) AND DBJG; DRAWTEXT(DBJGXC,DIFF*1.31,'S'),COLORGREEN,LINETHICK3;`,
		params: []IndicatorParameter{
			{Name: "S", Default: 12, Min: 1, Max: 500, Step: 1, Value: 12},
			{Name: "P", Default: 26, Min: 1, Max: 500, Step: 1, Value: 26},
			{Name: "M", Default: 9, Min: 1, Max: 500, Step: 1, Value: 9},
		},
	},
}

func validateIndicatorMutation(m IndicatorMutation) error {
	if name := strings.TrimSpace(m.Name); name == "" || utf8.RuneCountInString(name) > 64 {
		return errors.New("indicator name must contain 1-64 characters")
	}
	if m.Pane != "main" && m.Pane != "sub" {
		return errors.New("indicator pane must be main or sub")
	}
	if strings.TrimSpace(m.Formula) == "" || len(m.Formula) > maxIndicatorFormula {
		return errors.New("indicator formula must contain 1-65536 bytes")
	}
	if len(m.Parameters) > maxIndicatorParams {
		return errors.New("indicator supports at most 32 parameters")
	}
	seen := map[string]struct{}{}
	for _, p := range m.Parameters {
		name := strings.ToUpper(strings.TrimSpace(p.Name))
		if utf8.RuneCountInString(name) > 64 || !indicatorParameterName.MatchString(name) {
			return errors.New("indicator parameter name is invalid")
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("duplicate indicator parameter %s", name)
		}
		seen[name] = struct{}{}
		if !finite(p.Default) || !finite(p.Min) || !finite(p.Max) || !finite(p.Step) || !finite(p.Value) || p.Min > p.Max || p.Step <= 0 || p.Default < p.Min || p.Default > p.Max || p.Value < p.Min || p.Value > p.Max {
			return fmt.Errorf("indicator parameter %s has an invalid range", name)
		}
	}
	if m.SortOrder < 0 || m.SortOrder > 10000 {
		return errors.New("indicator sort_order must be between 0 and 10000")
	}
	if len(m.Warnings) > 32 {
		return errors.New("indicator supports at most 32 warnings")
	}
	for _, warning := range m.Warnings {
		if utf8.RuneCountInString(warning) > 256 {
			return errors.New("indicator warning is too long")
		}
	}
	return nil
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func (s *Store) ensureDefaultIndicators(ctx context.Context, userID string) error {
	now := time.Now().Unix()
	for _, template := range defaultIndicators {
		id, err := randomHex(16)
		if err != nil {
			return err
		}
		params, _ := json.Marshal(template.params)
		_, err = s.db.ExecContext(ctx, `INSERT OR IGNORE INTO user_indicators(id,user_id,kind,template_key,name,pane,formula,parameters_json,enabled,sort_order,revision,created_at,updated_at) VALUES(?,?,'template',?,?,?,?,?,1,?,1,?,?)`, id, userID, template.key, template.name, template.pane, template.formula, string(params), template.sortOrder, now, now)
		if err != nil {
			return err
		}
		if _, err = s.db.ExecContext(ctx, `UPDATE user_indicators SET name=?,pane=?,formula=? WHERE user_id=? AND template_key=?`, template.name, template.pane, template.formula, userID, template.key); err != nil {
			return err
		}
	}
	return nil
}

const indicatorColumns = `id,kind,template_key,name,pane,formula,parameters_json,warnings_json,enabled,sort_order,revision,created_at,updated_at`

func scanIndicator(scanner interface{ Scan(...any) error }) (IndicatorDefinition, error) {
	var indicator IndicatorDefinition
	var templateKey sql.NullString
	var paramsJSON, warningsJSON string
	var enabled int
	var created, updated int64
	err := scanner.Scan(&indicator.ID, &indicator.Kind, &templateKey, &indicator.Name, &indicator.Pane, &indicator.Formula, &paramsJSON, &warningsJSON, &enabled, &indicator.SortOrder, &indicator.Revision, &created, &updated)
	if err != nil {
		return indicator, err
	}
	indicator.TemplateKey = templateKey.String
	indicator.Enabled = enabled == 1
	indicator.CreatedAt = time.Unix(created, 0).UTC()
	indicator.UpdatedAt = time.Unix(updated, 0).UTC()
	if err := json.Unmarshal([]byte(paramsJSON), &indicator.Parameters); err != nil {
		return indicator, err
	}
	if err := json.Unmarshal([]byte(warningsJSON), &indicator.Warnings); err != nil {
		return indicator, err
	}
	if indicator.Parameters == nil {
		indicator.Parameters = []IndicatorParameter{}
	}
	return indicator, nil
}

func (s *Store) Indicators(ctx context.Context, userID string) ([]IndicatorDefinition, error) {
	if err := s.ensureDefaultIndicators(ctx, userID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+indicatorColumns+` FROM user_indicators WHERE user_id=? ORDER BY sort_order,name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	indicators := []IndicatorDefinition{}
	for rows.Next() {
		indicator, err := scanIndicator(rows)
		if err != nil {
			return nil, err
		}
		indicators = append(indicators, indicator)
	}
	return indicators, rows.Err()
}

func (s *Store) indicator(ctx context.Context, userID, id string) (IndicatorDefinition, error) {
	indicator, err := scanIndicator(s.db.QueryRowContext(ctx, `SELECT `+indicatorColumns+` FROM user_indicators WHERE user_id=? AND id=?`, userID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return indicator, ErrIndicatorNotFound
	}
	return indicator, err
}

func (s *Store) CreateIndicator(ctx context.Context, userID string, m IndicatorMutation) (IndicatorDefinition, error) {
	if err := s.ensureDefaultIndicators(ctx, userID); err != nil {
		return IndicatorDefinition{}, err
	}
	if err := validateIndicatorMutation(m); err != nil {
		return IndicatorDefinition{}, err
	}
	if exists, err := s.indicatorNameExists(ctx, userID, m.Name, ""); err != nil {
		return IndicatorDefinition{}, err
	} else if exists {
		return IndicatorDefinition{}, ErrIndicatorName
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_indicators WHERE user_id=? AND kind='personal'`, userID).Scan(&count); err != nil {
		return IndicatorDefinition{}, err
	}
	if count >= maxPersonalIndicators {
		return IndicatorDefinition{}, ErrIndicatorLimit
	}
	if m.Enabled {
		if err := s.checkEnabledIndicatorLimit(ctx, userID, ""); err != nil {
			return IndicatorDefinition{}, err
		}
	}
	id, err := randomHex(16)
	if err != nil {
		return IndicatorDefinition{}, err
	}
	params, _ := json.Marshal(m.Parameters)
	warnings, _ := json.Marshal(m.Warnings)
	now := time.Now().Unix()
	_, err = s.db.ExecContext(ctx, `INSERT INTO user_indicators(id,user_id,kind,name,pane,formula,parameters_json,warnings_json,enabled,sort_order,revision,created_at,updated_at) VALUES(?,?,'personal',?,?,?,?,?,?,?,1,?,?)`, id, userID, strings.TrimSpace(m.Name), m.Pane, m.Formula, string(params), string(warnings), boolInt(m.Enabled), m.SortOrder, now, now)
	if err != nil {
		return IndicatorDefinition{}, err
	}
	return s.indicator(ctx, userID, id)
}

func (s *Store) UpdateIndicator(ctx context.Context, userID, id string, m IndicatorMutation) (IndicatorDefinition, error) {
	current, err := s.indicator(ctx, userID, id)
	if err != nil {
		return IndicatorDefinition{}, err
	}
	if m.Revision != current.Revision {
		return IndicatorDefinition{}, ErrIndicatorConflict
	}
	if current.Kind == "template" {
		m.Name, m.Pane, m.Formula = current.Name, current.Pane, current.Formula
	}
	if m.Enabled && !current.Enabled {
		if err := s.checkEnabledIndicatorLimit(ctx, userID, id); err != nil {
			return IndicatorDefinition{}, err
		}
	}
	if err := validateIndicatorMutation(m); err != nil {
		return IndicatorDefinition{}, err
	}
	if exists, err := s.indicatorNameExists(ctx, userID, m.Name, id); err != nil {
		return IndicatorDefinition{}, err
	} else if exists {
		return IndicatorDefinition{}, ErrIndicatorName
	}
	params, _ := json.Marshal(m.Parameters)
	warnings, _ := json.Marshal(m.Warnings)
	result, err := s.db.ExecContext(ctx, `UPDATE user_indicators SET name=?,pane=?,formula=?,parameters_json=?,warnings_json=?,enabled=?,sort_order=?,revision=revision+1,updated_at=? WHERE user_id=? AND id=? AND revision=?`, strings.TrimSpace(m.Name), m.Pane, m.Formula, string(params), string(warnings), boolInt(m.Enabled), m.SortOrder, time.Now().Unix(), userID, id, m.Revision)
	if err != nil {
		return IndicatorDefinition{}, err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return IndicatorDefinition{}, ErrIndicatorConflict
	}
	return s.indicator(ctx, userID, id)
}

func (s *Store) checkEnabledIndicatorLimit(ctx context.Context, userID, excludeID string) error {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_indicators WHERE user_id=? AND enabled=1 AND id<>?`, userID, excludeID).Scan(&count); err != nil {
		return err
	}
	if count >= maxEnabledIndicators {
		return ErrIndicatorEnabled
	}
	return nil
}

func (s *Store) indicatorNameExists(ctx context.Context, userID, name, excludeID string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_indicators WHERE user_id=? AND name=? COLLATE NOCASE AND id<>?`, userID, strings.TrimSpace(name), excludeID).Scan(&count)
	return count > 0, err
}

func (s *Store) DeleteIndicator(ctx context.Context, userID, id string, revision int) error {
	current, err := s.indicator(ctx, userID, id)
	if err != nil {
		return err
	}
	if current.Kind == "template" {
		return ErrIndicatorTemplate
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM user_indicators WHERE user_id=? AND id=? AND revision=?`, userID, id, revision)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return ErrIndicatorConflict
	}
	return nil
}

func (s *Store) CopyIndicator(ctx context.Context, userID, id, requestedName string) (IndicatorDefinition, error) {
	current, err := s.indicator(ctx, userID, id)
	if err != nil {
		return IndicatorDefinition{}, err
	}
	name := strings.TrimSpace(requestedName)
	if name == "" {
		base := current.Name + " 副本"
		name = base
		for suffix := 2; ; suffix++ {
			exists, checkErr := s.indicatorNameExists(ctx, userID, name, "")
			if checkErr != nil {
				return IndicatorDefinition{}, checkErr
			}
			if !exists {
				break
			}
			name = fmt.Sprintf("%s %d", base, suffix)
		}
	}
	return s.CreateIndicator(ctx, userID, IndicatorMutation{Name: name, Pane: current.Pane, Formula: current.Formula, Parameters: current.Parameters, Warnings: current.Warnings, Enabled: current.Enabled, SortOrder: current.SortOrder + 1})
}
