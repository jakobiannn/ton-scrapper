package models

import (
	"encoding/json"
	"testing"
	"time"
)

// --- JSON сериализация BlockMetrics ---

func TestBlockMetrics_JSONMarshal(t *testing.T) {
	ts := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	m := &BlockMetrics{
		SeqNo:            42_000_000,
		Timestamp:        ts,
		TransactionCount: 150,
		UniqueAddresses:  80,
		TotalValue:       1234.56,
		TotalGasUsed:     500_000_000,
		AvgGasPrice:      3_333_333.33,
		BlockTime:        4.9,
		ShardCount:       4,
		ProcessedAt:      ts,
	}

	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	// Десериализуем обратно
	var got BlockMetrics
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.SeqNo != m.SeqNo {
		t.Errorf("SeqNo: got %d, want %d", got.SeqNo, m.SeqNo)
	}
	if got.TransactionCount != m.TransactionCount {
		t.Errorf("TransactionCount: got %d, want %d", got.TransactionCount, m.TransactionCount)
	}
	if got.UniqueAddresses != m.UniqueAddresses {
		t.Errorf("UniqueAddresses: got %d, want %d", got.UniqueAddresses, m.UniqueAddresses)
	}
	if got.TotalValue != m.TotalValue {
		t.Errorf("TotalValue: got %f, want %f", got.TotalValue, m.TotalValue)
	}
	if got.BlockTime != m.BlockTime {
		t.Errorf("BlockTime: got %f, want %f", got.BlockTime, m.BlockTime)
	}
	if got.ShardCount != m.ShardCount {
		t.Errorf("ShardCount: got %d, want %d", got.ShardCount, m.ShardCount)
	}
}

func TestBlockMetrics_AddressesNotSerialized(t *testing.T) {
	m := &BlockMetrics{
		SeqNo:     1,
		Addresses: []string{"addr1", "addr2"},
	}

	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	// Addresses имеет json:"-", значит его не должно быть в JSON
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal to map: %v", err)
	}
	if _, exists := raw["addresses"]; exists {
		t.Error("поле 'addresses' не должно сериализоваться в JSON (json:\"-\")")
	}
	if _, exists := raw["-"]; exists {
		t.Error("поле '-' не должно сериализоваться")
	}
}

func TestBlockMetrics_ZeroValue(t *testing.T) {
	var m BlockMetrics
	data, err := json.Marshal(&m)
	if err != nil {
		t.Fatalf("Marshal zero value: %v", err)
	}

	var got BlockMetrics
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal zero value: %v", err)
	}
	if got.SeqNo != 0 || got.TransactionCount != 0 {
		t.Error("zero value round-trip failed")
	}
}

func TestBlockMetrics_JSONFieldNames(t *testing.T) {
	m := &BlockMetrics{
		SeqNo:            1,
		TransactionCount: 10,
		UniqueAddresses:  5,
		ShardCount:       2,
		BlockTime:        4.5,
		TotalValue:       100.0,
		TotalGasUsed:     1000,
		AvgGasPrice:      100.0,
	}

	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var raw map[string]interface{}
	json.Unmarshal(data, &raw)

	expectedKeys := []string{
		"seqno", "timestamp", "transaction_count", "unique_addresses",
		"total_value", "total_gas_used", "avg_gas_price",
		"block_time", "shard_count", "processed_at",
	}
	for _, key := range expectedKeys {
		if _, ok := raw[key]; !ok {
			t.Errorf("JSON ключ %q не найден в сериализованных данных", key)
		}
	}
}

// --- JSON сериализация AggregatedMetrics ---

func TestAggregatedMetrics_JSONRoundTrip(t *testing.T) {
	ts := time.Now().UTC().Truncate(time.Second)
	m := &AggregatedMetrics{
		PeriodStart:       ts,
		PeriodEnd:         ts.Add(time.Minute),
		TotalTransactions: 5000,
		ActiveAddresses:   300,
		TPS:               83.3,
		AvgBlockTime:      4.8,
		TotalVolume:       100000.5,
		BlockCount:        12,
	}

	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal AggregatedMetrics: %v", err)
	}

	var got AggregatedMetrics
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal AggregatedMetrics: %v", err)
	}

	if got.TotalTransactions != m.TotalTransactions {
		t.Errorf("TotalTransactions: got %d, want %d", got.TotalTransactions, m.TotalTransactions)
	}
	if got.BlockCount != m.BlockCount {
		t.Errorf("BlockCount: got %d, want %d", got.BlockCount, m.BlockCount)
	}
	if got.TPS != m.TPS {
		t.Errorf("TPS: got %f, want %f", got.TPS, m.TPS)
	}
}
