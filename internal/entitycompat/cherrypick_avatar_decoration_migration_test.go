package entitycompat

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// cherrypickAvatarDecorationUpMigration is the Task 5 migration under test.
// The test loads only this file, never the whole migration chain, so the
// dedicated package schema stays under the test's control.
var cherrypickAvatarDecorationUpMigration = filepath.Join(
	"..", "..", "migration", "000075_cherrypick_avatar_decoration_cleanup.up.sql",
)

// cherrypickDecorationDDL mirrors the CherryPick avatar_decoration / user
// shape the migration consumes: avatar_decoration.host distinguishes remote
// rows, and user.avatarDecorations is the jsonb reference array.
const cherrypickDecorationDDL = `
CREATE TABLE IF NOT EXISTS avatar_decoration (
    "id" varchar(32) PRIMARY KEY,
    "updatedAt" timestamp with time zone,
    "url" varchar(1024) NOT NULL,
    "name" varchar(256) NOT NULL,
    "description" varchar(2048) NOT NULL DEFAULT '',
    "roleIdsThatCanBeUsedThisDecoration" varchar(128)[] NOT NULL DEFAULT '{}',
    "remoteId" varchar(32),
    "host" varchar(128),
    "rawUrl" text
);

CREATE TABLE IF NOT EXISTS "user" (
    "id" varchar(32) PRIMARY KEY,
    "avatarDecorations" jsonb NOT NULL DEFAULT '[]'
);
`

// cherrypickDecorationDDLNoHost is the clean mk-go shape: avatar_decoration
// exists but has no host column yet, so the migration must be a no-op.
const cherrypickDecorationDDLNoHost = `
CREATE TABLE IF NOT EXISTS avatar_decoration (
    "id" varchar(32) PRIMARY KEY,
    "updatedAt" timestamp with time zone,
    "url" varchar(1024) NOT NULL,
    "name" varchar(256) NOT NULL,
    "description" varchar(2048) NOT NULL DEFAULT '',
    "roleIdsThatCanBeUsedThisDecoration" varchar(128)[] NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS "user" (
    "id" varchar(32) PRIMARY KEY,
    "avatarDecorations" jsonb NOT NULL DEFAULT '[]'
);
`

// TestCherryPickAvatarDecorationMigration drives the focused up migration
// against the package-isolated DB: mixed local/remote fixtures clean up,
// re-running is a no-op, a clean mk-go schema is untouched, and every invalid
// or orphan JSON shape aborts the transaction without changing any row.
func TestCherryPickAvatarDecorationMigration(t *testing.T) {
	db := openDecorationTestDB(t)

	t.Run("cleans remote decorations and retained references", func(t *testing.T) {
		prepareDecorationSchema(t, db, cherrypickDecorationDDL)
		seedDecorations(t, db)
		seedDecorationUsers(t, db)

		require.NoError(t, db.Exec(loadCherrypickDecorationMigration(t)).Error)

		var total, remote int64
		require.NoError(t, db.Raw(`SELECT count(*) FROM avatar_decoration`).Row().Scan(&total))
		require.NoError(t, db.Raw(`SELECT count(*) FROM avatar_decoration WHERE host IS NOT NULL`).Row().Scan(&remote))
		require.Equal(t, int64(2), total, "only local rows must survive")
		require.Zero(t, remote, "no remote rows may remain")

		var localURL string
		require.NoError(t, db.Raw(`SELECT "url" FROM avatar_decoration WHERE id = 'local-1'`).Row().Scan(&localURL))
		require.Equal(t, "https://local.example/a.png", localURL)

		got := avatarDecorationsText(t, db, "migration-user")
		require.JSONEq(t,
			`[
				{"id":"local-1","angle":15,"flipH":false,"offsetX":-8,"offsetY":4},
				{"id":"local-2","angle":0,"flipH":false,"offsetX":2,"offsetY":-3}
			]`,
			got,
		)

		require.JSONEq(t, `[]`, avatarDecorationsText(t, db, "migration-user-remote-only"))
		require.JSONEq(t, `[]`, avatarDecorationsText(t, db, "migration-user-empty"))
		require.JSONEq(t,
			`[{"id":"local-1","angle":1,"flipH":true,"offsetX":0,"offsetY":0,"scale":1.5,"opacity":0.8}]`,
			avatarDecorationsText(t, db, "migration-user-no-decor"),
		)
	})

	t.Run("second execution is a no-op", func(t *testing.T) {
		prepareDecorationSchema(t, db, cherrypickDecorationDDL)
		seedDecorations(t, db)
		seedDecorationUsers(t, db)

		migration := loadCherrypickDecorationMigration(t)
		require.NoError(t, db.Exec(migration).Error)

		afterFirst := captureDecorationSnapshot(t, db)
		require.NoError(t, db.Exec(migration).Error)
		require.Equal(t, afterFirst, captureDecorationSnapshot(t, db))
	})

	t.Run("clean mk-go schema without avatar_decoration.host is a no-op", func(t *testing.T) {
		prepareDecorationSchema(t, db, cherrypickDecorationDDLNoHost)
		require.NoError(t, db.Exec(
			`INSERT INTO "user" ("id", "avatarDecorations") VALUES ('plain-user', '[{"id":"local-1","angle":5}]'::jsonb)`,
		).Error)

		before := captureDecorationSnapshot(t, db)
		require.NoError(t, db.Exec(loadCherrypickDecorationMigration(t)).Error)
		require.Equal(t, before, captureDecorationSnapshot(t, db))
	})

	t.Run("missing tables are a no-op", func(t *testing.T) {
		prepareDecorationSchema(t, db, "")
		require.NoError(t, db.Exec(loadCherrypickDecorationMigration(t)).Error)
	})

	t.Run("non-array avatarDecorations abort and preserve every row", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			value string
		}{
			// 値は JSON 文書として有効な字句で渡す (JSON 文字列は引用符付き)。
			{"string scalar", `'"just-a-string"'`},
			{"number scalar", `'42'`},
			{"json null", `'null'`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				prepareDecorationSchema(t, db, cherrypickDecorationDDL)
				seedDecorations(t, db)
				require.NoError(t, db.Exec(
					`INSERT INTO "user" ("id", "avatarDecorations") VALUES ('bad-user', `+tc.value+`::jsonb)`,
				).Error)
				assertDecorationMigrationAborts(t, db, "invalid user.avatarDecorations JSON")
			})
		}
	})

	t.Run("sql null avatarDecorations abort and preserve every row", func(t *testing.T) {
		prepareDecorationSchema(t, db, cherrypickDecorationDDL)
		seedDecorations(t, db)
		require.NoError(t, db.Exec(`ALTER TABLE "user" ALTER COLUMN "avatarDecorations" DROP NOT NULL`).Error)
		require.NoError(t, db.Exec(`INSERT INTO "user" ("id", "avatarDecorations") VALUES ('bad-user', NULL)`).Error)
		assertDecorationMigrationAborts(t, db, "invalid user.avatarDecorations JSON")
	})

	t.Run("invalid array items abort and preserve every row", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			value string
		}{
			{"element not an object", `'[{"id":"local-1"},42]'`},
			{"element is json null", `'[{"id":"local-1"},null]'`},
			{"element is an array", `'[{"id":"local-1"},["nested"]]'`},
			{"id is not a string", `'[{"id":42}]'`},
			{"id key missing", `'[{"angle":1}]'`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				prepareDecorationSchema(t, db, cherrypickDecorationDDL)
				seedDecorations(t, db)
				require.NoError(t, db.Exec(
					`INSERT INTO "user" ("id", "avatarDecorations") VALUES ('bad-user', `+tc.value+`::jsonb)`,
				).Error)
				assertDecorationMigrationAborts(t, db, "invalid user.avatarDecorations item")
			})
		}
	})

	t.Run("empty-string id aborts and preserves every row", func(t *testing.T) {
		prepareDecorationSchema(t, db, cherrypickDecorationDDL)
		seedDecorations(t, db)
		require.NoError(t, db.Exec(
			`INSERT INTO "user" ("id", "avatarDecorations") VALUES ('bad-user', '[{"id":""}]'::jsonb)`,
		).Error)
		assertDecorationMigrationAborts(t, db, "orphan avatar decoration references")
	})

	t.Run("any orphan decoration aborts and preserves every row", func(t *testing.T) {
		prepareDecorationSchema(t, db, cherrypickDecorationDDL)
		seedDecorations(t, db)
		require.NoError(t, db.Exec(`
			INSERT INTO "user" ("id", "avatarDecorations")
			VALUES ('synthetic-orphan-user', '[{"id":"synthetic-missing-decoration"}]'::jsonb)
		`).Error)
		assertDecorationMigrationAborts(t, db, "orphan avatar decoration references")
	})

	t.Run("unapproved orphan id aborts and preserves every row", func(t *testing.T) {
		prepareDecorationSchema(t, db, cherrypickDecorationDDL)
		seedDecorations(t, db)
		require.NoError(t, db.Exec(
			`INSERT INTO "user" ("id", "avatarDecorations") VALUES ('bad-user', '[{"id":"ghost-id","angle":9}]'::jsonb)`,
		).Error)
		assertDecorationMigrationAborts(t, db, "orphan avatar decoration references")
	})
}

// TestCherryPickMigrationContainsNoProductionStyleIDLiteral locks the source
// privacy contract: the migration must not carry quoted lowercase-alphanumeric
// 16-char literals, which are the CherryPick production ID shape. The cleanup
// accepts only a manually repaired DB, so no production-derived ID may appear
// in the migration file itself.
func TestCherryPickMigrationContainsNoProductionStyleIDLiteral(t *testing.T) {
	src := loadCherrypickDecorationMigration(t)
	re := regexp.MustCompile(`'[a-z0-9]{16}'`)
	require.Empty(t, re.FindAllString(src, -1))
}

func openDecorationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := testutil.OpenTestDB()
	if err != nil {
		t.Skipf("isolated PostgreSQL unavailable: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// prepareDecorationSchema drops any prior state in the dedicated schema, then
// applies the given DDL. サブテスト間の干渉を防ぐため毎回再作成する。
func prepareDecorationSchema(t *testing.T, db *gorm.DB, ddl string) {
	t.Helper()
	require.NoError(t, db.Exec(`DROP TABLE IF EXISTS avatar_decoration, "user" CASCADE`).Error)
	if ddl != "" {
		require.NoError(t, db.Exec(ddl).Error)
	}
}

func loadCherrypickDecorationMigration(t *testing.T) string {
	t.Helper()
	sql, err := os.ReadFile(cherrypickAvatarDecorationUpMigration)
	require.NoError(t, err, "read migration file %s", cherrypickAvatarDecorationUpMigration)
	return string(sql)
}

func seedDecorations(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`
INSERT INTO avatar_decoration ("id", "url", "name", "description", "host") VALUES
    ('local-1',  'https://local.example/a.png',  'Local One',  '', NULL),
    ('local-2',  'https://local.example/b.png',  'Local Two',  '', NULL),
    ('remote-1', 'https://remote.example/a.png', 'Remote One', '', 'example.com'),
    ('remote-2', 'https://remote.example/b.png', 'Remote Two', '', 'example.org');
`).Error)
}

func seedDecorationUsers(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`
INSERT INTO "user" ("id", "avatarDecorations") VALUES
    ('migration-user', '[
        {"id":"local-1","angle":15,"flipH":false,"offsetX":-8,"offsetY":4},
        {"id":"remote-1","angle":30,"flipH":true,"offsetX":0,"offsetY":10},
        {"id":"local-2","angle":0,"flipH":false,"offsetX":2,"offsetY":-3},
        {"id":"remote-2"}
    ]'::jsonb),
    ('migration-user-remote-only', '[{"id":"remote-1"},{"id":"remote-2","angle":5}]'::jsonb),
    ('migration-user-empty', '[]'::jsonb),
    ('migration-user-no-decor', '[{"id":"local-1","angle":1,"flipH":true,"offsetX":0,"offsetY":0,"scale":1.5,"opacity":0.8}]'::jsonb);
`).Error)
}

func avatarDecorationsText(t *testing.T, db *gorm.DB, userID string) string {
	t.Helper()
	var raw string
	require.NoError(t, db.Raw(
		`SELECT "avatarDecorations"::text FROM "user" WHERE id = ?`, userID,
	).Row().Scan(&raw))
	return raw
}

// decorationSnapshot captures the full fixture state so abort tests can prove
// that not a single row changed.
type decorationSnapshot struct {
	totalRows  int64
	remoteRows int64
	userDecos  map[string]string
}

func captureDecorationSnapshot(t *testing.T, db *gorm.DB) decorationSnapshot {
	t.Helper()
	s := decorationSnapshot{userDecos: map[string]string{}}
	require.NoError(t, db.Raw(`SELECT count(*) FROM avatar_decoration`).Row().Scan(&s.totalRows))
	// host 列を持たない clean mk-go schema でも使えるよう、列の有無で分岐する。
	if hasColumn(t, db, "avatar_decoration", "host") {
		require.NoError(t, db.Raw(`SELECT count(*) FROM avatar_decoration WHERE host IS NOT NULL`).Row().Scan(&s.remoteRows))
	}
	rows, err := db.Raw(`SELECT id, COALESCE("avatarDecorations"::text, '') FROM "user" ORDER BY id`).Rows()
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, decos string
		require.NoError(t, rows.Scan(&id, &decos))
		s.userDecos[id] = decos
	}
	require.NoError(t, rows.Err())
	return s
}

func hasColumn(t *testing.T, db *gorm.DB, table, column string) bool {
	t.Helper()
	var n int64
	require.NoError(t, db.Raw(
		`SELECT count(*) FROM information_schema.columns
		 WHERE table_schema = current_schema() AND table_name = ? AND column_name = ?`,
		table, column,
	).Row().Scan(&n))
	return n > 0
}

// assertDecorationMigrationAborts runs the migration, requires it to fail with
// the expected message fragment, and requires the state to be byte-for-byte
// unchanged. 例外は単一の DO 文を丸ごとロールバックするので、何も変わらない
// はずである。
func assertDecorationMigrationAborts(t *testing.T, db *gorm.DB, wantErr string) {
	t.Helper()
	before := captureDecorationSnapshot(t, db)
	err := db.Exec(loadCherrypickDecorationMigration(t)).Error
	require.Error(t, err, "migration must abort on invalid data")
	if wantErr != "" {
		require.Contains(t, err.Error(), wantErr)
	}
	require.Equal(t, before, captureDecorationSnapshot(t, db))
}
