# 本番データ非公開化 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** CherryPick互換基盤のfail-closed移行を維持しながら、本番DB由来の固有値をソース、テスト、文書、Git履歴、Pull Requestから完全に除去する。

**Architecture:** 本番固有値を使う修復は`docs/superpowers/specs/2026-08-16-database-internal-orphan-repair-design.md`のDB内部snapshot方式へ分離し、mk-goのpreflightは孤児参照の検出だけを行う。隔離リハーサルは復元後に停止する準備モードと、修復後に孤児0件を確認してmigrationを続行する再開モードへ分ける。実装完了後は現在のprivate履歴をpushせず、upstream baseからsanitizedなファイルsnapshotだけを使って公開用履歴を再構成する。

**Tech Stack:** Go 1.26、pgx v5、golang-migrate、PostgreSQL 18、PowerShell 7、Docker Compose v2、testify

## Global Constraints

- DB dump、資格情報、個人情報、本番由来のユーザー・ファイル・装飾IDをGitHubへuploadしない。
- dumpのローカルpath、ファイル名、hash、repair SQLの内容をtracked file、commit、PR、evidenceへ書かない。
- テストデータはsynthetic値だけを使う。
- 自動migrationは孤児装飾、孤児avatar、孤児bannerがすべて0件の場合だけ進行する。
- query失敗、schema不一致、無効なJSON、孤児残存はDB書込み前にfail-closedで停止する。
- Dockerはdigest固定、`pull_policy: never`、単一`internal: true` network、公開portなし、build/pullなしを維持する。
- git-ignoredのローカル報告と一時ログは保持するが、tracked、staged、PR添付、artifact uploadの対象にしない。
- 現在のfeature履歴は一切pushしない。公開用履歴はupstream baseから新規作成し、force-pushしない。
- frontend submoduleのpointerと内容を変更しない。
- commit前に`git diff --check`を実行し、ユーザーが承認した本計画の対象ファイルだけをstageする。

---

## File Map

- `internal/migrationcompat/preflight.go`: schema検証と孤児件数検出だけを行うread-only preflight。
- `internal/migrationcompat/preflight_test.go`: synthetic PostgreSQL fixtureによるpreflightのfail-closedテスト。
- `internal/migrationcompat/lock.go`: 検出専用preflightを保持するlock connection interfaceの説明。
- `cmd/migrate/main.go`: DB URL生成、advisory lock、上方向migration前のpreflight呼出し。
- `cmd/migrate/main_test.go`: URL予約文字を含むDB設定のparseテスト。
- `migration/000075_cherrypick_avatar_decoration_cleanup.up.sql`: 本番固有IDを持たないリモート装飾cleanup。
- `internal/entitycompat/cherrypick_avatar_decoration_migration_test.go`: migration 000075のsynthetic統合テスト。
- `tests/cherrypick_migration/aggregate.sql`: 個別値と決定的data hashを出さないcount-only集計。
- `tests/cherrypick_migration/verify.ps1`: validate、clean DB、manual prepare、manual resume、abortを実行するrunner。
- `tests/cherrypick_migration/verify.test.ps1`: isolation、mode、state、evidence、privacy契約の静的・負入力テスト。
- `tests/cherrypick_migration/compose.yml`: 既存の隔離Compose。serviceやmountを増やさない。
- `Makefile`: operatorが環境変数でpath/hashを渡す汎用target。
- `docs/migration-from-cherrypick.md`: 固有値を含まない二段階運用手順。
- `docs/superpowers/specs/2026-08-14-cherrypick-to-mk-migration-design.md`: privateな旧設計のため公開treeから削除。
- `docs/superpowers/plans/2026-08-14-cherrypick-compatibility-foundation.md`: privateな旧計画のため公開treeから削除。
- `docs/superpowers/specs/2026-08-15-production-data-nondisclosure-design.md`: 承認済み公開設計。
- `docs/superpowers/plans/2026-08-15-production-data-nondisclosure.md`: 本計画。

---

### Task 1: Preflightを検出専用にする

**Files:**
- Modify: `internal/migrationcompat/preflight.go`
- Modify: `internal/migrationcompat/preflight_test.go`
- Modify: `internal/migrationcompat/lock.go`
- Modify: `cmd/migrate/main.go`

**Interfaces:**
- Consumes: `Lock.conn lockConn`、`context.Context`
- Produces: `func (l *Lock) CheckPreflight(ctx context.Context) error`
- Produces: `var errIncompatibleSchema error`
- Removes: `approvedAvatarPairs`、`approvedPairsSQL`、`sqlLiteral`、`func (l *Lock) Reconcile(...)`

- [ ] **Step 1: synthetic fixtureだけを使う失敗テストへ書き換える**

`internal/migrationcompat/preflight_test.go`から承認済みpair、ユーザー状態、修復成功、更新件数に関するtest helperとsubtestを削除する。次の形で検出契約を追加する。

```go
func TestCheckPreflight(t *testing.T) {
	conn := preflightConn(t)
	ctx := context.Background()

	t.Run("compatible schema without orphans succeeds without mutation", func(t *testing.T) {
		recreatePreflightTables(t, conn)
		seedValidPreflightRows(t, conn)
		before := snapshotPreflightRows(t, conn)

		require.NoError(t, (&Lock{conn: conn}).CheckPreflight(ctx))
		require.Equal(t, before, snapshotPreflightRows(t, conn))
	})

	for _, tc := range []struct {
		name     string
		seedSQL  string
		wantPart string
	}{
		{"orphan decoration", `INSERT INTO "user" ("id", "avatarDecorations") VALUES ('synthetic-user-a', '[{"id":"synthetic-decoration-missing"}]')`, "orphan avatar decoration references"},
		{"orphan avatar", `INSERT INTO "user" ("id", "avatarId", "avatarDecorations") VALUES ('synthetic-user-b', 'synthetic-file-missing-a', '[]')`, "orphan avatar references"},
		{"orphan banner", `INSERT INTO "user" ("id", "bannerId", "avatarDecorations") VALUES ('synthetic-user-c', 'synthetic-file-missing-b', '[]')`, "orphan banner references"},
		{"invalid row shape", `INSERT INTO "user" ("id", "avatarDecorations") VALUES ('synthetic-user-d', 'null')`, "invalid avatar decoration JSON"},
		{"invalid item shape", `INSERT INTO "user" ("id", "avatarDecorations") VALUES ('synthetic-user-e', '[42]')`, "invalid avatar decoration item"},
	} {
		t.Run(tc.name+" fails without mutation", func(t *testing.T) {
			recreatePreflightTables(t, conn)
			seedValidPreflightRows(t, conn)
			_, err := conn.Exec(ctx, tc.seedSQL)
			require.NoError(t, err)
			before := snapshotPreflightRows(t, conn)

			err = (&Lock{conn: conn}).CheckPreflight(ctx)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantPart)
			require.Equal(t, before, snapshotPreflightRows(t, conn))
		})
	}
}
```

Fixture schemaは`user.id`、`user.avatarId`、`user.bannerId`、`user.avatarDecorations`、`drive_file.id`、`avatar_decoration.id`だけを持たせる。全IDは`synthetic-*`形式にする。

- [ ] **Step 2: red状態を確認する**

Run:

```powershell
go test ./internal/migrationcompat -run 'TestCheckPreflight|TestMigrateMainPreflightOrdering' -count=1 -v
```

Expected: `CheckPreflight`未定義、または旧`Reconcile`呼出しのためFAIL。

- [ ] **Step 3: preflight失敗時にmigrationが開始されないsource契約を書く**

旧`TestMigrateMainReconcileOrdering`を`TestMigrateMainPreflightOrdering`へ変更し、次を固定する。

```go
acquireIdx := strings.Index(src, "migrationcompat.Acquire(ctx, dbURL)")
preflightIdx := strings.Index(src, "lock.CheckPreflight(ctx)")
migrateIdx := strings.Index(src, `migrate.New("file://migration", dbURL)`)
require.GreaterOrEqual(t, acquireIdx, 0)
require.GreaterOrEqual(t, preflightIdx, 0)
require.GreaterOrEqual(t, migrateIdx, 0)
require.Less(t, acquireIdx, preflightIdx)
require.Less(t, preflightIdx, migrateIdx)
require.Contains(t, src[:preflightIdx], `if *direction == "up" {`)
```

- [ ] **Step 4: 最小の検出専用preflightを実装する**

`preflight.go`は次の順序だけを実行する。

1. transaction開始。
2. `user`、`drive_file`、`avatar_decoration`の存在確認。全て不存在ならclean DBとしてcommit、部分存在なら`errIncompatibleSchema`。
3. 使用列の型・長さ・nullability確認。
4. 3 tableを`SHARE ROW EXCLUSIVE MODE`でlock。
5. 無効な`avatarDecorations`行、無効item、孤児装飾、孤児avatar、孤児bannerを件数だけ取得。
6. 全件数0ならcommit、それ以外は件数だけを含む固定category errorを返しdefer rollback。

中心となるqueryは個別値を返さない。

```go
const preflightCountsSQL = `
SELECT
  (SELECT count(*) FROM "user" u
   WHERE jsonb_typeof(u."avatarDecorations") IS DISTINCT FROM 'array'),
  (SELECT count(*) FROM (
      SELECT u."avatarDecorations" FROM "user" u
      WHERE jsonb_typeof(u."avatarDecorations") = 'array'
   ) arr CROSS JOIN LATERAL jsonb_array_elements(arr."avatarDecorations") item
   WHERE jsonb_typeof(item) IS DISTINCT FROM 'object'
      OR jsonb_typeof(item->'id') IS DISTINCT FROM 'string'),
  (SELECT count(*) FROM (
      SELECT u."avatarDecorations" FROM "user" u
      WHERE jsonb_typeof(u."avatarDecorations") = 'array'
   ) arr CROSS JOIN LATERAL jsonb_array_elements(arr."avatarDecorations") item
   LEFT JOIN avatar_decoration d ON d.id = item->>'id'
   WHERE d.id IS NULL),
  (SELECT count(*) FROM "user" u LEFT JOIN drive_file f ON f.id = u."avatarId"
   WHERE u."avatarId" IS NOT NULL AND f.id IS NULL),
  (SELECT count(*) FROM "user" u LEFT JOIN drive_file f ON f.id = u."bannerId"
   WHERE u."bannerId" IS NOT NULL AND f.id IS NULL)`
```

`cmd/migrate/main.go`の上方向guardを次へ変更する。

```go
if *direction == "up" {
	if err := lock.CheckPreflight(ctx); err != nil {
		return fmt.Errorf("migration compatibility preflight failed: %w", err)
	}
}
```

`internal/migrationcompat/lock.go`の`lockConn` commentから修復transactionという表現を除き、検出専用preflight transactionを保持するinterfaceであることを記載する。

- [ ] **Step 5: transaction各段階のerror injectionを更新する**

`TestCheckPreflightFailsClosedOnTransactionalStepError`はtable存在、column query、table lock、count queryの4段階を対象にし、各失敗でsnapshot不変を要求する。旧pair比較、状態確認、UPDATE、再countのcaseは削除する。

- [ ] **Step 6: focused testをgreenにする**

Run:

```powershell
go test ./internal/migrationcompat -count=1 -v
go vet ./internal/migrationcompat ./cmd/migrate
git diff --check
```

Expected: 全PASS。DBが利用できない場合はskipを成功扱いにせず、CI serviceまたは隔離PostgreSQLで再実行する。

- [ ] **Step 7: commitする**

```powershell
git add -- internal/migrationcompat/preflight.go internal/migrationcompat/preflight_test.go internal/migrationcompat/lock.go cmd/migrate/main.go
git diff --cached --check
git commit -m "Fix migration: preflightを検出専用にする"
```

---

### Task 2: Migration 000075から本番固有IDを除去する

**Files:**
- Modify: `migration/000075_cherrypick_avatar_decoration_cleanup.up.sql`
- Modify: `internal/entitycompat/cherrypick_avatar_decoration_migration_test.go`

**Interfaces:**
- Consumes: 修復後で孤児装飾参照0件のDB。
- Produces: リモート装飾行と、その行を参照するJSON要素だけを削除するidempotent SQL。
- Removes: 固有ID入りtemp tableとapproved/unapproved分岐。

- [ ] **Step 1: 全孤児と本番型ID literalを拒否する失敗testを書く**

成功fixtureはlocal/remote装飾だけを持ち、存在しない装飾への参照を含めない。次のsubtestを維持または追加する。

```go
t.Run("any orphan decoration aborts and preserves every row", func(t *testing.T) {
	prepareDecorationSchema(t, db, cherrypickDecorationDDL)
	seedDecorations(t, db)
	require.NoError(t, db.Exec(`
		INSERT INTO "user" ("id", "avatarDecorations")
		VALUES ('synthetic-orphan-user', '[{"id":"synthetic-missing-decoration"}]'::jsonb)
	`).Error)
assertDecorationMigrationAborts(t, db, "orphan avatar decoration references")
})

func TestCherryPickMigrationContainsNoProductionStyleIDLiteral(t *testing.T) {
	src := loadCherrypickDecorationMigration(t)
	re := regexp.MustCompile(`'[a-z0-9]{16}'`)
	require.Empty(t, re.FindAllString(src, -1))
}
```

- [ ] **Step 2: testが旧allowlist挙動を検出することを確認する**

Run:

```powershell
go test ./internal/entitycompat -run TestCherryPickAvatarDecorationMigration -count=1 -v
```

Expected: 旧SQLの固定ID literalをsource/privacy assertionが検出してFAIL。

- [ ] **Step 3: migrationを一般条件だけにする**

`cherrypick_approved_stale_decoration_id`の作成、INSERT、JOIN、UPDATE条件を全て削除する。remote ID temp table作成前、かつUPDATE前に次を実行する。

```sql
SELECT count(*) INTO invalid_count
FROM "user" u
CROSS JOIN LATERAL jsonb_array_elements(u."avatarDecorations") item
LEFT JOIN avatar_decoration d ON d.id = item->>'id'
WHERE d.id IS NULL;
IF invalid_count <> 0 THEN
    RAISE EXCEPTION 'orphan avatar decoration references: % entries', invalid_count;
END IF;
```

UPDATEの除去条件は`cherrypick_remote_decoration_id`との一致だけにする。post assertionはremote row 0、remote reference 0、orphan reference 0を維持する。

- [ ] **Step 4: SQL source privacy契約をgreenにする**

Step 1で追加したsource/privacy assertionを単独実行し、quoted lowercase alphanumeric 16文字のliteralが0件になったことを確認する。

- [ ] **Step 5: focused testをgreenにする**

Run:

```powershell
go test ./internal/entitycompat -run 'TestCherryPickAvatarDecorationMigration|TestCherryPickMigrationContainsNoProductionStyleIDLiteral' -count=1 -v
git diff --check
```

Expected: PASS。invalid JSON、孤児、second run、clean schemaの全subtestを含む。

- [ ] **Step 6: commitする**

```powershell
git add -- migration/000075_cherrypick_avatar_decoration_cleanup.up.sql internal/entitycompat/cherrypick_avatar_decoration_migration_test.go
git diff --cached --check
git commit -m "Fix migration: 本番固有IDをcleanupから除去する"
```

---

### Task 3: DB URLを予約文字安全にする

**Files:**
- Create: `cmd/migrate/main_test.go`
- Modify: `cmd/migrate/main.go`

**Interfaces:**
- Produces: `func buildDatabaseURL(host string, port int, database, user, password string) string`
- Consumes: `config.IsUnixSocketPath(host)`

- [ ] **Step 1: URL予約文字とIPv6を含む失敗testを書く**

```go
func TestBuildDatabaseURL(t *testing.T) {
	for _, tc := range []struct {
		name     string
		host     string
		port     int
		database string
		user     string
		password string
	}{
		{"tcp reserved characters", "db.example.invalid", 5432, "synthetic-db", "synthetic:user", "synthetic@pass/with?#%"},
		{"tcp ipv6", "2001:db8::1", 5432, "synthetic-db", "synthetic-user", "synthetic-pass"},
		{"unix socket", "/run/postgresql", 5432, "synthetic-db", "synthetic:user", "synthetic@pass/with?#%"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := buildDatabaseURL(tc.host, tc.port, tc.database, tc.user, tc.password)
			parsed, err := pgx.ParseConfig(got)
			require.NoError(t, err)
			require.Equal(t, tc.user, parsed.User)
			require.Equal(t, tc.password, parsed.Password)
			require.Equal(t, tc.database, parsed.Database)
			require.Equal(t, tc.host, parsed.Host)
			require.Equal(t, uint16(tc.port), parsed.Port)
		})
	}
}
```

- [ ] **Step 2: 旧TCP文字列連結でtestがFAILすることを確認する**

Run:

```powershell
go test ./cmd/migrate -run TestBuildDatabaseURL -count=1 -v
```

Expected: helper未定義でFAIL。

- [ ] **Step 3: `net/url`構造体でhelperを実装する**

TCPは`url.UserPassword`と`net.JoinHostPort`を使う。UDSはquery parameterへhost、port、user、passwordを入れる。

```go
func buildDatabaseURL(host string, port int, database, user, password string) string {
	q := url.Values{"sslmode": {"disable"}}
	u := &url.URL{Scheme: "postgres", Path: "/" + database}
	if config.IsUnixSocketPath(host) {
		q.Set("host", host)
		q.Set("port", strconv.Itoa(port))
		q.Set("user", user)
		q.Set("password", password)
	} else {
		u.User = url.UserPassword(user, password)
		u.Host = net.JoinHostPort(host, strconv.Itoa(port))
	}
	u.RawQuery = q.Encode()
	return u.String()
}
```

`run()`は`dbURL := buildDatabaseURL(...)`だけを使う。DB URLをerror/logへ追加しない。

- [ ] **Step 4: test、vet、buildを実行する**

Run:

```powershell
go test ./cmd/migrate ./internal/migrationcompat -count=1 -v
go vet ./cmd/migrate ./internal/migrationcompat
$env:GOOS='linux'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'; go build ./cmd/migrate
```

Expected: PASS。

- [ ] **Step 5: commitする**

```powershell
git add -- cmd/migrate/main.go cmd/migrate/main_test.go
git diff --cached --check
git commit -m "Fix migrate: DB接続URLを安全にescapeする"
```

---

### Task 4: 集計とevidenceをprivacy-safeにする

**Files:**
- Modify: `tests/cherrypick_migration/aggregate.sql`
- Modify: `tests/cherrypick_migration/verify.ps1`
- Modify: `tests/cherrypick_migration/verify.test.ps1`

**Interfaces:**
- Produces: count-only aggregate keys。
- Produces: dump hashを含まないallowlist evidence。
- Consumes: operatorが引数で渡す`ExpectedBackupSha256`。値をsource/evidenceへ保存しない。

- [ ] **Step 1: 旧allowlist、data hash、dump hash evidenceを拒否するtestを書く**

`verify.test.ps1`のapproved/unexpected allowlist testと固定件数testを削除し、次を追加する。

```powershell
Assert-Test -Name 'tracked rehearsal sources contain no production allowlist or fixed dump hash' -Body {
    $aggregateSrc = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'aggregate.sql') -Raw
    $verifySrc = Get-Content -LiteralPath $script:VerifyPs1 -Raw
    $migrationSrc = Get-Content -LiteralPath (Join-Path $PSScriptRoot '..\..\migration\000075_cherrypick_avatar_decoration_cleanup.up.sql') -Raw
    $all = $aggregateSrc + "`n" + $verifySrc + "`n" + $migrationSrc
    if ($all -match "'[a-z0-9]{16}'") { throw 'production-style fixed ID literal found' }
    if ($verifySrc -match 'ExpectedDumpSha256') { throw 'fixed dump hash constant found' }
    if ($aggregateSrc -match 'approved_orphan|unexpected_orphan') { throw 'production allowlist aggregate found' }
}

Assert-Test -Name 'aggregate emits counts but no deterministic data hashes' -Body {
    $aggregateSrc = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'aggregate.sql') -Raw
    foreach ($key in @('orphan_decoration_reference_count', 'orphan_avatar_file_reference_count', 'orphan_banner_file_reference_count')) {
        if ($aggregateSrc -notmatch [regex]::Escape($key + '=')) { throw "missing $key" }
    }
    if ($aggregateSrc -match '(?i)sha256|role_hash|assignment_hash|decoration_hash') { throw 'data hash output found' }
}
```

- [ ] **Step 2: red状態を確認する**

Run:

```powershell
pwsh -NoProfile -File tests/cherrypick_migration/verify.test.ps1
git diff --check
```

Expected: 固定ID、固定dump hash、approved keys、data hashの検出でFAIL。

- [ ] **Step 3: `aggregate.sql`をcount-onlyへ縮小する**

次を削除する。

- `approved_orphan_decoration_reference_count`
- `unexpected_orphan_decoration_reference_count`
- `role_hash`
- `role_assignment_hash`
- `local_decoration_hash`
- `user_avatar_decoration_hash`

保持するのはphase、table件数、local/remote decoration件数、総/remote/orphan decoration参照件数、avatar/banner孤児件数、invalid JSON件数、schema migration状態だけとする。

- [ ] **Step 4: dump hashをsourceとevidenceから除去する**

`verify.ps1`へ`[string] $ExpectedBackupSha256`を追加し、固定constantを削除する。比較時は次のように引数を正規化するが、`$script:Evidence`へ保存しない。

```powershell
function Assert-DumpHash {
    if ($ExpectedBackupSha256 -notmatch '^[0-9a-fA-F]{64}$') { throw 'expected backup SHA-256 is required' }
    $actual = Get-DumpHash
    if ($actual -ne $ExpectedBackupSha256.ToLowerInvariant()) { throw 'dump hash mismatch' }
    return $actual
}
```

`EvidenceKeys`から`backupSha256`を削除する。pre/postから削除済みhash keysを参照しないよう`Compare-PrePost`のequal keysをtable件数とlocal decoration件数だけにする。pre gateは次の全keyが0であることだけを要求する。

```powershell
$preRequiredZero = @(
    'orphan_decoration_reference_count',
    'orphan_avatar_file_reference_count',
    'orphan_banner_file_reference_count',
    'empty_string_host_decoration_count',
    'invalid_decoration_json_row_count',
    'invalid_decoration_reference_count'
)
```

- [ ] **Step 5: evidence whitelistの禁止field testを強化する**

禁止patternへ`backup|dump|path|hash|url|blurhash|email|userId|fileId|decorationId`を追加する。failed evidenceは`status`、`stage`、`finalVerificationTimestamp`だけを許可する。

- [ ] **Step 6: static harness testをgreenにする**

Run:

```powershell
pwsh -NoProfile -File tests/cherrypick_migration/verify.test.ps1
```

Expected: 全PASS。Docker containerは起動しない。

- [ ] **Step 7: commitする**

```powershell
git add -- tests/cherrypick_migration/aggregate.sql tests/cherrypick_migration/verify.ps1 tests/cherrypick_migration/verify.test.ps1
git diff --cached --check
git commit -m "Fix rehearsal: 本番固有値を集計とevidenceから除外する"
```

---

### Task 5: リハーサルをmanual prepare/resumeへ分割する

**Files:**
- Modify: `tests/cherrypick_migration/verify.ps1`
- Modify: `tests/cherrypick_migration/verify.test.ps1`
- Modify: `Makefile`
- Modify: `docs/migration-from-cherrypick.md`

**Interfaces:**
- Produces switches: `PrepareManualRepair`、`ResumeManualRepair`、`AbortManualRepair`。
- Produces local state: `<RuntimePath>/session.json`。個人情報を含めない。
- Produces local secret: `<RuntimePath>/db-password.txt`。evidenceへ出さずresume/abort後に削除する。
- Consumes environment: `CHERRYPICK_BACKUP_PATH`、`CHERRYPICK_BACKUP_SHA256`、`CHERRYPICK_CREDENTIAL_PATH`。

- [ ] **Step 1: mode排他とcleanupの失敗testを書く**

`verify.test.ps1`に次を追加する。

```powershell
Assert-Test -Name 'runner rejects multiple operation modes' -Body {
    $r = Invoke-VerifyRunner @('-PrepareManualRepair', '-ResumeManualRepair', '-BackupPath', $dummyBackup, '-ExpectedBackupSha256', ('0' * 64), '-RuntimePath', $script:RunnerUnitRoot, '-EvidencePath', $dummyEvidence)
    if ($r.Code -eq 0 -or $r.Output -notmatch 'stage=validate-params') { throw 'mode conflict was not rejected' }
}

Assert-Test -Name 'prepare is the only mode allowed to preserve the stack' -Body {
    $src = Get-Content -LiteralPath $script:VerifyPs1 -Raw
    if ($src -notmatch 'PreserveForManualRepair') { throw 'preserve guard missing' }
    if ($src -notmatch 'PrepareManualRepair') { throw 'prepare mode missing' }
    if ($src -notmatch 'if \(-not \$script:PreserveForManualRepair\)') { throw 'finally cleanup guard missing' }
}

Assert-Test -Name 'resume requires zero orphan counts before migration-1' -Body {
    $src = Get-Content -LiteralPath $script:VerifyPs1 -Raw
    $resumeIdx = $src.IndexOf('resume-preflight')
    $migrationIdx = $src.IndexOf('migration-1')
    if ($resumeIdx -lt 0 -or $migrationIdx -lt 0 -or $resumeIdx -gt $migrationIdx) { throw 'resume preflight must precede migration' }
    foreach ($key in @('orphan_decoration_reference_count', 'orphan_avatar_file_reference_count', 'orphan_banner_file_reference_count')) {
        if ($src -notmatch [regex]::Escape($key)) { throw "missing zero gate for $key" }
    }
}
```

- [ ] **Step 2: red状態を確認する**

Run:

```powershell
pwsh -NoProfile -File tests/cherrypick_migration/verify.test.ps1
```

Expected: 新modeとpreserve guard未実装でFAIL。

- [ ] **Step 3: parameterとmode validationを実装する**

parameter blockを次のmode集合へ変更する。

```powershell
[string] $BackupPath,
[string] $ExpectedBackupSha256,
[string] $CredentialPath,
[string] $RuntimePath = (Join-Path ([System.IO.Path]::GetTempPath()) 'mk-cherrypick-migration'),
[string] $EvidencePath = (Join-Path ([System.IO.Path]::GetTempPath()) 'mk-cherrypick-migration-result.json'),
[switch] $ValidateOnly,
[switch] $CleanDatabaseOnly,
[switch] $PrepareManualRepair,
[switch] $ResumeManualRepair,
[switch] $AbortManualRepair
```

ちょうど1 modeだけを許可する。`ValidateOnly`、`PrepareManualRepair`、`ResumeManualRepair`はBackupPathとExpectedBackupSha256を必須とする。`ResumeManualRepair`だけCredentialPathを必須とする。`CleanDatabaseOnly`と`AbortManualRepair`はdumpも資格情報も読まない。

Compose変数は全modeで構文上解決可能にする。dumpを使わないmodeでは`MK_MIGRATION_BACKUP_PATH`へ存在する非機密tracked fileを設定し、restore service自体は起動しない。resumeは`db-password.txt`を読んだ後に`MK_MIGRATION_DB_PASSWORD`を設定してからCompose commandを実行する。

- [ ] **Step 4: prepare modeを実装する**

prepareは既存のbuild、config、restore、aggregate-pre、network inspectionまで実行し、migrationを呼ばない。次を満たす。

aggregate-preでは無効JSON、無効item、空文字hostが0件であることを要求する。孤児装飾、孤児avatar、孤児bannerの件数は修復前なので0件を要求せず、標準出力、session、evidenceへ保存しない。

```powershell
$session = [ordered]@{
    status = 'awaiting-manual-repair'
    implementationSha = Get-RepoHeadSha
    backupSha256 = $actualBackupHash
    projectName = $script:ProjectName
}
$session | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $RuntimePath 'session.json') -Encoding utf8
[System.IO.File]::WriteAllText((Join-Path $RuntimePath 'db-password.txt'), $script:DbPassword)
$script:PreserveForManualRepair = $true
```

`session.json`と`db-password.txt`はrepo外runtimeだけに置く。標準出力には`status=awaiting-manual-repair`だけを出し、path、hash、count、credentialを出さない。

- [ ] **Step 5: resume modeを実装する**

resumeは新しいDBを起動・restoreせず、既存sessionを検証する。

1. runtimeと2 state fileが存在する。
2. session statusが`awaiting-manual-repair`。
3. current HEAD、compose project、BackupPathのactual hashがsessionと一致。
4. DB passwordをsecret fileから読み、Compose環境へ設定。
5. DB/Redis readyとinternal-only networkを再確認。
6. `Invoke-Aggregate 'pre-repaired'`を実行し、orphan/invalid countsが全て0。
7. migrationを2回実行。
8. post aggregate、health、login、network、dump rehashを実行。
9. privacy-safe evidenceを書き、finallyでstack/runtimeを削除。

resume前のaggregateを`$pre`としてCompare-PrePostに渡す。修復前のaggregateはevidenceへ入れない。

- [ ] **Step 6: abort modeとfinally cleanupを実装する**

abortはCompose isolation gateを通した同じprojectだけに対して`down --volumes --remove-orphans`を実行し、allowed runtime root配下だけを削除する。prepare成功時だけfinally cleanupをskipする。

```powershell
finally {
    if (-not $script:PreserveForManualRepair) {
        if ($script:StartedCompose -or $ResumeManualRepair -or $AbortManualRepair) {
            & docker compose -f $script:ComposeFile down --volumes --remove-orphans 2>&1 | Out-Null
        }
        if (Test-Path -LiteralPath $RuntimePath) {
            Remove-Item -LiteralPath $RuntimePath -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
}
```

- [ ] **Step 7: Makefileからlocal固有pathを除去する**

`.PHONY`へ`cherrypick-rehearsal-prepare`、`cherrypick-rehearsal-resume`、`cherrypick-rehearsal-abort`を追加し、旧one-shot targetを削除する。値は環境変数だけから受け取る。

```make
cherrypick-rehearsal-validate:
	pwsh -NoProfile -File tests/cherrypick_migration/verify.ps1 -ValidateOnly -BackupPath "$(CHERRYPICK_BACKUP_PATH)" -ExpectedBackupSha256 "$(CHERRYPICK_BACKUP_SHA256)"

cherrypick-rehearsal-clean-db:
	pwsh -NoProfile -File tests/cherrypick_migration/verify.ps1 -CleanDatabaseOnly

cherrypick-rehearsal-prepare:
	pwsh -NoProfile -File tests/cherrypick_migration/verify.ps1 -PrepareManualRepair -BackupPath "$(CHERRYPICK_BACKUP_PATH)" -ExpectedBackupSha256 "$(CHERRYPICK_BACKUP_SHA256)"

cherrypick-rehearsal-resume:
	pwsh -NoProfile -File tests/cherrypick_migration/verify.ps1 -ResumeManualRepair -BackupPath "$(CHERRYPICK_BACKUP_PATH)" -ExpectedBackupSha256 "$(CHERRYPICK_BACKUP_SHA256)" -CredentialPath "$(CHERRYPICK_CREDENTIAL_PATH)"

cherrypick-rehearsal-abort:
	pwsh -NoProfile -File tests/cherrypick_migration/verify.ps1 -AbortManualRepair
```

- [ ] **Step 8: 運用文書を固有値なしで書き換える**

`docs/migration-from-cherrypick.md`から実path、実hash、固有ID、固定件数、private branch/worktree、reference SHAを削除する。次の順序を記載する。

1. operatorの現在のshellだけに3環境変数を設定。
2. validate。
3. clean DB gate。
4. prepare。
5. `docs/superpowers/specs/2026-08-16-database-internal-orphan-repair-design.md`に従ってcontrollerがDB内部snapshot方式で修復。SQL内容をコマンド例へ書かない。
6. resume。
7. 中断時はabort。

コマンド例は実値を直接書かず、`CHERRYPICK_BACKUP_PATH`、`CHERRYPICK_BACKUP_SHA256`、`CHERRYPICK_CREDENTIAL_PATH`の環境変数名だけを使う。

- [ ] **Step 9: harness testをgreenにする**

Run:

```powershell
pwsh -NoProfile -File tests/cherrypick_migration/verify.test.ps1
pwsh -NoProfile -File tests/cherrypick_migration/verify.ps1 -CleanDatabaseOnly
git diff --check
```

Expected: static/negative test全PASS、clean DBでmigration 2回、health PASS、container/volume/network/runtime残留0。

- [ ] **Step 10: commitする**

```powershell
git add -- tests/cherrypick_migration/verify.ps1 tests/cherrypick_migration/verify.test.ps1 Makefile docs/migration-from-cherrypick.md
git diff --cached --check
git commit -m "Fix rehearsal: 手動修復をmigration前へ分離する"
```

---

### Task 6: 公開treeをsanitizationし、安全な履歴を再構成する

**Files:**
- Delete from public tree: `docs/superpowers/specs/2026-08-14-cherrypick-to-mk-migration-design.md`
- Delete from public tree: `docs/superpowers/plans/2026-08-14-cherrypick-compatibility-foundation.md`
- Preserve: `docs/superpowers/specs/2026-08-15-production-data-nondisclosure-design.md`
- Preserve: `docs/superpowers/plans/2026-08-15-production-data-nondisclosure.md`
- Audit: all files in upstream-base-to-feature diff

**Interfaces:**
- Consumes: fully tested sanitized final tree in the current private worktree。
- Produces: upstream baseをparentに持ち、private commitを祖先に持たない公開branch。
- Produces: branch name `feature/cherrypick-compatibility-foundation` for push。

- [ ] **Step 1: 旧private設計文書をtracked treeから削除する**

```powershell
git rm -- "docs/superpowers/specs/2026-08-14-cherrypick-to-mk-migration-design.md" "docs/superpowers/plans/2026-08-14-cherrypick-compatibility-foundation.md"
git diff --cached --check
git commit -m "docs: 本番固有値を含む旧設計文書を除去する"
```

git-ignoredの`.superpowers/sdd`とrepo外ログは削除しない。

- [ ] **Step 2: private treeのprivacy監査を実行する**

実値を標準出力へ出さず、match数だけで判定する。次を確認する。

```powershell
$changed = @(git diff --name-only upstream-temp/develop..HEAD)
$trackedDumps = @(git ls-files -- '*.sql.gz' '*.dump' '*.backup')
if ($trackedDumps.Count -ne 0) { throw 'tracked database dump found' }

$files = $changed | Where-Object { Test-Path -LiteralPath $_ -PathType Leaf }
$productionStyleIdMatches = 0
foreach ($file in $files) {
    $text = [System.IO.File]::ReadAllText((Resolve-Path $file))
    $productionStyleIdMatches += [regex]::Matches($text, "['\"][a-z0-9]{16}['\"]").Count
}
if ($productionStyleIdMatches -ne 0) { throw "production-style fixed ID literals found: $productionStyleIdMatches" }
```

さらに、tracked diffに`.sql.gz`、credential JSON、evidence JSON、absolute operator path、固定dump hash定数、approved ID tableがないことをfile nameとcategoryだけで報告する。match本文は表示しない。

- [ ] **Step 3: private final treeの全gateを実行する**

Run:

```powershell
go test ./internal/auth/password ./internal/migrationcompat ./internal/api/signin ./internal/api/i ./internal/entitycompat ./cmd/migrate -count=1 -v
$env:GOOS='linux'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'; go build ./...
pwsh -NoProfile -File tests/cherrypick_migration/verify.test.ps1
pwsh -NoProfile -File tests/cherrypick_migration/verify.ps1 -CleanDatabaseOnly
git diff --check
git status --short --branch
```

Expected: focused tests、Linux build、harness test、clean DB gateがPASS。worktree clean。WindowsでLinux-only packageが失敗する場合、既存のoffline pinned Linux Docker gateで`go vet ./...`と`go build ./...`を再実行する。

- [ ] **Step 4: manual prepareを実行し、operatorへ停止点を渡す**

operatorが現在のshellへprivateな3環境変数を設定した状態で実行する。

```powershell
make cherrypick-rehearsal-validate
make cherrypick-rehearsal-prepare
```

Expected: prepareが`status=awaiting-manual-repair`だけを出力し、migrationを実行せず、隔離stackを意図的に保持する。

- [ ] **Step 5: 修復後にresumeする**

このstepではrepair SQLを作成、読取、表示、stageしない。controllerの修復成功の明示を受けてから実行する。

```powershell
make cherrypick-rehearsal-resume
```

Expected: pre-repaired orphan/invalid counts 0、migration 2回、health/login、internal network、dump rehashがPASSし、最後にcontainer/volume/network/runtime残留0。evidenceにdump hash、path、個人情報、固有ID、data hashがない。

- [ ] **Step 6: 公開用worktreeとbranchをupstream baseから作る**

現在のprivate HEADをshell変数へ保持する。値はcode revisionであり個人情報ではない。

```powershell
$privateHead = git rev-parse HEAD
$privateWorktree = (git rev-parse --show-toplevel).Trim()
$gitCommon = (Resolve-Path (git rev-parse --git-common-dir)).Path
$mainRoot = (Resolve-Path (Join-Path $gitCommon '..')).Path
$publicWorktree = Join-Path $mainRoot '.worktrees\cherrypick-compatibility-foundation-public'
git -C $mainRoot worktree add -b feature/cherrypick-compatibility-foundation-public $publicWorktree upstream-temp/develop
```

public worktreeへはprivate commitをcherry-pickしない。次のfile groupをsnapshotとして順に取り込み、各groupをtestしてcommitする。

Auth group:

```powershell
git -C $publicWorktree restore --source=$privateHead -- go.mod go.sum internal/auth/password internal/api/i/email_update.go internal/api/i/handler_2fa.go internal/api/i/handler_extra.go internal/api/i/handler_extra_test.go internal/api/i/move.go internal/api/signin/handler.go internal/api/signin/handler_test.go internal/entitycompat/password_verifier_test.go
go -C $publicWorktree test ./internal/auth/password ./internal/api/signin ./internal/api/i ./internal/entitycompat -run 'Verify|Argon2|PasswordVerifier|ChangePassword' -count=1 -v
git -C $publicWorktree add -- go.mod go.sum internal/auth/password internal/api/i internal/api/signin internal/entitycompat/password_verifier_test.go
git -C $publicWorktree diff --cached --check
git -C $publicWorktree commit -m "Phase 1: Argon2id認証互換を追加する"
```

Migration core group:

```powershell
git -C $publicWorktree restore --source=$privateHead -- cmd/migrate internal/migrationcompat migration/000001_initial.up.sql internal/entitycompat/note_relation_schema_test.go internal/repository/note_test.go
go -C $publicWorktree test ./cmd/migrate ./internal/migrationcompat ./internal/entitycompat ./internal/repository -run 'BuildDatabaseURL|CheckPreflight|AdvisoryLock|InitialMigrationPreservesDanglingNoteRelations' -count=1 -v
git -C $publicWorktree add -- cmd/migrate internal/migrationcompat migration/000001_initial.up.sql internal/entitycompat/note_relation_schema_test.go internal/repository/note_test.go
git -C $publicWorktree diff --cached --check
git -C $publicWorktree commit -m "Phase 1: migration互換preflightを追加する"
```

Decoration migration group:

```powershell
git -C $publicWorktree restore --source=$privateHead -- migration/000075_cherrypick_avatar_decoration_cleanup.up.sql migration/000075_cherrypick_avatar_decoration_cleanup.down.sql internal/entitycompat/cherrypick_avatar_decoration_migration_test.go
go -C $publicWorktree test ./internal/entitycompat -run 'TestCherryPickAvatarDecorationMigration|TestCherryPickMigrationContainsNoProductionStyleIDLiteral' -count=1 -v
git -C $publicWorktree add -- migration/000075_cherrypick_avatar_decoration_cleanup.up.sql migration/000075_cherrypick_avatar_decoration_cleanup.down.sql internal/entitycompat/cherrypick_avatar_decoration_migration_test.go
git -C $publicWorktree diff --cached --check
git -C $publicWorktree commit -m "Phase 1: 装飾migration互換を追加する"
```

Rehearsal and documentation group:

```powershell
git -C $publicWorktree restore --source=$privateHead -- Makefile docs/migration-from-cherrypick.md docs/superpowers/specs/2026-08-15-production-data-nondisclosure-design.md docs/superpowers/plans/2026-08-15-production-data-nondisclosure.md tests/cherrypick_migration
pwsh -NoProfile -File (Join-Path $publicWorktree 'tests/cherrypick_migration/verify.test.ps1')
git -C $publicWorktree add -- Makefile docs/migration-from-cherrypick.md docs/superpowers/specs/2026-08-15-production-data-nondisclosure-design.md docs/superpowers/plans/2026-08-15-production-data-nondisclosure.md tests/cherrypick_migration
git -C $publicWorktree diff --cached --check
git -C $publicWorktree commit -m "Phase 1: 隔離移行リハーサルを追加する"
```

各groupのcommit messageはprivate履歴の同種messageに合わせるが、private commit SHAや本番データ事実を書かない。

- [ ] **Step 7: public branchがprivate履歴を祖先に持たないことを検証する**

```powershell
git -C $publicWorktree merge-base --is-ancestor $privateHead HEAD
if ($LASTEXITCODE -eq 0) { throw 'private history is an ancestor of public branch' }
git -C $publicWorktree merge-base --is-ancestor upstream-temp/develop HEAD
if ($LASTEXITCODE -ne 0) { throw 'upstream base is not an ancestor of public branch' }
git -C $publicWorktree diff --check upstream-temp/develop..HEAD
git -C $publicWorktree status --short --branch
```

Expected: private ancestry check nonzero、upstream ancestry check 0、diff check 0、clean。

- [ ] **Step 8: public branchで全gateとprivacy再レビューを実行する**

Task 6 Step 2とStep 3をpublic worktreeで再実行する。その後、public HEADでmanual prepareを再実行し、controllerの修復成功後にresumeする。private branchで得た旧evidenceをpublic branchの検証根拠として再利用しない。

```powershell
git -C $publicWorktree status --short --branch
Push-Location $publicWorktree
try {
    make cherrypick-rehearsal-validate
    make cherrypick-rehearsal-prepare
} finally {
    Pop-Location
}
```

prepare後はcontrollerがDB内部snapshot方式の修復を実行し、成功した場合だけpublic worktreeで`make cherrypick-rehearsal-resume`を実行する。修復失敗時はabortして再試行しない。

さらに独立reviewerへ次を依頼する。

- upstream baseからpublic HEADまでの全差分。
- 認証、migration、data loss、fail-open、secret exposure、privacy、test gap。
- 本番由来ID、dump情報、個人情報がsource、docs、tests、historyにないこと。
- Critical/Importantがあればpush禁止。

- [ ] **Step 9: ローカル報告をrepo外へ保持し、旧branch refを削除する**

git-ignored report/logは内容を表示せず、repo外のprivate local directoryへ保持する。公開用worktreeの検証後、旧private worktreeをremoveし、旧branch refを削除する。復旧用branch、tag、bundle、patchは作成しない。

public branchを最終名へrenameする。

```powershell
$privateReportSource = Join-Path $privateWorktree '.superpowers\sdd'
$privateReportTarget = Join-Path ([System.IO.Path]::GetTempPath()) 'mk-cherrypick-private-reports'
if (Test-Path -LiteralPath $privateReportSource) {
    if (-not (Test-Path -LiteralPath $privateReportTarget)) {
        New-Item -ItemType Directory -Path $privateReportTarget | Out-Null
    }
    Copy-Item -LiteralPath $privateReportSource -Destination $privateReportTarget -Recurse -Force
    Remove-Item -LiteralPath $privateReportSource -Recurse -Force
}
git -C $mainRoot worktree remove $privateWorktree
git -C $mainRoot branch -D feature/cherrypick-compatibility-foundation
git -C $publicWorktree branch -m feature/cherrypick-compatibility-foundation
```

rename前に同名旧branchが削除済みであることを確認する。`git reset --hard`、`git checkout --`、force-pushは使わない。

- [ ] **Step 10: push直前監査を実行する**

```powershell
git -C $publicWorktree status --short --branch
git -C $publicWorktree diff --check upstream-temp/develop..HEAD
git -C $publicWorktree log --oneline --decorate -10
git -C $publicWorktree diff --stat upstream-temp/develop..HEAD
git -C $publicWorktree submodule status --recursive
git -C $publicWorktree ls-files -- '*.sql.gz' '*.dump' '*.backup' '*credential*.json' '*evidence*.json'
```

Expected: clean、dump/credential/evidence file 0、submodule unchanged、意図した差分だけ。

- [ ] **Step 11: branchをpushして日本語PRを作成する**

```powershell
git -C $publicWorktree push -u origin feature/cherrypick-compatibility-foundation
```

PR本文には一般化した変更、synthetic test、隔離検証、CI実行予定、`Closes #1`だけを書く。本番データの値、件数、hash、path、固有ID、添付を含めない。

```powershell
$prBody = @'
## 概要

CherryPickからmk-goへ移行するための認証・migration互換基盤を追加します。

## 主な変更点

- Argon2idとbcryptの既存password hashを共通検証
- migrationの同時実行防止とfail-closed preflight
- 本番固有値を持たない装飾cleanup migration
- 手動データ修復と自動migrationを分離した隔離リハーサル

## テスト

- synthetic fixtureによる認証・migration・preflight test
- Linux vet/build
- 外部通信不可の隔離環境でclean DBと移行リハーサルを検証
- 完全なtest suiteは本PRのCIで確認

本番データ、個別値、集計値、hash、local path、evidenceは添付していません。

Closes #1
'@
gh pr create --repo Misaki-Project/mk --base develop --head Misaki0331:feature/cherrypick-compatibility-foundation --title 'Phase 1 CherryPick互換基盤' --body $prBody
```

PR作成後、`gh pr checks`でGitHub CIの完全suiteを確認する。dump、evidence、local report、logはartifactやcommentへuploadしない。

---

## Final Verification Checklist

- [ ] `git status --short --branch`がclean。
- [ ] public branchのbaseが最新upstream `develop`。
- [ ] private feature commitがpublic branchの祖先ではない。
- [ ] 本番由来ID、dump情報、個人情報、資格情報pathがpublic tree/history/PRにない。
- [ ] tracked dump、credential、evidence、log、reportが0。保持対象のprivate reportはrepo外だけに存在する。
- [ ] focused Go testsがPASS。
- [ ] Linux `go vet ./...`と`go build ./...`がPASS。
- [ ] `verify.test.ps1`がPASS。
- [ ] clean DB gateがPASS。
- [ ] manual prepareがmigration前に停止。
- [ ] 修復後のresumeがmigration、health、login、isolation、cleanupを完了。
- [ ] dumpはread-onlyで、前後hashがローカル比較で不変。
- [ ] local evidenceにdump hash、path、data hash、個人情報、固有IDがない。
- [ ] frontend submoduleが変更されていない。
- [ ] 独立reviewでCritical/Importantが0。
- [ ] PR本文と添付に本番データがない。
- [ ] GitHub CIの完全suite結果を確認。
