#!/usr/bin/env python3
"""Provision $HOME/.pi/agent/models.json from an OpenAI-compatible identity.

Key pedantics: the API key is read from the OPENAI_API_KEY environment variable
inside this process (never via argv/process list). Values that freeze into
canary evidence (provider name, base URL, model ids) are explicit inputs and
reproducible; the key is opaque runtime material used only by Pi.
"""
import json
import os
import sys

def fail(message: str) -> None:
    raise SystemExit(f"[rc1-canary-provider-config] ERROR: {message}")

out_path, base_url, provider_key = sys.argv[1:]
if not base_url.startswith("https://"):
    fail("base URL must be a public https endpoint")
for marker in ("{", "}", "$", "`", " ", "\n", "\r", "\t"):
    if marker in base_url:
        fail("base URL contains unsafe/ambiguous characters")
csv_models = os.environ.get("OPENAI_MODELS", "")
api_key = os.environ.get("OPENAI_API_KEY", "")
pi_model = os.environ.get("PI_MODEL", "")
if not api_key:
    fail("OPENAI_API_KEY missing")
if not csv_models or not pi_model or "/" not in pi_model:
    fail("OPENAI_MODELS / PI_MODEL missing or malformed")
prefix, model_id = pi_model.split("/", 1)
if prefix != provider_key:
    fail("PI_MODEL provider 段与 provider_key 不一致")
ids = [m.strip() for m in csv_models.split(",") if m.strip()]
if model_id not in ids:
    fail(f"PI_MODEL 的 model 段未包含在 OPENAI_MODELS 中：{model_id}")
providers = {
    provider_key: {
        "name": provider_key,
        "baseUrl": base_url,
        "api": "openai-completions",
        "apiKey": api_key,
        "models": [
            {"id": m, "name": m, "input": ["text"], "contextWindow": 128000, "maxTokens": 16384}
            for m in ids
        ],
    }
}
os.makedirs(os.path.dirname(out_path), exist_ok=True)
with open(out_path, "w", encoding="utf-8") as handle:
    json.dump({"providers": providers}, handle, ensure_ascii=False)
os.chmod(out_path, 0o600)
print(f"[rc1-canary-provider-config] provider={provider_key} models={len(ids)}")
