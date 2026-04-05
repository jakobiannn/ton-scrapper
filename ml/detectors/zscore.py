"""
ml/detectors/zscore.py

Z-score детектор аномалий — статистический базлайн.

Логика: для каждой метрики считаем скользящее среднее и стандартное
отклонение за окно W блоков. Блок считается аномальным если хотя бы
одна метрика отклонилась больше чем на threshold*σ от своего окна.

Плюсы:  быстро, интерпретируемо, не требует обучения
Минусы: смотрит на метрики по одной, не видит комбинированные аномалии
"""

import numpy as np
import pandas as pd
from dataclasses import dataclass, field


@dataclass
class ZScoreConfig:
    # Окно (в блоках) для расчёта локального среднего и стандартного отклонения.
    # 100 блоков ≈ 3-5 минут для TON — достаточно для локального контекста.
    window: int = 100

    # Порог: блок помечается аномальным если |z| > threshold по любой метрике.
    # Стандартное значение 3.0 — выходит за 99.7% нормального распределения.
    threshold: float = 3.0

    # Метрики для анализа. Только те у которых есть реальный разброс.
    features: list[str] = field(default_factory=lambda: [
        "transaction_count",
        "unique_addresses",
        "tps",
        "block_time",
        "top_address_share",
        "address_reuse_ratio",
    ])


class ZScoreDetector:
    """
    Для каждого блока t и метрики f вычисляет:

        z(t, f) = (x(t,f) - mean(x[t-W:t], f)) / (std(x[t-W:t], f) + ε)

    Anomaly score = max(|z|) по всем метрикам.
    Блок аномален если score > threshold.
    """

    def __init__(self, config: ZScoreConfig = None):
        self.cfg = config or ZScoreConfig()

    def detect(self, df: pd.DataFrame) -> pd.DataFrame:
        """
        Принимает feature DataFrame из FeatureBuilder.
        Возвращает тот же DataFrame с добавленными колонками:
          - zscore_{feature}  — z-score каждой метрики
          - anomaly_score     — максимальный |z| по всем метрикам
          - is_anomaly        — True если score > threshold
          - anomaly_features  — список метрик которые превысили порог
        """
        result = df.copy()
        eps = 1e-9
        z_cols = []

        for feat in self.cfg.features:
            if feat not in df.columns:
                continue

            roll = df[feat].rolling(window=self.cfg.window, min_periods=10)
            mean = roll.mean()
            std  = roll.std().fillna(0)

            col = f"z_{feat}"
            result[col] = (df[feat] - mean) / (std + eps)
            z_cols.append(col)

        # Anomaly score = максимальный абсолютный z-score по всем метрикам
        result["anomaly_score"] = result[z_cols].abs().max(axis=1)
        result["is_anomaly"]    = result["anomaly_score"] > self.cfg.threshold

        # Какие конкретно метрики вышли за порог — полезно для интерпретации
        def explain(row):
            triggered = [
                col.replace("z_", "")
                for col in z_cols
                if abs(row[col]) > self.cfg.threshold
            ]
            return ", ".join(triggered) if triggered else ""

        result["anomaly_features"] = result.apply(explain, axis=1)

        return result

    def summary(self, result: pd.DataFrame) -> dict:
        """Сводная статистика по результатам детекции."""
        anomalies = result[result["is_anomaly"]]
        return {
            "total_blocks":     len(result),
            "anomaly_count":    len(anomalies),
            "anomaly_rate_pct": round(len(anomalies) / len(result) * 100, 2),
            "max_score":        round(result["anomaly_score"].max(), 3),
            "mean_score":       round(result["anomaly_score"].mean(), 3),
            "top_features":     (
                anomalies["anomaly_features"]
                .str.split(", ")
                .explode()
                .value_counts()
                .head(3)
                .to_dict()
            ),
        }