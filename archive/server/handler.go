package server

import (
	"reflect"
)

type (
	// 代表一个控制集
	THandler struct {
		name string        // name of service
		rcvr reflect.Value // receiver of methods for the service
		typ  reflect.Type  // type of the receiver
		//method   map[string]*methodType   // registered methods
		//function map[string]*functionType // registered functions
	}
)
