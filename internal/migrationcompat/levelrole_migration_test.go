package migrationcompat

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
const downFile = "000076_cherrypick_level_role.down.sql"

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

// columnExists reports whether a column is present in the package schema.
func columnExists(t *testing.T, db *gorm.DB, table, column string) bool {
	t.Helper()
	var count int
	require.NoError(t, db.Raw(`SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = ? AND column_name = ?`,
		table, column).Row().Scan(&count))
	return count == 1
}

// resetSchema drops and recreates the package schema so each subtest starts
// fully clean and is robust across repeated `go test` invocations (migrations
// are not all idempotent against an already-migrated schema)。
//
// このパッケージは migrationcompat の他 test (preflight / lock) と schema を
// 共有するが、preflight は対象 table を毎回 drop/recreate して自前 fixture を
// 張り、lock は advisory lock しか使わないため、全 migration を適用する本 test
// の schema 再作成と干渉しない。
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
		AND indexdef ILIKE '%(experience)%'`).Row().Scan(&indexCount))
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

// TestMigration076_DownRestoresPre076Shape is a DB-backed down round-trip:
// a fresh mk-go schema (000012 role + 000076 additions) after down returns to
// the pre-076 shape — the enum regains only manual/conditional with the
// default restored, and the four added columns plus the self-created
// experience index are gone.
func TestMigration076_DownRestoresPre076Shape(t *testing.T) {
	db, err := testutil.OpenTestDB()
	require.NoError(t, err)

	resetSchema(t, db)
	applyAllMigrations(t, db)

	require.True(t, columnExists(t, db, "role", "levelPolicies"), "up-added column must exist before down")
	require.True(t, columnExists(t, db, "role_assignment", "experience"), "up-added column must exist before down")

	require.NoError(t, db.Exec(readMigration(t, downFile)).Error, "apply %s on fresh migrated schema", downFile)

	var enumLabels string
	require.NoError(t, db.Raw(`
		SELECT COALESCE(string_agg(e.enumlabel, ',' ORDER BY e.enumsortorder), '')
		FROM pg_enum e
		JOIN pg_type t ON t.oid = e.enumtypid
		JOIN pg_namespace n ON n.oid = t.typnamespace
		WHERE n.nspname = current_schema() AND t.typname = 'role_target_enum'`).Row().Scan(&enumLabels))
	assert.Equal(t, "manual,conditional", enumLabels, "manualLevel must be removed on down")

	var targetDefault string
	require.NoError(t, db.Raw(`SELECT column_default FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'role' AND column_name = 'target'`).Row().Scan(&targetDefault))
	assert.Equal(t, "'manual'::role_target_enum", targetDefault, "target default restored to manual")

	for _, tc := range []struct{ table, column string }{
		{"role", "levelPolicies"},
		{"role", "canHideProfileByUser"},
		{"role_assignment", "experience"},
		{"role_assignment", "isHideProfile"},
	} {
		assert.False(t, columnExists(t, db, tc.table, tc.column),
			"column %s.%s must be absent after down", tc.table, tc.column)
	}

	var indexCount int
	require.NoError(t, db.Raw(`SELECT count(*) FROM pg_indexes
		WHERE schemaname = current_schema() AND tablename = 'role_assignment'
		AND indexname = 'IDX_role_assignment_experience'`).Row().Scan(&indexCount))
	assert.Zero(t, indexCount, "self-created experience index removed on down")
}

// TestMigration076_DownDataGuardProhibitsRollback proves the down SQL's data
// guard fails closed when a manualLevel role exists: the fixed error is raised
// and both the schema and the row are left untouched.
func TestMigration076_DownDataGuardProhibitsRollback(t *testing.T) {
	db, err := testutil.OpenTestDB()
	require.NoError(t, err)

	resetSchema(t, db)
	applyAllMigrations(t, db)

	require.NoError(t, db.Exec(`INSERT INTO "role" (id, name, target, "levelPolicies") VALUES
		('down-guard-r1', 'Level', 'manualLevel', '{"baseLevel":0,"experiencePolicies":[]}'::jsonb)`).Error)

	err = db.Exec(readMigration(t, downFile)).Error
	require.Error(t, err, "down must be blocked when level role data exists")
	assert.Contains(t, err.Error(), "cherrypick level role migration down: level role data present; rollback blocked")

	assert.True(t, columnExists(t, db, "role", "levelPolicies"), "schema must remain intact after blocked down")
	var count int
	require.NoError(t, db.Raw(`SELECT count(*) FROM "role" WHERE id = 'down-guard-r1'`).Row().Scan(&count))
	assert.Equal(t, 1, count, "the manualLevel role row must remain after blocked down")
}

// TestMigration076_DownPreservesImportedIndexAndExperience proves that on an
// empty imported CherryPick schema, down removes only the safely-owned
// additions while preserving the differently named CherryPick experience index
// and, because that index still references (experience), the experience column
// itself.
func TestMigration076_DownPreservesImportedIndexAndExperience(t *testing.T) {
	db, err := testutil.OpenTestDB()
	require.NoError(t, err)

	resetSchema(t, db)
	require.NoError(t, db.Exec(cherrypickRoleDDL).Error, "create imported schema fixture")
	applyUpFile(t, db)
	require.NoError(t, db.Exec(readMigration(t, downFile)).Error, "apply down on empty imported schema")

	var cpIndex int
	require.NoError(t, db.Raw(`SELECT count(*) FROM pg_indexes
		WHERE schemaname = current_schema() AND tablename = 'role_assignment'
		AND indexname = 'IDX_3a2b1f9d4e8c0f7b5a6d2e9c4f8b3'`).Row().Scan(&cpIndex))
	assert.Equal(t, 1, cpIndex, "CherryPick experience index must be preserved on down")

	assert.True(t, columnExists(t, db, "role_assignment", "experience"),
		"experience column must be preserved while its index references it")

	for _, tc := range []struct{ table, column string }{
		{"role", "levelPolicies"},
		{"role", "canHideProfileByUser"},
		{"role_assignment", "isHideProfile"},
	} {
		assert.False(t, columnExists(t, db, tc.table, tc.column),
			"cleared owned addition %s.%s must be gone after down", tc.table, tc.column)
	}

	var enumLabels string
	require.NoError(t, db.Raw(`
		SELECT COALESCE(string_agg(e.enumlabel, ',' ORDER BY e.enumsortorder), '')
		FROM pg_enum e
		JOIN pg_type t ON t.oid = e.enumtypid
		JOIN pg_namespace n ON n.oid = t.typnamespace
		WHERE n.nspname = current_schema() AND t.typname = 'role_target_enum'`).Row().Scan(&enumLabels))
	assert.Equal(t, "manual,conditional", enumLabels, "manualLevel must be removed on down")
}
