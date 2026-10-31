#!/usr/bin/env python3
"""Write manifest.json for the AO Grok Bot skills: version plus sha256 per skill.

Run after any skill change: python3 make-manifest.py <version>   (for example 2026.10.27.1)
"""
import hashlib, json, os, sys

here = os.path.dirname(os.path.abspath(__file__))
if len(sys.argv) != 2:
    sys.exit("usage: make-manifest.py <version>")
skills = []
for sid in sorted(os.listdir(os.path.join(here, "skills"))):
    path = f"skills/{sid}/SKILL.md"
    with open(os.path.join(here, path), "rb") as skill_file:
        data = skill_file.read()
    skills.append({"id": sid, "path": path, "bytes": len(data), "sha256": hashlib.sha256(data).hexdigest()})
with open(os.path.join(here, "manifest.json"), "w") as manifest_file:
    json.dump({"name": "ao-grok-bot", "version": sys.argv[1], "skills": skills},
              manifest_file, indent=1)
print(f"manifest.json: {sys.argv[1]}, {len(skills)} skills")
