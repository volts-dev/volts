package main

import (
	"context"

	"github.com/volts-dev/logger"
	"github.com/volts-dev/volts/client"
)

func main() {
	service := "Arith.Mul"
	endpoint := "Test.Endpoint"
	address := "127.0.0.1:35999"

	req := client.NewHttpRequest(service, endpoint, nil)

	// test calling remote address
	if _, err := client.Call(context.Background(), req, client.WithAddress(address)); err != nil {
		logger.Err("call with address error:", err)
	}
}
