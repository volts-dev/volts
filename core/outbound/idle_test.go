package outbound

import (
	"testing"
	"time"
)

func TestCloseIdleConnectionsDoesNotBuildPool(t *testing.T) {
	r := &rotator{}
	r.CloseIdleConnections()
	if r.idle != nil || len(r.personas)+len(r.reserves) != 0 {
		t.Fatal("清理空闲连接不能提前冻结尚未装载的身份配置")
	}
}

func TestCloseIdleConnectionsKeepsIdentityAndCooldown(t *testing.T) {
	p := newPersona(nil, 0, nil, 1)
	p.markFailure(time.Minute)
	p.mu.Lock()
	failures, until := p.failures, p.until
	p.mu.Unlock()
	r := &rotator{personas: []*persona{p}}
	r.CloseIdleConnections()
	if r.personas[0] != p {
		t.Fatal("网卡策略更新不应重建身份")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failures != failures || !p.until.Equal(until) {
		t.Fatal("网卡策略更新不应清空风控冷却")
	}
}
