package router

import (
	"fmt"
)

type (
	midWare struct {
		name string
	}
	iMidWare interface {
		String() string
	}

	ctrl struct {
		Name    string
		MidWare *midWare
	}
	iCtrl interface {
		String(...interface{}) string
	}
	virtulCtrl struct {
		ctrl interface{}
		hds  func() string
	}

	iwrapper interface {
		String(...interface{}) string
	}
	wrapper struct {
		Func func(...interface{}) string
	}
)

func (w wrapper) String(s ...interface{}) string {
	return w.Func(s...)
}
func (a ctrl) String(s ...interface{}) string {
	a.Name = fmt.Sprintf("%s%v", a.Name, s)
	return a.Name
}
