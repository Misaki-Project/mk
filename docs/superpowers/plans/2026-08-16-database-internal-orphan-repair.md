# DB内部孤児修復 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 固有IDをDB session外へ出さず、隔離DBの孤児参照を厳格なfail-closed transactionで修復し、既存のmigration rehearsalを再開する。

**Architecture:** generic repair SQLはtracked fileや一時fileへ保存せず、実行agentのprocess memory内で構成して`psql`標準入力へ直接渡す。同じSQLをsynthetic PostgreSQLで正常系とrollback系に通した後、現在prepare済みの隔離DBへ1回だけ適用する。private HEADでresume成功後は既存の公開履歴再構成planへ戻り、public HEADでもfresh volumeから同じ検証を繰り返す。

**Tech Stack:** PostgreSQL 18、PowerShell 7、Docker、Docker Compose v2、既存CherryPick migration harness

## Global Constraints

- 本番由来ID、件数、URL、profile値をDB session外へ出さない。
- repair SQL本文をtracked file、repo外file、report、evidence、Pull Requestへ保存しない。
- repair SQLのstdoutとstderrを外部へ出さず、`repair=passed`または`repair=failed`だけを扱う。
- 本番dump、credential、private SQL、既存local evidence/logを読取・表示・stage・commit・push・uploadしない。
- repairはprepare済みの隔離DBだけへ適用し、本番DBへ直接実行しない。
- invalid JSON、空文字host、孤児banner、unsafe avatar user、事後孤児残存は全rollbackする。
- Dockerは固定digest、`--pull=never`、internal network、公開portなし、buildなしを維持する。
- synthetic dataは`synthetic-*`だけを使う。
- repair失敗時は再試行せず、`cherrypick-rehearsal-abort`で隔離resourceを削除する。
- private branch/historyはpushしない。public branchは独立review完了前にpushしない。
- frontend submoduleを変更しない。

---

### Task 1: 公開文書をDB内部snapshot方式へ整合させる

**Files:**
- Modify: `docs/migration-from-cherrypick.md`
- Modify: `docs/superpowers/specs/2026-08-15-production-data-nondisclosure-design.md`
- Modify: `docs/superpowers/plans/2026-08-15-production-data-nondisclosure.md`
- Preserve: `docs/superpowers/specs/2026-08-16-database-internal-orphan-repair-design.md`

**Interfaces:**
- Consumes: `cherrypick-rehearsal-prepare`と`cherrypick-rehearsal-resume`。
- Produces: SQL fileや固有IDをoperatorへ要求しない公開手順。
- Produces: 本追加planへの明示的な参照。

- [ ] **Step 1: staleなSQL file前提を検出する文書contractを実行する**

Run:

```powershell
$targets = @(
  'docs/migration-from-cherrypick.md',
  'docs/superpowers/specs/2026-08-15-production-data-nondisclosure-design.md',
  'docs/superpowers/plans/2026-08-15-production-data-nondisclosure.md'
)
$stale = 0
foreach ($target in $targets) {
    $text = [System.IO.File]::ReadAllText((Resolve-Path $target))
    $stale += [regex]::Matches($text, 'operator.{0,30}repo外.{0,30}SQL|repo外の非公開SQL|非公開SQL.{0,20}operator').Count
}
if ($stale -eq 0) { throw 'expected stale private SQL file instructions before documentation update' }
```

Expected: stale contractを検出するためcommand自体は成功する。検出本文やpathを表示しない。

- [ ] **Step 2: 文書を承認済み設計へ更新する**

3文書から、operatorがrepo外SQL fileを用意する記述、SQL pathを渡す記述、IDごとのallowlistを用意する記述を削除する。

代わりに次を明記する。

- prepare後は`docs/superpowers/specs/2026-08-16-database-internal-orphan-repair-design.md`に従う。
- repair SQLはcontroller process memory内だけで構成しfileへ保存しない。
- ID集合はDB内部temporary tableだけにsnapshotする。
- synthetic正常系とrollback系を先に実行する。
- repair成功時だけresumeする。
- repair失敗時はabortし、再試行しない。
- public HEADでもfresh volumeから同じフローを再実行する。

実行可能SQL、固有ID、件数、dump情報、local pathは文書へ追加しない。

- [ ] **Step 3: stale contractが消えたことを確認する**

Run:

```powershell
$targets = @(
  'docs/migration-from-cherrypick.md',
  'docs/superpowers/specs/2026-08-15-production-data-nondisclosure-design.md',
  'docs/superpowers/plans/2026-08-15-production-data-nondisclosure.md'
)
$stale = 0
foreach ($target in $targets) {
    $text = [System.IO.File]::ReadAllText((Resolve-Path $target))
    $stale += [regex]::Matches($text, 'operator.{0,30}repo外.{0,30}SQL|repo外の非公開SQL|非公開SQL.{0,20}operator').Count
}
if ($stale -ne 0) { throw "stale SQL file instructions remain: $stale" }
git diff --check
```

Expected: exit 0。match本文は出力しない。

- [ ] **Step 4: privacy scanを実行する**

Run:

```powershell
$changed = @(
  'docs/migration-from-cherrypick.md',
  'docs/superpowers/specs/2026-08-15-production-data-nondisclosure-design.md',
  'docs/superpowers/plans/2026-08-15-production-data-nondisclosure.md',
  'docs/superpowers/specs/2026-08-16-database-internal-orphan-repair-design.md',
  'docs/superpowers/plans/2026-08-16-database-internal-orphan-repair.md'
)
$matches = 0
foreach ($file in $changed) {
    if (Test-Path -LiteralPath $file -PathType Leaf) {
        $text = [System.IO.File]::ReadAllText((Resolve-Path $file))
        $matches += [regex]::Matches($text, "['\"][a-z0-9]{16}['\"]").Count
        $matches += [regex]::Matches($text, '(?i)\b[0-9a-f]{64}\b').Count
        $matches += [regex]::Matches($text, '[A-Za-z]:\\').Count
    }
}
if ($matches -ne 0) { throw "private literal categories found: $matches" }
```

Expected: exit 0。個別matchを表示しない。

- [ ] **Step 5: commitする**

```powershell
git add -- docs/migration-from-cherrypick.md docs/superpowers/specs/2026-08-15-production-data-nondisclosure-design.md docs/superpowers/plans/2026-08-15-production-data-nondisclosure.md docs/superpowers/specs/2026-08-16-database-internal-orphan-repair-design.md docs/superpowers/plans/2026-08-16-database-internal-orphan-repair.md
git diff --cached --check
git commit -m "docs: DB内部snapshot修復へ手順を更新する"
```

---

### Task 2: Generic repairをsynthetic検証してprivate rehearsalを再開する

**Files:**
- Create: none。
- Modify: none。
- Runtime only: agent process memory、synthetic Docker resource、prepare済み隔離DB。

**Interfaces:**
- Consumes: prepare済みCompose projectのDB service。
- Consumes: user-scope `CHERRYPICK_BACKUP_PATH`、`CHERRYPICK_BACKUP_SHA256`、`CHERRYPICK_CREDENTIAL_PATH`。値を表示しない。
- Produces: fixed status `repair=passed`または`repair=failed`。
- Produces: successful `cherrypick-rehearsal-resume`、またはfailure時のsuccessful `cherrypick-rehearsal-abort`。

- [ ] **Step 1: prepare済み隔離DBを固定metadataだけで検証する**

対象containerをCompose project labelとDB service labelで解決し、exactly oneを要求する。次をboolean/countだけで検証し、名前、ID、image digest、pathを出力しない。

- running containerが1件。
- networkが1件でinternal。
- published portが0件。
- imageがComposeで宣言されたDB imageと一致。
- runtime session statusが`awaiting-manual-repair`。
- `schema_migrations`が存在しない。

1つでも不一致ならrepairを実行せず`cherrypick-rehearsal-abort`でcleanupする。

- [ ] **Step 2: synthetic PostgreSQL resourceを作る**

事前に同名Task resourceが0件であることを確認する。固定名のTask専用internal networkを作り、Composeと同じDB imageを`--pull=never`、公開portなし、anonymous volumeなしで起動する。passwordはprocess内でrandom生成し、reportへ出さない。

Run shape:

```powershell
docker network create --internal mk-orphan-repair-synthetic *> $null
docker run -d --rm --pull=never --network mk-orphan-repair-synthetic --name mk-orphan-repair-synthetic-db -e POSTGRES_DB=synthetic -e POSTGRES_USER=synthetic -e "POSTGRES_PASSWORD=$syntheticPassword" $pinnedDbImage *> $null
```

実際の`$pinnedDbImage`は現在のCompose configから取得し、値を出力しない。

- [ ] **Step 3: synthetic RED fixtureを作る**

synthetic DBへ次の最小schemaを作る。

- `user(id, avatarId, bannerId, avatarUrl, avatarBlurhash, avatarDecorations, host, isDeleted, isSuspended)`
- `drive_file(id, userId)`
- `avatar_decoration(id, host)`

`synthetic-*`だけを使い、valid local/remote decoration、孤児装飾参照、安全な孤児avatar、保持すべきJSON追加fieldを投入する。

repair前に「孤児装飾0、孤児avatar0」を期待するassert queryを実行し、nonzero exitになることを確認する。query outputは破棄する。

- [ ] **Step 4: repair transactionをprocess memory内で構成する**

実行agentは承認済みspecの順序をそのまま1つのSQL stringへ構成する。SQL stringをfile、report、tool outputへ書かない。

必須block順序:

1. `BEGIN`とlock timeout。
2. 3 tableの`SHARE ROW EXCLUSIVE MODE` lock。
3. JSON row/item shape、空文字host、孤児bannerの固定message assertion。
4. unsafe avatar userの固定message assertion。
5. `ON COMMIT DROP` temporary tableへ孤児装飾IDをdistinct snapshot。
6. `ON COMMIT DROP` temporary tableへ孤児avatarのuser/avatar pairをsnapshot。
7. JSON要素順序を維持した孤児装飾参照UPDATE。
8. exact pair一致による`avatarId`、`avatarUrl`、`avatarBlurhash`のNULL UPDATE。
9. 全孤児、invalid JSON、空文字hostの固定message post assertion。
10. `COMMIT`。

全assertionは件数や個別値をerror textへ含めない。temporary tableをclientへselectしない。

- [ ] **Step 5: synthetic GREENとrollback caseを実行する**

同じin-memory SQLを`psql -v ON_ERROR_STOP=1 -q`へ標準入力で渡し、stdout/stderrをprocess内で破棄する。

正常fixtureでexit 0後、count-only assertionで次を確認する。

- 全孤児0。
- avatar関連3 fieldがNULL。
- local/remoteの有効装飾参照が維持。
- JSON配列順序と追加fieldが維持。

DBをfixtureごとに作り直し、次の各caseでnonzero exitと全row snapshot不変を確認する。

- unsafe avatar user。
- 孤児banner。
- invalid JSON row。
- invalid JSON item。

snapshot比較結果はbooleanだけを扱い、row内容を出力しない。

- [ ] **Step 6: synthetic resourceを必ずcleanupする**

success/failureにかかわらずTask専用containerとnetworkを削除する。Task label/nameに一致するcontainer、volume、networkが0件であることを確認する。

- [ ] **Step 7: prepare済み隔離DBへ1回だけ適用する**

Step 1で解決したDB containerへ、Step 4と同じprocess-memory SQLを標準入力で渡す。stdout/stderrを変数へ捕捉して表示せず、exit codeだけを評価する。

成功時は`repair=passed`だけをcontrollerへ返す。失敗時は`repair=failed`だけを返し、直ちに次を実行して停止する。

```powershell
make cherrypick-rehearsal-abort
```

- [ ] **Step 8: private rehearsalをresumeする**

User-scopeの3環境変数を値を表示せずprocess envへ読み込んでから実行する。

```powershell
make cherrypick-rehearsal-resume
```

Expected: preflight、migration 2回、health、login、network、local dump rehash、cleanupがexit 0。container、volume、network、runtime残留0。evidence内容は読まず、file存在とrunner exitだけを扱う。

- [ ] **Step 9: runtime reportをgit-ignored workspaceへ書く**

reportへ書けるのは次だけとする。

- synthetic normal/rollback各categoryのpassed/failed status。
- repair fixed status。
- resume exit status。
- residue 0 status。
- source commit SHA。

ID、件数、hash、path、SQL、stdout/stderr、evidence内容は書かない。source変更とcommitは行わない。

---

### Task 3: 公開履歴を再構成してpublic HEADを検証する

**Files:**
- Consume: `docs/superpowers/plans/2026-08-15-production-data-nondisclosure.md`
- Consume: `docs/superpowers/specs/2026-08-16-database-internal-orphan-repair-design.md`
- Consume: `docs/superpowers/plans/2026-08-16-database-internal-orphan-repair.md`

**Interfaces:**
- Consumes: private HEADで成功したTask 2。
- Produces: private履歴を祖先に持たないpublic branch。
- Produces: public HEADでfresh prepare、synthetic-verified repair、resumeが成功した状態。

- [ ] **Step 1: upstream baseからpublic worktreeを作る**

private HEAD、private worktree、git common directory、main rootをprocess変数へ保持する。main rootの`.worktrees`配下に`feature/cherrypick-compatibility-foundation-public` branchの新しいworktreeをupstream `develop`から作る。

public worktreeへprivate commitをcherry-pick、merge、rebaseしない。`git restore --source=$privateHead`でsanitizedな最終file snapshotだけを次のlogical group順に取り込み、groupごとにtest、`git diff --cached --check`、commitする。

1. Auth: `go.mod`、`go.sum`、`internal/auth/password`、変更されたsignin/i handlerとtest、password verifier gate。
2. Migration core: `cmd/migrate`、`internal/migrationcompat`、initial migrationのnote relation変更とschema/repository test。
3. Decoration migration: migration 000075 up/downとsynthetic entitycompat test。
4. Rehearsal/docs: `Makefile`、`tests/cherrypick_migration`、公開migration手順、privacy spec/plan、DB内部repair spec/plan。

新しいspecとplanをRehearsal/docs groupへ必ず含める。

- `docs/superpowers/specs/2026-08-16-database-internal-orphan-repair-design.md`
- `docs/superpowers/plans/2026-08-16-database-internal-orphan-repair.md`

各groupのcommit messageへprivate commit SHA、本番データ事実、件数、path、hashを書かない。

- [ ] **Step 2: public ancestryとprivacyを検証する**

private HEADがpublic HEADの祖先でないこと、upstream `develop`が祖先であることを`git merge-base --is-ancestor`で確認する。public branchのtracked dump、credential、evidence、log、production-style fixed ID literal、fixed dump hash、absolute local pathが0件であることをmatch本文を表示せず確認する。

- [ ] **Step 3: public HEADで全gateとfresh prepareを実行する**

public worktreeで全privacy gate、focused test、Linux vet/build、harness test、clean DB gateを再実行する。その後、User-scope環境変数を値を出さず読み込み、validateとprepareを実行する。

- [ ] **Step 4: public HEADでsynthetic検証とrepair/resumeを再実行する**

private runのcontainer、session、evidence、resultを再利用しない。次をfresh resourceで順に実行する。

1. prepare済みDBのproject/service、internal network、公開port 0、image一致、session status、migration未実行をbooleanだけで確認。
2. Task専用synthetic internal networkと固定digest PostgreSQLを`--pull=never`、公開portなしで起動。
3. synthetic normal fixtureでrepair前RED、in-memory repair後GREEN、JSON順序/field維持を確認。
4. unsafe avatar、孤児banner、invalid row、invalid itemの各fixtureでnonzero exitと全row snapshot不変を確認。
5. synthetic container、volume、networkをcleanupして残留0を確認。
6. public prepare済みDBへ同じin-memory repairを1回だけ適用し、fixed statusだけを扱う。
7. repair成功時だけresumeし、preflight、migration 2回、health、login、network、rehash、cleanupのexit 0を確認。
8. repair失敗時はabortして停止し、public branchをpushしない。

- [ ] **Step 5: final review前監査を実行する**

public tree/history、staged content、tracked blob、submodule、PR本文案をprivacy観点で監査する。match本文を表示せずcategory countだけを扱う。Critical/Important findingがあればpushしない。

- [ ] **Step 6: controllerへhandoffする**

public branchをpushせず、次だけをcontrollerへ返す。

- public HEAD code SHA。
- test/gate category status。
- repair/resume fixed status。
- privacy review status。
- residue 0 status。
- report path。

controllerは独立whole-branch review完了後にだけpushとPull Request作成を行う。
