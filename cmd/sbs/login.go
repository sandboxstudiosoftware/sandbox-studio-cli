package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/sandboxstudiosoftware/sandbox-studio-cli/internal/auth"
	"github.com/sandboxstudiosoftware/sandbox-studio-cli/internal/output"
)

func newCredsCmd() *cobra.Command {
	var lease, role string

	cmd := &cobra.Command{
		Use:   "creds",
		Short: "Print temporary AWS credentials (AWS credential_process format)",
		Long: "Fetches temporary credentials for a lease and prints them as the JSON " +
			"object AWS expects from a credential_process entry. Intended for use in " +
			"~/.aws/config:\n\n  [profile my-lease]\n  credential_process = sbs creds --lease <id>\n\n" +
			"Sandbox Studio issues the credentials server-side; they last the lease " +
			"template's session duration (up to 12h) for owners, team members and " +
			"managers/admins alike. They are cached in the OS keyring until 5 minutes " +
			"before they expire, and the AWS CLI calls this again for fresh ones.\n\n" +
			"Requires a session from `sbs login` (or an API client); when the session " +
			"has expired, run `sbs login` again. Only the JSON object is written to " +
			"stdout; everything else goes to stderr.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cctx, a, err := resolve()
			if err != nil {
				return err
			}
			leaseID := resolveLease(lease, cctx)
			if leaseID == "" {
				return fmt.Errorf("no lease specified: pass --lease <id> or set a default lease")
			}
			r, err := normalizeRole(role)
			if err != nil {
				return err
			}
			pc, err := leaseCredentials(context.Background(), a, leaseID, r)
			if err != nil {
				return err
			}
			// stdout = credentials only.
			return pc.Write(os.Stdout)
		},
	}

	cmd.Flags().StringVar(&lease, "lease", "", "lease ID (defaults to the context's default lease)")
	cmd.Flags().StringVar(&role, "role", "USER", roleFlagHelp)
	return cmd
}

func newLoginCmd() *cobra.Command {
	var (
		lease   string
		role    string
		console bool
	)

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log in to Sandbox Studio, or to a sandbox account",
		Long: "With no --lease, opens the Sandbox Studio sign-in page in your browser " +
			"(email/password/MFA, or your identity provider). The login is exchanged for " +
			"a Sandbox Studio API session, which is stored in the OS keyring and used by " +
			"every other command. There is no refresh: when the session expires, run " +
			"`sbs login` again.\n\n" +
			"With --lease, prints temporary credentials like `creds`; with --console it " +
			"opens the AWS console for the lease right away. The sign-in link is only " +
			"valid for about 15 minutes; the console session then lasts the lease " +
			"template's session duration.\n\n" +
			"The CLI never talks to Cognito or AWS STS directly: Sandbox Studio issues " +
			"the credentials and console link.",
		Example: "  sbs login\n" +
			"  sbs login --lease 3f2a9c1e-... --console\n" +
			"  sbs login --lease 3f2a9c1e-... --role ADMIN --console",
		RunE: func(cmd *cobra.Command, args []string) error {
			cctx, a, err := resolve()
			if err != nil {
				return err
			}
			r, err := normalizeRole(role)
			if err != nil {
				return err
			}

			leaseID := resolveLease(lease, cctx)
			// No lease target: interactive browser login.
			if leaseID == "" && !cmd.Flags().Changed("lease") {
				if console {
					return fmt.Errorf("--console needs a lease: pass --lease <id> or set a default lease")
				}
				return runLogin(a)
			}
			if leaseID == "" {
				return fmt.Errorf("no lease specified: pass --lease <id> or set a default lease")
			}

			ctx := context.Background()
			if console {
				url, err := leaseConsoleURL(ctx, a, leaseID, r)
				if err != nil {
					return err
				}
				return launchBrowser(url)
			}

			pc, err := leaseCredentials(ctx, a, leaseID, r)
			if err != nil {
				return err
			}
			return pc.Write(os.Stdout)
		},
	}

	cmd.Flags().StringVar(&lease, "lease", "", "lease ID (defaults to the context's default lease)")
	cmd.Flags().StringVar(&role, "role", "USER", roleFlagHelp)
	cmd.Flags().BoolVar(&console, "console", false, "open the AWS console for the lease instead of printing credentials")
	return cmd
}

// runLogin performs `sbs login` with no lease: the branded browser login plus the
// API session exchange. Cached lease credentials from a previous login are dropped
// so they cannot outlive (or belong to a different user than) the new session.
func runLogin(a *auth.Authenticator) error {
	ctx, cancel := context.WithTimeout(context.Background(), loginTimeout)
	defer cancel()
	sess, err := a.Login(ctx)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("timed out after %s waiting for the browser login", loginTimeout)
		}
		return err
	}
	credsCache(a).Clear()

	if flagOutput == "json" {
		return printJSON(map[string]any{
			"url": sess.URL, "loggedIn": true, "expiresAt": sess.ExpiresAt.Format(time.RFC3339),
		})
	}
	fmt.Fprintf(os.Stderr, "%s Logged in to %s %s\n", output.Green("✓"), sess.URL,
		output.Faint(fmt.Sprintf("(session expires %s, in %s)",
			sess.ExpiresAt.Local().Format("2006-01-02 15:04 MST"),
			time.Until(sess.ExpiresAt).Round(time.Minute))))
	return nil
}

// loginTimeout bounds how long `sbs login` waits for the browser to come back.
var loginTimeout = 10 * time.Minute

// leaseConsoleURL returns a fresh AWS console sign-in link for a lease. It always
// calls POST /access (never the credentials cache): the sign-in token inside the
// link is only valid for about 15 minutes.
func leaseConsoleURL(ctx context.Context, a *auth.Authenticator, leaseID, role string) (string, error) {
	client, err := a.Client(ctx)
	if err != nil {
		return "", err
	}
	access, err := leaseAccess(ctx, a, client, leaseID, role)
	if err != nil {
		return "", err
	}
	if access.ConsoleURL == "" {
		return "", fmt.Errorf("no console URL returned for lease %s", leaseID)
	}
	return access.ConsoleURL, nil
}

// launchBrowser opens url, falling back to printing it on stderr/stdout failure.
func launchBrowser(url string) error {
	if flagOutput == "json" {
		err := openBrowser(url)
		return printJSON(map[string]any{"url": url, "opened": err == nil})
	}
	if err := openBrowser(url); err != nil {
		fmt.Fprintln(os.Stderr, "open this URL in your browser within about 15 minutes:")
		fmt.Println(url)
		return nil
	}
	fmt.Fprintln(os.Stderr, "Opened the AWS console in your browser.")
	fmt.Fprintln(os.Stderr, output.Faint("The sign-in link is valid for about 15 minutes; the console session then lasts the template's session duration."))
	return nil
}

// openBrowser opens url using the platform's default handler. It is a package var
// so tests can intercept it instead of spawning a real browser process.
var openBrowser = func(url string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd = "rundll32"
		args = []string{"url.dll,FileProtocolHandler"}
	default:
		cmd = "xdg-open"
	}
	args = append(args, url)
	return exec.Command(cmd, args...).Start()
}
