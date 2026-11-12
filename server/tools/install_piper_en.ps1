param(
    [string]$ModelConsoleRoot = 'D:\VL-models',
    [ValidateRange(1, 8)][int]$AsrMaxConcurrent = 8,
    [ValidateRange(1, 8)][int]$EnglishMaxConcurrent = 8
)
$ErrorActionPreference = 'Stop'
$tools = Split-Path -Parent $MyInvocation.MyCommand.Path
$registry = Join-Path $ModelConsoleRoot 'manager\models.json'
$manager = Join-Path $ModelConsoleRoot 'manager\server.js'
foreach ($name in @('start-english-tts.ps1', 'stop-english-tts.ps1')) {
    Copy-Item -LiteralPath (Join-Path $tools "model-console\$name") -Destination (Join-Path $ModelConsoleRoot $name) -Force
}

$services = @(Get-Content -LiteralPath $registry -Raw -Encoding UTF8 | ConvertFrom-Json | ForEach-Object { $_ })
$added = $false
if (@($services | Where-Object { $_.id -eq 'tts-en' }).Count -eq 0) {
    $entry = [pscustomobject]@{
        id='tts-en'; name='Piper English'; role='English speech synthesis'; runtime='piper-tts';
        capabilities=@('tts','audio'); port=8003; model='en_US-lessac-medium'; uiPath='/#speech';
        apiBase='http://127.0.0.1:8003/v1'; proxyRoute='/api/tts-en/speech'; backendPath='/v1/audio/speech';
        startScript='start-english-tts.ps1'; stopScript='stop-english-tts.ps1'; pidFile='english-tts.pid';
        log='logs/english-tts.stderr.log'; maxConcurrent=$EnglishMaxConcurrent; estimatedVramMiB=0;
        resourcePolicy='cpu-resident'; exclusiveGroup=$null; recommended=$true; autoRestart=$true
    }
    $services = @($services) + @($entry)
    $added = $true
}
$asr = @($services | Where-Object { $_.id -eq 'asr' })
$english = @($services | Where-Object { $_.id -eq 'tts-en' })
if ($asr.Count -ne 1 -or $english.Count -ne 1) { throw 'Expected exactly one ASR and one English TTS service' }
$changed = $added -or $asr[0].maxConcurrent -ne $AsrMaxConcurrent -or $english[0].maxConcurrent -ne $EnglishMaxConcurrent
if ($changed) {
    $asr[0].maxConcurrent = $AsrMaxConcurrent
    $english[0].maxConcurrent = $EnglishMaxConcurrent
    $json = ConvertTo-Json -InputObject $services -Depth 12
    [System.IO.File]::WriteAllText($registry, $json, [System.Text.UTF8Encoding]::new($false))
}

$source = Get-Content -LiteralPath $manager -Raw -Encoding UTF8
if (-not $source.Contains('const traceId = req.headers["x-trace-id"]')) {
    $anchor = '  if (declaredSize) headers["Content-Length"] = declaredSize;'
    if (-not $source.Contains($anchor)) { throw 'Model console proxy layout changed; trace forwarding needs review' }
    $patch = $anchor + "`n" + '  const traceId = req.headers["x-trace-id"];' + "`n" +
        '  if (typeof traceId === "string" && /^[A-Za-z0-9._:-]{1,128}$/.test(traceId)) headers["X-Trace-ID"] = traceId;'
    $source = $source.Replace($anchor, $patch)
    [System.IO.File]::WriteAllText($manager, $source, [System.Text.UTF8Encoding]::new($false))
}
Write-Output 'Piper English TTS registered. Restart the model console to load the new registry.'
