package main

import (
	_ "sync" // Used in types.go
)

// NewCircularBuffer creates a new circular buffer with fixed capacity
func NewCircularBuffer(capacity int) *CircularBuffer {
	return &CircularBuffer{
		items:    make([]interface{}, capacity),
		capacity: capacity,
		head:     0,
		tail:     0,
		count:    0,
	}
}

// Push adds an item to the buffer (overwrites oldest if full)
func (cb *CircularBuffer) Push(item interface{}) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	
	cb.items[cb.tail] = item
	cb.tail = (cb.tail + 1) % cb.capacity
	
	if cb.count < cb.capacity {
		cb.count++
	} else {
		// Buffer is full, move head forward
		cb.head = (cb.head + 1) % cb.capacity
	}
}

// GetAll returns all items in the buffer (newest first)
func (cb *CircularBuffer) GetAll() []interface{} {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	
	if cb.count == 0 {
		return []interface{}{}
	}
	
	result := make([]interface{}, cb.count)
	
	// Copy items from newest to oldest
	for i := 0; i < cb.count; i++ {
		idx := (cb.tail - 1 - i + cb.capacity) % cb.capacity
		result[i] = cb.items[idx]
	}
	
	return result
}

// GetFiltered returns items that match the filter function
func (cb *CircularBuffer) GetFiltered(filter func(interface{}) bool) []interface{} {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	
	if cb.count == 0 {
		return []interface{}{}
	}
	
	result := make([]interface{}, 0, cb.count)
	
	for i := 0; i < cb.count; i++ {
		idx := (cb.tail - 1 - i + cb.capacity) % cb.capacity
		item := cb.items[idx]
		if filter(item) {
			result = append(result, item)
		}
	}
	
	return result
}

// Clear removes all items from the buffer
func (cb *CircularBuffer) Clear() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	
	cb.head = 0
	cb.tail = 0
	cb.count = 0
}

// Size returns the current number of items in the buffer
func (cb *CircularBuffer) Size() int {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.count
}

