from pathlib import Path
import json, subprocess, sys
root=Path(__file__).resolve().parent
if len(sys.argv)!=3:
    raise SystemExit("usage: python3 build.py BEFORE_CHECKOUT AFTER_CHECKOUT")
for label, path in zip(("before","after"),sys.argv[1:]):
    checkout=Path(path).resolve()
    overlay={"Replace":{str(checkout/"backend/internal/service/chat/evidence_repro_test.go"):str(root/"evidence_repro_test.go"),str(checkout/"backend/internal/storage/sqlite/store/issue4657_repro.go"):str(root/"issue4657_repro.go")}}
    overlay_path=root/f"{label}-overlay.json"
    overlay_path.write_text(json.dumps(overlay,indent=2)+"\n")
    subprocess.run(["go","test","-c","-overlay",str(overlay_path),"-o",str(root/f"{label}.test"),"./internal/service/chat"],cwd=checkout/"backend",check=True)
