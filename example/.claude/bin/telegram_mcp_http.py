#!/usr/bin/env python3

import asyncio
import json
import os
import sys
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[2]
SETTINGS_LOCAL_JSON = REPO_ROOT / ".claude" / "settings.local.json"
HTTP_HOST = "127.0.0.1"
HTTP_PORT = 43123
HTTP_PATH = "/mcp"


def load_local_env() -> None:
    try:
        data = json.loads(SETTINGS_LOCAL_JSON.read_text())
    except FileNotFoundError:
        print(f"Missing {SETTINGS_LOCAL_JSON}", file=sys.stderr)
        sys.exit(1)
    except Exception as e:
        print(f"Failed to read {SETTINGS_LOCAL_JSON}: {e}", file=sys.stderr)
        sys.exit(1)

    env = data.get("env")
    if not isinstance(env, dict):
        print(f"{SETTINGS_LOCAL_JSON} has no env object", file=sys.stderr)
        sys.exit(1)

    for key in ("TELEGRAM_API_ID", "TELEGRAM_API_HASH", "TELEGRAM_SESSION_STRING"):
        value = env.get(key)
        if not value:
            print(f"{SETTINGS_LOCAL_JSON} is missing {key}", file=sys.stderr)
            sys.exit(1)
        os.environ[key] = str(value)


async def warm_dialog_cache(client) -> None:
    try:
        await client.get_dialogs()
    except Exception as e:
        print(f"Telegram dialog warmup failed: {e}", file=sys.stderr)


async def run() -> None:
    load_local_env()

    import nest_asyncio
    import main as telegram_mcp_main

    telegram_mcp_main._configure_allowed_roots_from_cli(sys.argv[1:])
    nest_asyncio.apply()

    telegram_mcp_main.mcp.settings.host = HTTP_HOST
    telegram_mcp_main.mcp.settings.port = HTTP_PORT
    telegram_mcp_main.mcp.settings.streamable_http_path = HTTP_PATH
    telegram_mcp_main.mcp.settings.log_level = "ERROR"

    print("Starting Telegram client...", file=sys.stderr)
    await telegram_mcp_main.client.start()
    asyncio.create_task(warm_dialog_cache(telegram_mcp_main.client))
    print("Telegram client started. Running MCP HTTP server...", file=sys.stderr)
    await telegram_mcp_main.mcp.run_streamable_http_async()


def main() -> None:
    asyncio.run(run())


if __name__ == "__main__":
    main()
