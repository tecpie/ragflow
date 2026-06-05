#!/usr/bin/env python3
"""Seed debug DB: LLM catalog + per-tenant models mapped from stable by user email."""

from __future__ import annotations

import os
import sys

import pymysql

STABLE_DB = "ragflow"
DEBUG_DB = "ragflow_debug"


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


def copy_catalog(conn) -> None:
    with conn.cursor() as cur:
        for table in ("llm_factories", "llm"):
            cols = _columns(conn, STABLE_DB, table)
            col_sql = ", ".join(f"`{c}`" for c in cols)
            cur.execute(f"DELETE FROM `{DEBUG_DB}`.`{table}`")
            cur.execute(
                f"INSERT INTO `{DEBUG_DB}`.`{table}` ({col_sql}) "
                f"SELECT {col_sql} FROM `{STABLE_DB}`.`{table}`"
            )
            print(f"copied {table}: {cur.rowcount} rows")


def copy_tenant_models_by_email(conn) -> None:
    with conn.cursor(pymysql.cursors.DictCursor) as cur:
        cur.execute(
            f"""
            SELECT u.email, st.id AS stable_tenant_id, dt.id AS debug_tenant_id
            FROM `{STABLE_DB}`.`user` u
            JOIN `{STABLE_DB}`.`user_tenant` sut ON sut.user_id = u.id
            JOIN `{STABLE_DB}`.`tenant` st ON st.id = sut.tenant_id
            JOIN `{DEBUG_DB}`.`user` du ON du.email COLLATE utf8mb4_unicode_ci = u.email COLLATE utf8mb4_unicode_ci
            JOIN `{DEBUG_DB}`.`user_tenant` dut ON dut.user_id = du.id
            JOIN `{DEBUG_DB}`.`tenant` dt ON dt.id = dut.tenant_id
            """
        )
        mappings = cur.fetchall()
        if not mappings:
            print("no matching users between stable and debug")
            return

        cols = _columns(conn, STABLE_DB, "tenant_llm")
        insert_cols_list = [c for c in cols if c != "id"]
        insert_cols = ", ".join(f"`{c}`" for c in insert_cols_list)

        with conn.cursor() as plain_cur:
            for row in mappings:
                email = row["email"]
                stable_tid = row["stable_tenant_id"]
                debug_tid = row["debug_tenant_id"]

                plain_cur.execute(f"DELETE FROM `{DEBUG_DB}`.`tenant_llm` WHERE tenant_id = %s", (debug_tid,))

                plain_cur.execute(
                    f"SELECT {insert_cols} FROM `{STABLE_DB}`.`tenant_llm` WHERE tenant_id = %s",
                    (stable_tid,),
                )
                rows = plain_cur.fetchall()
                for r in rows:
                    data = dict(zip(insert_cols_list, r))
                    data["tenant_id"] = debug_tid
                    placeholders = ", ".join(["%s"] * len(insert_cols_list))
                    plain_cur.execute(
                        f"INSERT INTO `{DEBUG_DB}`.`tenant_llm` ({insert_cols}) VALUES ({placeholders})",
                        [data[c] for c in insert_cols_list],
                    )
                print(f"copied tenant_llm for {email}: {len(rows)} rows")

                plain_cur.execute(
                    f"""
                    UPDATE `{DEBUG_DB}`.`tenant` dt
                    JOIN `{STABLE_DB}`.`tenant` st ON st.id = %s
                    SET dt.llm_id = st.llm_id,
                        dt.embd_id = st.embd_id,
                        dt.asr_id = st.asr_id,
                        dt.img2txt_id = st.img2txt_id,
                        dt.rerank_id = st.rerank_id,
                        dt.parser_ids = st.parser_ids
                    WHERE dt.id = %s
                    """,
                    (stable_tid, debug_tid),
                )


def main() -> int:
    conn = connect()
    try:
        copy_catalog(conn)
        copy_tenant_models_by_email(conn)
        conn.commit()
    except Exception:
        conn.rollback()
        raise
    finally:
        conn.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
