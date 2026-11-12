$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $MyInvocation.MyCommand.Path
$PidFile = Join-Path $Root "english-tts.pid"
if (!(Test-Path -LiteralPath $PidFile)) { Write-Output "English TTS is not running."; exit 0 }
$ServicePid = [int](Get-Content -LiteralPath $PidFile -Raw)
$Process = Get-CimInstance Win32_Process -Filter "ProcessId = $ServicePid" -ErrorAction SilentlyContinue
if ($Process -and $Process.CommandLine -like "*piper_en_server.py*") { Stop-Process -Id $ServicePid -Force }
Remove-Item -LiteralPath $PidFile -Force
Write-Output "English TTS stopped."
