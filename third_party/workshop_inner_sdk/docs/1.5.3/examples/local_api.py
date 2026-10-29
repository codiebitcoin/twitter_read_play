"""Python 3 standard-library example: pair, chat, or speak supplied text directly.
Usage: python local_api.py http://DEVICE_IP:8081 "Hello" --credentials credentials.json
Do not include /v1/api in the device URL. Enter the pairing code shown on the device on the first run; subsequent runs reuse the saved credentials.
Query this turn by turn_id; a new turn replaces the active conversation.
Use --speech device for a spoken chat reply, or --tts to read the supplied text directly.
"""
import argparse
import hashlib
import hmac
import json
import math
import os
from pathlib import Path
import sys
import time
import uuid
from urllib.error import HTTPError
from urllib.parse import unquote, urlsplit
from urllib.request import Request, urlopen


class APIError(Exception):
    def __init__(self, status, payload):
        super().__init__(f"HTTP {status}: {payload}")
        self.status = status
        self.payload = payload


class LocalAPI:
    def __init__(self, base_url, credentials=None):
        self.base_url = base_url.rstrip("/")
        url = urlsplit(self.base_url)
        if url.scheme not in ("http", "https") or not url.netloc or url.path or url.query or url.fragment:
            raise ValueError("Use a device URL such as http://DEVICE_IP:8081, without an API path or query parameters.")
        self.credentials = credentials or {}

    def headers(self, method, path, body=b"", mode="empty"):
        timestamp = str(int(time.time()))
        nonce = str(uuid.uuid4())
        user_id = self.credentials.get("user_id", "")
        # Use the UTF-8 bytes of the original local_secret string; do not decode it as hexadecimal.
        body_hash = hashlib.sha256(body).hexdigest()
        canonical = "|".join((method.upper(), unquote(urlsplit(path).path), timestamp, nonce, mode, body_hash, user_id))
        signature = hmac.new(self.credentials["local_secret"].encode(), canonical.encode(), hashlib.sha256).hexdigest()
        result = {"X-Local-Sig": signature, "X-Local-Timestamp": timestamp,
                  "X-Local-Nonce": nonce, "X-Local-Payload-Mode": mode,
                  "X-Local-Body-Hash": body_hash, "X-Local-User-Id": user_id}
        if self.credentials.get("local_session_id"):
            result["X-Local-Session-Id"] = self.credentials["local_session_id"]
        return result

    def request(self, method, path, payload=None, *, public=False, timeout=30):
        body = b"" if payload is None else json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode()
        mode = "json" if payload is not None else "empty"
        headers = {} if public else self.headers(method, path, body, mode)
        if payload is not None:
            headers["Content-Type"] = "application/json"
        request = Request(self.base_url + path, data=body if method.upper() != "GET" else None,
                          headers=headers, method=method.upper())
        try:
            with urlopen(request, timeout=timeout) as response:
                raw = response.read()
                return json.loads(raw)
        except HTTPError as error:
            raw = error.read()
            try:
                payload = json.loads(raw)
            except (ValueError, UnicodeDecodeError):
                payload = raw.decode(errors="replace")
            # Preserve structured error details; do not automatically retry writes.
            raise APIError(error.code, payload) from error

    def pair(self, read_code, auth_scope="session"):
        bootstrap = self.request("GET", "/v1/api/auth/bootstrap", public=True)["data"]
        challenge = self.request("POST", "/v1/api/auth/pair/request", public=True)["data"]
        result = self.request("POST", "/v1/api/auth/pair/confirm", {
            "challenge_id": challenge["challenge_id"], "pairing_code": read_code(), "auth_scope": auth_scope
        }, public=True)["data"]
        # REST clients only need these credentials; ignore additional Agent Service fields.
        self.credentials = {key: result[key] for key in ("local_secret", "local_session_id", "local_session_expires_at_ms", "auth_scope") if key in result}
        self.credentials["user_id"] = bootstrap.get("user_id", "")
        return self.credentials

    def speak(self, text, timeout=180):
        slot = self.request("GET", "/v1/api/data/user/slot")["data"]["slot_number"]
        path = "/v1/api/data/user/tts/utterances"
        task = self.request("POST", path, {
            "request_id": str(uuid.uuid4()), "slot": slot, "text": text
        })["data"]
        utterance_id = task["utterance_id"]
        print("Speech submitted. Utterance ID: " + utterance_id, flush=True)
        return self.wait_for_completion(path + "/" + utterance_id, timeout)

    def chat(self, text, timeout=120, speech="none"):
        if speech not in ("none", "device"):
            raise ValueError("Speech output must be none or device.")
        slot = self.request("GET", "/v1/api/data/user/slot")["data"]["slot_number"]
        result = self.request("POST", "/v1/api/data/user/chat/turns", {
            "request_id": str(uuid.uuid4()), "slot": slot,
            "input": {"type": "text", "text": text}, "output": {"speech": speech}
        })["data"]
        turn_id = result["turn_id"]
        print("Chat submitted. Turn ID: " + turn_id, flush=True)
        task = self.wait_for_completion("/v1/api/data/user/chat/turns/" + turn_id, timeout)
        return [task["assistant_text"]]

    def wait_for_completion(self, path, timeout):
        deadline = time.monotonic() + timeout
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise TimeoutError("Task timed out: " + path)
            task = self.request("GET", path, timeout=min(30, remaining))["data"]
            if task["state"] == "completed":
                return task
            if task["state"] in ("failed", "cancelled"):
                error = task.get("error") or {}
                code = error.get("code") or task["state"]
                reason = " (" + error["reason"] + ")" if error.get("reason") else ""
                raise RuntimeError("Task stopped [" + code + "]" + reason + ": " + (error.get("message") or task["state"]))
            time.sleep(min(1, max(0, deadline - time.monotonic())))



def main():
    parser = argparse.ArgumentParser(add_help=False)
    parser.add_argument("device", nargs="?")
    parser.add_argument("text", nargs="?")
    parser.add_argument("--credentials", default="credentials.json")
    parser.add_argument("--auth-scope", default="session")
    parser.add_argument("--timeout", type=float, default=120)
    parser.add_argument("--speech", choices=("none", "device"), default="none")
    parser.add_argument("--tts", action="store_true")
    parser.add_argument("--help", "-h", action="store_true")
    args = parser.parse_args()
    if args.help:
        print(__doc__)
        print("Options: --credentials credentials file; --auth-scope session or permanent; --timeout wait in seconds (default: 120); --speech none or device (default: none); --tts read the supplied text directly on the device without a chat reply.")
        return
    if not args.device or not args.text or not args.text.strip():
        raise ValueError("Provide a device URL and nonempty text. Use --help for usage.")
    if args.auth_scope not in ("session", "permanent") or not math.isfinite(args.timeout) or args.timeout <= 0:
        raise ValueError("Auth scope must be session or permanent; timeout must be a finite positive number.")
    if args.tts and args.speech != "none":
        raise ValueError("Use --tts for direct speech or --speech device for a chat reply.")
    api = LocalAPI(args.device)
    credentials_file = Path(args.credentials)
    try:
        api.credentials = json.loads(credentials_file.read_text(encoding="utf-8"))
    except FileNotFoundError:
        api.pair(lambda: input("Enter the pairing code shown on the device: ").strip(), args.auth_scope)
        # Create the credentials file exclusively with owner-only read/write permissions; never overwrite an existing file.
        descriptor = os.open(credentials_file, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(descriptor, "w", encoding="utf-8") as output:
            json.dump(api.credentials, output, ensure_ascii=False, indent=2)
            output.write("\n")
        print("Pairing succeeded. Credentials saved.")
    if args.tts:
        api.speak(args.text.strip(), args.timeout)
        print("Speech playback completed.")
    else:
        for reply in api.chat(args.text.strip(), args.timeout, args.speech):
            print("Assistant reply: " + reply)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, RuntimeError, APIError) as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
