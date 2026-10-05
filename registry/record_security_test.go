package registry

import (
	"errors"
	"testing"
)

func TestSignedNode_SignsACopyPerService(t *testing.T) {
	SetRecordSecurity(&RecordSecurity{Sign: func(s *Service) error {
		s.Nodes[0].Metadata["sig"] = "signed:" + s.Name
		return nil
	}})
	defer SetRecordSecurity(nil)

	// 同一次注册里所有服务共用一个 *Node（server.Register）：签名必须落在副本上。
	shared := &Node{Id: "n1", Address: "127.0.0.1:1", Metadata: map[string]string{"protocol": "TCP"}}
	a, err := SignedNode(&Service{Name: "sale"}, shared)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := SignedNode(&Service{Name: "stock"}, shared)
	if a.Metadata["sig"] != "signed:sale" || b.Metadata["sig"] != "signed:stock" {
		t.Fatalf("各服务的签名串了: %v / %v", a.Metadata, b.Metadata)
	}
	if _, ok := shared.Metadata["sig"]; ok || a == shared {
		t.Fatal("签名写到了共享的节点上")
	}
	if a.Metadata["protocol"] != "TCP" {
		t.Fatal("副本丢了原有的节点 metadata")
	}
}

func TestSignedNode_SignErrorStopsTheWrite(t *testing.T) {
	SetRecordSecurity(&RecordSecurity{Sign: func(*Service) error { return errors.New("no key") }})
	defer SetRecordSecurity(nil)
	if _, err := SignedNode(&Service{Name: "sale"}, &Node{Id: "n1"}); err == nil {
		t.Fatal("签不出来也照写了")
	}
}

func TestAcceptRecord(t *testing.T) {
	rec := &Service{Name: "sale", Nodes: []*Node{{Id: "n1", Metadata: map[string]string{}}}}
	if !AcceptRecord(rec) {
		t.Fatal("没装验签时应全收")
	}
	SetRecordSecurity(&RecordSecurity{Verify: func(s *Service) error {
		if s.Nodes[0].Metadata["sig"] == "" {
			return errors.New("unsigned")
		}
		return nil
	}})
	defer SetRecordSecurity(nil)
	if AcceptRecord(rec) {
		t.Fatal("验不过的注册项被收下了")
	}
	rec.Nodes[0].Metadata["sig"] = "x"
	if !AcceptRecord(rec) {
		t.Fatal("验得过的注册项被丢了")
	}
}

func TestSignedNode_NoSecurityIsIdentity(t *testing.T) {
	n := &Node{Id: "n1"}
	if got, err := SignedNode(&Service{Name: "sale"}, n); err != nil || got != n {
		t.Fatal("没装签名时应原样返回")
	}
}
