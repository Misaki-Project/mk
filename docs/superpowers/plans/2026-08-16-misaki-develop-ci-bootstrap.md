# Misaki-develop CI Bootstrap Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** PR #2（`Misaki-Project/mk` base `Misaki-develop` / head `Misaki0331:feature/cherrypick-compatibility-foundation`）でNO CHECKになっているCIを、`Misaki-develop`を`pull_request.branches`へ追加して走らせ、core check（build / test / lint）の成功を確認する。

**Architecture:** 6 workflowの`pull_request.branches`へ`Misaki-develop`を追加する。まずfeature側でregression testを追加してRED→GREENで確認し、次に`Misaki-Project/mk:Misaki-develop`へ6 workflow編集のみのnarrow bootstrap commitをfast-forwardで直接pushし、最後にfeature headを更新してPR #2を同期しCIを監視する。feature側とbase側で同一のfilter編集を持つことで、PR diffでrevertされない。

**Tech Stack:** Git、GitHub CLI (`gh`) v2.32.0、PowerShell 7、Go 1.26 (`go test` / `gopkg.in/yaml.v3`)、GitHub Actions

## Global Constraints

- actorおよびlocal commit authorは`Misaki0331`のみ。Organization `Misaki-Project`はauthorになれず、Issue/PRのactorは`Misaki0331`として表示される（意図した状態）。
- feature branch repositoryはfork `Misaki0331/mk`だけ。base branch・default branch・Issue・Pull Requestのrepositoryは`Misaki-Project/mk`。
- fork元`shiroha-a/mk`へは一切書き込まない（読み取り専用のまま）。
- 変更対象は`.github/workflows/ci.yml`、`diff-e2e.yml`、`docker.yml`、`dropin-e2e.yml`、`playwright.yml`、`upstream-backend-e2e.yml`の**exactly 6 file**のみ。`pull_request.branches`へ`Misaki-develop`を追加する。既存エントリ（`main`、`develop`）は保持し、順序は既存のものを維持する。
- `on.push.branches`（ci.yml / docker.yml / docker-branch.yml）、workflow内のjobs・permissions・paths・uses・actions・env、`.github/workflows/`の上記以外のファイル、他repository設定（branch protection等）は変更しない。
- `.github/workflows/docker-branch.yml`はpush専用workflowのため対象外。`.github/workflows/dropin-frontend-e2e.yml`と`.github/workflows/queue-bench-smoke.yml`は`pull_request` triggerを持たないため対象外。
- base bootstrap commitは`Misaki-Project/mk:Misaki-develop`への**直接push（承認済み）**。`--force`不使用のfast-forwardのみ。
- force-push、本planで明示した操作以外のrepository設定変更は行わない。
- dump、credential、local evidence、production由来の値・件数・path・hash・SQLをIssue、Pull Request、commit、artifactへ含めない。
- IssueとPull Requestのタイトル・本文は日本語にする。
- 全てのmutationコマンドは対象repositoryを明示する。`gh pr`/`gh issue`は`--repo OWNER/REPO`で指定する。git pushはforkの場合remote `origin`、Organizationの場合明示的なrepository URLを使う。
- 本planは既存plan `docs/superpowers/plans/2026-08-16-misaki-branch-topology.md`のTask 3（PR作成とCI確認）で見つかったCI blockerを解消するための子plan。親planの成果物（PR #2、Issue #1、branch構成）は不変の前提として使う。
- 本planは**mergeしない**。CI確認まで。baseのdefault branch・PR #2・Issueは変更しない（PR #2のhead更新とbase `Misaki-develop`へのnarrow commitのみ）。

---

### Task 1: Feature側TDD準備（pushなし）

**Files:**
- Create: `internal/entitycompat/workflow_branch_filter_test.go`（`package entitycompat`）
- Modify: `.github/workflows/ci.yml`、`.github/workflows/diff-e2e.yml`、`.github/workflows/docker.yml`、`.github/workflows/dropin-e2e.yml`、`.github/workflows/playwright.yml`、`.github/workflows/upstream-backend-e2e.yml`（各`pull_request.branches`のみ）
- Consume: `docs/superpowers/specs/2026-08-16-misaki-develop-ci-bootstrap-design.md`

**Interfaces:**
- Consumes: 公開feature worktree `feature/cherrypick-compatibility-foundation-public`（HEAD `da6765b4d89521cedf9069729bcb2b4a865bf19e`、設計/計画docsはcommit済み）
- Produces: コミット`CI: Misaki-develop向けPR workflow triggerとregression testを追加する`（test + 6 workflow編集のみ。docsは含めない）
- Produces: `TestWorkflowBranchFiltersIncludeMisakiDevelop` — `gopkg.in/yaml.v3`で6 fileの`on.pull_request.branches`をisolateし、`Misaki-develop`の包含を検証する。`on.push.branches`は見ない。

- [ ] **Step 1: 事前状態を確認する**

Run:
```powershell
git status --short
git rev-parse HEAD
```

Expected: worktreeはclean、HEADは`da6765b4d89521cedf9069729bcb2b4a865bf19e`。設計/計画docs（`docs/superpowers/specs/2026-08-16-misaki-develop-ci-bootstrap-design.md`等）は既にcommit済み。

- [ ] **Step 2: 失敗するtestを追加する**

Create `internal/entitycompat/workflow_branch_filter_test.go`:

```go
package entitycompat

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// pullRequestWorkflowFiles are the exactly six workflows that must trigger on
// pull requests whose base branch is Misaki-develop.
//
// A push-only workflow (docker-branch.yml) and schedule/workflow_dispatch-only
// workflows (dropin-frontend-e2e.yml, queue-bench-smoke.yml) are deliberately
// excluded: they must NOT be required to carry Misaki-develop.
var pullRequestWorkflowFiles = []string{
	".github/workflows/ci.yml",
	".github/workflows/diff-e2e.yml",
	".github/workflows/docker.yml",
	".github/workflows/dropin-e2e.yml",
	".github/workflows/playwright.yml",
	".github/workflows/upstream-backend-e2e.yml",
}

// TestWorkflowBranchFiltersIncludeMisakiDevelop guards CI coverage for PRs
// whose base is Misaki-develop (PR #2 was reported as NO CHECK).
//
// GitHub Actions ignores pull_request triggers whose `branches` filter does
// not match the PR's base branch. All pull_request-triggered workflows must
// include `Misaki-develop` in `on.pull_request.branches`. The test isolates
// the `on.pull_request` block and never inspects `on.push.branches`, so a
// push-only branch entry cannot produce a false pass.
func TestWorkflowBranchFiltersIncludeMisakiDevelop(t *testing.T) {
	for _, rel := range pullRequestWorkflowFiles {
		path := filepath.Join("..", "..", rel)
		got := pullRequestBranches(t, path)
		if !containsString(got, "Misaki-develop") {
			t.Errorf("%s: on.pull_request.branches = %v; want Misaki-develop", rel, got)
		}
	}
}

// pullRequestBranches parses a GitHub Actions workflow YAML with yaml.v3 and
// returns the `on.pull_request.branches` list. It returns nil when the
// workflow has no pull_request trigger, or no branches key under it.
func pullRequestBranches(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc map[string]interface{}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	on, ok := doc["on"].(map[string]interface{})
	if !ok {
		return nil
	}
	pr, ok := on["pull_request"].(map[string]interface{})
	if !ok {
		return nil
	}
	branches, ok := pr["branches"].([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(branches))
	for _, b := range branches {
		if s, ok := b.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
```

- [ ] **Step 3: REDを確認する**

Run:
```powershell
go test ./internal/entitycompat -run TestWorkflowBranchFiltersIncludeMisakiDevelop -count=1
```

Expected: 6 fileすべてで`on.pull_request.branches`に`Misaki-develop`が含まれないためFAIL。既存`[main, develop]`または`[develop, main]`一覧がエラー出力に含まれる。`on.push.branches`は検査しない。

- [ ] **Step 4: 6 workflowの`pull_request.branches`へ`Misaki-develop`を追加する**

Edit only the `on.pull_request.branches` line in each of the six files:

| File | 変更 |
|---|---|
| `.github/workflows/ci.yml` | `branches: [main, develop]` → `branches: [main, develop, Misaki-develop]` |
| `.github/workflows/diff-e2e.yml` | `branches: [develop, main]` → `branches: [develop, main, Misaki-develop]` |
| `.github/workflows/docker.yml` | `branches: [main, develop]` → `branches: [main, develop, Misaki-develop]` |
| `.github/workflows/dropin-e2e.yml` | `branches: [develop, main]` → `branches: [develop, main, Misaki-develop]` |
| `.github/workflows/playwright.yml` | `branches: [develop, main]` → `branches: [develop, main, Misaki-develop]` |
| `.github/workflows/upstream-backend-e2e.yml` | `branches: [develop, main]` → `branches: [develop, main, Misaki-develop]` |

`on.push.branches`、paths、jobs、permissions、uses、actions、env、`docker-branch.yml`を含む他workflowは変更しない。

- [ ] **Step 5: 変更スコープを検証する**

Run:
```powershell
git diff --check
git diff --stat
git diff -- .github/workflows/ -- internal/entitycompat/workflow_branch_filter_test.go
```

Expected: `git diff --check`はclean。`git diff --stat`は6 workflow + test fileのみ。各workflowのdiffが`on.pull_request.branches`の1行のみで、`on.push.branches`・jobs・paths・permissionsが変化していない。`docker-branch.yml`はdiffに出ない。

- [ ] **Step 6: GREENを確認する**

Run:
```powershell
go test ./internal/entitycompat -run TestWorkflowBranchFiltersIncludeMisakiDevelop -count=1
```

Expected: PASS。6 fileすべてで`Misaki-develop`が検出される。

- [ ] **Step 7: entitycompatのビルドと静的testが壊れていないことを確認する**

Run:
```powershell
go build ./internal/entitycompat/...
go test ./internal/entitycompat -run 'TestWorkflowBranchFiltersIncludeMisakiDevelop' -count=1
go vet ./internal/entitycompat/...
```

Expected: build成功、新test PASS、vet clean。本変更はworkflow YAMLのみでGoコードに触れないため、Goテストへの影響はない。entitycompat内のDB-backed test（`cherrypick_avatar_decoration_migration_test.go`、`note_relation_schema_test.go`）は`testutil.OpenTestDB`（testcontainers/Docker）を要するため本手順では実行しない。YAMLのみの変更ではこれらに影響しない。CI（test-shards）がカバーする。

- [ ] **Step 8: commitする**

Run:
```powershell
git add .github/workflows/ci.yml .github/workflows/diff-e2e.yml .github/workflows/docker.yml .github/workflows/dropin-e2e.yml .github/workflows/playwright.yml .github/workflows/upstream-backend-e2e.yml internal/entitycompat/workflow_branch_filter_test.go
git commit -m "CI: Misaki-develop向けPR workflow triggerとregression testを追加する"
```

Expected: commit成功。**このcommitにはtest + 6 workflow編集のみを含む**。設計/計画docs（既にcommit済み）は含めない。

- [ ] **Review points**

- 6 workflowのみが変更され、`docker-branch.yml`等の対象外workflowが未変更。
- `on.push.branches`が全workflowで未変更。
- testが`on.pull_request.branches`をisolateし、`on.push.branches`を参照していない。
- コミットが「test + 6 workflow」のみ。RED→GREENの実証済み。
- この時点では**何もpushしない**。

---

### Task 2: Organization base bootstrap

**Files:**
- Modify（disposable clone内）: `.github/workflows/ci.yml`、`.github/workflows/diff-e2e.yml`、`.github/workflows/docker.yml`、`.github/workflows/dropin-e2e.yml`、`.github/workflows/playwright.yml`、`.github/workflows/upstream-backend-e2e.yml`（各`pull_request.branches`のみ）
- Consume: Task 1の6 workflow編集と同一の内容

**Interfaces:**
- Consumes: `Misaki-Project/mk:Misaki-develop` live SHA（plan実行時点で`aebdad71ad09ac189a9433abc45559a1374d1c80`を期待）
- Produces: base bootstrap commit `CI: Misaki-develop向けPR workflow triggerを追加する`（**6 workflow編集のみ**、test/docsなし、author `Misaki0331`）
- Produces: `Misaki-Project/mk:Misaki-develop`がbootstrap commitへfast-forwardされた状態（pre-SHA/post-SHAを記録）
- Produces: default branchでのstatic workflow登録確認

- [ ] **Step 1: live base SHAを取得して記録する**

Run:
```powershell
$baseRepo = 'https://github.com/Misaki-Project/mk.git'
$preSha = (git ls-remote $baseRepo refs/heads/Misaki-develop | ForEach-Object { ($_ -split '\t')[0] })
Write-Output "PRE_SHA=$preSha"
```

Expected: `PRE_SHA=aebdad71ad09ac189a9433abc45559a1374d1c80`。これをレポートへ記録する。

- [ ] **Step 2: disposableな隔離cloneを作成する**

Run:
```powershell
$tmp = Join-Path $env:TEMP ("mk-bootstrap-" + [guid]::NewGuid().ToString("N"))
git clone --no-checkout $baseRepo $tmp
git -C $tmp checkout --detach $preSha
git -C $tmp config user.name "Misaki"
git -C $tmp config user.email "60120497+Misaki0331@users.noreply.github.com"
Write-Output "TMP=$tmp"
```

Expected: clone成功、`$preSha`でdetached HEAD、author設定が`Misaki <60120497+Misaki0331@users.noreply.github.com>`。`$tmp`をTask 2の作業dirとして保持する。

- [ ] **Step 3: 6 workflowの`pull_request.branches`編集のみを適用する**

Task 1 Step 4の表と同一の変更を、`$tmp`内の6 fileへ適用する（`on.pull_request.branches`へ`Misaki-develop`を追加。他は変更しない）。

- [ ] **Step 4: YAMLを検証する**

disposable clone内に一時的な検証スクリプトを作成し、6 fileをyaml.v3でパースして`on.pull_request.branches`に`Misaki-develop`があることを確認する。

Run:
```powershell
$tmpCheck = @'
package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

func main() {
	files := []string{
		".github/workflows/ci.yml",
		".github/workflows/diff-e2e.yml",
		".github/workflows/docker.yml",
		".github/workflows/dropin-e2e.yml",
		".github/workflows/playwright.yml",
		".github/workflows/upstream-backend-e2e.yml",
	}
	fail := false
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			fmt.Printf("READ-ERR %s: %v\n", f, err)
			fail = true
			continue
		}
		var doc map[string]interface{}
		if err := yaml.Unmarshal(data, &doc); err != nil {
			fmt.Printf("YAML-ERR %s: %v\n", f, err)
			fail = true
			continue
		}
		on, _ := doc["on"].(map[string]interface{})
		pr, _ := on["pull_request"].(map[string]interface{})
		branches, _ := pr["branches"].([]interface{})
		ok := false
		for _, b := range branches {
			if s, _ := b.(string); s == "Misaki-develop" {
				ok = true
			}
		}
		fmt.Printf("%s: hasMisakiDevelop=%v branches=%v\n", f, ok, branches)
		if !ok {
			fail = true
		}
	}
	if fail {
		os.Exit(1)
	}
}
'@
Set-Content -Path "$tmp\bootstrap_check.go" -Value $tmpCheck -Encoding utf8
go run "$tmp\bootstrap_check.go"
$yamlExit = $LASTEXITCODE
Remove-Item "$tmp\bootstrap_check.go" -Force
Write-Output "YAML_CHECK_EXIT=$yamlExit"
```

Expected: 6 fileすべて`hasMisakiDevelop=true`でexit 0。一時ファイルは削除済み（residue 0）。

- [ ] **Step 5: diffのscopeを検証する**

Run:
```powershell
git -C $tmp diff --check
git -C $tmp diff --stat
git -C $tmp diff
```

Expected: `--check`はclean。diffは6 workflowの`on.pull_request.branches` 1行ずつのみ。他ファイル・他変更なし。

- [ ] **Step 6: 直前でbaseが未変化であることを確認する**

Run:
```powershell
$nowSha = (git ls-remote $baseRepo refs/heads/Misaki-develop | ForEach-Object { ($_ -split '\t')[0] })
if ($nowSha -ne $preSha) { throw 'Misaki-develop moved; abort bootstrap' }
Write-Output "CONFIRMED=$nowSha"
```

Expected: `CONFIRMED=$preSha`。変化があれば停止（forceしない）。

- [ ] **Step 7: fast-forward pushする**

Run:
```powershell
git -C $tmp add .github/workflows/ci.yml .github/workflows/diff-e2e.yml .github/workflows/docker.yml .github/workflows/dropin-e2e.yml .github/workflows/playwright.yml .github/workflows/upstream-backend-e2e.yml
git -C $tmp commit -m "CI: Misaki-develop向けPR workflow triggerを追加する"
git -C $tmp push $baseRepo HEAD:refs/heads/Misaki-develop
```

Expected: commitは6 workflow編集のみ、author `Misaki <60120497+Misaki0331@users.noreply.github.com>`。pushはfast-forwardで成功（`--force`不使用）。non-fast-forwardで拒否された場合は停止し、forceしない。

- [ ] **Step 8: post-SHAを確認・記録する**

Run:
```powershell
$postSha = (git ls-remote $baseRepo refs/heads/Misaki-develop | ForEach-Object { ($_ -split '\t')[0] })
Write-Output "POST_SHA=$postSha"
```

Expected: `POST_SHA`がbootstrap commitのSHA。`PRE_SHA`と異なること。両者をレポートへ記録する。

- [ ] **Step 9: default branchのworkflow登録をpollする**

Run:
```powershell
$deadline = (Get-Date).AddMinutes(5)
$registered = $false
while ((Get-Date) -lt $deadline) {
  $names = gh api "repos/Misaki-Project/mk/actions/workflows" --jq '.workflows[].path' 2>$null
  if ($names -match '\.github/workflows/ci\.yml') { $registered = $true; break }
  Start-Sleep -Seconds 15
}
Write-Output "STATIC_WORKFLOW_REGISTERED=$registered"
```

Expected: `STATIC_WORKFLOW_REGISTERED=True`（`.github/workflows/ci.yml`等のstatic workflowがactions/workflows APIに現れる）。5分待っても登録されない場合は停止し、Task 3へ進まない（mergeしない）。

- [ ] **Step 10: cleanup（residue 0）**

Run:
```powershell
Remove-Item -LiteralPath $tmp -Recurse -Force
Write-Output "CLEANUP_DONE"
```

Expected: disposable cloneが削除済み。作業dirに残存なし。

- [ ] **Review points**

- base commitのdiffが6 workflowの`pull_request.branches`編集のみ（test/docsなし）。
- authorが`Misaki0331`、`--force`不使用のfast-forward。
- `PRE_SHA`/`POST_SHA`が記録され、`POST_SHA != PRE_SHA`。
- static workflow登録が確認できた。
- 既存worktree・作業ツリーを汚していない。disposable cloneを撤去済み（residue 0）。

---

### Task 3: Feature pushとPR CI

**Files:**
- Modify（push対象）: Task 1でcommit済みの`feature/cherrypick-compatibility-foundation-public`
- Consume: `Misaki-Project/mk:Misaki-develop` post-SHA（Task 2）、PR #2

**Interfaces:**
- Consumes: fork `Misaki0331/mk:feature/cherrypick-compatibility-foundation`（push前SHA `0cfeb133d98250d7f026d1ff127ba9e733503df2`）
- Produces: fork feature branchがTask 1 commitを含む新SHAへfast-forwardされた状態
- Produces: PR #2 headが新SHAへ同期された状態（base `Misaki-develop`のまま）
- Produces: core check（`build` / `test` / `lint`）の成功確認とnon-core checkの分類

- [ ] **Step 1: 6 workflowの`on.pull_request.branches`がbaseと一致することを確認する**

base（post-SHA）とfeature（Task 1後HEAD）の各6 fileについて、`on.pull_request.branches`をyaml.v3で抽出して比較する。ci.ymlはpostgres image version等の既存行がbase（16）とfeature（18）で異なるため**全体blob比較はしない**。`on.pull_request.branches`の一致のみを検証する。`on.push.branches`は比較しない（push filterは全workflowで未変更のため）。

Run:
```powershell
$featureHead = git rev-parse HEAD
$postSha = (git ls-remote https://github.com/Misaki-Project/mk.git refs/heads/Misaki-develop | ForEach-Object { ($_ -split '\t')[0] })
Write-Output "FEATURE_HEAD=$featureHead"
Write-Output "POST_SHA=$postSha"
$files = @('ci.yml','diff-e2e.yml','docker.yml','dropin-e2e.yml','playwright.yml','upstream-backend-e2e.yml')
foreach ($f in $files) {
  git show "$postSha`:.github/workflows/$f" | Set-Content -Path "$env:TEMP\base_$f" -Encoding utf8
  git show "HEAD`:.github/workflows/$f" | Set-Content -Path "$env:TEMP\feat_$f" -Encoding utf8
}
$cmp = @'
package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

func branches(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("READ-ERR %s: %v\n", path, err)
		os.Exit(2)
	}
	var doc map[string]interface{}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		fmt.Printf("YAML-ERR %s: %v\n", path, err)
		os.Exit(2)
	}
	on, _ := doc["on"].(map[string]interface{})
	pr, _ := on["pull_request"].(map[string]interface{})
	raw, _ := pr["branches"].([]interface{})
	out := make([]string, 0, len(raw))
	for _, b := range raw {
		if s, ok := b.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func main() {
	files := []string{"ci.yml", "diff-e2e.yml", "docker.yml", "dropin-e2e.yml", "playwright.yml", "upstream-backend-e2e.yml"}
	fail := false
	for _, f := range files {
		base := branches(os.Getenv("TEMP") + "\\base_" + f)
		feat := branches(os.Getenv("TEMP") + "\\feat_" + f)
		if !equal(base, feat) {
			fmt.Printf("MISMATCH %s base=%v feat=%v\n", f, base, feat)
			fail = true
		} else {
			fmt.Printf("MATCH %s %v\n", f, base)
		}
	}
	if fail {
		os.Exit(1)
	}
}
'@
Set-Content -Path "$env:TEMP\pr_filter_compare.go" -Value $cmp -Encoding utf8
go run "$env:TEMP\pr_filter_compare.go"
$cmpExit = $LASTEXITCODE
foreach ($f in $files) { Remove-Item "$env:TEMP\base_$f", "$env:TEMP\feat_$f" -Force -ErrorAction SilentlyContinue }
Remove-Item "$env:TEMP\pr_filter_compare.go" -Force -ErrorAction SilentlyContinue
Write-Output "FILTER_CMP_EXIT=$cmpExit"
```

Expected: `FILTER_CMP_EXIT=0`。6 fileすべて`MATCH`で`on.pull_request.branches`が一致（`[main, develop, Misaki-develop]`または`[develop, main, Misaki-develop]`）。`go run`は公開feature worktreeのmodule context（`gopkg.in/yaml.v3`は直接依存）で動作する。不一致がある場合、または比較が実行できない場合はpushせず停止。

- [ ] **Step 2: fork feature branchをfast-forward pushする**

Run:
```powershell
git status --short
git rev-parse HEAD
git push origin feature/cherrypick-compatibility-foundation-public:feature/cherrypick-compatibility-foundation
```

Expected: worktree clean。pushがfast-forwardで成功（remote feature `0cfeb133...` → 新HEAD）。`--force`不使用。拒否された場合は停止。

- [ ] **Step 3: PR #2が同期されたことを確認する**

Run:
```powershell
$prNumber = gh pr list --repo Misaki-Project/mk --state open --head feature/cherrypick-compatibility-foundation --json number --jq '.[] | .number'
if (@($prNumber).Count -ne 1) { throw 'matching open PR is not unique' }
if ([int]$prNumber -ne 2) { throw "expected PR number 2, got $prNumber" }
gh pr view --repo Misaki-Project/mk $prNumber --json number,state,baseRefName,headRefName,headRefOid,url
```

Expected: PR番号が一意で`2`。state OPEN、base `Misaki-develop`、head `Misaki0331:feature/cherrypick-compatibility-foundation`、`headRefOid`が新HEAD SHA。owner-prefixed head（`Misaki0331:feature/...`）の`gh pr list --head`がgh v2.32.0で空を返す場合は、bare branch名（`feature/cherrypick-compatibility-foundation`）で解決する。`--head Misaki0331:feature/...`形式は`gh pr create`のhead指定には使うが、`gh pr list --head`解決には使わない。

- [ ] **Step 4: CIを監視する**

Run:
```powershell
gh pr checks --repo Misaki-Project/mk $prNumber --watch --interval 10
```

Expected: checkが現れ、core check（`build` / `test` / `lint`）がsuccessへ至る。`--watch`が終わらない場合は一定間隔で状態を再取得し、進行を記録する。

- [ ] **Step 5: core checkの成功を個別に確認する**

Run:
```powershell
gh pr checks --repo Misaki-Project/mk $prNumber 2>&1
gh api "repos/Misaki-Project/mk/commits/$featureHead/check-runs" --jq '.check_runs[] | {name, status, conclusion}'
```

Expected: `build`、`test`、`lint`の3 checkが`conclusion=success`。**branch protection未設定でもcore checkを必須とみなす**（requiredでなくても本planのgateとする）。

- [ ] **Step 6: non-core checkを分類する**

`diff-e2e`、`dropin-e2e`（swap-test / mkgo-born / ed25519-verify / federation）、`upstream-backend-e2e`、`playwright`、`docker`等のcheckはnon-required。それぞれについて:

- pathsフィルタにより発火していない場合は「未発火（対象外paths）」と分類。
- 発火してsuccessなら「pass」。
- 発火してfailureならログを取得し、本branch変更（workflow filter行のみ）との関連を判定して分類する（filter行のみの変更でCIテスト自体が壊れることはない想定。failureはpre-existingまたはflakyと判定される見込み）。

- [ ] **Step 7: CI継続監視（core checkがpendingのままの場合）**

core checkがpendingのまま実用的な時間（例: 15分）を超える場合は、正確なcheck名とstatusだけを報告し、passと断定しない。required failure、またはstatic workflow未登録の場合は**mergeしない**。

- [ ] **Step 8: topology / privacy / worktree / residueを再検証する**

Run:
```powershell
gh repo view Misaki-Project/mk --json nameWithOwner,defaultBranchRef,hasIssuesEnabled,parent
gh repo view Misaki0331/mk --json nameWithOwner,defaultBranchRef,hasIssuesEnabled,parent
git ls-remote https://github.com/Misaki-Project/mk.git refs/heads/develop refs/heads/docker refs/heads/main refs/heads/Misaki-Stable refs/heads/Misaki-develop
git ls-remote --heads origin refs/heads/develop refs/heads/docker refs/heads/feature/cherrypick-compatibility-foundation refs/heads/Misaki-Stable refs/heads/Misaki-develop
git status --short
git rev-parse HEAD
```

Expected:
- Organization default `Misaki-develop`、`hasIssuesEnabled=true`、parent `shiroha-a/mk`、branch一覧（develop/docker/main/Misaki-Stable/Misaki-develop）不変。
- fork default `develop`、`hasIssuesEnabled=false`、parent `shiroha-a/mk`、branch一覧（develop/docker/featureのみ）不変。
- 公開feature worktreeはclean、HEADがTask 1後の新SHA。local feature HEADがfork remote feature SHAと一致すること。
- pre-bootstrap HEAD（`0cfeb133d98250d7f026d1ff127ba9e733503df2`）とfinal HEADは**異なる値**としてレポートへ記録する。
- privacy監査（branch追加行のartifact / forbidden source / operator absolute path / credentials / local path）が0であること。parent `shiroha-a/mk`は読み取り専用のまま未変更。
- 未コミット変更・generated artifact・residue 0。

- [ ] **Step 9: 完了報告する**

core check（`build` / `test` / `lint`）の結果、non-core checkの分類、PR #2のURL、pre/post base SHA、pre/post feature HEAD、topology、privacy結果をレポートする。production由来情報、local path、private branch SHA、local evidenceは報告しない。

- [ ] **Review points**

- PR #2のheadが更新され、base `Misaki-develop`のまま。
- 6 workflowの`on.pull_request.branches`がbaseとfeatureで一致（PRでrevertされない）。
- core check（build / test / lint）がsuccess。
- non-core checkが発火状況・結果で分類された。
- Organization default branch・fork default branch・branch一覧・Issues状態・parentが不変。
- privacy全category 0、parent読み取り専用、residue 0。
- **mergeしていない**。

---

## 全体の完了条件

- `Misaki-Project/mk:Misaki-develop`が6 workflow編集のみのbootstrap commitへfast-forwardされ、default branchのstatic workflow登録が確認できた。
- fork `Misaki0331/mk:feature/cherrypick-compatibility-foundation`がTask 1 commitを含む新SHAへfast-forwardされ、PR #2が同期された。
- regression test `TestWorkflowBranchFiltersIncludeMisakiDevelop`がGREENで、CIで`Misaki-develop`をbaseとするPRのcheckが走る。
- core check（`build` / `test` / `lint`）がsuccess。non-core checkは分類された。
- cross-fork topology（Organization default `Misaki-develop`・fork default `develop`・parent `shiroha-a/mk`）不変。
- privacy（production由来情報・private ancestry・local pathを公開しない）、parent読み取り専用が維持され、residue 0。
- mergeは行っていない。

## 失敗時の扱い

- base SHAが取得時点から変わっていた場合、またはfast-forward pushが拒否された場合はTask 2を停止しforceしない。
- default branchのstatic workflow登録が確認できない場合はTask 3へ進まず停止する。
- core checkが失敗した場合はmergeせず、固定されたcheck名と原因categoryだけを報告する。
- どの段階でもmerge・設定変更・Issue/PR編集は行わない（PR #2のhead更新とbase `Misaki-develop`へのnarrow commitのみ）。
