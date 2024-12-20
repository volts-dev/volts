package client

import (
	"bytes"
	"context"
	_errors "errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"
	"time"

	"github.com/volts-dev/utils"
	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/internal/body"
	"github.com/volts-dev/volts/internal/errors"
	"github.com/volts-dev/volts/internal/metadata"
	"github.com/volts-dev/volts/internal/pool"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/selector"
	"github.com/volts-dev/volts/transport"
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
		pool     pool.Pool // TODO connect pool
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
	if cfg.SerializeType == "" {
		cfg.Serialize = codec.Bytes
	}

	p := pool.NewPool(
		pool.Size(cfg.PoolSize),
		pool.TTL(cfg.PoolTtl),
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
	if cfg.ProxyUrl != "" {
		dialer, err = transport.NewProxyDialer(cfg.ProxyUrl, "")
		if err != nil {
			return nil, err
		}
	} else {
		dialer = proxy.Direct
	}

	cfg.Transport.Config().TlsConfig = cfg.TlsConfig
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
	if cfg.ProxyUrl != "" {
		cfg.DialOptions = append(cfg.DialOptions, transport.WithProxyURL(cfg.ProxyUrl))
	}

	self.config.Transport.Init()
	return nil
}

func (self *HttpClient) Config() *Config {
	return self.config
}

// 新建请求
func (self *HttpClient) NewRequest(method, url string, data interface{}, optinos ...RequestOption) (*httpRequest, error) {
	optinos = append(optinos,
		WithCodec(self.config.Serialize),
	)

	return newHttpRequest(method, url, data, optinos...)
}

func (h *HttpClient) next(request *httpRequest, opts CallOptions) (selector.Next, error) {

	/*
		// TODO 修改环境变量名称 get proxy
		if prx := os.Getenv("MICRO_PROXY"); len(prx) > 0 {
			service = prx
		}

	*/

	{
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
	}

	if request.opts.Service != "" && h.config.Registry.String() != "" {
		// 连接微服务
		var service string
		service = request.opts.Service

		// only get the things that are of http protocol
		selectOptions := append(opts.SelectOptions, selector.WithFilter(
			selector.FilterLabel("protocol", h.config.Transport.Protocol()),
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

	if request.URL.Host == "" {
		return nil, errors.NotFound("http.client", "target host is inavailable!")
	}

	return func() (*registry.Node, error) {
		return &registry.Node{
			Address: request.URL.Host,
		}, nil
	}, nil
}

func newHTTPCodec(contentType string) (codec.ICodec, error) {
	if c, ok := defaultHTTPCodecs[contentType]; ok {
		return c, nil
	}
	return nil, fmt.Errorf("Unsupported Content-Type: %s", contentType)
}

func (self *HttpClient) printRequest(buf *bytes.Buffer, req *http.Request, rsp *httpResponse) {
	if req != nil {
		buf.WriteString(fmt.Sprintf("%s %s %s\n", req.Method, req.URL.Path, req.Proto))
		for n, h := range req.Header {
			buf.WriteString(fmt.Sprintf("%s: %s  \n", n, strings.Join(h, ";")))
		}

		// cookies
		buf.WriteString("Cookies:\n")
		for _, cks := range self.client.Jar.Cookies(req.URL) {
			buf.WriteString(fmt.Sprintf("%s: %s  \n", cks.Name, cks.Value))
		}

		if bd, ok := req.Body.(*buffer); ok {
			context := bd.String()
			if !self.config.PrintRequestAll {
				if len(context) > 512 {
					context = context[:512] + "..."
				}
			}
			buf.WriteString(context + "\n")
		}
	}

	// Response
	if rsp != nil {
		if buf.Len() > 0 {
			buf.WriteString("\n")
		}
		buf.WriteString(fmt.Sprintf("Address: %s  \n", rsp.Request().URL.String()))
		buf.WriteString(fmt.Sprintf("Response code: %d  \n", rsp.StatusCode))
		buf.WriteString("Received Headers:\n")
		for n, h := range rsp.Header() {
			buf.WriteString(fmt.Sprintf("%s: %s  \n", n, strings.Join(h, ";")))
		}
		// cookies
		buf.WriteString("Received Cookies:\n")
		for _, cks := range rsp.Cookies() {
			buf.WriteString(fmt.Sprintf("%s: %s  \n", cks.Name, cks.Value))
		}
		//
		buf.WriteString("Received Payload:\n")

		context := string(rsp.Body().AsBytes())
		if !self.config.PrintRequestAll {
			if len(context) > 512 {
				context = context[:512] + "..."
			}
		}
		buf.WriteString(context + "\n")

	}
}

func (h *HttpClient) call(ctx context.Context, node *registry.Node, req *httpRequest, opts CallOptions) (*httpResponse, error) {
	if ctx == nil {
		return nil, _errors.New("net/http: nil Context")
	}

	// set the address
	//address := node.Address
	header := req.Header() // make(http.Header)
	if md, ok := metadata.FromContext(ctx); ok {
		for k, v := range md {
			header.Set(k, v)
		}
	}

	// User-Agent
	var ua string
	if h.config.UserAgent != "" {
		ua = h.config.UserAgent
	} else if h.config.Ja3.UserAgent != "" {
		ua = h.config.Ja3.UserAgent
	}

	if ua != "" {
		header.Set("User-Agent", ua)
	}

	// set timeout in nanoseconds
	header.Set("Timeout", fmt.Sprintf("%d", opts.RequestTimeout))

	// set the content type for the request
	// 默认bytes 编码不改Content-Type 以request为主
	st := h.config.Serialize
	if req.opts.SerializeType != h.config.Serialize {
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
	u.Scheme = h.config.Transport.Protocol()
	u.Host = removeEmptyPort(node.Address)
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

	// NOTE 必须提交前打印否则Body被清空req被修改清空
	var pr *bytes.Buffer
	if h.config.PrintRequest {
		pr = bytes.NewBufferString("")
		h.printRequest(pr, hreq, nil)
	}

	// make the request
	hrsp, err := h.client.Do(hreq.WithContext(ctx))
	if err != nil {
		return nil, errors.InternalServerError("http.client", err.Error())
	}

	// NOTE 提前读取避免Ctx被取消而出错
	b, err := io.ReadAll(hrsp.Body)
	if err != nil {
		return nil, errors.InternalServerError("http.client", err.Error())
	}

	bd := body.New(req.body.Codec)
	bd.Data.Write(b) // NOTED 存入编码数据
	rsp := &httpResponse{
		response:   hrsp,
		body:       bd,
		Status:     hrsp.Status,
		StatusCode: hrsp.StatusCode,
	}

	if h.config.PrintRequest {
		h.printRequest(pr, nil, rsp)
		log.Info(pr.String())
	}

	return rsp, nil
}

// 阻塞请求
func (self *HttpClient) Call(request *httpRequest, opts ...CallOption) (*httpResponse, error) {
	// make a copy of call opts
	callOpts := self.config.CallOptions
	callOpts.SelectOptions = append(callOpts.SelectOptions, selector.WithFilter(selector.FilterTrasport(self.config.Transport)))
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
		callOpts.RequestTimeout = utils.Max(request.Options().RequestTimeout, callOpts.RequestTimeout)
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, callOpts.RequestTimeout)
		defer cancel()
	} else {
		// got a deadline so no need to setup context
		// but we need to set the timeout we pass along
		callOpts.RequestTimeout = time.Until(d)
		//opt := WithRequestTimeout(d.Sub(time.Now()))
		//opt(&callOpts)
	}

	// should we noop right here?
	select {
	case <-ctx.Done():
		return nil, errors.New("http.client", 408, fmt.Sprintf("%v", ctx.Err()))
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
			return nil, errors.New("http.client", 408, fmt.Sprintf("%v", ctx.Err()))
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
