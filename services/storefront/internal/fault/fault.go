package fault

import "errors"

type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string             { return e.Message }
func New(status int, message string) error { return &Error{status, message} }
func Status(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 503
}
func Message(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Message
	}
	return "服务暂时不可用，请稍后重试。"
}
