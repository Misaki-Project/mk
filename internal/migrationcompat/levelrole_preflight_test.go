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
	// manualLevel role には valid な levelPolicies を入れておく。'{}' のまま
	// だと invalid-level-policies が先に発火し、対象の experience 範囲検査まで
	// 到達しない (LevelDataOnNonManual / InvalidLevelPolicies と同じ brief の
	// 不整合を fixture 側で解消する)。
	_, err := conn.Exec(context.Background(), `
		INSERT INTO "role" (id, name, target, "levelPolicies") VALUES
		('r1', 'Level', 'manualLevel',
		 '{"baseLevel":0,"experiencePolicies":[]}'::jsonb);
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
	// manualLevel role には valid な levelPolicies を入れておく。'{}' のまま
	// だと 6 より先の invalid-level-policies が先に発火し、対象の
	// invalid-profile-hide-reference まで到達しない (brief の SQL とテストの
	// 組み合わせの不整合を fixture 側で解消する)。
	_, err := conn.Exec(context.Background(), `
		INSERT INTO "role" (id, name, target, "levelPolicies", "canHideProfileByUser") VALUES
		('r1', 'Level', 'manualLevel',
		 '{"baseLevel":0,"experiencePolicies":[]}'::jsonb, false);
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

// TestCheckLevelRolePreflight_MalformedLevelPoliciesJSON proves malformed,
// null, string, and array levelPolicies values are detected without any
// unguarded cast: 不正 JSON 上の cast は CASE ガードの外では実行しない。
func TestCheckLevelRolePreflight_MalformedLevelPoliciesJSON(t *testing.T) {
	cases := []struct {
		name     string
		policies string
	}{
		{"null", `'null'::jsonb`},
		{"string", `'"level-data"'::jsonb`},
		{"array", `'[1,2]'::jsonb`},
		{"baseLevel null", `'{"baseLevel":null,"experiencePolicies":[]}'::jsonb`},
		{"baseLevel string", `'{"baseLevel":"10","experiencePolicies":[]}'::jsonb`},
		{"baseLevel fractional", `'{"baseLevel":1.5,"experiencePolicies":[]}'::jsonb`},
		{"baseLevel negative", `'{"baseLevel":-1,"experiencePolicies":[]}'::jsonb`},
		{"experiencePolicies object", `'{"baseLevel":0,"experiencePolicies":{}}'::jsonb`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn := mustLevelRoleConn(t)
			setupLevelRoleFixture(t, conn)
			_, err := conn.Exec(context.Background(), `
				INSERT INTO "role" (id, name, target, "levelPolicies") VALUES
				('r1', 'Level', 'manualLevel', `+tc.policies+`);
			`)
			require.NoError(t, err)
			lock := &Lock{conn: conn}
			err = lock.CheckLevelRolePreflight(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid-level-policies")
		})
	}
}

// TestCheckLevelRolePreflight_MalformedExperiencePolicyJSON proves every
// experience policy value (type / level / base / additional / exponential)
// is range-checked and non-number shapes are rejected inside the guarded
// lateral expressions only.
func TestCheckLevelRolePreflight_MalformedExperiencePolicyJSON(t *testing.T) {
	cases := []struct {
		name     string
		json     string
		wantPart string
	}{
		{"level null", `[{"level":null,"type":"const","base":10}]`, "invalid-experience-policy"},
		{"level string", `[{"level":"a","type":"const","base":10}]`, "invalid-experience-policy"},
		{"level zero", `[{"level":0,"type":"const","base":10}]`, "invalid-experience-policy"},
		{"level fractional", `[{"level":1.5,"type":"const","base":10}]`, "invalid-experience-policy"},
		{"base null", `[{"level":1,"type":"const","base":null}]`, "invalid-experience-policy"},
		{"base string", `[{"level":1,"type":"const","base":"x"}]`, "invalid-experience-policy"},
		{"base negative", `[{"level":1,"type":"const","base":-1}]`, "invalid-experience-policy"},
		{"additional negative", `[{"level":1,"type":"linear","base":10,"additional":-1}]`, "invalid-experience-policy"},
		{"additional string", `[{"level":1,"type":"linear","base":10,"additional":"x"}]`, "invalid-experience-policy"},
		{"exponential zero", `[{"level":1,"type":"exponential","base":10,"exponential":0}]`, "invalid-experience-policy"},
		{"exponential negative", `[{"level":1,"type":"exponential","base":10,"exponential":-1}]`, "invalid-experience-policy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn := mustLevelRoleConn(t)
			setupLevelRoleFixture(t, conn)
			_, err := conn.Exec(context.Background(), `
				INSERT INTO "role" (id, name, target, "levelPolicies") VALUES
				('r1', 'Level', 'manualLevel',
				 '{"baseLevel":0,"experiencePolicies":`+tc.json+`}'::jsonb);
			`)
			require.NoError(t, err)
			lock := &Lock{conn: conn}
			err = lock.CheckLevelRolePreflight(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantPart)
		})
	}
}

// TestCheckLevelRolePreflight_MalformedPolicyAsLevel proves unknown policyAsLevel
// types, non-number levels, and const entries without base are rejected.
func TestCheckLevelRolePreflight_MalformedPolicyAsLevel(t *testing.T) {
	cases := []struct {
		name string
		json string
	}{
		{"unknown type", `[{"level":1,"type":"weird","base":10}]`},
		{"level string", `[{"level":"a","type":"const","base":10}]`},
		{"level zero", `[{"level":0,"type":"const","base":10}]`},
		{"const without base", `[{"level":1,"type":"const"}]`},
		{"multiplier base not number", `[{"level":1,"type":"multiplier","base":true,"additional":1}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn := mustLevelRoleConn(t)
			setupLevelRoleFixture(t, conn)
			_, err := conn.Exec(context.Background(), `
				INSERT INTO "role" (id, name, target, "levelPolicies", "policies") VALUES
				('r1', 'Level', 'manualLevel',
				 '{"baseLevel":0,"experiencePolicies":[{"level":1,"type":"const","base":10}]}'::jsonb,
				 '{"canPublicNote":{"policyAsLevel":`+tc.json+`}}'::jsonb);
			`)
			require.NoError(t, err)
			lock := &Lock{conn: conn}
			err = lock.CheckLevelRolePreflight(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid-policy-as-level")
		})
	}
}

// TestCheckLevelRolePreflight_WrongColumnDefaultAborts proves the preflight
// validates column defaults (not only type/nullability): a present level
// column whose default differs from the migration's target shape aborts even
// though the migration would refuse to auto-change it.
func TestCheckLevelRolePreflight_WrongColumnDefaultAborts(t *testing.T) {
	cases := []struct {
		name   string
		alter  string
		target string
	}{
		{"levelPolicies default", `ALTER TABLE "role" ALTER COLUMN "levelPolicies" SET DEFAULT '[]'::jsonb`, `"role"`},
		{"canHideProfileByUser default", `ALTER TABLE "role" ALTER COLUMN "canHideProfileByUser" SET DEFAULT true`, `"role"`},
		{"experience must have no default", `ALTER TABLE "role_assignment" ALTER COLUMN "experience" SET DEFAULT 0`, `"role_assignment"`},
		{"policies default", `ALTER TABLE "role" ALTER COLUMN "policies" SET DEFAULT 'null'::jsonb`, `"role"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn := mustLevelRoleConn(t)
			setupLevelRoleFixture(t, conn)
			_, err := conn.Exec(context.Background(), tc.alter)
			require.NoError(t, err)
			lock := &Lock{conn: conn}
			err = lock.CheckLevelRolePreflight(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), "incompatible-column")
		})
	}
}

// TestCheckLevelRolePreflight_BeginError proves a connection-level Begin
// failure fails closed without touching the database.
func TestCheckLevelRolePreflight_BeginError(t *testing.T) {
	lock := &Lock{conn: &fakeConn{}}

	err := lock.CheckLevelRolePreflight(context.Background())

	require.ErrorIs(t, err, errScanSentinel)
	require.Contains(t, err.Error(), "begin level role migration preflight")
}

// TestCheckLevelRolePreflightFailsClosedOnTransactionalStepError proves every
// transactional step's error body fails closed with the fixed step context and
// the wrapped sentinel (実DB上で failure までの手順は正しく実行される)。
func TestCheckLevelRolePreflightFailsClosedOnTransactionalStepError(t *testing.T) {
	cases := []struct {
		name     string
		failOn   int
		wantPart string
	}{
		{"table existence check", 1, "check level role preflight tables"},
		{"column existence check", 2, "check level role preflight columns"},
		{"column definition read", 3, "read level role preflight columns"},
		{"data invariant check", 4, "level role preflight query"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn := mustLevelRoleConn(t)
			ctx := context.Background()
			setupLevelRoleFixture(t, conn)

			lock := &Lock{conn: &stepFailingConn{conn: conn, failOn: tc.failOn}}
			err := lock.CheckLevelRolePreflight(ctx)

			require.ErrorIs(t, err, errScanSentinel)
			require.Contains(t, err.Error(), tc.wantPart)
		})
	}
}

// TestCheckLevelRolePreflight_FixedCategoryErrorLeaksNoIdentifiers proves the
// error surface is exactly the fixed category: no role/user/assignment IDs and
// no values leak. (固定 category 文字列自体に "role"/"experience" という部分文字列
// が含まれるのは設計どおりで、ここで検査するのは実データ識別子と値のみ。)
func TestCheckLevelRolePreflight_FixedCategoryErrorLeaksNoIdentifiers(t *testing.T) {
	conn := mustLevelRoleConn(t)
	setupLevelRoleFixture(t, conn)
	_, err := conn.Exec(context.Background(), `
		INSERT INTO "role" (id, name, target, "levelPolicies") VALUES
		('r1', 'Level', 'manualLevel', '{"baseLevel":0,"experiencePolicies":[]}'::jsonb);
		INSERT INTO "role_assignment" (id, "roleId", "userId", experience) VALUES
		('a1', 'r1', 'u1', 9007199254740992);
	`)
	require.NoError(t, err)
	lock := &Lock{conn: conn}
	err = lock.CheckLevelRolePreflight(context.Background())
	require.Error(t, err)
	for _, leaked := range []string{"r1", "a1", "u1", "9007199254740992"} {
		assert.NotContains(t, err.Error(), leaked, "error must not leak %q", leaked)
	}
	assert.Equal(t, "level role preflight: invalid-assignment-experience", err.Error())
}
