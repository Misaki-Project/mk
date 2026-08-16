# Misaki-Project Branch Topology Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `Misaki0331/mk`へ`Misaki-Stable`と`Misaki-develop`を追加し、`Misaki-develop`をdefault branchにした上で、公開安全なCherryPick移行互換branchのIssueとPull Requestを作成する。

**Architecture:** repository管理操作を3段階に分離する。最初に不変の基準SHAから2 branchを作成してdefault branchを変更し、次にIssueとfeature branchを作成し、最後にPull Requestのmetadata、privacy、CIを検証する。各段階は前段のremote状態を再検証し、失敗時は後続操作を行わない。

**Tech Stack:** Git、GitHub CLI (`gh`)、PowerShell 7、GitHub Pull Requests/Issues

## Global Constraints

- 対象repositoryはfork側の`Misaki0331/mk`だけとし、fork元`shiroha-a/mk`へ書き込まない。
- `Misaki-Stable`と`Misaki-develop`は、確認済みの変更前`origin/develop` SHA `ab3bb1493ced07251a40347aa5b06c8351a5d526`から作成する。
- 既存の`develop`と`docker`は削除せず、force-pushしない。
- GitHubのdefault branchは`Misaki-develop`へ変更する。
- private branch `feature/cherrypick-compatibility-foundation`のlocal履歴はpushしない。
- 公開worktreeのHEADだけをremote `feature/cherrypick-compatibility-foundation`へpushする。
- dump、credential、local evidence、production由来の値・件数・path・hash・SQLをIssue、Pull Request、commit、artifactへ含めない。
- IssueとPull Requestのタイトル・本文は日本語にする。
- branch protectionなど、明示されていないrepository設定は変更しない。

---

### Task 1: Misaki branchを作成してdefault branchを変更する

**Files:**
- Consume: `docs/superpowers/specs/2026-08-16-misaki-branch-topology-design.md`
- Modify: なし

**Interfaces:**
- Consumes: remote `origin` = `https://github.com/Misaki0331/mk.git`
- Produces: remote branch `Misaki-Stable` at `ab3bb1493ced07251a40347aa5b06c8351a5d526`
- Produces: remote branch `Misaki-develop` at `ab3bb1493ced07251a40347aa5b06c8351a5d526`
- Produces: GitHub default branch `Misaki-develop`

- [ ] **Step 1: remote所有者と基準branchを再確認する**

Run:

```powershell
git remote get-url origin
git ls-remote --heads origin refs/heads/develop refs/heads/docker refs/heads/Misaki-Stable refs/heads/Misaki-develop
gh repo view Misaki0331/mk --json nameWithOwner,isFork,parent,defaultBranchRef
gh repo view shiroha-a/mk --json nameWithOwner,defaultBranchRef
```

Expected:

- `origin`は`Misaki0331/mk`を指す。
- `develop`は`ab3bb1493ced07251a40347aa5b06c8351a5d526`を指す。
- `docker`が存在する。
- `Misaki-Stable`と`Misaki-develop`はまだ存在しない。
- parentは`shiroha-a/mk`、default branchは`develop`。
- fork元`shiroha-a/mk`のdefault branchも`develop`。

いずれかが異なる場合はpushせず停止する。

- [ ] **Step 2: 2つのbranchを同じ基準SHAへpushする**

Run:

```powershell
git push --atomic origin ab3bb1493ced07251a40347aa5b06c8351a5d526:refs/heads/Misaki-Stable ab3bb1493ced07251a40347aa5b06c8351a5d526:refs/heads/Misaki-develop
```

Expected: 両branchが新規作成され、既存branchは更新されない。

- [ ] **Step 3: branch作成結果を検証する**

Run:

```powershell
git ls-remote --heads origin refs/heads/develop refs/heads/docker refs/heads/Misaki-Stable refs/heads/Misaki-develop
```

Expected:

- `develop`、`Misaki-Stable`、`Misaki-develop`が全て`ab3bb1493ced07251a40347aa5b06c8351a5d526`を指す。
- `docker`が変更されず存在する。

- [ ] **Step 4: default branchを変更する**

Run:

```powershell
gh repo edit Misaki0331/mk --default-branch Misaki-develop
```

Expected: exit 0。

- [ ] **Step 5: default branchとfork元非変更を検証する**

Run:

```powershell
gh repo view Misaki0331/mk --json nameWithOwner,isFork,parent,defaultBranchRef
gh repo view shiroha-a/mk --json nameWithOwner,defaultBranchRef
```

Expected:

- `Misaki0331/mk`のdefault branchは`Misaki-develop`。
- parentは引き続き`shiroha-a/mk`。
- fork元のdefault branchは操作前から変更されていない。

---

### Task 2: 日本語Issueを作成して公開feature branchをpushする

**Files:**
- Consume: `docs/superpowers/specs/2026-08-15-production-data-nondisclosure-design.md`
- Consume: `docs/superpowers/specs/2026-08-16-database-internal-orphan-repair-design.md`
- Consume: `docs/superpowers/specs/2026-08-16-misaki-branch-topology-design.md`
- Modify: なし

**Interfaces:**
- Consumes: Task 1のremote `Misaki-develop`
- Consumes: current public worktree HEAD
- Produces: open Issue `Phase 1 CherryPick移行互換基盤`
- Produces: remote branch `feature/cherrypick-compatibility-foundation` = public HEAD

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

Expected: 全categoryが安全（operator absolute path categoryはbranch追加行のみを対象に判定する）。問題があればIssue作成・pushを行わず停止する。

- [ ] **Step 3: 日本語Issueを作成する**

Run:

```powershell
$existingIssueCount = [int](gh issue list --repo Misaki0331/mk --state open --search '"Phase 1 CherryPick移行互換基盤" in:title' --json number,title --jq '[.[] | select(.title == "Phase 1 CherryPick移行互換基盤")] | length')
if ($existingIssueCount -ne 0) { throw 'matching open issue already exists' }
gh issue create --repo Misaki0331/mk --title "Phase 1 CherryPick移行互換基盤" --body "## 背景・目的
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

Expected: 新規Issue URLが返る。本文にproduction由来情報を含まない。

- [ ] **Step 4: Issueを読み戻して監査する**

Run:

```powershell
$issueNumber = gh issue list --repo Misaki0331/mk --state open --search '"Phase 1 CherryPick移行互換基盤" in:title' --json number,title --jq '.[] | select(.title == "Phase 1 CherryPick移行互換基盤") | .number'
if (@($issueNumber).Count -ne 1) { throw 'matching issue is not unique' }
gh issue view --repo Misaki0331/mk $issueNumber --json number,title,body,state,url
```

Expected: title/bodyは日本語、stateはOPEN、privacy禁止categoryは0。

- [ ] **Step 5: public HEADだけをremote feature branchへpushする**

Run:

```powershell
git push -u origin HEAD:refs/heads/feature/cherrypick-compatibility-foundation
```

Expected: remote feature branchが新規作成される。private local branchはpushされない。

- [ ] **Step 6: remote feature branchのSHAとbranch一覧を検証する**

Run:

```powershell
git ls-remote --heads origin refs/heads/feature/cherrypick-compatibility-foundation refs/heads/Misaki-Stable refs/heads/Misaki-develop refs/heads/develop refs/heads/docker
git rev-parse HEAD
```

Expected: remote feature SHAとlocal public HEADが一致し、5つの対象branchが存在する。

---

### Task 3: Pull Requestを作成してCIを確認する

**Files:**
- Consume: `docs/superpowers/specs/2026-08-16-misaki-branch-topology-design.md`
- Consume: `docs/migration-from-cherrypick.md`
- Modify: なし

**Interfaces:**
- Consumes: Task 2のIssue番号
- Consumes: remote base `Misaki-develop`
- Consumes: remote head `feature/cherrypick-compatibility-foundation`
- Produces: open Pull Request `Phase 1 CherryPick移行互換基盤`
- Produces: GitHub CI結果

- [ ] **Step 1: base/headと既存PRを確認する**

Run:

```powershell
gh pr list --repo Misaki0331/mk --state open --head feature/cherrypick-compatibility-foundation --json number,title,baseRefName,headRefName,url
git ls-remote --heads origin refs/heads/Misaki-develop refs/heads/feature/cherrypick-compatibility-foundation
```

Expected: 同じheadのopen PRはなく、base/head SHAが取得できる。

- [ ] **Step 2: Issue番号を取得する**

Run:

```powershell
gh issue list --repo Misaki0331/mk --state open --search '"Phase 1 CherryPick移行互換基盤" in:title' --json number,title,url
```

Expected: exact titleのIssueが1件だけ存在する。その番号を`$issueNumber`へ保持してStep 3で使用する。

- [ ] **Step 3: 日本語Pull Requestを作成する**

Run:

```powershell
$issueNumber = gh issue list --repo Misaki0331/mk --state open --search '"Phase 1 CherryPick移行互換基盤" in:title' --json number,title --jq '.[] | select(.title == "Phase 1 CherryPick移行互換基盤") | .number'
if (@($issueNumber).Count -ne 1) { throw 'matching issue is not unique' }
gh pr create --repo Misaki0331/mk --base Misaki-develop --head feature/cherrypick-compatibility-foundation --title "Phase 1 CherryPick移行互換基盤" --body "## 概要
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

Expected: `Misaki-develop`向けの新規Pull Request URLが返る。

- [ ] **Step 4: Pull Request metadataと本文を読み戻す**

Run:

```powershell
gh pr view --repo Misaki0331/mk feature/cherrypick-compatibility-foundation --json number,title,body,state,isDraft,baseRefName,headRefName,url,commits,files
```

Expected:

- stateはOPEN、isDraftはfalse。
- baseは`Misaki-develop`、headは`feature/cherrypick-compatibility-foundation`。
- title/bodyは日本語。
- Issue本文から取得した番号の`Closes #番号`が存在する。
- privacy禁止categoryは0。

- [ ] **Step 5: remote branchとdefault branchの最終状態を確認する**

Run:

```powershell
gh repo view Misaki0331/mk --json defaultBranchRef
git ls-remote --heads origin refs/heads/develop refs/heads/docker refs/heads/Misaki-Stable refs/heads/Misaki-develop refs/heads/feature/cherrypick-compatibility-foundation
```

Expected: default branchは`Misaki-develop`で、既存branchと新規branchが全て存在する。

- [ ] **Step 6: CIを監視する**

Run:

```powershell
gh pr checks --repo Misaki0331/mk feature/cherrypick-compatibility-foundation --watch --interval 10
```

Expected: required checksが成功する。non-required checkが失敗した場合はログを確認し、branch変更との関連を判定する。required check失敗時はmergeせず、固定されたcheck名と原因categoryだけを報告する。

- [ ] **Step 7: 完了結果を報告する**

default branch、branch作成・既存branch保持、実際のIssue URL、実際のPull Request URL、required CIの状態、privacy結果を報告する。production由来情報、local path、private branch SHA、local evidenceは報告しない。
