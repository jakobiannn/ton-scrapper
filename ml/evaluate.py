"""
ml/evaluate.py

Пайплайн обучения, оценки и сравнения всех детекторов аномалий.

Шаги:
  1. Загрузка данных (parquet / TimescaleDB / synthetic)
  2. Feature engineering
  3. Запуск 5 индивидуальных детекторов + ensemble
  4. Сравнительная таблица
  5. Метрики качества (precision/recall/F1 при наличии ground truth)
  6. Сохранение результатов
  7. Визуализация (опционально)

Запуск:
    python -m ml.evaluate --synthetic                    # на синтетических данных
    python -m ml.evaluate --input ml/data/features.parquet
    python -m ml.evaluate --hours 72                     # из TimescaleDB
    python -m ml.evaluate --input data.parquet --labels ml/data/synthetic_labels.csv
"""

import argparse
import json
import logging
import os
import time
from collections import OrderedDict

import numpy as np
import pandas as pd

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
)
log = logging.getLogger("evaluate")


# ---------------------------------------------------------------------------
# Детекторы
# ---------------------------------------------------------------------------

def _get_detectors() -> OrderedDict:
    """Возвращает OrderedDict[name, (DetectorClass, ConfigClass)]."""
    from ml.detectors.zscore import ZScoreDetector, ZScoreConfig
    from ml.detectors.cusum import CUSUMDetector, CUSUMConfig
    from ml.detectors.isolation import IsolationForestDetector, IsolationForestConfig
    from ml.detectors.autoencoder import AutoencoderDetector, AutoencoderConfig
    from ml.detectors.lof import LOFDetector, LOFConfig

    return OrderedDict([
        ("zscore",      (ZScoreDetector,          ZScoreConfig)),
        ("cusum",       (CUSUMDetector,           CUSUMConfig)),
        ("isolation",   (IsolationForestDetector,  IsolationForestConfig)),
        ("autoencoder", (AutoencoderDetector,      AutoencoderConfig)),
        ("lof",         (LOFDetector,             LOFConfig)),
    ])


# ---------------------------------------------------------------------------
# Загрузка данных
# ---------------------------------------------------------------------------

def load_features(
    parquet_path: str = None,
    hours: int = 24,
    synthetic: bool = False,
    synthetic_blocks: int = 10_000,
) -> tuple:
    """
    Загружает feature matrix и (опционально) labels.

    Returns:
        (features_df, labels_df or None)
    """
    labels_df = None

    if synthetic:
        log.info("Генерация синтетических данных: %d блоков", synthetic_blocks)
        from ml.data.generator import SyntheticBlockGenerator, GeneratorConfig
        gen = SyntheticBlockGenerator(GeneratorConfig(n_blocks=synthetic_blocks))
        blocks_df, labels_df = gen.generate_dataset()

        # Применяем feature engineering
        features_df = _build_features_from_df(blocks_df)
        return features_df, labels_df

    if parquet_path and os.path.exists(parquet_path):
        log.info("Загрузка из parquet: %s", parquet_path)
        features_df = pd.read_parquet(parquet_path)

        # Пытаемся найти labels рядом
        labels_path = os.path.join(os.path.dirname(parquet_path), "synthetic_labels.csv")
        if os.path.exists(labels_path) and labels_path.endswith(".csv"):
            labels_df = pd.read_csv(labels_path)
            log.info("Labels загружены: %s (%d записей)", labels_path, len(labels_df))

        return features_df, labels_df

    # Попытка загрузки из TimescaleDB
    try:
        log.info("Загрузка из TimescaleDB (последние %d часов)", hours)
        from ml.features.builder import FeatureBuilder, Config
        builder = FeatureBuilder(Config())
        features_df = builder.build(hours=hours)
        return features_df, None
    except Exception as e:
        log.warning("Не удалось загрузить из БД: %s", e)

    # Fallback: известные пути parquet
    for fallback in ["ml/data/features.parquet", "ml/results/features.parquet"]:
        if os.path.exists(fallback):
            log.info("Fallback загрузка: %s", fallback)
            return pd.read_parquet(fallback), None

    raise RuntimeError(
        "Нет данных для оценки. Используйте --synthetic, --input или запустите TimescaleDB."
    )


def load_labels(path: str) -> pd.DataFrame:
    """Загружает ground truth labels."""
    df = pd.read_csv(path)
    log.info("Labels: %d записей, %d аномалий", len(df), df["is_anomaly"].sum())
    return df


def _build_features_from_df(blocks_df: pd.DataFrame) -> pd.DataFrame:
    """Строит features из raw blocks DataFrame (без обращения к БД)."""
    from ml.features.builder import FeatureBuilder, Config, RAW_FEATURES, ROLLING_FEATURES

    builder = FeatureBuilder(Config())

    # Убеждаемся что все нужные колонки есть
    for col in RAW_FEATURES:
        if col not in blocks_df.columns:
            blocks_df[col] = 0

    df = blocks_df.copy()
    df = builder.add_rolling_features(df)
    df = builder.add_delta_features(df)
    df = builder.add_zscore_context(df)
    df = df.fillna(0)

    log.info("Features built: %d blocks x %d features", len(df), len(df.columns))
    return df


# ---------------------------------------------------------------------------
# Запуск детекторов
# ---------------------------------------------------------------------------

def run_all_detectors(df: pd.DataFrame) -> tuple[dict[str, pd.DataFrame], dict[str, object]]:
    """
    Запускает все 5 индивидуальных детекторов + ensemble.

    Returns:
        (results, trained_detectors)
        results: dict[name, result_DataFrame]
        trained_detectors: dict[name, detector_instance]
    """
    detectors = _get_detectors()
    results = {}
    trained = {}

    for name, (cls, cfg_cls) in detectors.items():
        log.info("=" * 60)
        log.info("Запуск детектора: %s", name)
        t0 = time.time()

        detector = cls(cfg_cls())
        result = detector.detect(df)
        elapsed = time.time() - t0

        summary = detector.summary(result)
        results[name] = result
        trained[name] = detector

        log.info("  Время: %.2fs", elapsed)
        log.info("  Результат: %d аномалий / %d блоков (%.1f%%)",
                 summary["anomaly_count"], summary["total_blocks"],
                 summary["anomaly_rate_pct"])
        log.info("  Top features: %s", summary.get("top_features", {}))

    # Ensemble
    log.info("=" * 60)
    log.info("Запуск Ensemble детектора")
    from ml.detectors.ensemble import EnsembleDetector
    ensemble = EnsembleDetector()
    ensemble_result = ensemble.detect(results)
    ensemble_summary = ensemble.summary(ensemble_result)

    results["ensemble"] = ensemble_result
    trained["ensemble"] = ensemble

    log.info("  Результат: %d аномалий (%.1f%%), vote distribution: %s",
             ensemble_summary["anomaly_count"],
             ensemble_summary["anomaly_rate_pct"],
             ensemble_summary.get("vote_distribution", {}))

    return results, trained


# ---------------------------------------------------------------------------
# Сравнительная таблица
# ---------------------------------------------------------------------------

def build_comparison(results: dict[str, pd.DataFrame], df: pd.DataFrame) -> pd.DataFrame:
    """Строит сводную таблицу: timestamp, seqno + per-model score/anomaly + votes."""
    comparison = pd.DataFrame()

    if "timestamp" in df.columns:
        comparison["timestamp"] = df["timestamp"].values
    if "seqno" in df.columns:
        comparison["seqno"] = df["seqno"].values

    individual_models = [n for n in results if n != "ensemble"]

    for name in results:
        r = results[name]
        comparison[f"{name}_score"] = r["anomaly_score"].values
        comparison[f"{name}_anomaly"] = r["is_anomaly"].values.astype(bool)

    # Голоса (только индивидуальные модели)
    anomaly_cols = [f"{n}_anomaly" for n in individual_models]
    comparison["total_votes"] = comparison[anomaly_cols].sum(axis=1).astype(int)
    comparison["any_anomaly"] = comparison["total_votes"] > 0
    comparison["majority_anomaly"] = comparison["total_votes"] >= 3

    return comparison


# ---------------------------------------------------------------------------
# Метрики качества
# ---------------------------------------------------------------------------

def compute_metrics(
    results: dict[str, pd.DataFrame],
    comparison: pd.DataFrame,
    labels: pd.DataFrame = None,
) -> dict:
    """
    Вычисляет метрики качества для всех моделей.

    С ground truth labels: precision, recall, F1, accuracy.
    Без labels: pseudo-F1 (ensemble как pseudo-truth), agreement matrix.
    """
    metrics = {
        "per_model": {},
        "agreement_matrix": {},
        "score_stats": {},
    }

    individual_models = [n for n in results if n != "ensemble"]
    all_models = list(results.keys())

    # --- Per-model statistics ---
    for name in all_models:
        r = results[name]
        anomalies = r[r["is_anomaly"]]
        scores = r["anomaly_score"]

        metrics["per_model"][name] = {
            "anomaly_count": int(len(anomalies)),
            "anomaly_rate_pct": round(len(anomalies) / len(r) * 100, 2),
            "mean_score": round(float(scores.mean()), 4),
            "max_score": round(float(scores.max()), 4),
            "p50": round(float(scores.quantile(0.5)), 4),
            "p95": round(float(scores.quantile(0.95)), 4),
            "p99": round(float(scores.quantile(0.99)), 4),
        }

    # --- Agreement matrix (Jaccard similarity) ---
    for m1 in all_models:
        metrics["agreement_matrix"][m1] = {}
        set1 = set(results[m1][results[m1]["is_anomaly"]].index)
        for m2 in all_models:
            set2 = set(results[m2][results[m2]["is_anomaly"]].index)
            if not set1 and not set2:
                jaccard = 1.0
            elif not set1 or not set2:
                jaccard = 0.0
            else:
                jaccard = len(set1 & set2) / len(set1 | set2)
            metrics["agreement_matrix"][m1][m2] = round(jaccard, 4)

    # --- Score statistics ---
    for name in all_models:
        scores = results[name]["anomaly_score"]
        metrics["score_stats"][name] = {
            "mean": round(float(scores.mean()), 4),
            "std": round(float(scores.std()), 4),
            "min": round(float(scores.min()), 4),
            "max": round(float(scores.max()), 4),
            "p25": round(float(scores.quantile(0.25)), 4),
            "p75": round(float(scores.quantile(0.75)), 4),
        }

    # --- Ground truth metrics (if labels available) ---
    if labels is not None and "is_anomaly" in labels.columns:
        metrics["ground_truth"] = {}
        y_true = labels["is_anomaly"].values[:len(comparison)]

        for name in all_models:
            y_pred = results[name]["is_anomaly"].values[:len(y_true)]

            tp = int(np.sum(y_true & y_pred))
            fp = int(np.sum(~y_true & y_pred))
            fn = int(np.sum(y_true & ~y_pred))
            tn = int(np.sum(~y_true & ~y_pred))

            precision = tp / max(tp + fp, 1)
            recall = tp / max(tp + fn, 1)
            f1 = 2 * precision * recall / max(precision + recall, 1e-9)
            accuracy = (tp + tn) / max(tp + fp + fn + tn, 1)

            metrics["ground_truth"][name] = {
                "precision": round(precision, 4),
                "recall": round(recall, 4),
                "f1": round(f1, 4),
                "accuracy": round(accuracy, 4),
                "tp": tp, "fp": fp, "fn": fn, "tn": tn,
            }

        # Per anomaly type breakdown
        if "anomaly_type" in labels.columns:
            metrics["per_anomaly_type"] = {}
            for atype in labels[labels["is_anomaly"]]["anomaly_type"].unique():
                type_mask = (labels["anomaly_type"] == atype).values[:len(comparison)]
                metrics["per_anomaly_type"][atype] = {}

                for name in all_models:
                    y_pred = results[name]["is_anomaly"].values[:len(type_mask)]
                    detected = int(np.sum(type_mask & y_pred))
                    total = int(np.sum(type_mask))
                    metrics["per_anomaly_type"][atype][name] = {
                        "detected": detected,
                        "total": total,
                        "recall": round(detected / max(total, 1), 4),
                    }
    else:
        # Pseudo-F1 using ensemble majority as ground truth
        metrics["pseudo_f1"] = {}
        y_pseudo = comparison["majority_anomaly"].values

        for name in individual_models:
            y_pred = results[name]["is_anomaly"].values[:len(y_pseudo)]

            tp = int(np.sum(y_pseudo & y_pred))
            fp = int(np.sum(~y_pseudo & y_pred))
            fn = int(np.sum(y_pseudo & ~y_pred))

            precision = tp / max(tp + fp, 1)
            recall = tp / max(tp + fn, 1)
            f1 = 2 * precision * recall / max(precision + recall, 1e-9)

            metrics["pseudo_f1"][name] = {
                "precision": round(precision, 4),
                "recall": round(recall, 4),
                "f1": round(f1, 4),
            }

    # --- Feature importance ---
    metrics["feature_importance"] = {}
    for name in all_models:
        r = results[name]
        anomalies = r[r["is_anomaly"]]
        if len(anomalies) > 0 and "anomaly_features" in anomalies.columns:
            feat_counts = (
                anomalies["anomaly_features"]
                .str.split(", ")
                .explode()
                .value_counts()
                .head(10)
                .to_dict()
            )
            metrics["feature_importance"][name] = feat_counts

    # --- Stability (anomaly rate variance across windows) ---
    metrics["stability"] = {}
    window_size = min(500, len(comparison) // 5) if len(comparison) > 100 else len(comparison)
    if window_size > 10:
        for name in all_models:
            anomaly_col = results[name]["is_anomaly"].astype(float)
            rates = anomaly_col.rolling(window=window_size).mean().dropna()
            if len(rates) > 0:
                metrics["stability"][name] = {
                    "mean_rate": round(float(rates.mean()), 4),
                    "std_rate": round(float(rates.std()), 4),
                    "min_rate": round(float(rates.min()), 4),
                    "max_rate": round(float(rates.max()), 4),
                }

    return metrics


# ---------------------------------------------------------------------------
# Сохранение
# ---------------------------------------------------------------------------

def save_results(
    features_df: pd.DataFrame,
    results: dict[str, pd.DataFrame],
    comparison: pd.DataFrame,
    metrics: dict,
    output_dir: str,
):
    """Сохраняет все результаты на диск."""
    os.makedirs(output_dir, exist_ok=True)
    os.makedirs(os.path.join(output_dir, "plots"), exist_ok=True)

    # Features
    features_df.to_parquet(os.path.join(output_dir, "features.parquet"), index=False)

    # Per-model results
    for name, r in results.items():
        cols = ["anomaly_score", "is_anomaly", "anomaly_features"]
        if "timestamp" in r.columns:
            cols = ["timestamp"] + cols
        if "seqno" in r.columns:
            cols = ["seqno"] + cols
        if "vote_count" in r.columns:
            cols += ["vote_count", "voters"]

        save_cols = [c for c in cols if c in r.columns]
        r[save_cols].to_csv(os.path.join(output_dir, f"anomalies_{name}.csv"), index=False)

    # Comparison
    comparison.to_csv(os.path.join(output_dir, "comparison.csv"), index=False)

    # Metrics
    with open(os.path.join(output_dir, "metrics.json"), "w") as f:
        json.dump(metrics, f, indent=2, default=str)

    log.info("Результаты сохранены в %s", output_dir)


# ---------------------------------------------------------------------------
# Вывод результатов
# ---------------------------------------------------------------------------

def print_summary(metrics: dict):
    """Печатает красивую сводку в консоль."""
    print("\n" + "=" * 70)
    print("  СВОДКА РЕЗУЛЬТАТОВ")
    print("=" * 70)

    # Per-model
    print(f"\n{'Модель':<15} {'Аномалий':>10} {'Rate %':>8} {'Mean':>8} {'Max':>8} {'P95':>8}")
    print("-" * 65)
    for name, stats in metrics["per_model"].items():
        print(f"{name:<15} {stats['anomaly_count']:>10} {stats['anomaly_rate_pct']:>7.1f}% "
              f"{stats['mean_score']:>8.4f} {stats['max_score']:>8.4f} {stats['p95']:>8.4f}")

    # Ground truth
    if "ground_truth" in metrics:
        print(f"\n{'Модель':<15} {'Precision':>10} {'Recall':>10} {'F1':>10} {'Accuracy':>10}")
        print("-" * 55)
        for name, gt in metrics["ground_truth"].items():
            print(f"{name:<15} {gt['precision']:>10.4f} {gt['recall']:>10.4f} "
                  f"{gt['f1']:>10.4f} {gt['accuracy']:>10.4f}")

        # Per anomaly type
        if "per_anomaly_type" in metrics:
            print("\nRecall по типам аномалий:")
            types = list(metrics["per_anomaly_type"].keys())
            models = list(metrics["ground_truth"].keys())
            header = f"{'Тип':<18}" + "".join(f"{m:>12}" for m in models)
            print(header)
            print("-" * len(header))
            for atype in types:
                row = f"{atype:<18}"
                for m in models:
                    data = metrics["per_anomaly_type"][atype].get(m, {})
                    recall = data.get("recall", 0)
                    total = data.get("total", 0)
                    row += f"{recall:>8.2f}({total:>2})"
                print(row)

    # Agreement matrix
    if "agreement_matrix" in metrics:
        print("\nМатрица согласия (Jaccard):")
        models = list(metrics["agreement_matrix"].keys())
        header = f"{'':>12}" + "".join(f"{m:>12}" for m in models)
        print(header)
        for m1 in models:
            row = f"{m1:>12}"
            for m2 in models:
                row += f"{metrics['agreement_matrix'][m1][m2]:>12.3f}"
            print(row)

    print("\n" + "=" * 70)


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(description="TON anomaly detection evaluation")
    parser.add_argument("--input", type=str, default=None, help="Путь к features.parquet")
    parser.add_argument("--hours", type=int, default=24, help="Часы данных из TimescaleDB")
    parser.add_argument("--synthetic", action="store_true", help="Использовать синтетические данные")
    parser.add_argument("--synthetic-blocks", type=int, default=10_000, help="Кол-во синт. блоков")
    parser.add_argument("--labels", type=str, default=None, help="Путь к labels CSV")
    parser.add_argument("--output", type=str, default="ml/results", help="Директория результатов")
    parser.add_argument("--save-models", action="store_true", help="Сохранить обученные модели")
    parser.add_argument("--no-viz", action="store_true", help="Пропустить генерацию графиков")
    args = parser.parse_args()

    # 1. Загрузка данных
    features_df, labels_df = load_features(
        parquet_path=args.input,
        hours=args.hours,
        synthetic=args.synthetic,
        synthetic_blocks=args.synthetic_blocks,
    )

    if args.labels:
        labels_df = load_labels(args.labels)

    log.info("Данные загружены: %d blocks x %d features", len(features_df), len(features_df.columns))

    # 2. Запуск детекторов
    results, trained = run_all_detectors(features_df)

    # 3. Сравнительная таблица
    comparison = build_comparison(results, features_df)

    # 4. Метрики
    metrics = compute_metrics(results, comparison, labels_df)

    # 5. Вывод
    print_summary(metrics)

    # 6. Сохранение
    save_results(features_df, results, comparison, metrics, args.output)

    # 7. Сохранение моделей
    if args.save_models:
        from ml.models.saver import save_all_models
        model_dir = os.path.join(args.output, "trained")
        # Не сохраняем ensemble — он не имеет собственной модели
        individual = {k: v for k, v in trained.items() if k != "ensemble"}
        save_all_models(individual, model_dir)

    # 8. Визуализация
    if not args.no_viz:
        try:
            from ml.visualize import generate_all_plots
            generate_all_plots(results, comparison, metrics, labels_df, args.output)
        except Exception as e:
            log.warning("Ошибка генерации графиков: %s", e)

    log.info("Оценка завершена. Результаты: %s", args.output)


if __name__ == "__main__":
    main()
