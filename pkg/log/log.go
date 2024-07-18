// Package log implements the Logger interface. It's intended to be used together with
// a system logger that uses stdout and stderr of this process. It's up to the
// system logger to add timestamps and loglevel.
//
// The typical use-case is when logging docker plugin output and capturing it with
//
//	journalctl -fxu docker.service
package log

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

var msgChan = make(chan string, 10)
var debug = false

func Start(doneCh <-chan struct{}) {
	// This code is designed to send the log as a serialized stream of batches that are at least
	// 50 ms apart.
	// For reasons unknown, logging too quickly will result in lost log output when capturing
	// the output under /run/docker/plugins.
	w := os.Stderr
	var buf bytes.Buffer
	bufLock := sync.Mutex{}
	fire := time.AfterFunc(time.Duration(math.MaxInt64), func() {
		bufLock.Lock()
		if buf.Len() > 0 {
			_, _ = w.Write(buf.Bytes())
			buf.Reset()
		}
		bufLock.Unlock()
	})

	go func() {
		for {
			select {
			case msg := <-msgChan:
				bufLock.Lock()
				buf.WriteString(msg)
				buf.WriteByte('\n')
				bufLock.Unlock()
				fire.Reset(50 * time.Millisecond)
			case <-doneCh:
				fire.Stop()
				// Output any remaining logs.
				// No need for locks now. The timer is dead.
				for len(msgChan) > 0 {
					buf.WriteString(<-msgChan)
					buf.WriteByte('\n')
				}
				if buf.Len() > 0 {
					_, _ = w.Write(buf.Bytes())
				}
				return
			}
		}
	}()
}

type logWriter string

func (w logWriter) Write(p []byte) (n int, err error) {
	ps := string(p)
	if len(ps) > 0 {
		for _, line := range strings.Split(string(p), "\n") {
			if len(line) > 0 {
				tsPrintln(string(w), line)
			}
		}
	}
	return len(p), nil
}

func Stdlog(level string) io.Writer {
	return logWriter(level)
}

func SetDebug(flag bool) {
	debug = flag
}

func IsDebug() bool {
	return debug
}

func Fatal(args ...any) {
	Error(args...)
	os.Exit(1)
}

func Error(args ...any) {
	tsPrintln("error", args...)
}

func Errorf(format string, args ...any) {
	tsPrintf("error", format, args...)
}

func Info(args ...any) {
	tsPrintln("info", args...)
}

func Infof(format string, args ...any) {
	tsPrintf("info", format, args...)
}

func Debug(args ...any) {
	if debug {
		tsPrintln("debug", args...)
	}
}

func Debugf(format string, args ...any) {
	if debug {
		tsPrintf("debug", format, args...)
	}
}

func tsPrintf(level string, format string, args ...any) {
	msgChan <- fmt.Sprintf("%s %-6s "+format, slices.Insert(args, 0, any(time.Now().Format("15:04:05.0000")), any(level))...)
}

func tsPrintln(level string, args ...any) {
	msgChan <- fmt.Sprint(slices.Insert(args, 0, any(fmt.Sprintf("%s %-6s", time.Now().Format("15:04:05.0000"), level)))...)
}
