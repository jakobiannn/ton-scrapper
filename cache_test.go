package main

import (
	"fmt"
	"sync"
	"testing"
)

func newTestCache(capacity int) *LRUCache {
	return NewLRUCache(capacity)
}

// --- put / get ---

func TestCache_PutAndGet(t *testing.T) {
	c := newTestCache(3)
	c.put("a", "1")
	c.put("b", "2")

	val, ok := c.get("a")
	if !ok || val != "1" {
		t.Errorf("get(a): got (%q, %v), want (\"1\", true)", val, ok)
	}
	val, ok = c.get("b")
	if !ok || val != "2" {
		t.Errorf("get(b): got (%q, %v), want (\"2\", true)", val, ok)
	}
}

func TestCache_GetMissingKey(t *testing.T) {
	c := newTestCache(3)
	val, ok := c.get("missing")
	if ok || val != "" {
		t.Errorf("get(missing): expected (\"\", false), got (%q, %v)", val, ok)
	}
}

func TestCache_UpdateExistingKey(t *testing.T) {
	c := newTestCache(3)
	c.put("k", "v1")
	c.put("k", "v2")

	val, ok := c.get("k")
	if !ok || val != "v2" {
		t.Errorf("after update get(k): got (%q, %v), want (\"v2\", true)", val, ok)
	}
	if len(c.elements) != 1 {
		t.Errorf("expected 1 element after update, got %d", len(c.elements))
	}
}

// --- LRU eviction ---

func TestCache_EvictsLRU(t *testing.T) {
	c := newTestCache(3)
	c.put("a", "1")
	c.put("b", "2")
	c.put("c", "3")

	// a, b, c — все в кэше
	// Доступ к "a" делает его MRU: порядок станет b, c, a
	c.get("a")

	// Добавляем "d" — вытесняется LRU (b)
	c.put("d", "4")

	if _, ok := c.get("b"); ok {
		t.Error("'b' должна была быть вытеснена как LRU")
	}
	if _, ok := c.get("a"); !ok {
		t.Error("'a' не должна быть вытеснена (была использована)")
	}
	if _, ok := c.get("c"); !ok {
		t.Error("'c' не должна быть вытеснена")
	}
	if _, ok := c.get("d"); !ok {
		t.Error("'d' должна быть в кэше")
	}
}

func TestCache_EvictsOldestWhenFull(t *testing.T) {
	c := newTestCache(2)
	c.put("x", "1")
	c.put("y", "2")
	c.put("z", "3") // x должен быть вытеснен

	if _, ok := c.get("x"); ok {
		t.Error("'x' должна была быть вытеснена")
	}
	if _, ok := c.get("y"); !ok {
		t.Error("'y' должна быть в кэше")
	}
	if _, ok := c.get("z"); !ok {
		t.Error("'z' должна быть в кэше")
	}
}

func TestCache_SizeStaysWithinCapacity(t *testing.T) {
	cap := 5
	c := newTestCache(cap)
	for i := 0; i < 20; i++ {
		c.put(fmt.Sprintf("key%d", i), fmt.Sprintf("val%d", i))
	}
	if len(c.elements) > cap {
		t.Errorf("размер кэша %d превысил ёмкость %d", len(c.elements), cap)
	}
}

// --- capacity 1 ---

func TestCache_CapacityOne(t *testing.T) {
	c := newTestCache(1)
	c.put("a", "1")
	c.put("b", "2") // вытесняет a

	if _, ok := c.get("a"); ok {
		t.Error("'a' должна быть вытеснена при capacity=1")
	}
	if v, ok := c.get("b"); !ok || v != "2" {
		t.Errorf("ожидали b=2, получили (%q, %v)", v, ok)
	}
}

// --- thread safety ---

func TestCache_ConcurrentAccess(t *testing.T) {
	c := newTestCache(100)
	var wg sync.WaitGroup

	// 50 горутин пишут
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			c.put(fmt.Sprintf("key%d", n), fmt.Sprintf("val%d", n))
		}(i)
	}

	// 50 горутин читают
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			c.get(fmt.Sprintf("key%d", n))
		}(i)
	}

	wg.Wait()
	// Если race detector не сработал — тест пройден
}

func TestCache_ConcurrentPutSameKey(t *testing.T) {
	c := newTestCache(10)
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			c.put("shared", fmt.Sprintf("%d", n))
		}(i)
	}

	wg.Wait()

	// После всех записей ключ должен существовать
	if _, ok := c.get("shared"); !ok {
		t.Error("'shared' должен быть в кэше после concurrent puts")
	}
	// Должен быть ровно один элемент
	if len(c.elements) != 1 {
		t.Errorf("ожидали 1 элемент, получили %d", len(c.elements))
	}
}

// --- list integrity ---

func TestCache_ListIntegrity(t *testing.T) {
	c := newTestCache(4)
	keys := []string{"a", "b", "c", "d"}
	for i, k := range keys {
		c.put(k, fmt.Sprintf("%d", i))
	}

	// Проверяем что список замкнут и не зациклен
	visited := make(map[string]bool)
	node := c.firstElement
	for node != nil {
		if visited[node.key] {
			t.Fatal("обнаружен цикл в двусвязном списке кэша")
		}
		visited[node.key] = true
		node = node.next
	}

	if len(visited) != len(c.elements) {
		t.Errorf("список содержит %d узлов, map содержит %d", len(visited), len(c.elements))
	}
}
