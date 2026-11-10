package transport

import (
	"bytes"
	"fmt"
	"net"
	"testing"
	"time"
)

func receivePayload(t *testing.T, dst *Message, src *Message) {
	t.Helper()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	_ = b.SetWriteDeadline(time.Now().Add(3 * time.Second))
	done := make(chan error, 1)
	go func() { _, err := b.Write(src.Encode()); done <- err }()
	if err := NewTcpTransportSocket(a, 3*time.Second, 0).Recv(dst); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("sender did not complete")
	}
}

func TestTakePayloadNativeRetainsResponseAcrossReuse(t *testing.T) {
	src, dst := newMessage(), newMessage()
	src.Path, src.Header["trace"] = "original-path", "original-header"
	src.Payload = bytes.Repeat([]byte("original"), 40000)
	receivePayload(t, dst, src)
	original := dst.Payload
	path, header := dst.Path, dst.Header["trace"]
	got := dst.TakePayload()
	if &got[0] != &original[0] {
		t.Fatal("native payload was copied")
	}
	if cap(got) != len(got) || dst.Body != nil || dst.Payload != nil {
		t.Fatal("payload not detached")
	}
	if dst.TakePayload() != nil {
		t.Fatal("second transfer returned old bytes")
	}
	for i := 0; i < 10; i++ {
		dst.Reset()
		src.Path, src.Header["trace"] = "changed-path", "changed-header"
		src.Payload = bytes.Repeat([]byte("changed!"), 40000)
		receivePayload(t, dst, src)
	}
	if !bytes.Equal(got, original) || string(got[:8]) != "original" || path != "original-path" || header != "original-header" {
		t.Fatal("returned response changed on message reuse")
	}
	buf := bytes.NewBuffer(got)
	buf.Next(len(got) - 4)
	buf.WriteString("more response bytes")
	if path != "original-path" || header != "original-header" {
		t.Fatal("body mutation corrupted metadata")
	}
}

func TestTakePayloadCopiesUncertifiedOrReplacedStorage(t *testing.T) {
	for _, kind := range []string{"custom", "decode-only", "replaced", "reset", "failed-decode"} {
		t.Run(kind, func(t *testing.T) {
			src, dst := newMessage(), newMessage()
			src.Payload = []byte("borrowed")
			switch kind {
			case "decode-only":
				if err := dst.Decode(bytes.NewReader(src.Encode())); err != nil {
					t.Fatal(err)
				}
			case "replaced", "reset", "failed-decode":
				receivePayload(t, dst, src)
				old := dst.Payload
				switch kind {
				case "replaced":
					dst.Payload = bytes.Clone(old)
				case "reset":
					dst.Reset()
					dst.Payload = old
				case "failed-decode":
					if dst.Decode(bytes.NewReader(nil)) == nil {
						t.Fatal("expected EOF")
					}
				}
			default:
				dst.Payload = src.Payload
			}
			borrowed := dst.Payload
			got := dst.TakePayload()
			borrowed[0] = 'X'
			if string(got) != "borrowed" {
				t.Fatal("response borrows transport storage")
			}
		})
	}
}

func BenchmarkReceivedPayloadBuffer(b *testing.B) {
	for _, size := range []int{1024, 256 << 10, 1 << 20} {
		for _, take := range []bool{false, true} {
			b.Run(fmt.Sprintf("%d/transfer=%t", size, take), func(b *testing.B) {
				payload := make([]byte, size)
				b.ReportAllocs()
				b.SetBytes(int64(size))
				for i := 0; i < b.N; i++ {
					m := Message{Payload: payload, receivedPayload: payload}
					var buf bytes.Buffer
					if take {
						buf = *bytes.NewBuffer(m.TakePayload())
					} else {
						buf.Write(m.Payload)
					}
					if buf.Len() != size {
						b.Fatal("missing response")
					}
				}
			})
		}
	}
}
