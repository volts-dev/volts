package errors

import (
	"encoding/json"
	stderrors "errors"
	"fmt"
	"net/http"
)

// 标准库的四个组合子在这里原样转出。
//
// 本包叫 errors，且被全仓当作 errors 直接 import（vectors 那边 191 个文件）——
// 一旦某个文件既要 New 一个带码的错误、又要 errors.Is 判一次哨兵，就得给其中一个
// 起别名。结果是同一个仓里两种写法并存，而"这一行的 errors 是哪个包"要回头看
// import 才知道。转出来之后不必再二选一。
var (
	// Is 报告 err 的链上是否有与 target 匹配的错误。见 (*Error).Is：本包的 Error
	// 按 Id+Code+Detail 相等判定，因此经 JSON 往返回来的哨兵仍然认得出。
	Is = stderrors.Is
	// As 在 err 的链上找第一个可赋给 target 的错误。
	As = stderrors.As
	// Join 把多个错误并成一个（任一匹配即 Is 为真）。
	Join = stderrors.Join
	// Unwrap 剥一层。
	Unwrap = stderrors.Unwrap
)

// ErrShutdown connection is closed.
var (
	Shutdown           = New("", 0, "connection is shut down")
	PermissionDenied   = New("", 500, "permission denied.")
	UnavailableService = New("", 500, "Unavailable Service")
)

type (
	Error struct {
		Id     string `protobuf:"bytes,1,opt,name=id,proto3" json:"id,omitempty"`
		Code   int    `protobuf:"varint,2,opt,name=code,proto3" json:"code,omitempty"`
		Detail string `protobuf:"bytes,3,opt,name=detail,proto3" json:"detail,omitempty"`
		Status string `protobuf:"bytes,4,opt,name=status,proto3" json:"status,omitempty"`

		// cause 是被本错误包住的下层错误，不导出也不上线（JSON/protobuf 都看不见）——
		// 它只在**本进程内**供 errors.Is/As 穿透，跨进程传的仍然只有上面四个字段。
		cause error
	}
)

func (self Error) Error() string {
	return self.Detail
}

// Unwrap 让 errors.Is / errors.As 能穿过本错误看到底层原因。
//
// 没有它的时候，`Wrap` 出来的错误对 `errors.Is(err, 某哨兵)` 一律是假 —— 而这正是
// "连不上"之类的判定所依赖的。
func (self *Error) Unwrap() error {
	if self == nil {
		return nil
	}
	return self.cause
}

// Is 让两个**字段相同**的 Error 相等，而不是只有同一个指针才相等。
//
// 本包的哨兵（Shutdown 等）是包级变量，`err == errors.Shutdown` 只在同一个指针时
// 成立；而错误一旦经 RPC 以 JSON 往返，回来的是一个**新对象** —— 判等永远为假，
// 调用方于是走进"未知错误"分支。按 Id+Code+Detail 判定后，往返回来的哨兵仍然认得出。
//
// 三个字段都要比：Shutdown 之外的两个内置哨兵 Id 同为 ""、Code 同为 500，只差 Detail
// —— 少比一个字段，PermissionDenied 与 UnavailableService 就成了同一个错误。
func (self *Error) Is(target error) bool {
	t, ok := target.(*Error)
	if !ok || self == nil || t == nil {
		return false
	}
	return self.Id == t.Id && self.Code == t.Code && self.Detail == t.Detail
}

// Wrap 用一个带码的 Error 包住 err，原因链保留（errors.Is/As 可穿透）。
//
// detail 为空时沿用 err 自己的文案 —— 包一层不该把原来的话吞掉。
func Wrap(err error, id string, code int, detail string) error {
	if detail == "" && err != nil {
		detail = err.Error()
	}
	return &Error{
		Id:     id,
		Code:   code,
		Detail: detail,
		Status: http.StatusText(code),
		cause:  err,
	}
}

// From 在 err 的链上取第一个 *Error；没有则返回 nil。
func From(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if stderrors.As(err, &e) {
		return e
	}
	return nil
}

// Code 取 err 链上第一个 *Error 的状态码；没有则返回 0。
//
// **必须走链，不能用类型断言。** 业务代码普遍会 `fmt.Errorf("...: %w", err)` 再往上
// 抛，断言 `err.(*Error)` 一遇到包装就落空 —— 上层拿不到码，只好一律当 500。真栈
// 上撞过：etcd 超时返回的是裸 context.DeadlineExceeded，带不出码，于是一次注册中心
// 抖动在前端表现为 500 而不是 503。
func Code(err error) int {
	if e := From(err); e != nil {
		return e.Code
	}
	return 0
}

// New generates a custom error.
func New(id string, code int, detail string) error {
	return &Error{
		Id:     id,
		Code:   code,
		Detail: detail,
		Status: http.StatusText(int(code)),
	}
}

// Parse tries to parse a JSON string into an error. If that
// fails, it will set the given string as the error detail.
func Parse(err string) *Error {
	e := new(Error)
	jsonErr := json.Unmarshal([]byte(err), e)
	if jsonErr != nil {
		e.Detail = err
	}
	return e
}

// BadRequest generates a 400 error.
func BadRequest(id, msg string) error {
	return &Error{
		Id:     id,
		Code:   400,
		Detail: msg,
		Status: http.StatusText(400),
	}
}

// Unauthorized generates a 401 error.
func Unauthorized(id string, msg ...any) error {
	return &Error{
		Id:     id,
		Code:   401,
		Detail: fmt.Sprintf("%s Session Expired %v", id, msg),
		Status: http.StatusText(401),
	}
}

// Forbidden generates a 403 error.
func Forbidden(id string, msg ...any) error {
	return &Error{
		Id:     id,
		Code:   403,
		Detail: fmt.Sprintf("%s Forbidden %v", id, msg),
		Status: http.StatusText(403),
	}
}

// NotFound generates a 404 error.
func NotFound(id, msg string) error {
	return &Error{
		Id:     id,
		Code:   404,
		Detail: msg,
		Status: http.StatusText(404),
	}
}

// MethodNotAllowed generates a 405 error.
func MethodNotAllowed(id, msg string) error {
	return &Error{
		Id:     id,
		Code:   405,
		Detail: msg,
		Status: http.StatusText(405),
	}
}

// Timeout generates a 408 error.
func Timeout(id, msg string) error {
	return &Error{
		Id:     id,
		Code:   408,
		Detail: msg,
		Status: http.StatusText(408),
	}
}

// Conflict generates a 409 error.
func Conflict(id, msg string) error {
	return &Error{
		Id:     id,
		Code:   409,
		Detail: msg,
		Status: http.StatusText(409),
	}
}

// InternalServerError generates a 500 error.
func InternalServerError(id string, msg ...any) error {
	return &Error{
		Id:     id,
		Code:   500,
		Detail: fmt.Sprintf("%s Internal Server Error %v", id, msg),
		Status: http.StatusText(500),
	}
}

func UnsupportedCodec(id string, a ...interface{}) error {
	return &Error{
		Id:     id,
		Code:   500,
		Detail: fmt.Sprintf("%s unsupported codec %v", id, a),
		Status: http.StatusText(500),
	}
}
