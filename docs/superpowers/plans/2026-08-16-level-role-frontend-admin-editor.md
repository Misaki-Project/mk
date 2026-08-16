# Level Role Frontend Admin Editor Implementation Plan (Frontend PR 2)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the level-role admin editor UI: an experience-policy editor, a per-policy level-condition editor, and manualLevel create/update/member support, implemented by **extending the existing `roles.policy-editor.vue` and `roles.policy-editor.folder.vue`** (no duplicate policy-list component), while preserving normal manual/conditional role behavior.

**Architecture:** Two new editor components (`RolesEditorLevel.vue`, `RolesEditorLevelCond.vue`) provide the level-policy and per-policy level-condition editing controls. The existing `roles.policy-editor.folder.vue` gains a `levelMode` prop (hides the "use base value" switch in level mode) and the existing `roles.policy-editor.vue` gains a `isLevelRole` branch that reuses the same folder component to render one `RolesEditorLevelCond` per policy, bound to `policyAsLevel` and emitting `update:policyAsLevels`. `roles.editor.vue` switches the existing `XPolicyEditor` into level mode for `manualLevel` targets and merges `policyAsLevels` into the save payload. Drag-and-drop uses the project's `MkDraggable` (a `manualDragStart` + drag-handle pattern, matching `RolesEditorFormula.vue`); `vuedraggable` is never used. Pure payload mapping lives in `role-level-editor.util.ts` and is unit-tested without mounting components. The CherryPick 1833-line editor is NOT ported.

**Tech Stack:** Vue 3.5 (`<script setup>`), `MkDraggable`, `MkInput`/`MkSelect`/`MkSwitch`/`MkFolder`/`MkContainer`, `misskey-js` generated types (from Frontend PR 1), `@/i18n.js`, vitest 4.1 + happy-dom, vue-tsc 3.3.

## Global Constraints

All file paths are relative to the fork repo root `Misaki-Project/misskey-ts` (based on `ff25eac144c64d3ca1a06862547f7b101d315f98`). Frontend PR 1 (`2026-08-16-level-role-frontend-contract-i18n.md`) MUST be merged first — this plan consumes its generated types, its complete i18n key set, and `@/utility/role-level.js`.

- **Do NOT port the CherryPick 1833-line `roles.editor.vue`.** Extend the existing 2026.7 components in place.
- **Do NOT create a duplicate policy-list component.** Per-policy level conditions must be integrated into `roles.policy-editor.vue` / `roles.policy-editor.folder.vue`.
- **Use `MkDraggable`, never `vuedraggable`.** `MkDraggable` API: props `modelValue`, `direction`, `group`, `manualDragStart`, `withGaps`, `canNest`; default slot exposes `{ item, index, dragStart }`; no `disabled`/`readonly` prop. Use `manualDragStart` + a drag-handle button bound via `@dragstart.stop="dragStart($event)"` (readonly hides the handle). Strip the `id` before emitting payloads.
- **Form control props (verified):** `MkInput` and `MkSelect` accept `readonly` and `disabled`; `MkSwitch` and `MkRange` accept `disabled` ONLY (a `readonly` prop on `MkSwitch` is a silent no-op). Use `:readonly` on MkInput/MkSelect and `:disabled="readonly"` on MkSwitch.
- **Policy value types:** level conditions apply to `boolean` (switch) and `number` (number input) constants; `string`/array policies offer only the `base` mode (use instance default) — **never render a string value as a switch**.
- **Contract (from PR 1):** `Role.target` includes `'manualLevel'`; `Role.levelPolicies`; `Role.canHideProfileByUser`; `Role.policies[key].policyAsLevel: ({ type: 'const'|'multiplier'; base: number|boolean; additional: number } | { type: 'base' }) & { level: number }[] | null`; `Role.experience.nextLevelExp` is `null` at max level. Endpoints `admin/roles/create`/`update` accept `manualLevel`, `canHideProfileByUser`, `levelPolicies`; `admin/roles/users` includes assignment experience while preserving its existing order.
- **Locale rule:** this plan makes NO locale YAML edits. The required keys were added in PR 1; the final gate verifies they exist and FAILS if absent.
- **SPDX headers** on every new `.vue`/`.ts` file.
- **Non-regression:** manual/conditional role editing must behave exactly as before (the `isLevelRole` branch is `v-else` and does not touch the existing 40 policy blocks).
- **No auto-commit:** `git commit` steps are conditional — run them ONLY with explicit authorization. Never push/merge/PR automatically.
- Do not modify anything outside the files listed in this plan.

---

### Task 1: Pure level-editor helper module and payload tests

**Files:**
- Create: `packages/frontend/src/pages/admin/role-level-editor.util.ts`
- Test: `packages/frontend/test/unit/role-level-editor.test.ts`

**Interfaces:**
- Produces (used by Tasks 2-6):
  - `export type EditableExperiencePolicy = { id: string; level: number; type: 'const' | 'linear' | 'exponential'; base: number; additional: number; exponential: number }`
  - `export type EditableLevelCond = { id: string; level: number; type: 'base' | 'const' | 'multiplier'; base: number | boolean; additional: number }`
  - `export type PolicyAsLevelMap = Record<string, NonNullable<Misskey.entities.Role['policies'][string]>['policyAsLevel']>`
  - `export function defaultLevelPolicies(): RoleLevelPolicies`
  - `export function calcMaxLevel(policies: RoleLevelPolicies): number`
  - `export function totalLevelAt(index: number, policies: RoleLevelPolicies): number`
  - `export function toExperiencePoliciesPayload(items: EditableExperiencePolicy[]): RoleExperienceLevelPolicyValue[]`
  - `export function toPolicyAsLevelPayload(items: EditableLevelCond[]): RoleExperiencePolicyCulcValue[]`
  - `export function shouldShowLevelPolicies(target: Misskey.entities.Role['target']): boolean`
  - `export function isPolicyLevelDefault(items: EditableLevelCond[]): boolean`

- [ ] **Step 1: Write the failing payload tests**

Create `packages/frontend/test/unit/role-level-editor.test.ts`:

```ts
/*
 * SPDX-FileCopyrightText: syuilo and misskey-project
 * SPDX-License-Identifier: AGPL-3.0-only
 */

import { describe, expect, it } from 'vitest';
import {
	calcMaxLevel,
	defaultLevelPolicies,
	isPolicyLevelDefault,
	shouldShowLevelPolicies,
	toExperiencePoliciesPayload,
	toPolicyAsLevelPayload,
	totalLevelAt,
} from '@/pages/admin/role-level-editor.util.js';

describe('role-level-editor util', () => {
	it('defaultLevelPolicies returns a base-level 0 role with one const policy', () => {
		const p = defaultLevelPolicies();
		expect(p.baseLevel).toBe(0);
		expect(p.experiencePolicies).toHaveLength(1);
		expect(p.experiencePolicies[0].type).toBe('const');
	});

	it('calcMaxLevel sums baseLevel and policy levels', () => {
		const p = { baseLevel: 1, experiencePolicies: [{ level: 3, type: 'const' as const, base: 100 }, { level: 5, type: 'linear' as const, base: 200, additional: 50 }] };
		expect(calcMaxLevel(p)).toBe(9);
	});

	it('totalLevelAt accumulates previous policy levels', () => {
		const p = { baseLevel: 1, experiencePolicies: [{ level: 3, type: 'const' as const, base: 100 }, { level: 5, type: 'linear' as const, base: 200, additional: 50 }] };
		expect(totalLevelAt(0, p)).toBe(1);
		expect(totalLevelAt(1, p)).toBe(4);
	});

	it('toExperiencePoliciesPayload strips id and omits unused coefficients', () => {
		const items = [
			{ id: 'a', level: 10, type: 'const' as const, base: 100, additional: 50, exponential: 1 },
			{ id: 'b', level: 5, type: 'exponential' as const, base: 100, additional: 50, exponential: 1.2 },
		];
		const payload = toExperiencePoliciesPayload(items);
		expect(payload[0]).toEqual({ level: 10, type: 'const', base: 100 });
		expect(payload[1]).toEqual({ level: 5, type: 'exponential', base: 100, additional: 50, exponential: 1.2 });
		expect('id' in payload[0]).toBe(false);
	});

	it('toPolicyAsLevelPayload maps base/const/multiplier items', () => {
		const items = [
			{ id: 'a', level: 3, type: 'base' as const, base: false, additional: 0 },
			{ id: 'b', level: 5, type: 'const' as const, base: 100, additional: 0 },
		];
		const payload = toPolicyAsLevelPayload(items);
		expect(payload[0]).toEqual({ level: 3, type: 'base' });
		expect(payload[1]).toEqual({ level: 5, type: 'const', base: 100, additional: 0 });
	});

	it('shouldShowLevelPolicies is true only for manualLevel', () => {
		expect(shouldShowLevelPolicies('manualLevel')).toBe(true);
		expect(shouldShowLevelPolicies('manual')).toBe(false);
		expect(shouldShowLevelPolicies('conditional')).toBe(false);
	});

	it('isPolicyLevelDefault is true only for a single base item', () => {
		expect(isPolicyLevelDefault([])).toBe(false);
		expect(isPolicyLevelDefault([{ id: 'a', level: 1, type: 'base' as const, base: 0, additional: 0 }])).toBe(true);
		expect(isPolicyLevelDefault([{ id: 'a', level: 1, type: 'const' as const, base: 1, additional: 0 }])).toBe(false);
	});
});
```

- [ ] **Step 2: Run to verify it fails**

Run: `pnpm --filter frontend test -- role-level-editor`
Expected: FAIL — `Cannot find module '@/pages/admin/role-level-editor.util.js'`.

- [ ] **Step 3: Implement `packages/frontend/src/pages/admin/role-level-editor.util.ts`**

```ts
/*
 * SPDX-FileCopyrightText: syuilo and misskey-project
 * SPDX-License-Identifier: AGPL-3.0-only
 */

import type * as Misskey from 'misskey-js';

type RoleLevelPolicies = NonNullable<Misskey.entities.Role['levelPolicies']>;
type RoleExperienceLevelPolicyValue = RoleLevelPolicies['experiencePolicies'][number];

export type EditableExperiencePolicy = {
	id: string;
	level: number;
	type: 'const' | 'linear' | 'exponential';
	base: number;
	additional: number;
	exponential: number;
};

export type EditableLevelCond = {
	id: string;
	level: number;
	type: 'base' | 'const' | 'multiplier';
	base: number | boolean;
	additional: number;
};

export type PolicyAsLevelMap = Record<string, NonNullable<Misskey.entities.Role['policies'][string]>['policyAsLevel']>;

export function defaultLevelPolicies(): RoleLevelPolicies {
	return {
		baseLevel: 0,
		experiencePolicies: [{ level: 10, type: 'const', base: 100, additional: 50 }],
	};
}

export function calcMaxLevel(policies: RoleLevelPolicies): number {
	return policies.baseLevel + policies.experiencePolicies.reduce((acc, p) => acc + p.level, 0);
}

export function totalLevelAt(index: number, policies: RoleLevelPolicies): number {
	if (index < 0) return policies.baseLevel;
	return policies.baseLevel + policies.experiencePolicies.slice(0, index).reduce((acc, p) => acc + p.level, 0);
}

// MkDraggable用のidを除去し、未使用の係数を省略したpayloadを返す
export function toExperiencePoliciesPayload(items: EditableExperiencePolicy[]): RoleExperienceLevelPolicyValue[] {
	return items.map(({ id, additional, exponential, ...rest }) => ({
		level: rest.level,
		type: rest.type,
		base: rest.base,
		...(rest.type !== 'const' ? { additional } : {}),
		...(rest.type === 'exponential' ? { exponential } : {}),
	}));
}

// policyAsLevel payload: baseはデフォルト値を使うためbase/additionalを持たない
export function toPolicyAsLevelPayload(items: EditableLevelCond[]): Misskey.entities.Role['policies'][string]['policyAsLevel'] {
	return items.map(({ id, additional, ...rest }) => rest.type === 'base'
		? { level: rest.level, type: rest.type as 'base' }
		: { level: rest.level, type: rest.type as 'const' | 'multiplier', base: rest.base, additional });
}

export function shouldShowLevelPolicies(target: Misskey.entities.Role['target']): boolean {
	return target === 'manualLevel';
}

export function isPolicyLevelDefault(items: EditableLevelCond[]): boolean {
	return items.length === 1 && items[0].type === 'base';
}
```

- [ ] **Step 4: Run tests and typecheck**

Run: `pnpm --filter frontend test -- role-level-editor`
Expected: PASS.

Run: `pnpm --filter frontend typecheck`
Expected: PASS.

- [ ] **Step 5: Commit (conditional)**

```bash
git add packages/frontend/src/pages/admin/role-level-editor.util.ts packages/frontend/test/unit/role-level-editor.test.ts
git commit -m "feat(role): add level-editor payload util and unit tests"
```

---

### Task 2: `RolesEditorLevel.vue` — sortable experience-policy editor

**Files:**
- Create: `packages/frontend/src/pages/admin/RolesEditorLevel.vue`

**Interfaces:**
- Consumes: `EditableExperiencePolicy`, `calcMaxLevel`, `totalLevelAt`, `defaultLevelPolicies`, `toExperiencePoliciesPayload` (Task 1), `formatLevel` (PR 1), i18n `_experience.*`, `addItem`.
- Produces:
  - Props: `modelValue: NonNullable<Misskey.entities.Role['levelPolicies']> | null | undefined`, `readonly?: boolean`
  - Emits: `(ev: 'update:modelValue', v: NonNullable<Misskey.entities.Role['levelPolicies']>)`
  - Behavior: reorders `experiencePolicies` via `MkDraggable` (drag handle only, hidden in readonly); add/remove rows; emits payload WITHOUT `id` fields (via `toExperiencePoliciesPayload`).

- [ ] **Step 1: Create the component**

```vue
<!--
SPDX-FileCopyrightText: syuilo and misskey-project
SPDX-License-Identifier: AGPL-3.0-only
-->

<template>
<div class="_gaps">
	<div :class="$style.baseRow">
		<MkInput v-model="baseLevel" type="number" :min="0" :readonly="readonly">
			<template #label>{{ i18n.ts._experience.baseLevel }}</template>
		</MkInput>
		<MkInput :modelValue="maxLevel" type="number" readonly>
			<template #label>{{ i18n.ts._experience.maxLevel }}</template>
		</MkInput>
	</div>

	<MkContainer :showHeader="false">
		<MkDraggable v-model="items" direction="vertical" withGaps manualDragStart>
			<template #default="{ item, index, dragStart }">
				<div :class="$style.item">
					<div :class="$style.itemMain">
						<MkInput v-model="item.level" type="number" :min="1" :readonly="readonly">
							<template #prefix>{{ formatLevel(totalLevelAt(index, currentPayload())) }} <i class="ti ti-arrow-right"></i> +</template>
							<template #suffix><i class="ti ti-arrow-right"></i> {{ formatLevel(totalLevelAt(index, currentPayload()) + item.level - 1) }}</template>
						</MkInput>
						<MkSelect v-model="item.type" :items="ruleDef" :readonly="readonly"></MkSelect>
						<button v-if="!readonly" class="_button" :class="$style.handle" :draggable="true" @dragstart.stop="dragStart($event)"><i class="ti ti-menu-2"></i></button>
						<button v-if="items.length > 1 && !readonly" class="_button" :class="$style.remove" @click="remove(index)"><i class="ti ti-x"></i></button>
					</div>
					<div :class="$style.itemSub">
						<MkInput v-model="item.base" type="number" :readonly="readonly">
							<template #label>{{ i18n.ts._experience._values.base }}</template>
						</MkInput>
						<template v-if="item.type !== 'const'">
							<span> + </span>
							<MkInput v-model="item.additional" type="number" :readonly="readonly">
								<template #label>{{ i18n.ts._experience._values.additional }}</template>
							</MkInput>
						</template>
						<template v-if="item.type === 'exponential'">
							<span> ^ </span>
							<MkInput v-model="item.exponential" type="number" step="0.001" :readonly="readonly">
								<template #label>{{ i18n.ts._experience._values.exponential }}</template>
							</MkInput>
						</template>
					</div>
				</div>
			</template>
		</MkDraggable>
	</MkContainer>

	<div class="_buttons">
		<MkButton v-if="!readonly" rounded style="margin: 0 auto;" @click="add"><i class="ti ti-plus"></i> {{ i18n.ts.addItem }}</MkButton>
	</div>
</div>
</template>

<script lang="ts" setup>
import { computed, ref, watch } from 'vue';
import type * as Misskey from 'misskey-js';
import MkDraggable from '@/components/MkDraggable.vue';
import MkInput from '@/components/MkInput.vue';
import MkSelect from '@/components/MkSelect.vue';
import MkButton from '@/components/MkButton.vue';
import MkContainer from '@/components/MkContainer.vue';
import { i18n } from '@/i18n.js';
import { genId } from '@/utility/id.js';
import { formatLevel } from '@/utility/role-level.js';
import {
	calcMaxLevel,
	defaultLevelPolicies,
	toExperiencePoliciesPayload,
	totalLevelAt,
	type EditableExperiencePolicy,
} from './role-level-editor.util.js';

const props = withDefaults(defineProps<{
	modelValue: NonNullable<Misskey.entities.Role['levelPolicies']> | null | undefined;
	readonly?: boolean;
}>(), {
	readonly: false,
});

const emit = defineEmits<{
	(ev: 'update:modelValue', v: NonNullable<Misskey.entities.Role['levelPolicies']>): void;
}>();

const ruleDef = [
	{ label: i18n.ts._experience._rules.const, value: 'const' },
	{ label: i18n.ts._experience._rules.linear, value: 'linear' },
	{ label: i18n.ts._experience._rules.exponential, value: 'exponential' },
] as const;

const baseLevel = ref(0);
const items = ref<EditableExperiencePolicy[]>([]);

function currentPayload() {
	return {
		baseLevel: baseLevel.value,
		experiencePolicies: toExperiencePoliciesPayload(items.value),
	};
}

function syncFromProps() {
	const p = props.modelValue ?? defaultLevelPolicies();
	baseLevel.value = p.baseLevel;
	// 既存DB行のlevelPoliciesは'{}' (experiencePolicies無し)の可能性があるためArray.isArrayで防御する
	items.value = Array.isArray(p.experiencePolicies)
		? p.experiencePolicies.map(ep => ({
			id: genId(),
			level: ep.level,
			type: ep.type,
			base: ep.base,
			additional: ep.additional ?? 0,
			exponential: ep.exponential ?? 1,
		}))
		: [];
}

// 親からの入力が現在の編集内容と同一なら再同期しない(emitループ防止、RolesEditorFormulaと同パターン)
watch(() => props.modelValue, () => {
	if (props.modelValue == null) return;
	if (JSON.stringify(props.modelValue) === JSON.stringify(currentPayload())) return;
	syncFromProps();
}, { deep: true, immediate: true });

const maxLevel = computed(() => calcMaxLevel(currentPayload()));

function emitPayload() {
	emit('update:modelValue', currentPayload());
}

watch([baseLevel, items], emitPayload, { deep: true });

function add() {
	items.value.push({ id: genId(), level: 10, type: 'const', base: 100, additional: 50, exponential: 1 });
}

function remove(idx: number) {
	items.value.splice(idx, 1);
}
</script>

<style lang="scss" module>
.baseRow {
	display: grid;
	grid-template-columns: repeat(2, 1fr);
	gap: 8px;
	width: 100%;
}

.item {
	border: solid 2px var(--MI_THEME-divider);
	border-radius: var(--MI-radius);
	padding: 12px;
}

.itemMain,
.itemSub {
	display: flex;
	align-items: center;
	gap: 8px;
}

.handle {
	cursor: move;
}

.remove {
	margin-left: auto;
}
</style>
```

- [ ] **Step 2: Run typecheck and unit tests**

Run: `pnpm --filter frontend typecheck`
Expected: PASS.

Run: `pnpm --filter frontend test -- role-level-editor`
Expected: PASS.

- [ ] **Step 3: Commit (conditional)**

```bash
git add packages/frontend/src/pages/admin/RolesEditorLevel.vue
git commit -m "feat(role): add sortable level-policy editor (RolesEditorLevel)"
```

---

### Task 3: `RolesEditorLevelCond.vue` — per-policy level-condition editor

**Files:**
- Create: `packages/frontend/src/pages/admin/RolesEditorLevelCond.vue`

**Interfaces:**
- Consumes: `EditableLevelCond` (Task 1), `formatLevel` (PR 1), i18n `_experience._rules.{base,const,multiplier}`, `_experience._values.{base,additional}`, `_role.useBaseValue`, `_experience.maxLevel`, `addItem`, `enable`.
- Produces:
  - Props: `modelValue: { type: 'boolean' | 'number' | 'string'; defaultValue: number | boolean | string | string[]; baseLevel: number; CondFormula: EditableLevelCond[] }`, `readonly?: boolean`
  - Emits: `(ev: 'update:modelValue', v: typeof modelValue)`
  - Behavior: sortable condition list via `MkDraggable` (drag-handle pattern); add/remove; the last item has no level input (it extends to max level). Value-type-aware controls: `boolean` -> `MkSwitch`, `number` -> `MkInput`; `string`/array policies offer only the `base` mode (no editable control, no string-as-switch).

- [ ] **Step 1: Create the component**

```vue
<!--
SPDX-FileCopyrightText: syuilo and misskey-project
SPDX-License-Identifier: AGPL-3.0-only
-->

<template>
<div class="_gaps">
	<MkContainer :showHeader="false">
		<MkDraggable v-model="items" direction="vertical" withGaps manualDragStart>
			<template #default="{ item, index, dragStart }">
				<div :class="$style.item">
					<div :class="$style.itemMain">
						<MkInput
							v-if="index !== items.length - 1"
							v-model="item.level"
							type="number"
							:min="1"
							:readonly="readonly"
						>
							<template #prefix>{{ formatLevel(levelAt(index)) }} <i class="ti ti-arrow-right"></i> +</template>
							<template #suffix><i class="ti ti-arrow-right"></i> {{ formatLevel(levelAt(index) + item.level - 1) }}</template>
						</MkInput>
						<MkInput v-else type="text" readonly>
							<template #prefix>{{ formatLevel(levelAt(index)) }} <i class="ti ti-arrow-right"></i></template>
							<template #suffix><i class="ti ti-arrow-right"></i>{{ i18n.ts._experience.maxLevel }}</template>
						</MkInput>
						<MkSelect v-model="item.type" :items="ruleDef" :readonly="readonly"></MkSelect>
						<button v-if="!readonly" class="_button" :class="$style.handle" :draggable="true" @dragstart.stop="dragStart($event)"><i class="ti ti-menu-2"></i></button>
						<button v-if="items.length > 1 && !readonly" class="_button" :class="$style.remove" @click="remove(index)"><i class="ti ti-x"></i></button>
					</div>
					<div :class="$style.itemSub">
						<span v-if="item.type === 'base' || !isEditableScalar">{{ i18n.ts._role.useBaseValue }}</span>
						<template v-else-if="modelValue.type === 'boolean'">
							<MkSwitch v-model="item.base" :disabled="readonly">
								<template #label>{{ i18n.ts.enable }}</template>
							</MkSwitch>
						</template>
						<template v-else-if="modelValue.type === 'number'">
							<MkInput v-model="item.base" type="number" :readonly="readonly">
								<template #label>{{ i18n.ts._experience._values.base }}</template>
							</MkInput>
							<span v-if="item.type === 'multiplier'"> + </span>
							<MkInput v-if="item.type === 'multiplier'" v-model="item.additional" type="number" :readonly="readonly">
								<template #label>{{ i18n.ts._experience._values.additional }}</template>
							</MkInput>
							<span v-if="item.type === 'multiplier'"> * level</span>
						</template>
					</div>
				</div>
			</template>
		</MkDraggable>
	</MkContainer>

	<div class="_buttons">
		<MkButton v-if="!readonly" rounded style="margin: 0 auto;" @click="add"><i class="ti ti-plus"></i> {{ i18n.ts.addItem }}</MkButton>
	</div>
</div>
</template>

<script lang="ts" setup>
import { computed, ref, watch } from 'vue';
import MkDraggable from '@/components/MkDraggable.vue';
import MkInput from '@/components/MkInput.vue';
import MkSelect from '@/components/MkSelect.vue';
import MkButton from '@/components/MkButton.vue';
import MkSwitch from '@/components/MkSwitch.vue';
import MkContainer from '@/components/MkContainer.vue';
import { i18n } from '@/i18n.js';
import { genId } from '@/utility/id.js';
import { formatLevel } from '@/utility/role-level.js';
import type { EditableLevelCond } from './role-level-editor.util.js';

type LevelCondModel = {
	type: 'boolean' | 'number' | 'string';
	defaultValue: number | boolean | string | string[];
	baseLevel: number;
	CondFormula: EditableLevelCond[];
};

const props = withDefaults(defineProps<{
	modelValue: LevelCondModel;
	readonly?: boolean;
}>(), {
	readonly: false,
});

const emit = defineEmits<{
	(ev: 'update:modelValue', v: LevelCondModel): void;
}>();

// 数値/booleanのみconst/multiplierを提供する。string/配列はbaseのみ。
const isEditableScalar = computed(() => props.modelValue.type === 'boolean' || props.modelValue.type === 'number');

const ruleDef = computed(() => {
	const def: Array<{ label: string; value: 'base' | 'const' | 'multiplier' }> = [
		{ label: i18n.ts._experience._rules.base, value: 'base' },
	];
	if (props.modelValue.type === 'boolean' || props.modelValue.type === 'number') {
		def.push({ label: i18n.ts._experience._rules.const, value: 'const' });
	}
	if (props.modelValue.type === 'number') {
		def.push({ label: i18n.ts._experience._rules.multiplier, value: 'multiplier' });
	}
	return def;
});

const items = ref<EditableLevelCond[]>([]);

// 各条件行が開始するlevelを求める
function levelAt(index: number): number {
	return props.modelValue.baseLevel + items.value.slice(0, index).reduce((acc, p) => acc + p.level, 0);
}

function currentPayload() {
	return { ...props.modelValue, CondFormula: items.value };
}

function syncFromProps() {
	items.value = props.modelValue.CondFormula.map(c => ({ ...c, id: c.id || genId() }));
}

// 親からの入力が現在の編集内容と同一なら再同期しない(emitループ防止)
watch(() => props.modelValue, () => {
	if (JSON.stringify(props.modelValue) === JSON.stringify(currentPayload())) return;
	syncFromProps();
}, { deep: true, immediate: true });

function emitPayload() {
	emit('update:modelValue', currentPayload());
}

watch(items, emitPayload, { deep: true });

function add() {
	const defaultValue = props.modelValue.defaultValue;
	items.value.push({
		id: genId(),
		level: 10,
		type: 'base',
		// 配列型(例: uploadableFileTypes)はbaseとして扱えないためfalseにフォールバックする
		base: typeof defaultValue === 'number' || typeof defaultValue === 'boolean' ? defaultValue : false,
		additional: 50,
	});
}

function remove(idx: number) {
	items.value.splice(idx, 1);
}
</script>

<style lang="scss" module>
.item {
	border: solid 2px var(--MI_THEME-divider);
	border-radius: var(--MI-radius);
	padding: 12px;
}

.itemMain,
.itemSub {
	display: flex;
	align-items: center;
	gap: 8px;
}

.handle {
	cursor: move;
}

.remove {
	margin-left: auto;
}
</style>
```

- [ ] **Step 2: Run typecheck**

Run: `pnpm --filter frontend typecheck`
Expected: PASS.

- [ ] **Step 3: Commit (conditional)**

```bash
git add packages/frontend/src/pages/admin/RolesEditorLevelCond.vue
git commit -m "feat(role): add per-policy level-condition editor (RolesEditorLevelCond)"
```

---

### Task 4: Extend `roles.policy-editor.folder.vue` and `roles.policy-editor.vue`

**Files:**
- Modify: `packages/frontend/src/pages/admin/roles.policy-editor.folder.vue`
- Modify: `packages/frontend/src/pages/admin/roles.policy-editor.vue`

**Interfaces:**
- Consumes: `RolesEditorLevelCond` (Task 3), `toPolicyAsLevelPayload`/`isPolicyLevelDefault`/`EditableLevelCond`/`PolicyAsLevelMap` (Task 1), `Misskey.rolePolicies`, `instance.policies`, i18n `_role._options`, `_role.useBaseValue`.
- Produces:
  - `roles.policy-editor.folder.vue` new prop `levelMode?: boolean` (hides the "use base value" `MkSwitch` in level mode; priority stays).
  - `roles.policy-editor.vue` new props `isLevelRole?: boolean`, `baseLevel?: number`, `policyAsLevels?: PolicyAsLevelMap`; new emit `update:policyAsLevels`. In level mode the component renders one `XFolder` (+ `RolesEditorLevelCond`) per policy via the SAME folder component; the existing 40 policy blocks remain untouched and are rendered only in non-level mode.

- [ ] **Step 1: Extend `roles.policy-editor.folder.vue`**

Add the prop and gate the useBaseValue switch:

```ts
const props = defineProps<{
	isBaseRole: boolean;
	policyMeta?: PolicyMeta | null;
	readonly?: boolean;
	levelMode?: boolean;
}>();
```

In the template, change the switch condition:

```vue
			<MkSwitch v-if="!isBaseRole && !levelMode && policyMeta != null" v-model="useDefaultModel" :disabled="readonly">
```

- [ ] **Step 2: Extend `roles.policy-editor.vue` — template level branch**

Wrap the existing list in `<template v-if="!isLevelRole">` and add the level branch. The current root is `<div class="_gaps_s">`; keep it for the non-level path and add a second branch:

```vue
	<template v-if="isLevelRole">
		<div class="_gaps_s">
			<template v-for="policy in rolePolicies" :key="policy">
				<XFolder v-if="matchQuery([policyLabel(policy), policy])" v-model:policyMeta="policyMetaModel[policy]" :is-base-role="false" :readonly="readonly" level-mode>
					<template #label>{{ policyLabel(policy) }}</template>
					<template #valueText>
						<span v-if="isPolicyLevelDefault(levelCondModels[policy]?.CondFormula ?? [])">{{ i18n.ts._role.useBaseValue }}</span>
					</template>
					<template #default>
						<XLevelCond :model-value="levelCondModels[policy]" @update:model-value="updateLevelCond(policy, $event)"/>
					</template>
				</XFolder>
			</template>
		</div>
	</template>
	<div v-else class="_gaps_s">
		<!-- 既存の全XFolderブロック (変更しない) -->
	</div>
```

- [ ] **Step 3: Extend `roles.policy-editor.vue` — script**

Add imports, props, emits, and the level-cond state:

```ts
import { ref, watch, computed } from 'vue';
import { genId } from '@/utility/id.js';
import { instance } from '@/instance.js';
import XLevelCond from './RolesEditorLevelCond.vue';
import { isPolicyLevelDefault, toPolicyAsLevelPayload, type EditableLevelCond, type PolicyAsLevelMap } from './role-level-editor.util.js';

const props = defineProps<{
	isBaseRole: boolean;
	rolePolicies: Misskey.entities.RolePolicies;
	policiesMeta?: PolicyMetaRecord;
	roleQuery?: string;
	readonly?: boolean;
	isLevelRole?: boolean;
	baseLevel?: number;
	policyAsLevels?: PolicyAsLevelMap;
}>();

const emit = defineEmits<{
	(event: 'update:rolePolicies', value: Misskey.entities.RolePolicies): void;
	(event: 'update:policiesMeta', value: PolicyMetaRecord): void;
	(event: 'update:policyAsLevels', value: PolicyAsLevelMap): void;
}>();

const rolePolicies = Misskey.rolePolicies;

type LevelCondModel = {
	type: 'boolean' | 'number' | 'string';
	defaultValue: number | boolean | string | string[];
	baseLevel: number;
	CondFormula: EditableLevelCond[];
};

function buildLevelCondModels(pals: PolicyAsLevelMap): Record<string, LevelCondModel> {
	const result: Record<string, LevelCondModel> = {};
	for (const policy of rolePolicies) {
		const value = props.rolePolicies[policy];
		const def = instance.policies[policy];
		result[policy] = {
			type: typeof value === 'boolean' ? 'boolean' : typeof value === 'number' ? 'number' : 'string',
			defaultValue: def,
			baseLevel: props.baseLevel ?? 0,
			CondFormula: Array.isArray(pals[policy])
				? pals[policy]!.map(p => {
					return {
						id: genId(),
						level: p.level,
						type: p.type,
						base: p.type === 'base' ? (typeof def === 'number' || typeof def === 'boolean' ? def : false) : p.base,
						additional: p.additional ?? 0,
					};
				})
				: [],
		};
	}
	return result;
}

const levelCondModels = ref<Record<string, LevelCondModel>>(buildLevelCondModels(props.policyAsLevels ?? {}));

function currentPolicyAsLevels(): PolicyAsLevelMap {
	const next: PolicyAsLevelMap = {};
	for (const policy of rolePolicies) {
		next[policy] = toPolicyAsLevelPayload(levelCondModels.value[policy]?.CondFormula ?? []);
	}
	return next;
}

// 親からの入力が現在の編集内容と同一なら再同期しない(emitループ防止)
watch(() => props.policyAsLevels, () => {
	if (JSON.stringify(props.policyAsLevels) === JSON.stringify(currentPolicyAsLevels())) return;
	levelCondModels.value = buildLevelCondModels(props.policyAsLevels ?? {});
}, { deep: true });

function updateLevelCond(policy: string, model: LevelCondModel) {
	levelCondModels.value[policy] = model;
	emit('update:policyAsLevels', currentPolicyAsLevels());
}

function policyLabel(policy: string): string {
	return i18n.ts._role._options[policy as keyof typeof i18n.ts._role._options] ?? policy;
}
```

Note: `matchQuery` already exists in this file and reads `props.roleQuery`; `policyMetaModel` is the existing ref. The non-level path (`props.isLevelRole` falsy) renders the existing blocks exactly as before, preserving normal role behavior.

- [ ] **Step 4: Run typecheck and lint**

Run: `pnpm --filter frontend typecheck`
Expected: PASS.

Run: `pnpm --filter frontend eslint`
Expected: PASS.

- [ ] **Step 5: Commit (conditional)**

```bash
git add packages/frontend/src/pages/admin/roles.policy-editor.folder.vue packages/frontend/src/pages/admin/roles.policy-editor.vue
git commit -m "feat(role): integrate per-policy level conditions into policy editor"
```

---

### Task 5: Integrate manualLevel into `roles.editor.vue` and `roles.edit.vue`

**Files:**
- Modify: `packages/frontend/src/pages/admin/roles.editor.vue`
- Modify: `packages/frontend/src/pages/admin/roles.edit.vue`

**Interfaces:**
- Consumes: `shouldShowLevelPolicies` (Task 1), `RolesEditorLevel` (Task 2), `PolicyAsLevelMap` (Task 1), the extended `XPolicyEditor` (Task 4), i18n `_role.manualLevel`, `_role.levelPolicies`, `_role.canHideProfileByUser`, `_role.descriptionOfcanHideProfileByUser`.
- Produces: `RoleLike` payload including `target: 'manualLevel'`, `levelPolicies`, `canHideProfileByUser`, and `policies` whose values carry `policyAsLevel` (merged from `policyAsLevels`).

- [ ] **Step 1: Extend the `RoleLike` type in `roles.editor.vue`**

```ts
type RoleLike = Pick<Misskey.entities.Role, 'name' | 'description' | 'isAdministrator' | 'isModerator' | 'color' | 'iconUrl' | 'target' | 'isPublic' | 'isExplorable' | 'asBadge' | 'canEditMembersByModerator' | 'displayOrder' | 'preserveAssignmentOnMoveAccount' | 'canHideProfileByUser'> & {
	id?: Misskey.entities.Role['id'] | null;
	condFormula: any;
	policies: any;
	levelPolicies?: Misskey.entities.Role['levelPolicies'];
};
```

- [ ] **Step 2: Extend the target dropdown**

Replace the `MkSelect v-model="role.target"` items array:

```vue
	<MkSelect v-model="role.target" :items="[{ label: i18n.ts._role.manual, value: 'manual' }, { label: i18n.ts._role.conditional, value: 'conditional' }, { label: i18n.ts._role.manualLevel, value: 'manualLevel' }]" :readonly="readonly">
```

- [ ] **Step 3: Add the level-policies folder and the canHideProfileByUser switch**

Add after the `MkFolder v-if="role.target === 'conditional'"` block:

```vue
	<MkFolder v-if="shouldShowLevelPolicies(role.target)" defaultOpen>
		<template #label><i class="ti ti-chart-line"></i> {{ i18n.ts._role.levelPolicies }}</template>
		<div class="_gaps">
			<XLevelEditor v-model="role.levelPolicies" :readonly="readonly"/>
		</div>
	</MkFolder>
```

Add the switch next to the existing `asBadge`/`isExplorable` switches:

```vue
	<MkSwitch v-model="role.canHideProfileByUser" :disabled="readonly">
		<template #label>{{ i18n.ts._role.canHideProfileByUser }}</template>
		<template #caption>{{ i18n.ts._role.descriptionOfcanHideProfileByUser }}</template>
	</MkSwitch>
```

(Note: `MkSwitch` has no `readonly` prop; use `:disabled="readonly"`.)

- [ ] **Step 4: Pass the level-mode props into the existing `XPolicyEditor`**

In the `FormSlot` policies block, keep the existing `XPolicyEditor` and add the new bindings:

```vue
			<XPolicyEditor
				v-model:rolePolicies="rolePolicyValues"
				v-model:policiesMeta="rolePolicyMeta"
				v-model:policyAsLevels="policyAsLevels"
				:is-base-role="false"
				:is-level-role="shouldShowLevelPolicies(role.target)"
				:base-level="role.levelPolicies?.baseLevel ?? 0"
				:role-query="q"
				:readonly="readonly"
			/>
```

- [ ] **Step 5: Script state and `save()` merge**

Add imports and the `policyAsLevels` ref:

```ts
import XLevelEditor from './RolesEditorLevel.vue';
import { shouldShowLevelPolicies, type PolicyAsLevelMap } from './role-level-editor.util.js';

const policyAsLevels = ref<PolicyAsLevelMap>(
	Object.fromEntries(
		Object.entries(role.value.policies).map(([k, v]) => [k, (v as { policyAsLevel?: PolicyAsLevelMap[string] }).policyAsLevel ?? null]),
	),
);
```

In `save()`, merge `policyAsLevels` into the policies payload and add the new fields:

```ts
	const policies = { ...role.value.policies };
	for (const [k, v] of Object.entries(policyAsLevels.value)) {
		if (policies[k] != null) policies[k].policyAsLevel = v;
	}
	const data = {
		name: role.value.name,
		description: role.value.description,
		color: role.value.color === '' ? null : role.value.color,
		iconUrl: role.value.iconUrl === '' ? null : role.value.iconUrl,
		displayOrder: role.value.displayOrder,
		target: role.value.target,
		condFormula: role.value.condFormula,
		isAdministrator: role.value.isAdministrator,
		isModerator: role.value.isModerator,
		isPublic: role.value.isPublic,
		isExplorable: role.value.isExplorable,
		asBadge: role.value.asBadge,
		canEditMembersByModerator: role.value.canEditMembersByModerator,
		preserveAssignmentOnMoveAccount: role.value.preserveAssignmentOnMoveAccount,
		canHideProfileByUser: role.value.canHideProfileByUser,
		levelPolicies: role.value.levelPolicies ?? null,
		policies,
	};
```

- [ ] **Step 6: Extend `roles.edit.vue` default payload for a new role**

In the `else` branch of the `data.value` initializer, add:

```ts
			canHideProfileByUser: false,
			levelPolicies: defaultLevelPolicies(),
```

Add the import: `import { defaultLevelPolicies } from './role-level-editor.util.js';`

- [ ] **Step 7: Run typecheck, lint, and unit tests**

Run: `pnpm --filter frontend typecheck`
Expected: PASS.

Run: `pnpm --filter frontend eslint`
Expected: PASS.

Run: `pnpm --filter frontend test`
Expected: PASS.

- [ ] **Step 8: Commit (conditional)**

```bash
git add packages/frontend/src/pages/admin/roles.editor.vue packages/frontend/src/pages/admin/roles.edit.vue
git commit -m "feat(role): support manualLevel target in role create/edit"
```

---

### Task 6: `roles.vue` and `roles.role.vue` manualLevel wiring

**Files:**
- Modify: `packages/frontend/src/pages/admin/roles.vue`
- Modify: `packages/frontend/src/pages/admin/roles.role.vue`

**Interfaces:**
- Consumes: i18n `_role.manualLevelRoles`.
- Produces: admin roles list grouped into manual / manualLevel / conditional; the role detail page shows the member list for both `manual` and `manualLevel` (backend orders by experience).

- [ ] **Step 1: `roles.vue` — add the manualLevelRoles section**

Add a third `MkFoldableSection` between the manual and conditional sections:

```vue
				<MkFoldableSection>
					<template #header>{{ i18n.ts._role.manualLevelRoles }}</template>
					<div class="_gaps_s">
						<MkRolePreview v-for="role in roles.filter(x => x.target === 'manualLevel')" :key="role.id" :role="role" :forModeration="true"/>
					</div>
				</MkFoldableSection>
```

- [ ] **Step 2: `roles.role.vue` — include manualLevel in the member folder condition**

Replace the member folder condition:

```vue
			<MkFolder v-if="role.target === 'manual' || role.target === 'manualLevel'" defaultOpen>
```

Keep the existing paginator (`admin/roles/users`) and its current ordering. Render the returned assignment experience/level for manualLevel members; experience ordering is exclusive to the public `roles/users` endpoint.

- [ ] **Step 3: Run typecheck, lint, and unit tests**

Run: `pnpm --filter frontend typecheck`
Expected: PASS.

Run: `pnpm --filter frontend eslint`
Expected: PASS.

Run: `pnpm --filter frontend test`
Expected: PASS.

- [ ] **Step 4: Commit (conditional)**

```bash
git add packages/frontend/src/pages/admin/roles.vue packages/frontend/src/pages/admin/roles.role.vue
git commit -m "feat(role): wire manualLevel into role list and detail pages"
```

---

### Task 7: Final gates for Frontend PR 2

**Files:**
- No new source changes; this task runs required deterministic gates.

**Interfaces:**
- Consumes: all Tasks 1-6.

- [ ] **Step 1: Full typecheck and lint**

Run: `pnpm --filter frontend lint`
Expected: PASS.

- [ ] **Step 2: Full unit test suite**

Run: `pnpm --filter frontend test`
Expected: PASS (includes `role-level-editor.test.ts`, `role-level.test.ts`, `level-role-contract.test.ts`).

- [ ] **Step 3: Whole-tree gates**

Run: `pnpm --filter backend lint`
Expected: PASS.

Run: `pnpm lint`
Expected: PASS.

- [ ] **Step 4: Locale-existence gate (FAILS if Frontend PR 1 keys are absent)**

This plan adds no locale edits; verify every key this plan uses exists (PowerShell-native check; `rg`/`grep` are not assumed):

Run: `foreach ($p in 'manualLevel:','manualLevelRoles:','levelPolicies:','canHideProfileByUser:','changeExpConfirm:','_experience:') { $n = (Select-String -Path locales\ja-JP.yml -Pattern $p).Count; Write-Output "$p = $n"; if ($n -lt 1) { throw "missing locale key: $p" } }`
Expected: each key prints `= 1` or more; no throw. If any key is missing, STOP — Frontend PR 1 was not merged.

Run: `git diff --name-only develop -- 'locales/*.yml' | Select-String -NotMatch '^locales/ja-JP\.yml$'`
Expected: empty (no output).

- [ ] **Step 5: PR handoff note**

Browser-level verification of the admin editor is covered by the contract-smoke E2E in Frontend PR 3 (`2026-08-16-level-role-frontend-user-ui.md`) and, for full semantics, by the mk-go imported-DB Docker E2E (Backend PR 3). This PR must stay green on the deterministic gates above.
