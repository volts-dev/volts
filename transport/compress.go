package transport

import (
	"bytes"
	"compress/gzip"
	"io"
)

// Unzip unzips data.
func Unzip(data []byte) ([]byte, error) {
	gr, err := gzip.NewReader(bytes.NewBuffer(data))
	if err != nil {
		return nil, err
	}
	defer gr.Close()

	data, err = io.ReadAll(gr)
	if err != nil {
		return nil, err
	}
	return data, err
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
