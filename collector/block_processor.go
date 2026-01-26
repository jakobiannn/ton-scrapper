package collector

import (
	"context"
	"log"
	"time"

	"ton-scrapper/models"
)

type TonAPIProcessor struct {
	client *TonAPIClient
}

func NewTonAPIProcessor(apiKey string) *TonAPIProcessor {
	return &TonAPIProcessor{
		client: NewTonAPIClient(apiKey),
	}
}

func (p *TonAPIProcessor) ProcessBlock(ctx context.Context, workchain int, shard string, seqno int) (*models.BlockMetrics, error) {
	// Получаем информацию о блоке
	blockInfo, err := p.client.GetBlockInfo(ctx, workchain, shard, seqno)
	if err != nil {
		return nil, err
	}

	// Получаем транзакции блока
	txResponse, err := p.client.GetBlockTransactions(ctx, workchain, shard, seqno)
	if err != nil {
		return nil, err
	}

	metrics := &models.BlockMetrics{
		SeqNo:     uint32(seqno),
		Timestamp: time.Unix(blockInfo.GenUtime, 0),
	}

	// Обрабатываем транзакции
	addressMap := make(map[string]bool)
	var totalValue uint64
	var totalGas uint64

	for _, tx := range txResponse.Transactions {
		// Добавляем адрес аккаунта
		addressMap[tx.Account.Address] = true

		// Обрабатываем входящее сообщение
		if tx.InMsg != nil && tx.InMsg.Source != nil {
			addressMap[tx.InMsg.Source.Address] = true
		}

		// Обрабатываем исходящие сообщения
		for _, msg := range tx.OutMsgs {
			if msg.Destination != nil {
				addressMap[msg.Destination.Address] = true
			}
			totalValue += uint64(msg.Value)
		}

		// Суммируем комиссии
		totalGas += uint64(tx.TotalFees)
	}

	metrics.TransactionCount = len(txResponse.Transactions)
	metrics.UniqueAddresses = len(addressMap)
	metrics.TotalValue = float64(totalValue) / 1e9
	metrics.TotalGasUsed = totalGas

	if metrics.TransactionCount > 0 {
		metrics.AvgGasPrice = float64(totalGas) / float64(metrics.TransactionCount) / 1e9
	}

	return metrics, nil
}

func (p *TonAPIProcessor) StreamBlocks(ctx context.Context, startSeqno uint32, output chan<- *models.BlockMetrics) error {
	log.Printf("Начинаем стриминг с блока %d через TonAPI", startSeqno)

	var lastBlockTime time.Time
	lastSeqno := int(startSeqno)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			// Получаем последние блоки
			blocks, err := p.client.GetLatestBlocks(ctx, 10)
			if err != nil {
				log.Printf("Ошибка получения блоков: %v", err)
				time.Sleep(5 * time.Second)
				continue
			}

			if len(blocks.Blocks) == 0 {
				time.Sleep(5 * time.Second)
				continue
			}

			// Берем самый свежий блок мастерчейна
			var latestMasterBlock *BlockInfo
			for i := range blocks.Blocks {
				if blocks.Blocks[i].Workchain == -1 {
					latestMasterBlock = &blocks.Blocks[i]
					break
				}
			}

			if latestMasterBlock == nil {
				time.Sleep(5 * time.Second)
				continue
			}

			// Обрабатываем новые блоки
			for seqno := lastSeqno; seqno <= latestMasterBlock.Seqno; seqno++ {
				metrics, err := p.ProcessBlock(ctx, -1, "-9223372036854775808", seqno)
				if err != nil {
					log.Printf("Ошибка обработки блока %d: %v", seqno, err)
					time.Sleep(1 * time.Second)
					continue
				}

				if !lastBlockTime.IsZero() {
					metrics.BlockTime = metrics.Timestamp.Sub(lastBlockTime).Seconds()
				}
				lastBlockTime = metrics.Timestamp

				log.Printf("Блок %d | TX: %d | Адреса: %d | Объём: %.2f TON | Газ: %.4f TON | BlockTime: %.2fs",
					metrics.SeqNo, metrics.TransactionCount, metrics.UniqueAddresses,
					metrics.TotalValue, metrics.AvgGasPrice*float64(metrics.TransactionCount), metrics.BlockTime)

				output <- metrics
				lastSeqno = seqno + 1
			}

			time.Sleep(5 * time.Second)
		}
	}
}
