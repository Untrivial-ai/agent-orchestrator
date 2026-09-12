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
    parser.add_argument("--nodeops", required=True)
    parser.add_argument("--worker", required=True)
    args = parser.parse_args()
    validate_hosted_settings(load_secret(args.nodeops), load_secret(args.worker))


if __name__ == "__main__":
    main()
