package Utils

import (
	"net"
	"sync"
)

type WorkerPool struct {
	maxWorkers  int
	wg          sync.WaitGroup
	TaskChan    chan *net.Conn
	taskHandler func(*net.Conn)
	done        chan struct{}
}

func NewWorkerPool(maxWorkers int, taskHandler func(*net.Conn)) *WorkerPool {
	wp := &WorkerPool{maxWorkers: maxWorkers,
		TaskChan:    make(chan *net.Conn, 1024),
		taskHandler: taskHandler,
		done:        make(chan struct{})}
	for i := 0; i < wp.maxWorkers; i++ {
		wp.wg.Add(1)
		go func() {
			defer wp.wg.Done()
			for {
				select {
				case task := <-wp.TaskChan:
					wp.taskHandler(task)
				case <-wp.done:
					return
				}
			}
		}()
	}
	return wp
}

func (wp *WorkerPool) Submit(task *net.Conn) {
	wp.TaskChan <- task
}

func (wp *WorkerPool) Shutdown() {
	close(wp.done)
	wp.wg.Wait()
}
