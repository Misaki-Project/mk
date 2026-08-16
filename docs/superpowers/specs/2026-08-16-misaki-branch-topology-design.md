# Misaki-Project branch構成変更設計

## 目的

fork repository `Misaki0331/mk`に、安定版と開発版を区別するbranchを追加する。
`Misaki-develop`をdefault branchとし、CherryPick移行互換の変更をPull Requestで取り込める状態にする。

fork元repositoryには変更を加えない。

## Branch構成

- `Misaki-Stable`は、変更実施時点の`origin/develop`先端から作成する。
- `Misaki-develop`も同じ`origin/develop`先端から作成する。
- GitHubのdefault branchを`develop`から`Misaki-develop`へ変更する。
- 既存の`develop`と`docker`は削除せず保持する。
- branch protectionなど、明示されていないrepository設定は変更しない。

この構成により、変更前のfork既定状態を`Misaki-Stable`に保存し、以後の開発変更を`Misaki-develop`向けPull Requestとして扱える。

## Feature Branch

公開安全性を検証済みのfeature HEADだけを、remote branch `feature/cherrypick-compatibility-foundation`へpushする。

- private作業branchはpushしない。
- dump、credential、local evidence、production由来情報はpushしない。
- remote feature branchは`Misaki-develop`をbaseとするPull Requestだけに使用する。

## IssueとPull Request

repository運用規約に従い、日本語Issueを先に作成する。Pull Request本文からそのIssueをcloseする。

Pull Requestは次の条件で作成する。

- base: `Misaki-develop`
- head: `feature/cherrypick-compatibility-foundation`
- タイトルと本文: 日本語
- 本文: 変更概要、主な変更点、検証結果、関連Issueを記載する
- production由来の値、件数、path、hash、SQL、evidenceは記載しない

現在のfeature branchは`origin/develop`の子孫であるため、Pull RequestにはCherryPick移行互換の実装commit、本設計commit、およびfork側が未取得のupstream更新commitが含まれる。この差分は履歴を改変せず、そのままreview対象とする。

## 実行順序

1. local/remote状態と`origin/develop`先端を再確認する。
2. `Misaki-Stable`と`Misaki-develop`を同じ先端へpushする。
3. 両branchのremote SHAが一致することを確認する。
4. GitHubのdefault branchを`Misaki-develop`へ変更する。
5. 日本語Issueを作成する。
6. 公開feature HEADをremote feature branchへpushする。
7. `Misaki-develop`向け日本語Pull Requestを作成する。
8. Pull Requestのbase/head、diff、本文、CIを確認する。

## 失敗時の扱い

- branch作成に失敗した場合はdefault branchを変更しない。
- default branch変更に失敗した場合はfeature branchとPull Requestを作成せず停止する。
- privacy監査、base/head確認、またはCIで問題が見つかった場合はmergeしない。
- force-push、既存branch削除、fork元repositoryへの書き込みは行わない。

## 完了条件

- `Misaki-Stable`と`Misaki-develop`が変更前の`origin/develop`先端から作成されている。
- GitHubのdefault branchが`Misaki-develop`になっている。
- 既存の`develop`と`docker`が保持されている。
- 公開feature branchだけがpushされている。
- 日本語Issueと`Misaki-develop`向け日本語Pull Requestが作成されている。
- Pull Requestのprivacy監査が通り、CI結果を確認できる。
