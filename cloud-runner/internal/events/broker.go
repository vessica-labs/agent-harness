package events

import "sync"

type Broker struct {
	mu      sync.Mutex
	next    int
	waiters map[int]chan struct{}
}

func NewBroker() *Broker { return &Broker{waiters: make(map[int]chan struct{})} }

func (b *Broker) Subscribe() (<-chan struct{}, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.next++
	id := b.next
	channel := make(chan struct{}, 1)
	b.waiters[id] = channel
	return channel, func() {
		b.mu.Lock()
		delete(b.waiters, id)
		b.mu.Unlock()
	}
}

func (b *Broker) Notify() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, channel := range b.waiters {
		select {
		case channel <- struct{}{}:
		default:
		}
	}
}
