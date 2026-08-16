# Level Role Frontend User UI Implementation Plan (Frontend PR 3)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the user-facing level-role UI: moderator experience operations, level/progress display in role previews and the profile, profile-role hide/show controls (inline in `settings/other.vue`), manualLevel inclusion in explore, moderation-log rendering for `changeExperienceRole`, and a fork-stack **contract-smoke** E2E.

**Architecture:** This plan builds on the generated contract, the complete i18n key set, and `@/utility/role-level.js` from Frontend PR 1, and the admin editor from Frontend PR 2. It modifies existing 2026.7 components in place (`admin-user.vue`, `MkRolePreview.vue`, `user/home.vue`, `settings/other.vue`, `explore.roles.vue`, `modlog.ModLog.vue`) and adds one small component `MkRoleDescriptionTooltip.vue`. **Defense-in-depth filtering of hidden roles is applied only where `isHideProfile` exists (profile/self `roles`), via `filterVisibleRoles` in `user/home.vue`; note-header `badgeRoles` filtering is NOT attempted because note users are `UserLite` and carry no hide metadata — the backend filters hidden badges for public views.** The E2E in this plan is a **fork TS dev-stack contract smoke** (endpoints respond, fields present, `setMode`/`hide` work); full level-calc semantics are verified only by the mk-go imported-DB Docker E2E (Backend PR 3).

**Tech Stack:** Vue 3.5, `misskey-js` generated types, `@/os.js` dialogs (`inputNumber`/`inputText`/`confirm`/`popupMenu`/`apiWithDialog`), `MkTooltip` (global `Mfm` component), Playwright 1.61, vitest 4.1 + happy-dom, vue-tsc 3.3.

## Global Constraints

All file paths are relative to the fork repo root `Misaki-Project/misskey-ts` (based on `ff25eac144c64d3ca1a06862547f7b101d315f98`). Frontend PR 1 and PR 2 MUST be merged first.

- **Endpoint request fields (from PR 1):** `admin/roles/change-exp` uses `setMode: 'set' | 'add' | 'multiplier'`, `value`, optional `assignForce`/`note`; `roles/profile-hide` uses `roleId` + `hide: boolean`.
- **`os` dialog signatures (verified in `@/os.js`):** `inputNumber({ title?, text?, placeholder?, default? })` returns `{ canceled, result }` with `result: number | null` — **`min`/`max`/`step` are NOT accepted**; `inputText({ type?, title?, placeholder?, default? })` returns `{ canceled, result }` with `result: string | null`; `confirm({ type, title?, text?, okText?, cancelText? })` returns `{ canceled }`; `popupMenu(items, anchorElement?)`; `apiWithDialog(endpoint, data)`.
- **Profile-hide visibility rule:** hidden assignments must not render in profiles/role badges/public user entities; self/moderator management responses carry the state. Frontend defense-in-depth applies **only where `isHideProfile` exists** (`UserDetailed['roles']` for self/moderator) via `filterVisibleRoles` in `user/home.vue`. **Do NOT filter `badgeRoles` in note headers** — note users are `UserLite` (no `roles`/`isHideProfile`), and the backend already excludes hidden badges from public views. No `badgeRoles.id` contract extension.
- **Moderation log:** experience changes log under type `changeExperienceRole` with `actionType` = the `setMode` value (`'set' | 'add' | 'multiplier'` — do NOT reproduce CherryPick's `'multipiler'` typo). Only the admin modlog renders internal values; nothing internal is exposed to public logs.
- **Locale rule:** this plan makes NO locale YAML edits; all keys (including `changeExpConfirm` and `_moderationLogTypes.changeExperienceRole`) were added in PR 1. The final gate verifies them and FAILS if absent.
- **E2E scope:** the spec in this plan runs against the fork TS dev stack (`pnpm e2e`) and asserts **contract smoke** only (endpoints respond, fields present, `setMode`/`hide` round-trip, profile chip visibility). It must NOT assert level-calc values (the fork has no level engine). Value-level semantics are asserted by the mk-go imported-DB Docker E2E (Backend PR 3), which re-runs the same stack against mk-go.
- **SPDX headers** on all new files.
- **No auto-commit:** `git commit` steps are conditional — run them ONLY with explicit authorization. Never push/merge/PR automatically.
- Do not modify anything outside the files listed in this plan.

---

### Task 1: Admin user experience operations (`admin-user.vue`)

**Files:**
- Modify: `packages/frontend/src/pages/admin-user.vue`

**Interfaces:**
- Consumes: `Misskey.api.AdminRolesChangeExpRequest`, `@/os.js` (`inputNumber`, `inputText`, `confirm`, `popupMenu`, `apiWithDialog`), i18n `_experience._calcs.{set,additional,multiplier}`, `_experience.settingValue`, `_experience.changeExpConfirm`, `note`, `moderationNote`, `none`, `unassign`, `formatLevel`.
- Produces: a per-manualLevel-role row action (pencil) opening a popup menu (set/add/multiplier); each mode collects a value, an optional note, a **confirm dialog** (`_experience.changeExpConfirm`), then `admin/roles/change-exp` and `refreshUser()`; the expanded row shows `Lv.{currentLevel}`. Uses the existing `user`/`info`/`expandedRoleIds`/`refreshUser` state in `admin-user.vue`; the viewed user's level comes from `user.value.roles` (the `users/show` UserDetailed pack), NOT from `info.roles` (which are packed for the requesting moderator).

- [ ] **Step 1: Add the experience trigger button in the roles tab**

In `packages/frontend/src/pages/admin-user.vue`, in the roles tab row, change the action buttons so manualLevel roles get the experience menu (keep manual = unassign, conditional = disabled):

```vue
					<button v-if="role.target === 'manual'" class="_button" :class="$style.roleUnassign" @click="unassignRole(role, $event)"><i class="ti ti-x"></i></button>
					<button v-else-if="role.target === 'manualLevel'" class="_button" :class="$style.roleUnassign" @click="experienceMenu(role, $event)"><i class="ti ti-pencil"></i></button>
					<button v-else class="_button" :class="$style.roleUnassign" disabled><i class="ti ti-ban"></i></button>
```

In the expanded role sub, add the current level line (above the "Assigned:" line):

```vue
					<div v-if="role.target === 'manualLevel' && viewedUserRole(role.id)?.experience">Level: {{ formatLevel(viewedUserRole(role.id)!.experience!.currentLevel) }}</div>
```

Add the helper:

```ts
function viewedUserRole(roleId: string) {
	return user.value.roles.find(r => r.id === roleId);
}
```

- [ ] **Step 2: Add the experience menu and send-with-confirm helpers**

Add to the script section (near `unassignRole`). Note: `os.inputNumber` accepts only `{ title?, text?, placeholder?, default? }` in this fork — do NOT pass `min`/`max`/`step`:

```ts
async function experienceMenu(role: typeof info.value.roles[number], ev: PointerEvent) {
	os.popupMenu([{
		text: i18n.ts._experience._calcs.set,
		icon: 'ti ti-letter-n',
		action: async () => {
			const { canceled, result: value } = await os.inputNumber({
				title: i18n.ts._experience.settingValue,
				default: 0,
			});
			if (canceled || value == null) return;
			await confirmAndSendChangeExp(role, 'set', value);
		},
	}, {
		text: i18n.ts._experience._calcs.additional,
		icon: 'ti ti-plus-minus',
		action: async () => {
			const { canceled, result: value } = await os.inputNumber({
				title: i18n.ts._experience.settingValue,
				default: 0,
			});
			if (canceled || value == null) return;
			await confirmAndSendChangeExp(role, 'add', value);
		},
	}, {
		text: i18n.ts._experience._calcs.multiplier,
		icon: 'ti ti-percentage',
		action: async () => {
			const { canceled, result: value } = await os.inputNumber({
				title: i18n.ts._experience.settingValue,
				default: 1,
			});
			if (canceled || value == null) return;
			await confirmAndSendChangeExp(role, 'multiplier', value);
		},
	}, {
		text: i18n.ts.unassign,
		icon: 'ti ti-x',
		danger: true,
		action: async () => {
			await os.apiWithDialog('admin/roles/unassign', {
				roleId: role.id, userId: user.value.id,
			}).then(refreshUser);
		},
	}], ev.currentTarget ?? ev.target);
}

async function confirmAndSendChangeExp(role: typeof info.value.roles[number], setMode: 'set' | 'add' | 'multiplier', value: number) {
	const { canceled: canceledNote, result: note } = await os.inputText({
		type: 'text',
		title: i18n.ts.note,
		default: null,
		placeholder: i18n.ts.moderationNote,
	});
	if (canceledNote) return;

	const modeLabel = setMode === 'add' ? i18n.ts._experience._calcs.additional : i18n.ts._experience._calcs[setMode];
	const { canceled } = await os.confirm({
		type: 'question',
		text: i18n.tsx._experience.changeExpConfirm({
			mode: modeLabel,
			value: String(value),
			note: note ?? i18n.ts.none,
		}),
	});
	if (canceled) return;

	await os.apiWithDialog('admin/roles/change-exp', {
		roleId: role.id,
		userId: user.value.id,
		setMode,
		value,
		assignForce: true,
		note: note ?? null,
	}).then(refreshUser);
}
```

Add the import: `import { formatLevel } from '@/utility/role-level.js';`

- [ ] **Step 3: Run typecheck and lint**

Run: `pnpm --filter frontend typecheck`
Expected: PASS.

Run: `pnpm --filter frontend eslint`
Expected: PASS.

- [ ] **Step 4: Commit (conditional)**

```bash
git add packages/frontend/src/pages/admin-user.vue
git commit -m "feat(role): add admin experience operations for manualLevel roles"
```

---

### Task 2: `MkRolePreview.vue` level display

**Files:**
- Modify: `packages/frontend/src/components/MkRolePreview.vue`

**Interfaces:**
- Consumes: `formatLevel` from `@/utility/role-level.js`, `role.experience` (generated type; `nextLevelExp: number | null`).
- Produces: shows `Lv.{currentLevel}` and `(currentExp / nextLevelExp)` (when not at max level) next to the role name for any role carrying `experience`, in both moderation and user contexts.

- [ ] **Step 1: Add the experience readout to the title row**

In `MkRolePreview.vue`, inside `:class="$style.bodyTitle"`, after the `bodyName` span and before the `bodyUsers` count block, add:

```vue
			<span v-if="'experience' in role && role.experience" :class="$style.bodyUsers">
				<b>{{ formatLevel(role.experience.currentLevel) }}</b>
				<span v-if="role.experience.nextLevelExp != null"> ({{ role.experience.currentExp }} / {{ role.experience.nextLevelExp }})</span>
			</span>
```

- [ ] **Step 2: Add the import**

```ts
import { formatLevel } from '@/utility/role-level.js';
```

- [ ] **Step 3: Run typecheck and lint**

Run: `pnpm --filter frontend typecheck`
Expected: PASS.

Run: `pnpm --filter frontend eslint`
Expected: PASS.

- [ ] **Step 4: Commit (conditional)**

```bash
git add packages/frontend/src/components/MkRolePreview.vue
git commit -m "feat(role): show current level and experience progress in role previews"
```

---

### Task 3: `MkRoleDescriptionTooltip.vue` and profile role rendering (`user/home.vue`)

**Files:**
- Create: `packages/frontend/src/components/MkRoleDescriptionTooltip.vue`
- Modify: `packages/frontend/src/pages/user/home.vue`

**Interfaces:**
- Consumes: `formatLevel`, `levelProgressPercent` from `@/utility/role-level.js`, `MkTooltip` (props `showing`, `anchorElement`, `maxWidth`, `direction`, emits `closed`), the global `Mfm` component (registered app-wide; do NOT import it), `filterVisibleRoles`.
- Produces:
  - `MkRoleDescriptionTooltip` props: `{ showing: boolean; anchorElement?: HTMLElement; role: { description: string; experience?: RoleExperience | null } }`; emits `(ev: 'closed')`.
  - `user/home.vue`: profile role chips filtered through `filterVisibleRoles` (the defense-in-depth point, where `isHideProfile` exists) and show `Lv.{currentLevel}` inline; hovering a role chip opens the level-progress tooltip.

- [ ] **Step 1: Create `packages/frontend/src/components/MkRoleDescriptionTooltip.vue`**

`Mfm` is a global component in this fork (defined in `packages/frontend/src/components/global/MkMfm.ts` and registered app-wide) — use `<Mfm>` in the template with no import. `MkTooltip` uses `anchorElement` (not `targetElement`):

```vue
<!--
SPDX-FileCopyrightText: syuilo and misskey-project
SPDX-License-Identifier: AGPL-3.0-only
-->

<template>
<MkTooltip :showing="showing" :anchorElement="anchorElement" :maxWidth="250" direction="top" @closed="emit('closed')">
	<div :class="$style.root">
		<div v-if="role.experience" style="margin-bottom: 1px;">
			<span style="font-size: 1.5em;">{{ formatLevel(role.experience.currentLevel) }}</span>
			<span> ({{ role.experience.currentExp }}</span><span v-if="role.experience.nextLevelExp != null"> / {{ role.experience.nextLevelExp }}</span><span>)</span>
		</div>
		<div v-if="role.experience" style="margin-bottom: 6px;">
			<div :style="{ background: 'var(--MI_THEME-accentedBg)', borderRadius: '4px', height: '5px', width: '100%', overflow: 'hidden' }">
				<div :style="{ background: 'var(--MI_THEME-accent)', width: levelProgressPercent(role.experience) + '%', height: '100%', transition: 'width 0.3s' }"></div>
			</div>
		</div>
		<Mfm :text="role.description"/>
	</div>
</MkTooltip>
</template>

<script lang="ts" setup>
import MkTooltip from '@/components/MkTooltip.vue';
import { formatLevel, levelProgressPercent, type RoleExperience } from '@/utility/role-level.js';

defineProps<{
	showing: boolean;
	anchorElement?: HTMLElement;
	role: {
		description: string;
		experience?: RoleExperience | null;
	};
}>();

const emit = defineEmits<{
	(ev: 'closed'): void;
}>();
</script>

<style lang="scss" module>
.root {
	font-size: 1em;
	text-align: left;
	text-wrap: normal;
}
</style>
```

- [ ] **Step 2: Modify `user/home.vue` role chips**

Replace the roles block with the filtered list, inline level, and tooltip wiring. The filter is applied here because `user.roles` (UserDetailed) carries `isHideProfile` for self/moderator views; anonymous views are already filtered by the backend:

```vue
						<div v-if="visibleRoles.length > 0" class="roles">
							<span v-for="role in visibleRoles" :key="role.id" class="role" :style="{ '--color': role.color ?? '' }">
								<MkA v-adaptive-bg :to="`/roles/${role.id}`" @mouseenter="showRoleTooltip($event, role)" @mouseleave="hideRoleTooltip">
									<img v-if="role.iconUrl" style="height: 1.3em; vertical-align: -22%; border-radius: 0.4em;" :src="role.iconUrl"/>
									<span>{{ role.name }}</span>
									<span v-if="role.experience" style="font-size: 0.85em; opacity: 0.8; padding-left: 4px"><b>{{ formatLevel(role.experience.currentLevel) }}</b></span>
								</MkA>
								<XRoleDescriptionTooltip v-if="tooltipRole === role" :role="role" :showing="tooltipShowing" :anchorElement="tooltipTarget"/>
							</span>
						</div>
```

Add the imports and script state/functions:

```ts
import XRoleDescriptionTooltip from '@/components/MkRoleDescriptionTooltip.vue';
import { filterVisibleRoles, formatLevel } from '@/utility/role-level.js';

const visibleRoles = computed(() => filterVisibleRoles(user.value.roles));
const tooltipRole = ref<Misskey.entities.UserDetailed['roles'][number] | null>(null);
const tooltipShowing = ref(false);
const tooltipTarget = ref<HTMLElement | null>(null);

function showRoleTooltip(event: MouseEvent, role: Misskey.entities.UserDetailed['roles'][number]) {
	tooltipRole.value = role;
	tooltipShowing.value = true;
	tooltipTarget.value = event.currentTarget as HTMLElement;
}

function hideRoleTooltip() {
	tooltipShowing.value = false;
	tooltipRole.value = null;
	tooltipTarget.value = null;
}
```

(`computed` and `ref` are already imported in `home.vue`; `user` is the reactive user entity.)

- [ ] **Step 3: Run typecheck and lint**

Run: `pnpm --filter frontend typecheck`
Expected: PASS.

Run: `pnpm --filter frontend eslint`
Expected: PASS.

- [ ] **Step 4: Commit (conditional)**

```bash
git add packages/frontend/src/components/MkRoleDescriptionTooltip.vue packages/frontend/src/pages/user/home.vue
git commit -m "feat(role): show level and progress tooltip on profile role chips"
```

---

### Task 4: Include manualLevel roles in explore

**Files:**
- Modify: `packages/frontend/src/pages/explore.roles.vue`

**Interfaces:**
- Consumes: `roles/list` response.
- Produces: the explore roles list includes explorable `manual` AND `manualLevel` roles (CherryPick-compatible filter).

- [ ] **Step 1: Change the filter**

```ts
	roles.value = res.filter(x => x.target === 'manual' || x.target === 'manualLevel').sort((a, b) => b.displayOrder - a.displayOrder);
```

- [ ] **Step 2: Run typecheck and lint**

Run: `pnpm --filter frontend typecheck`
Expected: PASS.

Run: `pnpm --filter frontend eslint`
Expected: PASS.

- [ ] **Step 3: Commit (conditional)**

```bash
git add packages/frontend/src/pages/explore.roles.vue
git commit -m "feat(role): include manualLevel roles in explore"
```

---

### Task 5: Profile-hide controls inline in `settings/other.vue`

**Files:**
- Modify: `packages/frontend/src/pages/settings/other.vue`

**Interfaces:**
- Consumes: `$i.roles` (self/MeDetailed roles carrying `canHideProfileByUser`/`isHideProfile`), `misskeyApi('roles/profile-hide', { roleId, hide })`, `misskeyApi('i', {})`, `MkRolePreview`, `os.apiWithDialog`.
- Produces: inline eye/eye-off toggle per role in the settings roles folder; the toggle renders for roles where `canHideProfileByUser === true` (or already hidden), per the PR 1 contract. Inline in the existing `settings/other.vue` roles folder — no new settings route.

- [ ] **Step 1: Add the toggle UI in `settings/other.vue`**

Replace the roles folder content so each role row carries a hide/show toggle (the folder already exists at the `rolesAssignedToMe` SearchMarker):

```vue
			<SearchMarker :keywords="['roles']">
				<MkFolder>
					<template #icon><SearchIcon><i class="ti ti-badges"></i></SearchIcon></template>
					<template #label><SearchLabel>{{ i18n.ts.rolesAssignedToMe }}</SearchLabel></template>

					<div class="_gaps_s">
						<div v-for="role in roles" :key="role.id" :class="$style.roleItemMain">
							<MkRolePreview :class="$style.role" :role="role" :forModeration="false"/>
							<button v-if="role.isHideProfile" v-tooltip="i18n.ts.roleShowProfileTip" class="_button" :class="$style.roleHide" @click="setRoleProfileHidden(role, false)"><i class="ti ti-eye-off"></i></button>
							<button v-else-if="role.canHideProfileByUser" v-tooltip="i18n.ts.roleHideProfileTip" class="_button" :class="$style.roleHide" @click="setRoleProfileHidden(role, true)"><i class="ti ti-eye"></i></button>
						</div>
					</div>
				</MkFolder>
			</SearchMarker>
```

Add the script state and handler (`os` and `ref` are already imported in this file; add the `misskey-api` import):

```ts
import { misskeyApi } from '@/utility/misskey-api.js';

const roles = ref($i.roles);

async function setRoleProfileHidden(role: typeof $i.roles[number], hide: boolean) {
	await os.apiWithDialog('roles/profile-hide', { roleId: role.id, hide });
	const fresh = await misskeyApi('i', {});
	roles.value = fresh.roles;
}
```

Add the style module entries:

```scss
<style lang="scss" module>
.role {
	flex: 1;
	min-width: 0;
	margin-right: 8px;
}

.roleItemMain {
	display: flex;
}

.roleHide {
	width: 32px;
	height: 32px;
	margin-left: 8px;
	align-self: center;
}
</style>
```

- [ ] **Step 2: Run typecheck and lint**

Run: `pnpm --filter frontend typecheck`
Expected: PASS.

Run: `pnpm --filter frontend eslint`
Expected: PASS.

- [ ] **Step 3: Commit (conditional)**

```bash
git add packages/frontend/src/pages/settings/other.vue
git commit -m "feat(role): add profile-hide toggles in settings"
```

---

### Task 6: Moderation-log rendering for `changeExperienceRole`

**Files:**
- Modify: `packages/frontend/src/pages/admin/modlog.ModLog.vue`

**Interfaces:**
- Consumes: i18n `_moderationLogTypes.changeExperienceRole` (from PR 1), `log.info` fields `{ userUsername, userHost, roleName, actionType, actionValue, beforeValue, afterValue, note }`.
- Produces: green category styling, a one-line summary, a details block (userId, roleName, per-`actionType` value rendering with the correct `'multiplier'` string — NOT CherryPick's `'multipiler'`), and a note folder. The modlog page is admin-only; no internal values are exposed publicly.

- [ ] **Step 1: Add the category color**

In the `logGreen` array, add `'changeExperienceRole'`:

```ts
					'createAbuseReportNotificationRecipient',
					'changeExperienceRole',
				].includes(log.type),
```

- [ ] **Step 2: Add the one-line summary**

After the existing `v-else-if="log.type === 'unassignRole'"` span, add:

```vue
		<span v-else-if="log.type === 'changeExperienceRole'">: @{{ log.info.userUsername }}{{ log.info.userHost ? '@' + log.info.userHost : '' }} {{ log.info.roleName }} <small>{{ Number(log.info.beforeValue).toLocaleString() }}</small> <i class="ti ti-arrow-right"></i> {{ Number(log.info.afterValue).toLocaleString() }}</span>
```

- [ ] **Step 3: Add the icon**

In the icon block, add:

```vue
		<i v-else-if="log.type === 'changeExperienceRole'" class="ti ti-chart-line"></i>
```

- [ ] **Step 4: Add the details block**

In the details section's `v-else-if` chain, add a `changeExperienceRole` branch:

```vue
		<template v-else-if="log.type === 'changeExperienceRole'">
			<div>{{ i18n.ts.user }}: {{ log.info.userId }}</div>
			<div>{{ i18n.ts.role }}: {{ log.info.roleName }}</div>
			<div v-if="log.info.actionType === 'set'">{{ Number(log.info.beforeValue).toLocaleString() }} <i class="ti ti-arrow-right"></i> {{ Number(log.info.afterValue).toLocaleString() }}</div>
			<div v-else-if="log.info.actionType === 'add'">
				{{ Number(log.info.beforeValue).toLocaleString() }} <i class="ti ti-arrow-right"></i>
				<span v-if="Number(log.info.actionValue) >= 0" :class="$style.logGreen">+{{ Number(log.info.actionValue).toLocaleString() }}</span>
				<span v-else :class="$style.logRed">{{ Number(log.info.actionValue).toLocaleString() }}</span>
				<i class="ti ti-arrow-right"></i> {{ Number(log.info.afterValue).toLocaleString() }}
			</div>
			<div v-else-if="log.info.actionType === 'multiplier'">{{ Number(log.info.beforeValue).toLocaleString() }} <i class="ti ti-arrow-right"></i> x{{ log.info.actionValue }} <i class="ti ti-arrow-right"></i> {{ Number(log.info.afterValue).toLocaleString() }}</div>
			<MkFolder v-if="log.info.note" :defaultOpen="true">
				<template #label>{{ i18n.ts.note }}</template>
				<pre>{{ log.info.note }}</pre>
			</MkFolder>
		</template>
```

(`logGreen`/`logRed` style classes already exist in this file.)

- [ ] **Step 5: Run typecheck and lint**

Run: `pnpm --filter frontend typecheck`
Expected: PASS.

Run: `pnpm --filter frontend eslint`
Expected: PASS.

- [ ] **Step 6: Commit (conditional)**

```bash
git add packages/frontend/src/pages/admin/modlog.ModLog.vue
git commit -m "feat(role): render changeExperienceRole moderation log entries"
```

---

### Task 7: Fork-stack contract-smoke E2E spec

**Files:**
- Create: `packages/frontend/test/e2e/level-role.spec.ts`

**Interfaces:**
- Consumes: `test`/`expect` from `./fixtures.js`, utils from `./utils.js` (`BASE_URL`, `registerUser`, `resetState`, `signIn`, `closeUserSetupDialog`), the fork dev endpoints from PR 1.
- Produces: a Playwright spec run against the fork TS dev stack (`pnpm e2e`) that asserts **contract smoke** only: `admin/roles/create` accepts `manualLevel` + `levelPolicies` + `canHideProfileByUser`; `admin/roles/show` returns those fields; `admin/roles/change-exp` with `setMode: 'set'` persists experience into `admin/show-user.roleAssigns[].experience`; `roles/profile-hide` with `hide: true` flips `roleAssigns[].isHideProfile`; the profile shows the role chip and removes it after hiding (frontend `filterVisibleRoles`). **No level-calc value assertions** (the fork has no level engine).

- [ ] **Step 1: Write the failing spec**

```ts
/*
 * SPDX-FileCopyrightText: syuilo and misskey-project
 * SPDX-License-Identifier: AGPL-3.0-only
 */

import { test, expect } from './fixtures.js';
import {
	BASE_URL,
	registerUser,
	resetState,
	signIn,
	closeUserSetupDialog,
} from './utils.js';
import type { RegisteredUser } from './utils.js';

async function createLevelRole(admin: RegisteredUser, request: { post: (url: string, opts: object) => Promise<{ ok(): boolean; json(): Promise<any> }> }) {
	const res = await request.post(`${BASE_URL}/api/admin/roles/create`, {
		data: {
			i: admin.token,
			name: 'Leveled',
			description: 'synthetic e2e level role',
			color: '#00ff00',
			iconUrl: null,
			target: 'manualLevel',
			condFormula: {},
			isPublic: true,
			isExplorable: true,
			isModerator: false,
			isAdministrator: false,
			asBadge: true,
			preserveAssignmentOnMoveAccount: false,
			canEditMembersByModerator: true,
			canHideProfileByUser: true,
			displayOrder: 0,
			policies: {},
			levelPolicies: {
				baseLevel: 1,
				experiencePolicies: [{ level: 3, type: 'const', base: 100, additional: 50 }],
			},
		},
	});
	expect(res.ok()).toBeTruthy();
	return (await res.json()) as { id: string };
}

test.describe('Level role (fork dev-stack contract smoke)', () => {
	let admin: RegisteredUser;
	let alice: RegisteredUser;

	test.beforeEach(async ({ request }) => {
		await resetState();
		admin = await registerUser('admin', 'pass', true);
		alice = await registerUser('alice', 'alice1234');
	});

	test('manualLevel role lifecycle: create, assign, change-exp, profile-hide', async ({ page, request }) => {
		const role = await createLevelRole(admin, request);

		const assign = await request.post(`${BASE_URL}/api/admin/roles/assign`, {
			data: { i: admin.token, roleId: role.id, userId: alice.id, expiresAt: null },
		});
		expect(assign.ok()).toBeTruthy();

		const setRes = await request.post(`${BASE_URL}/api/admin/roles/change-exp`, {
			data: { i: admin.token, roleId: role.id, userId: alice.id, setMode: 'set', value: 250, assignForce: true, note: null },
		});
		expect(setRes.ok()).toBeTruthy();

		// role detail exposes the manualLevel contract fields
		const show = await request.post(`${BASE_URL}/api/admin/roles/show`, {
			data: { i: admin.token, roleId: role.id },
		});
		const shown = await show.json();
		expect(shown.target).toBe('manualLevel');
		expect(shown.levelPolicies.baseLevel).toBe(1);
		expect(shown.canHideProfileByUser).toBe(true);

		// admin/show-user exposes experience and isHideProfile for the assignment
		const showUser = await request.post(`${BASE_URL}/api/admin/show-user`, {
			data: { i: admin.token, userId: alice.id },
		});
		const su = await showUser.json();
		const assignInfo = su.roleAssigns.find((a: { roleId: string }) => a.roleId === role.id);
		expect(assignInfo.experience).toBe(250);
		expect(assignInfo.isHideProfile).toBe(false);

		// the user sees the role chip on their own profile
		await signIn(page, 'alice', 'alice1234');
		await closeUserSetupDialog(page);
		await page.goto('/@alice');
		await page.getByText('Leveled').waitFor({ state: 'visible' });

		// profile-hide removes the chip (frontend defense-in-depth via filterVisibleRoles)
		const hide = await request.post(`${BASE_URL}/api/roles/profile-hide`, {
			data: { i: alice.token, roleId: role.id, hide: true },
		});
		expect(hide.ok()).toBeTruthy();

		const showUser2 = await request.post(`${BASE_URL}/api/admin/show-user`, {
			data: { i: admin.token, userId: alice.id },
		});
		const su2 = await showUser2.json();
		expect(su2.roleAssigns.find((a: { roleId: string }) => a.roleId === role.id).isHideProfile).toBe(true);

		await page.goto('/@alice');
		await page.getByText('Leveled').waitFor({ state: 'hidden' });
	});
});
```

- [ ] **Step 2: Run the spec collection to confirm it loads**

Run: `pnpm --filter frontend exec playwright test --list --config playwright.config.ts`
Expected: lists the new test without compile errors.

- [ ] **Step 3: Run the E2E suite against the fork dev stack**

Run: `pnpm e2e`
Expected: PASS — boots the fork backend (`start:test`) + frontend on `http://localhost:61812` and runs `test/e2e/**` including `level-role.spec.ts`. Requires Docker (PostgreSQL 18 + Redis 7) and `.config/test.yml` (`ncp .github/misskey/test.yml .config/test.yml`).

If the smoke fails against the fork stack, debug the fork dev contract handlers (Frontend PR 1) rather than weakening the spec.

- [ ] **Step 4: Commit (conditional)**

```bash
git add packages/frontend/test/e2e/level-role.spec.ts
git commit -m "test(role): add level-role contract-smoke E2E spec"
```

---

### Task 8: Final gates, locale gate, and integration dependency

**Files:**
- No new source changes; this task runs required deterministic gates.

**Interfaces:**
- Consumes: all Tasks 1-7.

- [ ] **Step 1: Full typecheck and lint**

Run: `pnpm --filter frontend lint`
Expected: PASS.

- [ ] **Step 2: Full unit test suite**

Run: `pnpm --filter frontend test`
Expected: PASS.

- [ ] **Step 3: Whole-tree gates**

Run: `pnpm --filter backend lint`
Expected: PASS.

Run: `pnpm lint`
Expected: PASS.

- [ ] **Step 4: Locale-existence gate (FAILS if Frontend PR 1 keys are absent)**

This plan adds no locale edits; verify every key this plan uses exists (PowerShell-native check; `rg`/`grep` are not assumed):

Run: `foreach ($p in 'changeExpConfirm:','changeExperienceRole:','roleHideProfileTip:','roleShowProfileTip:','descriptionRolesAssignedToMeOfSetting:') { $n = (Select-String -Path locales\ja-JP.yml -Pattern $p).Count; Write-Output "$p = $n"; if ($n -lt 1) { throw "missing locale key: $p" } }`
Expected: each key prints `= 1` or more; no throw. If any key is missing, STOP — Frontend PR 1 was not merged.

Run: `git diff --name-only develop -- 'locales/*.yml' | Select-String -NotMatch '^locales/ja-JP\.yml$'`
Expected: empty (no output).

- [ ] **Step 5: Exact final integration dependency**

The smoke spec in this plan runs against the fork TS dev stack. The **full imported-DB Docker E2E** — level-calc value assertions (`currentLevel`/`nextLevelExp`/`totalExp`/`minLevel`/`maxLevel`), member experience ordering, policy aggregation, and anonymous/self/moderator badge visibility — is Backend PR 3 of `Misaki-Project/mk` and runs **mk-go** serving the same contract, then re-runs this smoke spec against mk-go. After this PR (and PR 1/2) are merged, the mk-go `third_party/misskey` submodule pointer MUST be updated to the frontend repo's final merge SHA. Record this in the PR body so Backend PR 3 can be published as a contract-draft and finalized only after this PR lands. Do not merge Backend PR 3 before this PR.
