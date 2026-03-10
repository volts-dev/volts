package client

import (
	"context"
	"fmt"
	"time"

	"github.com/volts-dev/utils"
	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/errors"
	"github.com/volts-dev/volts/internal/addr"
	"github.com/volts-dev/volts/internal/body"
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
func (self *RpcClient) NewRequest(service, method string, request interface{}, options ...RequestOption) (*rpcRequest, error) {
	options = append(options,
		WithCodec(self.config.Serialize),
	)
	return newRpcRequest(service, method, request, options...)
}

// 阻塞请求
func (self *RpcClient) Call(request IRequest, opts ...CallOption) (*rpcResponse, error) {
	// make a copy of call opts
	callOpts := self.config.CallOptions
	callOpts.SelectOptions = append(callOpts.SelectOptions, selector.WithFilter(selector.FilterTrasport(self.config.Transport)))
	for _, opt := range opts {
		opt(&callOpts)
	}

	next, err := self.next(request, callOpts)
	if err != nil {
		return nil, err
	}

	ctx := callOpts.Context
	if ctx == nil {
		ctx = context.Background()
	}

	// check if we already have a deadline
	d, ok := ctx.Deadline()
	if !ok {
		callOpts.RequestTimeout = utils.Max(request.Options().RequestTimeout, callOpts.RequestTimeout)
		// no deadline so we create a new one
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, callOpts.RequestTimeout)
		defer cancel()
	} else {
		// got a deadline so no need to setup context
		// but we need to set the timeout we pass along
		callOpts.RequestTimeout = time.Until(d)
		//opt := WithRequestTimeout(time.Until(d))
		//opt(&callOpts)
	}

	// should we noop right here?
	select {
	case <-ctx.Done():
		return nil, errors.Timeout("volts.client", fmt.Sprintf("%v:%v", self.config.CallOptions.Address, ctx.Err()))
	default:
	}

	var gerr error
	var response *rpcResponse
	// get the retries
	retries := callOpts.Retries

	for i := 0; i <= retries; i++ {
		// 检查 context 是否已超时
		select {
		case <-ctx.Done():
			return nil, errors.Timeout("volts.client", fmt.Sprintf("%v:%v", self.config.CallOptions.Address, ctx.Err()))
		default:
		}

		// select next node
		// selector 可能因为过滤后得不到合适服务器
		node, err := next()
		if err != nil {
			gerr = err
		} else {
			// 本机 IP 自动转为 loopback 以获得最低延迟
			node.Address = addr.LocalFormat(node.Address)
			response, err = self.call(ctx, node, request, callOpts)
			// if the call succeeded lets bail early
			if err == nil {
				return response, nil
			}
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

	// 获取空闲链接
	dOpts := []transport.DialOption{
		transport.WithStream(),
	}

	if opts.DialTimeout >= 0 {
		dOpts = append(dOpts, transport.WithTimeout(opts.DialTimeout, opts.RequestTimeout, opts.RequestTimeout))
	}

	conn, err := self.pool.Get(node.Address, dOpts...)
	if err != nil {
		return nil, errors.InternalServerError("volts.client", err)
	}

	// 获取请求消息载体
	reqMsg := transport.GetMessageFromPool()
	defer transport.PutMessageToPool(reqMsg)

	reqMsg.SetMessageType(transport.MT_REQUEST)
	reqMsg.SetSerializeType(self.config.Serialize)

	// init header
	for k, v := range req.Header() {
		reqMsg.Header[k] = v[0]
	}
	md, ok := metadata.FromContext(ctx)
	if ok {
		for k, v := range md {
			reqMsg.Header[k] = v
		}
	}

	// set timeout in nanoseconds
	reqMsg.Header["Timeout"] = fmt.Sprintf("%d", opts.RequestTimeout)
	// set the content type for the request
	reqMsg.Header["Content-Type"] = req.ContentType()
	// set the accept header
	reqMsg.Header["Accept"] = req.ContentType()

	reqMsg.Path = req.Method()
	data := req.Body().Data.Bytes()
	if len(data) > 1024 && self.config.CompressType == transport.Gzip {
		data, err = transport.Zip(data)
		if err != nil {
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
		bd.Data.Write(respMsg.Payload)
		resp := &rpcResponse{
			contentType: respMsg.SerializeType(),
			body:        bd,
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
		// 超时，关闭连接
		self.pool.Release(conn, ctx.Err())
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
	selectOptions := append(opts.SelectOptions, selector.WithFilter(
		selector.FilterLabel("protocol", r.config.Transport.Protocol()),
	))

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
