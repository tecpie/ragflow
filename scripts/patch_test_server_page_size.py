#!/usr/bin/env python3
"""One-off: set rest_api_max_page_size on test server service_conf template."""

from pathlib import Path

TEMPLATE = Path("/data/docker/ragflow/service_conf.yaml.template")
text = TEMPLATE.read_text(encoding="utf-8")
key = "rest_api_max_page_size"

if key in text:
    lines = []
    for line in text.splitlines():
        if line.strip().startswith(f"{key}:"):
            indent = line[: len(line) - len(line.lstrip())]
            lines.append(f"{indent}{key}: 5000")
        else:
            lines.append(line)
    text = "\n".join(lines) + "\n"
else:
    inserted = False
    lines = []
    for line in text.splitlines():
        lines.append(line)
        if not inserted and line.strip() == "http_port: 9380":
            lines.append("  rest_api_max_page_size: 5000")
            inserted = True
        elif not inserted and "http_port:" in line and line.strip().startswith("http_port:"):
            lines.append("  rest_api_max_page_size: 5000")
            inserted = True
    if not inserted:
        raise SystemExit("http_port line not found in template")
    text = "\n".join(lines) + "\n"

TEMPLATE.write_text(text, encoding="utf-8")
print("ok")
for line in text.splitlines()[:8]:
    print(line)
