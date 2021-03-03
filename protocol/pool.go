package protocol

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
			header := Header([12]byte{})
			header[0] = MagicNumber

			return &TMessage{
				Header: &header,
			}
		},
	}
)

func GetMessageFromPool() *TMessage {
	return msgPool.Get().(*TMessage)
}
func PutMessageToPool(msg *TMessage) {
	if msg != nil {
		msg.Reset()
		msgPool.Put(msg)
	}
}
