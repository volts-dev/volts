package transport

import (
	"io"
	"sync"

	"github.com/volts-dev/volts/core/memory"
)

// Scratch chunks never escape a decode. The final Body is a fresh allocation:
// metadata strings and callers may retain aliases after the Message is reset.
// Retention is bounded independently of concurrency and declared frame length.
const maxFrameChunks = 64 // 4 MiB across the process

type frameChunk [initialBodyBuffer]byte
type frameChunkCache struct {
	mu    sync.Mutex
	free  []*frameChunk
	epoch uint64
}

var frameChunks frameChunkCache

func init() { memory.Register(&frameChunks) }

func (*frameChunkCache) String() string { return "transport.frame_chunks" }
func (p *frameChunkCache) Usage() memory.Usage {
	p.mu.Lock()
	defer p.mu.Unlock()
	return memory.Usage{Entries: len(p.free), Bytes: int64(len(p.free) * initialBodyBuffer)}
}
func (p *frameChunkCache) Reclaim(level memory.Level) memory.Usage {
	p.mu.Lock()
	defer p.mu.Unlock()
	if level == memory.None {
		return memory.Usage{}
	}
	u := memory.Usage{Entries: len(p.free), Bytes: int64(len(p.free) * initialBodyBuffer)}
	p.free = nil
	p.epoch++
	return u
}
func (p *frameChunkCache) generation() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.epoch
}
func (p *frameChunkCache) take() *frameChunk {
	p.mu.Lock()
	if n := len(p.free); n > 0 {
		b := p.free[n-1]
		p.free[n-1] = nil
		p.free = p.free[:n-1]
		p.mu.Unlock()
		return b
	}
	p.mu.Unlock()
	return new(frameChunk)
}
func (p *frameChunkCache) give(chunks []*frameChunk, epoch uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.epoch != epoch {
		return
	}
	for _, b := range chunks {
		if len(p.free) == maxFrameChunks {
			break
		}
		p.free = append(p.free, b)
	}
}

func readFrameBody(r io.Reader, length int) ([]byte, error) {
	if length <= initialBodyBuffer {
		data := make([]byte, length)
		_, err := io.ReadFull(r, data)
		return data, err
	}
	// Do not allocate the declared length, even for a previously large peer.
	// Each next chunk is obtained only after the preceding chunk arrived.
	epoch := frameChunks.generation()
	var chunks []*frameChunk
	defer func() { frameChunks.give(chunks, epoch) }()
	for remaining := length; remaining > 0; {
		b := frameChunks.take()
		chunks = append(chunks, b)
		n := min(remaining, len(b))
		if _, err := io.ReadFull(r, b[:n]); err != nil {
			return nil, err
		}
		remaining -= n
	}
	// The peer has delivered every byte. One exact allocation avoids repeated
	// geometric growth while preserving the untrusted-prefix amplification guard.
	data := make([]byte, length)
	offset := 0
	for _, b := range chunks {
		offset += copy(data[offset:], b[:])
	}
	return data, nil
}
