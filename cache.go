package main

import (
	"sync"
)

type Cache interface {
	put(key string, value string)
	get(key string) (string, bool)
}

type LRUCache struct {
	elements     map[string]*Node
	firstElement *Node
	lastElement  *Node
	lock         sync.Mutex // Mutex (не RWMutex) — put и get оба пишут (меняют порядок)
	capacity     int
}

type Node struct {
	key   string
	value string
	prev  *Node
	next  *Node
}

// put добавляет или обновляет элемент. При переполнении вытесняет LRU (последний).
func (cache *LRUCache) put(key string, value string) {
	cache.lock.Lock()
	defer cache.lock.Unlock()

	// Если ключ уже есть — обновляем значение и перемещаем в начало
	if node, exists := cache.elements[key]; exists {
		node.value = value
		cache.moveToFront(node)
		return
	}

	// Вытесняем LRU если достигнут предел
	if len(cache.elements) >= cache.capacity && cache.lastElement != nil {
		evicted := cache.lastElement
		cache.removeNode(evicted)
		delete(cache.elements, evicted.key)
	}

	newNode := &Node{
		key:   key,
		value: value,
	}
	cache.addToFront(newNode)
	cache.elements[key] = newNode
}

// get возвращает значение и флаг found. Перемещает элемент в начало (MRU).
func (cache *LRUCache) get(key string) (string, bool) {
	cache.lock.Lock()
	defer cache.lock.Unlock()

	node, exists := cache.elements[key]
	if !exists {
		return "", false
	}

	cache.moveToFront(node)
	return node.value, true
}

// moveToFront — вызывается только под локом
func (cache *LRUCache) moveToFront(node *Node) {
	if node == cache.firstElement {
		return // уже в начале
	}
	cache.removeNode(node)
	cache.addToFront(node)
}

// removeNode — вырезает узел из двусвязного списка (под локом)
func (cache *LRUCache) removeNode(node *Node) {
	if node.prev != nil {
		node.prev.next = node.next
	} else {
		cache.firstElement = node.next
	}
	if node.next != nil {
		node.next.prev = node.prev
	} else {
		cache.lastElement = node.prev
	}
	node.prev = nil
	node.next = nil
}

// addToFront — добавляет узел в голову списка (под локом)
func (cache *LRUCache) addToFront(node *Node) {
	node.prev = nil
	node.next = cache.firstElement
	if cache.firstElement != nil {
		cache.firstElement.prev = node
	}
	cache.firstElement = node
	if cache.lastElement == nil {
		cache.lastElement = node
	}
}

// NewLRUCache создаёт кэш с заданной ёмкостью
func NewLRUCache(capacity int) *LRUCache {
	return &LRUCache{
		elements: make(map[string]*Node),
		capacity: capacity,
	}
}
