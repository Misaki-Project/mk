# Level Role Frontend Contract & i18n Implementation Plan (Frontend PR 1)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Define the fork's API contract and i18n surface for the CherryPick level-role feature: the generated `misskey-js` types (`Role.target` including `manualLevel`, `levelPolicies`, `canHideProfileByUser`, optional per-requester `experience`, `roleAssigns.experience`/`isHideProfile`, `admin/roles/change-exp`, `roles/profile-hide`), the complete ja-JP locale key set, and the shared pure frontend role-level helper + fixtures with tests.

**Architecture:** `pnpm build-misskey-js-with-types` generates `misskey-js` autogen from the fork's TypeScript backend endpoint definitions and JSON schemas, so this monorepo's generator requires backend source changes (entities, json-schemas, endpoint files, `endpoint-list.ts` registration) **purely as the contract source**. Those backend files also receive a minimal, runnable dev implementation so the fork dev stack can answer a **contract-smoke** E2E; this is NOT production behavior — full correctness (level engine, policy aggregation, transactions, golden cases, cache invalidation) is owned by mk-go (`Misaki-Project/mk`). No level-calculation engine is duplicated in TypeScript. Locale changes are limited to `locales/ja-JP.yml` (hand-edited); `packages/i18n` artifacts are regenerated.

**Tech Stack:** TypeScript 6.0.2, NestJS (fork backend contract source), TypeORM, `openapi-typescript`-based `misskey-js-type-generator`, Vue 3.5 / vue-tsc 3.3, vitest 4.1 (happy-dom), pnpm workspaces, `packages/i18n` autogen generation.

## Global Constraints

All file paths are relative to the fork repo root `Misaki-Project/misskey-ts`, based on the current submodule HEAD `ff25eac144c64d3ca1a06862547f7b101d315f98`. The authoritative spec is `docs/superpowers/specs/2026-08-16-cherrypick-level-role-compatibility-design.md`; the CherryPick reference is `Misaki-Project/cherrypick` at `b30826d8ae`.

- **Contract source vs production:** the fork's TypeScript backend changes in this plan exist **only because this monorepo's generator builds from backend endpoint/schema definitions**. They are the contract source plus a minimal dev/E2E serve implementation. **Full correctness is mk-go's scope; this plan claims NO production behavior.** Existing `manual`/`conditional` role response shapes and behavior must not change.
- **Generated contract shape (exact):** `Role.target` = `'manual' | 'conditional' | 'manualLevel'`; `Role.levelPolicies` = `{ baseLevel: number; experiencePolicies: { level: number; type: 'const'|'linear'|'exponential'; base: number; additional?: number; exponential?: number }[] } | null`; `Role.canHideProfileByUser: boolean`; `Role.experience?: { minLevel; maxLevel; currentLevel; currentExp; nextLevelExp: number | null; totalExp } | null`. **`nextLevelExp` MUST be `number | null` (null at max level); CherryPick's `NaN` must NOT be ported.**
- **Endpoint request field names MUST match the backend contract exactly:** `admin/roles/change-exp` uses `setMode` (`enum: ['set','add','multiplier']`); `roles/profile-hide` uses `hide` (boolean).
- **DB contract (fork dev schema mirrors the spec):** `role_assignment.experience` is `bigint NULL`; `role_assignment.isHideProfile` is **`boolean NULL`** (nullable, no default — align exactly); `role.canHideProfileByUser` is `boolean NOT NULL DEFAULT false`; `role.levelPolicies` is `jsonb NOT NULL DEFAULT '{}'::jsonb`; `role_target_enum` gains value `'manualLevel'`.
- **Migration rule:** do NOT edit merged TypeORM migrations. Add ONE new timestamped file. The `down` must be a safe no-op/guarded removal for this dev-only one-way migration; **never use `ALTER TYPE ... DROP VALUE`** (PostgreSQL offers no safe unconditional enum-value drop — the value may be in use; production down is owned by mk-go).
- **Locale rule:** only `locales/ja-JP.yml` is hand-edited, and it must contain ALL keys this feature needs (including `_experience.changeExpConfirm` and `_moderationLogTypes.changeExperienceRole`), because Frontend PR 2/3 add no locale edits. After regeneration, `git diff --name-only develop -- 'locales/*.yml' | Select-String -NotMatch '^locales/ja-JP\.yml$'` must be empty (no output).
- **Regeneration rule:** `pnpm build-misskey-js-with-types` regenerates `packages/misskey-js/src/autogen/*` and `packages/misskey-js/built/*`; commit the diff (it is the committed contract artifact).
- **SPDX headers:** every new `.ts`/`.js` file carries the AGPL header.
- **Privacy gate:** fixtures/tests use synthetic values only; no production-derived IDs, values, counts, paths, or hashes.
- **No auto-commit:** the `git commit` steps below are conditional — execute them ONLY if you have explicit authorization to commit. Never push/merge/PR automatically.
- Do not modify anything outside the files listed in this plan.

---

### Task 1: Fork backend model types and entity fields

**Files:**
- Modify: `packages/backend/src/models/Role.ts`
- Modify: `packages/backend/src/models/RoleAssignment.ts`
- Test: `packages/backend/test/unit/role-level.test.ts`

**Interfaces:**
- Produces (consumed by Tasks 2-8 and, via the generated types, by Plans 2/3):
  - `RoleExperienceSetMode` const/type `{ Set: 'set'; Add: 'add'; Multiplier: 'multiplier' }` and `'set' | 'add' | 'multiplier'`.
  - `RoleExperienceLevelPolicyValue`: `{ level: number; type: 'const' | 'linear' | 'exponential'; base: number; additional?: number; exponential?: number }`.
  - `RoleLevelPolicies`: `{ baseLevel: number; experiencePolicies: RoleExperienceLevelPolicyValue[] }`.
  - `RoleExperience`: `{ minLevel: number; maxLevel: number; currentLevel: number; currentExp: number; nextLevelExp: number | null; totalExp: number }`.
  - `RoleExperiencePolicyCulcValue`: `(({ type: 'const'; base: number | boolean; additional: number }) | ({ type: 'multiplier'; base: number | boolean; additional: number }) | { type: 'base' }) & { level: number }` (for `policies[key].policyAsLevel`; `base` is `number | boolean` because boolean policies use boolean constants).
  - `MiRole.target` gains `'manualLevel'`; `MiRole.canHideProfileByUser: boolean`; `MiRole.levelPolicies: RoleLevelPolicies | null`; `MiRole.policies` values gain `policyAsLevel: RoleExperiencePolicyCulcValue | null`.
  - `MiRoleAssignment.experience: string | null` (bigint arrives as string from pg); `MiRoleAssignment.isHideProfile: boolean | null` (nullable DB column).

- [ ] **Step 1: Write the failing model test**

Create `packages/backend/test/unit/role-level.test.ts`:

```ts
/*
 * SPDX-FileCopyrightText: syuilo and misskey-project
 * SPDX-License-Identifier: AGPL-3.0-only
 */

import { describe, expectTypeOf, it } from 'vitest';
import { RoleExperienceSetMode } from '@/models/Role.js';

describe('Role model level-role types', () => {
	it('RoleExperienceSetMode values match the backend contract', () => {
		expectTypeOf(RoleExperienceSetMode).toEqualTypeOf<{ Set: 'set'; Add: 'add'; Multiplier: 'multiplier' }>();
	});

	it('MiRole.target includes manualLevel and assignment isHideProfile is nullable', async () => {
		const { MiRole } = await import('@/models/Role.js');
		expectTypeOf<MiRole['target']>().toEqualTypeOf<'manual' | 'conditional' | 'manualLevel'>();
		const { MiRoleAssignment } = await import('@/models/RoleAssignment.js');
		expectTypeOf<MiRoleAssignment['isHideProfile']>().toEqualTypeOf<boolean | null>();
		expectTypeOf<MiRoleAssignment['experience']>().toEqualTypeOf<string | null>();
	});
});
```

- [ ] **Step 2: Run to verify it fails**

Run: `pnpm --filter backend typecheck`
Expected: FAIL — `Property 'manualLevel' does not exist` / `Cannot find name 'RoleExperienceSetMode'`.

- [ ] **Step 3: Add the level-role types to `packages/backend/src/models/Role.ts`**

Append at the end of the file:

```ts
export type RoleExperiencePolicyCalcValueBase = {
	base: number | boolean;
	additional: number;
};

export type RoleExperiencePolicyCulcValueConst = RoleExperiencePolicyCalcValueBase & { type: 'const' };

export type RoleExperiencePolicyCulcValueMultiplier = RoleExperiencePolicyCalcValueBase & { type: 'multiplier' };

export type RoleExperiencePolicyCulcValueBaseDefault = { type: 'base' };

export type RoleExperiencePolicyCulcValue =
	(RoleExperiencePolicyCulcValueConst | RoleExperiencePolicyCulcValueMultiplier | RoleExperiencePolicyCulcValueBaseDefault) & { level: number };

export type RoleExperienceLevelPolicyValue = {
	level: number;
	type: 'const' | 'linear' | 'exponential';
	base: number;
	additional?: number;
	exponential?: number;
};

export type RoleLevelPolicies = {
	baseLevel: number;
	experiencePolicies: RoleExperienceLevelPolicyValue[];
};

export const RoleExperienceSetMode = {
	Set: 'set',
	Add: 'add',
	Multiplier: 'multiplier',
} as const;

export type RoleExperienceSetMode = typeof RoleExperienceSetMode[keyof typeof RoleExperienceSetMode];

export type RoleExperience = {
	minLevel: number;
	maxLevel: number;
	currentLevel: number;
	currentExp: number;
	nextLevelExp: number | null;
	totalExp: number;
};
```

Modify the `MiRole` class:

```ts
	@Column('enum', {
		enum: ['manual', 'conditional', 'manualLevel'],
		default: 'manual',
	})
	public target: 'manual' | 'conditional' | 'manualLevel';

	// ユーザーによるプロフィールからの非表示を許可するかどうか
	@Column('boolean', {
		default: false,
	})
	public canHideProfileByUser: boolean;

	// レベルロールの経験値ポリシー
	@Column('jsonb', {
		default: { },
	})
	public levelPolicies: RoleLevelPolicies | null;
```

Replace the `policies` column type:

```ts
	@Column('jsonb', {
		default: { },
	})
	public policies: Record<string, {
		useDefault: boolean;
		priority: number;
		value: any;
		policyAsLevel: RoleExperiencePolicyCulcValue | null;
	}>;
```

- [ ] **Step 4: Add the assignment fields to `packages/backend/src/models/RoleAssignment.ts`**

```ts
	@Index('IDX_role_assignment_experience')
	@Column('bigint', {
		nullable: true,
		comment: 'Experience for the role',
	})
	public experience: string | null;

	// プロフィールからの非表示状態。boolean NULL (specのDB契約に合わせる)
	@Column('boolean', {
		nullable: true,
	})
	public isHideProfile: boolean | null;
```

- [ ] **Step 5: Run typecheck and tests**

Run: `pnpm --filter backend typecheck`
Expected: PASS.

Run: `pnpm --filter backend test -- role-level`
Expected: PASS.

- [ ] **Step 6: Commit (conditional)**

```bash
git add packages/backend/src/models/Role.ts packages/backend/src/models/RoleAssignment.ts packages/backend/test/unit/role-level.test.ts
git commit -m "feat(contract): add level-role fields to Role and RoleAssignment models"
```

Only run the commit if explicitly authorized; otherwise proceed with changes uncommitted.

---

### Task 2: Fork dev migration and minimal experience helpers

**Files:**
- Create: `packages/backend/migration/1784900000000-AddLevelRoleFields.js`
- Create: `packages/backend/src/core/RoleLevelService.ts`
- Modify: `packages/backend/test/unit/role-level.test.ts`

**Interfaces:**
- Consumes: model types from Task 1.
- Produces (minimal; the full level engine is mk-go's scope):
  - `export function parseExperience(raw: string | number | null | undefined): number | null` — bigint string to a safe number in `0..Number.MAX_SAFE_INTEGER`, else `null`.
  - `export function applySetMode(current: number, setMode: RoleExperienceSetMode, value: number): number` — clamped to `0..Number.MAX_SAFE_INTEGER`.

**Why a runnable dev implementation is retained:** the fork dev stack must answer the contract-smoke E2E (`admin/roles/change-exp` must persist experience and `admin/show-user` must expose it). That only requires parse/clamp of the experience value. The level-calculation engine is NOT duplicated here; its correctness is verified by mk-go's synthetic golden cases.

- [ ] **Step 1: Verify the fixed migration ID is unused**

Run: `if (Test-Path -LiteralPath 'packages/backend/migration/1784900000000-AddLevelRoleFields.js') { throw 'migration ID 1784900000000 is already in use' }`
Expected: exit 0。`1784900000000`は現行latest migration ID `1784899839024`より大きく、このbranchで未使用であること。

- [ ] **Step 2: Write the failing helper tests**

Append to `packages/backend/test/unit/role-level.test.ts`:

```ts
import { applySetMode, parseExperience } from '@/core/RoleLevelService.js';

describe('RoleLevelService (fork dev implementation)', () => {
	it('parseExperience returns null for unsafe values', () => {
		expect(parseExperience(null)).toBeNull();
		expect(parseExperience(Number.MAX_SAFE_INTEGER + 1)).toBeNull();
		expect(parseExperience(-1)).toBeNull();
		expect(parseExperience('9007199254740991')).toBe(Number.MAX_SAFE_INTEGER);
	});

	it('applySetMode clamps into 0..MAX_SAFE_INTEGER', () => {
		expect(applySetMode(10, 'set', 5)).toBe(5);
		expect(applySetMode(10, 'add', -20)).toBe(0);
		expect(applySetMode(10, 'multiplier', 1.5)).toBe(15);
	});
});
```

- [ ] **Step 3: Run to verify it fails**

Run: `pnpm --filter backend test`
Expected: FAIL — `Cannot find module '@/core/RoleLevelService.js'`.

- [ ] **Step 4: Implement `packages/backend/src/core/RoleLevelService.ts`**

```ts
/*
 * SPDX-FileCopyrightText: syuilo and misskey-project
 * SPDX-License-Identifier: AGPL-3.0-only
 */

import type { RoleExperienceSetMode } from '@/models/Role.js';

// 外部APIで扱う経験値は0..Number.MAX_SAFE_INTEGERへ制限する。
// DBのbigintはstringで届くため、安全な整数へ変換する。
// 完全なlevel計算エンジンはmk-goのscopeであり、ここには実装しない。
export function parseExperience(raw: string | number | null | undefined): number | null {
	if (raw == null) return null;
	const n = Number(raw);
	if (!Number.isFinite(n) || n < 0 || n > Number.MAX_SAFE_INTEGER) return null;
	return Math.floor(n);
}

export function applySetMode(current: number, setMode: RoleExperienceSetMode, value: number): number {
	let next: number;
	switch (setMode) {
		case 'set': next = value; break;
		case 'add': next = current + value; break;
		case 'multiplier': next = current * value; break;
	}
	return Math.min(Math.max(Math.floor(next), 0), Number.MAX_SAFE_INTEGER);
}
```

- [ ] **Step 5: Create the fork dev migration**

Create `packages/backend/migration/1784900000000-AddLevelRoleFields.js`:

```js
/*
 * SPDX-FileCopyrightText: syuilo and misskey-project
 * SPDX-License-Identifier: AGPL-3.0-only
 */

export class AddLevelRoleFields1784900000000 {
    name = 'AddLevelRoleFields1784900000000'

    async up(queryRunner) {
        await queryRunner.query(`ALTER TYPE "role_target_enum" ADD VALUE IF NOT EXISTS 'manualLevel'`);
        await queryRunner.query(`ALTER TABLE "role" ADD COLUMN IF NOT EXISTS "canHideProfileByUser" boolean NOT NULL DEFAULT false`);
        await queryRunner.query(`ALTER TABLE "role" ADD COLUMN IF NOT EXISTS "levelPolicies" jsonb NOT NULL DEFAULT '{}'::jsonb`);
        await queryRunner.query(`ALTER TABLE "role_assignment" ADD COLUMN IF NOT EXISTS "experience" bigint`);
        await queryRunner.query(`ALTER TABLE "role_assignment" ADD COLUMN IF NOT EXISTS "isHideProfile" boolean`);
        await queryRunner.query(`CREATE INDEX IF NOT EXISTS "IDX_role_assignment_experience" ON "role_assignment" ("experience")`);
    }

    async down(queryRunner) {
        // このmigrationはfork dev/E2E専用のone-way migration。
        // ALTER TYPE ... DROP VALUEは安全に実行できない(値が使用中だと失敗する)ためenum valueは除去しない。
        // 本番のdownはmk-goが所有する。dev DBに対して列とindexのみ除去する。
        await queryRunner.query(`DROP INDEX IF EXISTS "IDX_role_assignment_experience"`);
        await queryRunner.query(`ALTER TABLE "role_assignment" DROP COLUMN IF EXISTS "isHideProfile"`);
        await queryRunner.query(`ALTER TABLE "role_assignment" DROP COLUMN IF EXISTS "experience"`);
        await queryRunner.query(`ALTER TABLE "role" DROP COLUMN IF EXISTS "levelPolicies"`);
        await queryRunner.query(`ALTER TABLE "role" DROP COLUMN IF EXISTS "canHideProfileByUser"`);
    }
}
```

- [ ] **Step 6: Run tests, typecheck, migration check**

Run: `pnpm --filter backend test`
Expected: PASS.

Run: `pnpm --filter backend typecheck`
Expected: PASS.

Run: `pnpm --filter backend check-migrations`
Expected: PASS — no pending DDL. If it reports pending DDL, verify the entity column types match the migration exactly (`bigint` nullable, `boolean` nullable for `isHideProfile`, `jsonb NOT NULL DEFAULT '{}'`).

- [ ] **Step 7: Commit (conditional)**

```bash
git add packages/backend/migration/1784900000000-AddLevelRoleFields.js packages/backend/src/core/RoleLevelService.ts packages/backend/test/unit/role-level.test.ts
git commit -m "feat(contract): add fork dev migration and experience helpers for level roles"
```

---

### Task 3: JSON-schema contract extension and endpoint registration

**Files:**
- Modify: `packages/backend/src/models/json-schema/role.ts`
- Modify: `packages/backend/src/models/json-schema/user.ts`
- Modify: `packages/backend/src/server/api/endpoint-list.ts`
- Test: `packages/frontend/test/unit/level-role-contract.test.ts` (red until Task 6 regenerates types)

**Interfaces:**
- Produces (consumed by Plans 2/3): `Misskey.entities.Role['target']` includes `'manualLevel'`; `Role['levelPolicies']`; `Role['canHideProfileByUser']`; `Role['experience']` with `nextLevelExp: number | null`; `Misskey.entities.UserDetailed['roles'][number]` = `RoleLite & { canHideProfileByUser: boolean; isHideProfile: boolean; experience?: RoleExperience | null }`; the `admin/roles/change-exp` and `roles/profile-hide` endpoint operations. No `badgeRoles` schema change (no `id` extension; backend filters hidden badges, see Plan 3).

- [ ] **Step 1: Write the failing contract-assertion test**

Create `packages/frontend/test/unit/level-role-contract.test.ts`:

```ts
/*
 * SPDX-FileCopyrightText: syuilo and misskey-project
 * SPDX-License-Identifier: AGPL-3.0-only
 */

import { describe, expectTypeOf, it } from 'vitest';
import type * as Misskey from 'misskey-js';

type LevelExperience = {
	minLevel: number;
	maxLevel: number;
	currentLevel: number;
	currentExp: number;
	nextLevelExp: number | null;
	totalExp: number;
};

describe('generated level-role contract', () => {
	it('Role.target includes manualLevel', () => {
		expectTypeOf<Misskey.entities.Role['target']>().toEqualTypeOf<'manual' | 'conditional' | 'manualLevel'>();
	});

	it('Role.experience uses minLevel/maxLevel and nullable nextLevelExp', () => {
		expectTypeOf<NonNullable<Misskey.entities.Role['experience']>>().toEqualTypeOf<LevelExperience>();
	});

	it('Role.levelPolicies is a nullable object with baseLevel and experiencePolicies', () => {
		expectTypeOf<NonNullable<Misskey.entities.Role['levelPolicies']>['experiencePolicies'][number]['type']>().toEqualTypeOf<'const' | 'linear' | 'exponential'>();
	});

	it('UserDetailed.roles items expose canHideProfileByUser, isHideProfile and optional experience', () => {
		expectTypeOf<Misskey.entities.UserDetailed['roles'][number]['canHideProfileByUser']>().toEqualTypeOf<boolean>();
		expectTypeOf<Misskey.entities.UserDetailed['roles'][number]['isHideProfile']>().toEqualTypeOf<boolean>();
		expectTypeOf<NonNullable<Misskey.entities.UserDetailed['roles'][number]['experience']>>().toEqualTypeOf<LevelExperience>();
	});

	it('admin/roles/change-exp uses setMode and roles/profile-hide uses hide', () => {
		expectTypeOf<Misskey.api.AdminRolesChangeExpRequest['setMode']>().toEqualTypeOf<'set' | 'add' | 'multiplier'>();
		expectTypeOf<Misskey.api.RolesProfileHideRequest['hide']>().toEqualTypeOf<boolean>();
	});

	it('admin/show-user roleAssigns expose experience and isHideProfile', () => {
		expectTypeOf<Misskey.entities.AdminShowUserResponse['roleAssigns'][number]['experience']>().toEqualTypeOf<number | null>();
		expectTypeOf<Misskey.entities.AdminShowUserResponse['roleAssigns'][number]['isHideProfile']>().toEqualTypeOf<boolean>();
	});
});
```

- [ ] **Step 2: Run to verify it fails**

Run: `pnpm --filter frontend typecheck`
Expected: FAIL — `Type '"manual" | "conditional"' is not assignable` / `Property 'levelPolicies' does not exist on type 'Role'`.

- [ ] **Step 3: Extend `packages/backend/src/models/json-schema/role.ts`**

In `packedRoleSchema`, change the `target` enum:

```ts
				target: {
					type: 'string',
					optional: false, nullable: false,
					enum: ['manual', 'conditional', 'manualLevel'],
				},
```

Add `canHideProfileByUser`, `levelPolicies`, `experience` as new properties (e.g. after the `canEditMembersByModerator` property):

```ts
				canHideProfileByUser: {
					type: 'boolean',
					optional: false, nullable: false,
					example: false,
				},
				levelPolicies: {
					type: 'object',
					optional: true, nullable: true,
					properties: {
						baseLevel: { type: 'integer', optional: false, nullable: false },
						experiencePolicies: {
							type: 'array',
							optional: false, nullable: false,
							items: {
								type: 'object',
								optional: false, nullable: false,
								properties: {
									level: { type: 'integer', optional: false, nullable: false },
									type: { type: 'string', optional: false, nullable: false, enum: ['const', 'linear', 'exponential'] },
									base: { type: 'number', optional: false, nullable: false },
									additional: { type: 'number', optional: true, nullable: true },
									exponential: { type: 'number', optional: true, nullable: true },
								},
							},
						},
					},
				},
				experience: {
					type: 'object',
					optional: true, nullable: true,
					properties: {
						minLevel: { type: 'integer', optional: false, nullable: false },
						maxLevel: { type: 'integer', optional: false, nullable: false },
						currentLevel: { type: 'integer', optional: false, nullable: false },
						currentExp: { type: 'integer', optional: false, nullable: false },
						nextLevelExp: { type: 'integer', optional: false, nullable: true },
						totalExp: { type: 'integer', optional: false, nullable: false },
					},
				},
```

In the `policies.additionalProperties` inner object, add `policyAsLevel` next to `useDefault`:

```ts
								policyAsLevel: {
									type: 'array',
									optional: true, nullable: true,
									items: {
										type: 'object',
										optional: false, nullable: false,
										properties: {
											level: { type: 'integer', optional: false, nullable: false },
											type: { type: 'string', optional: false, nullable: false, enum: ['base', 'const', 'multiplier'] },
											base: { oneOf: [{ type: 'number' }, { type: 'boolean' }], optional: false, nullable: false },
											additional: { type: 'number', optional: true, nullable: true },
										},
									},
								},
```

Note on schema validity: the custom JSON-schema (`@/misc/json-schema.ts`) and the OpenAPI converter (`packages/backend/src/server/api/openapi/schemas.ts`, lines 15-52) support `allOf`/`oneOf`/`anyOf` (recursively converted), `ref` (in `res` position), and `nullable` (adds `'null'` to the type array). The generated types are produced by `openapi-typescript`, which resolves all of these. `oneOf` with two primitive members is already used elsewhere in the tree (e.g. `policies.additionalProperties.value`). Do not introduce `prefixItems` or `$ref` in `param` position — neither is supported for request schemas.

- [ ] **Step 4: Extend `packages/backend/src/models/json-schema/user.ts`**

Replace the `roles` items (currently `ref: 'RoleLite'`) with an inline `allOf` object combining `RoleLite` plus the per-role hide state. This mirrors the existing `packedRoleSchema` `allOf` idiom (already resolved correctly by the converter and generator):

```ts
		roles: {
			type: 'array',
			nullable: false, optional: false,
			items: {
				type: 'object',
				nullable: false, optional: false,
				allOf: [
					{
						type: 'object',
						ref: 'RoleLite',
					},
					{
						type: 'object',
						properties: {
							canHideProfileByUser: {
								type: 'boolean',
								nullable: false, optional: false,
							},
							isHideProfile: {
								type: 'boolean',
								nullable: false, optional: false,
							},
							experience: {
								type: 'object',
								nullable: true, optional: true,
								properties: {
									minLevel: { type: 'integer', nullable: false, optional: false },
									maxLevel: { type: 'integer', nullable: false, optional: false },
									currentLevel: { type: 'integer', nullable: false, optional: false },
									currentExp: { type: 'integer', nullable: false, optional: false },
									nextLevelExp: { type: 'integer', nullable: true, optional: false },
									totalExp: { type: 'integer', nullable: false, optional: false },
								},
							},
						},
					},
				],
			},
		},
```

Do NOT change the `badgeRoles` items schema (no `id` extension). Leave `badgeRoles` exactly as `{ name, iconUrl, displayOrder }` — the backend excludes hidden badges from public views; the frontend has no hide metadata to filter on there.

- [ ] **Step 5: Register the new endpoints in `packages/backend/src/server/api/endpoint-list.ts`**

This file is the manual endpoint registry (`endpoints.ts` imports it; `EndpointsModule` builds providers from it). Add two lines:

After `export * as 'admin/roles/assign' from './endpoints/admin/roles/assign.js';` insert:

```ts
export * as 'admin/roles/change-exp' from './endpoints/admin/roles/change-exp.js';
```

Between `export * as 'roles/notes' ...` and `export * as 'roles/show' ...` insert:

```ts
export * as 'roles/profile-hide' from './endpoints/roles/profile-hide.js';
```

- [ ] **Step 6: Run backend typecheck to confirm the schema files are valid**

Run: `pnpm --filter backend typecheck`
Expected: PASS (json-schema files are plain objects; `endpoint-list.ts` entries resolve only after Task 4 creates the files, so run this step again after Task 4).

- [ ] **Step 7: Commit (conditional)**

```bash
git add packages/backend/src/models/json-schema/role.ts packages/backend/src/models/json-schema/user.ts packages/backend/src/server/api/endpoint-list.ts packages/frontend/test/unit/level-role-contract.test.ts
git commit -m "feat(contract): extend Role/User json-schemas and register level-role endpoints"
```

---

### Task 4: New level-role endpoints (contract source + minimal dev handlers)

**Files:**
- Create: `packages/backend/src/server/api/endpoints/admin/roles/change-exp.ts`
- Create: `packages/backend/src/server/api/endpoints/roles/profile-hide.ts`

**Interfaces:**
- Consumes: `RoleExperienceSetMode` (Task 1), `parseExperience`/`applySetMode` (Task 2), repositories via DI.
- Produces (exact request/response contract):
  - `admin/roles/change-exp`: request `{ roleId, userId, setMode: 'set'|'add'|'multiplier', value: number, assignForce?: boolean, note?: string|null }`, response `UserDetailed`. Errors: `NO_SUCH_ROLE` (id `6503c040-6af4-4ed9-bf07-f2dd16678eab`), `NO_SUCH_USER` (id `558ea170-f653-4700-94d0-5a818371d0df`), `ACCESS_DENIED` (id `25b5bc31-dc79-4ebd-9bd2-c84978fd052c`), `INVALID_ROLE_TARGET` (id `a2f3b5c4-1d8e-4b0e-9f6c-7a2d3e4f5b6a`).
  - `roles/profile-hide`: request `{ roleId, hide: boolean }`, response 204. Errors: `NO_SUCH_ROLE` (id `30aaaee3-4792-48dc-ab0d-cf501a575ac5`), `CANNOT_HIDE_THIS_ROLE` (id `a21fb109-1d95-9a10-fe18-42ea7c91dabe`).

- [ ] **Step 1: Confirm the contract test is still red for the new endpoints**

Run: `pnpm --filter frontend typecheck`
Expected: FAIL — `Property 'AdminRolesChangeExpRequest' does not exist` / `Property 'RolesProfileHideRequest' does not exist` (endpoints not yet generated).

- [ ] **Step 2: Create `packages/backend/src/server/api/endpoints/admin/roles/change-exp.ts`**

```ts
/*
 * SPDX-FileCopyrightText: syuilo and misskey-project
 * SPDX-License-Identifier: AGPL-3.0-only
 */

import { Inject, Injectable } from '@nestjs/common';
import type { RoleAssignmentsRepository, RolesRepository, UsersRepository } from '@/models/_.js';
import { Endpoint } from '@/server/api/endpoint-base.js';
import { DI } from '@/di-symbols.js';
import { ApiError } from '@/server/api/error.js';
import { UserEntityService } from '@/core/entities/UserEntityService.js';
import { RoleService } from '@/core/RoleService.js';
import { IdService } from '@/core/IdService.js';
import { RoleExperienceSetMode } from '@/models/Role.js';
import { applySetMode, parseExperience } from '@/core/RoleLevelService.js';

export const meta = {
	tags: ['admin', 'role'],

	requireCredential: true,
	requireModerator: true,
	kind: 'write:admin:roles',

	errors: {
		noSuchRole: {
			message: 'No such role.',
			code: 'NO_SUCH_ROLE',
			id: '6503c040-6af4-4ed9-bf07-f2dd16678eab',
		},
		noSuchUser: {
			message: 'No such user.',
			code: 'NO_SUCH_USER',
			id: '558ea170-f653-4700-94d0-5a818371d0df',
		},
		accessDenied: {
			message: 'Only administrators can edit members of the role.',
			code: 'ACCESS_DENIED',
			id: '25b5bc31-dc79-4ebd-9bd2-c84978fd052c',
		},
		invalidRoleTarget: {
			message: 'Invalid role target.',
			code: 'INVALID_ROLE_TARGET',
			id: 'a2f3b5c4-1d8e-4b0e-9f6c-7a2d3e4f5b6a',
		},
	},
} as const;

export const paramDef = {
	type: 'object',
	properties: {
		roleId: { type: 'string', format: 'misskey:id' },
		userId: { type: 'string', format: 'misskey:id' },
		setMode: { type: 'string', enum: Object.values(RoleExperienceSetMode) },
		value: { type: 'number', nullable: false },
		assignForce: { type: 'boolean', nullable: true },
		note: { type: 'string', nullable: true },
	},
	required: ['roleId', 'userId', 'setMode', 'value'],
} as const;

@Injectable()
export default class extends Endpoint<typeof meta, typeof paramDef> { // eslint-disable-line import/no-default-export
	constructor(
		@Inject(DI.usersRepository)
		private usersRepository: UsersRepository,

		@Inject(DI.rolesRepository)
		private rolesRepository: RolesRepository,

		@Inject(DI.roleAssignmentsRepository)
		private roleAssignmentsRepository: RoleAssignmentsRepository,

		private roleService: RoleService,
		private userEntityService: UserEntityService,
		private idService: IdService,
	) {
		super(meta, paramDef, async (ps, me) => {
			const role = await this.rolesRepository.findOneBy({ id: ps.roleId });
			if (role == null) throw new ApiError(meta.errors.noSuchRole);

			// 管理者以外はcanEditMembersByModeratorが無いロールを変更できない
			// (admin/roles/assign.tsと同じroleService.isAdministratorパターン)
			if (!role.canEditMembersByModerator && !(await this.roleService.isAdministrator(me))) {
				throw new ApiError(meta.errors.accessDenied);
			}

			const user = await this.usersRepository.findOneBy({ id: ps.userId });
			if (user == null) throw new ApiError(meta.errors.noSuchUser);

			if (role.target !== 'manualLevel') {
				throw new ApiError(meta.errors.invalidRoleTarget);
			}

			const current = parseExperience((await this.roleAssignmentsRepository.findOneBy({ userId: user.id, roleId: role.id }))?.experience ?? 0) ?? 0;
			const next = applySetMode(current, ps.setMode, ps.value);

			const assign = await this.roleAssignmentsRepository.findOneBy({ userId: user.id, roleId: role.id });
			if (assign == null) {
				// assignForceなしでは新規アサインを自動作成しない(fork dev実装)
				const assignForce = ps.assignForce ?? true;
				if (!assignForce) throw new ApiError(meta.errors.noSuchRole);
				await this.roleAssignmentsRepository.insertOne({
					id: this.idService.gen(),
					userId: user.id,
					roleId: role.id,
					experience: String(next),
					isHideProfile: false,
				});
			} else {
				await this.roleAssignmentsRepository.update(assign.id, { experience: String(next) });
			}

			await this.rolesRepository.update(role.id, { lastUsedAt: new Date() });

			return await this.userEntityService.pack(user.id, me, { schema: 'UserDetailed' });
		});
	}
}
```

Note: the handler is the minimal dev implementation needed for the fork contract smoke; production transactionality, concurrency protection, event emission, and cache invalidation are mk-go's scope.

- [ ] **Step 3: Create `packages/backend/src/server/api/endpoints/roles/profile-hide.ts`**

```ts
/*
 * SPDX-FileCopyrightText: syuilo and misskey-project
 * SPDX-License-Identifier: AGPL-3.0-only
 */

import ms from 'ms';
import { Inject, Injectable } from '@nestjs/common';
import type { RoleAssignmentsRepository, RolesRepository } from '@/models/_.js';
import { Endpoint } from '@/server/api/endpoint-base.js';
import { DI } from '@/di-symbols.js';
import { ApiError } from '@/server/api/error.js';

export const meta = {
	tags: ['role'],

	requireCredential: true,
	kind: 'write:account',
	requireAdmin: false,

	limit: {
		duration: ms('1hour'),
		max: 20,
		minInterval: ms('1sec'),
	},

	errors: {
		noSuchRole: {
			message: 'No such role.',
			code: 'NO_SUCH_ROLE',
			id: '30aaaee3-4792-48dc-ab0d-cf501a575ac5',
		},
		cannotHideThisRole: {
			message: 'This role cannot be hidden.',
			code: 'CANNOT_HIDE_THIS_ROLE',
			id: 'a21fb109-1d95-9a10-fe18-42ea7c91dabe',
		},
	},
} as const;

export const paramDef = {
	type: 'object',
	properties: {
		roleId: { type: 'string', format: 'misskey:id' },
		hide: { type: 'boolean', nullable: false },
	},
	required: ['roleId', 'hide'],
} as const;

@Injectable()
export default class extends Endpoint<typeof meta, typeof paramDef> { // eslint-disable-line import/no-default-export
	constructor(
		@Inject(DI.rolesRepository)
		private rolesRepository: RolesRepository,

		@Inject(DI.roleAssignmentsRepository)
		private roleAssignmentsRepository: RoleAssignmentsRepository,
	) {
		super(meta, paramDef, async (ps, me) => {
			const role = await this.rolesRepository.findOneBy({
				id: ps.roleId,
				isPublic: true,
			});
			if (role == null) throw new ApiError(meta.errors.noSuchRole);
			if (!role.canHideProfileByUser) throw new ApiError(meta.errors.cannotHideThisRole);

			const assign = await this.roleAssignmentsRepository.findOneBy({ userId: me.id, roleId: role.id });
			if (assign == null) throw new ApiError(meta.errors.noSuchRole);

			await this.roleAssignmentsRepository.update(assign.id, { isHideProfile: ps.hide });
		});
	}
}
```

- [ ] **Step 4: Run backend typecheck and eslint**

Run: `pnpm --filter backend typecheck`
Expected: PASS.

Run: `pnpm --filter backend eslint`
Expected: PASS.

- [ ] **Step 5: Commit (conditional)**

```bash
git add packages/backend/src/server/api/endpoints/admin/roles/change-exp.ts packages/backend/src/server/api/endpoints/roles/profile-hide.ts
git commit -m "feat(contract): add admin/roles/change-exp and roles/profile-hide endpoints"
```

---

### Task 5: Extend create/update/member-list/user contracts and dev handlers

**Files:**
- Modify: `packages/backend/src/server/api/endpoints/admin/roles/create.ts`
- Modify: `packages/backend/src/server/api/endpoints/admin/roles/update.ts`
- Modify: `packages/backend/src/server/api/endpoints/admin/roles/users.ts`
- Modify: `packages/backend/src/server/api/endpoints/roles/users.ts`
- Modify: `packages/backend/src/server/api/endpoints/admin/show-user.ts`
- Modify: `packages/backend/src/core/RoleService.ts` (fork `create()` must persist `canHideProfileByUser`/`levelPolicies`)
- Modify: `packages/backend/src/core/entities/RoleEntityService.ts`
- Modify: `packages/backend/src/core/entities/UserEntityService.ts`

**Interfaces:**
- Consumes: model types (Task 1), `parseExperience` (Task 2), endpoint files (Task 4).
- Produces:
  - `admin/roles/create`/`update` requests accept `target: 'manual' | 'conditional' | 'manualLevel'`, `canHideProfileByUser: boolean`, `levelPolicies: object`.
  - Public `roles/users` orders by `experience DESC NULLS LAST` when `role.target === 'manualLevel'` and excludes suspended users. `admin/roles/users` preserves its existing order and only adds the experience response field.
  - `admin/show-user` response `roleAssigns[number]` gains `experience: number | null` and `isHideProfile: boolean` (coerced from the nullable DB value).
  - `RoleEntityService.pack` returns `canHideProfileByUser` and `levelPolicies` (NO `experience` computation — the level engine is mk-go's scope; the field is optional in the schema and simply absent at runtime in the fork).
  - `UserEntityService` packs user `roles` items with `canHideProfileByUser`/`isHideProfile` and excludes hidden assignments for non-self/non-moderator viewers; `badgeRoles` is filtered the same way (no schema/`id` change).

- [ ] **Step 1: Extend `admin/roles/create.ts` paramDef**

In `paramDef.properties`, change `target` to include `manualLevel` and add the two fields (keep all existing properties):

```ts
		target: { type: 'string', enum: ['manual', 'conditional', 'manualLevel'] },
		canHideProfileByUser: { type: 'boolean', default: false },
		levelPolicies: {
			type: 'object',
			nullable: true,
			properties: {
				baseLevel: { type: 'number', nullable: false },
				experiencePolicies: { type: 'array', items: { type: 'object' } },
			},
			required: ['baseLevel', 'experiencePolicies'],
		},
```

The handler already calls `this.roleService.create(ps, me)` with the full params object, so no handler change is needed there.

- [ ] **Step 2: Make the fork's `RoleService.create` persist the new fields**

In `packages/backend/src/core/RoleService.ts`, in `create()`, add the two fields to the inserted row (after `policies`):

```ts
			canHideProfileByUser: values.canHideProfileByUser ?? false,
			levelPolicies: values.levelPolicies ?? null,
```

`RoleService.update()` already spreads `...params`, so it persists the new fields once the endpoint forwards them (Step 3).

- [ ] **Step 3: Extend `admin/roles/update.ts` paramDef and handler**

In `paramDef.properties`, add:

```ts
		target: { type: 'string', enum: ['manual', 'conditional', 'manualLevel'] },
		canHideProfileByUser: { type: 'boolean' },
		levelPolicies: {
			type: 'object',
			nullable: true,
			properties: {
				baseLevel: { type: 'number', nullable: false },
				experiencePolicies: { type: 'array', items: { type: 'object' } },
			},
			required: ['baseLevel', 'experiencePolicies'],
		},
```

In the handler's `roleService.update(role, {...})` object, add:

```ts
				canHideProfileByUser: ps.canHideProfileByUser,
				levelPolicies: ps.levelPolicies ?? null,
```

- [ ] **Step 4: Add admin assignment experience and public manualLevel ordering**

In `admin/roles/users.ts`, preserve the existing query ordering. Add `experience` to the response item schema and packed assignment:

```ts
				experience: {
					type: 'integer',
					optional: true, nullable: true,
				},
```

```ts
				experience: parseExperience(assign.experience),
```

Import `parseExperience` from `@/core/RoleLevelService.js`. In public `roles/users.ts`, change `const query =` to `let query =` and apply manualLevel ordering plus the CherryPick suspended-user exclusion before `.limit(...)`:

```ts
			let query = this.queryService.makePaginationQuery(this.roleAssignmentsRepository.createQueryBuilder('assign'), ps.sinceId, ps.untilId, ps.sinceDate, ps.untilDate)
				.andWhere('assign.roleId = :roleId', { roleId: role.id })
				.andWhere(new Brackets(qb => {
					qb
						.where('assign.expiresAt IS NULL')
						.orWhere('assign.expiresAt > :now', { now: new Date() });
				}))
				.innerJoinAndSelect('assign.user', 'user')
				.andWhere('user.isSuspended = FALSE');
			if (role.target === 'manualLevel') {
				query = query.orderBy('assign.experience', 'DESC', 'NULLS LAST');
			}
```

(TypeORM 0.3 `orderBy(property, direction, nulls)` supports `'NULLS LAST'`.)

- [ ] **Step 5: Extend `admin/show-user.ts` response schema and handler**

In `meta.res.properties.roleAssigns.items.properties`, add:

```ts
						experience: {
							type: 'integer',
							optional: true, nullable: true,
						},
						isHideProfile: {
							type: 'boolean',
							optional: true, nullable: false,
						},
```

In the handler's `roleAssigns` map, add the two fields (coerce the nullable DB boolean to a boolean):

```ts
				roleAssigns: roleAssigns.map(a => ({
					createdAt: this.idService.parse(a.id).date.toISOString(),
					expiresAt: a.expiresAt ? a.expiresAt.toISOString() : null,
					roleId: a.roleId,
					experience: parseExperience(a.experience),
					isHideProfile: a.isHideProfile ?? false,
				})),
```

Add the import: `import { parseExperience } from '@/core/RoleLevelService.js';`

- [ ] **Step 6: Extend `RoleEntityService.pack`**

Add the two response fields (no `experience` computation — omitted at runtime; the schema field is optional):

```ts
			canHideProfileByUser: role.canHideProfileByUser,
			levelPolicies: role.levelPolicies,
```

Add these to the `awaitAll({...})` return object (after `canEditMembersByModerator`).

- [ ] **Step 7: Extend `UserEntityService` role/badge packing**

Replace the existing `badgeRoles` block so hidden assignments are excluded for non-self/non-moderator viewers (no schema change; the `isHideProfile` column is nullable, and `!== true` treats `null` as not hidden):

```ts
			badgeRoles: (this.meta.showRoleBadgesOfRemoteUsers || user.host == null) ? Promise.all([
				this.roleService.getUserBadgeRoles(user.id),
				this.roleService.getUserAssigns(user.id),
			]).then(([rs, assigns]) => {
				const assignMap = new Map(assigns.map(a => [a.roleId, a]));
				return rs
					.filter((r) => r.isPublic || iAmModerator)
					.filter((r) => (isMe || iAmModerator) || assignMap.get(r.id)?.isHideProfile !== true)
					.sort((a, b) => b.displayOrder - a.displayOrder)
					.map((r) => ({
						name: r.name,
						iconUrl: r.iconUrl,
						displayOrder: r.displayOrder,
					}));
			}) : undefined,
```

Replace the existing `roles` block (inside `...(isDetailed ? {...})`) so it attaches the hide state and excludes hidden assignments for non-self/non-moderator (NO `experience` computation):

```ts
				roles: Promise.all([
					this.roleService.getUserRoles(user.id),
					this.roleService.getUserAssigns(user.id),
				]).then(([roles, assigns]) => {
					const assignMap = new Map(assigns.map(a => [a.roleId, a]));
					return roles
						.filter(role => role.isPublic)
						.sort((a, b) => b.displayOrder - a.displayOrder)
						.filter(role => (isMe || iAmModerator) || assignMap.get(role.id)?.isHideProfile !== true)
						.map(role => {
							const assign = assignMap.get(role.id);
							return {
								id: role.id,
								name: role.name,
								color: role.color,
								iconUrl: role.iconUrl,
								description: role.description,
								isModerator: role.isModerator,
								isAdministrator: role.isAdministrator,
								displayOrder: role.displayOrder,
								canHideProfileByUser: role.canHideProfileByUser,
								isHideProfile: role.canHideProfileByUser ? (assign?.isHideProfile ?? false) : false,
							};
						});
				}),
```

- [ ] **Step 8: Run backend typecheck and eslint**

Run: `pnpm --filter backend typecheck`
Expected: PASS.

Run: `pnpm --filter backend eslint`
Expected: PASS.

- [ ] **Step 9: Commit (conditional)**

```bash
git add packages/backend/src/server/api/endpoints/admin/roles/create.ts packages/backend/src/server/api/endpoints/admin/roles/update.ts packages/backend/src/server/api/endpoints/admin/roles/users.ts packages/backend/src/server/api/endpoints/roles/users.ts packages/backend/src/server/api/endpoints/admin/show-user.ts packages/backend/src/core/RoleService.ts packages/backend/src/core/entities/RoleEntityService.ts packages/backend/src/core/entities/UserEntityService.ts
git commit -m "feat(contract): extend create/update/member/user responses for level roles"
```

---

### Task 6: Regenerate misskey-js autogen and verify the contract

**Files:**
- Regenerate (committed artifacts): `packages/misskey-js/src/autogen/*` (`types.ts`, `entities.ts`, `endpoint.ts`, `models.ts`, `apiClientJSDoc.ts`), `packages/misskey-js/built/*`
- Test: `packages/frontend/test/unit/level-role-contract.test.ts`

**Interfaces:**
- Consumes: all contract-source changes from Tasks 1, 3, 4, 5.
- Produces: the generated `Misskey.entities.Role`, `Misskey.entities.UserDetailed`, `Misskey.api.AdminRolesChangeExpRequest`/`AdminRolesChangeExpResponse`, `Misskey.api.RolesProfileHideRequest`/`RolesProfileHideResponse`, and the `Endpoints` map entries `'admin/roles/change-exp'` and `'roles/profile-hide'`.

- [ ] **Step 1: Regenerate the misskey-js types**

Run: `pnpm build-misskey-js-with-types`
Expected: succeeds. `packages/misskey-js/src/autogen/types.ts` now contains `target: 'manual' | 'conditional' | 'manualLevel'`, `levelPolicies`, `canHideProfileByUser`, `experience` (with `nextLevelExp` nullable), and the operations `admin___roles___change-exp` / `roles___profile-hide`; `packages/misskey-js/src/autogen/endpoint.ts` maps both new endpoints.

- [ ] **Step 2: Verify the generated contract is exact (no hand-editing of autogen)**

Run: `(Select-String -Path packages\misskey-js\src\autogen\types.ts -Pattern "'manualLevel'").Count`
Expected: at least `3` (Role schema `target`, admin create request `target`, admin update request `target`).

Run: `Select-String -Path packages\misskey-js\src\autogen\types.ts -Pattern "nextLevelExp"`
Expected: matches where `nextLevelExp` is `number | null` (or `['integer', 'null']`), including the `experience` objects.

Run: `Select-String -Path packages\misskey-js\src\autogen\types.ts -Pattern "admin___roles___change-exp|roles___profile-hide"`
Expected: 2 or more matches.

- [ ] **Step 3: Run the contract-assertion tests**

Run: `pnpm --filter frontend typecheck`
Expected: PASS (the `level-role-contract.test.ts` assertions now compile).

Run: `pnpm --filter frontend test -- level-role-contract`
Expected: PASS.

- [ ] **Step 4: Commit (conditional)**

```bash
git add packages/misskey-js/src/autogen packages/misskey-js/built packages/frontend/test/unit/level-role-contract.test.ts
git commit -m "feat(contract): regenerate misskey-js autogen for level-role endpoints"
```

---

### Task 7: ja-JP.yml locale keys and i18n regeneration (ALL keys live here)

**Files:**
- Modify (only hand-edited locale): `locales/ja-JP.yml`
- Regenerate (committed artifacts): `packages/i18n/src/autogen/locale.ts`, `packages/i18n/built/*`

**Interfaces:**
- Produces the COMPLETE key set consumed by Plans 2/3 (Plans 2/3 add NO locale edits, only a failing gate if a key is absent):
  - `_role`: `manualLevel`, `manualLevelRoles`, `canHideProfileByUser`, `descriptionOfcanHideProfileByUser`, `levelPolicies`, `countOfCondLevelPolicies`, updated `descriptionOfAssignTarget`.
  - Top-level: `manualLevel`, `experience`, `changeExperienceRole`, `_experience` (`levelShort`, `baseLevel`, `maxLevel`, `settingValue`, `changeExpConfirm`, `_rules.{base,const,linear,exponential,multiplier}`, `_values.{base,additional,exponential}`, `_calcs.{additional,multiplier,set}`).
  - `_moderationLogTypes.changeExperienceRole`.
  - Settings: `descriptionRolesAssignedToMeOfSetting`, `roleHideProfileTip`, `roleShowProfileTip`.

- [ ] **Step 1: Add the `_role` keys**

In `locales/ja-JP.yml`, inside the `_role:` block, after the `conditionalRoles` line, insert:

```yaml
  manualLevel: "マニュアルレベル"
  manualLevelRoles: "レベルロール"
```

and after the `descriptionOfCanEditMembersByModerator` line (before `priority`), insert:

```yaml
  canHideProfileByUser: "ユーザーによるプロフィールからの非表示を許可"
  descriptionOfcanHideProfileByUser: "オンにすると、ユーザーはロールをプロフィールから非表示にできます。オフにすると、ロールのプロフィールは常に表示されます。これは公開ロールのみに適用されます。"
  levelPolicies: "レベルアップポリシー"
  countOfCondLevelPolicies: "{value} 件のレベルポリシー"
```

Update `descriptionOfAssignTarget` to mention manualLevel:

```yaml
  descriptionOfAssignTarget: "<b>マニュアル</b>は誰がこのロールに含まれるかを手動で管理します。\n<b>コンディショナル</b>は条件を設定し、それに合致するユーザーが自動で含まれるようになります。\n<b>マニュアルレベル</b>はユーザーの評価値を手動で設定し、評価値を元に異なるポリシーを管理します。"
```

- [ ] **Step 2: Add the top-level level-role keys (including `changeExpConfirm`)**

Add a new top-level block near the existing `rolesAssignedToMe` key:

```yaml
manualLevel: "マニュアルレベル"
experience: "経験値"
_experience:
  levelShort: "Lv.{value}"
  baseLevel: "ベースレベル"
  maxLevel: "最大レベル"
  settingValue: "設定値"
  changeExpConfirm: "ロールの経験値を変更します。\n方式: {mode}\n値: {value}\nノート: {note}"
  _rules:
    base: "ベース"
    const: "定数"
    linear: "線形"
    exponential: "指数"
    multiplier: "乗数"
  _values:
    base: "ベース値"
    additional: "加算値"
    exponential: "指数"
  _calcs:
    additional: "加減算"
    multiplier: "乗算"
    set: "固定値"
```

- [ ] **Step 3: Add the settings and moderation-log keys**

In the `_moderationLogTypes:` block, after the last entry, add:

```yaml
  changeExperienceRole: "ロールの経験値を変更"
```

Add the settings keys near `rolesAssignedToMe`:

```yaml
descriptionRolesAssignedToMeOfSetting: "ここは自分に割り振られたロールです。プロフィールに表示するロールをカスタマイズすることができます。"
roleHideProfileTip: "プロフィールにロールを表示しない"
roleShowProfileTip: "プロフィールにロールを表示する"
```

- [ ] **Step 4: Regenerate the i18n autogen and build**

Run: `pnpm --filter i18n generate && pnpm --filter i18n build`
Expected: `packages/i18n/src/autogen/locale.ts` and `packages/i18n/built/` updated; `_experience`, `manualLevel`, `changeExperienceRole`, `changeExpConfirm` appear in the generated `Locale` type.

Run: `pnpm --filter i18n verify`
Expected: PASS.

- [ ] **Step 5: Verify locale safety (only ja-JP.yml hand-edited)**

Run: `git diff --name-only develop -- 'locales/*.yml' | Select-String -NotMatch '^locales/ja-JP\.yml$'`
Expected: empty (no output).

- [ ] **Step 6: Run frontend typecheck and lint**

Run: `pnpm --filter frontend typecheck`
Expected: PASS.

Run: `pnpm --filter frontend eslint`
Expected: PASS.

- [ ] **Step 7: Commit (conditional)**

```bash
git add locales/ja-JP.yml packages/i18n/src/autogen/locale.ts packages/i18n/built
git commit -m "feat(i18n): add level-role locale keys and regenerate i18n autogen"
```

---

### Task 8: Shared frontend role-level helper, fixtures, and final gates

**Files:**
- Create: `packages/frontend/src/utility/role-level.ts`
- Create: `packages/frontend/test/unit/lib/level-role.fixtures.ts`
- Create: `packages/frontend/test/unit/role-level.test.ts`
- Test: `packages/frontend/test/unit/level-role-contract.test.ts` (final gate)

**Interfaces:**
- Consumes: generated types (Task 6), i18n keys (Task 7).
- Produces (consumed by Plans 2/3):
  - `export type RoleExperience = NonNullable<Misskey.entities.Role['experience']>`
  - `export function isManualLevelRole(role: Pick<Misskey.entities.Role, 'target'>): boolean`
  - `export function isRoleProfileHidden(role: { isHideProfile?: boolean }): boolean`
  - `export function filterVisibleRoles<T extends { isHideProfile?: boolean }>(roles: T[]): T[]` (defense-in-depth: drops `isHideProfile === true`; the ONLY place the frontend can filter hidden roles is where `isHideProfile` exists — profile/self roles)
  - `export function formatLevel(level: number): string` (`Lv.{value}` via `i18n.tsx._experience.levelShort`)
  - `export function levelProgressPercent(exp: RoleExperience): number` (0-100, 100 at max level)
  - Fixtures: `manualLevelRoleFixture`, `experienceFixture`, `maxLevelExperienceFixture`, `levelPoliciesFixture`, `hiddenRoleFixture`.

- [ ] **Step 1: Write the failing unit tests**

Create `packages/frontend/test/unit/role-level.test.ts`:

```ts
/*
 * SPDX-FileCopyrightText: syuilo and misskey-project
 * SPDX-License-Identifier: AGPL-3.0-only
 */

import { describe, expect, it } from 'vitest';
import { filterVisibleRoles, isManualLevelRole, isRoleProfileHidden, levelProgressPercent } from '@/utility/role-level.js';
import { experienceFixture, hiddenRoleFixture, manualLevelRoleFixture, maxLevelExperienceFixture } from './lib/level-role.fixtures.js';

describe('role-level utility', () => {
	it('isManualLevelRole detects manualLevel target', () => {
		expect(isManualLevelRole(manualLevelRoleFixture)).toBe(true);
		expect(isManualLevelRole({ target: 'manual' })).toBe(false);
	});

	it('isRoleProfileHidden checks isHideProfile', () => {
		expect(isRoleProfileHidden(hiddenRoleFixture)).toBe(true);
		expect(isRoleProfileHidden({ isHideProfile: false })).toBe(false);
	});

	it('filterVisibleRoles drops hidden roles (defense-in-depth)', () => {
		const visible = filterVisibleRoles([manualLevelRoleFixture, hiddenRoleFixture]);
		expect(visible).toHaveLength(1);
		expect(visible[0].id).toBe(manualLevelRoleFixture.id);
	});

	it('levelProgressPercent is 100 at max level', () => {
		expect(levelProgressPercent(maxLevelExperienceFixture)).toBe(100);
	});

	it('levelProgressPercent is proportional otherwise', () => {
		expect(levelProgressPercent(experienceFixture)).toBeGreaterThan(0);
		expect(levelProgressPercent(experienceFixture)).toBeLessThanOrEqual(100);
	});
});
```

- [ ] **Step 2: Run to verify it fails**

Run: `pnpm --filter frontend test -- role-level`
Expected: FAIL — `Cannot find module '@/utility/role-level.js'` / `Cannot find module './lib/level-role.fixtures.js'`.

- [ ] **Step 3: Create the type fixtures (synthetic values only)**

Create `packages/frontend/test/unit/lib/level-role.fixtures.ts`:

```ts
/*
 * SPDX-FileCopyrightText: syuilo and misskey-project
 * SPDX-License-Identifier: AGPL-3.0-only
 */

import type * as Misskey from 'misskey-js';

export const levelPoliciesFixture: NonNullable<Misskey.entities.Role['levelPolicies']> = {
	baseLevel: 1,
	experiencePolicies: [
		{ level: 3, type: 'const', base: 100, additional: 0 },
		{ level: 5, type: 'linear', base: 200, additional: 50 },
	],
};

export const experienceFixture: NonNullable<Misskey.entities.Role['experience']> = {
	minLevel: 1,
	maxLevel: 9,
	currentLevel: 3,
	currentExp: 40,
	nextLevelExp: 100,
	totalExp: 340,
};

export const maxLevelExperienceFixture: NonNullable<Misskey.entities.Role['experience']> = {
	minLevel: 1,
	maxLevel: 9,
	currentLevel: 9,
	currentExp: 120,
	nextLevelExp: null,
	totalExp: 5120,
};

export const manualLevelRoleFixture: Misskey.entities.Role = {
	id: 'role-manual-level-000000000001',
	createdAt: '2026-01-01T00:00:00.000Z',
	updatedAt: '2026-01-01T00:00:00.000Z',
	name: 'Leveled',
	description: 'synthetic fixture role',
	color: '#00ff00',
	iconUrl: null,
	target: 'manualLevel',
	condFormula: { id: 'c1', type: 'isRemote' },
	isPublic: true,
	isExplorable: true,
	asBadge: true,
	preserveAssignmentOnMoveAccount: false,
	canEditMembersByModerator: true,
	isModerator: false,
	isAdministrator: false,
	displayOrder: 0,
	canHideProfileByUser: true,
	levelPolicies: levelPoliciesFixture,
	experience: experienceFixture,
	policies: {},
	usersCount: 1,
};

export const hiddenRoleFixture: Pick<Misskey.entities.Role, 'id' | 'target'> & { isHideProfile: boolean } = {
	id: 'role-hidden-000000000001',
	target: 'manualLevel',
	isHideProfile: true,
};
```

Note: the fixture compiles once Task 6 regenerated the types. Keep only synthetic values.

- [ ] **Step 4: Implement `packages/frontend/src/utility/role-level.ts`**

```ts
/*
 * SPDX-FileCopyrightText: syuilo and misskey-project
 * SPDX-License-Identifier: AGPL-3.0-only
 */

import type * as Misskey from 'misskey-js';
import { i18n } from '@/i18n.js';

export type RoleExperience = NonNullable<Misskey.entities.Role['experience']>;

export function isManualLevelRole(role: Pick<Misskey.entities.Role, 'target'>): boolean {
	return role.target === 'manualLevel';
}

export function isRoleProfileHidden(role: { isHideProfile?: boolean }): boolean {
	return role.isHideProfile === true;
}

// バックエンドがフィルタ済みでも、フロントエンド側でも隠しロールを表示しないための防御的フィルタ。
// 適用できるのはisHideProfileが存在する場面(プロフィール/自分のroles)のみ。
export function filterVisibleRoles<T extends { isHideProfile?: boolean }>(roles: T[]): T[] {
	return roles.filter(role => !isRoleProfileHidden(role));
}

export function formatLevel(level: number): string {
	return i18n.tsx._experience.levelShort({ value: level });
}

export function levelProgressPercent(exp: RoleExperience): number {
	if (exp.nextLevelExp == null) return 100;
	if (exp.nextLevelExp <= 0) return 100;
	return Math.min(Math.max(Math.floor((exp.currentExp / exp.nextLevelExp) * 100), 0), 100);
}
```

- [ ] **Step 5: Run the unit tests and typecheck**

Run: `pnpm --filter frontend test`
Expected: PASS (`role-level.test.ts`, `level-role-contract.test.ts`, and the existing suite).

Run: `pnpm --filter frontend typecheck`
Expected: PASS.

- [ ] **Step 6: Run the full gates**

Run: `pnpm --filter frontend lint`
Expected: PASS.

Run: `pnpm --filter backend lint`
Expected: PASS.

Run: `pnpm lint`
Expected: PASS (recursive lint + `check-dts`).

Run: `pnpm --filter backend check-migrations`
Expected: PASS.

- [ ] **Step 7: Commit (conditional)**

```bash
git add packages/frontend/src/utility/role-level.ts packages/frontend/test/unit/role-level.test.ts packages/frontend/test/unit/lib/level-role.fixtures.ts
git commit -m "feat(role): add shared role-level utility, fixtures, and unit tests"
```

---

## Final integration dependency (this plan)

Frontend PR 1 is the contract prerequisite for Frontend PR 2 (`2026-08-16-level-role-frontend-admin-editor.md`) and Frontend PR 3 (`2026-08-16-level-role-frontend-user-ui.md`). Those plans consume the generated types, the complete i18n key set, and `@/utility/role-level.js`. mk-go (`Misaki-Project/mk`) implements the same contract in `internal/core/role`; the full imported-DB Docker E2E (including level-calc value assertions) is Backend PR 3's scope and runs mk-go + this fork's frontend SHA. Do not merge this PR until `pnpm lint`, `pnpm --filter frontend test`, and `pnpm --filter backend check-migrations` are green.
