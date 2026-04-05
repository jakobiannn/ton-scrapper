package collector

import (
	"context"
	"log"
	"math"
	"strconv"
	"time"
	"ton-scrapper/collector/models"
)

type TonCenterProcessor struct {
	client *TonCenterClient
}

func NewTonCenterProcessor(apiKey string) *TonCenterProcessor {
	return &TonCenterProcessor{
		client: NewTonCenterClient(apiKey),
	}
}

// ProcessBlock получает полные данные TX через TonCenter API и вычисляет все метрики,
// включая Tier 2: типы сообщений, объёмы, TopAddressShare по стоимости.
//
// В отличие от TonStreamProcessor.ProcessBlockDetailed, здесь доступны полные данные
// каждой транзакции (InMsg, OutMsgs, значения, комиссии), что позволяет заполнить
// ExternalMsgCount, InternalMsgCount, ContractCallCount, ZeroValueTxCount, MaxTxValue и т.д.
//
// Ограничение: N+1 запросов к API (один GetBlockTransactions + по одному GetTransactions
// на каждую TX). Для блоков с большим количеством TX используй TonStreamProcessor.
func (p *TonCenterProcessor) ProcessBlock(ctx context.Context, workchain int, shard string, seqno int) (*models.BlockMetrics, error) {
	blockTxs, err := p.client.GetBlockTransactions(ctx, workchain, shard, seqno)
	if err != nil {
		return nil, err
	}

	metrics := &models.BlockMetrics{
		SeqNo:      uint32(seqno),
		Timestamp:  time.Now(),
		IsDetailed: true,
	}

	// --- счётчики Tier 1 ---
	var (
		totalValueNano uint64            // сумма входящих значений (нанотон)
		totalGasNano   uint64            // сумма комиссий (нанотон)
		maxTxValue     float64           // максимальный объём одной TX (TON)
		minTxValue     = math.MaxFloat64 // минимальный ненулевой объём TX (TON)
	)

	// --- счётчики Tier 2 ---
	var (
		externalMsgCount  int // InMsg.Source == "" → сообщение пришло снаружи блокчейна
		internalMsgCount  int // InMsg.Source != "" → межконтрактный вызов
		contractCallCount int // len(OutMsgs) > 0 → TX породила исходящие сообщения
		zeroValueTxCount  int // входящее значение == 0 (сервисный вызов / спам)
	)

	// --- для адресных метрик ---
	addressValueMap := make(map[string]float64) // адрес → суммарный объём входящих (TON)
	addressSeen := make(map[string]struct{})    // дедупликация уникальных адресов

	// --- обработка транзакций ---
	for i, shortTx := range blockTxs.Transactions {
		txDetails, err := p.client.GetTransactions(ctx, shortTx.Account, shortTx.Lt, shortTx.Hash, 1)
		if err != nil {
			log.Printf("Блок %d: ошибка деталей TX %s: %v", seqno, shortTx.Hash, err)
			continue
		}
		if len(txDetails) == 0 {
			continue
		}

		tx := txDetails[0]

		// Время блока — берём из первой успешной TX
		if i == 0 {
			metrics.Timestamp = time.Unix(tx.Now, 0)
		}

		// Аккаунт транзакции
		addressSeen[tx.Account] = struct{}{}

		// --- Входящее сообщение ---
		var txValueNano uint64

		if tx.InMsg != nil {
			if tx.InMsg.Source == "" {
				// External message: инициировано кошельком пользователя вне блокчейна.
				// Характерно для пользовательских транзакций (deploy, transfer, вызов контракта).
				externalMsgCount++
			} else {
				// Internal message: контракт-инициатор внутри TON.
				// Характерно для межконтрактных вызовов и цепочек транзакций.
				internalMsgCount++
				addressSeen[tx.InMsg.Source] = struct{}{}
			}

			if tx.InMsg.Value != "" && tx.InMsg.Value != "0" {
				if val, err := strconv.ParseUint(tx.InMsg.Value, 10, 64); err == nil {
					txValueNano = val
					totalValueNano += val
				}
			}
		}

		// Объём в TON для этой TX
		txValueTON := float64(txValueNano) / 1e9

		// --- Исходящие сообщения ---
		if len(tx.OutMsgs) > 0 {
			// TX породила исходящие сообщения → смарт-контракт выполнил логику.
			// Proxy для "ContractCallCount": не 100% точен (простой transfer тоже
			// порождает OutMsg), но для anomaly detection достаточно.
			contractCallCount++
		}

		for _, msg := range tx.OutMsgs {
			if msg.Destination != "" {
				addressSeen[msg.Destination] = struct{}{}
			}
		}

		// --- Zero-value ---
		if txValueNano == 0 {
			zeroValueTxCount++
		}

		// --- Max / Min TX value ---
		if txValueTON > maxTxValue {
			maxTxValue = txValueTON
		}
		if txValueNano > 0 && txValueTON < minTxValue {
			minTxValue = txValueTON
		}

		// --- Адресная стоимость (для TopAddressShare) ---
		addressValueMap[tx.Account] += txValueTON

		// --- Газ ---
		if tx.TotalFees != "" {
			if fees, err := strconv.ParseUint(tx.TotalFees, 10, 64); err == nil {
				totalGasNano += fees
			}
		}
	}

	txCount := len(blockTxs.Transactions)

	// --- Tier 1 ---
	metrics.TransactionCount = txCount
	metrics.UniqueAddresses = len(addressSeen)
	metrics.TotalValue = float64(totalValueNano) / 1e9
	metrics.TotalGasUsed = totalGasNano
	if txCount > 0 {
		metrics.AvgGasPrice = float64(totalGasNano) / float64(txCount) / 1e9
	}

	// --- Tier 2: типы сообщений ---
	metrics.ExternalMsgCount = externalMsgCount
	metrics.InternalMsgCount = internalMsgCount
	metrics.ContractCallCount = contractCallCount
	metrics.ZeroValueTxCount = zeroValueTxCount

	// --- Tier 2: объёмы ---
	metrics.MaxTxValue = maxTxValue
	if minTxValue == math.MaxFloat64 {
		metrics.MinTxValue = 0 // все TX были нулевыми
	} else {
		metrics.MinTxValue = minTxValue
	}

	// --- Tier 2: адресная концентрация ---
	// TopAddressShare: какую долю суммарного объёма контролирует самый активный адрес.
	// Высокое значение (> 0.5) может сигнализировать о wash trading или drain-атаке.
	if metrics.TotalValue > 0 {
		var topValue float64
		for _, v := range addressValueMap {
			if v > topValue {
				topValue = v
			}
		}
		metrics.TopAddressShare = topValue / metrics.TotalValue
	}

	metrics.ProcessedAt = time.Now()

	// Compute() заполняет AvgTxValue, ValuePerAddr, AddressReuseRatio.
	// BlockTime устанавливается в StreamBlocks после вызова — там Compute() вызывается повторно.
	metrics.Compute()

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
			info, err := p.client.GetMasterchainInfo(ctx)
			if err != nil {
				log.Printf("Ошибка получения мастерчейн: %v", err)
				time.Sleep(5 * time.Second)
				continue
			}

			for currentSeqno <= info.Last.Seqno {
				metrics, err := p.ProcessBlock(ctx, info.Last.Workchain, info.Last.Shard, currentSeqno)
				if err != nil {
					log.Printf("Ошибка обработки блока %d: %v", currentSeqno, err)
					currentSeqno++
					time.Sleep(1 * time.Second)
					continue
				}

				// BlockTime известен только здесь — пересчитываем TPS через Compute().
				if !lastBlockTime.IsZero() {
					metrics.BlockTime = metrics.Timestamp.Sub(lastBlockTime).Seconds()
					metrics.Compute()
				}
				lastBlockTime = metrics.Timestamp

				log.Printf(
					"Блок %d | TX: %d | Addr: %d | Vol: %.2f TON | Gas: %.4f TON | "+
						"Ext: %d | Int: %d | Contracts: %d | ZeroVal: %d | TopShare: %.2f | BlockTime: %.2fs",
					metrics.SeqNo, metrics.TransactionCount, metrics.UniqueAddresses,
					metrics.TotalValue, float64(metrics.TotalGasUsed)/1e9,
					metrics.ExternalMsgCount, metrics.InternalMsgCount,
					metrics.ContractCallCount, metrics.ZeroValueTxCount,
					metrics.TopAddressShare, metrics.BlockTime,
				)

				output <- metrics
				currentSeqno++
			}

			time.Sleep(5 * time.Second)
		}
	}
}
