"""
ml/data/generator.py

Генератор синтетических блоков TON с инъекцией аномалий.

Два режима работы:
  1. Offline — генерирует parquet + labels.csv для обучения/тестирования
  2. Kafka  — инжектит аномальные блоки в Kafka stream среди реальных

Типы аномалий:
  - volume_spike:   transaction_count × 5-10x (внезапный всплеск активности)
  - tps_drop:       tps → 0-5 (замедление сети)
  - gas_anomaly:    avg_gas_price × 20-50x (газовый шторм)
  - concentration:  top_address_share → 0.8-0.99 (одиночный кит)
  - drift:          постепенный рост метрик на +10% за блок (серия 20-50 блоков)

Запуск:
    python -m ml.data.generator --mode offline --blocks 10000 --output ml/data/
    python -m ml.data.generator --mode kafka --brokers localhost:9092 --interval 30
"""

import argparse
import csv
import json
import logging
import os
import time
from dataclasses import dataclass, field
from datetime import datetime, timezone, timedelta
from typing import Optional

import numpy as np
import pandas as pd

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
)
log = logging.getLogger("data-generator")


# ---------------------------------------------------------------------------
# Распределения реальных метрик TON (по данным наблюдений)
# ---------------------------------------------------------------------------

NORMAL_DISTRIBUTIONS = {
    "transaction_count": {"mean": 120, "std": 45, "min": 5, "max": 800},
    "unique_addresses":  {"mean": 80, "std": 30, "min": 3, "max": 500},
    "total_value":       {"mean": 5000.0, "std": 3000.0, "min": 0, "max": 100000},
    "total_gas_used":    {"mean": 500_000_000, "std": 200_000_000, "min": 0, "max": 5_000_000_000},
    "avg_gas_price":     {"mean": 25000.0, "std": 5000.0, "min": 1000, "max": 100000},
    "shard_count":       {"mean": 2, "std": 1, "min": 1, "max": 8},
    "block_time":        {"mean": 3.0, "std": 1.0, "min": 1.0, "max": 10.0},
    "tps":               {"mean": 50.0, "std": 20.0, "min": 1.0, "max": 300.0},
    "top_address_share": {"mean": 0.15, "std": 0.08, "min": 0.0, "max": 0.5},
    "address_reuse_ratio": {"mean": 0.7, "std": 0.15, "min": 0.1, "max": 1.0},
}


@dataclass
class GeneratorConfig:
    n_blocks: int = 10_000
    anomaly_rate: float = 0.05
    drift_series_len: int = 30
    random_state: int = 42
    anomaly_types: list[str] = field(default_factory=lambda: [
        "volume_spike", "tps_drop", "gas_anomaly", "concentration", "drift",
    ])


class SyntheticBlockGenerator:
    """Генерирует реалистичные BlockMetrics с инжектированными аномалиями."""

    def __init__(self, config: GeneratorConfig = None):
        self.cfg = config or GeneratorConfig()
        self.rng = np.random.default_rng(self.cfg.random_state)
        self._drift_remaining = 0
        self._drift_base = {}

    def _sample_normal_block(self, seqno: int, timestamp: datetime) -> dict:
        """Генерирует один нормальный блок на основе реальных распределений."""
        block = {"seqno": seqno, "timestamp": timestamp.isoformat()}

        for metric, dist in NORMAL_DISTRIBUTIONS.items():
            value = self.rng.normal(dist["mean"], dist["std"])
            value = np.clip(value, dist["min"], dist["max"])
            if metric in ("transaction_count", "unique_addresses", "shard_count",
                          "total_gas_used"):
                value = int(round(value))
            block[metric] = value

        # Computed fields
        tx = max(block["transaction_count"], 1)
        block["avg_tx_value"] = block["total_value"] / tx
        block["value_per_addr"] = block["total_value"] / max(block["unique_addresses"], 1)
        block["tps"] = tx / max(block["block_time"], 0.1)
        block["address_reuse_ratio"] = min(1.0, block["unique_addresses"] / tx)

        # Tier 2 (optional)
        block["external_msg_count"] = int(tx * self.rng.uniform(0.3, 0.6))
        block["internal_msg_count"] = int(tx * self.rng.uniform(0.4, 0.8))
        block["contract_call_count"] = int(tx * self.rng.uniform(0.1, 0.4))
        block["zero_value_tx_count"] = int(tx * self.rng.uniform(0.0, 0.15))
        block["max_tx_value"] = block["total_value"] * self.rng.uniform(0.3, 0.8)
        block["min_tx_value"] = block["total_value"] * self.rng.uniform(0.0001, 0.01)

        block["is_detailed"] = True
        block["processed_at"] = datetime.now(timezone.utc).isoformat()

        return block

    def inject_anomaly(self, block: dict, anomaly_type: str) -> dict:
        """Модифицирует блок, создавая аномалию заданного типа."""
        block = block.copy()

        if anomaly_type == "volume_spike":
            multiplier = self.rng.uniform(5, 10)
            block["transaction_count"] = int(block["transaction_count"] * multiplier)
            block["unique_addresses"] = int(block["unique_addresses"] * multiplier * 0.7)
            block["total_value"] *= multiplier * 1.5
            block["tps"] = block["transaction_count"] / max(block["block_time"], 0.1)

        elif anomaly_type == "tps_drop":
            block["tps"] = self.rng.uniform(0.5, 5.0)
            block["block_time"] = self.rng.uniform(15.0, 60.0)
            block["transaction_count"] = max(1, int(block["tps"] * block["block_time"]))

        elif anomaly_type == "gas_anomaly":
            multiplier = self.rng.uniform(20, 50)
            block["avg_gas_price"] *= multiplier
            block["total_gas_used"] = int(block["total_gas_used"] * multiplier * 0.5)

        elif anomaly_type == "concentration":
            block["top_address_share"] = self.rng.uniform(0.8, 0.99)
            block["unique_addresses"] = max(1, int(block["unique_addresses"] * 0.1))
            block["address_reuse_ratio"] = self.rng.uniform(0.01, 0.1)
            block["total_value"] *= self.rng.uniform(3, 10)

        elif anomaly_type == "drift":
            # Drift использует внутренний счётчик серии
            pass  # Обрабатывается в generate_dataset

        return block

    def generate_dataset(
        self,
        n_blocks: int = None,
        anomaly_rate: float = None,
        start_seqno: int = 40_000_000,
    ) -> tuple[pd.DataFrame, pd.DataFrame]:
        """
        Генерирует датасет из n_blocks блоков с anomaly_rate долей аномалий.

        Returns:
            (blocks_df, labels_df) — DataFrame с блоками и DataFrame с метками
        """
        n = n_blocks or self.cfg.n_blocks
        rate = anomaly_rate or self.cfg.anomaly_rate
        types = [t for t in self.cfg.anomaly_types if t != "drift"]
        drift_enabled = "drift" in self.cfg.anomaly_types

        blocks = []
        labels = []
        ts = datetime(2024, 6, 1, tzinfo=timezone.utc)
        drift_remaining = 0
        drift_factor = 1.0
        drift_type_label = ""

        for i in range(n):
            seqno = start_seqno + i
            ts += timedelta(seconds=self.rng.uniform(2, 5))

            block = self._sample_normal_block(seqno, ts)
            is_anomaly = False
            anomaly_type = "normal"

            # Drift серия
            if drift_remaining > 0:
                drift_factor *= 1.10  # +10% каждый блок
                block["transaction_count"] = int(block["transaction_count"] * drift_factor)
                block["tps"] = block["transaction_count"] / max(block["block_time"], 0.1)
                block["unique_addresses"] = int(block["unique_addresses"] * drift_factor * 0.8)
                is_anomaly = True
                anomaly_type = "drift"
                drift_remaining -= 1
                if drift_remaining == 0:
                    drift_factor = 1.0

            # Случайная аномалия (кроме drift)
            elif self.rng.random() < rate:
                if drift_enabled and self.rng.random() < 0.2:
                    # Начинаем drift серию
                    drift_remaining = self.rng.integers(20, 50)
                    drift_factor = 1.0
                    is_anomaly = True
                    anomaly_type = "drift"
                else:
                    anomaly_type = self.rng.choice(types)
                    block = self.inject_anomaly(block, anomaly_type)
                    is_anomaly = True

            # Маркер синтетики
            block["is_synthetic"] = True

            blocks.append(block)
            labels.append({
                "seqno": seqno,
                "timestamp": block["timestamp"],
                "is_anomaly": is_anomaly,
                "anomaly_type": anomaly_type,
            })

        blocks_df = pd.DataFrame(blocks)
        labels_df = pd.DataFrame(labels)

        n_anomalies = labels_df["is_anomaly"].sum()
        log.info(
            "Сгенерировано %d блоков: %d аномалий (%.1f%%), типы: %s",
            n, n_anomalies, n_anomalies / n * 100,
            labels_df[labels_df["is_anomaly"]]["anomaly_type"].value_counts().to_dict(),
        )

        return blocks_df, labels_df


class AnomalyInjector:
    """Инжектирует аномальные блоки в Kafka stream среди реальных."""

    def __init__(self, brokers: str, topic: str, config: GeneratorConfig = None):
        from confluent_kafka import Producer

        self.cfg = config or GeneratorConfig()
        self.generator = SyntheticBlockGenerator(self.cfg)
        self.topic = topic
        self._producer = Producer({"bootstrap.servers": brokers})
        self._labels: list[dict] = []
        self._seqno_counter = 99_000_000  # Отличимые seqno для синтетики

    def inject_one(self, anomaly_type: str = None) -> dict:
        """Создаёт и публикует один аномальный блок в Kafka."""
        self._seqno_counter += 1
        ts = datetime.now(timezone.utc)
        block = self.generator._sample_normal_block(self._seqno_counter, ts)

        if anomaly_type is None:
            anomaly_type = self.generator.rng.choice(self.cfg.anomaly_types)

        if anomaly_type != "normal":
            block = self.generator.inject_anomaly(block, anomaly_type)

        block["is_synthetic"] = True

        # Публикуем в Kafka
        key = str(block["seqno"]).encode()
        value = json.dumps(block, default=str).encode()
        self._producer.produce(self.topic, key=key, value=value)
        self._producer.flush()

        label = {
            "seqno": block["seqno"],
            "timestamp": block["timestamp"],
            "is_anomaly": anomaly_type != "normal",
            "anomaly_type": anomaly_type,
        }
        self._labels.append(label)

        log.info("Инжектирован блок seqno=%d тип=%s", block["seqno"], anomaly_type)
        return block

    def run_periodic(self, interval_sec: float = 30.0, duration_sec: float = None):
        """Периодически инжектирует аномалии с заданным интервалом."""
        log.info("Запуск инъекции: интервал=%.0fs, топик=%s", interval_sec, self.topic)
        start = time.monotonic()

        try:
            while True:
                self.inject_one()
                if duration_sec and (time.monotonic() - start) >= duration_sec:
                    break
                time.sleep(interval_sec)
        except KeyboardInterrupt:
            log.info("Инъекция остановлена пользователем")
        finally:
            self._producer.flush()

        log.info("Инжектировано блоков: %d", len(self._labels))
        return self._labels

    def save_labels(self, path: str):
        """Сохраняет ground truth labels инжектированных блоков."""
        if not self._labels:
            log.warning("Нет меток для сохранения")
            return

        with open(path, "w", newline="") as f:
            writer = csv.DictWriter(f, fieldnames=["seqno", "timestamp", "is_anomaly", "anomaly_type"])
            writer.writeheader()
            writer.writerows(self._labels)

        log.info("Метки сохранены: %s (%d записей)", path, len(self._labels))


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(description="TON synthetic data generator")
    parser.add_argument("--mode", choices=["offline", "kafka"], default="offline",
                        help="offline = parquet, kafka = inject to stream")
    parser.add_argument("--blocks", type=int, default=10_000,
                        help="Количество блоков для offline-генерации")
    parser.add_argument("--anomaly-rate", type=float, default=0.05,
                        help="Доля аномалий (0.0-1.0)")
    parser.add_argument("--output", type=str, default="ml/data/",
                        help="Директория для сохранения (offline)")
    parser.add_argument("--brokers", type=str, default="localhost:9092",
                        help="Kafka брокеры")
    parser.add_argument("--topic", type=str, default="ton.blocks",
                        help="Kafka топик")
    parser.add_argument("--interval", type=float, default=30.0,
                        help="Интервал инъекции в секундах (kafka)")
    parser.add_argument("--duration", type=float, default=None,
                        help="Длительность инъекции в секундах (kafka, None=бесконечно)")
    parser.add_argument("--types", type=str, default=None,
                        help="Типы аномалий через запятую (volume_spike,tps_drop,...)")
    parser.add_argument("--seed", type=int, default=42)
    args = parser.parse_args()

    anomaly_types = args.types.split(",") if args.types else None
    cfg = GeneratorConfig(
        n_blocks=args.blocks,
        anomaly_rate=args.anomaly_rate,
        random_state=args.seed,
        anomaly_types=anomaly_types or GeneratorConfig().anomaly_types,
    )

    if args.mode == "offline":
        gen = SyntheticBlockGenerator(cfg)
        blocks_df, labels_df = gen.generate_dataset()

        os.makedirs(args.output, exist_ok=True)
        blocks_path = os.path.join(args.output, "synthetic_blocks.parquet")
        labels_path = os.path.join(args.output, "synthetic_labels.csv")

        blocks_df.to_parquet(blocks_path, index=False)
        labels_df.to_csv(labels_path, index=False)

        log.info("Сохранено: %s, %s", blocks_path, labels_path)

    elif args.mode == "kafka":
        injector = AnomalyInjector(args.brokers, args.topic, cfg)
        injector.run_periodic(interval_sec=args.interval, duration_sec=args.duration)

        labels_path = os.path.join(args.output, "injected_labels.csv")
        os.makedirs(args.output, exist_ok=True)
        injector.save_labels(labels_path)


if __name__ == "__main__":
    main()
