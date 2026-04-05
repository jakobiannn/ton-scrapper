"""
ml/models/saver.py

Сериализация и загрузка обученных моделей для real-time inference.

Каждый тип модели сохраняется в своём формате:
  - sklearn (IsolationForest, LOF): joblib → .joblib
  - PyTorch (Autoencoder): torch.save(state_dict) → .pt + config .json
  - Статистические (ZScore, CUSUM): параметры → .json
  - StandardScaler: сохраняется вместе с моделью через joblib

Запуск:
    # Сохранение после обучения
    from ml.models.saver import save_all_models, load_all_models
    save_all_models(detectors, "ml/models/trained/")
    loaded = load_all_models("ml/models/trained/")
"""

import json
import logging
import os
from typing import Any

import joblib
import numpy as np

log = logging.getLogger("model-saver")


def save_all_models(detectors: dict[str, Any], output_dir: str) -> dict[str, str]:
    """
    Сохраняет все обученные детекторы.

    Args:
        detectors: dict[name, detector_instance] — обученные детекторы
        output_dir: директория для сохранения

    Returns:
        dict[name, file_path] — пути к сохранённым файлам
    """
    os.makedirs(output_dir, exist_ok=True)
    saved = {}

    for name, detector in detectors.items():
        try:
            path = _save_detector(name, detector, output_dir)
            saved[name] = path
            log.info("Модель '%s' сохранена: %s", name, path)
        except Exception as e:
            log.error("Ошибка сохранения модели '%s': %s", name, e)

    # Сохраняем manifest
    manifest = {
        "models": {name: os.path.basename(path) for name, path in saved.items()},
        "feature_cols": {},
    }
    for name, detector in detectors.items():
        if hasattr(detector, "feature_cols"):
            manifest["feature_cols"][name] = detector.feature_cols

    manifest_path = os.path.join(output_dir, "manifest.json")
    with open(manifest_path, "w") as f:
        json.dump(manifest, f, indent=2)

    log.info("Manifest сохранён: %s (%d моделей)", manifest_path, len(saved))
    return saved


def _save_detector(name: str, detector: Any, output_dir: str) -> str:
    """Сохраняет один детектор в подходящем формате."""
    from ml.detectors.autoencoder import AutoencoderDetector
    from ml.detectors.isolation import IsolationForestDetector
    from ml.detectors.lof import LOFDetector
    from ml.detectors.zscore import ZScoreDetector
    from ml.detectors.cusum import CUSUMDetector

    if isinstance(detector, AutoencoderDetector):
        return _save_autoencoder(name, detector, output_dir)
    elif isinstance(detector, (IsolationForestDetector, LOFDetector)):
        return _save_sklearn(name, detector, output_dir)
    elif isinstance(detector, (ZScoreDetector, CUSUMDetector)):
        return _save_statistical(name, detector, output_dir)
    else:
        # Fallback: joblib для любого объекта
        path = os.path.join(output_dir, f"{name}.joblib")
        joblib.dump(detector, path)
        return path


def _save_autoencoder(name: str, detector: Any, output_dir: str) -> str:
    """Сохраняет Autoencoder: state_dict + scaler + config."""
    import torch

    base = os.path.join(output_dir, name)
    os.makedirs(base, exist_ok=True)

    # Модель PyTorch
    model_path = os.path.join(base, "model.pt")
    torch.save(detector.model.state_dict(), model_path)

    # Scaler
    scaler_path = os.path.join(base, "scaler.joblib")
    joblib.dump(detector.scaler, scaler_path)

    # Config + threshold + feature_cols
    config_path = os.path.join(base, "config.json")
    config_data = {
        "latent_dim": detector.cfg.latent_dim,
        "hidden_dims": detector.cfg.hidden_dims,
        "threshold": detector.threshold_,
        "feature_cols": detector.feature_cols,
        "input_dim": len(detector.feature_cols),
    }
    with open(config_path, "w") as f:
        json.dump(config_data, f, indent=2)

    return base


def _save_sklearn(name: str, detector: Any, output_dir: str) -> str:
    """Сохраняет sklearn-based детектор через joblib."""
    path = os.path.join(output_dir, f"{name}.joblib")
    joblib.dump({
        "model": detector.model,
        "scaler": detector.scaler,
        "feature_cols": detector.feature_cols,
        "config": detector.cfg,
    }, path)
    return path


def _save_statistical(name: str, detector: Any, output_dir: str) -> str:
    """Сохраняет статистический детектор (параметры конфигурации)."""
    path = os.path.join(output_dir, f"{name}.json")
    config_data = {}

    if hasattr(detector.cfg, "__dataclass_fields__"):
        for field_name in detector.cfg.__dataclass_fields__:
            val = getattr(detector.cfg, field_name)
            if isinstance(val, (list, tuple)):
                config_data[field_name] = list(val)
            else:
                config_data[field_name] = val

    with open(path, "w") as f:
        json.dump(config_data, f, indent=2)
    return path


def load_all_models(model_dir: str) -> dict[str, Any]:
    """
    Загружает все модели из директории по manifest.json.

    Returns:
        dict[name, detector_instance] — готовые к inference детекторы
    """
    manifest_path = os.path.join(model_dir, "manifest.json")
    if not os.path.exists(manifest_path):
        raise FileNotFoundError(f"Manifest не найден: {manifest_path}")

    with open(manifest_path) as f:
        manifest = json.load(f)

    loaded = {}
    for name, filename in manifest["models"].items():
        try:
            path = os.path.join(model_dir, filename)
            detector = _load_detector(name, path, manifest.get("feature_cols", {}).get(name))
            loaded[name] = detector
            log.info("Модель '%s' загружена: %s", name, path)
        except Exception as e:
            log.error("Ошибка загрузки модели '%s': %s", name, e)

    return loaded


def _load_detector(name: str, path: str, feature_cols: list[str] = None) -> Any:
    """Загружает один детектор из файла."""
    if os.path.isdir(path):
        # Autoencoder (директория с model.pt, scaler.joblib, config.json)
        return _load_autoencoder(path)
    elif path.endswith(".joblib"):
        return _load_sklearn(path)
    elif path.endswith(".json"):
        return _load_statistical(name, path)
    else:
        raise ValueError(f"Неизвестный формат модели: {path}")


def _load_autoencoder(base_dir: str) -> Any:
    """Загружает Autoencoder из директории."""
    import torch
    from ml.detectors.autoencoder import AutoencoderDetector, AutoencoderConfig, _AutoencoderNet

    config_path = os.path.join(base_dir, "config.json")
    with open(config_path) as f:
        config_data = json.load(f)

    cfg = AutoencoderConfig(
        latent_dim=config_data["latent_dim"],
        hidden_dims=config_data["hidden_dims"],
    )

    detector = AutoencoderDetector(cfg)
    detector.feature_cols = config_data["feature_cols"]
    detector.threshold_ = config_data["threshold"]

    # Scaler
    scaler_path = os.path.join(base_dir, "scaler.joblib")
    detector.scaler = joblib.load(scaler_path)

    # Model
    model_path = os.path.join(base_dir, "model.pt")
    detector.model = _AutoencoderNet(
        input_dim=config_data["input_dim"],
        hidden_dims=config_data["hidden_dims"],
        latent_dim=config_data["latent_dim"],
    ).to(detector.device)
    detector.model.load_state_dict(torch.load(model_path, map_location=detector.device, weights_only=True))
    detector.model.eval()

    return detector


def _load_sklearn(path: str) -> Any:
    """Загружает sklearn детектор из joblib."""
    data = joblib.load(path)

    # Определяем тип по наличию модели
    from ml.detectors.isolation import IsolationForestDetector, IsolationForestConfig
    from ml.detectors.lof import LOFDetector, LOFConfig

    config = data["config"]
    if isinstance(config, IsolationForestConfig):
        detector = IsolationForestDetector(config)
    elif isinstance(config, LOFConfig):
        detector = LOFDetector(config)
    else:
        detector = IsolationForestDetector(config)

    detector.model = data["model"]
    detector.scaler = data["scaler"]
    detector.feature_cols = data["feature_cols"]

    return detector


def _load_statistical(name: str, path: str) -> Any:
    """Загружает статистический детектор из JSON."""
    with open(path) as f:
        config_data = json.load(f)

    if name == "zscore" or "threshold" in config_data:
        from ml.detectors.zscore import ZScoreDetector, ZScoreConfig
        cfg = ZScoreConfig(**{k: v for k, v in config_data.items()
                              if k in ZScoreConfig.__dataclass_fields__})
        return ZScoreDetector(cfg)
    else:
        from ml.detectors.cusum import CUSUMDetector, CUSUMConfig
        cfg = CUSUMConfig(**{k: v for k, v in config_data.items()
                             if k in CUSUMConfig.__dataclass_fields__})
        return CUSUMDetector(cfg)
