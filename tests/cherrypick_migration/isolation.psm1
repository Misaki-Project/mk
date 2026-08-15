#requires -Version 7.0

# 隔離リハーサルハーネス (tests/cherrypick_migration) の Compose 静的不変条件を
# 判定する共通関数。verify.test.ps1 (静的テスト) と verify.ps1 (ランナー) の両方から
# 使う。Task 6 (commit a36873c6..a8338962) の gate 実装をそのまま共有する。
#
# コンテナは一切起動しない (docker compose config / docker image inspect のみ)。

function Get-Prop {
    param($Obj, [string]$Name)
    if ($null -eq $Obj) { return $null }
    $p = $Obj.PSObject.Properties[$Name]
    if ($null -eq $p) { return $null }
    return $p.Value
}

function Invoke-DockerComposeConfig {
    param([string]$ComposeFile)
    $json = & docker compose -f $ComposeFile config --format json 2>$null
    if ($LASTEXITCODE -ne 0) { throw "docker compose config failed for $ComposeFile" }
    return ($json | ConvertFrom-Json)
}

# 禁止条件を列挙する。1 つもなければ空リスト (= 合格)。
function Get-ComposeViolations {
    param([string]$ComposeFile)
    $raw = Get-Content -LiteralPath $ComposeFile -Raw
    $cfg = Invoke-DockerComposeConfig -ComposeFile $ComposeFile
    $violations = [System.Collections.Generic.List[string]]::new()

    # 生 YAML の静的キー検出 (compose が正規化で落とす余地を残さない)
    if ($raw -match '(?m)^\s*ports:') { $violations.Add('raw YAML contains a ports: key') }
    if ($raw -match '(?m)^\s*build:') { $violations.Add('raw YAML contains a build: key') }

    # ネットワーク: ちょうど 1 つ、かつ internal: true
    if ($null -eq $cfg.networks -or @($cfg.networks.PSObject.Properties).Count -eq 0) {
        $violations.Add('no network is configured')
    } else {
        $netNames = @($cfg.networks.PSObject.Properties.Name)
        if ($netNames.Count -gt 1) { $violations.Add("more than one configured network: $($netNames -join ',')") }
        foreach ($n in $netNames) {
            $net = $cfg.networks.$n
            $internalProp = $net.PSObject.Properties['internal']
            $isInternal = ($null -ne $internalProp -and $internalProp.Value -eq $true)
            if (-not $isInternal) { $violations.Add("network '$n' is not internal") }
            $externalProp = $net.PSObject.Properties['external']
            if ($null -ne $externalProp -and $externalProp.Value -eq $true) {
                $violations.Add("network '$n' is external")
            }
        }
    }

    $credentialMounts = [System.Collections.Generic.List[object]]::new()
    $pg18dataMounts = [System.Collections.Generic.List[object]]::new()

    foreach ($svcProp in $cfg.services.PSObject.Properties) {
        $svcName = $svcProp.Name
        $svc = $svcProp.Value

        if ($null -ne (Get-Prop $svc 'ports')) { $violations.Add("service '$svcName' publishes ports") }
        if ($null -ne (Get-Prop $svc 'build')) { $violations.Add("service '$svcName' builds an image") }

        $pp = Get-Prop $svc 'pull_policy'
        if ($pp -ne 'never') { $violations.Add("service '$svcName' lacks pull_policy: never") }

        $image = [string](Get-Prop $svc 'image')
        if ([string]::IsNullOrWhiteSpace($image)) {
            $violations.Add("service '$svcName' has no image")
        } elseif ($image -notmatch '@sha256:[0-9a-f]{64}') {
            $violations.Add("service '$svcName' image is not pinned to a sha256 digest: $image")
        } else {
            & docker image inspect $image *> $null
            if ($LASTEXITCODE -ne 0) { $violations.Add("image '$image' is not present locally") }
        }

        $svcNetworks = Get-Prop $svc 'networks'
        $attached = @($svcNetworks.PSObject.Properties.Name)
        if ($attached.Count -eq 0) { $attached = @('default') }
        foreach ($n in $attached) {
            if ($n -ne 'private') { $violations.Add("service '$svcName' attaches to network '$n' instead of 'private'") }
        }

        $volumes = Get-Prop $svc 'volumes'
        if ($null -ne $volumes) {
            foreach ($vol in $volumes) {
                $target = [string](Get-Prop $vol 'target')
                $source = [string](Get-Prop $vol 'source')
                $roProp = $vol.PSObject.Properties['read_only']
                $ro = ($null -ne $roProp -and $roProp.Value -eq $true)

                if ($target -eq '/backup/source.sql.gz') {
                    if ($svcName -ne 'restore') { $violations.Add("dump mount on service '$svcName' (must be restore only)") }
                    if (-not $ro) { $violations.Add('dump mount is not read-only') }
                }
                if ($target -eq '/run/secrets/login.json') {
                    $credentialMounts.Add([pscustomobject]@{ Service = $svcName; ReadOnly = $ro })
                }
                if ($target -eq '/migration' -and $svcName -eq 'migration' -and -not $ro) {
                    $violations.Add('migration/ mount on migration service is not read-only')
                }
                if ($source -eq 'pg18data') {
                    $pg18dataMounts.Add([pscustomobject]@{ Service = $svcName; Target = $target })
                }
            }
        }
    }

    # credential: /run/secrets/login.json は probe にちょうど 1 つ、read_only のみ許可
    if ($credentialMounts.Count -ne 1) {
        $violations.Add("expected exactly one credential mount at /run/secrets/login.json, found $($credentialMounts.Count)")
    }
    foreach ($cm in $credentialMounts) {
        if ($cm.Service -ne 'probe') { $violations.Add("credential mount on service '$($cm.Service)' (must be probe only)") }
        if (-not $cm.ReadOnly) { $violations.Add('credential mount is not read-only') }
    }

    # pg18data: db にちょうど 1 つ、/var/lib/postgresql のみ (PG18 の data ディレクトリ位置)
    if ($pg18dataMounts.Count -ne 1) {
        $violations.Add("expected exactly one pg18data volume mount, found $($pg18dataMounts.Count)")
    }
    foreach ($pm in $pg18dataMounts) {
        if ($pm.Service -ne 'db') { $violations.Add("pg18data volume mount on service '$($pm.Service)' (must be db only)") }
        if ($pm.Target -ne '/var/lib/postgresql') { $violations.Add("pg18data must mount at /var/lib/postgresql, not '$($pm.Target)'") }
    }

    $restoreProp = $cfg.services.PSObject.Properties['restore']
    if ($null -eq $restoreProp) {
        $violations.Add('restore service is missing')
    } else {
        $envObj = Get-Prop $restoreProp.Value 'environment'
        $missing = @()
        foreach ($k in 'PGHOST', 'PGDATABASE', 'PGUSER', 'PGPASSWORD') {
            if ($null -eq $envObj.PSObject.Properties[$k]) { $missing += $k }
        }
        if ($missing.Count -gt 0) { $violations.Add("restore environment missing: $($missing -join ',')") }
        elseif ([string](Get-Prop $envObj 'PGHOST') -ne 'db') { $violations.Add('restore PGHOST must be db') }
    }

    return $violations
}
