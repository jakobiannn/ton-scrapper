package models

import "time"

// BlockMetrics содержит метрики одного блока
type BlockMetrics struct {
	SeqNo     uint32    `json:"seqno"`
	Timestamp time.Time `json:"timestamp"`

	// Tier 1 метрики
	TransactionCount int     `json:"transaction_count"`
	UniqueAddresses  int     `json:"unique_addresses"`
	TotalValue       float64 `json:"total_value"` // в TON
	TotalGasUsed     uint64  `json:"total_gas_used"`
	AvgGasPrice      float64 `json:"avg_gas_price"`

	// Технические
	BlockTime float64  `json:"block_time"` // секунды с предыдущего блока
	Addresses []string `json:"-"`          // не сериализуем в JSON
}

// AggregatedMetrics - агрегированные метрики за период
type AggregatedMetrics struct {
	PeriodStart time.Time `json:"period_start"`
	PeriodEnd   time.Time `json:"period_end"`

	TotalTransactions int     `json:"total_transactions"`
	ActiveAddresses   int     `json:"active_addresses"`
	TPS               float64 `json:"tps"`
	AvgBlockTime      float64 `json:"avg_block_time"`
	TotalVolume       float64 `json:"total_volume"`
}
