from pathlib import Path
import difflib

original = Path('D:/VL-models/manager/tts-server.js').read_text(encoding='utf-8')
replacement = r'''function synthesize(input, voice, speed, signal) {
  return new Promise((resolve, reject) => {
    const work = fs.mkdtempSync(path.join(os.tmpdir(), "picer-tts-"));
    const textFile = path.join(work, "input.txt");
    const output = path.join(work, "output.wav");
    const helper = path.join(ROOT, "manager", "tts-utf8-worker.exe");
    const cleanup = () => {
      for (const file of [textFile, output]) try { fs.unlinkSync(file); } catch {}
      try { fs.rmdirSync(work); } catch {}
    };
    if (signal.aborted) { cleanup(); reject(new Error("TTS canceled")); return; }
    try { fs.writeFileSync(textFile, input, { encoding: "utf8", flag: "wx" }); }
    catch (error) { cleanup(); reject(error); return; }
    let child;
    const abort = () => { if (child) child.kill(); };
    try { child = execFile(helper, [textFile, output, String(voice), String(speed)], {
      cwd: ROOT, windowsHide: true, timeout: 120000, maxBuffer: 64 * 1024
    }, (error) => {
      signal.removeEventListener("abort", abort);
      try {
        // Native stderr can contain input fragments; keep API errors bounded.
        if (error) { reject(new Error(`TTS synthesis failed (exit ${error.code ?? "unknown"})`)); return; }
        if (signal.aborted) { reject(new Error("TTS canceled")); return; }
        resolve({ output, audio: fs.readFileSync(output) });
      } catch (readError) { reject(readError); }
      finally { cleanup(); }
    }); } catch (error) { cleanup(); reject(error); return; }
    signal.addEventListener("abort", abort, { once: true });
    if (signal.aborted) abort();
  });
}
'''
start = original.index('function synthesize(')
end = original.index('\nconst server =', start)
modified = original[:start] + replacement + original[end:]
modified = modified.replace('const EXECUTABLE = path.join(ROOT, "sherpa-onnx", "bin", "sherpa-onnx-offline-tts.exe");\n', '')
modified = modified.replace('const MODEL_DIR = path.join(ROOT, "models", MODEL_ID);\n', '')
cpp = Path('review/platform-tts-utf8-worker.cpp').read_text(encoding='utf-8')
patch = ''.join(difflib.unified_diff(original.splitlines(True), modified.splitlines(True), fromfile='a/manager/tts-server.js', tofile='b/manager/tts-server.js'))
patch += ''.join(difflib.unified_diff([], cpp.splitlines(True), fromfile='/dev/null', tofile='b/manager/tts-utf8-worker.cpp'))
Path('review/platform-tts-utf8.patch').write_text(patch, encoding='utf-8')
Path('review/platform-tts-server.candidate.cjs').write_text(modified, encoding='utf-8')
print('Candidate patch generated; shared platform remains unchanged.')
