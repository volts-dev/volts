package client

import (
	"context"
	"errors"
	"io"
	"sync"

	verrors "github.com/volts-dev/volts/internal/errors"

	"github.com/volts-dev/volts/transport"
)

var (
	errShutdown = errors.New("connection is shut down")
)

// Implements the streamer interface.
type rpcStream struct {
	err      error
	request  IRequest
	response *rpcResponse
	//codec    codec.ICodec
	context context.Context
	conn    transport.ISocket
	closed  chan bool

	// release releases the connection back to the pool
	release func(err error)
	id      string
	sync.RWMutex
	// Indicates whether connection should be closed directly.
	close bool

	// signal whether we should send EOS
	sendEOS bool
}

func (r *rpcStream) isClosed() bool {
	select {
	case <-r.closed:
		return true
	default:
		return false
	}
}

func (r *rpcStream) Context() context.Context {
	return r.context
}

func (r *rpcStream) Request() IRequest {
	return r.request
}

func (r *rpcStream) Response() *rpcResponse {
	return r.response
}

func (r *rpcStream) Send(msg *transport.Message) error {
	r.Lock()
	defer r.Unlock()

	if r.isClosed() {
		r.err = errShutdown
		return errShutdown
	}

	/*req := codec.Message{
		Id:       r.id,
		Target:   r.request.Service(),
		Method:   r.request.Method(),
		Endpoint: r.request.Endpoint(),
		Type:     codec.Request,
	}*/

	if err := r.conn.Send(msg); err != nil {
		r.err = err
		return err
	}

	return nil
}

func (r *rpcStream) Recv(msg *transport.Message) error {
	r.Lock()

	if r.isClosed() {
		r.err = errShutdown
		r.Unlock()

		return errShutdown
	}

	//var resp codec.Message

	r.Unlock()
	//err := r.codec.ReadHeader(&resp, codec.Response)
	err := r.conn.Recv(msg)
	r.Lock()

	if err != nil {
		if errors.Is(err, io.EOF) && !r.isClosed() {
			r.err = io.ErrUnexpectedEOF
			r.Unlock()

			return io.ErrUnexpectedEOF
		}

		r.err = err

		r.Unlock()

		return err
	}

	switch msg.MessageStatusType() {
	case transport.StatusOK:
		break
	case transport.StatusError:
		r.err = verrors.New("StatusError", int32(transport.StatusError), string(msg.Payload))
	default:
		r.err = verrors.New("", int32(msg.MessageStatusType()), string(msg.Payload))
	}
	/*
		switch {
		case len(resp.Error) > 0:
			// We've got an error response. Give this to the request;
			// any subsequent requests will get the ReadResponseBody
			// error if there is one.
			if resp.Error != lastStreamResponseError {
				r.err = serverError(resp.Error)
			} else {
				r.err = io.EOF
			}
			r.Unlock()
			err = r.codec.ReadBody(nil)
			r.Lock()
			if err != nil {
				r.err = err
			}
		default:
			r.Unlock()
			err = r.codec.ReadBody(msg)
			r.Lock()
			if err != nil {
				r.err = err
			}
		}
	*/
	defer r.Unlock()

	return r.err
}

func (r *rpcStream) Error() error {
	r.RLock()
	defer r.RUnlock()

	return r.err
}

func (r *rpcStream) CloseSend() error {
	return errors.New("streamer not implemented")
}

func (r *rpcStream) Close() error {
	r.Lock()

	select {
	case <-r.closed:
		r.Unlock()
		return nil
	default:
		close(r.closed)
		r.Unlock()

		// send the end of stream message
		if r.sendEOS {
			// no need to check for error
			//nolint:errcheck,gosec
			/*
				r.codec.Write(&codec.Message{
					Id:       r.id,
					Target:   r.request.Service(),
					Method:   r.request.Method(),
					Endpoint: r.request.Endpoint(),
					Type:     codec.Error,
					Error:    lastStreamResponseError,
				}, nil)*/
		}

		//err := r.codec.Close()
		err := r.conn.Close()
		rerr := r.Error()
		if r.close && rerr == nil {
			rerr = errors.New("connection header set to close")
		}
		// release the connection
		r.release(rerr)

		return err
	}
}
