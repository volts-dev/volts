package avatar

import (
	"fmt"
	"reflect"
	"testing"
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

	virtulCtrl struct {
		ctrl interface{}
		hds  func() string
	}
)

func TestBase(t *testing.T) {
	av := New(reflect.ValueOf(&ctrl{}))
	av.SetFields("world", &midWare{})
	fn := av.NewCreator()
	fmt.Println(fn())
}
