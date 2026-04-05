"""
ml/visualize.py

Визуализация результатов детекции аномалий.

Генерирует 6 типов графиков:
  1. anomaly_timeline.png   — временная шкала anomaly scores по моделям
  2. score_distributions.png — гистограммы распределения scores
  3. model_agreement.png    — heatmap Jaccard similarity
  4. confusion_matrices.png — confusion matrices vs ground truth
  5. feature_importance.png — top-10 фич по каждой модели
  6. roc_curves.png         — ROC кривые с AUC

Запуск:
    python -m ml.visualize --input ml/results
"""

import argparse
import json
import logging
import os

import numpy as np
import pandas as pd

# Headless backend (без дисплея — Docker, SSH)
import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt
import matplotlib.patches as mpatches
import seaborn as sns

logging.basicConfig(level=logging.INFO, format="%(asctime)s [%(levelname)s] %(name)s: %(message)s")
log = logging.getLogger("visualize")

# Стиль
sns.set_style("whitegrid")
plt.rcParams.update({
    "figure.figsize": (16, 10),
    "font.size": 11,
    "axes.titlesize": 13,
    "axes.labelsize": 11,
})

COLORS = {
    "zscore": "#2196F3",
    "cusum": "#FF9800",
    "isolation": "#4CAF50",
    "autoencoder": "#9C27B0",
    "lof": "#F44336",
    "ensemble": "#795548",
}


# ---------------------------------------------------------------------------
# 1. Anomaly Timeline
# ---------------------------------------------------------------------------

def plot_anomaly_timeline(
    results: dict[str, pd.DataFrame],
    labels: pd.DataFrame = None,
    output_path: str = "ml/results/plots/anomaly_timeline.png",
):
    """6 subplots: anomaly_score линия + красные зоны аномалий + зелёный ground truth."""
    models = list(results.keys())
    n_models = len(models)
    fig, axes = plt.subplots(n_models, 1, figsize=(18, 3 * n_models), sharex=True)

    if n_models == 1:
        axes = [axes]

    for ax, name in zip(axes, models):
        r = results[name]
        x = range(len(r))
        scores = r["anomaly_score"].values
        anomalies = r["is_anomaly"].values

        color = COLORS.get(name, "#666666")
        ax.plot(x, scores, color=color, alpha=0.7, linewidth=0.5, label="score")

        # Красные зоны аномалий
        anomaly_idx = np.where(anomalies)[0]
        if len(anomaly_idx) > 0:
            for idx in anomaly_idx:
                ax.axvspan(idx - 0.5, idx + 0.5, alpha=0.15, color="red", linewidth=0)

        # Ground truth (зелёные маркеры)
        if labels is not None and "is_anomaly" in labels.columns:
            gt = labels["is_anomaly"].values[:len(r)]
            gt_idx = np.where(gt)[0]
            if len(gt_idx) > 0:
                for idx in gt_idx:
                    ax.axvspan(idx - 0.5, idx + 0.5, alpha=0.1, color="green", linewidth=0)

        n_anom = int(np.sum(anomalies))
        ax.set_ylabel("Score")
        ax.set_title(f"{name} ({n_anom} anomalies)")
        ax.legend(loc="upper right", fontsize=8)

    axes[-1].set_xlabel("Block index")
    fig.suptitle("Anomaly Detection Timeline", fontsize=15, y=1.01)
    plt.tight_layout()
    plt.savefig(output_path, dpi=150, bbox_inches="tight")
    plt.close()
    log.info("Saved: %s", output_path)


# ---------------------------------------------------------------------------
# 2. Score Distributions
# ---------------------------------------------------------------------------

def plot_score_distributions(
    results: dict[str, pd.DataFrame],
    output_path: str = "ml/results/plots/score_distributions.png",
):
    """2x3 гистограммы с KDE."""
    models = list(results.keys())
    n = len(models)
    rows = (n + 2) // 3
    fig, axes = plt.subplots(rows, 3, figsize=(16, 4 * rows))
    axes = axes.flatten() if n > 1 else [axes]

    for i, name in enumerate(models):
        ax = axes[i]
        scores = results[name]["anomaly_score"].values
        anomalies = results[name]["is_anomaly"].values
        color = COLORS.get(name, "#666666")

        # Отделяем нормальные и аномальные для разных цветов
        normal_scores = scores[~anomalies]
        anomaly_scores = scores[anomalies]

        ax.hist(normal_scores, bins=50, alpha=0.6, color=color, label="Normal", density=True)
        if len(anomaly_scores) > 0:
            ax.hist(anomaly_scores, bins=30, alpha=0.6, color="red", label="Anomaly", density=True)

        rate = np.mean(anomalies) * 100
        ax.set_title(f"{name} (anomaly rate: {rate:.1f}%)")
        ax.set_xlabel("Anomaly Score")
        ax.legend(fontsize=8)

    # Скрываем пустые subplots
    for i in range(n, len(axes)):
        axes[i].set_visible(False)

    fig.suptitle("Score Distributions", fontsize=15)
    plt.tight_layout()
    plt.savefig(output_path, dpi=150, bbox_inches="tight")
    plt.close()
    log.info("Saved: %s", output_path)


# ---------------------------------------------------------------------------
# 3. Model Agreement Heatmap
# ---------------------------------------------------------------------------

def plot_model_agreement(
    metrics: dict,
    output_path: str = "ml/results/plots/model_agreement.png",
):
    """Heatmap Jaccard similarity."""
    agreement = metrics.get("agreement_matrix", {})
    if not agreement:
        log.warning("No agreement matrix in metrics")
        return

    models = list(agreement.keys())
    matrix = np.array([[agreement[m1][m2] for m2 in models] for m1 in models])

    fig, ax = plt.subplots(figsize=(10, 8))
    sns.heatmap(
        matrix,
        xticklabels=models,
        yticklabels=models,
        annot=True,
        fmt=".3f",
        cmap="YlOrRd",
        vmin=0, vmax=1,
        ax=ax,
    )
    ax.set_title("Model Agreement (Jaccard Similarity)")
    plt.tight_layout()
    plt.savefig(output_path, dpi=150, bbox_inches="tight")
    plt.close()
    log.info("Saved: %s", output_path)


# ---------------------------------------------------------------------------
# 4. Confusion Matrices
# ---------------------------------------------------------------------------

def plot_confusion_matrices(
    metrics: dict,
    output_path: str = "ml/results/plots/confusion_matrices.png",
):
    """2x3 confusion matrices vs ground truth."""
    gt_metrics = metrics.get("ground_truth", metrics.get("pseudo_f1", {}))
    if not gt_metrics:
        log.warning("No ground truth or pseudo-F1 metrics for confusion matrices")
        return

    source = "ground_truth" if "ground_truth" in metrics else "pseudo_f1"
    models = list(gt_metrics.keys())
    n = len(models)
    rows = max(1, (n + 2) // 3)
    fig, axes = plt.subplots(rows, 3, figsize=(15, 5 * rows))
    axes = axes.flatten() if n > 1 else [axes]

    for i, name in enumerate(models):
        ax = axes[i]
        data = gt_metrics[name]

        if "tp" in data:
            cm = np.array([[data["tn"], data["fp"]],
                           [data["fn"], data["tp"]]])
        else:
            # pseudo_f1 doesn't have tp/fp/fn/tn, skip
            ax.text(0.5, 0.5, f"{name}\nP={data['precision']:.3f}\nR={data['recall']:.3f}\nF1={data['f1']:.3f}",
                    ha="center", va="center", fontsize=14, transform=ax.transAxes)
            ax.set_title(name)
            continue

        sns.heatmap(
            cm, annot=True, fmt="d", cmap="Blues",
            xticklabels=["Normal", "Anomaly"],
            yticklabels=["Normal", "Anomaly"],
            ax=ax,
        )
        ax.set_xlabel("Predicted")
        ax.set_ylabel("Actual")

        p, r, f1 = data["precision"], data["recall"], data["f1"]
        ax.set_title(f"{name}\nP={p:.3f} R={r:.3f} F1={f1:.3f}")

    for i in range(n, len(axes)):
        axes[i].set_visible(False)

    fig.suptitle(f"Confusion Matrices ({source})", fontsize=15)
    plt.tight_layout()
    plt.savefig(output_path, dpi=150, bbox_inches="tight")
    plt.close()
    log.info("Saved: %s", output_path)


# ---------------------------------------------------------------------------
# 5. Feature Importance
# ---------------------------------------------------------------------------

def plot_feature_importance(
    metrics: dict,
    output_path: str = "ml/results/plots/feature_importance.png",
):
    """Horizontal bar chart top-10 фич по модели."""
    fi = metrics.get("feature_importance", {})
    if not fi:
        log.warning("No feature importance data")
        return

    models = [m for m in fi if fi[m]]
    if not models:
        return

    n = len(models)
    fig, axes = plt.subplots(1, n, figsize=(5 * n, 8))
    if n == 1:
        axes = [axes]

    for ax, name in zip(axes, models):
        features = fi[name]
        if not features:
            continue

        names = list(features.keys())[:10]
        counts = [features[n] for n in names]
        color = COLORS.get(name, "#666666")

        ax.barh(range(len(names)), counts, color=color, alpha=0.8)
        ax.set_yticks(range(len(names)))
        ax.set_yticklabels(names, fontsize=9)
        ax.set_xlabel("Count")
        ax.set_title(name)
        ax.invert_yaxis()

    fig.suptitle("Top Anomaly Features per Model", fontsize=15)
    plt.tight_layout()
    plt.savefig(output_path, dpi=150, bbox_inches="tight")
    plt.close()
    log.info("Saved: %s", output_path)


# ---------------------------------------------------------------------------
# 6. ROC Curves
# ---------------------------------------------------------------------------

def plot_roc_curves(
    results: dict[str, pd.DataFrame],
    labels: pd.DataFrame = None,
    comparison: pd.DataFrame = None,
    output_path: str = "ml/results/plots/roc_curves.png",
):
    """ROC кривые с AUC для каждой модели vs ground truth (или ensemble)."""
    # Определяем ground truth
    if labels is not None and "is_anomaly" in labels.columns:
        y_true = labels["is_anomaly"].values
        gt_source = "ground truth"
    elif comparison is not None and "majority_anomaly" in comparison.columns:
        y_true = comparison["majority_anomaly"].values
        gt_source = "ensemble majority"
    else:
        log.warning("No ground truth for ROC curves")
        return

    fig, ax = plt.subplots(figsize=(10, 8))

    individual = [n for n in results if n != "ensemble"]

    for name in individual:
        scores = results[name]["anomaly_score"].values[:len(y_true)]

        # Вычисляем ROC вручную (без sklearn.metrics для меньше зависимостей)
        thresholds = np.percentile(scores, np.linspace(0, 100, 200))
        thresholds = np.unique(thresholds)

        tpr_list, fpr_list = [], []
        for thresh in thresholds:
            y_pred = scores >= thresh
            tp = np.sum(y_true & y_pred)
            fp = np.sum(~y_true & y_pred)
            fn = np.sum(y_true & ~y_pred)
            tn = np.sum(~y_true & ~y_pred)

            tpr = tp / max(tp + fn, 1)
            fpr = fp / max(fp + tn, 1)
            tpr_list.append(tpr)
            fpr_list.append(fpr)

        # Сортируем по FPR
        sorted_pairs = sorted(zip(fpr_list, tpr_list))
        fpr_sorted = [p[0] for p in sorted_pairs]
        tpr_sorted = [p[1] for p in sorted_pairs]

        # AUC (trapezoidal)
        auc = np.trapz(tpr_sorted, fpr_sorted)

        color = COLORS.get(name, "#666666")
        ax.plot(fpr_sorted, tpr_sorted, color=color, linewidth=2,
                label=f"{name} (AUC={auc:.3f})")

    ax.plot([0, 1], [0, 1], "k--", alpha=0.3, label="Random")
    ax.set_xlabel("False Positive Rate")
    ax.set_ylabel("True Positive Rate")
    ax.set_title(f"ROC Curves (vs {gt_source})")
    ax.legend(loc="lower right")
    ax.set_xlim(-0.02, 1.02)
    ax.set_ylim(-0.02, 1.02)

    plt.tight_layout()
    plt.savefig(output_path, dpi=150, bbox_inches="tight")
    plt.close()
    log.info("Saved: %s", output_path)


# ---------------------------------------------------------------------------
# Generate All
# ---------------------------------------------------------------------------

def generate_all_plots(
    results: dict[str, pd.DataFrame],
    comparison: pd.DataFrame,
    metrics: dict,
    labels: pd.DataFrame = None,
    output_dir: str = "ml/results",
):
    """Генерирует все 6 типов графиков."""
    plots_dir = os.path.join(output_dir, "plots")
    os.makedirs(plots_dir, exist_ok=True)

    plot_fns = [
        ("anomaly_timeline", lambda: plot_anomaly_timeline(
            results, labels, os.path.join(plots_dir, "anomaly_timeline.png"))),
        ("score_distributions", lambda: plot_score_distributions(
            results, os.path.join(plots_dir, "score_distributions.png"))),
        ("model_agreement", lambda: plot_model_agreement(
            metrics, os.path.join(plots_dir, "model_agreement.png"))),
        ("confusion_matrices", lambda: plot_confusion_matrices(
            metrics, os.path.join(plots_dir, "confusion_matrices.png"))),
        ("feature_importance", lambda: plot_feature_importance(
            metrics, os.path.join(plots_dir, "feature_importance.png"))),
        ("roc_curves", lambda: plot_roc_curves(
            results, labels, comparison, os.path.join(plots_dir, "roc_curves.png"))),
    ]

    success = 0
    for plot_name, fn in plot_fns:
        try:
            fn()
            success += 1
        except Exception as e:
            log.error("Ошибка графика '%s': %s", plot_name, e)

    log.info("Графики: %d/%d успешно сохранены в %s", success, len(plot_fns), plots_dir)


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(description="TON anomaly detection visualization")
    parser.add_argument("--input", type=str, default="ml/results", help="Директория с результатами")
    parser.add_argument("--labels", type=str, default=None, help="Путь к labels CSV")
    args = parser.parse_args()

    input_dir = args.input

    # Загрузка данных
    metrics_path = os.path.join(input_dir, "metrics.json")
    comparison_path = os.path.join(input_dir, "comparison.csv")

    with open(metrics_path) as f:
        metrics = json.load(f)

    comparison = pd.read_csv(comparison_path)

    labels = None
    if args.labels and os.path.exists(args.labels):
        labels = pd.read_csv(args.labels)

    # Загрузка per-model results
    results = {}
    for f_name in os.listdir(input_dir):
        if f_name.startswith("anomalies_") and f_name.endswith(".csv"):
            model_name = f_name.replace("anomalies_", "").replace(".csv", "")
            results[model_name] = pd.read_csv(os.path.join(input_dir, f_name))

    if not results:
        log.error("Нет файлов anomalies_*.csv в %s", input_dir)
        return

    generate_all_plots(results, comparison, metrics, labels, input_dir)


if __name__ == "__main__":
    main()
