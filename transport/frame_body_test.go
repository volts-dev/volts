package transport

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/volts-dev/volts/core/memory"
)

type fragmentedFrameReader struct{ r io.Reader }

func (r fragmentedFrameReader) Read(p []byte) (int, error) { return r.r.Read(p[:min(len(p), 7919)]) }

func TestFrameBodyFragmentedOwnership(t *testing.T) {
	src := newMessage()
	src.Path = "original.path"
	src.Header["Trace"] = "original-trace"
	src.Payload = bytes.Repeat([]byte("original payload"), 20000)
	firstWire := src.Encode()
	dst := newMessage()
	if err := dst.Decode(fragmentedFrameReader{bytes.NewReader(firstWire)}); err != nil {
		t.Fatal(err)
	}
	path, trace, payload, body := dst.Path, dst.Header["Trace"], dst.Payload, dst.Body
	dst.Reset()
	src.Path = "changed.path"
	src.Header["Trace"] = "changed-trace"
	src.Payload = bytes.Repeat([]byte("different payload"), 20000)
	if err := dst.Decode(fragmentedFrameReader{bytes.NewReader(src.Encode())}); err != nil {
		t.Fatal(err)
	}
	if path != "original.path" || trace != "original-trace" || !bytes.Equal(payload, bytes.Repeat([]byte("original payload"), 20000)) || !bytes.Equal(body, firstWire[16:]) {
		t.Fatal("scratch reuse changed an earlier decoded body or aliased metadata")
	}
	if !bytes.Equal(dst.Payload, src.Payload) {
		t.Fatal("fragmented decode changed payload")
	}
}

func TestFrameBodyTruncationAtChunkBoundaries(t *testing.T) {
	src := newMessage()
	src.Payload = bytes.Repeat([]byte("x"), 3*initialBodyBuffer)
	wire := src.Encode()
	for _, arrived := range []int{0, 1, initialBodyBuffer - 1, initialBodyBuffer, initialBodyBuffer + 1, 2 * initialBodyBuffer} {
		if err := newMessage().Decode(bytes.NewReader(wire[:16+arrived])); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("arrived=%d err=%v", arrived, err)
		}
	}
}

func TestFrameChunkBudgetAndReclaimFence(t *testing.T) {
	var p frameChunkCache
	epoch := p.generation()
	chunks := make([]*frameChunk, maxFrameChunks+5)
	for i := range chunks {
		chunks[i] = p.take()
	}
	p.give(chunks, epoch)
	if u := p.Usage(); u.Entries != maxFrameChunks || u.Bytes != 4<<20 {
		t.Fatal(u)
	}
	p.Reclaim(memory.None)
	if p.Usage().Entries != maxFrameChunks {
		t.Fatal("None reclaimed chunks")
	}
	if freed := p.Reclaim(memory.Trim); freed.Entries != maxFrameChunks {
		t.Fatal(freed)
	}
	p.give(chunks, epoch)
	if p.Usage().Entries != 0 {
		t.Fatal("pre-reclaim decode refilled scratch pool")
	}
}

func TestFrameBodyConcurrentScratchReuse(t *testing.T) {
	var wg sync.WaitGroup
	for worker := 0; worker < 12; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			want := bytes.Repeat([]byte{byte(worker)}, 3*initialBodyBuffer+worker)
			for i := 0; i < 12; i++ {
				got, err := readFrameBody(fragmentedFrameReader{bytes.NewReader(want)}, len(want))
				if err != nil || !bytes.Equal(got, want) {
					t.Errorf("worker %d: payload corruption, err=%v", worker, err)
					return
				}
			}
		}(worker)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent decode did not finish")
	}
}
