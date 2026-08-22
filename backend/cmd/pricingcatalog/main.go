// Command pricingcatalog generates and validates AO's reviewed pricing catalog.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/aoagents/agent-orchestrator/backend/internal/pricing/catalogsync"
)

const maxSourceBytes = 32 << 20

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: pricingcatalog <sync|validate> [flags]")
	}
	switch args[0] {
	case "sync":
		return runSync(args[1:], stdout, stderr)
	case "validate":
		return runValidate(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q; usage: pricingcatalog <sync|validate> [flags]", args[0])
	}
}

func runSync(args []string, stdout, stderr io.Writer) (err error) {
	flags := flag.NewFlagSet("sync", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "repository root")
	sourcePath := flags.String("source", "", "pinned LiteLLM model_prices_and_context_window.json")
	revision := flags.String("revision", "", "exact LiteLLM revision SHA")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *sourcePath == "" || *revision == "" {
		return errors.New("sync requires -source and -revision")
	}
	file, err := os.Open(*sourcePath)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
	}()
	source, err := io.ReadAll(io.LimitReader(file, maxSourceBytes+1))
	if err != nil {
		return fmt.Errorf("read source: %w", err)
	}
	if len(source) > maxSourceBytes {
		return fmt.Errorf("source exceeds %d-byte limit", maxSourceBytes)
	}
	result, err := catalogsync.Sync(*root, source, catalogsync.Source{
		Repository: "BerriAI/litellm",
		Revision:   *revision,
		Path:       "model_prices_and_context_window.json",
	})
	if err != nil {
		return err
	}
	if result.Changed {
		_, _ = fmt.Fprintln(stdout, "changed")
	} else {
		_, _ = fmt.Fprintln(stdout, "unchanged")
	}
	return nil
}

func runValidate(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("validate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "repository root")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := catalogsync.Validate(*root); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(stdout, "valid")
	return nil
}
