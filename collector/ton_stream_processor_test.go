package collector

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xssnick/tonutils-go/ton"
	"ton-scrapper/models"
)

// --- ProcessBlockFast ---

func TestProcessBlockFast_Success(t *testing.T) {
	mock := &mockTonAPI{
		shards: makeShards(3),
	}
	p := NewTonStreamProcessorWithAPI(mock)

	metrics, err := p.ProcessBlockFast(context.Background(), 100)
	if err != nil {
		t.Fatalf("ProcessBlockFast: неожиданная ошибка: %v", err)
	}
	if metrics.SeqNo != 100 {
		t.Errorf("SeqNo: got %d, want 100", metrics.SeqNo)
	}
	if metrics.ShardCount != 3 {
		t.Errorf("ShardCount: got %d, want 3", metrics.ShardCount)
	}
	if metrics.TransactionCount != 3 {
		t.Errorf("TransactionCount (fast=shards): got %d, want 3", metrics.TransactionCount)
	}
	if metrics.ProcessedAt.IsZero() {
		t.Error("ProcessedAt не должен быть zero")
	}
}

func TestProcessBlockFast_LookupError(t *testing.T) {
	mock := &mockTonAPI{
		lookupErr: errors.New("lookup failed"),
	}
	p := NewTonStreamProcessorWithAPI(mock)

	_, err := p.ProcessBlockFast(context.Background(), 100)
	if err == nil {
		t.Fatal("ожидали ошибку при lookup failure")
	}
}

func TestProcessBlockFast_BlockDataError(t *testing.T) {
	mock := &mockTonAPI{
		blockDataErr: errors.New("block data failed"),
	}
	p := NewTonStreamProcessorWithAPI(mock)

	_, err := p.ProcessBlockFast(context.Background(), 100)
	if err == nil {
		t.Fatal("ожидали ошибку при GetBlockData failure")
	}
}

func TestProcessBlockFast_ShardsError_GracefulDegradation(t *testing.T) {
	// Если шарды недоступны — возвращаем метрики без них (ShardCount=0)
	mock := &mockTonAPI{
		shardsErr: errors.New("shards unavailable"),
	}
	p := NewTonStreamProcessorWithAPI(mock)

	metrics, err := p.ProcessBlockFast(context.Background(), 100)
	if err != nil {
		t.Fatalf("ProcessBlockFast не должен возвращать ошибку при shardsErr: %v", err)
	}
	if metrics.ShardCount != 0 {
		t.Errorf("ShardCount должен быть 0 при ошибке шардов, получили %d", metrics.ShardCount)
	}
}

func TestProcessBlockFast_ZeroShards(t *testing.T) {
	mock := &mockTonAPI{
		shards: []*ton.BlockIDExt{},
	}
	p := NewTonStreamProcessorWithAPI(mock)

	metrics, err := p.ProcessBlockFast(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.ShardCount != 0 || metrics.TransactionCount != 0 {
		t.Errorf("ожидали 0 шардов/транзакций, получили shards=%d tx=%d",
			metrics.ShardCount, metrics.TransactionCount)
	}
}

// --- ProcessBlockDetailed ---

func TestProcessBlockDetailed_CountsTransactions(t *testing.T) {
	txs := []ton.TransactionShortInfo{
		makeTx("addr1"),
		makeTx("addr2"),
		makeTx("addr3"),
	}
	mock := &mockTonAPI{
		shards: makeShards(1),
		txs:    txs,
		more:   false,
	}
	p := NewTonStreamProcessorWithAPI(mock)

	metrics, err := p.ProcessBlockDetailed(context.Background(), 200)
	if err != nil {
		t.Fatalf("ProcessBlockDetailed: %v", err)
	}
	if metrics.SeqNo != 200 {
		t.Errorf("SeqNo: got %d, want 200", metrics.SeqNo)
	}

	// masterchain + 1 shard = 2 блока, каждый содержит 3 TX
	expectedTx := 3 * 2
	if metrics.TransactionCount != expectedTx {
		t.Errorf("TransactionCount: got %d, want %d", metrics.TransactionCount, expectedTx)
	}
}

func TestProcessBlockDetailed_CountsUniqueAddresses(t *testing.T) {
	// Одинаковые адреса должны считаться один раз
	txs := []ton.TransactionShortInfo{
		{Account: []byte{0x01, 0x02}},
		{Account: []byte{0x01, 0x02}}, // дубль
		{Account: []byte{0x03, 0x04}},
	}
	mock := &mockTonAPI{
		shards: nil, // только masterchain (1 блок)
		txs:    txs,
		more:   false,
	}
	p := NewTonStreamProcessorWithAPI(mock)

	metrics, err := p.ProcessBlockDetailed(context.Background(), 300)
	if err != nil {
		t.Fatalf("ProcessBlockDetailed: %v", err)
	}
	if metrics.UniqueAddresses != 2 {
		t.Errorf("UniqueAddresses: got %d, want 2 (дубли должны дедупроваться)", metrics.UniqueAddresses)
	}
}

func TestProcessBlockDetailed_EmptyBlock(t *testing.T) {
	mock := &mockTonAPI{
		shards: nil,
		txs:    []ton.TransactionShortInfo{},
		more:   false,
	}
	p := NewTonStreamProcessorWithAPI(mock)

	metrics, err := p.ProcessBlockDetailed(context.Background(), 400)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.TransactionCount != 0 {
		t.Errorf("TransactionCount: got %d, want 0", metrics.TransactionCount)
	}
	if metrics.UniqueAddresses != 0 {
		t.Errorf("UniqueAddresses: got %d, want 0", metrics.UniqueAddresses)
	}
}

func TestProcessBlockDetailed_LookupError(t *testing.T) {
	mock := &mockTonAPI{
		lookupErr: errors.New("lookup error"),
	}
	p := NewTonStreamProcessorWithAPI(mock)

	_, err := p.ProcessBlockDetailed(context.Background(), 100)
	if err == nil {
		t.Fatal("ожидали ошибку при lookup failure")
	}
}

func TestProcessBlockDetailed_TxsError_ContinuesGracefully(t *testing.T) {
	// Ошибка GetBlockTransactionsV2 не должна ломать всю обработку
	mock := &mockTonAPI{
		shards: nil,
		txsErr: errors.New("tx fetch failed"),
	}
	p := NewTonStreamProcessorWithAPI(mock)

	metrics, err := p.ProcessBlockDetailed(context.Background(), 100)
	if err != nil {
		t.Fatalf("ProcessBlockDetailed не должен возвращать ошибку при txsErr: %v", err)
	}
	if metrics.TransactionCount != 0 {
		t.Errorf("TransactionCount должен быть 0 при ошибке TX: %d", metrics.TransactionCount)
	}
}

func TestProcessBlockDetailed_ShardCountSet(t *testing.T) {
	mock := &mockTonAPI{
		shards: makeShards(4),
		txs:    []ton.TransactionShortInfo{},
	}
	p := NewTonStreamProcessorWithAPI(mock)

	metrics, err := p.ProcessBlockDetailed(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.ShardCount != 4 {
		t.Errorf("ShardCount: got %d, want 4", metrics.ShardCount)
	}
}

// --- SubscribeToBlocks ---

func TestSubscribeToBlocks_ContextCancellation(t *testing.T) {
	mock := &mockTonAPI{
		masterBlock: makeBlock(1000),
	}
	p := NewTonStreamProcessorWithAPI(mock)
	out := make(chan *models.BlockMetrics, 10)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := p.SubscribeToBlocks(ctx, out, false)

	if err == nil {
		t.Fatal("ожидали ошибку (context cancelled)")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Errorf("ожидали context error, получили: %v", err)
	}
}

func TestSubscribeToBlocks_MasterchainInfoError(t *testing.T) {
	// SubscribeToBlocks сразу вызывает CurrentMasterchainInfo — должен вернуть ошибку
	mock := &mockTonAPI{
		masterErr: errors.New("нет соединения с TON"),
	}
	p := NewTonStreamProcessorWithAPI(mock)
	out := make(chan *models.BlockMetrics, 10)

	err := p.SubscribeToBlocks(context.Background(), out, false)
	if err == nil {
		t.Fatal("ожидали ошибку при сбое CurrentMasterchainInfo")
	}
}
