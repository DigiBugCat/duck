//go:build darwin

package window

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

type webviewBackend struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	done   chan struct{}
	write  sync.Mutex
	cancel context.CancelFunc
	nextID uint64
	mu     sync.Mutex
	wait   map[string]chan snapshotReply
}

type snapshotReply struct {
	data []byte
	err  error
}

func launchWebviewBackend(parent context.Context, sink markSink) (browserBackend, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	runtimePath, err := writeWebviewRuntime(home)
	if err != nil {
		return nil, fmt.Errorf("duck window: writing WKWebView runtime: %w", err)
	}
	bundle, err := ensureNativeDuckWindowBundle(home)
	if err != nil {
		return nil, fmt.Errorf("duck window: preparing native WKWebView app: %w", err)
	}

	ctx, cancel := context.WithCancel(parent)
	exe := filepath.Join(bundle, "Contents", "MacOS", "duck-window")
	cmd := exec.CommandContext(ctx, exe, runtimePath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("duck window: start native app: %w", err)
	}

	b := &webviewBackend{
		cmd:    cmd,
		stdin:  stdin,
		done:   make(chan struct{}),
		cancel: cancel,
		wait:   make(map[string]chan snapshotReply),
	}
	ready := make(chan error, 1)
	go b.readEvents(stdout, sink, ready)
	go func() {
		_ = cmd.Wait()
		close(b.done)
	}()

	select {
	case err := <-ready:
		if err != nil {
			b.Close()
			return nil, err
		}
	case <-time.After(10 * time.Second):
		b.Close()
		return nil, fmt.Errorf("duck window: native app did not become ready")
	case <-ctx.Done():
		b.Close()
		return nil, ctx.Err()
	}
	return b, nil
}

func (b *webviewBackend) readEvents(stdout io.Reader, sink markSink, ready chan<- error) {
	sawReady := false
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		ev, err := DecodeWebviewEvent(sc.Bytes())
		if err != nil {
			continue
		}
		if ev.Ready && !sawReady {
			sawReady = true
			ready <- nil
			continue
		}
		if ev.Mark != nil {
			if m, err := DecodeWebviewMark(*ev.Mark); err == nil {
				go sink(m)
			}
		}
		if ev.ID != "" {
			b.resolveSnapshot(ev.ID, ev.Snapshot)
		}
	}
	if !sawReady {
		if err := sc.Err(); err != nil {
			ready <- fmt.Errorf("duck window: reading native app: %w", err)
		} else {
			ready <- fmt.Errorf("duck window: native app exited before ready")
		}
	}
}

func (b *webviewBackend) Navigate(url string) error {
	line, err := EncodeNavigateCommand(url)
	if err != nil {
		return err
	}
	return b.writeLine(line)
}

func (b *webviewBackend) Eval(js string) error {
	line, err := EncodeEvalCommand(js)
	if err != nil {
		return err
	}
	return b.writeLine(line)
}

func (b *webviewBackend) Snapshot(rect Rect) ([]byte, error) {
	id := fmt.Sprintf("snapshot-%d", atomic.AddUint64(&b.nextID, 1))
	ch := make(chan snapshotReply, 1)
	b.mu.Lock()
	b.wait[id] = ch
	b.mu.Unlock()
	line, err := EncodeSnapshotCommand(id, rect)
	if err != nil {
		b.dropSnapshot(id)
		return nil, err
	}
	if err := b.writeLine(line); err != nil {
		b.dropSnapshot(id)
		return nil, err
	}
	select {
	case reply := <-ch:
		return reply.data, reply.err
	case <-time.After(5 * time.Second):
		b.dropSnapshot(id)
		return nil, fmt.Errorf("duck window: native snapshot timed out")
	case <-b.done:
		return nil, fmt.Errorf("duck window: native app exited before snapshot")
	}
}

func (b *webviewBackend) resolveSnapshot(id string, encoded string) {
	b.mu.Lock()
	ch := b.wait[id]
	delete(b.wait, id)
	b.mu.Unlock()
	if ch == nil {
		return
	}
	if encoded == "" {
		ch <- snapshotReply{err: fmt.Errorf("duck window: native snapshot failed")}
		return
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	ch <- snapshotReply{data: data, err: err}
}

func (b *webviewBackend) dropSnapshot(id string) {
	b.mu.Lock()
	delete(b.wait, id)
	b.mu.Unlock()
}

func (b *webviewBackend) writeLine(line []byte) error {
	b.write.Lock()
	defer b.write.Unlock()
	_, err := b.stdin.Write(line)
	return err
}

func (b *webviewBackend) Close() {
	_ = b.stdin.Close()
	b.cancel()
	select {
	case <-b.done:
	case <-time.After(2 * time.Second):
		if b.cmd.Process != nil {
			_ = b.cmd.Process.Kill()
		}
		<-b.done
	}
}
