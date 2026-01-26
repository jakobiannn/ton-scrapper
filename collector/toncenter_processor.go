package collector

import (
	"context"
	"log"
	"strconv"
	"time"

	"ton-scrapper/models"
)

type TonCenterProcessor struct {
	client *TonCenterClient
}

func NewTonCenterProcessor(apiKey string) *TonCenterProcessor {
	return &TonCenterProcessor{
		client: NewTonCenterClient(apiKey),
	}
}

func (p *TonCenterProcessor) ProcessBlock(ctx context.Context, workchain int, shard string, seqno int) (*models.BlockMetrics, error) {
	// Получаем список транзакций в блоке
	blockTxs, err := p.client.GetBlockTransactions(ctx, workchain, shard, seqno)
	if err != nil {
		return nil, err
	}

	metrics := &models.BlockMetrics{
		SeqNo:     uint32(seqno),
		Timestamp: time.Now(),
	}

	addressMap := make(map[string]bool)
	var totalValue uint64
	var totalGas uint64
	var firstTxTime int64

	// Обрабатываем каждую транзакцию
	for i, shortTx := range blockTxs.Transactions {
		// Получаем детали транзакции
		txDetails, err := p.client.GetTransactions(ctx, shortTx.Account, shortTx.Lt, shortTx.Hash, 1)
		if err != nil {
			log.Printf("Ошибка получения деталей транзакции: %v", err)
			continue
		}

		if len(txDetails) == 0 {
			continue
		}

		tx := txDetails[0]

		// Берем время из первой транзакции
		if i == 0 {
			firstTxTime = tx.Now
			metrics.Timestamp = time.Unix(firstTxTime, 0)
		}

		// Добавляем адрес аккаунта
		addressMap[tx.Account] = true

		// Обрабатываем входящее сообщение
		if tx.InMsg != nil {
			if tx.InMsg.Source != "" {
				addressMap[tx.InMsg.Source] = true
			}
			if tx.InMsg.Value != "" {
				if val, err := strconv.ParseUint(tx.InMsg.Value, 10, 64); err == nil {
					totalValue += val
				}
			}
		}

		// Обрабатываем исходящие сообщения
		for _, msg := range tx.OutMsgs {
			if msg.Destination != "" {
				addressMap[msg.Destination] = true
			}
			if msg.Value != "" {
				if val, err := strconv.ParseUint(msg.Value, 10, 64); err == nil {
					totalValue += val
				}
			}
		}

		// Суммируем комиссии
		if tx.TotalFees != "" {
			if fees, err := strconv.ParseUint(tx.TotalFees, 10, 64); err == nil {
				totalGas += fees
			}
		}
	}

	metrics.TransactionCount = len(blockTxs.Transactions)
	metrics.UniqueAddresses = len(addressMap)
	metrics.TotalValue = float64(totalValue) / 1e9
	metrics.TotalGasUsed = totalGas

	if metrics.TransactionCount > 0 {
		metrics.AvgGasPrice = float64(totalGas) / float64(metrics.TransactionCount) / 1e9
	}

	return metrics, nil
}

func (p *TonCenterProcessor) StreamBlocks(ctx context.Context, startSeqno uint32, output chan<- *models.BlockMetrics) error {
	log.Printf("Начинаем стриминг с блока %d через TonCenter", startSeqno)

	var lastBlockTime time.Time
	currentSeqno := int(startSeqno)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			// Получаем информацию о последнем блоке
			info, err := p.client.GetMasterchainInfo(ctx)
			if err != nil {
				log.Printf("Ошибка получения мастерчейн: %v", err)
				time.Sleep(5 * time.Second)
				continue
			}

			// Обрабатываем новые блоки
			for currentSeqno <= info.Last.Seqno {
				metrics, err := p.ProcessBlock(ctx, info.Last.Workchain, info.Last.Shard, currentSeqno)
				if err != nil {
					log.Printf("Ошибка обработки блока %d: %v", currentSeqno, err)
					currentSeqno++
					time.Sleep(1 * time.Second)
					continue
				}

				if !lastBlockTime.IsZero() {
					metrics.BlockTime = metrics.Timestamp.Sub(lastBlockTime).Seconds()
				}
				lastBlockTime = metrics.Timestamp

				log.Printf("Блок %d | TX: %d | Адреса: %d | Объём: %.2f TON | Газ: %.4f TON | BlockTime: %.2fs",
					metrics.SeqNo, metrics.TransactionCount, metrics.UniqueAddresses,
					metrics.TotalValue, float64(metrics.TotalGasUsed)/1e9, metrics.BlockTime)

				output <- metrics
				currentSeqno++
			}

			time.Sleep(5 * time.Second)
		}
	}
}
