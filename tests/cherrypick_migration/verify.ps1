#requires -Version 7.0
[CmdletBinding()]
param(
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
)

# 本番 dump の隔離リハーサル・オーケストレータ。
#
# 排他モード (ちょうど 1 つだけ指定できる):
#   -ValidateOnly         dump ハッシュと隔離条件だけを確認する (コンテナ起動なし)
#   -CleanDatabaseOnly    空 DB でマイグレーション 2 回 + 起動 + health を確認する
#   -PrepareManualRepair  復元 → pre 集計まで実行して停止し、stack を手動修復用に保持する
#   -ResumeManualRepair   保持済み stack の session を検証し、修復後の全零 gate を経て再開する
#   -AbortManualRepair    保持済み stack / runtime を破棄する
#
# コンテナは internal ネットワークのみで動かし、build / コンテナ内依存の取得は
# 一切行わない。ホストで Linux 静的バイナリをビルドしてから dump をマウントする。
# 失敗時はアプリケーションログを残さず固定のステージ名 (stage=<name>) だけを
# 標準エラーへ出し、finally で Compose の volume とランタイムディレクトリを撤去する。
# prepare 成功時だけ stack / runtime を保持し、他の mode は成功失敗を問わず撤去する。
# session.json と db-password.txt は repo 外 runtime だけに置き、標準出力や
# evidence へ path / hash / password / count を一切出さない。エビデンス JSON には
# 件数・スキーマ事実・健康/ログイン成否のみを書き、パスワードやトークンなどの
# 個別値は一切含めない。期待 dump ハッシュ (ExpectedBackupSha256) は引数として
# 受け取り比較だけに使い、source や evidence へは保存しない。

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false

$script:Stage = 'init'
$script:StartedCompose = $false
$script:CreatedRuntime = $false
$script:PreserveForManualRepair = $false
$script:Evidence = @{}

$script:ScriptDir = $PSScriptRoot
$script:RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$script:ComposeFile = Join-Path $PSScriptRoot 'compose.yml'
# 既定の session root と allowed runtime root を同じベースから導出する。
# 明示されない RuntimePath はこの default session root を基準に mode 別 subdirectory
# へ振り分ける (validate / clean-db) か、session root をそのまま使う (prepare / resume /
# abort)。compose project / volume は全 mode で共有されるため、guard は subdirectory
# だけでなく default session root も必ず検査する。
$script:DefaultSessionRoot = (Join-Path ([System.IO.Path]::GetTempPath()) 'mk-cherrypick-migration')
$script:AllowedRuntimeRoot = $script:DefaultSessionRoot
$script:ProjectName = 'mk-cherrypick-migration'
$script:PrivateNetwork = "$($script:ProjectName)_private"

# エビデンス JSON に書き込むキーのホワイトリスト。password / token 等の
# 個別値キーはここに存在し得ない (verify.test.ps1 が契約として検査する)。
$script:EvidenceKeys = @(
    'status', 'finalVerificationTimestamp',
    'shirohaAMkDevelopSha', 'misakiProjectMkImplSha', 'thirdPartyMisskeySha',
    'cherrypickReferenceSha', 'health', 'login', 'internalNetworkOnly',
    'remoteDecorationCount', 'remoteDecorationReferenceCount',
    'orphanDecorationReferenceCount', 'emptyStringHostDecorationCount',
    'schemaMigrationsPresent', 'schemaMigrationsDirty', 'pre', 'post'
)

function Set-Stage { param([string]$Name) $script:Stage = $Name }

function Write-Evidence {
    Set-Stage 'evidence'
    $obj = [ordered]@{}
    foreach ($k in $script:EvidenceKeys) {
        if ($script:Evidence.ContainsKey($k)) { $obj[$k] = $script:Evidence[$k] }
    }
    $parent = Split-Path -Parent $EvidencePath
    if ($parent -and -not (Test-Path -LiteralPath $parent)) {
        New-Item -ItemType Directory -Path $parent -Force | Out-Null
    }
    $obj | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath $EvidencePath -Encoding utf8
}

function Get-DumpHash {
    $h = (Get-FileHash -Algorithm SHA256 -LiteralPath $BackupPath).Hash
    return $h.ToLowerInvariant()
}

function Assert-DumpHash {
    if ($ExpectedBackupSha256 -notmatch '^[0-9a-fA-F]{64}$') { throw 'expected backup SHA-256 is required' }
    $actual = Get-DumpHash
    if ($actual -ne $ExpectedBackupSha256.ToLowerInvariant()) { throw 'dump hash mismatch' }
    return $actual
}

function Get-RepoHeadSha {
    $out = & git -C $script:RepoRoot rev-parse HEAD 2>$null
    if ($LASTEXITCODE -ne 0) { throw 'failed to resolve implementation commit' }
    return ([string]$out).Trim()
}

function Get-UpstreamDevelopSha {
    foreach ($ref in @('upstream-temp/develop', 'origin/develop')) {
        $out = & git -C $script:RepoRoot rev-parse $ref 2>$null
        if ($LASTEXITCODE -eq 0) { return ([string]$out).Trim() }
    }
    return Get-RepoHeadSha
}

function Get-SubmoduleSha {
    $sub = Join-Path $script:RepoRoot 'third_party\misskey'
    $out = & git -C $sub rev-parse HEAD 2>$null
    if ($LASTEXITCODE -ne 0) { throw 'failed to resolve frontend submodule commit' }
    return ([string]$out).Trim()
}

function Invoke-Docker {
    param([string]$StageName, [object[]]$Arguments, [switch]$AllowNonZero)
    $output = & docker @Arguments 2>&1
    $code = $LASTEXITCODE
    if (-not $AllowNonZero -and $code -ne 0) {
        throw "stage '$StageName' failed"
    }
    return [pscustomobject]@{ Code = $code; Output = $output }
}

function Wait-DbReady {
    for ($i = 0; $i -lt 90; $i++) {
        & docker compose -f $script:ComposeFile exec -T db pg_isready -U misskey -d misskey *> $null
        if ($LASTEXITCODE -eq 0) { return }
        Start-Sleep -Seconds 2
    }
    throw 'database did not become ready'
}

function Wait-RedisReady {
    for ($i = 0; $i -lt 60; $i++) {
        $out = & docker compose -f $script:ComposeFile exec -T redis redis-cli ping 2>&1
        if ($LASTEXITCODE -eq 0 -and ($out -join '') -match 'PONG') { return }
        Start-Sleep -Seconds 2
    }
    throw 'redis did not become ready'
}

function Invoke-Aggregate {
    param([string]$Phase)
    $r = Invoke-Docker 'aggregate' @(
        'compose', '-f', $script:ComposeFile, 'run', '--rm',
        '-e', 'PGHOST=db', '-e', 'PGDATABASE=misskey', '-e', 'PGUSER=misskey',
        '-e', "PGPASSWORD=$($script:DbPassword)",
        'aggregate', 'psql', '-A', '-t', '-q', '-v', 'ON_ERROR_STOP=1',
        '-v', "phase=$Phase",
        '-f', '/runtime/aggregate.sql'
    )
    $result = @{}
    foreach ($line in $r.Output) {
        $s = [string]$line
        if ($s -match '^([a-z_]+)=(.*)$') {
            $result[$matches[1]] = $matches[2]
        }
    }
    return $result
}

function Invoke-Migration {
    param([string]$StageName)
    return Invoke-Docker $StageName @(
        'compose', '-f', $script:ComposeFile, 'run', '--rm', '-w', '/',
        '-v', "$RuntimePath\config.yml:/migration-run/config.yml:ro",
        'migration', '/usr/local/bin/migrate', '-config',
        '/migration-run/config.yml', '-direction', 'up'
    )
}

function Wait-Healthy {
    for ($i = 0; $i -lt 90; $i++) {
        $r = Invoke-Docker 'health' @(
            'compose', '-f', $script:ComposeFile, 'run', '--rm',
            '-e', 'MK_PROBE_HEALTH_ONLY=1', 'probe', 'node', '/probe.mjs'
        ) -AllowNonZero
        if ($r.Code -eq 0) { return }
        Start-Sleep -Seconds 2
    }
    throw 'health check did not pass'
}

function Test-InternalNetworkOnly {
    $internal = [string](& docker network inspect $script:PrivateNetwork --format '{{.Internal}}' 2>$null)
    if ($LASTEXITCODE -ne 0 -or $internal.Trim() -ne 'true') { return $false }
    $containers = @(& docker ps --filter "label=com.docker.compose.project=$($script:ProjectName)" --format '{{.Names}}' 2>$null)
    # プロジェクトのコンテナが 1 つも無ければ隔離を確認できていない (fail-closed)。
    if ($containers.Count -eq 0) { return $false }
    foreach ($c in $containers) {
        $nets = [string](& docker inspect $c --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}' 2>$null)
        $netList = @(($nets -split '\s+') | Where-Object { $_ })
        if ($netList.Count -ne 1 -or $netList[0] -ne $script:PrivateNetwork) { return $false }
    }
    return $true
}

function Compare-PrePost {
    param($Pre, $Post)
    # 完全一致を要求する集計: 件数 (user/note/following/drive_file/role/role_assignment) と
    # ローカル装飾行件数。決定的ハッシュは出さないため、比較は count だけ。
    $equalKeys = @(
        'user_count', 'note_count', 'following_count', 'drive_file_count',
        'role_count', 'role_assignment_count', 'local_decoration_count'
    )
    foreach ($k in $equalKeys) {
        if ($Pre[$k] -ne $Post[$k]) {
            throw "pre/post mismatch for '$k': pre=$($Pre[$k]) post=$($Post[$k])"
        }
    }
    # post に要求する値
    $postRequired = @{
        'remote_decoration_count'            = '0'
        'remote_decoration_reference_count'  = '0'
        'orphan_decoration_reference_count'  = '0'
        'orphan_avatar_file_reference_count' = '0'
        'orphan_banner_file_reference_count' = '0'
        'empty_string_host_decoration_count' = '0'
        'invalid_decoration_json_row_count'  = '0'
        'invalid_decoration_reference_count' = '0'
        'schema_migrations_present'          = '1'
        'schema_migrations_dirty'            = 'false'
    }
    foreach ($k in $postRequired.Keys) {
        if ($Post[$k] -ne $postRequired[$k]) {
            throw "post '$k' = $($Post[$k]), expected $($postRequired[$k])"
        }
    }
}

# preserved session (prepare 成功で残った awaiting-manual-repair) が指定 root に
# 存在するかを判定する。session.json の status だけを文字列比較で確認し、
# 個別値 (hash 等) は読まない。try/catch を使わず、この関数より後に置かれる
# 本体の catch / finally を static 契約テストが誤検知しないようにする。
function Test-PreservedSession {
    param([string]$Root)
    $sessionPath = Join-Path $Root 'session.json'
    if (-not (Test-Path -LiteralPath $sessionPath -PathType Leaf)) { return $false }
    $raw = Get-Content -LiteralPath $sessionPath -Raw
    return ($raw -match '"status"\s*:\s*"awaiting-manual-repair"')
}

# 新規の隔離 runtime を作る共通ステップ (clean-db / prepare)。
# restore 自体は dump を読む mode 側で個別に実行する。ここでは volume 付きの
# DB / Redis を起動するところまでを行う。
function Initialize-IsolatedRuntime {
    Set-Stage 'runtime-dir'
    New-Item -ItemType Directory -Path $RuntimePath -Force | Out-Null
    $script:CreatedRuntime = $true

    Set-Stage 'build'
    Push-Location $script:RepoRoot
    try {
        $env:GOOS = 'linux'
        $env:GOARCH = 'amd64'
        $env:CGO_ENABLED = '0'
        & go build -tags nodynamic -trimpath -o (Join-Path $RuntimePath 'misskey') ./cmd/misskey
        if ($LASTEXITCODE -ne 0) { throw 'host linux build failed for misskey' }
        & go build -tags nodynamic -trimpath -o (Join-Path $RuntimePath 'migrate') ./cmd/migrate
        if ($LASTEXITCODE -ne 0) { throw 'host linux build failed for migrate' }
    } finally {
        Pop-Location
    }

    Set-Stage 'config'
    $configYaml = @"
url: http://app:3000/
port: 3000
db:
  host: db
  port: 5432
  db: misskey
  user: misskey
  pass: $($script:DbPassword)
dbReplications: false
redis:
  host: redis
  port: 6379
fulltextSearch:
  provider: sqlLike
id: aidx
deliverJobConcurrency: 1
inboxJobConcurrency: 1
relationshipJobConcurrency: 1
deliverJobPerSec: 1
inboxJobPerSec: 1
"@
    Set-Content -LiteralPath (Join-Path $RuntimePath 'config.yml') -Value $configYaml -Encoding utf8

    Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'aggregate.sql') -Destination (Join-Path $RuntimePath 'aggregate.sql') -Force
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'probe.mjs') -Destination (Join-Path $RuntimePath 'probe.mjs') -Force

    # restore.sh は Linux コンテナ内の sh が実行する。Set-Content は末尾に
    # プラットフォーム改行 (Windows では CRLF) を追加するため、ここでは CRLF を
    # LF へ正規化してから WriteAllText で書き、最終行の "--set ON_ERROR_STOP=on\r"
    # が psql の真偽値解析を壊さないようにする。
    $restoreSh = (@'
#!/bin/sh
set -e
psql -v ON_ERROR_STOP=1 -c "DO \$\$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'misskey') THEN CREATE ROLE misskey; END IF; END \$\$;"
gzip -dc /backup/source.sql.gz | psql --set ON_ERROR_STOP=on
'@).Replace("`r`n", "`n")
    [System.IO.File]::WriteAllText((Join-Path $RuntimePath 'restore.sh'), $restoreSh)

    Set-Stage 'db-up'
    # up が途中で失敗しても finally の down --volumes --remove-orphans が
    # 必ず実行されるよう、起動前に StartedCompose を立てる。
    $script:StartedCompose = $true
    Invoke-Docker 'db-up' @('compose', '-f', $script:ComposeFile, 'up', '-d', 'db', 'redis') | Out-Null
    Wait-DbReady
    Wait-RedisReady
}

$script:DbPassword = [Guid]::NewGuid().ToString('N')

# 明示されていない RuntimePath は、prepare / resume / abort が共有する session root
# (既定 RuntimePath そのもの) と、validate / clean-db が使う mode 別 subdirectory に
# 分ける。これにより、prepare 成功後に validate / clean-db / prepare を既定値で
# 誤実行しても preserved session / runtime を finally が削除できない。
$script:RuntimePathExplicit = $PSBoundParameters.ContainsKey('RuntimePath')
if (-not $script:RuntimePathExplicit) {
    $modeSubdir = if ($ValidateOnly) { 'validate-only' } elseif ($CleanDatabaseOnly) { 'clean-db' } else { '' }
    if ($modeSubdir) {
        $RuntimePath = Join-Path $RuntimePath $modeSubdir
    }
}

# dump を使わない mode (clean-db / abort) では restore のマウント先を構文上
# 解決できるよう、存在する非機密 tracked file を compose 補間へ設定する。
# restore service は起動しないため、この値が読まれることはない。
$usesDumpMode = $ValidateOnly -or $PrepareManualRepair -or $ResumeManualRepair
$env:MK_MIGRATION_BACKUP_PATH = if ($usesDumpMode) { $BackupPath } else { Join-Path $PSScriptRoot 'verify.ps1' }
$env:MK_MIGRATION_RUNTIME_PATH = $RuntimePath
$env:MK_MIGRATION_CONFIG_PATH = (Join-Path $RuntimePath 'config.yml')
$env:MK_MIGRATION_CREDENTIAL_PATH = if ($CredentialPath) { $CredentialPath } else { Join-Path $RuntimePath 'login.json' }
$env:MK_MIGRATION_DB_PASSWORD = $script:DbPassword

try {
    Set-Stage 'validate-params'
    # ちょうど 1 つの操作モードだけを許可する
    $activeModes = @(
        [pscustomobject]@{ Name = 'ValidateOnly';         Active = [bool]$ValidateOnly }
        [pscustomobject]@{ Name = 'CleanDatabaseOnly';    Active = [bool]$CleanDatabaseOnly }
        [pscustomobject]@{ Name = 'PrepareManualRepair';  Active = [bool]$PrepareManualRepair }
        [pscustomobject]@{ Name = 'ResumeManualRepair';   Active = [bool]$ResumeManualRepair }
        [pscustomobject]@{ Name = 'AbortManualRepair';    Active = [bool]$AbortManualRepair }
    )
    $enabledModes = @($activeModes | Where-Object { $_.Active })
    if ($enabledModes.Count -ne 1) {
        throw 'exactly one operation mode is required'
    }

    Set-Stage 'runtime-path'
    $runtimeAllowed = $RuntimePath -eq $script:AllowedRuntimeRoot -or
        $RuntimePath.StartsWith($script:AllowedRuntimeRoot + [System.IO.Path]::DirectorySeparatorChar, [System.StringComparison]::OrdinalIgnoreCase)
    if (-not $runtimeAllowed) {
        throw 'runtime path must be under the allowed runtime root'
    }

    Set-Stage 'credential-path'
    if ($CredentialPath) {
        $credFull = [System.IO.Path]::GetFullPath($CredentialPath)
        $rootFull = [System.IO.Path]::GetFullPath($script:RepoRoot)
        if ($credFull.StartsWith($rootFull + [System.IO.Path]::DirectorySeparatorChar, [System.StringComparison]::OrdinalIgnoreCase)) {
            throw 'credential path must be outside the git repository'
        }
        if (-not (Test-Path -LiteralPath $CredentialPath -PathType Leaf)) {
            throw "credential file not found: $CredentialPath"
        }
    }
    if ($ResumeManualRepair -and -not $CredentialPath) {
        throw 'CredentialPath is required for resume'
    }

    # dump を読む mode は BackupPath / ExpectedBackupSha256 を必須とする
    # (値の一致は dump-hash ステージで検証する)
    if ($usesDumpMode) {
        if (-not $BackupPath) { throw 'BackupPath is required for this mode' }
        if (-not $ExpectedBackupSha256) { throw 'ExpectedBackupSha256 is required for this mode' }
        if (-not (Test-Path -LiteralPath $BackupPath -PathType Leaf)) {
            throw "backup file not found: $BackupPath"
        }
    }

    Set-Stage 'session-guard'
    # preserved session (awaiting-manual-repair) が default session root または実行対象の
    # RuntimePath のどちらかに存在する場合、resume / abort 以外の mode は Docker
    # コマンドや cleanup の前に fail-closed で拒否する。compose project / volume は
    # 全 mode で共有されるため、mode 別 subdirectory 側だけでなく default session
    # root の session も必ず検査する。拒否時は prepare 成功と同じく stack / volume /
    # runtime を一切 cleanup しない (PreserveForManualRepair=true)。
    if (-not $ResumeManualRepair -and -not $AbortManualRepair) {
        $guardRoots = @($script:DefaultSessionRoot)
        if ($RuntimePath -ne $script:DefaultSessionRoot) { $guardRoots += $RuntimePath }
        foreach ($root in $guardRoots) {
            if (Test-PreservedSession -Root $root) {
                $script:PreserveForManualRepair = $true
                throw 'an awaiting-manual-repair session exists; use resume or abort'
            }
        }
    }

    # 前回のリハーサルが残したエビデンス (status=passed 等) を開始時に消す。
    # 失敗時は catch が sanitized な failed を書くので、古い passed が残らない。
    if (Test-Path -LiteralPath $EvidencePath) {
        Remove-Item -LiteralPath $EvidencePath -Force
    }

    Set-Stage 'compose-gate'
    Import-Module (Join-Path $PSScriptRoot 'isolation.psm1') -Force
    $violations = @(Get-ComposeViolations -ComposeFile $script:ComposeFile)
    if ($violations.Count -gt 0) {
        throw "compose isolation violations: $($violations.Count)"
    }

    Set-Stage 'images'
    $imageList = @(
        'postgres:18-alpine@sha256:9a8afca54e7861fd90fab5fdf4c42477a6b1cb7d293595148e674e0a3181de15',
        'redis:7-alpine@sha256:6ab0b6e7381779332f97b8ca76193e45b0756f38d4c0dcda72dbb3c32061ab99',
        'golang:1.26@sha256:3aff6657219a4d9c14e27fb1d8976c49c29fddb70ba835014f477e1c70636647',
        'mcr.microsoft.com/devcontainers/javascript-node:4.0.3-24-trixie@sha256:fd373e0409247e4ba0b03bcbe97419437caeaa71a8ccd835836a94a4a36820fb'
    )
    foreach ($image in $imageList) {
        & docker image inspect $image *> $null
        if ($LASTEXITCODE -ne 0) { throw "image not present locally: $image" }
    }

    Set-Stage 'shas'
    $script:Evidence['misakiProjectMkImplSha'] = Get-RepoHeadSha
    $script:Evidence['shirohaAMkDevelopSha'] = Get-UpstreamDevelopSha
    $script:Evidence['thirdPartyMisskeySha'] = Get-SubmoduleSha
    $script:Evidence['cherrypickReferenceSha'] = 'b30826d8ae'
    $script:Evidence['finalVerificationTimestamp'] = [DateTime]::UtcNow.ToString('o')

    if ($ValidateOnly) {
        Set-Stage 'dump-hash'
        Assert-DumpHash | Out-Null
        $script:Evidence['status'] = 'passed'
        Write-Evidence
    } elseif ($AbortManualRepair) {
        # 隔離 gate を通った同じ project だけを対象にする。down と runtime 削除は
        # finally が実行する (prepare 成功時以外は必ず走る)。
        Set-Stage 'compose-gate'
        Import-Module (Join-Path $PSScriptRoot 'isolation.psm1') -Force
        $violations = @(Get-ComposeViolations -ComposeFile $script:ComposeFile)
        if ($violations.Count -gt 0) {
            throw "compose isolation violations: $($violations.Count)"
        }
    } elseif ($PrepareManualRepair) {
        Set-Stage 'dump-hash'
        $actualBackupHash = Assert-DumpHash

        Initialize-IsolatedRuntime

        Set-Stage 'restore'
        Invoke-Docker 'restore' @(
            'compose', '-f', $script:ComposeFile, 'run', '--rm',
            '-v', "$RuntimePath\restore.sh:/restore.sh:ro",
            'restore', 'sh', '/restore.sh'
        ) | Out-Null

        Set-Stage 'aggregate-pre'
        $pre = Invoke-Aggregate 'pre'

        Set-Stage 'fail-closed'
        # 手動修復前の孤児装飾 / 孤児 avatar / 孤児 banner の件数は 0 を要求しない
        # (0 以外が存在することが修復対象の前提)。不正 JSON / 不正 item / 空文字 host
        # は手動修復と無関係なので 0 を要求し、標準出力・session・evidence へは
        # 件数を一切出さない。
        $prepareRequiredZero = @(
            'empty_string_host_decoration_count',
            'invalid_decoration_json_row_count',
            'invalid_decoration_reference_count'
        )
        foreach ($k in $prepareRequiredZero) {
            if ($pre[$k] -ne '0') {
                throw "pre-repair '$k' is $($pre[$k]); stop for user decision"
            }
        }

        Set-Stage 'network-inspect'
        if (-not (Test-InternalNetworkOnly)) {
            throw 'internal network isolation was not confirmed'
        }

        # 手動修復の間だけ stack / runtime を保持する。session と DB password は
        # repo 外 runtime だけに置き、標準出力や evidence へは出さない。
        $session = [ordered]@{
            status = 'awaiting-manual-repair'
            implementationSha = Get-RepoHeadSha
            backupSha256 = $actualBackupHash
            projectName = $script:ProjectName
        }
        $session | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $RuntimePath 'session.json') -Encoding utf8
        [System.IO.File]::WriteAllText((Join-Path $RuntimePath 'db-password.txt'), $script:DbPassword)
        $script:PreserveForManualRepair = $true
        Write-Host 'status=awaiting-manual-repair'
    } elseif ($ResumeManualRepair) {
        # 新しい DB を起動・restore しない。prepare が残した session / secret を
        # 検証して、手動修復済みの stack に対して migration 以降を再開する。
        Set-Stage 'resume-state'
        $sessionPath = Join-Path $RuntimePath 'session.json'
        $secretPath = Join-Path $RuntimePath 'db-password.txt'
        if (-not (Test-Path -LiteralPath $sessionPath)) { throw 'session.json not found; run prepare first' }
        if (-not (Test-Path -LiteralPath $secretPath)) { throw 'db-password.txt not found; run prepare first' }
        $session = Get-Content -LiteralPath $sessionPath -Raw | ConvertFrom-Json
        if ($session.status -ne 'awaiting-manual-repair') {
            throw "unexpected session status: $($session.status)"
        }
        if ($session.projectName -ne $script:ProjectName) {
            throw 'compose project in session does not match this harness'
        }
        if ($session.implementationSha -ne (Get-RepoHeadSha)) {
            throw 'implementation commit changed since prepare'
        }
        Set-Stage 'dump-hash'
        $actualBackupHash = Assert-DumpHash
        if ($session.backupSha256 -ne $actualBackupHash) {
            throw 'dump hash changed since prepare'
        }
        # DB password は prepare が残した secret file からのみ復元し、Compose 環境へ設定する
        $script:DbPassword = [System.IO.File]::ReadAllText($secretPath).Trim()
        $env:MK_MIGRATION_DB_PASSWORD = $script:DbPassword

        Set-Stage 'resume-ready'
        Wait-DbReady
        Wait-RedisReady

        Set-Stage 'network-inspect'
        if (-not (Test-InternalNetworkOnly)) {
            throw 'internal network isolation was not confirmed'
        }

        Set-Stage 'resume-preflight'
        # 手動修復後の全零 gate: 孤児装飾 / 孤児 avatar / 孤児 banner / 不正 JSON が
        # すべて 0 であることをマイグレーションの前に要求する。
        $pre = Invoke-Aggregate 'pre-repaired'
        $preRequiredZero = @(
            'orphan_decoration_reference_count',
            'orphan_avatar_file_reference_count',
            'orphan_banner_file_reference_count',
            'empty_string_host_decoration_count',
            'invalid_decoration_json_row_count',
            'invalid_decoration_reference_count'
        )
        foreach ($k in $preRequiredZero) {
            if ($pre[$k] -ne '0') {
                throw "resume preflight '$k' is $($pre[$k]); manual repair incomplete"
            }
        }

        Set-Stage 'migration-1'
        Invoke-Migration 'migration-1' | Out-Null

        Set-Stage 'migration-2'
        $migrate2 = Invoke-Migration 'migration-2'
        if (($migrate2.Output -join ' ') -notmatch 'no migration changes to apply') {
            throw 'second migration run reported changes (migration is not idempotent)'
        }

        Set-Stage 'aggregate-post'
        $post = Invoke-Aggregate 'post'

        Set-Stage 'compare'
        Compare-PrePost -Pre $pre -Post $post
        $script:Evidence['pre'] = $pre
        $script:Evidence['post'] = $post
        $script:Evidence['remoteDecorationCount'] = [int]$post['remote_decoration_count']
        $script:Evidence['remoteDecorationReferenceCount'] = [int]$post['remote_decoration_reference_count']
        $script:Evidence['orphanDecorationReferenceCount'] = [int]$post['orphan_decoration_reference_count']
        $script:Evidence['emptyStringHostDecorationCount'] = [int]$post['empty_string_host_decoration_count']
        $script:Evidence['schemaMigrationsPresent'] = ($post['schema_migrations_present'] -eq '1')
        $script:Evidence['schemaMigrationsDirty'] = ($post['schema_migrations_dirty'] -eq 'true')

        Set-Stage 'app-up'
        Invoke-Docker 'app-up' @(
            'compose', '-f', $script:ComposeFile, 'run', '--use-aliases', '-d', 'app',
            '/usr/local/bin/misskey', '-config', '/app/config.yml'
        ) | Out-Null

        Set-Stage 'health'
        Wait-Healthy
        $script:Evidence['health'] = 'passed'

        Set-Stage 'probe'
        $probeResult = Invoke-Docker 'probe' @(
            'compose', '-f', $script:ComposeFile, 'run', '--rm',
            'probe', 'node', '/probe.mjs'
        )
        if (($probeResult.Output -join ' ') -notmatch 'login=passed') {
            throw 'internal login probe did not pass'
        }
        $script:Evidence['login'] = 'passed'

        Set-Stage 'network-inspect'
        if (-not (Test-InternalNetworkOnly)) {
            throw 'internal network isolation was not confirmed'
        }
        $script:Evidence['internalNetworkOnly'] = $true

        Set-Stage 'rehash'
        $afterHash = Get-DumpHash
        if ($afterHash -ne $ExpectedBackupSha256.ToLowerInvariant()) {
            throw 'dump hash changed during rehearsal'
        }

        $script:Evidence['status'] = 'passed'
        Write-Evidence
    } else {
        # CleanDatabaseOnly: dump も資格情報も読まない。空 DB でマイグレーション
        # 2 回 (2 回目は no-change) + 起動 + health を確認するゲート。
        Initialize-IsolatedRuntime

        Set-Stage 'clean-db-credential'
        # probe サービスは credential を bind mount するため、プレースホルダを
        # 用意する。本番の資格情報は一切読まず、probe は health のみ行う。
        Set-Content -LiteralPath (Join-Path $RuntimePath 'login.json') -Value '{ "username": "", "password": "" }' -Encoding utf8

        Set-Stage 'migration-1'
        Invoke-Migration 'migration-1' | Out-Null

        Set-Stage 'migration-2'
        $migrate2 = Invoke-Migration 'migration-2'
        if (($migrate2.Output -join ' ') -notmatch 'no migration changes to apply') {
            throw 'second migration run reported changes (migration is not idempotent)'
        }

        Set-Stage 'app-up'
        Invoke-Docker 'app-up' @(
            'compose', '-f', $script:ComposeFile, 'run', '--use-aliases', '-d', 'app',
            '/usr/local/bin/misskey', '-config', '/app/config.yml'
        ) | Out-Null

        Set-Stage 'health'
        Wait-Healthy
        $script:Evidence['health'] = 'passed'

        Set-Stage 'network-inspect'
        if (-not (Test-InternalNetworkOnly)) {
            throw 'internal network isolation was not confirmed'
        }
        $script:Evidence['internalNetworkOnly'] = $true

        $script:Evidence['status'] = 'passed'
        Write-Evidence
    }
}
catch {
    # 失敗時は sanitized な failed エビデンスだけを書く (固定の stage / timestamp。
    # 例外メッセージや資格情報・アプリケーションログは一切書かない)。
    try {
        $parent = Split-Path -Parent $EvidencePath
        if ($parent -and -not (Test-Path -LiteralPath $parent)) {
            New-Item -ItemType Directory -Path $parent -Force | Out-Null
        }
        $failed = [ordered]@{
            'status'                     = 'failed'
            'stage'                      = $script:Stage
            'finalVerificationTimestamp' = [DateTime]::UtcNow.ToString('o')
        }
        $failed | ConvertTo-Json | Set-Content -LiteralPath $EvidencePath -Encoding utf8
    } catch {
        # エビデンス書き出し自体の失敗は stage 出力を妨げない
    }
    [Console]::Error.WriteLine("stage=$($script:Stage)")
    exit 1
}
finally {
    # prepare 成功時と session-guard で拒否した場合だけ stack / runtime を保持する。
    # guard 拒否時も shared compose project / volume を壊さないため、down --volumes と
    # runtime 削除のどちらも実行しない。それ以外の mode は成功失敗を問わず
    # down --volumes --remove-orphans と runtime 削除を実行する。
    if (-not $script:PreserveForManualRepair) {
        if ($script:StartedCompose -or $ResumeManualRepair -or $AbortManualRepair) {
            & docker compose -f $script:ComposeFile down --volumes --remove-orphans 2>&1 | Out-Null
        }
        if (Test-Path -LiteralPath $RuntimePath) {
            Remove-Item -LiteralPath $RuntimePath -Recurse -Force -ErrorAction SilentlyContinue
        }
        # 既定 (非明示) RuntimePath の validate / clean-db は subdirectory を使うため、
        # 空になった default session root の残骸も撤去する。preserved session が残る
        # 場合は guard が拒否して preserve されるか、session root が空でないので
        # 削除されない。
        if (($ValidateOnly -or $CleanDatabaseOnly) -and -not $script:RuntimePathExplicit) {
            if (Test-Path -LiteralPath $script:DefaultSessionRoot -PathType Container) {
                $children = @(Get-ChildItem -LiteralPath $script:DefaultSessionRoot -Force -ErrorAction SilentlyContinue)
                if ($children.Count -eq 0) {
                    Remove-Item -LiteralPath $script:DefaultSessionRoot -Force -ErrorAction SilentlyContinue
                }
            }
        }
    }
}
