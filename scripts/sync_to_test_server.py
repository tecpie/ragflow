#!/usr/bin/env python3
"""Upload files to the RAGFlow test server via SFTP and restart the backend container."""

from __future__ import annotations

import argparse
import os
import subprocess
import sys
from pathlib import Path

import paramiko

REPO_ROOT = Path(__file__).resolve().parent.parent
DEFAULT_ENV_FILE = Path(__file__).resolve().parent / "test-server.env"
DEFAULT_KEY_NAME = "id_ed25519_ragflow"

TARGETS = {
    "debug": {
        "remote_base": "/data/docker/ragflow-debug/ragflow",
        "docker_dir": "/data/docker/ragflow-debug",
        "docker_service": "ragflow-cpu",
    },
    "stable": {
        "remote_base": "/data/docker/ragflow/ragflow",
        "docker_dir": "/data/docker/ragflow",
        "docker_service": "ragflow-cpu",
    },
}


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
    local_env = Path(__file__).resolve().parent / "test-server.local.env"
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
                "Missing SYNC_PASS. Add it to scripts/test-server.env for first-time --setup-ssh."
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
                "No SSH key found. Run: py -3 scripts/sync_to_test_server.py --setup-ssh"
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


def _remote_path(cfg: dict[str, str], local_path: Path) -> str:
    rel = local_path.relative_to(REPO_ROOT).as_posix()
    if rel == "docker/entrypoint.sh":
        return f"{cfg['docker_dir']}/entrypoint.sh"
    return f"{cfg['remote_base']}/{rel}"


def _upload_files(
    sftp: paramiko.SFTPClient,
    files: list[Path],
    cfg: dict[str, str],
) -> None:
    for local_path in files:
        rel = local_path.relative_to(REPO_ROOT).as_posix()
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
        help="Upload all git changed/untracked files (default when no files given)",
    )
    parser.add_argument(
        "--target",
        choices=sorted(TARGETS),
        default="debug",
        help="Deploy target: debug (default) or stable",
    )
    parser.add_argument("--no-restart", action="store_true", help="Skip docker-compose restart")
    parser.add_argument(
        "--setup-ssh",
        action="store_true",
        help="Generate local SSH key and install it on the test server (one-time)",
    )
    args = parser.parse_args()

    cfg = _config(args.target)
    if args.setup_ssh:
        setup_ssh(cfg)
        return 0

    use_changed = args.changed or (not args.files and not args.all)
    excludes = args.exclude or (["web/"] if args.all else [])
    files = _resolve_files(args.files, use_changed, args.all, excludes)

    print(f"target {cfg['target']} -> {cfg['user']}@{cfg['host']}:{cfg['remote_base']}")
    print(f"files {len(files)}")

    client, sftp = _connect(cfg)
    try:
        _upload_files(sftp, files, cfg)
        if not args.no_restart:
            _restart(cfg, client)
    finally:
        sftp.close()
        client.close()

    return 0


if __name__ == "__main__":
    sys.exit(main())
