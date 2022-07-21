package router

import "fmt"

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
)

func (a ctrl) String(s ...interface{}) string {
	a.Name = fmt.Sprintf("%s%v", a.Name, s)
	return a.Name
}
