#!/usr/bin/env python3
"""Provision $HOME/.pi/agent/models.json from an OpenAI-compatible identity.

Key pedantics: the API key is read from the OPENAI_API_KEY environment variable
inside this process (never via argv/process list). Values that freeze into
canary evidence (provider name, base URL, model ids) are explicit inputs and
reproducible; the key is opaque runtime material used only by Pi.
"""
import json
import hashlib
import os
import sys

def fail(message: str) -> None:
    raise SystemExit(f"[rc1-canary-provider-config] ERROR: {message}")

def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            fail("duplicate profile field")
        result[key] = value
    return result

def model_profile(raw, model_id):
    if len(raw.encode("utf-8")) > 8192:
        fail("model profile too large")
    try:
        profile = json.loads(raw, object_pairs_hook=unique_object)
    except (ValueError, UnicodeError, RecursionError):
        fail("invalid model profile JSON")
    required = {"id", "contextWindow", "maxTokens", "reasoning"}
    if (not isinstance(profile, dict) or not required <= profile.keys()
            or profile.keys() - required - {"compat", "thinkingLevelMap"}
            or profile["id"] != model_id or type(profile["reasoning"]) is not bool):
        fail("model profile fields or identity mismatch")
    if any(type(profile[key]) is not int or not 0 < profile[key] <= 2147483647
           for key in ("contextWindow", "maxTokens")) or profile["maxTokens"] > profile["contextWindow"]:
        fail("invalid model profile limits")
    compat = profile.get("compat", {})
    bool_fields = {"supportsDeveloperRole", "supportsStore", "supportsReasoningEffort", "supportsUsageInStreaming"}
    enums = {"thinkingFormat": {"qwen", "deepseek", "zai", "together", "string-thinking"},
             "maxTokensField": {"max_tokens", "max_completion_tokens"}}
    if not isinstance(compat, dict) or compat.keys() - bool_fields - enums.keys():
        fail("unsupported model compatibility fields")
    for key, value in compat.items():
        if (key in bool_fields and type(value) is not bool) or (key in enums and (not isinstance(value, str) or value not in enums[key])):
            fail("invalid model compatibility value")
    levels = profile.get("thinkingLevelMap", {})
    if not isinstance(levels, dict) or levels.keys() - {"off", "minimal", "low", "medium", "high", "xhigh", "max"}:
        fail("invalid thinking level map")
    if any(value is not None and (not isinstance(value, str) or not value.isascii()
           or not 0 < len(value) <= 64 or not all(c.isalnum() or c in "_-" for c in value)) for value in levels.values()):
        fail("invalid thinking level value")
    return profile

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
raw_profile = os.environ.get("PI_MODEL_PROFILE_JSON", "")
profile = model_profile(raw_profile, model_id) if raw_profile else None
providers = {
    provider_key: {
        "name": provider_key,
        "baseUrl": base_url,
        "api": "openai-completions",
        "apiKey": api_key,
        "models": [
            {"id": m, "name": m, "input": ["text"], **(profile if profile is not None and m == model_id
             else {"contextWindow": 128000, "maxTokens": 16384})}
            for m in ids
        ],
    }
}
os.makedirs(os.path.dirname(out_path), exist_ok=True)
with open(out_path, "w", encoding="utf-8") as handle:
    json.dump({"providers": providers}, handle, ensure_ascii=False)
os.chmod(out_path, 0o600)
print(f"[rc1-canary-provider-config] provider={provider_key} models={len(ids)}")
if profile is None:
    print("[rc1-canary-provider-config] modelProfile=legacy-default contextWindow=128000 maxTokens=16384")
else:
    digest = hashlib.sha256(json.dumps(profile, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode()).hexdigest()
    print(f"[rc1-canary-provider-config] modelProfile=explicit sha256={digest}")
