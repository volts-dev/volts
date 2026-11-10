package transport

import (
	"bytes"
	"fmt"
	"testing"
)

func BenchmarkMessageDecodePayload(b *testing.B) {
	for _, size := range []int{1024, 256 << 10, 1 << 20} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			src := newMessage()
			src.Path = "registry.loadviews"
			src.Payload = bytes.Repeat([]byte("x"), size)
			wire := src.Encode()
			dst := newMessage()
			b.ReportAllocs()
			b.SetBytes(int64(len(wire)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				dst.Reset()
				if err := dst.Decode(bytes.NewReader(wire)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkMessageDecodeParallel(b *testing.B) {
	src := newMessage()
	src.Path = "registry.loadviews"
	src.Payload = bytes.Repeat([]byte("x"), 256<<10)
	wire := src.Encode()
	b.ReportAllocs()
	b.SetBytes(int64(len(wire)))
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		dst := newMessage()
		for pb.Next() {
			dst.Reset()
			if err := dst.Decode(bytes.NewReader(wire)); err != nil {
				b.Fatal(err)
			}
		}
	})
}
