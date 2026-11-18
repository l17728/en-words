$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $MyInvocation.MyCommand.Path
$PidFile = Join-Path $Root "english-tts.pid"
$LogFile = Join-Path $Root "logs\english-tts.stdout.log"
$ErrorLog = Join-Path $Root "logs\english-tts.stderr.log"
$Server = "D:\en-words\server\tools\piper_en_server.py"
$Python = if ($env:ENWORDS_PIPER_PYTHON) { $env:ENWORDS_PIPER_PYTHON } else { "C:\Users\a\enwords-runtime\python312\python.exe" }
$Model = if ($env:ENWORDS_PIPER_EN_MODEL) { $env:ENWORDS_PIPER_EN_MODEL } else { "C:\Users\a\enwords-runtime\piper-en\en_US-lessac-medium.onnx" }
$Packages = if ($env:ENWORDS_PIPER_PACKAGES) { $env:ENWORDS_PIPER_PACKAGES } else { "C:\Users\a\enwords-runtime\piper-packages" }

foreach ($Path in @($Python, $Model, "$Model.json", $Server, $Packages)) {
    if (!(Test-Path -LiteralPath $Path)) { throw "English TTS dependency is missing: $Path" }
}
if (Test-Path -LiteralPath $PidFile) {
    $ExistingPid = [int](Get-Content -LiteralPath $PidFile -Raw)
    $Existing = Get-CimInstance Win32_Process -Filter "ProcessId = $ExistingPid" -ErrorAction SilentlyContinue
    if ($Existing -and $Existing.CommandLine -like "*piper_en_server.py*") { Write-Output "English TTS is already running (PID $ExistingPid)."; exit 0 }
    if ($Existing) { throw "PID file points to another process: $ExistingPid" }
}
New-Item -ItemType Directory -Force -Path (Split-Path $LogFile) | Out-Null
$env:ENWORDS_PIPER_EN_MODEL = $Model
$env:ENWORDS_LOCAL_MODEL_KEY = & (Join-Path $Root "get-model-api-key.ps1")
$env:PYTHONPATH = $Packages
$Process = Start-Process -FilePath $Python -ArgumentList @($Server) -WorkingDirectory $Root -WindowStyle Hidden -RedirectStandardOutput $LogFile -RedirectStandardError $ErrorLog -PassThru
Set-Content -LiteralPath $PidFile -Value $Process.Id -Encoding ASCII
Remove-Item Env:ENWORDS_LOCAL_MODEL_KEY -ErrorAction SilentlyContinue
Write-Output "English TTS started (PID $($Process.Id))."
