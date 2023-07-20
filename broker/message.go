package broker

// Message is a message send/received from the broker.
type Message struct {
	Header map[string]string
	Body   []byte
}
