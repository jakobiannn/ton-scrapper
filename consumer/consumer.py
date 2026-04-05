"""
TON Kafka Consumer → TimescaleDB
Читает BlockMetrics из Kafka, сохраняет в TimescaleDB для обучения моделей аномалий.

Запуск:
    pip install confluent-kafka psycopg2-binary python-dotenv
    python consumer.py

Переменные окружения (.env):
    KAFKA_BROKERS=localhost:9092
    KAFKA_TOPIC=ton.blocks
    KAFKA_GROUP_ID=ton-ml-consumer
    TIMESCALE_DSN=postgresql://user:pass@localhost:5432/ton_metrics
"""

import json
import logging
import os
import signal
import sys
import time
from contextlib import contextmanager
from dataclasses import dataclass, field
from datetime import datetime, timezone
from typing import Optional

import psycopg2
import psycopg2.extras
from confluent_kafka import Consumer, KafkaError, KafkaException
from dotenv import load_dotenv

load_dotenv()

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
)
log = logging.getLogger("ton-consumer")


# ---------------------------------------------------------------------------
# Schema SQL — создать таблицы один раз перед запуском
# ---------------------------------------------------------------------------

SCHEMA_SQL = """
-- Расширение TimescaleDB (должно быть установлено)
CREATE EXTENSION IF NOT EXISTS timescaledb;

-- Основная таблица метрик блоков
CREATE TABLE IF NOT EXISTS block_metrics (
    -- Временная метка (ось гипертаблицы)
    timestamp           TIMESTAMPTZ     NOT NULL,
    seqno               BIGINT          NOT NULL,

    -- Tier 1: базовые агрегаты
    transaction_count   INTEGER         NOT NULL DEFAULT 0,
    unique_addresses    INTEGER         NOT NULL DEFAULT 0,
    total_value         DOUBLE PRECISION NOT NULL DEFAULT 0,  -- TON
    total_gas_used      BIGINT          NOT NULL DEFAULT 0,   -- нанотон
    avg_gas_price       DOUBLE PRECISION NOT NULL DEFAULT 0,
    shard_count         INTEGER         NOT NULL DEFAULT 0,

    -- Tier 2: поведенческие метрики (nullable — могут отсутствовать в fast режиме)
    external_msg_count  INTEGER,
    internal_msg_count  INTEGER,
    contract_call_count INTEGER,
    zero_value_tx_count INTEGER,
    max_tx_value        DOUBLE PRECISION,
    min_tx_value        DOUBLE PRECISION,
    top_address_share   DOUBLE PRECISION,  -- [0..1]
    address_reuse_ratio DOUBLE PRECISION,  -- unique_addresses / tx_count

    -- Computed
    tps                 DOUBLE PRECISION,
    avg_tx_value        DOUBLE PRECISION,  -- TON
    value_per_addr      DOUBLE PRECISION,

    -- Технические
    block_time          DOUBLE PRECISION,  -- секунды
    is_detailed         BOOLEAN         NOT NULL DEFAULT FALSE,
    processed_at        TIMESTAMPTZ,
    ingested_at         TIMESTAMPTZ     NOT NULL DEFAULT NOW(),

    PRIMARY KEY (timestamp, seqno)
);

-- Создаём гипертаблицу по timestamp (chunk interval = 1 день)
SELECT create_hypertable(
    'block_metrics',
    'timestamp',
    if_not_exists => TRUE,
    chunk_time_interval => INTERVAL '1 day'
);

-- Индексы для типовых ML-запросов
CREATE INDEX IF NOT EXISTS idx_block_metrics_seqno
    ON block_metrics (seqno);

CREATE INDEX IF NOT EXISTS idx_block_metrics_tps
    ON block_metrics (timestamp DESC, tps);

CREATE INDEX IF NOT EXISTS idx_block_metrics_anomaly_features
    ON block_metrics (timestamp DESC, transaction_count, unique_addresses, total_value);

-- Непрерывный агрегат: 5-минутные окна для rolling features
CREATE MATERIALIZED VIEW IF NOT EXISTS block_metrics_5m
WITH (timescaledb.continuous) AS
SELECT
    time_bucket('5 minutes', timestamp)    AS bucket,
    COUNT(*)                               AS block_count,
    SUM(transaction_count)                 AS total_transactions,
    AVG(transaction_count)                 AS avg_tx_per_block,
    AVG(unique_addresses)                  AS avg_unique_addresses,
    SUM(total_value)                       AS total_volume,
    AVG(tps)                               AS avg_tps,
    AVG(block_time)                        AS avg_block_time,
    MAX(tps)                               AS max_tps,
    AVG(top_address_share)                 AS avg_top_address_share,
    AVG(zero_value_tx_count::FLOAT /
        NULLIF(transaction_count, 0))      AS avg_zero_value_ratio
FROM block_metrics
GROUP BY bucket
WITH NO DATA;

-- Политика обновления агрегата (каждую минуту, покрывает последний час)
SELECT add_continuous_aggregate_policy(
    'block_metrics_5m',
    start_offset => INTERVAL '1 hour',
    end_offset   => INTERVAL '1 minute',
    schedule_interval => INTERVAL '1 minute',
    if_not_exists => TRUE
);

-- Политика хранения: удалять данные старше 90 дней
SELECT add_retention_policy(
    'block_metrics',
    INTERVAL '90 days',
    if_not_exists => TRUE
);
"""


# ---------------------------------------------------------------------------
# Конфигурация
# ---------------------------------------------------------------------------

@dataclass
class Config:
    kafka_brokers: str = field(default_factory=lambda: os.getenv("KAFKA_BROKERS", "localhost:9092"))
    kafka_topic: str = field(default_factory=lambda: os.getenv("KAFKA_TOPIC", "ton.blocks"))
    kafka_group_id: str = field(default_factory=lambda: os.getenv("KAFKA_GROUP_ID", "ton-ml-consumer"))
    timescale_dsn: str = field(
        default_factory=lambda: os.getenv(
            "TIMESCALE_DSN",
            "postgresql://postgres:postgres@localhost:5432/ton_metrics",
        )
    )
    # Батч-вставка: накапливаем N записей или M секунд — что наступит раньше
    batch_size: int = int(os.getenv("BATCH_SIZE", "100"))
    batch_timeout_sec: float = float(os.getenv("BATCH_TIMEOUT_SEC", "5.0"))


# ---------------------------------------------------------------------------
# Маппинг JSON → строка БД
# ---------------------------------------------------------------------------

def parse_block_metrics(raw: bytes) -> Optional[dict]:
    """Парсит JSON из Kafka и возвращает dict для вставки в БД."""
    try:
        data = json.loads(raw)
    except json.JSONDecodeError as e:
        log.warning("Ошибка парсинга JSON: %s | данные: %s", e, raw[:200])
        return None

    # Нормализуем timestamp — Go отдаёт RFC3339
    ts_raw = data.get("timestamp")
    if not ts_raw:
        log.warning("Пропускаем запись без timestamp: seqno=%s", data.get("seqno"))
        return None

    try:
        # Python 3.11+ понимает Z, для старых версий заменяем
        ts = datetime.fromisoformat(ts_raw.replace("Z", "+00:00"))
    except ValueError as e:
        log.warning("Некорректный timestamp %r: %s", ts_raw, e)
        return None

    processed_raw = data.get("processed_at")
    processed_at = None
    if processed_raw:
        try:
            processed_at = datetime.fromisoformat(processed_raw.replace("Z", "+00:00"))
        except ValueError:
            pass

    return {
        "timestamp":           ts,
        "seqno":               data.get("seqno", 0),

        # Tier 1
        "transaction_count":   data.get("transaction_count", 0),
        "unique_addresses":    data.get("unique_addresses", 0),
        "total_value":         data.get("total_value", 0.0),
        "total_gas_used":      data.get("total_gas_used", 0),
        "avg_gas_price":       data.get("avg_gas_price", 0.0),
        "shard_count":         data.get("shard_count", 0),

        # Tier 2 — могут отсутствовать (None → NULL в БД)
        "external_msg_count":  data.get("external_msg_count"),
        "internal_msg_count":  data.get("internal_msg_count"),
        "contract_call_count": data.get("contract_call_count"),
        "zero_value_tx_count": data.get("zero_value_tx_count"),
        "max_tx_value":        data.get("max_tx_value"),
        "min_tx_value":        data.get("min_tx_value"),
        "top_address_share":   data.get("top_address_share"),
        "address_reuse_ratio": data.get("address_reuse_ratio"),

        # Computed
        "tps":                 data.get("tps"),
        "avg_tx_value":        data.get("avg_tx_value"),
        "value_per_addr":      data.get("value_per_addr"),

        # Технические
        "block_time":          data.get("block_time"),
        "is_detailed":         data.get("is_detailed", False),
        "processed_at":        processed_at,
    }


# ---------------------------------------------------------------------------
# TimescaleDB writer
# ---------------------------------------------------------------------------

INSERT_SQL = """
INSERT INTO block_metrics (
    timestamp, seqno,
    transaction_count, unique_addresses, total_value, total_gas_used,
    avg_gas_price, shard_count,
    external_msg_count, internal_msg_count, contract_call_count,
    zero_value_tx_count, max_tx_value, min_tx_value,
    top_address_share, address_reuse_ratio,
    tps, avg_tx_value, value_per_addr,
    block_time, is_detailed, processed_at
) VALUES (
    %(timestamp)s, %(seqno)s,
    %(transaction_count)s, %(unique_addresses)s, %(total_value)s, %(total_gas_used)s,
    %(avg_gas_price)s, %(shard_count)s,
    %(external_msg_count)s, %(internal_msg_count)s, %(contract_call_count)s,
    %(zero_value_tx_count)s, %(max_tx_value)s, %(min_tx_value)s,
    %(top_address_share)s, %(address_reuse_ratio)s,
    %(tps)s, %(avg_tx_value)s, %(value_per_addr)s,
    %(block_time)s, %(is_detailed)s, %(processed_at)s
)
ON CONFLICT (timestamp, seqno) DO NOTHING;
"""


class TimescaleWriter:
    def __init__(self, dsn: str):
        self._dsn = dsn
        self._conn: Optional[psycopg2.extensions.connection] = None
        self._total_inserted = 0
        self._total_skipped = 0

    def connect(self) -> None:
        self._conn = psycopg2.connect(self._dsn)
        self._conn.autocommit = False
        log.info("Подключено к TimescaleDB: %s", self._dsn.split("@")[-1])

    def ensure_schema(self) -> None:
        """Создаёт таблицы если не существуют. Идемпотентно."""
        with self._conn.cursor() as cur:
            cur.execute(SCHEMA_SQL)
        self._conn.commit()
        log.info("Схема БД проверена/создана")

    def insert_batch(self, records: list[dict]) -> int:
        """Вставляет батч записей. Возвращает количество вставленных строк."""
        if not records:
            return 0

        with self._conn.cursor() as cur:
            psycopg2.extras.execute_batch(cur, INSERT_SQL, records, page_size=len(records))
            inserted = cur.rowcount

        self._conn.commit()
        self._total_inserted += inserted
        self._total_skipped += len(records) - inserted
        return inserted

    def close(self) -> None:
        if self._conn and not self._conn.closed:
            self._conn.close()

    @property
    def stats(self) -> dict:
        return {
            "total_inserted": self._total_inserted,
            "total_skipped": self._total_skipped,
        }

    @contextmanager
    def reconnect_on_failure(self):
        """Пере-подключается к БД при обрыве соединения."""
        try:
            yield
        except psycopg2.OperationalError as e:
            log.warning("Соединение с БД потеряно (%s), переподключаемся...", e)
            try:
                self.connect()
            except Exception as reconnect_err:
                log.error("Переподключение не удалось: %s", reconnect_err)
                raise


# ---------------------------------------------------------------------------
# Kafka consumer loop
# ---------------------------------------------------------------------------

class TonKafkaConsumer:
    def __init__(self, config: Config):
        self._cfg = config
        self._running = True
        self._writer = TimescaleWriter(config.timescale_dsn)

        kafka_conf = {
            "bootstrap.servers": config.kafka_brokers,
            "group.id": config.kafka_group_id,
            "auto.offset.reset": "earliest",          # читаем всю историю при первом старте
            "enable.auto.commit": False,               # коммитим вручную после успешной вставки
            "max.poll.interval.ms": 300_000,
            "session.timeout.ms": 30_000,
        }
        self._consumer = Consumer(kafka_conf)

    def _handle_shutdown(self, signum, frame):
        log.info("Получен сигнал %s, останавливаемся...", signum)
        self._running = False

    def run(self) -> None:
        signal.signal(signal.SIGINT, self._handle_shutdown)
        signal.signal(signal.SIGTERM, self._handle_shutdown)

        self._writer.connect()
        self._writer.ensure_schema()

        self._consumer.subscribe([self._cfg.kafka_topic])
        log.info(
            "Подписка на топик %s | группа: %s | батч: %d / %.1fs",
            self._cfg.kafka_topic,
            self._cfg.kafka_group_id,
            self._cfg.batch_size,
            self._cfg.batch_timeout_sec,
        )

        batch: list[dict] = []
        last_flush = time.monotonic()

        try:
            while self._running:
                msg = self._consumer.poll(timeout=1.0)

                # Батч-таймаут: вставляем накопленное даже если батч неполный
                if time.monotonic() - last_flush >= self._cfg.batch_timeout_sec:
                    if batch:
                        self._flush(batch)
                        batch = []
                    last_flush = time.monotonic()

                if msg is None:
                    continue

                if msg.error():
                    if msg.error().code() == KafkaError._PARTITION_EOF:
                        log.debug("Достигнут конец партиции %s [%d]",
                                  msg.topic(), msg.partition())
                    else:
                        raise KafkaException(msg.error())
                    continue

                record = parse_block_metrics(msg.value())
                if record is None:
                    # Плохое сообщение — коммитим офсет чтобы не зависнуть
                    self._consumer.commit(message=msg, asynchronous=False)
                    continue

                batch.append(record)

                # Полный батч — сбрасываем
                if len(batch) >= self._cfg.batch_size:
                    self._flush(batch)
                    batch = []
                    last_flush = time.monotonic()
                    self._consumer.commit(asynchronous=False)

        except Exception as e:
            log.exception("Критическая ошибка consumer: %s", e)
        finally:
            # Финальный flush
            if batch:
                self._flush(batch)
            self._consumer.close()
            self._writer.close()
            log.info(
                "Consumer остановлен. Итого: вставлено=%d, пропущено=%d",
                self._writer.stats["total_inserted"],
                self._writer.stats["total_skipped"],
            )

    def _flush(self, batch: list[dict]) -> None:
        with self._writer.reconnect_on_failure():
            inserted = self._writer.insert_batch(batch)
            log.info(
                "Вставлено %d/%d блоков (всего в БД: %d)",
                inserted, len(batch), self._writer.stats["total_inserted"],
            )


# ---------------------------------------------------------------------------
# Feature query helpers — удобные запросы для ML
# ---------------------------------------------------------------------------

FEATURE_QUERY = """
-- Получить feature vector за последние N часов для обучения
-- Возвращает один ряд на блок со всеми признаками
SELECT
    timestamp,
    seqno,
    transaction_count,
    unique_addresses,
    COALESCE(tps, 0)                                        AS tps,
    COALESCE(avg_tx_value, 0)                               AS avg_tx_value,
    total_value,
    COALESCE(block_time, 5.0)                               AS block_time,
    shard_count,
    COALESCE(top_address_share, 0)                          AS top_address_share,
    COALESCE(address_reuse_ratio, 1.0)                      AS address_reuse_ratio,
    COALESCE(zero_value_tx_count::FLOAT /
             NULLIF(transaction_count, 0), 0)               AS zero_value_ratio,
    COALESCE(contract_call_count::FLOAT /
             NULLIF(transaction_count, 0), 0)               AS contract_call_ratio,

    -- Rolling features из materialized view (5-минутные окна)
    m5.avg_tps                                              AS m5_avg_tps,
    m5.avg_unique_addresses                                 AS m5_avg_unique_addresses,
    m5.avg_top_address_share                                AS m5_avg_top_address_share

FROM block_metrics b
LEFT JOIN block_metrics_5m m5
       ON m5.bucket = time_bucket('5 minutes', b.timestamp)
WHERE b.timestamp >= NOW() - INTERVAL '{hours} hours'
  AND b.is_detailed = TRUE
ORDER BY b.timestamp;
"""

if __name__ == "__main__":
    cfg = Config()
    log.info(
        "Конфигурация: kafka=%s topic=%s db=%s",
        cfg.kafka_brokers, cfg.kafka_topic,
        cfg.timescale_dsn.split("@")[-1],
    )
    TonKafkaConsumer(cfg).run()