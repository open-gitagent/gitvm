package agent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// RunCommand executes a shell command and returns the result.
func RunCommand(req ExecRequest, defaults *Defaults) (*ExecResult, error) {
	cwd := req.Cwd
	if cwd == "" {
		cwd = defaults.WorkDir
	}

	// Fall back to current directory if workdir doesn't exist
	if !dirExists(cwd) {
		cwd, _ = os.Getwd()
	}

	// Merge environment: defaults + request overrides
	env := mergeEnv(defaults.EnvVars, req.Env)

	var ctx context.Context
	var cancel context.CancelFunc
	if req.Timeout > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), time.Duration(req.Timeout)*time.Second)
	} else {
		ctx, cancel = context.WithCancel(context.Background())
	}
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", req.Command)
	cmd.Dir = cwd
	cmd.Env = env

	// Run as specified user if set
	if defaults.User != "" && defaults.User != "root" {
		if err := setUser(cmd, defaults.User); err != nil {
			return nil, fmt.Errorf("set user %q: %w", defaults.User, err)
		}
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else if ctx.Err() == context.DeadlineExceeded {
			return &ExecResult{
				ExitCode: 124, // timeout convention
				Stdout:   stdout.String(),
				Stderr:   stderr.String() + "\ngitvm-agent: command timed out",
			}, nil
		} else {
			return nil, fmt.Errorf("exec: %w", err)
		}
	}

	return &ExecResult{
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	}, nil
}

// StreamCommand executes a command and calls onData with output chunks.
// It returns the exit code when the command completes.
func StreamCommand(req ExecRequest, defaults *Defaults, onData func(stream string, data string)) (int, error) {
	cwd := req.Cwd
	if cwd == "" {
		cwd = defaults.WorkDir
	}

	// Fall back to current directory if workdir doesn't exist
	if !dirExists(cwd) {
		cwd, _ = os.Getwd()
	}

	env := mergeEnv(defaults.EnvVars, req.Env)

	var ctx context.Context
	var cancel context.CancelFunc
	if req.Timeout > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), time.Duration(req.Timeout)*time.Second)
	} else {
		ctx, cancel = context.WithCancel(context.Background())
	}
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", req.Command)
	cmd.Dir = cwd
	cmd.Env = env

	if defaults.User != "" && defaults.User != "root" {
		if err := setUser(cmd, defaults.User); err != nil {
			return -1, fmt.Errorf("set user %q: %w", defaults.User, err)
		}
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return -1, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return -1, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return -1, fmt.Errorf("start: %w", err)
	}

	done := make(chan struct{})
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stdoutPipe.Read(buf)
			if n > 0 {
				onData("stdout", string(buf[:n]))
			}
			if err != nil {
				break
			}
		}
		done <- struct{}{}
	}()

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stderrPipe.Read(buf)
			if n > 0 {
				onData("stderr", string(buf[:n]))
			}
			if err != nil {
				break
			}
		}
		done <- struct{}{}
	}()

	// Wait for both readers
	<-done
	<-done

	exitCode := 0
	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return -1, fmt.Errorf("wait: %w", err)
		}
	}

	return exitCode, nil
}

func mergeEnv(base map[string]string, overrides map[string]string) []string {
	merged := make(map[string]string)
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range overrides {
		merged[k] = v
	}

	// Ensure PATH exists
	if _, ok := merged["PATH"]; !ok {
		merged["PATH"] = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}

	env := make([]string, 0, len(merged))
	for k, v := range merged {
		env = append(env, k+"="+v)
	}
	return env
}

func setUser(cmd *exec.Cmd, username string) error {
	u, err := user.Lookup(username)
	if err != nil {
		// Try as numeric UID
		if strings.HasPrefix(username, "#") {
			return nil // skip
		}
		return err
	}

	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{
			Uid: uint32(uid),
			Gid: uint32(gid),
		},
	}
	return nil
}
