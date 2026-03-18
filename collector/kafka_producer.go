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

// KafkaProducer публикует метрики блоков в Kafka топики.
type KafkaProducer struct {
	blocksWriter *kafka.Writer
}

// NewKafkaProducer создаёт продюсера с двумя райтерами: для блоков и метрик.
func NewKafkaProducer(brokers []string, topicBlocks string) *KafkaProducer {
	blocksWriter := &kafka.Writer{
		Addr:         kafka.TCP(brokers...),
		Topic:        topicBlocks,
		Balancer:     &kafka.Hash{}, // один и тот же seqno всегда идёт в одну партицию
		RequiredAcks: kafka.RequireOne,
		Async:        false,
		BatchTimeout: 10 * time.Millisecond,
		MaxAttempts:  3,
	}

	return &KafkaProducer{
		blocksWriter: blocksWriter,
	}
}

// PublishBlockMetrics сериализует BlockMetrics в JSON и отправляет в топик ton.blocks.
// Ключ сообщения = seqno (для детерминированного шардирования по партициям).
func (p *KafkaProducer) PublishBlockMetrics(ctx context.Context, metrics *models.BlockMetrics) error {
	data, err := json.Marshal(metrics)
	if err != nil {
		return fmt.Errorf("сериализация BlockMetrics: %w", err)
	}

	key := []byte(fmt.Sprintf("%d", metrics.SeqNo))

	err = p.blocksWriter.WriteMessages(ctx, kafka.Message{
		Key:   key,
		Value: data,
		Time:  metrics.Timestamp,
	})
	if err != nil {
		return fmt.Errorf("запись в Kafka (blocks): %w", err)
	}

	return nil
}

// Close корректно закрывает все writers (flush буферов).
func (p *KafkaProducer) Close() {
	if err := p.blocksWriter.Close(); err != nil {
		log.Printf("Ошибка закрытия Kafka writer: %v", err)
	}
}

// EnsureTopics создаёт топики если они не существуют.
// Идемпотентно — повторный вызов не вызывает ошибок.
func EnsureTopics(brokers []string, topics []TopicConfig) error {
	if len(brokers) == 0 {
		return fmt.Errorf("не указаны Kafka брокеры")
	}

	conn, err := kafka.Dial("tcp", brokers[0])
	if err != nil {
		return fmt.Errorf("подключение к Kafka %s: %w", brokers[0], err)
	}
	defer conn.Close()

	controller, err := conn.Controller()
	if err != nil {
		return fmt.Errorf("получение контроллера Kafka: %w", err)
	}

	controllerAddr := fmt.Sprintf("%s:%d", controller.Host, controller.Port)
	controllerConn, err := kafka.Dial("tcp", controllerAddr)
	if err != nil {
		return fmt.Errorf("подключение к контроллеру %s: %w", controllerAddr, err)
	}
	defer controllerConn.Close()

	kafkaTopics := make([]kafka.TopicConfig, 0, len(topics))
	for _, t := range topics {
		kafkaTopics = append(kafkaTopics, kafka.TopicConfig{
			Topic:             t.Name,
			NumPartitions:     t.Partitions,
			ReplicationFactor: int(t.Replication),
			ConfigEntries: []kafka.ConfigEntry{
				{
					ConfigName:  "retention.ms",
					ConfigValue: fmt.Sprintf("%d", t.RetentionHours*3600*1000),
				},
			},
		})
	}

	if err := controllerConn.CreateTopics(kafkaTopics...); err != nil {
		// Ошибка "topic already exists" — это нормально
		log.Printf("Создание Kafka топиков: %v (если топики уже существуют — это OK)", err)
	} else {
		for _, t := range topics {
			log.Printf("Kafka топик создан: %s (партиций: %d)", t.Name, t.Partitions)
		}
	}

	return nil
}

// TopicConfig описывает параметры создаваемого топика.
type TopicConfig struct {
	Name           string
	Partitions     int
	Replication    int16
	RetentionHours int64
}

// DefaultTopics возвращает стандартные топики для TON скраппера.
func DefaultTopics(topicBlocks, topicMetrics string) []TopicConfig {
	return []TopicConfig{
		{
			Name:           topicBlocks,
			Partitions:     3,
			Replication:    1,
			RetentionHours: 168, // 7 дней
		},
		{
			Name:           topicMetrics,
			Partitions:     1,
			Replication:    1,
			RetentionHours: 168,
		},
	}
}
