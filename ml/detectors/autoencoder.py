"""
ml/detectors/autoencoder.py

Autoencoder детектор — нейросетевой метод для сложных паттернов.

Логика: нейросеть учится сжимать нормальные блоки в маленький латентный
вектор и восстанавливать их обратно. На аномальных блоках ошибка
восстановления (reconstruction error) резко возрастает — модель не знает
как восстановить паттерн которого не видела при обучении.

Архитектура: encoder → bottleneck → decoder (симметричная)
  вход (n_features) → 64 → 32 → 8 (латент) → 32 → 64 → вход

Плюсы:  ловит сложные нелинейные паттерны, хорошая визуализация,
        порог можно настраивать после обучения
Минусы: нужно больше данных, дольше обучается, сложнее интерпретировать
"""

import numpy as np
import pandas as pd
import torch
import torch.nn as nn
from dataclasses import dataclass, field
from sklearn.preprocessing import StandardScaler


@dataclass
class AutoencoderConfig:
    # Размер латентного пространства — "сжатое" представление блока.
    # 8 чисел для ~50 признаков — достаточно чтобы выучить нормальный паттерн.
    latent_dim: int = 8

    # Промежуточные слои encoder'а (decoder симметричен).
    hidden_dims: list[int] = field(default_factory=lambda: [64, 32])

    # Параметры обучения
    epochs: int = 50
    batch_size: int = 64
    learning_rate: float = 1e-3

    # Порог аномалии: блок аномален если reconstruction error > threshold.
    # None = автоматически: mean + 3*std по обучающим данным.
    threshold: float = None

    # Доля данных для валидации при обучении
    val_split: float = 0.1

    random_state: int = 42

    feature_prefix_exclude: list[str] = field(
        default_factory=lambda: ["timestamp", "seqno", "z_", "cusum_"]
    )


class _AutoencoderNet(nn.Module):
    """
    Симметричный autoencoder с BatchNorm и LeakyReLU.

    BatchNorm стабилизирует обучение на временных рядах с разным масштабом.
    LeakyReLU вместо ReLU — избегаем "мёртвых нейронов" (dying ReLU).
    """

    def __init__(self, input_dim: int, hidden_dims: list[int], latent_dim: int):
        super().__init__()

        # Encoder: input → hidden → latent
        enc_layers = []
        prev = input_dim
        for h in hidden_dims:
            enc_layers += [
                nn.Linear(prev, h),
                nn.BatchNorm1d(h),
                nn.LeakyReLU(0.1),
            ]
            prev = h
        enc_layers.append(nn.Linear(prev, latent_dim))
        self.encoder = nn.Sequential(*enc_layers)

        # Decoder: latent → hidden (обратный порядок) → input
        dec_layers = []
        prev = latent_dim
        for h in reversed(hidden_dims):
            dec_layers += [
                nn.Linear(prev, h),
                nn.BatchNorm1d(h),
                nn.LeakyReLU(0.1),
            ]
            prev = h
        dec_layers.append(nn.Linear(prev, input_dim))
        self.decoder = nn.Sequential(*dec_layers)

    def forward(self, x: torch.Tensor) -> torch.Tensor:
        return self.decoder(self.encoder(x))

    def encode(self, x: torch.Tensor) -> torch.Tensor:
        return self.encoder(x)


class AutoencoderDetector:

    def __init__(self, config: AutoencoderConfig = None):
        self.cfg          = config or AutoencoderConfig()
        self.model        = None
        self.scaler       = StandardScaler()
        self.threshold_   = None
        self.feature_cols: list[str] = []
        self.train_losses: list[float] = []

        torch.manual_seed(self.cfg.random_state)
        self.device = torch.device("cuda" if torch.cuda.is_available() else "cpu")

    def _select_features(self, df: pd.DataFrame) -> list[str]:
        exclude = set(self.cfg.feature_prefix_exclude)
        return [
            col for col in df.select_dtypes(include=[np.number]).columns
            if not any(col.startswith(e) for e in exclude)
        ]

    def fit(self, df: pd.DataFrame) -> "AutoencoderDetector":
        """
        Обучает autoencoder на переданных данных.

        Важно: передавать только "нормальные" периоды.
        Для нашего случая — используем всю историю, предполагая что
        аномалии составляют малую долю и не испортят обучение.
        """
        self.feature_cols = self._select_features(df)
        X = df[self.feature_cols].values.astype(np.float32)
        X = self.scaler.fit_transform(X)

        # Train / val split
        n_val  = max(1, int(len(X) * self.cfg.val_split))
        X_train = torch.tensor(X[:-n_val]).to(self.device)
        X_val   = torch.tensor(X[-n_val:]).to(self.device)

        self.model = _AutoencoderNet(
            input_dim   = len(self.feature_cols),
            hidden_dims = self.cfg.hidden_dims,
            latent_dim  = self.cfg.latent_dim,
        ).to(self.device)

        optimizer = torch.optim.Adam(self.model.parameters(), lr=self.cfg.learning_rate)
        loss_fn   = nn.MSELoss()

        self.train_losses = []

        for epoch in range(self.cfg.epochs):
            self.model.train()
            # Перемешиваем каждую эпоху
            idx = torch.randperm(len(X_train))
            epoch_loss = 0.0
            batches = 0

            for i in range(0, len(X_train), self.cfg.batch_size):
                batch = X_train[idx[i:i + self.cfg.batch_size]]
                optimizer.zero_grad()
                reconstructed = self.model(batch)
                loss = loss_fn(reconstructed, batch)
                loss.backward()
                optimizer.step()
                epoch_loss += loss.item()
                batches += 1

            avg_loss = epoch_loss / max(batches, 1)
            self.train_losses.append(avg_loss)

            if (epoch + 1) % 10 == 0:
                self.model.eval()
                with torch.no_grad():
                    val_loss = loss_fn(self.model(X_val), X_val).item()
                print(f"  Epoch {epoch+1:3d}/{self.cfg.epochs} "
                      f"| train_loss: {avg_loss:.4f} | val_loss: {val_loss:.4f}")

        # Устанавливаем порог аномалии по обучающим данным
        if self.cfg.threshold is None:
            self._set_threshold(X_train)

        return self

    def _set_threshold(self, X_train: torch.Tensor) -> None:
        """
        Автоматический порог: mean(reconstruction_error) + 3 * std.
        Покрывает ~99.7% нормальных блоков при нормальном распределении ошибок.
        """
        self.model.eval()
        with torch.no_grad():
            errors = self._reconstruction_errors(X_train).cpu().numpy()
        self.threshold_ = float(errors.mean() + 3 * errors.std())
        print(f"  Порог аномалии установлен: {self.threshold_:.4f} "
              f"(mean={errors.mean():.4f}, std={errors.std():.4f})")

    def _reconstruction_errors(self, X: torch.Tensor) -> torch.Tensor:
        """MSE на уровне отдельного примера (среднее по признакам)."""
        reconstructed = self.model(X)
        return ((X - reconstructed) ** 2).mean(dim=1)

    def detect(self, df: pd.DataFrame) -> pd.DataFrame:
        """
        Применяет обученный autoencoder.
        Возвращает DataFrame с колонками:
          - reconstruction_error  — MSE восстановления
          - anomaly_score         — то же что reconstruction_error (для единообразия)
          - is_anomaly
          - anomaly_features      — топ-3 признака с наибольшей ошибкой восстановления
        """
        if self.model is None:
            self.fit(df)

        result = df.copy()
        X = df[self.feature_cols].values.astype(np.float32)
        X_scaled = self.scaler.transform(X)
        X_tensor  = torch.tensor(X_scaled).to(self.device)

        self.model.eval()
        with torch.no_grad():
            X_reconstructed = self.model(X_tensor).cpu().numpy()
            errors_per_feature = (X_scaled - X_reconstructed) ** 2  # (N, F)
            errors = errors_per_feature.mean(axis=1)                 # (N,)

        result["reconstruction_error"] = errors
        result["anomaly_score"]        = errors
        result["is_anomaly"]           = errors > self.threshold_

        # Интерпретация: какие признаки внесли наибольший вклад в ошибку
        top_k = 3
        top_feat_idx = np.argsort(errors_per_feature, axis=1)[:, -top_k:][:, ::-1]
        result["anomaly_features"] = [
            ", ".join(self.feature_cols[i] for i in row)
            for row in top_feat_idx
        ]

        return result

    def summary(self, result: pd.DataFrame) -> dict:
        anomalies = result[result["is_anomaly"]]
        return {
            "total_blocks":     len(result),
            "anomaly_count":    len(anomalies),
            "anomaly_rate_pct": round(len(anomalies) / len(result) * 100, 2),
            "threshold":        round(self.threshold_, 4),
            "max_error":        round(result["reconstruction_error"].max(), 4),
            "mean_error":       round(result["reconstruction_error"].mean(), 4),
            "n_features":       len(self.feature_cols),
            "latent_dim":       self.cfg.latent_dim,
            "top_features":     (
                anomalies["anomaly_features"]
                .str.split(", ")
                .explode()
                .value_counts()
                .head(3)
                .to_dict()
            ),
        }