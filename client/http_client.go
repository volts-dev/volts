package client

import (
	"bytes"
	"context"
	_errors "errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/asim/go-micro/v3/metadata"
	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/errors"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/selector"
	"github.com/volts-dev/volts/transport"
	"github.com/volts-dev/volts/util/body"
	"github.com/volts-dev/volts/util/pool"
	"golang.org/x/net/http/httpguts"
	"golang.org/x/net/proxy"
)

var (
	defaultHTTPCodecs = map[string]codec.ICodec{
		"application/json": codec.IdentifyCodec(codec.JSON),
		//"application/proto":        protoCodec{},
		//"application/protobuf":     protoCodec{},
		//"application/octet-stream": protoCodec{},
	}
)

type (
	buffer struct {
		*bytes.Buffer
	}

	httpClient struct {
		config   *Config
		client   *http.Client
		pool     pool.Pool // connect pool
		closing  bool      // user has called Close
		shutdown bool      // server has told us to stop
	}
)

// Given a string of the form "host", "host:port", or "[ipv6::address]:port",
// return true if the string includes a port.
func hasPort(s string) bool { return strings.LastIndex(s, ":") > strings.LastIndex(s, "]") }

// removeEmptyPort strips the empty port in ":port" to ""
// as mandated by RFC 3986 Section 6.2.3.
func removeEmptyPort(host string) string {
	if hasPort(host) {
		return strings.TrimSuffix(host, ":")
	}
	return host
}

func isNotToken(r rune) bool {
	return !httpguts.IsTokenRune(r)
}
func (b *buffer) Close() error {
	b.Buffer.Reset()
	return nil
}

func NewHttpClient(opts ...Option) *httpClient {
	cfg := newConfig(
		transport.NewHTTPTransport(),
		opts...,
	)

	p := pool.NewPool(
		pool.Size(cfg.PoolSize),
		pool.TTL(cfg.PoolTTL),
		pool.Transport(cfg.Transport),
	)

	// 使用指纹
	var dialOptions []transport.DialOption
	if cfg.Ja3.Ja3 != "" {
		dialOptions = append(dialOptions, transport.WithJa3(cfg.Ja3.Ja3, cfg.Ja3.UserAgent))
	}

	// 代理
	var dialer proxy.Dialer
	var err error
	if cfg.ProxyURL != "" {
		dialer, err = transport.NewProxyDialer(cfg.ProxyURL, "")
		if err != nil {
			log.Panic(err)
		}
	} else {
		dialer = proxy.Direct
	}

	cfg.Transport.Config().TLSConfig = cfg.TLSConfig
	cli := &httpClient{
		config: cfg,
		pool:   p,
		client: &http.Client{
			Transport: &roundTripper{
				Dialer: dialer,
				DialTLS: func(network, addr string) (net.Conn, error) {
					dialOptions = append(dialOptions,
						transport.WithDialer(dialer),
						transport.WithTLS(),
						//transport.WithContext(ctx),
						transport.WithNetwork(network),
					)
					dialConn, err := cfg.Transport.Dial(addr, dialOptions...)
					if err != nil {
						return nil, err
					}

					return dialConn.Conn(), nil
				},
			},
		},
	}

	// TODO add switch
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil
	}
	cli.client.Jar = jar

	cfg.Client = cli
	return cli
}

func (self *httpClient) String() string {
	return "httpClient"
}

func (self *httpClient) Init(opts ...Option) error {
	cfg := self.config
	for _, opt := range opts {
		opt(cfg)
	}

	// clear
	cfg.DialOptions = nil

	if cfg.Ja3.Ja3 != "" {
		cfg.DialOptions = append(cfg.DialOptions, transport.WithJa3(cfg.Ja3.Ja3, cfg.Ja3.UserAgent))
	}

	// 使用代理
	if cfg.ProxyURL != "" {
		cfg.DialOptions = append(cfg.DialOptions, transport.WithProxyURL(cfg.ProxyURL))
	}

	self.config.Transport.Init()
	return nil
}

func (self *httpClient) Config() *Config {
	return self.config
}

// 新建请求
func (self *httpClient) NewRequest(service, method string, request interface{}, reqOpts ...RequestOption) (IRequest, error) {
	return NewHttpRequest(service, method, request, reqOpts...)
}

func (h *httpClient) next(request IRequest, opts CallOptions) (selector.Next, error) {
	if h.config.Selector == nil {
		return func() (*registry.Node, error) {
			return &registry.Node{
				Address: request.Service(),
			}, nil
		}, nil
	}
	service := request.Service()

	// get proxy
	if prx := os.Getenv("MICRO_PROXY"); len(prx) > 0 {
		service = prx
	}

	// get proxy address
	if prx := os.Getenv("MICRO_PROXY_ADDRESS"); len(prx) > 0 {
		opts.Address = []string{prx}
	}

	// return remote address
	if len(opts.Address) > 0 {
		return func() (*registry.Node, error) {
			return &registry.Node{
				Address: opts.Address[0],
				Metadata: map[string]string{
					"protocol": "http",
				},
			}, nil
		}, nil
	}

	// only get the things that are of mucp protocol
	selectOptions := append(opts.SelectOptions, selector.WithFilter(
		selector.FilterLabel("protocol", "http"),
	))

	// get next nodes from the selector
	next, err := h.config.Selector.Select(service, selectOptions...)
	if err != nil && err == selector.ErrNotFound {
		return nil, errors.NotFound("http.client", err.Error())
	} else if err != nil {
		return nil, errors.InternalServerError("http.client", err.Error())
	}

	return next, nil
}

func (h *httpClient) newHTTPCodec(contentType string) (codec.ICodec, error) {
	if c, ok := defaultHTTPCodecs[contentType]; ok {
		return c, nil
	}
	return nil, fmt.Errorf("Unsupported Content-Type: %s", contentType)
}

func (h *httpClient) call(ctx context.Context, node *registry.Node, req IRequest, opts CallOptions) (*httpResponse, error) {
	r, ok := req.(*httpRequest)
	if !ok {
		return nil, _errors.New("the request is not for http!")
	}

	if ctx == nil {
		return nil, _errors.New("net/http: nil Context")
	}

	// set the address
	address := node.Address
	header := r.Header // make(http.Header)
	if md, ok := metadata.FromContext(ctx); ok {
		for k, v := range md {
			header.Set(k, v)
		}
	}

	// set timeout in nanoseconds
	header.Set("Timeout", fmt.Sprintf("%d", opts.RequestTimeout))
	// set the content type for the request
	//header.Set("Content-Type", req.ContentType()) // TODO 自动类型

	// marshal request
	var err error
	var buf *buffer
	var cf codec.ICodec
	data := make([]byte, 0)
	if req.Body() != nil {
		// get codec
		cf, err = h.newHTTPCodec(req.ContentType())
		if err != nil {
			return nil, errors.InternalServerError("http.client", err.Error())
		}

		err = cf.Decode(req.Body().([]byte), data)
		if err != nil {
			return nil, errors.InternalServerError("http.client", err.Error())
		}

	}

	// 无论如何都要创建
	buf = &buffer{bytes.NewBuffer(data)}
	defer buf.Close()

	u, err := url.Parse(address)
	if err != nil {
		return nil, err
	}

	u.Host = removeEmptyPort(u.Host)
	hreq := &http.Request{
		Method:        r.method,
		URL:           u,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        header,
		Body:          buf,
		ContentLength: int64(len(data)),
		Host:          u.Host,
	}

	//hreq, _ = http.NewRequest("get", "https://www.baidu.com/", nil)
	// make the request
	hrsp, err := h.client.Do(hreq.WithContext(ctx))
	if err != nil {
		return nil, errors.InternalServerError("http.client", err.Error())
	}
	//defer hrsp.Body.Close()

	// parse response
	//b, err := ioutil.ReadAll(hrsp.Body)
	//if err != nil {
	//	return nil, errors.InternalServerError("http.client", err.Error())
	//}

	bd := &body.TBody{
		Codec: cf,
	}
	//bd.Data.Write(b)
	rsp := &httpResponse{
		response: hrsp,
		body:     bd,

		Status:     hrsp.Status,
		StatusCode: hrsp.StatusCode,
	}
	/*
		// unmarshal
		if err := cf.Decode(b, rsp.body); err != nil {
			return nil, errors.InternalServerError("http.client", err.Error())
		}*/
	return rsp, nil
}

// 阻塞请求
func (self *httpClient) Call(ctx context.Context, request IRequest, opts ...CallOption) (*httpResponse, error) {
	// make a copy of call opts
	callOpts := self.config.CallOptions
	for _, opt := range opts {
		opt(&callOpts)
	}

	// get next nodes from the selector
	next, err := self.next(request, callOpts)
	if err != nil {
		return nil, err
	}

	// check if we already have a deadline
	d, ok := ctx.Deadline()
	if !ok {
		// no deadline so we create a new one
		ctx, _ = context.WithTimeout(ctx, callOpts.RequestTimeout)
	} else {
		// got a deadline so no need to setup context
		// but we need to set the timeout we pass along
		opt := WithRequestTimeout(d.Sub(time.Now()))
		opt(&callOpts)
	}

	// should we noop right here?
	select {
	case <-ctx.Done():
		return nil, errors.New("http.client", fmt.Sprintf("%v", ctx.Err()), 408)
	default:
	}

	// make copy of call method
	hcall := self.call

	// wrap the call in reverse
	//for i := len(callOpts.CallWrappers); i > 0; i-- {
	//	hcall = callOpts.CallWrappers[i-1](hcall)
	//}

	// return errors.New("http.client", "request timeout", 408)
	call := func(i int, response *httpResponse) error {
		// call backoff first. Someone may want an initial start delay
		/*t, err := callOpts.Backoff(ctx, request, i)
		if err != nil {
			return errors.InternalServerError("http.client", err.Error())
		}

		// only sleep if greater than 0
		if t.Seconds() > 0 {
			time.Sleep(t)
		}
		*/
		// select next node
		node, err := next()
		if err != nil && err == selector.ErrNotFound {
			return errors.NotFound("http.client", err.Error())
		} else if err != nil {
			return errors.InternalServerError("http.client", err.Error())
		}

		// make the call
		resp, err := hcall(ctx, node, request, callOpts)
		if self.config.Selector != nil {
			self.config.Selector.Mark(request.Service(), node, err)
		}
		*response = *resp
		return err
	}

	ch := make(chan error, callOpts.Retries)
	var gerr error
	response := &httpResponse{}
	// 调用
	for i := 0; i < callOpts.Retries; i++ {
		go func(i int, response *httpResponse) {
			ch <- call(i, response)
		}(i, response)

		select {
		case <-ctx.Done():
			return nil, errors.New("http.client", fmt.Sprintf("%v", ctx.Err()), 408)
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

func (self *httpClient) CookiesManager() http.CookieJar {
	return self.client.Jar
}
