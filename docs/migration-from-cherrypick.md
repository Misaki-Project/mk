# CherryPick から mk-go への移行リハーサル手順

本番の CherryPick dump を mk-go へ移行する前に、隔離された Docker 環境で
復元 → 修復 → マイグレーション → 起動 → ログインまでを通しで確認する
リハーサル手順。`tests/cherrypick_migration/` のハーネスと `Makefile` の
`cherrypick-rehearsal-*` ターゲットが実行する。

## 前提

- Docker Desktop が起動しており、Docker Compose v2 が使えること。
- PostgreSQL 18 / Redis 7 / golang 1.26 / Node 24 のリハーサル用イメージが
  **ローカルに存在**していること。ハーネスは決して pull も build もせず、
  コミット済みの immutable digest に固定したイメージだけを使う。
- Go 1.26 がホストにあり、`GOOS=linux` のクロスビルドが通ること。

## 環境変数

リハーサルに渡す値は、operator の現在の shell だけに設定する 3 つの環境変数で
与える。実パス・実ハッシュ・資格情報の置き場所をリポジトリやドキュメントに
書かない。

- `CHERRYPICK_BACKUP_PATH`: 本番 dump のパス
- `CHERRYPICK_BACKUP_SHA256`: dump の期待 SHA-256 (比較にのみ使う)
- `CHERRYPICK_CREDENTIAL_PATH`: ログイン用資格情報 JSON のパス (Git の外)

資格情報 JSON は読み取り専用で Git リポジトリの**外**に置く。probe コンテナが
`/run/secrets/login.json:ro` として read-only でマウントして読み、ネットワークや
エビデンスに漏らさない。validate / clean-db / prepare / abort は資格情報を
**読まない**ため、このファイルは不要。

## 実行手順

### 1. 静的テスト (ハーネスの契約テスト)

```powershell
pwsh -NoProfile -File tests/cherrypick_migration/verify.test.ps1
```

compose の隔離不変条件と、`verify.ps1` の挙動契約 (mode 排他 / 負入力の拒否 /
ランタイムパスの制限 / finally での `down --volumes --remove-orphans` /
結果 JSON に資格情報を入れない) を検証する。コンテナは起動しない。

### 2. 検証のみ (validate)

```sh
make cherrypick-rehearsal-validate
```

dump の SHA-256 が期待値と一致することと、compose / イメージの隔離条件を確認し、
エビデンスを書く。**コンテナは一切起動しない**。資格情報は不要。

### 3. クリーンDB ゲート (clean-db)

```sh
make cherrypick-rehearsal-clean-db
```

空の DB に対して全マイグレーションを 2 回適用し (2 回目は no-change)、mk-go を
起動して health を確認する。dump も資格情報も使わない。本番データを使う前に
必ず通しておくゲート。

### 4. 修復用の準備 (prepare)

```sh
make cherrypick-rehearsal-prepare
```

dump を隔離 DB に復元し、pre 集計を実行して停止する。不正 JSON / 不正 item /
空文字 host が 0 件であることを要求し、修復前の孤児装飾 / 孤児 avatar /
孤児 banner の件数は要求しない (0 以外が修復対象の前提)。成功すると stack と
ランタイムが保持され、標準出力に `status=awaiting-manual-repair` とだけ出る。
session と DB password は repo 外のランタイムだけに置き、標準出力・エビデンスへ
path / hash / password / count を一切出さない。

### 5. DB内部修復

prepare の停止を確認したら、`docs/superpowers/specs/2026-08-16-database-internal-orphan-repair-design.md`
に従って孤児参照を修復する。修復 SQL は controller process memory 内だけで構成し
file へ保存しない。ID 集合は DB 内部の temporary table だけに snapshot する。

先に synthetic 正常系と rollback 系を実行し、成功した場合だけ prepare 済み隔離 DB
へ 1 回適用する。修復が成功した場合だけ resume へ進む。失敗した場合は再試行せず
abort で破棄する。public HEAD でも fresh volume から同じフローを再実行する。

### 6. 再開 (resume)

```sh
make cherrypick-rehearsal-resume
```

prepare が保持した session を検証する (status / 実装 HEAD / compose project /
dump hash)。DB password は secret file から復元し、DB / Redis ready と
internal-only ネットワークを再確認する。`pre-repaired` 集計で孤児・不正参照が
すべて 0 であることをマイグレーションの前に要求し、その後 マイグレーション
2 回 → post 集計 → 起動 → health / ログイン → ネットワーク再確認 → dump 再ハッシュ
→ エビデンス書き出し を実行する。終了後は stack とランタイムを撤去する。

### 7. 中断時の破棄 (abort)

```sh
make cherrypick-rehearsal-abort
```

prepare で保持した stack とランタイムを破棄する。再開せずに中止する場合に使う。

## エビデンスの取り扱い

`-EvidencePath` に書かれる JSON は件数・スキーマ事実・健康 / ログイン成否のみを
含む。ユーザー名・パスワード・トークン・メール・IP・ノート本文・生プロフィール
JSON・パス・ハッシュ・個別 ID は一切含めない。失敗時は `stage=<名前>` を標準
エラーに出し、アプリケーションログは残さない。

## Docker のインターネット禁止

- 全サービスは `pull_policy: never` で、イメージはローカル検証済みの immutable
  digest に固定されている。
- コンテナは `internal: true` の単一ネットワーク (`private`) だけに接続し、
  ホストへのポート公開・外部ネットワーク・build・pull は一切行わない。
- ハーネス内で Docker が依存を取得する操作をした場合、静的テストが拒否する。
