$ErrorActionPreference = 'Continue'
$report = 'D:\en-words\review\ladder-integrated-browser-acceptance-20261016'
$web = Join-Path $report 'source\web'
$node = 'C:\Program Files\nodejs\node.exe'
$cli = Join-Path $web 'node_modules\@playwright\test\cli.js'
$binary = Join-Path $report 'bin\en-words-formal.exe'
$harness = Join-Path $report 'bin\en-words-api-harness.test.exe'
$serverSource = Join-Path $report 'source\server'
$browserCache = Join-Path $env:LOCALAPPDATA 'ms-playwright'
$env:E2E_SERVER_BINARY = $binary
$env:E2E_HARNESS_BINARY = $harness
$env:E2E_EN_AUDIO = Join-Path $report 'source\review\audio-fixtures\en\05.wav'
$env:E2E_ZH_AUDIO = Join-Path $report 'source\review\audio-fixtures\journey-cat-b-sentence-20261014.wav'
$env:PLAYWRIGHT_BROWSERS_PATH = $browserCache
$env:PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD = '1'
$env:CI = '1'
$env:NPM_CONFIG_CACHE = Join-Path $report 'cache\npm'
$env:TEMP = Join-Path $report 'tmp'
$env:TMP = $env:TEMP
$env:Path = "C:\Program Files\nodejs;$(Join-Path $web 'node_modules\.bin');$env:Path"
$null = New-Item -ItemType Directory -Force -Path (Join-Path $report 'logs'),(Join-Path $report 'cache\npm'),$env:TEMP
if (-not (Test-Path -LiteralPath $harness -PathType Leaf)) {
  Push-Location -LiteralPath $serverSource
  try {
    & go test -c -o $harness ./internal/api
    if ($LASTEXITCODE -ne 0) { throw "Failed to build E2E harness (exit $LASTEXITCODE)" }
  } finally { Pop-Location }
}
if (-not (Test-Path -LiteralPath $harness -PathType Leaf)) { throw 'E2E harness binary was not created' }
$suites = @(
  @{config='playwright.config.ts';ports=@(18080,15173)},
  @{config='playwright.headless.config.ts';ports=@(18080,15173)},
  @{config='playwright.closure.config.ts';ports=@(18080,15173)},
  @{config='playwright.continuation.config.ts';ports=@(18081,15174)},
  @{config='playwright.duplex.config.ts';ports=@(18081,15174)},
  @{config='playwright.feedback.config.ts';ports=@(18080,15173)},
  @{config='playwright.firstuse.config.ts';ports=@(18080,15173)},
  @{config='playwright.interaction.config.ts';ports=@(18081,15174)},
  @{config='playwright.journey.config.ts';ports=@(18080,15173)},
  @{config='playwright.ladder.config.ts';ports=@(18081,15174)},
  @{config='playwright.media.config.ts';ports=@(18082,15175)},
  @{config='playwright.overdue.config.ts';ports=@(18081,15174)},
  @{config='playwright.playback.config.ts';ports=@(18080,15173)},
  @{config='playwright.recovery.config.ts';ports=@(18081,15174)},
  @{config='playwright.repeat.config.ts';ports=@(18081,15174)},
  @{config='playwright.startup.config.ts';ports=@(18083,15175)},
  @{config='playwright.subtitle-real.config.ts';ports=@(18082,15175)}
)
$results = [Collections.Generic.List[object]]::new()
foreach ($suite in $suites) {
  $available = $true; $busy = @()
  foreach ($port in $suite.ports) {
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback,[int]$port)
    try { $listener.Start(); $listener.Stop() } catch { $available = $false; $busy += [int]$port; try { $listener.Stop() } catch {} }
  }
  $safe = [IO.Path]::GetFileNameWithoutExtension($suite.config)
  $out = Join-Path $report "logs\$safe.stdout.log"; $err = Join-Path $report "logs\$safe.stderr.log"
  $entry = [ordered]@{config=$suite.config; ports=$suite.ports; started_at=(Get-Date).ToString('o'); finished_at=$null; pid=$null; exit_code=$null; duration_seconds=0; status='not_run'; busy_ports=$busy}
  if (-not $available) { $entry.status='skipped_port_busy'; $entry.finished_at=(Get-Date).ToString('o'); $results.Add([pscustomobject]$entry); $results | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $report 'evidence\playwright-results.json') -Encoding UTF8; continue }
  $argLine = '"{0}" test --config "{1}"' -f $cli,$suite.config
  $sw = [Diagnostics.Stopwatch]::StartNew()
  $proc = Start-Process -FilePath $node -ArgumentList $argLine -WorkingDirectory $web -PassThru -NoNewWindow -RedirectStandardOutput $out -RedirectStandardError $err
  $entry.pid = $proc.Id
  $proc.WaitForExit(); $sw.Stop(); $proc.Refresh()
  $entry.exit_code = $proc.ExitCode; $entry.duration_seconds = [math]::Round($sw.Elapsed.TotalSeconds,2); $entry.finished_at=(Get-Date).ToString('o')
  if ($proc.ExitCode -eq 0) { $entry.status='passed' } else { $entry.status='failed' }
  $results.Add([pscustomobject]$entry)
  $results | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $report 'evidence\playwright-results.json') -Encoding UTF8
  "[$($entry.status)] $($suite.config) pid=$($entry.pid) exit=$($entry.exit_code) duration=$($entry.duration_seconds)s"
}
