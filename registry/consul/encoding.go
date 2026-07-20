package consul

import (
	"bytes"
	"compress/zlib"
	"encoding/hex"
	"encoding/json"
	"io"

	"github.com/volts-dev/volts/registry"
)

func encode(buf []byte) string {
	var b bytes.Buffer
	defer b.Reset()

	w := zlib.NewWriter(&b)
	if _, err := w.Write(buf); err != nil {
		return ""
	}
	w.Close()

	return hex.EncodeToString(b.Bytes())
}

// maxDecodedTag 是单个服务标签解压后的字节上限。
//
// 标签装的是服务元数据（endpoint 定义、node metadata），正常在 KB 量级。
// 不设上限则一个被篡改/构造的 zlib 标签可膨胀到数 GB 把进程 OOM 掉 ——
// 注册中心应答同样是不可信输入（TLS 校验只保证链路，不保证内容合理）。
const maxDecodedTag = 4 << 20

func decode(d string) []byte {
	hr, err := hex.DecodeString(d)
	if err != nil {
		return nil
	}

	br := bytes.NewReader(hr)
	zr, err := zlib.NewReader(br)
	if err != nil {
		return nil
	}
	defer zr.Close()

	// 多读 1 字节用于判定越界：CopyN 返回 EOF 说明源已耗尽，
	// 没有 EOF 就说明还有数据，即已超过上限。
	var buf bytes.Buffer
	n, err := io.CopyN(&buf, zr, maxDecodedTag+1)
	if err != nil && err != io.EOF {
		return nil
	}
	if n > maxDecodedTag {
		return nil
	}

	return buf.Bytes()
}

func encodeEndpoints(en []*registry.Endpoint) []string {
	var tags []string
	for _, e := range en {
		if b, err := json.Marshal(e); err == nil {
			tags = append(tags, "e-"+encode(b))
		}
	}
	return tags
}

func decodeEndpoints(tags []string) []*registry.Endpoint {
	var en []*registry.Endpoint

	// use the first format you find
	var ver byte

	for _, tag := range tags {
		if len(tag) == 0 || tag[0] != 'e' {
			continue
		}

		// check version
		if ver > 0 && tag[1] != ver {
			continue
		}

		var e *registry.Endpoint
		var buf []byte

		// Old encoding was plain
		if tag[1] == '=' {
			buf = []byte(tag[2:])
		}

		// New encoding is hex
		if tag[1] == '-' {
			buf = decode(tag[2:])
		}

		if err := json.Unmarshal(buf, &e); err == nil {
			en = append(en, e)
		}

		// set version
		ver = tag[1]
	}
	return en
}

func encodeMetadata(md map[string]string) []string {
	var tags []string
	for k, v := range md {
		if b, err := json.Marshal(map[string]string{
			k: v,
		}); err == nil {
			// new encoding
			tags = append(tags, "t-"+encode(b))
		}
	}
	return tags
}

func decodeMetadata(tags []string) map[string]string {
	md := make(map[string]string)

	var ver byte

	for _, tag := range tags {
		if len(tag) == 0 || tag[0] != 't' {
			continue
		}

		// check version
		if ver > 0 && tag[1] != ver {
			continue
		}

		var kv map[string]string
		var buf []byte

		// Old encoding was plain
		if tag[1] == '=' {
			buf = []byte(tag[2:])
		}

		// New encoding is hex
		if tag[1] == '-' {
			buf = decode(tag[2:])
		}

		// Now unmarshal
		if err := json.Unmarshal(buf, &kv); err == nil {
			for k, v := range kv {
				md[k] = v
			}
		}

		// set version
		ver = tag[1]
	}
	return md
}

func encodeVersion(v string) []string {
	return []string{"v-" + encode([]byte(v))}
}

func decodeVersion(tags []string) (string, bool) {
	for _, tag := range tags {
		if len(tag) < 2 || tag[0] != 'v' {
			continue
		}

		// Old encoding was plain
		if tag[1] == '=' {
			return tag[2:], true
		}

		// New encoding is hex
		if tag[1] == '-' {
			return string(decode(tag[2:])), true
		}
	}
	return "", false
}
