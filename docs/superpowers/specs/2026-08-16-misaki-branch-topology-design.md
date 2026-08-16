# Misaki-Project branch構成変更設計（cross-fork運用）

## 目的

公開安全なCherryPick移行互換feature branchを、Organization repository `Misaki-Project/mk`の`Misaki-develop`へcross-fork Pull Requestで取り込める状態にする。

## 役割分担

- actorおよびlocal commit author: `Misaki0331`
- feature branch repository: `Misaki0331/mk`（fork）のみ
- base branch・default branch・Issue・Pull Requestのrepository: `Misaki-Project/mk`（Organization）
- fork元`shiroha-a/mk`は読み取り専用のまま変更しない

Organization `Misaki-Project`自体はauthorになれない。Issue・Pull Requestのactorは`Misaki0331`として表示される（意図した状態）。

## 現在の誤状態（修正対象）

`Misaki0331/mk`（fork）に以下が誤って作成されているため、修正する。

- `Misaki-Stable`と`Misaki-develop`（誤作成branch）
- default branch `Misaki-develop`（誤）
- Issue #1 `Phase 1 CherryPick移行互換基盤`（誤作成）
- Issues有効（誤）

修正後、`Misaki0331/mk`には`feature/cherrypick-compatibility-foundation`、`develop`、`docker`だけを保持し、default branchを`develop`へ戻し、Issuesを無効化する。削除対象は誤作成の2 branchとfork Issue #1のみで、force-push・他branch削除・parentへの書き込みは行わない。

## Branch構成

### `Misaki0331/mk`（fork）

- `feature/cherrypick-compatibility-foundation`、`develop`、`docker`を保持する。
- `Misaki-Stable`と`Misaki-develop`を削除する。
- default branchを`develop`へ戻す。
- Issuesを無効化する。

### `Misaki-Project/mk`（Organization）

- 既存の`develop`、`docker`、`main`を保持する。
- `Misaki-Stable`と`Misaki-develop`を、`develop`のexact SHA `aebdad71ad09ac189a9433abc45559a1374d1c80`からatomicに作成する。
- default branchを`Misaki-develop`へ変更する。
- Issuesは既に有効であり、有効のまま維持する。

## Feature Branch

公開安全性を検証済みのfeature HEADだけを、fork `Misaki0331/mk`のremote branch `feature/cherrypick-compatibility-foundation`へpushする。

- private作業branchはpushしない。
- dump、credential、local evidence、production由来情報はpushしない。
- remote feature branchは`Misaki-Project/mk`の`Misaki-develop`をbaseとするcross-fork Pull Requestのheadだけに使用する。

## IssueとPull Request

Organization `Misaki-Project/mk`の既存Issue #1 `Phase 1 CherryPick互換基盤`を複製せず、sanitized title `Phase 1 CherryPick移行互換基盤`とsanitized本文へ更新する。Pull Request本文からそのIssueをcloseする。

Pull Requestは次の条件で作成する。

- repository: `Misaki-Project/mk`
- base: `Misaki-develop`
- head: `Misaki0331:feature/cherrypick-compatibility-foundation`（cross-fork）
- actor: `Misaki0331`
- タイトルと本文: 日本語
- 本文: 変更概要、主な変更点、検証結果、関連Issueを記載する
- production由来の値、件数、path、hash、SQL、evidenceは記載しない

## 実行順序

### Task 1: fork誤状態のcleanupとOrganization base branch作成

1. forkの誤状態（default branch、誤branch、Issues、Issue #1、保持branch）を事前確認する。
2. forkのdefault branchを`develop`へ戻す。
3. forkのdefault branch復帰を検証する。
4. fork Issue #1をexact identity確認後に削除する。
5. fork Issue #1の削除を検証する。
6. forkのIssuesを無効化する。
7. forkのIssues無効化を検証する。
8. forkの誤作成2 branchを削除する。
9. forkのpost-stateを検証する。
10. Organizationの`develop` SHA `aebdad71ad09ac189a9433abc45559a1374d1c80`と既存branchを確認する。
11. Organizationに`Misaki-Stable`と`Misaki-develop`をatomicに作成する。
12. Organizationのdefault branchを`Misaki-develop`へ変更する。
13. Organizationのpost-stateを検証する。

### Task 2: privacy/tests再実行、Organization Issue #1更新、fork feature branch fast-forward

1. public HEADとprivacy前提を再確認する。
2. tracked treeとbranch差分をprivacy監査し、テストを実行する。
3. Organization Issue #1をexact identity確認後にsanitized title/bodyへ更新する。
4. Organization Issue #1を読み戻して監査する。
5. fork feature branchをcurrent public HEADへfast-forwardする。
6. fork feature branchのSHAとbranch一覧を検証する。

### Task 3: cross-fork Pull Request作成とCI確認

1. base/headと既存PRを確認する。
2. Organization Issue #1の番号を取得する。
3. `Misaki-Project/mk`へ日本語cross-fork Pull Requestを作成する。
4. Pull Requestのmetadataと本文を読み戻す。
5. Organizationとforkの最終repository状態を確認する。
6. CIを監視する。
7. 完了結果を報告する。

## 失敗時の扱い

- forkの誤状態が事前検証と異なる場合はcleanupを実行せず停止する。
- default branch復帰に失敗した場合はIssue削除・branch削除を実行せず停止する。
- fork Issue #1のexact identityが一致しない場合は削除せず停止する。
- Issue削除に失敗した場合はIssues無効化を実行せず停止する。
- Issues無効化に失敗した場合はbranch削除を実行せず停止する。
- Organizationの`develop` SHAが`aebdad71ad09ac189a9433abc45559a1374d1c80`でない場合はbranch作成を実行せず停止する。
- Organizationのbranch作成に失敗した場合はdefault branchを変更しない。
- Organizationのdefault branch変更に失敗した場合はIssue更新・pushを実行せず停止する。
- privacy監査、base/head確認、またはCIで問題が見つかった場合はmergeしない。
- force-push、forkの誤作成2 branch以外のbranch削除、Issue #1以外のIssue削除、fork元repositoryへの書き込みは行わない。

## 完了条件

- `Misaki0331/mk`（fork）は`feature/cherrypick-compatibility-foundation`、`develop`、`docker`のみを保持し、default branchが`develop`で、Issuesが無効になっている。
- `Misaki-Project/mk`（Organization）の`Misaki-Stable`と`Misaki-develop`が`develop`のexact SHA `aebdad71ad09ac189a9433abc45559a1374d1c80`から作成されている。
- Organizationのdefault branchが`Misaki-develop`になっている。
- Organizationの既存`develop`、`docker`、`main`が保持されている。
- OrganizationのIssuesが有効のまま（`hasIssuesEnabled=true`）。
- Organization Issue #1がsanitized title `Phase 1 CherryPick移行互換基盤`とsanitized本文へ更新されている（複製なし）。
- 公開feature branchがcurrent public HEADを指している。
- `Misaki-Project/mk`に`Misaki-develop`をbaseとする日本語cross-fork Pull Requestが作成されている。
- Pull Requestのprivacy監査が通り、CI結果を確認できる。
- fork元`shiroha-a/mk`が変更されていない。
