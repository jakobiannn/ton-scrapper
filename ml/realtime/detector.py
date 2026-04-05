"""
ml/realtime/detector.py

Real-time детектор аномалий через Kafka.

Читает BlockMetrics из Kafka, применяет предобученные модели,
публикует аномалии в ton.anomalies. Опционально инжектирует
синтетические аномалии для экспериментов.

Архитектура:
  Kafka(ton.blocks) → [скользящее окно 500 блоков]
                    → [feature engineering]
                    → [5 детекторов + ensemble]
                    → Kafka(ton.anomalies) + CSV лог

Запуск:
    python -m ml.realtime.detector --models ml/models/trained/
    python -m ml.realtime.detector --models ml/models/trained/ --inject-anomalies --interval 30
"""

import argparse
import csv
import json
import logging
import os
import signal
import sys
import time
import threading
from collections import deque
from dataclasses import dataclass, field
from datetime import datetime, timezone

import numpy as np
import pandas as pd

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
)
log = logging.getLogger("realtime-detector")


@dataclass
class RealtimeConfig:
    kafka_brokers: str = "localhost:9092"
    input_topic: str = "ton.blocks"
    output_topic: str = "ton.anomalies"
    group_id: str = "ml-realtime"
    window_size: int = 500       # блоков в скользящем окне
    min_window: int = 100        # минимум блоков перед началом детекции
    model_dir: str = "ml/models/trained/"
    predictions_log: str = "ml/results/realtime_predictions.csv"


class RealtimeAnomalyDetector:
    """
    Потоковый детектор аномалий.

    Поддерживает скользящее окно блоков и на каждом новом блоке:
    1. Обновляет окно
    2. Строит features
    3. Прогоняет через модели
    4. Публикует аномалии
    """

    def __init__(self, config: RealtimeConfig = None):
        self.cfg = config or RealtimeConfig()
        self._running = True
        self._window: deque[dict] = deque(maxlen=self.cfg.window_size)
        self._detectors = {}
        self._predictions: list[dict] = []
        self._stats = {"total": 0, "anomalies": 0, "errors": 0}
        self._telegram = None

        # Инициализация Telegram alerter (если настроен)
        try:
            from ml.alerting.telegram_bot import TelegramAlerter
            self._telegram = TelegramAlerter()
            if self._telegram.is_configured:
                log.info("Telegram alerter подключён")
            else:
                self._telegram = None
        except ImportError:
            pass

    def _load_models(self):
        """Загрузка предобученных моделей."""
        from ml.models.saver import load_all_models

        if not os.path.exists(self.cfg.model_dir):
            log.warning("Модели не найдены в %s — используем необученные", self.cfg.model_dir)
            self._init_fresh_detectors()
            return

        try:
            self._detectors = load_all_models(self.cfg.model_dir)
            log.info("Загружено %d моделей из %s", len(self._detectors), self.cfg.model_dir)
        except Exception as e:
            log.warning("Ошибка загрузки моделей: %s — используем необученные", e)
            self._init_fresh_detectors()

    def _init_fresh_detectors(self):
        """Инициализация детекторов без предобучения (обучатся на первых данных)."""
        from ml.detectors.zscore import ZScoreDetector
        from ml.detectors.cusum import CUSUMDetector
        from ml.detectors.isolation import IsolationForestDetector
        from ml.detectors.autoencoder import AutoencoderDetector
        from ml.detectors.lof import LOFDetector

        self._detectors = {
            "zscore": ZScoreDetector(),
            "cusum": CUSUMDetector(),
            "isolation": IsolationForestDetector(),
            "autoencoder": AutoencoderDetector(),
            "lof": LOFDetector(),
        }

    def _build_features(self) -> pd.DataFrame:
        """Строит feature matrix из текущего окна."""
        from ml.features.builder import FeatureBuilder, Config, RAW_FEATURES

        df = pd.DataFrame(list(self._window))

        # Заполняем отсутствующие колонки
        for col in RAW_FEATURES:
            if col not in df.columns:
                df[col] = 0

        builder = FeatureBuilder(Config())
        df = builder.add_rolling_features(df)
        df = builder.add_delta_features(df)
        df = builder.add_zscore_context(df)
        df = df.fillna(0)

        return df

    def _detect_anomalies(self, df: pd.DataFrame) -> dict:
        """Прогоняет последний блок через все модели."""
        from ml.detectors.ensemble import EnsembleDetector

        individual_results = {}
        for name, detector in self._detectors.items():
            try:
                result = detector.detect(df)
                individual_results[name] = result
            except Exception as e:
                log.debug("Детектор '%s' ошибка: %s", name, e)

        if not individual_results:
            return {}

        # Ensemble
        ensemble = EnsembleDetector()
        ensemble_result = ensemble.detect(individual_results)

        # Берём результат для последнего блока
        last_idx = len(df) - 1
        prediction = {
            "is_anomaly": bool(ensemble_result.iloc[last_idx]["is_anomaly"]),
            "vote_count": int(ensemble_result.iloc[last_idx]["vote_count"]),
            "anomaly_score": float(ensemble_result.iloc[last_idx]["anomaly_score"]),
            "voters": str(ensemble_result.iloc[last_idx]["voters"]),
            "anomaly_features": str(ensemble_result.iloc[last_idx]["anomaly_features"]),
        }

        # Per-model scores
        for name, result in individual_results.items():
            prediction[f"{name}_score"] = float(result.iloc[last_idx]["anomaly_score"])
            prediction[f"{name}_anomaly"] = bool(result.iloc[last_idx]["is_anomaly"])

        return prediction

    def _publish_anomaly(self, producer, block: dict, prediction: dict):
        """Публикует аномалию в Kafka + отправляет в Telegram."""
        alert = {
            "seqno": block.get("seqno"),
            "timestamp": block.get("timestamp"),
            "detected_at": datetime.now(timezone.utc).isoformat(),
            "vote_count": prediction["vote_count"],
            "voters": prediction["voters"],
            "anomaly_score": prediction["anomaly_score"],
            "anomaly_features": prediction["anomaly_features"],
            "is_synthetic": block.get("is_synthetic", False),
        }

        # Kafka
        value = json.dumps(alert, default=str).encode()
        producer.produce(self.cfg.output_topic, value=value)
        producer.flush()

        # Telegram (если настроен)
        if self._telegram:
            self._telegram.send_anomaly_alert(alert)

    def _log_prediction(self, block: dict, prediction: dict, latency_ms: float):
        """Записывает prediction для последующего анализа."""
        record = {
            "seqno": block.get("seqno"),
            "timestamp": block.get("timestamp"),
            "is_synthetic": block.get("is_synthetic", False),
            "is_anomaly": prediction.get("is_anomaly", False),
            "vote_count": prediction.get("vote_count", 0),
            "anomaly_score": prediction.get("anomaly_score", 0),
            "voters": prediction.get("voters", ""),
            "latency_ms": round(latency_ms, 2),
        }

        # Per-model
        for name in self._detectors:
            record[f"{name}_anomaly"] = prediction.get(f"{name}_anomaly", False)

        self._predictions.append(record)

    def run(self):
        """Основной цикл: читаем из Kafka, детектируем, публикуем."""
        from confluent_kafka import Consumer, Producer, KafkaError

        self._load_models()

        consumer_conf = {
            "bootstrap.servers": self.cfg.kafka_brokers,
            "group.id": self.cfg.group_id,
            "auto.offset.reset": "earliest",
            "enable.auto.commit": True,
        }
        consumer = Consumer(consumer_conf)
        consumer.subscribe([self.cfg.input_topic])

        producer = Producer({"bootstrap.servers": self.cfg.kafka_brokers})

        log.info("Real-time детекция запущена: %s → модели → %s",
                 self.cfg.input_topic, self.cfg.output_topic)
        log.info("Окно: %d блоков, минимум: %d", self.cfg.window_size, self.cfg.min_window)

        def handle_signal(signum, frame):
            log.info("Получен сигнал %s, останавливаемся...", signum)
            self._running = False

        signal.signal(signal.SIGINT, handle_signal)
        signal.signal(signal.SIGTERM, handle_signal)

        try:
            while self._running:
                msg = consumer.poll(timeout=1.0)
                if msg is None:
                    continue

                if msg.error():
                    if msg.error().code() == KafkaError._PARTITION_EOF:
                        continue
                    log.error("Kafka error: %s", msg.error())
                    continue

                # Парсим блок
                try:
                    block = json.loads(msg.value())
                except json.JSONDecodeError:
                    self._stats["errors"] += 1
                    continue

                self._window.append(block)
                self._stats["total"] += 1

                # Ждём минимум блоков
                if len(self._window) < self.cfg.min_window:
                    if self._stats["total"] % 50 == 0:
                        log.info("Накопление окна: %d/%d блоков",
                                 len(self._window), self.cfg.min_window)
                    continue

                # Детекция
                t0 = time.time()
                try:
                    df = self._build_features()
                    prediction = self._detect_anomalies(df)
                except Exception as e:
                    self._stats["errors"] += 1
                    if self._stats["errors"] % 10 == 1:
                        log.error("Ошибка детекции: %s", e)
                    continue

                latency_ms = (time.time() - t0) * 1000

                if not prediction:
                    continue

                # Логируем
                self._log_prediction(block, prediction, latency_ms)

                if prediction.get("is_anomaly"):
                    self._stats["anomalies"] += 1
                    self._publish_anomaly(producer, block, prediction)
                    log.info(
                        "ANOMALY seqno=%s votes=%d score=%.3f voters=[%s] synthetic=%s latency=%.0fms",
                        block.get("seqno"), prediction["vote_count"],
                        prediction["anomaly_score"], prediction["voters"],
                        block.get("is_synthetic", False), latency_ms,
                    )

                # Периодическая статистика
                if self._stats["total"] % 100 == 0:
                    log.info(
                        "Stats: total=%d anomalies=%d (%.1f%%) errors=%d avg_latency=N/A",
                        self._stats["total"], self._stats["anomalies"],
                        self._stats["anomalies"] / max(self._stats["total"], 1) * 100,
                        self._stats["errors"],
                    )

        finally:
            consumer.close()
            producer.flush()
            self._save_predictions()
            log.info(
                "Остановлен. Итого: %d блоков, %d аномалий, %d ошибок",
                self._stats["total"], self._stats["anomalies"], self._stats["errors"],
            )

    def _save_predictions(self):
        """Сохраняет лог предсказаний в CSV."""
        if not self._predictions:
            return

        os.makedirs(os.path.dirname(self.cfg.predictions_log), exist_ok=True)
        keys = self._predictions[0].keys()

        with open(self.cfg.predictions_log, "w", newline="") as f:
            writer = csv.DictWriter(f, fieldnames=keys)
            writer.writeheader()
            writer.writerows(self._predictions)

        log.info("Predictions сохранены: %s (%d записей)",
                 self.cfg.predictions_log, len(self._predictions))


def run_with_injection(config: RealtimeConfig, inject_interval: float = 30.0):
    """Запускает детектор + параллельную инъекцию аномалий."""
    from ml.data.generator import AnomalyInjector, GeneratorConfig

    detector = RealtimeAnomalyDetector(config)

    # Запускаем инъектор в отдельном потоке
    injector = AnomalyInjector(
        brokers=config.kafka_brokers,
        topic=config.input_topic,
        config=GeneratorConfig(),
    )

    inject_thread = threading.Thread(
        target=injector.run_periodic,
        kwargs={"interval_sec": inject_interval},
        daemon=True,
    )
    inject_thread.start()
    log.info("Инъектор запущен: интервал=%.0fs", inject_interval)

    try:
        detector.run()
    finally:
        # Сохраняем labels инжектированных блоков
        labels_path = config.predictions_log.replace("predictions", "injected_labels")
        injector.save_labels(labels_path)


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(description="TON real-time anomaly detector")
    parser.add_argument("--models", type=str, default="ml/models/trained/",
                        help="Директория с обученными моделями")
    parser.add_argument("--brokers", type=str, default="localhost:9092")
    parser.add_argument("--input-topic", type=str, default="ton.blocks")
    parser.add_argument("--output-topic", type=str, default="ton.anomalies")
    parser.add_argument("--window", type=int, default=500,
                        help="Размер скользящего окна (блоков)")
    parser.add_argument("--min-window", type=int, default=100,
                        help="Минимум блоков перед детекцией")
    parser.add_argument("--predictions-log", type=str,
                        default="ml/results/realtime_predictions.csv")
    parser.add_argument("--inject-anomalies", action="store_true",
                        help="Параллельно инжектировать аномалии")
    parser.add_argument("--inject-interval", type=float, default=30.0,
                        help="Интервал инъекции (секунды)")
    args = parser.parse_args()

    config = RealtimeConfig(
        kafka_brokers=args.brokers,
        input_topic=args.input_topic,
        output_topic=args.output_topic,
        window_size=args.window,
        min_window=args.min_window,
        model_dir=args.models,
        predictions_log=args.predictions_log,
    )

    if args.inject_anomalies:
        run_with_injection(config, args.inject_interval)
    else:
        detector = RealtimeAnomalyDetector(config)
        detector.run()


if __name__ == "__main__":
    main()
