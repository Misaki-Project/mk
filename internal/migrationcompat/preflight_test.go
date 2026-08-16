package migrationcompat

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/require"
)

// preflightUserDDL is the focused CherryPick "user" table shape CheckPreflight
// consumes. 実装に一致するよう、消費するカラムと型・nullability をそのまま
// 複製した fixture schema であり、本番 dump の該当テーブルを縮小したもの。
const preflightUserDDL = `CREATE TABLE "user" (
    "id" varchar(32) PRIMARY KEY,
    "avatarId" varchar(32),
    "bannerId" varchar(32),
    "avatarDecorations" jsonb NOT NULL DEFAULT '[]'::jsonb
)`

// preflightDriveFileDDL is the focused CherryPick drive_file table shape.
const preflightDriveFileDDL = `CREATE TABLE drive_file (
    "id" varchar(32) PRIMARY KEY
)`

// preflightAvatarDecorationDDL is the focused CherryPick avatar_decoration
// table shape.
const preflightAvatarDecorationDDL = `CREATE TABLE avatar_decoration (
    "id" varchar(32) PRIMARY KEY
)`

// preflightConn opens the package-isolated test schema on a dedicated pgx
// connection and pins its search_path to that schema before CheckPreflight runs.
func preflightConn(t *testing.T) *pgx.Conn {
	t.Helper()
	db, err := testutil.OpenTestDB()
	require.NoError(t, err, "open package-isolated test DB")
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer func() { _ = sqlDB.Close() }()

	var schema string
	require.NoError(t, db.Raw("SELECT current_schema()").Row().Scan(&schema))
	require.NotEmpty(t, schema, "package-isolated schema must be non-empty")

	cfg, err := pgx.ParseConfig(testDBURL(t))
	require.NoError(t, err)
	// 複数 subtest が同じ接続で CREATE/ALTER を繰り返すため、pgx の
	// auto-prepared statement cache を無効化する。GORM の PreferSimpleProtocol
	// と同じ理由で、cached plan must not change result type (0A000) を避ける。
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	conn, err := pgx.ConnectConfig(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	// testDBURL は search_path を持たないため、接続後にパッケージ専用
	// schema へ固定する。schema 名は callerPackage 由来で [a-z0-9_] のみ。
	_, err = conn.Exec(context.Background(), `SET search_path TO `+pgx.Identifier{schema}.Sanitize())
	require.NoError(t, err, "pin search_path to package schema")
	return conn
}

// recreatePreflightTables drops and recreates only the focused tables so each
// subtest starts from a clean, isolated fixture.
func recreatePreflightTables(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	ctx := context.Background()
	_, err := conn.Exec(ctx, `DROP TABLE IF EXISTS "user", drive_file, avatar_decoration`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, preflightUserDDL)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, preflightDriveFileDDL)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, preflightAvatarDecorationDDL)
	require.NoError(t, err)
}

// seedValidPreflightRows inserts a fully valid user together with the drive
// files and decorations it references, so a compatible schema has zero orphans.
func seedValidPreflightRows(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	ctx := context.Background()
	_, err := conn.Exec(ctx, `INSERT INTO avatar_decoration ("id")
		VALUES ('synthetic-decoration-1'), ('synthetic-decoration-2')`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO drive_file ("id")
		VALUES ('synthetic-file-1'), ('synthetic-file-2')`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO "user" ("id", "avatarId", "bannerId", "avatarDecorations")
		VALUES ('synthetic-user-1', 'synthetic-file-1', 'synthetic-file-2',
		        '[{"id":"synthetic-decoration-1"},{"id":"synthetic-decoration-2"}]')`)
	require.NoError(t, err)
}

// snapshotPreflightRows renders every row of the focused tables so an abort can
// prove byte-for-byte that no row changed.
func snapshotPreflightRows(t *testing.T, conn *pgx.Conn) string {
	t.Helper()
	ctx := context.Background()
	var out strings.Builder

	rows, err := conn.Query(ctx, `SELECT "id", "avatarId", "bannerId", "avatarDecorations" FROM "user" ORDER BY "id"`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var id, avatarID, bannerID *string
		var decorations []byte
		require.NoError(t, rows.Scan(&id, &avatarID, &bannerID, &decorations))
		fmt.Fprintf(&out, "u:%s|%s|%s|%s\n",
			orNil(id), orNil(avatarID), orNil(bannerID), decorations)
	}
	require.NoError(t, rows.Err())

	frows, err := conn.Query(ctx, `SELECT "id" FROM drive_file ORDER BY "id"`)
	require.NoError(t, err)
	defer frows.Close()
	for frows.Next() {
		var id *string
		require.NoError(t, frows.Scan(&id))
		fmt.Fprintf(&out, "f:%s\n", orNil(id))
	}
	require.NoError(t, frows.Err())

	drows, err := conn.Query(ctx, `SELECT "id" FROM avatar_decoration ORDER BY "id"`)
	require.NoError(t, err)
	defer drows.Close()
	for drows.Next() {
		var id *string
		require.NoError(t, drows.Scan(&id))
		fmt.Fprintf(&out, "d:%s\n", orNil(id))
	}
	require.NoError(t, drows.Err())

	return out.String()
}

// orNil renders a nullable text column unambiguously (NULL vs empty string).
func orNil(s *string) string {
	if s == nil {
		return "<NULL>"
	}
	return *s
}

// TestCheckPreflight proves the detection contract: a compatible schema without
// orphans passes untouched, while every detectable defect fails closed without
// mutating a single row.
func TestCheckPreflight(t *testing.T) {
	conn := preflightConn(t)
	ctx := context.Background()

	t.Run("compatible schema without orphans succeeds without mutation", func(t *testing.T) {
		recreatePreflightTables(t, conn)
		seedValidPreflightRows(t, conn)
		before := snapshotPreflightRows(t, conn)

		require.NoError(t, (&Lock{conn: conn}).CheckPreflight(ctx))
		require.Equal(t, before, snapshotPreflightRows(t, conn))
	})

	for _, tc := range []struct {
		name     string
		seedSQL  string
		wantPart string
	}{
		{"orphan decoration", `INSERT INTO "user" ("id", "avatarDecorations") VALUES ('synthetic-user-a', '[{"id":"synthetic-decoration-missing"}]')`, "orphan avatar decoration references"},
		{"orphan avatar", `INSERT INTO "user" ("id", "avatarId", "avatarDecorations") VALUES ('synthetic-user-b', 'synthetic-file-missing-a', '[]')`, "orphan avatar references"},
		{"orphan banner", `INSERT INTO "user" ("id", "bannerId", "avatarDecorations") VALUES ('synthetic-user-c', 'synthetic-file-missing-b', '[]')`, "orphan banner references"},
		{"invalid row shape", `INSERT INTO "user" ("id", "avatarDecorations") VALUES ('synthetic-user-d', 'null')`, "invalid avatar decoration JSON"},
		{"invalid item shape", `INSERT INTO "user" ("id", "avatarDecorations") VALUES ('synthetic-user-e', '[42]')`, "invalid avatar decoration item"},
	} {
		t.Run(tc.name+" fails without mutation", func(t *testing.T) {
			recreatePreflightTables(t, conn)
			seedValidPreflightRows(t, conn)
			_, err := conn.Exec(ctx, tc.seedSQL)
			require.NoError(t, err)
			before := snapshotPreflightRows(t, conn)

			err = (&Lock{conn: conn}).CheckPreflight(ctx)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantPart)
			require.Equal(t, before, snapshotPreflightRows(t, conn))
		})
	}
}

// TestCheckPreflightRejectsIncompatibleSchema proves the schema-mismatch
// branches of the detection contract fail closed: an incomplete lifecycle
// schema (fewer than all three tables) is a clean no-op so repeated incremental
// `-steps` runs can keep applying migrations, and once all three tables exist
// every consumed column must match type/length/nullability before any row is
// read.
func TestCheckPreflightRejectsIncompatibleSchema(t *testing.T) {
	conn := preflightConn(t)
	ctx := context.Background()

	t.Run("all three tables absent is a clean no-op", func(t *testing.T) {
		recreatePreflightTables(t, conn)
		_, err := conn.Exec(ctx, `DROP TABLE IF EXISTS "user", drive_file, avatar_decoration`)
		require.NoError(t, err)

		require.NoError(t, (&Lock{conn: conn}).CheckPreflight(ctx))
	})

	// CheckPreflight runs before every migration step, so every schema shape a
	// clean database passes through between migrations 0 and N must commit
	// without blocking the next `-steps 1` call.
	t.Run("partial lifecycle schemas are a clean no-op", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			drop string
		}{
			{"only user exists", `drive_file, avatar_decoration`},
			{"user and drive_file exist", `avatar_decoration`},
			{"only avatar_decoration exists", `"user", drive_file`},
			{"drive_file and avatar_decoration exist", `"user"`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				recreatePreflightTables(t, conn)
				seedValidPreflightRows(t, conn)
				_, err := conn.Exec(ctx, `DROP TABLE IF EXISTS `+tc.drop)
				require.NoError(t, err)

				require.NoError(t, (&Lock{conn: conn}).CheckPreflight(ctx))
			})
		}
	})

	t.Run("missing consumed column aborts", func(t *testing.T) {
		recreatePreflightTables(t, conn)
		seedValidPreflightRows(t, conn)
		_, err := conn.Exec(ctx, `ALTER TABLE "user" DROP COLUMN "bannerId"`)
		require.NoError(t, err)

		err = (&Lock{conn: conn}).CheckPreflight(ctx)
		require.Error(t, err)
		require.Contains(t, err.Error(), "missing column")
	})

	t.Run("incompatible consumed column aborts", func(t *testing.T) {
		recreatePreflightTables(t, conn)
		seedValidPreflightRows(t, conn)
		_, err := conn.Exec(ctx, `ALTER TABLE "user" ALTER COLUMN "avatarId" TYPE varchar(64)`)
		require.NoError(t, err)

		err = (&Lock{conn: conn}).CheckPreflight(ctx)
		require.Error(t, err)
		require.Contains(t, err.Error(), "incompatible column")
	})
}

// TestCheckPreflightBeginError proves a connection-level Begin failure fails
// closed without touching the database.
func TestCheckPreflightBeginError(t *testing.T) {
	lock := &Lock{conn: &fakeConn{}}

	err := lock.CheckPreflight(context.Background())

	require.ErrorIs(t, err, errScanSentinel)
}

// failRow fails every Scan; it simulates a transactional step error.
type failRow struct{}

func (failRow) Scan(dest ...any) error { return errScanSentinel }

// stepFailingTx runs real SQL against a real transaction but makes exactly the
// failOn-th Query / QueryRow / Exec call fail. これにより失敗地点より前の
// 手順は実DBで正しく実行された上で、各エラー分岐が fail-closed になることを
// 実データ相手に検証できる。
type stepFailingTx struct {
	pgx.Tx
	failOn int
	step   int
}

func (t *stepFailingTx) next() bool {
	t.step++
	return t.step == t.failOn
}

func (t *stepFailingTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if t.next() {
		return failRow{}
	}
	return t.Tx.QueryRow(ctx, sql, args...)
}

func (t *stepFailingTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if t.next() {
		return nil, errScanSentinel
	}
	return t.Tx.Query(ctx, sql, args...)
}

func (t *stepFailingTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if t.next() {
		return pgconn.CommandTag{}, errScanSentinel
	}
	return t.Tx.Exec(ctx, sql, args...)
}

// stepFailingConn wraps a real connection so Begin hands out a stepFailingTx.
type stepFailingConn struct {
	conn   *pgx.Conn
	failOn int
}

func (c *stepFailingConn) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return c.conn.QueryRow(ctx, sql, args...)
}

func (c *stepFailingConn) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := c.conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &stepFailingTx{Tx: tx, failOn: c.failOn}, nil
}

func (c *stepFailingConn) Close(ctx context.Context) error {
	return c.conn.Close(ctx)
}

// TestCheckPreflightFailsClosedOnTransactionalStepError proves every
// transactional step's error body fails closed: CheckPreflight returns the step
// error and the deferred rollback leaves the valid fixture byte-for-byte
// unchanged.
func TestCheckPreflightFailsClosedOnTransactionalStepError(t *testing.T) {
	cases := []struct {
		name     string
		failOn   int
		wantPart string
	}{
		{"table existence check", 1, "check migration compatibility tables"},
		{"column definition read", 2, "read migration compatibility columns"},
		{"table lock", 3, "lock migration compatibility tables"},
		{"orphan count", 4, "count migration compatibility orphans"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn := preflightConn(t)
			ctx := context.Background()
			recreatePreflightTables(t, conn)
			seedValidPreflightRows(t, conn)

			before := snapshotPreflightRows(t, conn)
			lock := &Lock{conn: &stepFailingConn{conn: conn, failOn: tc.failOn}}
			err := lock.CheckPreflight(ctx)

			require.ErrorIs(t, err, errScanSentinel)
			require.Contains(t, err.Error(), tc.wantPart)
			require.Equal(t, before, snapshotPreflightRows(t, conn), "failed transactional step must roll back every row")
		})
	}
}

// TestMigrateMainPreflightOrdering is a source contract: cmd/migrate must call
// CheckPreflight after Acquire, before migrate.New, and only for direction "up".
// down 方向で preflight を呼ばないことを、呼び出し順序の形で固定する。
func TestMigrateMainPreflightOrdering(t *testing.T) {
	src := readFileOrFail(t, filepath.Join(repoRoot(t), "cmd", "migrate", "main.go"))

	acquireIdx := strings.Index(src, "migrationcompat.Acquire(ctx, dbURL)")
	preflightIdx := strings.Index(src, "lock.CheckPreflight(ctx)")
	migrateIdx := strings.Index(src, `migrate.New("file://migration", dbURL)`)
	require.GreaterOrEqual(t, acquireIdx, 0, "cmd/migrate must acquire the migration lock")
	require.GreaterOrEqual(t, preflightIdx, 0, "cmd/migrate must call lock.CheckPreflight")
	require.GreaterOrEqual(t, migrateIdx, 0, "cmd/migrate must create the golang-migrate migrator")

	require.Less(t, acquireIdx, preflightIdx, "CheckPreflight must run after Acquire")
	require.Less(t, preflightIdx, migrateIdx, "CheckPreflight must run before migrate.New")
	require.Contains(t, src[:preflightIdx], `if *direction == "up" {`, "CheckPreflight must sit inside the up guard")
}

// repoRoot finds the module root by walking up until go.mod is found.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	d := wd
	for d != "" {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	t.Fatalf("go.mod not found from %s", wd)
	return ""
}

func readFileOrFail(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}
