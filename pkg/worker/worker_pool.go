package worker

import (
	"context"
	"sync"
)

// WorkerPool manages a pool of workers that process tasks concurrently
type WorkerPool struct {
	workerCount int
	taskChan    chan func() error
	wg          sync.WaitGroup
	ctx         context.Context
	cancel      context.CancelFunc
	closed      bool
	mu          sync.Mutex
}

// NewWorkerPool creates a new worker pool with the specified number of workers
func NewWorkerPool(workerCount int) *WorkerPool {
	ctx, cancel := context.WithCancel(context.Background())
	wp := &WorkerPool{
		workerCount: workerCount,
		taskChan:    make(chan func() error, workerCount*2), // Buffer channel
		ctx:         ctx,
		cancel:      cancel,
	}
	wp.start()
	return wp
}

// start initializes the worker goroutines
func (wp *WorkerPool) start() {
	for i := 0; i < wp.workerCount; i++ {
		wp.wg.Add(1)
		go wp.worker()
	}
}

// worker is the main worker loop that processes tasks
func (wp *WorkerPool) worker() {
	defer wp.wg.Done()
	for {
		select {
		case <-wp.ctx.Done():
			return
		case task, ok := <-wp.taskChan:
			if !ok {
				return
			}
			_ = task() // Execute task, errors are handled by caller
		}
	}
}

// Submit adds a task to the worker pool
func (wp *WorkerPool) Submit(task func() error) error {
	select {
	case <-wp.ctx.Done():
		return wp.ctx.Err()
	case wp.taskChan <- task:
		return nil
	}
}

// Wait waits for all workers to finish processing current tasks
func (wp *WorkerPool) Wait() {
	wp.mu.Lock()
	if !wp.closed {
		close(wp.taskChan)
		wp.closed = true
	}
	wp.mu.Unlock()
	wp.wg.Wait()
}

// Shutdown gracefully shuts down the worker pool
func (wp *WorkerPool) Shutdown() {
	wp.cancel()
	wp.mu.Lock()
	if !wp.closed {
		close(wp.taskChan)
		wp.closed = true
	}
	wp.mu.Unlock()
	wp.wg.Wait()
}

// ProcessBatch processes a batch of tasks with error collection
func (wp *WorkerPool) ProcessBatch(tasks []func() error) []error {
	var wg sync.WaitGroup
	errors := make([]error, len(tasks))
	mu := sync.Mutex{}

	for i, task := range tasks {
		wg.Add(1)
		i, task := i, task // Capture loop variables
		go func() {
			defer wg.Done()
			if err := wp.Submit(task); err != nil {
				mu.Lock()
				errors[i] = err
				mu.Unlock()
			}
		}()
	}

	wg.Wait()
	return errors
}

// SubmitAndWait submits a task and returns a WaitGroup that will be done when the task completes
// This allows tracking task completion without closing the channel
func (wp *WorkerPool) SubmitAndWait(task func() error) (*sync.WaitGroup, error) {
	var wg sync.WaitGroup
	wg.Add(1)

	wrappedTask := func() error {
		defer wg.Done()
		return task()
	}

	if err := wp.Submit(wrappedTask); err != nil {
		wg.Done() // Release the wait group if submission fails
		return nil, err
	}

	return &wg, nil
}
