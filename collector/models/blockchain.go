package models

import "time"

// BlockMetrics содержит метрики одного блока TON.
// Структура разбита на тиры по источнику данных:
//   - Tier 1 (fast + detailed): базовые агрегаты
//   - Tier 2 (detailed only): поведенческие метрики для anomaly detection
//   - Computed: вычисляемые поля, не требующие доп. API-вызовов
type BlockMetrics struct {
	SeqNo     uint32    `json:"seqno"`
	Timestamp time.Time `json:"timestamp"`

	// --- Tier 1: базовые агрегаты (fast + detailed) ---
	TransactionCount int     `json:"transaction_count"`
	UniqueAddresses  int     `json:"unique_addresses"`
	TotalValue       float64 `json:"total_value"`    // в TON
	TotalGasUsed     uint64  `json:"total_gas_used"` // в нанотон
	AvgGasPrice      float64 `json:"avg_gas_price"`  // TON/tx
	ShardCount       int     `json:"shard_count"`

	// --- Tier 2: поведенческие метрики (только detailed режим) ---

	// Типы транзакций
	ExternalMsgCount  int `json:"external_msg_count"`  // входящие сообщения извне (external in)
	InternalMsgCount  int `json:"internal_msg_count"`  // межконтрактные вызовы
	ContractCallCount int `json:"contract_call_count"` // TX с вызовом смарт-контракта (non-empty body)
	ZeroValueTxCount  int `json:"zero_value_tx_count"` // TX с нулевым объёмом (спам / сервисные вызовы)

	// Объём
	MaxTxValue float64 `json:"max_tx_value"` // максимальный объём одной TX (TON)
	MinTxValue float64 `json:"min_tx_value"` // минимальный ненулевой объём TX (TON)

	// Адресная активность
	NewAddressCount   int     `json:"new_address_count"`   // адреса, впервые встреченные в блоке (требует внешнего состояния — заполняется consumer'ом)
	TopAddressShare   float64 `json:"top_address_share"`   // доля топ-1 адреса в общем объёме [0..1]
	AddressReuseRatio float64 `json:"address_reuse_ratio"` // UniqueAddresses / TransactionCount — ниже = больше концентрация

	// --- Computed: вычисляются из Tier 1/2, не требуют API ---
	TPS          float64 `json:"tps"`            // TransactionCount / BlockTime
	AvgTxValue   float64 `json:"avg_tx_value"`   // TotalValue / TransactionCount (TON)
	ValuePerAddr float64 `json:"value_per_addr"` // TotalValue / UniqueAddresses — концентрация объёма

	// --- Технические ---
	BlockTime   float64   `json:"block_time"`   // секунды с предыдущего блока
	ProcessedAt time.Time `json:"processed_at"` // когда scrapper обработал блок
	IsDetailed  bool      `json:"is_detailed"`  // true если заполнены Tier 2 поля

	// Внутреннее использование, не сериализуется
	Addresses []string `json:"-"`
}

// Compute заполняет вычисляемые поля на основе уже собранных данных.
// Вызывать после заполнения Tier 1/2.
func (m *BlockMetrics) Compute() {
	if m.BlockTime > 0 {
		m.TPS = float64(m.TransactionCount) / m.BlockTime
	}
	if m.TransactionCount > 0 {
		m.AvgTxValue = m.TotalValue / float64(m.TransactionCount)
		m.AddressReuseRatio = float64(m.UniqueAddresses) / float64(m.TransactionCount)
	}
	if m.UniqueAddresses > 0 {
		m.ValuePerAddr = m.TotalValue / float64(m.UniqueAddresses)
	}
}

// AggregatedMetrics — агрегированные метрики за скользящее окно.
// Используется как входной вектор для моделей (Isolation Forest, Autoencoder).
type AggregatedMetrics struct {
	PeriodStart time.Time `json:"period_start"`
	PeriodEnd   time.Time `json:"period_end"`

	TotalTransactions int     `json:"total_transactions"`
	ActiveAddresses   int     `json:"active_addresses"`
	TPS               float64 `json:"tps"`
	AvgBlockTime      float64 `json:"avg_block_time"`
	TotalVolume       float64 `json:"total_volume"`
	BlockCount        int     `json:"block_count"`

	// Агрегаты Tier 2 (для feature vector)
	AvgContractCallRatio float64 `json:"avg_contract_call_ratio"` // доля контрактных вызовов
	AvgZeroValueRatio    float64 `json:"avg_zero_value_ratio"`    // доля нулевых TX
	AvgTopAddressShare   float64 `json:"avg_top_address_share"`   // средняя концентрация адресов
	MaxTxValue           float64 `json:"max_tx_value"`            // пик объёма за период
}
