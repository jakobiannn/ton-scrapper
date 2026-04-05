-- =============================================================================
-- TON Metrics — схема TimescaleDB
-- Выполняется автоматически при первом старте контейнера timescaledb
-- (docker-entrypoint-initdb.d порядок: файлы выполняются по имени)
-- =============================================================================

CREATE EXTENSION IF NOT EXISTS timescaledb;

-- ---------------------------------------------------------------------------
-- Основная таблица метрик блоков
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS block_metrics (
                                             timestamp               TIMESTAMPTZ         NOT NULL,
                                             seqno                   BIGINT              NOT NULL,

    -- Tier 1: базовые агрегаты
                                             transaction_count       INTEGER             NOT NULL DEFAULT 0,
                                             unique_addresses        INTEGER             NOT NULL DEFAULT 0,
                                             total_value             DOUBLE PRECISION    NOT NULL DEFAULT 0,   -- TON
                                             total_gas_used          BIGINT              NOT NULL DEFAULT 0,   -- нанотон
                                             avg_gas_price           DOUBLE PRECISION    NOT NULL DEFAULT 0,
                                             shard_count             INTEGER             NOT NULL DEFAULT 0,

    -- Tier 2: поведенческие метрики (NULL если is_detailed=false)
                                             external_msg_count      INTEGER,
                                             internal_msg_count      INTEGER,
                                             contract_call_count     INTEGER,
                                             zero_value_tx_count     INTEGER,
                                             max_tx_value            DOUBLE PRECISION,
                                             min_tx_value            DOUBLE PRECISION,
                                             top_address_share       DOUBLE PRECISION,   -- [0..1]
                                             address_reuse_ratio     DOUBLE PRECISION,

    -- Computed
                                             tps                     DOUBLE PRECISION,
                                             avg_tx_value            DOUBLE PRECISION,
                                             value_per_addr          DOUBLE PRECISION,

    -- Технические
                                             block_time              DOUBLE PRECISION,   -- секунды с предыдущего блока
                                             is_detailed             BOOLEAN             NOT NULL DEFAULT FALSE,
                                             processed_at            TIMESTAMPTZ,
                                             ingested_at             TIMESTAMPTZ         NOT NULL DEFAULT NOW(),

    PRIMARY KEY (timestamp, seqno)
    );

-- Гипертаблица по timestamp, chunk = 1 день
SELECT create_hypertable(
               'block_metrics',
               'timestamp',
               if_not_exists      => TRUE,
               chunk_time_interval => INTERVAL '1 day'
       );

-- ---------------------------------------------------------------------------
-- Индексы
-- ---------------------------------------------------------------------------
CREATE INDEX IF NOT EXISTS idx_bm_seqno
    ON block_metrics (seqno DESC);

CREATE INDEX IF NOT EXISTS idx_bm_tps
    ON block_metrics (timestamp DESC, tps);

CREATE INDEX IF NOT EXISTS idx_bm_anomaly_features
    ON block_metrics (timestamp DESC, transaction_count, unique_addresses, total_value);

-- ---------------------------------------------------------------------------
-- Continuous aggregate: 5-минутные окна (для rolling features в ML)
-- ---------------------------------------------------------------------------
CREATE MATERIALIZED VIEW IF NOT EXISTS block_metrics_5m
WITH (timescaledb.continuous) AS
SELECT
    time_bucket('5 minutes', timestamp)                                     AS bucket,
    COUNT(*)                                                                AS block_count,
    SUM(transaction_count)                                                  AS total_transactions,
    AVG(transaction_count)                                                  AS avg_tx_per_block,
    AVG(unique_addresses)                                                   AS avg_unique_addresses,
    SUM(total_value)                                                        AS total_volume,
    AVG(tps)                                                                AS avg_tps,
    MAX(tps)                                                                AS max_tps,
    AVG(block_time)                                                         AS avg_block_time,
    AVG(top_address_share)                                                  AS avg_top_address_share,
    AVG(zero_value_tx_count::FLOAT / NULLIF(transaction_count, 0))         AS avg_zero_value_ratio,
    AVG(contract_call_count::FLOAT / NULLIF(transaction_count, 0))         AS avg_contract_call_ratio
FROM block_metrics
GROUP BY bucket
    WITH NO DATA;

SELECT add_continuous_aggregate_policy(
               'block_metrics_5m',
               start_offset      => INTERVAL '1 hour',
               end_offset        => INTERVAL '1 minute',
               schedule_interval => INTERVAL '1 minute',
               if_not_exists     => TRUE
       );

-- ---------------------------------------------------------------------------
-- Retention: удалять данные старше 90 дней
-- ---------------------------------------------------------------------------
SELECT add_retention_policy(
               'block_metrics',
               INTERVAL '90 days',
               if_not_exists => TRUE
       );

-- ---------------------------------------------------------------------------
-- Вспомогательное view: feature vector для ML (на основе последних данных)
-- ---------------------------------------------------------------------------
CREATE OR REPLACE VIEW ml_feature_vector AS
SELECT
    b.timestamp,
    b.seqno,
    b.transaction_count,
    b.unique_addresses,
    COALESCE(b.tps, 0)                                                      AS tps,
    COALESCE(b.avg_tx_value, 0)                                             AS avg_tx_value,
    b.total_value,
    COALESCE(b.block_time, 5.0)                                             AS block_time,
    b.shard_count,
    COALESCE(b.top_address_share, 0)                                        AS top_address_share,
    COALESCE(b.address_reuse_ratio, 1.0)                                    AS address_reuse_ratio,
    COALESCE(b.zero_value_tx_count::FLOAT / NULLIF(b.transaction_count,0), 0) AS zero_value_ratio,
    COALESCE(b.contract_call_count::FLOAT / NULLIF(b.transaction_count,0), 0) AS contract_call_ratio,
    m5.avg_tps                                                              AS m5_avg_tps,
    m5.avg_unique_addresses                                                 AS m5_avg_unique_addresses,
    m5.avg_top_address_share                                                AS m5_avg_top_address_share,
    m5.avg_zero_value_ratio                                                 AS m5_avg_zero_value_ratio
FROM block_metrics b
         LEFT JOIN block_metrics_5m m5
                   ON m5.bucket = time_bucket('5 minutes', b.timestamp)
WHERE b.is_detailed = TRUE
ORDER BY b.timestamp DESC;