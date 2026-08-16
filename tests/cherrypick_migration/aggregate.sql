-- 隔離リハーサル用の集計 SQL (key=value 行を出力する)。
--
-- 出力は件数・装飾参照件数・スキーマ事実のみに限定する。
-- ユーザー名・パスワードハッシュ・平文パスワード・トークン・メール・IP・
-- ノート本文・生プロフィール JSON を一切出力しない。決定的ハッシュや
-- 個別 ID も出力しない (count と状態だけを evidence へ渡す)。
--
-- 実行: psql -A -t -q -v phase=pre|post -f aggregate.sql
-- :'phase' は psql 変数 (pre / post) で、どの段階の集計かを表すだけ。
--
-- schema_migrations は CherryPick dump (TypeORM 運用) には存在しないため、
-- to_regclass で存在を確認してから version / dirty だけを読む。存在しない
-- dump でも集計を失敗させない (\gset + \if で条件分岐)。

-- schema_migrations の存在有無を psql 変数に取り込む (出力はされない)。
SELECT CASE WHEN to_regclass('schema_migrations') IS NOT NULL THEN 1 ELSE 0 END AS sm_present \gset

SELECT 'phase=' || :'phase'

UNION ALL

-- 件数 (個別値は出さない)
SELECT 'user_count=' || count(*) FROM "user"
UNION ALL
SELECT 'note_count=' || count(*) FROM note
UNION ALL
SELECT 'following_count=' || count(*) FROM following
UNION ALL
SELECT 'drive_file_count=' || count(*) FROM drive_file
UNION ALL
SELECT 'role_count=' || count(*) FROM role
UNION ALL
SELECT 'role_assignment_count=' || count(*) FROM role_assignment
UNION ALL
SELECT 'avatar_decoration_count=' || count(*) FROM avatar_decoration

UNION ALL

-- 装飾行の種別件数
SELECT 'local_decoration_count=' || count(*) FROM avatar_decoration WHERE host IS NULL
UNION ALL
SELECT 'remote_decoration_count=' || count(*) FROM avatar_decoration WHERE host IS NOT NULL
UNION ALL
SELECT 'empty_string_host_decoration_count=' || count(*) FROM avatar_decoration WHERE host = ''

UNION ALL

-- ユーザー装飾参照。配列型の行だけを jsonb_array_elements に渡す
-- (スカラーや NULL は関数に届けず、集計を失敗させない)。
-- 参照のカウントに個別のユーザー ID や装飾 ID は出さない。
SELECT 'total_decoration_reference_count=' || COALESCE((
    SELECT count(*) FROM (
        SELECT u."avatarDecorations" FROM "user" u
        WHERE jsonb_typeof(u."avatarDecorations") = 'array'
    ) arr CROSS JOIN LATERAL jsonb_array_elements(arr."avatarDecorations") item
), 0)
UNION ALL
SELECT 'remote_decoration_reference_count=' || COALESCE((
    SELECT count(*) FROM (
        SELECT u."avatarDecorations" FROM "user" u
        WHERE jsonb_typeof(u."avatarDecorations") = 'array'
    ) arr CROSS JOIN LATERAL jsonb_array_elements(arr."avatarDecorations") item
    JOIN avatar_decoration d ON d.id = item->>'id'
    WHERE d.host IS NOT NULL
), 0)
UNION ALL
SELECT 'orphan_decoration_reference_count=' || COALESCE((
    SELECT count(*) FROM (
        SELECT u."avatarDecorations" FROM "user" u
        WHERE jsonb_typeof(u."avatarDecorations") = 'array'
    ) arr
    CROSS JOIN LATERAL jsonb_array_elements(arr."avatarDecorations") item
    LEFT JOIN avatar_decoration d ON d.id = item->>'id'
    WHERE d.id IS NULL
), 0)

UNION ALL

-- 孤立した avatar / banner ファイル参照 (user.avatarId / user.bannerId が
-- drive_file に解決しない件数)。個別のユーザー ID やファイル ID は出さない。
SELECT 'orphan_avatar_file_reference_count=' || count(*)
FROM "user" u
LEFT JOIN drive_file f ON f.id = u."avatarId"
WHERE u."avatarId" IS NOT NULL AND f.id IS NULL
UNION ALL
SELECT 'orphan_banner_file_reference_count=' || count(*)
FROM "user" u
LEFT JOIN drive_file f ON f.id = u."bannerId"
WHERE u."bannerId" IS NOT NULL AND f.id IS NULL

UNION ALL
-- 不正な avatarDecorations の行/要素 (migration 000075 と同じ separated check)。
-- 行型チェック (jsonb_typeof IS DISTINCT FROM 'array') は jsonb_array_elements を
-- 呼ばず、要素チェックは配列型の行だけを関数に渡す。スカラーや NULL が
-- jsonb_array_elements に届いて集計が落ちることはない。個別の行・要素値は出さない。
SELECT 'invalid_decoration_json_row_count=' || COALESCE((
    SELECT count(*) FROM "user" u
    WHERE jsonb_typeof(u."avatarDecorations") IS DISTINCT FROM 'array'
), 0)
UNION ALL
SELECT 'invalid_decoration_reference_count=' || COALESCE((
    SELECT count(*) FROM (
        SELECT u."avatarDecorations" FROM "user" u
        WHERE jsonb_typeof(u."avatarDecorations") = 'array'
    ) arr CROSS JOIN LATERAL jsonb_array_elements(arr."avatarDecorations") item
    WHERE jsonb_typeof(item) IS DISTINCT FROM 'object'
       OR jsonb_typeof(item->'id') IS DISTINCT FROM 'string'
), 0)

UNION ALL

-- スキーマ事実: schema_migrations が無い dump では present=0 だけを出す。
SELECT 'schema_migrations_present=' || CASE
    WHEN to_regclass('schema_migrations') IS NOT NULL THEN '1'
    ELSE '0'
END;

-- schema_migrations が存在するときだけ version / dirty を読む。
\if :sm_present
SELECT 'schema_migrations_version=' || version::text FROM schema_migrations LIMIT 1;
SELECT 'schema_migrations_dirty=' || dirty::text FROM schema_migrations LIMIT 1;
\endif
