$ErrorActionPreference = 'Stop'
$repo = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..\..')).Path
$compiler = 'D:\en-words-toolchain\winlibs-gcc-16.2.0\toolchain\mingw64\bin\g++.exe'
$header = 'D:\VL-models\sherpa-onnx\include'
$outDir = Join-Path $repo '.test-artifacts\zh-tts-resident'
if (-not (Test-Path -LiteralPath $compiler)) { throw 'C++ compiler unavailable' }
if (-not (Test-Path -LiteralPath $header)) { throw 'Sherpa C API header unavailable' }
New-Item -ItemType Directory -Force -Path $outDir | Out-Null
& $compiler -std=c++17 -O2 -municode -I $header (Join-Path $PSScriptRoot 'zh_tts_resident_worker.cpp') -o (Join-Path $outDir 'zh_tts_resident_worker.exe')
if ($LASTEXITCODE -ne 0) { throw "Compiler failed: $LASTEXITCODE" }
Write-Output (Join-Path $outDir 'zh_tts_resident_worker.exe')
