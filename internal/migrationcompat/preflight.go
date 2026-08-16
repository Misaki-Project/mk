package migrationcompat

import (
	"context"
	"fmt"
)

// columnSpec pins one consumed column's exact type/length/nullability.
type columnSpec struct {
	table    string
	column   string
	dataType string
	maxLen   int // 0 = length is not constrained (jsonb)
	nullable bool
}

// preflightColumnSpecs is the exact schema shape CheckPreflight requires before
// it reads any row. information_schema の data_type は varchar(n) が
// 'character varying'、jsonb が 'jsonb' で返る。
var preflightColumnSpecs = []columnSpec{
	{table: "user", column: "avatarDecorations", dataType: "jsonb", maxLen: 0, nullable: false},
	{table: "user", column: "avatarId", dataType: "character varying", maxLen: 32, nullable: true},
	{table: "user", column: "bannerId", dataType: "character varying", maxLen: 32, nullable: true},
	{table: "drive_file", column: "id", dataType: "character varying", maxLen: 32, nullable: false},
	{table: "avatar_decoration", column: "id", dataType: "character varying", maxLen: 32, nullable: false},
}

// preflightCountsSQL counts every incompatible row and orphan reference in one
// pass: 無効な avatarDecorations 行、無効 item、孤児装飾、孤児avatar、孤児banner。
// 個別の値 (ID・URL・JSON) は一切返さず、件数のみを返す。
const preflightCountsSQL = `
SELECT
  (SELECT count(*) FROM "user" u
   WHERE jsonb_typeof(u."avatarDecorations") IS DISTINCT FROM 'array'),
  (SELECT count(*) FROM (
      SELECT u."avatarDecorations" FROM "user" u
      WHERE jsonb_typeof(u."avatarDecorations") = 'array'
   ) arr CROSS JOIN LATERAL jsonb_array_elements(arr."avatarDecorations") item
   WHERE jsonb_typeof(item) IS DISTINCT FROM 'object'
      OR jsonb_typeof(item->'id') IS DISTINCT FROM 'string'),
  (SELECT count(*) FROM (
      SELECT u."avatarDecorations" FROM "user" u
      WHERE jsonb_typeof(u."avatarDecorations") = 'array'
   ) arr CROSS JOIN LATERAL jsonb_array_elements(arr."avatarDecorations") item
   LEFT JOIN avatar_decoration d ON d.id = item->>'id'
   WHERE d.id IS NULL),
  (SELECT count(*) FROM "user" u LEFT JOIN drive_file f ON f.id = u."avatarId"
   WHERE u."avatarId" IS NOT NULL AND f.id IS NULL),
  (SELECT count(*) FROM "user" u LEFT JOIN drive_file f ON f.id = u."bannerId"
   WHERE u."bannerId" IS NOT NULL AND f.id IS NULL)`

// CheckPreflight is a detection-only preflight that runs before an upward
// golang-migrate run adds the decoration/avatar FK constraints.
//
// 修復は一切行わず、件数だけを返す。3テーブルのうち1つでも欠けた schema
// (クリーン DB、装飾 migration 前の既存 install) は何も検証せず commit する。
// 全て揃ってから列定義を照合し、不一致か孤児が1件でもあれば fail-closed で
// abort する。エラーは件数と固定のテーブル/カラム名・カテゴリ名のみを含み、
// ユーザー ID・ファイル ID・装飾 ID・URL・JSON・接続 URL は決して含めない。
func (l *Lock) CheckPreflight(ctx context.Context) error {
	tx, err := l.conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migration compatibility preflight: %w", err)
	}
	// 途中で abort した場合はこの defer のロールバックで何も書き込まれない。
	// 成功時の Commit 後は no-op になる。
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. 必要テーブルの存在確認。3つ揃う前の schema (クリーン DB や
	// `-steps 1` で装飾 migration がまだ走っていない段階) はそのまま commit
	// し、追加分は migration 自体が作る。揃ってから初めて列・孤児を検証する。
	var hasUser, hasDriveFile, hasDecoration bool
	if err := tx.QueryRow(ctx, `
		SELECT (to_regclass('"user"') IS NOT NULL),
		       (to_regclass('drive_file') IS NOT NULL),
		       (to_regclass('avatar_decoration') IS NOT NULL)
	`).Scan(&hasUser, &hasDriveFile, &hasDecoration); err != nil {
		return fmt.Errorf("check migration compatibility tables: %w", err)
	}
	if !hasUser || !hasDriveFile || !hasDecoration {
		return tx.Commit(ctx)
	}

	// 2. 消費する全カラムの型・長さ・nullability を検証する。不一致は
	// どの行も読まずに abort する。
	actual := make(map[string]columnSpec)
	rows, err := tx.Query(ctx, `
		SELECT table_name, column_name, data_type,
		       COALESCE(character_maximum_length, 0), is_nullable
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name IN ('user', 'drive_file', 'avatar_decoration')
	`)
	if err != nil {
		return fmt.Errorf("read migration compatibility columns: %w", err)
	}
	for rows.Next() {
		var table, column, dataType, nullable string
		var maxLen int
		if err := rows.Scan(&table, &column, &dataType, &maxLen, &nullable); err != nil {
			rows.Close()
			return fmt.Errorf("scan migration compatibility column: %w", err)
		}
		actual[table+"."+column] = columnSpec{
			table: table, column: column, dataType: dataType, maxLen: maxLen, nullable: nullable == "YES",
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate migration compatibility columns: %w", err)
	}
	rows.Close()
	for _, spec := range preflightColumnSpecs {
		info, ok := actual[spec.table+"."+spec.column]
		if !ok {
			return fmt.Errorf("migration compatibility: incompatible schema: missing column %s.%s", spec.table, spec.column)
		}
		if info.dataType != spec.dataType || info.nullable != spec.nullable ||
			(spec.maxLen != 0 && info.maxLen != spec.maxLen) {
			return fmt.Errorf("migration compatibility: incompatible column %s.%s", spec.table, spec.column)
		}
	}

	// 3. 検証から count までの間に同時書込が割り込まないよう3テーブルを固定する。
	if _, err := tx.Exec(ctx, `LOCK TABLE "user", drive_file, avatar_decoration IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return fmt.Errorf("lock migration compatibility tables: %w", err)
	}

	// 4. 全カテゴリの件数を一度に数える。0 でなければ fail-closed で abort する。
	var invalidRows, invalidItems, orphanDecorations, orphanAvatars, orphanBanners int64
	if err := tx.QueryRow(ctx, preflightCountsSQL).Scan(
		&invalidRows, &invalidItems, &orphanDecorations, &orphanAvatars, &orphanBanners,
	); err != nil {
		return fmt.Errorf("count migration compatibility orphans: %w", err)
	}
	if invalidRows == 0 && invalidItems == 0 && orphanDecorations == 0 && orphanAvatars == 0 && orphanBanners == 0 {
		return tx.Commit(ctx)
	}
	return fmt.Errorf("migration compatibility: "+
		"invalid avatar decoration JSON: %d rows, "+
		"invalid avatar decoration item: %d rows, "+
		"orphan avatar decoration references: %d rows, "+
		"orphan avatar references: %d rows, "+
		"orphan banner references: %d rows",
		invalidRows, invalidItems, orphanDecorations, orphanAvatars, orphanBanners)
}
