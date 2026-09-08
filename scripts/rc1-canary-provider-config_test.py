#!/usr/bin/env python3
"""模型配置生成回归；仅测试密钥，不请求模型服务。"""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("rc1-canary-provider-config.py")
PROFILE = {"id": "qwen-fixture", "contextWindow": 1000000, "maxTokens": 131072,
           "reasoning": True, "compat": {"thinkingFormat": "qwen", "supportsStore": False},
           "thinkingLevelMap": {"low": "low", "high": None}}


class ModelProfileTest(unittest.TestCase):
    def invoke(self, profile=None):
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "models.json"
            env = {"OPENAI_API_KEY": "fixture-secret-never-log", "OPENAI_MODELS": "qwen-fixture,other",
                   "PI_MODEL": "fixture/qwen-fixture"}
            if profile is not None:
                env["PI_MODEL_PROFILE_JSON"] = profile if isinstance(profile, str) else json.dumps(profile)
            result = subprocess.run([sys.executable, "-I", "-B", str(SCRIPT), str(target),
                                     "https://invalid.example/v1", "fixture"], env=env,
                                    capture_output=True, timeout=5, check=False)
            self.assertNotIn(b"fixture-secret-never-log", result.stdout + result.stderr)
            if target.exists():
                self.assertEqual(target.stat().st_mode & 0o777, 0o600)
            return result, json.loads(target.read_text()) if target.exists() else None

    def test_explicit_profile_preserves_selected_metadata_and_legacy_other(self):
        result, value = self.invoke(PROFILE)
        self.assertEqual(result.returncode, 0, result.stderr)
        selected, other = value["providers"]["fixture"]["models"]
        for key, expected in PROFILE.items():
            self.assertEqual(selected[key], expected)
        self.assertEqual(other["maxTokens"], 16384)
        self.assertIn(b"modelProfile=explicit sha256=", result.stdout)
        self.assertNotIn(b"baseUrl", result.stdout)

    def test_legacy_default_remains_explicit_in_diagnostics(self):
        result, value = self.invoke()
        self.assertEqual(result.returncode, 0)
        self.assertIn(b"modelProfile=legacy-default", result.stdout)
        self.assertEqual(value["providers"]["fixture"]["models"][0]["maxTokens"], 16384)

    def test_invalid_profiles_reject_before_writing_without_fallback(self):
        cases = ["{", "[]", "{}", "x"*8193, "["*1500 + "]"*1500, '{"id":"a","id":"b"}',
                 {**PROFILE, "id": "wrong"}, {**PROFILE, "apiKey": "forbidden"},
                 {**PROFILE, "reasoning": 1}, {**PROFILE, "maxTokens": True},
                 {**PROFILE, "maxTokens": 1000001}, {**PROFILE, "contextWindow": 0},
                 {**PROFILE, "compat": {"headers": {}}},
                 {**PROFILE, "compat": {"supportsStore": "false"}},
                 {**PROFILE, "compat": {"thinkingFormat": []}},
                 {**PROFILE, "thinkingLevelMap": {"low": "bad\nvalue"}},
                 {**PROFILE, "thinkingLevelMap": {"secret": "value"}}]
        for case in cases:
            with self.subTest(case=case):
                result, value = self.invoke(case)
                self.assertNotEqual(result.returncode, 0)
                self.assertIsNone(value)
                self.assertNotIn(b"Traceback", result.stderr)


if __name__ == "__main__":
    unittest.main()
