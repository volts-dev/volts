package codec

import (
	"bytes"
	"encoding/gob"
	"log"
)

type gobCodec struct {
	buf bytes.Buffer
}

var Gob SerializeType = RegisterCodec("Gob", newGobCodec())

func newGobCodec() *byteCodec {
	return &byteCodec{}
}

// Encode returns raw slice of bytes.
func (c gobCodec) Encode(i interface{}) ([]byte, error) {
	var network bytes.Buffer        // Stand-in for a network connection
	enc := gob.NewEncoder(&network) // Will write to network.
	err := enc.Encode(i)
	if err != nil {
		log.Fatal("encode error:", err)
	}

	return network.Bytes(), nil
}

// Decode returns raw slice of bytes.
func (c gobCodec) Decode(data []byte, i interface{}) error {
	var network bytes.Buffer // Stand-in for a network connection

	dec := gob.NewDecoder(&network) // Will read from network.
	err := dec.Decode(&data)
	if err != nil {
		return err
	}
	return nil
}

func (c gobCodec) String() string {
	return "Gob"
}
