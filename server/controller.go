package server

type (
	// Controller support middleware
	TController struct {
		rpcHandler *TRpcHandler
		webHandler *TWebHandler
	}
)

// return the handler for rpc
func (self *TController) GetHttpHandler() *TWebHandler {
	return self.webHandler
}

// return the handler for web
func (self *TController) GetRpcHandler() *TRpcHandler {
	return self.rpcHandler
}

func (self *TController) GetProtocol() string {
	if self.rpcHandler == nil {
		return "HTTP"
	} else {
		return "RPC"
	}
}
