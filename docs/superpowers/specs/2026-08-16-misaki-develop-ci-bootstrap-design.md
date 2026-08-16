# Misaki-develop CI bootstrap設計

## 目的

PR #2 `Phase 1 CherryPick移行互換基盤`（`Misaki-Project/mk` base `Misaki-develop` / head `Misaki0331:feature/cherrypick-compatibility-foundation`）で**0件のcheck**しか報告されていない問題を解消し、`Misaki-develop`をbaseとするcross-fork Pull RequestへCI（required check）を走らせられるようにする。**NO CHECKは受け入れない。**

## 現在の状態（問題）

- `gh pr checks`は「no checks reported on the 'feature/cherrypick-compatibility-foundation' branch」を返す。
- head SHA `0cfeb133d98250d7f026d1ff127ba9e733503df2` のcheck-runs総数は0、actions runs総数も0。
- 原因は2点。
  1. **trigger filter不一致**: 全PRトリガーworkflowの`pull_request.branches`が`[main, develop]`（または`[develop, main]`）のみで、base `Misaki-develop`がどのworkflowとも一致しない。このためworkflowが登録されていても発火しない。
  2. **static workflow未登録**: Organization `Misaki-Project/mk`のworkflow一覧（actions/workflows API）はDependabot動的workflow `Dependency Graph`のみで、static workflow（ci.yml等）が未登録。fork `Misaki0331/mk`ではstatic workflowが登録済みであることと対照的。
- `Misaki-develop`にはbranch protectionもrulesetもなく、required checkも定義されていない。
- マージ可否はMERGEABLE/CLEANであり、CIが無いことが緑に見える状態（false green）。

## 承認済みアーキテクチャ

1. `Misaki0331` authorの**narrow bootstrap commit**を、`Misaki-Project/mk:Misaki-develop`へ**fast-forward**で直接pushする。
2. fork feature head側にも**同一のworkflow filter変更**を入れ、PR #2のdiffでfilterがrevertされないようにする。
3. CIを守る**static Go regression test**を追加し、今後`Misaki-develop`が`pull_request.branches`から外れる変更をCIで検知する。
4. baseを先に更新してdefault branchのworkflow登録を確認し、featureを後から更新してPR #2を同期し、CIを監視する。

PRを作り直すわけではない。既存PR #2をそのまま使い、headを更新してCIを発火させる。

## 変更対象（exactly 6 workflow）

`pull_request.branches`の一覧へ`Misaki-develop`を**追加する**。既存エントリ（`main`、`develop`）は保持し、順序は既存のものを維持する。**push filter・jobs・permissions・paths・actions・他workflowは変更しない。**

| File | 現状の `pull_request.branches` | 変更後 |
|---|---|---|
| `.github/workflows/ci.yml` | `[main, develop]` | `[main, develop, Misaki-develop]` |
| `.github/workflows/diff-e2e.yml` | `[develop, main]` | `[develop, main, Misaki-develop]` |
| `.github/workflows/docker.yml` | `[main, develop]` | `[main, develop, Misaki-develop]` |
| `.github/workflows/dropin-e2e.yml` | `[develop, main]` | `[develop, main, Misaki-develop]` |
| `.github/workflows/playwright.yml` | `[develop, main]` | `[develop, main, Misaki-develop]` |
| `.github/workflows/upstream-backend-e2e.yml` | `[develop, main]` | `[develop, main, Misaki-develop]` |

- `ci.yml`と`docker.yml`は`push.branches`も持つが、**push側は変更しない**（`Misaki-develop`へpushした際に無駄に二重実行されないように、push triggerは現状の`[main, develop]`のまま維持）。
- `.github/workflows/docker-branch.yml`は`pull_request` triggerを持たないpush専用workflow（`push.branches: [develop]`）のため**対象外**。テストでも対象にしない。
- `.github/workflows/dropin-frontend-e2e.yml`と`.github/workflows/queue-bench-smoke.yml`は`pull_request` triggerを持たない（schedule / workflow_dispatchのみ）ため対象外。テストでも対象にしない。

### 変更しないもの

- `on.push.branches`（ci.yml / docker.yml / docker-branch.yml）
- workflow内のjobs、permissions、paths、uses、actions、env
- `.github/workflows/`以下の上記以外のファイル
- 他repository設定（branch protection等）

## TDD（regression test）

### Test file

`internal/entitycompat/workflow_branch_filter_test.go`（新規、`package entitycompat`）

### 要件

- `gopkg.in/yaml.v3`（既存の直接依存、`go.mod`記載済み）で各workflowをパースする。
- **各YAMLの`pull_request`ブロックをisolateする**。`on.pull_request.branches`を明示的に辿り、その一覧に`Misaki-develop`が含まれることを要求する。**`on.push.branches`を見てはいけない**。これにより、push-onlyのbranchエントリ（例: `docker-branch.yml`の`push.branches`へ`Misaki-develop`を足しただけ）がfalse passを作れない。
- 対象は**exactly 6 file**。上記6 fileをハードコードし、それぞれについて以下を検証する。
  - `on.pull_request`ブロックが存在すること。
  - `on.pull_request.branches`が存在し、要素に`Misaki-develop`が含まれること。
- 対象外workflow（`docker-branch.yml`等）は検査対象に含めない。
- テストはworkflow fileをrepo root相対パス（`../../.github/workflows/...`）で読む。CIのtest-shardsは`internal/entitycompat`を包むので、このtestはPR CIで必ず実行される。

### RED / GREEN手順

1. **RED**: 変更前のcode（`pull_request.branches`が`[main, develop]`等）でtestを追加して実行する。6 fileすべてで`Misaki-develop`不在によりFAILすることを確認する。
   - 実行: `go test ./internal/entitycompat -run TestWorkflowBranchFiltersIncludeMisakiDevelop -count=1`
2. 6 workflowの`pull_request.branches`へ`Misaki-develop`を追加する。
3. **GREEN**: 上記testを再実行しPASSすることを確認する。

## コミット構成

### Base bootstrap commit（`Misaki-Project/mk:Misaki-develop`へ直接）

- 含める変更: **6 workflowの`pull_request.branches`編集のみ**。
- regression test・設計/計画docsは**含めない**（後述のfeature側に含める）。
- author: `Misaki0331`。

### Feature commit（`Misaki0331/mk:feature/cherrypick-compatibility-foundation`のhead）

- 含める変更:
  - 上記6 workflowと**同一の**`pull_request.branches`編集（base側とdiffで消えないように同一内容）
  - `internal/entitycompat/workflow_branch_filter_test.go`（regression test）
  - `docs/superpowers/specs/2026-08-16-misaki-develop-ci-bootstrap-design.md`（本設計）
  - 対応するplanドキュメント
- author: `Misaki0331`。

## Base bootstrap手順

1. **disposableな隔離worktree/clone**を、Organization `Misaki-Project/mk`の`Misaki-develop` **exact current SHA**から作成する。既存worktreeや作業ツリーを汚さない。
   - 実行時点の`Misaki-develop` SHAを`git ls-remote https://github.com/Misaki-Project/mk.git refs/heads/Misaki-develop`で取得し、そのSHAから`git worktree add`または`git clone`する。
2. 6 workflowの`pull_request.branches`編集のみを適用する。
3. `git diff --check`、`git diff`で**6 fileだけ**が変わっていることを確認する（他ファイル・他変更なし）。
4. 作業対象のSHAが取得時点から変わっていないこと（remoteがまだ同じSHA）を確認する。
5. **fast-forward push**（`--force`不使用）で`Misaki-Project/mk:Misaki-develop`へ反映する。
   - `git push https://github.com/Misaki-Project/mk.git <local-ref>:refs/heads/Misaki-develop`
   - non-fast-forwardで拒否された場合は停止する（forceしない）。
6. default branch `Misaki-develop`の**workflow登録**を確認する。
   - `gh api "repos/Misaki-Project/mk/actions/workflows"`に`ci.yml`等のstatic workflowが現れることを確認する。
   - 現れない場合は停止し、mergeしない。

## Push order（feature / PR同期）

1. **base先**: base bootstrap commitを`Misaki-Project/mk:Misaki-develop`へfast-forward push。
2. **workflow登録確認**: default branchのstatic workflow登録を確認。
3. **feature後**: fork feature headへfeature commitを追加して`Misaki0331/mk:feature/cherrypick-compatibility-foundation`へpush。
4. **PR #2同期確認**: `gh pr list --repo Misaki-Project/mk --state open --head feature/cherrypick-compatibility-foundation`でPR番号を解決し、headが新SHAへ更新されたこと、baseが`Misaki-develop`のままであることを確認する。
5. **CI監視**: `gh pr checks --repo Misaki-Project/mk $prNumber --watch --interval 10`。
   - required checkが成功することを確認する。
   - workflow登録が失敗、またはrequired checkが失敗した場合は**mergeしない**。

## エラー処理

- base SHAが取得時点から変わっていた場合: pushせず停止。
- fast-forward pushが拒否された場合: forceせず停止。
- default branchのworkflow登録に失敗した場合: feature push・PR同期を行わず停止。
- CIのrequired checkが失敗した場合: mergeせず、固定されたcheck名と原因categoryだけを報告する。
- non-required checkが失敗した場合: ログを確認し、branch変更との関連を判定する。required checkは待つ。
- どの段階でもmerge・設定変更・issue/PR編集は行わない（PR #2のhead更新を除く）。

## 検証・cleanup・完了条件

### 検証

- 6 workflowそれぞれで`pull_request.branches`に`Misaki-develop`が含まれる（regression test GREEN）。
- `docker-branch.yml`等の対象外workflowは変更されていない。
- base commitのdiffが6 workflowの`pull_request.branches`編集のみ。
- PR #2のheadが更新され、CIでrequired checkが走り、成功する。
- regression testが6 fileすべてをisolateした`pull_request`ブロックで検査するため、push-only entryによるfalse passがない。

### cleanup

- disposable worktree/cloneを`git worktree remove`またはclone dir削除で撤去する。
- 公開feature worktreeの`git status --short`が空、HEADが変更前（`0cfeb133d98250d7f026d1ff127ba9e733503df2`）のままであること（本設計の実装でfeature headは更新されるが、worktree作業ツリー自体はclean）。
- 残存artifact・residue 0。

### 完了条件

- `Misaki-Project/mk:Misaki-develop`がbase bootstrap commitへfast-forwardされている。
- default branchのstatic workflow登録が確認でき、`Misaki-develop`をbaseとするPRでCIが発火する。
- PR #2のheadが更新され、required checkが成功する。
- regression test `TestWorkflowBranchFiltersIncludeMisakiDevelop`がGREENで維持される。
- cross-fork topology（Organization default `Misaki-develop`・fork default `develop`・parent `shiroha-a/mk`）が不変。
- privacy（production由来情報・private ancestry・local pathを公開しない）、parent読み取り専用が維持される。
- mergeは本設計の範囲外（本設計はCI bootstrapまで）。

## 代替案

- **NO CHECKを受け入れる（却下）**: false greenが続き、`Misaki-develop`をbaseとするPRの回帰を検知できない。本設計の前提として却下。
- **別bootstrap PRでワークフローを変更する（却下）**: bootstrap PR自体がbase `Misaki-develop`であり、変更前はtrigger filterの外にあるため、**CIが発火せず**、同じNO CHECK問題を持つ。直接のnarrow commitでbaseへ入れる本設計が必須。
- **push triggerへ`Misaki-develop`を追加する（却下）**: `push.branches`を広げると`Misaki-develop`へのpushで無駄な二重CIが走る。`pull_request.branches`のみの変更に限定する。

## 守る制約（既存planから継承）

- actor/local commit authorは`Misaki0331`のみ。
- `Misaki-Project/mk`はbase branch・default branch・Issue・Pull Request用。`Misaki0331/mk`はfeature branch用。
- fork元`shiroha-a/mk`へは一切書き込まない（読み取り専用のまま）。
- production由来の値・件数・path・hash・SQL、private branch履歴、local evidenceを公開しない。
- force-push、本設計で明示した操作以外のrepository設定変更は行わない。
