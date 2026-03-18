package config

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Mode        string // "realtime", "historical", "both"
	WorkerCount int
	Detailed    bool // true → реальные TX через GetBlockTransactionsV2; false → быстро (шарды)
	Kafka       KafkaConfig
}

type KafkaConfig struct {
	Enabled      bool
	Brokers      []string
	TopicBlocks  string
	TopicMetrics string
	GroupID      string
}

func Load() *Config {
	mode := os.Getenv("MODE")
	if mode == "" {
		mode = "realtime"
	}

	workerCount := 5
	if wc := os.Getenv("WORKER_COUNT"); wc != "" {
		if parsed, err := strconv.Atoi(wc); err == nil && parsed > 0 {
			workerCount = parsed
		}
	}

	// DETAILED=false → режим fast (только шарды, быстро)
	// DETAILED=true или не задан → детальный (реальные TX и адреса)
	detailed := os.Getenv("DETAILED") != "false"

	// --- Kafka конфигурация ---
	kafkaEnabled := os.Getenv("KAFKA_ENABLED") != "false"

	brokersStr := os.Getenv("KAFKA_BROKERS")
	if brokersStr == "" {
		brokersStr = "localhost:9092"
	}
	brokers := strings.Split(brokersStr, ",")
	for i := range brokers {
		brokers[i] = strings.TrimSpace(brokers[i])
	}

	topicBlocks := os.Getenv("KAFKA_TOPIC_BLOCKS")
	if topicBlocks == "" {
		topicBlocks = "ton.blocks"
	}

	topicMetrics := os.Getenv("KAFKA_TOPIC_METRICS")
	if topicMetrics == "" {
		topicMetrics = "ton.metrics"
	}

	groupID := os.Getenv("KAFKA_GROUP_ID")
	if groupID == "" {
		groupID = "ton-scrapper"
	}

	return &Config{
		Mode:        mode,
		WorkerCount: workerCount,
		Detailed:    detailed,
		Kafka: KafkaConfig{
			Enabled:      kafkaEnabled,
			Brokers:      brokers,
			TopicBlocks:  topicBlocks,
			TopicMetrics: topicMetrics,
			GroupID:      groupID,
		},
	}
}
