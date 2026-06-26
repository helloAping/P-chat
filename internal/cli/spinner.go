package cli

import (
	"fmt"
	"sync"
	"time"
)

type Spinner struct {
	message string
	done    chan struct{}
	stopped chan struct{}
	running bool
	mu      sync.Mutex
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func NewSpinner(message string) *Spinner {
	return &Spinner{
		message: message,
		done:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
}

func (s *Spinner) Start() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()

	go func() {
		defer close(s.stopped)
		i := 0
		for {
			select {
			case <-s.done:
				fmt.Printf("\r\033[K")
				return
			default:
				fmt.Printf("\r\033[36m%s\033[0m %s", spinnerFrames[i%len(spinnerFrames)], s.message)
				i++
				time.Sleep(80 * time.Millisecond)
			}
		}
	}()
}

func (s *Spinner) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	close(s.done)
	s.mu.Unlock()

	// Wait for goroutine to finish to avoid race
	<-s.stopped
}

func (s *Spinner) UpdateMessage(msg string) {
	s.message = msg
}
