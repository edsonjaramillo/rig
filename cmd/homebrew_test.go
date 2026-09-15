package cmd

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
)

type memoryArtifact struct {
	strings.Builder
	closed bool
}

func (a *memoryArtifact) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (a *memoryArtifact) Close() error {
	a.closed = true
	return nil
}

func newReadyHomebrewHost() (*fakeHost, *memoryArtifact, *bool) {
	host := newFakeHost()
	host.platform = Platform{OS: "linux", Distribution: "ubuntu", Architecture: "amd64", Version: "24.04"}
	host.user = User{UID: 1000}
	host.terminal = true
	for _, executable := range []string{"git", "curl", "cc", "c++", "make", "ar", "ld", "ps", "file", "tar"} {
		host.lookPaths[executable] = "/usr/bin/" + executable
	}
	host.response = HTTPResponse{
		StatusCode: 200,
		FinalURL:   homebrewInstallerURL,
		Body:       io.NopCloser(strings.NewReader("#!/bin/bash\necho install\n")),
	}
	artifact := &memoryArtifact{}
	removed := false
	host.artifact = TemporaryArtifact{
		Name: "/tmp/rig-homebrew-test",
		File: artifact,
		Remove: func() error {
			removed = true
			return nil
		},
	}
	return host, artifact, &removed
}

func TestHomebrewBootstrapRunsOfficialInstallerAndVerifiesCanonicalExecutable(t *testing.T) {
	t.Parallel()

	host, artifact, removed := newReadyHomebrewHost()
	installerWasInteractive := false
	host.runHook = func(command Command) {
		if command.Path != "/bin/bash" {
			return
		}
		installerWasInteractive = command.Interactive
		_, _ = io.WriteString(command.Stdout, "upstream next steps\n")
		_, _ = io.WriteString(command.Stderr, "upstream warning\n")
	}

	exitCode, stdout, stderr := executeForTest(host, "install", "homebrew")

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", exitCode, stderr)
	}
	if stdout != "upstream next steps\n" || stderr != "upstream warning\n" {
		t.Fatalf("streams = (%q, %q), want unchanged upstream output", stdout, stderr)
	}
	if got := artifact.String(); got != "#!/bin/bash\necho install\n" {
		t.Fatalf("temporary installer = %q, want downloaded body", got)
	}
	if !artifact.closed || !*removed {
		t.Fatalf("cleanup = closed %t, removed %t; want both", artifact.closed, *removed)
	}
	if !installerWasInteractive {
		t.Fatal("installer command was not marked interactive")
	}
	wantOrderedCalls := []string{
		"platform",
		"user",
		"terminal",
		"sudo",
		"retrieve:" + homebrewInstallerURL,
		"create-temp:rig-homebrew-*",
		"run:/bin/bash /tmp/rig-homebrew-test",
		"run:" + linuxHomebrewExecutable + " --version",
	}
	if !isSubsequence(host.calls, wantOrderedCalls) {
		t.Fatalf("calls = %v, want ordered subsequence %v", host.calls, wantOrderedCalls)
	}
	if slices.Contains(host.calls, "bootstrap:homebrew") {
		t.Fatalf("calls = %v, Homebrew behavior must run above the host boundary", host.calls)
	}
}

func TestHomebrewSupportsDocumentedHosts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		platform  Platform
		canonical string
	}{
		{name: "Apple Silicon macOS 15", platform: Platform{OS: "darwin", Architecture: "arm64", Version: "15.0"}, canonical: macOSHomebrewExecutable},
		{name: "Ubuntu amd64", platform: Platform{OS: "linux", Distribution: "ubuntu", Architecture: "amd64", Version: "24.04"}, canonical: linuxHomebrewExecutable},
		{name: "Ubuntu arm64", platform: Platform{OS: "linux", Distribution: "ubuntu", Architecture: "arm64", Version: "24.04"}, canonical: linuxHomebrewExecutable},
		{name: "Ubuntu x86_64", platform: Platform{OS: "linux", Distribution: "ubuntu", Architecture: "x86_64", Version: "24.04"}, canonical: linuxHomebrewExecutable},
		{name: "Ubuntu aarch64", platform: Platform{OS: "linux", Distribution: "ubuntu", Architecture: "aarch64", Version: "24.04"}, canonical: linuxHomebrewExecutable},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			host, _, _ := newReadyHomebrewHost()
			host.platform = test.platform

			exitCode, _, stderr := executeForTest(host, "install", "homebrew")

			if exitCode != 0 {
				t.Fatalf("exit code = %d, want 0; stderr = %q", exitCode, stderr)
			}
			if !slices.Contains(host.calls, "run:"+test.canonical+" --version") {
				t.Fatalf("calls = %v, want canonical verification at %s", host.calls, test.canonical)
			}
		})
	}
}

func TestHealthyHomebrewSkipsAllBootstrapWork(t *testing.T) {
	t.Parallel()

	host := newFakeHost()
	host.lookPaths["brew"] = "/custom/bin/brew"

	exitCode, stdout, stderr := executeForTest(host, "install", "homebrew")

	if exitCode != 0 || stdout != "" || stderr != "" {
		t.Fatalf("result = (%d, %q, %q), want quiet success", exitCode, stdout, stderr)
	}
	want := []string{"look-path:brew", "run:/custom/bin/brew --version"}
	if !slices.Equal(host.calls, want) {
		t.Fatalf("calls = %v, want only %v", host.calls, want)
	}
}

func TestHomebrewRejectsUnsupportedPlatformsBeforeDownload(t *testing.T) {
	t.Parallel()

	platforms := []Platform{
		{OS: "darwin", Architecture: "amd64", Version: "15.0"},
		{OS: "darwin", Architecture: "arm64", Version: "14.7"},
		{OS: "linux", Distribution: "debian", Architecture: "amd64", Version: "24.04"},
		{OS: "linux", Distribution: "ubuntu", Architecture: "386", Version: "24.04"},
		{OS: "linux", Distribution: "ubuntu", Architecture: "arm64", Version: "23.10"},
		{OS: "windows", Architecture: "amd64", Version: "11"},
	}
	for _, platform := range platforms {
		platform := platform
		t.Run(platform.OS+"-"+platform.Architecture+"-"+platform.Version, func(t *testing.T) {
			t.Parallel()
			host := newFakeHost()
			host.platform = platform

			exitCode, stdout, stderr := executeForTest(host, "install", "homebrew")

			if exitCode != 1 || stdout != "" || !strings.Contains(stderr, "unsupported platform") {
				t.Fatalf("result = (%d, %q, %q), want unsupported operational failure", exitCode, stdout, stderr)
			}
			if containsCallPrefix(host.calls, "retrieve:") {
				t.Fatalf("calls = %v, unsupported host must fail before download", host.calls)
			}
		})
	}
}

func TestHomebrewRejectsRootAndNonInteractiveExecutionBeforeDownload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		prepare  func(*fakeHost)
		wantText string
	}{
		{name: "root", prepare: func(h *fakeHost) { h.user.UID = 0 }, wantText: "regular user"},
		{name: "without terminal", prepare: func(h *fakeHost) { h.terminal = false }, wantText: "interactive terminal"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			host, _, _ := newReadyHomebrewHost()
			test.prepare(host)

			exitCode, _, stderr := executeForTest(host, "install", "homebrew")

			if exitCode != 1 || !strings.Contains(stderr, test.wantText) {
				t.Fatalf("result = (%d, %q), want failure containing %q", exitCode, stderr, test.wantText)
			}
			if containsCallPrefix(host.calls, "retrieve:") {
				t.Fatalf("calls = %v, want no download", host.calls)
			}
		})
	}
}

func TestHomebrewReportsAllMissingUbuntuPrerequisites(t *testing.T) {
	t.Parallel()

	host, _, _ := newReadyHomebrewHost()
	delete(host.lookPaths, "git")
	delete(host.lookPaths, "cc")
	delete(host.lookPaths, "ps")
	delete(host.lookPaths, "file")

	exitCode, stdout, stderr := executeForTest(host, "install", "homebrew")

	if exitCode != 1 || stdout != "" {
		t.Fatalf("result = (%d, %q), want operational failure", exitCode, stdout)
	}
	for _, missing := range []string{"git", "cc", "ps", "file"} {
		if !strings.Contains(stderr, missing) {
			t.Errorf("stderr = %q, want missing prerequisite %q", stderr, missing)
		}
	}
	if slices.Contains(host.calls, "sudo") || containsCallPrefix(host.calls, "retrieve:") {
		t.Fatalf("calls = %v, preflight must finish before sudo and download", host.calls)
	}
}

func TestHomebrewMacOSRequiresDeveloperToolsReadiness(t *testing.T) {
	t.Parallel()

	host, _, _ := newReadyHomebrewHost()
	host.platform = Platform{OS: "darwin", Architecture: "arm64", Version: "15.0"}
	host.runErrors["/usr/bin/xcrun"] = errors.New("license not accepted")

	exitCode, _, stderr := executeForTest(host, "install", "homebrew")

	if exitCode != 1 || !strings.Contains(stderr, "Xcode Command Line Tools") || !strings.Contains(stderr, "accepted license") {
		t.Fatalf("result = (%d, %q), want developer tools and license diagnostic", exitCode, stderr)
	}
	if slices.Contains(host.calls, "sudo") || containsCallPrefix(host.calls, "retrieve:") {
		t.Fatalf("calls = %v, want developer readiness before sudo and download", host.calls)
	}
}

func TestHomebrewValidatesSudoBeforeDownload(t *testing.T) {
	t.Parallel()

	host, _, _ := newReadyHomebrewHost()
	host.sudoError = errors.New("authentication failed")

	exitCode, _, stderr := executeForTest(host, "install", "homebrew")

	if exitCode != 1 || !strings.Contains(stderr, "validate sudo") {
		t.Fatalf("result = (%d, %q), want sudo validation failure", exitCode, stderr)
	}
	if containsCallPrefix(host.calls, "retrieve:") || containsCallPrefix(host.calls, "create-temp:") {
		t.Fatalf("calls = %v, sudo must fail before download and mutation", host.calls)
	}
}

func TestHomebrewRejectsUnsafeInstallerResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		prepare  func(*fakeHost)
		wantText string
	}{
		{name: "transport error", prepare: func(h *fakeHost) { h.retrieveError = errors.New("network down") }, wantText: "network down"},
		{name: "unsuccessful status", prepare: func(h *fakeHost) { h.response.StatusCode = 503 }, wantText: "HTTP status 503"},
		{name: "insecure redirect", prepare: func(h *fakeHost) { h.response.FinalURL = "http://example.test/install.sh" }, wantText: "HTTPS"},
		{name: "oversized body", prepare: func(h *fakeHost) {
			h.response.Body = io.NopCloser(strings.NewReader(strings.Repeat("x", homebrewInstallerSizeLimit+1)))
		}, wantText: "exceeds"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			host, artifact, removed := newReadyHomebrewHost()
			test.prepare(host)

			exitCode, _, stderr := executeForTest(host, "install", "homebrew")

			if exitCode != 1 || !strings.Contains(stderr, test.wantText) {
				t.Fatalf("result = (%d, %q), want failure containing %q", exitCode, stderr, test.wantText)
			}
			if containsCallPrefix(host.calls, "run:/bin/bash") {
				t.Fatalf("calls = %v, unsafe response must not execute", host.calls)
			}
			if test.name == "oversized body" && (!artifact.closed || !*removed) {
				t.Fatalf("oversized cleanup = closed %t removed %t, want both", artifact.closed, *removed)
			}
		})
	}
}

func TestHomebrewInstallerFailureIsConciseAndCleansUp(t *testing.T) {
	t.Parallel()

	host, artifact, removed := newReadyHomebrewHost()
	host.runErrors["/bin/bash"] = errors.New("exit status 1")
	host.runHook = func(command Command) {
		if command.Path == "/bin/bash" {
			_, _ = io.WriteString(command.Stderr, "upstream diagnostic\n")
		}
	}

	exitCode, stdout, stderr := executeForTest(host, "install", "homebrew")

	if exitCode != 1 || stdout != "" {
		t.Fatalf("result = (%d, %q), want installer failure", exitCode, stdout)
	}
	if stderr != "upstream diagnostic\nError: homebrew installer failed\n" {
		t.Fatalf("stderr = %q, want upstream output followed by one concise Rig line", stderr)
	}
	if !artifact.closed || !*removed {
		t.Fatalf("cleanup = closed %t removed %t, want both", artifact.closed, *removed)
	}
}

func TestHomebrewVerificationFailureDoesNotRetryOrRollback(t *testing.T) {
	t.Parallel()

	host, _, removed := newReadyHomebrewHost()
	host.runErrors[linuxHomebrewExecutable] = errors.New("not found")

	exitCode, _, stderr := executeForTest(host, "install", "homebrew")

	if exitCode != 1 || !strings.Contains(stderr, "verify homebrew") {
		t.Fatalf("result = (%d, %q), want verification failure", exitCode, stderr)
	}
	if countCalls(host.calls, "run:/bin/bash /tmp/rig-homebrew-test") != 1 {
		t.Fatalf("calls = %v, want one installer attempt and no retry", host.calls)
	}
	if !*removed {
		t.Fatal("temporary installer was not removed")
	}
}

func TestHomebrewCancellationExits130WithoutFailureLineAndCleansUp(t *testing.T) {
	t.Parallel()

	host, _, removed := newReadyHomebrewHost()
	host.runErrors["/bin/bash"] = context.Canceled

	exitCode, stdout, stderr := executeForTest(host, "install", "homebrew")

	if exitCode != 130 || stdout != "" || stderr != "" {
		t.Fatalf("result = (%d, %q, %q), want silent cancellation status 130", exitCode, stdout, stderr)
	}
	if !*removed {
		t.Fatal("temporary installer was not removed after cancellation")
	}
}

func containsCallPrefix(calls []string, prefix string) bool {
	for _, call := range calls {
		if strings.HasPrefix(call, prefix) {
			return true
		}
	}
	return false
}

func countCalls(calls []string, want string) int {
	count := 0
	for _, call := range calls {
		if call == want {
			count++
		}
	}
	return count
}

func isSubsequence(got, want []string) bool {
	position := 0
	for _, call := range got {
		if position < len(want) && call == want[position] {
			position++
		}
	}
	return position == len(want)
}
