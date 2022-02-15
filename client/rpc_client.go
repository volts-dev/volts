package client

import (
	"context"
	"fmt"
	"time"

	"github.com/asim/go-micro/v3/metadata"
	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/errors"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/selector"
	"github.com/volts-dev/volts/transport"
	"github.com/volts-dev/volts/util/body"
	vnet "github.com/volts-dev/volts/util/net"
	"github.com/volts-dev/volts/util/pool"
)

type (
	rpcClient struct {
		config   *Config
		pool     pool.Pool // connect pool
		closing  bool      // user has called Close
		shutdown bool      // server has told us to stop
	}
)

func NewRpcClient(opts ...Option) IClient {
	cfg := newConfig(opts...)
	cfg.Init(
		Transport(transport.NewTCPTransport()),
	)

	p := pool.NewPool(
		pool.Size(cfg.PoolSize),
		pool.TTL(cfg.PoolTTL),
		pool.Transport(cfg.Transport),
	)

	cli := &rpcClient{
		config: cfg,
		pool:   p,
	}

	return cli
}

func (self *rpcClient) Init(opts ...Option) error {
	for _, opt := range opts {
		opt(self.config)
	}
	return nil
}

func (self *rpcClient) Config() *Config {
	return self.config
}

// 新建请求
func (self *rpcClient) NewRequest(service, method string, request interface{}, reqOpts ...RequestOption) IRequest {
	return NewRpcRequest(service, method, request, reqOpts...)
}

func (self *rpcClient) call(ctx context.Context, node *registry.Node, req IRequest, opts CallOptions) (IResponse, error) {
	address := node.Address

	msg := transport.GetMessageFromPool()
	md, ok := metadata.FromContext(ctx)
	if ok {
		for k, v := range md {
			// don't copy Micro-Topic header, that used for pub/sub
			// this fix case then client uses the same context that received in subscriber
			if k == "Micro-Topic" {
				continue
			}
			msg.Header[k] = v
		}
	}
	msg.SetMessageType(transport.MT_REQUEST)
	msg.SetSerializeType(self.config.SerializeType)

	// set timeout in nanoseconds
	msg.Header["Timeout"] = fmt.Sprintf("%d", opts.RequestTimeout)
	// set the content type for the request
	msg.Header["Content-Type"] = req.ContentType()
	// set the accept header
	msg.Header["Accept"] = req.ContentType()

	// 获得解码器
	msgCodece := codec.IdentifyCodec(self.config.SerializeType)
	if msgCodece == nil { // no codec specified
		//call.Error = rpc.ErrUnsupportedCodec
		//client.mutex.Unlock()
		//call.done()
		return nil, errors.UnsupportedCodec("volts.client", self.config.SerializeType)
	}

	dOpts := []transport.DialOption{
		transport.WithStream(),
	}

	//if opts.DialTimeout >= 0 {
	//	dOpts = append(dOpts, transport.WithTimeout(opts.DialTimeout))
	//}

	// 获取空闲链接
	conn, err := self.pool.Get(address, dOpts...)
	if err != nil {
		return nil, errors.InternalServerError("volts.client", "connection error: %v", err)
	}

	msg.Path = req.Service()
	data, err := msgCodece.Encode(req.Body())
	if err != nil {
		logger.Dbg("odec.Encode(call.Args)", err.Error())
		//call.Error = err
		//call.done()
		return nil, err
	}
	if len(data) > 1024 && self.config.CompressType == transport.Gzip {
		data, err = transport.Zip(data)
		if err != nil {
			//call.Error = err
			//call.done()
			return nil, err
		}

		msg.SetCompressType(self.config.CompressType)
	}

	msg.Payload = data
	//seq := atomic.AddUint64(&self.seq, 1) - 1
	//codec := newRpcCodec(msg, c, cf, "")

	// wait for error response
	ch := make(chan error, 1)

	resp := &rpcResponse{
		//body: []byte{},
	}
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

		b := &body.TBody{
			Codec: codec.IdentifyCodec(msg.SerializeType()),
		}
		b.Data.Write(msg.Payload)
		// 解码消息内容
		resp.contentType = msg.SerializeType()
		resp.body = b // msg.Payload
		/*
			///	移动到reponse里处理
				data := msg.Payload
				if len(data) > 0 {
					msgCodece := codec.IdentifyCodec(msg.SerializeType())
					if msgCodece == nil {
						//call.Error = ServiceError(ErrUnsupportedCodec.Error())
						ch <- errors.UnsupportedCodec("volts.client", msg.SerializeType())
						return
					} else {

						// 解码内容
						err = msgCodece.Decode(data, &resp.body)
						if err != nil {
							ch <- err
							return
						}
					}
				}
		*/
		// success
		ch <- nil
	}(resp)

	var grr error
	select {
	case err := <-ch:
		return resp, err
	case <-ctx.Done():
		grr = errors.Timeout("volts.client", fmt.Sprintf("%v", ctx.Err()))
		break
	}

	// set the stream error
	if grr != nil {
		//stream.Lock()
		//stream.err = grr
		//stream.Unlock()

		return nil, grr
	}

	return resp, nil
}

// 阻塞请求
func (self *rpcClient) Call(ctx context.Context, request IRequest, opts ...CallOption) (IResponse, error) {
	// make a copy of call opts
	callOpts := self.config.CallOptions
	for _, opt := range opts {
		opt(&callOpts)
	}

	next, err := self.next(request, callOpts)
	if err != nil {
		return nil, err
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
		node, err := next()
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
func (r *rpcClient) next(request IRequest, opts CallOptions) (selector.Next, error) {
	// try get the proxy
	service, address, _ := vnet.Proxy(request.Service(), opts.Address)

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