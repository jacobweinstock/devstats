package app

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// progress prints a periodic heartbeat and per-repo completion lines to stderr.
type progress struct {
	total    int
	interval time.Duration

	mu   sync.Mutex
	done int

	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
}

func newProgress(total int, interval time.Duration) *progress {
	return &progress{
		total:    total,
		interval: interval,
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
}

func (p *progress) start() {
	if p.interval <= 0 {
		close(p.doneCh)
		return
	}
	go func() {
		t := time.NewTicker(p.interval)
		defer t.Stop()
		for {
			select {
			case <-p.stopCh:
				close(p.doneCh)
				return
			case <-t.C:
				p.mu.Lock()
				d := p.done
				p.mu.Unlock()
				fmt.Fprintf(os.Stderr, "  [%d/%d] repos done\n", d, p.total)
			}
		}
	}()
}

func (p *progress) repoDone(repo string) {
	p.mu.Lock()
	p.done++
	d := p.done
	p.mu.Unlock()
	fmt.Fprintf(os.Stderr, "  [%d/%d] %s\n", d, p.total, repo)
}

func (p *progress) stop() {
	p.stopOnce.Do(func() { close(p.stopCh) })
	<-p.doneCh
}
