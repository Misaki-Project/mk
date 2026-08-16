# CherryPick Level Role Backend API・Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** mk-goへCherryPick level roleのAPI contract（`admin/roles/create`・`update`の`manualLevel`/`levelPolicies`/`canHideProfileByUser`受理、`admin/roles/change-exp`、`roles/profile-hide`）を追加し、user entityのrole別experience・hide状態、`roles/users`のexperience降順、moderation log、内部event配線、imported data統合E2Eまでを完成させる。

**Architecture:** handler層は既存の`internal/api/admin`/`internal/api/roles`へ追加し、entity層は`PackRole`へ`levelPolicies`/`canHideProfileByUser`を、user entityのrole viewへ`experience`/`isHideProfile`/`canHideProfileByUser`を足す。hide stateの解決はentityの`UserRoleStateLookup`（core/roleのadapterが実装）経由とし、public packではhidden roleを除外し、self-view（`/api/i`）は状態込みを再設定する。`roles/profile-hide`は1h/20回＋1s minIntervalのrate limitを掛け、`admin/roles/change-exp`は`changeExperienceRole`のmoderation logを書く。内部eventは`internal:` pubsubへimmutable payloadをpublishし、subscriberがper-user cacheをinvalidateする。imported data統合E2Eはfresh mk-go schemaにCherryPick shapeのrole dataをSQL seedし、service/handlerを実配線して通しで検証する。

**Tech Stack:** Go 1.26、Echo、GORM、`internal/core/role`（Plan 2）、`internal/entity`、`internal/core/event`（Redis pubsub）、`internal/api/apierr`、`internal/server/middleware`

## Global Constraints

- 保証対象は最終構成の`mk-go + Misaki-Project frontend`のみ。cross-combinationは保証しない。
- CherryPickのAPI contract（request field、permission、error code・error ID、rate limit、response shape）に合わせる。既存manual/conditional roleのresponse shapeと動作を変更しない。
- `canHideProfileByUser`はadditive API fieldとして追加する（既存fieldの削除・改名なし）。
- `roles/profile-hide`は認証済みユーザー本人だけが呼べる。public roleが存在しない場合・assignmentが存在しない場合の両方で`NO_SUCH_ROLE`を返す。`canHideProfileByUser=false`は`CANNOT_HIDE_THIS_ROLE`。
- プロフィール、role badge、public user entityではhidden assignmentを表示しない。本人（`/api/i`・users/show self）とmoderator向け管理response（`admin/show-user`の`roleAssigns`）には状態を含める。
- `experience` responseはCherryPickと同じfield（`currentLevel`/`currentExp`/`nextLevelExp`/`totalExp`/`minLevel`/`maxLevel`）を持つ。最大levelの`nextLevelExp`はnull（CherryPickの`NaN`→JSON null）。
- `roles/users`の`manualLevel`のみexperience降順。`manual`/`conditional`は既存のid keyset順を維持する。
- event payloadはmodel pointerを共有せず、更新後のimmutable valueだけを渡す。
- experience変更は`changeExperienceRole`相当のmoderation logへ記録する。profile hideは本人操作として監査可能な固定category（`hideRoleProfile`）を記録し、公開ログへ内部値を出さない。
- 本番由来ID・件数・path・hash・credentialをfixture、test、report、commitへ出さない。test dataは`synthetic-*`と固定の小さな整数のみ。
- CherryPickの公開固定API error ID（`NO_SUCH_ROLE`等）はcontract testで必要になるchange-exp／profile-hideのみで使う。それ以外のerrorはmk-go既存の汎用IDを使う。
- `Backend PR 3`はdraftとして公開し、frontend fork（`Misaki-Project/misskey-ts`）のPR 3完了後の最終submodule SHAが確定するまでmergeしない。submodule pointerはTask 9（frontend PR 3完了後ゲート）まで更新しない。
- 内部role modelはplugin公開面へ露出しない（`shiroha-a/mk#2585`は将来のplugin化提案。本互換実装はcore機能として進め、plugin公開APIは追加しない）。
- 不正level policy（policyAsLevelの型不一致）はadmin create/updateのwrite-time検証で拒否し、永続化を防ぐ（runtimeはPlan 2でfail-closed）。create/updateは`manualLevel` targetで`levelPolicies`/`policies`(policyAsLevel)を検証してからpersistする。
- commit・PR作成・push・mergeはユーザーの明示的な承認がある場合のみ実行する（リポジトリ規約: Claudeはコミットを自動作成しない）。承認前は検証とステージングに留める。remote設定変更は行わない。
- commit前には`make fmt && make lint`を通すこと。

---

### Task 1: admin/roles create・updateのmanualLevel受理とentity packを追加する

**Files:**
- Modify: `internal/entity/role.go:59-79`（`PackRole`）
- Modify: `internal/entity/role_test.go`
- Modify: `internal/core/role/role_service.go:1159-1173`（`CreateOptions`）と`1112-1152`（`Create`）
- Modify: `internal/api/admin/handler.go:1956-2041`（`RolesCreate`）と`2092-2217`（`RolesUpdate`）
- Modify: `internal/api/admin/handler_test.go`

**Interfaces:**
- Consumes: Plan 1の`model.RoleTargetManualLevel`/`model.Role.LevelPolicies`/`model.Role.CanHideProfileByUser`、Plan 2の`role.ErrInvalidLevelPolicy`。
- Produces: `role.CreateOptions.LevelPolicies datatypes.JSON`、`role.CreateOptions.CanHideProfileByUser bool`。
- Produces: `entity.PackRole`のresponseへ`levelPolicies`（object or `{}`）と`canHideProfileByUser`（bool）を追加。
- Consumers: `roles/list`・`show`、`admin/roles/list`・`show`・`create`。

- [ ] **Step 1: 失敗するtestを書く**

`internal/entity/role_test.go`へ`PackRole`のlevel field検証を追加する。

```go
func TestPackRole_LevelRoleFields(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	r := &model.Role{
		ID: "r1", Name: "Level", Target: model.RoleTargetManualLevel,
		LevelPolicies: datatypes.JSON([]byte(
			`{"baseLevel":10,"experiencePolicies":[{"level":5,"type":"const","base":100}]}`)),
		CanHideProfileByUser: true,
		Policies:             datatypes.JSON([]byte("{}")),
		CondFormula:          datatypes.JSON([]byte("{}")),
	}
	got := PackRole(r, 0, idGen, map[string]any{})
	assert.Equal(t, "manualLevel", got["target"])
	assert.Equal(t, true, got["canHideProfileByUser"])
	lp, ok := got["levelPolicies"].(map[string]any)
	require.True(t, ok, "levelPolicies must be a JSON object")
	assert.Equal(t, float64(10), lp["baseLevel"])
	eps, ok := lp["experiencePolicies"].([]any)
	require.True(t, ok)
	require.Len(t, eps, 1)
	first := eps[0].(map[string]any)
	assert.Equal(t, "const", first["type"])
	assert.Equal(t, float64(100), first["base"])
}

func TestPackRole_LevelPoliciesEmptyObjectForManualRole(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	r := &model.Role{
		ID: "r2", Name: "Plain", Target: model.RoleTargetManual,
		LevelPolicies: datatypes.JSON([]byte("{}")),
		Policies:      datatypes.JSON([]byte("{}")),
		CondFormula:   datatypes.JSON([]byte("{}")),
	}
	got := PackRole(r, 0, idGen, map[string]any{})
	assert.Equal(t, map[string]any{}, got["levelPolicies"], "non-manual role は {} を返す (CherryPick と同じ)")
	assert.Equal(t, false, got["canHideProfileByUser"])
}
```

`internal/api/admin/handler_test.go`へcreate/updateの受理testを追加する。

```go
func TestRolesCreate_ManualLevelAccepted(t *testing.T) {
	h, _, _, roleRepo := newTestHandler(t)
	body := `{"name":"Level","description":"","color":null,"iconUrl":null,"target":"manualLevel",` +
		`"condFormula":{},"isPublic":true,"isModerator":false,"isAdministrator":false,` +
		`"asBadge":true,"canEditMembersByModerator":false,"displayOrder":0,"policies":{},` +
		`"canHideProfileByUser":true,"levelPolicies":{"baseLevel":10,"experiencePolicies":[` +
		`{"level":5,"type":"const","base":100}]}}`
	rec := doPost(h.RolesCreate, body, adminUser)
	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "manualLevel", resp["target"])
	assert.Equal(t, true, resp["canHideProfileByUser"])
	assert.NotNil(t, resp["levelPolicies"])

	var saved *model.Role
	for _, r := range roleRepo.Roles {
		saved = r
	}
	require.NotNil(t, saved)
	assert.Equal(t, model.RoleTargetManualLevel, saved.Target)
	assert.True(t, saved.CanHideProfileByUser)
	require.NotEmpty(t, saved.LevelPolicies)
}

func TestRolesCreate_ManualLevelDefaultsLevelPolicies(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	body := `{"name":"Level","description":"","color":null,"iconUrl":null,"target":"manualLevel",` +
		`"condFormula":{},"isPublic":false,"isModerator":false,"isAdministrator":false,` +
		`"asBadge":false,"canEditMembersByModerator":false,"displayOrder":0,"policies":{}}`
	rec := doPost(h.RolesCreate, body, adminUser)
	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "manualLevel", resp["target"])
	assert.Equal(t, map[string]any{"baseLevel": float64(0), "experiencePolicies": []any{}},
		resp["levelPolicies"], "未指定なら {baseLevel:0, experiencePolicies:[]} を default (CherryPick)")
}

func TestRolesUpdate_AcceptsLevelFields(t *testing.T) {
	h, _, _, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{
		ID: "r1", Name: "Base", Target: model.RoleTargetManual,
		Policies: datatypes.JSON([]byte("{}")), CondFormula: datatypes.JSON([]byte("{}")),
	}
	body := `{"roleId":"r1","levelPolicies":{"baseLevel":5,"experiencePolicies":[` +
		`{"level":2,"type":"linear","base":100,"additional":50}]},"canHideProfileByUser":true}`
	rec := doPost(h.RolesUpdate, body, adminUser)
	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, model.RoleTargetManual, roleRepo.Roles["r1"].Target, "既存 manual role の target は不変")
	assert.True(t, roleRepo.Roles["r1"].CanHideProfileByUser)
	require.NotEmpty(t, roleRepo.Roles["r1"].LevelPolicies)
}

func TestRolesCreate_InvalidTargetStillRejected(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	body := `{"name":"X","description":"","color":null,"iconUrl":null,"target":"bogus",` +
		`"condFormula":{},"isPublic":false,"isModerator":false,"isAdministrator":false,` +
		`"asBadge":false,"canEditMembersByModerator":false,"displayOrder":0,"policies":{}}`
	rec := doPost(h.RolesCreate, body, adminUser)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRolesCreate_InvalidPolicyAsLevelRejected(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	// canPublicNote (bool) の const policyAsLevel に number 5 → write-time で 400。
	body := `{"name":"Level","description":"","color":null,"iconUrl":null,"target":"manualLevel",` +
		`"condFormula":{},"isPublic":false,"isModerator":false,"isAdministrator":false,` +
		`"asBadge":false,"canEditMembersByModerator":false,"displayOrder":0,` +
		`"policies":{"canPublicNote":{"useDefault":true,"priority":1,"value":false,` +
		`"policyAsLevel":[{"level":1,"type":"const","base":5}]}},"levelPolicies":{"baseLevel":0,"experiencePolicies":[]}}`
	rec := doPost(h.RolesCreate, body, adminUser)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	errObj, _ := resp["error"].(map[string]any)
	require.NotNil(t, errObj)
	assert.Equal(t, "INVALID_PARAM", errObj["code"])
}
```

- [ ] **Step 2: testがREDであることを確認する**

Run: `go test ./internal/entity -run TestPackRole_Level -count=1`
Run: `go test ./internal/api/admin -run 'TestRolesCreate_ManualLevel|TestRolesCreate_InvalidTarget|TestRolesUpdate_AcceptsLevelFields' -count=1`

Expected: `levelPolicies`/`canHideProfileByUser`未出力・`manualLevel`未受理でFAILする。

- [ ] **Step 3: 最小実装**

`internal/entity/role.go`の`PackRole`のreturn mapへ追加する。

```go
	// levelPolicies は jsonb をそのまま object として返す (CherryPick は
	// role.levelPolicies を truthy で包むため、空 {} も object として返る)。
	levelPolicies := map[string]any{}
	if len(r.LevelPolicies) > 0 {
		var parsed any
		if json.Unmarshal(r.LevelPolicies, &parsed) == nil {
			if m, ok := parsed.(map[string]any); ok {
				levelPolicies = m
			}
		}
	}
```

return mapへ次を追加する。

```go
		"levelPolicies":          levelPolicies,
		"canHideProfileByUser":   r.CanHideProfileByUser,
```

`internal/core/role/role_service.go`の`CreateOptions`へ追加する。

```go
	LevelPolicies        datatypes.JSON
	CanHideProfileByUser bool
```

`Create`へ追加する（`opts.Policies`の直後）。

```go
	if len(opts.LevelPolicies) > 0 {
		role.LevelPolicies = opts.LevelPolicies
	}
	role.CanHideProfileByUser = opts.CanHideProfileByUser
```

`internal/api/admin/handler.go`の`RolesCreate`のrequest structへ追加する。

```go
		CanHideProfileByUser *bool           `json:"canHideProfileByUser"`
		LevelPolicies        *map[string]any `json:"levelPolicies"`
```

`RolesCreate`のtarget switchへ`manualLevel`を追加する。

```go
	case string(model.RoleTargetManualLevel):
		opts.Target = model.RoleTargetManualLevel
```

`RolesCreate`の`opts`へ設定を追加する（`Policies`のmarshalの後）。

```go
	if req.CanHideProfileByUser != nil {
		opts.CanHideProfileByUser = *req.CanHideProfileByUser
	}
	// CherryPick create.ts: levelPolicies 未指定は {baseLevel:0, experiencePolicies:[]}。
	lp := req.LevelPolicies
	if lp == nil {
		lp = &map[string]any{"baseLevel": float64(0), "experiencePolicies": []any{}}
	}
	if lpb, err := json.Marshal(*lp); err == nil {
		opts.LevelPolicies = lpb
	} else {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "levelPolicies must be a JSON object.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}

	// manualLevel role は policyAsLevel の型を write-time で検証し、不正値を
	// 永続化しない (Plan 2 ValidateManualLevelPolicies)。返す error に識別子は
	// 含めない。
	if opts.Target == model.RoleTargetManualLevel {
		if err := role.ValidateManualLevelPolicies(opts.Policies); err != nil {
			return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid level policy.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
		}
	}
```

`RolesUpdate`のrequest structへ追加する。

```go
		CanHideProfileByUser *bool           `json:"canHideProfileByUser"`
		LevelPolicies        *map[string]any `json:"levelPolicies"`
```

`RolesUpdate`のfield構築へ追加する（`req.Policies`の後）。

```go
	if req.CanHideProfileByUser != nil {
		fields["canHideProfileByUser"] = *req.CanHideProfileByUser
	}
	if req.LevelPolicies != nil {
		lpb, err := json.Marshal(*req.LevelPolicies)
		if err != nil {
			return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "levelPolicies must be a JSON object.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
		}
		fields["levelPolicies"] = lpb
	}
```

`RolesUpdate`のtarget switchへ`manualLevel`を追加する。

```go
	case string(model.RoleTargetManualLevel):
		fields["target"] = model.RoleTargetManualLevel
```

`RolesUpdate`へwrite-time検証を追加する（`fields`構築後、`UpdateFields`前）。対象roleの有効targetがmanualLevelのとき、incoming policiesのpolicyAsLevel型を検証して不正なら400で拒否する。

```go
	// 有効 target が manualLevel になる場合、incoming policies の policyAsLevel
	// を write-time で検証する (Plan 2 ValidateManualLevelPolicies)。
	newTarget := before.Target
	if req.Target != nil {
		newTarget = model.RoleTarget(*req.Target)
	}
	if newTarget == model.RoleTargetManualLevel && req.Policies != nil {
		polJSON, merr := json.Marshal(*req.Policies)
		if merr == nil {
			if verr := role.ValidateManualLevelPolicies(polJSON); verr != nil {
				return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid level policy.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
			}
		}
	}
```

- [ ] **Step 4: testがGREENであることを確認する**

Run: `go test ./internal/entity -run TestPackRole_Level -count=1`
Run: `go test ./internal/api/admin -run 'TestRolesCreate|TestRolesUpdate' -count=1`

Expected: 全testがPASS。

- [ ] **Step 5: 既存non-regressionを確認する**

Run: `go test ./internal/api/admin ./internal/api/roles ./internal/entity -count=1`

Expected: 全てPASS。

- [ ] **Step 6: commit**（ユーザー承認後のみ実行する）

リポジトリ規約に従い、commitはユーザーの明示的な指示がある場合のみ実行する。
```powershell
git add -- internal/entity/role.go internal/entity/role_test.go internal/core/role/role_service.go internal/api/admin/handler.go internal/api/admin/handler_test.go
git diff --cached --check
git commit -m "api: admin/roles create/updateでmanualLevelとlevel fieldを受理する"
```
---

### Task 2: admin/roles/change-exp endpointとmoderation logを追加する

**Files:**
- Modify: `internal/core/moderationlog/types.go:44-49`（`LogChangeExperienceRole`追加）
- Modify: `internal/api/admin/modlog_helpers.go`（`logChangeExperienceRole`追加）
- Modify: `internal/api/admin/handler.go`（`RolesChangeExp`追加）
- Create: `internal/api/admin/change_exp_test.go`

**Interfaces:**
- Consumes: Plan 2の`role.Service.ChangeExperience`/`ChangeExperienceInput`/`ChangeExperienceResult`/`ErrNotAssigned`/`ErrInvalidRoleTarget`/`ErrInvalidExperienceValue`。
- Produces: `POST /api/admin/roles/change-exp` handler。
- Produces: `moderationlog.LogChangeExperienceRole LogType = "changeExperienceRole"`。
- Consumes: CherryPick公開固定error ID（`6503c040-...`=NO_SUCH_ROLE、`558ea170-...`=NO_SUCH_USER、`25b5bc31-...`=ACCESS_DENIED、`a2f3b5c4-...`=INVALID_ROLE_TARGET、`b9060ac7-...`=NOT_ASSIGNED）。
- Consumers: router配線（Task 5）、`roles` admin UI（future frontend）。

- [ ] **Step 1: 失敗するtestを書く**

`internal/api/admin/change_exp_test.go`を新規作成する。`integrationDB`（`federation_admin_integration_test.go`のTestMainが全migration適用済み）を再利用し、`roleService`へ`SetDB`を配線する。

```go
package admin_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	apiadmin "github.com/shiroha-a/mk/internal/api/admin"
	"github.com/shiroha-a/mk/internal/core/role"
	"github.com/shiroha-a/mk/internal/core/signup"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// changeExpEnv bundles a DB-backed change-exp handler.
type changeExpEnv struct {
	h *apiadmin.Handler
}

// newChangeExpEnv wires real user/role/assignment repos + SetDB so
// ChangeExperience runs its transaction, with a mock meta repo that marks
// admin as root (so IsAdministrator bypasses the member-edit gate).
func newChangeExpEnv(t *testing.T) (*changeExpEnv, *model.User, string) {
	t.Helper()
	if integrationDB == nil {
		t.Skip("integrationDB unavailable")
	}
	idGen, _ := id.NewGenerator("aidx")

	userRepo := repository.NewUserRepository(integrationDB)
	roleRepo := repository.NewRoleRepository(integrationDB)
	assignRepo := repository.NewRoleAssignmentRepository(integrationDB)
	metaRepo := testutil.NewMockMetaRepository()

	adminID := "exp_admin_" + idGen.Generate(time.Now())
	metaRepo.Meta = &model.Meta{ID: "x", RootUserID: &adminID}

	signupSvc := signup.NewService(userRepo, metaRepo, idGen)
	roleSvc := role.NewService(roleRepo, assignRepo, metaRepo, idGen)
	roleSvc.SetDB(integrationDB)
	roleSvc.SetUserRepo(userRepo)

	h := apiadmin.NewHandler(signupSvc, roleSvc, metaRepo, userRepo, idGen)

	admin := &model.User{
		ID: adminID, Username: "exp_admin", UsernameLower: "exp_admin",
	}
	require.NoError(t, integrationDB.Create(admin).Error)
	t.Cleanup(func() {
		integrationDB.Exec(`DELETE FROM "moderation_log" WHERE "userId" = ?`, admin.ID)
		integrationDB.Exec(`DELETE FROM "user" WHERE id = ?`, admin.ID)
	})
	return &changeExpEnv{h: h}, admin, adminID
}

// seedChangeExpUser creates the target user row.
func seedChangeExpUser(t *testing.T, id, username string) {
	t.Helper()
	require.NoError(t, integrationDB.Exec(
		`INSERT INTO "user" (id, username, "usernameLower", "avatarDecorations") VALUES (?, ?, ?, '[]') ON CONFLICT DO NOTHING`,
		id, username, username).Error)
	t.Cleanup(func() { integrationDB.Exec(`DELETE FROM "user" WHERE id = ?`, id) })
}

// seedChangeExpRole creates a manualLevel role and returns its id.
func seedChangeExpRole(t *testing.T) string {
	t.Helper()
	now := time.Now()
	roleID := fmt.Sprintf("exp_role_%d", now.UnixNano()%100000)
	require.NoError(t, integrationDB.Exec(`
		INSERT INTO "role" (id, "updatedAt", "lastUsedAt", name, description, target,
			"condFormula", "levelPolicies", "policies")
		VALUES (?, ?, ?, 'exp', '', 'manualLevel', '{}'::jsonb, ?::jsonb, '{}'::jsonb)`,
		roleID, now, now,
		`{"baseLevel":0,"experiencePolicies":[{"level":100,"type":"const","base":100}]}`).Error)
	t.Cleanup(func() {
		integrationDB.Exec(`DELETE FROM "role_assignment" WHERE "roleId" = ?`, roleID)
		integrationDB.Exec(`DELETE FROM "role" WHERE id = ?`, roleID)
	})
	return roleID
}

func TestRolesChangeExp_SetMode(t *testing.T) {
	env, admin, _ := newChangeExpEnv(t)
	roleID := seedChangeExpRole(t)
	seedChangeExpUser(t, "exp_u1", "exp_u1")
	exp := int64(100)
	require.NoError(t, integrationDB.Exec(
		`INSERT INTO "role_assignment" (id, "userId", "roleId", experience) VALUES (?, ?, ?, ?)`,
		"exp_a1", "exp_u1", roleID, exp).Error)
	t.Cleanup(func() { integrationDB.Exec(`DELETE FROM "role_assignment" WHERE id = ?`, "exp_a1") })

	rec := doPost(env.h.RolesChangeExp,
		`{"roleId":"`+roleID+`","userId":"exp_u1","setMode":"add","value":150}`, admin)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var got int64
	require.NoError(t, integrationDB.Raw(
		`SELECT experience FROM "role_assignment" WHERE id = 'exp_a1'`).Row().Scan(&got))
	assert.Equal(t, int64(250), got, "add mode: 100 + 150")

	// レスポンスは UserDetailed shape。
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "exp_u1", resp["id"])
}

func TestRolesChangeExp_MultiplierAndClamp(t *testing.T) {
	env, admin, _ := newChangeExpEnv(t)
	roleID := seedChangeExpRole(t)
	seedChangeExpUser(t, "exp_u2", "exp_u2")
	exp := int64(100)
	require.NoError(t, integrationDB.Exec(
		`INSERT INTO "role_assignment" (id, "userId", "roleId", experience) VALUES (?, ?, ?, ?)`,
		"exp_a2", "exp_u2", roleID, exp).Error)
	t.Cleanup(func() { integrationDB.Exec(`DELETE FROM "role_assignment" WHERE id = ?`, "exp_a2") })

	rec := doPost(env.h.RolesChangeExp,
		`{"roleId":"`+roleID+`","userId":"exp_u2","setMode":"multiplier","value":1.5}`, admin)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var got int64
	require.NoError(t, integrationDB.Raw(
		`SELECT experience FROM "role_assignment" WHERE id = 'exp_a2'`).Row().Scan(&got))
	assert.Equal(t, int64(150), got, "multiplier: floor(100*1.5)")
}

func TestRolesChangeExp_InvalidRoleTarget(t *testing.T) {
	env, admin, _ := newChangeExpEnv(t)
	// manual target の role を作る。
	now := time.Now()
	roleID := "exp_bad_target"
	require.NoError(t, integrationDB.Exec(`
		INSERT INTO "role" (id, "updatedAt", "lastUsedAt", name, description, target,
			"condFormula", "policies")
		VALUES (?, ?, ?, 'bad', '', 'manual', '{}'::jsonb, '{}'::jsonb)`,
		roleID, now, now).Error)
	t.Cleanup(func() { integrationDB.Exec(`DELETE FROM "role" WHERE id = ?`, roleID) })
	seedChangeExpUser(t, "exp_u3", "exp_u3")

	rec := doPost(env.h.RolesChangeExp,
		`{"roleId":"`+roleID+`","userId":"exp_u3","setMode":"set","value":10}`, admin)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	errObj, _ := body["error"].(map[string]any)
	require.NotNil(t, errObj)
	assert.Equal(t, "INVALID_ROLE_TARGET", errObj["code"])
}

func TestRolesChangeExp_NoSuchRole(t *testing.T) {
	env, admin, _ := newChangeExpEnv(t)
	rec := doPost(env.h.RolesChangeExp,
		`{"roleId":"ghost","userId":"x","setMode":"set","value":10}`, admin)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	errObj, _ := body["error"].(map[string]any)
	require.NotNil(t, errObj)
	assert.Equal(t, "NO_SUCH_ROLE", errObj["code"])
}

func TestRolesChangeExp_NoSuchUser(t *testing.T) {
	env, admin, _ := newChangeExpEnv(t)
	roleID := seedChangeExpRole(t)
	rec := doPost(env.h.RolesChangeExp,
		`{"roleId":"`+roleID+`","userId":"ghost_user","setMode":"set","value":10}`, admin)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	errObj, _ := body["error"].(map[string]any)
	require.NotNil(t, errObj)
	assert.Equal(t, "NO_SUCH_USER", errObj["code"])
}

func TestRolesChangeExp_AccessDeniedForNonEditableRole(t *testing.T) {
	env, admin, _ := newChangeExpEnv(t)
	roleID := seedChangeExpRole(t)
	seedChangeExpUser(t, "exp_u4", "exp_u4")
	// root viewer でない moderator として叩くため、role を canEditMembersByModerator=false
	// のままにして非 root の viewer を渡す。非 root viewer は admin でないので deny。
	rec := doPost(env.h.RolesChangeExp,
		`{"roleId":"`+roleID+`","userId":"exp_u4","setMode":"set","value":10}`,
		&model.User{ID: "exp_not_root", Username: "mod", UsernameLower: "mod"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	errObj, _ := body["error"].(map[string]any)
	require.NotNil(t, errObj)
	assert.Equal(t, "ACCESS_DENIED", errObj["code"])
}
```

`internal/core/moderationlog/types.go`のRoles blockへ追加する。

```go
	LogChangeExperienceRole LogType = "changeExperienceRole"
```

`internal/api/admin/modlog_helpers.go`へ追加する。

```go
// logChangeExperienceRole records the CherryPick changeExperienceRole audit
// entry: {roleId, roleName, userId, userUsername, userHost, actionType,
// actionValue, beforeValue, afterValue, note}.
func (h *Handler) logChangeExperienceRole(c echo.Context, role *model.Role, user *model.User, setMode string, value float64, note *string, res *role.ChangeExperienceResult) {
	if h.userRepo == nil || role == nil || user == nil || res == nil {
		return
	}
	info := moderationlog.UserInfo(user)
	info["roleId"] = role.ID
	info["roleName"] = role.Name
	info["actionType"] = setMode
	info["actionValue"] = value
	info["beforeValue"] = res.BeforeValue
	info["afterValue"] = res.AfterValue
	if note != nil {
		info["note"] = *note
	}
	h.logModeration(c, moderationlog.LogChangeExperienceRole, info)
}
```

- [ ] **Step 2: testがREDであることを確認する**

Run: `go test ./internal/api/admin -run TestRolesChangeExp -count=1`

Expected: `RolesChangeExp`未定義でcompile error→FAIL。

- [ ] **Step 3: 最小handlerを実装する**

`internal/api/admin/handler.go`へ`RolesChangeExp`を追加する（`RolesUsers`の後）。

```go
// RolesChangeExp handles POST /api/admin/roles/change-exp.
//
// CherryPick change-exp.ts 互換: moderator 以上、canEditMembersByModerator
// gate、set/add/multiplier、assignForce(default true)、任意 note。
// 成功時は UserDetailed を返す。error ID は CherryPick の公開固定値。
func (h *Handler) RolesChangeExp(c echo.Context) error {
	var req struct {
		RoleID      string   `json:"roleId"`
		UserID      string   `json:"userId"`
		SetMode     string   `json:"setMode"`
		Value       *float64 `json:"value"`
		AssignForce *bool    `json:"assignForce"`
		Note        *string  `json:"note"`
	}
	if err := c.Bind(&req); err != nil || req.RoleID == "" || req.UserID == "" ||
		req.SetMode == "" || req.Value == nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "roleId, userId, setMode and value are required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	var mode role.ExperienceSetMode
	switch req.SetMode {
	case string(role.ExperienceSetModeSet):
		mode = role.ExperienceSetModeSet
	case string(role.ExperienceSetModeAdd):
		mode = role.ExperienceSetModeAdd
	case string(role.ExperienceSetModeMultiplier):
		mode = role.ExperienceSetModeMultiplier
	default:
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "setMode must be 'set', 'add' or 'multiplier'.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}

	r, err := h.roleService.Show(req.RoleID)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("NO_SUCH_ROLE", "No such role.", "6503c040-6af4-4ed9-bf07-f2dd16678eab"))
	}
	if !r.CanEditMembersByModerator {
		if me := middleware.GetUser(c); me == nil || !h.roleService.IsAdministrator(me.ID) {
			return c.JSON(http.StatusBadRequest, apierr.Error("ACCESS_DENIED", "Only administrators can edit members of the role.", "25b5bc31-dc79-4ebd-9bd2-c84978fd052c"))
		}
	}
	user, err := h.userRepo.FindByID(req.UserID)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("NO_SUCH_USER", "No such user.", "558ea170-f653-4700-94d0-5a818371d0df"))
	}
	if r.Target != model.RoleTargetManualLevel {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_ROLE_TARGET", "Invalid role target.", "a2f3b5c4-1d8e-4b0e-9f6c-7a2d3e4f5b6a"))
	}

	assignForce := true
	if req.AssignForce != nil {
		assignForce = *req.AssignForce
	}
	res, err := h.roleService.ChangeExperience(c.Request().Context(), role.ChangeExperienceInput{
		UserID: req.UserID, RoleID: req.RoleID, Mode: mode, Value: *req.Value, AssignForce: assignForce,
	})
	if err != nil {
		switch {
		case errors.Is(err, role.ErrRoleNotFound):
			return c.JSON(http.StatusBadRequest, apierr.Error("NO_SUCH_ROLE", "No such role.", "6503c040-6af4-4ed9-bf07-f2dd16678eab"))
		case errors.Is(err, role.ErrInvalidRoleTarget):
			return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_ROLE_TARGET", "Invalid role target.", "a2f3b5c4-1d8e-4b0e-9f6c-7a2d3e4f5b6a"))
		case errors.Is(err, role.ErrNotAssigned):
			return c.JSON(http.StatusBadRequest, apierr.Error("NOT_ASSIGNED", "Role not assigned.", "b9060ac7-5c94-4da4-9f55-2047c953df44"))
		case errors.Is(err, role.ErrInvalidExperienceValue):
			return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid experience value.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
		default:
			return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
		}
	}

	h.logChangeExperienceRole(c, r, user, req.SetMode, *req.Value, req.Note, res)

	profiles, _ := h.userRepo.FindProfilesByUserIDs([]string{user.ID})
	var profile *model.UserProfile
	if len(profiles) > 0 {
		profile = profiles[0]
	}
	return c.JSON(http.StatusOK, entity.PackUserDetailed(user, profile, h.idGen))
}
```

- [ ] **Step 4: testがGREENであることを確認する**

Run: `go test ./internal/api/admin -run TestRolesChangeExp -count=1`

Expected: 全testがPASS（set/add/multiplierのDB値、INVALID_ROLE_TARGET、NO_SUCH_ROLE、NO_SUCH_USER、ACCESS_DENIED）。

- [ ] **Step 5: modlog検証を追加する**

`change_exp_test.go`へ追加する。

```go
func TestRolesChangeExp_WritesModerationLog(t *testing.T) {
	env, admin, _ := newChangeExpEnv(t)
	// newChangeExpEnv が attachModLog 済み。Snapshot 用に repo を取り直す。
	repo := attachModLog(t, env.h)
	roleID := seedChangeExpRole(t)
	seedChangeExpUser(t, "exp_u5", "exp_u5")
	rec := doPost(env.h.RolesChangeExp,
		`{"roleId":"`+roleID+`","userId":"exp_u5","setMode":"set","value":50,"note":"promotion"}`, admin)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Eventually(t, func() bool { return len(repo.Snapshot()) >= 1 }, time.Second, 10*time.Millisecond)
	logs := repo.Snapshot()
	assert.Equal(t, "changeExperienceRole", logs[len(logs)-1].Type)
	var info map[string]any
	require.NoError(t, json.Unmarshal(logs[len(logs)-1].Info, &info))
	assert.Equal(t, "exp_u5", info["userId"])
	assert.Equal(t, roleID, info["roleId"])
	assert.Equal(t, "set", info["actionType"])
	assert.Equal(t, "promotion", info["note"])
}
```

`attachModLog`は`internal/api/admin/password_reset_test.go:22`に既に定義されている（`modLogServiceSetter`経由で`*apiadmin.Handler`へ配線し、`*testutil.MockModerationLogRepository`を返す）。重複定義しない。

Run: `go test ./internal/api/admin -run TestRolesChangeExp_WritesModerationLog -count=1`

Expected: PASS。

- [ ] **Step 6: commit**（ユーザー承認後のみ実行する）

リポジトリ規約に従い、commitはユーザーの明示的な指示がある場合のみ実行する。
```powershell
git add -- internal/core/moderationlog/types.go internal/api/admin/modlog_helpers.go internal/api/admin/handler.go internal/api/admin/change_exp_test.go
git diff --cached --check
git commit -m "api: admin/roles/change-expとchangeExperienceRole modlogを追加する"
```
---

### Task 3: roles/profile-hide endpointとcore operationを追加する

**Files:**
- Modify: `internal/repository/role.go`（`RoleAssignmentRepository`へ`FindByUserAndRole`/`UpdateIsHideProfile`追加）
- Modify: `internal/testutil/mock_repository.go`（mock実装）
- Create: `internal/core/role/hide_profile.go`
- Modify: `internal/core/role/role_service.go`（`hidePub` field + `SetHideProfileEventPublisher`）
- Modify: `internal/core/moderationlog/types.go`（`LogHideRoleProfile`追加）
- Modify: `internal/api/roles/handler.go`（`ProfileHide` + `SetModLogService`）
- Modify: `internal/server/middleware/ratelimit_defs.go`（`roles/profile-hide`）
- Modify: `internal/api/roles/handler_test.go`

**Interfaces:**
- Consumes: Plan 1の`model.Role.CanHideProfileByUser`/`model.RoleAssignment.IsHideProfile`。
- Produces: `type HideProfileUpdatedPayload struct { AssignmentID string; UserID string; RoleID string; IsHideProfile bool }`
- Produces: `type HideProfileEventPublisher interface { PublishHideProfileUpdated(payload HideProfileUpdatedPayload) error }`
- Produces: `func (s *Service) SetHideProfileEventPublisher(pub HideProfileEventPublisher)`
- Produces: `func (s *Service) HideUserProfileRole(userID, roleID string, isHide bool) error`
- Produces: `POST /api/roles/profile-hide` handler。
- Produces: `moderationlog.LogHideRoleProfile LogType = "hideRoleProfile"`。
- Consumes: CherryPick公開固定error ID（`30aaaee3-4792-48dc-ab0d-cf501a575ac5`=NO_SUCH_ROLE、`a21fb109-1d95-9a10-fe18-42ea7c91dabe`=CANNOT_HIDE_THIS_ROLE）。
- Consumers: router配線（Task 6）、user entity role view（Task 4）。

- [ ] **Step 1: 失敗するtestを書く**

`internal/api/roles/handler_test.go`へ追加する。mock repoへ新methodが必要になるため、先にmockへ追加する。

`internal/testutil/mock_repository.go`の`MockRoleAssignmentRepository`へ追加する。

```go
func (m *MockRoleAssignmentRepository) FindByUserAndRole(userID, roleID string) (*model.RoleAssignment, error) {
	a, ok := m.Assignments[userID+":"+roleID]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return a, nil
}

func (m *MockRoleAssignmentRepository) UpdateIsHideProfile(userID, roleID string, isHide bool) error {
	a, ok := m.Assignments[userID+":"+roleID]
	if !ok {
		return gorm.ErrRecordNotFound
	}
	a.IsHideProfile = &isHide
	return nil
}
```

`internal/api/roles/handler_test.go`へ追加する。assignRepoへのseedが必要なため、assignRepoを返すhelperを追加する（Task 5の`newTestHandlerWithAssign`もこれを使う）。

```go
func newTestHandlerWithAssign(t *testing.T) (*roles.Handler, *testutil.MockRoleRepository, *testutil.MockRoleAssignmentRepository) {
	t.Helper()
	roleRepo := testutil.NewMockRoleRepository()
	assignRepo := testutil.NewMockRoleAssignmentRepository(roleRepo)
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	idGen, _ := id.NewGenerator("aidx")
	svc := corerole.NewService(roleRepo, assignRepo, metaRepo, idGen)
	h := roles.NewHandler(svc, idGen)
	return h, roleRepo, assignRepo
}

func TestProfileHide_EndpointSuccess(t *testing.T) {
	h, roleRepo, assignRepo := newTestHandlerWithAssign(t)
	roleRepo.Roles["r1"] = &model.Role{
		ID: "r1", Name: "Pub", IsPublic: true, CanHideProfileByUser: true,
		Policies: datatypes.JSON([]byte("{}")), CondFormula: datatypes.JSON([]byte("{}")),
	}
	assignRepo.Assignments["me1:r1"] = &model.RoleAssignment{ID: "a1", UserID: "me1", RoleID: "r1"}

	vc := newCtxWithViewer(`{"roleId":"r1","hide":true}`, "me1")
	require.NoError(t, h.ProfileHide(vc.ctx))
	assert.Equal(t, http.StatusNoContent, vc.rec.Code)
	require.NotNil(t, assignRepo.Assignments["me1:r1"].IsHideProfile)
	assert.True(t, *assignRepo.Assignments["me1:r1"].IsHideProfile)
}

func TestProfileHide_NoAssignmentIsNoSuchRole(t *testing.T) {
	h, roleRepo, assignRepo := newTestHandlerWithAssign(t)
	roleRepo.Roles["r1"] = &model.Role{
		ID: "r1", Name: "Pub", IsPublic: true, CanHideProfileByUser: true,
		Policies: datatypes.JSON([]byte("{}")), CondFormula: datatypes.JSON([]byte("{}")),
	}
	assignRepo.Assignments = map[string]*model.RoleAssignment{}

	vc := newCtxWithViewer(`{"roleId":"r1","hide":true}`, "me1")
	_ = h.ProfileHide(vc.ctx)
	assert.Equal(t, http.StatusBadRequest, vc.rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(vc.rec.Body.Bytes(), &body))
	errObj, _ := body["error"].(map[string]any)
	require.NotNil(t, errObj)
	assert.Equal(t, "NO_SUCH_ROLE", errObj["code"], "assignment 不在も NO_SUCH_ROLE")
}
```

`internal/core/role/hide_profile_test.go`を新規作成する。

```go
package role_test

import (
	"testing"

	"github.com/shiroha-a/mk/internal/core/role"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHideUserProfileRole_Success(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Pub", IsPublic: true, CanHideProfileByUser: true}
	assignRepo.Assignments["u1:r1"] = &model.RoleAssignment{ID: "a1", UserID: "u1", RoleID: "r1"}

	require.NoError(t, svc.HideUserProfileRole("u1", "r1", true))
	require.NotNil(t, assignRepo.Assignments["u1:r1"].IsHideProfile)
	assert.True(t, *assignRepo.Assignments["u1:r1"].IsHideProfile)
}

func TestHideUserProfileRole_NoAssignmentReturnsRoleNotFound(t *testing.T) {
	svc, _, assignRepo, _ := newTestService(t)
	assignRepo.Assignments = map[string]*model.RoleAssignment{}
	err := svc.HideUserProfileRole("u1", "r1", true)
	require.Error(t, err)
	assert.ErrorIs(t, err, role.ErrRoleNotFound, "assignment 不在は NO_SUCH_ROLE 相当")
}

func TestHideUserProfileRole_NoChangeWhenAlreadySet(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Pub", IsPublic: true, CanHideProfileByUser: true}
	hide := true
	assignRepo.Assignments["u1:r1"] = &model.RoleAssignment{ID: "a1", UserID: "u1", RoleID: "r1", IsHideProfile: &hide}

	require.NoError(t, svc.HideUserProfileRole("u1", "r1", true))
	assert.True(t, *assignRepo.Assignments["u1:r1"].IsHideProfile)
}

func TestHideUserProfileRole_PublishesImmutableEvent(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Pub", IsPublic: true, CanHideProfileByUser: true}
	assignRepo.Assignments["u1:r1"] = &model.RoleAssignment{ID: "a1", UserID: "u1", RoleID: "r1"}
	pub := &hideCapturePublisher{}
	svc.SetHideProfileEventPublisher(pub)

	require.NoError(t, svc.HideUserProfileRole("u1", "r1", true))
	require.Len(t, pub.payloads, 1)
	assert.Equal(t, "u1", pub.payloads[0].UserID)
	assert.Equal(t, "r1", pub.payloads[0].RoleID)
	assert.True(t, pub.payloads[0].IsHideProfile)
}

type hideCapturePublisher struct {
	payloads []role.HideProfileUpdatedPayload
}

func (p *hideCapturePublisher) PublishHideProfileUpdated(payload role.HideProfileUpdatedPayload) error {
	p.payloads = append(p.payloads, payload)
	return nil
}
```

`internal/api/roles/handler_test.go`へhandler error系testを追加する。

```go
func TestProfileHide_NoSuchRole(t *testing.T) {
	h, _ := newTestHandler(t)
	vc := newCtxWithViewer(`{"roleId":"ghost","hide":true}`, "me1")
	_ = h.ProfileHide(vc.ctx)
	assert.Equal(t, http.StatusBadRequest, vc.rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(vc.rec.Body.Bytes(), &body))
	errObj, _ := body["error"].(map[string]any)
	require.NotNil(t, errObj)
	assert.Equal(t, "NO_SUCH_ROLE", errObj["code"])
}

func TestProfileHide_NonPublicRoleIsNoSuchRole(t *testing.T) {
	h, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Priv", IsPublic: false, CanHideProfileByUser: true}
	vc := newCtxWithViewer(`{"roleId":"r1","hide":true}`, "me1")
	_ = h.ProfileHide(vc.ctx)
	assert.Equal(t, http.StatusBadRequest, vc.rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(vc.rec.Body.Bytes(), &body))
	errObj, _ := body["error"].(map[string]any)
	require.NotNil(t, errObj)
	assert.Equal(t, "NO_SUCH_ROLE", errObj["code"])
}

func TestProfileHide_CannotHideThisRole(t *testing.T) {
	h, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Pub", IsPublic: true, CanHideProfileByUser: false}
	vc := newCtxWithViewer(`{"roleId":"r1","hide":true}`, "me1")
	_ = h.ProfileHide(vc.ctx)
	assert.Equal(t, http.StatusBadRequest, vc.rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(vc.rec.Body.Bytes(), &body))
	errObj, _ := body["error"].(map[string]any)
	require.NotNil(t, errObj)
	assert.Equal(t, "CANNOT_HIDE_THIS_ROLE", errObj["code"])
}
```

- [ ] **Step 2: testがREDであることを確認する**

Run: `go test ./internal/core/role -run TestHideUserProfileRole -count=1`
Run: `go test ./internal/api/roles -run TestProfileHide -count=1`

Expected: `HideUserProfileRole`/`ProfileHide`未定義でFAILする。

- [ ] **Step 3: 最小実装**

`internal/repository/role.go`の`RoleAssignmentRepository`interfaceへ追加する。

```go
	// FindByUserAndRole returns the assignment row without an expiry filter
	// (used by profile-hide, mirroring CherryPick findOneBy({userId, roleId})).
	FindByUserAndRole(userID, roleID string) (*model.RoleAssignment, error)
	// UpdateIsHideProfile flips the profile-badge visibility flag.
	UpdateIsHideProfile(userID, roleID string, isHide bool) error
```

`roleAssignmentRepository`へ実装を追加する。

```go
func (r *roleAssignmentRepository) FindByUserAndRole(userID, roleID string) (*model.RoleAssignment, error) {
	var a model.RoleAssignment
	if err := r.db.Where("\"userId\" = ? AND \"roleId\" = ?", userID, roleID).First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *roleAssignmentRepository) UpdateIsHideProfile(userID, roleID string, isHide bool) error {
	return r.db.Model(&model.RoleAssignment{}).
		Where("\"userId\" = ? AND \"roleId\" = ?", userID, roleID).
		Update("isHideProfile", isHide).Error
}
```

`internal/core/role/hide_profile.go`を新規作成する。

```go
package role

import (
	"errors"
	"log/slog"

	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
)

// HideProfileUpdatedPayload is the immutable snapshot published after a
// profile-badge hide change. Value struct: no model pointers are shared.
type HideProfileUpdatedPayload struct {
	AssignmentID string
	UserID       string
	RoleID       string
	IsHideProfile bool
}

// HideProfileEventPublisher emits the post-commit hide event. The router
// wires a Redis-backed implementation; tests use a capturing stub.
type HideProfileEventPublisher interface {
	PublishHideProfileUpdated(payload HideProfileUpdatedPayload) error
}

// HideUserProfileRole updates the assignment's profile-badge visibility.
// Mirrors CherryPick RoleService.hideUserProfileRole; a missing assignment
// returns ErrRoleNotFound (the public endpoint maps it to NO_SUCH_ROLE).
// The per-user role cache is invalidated and the immutable event published
// after the write.
func (s *Service) HideUserProfileRole(userID, roleID string, isHide bool) error {
	assign, err := s.assignmentRepo.FindByUserAndRole(userID, roleID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrRoleNotFound
		}
		return err
	}
	if assign.IsHideProfile != nil && *assign.IsHideProfile == isHide {
		return nil
	}
	if err := s.assignmentRepo.UpdateIsHideProfile(userID, roleID, isHide); err != nil {
		return err
	}
	s.InvalidateUserRoleCache(userID)
	if s.hidePub != nil {
		payload := HideProfileUpdatedPayload{
			AssignmentID: assign.ID, UserID: userID, RoleID: roleID, IsHideProfile: isHide,
		}
		if perr := s.hidePub.PublishHideProfileUpdated(payload); perr != nil {
			slog.Warn("role: publish hide profile updated failed",
				"roleId", roleID, "userId", userID, "err", perr)
		}
	}
	return nil
}
```

`internal/core/role/role_service.go`の`Service` structへ追加し、setterを実装する。

```go
	// hidePub は profile badge 表示切替後の内部 event publisher (nil なら発行しない)。
	hidePub HideProfileEventPublisher
```

```go
// SetHideProfileEventPublisher wires the post-commit hide event publisher
// used by HideUserProfileRole. Optional: nil disables publishing.
func (s *Service) SetHideProfileEventPublisher(pub HideProfileEventPublisher) {
	s.hidePub = pub
}
```

`internal/core/moderationlog/types.go`のRoles blockへ追加する。

```go
	// LogHideRoleProfile は profile role badge 表示切替 (本人操作)。公開ログへ
	// 内部値は出さない固定category。
	LogHideRoleProfile LogType = "hideRoleProfile"
```

`internal/api/roles/handler.go`の`Handler` structへ`modLogService`を追加する。

```go
	// modLogService は profile hide の本人操作監査記録用 (nil なら記録しない)。
	modLogService *moderationlog.Service
```

```go
// SetModLogService wires the moderation log service used to record the
// profile-hide audit entry (fixed category, no internal values).
func (h *Handler) SetModLogService(s *moderationlog.Service) {
	h.modLogService = s
}
```

`internal/api/roles/handler.go`へ`ProfileHide`を追加する。

```go
// ProfileHide handles POST /api/roles/profile-hide.
//
// CherryPick profile-hide.ts 互換: 認証済み本人だけが呼べる (RequireAuth)。
// public role が無い場合と assignment が無い場合の両方で NO_SUCH_ROLE、
// canHideProfileByUser=false は CANNOT_HIDE_THIS_ROLE。成功時 204。
func (h *Handler) ProfileHide(c echo.Context) error {
	var req struct {
		RoleID string `json:"roleId"`
		Hide   bool   `json:"hide"`
	}
	if err := c.Bind(&req); err != nil || req.RoleID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "roleId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	me := middleware.GetUser(c)
	if me == nil {
		return c.JSON(http.StatusUnauthorized, apierr.Error("UNAUTHORIZED", "You are not signed in.", "09d497ab-3b8b-4a2d-a32e-f94e68f10dfa"))
	}
	r, err := h.roleService.Show(req.RoleID)
	if err != nil || !r.IsPublic {
		return c.JSON(http.StatusBadRequest, apierr.Error("NO_SUCH_ROLE", "No such role.", "30aaaee3-4792-48dc-ab0d-cf501a575ac5"))
	}
	if !r.CanHideProfileByUser {
		return c.JSON(http.StatusBadRequest, apierr.Error("CANNOT_HIDE_THIS_ROLE", "This role cannot be hidden.", "a21fb109-1d95-9a10-fe18-42ea7c91dabe"))
	}
	if err := h.roleService.HideUserProfileRole(me.ID, req.RoleID, req.Hide); err != nil {
		if errors.Is(err, role.ErrRoleNotFound) {
			return c.JSON(http.StatusBadRequest, apierr.Error("NO_SUCH_ROLE", "No such role.", "30aaaee3-4792-48dc-ab0d-cf501a575ac5"))
		}
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	h.logHideRoleProfile(c, me.ID, req.RoleID, req.Hide)
	return c.NoContent(http.StatusNoContent)
}

// logHideRoleProfile records the self-service hide action under a fixed
// audit category. Only the operation parameters (userId / roleId / hide) are
// logged; no internal DB values.
func (h *Handler) logHideRoleProfile(c echo.Context, userID, roleID string, hide bool) {
	if h.modLogService == nil {
		return
	}
	actor := middleware.GetUser(c)
	if actor == nil {
		return
	}
	h.modLogService.Log(c.Request().Context(), actor.ID, moderationlog.LogHideRoleProfile, map[string]any{
		"userId": userID,
		"roleId": roleID,
		"hide":   hide,
	})
}
```

importへ`errors`と`github.com/shiroha-a/mk/internal/core/moderationlog`を追加する。

`internal/server/middleware/ratelimit_defs.go`のMisc blockへ追加する。

```go
	"roles/profile-hide": {Duration: time.Hour, Max: 20, MinInterval: time.Second},
```

- [ ] **Step 4: testがGREENであることを確認する**

Run: `go test ./internal/core/role -run TestHideUserProfileRole -count=1`
Run: `go test ./internal/api/roles -run TestProfileHide -count=1`

Expected: 全testがPASS。

- [ ] **Step 5: 既存non-regressionを確認する**

Run: `go test ./internal/core/role ./internal/api/roles ./internal/repository -count=1`

Expected: 全てPASS（interface追加によるmock/実装の整合）。

- [ ] **Step 6: commit**（ユーザー承認後のみ実行する）

リポジトリ規約に従い、commitはユーザーの明示的な指示がある場合のみ実行する。
```powershell
git add -- internal/repository/role.go internal/testutil/mock_repository.go internal/core/role/hide_profile.go internal/core/role/role_service.go internal/core/moderationlog/types.go internal/api/roles/handler.go internal/api/roles/handler_test.go internal/core/role/hide_profile_test.go internal/server/middleware/ratelimit_defs.go
git diff --cached --check
git commit -m "api: roles/profile-hideとhideRoleProfile監査記録を追加する"
```
---

### Task 4: user entityのrole経験値・hide状態とadmin/show-user roleAssignsを実装する

**Files:**
- Modify: `internal/model/role.go`（`UserRoleExperience`追加）
- Create: `internal/core/role/user_roles_state.go`（`UserRolesStateAdapter` + `Service.GetUserRoleStates`）
- Create: `internal/core/role/user_roles_state_test.go`
- Modify: `internal/entity/user_roles.go`（`UserRoleStateLookup` + `packPublicRoles`/`packBadgeRoles`更新 + `PackSelfPublicRoles`）
- Modify: `internal/entity/user_roles_test.go`
- Modify: `internal/api/admin/handler.go`（`packUserRoleAssigns`へ`experience`/`isHideProfile`）
- Modify: `internal/api/admin/handler_test.go`
- Modify: `internal/api/i/handler.go`（self-view Roles enrichment）

**Interfaces:**
- Consumes: Plan 2の`EvaluateLevel`、Task 3の`model.RoleAssignment.IsHideProfile`、Plan 1の`model.ParseLevelPolicies`。
- Produces: `model.UserRoleExperience struct { CurrentLevel int; CurrentExp int64; NextLevelExp *int64; TotalExp int64; MinLevel int; MaxLevel int; IsHideProfile bool }`
- Produces: `func (s *Service) GetUserRoleStates(userID string) (map[string]model.UserRoleExperience, error)`
- Produces: `entity.UserRoleStateLookup` interface + `SetUserRoleStateLookup` + `entity.PackSelfPublicRoles(u *model.User) []any`
- Produces: `admin/show-user`の`roleAssigns[].experience`（number or omitted）と`roleAssigns[].isHideProfile`（bool or omitted）。
- Consumers: `roles` field of UserDetailed/MeDetailed、`badgeRoles`、`/api/i`、`/api/users/show` self-view。

- [ ] **Step 1: 失敗するtestを書く**

`internal/core/role/user_roles_state_test.go`を新規作成する。

```go
package role_test

import (
	"testing"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

func TestGetUserRoleStates_ManualLevelExperience(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{
		ID: "r1", Name: "Level", Target: model.RoleTargetManualLevel,
		LevelPolicies: datatypes.JSON([]byte(
			`{"baseLevel":0,"experiencePolicies":[{"level":100,"type":"const","base":100}]}`)),
	}
	exp := int64(250)
	assignRepo.Assignments["u1:r1"] = &model.RoleAssignment{
		ID: "a1", UserID: "u1", RoleID: "r1", Experience: &exp,
	}

	states, err := svc.GetUserRoleStates("u1")
	require.NoError(t, err)
	st, ok := states["r1"]
	require.True(t, ok)
	assert.Equal(t, 2, st.CurrentLevel)
	assert.Equal(t, int64(50), st.CurrentExp)
	require.NotNil(t, st.NextLevelExp)
	assert.Equal(t, int64(100), *st.NextLevelExp)
	assert.Equal(t, int64(250), st.TotalExp)
	assert.Equal(t, 0, st.MinLevel)
	assert.Equal(t, 100, st.MaxLevel)
	assert.False(t, st.IsHideProfile)
}

func TestGetUserRoleStates_IsHideProfileAndMaxLevel(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{
		ID: "r1", Name: "Level", Target: model.RoleTargetManualLevel,
		LevelPolicies: datatypes.JSON([]byte(
			`{"baseLevel":0,"experiencePolicies":[{"level":2,"type":"const","base":100}]}`)),
	}
	exp := int64(200) // 最大 level
	hide := true
	assignRepo.Assignments["u1:r1"] = &model.RoleAssignment{
		ID: "a1", UserID: "u1", RoleID: "r1", Experience: &exp, IsHideProfile: &hide,
	}
	// non-manual hideable role も状態に入る (hide のみ、experience なし)。
	roleRepo.Roles["r2"] = &model.Role{ID: "r2", Name: "Manual", Target: model.RoleTargetManual}
	hide2 := true
	assignRepo.Assignments["u1:r2"] = &model.RoleAssignment{
		ID: "a2", UserID: "u1", RoleID: "r2", IsHideProfile: &hide2,
	}

	states, err := svc.GetUserRoleStates("u1")
	require.NoError(t, err)
	st := states["r1"]
	assert.Equal(t, 2, st.CurrentLevel)
	assert.True(t, st.IsHideProfile)
	assert.Nil(t, st.NextLevelExp, "最大 level では next なし")
	assert.True(t, states["r2"].IsHideProfile)
}
```

`internal/entity/user_roles_test.go`へ追加する。

```go
// stubStateLookup implements UserRoleStateLookup for tests.
type stubStateLookup struct {
	states map[string]map[string]model.UserRoleExperience
}

func (s *stubStateLookup) LookupUserRoleStates(userID string) map[string]model.UserRoleExperience {
	return s.states[userID]
}

func TestPackPublicRoles_ExcludesHiddenAndCarriesExperience(t *testing.T) {
	t.Cleanup(func() { SetUserRolesLookup(nil); SetUserRoleStateLookup(nil) })
	SetUserRolesLookup(&stubRolesLookup{roles: map[string][]*model.Role{
		"u1": {
			{ID: "rShow", Name: "Show", IsPublic: true, CanHideProfileByUser: true, DisplayOrder: 5},
			{ID: "rHide", Name: "Hide", IsPublic: true, CanHideProfileByUser: true, DisplayOrder: 3},
		},
	}})
	next := int64(100)
	SetUserRoleStateLookup(&stubStateLookup{states: map[string]map[string]model.UserRoleExperience{
		"u1": {
			"rShow": {CurrentLevel: 2, CurrentExp: 50, NextLevelExp: &next, TotalExp: 250, MinLevel: 0, MaxLevel: 5},
			"rHide": {IsHideProfile: true},
		},
	}})
	got := packPublicRoles(&model.User{ID: "u1"})
	require.Len(t, got, 1, "hidden role は公開一覧から除外される")
	entry := got[0].(map[string]any)
	assert.Equal(t, "rShow", entry["id"])
	assert.Equal(t, true, entry["canHideProfileByUser"])
	assert.Equal(t, false, entry["isHideUserProfile"])
	exp, ok := entry["experience"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(2), exp["currentLevel"])
	assert.Equal(t, float64(50), exp["currentExp"])
	assert.Equal(t, float64(100), exp["nextLevelExp"])
	assert.Equal(t, float64(250), exp["totalExp"])
	assert.Equal(t, float64(0), exp["minLevel"])
	assert.Equal(t, float64(5), exp["maxLevel"])
}

func TestPackSelfPublicRoles_IncludesHiddenWithState(t *testing.T) {
	t.Cleanup(func() { SetUserRolesLookup(nil); SetUserRoleStateLookup(nil) })
	SetUserRolesLookup(&stubRolesLookup{roles: map[string][]*model.Role{
		"u1": {
			{ID: "rHide", Name: "Hide", IsPublic: true, CanHideProfileByUser: true, DisplayOrder: 3},
		},
	}})
	SetUserRoleStateLookup(&stubStateLookup{states: map[string]map[string]model.UserRoleExperience{
		"u1": {"rHide": {IsHideProfile: true}},
	}})
	got := PackSelfPublicRoles(&model.User{ID: "u1"})
	require.Len(t, got, 1, "本人 view は hidden も含む")
	entry := got[0].(map[string]any)
	assert.Equal(t, "rHide", entry["id"])
	assert.Equal(t, true, entry["isHideUserProfile"])
}

func TestPackBadgeRoles_ExcludesHidden(t *testing.T) {
	t.Cleanup(func() { SetUserRolesLookup(nil); SetUserRoleStateLookup(nil) })
	SetUserRolesLookup(&stubRolesLookup{roles: map[string][]*model.Role{
		"u1": {
			{ID: "r1", Name: "Badge", AsBadge: true, IsPublic: true, DisplayOrder: 5},
		},
	}})
	SetUserRoleStateLookup(&stubStateLookup{states: map[string]map[string]model.UserRoleExperience{
		"u1": {"r1": {IsHideProfile: true}},
	}})
	got := packBadgeRoles(&model.User{ID: "u1"})
	require.NotNil(t, got)
	assert.Empty(t, *got, "hidden role は badge にも出さない")
}
```

`internal/api/admin/handler_test.go`へ追加する（`TestShowUser_WithRoleAssigns`と同じpattern）。

```go
func TestShowUser_RoleAssignsIncludesExperienceAndHide(t *testing.T) {
	h, userRepo, _, roleRepo, assignRepo := newTestHandlerWithAssign(t)
	idGen, _ := id.NewGenerator("aidx")
	uid := idGen.Generate(time.Now())
	userRepo.Users[uid] = &model.User{ID: uid, Username: "test", AvatarDecorations: []byte("[]")}
	userRepo.Profiles[uid] = &model.UserProfile{
		UserID: uid, MutedWords: []byte("[]"), HardMutedWords: []byte("[]"), MutedInstances: []byte("[]"),
	}
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Level", Target: model.RoleTargetManualLevel}
	exp := int64(250)
	hide := true
	aid := idGen.Generate(time.Now())
	assignRepo.Assignments[uid+":r1"] = &model.RoleAssignment{
		ID: aid, UserID: uid, RoleID: "r1", Experience: &exp, IsHideProfile: &hide,
	}

	rec := doPost(h.ShowUser, `{"userId":"`+uid+`"}`, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assigns, ok := resp["roleAssigns"].([]any)
	require.True(t, ok)
	require.Len(t, assigns, 1)
	entry := assigns[0].(map[string]any)
	assert.Equal(t, float64(250), entry["experience"])
	assert.Equal(t, true, entry["isHideProfile"])
	assert.Equal(t, "r1", entry["roleId"])
}
```

- [ ] **Step 2: testがREDであることを確認する**

Run: `go test ./internal/core/role -run TestGetUserRoleStates -count=1`
Run: `go test ./internal/entity -run 'TestPackPublicRoles_Excludes|TestPackSelfPublicRoles|TestPackBadgeRoles_Excludes' -count=1`

Expected: `GetUserRoleStates`/`SetUserRoleStateLookup`/`PackSelfPublicRoles`未定義でFAILする。

- [ ] **Step 3: 最小実装**

`internal/model/role.go`へ追加する。

```go
// UserRoleExperience is the per-role level + hide state attached to the
// user entity's roles field. NextLevelExp is nil at max level (JSON null).
type UserRoleExperience struct {
	CurrentLevel  int
	CurrentExp    int64
	NextLevelExp  *int64
	TotalExp      int64
	MinLevel      int
	MaxLevel      int
	IsHideProfile bool
}
```

`internal/core/role/user_roles_state.go`を新規作成する。

```go
package role

import (
	"log/slog"

	"github.com/shiroha-a/mk/internal/model"
)

// UserRolesStateAdapter implements entity.UserRoleStateLookup so the entity
// packers can enrich the roles field with level experience and hide state
// without entity depending on core/role (layering: api → core → entity)。
type UserRolesStateAdapter struct {
	provider UserRolesStateProvider
}

// UserRolesStateProvider is the subset of role.Service used by the adapter.
type UserRolesStateProvider interface {
	GetUserRoleStates(userID string) (map[string]model.UserRoleExperience, error)
}

// NewUserRolesStateAdapter returns an entity.UserRoleStateLookup backed by
// the role service. Pass nil provider for an adapter that yields nil.
func NewUserRolesStateAdapter(provider UserRolesStateProvider) *UserRolesStateAdapter {
	return &UserRolesStateAdapter{provider: provider}
}

// LookupUserRoleStates delegates to the role service. Errors are logged and
// nil returned so the packer falls back to the pre-level-role output.
func (a *UserRolesStateAdapter) LookupUserRoleStates(userID string) map[string]model.UserRoleExperience {
	if a == nil || a.provider == nil {
		return nil
	}
	states, err := a.provider.GetUserRoleStates(userID)
	if err != nil {
		slog.Warn("user role states lookup: GetUserRoleStates failed", "userID", userID, "err", err)
		return nil
	}
	return states
}

// GetUserRoleStates resolves each active assignment's hide state and, for
// manualLevel roles, its level experience. It is keyed by roleID.
func (s *Service) GetUserRoleStates(userID string) (map[string]model.UserRoleExperience, error) {
	if userID == "" {
		return nil, nil
	}
	assigns, err := s.assignmentRepo.ListByUser(userID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]model.UserRoleExperience, len(assigns))
	for _, a := range assigns {
		if a == nil || a.Role == nil {
			continue
		}
		st := model.UserRoleExperience{
			IsHideProfile: a.IsHideProfile != nil && *a.IsHideProfile,
		}
		if a.Role.Target == model.RoleTargetManualLevel && len(a.Role.LevelPolicies) > 0 {
			if lp, err := model.ParseLevelPolicies(a.Role.LevelPolicies); err == nil {
				if lvl, err := EvaluateLevel(experienceOf(a), lp.BaseLevel, levelInputsFrom(lp)); err == nil {
					st.CurrentLevel = lvl.Level
					st.CurrentExp = lvl.CurrentExp
					st.NextLevelExp = lvl.NextLevelExp
					st.TotalExp = lvl.TotalExp
					st.MinLevel = lvl.MinLevel
					st.MaxLevel = lvl.MaxLevel
				}
			}
		}
		out[a.RoleID] = st
	}
	return out, nil
}

func experienceOf(a *model.RoleAssignment) int64 {
	if a.Experience != nil {
		return *a.Experience
	}
	return 0
}

func levelInputsFrom(lp *model.LevelPolicies) []LevelPolicyInput {
	inputs := make([]LevelPolicyInput, 0, len(lp.ExperiencePolicies))
	for _, p := range lp.ExperiencePolicies {
		inputs = append(inputs, LevelPolicyInput{
			Level: p.Level, Type: p.Type, Base: p.Base,
			Additional: derefFloat(p.Additional), Exponential: derefFloat(p.Exponential),
		})
	}
	return inputs
}
```

`internal/entity/user_roles.go`へ追加する。

```go
// UserRoleStateLookup resolves per-role hide state and level experience for
// the roles / badgeRoles fields. Mirrors upstream where getUserRoles returns
// roles already carrying isHideProfile and experience.
type UserRoleStateLookup interface {
	LookupUserRoleStates(userID string) map[string]model.UserRoleExperience
}

var (
	userRoleStateMu      sync.RWMutex
	userRoleStateLookup  UserRoleStateLookup
)

// SetUserRoleStateLookup wires the per-role state lookup used by
// packPublicRoles / packBadgeRoles / PackSelfPublicRoles. Pass nil to clear.
func SetUserRoleStateLookup(l UserRoleStateLookup) {
	userRoleStateMu.Lock()
	userRoleStateLookup = l
	userRoleStateMu.Unlock()
}

func resolveUserRoleStates(userID string) map[string]model.UserRoleExperience {
	userRoleStateMu.RLock()
	l := userRoleStateLookup
	userRoleStateMu.RUnlock()
	if l == nil {
		return nil
	}
	return l.LookupUserRoleStates(userID)
}
```

`packPublicRoles`を更新する（hidden除外＋`canHideProfileByUser`/`isHideUserProfile`/`experience`追加）。

```go
func packPublicRoles(u *model.User) []any {
	out := []any{}
	if u == nil {
		return out
	}
	roles := resolveUserRolesSorted(u.ID)
	states := resolveUserRoleStates(u.ID)
	for _, r := range roles {
		if r == nil || !r.IsPublic {
			continue
		}
		st, hasState := states[r.ID]
		if hasState && st.IsHideProfile {
			continue // hidden assignment は公開 role 一覧から除外 (spec)
		}
		entry := map[string]any{
			"id":                   r.ID,
			"name":                 r.Name,
			"color":                r.Color,
			"iconUrl":              ProxyMediaURLPtr(r.IconURL),
			"description":          r.Description,
			"isModerator":          r.IsModerator,
			"isAdministrator":      r.IsAdministrator,
			"displayOrder":         r.DisplayOrder,
			"canHideProfileByUser": r.CanHideProfileByUser,
		}
		if hasState {
			entry["isHideUserProfile"] = st.IsHideProfile
			entry["experience"] = map[string]any{
				"currentLevel": st.CurrentLevel,
				"currentExp":   st.CurrentExp,
				"nextLevelExp": st.NextLevelExp,
				"totalExp":     st.TotalExp,
				"minLevel":     st.MinLevel,
				"maxLevel":     st.MaxLevel,
			}
		}
		out = append(out, entry)
	}
	return out
}
```

`packBadgeRoles`のループへhidden除外を追加する。

```go
	states := resolveUserRoleStates(u.ID)
	for _, r := range roles {
		if r == nil || !r.AsBadge || !r.IsPublic {
			continue
		}
		if st, ok := states[r.ID]; ok && st.IsHideProfile {
			continue
		}
		out = append(out, map[string]any{ ... })
	}
```

`PackSelfPublicRoles`を追加する（本人view: hiddenも含め、`isHideUserProfile`で状態を伝える）。

```go
// PackSelfPublicRoles builds the roles array for the user themself: hidden
// roles are included with isHideUserProfile=true so the frontend can render
// the unhide toggle. Used by /api/i and users/show self-view.
func PackSelfPublicRoles(u *model.User) []any {
	out := []any{}
	if u == nil {
		return out
	}
	roles := resolveUserRolesSorted(u.ID)
	states := resolveUserRoleStates(u.ID)
	for _, r := range roles {
		if r == nil || !r.IsPublic {
			continue
		}
		st, hasState := states[r.ID]
		entry := map[string]any{
			"id":                   r.ID,
			"name":                 r.Name,
			"color":                r.Color,
			"iconUrl":              ProxyMediaURLPtr(r.IconURL),
			"description":          r.Description,
			"isModerator":          r.IsModerator,
			"isAdministrator":      r.IsAdministrator,
			"displayOrder":         r.DisplayOrder,
			"canHideProfileByUser": r.CanHideProfileByUser,
		}
		if hasState {
			entry["isHideUserProfile"] = st.IsHideProfile
			entry["experience"] = map[string]any{
				"currentLevel": st.CurrentLevel,
				"currentExp":   st.CurrentExp,
				"nextLevelExp": st.NextLevelExp,
				"totalExp":     st.TotalExp,
				"minLevel":     st.MinLevel,
				"maxLevel":     st.MaxLevel,
			}
		}
		out = append(out, entry)
	}
	return out
}
```

`internal/entity/user_roles.go`のimportへ`model`を追加する。

`internal/api/admin/handler.go`の`packUserRoleAssigns`へ追加する。

```go
		entry := map[string]any{
			"roleId":    a.RoleID,
			"expiresAt": nil,
		}
		if a.Experience != nil {
			entry["experience"] = *a.Experience
		}
		if a.IsHideProfile != nil {
			entry["isHideProfile"] = *a.IsHideProfile
		}
```

- [ ] **Step 4: testがGREENであることを確認する**

Run: `go test ./internal/core/role -run TestGetUserRoleStates -count=1`
Run: `go test ./internal/entity -run 'TestPackPublicRoles|TestPackSelfPublicRoles|TestPackBadgeRoles|TestPackUserDetailed|TestPackUserLite' -count=1`

Expected: 全testがPASS。既存exact-field assertに影響がないこと（追加fieldはkeyed assertで破壊しない）。

- [ ] **Step 5: self-viewのenrichmentを配線する**

`internal/api/i/handler.go`のMe handlerで、`PackMeDetailed`後の`resp.Roles`を`entity.PackSelfPublicRoles(user)`で上書きする（handler層でviewer=本人を確定できるため）。

```go
	// self-view は hidden role も含め、isHideUserProfile で状態を伝える。
	resp.Roles = entity.PackSelfPublicRoles(user)
```

`internal/api/users/handler.go`のself-view分岐（`me != nil && me.ID == user.ID`）でも同様に`packed.Roles = entity.PackSelfPublicRoles(user)`を設定する。adminの`/api/i` responseに`roleAssigns`（experience/isHideProfile）が入ることを確認するtestを`internal/api/admin/handler_test.go`の`TestShowUser_WithRoleAssigns`へ追加する。

- [ ] **Step 6: 既存non-regressionを確認する**

Run: `go test ./internal/entity ./internal/core/role ./internal/api/admin ./internal/api/i ./internal/api/users -count=1`

Expected: 全てPASS。

- [ ] **Step 7: commit**（ユーザー承認後のみ実行する）

リポジトリ規約に従い、commitはユーザーの明示的な指示がある場合のみ実行する。
```powershell
git add -- internal/model/role.go internal/core/role/user_roles_state.go internal/core/role/user_roles_state_test.go internal/entity/user_roles.go internal/entity/user_roles_test.go internal/api/admin/handler.go internal/api/admin/handler_test.go internal/api/i/handler.go internal/api/users/handler.go
git diff --cached --check
git commit -m "entity: user role経験値・hide状態をroles fieldへ反映する"
```
---

### Task 5: roles/usersのmanualLevel experience降順を実装する

**Files:**
- Modify: `internal/repository/role.go`（`RoleAssignmentRepository`へ`ListByRoleExperienceDesc`追加）
- Modify: `internal/repository/role_test.go`
- Modify: `internal/testutil/mock_repository.go`（mock実装）
- Modify: `internal/core/role/role_service.go`（`ListByRoleExperienceDesc`）
- Modify: `internal/api/roles/handler.go`（`Users`分岐）
- Modify: `internal/api/roles/handler_test.go`

**Interfaces:**
- Consumes: Plan 1の`model.RoleTargetManualLevel`。
- Produces: `func (s *Service) ListByRoleExperienceDesc(roleID, untilID, sinceID string, limit int) ([]*model.RoleAssignment, error)`
- Produces: `repository.RoleAssignmentRepository.ListByRoleExperienceDesc(...)` — `"experience" DESC NULLS FIRST, id DESC`順。
- Consumers: public `roles/users` handler（manualLevelのみ）。
- 適用範囲: experience降順はpublic `roles/users`（`internal/api/roles/handler.go`）だけ。`admin/roles/users`（`internal/api/admin/handler.go`）は既存のid keyset順を維持し変更しない（CherryPick admin users.tsにもordering分岐は無い）。`admin/show-user`の`roleAssigns`への`experience`追加はTask 4の別変更。

- [ ] **Step 1: 失敗するtestを書く**

`internal/repository/role_test.go`へ追加する。

```go
// CherryPick roles/users.ts は manualLevel role で orderBy('assign.experience',
// 'DESC') する。PostgreSQL の DESC は NULL を先頭に置くため、その挙動を
// 明示的に再現する (id DESC は tie-breaker)。
func TestRoleAssignmentRepository_ListByRoleExperienceDesc(t *testing.T) {
	roleRepo := NewRoleRepository(testDB)
	assignRepo := NewRoleAssignmentRepository(testDB)

	now := time.Now()
	role := &model.Role{
		ID: "role_exp_order", UpdatedAt: now, LastUsedAt: now, Name: "Level",
		Target: model.RoleTargetManualLevel, Policies: datatypes.JSON([]byte("{}")),
		CondFormula: datatypes.JSON([]byte("{}")),
	}
	require.NoError(t, roleRepo.Create(role))
	defer cleanupRole(t, role.ID)
	createTestUser(t, "eo_u1")
	createTestUser(t, "eo_u2")
	createTestUser(t, "eo_u3")

	high := int64(300)
	low := int64(100)
	require.NoError(t, assignRepo.Create(&model.RoleAssignment{ID: "eo_a1", UserID: "eo_u1", RoleID: role.ID, Experience: &high}))
	require.NoError(t, assignRepo.Create(&model.RoleAssignment{ID: "eo_a2", UserID: "eo_u2", RoleID: role.ID, Experience: &low}))
	require.NoError(t, assignRepo.Create(&model.RoleAssignment{ID: "eo_a3", UserID: "eo_u3", RoleID: role.ID})) // nil experience
	t.Cleanup(func() { testDB.Exec(`DELETE FROM "role_assignment" WHERE "roleId" = ?`, role.ID) })

	result, err := assignRepo.ListByRoleExperienceDesc(role.ID, "", "", 10)
	require.NoError(t, err)
	require.Len(t, result, 3)
	assert.Equal(t, "eo_a3", result[0].ID, "NULL experience が先頭 (PostgreSQL DESC default)")
	assert.Equal(t, "eo_a1", result[1].ID, "300 が次")
	assert.Equal(t, "eo_a2", result[2].ID, "100 が最後")
}
```

`internal/core/role/role_service_test.go`へ追加する。

```go
func TestListByRoleExperienceDesc_OrdersAndNotFound(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Level", Target: model.RoleTargetManualLevel}
	high := int64(300)
	low := int64(100)
	assignRepo.Assignments["u1:r1"] = &model.RoleAssignment{ID: "a1", UserID: "u1", RoleID: "r1", Experience: &high, User: &model.User{ID: "u1"}}
	assignRepo.Assignments["u2:r1"] = &model.RoleAssignment{ID: "a2", UserID: "u2", RoleID: "r1", Experience: &low, User: &model.User{ID: "u2"}}

	got, err := svc.ListByRoleExperienceDesc("r1", "", "", 10)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "u1", got[0].UserID, "experience 300 が先")

	_, err = svc.ListByRoleExperienceDesc("ghost", "", "", 10)
	require.Error(t, err)
	assert.ErrorIs(t, err, role.ErrRoleNotFound)
}
```

`internal/api/roles/handler_test.go`へhandler分岐testを追加する。`newTestHandlerWithAssign`はTask 3で既に定義済みなので再利用する（再定義しない）。

```go
func TestUsers_ManualLevelOrdersByExperienceDesc(t *testing.T) {
	h, roleRepo, assignRepo := newTestHandlerWithAssign(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Level", IsPublic: true, IsExplorable: true, Target: model.RoleTargetManualLevel}
	high := int64(300)
	low := int64(100)
	assignRepo.Assignments["u1:r1"] = &model.RoleAssignment{ID: "a1", UserID: "u1", RoleID: "r1", Experience: &high, User: &model.User{ID: "u1", Username: "hi"}}
	assignRepo.Assignments["u2:r1"] = &model.RoleAssignment{ID: "a2", UserID: "u2", RoleID: "r1", Experience: &low, User: &model.User{ID: "u2", Username: "lo"}}

	rec := doPost(h.Users, `{"roleId":"r1","limit":10}`)
	require.Equal(t, http.StatusOK, rec.Code)
	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp, 2)
	assert.Equal(t, "u1", resp[0]["user"].(map[string]any)["id"], "experience 300 が先頭")
	assert.Equal(t, "u2", resp[1]["user"].(map[string]any)["id"])
}

func TestUsers_ManualRoleKeepsIdOrder(t *testing.T) {
	h, roleRepo, assignRepo := newTestHandlerWithAssign(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Manual", IsPublic: true, IsExplorable: true, Target: model.RoleTargetManual}
	assignRepo.Assignments["u1:r1"] = &model.RoleAssignment{ID: "a1", UserID: "u1", RoleID: "r1", User: &model.User{ID: "u1", Username: "u1"}}
	assignRepo.Assignments["u2:r1"] = &model.RoleAssignment{ID: "a2", UserID: "u2", RoleID: "r1", User: &model.User{ID: "u2", Username: "u2"}}

	rec := doPost(h.Users, `{"roleId":"r1","untilId":"a2","limit":10}`)
	require.Equal(t, http.StatusOK, rec.Code)
	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp, 1)
	assert.Equal(t, "u1", resp[0]["user"].(map[string]any)["id"], "manual は id keyset (untilId=a2 の次) のまま")
}
```

- [ ] **Step 2: testがREDであることを確認する**

Run: `go test ./internal/repository -run TestRoleAssignmentRepository_ListByRoleExperienceDesc -count=1`
Run: `go test ./internal/api/roles -run 'TestUsers_ManualLevel|TestUsers_ManualRole' -count=1`

Expected: `ListByRoleExperienceDesc`未定義でFAILする。

- [ ] **Step 3: 最小実装**

`internal/repository/role.go`のinterfaceへ追加する。

```go
	// ListByRoleExperienceDesc returns active assignments ordered by
	// experience DESC (NULLS FIRST, id DESC tie-break) for manualLevel
	// public member lists (CherryPick roles/users.ts orderBy experience DESC)。
	ListByRoleExperienceDesc(roleID, untilID, sinceID string, limit int) ([]*model.RoleAssignment, error)
```

実装を追加する。

```go
func (r *roleAssignmentRepository) ListByRoleExperienceDesc(roleID, untilID, sinceID string, limit int) ([]*model.RoleAssignment, error) {
	var assignments []*model.RoleAssignment
	now := time.Now()
	q := r.db.Preload("User").
		Where("\"roleId\" = ? AND (\"expiresAt\" IS NULL OR \"expiresAt\" > ?)", roleID, now)
	if untilID != "" {
		q = q.Where("id < ?", untilID)
	}
	if sinceID != "" {
		q = q.Where("id > ?", sinceID)
	}
	// CherryPick は orderBy('assign.experience', 'DESC')。PostgreSQL の DESC は
	// NULL を先頭に置くため、その挙動を NULLS FIRST で明示する。id DESC は
	// 同 experience の安定順 tie-breaker。
	q = q.Order("\"experience\" DESC NULLS FIRST, id DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Find(&assignments).Error; err != nil {
		return nil, err
	}
	return assignments, nil
}
```

`internal/testutil/mock_repository.go`の`MockRoleAssignmentRepository`へ追加する。

```go
func (m *MockRoleAssignmentRepository) ListByRoleExperienceDesc(roleID, untilID, sinceID string, limit int) ([]*model.RoleAssignment, error) {
	var out []*model.RoleAssignment
	for _, a := range m.Assignments {
		if a.RoleID != roleID {
			continue
		}
		if untilID != "" && a.ID >= untilID {
			continue
		}
		if sinceID != "" && a.ID <= sinceID {
			continue
		}
		out = append(out, a)
	}
	// experience DESC、NULL は先頭 (PostgreSQL default)、同値は id DESC。
	sort.SliceStable(out, func(i, j int) bool {
		ei, ej := m.expValue(out[i]), m.expValue(out[j])
		if ei != ej {
			return ei > ej
		}
		return out[i].ID > out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MockRoleAssignmentRepository) expValue(a *model.RoleAssignment) int64 {
	if a.Experience == nil {
		// NULL は PostgreSQL の DESC default どおり先頭に置く (= 最大として扱う)。
		return math.MaxInt64
	}
	return *a.Experience
}
```

`sort`と`math`がmock_repository.goに未importなら追加する。

`internal/core/role/role_service.go`へ追加する。

```go
// ListByRoleExperienceDesc returns active assignments for the role ordered
// by experience DESC (manualLevel public member lists only)。CherryPick
// roles/users.ts 互換。ロール不在は ErrRoleNotFound。
func (s *Service) ListByRoleExperienceDesc(roleID, untilID, sinceID string, limit int) ([]*model.RoleAssignment, error) {
	if _, err := s.roleRepo.FindByID(roleID); err != nil {
		return nil, ErrRoleNotFound
	}
	return s.assignmentRepo.ListByRoleExperienceDesc(roleID, untilID, sinceID, limit)
}
```

`internal/api/roles/handler.go`の`Users`のassignment取得を分岐する。

```go
	assigns, err := h.roleService.ListByRole(req.RoleID, untilID, sinceID, limit)
	if r.Target == model.RoleTargetManualLevel {
		// CherryPick は manualLevel だけ experience 降順 (roles/users.ts)。
		// manual/conditional は既存 id keyset 順を維持する。
		assigns, err = h.roleService.ListByRoleExperienceDesc(req.RoleID, untilID, sinceID, limit)
	}
```

- [ ] **Step 4: testがGREENであることを確認する**

Run: `go test ./internal/repository -run TestRoleAssignmentRepository_ListByRoleExperienceDesc -count=1`
Run: `go test ./internal/api/roles -run 'TestUsers' -count=1`

Expected: 全testがPASS。

- [ ] **Step 5: 既存non-regressionを確認する**

Run: `go test ./internal/repository ./internal/core/role ./internal/api/roles -count=1`

Expected: 全てPASS。

- [ ] **Step 6: commit**（ユーザー承認後のみ実行する）

リポジトリ規約に従い、commitはユーザーの明示的な指示がある場合のみ実行する。
```powershell
git add -- internal/repository/role.go internal/repository/role_test.go internal/testutil/mock_repository.go internal/core/role/role_service.go internal/core/role/role_service_test.go internal/api/roles/handler.go internal/api/roles/handler_test.go
git diff --cached --check
git commit -m "api: roles/usersのmanualLevelをexperience降順にする"
```
---

### Task 6: router配線と内部event publisherを追加する

**Files:**
- Modify: `internal/server/router.go`（route登録、event配線、`SetUserRoleStateLookup`、roles handlerの`SetModLogService`）
- Modify: `internal/server/router.go`内の`internalPubSub`周辺

**Interfaces:**
- Consumes: Task 2の`RolesChangeExp`、Task 3の`ProfileHide`/publisher、Task 4の`SetUserRoleStateLookup`、Plan 2の`SetExperienceEventPublisher`。
- Produces: `POST /api/admin/roles/change-exp`（RequireModerator + `write:admin:roles`）。
- Produces: `POST /api/roles/profile-hide`（RequireAuth + `write:account`）。
- Produces: `internal:` pubsubの`userRoleStateUpdated`イベント（immutable payload）と、受信時のper-user cache invalidate subscriber。
- Consumes: 既存`internalPubSub`（`internal:` prefix、metaUpdatedで使用）。

- [ ] **Step 1: 失敗するtestを書く**

routerのopenapi整合性test（`internal/server/openapi_test.go`）が新routeを検知できるかは既存の枠組みに依存する。本Taskのtestはroute登録のcompile + 既存`frontend_test.go`/`openapi_test.go`のnon-regressionで担保する。

`internal/server/router.go`の変更はEchoのroute表へ登録するだけなので、専用の新規testは置かず、次のStepでコンパイルと既存testで検証する。

- [ ] **Step 2: routesを登録する**

`internal/server/router.go`のroles routes（`/roles/users`付近）へ追加する。

```go
	api.POST("/roles/profile-hide", rolesHandler.ProfileHide, middleware.RequireAuth(), middleware.RequireScope("write:account"))
```

admin roles routes（`/admin/roles/users`付近）へ追加する。

```go
	api.POST("/admin/roles/change-exp", adminHandler.RolesChangeExp, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:roles"))
```

- [ ] **Step 3: entity state lookupとmodlogを配線する**

`internal/server/router.go`のrolesHandler配線blockへ追加する。

```go
	// level role の user entity `roles` field を experience / hide state で
	// enrich する lookup (entity → core adapter)。
	entity.SetUserRoleStateLookup(corerole.NewUserRolesStateAdapter(roleService))
	// profile hide の本人操作監査記録。
	rolesHandler.SetModLogService(modLogService)
```

`modLogService`変数がこのscopeに存在しない場合は、`adminHandler.SetModLogService(...)`に渡している同じ`*moderationlog.Service`を使う。

- [ ] **Step 4: 内部event publisherとsubscriberを配線する**

`internal/server/router.go`の`internalPubSub`定義の直後に追加する。

```go
	// role assignment state 変更 (experience / hide) の cross-worker cache
	// invalidation。metaUpdated と同じ pattern。payload は immutable な
	// wire struct で、model pointer は共有しない。
	roleStatePub := roleStateAdapter{pub: internalPubSub}
	roleService.SetExperienceEventPublisher(roleStatePub)
	roleService.SetHideProfileEventPublisher(roleStatePub)
	internalPubSub.Subscribe(context.Background(), "userRoleStateUpdated", func(raw []byte) {
		var e roleAssignmentStateEvent
		if err := json.Unmarshal(raw, &e); err != nil || e.UserID == "" {
			return
		}
		roleService.InvalidateUserRoleCache(e.UserID)
	})
```

`internal/server/router.go`のpackage内に次のadapterとevent型を定義する。

```go
// roleAssignmentStateEvent is the immutable wire payload for role assignment
// state changes (experience / profile-hide) published on the internal channel.
type roleAssignmentStateEvent struct {
	Type         string `json:"type"`
	UserID       string `json:"userId"`
	RoleID       string `json:"roleId"`
	AssignmentID string `json:"assignmentId,omitempty"`
	Experience   int64  `json:"experience,omitempty"`
	IsHideProfile bool  `json:"isHideProfile,omitempty"`
}

// roleStateAdapter bridges the role service's event publisher interfaces to
// the internal Redis pubsub. Payloads are value structs (no pointers).
type roleStateAdapter struct {
	pub *event.PubSubService
}

func (a roleStateAdapter) PublishExperienceUpdated(p corerole.ExperienceUpdatedPayload) error {
	return a.pub.Publish(context.Background(), "userRoleStateUpdated", roleAssignmentStateEvent{
		Type: "experience", UserID: p.UserID, RoleID: p.RoleID,
		AssignmentID: p.AssignmentID, Experience: p.Experience,
	})
}

func (a roleStateAdapter) PublishHideProfileUpdated(p corerole.HideProfileUpdatedPayload) error {
	return a.pub.Publish(context.Background(), "userRoleStateUpdated", roleAssignmentStateEvent{
		Type: "hide", UserID: p.UserID, RoleID: p.RoleID,
		AssignmentID: p.AssignmentID, IsHideProfile: p.IsHideProfile,
	})
}
```

`json`のimportが必要。role packageはrouter.go既存のalias `corerole`（`github.com/shiroha-a/mk/internal/core/role`）を使う。`event`は既にimport済み。

- [ ] **Step 5: ビルドと既存testで検証する**

Run:

```powershell
go build ./...
go test ./internal/server -run 'TestOpenAPI|TestRoutes' -count=1
```

Expected: exit 0。route追加で既存openapi/route整合testが壊れないこと。`openapi_test.go`が新routeを必須検査する場合は、そのgoldenへ`/admin/roles/change-exp`と`/roles/profile-hide`を追加する。

- [ ] **Step 6: commit**（ユーザー承認後のみ実行する）

リポジトリ規約に従い、commitはユーザーの明示的な指示がある場合のみ実行する。
```powershell
git add -- internal/server/router.go
git diff --cached --check
git commit -m "server: level role routeと内部event配線を追加する"
```
---

### Task 7: imported data統合E2Eを追加する

**Files:**
- Create: `internal/api/admin/level_role_integration_test.go`

**Interfaces:**
- Consumes: `integrationDB`（admin package TestMain、全migration適用済み）、Plan 1-6の全実装。
- Produces: CherryPick shapeのrole data（synthetic）をSQL seedし、service/handlerを実配線して「既存level roleが読める」「experience変更でlevelが更新される」「policyが反映される」「memberがexperience順になる」「profile hideが効く」を通しで検証する。
- Consumers: なし（検証のみ）。

- [ ] **Step 1: 統合E2E testを書く（検証用。TDDのRED cycleはTask 1-6のunit/DB testが担う）**

`internal/api/admin/level_role_integration_test.go`を新規作成する。`integrationDB`がnilならskipする。test dataは`synthetic-*`IDと固定の小さな整数のみ（本番由来値なし）。本E2Eは既に構築済みのTask 1-6成果物を実配線して通しで検証するため、RED phaseを持たない（先にTask 1-6が完了していること）。

```go
package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	apiadmin "github.com/shiroha-a/mk/internal/api/admin"
	"github.com/shiroha-a/mk/internal/core/role"
	"github.com/shiroha-a/mk/internal/core/signup"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedImportedLevelRole inserts a CherryPick-shaped manualLevel role +
// assignment directly via SQL, simulating an imported DB row.
func seedImportedLevelRole(t *testing.T) (roleID, userID string) {
	t.Helper()
	if integrationDB == nil {
		t.Skip("integrationDB unavailable")
	}
	now := time.Now()
	roleID = "synth_level_r"
	userID = "synth_level_u"
	require.NoError(t, integrationDB.Exec(`
		INSERT INTO "user" (id, username, "usernameLower", "avatarDecorations") VALUES (?, ?, ?, '[]') ON CONFLICT DO NOTHING`,
		userID, userID, userID).Error)
	t.Cleanup(func() { integrationDB.Exec(`DELETE FROM "user" WHERE id = ?`, userID) })
	require.NoError(t, integrationDB.Exec(`
		INSERT INTO "role" (id, "updatedAt", "lastUsedAt", name, description, target,
			"condFormula", "levelPolicies", "canHideProfileByUser", "canEditMembersByModerator", "policies", "isPublic", "isExplorable")
		VALUES (?, ?, ?, 'synth-level', '', 'manualLevel', '{}'::jsonb, ?::jsonb, true, true, ?::jsonb, true, true)`,
		roleID, now, now,
		`{"baseLevel":0,"experiencePolicies":[{"level":100,"type":"const","base":100}]}`,
		`{"driveCapacityMb":{"useDefault":true,"priority":1,"value":30,"policyAsLevel":[{"level":100,"type":"const","base":500}]}}`).Error)
	t.Cleanup(func() {
		integrationDB.Exec(`DELETE FROM "role_assignment" WHERE "roleId" = ?`, roleID)
		integrationDB.Exec(`DELETE FROM "role" WHERE id = ?`, roleID)
	})
	exp := int64(250)
	require.NoError(t, integrationDB.Exec(`
		INSERT INTO "role_assignment" (id, "userId", "roleId", experience, "isHideProfile")
		VALUES (?, ?, ?, ?, false)`,
		"synth_level_a", userID, roleID, exp).Error)
	t.Cleanup(func() { integrationDB.Exec(`DELETE FROM "role_assignment" WHERE id = ?`, "synth_level_a") })
	return roleID, userID
}

// wiredHandler builds an admin handler with real repos + SetDB + state lookup.
func wiredHandler(t *testing.T) (*apiadmin.Handler, *role.Service) {
	t.Helper()
	if integrationDB == nil {
		t.Skip("integrationDB unavailable")
	}
	idGen, _ := id.NewGenerator("aidx")
	userRepo := repository.NewUserRepository(integrationDB)
	metaRepo := repository.NewMetaRepository(integrationDB)
	roleRepo := repository.NewRoleRepository(integrationDB)
	assignRepo := repository.NewRoleAssignmentRepository(integrationDB)
	signupSvc := signup.NewService(userRepo, metaRepo, idGen)
	roleSvc := role.NewService(roleRepo, assignRepo, metaRepo, idGen)
	roleSvc.SetDB(integrationDB)
	roleSvc.SetUserRepo(userRepo)
	h := apiadmin.NewHandler(signupSvc, roleSvc, metaRepo, userRepo, idGen)
	entity.SetUserRoleStateLookup(role.NewUserRolesStateAdapter(roleSvc))
	t.Cleanup(func() { entity.SetUserRoleStateLookup(nil) })
	return h, roleSvc
}

func TestImportedLevelRole_ReadExperiencePolicyOrderHide(t *testing.T) {
	roleID, userID := seedImportedLevelRole(t)
	h, roleSvc := wiredHandler(t)

	// 1. admin/roles/show が既存 level role (CherryPick shape) を読める。
	rec := doPost(h.RolesShow, `{"roleId":"`+roleID+`"}`, adminUser)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var shown map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &shown))
	assert.Equal(t, "manualLevel", shown["target"])
	assert.Equal(t, true, shown["canHideProfileByUser"])
	lp, ok := shown["levelPolicies"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(0), lp["baseLevel"])
	eps, ok := lp["experiencePolicies"].([]any)
	require.True(t, ok)
	require.Len(t, eps, 1)
	assert.Equal(t, "const", eps[0].(map[string]any)["type"])

	// 2. change-exp (add) → experience 更新 + level 反映。
	rec = doPost(h.RolesChangeExp,
		`{"roleId":"`+roleID+`","userId":"`+userID+`","setMode":"add","value":50}`, adminUser)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	states, err := roleSvc.GetUserRoleStates(userID)
	require.NoError(t, err)
	st := states[roleID]
	assert.Equal(t, 3, st.CurrentLevel, "exp=300 → level 3 (const 100/level)")
	assert.Equal(t, int64(0), st.CurrentExp, "ちょうど level 境界")

	// 3. policyAsLevel 反映: driveCapacityMb が level 3 で const(500) になる。
	policies, err := roleSvc.GetUserPoliciesChecked(userID)
	require.NoError(t, err)
	assert.Equal(t, 500, policies["driveCapacityMb"], "level 3 は const(500) 区間")

	// 4. member experience 順 (experience DESC、NULL 先頭)。
	members, err := roleSvc.ListByRoleExperienceDesc(roleID, "", "", 10)
	require.NoError(t, err)
	require.Len(t, members, 1)
	assert.Equal(t, userID, members[0].UserID)
	assert.Equal(t, int64(300), *members[0].Experience)

	// 5. profile hide: hide=true → public role list から除外。
	require.NoError(t, roleSvc.HideUserProfileRole(userID, roleID, true))
	// packPublicRoles は unexported のため、service の state で確認する。
	states, err = roleSvc.GetUserRoleStates(userID)
	require.NoError(t, err)
	assert.True(t, states[roleID].IsHideProfile, "hide が state に反映される")
	// public pack からの除外は Task 4 の entity test
	// (TestPackPublicRoles_ExcludesHiddenAndCarriesExperience) で担保済み。
}
```

- [ ] **Step 2: testを実行してPASSを確認する**

Run: `go test ./internal/api/admin -run TestImportedLevelRole -count=1`

Expected: PASS。Docker不要（CI service PG経由）。public pack除外の検証は`internal/entity/user_roles_test.go`の`TestPackPublicRoles_ExcludesHiddenAndCarriesExperience`（Task 4）で担保済みで、本E2Eはstate反映までを検証する。

- [ ] **Step 3: commit（ユーザー承認後のみ実行する）**

リポジトリ規約に従い、commitはユーザーの明示的な指示がある場合のみ実行する。

```powershell
git add -- internal/api/admin/level_role_integration_test.go
git diff --cached --check
git commit -m "test: imported level role dataの統合E2Eを追加する"
```

---

### Task 8: PR検証・privacy gate・draft PR作成

**Files:**
- Consume: Plan 3全Taskの成果物。

**Interfaces:**
- Consumes: Task 1-7。
- Produces: `Backend PR 3`（draft）の作成状態。frontend fork（`Misaki-Project/misskey-ts`）のPR 3完了後の最終submodule SHA確定までmergeしない。

- [ ] **Step 1: 全gateを実行する**

Run:

```powershell
make fmt
make lint
go build ./...
go test ./internal/model ./internal/migrationcompat ./internal/levelrolemigration ./internal/repository ./internal/core/role -count=1
go test ./internal/api/admin ./internal/api/roles ./internal/api/i ./internal/api/users ./internal/entity -count=1
go test ./internal/server -count=1
```

Expected: 全てexit 0。

- [ ] **Step 2: race + coverageを確認する**

Run:

```powershell
go test -race -count=1 -timeout 10m -coverprofile=coverage-role.out -covermode=atomic ./internal/core/role ./internal/api/roles
go test -race -count=1 -timeout 10m -coverprofile=coverage-admin.out -covermode=atomic ./internal/api/admin
go tool cover -func=coverage-role.out | Select-String -Pattern "total:"
go tool cover -func=coverage-admin.out | Select-String -Pattern "total:"
```

Expected: `internal/core/role` 90%以上、`internal/api/admin` 80%以上（CI閾値）。

- [ ] **Step 3: privacy gateを実行する**

```powershell
$changed = @(
  'internal/model/role.go',
  'internal/repository/role.go',
  'internal/testutil/mock_repository.go',
  'internal/core/role/role_service.go',
  'internal/core/role/user_roles_state.go',
  'internal/core/role/hide_profile.go',
  'internal/core/moderationlog/types.go',
  'internal/entity/role.go',
  'internal/entity/user_roles.go',
  'internal/api/admin/handler.go',
  'internal/api/admin/modlog_helpers.go',
  'internal/api/admin/change_exp_test.go',
  'internal/api/admin/level_role_integration_test.go',
  'internal/api/roles/handler.go',
  'internal/api/i/handler.go',
  'internal/api/users/handler.go',
  'internal/server/router.go',
  'internal/server/middleware/ratelimit_defs.go'
)
$matches = 0
foreach ($file in $changed) {
    if (Test-Path -LiteralPath $file -PathType Leaf) {
        $text = [System.IO.File]::ReadAllText((Resolve-Path $file))
        $matches += [regex]::Matches($text, "['""][a-z0-9]{16}['""]").Count
        $matches += [regex]::Matches($text, '(?i)\b[0-9a-f]{64}\b').Count
        $matches += [regex]::Matches($text, '[A-Za-z]:\\').Count
    }
}
if ($matches -ne 0) { throw "private literal categories found: $matches" }
```

Expected: exit 0。個別matchを表示しない。CherryPickの公開固定error ID（`6503c040-...`等）は16桁hexだが、これらはpublic API contractで、privacy scan対象の「production由来」ではない。scanが誤検知する場合は、`00000000-0000-0000-0000-000000000000`形のUUID（8-4-4-4-12）を除外する正規表現へ調整する（例: `"['""][0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}['""]"`を許可listとして除外）。

- [ ] **Step 4: submodule pointerを確認する（変更しない）**

Run:

```powershell
git -C third_party/misskey rev-parse HEAD
git diff --stat -- third_party/misskey
```

Expected: submodule HEADが現在のfork前SHAのままであり、`git diff --stat`が空（本planでsubmoduleを変更していない）。frontend fork完了後、`Misaki-Project/misskey-ts`のPR 3最終SHAへpointerを更新するのは`Backend PR 3`のfinalize時（draft解除時）のみ。

- [ ] **Step 5: commit（ユーザー承認後のみ）とcontroller handoff**

リポジトリ規約に従い、commit・PR作成・push・mergeはユーザーの明示的な指示がある場合のみ実行する。本planでは自動で`gh pr create`を実行しない。

Run:

```powershell
git status --short
git log --oneline -8
```

Expected: Task 1-7のcommitが並び、working treeがclean。ユーザー承認後に`Backend PR 3`をdraftとして作成する（base `Misaki-Project/mk:Misaki-develop`、日本語タイトル・本文、`Closes`には実装開始前に作成した対応Issue番号を指定）。

draft PR本文に次を明記する。

- draftの理由: `Misaki-Project/misskey-ts`のfrontend PR 3完了と最終submodule SHA確定までmergeしない。
- API contractは本PRで固定し、frontend側はこのcontractをconsumingする。
- 本番由来ID・件数・path・hash・credentialを含まないこと（privacy gate通過済み）。

---

### Task 9: frontend PR 3完了後ゲートのfinalize（gated）

**Files:**
- Modify: `.gitmodules`（URL変更: `shiroha-a/misskey-ts` → `Misaki-Project/misskey-ts`）
- Modify: `third_party/misskey`（submodule pointer更新）
- Modify: `internal/entitycompat/testdata/golden_*.json`（`make shapecheck-gen`で再生成）
- Consume: frontend fork `Misaki-Project/misskey-ts`のPR 3完了と最終SHA

**Interfaces:**
- Consumes: Task 8でdraft公開した`Backend PR 3`、frontend側PR 3のmerge。
- Produces: `Backend PR 3`のdraft解除・merge可能状態。
- 前提: 本Taskはfrontend PR 3がmergeされるまで**実行しない**（gated）。`Backend PR 3`はそれまでdraftのまま。

- [ ] **Step 1: frontend PR 3のmergeを確認する（gated）**

`Misaki-Project/misskey-ts`のfrontend PR 3がmergeされ、`third_party/misskey`の期待SHAが確定していることを確認する。未mergeならここで停止し、`Backend PR 3`をdraftのまま維持する。

- [ ] **Step 2: .gitmodulesのURLをOrganization forkへ更新する**

`.gitmodules`の`url`を`https://github.com/Misaki-Project/misskey-ts.git`へ変更し、submodule pointerをfrontend PR 3の最終SHAへ更新する。

```powershell
git config -f .gitmodules submodule.third_party/misskey.url https://github.com/Misaki-Project/misskey-ts.git
git -C third_party/misskey fetch --all
$frontendSHA = $env:FRONTEND_LEVEL_ROLE_SHA
if ($frontendSHA -notmatch '^[0-9a-f]{40}$') { throw 'FRONTEND_LEVEL_ROLE_SHA must be the verified 40-character merge SHA' }
git -C third_party/misskey cat-file -e "$frontendSHA^{commit}"
git -C third_party/misskey checkout $frontendSHA
git add .gitmodules third_party/misskey
```

Expected: `.gitmodules`のURLが`Misaki-Project/misskey-ts.git`になり、`git diff --submodule`で期待SHAの変更がstageされる。

- [ ] **Step 3: shape・error・permission goldenを再生成する**

Run: `make shapecheck-gen`

Expected: `internal/entitycompat/testdata/golden_schemas.json` / `golden_error_ids.json` / `golden_permissions.json`等が新endpoint（`admin/roles/change-exp`、`roles/profile-hide`）のerror code・id・shapeを反映して更新される。

- [ ] **Step 4: frontend-checkとimported DB E2Eを実行する**

Run:

```powershell
go test ./internal/entitycompat/... -count=1
go test ./internal/api/admin -run TestImportedLevelRole -count=1
# frontend-check job 相当 (fork frontend の vue-tsc --noEmit) を CI で確認
```

Expected: entitycompat golden gateとimported DB E2EがPASSし、CIの`frontend-check` jobが緑になる。

- [ ] **Step 5: commitとdraft解除（ユーザー承認後のみ）**

リポジトリ規約に従い、commit・PR操作はユーザーの明示的な指示がある場合のみ実行する。承認後、本commitを`Backend PR 3`へ追加し、`gh pr ready`でdraftを解除する。mergeはユーザー承認後のみ。
