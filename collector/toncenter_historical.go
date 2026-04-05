package collector

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"ton-scrapper/collector/models"
)

// TonCenterBlockProcessor адаптирует TonCenterProcessor под BlockProcessor интерфейс.
// Используется для загрузки исторических блоков через TonCenter API (полный архив).
type TonCenterBlockProcessor struct {
	processor  *TonCenterProcessor
	rateLimit  time.Duration // интервал между запросами (rate limiting)
	lastReqMu  sync.Mutex
	lastReqAt  time.Time
}

// NewTonCenterBlockProcessor создаёт адаптер.
// ratePerSec: количество запросов в секунду (1 без ключа, 10 с ключом).
func NewTonCenterBlockProcessor(apiKey string, ratePerSec int) *TonCenterBlockProcessor {
	if ratePerSec <= 0 {
		ratePerSec = 1
	}
	// Добавляем 200ms буфер к интервалу для надёжности
	interval := time.Second/time.Duration(ratePerSec) + 200*time.Millisecond
	return &TonCenterBlockProcessor{
		processor: NewTonCenterProcessor(apiKey),
		rateLimit: interval,
	}
}

func (p *TonCenterBlockProcessor) throttle() {
	p.lastReqMu.Lock()
	defer p.lastReqMu.Unlock()

	elapsed := time.Since(p.lastReqAt)
	if elapsed < p.rateLimit {
		time.Sleep(p.rateLimit - elapsed)
	}
	p.lastReqAt = time.Now()
}

// ProcessBlockFast через TonCenter — один HTTP запрос на блок.
// Не загружает детали каждой транзакции (в отличие от ProcessBlock).
func (p *TonCenterBlockProcessor) ProcessBlockFast(ctx context.Context, seqno uint32) (*models.BlockMetrics, error) {
	p.throttle()

	blockTxs, err := p.processor.client.GetBlockTransactions(ctx, -1, "-9223372036854775808", int(seqno))
	if err != nil {
		return nil, err
	}

	// Собираем уникальные адреса из ShortTxInfo (без доп. запросов)
	addressMap := make(map[string]bool)
	for _, tx := range blockTxs.Transactions {
		addressMap[tx.Account] = true
	}

	metrics := &models.BlockMetrics{
		SeqNo:            uint32(seqno),
		Timestamp:        time.Now(), // будет уточнён при вставке
		TransactionCount: len(blockTxs.Transactions),
		UniqueAddresses:  len(addressMap),
		ShardCount:       1,
		ProcessedAt:      time.Now(),
	}

	if metrics.TransactionCount > 0 && metrics.UniqueAddresses > 0 {
		metrics.AvgGasPrice = 0 // недоступно без деталей TX
	}

	return metrics, nil
}

// ProcessBlockDetailed — то же что Fast для TonCenter (API не различает режимы).
func (p *TonCenterBlockProcessor) ProcessBlockDetailed(ctx context.Context, seqno uint32) (*models.BlockMetrics, error) {
	return p.ProcessBlockFast(ctx, seqno)
}

// TonCenterHistoricalLoader загружает блоки через TonCenter API.
// Однопоточный из-за rate limiting (1 req/sec без ключа).
type TonCenterHistoricalLoader struct {
	processor      *TonCenterBlockProcessor
	checkpointFile string
}

func NewTonCenterHistoricalLoader(apiKey string, ratePerSec int) *TonCenterHistoricalLoader {
	return &TonCenterHistoricalLoader{
		processor:      NewTonCenterBlockProcessor(apiKey, ratePerSec),
		checkpointFile: ".checkpoint_toncenter",
	}
}

// LoadBlocks загружает блоки от startSeqno до endSeqno через TonCenter API.
// Однопоточный — TonCenter без ключа: 1 req/sec.
func (l *TonCenterHistoricalLoader) LoadBlocks(
	ctx context.Context,
	startSeqno, endSeqno uint32,
	output chan<- *models.BlockMetrics,
) error {
	total := endSeqno - startSeqno + 1
	log.Printf("[TonCenter] Загрузка блоков %d–%d (%d блоков)", startSeqno, endSeqno, total)

	var processed, failed atomic.Int64
	startTime := time.Now()

	for seqno := startSeqno; seqno <= endSeqno; seqno++ {
		if ctx.Err() != nil {
			break
		}

		metrics, err := l.processor.ProcessBlockFast(ctx, seqno)
		if err != nil {
			failed.Add(1)
			if failed.Load()%100 == 1 {
				log.Printf("[TonCenter] Ошибка блока %d: %v", seqno, err)
			}
			continue
		}

		select {
		case output <- metrics:
		case <-ctx.Done():
			return ctx.Err()
		}

		n := processed.Add(1)
		if n%1000 == 0 {
			elapsed := time.Since(startTime)
			bps := float64(n) / elapsed.Seconds()
			remaining := time.Duration(float64(int64(total)-n)/bps) * time.Second
			log.Printf("[TonCenter] Прогресс: %d/%d (%.1f%%) | %.1f блоков/сек | осталось ~%v | ошибок: %d",
				n, total, float64(n)/float64(total)*100,
				bps, remaining.Round(time.Minute), failed.Load())
		}
	}

	log.Printf("[TonCenter] Завершено: %d блоков, %d ошибок", processed.Load(), failed.Load())
	return nil
}
