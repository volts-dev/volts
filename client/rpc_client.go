package client

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/errors"
	"github.com/volts-dev/volts/internal/addr"
	"github.com/volts-dev/volts/internal/body"
	"github.com/volts-dev/volts/internal/header"
	"github.com/volts-dev/volts/internal/metadata"
	"github.com/volts-dev/volts/internal/net"
	"github.com/volts-dev/volts/internal/pool"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/selector"
	"github.com/volts-dev/volts/transport"
)

type (
	RpcClient struct {
		config   *Config
		pool     pool.Pool // connect pool
		closing  bool      // user has called Close
		shutdown bool      // server has told us to stop
		seq      uint64
	}
)

func NewRpcClient(opts ...Option) *RpcClient {
	cfg := newConfig(
		transport.NewTCPTransport(),
		opts...,
	)

	// 默认编码
	if cfg.SerializeType == "" {
		cfg.Serialize = codec.JSON
	}

	p := pool.NewPool(
		pool.Size(cfg.PoolSize),
		pool.TTL(cfg.PoolTtl),
		pool.Transport(cfg.Transport),
	)

	return &RpcClient{
		config: cfg,
		pool:   p,
	}
}

func (self *RpcClient) Init(opts ...Option) error {
	self.config.Init(opts...)
	return nil
}

func (self *RpcClient) Config() *Config {
	return self.config
}

// 新建请求
func (self *RpcClient) NewRequest(service, method string, request interface{}, options ...RequestOption) (IRequest, error) {
	options = append(options,
		WithCodec(self.config.Serialize),
	)
	return newRpcRequest(service, method, request, options...)
}

// 阻塞请求
func (self *RpcClient) Call(request IRequest, opts ...CallOption) (IResponse, error) {
	// make a copy of call opts
	callOpts := self.config.CallOptions
	callOpts.SelectOptions = append(callOpts.SelectOptions, selector.WithFilter(selector.FilterTrasport(self.config.Transport)))
	for _, opt := range opts {
		opt(&callOpts)
	}

	ctx := callOpts.Context
	if ctx == nil {
		ctx = request.Options().Context
	}
	if ctx == nil {
		ctx = context.Background()
	}
	budget := callOpts.RequestTimeout
	if t := request.Options().RequestTimeout; t > 0 {
		budget = t
	}
	callOpts.RequestTimeout = budget
	if budget > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, budget) // 不得延长已有的、更短父级预算。
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Timeout("volts.client", err.Error())
	}
	var next selector.Next
	var err error

	// 出站请求钩子：横切信息（调用方身份、追踪 id 等）统一在这里盖上，见 hook.go。
	// 位置在重试循环**之外** —— 每个请求盖一次，不是每次重试盖一次。
	ctx, err = applyRequestHooks(ctx, request, self.config.RequestHooks)
	if err != nil {
		return nil, errors.InternalServerError("volts.client", err.Error())
	}

	var gerr error
	var response IResponse
	// get the retries
	retries := callOpts.Retries

	// i < retries (NOT <=) —— 与 HTTP client 对齐：Retries=N 表示总尝试 N 次。
	// 原 i <= retries 多跑一次（N+1 次）；调用方若依赖旧 N+1 行为，需把 Retries 加 1。
	for i := 0; i < retries; i++ {
		// 检查 context 是否已超时
		select {
		case <-ctx.Done():
			return nil, errors.Timeout("volts.client", fmt.Sprintf("%v:%v", self.config.CallOptions.Address, ctx.Err()))
		default:
		}

		// select next node
		// selector 可能因为过滤后得不到合适服务器
		// selector 接口没有 Context：只取消等待，不改共享 selector，也不发送晚到的请求。
		node, err := waitRPCNode(ctx, func() (*registry.Node, error) {
			if next == nil {
				it, err := self.next(request, callOpts)
				if err != nil {
					return nil, err
				}
				next = it
			}
			return next()
		})
		if err != nil {
			gerr = err
		} else {
			// 本机 IP 自动转为 loopback 以获得最低延迟
			node.Address = addr.LocalFormat(node.Address)
			resp, err := self.call(ctx, node, request, callOpts)

			// 上报调用结果，供 selector 做失败节点摘除与熔断。
			// 此前 RPC 路径完全不上报，挂掉的节点会被反复选中直到注册中心
			// TTL 过期（分钟级）。
			if self.config.Selector != nil && ctx.Err() == nil {
				self.config.Selector.Mark(request.Service(), node, err)
			}

			// if the call succeeded lets bail early
			if err == nil {
				// 传输层成功 ≠ 调用成功：服务端把「路由不存在 / 编解码被拒 /
				// 鉴权失败 / Abort」写在消息状态位上，body 是空的。此前这里
				// 直接 return (resp, nil)，调用方无法与「正常返回空结果」区分，
				// 网关于是把一次失败的 RPC 原样变成 HTTP 200 + 空 body。
				//
				// 立即返回，不进重试、不改熔断上报：节点应答了就是活的（熔断
				// 只认连接级故障），而重试一个非幂等 RPC 可能造成重复写。
				if resp.status != transport.StatusOK {
					return resp, resp.statusError(request.Method())
				}
				return resp, nil
			}
			response = resp
			gerr = err
		}

		retry, rerr := callOpts.Retry(ctx, request, i, gerr)
		if rerr != nil {
			return nil, rerr
		}

		if !retry {
			return nil, gerr
		}
	}

	return response, gerr
}

func (self *RpcClient) call(ctx context.Context, node *registry.Node, req IRequest, opts CallOptions) (*rpcResponse, error) {
	// 验证解码器
	msgCodec := codec.IdentifyCodec(self.config.Serialize)
	if msgCodec == nil { // no codec specified
		return nil, errors.UnsupportedCodec("volts.client", self.config.SerializeType)
	}

	if err := ctx.Err(); err != nil {
		return nil, errors.Timeout("volts.client", err.Error())
	}
	remaining := opts.RequestTimeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining = time.Until(deadline)
	}
	if remaining <= 0 {
		return nil, errors.Timeout("volts.client", "request budget exhausted before connection acquisition")
	}
	// 池 key 包含传输超时，不能塞每次略有不同的 remaining，否则每个请求都新建一个池桶。
	// 拨号仍有固定上限；调用方取消立即返回，晚到连接由获取协程关闭，绝不发送请求。
	dialTimeout := opts.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = transport.DefaultTimeout
	}
	if opts.RequestTimeout > 0 && dialTimeout > opts.RequestTimeout {
		dialTimeout = opts.RequestTimeout
	}
	dOpts := []transport.DialOption{transport.WithStream(), transport.WithTimeout(dialTimeout, opts.RequestTimeout, opts.RequestTimeout)}
	type acquired struct {
		conn pool.Conn
		err  error
	}
	acquiredConn := make(chan acquired)
	go func() {
		var result acquired
		defer func() {
			if r := recover(); r != nil {
				result.err = errors.InternalServerError("volts.client", "panic acquiring connection: %v", r)
			}
			select {
			case acquiredConn <- result:
			case <-ctx.Done():
				if result.conn != nil {
					_ = self.pool.Release(result.conn, ctx.Err())
				}
			}
		}()
		result.conn, result.err = self.pool.Get(node.Address, dOpts...)
	}()
	var conn pool.Conn
	var err error
	select {
	case result := <-acquiredConn:
		if result.err != nil {
			return nil, errors.Wrap(ErrConnectFailed, "volts.client", 500, fmt.Sprintf("dial %s: %v", node.Address, result.err))
		}
		conn = result.conn
	case <-ctx.Done():
		return nil, errors.Timeout("volts.client", ctx.Err().Error())
	}
	if err := ctx.Err(); err != nil {
		self.pool.Release(conn, err)
		return nil, errors.Timeout("volts.client", err.Error())
	}

	// 获取请求消息载体
	reqMsg := transport.GetMessageFromPool()

	reqMsg.SetMessageType(transport.MT_REQUEST)
	reqMsg.SetSerializeType(self.config.Serialize)

	// Inherited metadata supplies defaults; this hop's explicit headers (including
	// empty values masking inbound credentials) must win after request hooks run.
	md, ok := metadata.FromContext(ctx)
	if ok {
		for k, v := range md {
			if req.Header().Values(k) == nil { // Header lookup is case-insensitive.
				reqMsg.Header[k] = v
			}
		}
	}
	for k, v := range req.Header() {
		reqMsg.Header[k] = v[0]
	}

	// set timeout in nanoseconds
	if deadline, ok := ctx.Deadline(); ok {
		remaining = time.Until(deadline)
	}
	if remaining <= 0 {
		transport.PutMessageToPool(reqMsg)
		self.pool.Release(conn, context.DeadlineExceeded)
		return nil, errors.Timeout("volts.client", "request budget exhausted")
	}
	reqMsg.Header["Timeout"] = fmt.Sprintf("%d", remaining)
	// set the content type for the request
	reqMsg.Header["Content-Type"] = req.ContentType()
	// set the accept header
	reqMsg.Header["Accept"] = req.ContentType()

	reqMsg.Path = req.Method()
	data := append([]byte(nil), req.Body().Data.Bytes()...) // IO 可能晚于调用方取消返回；不借请求正文。
	if len(data) > 1024 && self.config.CompressType == transport.Gzip {
		data, err = transport.Zip(data)
		if err != nil {
			transport.PutMessageToPool(reqMsg)
			self.pool.Release(conn, nil)
			return nil, err
		}
		reqMsg.SetCompressType(self.config.CompressType)
	}
	reqMsg.Payload = data

	// 使用带超时的 channel 做 send/recv
	type callResult struct {
		resp *rpcResponse
		err  error
	}
	ch := make(chan callResult, 1)

	go func() {
		defer transport.PutMessageToPool(reqMsg) // 帧只由使用它的 IO 协程归还。
		defer func() {
			if r := recover(); r != nil {
				ch <- callResult{nil, errors.InternalServerError("volts.client", "panic recovered: %v", r)}
			}
		}()

		// 发送请求
		if err := conn.Send(reqMsg); err != nil {
			ch <- callResult{nil, err}
			return
		}

		// 接收响应
		respMsg := transport.GetMessageFromPool()
		if err := conn.Recv(respMsg); err != nil {
			transport.PutMessageToPool(respMsg)
			ch <- callResult{nil, err}
			return
		}

		// 解码响应
		bd := body.New(codec.IdentifyCodec(respMsg.SerializeType()))
		*bd.Data = *bytes.NewBuffer(respMsg.TakePayload())
		resp := &rpcResponse{
			contentType: respMsg.SerializeType(),
			body:        bd,
			// 状态位与消息头必须在归还消息池之前拷走：respMsg 一旦回池就会被
			// 下一个请求覆写。此前两者都没拷，服务端的失败信号整个丢失。
			status: respMsg.MessageStatusType(),
			header: make(header.Header, len(respMsg.Header)),
		}
		for k, v := range respMsg.Header {
			resp.header.Set(k, v)
		}
		transport.PutMessageToPool(respMsg)

		ch <- callResult{resp, nil}
	}()

	select {
	case result := <-ch:
		if result.err != nil {
			// 有错误，关闭连接不复用
			self.pool.Release(conn, result.err)
			return nil, result.err
		}
		// 成功，归还连接到池中复用
		self.pool.Release(conn, nil)
		return result.resp, nil
	case <-ctx.Done():
		// 超时，关闭连接（使阻塞的 Send/Recv 立即返回 error）
		self.pool.Release(conn, ctx.Err())
		// IO 协程独占帧与正文，且结果通道有一格缓冲；迟到的退出自行归池。
		// 不再额外等两秒，否则过载时取消仍占着 HTTP 调用方与上游资源。
		return nil, errors.Timeout("volts.client", fmt.Sprintf("%v:%v", self.config.CallOptions.Address, ctx.Err()))
	}
}

// next returns an iterator for the next nodes to call
func (r *RpcClient) next(request IRequest, opts CallOptions) (selector.Next, error) {
	// try get the proxy
	service, address, _ := net.Proxy(request.Service(), opts.Address)

	// return remote address
	if len(address) > 0 {
		nodes := make([]*registry.Node, len(address))

		for i, add := range address {
			nodes[i] = &registry.Node{
				Address: add,
				// Set the protocol
				Metadata: map[string]string{
					"protocol": "mucp",
				},
			}
		}

		// crude return method
		return func() (*registry.Node, error) {
			return nodes[time.Now().UnixNano()%int64(len(nodes))], nil
		}, nil
	}
	// only get the things that are of http protocol
	selectOptions := append(opts.SelectOptions,
		selector.WithFilter(selector.FilterLabel("protocol", r.config.Transport.Protocol())),
		// ★ 再按**端点**筛一道：同一个服务名下的进程不一定都实现了这条路径
		// （别的进程 blank-import 了这个模块，或者滚动升级期间新旧二进制并存）。
		// 不筛的话请求会轮询到不认识它的那个进程，回一句 NotFound —— 与"这个
		// 部署没装那个模块"长得一模一样，而且是间歇性的。没有任何节点声明过这个
		// 名字时它原样放行，见 FilterEndpointIfKnown。
		selector.WithFilter(selector.FilterEndpointIfKnown(request.Method())),
	)

	// get next nodes from the selector
	next, err := r.config.Selector.Select(service, selectOptions...)
	if err != nil {
		if err == selector.ErrNotFound {
			return nil, errors.InternalServerError("volts.client", fmt.Sprintf("service %s: %s", service, err.Error()))
		}
		return nil, errors.InternalServerError("volts.client", fmt.Sprintf("error selecting %s node: %s", service, err.Error()))
	}

	return next, nil
}

func (self *RpcClient) String() string {
	return "RpcClient"
}

// waitRPCNode 的结果通道有界；取消后晚到结果只丢弃，不触碰调用方的连接/帧。
// 实际 discovery 仍由后端自己的超时终止，不能强行中断不支持 Context 的第三方 selector。
func waitRPCNode(ctx context.Context, selectNode func() (*registry.Node, error)) (*registry.Node, error) {
	type result struct {
		node *registry.Node
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		var value result
		defer func() {
			if r := recover(); r != nil {
				value.err = errors.InternalServerError("volts.client", "panic selecting node: %v", r)
			}
			ch <- value
		}()
		value.node, value.err = selectNode()
	}()
	select {
	case value := <-ch:
		return value.node, value.err
	case <-ctx.Done():
		return nil, errors.Timeout("volts.client", ctx.Err().Error())
	}
}
