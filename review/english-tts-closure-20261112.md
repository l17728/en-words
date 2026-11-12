# Offline English TTS closure evidence (2026-11-12)

## User journey

The 8090 model console now registers a separate CPU Piper English service (`tts-en`, 8003). The product server discovers it independently of the text model and routes `lang=en` through `/api/tts-en/speech`. A classroom segment receives a completion event only after playback starts and ends. Failure pauses the same segment; the student can retry it or explicitly choose a logged text-only skip.

## Verification

| Gate | Result |
| --- | --- |
| `go test -p 1 ./...` and `go vet ./...` | All packages passed; vet clean |
| Core Go coverage | `api` 82.3%, `classroom` 90.3%, `localmodel` 89.8% |
| `npm run build` | Passed, 3134 modules |
| Playwright interaction boundaries | 5/5 passed: retry, reload then explicit skip, browser ended without start, local Audio ended without playing, delayed first capability discovery |
| Playwright real media | 1/1 passed: English and Chinese ASR, 4 actual audio playbacks, 2 successful synthesis requests due to English cache reuse, lesson settlement |
| Playwright real subtitles | 1/1 passed: 6 playback intervals with matching captions, 2 synthesis requests and 4 cache hits |
| Piper annotated lexicon | 87/87 special-format words synthesized and decoded; 4 invalid input classes rejected |
| Model console | `tts-en=ready`; annotated phrase through 8090 returned WAV 200; startup script and registry installer are idempotent |
| Cancellation | Disconnected queued request logged `canceled_in_queue` and did not enter synthesis |
| Human listening | User confirmed `teacher`, `cat`, and `apple` all sound clear, accurate, and natural (2026-11-12) |

The automated ASR and media tests use frozen independent audio fixtures. The three-word human listening check supports pronunciation quality for those samples only. Live classroom sound and broader pronunciation quality remain separate acceptance items.

## Diagnostic fields

The manager forwards a bounded `X-Trace-ID`. Sidecar records trace ID, model, outcome and duration without spoken text or credentials. Classroom logs retain lesson, turn, segment, language, playback result and explicit `explicit_no_audio` skip reason. Browser probes record local source and actual playback start.

## Reusable checks

Treat text, ASR and each TTS language as independent provider health. Require both playback start and end before sending an ACK. Keep failed segments in place until a user retries or explicitly confirms a skip. Test first-use status races without prewarming the provider API, and run corpus-based synthesis checks against real wordbook punctuation instead of testing only simple words.
