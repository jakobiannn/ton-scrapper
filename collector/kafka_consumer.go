package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/segmentio/kafka-go"
	"ton-scrapper/models"
)

// KafkaConsumer читает BlockMetrics из Kafka топика.
// Используется для downstream обработки: feature engineering, anomaly detection.
type KafkaConsumer struct {
	reader *kafka.Reader
	topic  string
}

// NewKafkaConsumer создаёт consumer с заданным groupID.
// GroupID определяет независимость чтения: разные группы читают один топик независимо.
func NewKafkaConsumer(brokers []string, topic, groupID string) *KafkaConsumer {
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:        brokers,
		Topic:          topic,
		GroupID:        groupID,
		MinBytes:       1,                // читаем сразу как появился хоть 1 байт
		MaxBytes:       10 << 20,         // 10 MB максимальный размер батча
		CommitInterval: time.Second,      // авто-коммит оффсета раз в секунду
		StartOffset:    kafka.LastOffset, // начинаем с последнего (не читаем историю)
	})

	return &KafkaConsumer{
		reader: reader,
		topic:  topic,
	}
}

// NewKafkaConsumerFromBeginning создаёт consumer, который читает с начала топика.
// Полезно для переобработки данных (feature engineering, anomaly detection).
func NewKafkaConsumerFromBeginning(brokers []string, topic, groupID string) *KafkaConsumer {
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:        brokers,
		Topic:          topic,
		GroupID:        groupID,
		MinBytes:       1,
		MaxBytes:       10 << 20,
		CommitInterval: time.Second,
		StartOffset:    kafka.FirstOffset, // с самого начала
	})

	return &KafkaConsumer{
		reader: reader,
		topic:  topic,
	}
}

// Consume читает сообщения из топика и вызывает handler для каждого BlockMetrics.
// Блокирующий вызов — работает до отмены ctx.
func (c *KafkaConsumer) Consume(ctx context.Context, handler func(*models.BlockMetrics) error) error {
	log.Printf("Kafka consumer запущен: топик=%s", c.topic)

	for {
		msg, err := c.reader.ReadMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				log.Printf("Kafka consumer остановлен (context cancelled)")
				return ctx.Err()
			}
			return fmt.Errorf("ошибка чтения из Kafka: %w", err)
		}

		var metrics models.BlockMetrics
		if err := json.Unmarshal(msg.Value, &metrics); err != nil {
			log.Printf("Ошибка десериализации сообщения (offset=%d): %v", msg.Offset, err)
			continue // пропускаем повреждённое сообщение
		}

		if err := handler(&metrics); err != nil {
			log.Printf("Ошибка обработки блока %d: %v", metrics.SeqNo, err)
			// Продолжаем работу — не останавливаемся из-за ошибки в одном сообщении
		}
	}
}

// Stats возвращает статистику чтения.
func (c *KafkaConsumer) Stats() kafka.ReaderStats {
	return c.reader.Stats()
}

// Close закрывает reader, освобождает ресурсы.
func (c *KafkaConsumer) Close() error {
	return c.reader.Close()
}
