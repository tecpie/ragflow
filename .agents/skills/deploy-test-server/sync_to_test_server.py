#!/usr/bin/env python3
"""Upload files to the RAGFlow test server via SFTP and restart the backend container."""

from __future__ import annotations

import argparse
import os
import re
import shutil
import subprocess
import sys
import zipfile
from pathlib import Path

import paramiko

SKILL_DIR = Path(__file__).resolve().parent
REPO_ROOT = SKILL_DIR.parent.parent.parent
DOCKER_DIR_LOCAL = REPO_ROOT / "docker"
DEFAULT_ENV_FILE = SKILL_DIR / "test-server.env"
DEFAULT_KEY_NAME = "id_ed25519_ragflow"
WEB_DIR = REPO_ROOT / "web"
WEB_DIST_DIR = WEB_DIR / "dist"
WEB_DIST_ZIP = REPO_ROOT / ".tmp-deploy" / "web-dist.zip"

TARGETS = {
    "debug": {
        "remote_base": "/data/docker/ragflow-debug/ragflow",
        "docker_dir": "/data/docker/ragflow-debug",
        "docker_service": "ragflow-cpu",
        "container": "ragflow-debug",
    },
    "stable": {
        "remote_base": "/data/docker/ragflow/ragflow",
        "docker_dir": "/data/docker/ragflow",
        "docker_service": "ragflow-cpu",
        "container": "ragflow",
    },
}

# Local build helper 鈥?do not upload to the test server docker dirs.
DOCKER_SYNC_SKIP = frozenset({"build_amd64_image.sh"})

_DEBUG_VOLUMES = """\
    volumes:
      - ./ragflow:/ragflow
      - ./ragflow-logs:/ragflow/logs
      - ./service_conf.yaml.template:/ragflow/conf/service_conf.yaml.template
      - ./entrypoint.sh:/ragflow/entrypoint.sh
      - /var/run/docker.sock:/var/run/docker.sock
      - /usr/bin/docker:/usr/bin/docker:ro
    environment:
      - DOCKER_HOST=unix:///var/run/docker.sock
      - SANDBOX_EXECUTOR_MANAGER_API_TOKEN=${SANDBOX_EXECUTOR_MANAGER_API_TOKEN:-}
"""

_STABLE_VOLUMES = """\
    volumes:
      - ./ragflow-logs:/ragflow/logs
      - ./service_conf.yaml.template:/ragflow/conf/service_conf.yaml.template
      - ./entrypoint.sh:/ragflow/entrypoint.sh
      - /var/run/docker.sock:/var/run/docker.sock
      - /usr/bin/docker:/usr/bin/docker:ro
    environment:
      - DOCKER_HOST=unix:///var/run/docker.sock
      - SANDBOX_EXECUTOR_MANAGER_API_TOKEN=${SANDBOX_EXECUTOR_MANAGER_API_TOKEN:-}
"""


def _load_env_file(path: Path) -> None:
    if not path.is_file():
        return
    for raw in path.read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        if "=" not in line:
            continue
        key, value = line.split("=", 1)
        key = key.strip()
        value = value.strip().strip('"').strip("'")
        if key and key not in os.environ:
            os.environ[key] = value


def _ssh_key_path() -> Path:
    custom = os.environ.get("SYNC_SSH_KEY", "").strip()
    if custom:
        return Path(custom).expanduser()
    return Path.home() / ".ssh" / DEFAULT_KEY_NAME


def _target_env(key: str, target: str, default: str) -> str:
    prefixed = os.environ.get(f"{key}_{target.upper()}", "").strip()
    if prefixed:
        return prefixed
    if target == "stable":
        legacy = os.environ.get(key, "").strip()
        if legacy:
            return legacy
    return default


def _config(target: str = "debug") -> dict[str, str]:
    _load_env_file(DEFAULT_ENV_FILE)
    local_env = SKILL_DIR / "test-server.local.env"
    _load_env_file(local_env)

    target = target.strip().lower()
    if target not in TARGETS:
        raise SystemExit(f"Unknown target {target!r}. Use: {', '.join(TARGETS)}")

    defaults = TARGETS[target]
    host = os.environ.get("SYNC_HOST", "172.16.0.31").strip()
    user = os.environ.get("SYNC_USER", "root").strip()
    remote_base = _target_env("SYNC_REMOTE_BASE", target, defaults["remote_base"])
    docker_dir = _target_env("SYNC_DOCKER_DIR", target, defaults["docker_dir"])
    docker_service = _target_env("SYNC_DOCKER_SERVICE", target, defaults["docker_service"])
    docker_compose = os.environ.get("SYNC_DOCKER_COMPOSE", "docker-compose").strip()

    return {
        "target": target,
        "host": host,
        "user": user,
        "password": os.environ.get("SYNC_PASS", "").strip(),
        "ssh_key": str(_ssh_key_path()),
        "remote_base": remote_base.rstrip("/"),
        "docker_dir": docker_dir.rstrip("/"),
        "docker_service": docker_service,
        "docker_compose": docker_compose,
        "container": _target_env("SYNC_CONTAINER", target, defaults["container"]),
    }


def _load_private_key(key_path: Path) -> paramiko.PKey:
    errors: list[str] = []
    for key_cls in (paramiko.Ed25519Key, paramiko.RSAKey, paramiko.ECDSAKey):
        try:
            return key_cls.from_private_key_file(str(key_path))
        except paramiko.SSHException as exc:
            errors.append(str(exc))
    raise paramiko.SSHException("; ".join(errors) or f"Unsupported key: {key_path}")


def _connect(cfg: dict[str, str], *, require_password: bool = False) -> tuple[paramiko.SSHClient, paramiko.SFTPClient]:
    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    connect_kwargs = {
        "hostname": cfg["host"],
        "username": cfg["user"],
        "timeout": 30,
        "allow_agent": True,
        "look_for_keys": not require_password,
    }

    key_path = Path(cfg["ssh_key"]).expanduser()
    if key_path.is_file():
        connect_kwargs["pkey"] = _load_private_key(key_path)

    if require_password:
        if not cfg["password"]:
            raise SystemExit(
                "Missing SYNC_PASS. Add it to .agents/skills/deploy-test-server/test-server.env for first-time --setup-ssh."
            )
        connect_kwargs["password"] = cfg["password"]
        connect_kwargs.pop("pkey", None)
        connect_kwargs["look_for_keys"] = False
        connect_kwargs["allow_agent"] = False
    elif "pkey" not in connect_kwargs:
        if cfg["password"]:
            connect_kwargs["password"] = cfg["password"]
        else:
            raise SystemExit(
                "No SSH key found. Run: .\\scripts\\sync-to-test-server.ps1 --setup-ssh"
            )

    try:
        client.connect(**connect_kwargs)
    except paramiko.AuthenticationException:
        if require_password or not cfg["password"]:
            raise
        connect_kwargs.pop("pkey", None)
        connect_kwargs["look_for_keys"] = False
        connect_kwargs["allow_agent"] = False
        connect_kwargs["password"] = cfg["password"]
        client.connect(**connect_kwargs)

    return client, client.open_sftp()


def _ensure_local_ssh_key(key_path: Path) -> Path:
    pub_path = Path(f"{key_path}.pub")
    if key_path.is_file() and pub_path.is_file():
        return pub_path

    key_path.parent.mkdir(parents=True, exist_ok=True)
    subprocess.run(
        [
            "ssh-keygen",
            "-t",
            "ed25519",
            "-f",
            str(key_path),
            "-N",
            "",
            "-C",
            "ragflow-test-server",
        ],
        check=True,
    )
    return pub_path


def _install_public_key(client: paramiko.SSHClient, pubkey_line: str) -> None:
    sftp = client.open_sftp()
    try:
        try:
            sftp.stat(".ssh")
        except OSError:
            sftp.mkdir(".ssh")

        authorized_keys = ".ssh/authorized_keys"
        existing = ""
        try:
            with sftp.open(authorized_keys, "r") as remote_file:
                existing = remote_file.read().decode("utf-8", errors="replace")
        except OSError:
            pass

        lines = [line for line in existing.splitlines() if line.strip()]
        if pubkey_line in lines:
            print("SSH public key already installed on server.")
            return

        lines.append(pubkey_line)
        with sftp.open(authorized_keys, "w") as remote_file:
            remote_file.write("\n".join(lines) + "\n")
        print("Installed SSH public key on server.")
    finally:
        sftp.close()


def setup_ssh(cfg: dict[str, str]) -> None:
    key_path = Path(cfg["ssh_key"]).expanduser()
    pub_path = _ensure_local_ssh_key(key_path)
    pubkey_line = pub_path.read_text(encoding="utf-8").strip()
    print(f"local key {key_path}")

    client, _ = _connect(cfg, require_password=True)
    try:
        _install_public_key(client, pubkey_line)
    finally:
        client.close()

    verify_client, _ = _connect(cfg)
    verify_client.close()
    print("SSH key authentication verified.")


def _git_all_files(excludes: list[str]) -> list[Path]:
    proc = subprocess.run(
        ["git", "ls-files"],
        cwd=REPO_ROOT,
        capture_output=True,
        text=True,
        check=False,
    )
    if proc.returncode != 0:
        raise SystemExit(proc.stderr.strip() or "git ls-files failed")

    normalized_excludes = [item if item.endswith("/") else f"{item}/" for item in excludes]
    files: list[Path] = []
    for line in proc.stdout.splitlines():
        rel = line.strip().replace("\\", "/")
        if not rel:
            continue
        if any(rel.startswith(prefix) or rel == prefix.rstrip("/") for prefix in normalized_excludes):
            continue
        path = REPO_ROOT / rel
        if path.is_file():
            files.append(path)
    return sorted(files)


def _git_changed_files() -> list[Path]:
    cmds = [
        ["git", "diff", "--name-only", "HEAD"],
        ["git", "diff", "--name-only", "--cached"],
        ["git", "ls-files", "--others", "--exclude-standard"],
    ]
    seen: set[str] = set()
    files: list[Path] = []
    for cmd in cmds:
        proc = subprocess.run(cmd, cwd=REPO_ROOT, capture_output=True, text=True, check=False)
        if proc.returncode != 0:
            continue
        for line in proc.stdout.splitlines():
            rel = line.strip().replace("\\", "/")
            if not rel or rel in seen:
                continue
            seen.add(rel)
            path = REPO_ROOT / rel
            if path.is_file():
                files.append(path)
    return sorted(files)


def _resolve_files(explicit: list[str], use_changed: bool, use_all: bool, excludes: list[str]) -> list[Path]:
    if explicit:
        files: list[Path] = []
        for item in explicit:
            path = Path(item)
            if not path.is_absolute():
                path = REPO_ROOT / path
            path = path.resolve()
            if not path.is_file():
                raise SystemExit(f"File not found: {item}")
            if REPO_ROOT not in path.parents and path != REPO_ROOT:
                raise SystemExit(f"File outside repo: {item}")
            files.append(path)
        return files

    if use_all:
        return _git_all_files(excludes)

    if use_changed:
        files = _git_changed_files()
        if not files:
            raise SystemExit("No changed files to sync.")
        return files

    raise SystemExit("Specify files, pass --changed, or pass --all.")


def _ensure_remote_dir(sftp: paramiko.SFTPClient, remote_dir: str) -> None:
    parts = [p for p in remote_dir.split("/") if p]
    current = ""
    for part in parts:
        current += f"/{part}"
        try:
            sftp.stat(current)
        except OSError:
            sftp.mkdir(current)


def _image_repo() -> str:
    return os.environ.get("RAGFLOW_IMAGE_REPO", "registry.cn-hangzhou.aliyuncs.com/tecpie/ragflow").strip()


def _debug_image() -> str:
    return os.environ.get("DEBUG_RAGFLOW_IMAGE", f"{_image_repo()}:amd64-dev").strip()



def _current_git_branch() -> str:
    proc = subprocess.run(
        ["git", "branch", "--show-current"],
        cwd=REPO_ROOT,
        capture_output=True,
        text=True,
        check=False,
    )
    return proc.stdout.strip() if proc.returncode == 0 else ""


def _require_dev_branch(*, allow_non_dev: bool) -> None:
    """Sync must run from local `dev` so feature trees do not wipe dev-only fixes."""
    branch = _current_git_branch()
    if branch == "dev":
        return
    if allow_non_dev:
        print(
            f"WARNING: syncing from branch {branch!r} with --allow-non-dev; "
            "may overwrite dev-only fixes on the server."
        )
        return
    raise SystemExit(
        f"Refusing to sync from branch {branch!r}. "
        "Checkout `dev` (merge/cherry-pick your changes into `dev` first), "
        "or pass --allow-non-dev for an emergency override."
    )


def _branch_image_tag() -> str:
    proc = subprocess.run(
        ["git", "branch", "--show-current"],
        cwd=REPO_ROOT,
        capture_output=True,
        text=True,
        check=False,
    )
    branch = proc.stdout.strip() if proc.returncode == 0 else "unknown"
    branch_tag = re.sub(r"[^a-zA-Z0-9._-]", "-", branch)
    commit_proc = subprocess.run(
        ["git", "rev-parse", "--short", "HEAD"],
        cwd=REPO_ROOT,
        capture_output=True,
        text=True,
        check=False,
    )
    commit = commit_proc.stdout.strip() if commit_proc.returncode == 0 else "unknown"
    return f"amd64-{branch_tag}-{commit}"


def _stable_image() -> str:
    custom = os.environ.get("STABLE_RAGFLOW_IMAGE", "").strip()
    if custom:
        return custom
    return f"{_image_repo()}:{_branch_image_tag()}"


def _parse_env_assignments(text: str, *, first_wins: bool = True) -> dict[str, str]:
    result: dict[str, str] = {}
    for line in text.splitlines():
        stripped = line.strip()
        if not stripped or stripped.startswith("#") or "=" not in stripped:
            continue
        key, value = stripped.split("=", 1)
        key = key.strip()
        if first_wins and key in result:
            continue
        result[key] = value
    return result


def _all_env_values(text: str, key: str) -> list[str]:
    values: list[str] = []
    for line in text.splitlines():
        stripped = line.strip()
        if not stripped or stripped.startswith("#") or "=" not in stripped:
            continue
        k, value = stripped.split("=", 1)
        if k.strip() == key:
            values.append(value)
    return values


def _normalize_compose_profiles(existing: str | None, merged: str) -> str:
    """Repair duplicated / self-referential COMPOSE_PROFILES from older server .env files."""
    old_vals = _all_env_values(existing or "", "COMPOSE_PROFILES")
    want_sandbox = any("sandbox" in v for v in old_vals)
    current = _parse_env_assignments(merged).get("COMPOSE_PROFILES", "")
    if want_sandbox:
        return _set_env_lines(merged, {"COMPOSE_PROFILES": "${DOC_ENGINE},${DEVICE},sandbox"})
    if "${COMPOSE_PROFILES}" in current:
        return _set_env_lines(merged, {"COMPOSE_PROFILES": "${DOC_ENGINE},${DEVICE}"})
    return merged


def _merge_dotenv(existing: str | None, template: str) -> str:
    """Keep existing values for known keys; append only keys new in template.

    Keys that exist only on the server (not in the template) are preserved at the end.
    """
    old = _parse_env_assignments(existing or "")
    used: set[str] = set()
    out: list[str] = []
    for line in template.splitlines():
        stripped = line.strip()
        if not stripped or stripped.startswith("#") or "=" not in stripped:
            out.append(line)
            continue
        key, _ = stripped.split("=", 1)
        key = key.strip()
        used.add(key)
        if key in old:
            out.append(f"{key}={old[key]}")
        else:
            out.append(line)
    orphans = [(k, v) for k, v in old.items() if k not in used]
    if orphans:
        out.append("")
        out.append("# --- preserved from previous server .env ---")
        for key, value in orphans:
            out.append(f"{key}={value}")
    merged = "\n".join(out) + "\n"
    return _normalize_compose_profiles(existing, merged)


def _set_env_lines(text: str, updates: dict[str, str]) -> str:
    """Force-set keys in a .env document (update in place or append)."""
    lines = text.splitlines()
    seen: set[str] = set()
    out: list[str] = []
    for line in lines:
        stripped = line.strip()
        if stripped and not stripped.startswith("#") and "=" in stripped:
            key = stripped.split("=", 1)[0].strip()
            if key in updates:
                out.append(f"{key}={updates[key]}")
                seen.add(key)
                continue
        out.append(line)
    for key, value in updates.items():
        if key not in seen:
            out.append(f"{key}={value}")
    return "\n".join(out) + "\n"


def _comment_depends_on_blocks(text: str) -> str:
    """Comment out depends_on blocks 鈥?test server uses external MySQL/ES hosts."""
    lines = text.splitlines(keepends=True)
    out: list[str] = []
    i = 0
    while i < len(lines):
        line = lines[i]
        if re.match(r"^[ \t]*depends_on:\s*$", line) and not line.lstrip().startswith("#"):
            indent = len(line) - len(line.lstrip(" \t"))
            block = [line]
            i += 1
            while i < len(lines):
                nxt = lines[i]
                if not nxt.strip():
                    break
                nxt_indent = len(nxt) - len(nxt.lstrip(" \t"))
                if nxt_indent <= indent and not nxt.lstrip().startswith("#"):
                    break
                if nxt_indent <= indent and nxt.lstrip().startswith("#"):
                    break
                if nxt_indent > indent or nxt.lstrip().startswith("#"):
                    block.append(nxt)
                    i += 1
                    continue
                break
            for bline in block:
                if bline.lstrip().startswith("#"):
                    out.append(bline)
                else:
                    lead = bline[: len(bline) - len(bline.lstrip(" \t"))]
                    out.append(f"{lead}# {bline.lstrip()}")
            continue
        out.append(line)
        i += 1
    return "".join(out)


def _replace_service_volumes(text: str, volumes_block: str) -> str:
    """Replace only the volumes: list under ragflow-cpu / ragflow-gpu (keep env_file etc.)."""
    if not volumes_block.endswith("\n"):
        volumes_block += "\n"
    lines = text.splitlines(keepends=True)
    out: list[str] = []
    i = 0
    replaced = 0
    env_skipped = 0
    while i < len(lines):
        line = lines[i]
        # Official compose now has environment: above or below volumes.
        # volumes_block already writes one; drop the template copy.
        if line.startswith("    environment:") and env_skipped < 2:
            i += 1
            while i < len(lines):
                nxt = lines[i]
                if nxt.startswith("      ") or nxt.strip() == "":
                    if nxt.strip() == "":
                        break
                    i += 1
                    continue
                if re.match(r"^    \S", nxt):
                    break
                i += 1
            env_skipped += 1
            continue
        if line.startswith("    volumes:") and replaced < 2:
            i += 1
            while i < len(lines):
                nxt = lines[i]
                # Continuation lines of the volumes list (items / comments).
                if nxt.startswith("      ") or nxt.strip() == "":
                    if nxt.strip() == "":
                        break
                    i += 1
                    continue
                # Same-indent sibling key (env_file / networks / ...) ends the volumes block.
                if re.match(r"^    \S", nxt):
                    break
                i += 1
            # volumes_block already includes environment:; drop the template's copy.
            if i < len(lines) and lines[i].startswith("    environment:"):
                i += 1
                while i < len(lines):
                    nxt = lines[i]
                    if nxt.startswith("      ") or nxt.strip() == "":
                        if nxt.strip() == "":
                            break
                        i += 1
                        continue
                    if re.match(r"^    \S", nxt):
                        break
                    i += 1
            out.append(volumes_block)
            replaced += 1
            continue
        out.append(line)
        i += 1
    return "".join(out)


def _ensure_container_name(text: str, service: str, name: str) -> str:
    service_re = re.compile(rf"(^[ \t]*{re.escape(service)}:\s*\n)", re.MULTILINE)
    match = service_re.search(text)
    if not match:
        return text
    rest = text[match.end() :]
    # Replace existing container_name under this service (before next service)
    next_svc = re.search(r"^[ \t]*[a-zA-Z0-9_-]+:\s*$", rest, re.MULTILINE)
    service_body = rest[: next_svc.start()] if next_svc else rest
    tail = rest[next_svc.start() :] if next_svc else ""
    if re.search(r"^[ \t]*container_name:\s*.+$", service_body, re.MULTILINE):
        service_body = re.sub(
            r"^[ \t]*container_name:\s*.+$",
            f"    container_name: {name}",
            service_body,
            count=1,
            flags=re.MULTILINE,
        )
    else:
        service_body = f"    container_name: {name}\n" + service_body
    return text[: match.end()] + service_body + tail


def _strip_init_model_provider_flag(text: str) -> str:
    """Test MySQL has mixed collations; skip auto provider-table init on boot."""
    return re.sub(
        r"^[ \t]*- --init-model-provider-tables\s*\n",
        "",
        text,
        flags=re.MULTILINE,
    )


def _patch_compose_for_target(text: str, target: str) -> str:
    text = _comment_depends_on_blocks(text)
    text = _strip_init_model_provider_flag(text)
    name = "ragflow-debug" if target == "debug" else "ragflow"
    text = _ensure_container_name(text, "ragflow-cpu", name)
    # gpu keeps default generated name; cpu is the one we run
    volumes = _DEBUG_VOLUMES if target == "debug" else _STABLE_VOLUMES
    text = _replace_service_volumes(text, volumes)
    return text


def _iter_docker_files() -> list[Path]:
    files: list[Path] = []
    for path in sorted(DOCKER_DIR_LOCAL.rglob("*")):
        if not path.is_file():
            continue
        rel = path.relative_to(DOCKER_DIR_LOCAL).as_posix()
        if rel in DOCKER_SYNC_SKIP:
            continue
        if path.name.startswith(".") and path.name not in {".env", ".env.single-bucket-example"}:
            continue
        files.append(path)
    return files


def _remote_docker_path(cfg: dict[str, str], local_path: Path) -> str:
    rel = local_path.relative_to(DOCKER_DIR_LOCAL).as_posix()
    return f"{cfg['docker_dir']}/{rel}"


def _remote_path(cfg: dict[str, str], local_path: Path) -> str:
    rel = local_path.relative_to(REPO_ROOT).as_posix()
    if rel == "docker" or rel.startswith("docker/"):
        suffix = rel[len("docker/") :] if rel.startswith("docker/") else ""
        return f"{cfg['docker_dir']}/{suffix}" if suffix else cfg["docker_dir"]
    return f"{cfg['remote_base']}/{rel}"


def _sftp_read_text(sftp: paramiko.SFTPClient, remote_path: str) -> str | None:
    try:
        with sftp.open(remote_path, "r") as fh:
            return fh.read().decode("utf-8")
    except OSError:
        return None


def _sftp_write_text(sftp: paramiko.SFTPClient, remote_path: str, text: str) -> None:
    remote_dir = os.path.dirname(remote_path)
    _ensure_remote_dir(sftp, remote_dir)
    with sftp.open(remote_path, "w") as fh:
        fh.write(text.encode("utf-8"))


def _sync_dotenv(
    sftp: paramiko.SFTPClient,
    cfg: dict[str, str],
    *,
    force_updates: dict[str, str] | None = None,
) -> None:
    remote_env = f"{cfg['docker_dir']}/.env"
    template = (DOCKER_DIR_LOCAL / ".env").read_text(encoding="utf-8")
    existing = _sftp_read_text(sftp, remote_env)
    merged = _merge_dotenv(existing, template)
    if force_updates:
        merged = _set_env_lines(merged, force_updates)
    _sftp_write_text(sftp, remote_env, merged)
    added = sorted(set(_parse_env_assignments(template)) - set(_parse_env_assignments(existing or "")))
    print(f"synced .env -> {remote_env} (merge keep existing; new keys: {len(added)})")
    if added:
        print(f"  new keys: {', '.join(added[:20])}{'...' if len(added) > 20 else ''}")


def _sync_docker_dir(
    cfg: dict[str, str],
    sftp: paramiko.SFTPClient,
    *,
    force_env: dict[str, str] | None = None,
) -> None:
    """Sync local docker/ into the target compose directory."""
    target = cfg["target"]
    files = _iter_docker_files()
    print(f"sync docker/ -> {cfg['docker_dir']} ({len(files)} files, target={target})")
    for local_path in files:
        rel = local_path.relative_to(DOCKER_DIR_LOCAL).as_posix()
        if rel == ".env":
            continue
        remote_path = _remote_docker_path(cfg, local_path)
        if rel == "docker-compose.yml":
            content = _patch_compose_for_target(
                local_path.read_text(encoding="utf-8"),
                target,
            )
            _sftp_write_text(sftp, remote_path, content)
            print(f"uploaded docker/{rel} (patched for {target}) -> {remote_path}")
            continue
        _ensure_remote_dir(sftp, os.path.dirname(remote_path))
        sftp.put(str(local_path), remote_path)
        if rel == "entrypoint.sh":
            # best-effort executable bit via remote chmod later
            pass
        print(f"uploaded docker/{rel} -> {remote_path}")
    _sync_dotenv(sftp, cfg, force_updates=force_env)


def _upload_files(
    sftp: paramiko.SFTPClient,
    files: list[Path],
    cfg: dict[str, str],
) -> None:
    for local_path in files:
        rel = local_path.relative_to(REPO_ROOT).as_posix()
        if rel.startswith("web/dist/") or rel.startswith(".tmp-deploy/"):
            print(f"skip {rel} (deployed via web-dist zip)")
            continue
        if rel == "docker/.env":
            _sync_dotenv(sftp, cfg)
            continue
        if rel.startswith("docker/"):
            suffix = rel[len("docker/") :]
            if suffix in DOCKER_SYNC_SKIP:
                print(f"skip {rel}")
                continue
            remote_path = f"{cfg['docker_dir']}/{suffix}"
            if suffix == "docker-compose.yml":
                content = _patch_compose_for_target(
                    local_path.read_text(encoding="utf-8"),
                    cfg["target"],
                )
                _sftp_write_text(sftp, remote_path, content)
                print(f"uploaded {rel} (patched) -> {remote_path}")
                continue
            _ensure_remote_dir(sftp, os.path.dirname(remote_path))
            sftp.put(str(local_path), remote_path)
            print(f"uploaded {rel} -> {remote_path}")
            continue
        remote_path = _remote_path(cfg, local_path)
        remote_dir = os.path.dirname(remote_path)
        _ensure_remote_dir(sftp, remote_dir)
        sftp.put(str(local_path), remote_path)
        print(f"uploaded {rel} -> {remote_path}")


def _restart(cfg: dict[str, str], client: paramiko.SSHClient) -> None:
    cmd = (
        f"cd {cfg['docker_dir']} && "
        f"{cfg['docker_compose']} restart {cfg['docker_service']} 2>&1"
    )
    _, stdout, stderr = client.exec_command(cmd)
    exit_code = stdout.channel.recv_exit_status()
    out = stdout.read().decode("utf-8", errors="replace").strip()
    err = stderr.read().decode("utf-8", errors="replace").strip()
    if exit_code != 0:
        raise SystemExit(err or out or f"Restart failed with exit code {exit_code}")
    print(out or f"Restarted {cfg['docker_service']}")


def _safe_print(text: str) -> None:
    encoding = sys.stdout.encoding or "utf-8"
    print(text.encode(encoding, errors="replace").decode(encoding, errors="replace"))


def _needs_web_build(files: list[Path]) -> bool:
    return any(
        f.relative_to(REPO_ROOT).as_posix().startswith("web/")
        for f in files
    )


def _npm_executable() -> str:
    for name in ("npm.cmd", "npm"):
        found = shutil.which(name)
        if found:
            return found
    raise SystemExit("npm not found on PATH; install Node.js to build the web frontend")


def _build_web_local() -> Path:
    if not (WEB_DIR / "package.json").is_file():
        raise SystemExit(f"Missing {WEB_DIR / 'package.json'}")
    print(f"building web locally in {WEB_DIR} ...")
    proc = subprocess.run(
        [_npm_executable(), "run", "build"],
        cwd=WEB_DIR,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        check=False,
    )
    combined = "\n".join(
        part for part in (proc.stdout or "", proc.stderr or "") if part
    ).strip()
    for line in combined.splitlines()[-8:]:
        _safe_print(line)
    if proc.returncode != 0:
        raise SystemExit(f"Local web build failed with exit code {proc.returncode}")
    if not WEB_DIST_DIR.is_dir():
        raise SystemExit(f"Build finished but {WEB_DIST_DIR} is missing")
    return WEB_DIST_DIR


def _zip_web_dist(dist: Path) -> Path:
    WEB_DIST_ZIP.parent.mkdir(parents=True, exist_ok=True)
    if WEB_DIST_ZIP.exists():
        WEB_DIST_ZIP.unlink()
    with zipfile.ZipFile(WEB_DIST_ZIP, "w", compression=zipfile.ZIP_DEFLATED) as zf:
        for path in dist.rglob("*"):
            if path.is_file():
                zf.write(path, path.relative_to(dist).as_posix())
    print(f"packed {dist} -> {WEB_DIST_ZIP} ({WEB_DIST_ZIP.stat().st_size} bytes)")
    return WEB_DIST_ZIP


def _deploy_web_dist(
    cfg: dict[str, str],
    client: paramiko.SSHClient,
    sftp: paramiko.SFTPClient,
) -> None:
    """Local npm build -> zip dist -> upload -> extract into remote web/dist."""
    dist = _build_web_local()
    zip_path = _zip_web_dist(dist)
    remote_zip = f"{cfg['docker_dir']}/web-dist.zip"
    remote_dist = f"{cfg['remote_base']}/web/dist"
    print(f"uploading web dist -> {remote_zip}")
    sftp.put(str(zip_path), remote_zip)
    script = f"""set -euo pipefail
python3 - <<'PY'
import pathlib
import shutil
import zipfile

remote_zip = pathlib.Path({remote_zip!r})
remote_dist = pathlib.Path({remote_dist!r})
if remote_dist.exists():
    shutil.rmtree(remote_dist)
remote_dist.mkdir(parents=True)
with zipfile.ZipFile(remote_zip) as zf:
    zf.extractall(remote_dist)
remote_zip.unlink(missing_ok=True)
print(f"extracted web dist to {{remote_dist}}")
PY
"""
    _run_remote_script(client, script, timeout=180)
    print(f"deployed web dist -> {remote_dist}")


def _run_remote_script(client: paramiko.SSHClient, script: str, *, timeout: int = 1800) -> None:
    _, stdout, stderr = client.exec_command(f"bash -s <<'EOF'\n{script}\nEOF", timeout=timeout)
    exit_code = stdout.channel.recv_exit_status()
    out = stdout.read().decode("utf-8", errors="replace").strip()
    err = stderr.read().decode("utf-8", errors="replace").strip()
    if out:
        print(out)
    if exit_code != 0:
        raise SystemExit(err or out or f"Remote command failed with exit code {exit_code}")
    if err:
        print(err)


def _full_update_debug(
    cfg: dict[str, str],
    client: paramiko.SSHClient,
    sftp: paramiko.SFTPClient,
    *,
    run_migrate: bool,
) -> None:
    """Sync docker/, refresh host ragflow/ from image, restore bind mount."""
    docker_dir = cfg["docker_dir"]
    remote_base = cfg["remote_base"]
    container = cfg["container"]
    compose = cfg["docker_compose"]
    service = cfg["docker_service"]
    debug_image = _debug_image()

    _sync_docker_dir(
        cfg,
        sftp,
        force_env={
            "RAGFLOW_IMAGE": debug_image,
            "API_PROXY_SCHEME": "python",
            "REDIS_DB": "2",
            # Avoid colliding with stable's 9383/9384 when template adds GO_* ports.
            "GO_HTTP_PORT": "9394",
            "GO_ADMIN_PORT": "9393",
        },
    )

    script = f"""set -euo pipefail
docker_dir={docker_dir!r}
remote_base={remote_base!r}
container={container!r}
compose_file="$docker_dir/docker-compose.yml"
service={service!r}
compose={compose!r}
debug_image={debug_image!r}

backup_compose() {{
  cp -a "$compose_file" "$compose_file.bak-$(date +%Y%m%d%H%M%S)"
}}

remove_code_mount() {{
  if grep -qE '^[[:space:]]*-[[:space:]]*\\./ragflow:/ragflow[[:space:]]*$' "$compose_file"; then
    sed -i '/^[[:space:]]*-[[:space:]]*\\.\\/ragflow:\\/ragflow[[:space:]]*$/d' "$compose_file"
    echo "removed ./ragflow bind mount from compose"
  else
    echo "compose already has no ./ragflow bind mount"
  fi
}}

restore_code_mount() {{
  if grep -qE '^[[:space:]]*-[[:space:]]*\\./ragflow:/ragflow[[:space:]]*$' "$compose_file"; then
    echo "compose already mounts ./ragflow"
    return
  fi
  sed -i '/^[[:space:]]*volumes:/a\\      - ./ragflow:/ragflow' "$compose_file"
  echo "restored ./ragflow bind mount in compose"
}}

chmod +x "$docker_dir/entrypoint.sh" 2>/dev/null || true
sed -i 's/\\r$//' "$docker_dir/entrypoint.sh" "$docker_dir/service_conf.yaml.template" 2>/dev/null || true

echo "=== [1/6] pull latest image ==="
cd "$docker_dir"
$compose pull "$service"

echo "=== [2/6] remove ./ragflow bind mount ==="
backup_compose
remove_code_mount

echo "=== [3/6] recreate container without bind mount ==="
$compose --profile cpu up -d --force-recreate "$service"

echo "=== [4/6] docker cp /ragflow from container to host ==="
mkdir -p "$remote_base"
docker cp "$container:/ragflow/." "$remote_base/"
rm -f "$remote_base/conf/service_conf.yaml"

echo "=== [5/6] restore bind mount ==="
restore_code_mount

echo "=== [6/6] recreate container with bind mount ==="
$compose --profile cpu up -d --force-recreate "$service"
echo "debug full update complete (image=$debug_image)"
"""
    print(f"full update debug -> {cfg['host']}:{remote_base}")
    print(f"image {debug_image}")
    _run_remote_script(client, script, timeout=1800)
    if run_migrate:
        print("running model provider migrations in container ...")
        migrate_cmd = (
            f"docker exec {container} bash -c 'cd /ragflow && tools/scripts/run_migrations.sh' 2>&1"
        )
        _, stdout, stderr = client.exec_command(migrate_cmd, timeout=1800)
        exit_code = stdout.channel.recv_exit_status()
        out = stdout.read().decode("utf-8", errors="replace").strip()
        err = stderr.read().decode("utf-8", errors="replace").strip()
        if out:
            print(out)
        if exit_code != 0:
            raise SystemExit(err or out or f"Migration failed with exit code {exit_code}")


def _release_stable(
    cfg: dict[str, str],
    client: paramiko.SSHClient,
    sftp: paramiko.SFTPClient,
) -> None:
    docker_dir = cfg["docker_dir"]
    compose = cfg["docker_compose"]
    service = cfg["docker_service"]
    stable_image = _stable_image()

    _sync_docker_dir(
        cfg,
        sftp,
        force_env={"RAGFLOW_IMAGE": stable_image},
    )

    script = f"""set -euo pipefail
docker_dir={docker_dir!r}
service={service!r}
compose={compose!r}
stable_image={stable_image!r}

chmod +x "$docker_dir/entrypoint.sh" 2>/dev/null || true
sed -i 's/\\r$//' "$docker_dir/entrypoint.sh" "$docker_dir/service_conf.yaml.template" 2>/dev/null || true

# Ensure no debug/code tree is mounted into stable.
if grep -qE 'ragflow-debug/ragflow|/ragflow:[[:space:]]*$|\\./ragflow:/ragflow' "$docker_dir/docker-compose.yml"; then
  echo "WARNING: compose still references a host ragflow code mount; patched sync should have removed it"
  grep -nE 'ragflow-debug|\\./ragflow:/ragflow' "$docker_dir/docker-compose.yml" || true
fi

cd "$docker_dir"
echo "=== pull image ==="
$compose pull "$service"

echo "=== recreate stable container ==="
$compose --profile cpu up -d --force-recreate "$service"
echo "stable release complete (image=$stable_image)"
"""
    print(f"releasing stable -> {cfg['host']} (image {stable_image})")
    _run_remote_script(client, script, timeout=1800)


def main() -> int:
    parser = argparse.ArgumentParser(description="Sync files to RAGFlow test server.")
    parser.add_argument("files", nargs="*", help="Repo-relative file paths to upload")
    parser.add_argument(
        "--all",
        action="store_true",
        help="Upload all git-tracked files (default excludes web/ when --exclude is not set)",
    )
    parser.add_argument(
        "--exclude",
        action="append",
        default=[],
        metavar="PREFIX",
        help="Skip paths with this prefix (repeatable)",
    )
    parser.add_argument(
        "--changed",
        action="store_true",
        help="Incremental: upload git changed/untracked files (default when no files given)",
    )
    parser.add_argument(
        "--incremental",
        action="store_true",
        help="Incremental update: sync files into the debug bind-mount directory (alias for --changed)",
    )
    parser.add_argument(
        "--full",
        action="store_true",
        help="Debug full update: pull image, seed host ragflow/ via docker cp, restore mount",
    )
    parser.add_argument(
        "--migrate",
        action="store_true",
        help="After --full, run tools/scripts/run_migrations.sh inside the debug container",
    )
    parser.add_argument(
        "--target",
        choices=sorted(TARGETS),
        default="debug",
        help="Deploy target: debug (default) or stable",
    )
    parser.add_argument("--no-restart", action="store_true", help="Skip docker-compose restart")
    parser.add_argument(
        "--build-web",
        action="store_true",
        help="Build web locally (npm run build), zip dist, upload and extract on server",
    )
    parser.add_argument(
        "--release-stable",
        action="store_true",
        help="Pull latest image and recreate stable container (no file upload)",
    )
    parser.add_argument(
        "--allow-non-dev",
        action="store_true",
        help="Allow sync while not on local `dev` branch (emergency only)",
    )
    parser.add_argument(
        "--setup-ssh",
        action="store_true",
        help="Generate local SSH key and install it on the test server (one-time)",
    )
    args = parser.parse_args()

    cfg = _config(args.target)
    if not args.setup_ssh:
        _require_dev_branch(allow_non_dev=args.allow_non_dev)
    if args.setup_ssh:
        setup_ssh(cfg)
        return 0

    if args.release_stable:
        if cfg["target"] != "stable":
            raise SystemExit("--release-stable requires --target stable")
        client, sftp = _connect(cfg)
        try:
            _release_stable(cfg, client, sftp)
        finally:
            sftp.close()
            client.close()
        return 0

    if args.full:
        if cfg["target"] != "debug":
            raise SystemExit("--full is only supported for --target debug")
        if args.files or args.all or args.changed or args.incremental:
            raise SystemExit("--full cannot be combined with file upload flags")
        client, sftp = _connect(cfg)
        try:
            _full_update_debug(cfg, client, sftp, run_migrate=args.migrate)
        finally:
            sftp.close()
            client.close()
        return 0

    use_changed = args.changed or args.incremental or (
        not args.files and not args.all and not args.build_web
    )
    excludes = args.exclude or (["web/"] if args.all else [])
    if args.build_web and not args.files and not args.all and not args.changed and not args.incremental:
        files = []
    else:
        files = _resolve_files(args.files, use_changed, args.all, excludes)

    print(f"target {cfg['target']} -> {cfg['user']}@{cfg['host']}:{cfg['remote_base']}")
    print(f"files {len(files)}")

    client, sftp = _connect(cfg)
    try:
        if files:
            _upload_files(sftp, files, cfg)
        build_web = args.build_web or (cfg["target"] == "debug" and _needs_web_build(files))
        if build_web:
            if cfg["target"] != "debug":
                raise SystemExit("--build-web is only supported for --target debug")
            _deploy_web_dist(cfg, client, sftp)
        if not args.no_restart and (files or build_web):
            _restart(cfg, client)
    finally:
        sftp.close()
        client.close()

    return 0


if __name__ == "__main__":
    sys.exit(main())
