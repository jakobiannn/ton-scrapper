package collector

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/xssnick/tonutils-go/ton"
	"ton-scrapper/models"
)

type TonStreamProcessor struct {
	api TonAPIClient
}

// NewTonStreamProcessor создаёт процессор, использующий реальный TON API.
func NewTonStreamProcessor(client *TONClient) *TonStreamProcessor {
	return &TonStreamProcessor{api: client.GetAPI()}
}

// NewTonStreamProcessorWithAPI создаёт процессор с кастомным API (для тестов).
func NewTonStreamProcessorWithAPI(api TonAPIClient) *TonStreamProcessor {
	return &TonStreamProcessor{api: api}
}

// SubscribeToBlocks подписывается на новые блоки в реальном времени.
// detailed=true → вызывает ProcessBlockDetailed (реальные TX), false → ProcessBlockFast (шарды).
func (p *TonStreamProcessor) SubscribeToBlocks(ctx context.Context, output chan<- *models.BlockMetrics, detailed bool) error {
	log.Println("Подписка на новые блоки TON...")

	currentBlock, err := p.api.CurrentMasterchainInfo(ctx)
	if err != nil {
		return fmt.Errorf("получение текущего блока: %w", err)
	}

	log.Printf("Начинаем с блока %d", currentBlock.SeqNo)

	lastSeqno := currentBlock.SeqNo
	var lastBlockTime time.Time

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			master, err := p.api.CurrentMasterchainInfo(ctx)
			if err != nil {
				log.Printf("Ошибка получения мастерблока: %v", err)
				continue
			}

			for seqno := lastSeqno + 1; seqno <= master.SeqNo; seqno++ {
				var metrics *models.BlockMetrics
				var processErr error

				if detailed {
					metrics, processErr = p.ProcessBlockDetailed(ctx, seqno)
				} else {
					metrics, processErr = p.ProcessBlockFast(ctx, seqno)
				}

				if processErr != nil {
					log.Printf("Ошибка обработки блока %d: %v", seqno, processErr)
					continue
				}

				if !lastBlockTime.IsZero() {
					metrics.BlockTime = metrics.Timestamp.Sub(lastBlockTime).Seconds()
				}
				lastBlockTime = metrics.Timestamp

				log.Printf("Блок %d | TX: %d | Адреса: %d | Шарды: %d | BlockTime: %.2fs",
					metrics.SeqNo, metrics.TransactionCount, metrics.UniqueAddresses,
					metrics.ShardCount, metrics.BlockTime)

				select {
				case output <- metrics:
				case <-ctx.Done():
					return ctx.Err()
				}

				lastSeqno = seqno
			}
		}
	}
}

// ProcessBlockFast — быстрый режим: только шарды, без анализа транзакций.
func (p *TonStreamProcessor) ProcessBlockFast(ctx context.Context, seqno uint32) (*models.BlockMetrics, error) {
	masterBlock, err := p.api.LookupBlock(ctx, -1, -9223372036854775808, seqno)
	if err != nil {
		return nil, fmt.Errorf("lookup block %d: %w", seqno, err)
	}

	blockData, err := p.api.GetBlockData(ctx, masterBlock)
	if err != nil {
		return nil, fmt.Errorf("get block data %d: %w", seqno, err)
	}

	metrics := &models.BlockMetrics{
		SeqNo:       seqno,
		Timestamp:   time.Unix(int64(blockData.BlockInfo.GenUtime), 0),
		ProcessedAt: time.Now(),
	}

	shards, err := p.api.GetBlockShardsInfo(ctx, masterBlock)
	if err != nil {
		log.Printf("Блок %d: не удалось получить шарды: %v", seqno, err)
	} else {
		metrics.ShardCount = len(shards)
		metrics.TransactionCount = len(shards)
	}

	return metrics, nil
}

// ProcessBlockDetailed — детальная обработка: реальное количество транзакций и уникальных адресов.
func (p *TonStreamProcessor) ProcessBlockDetailed(ctx context.Context, seqno uint32) (*models.BlockMetrics, error) {
	masterBlock, err := p.api.LookupBlock(ctx, -1, -9223372036854775808, seqno)
	if err != nil {
		return nil, fmt.Errorf("lookup block %d: %w", seqno, err)
	}

	blockData, err := p.api.GetBlockData(ctx, masterBlock)
	if err != nil {
		return nil, fmt.Errorf("get block data %d: %w", seqno, err)
	}

	metrics := &models.BlockMetrics{
		SeqNo:       seqno,
		Timestamp:   time.Unix(int64(blockData.BlockInfo.GenUtime), 0),
		ProcessedAt: time.Now(),
	}

	// Собираем все блоки: masterchain + шарды
	allBlocks := []*ton.BlockIDExt{masterBlock}
	shards, err := p.api.GetBlockShardsInfo(ctx, masterBlock)
	if err != nil {
		log.Printf("Блок %d: не удалось получить шарды: %v", seqno, err)
	} else {
		allBlocks = append(allBlocks, shards...)
		metrics.ShardCount = len(shards)
	}

	addressSet := make(map[string]struct{})
	totalTxCount := 0

	for _, block := range allBlocks {
		var after *ton.TransactionID3

		for {
			// Передаём after только если он не nil (variadic параметр)
			var txs []ton.TransactionShortInfo
			var more bool
			if after == nil {
				txs, more, err = p.api.GetBlockTransactionsV2(ctx, block, 256)
			} else {
				txs, more, err = p.api.GetBlockTransactionsV2(ctx, block, 256, after)
			}
			if err != nil {
				log.Printf("Блок %d: ошибка получения транзакций: %v", seqno, err)
				break
			}

			for _, tx := range txs {
				totalTxCount++
				if len(tx.Account) > 0 {
					addressSet[fmt.Sprintf("%x", tx.Account)] = struct{}{}
				}
			}

			if !more || len(txs) == 0 {
				break
			}

			// Курсор на последнюю транзакцию для следующей страницы
			last := txs[len(txs)-1]
			after = last.ID3()
		}
	}

	metrics.TransactionCount = totalTxCount
	metrics.UniqueAddresses = len(addressSet)

	return metrics, nil
}
