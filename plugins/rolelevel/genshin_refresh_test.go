package rolelevel

import "testing"

func TestGenshinRefreshRanges(t *testing.T) {
	const key = "genshinRefreshIntervalMinutes"
	for _, value := range []any{1, 10, 1440, int64(60), float64(5)} {
		if _, err := defaultCatalog.NormalizeConst(key, value); err != nil {
			t.Fatalf("有効な値を拒否: %v: %v", value, err)
		}
	}
	for _, value := range []any{0, 1441, 1.5, true, "10", nil} {
		if _, err := defaultCatalog.NormalizeConst(key, value); err == nil {
			t.Fatalf("不正な値を受理: %v", value)
		}
	}
	for _, test := range []struct {
		base, additional float64
		valid            bool
	}{
		{10, 1, true}, {10, -1, true}, {1, -1, false}, {1440, 1, false}, {0, 1, false},
	} {
		err := validateRangeRules([]PolicyRange{{Key: key, Type: RangeMultiplier, Start: 1, End: 5, Base: test.base, Additional: test.additional}}, defaultCatalog)
		if (err == nil) != test.valid {
			t.Fatalf("base=%v additional=%v err=%v", test.base, test.additional, err)
		}
	}
}
