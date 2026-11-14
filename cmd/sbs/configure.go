package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/sandboxstudiosoftware/sandbox-studio-cli/internal/auth"
	"github.com/sandboxstudiosoftware/sandbox-studio-cli/internal/config"
)

// secretReader reads the client secret. It is a package var so tests can inject a
// non-interactive reader; the default reads a hidden terminal prompt.
var secretReader = promptSecret

func newConfigureCmd() *cobra.Command {
	var (
		urlFlag     string
		contextName string
		clientID    string
	)

	cmd := &cobra.Command{
		Use:   "configure",
		Short: "Configure the Sandbox Studio URL and API client credentials",
		Long: "Stores the deployment URL in the config file and, optionally, an API client " +
			"id/secret in the OS keyring. The client secret is read interactively (never " +
			"from a flag) so it does not leak into shell history. Leave the client ID blank " +
			"to use browser login (`sbs login`) instead; an API client is only needed " +
			"for CI and scripts.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			name := contextName
			if name == "" {
				name = "default"
			}

			// Prompt for URL if not supplied.
			url := urlFlag
			if url == "" {
				url = prompt("Sandbox Studio URL (e.g. https://sandbox.example.com): ")
			}
			url = strings.TrimSpace(url)
			if url == "" {
				return fmt.Errorf("a URL is required")
			}

			// The API client is optional: without one, API calls use the browser
			// login (`sbs login`). Leave it blank to rely on that.
			if clientID == "" {
				clientID = prompt("API client ID (leave blank to use browser login instead): ")
			}
			clientID = strings.TrimSpace(clientID)

			var secret string
			if clientID != "" {
				// Always read the secret from a hidden prompt (injectable for tests).
				secret, err = secretReader("API client secret: ")
				if err != nil {
					return err
				}
				if secret == "" {
					return fmt.Errorf("a client secret is required")
				}
			}

			// Persist non-secret config.
			ctx := cfg.Contexts[name]
			ctx.URL = url
			cfg.Contexts[name] = ctx
			if cfg.CurrentContext == "" {
				cfg.CurrentContext = name
			}
			if err := cfg.Save(); err != nil {
				return err
			}

			// Persist secrets to the keyring (only when an API client was given;
			// otherwise any existing stored client is left untouched).
			if clientID != "" {
				kr := newKeyring()
				if err := kr.Set(auth.KeyClientID, clientID); err != nil {
					return fmt.Errorf("storing client id in keyring: %w", err)
				}
				if err := kr.Set(auth.KeyClientSecret, secret); err != nil {
					return fmt.Errorf("storing client secret in keyring: %w", err)
				}
			}

			if flagOutput == "json" {
				return printJSON(map[string]any{"context": name, "url": url, "apiClientConfigured": clientID != ""})
			}
			fmt.Fprintf(os.Stderr, "Configured context %q for %s\n", name, url)
			if clientID == "" {
				fmt.Fprintln(os.Stderr, "No API client set: run `sbs login` to sign in with your browser.")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&urlFlag, "url", "", "Sandbox Studio URL")
	cmd.Flags().StringVar(&contextName, "name", "", "context name (default \"default\")")
	cmd.Flags().StringVar(&clientID, "client-id", "", "API client ID")
	return cmd
}

// prompt writes a message to stderr and reads a line from stdin.
func prompt(msg string) string {
	fmt.Fprint(os.Stderr, msg)
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	return strings.TrimRight(line, "\r\n")
}

// promptSecret reads a line without echoing it to the terminal.
func promptSecret(msg string) (string, error) {
	fmt.Fprint(os.Stderr, msg)
	b, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
