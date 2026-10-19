#!/usr/bin/env python3
"""Restore mode, owner and group of every file debugfs rdump extracted, from the ext4 image."""
import os, subprocess, sys
img, root = sys.argv[1], sys.argv[2]
dirs = ["/"]
for d, sub, _ in os.walk(root):
    for s in sub:
        p = os.path.join(d, s)
        if not os.path.islink(p):
            dirs.append("/" + os.path.relpath(p, root))
cmds = "".join(f'ls -p "{d}"\n' for d in dirs)
out = subprocess.run(["debugfs", "-f", "-", img], input=cmds, capture_output=True, text=True).stdout
cur, fixed = None, 0
for line in out.splitlines():
    if line.startswith("debugfs: ls -p "):
        cur = line[len('debugfs: ls -p "'):-1]
        continue
    if not line.startswith("/") or cur is None:
        continue
    parts = line.split("/")
    # /inode/mode/uid/gid/name/size/
    if len(parts) < 7:
        continue
    mode, uid, gid, name = int(parts[2], 8), int(parts[3]), int(parts[4]), "/".join(parts[5:-2])
    if name in (".", ".."):
        continue
    p = os.path.join(root, cur.lstrip("/"), name)
    try:
        st = os.lstat(p)
    except FileNotFoundError:
        print("missing", p); continue
    if os.path.islink(p):
        if (st.st_uid, st.st_gid) != (uid, gid):
            os.lchown(p, uid, gid); fixed += 1
        continue
    if (st.st_uid, st.st_gid) != (uid, gid):
        os.chown(p, uid, gid); fixed += 1
    if (st.st_mode & 0o7777) != (mode & 0o7777):
        os.chmod(p, mode & 0o7777); fixed += 1
print("fixed", fixed, "dirs", len(dirs))
