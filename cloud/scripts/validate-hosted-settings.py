#!/usr/bin/env python3

import argparse
import json
from pathlib import Path

from lib.deployment import validate_hosted_settings


def load_secret(path: str) -> dict[str, object]:
    value = json.loads(Path(path).read_text())
    if not isinstance(value, dict):
        raise ValueError(f"{path} must contain a JSON object")
    return value


def main() -> None:
    parser = argparse.ArgumentParser()
    provider = parser.add_mutually_exclusive_group(required=True)
    provider.add_argument("--nodeops")
    provider.add_argument("--coder")
    provider.add_argument("--freestyle")
    parser.add_argument("--worker", required=True)
    args = parser.parse_args()
    provider_name = "freestyle" if args.freestyle else "coder" if args.coder else "nodeops"
    provider_path = args.freestyle or args.coder or args.nodeops
    validate_hosted_settings(
        load_secret(provider_path),
        load_secret(args.worker),
        provider=provider_name,
    )


if __name__ == "__main__":
    main()
