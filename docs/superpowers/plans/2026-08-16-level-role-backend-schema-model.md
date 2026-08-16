# CherryPick Level Role Backend Schema・Model Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** mk-goへCherryPick level roleのDB contract（`manualLevel` enum値、`role.levelPolicies`、`role.canHideProfileByUser`、`role_assignment.experience`/`isHideProfile`）を片道移行できる形で追加し、fresh DB・CherryPick import済みDBの両方で同じmigrationを冪等に適用できるようにする。

**Architecture:** 既存`000012_role`を編集せず、新規`000076_cherrypick_level_role` migrationで追加する。migrationは`IF NOT EXISTS`／enum `ADD VALUE IF NOT EXISTS`／index存在チェックで冪等にし、適用前に`internal/migrationcompat`の`CheckLevelRolePreflight`がCherryPick由来データの不変条件を固定categoryだけで検証する。downはdata guard通過時のみ追加物を除去し、enumはPostgreSQLに`DROP VALUE`が無いためrebuild/swapで戻す。modelは`RoleTargetManualLevel`、`LevelPolicies`、`CanHideProfileByUser`、`RoleAssignment.Experience *int64`／`IsHideProfile *bool`を追加する。

**Tech Stack:** Go 1.26、golang-migrate、GORM、pgx/v5、PostgreSQL 18

## Global Constraints

- 保証対象は最終構成の`mk-go + Misaki-Project frontend`のみ。旧CherryPick frontendとmk-go、新frontendとCherryPick backendのcross-combinationは保証しない。
- `000012_role`は編集しない。追加は新規migrationのみで行う。
- `role_assignment.experience`はDB `bigint`、外部APIは`0..Number.MAX_SAFE_INTEGER`（9007199254740991）へ制限する。hide state（`isHideProfile`）はnullableにする。
- migrationはfresh DBとCherryPick import済みDBの両方へ同じものを適用できること。enum value、column、indexが存在する場合はno-opにする。
- schema名だけでなくcolumn型、nullable、defaultも検証する。名前が同じでshapeが違う場合は自動変更せず停止する。
- migration 2回目がno-changeになることをtestで固定する。
- 不一致時は固定categoryだけを返し、ID、値、件数、path、hashに加えてtable名・column名・role ID・user IDを出力せずmigrationを開始しない。
- down fileは用意するが、`manualLevel` role、experience、level policy、profile hide dataが存在する場合は固定errorで停止する。dataが存在しない場合だけ追加column・index・enum valueを除去できる。本番復旧手段としてdown migrationは使用しない（片道移行方針）。
- PostgreSQLにはenum valueの`DROP VALUE`が無いため、downのenum除去はrebuild/swap（`CREATE TYPE new` → `ALTER COLUMN TYPE USING text` → `DROP TYPE old` → `RENAME`）で行う。
- 本番由来ID、件数、path、hash、credentialをfixture、terminal、report、commitへ出さない。test fixtureは`synthetic-*`と固定の小さな整数だけを使う。
- 本番導出のerror IDを使わない。migration/preflightのerrorは固定category文字列のみ。
- 内部role modelはplugin公開面へ露出しない（`shiroha-a/mk#2585`は将来のplugin化提案。本互換実装はcore機能として進め、plugin公開APIは追加しない）。
- commit・PR作成・push・mergeはユーザーの明示的な承認がある場合のみ実行する（リポジトリ規約: Claudeはコミットを自動作成しない）。承認前は検証とステージングに留める。remote設定変更・frontend submodule変更は行わない。
- commit前には`make fmt && make lint`を通すこと。

---

### Task 1: Role / RoleAssignment modelへlevel role型を追加する

**Files:**
- Modify: `internal/model/role.go:9-54`
- Create: `internal/model/role_level_test.go`

**Interfaces:**
- Produces: `model.RoleTargetManualLevel RoleTarget = "manualLevel"`
- Produces: `model.Role.LevelPolicies datatypes.JSON`（column `levelPolicies`, jsonb）
- Produces: `model.Role.CanHideProfileByUser bool`（column `canHideProfileByUser`）
- Produces: `model.RoleAssignment.Experience *int64`（column `experience`）
- Produces: `model.RoleAssignment.IsHideProfile *bool`（column `isHideProfile`）
- Produces: `model.ExperiencePolicyType`、`model.ExperiencePolicy`、`model.LevelPolicies`、`model.ParseLevelPolicies(raw []byte) (*LevelPolicies, error)`
- Consumes: 既存`datatypes.JSON`、既存`RoleTarget`/`RoleAssignment`。
- Consumers: Task 2のmigration test、Task 3のpreflight、Plan 2のlevel engine / policy。

- [ ] **Step 1: 失敗するtestを書く**

`internal/model/role_level_test.go`を新規作成する。`ParseLevelPolicies`はlevelPolicies jsonbをtyped structへ変換し、CherryPickの`{baseLevel, experiencePolicies}` shapeをそのまま読む。

```go
package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoleTargetManualLevel(t *testing.T) {
	assert.Equal(t, RoleTarget("manualLevel"), RoleTargetManualLevel)
	assert.NotEqual(t, RoleTargetManual, RoleTargetManualLevel)
}

func TestParseLevelPolicies(t *testing.T) {
	valid := `{"baseLevel":10,"experiencePolicies":[` +
		`{"level":5,"type":"const","base":100},` +
		`{"level":3,"type":"linear","base":100,"additional":50},` +
		`{"level":2,"type":"exponential","base":50,"additional":25,"exponential":2}]}`

	got, err := ParseLevelPolicies([]byte(valid))
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 10, got.BaseLevel)
	require.Len(t, got.ExperiencePolicies, 3)

	c := got.ExperiencePolicies[0]
	assert.Equal(t, 5, c.Level)
	assert.Equal(t, string(ExperiencePolicyConst), c.Type)
	assert.Equal(t, 100.0, c.Base)
	assert.Nil(t, c.Additional)

	l := got.ExperiencePolicies[1]
	assert.Equal(t, string(ExperiencePolicyLinear), l.Type)
	require.NotNil(t, l.Additional)
	assert.Equal(t, 50.0, *l.Additional)

	e := got.ExperiencePolicies[2]
	assert.Equal(t, string(ExperiencePolicyExponential), e.Type)
	require.NotNil(t, e.Exponential)
	assert.Equal(t, 2.0, *e.Exponential)
}

func TestParseLevelPolicies_Errors(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{name: "empty", raw: ""},
		{name: "invalid JSON", raw: `{invalid`},
		{name: "not object", raw: `[1,2]`},
		{name: "missing baseLevel", raw: `{"experiencePolicies":[]}`},
		{name: "missing experiencePolicies", raw: `{"baseLevel":0}`},
		{name: "baseLevel not number", raw: `{"baseLevel":"x","experiencePolicies":[]}`},
		{name: "experiencePolicies not array", raw: `{"baseLevel":0,"experiencePolicies":{}}`},
		{name: "policy type unknown", raw: `{"baseLevel":0,"experiencePolicies":[{"level":1,"type":"sqrt","base":10}]}`},
		{name: "policy level not number", raw: `{"baseLevel":0,"experiencePolicies":[{"level":"a","type":"const","base":10}]}`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseLevelPolicies([]byte(tt.raw))
			require.Error(t, err)
			assert.Nil(t, got)
		})
	}
}

func TestParseLevelPolicies_EmptyPolicies(t *testing.T) {
	got, err := ParseLevelPolicies([]byte(`{"baseLevel":0,"experiencePolicies":[]}`))
	require.NoError(t, err)
	assert.Empty(t, got.ExperiencePolicies)
}
```

- [ ] **Step 2: testがREDであることを確認する**

Run: `go test ./internal/model -run 'TestRoleTargetManualLevel|TestParseLevelPolicies' -count=1`

Expected: `undefined: RoleTargetManualLevel`／`undefined: ParseLevelPolicies`でFAILする。typoやtest harness errorによるFAILなら修正して再実行する。

- [ ] **Step 3: 最小model実装**

`internal/model/role.go`を編集する。

```go
package model

import (
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/datatypes"
)

// RoleTarget defines how a role is assigned to users.
type RoleTarget string

const (
	RoleTargetManual      RoleTarget = "manual"
	RoleTargetConditional RoleTarget = "conditional"
	// RoleTargetManualLevel is a CherryPick level role: the assignment
	// carries an experience value that maps to a level via LevelPolicies.
	RoleTargetManualLevel RoleTarget = "manualLevel"
)

// ExperiencePolicyType enumerates the level experience curve types.
type ExperiencePolicyType string

const (
	ExperiencePolicyConst       ExperiencePolicyType = "const"
	ExperiencePolicyLinear      ExperiencePolicyType = "linear"
	ExperiencePolicyExponential ExperiencePolicyType = "exponential"
)

// ExperiencePolicy is one element of levelPolicies.experiencePolicies.
// Field types match CherryPick model Role.ts: level/base are numbers,
// additional/exponential are optional numbers.
type ExperiencePolicy struct {
	Level       int      `json:"level"`
	Type        string   `json:"type"`
	Base        float64  `json:"base"`
	Additional  *float64 `json:"additional,omitempty"`
	Exponential *float64 `json:"exponential,omitempty"`
}

// LevelPolicies is the parsed shape of role.levelPolicies.
type LevelPolicies struct {
	BaseLevel          int                `json:"baseLevel"`
	ExperiencePolicies []ExperiencePolicy `json:"experiencePolicies"`
}

// ParseLevelPolicies decodes a role.levelPolicies jsonb value into the
// typed shape. The `{baseLevel, experiencePolicies}` keys must both be
// present (raw presence check: a plain struct unmarshal would silently
// default a missing baseLevel to 0). An empty experiencePolicies array is
// valid (a role pinned at baseLevel); every present element must carry an
// interpretable type.
func ParseLevelPolicies(raw []byte) (*LevelPolicies, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("level policies: empty jsonb")
	}
	// 生の key presence を先に検証する。struct への素の Unmarshal では
	// baseLevel 欠落が 0 に黙って丸まるため、`{baseLevel, experiencePolicies}`
	// の両 key の存在を map で確認する。
	var presence map[string]json.RawMessage
	if err := json.Unmarshal(raw, &presence); err != nil {
		return nil, fmt.Errorf("level policies: not a json object: %w", err)
	}
	if _, ok := presence["baseLevel"]; !ok {
		return nil, fmt.Errorf("level policies: missing baseLevel")
	}
	if _, ok := presence["experiencePolicies"]; !ok {
		return nil, fmt.Errorf("level policies: missing experiencePolicies")
	}
	// baseLevel は整数・experiencePolicies は配列でなければならない
	// (1.5 / 文字列 / null / object は typed Unmarshal が拒否する)。
	var lp LevelPolicies
	if err := json.Unmarshal(raw, &lp); err != nil {
		return nil, fmt.Errorf("level policies: invalid shape: %w", err)
	}
	if lp.ExperiencePolicies == nil {
		return nil, fmt.Errorf("level policies: experiencePolicies must be an array")
	}
	for i, p := range lp.ExperiencePolicies {
		switch ExperiencePolicyType(p.Type) {
		case ExperiencePolicyConst, ExperiencePolicyLinear, ExperiencePolicyExponential:
		default:
			return nil, fmt.Errorf("level policies: policy[%d]: unknown type %q", i, p.Type)
		}
	}
	return &lp, nil
}
```

`Role` structへ次を追加する（`Policies` fieldの直後）。

```go
	// LevelPolicies は manualLevel role の level曲線 (CherryPick role.levelPolicies)。
	LevelPolicies        datatypes.JSON `gorm:"column:levelPolicies;type:jsonb;not null;default:'{}'" json:"levelPolicies"`
	CanHideProfileByUser bool           `gorm:"column:canHideProfileByUser;not null;default:false" json:"canHideProfileByUser"`
```

`RoleAssignment` structへ次を追加する。

```go
	// Experience は manualLevel role の累積経験値。DB は bigint。
	// 外部APIで扱う値は 0..Number.MAX_SAFE_INTEGER へ制限する。
	Experience *int64 `gorm:"column:experience" json:"experience"`
	// IsHideProfile は本人が profile badge を隠した状態 (nullable)。
	IsHideProfile *bool `gorm:"column:isHideProfile" json:"isHideProfile"`
```

`import`に`encoding/json`と`fmt`を追加する（`time`と`gorm.io/datatypes`は既存）。

- [ ] **Step 4: testがGREENであることを確認する**

Run: `go test ./internal/model -run 'TestRoleTargetManualLevel|TestParseLevelPolicies' -count=1`

Expected: PASS。続けて `go test ./internal/model -count=1` でpackage全体もPASSする（既存model testを壊していない）。

- [ ] **Step 5: commit（ユーザー承認後のみ実行する）**

リポジトリ規約に従い、commitはユーザーの明示的な指示がある場合のみ実行する。

```powershell
git add -- internal/model/role.go internal/model/role_level_test.go
git diff --cached --check
git commit -m "model: CherryPick level role型を追加する"
```
---

### Task 2: 新規migration `000076_cherrypick_level_role`のup/downと冪等性・round-trip testを作成する

**Files:**
- Create: `migration/000076_cherrypick_level_role.up.sql`
- Create: `migration/000076_cherrypick_level_role.down.sql`
- Create: `internal/levelrolemigration/migration_test.go`
- Modify: `internal/repository/role_test.go`（level field round-trip）

**Interfaces:**
- Consumes: `000012_role`が作成する`role`/`role_assignment`/`role_target_enum`、Task 1のmodel field。
- Produces: `manualLevel` enum値、level columns/index、適用後shape検証付きの冪等up、column-existence-aware guardとimport index保護付きのdown。
- Produces: fresh/imported/no-op/shape-mismatchのmigration testとrepository round-trip test。
- Consumers: Task 3のpreflight、`testutil.ApplyMigrations`（全test DB）、`cmd/migrate`、Plan 3のimported DB E2E。

- [ ] **Step 1: 失敗するtestを書く**

`internal/levelrolemigration/migration_test.go`を新規作成する。このpackageは`OpenTestDB`がpackage専用schemaを割り当てるため、他testと干渉しない。`internal/repository/role_test.go`へlevel fieldのround-trip testも追加する（`internal/repository`のTestMainが全migration適用済み）。

```go
package levelrolemigration

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const upFile = "000076_cherrypick_level_role.up.sql"

// applyUpFile executes the 000076 up migration against db.
func applyUpFile(t *testing.T, db *gorm.DB) {
	t.Helper()
	sql := readMigration(t, upFile)
	require.NoError(t, db.Exec(sql).Error, "apply %s", upFile)
}

func readMigration(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "..", "migration", name)
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "read %s", name)
	return string(raw)
}

// applyAllMigrations applies every *.up.sql in order, asserting each one
// succeeds (unlike testutil.ApplyMigrations which swallows errors).
func applyAllMigrations(t *testing.T, db *gorm.DB) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join("..", "..", "migration", "*.up.sql"))
	require.NoError(t, err)
	sort.Strings(matches)
	for _, path := range matches {
		raw, err := os.ReadFile(path)
		require.NoError(t, err, "read %s", path)
		require.NoError(t, db.Exec(string(raw)).Error, "apply %s", filepath.Base(path))
	}
}

// mustColumnShape returns the data_type / is_nullable of one column.
func mustColumnShape(t *testing.T, db *gorm.DB, table, column string) (dataType string, nullable bool) {
	t.Helper()
	var dataTypeRaw, nullableRaw string
	err := db.Raw(`SELECT data_type, is_nullable FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = ? AND column_name = ?`,
		table, column).Row().Scan(&dataTypeRaw, &nullableRaw)
	require.NoError(t, err)
	return dataTypeRaw, nullableRaw == "YES"
}

// resetSchema drops and recreates the package schema so each subtest starts
// fully clean and is robust across repeated `go test` invocations (migrations
// are not all idempotent against an already-migrated schema)。
func resetSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	var schema string
	require.NoError(t, db.Raw("SELECT current_schema()").Row().Scan(&schema))
	require.NotEmpty(t, schema)
	require.NoError(t, db.Exec(`DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`).Error)
	require.NoError(t, db.Exec(`CREATE SCHEMA "`+schema+`"`).Error)
}

func TestMigration076_FreshThenNoOp(t *testing.T) {
	db, err := testutil.OpenTestDB()
	require.NoError(t, err)

	resetSchema(t, db)
	applyAllMigrations(t, db)

	dt, nullable := mustColumnShape(t, db, "role_assignment", "experience")
	assert.Equal(t, "bigint", dt)
	assert.True(t, nullable, "experience must be nullable")
	dt, nullable = mustColumnShape(t, db, "role_assignment", "isHideProfile")
	assert.Equal(t, "boolean", dt)
	assert.True(t, nullable, "isHideProfile must be nullable")
	dt, _ = mustColumnShape(t, db, "role", "levelPolicies")
	assert.Equal(t, "jsonb", dt)
	dt, _ = mustColumnShape(t, db, "role", "canHideProfileByUser")
	assert.Equal(t, "boolean", dt)

	// 2回目適用は no-change (冪等)。
	applyUpFile(t, db)

	var indexCount int
	require.NoError(t, db.Raw(`SELECT count(*) FROM pg_indexes
		WHERE schemaname = current_schema() AND tablename = 'role_assignment'
		AND indexdef ILIKE '%"experience"%'`).Row().Scan(&indexCount))
	assert.Equal(t, 1, indexCount, "exactly one index on role_assignment.experience")
}

// cherrypickRoleDDL is the imported role table shape (CherryPick migrations
// 1673500412259-Role.js + 1740640480147-roleLevelPolicies.js +
// 1749166675855-roleUserHide.js + displayOrder / policies defaults)。
const cherrypickRoleDDL = `
CREATE TYPE role_target_enum AS ENUM ('manual', 'conditional', 'manualLevel');
CREATE TABLE "role" (
    "id" varchar(32) PRIMARY KEY,
    "updatedAt" timestamptz NOT NULL DEFAULT now(),
    "lastUsedAt" timestamptz NOT NULL DEFAULT now(),
    "name" varchar(256) NOT NULL,
    "description" varchar(1024) NOT NULL DEFAULT '',
    "color" varchar(256),
    "iconUrl" varchar(512),
    "target" role_target_enum NOT NULL DEFAULT 'manual',
    "condFormula" jsonb NOT NULL DEFAULT '{}',
    "isPublic" boolean NOT NULL DEFAULT false,
    "asBadge" boolean NOT NULL DEFAULT false,
    "isModerator" boolean NOT NULL DEFAULT false,
    "isAdministrator" boolean NOT NULL DEFAULT false,
    "isExplorable" boolean NOT NULL DEFAULT false,
    "preserveAssignmentOnMoveAccount" boolean NOT NULL DEFAULT false,
    "canEditMembersByModerator" boolean NOT NULL DEFAULT false,
    "canHideProfileByUser" boolean NOT NULL DEFAULT false,
    "displayOrder" integer NOT NULL DEFAULT 0,
    "levelPolicies" jsonb NOT NULL DEFAULT '{}',
    "policies" jsonb NOT NULL DEFAULT '{}'
);
CREATE TABLE "role_assignment" (
    "id" varchar(32) PRIMARY KEY,
    "userId" varchar(32) NOT NULL,
    "roleId" varchar(32) NOT NULL,
    "expiresAt" timestamptz,
    "experience" bigint,
    "isHideProfile" boolean
);
CREATE INDEX "IDX_3a2b1f9d4e8c0f7b5a6d2e9c4f8b3" ON "role_assignment" ("experience");
`

func TestMigration076_ImportedSchemaNoOp(t *testing.T) {
	db, err := testutil.OpenTestDB()
	require.NoError(t, err)

	resetSchema(t, db)
	require.NoError(t, db.Exec(cherrypickRoleDDL).Error, "create imported schema fixture")

	applyUpFile(t, db)

	// 000076 は imported shape に対して no-op。mk-go の重複 index は作らない。
	var mkIndex int
	require.NoError(t, db.Raw(`SELECT count(*) FROM pg_indexes
		WHERE schemaname = current_schema() AND tablename = 'role_assignment'
		AND indexname = 'IDX_role_assignment_experience'`).Row().Scan(&mkIndex))
	assert.Zero(t, mkIndex, "must not duplicate the imported experience index")

	var cpIndex int
	require.NoError(t, db.Raw(`SELECT count(*) FROM pg_indexes
		WHERE schemaname = current_schema() AND tablename = 'role_assignment'
		AND indexname = 'IDX_3a2b1f9d4e8c0f7b5a6d2e9c4f8b3'`).Row().Scan(&cpIndex))
	assert.Equal(t, 1, cpIndex, "imported index must be preserved")
}

func TestMigration076_ShapeMismatchAborts(t *testing.T) {
	db, err := testutil.OpenTestDB()
	require.NoError(t, err)

	resetSchema(t, db)
	// experience が integer (mk-go 期待は bigint) の imported shape。
	require.NoError(t, db.Exec(`
		CREATE TYPE role_target_enum AS ENUM ('manual', 'conditional', 'manualLevel');
		CREATE TABLE "role" (
			"id" varchar(32) PRIMARY KEY,
			"name" varchar(256) NOT NULL,
			"target" role_target_enum NOT NULL DEFAULT 'manual',
			"levelPolicies" jsonb NOT NULL DEFAULT '{}',
			"canHideProfileByUser" boolean NOT NULL DEFAULT false,
			"policies" jsonb NOT NULL DEFAULT '{}'
		);
		CREATE TABLE "role_assignment" (
			"id" varchar(32) PRIMARY KEY,
			"roleId" varchar(32) NOT NULL,
			"userId" varchar(32) NOT NULL,
			"experience" integer
		);
	`).Error)

	// 名前が同じで shape が違う column は自動変更せず、適用後検証が abort する。
	err = db.Exec(readMigration(t, upFile)).Error
	require.Error(t, err)
	assert.Contains(t, err.Error(), "incompatible-experience-column")

	dt, _ := mustColumnShape(t, db, "role_assignment", "experience")
	assert.Equal(t, "integer", dt, "migration must not auto-change a mismatched column")
}
```

`internal/repository/role_test.go`へ追加する。

```go
func TestRoleRepository_LevelRoleFieldsRoundTrip(t *testing.T) {
	repo := NewRoleRepository(testDB)
	assignRepo := NewRoleAssignmentRepository(testDB)

	now := time.Now()
	role := &model.Role{
		ID: "role_lvl_test", UpdatedAt: now, LastUsedAt: now, Name: "Level",
		Target: model.RoleTargetManualLevel,
		LevelPolicies: datatypes.JSON([]byte(
			`{"baseLevel":10,"experiencePolicies":[{"level":5,"type":"const","base":100}]}`)),
		CanHideProfileByUser: true,
		Policies:             datatypes.JSON([]byte("{}")),
		CondFormula:          datatypes.JSON([]byte("{}")),
	}
	require.NoError(t, repo.Create(role))
	defer cleanupRole(t, role.ID)

	found, err := repo.FindByID(role.ID)
	require.NoError(t, err)
	assert.Equal(t, model.RoleTargetManualLevel, found.Target)
	assert.True(t, found.CanHideProfileByUser)

	lp, err := model.ParseLevelPolicies(found.LevelPolicies)
	require.NoError(t, err)
	assert.Equal(t, 10, lp.BaseLevel)
	require.Len(t, lp.ExperiencePolicies, 1)
	assert.Equal(t, 5, lp.ExperiencePolicies[0].Level)
	assert.Equal(t, 100.0, lp.ExperiencePolicies[0].Base)

	createTestUser(t, "lvl_u1")
	exp := int64(250)
	hide := true
	require.NoError(t, assignRepo.Create(&model.RoleAssignment{
		ID: "lvl_a1", UserID: "lvl_u1", RoleID: role.ID, Experience: &exp, IsHideProfile: &hide,
	}))
	t.Cleanup(func() { testDB.Exec(`DELETE FROM "role_assignment" WHERE id = ?`, "lvl_a1") })

	assigns, err := assignRepo.ListByUser("lvl_u1")
	require.NoError(t, err)
	require.Len(t, assigns, 1)
	require.NotNil(t, assigns[0].Experience)
	assert.Equal(t, int64(250), *assigns[0].Experience)
	require.NotNil(t, assigns[0].IsHideProfile)
	assert.True(t, *assigns[0].IsHideProfile)
}
```

- [ ] **Step 2: testがREDであることを確認する**

Run: `go test ./internal/levelrolemigration -count=1`
Run: `go test ./internal/repository -run TestRoleRepository_LevelRoleFieldsRoundTrip -count=1`

Expected: `migration/000076_cherrypick_level_role.up.sql`がまだ無いため`read migration/000076_cherrypick_level_role.up.sql: The system cannot find the file specified`でFAILし、repository testは`column "levelPolicies" does not exist`でFAILする（いずれもgenuine RED）。

- [ ] **Step 3: up/down migrationを実装する**

`migration/000076_cherrypick_level_role.up.sql`を新規作成する。全て冪等にし、適用後に列shapeを再検証して不一致ならabortする。

```sql
-- CherryPick level role互換: manualLevel target, levelPolicies,
-- canHideProfileByUser, role_assignment.experience / isHideProfile。
-- 既存 000012_role は編集せず、追加は本 migration だけで行う。
-- 全操作は冪等にし、適用後に列 shape (型・nullable・default) を再検証して
-- 不一致なら固定 category で abort する (自動変更しない)。

-- 1. role_target_enum へ 'manualLevel' を追加。プロジェクト規約の
--    duplicate_object 例外 pattern で冪等化する。pg_type の schema 非依存
--    検査は使わない。ALTER TYPE は search_path 解決で current_schema の
--    type を対象にする。
DO $$ BEGIN
    ALTER TYPE role_target_enum ADD VALUE 'manualLevel';
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- 2. role.levelPolicies (jsonb NOT NULL DEFAULT '{}')
ALTER TABLE "role" ADD COLUMN IF NOT EXISTS "levelPolicies" jsonb NOT NULL DEFAULT '{}'::jsonb;

-- 3. role.policies の default を '{}'::jsonb に揃える (CherryPick 1740640480147)。
ALTER TABLE "role" ALTER COLUMN "policies" SET DEFAULT '{}'::jsonb;

-- 4. role_assignment.experience (bigint NULL。移行時 schema を source of truth に採用)
ALTER TABLE "role_assignment" ADD COLUMN IF NOT EXISTS "experience" bigint;

-- 5. experience index。CherryPick import 済み DB は既存 index を持つため、
--    (experience) に対する index が 1 つも無い場合にだけ作成する。
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_indexes
        WHERE schemaname = current_schema()
          AND tablename = 'role_assignment'
          AND indexdef ILIKE '%"experience"%'
    ) THEN
        CREATE INDEX "IDX_role_assignment_experience" ON "role_assignment" ("experience");
    END IF;
END $$;

-- 6. role.canHideProfileByUser (boolean NOT NULL DEFAULT false)
ALTER TABLE "role" ADD COLUMN IF NOT EXISTS "canHideProfileByUser" boolean NOT NULL DEFAULT false;

-- 7. role_assignment.isHideProfile (boolean NULL)
ALTER TABLE "role_assignment" ADD COLUMN IF NOT EXISTS "isHideProfile" boolean;

-- 8. 追加後検証: 名前が同じで shape が違う column は自動変更せず固定 category で
--    abort する。fresh DB でも import 済み DB でも適用後に必ず通る。
DO $$
DECLARE
    v_type text; v_null text; v_def text;
BEGIN
    SELECT data_type, is_nullable, COALESCE(column_default, '') INTO v_type, v_null, v_def
    FROM information_schema.columns
    WHERE table_schema = current_schema() AND table_name = 'role' AND column_name = 'levelPolicies';
    IF v_type IS DISTINCT FROM 'jsonb' OR v_null IS DISTINCT FROM 'NO'
       OR v_def IS DISTINCT FROM '''{}''::jsonb' THEN
        RAISE EXCEPTION 'cherrypick level role migration: incompatible-level-policies-column';
    END IF;

    SELECT data_type, is_nullable, COALESCE(column_default, '') INTO v_type, v_null, v_def
    FROM information_schema.columns
    WHERE table_schema = current_schema() AND table_name = 'role' AND column_name = 'canHideProfileByUser';
    IF v_type IS DISTINCT FROM 'boolean' OR v_null IS DISTINCT FROM 'NO'
       OR v_def IS DISTINCT FROM 'false' THEN
        RAISE EXCEPTION 'cherrypick level role migration: incompatible-can-hide-column';
    END IF;

    SELECT data_type, is_nullable, COALESCE(column_default, '') INTO v_type, v_null, v_def
    FROM information_schema.columns
    WHERE table_schema = current_schema() AND table_name = 'role_assignment' AND column_name = 'experience';
    IF v_type IS DISTINCT FROM 'bigint' OR v_null IS DISTINCT FROM 'YES' OR v_def <> '' THEN
        RAISE EXCEPTION 'cherrypick level role migration: incompatible-experience-column';
    END IF;

    SELECT data_type, is_nullable, COALESCE(column_default, '') INTO v_type, v_null, v_def
    FROM information_schema.columns
    WHERE table_schema = current_schema() AND table_name = 'role_assignment' AND column_name = 'isHideProfile';
    IF v_type IS DISTINCT FROM 'boolean' OR v_null IS DISTINCT FROM 'YES' OR v_def <> '' THEN
        RAISE EXCEPTION 'cherrypick level role migration: incompatible-hide-column';
    END IF;
END $$;
```

`migration/000076_cherrypick_level_role.down.sql`を新規作成する。level role dataが1件でもあれば固定errorで停止し、dataが無い場合だけ除去する。optional columnが無いpartial schemaでも安全に動く。

```sql
-- CherryPick level role互換 migration の down。
-- 本番復旧手段としては使わない (片道移行方針)。data guard を通過した場合に
-- だけ追加物を除去する。guard は optional column の存在を先に確認してから
-- data を判定するため、column が無い schema でも安全に動く。

-- 1. guard: 存在する level column の data を確認し、level role data が
--    1 件でもあれば固定 error で停止する。ID・値・件数は出さない。
DO $$
DECLARE
    has_role_lp boolean; has_role_canhide boolean;
    has_assign_exp boolean; has_assign_ishide boolean;
    has_manual_label boolean;
    has_data boolean;
BEGIN
    SELECT EXISTS(SELECT 1 FROM information_schema.columns
                  WHERE table_schema = current_schema() AND table_name = 'role' AND column_name = 'levelPolicies')
        INTO has_role_lp;
    SELECT EXISTS(SELECT 1 FROM information_schema.columns
                  WHERE table_schema = current_schema() AND table_name = 'role' AND column_name = 'canHideProfileByUser')
        INTO has_role_canhide;
    SELECT EXISTS(SELECT 1 FROM information_schema.columns
                  WHERE table_schema = current_schema() AND table_name = 'role_assignment' AND column_name = 'experience')
        INTO has_assign_exp;
    SELECT EXISTS(SELECT 1 FROM information_schema.columns
                  WHERE table_schema = current_schema() AND table_name = 'role_assignment' AND column_name = 'isHideProfile')
        INTO has_assign_ishide;
    SELECT EXISTS(
        SELECT 1 FROM pg_enum e
        JOIN pg_type t ON t.oid = e.enumtypid
        JOIN pg_namespace n ON n.oid = t.typnamespace
        WHERE n.nspname = current_schema() AND t.typname = 'role_target_enum'
          AND e.enumlabel = 'manualLevel'
    ) INTO has_manual_label;

    SELECT (
        (has_manual_label AND EXISTS(SELECT 1 FROM "role" r WHERE r.target = 'manualLevel'))
        OR (has_role_lp AND EXISTS(SELECT 1 FROM "role" r WHERE r."levelPolicies" IS DISTINCT FROM '{}'::jsonb))
        OR (has_role_canhide AND EXISTS(SELECT 1 FROM "role" r WHERE r."canHideProfileByUser" = true))
        OR (has_assign_exp AND EXISTS(SELECT 1 FROM "role_assignment" a WHERE a."experience" IS NOT NULL))
        OR (has_assign_ishide AND EXISTS(SELECT 1 FROM "role_assignment" a WHERE a."isHideProfile" IS NOT NULL))
    ) INTO has_data;

    IF has_data THEN
        RAISE EXCEPTION 'cherrypick level role migration down: level role data present; rollback blocked';
    END IF;
END $$;

-- 2. 自前 index だけを除去する (import 由来 index は名指しで drop しない)。
DROP INDEX IF EXISTS "IDX_role_assignment_experience";

-- 3. experience column は、(experience) への index が残っていない場合にだけ
--    drop する。import 済み DB の CherryPick index が残る場合は column drop の
--    カスケードで index を壊さないよう column ごと残す。
DO $$
DECLARE
    has_exp_index boolean;
BEGIN
    SELECT EXISTS(
        SELECT 1 FROM pg_indexes
        WHERE schemaname = current_schema() AND tablename = 'role_assignment'
          AND indexdef ILIKE '%"experience"%'
    ) INTO has_exp_index;
    IF NOT has_exp_index THEN
        ALTER TABLE "role_assignment" DROP COLUMN IF EXISTS "experience";
    END IF;
END $$;

-- 4. 残りの追加 column を除去する。
ALTER TABLE "role_assignment" DROP COLUMN IF EXISTS "isHideProfile";
ALTER TABLE "role" DROP COLUMN IF EXISTS "levelPolicies";
ALTER TABLE "role" DROP COLUMN IF EXISTS "canHideProfileByUser";
ALTER TABLE "role" ALTER COLUMN "policies" SET DEFAULT '{}'::jsonb;

-- 5. 追加した enum value を除去する。PostgreSQL には enum の DROP VALUE が無い
--    ため rebuild/swap で戻す。target column の default は swap 前に drop し、
--    swap 後に再適用する (旧 type 参照で swap が失敗しないように)。
ALTER TABLE "role" ALTER COLUMN "target" DROP DEFAULT;
DROP TYPE IF EXISTS role_target_enum_new;
CREATE TYPE role_target_enum_new AS ENUM ('manual', 'conditional');
ALTER TABLE "role"
    ALTER COLUMN "target" TYPE role_target_enum_new
    USING "target"::text::role_target_enum_new;
DROP TYPE "role_target_enum";
ALTER TYPE role_target_enum_new RENAME TO role_target_enum;
ALTER TABLE "role" ALTER COLUMN "target" SET DEFAULT 'manual'::role_target_enum;
```

- [ ] **Step 4: 命名規則とGREENを確認する**

Run:

```powershell
$up = Get-Item -LiteralPath "migration\000076_cherrypick_level_role.up.sql"
$down = Get-Item -LiteralPath "migration\000076_cherrypick_level_role.down.sql"
if (-not ($up.Name -match '^\d+_.+\.up\.sql$')) { throw "bad up name" }
if (-not ($down.Name -match '^\d+_.+\.down\.sql$')) { throw "bad down name" }
if ($up.Length -eq 0) { throw "empty up" }
if ($down.Length -eq 0) { throw "empty down" }
Write-Output "ok"
```

Expected: `ok`。列挙順 (`000076`) は既存 `000075` の直後で衝突しない。

Run: `go test ./internal/levelrolemigration -count=1`
Run: `go test ./internal/repository -run TestRoleRepository_LevelRoleFieldsRoundTrip -count=1`

Expected: migration testはfresh 1回適用+2回目no-op、imported shapeでno-op、shape mismatchで`incompatible-experience-column` abort。round-trip testがPASSする。Docker不要（`OpenTestDB`はCI service PGまたは`.env.test`のPGへ接続）。

- [ ] **Step 5: commit（ユーザー承認後のみ実行する）**

リポジトリ規約に従い、commitはユーザーの明示的な指示がある場合のみ実行する。

```powershell
git add -- migration/000076_cherrypick_level_role.up.sql migration/000076_cherrypick_level_role.down.sql internal/levelrolemigration/migration_test.go internal/repository/role_test.go
git diff --cached --check
git commit -m "migration: CherryPick level role互換カラム・enumと冪等性testを追加する"
```


---
### Task 3: level role preflightを実装してcmd/migrateへ組み込む

**Files:**
- Create: `internal/migrationcompat/levelrole.go`
- Create: `internal/migrationcompat/levelrole_preflight_test.go`
- Modify: `cmd/migrate/main.go:58-62`

**Interfaces:**
- Consumes: `migrationcompat.Lock`とその`lockConn`（`Begin`）、既存`columnSpec`型（`preflight.go`）。
- Produces: `func (l *Lock) CheckLevelRolePreflight(ctx context.Context) error` — 不一致時は固定category文字列のみを含むerrorを返す。`role`/`role_assignment` tableが無い場合、またはlevel columnが1つも無い場合はno-opでcommitする。level columnが1つでも存在すれば既存columnのshapeを検証し、全columnが揃えばdata不変条件も検証する。
- Consumers: `cmd/migrate`の`run()`。

- [ ] **Step 1: 失敗するtestを書く**

`internal/migrationcompat/levelrole_preflight_test.go`を新規作成する。`preflight_test.go`の`preflightConn`と同じpackage schema接続patternを使う。FK制約は不要（検証はSELECT EXISTSのみ）。

```go
package migrationcompat

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// levelRoleFixtureDDL creates the level-role tables without FK constraints
// (the preflight only runs SELECT EXISTS over them).
const levelRoleFixtureDDL = `
CREATE TYPE role_target_enum AS ENUM ('manual', 'conditional', 'manualLevel');
CREATE TABLE "role" (
    "id" varchar(32) PRIMARY KEY,
    "name" varchar(256) NOT NULL,
    "target" role_target_enum NOT NULL DEFAULT 'manual',
    "levelPolicies" jsonb NOT NULL DEFAULT '{}',
    "canHideProfileByUser" boolean NOT NULL DEFAULT false,
    "policies" jsonb NOT NULL DEFAULT '{}'
);
CREATE TABLE "role_assignment" (
    "id" varchar(32) PRIMARY KEY,
    "roleId" varchar(32) NOT NULL,
    "userId" varchar(32) NOT NULL,
    "experience" bigint,
    "isHideProfile" boolean
);
`

func setupLevelRoleFixture(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	ctx := context.Background()
	_, err := conn.Exec(ctx, `DROP TABLE IF EXISTS "role_assignment", "role"`)
	require.NoError(t, err)
	// 同一 schema を subtest 間で再利用するため、enum type も drop してから作る
	// (CREATE TYPE に IF NOT EXISTS は無い)。
	_, err = conn.Exec(ctx, `DROP TYPE IF EXISTS role_target_enum`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, levelRoleFixtureDDL)
	require.NoError(t, err)
}

func mustLevelRoleConn(t *testing.T) *pgx.Conn {
	t.Helper()
	db, err := testutil.OpenTestDB()
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer func() { _ = sqlDB.Close() }()

	var schema string
	require.NoError(t, db.Raw("SELECT current_schema()").Row().Scan(&schema))
	cfg, err := pgx.ParseConfig(testDBURL(t))
	require.NoError(t, err)
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	conn, err := pgx.ConnectConfig(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	_, err = conn.Exec(context.Background(), `SET search_path TO `+pgx.Identifier{schema}.Sanitize())
	require.NoError(t, err)
	return conn
}

func TestCheckLevelRolePreflight_NoTablesIsNoOp(t *testing.T) {
	conn := mustLevelRoleConn(t)
	_, err := conn.Exec(context.Background(), `DROP TABLE IF EXISTS "role_assignment", "role"`)
	require.NoError(t, err)
	_, err = conn.Exec(context.Background(), `DROP TYPE IF EXISTS role_target_enum`)
	require.NoError(t, err)
	lock := &Lock{conn: conn}
	require.NoError(t, lock.CheckLevelRolePreflight(context.Background()))
}

func TestCheckLevelRolePreflight_MissingColumnsIsNoOp(t *testing.T) {
	conn := mustLevelRoleConn(t)
	_, err := conn.Exec(context.Background(), `
		DROP TABLE IF EXISTS "role_assignment", "role";
		DROP TYPE IF EXISTS role_target_enum;
		CREATE TABLE "role" ("id" varchar(32) PRIMARY KEY, "name" varchar(256) NOT NULL);
		CREATE TABLE "role_assignment" ("id" varchar(32) PRIMARY KEY);
	`)
	require.NoError(t, err)
	lock := &Lock{conn: conn}
	require.NoError(t, lock.CheckLevelRolePreflight(context.Background()))
}

func TestCheckLevelRolePreflight_PartialSchemaValidatesExistingShape(t *testing.T) {
	conn := mustLevelRoleConn(t)
	// levelPolicies だけが正しい shape で存在する partial schema。
	_, err := conn.Exec(context.Background(), `
		DROP TABLE IF EXISTS "role_assignment", "role";
		DROP TYPE IF EXISTS role_target_enum;
		CREATE TYPE role_target_enum AS ENUM ('manual', 'conditional', 'manualLevel');
		CREATE TABLE "role" ("id" varchar(32) PRIMARY KEY, "name" varchar(256) NOT NULL,
			"target" role_target_enum NOT NULL DEFAULT 'manual',
			"levelPolicies" jsonb NOT NULL DEFAULT '{}',
			"policies" jsonb NOT NULL DEFAULT '{}');
		CREATE TABLE "role_assignment" ("id" varchar(32) PRIMARY KEY);
	`)
	require.NoError(t, err)
	lock := &Lock{conn: conn}
	require.NoError(t, lock.CheckLevelRolePreflight(context.Background()),
		"存在する column の shape が正しければ partial schema でも no-error")
}

func TestCheckLevelRolePreflight_PartialSchemaWrongShapeAborts(t *testing.T) {
	conn := mustLevelRoleConn(t)
	// levelPolicies が text (jsonb 期待) で存在する partial schema → abort。
	_, err := conn.Exec(context.Background(), `
		DROP TABLE IF EXISTS "role_assignment", "role";
		DROP TYPE IF EXISTS role_target_enum;
		CREATE TYPE role_target_enum AS ENUM ('manual', 'conditional', 'manualLevel');
		CREATE TABLE "role" ("id" varchar(32) PRIMARY KEY, "name" varchar(256) NOT NULL,
			"target" role_target_enum NOT NULL DEFAULT 'manual',
			"levelPolicies" text,
			"policies" jsonb NOT NULL DEFAULT '{}');
		CREATE TABLE "role_assignment" ("id" varchar(32) PRIMARY KEY);
	`)
	require.NoError(t, err)
	lock := &Lock{conn: conn}
	err = lock.CheckLevelRolePreflight(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "incompatible-column")
}

func TestCheckLevelRolePreflight_ValidImportedDataPasses(t *testing.T) {
	conn := mustLevelRoleConn(t)
	setupLevelRoleFixture(t, conn)
	_, err := conn.Exec(context.Background(), `
		INSERT INTO "role" (id, name, target, "levelPolicies", "canHideProfileByUser") VALUES
		('r1', 'Level', 'manualLevel',
		 '{"baseLevel":10,"experiencePolicies":[{"level":5,"type":"const","base":100}]}'::jsonb,
		 true);
		INSERT INTO "role" (id, name, target) VALUES ('r2', 'Manual', 'manual');
		INSERT INTO "role_assignment" (id, "roleId", "userId", experience, "isHideProfile") VALUES
		('a1', 'r1', 'u1', 250, true);
	`)
	require.NoError(t, err)
	lock := &Lock{conn: conn}
	require.NoError(t, lock.CheckLevelRolePreflight(context.Background()))
}

func TestCheckLevelRolePreflight_LevelDataOnNonManualRole(t *testing.T) {
	conn := mustLevelRoleConn(t)
	setupLevelRoleFixture(t, conn)
	_, err := conn.Exec(context.Background(), `
		INSERT INTO "role" (id, name, target, "levelPolicies") VALUES
		('r1', 'Manual', 'manual',
		 '{"baseLevel":1,"experiencePolicies":[{"level":1,"type":"const","base":10}]}'::jsonb);
	`)
	require.NoError(t, err)
	lock := &Lock{conn: conn}
	err = lock.CheckLevelRolePreflight(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "level-data-on-non-manual-role")
}

func TestCheckLevelRolePreflight_InvalidLevelPolicies(t *testing.T) {
	conn := mustLevelRoleConn(t)
	setupLevelRoleFixture(t, conn)
	_, err := conn.Exec(context.Background(), `
		INSERT INTO "role" (id, name, target, "levelPolicies") VALUES
		('r1', 'Level', 'manualLevel', '{}'::jsonb);
	`)
	require.NoError(t, err)
	lock := &Lock{conn: conn}
	err = lock.CheckLevelRolePreflight(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid-level-policies")
}

func TestCheckLevelRolePreflight_InvalidExperiencePolicy(t *testing.T) {
	conn := mustLevelRoleConn(t)
	setupLevelRoleFixture(t, conn)
	_, err := conn.Exec(context.Background(), `
		INSERT INTO "role" (id, name, target, "levelPolicies") VALUES
		('r1', 'Level', 'manualLevel',
		 '{"baseLevel":0,"experiencePolicies":[{"level":1,"type":"sqrt","base":10}]}'::jsonb);
	`)
	require.NoError(t, err)
	lock := &Lock{conn: conn}
	err = lock.CheckLevelRolePreflight(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid-experience-policy")
}

func TestCheckLevelRolePreflight_InvalidPolicyAsLevel(t *testing.T) {
	conn := mustLevelRoleConn(t)
	setupLevelRoleFixture(t, conn)
	_, err := conn.Exec(context.Background(), `
		INSERT INTO "role" (id, name, target, "levelPolicies", "policies") VALUES
		('r1', 'Level', 'manualLevel',
		 '{"baseLevel":0,"experiencePolicies":[{"level":1,"type":"const","base":10}]}'::jsonb,
		 '{"canPublicNote":{"useDefault":true,"priority":1,"value":false,
		    "policyAsLevel":[{"level":0,"type":"const","base":true}]}}'::jsonb);
	`)
	require.NoError(t, err)
	lock := &Lock{conn: conn}
	err = lock.CheckLevelRolePreflight(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid-policy-as-level")
}

func TestCheckLevelRolePreflight_ExperienceOutOfRange(t *testing.T) {
	conn := mustLevelRoleConn(t)
	setupLevelRoleFixture(t, conn)
	_, err := conn.Exec(context.Background(), `
		INSERT INTO "role" (id, name, target) VALUES ('r1', 'Level', 'manualLevel');
		INSERT INTO "role_assignment" (id, "roleId", "userId", experience) VALUES
		('a1', 'r1', 'u1', 9007199254740992);
	`)
	require.NoError(t, err)
	lock := &Lock{conn: conn}
	err = lock.CheckLevelRolePreflight(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid-assignment-experience")
}

func TestCheckLevelRolePreflight_HideReferencesUnhideableRole(t *testing.T) {
	conn := mustLevelRoleConn(t)
	setupLevelRoleFixture(t, conn)
	_, err := conn.Exec(context.Background(), `
		INSERT INTO "role" (id, name, target, "canHideProfileByUser") VALUES
		('r1', 'Level', 'manualLevel', false);
		INSERT INTO "role_assignment" (id, "roleId", "userId", "isHideProfile") VALUES
		('a1', 'r1', 'u1', true);
	`)
	require.NoError(t, err)
	lock := &Lock{conn: conn}
	err = lock.CheckLevelRolePreflight(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid-profile-hide-reference")
}

func TestCheckLevelRolePreflight_ShapeMismatch(t *testing.T) {
	conn := mustLevelRoleConn(t)
	_, err := conn.Exec(context.Background(), `
		DROP TABLE IF EXISTS "role_assignment", "role";
		DROP TYPE IF EXISTS role_target_enum;
		CREATE TYPE role_target_enum AS ENUM ('manual', 'conditional', 'manualLevel');
		CREATE TABLE "role" (
			"id" varchar(32) PRIMARY KEY,
			"name" varchar(256) NOT NULL,
			"target" role_target_enum NOT NULL DEFAULT 'manual',
			"levelPolicies" jsonb NOT NULL DEFAULT '{}',
			"canHideProfileByUser" boolean NOT NULL DEFAULT false,
			"policies" jsonb NOT NULL DEFAULT '{}'
		);
		CREATE TABLE "role_assignment" (
			"id" varchar(32) PRIMARY KEY,
			"roleId" varchar(32) NOT NULL,
			"userId" varchar(32) NOT NULL,
			"experience" integer
		);
	`)
	require.NoError(t, err)
	lock := &Lock{conn: conn}
	err = lock.CheckLevelRolePreflight(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "incompatible-column")
}
```

- [ ] **Step 2: testがREDであることを確認する**

Run: `go test ./internal/migrationcompat -run TestCheckLevelRolePreflight -count=1`

Expected: `CheckLevelRolePreflight`未定義でcompile error→FAIL。`pgx.Identifier`等のimportが合わなければ修正して再実行する。

- [ ] **Step 3: 最小preflightを実装する**

`internal/migrationcompat/levelrole.go`を新規作成する。

```go
package migrationcompat

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// levelRoleCategories は CheckLevelRolePreflight が返す固定category。
// 本番由来ID・値・件数は一切含めない。
const (
	levelRoleCatIncompatibleColumn          = "incompatible-column"
	levelRoleCatLevelDataOnNonManual        = "level-data-on-non-manual-role"
	levelRoleCatInvalidLevelPolicies        = "invalid-level-policies"
	levelRoleCatInvalidExperiencePolicy     = "invalid-experience-policy"
	levelRoleCatInvalidPolicyAsLevel        = "invalid-policy-as-level"
	levelRoleCatInvalidAssignmentExperience = "invalid-assignment-experience"
	levelRoleCatInvalidProfileHideReference = "invalid-profile-hide-reference"
)

// levelRoleMaxExperience は外部APIで扱う experience の上限
// (Number.MAX_SAFE_INTEGER = 9007199254740991)。SQL 側でも同値を定数で使う。
const levelRoleMaxExperience int64 = 9007199254740991

// levelRoleColumnSpecs は import 済み DB で検証する level column shape。
var levelRoleColumnSpecs = []columnSpec{
	{table: "role", column: "levelPolicies", dataType: "jsonb", maxLen: 0, nullable: false},
	{table: "role", column: "canHideProfileByUser", dataType: "boolean", maxLen: 0, nullable: false},
	{table: "role_assignment", column: "experience", dataType: "bigint", maxLen: 0, nullable: true},
	{table: "role_assignment", column: "isHideProfile", dataType: "boolean", maxLen: 0, nullable: true},
}

// CheckLevelRolePreflight は 000076_cherrypick_level_role の適用前に
// CherryPick 由来 level role data の不変条件を検出専用で検証する。
//
//   - role / role_assignment table が無い fresh DB → no-op
//   - level column が1つも無い mk-go 既存 schema → no-op (000076 が追加)
//   - level column が1つでも存在する partial schema → 存在する column の
//     shape を検証し、不一致なら abort (skip しない)
//   - 全 level column が揃った import 済み DB → shape に加えて data 不変条件
//     を検証する
//
// data 不変条件:
//  1. manualLevel role だけが level data を持つこと
//  2. levelPolicies が {baseLevel, experiencePolicies} として解釈できること
//     (baseLevel は 0..MaxInt64 の整数)
//  3. experience policy の type が const/linear/exponential、level/base/
//     additional/exponential が許容範囲にあること
//  4. policyAsLevel の構造 (type / level / base / additional) が valid であること
//  5. assignment experience が非負かつ Number.MAX_SAFE_INTEGER 以下
//  6. isHideProfile=true の assignment が canHideProfileByUser=true の
//     role を参照すること
//
// 許容範囲 (engine と共通): level >= 1 の整数、base >= 0、additional >= 0、
// exponential > 0、いずれも有限 (float64 表現可能、< 1.7976931348623157e308)。
// JSON は NaN/Inf を持たないため numeric で評価し、float64 表現不能な巨大値は
// 範囲外として検出する。不正 JSON 上の cast は CASE ガードで評価しない
// (unsafe cast を実行しない)。
//
// 不一致時は固定categoryだけを返し、ID・値・件数・table名・column名・role ID・
// user ID を出力しない。1つでも category に該当すれば migration を開始しない
// (fail-closed)。
func (l *Lock) CheckLevelRolePreflight(ctx context.Context) error {
	tx, err := l.conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin level role migration preflight: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. table 存在確認。揃う前の schema はそのまま commit する。
	var hasRole, hasAssignment bool
	if err := tx.QueryRow(ctx, `
		SELECT (to_regclass('"role"') IS NOT NULL),
		       (to_regclass('role_assignment') IS NOT NULL)
	`).Scan(&hasRole, &hasAssignment); err != nil {
		return fmt.Errorf("check level role preflight tables: %w", err)
	}
	if !hasRole || !hasAssignment {
		return tx.Commit(ctx)
	}

	// 2. level column 存在確認。
	var hasLevelPolicies, hasExp, hasHide, hasCanHide bool
	if err := tx.QueryRow(ctx, `
		SELECT
			bool_or((table_name = 'role' AND column_name = 'levelPolicies')),
			bool_or((table_name = 'role_assignment' AND column_name = 'experience')),
			bool_or((table_name = 'role_assignment' AND column_name = 'isHideProfile')),
			bool_or((table_name = 'role' AND column_name = 'canHideProfileByUser'))
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name IN ('role', 'role_assignment')
		  AND column_name IN ('levelPolicies', 'experience', 'isHideProfile', 'canHideProfileByUser')
	`).Scan(&hasLevelPolicies, &hasExp, &hasHide, &hasCanHide); err != nil {
		return fmt.Errorf("check level role preflight columns: %w", err)
	}
	if !hasLevelPolicies && !hasExp && !hasHide && !hasCanHide {
		// 000076 が全 column を追加する fresh / 旧 mk-go schema。
		return tx.Commit(ctx)
	}

	// 3. 存在する level column の shape 検証。partial schema でも既存 column の
	//    不一致は abort する (skip しない)。shape は型・nullable を検証し、
	//    default は migration の適用後検証が担う。
	if err := checkLevelRoleColumns(ctx, tx); err != nil {
		return err
	}

	// 4. 全 column が揃った import 済み DB だけ data 不変条件を検証する。
	if !hasLevelPolicies || !hasExp || !hasHide || !hasCanHide {
		return tx.Commit(ctx)
	}
	checks := []struct {
		category string
		query    string
	}{
		{levelRoleCatLevelDataOnNonManual, `
			SELECT EXISTS(
				SELECT 1 FROM "role" r
				WHERE r.target IS DISTINCT FROM 'manualLevel'
				  AND r."levelPolicies" IS DISTINCT FROM '{}'::jsonb
			)`},
		{levelRoleCatInvalidLevelPolicies, `
			SELECT EXISTS(
				SELECT 1 FROM "role" r
				WHERE r.target = 'manualLevel'
				  AND (
					jsonb_typeof(r."levelPolicies") IS DISTINCT FROM 'object'
					OR NOT (r."levelPolicies" ? 'baseLevel')
					OR NOT (r."levelPolicies" ? 'experiencePolicies')
					OR jsonb_typeof(r."levelPolicies"->'baseLevel') IS DISTINCT FROM 'number'
					OR CASE WHEN jsonb_typeof(r."levelPolicies"->'baseLevel') = 'number'
							THEN (r."levelPolicies"->>'baseLevel')::numeric ELSE NULL END < 0
					OR CASE WHEN jsonb_typeof(r."levelPolicies"->'baseLevel') = 'number'
							THEN (r."levelPolicies"->>'baseLevel')::numeric ELSE NULL END
						 != floor(CASE WHEN jsonb_typeof(r."levelPolicies"->'baseLevel') = 'number'
										THEN (r."levelPolicies"->>'baseLevel')::numeric ELSE NULL END)
					OR jsonb_typeof(r."levelPolicies"->'experiencePolicies') IS DISTINCT FROM 'array'
				  )
			)`},
		{levelRoleCatInvalidExperiencePolicy, `
			SELECT EXISTS(
				SELECT 1 FROM "role" r
				CROSS JOIN LATERAL jsonb_array_elements(
					CASE WHEN jsonb_typeof(r."levelPolicies"->'experiencePolicies') = 'array'
						 THEN r."levelPolicies"->'experiencePolicies' ELSE '[]'::jsonb END
				) p
				WHERE r.target = 'manualLevel'
				  AND (
					(p->>'type') NOT IN ('const','linear','exponential')
					OR jsonb_typeof(p->'level') IS DISTINCT FROM 'number'
					OR CASE WHEN jsonb_typeof(p->'level') = 'number' THEN (p->>'level')::numeric ELSE NULL END < 1
					OR CASE WHEN jsonb_typeof(p->'level') = 'number' THEN (p->>'level')::numeric ELSE NULL END
						 != floor(CASE WHEN jsonb_typeof(p->'level') = 'number' THEN (p->>'level')::numeric ELSE NULL END)
					OR CASE WHEN jsonb_typeof(p->'level') = 'number' THEN (p->>'level')::numeric ELSE NULL END
						 > 9223372036854775807
					OR jsonb_typeof(p->'base') IS DISTINCT FROM 'number'
					OR CASE WHEN jsonb_typeof(p->'base') = 'number' THEN (p->>'base')::numeric ELSE NULL END < 0
					OR CASE WHEN jsonb_typeof(p->'base') = 'number' THEN (p->>'base')::numeric ELSE NULL END
						 >= 1.7976931348623157e308
					OR ((p->>'type') IN ('linear','exponential') AND (
						jsonb_typeof(p->'additional') IS DISTINCT FROM 'number'
						OR CASE WHEN jsonb_typeof(p->'additional') = 'number' THEN (p->>'additional')::numeric ELSE NULL END < 0
						OR CASE WHEN jsonb_typeof(p->'additional') = 'number' THEN (p->>'additional')::numeric ELSE NULL END
							 >= 1.7976931348623157e308))
					OR ((p->>'type') = 'exponential' AND (
						jsonb_typeof(p->'exponential') IS DISTINCT FROM 'number'
						OR CASE WHEN jsonb_typeof(p->'exponential') = 'number' THEN (p->>'exponential')::numeric ELSE NULL END <= 0
						OR CASE WHEN jsonb_typeof(p->'exponential') = 'number' THEN (p->>'exponential')::numeric ELSE NULL END
							 >= 1.7976931348623157e308))
				  )
			)`},
		{levelRoleCatInvalidPolicyAsLevel, `
			SELECT EXISTS(
				SELECT 1 FROM "role" r
				WHERE r.target = 'manualLevel'
				  AND r."policies" IS NOT NULL
				  AND EXISTS (
					  SELECT 1 FROM jsonb_each(r."policies") pol
					  WHERE pol.value ? 'policyAsLevel'
						AND jsonb_typeof(pol.value->'policyAsLevel') = 'array'
						AND EXISTS (
							SELECT 1 FROM jsonb_array_elements(pol.value->'policyAsLevel') pa
							WHERE (pa->>'type') NOT IN ('base','const','multiplier')
							   OR jsonb_typeof(pa->'level') IS DISTINCT FROM 'number'
							   OR CASE WHEN jsonb_typeof(pa->'level') = 'number' THEN (pa->>'level')::numeric ELSE NULL END < 1
							   OR CASE WHEN jsonb_typeof(pa->'level') = 'number' THEN (pa->>'level')::numeric ELSE NULL END
									!= floor(CASE WHEN jsonb_typeof(pa->'level') = 'number' THEN (pa->>'level')::numeric ELSE NULL END)
							   OR ((pa->>'type') = 'const' AND NOT (pa ? 'base'))
							   OR ((pa->>'type') = 'multiplier' AND (
									jsonb_typeof(pa->'base') IS DISTINCT FROM 'number'
									OR jsonb_typeof(pa->'additional') IS DISTINCT FROM 'number'))
						)
				  )
			)`},
		{levelRoleCatInvalidAssignmentExperience, fmt.Sprintf(`
			SELECT EXISTS(
				SELECT 1 FROM "role_assignment" a
				WHERE a."experience" IS NOT NULL
				  AND (a."experience" < 0 OR a."experience" > %d)
			)`, levelRoleMaxExperience)},
		{levelRoleCatInvalidProfileHideReference, `
			SELECT EXISTS(
				SELECT 1 FROM "role_assignment" a
				JOIN "role" r ON r.id = a."roleId"
				WHERE a."isHideProfile" = true
				  AND r."canHideProfileByUser" = false
			)`},
	}
	for _, c := range checks {
		var violated bool
		if err := tx.QueryRow(ctx, c.query).Scan(&violated); err != nil {
			return fmt.Errorf("level role preflight query (%s): %w", c.category, err)
		}
		if violated {
			return fmt.Errorf("level role preflight: %s", c.category)
		}
	}
	return tx.Commit(ctx)
}

// checkLevelRoleColumns validates the shapes (data type + nullability) of the
// level columns that exist. Any mismatch aborts with the fixed category only
// (no table / column names in the error)。default の検証は migration の適用後
// 検証が担う。
func checkLevelRoleColumns(ctx context.Context, tx pgx.Tx) error {
	actual := make(map[string]columnSpec)
	rows, err := tx.Query(ctx, `
		SELECT table_name, column_name, data_type,
		       COALESCE(character_maximum_length, 0), is_nullable
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name IN ('role', 'role_assignment')
		  AND column_name IN ('levelPolicies','canHideProfileByUser','experience','isHideProfile')
	`)
	if err != nil {
		return fmt.Errorf("read level role preflight columns: %w", err)
	}
	for rows.Next() {
		var table, column, dataType, nullable string
		var maxLen int
		if err := rows.Scan(&table, &column, &dataType, &maxLen, &nullable); err != nil {
			rows.Close()
			return fmt.Errorf("scan level role preflight column: %w", err)
		}
		actual[table+"."+column] = columnSpec{
			table: table, column: column, dataType: dataType, maxLen: maxLen, nullable: nullable == "YES",
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate level role preflight columns: %w", err)
	}
	// 存在する column だけを検証する (partial schema でも skip しない)。
	for _, spec := range levelRoleColumnSpecs {
		info, ok := actual[spec.table+"."+spec.column]
		if !ok {
			continue
		}
		if info.dataType != spec.dataType || info.nullable != spec.nullable ||
			(spec.maxLen != 0 && info.maxLen != spec.maxLen) {
			return fmt.Errorf("level role preflight: %s", levelRoleCatIncompatibleColumn)
		}
	}
	return nil
}
```

`cmd/migrate/main.go`の`run()`で、`CheckPreflight`の直後に呼ぶ。

```go
	if *direction == "up" {
		if err := lock.CheckPreflight(ctx); err != nil {
			return fmt.Errorf("migration compatibility preflight failed: %w", err)
		}
		if err := lock.CheckLevelRolePreflight(ctx); err != nil {
			return fmt.Errorf("level role migration preflight failed: %w", err)
		}
	}
```

- [ ] **Step 4: testがGREENであることを確認する**

Run: `go test ./internal/migrationcompat -run TestCheckLevelRolePreflight -count=1`

Expected: 12 testがPASS（no-op 2、partial schema 2、valid 1、各data不変条件 5、shape mismatch 1、policyAsLevel 1）。`cmd/migrate`がコンパイルできることを確認する。

Run: `go build ./...`

Expected: exit 0。

- [ ] **Step 5: commit（ユーザー承認後のみ実行する）**

リポジトリ規約に従い、commitはユーザーの明示的な指示がある場合のみ実行する。

```powershell
git add -- internal/migrationcompat/levelrole.go internal/migrationcompat/levelrole_preflight_test.go cmd/migrate/main.go
git diff --cached --check
git commit -m "migration: level role preflightを追加してcmd/migrateへ組み込む"
```

---

### Task 4: PR検証・privacy gate・controller handoffを実行する

**Files:**
- Consume: Task 1-3の成果物。

**Interfaces:**
- Consumes: Task 1のmodel、Task 2のmigration（round-trip test含む）、Task 3のpreflight。
- Produces: `Backend PR 1`としてレビュー可能な検証済み状態（commit・PR作成はユーザー承認後のみ）。

- [ ] **Step 1: 全gateを実行する**

Run:

```powershell
make fmt
make lint
go build ./...
go test ./internal/model ./internal/migrationcompat ./internal/levelrolemigration ./internal/repository -count=1
go test ./internal/core/role ./internal/api/roles ./internal/api/admin -count=1
```

Expected: 全てexit 0。`internal/core/role`など既存role系のnon-regressionもPASSする（model追加による破壊がないこと）。

- [ ] **Step 2: privacy gateを実行する**

変更ファイルを本番由来値（16桁以上の固定hex、64桁hex hash、`[A-Za-z]:\` の絶対path）で走査し、match本文は表示せずcategory countだけを扱う。

```powershell
$changed = @(
  'internal/model/role.go',
  'internal/model/role_level_test.go',
  'migration/000076_cherrypick_level_role.up.sql',
  'migration/000076_cherrypick_level_role.down.sql',
  'internal/levelrolemigration/migration_test.go',
  'internal/migrationcompat/levelrole.go',
  'internal/migrationcompat/levelrole_preflight_test.go',
  'internal/repository/role_test.go',
  'cmd/migrate/main.go'
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

Expected: exit 0。個別matchを表示しない。`error ID`は本planでは一切使わないため、上記scanに引っかかる値が無いこと。

- [ ] **Step 3: commit（ユーザー承認後のみ実行する）**

リポジトリ規約に従い、commitはユーザーの明示的な指示がある場合のみ実行する。

```powershell
git add -- internal/model/role.go internal/model/role_level_test.go migration/000076_cherrypick_level_role.up.sql migration/000076_cherrypick_level_role.down.sql internal/levelrolemigration/migration_test.go internal/migrationcompat/levelrole.go internal/migrationcompat/levelrole_preflight_test.go internal/repository/role_test.go cmd/migrate/main.go
git diff --cached --check
git commit -m "phase: CherryPick level role schema・modelを追加する"
```

- [ ] **Step 4: controllerへhandoffする**

`Backend PR 1`のPR作成・push・mergeは、ユーザーが明示的に指示した場合のみ実施する。本planでは自動で`gh pr create`を実行しない。PR baseは`Misaki-Project/mk:Misaki-develop`を想定し、タイトル・本文は日本語、本文の`Closes`には実装開始前に作成した対応Issue番号を指定する。

Run:

```powershell
git log --oneline -5
git status --short
```

Expected: Task 1-3のcommitが上から並び、working treeがcleanであること。
