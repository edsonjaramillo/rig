package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ErrNotFound reports that a requested executable or filesystem path does not exist.
var ErrNotFound = errors.New("not found")

// Host is the single boundary for effects on the machine running Rig.
type Host interface {
	Platform(context.Context) (Platform, error)
	User(context.Context) (User, error)
	IsTerminal() bool
	LookPath(string) (string, error)
	InspectPath(string) (PathInfo, error)
	Retrieve(context.Context, string) (HTTPResponse, error)
	CreateTemp(string) (TemporaryArtifact, error)
	ValidateSudo(context.Context) error
	Run(context.Context, Command) error
	Bootstrap(context.Context, InstallTarget, Streams) error
}

// PathInfo describes relevant filesystem state without following absent symlink targets.
type PathInfo struct {
	Exists     bool
	Executable bool
}

// Platform describes operating-system facts needed by installation preflight.
type Platform struct {
	OS           string
	Distribution string
	Architecture string
	Version      string
}

// User describes the current host user.
type User struct {
	UID int
}

// Streams are the process streams exposed to a command or bootstrap operation.
type Streams struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Command describes a child process crossing the host boundary.
type Command struct {
	Path        string
	Args        []string
	Env         []string
	Dir         string
	Interactive bool
	Streams
}

// HTTPResponse is the host-provided result of an HTTP retrieval.
type HTTPResponse struct {
	StatusCode int
	FinalURL   string
	Body       io.ReadCloser
}

// TemporaryArtifact is a securely created temporary file and its cleanup operation.
type TemporaryArtifact struct {
	Name   string
	File   io.ReadWriteCloser
	Remove func() error
}

// OSHost implements Host using the local operating system.
type OSHost struct {
	streams Streams
	client  *http.Client
}

// NewOSHost creates the production host boundary.
func NewOSHost(streams Streams) *OSHost {
	return &OSHost{
		streams: streams,
		client: &http.Client{
			CheckRedirect: func(request *http.Request, _ []*http.Request) error {
				return requireHTTPS(request.URL)
			},
		},
	}
}

// Platform returns local operating-system facts.
func (h *OSHost) Platform(ctx context.Context) (Platform, error) {
	platform := Platform{OS: runtime.GOOS, Architecture: runtime.GOARCH}
	switch runtime.GOOS {
	case "darwin":
		output, err := exec.CommandContext(ctx, "/usr/bin/sw_vers", "-productVersion").Output()
		if err != nil {
			return Platform{}, fmt.Errorf("read macOS version: %w", err)
		}
		platform.Version = strings.TrimSpace(string(output))
	case "linux":
		contents, err := os.ReadFile("/etc/os-release")
		if err != nil {
			return Platform{}, fmt.Errorf("read operating-system release: %w", err)
		}
		values := parseOSRelease(string(contents))
		platform.Distribution = values["ID"]
		platform.Version = values["VERSION_ID"]
	}
	return platform, nil
}

func parseOSRelease(contents string) map[string]string {
	values := make(map[string]string)
	for _, line := range strings.Split(contents, "\n") {
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		values[key] = strings.Trim(strings.TrimSpace(value), "\"'")
	}
	return values
}

// User returns the current local user.
func (h *OSHost) User(context.Context) (User, error) {
	current, err := user.Current()
	if err != nil {
		return User{}, fmt.Errorf("inspect current user: %w", err)
	}
	uid, err := strconv.Atoi(current.Uid)
	if err != nil {
		return User{}, fmt.Errorf("parse current user ID: %w", err)
	}
	return User{UID: uid}, nil
}

// IsTerminal reports whether Rig's standard input is a terminal-like character device.
func (h *OSHost) IsTerminal() bool {
	file, ok := h.streams.Stdin.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// LookPath searches PATH for an executable.
func (h *OSHost) LookPath(name string) (string, error) {
	path, err := exec.LookPath(name)
	if errors.Is(err, exec.ErrNotFound) {
		return "", ErrNotFound
	}
	return path, err
}

// InspectPath reports whether a filesystem path exists and resolves to an executable file.
func (h *OSHost) InspectPath(path string) (PathInfo, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return PathInfo{}, nil
	}
	if err != nil {
		return PathInfo{}, err
	}

	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return PathInfo{Exists: true}, nil
	}
	if err != nil {
		return PathInfo{}, err
	}
	return PathInfo{
		Exists:     true,
		Executable: !info.IsDir() && info.Mode().Perm()&0o111 != 0,
	}, nil
}

// Retrieve performs an HTTP GET through the production HTTP client.
func (h *OSHost) Retrieve(ctx context.Context, address string) (HTTPResponse, error) {
	parsed, err := url.Parse(address)
	if err != nil {
		return HTTPResponse{}, err
	}
	if err := requireHTTPS(parsed); err != nil {
		return HTTPResponse{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return HTTPResponse{}, err
	}
	response, err := h.client.Do(request)
	if err != nil {
		return HTTPResponse{}, err
	}
	finalURL := response.Request.URL
	if err := requireHTTPS(finalURL); err != nil {
		_ = response.Body.Close()
		return HTTPResponse{}, err
	}
	return HTTPResponse{
		StatusCode: response.StatusCode,
		FinalURL:   finalURL.String(),
		Body:       response.Body,
	}, nil
}

func requireHTTPS(address *url.URL) error {
	if address == nil || !strings.EqualFold(address.Scheme, "https") {
		return errors.New("installer retrieval requires HTTPS")
	}
	return nil
}

// CreateTemp creates a securely permissioned temporary file.
func (h *OSHost) CreateTemp(pattern string) (TemporaryArtifact, error) {
	file, err := os.CreateTemp("", pattern)
	if err != nil {
		return TemporaryArtifact{}, err
	}
	return TemporaryArtifact{
		Name: file.Name(),
		File: file,
		Remove: func() error {
			return os.Remove(file.Name())
		},
	}, nil
}

// ValidateSudo asks sudo to validate the current user's credentials.
func (h *OSHost) ValidateSudo(ctx context.Context) error {
	return h.Run(ctx, Command{Path: "sudo", Args: []string{"-v"}, Streams: h.streams})
}

// Run executes a child process. Interactive children remain in Rig's foreground
// process group so the terminal delivers Ctrl-C to the installer and its children.
func (h *OSHost) Run(ctx context.Context, command Command) error {
	if !command.Interactive {
		child := exec.CommandContext(ctx, command.Path, command.Args...)
		configureChild(child, command)
		return child.Run()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	child := exec.Command(command.Path, command.Args...)
	configureChild(child, command)
	if err := child.Start(); err != nil {
		return err
	}

	completed := make(chan error, 1)
	go func() {
		completed <- child.Wait()
	}()

	select {
	case err := <-completed:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	case <-ctx.Done():
		// A terminal interrupt already reached the whole foreground process group.
		// Signalling the installer directly also handles programmatic cancellation.
		_ = child.Process.Signal(os.Interrupt)
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		select {
		case <-completed:
		case <-timer.C:
			_ = child.Process.Kill()
			<-completed
		}
		return ctx.Err()
	}
}

func configureChild(child *exec.Cmd, command Command) {
	child.Env = command.Env
	child.Dir = command.Dir
	child.Stdin = command.Stdin
	child.Stdout = command.Stdout
	child.Stderr = command.Stderr
}

// Bootstrap is the extension point for package-manager installation slices.
func (h *OSHost) Bootstrap(_ context.Context, target InstallTarget, _ Streams) error {
	return fmt.Errorf("%s bootstrap is not implemented", target)
}
