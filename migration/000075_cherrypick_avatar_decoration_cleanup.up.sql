-- リモートアバターデコレーションを除去する (CherryPick → mk-go 移行用)。
--
-- CherryPick はローカルとリモートの装飾を avatar_decoration に混在して持つ。
-- host IS NULL がローカル、host IS NOT NULL がリモートを表す。mk-go は
-- リモート装飾を扱わないため、この移行はリモート行を削除し、
-- user.avatarDecorations の JSON から該当 ID の参照を取り除く。
--
-- 既存の不正 JSON は「置き換える」のではなく例外で中止する。スカラーや
-- NULL を黙って無視して残すと、後段の jsonb_array_elements が落ちて
-- migration が dirty 停止するため、事前に全件検証する。
--
-- この移行は手動修復後のDBだけを受け入れる。存在しない装飾への参照(孤児)は
-- 本番固有IDの allowlist を持たず、1件でもあれば変更前に中止する。したがって
-- migration 自体には本番固有の ID literal を含めない。
--
-- この移行はパッケージ隔離された current schema だけで動く。public. は
-- 明示しない (search_path を跨ぐとテストの隔離が壊れるため)。
DO $$
DECLARE
    invalid_count bigint;
BEGIN
    IF to_regclass('avatar_decoration') IS NULL
       OR to_regclass('"user"') IS NULL
       OR NOT EXISTS (
           SELECT 1 FROM information_schema.columns
           WHERE table_schema = current_schema()
             AND table_name = 'avatar_decoration'
             AND column_name = 'host'
       ) THEN
        RETURN;
    END IF;

    LOCK TABLE avatar_decoration, "user" IN SHARE ROW EXCLUSIVE MODE;

    -- array型の検証は要素検証と分離する。スカラーやNULLが
    -- jsonb_array_elements に届かないよう先に全体を数える
    SELECT count(*) INTO invalid_count
    FROM "user" u
    WHERE jsonb_typeof(u."avatarDecorations") IS DISTINCT FROM 'array';
    IF invalid_count <> 0 THEN
        RAISE EXCEPTION 'invalid user.avatarDecorations JSON: % rows', invalid_count;
    END IF;

    -- array型の行のみ要素を検証する
    SELECT count(*) INTO invalid_count
    FROM "user" u
    CROSS JOIN LATERAL jsonb_array_elements(u."avatarDecorations") item
    WHERE jsonb_typeof(item) IS DISTINCT FROM 'object'
       OR jsonb_typeof(item->'id') IS DISTINCT FROM 'string';
    IF invalid_count <> 0 THEN
        RAISE EXCEPTION 'invalid user.avatarDecorations item: % entries', invalid_count;
    END IF;

    -- 孤児参照は 1件でも許さない。手動修復後のDBでは 0 件のはずで、
    -- ここで中止すれば UPDATE の前に全行を温存できる。
    SELECT count(*) INTO invalid_count
    FROM "user" u
    CROSS JOIN LATERAL jsonb_array_elements(u."avatarDecorations") item
    LEFT JOIN avatar_decoration d ON d.id = item->>'id'
    WHERE d.id IS NULL;
    IF invalid_count <> 0 THEN
        RAISE EXCEPTION 'orphan avatar decoration references: % entries', invalid_count;
    END IF;

    CREATE TEMP TABLE IF NOT EXISTS cherrypick_remote_decoration_id (
        id varchar(32) PRIMARY KEY
    ) ON COMMIT DROP;
    TRUNCATE cherrypick_remote_decoration_id;
    INSERT INTO cherrypick_remote_decoration_id (id)
    SELECT id FROM avatar_decoration WHERE host IS NOT NULL;

    -- 除去するのはリモート装飾への参照だけ。孤児は既に上で除外済み。
    UPDATE "user" u
    SET "avatarDecorations" = (
        SELECT COALESCE(jsonb_agg(item.value ORDER BY item.ordinality), '[]'::jsonb) AS value
        FROM jsonb_array_elements(u."avatarDecorations") WITH ORDINALITY AS item(value, ordinality)
        WHERE NOT EXISTS (
            SELECT 1 FROM cherrypick_remote_decoration_id remote WHERE remote.id = item.value->>'id'
        )
    )
    WHERE EXISTS (
        SELECT 1 FROM jsonb_array_elements(u."avatarDecorations") item
        WHERE EXISTS (
            SELECT 1 FROM cherrypick_remote_decoration_id remote WHERE remote.id = item->>'id'
        )
    );

    DELETE FROM avatar_decoration d
    USING cherrypick_remote_decoration_id remote
    WHERE d.id = remote.id;

    SELECT count(*) INTO invalid_count FROM avatar_decoration WHERE host IS NOT NULL;
    IF invalid_count <> 0 THEN
        RAISE EXCEPTION 'remote avatar decorations remain: % rows', invalid_count;
    END IF;

    SELECT count(*) INTO invalid_count
    FROM "user" u
    CROSS JOIN LATERAL jsonb_array_elements(u."avatarDecorations") item
    JOIN cherrypick_remote_decoration_id remote ON remote.id = item->>'id';
    IF invalid_count <> 0 THEN
        RAISE EXCEPTION 'remote avatar decoration references remain: % entries', invalid_count;
    END IF;

    SELECT count(*) INTO invalid_count
    FROM "user" u
    CROSS JOIN LATERAL jsonb_array_elements(u."avatarDecorations") item
    LEFT JOIN avatar_decoration d ON d.id = item->>'id'
    WHERE d.id IS NULL;
    IF invalid_count <> 0 THEN
        RAISE EXCEPTION 'orphan avatar decoration references remain: % entries', invalid_count;
    END IF;
END
$$;
