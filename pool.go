package gremgo

import (
	"sync"
	"time"
)

// Pool maintains a list of connections.
type Pool struct {
	Dial        func() (*Client, error)
	MaxActive   int
	IdleTimeout time.Duration
	mutex       sync.Mutex
	idle        []*idleConnection
	Active      int
	cond        *sync.Cond
}

// PooledConnection represents a shared and reusable connection.
type PooledConnection struct {
	Pool   *Pool
	Client *Client
}

type idleConnection struct {
	pc *PooledConnection
	// t is the time the connection was idled
	t time.Time
}

// Get will return an available pooled connection. Either an idle connection or
// by dialing a new one if the pool does not currently have a maximum number
// of Active connections.
func (p *Pool) Get() (*PooledConnection, error) {
	// Lock the pool to keep the kids out.
	p.mutex.Lock()

	// Clean this place up.
	p.purge()

	// Wait loop
	for {
		// Try to grab first available idle connection
		if conn := p.first(); conn != nil {

			// Remove the connection from the idle slice
			p.idle = append(p.idle[:0], p.idle[1:]...)
			p.Active++
			p.mutex.Unlock()
			pc := &PooledConnection{Pool: p, Client: conn.pc.Client}
			return pc, nil

		}

		// No idle connections, try dialing a new one
		if p.MaxActive == 0 || p.Active < p.MaxActive {
			p.Active++
			dial := p.Dial

			// Unlock here so that any other connections that need to be
			// dialed do not have to wait.
			p.mutex.Unlock()

			dc, err := dial()
			if err != nil {
				p.mutex.Lock()
				p.release()
				p.mutex.Unlock()
				return nil, err
			}

			pc := &PooledConnection{Pool: p, Client: dc}
			return pc, nil
		}

		//No idle connections and max Active connections, let's wait.
		if p.cond == nil {
			p.cond = sync.NewCond(&p.mutex)
		}

		p.cond.Wait()
	}
}

// put pushes the supplied PooledConnection to the top of the idle slice to be reused.
// It is not threadsafe. The caller should manage locking the pool.
func (p *Pool) put(pc *PooledConnection) {
	idle := &idleConnection{pc: pc, t: time.Now()}
	// Prepend the connection to the front of the slice
	p.idle = append([]*idleConnection{idle}, p.idle...)

}

// purge removes expired idle connections from the pool.
// It is not threadsafe. The caller should manage locking the pool.
func (p *Pool) purge() {
	if timeout := p.IdleTimeout; timeout > 0 {
		var valid []*idleConnection
		now := time.Now()
		for _, v := range p.idle {
			// If the client has an error then exclude it from the pool
			if v.pc.Client.Errored {
				continue
			}

			if v.t.Add(timeout).After(now) {
				valid = append(valid, v)
			} else {
				// Force underlying connection closed
				v.pc.Client.Close()
			}
		}
		p.idle = valid
	}
}

// release decrements Active and alerts waiters.
// It is not threadsafe. The caller should manage locking the pool.
func (p *Pool) release() {
	p.Active--
	if p.cond != nil {
		p.cond.Signal()
	}

}

func (p *Pool) first() *idleConnection {
	if len(p.idle) == 0 {
		return nil
	}
	return p.idle[0]
}

// Close signals that the caller is finished with the connection and should be
// returned to the pool for future use.
func (pc *PooledConnection) Close() {
	pc.Pool.mutex.Lock()
	defer pc.Pool.mutex.Unlock()

	pc.Pool.put(pc)
	pc.Pool.release()
}

func CreatePool(dialer func() (*Client, error), timeout time.Duration) *Pool {
	pool := Pool{
		IdleTimeout: timeout,
		Dial:        dialer,
		MaxActive:   10,
		mutex:       sync.Mutex{},
	}
	return &pool
}
