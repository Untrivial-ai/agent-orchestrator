#!/usr/bin/env python3
"""Select a <=15 second clip from an attempt's retained recording."""

import argparse
import hashlib
import json
import math
from pathlib import Path
import subprocess
import uuid


def duration(path):
    result = subprocess.run(
        ["ffprobe", "-v", "error", "-show_entries", "format=duration",
         "-of", "default=noprint_wrappers=1:nokey=1", str(path)],
        check=True, capture_output=True, text=True)
    return float(result.stdout.strip())


def digest(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def export(attempt, recording, start, seconds, label):
    if not math.isfinite(start) or not math.isfinite(seconds) or start < 0 or not 0 < seconds <= 15:
        raise ValueError("Select a nonnegative start and a duration of 0 < seconds <= 15")
    attempt = attempt.resolve(strict=True)
    recording = recording.resolve(strict=True)
    source = recording.relative_to(attempt)
    if start + seconds > duration(recording):
        raise ValueError("Selected segment exceeds the original recording")
    clips = attempt / "clips"
    clips.mkdir(exist_ok=True)
    if clips.resolve() != clips:
        raise ValueError("Clip directory must not contain symlinks")
    output = clips / f"{label}-{uuid.uuid4()}.mp4"
    subprocess.run(
        ["ffmpeg", "-hide_banner", "-loglevel", "error", "-nostdin", "-n",
         "-ss", str(start), "-i", str(recording),
         "-t", str(seconds), "-an", "-c:v", "libx264", "-pix_fmt", "yuv420p",
         "-movflags", "+faststart", str(output)], check=True)
    export_seconds = duration(output)
    if not 0 < export_seconds <= 15:
        raise ValueError("Export duration is outside the 15 second limit; do not publish it")
    subprocess.run(["ffmpeg", "-v", "error", "-i", str(output), "-f", "null", "-"], check=True)
    checksum = digest(output)
    output.with_suffix(".sha256").write_text(f"{checksum}  {output.name}\n")
    output.with_suffix(".selection.json").write_text(json.dumps({
        "source": str(source), "sourceSHA256": digest(recording),
        "startSeconds": start, "durationSeconds": seconds,
        "exportDurationSeconds": export_seconds, "label": label, "exportSHA256": checksum,
    }, indent=2) + "\n")
    return output


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--attempt-dir", type=Path, required=True)
    parser.add_argument("--recording", type=Path, required=True)
    parser.add_argument("--start", type=float, required=True, help="Seconds from recording start")
    parser.add_argument("--duration", type=float, required=True, help="At most 15 seconds")
    parser.add_argument("--label", choices=("before", "after"), required=True)
    args = parser.parse_args()
    print(export(args.attempt_dir, args.recording, args.start, args.duration, args.label))


if __name__ == "__main__":
    main()
