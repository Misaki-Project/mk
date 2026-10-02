package rolelevel

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/shiroha-a/mk/plugin"
)

// manifest の `name` と `Definition.Name` がずれると、ルートが
// /api/plugin/<manifest名> に生えたのに admin/server-plugins の名前が別になる。
func TestPluginNameMatchesManifest(t *testing.T) {
	if Plugin.Name != "role-level" {
		t.Fatalf("Definition.Name = %q, want %q", Plugin.Name, "role-level")
	}
	if Plugin.APIVersion != plugin.APIVersion {
		t.Fatalf("APIVersion = %d, want %d", Plugin.APIVersion, plugin.APIVersion)
	}
	if err := Plugin.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(Plugin.Migrations) == 0 {
		t.Fatal("Migrations が空です")
	}
	if Plugin.Peered || Plugin.Peer != nil {
		t.Fatal("rolelevel は peer を使わないので Peered / Peer は立てない")
	}
}

// migration はplugin所有tableをversionedかつtransactionalに作る。
func TestMigrationsCreatePluginTables(t *testing.T) {
	db := testDB(t)
	newHarness(t, db, nil).Routes(Plugin)

	for _, table := range []string{
		"role_level_config", "role_level_experience",
		"role_level_operation", "role_level_audit",
		"role_level_profile_visibility",
		"role_level_legacy_import",
	} {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM information_schema.tables
			WHERE table_schema = current_schema() AND table_name = $1`, table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("%s が作られていません", table)
		}
	}
}

func TestLegacyMigrationPolicyAllowlistMatchesCatalog(t *testing.T) {
	var legacySQL string
	for _, migration := range Plugin.Migrations {
		if migration.Version == 6 {
			legacySQL = migration.SQL
		}
	}
	if legacySQL == "" {
		t.Fatal("legacy migration 6が見つかりません")
	}
	for _, key := range defaultCatalog.Keys() {
		// Migration 6の後に追加したpolicyは旧データに存在しない。
		// 新policyのために適用済みmigrationを書き換えない。
		if key == "genshinRefreshIntervalMinutes" {
			if strings.Contains(legacySQL, "'"+key+"'") {
				t.Fatal("新policyが適用済みlegacy migrationへ混入しています")
			}
			continue
		}
		if !strings.Contains(legacySQL, "'"+key+"'") {
			t.Fatalf("legacy migration policy allowlist is missing %q", key)
		}
	}
	if !strings.Contains(legacySQL, "role_level_legacy_import") {
		t.Fatal("legacy migration does not archive its complete source")
	}
}

// migration は冪等。production は起動のたびに呼ぶので、2回目が already exists で
// 落ちたら起動不能になる。
func TestMigrationsAreIdempotent(t *testing.T) {
	db := testDB(t)
	h := newHarness(t, db, nil)
	h.Routes(Plugin)
	h.Routes(Plugin)
}

func TestMigrationCreatesRequiredIndexes(t *testing.T) {
	db := testDB(t)
	newHarness(t, db, nil).Routes(Plugin)

	for name, want := range map[string]string{
		"role_level_experience_role_user_idx":    `(role_id, user_id)`,
		"role_level_experience_user_idx":         `(user_id)`,
		"role_level_experience_role_rank_idx":    `(role_id, experience DESC, assignment_id)`,
		"role_level_operation_status_idx":        `(status, updated_at)`,
		"role_level_audit_role_created_idx":      `(role_id, created_at DESC)`,
		"role_level_audit_user_created_idx":      `(user_id, created_at DESC)`,
		"role_level_profile_visibility_user_idx": `(user_id)`,
	} {
		var definition string
		err := db.QueryRow(`SELECT indexdef FROM pg_indexes
			WHERE schemaname = current_schema() AND indexname = $1`, name).Scan(&definition)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(definition, want) {
			t.Fatalf("%s = %q, want %q", name, definition, want)
		}
	}
}

func TestMigrationEnforcesExperienceBounds(t *testing.T) {
	db := testDB(t)
	newHarness(t, db, nil).Routes(Plugin)

	for _, experience := range []int64{0, 9007199254740991} {
		_, err := db.Exec(`INSERT INTO role_level_experience
			(assignment_id, role_id, user_id, experience) VALUES ($1, 'role', 'user', $2)`,
			fmt.Sprintf("valid-%d", experience), experience)
		if err != nil {
			t.Fatalf("valid experience %d was rejected: %v", experience, err)
		}
	}
	for _, experience := range []int64{-1, 9007199254740992} {
		_, err := db.Exec(`INSERT INTO role_level_experience
			(assignment_id, role_id, user_id, experience) VALUES ($1, 'role', 'user', $2)`,
			fmt.Sprintf("invalid-%d", experience), experience)
		if err == nil {
			t.Fatalf("out-of-range experience %d was accepted", experience)
		}
	}
}

func TestMigrationEnforcesDesiredExperienceBounds(t *testing.T) {
	db := testDB(t)
	newHarness(t, db, nil).Routes(Plugin)

	for i, desired := range []any{nil, int64(0), int64(9007199254740991)} {
		_, err := db.Exec(`INSERT INTO role_level_operation
			(idempotency_key, actor_id, user_id, role_id, mode, operand, desired_exp, status)
			VALUES ($1, 'actor', 'user', 'role', 'set', 0, $2, 'pending')`,
			fmt.Sprintf("valid-desired-%d", i), desired)
		if err != nil {
			t.Fatalf("valid desired experience %v was rejected: %v", desired, err)
		}
	}
	for i, desired := range []int64{-1, 9007199254740992} {
		_, err := db.Exec(`INSERT INTO role_level_operation
			(idempotency_key, actor_id, user_id, role_id, mode, operand, desired_exp, status)
			VALUES ($1, 'actor', 'user', 'role', 'set', 0, $2, 'pending')`,
			fmt.Sprintf("invalid-desired-%d", i), desired)
		if err == nil {
			t.Fatalf("out-of-range desired experience %d was accepted", desired)
		}
	}
}

func TestMigrationAcceptsEveryFiniteOperand(t *testing.T) {
	db := testDB(t)
	newHarness(t, db, nil).Routes(Plugin)

	for i, operand := range []float64{-math.MaxFloat64, -9007199254740991, 9007199254740991, math.MaxFloat64} {
		_, err := db.Exec(`INSERT INTO role_level_operation
			(idempotency_key, actor_id, user_id, role_id, mode, operand, status)
			VALUES ($1, 'actor', 'user', 'role', 'set', $2, 'pending')`,
			fmt.Sprintf("finite-%d", i), operand)
		if err != nil {
			t.Fatalf("finite operand %v was rejected: %v", operand, err)
		}
	}
}

func TestMigrationRejectsNonFiniteOperands(t *testing.T) {
	db := testDB(t)
	newHarness(t, db, nil).Routes(Plugin)

	for i, operand := range []float64{math.NaN(), math.Inf(-1), math.Inf(1)} {
		_, err := db.Exec(`INSERT INTO role_level_operation
			(idempotency_key, actor_id, user_id, role_id, mode, operand, status)
			VALUES ($1, 'actor', 'user', 'role', 'set', $2, 'pending')`,
			fmt.Sprintf("non-finite-%d", i), operand)
		if err == nil {
			t.Fatalf("non-finite operand %v was accepted", operand)
		}
	}
}

func TestMigrationRollsBackFailedVersion(t *testing.T) {
	db := testDB(t)
	h := newHarness(t, db, nil)
	h.Routes(Plugin)

	version := probeMigrationVersion()
	err := h.Context().Storage().Migrate(t.Context(), []plugin.Migration{{Version: version, SQL: `
		CREATE TABLE role_level_rollback_probe (id bigint);
		SELECT 1 / 0;
	`}})
	if err == nil {
		t.Fatal("failing migration succeeded")
	}
	var exists bool
	if err := db.QueryRow(`SELECT to_regclass(current_schema() || '.role_level_rollback_probe') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("failed migration left role_level_rollback_probe behind")
	}
	// **記録も残さない。** 残ると次回の起動が「適用済み」と読み飛ばし、失敗した
	// migration が二度と実行されない (成功したように見える) 状態になる。
	var recorded bool
	if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, version).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded {
		t.Fatalf("failed migration %d was recorded as applied", version)
	}
}

// probeMigrationVersion is a version no declared migration uses.
//
// **probe の version を固定しない。** 固定値 (2 など) にすると、Plugin が同じ
// version を宣言した時点で probe が「適用済み」判定になって実行されず、テストが
// 「failing migration succeeded」で落ち、rollback の検証ではなく番号の重複だけを
// 報告してしまう。宣言済み migration の次の version なら、あとから何版を足しても
// 衝突しない。
func probeMigrationVersion() int {
	highest := 0
	for _, m := range Plugin.Migrations {
		if m.Version > highest {
			highest = m.Version
		}
	}
	return highest + 1
}

// config の範囲違反は **起動を止める。** 黙って既定値で動かせない。
func TestLoadConfigRejectsOutOfRange(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  map[string]any
	}{
		{"assignmentScanPages 0", map[string]any{"assignmentScanPages": 0}},
		{"assignmentScanPages 201", map[string]any{"assignmentScanPages": 201}},
		{"orphanRetentionDays 0", map[string]any{"orphanRetentionDays": 0}},
		{"orphanRetentionDays 3651", map[string]any{"orphanRetentionDays": 3651}},
		{"reconcileCron 4 fields", map[string]any{"reconcileCron": "*/10 * * *"}},
		{"orphanCron 6 fields", map[string]any{"orphanCron": "17 3 * * * *"}},
		{"pruneCron に不正文字", map[string]any{"pruneCron": "43 4 * * *; rm"}},
		{"actorId の形式が不正", map[string]any{"actorId": "bad id"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := loadConfig(plugintestContext(t, tt.cfg)); err == nil {
				t.Fatal("範囲外の設定を受け入れています")
			}
		})
	}
}

// actorId 未設定でも **起動は止めない。** read系の経路は動き、native を要する操作
// だけが stable code を返す。
func TestLoadConfigAllowsMissingActor(t *testing.T) {
	cfg, err := loadConfig(plugintestContext(t, nil))
	if err != nil {
		t.Fatalf("actorId 無しで起動を止めています: %v", err)
	}
	if cfg.ActorID != "" {
		t.Fatalf("actorId = %q, want empty", cfg.ActorID)
	}
	if cfg.AssignmentScanPages != 50 || cfg.OrphanRetentionDays != 30 {
		t.Fatalf("既定値が効きません: %+v", cfg)
	}
	if cfg.ReconcileCron != "*/10 * * * *" || cfg.OrphanCron != "17 3 * * *" || cfg.PruneCron != "43 4 * * *" {
		t.Fatalf("cron の既定値が効きません: %+v", cfg)
	}
}
