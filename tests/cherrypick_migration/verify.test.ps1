#requires -Version 7.0
[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# 隔離リハーサルハーネス (tests/cherrypick_migration) の静的ガードテスト。
# compose.yml が検証される本番イメージ・ネットワーク・マウントの不変条件を
# すべて満たすことと、禁止条件を持つ fixture を必ず拒否することを検証する。
# コンテナは一切起動しない (docker compose config / docker image inspect のみ)。

$script:ScriptDir = $PSScriptRoot
$script:ComposeFile = Join-Path $PSScriptRoot 'compose.yml'

# ローカルに存在する immutable digest (Task 6 実行時に docker image inspect で確定)。
$script:PG_IMAGE = 'postgres:18-alpine@sha256:9a8afca54e7861fd90fab5fdf4c42477a6b1cb7d293595148e674e0a3181de15'
$script:REDIS_IMAGE = 'redis:7-alpine@sha256:6ab0b6e7381779332f97b8ca76193e45b0756f38d4c0dcda72dbb3c32061ab99'
$script:GOLANG_IMAGE = 'golang:1.26@sha256:3aff6657219a4d9c14e27fb1d8976c49c29fddb70ba835014f477e1c70636647'
$script:NODE_IMAGE = 'mcr.microsoft.com/devcontainers/javascript-node:4.0.3-24-trixie@sha256:fd373e0409247e4ba0b03bcbe97419437caeaa71a8ccd835836a94a4a36820fb'
$script:FAKE_IMAGE = 'postgres:18-alpine@sha256:0000000000000000000000000000000000000000000000000000000000000000'

# 実 compose.yml の補間用ダミー値 (存在しなくてよい。git の外で verify.ps1 が実値を注入する)。
# パスはマシン固有の temp から導出し、コミットにユーザー固有パスを残さない。
$script:VerifyTempRoot = Join-Path ([System.IO.Path]::GetTempPath()) 'mk-cherrypick-migration-verify'
$env:MK_MIGRATION_BACKUP_PATH = Join-Path $script:VerifyTempRoot 'backup.sql.gz'
$env:MK_MIGRATION_RUNTIME_PATH = Join-Path $script:VerifyTempRoot 'runtime'
$env:MK_MIGRATION_CONFIG_PATH = Join-Path $script:VerifyTempRoot 'config.yml'
$env:MK_MIGRATION_CREDENTIAL_PATH = Join-Path $script:VerifyTempRoot 'login.json'
$env:MK_MIGRATION_DB_PASSWORD = 'verifytest'

# 隔離gate の共有実装 (Task 6: compose.yml / verify.test.ps1) をそのまま再利用する。
# 検証ロジックをランナー (verify.ps1) と二重管理しないため、一箇所に集約している。
Import-Module (Join-Path $PSScriptRoot 'isolation.psm1') -Force

function Build-FixtureYaml {
    param([string]$Name, [string]$Services, [string]$NetworksBlock = "  private:`n    internal: true", [string]$VolumesBlock = '  pg18data:')
    $yaml = "name: fixture`nservices:`n$Services`nnetworks:`n$NetworksBlock`nvolumes:`n$VolumesBlock`n"
    $yaml = $yaml -replace [regex]::Escape('@@PG@@'), $script:PG_IMAGE
    $yaml = $yaml -replace [regex]::Escape('@@REDIS@@'), $script:REDIS_IMAGE
    $yaml = $yaml -replace [regex]::Escape('@@GOLANG@@'), $script:GOLANG_IMAGE
    $yaml = $yaml -replace [regex]::Escape('@@NODE@@'), $script:NODE_IMAGE
    $yaml = $yaml -replace [regex]::Escape('@@FAKEPG@@'), $script:FAKE_IMAGE
    return $yaml
}

# --- fixture 用サービスブロック ---
$script:Db = @'
  db:
    image: @@PG@@
    pull_policy: never
    networks:
      - private
'@

$script:DbPorts = @'
  db:
    image: @@PG@@
    pull_policy: never
    ports:
      - "3000:3000"
    networks:
      - private
'@

$script:DbMultiNet = @'
  db:
    image: @@PG@@
    pull_policy: never
    networks:
      - private
      - extra
'@

$script:DbNoPull = @'
  db:
    image: @@PG@@
    networks:
      - private
'@

$script:DbFloating = @'
  db:
    image: postgres:18-alpine
    pull_policy: never
    networks:
      - private
'@

$script:DbBuild = @'
  db:
    image: @@PG@@
    pull_policy: never
    build:
      context: .
    networks:
      - private
'@

$script:DbDefaultNet = @'
  db:
    image: @@PG@@
    pull_policy: never
'@

$script:DbFake = @'
  db:
    image: @@FAKEPG@@
    pull_policy: never
    networks:
      - private
'@

$script:Restore = @'
  restore:
    image: @@PG@@
    pull_policy: never
    environment:
      PGHOST: db
      PGDATABASE: misskey
      PGUSER: misskey
      PGPASSWORD: pw
    networks:
      - private
'@

$script:RestoreDumpRo = @'
  restore:
    image: @@PG@@
    pull_policy: never
    environment:
      PGHOST: db
      PGDATABASE: misskey
      PGUSER: misskey
      PGPASSWORD: pw
    working_dir: /backup
    volumes:
      - ./dump.sql.gz:/backup/source.sql.gz:ro
    networks:
      - private
'@

$script:RestoreDumpRw = @'
  restore:
    image: @@PG@@
    pull_policy: never
    environment:
      PGHOST: db
      PGDATABASE: misskey
      PGUSER: misskey
      PGPASSWORD: pw
    working_dir: /backup
    volumes:
      - ./dump.sql.gz:/backup/source.sql.gz
    networks:
      - private
'@

$script:RestoreNoPgPassword = @'
  restore:
    image: @@PG@@
    pull_policy: never
    environment:
      PGHOST: db
      PGDATABASE: misskey
      PGUSER: misskey
    networks:
      - private
'@

$script:AggregateDump = @'
  aggregate:
    image: @@PG@@
    pull_policy: never
    volumes:
      - ./dump.sql.gz:/backup/source.sql.gz:ro
    networks:
      - private
'@

$script:AggregatePg18 = @'
  aggregate:
    image: @@PG@@
    pull_policy: never
    volumes:
      - pg18data:/var/lib/postgresql
    networks:
      - private
'@

$script:MigrationRw = @'
  migration:
    image: @@GOLANG@@
    pull_policy: never
    volumes:
      - ./migration:/migration
    networks:
      - private
'@

$script:AppCred = @'
  app:
    image: @@GOLANG@@
    pull_policy: never
    volumes:
      - ./login.json:/run/secrets/login.json:ro
    networks:
      - private
'@

$script:ProbeCredRw = @'
  probe:
    image: @@NODE@@
    pull_policy: never
    volumes:
      - ./login.json:/run/secrets/login.json
    networks:
      - private
'@

$script:DbPg18Data = @'
  db:
    image: @@PG@@
    pull_policy: never
    volumes:
      - pg18data:/var/lib/postgresql/data
    networks:
      - private
'@

# --- 拒否されるべき fixture 一覧 ---
$script:Fixtures = @(
    @{ Name = 'ports'; Services = "$($script:DbPorts)`n$($script:Restore)"; Expected = 'publishes ports' }
    @{ Name = 'network-not-internal'; Services = "$($script:Db)`n$($script:Restore)"; Networks = '  private:'; Expected = 'is not internal' }
    @{ Name = 'multiple-networks'; Services = "$($script:DbMultiNet)`n$($script:Restore)"; Networks = "  private:`n    internal: true`n  extra:`n    internal: true"; Expected = 'more than one configured network' }
    @{ Name = 'no-pull-policy'; Services = "$($script:DbNoPull)`n$($script:Restore)"; Expected = 'lacks pull_policy: never' }
    @{ Name = 'floating-tag-image'; Services = "$($script:DbFloating)`n$($script:Restore)"; Expected = 'is not pinned to a sha256 digest' }
    @{ Name = 'build-key'; Services = "$($script:DbBuild)`n$($script:Restore)"; Expected = 'builds an image' }
    @{ Name = 'attached-outside-private'; Services = "$($script:DbDefaultNet)`n$($script:Restore)"; Expected = "attaches to network 'default'" }
    @{ Name = 'dump-mount-elsewhere'; Services = "$($script:RestoreDumpRo)`n$($script:AggregateDump)"; Expected = "dump mount on service 'aggregate'" }
    @{ Name = 'dump-mount-not-readonly'; Services = $script:RestoreDumpRw; Expected = 'dump mount is not read-only' }
    @{ Name = 'credential-mount-elsewhere'; Services = "$($script:Restore)`n$($script:AppCred)"; Expected = "credential mount on service 'app'" }
    @{ Name = 'credential-mount-writable'; Services = "$($script:Restore)`n$($script:ProbeCredRw)"; Expected = 'credential mount is not read-only' }
    @{ Name = 'migration-mount-not-readonly'; Services = "$($script:Restore)`n$($script:MigrationRw)"; Expected = 'migration/ mount on migration service is not read-only' }
    @{ Name = 'pg18data-elsewhere'; Services = "$($script:Restore)`n$($script:AggregatePg18)"; Expected = 'pg18data volume mount on service' }
    @{ Name = 'pg18data-wrong-target'; Services = "$($script:DbPg18Data)`n$($script:Restore)"; Expected = 'pg18data must mount at /var/lib/postgresql' }
    @{ Name = 'network-external'; Services = "$($script:Db)`n$($script:Restore)"; Networks = "  private:`n    external: true"; Expected = "network 'private' is external" }
    @{ Name = 'restore-missing-env'; Services = $script:RestoreNoPgPassword; Expected = 'restore environment missing: PGPASSWORD' }
    @{ Name = 'missing-local-image'; Services = "$($script:DbFake)`n$($script:Restore)"; Expected = 'is not present locally' }
)

# --- テストハーネス ---
$script:Passed = 0
$script:Failed = 0

function Assert-Test {
    param([string]$Name, [scriptblock]$Body)
    try {
        & $Body
        $script:Passed++
        Write-Host "PASS  $Name"
    } catch {
        $script:Failed++
        Write-Host "FAIL  $Name : $($_.Exception.Message)"
    }
}

$script:FixtureDir = Join-Path $script:VerifyTempRoot 'fixtures'
if (Test-Path -LiteralPath $script:VerifyTempRoot) { Remove-Item -LiteralPath $script:VerifyTempRoot -Recurse -Force }
New-Item -ItemType Directory -Path $script:FixtureDir -Force | Out-Null

try {
    # 各禁止条件 fixture は必ず拒否される
    foreach ($fx in $script:Fixtures) {
        $networksBlock = "  private:`n    internal: true"
        if ($fx.ContainsKey('Networks')) { $networksBlock = $fx.Networks }
        $yaml = Build-FixtureYaml -Name $fx.Name -Services $fx.Services -NetworksBlock $networksBlock -VolumesBlock '  pg18data:'
        $file = Join-Path $script:FixtureDir ($fx.Name + '.yml')
        Set-Content -LiteralPath $file -Value $yaml -Encoding utf8
        Assert-Test -Name "rejects fixture: $($fx.Name)" -Body {
            $violations = @(Get-ComposeViolations -ComposeFile $file)
            if ($violations.Count -eq 0) { throw "expected rejection but compose passed" }
            $joined = $violations -join "`n"
            if ($joined -notmatch [regex]::Escape($fx.Expected)) {
                throw "expected violation '$($fx.Expected)' not found in:`n$joined"
            }
        }
    }

    # 実 compose.yml: 全イメージがローカルに存在するときだけ合格
    Assert-Test -Name 'accepts real compose.yml with all local images' -Body {
        if (-not (Test-Path -LiteralPath $script:ComposeFile)) {
            throw "compose.yml does not exist: $script:ComposeFile"
        }
        $violations = @(Get-ComposeViolations -ComposeFile $script:ComposeFile)
        if ($violations.Count -gt 0) { throw "compose.yml has violations:`n$($violations -join "`n")" }
    }

    # 正規化出力の形状 (Step 4): 1 internal network / ports なし / pull_policy never / migration/ read-only
    Assert-Test -Name 'normalized config shape' -Body {
        if (-not (Test-Path -LiteralPath $script:ComposeFile)) { throw 'compose.yml does not exist' }
        $cfg = Invoke-DockerComposeConfig -ComposeFile $script:ComposeFile

        if ([string]$cfg.name -ne 'mk-cherrypick-migration') { throw "stack name mismatch: $($cfg.name)" }

        $netNames = @($cfg.networks.PSObject.Properties.Name)
        if ($netNames.Count -ne 1 -or $netNames[0] -ne 'private') { throw "expected exactly one network named 'private', got: $($netNames -join ',')" }
        $net = $cfg.networks.private
        $internalProp = $net.PSObject.Properties['internal']
        if ($null -eq $internalProp -or $internalProp.Value -ne $true) { throw 'private network is not internal' }
        $externalProp = $net.PSObject.Properties['external']
        if ($null -ne $externalProp -and $externalProp.Value -eq $true) { throw 'private network is external' }

        foreach ($svcProp in $cfg.services.PSObject.Properties) {
            $svc = $svcProp.Value
            if ($null -ne (Get-Prop $svc 'ports')) { throw "service $($svcProp.Name) publishes ports" }
            if ([string](Get-Prop $svc 'pull_policy') -ne 'never') { throw "service $($svcProp.Name) does not use pull_policy: never" }
            $image = [string](Get-Prop $svc 'image')
            if ($image -notmatch '@sha256:[0-9a-f]{64}') { throw "service $($svcProp.Name) image is not pinned: $image" }
        }

        # migration/ の source はリポジトリルートの migration へ解決され、read_only でマウントされる
        $expectedMigrationSource = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..\migration'))
        $migrationSvc = $cfg.services.migration
        $migrationMountSeen = $false
        foreach ($vol in @((Get-Prop $migrationSvc 'volumes'))) {
            if ([string](Get-Prop $vol 'target') -eq '/migration') {
                $migrationMountSeen = $true
                $roProp = $vol.PSObject.Properties['read_only']
                if ($null -eq $roProp -or $roProp.Value -ne $true) { throw 'migration/ mount is not read-only' }
                $src = [string](Get-Prop $vol 'source')
                if (-not $src.Equals($expectedMigrationSource, [System.StringComparison]::OrdinalIgnoreCase)) {
                    throw "migration/ source does not resolve to repo-root migration: expected '$expectedMigrationSource', got '$src'"
                }
            }
        }
        if (-not $migrationMountSeen) { throw 'migration/ mount not found' }

        # credential: /run/secrets/login.json は probe にちょうど 1 つ、read_only
        $credentialMounts = @()
        foreach ($svcProp in $cfg.services.PSObject.Properties) {
            foreach ($vol in @((Get-Prop $svcProp.Value 'volumes'))) {
                if ([string](Get-Prop $vol 'target') -eq '/run/secrets/login.json') {
                    $roProp = $vol.PSObject.Properties['read_only']
                    $credentialMounts += [pscustomobject]@{ Service = $svcProp.Name; ReadOnly = ($null -ne $roProp -and $roProp.Value -eq $true) }
                }
            }
        }
        if ($credentialMounts.Count -ne 1) { throw "expected exactly one credential mount, found $($credentialMounts.Count)" }
        if ($credentialMounts[0].Service -ne 'probe') { throw "credential mount must be on probe, got '$($credentialMounts[0].Service)'" }
        if (-not $credentialMounts[0].ReadOnly) { throw 'credential mount is not read-only' }

        # pg18data: db にちょうど 1 つ、/var/lib/postgresql のみ
        $pg18dataMounts = @()
        foreach ($svcProp in $cfg.services.PSObject.Properties) {
            foreach ($vol in @((Get-Prop $svcProp.Value 'volumes'))) {
                if ([string](Get-Prop $vol 'source') -eq 'pg18data') {
                    $pg18dataMounts += [pscustomobject]@{ Service = $svcProp.Name; Target = [string](Get-Prop $vol 'target') }
                }
            }
        }
        if ($pg18dataMounts.Count -ne 1) { throw "expected exactly one pg18data mount, found $($pg18dataMounts.Count)" }
        if ($pg18dataMounts[0].Service -ne 'db') { throw "pg18data must be on db, got '$($pg18dataMounts[0].Service)'" }
        if ($pg18dataMounts[0].Target -ne '/var/lib/postgresql') { throw "pg18data must mount at /var/lib/postgresql, got '$($pg18dataMounts[0].Target)'" }
    }

    # --- verify.ps1 ランナー挙動テスト (Task 7 Step 1) ---
    # ランナー (verify.ps1) の不変条件を契約として固定する。負入力テストは実
    # ランナーを子プロセスで叩き、失敗時の固定ステージ (stage=<name>) を検証する。
    # 構造テストはソースの静的検査で、ランナーが Docker を誤起動・誤清掃しない
    # ことと、結果 JSON に資格情報が入り得ないことを保証する。
    $script:VerifyPs1 = Join-Path $PSScriptRoot 'verify.ps1'
    $script:RunnerRepoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
    $script:RunnerAllowedRoot = (Join-Path ([System.IO.Path]::GetTempPath()) 'mk-cherrypick-migration')
    $script:RunnerUnitRoot = Join-Path $script:RunnerAllowedRoot 'verify-unit'
    $dummyBackup = Join-Path $script:VerifyTempRoot 'wrong-hash.sql.gz'
    Set-Content -LiteralPath $dummyBackup -Value 'not the production dump' -Encoding utf8
    # 合成の 64 hex。実 dump のハッシュは一切使わない (runtime parameter は比較のみ)。
    $script:FakeBackupSha256 = '0' * 64
    $inRepoCredential = Join-Path $script:RunnerRepoRoot 'tests\cherrypick_migration\compose.yml'
    # 負入力テストは既定のエビデンスパスを汚さないよう temp パスを使う
    $dummyEvidence = Join-Path $script:VerifyTempRoot 'runner-evidence.json'

    function Invoke-VerifyRunner {
        param([string[]]$Arguments)
        $output = & pwsh -NoProfile -File $script:VerifyPs1 @Arguments 2>&1
        $code = $LASTEXITCODE
        return [pscustomobject]@{ Code = $code; Output = ($output -join "`n") }
    }

    Assert-Test -Name 'verify.ps1 rejects wrong dump hash before starting compose' -Body {
        if (-not (Test-Path -LiteralPath $script:VerifyPs1)) { throw 'verify.ps1 does not exist' }
        $r = Invoke-VerifyRunner @('-ValidateOnly', '-BackupPath', $dummyBackup, '-RuntimePath', $script:RunnerUnitRoot, '-EvidencePath', $dummyEvidence, '-ExpectedBackupSha256', $script:FakeBackupSha256)
        if ($r.Code -eq 0) { throw 'expected nonzero exit for wrong dump hash' }
        if ($r.Output -notmatch 'stage=dump-hash') { throw "expected stage=dump-hash, got: $($r.Output)" }
    }

    Assert-Test -Name 'verify.ps1 rejects runtime path outside the allowed runtime root' -Body {
        if (-not (Test-Path -LiteralPath $script:VerifyPs1)) { throw 'verify.ps1 does not exist' }
        $r = Invoke-VerifyRunner @('-ValidateOnly', '-BackupPath', $dummyBackup, '-RuntimePath', 'E:\tmp\not-allowed', '-EvidencePath', $dummyEvidence)
        if ($r.Code -eq 0) { throw 'expected nonzero exit for bad runtime path' }
        if ($r.Output -notmatch 'stage=runtime-path') { throw "expected stage=runtime-path, got: $($r.Output)" }
    }

    Assert-Test -Name 'verify.ps1 rejects credential path inside git repository' -Body {
        if (-not (Test-Path -LiteralPath $script:VerifyPs1)) { throw 'verify.ps1 does not exist' }
        $r = Invoke-VerifyRunner @('-ValidateOnly', '-BackupPath', $dummyBackup, '-RuntimePath', $script:RunnerUnitRoot, '-CredentialPath', $inRepoCredential, '-EvidencePath', $dummyEvidence)
        if ($r.Code -eq 0) { throw 'expected nonzero exit for in-repo credential path' }
        if ($r.Output -notmatch 'stage=credential-path') { throw "expected stage=credential-path, got: $($r.Output)" }
    }

    Assert-Test -Name 'verify.ps1 builds Linux binaries on host before any service mounts the dump' -Body {
        if (-not (Test-Path -LiteralPath $script:VerifyPs1)) { throw 'verify.ps1 does not exist' }
        $src = Get-Content -LiteralPath $script:VerifyPs1 -Raw
        if ($src -notmatch 'GOOS\s*=\s*[''"]linux[''"]') { throw 'missing GOOS=linux host build' }
        if ($src -notmatch 'GOARCH\s*=\s*[''"]amd64[''"]') { throw 'missing GOARCH=amd64 host build' }
        if ($src -notmatch 'CGO_ENABLED\s*=\s*[''"]?0') { throw 'missing CGO_ENABLED=0 host build' }
        if ($src -notmatch '-tags nodynamic') { throw 'missing -tags nodynamic' }
        $buildIdx = $src.IndexOf('$env:GOOS')
        $restoreIdx = $src.IndexOf('/restore.sh')
        if ($buildIdx -lt 0 -or $restoreIdx -lt 0) { throw 'build step or restore step not found' }
        if ($buildIdx -gt $restoreIdx) { throw 'host build must precede restore (dump mount)' }
    }

    Assert-Test -Name 'verify.ps1 calls docker compose without --build and without pull' -Body {
        if (-not (Test-Path -LiteralPath $script:VerifyPs1)) { throw 'verify.ps1 does not exist' }
        $src = Get-Content -LiteralPath $script:VerifyPs1 -Raw
        if ($src -match '--build') { throw 'verify.ps1 contains --build' }
        if ($src -match 'pull') { throw 'verify.ps1 contains pull' }
    }

    Assert-Test -Name 'verify.ps1 always calls down --volumes --remove-orphans in finally' -Body {
        if (-not (Test-Path -LiteralPath $script:VerifyPs1)) { throw 'verify.ps1 does not exist' }
        $src = Get-Content -LiteralPath $script:VerifyPs1 -Raw
        $finallyIdx = $src.IndexOf('finally')
        $downIdx = $src.IndexOf('--remove-orphans')
        if ($finallyIdx -lt 0) { throw 'no finally block' }
        if ($downIdx -lt 0) { throw 'no --remove-orphans down call' }
        if ($finallyIdx -gt $downIdx) { throw 'down --remove-orphans must be inside finally' }
        if ($src -notmatch 'down[^\r\n]*--volumes[^\r\n]*--remove-orphans') { throw 'down must combine --volumes and --remove-orphans' }
    }

    Assert-Test -Name 'verify.ps1 re-hashes the dump after migration' -Body {
        if (-not (Test-Path -LiteralPath $script:VerifyPs1)) { throw 'verify.ps1 does not exist' }
        $src = Get-Content -LiteralPath $script:VerifyPs1 -Raw
        $hashCount = ([regex]::Matches($src, 'Get-DumpHash')).Count
        if ($hashCount -lt 3) { throw "expected at least 3 Get-DumpHash references (definition + initial + re-hash), found $hashCount" }
        $postIdx = $src.IndexOf('aggregate-post')
        $rehashIdx = $src.IndexOf('rehash')
        if ($postIdx -lt 0 -or $rehashIdx -lt 0) { throw 'aggregate-post or rehash stage not found' }
        if ($postIdx -gt $rehashIdx) { throw 'dump must be re-hashed after aggregate-post' }
    }

    Assert-Test -Name 'verify.ps1 evidence whitelist contains no privacy-sensitive keys' -Body {
        if (-not (Test-Path -LiteralPath $script:VerifyPs1)) { throw 'verify.ps1 does not exist' }
        $src = Get-Content -LiteralPath $script:VerifyPs1 -Raw
        $m = [regex]::Match($src, 'EvidenceKeys\s*=\s*@\(([^)]*)\)')
        if (-not $m.Success) { throw 'evidence key whitelist not found' }
        $keys = $m.Groups[1].Value -split ',' | ForEach-Object { $_.Trim().Trim("'") }
        foreach ($k in $keys) {
            if ($k -match '(?i)password|token|username|credential|secret|accessToken|backup|dump|path|hash|url|blurhash|email|userId|fileId|decorationId') {
                throw "evidence whitelist contains sensitive key: $k"
            }
        }
        if ($src -match 'Get-Content[^\r\n]*CredentialPath') { throw 'credential file must not be read into memory' }
        if ($src -notmatch 'ConvertTo-Json') { throw 'evidence must be serialized as JSON' }
    }

    # --- Task 7 fix round 1/5: 後始末と fail-closed の強化 ---

    Assert-Test -Name 'verify.ps1 writes restore.sh with LF line endings for Linux sh' -Body {
        if (-not (Test-Path -LiteralPath $script:VerifyPs1)) { throw 'verify.ps1 does not exist' }
        $src = Get-Content -LiteralPath $script:VerifyPs1 -Raw
        $restoreShIdx = $src.IndexOf("'@).Replace")
        $writeAllTextIdx = $src.IndexOf('[System.IO.File]::WriteAllText((Join-Path $RuntimePath ''restore.sh'')')
        if ($restoreShIdx -lt 0) { throw 'restore.sh here-string is not LF-normalized (.Replace("`r`n","`n"))' }
        if ($writeAllTextIdx -lt 0) { throw 'restore.sh must be written with WriteAllText, not Set-Content' }
        if ($restoreShIdx -gt $writeAllTextIdx) { throw 'restore.sh LF normalization must precede its write' }
        if ($src -match 'Set-Content[^\r\n]*\$RuntimePath ''restore\.sh''') { throw 'restore.sh must not use Set-Content (platform CRLF)' }
    }

    Assert-Test -Name 'verify.ps1 sets StartedCompose before docker compose up -d' -Body {
        if (-not (Test-Path -LiteralPath $script:VerifyPs1)) { throw 'verify.ps1 does not exist' }
        $src = Get-Content -LiteralPath $script:VerifyPs1 -Raw
        $m = [regex]::Match($src, 'StartedCompose\s*=\s*\$true')
        if (-not $m.Success) { throw 'StartedCompose=true assignment not found' }
        $upIdx = $src.IndexOf("'up', '-d'")
        if ($upIdx -lt 0) { throw 'up -d invocation not found' }
        if ($m.Index -gt $upIdx) { throw 'StartedCompose must be set before docker compose up -d so partial failure reaches down --volumes --remove-orphans' }
    }

    Assert-Test -Name 'verify.ps1 Test-InternalNetworkOnly fails closed when project has no running containers' -Body {
        if (-not (Test-Path -LiteralPath $script:VerifyPs1)) { throw 'verify.ps1 does not exist' }
        $src = Get-Content -LiteralPath $script:VerifyPs1 -Raw
        $start = $src.IndexOf('function Test-InternalNetworkOnly')
        $end = $src.IndexOf('function Compare-PrePost')
        if ($start -lt 0 -or $end -lt 0 -or $end -le $start) { throw 'Test-InternalNetworkOnly function region not found' }
        $fn = $src.Substring($start, $end - $start)
        if ($fn -notmatch 'containers\.Count\s*-eq\s*0') { throw 'empty container list must return false' }
        if ($fn -notmatch 'return \$false') { throw 'must return false on empty container list' }
    }

    Assert-Test -Name 'aggregate.sql counts invalid avatarDecorations rows and elements with separated safe checks' -Body {
        $aggPath = Join-Path $PSScriptRoot 'aggregate.sql'
        if (-not (Test-Path -LiteralPath $aggPath)) { throw 'aggregate.sql does not exist' }
        $agg = Get-Content -LiteralPath $aggPath -Raw
        if ($agg -notmatch 'invalid_decoration_json_row_count') { throw 'missing invalid_decoration_json_row_count' }
        if ($agg -notmatch 'invalid_decoration_reference_count') { throw 'missing invalid_decoration_reference_count' }
        # 行型チェックは jsonb_array_elements を呼ばない (スカラー/NULL を関数に渡さない)
        $rowWindow = [regex]::Match($agg, 'invalid_decoration_json_row_count.*?(?=UNION ALL)', 'Singleline').Value
        if ($rowWindow -match 'jsonb_array_elements') { throw 'row-type check must not call jsonb_array_elements' }
        # 要素チェックは配列型の行だけを jsonb_array_elements に渡す (migration 000075 と同一)
        $elemWindow = [regex]::Match($agg, 'invalid_decoration_reference_count.*?(?=UNION ALL)', 'Singleline').Value
        if ($elemWindow -notmatch "= 'array'") { throw 'element check must be gated to array rows' }
        if ($elemWindow -notmatch 'jsonb_typeof\(item\)') { throw 'element check must inspect item shape' }
    }

    Assert-Test -Name 'verify.ps1 aborts before migration when invalid avatarDecorations counts are nonzero' -Body {
        if (-not (Test-Path -LiteralPath $script:VerifyPs1)) { throw 'verify.ps1 does not exist' }
        $src = Get-Content -LiteralPath $script:VerifyPs1 -Raw
        foreach ($k in @('invalid_decoration_json_row_count', 'invalid_decoration_reference_count')) {
            if ($src -notmatch $k) { throw "missing '$k' in fail-closed gate" }
        }
        $failIdx = $src.IndexOf('fail-closed')
        $migIdx = $src.IndexOf('migration-1')
        if ($failIdx -lt 0 -or $migIdx -lt 0 -or $failIdx -gt $migIdx) { throw 'fail-closed must precede migration' }
        # post も不正 JSON が 0 であることを要求する
        if ($src -notmatch "'invalid_decoration_json_row_count'\s*=\s*'0'") { throw 'post must require invalid_decoration_json_row_count=0' }
        if ($src -notmatch "'invalid_decoration_reference_count'\s*=\s*'0'") { throw 'post must require invalid_decoration_reference_count=0' }
    }

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

    Assert-Test -Name 'aggregate.sql emits count-only orphan avatar/banner file reference keys' -Body {
        $aggPath = Join-Path $PSScriptRoot 'aggregate.sql'
        if (-not (Test-Path -LiteralPath $aggPath)) { throw 'aggregate.sql does not exist' }
        $aggregateSrc = Get-Content -LiteralPath $aggPath -Raw
        foreach ($key in @('orphan_avatar_file_reference_count', 'orphan_banner_file_reference_count')) {
            if ($aggregateSrc -notmatch [regex]::Escape($key + '=')) {
                throw "aggregate.sql does not emit $key"
            }
        }
    }

    Assert-Test -Name 'verify.ps1 preflight requires all orphan and invalid keys to be zero before migration' -Body {
        if (-not (Test-Path -LiteralPath $script:VerifyPs1)) { throw 'verify.ps1 does not exist' }
        $src = Get-Content -LiteralPath $script:VerifyPs1 -Raw
        $m = [regex]::Match($src, '\$preRequiredZero\s*=\s*@\(([^)]*)\)')
        if (-not $m.Success) { throw 'preRequiredZero zero-gate array not found' }
        $keys = @($m.Groups[1].Value -split ',' | ForEach-Object { $_.Trim().Trim("'") })
        foreach ($k in @(
            'orphan_decoration_reference_count',
            'orphan_avatar_file_reference_count',
            'orphan_banner_file_reference_count',
            'empty_string_host_decoration_count',
            'invalid_decoration_json_row_count',
            'invalid_decoration_reference_count'
        )) {
            if ($k -notin $keys) { throw "preflight zero-gate is missing key $k" }
        }
        if ($src -notmatch '\[[\$]k\]\s*-\s*ne\s*''0''') { throw 'preflight must reject any nonzero key' }
        $failIdx = $src.IndexOf('fail-closed')
        $migIdx = $src.IndexOf('migration-1')
        if ($failIdx -lt 0 -or $migIdx -lt 0 -or $failIdx -gt $migIdx) { throw 'fail-closed must precede migration-1' }
    }

    Assert-Test -Name 'verify.ps1 post contract requires orphan avatar/banner file counts to be zero' -Body {
        if (-not (Test-Path -LiteralPath $script:VerifyPs1)) { throw 'verify.ps1 does not exist' }
        $src = Get-Content -LiteralPath $script:VerifyPs1 -Raw
        if ($src -notmatch "'orphan_avatar_file_reference_count'\s*=\s*'0'") { throw 'post must require orphan_avatar_file_reference_count=0' }
        if ($src -notmatch "'orphan_banner_file_reference_count'\s*=\s*'0'") { throw 'post must require orphan_banner_file_reference_count=0' }
    }

    Assert-Test -Name 'verify.ps1 removes stale evidence and catch writes sanitized failed state only' -Body {
        if (-not (Test-Path -LiteralPath $script:VerifyPs1)) { throw 'verify.ps1 does not exist' }
        $src = Get-Content -LiteralPath $script:VerifyPs1 -Raw
        $remIdx = $src.IndexOf('Remove-Item -LiteralPath $EvidencePath -Force')
        $writeIdx = $src.LastIndexOf('Write-Evidence')
        if ($remIdx -lt 0) { throw 'stale evidence removal not found' }
        if ($remIdx -gt $writeIdx) { throw 'stale evidence removal must precede success evidence write' }
        $catchRegion = [regex]::Match($src, 'catch \{.*?finally \{', 'Singleline').Value
        if ($catchRegion -notmatch "'status'\s*=\s*'failed'") { throw 'catch must write status=failed' }
        if ($catchRegion -match '(?i)password|token|username|secret|credential|Exception\.Message') {
            throw 'catch must not write secrets or error text'
        }
    }

    Assert-Test -Name 'verify.ps1 failed evidence writes only status stage finalVerificationTimestamp' -Body {
        if (-not (Test-Path -LiteralPath $script:VerifyPs1)) { throw 'verify.ps1 does not exist' }
        $src = Get-Content -LiteralPath $script:VerifyPs1 -Raw
        $catchRegion = [regex]::Match($src, 'catch \{.*?finally \{', 'Singleline').Value
        $failedBlock = [regex]::Match($catchRegion, '\$failed\s*=\s*\[ordered\]@\{.*?\}', 'Singleline')
        if (-not $failedBlock.Success) { throw 'failed evidence ordered hashtable not found' }
        $keys = @([regex]::Matches($failedBlock.Value, "'([a-zA-Z]+)'\s*=") | ForEach-Object { $_.Groups[1].Value })
        foreach ($k in $keys) {
            if ($k -notin @('status', 'stage', 'finalVerificationTimestamp')) {
                throw "failed evidence contains disallowed key: $k"
            }
        }
        foreach ($required in @('status', 'stage', 'finalVerificationTimestamp')) {
            if ($required -notin $keys) { throw "failed evidence missing required key: $required" }
        }
    }

    Assert-Test -Name 'verify.ps1 overwrites stale passed evidence with sanitized failed on failure' -Body {
        if (-not (Test-Path -LiteralPath $script:VerifyPs1)) { throw 'verify.ps1 does not exist' }
        $stale = Join-Path $script:VerifyTempRoot 'stale-evidence.json'
        Set-Content -LiteralPath $stale -Value '{ "status": "passed" }' -Encoding utf8
        $r = Invoke-VerifyRunner @('-ValidateOnly', '-BackupPath', $dummyBackup, '-RuntimePath', $script:RunnerUnitRoot, '-EvidencePath', $stale, '-ExpectedBackupSha256', $script:FakeBackupSha256)
        if ($r.Code -eq 0) { throw 'expected nonzero exit' }
        $after = Get-Content -Raw -LiteralPath $stale
        if ($after -notmatch '"status"\s*:\s*"failed"') { throw "expected failed evidence, got: $after" }
        if ($after -match '"status"\s*:\s*"passed"') { throw 'stale passed evidence remained' }
        if ($after -match '(?i)password|token|username|secret|credential') { throw 'failed evidence contains sensitive key' }
    }

    # --- Task 5: rehearsal を manual prepare / resume へ分割する契約 ---

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

    # --- Task 5 fix round 1/5: preserved session を誤削除しない ---

    Assert-Test -Name 'verify.ps1 clean-db rejects an existing awaiting-manual-repair session without deleting it' -Body {
        if (-not (Test-Path -LiteralPath $script:VerifyPs1)) { throw 'verify.ps1 does not exist' }
        $sessionRoot = $script:RunnerUnitRoot
        $sessionFile = Join-Path $sessionRoot 'session.json'
        New-Item -ItemType Directory -Path $sessionRoot -Force | Out-Null
        Set-Content -LiteralPath $sessionFile -Value '{ "status": "awaiting-manual-repair" }' -Encoding utf8
        try {
            $r = Invoke-VerifyRunner @('-CleanDatabaseOnly', '-RuntimePath', $sessionRoot, '-EvidencePath', $dummyEvidence)
            if ($r.Code -eq 0) { throw 'expected nonzero exit for session collision' }
            if ($r.Output -notmatch 'stage=session-guard') { throw "expected stage=session-guard, got: $($r.Output)" }
            if (-not (Test-Path -LiteralPath $sessionFile)) { throw 'preserved session file was deleted' }
        } finally {
            if (Test-Path -LiteralPath $sessionRoot) { Remove-Item -LiteralPath $sessionRoot -Recurse -Force }
        }
    }

    Assert-Test -Name 'verify.ps1 defaults validate and clean-db to mode-specific runtime subdirectories' -Body {
        $src = Get-Content -LiteralPath $script:VerifyPs1 -Raw
        if ($src -notmatch 'RuntimePathExplicit') { throw 'explicit RuntimePath detection missing' }
        if ($src -notmatch "'validate-only'") { throw "missing mode-specific subdirectory 'validate-only'" }
        if ($src -notmatch "'clean-db'") { throw "missing mode-specific subdirectory 'clean-db'" }
        $guardIdx = $src.IndexOf('RuntimePathExplicit')
        $routing = [regex]::Match($src, '\$modeSubdir\s*=\s*if[^\r\n]+').Value
        if ($routing -notmatch 'ValidateOnly') { throw 'validate must use a mode-specific subdirectory' }
        if ($routing -notmatch 'CleanDatabaseOnly') { throw 'clean-db must use a mode-specific subdirectory' }
        if ($routing -match 'PrepareManualRepair|ResumeManualRepair|AbortManualRepair') { throw 'prepare/resume/abort must share the session root' }
        $routingIdx = $src.IndexOf('$modeSubdir')
        if ($guardIdx -lt 0 -or $routingIdx -lt 0 -or $guardIdx -gt $routingIdx) { throw 'mode-specific routing must appear after explicit-RuntimePath detection' }
    }

    # --- Task 5 fix round 2/5: shared compose project を守る default session root guard ---

    Assert-Test -Name 'verify.ps1 default clean-db rejects an awaiting session at the default session root without starting docker' -Body {
        if (-not (Test-Path -LiteralPath $script:VerifyPs1)) { throw 'verify.ps1 does not exist' }
        $origTemp = $env:TEMP
        $origTmp = $env:TMP
        $syntheticTemp = Join-Path $script:VerifyTempRoot 'synth-temp'
        $sessionFile = Join-Path $syntheticTemp 'mk-cherrypick-migration\session.json'
        try {
            $env:TEMP = $syntheticTemp
            $env:TMP = $syntheticTemp
            New-Item -ItemType Directory -Path (Split-Path -Parent $sessionFile) -Force | Out-Null
            Set-Content -LiteralPath $sessionFile -Value '{ "status": "awaiting-manual-repair" }' -Encoding utf8
            $r = Invoke-VerifyRunner @('-CleanDatabaseOnly', '-EvidencePath', $dummyEvidence)
            if ($r.Code -eq 0) { throw 'expected nonzero exit for default session collision' }
            if ($r.Output -notmatch 'stage=session-guard') { throw "expected stage=session-guard, got: $($r.Output)" }
            if (-not (Test-Path -LiteralPath $sessionFile)) { throw 'preserved session file was deleted' }
            $started = @(& docker ps -aq --filter 'label=com.docker.compose.project=mk-cherrypick-migration' 2>$null)
            if ($started.Count -gt 0) { throw 'docker resources were started despite session-guard rejection' }
        } finally {
            $env:TEMP = $origTemp
            $env:TMP = $origTmp
        }
    }
}
finally {
    if (Test-Path -LiteralPath $script:VerifyTempRoot) { Remove-Item -LiteralPath $script:VerifyTempRoot -Recurse -Force }
}

Write-Host ''
Write-Host "verify.test.ps1: $script:Passed passed, $script:Failed failed"
if ($script:Failed -gt 0) { exit 1 }
exit 0
