package client

type (
	// Call represents an active RPC.
	TCall struct {
		ServicePath   string            // The name of the service and method to call.
		ServiceMethod string            // The name of the service and method to call.
		Metadata      map[string]string //metadata
		ResMetadata   map[string]string
		Args          interface{} // The argument to the function (*struct).
		Reply         interface{} // The reply from the function (*struct).
		Error         error       // After completion, the error status.
		Done          chan *TCall // Strobes when call is complete.
		Raw           bool        // raw message or not
	}
)
