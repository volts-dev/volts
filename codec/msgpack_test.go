package codec

import (
	"errors"
	"log"
	"testing"
)

func TestMg(t *testing.T) {
	type Test1 struct {
		Id   int64
		Name string
		//Time string
	}
	type Test2 struct {
		Id   int64
		Name string
		Test Test1
		//Time time.Time
	}
	coder := new(msgpackCodec)
	buf, err := coder.Encode(&Test2{Id: 1234541341234123412, Name: "adc", Test: Test1{Id: 1234541341234123412, Name: errors.New("403 Forbidden").Error()}}) //"2016-01-02"
	if err != nil {

	}

	//buf = []byte(`{"id":1234541341234123412,"Time": "2014-04-11"}`)
	var test *Test2
	err = coder.Decode(buf, &test)
	if err != nil {
		t.Fatal(err)
	}
	log.Print(test)
}
