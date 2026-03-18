package models

import "time"

// BlockMetrics содержит метрики одного блока
type BlockMetrics struct {
	SeqNo     uint32    `json:"seqno"`
	Timestamp time.Time `json:"timestamp"`

	// Tier 1 — основные метрики блока
	TransactionCount int     `json:"transaction_count"`
	UniqueAddresses  int     `json:"unique_addresses"`
	TotalValue       float64 `json:"total_value"`    // в TON
	TotalGasUsed     uint64  `json:"total_gas_used"` // в нанотон
	AvgGasPrice      float64 `json:"avg_gas_price"`  // в нанотон

	// Технические
	BlockTime  float64 `json:"block_time"`  // секунды с предыдущего блока
	ShardCount int     `json:"shard_count"` // количество шардов в этом блоке

	// Служебные (не сохраняем в Kafka как основные данные)
	ProcessedAt time.Time `json:"processed_at"` // когда scrapper обработал блок
	Addresses   []string  `json:"-"`            // адреса (не сериализуем — только для внутреннего использования)
}

// AggregatedMetrics — агрегированные метрики за период (окно времени)
type AggregatedMetrics struct {
	PeriodStart time.Time `json:"period_start"`
	PeriodEnd   time.Time `json:"period_end"`

	TotalTransactions int     `json:"total_transactions"`
	ActiveAddresses   int     `json:"active_addresses"`
	TPS               float64 `json:"tps"`
	AvgBlockTime      float64 `json:"avg_block_time"`
	TotalVolume       float64 `json:"total_volume"`
	BlockCount        int     `json:"block_count"`
}
