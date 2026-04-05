"""
ml/features/builder.py

Читает сырые метрики блоков из TimescaleDB и строит feature matrix
для моделей детекции аномалий.

Запуск:
    python -m ml.features.builder               # последние 24 часа
    python -m ml.features.builder --hours 72    # последние 72 часа
    python -m ml.features.builder --output features.parquet
"""

import argparse
import logging
import os
from dataclasses import dataclass, field

import numpy as np
import pandas as pd
import psycopg2
import psycopg2.extras
from dotenv import load_dotenv

load_dotenv()

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
)
log = logging.getLogger("feature-builder")


# ---------------------------------------------------------------------------
# Конфигурация
# ---------------------------------------------------------------------------

@dataclass
class Config:
    dsn: str = field(
        default_factory=lambda: os.getenv(
            "TIMESCALE_DSN",
            "postgresql://ton:ton_secret@localhost:5432/ton_metrics",
        )
    )
    # Окна для rolling-признаков (в блоках).
    # TON генерирует ~1 блок в 2-3 сек, значит:
    #   10  блоков ≈ 20-30 сек  — очень краткосрочный контекст
    #   30  блоков ≈ 1 мин     — минутное окно
    #   100 блоков ≈ 3-5 мин   — среднесрочный контекст
    rolling_windows: list[int] = field(default_factory=lambda: [10, 30, 100])


# ---------------------------------------------------------------------------
# SQL — выборка сырых метрик
# ---------------------------------------------------------------------------

RAW_QUERY = """
            SELECT
                timestamp, seqno,

                -- Tier 1
                transaction_count, unique_addresses, total_value, total_gas_used, avg_gas_price, shard_count,

                -- Tier 2 (могут быть NULL в fast-режиме)
                COALESCE (external_msg_count, 0) AS external_msg_count, COALESCE (internal_msg_count, 0) AS internal_msg_count, COALESCE (contract_call_count, 0) AS contract_call_count, COALESCE (zero_value_tx_count, 0) AS zero_value_tx_count, COALESCE (max_tx_value, 0) AS max_tx_value, COALESCE (top_address_share, 0) AS top_address_share,

                -- Computed
                COALESCE (tps, 0) AS tps, COALESCE (avg_tx_value, 0) AS avg_tx_value, COALESCE (value_per_addr, 0) AS value_per_addr, COALESCE (block_time, 0) AS block_time, COALESCE (address_reuse_ratio, 0) AS address_reuse_ratio

            FROM block_metrics
            WHERE timestamp >= NOW() - INTERVAL '{hours} hours'
            ORDER BY timestamp ASC; \
            """

# ---------------------------------------------------------------------------
# Базовые признаки — какие колонки участвуют в feature matrix
# ---------------------------------------------------------------------------

# Эти метрики идут в матрицу признаков напрямую, без трансформации.
# Выбраны те что несут самостоятельный сигнал для детекции аномалий.
RAW_FEATURES = [
    "transaction_count",
    "unique_addresses",
    "tps",
    "block_time",
    "top_address_share",
    "address_reuse_ratio",
    "shard_count"
]

# Подмножество RAW_FEATURES для которых считаем rolling-статистики.
# total_value и avg_tx_value нестабильны в fast-режиме (всегда 0) — исключаем
# чтобы не засорять матрицу нулями.
ROLLING_FEATURES = [
    "transaction_count",
    "unique_addresses",
    "tps",
    "block_time",
    "top_address_share",
]


# ---------------------------------------------------------------------------
# FeatureBuilder
# ---------------------------------------------------------------------------

class FeatureBuilder:
    def __init__(self, config: Config):
        self._cfg = config

    def load_raw(self, hours: int = 24) -> pd.DataFrame:
        """Загружает сырые метрики блоков из TimescaleDB за последние N часов."""
        query = RAW_QUERY.format(hours=hours)
        with psycopg2.connect(self._cfg.dsn) as conn:
            df = pd.read_sql(query, conn, parse_dates=["timestamp"])

        log.info("Загружено %d блоков за последние %d ч.", len(df), hours)
        return df

    def add_rolling_features(self, df: pd.DataFrame) -> pd.DataFrame:
        """
        Добавляет скользящие статистики (mean, std) по окнам из config.rolling_windows.

        Для каждого признака из ROLLING_FEATURES и каждого окна W создаёт:
          {feature}_mean_{W}  — среднее за W блоков
          {feature}_std_{W}   — стандартное отклонение за W блоков

        min_periods=1 чтобы не терять первые W-1 блоков в начале датасета.
        """
        df = df.copy()
        for w in self._cfg.rolling_windows:
            for feat in ROLLING_FEATURES:
                roll = df[feat].rolling(window=w, min_periods=1)
                df[f"{feat}_mean_{w}"] = roll.mean()
                df[f"{feat}_std_{w}"] = roll.std().fillna(0)

        log.info(
            "Rolling-признаки добавлены: %d окон × %d метрик = %d новых колонок",
            len(self._cfg.rolling_windows),
            len(ROLLING_FEATURES),
            len(self._cfg.rolling_windows) * len(ROLLING_FEATURES) * 2,
        )
        return df

    def add_delta_features(self, df: pd.DataFrame) -> pd.DataFrame:
        """
        Добавляет признаки скорости изменения (дельты) для каждого RAW_FEATURE.

        Для каждого признака F создаёт:
          {F}_delta      — абсолютное изменение: F[t] - F[t-1]
          {F}_delta_pct  — относительное изменение: delta / (|F[t-1]| + ε)

        Первый блок получает delta=0 (нет предыдущего значения).
        ε=1e-9 защищает от деления на ноль когда предыдущее значение было 0.
        """
        df = df.copy()
        eps = 1e-9
        for feat in RAW_FEATURES:
            prev = df[feat].shift(1).fillna(df[feat].iloc[0])
            delta = df[feat] - prev
            df[f"{feat}_delta"] = delta
            df[f"{feat}_delta_pct"] = delta / (prev.abs() + eps)

        log.info("Delta-признаки добавлены: %d метрик × 2 = %d новых колонок",
                 len(RAW_FEATURES), len(RAW_FEATURES) * 2)
        return df

    def add_zscore_context(self, df: pd.DataFrame) -> pd.DataFrame:
        """
        Добавляет z-score каждого RAW_FEATURE внутри скользящего окна.

        z_score = (x - rolling_mean) / (rolling_std + ε)

        Использует самое большое окно из config.rolling_windows как контекст.
        Высокий |z-score| (>3) уже сам по себе слабый сигнал аномалии,
        но главное — это нормализованный признак который хорошо работает
        с Isolation Forest и Autoencoder без дополнительного скейлинга.
        """
        df = df.copy()
        w = max(self._cfg.rolling_windows)
        eps = 1e-9
        for feat in RAW_FEATURES:
            mean = df[feat].rolling(window=w, min_periods=1).mean()
            std = df[feat].rolling(window=w, min_periods=1).std().fillna(0)
            df[f"{feat}_zscore"] = (df[feat] - mean) / (std + eps)

        log.info("Z-score признаки добавлены для окна %d блоков", w)
        return df

    def build(self, hours: int = 24) -> pd.DataFrame:
        """
        Полный pipeline: загрузка → rolling → delta → zscore.

        Возвращает DataFrame с колонками:
          - timestamp, seqno (идентификаторы, не входят в X)
          - raw features
          - rolling_mean/std features
          - delta features
          - zscore features
        """
        df = self.load_raw(hours)

        if len(df) < 10:
            log.warning("Мало данных (%d блоков) — увеличь --hours или собери больше", len(df))

        df = self.add_rolling_features(df)
        df = self.add_delta_features(df)
        df = self.add_zscore_context(df)

        # Убираем NaN которые могут остаться в начале после rolling
        df = df.fillna(0)

        log.info(
            "Feature matrix готова: %d блоков × %d признаков",
            len(df), len(df.columns) - 2  # минус timestamp и seqno
        )
        return df

    @staticmethod
    def get_feature_columns(df: pd.DataFrame) -> list[str]:
        """Возвращает список колонок которые идут в модель (без timestamp и seqno)."""
        return [c for c in df.columns if c not in ("timestamp", "seqno")]

    @staticmethod
    def to_matrix(df: pd.DataFrame) -> np.ndarray:
        """Конвертирует DataFrame в numpy-матрицу X для передачи в sklearn/torch."""
        cols = FeatureBuilder.get_feature_columns(df)
        return df[cols].values.astype(np.float32)


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(description="TON feature builder")
    parser.add_argument("--hours", type=int, default=24, help="Глубина выборки в часах")
    parser.add_argument("--output", type=str, default=None, help="Сохранить в parquet-файл")
    parser.add_argument("--info", action="store_true", help="Показать статистику по признакам")
    args = parser.parse_args()

    builder = FeatureBuilder(Config())
    df = builder.build(hours=args.hours)

    if args.info:
        print("\n--- Feature matrix info ---")
        print(f"Shape: {df.shape}")
        print(f"Период: {df['timestamp'].min()} → {df['timestamp'].max()}")
        print(f"Блоки: {df['seqno'].min()} → {df['seqno'].max()}")
        print("\n--- Статистики сырых признаков ---")
        print(df[RAW_FEATURES].describe().round(3).to_string())

    if args.output:
        df.to_parquet(args.output, index=False)
        log.info("Сохранено в %s", args.output)
    else:
        print(df[["timestamp", "seqno"] + RAW_FEATURES].tail(10).to_string())


if __name__ == "__main__":
    main()
