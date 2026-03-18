package collector

import (
	"context"
	"fmt"

	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/ton"
)

// mockTonAPI — тестовая реализация TonAPIClient.
// Все поля настраиваются перед тестом.
type mockTonAPI struct {
	// CurrentMasterchainInfo
	masterBlock *ton.BlockIDExt
	masterErr   error

	// LookupBlock
	lookedUpBlock *ton.BlockIDExt
	lookupErr     error

	// GetBlockData
	blockGenUtime uint32
	blockDataErr  error

	// GetBlockShardsInfo
	shards    []*ton.BlockIDExt
	shardsErr error

	// GetBlockTransactionsV2
	txs    []ton.TransactionShortInfo
	more   bool
	txsErr error

	// Счётчики вызовов (для проверки в тестах)
	lookupCalls int
	txsCalls    int
}

func (m *mockTonAPI) CurrentMasterchainInfo(_ context.Context) (*ton.BlockIDExt, error) {
	return m.masterBlock, m.masterErr
}

func (m *mockTonAPI) LookupBlock(_ context.Context, _ int32, _ int64, _ uint32) (*ton.BlockIDExt, error) {
	m.lookupCalls++
	if m.lookupErr != nil {
		return nil, m.lookupErr
	}
	if m.lookedUpBlock != nil {
		return m.lookedUpBlock, nil
	}
	return &ton.BlockIDExt{}, nil
}

func (m *mockTonAPI) GetBlockData(_ context.Context, _ *ton.BlockIDExt) (*tlb.Block, error) {
	if m.blockDataErr != nil {
		return nil, m.blockDataErr
	}
	return &tlb.Block{
		BlockInfo: tlb.BlockHeader{
			// GenUtime — unexported, поэтому используем zero value
			// (тест проверит Timestamp != zero через другой механизм)
		},
	}, nil
}

func (m *mockTonAPI) GetBlockShardsInfo(_ context.Context, _ *ton.BlockIDExt) ([]*ton.BlockIDExt, error) {
	return m.shards, m.shardsErr
}

func (m *mockTonAPI) GetBlockTransactionsV2(_ context.Context, _ *ton.BlockIDExt, _ uint32, _ ...*ton.TransactionID3) ([]ton.TransactionShortInfo, bool, error) {
	m.txsCalls++
	return m.txs, m.more, m.txsErr
}

// --- helpers ---

func makeBlock(seqno uint32) *ton.BlockIDExt {
	return &ton.BlockIDExt{SeqNo: seqno}
}

func makeTx(accountHex string) ton.TransactionShortInfo {
	// Account — []byte, представленный как hex-байты
	return ton.TransactionShortInfo{
		Account: []byte(accountHex),
		LT:      12345,
		Hash:    []byte("hash"),
	}
}

func makeShards(count int) []*ton.BlockIDExt {
	shards := make([]*ton.BlockIDExt, count)
	for i := range shards {
		shards[i] = &ton.BlockIDExt{SeqNo: uint32(i + 1)}
	}
	return shards
}

// compile-time check: mockTonAPI реализует TonAPIClient
var _ TonAPIClient = (*mockTonAPI)(nil)

// errBlockData — вспомогательная функция для мока с ошибкой GetBlockData
func errMock(msg string) *mockTonAPI {
	return &mockTonAPI{
		blockDataErr: fmt.Errorf("%s", msg),
	}
}
