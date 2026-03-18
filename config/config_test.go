package config

import (
	"os"
	"testing"
)

func setEnv(t *testing.T, key, value string) {
	t.Helper()
	t.Setenv(key, value) // автоматически восстанавливает значение после теста
}

func clearKafkaEnv(t *testing.T) {
	for _, k := range []string{
		"MODE", "WORKER_COUNT", "DETAILED",
		"KAFKA_ENABLED", "KAFKA_BROKERS", "KAFKA_TOPIC_BLOCKS",
		"KAFKA_TOPIC_METRICS", "KAFKA_GROUP_ID",
	} {
		os.Unsetenv(k)
	}
}

// --- defaults ---

func TestLoad_Defaults(t *testing.T) {
	clearKafkaEnv(t)

	cfg := Load()

	if cfg.Mode != "realtime" {
		t.Errorf("Mode default: got %q, want \"realtime\"", cfg.Mode)
	}
	if cfg.WorkerCount != 5 {
		t.Errorf("WorkerCount default: got %d, want 5", cfg.WorkerCount)
	}
	if !cfg.Detailed {
		t.Error("Detailed default должен быть true")
	}
	if !cfg.Kafka.Enabled {
		t.Error("Kafka.Enabled default должен быть true")
	}
	if len(cfg.Kafka.Brokers) != 1 || cfg.Kafka.Brokers[0] != "localhost:9092" {
		t.Errorf("Kafka.Brokers default: got %v, want [localhost:9092]", cfg.Kafka.Brokers)
	}
	if cfg.Kafka.TopicBlocks != "ton.blocks" {
		t.Errorf("TopicBlocks default: got %q, want \"ton.blocks\"", cfg.Kafka.TopicBlocks)
	}
	if cfg.Kafka.TopicMetrics != "ton.metrics" {
		t.Errorf("TopicMetrics default: got %q, want \"ton.metrics\"", cfg.Kafka.TopicMetrics)
	}
	if cfg.Kafka.GroupID != "ton-scrapper" {
		t.Errorf("GroupID default: got %q, want \"ton-scrapper\"", cfg.Kafka.GroupID)
	}
}

// --- MODE ---

func TestLoad_Mode(t *testing.T) {
	tests := []struct {
		envVal string
		want   string
	}{
		{"realtime", "realtime"},
		{"historical", "historical"},
		{"both", "both"},
		{"", "realtime"}, // пустая строка → default
	}

	for _, tt := range tests {
		t.Run("MODE="+tt.envVal, func(t *testing.T) {
			clearKafkaEnv(t)
			setEnv(t, "MODE", tt.envVal)
			cfg := Load()
			if cfg.Mode != tt.want {
				t.Errorf("Mode: got %q, want %q", cfg.Mode, tt.want)
			}
		})
	}
}

// --- WORKER_COUNT ---

func TestLoad_WorkerCount(t *testing.T) {
	tests := []struct {
		envVal string
		want   int
	}{
		{"3", 3},
		{"10", 10},
		{"1", 1},
		{"0", 5},   // 0 → невалидно → default
		{"-1", 5},  // отрицательное → невалидно → default
		{"abc", 5}, // не число → default
		{"", 5},    // пустая строка → default
	}

	for _, tt := range tests {
		t.Run("WORKER_COUNT="+tt.envVal, func(t *testing.T) {
			clearKafkaEnv(t)
			setEnv(t, "WORKER_COUNT", tt.envVal)
			cfg := Load()
			if cfg.WorkerCount != tt.want {
				t.Errorf("WorkerCount: got %d, want %d", cfg.WorkerCount, tt.want)
			}
		})
	}
}

// --- DETAILED ---

func TestLoad_Detailed(t *testing.T) {
	tests := []struct {
		envVal string
		want   bool
	}{
		{"true", true},
		{"", true}, // не задан → default true
		{"1", true},
		{"false", false},
	}

	for _, tt := range tests {
		t.Run("DETAILED="+tt.envVal, func(t *testing.T) {
			clearKafkaEnv(t)
			if tt.envVal != "" {
				setEnv(t, "DETAILED", tt.envVal)
			}
			cfg := Load()
			if cfg.Detailed != tt.want {
				t.Errorf("Detailed: got %v, want %v", cfg.Detailed, tt.want)
			}
		})
	}
}

// --- KAFKA_ENABLED ---

func TestLoad_KafkaEnabled(t *testing.T) {
	clearKafkaEnv(t)
	setEnv(t, "KAFKA_ENABLED", "false")
	cfg := Load()
	if cfg.Kafka.Enabled {
		t.Error("Kafka.Enabled должен быть false при KAFKA_ENABLED=false")
	}
}

// --- KAFKA_BROKERS ---

func TestLoad_KafkaBrokers_Single(t *testing.T) {
	clearKafkaEnv(t)
	setEnv(t, "KAFKA_BROKERS", "broker1:9092")
	cfg := Load()
	if len(cfg.Kafka.Brokers) != 1 || cfg.Kafka.Brokers[0] != "broker1:9092" {
		t.Errorf("Kafka.Brokers: got %v", cfg.Kafka.Brokers)
	}
}

func TestLoad_KafkaBrokers_Multiple(t *testing.T) {
	clearKafkaEnv(t)
	setEnv(t, "KAFKA_BROKERS", "b1:9092, b2:9092 , b3:9092")
	cfg := Load()
	if len(cfg.Kafka.Brokers) != 3 {
		t.Fatalf("ожидали 3 брокера, получили %d: %v", len(cfg.Kafka.Brokers), cfg.Kafka.Brokers)
	}
	// Пробелы должны быть обрезаны
	for _, b := range cfg.Kafka.Brokers {
		if b != "b1:9092" && b != "b2:9092" && b != "b3:9092" {
			t.Errorf("неожиданный брокер с пробелом: %q", b)
		}
	}
}

// --- кастомные топики ---

func TestLoad_CustomTopics(t *testing.T) {
	clearKafkaEnv(t)
	setEnv(t, "KAFKA_TOPIC_BLOCKS", "my.blocks")
	setEnv(t, "KAFKA_TOPIC_METRICS", "my.metrics")
	setEnv(t, "KAFKA_GROUP_ID", "my-group")

	cfg := Load()

	if cfg.Kafka.TopicBlocks != "my.blocks" {
		t.Errorf("TopicBlocks: got %q", cfg.Kafka.TopicBlocks)
	}
	if cfg.Kafka.TopicMetrics != "my.metrics" {
		t.Errorf("TopicMetrics: got %q", cfg.Kafka.TopicMetrics)
	}
	if cfg.Kafka.GroupID != "my-group" {
		t.Errorf("GroupID: got %q", cfg.Kafka.GroupID)
	}
}
