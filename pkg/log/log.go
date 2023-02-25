// Package log implements the Logger interface. It's intended to be used together with
// a system logger that uses stdout and stderr of this process. It's up to the
// system logger to add timestamps and loglevel.
//
// The typical use-case is when logging docker plugin output and capturing it with
//
//	journalctl -fxu docker.service
package log

import (
	"errors"
	"fmt"
	"io"
	"os"
)

var nl = []byte{'\n'}
var debug = false

func SetDebug(flag bool) {
	debug = flag
}

func Error(v any) error {
	switch v := v.(type) {
	case nil:
		return nil
	case error:
		_, _ = fmt.Fprintln(os.Stderr, v.Error())
		return v
	case string:
		_, _ = fmt.Fprintln(os.Stderr, v)
		return errors.New(v)
	case fmt.Stringer:
		_, _ = fmt.Fprintln(os.Stderr, v)
		return errors.New(v.String())
	default:
		err := fmt.Errorf("%v", v)
		_, _ = fmt.Fprintln(os.Stderr, err.Error())
		return err
	}
}

func IsDebug() bool {
	return debug
}

func Errorf(format string, args ...any) error {
	return Error(fmt.Errorf(format, args...))
}

func Fatal(v any) {
	_ = Error(v)
	os.Exit(1)
}

func Info(v any) {
	_, _ = fmt.Fprintln(os.Stdout, v)
}

func Infof(format string, args ...any) {
	fprintfln(os.Stdout, format, args...)
}

func Debug(v any) {
	if debug {
		Info(v)
	}
}

func Debugf(format string, args ...any) {
	if debug {
		Infof(format, args...)
	}
}

func fprintfln(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
	_, _ = w.Write(nl)
}
