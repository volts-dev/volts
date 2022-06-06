package main

import (
	"github.com/volts-dev/volts/client"
	"github.com/volts-dev/volts/logger"
)

func main() {
	service := "Arith.Mul"
	endpoint := "Test.Endpoint"
	address := "127.0.0.1:35999"
	cli, err := client.NewHttpClient()
	req, _ := cli.NewRequest(service, endpoint, nil)

	// test calling remote address
	if _, err = cli.Call(req, client.WithAddress(address)); err != nil {
		logger.Err("call with address error:", err)
	}
}
