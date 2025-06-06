package util

import (
	"errors"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/operator-framework/operator-registry/pkg/image"
	"github.com/operator-framework/operator-registry/pkg/image/containersimageregistry"
)

// GetTLSOptions validates and returns TLS options set by opm flags
func GetTLSOptions(cmd *cobra.Command) (bool, bool, error) {
	skipTLS, err := cmd.Flags().GetBool("skip-tls")
	if err != nil {
		return false, false, err
	}
	skipTLSVerify, err := cmd.Flags().GetBool("skip-tls-verify")
	if err != nil {
		return false, false, err
	}
	useHTTP, err := cmd.Flags().GetBool("use-http")
	if err != nil {
		return false, false, err
	}

	switch {
	case cmd.Flags().Changed("skip-tls") && cmd.Flags().Changed("use-http"):
		return false, false, errors.New("invalid flag combination: cannot use --use-http with --skip-tls")
	case cmd.Flags().Changed("skip-tls") && cmd.Flags().Changed("skip-tls-verify"):
		return false, false, errors.New("invalid flag combination: cannot use --skip-tls-verify with --skip-tls")
	case skipTLSVerify && useHTTP:
		return false, false, errors.New("invalid flag combination: --use-http and --skip-tls-verify cannot both be true")
	default:
		// return use HTTP true if just skipTLS
		// is set for functional parity with existing
		if skipTLS {
			return false, true, nil
		}
		return skipTLSVerify, useHTTP, nil
	}
}

// This works in tandem with opm/index/cmd, which adds the relevant flags as persistent
// as part of the root command (cmd/root/cmd) initialization
func CreateCLIRegistry(cmd *cobra.Command) (image.Registry, error) {
	skipTLSVerify, useHTTP, err := GetTLSOptions(cmd)
	if err != nil {
		return nil, err
	}
	return containersimageregistry.New(
		containersimageregistry.DefaultSystemContext,
		containersimageregistry.WithInsecureSkipTLSVerify(skipTLSVerify || useHTTP),
	)
}

func OpenFileOrStdin(cmd *cobra.Command, args []string) (io.ReadCloser, string, error) {
	if len(args) == 0 || args[0] == "-" {
		return io.NopCloser(cmd.InOrStdin()), "stdin", nil
	}
	reader, err := os.Open(args[0])
	return reader, args[0], err
}
