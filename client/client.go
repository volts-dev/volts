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

	rpc "github.com/volts-dev/volts"
	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/protocol"

	log "github.com/volts-dev/logger"
)

const (
	XVersion           = "X-RPC-Version"
	XMessageType       = "X-RPC-MesssageType"
	XHeartbeat         = "X-RPC-Heartbeat"
	XOneway            = "X-RPC-Oneway"
	XMessageStatusType = "X-RPC-MessageStatusType"
	XSerializeType     = "X-RPC-SerializeType"
	XMessageID         = "X-RPC-MessageID"
	XServicePath       = "X-RPC-ServicePath"
	XServiceMethod     = "X-RPC-ServiceMethod"
	XMeta              = "X-RPC-Meta"
	XErrorMessage      = "X-RPC-ErrorMessage"
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

		seq      uint64            // Call任务列队序号
		pending  map[uint64]*TCall // Call任务列队
		closing  bool              // user has called Close
		shutdown bool              // server has told us to stop

		r                 *bufio.Reader
		ServerMessageChan chan<- *protocol.TMessage
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
func convertRes2Raw(res *protocol.TMessage) (map[string]string, []byte, error) {
	m := make(map[string]string)
	m[XVersion] = strconv.Itoa(int(res.Version()))
	if res.IsHeartbeat() {
		m[XHeartbeat] = "true"
	}
	if res.IsOneway() {
		m[XOneway] = "true"
	}
	if res.MessageStatusType() == protocol.Error {
		m[XMessageStatusType] = "Error"
	} else {
		m[XMessageStatusType] = "Normal"
	}

	if res.CompressType() == protocol.Gzip {
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

	return cli
}

// Connect connects the server via specified network.
func (self *TClient) Connect(network, address string) error {
	var conn net.Conn
	var err error

	switch network {
	case "http":
		//conn, err = newDirectHTTPConn(self, network, address)
	case "kcp":
		//conn, err = newDirectKCPConn(self, network, address)
	case "quic":
		//conn, err = newDirectQuicConn(self, network, address)
	case "unix":
		//conn, err = newDirectConn(self, network, address)
	default:
		conn, err = newDirectConn(self, network, address)
	}

	if err == nil && conn != nil {
		if self.option.ReadTimeout != 0 {
			conn.SetReadDeadline(time.Now().Add(self.option.ReadTimeout))
		}
		if self.option.WriteTimeout != 0 {
			conn.SetWriteDeadline(time.Now().Add(self.option.WriteTimeout))
		}

		self.Conn = conn

		self.r = bufio.NewReaderSize(conn, ReaderBuffsize)
		log.Dbg("Connect", self.Conn.RemoteAddr(), self.r.Buffered())
		log.Dbg("NewCli", self.Conn.LocalAddr())
		// start reading and writing since connected
		go self.input()

		if self.option.Heartbeat && self.option.HeartbeatInterval > 0 {
			go self.heartbeat()
		}

	}

	return err
}

func (client *TClient) handleServerRequest(msg *protocol.TMessage) {
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
	//var msg = protocol.NewMessage()
	msg := protocol.GetMessageFromPool()

	for err == nil {
		if client.option.ReadTimeout != 0 {
			client.Conn.SetReadDeadline(time.Now().Add(client.option.ReadTimeout))
		}
		log.Dbg("input", client.r.Size())
		//time.Sleep(5 * time.Second)
		//buf := make([]byte, 6500)
		//cnt, err := client.r.Read(buf)
		//log.Dbg("buf", buf, cnt, err)

		// 从Reader解码到Msg
		err = msg.Decode(client.r)
		log.Dbg("input1", client.r.Size())
		//msg, err = protocol.Read(client.r)
		if err != nil {
			log.Dbg("1", err.Error())
			break
		}

		seq := msg.Seq()
		var call *TCall
		isServerMessage := (msg.MessageType() == protocol.Request && !msg.IsHeartbeat() && msg.IsOneway())
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
					go client.handleServerRequest(msg)
					msg = protocol.NewMessage()
				}
				continue
			}
		case msg.MessageStatusType() == protocol.Error:
			// We've got an error response. Give this to the request;
			call.Error = ServiceError(msg.Metadata[protocol.ServiceError])
			call.ResMetadata = msg.Metadata

			if call.Raw {
				call.Metadata, call.Reply, _ = convertRes2Raw(msg)
				call.Metadata[XErrorMessage] = call.Error.Error()
			}
			call.done()
		default:
			if call.Raw {
				call.Metadata, call.Reply, _ = convertRes2Raw(msg)
			} else {
				data := msg.Payload
				if len(data) > 0 {
					codec := codec.Codecs[msg.SerializeType()]
					if codec == nil {
						call.Error = ServiceError(ErrUnsupportedCodec.Error())
					} else {
						// 解码内容
						err = codec.Decode(data, call.Reply)
						if err != nil {
							//log.Dbg("2", err.Error())
							call.Error = ServiceError(err.Error())
						}
					}
				}
				call.ResMetadata = msg.Metadata
			}

			call.done()
		}

		msg.Reset()
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
		log.Err("rpc: client protocol error:", err)
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
	var err error
	Done := client.Go(serviceMethod, args, reply, make(chan *TCall, 1)).Done

	select {
	/*
		case <-ctx.Done(): //cancel by context
			client.mutex.Lock()
			call := client.pending[*seq]
			delete(client.pending, *seq)
			client.mutex.Unlock()
			if call != nil {
				call.Error = ctx.Err()
				call.done()
			}

			return ctx.Err()
	*/
	case call := <-Done:
		err = call.Error
		/*	meta := ctx.Value(share.ResMetaDataKey)
			if meta != nil && len(call.ResMetadata) > 0 {
				resMeta := meta.(map[string]string)
				for k, v := range call.ResMetadata {
					resMeta[k] = v
				}
			}*/
	}
	return err
}

// Go invokes the function asynchronously. It returns the Call structure representing
// the invocation. The done channel will signal when the call is complete by returning
// the same Call object. If done is nil, Go will allocate a new channel.
// If non-nil, done must be buffered or Go will deliberately crash.
func (client *TClient) Go(path string, args interface{}, reply interface{}, done chan *TCall) *TCall {
	// TODO 缓存
	call := new(TCall)
	call.ServiceMethod = path // 废弃
	call.Path = path
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
			panic("rpc client: go() channel is unbuffered")
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
	//log.Dbg("codec", client.option.SerializeType)
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

	// TODO  服务器和客户端共享使用一个msgPool缓冲池
	//req := protocol.GetPooledMsg()
	req := protocol.GetMessageFromPool() // request protocol
	req.SetMessageType(protocol.Request)
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
			log.Dbg("odec.Encode(call.Args)", err.Error())
			call.Error = err
			call.done()
			return
		}
		if len(data) > 1024 && client.option.CompressType == protocol.Gzip {
			data, err = protocol.Zip(data)
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
			log.Dbg("asdfa", err.Error())
			call.Error = err
			call.done()
		}
	}

	//protocol.FreeMsg(req)

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
