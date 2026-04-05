"""
ml/detectors/ensemble.py

Ensemble детектор — объединяет результаты всех индивидуальных моделей
через majority voting.

Научный вклад: демонстрирует что комбинация разнородных подходов
(статистических, density-based, tree-based, нейросетевых) даёт более
робастное обнаружение аномалий, снижая false positive от индивидуальных
bias'ов каждой модели.

Два режима голосования:
  - uniform:         каждая модель = 1 голос
  - score_weighted:  вес голоса пропорционален нормализованному score модели

Порог: блок аномален если >= min_votes моделей проголосовали за аномалию.
"""

import numpy as np
import pandas as pd
from dataclasses import dataclass


@dataclass
class EnsembleConfig:
    # Минимальное количество моделей которые должны согласиться.
    # 3 из 5 = majority (60%). Баланс между чувствительностью и точностью.
    min_votes: int = 3

    # Режим агрегации scores:
    #   "uniform"        — простое голосование (is_anomaly count)
    #   "score_weighted"  — взвешенное усреднение нормализованных scores
    weight_mode: str = "uniform"


class EnsembleDetector:
    """
    Принимает dict результатов от индивидуальных детекторов.
    Каждое значение — DataFrame с колонками is_anomaly, anomaly_score, anomaly_features.
    """

    def __init__(self, config: EnsembleConfig = None):
        self.cfg = config or EnsembleConfig()

    @staticmethod
    def _normalize_scores(scores: pd.Series) -> pd.Series:
        """Min-max нормализация в [0, 1]. Защита от константных значений."""
        smin, smax = scores.min(), scores.max()
        if smax - smin < 1e-12:
            return pd.Series(np.zeros(len(scores)), index=scores.index)
        return (scores - smin) / (smax - smin)

    def detect(self, individual_results: dict[str, pd.DataFrame]) -> pd.DataFrame:
        """
        Объединяет результаты индивидуальных детекторов.

        Args:
            individual_results: dict[model_name, DataFrame]
                Каждый DataFrame должен содержать: is_anomaly, anomaly_score, anomaly_features

        Returns:
            DataFrame с колонками:
              - vote_count:       сколько моделей проголосовали за аномалию
              - voters:           имена моделей-голосователей
              - anomaly_score:    агрегированный score
              - is_anomaly:       vote_count >= min_votes
              - anomaly_features: объединённые features от голосовавших моделей
              - {model}_score:    нормализованный score каждой модели
              - {model}_anomaly:  is_anomaly каждой модели
        """
        if not individual_results:
            raise ValueError("individual_results пуст — нет моделей для ансамбля")

        # Берём первый результат как основу (timestamp, seqno, raw features)
        first_name = next(iter(individual_results))
        first_df = individual_results[first_name]
        n = len(first_df)

        result = pd.DataFrame(index=range(n))
        if "timestamp" in first_df.columns:
            result["timestamp"] = first_df["timestamp"].values
        if "seqno" in first_df.columns:
            result["seqno"] = first_df["seqno"].values

        # Собираем голоса и scores
        vote_matrix = pd.DataFrame(index=range(n))
        score_matrix = pd.DataFrame(index=range(n))
        features_map = {}  # model_name -> anomaly_features Series

        for name, df in individual_results.items():
            if len(df) != n:
                raise ValueError(
                    f"Размер результата '{name}' ({len(df)}) не совпадает "
                    f"с первым ({n})"
                )

            vote_matrix[name] = df["is_anomaly"].values.astype(bool)
            normalized = self._normalize_scores(df["anomaly_score"])
            score_matrix[name] = normalized.values
            features_map[name] = df["anomaly_features"].values

            # Per-model columns
            result[f"{name}_score"] = normalized.values
            result[f"{name}_anomaly"] = df["is_anomaly"].values.astype(bool)

        # Vote count
        result["vote_count"] = vote_matrix.sum(axis=1).astype(int)

        # Voters
        model_names = list(individual_results.keys())

        def get_voters(row_idx):
            return ", ".join(
                name for name in model_names
                if vote_matrix.iloc[row_idx][name]
            )

        result["voters"] = [get_voters(i) for i in range(n)]

        # Aggregated score
        if self.cfg.weight_mode == "score_weighted":
            # Средний нормализованный score по ВСЕМ моделям
            result["anomaly_score"] = score_matrix.mean(axis=1)
        else:
            # uniform: средний score только от голосовавших за аномалию
            def avg_voter_score(row_idx):
                voter_scores = [
                    score_matrix.iloc[row_idx][name]
                    for name in model_names
                    if vote_matrix.iloc[row_idx][name]
                ]
                return np.mean(voter_scores) if voter_scores else 0.0

            result["anomaly_score"] = [avg_voter_score(i) for i in range(n)]

        # Is anomaly
        result["is_anomaly"] = result["vote_count"] >= self.cfg.min_votes

        # Anomaly features: union от голосующих моделей
        def merge_features(row_idx):
            all_features = []
            for name in model_names:
                if vote_matrix.iloc[row_idx][name]:
                    feats = features_map[name][row_idx]
                    if feats:
                        all_features.extend(f.strip() for f in str(feats).split(","))
            # Уникальные, сохраняя порядок
            seen = set()
            unique = []
            for f in all_features:
                if f and f not in seen:
                    seen.add(f)
                    unique.append(f)
            return ", ".join(unique[:5])  # топ-5 уникальных

        result["anomaly_features"] = [merge_features(i) for i in range(n)]

        return result

    def summary(self, result: pd.DataFrame) -> dict:
        anomalies = result[result["is_anomaly"]]

        # Статистика по голосам
        vote_dist = result["vote_count"].value_counts().sort_index().to_dict()

        return {
            "total_blocks": len(result),
            "anomaly_count": len(anomalies),
            "anomaly_rate_pct": round(len(anomalies) / len(result) * 100, 2),
            "min_votes": self.cfg.min_votes,
            "max_score": round(result["anomaly_score"].max(), 3),
            "mean_score": round(result["anomaly_score"].mean(), 3),
            "vote_distribution": vote_dist,
            "top_features": (
                anomalies["anomaly_features"]
                .str.split(", ")
                .explode()
                .value_counts()
                .head(5)
                .to_dict()
            ) if len(anomalies) > 0 else {},
        }
