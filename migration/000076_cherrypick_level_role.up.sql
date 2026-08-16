-- CherryPick level role互換: manualLevel target, levelPolicies,
-- canHideProfileByUser, role_assignment.experience / isHideProfile。
-- 既存 000012_role は編集せず、追加は本 migration だけで行う。
-- 全操作は冪等にし、適用後に列 shape (型・nullable・default) を再検証して
-- 不一致なら固定 category で abort する (自動変更しない)。

-- 1. role_target_enum へ 'manualLevel' を追加。プロジェクト規約の
--    duplicate_object 例外 pattern で冪等化する。pg_type の schema 非依存
--    検査は使わない。ALTER TYPE は search_path 解決で current_schema の
--    type を対象にする。
DO $$ BEGIN
    ALTER TYPE role_target_enum ADD VALUE 'manualLevel';
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- 2. role.levelPolicies (jsonb NOT NULL DEFAULT '{}')
ALTER TABLE "role" ADD COLUMN IF NOT EXISTS "levelPolicies" jsonb NOT NULL DEFAULT '{}'::jsonb;

-- 3. role.policies の default を '{}'::jsonb に揃える (CherryPick 1740640480147)。
ALTER TABLE "role" ALTER COLUMN "policies" SET DEFAULT '{}'::jsonb;

-- 4. role_assignment.experience (bigint NULL。移行時 schema を source of truth に採用)
ALTER TABLE "role_assignment" ADD COLUMN IF NOT EXISTS "experience" bigint;

-- 5. experience index。CherryPick import 済み DB は既存 index を持つため、
--    (experience) に対する index が 1 つも無い場合にだけ作成する。
--    pg_indexes.indexdef は lowercase 識別子を引用符なしで描画するため、
--    `(experience)` で列参照を照合する (`"experience"` では常に不一致になる)。
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_indexes
        WHERE schemaname = current_schema()
          AND tablename = 'role_assignment'
          AND indexdef ILIKE '%(experience)%'
    ) THEN
        CREATE INDEX "IDX_role_assignment_experience" ON "role_assignment" ("experience");
    END IF;
END $$;

-- 6. role.canHideProfileByUser (boolean NOT NULL DEFAULT false)
ALTER TABLE "role" ADD COLUMN IF NOT EXISTS "canHideProfileByUser" boolean NOT NULL DEFAULT false;

-- 7. role_assignment.isHideProfile (boolean NULL)
ALTER TABLE "role_assignment" ADD COLUMN IF NOT EXISTS "isHideProfile" boolean;

-- 8. 追加後検証: 名前が同じで shape が違う column は自動変更せず固定 category で
--    abort する。fresh DB でも import 済み DB でも適用後に必ず通る。
DO $$
DECLARE
    v_type text; v_null text; v_def text;
BEGIN
    SELECT data_type, is_nullable, COALESCE(column_default, '') INTO v_type, v_null, v_def
    FROM information_schema.columns
    WHERE table_schema = current_schema() AND table_name = 'role' AND column_name = 'levelPolicies';
    IF v_type IS DISTINCT FROM 'jsonb' OR v_null IS DISTINCT FROM 'NO'
       OR v_def IS DISTINCT FROM '''{}''::jsonb' THEN
        RAISE EXCEPTION 'cherrypick level role migration: incompatible-level-policies-column';
    END IF;

    SELECT data_type, is_nullable, COALESCE(column_default, '') INTO v_type, v_null, v_def
    FROM information_schema.columns
    WHERE table_schema = current_schema() AND table_name = 'role' AND column_name = 'canHideProfileByUser';
    IF v_type IS DISTINCT FROM 'boolean' OR v_null IS DISTINCT FROM 'NO'
       OR v_def IS DISTINCT FROM 'false' THEN
        RAISE EXCEPTION 'cherrypick level role migration: incompatible-can-hide-column';
    END IF;

    SELECT data_type, is_nullable, COALESCE(column_default, '') INTO v_type, v_null, v_def
    FROM information_schema.columns
    WHERE table_schema = current_schema() AND table_name = 'role_assignment' AND column_name = 'experience';
    IF v_type IS DISTINCT FROM 'bigint' OR v_null IS DISTINCT FROM 'YES' OR v_def <> '' THEN
        RAISE EXCEPTION 'cherrypick level role migration: incompatible-experience-column';
    END IF;

    SELECT data_type, is_nullable, COALESCE(column_default, '') INTO v_type, v_null, v_def
    FROM information_schema.columns
    WHERE table_schema = current_schema() AND table_name = 'role_assignment' AND column_name = 'isHideProfile';
    IF v_type IS DISTINCT FROM 'boolean' OR v_null IS DISTINCT FROM 'YES' OR v_def <> '' THEN
        RAISE EXCEPTION 'cherrypick level role migration: incompatible-hide-column';
    END IF;
END $$;
