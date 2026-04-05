"""
ml/detectors/isolation.py

Isolation Forest детектор — ML-метод для многомерных аномалий.

Логика: строит случайные деревья решений. Нормальные точки окружены
похожими — их сложно изолировать, нужно много разрезов (глубокое дерево).
Аномальные точки стоят особняком — изолируются быстро (мелкое дерево).
Anomaly score = обратная средняя глубина изоляции по всем деревьям.

Плюсы:  видит комбинированные аномалии, не нужна разметка,
        устойчив к масштабу признаков
Минусы: не интерпретирует какой конкретно признак виноват,
        contamination нужно подбирать
"""

import numpy as np
import pandas as pd
from dataclasses import dataclass, field
from sklearn.ensemble import IsolationForest
from sklearn.preprocessing import StandardScaler


@dataclass
class IsolationForestConfig:
    # Ожидаемая доля аномалий в данных.
    # 0.05 = 5% — консервативная оценка для блокчейна в обычное время.
    # Если научник скажет "слишком много аномалий" — увеличь до 0.02.
    contamination: float = 0.05

    # Количество деревьев. 200 — хороший баланс точности и скорости.
    n_estimators: int = 200

    # Размер подвыборки для каждого дерева.
    # 256 — рекомендация авторов Isolation Forest (Liu et al., 2008).
    max_samples: int = 256

    random_state: int = 42

    # Признаки для модели — все rolling и delta варианты из FeatureBuilder.
    # None = брать все колонки кроме timestamp, seqno, служебных.
    feature_prefix_exclude: list[str] = field(
        default_factory=lambda: ["timestamp", "seqno", "z_", "cusum_"]
    )


class IsolationForestDetector:
    """
    Обучается на всём датасете (unsupervised).
    Предполагаем что большинство блоков нормальные —
    модель сама выделит outliers на основе contamination.
    """

    def __init__(self, config: IsolationForestConfig = None):
        self.cfg    = config or IsolationForestConfig()
        self.model  = None
        self.scaler = StandardScaler()
        self.feature_cols: list[str] = []

    def _select_features(self, df: pd.DataFrame) -> list[str]:
        """Отбирает признаки: все числовые кроме служебных."""
        exclude = set(self.cfg.feature_prefix_exclude)
        cols = []
        for col in df.select_dtypes(include=[np.number]).columns:
            if not any(col.startswith(e) for e in exclude):
                cols.append(col)
        return cols

    def fit(self, df: pd.DataFrame) -> "IsolationForestDetector":
        """
        Обучает модель на переданном датасете.
        В production: обучаем на исторических "нормальных" данных,
        затем применяем на новых блоках через detect().
        """
        self.feature_cols = self._select_features(df)
        X = df[self.feature_cols].values.astype(np.float32)
        X = self.scaler.fit_transform(X)

        self.model = IsolationForest(
            n_estimators=self.cfg.n_estimators,
            max_samples=self.cfg.max_samples,
            contamination=self.cfg.contamination,
            random_state=self.cfg.random_state,
            n_jobs=-1,  # использовать все CPU
        )
        self.model.fit(X)
        return self

    def detect(self, df: pd.DataFrame) -> pd.DataFrame:
        """
        Применяет обученную модель.
        Если модель ещё не обучена — обучает на тех же данных (fit_detect).

        Anomaly score: sklearn возвращает отрицательные значения,
        мы инвертируем знак чтобы высокий score = более аномальный.
        Порог 0: score > 0 → аномалия (соответствует contamination).
        """
        if self.model is None:
            self.fit(df)

        result = df.copy()
        X = df[self.feature_cols].values.astype(np.float32)
        X = self.scaler.transform(X)

        # score_samples возвращает log-likelihood: чем меньше, тем аномальнее
        # Инвертируем: anomaly_score > 0 означает аномалию
        raw_scores = self.model.score_samples(X)
        result["anomaly_score"] = -raw_scores  # инвертируем знак

        # predict: -1 = аномалия, 1 = норма
        predictions = self.model.predict(X)
        result["is_anomaly"] = predictions == -1

        # Для интерпретации: топ-признаки с наибольшим отклонением от медианы
        medians = df[self.feature_cols].median()
        stds    = df[self.feature_cols].std().replace(0, 1)

        def explain(row):
            deviations = {
                col: abs(row[col] - medians[col]) / stds[col]
                for col in self.feature_cols
            }
            top3 = sorted(deviations, key=deviations.get, reverse=True)[:3]
            return ", ".join(top3)

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
            "n_features":       len(self.feature_cols),
            "top_features":     (
                anomalies["anomaly_features"]
                .str.split(", ")
                .explode()
                .value_counts()
                .head(3)
                .to_dict()
            ),
        }