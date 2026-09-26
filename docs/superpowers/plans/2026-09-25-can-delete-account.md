# canDeleteAccount Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** CherryPick互換の`canDeleteAccount`を追加し、本人削除を実効policyで安全に拒否しながら、frontendの削除UIとrole editorを同じpolicyへ接続する。

**Architecture:** mk-goはpolicy schema、checkedなendpoint enforcement、frontend表示契約だけを保持する。policy値はnative roleと既存`EffectivePolicyResolver`の集約結果から取得し、将来pluginへ決定ロジックを移してもendpointとUIを変更しない。frontendは新しい`Misaki-Project/misskey-ts` forkで最小変更し、同commitのsubmoduleとbundled assets imageをmk-goへpinする。

**Tech Stack:** Go 1.27、Echo、testify、Vue 3、TypeScript、Vitest、pnpm 11.25.0、Git submodule、GitHub Actions、GHCR

## Global Constraints

- 上流`shiroha-a/mk`および`shiroha-a/misskey-ts`へpush、PR、設定変更を行わない。
- 変更先は`Misaki-Project/mk`と新設する`Misaki-Project/misskey-ts`だけにする。
- `implementer` subagentは使用しない。subagentを使う場合は`general`を使う。
- `canDeleteAccount`の既定値はboolean `true`とする。
- `POST /api/i/delete-account`だけを制限し、`POST /api/admin/delete-account`は変更しない。
- 管理者・rootの本人削除も実効policy `false`なら拒否する。
- policy `false`は`403 ROLE_PERMISSION_DENIED`、checked policy解決失敗は`500 INTERNAL_ERROR`でfail closedにする。
- policy判定はrequest bind、2FA、password検証より前に実行する。
- `canPurgeAccount`と`canTruncateAccount`は実装しない。
- DB migrationと新規依存は追加しない。
- frontend plugin用のnative route interception APIやDOM操作は追加しない。
- frontend forkのtagは`2026.9.1-mk.2`とし、submodule commit、assets image、pin documentationを一致させる。
- 変更前の`go test ./...`にはPostgreSQL未構成、plugin surface golden drift、Windows固有testの既存failureがある。対象packageとrepository gateで新規regressionを判定する。

---

### Task 1: Project Tracking And Frontend Fork

**Files:**
- No repository file changes

**Interfaces:**
- Produces: `Misaki-Project/mk`のfeature issue URL
- Produces: `Misaki-Project/misskey-ts` fork
- Produces: backend branch `feature/can-delete-account` based on `origin/Misaki-develop`
- Produces: frontend branch `feature/can-delete-account` based on commit `71367bfef82c68b93e99fac2f8efc746a7c42735`

- [ ] **Step 1: Confirm the backend worktree baseline**

Run in `E:\tmp\opencode\mk-can-delete-account`:

```powershell
git status --short --branch
git rev-parse HEAD
```

Expected: branch `feature/can-delete-account`, HEAD `30e138162661fc368ca557dc5ac026b54987412e`, and only the approved design/plan documents are untracked or modified.

- [ ] **Step 2: Create the mk tracking issue**

```powershell
gh issue create --repo Misaki-Project/mk --title "CherryPick互換のcanDeleteAccount policyを移植する" --body "## Scope`n- canDeleteAccount default true`n- i/delete-accountを実効policyで拒否`n- admin/delete-accountは対象外`n- frontendの本人削除UIとrole editorを接続`n- 将来EffectivePolicyResolverへ判断を移せるchecked境界を維持`n`n## Repositories`n- Misaki-Project/misskey-ts: frontend変更とassets image`n- Misaki-Project/mk: backend、submodule、bundled assets pin`n`n## Out of scope`n- canPurgeAccount`n- canTruncateAccount`n- 汎用endpoint interception plugin API"
```

Expected: a new `https://github.com/Misaki-Project/mk/issues/...` URL. Record that URL in both PR descriptions later.

- [ ] **Step 3: Create the frontend fork without modifying the source repository**

```powershell
gh repo fork shiroha-a/misskey-ts --org Misaki-Project --clone=false
gh repo view Misaki-Project/misskey-ts --json nameWithOwner,isFork,parent
gh workflow enable assets-image.yml --repo Misaki-Project/misskey-ts
```

Expected: `nameWithOwner` is `Misaki-Project/misskey-ts`, `isFork` is `true`, and parent is `shiroha-a/misskey-ts`.

- [ ] **Step 4: Create an isolated frontend checkout**

First verify `E:\tmp\opencode` exists and `E:\tmp\opencode\misskey-ts-can-delete-account` does not exist. Then run:

```powershell
git clone https://github.com/Misaki-Project/misskey-ts.git "E:\tmp\opencode\misskey-ts-can-delete-account"
git switch --create feature/can-delete-account 71367bfef82c68b93e99fac2f8efc746a7c42735
git status --short --branch
```

Expected: a clean `feature/can-delete-account` branch at `71367bfe`. Run the last two commands with workdir `E:\tmp\opencode\misskey-ts-can-delete-account`.

### Task 2: Native Policy Catalog And Plugin Boundary

**Files:**
- Modify: `internal/effectivepolicy/validation.go:13-98`
- Modify: `internal/effectivepolicy/validation_test.go:13-30`
- Modify: `internal/core/role/role_service.go:126-166`
- Modify: `internal/core/role/plugin_policy_test.go:62-90`
- Modify: `docs/design/can-delete-account.md`
- Create: `docs/superpowers/plans/2026-09-25-can-delete-account.md`

**Interfaces:**
- Produces: `role.PolicyCanDeleteAccount = "canDeleteAccount"`
- Produces: `effectivepolicy.Defaults()["canDeleteAccount"] == true`
- Preserves: `plugin.EffectivePolicyRegistration{Keys: []string{role.PolicyCanDeleteAccount}}` is accepted

- [ ] **Step 1: Write the failing policy contract test**

Add to `internal/effectivepolicy/validation_test.go`:

```go
func TestCanDeleteAccountPolicyContract(t *testing.T) {
	defaults := Defaults()
	require.Equal(t, true, defaults["canDeleteAccount"])

	resolver := func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		return []plugin.EffectivePolicyContribution{{Key: "canDeleteAccount", Value: false}}, nil
	}
	require.NoError(t, ValidateRegistration(plugin.EffectivePolicyRegistration{
		Keys:    []string{"canDeleteAccount"},
		Resolve: resolver,
	}))
	require.True(t, ValidateContributions(
		[]string{"canDeleteAccount"},
		[]plugin.EffectivePolicyContribution{{Key: "canDeleteAccount", Value: false}},
	))
}
```

- [ ] **Step 2: Run the contract test and confirm RED**

Run:

```powershell
go test ./internal/effectivepolicy -run TestCanDeleteAccountPolicyContract -count=1
```

Expected: FAIL because `canDeleteAccount` is absent and provider registration rejects it.

- [ ] **Step 3: Add the default and constant**

Add this boolean entry near the other account capability defaults in `internal/effectivepolicy/validation.go`:

```go
"canDeleteAccount": true,
```

Add this constant to the policy constant block in `internal/core/role/role_service.go`:

```go
// PolicyCanDeleteAccount gates a user's own i/delete-account request. Unlike
// HasRolePolicy consumers, this policy does not grant administrators a bypass.
PolicyCanDeleteAccount = "canDeleteAccount"
```

- [ ] **Step 4: Add a plugin aggregation regression test**

Add to `internal/core/role/plugin_policy_test.go`:

```go
func TestEffectivePolicy_CanDeleteAccountProviderCanDeny(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	registerProvider(t, svc, "account-policy", []string{role.PolicyCanDeleteAccount},
		func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
			return []plugin.EffectivePolicyContribution{{
				Key:      role.PolicyCanDeleteAccount,
				Priority: 2,
				Value:    false,
			}}, nil
		})

	policies, err := svc.GetUserPoliciesChecked("u1")
	require.NoError(t, err)
	assert.Equal(t, false, policies[role.PolicyCanDeleteAccount])
}
```

- [ ] **Step 5: Run focused policy tests and confirm GREEN**

```powershell
go test ./internal/effectivepolicy ./internal/core/role -run "CanDeleteAccount|ValidateRegistration|ValidateContributions" -count=1
```

Expected: PASS.

- [ ] **Step 6: Review and commit the catalog contract**

```powershell
gofmt -w internal/effectivepolicy/validation.go internal/effectivepolicy/validation_test.go internal/core/role/role_service.go internal/core/role/plugin_policy_test.go
git diff --check
git diff -- internal/effectivepolicy internal/core/role docs/design/can-delete-account.md
git add docs/design/can-delete-account.md docs/superpowers/plans/2026-09-25-can-delete-account.md internal/effectivepolicy/validation.go internal/effectivepolicy/validation_test.go internal/core/role/role_service.go internal/core/role/plugin_policy_test.go
git commit -m "Add role: define canDeleteAccount policy"
```

Expected: one commit containing the reviewed design and native/plugin policy contract, with no frontend or endpoint changes.

### Task 3: Checked Self-Delete Enforcement

**Files:**
- Modify: `internal/api/i/handler.go:36-47,543-576`
- Modify: `internal/api/i/handler_extra.go:126-225`
- Modify: `internal/api/i/handler_extra_test.go:35-45,197-286,662-742`
- Test: `internal/api/i/handler_extra_test.go`

**Interfaces:**
- Consumes: `role.PolicyCanDeleteAccount`
- Consumes: production `role.Service.GetUserPoliciesChecked(userID string) (map[string]any, error)`
- Produces: private `checkedRoleProvider` interface with that exact method
- Produces: `Handler.DeleteAccount` returning 403 for resolved deny and 500 for resolution failure

- [ ] **Step 1: Extend the test role provider with checked resolution**

Add `policyErr error` to `stubRoleProvider` in `internal/api/i/handler_test.go`, then add:

```go
func (s *stubRoleProvider) GetUserPoliciesChecked(userID string) (map[string]any, error) {
	return s.GetUserPolicies(userID), s.policyErr
}
```

Add this helper near the delete-account tests in `internal/api/i/handler_extra_test.go` and use it for every existing success/error test that expects to reach the old validation logic:

```go
func newDeleteAccountHandler(t *testing.T) (*Handler, *testutil.MockUserRepository, *stubRoleProvider) {
	t.Helper()
	h, repo := newExtraHandler(t)
	provider := &stubRoleProvider{policies: map[string]any{role.PolicyCanDeleteAccount: true}}
	h.SetRoleProvider(provider)
	return h, repo, provider
}
```

Import `github.com/shiroha-a/mk/internal/core/role`. Leave `newExtraHandler` unchanged so missing wiring can still be tested explicitly and unrelated tests retain their old setup.

- [ ] **Step 2: Write failing denial and failure tests**

Add table-driven coverage in `internal/api/i/handler_extra_test.go`:

```go
func TestDeleteAccount_CanDeleteAccountPolicy(t *testing.T) {
	for _, tt := range []struct {
		name     string
		provider *stubRoleProvider
		wantCode int
		wantBody string
	}{
		{"false", &stubRoleProvider{policies: map[string]any{role.PolicyCanDeleteAccount: false}}, http.StatusForbidden, "ROLE_PERMISSION_DENIED"},
		{"administrator false", &stubRoleProvider{admin: true, policies: map[string]any{role.PolicyCanDeleteAccount: false}}, http.StatusForbidden, "ROLE_PERMISSION_DENIED"},
		{"missing", &stubRoleProvider{policies: map[string]any{}}, http.StatusForbidden, "ROLE_PERMISSION_DENIED"},
		{"wrong type", &stubRoleProvider{policies: map[string]any{role.PolicyCanDeleteAccount: "true"}}, http.StatusForbidden, "ROLE_PERMISSION_DENIED"},
		{"resolver error", &stubRoleProvider{policies: map[string]any{role.PolicyCanDeleteAccount: true}, policyErr: errors.New("resolver failed")}, http.StatusInternalServerError, "INTERNAL_ERROR"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h, repo := newExtraHandler(t)
			h.SetRoleProvider(tt.provider)
			enq := &fakeDeleteEnqueuer{}
			fed := &fakeAccountDeletionFed{}
			inv := &stubTokenInvalidator{}
			h.SetDeleteAccountEnqueuer(enq)
			h.SetAccountDeletionFederationHook(fed)
			h.SetAuthInvalidator(inv)
			user := setupUserWithPassword(repo, "u1", "pass")

			rec := postExtra(h.DeleteAccount, `{"password":"pass"}`, user)

			assert.Equal(t, tt.wantCode, rec.Code)
			assert.Contains(t, rec.Body.String(), tt.wantBody)
			assert.False(t, repo.Users["u1"].IsDeleted)
			assert.False(t, repo.Users["u1"].IsSuspended)
			assert.Empty(t, enq.called)
			assert.Empty(t, fed.deleted)
			assert.Empty(t, inv.userCalls)
		})
	}
}

func TestDeleteAccount_MissingCheckedProviderFailsClosed(t *testing.T) {
	h, repo := newExtraHandler(t)
	user := setupUserWithPassword(repo, "u1", "pass")
	rec := postExtra(h.DeleteAccount, `{"password":"pass"}`, user)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "INTERNAL_ERROR")
	assert.False(t, repo.Users["u1"].IsDeleted)
}
```

- [ ] **Step 3: Prove the policy gate runs before credential handling**

Add this test. It confirms a backup code is not consumed while denied by changing the same checked provider to allow and reusing the code:

```go
func TestDeleteAccount_PolicyDenialDoesNotConsumeTwoFactorToken(t *testing.T) {
	h, repo, provider := newDeleteAccountHandler(t)
	user := setupUserWithPassword(repo, "u1", "pass")
	enableTwoFactorWithBackupCodes(repo, "u1")
	provider.policies[role.PolicyCanDeleteAccount] = false

	denied := postExtra(h.DeleteAccount, `{"password":"pass","token":"backup1"}`, user)
	assert.Equal(t, http.StatusForbidden, denied.Code)

	provider.policies[role.PolicyCanDeleteAccount] = true
	allowed := postExtra(h.DeleteAccount, `{"password":"pass","token":"backup1"}`, user)
	assert.Equal(t, http.StatusNoContent, allowed.Code)
}
```

- [ ] **Step 4: Run the new tests and confirm RED**

```powershell
go test ./internal/api/i -run "TestDeleteAccount_(CanDeleteAccountPolicy|MissingCheckedProviderFailsClosed|PolicyDenialDoesNotConsumeTwoFactorToken)" -count=1
```

Expected: FAIL because `DeleteAccount` does not consult the policy.

- [ ] **Step 5: Implement the narrow checked gate**

Define near `RoleProvider` in `internal/api/i/handler.go`:

```go
type checkedRoleProvider interface {
	GetUserPoliciesChecked(userID string) (map[string]any, error)
}
```

At the start of `DeleteAccount`, immediately after `u := middleware.GetUser(c)` and before `c.Bind`, add the checked lookup. Do not call `HasRolePolicy`:

```go
	checked, ok := h.roleProvider.(checkedRoleProvider)
	if !ok {
		slog.Error("i/delete-account: checked role provider is not wired")
		return apierr.JSONInternalError(c)
	}
	policies, err := checked.GetUserPoliciesChecked(u.ID)
	if err != nil {
		slog.Error("i/delete-account: cannot resolve effective policies", "err", err)
		return apierr.JSONInternalError(c)
	}
	allowed, valid := policies[role.PolicyCanDeleteAccount].(bool)
	if !valid || !allowed {
		return apierr.JSONRolePermissionDenied(c)
	}
```

The error log must not include plugin names, policy values, password, token, or user ID.

- [ ] **Step 6: Run the complete account handler tests**

```powershell
gofmt -w internal/api/i/handler.go internal/api/i/handler_extra.go internal/api/i/handler_test.go internal/api/i/handler_extra_test.go
go test ./internal/api/i -run TestDeleteAccount -count=1
go test ./internal/api/i -count=1
go test ./internal/api/admin -run DeleteAccount -count=1
```

Expected: PASS. The admin tests prove `admin/delete-account` remains unchanged.

- [ ] **Step 7: Commit the endpoint enforcement**

```powershell
git diff --check
git diff -- internal/api/i
git add internal/api/i/handler.go internal/api/i/handler_extra.go internal/api/i/handler_test.go internal/api/i/handler_extra_test.go
git commit -m "Fix account: enforce self-delete policy"
```

### Task 4: Frontend Policy UI In Misaki Fork

**Files:**
- Create: `packages/frontend/src/utility/account-delete-policy.ts`
- Create: `packages/frontend/test/unit/account-delete-policy.test.ts`
- Modify: `packages/frontend/src/pages/settings/other.vue:72-84,160-188`
- Modify: `packages/frontend/src/pages/admin/roles.editor.vue:128-152`
- Modify: `packages/frontend/src/pages/admin/roles.policy-editor.vue:235-289,625-739`
- Modify: `locales/en-US.yml:3712-3723`
- Modify: `locales/ja-JP.yml:3727-3745`

**Interfaces:**
- Consumes: `/api/i` response field `policies.canDeleteAccount`
- Produces: `isAccountDeletionAllowed(policies: object): boolean`
- Produces: role editor metadata/value entry `canDeleteAccount`, default `true`

- [ ] **Step 1: Write the failing pure policy test**

Create `packages/frontend/test/unit/account-delete-policy.test.ts`:

```ts
import { describe, expect, test } from 'vitest';
import { isAccountDeletionAllowed } from '@/utility/account-delete-policy.js';

describe('isAccountDeletionAllowed', () => {
	test.each([
		[{ canDeleteAccount: true }, true],
		[{ canDeleteAccount: false }, false],
		[{}, false],
		[{ canDeleteAccount: 'true' }, false],
	])('uses only a typed true value', (policies, expected) => {
		expect(isAccountDeletionAllowed(policies)).toBe(expected);
	});
});
```

- [ ] **Step 2: Run the test and confirm RED**

After `pnpm install --frozen-lockfile`, run in `E:\tmp\opencode\misskey-ts-can-delete-account`:

```powershell
pnpm --filter frontend exec vitest --run --globals --config vitest.config.unit.ts test/unit/account-delete-policy.test.ts
```

Expected: FAIL because the utility module does not exist.

- [ ] **Step 3: Implement the strict boolean helper**

Create `packages/frontend/src/utility/account-delete-policy.ts`:

```ts
export function isAccountDeletionAllowed(policies: object): boolean {
	return (policies as Record<string, unknown>).canDeleteAccount === true;
}
```

Run the focused test again; expected PASS.

- [ ] **Step 4: Hide the complete account deletion section**

In `other.vue`, import the helper and put the guard on the outer `SearchMarker`, not only the button:

```vue
<SearchMarker v-if="isAccountDeletionAllowed($i.policies)" :keywords="['account', 'close', 'delete']">
```

```ts
import { isAccountDeletionAllowed } from '@/utility/account-delete-policy.js';
```

Do not alter `deleteAccount()`: backend enforcement remains authoritative.

- [ ] **Step 5: Add the role editor key and control**

Add `'canDeleteAccount'` to both `mkGoRolePolicyKeys` in `roles.editor.vue` and `mkGoPolicyMetaKeys` in `roles.policy-editor.vue`.

Add a boolean folder beside the other capability controls:

```vue
<XFolder v-if="matchQuery([i18n.ts._mkgoRolePolicy.canDeleteAccount, 'canDeleteAccount'])" v-model:policyMeta="canDeleteAccountMeta" :isBaseRole="isBaseRole" :readonly="readonly">
	<template #label>{{ i18n.ts._mkgoRolePolicy.canDeleteAccount }}</template>
	<template #valueText>{{ canDeleteAccount ? i18n.ts.yes : i18n.ts.no }}</template>
	<template #default="{ disabled }">
		<MkSwitch v-model="canDeleteAccount" :disabled="disabled">
			<template #label>{{ i18n.ts.enable }}</template>
			<template #caption>{{ i18n.ts._mkgoRolePolicy.canDeleteAccount_caption }}</template>
		</MkSwitch>
	</template>
</XFolder>
```

Add the value/meta pair with the backend default:

```ts
const canDeleteAccount = mkGoPolicyValue('canDeleteAccount', true);
const canDeleteAccountMeta = mkGoPolicyMeta('canDeleteAccount');
```

- [ ] **Step 6: Add English and Japanese copy**

Under `_mkgoRolePolicy` add:

```yaml
# en-US.yml
  canDeleteAccount: "Can delete own account"
  canDeleteAccount_caption: "Allows members of this role to request deletion of their own account. This does not affect administrators deleting other accounts."
```

```yaml
# ja-JP.yml
  canDeleteAccount: "自分のアカウントを削除できる"
  canDeleteAccount_caption: "このロールのメンバーが自分のアカウントの削除を申請できるようにします。管理者が他のアカウントを削除する操作には影響しません。"
```

- [ ] **Step 7: Run frontend verification**

```powershell
pnpm --filter frontend exec vitest --run --globals --config vitest.config.unit.ts test/unit/account-delete-policy.test.ts
pnpm --filter frontend typecheck
pnpm --filter frontend eslint
pnpm exec eslint "packages/frontend/src/utility/account-delete-policy.ts" "packages/frontend/test/unit/account-delete-policy.test.ts"
pnpm exec eslint "packages/frontend/src/pages/settings/other.vue" "packages/frontend/src/pages/admin/roles.editor.vue" "packages/frontend/src/pages/admin/roles.policy-editor.vue"
```

Expected: all commands PASS. If the repository's root locale check is available, also run `pnpm lint` and record unrelated pre-existing failures separately.

- [ ] **Step 8: Commit and open the frontend PR**

```powershell
git diff --check
git status --short
git add packages/frontend/src/utility/account-delete-policy.ts packages/frontend/test/unit/account-delete-policy.test.ts packages/frontend/src/pages/settings/other.vue packages/frontend/src/pages/admin/roles.editor.vue packages/frontend/src/pages/admin/roles.policy-editor.vue locales/en-US.yml locales/ja-JP.yml
git commit -m "Add roles: control self-service account deletion"
git push -u origin feature/can-delete-account
$issue = gh issue list --repo Misaki-Project/mk --state open --search "CherryPick互換のcanDeleteAccount policyを移植する in:title" --json url | ConvertFrom-Json
gh pr create --repo Misaki-Project/misskey-ts --base mk-2026.9.1 --head feature/can-delete-account --title "Add canDeleteAccount frontend controls" --body "Tracking issue: $($issue[0].url)`n`nAdds the role editor control and hides self-service account deletion unless the effective canDeleteAccount policy is true. Backend enforcement is implemented in Misaki-Project/mk."
```

Expected: a PR only in `Misaki-Project/misskey-ts`. Do not create a PR against `shiroha-a/misskey-ts`.

### Task 5: Publish Frontend Assets And Pin Mk

**Files:**
- Modify: `.gitmodules`
- Modify: `third_party/misskey` gitlink
- Modify: `Dockerfile.bundled:22`
- Modify: `docs/divergence.md:13,41,463,600`
- Modify: `docs/upstream-catch-up.md:189-190,352-360,379`
- Modify: `docs/deployment.md:170`
- Modify: `docs/plugins/operating.md:39`
- Modify: `docs/design/can-delete-account.md`
- Modify: `internal/server/rolepolicy_keys_gate_test.go:37-93`
- Modify: `Makefile:50-60`

**Interfaces:**
- Consumes: merged frontend commit tagged `2026.9.1-mk.2`
- Produces: submodule URL `https://github.com/Misaki-Project/misskey-ts.git`
- Produces: assets image `ghcr.io/misaki-project/misskey-ts-assets:2026.9.1-mk.2`
- Produces: static gate proving the pinned settings page uses the deletion policy helper

- [ ] **Step 1: Merge the frontend PR and tag the exact merge commit**

After required checks and user approval, inspect and merge the Misaki frontend PR without force-push or amend. Then run:

```powershell
$openFrontendPr = gh pr list --repo Misaki-Project/misskey-ts --state open --search "Add canDeleteAccount frontend controls in:title" --json number | ConvertFrom-Json
gh pr checks --repo Misaki-Project/misskey-ts $openFrontendPr[0].number
gh pr merge --repo Misaki-Project/misskey-ts $openFrontendPr[0].number --merge --delete-branch
$frontendPr = gh pr list --repo Misaki-Project/misskey-ts --state merged --search "Add canDeleteAccount frontend controls in:title" --json number,mergeCommit,url | ConvertFrom-Json
$frontendCommit = $frontendPr[0].mergeCommit.oid
if (-not $frontendCommit) { throw "frontend PR merge commit is unavailable" }
git fetch origin
git tag -a 2026.9.1-mk.2 $frontendCommit -m "2026.9.1-mk.2"
git push origin 2026.9.1-mk.2
```

Run the git commands in `E:\tmp\opencode\misskey-ts-can-delete-account`. Expected: tag `2026.9.1-mk.2` points exactly at the merged PR commit.

- [ ] **Step 2: Verify the assets workflow publishes from the dynamic repository name**

The inherited `.github/workflows/assets-image.yml` uses `IMAGE_NAME: ${{ github.repository }}-assets`, so the new fork publishes to the correct GHCR namespace without editing the workflow. Verify:

```powershell
gh run list --repo Misaki-Project/misskey-ts --workflow "Publish frontend assets image" --branch 2026.9.1-mk.2 --limit 1
gh run watch --repo Misaki-Project/misskey-ts $((gh run list --repo Misaki-Project/misskey-ts --workflow "Publish frontend assets image" --branch 2026.9.1-mk.2 --limit 1 --json databaseId | ConvertFrom-Json)[0].databaseId) --exit-status
```

Expected: workflow success and published image `ghcr.io/misaki-project/misskey-ts-assets:2026.9.1-mk.2`.

Make the newly created package public so unauthenticated bundled builds can pull it, then inspect the immutable tag:

```powershell
gh api --method PATCH /orgs/Misaki-Project/packages/container/misskey-ts-assets -f visibility=public
docker buildx imagetools inspect ghcr.io/misaki-project/misskey-ts-assets:2026.9.1-mk.2
```

Expected: the API call succeeds and `imagetools inspect` resolves the published manifest without registry login.

- [ ] **Step 3: Write the failing parent frontend wiring gate**

Add to `internal/server/rolepolicy_keys_gate_test.go`:

```go
func TestCanDeleteAccountIsWiredInSettings(t *testing.T) {
	path := filepath.Join(repoRootDir(t), "third_party", "misskey", "packages", "frontend", "src", "pages", "settings", "other.vue")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.Getenv("MK_FRONTEND_GATES_REQUIRE_SUBMODULE") != "" {
			require.NoError(t, err)
		}
		t.Skipf("submodule が無い: %v", err)
	}
	src := htmlComment.ReplaceAll(raw, nil)
	require.Contains(t, string(src), `import { isAccountDeletionAllowed } from '@/utility/account-delete-policy.js';`)
	require.Regexp(t, regexp.MustCompile(`<SearchMarker\s+v-if="isAccountDeletionAllowed\(\$i\.policies\)"\s+:keywords="\['account', 'close', 'delete'\]">`), string(src))
}
```

Add `TestCanDeleteAccountIsWiredInSettings` to the `make frontend-check` test regex in `Makefile`.

Run against the old pin:

```powershell
$env:MK_FRONTEND_GATES_REQUIRE_SUBMODULE = "1"
go test ./internal/server -run TestCanDeleteAccountIsWiredInSettings -count=1
Remove-Item Env:MK_FRONTEND_GATES_REQUIRE_SUBMODULE
```

Expected: FAIL because the old submodule lacks the helper and guard.

- [ ] **Step 4: Update the submodule remote and checkout the tagged commit**

Change `.gitmodules` to:

```ini
[submodule "third_party/misskey"]
	path = third_party/misskey
	url = https://github.com/Misaki-Project/misskey-ts.git
```

Then run:

```powershell
git submodule sync -- third_party/misskey
git -C third_party/misskey fetch origin tag 2026.9.1-mk.2
git -C third_party/misskey checkout 2026.9.1-mk.2
git -C third_party/misskey rev-parse HEAD
```

Expected: HEAD equals the frontend PR merge commit captured in Step 1.

- [ ] **Step 5: Update the bundled assets pin and current operational docs**

Set `Dockerfile.bundled` to:

```dockerfile
ARG MISSKEY_ASSETS_IMAGE=ghcr.io/misaki-project/misskey-ts-assets:2026.9.1-mk.2
```

Use `git -C third_party/misskey rev-parse --short=8 HEAD` for the exact SHA in the `docs/divergence.md` current-pin sentence, add a `2026.9.1-mk.2` table row describing `canDeleteAccount`, and update current fork/assets instructions in `docs/upstream-catch-up.md`, `docs/deployment.md`, and `docs/plugins/operating.md` from `shiroha-a` to `Misaki-Project`/`misaki-project`. Preserve historical changelog entries that intentionally describe old repositories. Change `docs/design/can-delete-account.md` from planned fork wording to the actual fork/tag/image.

- [ ] **Step 6: Run parent frontend and pin gates**

```powershell
go test ./internal/server -run "TestMkGoRolePolicyKeysAreListedInFrontend|TestCanDeleteAccountIsWiredInSettings" -count=1
go test ./internal/entitycompat -run "TestBundledAssetsPinMatchesDoc|TestSubmodulePin" -count=1
make submodulepin-check
make frontend-check
```

Expected: PASS. The role-policy gate must find `canDeleteAccount` in both editor key lists and as an `XFolder` control.

- [ ] **Step 7: Commit the frontend integration**

```powershell
git diff --check
git status --short
git diff --submodule=log
git add .gitmodules third_party/misskey Dockerfile.bundled Makefile docs/divergence.md docs/upstream-catch-up.md docs/deployment.md docs/plugins/operating.md docs/design/can-delete-account.md internal/server/rolepolicy_keys_gate_test.go
git commit -m "Update frontend: pin canDeleteAccount controls"
```

Expected: the commit contains the new fork URL, exact frontend gitlink, matching GHCR tag, current docs, and wiring gate.

### Task 6: Final Verification And Misaki Mk PR

**Files:**
- Verify all files changed in Tasks 2-5
- No additional implementation files unless verification exposes a defect

**Interfaces:**
- Produces: one `Misaki-Project/mk` PR linked to the tracking issue and frontend PR
- Preserves: no changes or PRs in either `shiroha-a` repository

- [ ] **Step 1: Run formatting and focused tests from a clean command environment**

```powershell
gofmt -w internal/effectivepolicy/validation.go internal/effectivepolicy/validation_test.go internal/core/role/role_service.go internal/core/role/plugin_policy_test.go internal/api/i/handler.go internal/api/i/handler_extra.go internal/api/i/handler_test.go internal/api/i/handler_extra_test.go internal/server/rolepolicy_keys_gate_test.go
go test ./internal/effectivepolicy ./internal/core/role -run "CanDeleteAccount|ValidateRegistration|ValidateContributions" -count=1
go test ./internal/core/role -count=1
go test ./internal/api/i -run TestDeleteAccount -count=1
go test ./internal/api/admin -run DeleteAccount -count=1
go test ./internal/server -run "TestMkGoRolePolicyKeysAreListedInFrontend|TestCanDeleteAccountIsWiredInSettings" -count=1
go test ./internal/entitycompat -run "TestBundledAssetsPinMatchesDoc|TestSubmodulePin" -count=1
```

Expected: all focused tests PASS.

- [ ] **Step 2: Run repository gates and builds that do not require the unavailable test database**

```powershell
go build ./...
make gates
make frontend-check
git diff --check
```

Expected: PASS. If `make gates` reproduces the known plugin surface golden baseline drift, record the exact failure and run every unaffected gate individually; do not update unrelated golden files in this feature.

- [ ] **Step 3: Confirm scope and history before push**

```powershell
git status --short --branch
git diff origin/Misaki-develop...HEAD --stat
git diff origin/Misaki-develop...HEAD -- . ":(exclude)third_party/misskey"
git diff --submodule=log origin/Misaki-develop...HEAD -- third_party/misskey
git log --oneline -10
```

Confirm there is no `canPurgeAccount`, `canTruncateAccount`, generic endpoint interception API, migration, dependency change, or upstream remote target in the diff.

- [ ] **Step 4: Push only the Misaki backend branch and open the PR**

```powershell
git push -u origin feature/can-delete-account
$issue = gh issue list --repo Misaki-Project/mk --state open --search "CherryPick互換のcanDeleteAccount policyを移植する in:title" --json number,url | ConvertFrom-Json
$frontendPr = gh pr list --repo Misaki-Project/misskey-ts --state merged --search "Add canDeleteAccount frontend controls in:title" --json number,url | ConvertFrom-Json
gh pr create --repo Misaki-Project/mk --base Misaki-develop --head feature/can-delete-account --title "Add canDeleteAccount self-delete policy" --body "Closes $($issue[0].url)`n`nFrontend: $($frontendPr[0].url)`n`n## Summary`n- add canDeleteAccount with default true and EffectivePolicyResolver support`n- fail closed on checked policy resolution before 2FA/password processing`n- pin the Misaki frontend controls and matching bundled assets image`n`n## Verification`n- focused effectivepolicy/core role tests`n- i/delete-account and admin delete-account tests`n- frontend role/UI wiring gates`n- submodule/assets pin gates`n- go build ./...`n- make frontend-check"
```

Expected: one PR under `Misaki-Project/mk`. Return both Misaki PR URLs to the user.

- [ ] **Step 5: Inspect CI without changing upstream repositories**

```powershell
$pr = gh pr list --repo Misaki-Project/mk --state open --head feature/can-delete-account --json number,url | ConvertFrom-Json
gh pr checks --repo Misaki-Project/mk $pr[0].number --watch
```

Expected: required checks pass. Fix only failures caused by this branch; report known baseline/infrastructure failures separately.
