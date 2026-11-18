param(
    [string]$ModelConsoleRoot = 'D:\VL-models',
    [ValidateRange(1, 4)][int]$MaxConcurrent = 2
)
$ErrorActionPreference = 'Stop'
$tools = Split-Path -Parent $MyInvocation.MyCommand.Path
$source = Join-Path $tools 'model-console\tts-server.js'
$target = Join-Path $ModelConsoleRoot 'manager\tts-server.js'
$registry = Join-Path $ModelConsoleRoot 'manager\models.json'
foreach ($path in @($source, $target, $registry)) {
    if (!(Test-Path -LiteralPath $path)) { throw "Missing Chinese TTS dependency: $path" }
}
$services = @(Get-Content -LiteralPath $registry -Raw -Encoding UTF8 | ConvertFrom-Json | ForEach-Object { $_ })
$matches = @($services | Where-Object { $_.id -eq 'tts' })
if ($matches.Count -ne 1) { throw 'Expected exactly one Chinese TTS service' }
if ($matches[0].maxConcurrent -ne $MaxConcurrent) {
    $matches[0].maxConcurrent = $MaxConcurrent
    $json = ConvertTo-Json -InputObject $services -Depth 12
    [System.IO.File]::WriteAllText($registry, $json, [System.Text.UTF8Encoding]::new($false))
}
if ((Get-FileHash -LiteralPath $source -Algorithm SHA256).Hash -ne (Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash) {
    Copy-Item -LiteralPath $source -Destination $target -Force
}
Write-Output "Chinese TTS queue installed (manager maxConcurrent=$MaxConcurrent). Restart console and Chinese TTS service."
