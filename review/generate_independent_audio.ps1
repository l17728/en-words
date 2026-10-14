param([string]$Manifest = (Join-Path $PSScriptRoot 'audio-fixtures/manifest.json'))
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Speech
$manifestPath = (Resolve-Path -LiteralPath $Manifest).Path
$fixtureRoot = Split-Path -Parent $manifestPath
$document = Get-Content -LiteralPath $manifestPath -Encoding UTF8 -Raw | ConvertFrom-Json
$speaker = New-Object System.Speech.Synthesis.SpeechSynthesizer
try {
    $speaker.SelectVoice('Microsoft Zira Desktop')
    $speaker.Rate = 0
    $speaker.Volume = 100
    $format = New-Object System.Speech.AudioFormat.SpeechAudioFormatInfo(16000, [System.Speech.AudioFormat.AudioBitsPerSample]::Sixteen, [System.Speech.AudioFormat.AudioChannel]::Mono)
    foreach ($sample in $document.samples) {
        if ($sample.language -ne 'en') { throw 'Only independently sourced English labels are supported.' }
        $target = [System.IO.Path]::GetFullPath((Join-Path $fixtureRoot $sample.file))
        if (-not $target.StartsWith($fixtureRoot + [System.IO.Path]::DirectorySeparatorChar, [System.StringComparison]::OrdinalIgnoreCase)) { throw 'Audio path escapes fixture directory.' }
        if (Test-Path -LiteralPath $target) { throw "Refusing to overwrite frozen audio: $target" }
        New-Item -ItemType Directory -Path (Split-Path -Parent $target) -Force | Out-Null
        $speaker.SetOutputToWaveFile($target, $format)
        $speaker.Speak([string]$sample.text)
        $speaker.SetOutputToNull()
        Write-Output "$($sample.case_id) generated"
    }
} finally { $speaker.Dispose() }
Write-Output 'Run review/freeze_audio_manifest.py to validate PCM, compute hashes, and freeze source metadata.'
