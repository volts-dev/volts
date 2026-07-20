package transport

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
)

// DefaultMaxDecompressedLength 是解压输出的默认上限。
//
// 入站帧本身受 MaxMessageLength(64 MiB) 约束，但压缩比不受约束 —— 一个几 MiB
// 的 gzip 可以膨胀到数十 GB（"解压炸弹"），对端只需一个包就能把进程 OOM 掉。
// 256 MiB 给正常的大消息留足余量，同时把放大倍数钉死在可控范围。
const DefaultMaxDecompressedLength = 256 << 20

// MaxDecompressedLength 是 Unzip 允许的解压后字节上限。
// 设为 0 可关闭该限制（不建议对不可信输入这么做）。
var MaxDecompressedLength int64 = DefaultMaxDecompressedLength

// ErrDecompressedTooLarge 表示解压输出超过 MaxDecompressedLength。
var ErrDecompressedTooLarge = errors.New("decompressed data is too large")

// Unzip unzips data.
//
// 解压输出受 MaxDecompressedLength 限制，超限返回 ErrDecompressedTooLarge。
func Unzip(data []byte) ([]byte, error) {
	gr, err := gzip.NewReader(bytes.NewBuffer(data))
	if err != nil {
		return nil, err
	}
	defer gr.Close()

	return readLimited(gr, MaxDecompressedLength)
}

// readLimited 最多读 limit 字节；若源还有剩余则报错。
//
// 多读 1 字节是判定"超限"与"恰好读满"的唯一可靠手段：CopyN 返回 EOF 说明
// 源已耗尽，没有 EOF 则说明还有数据没读完，即已越界。limit<=0 表示不限制。
func readLimited(r io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		return io.ReadAll(r)
	}

	var buf bytes.Buffer
	n, err := io.CopyN(&buf, r, limit+1)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if n > limit {
		return nil, ErrDecompressedTooLarge
	}
	return buf.Bytes(), nil
}

// Zip zips data.
func Zip(data []byte) ([]byte, error) {
	var err error
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err = w.Write(data); err != nil {
		return nil, err
	}

	if err = w.Flush(); err != nil {
		return nil, err
	}

	if err = w.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}
