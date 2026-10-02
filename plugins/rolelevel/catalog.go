package rolelevel

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
)

// nativePolicyCatalogJSON is the plugin's own copy of the host's native policy schema.
//
// **embed するのは「実行時にファイルが手元に無い」ため。** pluginはバイナリに
// 組み込まれるのでJSONを外置するとビルド環境でしか読めない形になる。
//
// **二重管理になるので host側drift gate が必ず見る。**
// `internal/entitycompat/rolelevel_policy_catalog_test.go` がこのファイルを
// `effectivepolicy.Defaults()` と突き合わせる。`/plugins/` を直接importできないので
// JSONファイル越しに見るのは、そのgateが同じファイルを読めるようにするため。
//
//go:embed native_policy_catalog.json
var nativePolicyCatalogJSON []byte

// Kind is the native policy value type a key accepts.
type Kind string

const (
	KindBoolean   Kind = "boolean"
	KindNumber    Kind = "number"
	KindString    Kind = "string"
	KindStringSet Kind = "stringSet"
)

// Numeric reports whether multiplier ranges may target this kind. Only numbers
// have an arithmetic meaning; a boolean or an enum has no per-level scaling, and
// the host's own aggregation does not multiply them.
func (k Kind) Numeric() bool { return k == KindNumber }

type catalogEntry struct {
	Key  string   `json:"key"`
	Kind Kind     `json:"kind"`
	Enum []string `json:"enum,omitempty"`
}

// Catalog is the plugin's copy of the host's native policy schema.
type Catalog struct {
	entries []catalogEntry
	byKey   map[string]catalogEntry
}

// defaultCatalog is the decoded embedded catalog. A decode failure is a build
// defect, not an operational state, so it panics with a message naming the file
// (otherwise the catalog tests would never run).
var defaultCatalog = mustLoadCatalog()

func mustLoadCatalog() *Catalog {
	var doc struct {
		Keys []catalogEntry `json:"keys"`
	}
	if err := json.Unmarshal(nativePolicyCatalogJSON, &doc); err != nil {
		panic(fmt.Sprintf("rolelevel: native_policy_catalog.json を解釈できません: %v", err))
	}
	c := &Catalog{entries: doc.Keys, byKey: make(map[string]catalogEntry, len(doc.Keys))}
	for _, e := range doc.Keys {
		if _, dup := c.byKey[e.Key]; dup {
			panic(fmt.Sprintf("rolelevel: native_policy_catalog.json に %q が重複しています", e.Key))
		}
		c.byKey[e.Key] = e
	}
	return c
}

// Keys returns every native policy key the plugin may touch, sorted.
func (c *Catalog) Keys() []string {
	out := make([]string, 0, len(c.entries))
	for _, e := range c.entries {
		out = append(out, e.Key)
	}
	slices.Sort(out)
	return out
}

// Kind reports the native type of key.
func (c *Catalog) Kind(key string) (Kind, bool) {
	e, ok := c.byKey[key]
	if !ok {
		return "", false
	}
	return e.Kind, true
}

// NumericKey reports whether key accepts a multiplier range.
func (c *Catalog) NumericKey(key string) bool {
	kind, ok := c.Kind(key)
	return ok && kind.Numeric()
}

// NormalizeConst converts a JSON-decoded constant into the Go value the host
// expects for key, rejecting anything the host's own contribution validation
// would reject.
//
// 数値はhostと同じく有限なint範囲内なら小数も受理する。rateLimitFactorなどは
// 小数にも意味があり、hostの集約もfloat64のまま保持する。
func (c *Catalog) NormalizeConst(key string, value any) (any, error) {
	e, ok := c.byKey[key]
	if !ok {
		return nil, invalid(CodeUnknownPolicyKey, "policyRanges.key",
			"%q はネイティブの policy key ではありません", key)
	}
	switch e.Kind {
	case KindBoolean:
		b, isBool := value.(bool)
		if !isBool {
			return nil, invalid(CodeInvalidRangeValue, "policyRanges.value",
				"%q は boolean ですが %#v が渡されました", key, value)
		}
		return b, nil
	case KindNumber:
		n, err := asNativeNumber(value)
		if err != nil {
			return nil, invalid(CodeInvalidRangeValue, "policyRanges.value",
				"%q は数値ですが %#v が渡されました (%s)", key, value, err)
		}
		if key == "genshinRefreshIntervalMinutes" {
			var minutes float64
			switch v := n.(type) {
			case int64:
				minutes = float64(v)
			case float64:
				minutes = v
			default:
				return nil, invalid(CodeInvalidRangeValue, "policyRanges.value", "原神の取得間隔は1〜1440分の整数で指定してください")
			}
			if minutes < 1 || minutes > 1440 || math.Trunc(minutes) != minutes {
				return nil, invalid(CodeInvalidRangeValue, "policyRanges.value", "原神の取得間隔は1〜1440分の整数で指定してください")
			}
		}
		return n, nil
	case KindString:
		s, isStr := value.(string)
		if !isStr {
			return nil, invalid(CodeInvalidRangeValue, "policyRanges.value",
				"%q は文字列ですが %#v が渡されました", key, value)
		}
		if len(e.Enum) > 0 && !slices.Contains(e.Enum, s) {
			return nil, invalid(CodeInvalidRangeValue, "policyRanges.value",
				"%q の値 %q は許列表にありません (%s)", key, s, strings.Join(e.Enum, ", "))
		}
		return s, nil
	case KindStringSet:
		items, err := asStringSlice(value)
		if err != nil {
			return nil, invalid(CodeInvalidRangeValue, "policyRanges.value",
				"%q は文字列の配列ですが %#v が渡されました (%s)", key, value, err)
		}
		return items, nil
	}
	return nil, invalid(CodeUnknownPolicyKey, "policyRanges.key", "%q の kind が不正です", key)
}

// AcceptsValue reports whether value is usable as a native policy value for key.
func (c *Catalog) AcceptsValue(key string, value any) bool {
	_, err := c.NormalizeConst(key, value)
	return err == nil
}

// nativeNumber validates a computed multiplier value against the host's native
// numeric policy range. Fractional values remain fractional.
func nativeNumber(v float64) (any, error) {
	if err := validateNativeNumber(v); err != nil {
		return nil, invalid(CodeInvalidRangeValue, "policyRanges", "計算結果が不正です (%s)", err)
	}
	return v, nil
}

// asNativeNumber accepts the shapes a JSON number or a Go number arrives in.
func asNativeNumber(value any) (any, error) {
	switch v := value.(type) {
	case int:
		return int64(v), nil
	case int64:
		if v < int64(math.MinInt) || v > int64(math.MaxInt) {
			return nil, fmt.Errorf("int の範囲外です")
		}
		return v, nil
	case float64:
		if err := validateNativeNumber(v); err != nil {
			return nil, err
		}
		if v == math.Trunc(v) {
			return int64(v), nil
		}
		return v, nil
	case json.Number:
		n, err := v.Float64()
		if err != nil {
			return nil, err
		}
		return asNativeNumber(n)
	}
	return nil, fmt.Errorf("数値ではありません")
}

func validateNativeNumber(v float64) error {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return fmt.Errorf("有限値ではありません")
	}
	minInclusive := float64(math.MinInt)
	maxExclusive := -minInclusive
	if v < minInclusive || v >= maxExclusive {
		return fmt.Errorf("int の範囲外です")
	}
	return nil
}

// asStringSlice accepts []string and the []any that encoding/json produces.
func asStringSlice(value any) ([]string, error) {
	switch v := value.(type) {
	case []string:
		for _, item := range v {
			if strings.TrimSpace(item) == "" {
				return nil, fmt.Errorf("空白だけの要素があります")
			}
		}
		return v, nil
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("要素が文字列ではありません")
			}
			if strings.TrimSpace(s) == "" {
				return nil, fmt.Errorf("空白だけの要素があります")
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, fmt.Errorf("配列ではありません")
}
