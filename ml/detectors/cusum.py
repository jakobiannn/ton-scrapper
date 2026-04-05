"""
ml/detectors/cusum.py

CUSUM (Cumulative Sum) детектор — статистический базлайн для дрейфа.

Логика: отслеживает накопленное отклонение от нормы. Как только
накопленная сумма превышает порог — сигнал аномалии. После срабатывания
счётчик сбрасывается.

Плюсы:  ловит медленный дрейф который Z-score пропускает,
        чувствителен к направлению изменений (рост vs падение)
Минусы: только одномерный, нужно подбирать два параметра (k и h)
"""

import numpy as np
import pandas as pd
from dataclasses import dataclass, field


@dataclass
class CUSUMConfig:
    # Окно для оценки локальной нормы (mean, std).
    window: int = 100

    # Допустимое отклонение в единицах σ.
    # k=0.5 — стандартное значение: игнорируем отклонения меньше 0.5σ,
    # чтобы не реагировать на мелкий шум.
    k: float = 0.5

    # Порог срабатывания: CUSUM накапливает отклонения и сигналит когда
    # накопленная сумма превышает h*σ.
    # h=5.0 — стандартное значение для финансовых временных рядов.
    h: float = 5.0

    # Метрики для анализа.
    features: list[str] = field(default_factory=lambda: [
        "transaction_count",
        "tps",
        "top_address_share",
        "block_time",
    ])


class CUSUMDetector:
    """
    Для каждой метрики поддерживает два накопителя:
      S_pos[t] = max(0, S_pos[t-1] + (x[t] - μ)/σ - k)  — рост выше нормы
      S_neg[t] = max(0, S_neg[t-1] + (μ - x[t])/σ - k)  — падение ниже нормы

    Аномалия когда S_pos > h или S_neg > h.
    Anomaly score = max(S_pos, S_neg) по всем метрикам.
    """

    def __init__(self, config: CUSUMConfig = None):
        self.cfg = config or CUSUMConfig()

    def detect(self, df: pd.DataFrame) -> pd.DataFrame:
        """
        Принимает feature DataFrame.
        Возвращает DataFrame с колонками:
          - cusum_pos_{feature}, cusum_neg_{feature} — накопители
          - anomaly_score  — максимум по всем накопителям всех метрик
          - is_anomaly     — True если score > h
          - anomaly_features
        """
        result   = df.copy()
        eps      = 1e-9
        score_cols = []

        for feat in self.cfg.features:
            if feat not in df.columns:
                continue

            roll = df[feat].rolling(window=self.cfg.window, min_periods=10)
            mu   = roll.mean().fillna(df[feat].mean())
            sig  = roll.std().fillna(1).replace(0, 1)

            values = df[feat].values
            mu_arr = mu.values
            sig_arr = sig.values
            k = self.cfg.k

            s_pos = np.zeros(len(df))
            s_neg = np.zeros(len(df))

            for t in range(1, len(df)):
                deviation = (values[t] - mu_arr[t]) / (sig_arr[t] + eps)
                s_pos[t] = max(0.0, s_pos[t-1] + deviation - k)
                s_neg[t] = max(0.0, s_neg[t-1] - deviation - k)

            col_pos = f"cusum_pos_{feat}"
            col_neg = f"cusum_neg_{feat}"
            result[col_pos] = s_pos
            result[col_neg] = s_neg
            score_cols.extend([col_pos, col_neg])

        # Anomaly score = максимум среди всех накопителей
        result["anomaly_score"] = result[score_cols].max(axis=1)
        result["is_anomaly"]    = result["anomaly_score"] > self.cfg.h

        def explain(row):
            triggered = []
            for feat in self.cfg.features:
                if feat not in df.columns:
                    continue
                pos = row.get(f"cusum_pos_{feat}", 0)
                neg = row.get(f"cusum_neg_{feat}", 0)
                if pos > self.cfg.h:
                    triggered.append(f"{feat}↑")
                if neg > self.cfg.h:
                    triggered.append(f"{feat}↓")
            return ", ".join(triggered)

        result["anomaly_features"] = result.apply(explain, axis=1)

        return result

    def summary(self, result: pd.DataFrame) -> dict:
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