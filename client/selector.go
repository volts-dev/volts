package client

import "context"

// 服务选择器
// Selector defines selector that selects one service from candidates.
type (
	Selector interface {
		Select(ctx context.Context, servicePath, serviceMethod string, args interface{}) string
		UpdateServer(servers map[string]string)
	}
)
