package cmd

import (
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestBareInstallSkipsInstalledTargetsWithoutPreflightOrOutput(t *testing.T) {
	t.Parallel()

	host := newFakeHost()
	host.lookPaths["nix"] = "/custom/bin/nix"
	host.lookPaths["brew"] = "/custom/bin/brew"

	exitCode, stdout, stderr := executeForTest(host, "install")

	if exitCode != 0 || stdout != "" || stderr != "" {
		t.Fatalf("result = (%d, %q, %q), want quiet success", exitCode, stdout, stderr)
	}
	want := []string{
		"look-path:nix",
		"run:/custom/bin/nix --version",
		"look-path:brew",
		"run:/custom/bin/brew --version",
	}
	if !slices.Equal(host.calls, want) {
		t.Fatalf("calls = %v, want only ordered detection %v", host.calls, want)
	}
}

func TestBareInstallRunsPendingHomebrewPreflightAfterInstalledNixDetection(t *testing.T) {
	t.Parallel()

	host, _, _ := newReadyHomebrewHost()
	host.lookPaths["nix"] = "/custom/bin/nix"

	exitCode, stdout, stderr := executeForTest(host, "install")

	if exitCode != 0 || stdout != "" || stderr != "" {
		t.Fatalf("result = (%d, %q, %q), want quiet success", exitCode, stdout, stderr)
	}
	wantOrder := []string{
		"look-path:nix",
		"run:/custom/bin/nix --version",
		"look-path:brew",
		"inspect-path:" + macOSHomebrewExecutable,
		"inspect-path:" + linuxHomebrewExecutable,
		"inspect-path:/usr/local/bin/brew",
		"inspect-path:/opt/homebrew",
		"inspect-path:/home/linuxbrew/.linuxbrew",
		"inspect-path:/usr/local/Homebrew",
		"platform",
		"user",
		"terminal",
		"sudo",
		"retrieve:" + homebrewInstallerURL,
		"create-temp:rig-homebrew-*",
		"run:/bin/bash /tmp/rig-homebrew-test",
	}
	if !isSubsequence(host.calls, wantOrder) {
		t.Fatalf("calls = %v, want detection then immediately ordered preflight and installation %v", host.calls, wantOrder)
	}
	if !slices.Equal(host.calls[:2], wantOrder[:2]) {
		t.Fatalf("calls = %v, installed Nix must skip its preflight", host.calls)
	}
}

func TestBareInstallDetectsHomebrewOnlyAfterNixCompletes(t *testing.T) {
	t.Parallel()

	fixture := makeNixReady(newFakeHost())
	fixture.host.runHook = func(command Command) {
		if slices.Equal(command.Args, nixBaselineVerificationArgs) {
			_, _ = io.WriteString(command.Stdout, "flakes nix-command\n")
			fixture.host.lookPaths["brew"] = "/became-available/bin/brew"
		}
	}

	exitCode, stdout, stderr := executeForTest(fixture.host, "install")

	if exitCode != 0 || stdout != "" || stderr != "" {
		t.Fatalf("result = (%d, %q, %q), want quiet success", exitCode, stdout, stderr)
	}
	nixComplete := slices.Index(fixture.host.calls,
		"run:"+nixCanonicalExecutable+" "+strings.Join(nixBaselineVerificationArgs, " "))
	homebrewDetection := slices.Index(fixture.host.calls, "look-path:brew")
	if nixComplete < 0 || homebrewDetection <= nixComplete {
		t.Fatalf("calls = %v, want Homebrew detection after Nix completion", fixture.host.calls)
	}
	wantTail := []string{"look-path:brew", "run:/became-available/bin/brew --version"}
	if !slices.Equal(fixture.host.calls[len(fixture.host.calls)-len(wantTail):], wantTail) {
		t.Fatalf("calls = %v, want installed Homebrew skipped without preflight", fixture.host.calls)
	}
}

func TestBareInstallStopsBeforeHomebrewWhenNixCannotComplete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prepare func(*fakeHost)
	}{
		{name: "broken installation", prepare: func(host *fakeHost) {
			host.lookPaths["nix"] = "/broken/bin/nix"
			host.runErrors["/broken/bin/nix"] = errors.New("exit status 1")
		}},
		{name: "partial installation", prepare: func(host *fakeHost) {
			host.pathInfo[nixStateRoot] = PathInfo{Exists: true}
		}},
		{name: "failed preflight", prepare: func(host *fakeHost) {
			host.sudoError = errors.New("authentication failed")
		}},
		{name: "failed installer", prepare: func(host *fakeHost) {
			host.runErrors["/bin/bash"] = errors.New("exit status 1")
		}},
		{name: "failed post-install verification", prepare: func(host *fakeHost) {
			host.runErrorHook = func(command Command) error {
				if slices.Equal(command.Args, nixDaemonVerificationArgs) {
					return errors.New("daemon unavailable")
				}
				return nil
			}
		}},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newReadyNixHost()
			test.prepare(fixture.host)

			exitCode, _, _ := executeForTest(fixture.host, "install")

			if exitCode != 1 {
				t.Fatalf("exit code = %d, want 1; calls = %v", exitCode, fixture.host.calls)
			}
			if slices.Contains(fixture.host.calls, "look-path:brew") ||
				containsCallPrefix(fixture.host.calls, "retrieve:"+homebrewInstallerURL) {
				t.Fatalf("calls = %v, want no Homebrew detection, preflight, or installation", fixture.host.calls)
			}
		})
	}
}

func TestBareInstallStopsAtEachHomebrewFailureAfterInstalledNix(t *testing.T) {
	t.Parallel()

	tests := homebrewFailureScenarios(func(host *fakeHost) {
		host.runErrors["/bin/bash"] = errors.New("exit status 1")
	})

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			host, _, _ := newReadyHomebrewHost()
			host.lookPaths["nix"] = "/custom/bin/nix"
			test.prepare(host)

			exitCode, _, _ := executeForTest(host, "install")

			if exitCode != 1 {
				t.Fatalf("exit code = %d, want 1; calls = %v", exitCode, host.calls)
			}
			wantPrefix := []string{
				"look-path:nix",
				"run:/custom/bin/nix --version",
				"look-path:brew",
			}
			if !slices.Equal(host.calls[:len(wantPrefix)], wantPrefix) {
				t.Fatalf("calls = %v, want installed Nix skip followed by Homebrew detection", host.calls)
			}
		})
	}
}

func TestBareInstallRetainsSuccessfulNixWhenHomebrewFails(t *testing.T) {
	t.Parallel()

	tests := homebrewFailureScenarios(func(host *fakeHost) {
		host.runErrorHook = func(command Command) error {
			if isHomebrewInstallerCommand(command) {
				return errors.New("exit status 1")
			}
			return nil
		}
	})

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			host, _, _ := newReadyHomebrewHost()
			fixture := makeNixReady(host)
			originalRunHook := host.runHook
			host.runHook = func(command Command) {
				originalRunHook(command)
				if slices.Equal(command.Args, nixBaselineVerificationArgs) {
					host.lookPaths["nix"] = nixCanonicalExecutable
				}
			}
			test.prepare(host)

			exitCode, _, _ := executeForTest(host, "install")

			if exitCode != 1 {
				t.Fatalf("exit code = %d, want 1; calls = %v", exitCode, host.calls)
			}
			nixInstall := "run:/bin/bash " + strings.Join([]string{
				testNixInstallerPath, "--daemon", "--nix-extra-conf-file", testNixConfigPath,
			}, " ")
			nixVerification := "run:" + nixCanonicalExecutable + " " + strings.Join(nixBaselineVerificationArgs, " ")
			if !slices.Contains(host.calls, nixInstall) || !slices.Contains(host.calls, nixVerification) {
				t.Fatalf("calls = %v, want Nix installed and verified before Homebrew failure", host.calls)
			}
			if !*fixture.installerRemoved || !*fixture.configRemoved {
				t.Fatal("Nix temporary artifacts were not cleaned after its successful installation")
			}
			if countCalls(host.calls, nixInstall) != 1 {
				t.Fatalf("calls = %v, want exactly one Nix installation", host.calls)
			}
			homebrewDetection := slices.Index(host.calls, "look-path:brew")
			for _, call := range host.calls[homebrewDetection+1:] {
				if strings.Contains(call, "nix") {
					t.Fatalf("calls after Homebrew detection = %v, want no Nix retry or rollback", host.calls[homebrewDetection+1:])
				}
			}

			host.calls = nil
			exitCode, stdout, stderr := executeForTest(host, "install", "nix")
			if exitCode != 0 || stdout != "" || stderr != "" {
				t.Fatalf("later Nix result = (%d, %q, %q), want retained installed target", exitCode, stdout, stderr)
			}
			wantRetainedCalls := []string{
				"look-path:nix",
				"run:" + nixCanonicalExecutable + " --version",
			}
			if !slices.Equal(host.calls, wantRetainedCalls) {
				t.Fatalf("later Nix calls = %v, want %v", host.calls, wantRetainedCalls)
			}
		})
	}
}

func TestBareInstallPreservesUpstreamOutputBeforeLaterRigError(t *testing.T) {
	t.Parallel()

	host, _, _ := newReadyHomebrewHost()
	_ = makeNixReady(host)
	host.runHook = func(command Command) {
		switch {
		case isNixInstallerCommand(command):
			_, _ = io.WriteString(command.Stdout, "nix stdout\n")
			_, _ = io.WriteString(command.Stderr, "nix stderr\n")
		case slices.Equal(command.Args, nixBaselineVerificationArgs):
			_, _ = io.WriteString(command.Stdout, "flakes nix-command\n")
		case isHomebrewInstallerCommand(command):
			_, _ = io.WriteString(command.Stdout, "homebrew stdout\n")
			_, _ = io.WriteString(command.Stderr, "homebrew stderr\n")
		}
	}
	host.runErrorHook = func(command Command) error {
		if isHomebrewInstallerCommand(command) {
			return errors.New("exit status 1")
		}
		return nil
	}

	exitCode, stdout, stderr := executeForTest(host, "install")

	if exitCode != 1 {
		t.Fatalf("exit code = %d, want 1", exitCode)
	}
	if stdout != "nix stdout\nhomebrew stdout\n" {
		t.Fatalf("stdout = %q, want upstream output in target order", stdout)
	}
	if stderr != "nix stderr\nhomebrew stderr\nError: homebrew installer failed\n" {
		t.Fatalf("stderr = %q, want upstream diagnostics before concise Rig error", stderr)
	}
}

type homebrewFailureScenario struct {
	name    string
	prepare func(*fakeHost)
}

func homebrewFailureScenarios(failInstaller func(*fakeHost)) []homebrewFailureScenario {
	return []homebrewFailureScenario{
		{name: "broken installation", prepare: func(host *fakeHost) {
			host.lookPaths["brew"] = "/broken/bin/brew"
			host.runErrors["/broken/bin/brew"] = errors.New("exit status 1")
		}},
		{name: "partial installation", prepare: func(host *fakeHost) {
			host.pathInfo[linuxHomebrewExecutable] = PathInfo{Exists: true}
		}},
		{name: "failed preflight", prepare: func(host *fakeHost) {
			delete(host.lookPaths, "git")
		}},
		{name: "failed installer", prepare: failInstaller},
		{name: "failed post-install verification", prepare: func(host *fakeHost) {
			host.runErrors[linuxHomebrewExecutable] = errors.New("not found")
		}},
	}
}

func isNixInstallerCommand(command Command) bool {
	return command.Path == "/bin/bash" && len(command.Args) > 0 && command.Args[0] == testNixInstallerPath
}

func isHomebrewInstallerCommand(command Command) bool {
	return command.Path == "/bin/bash" && slices.Equal(command.Args, []string{"/tmp/rig-homebrew-test"})
}
