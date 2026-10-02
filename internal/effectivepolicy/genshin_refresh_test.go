package effectivepolicy

import (
	"math"
	"testing"
)

func TestGenshinRefreshIntervalBounds(t *testing.T) {
	const key = "genshinRefreshIntervalMinutes"
	if Defaults()[key] != 10 {
		t.Fatal("原神の既定取得間隔は10分")
	}
	for _, value := range []any{1, 10, 1440, int64(60), float64(5)} {
		if !ValidatePolicyValue(key, value) {
			t.Errorf("有効な値を拒否: %v", value)
		}
	}
	for _, value := range []any{0, -1, 1441, 1.5, "10", true, nil, math.NaN(), math.Inf(1), int64(math.MaxInt64)} {
		if ValidatePolicyValue(key, value) {
			t.Errorf("不正な値を許可: %v", value)
		}
	}
}
