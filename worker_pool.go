package main

import (
	_ "sync" // Used in types.go
)

// NewWorkerPool creates a new worker pool
func NewWorkerPool(workers int) *WorkerPool {
	wp := &WorkerPool{
		workers:  workers,
		taskChan: make(chan func(), workers*2), // Buffered channel
		stopChan: make(chan struct{}),
	}
	
	// Start workers
	for i := 0; i < workers; i++ {
		wp.wg.Add(1)
		go wp.worker()
	}
	
	return wp
}

// worker processes tasks from the task channel
func (wp *WorkerPool) worker() {
	defer wp.wg.Done()
	
	for {
		select {
		case task := <-wp.taskChan:
			// Execute task with panic recovery
			func() {
				defer func() {
					if r := recover(); r != nil {
						// Log panic but continue
					}
				}()
				task()
			}()
			
		case <-wp.stopChan:
			return
		}
	}
}

// Submit submits a task to the worker pool
func (wp *WorkerPool) Submit(task func()) bool {
	select {
	case wp.taskChan <- task:
		return true
	default:
		return false // Pool is full
	}
}

// Stop gracefully stops the worker pool
func (wp *WorkerPool) Stop() {
	wp.mu.Lock()
	if wp.stopped {
		wp.mu.Unlock()
		return // Already stopped
	}
	wp.stopped = true
	wp.mu.Unlock()
	
	close(wp.stopChan)
	wp.wg.Wait()
}

// WaitForCompletion waits for all submitted tasks to complete
func (wp *WorkerPool) WaitForCompletion() {
	wp.mu.Lock()
	if wp.stopped {
		wp.mu.Unlock()
		return // Pool is stopped, cannot restart
	}
	
	// Close task channel temporarily
	oldTaskChan := wp.taskChan
	wp.taskChan = make(chan func(), wp.workers*2)
	wp.mu.Unlock()
	
	close(oldTaskChan)
	wp.wg.Wait()
	
	// Restart workers with new task channel
	for i := 0; i < wp.workers; i++ {
		wp.wg.Add(1)
		go wp.worker()
	}
}

