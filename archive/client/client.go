package client

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"strconv"
	"sync"
	"time"
	"vectors/volts"
	"vectors/volts/codec"
	"vectors/volts/message"

	log "github.com/VectorsOrigin/logger"
)

const (
	XVersion           = "X-RPCX-Version"
	XMessageType       = "X-RPCX-MesssageType"
	XHeartbeat         = "X-RPCX-Heartbeat"
	XOneway            = "X-RPCX-Oneway"
	XMessageStatusType = "X-RPCX-MessageStatusType"
	XSerializeType     = "X-RPCX-SerializeType"
	XMessageID         = "X-RPCX-MessageID"
	XServicePath       = "X-RPCX-ServicePath"
	XServiceMethod     = "X-RPCX-ServiceMethod"
	XMeta              = "X-RPCX-Meta"
	XErrorMessage      = "X-RPCX-ErrorMessage"
)

// ErrShutdown connection is closed.
var (
	ErrShutdown         = errors.New("connection is shut down")
	ErrUnsupportedCodec = errors.New("unsupported codec")
)

type (
	seqKey struct{}
	// ServiceError is an error from server.
	ServiceError string

	TClient struct {
		Conn   net.Conn
		mutex  sync.Mutex // protects following
		option Option

		msgPool  sync.Pool
		pending  map[uint64]*TCall
		closing  bool // user has called Close
		shutdown bool // server has told us to stop

		seq               uint64
		r                 *bufio.Reader
		ServerMessageChan chan<- *message.TMessage
	}
)

const (
	// ReaderBuffsize is used for bufio reader.
	ReaderBuffsize = 16 * 1024
	// WriterBuffsize is used for bufio writer.
	WriterBuffsize = 16 * 1024
)

func (e ServiceError) Error() string {
	return string(e)
}

func urlencode(data map[string]string) string {
	if len(data) == 0 {
		return ""
	}
	var buf bytes.Buffer
	for k, v := range data {
		buf.WriteString(url.QueryEscape(k))
		buf.WriteByte('=')
		buf.WriteString(url.QueryEscape(v))
		buf.WriteByte('&')
	}
	s := buf.String()
	return s[0 : len(s)-1]
}
func convertRes2Raw(res *message.TMessage) (map[string]string, []byte, error) {
	m := make(map[string]string)
	m[XVersion] = strconv.Itoa(int(res.Version()))
	if res.IsHeartbeat() {
		m[XHeartbeat] = "true"
	}
	if res.IsOneway() {
		m[XOneway] = "true"
	}
	if res.MessageStatusType() == message.Error {
		m[XMessageStatusType] = "Error"
	} else {
		m[XMessageStatusType] = "Normal"
	}

	if res.CompressType() == message.Gzip {
		m["Content-Encoding"] = "gzip"
	}

	m[XMeta] = urlencode(res.Metadata)
	m[XSerializeType] = strconv.Itoa(int(res.SerializeType()))
	m[XMessageID] = strconv.FormatUint(res.Seq(), 10)
	// TODO
	m[XServicePath] = res.ServicePath     // 废弃
	m[XServiceMethod] = res.ServiceMethod // 废弃

	return m, res.Payload, nil
}

func NewClient(opt Option) *TClient {
	cli := &TClient{
		option: opt,
	}

	cli.msgPool.New = func() interface{} {
		header := message.Header([12]byte{})
		header[0] = message.MagicNumber

		return &message.TMessage{
			Header: &header,
		}
	}
	return cli
}

// Connect connects the server via specified network.
func (c *TClient) Connect(network, address string) error {
	var conn net.Conn
	var err error

	switch network {
	case "http":
		//conn, err = newDirectHTTPConn(c, network, address)
	case "kcp":
		//conn, err = newDirectKCPConn(c, network, address)
	case "quic":
		//conn, err = newDirectQuicConn(c, network, address)
	case "unix":
		//conn, err = newDirectConn(c, network, address)
	default:
		conn, err = newDirectConn(c, network, address)
	}

	if err == nil && conn != nil {
		if c.option.ReadTimeout != 0 {
			conn.SetReadDeadline(time.Now().Add(c.option.ReadTimeout))
		}
		if c.option.WriteTimeout != 0 {
			conn.SetWriteDeadline(time.Now().Add(c.option.WriteTimeout))
		}

		c.Conn = conn
		c.r = bufio.NewReaderSize(conn, ReaderBuffsize)
		//c.w = bufio.NewWriterSize(conn, WriterBuffsize)

		// start reading and writing since connected
		go c.input()

		if c.option.Heartbeat && c.option.HeartbeatInterval > 0 {
			go c.heartbeat()
		}

	}

	return err
}

func (client *TClient) handleServerRequest(msg *message.TMessage) {
	defer func() {
		if r := recover(); r != nil {
			log.Errf("ServerMessageChan may be closed so client remove it. Please add it again if you want to handle server requests. error is %v", r)
			client.ServerMessageChan = nil
		}
	}()

	t := time.NewTimer(5 * time.Second)
	select {
	case client.ServerMessageChan <- msg:
	case <-t.C:
		log.Warnf("ServerMessageChan may be full so the server request %d has been dropped", msg.Seq())
	}
	t.Stop()
}

// 心跳回应
func (client *TClient) heartbeat() {
	t := time.NewTicker(client.option.HeartbeatInterval)

	for range t.C {
		if client.shutdown || client.closing {
			return
		}

		err := client.Call("", nil, nil)
		if err != nil {
			log.Warnf("failed to heartbeat to %s", client.Conn.RemoteAddr().String())
		}
	}
}

func (client *TClient) input() {
	var err error
	var res = message.NewMessage()

	for err == nil {
		if client.option.ReadTimeout != 0 {
			client.Conn.SetReadDeadline(time.Now().Add(client.option.ReadTimeout))
		}

		err = res.Decode(client.r)
		//res, err = protocol.Read(client.r)

		if err != nil {
			break
		}
		seq := res.Seq()
		var call *TCall
		isServerMessage := (res.MessageType() == message.Request && !res.IsHeartbeat() && res.IsOneway())
		if !isServerMessage {
			client.mutex.Lock()
			call = client.pending[seq]
			delete(client.pending, seq)
			client.mutex.Unlock()
		}

		switch {
		case call == nil:
			if isServerMessage {
				if client.ServerMessageChan != nil {
					go client.handleServerRequest(res)
					res = message.NewMessage()
				}
				continue
			}
		case res.MessageStatusType() == message.Error:
			// We've got an error response. Give this to the request;
			call.Error = ServiceError(res.Metadata[message.ServiceError])
			call.ResMetadata = res.Metadata

			if call.Raw {
				call.Metadata, call.Reply, _ = convertRes2Raw(res)
				call.Metadata[XErrorMessage] = call.Error.Error()
			}
			call.done()
		default:
			if call.Raw {
				call.Metadata, call.Reply, _ = convertRes2Raw(res)
			} else {
				data := res.Payload
				if len(data) > 0 {
					codec := codec.Codecs[res.SerializeType()]
					if codec == nil {
						call.Error = ServiceError(ErrUnsupportedCodec.Error())
					} else {
						err = codec.Decode(data, call.Reply)
						if err != nil {
							call.Error = ServiceError(err.Error())
						}
					}
				}
				call.ResMetadata = res.Metadata
			}

			call.done()
		}

		res.Reset()
	}
	// Terminate pending calls.
	client.mutex.Lock()
	client.shutdown = true
	closing := client.closing
	if err == io.EOF {
		if closing {
			err = ErrShutdown
		} else {
			err = io.ErrUnexpectedEOF
		}
	}
	for _, call := range client.pending {
		call.Error = err
		call.done()
	}
	client.mutex.Unlock()
	if err != nil && err != io.EOF && !closing {
		log.Err("rpcx: client protocol error:", err)
	}
}

// Close calls the underlying codec's Close method. If the connection is already
// shutting down, ErrShutdown is returned.
func (client *TClient) Close() error {
	client.mutex.Lock()
	if client.closing {
		client.mutex.Unlock()
		return ErrShutdown
	}
	client.closing = true
	client.mutex.Unlock()
	return client.Conn.Close()
}

// Call invokes the named function, waits for it to complete, and returns its error status.
func (client *TClient) Call(serviceMethod string, args interface{}, reply interface{}) error {
	call := <-client.Go(serviceMethod, args, reply, make(chan *TCall, 1)).Done
	return call.Error
}

// Go invokes the function asynchronously. It returns the Call structure representing
// the invocation. The done channel will signal when the call is complete by returning
// the same Call object. If done is nil, Go will allocate a new channel.
// If non-nil, done must be buffered or Go will deliberately crash.
func (client *TClient) Go(serviceMethod string, args interface{}, reply interface{}, done chan *TCall) *TCall {
	call := new(TCall)
	call.ServiceMethod = serviceMethod
	call.Path = serviceMethod
	call.Args = args
	call.Reply = reply
	if done == nil {
		done = make(chan *TCall, 10) // buffered.
	} else {
		// If caller passes done != nil, it must arrange that
		// done has enough buffer for the number of simultaneous
		// RPCs that will be using that channel. If the channel
		// is totally unbuffered, it's best not to run at all.
		if cap(done) == 0 {
			log.Panic("rpc: done channel is unbuffered")
		}
	}
	call.Done = done

	client.send(nil, call)
	return call
}

// 发送消息
func (client *TClient) send(ctx context.Context, call *TCall) {
	// Register this call.
	client.mutex.Lock()
	if client.shutdown || client.closing {
		call.Error = rpc.ErrShutdown
		client.mutex.Unlock()
		call.done()
		return
	}

	// 获得解码器
	log.Dbg("codec", client.option.SerializeType)
	codec := codec.Codecs[client.option.SerializeType]
	if codec == nil {
		call.Error = rpc.ErrUnsupportedCodec
		client.mutex.Unlock()
		call.done()
		return
	}

	if client.pending == nil {
		client.pending = make(map[uint64]*TCall)
	}

	seq := client.seq
	client.seq++
	client.pending[seq] = call
	client.mutex.Unlock()

	///if cseq, ok := ctx.Value(seqKey{}).(*uint64); ok {
	///	*cseq = seq
	///}

	//req := protocol.GetPooledMsg()
	req := client.msgPool.Get().(*message.TMessage) // request message
	req.SetMessageType(message.Request)
	req.SetSeq(seq)
	// heartbeat
	//if call.ServicePath == "" && call.ServiceMethod == "" {
	if call.Path == "" {
		req.SetHeartbeat(true)
	} else {
		req.SetSerializeType(client.option.SerializeType)
		if call.Metadata != nil {
			req.Metadata = call.Metadata
		}

		req.ServicePath = call.ServicePath     // 废弃
		req.ServiceMethod = call.ServiceMethod // 废弃
		req.Path = call.Path

		data, err := codec.Encode(call.Args)
		if err != nil {
			//log.Dbg("odec.Encode(call.Args)", err.Error())
			call.Error = err
			call.done()
			return
		}
		if len(data) > 1024 && client.option.CompressType == message.Gzip {
			data, err = message.Zip(data)
			if err != nil {
				call.Error = err
				call.done()
				return
			}

			req.SetCompressType(client.option.CompressType)
		}

		req.Payload = data
	}

	// 编码
	data := req.Encode()
	// 返回编译过的数据
	_, err := client.Conn.Write(data)
	if err != nil {
		client.mutex.Lock()
		call = client.pending[seq]
		delete(client.pending, seq)
		client.mutex.Unlock()
		if call != nil {
			call.Error = err
			call.done()
		}
	}

	//message.FreeMsg(req)

	if req.IsOneway() {
		client.mutex.Lock()
		call = client.pending[seq]
		delete(client.pending, seq)
		client.mutex.Unlock()
		if call != nil {
			call.done()
		}
	}

}
