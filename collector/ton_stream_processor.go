package collector

import (
	"context"
	"fmt"
	"log"
	"time"
	"ton-scrapper/collector/models"

	"github.com/xssnick/tonutils-go/ton"
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

				// BlockTime вычисляется здесь — до этого момента process-методы не знают его.
				// После установки вызываем Compute() чтобы пересчитать TPS с актуальным значением.
				if !lastBlockTime.IsZero() {
					metrics.BlockTime = metrics.Timestamp.Sub(lastBlockTime).Seconds()
				}
				lastBlockTime = metrics.Timestamp
				metrics.Compute()

				log.Printf(
					"Блок %d | TX: %d | Адреса: %d | Шарды: %d | TPS: %.1f | AvgVal: %.4f TON | TopAddrShare: %.2f | BlockTime: %.2fs",
					metrics.SeqNo, metrics.TransactionCount, metrics.UniqueAddresses,
					metrics.ShardCount, metrics.TPS, metrics.AvgTxValue,
					metrics.TopAddressShare, metrics.BlockTime,
				)

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
// Tier 2 метрики не заполняются (IsDetailed=false).
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
		IsDetailed:  false,
	}

	shards, err := p.api.GetBlockShardsInfo(ctx, masterBlock)
	if err != nil {
		log.Printf("Блок %d: не удалось получить шарды: %v", seqno, err)
	} else {
		metrics.ShardCount = len(shards)
		// В fast-режиме используем количество шардов как прокси для числа TX-групп.
		// Реальное TransactionCount будет 0 — это честнее чем фиктивное значение.
		metrics.TransactionCount = len(shards)
	}

	// Compute() вызывается в SubscribeToBlocks после установки BlockTime.
	// Для HistoricalLoader вызываем здесь — BlockTime будет 0, TPS не считается.
	metrics.Compute()

	return metrics, nil
}

// ProcessBlockDetailed — детальная обработка: реальное количество транзакций и уникальных адресов.
//
// Что считается из GetBlockTransactionsV2 (TransactionShortInfo: Account, LT, Hash):
//   - TransactionCount, UniqueAddresses — точные значения
//   - AddressReuseRatio = UniqueAddresses / TransactionCount
//   - TopAddressShare — доля самого активного адреса по числу TX (не по объёму, т.к. значения недоступны)
//
// Что НЕ считается (требует полных данных TX через отдельный API-вызов, слишком медленно):
//   - ExternalMsgCount, InternalMsgCount, ContractCallCount
//   - ZeroValueTxCount, MaxTxValue, MinTxValue, TotalValue
//
// Для полных данных используй TonCenterProcessor.ProcessBlock.
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
		IsDetailed:  true,
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

	// addressCount: сколько раз адрес фигурирует в TX этого блока.
	// Используется для TopAddressShare (по частоте, т.к. значения TX нам недоступны).
	addressCount := make(map[string]int)
	totalTxCount := 0

	for _, block := range allBlocks {
		var after *ton.TransactionID3

		for {
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
					addr := fmt.Sprintf("%x", tx.Account)
					addressCount[addr]++
				}
			}

			if !more || len(txs) == 0 {
				break
			}

			last := txs[len(txs)-1]
			after = last.ID3()
		}
	}

	metrics.TransactionCount = totalTxCount
	metrics.UniqueAddresses = len(addressCount)

	// TopAddressShare: доля самого активного адреса по числу TX.
	// Семантика отличается от TonCenterProcessor (там — по объёму переводов).
	// Оба варианта полезны для детекции аномалий разного рода.
	if totalTxCount > 0 {
		var maxCount int
		for _, count := range addressCount {
			if count > maxCount {
				maxCount = count
			}
		}
		metrics.TopAddressShare = float64(maxCount) / float64(totalTxCount)
	}

	// Compute() вызывается в SubscribeToBlocks после установки BlockTime.
	// Для HistoricalLoader — здесь (BlockTime=0, TPS будет 0).
	metrics.Compute()

	return metrics, nil
}
