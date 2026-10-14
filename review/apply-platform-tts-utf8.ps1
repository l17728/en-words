# Authorized controlled deployment; no model/configuration changes.
$ErrorActionPreference = 'Stop'
$RepoRoot = 'D:\en-words'
$PlatformRoot = 'D:\VL-models'
$Wrapper = Join-Path $PlatformRoot 'manager\tts-server.js'
$PidFile = Join-Path $PlatformRoot 'chinese-tts.pid'
$Evidence = Join-Path $RepoRoot 'review\tts-utf8-deployment-20261014.json'
if (Test-Path -LiteralPath $Evidence) { throw 'Deployment evidence already exists; inspect before retrying.' }
$Rows = Get-Content -LiteralPath (Join-Path $RepoRoot 'review\tts-utf8-artifact-hashes-20261014.json') -Raw | ConvertFrom-Json
foreach ($Row in $Rows) {
    $Target = if ($Row.path.StartsWith('platform/')) { Join-Path $PlatformRoot $Row.path.Substring(9) } else { Join-Path $RepoRoot $Row.path }
    if ((Get-FileHash -LiteralPath $Target -Algorithm SHA256).Hash.ToLowerInvariant() -ne $Row.sha256) { throw "Hash mismatch: $($Row.path)" }
}
$OldTtsId = [int](Get-Content -LiteralPath $PidFile -Raw)
$Listeners = @(Get-NetTCPConnection -State Listen -LocalPort 8001,8002,8090)
foreach ($Port in @(8001,8002,8090)) {
    $Found = @($Listeners | Where-Object LocalPort -eq $Port)
    if ($Found.Count -ne 1 -or $Found[0].LocalAddress -ne '127.0.0.1') { throw "Unexpected listener: $Port" }
}
$OldProcess = Get-CimInstance Win32_Process -Filter "ProcessId=$OldTtsId"
$ExpectedCommand = '"C:\Program Files\nodejs\node.exe" D:\vl-models\manager\tts-server.js'
if ($OldProcess.ExecutablePath -ine 'C:\Program Files\nodejs\node.exe' -or $OldProcess.CommandLine.Trim() -ine $ExpectedCommand -or ($Listeners | Where-Object LocalPort -eq 8002).OwningProcess -ne $OldTtsId) { throw 'PID identity mismatch' }
$Children = @(Get-CimInstance Win32_Process -Filter "ParentProcessId=$OldTtsId" | Where-Object Name -ne 'conhost.exe')
if ($Children.Count) { throw 'TTS has active children; wait for requests to end.' }
$Backup = Join-Path $PlatformRoot ('service-backups\tts-utf8-' + (Get-Date -Format 'yyyyMMdd-HHmmss'))
if (Test-Path -LiteralPath $Backup) { throw 'Backup path exists' }
New-Item -ItemType Directory -Path $Backup | Out-Null
$FileNames = @('tts-server.js','tts-utf8-worker.cpp','tts-utf8-worker.exe')
$OriginalFiles = @()
foreach ($Name in $FileNames) {
    $Target = Join-Path $PlatformRoot "manager\$Name"
    if (Test-Path -LiteralPath $Target) {
        Copy-Item -LiteralPath $Target -Destination (Join-Path $Backup $Name)
        $OriginalFiles += $Name
    }
}
Copy-Item -LiteralPath $PidFile -Destination (Join-Path $Backup 'chinese-tts.pid')
$Record = [ordered]@{ authorization='Root confirmed explicit user continue authorization for reviewed 8002-only deployment'; timestamp=(Get-Date).ToString('o'); backup=$Backup; old_pid=$OldTtsId; unchanged_ports=@($Listeners | Where-Object LocalPort -ne 8002 | Select-Object LocalPort,OwningProcess); original_files=$OriginalFiles; status='prepared' }
function Start-Tts([string]$Suffix) {
    $PreviousPort = $env:TTS_PORT
    try {
        $env:TTS_PORT = '8002'
        $Started = Start-Process -FilePath $OldProcess.ExecutablePath -ArgumentList @($Wrapper) -WorkingDirectory $PlatformRoot -WindowStyle Hidden -RedirectStandardOutput (Join-Path $Backup "$Suffix.stdout.log") -RedirectStandardError (Join-Path $Backup "$Suffix.stderr.log") -PassThru
        Set-Content -LiteralPath $PidFile -Value $Started.Id -Encoding ASCII
        return $Started.Id
    } finally { $env:TTS_PORT = $PreviousPort }
}
function Check-Tts([int]$ExpectedId) {
    for ($Attempt=0; $Attempt -lt 20; $Attempt++) {
        try { $Health = Invoke-RestMethod 'http://127.0.0.1:8002/v1/models' -TimeoutSec 2; if ($Health.data[0].id -eq 'sherpa-onnx-vits-zh-ll') { break } } catch {}
        Start-Sleep -Milliseconds 250
    }
    $Current = @(Get-NetTCPConnection -State Listen -LocalPort 8001,8002,8090)
    if (($Current | Where-Object LocalPort -eq 8002).OwningProcess -ne $ExpectedId) { throw 'New TTS listener identity mismatch' }
    foreach ($Saved in $Record.unchanged_ports) { if (($Current | Where-Object LocalPort -eq $Saved.LocalPort).OwningProcess -ne $Saved.OwningProcess) { throw 'Other platform service changed' } }
}
$NewTtsId = 0
try {
    Stop-Process -Id $OldTtsId
    Wait-Process -Id $OldTtsId -Timeout 10 -ErrorAction SilentlyContinue
    Copy-Item -LiteralPath (Join-Path $RepoRoot 'review\platform-tts-server.candidate.cjs') -Destination $Wrapper
    Copy-Item -LiteralPath (Join-Path $RepoRoot 'review\platform-tts-utf8-worker.cpp') -Destination (Join-Path $PlatformRoot 'manager\tts-utf8-worker.cpp')
    Copy-Item -LiteralPath (Join-Path $RepoRoot '.test-tools\tts-utf8-worker.exe') -Destination (Join-Path $PlatformRoot 'manager\tts-utf8-worker.exe')
    $NewTtsId = Start-Tts 'applied'
    $Record.new_pid = $NewTtsId
    Check-Tts $NewTtsId
    & python (Join-Path $RepoRoot 'review\platform-tts-deploy-smoke.py') (Join-Path $RepoRoot 'review\tts-utf8-deployed-smoke-20261014.json')
    if ($LASTEXITCODE -ne 0) { throw 'Deployed smoke failed' }
    Check-Tts $NewTtsId
    $Record.status = 'applied_smoke_passed'
    $Record.installed_hashes = @($FileNames | ForEach-Object { $Path = Join-Path $PlatformRoot "manager\$_"; @{name=$_; sha256=(Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()} })
} catch {
    $Record.failure_type = $_.Exception.GetType().Name
    if ($NewTtsId) {
        $NewProcess = Get-CimInstance Win32_Process -Filter "ProcessId=$NewTtsId"
        if ($NewProcess -and $NewProcess.ExecutablePath -ieq $OldProcess.ExecutablePath -and $NewProcess.CommandLine -match [regex]::Escape($Wrapper)) { Stop-Process -Id $NewTtsId; Wait-Process -Id $NewTtsId -Timeout 10 -ErrorAction SilentlyContinue }
    }
    foreach ($Name in $OriginalFiles) { Copy-Item -LiteralPath (Join-Path $Backup $Name) -Destination (Join-Path $PlatformRoot "manager\$Name") }
    $RollbackId = Start-Tts 'rollback'
    Check-Tts $RollbackId
    $Record.status = 'rolled_back'
    $Record.rollback_pid = $RollbackId
    throw
} finally {
    $Record.finished=(Get-Date).ToString('o')
    $Record | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath $Evidence -Encoding UTF8
}
$Record | ConvertTo-Json -Depth 6
