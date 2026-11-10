package transport

import "bytes"

// TakePayload returns independently owned payload storage and detaches Body and
// Payload from the message. Native Recv transfers its fresh decoded allocation;
// custom transports and replaced payloads are copied. Header and Path remain
// readable, including after Reset and subsequent receives. The caller must not
// retain or mutate aliases to the detached Body/Payload.
func (m *Message) TakePayload() []byte {
	p := m.Payload
	if len(p) > 0 {
		if len(p) != len(m.receivedPayload) || &p[0] != &m.receivedPayload[0] {
			p = bytes.Clone(p)
		}
		// A response buffer may append after consuming its payload. Never allow
		// that append to expose unrelated trailing frame bytes.
		p = p[:len(p):len(p)]
	} else {
		p = nil
	}
	m.Body, m.Payload, m.receivedPayload = nil, nil, nil
	return p
}
