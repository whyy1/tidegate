package controller

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"

	"tidegate/internal/agent"
	"tidegate/internal/engine"
)

type localSupervisor struct {
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func startLocalAgentSupervisor(
	parent context.Context,
	joinAddr string,
	token string,
	version string,
	inProcess bool,
	exePath string,
	client engine.ClientKind,
	log *slog.Logger,
) (*localSupervisor, error) {
	ctx, cancel := context.WithCancel(parent)
	sup := &localSupervisor{cancel: cancel}

	if inProcess {
		ag, err := agent.New(agent.Options{
			JoinAddr: joinAddr,
			Token:    token,
			Name:     "local",
			Version:  version,
			Client:   client,
			Logger:   log,
		})
		if err != nil {
			cancel()
			return nil, err
		}
		sup.wg.Add(1)
		go func() {
			defer sup.wg.Done()
			_ = ag.Run(ctx)
		}()
		return sup, nil
	}

	if exePath == "" {
		var err error
		exePath, err = os.Executable()
		if err != nil {
			cancel()
			return nil, err
		}
	}

	sup.wg.Add(1)
	go func() {
		defer sup.wg.Done()
		backoff := 200 * time.Millisecond
		for {
			if ctx.Err() != nil {
				return
			}
			started := time.Now()
			err := runLocalSubprocess(ctx, exePath, joinAddr, token, client, log)
			if ctx.Err() != nil {
				return
			}
			log.Warn("本机发压节点进程退出，即将重启", "err", err, "backoff", backoff)
			if time.Since(started) > 10*time.Second {
				backoff = 200 * time.Millisecond
			}
			t := time.NewTimer(backoff)
			select {
			case <-t.C:
				backoff = min(backoff*2, 5*time.Second)
			case <-ctx.Done():
				t.Stop()
				return
			}
		}
	}()
	return sup, nil
}

func (s *localSupervisor) close() {
	if s == nil {
		return
	}
	s.cancel()
	s.wg.Wait()
}

func runLocalSubprocess(
	ctx context.Context,
	exePath string,
	joinAddr string,
	token string,
	client engine.ClientKind,
	log *slog.Logger,
) error {
	args := []string{"agent", "--join", joinAddr, "--token", token, "--name", "local"}
	if client != "" {
		args = append(args, "--client", string(client))
	}
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd := exec.Command(exePath, args...)
	cmd.Stdin = stdinR
	cmd.Env = append(os.Environ(), "TIDEGATE_WATCH_STDIN=1")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		return err
	}
	_ = stdinR.Close()
	defer stdinW.Close()

	var pipeWG sync.WaitGroup
	pipeWG.Add(2)
	drain := func(r io.Reader) {
		defer pipeWG.Done()
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			log.Info("[local-agent] " + sc.Text())
		}
	}
	go drain(stdout)
	go drain(stderr)

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	var waitErr error
	select {
	case waitErr = <-waitCh:
	case <-ctx.Done():
		_ = stdinW.Close()
		select {
		case waitErr = <-waitCh:
		case <-time.After(500 * time.Millisecond):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			waitErr = <-waitCh
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			waitErr = ctx.Err()
		}
	}
	pipeWG.Wait()
	return waitErr
}
