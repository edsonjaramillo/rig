package cmd

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

type fakeHost struct {
	platform       Platform
	platformError  error
	user           User
	userError      error
	terminal       bool
	lookPaths      map[string]string
	lookPathErrors map[string]error
	pathInfo       map[string]PathInfo
	pathErrors     map[string]error
	response       HTTPResponse
	retrieveError  error
	artifact       TemporaryArtifact
	createTempErr  error
	sudoError      error
	runErrors      map[string]error
	runHook        func(Command)
	calls          []string
	bootstrapped   []InstallTarget
	bootstrapError error
}

func (h *fakeHost) Platform(context.Context) (Platform, error) {
	h.calls = append(h.calls, "platform")
	return h.platform, h.platformError
}

func (h *fakeHost) User(context.Context) (User, error) {
	h.calls = append(h.calls, "user")
	return h.user, h.userError
}

func (h *fakeHost) IsTerminal() bool {
	h.calls = append(h.calls, "terminal")
	return h.terminal
}

func (h *fakeHost) Retrieve(_ context.Context, address string) (HTTPResponse, error) {
	h.calls = append(h.calls, "retrieve:"+address)
	return h.response, h.retrieveError
}

func (h *fakeHost) CreateTemp(pattern string) (TemporaryArtifact, error) {
	h.calls = append(h.calls, "create-temp:"+pattern)
	return h.artifact, h.createTempErr
}

func (h *fakeHost) ValidateSudo(context.Context) error {
	h.calls = append(h.calls, "sudo")
	return h.sudoError
}

func (h *fakeHost) LookPath(name string) (string, error) {
	h.calls = append(h.calls, "look-path:"+name)
	if err := h.lookPathErrors[name]; err != nil {
		return "", err
	}
	if path := h.lookPaths[name]; path != "" {
		return path, nil
	}
	return "", ErrNotFound
}

func (h *fakeHost) InspectPath(path string) (PathInfo, error) {
	h.calls = append(h.calls, "inspect-path:"+path)
	if err := h.pathErrors[path]; err != nil {
		return PathInfo{}, err
	}
	return h.pathInfo[path], nil
}

func (h *fakeHost) Run(_ context.Context, command Command) error {
	h.calls = append(h.calls, "run:"+command.Path+" "+strings.Join(command.Args, " "))
	if h.runHook != nil {
		h.runHook(command)
	}
	return h.runErrors[command.Path]
}

func (h *fakeHost) Bootstrap(_ context.Context, target InstallTarget, _ Streams) error {
	h.calls = append(h.calls, "bootstrap:"+string(target))
	h.bootstrapped = append(h.bootstrapped, target)
	return h.bootstrapError
}

func executeForTest(host Host, args ...string) (int, string, string) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := Run(context.Background(), host, args, Streams{
		Stdin:  strings.NewReader(""),
		Stdout: &stdout,
		Stderr: &stderr,
	})

	return exitCode, stdout.String(), stderr.String()
}

func newFakeHost() *fakeHost {
	return &fakeHost{
		lookPaths:      make(map[string]string),
		lookPathErrors: make(map[string]error),
		pathInfo:       make(map[string]PathInfo),
		pathErrors:     make(map[string]error),
		runErrors:      make(map[string]error),
	}
}

func TestInstallAcceptsIntendedCommandShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                     string
		args                     []string
		wantHostBootstrapTargets []InstallTarget
		wantHomebrew             bool
	}{
		{name: "bare install", args: []string{"install"}, wantHostBootstrapTargets: []InstallTarget{Nix}, wantHomebrew: true},
		{name: "nix", args: []string{"install", "nix"}, wantHostBootstrapTargets: []InstallTarget{Nix}},
		{name: "homebrew", args: []string{"install", "homebrew"}, wantHomebrew: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			host, _, _ := newReadyHomebrewHost()

			exitCode, stdout, stderr := executeForTest(host, test.args...)

			if exitCode != 0 {
				t.Fatalf("exit code = %d, want 0; stderr = %q", exitCode, stderr)
			}
			if stdout != "" || stderr != "" {
				t.Fatalf("output = stdout %q, stderr %q; want silence", stdout, stderr)
			}
			if got := host.bootstrapped; !slices.Equal(got, test.wantHostBootstrapTargets) {
				t.Fatalf("host bootstrap targets = %v, want %v", got, test.wantHostBootstrapTargets)
			}
			if got := containsCallPrefix(host.calls, "run:/bin/bash"); got != test.wantHomebrew {
				t.Fatalf("Homebrew installer called = %t, want %t; calls = %v", got, test.wantHomebrew, host.calls)
			}
		})
	}
}

func TestInstallRejectsInvalidShapes(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"install", "apt"},
		{"install", "nix", "homebrew"},
		{"install", "apt", "--help"},
		{"install", "nix", "homebrew", "--help"},
	} {
		host := newFakeHost()

		exitCode, stdout, stderr := executeForTest(host, args...)

		if exitCode != 2 {
			t.Errorf("Run(%q) exit code = %d, want 2", args, exitCode)
		}
		if stdout != "" {
			t.Errorf("Run(%q) stdout = %q, want empty", args, stdout)
		}
		if !strings.Contains(stderr, "Error:") || !strings.Contains(stderr, "Usage: rig install [nix|homebrew]") {
			t.Errorf("Run(%q) stderr = %q, want concise error and usage", args, stderr)
		}
		if strings.Contains(stderr, "Flags:") || strings.Contains(stderr, "Available Commands:") {
			t.Errorf("Run(%q) stderr contains full help: %q", args, stderr)
		}
		if len(host.calls) != 0 {
			t.Errorf("Run(%q) host calls = %v, want none", args, host.calls)
		}
	}
}

func TestInstallChecksPATHBeforeCanonicalLocation(t *testing.T) {
	t.Parallel()

	host := newFakeHost()
	host.lookPaths["nix"] = "/custom/bin/nix"

	exitCode, stdout, stderr := executeForTest(host, "install", "nix")

	if exitCode != 0 || stdout != "" || stderr != "" {
		t.Fatalf("result = (%d, %q, %q), want quiet success", exitCode, stdout, stderr)
	}
	wantCalls := []string{"look-path:nix", "run:/custom/bin/nix --version"}
	if !slices.Equal(host.calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", host.calls, wantCalls)
	}
}

func TestInstallChecksCanonicalExecutableAfterPATH(t *testing.T) {
	t.Parallel()

	host := newFakeHost()
	host.pathInfo[nixCanonicalExecutable] = PathInfo{Exists: true, Executable: true}

	exitCode, stdout, stderr := executeForTest(host, "install", "nix")

	if exitCode != 0 || stdout != "" || stderr != "" {
		t.Fatalf("result = (%d, %q, %q), want quiet success", exitCode, stdout, stderr)
	}
	wantCalls := []string{
		"look-path:nix",
		"inspect-path:" + nixCanonicalExecutable,
		"run:" + nixCanonicalExecutable + " --version",
	}
	if !slices.Equal(host.calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", host.calls, wantCalls)
	}
}

func TestInstallReportsBrokenInstallationWithoutOverwritingIt(t *testing.T) {
	t.Parallel()

	host := newFakeHost()
	host.lookPaths["brew"] = "/custom/bin/brew"
	host.runErrors["/custom/bin/brew"] = errors.New("exit status 1")

	exitCode, stdout, stderr := executeForTest(host, "install", "homebrew")

	if exitCode != 1 || stdout != "" {
		t.Fatalf("result = (%d, %q), want operational failure and empty stdout", exitCode, stdout)
	}
	if !strings.Contains(stderr, "broken installation") || strings.Contains(stderr, "Usage:") {
		t.Fatalf("stderr = %q, want broken installation without usage", stderr)
	}
	if len(host.bootstrapped) != 0 {
		t.Fatalf("bootstrapped = %v, want none", host.bootstrapped)
	}
}

func TestInstallReportsPartialInstallationWithoutOverwritingIt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		info PathInfo
	}{
		{name: "canonical state root", path: nixStateRoot, info: PathInfo{Exists: true}},
		{name: "nonworking canonical executable", path: nixCanonicalExecutable, info: PathInfo{Exists: true}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			host := newFakeHost()
			host.pathInfo[test.path] = test.info

			exitCode, stdout, stderr := executeForTest(host, "install", "nix")

			if exitCode != 1 || stdout != "" {
				t.Fatalf("result = (%d, %q), want operational failure and empty stdout", exitCode, stdout)
			}
			if !strings.Contains(stderr, "partial installation") || strings.Contains(stderr, "Usage:") {
				t.Fatalf("stderr = %q, want partial installation without usage", stderr)
			}
			if len(host.bootstrapped) != 0 {
				t.Fatalf("bootstrapped = %v, want none", host.bootstrapped)
			}
		})
	}
}

func TestInstallAbsentHomebrewReachesInjectedHostEffects(t *testing.T) {
	t.Parallel()

	host, _, _ := newReadyHomebrewHost()

	exitCode, stdout, stderr := executeForTest(host, "install", "homebrew")

	if exitCode != 0 || stdout != "" || stderr != "" {
		t.Fatalf("result = (%d, %q, %q), want quiet success", exitCode, stdout, stderr)
	}
	if !containsCallPrefix(host.calls, "run:/bin/bash") {
		t.Fatalf("calls = %v, want Homebrew installer through injected host", host.calls)
	}
}

func TestInstallOperationalInspectionFailureIsConcise(t *testing.T) {
	t.Parallel()

	host := newFakeHost()
	host.lookPathErrors["nix"] = errors.New("PATH unavailable")

	exitCode, stdout, stderr := executeForTest(host, "install", "nix")

	if exitCode != 1 || stdout != "" {
		t.Fatalf("result = (%d, %q), want operational failure and empty stdout", exitCode, stdout)
	}
	if !strings.Contains(stderr, "PATH unavailable") || strings.Contains(stderr, "Usage:") {
		t.Fatalf("stderr = %q, want concise operational error", stderr)
	}
}

var _ Host = (*fakeHost)(nil)
