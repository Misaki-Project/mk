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

// initialUpMigration is the mk-go baseline migration under test. It is loaded
// from disk so the test guards both the source text and the real DB result.
var initialUpMigration = filepath.Join("..", "..", "migration", "000001_initial.up.sql")

// TestInitialMigrationPreservesDanglingNoteRelations guards upstream Misskey's
// `createForeignKeyConstraints: false` contract for note.replyId and
// note.renoteId. Upstream keeps both nullable columns but never adds an FK, so
// reply / renote IDs may legitimately dangle (point at deleted notes). The
// mk-go initial migration must produce the same shape.
func TestInitialMigrationPreservesDanglingNoteRelations(t *testing.T) {
	migration := loadInitialUpMigration(t)

	require.NotContains(t, migration, `FK_note_replyId`)
	require.NotContains(t, migration, `FK_note_renoteId`)
	require.NotRegexp(t, regexp.MustCompile(`(?is)FOREIGN\s+KEY\s*\(\s*"replyId"\s*\)`), migration)
	require.NotRegexp(t, regexp.MustCompile(`(?is)FOREIGN\s+KEY\s*\(\s*"renoteId"\s*\)`), migration)

	db := openNoteRelationTestDB(t)
	resetNoteRelationSchema(t, db)
	seedNoteRelationFixture(t, db)

	require.NoError(t, db.Exec(migration).Error,
		"initial migration must apply cleanly over the CherryPick-shaped note")

	// Dangling relation IDs must survive the migration untouched.
	{
		var replyID, renoteID *string
		require.NoError(t, db.Raw(
			`SELECT "replyId", "renoteId" FROM note WHERE id = 'surviving-reply'`,
		).Row().Scan(&replyID, &renoteID))
		require.Equal(t, "deleted-reply-target", *replyID)
		require.Nil(t, renoteID)
	}
	{
		var replyID, renoteID *string
		require.NoError(t, db.Raw(
			`SELECT "replyId", "renoteId" FROM note WHERE id = 'surviving-renote'`,
		).Row().Scan(&replyID, &renoteID))
		require.Nil(t, replyID)
		require.Equal(t, "deleted-renote-target", *renoteID)
	}

	// No foreign key in the whole schema may constrain replyId or renoteId.
	var noteFKs int64
	require.NoError(t, db.Raw(`
		SELECT count(*)
		FROM pg_constraint c
		JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = ANY(c.conkey)
		WHERE c.contype = 'f'
		  AND c.connamespace = current_schema()::regnamespace
		  AND a.attname IN ('replyId', 'renoteId')
	`).Row().Scan(&noteFKs))
	require.Zero(t, noteFKs, "pg_constraint must not hold any FK on note replyId/renoteId")
}

func loadInitialUpMigration(t *testing.T) string {
	t.Helper()
	sql, err := os.ReadFile(initialUpMigration)
	require.NoError(t, err, "read migration file %s", initialUpMigration)
	return string(sql)
}

func openNoteRelationTestDB(t *testing.T) *gorm.DB {
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

// resetNoteRelationSchema wipes the package-dedicated schema so the full
// initial migration runs on a fresh state on every invocation. 000001 has a
// few unconditional ALTER TABLE ADD CONSTRAINT statements, so it cannot be
// re-applied over itself.
func resetNoteRelationSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	var schema string
	require.NoError(t, db.Raw(`SELECT current_schema()`).Row().Scan(&schema))
	require.NoError(t, db.Exec(`DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`).Error)
	require.NoError(t, db.Exec(`CREATE SCHEMA "`+schema+`"`).Error)
}

// seedNoteRelationFixture mirrors a CherryPick-shaped existing instance: note
// already exists before the migration with dangling reply/renote targets.
func seedNoteRelationFixture(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`
CREATE TABLE note (
    id varchar(32) PRIMARY KEY,
    "replyId" varchar(32),
    "renoteId" varchar(32),
    "userId" varchar(32) NOT NULL,
    visibility varchar(32) NOT NULL,
    "userHost" varchar(128)
);
INSERT INTO note (id, "replyId", "renoteId", "userId", visibility)
VALUES
    ('surviving-reply', 'deleted-reply-target', NULL, 'user-1', 'public'),
    ('surviving-renote', NULL, 'deleted-renote-target', 'user-1', 'public');
`).Error)
}
