package cmd

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
)

const (
	testNixInstallerPath = "/tmp/rig-nix-installer-test"
	testNixConfigPath    = "/tmp/rig-nix-config-test"
)

type readyNixHost struct {
	host             *fakeHost
	installer        *memoryArtifact
	configuration    *memoryArtifact
	installerRemoved *bool
	configRemoved    *bool
}

func newReadyNixHost() readyNixHost {
	return makeNixReady(newFakeHost())
}

func makeNixReady(host *fakeHost) readyNixHost {
	host.platform = Platform{OS: "linux", Distribution: "ubuntu", Architecture: "amd64", Version: "24.04"}
	host.user = User{UID: 1000}
	host.terminal = true
	for _, executable := range []string{
		"awk", "bash", "cat", "chmod", "chown", "cp", "curl", "cut", "diff", "dirname", "env", "getent",
		"grep", "groupadd", "groups", "head", "id", "install", "ln", "mkdir", "mktemp", "mv", "rm", "sed",
		"seq", "sha256sum", "sleep", "sudo", "systemctl", "systemd-tmpfiles", "tar", "tee", "touch",
		"tr", "uname", "useradd", "usermod", "xz",
	} {
		host.lookPaths[executable] = "/usr/bin/" + executable
	}
	host.pathInfo[systemdRuntimePath] = PathInfo{Exists: true}
	host.responses[nixInstallerURL] = HTTPResponse{
		StatusCode: 200,
		FinalURL:   nixInstallerURL,
		Body:       io.NopCloser(strings.NewReader("#!/bin/sh\necho install nix\n")),
	}

	installer := &memoryArtifact{}
	configuration := &memoryArtifact{}
	installerRemoved := false
	configRemoved := false
	host.artifacts[nixInstallerTempPattern] = TemporaryArtifact{
		Name: testNixInstallerPath,
		File: installer,
		Remove: func() error {
			installerRemoved = true
			return nil
		},
	}
	host.artifacts[nixConfigTempPattern] = TemporaryArtifact{
		Name: testNixConfigPath,
		File: configuration,
		Remove: func() error {
			configRemoved = true
			return nil
		},
	}
	host.runHook = func(command Command) {
		if slices.Equal(command.Args, nixBaselineVerificationArgs) {
			_, _ = io.WriteString(command.Stdout, "fetch-tree flakes nix-command\n")
		}
	}
	return readyNixHost{
		host:             host,
		installer:        installer,
		configuration:    configuration,
		installerRemoved: &installerRemoved,
		configRemoved:    &configRemoved,
	}
}

func TestNixBootstrapInstallsDaemonWithAdditiveBaselineAndVerifiesIt(t *testing.T) {
	t.Parallel()

	fixture := newReadyNixHost()
	var installerCommand Command
	var daemonCommand Command
	var baselineCommand Command
	fixture.host.runHook = func(command Command) {
		switch {
		case command.Path == "/bin/bash":
			installerCommand = command
			_, _ = io.WriteString(command.Stdout, "upstream warning on stdout\n")
			_, _ = io.WriteString(command.Stderr, "upstream warning on stderr\n")
		case slices.Equal(command.Args, nixDaemonVerificationArgs):
			daemonCommand = command
		case slices.Equal(command.Args, nixBaselineVerificationArgs):
			baselineCommand = command
			_, _ = io.WriteString(command.Stdout, "fetch-tree flakes nix-command\n")
		}
	}

	exitCode, stdout, stderr := executeForTest(fixture.host, "install", "nix")

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", exitCode, stderr)
	}
	if stdout != "upstream warning on stdout\n" || stderr != "upstream warning on stderr\n" {
		t.Fatalf("streams = (%q, %q), want unchanged installer output", stdout, stderr)
	}
	if got := fixture.configuration.String(); got != "extra-experimental-features = nix-command flakes\n" {
		t.Fatalf("temporary Nix configuration = %q, want additive baseline", got)
	}
	wantInstallerArgs := []string{testNixInstallerPath, "--daemon", "--nix-extra-conf-file", testNixConfigPath}
	if installerCommand.Path != "/bin/bash" || !slices.Equal(installerCommand.Args, wantInstallerArgs) || !installerCommand.Interactive {
		t.Fatalf("installer command = %#v, want interactive /bin/bash %v", installerCommand, wantInstallerArgs)
	}
	if installerCommand.Stdin == nil || installerCommand.Stdout == nil || installerCommand.Stderr == nil {
		t.Fatalf("installer streams = %#v, want stdin/stdout/stderr passthrough", installerCommand.Streams)
	}
	if daemonCommand.Path != nixCanonicalExecutable {
		t.Fatalf("daemon verification = %#v, want canonical Nix executable", daemonCommand)
	}
	if baselineCommand.Path != nixCanonicalExecutable || !slices.Equal(baselineCommand.Env, nixVerificationEnvironment) {
		t.Fatalf("baseline verification = %#v, want canonical executable and sanitized environment %v", baselineCommand, nixVerificationEnvironment)
	}
	if !fixture.installer.closed || !fixture.configuration.closed || !*fixture.installerRemoved || !*fixture.configRemoved {
		t.Fatalf("cleanup = installer(closed=%t removed=%t), config(closed=%t removed=%t); want all true",
			fixture.installer.closed, *fixture.installerRemoved, fixture.configuration.closed, *fixture.configRemoved)
	}
	wantOrder := []string{
		"platform", "user", "terminal", "inspect-path:" + systemdRuntimePath,
		"run:systemctl is-system-running", "sudo", "retrieve:" + nixInstallerURL,
		"create-temp:" + nixInstallerTempPattern, "create-temp:" + nixConfigTempPattern,
		"run:/bin/bash " + strings.Join(wantInstallerArgs, " "),
		"run:" + nixCanonicalExecutable + " --version",
		"run:" + nixCanonicalExecutable + " " + strings.Join(nixDaemonVerificationArgs, " "),
		"run:" + nixCanonicalExecutable + " " + strings.Join(nixBaselineVerificationArgs, " "),
	}
	if !isSubsequence(fixture.host.calls, wantOrder) {
		t.Fatalf("calls = %v, want ordered subsequence %v", fixture.host.calls, wantOrder)
	}
	if slices.Contains(fixture.host.calls, "bootstrap:nix") {
		t.Fatalf("calls = %v, Nix behavior must run above the host boundary", fixture.host.calls)
	}
}

func TestNixBootstrapExplicitlySelectsDaemonModeOnMacOS(t *testing.T) {
	t.Parallel()

	fixture := newReadyNixHost()
	fixture.host.platform = Platform{OS: "darwin", Architecture: "arm64", Version: "15.0"}
	for _, executable := range []string{
		"diskutil", "dscl", "dseditgroup", "fdesetup", "launchctl", "plutil", "security", "shasum", "stat", "xmllint",
	} {
		fixture.host.lookPaths[executable] = "/usr/bin/" + executable
	}

	exitCode, _, stderr := executeForTest(fixture.host, "install", "nix")

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", exitCode, stderr)
	}
	want := "run:/bin/bash " + strings.Join([]string{
		testNixInstallerPath, "--daemon", "--nix-extra-conf-file", testNixConfigPath,
	}, " ")
	if !slices.Contains(fixture.host.calls, want) {
		t.Fatalf("calls = %v, want explicit daemon installer call %q", fixture.host.calls, want)
	}
	if slices.Contains(fixture.host.calls, "run:systemctl is-system-running") {
		t.Fatalf("calls = %v, macOS preflight must not use systemd", fixture.host.calls)
	}
}

func TestInstalledNixSkipsBootstrapAndBaselineConfiguration(t *testing.T) {
	t.Parallel()

	host := newFakeHost()
	host.lookPaths["nix"] = "/custom/bin/nix"

	exitCode, stdout, stderr := executeForTest(host, "install", "nix")

	if exitCode != 0 || stdout != "" || stderr != "" {
		t.Fatalf("result = (%d, %q, %q), want quiet success", exitCode, stdout, stderr)
	}
	want := []string{"look-path:nix", "run:/custom/bin/nix --version"}
	if !slices.Equal(host.calls, want) {
		t.Fatalf("calls = %v, want only %v even when the existing flakes setting is unknown", host.calls, want)
	}
}

func TestNixBootstrapRejectsUnsafeExecutionBeforeDownload(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		prepare  func(*fakeHost)
		wantText string
	}{
		{name: "unsupported platform", prepare: func(host *fakeHost) {
			host.platform = Platform{OS: "linux", Distribution: "debian", Architecture: "amd64", Version: "24.04"}
		}, wantText: "unsupported platform"},
		{name: "root", prepare: func(host *fakeHost) { host.user.UID = 0 }, wantText: "regular user"},
		{name: "without terminal", prepare: func(host *fakeHost) { host.terminal = false }, wantText: "interactive terminal"},
		{name: "without sudo", prepare: func(host *fakeHost) {
			host.sudoError = errors.New("authentication failed")
		}, wantText: "validate sudo"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newReadyNixHost()
			test.prepare(fixture.host)

			exitCode, _, stderr := executeForTest(fixture.host, "install", "nix")

			if exitCode != 1 || !strings.Contains(stderr, test.wantText) {
				t.Fatalf("result = (%d, %q), want failure containing %q", exitCode, stderr, test.wantText)
			}
			if containsCallPrefix(fixture.host.calls, "retrieve:") {
				t.Fatalf("calls = %v, unsafe execution must fail before download", fixture.host.calls)
			}
		})
	}
}

func TestNixBootstrapRequiresRunningSystemdBeforeDownload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prepare func(*fakeHost)
	}{
		{name: "runtime absent", prepare: func(host *fakeHost) { delete(host.pathInfo, systemdRuntimePath) }},
		{name: "manager not running", prepare: func(host *fakeHost) {
			host.runErrors["systemctl"] = errors.New("offline")
		}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newReadyNixHost()
			test.prepare(fixture.host)

			exitCode, _, stderr := executeForTest(fixture.host, "install", "nix")

			if exitCode != 1 || !strings.Contains(stderr, "systemd") {
				t.Fatalf("result = (%d, %q), want systemd preflight failure", exitCode, stderr)
			}
			if containsCallPrefix(fixture.host.calls, "retrieve:") {
				t.Fatalf("calls = %v, want no download", fixture.host.calls)
			}
		})
	}
}

func TestNixBootstrapReportsMissingUbuntuPrerequisitesTogether(t *testing.T) {
	t.Parallel()

	fixture := newReadyNixHost()
	delete(fixture.host.lookPaths, "cat")
	delete(fixture.host.lookPaths, "id")
	delete(fixture.host.lookPaths, "tar")
	delete(fixture.host.lookPaths, "curl")
	delete(fixture.host.lookPaths, "sha256sum")

	exitCode, _, stderr := executeForTest(fixture.host, "install", "nix")

	if exitCode != 1 {
		t.Fatalf("exit code = %d, want 1; stderr = %q", exitCode, stderr)
	}
	for _, missing := range []string{"cat", "id", "tar", "curl or wget", "sha256sum, shasum, or openssl"} {
		if !strings.Contains(stderr, missing) {
			t.Errorf("stderr = %q, want missing prerequisite %q", stderr, missing)
		}
	}
	if slices.Contains(fixture.host.calls, "sudo") || containsCallPrefix(fixture.host.calls, "retrieve:") {
		t.Fatalf("calls = %v, preflight must finish before sudo and download", fixture.host.calls)
	}
}

func TestNixBootstrapValidatesMacOSAdministrationCapabilities(t *testing.T) {
	t.Parallel()

	fixture := newReadyNixHost()
	fixture.host.platform = Platform{OS: "darwin", Architecture: "arm64", Version: "15.0"}
	for _, executable := range []string{"dscl", "dseditgroup", "fdesetup", "launchctl", "plutil", "security", "shasum", "stat", "xmllint"} {
		fixture.host.lookPaths[executable] = "/usr/bin/" + executable
	}
	fixture.host.lookPaths["diskutil"] = "/usr/sbin/diskutil"
	delete(fixture.host.lookPaths, "dscl")
	delete(fixture.host.lookPaths, "diskutil")

	exitCode, _, stderr := executeForTest(fixture.host, "install", "nix")

	if exitCode != 1 || !strings.Contains(stderr, "dscl") || !strings.Contains(stderr, "diskutil") {
		t.Fatalf("result = (%d, %q), want all missing macOS administration tools", exitCode, stderr)
	}
	if containsCallPrefix(fixture.host.calls, "retrieve:") {
		t.Fatalf("calls = %v, want capability failure before download", fixture.host.calls)
	}
}

func TestNixBootstrapAcceptsDegradedRunningSystemd(t *testing.T) {
	t.Parallel()

	fixture := newReadyNixHost()
	fixture.host.runHook = func(command Command) {
		switch {
		case command.Path == "systemctl":
			_, _ = io.WriteString(command.Stdout, "degraded\n")
		case slices.Equal(command.Args, nixBaselineVerificationArgs):
			_, _ = io.WriteString(command.Stdout, "flakes nix-command\n")
		}
	}
	fixture.host.runErrorHook = func(command Command) error {
		if command.Path == "systemctl" {
			return errors.New("exit status 1")
		}
		return nil
	}

	exitCode, _, stderr := executeForTest(fixture.host, "install", "nix")

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0 for running degraded systemd; stderr = %q", exitCode, stderr)
	}
}

func TestNixPreparationFailureReportsCleanupFailure(t *testing.T) {
	t.Parallel()

	fixture := newReadyNixHost()
	fixture.host.responses[nixInstallerURL] = HTTPResponse{
		StatusCode: 200,
		FinalURL:   nixInstallerURL,
		Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", nixInstallerSizeLimit+1))),
	}
	installer := fixture.host.artifacts[nixInstallerTempPattern]
	installer.Remove = func() error { return errors.New("permission denied") }
	fixture.host.artifacts[nixInstallerTempPattern] = installer

	exitCode, _, stderr := executeForTest(fixture.host, "install", "nix")

	if exitCode != 1 || !strings.Contains(stderr, "response exceeds") || !strings.Contains(stderr, "remove temporary nix installer") {
		t.Fatalf("result = (%d, %q), want preparation and cleanup failures", exitCode, stderr)
	}
}

func TestNixInstallerFailureDoesNotRetryAndCleansBothArtifacts(t *testing.T) {
	t.Parallel()

	fixture := newReadyNixHost()
	fixture.host.runErrors["/bin/bash"] = errors.New("exit status 1")

	exitCode, stdout, stderr := executeForTest(fixture.host, "install", "nix")

	if exitCode != 1 || stdout != "" || stderr != "Error: nix installer failed\n" {
		t.Fatalf("result = (%d, %q, %q), want concise installer failure", exitCode, stdout, stderr)
	}
	if countCalls(fixture.host.calls, "run:/bin/bash "+strings.Join([]string{testNixInstallerPath, "--daemon", "--nix-extra-conf-file", testNixConfigPath}, " ")) != 1 {
		t.Fatalf("calls = %v, want exactly one installer attempt", fixture.host.calls)
	}
	if !*fixture.installerRemoved || !*fixture.configRemoved {
		t.Fatalf("cleanup = installer %t config %t, want both removed", *fixture.installerRemoved, *fixture.configRemoved)
	}
}

func TestNixInstallerFailureReportsCleanupFailure(t *testing.T) {
	t.Parallel()

	fixture := newReadyNixHost()
	fixture.host.runErrors["/bin/bash"] = errors.New("exit status 1")
	configuration := fixture.host.artifacts[nixConfigTempPattern]
	configuration.Remove = func() error { return errors.New("permission denied") }
	fixture.host.artifacts[nixConfigTempPattern] = configuration

	exitCode, _, stderr := executeForTest(fixture.host, "install", "nix")

	if exitCode != 1 || !strings.Contains(stderr, "nix installer failed") || !strings.Contains(stderr, "remove temporary nix configuration") {
		t.Fatalf("result = (%d, %q), want installer and cleanup failures", exitCode, stderr)
	}
}

func TestNixCancellationExits130AndCleansBothArtifacts(t *testing.T) {
	t.Parallel()

	fixture := newReadyNixHost()
	fixture.host.runErrors["/bin/bash"] = context.Canceled

	exitCode, stdout, stderr := executeForTest(fixture.host, "install", "nix")

	if exitCode != 130 || stdout != "" || stderr != "" {
		t.Fatalf("result = (%d, %q, %q), want silent cancellation status 130", exitCode, stdout, stderr)
	}
	if !*fixture.installerRemoved || !*fixture.configRemoved {
		t.Fatalf("cleanup = installer %t config %t, want both removed", *fixture.installerRemoved, *fixture.configRemoved)
	}
}

func TestNixVerificationFailureReportsIncompleteBootstrapWithoutRetry(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		fail func(Command) error
	}{
		{name: "daemon unavailable", fail: func(command Command) error {
			if slices.Equal(command.Args, nixDaemonVerificationArgs) {
				return errors.New("daemon unavailable")
			}
			return nil
		}},
		{name: "baseline missing flakes", fail: func(command Command) error {
			if slices.Equal(command.Args, nixBaselineVerificationArgs) {
				_, _ = io.WriteString(command.Stdout, "nix-command\n")
			}
			return nil
		}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newReadyNixHost()
			fixture.host.runHook = nil
			fixture.host.runErrorHook = test.fail

			exitCode, _, stderr := executeForTest(fixture.host, "install", "nix")

			if exitCode != 1 || !strings.Contains(stderr, "incomplete bootstrap") || !strings.Contains(stderr, "manual remediation") {
				t.Fatalf("result = (%d, %q), want incomplete bootstrap remediation", exitCode, stderr)
			}
			if countCalls(fixture.host.calls, "run:/bin/bash "+strings.Join([]string{testNixInstallerPath, "--daemon", "--nix-extra-conf-file", testNixConfigPath}, " ")) != 1 {
				t.Fatalf("calls = %v, want no installer retry", fixture.host.calls)
			}
			if !*fixture.installerRemoved || !*fixture.configRemoved {
				t.Fatal("temporary files were not removed after verification failure")
			}

			fixture.host.calls = nil
			fixture.host.runErrorHook = nil
			fixture.host.pathInfo[nixCanonicalExecutable] = PathInfo{Exists: true, Executable: true}
			exitCode, stdout, stderr := executeForTest(fixture.host, "install", "nix")
			if exitCode != 0 || stdout != "" || stderr != "" {
				t.Fatalf("later result = (%d, %q, %q), want installed-target skip", exitCode, stdout, stderr)
			}
			want := []string{
				"look-path:nix", "inspect-path:" + nixCanonicalExecutable,
				"run:" + nixCanonicalExecutable + " --version",
			}
			if !slices.Equal(fixture.host.calls, want) {
				t.Fatalf("later calls = %v, want only %v", fixture.host.calls, want)
			}
		})
	}
}
