import hashlib
from pathlib import Path
import struct
import tempfile
import unittest
import wave
from score_speech_quality import distance, normalize, pcm_metrics, score, verify_manifest


class SpeechScoringTests(unittest.TestCase):
    def test_manifest_digest_prevents_label_replacement(self):
        raw=b'{"samples":[]}'
        report={"manifest_sha256":hashlib.sha256(raw).hexdigest()}
        self.assertEqual(verify_manifest(report,raw),report["manifest_sha256"])
        with self.assertRaises(ValueError):
            verify_manifest(report,raw+b' ')

    def test_normalization_preserves_negation_and_number_spelling(self):
        self.assertEqual(normalize("  I DO not know!  "), "i do not know")
        self.assertNotEqual(normalize("Two lessons"), normalize("2 lessons"))
        self.assertNotEqual(normalize("I do not know"), normalize("I do know"))

    def test_edit_distance_counts_insertions_deletions_and_substitution(self):
        self.assertEqual(distance(["a", "b"], ["a", "x", "c"]), 2)
        self.assertEqual(distance(["a", "b"], []), 2)

    def test_failed_transcript_remains_in_denominator(self):
        labels = {"samples": [{"case_id": "x", "text": "I do not know", "category": "classroom", "critical_slots": ["negation"]}]}
        report = {"expected_cases": 1, "cases": [{"case_id": "x", "transcript": "", "transport_pass": False}]}
        scored = score(report, labels)
        self.assertEqual(scored["wer"], 1)
        self.assertEqual(scored["critical_total"], 1)
        self.assertEqual(scored["critical_pass"], 0)

    def test_wav_silence_energy_hash_and_truncation(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "sample.wav"
            for sample, expected in ((0, False), (1000, True)):
                with wave.open(str(path), "wb") as wav:
                    wav.setnchannels(1); wav.setsampwidth(2); wav.setframerate(16000)
                    wav.writeframes(struct.pack("<h", sample) * 160)
                sha = hashlib.sha256(path.read_bytes()).hexdigest()
                self.assertEqual(pcm_metrics(path, sha)["decoded_non_silent"], expected)
            with self.assertRaises(ValueError):
                pcm_metrics(path, "wrong")
            path.write_bytes(path.read_bytes()[:-1])
            with self.assertRaises(ValueError):
                pcm_metrics(path, hashlib.sha256(path.read_bytes()).hexdigest())

    def test_rejects_unknown_duplicate_and_wrong_count(self):
        labels={"samples":[{"case_id":"x","text":"apple","category":"word"}]}
        for report in ({"expected_cases":2,"cases":[]},
                       {"expected_cases":1,"cases":[{"case_id":"other"}]},
                       {"expected_cases":1,"cases":[{"case_id":"x"},{"case_id":"x"}]}):
            with self.assertRaises(ValueError):
                score(report, labels)


if __name__ == "__main__":
    unittest.main()
