package transport

import (
	"sync"
)

var (
	poolUint32Data = sync.Pool{
		New: func() interface{} {
			data := make([]byte, 4)
			return &data
		},
	}

	msgPool = sync.Pool{
		New: func() interface{} {
			bom := Bom([12]byte{})
			bom[0] = MagicNumber

			return &Message{
				Bom:    &bom,
				Header: make(map[string]string), //TODO 优化替代或者删除
			}
		},
	}
)

type (
	Pool struct {
		sync.RWMutex
		pool map[string]*Socket
	}
)

func GetMessageFromPool() *Message {
	return msgPool.Get().(*Message)
}

func PutMessageToPool(msg *Message) {
	if msg != nil {
		msg.Reset()
		msgPool.Put(msg)
	}
}

// NewPool returns a new socket pool
func NewPool() *Pool {
	return &Pool{
		pool: make(map[string]*Socket),
	}
}

func (p *Pool) Get(id string) (*Socket, bool) {
	// attempt to get existing socket
	p.RLock()
	socket, ok := p.pool[id]
	if ok {
		p.RUnlock()
		return socket, ok
	}
	p.RUnlock()

	// save socket
	p.Lock()
	defer p.Unlock()
	// double checked locking
	socket, ok = p.pool[id]
	if ok {
		return socket, ok
	}
	// create new socket
	socket = New(id)
	p.pool[id] = socket

	// return socket
	return socket, false
}

func (p *Pool) Release(s *Socket) {
	p.Lock()
	defer p.Unlock()

	// close the socket
	s.Close()
	delete(p.pool, s.id)
}

// Close the pool and delete all the sockets
func (p *Pool) Close() {
	p.Lock()
	defer p.Unlock()
	for id, sock := range p.pool {
		sock.Close()
		delete(p.pool, id)
	}
}
