package collector

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"ton-scrapper/models"
)

// mockBlockProcessor — мок BlockProcessor для тестов HistoricalLoader.
type mockBlockProcessor struct {
	fastErr   error
	detailErr error
	callCount atomic.Int64
	fastDelay time.Duration // имитация задержки на блок
}

func (m *mockBlockProcessor) ProcessBlockFast(ctx context.Context, seqno uint32) (*models.BlockMetrics, error) {
	m.callCount.Add(1)
	if m.fastDelay > 0 {
		time.Sleep(m.fastDelay)
	}
	if m.fastErr != nil {
		return nil, m.fastErr
	}
	return &models.BlockMetrics{
		SeqNo:            seqno,
		TransactionCount: int(seqno % 10),
		Timestamp:        time.Now(),
	}, nil
}

func (m *mockBlockProcessor) ProcessBlockDetailed(ctx context.Context, seqno uint32) (*models.BlockMetrics, error) {
	m.callCount.Add(1)
	if m.detailErr != nil {
		return nil, m.detailErr
	}
	return &models.BlockMetrics{
		SeqNo:            seqno,
		TransactionCount: int(seqno % 50),
		UniqueAddresses:  int(seqno % 20),
		Timestamp:        time.Now(),
	}, nil
}

// compile-time check
var _ BlockProcessor = (*mockBlockProcessor)(nil)

// --- основные тесты ---

func TestHistoricalLoader_LoadsAllBlocks(t *testing.T) {
	mock := &mockBlockProcessor{}
	loader := NewHistoricalLoaderWithProcessor(mock, 3)

	out := make(chan *models.BlockMetrics, 100)
	ctx := context.Background()

	start, end := uint32(1), uint32(20)
	err := loader.LoadHistoricalBlocks(ctx, start, end, out, false)
	if err != nil {
		t.Fatalf("LoadHistoricalBlocks: %v", err)
	}
	close(out)

	received := 0
	for range out {
		received++
	}

	expected := int(end-start) + 1
	if received != expected {
		t.Errorf("получено %d блоков, ожидали %d", received, expected)
	}
}

func TestHistoricalLoader_CorrectSeqNos(t *testing.T) {
	mock := &mockBlockProcessor{}
	loader := NewHistoricalLoaderWithProcessor(mock, 2)

	out := make(chan *models.BlockMetrics, 100)
	ctx := context.Background()

	start, end := uint32(10), uint32(19)
	err := loader.LoadHistoricalBlocks(ctx, start, end, out, false)
	if err != nil {
		t.Fatal(err)
	}
	close(out)

	seqNos := make(map[uint32]bool)
	for m := range out {
		seqNos[m.SeqNo] = true
	}

	for seqno := start; seqno <= end; seqno++ {
		if !seqNos[seqno] {
			t.Errorf("блок %d не был получен", seqno)
		}
	}
}

func TestHistoricalLoader_UsesDetailedMode(t *testing.T) {
	mock := &mockBlockProcessor{}
	loader := NewHistoricalLoaderWithProcessor(mock, 2)

	out := make(chan *models.BlockMetrics, 50)
	ctx := context.Background()

	_ = loader.LoadHistoricalBlocks(ctx, 1, 5, out, true) // detailed=true
	close(out)

	for m := range out {
		// ProcessBlockDetailed устанавливает UniqueAddresses = seqno % 20
		expected := int(m.SeqNo % 20)
		if m.UniqueAddresses != expected {
			t.Errorf("блок %d: UniqueAddresses=%d (ожидали %d) — значит вызывался не Detailed",
				m.SeqNo, m.UniqueAddresses, expected)
		}
	}
}

func TestHistoricalLoader_HandlesErrors(t *testing.T) {
	// Процессор всегда возвращает ошибку
	mock := &mockBlockProcessor{
		fastErr: errors.New("имитация ошибки блока"),
	}
	loader := NewHistoricalLoaderWithProcessor(mock, 2)

	out := make(chan *models.BlockMetrics, 20)
	ctx := context.Background()

	err := loader.LoadHistoricalBlocks(ctx, 1, 10, out, false)
	close(out)

	// LoadHistoricalBlocks должен завершиться без ошибки (ошибки блоков логируются, не прерывают работу)
	if err != nil {
		t.Fatalf("LoadHistoricalBlocks должен возвращать nil даже при ошибках блоков: %v", err)
	}

	// Ни один блок не должен быть в канале
	count := 0
	for range out {
		count++
	}
	if count != 0 {
		t.Errorf("при ошибках в канале должно быть 0 блоков, получили %d", count)
	}
}

func TestHistoricalLoader_ContextCancellation(t *testing.T) {
	// Процессор медленный — context успеет отмениться
	mock := &mockBlockProcessor{fastDelay: 50 * time.Millisecond}
	loader := NewHistoricalLoaderWithProcessor(mock, 2)

	out := make(chan *models.BlockMetrics, 1000)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- loader.LoadHistoricalBlocks(ctx, 1, 1000, out, false)
	}()

	// Отменяем через 100ms
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("неожиданная ошибка: %v", err)
		}
		// Проверяем что загрузилась только часть блоков
		close(out)
		count := 0
		for range out {
			count++
		}
		if count >= 1000 {
			t.Error("после отмены контекста не должны быть загружены все 1000 блоков")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("LoadHistoricalBlocks завис после отмены контекста")
	}
}

func TestHistoricalLoader_ParallelWorkers(t *testing.T) {
	// Проверяем что несколько воркеров действительно работают параллельно
	const blockCount = 20
	const workerCount = 4

	mock := &mockBlockProcessor{fastDelay: 10 * time.Millisecond}
	loader := NewHistoricalLoaderWithProcessor(mock, workerCount)

	out := make(chan *models.BlockMetrics, blockCount)
	ctx := context.Background()

	start := time.Now()
	loader.LoadHistoricalBlocks(ctx, 1, blockCount, out, false)
	elapsed := time.Since(start)
	close(out)

	// Ожидаем примерно blockCount/workerCount * delay ≈ 50ms
	// Если бы было последовательно — 200ms
	expectedMax := time.Duration(float64(blockCount)/float64(workerCount)*2) * 10 * time.Millisecond
	if elapsed > expectedMax {
		t.Errorf("параллельная загрузка заняла %v, ожидали меньше %v (возможно воркеры не параллельны)", elapsed, expectedMax)
	}
}

func TestHistoricalLoader_SingleBlock(t *testing.T) {
	mock := &mockBlockProcessor{}
	loader := NewHistoricalLoaderWithProcessor(mock, 1)

	out := make(chan *models.BlockMetrics, 1)
	loader.LoadHistoricalBlocks(context.Background(), 42, 42, out, false)
	close(out)

	var got *models.BlockMetrics
	for m := range out {
		got = m
	}
	if got == nil {
		t.Fatal("ожидали 1 блок в канале")
	}
	if got.SeqNo != 42 {
		t.Errorf("SeqNo: got %d, want 42", got.SeqNo)
	}
}

func TestHistoricalLoader_CallCount(t *testing.T) {
	mock := &mockBlockProcessor{}
	loader := NewHistoricalLoaderWithProcessor(mock, 3)

	out := make(chan *models.BlockMetrics, 100)
	loader.LoadHistoricalBlocks(context.Background(), 1, 30, out, false)
	close(out)
	for range out {
	}

	if mock.callCount.Load() != 30 {
		t.Errorf("ожидали 30 вызовов ProcessBlockFast, получили %d", mock.callCount.Load())
	}
}

// --- MixedErrors test ---

func TestHistoricalLoader_PartialErrors(t *testing.T) {
	// Каждый 3й блок возвращает ошибку
	callNum := atomic.Int64{}
	mock := &mockBlockProcessor{}

	// Переопределяем через обёртку
	partialMock := &partialErrorProcessor{
		inner:     mock,
		failEvery: 3,
		callNum:   &callNum,
	}

	loader := NewHistoricalLoaderWithProcessor(partialMock, 2)
	out := make(chan *models.BlockMetrics, 20)
	loader.LoadHistoricalBlocks(context.Background(), 1, 9, out, false)
	close(out)

	count := 0
	for range out {
		count++
	}

	// 9 блоков, каждый 3й падает → 6 успешных (блоки 1,2, 4,5, 7,8)
	if count != 6 {
		t.Errorf("ожидали 6 успешных блоков, получили %d", count)
	}
}

// partialErrorProcessor — каждый N-й вызов возвращает ошибку
type partialErrorProcessor struct {
	inner     BlockProcessor
	failEvery int64
	callNum   *atomic.Int64
}

func (p *partialErrorProcessor) ProcessBlockFast(ctx context.Context, seqno uint32) (*models.BlockMetrics, error) {
	n := p.callNum.Add(1)
	if n%p.failEvery == 0 {
		return nil, fmt.Errorf("намеренная ошибка на вызове %d", n)
	}
	return p.inner.ProcessBlockFast(ctx, seqno)
}

func (p *partialErrorProcessor) ProcessBlockDetailed(ctx context.Context, seqno uint32) (*models.BlockMetrics, error) {
	return p.inner.ProcessBlockDetailed(ctx, seqno)
}
