#!/usr/bin/env python3
"""Backward-compatible entry point. Implementation lives in the deploy skill."""

from __future__ import annotations

import runpy
from pathlib import Path

_SKILL_SCRIPT = (
    Path(__file__).resolve().parent.parent
    / ".agents"
    / "skills"
    / "deploy-test-server"
    / "sync_to_test_server.py"
)

if __name__ == "__main__":
    runpy.run_path(str(_SKILL_SCRIPT), run_name="__main__")
