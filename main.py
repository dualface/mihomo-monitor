import argparse
import asyncio
import json
import logging
import os
import signal
import unicodedata
from dataclasses import dataclass
from typing import Dict, List, Optional

import aiohttp
from dotenv import load_dotenv


@dataclass
class Config:
    controller_url: str
    controller_secret: str
    proxy_group: str
    test_url: str
    delay_timeout_ms: int
    auto_select_diff_ms: int
    monitor_interval_s: int


@dataclass
class ProxyDelay:
    name: str
    delay_ms: int


def load_config() -> Config:
    load_dotenv()
    controller_url = os.getenv("MIHOMO_CONTROLLER_URL", "").strip()
    controller_secret = os.getenv("MIHOMO_CONTROLLER_SECRET", "").strip()
    proxy_group = os.getenv("MIHOMO_PROXY_GROUP", "GLOBAL").strip()

    if not controller_url:
        raise ValueError("MIHOMO_CONTROLLER_URL is required")

    return Config(
        controller_url=controller_url.rstrip("/"),
        controller_secret=controller_secret,
        proxy_group=proxy_group,
        test_url=os.getenv("TEST_URL", "https://google.com").strip(),
        delay_timeout_ms=int(os.getenv("DELAY_TIMEOUT_MS", "3000")),
        auto_select_diff_ms=int(os.getenv("AUTO_SELECT_DIFF_MS", "300")),
        monitor_interval_s=int(os.getenv("MONITOR_INTERVAL_S", "60")),
    )


def auth_headers(secret: str) -> Dict[str, str]:
    if not secret:
        return {}
    return {"Authorization": f"Bearer {secret}"}


async def get_group_delays(
    session: aiohttp.ClientSession,
    config: Config,
) -> List[ProxyDelay]:
    url = f"{config.controller_url}/group/{config.proxy_group}/delay"
    params = {"url": config.test_url, "timeout": str(config.delay_timeout_ms)}
    try:
        async with session.get(
            url,
            headers=auth_headers(config.controller_secret),
            params=params,
        ) as resp:
            resp.raise_for_status()
            payload = await resp.json()
        return parse_group_delays(payload)
    except Exception as exc:
        logging.warning("Group delay check failed: %s", exc)
        return []


def parse_group_delays(payload: Dict[str, object]) -> List[ProxyDelay]:
    delays: List[ProxyDelay] = []

    def _to_int(value: object) -> Optional[int]:
        try:
            return int(value)  # type: ignore[arg-type]
        except (TypeError, ValueError):
            return None

    if "delays" in payload and isinstance(payload["delays"], dict):
        for name, delay in payload["delays"].items():
            delay_ms = _to_int(delay)
            if delay_ms is None:
                continue
            if delay_ms >= 0:
                delays.append(ProxyDelay(name=name, delay_ms=delay_ms))
        return delays

    if all(isinstance(key, str) for key in payload.keys()):
        for name, delay in payload.items():
            delay_ms = _to_int(delay)
            if delay_ms is None:
                continue
            if delay_ms >= 0:
                delays.append(ProxyDelay(name=name, delay_ms=delay_ms))
        if delays:
            return delays

    if "proxies" in payload and isinstance(payload["proxies"], list):
        for item in payload["proxies"]:
            if not isinstance(item, dict):
                continue
            name = item.get("name")
            delay = item.get("delay")
            if not isinstance(name, str):
                continue
            delay_ms = _to_int(delay)
            if delay_ms is None:
                continue
            if delay_ms >= 0:
                delays.append(ProxyDelay(name=name, delay_ms=delay_ms))
        return delays

    if "name" in payload and "delay" in payload:
        name = payload.get("name")
        delay = payload.get("delay")
        if isinstance(name, str):
            delay_ms = _to_int(delay)
            if delay_ms is not None and delay_ms >= 0:
                return [ProxyDelay(name=name, delay_ms=delay_ms)]

    logging.warning("Unexpected delay payload shape: %s", payload)
    return []


def sanitize_name(name: str) -> str:
    safe_punct = set(" .-_()/[]:")
    cleaned: List[str] = []
    for ch in name:
        if ch in safe_punct:
            cleaned.append(ch)
            continue
        category = unicodedata.category(ch)
        if category and category[0] in ("L", "N", "M"):
            cleaned.append(ch)
    return "".join(cleaned).strip()


async def get_current_proxy(
    session: aiohttp.ClientSession,
    config: Config,
) -> Optional[str]:
    url = f"{config.controller_url}/proxies/{config.proxy_group}"
    async with session.get(url, headers=auth_headers(config.controller_secret)) as resp:
        resp.raise_for_status()
        payload = await resp.json()
    return payload.get("now")


async def switch_proxy(
    session: aiohttp.ClientSession,
    config: Config,
    candidate: ProxyDelay,
) -> None:
    url = f"{config.controller_url}/proxies/{config.proxy_group}"
    payload = {"name": candidate.name}
    async with session.put(
        url,
        headers=auth_headers(config.controller_secret),
        json=payload,
    ) as resp:
        resp.raise_for_status()


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Mihomo proxy monitor")
    action_group = parser.add_mutually_exclusive_group(required=True)
    action_group.add_argument(
        "--print-delays",
        action="store_true",
        help="Print proxy delays for group and exit",
    )
    parser.add_argument(
        "--json",
        action="store_true",
        help="Use JSON output when printing delays",
    )
    action_group.add_argument(
        "--print-current",
        action="store_true",
        help="Print current proxy delay and exit",
    )
    action_group.add_argument(
        "--auto-select",
        action="store_true",
        help="Auto select faster proxy and exit",
    )
    action_group.add_argument(
        "--monitor",
        action="store_true",
        help="Run monitor loop with auto selection",
    )
    return parser


async def print_delays_once(
    session: aiohttp.ClientSession,
    config: Config,
    json_output: bool,
) -> None:
    delays = await get_group_delays(session, config)
    delays.sort(key=lambda item: item.delay_ms)
    delays = delays[:10]
    if not delays:
        if json_output:
            print("[]")
        else:
            print("No delay data returned")
        return
    if json_output:
        payload = [{"name": item.name, "delay_ms": item.delay_ms} for item in delays]
        print(json.dumps(payload, ensure_ascii=True))
        return
    for item in delays:
        safe_name = sanitize_name(item.name)
        print(f"{item.delay_ms}ms\t{safe_name}")


async def print_current_delay_once(
    session: aiohttp.ClientSession,
    config: Config,
    json_output: bool,
) -> None:
    current = await get_current_proxy(session, config)
    if not current:
        if json_output:
            print(json.dumps({"error": "current proxy not found"}, ensure_ascii=True))
        else:
            print("Current proxy not found")
        return

    delays = await get_group_delays(session, config)
    delay_map = {item.name: item.delay_ms for item in delays}
    delay_ms = delay_map.get(current)
    if delay_ms is None:
        if json_output:
            print(
                json.dumps(
                    {"name": current, "delay_ms": None},
                    ensure_ascii=True,
                )
            )
        else:
            safe_name = sanitize_name(current)
            print(f"delay unavailable\t{safe_name}")
        return

    if json_output:
        print(json.dumps({"name": current, "delay_ms": delay_ms}, ensure_ascii=True))
        return
    safe_name = sanitize_name(current)
    print(f"{delay_ms}ms\t{safe_name}")


async def auto_select_once(
    session: aiohttp.ClientSession,
    config: Config,
    json_output: bool,
) -> None:
    current = await get_current_proxy(session, config)
    delays = await get_group_delays(session, config)
    delays.sort(key=lambda item: item.delay_ms)

    if not delays:
        if json_output:
            print(json.dumps({"error": "no delay data"}, ensure_ascii=True))
        else:
            print("No delay data returned")
        return

    best = delays[0]
    delay_map = {item.name: item.delay_ms for item in delays}
    current_delay = delay_map.get(current) if current else None

    delays = delays[:10]

    should_switch = False
    reason = ""
    if current is None:
        should_switch = False
        reason = "current proxy not found, keeping best as target"
    elif current_delay is None:
        should_switch = False
        reason = "current delay unavailable, keeping current"
    elif (
        best.name != current
        and current_delay > 1000
        and (current_delay - best.delay_ms) > config.auto_select_diff_ms
    ):
        should_switch = True
        reason = "current slower than best and delay > 1000ms"
    elif current is not None and current_delay is not None and current_delay <= 1000:
        should_switch = False
        reason = "current delay <= 1000ms, keeping current"

    if should_switch and best.name != current:
        await switch_proxy(session, config, best)
        result = {
            "action": "switched",
            "from": current,
            "to": best.name,
            "from_delay_ms": current_delay,
            "to_delay_ms": best.delay_ms,
            "reason": reason,
        }
    else:
        result = {
            "action": "kept",
            "current": current,
            "delay_ms": current_delay,
            "best": best.name,
            "best_delay_ms": best.delay_ms,
        }

    if json_output:
        print(json.dumps(result, ensure_ascii=True))
        return
    if result["action"] == "switched":
        from_name = sanitize_name(current or "")
        to_name = sanitize_name(best.name)
        print(
            f"switched\t{from_name}\t{current_delay}ms -> {best.delay_ms}ms\t{to_name}"
        )
        return
    keep_name = sanitize_name(current or "")
    print(f"kept\t{current_delay}ms\t{keep_name}")


async def monitor_loop(
    session: aiohttp.ClientSession,
    config: Config,
    json_output: bool,
) -> None:
    stop_event = asyncio.Event()

    def _stop(*_args: object) -> None:
        logging.info("Shutdown signal received")
        stop_event.set()

    loop = asyncio.get_running_loop()
    for sig in (signal.SIGINT, signal.SIGTERM):
        loop.add_signal_handler(sig, _stop)

    while not stop_event.is_set():
        await auto_select_once(session, config, json_output)
        try:
            await asyncio.wait_for(stop_event.wait(), timeout=config.monitor_interval_s)
        except asyncio.TimeoutError:
            continue


async def main() -> None:
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(message)s",
    )
    parser = build_parser()
    args = parser.parse_args()
    config = load_config()

    async with aiohttp.ClientSession() as session:
        if args.print_delays:
            await print_delays_once(session, config, args.json)
            return
        if args.print_current:
            await print_current_delay_once(session, config, args.json)
            return
        if args.auto_select:
            await auto_select_once(session, config, args.json)
            return
        if args.monitor:
            await monitor_loop(session, config, args.json)
            return


if __name__ == "__main__":
    asyncio.run(main())
