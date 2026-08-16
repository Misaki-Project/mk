# Misaki-Project Branch Topology Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** fork `Misaki0331/mk`の誤作成状態をcleanupし、Organization `Misaki-Project/mk`へ`Misaki-Stable`と`Misaki-develop`を追加して`Misaki-develop`をdefault branchにした上で、公開安全なCherryPick移行互換feature branchのIssueとcross-fork Pull Requestを作成する。

**Architecture:** repository管理操作を3段階に分離する。最初にforkの誤状態（default branch、誤branch、Issues、Issue #1）を事前検証付きでcleanupし、Organizationのbase branchを作成してdefault branchを変更する。次にprivacy/testsを再実行し、既存Organization Issueを更新してfork feature branchをcurrent public HEADへfast-forwardする。最後にOrganizationへcross-fork Pull Requestを作成し、metadata、privacy、CIを検証する。各段階は前段のremote状態を再検証し、失敗時は後続操作を行わない。

**Tech Stack:** Git、GitHub CLI (`gh`)、PowerShell 7、GitHub Pull Requests/Issues

## Global Constraints

- actorおよびlocal commit authorは`Misaki0331`のみ。Organization `Misaki-Project`はauthorになれず、Issue/PRのactorは`Misaki0331`として表示される（意図した状態）。
- feature branch repositoryはfork `Misaki0331/mk`だけ。base branch・default branch・Issue・Pull Requestのrepositoryは`Misaki-Project/mk`。
- fork元`shiroha-a/mk`へは一切書き込まない（読み取り専用のまま）。
- `Misaki0331/mk`で保持するのは`feature/cherrypick-compatibility-foundation`、`develop`、`docker`のみ。default branchを`develop`へ戻し、Issuesを無効化する。
- forkの誤作成branch削除は`Misaki-Stable`と`Misaki-develop`の2つだけ。Issue削除はfork Issue #1のみ（exact repo/title/body identity確認後）。
- `Misaki-Project/mk`の`Misaki-Stable`と`Misaki-develop`は、`develop`のexact SHA `aebdad71ad09ac189a9433abc45559a1374d1c80`からatomicに作成する。
- `Misaki-Project/mk`の既存`develop`、`docker`、`main`は削除せず保持する。default branchは`Misaki-develop`へ変更する。Issuesは既に有効であり、有効のまま維持する。
- private branch `feature/cherrypick-compatibility-foundation`のlocal履歴はpushしない。
- forkの公開worktree HEADだけをremote `feature/cherrypick-compatibility-foundation`へpushする。
- dump、credential、local evidence、production由来の値・件数・path・hash・SQLをIssue、Pull Request、commit、artifactへ含めない。
- IssueとPull Requestのタイトル・本文は日本語にする。Organization Issue #1の更新本文はsanitized本文のみを使用する。
- force-push、削除対象外branchの削除、Issue #1以外のIssue削除は行わない。
- `gh`のmutationは全て明示的な`--repo`引数を付ける。cross-fork PRのheadは`Misaki0331:feature/cherrypick-compatibility-foundation`形式を使う。
- branch protectionなど、本planで明示した操作（fork default branch復帰・Issues無効化、fork誤branch/Issue削除、Organization branch作成・default branch変更、Issue更新、PR作成）以外のrepository設定は変更しない。

---

### Task 1: fork誤状態をcleanupしてOrganization base branchを作成する

**Files:**
- Consume: `docs/superpowers/specs/2026-08-16-misaki-branch-topology-design.md`
- Modify: なし

**Interfaces:**
- Consumes: fork `Misaki0331/mk`（誤状態）、Organization `Misaki-Project/mk`
- Produces: fork `Misaki0331/mk` の default branch `develop`・Issues無効・誤branch/Issue削除（feature/develop/dockerのみ保持）
- Produces: Organization `Misaki-Project/mk` の `Misaki-Stable`・`Misaki-develop` at `aebdad71ad09ac189a9433abc45559a1374d1c80`、default branch `Misaki-develop`

- [ ] **Step 1: forkの誤状態を事前確認する**

Run:

```powershell
gh repo view Misaki0331/mk --json nameWithOwner,isFork,parent,defaultBranchRef,hasIssuesEnabled
git ls-remote --heads origin refs/heads/develop refs/heads/docker refs/heads/feature/cherrypick-compatibility-foundation refs/heads/Misaki-Stable refs/heads/Misaki-develop
gh issue view --repo Misaki0331/mk 1 --json number,title,body,state
```

Expected:

- `Misaki0331/mk`のdefault branchは`Misaki-develop`（誤）、`hasIssuesEnabled`は`true`（誤）。
- `develop`、`docker`、`feature/cherrypick-compatibility-foundation`、`Misaki-Stable`、`Misaki-develop`が存在する。
- Issue #1が存在し、title=`Phase 1 CherryPick移行互換基盤`、state=OPEN、bodyがsanitized本文と完全一致する。

いずれかが異なる場合はcleanupせず停止する。

- [ ] **Step 2: forkのdefault branchを`develop`へ戻す**

Run:

```powershell
gh repo edit --repo Misaki0331/mk --default-branch develop
```

Expected: exit 0。

- [ ] **Step 3: forkのdefault branch復帰を検証する**

Run:

```powershell
gh repo view Misaki0331/mk --json nameWithOwner,isFork,parent,defaultBranchRef
```

Expected:

- `Misaki0331/mk`のdefault branchは`develop`。
- parentは引き続き`shiroha-a/mk`。

- [ ] **Step 4: fork Issue #1をexact identity確認後に削除する**

Run:

```powershell
$issue = gh issue view --repo Misaki0331/mk 1 --json number,title,body,state
# number==1, title=='Phase 1 CherryPick移行互換基盤', state=='OPEN', body==sanitized本文 を検査する
# 一致しない場合は削除せず停止する
gh issue delete --repo Misaki0331/mk 1 --yes
```

Expected: exit 0、Issue #1が削除される。

- [ ] **Step 5: fork Issue #1の削除を検証する**

Run:

```powershell
gh issue view --repo Misaki0331/mk 1
```

Expected: Issueが存在せず失敗（exit 1相当）。存在する場合は停止する。

- [ ] **Step 6: forkのIssuesを無効化する**

Run:

```powershell
gh repo edit --repo Misaki0331/mk --enable-issues=false
```

Expected: exit 0。

- [ ] **Step 7: forkのIssues無効化を検証する**

Run:

```powershell
gh repo view Misaki0331/mk --json nameWithOwner,defaultBranchRef,hasIssuesEnabled
```

Expected: `hasIssuesEnabled`は`false`、default branchは`develop`。

- [ ] **Step 8: forkの誤作成2 branchを削除する**

Run:

```powershell
git push origin --delete refs/heads/Misaki-Stable refs/heads/Misaki-develop
```

Expected: 両branchが削除される。`feature/cherrypick-compatibility-foundation`、`develop`、`docker`は削除されない。

- [ ] **Step 9: forkのpost-stateを検証する**

Run:

```powershell
gh repo view Misaki0331/mk --json nameWithOwner,isFork,parent,defaultBranchRef,hasIssuesEnabled
git ls-remote --heads origin refs/heads/develop refs/heads/docker refs/heads/feature/cherrypick-compatibility-foundation refs/heads/Misaki-Stable refs/heads/Misaki-develop
```

Expected:

- default branchは`develop`、`hasIssuesEnabled`は`false`。
- `develop`、`docker`、`feature/cherrypick-compatibility-foundation`のみ存在する。
- `Misaki-Stable`と`Misaki-develop`は存在しない。
- parentは引き続き`shiroha-a/mk`。

- [ ] **Step 10: Organizationのbase stateを事前確認する**

Run:

```powershell
gh repo view Misaki-Project/mk --json nameWithOwner,isFork,parent,defaultBranchRef,hasIssuesEnabled
git ls-remote https://github.com/Misaki-Project/mk.git refs/heads/develop refs/heads/docker refs/heads/main refs/heads/Misaki-Stable refs/heads/Misaki-develop
```

Expected:

- `Misaki-Project/mk`の`hasIssuesEnabled`は`true`、parentは`shiroha-a/mk`。
- `develop`は`aebdad71ad09ac189a9433abc45559a1374d1c80`、`docker`と`main`が存在する。
- `Misaki-Stable`と`Misaki-develop`はまだ存在しない。

いずれかが異なる場合はbranch作成せず停止する。

- [ ] **Step 11: Organizationに2 branchをatomicに作成する**

Run:

```powershell
git fetch https://github.com/Misaki-Project/mk.git refs/heads/develop
if ((git rev-parse FETCH_HEAD) -ne 'aebdad71ad09ac189a9433abc45559a1374d1c80') { throw 'develop SHA mismatch' }
git push --atomic https://github.com/Misaki-Project/mk.git aebdad71ad09ac189a9433abc45559a1374d1c80:refs/heads/Misaki-Stable aebdad71ad09ac189a9433abc45559a1374d1c80:refs/heads/Misaki-develop
```

Expected: 両branchが新規作成され、既存branchは更新されない。

- [ ] **Step 12: Organizationのdefault branchを`Misaki-develop`へ変更する**

Run:

```powershell
gh repo edit --repo Misaki-Project/mk --default-branch Misaki-develop
```

Expected: exit 0。

- [ ] **Step 13: Organizationのpost-stateを検証する**

Run:

```powershell
gh repo view Misaki-Project/mk --json nameWithOwner,isFork,parent,defaultBranchRef,hasIssuesEnabled
git ls-remote https://github.com/Misaki-Project/mk.git refs/heads/develop refs/heads/docker refs/heads/main refs/heads/Misaki-Stable refs/heads/Misaki-develop
```

Expected:

- default branchは`Misaki-develop`、`hasIssuesEnabled`は`true`。
- `develop`、`docker`、`main`、`Misaki-Stable`、`Misaki-develop`が全て存在する。
- parentは引き続き`shiroha-a/mk`。

---

### Task 2: privacy/testsを再実行しOrganization Issue #1を更新してfeature branchをfast-forwardする

**Files:**
- Consume: `docs/superpowers/specs/2026-08-15-production-data-nondisclosure-design.md`
- Consume: `docs/superpowers/specs/2026-08-16-database-internal-orphan-repair-design.md`
- Consume: `docs/superpowers/specs/2026-08-16-misaki-branch-topology-design.md`
- Modify: なし

**Interfaces:**
- Consumes: Organization Issue #1（更新前title `Phase 1 CherryPick互換基盤`）
- Consumes: current public worktree HEAD
- Produces: 更新済みOrganization Issue #1 `Phase 1 CherryPick移行互換基盤`
- Produces: fork feature branch `feature/cherrypick-compatibility-foundation` = current public HEAD

- [ ] **Step 1: public HEADとprivacy前提を再確認する**

Run:

```powershell
git status --short
git rev-parse HEAD
git merge-base --is-ancestor origin/develop HEAD
$privateIsAncestor = $false
git merge-base --is-ancestor 65e2940da611da49fbb1289600d65b3bba09c5ef HEAD
if ($LASTEXITCODE -eq 0) { $privateIsAncestor = $true }
if ($privateIsAncestor) { throw 'private history is reachable from public HEAD' }
git diff --check origin/develop...HEAD
```

Expected:

- worktreeはclean。
- `origin/develop`はHEADの祖先。
- private HEADはpublic HEADの祖先ではない。private ancestry checkはexit 1が成功条件。
- diff checkはclean。

- [ ] **Step 2: tracked treeとbranch差分をprivacy監査する**

Run:

```powershell
$addedFiles = @(git diff --diff-filter=A --name-only origin/develop...HEAD)
$artifactNameCount = @($addedFiles | Where-Object { $_ -match '(?i)(\.dump$|\.backup$|\.sql\.gz$|credential|evidence|session\.json$|password\.txt$|\.log$)' }).Count
$forbiddenSourceCount = @(git grep -l -E 'approvedAvatarPairs|approvedPairsSQL|deterministic_data_hash' HEAD -- migration tests/cherrypick_migration/aggregate.sql tests/cherrypick_migration/verify.ps1 tests/cherrypick_migration/compose.yml docs/migration-from-cherrypick.md 2>$null).Count
$addedDiffLines = @(git diff --unified=0 origin/develop...HEAD -- docs migration internal cmd tests/cherrypick_migration/verify.ps1 tests/cherrypick_migration/compose.yml ':(exclude)docs/superpowers/plans/2026-08-16-misaki-branch-topology.md' 2>$null | Where-Object { $_ -match '^\+(?!\+\+\+)' })
$absoluteOperatorPathCount = @($addedDiffLines | Where-Object { $_ -cmatch '([A-Za-z]:\\Users\\[^<]|/home/[^<]+/|/Users/[^<]+/)' }).Count
go test ./internal/entitycompat -run TestCherryPickAvatarDecorationMigration -count=1
pwsh -NoProfile -File tests/cherrypick_migration/verify.test.ps1
if ($artifactNameCount -ne 0 -or $forbiddenSourceCount -ne 0 -or $absoluteOperatorPathCount -ne 0) { throw 'privacy category count is nonzero' }
```

operator absolute path gateは`origin/develop...HEAD`の**branch追加行のみ**を検査し、`git grep`によるHEAD全体走査は行わない。`$addedDiffLines`は`git diff --unified=0`の`+`始まりの追加行のみ（hunk headerの`+++`は除外）を対象とし、source path scopeは`docs`、`migration`、`internal`、`cmd`、`tests/cherrypick_migration/verify.ps1`、`tests/cherrypick_migration/compose.yml`に限定する。本planファイル自体はpathspec excludeで対象外とし（文書化したregexのself-match防止）、`tests/cherrypick_migration/verify.test.ps1`も意図的なsynthetic negative fixtureのため対象外のままとする。operator path regexは`-cmatch`（大文字小文字区別あり）で適用し、従来の`git grep -E`と同義の判定を維持する。match本文は表示せず、category件数だけを判定する。

Required categories:

```text
tracked dump/backup files = 0
tracked credential/evidence/log files = 0
branch-added backup hash literals = 0
branch-added production-style fixed ID literals = 0
branch-added operator absolute paths = 0
private commit ancestry = false
```

Expected: 全categoryが安全（operator absolute path categoryはbranch追加行のみを対象に判定する）。問題があればIssue更新・pushを行わず停止する。

- [ ] **Step 3: Organization Issue #1をexact identity確認後にsanitized title/bodyへ更新する**

Run:

```powershell
$issue = gh issue view --repo Misaki-Project/mk 1 --json number,title,state
# number==1, title=='Phase 1 CherryPick互換基盤', state=='OPEN' を検査する
# 一致しない場合は更新せず停止する
gh issue edit --repo Misaki-Project/mk 1 --title "Phase 1 CherryPick移行互換基盤" --body "## 背景・目的
CherryPickからmk-goへ移行する際の認証・migration互換性と、個別データを公開しない隔離検証手順を整備します。

## 作業内容
- Argon2id認証の読み取り互換とbcrypt書き戻しを追加する
- migrationの排他制御と検出専用preflightを追加する
- 装飾参照の安全なcleanup migrationを追加する
- count-onlyの隔離移行rehearsalを追加する
- CherryPick移行手順と非開示設計を文書化する

## 影響範囲
- signinとpassword確認処理
- migration CLIとmigration SQL
- CherryPick移行用の検証harnessと文書

## 完了条件
- [ ] 認証互換testが通る
- [ ] migration互換testとclean DB検証が通る
- [ ] 隔離rehearsalとprivacy gateが通る
- [ ] production由来情報をcommit・Issue・PRへ含めない

## 関連文書
- docs/migration-from-cherrypick.md
- docs/superpowers/specs/2026-08-15-production-data-nondisclosure-design.md
- docs/superpowers/specs/2026-08-16-database-internal-orphan-repair-design.md"
```

Expected: exit 0、既存Issue #1が更新され、新規Issueは作成されない（複製なし）。

- [ ] **Step 4: Organization Issue #1を読み戻して監査する**

Run:

```powershell
$issueNumber = gh issue list --repo Misaki-Project/mk --state open --search '"Phase 1 CherryPick移行互換基盤" in:title' --json number,title --jq '.[] | select(.title == "Phase 1 CherryPick移行互換基盤") | .number'
if (@($issueNumber).Count -ne 1) { throw 'matching issue is not unique' }
gh issue view --repo Misaki-Project/mk $issueNumber --json number,title,body,state,url
```

Expected: title/bodyは日本語、stateはOPEN、privacy禁止categoryは0。

- [ ] **Step 5: fork feature branchをcurrent public HEADへfast-forwardする**

Run:

```powershell
git push origin HEAD:refs/heads/feature/cherrypick-compatibility-foundation
```

Expected: fork feature branchがcurrent public HEADへfast-forwardされる。private local branchはpushされない。既に一致していればno-op。

- [ ] **Step 6: fork feature branchのSHAとbranch一覧を検証する**

Run:

```powershell
git ls-remote --heads origin refs/heads/feature/cherrypick-compatibility-foundation refs/heads/develop refs/heads/docker refs/heads/Misaki-Stable refs/heads/Misaki-develop
git rev-parse HEAD
```

Expected: fork feature SHAとlocal public HEADが一致し、forkには`feature/cherrypick-compatibility-foundation`、`develop`、`docker`の3 branchのみ存在する。

---

### Task 3: cross-fork Pull Requestを作成してCIを確認する

**Files:**
- Consume: `docs/superpowers/specs/2026-08-16-misaki-branch-topology-design.md`
- Consume: `docs/migration-from-cherrypick.md`
- Modify: なし

**Interfaces:**
- Consumes: 更新済みOrganization Issue #1
- Consumes: Organization base `Misaki-develop`
- Consumes: fork head `Misaki0331:feature/cherrypick-compatibility-foundation`
- Produces: open Pull Request `Phase 1 CherryPick移行互換基盤`
- Produces: GitHub CI結果

- [ ] **Step 1: base/headと既存PRを確認する**

Run:

```powershell
gh pr list --repo Misaki-Project/mk --state open --head Misaki0331:feature/cherrypick-compatibility-foundation --json number,title,baseRefName,headRefName,url
git ls-remote https://github.com/Misaki-Project/mk.git refs/heads/Misaki-develop
git ls-remote --heads origin refs/heads/feature/cherrypick-compatibility-foundation
```

Expected: 同じheadのopen PRはなく、base/head SHAが取得できる。

- [ ] **Step 2: Organization Issue番号を取得する**

Run:

```powershell
gh issue list --repo Misaki-Project/mk --state open --search '"Phase 1 CherryPick移行互換基盤" in:title' --json number,title,url
```

Expected: exact titleのIssueが1件だけ存在する。その番号を`$issueNumber`へ保持してStep 3で使用する。

- [ ] **Step 3: 日本語cross-fork Pull Requestを作成する**

Run:

```powershell
$issueNumber = gh issue list --repo Misaki-Project/mk --state open --search '"Phase 1 CherryPick移行互換基盤" in:title' --json number,title --jq '.[] | select(.title == "Phase 1 CherryPick移行互換基盤") | .number'
if (@($issueNumber).Count -ne 1) { throw 'matching issue is not unique' }
gh pr create --repo Misaki-Project/mk --base Misaki-develop --head Misaki0331:feature/cherrypick-compatibility-foundation --title "Phase 1 CherryPick移行互換基盤" --body "## 概要
CherryPickからmk-goへ移行するための認証・migration互換基盤と、privacyを維持する隔離検証手順を追加します。

## 主な変更点
- Argon2id認証を読み取り可能にし、password更新時はbcryptへ統一
- migration advisory lock、DB URL処理、検出専用preflightを追加
- 不正・孤児装飾参照をfail-closedで扱うcleanup migrationを追加
- note reply・renote relationのschema互換を調整
- count-onlyの隔離rehearsalと移行文書を追加
- Misaki-Projectのbranch運用設計を文書化

## 検証
- Go全package test
- Linux向けvet・build
- migration互換testとclean DB・incremental migration検証
- rehearsal harness static test
- synthetic rollback・snapshot不変検証
- privacy・履歴・submodule監査

## その他
- fork側が未取得のupstream更新commitも同じPull Requestに含まれます
- production由来の個別情報はcommitおよび本文に含めていません

Closes #$issueNumber"
```

Expected: `Misaki-Project/mk`の`Misaki-develop`向け新規cross-fork Pull Request URLが返る。

- [ ] **Step 4: Pull Request metadataと本文を読み戻す**

Run:

```powershell
gh pr view --repo Misaki-Project/mk Misaki0331:feature/cherrypick-compatibility-foundation --json number,title,body,state,isDraft,baseRefName,headRefName,url,commits,files
```

Expected:

- stateはOPEN、isDraftはfalse。
- baseは`Misaki-develop`、headは`Misaki0331:feature/cherrypick-compatibility-foundation`。
- title/bodyは日本語。
- Issue本文から取得した番号の`Closes #番号`が存在する。
- privacy禁止categoryは0。

- [ ] **Step 5: Organizationとforkの最終repository状態を確認する**

Run:

```powershell
gh repo view Misaki-Project/mk --json nameWithOwner,isFork,parent,defaultBranchRef,hasIssuesEnabled
gh repo view Misaki0331/mk --json nameWithOwner,isFork,parent,defaultBranchRef,hasIssuesEnabled
git ls-remote https://github.com/Misaki-Project/mk.git refs/heads/develop refs/heads/docker refs/heads/main refs/heads/Misaki-Stable refs/heads/Misaki-develop
git ls-remote --heads origin refs/heads/develop refs/heads/docker refs/heads/feature/cherrypick-compatibility-foundation refs/heads/Misaki-Stable refs/heads/Misaki-develop
```

Expected:

- `Misaki-Project/mk`: default branchは`Misaki-develop`、`hasIssuesEnabled`は`true`、`develop`/`docker`/`main`/`Misaki-Stable`/`Misaki-develop`が全て存在。
- `Misaki0331/mk`: default branchは`develop`、`hasIssuesEnabled`は`false`、`develop`/`docker`/`feature/cherrypick-compatibility-foundation`のみ存在。
- 両repoのparentは`shiroha-a/mk`のまま。

- [ ] **Step 6: CIを監視する**

Run:

```powershell
gh pr checks --repo Misaki-Project/mk Misaki0331:feature/cherrypick-compatibility-foundation --watch --interval 10
```

Expected: required checksが成功する。non-required checkが失敗した場合はログを確認し、branch変更との関連を判定する。required check失敗時はmergeせず、固定されたcheck名と原因categoryだけを報告する。

- [ ] **Step 7: 完了結果を報告する**

Organizationのdefault branch、fork default branch復帰、Organization branch作成・既存branch保持、fork誤branch/Issue削除・Issues無効化、Issues有効状態、実際のIssue URL、実際のPull Request URL、required CIの状態、privacy結果を報告する。production由来情報、local path、private branch SHA、local evidenceは報告しない。
