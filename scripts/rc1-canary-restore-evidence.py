#!/usr/bin/env python3
"""Safely extract the run-phase evidence artifact into the checkout root.
usage: rc1-canary-restore-evidence.py <archive.zip>"""
import os
import sys
import zipfile

archive = sys.argv[1]
with zipfile.ZipFile(archive) as zf:
    infos = zf.infolist()
    names = [info.filename for info in infos]
    if len(names) != len(set(names)):
        raise SystemExit("[rc1-canary-restore-evidence] archive member 重名")
    for info in infos:
        destination = os.path.normpath(info.filename)
        if destination.startswith("..") or os.path.isabs(destination) or destination.startswith("~" + os.sep):
            raise SystemExit(f"[rc1-canary-restore-evidence] unsafe member: {info.filename}")
        zf.extract(info, ".")
        if not info.is_dir():
            mode = (info.external_attr >> 16) & 0o777
            if mode:
                os.chmod(destination, mode)
print("[rc1-canary-restore-evidence] extracted")
