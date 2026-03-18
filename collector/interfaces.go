package collector

import (
	"context"

	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/ton"
	"ton-scrapper/models"
)

// TonAPIClient абстрагирует TON blockchain API для тестируемости.
// *ton.APIClient реализует этот интерфейс.
type TonAPIClient interface {
	CurrentMasterchainInfo(ctx context.Context) (*ton.BlockIDExt, error)
	LookupBlock(ctx context.Context, workchain int32, shard int64, seqno uint32) (*ton.BlockIDExt, error)
	GetBlockData(ctx context.Context, block *ton.BlockIDExt) (*tlb.Block, error)
	GetBlockShardsInfo(ctx context.Context, master *ton.BlockIDExt) ([]*ton.BlockIDExt, error)
	GetBlockTransactionsV2(ctx context.Context, block *ton.BlockIDExt, count uint32, after ...*ton.TransactionID3) ([]ton.TransactionShortInfo, bool, error)
}

// Compile-time проверка: *ton.APIClient должен реализовывать TonAPIClient.
var _ TonAPIClient = (*ton.APIClient)(nil)

// BlockProcessor обрабатывает отдельные блоки и возвращает метрики.
// Используется для dependency injection в HistoricalLoader.
type BlockProcessor interface {
	ProcessBlockFast(ctx context.Context, seqno uint32) (*models.BlockMetrics, error)
	ProcessBlockDetailed(ctx context.Context, seqno uint32) (*models.BlockMetrics, error)
}
