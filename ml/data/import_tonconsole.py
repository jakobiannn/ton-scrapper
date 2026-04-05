"""
ml/data/import_tonconsole.py

Импорт данных из TonConsole SQL в TimescaleDB или parquet.

Workflow:
  1. Генерирует SQL запросы чанками для TonConsole
  2. Загружает CSV-файлы экспортированные из TonConsole
  3. Трансформирует в формат block_metrics
  4. Вставляет в TimescaleDB или сохраняет в parquet

Использование:
  # Шаг 1: Сгенерировать SQL запросы
  python -m ml.data.import_tonconsole --generate-sql --start 55000000 --end 59000000 --chunk 100000

  # Шаг 2: После скачивания CSV из TonConsole — импорт в БД
  python -m ml.data.import_tonconsole --import-csv ./downloads/ --output ml/data/blocks_raw.parquet

  # Шаг 3: Или напрямую в TimescaleDB
  python -m ml.data.import_tonconsole --import-csv ./downloads/ --to-db
"""

import argparse
import csv
import glob
import json
import logging
import os
from datetime import datetime, timezone

import numpy as np
import pandas as pd

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
)
log = logging.getLogger("import-tonconsole")


# ---------------------------------------------------------------------------
# SQL запросы для TonConsole
# ---------------------------------------------------------------------------

BLOCKS_QUERY_TEMPLATE = """SELECT
    b.seqno,
    b.gen_utime,
    b.tx_quantity as transaction_count,
    b.value_flow_fees_collected as total_gas_used,
    b.value_flow_imported + b.value_flow_exported as total_value,
    b.value_flow_fees_collected / NULLIF(b.tx_quantity, 0) as avg_gas_price,
    b.shard,
    b.workchain,
    b.in_msg_descr_length as external_msg_count,
    b.out_msg_descr_length as internal_msg_count,
    b.want_split,
    b.want_merge,
    b.after_merge,
    b.before_split
FROM blockchain.blocks b
WHERE b.workchain = 0
  AND b.gen_utime BETWEEN {start_time} AND {end_time}
ORDER BY b.gen_utime"""


def generate_sql_queries(start_utime: int, end_utime: int, chunk_seconds: int = 259200):
    """
    Генерирует SQL запросы чанками по времени для TonConsole.
    chunk_seconds=259200 = 3 дня ≈ ~90K блоков (при ~2.8 сек/блок).
    """
    queries = []
    current = start_utime

    while current < end_utime:
        chunk_end = min(current + chunk_seconds - 1, end_utime)
        query = BLOCKS_QUERY_TEMPLATE.format(start_time=current, end_time=chunk_end)

        # Человекочитаемые даты
        from datetime import datetime, timezone
        dt_start = datetime.fromtimestamp(current, tz=timezone.utc).strftime("%Y-%m-%d")
        dt_end = datetime.fromtimestamp(chunk_end, tz=timezone.utc).strftime("%Y-%m-%d")
        est_blocks = chunk_seconds // 3  # ~1 блок на 3 секунды

        queries.append({
            "start_time": current,
            "end_time": chunk_end,
            "date_range": f"{dt_start} → {dt_end}",
            "est_blocks": est_blocks,
            "query": query,
        })
        current = chunk_end + 1

    return queries


def print_sql_queries(queries: list):
    """Выводит SQL запросы для копирования в TonConsole."""
    total_est = sum(q["est_blocks"] for q in queries)
    print(f"\n{'='*70}")
    print(f"  ЗАПРОСЫ ДЛЯ TONCONSOLE ({len(queries)} чанков, ~{total_est:,} блоков)")
    print(f"{'='*70}\n")

    for i, q in enumerate(queries):
        print(f"-- Чанк {i+1}/{len(queries)}: {q['date_range']} (~{q['est_blocks']:,} блоков)")
        print(f"-- Сохранить как: blocks_{q['start_time']}_{q['end_time']}.csv")
        print(q["query"])
        print()

    print(f"{'='*70}")
    print(f"Итого: ~{total_est:,} блоков в {len(queries)} запросах")
    print(f"Сохраняйте CSV файлы в одну директорию, затем:")
    print(f"  python -m ml.data.import_tonconsole --import-csv <директория> --output ml/data/blocks.parquet")
    print(f"  python -m ml.data.import_tonconsole --import-csv <директория> --to-db")
    print(f"{'='*70}\n")


# ---------------------------------------------------------------------------
# Импорт CSV
# ---------------------------------------------------------------------------

def import_csv_files(csv_dir: str) -> pd.DataFrame:
    """Загружает и объединяет CSV файлы из TonConsole."""
    patterns = [
        os.path.join(csv_dir, "*.csv"),
        os.path.join(csv_dir, "blocks_*.csv"),
    ]

    files = []
    for pattern in patterns:
        files.extend(glob.glob(pattern))
    files = sorted(set(files))

    if not files:
        raise FileNotFoundError(f"CSV файлы не найдены в {csv_dir}")

    log.info("Найдено %d CSV файлов", len(files))

    dfs = []
    for f in files:
        try:
            df = pd.read_csv(f)
            log.info("  %s: %d строк", os.path.basename(f), len(df))
            dfs.append(df)
        except Exception as e:
            log.warning("  Ошибка чтения %s: %s", f, e)

    if not dfs:
        raise ValueError("Ни один CSV не удалось прочитать")

    combined = pd.concat(dfs, ignore_index=True)

    # Удаляем дубликаты по seqno
    before = len(combined)
    combined = combined.drop_duplicates(subset=["seqno"])
    after = len(combined)
    if before != after:
        log.info("Удалено %d дубликатов по seqno", before - after)

    combined = combined.sort_values("seqno").reset_index(drop=True)
    log.info("Итого: %d уникальных блоков", len(combined))

    return combined


def transform_to_block_metrics(df: pd.DataFrame) -> pd.DataFrame:
    """Трансформирует raw данные из TonConsole в формат block_metrics."""
    result = pd.DataFrame()

    result["seqno"] = df["seqno"].astype(int)

    # gen_utime → timestamp
    result["timestamp"] = pd.to_datetime(df["gen_utime"], unit="s", utc=True)

    # Базовые метрики
    result["transaction_count"] = pd.to_numeric(
        df.get("transaction_count", df.get("tx_quantity", 0)), errors="coerce"
    ).fillna(0).astype(int)

    # unique_addresses — приближение ~70% от tx_count (точное требует JOIN с transactions)
    if "unique_addresses" in df.columns:
        result["unique_addresses"] = df["unique_addresses"].fillna(0).astype(int)
    else:
        result["unique_addresses"] = (result["transaction_count"] * 0.7).astype(int)

    # Value (в нанотонах → TON)
    NANOTON = 1e9
    if "total_value" in df.columns:
        result["total_value"] = pd.to_numeric(df["total_value"], errors="coerce").fillna(0) / NANOTON
    else:
        result["total_value"] = 0.0

    # Gas
    if "total_gas_used" in df.columns:
        result["total_gas_used"] = pd.to_numeric(df["total_gas_used"], errors="coerce").fillna(0).astype(int)
    else:
        result["total_gas_used"] = 0

    if "avg_gas_price" in df.columns:
        result["avg_gas_price"] = pd.to_numeric(df["avg_gas_price"], errors="coerce").fillna(0) / NANOTON
    else:
        result["avg_gas_price"] = result["total_gas_used"] / result["transaction_count"].clip(lower=1) / NANOTON

    # Shard count — по количеству уникальных шардов в окне gen_utime
    result["shard_count"] = 1

    # Tier 2 — доступны из TonConsole!
    if "external_msg_count" in df.columns:
        result["external_msg_count"] = pd.to_numeric(df["external_msg_count"], errors="coerce").fillna(0).astype(int)
    else:
        result["external_msg_count"] = None

    if "internal_msg_count" in df.columns:
        result["internal_msg_count"] = pd.to_numeric(df["internal_msg_count"], errors="coerce").fillna(0).astype(int)
    else:
        result["internal_msg_count"] = None

    result["contract_call_count"] = None
    result["zero_value_tx_count"] = None
    result["max_tx_value"] = None
    result["min_tx_value"] = None

    # Top address share — недоступен из blocks table
    result["top_address_share"] = 0.0

    # Computed
    result["block_time"] = df["gen_utime"].diff().fillna(3.0).clip(0, 300).astype(float)
    result["tps"] = result["transaction_count"] / result["block_time"].clip(lower=0.1)
    result["avg_tx_value"] = result["total_value"] / result["transaction_count"].clip(lower=1)
    result["value_per_addr"] = result["total_value"] / result["unique_addresses"].clip(lower=1)

    result["address_reuse_ratio"] = (
        result["unique_addresses"] / result["transaction_count"].clip(lower=1)
    ).clip(0, 1)

    result["is_detailed"] = False
    result["processed_at"] = pd.Timestamp.now(tz="UTC")

    log.info("Трансформировано %d блоков в формат block_metrics", len(result))
    return result


# ---------------------------------------------------------------------------
# Загрузка в TimescaleDB
# ---------------------------------------------------------------------------

def insert_to_timescaledb(df: pd.DataFrame, dsn: str = None, batch_size: int = 1000):
    """Вставляет block_metrics в TimescaleDB."""
    import psycopg2
    import psycopg2.extras

    if dsn is None:
        dsn = os.getenv("TIMESCALE_DSN", "postgresql://ton:ton_secret@localhost:5433/ton_metrics")

    INSERT_SQL = """
    INSERT INTO block_metrics (
        timestamp, seqno, transaction_count, unique_addresses,
        total_value, total_gas_used, avg_gas_price, shard_count,
        tps, avg_tx_value, value_per_addr, block_time,
        address_reuse_ratio, is_detailed, processed_at
    ) VALUES (
        %(timestamp)s, %(seqno)s, %(transaction_count)s, %(unique_addresses)s,
        %(total_value)s, %(total_gas_used)s, %(avg_gas_price)s, %(shard_count)s,
        %(tps)s, %(avg_tx_value)s, %(value_per_addr)s, %(block_time)s,
        %(address_reuse_ratio)s, %(is_detailed)s, %(processed_at)s
    )
    ON CONFLICT (timestamp, seqno) DO NOTHING;
    """

    conn = psycopg2.connect(dsn)
    conn.autocommit = False

    records = df.to_dict("records")
    total_inserted = 0

    try:
        with conn.cursor() as cur:
            for i in range(0, len(records), batch_size):
                batch = records[i:i + batch_size]
                psycopg2.extras.execute_batch(cur, INSERT_SQL, batch, page_size=batch_size)
                total_inserted += cur.rowcount

                if (i + batch_size) % 10000 == 0:
                    conn.commit()
                    log.info("Вставлено %d/%d записей", i + batch_size, len(records))

        conn.commit()
    finally:
        conn.close()

    log.info("Всего вставлено %d записей в TimescaleDB", total_inserted)
    return total_inserted


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(description="TonConsole data import tool")

    parser.add_argument("--generate-sql", action="store_true",
                        help="Сгенерировать SQL запросы для TonConsole")
    parser.add_argument("--start", type=int, default=None,
                        help="Начальный gen_utime (unix timestamp)")
    parser.add_argument("--end", type=int, default=None,
                        help="Конечный gen_utime (unix timestamp)")
    parser.add_argument("--months", type=int, default=3,
                        help="Сколько месяцев назад (если --start/--end не указаны)")
    parser.add_argument("--chunk-days", type=int, default=3,
                        help="Размер чанка в днях")

    parser.add_argument("--import-csv", type=str, default=None,
                        help="Директория с CSV из TonConsole")
    parser.add_argument("--output", type=str, default=None,
                        help="Сохранить в parquet")
    parser.add_argument("--to-db", action="store_true",
                        help="Вставить в TimescaleDB")
    parser.add_argument("--dsn", type=str, default=None,
                        help="DSN для TimescaleDB")

    args = parser.parse_args()

    if args.generate_sql:
        import time as _time
        now = int(_time.time())
        end_utime = args.end or now
        start_utime = args.start or (now - args.months * 30 * 86400)
        chunk_sec = args.chunk_days * 86400

        queries = generate_sql_queries(start_utime, end_utime, chunk_sec)
        print_sql_queries(queries)
        return

    if args.import_csv:
        # Загрузка CSV
        raw_df = import_csv_files(args.import_csv)

        # Трансформация
        metrics_df = transform_to_block_metrics(raw_df)

        # Сохранение в parquet
        if args.output:
            os.makedirs(os.path.dirname(args.output) or ".", exist_ok=True)
            metrics_df.to_parquet(args.output, index=False)
            log.info("Сохранено в %s", args.output)

        # Вставка в БД
        if args.to_db:
            insert_to_timescaledb(metrics_df, dsn=args.dsn)

        # Если ни --output ни --to-db — показать статистику
        if not args.output and not args.to_db:
            print("\nСтатистика:")
            print(f"  Блоков: {len(metrics_df):,}")
            print(f"  Период: {metrics_df['timestamp'].min()} → {metrics_df['timestamp'].max()}")
            print(f"  Seqno:  {metrics_df['seqno'].min():,} → {metrics_df['seqno'].max():,}")
            print(f"\nСредние метрики:")
            for col in ["transaction_count", "tps", "block_time", "total_value"]:
                if col in metrics_df.columns:
                    print(f"  {col}: {metrics_df[col].mean():.2f}")
            print(f"\nИспользуйте --output или --to-db для сохранения")

        return

    parser.print_help()


if __name__ == "__main__":
    main()
