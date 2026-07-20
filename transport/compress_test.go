package transport

import (
	"bytes"
	"compress/gzip"
	"testing"
)

// 正常往返必须无损。
func TestZipUnzipRoundTrip(t *testing.T) {
	in := []byte("volts transport payload — 中文与 ascii 混合")

	z, err := Zip(in)
	if err != nil {
		t.Fatalf("Zip: %v", err)
	}
	out, err := Unzip(z)
	if err != nil {
		t.Fatalf("Unzip: %v", err)
	}
	if !bytes.Equal(in, out) {
		t.Fatalf("往返后内容变了\n want %q\n got  %q", in, out)
	}
}

// 解压炸弹必须被拒绝，而不是把进程撑爆。
//
// 入站帧受 MaxMessageLength(64 MiB) 约束，但压缩比不受约束 —— 几 MiB 的 gzip
// 可膨胀到数十 GB。这里用一个远小于真实攻击的比例来验证限制生效：把上限临时
// 调到 1 KiB，喂一个解压出 1 MiB 的包。
func TestUnzipRejectsBomb(t *testing.T) {
	orig := MaxDecompressedLength
	MaxDecompressedLength = 1 << 10
	t.Cleanup(func() { MaxDecompressedLength = orig })

	// 1 MiB 全零 —— gzip 后只有几 KB，是最典型的放大形态。
	bomb, err := Zip(make([]byte, 1<<20))
	if err != nil {
		t.Fatalf("构造样本失败: %v", err)
	}
	if len(bomb) >= 1<<20 {
		t.Fatalf("样本没被压缩，测不出放大: %d 字节", len(bomb))
	}

	out, err := Unzip(bomb)
	if err != ErrDecompressedTooLarge {
		t.Fatalf("err = %v (输出 %d 字节), want ErrDecompressedTooLarge", err, len(out))
	}
}

// 恰好等于上限的数据必须放行 —— 边界不能误伤。
func TestUnzipAllowsExactlyAtLimit(t *testing.T) {
	const size = 4096

	orig := MaxDecompressedLength
	MaxDecompressedLength = size
	t.Cleanup(func() { MaxDecompressedLength = orig })

	in := bytes.Repeat([]byte("x"), size)
	z, err := Zip(in)
	if err != nil {
		t.Fatalf("Zip: %v", err)
	}

	out, err := Unzip(z)
	if err != nil {
		t.Fatalf("恰好等于上限被误拒: %v", err)
	}
	if len(out) != size {
		t.Fatalf("len = %d, want %d", len(out), size)
	}
}

// MaxDecompressedLength = 0 表示不限制，保留给可信内网链路。
func TestUnzipUnlimitedWhenZero(t *testing.T) {
	orig := MaxDecompressedLength
	MaxDecompressedLength = 0
	t.Cleanup(func() { MaxDecompressedLength = orig })

	in := bytes.Repeat([]byte("y"), 1<<20)
	z, _ := Zip(in)

	out, err := Unzip(z)
	if err != nil {
		t.Fatalf("不限制时不该报错: %v", err)
	}
	if len(out) != len(in) {
		t.Fatalf("len = %d, want %d", len(out), len(in))
	}
}

// 损坏的 gzip 头应当报错而不是 panic。
func TestUnzipRejectsGarbage(t *testing.T) {
	if _, err := Unzip([]byte("not a gzip stream at all")); err == nil {
		t.Fatal("垃圾输入未报错")
	}
}

// 头部合法但数据截断时应报错 —— 攻击者可以只发半个流来试探行为。
func TestUnzipRejectsTruncatedStream(t *testing.T) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write(bytes.Repeat([]byte("z"), 4096))
	w.Close()

	truncated := buf.Bytes()[:buf.Len()/2]
	if _, err := Unzip(truncated); err == nil {
		t.Fatal("截断流未报错")
	}
}
