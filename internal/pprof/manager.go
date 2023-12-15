package pprof

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"sync/atomic"
	"time"
)

type Manager struct {
	port *int64
}

func NewManager(startPort int64) Manager {
	return Manager{
		port: &startPort,
	}
}

func (v Manager) Persistent(source string, destPrefix string) error {
	cmd := exec.Command("go", "tool", "pprof", "-proto", "-output", destPrefix+"-"+time.Now().Format("2006-01-02_15:04:05")+".pb.gz", source)
	return cmd.Run()
}

func (v Manager) Proxy(timeout time.Duration, source string) (int64, error) {
	port := atomic.AddInt64(v.port, 1)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	cmd := exec.CommandContext(ctx, "go", "tool", "pprof", "-http", "localhost:"+strconv.FormatInt(port, 10), "-no_browser", source)
	if err := cmd.Start(); err != nil {
		cancel()
		return 0, err
	}
	go func() {
		defer cancel()
		var stdout bytes.Buffer
		cmd.Stdout = &stdout

		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Wait(); err != nil && err != ctx.Err() {
			fmt.Println(err, "stdout: ", stdout.String(), "stderr:", stderr.String())
		}
	}()

	return port, nil
}
