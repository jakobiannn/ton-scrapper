"""
ml/alerting/telegram_bot.py

Telegram-бот для алертинга об аномалиях в блокчейне TON.

Два режима работы:
  1. Kafka consumer — читает аномалии из ton.anomalies и отправляет в Telegram
  2. Standalone — интегрируется в RealtimeAnomalyDetector как callback

Настройка:
  1. Создать бота через @BotFather → получить TOKEN
  2. Узнать chat_id: отправить боту сообщение, затем открыть
     https://api.telegram.org/bot<TOKEN>/getUpdates
  3. Задать переменные окружения:
     TELEGRAM_BOT_TOKEN=123456:ABC-DEF...
     TELEGRAM_CHAT_ID=123456789

Запуск:
    python -m ml.alerting.telegram_bot                          # из Kafka
    python -m ml.alerting.telegram_bot --test                   # тестовое сообщение
"""

import argparse
import json
import logging
import os
import time
import signal
from dataclasses import dataclass, field
from datetime import datetime, timezone
from typing import Optional
from urllib.request import urlopen, Request
from urllib.error import URLError
from urllib.parse import quote

from dotenv import load_dotenv

load_dotenv()

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
)
log = logging.getLogger("telegram-alert")


@dataclass
class TelegramConfig:
    bot_token: str = field(
        default_factory=lambda: os.getenv("TELEGRAM_BOT_TOKEN", "")
    )
    chat_id: str = field(
        default_factory=lambda: os.getenv("TELEGRAM_CHAT_ID", "")
    )
    kafka_brokers: str = field(
        default_factory=lambda: os.getenv("KAFKA_BROKERS", "localhost:9092")
    )
    anomaly_topic: str = "ton.anomalies"
    # Минимальный интервал между алертами (секунды) — антиспам
    min_alert_interval: float = 10.0
    # Минимальное количество голосов для алерта
    min_votes: int = 3


class TelegramAlerter:
    """Отправляет алерты об аномалиях в Telegram."""

    def __init__(self, config: TelegramConfig = None):
        self.cfg = config or TelegramConfig()
        self._last_alert_time = 0.0
        self._alert_count = 0
        self._error_count = 0

        if not self.cfg.bot_token:
            log.warning("TELEGRAM_BOT_TOKEN не задан — алерты не будут отправляться")
        if not self.cfg.chat_id:
            log.warning("TELEGRAM_CHAT_ID не задан — алерты не будут отправляться")

    @property
    def is_configured(self) -> bool:
        return bool(self.cfg.bot_token and self.cfg.chat_id)

    def send_message(self, text: str, parse_mode: str = "HTML") -> bool:
        """Отправляет сообщение в Telegram через Bot API."""
        if not self.is_configured:
            log.debug("Telegram не настроен, пропускаем")
            return False

        url = (
            f"https://api.telegram.org/bot{self.cfg.bot_token}/sendMessage"
            f"?chat_id={self.cfg.chat_id}"
            f"&parse_mode={parse_mode}"
            f"&text={quote(text)}"
        )

        try:
            req = Request(url, method="GET")
            with urlopen(req, timeout=10) as resp:
                result = json.loads(resp.read())
                if result.get("ok"):
                    self._alert_count += 1
                    return True
                else:
                    log.warning("Telegram API error: %s", result)
                    self._error_count += 1
                    return False
        except URLError as e:
            log.warning("Ошибка отправки в Telegram: %s", e)
            self._error_count += 1
            return False
        except Exception as e:
            log.warning("Неожиданная ошибка Telegram: %s", e)
            self._error_count += 1
            return False

    def send_anomaly_alert(self, anomaly: dict) -> bool:
        """Форматирует и отправляет алерт об аномалии."""
        # Антиспам
        now = time.time()
        if now - self._last_alert_time < self.cfg.min_alert_interval:
            log.debug("Антиспам: пропускаем алерт (%.1fs с последнего)",
                      now - self._last_alert_time)
            return False

        # Фильтр по голосам
        votes = anomaly.get("vote_count", 0)
        if votes < self.cfg.min_votes:
            return False

        seqno = anomaly.get("seqno", "?")
        score = anomaly.get("anomaly_score", 0)
        voters = anomaly.get("voters", "")
        features = anomaly.get("anomaly_features", "")
        is_synthetic = anomaly.get("is_synthetic", False)
        detected_at = anomaly.get("detected_at", datetime.now(timezone.utc).isoformat())

        # Эмодзи по количеству голосов
        severity = "🔴" if votes >= 4 else "🟡" if votes >= 3 else "🟢"
        synth_tag = " [SYNTHETIC]" if is_synthetic else ""

        text = (
            f"{severity} <b>TON Anomaly Detected</b>{synth_tag}\n"
            f"\n"
            f"📦 Block: <code>{seqno}</code>\n"
            f"🗳 Votes: <b>{votes}/5</b>\n"
            f"📊 Score: <b>{score:.3f}</b>\n"
            f"🤖 Models: {voters}\n"
            f"🔍 Features: {features}\n"
            f"⏰ {detected_at}\n"
        )

        self._last_alert_time = now
        return self.send_message(text)

    def send_startup_message(self):
        """Отправляет сообщение о запуске детектора."""
        text = (
            "🚀 <b>TON Anomaly Detector Started</b>\n"
            "\n"
            f"📡 Kafka: {self.cfg.kafka_brokers}\n"
            f"📥 Topic: {self.cfg.anomaly_topic}\n"
            f"🗳 Min votes: {self.cfg.min_votes}\n"
            f"⏰ {datetime.now(timezone.utc).strftime('%Y-%m-%d %H:%M:%S UTC')}\n"
        )
        return self.send_message(text)

    def send_stats_message(self, stats: dict):
        """Отправляет периодическую статистику."""
        text = (
            "📊 <b>TON Detector Stats</b>\n"
            "\n"
            f"📦 Blocks processed: {stats.get('total', 0):,}\n"
            f"🔴 Anomalies detected: {stats.get('anomalies', 0)}\n"
            f"📩 Alerts sent: {self._alert_count}\n"
            f"❌ Errors: {stats.get('errors', 0)}\n"
            f"⏰ {datetime.now(timezone.utc).strftime('%H:%M:%S UTC')}\n"
        )
        return self.send_message(text)

    @property
    def stats(self) -> dict:
        return {
            "alerts_sent": self._alert_count,
            "alert_errors": self._error_count,
        }


def run_kafka_consumer(config: TelegramConfig):
    """Читает аномалии из Kafka и отправляет в Telegram."""
    from confluent_kafka import Consumer, KafkaError

    alerter = TelegramAlerter(config)
    alerter.send_startup_message()

    consumer_conf = {
        "bootstrap.servers": config.kafka_brokers,
        "group.id": "telegram-alerter",
        "auto.offset.reset": "latest",
        "enable.auto.commit": True,
    }
    consumer = Consumer(consumer_conf)
    consumer.subscribe([config.anomaly_topic])

    log.info("Kafka consumer запущен: %s → Telegram", config.anomaly_topic)

    running = True

    def handle_signal(signum, frame):
        nonlocal running
        running = False

    signal.signal(signal.SIGINT, handle_signal)
    signal.signal(signal.SIGTERM, handle_signal)

    try:
        while running:
            msg = consumer.poll(timeout=1.0)
            if msg is None:
                continue
            if msg.error():
                if msg.error().code() == KafkaError._PARTITION_EOF:
                    continue
                log.error("Kafka error: %s", msg.error())
                continue

            try:
                anomaly = json.loads(msg.value())
                sent = alerter.send_anomaly_alert(anomaly)
                if sent:
                    log.info("Alert sent: seqno=%s votes=%s",
                             anomaly.get("seqno"), anomaly.get("vote_count"))
            except json.JSONDecodeError:
                continue

    finally:
        consumer.close()
        log.info("Telegram alerter остановлен. Алертов: %d, ошибок: %d",
                 alerter._alert_count, alerter._error_count)


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(description="TON anomaly Telegram alerter")
    parser.add_argument("--test", action="store_true", help="Отправить тестовое сообщение")
    parser.add_argument("--brokers", type=str, default="localhost:9092")
    parser.add_argument("--topic", type=str, default="ton.anomalies")
    parser.add_argument("--min-votes", type=int, default=3)
    parser.add_argument("--min-interval", type=float, default=10.0,
                        help="Минимальный интервал между алертами (сек)")
    args = parser.parse_args()

    config = TelegramConfig(
        kafka_brokers=args.brokers,
        anomaly_topic=args.topic,
        min_votes=args.min_votes,
        min_alert_interval=args.min_interval,
    )

    if args.test:
        alerter = TelegramAlerter(config)
        test_anomaly = {
            "seqno": 99999999,
            "vote_count": 5,
            "anomaly_score": 0.95,
            "voters": "zscore, cusum, isolation, autoencoder, lof",
            "anomaly_features": "transaction_count, tps, block_time",
            "is_synthetic": True,
            "detected_at": datetime.now(timezone.utc).isoformat(),
        }
        if alerter.send_anomaly_alert(test_anomaly):
            print("Test alert sent successfully!")
        else:
            print("Failed to send test alert. Check TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID")
        return

    run_kafka_consumer(config)


if __name__ == "__main__":
    main()
