$ErrorActionPreference = 'Stop'
$repo = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..\..')).Path
$bin = Join-Path $repo '.test-artifacts\zh-tts-resident\zh_tts_resident_worker.exe'
$runtime = 'D:\en-words-toolchain\winlibs-gcc-16.2.0\toolchain\mingw64\bin'
if (-not (Test-Path -LiteralPath $bin)) { throw 'Build isolated worker first' }
if (-not (Test-Path -LiteralPath $runtime)) { throw 'WinLibs runtime unavailable' }
$env:PATH = "$runtime;$env:PATH"
$env:ZH_TTS_ISOLATED_WORKER = $bin
$env:ZH_TTS_ISOLATED_PORT = '18002'
$env:ZH_TTS_WORKERS = '4'
& node (Join-Path $PSScriptRoot 'zh_tts_resident_http.js')
