package client

import (
	"bytes"
	"context"
	_errors "errors"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
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

type buffer struct {
	*bytes.Buffer
}

func (b *buffer) Close() error {
	b.Buffer.Reset()
	return nil
}

type (
	HttpClient struct {
		config   *Config
		client   *http.Client
		pool     pool.Pool // connect pool
		closing  bool      // user has called Close
		shutdown bool      // server has told us to stop
	}
)

func NewHttpClient(opts ...Option) (*HttpClient, error) {
	cfg := newConfig(
		transport.NewHTTPTransport(),
		opts...,
	)

	// 默认编码
	if cfg.SerializeType == 0 {
		cfg.SerializeType = codec.Bytes
	}

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
			return nil, err
		}
	} else {
		dialer = proxy.Direct
	}

	cfg.Transport.Config().TLSConfig = cfg.TLSConfig
	cli := &HttpClient{
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
		return nil, err
	}
	cli.client.Jar = jar

	cfg.Client = cli
	return cli, nil
}

func (self *HttpClient) String() string {
	return "HttpClient"
}

func (self *HttpClient) Init(opts ...Option) error {
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

func (self *HttpClient) Config() *Config {
	return self.config
}

// 新建请求
func (self *HttpClient) NewRequest(method, url string, data interface{}, optinos ...RequestOption) (*httpRequest, error) {
	opts := []RequestOption{WithCodec(self.config.SerializeType)}
	opts = append(opts,
		optinos...,
	)

	return newHttpRequest(method, url, data, opts...)
}

func (h *HttpClient) next(request *httpRequest, opts CallOptions) (selector.Next, error) {
	if h.config.Selector == nil {
		return func() (*registry.Node, error) {
			return &registry.Node{
				Address: request.url,
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

func newHTTPCodec(contentType string) (codec.ICodec, error) {
	if c, ok := defaultHTTPCodecs[contentType]; ok {
		return c, nil
	}
	return nil, fmt.Errorf("Unsupported Content-Type: %s", contentType)
}

func (h *HttpClient) call(ctx context.Context, node *registry.Node, req *httpRequest, opts CallOptions) (*httpResponse, error) {
	if ctx == nil {
		return nil, _errors.New("net/http: nil Context")
	}

	// set the address
	//address := node.Address
	header := req.Header // make(http.Header)
	if md, ok := metadata.FromContext(ctx); ok {
		for k, v := range md {
			header.Set(k, v)
		}
	}

	// set timeout in nanoseconds
	header.Set("Timeout", fmt.Sprintf("%d", opts.RequestTimeout))

	// set the content type for the request
	// 默认bytes 编码不改Content-Type 以request为主
	st := h.config.SerializeType
	if req.opts.SerializeType != h.config.SerializeType {
		st = req.opts.SerializeType
	}
	if st != codec.Bytes {
		header.Set("Content-Type", req.ContentType()) // TODO 自动类型
	}

	// to ReadCloser
	/*	data := make([]byte, 0)
		if req.Body().Data.Len() != 0 {
			data = req.Body().Data.Bytes()
		}*/
	buf := &buffer{bytes.NewBuffer(req.Body().Data.Bytes())}
	defer buf.Close()

	u := req.URL
	u.Host = removeEmptyPort(u.Host)
	hreq := &http.Request{
		Method:        req.method,
		URL:           u,
		Host:          u.Host,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        header,
		Body:          buf,
		ContentLength: int64(buf.Len()),
	}

	// make the request
	hrsp, err := h.client.Do(hreq.WithContext(ctx))
	if err != nil {
		return nil, errors.InternalServerError("http.client", err.Error())
	}

	// parse response
	/*b, err := ioutil.ReadAll(hrsp.Body)
	if err != nil {
		return nil, errors.InternalServerError("http.client", err.Error())
	}
	defer hrsp.Body.Close()
	*/
	bd := body.New(req.body.Codec)
	//bd.Data.Write(b) // NOTED 存入编码数据
	rsp := &httpResponse{
		response:   hrsp,
		body:       bd,
		Status:     hrsp.Status,
		StatusCode: hrsp.StatusCode,
	}

	return rsp, nil
}

// 阻塞请求
func (self *HttpClient) Call(request *httpRequest, opts ...CallOption) (*httpResponse, error) {
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

	ctx := callOpts.Context
	if ctx == nil {
		ctx = context.Background()
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
		if err != nil {
			return err
		}
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

func (self *HttpClient) CookiesManager() http.CookieJar {
	return self.client.Jar
}

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
