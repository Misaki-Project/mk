# CherryPick Level Role Backend Engine・Policy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** CherryPickのlevel計算・policy interpolation・experience更新をmk-goの`internal/core/role`へ副作用を持たないcomponentとして移植し、`set`/`add`/`multiplier`更新を1 transactionでlost updateなしに適用し、cache invalidationと内部eventを発行できるようにする。

**Architecture:** level計算は`internal/core/role/level.go`の純粋関数`EvaluateLevel`へ分離し、CherryPickの`evalRoleLevel`/`calculateExponentialSum`/`calculateLinearSum`をそのまま移植する。experience更新は`ChangeExperience`を`SetDB`経由のGORM transaction（`SELECT ... FOR UPDATE`）で行い、commit後にper-user cacheをinvalidateし、immutable payloadで内部eventをpublishする。policy interpolationは`rolePolicyOverride`へ`policyAsLevel`を追加し、`GetUserPoliciesChecked`がmanualLevel roleのlevel連動値を解決してから既存のpriority集約へ流す。不正なpolicy固有型は`ErrInvalidLevelPolicy`でrequestを失敗させる。

**Tech Stack:** Go 1.26、GORM（`clause.Locking`）、`math`、`gorm.io/datatypes`、`slog`、`internal/misc/id`

## Global Constraints

- 保証対象は最終構成の`mk-go + Misaki-Project frontend`のみ。cross-combinationは保証しない。
- CherryPickの計算式と挙動をそのまま移植する（`calculateLinearSum`、`calculateExponentialSum`、`evalRoleLevel`の二分探索・境界処理）。specが明示的に分岐する場合のみ変更する。
- experienceはDB `bigint`で扱い、外部APIは`0..Number.MAX_SAFE_INTEGER`（9007199254740991）へclampする。clampは`Math.floor`後、`[0, MAX_SAFE_INTEGER]`へ。
- overflow、NaN、負値、不正係数は受け入れずfail-closedにする（`EvaluateLevel`はerror返却、`ChangeExperience`は`ErrInvalidExperienceValue`）。
- 更新は1 transactionで行う。roleとassignment rowを`FOR UPDATE`でlockし、lost updateを防ぐ。commit後にcacheをinvalidateする。
- event payloadはmodel pointerを共有せず、更新後の値のコピー（value struct）だけを渡す。
- policyAsLevelの型検証はpolicy固有型（bool / number / string）に従う。不正値はそのroleの寄与を無視せず固定errorで失敗させる。
- 複数roleが同じpolicyへ寄与する場合は既存のpriority規則を維持し、同priorityの決定順序はGo map iterationへ依存させない（role順sliceで集約）。
- 本番由来ID・値・件数・hash・pathをfixture、test、report、commitへ出さない。golden caseはCherryPick実装から導出したsynthetic値のみ。
- 本番導出のerror IDは使わない。engine/policyのerrorはGo sentinel errorと固定category文字列のみ。
- 内部role modelはplugin公開面へ露出しない（`shiroha-a/mk#2585`は将来のplugin化提案。本互換実装はcore機能として進め、plugin公開APIは追加しない）。
- commit・PR作成・push・mergeはユーザーの明示的な承認がある場合のみ実行する（リポジトリ規約: Claudeはコミットを自動作成しない）。承認前は検証とステージングに留める。remote設定変更・frontend submodule変更は行わない。
- commit前には`make fmt && make lint`を通すこと。
- 不正level policyはどのrequest pathでもfail-closedにする。base fallback（silentにdefaultへ倒す）は禁止する。`GetUserPoliciesChecked`（API/entity経路）は`ErrInvalidLevelPolicy`でrequestを失敗させ、`GetUserPolicies`（gate経路、errorを返せない）は不正keyをdeny値（bool→false、number→0、string→""）へ倒す。返すerrorに識別子（role ID・user ID・policy key）を含めない。
- 不正level policyが永続化されないよう、admin create/updateのwrite-time検証とpreflightの構造検証で防ぐ（Plan 3 Task 1とPlan 1 Task 3）。runtimeのdeny/error分岐は検証漏れ・corruptionに対する防御。

---

### Task 1: 副作用を持たないlevel engineを実装する

**Files:**
- Create: `internal/core/role/level.go`
- Create: `internal/core/role/level_test.go`

**Interfaces:**
- Produces: `type LevelPolicyInput struct { Level int; Type string; Base float64; Additional float64; Exponential float64 }`
- Produces: `type LevelResult struct { Level int; CurrentExp int64; NextLevelExp *int64; TotalExp int64; MinLevel int; MaxLevel int }`
- Produces: `func EvaluateLevel(experience int64, baseLevel int, policies []LevelPolicyInput) (LevelResult, error)`
- Produces: `var ErrInvalidLevelInput = errors.New(...)`、`var ErrLevelOverflow = errors.New(...)`
- Consumes: `model.ExperiencePolicyType`定数（Plan 1 Task 1）。
- Consumers: Task 2の`ChangeExperience`（level不変条件の確認なしで利用）、Task 3の`applyPolicyAsLevel`、Plan 3のuser entity role view。

- [ ] **Step 1: 失敗するtestを書く**

`internal/core/role/level_test.go`を新規作成する。golden caseはCherryPick `RoleService.ts` の`evalRoleLevel`をsynthetic値で導出したもの（本番由来値は使わない）。`nextLevelExp`が最大levelではnil（CherryPickの`NaN`→JSON `null`に相当）であることを含む。

```go
package role

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func int64p(v int64) *int64 { return &v }

func TestEvaluateLevel_GoldenCases(t *testing.T) {
	constPolicy := func(level int, base float64) LevelPolicyInput {
		return LevelPolicyInput{Level: level, Type: "const", Base: base}
	}
	linearPolicy := func(level int, base, additional float64) LevelPolicyInput {
		return LevelPolicyInput{Level: level, Type: "linear", Base: base, Additional: additional}
	}
	expPolicy := func(level int, base, additional, exponential float64) LevelPolicyInput {
		return LevelPolicyInput{Level: level, Type: "exponential", Base: base, Additional: additional, Exponential: exponential}
	}

	tests := []struct {
		name           string
		experience     int64
		baseLevel      int
		policies       []LevelPolicyInput
		wantLevel      int
		wantCurrentExp int64
		wantNext       *int64
		wantMin        int
		wantMax        int
	}{
		{
			// const: base=100/level を 5 level。exp=250 → level 2 の途中。
			name: "const partial", experience: 250, baseLevel: 0,
			policies: []LevelPolicyInput{constPolicy(5, 100)},
			wantLevel: 2, wantCurrentExp: 50, wantNext: int64p(100), wantMin: 0, wantMax: 5,
		},
		{
			// linear: base=100 + additional=50。sum(L)=100L+25L(L-1)。
			// exp=700 → sum(4)=700 でちょうど level 4。
			name: "linear exact boundary", experience: 700, baseLevel: 0,
			policies: []LevelPolicyInput{linearPolicy(5, 100, 50)},
			wantLevel: 4, wantCurrentExp: 0, wantNext: int64p(300), wantMin: 0, wantMax: 5,
		},
		{
			// linear で exp=749 → level 4 の currentExp=49, next=300。
			name: "linear inside level", experience: 749, baseLevel: 0,
			policies: []LevelPolicyInput{linearPolicy(5, 100, 50)},
			wantLevel: 4, wantCurrentExp: 49, wantNext: int64p(300), wantMin: 0, wantMax: 5,
		},
		{
			// exponential: base=100, additional=50, exponential=2。
			// sum(L)=100L+50(2^L-1)。exp=650 → sum(3)=650 で level 3。
			name: "exponential boundary", experience: 650, baseLevel: 0,
			policies: []LevelPolicyInput{expPolicy(4, 100, 50, 2)},
			wantLevel: 3, wantCurrentExp: 0, wantNext: int64p(500), wantMin: 0, wantMax: 4,
		},
		{
			// 複数 policy 境界: const(2,100) 完走後 linear(3,200,100)。
			// exp=700 → level=2+2=4, next=400。
			name: "multi policy boundary", experience: 700, baseLevel: 0,
			policies: []LevelPolicyInput{
				constPolicy(2, 100),
				linearPolicy(3, 200, 100),
			},
			wantLevel: 4, wantCurrentExp: 0, wantNext: int64p(400), wantMin: 0, wantMax: 5,
		},
		{
			// 最大 level: 全 policy を完走すると nextLevelExp は nil。
			name: "max level no next", experience: 1100, baseLevel: 0,
			policies: []LevelPolicyInput{
				constPolicy(2, 100),
				linearPolicy(3, 200, 100),
			},
			wantLevel: 5, wantCurrentExp: 0, wantNext: nil, wantMin: 0, wantMax: 5,
		},
		{
			// baseLevel が 0 でない場合。空 policy → level=baseLevel。
			name: "empty policies base level", experience: 42, baseLevel: 3,
			policies: []LevelPolicyInput{},
			wantLevel: 3, wantCurrentExp: 42, wantNext: nil, wantMin: 3, wantMax: 3,
		},
		{
			// baseLevel 4, const(2,100)。exp=250 → baseLevel+2=6。
			name: "base level offset", experience: 250, baseLevel: 4,
			policies: []LevelPolicyInput{constPolicy(5, 100)},
			wantLevel: 6, wantCurrentExp: 50, wantNext: int64p(100), wantMin: 4, wantMax: 9,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := EvaluateLevel(tt.experience, tt.baseLevel, tt.policies)
			require.NoError(t, err)
			assert.Equal(t, tt.wantLevel, got.Level)
			assert.Equal(t, tt.wantCurrentExp, got.CurrentExp)
			assert.Equal(t, tt.wantNext, got.NextLevelExp)
			assert.Equal(t, tt.experience, got.TotalExp)
			assert.Equal(t, tt.wantMin, got.MinLevel)
			assert.Equal(t, tt.wantMax, got.MaxLevel)
		})
	}
}

func TestEvaluateLevel_InvalidInputFailsClosed(t *testing.T) {
	cases := []struct {
		name      string
		experience int64
		baseLevel int
		policies  []LevelPolicyInput
		wantErr   error
	}{
		{name: "negative experience", experience: -1, wantErr: ErrInvalidLevelInput},
		{name: "negative base level", experience: 0, baseLevel: -1, wantErr: ErrInvalidLevelInput},
		{name: "unknown type", experience: 10, baseLevel: 0,
			policies: []LevelPolicyInput{{Level: 1, Type: "sqrt", Base: 10}}, wantErr: ErrInvalidLevelInput},
		{name: "zero policy level", experience: 10, baseLevel: 0,
			policies: []LevelPolicyInput{{Level: 0, Type: "const", Base: 10}}, wantErr: ErrInvalidLevelInput},
		{name: "negative const base", experience: 10, baseLevel: 0,
			policies: []LevelPolicyInput{{Level: 1, Type: "const", Base: -5}}, wantErr: ErrInvalidLevelInput},
		{name: "linear missing additional", experience: 10, baseLevel: 0,
			policies: []LevelPolicyInput{{Level: 1, Type: "linear", Base: 5}}, wantErr: ErrInvalidLevelInput},
		{name: "exponential non-positive ratio", experience: 10, baseLevel: 0,
			policies: []LevelPolicyInput{{Level: 1, Type: "exponential", Base: 5, Additional: 1, Exponential: 0}}, wantErr: ErrInvalidLevelInput},
		{name: "exponential overflow", experience: 9007199254740991, baseLevel: 0,
			policies: []LevelPolicyInput{{Level: 400, Type: "exponential", Base: 1000, Additional: 1000, Exponential: 10}},
			wantErr: ErrLevelOverflow},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := EvaluateLevel(tt.experience, tt.baseLevel, tt.policies)
			require.Error(t, err)
			assert.True(t, errors.Is(err, tt.wantErr),
				"expected %v, got %v", tt.wantErr, err)
		})
	}
}
```

- [ ] **Step 2: testがREDであることを確認する**

Run: `go test ./internal/core/role -run TestEvaluateLevel -count=1`

Expected: `undefined: EvaluateLevel`でFAILする。他のexisting testに影響を与えないことを確認する。

- [ ] **Step 3: 最小level engineを実装する**

`internal/core/role/level.go`を新規作成する。CherryPickの`evalRoleLevel`をそのまま移植し、overflow/NaN/不正係数をfail-closedにする。

```go
package role

import (
	"errors"
	"fmt"
	"math"

	"github.com/shiroha-a/mk/internal/model"
)

// ErrInvalidLevelInput is returned when a level evaluation input violates
// the documented bounds (negative experience, unknown type, out-of-range
// coefficients). Level evaluation is fail-closed: nothing is computed.
var ErrInvalidLevelInput = errors.New("level engine: invalid input")

// ErrLevelOverflow is returned when an exponential accumulation overflows
// float64. The result would be meaningless, so the evaluation fails closed.
var ErrLevelOverflow = errors.New("level engine: exponential overflow")

// LevelPolicyInput is one ordered experience policy. Type is one of
// model.ExperiencePolicyType (const / linear / exponential).
type LevelPolicyInput struct {
	Level       int
	Type        string
	Base        float64
	Additional  float64
	Exponential float64
}

// LevelResult is the outcome of EvaluateLevel.
//
// NextLevelExp is nil when the user is at max level or the role has no
// policies (CherryPick returns Number.NaN there, which JSON-serialises to
// null). TotalExp is the input experience (= the assignment's experience).
type LevelResult struct {
	Level        int
	CurrentExp   int64
	NextLevelExp *int64
	TotalExp     int64
	MinLevel     int
	MaxLevel     int
}

// validateLevelPolicies rejects out-of-range coefficients before any math.
// Bound ranges are shared with the migration preflight (Plan 1 Task 3):
//   - level: 1..MaxInt64 の整数
//   - base:   0 <= x < 1.7976931348623157e308 (finite float64)
//   - additional: 0 <= x < 1.7976931348623157e308 (finite float64)
//   - exponential: 0 < x < 1.7976931348623157e308 (finite float64, 1 allowed)
//
// base=0 は有効 (累積 0)。exponential==1 は calculateExponentialSum が
// (base+additional)*level に特化し、0 除算を回避する (公式の分母
// (1-exponential) は exponential==1 でのみ 0 になる)。JSON は NaN/Inf を
// 持たないため、float64 表現不能な巨大値は Unmarshal が拒否する。
func validateLevelPolicies(policies []LevelPolicyInput) error {
	for i, p := range policies {
		switch model.ExperiencePolicyType(p.Type) {
		case model.ExperiencePolicyConst, model.ExperiencePolicyLinear, model.ExperiencePolicyExponential:
		default:
			return fmt.Errorf("%w: policy[%d] type %q", ErrInvalidLevelInput, i, p.Type)
		}
		if p.Level < 1 {
			return fmt.Errorf("%w: policy[%d] level %d", ErrInvalidLevelInput, i, p.Level)
		}
		if math.IsNaN(p.Base) || math.IsInf(p.Base, 0) || p.Base < 0 {
			return fmt.Errorf("%w: policy[%d] base", ErrInvalidLevelInput, i)
		}
		switch model.ExperiencePolicyType(p.Type) {
		case model.ExperiencePolicyLinear:
			if math.IsNaN(p.Additional) || math.IsInf(p.Additional, 0) || p.Additional < 0 {
				return fmt.Errorf("%w: policy[%d] additional", ErrInvalidLevelInput, i)
			}
		case model.ExperiencePolicyExponential:
			if math.IsNaN(p.Additional) || math.IsInf(p.Additional, 0) || p.Additional < 0 {
				return fmt.Errorf("%w: policy[%d] additional", ErrInvalidLevelInput, i)
			}
			if math.IsNaN(p.Exponential) || math.IsInf(p.Exponential, 0) || p.Exponential <= 0 {
				return fmt.Errorf("%w: policy[%d] exponential", ErrInvalidLevelInput, i)
			}
		}
	}
	return nil
}

// EvaluateLevel computes the current level and experience progress for an
// assignment. It is a pure function: no IO, no caching, no side effects.
//
// Port of CherryPick RoleService.evalRoleLevel. The binary search over diff
// and the per-policy accumulation mirror the TS code exactly; only the
// fail-closed overflow / NaN checks are added (spec: overflow・NaN・負値・
// 不正係数は受け入れず fail-closed にする)。
func EvaluateLevel(experience int64, baseLevel int, policies []LevelPolicyInput) (LevelResult, error) {
	if experience < 0 {
		return LevelResult{}, fmt.Errorf("%w: negative experience", ErrInvalidLevelInput)
	}
	if baseLevel < 0 {
		return LevelResult{}, fmt.Errorf("%w: negative base level", ErrInvalidLevelInput)
	}
	if err := validateLevelPolicies(policies); err != nil {
		return LevelResult{}, err
	}

	maxLevel := baseLevel
	for _, p := range policies {
		maxLevel += p.Level
	}

	if len(policies) == 0 {
		return LevelResult{
			Level: baseLevel, CurrentExp: experience, NextLevelExp: nil,
			TotalExp: experience, MinLevel: baseLevel, MaxLevel: maxLevel,
		}, nil
	}

	exp := float64(experience)
	level := 0
	totalExp := 0.0

	for _, policy := range policies {
		if policy.Level <= 0 {
			continue
		}
		max := policy.Level
		diff := max
		estLevel := 0
		currentExp := exp - totalExp

		for diff > 0 {
			switch model.ExperiencePolicyType(policy.Type) {
			case model.ExperiencePolicyConst:
				if policy.Base*float64(policy.Level) <= currentExp {
					totalExp += policy.Base * float64(policy.Level)
					level += policy.Level
				} else {
					nextLevel := int64(math.Floor(currentExp / policy.Base))
					currentLevelExp := int64(math.Floor(exp - totalExp - policy.Base*float64(nextLevel)))
					next := int64(math.Floor(policy.Base))
					return LevelResult{
						Level: baseLevel + level + int(nextLevel),
						CurrentExp: currentLevelExp, NextLevelExp: &next,
						TotalExp: experience, MinLevel: baseLevel, MaxLevel: maxLevel,
					}, nil
				}
			case model.ExperiencePolicyLinear:
				if currentExp >= calculateLinearSum(policy, estLevel+diff) {
					estLevel += diff
				}
			case model.ExperiencePolicyExponential:
				sum, err := calculateExponentialSum(policy, estLevel+diff)
				if err != nil {
					return LevelResult{}, err
				}
				if currentExp >= sum {
					estLevel += diff
				}
			}

			if policy.Type == string(model.ExperiencePolicyConst) {
				break
			}

			if estLevel == max {
				switch model.ExperiencePolicyType(policy.Type) {
				case model.ExperiencePolicyLinear:
					totalExp += calculateLinearSum(policy, max)
					level += policy.Level
				case model.ExperiencePolicyExponential:
					sum, err := calculateExponentialSum(policy, max)
					if err != nil {
						return LevelResult{}, err
					}
					totalExp += sum
					level += policy.Level
				}
				break
			}

			if diff != 1 {
				diff = (diff + 1) / 2
				continue
			}

			// diff == 1: current level is within this policy.
			switch model.ExperiencePolicyType(policy.Type) {
			case model.ExperiencePolicyLinear:
				current := totalExp + calculateLinearSum(policy, estLevel)
				next := int64(math.Floor(math.Mod(current, 1) +
					policy.Base + policy.Additional*float64(estLevel)))
				return LevelResult{
					Level: baseLevel + level + estLevel,
					CurrentExp: int64(math.Floor(exp - current)), NextLevelExp: &next,
					TotalExp: experience, MinLevel: baseLevel, MaxLevel: maxLevel,
				}, nil
			case model.ExperiencePolicyExponential:
				currentSum, err := calculateExponentialSum(policy, estLevel)
				if err != nil {
					return LevelResult{}, err
				}
				current := totalExp + currentSum
				pow, err := checkedPow(policy.Exponential, float64(estLevel))
				if err != nil {
					return LevelResult{}, err
				}
				next := int64(math.Floor(math.Mod(current, 1) +
					policy.Base + policy.Additional*pow))
				return LevelResult{
					Level: baseLevel + level + estLevel,
					CurrentExp: int64(math.Floor(exp - current)), NextLevelExp: &next,
					TotalExp: experience, MinLevel: baseLevel, MaxLevel: maxLevel,
				}, nil
			}
		}
	}

	return LevelResult{
		Level: baseLevel + level, CurrentExp: int64(math.Floor(exp - totalExp)),
		NextLevelExp: nil, TotalExp: experience, MinLevel: baseLevel, MaxLevel: maxLevel,
	}, nil
}

// calculateLinearSum mirrors CherryPick RoleService.calculateLinearSum:
// base*level + additional*level*(level-1)/2.
func calculateLinearSum(p LevelPolicyInput, level int) float64 {
	return p.Base*float64(level) +
		p.Additional*float64(level)*float64(level-1)/2
}

// calculateExponentialSum mirrors CherryPick RoleService.calculateExponentialSum:
// exponential==1 → (base+additional)*level, else
// base*level + additional*(1-exponential^level)/(1-exponential).
func calculateExponentialSum(p LevelPolicyInput, level int) (float64, error) {
	if p.Exponential == 1 {
		return (p.Base + p.Additional) * float64(level), nil
	}
	pow, err := checkedPow(p.Exponential, float64(level))
	if err != nil {
		return 0, err
	}
	sum := p.Base*float64(level) +
		p.Additional*(1-pow)/(1-p.Exponential)
	if math.IsNaN(sum) || math.IsInf(sum, 0) {
		return 0, fmt.Errorf("%w: level %d", ErrLevelOverflow, level)
	}
	return sum, nil
}

// checkedPow returns math.Pow(b, e) but fails closed on overflow.
func checkedPow(b, e float64) (float64, error) {
	p := math.Pow(b, e)
	if math.IsInf(p, 0) || math.IsNaN(p) {
		return 0, fmt.Errorf("%w: pow(%v, %v)", ErrLevelOverflow, b, e)
	}
	return p, nil
}
```

- [ ] **Step 4: testがGREENであることを確認する**

Run: `go test ./internal/core/role -run TestEvaluateLevel -count=1`

Expected: 全golden caseとfail-closed caseがPASSする。`TestEvaluateLevel_InvalidInputFailsClosed`の"exponential overflow"は`EvaluateLevel`が`ErrLevelOverflow`を返すこと。

- [ ] **Step 5: 既存packageのnon-regressionを確認する**

Run: `go test ./internal/core/role -count=1`

Expected: 既存role testが全てPASSする（level engineは新しいfileのみで既存呼び出しに影響しない）。

- [ ] **Step 6: commit**（ユーザー承認後のみ実行する）

リポジトリ規約に従い、commitはユーザーの明示的な指示がある場合のみ実行する。
```powershell
git add -- internal/core/role/level.go internal/core/role/level_test.go
git diff --cached --check
git commit -m "role: CherryPick level計算エンジンを純粋componentとして移植する"
```
---

### Task 2: experience更新core operationを1 transactionで実装する

**Files:**
- Create: `internal/core/role/experience.go`
- Create: `internal/core/role/experience_test.go`
- Create: `internal/core/role/experience_concurrency_test.go`
- Modify: `internal/core/role/role_service.go:100-145`（Service structへ`db`と`expPub`追加）
- Modify: `internal/core/role/role_service.go:159-172`（`SetDB`、`SetExperienceEventPublisher`）

**Interfaces:**
- Produces: `type ExperienceSetMode string`（`ExperienceSetModeSet="set"`、`ExperienceSetModeAdd="add"`、`ExperienceSetModeMultiplier="multiplier"`）
- Produces: `type ChangeExperienceInput struct { UserID string; RoleID string; Mode ExperienceSetMode; Value float64; AssignForce bool }`
- Produces: `type ChangeExperienceResult struct { RoleID string; UserID string; AssignmentID string; Mode ExperienceSetMode; BeforeValue int64; AfterValue int64 }`
- Produces: `type ExperienceUpdatedPayload struct { AssignmentID string; UserID string; RoleID string; Experience int64 }`
- Produces: `type ExperienceEventPublisher interface { PublishExperienceUpdated(payload ExperienceUpdatedPayload) error }`
- Produces: `func (s *Service) SetDB(db *gorm.DB)`、`func (s *Service) SetExperienceEventPublisher(pub ExperienceEventPublisher)`
- Produces: `func (s *Service) ChangeExperience(ctx context.Context, in ChangeExperienceInput) (*ChangeExperienceResult, error)`
- Produces: `var ErrInvalidRoleTarget = errors.New(...)`、`var ErrInvalidExperienceValue = errors.New(...)`
- Consumes: Task 1の`EvaluateLevel`は不要（levelはread時に計算）。`s.idGen`、`s.InvalidateUserRoleCache`。
- Consumers: Plan 3の`admin/roles/change-exp`handler、routerのevent配線、DB concurrency test。

- [ ] **Step 1: 失敗するtestを書く**

`internal/core/role/experience_test.go`（validation unit test）と`internal/core/role/experience_concurrency_test.go`（実DB test）を新規作成する。

`experience_test.go`:

```go
package role_test

import (
	"context"
	"testing"

	"github.com/shiroha-a/mk/internal/core/role"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChangeExperience_Validation(t *testing.T) {
	svc, _, _, _ := newTestService(t) // SetDB 未配線
	ctx := context.Background()

	_, err := svc.ChangeExperience(ctx, role.ChangeExperienceInput{})
	require.Error(t, err)
	assert.ErrorIs(t, err, role.ErrInvalidExperienceValue)

	_, err = svc.ChangeExperience(ctx, role.ChangeExperienceInput{
		UserID: "u1", RoleID: "r1", Mode: role.ExperienceSetModeAdd, Value: 1,
	})
	require.Error(t, err, "SetDB not wired must fail")
	assert.Contains(t, err.Error(), "SetDB")

	_, err = svc.ChangeExperience(ctx, role.ChangeExperienceInput{
		UserID: "u1", RoleID: "r1", Mode: "bogus", Value: 1,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, role.ErrInvalidExperienceValue)
}

// capturePublisher records immutable payloads for the cache/event assertion.
type capturePublisher struct {
	payloads []role.ExperienceUpdatedPayload
}

func (p *capturePublisher) PublishExperienceUpdated(payload role.ExperienceUpdatedPayload) error {
	p.payloads = append(p.payloads, payload)
	return nil
}
```

`experience_concurrency_test.go`（実DB。package専用schemaに全migrationを適用する）:

```go
package role_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/core/role"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// openRoleTestDB opens the package schema, applying every migration once
// (role table absent = first use in this process)。ロール table が既にあれば
// 再適用しない (migration は全て冪等ではないため)。
func openRoleTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := testutil.OpenTestDB()
	require.NoError(t, err)
	var hasRole bool
	require.NoError(t, db.Raw(`SELECT to_regclass('"role"') IS NOT NULL`).Row().Scan(&hasRole))
	if hasRole {
		return db
	}
	matches, err := filepath.Glob(filepath.Join("..", "..", "..", "migration", "*.up.sql"))
	require.NoError(t, err)
	sort.Strings(matches)
	for _, path := range matches {
		raw, err := os.ReadFile(path)
		require.NoError(t, err, "read %s", path)
		require.NoError(t, db.Exec(string(raw)).Error, "apply %s", filepath.Base(path))
	}
	return db
}

// seedLevelRole creates a manualLevel role + assignment, returning the service.
func seedLevelRole(t *testing.T, db *gorm.DB, roleID, userID string, startExp *int64) *role.Service {
	t.Helper()
	roleRepo := repository.NewRoleRepository(db)
	assignRepo := repository.NewRoleAssignmentRepository(db)
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	idGen, _ := id.NewGenerator("aidx")
	now := time.Now()

	// 前回失敗 run の残骸を先に掃除して Create を idempotent にする。
	db.Exec(`DELETE FROM "role_assignment" WHERE "roleId" = ?`, roleID)
	db.Exec(`DELETE FROM "role" WHERE id = ?`, roleID)
	db.Exec(`DELETE FROM "user" WHERE id = ?`, userID)

	require.NoError(t, roleRepo.Create(&model.Role{
		ID: roleID, UpdatedAt: now, LastUsedAt: now, Name: "Level",
		Target: model.RoleTargetManualLevel,
		LevelPolicies: datatypes.JSON([]byte(
			`{"baseLevel":0,"experiencePolicies":[{"level":100,"type":"const","base":100}]}`)),
		Policies:    datatypes.JSON([]byte("{}")),
		CondFormula: datatypes.JSON([]byte("{}")),
	}))
	t.Cleanup(func() {
		db.Exec(`DELETE FROM "role_assignment" WHERE "roleId" = ?`, roleID)
		db.Exec(`DELETE FROM "role" WHERE id = ?`, roleID)
	})
	require.NoError(t, db.Exec(
		`INSERT INTO "user" (id, username, "usernameLower", "avatarDecorations") VALUES (?, ?, ?, '[]')`,
		userID, userID, userID).Error)
	t.Cleanup(func() { db.Exec(`DELETE FROM "user" WHERE id = ?`, userID) })

	if startExp != nil {
		require.NoError(t, assignRepo.Create(&model.RoleAssignment{
			ID: "a_" + roleID, UserID: userID, RoleID: roleID, Experience: startExp,
		}))
		t.Cleanup(func() { db.Exec(`DELETE FROM "role_assignment" WHERE id = ?`, "a_"+roleID) })
	}

	svc := role.NewService(roleRepo, assignRepo, metaRepo, idGen)
	svc.SetDB(db)
	return svc
}

func TestChangeExperience_ConcurrentAddsNoLostUpdate(t *testing.T) {
	db := openRoleTestDB(t)
	start := int64(100)
	svc := seedLevelRole(t, db, "clvl_r1", "clvl_u1", &start)

	const goroutines = 8
	const addAmount = 100.0
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.ChangeExperience(context.Background(), role.ChangeExperienceInput{
				UserID: "clvl_u1", RoleID: "clvl_r1",
				Mode: role.ExperienceSetModeAdd, Value: addAmount, AssignForce: true,
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	var got int64
	require.NoError(t, db.Raw(
		`SELECT COALESCE(experience, 0) FROM "role_assignment" WHERE id = 'a_clvl_r1'`).
		Row().Scan(&got))
	assert.Equal(t, int64(100+goroutines*100), got, "no lost update across concurrent adds")
}

func TestChangeExperience_ModesAssignForceAndClamp(t *testing.T) {
	db := openRoleTestDB(t)
	start := int64(250)
	svc := seedLevelRole(t, db, "clvl_m1", "clvl_u2", &start)
	ctx := context.Background()

	// set → 250 のまま
	res, err := svc.ChangeExperience(ctx, role.ChangeExperienceInput{
		UserID: "clvl_u2", RoleID: "clvl_m1", Mode: role.ExperienceSetModeSet, Value: 250,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(250), res.AfterValue)
	assert.Equal(t, int64(250), res.BeforeValue)

	// add +100 → 350
	res, err = svc.ChangeExperience(ctx, role.ChangeExperienceInput{
		UserID: "clvl_u2", RoleID: "clvl_m1", Mode: role.ExperienceSetModeAdd, Value: 100,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(350), res.AfterValue)

	// multiplier 1.5 → floor(525) = 525
	res, err = svc.ChangeExperience(ctx, role.ChangeExperienceInput{
		UserID: "clvl_u2", RoleID: "clvl_m1", Mode: role.ExperienceSetModeMultiplier, Value: 1.5,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(525), res.AfterValue)

	// 負値は 0 に clamp (現在 525 → -475 → 0)
	res, err = svc.ChangeExperience(ctx, role.ChangeExperienceInput{
		UserID: "clvl_u2", RoleID: "clvl_m1", Mode: role.ExperienceSetModeAdd, Value: -1000,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(0), res.AfterValue)

	// clamp: MAX_SAFE_INTEGER 超過の add は 9007199254740991 に止まる
	res, err = svc.ChangeExperience(ctx, role.ChangeExperienceInput{
		UserID: "clvl_u2", RoleID: "clvl_m1", Mode: role.ExperienceSetModeAdd, Value: 9007199254740991,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(9007199254740991), res.AfterValue)
}

func TestChangeExperience_AssignForce(t *testing.T) {
	db := openRoleTestDB(t)
	svc := seedLevelRole(t, db, "clvl_f1", "clvl_u3", nil)
	ctx := context.Background()

	// assignment 無し + assignForce=false → ErrNotAssigned
	_, err := svc.ChangeExperience(ctx, role.ChangeExperienceInput{
		UserID: "clvl_u3", RoleID: "clvl_f1", Mode: role.ExperienceSetModeAdd, Value: 10,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, role.ErrNotAssigned)

	// assignment 無し + assignForce=true (set) → 新規 assignment 作成
	res, err := svc.ChangeExperience(ctx, role.ChangeExperienceInput{
		UserID: "clvl_u3", RoleID: "clvl_f1", Mode: role.ExperienceSetModeSet, Value: 777, AssignForce: true,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(777), res.AfterValue)
	require.NotEmpty(t, res.AssignmentID)

	var exp int64
	require.NoError(t, db.Raw(`SELECT COALESCE(experience, 0) FROM "role_assignment"
		WHERE "userId" = 'clvl_u3' AND "roleId" = 'clvl_f1'`).Row().Scan(&exp))
	assert.Equal(t, int64(777), exp)
}

func TestChangeExperience_PublishesImmutableEventAfterCommit(t *testing.T) {
	db := openRoleTestDB(t)
	start := int64(100)
	svc := seedLevelRole(t, db, "clvl_e1", "clvl_u4", &start)
	pub := &capturePublisher{}
	svc.SetExperienceEventPublisher(pub)
	ctx := context.Background()

	_, err := svc.ChangeExperience(ctx, role.ChangeExperienceInput{
		UserID: "clvl_u4", RoleID: "clvl_e1", Mode: role.ExperienceSetModeAdd, Value: 50, AssignForce: true,
	})
	require.NoError(t, err)

	require.Len(t, pub.payloads, 1)
	// immutable payload は value copy (pointer共有なし) で更新後値を運ぶ。
	assert.Equal(t, "clvl_u4", pub.payloads[0].UserID)
	assert.Equal(t, "clvl_e1", pub.payloads[0].RoleID)
	assert.Equal(t, int64(150), pub.payloads[0].Experience)
	require.NotEmpty(t, pub.payloads[0].AssignmentID)
}
```

- [ ] **Step 2: testがREDであることを確認する**

Run: `go test ./internal/core/role -run 'TestChangeExperience' -count=1`

Expected: `undefined: ChangeExperience`／`undefined: ExperienceSetModeAdd`でcompile error→FAIL。Docker不要（`OpenTestDB`はCI service PGまたは`.env.test`のPGへ接続）。

- [ ] **Step 3: 最小実装**

`internal/core/role/experience.go`を新規作成する。

```go
package role

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// maxSafeExperience は外部APIで扱う experience の上限 (Number.MAX_SAFE_INTEGER)。
const maxSafeExperience int64 = 9007199254740991

// ExperienceSetMode mirrors CherryPick RoleExperienceSetMode.
type ExperienceSetMode string

const (
	ExperienceSetModeSet        ExperienceSetMode = "set"
	ExperienceSetModeAdd        ExperienceSetMode = "add"
	ExperienceSetModeMultiplier ExperienceSetMode = "multiplier"
)

// ErrInvalidRoleTarget is returned when ChangeExperience targets a role that
// is not target=manualLevel (CherryPick change-exp INVALID_ROLE_TARGET).
var ErrInvalidRoleTarget = errors.New("role target is not manualLevel")

// ErrInvalidExperienceValue is returned for a non-finite value or an
// unrepresentable operation. Experience updates are fail-closed.
var ErrInvalidExperienceValue = errors.New("invalid experience value")

// ChangeExperienceInput is the parameter set of an experience mutation.
type ChangeExperienceInput struct {
	UserID      string
	RoleID      string
	Mode        ExperienceSetMode
	Value       float64
	AssignForce bool
}

// ChangeExperienceResult carries the before/after values so the caller (the
// admin handler) can write the changeExperienceRole moderation log.
type ChangeExperienceResult struct {
	RoleID       string
	UserID       string
	AssignmentID string
	Mode         ExperienceSetMode
	BeforeValue  int64
	AfterValue   int64
}

// ExperienceUpdatedPayload is the immutable snapshot published after an
// experience change. It is a value struct: no model pointers are shared.
type ExperienceUpdatedPayload struct {
	AssignmentID string
	UserID       string
	RoleID       string
	Experience   int64
}

// ExperienceEventPublisher emits the post-commit experience event. The router
// wires a Redis-backed implementation; tests use a capturing stub.
type ExperienceEventPublisher interface {
	PublishExperienceUpdated(payload ExperienceUpdatedPayload) error
}

// ChangeExperience applies one set/add/multiplier mutation inside a single
// transaction, mirroring CherryPick RoleService.assignExperience.
//
//  1. lock role row and assignment row (SELECT ... FOR UPDATE)
//  2. verify role.target == manualLevel
//  3. missing assignment requires assignForce (else ErrNotAssigned)
//  4. apply the mode
//  5. clamp the result to [0, Number.MAX_SAFE_INTEGER] after floor
//  6. update role.lastUsedAt
//  7. after commit: invalidate the per-user role cache
//  8. publish the immutable internal event
//
// The moderation log is written by the admin handler (mk-go convention),
// using BeforeValue / AfterValue from the returned result.
func (s *Service) ChangeExperience(ctx context.Context, in ChangeExperienceInput) (*ChangeExperienceResult, error) {
	if s.db == nil {
		return nil, fmt.Errorf("role: ChangeExperience: SetDB not wired")
	}
	if in.UserID == "" || in.RoleID == "" {
		return nil, ErrInvalidExperienceValue
	}
	switch in.Mode {
	case ExperienceSetModeSet, ExperienceSetModeAdd, ExperienceSetModeMultiplier:
	default:
		return nil, ErrInvalidExperienceValue
	}
	if math.IsNaN(in.Value) || math.IsInf(in.Value, 0) {
		return nil, ErrInvalidExperienceValue
	}

	var result ChangeExperienceResult
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 1. role row lock + target 検証。
		var role model.Role
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", in.RoleID).First(&role).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRoleNotFound
			}
			return err
		}
		if role.Target != model.RoleTargetManualLevel {
			return ErrInvalidRoleTarget
		}

		// 2. assignment row lock。存在しなければ assignForce を要求。
		var assign model.RoleAssignment
		findErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("\"userId\" = ? AND \"roleId\" = ?", in.UserID, in.RoleID).
			First(&assign).Error

		var before int64
		var raw float64
		switch {
		case findErr == nil:
			if assign.Experience != nil {
				before = *assign.Experience
			}
			switch in.Mode {
			case ExperienceSetModeSet:
				raw = in.Value
			case ExperienceSetModeAdd:
				raw = float64(before) + in.Value
			case ExperienceSetModeMultiplier:
				raw = float64(before) * in.Value
			}
		case errors.Is(findErr, gorm.ErrRecordNotFound):
			if !in.AssignForce {
				return ErrNotAssigned
			}
			switch in.Mode {
			case ExperienceSetModeSet, ExperienceSetModeAdd:
				raw = in.Value
			case ExperienceSetModeMultiplier:
				raw = 0
			}
		default:
			return findErr
		}

		// 3. clamp: Math.floor → [0, MAX_SAFE_INTEGER]。
		after, err := clampExperience(raw)
		if err != nil {
			return err
		}

		result = ChangeExperienceResult{
			RoleID: in.RoleID, UserID: in.UserID, Mode: in.Mode,
			BeforeValue: before, AfterValue: after,
		}

		if findErr == nil {
			if err := tx.Model(&model.RoleAssignment{}).
				Where("id = ?", assign.ID).
				Update("experience", after).Error; err != nil {
				return err
			}
			result.AssignmentID = assign.ID
		} else {
			a := &model.RoleAssignment{
				ID: s.idGen.Generate(time.Now()), UserID: in.UserID,
				RoleID: in.RoleID, Experience: &after,
			}
			if err := tx.Create(a).Error; err != nil {
				return err
			}
			result.AssignmentID = a.ID
		}

		// 4. role.lastUsedAt 更新 (CherryPick assignExperience 相当)。
		if err := tx.Model(&model.Role{}).
			Where("id = ?", in.RoleID).
			Update("lastUsedAt", time.Now()).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// 5. commit 後: per-user cache invalidate + immutable payload で event publish。
	s.InvalidateUserRoleCache(in.UserID)
	if s.expPub != nil {
		payload := ExperienceUpdatedPayload{
			AssignmentID: result.AssignmentID,
			UserID:       in.UserID,
			RoleID:       in.RoleID,
			Experience:   result.AfterValue,
		}
		if perr := s.expPub.PublishExperienceUpdated(payload); perr != nil {
			slog.Warn("role: publish experience updated failed",
				"roleId", in.RoleID, "userId", in.UserID, "err", perr)
		}
	}
	return &result, nil
}

// clampExperience mirrors CherryPick Math.min(Math.max(Math.floor(v), 0),
// Number.MAX_SAFE_INTEGER). Non-finite input fails closed.
func clampExperience(raw float64) (int64, error) {
	if math.IsNaN(raw) || math.IsInf(raw, 0) {
		return 0, ErrInvalidExperienceValue
	}
	f := math.Floor(raw)
	if f < 0 {
		f = 0
	}
	if f > float64(maxSafeExperience) {
		f = float64(maxSafeExperience)
	}
	return int64(f), nil
}
```

`internal/core/role/role_service.go`の`Service` structへ次を追加する（`roleAssignNotifier`の直後）。

```go
	// db は ChangeExperience の transaction 用 (signup.Service と同じ SetDB
	// pattern)。nil なら ChangeExperience は SetDB 未配線で fail する。
	db *gorm.DB
	// expPub は experience 更新後の内部 event publisher (nil なら発行しない)。
	expPub ExperienceEventPublisher
```

`SetDB`は新規に発明したshortcutではなく、`internal/core/signup/signup_service.go`に既存する確立patternである（`signup.Service.SetDB` + `s.db.Transaction` + `SELECT ... FOR UPDATE`、#600/#604）。transaction境界をserviceへ注入する最小の方法として、既存実装と同一の`SetDB(db *gorm.DB)`を採用する。repository interface経由ではtransactionを横断できないため、core層でtransactionを張るにはDB handle注入が必要。

`role_service.go`へ次を追加する（`SetRoleAssignNotifier`の直後）。

```go
// SetDB wires the GORM handle used by ChangeExperience to run the update in
// one transaction with row locks. Production wiring calls this at startup;
// mock-based tests leave it unset.
func (s *Service) SetDB(db *gorm.DB) {
	s.db = db
}

// SetExperienceEventPublisher wires the post-commit internal event publisher
// used by ChangeExperience. Optional: nil disables publishing.
func (s *Service) SetExperienceEventPublisher(pub ExperienceEventPublisher) {
	s.expPub = pub
}
```

`import`に`gorm.io/gorm`を追加する（`experience.go`は`gorm.io/gorm/clause`もimport）。

- [ ] **Step 4: testがGREENであることを確認する**

Run: `go test ./internal/core/role -run 'TestChangeExperience' -count=1`

Expected: 全testがPASS。concurrency testは`FOR UPDATE`により8並列addがlost updateなしに`900`になる。

- [ ] **Step 5: 既存role packageのnon-regressionを確認する**

Run: `go test ./internal/core/role -count=1`

Expected: 既存testが全てPASSする（Service structへのfield追加は既存挙動を変えない）。

- [ ] **Step 6: commit**（ユーザー承認後のみ実行する）

リポジトリ規約に従い、commitはユーザーの明示的な指示がある場合のみ実行する。
```powershell
git add -- internal/core/role/experience.go internal/core/role/experience_test.go internal/core/role/experience_concurrency_test.go internal/core/role/role_service.go
git diff --cached --check
git commit -m "role: experience更新をtransaction+row lockで実装する"
```
---

### Task 3: manualLevel policy interpolation（policyAsLevel）を実装する

**Files:**
- Create: `internal/core/role/policy_level.go`
- Create: `internal/core/role/policy_level_test.go`
- Modify: `internal/core/role/role_service.go:542-577`（`rolePolicyOverride`へ`PolicyAsLevel`追加）
- Modify: `internal/core/role/role_service.go:672-708`（`GetUserPolicies`→`GetUserPoliciesChecked`委譲）

**Interfaces:**
- Produces: `type policyAsLevelEntry struct { Level int; Type string; Base any; Additional *float64 }`
- Produces: `func (s *Service) GetUserPoliciesChecked(userID string) (map[string]any, error)`
- Produces: `func ValidateManualLevelPolicies(policies datatypes.JSON) error`（write-time検証。Plan 3 Task 1のcreate/updateが利用）
- Produces: `var ErrInvalidLevelPolicy = errors.New(...)`
- Produces: `func (s *Service) applyPolicyAsLevel(role *model.Role, assign *model.RoleAssignment, basePolicies map[string]any, overrides map[string]rolePolicyOverride) error`
- Consumes: Task 1の`EvaluateLevel`/`LevelResult`、Task 1の`model.ParseLevelPolicies`、既存`parseRolePolicies`/`computePolicy`。
- Consumers: Plan 3の`i`/`admin/show-user`/`users/show`のpolicies出力（checked経路）、gate経路の`GetUserPolicies`。

- [ ] **Step 1: 失敗するtestを書く**

`internal/core/role/policy_level_test.go`を新規作成する。

```go
package role_test

import (
	"testing"

	"github.com/shiroha-a/mk/internal/core/role"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

// manualLevelRole seeds a manualLevel role with a level curve and an
// optional policyAsLevel set for the given policy key.
func manualLevelRole(policyKey, policiesJSON string, experience int64) (*model.Role, *model.RoleAssignment) {
	r := &model.Role{
		ID: "r1", Name: "Level", Target: model.RoleTargetManualLevel,
		LevelPolicies: datatypes.JSON([]byte(
			`{"baseLevel":0,"experiencePolicies":[{"level":10,"type":"const","base":100}]}`)),
		Policies: datatypes.JSON([]byte(policiesJSON)),
	}
	return r, &model.RoleAssignment{ID: "a1", UserID: "user1", RoleID: "r1", Experience: &experience}
}

func seedPolicies(t *testing.T, role *model.Role, assign *model.RoleAssignment) *role.Service {
	t.Helper()
	svc, roleRepo, assignRepo, _ := newTestService(t)
	roleRepo.Roles[role.ID] = role
	assignRepo.Assignments["user1:"+role.ID] = assign
	return svc
}

func TestGetUserPoliciesChecked_ConstPolicyAsLevel(t *testing.T) {
	// exp=250 → level 2 → 最初の const 区間 (level 0..1) に該当 → canPublicNote=true。
	role, assign := manualLevelRole("canPublicNote", `{"canPublicNote":{"useDefault":true,"priority":1,"value":false,"policyAsLevel":[{"level":2,"type":"const","base":true},{"level":3,"type":"const","base":false}]}}`, 250)
	svc := seedPolicies(t, role, assign)

	policies, err := svc.GetUserPoliciesChecked("user1")
	require.NoError(t, err)
	assert.Equal(t, true, policies["canPublicNote"], "level 2 は const(true) 区間")
}

func TestGetUserPoliciesChecked_ConstPolicyAsLevelSecondSegment(t *testing.T) {
	// exp=500 → level 5 → 2番目の const 区間 (level 2..4) に該当 → false。
	role, assign := manualLevelRole("canPublicNote", `{"canPublicNote":{"useDefault":true,"priority":1,"value":true,"policyAsLevel":[{"level":2,"type":"const","base":true},{"level":3,"type":"const","base":false}]}}`, 500)
	svc := seedPolicies(t, role, assign)

	policies, err := svc.GetUserPoliciesChecked("user1")
	require.NoError(t, err)
	assert.Equal(t, false, policies["canPublicNote"], "level 5 は const(false) 区間")
}

func TestGetUserPoliciesChecked_BasePolicyAsLevel(t *testing.T) {
	// base 型 → useDefault=true → instance default に倒れる。
	role, assign := manualLevelRole("canPublicNote", `{"canPublicNote":{"useDefault":false,"priority":1,"value":true,"policyAsLevel":[{"level":100,"type":"base"}]}}`, 0)
	svc := seedPolicies(t, role, assign)

	policies, err := svc.GetUserPoliciesChecked("user1")
	require.NoError(t, err)
	assert.Equal(t, false, policies["canPublicNote"], "base 区間は default 値 (false) にフォールバック")
}

func TestGetUserPoliciesChecked_MultiplierPolicyAsLevel(t *testing.T) {
	// multiplier: base=100, additional=10。absolute level 2 (baseLevel 0) →
	// value = min(100 + 10*(2-0), MAX_SAFE_INTEGER) = 120。
	role, assign := manualLevelRole("driveCapacityMb", `{"driveCapacityMb":{"useDefault":true,"priority":1,"value":30,"policyAsLevel":[{"level":100,"type":"multiplier","base":100,"additional":10}]}}`, 250)
	svc := seedPolicies(t, role, assign)

	policies, err := svc.GetUserPoliciesChecked("user1")
	require.NoError(t, err)
	got, ok := policies["driveCapacityMb"].(int)
	require.True(t, ok, "multiplier は整数 policy として int に集約される")
	assert.Equal(t, 120, got)
}

func TestGetUserPoliciesChecked_InvalidConstTypeFailsClosed(t *testing.T) {
	// canPublicNote は bool policy。const の base が number 5 → ErrInvalidLevelPolicy。
	role, assign := manualLevelRole("canPublicNote", `{"canPublicNote":{"useDefault":true,"priority":1,"value":false,"policyAsLevel":[{"level":1,"type":"const","base":5}]}}`, 100)
	svc := seedPolicies(t, role, assign)

	_, err := svc.GetUserPoliciesChecked("user1")
	require.Error(t, err)
	assert.ErrorIs(t, err, role.ErrInvalidLevelPolicy)
}

func TestGetUserPolicies_DeniesCorruptedPolicyAsLevel(t *testing.T) {
	// gate 経路 (GetUserPolicies) は error を返せないため、不正 policyAsLevel
	// (corruption) の key を deny 値へ倒す。base default (100) へは倒さない
	// (base fallback 禁止)。
	role, assign := manualLevelRole("driveCapacityMb", `{"driveCapacityMb":{"useDefault":true,"priority":1,"value":100,"policyAsLevel":[{"level":1,"type":"const","base":true}]}}`, 100)
	svc := seedPolicies(t, role, assign)

	policies := svc.GetUserPolicies("user1")
	got, ok := policies["driveCapacityMb"].(int)
	require.True(t, ok, "deny は int 0 を返す")
	assert.Equal(t, 0, got, "corrupted key は deny 値 0 (base default 100 ではない)")
}

func TestGetUserPoliciesChecked_DeterministicAcrossRoles(t *testing.T) {
	// 複数 role が同じ policy へ寄与しても同 priority の決定順序は role 順で
	// 固定される (Go map iteration 非依存)。manualLevel と manual の混合で
	// 値が決定的に決まることを確認する。
	svc, roleRepo, assignRepo, _ := newTestService(t)

	roleRepo.Roles["r1"] = &model.Role{
		ID: "r1", Name: "Level", Target: model.RoleTargetManualLevel,
		LevelPolicies: datatypes.JSON([]byte(
			`{"baseLevel":0,"experiencePolicies":[{"level":10,"type":"const","base":100}]}`)),
		Policies: datatypes.JSON([]byte(
			`{"canPublicNote":{"useDefault":false,"priority":1,"value":false,"policyAsLevel":[{"level":100,"type":"const","base":true}]}}`)),
	}
	exp := int64(250)
	assignRepo.Assignments["u1:r1"] = &model.RoleAssignment{ID: "a1", UserID: "u1", RoleID: "r1", Experience: &exp}
	roleRepo.Roles["r2"] = &model.Role{
		ID: "r2", Name: "Manual", Target: model.RoleTargetManual,
		Policies: datatypes.JSON([]byte(
			`{"canPublicNote":{"useDefault":false,"priority":2,"value":true}}`)),
	}

	for i := 0; i < 20; i++ {
		policies, err := svc.GetUserPoliciesChecked("u1")
		require.NoError(t, err)
		assert.Equal(t, true, policies["canPublicNote"], "priority 2 の manual role が勝つ (r1 の priority 1 は無視)")
	}
}
```

- [ ] **Step 2: testがREDであることを確認する**

Run: `go test ./internal/core/role -run 'TestGetUserPolicies' -count=1`

Expected: `GetUserPoliciesChecked`未定義・`PolicyAsLevel`未追加でFAILする。

- [ ] **Step 3: 最小実装**

`internal/core/role/policy_level.go`を新規作成する。

```go
package role

import (
	"errors"
	"fmt"
	"math"

	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/datatypes"
)

// ErrInvalidLevelPolicy is returned when a manualLevel role's policyAsLevel
// value does not match the target policy's declared type, or the level data
// cannot be interpreted. The request fails closed instead of silently
// ignoring that role's contribution (spec: 不正値は固定errorで失敗させる)。
var ErrInvalidLevelPolicy = errors.New("invalid level policy value")

// policyAsLevelEntry is one element of a policy override's policyAsLevel.
// base carries the const value (bool / number) or the multiplier's numeric
// base; additional is the multiplier increment.
type policyAsLevelEntry struct {
	Level      int      `json:"level"`
	Type       string   `json:"type"` // base | const | multiplier
	Base       any      `json:"base"`
	Additional *float64 `json:"additional,omitempty"`
}

// policyAsLevelResolution is the outcome of resolving one policy key for the
// user's current level. useDefault=false carries a concrete value.
type policyAsLevelResolution struct {
	useDefault bool
	value      any
}

// applyPolicyAsLevel resolves every policyAsLevel entry of a manualLevel
// role against the user's current level, mutating the parsed overrides map.
// Invalid types or level data fail closed with ErrInvalidLevelPolicy.
// 返す error には識別子 (role ID・policy key・user ID) を含めない。
func (s *Service) applyPolicyAsLevel(role *model.Role, assign *model.RoleAssignment, basePolicies map[string]any, overrides map[string]rolePolicyOverride) error {
	if role == nil || len(role.LevelPolicies) == 0 {
		return nil
	}
	lp, err := model.ParseLevelPolicies(role.LevelPolicies)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidLevelPolicy, "invalid-level-policies")
	}
	inputs := make([]LevelPolicyInput, 0, len(lp.ExperiencePolicies))
	for _, p := range lp.ExperiencePolicies {
		inputs = append(inputs, LevelPolicyInput{
			Level: p.Level, Type: p.Type, Base: p.Base,
			Additional: derefFloat(p.Additional), Exponential: derefFloat(p.Exponential),
		})
	}
	var exp int64
	if assign != nil && assign.Experience != nil {
		exp = *assign.Experience
	}
	lvl, err := EvaluateLevel(exp, lp.BaseLevel, inputs)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidLevelPolicy, "invalid-level-evaluation")
	}

	// overrides は keyed map。map iteration 順は結果に影響しない
	// (各 key は独立して resolve → computePolicy の role 順 slice で集約)。
	for key, pol := range overrides {
		if len(pol.PolicyAsLevel) == 0 {
			continue
		}
		resolved, err := resolvePolicyAsLevel(pol.PolicyAsLevel, lvl, basePolicies[key])
		if err != nil {
			return err
		}
		if resolved == nil {
			continue
		}
		pol.UseDefault = resolved.useDefault
		pol.Value = resolved.value
		overrides[key] = pol
	}
	return nil
}

// resolvePolicyAsLevel ports CherryPick getUserPolicies' level-policy loop.
// It returns nil when no segment matches (the role's override is left as-is).
func resolvePolicyAsLevel(entries []policyAsLevelEntry, lvl LevelResult, baseVal any) (*policyAsLevelResolution, error) {
	level := lvl.Level - lvl.MinLevel
	span := lvl.MaxLevel - lvl.MinLevel + 1
	startLevel := 0
	for i, e := range entries {
		nextLevel := startLevel + e.Level
		if i == len(entries)-1 {
			nextLevel = span
		}
		if level >= startLevel && level <= nextLevel {
			switch e.Type {
			case "base":
				return &policyAsLevelResolution{useDefault: true}, nil
			case "const":
				if err := validatePolicyAsLevelValue(baseVal, e.Base); err != nil {
					return nil, err
				}
				return &policyAsLevelResolution{useDefault: false, value: e.Base}, nil
			case "multiplier":
				if !isNumericPolicy(baseVal) {
					return nil, fmt.Errorf("%w: multiplier on non-numeric policy", ErrInvalidLevelPolicy)
				}
				b, ok := e.Base.(float64)
				if !ok {
					return nil, fmt.Errorf("%w: multiplier base must be a number", ErrInvalidLevelPolicy)
				}
				if e.Additional == nil {
					return nil, fmt.Errorf("%w: multiplier missing additional", ErrInvalidLevelPolicy)
				}
				v := b + *e.Additional*float64(lvl.Level-startLevel)
				if v > float64(maxSafeExperience) {
					v = float64(maxSafeExperience)
				}
				return &policyAsLevelResolution{useDefault: false, value: v}, nil
			default:
				return nil, fmt.Errorf("%w: unknown policyAsLevel type %q", ErrInvalidLevelPolicy, e.Type)
			}
		}
		startLevel += e.Level
	}
	return nil, nil
}

// validatePolicyAsLevelValue checks a const value against the target policy's
// declared Go type (bool / number / string)。型不一致は fail-closed。
func validatePolicyAsLevelValue(baseVal, candidate any) error {
	switch baseVal.(type) {
	case bool:
		if _, ok := candidate.(bool); !ok {
			return fmt.Errorf("%w: bool policy got %T", ErrInvalidLevelPolicy, candidate)
		}
	case int, int64, float64:
		switch candidate.(type) {
		case float64, int, int64:
		default:
			return fmt.Errorf("%w: numeric policy got %T", ErrInvalidLevelPolicy, candidate)
		}
	case string:
		if _, ok := candidate.(string); !ok {
			return fmt.Errorf("%w: string policy got %T", ErrInvalidLevelPolicy, candidate)
		}
	default:
		return fmt.Errorf("%w: unsupported base type %T", ErrInvalidLevelPolicy, baseVal)
	}
	return nil
}

func isNumericPolicy(baseVal any) bool {
	switch baseVal.(type) {
	case int, int64, float64:
		return true
	}
	return false
}

func derefFloat(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// ValidateManualLevelPolicies is the write-time gate that keeps invalid
// policyAsLevel data from being persisted (admin/roles/create・updateが
// target=manualLevel のとき呼ぶ)。各 policy key の policyAsLevel を対象
// policy の宣言型 (bool / number / string) に照合し、不正なら
// ErrInvalidLevelPolicy を返す。preflight の構造検証と合わせて、不正level
// policy が DB に永続化されるのを防ぐ (runtime の deny/error 分岐は防御)。
func ValidateManualLevelPolicies(policies datatypes.JSON) error {
	if len(policies) == 0 {
		return nil
	}
	overrides := parseRolePolicies(policies)
	defaults := DefaultPolicies()
	for key, pol := range overrides {
		if len(pol.PolicyAsLevel) == 0 {
			continue
		}
		baseVal, ok := defaults[key]
		if !ok {
			// 未知 policy key は型情報が無いため構造のみ (preflight) で担保する。
			continue
		}
		for _, e := range pol.PolicyAsLevel {
			switch e.Type {
			case "base":
			case "const":
				if err := validatePolicyAsLevelValue(baseVal, e.Base); err != nil {
					return err
				}
			case "multiplier":
				if !isNumericPolicy(baseVal) {
					return fmt.Errorf("%w: multiplier on non-numeric policy", ErrInvalidLevelPolicy)
				}
				if _, ok := e.Base.(float64); !ok {
					return fmt.Errorf("%w: multiplier base must be a number", ErrInvalidLevelPolicy)
				}
				if e.Additional == nil {
					return fmt.Errorf("%w: multiplier missing additional", ErrInvalidLevelPolicy)
				}
			default:
				return fmt.Errorf("%w: unknown policyAsLevel type %q", ErrInvalidLevelPolicy, e.Type)
			}
		}
	}
	return nil
}
```

`internal/core/role/role_service.go`の`rolePolicyOverride`へ`PolicyAsLevel`を追加する。

```go
type rolePolicyOverride struct {
	UseDefault    bool                `json:"useDefault"`
	Priority      int                 `json:"priority"`
	Value         any                 `json:"value"`
	PolicyAsLevel []policyAsLevelEntry `json:"policyAsLevel"`
}
```

`GetUserPolicies`を`GetUserPoliciesChecked`への委譲へ書き換える。不正level policyはbase fallbackせず、gate経路は該当keyをdeny値（fail-closed）へ倒す。

```go
// GetUserPolicies returns the user's effective role policies for gate paths
// (middleware, limits) that cannot return an error. Invalid manualLevel
// policyAsLevel data is prevented at write time (admin create/update) and by
// the migration preflight, so the error branch below is defense against
// corruption: the affected key is denied (bool→false, number→0, string→""),
// never silently fallen back to the base default. API/entity paths use
// GetUserPoliciesChecked and fail the request.
func (s *Service) GetUserPolicies(userID string) map[string]any {
	policies, err := s.GetUserPoliciesChecked(userID)
	if err != nil {
		// 返す error には識別子を含めない (ErrInvalidLevelPolicy のみ)。
		slog.Warn("role: policy aggregation failed; denying affected key", "err", err)
		return s.denyPolicies(userID)
	}
	return policies
}

// denyPolicies fail-closes every policy key to its deny value. This is NOT a
// base fallback: it never returns a usable default. Validation makes this
// branch unreachable in practice (corruption-only safety net)。
func (s *Service) denyPolicies(userID string) map[string]any {
	base := DefaultPoliciesClone()
	s.applyMetaBasePolicies(base)
	out := make(map[string]any, len(base))
	for k, v := range base {
		switch v.(type) {
		case bool:
			out[k] = false
		case int, int64, float64:
			out[k] = 0
		case string:
			if k == "chatAvailability" {
				out[k] = "unavailable"
			} else {
				out[k] = ""
			}
		case []string:
			out[k] = []string{}
		default:
			out[k] = v
		}
	}
	return s.applyServerCaps(out)
}

// GetUserPoliciesChecked resolves the user's effective role policies,
// failing with ErrInvalidLevelPolicy when a manualLevel role's policyAsLevel
// entry carries a value whose type does not match the target policy.
func (s *Service) GetUserPoliciesChecked(userID string) (map[string]any, error) {
	basePolicies := DefaultPoliciesClone()
	s.applyMetaBasePolicies(basePolicies)

	if userID == "" {
		return s.applyServerCaps(basePolicies), nil
	}
	roles, err := s.GetUserRoles(userID)
	if err != nil {
		return s.applyServerCaps(basePolicies), nil
	}
	if len(roles) == 0 {
		return s.applyServerCaps(basePolicies), nil
	}
	assigns, err := s.assignmentRepo.ListByUser(userID)
	if err != nil {
		return s.applyServerCaps(basePolicies), nil
	}
	assignByRole := make(map[string]*model.RoleAssignment, len(assigns))
	for _, a := range assigns {
		assignByRole[a.RoleID] = a
	}

	roleOverrides := make([]map[string]rolePolicyOverride, 0, len(roles))
	for _, r := range roles {
		if r == nil || len(r.Policies) == 0 {
			roleOverrides = append(roleOverrides, nil)
			continue
		}
		m := parseRolePolicies(r.Policies)
		if r.Target == model.RoleTargetManualLevel {
			if err := s.applyPolicyAsLevel(r, assignByRole[r.ID], basePolicies, m); err != nil {
				return nil, err
			}
		}
		roleOverrides = append(roleOverrides, m)
	}

	out := make(map[string]any, len(basePolicies))
	for key, baseVal := range basePolicies {
		out[key] = computePolicy(key, baseVal, roleOverrides)
	}
	return s.applyServerCaps(out), nil
}
```

`rolePolicyOverride`の`PolicyAsLevel`を`parseRolePolicies`が自動でdecodeするため、`parseRolePolicies`の変更は不要。

- [ ] **Step 4: testがGREENであることを確認する**

Run: `go test ./internal/core/role -run 'TestGetUserPolicies' -count=1`

Expected: 全testがPASS。`TestGetUserPoliciesChecked_InvalidConstTypeFailsClosed`は`ErrInvalidLevelPolicy`を返す。

- [ ] **Step 5: 既存package全体のnon-regressionを確認する**

Run: `go test ./internal/core/role ./internal/api/admin ./internal/api/roles ./internal/api/i -count=1`

Expected: 全てPASS（`GetUserPolicies`の挙動はvalid dataで不変）。

- [ ] **Step 6: commit**（ユーザー承認後のみ実行する）

リポジトリ規約に従い、commitはユーザーの明示的な指示がある場合のみ実行する。
```powershell
git add -- internal/core/role/policy_level.go internal/core/role/policy_level_test.go internal/core/role/role_service.go
git diff --cached --check
git commit -m "role: manualLevelのpolicyAsLevel interpolationを追加する"
```

---

### Task 4: PR検証・privacy gateを実行する

**Files:**
- Consume: Plan 2全Taskの成果物。

**Interfaces:**
- Consumes: Task 1-3の実装。
- Produces: PR作成可能な検証済み状態。

- [ ] **Step 1: 全gateを実行する**

Run:

```powershell
make fmt
make lint
go build ./...
go test ./internal/core/role -count=1
go test ./internal/api/admin ./internal/api/roles ./internal/api/i ./internal/api/users -count=1
```

Expected: 全てexit 0。

- [ ] **Step 2: race + coverageでCI相当を確認する**

Run:

```powershell
go test -race -count=1 -timeout 10m -coverprofile=coverage-role.out -covermode=atomic ./internal/core/role
go tool cover -func=coverage-role.out | Select-String -Pattern "total:"
```

Expected: race detectorがPASSし、`internal/core/role`のtotal coverageが90%以上であること（CI閾値）。

- [ ] **Step 3: privacy gateを実行する**

```powershell
$changed = @(
  'internal/core/role/level.go',
  'internal/core/role/level_test.go',
  'internal/core/role/experience.go',
  'internal/core/role/experience_test.go',
  'internal/core/role/experience_concurrency_test.go',
  'internal/core/role/policy_level.go',
  'internal/core/role/policy_level_test.go',
  'internal/core/role/role_service.go'
)
$matches = 0
foreach ($file in $changed) {
    if (Test-Path -LiteralPath $file -PathType Leaf) {
        $text = [System.IO.File]::ReadAllText((Resolve-Path $file))
        $matches += [regex]::Matches($text, "['""][a-z0-9]{16}['""]").Count
        $matches += [regex]::Matches($text, '(?i)\b[0-9a-f]{64}\b').Count
        $matches += [regex]::Matches($text, '[A-Za-z]:\\').Count
    }
}
if ($matches -ne 0) { throw "private literal categories found: $matches" }
```

Expected: exit 0。個別matchを表示しない。`maxSafeExperience`（9007199254740991）はspec固定値でありprivacy対象外。

- [ ] **Step 4: controllerへhandoffする**

Run:

```powershell
git status --short
git log --oneline -5
```

Expected: Task 1-3のcommitが並び、working treeがclean。`Backend PR 2`のPR作成・push・mergeは、ユーザーが明示的に指示した場合のみ実施する。本planでは自動で`gh pr create`を実行しない。PR baseは`Misaki-Project/mk:Misaki-develop`を想定し、タイトル・本文は日本語、本文の`Closes`には実装開始前に作成した対応Issue番号を指定する。
