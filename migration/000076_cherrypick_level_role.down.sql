-- CherryPick level role互換 migration の down。
-- 本番復旧手段としては使わない (片道移行方針)。data guard を通過した場合に
-- だけ追加物を除去する。guard は optional column の存在を先に確認してから
-- data を判定するため、column が無い schema でも安全に動く。

-- 1. guard: 存在する level column の data を確認し、level role data が
--    1 件でもあれば固定 error で停止する。ID・値・件数は出さない。
--    各 column の参照は「column が存在する」ことが分かった後だけに分けて
--    実行する。1 本の SELECT に EXISTS を並べると、PL/pgSQL は存在チェックの
--    boolean が false でも**計画時**に a."experience" 等を解決しようとして、
--    optional column が無い schema で計画自体が落ちる (実際にエラー確認済み)。
DO $$
DECLARE
    has_role_lp boolean; has_role_canhide boolean;
    has_assign_exp boolean; has_assign_ishide boolean;
    has_manual_label boolean;
    has_data boolean := false;
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

    IF has_manual_label THEN
        SELECT EXISTS(SELECT 1 FROM "role" r WHERE r.target = 'manualLevel') INTO has_data;
    END IF;
    IF NOT has_data AND has_role_lp THEN
        SELECT EXISTS(SELECT 1 FROM "role" r WHERE r."levelPolicies" IS DISTINCT FROM '{}'::jsonb) INTO has_data;
    END IF;
    IF NOT has_data AND has_role_canhide THEN
        SELECT EXISTS(SELECT 1 FROM "role" r WHERE r."canHideProfileByUser" = true) INTO has_data;
    END IF;
    IF NOT has_data AND has_assign_exp THEN
        SELECT EXISTS(SELECT 1 FROM "role_assignment" a WHERE a."experience" IS NOT NULL) INTO has_data;
    END IF;
    IF NOT has_data AND has_assign_ishide THEN
        SELECT EXISTS(SELECT 1 FROM "role_assignment" a WHERE a."isHideProfile" IS NOT NULL) INTO has_data;
    END IF;

    IF has_data THEN
        RAISE EXCEPTION 'cherrypick level role migration down: level role data present; rollback blocked';
    END IF;
END $$;

-- 2. 自前 index だけを除去する (import 由来 index は名指しで drop しない)。
DROP INDEX IF EXISTS "IDX_role_assignment_experience";

-- 3. experience column は、(experience) への index が残っていない場合にだけ
--    drop する。import 済み DB の CherryPick index が残る場合は column drop の
--    カスケードで index を壊さないよう column ごと残す。indexdef の照合は
--    `(experience)` で行う (lowercase 識別子は引用符なし描画)。
DO $$
DECLARE
    has_exp_index boolean;
BEGIN
    SELECT EXISTS(
        SELECT 1 FROM pg_indexes
        WHERE schemaname = current_schema() AND tablename = 'role_assignment'
          AND indexdef ILIKE '%(experience)%'
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
