#!/usr/bin/env python3
# /// script
# requires-python = ">=3.10"
# dependencies = [
#     "httpx>=0.27",
#     "openai>=1.50",
#     "anthropic>=0.40",
# ]
# ///
"""
End-to-end test script for cli2api.

USAGE
    python3 scripts/test_e2e.py                # cold smoke only — no upstream calls
    python3 scripts/test_e2e.py --live         # also run live tests against real mulerun
    python3 scripts/test_e2e.py --live --skip-video   # live but skip the long $$$ video tests
    python3 scripts/test_e2e.py --keep-server  # leave the server up after tests for manual poking

ENV
    MULERUN_TOKEN     required when --live (or pre-existing ~/.mulerun/ OAuth cache)
    CLI2API_BIN       optional, override the binary path (default: ./bin/cli2api)

EXIT CODES
    0 = all green
    1 = some test failed
    2 = setup error (server didn't start, etc.)
"""
from __future__ import annotations

import argparse
import contextlib
import json
import os
import random
import shutil
import signal
import socket
import sqlite3
import subprocess
import sys
import tempfile
import time
import traceback
from dataclasses import dataclass, field
from pathlib import Path
from typing import Callable

import httpx
from anthropic import Anthropic
from openai import OpenAI

REPO = Path(__file__).resolve().parent.parent
BIN = Path(os.environ.get("CLI2API_BIN", REPO / "bin" / "cli2api"))
INBOUND_KEY = "sk-test-" + str(random.randint(10_000, 99_999))


# ---------------------------------------------------------------- result table


@dataclass
class Result:
    name: str
    passed: bool
    elapsed_ms: int
    detail: str = ""
    skipped: bool = False


@dataclass
class Runner:
    results: list[Result] = field(default_factory=list)

    def run(self, name: str, fn: Callable[[], None], *, skip_reason: str | None = None) -> None:
        if skip_reason:
            print(f"  ⊘ {name} — {skip_reason}")
            self.results.append(Result(name, True, 0, skip_reason, skipped=True))
            return
        t0 = time.monotonic()
        try:
            fn()
        except AssertionError as e:
            elapsed = int((time.monotonic() - t0) * 1000)
            print(f"  ✗ {name} ({elapsed} ms)")
            print(f"      {e}")
            self.results.append(Result(name, False, elapsed, str(e)))
            return
        except Exception as e:  # noqa: BLE001
            elapsed = int((time.monotonic() - t0) * 1000)
            print(f"  ✗ {name} ({elapsed} ms)  [exception]")
            print(f"      {type(e).__name__}: {e}")
            self.results.append(Result(name, False, elapsed, f"{type(e).__name__}: {e}"))
            return
        elapsed = int((time.monotonic() - t0) * 1000)
        print(f"  ✓ {name} ({elapsed} ms)")
        self.results.append(Result(name, True, elapsed))

    def summary(self) -> int:
        passed = sum(1 for r in self.results if r.passed and not r.skipped)
        failed = sum(1 for r in self.results if not r.passed)
        skipped = sum(1 for r in self.results if r.skipped)
        total_ms = sum(r.elapsed_ms for r in self.results)
        print()
        print("─" * 60)
        print(
            f"  {passed} passed · {failed} failed · {skipped} skipped"
            f" · {total_ms} ms total"
        )
        if failed:
            print()
            print("FAILED:")
            for r in self.results:
                if not r.passed:
                    print(f"  • {r.name}: {r.detail}")
        return 1 if failed else 0


# ---------------------------------------------------------------- server boot


def free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


@contextlib.contextmanager
def cli2api_server(*, env_overrides: dict[str, str], log_path: Path, keep: bool = False):
    if not BIN.exists():
        raise SystemExit(f"binary not found: {BIN} — run `make build` first")

    env = os.environ.copy()
    port = str(free_port())
    env.update(
        {
            "CLI2API_PORT": port,
            "CLI2API_LOG_LEVEL": "info",
            **env_overrides,
        }
    )
    base_url = f"http://127.0.0.1:{port}"
    log_file = log_path.open("w", buffering=1)
    proc = subprocess.Popen(
        [str(BIN)],
        env=env,
        stdout=log_file,
        stderr=subprocess.STDOUT,
    )
    try:
        # Wait for /healthz
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            if proc.poll() is not None:
                log_file.close()
                raise SystemExit(
                    f"server died during startup; tail of log:\n"
                    + log_path.read_text()[-2000:]
                )
            try:
                r = httpx.get(f"{base_url}/healthz", timeout=0.3)
                if r.status_code == 200:
                    break
            except httpx.HTTPError:
                pass
            time.sleep(0.1)
        else:
            raise SystemExit("server did not become healthy within 5 s")
        yield base_url, port
    finally:
        if keep:
            print(f"\n[--keep-server] cli2api is still running at {base_url}  (pid {proc.pid})")
            print(f"               logs: {log_path}")
            print(f"               kill with: kill {proc.pid}")
            log_file.close()
            return
        proc.send_signal(signal.SIGTERM)
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()
        log_file.close()


# ---------------------------------------------------------------- cold tests


def cold_smoke(runner: Runner, base: str, db_path: Path) -> None:
    """All tests below must work WITHOUT a valid upstream token."""
    print()
    print("== Cold smoke (no upstream calls) ==")

    def healthz():
        r = httpx.get(f"{base}/healthz", timeout=2)
        assert r.status_code == 200, f"status {r.status_code}"
        assert r.text == "ok", f"body {r.text!r}"

    runner.run("healthz returns ok", healthz)

    def models_list_shape():
        r = httpx.get(f"{base}/v1/models", headers={"Authorization": f"Bearer {INBOUND_KEY}"}, timeout=2)
        assert r.status_code == 200
        body = r.json()
        assert body["object"] == "list"
        ids = {m["id"] for m in body["data"]}
        for required in ("gpt-image-2", "nano-banana", "sora-2", "speech-2.8-hd", "music-2.5", "veo"):
            assert required in ids, f"missing model {required}"
        # 70+ registered (61 generation + ~9 chat dummies)
        assert len(ids) >= 60, f"only {len(ids)} models"

    runner.run("/v1/models lists 60+ models including key ones", models_list_shape)

    def models_edit_action_present():
        r = httpx.get(f"{base}/v1/models", headers={"Authorization": f"Bearer {INBOUND_KEY}"}, timeout=2).json()
        ids = {m["id"] for m in r["data"]}
        for required in ("gpt-image-2-edit", "nano-banana-edit", "wan2.5-i2i-preview-edit"):
            assert required in ids, f"missing edit model {required}"

    runner.run("edit-action models are listed", models_edit_action_present)

    def auth_rejected_when_missing():
        # Force the protected key
        r = httpx.post(
            f"{base}/v1/chat/completions",
            json={"model": "gpt-5", "messages": [{"role": "user", "content": "hi"}]},
            timeout=2,
        )
        assert r.status_code == 401, f"want 401, got {r.status_code} body={r.text[:200]}"
        assert "missing API key" in r.text or "invalid_api_key" in r.text

    runner.run("inbound auth rejects when key missing", auth_rejected_when_missing)

    def auth_rejected_when_wrong():
        r = httpx.post(
            f"{base}/v1/chat/completions",
            headers={"Authorization": "Bearer wrong-key"},
            json={"model": "gpt-5", "messages": [{"role": "user", "content": "hi"}]},
            timeout=2,
        )
        assert r.status_code == 401, f"want 401, got {r.status_code}"

    runner.run("inbound auth rejects wrong key", auth_rejected_when_wrong)

    def edit_only_model_on_generations_404():
        # Codex P2 regression guard
        r = httpx.post(
            f"{base}/v1/images/generations",
            headers={"Authorization": f"Bearer {INBOUND_KEY}"},
            json={"model": "gpt-image-2-edit", "prompt": "x"},
            timeout=3,
        )
        assert r.status_code == 404, f"want 404 (was the panic fix), got {r.status_code}"
        assert "model_not_found" in r.text

    runner.run("edit-only model on /images/generations returns 404 (no panic)", edit_only_model_on_generations_404)

    def unknown_video_model_404():
        r = httpx.post(
            f"{base}/v1/videos",
            headers={"Authorization": f"Bearer {INBOUND_KEY}"},
            json={"model": "fake-9000", "prompt": "x"},
            timeout=3,
        )
        assert r.status_code == 404, f"want 404, got {r.status_code}"

    runner.run("unknown video model returns 404", unknown_video_model_404)

    def unknown_job_404():
        r = httpx.get(
            f"{base}/v1/videos/video_does_not_exist",
            headers={"Authorization": f"Bearer {INBOUND_KEY}"},
            timeout=3,
        )
        assert r.status_code == 404

    runner.run("unknown video job ID returns 404", unknown_job_404)

    def missing_prompt_400():
        r = httpx.post(
            f"{base}/v1/images/generations",
            headers={"Authorization": f"Bearer {INBOUND_KEY}"},
            json={"model": "wan2.6-t2i"},
            timeout=3,
        )
        assert r.status_code == 400
        assert "prompt is required" in r.text

    runner.run("missing prompt returns 400", missing_prompt_400)

    def request_body_too_large():
        big = "x" * (70 * 1024 * 1024)
        r = httpx.post(
            f"{base}/v1/videos",
            headers={"Authorization": f"Bearer {INBOUND_KEY}"},
            content=json.dumps({"model": "sora-2", "prompt": big}),
            timeout=10,
        )
        # chi RequestSize cap is 64 MB; we wrap the read error as 400
        assert r.status_code == 400, f"got {r.status_code}"
        assert "too large" in r.text.lower()

    runner.run("70 MB request body is rejected (64 MB cap)", request_body_too_large)

    def cors_preflight():
        r = httpx.options(
            f"{base}/v1/chat/completions",
            headers={
                "Origin": "https://app.example",
                "Access-Control-Request-Method": "POST",
                "Access-Control-Request-Headers": "authorization,content-type",
            },
            timeout=2,
        )
        assert r.status_code == 204, f"got {r.status_code}"
        assert "*" in (r.headers.get("Access-Control-Allow-Origin") or "")

    runner.run("CORS preflight returns 204 with allow-origin", cors_preflight)

    def reaper_clears_expired():
        # Insert a synthetic past-due job directly into the libsql file
        # (the cli2api process owns it; pure-sqlite reader is fine while it
        # holds the WAL.)
        conn = sqlite3.connect(str(db_path), timeout=5)
        try:
            conn.execute(
                "INSERT OR REPLACE INTO jobs (local_id, kind, model, vendor_path, vendor_task_id, created_at, status, result_urls, expires_at) "
                "VALUES (?,?,?,?,?,?,?,?,?)",
                ("test_e2e_expired", "video", "sora-2", "/x", "vt", 1, "completed", "[]", 1),
            )
            conn.commit()
        finally:
            conn.close()

        # Reaper interval was set to 1s for this test; wait up to 5s
        for _ in range(50):
            time.sleep(0.1)
            conn = sqlite3.connect(str(db_path), timeout=5)
            try:
                cur = conn.execute(
                    "SELECT COUNT(*) FROM jobs WHERE local_id='test_e2e_expired'"
                ).fetchone()
            finally:
                conn.close()
            if cur[0] == 0:
                return
        raise AssertionError("reaper did not delete expired job within 5 s")

    runner.run("reaper deletes expired jobs", reaper_clears_expired)


# ---------------------------------------------------------------- live tests


def live_tests(runner: Runner, base: str, *, skip_video: bool, skip_music: bool, mulerun_token: str | None) -> None:
    print()
    print("== Live tests (real upstream calls — incurs cost) ==")
    if skip_video:
        print("   [--skip-video active — sora/veo/kling not run]")

    openai_client = OpenAI(api_key=INBOUND_KEY, base_url=f"{base}/v1")
    anthropic_client = Anthropic(api_key=INBOUND_KEY, base_url=base)

    # Mulerun has TWO independent auth planes:
    #   - studio token (muk-xxx): authorizes /vendors/* image / video / audio
    #   - LLM gateway token (CRS_OAI_KEY style): authorizes /v1/chat/completions
    #     and /v1/messages, served by a different host.
    # If the user provided a studio-only token, skip chat/messages/responses
    # tests with a clear reason. They'll fail with 401 otherwise, which looks
    # like a cli2api bug but isn't.
    studio_only = bool(mulerun_token and mulerun_token.startswith("muk-"))
    chat_skip = "studio token (muk-) cannot reach LLM plane — skipping chat/messages/responses" if studio_only else None

    def chat_completion():
        r = openai_client.chat.completions.create(
            model="gpt-5",
            messages=[{"role": "user", "content": "Say the word ONE and nothing else."}],
            max_tokens=10,
        )
        text = r.choices[0].message.content or ""
        assert text.strip(), f"empty response: {r}"

    runner.run("openai.chat.completions: gpt-5", chat_completion, skip_reason=chat_skip)

    def chat_streaming():
        chunks = []
        stream = openai_client.chat.completions.create(
            model="gpt-5",
            messages=[{"role": "user", "content": "count: 1 2 3"}],
            stream=True,
            max_tokens=20,
        )
        for ch in stream:
            if ch.choices and ch.choices[0].delta.content:
                chunks.append(ch.choices[0].delta.content)
        assert len(chunks) >= 1, "no streamed chunks"

    runner.run("openai chat streaming yields chunks", chat_streaming, skip_reason=chat_skip)

    def anthropic_messages():
        r = anthropic_client.messages.create(
            model="claude-sonnet-4-6",
            max_tokens=20,
            messages=[{"role": "user", "content": "Say the word ONE and nothing else."}],
        )
        text = "".join(b.text for b in r.content if hasattr(b, "text"))
        assert text.strip(), f"empty response: {r}"

    runner.run("anthropic.messages: claude-sonnet-4-6", anthropic_messages, skip_reason=chat_skip)

    def responses_endpoint():
        r = httpx.post(
            f"{base}/v1/responses",
            headers={"Authorization": f"Bearer {INBOUND_KEY}"},
            json={"model": "gpt-5", "input": "Say HI in one word.", "max_output_tokens": 10},
            timeout=60,
        )
        assert r.status_code == 200, f"got {r.status_code} body={r.text[:300]}"
        body = r.json()
        # /v1/responses returns either output_text or output[].content
        text = body.get("output_text") or json.dumps(body.get("output", []))
        assert text, "empty response"

    runner.run("/v1/responses returns content", responses_endpoint, skip_reason=chat_skip)

    def image_generation_wan():
        r = openai_client.images.generate(
            model="wan2.6-t2i",
            prompt="a single red dot on white background, minimal",
            size="1024x1024",
        )
        url = r.data[0].url
        assert url and url.startswith("http"), f"bad url: {url}"

    runner.run("image generation: wan2.6-t2i", image_generation_wan)

    def image_generation_gpt():
        r = openai_client.images.generate(
            model="gpt-image-2",
            prompt="a single red dot on white background, minimal",
            size="1024x1024",
            n=1,
        )
        assert r.data[0].url, "missing url"

    runner.run("image generation: gpt-image-2", image_generation_gpt)

    def image_edit_nano_banana():
        # Use an existing tiny test image URL — we don't need much; mulerun
        # validates content type, not aesthetics. Use the wan2.6 output.
        seed = openai_client.images.generate(
            model="wan2.6-t2i",
            prompt="solid blue square 1024x1024",
            size="1024x1024",
        )
        src_url = seed.data[0].url
        # OpenAI SDK's images.edit() requires file-like objects, not URLs;
        # mulerun supports both URL and base64 in the JSON body, so test the
        # JSON path with raw httpx.
        r = httpx.post(
            f"{base}/v1/images/edits",
            headers={"Authorization": f"Bearer {INBOUND_KEY}", "Content-Type": "application/json"},
            json={
                "model": "nano-banana-edit",
                "prompt": "add a small white circle in the center",
                "images": [src_url],
            },
            timeout=300,
        )
        assert r.status_code == 200, f"status {r.status_code} body={r.text[:300]}"
        body = r.json()
        assert body["data"] and body["data"][0]["url"], "edit returned no url"

    runner.run("image edit: nano-banana-edit (JSON path with URL input)", image_edit_nano_banana)

    def speech_turbo():
        out = openai_client.audio.speech.create(
            model="speech-2.8-turbo",
            voice="Charming_Lady",
            input="Hello from cli2api end to end test.",
            response_format="mp3",
        )
        data = out.read()
        assert len(data) > 1000, f"audio body too small: {len(data)} bytes"
        # MP3 magic: ID3 or 0xFF 0xFB
        assert data[:3] == b"ID3" or data[0] == 0xFF, f"not mp3: {data[:8].hex()}"

    runner.run("speech: speech-2.8-turbo returns mp3 bytes", speech_turbo)

    if not skip_video:
        # Pick the cheapest video: kling-v2.6 or seedance-2.0-fast 5-sec
        def video_seedance_fast():
            r = httpx.post(
                f"{base}/v1/videos",
                headers={"Authorization": f"Bearer {INBOUND_KEY}"},
                json={
                    "model": "seedance-2.0-fast-text-to-video",
                    "prompt": "a red ball bouncing on a table, simple",
                    "resolution": "480p",
                    "duration": 4,
                    "generate_audio": False,
                },
                timeout=10,
            )
            assert r.status_code == 202, f"submit failed: {r.status_code} {r.text}"
            job_id = r.json()["id"]
            assert job_id.startswith("video_")

            # Mulerouter CLI's default max-wait is 15 min; some "fast" models
            # are not actually fast in practice (~5+ min observed for
            # seedance-2.0-fast). Match the CLI's tolerance.
            deadline = time.monotonic() + 900  # 15 min hard cap
            while time.monotonic() < deadline:
                time.sleep(8)
                pr = httpx.get(
                    f"{base}/v1/videos/{job_id}",
                    headers={"Authorization": f"Bearer {INBOUND_KEY}"},
                    timeout=10,
                )
                if pr.status_code != 200:
                    raise AssertionError(f"poll failed: HTTP {pr.status_code} body={pr.text[:300]}")
                poll = pr.json()
                if poll.get("status") in ("completed", "failed"):
                    break
            else:
                raise AssertionError("video did not finish within 15 min")
            assert poll["status"] == "completed", f"final status {poll['status']}: {poll.get('error')}"
            assert poll["videos"] and poll["videos"][0].startswith("http")

        runner.run("video: seedance-2.0-fast (submit + poll to completion)", video_seedance_fast)

        def video_sora_2():
            r = httpx.post(
                f"{base}/v1/videos",
                headers={"Authorization": f"Bearer {INBOUND_KEY}"},
                json={"model": "sora-2", "prompt": "a quiet sunrise", "seconds": "4"},
                timeout=10,
            )
            assert r.status_code == 202
            job_id = r.json()["id"]
            deadline = time.monotonic() + 600
            while time.monotonic() < deadline:
                time.sleep(10)
                pr = httpx.get(
                    f"{base}/v1/videos/{job_id}",
                    headers={"Authorization": f"Bearer {INBOUND_KEY}"},
                    timeout=10,
                )
                if pr.status_code != 200:
                    raise AssertionError(f"poll failed: HTTP {pr.status_code} body={pr.text[:300]}")
                poll = pr.json()
                if poll.get("status") in ("completed", "failed"):
                    break
            else:
                raise AssertionError("sora-2 did not finish within 10 min")
            assert poll["status"] == "completed", f"final: {poll}"

        runner.run("video: sora-2 (short, ~$0.50)", video_sora_2)

    if not skip_music:
        def music_25():
            r = httpx.post(
                f"{base}/v1/audio/music",
                headers={"Authorization": f"Bearer {INBOUND_KEY}"},
                json={
                    "model": "music-2.5",
                    "prompt": "calm piano",
                    "lyrics_prompt": "[verse]\nsoft and slow",
                },
                timeout=10,
            )
            assert r.status_code == 202, f"submit failed: {r.status_code} {r.text}"
            job_id = r.json()["id"]
            # Music can take longer than its nominal duration; observed up to
            # 5+ min on busy days. Match the video timeout.
            deadline = time.monotonic() + 900
            while time.monotonic() < deadline:
                time.sleep(8)
                pr = httpx.get(
                    f"{base}/v1/audio/music/{job_id}",
                    headers={"Authorization": f"Bearer {INBOUND_KEY}"},
                    timeout=10,
                )
                # Polling can return 502 wrapping an upstream error;
                # surface it instead of crashing on KeyError.
                if pr.status_code != 200:
                    raise AssertionError(
                        f"poll failed: HTTP {pr.status_code} body={pr.text[:300]}"
                    )
                poll = pr.json()
                if "status" not in poll:
                    raise AssertionError(f"unexpected poll body: {poll}")
                if poll["status"] in ("completed", "failed"):
                    break
            else:
                raise AssertionError("music did not finish within 15 min")
            assert poll["status"] == "completed", f"final: {poll}"
            assert poll["audios"] and poll["audios"][0].startswith("http")

        runner.run("music: music-2.5 (submit + poll)", music_25)


# ---------------------------------------------------------------- main


def main() -> int:
    ap = argparse.ArgumentParser(description="cli2api end-to-end test runner")
    ap.add_argument("--live", action="store_true", help="run live tests against real mulerun (incurs cost)")
    ap.add_argument("--skip-video", action="store_true", help="with --live, skip video tests (the expensive ones)")
    ap.add_argument("--skip-music", action="store_true", help="with --live, skip music tests")
    ap.add_argument("--keep-server", action="store_true", help="leave the cli2api process running after tests")
    args = ap.parse_args()

    if not BIN.exists():
        print(f"binary not found: {BIN}", file=sys.stderr)
        print("run: make build", file=sys.stderr)
        return 2

    tmpdir = Path(tempfile.mkdtemp(prefix="cli2api-e2e-"))
    log_path = tmpdir / "server.log"
    db_path = tmpdir / "jobs.db"
    print(f"[fixtures] tmpdir = {tmpdir}")
    print(f"[fixtures] inbound key = {INBOUND_KEY}")
    print(f"[fixtures] binary = {BIN}")

    env_overrides = {
        "CLI2API_API_KEYS": INBOUND_KEY,  # force inbound auth
        "CLI2API_JOBSTORE_DSN": f"file:{db_path}",
        # 24h retention — way longer than any video / music run, so the reaper
        # doesn't accidentally sweep an in-flight live test. The reaper test
        # forces deletion by inserting a synthetic row with expires_at=1.
        "CLI2API_JOB_RETENTION": "24h",
        "CLI2API_REAPER_INTERVAL": "1s",
    }

    # For cold-smoke only, the server still needs A token to start (it can't
    # know we won't call upstream). Inject a dummy if the user hasn't set one
    # AND there's no ~/.mulerun/ cache. --live always requires a real token.
    if not os.environ.get("MULERUN_TOKEN"):
        home_dir = Path.home() / ".mulerun"
        has_oauth_cache = any((home_dir / f).exists() for f in ("auth.json", "credentials.json", "token.json"))
        if args.live and not has_oauth_cache:
            print("ERROR: --live requires MULERUN_TOKEN or `mulerun login` to have been run", file=sys.stderr)
            return 2
        if not has_oauth_cache:
            env_overrides["MULERUN_TOKEN"] = "cold-smoke-dummy-token-not-for-upstream-calls"

    runner = Runner()
    try:
        with cli2api_server(env_overrides=env_overrides, log_path=log_path, keep=args.keep_server) as (base, port):
            print(f"[server] up at {base}  (log: {log_path})")
            cold_smoke(runner, base, db_path)
            if args.live:
                live_tests(
                    runner, base,
                    skip_video=args.skip_video,
                    skip_music=args.skip_music,
                    mulerun_token=os.environ.get("MULERUN_TOKEN"),
                )
    except SystemExit as e:
        print(f"\n[setup] {e}", file=sys.stderr)
        if log_path.exists():
            print("\n[server log tail]\n" + log_path.read_text()[-2000:], file=sys.stderr)
        return 2
    except Exception as e:  # noqa: BLE001
        print(f"\n[setup] unexpected error: {e}", file=sys.stderr)
        traceback.print_exc()
        return 2

    code = runner.summary()
    if not args.keep_server:
        shutil.rmtree(tmpdir, ignore_errors=True)
    else:
        print(f"\n[--keep-server] fixtures preserved at {tmpdir}")
    return code


if __name__ == "__main__":
    sys.exit(main())
