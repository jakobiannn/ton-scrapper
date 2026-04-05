package collector

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ton-scrapper/collector/models"
)

type HistoricalLoader struct {
	processor      BlockProcessor
	workerCount    int
	checkpointFile string
	maxRetries     int
}

// NewHistoricalLoader создаёт загрузчик на основе реального TON клиента.
func NewHistoricalLoader(client *TONClient, workerCount int) *HistoricalLoader {
	return &HistoricalLoader{
		processor:      NewTonStreamProcessor(client),
		workerCount:    workerCount,
		checkpointFile: ".checkpoint",
		maxRetries:     3,
	}
}

// NewHistoricalLoaderWithProcessor создаёт загрузчик с кастомным процессором (для тестов).
func NewHistoricalLoaderWithProcessor(processor BlockProcessor, workerCount int) *HistoricalLoader {
	return &HistoricalLoader{
		processor:      processor,
		workerCount:    workerCount,
		checkpointFile: ".checkpoint",
		maxRetries:     3,
	}
}

// SetCheckpointFile задаёт путь к файлу checkpoint для возобновления.
func (h *HistoricalLoader) SetCheckpointFile(path string) {
	h.checkpointFile = path
}

// LoadCheckpoint загружает последний обработанный seqno из файла.
func (h *HistoricalLoader) LoadCheckpoint() (uint32, error) {
	data, err := os.ReadFile(h.checkpointFile)
	if err != nil {
		return 0, err
	}
	val, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 32)
	if err != nil {
		return 0, err
	}
	return uint32(val), nil
}

// SaveCheckpoint сохраняет последний обработанный seqno в файл.
func (h *HistoricalLoader) SaveCheckpoint(seqno uint32) error {
	return os.WriteFile(h.checkpointFile, []byte(fmt.Sprintf("%d", seqno)), 0644)
}

// LoadHistoricalBlocks загружает исторические блоки параллельно через worker pool.
// detailed=true → реальные TX и адреса (медленнее); false → только шарды (быстро).
func (h *HistoricalLoader) LoadHistoricalBlocks(ctx context.Context, startSeqno, endSeqno uint32, output chan<- *models.BlockMetrics, detailed bool) error {
	total := int(endSeqno - startSeqno + 1)
	log.Printf("Загрузка блоков %d–%d (%d блоков, ~%.1f часов данных) | воркеров: %d | режим: %s",
		startSeqno, endSeqno, total, float64(total)*3.0/3600.0, h.workerCount, modeLabel(detailed))

	startTime := time.Now()
	jobs := make(chan uint32, h.workerCount*4)

	var wg sync.WaitGroup
	var processed atomic.Int64
	var failed atomic.Int64

	// Интервал прогресса и checkpoint зависит от размера загрузки
	progressInterval := int64(5000)
	if total < 50000 {
		progressInterval = 500
	}

	var lastCheckpoint atomic.Uint32

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

				// Retry с экспоненциальной задержкой
				for attempt := 0; attempt <= h.maxRetries; attempt++ {
					if detailed {
						metrics, err = h.processor.ProcessBlockDetailed(ctx, seqno)
					} else {
						metrics, err = h.processor.ProcessBlockFast(ctx, seqno)
					}

					if err == nil {
						break
					}

					if attempt < h.maxRetries {
						delay := time.Duration(1<<uint(attempt)) * 100 * time.Millisecond
						time.Sleep(delay)
					}
				}

				if err != nil {
					failed.Add(1)
					if failed.Load()%100 == 1 {
						log.Printf("Воркер %d: ошибка блока %d (после %d попыток): %v",
							workerID, seqno, h.maxRetries+1, err)
					}
					continue
				}

				select {
				case output <- metrics:
				case <-ctx.Done():
					return
				}

				n := processed.Add(1)

				// Обновляем checkpoint (атомарно сохраняем максимальный обработанный seqno)
				for {
					old := lastCheckpoint.Load()
					if seqno <= old {
						break
					}
					if lastCheckpoint.CompareAndSwap(old, seqno) {
						break
					}
				}

				if n%progressInterval == 0 {
					elapsed := time.Since(startTime)
					blocksPerSec := float64(n) / elapsed.Seconds()
					remaining := time.Duration(float64(int64(total)-n)/blocksPerSec) * time.Second
					log.Printf("Прогресс: %d/%d блоков (%.1f%%) | %.0f блоков/сек | осталось ~%v | ошибок: %d",
						n, total, float64(n)/float64(total)*100,
						blocksPerSec, remaining.Round(time.Second), failed.Load())

					// Checkpoint
					if cp := lastCheckpoint.Load(); cp > 0 {
						_ = h.SaveCheckpoint(cp)
					}
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
