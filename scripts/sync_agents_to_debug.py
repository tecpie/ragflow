#!/usr/bin/env python3
"""Copy configured agents (and related KB) from stable DB to debug DB."""

from __future__ import annotations

import json
import os
import re
import sys

import pymysql

STABLE_DB = "ragflow"
DEBUG_DB = "ragflow_debug"

AGENT_IDS = [
    "d0f07bc055b411f1ab8757429e2e48f5",  # popular
    "478202d3e13e11f091b87a063ba180ad",  # name
    "897324062fda11f082e633bfb2be14f7",  # parse
    "4cf9283e326a11f1bff30f290b8d52c7",  # intent-recognition
    "c2036654eba511f0a20fea224a7ee167",  # review.rule
    "2c5a5d4feb9f11f08a1bea224a7ee167",  # review
]

EXTRA_KB_IDS = [
    "e1be7aa2eba511f08c5eea224a7ee167",  # review.rule kb from app config
]

OWNER_EMAIL = "platform@tecpie.com"
ID_RE = re.compile(r"[0-9a-f]{32}")


def connect():
    return pymysql.connect(
        host=os.environ.get("MYSQL_HOST", "172.16.0.20"),
        port=int(os.environ.get("MYSQL_PORT", 3306)),
        user=os.environ.get("MYSQL_USER", "tecpie"),
        password=os.environ.get("MYSQL_PASSWORD", ""),
        charset="utf8mb4",
        autocommit=False,
    )


def _columns(conn, db: str, table: str) -> list[str]:
    with conn.cursor() as cur:
        cur.execute(f"SHOW COLUMNS FROM `{db}`.`{table}`")
        return [row[0] for row in cur.fetchall()]


def _pk_column(conn, db: str, table: str) -> str:
    with conn.cursor() as cur:
        cur.execute(f"SHOW KEYS FROM `{db}`.`{table}` WHERE Key_name = 'PRIMARY'")
        rows = cur.fetchall()
        if not rows:
            raise RuntimeError(f"No primary key on {table}")
        return rows[0][4]


def _tenant_id_by_email(conn, db: str, email: str) -> str | None:
    with conn.cursor() as cur:
        cur.execute(
            f"""
            SELECT ut.tenant_id FROM `{db}`.`user` u
            JOIN `{db}`.`user_tenant` ut ON ut.user_id = u.id
            WHERE u.email COLLATE utf8mb4_unicode_ci = %s COLLATE utf8mb4_unicode_ci
            LIMIT 1
            """,
            (email,),
        )
        row = cur.fetchone()
        return row[0] if row else None


def copy_rows_by_ids(conn, table: str, ids: list[str]) -> int:
    if not ids:
        return 0
    pk = _pk_column(conn, STABLE_DB, table)
    cols = _columns(conn, STABLE_DB, table)
    col_sql = ", ".join(f"`{c}`" for c in cols)
    placeholders = ", ".join(["%s"] * len(ids))

    with conn.cursor() as cur:
        cur.execute(
            f"SELECT {col_sql} FROM `{STABLE_DB}`.`{table}` WHERE `{pk}` IN ({placeholders})",
            ids,
        )
        rows = cur.fetchall()
        if not rows:
            return 0

        cur.execute(
            f"DELETE FROM `{DEBUG_DB}`.`{table}` WHERE `{pk}` IN ({placeholders})",
            ids,
        )
        insert_ph = ", ".join(["%s"] * len(cols))
        for row in rows:
            cur.execute(
                f"INSERT INTO `{DEBUG_DB}`.`{table}` ({col_sql}) VALUES ({insert_ph})",
                row,
            )
        return len(rows)


def copy_canvas_versions(conn, canvas_ids: list[str]) -> int:
    with conn.cursor() as cur:
        placeholders = ", ".join(["%s"] * len(canvas_ids))
        cur.execute(
            f"SELECT id FROM `{STABLE_DB}`.`user_canvas_version` "
            f"WHERE user_canvas_id IN ({placeholders})",
            canvas_ids,
        )
        version_ids = [r[0] for r in cur.fetchall()]
    return copy_rows_by_ids(conn, "user_canvas_version", version_ids)


def collect_kb_ids_from_agents(conn) -> list[str]:
    kb_ids = set(EXTRA_KB_IDS)
    with conn.cursor() as cur:
        placeholders = ", ".join(["%s"] * len(AGENT_IDS))
        cur.execute(
            f"SELECT dsl FROM `{STABLE_DB}`.`user_canvas` WHERE id IN ({placeholders})",
            AGENT_IDS,
        )
        for (dsl,) in cur.fetchall():
            text = json.dumps(dsl, ensure_ascii=False)
            for token in ID_RE.findall(text):
                if token in AGENT_IDS:
                    continue
                cur.execute(
                    f"SELECT 1 FROM `{STABLE_DB}`.`knowledgebase` WHERE id = %s", (token,)
                )
                if cur.fetchone():
                    kb_ids.add(token)
    return sorted(kb_ids)


def remap_canvas_owner(conn, stable_tid: str, debug_tid: str) -> int:
    with conn.cursor() as cur:
        placeholders = ", ".join(["%s"] * len(AGENT_IDS))
        cur.execute(
            f"UPDATE `{DEBUG_DB}`.`user_canvas` SET user_id = %s "
            f"WHERE id IN ({placeholders}) AND user_id = %s",
            [debug_tid, *AGENT_IDS, stable_tid],
        )
        return cur.rowcount


def main() -> int:
    stable_tid = _tenant_id_by_email(connect(), STABLE_DB, OWNER_EMAIL)
    debug_tid = _tenant_id_by_email(connect(), DEBUG_DB, OWNER_EMAIL)
    if not stable_tid or not debug_tid:
        print(f"tenant not found: stable={stable_tid} debug={debug_tid}")
        return 1

    conn = connect()
    try:
        n_canvas = copy_rows_by_ids(conn, "user_canvas", AGENT_IDS)
        print(f"user_canvas: {n_canvas}")

        n_versions = copy_canvas_versions(conn, AGENT_IDS)
        print(f"user_canvas_version: {n_versions}")

        kb_ids = collect_kb_ids_from_agents(conn)
        n_kb = copy_rows_by_ids(conn, "knowledgebase", kb_ids)
        print(f"knowledgebase ({len(kb_ids)} ids): {n_kb}")

        n_remap = remap_canvas_owner(conn, stable_tid, debug_tid)
        print(f"remap user_id {stable_tid} -> {debug_tid}: {n_remap} agents")

        conn.commit()
    except Exception:
        conn.rollback()
        raise
    finally:
        conn.close()

    missing = []
    conn = connect()
    with conn.cursor() as cur:
        for aid in AGENT_IDS:
            cur.execute(f"SELECT 1 FROM `{DEBUG_DB}`.`user_canvas` WHERE id = %s", (aid,))
            if not cur.fetchone():
                missing.append(aid)
    conn.close()
    if missing:
        print("WARNING: not found in stable:", ", ".join(missing))
        return 1

    conn = connect()
    with conn.cursor() as cur:
        for kid in EXTRA_KB_IDS:
            cur.execute(f"SELECT 1 FROM `{STABLE_DB}`.`knowledgebase` WHERE id = %s", (kid,))
            if not cur.fetchone():
                print(f"WARNING: config kb {kid} not in stable DB (skipped)")
    conn.close()

    print("done")
    return 0


if __name__ == "__main__":
    sys.exit(main())
