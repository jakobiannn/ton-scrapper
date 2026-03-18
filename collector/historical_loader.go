package collector

import (
	"context"
	"log"
	"sync"
	"sync/atomic"

	"ton-scrapper/models"
)

type HistoricalLoader struct {
	processor   BlockProcessor
	workerCount int
}

// NewHistoricalLoader создаёт загрузчик на основе реального TON клиента.
func NewHistoricalLoader(client *TONClient, workerCount int) *HistoricalLoader {
	return &HistoricalLoader{
		processor:   NewTonStreamProcessor(client),
		workerCount: workerCount,
	}
}

// NewHistoricalLoaderWithProcessor создаёт загрузчик с кастомным процессором (для тестов).
func NewHistoricalLoaderWithProcessor(processor BlockProcessor, workerCount int) *HistoricalLoader {
	return &HistoricalLoader{
		processor:   processor,
		workerCount: workerCount,
	}
}

// LoadHistoricalBlocks загружает исторические блоки параллельно через worker pool.
// detailed=true → реальные TX и адреса (медленнее); false → только шарды (быстро).
func (h *HistoricalLoader) LoadHistoricalBlocks(ctx context.Context, startSeqno, endSeqno uint32, output chan<- *models.BlockMetrics, detailed bool) error {
	total := int(endSeqno - startSeqno + 1)
	log.Printf("Загрузка блоков %d–%d (%d блоков) | воркеров: %d | режим: %s",
		startSeqno, endSeqno, total, h.workerCount, modeLabel(detailed))

	jobs := make(chan uint32, h.workerCount*2)

	var wg sync.WaitGroup
	var processed atomic.Int64
	var failed atomic.Int64

	// Запускаем воркеры
	for i := 0; i < h.workerCount; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for seqno := range jobs {
				if ctx.Err() != nil {
					return
				}

				var metrics *models.BlockMetrics
				var err error

				if detailed {
					metrics, err = h.processor.ProcessBlockDetailed(ctx, seqno)
				} else {
					metrics, err = h.processor.ProcessBlockFast(ctx, seqno)
				}

				if err != nil {
					failed.Add(1)
					log.Printf("Воркер %d: ошибка блока %d: %v", workerID, seqno, err)
					continue
				}

				select {
				case output <- metrics:
				case <-ctx.Done():
					return
				}

				n := processed.Add(1)
				if n%500 == 0 {
					log.Printf("Прогресс: %d/%d блоков (%.1f%%)", n, total, float64(n)/float64(total)*100)
				}
			}
		}(i)
	}

	// Генератор задач
	go func() {
		defer close(jobs)
		for seqno := startSeqno; seqno <= endSeqno; seqno++ {
			select {
			case <-ctx.Done():
				return
			case jobs <- seqno:
			}
		}
	}()

	wg.Wait()

	log.Printf("Загрузка завершена: обработано %d блоков, ошибок %d",
		processed.Load(), failed.Load())

	return nil
}

func modeLabel(detailed bool) string {
	if detailed {
		return "detailed (реальные TX)"
	}
	return "fast (шарды)"
}
