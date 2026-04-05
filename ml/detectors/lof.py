"""
ml/detectors/lof.py

Local Outlier Factor (LOF) детектор — density-based метод.

Логика: для каждой точки вычисляет "локальную плотность" — насколько
близко расположены k ближайших соседей. Если локальная плотность точки
существенно ниже чем у её соседей — это аномалия.

Ключевое отличие от Isolation Forest: IF измеряет глобальную изоляцию
(сколько разрезов нужно чтобы отделить точку), а LOF сравнивает точку
с её ближайшим окружением. Это позволяет LOF находить аномалии в зонах
разной плотности — например, спад активности в период высокой нагрузки
будет аномалией относительно соседей, но может выглядеть нормально глобально.

Плюсы:  обнаруживает локальные аномалии, работает без предположений о распределении
Минусы: O(n * k) по памяти и времени, чувствителен к n_neighbors
"""

import numpy as np
import pandas as pd
from dataclasses import dataclass, field
from sklearn.neighbors import LocalOutlierFactor
from sklearn.preprocessing import StandardScaler


@dataclass
class LOFConfig:
    # Количество соседей для оценки локальной плотности.
    # 20 — баланс: слишком мало → шум, слишком много → потеря локальности.
    n_neighbors: int = 20

    # Ожидаемая доля аномалий (для калибровки порога).
    contamination: float = 0.05

    # Алгоритм поиска соседей. ball_tree быстрее для средних датасетов.
    algorithm: str = "ball_tree"

    # Метрика расстояния
    metric: str = "minkowski"

    random_state: int = 42

    feature_prefix_exclude: list[str] = field(
        default_factory=lambda: ["timestamp", "seqno", "z_", "cusum_"]
    )


class LOFDetector:
    """
    Обучается на всём датасете (unsupervised, novelty=False).
    LOF с novelty=False использует fit_predict — обучение и предсказание
    на одних и тех же данных. Это стандартный подход для batch anomaly detection.
    """

    def __init__(self, config: LOFConfig = None):
        self.cfg = config or LOFConfig()
        self.model = None
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

    def fit(self, df: pd.DataFrame) -> "LOFDetector":
        """Обучает LOF на переданном датасете."""
        self.feature_cols = self._select_features(df)
        X = df[self.feature_cols].values.astype(np.float32)
        X = self.scaler.fit_transform(X)

        self.model = LocalOutlierFactor(
            n_neighbors=self.cfg.n_neighbors,
            contamination=self.cfg.contamination,
            algorithm=self.cfg.algorithm,
            metric=self.cfg.metric,
            novelty=False,
            n_jobs=-1,
        )
        # fit_predict для novelty=False
        self._predictions = self.model.fit_predict(X)
        self._scores = -self.model.negative_outlier_factor_  # инвертируем: больше = аномальнее
        return self

    def detect(self, df: pd.DataFrame) -> pd.DataFrame:
        """
        Применяет LOF. Если модель не обучена — обучает на этих же данных.

        Anomaly score: LOF возвращает negative_outlier_factor_ (отрицательные,
        чем более отрицательное — тем аномальнее). Инвертируем знак для
        единообразия с другими детекторами (высокий score = аномалия).
        """
        if self.model is None:
            self.fit(df)

        result = df.copy()

        result["anomaly_score"] = self._scores
        result["is_anomaly"] = self._predictions == -1

        # Интерпретация: топ-3 признака с наибольшим отклонением от медианы
        medians = df[self.feature_cols].median()
        stds = df[self.feature_cols].std().replace(0, 1)

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
            "total_blocks": len(result),
            "anomaly_count": len(anomalies),
            "anomaly_rate_pct": round(len(anomalies) / len(result) * 100, 2),
            "max_score": round(result["anomaly_score"].max(), 3),
            "mean_score": round(result["anomaly_score"].mean(), 3),
            "n_neighbors": self.cfg.n_neighbors,
            "n_features": len(self.feature_cols),
            "top_features": (
                anomalies["anomaly_features"]
                .str.split(", ")
                .explode()
                .value_counts()
                .head(3)
                .to_dict()
            ),
        }
