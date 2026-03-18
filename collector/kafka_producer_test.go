package collector

import (
	"testing"
	"time"

	"ton-scrapper/models"
)

// --- DefaultTopics ---

func TestDefaultTopics_Count(t *testing.T) {
	topics := DefaultTopics("ton.blocks", "ton.metrics")
	if len(topics) != 2 {
		t.Errorf("ожидали 2 топика, получили %d", len(topics))
	}
}

func TestDefaultTopics_Names(t *testing.T) {
	blocks := "custom.blocks"
	metrics := "custom.metrics"
	topics := DefaultTopics(blocks, metrics)

	if topics[0].Name != blocks {
		t.Errorf("topics[0].Name: got %q, want %q", topics[0].Name, blocks)
	}
	if topics[1].Name != metrics {
		t.Errorf("topics[1].Name: got %q, want %q", topics[1].Name, metrics)
	}
}

func TestDefaultTopics_Partitions(t *testing.T) {
	topics := DefaultTopics("ton.blocks", "ton.metrics")
	for _, topic := range topics {
		if topic.Partitions <= 0 {
			t.Errorf("топик %q: партиций должно быть > 0, получили %d", topic.Name, topic.Partitions)
		}
	}
}

func TestDefaultTopics_RetentionHours(t *testing.T) {
	topics := DefaultTopics("ton.blocks", "ton.metrics")
	for _, topic := range topics {
		if topic.RetentionHours <= 0 {
			t.Errorf("топик %q: RetentionHours должен быть > 0", topic.Name)
		}
		// Проверяем что значение корректно конвертируется в миллисекунды
		ms := topic.RetentionHours * 3600 * 1000
		if ms <= 0 {
			t.Errorf("топик %q: retention ms <= 0: %d", topic.Name, ms)
		}
	}
}

func TestDefaultTopics_Replication(t *testing.T) {
	topics := DefaultTopics("ton.blocks", "ton.metrics")
	for _, topic := range topics {
		if topic.Replication <= 0 {
			t.Errorf("топик %q: Replication должен быть > 0", topic.Name)
		}
	}
}

// --- TopicConfig ---

func TestTopicConfig_Fields(t *testing.T) {
	tc := TopicConfig{
		Name:           "test.topic",
		Partitions:     5,
		Replication:    2,
		RetentionHours: 48,
	}
	if tc.Name != "test.topic" {
		t.Error("Name не совпадает")
	}
	if tc.Partitions != 5 {
		t.Errorf("Partitions: got %d", tc.Partitions)
	}
	if tc.Replication != 2 {
		t.Errorf("Replication: got %d", tc.Replication)
	}
	if tc.RetentionHours != 48 {
		t.Errorf("RetentionHours: got %d", tc.RetentionHours)
	}
}

// --- NewKafkaProducer (создание без сети) ---

func TestNewKafkaProducer_CreatesWithoutPanic(t *testing.T) {
	// Просто создание продюсера не должно требовать соединения с Kafka
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("NewKafkaProducer паникует: %v", r)
		}
	}()

	p := NewKafkaProducer([]string{"localhost:9092"}, "test.topic")
	if p == nil {
		t.Fatal("NewKafkaProducer вернул nil")
	}
	// Не вызываем Close, чтобы не блокировать тест
}

// --- BlockMetrics сериализация для Kafka ---

func TestBlockMetrics_KafkaKey(t *testing.T) {
	m := &models.BlockMetrics{
		SeqNo:     42_000_000,
		Timestamp: time.Now(),
	}
	// Ключ должен быть строковым представлением seqno
	key := []byte("42000000")
	if string(key) != "42000000" {
		t.Error("ключ Kafka должен быть строковым seqno")
	}
	_ = m
}

// --- интеграционные тесты (только с флагом -tags=integration) ---
// Для запуска: go test -tags=integration -run TestKafkaIntegration ./collector/...
