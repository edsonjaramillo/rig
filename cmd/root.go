package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/spf13/cobra"
)

// Run executes the real Rig command tree with an injected host and streams.
func Run(ctx context.Context, host Host, args []string, streams Streams) int {
	if err := validateInstallHelpArgs(args); err != nil {
		writeUsageError(streams.Stderr, err)
		return 2
	}

	root := newRootCommand(host, streams)
	root.SetArgs(args)
	root.SetIn(streams.Stdin)
	root.SetOut(streams.Stdout)
	root.SetErr(streams.Stderr)

	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}

	var invalidUsage *usageError
	if errors.As(err, &invalidUsage) {
		writeUsageError(streams.Stderr, err)
		return 2
	}
	var cancelled *cancellationError
	if errors.As(err, &cancelled) {
		return 130
	}

	_, _ = fmt.Fprintf(streams.Stderr, "Error: %s\n", err)
	return 1
}

// Execute runs Rig against the local operating system and exits with its status.
func Execute() {
	streams := Streams{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	exitCode := Run(ctx, NewOSHost(streams), os.Args[1:], streams)
	stop()
	os.Exit(exitCode)
}

type cancellationError struct{}

func (*cancellationError) Error() string {
	return "installation cancelled"
}

func writeUsageError(stderr io.Writer, err error) {
	_, _ = fmt.Fprintf(stderr, "Error: %s\nUsage: rig %s\n", err, installUse)
}

func newRootCommand(host Host, streams Streams) *cobra.Command {
	root := &cobra.Command{
		Use:           "rig",
		Short:         "Bootstrap and manage developer-machine tooling",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.AddCommand(newInstallCommand(host, streams))
	return root
}
