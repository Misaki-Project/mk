package role

import "testing"

func TestGenshinRefreshAggregation(t *testing.T) {
	const key = "genshinRefreshIntervalMinutes"
	for _, test := range []struct {
		values []any
		want   int
	}{
		{nil, 10},
		{[]any{60, 5, int64(30)}, 5},
		{[]any{60, 120}, 60},
		{[]any{float64(1), 1440}, 1},
		{[]any{0, 1441, 1.5, "2"}, 10},
	} {
		if got := aggregatePolicyValues(key, 10, test.values); got != test.want {
			t.Errorf("values=%v got=%v want=%v", test.values, got, test.want)
		}
	}
	if got := aggregatePolicyValues("mentionLimit", 20, []any{60, 5}); got != 60 {
		t.Fatalf("既存の数値policyのmax集約を変更した: %v", got)
	}
}
