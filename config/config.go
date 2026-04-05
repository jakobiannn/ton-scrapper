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

	// Для historical loading: явный диапазон блоков (3-5M блоков)
	StartSeqNo uint32 // 0 = автоматически (текущий - HistoryDepth)
	EndSeqNo   uint32 // 0 = автоматически (текущий)
	HistoryDepth uint32 // кол-во блоков назад от текущего (по умолчанию 10000)

	// Checkpoint для возобновления загрузки
	CheckpointFile string

	// TonCenter API
	TonCenterAPIKey string
	TonCenterRate   int // запросов/сек (1 без ключа, 10 с ключом)
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

	// Диапазон блоков для historical loading
	var startSeqNo, endSeqNo, historyDepth uint32
	if s := os.Getenv("START_SEQNO"); s != "" {
		if parsed, err := strconv.ParseUint(s, 10, 32); err == nil {
			startSeqNo = uint32(parsed)
		}
	}
	if s := os.Getenv("END_SEQNO"); s != "" {
		if parsed, err := strconv.ParseUint(s, 10, 32); err == nil {
			endSeqNo = uint32(parsed)
		}
	}
	historyDepth = 10_000
	if s := os.Getenv("HISTORY_DEPTH"); s != "" {
		if parsed, err := strconv.ParseUint(s, 10, 32); err == nil && parsed > 0 {
			historyDepth = uint32(parsed)
		}
	}
	checkpointFile := os.Getenv("CHECKPOINT_FILE")
	if checkpointFile == "" {
		checkpointFile = ".checkpoint"
	}

	tonCenterAPIKey := os.Getenv("TONCENTER_API_KEY")
	tonCenterRate := 1
	if tonCenterAPIKey != "" {
		tonCenterRate = 10
	}
	if r := os.Getenv("TONCENTER_RATE"); r != "" {
		if parsed, err := strconv.Atoi(r); err == nil && parsed > 0 {
			tonCenterRate = parsed
		}
	}

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
		Mode:           mode,
		WorkerCount:    workerCount,
		Detailed:       detailed,
		StartSeqNo:     startSeqNo,
		EndSeqNo:       endSeqNo,
		HistoryDepth:   historyDepth,
		CheckpointFile:  checkpointFile,
		TonCenterAPIKey: tonCenterAPIKey,
		TonCenterRate:   tonCenterRate,
		Kafka: KafkaConfig{
			Enabled:      kafkaEnabled,
			Brokers:      brokers,
			TopicBlocks:  topicBlocks,
			TopicMetrics: topicMetrics,
			GroupID:      groupID,
		},
	}
}
