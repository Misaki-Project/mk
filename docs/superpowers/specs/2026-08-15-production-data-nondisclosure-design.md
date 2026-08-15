# 本番データ非公開化設計

## 目的

CherryPick互換基盤をGitHubへ公開する前に、本番DBから得た固有値をソース、テスト、文書、Git履歴、Pull Requestから除去する。DB dump、資格情報、個人情報、ファイルや装飾を含む本番由来IDはuploadしない。

既存のfail-closed方針は維持する。自動migrationは修復済みのDBだけを受け入れ、孤児参照が残っている場合は書込み前に停止する。

## 非公開対象

GitHubへ送信してはならない情報は次のとおりとする。

- 本番DB dump本体
- dumpのローカルpath、ファイル名、hash
- username、email、URL、資格情報、資格情報path
- ユーザー、ファイル、装飾など、本番DBから得たすべての固有ID
- 本番固有値を復元できるmanifest、hash、対応表、SQL
- operator固有のローカルpath
- 上記を含むログ、evidence、作業報告、patch、Git commit

集計件数や成功状態は個人を識別できない範囲でローカルevidenceに保存できる。ただしPull Request本文には本番データの値、件数、hashを記載しない。

## 移行フロー

### 1. 自動migration前の修復

本番固有IDを使う修復は自動migrationから分離し、`docs/superpowers/specs/2026-08-16-database-internal-orphan-repair-design.md`に従う。repair SQLはcontroller process memory内だけで構成してfileへ保存せず、ID集合はDB内部のtemporary tableだけにsnapshotする。

リポジトリには実値入りSQL、manifest、対応表を作成しない。repair SQLの内容はアプリ、migration harness、Gitで扱わない。

### 2. 検出専用preflight

`internal/migrationcompat`は次の孤児参照を検出する。

- 存在しない装飾を参照するユーザー設定
- 存在しないdrive fileを参照するavatar
- 存在しないdrive fileを参照するbanner

いずれかが1件でも存在する場合、またはqueryが失敗した場合、preflightはエラーを返す。preflight自身はDBを更新しない。

`cmd/migrate`はadvisory lock取得後、最初のschema migrationより前にpreflightを実行する。preflight失敗時はmigrationを開始しない。

### 3. Schema migration

CherryPick互換migrationは本番固有IDを含めない。リモート装飾の一般的な判定条件だけを使って対象行と参照を除去し、処理前後に孤児参照が0件であることをassertする。

未知のデータ形状、無効なJSON、孤児参照、更新件数の不整合を検出した場合はtransactionをrollbackする。

## 隔離リハーサル

本番dumpリハーサルは準備と再開の2段階に分ける。

### 準備モード

1. dumpのhashをローカルで検証する。
2. digest固定、`pull_policy: never`、外部通信不可のinternal networkに隔離DBを作る。
3. dumpを復元する。
4. migrationを実行せず、修復を実行できる状態で停止する。
5. 再開に必要な非機密stateをrepo外に保存する。

準備モードだけが明示的にstackを残せる。公開port、外部network、Docker imageのpull/buildは引き続き禁止する。

### 修復

prepare後はDB内部snapshot方式のrepairをcontrollerが実行する。operatorはSQL fileや固有IDを用意しない。先にsynthetic正常系とrollback系を実行し、repair成功時だけresumeへ進む。repair失敗時はabortして再試行しない。public HEADでもfresh volumeから同じフローを再実行する。

### 再開モード

1. state、compose project、internal network、dump hashが準備時と一致することを確認する。
2. 検出専用preflightで全孤児参照が0件であることを確認する。
3. migrationを2回実行し、idempotencyとschema versionを確認する。
4. アプリを起動し、healthとloginを検証する。
5. 最終集計と非機密evidenceを生成する。
6. 成否にかかわらずcontainer、volume、network、runtime stateを削除する。

修復が未完了の場合、再開モードはmigration前に停止する。

## Evidence

GitHubへevidenceをuploadしない。ローカルevidenceはallowlist形式で生成し、次の情報だけを許可する。

- 全体、health、loginの成功状態
- internal networkだけを使用したことを示すboolean
- schema migrationのversionとdirty状態
- 個人を識別しない集計件数
- 検証対象のコードrevision
- 検証timestamp

固有ID、username、email、URL、blurhash、資格情報、dump path、dump名、dump hash、repair SQLの内容は出力しない。

## DB URL処理

TCP接続用DB URLを文字列連結で組み立てず、URL構造体または適切なescape処理を使う。usernameまたはpasswordにURL予約文字が含まれても、advisory lock接続とmigration接続が同じ接続先を解釈することをテストする。

エラーとログへDB URLまたは資格情報を出力しない。

## Git履歴の再構成

現在の未push feature履歴はGitHubへ送信しない。upstream baseから新しいbranch履歴を作り、sanitizedな最終treeだけを論理的なcommitへ再構成する。

- 現feature commitをcherry-pick、merge、pushしない。
- 新履歴は各commitでbuild可能な順序にする。
- 新履歴の検証後に旧feature branch refを削除する。
- 旧履歴を保持する復旧用ref、patch、bundleを作らない。
- reflog内の到達不能objectはpush対象にしない。
- force-pushは行わない。

git-ignoredのローカル報告と一時ログは保持できる。ただしGit管理、Pull Request添付、artifact uploadの対象外とし、push前にignoredのままであることを確認する。

## テスト

すべてのrepository test dataはsynthetic値を使う。本番由来値をfixture、golden file、test name、commentへ転記しない。

最低限、次を検証する。

- 孤児が0件ならpreflightが成功する。
- 装飾、avatar、bannerの各孤児がある場合はpreflightが失敗する。
- query失敗時はpreflightが失敗する。
- preflight失敗時はschema migrationが開始されない。
- migrationに本番固有IDのallowlistが存在しない。
- migrationが一般条件でリモート装飾をcleanupし、孤児0件をassertする。
- 準備モード以外ではstackが残らない。
- 再開モードがstate不一致、非internal network、未修復DBを拒否する。
- evidenceが禁止fieldを出力しない。
- URL予約文字を含むDB資格情報を正しくescapeする。
- Linux focused tests、`go vet ./...`、`go build ./...`、隔離リハーサルが成功する。
- 完全なtest suiteをGitHub CIで実行する。

## Push前監査

push前に新branch全履歴を対象として次を確認する。

- tracked fileとcommit差分に本番由来ID、個人情報、dump情報がない。
- dump、SQL、evidence、credential、log、作業報告がtrackedまたはstagedされていない。
- 意図しない大容量blobが履歴にない。
- submodule pointerが変更されていない。
- worktreeがcleanである。
- privacy観点を含む独立code reviewでCritical/Important指摘がない。

Pull Request本文は日本語で作成し、変更目的、一般化した実装内容、synthetic testと隔離検証の結果、`Closes #1`だけを記載する。本番データの値、件数、hash、path、添付は含めない。

## 完了条件

- 本番dumpがGitHubへuploadされていない。
- 本番由来の全固有IDが新しいGit履歴とPull Requestに存在しない。
- 修復後だけ自動migrationが進行する。
- 孤児参照が残る場合はDB書込み前に停止する。
- 自動処理が本番固有値を受け取らない。
- privacy-safeなテストと隔離リハーサルが成功する。
- GitHub CIで完全なtest suiteの結果を確認できる。
