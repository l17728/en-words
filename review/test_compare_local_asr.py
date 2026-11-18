import unittest
from compare_local_asr import multipart,normalize_protocol,dataset

class ComparisonProtocolTests(unittest.TestCase):
    def test_payload_contains_audio_language_and_no_answer_hint(self):
        audio=b'RIFF\x00\xffWAVE'
        body,kind=multipart(audio,'zh',None)
        self.assertIn(audio,body)
        self.assertIn(b'name="language"\r\n\r\nzh',body)
        self.assertNotIn(b'name="prompt"',body)
        self.assertNotIn(b'name="temperature"',body)
        self.assertTrue(kind.startswith('multipart/form-data; boundary='))

    def test_d_arm_has_numeric_zero_temperature_and_full_language(self):
        body,_=multipart(b'audio','Chinese',0)
        self.assertIn(b'name="temperature"\r\n\r\n0',body)
        self.assertIn(b'name="language"\r\n\r\nChinese',body)
        self.assertNotIn(b'prompt',body)

    def test_normalization_only_removes_protocol_not_mistakes(self):
        self.assertEqual(normalize_protocol('language Chinese<asr_text>\u6697\u91d1\u3002<|endoftext|>'),'\u6697\u91d1\u3002')
        self.assertEqual(normalize_protocol(' Cat. '),'Cat.')

    def test_both_original_frozen_sources_are_complete(self):
        self.assertEqual(len(dataset('en')[0]['samples']),16)
        self.assertEqual(len(dataset('zh')[0]['samples']),12)

if __name__=='__main__':unittest.main()
