package collector

import (
	"context"
	"errors"
	"testing"
	"time"

	"ton-scrapper/models"

	"github.com/xssnick/tonutils-go/ton"
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

func TestProcessBlockFast_IsDetailedFalse(t *testing.T) {
	mock := &mockTonAPI{shards: makeShards(2)}
	p := NewTonStreamProcessorWithAPI(mock)

	metrics, err := p.ProcessBlockFast(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.IsDetailed {
		t.Error("ProcessBlockFast: IsDetailed должен быть false")
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
	txs := []ton.TransactionShortInfo{
		{Account: []byte{0x01, 0x02}},
		{Account: []byte{0x01, 0x02}}, // дубль
		{Account: []byte{0x03, 0x04}},
	}
	mock := &mockTonAPI{
		shards: nil,
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

func TestProcessBlockDetailed_IsDetailedTrue(t *testing.T) {
	mock := &mockTonAPI{
		shards: nil,
		txs:    []ton.TransactionShortInfo{},
	}
	p := NewTonStreamProcessorWithAPI(mock)

	metrics, err := p.ProcessBlockDetailed(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !metrics.IsDetailed {
		t.Error("ProcessBlockDetailed: IsDetailed должен быть true")
	}
}

func TestProcessBlockDetailed_TopAddressShare_SingleAddr(t *testing.T) {
	// Один адрес во всех TX → TopAddressShare должен быть 1.0
	txs := []ton.TransactionShortInfo{
		{Account: []byte{0xAA}},
		{Account: []byte{0xAA}},
		{Account: []byte{0xAA}},
	}
	mock := &mockTonAPI{shards: nil, txs: txs}
	p := NewTonStreamProcessorWithAPI(mock)

	metrics, err := p.ProcessBlockDetailed(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.TopAddressShare != 1.0 {
		t.Errorf("TopAddressShare: got %.3f, want 1.0 (один адрес занимает все TX)", metrics.TopAddressShare)
	}
}

func TestProcessBlockDetailed_TopAddressShare_EqualDistribution(t *testing.T) {
	// Два разных адреса в равном количестве TX → TopAddressShare = 0.5
	txs := []ton.TransactionShortInfo{
		{Account: []byte{0x01}},
		{Account: []byte{0x02}},
		{Account: []byte{0x01}},
		{Account: []byte{0x02}},
	}
	mock := &mockTonAPI{shards: nil, txs: txs}
	p := NewTonStreamProcessorWithAPI(mock)

	metrics, err := p.ProcessBlockDetailed(context.Background(), 11)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.TopAddressShare != 0.5 {
		t.Errorf("TopAddressShare: got %.3f, want 0.5", metrics.TopAddressShare)
	}
}

func TestProcessBlockDetailed_TopAddressShare_EmptyBlock(t *testing.T) {
	// Пустой блок → TopAddressShare = 0 (нет деления на ноль)
	mock := &mockTonAPI{shards: nil, txs: []ton.TransactionShortInfo{}}
	p := NewTonStreamProcessorWithAPI(mock)

	metrics, err := p.ProcessBlockDetailed(context.Background(), 12)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.TopAddressShare != 0 {
		t.Errorf("TopAddressShare: got %.3f, want 0.0 для пустого блока", metrics.TopAddressShare)
	}
}

func TestProcessBlockDetailed_AddressReuseRatio(t *testing.T) {
	// 2 уникальных адреса из 4 TX → ratio = 0.5
	txs := []ton.TransactionShortInfo{
		{Account: []byte{0x01}},
		{Account: []byte{0x01}},
		{Account: []byte{0x02}},
		{Account: []byte{0x02}},
	}
	mock := &mockTonAPI{shards: nil, txs: txs}
	p := NewTonStreamProcessorWithAPI(mock)

	metrics, err := p.ProcessBlockDetailed(context.Background(), 13)
	if err != nil {
		t.Fatal(err)
	}

	// Compute() устанавливает AddressReuseRatio = UniqueAddresses / TransactionCount
	if metrics.AddressReuseRatio != 0.5 {
		t.Errorf("AddressReuseRatio: got %.3f, want 0.5", metrics.AddressReuseRatio)
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

// --- Compute() integration ---

func TestProcessBlockDetailed_ComputeCalledOnReturn(t *testing.T) {
	// Проверяем что computed-поля не нулевые после вызова process-метода
	// (Compute вызывается внутри при BlockTime=0, TPS будет 0 — это корректно)
	txs := []ton.TransactionShortInfo{
		{Account: []byte{0x01}},
		{Account: []byte{0x02}},
	}
	mock := &mockTonAPI{shards: nil, txs: txs}
	p := NewTonStreamProcessorWithAPI(mock)

	metrics, err := p.ProcessBlockDetailed(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}

	// AddressReuseRatio = 2/2 = 1.0
	if metrics.AddressReuseRatio != 1.0 {
		t.Errorf("AddressReuseRatio: got %.3f, want 1.0", metrics.AddressReuseRatio)
	}
	// TPS = 0 т.к. BlockTime=0 (устанавливается снаружи)
	if metrics.TPS != 0 {
		t.Errorf("TPS: got %.3f, want 0 (BlockTime не установлен)", metrics.TPS)
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
