package client

import (
	"context"
	"fmt"
	"time"

	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/internal/body"
	"github.com/volts-dev/volts/internal/errors"
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
	}
)

func NewRpcClient(opts ...Option) *RpcClient {
	cfg := newConfig(
		transport.NewTCPTransport(),
		opts...,
	)

	// 默认编码
	if cfg.SerializeType == "" {
		cfg.Serialize = codec.MsgPack
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
func (self *RpcClient) NewRequest(service, method string, request interface{}, optinos ...RequestOption) (*rpcRequest, error) {
	optinos = append(optinos,
		WithCodec(self.config.Serialize),
	)
	return newRpcRequest(service, method, request, optinos...)
}

func (self *RpcClient) call(ctx context.Context, node *registry.Node, req IRequest, opts CallOptions) (IResponse, error) {
	// 验证解码器
	msgCodece := codec.IdentifyCodec(self.config.Serialize)
	if msgCodece == nil { // no codec specified
		//call.Error = rpc.ErrUnsupportedCodec
		//client.mutex.Unlock()
		//call.done()
		return nil, errors.UnsupportedCodec("volts.client", self.config.SerializeType)
	}

	// 获取空闲链接
	dOpts := []transport.DialOption{
		transport.WithStream(),
	}

	if opts.DialTimeout >= 0 {
		dOpts = append(dOpts, transport.WithTimeout(opts.DialTimeout, opts.RequestTimeout, 0))
	}

	conn, err := self.pool.Get(node.Address, dOpts...)
	if err != nil {
		return nil, errors.InternalServerError("volts.client", "connection error: %v", err)
	}
	defer self.pool.Release(conn, nil)

	// 获取消息载体
	msg := transport.GetMessageFromPool()
	msg.SetMessageType(transport.MT_REQUEST)
	msg.SetSerializeType(self.config.Serialize)

	// init header
	for k, v := range req.Header() {
		msg.Header[k] = v[0]
	}
	md, ok := metadata.FromContext(ctx)
	if ok {
		for k, v := range md {
			msg.Header[k] = v
		}
	}

	// set timeout in nanoseconds
	msg.Header["Timeout"] = fmt.Sprintf("%d", opts.RequestTimeout)
	// set the content type for the request
	msg.Header["Content-Type"] = req.ContentType()
	// set the accept header
	msg.Header["Accept"] = req.ContentType()

	msg.Path = req.Method() // TODO msg 添加server action
	data := req.Body().Data.Bytes()
	if len(data) > 1024 && self.config.CompressType == transport.Gzip {
		data, err = transport.Zip(data)
		if err != nil {
			return nil, err
		}

		msg.SetCompressType(self.config.CompressType)
	}

	msg.Payload = data
	//seq := atomic.AddUint64(&self.seq, 1) - 1
	//codec := newRpcCodec(msg, c, cf, "")

	// 开始发送消息
	// wait for error response
	ch := make(chan error, 1)
	resp := &rpcResponse{}
	go func(resp *rpcResponse) {
		defer func() {
			if r := recover(); r != nil {
				ch <- errors.InternalServerError("volts.client", "panic recovered: %v", r)
			}
		}()

		// send request
		// 返回编译过的数据
		err := conn.Send(msg)
		if err != nil {
			ch <- err
			return
		}

		// recv request
		msg = transport.GetMessageFromPool()
		err = conn.Recv(msg)
		if err != nil {
			ch <- err
			return
		}

		// 状态码处理
		switch msg.MessageStatusType() {
		case transport.StatusOK:
			break
		case transport.StatusError:
			ch <- errors.New("StatusError", int32(transport.StatusError), string(msg.Payload))
			return
		default:
			ch <- errors.New("", int32(msg.MessageStatusType()), string(msg.Payload))
			return
		}

		bd := body.New(codec.IdentifyCodec(msg.SerializeType()))
		bd.Data.Write(msg.Payload)
		// 解码消息内容
		resp.contentType = msg.SerializeType()
		resp.body = bd // msg.Payload

		// success
		ch <- nil
	}(resp)

	err = nil
	select {
	case err := <-ch:
		return resp, err
	case <-ctx.Done():
		err = errors.Timeout("volts.client", fmt.Sprintf("%v", ctx.Err()))
		break
	}

	// set the stream error
	if err != nil {
		//stream.Lock()
		//stream.err = grr
		//stream.Unlock()
		return nil, err
	}

	return resp, nil
}

// 阻塞请求
func (self *RpcClient) Call(request IRequest, opts ...CallOption) (IResponse, error) {
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
		// no deadline so we create a new one
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, callOpts.RequestTimeout)
		defer cancel()
	} else {
		// got a deadline so no need to setup context
		// but we need to set the timeout we pass along
		opt := WithRequestTimeout(time.Until(d))
		opt(&callOpts)
	}

	// should we noop right here?
	select {
	case <-ctx.Done():
		return nil, errors.Timeout("volts.client", fmt.Sprintf("%v", ctx.Err()))
	default:
	}

	// return errors.New("volts.client", "request timeout", 408)
	call := func(i int, response *IResponse) error {
		// select next node
		// selector 可能因为过滤后得不到合适服务器
		node, err := next()
		if err != nil {
			return err
		}

		// make the call
		*response, err = self.call(ctx, node, request, callOpts)
		//r.opts.Selector.Mark(service, node, err)
		return err
	}
	var response IResponse
	// get the retries
	retries := callOpts.Retries
	ch := make(chan error, retries+1)
	var gerr error
	for i := 0; i <= retries; i++ {
		go func(i int, response *IResponse) {
			ch <- call(i, response)
		}(i, &response)

		select {
		case <-ctx.Done():
			return nil, errors.Timeout("volts.client", fmt.Sprintf("call timeout: %v", ctx.Err()))
		case err := <-ch:
			// if the call succeeded lets bail early
			if err == nil {
				return response, nil
			}

			retry, rerr := callOpts.Retry(ctx, request, i, err)
			if rerr != nil {
				return nil, rerr
			}

			if !retry {
				return nil, err
			}

			gerr = err
		}
	}

	return response, gerr
}

// next returns an iterator for the next nodes to call
func (r *RpcClient) next(request IRequest, opts CallOptions) (selector.Next, error) {
	// try get the proxy
	service, address, _ := net.Proxy(request.Service(), opts.Address)

	// return remote address
	if len(address) > 0 {
		nodes := make([]*registry.Node, len(address))

		for i, addr := range address {
			nodes[i] = &registry.Node{
				Address: addr,
				// Set the protocol
				Metadata: map[string]string{
					"protocol": "mucp",
				},
			}
		}

		// crude return method
		return func() (*registry.Node, error) {
			return nodes[time.Now().Unix()%int64(len(nodes))], nil
		}, nil
	}

	// get next nodes from the selector
	next, err := r.config.Selector.Select(service, opts.SelectOptions...)
	if err != nil {
		if err == selector.ErrNotFound {
			return nil, errors.InternalServerError("volts.client", "service %s: %s", service, err.Error())
		}
		return nil, errors.InternalServerError("volts.client", "error selecting %s node: %s", service, err.Error())
	}

	return next, nil
}

func (self *RpcClient) String() string {
	return "RpcClient"
}
