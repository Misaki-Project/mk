package migrationcompat

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// levelRoleCategories は CheckLevelRolePreflight が返す固定category。
// 本番由来ID・値・件数は一切含めない。
const (
	levelRoleCatIncompatibleColumn          = "incompatible-column"
	levelRoleCatLevelDataOnNonManual        = "level-data-on-non-manual-role"
	levelRoleCatInvalidLevelPolicies        = "invalid-level-policies"
	levelRoleCatInvalidExperiencePolicy     = "invalid-experience-policy"
	levelRoleCatInvalidPolicyAsLevel        = "invalid-policy-as-level"
	levelRoleCatInvalidAssignmentExperience = "invalid-assignment-experience"
	levelRoleCatInvalidProfileHideReference = "invalid-profile-hide-reference"
)

// levelRoleMaxExperience は外部APIで扱う experience の上限
// (Number.MAX_SAFE_INTEGER = 9007199254740991)。SQL 側でも同値を定数で使う。
const levelRoleMaxExperience int64 = 9007199254740991

// levelRoleMaxPolicyLevel は experience policy の level が取り得る上限
// (baseLevel と同様に model 側で int64 として保持する値域の上限)。
const levelRoleMaxPolicyLevel = "9223372036854775807"

// ptr はリテラルを package-level の *string 期待値として持ち上げる。
func ptr[T any](v T) *T { return &v }

// levelRoleColumnSpecs は import 済み DB で検証する level column shape。
// type・nullable に加えて default も検証する (migration 適用後の検証と同じ
// 期待値)。role.policies は level column ではないが、000076 が default を
// '{}' に揃える対象であり、import 済み DB の契約として shape を固定する。
// 存在する column だけを検証するため、policies が無い partial schema でも
// no-op のまま通る。
var levelRoleColumnSpecs = []columnSpec{
	{table: "role", column: "levelPolicies", dataType: "jsonb", maxLen: 0, nullable: false, defaultExpr: ptr("'{}'::jsonb")},
	{table: "role", column: "canHideProfileByUser", dataType: "boolean", maxLen: 0, nullable: false, defaultExpr: ptr("false")},
	{table: "role", column: "policies", dataType: "jsonb", maxLen: 0, nullable: false, defaultExpr: ptr("'{}'::jsonb")},
	{table: "role_assignment", column: "experience", dataType: "bigint", maxLen: 0, nullable: true, defaultExpr: ptr("")},
	{table: "role_assignment", column: "isHideProfile", dataType: "boolean", maxLen: 0, nullable: true, defaultExpr: ptr("")},
}

// CheckLevelRolePreflight は 000076_cherrypick_level_role の適用前に
// CherryPick 由来 level role data の不変条件を検出専用で検証する。
//
//   - role / role_assignment table が無い fresh DB → no-op
//   - level column が1つも無い mk-go 既存 schema → no-op (000076 が追加)
//   - level column が1つでも存在する partial schema → 存在する column の
//     shape を検証し、不一致なら abort (skip しない)
//   - 全 level column が揃った import 済み DB → shape に加えて data 不変条件
//     を検証する
//
// data 不変条件:
//  1. manualLevel role だけが level data を持つこと
//  2. levelPolicies が {baseLevel, experiencePolicies} として解釈できること
//     (baseLevel は 0..MaxInt64 の整数)
//  3. experience policy の type が string かつ const/linear/exponential、
//     level/base/additional/exponential が許容範囲にあること
//  4. role.policies が object であること、かつ policyAsLevel の構造
//     (type が string かつ base/const/multiplier、level / base / additional)
//     が valid であること
//  5. assignment experience が非負かつ Number.MAX_SAFE_INTEGER 以下
//  6. isHideProfile=true の assignment が canHideProfileByUser=true の
//     role を参照すること
//
// 許容範囲 (engine と共通): level >= 1 の整数、base >= 0、additional >= 0、
// exponential > 0、いずれも有限 (float64 表現可能、< 1.7976931348623157e308)。
// JSON は NaN/Inf を持たないため numeric で評価し、float64 表現不能な巨大値は
// 範囲外として検出する。不正 JSON 上の cast は CASE ガードで評価しない
// (unsafe cast を実行しない)。
//
// 不一致時は固定categoryだけを返し、ID・値・件数・table名・column名・role ID・
// user ID を出力しない。1つでも category に該当すれば migration を開始しない
// (fail-closed)。
func (l *Lock) CheckLevelRolePreflight(ctx context.Context) error {
	tx, err := l.conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin level role migration preflight: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. table 存在確認。揃う前の schema はそのまま commit する。
	var hasRole, hasAssignment bool
	if err := tx.QueryRow(ctx, `
		SELECT (to_regclass('"role"') IS NOT NULL),
		       (to_regclass('role_assignment') IS NOT NULL)
	`).Scan(&hasRole, &hasAssignment); err != nil {
		return fmt.Errorf("check level role preflight tables: %w", err)
	}
	if !hasRole || !hasAssignment {
		return tx.Commit(ctx)
	}

	// 2. level column 存在確認。bool_or は行が1つも無いと NULL を返すため
	//    (MissingColumns の no-op 経路)、COALESCE で false に倒す。
	var hasLevelPolicies, hasExp, hasHide, hasCanHide bool
	if err := tx.QueryRow(ctx, `
		SELECT
			COALESCE(bool_or((table_name = 'role' AND column_name = 'levelPolicies')), false),
			COALESCE(bool_or((table_name = 'role_assignment' AND column_name = 'experience')), false),
			COALESCE(bool_or((table_name = 'role_assignment' AND column_name = 'isHideProfile')), false),
			COALESCE(bool_or((table_name = 'role' AND column_name = 'canHideProfileByUser')), false)
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name IN ('role', 'role_assignment')
		  AND column_name IN ('levelPolicies', 'experience', 'isHideProfile', 'canHideProfileByUser')
	`).Scan(&hasLevelPolicies, &hasExp, &hasHide, &hasCanHide); err != nil {
		return fmt.Errorf("check level role preflight columns: %w", err)
	}
	if !hasLevelPolicies && !hasExp && !hasHide && !hasCanHide {
		// 000076 が全 column を追加する fresh / 旧 mk-go schema。
		return tx.Commit(ctx)
	}

	// 3. 存在する level column の shape 検証。partial schema でも既存 column の
	//    不一致は abort する (skip しない)。shape は型・nullable に加えて
	//    default も検証する (適用後検証と同じ期待値で早く失敗させる)。
	if err := checkLevelRoleColumns(ctx, tx); err != nil {
		return err
	}

	// 4. 全 column が揃った import 済み DB だけ data 不変条件を検証する。
	if !hasLevelPolicies || !hasExp || !hasHide || !hasCanHide {
		return tx.Commit(ctx)
	}
	checks := []struct {
		category string
		query    string
	}{
		{levelRoleCatLevelDataOnNonManual, `
			SELECT EXISTS(
				SELECT 1 FROM "role" r
				WHERE r.target IS DISTINCT FROM 'manualLevel'
				  AND r."levelPolicies" IS DISTINCT FROM '{}'::jsonb
			)`},
		{levelRoleCatInvalidLevelPolicies, `
			SELECT EXISTS(
				SELECT 1 FROM "role" r
				WHERE r.target = 'manualLevel'
				  AND (
					jsonb_typeof(r."levelPolicies") IS DISTINCT FROM 'object'
					OR NOT (r."levelPolicies" ? 'baseLevel')
					OR NOT (r."levelPolicies" ? 'experiencePolicies')
					OR jsonb_typeof(r."levelPolicies"->'baseLevel') IS DISTINCT FROM 'number'
					OR CASE WHEN jsonb_typeof(r."levelPolicies"->'baseLevel') = 'number'
							THEN (r."levelPolicies"->>'baseLevel')::numeric ELSE NULL END < 0
					OR CASE WHEN jsonb_typeof(r."levelPolicies"->'baseLevel') = 'number'
							THEN (r."levelPolicies"->>'baseLevel')::numeric ELSE NULL END
						 > ` + levelRoleMaxPolicyLevel + `
					OR CASE WHEN jsonb_typeof(r."levelPolicies"->'baseLevel') = 'number'
							THEN (r."levelPolicies"->>'baseLevel')::numeric ELSE NULL END
						 != floor(CASE WHEN jsonb_typeof(r."levelPolicies"->'baseLevel') = 'number'
										THEN (r."levelPolicies"->>'baseLevel')::numeric ELSE NULL END)
					OR jsonb_typeof(r."levelPolicies"->'experiencePolicies') IS DISTINCT FROM 'array'
				  )
			)`},
		{levelRoleCatInvalidExperiencePolicy, `
			SELECT EXISTS(
				SELECT 1 FROM "role" r
				CROSS JOIN LATERAL jsonb_array_elements(
					CASE WHEN jsonb_typeof(r."levelPolicies"->'experiencePolicies') = 'array'
						 THEN r."levelPolicies"->'experiencePolicies' ELSE '[]'::jsonb END
				) p
				WHERE r.target = 'manualLevel'
				  AND (
					jsonb_typeof(p->'type') IS DISTINCT FROM 'string'
					OR (p->>'type') NOT IN ('const','linear','exponential')
					OR jsonb_typeof(p->'level') IS DISTINCT FROM 'number'
					OR CASE WHEN jsonb_typeof(p->'level') = 'number' THEN (p->>'level')::numeric ELSE NULL END < 1
					OR CASE WHEN jsonb_typeof(p->'level') = 'number' THEN (p->>'level')::numeric ELSE NULL END
						 != floor(CASE WHEN jsonb_typeof(p->'level') = 'number' THEN (p->>'level')::numeric ELSE NULL END)
					OR CASE WHEN jsonb_typeof(p->'level') = 'number' THEN (p->>'level')::numeric ELSE NULL END
						 > ` + levelRoleMaxPolicyLevel + `
					OR jsonb_typeof(p->'base') IS DISTINCT FROM 'number'
					OR CASE WHEN jsonb_typeof(p->'base') = 'number' THEN (p->>'base')::numeric ELSE NULL END < 0
					OR CASE WHEN jsonb_typeof(p->'base') = 'number' THEN (p->>'base')::numeric ELSE NULL END
						 >= 1.7976931348623157e308
					OR ((p->>'type') IN ('linear','exponential') AND (
						jsonb_typeof(p->'additional') IS DISTINCT FROM 'number'
						OR CASE WHEN jsonb_typeof(p->'additional') = 'number' THEN (p->>'additional')::numeric ELSE NULL END < 0
						OR CASE WHEN jsonb_typeof(p->'additional') = 'number' THEN (p->>'additional')::numeric ELSE NULL END
							 >= 1.7976931348623157e308))
					OR ((p->>'type') = 'exponential' AND (
						jsonb_typeof(p->'exponential') IS DISTINCT FROM 'number'
						OR CASE WHEN jsonb_typeof(p->'exponential') = 'number' THEN (p->>'exponential')::numeric ELSE NULL END <= 0
						OR CASE WHEN jsonb_typeof(p->'exponential') = 'number' THEN (p->>'exponential')::numeric ELSE NULL END
							 >= 1.7976931348623157e308))
				  )
			)`},
		{levelRoleCatInvalidPolicyAsLevel, `
			SELECT EXISTS(
				SELECT 1 FROM "role" r
				WHERE r.target = 'manualLevel'
				  AND r."policies" IS NOT NULL
				  AND (
					jsonb_typeof(r."policies") IS DISTINCT FROM 'object'
					OR EXISTS (
						SELECT 1
						FROM jsonb_each(
							CASE WHEN jsonb_typeof(r."policies") = 'object'
								 THEN r."policies" ELSE '{}'::jsonb END
						) pol
						CROSS JOIN LATERAL jsonb_array_elements(
							CASE WHEN jsonb_typeof(pol.value->'policyAsLevel') = 'array'
								 THEN pol.value->'policyAsLevel' ELSE '[]'::jsonb END
						) pa
						WHERE pol.value ? 'policyAsLevel'
						  AND (
							jsonb_typeof(pa->'type') IS DISTINCT FROM 'string'
							OR (pa->>'type') NOT IN ('base','const','multiplier')
							OR jsonb_typeof(pa->'level') IS DISTINCT FROM 'number'
							OR CASE WHEN jsonb_typeof(pa->'level') = 'number' THEN (pa->>'level')::numeric ELSE NULL END < 1
							OR CASE WHEN jsonb_typeof(pa->'level') = 'number' THEN (pa->>'level')::numeric ELSE NULL END
								 != floor(CASE WHEN jsonb_typeof(pa->'level') = 'number' THEN (pa->>'level')::numeric ELSE NULL END)
							OR ((pa->>'type') = 'const' AND NOT (pa ? 'base'))
							OR ((pa->>'type') = 'multiplier' AND (
								jsonb_typeof(pa->'base') IS DISTINCT FROM 'number'
								OR jsonb_typeof(pa->'additional') IS DISTINCT FROM 'number'))
						)
					)
				  )
			)`},
		{levelRoleCatInvalidAssignmentExperience, fmt.Sprintf(`
			SELECT EXISTS(
				SELECT 1 FROM "role_assignment" a
				WHERE a."experience" IS NOT NULL
				  AND (a."experience" < 0 OR a."experience" > %d)
			)`, levelRoleMaxExperience)},
		{levelRoleCatInvalidProfileHideReference, `
			SELECT EXISTS(
				SELECT 1 FROM "role_assignment" a
				JOIN "role" r ON r.id = a."roleId"
				WHERE a."isHideProfile" = true
				  AND r."canHideProfileByUser" = false
			)`},
	}
	for _, c := range checks {
		var violated bool
		if err := tx.QueryRow(ctx, c.query).Scan(&violated); err != nil {
			return fmt.Errorf("level role preflight query (%s): %w", c.category, err)
		}
		if violated {
			return fmt.Errorf("level role preflight: %s", c.category)
		}
	}
	return tx.Commit(ctx)
}

// checkLevelRoleColumns validates the shapes (data type + nullability + default)
// of the level columns that exist. Any mismatch aborts with the fixed category
// only (no table / column names in the error)。存在しない column は検証しない
// (partial schema でも skip しない、は「存在する column は必ず検証する」の意)。
func checkLevelRoleColumns(ctx context.Context, tx pgx.Tx) error {
	actual := make(map[string]columnSpec)
	rows, err := tx.Query(ctx, `
		SELECT table_name, column_name, data_type,
		       COALESCE(character_maximum_length, 0), is_nullable,
		       COALESCE(column_default, '')
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name IN ('role', 'role_assignment')
		  AND column_name IN ('levelPolicies','canHideProfileByUser','experience','isHideProfile','policies')
	`)
	if err != nil {
		return fmt.Errorf("read level role preflight columns: %w", err)
	}
	for rows.Next() {
		var table, column, dataType, nullable, columnDefault string
		var maxLen int
		if err := rows.Scan(&table, &column, &dataType, &maxLen, &nullable, &columnDefault); err != nil {
			rows.Close()
			return fmt.Errorf("scan level role preflight column: %w", err)
		}
		actual[table+"."+column] = columnSpec{
			table: table, column: column, dataType: dataType, maxLen: maxLen,
			nullable: nullable == "YES", columnDefault: columnDefault,
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate level role preflight columns: %w", err)
	}
	// 存在する column だけを検証する (partial schema でも skip しない)。
	for _, spec := range levelRoleColumnSpecs {
		info, ok := actual[spec.table+"."+spec.column]
		if !ok {
			continue
		}
		if info.dataType != spec.dataType || info.nullable != spec.nullable ||
			(spec.maxLen != 0 && info.maxLen != spec.maxLen) ||
			(spec.defaultExpr != nil && info.columnDefault != *spec.defaultExpr) {
			return fmt.Errorf("level role preflight: %s", levelRoleCatIncompatibleColumn)
		}
	}
	return nil
}
