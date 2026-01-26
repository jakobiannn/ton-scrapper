package main

import (
	sync "sync"
)

type Cache interface {
	put(key string, value string)
	get(key string) string
}

type LRUCache struct {
	elements     map[string]*Node
	firstElement *Node
	lastElement  *Node
	lock         sync.RWMutex
	capacity     int
}

type Node struct {
	key   string
	value string
	prev  *Node
	next  *Node
}

func (cache *LRUCache) put(key string, value string) {
	cache.lock.Lock()
	if len(cache.elements) >= cache.capacity && cache.elements[key] == nil {
		cache.lastElement = cache.lastElement.prev
		cache.lastElement.next = nil
		delete(cache.elements, key)
	}
	var newNode = &Node{
		key:   key,
		value: value,
		prev:  nil,
		next:  cache.firstElement,
	}

	cache.elements[key] = newNode
	cache.firstElement = newNode
	cache.lock.Unlock()
}

func (cache *LRUCache) get(key string) string {
	if cache.elements[key] != nil {
		cache.lock.Lock()
		cache.moveToFront(cache.elements[key])
		cache.lock.Unlock()
	}
	return cache.elements[key].value
}

func (cache *LRUCache) moveToFront(node *Node) {
	cache.remove(node)
	cache.addToFront(node)
}

func (cache *LRUCache) remove(node *Node) {
	node.prev.next = node.next
	node.next.prev = node.prev
}

func (cache *LRUCache) addToFront(node *Node) {
	cache.lock.Lock()
	cache.firstElement.prev = node
	node.next = cache.firstElement
	cache.firstElement = node
}
