package codec

import (
	"log"
	"testing"
	"time"
	//"github.com/araddon/dateparse"
)

func TestJosnInt64Value(t *testing.T) {
	type Test1 struct {
		Id   int64
		Name string
		Time string
	}
	type Test2 struct {
		Id   int64
		Name string
		Time time.Time
	}
	coder := new(jsonCodec)
	buf, err := coder.Encode(Test1{Id: 1234541341234123412, Name: "adc", Time: "2014-04-26"}) //"2016-01-02"
	if err != nil {

	}

	//buf = []byte(`{"id":1234541341234123412,"Time": "2014-04-11"}`)
	var test Test2
	err = coder.Decode(buf, &test)
	if err != nil {
		t.Fatal(err)
	}
	log.Print(test)
}
