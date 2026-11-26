// Package agent supports Lacia in the local environment. It connects to
// a server when configured and helps deliver work to this device.
package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Forward opens a connection when the local environment is configured to
// reach its server through `kubectl port-forward`. It uses the existing
// credentials for the configured Kubernetes context.
type Forward struct {
	Kubectl    string
	Context    string
	Namespace  string
	Target     string // e.g. "deployment/lacia" or "service/lacia"
	LocalPort  int
	RemotePort int

	mu   sync.Mutex
	cmd  *exec.Cmd
	done chan struct{}
	last string // last line kubectl printed, for the log
}

// Start launches kubectl and waits until it reports that it is forwarding.
func (f *Forward) Start(ctx context.Context) error {
	// Refuse to talk to whatever else may already be listening on the port.
	probe, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(f.LocalPort))
	if err != nil {
		return fmt.Errorf("ポート %d はすでに使われています", f.LocalPort)
	}
	probe.Close()
	cmd := exec.Command(f.Kubectl, "--context", f.Context, "-n", f.Namespace, "port-forward", "--address", "127.0.0.1",
		f.Target, strconv.Itoa(f.LocalPort)+":"+strconv.Itoa(f.RemotePort))
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}
	ready := make(chan bool, 1)
	done := make(chan struct{})
	go func() {
		scanner := bufio.NewScanner(out)
		first := true
		for scanner.Scan() {
			line := scanner.Text()
			f.mu.Lock()
			f.last = line
			f.mu.Unlock()
			if first {
				first = false
				ready <- strings.HasPrefix(line, "Forwarding from 127.0.0.1:")
			}
		}
		if first {
			ready <- false
		}
		cmd.Wait()
		close(done)
	}()
	f.mu.Lock()
	f.cmd, f.done = cmd, done
	f.mu.Unlock()
	select {
	case ok := <-ready:
		if !ok {
			f.Stop()
			return errors.New("kubectl port-forward を始められませんでした: " + f.Last())
		}
		return nil
	case <-time.After(20 * time.Second):
		f.Stop()
		return errors.New("kubectl port-forward が20秒たっても始まりませんでした")
	case <-ctx.Done():
		f.Stop()
		return ctx.Err()
	}
}

// Alive reports whether kubectl is still running.
func (f *Forward) Alive() bool {
	f.mu.Lock()
	done := f.done
	f.mu.Unlock()
	if done == nil {
		return false
	}
	select {
	case <-done:
		return false
	default:
		return true
	}
}

// Last returns the last line kubectl printed.
func (f *Forward) Last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

// Stop ends kubectl and waits for it.
func (f *Forward) Stop() {
	f.mu.Lock()
	cmd, done := f.cmd, f.done
	f.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	cmd.Process.Kill()
	if done != nil {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
}

// URL is the local address of the forwarded server.
func (f *Forward) URL() string { return "http://127.0.0.1:" + strconv.Itoa(f.LocalPort) }
