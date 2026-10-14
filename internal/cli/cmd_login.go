package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rcss-labs/stackstead/internal/client"
	"github.com/rcss-labs/stackstead/internal/version"
)

// newAPIClient builds the typed client for one saved context and, for an
// insecure context, repeats the TLS warning: it is shown on every use.
func newAPIClient(env *Env, g *globalFlags, name string, cx Context) (*client.Client, error) {
	u, err := url.Parse(cx.URL)
	if err != nil {
		return nil, exitf(ExitGeneral, "context %s has an invalid URL %q", name, cx.URL)
	}
	if cx.Insecure {
		fmt.Fprintf(env.Stderr, "warning: TLS verification is disabled for context %s\n", name)
	}
	return client.New(client.Config{
		BaseURL: u, Session: cx.Session, CAFile: cx.CAFile, Insecure: cx.Insecure,
		Timeout: g.Timeout, UserAgent: version.UserAgent(),
	})
}

// apiClient resolves the context (--context, STACKSTEAD_CONTEXT, or the
// current one) and returns a client that carries its session. It warns on
// stderr about a loose contexts file and about an insecure context, and
// fails with ExitAuth when the context has no session.
func apiClient(env *Env, g *globalFlags) (c *client.Client, name string, cx Context, err error) {
	store := newContextStore(env)
	for _, w := range store.Warnings(env) {
		fmt.Fprintf(env.Stderr, "warning: %s\n", w)
	}
	all, err := store.Load()
	if err != nil {
		return nil, "", Context{}, err
	}
	name, cx, err = resolveContext(all, g.Context)
	if err != nil {
		return nil, "", Context{}, err
	}
	if cx.Session == "" {
		return nil, "", Context{}, exitf(ExitAuth, "not logged in to %s (%s): run stackstead login %s", name, cx.URL, cx.URL)
	}
	c, err = newAPIClient(env, g, name, cx)
	if err != nil {
		return nil, "", Context{}, err
	}
	return c, name, cx, nil
}

// withAPI runs fn with the current context's client and routes its error
// through onUnauthorized, so every authenticated command behaves the same.
func withAPI(env *Env, g *globalFlags, fn func(c *client.Client, name string, cx Context) error) error {
	c, name, cx, err := apiClient(env, g)
	if err != nil {
		return err
	}
	return clearIfUnauthorized(env, newContextStore(env), name, cx, fn(c, name, cx))
}

// onUnauthorized turns a 401 into "log in again" (clearing the dead session
// locally) and any other API error into an ExitError with a mapped code and
// text safe to print. Other errors pass through unchanged. Prefer withAPI,
// which also protects a session a concurrent login just saved.
func onUnauthorized(env *Env, store *ContextStore, name string, err error) error {
	return clearIfUnauthorized(env, store, name, Context{}, err)
}

// clearIfUnauthorized is onUnauthorized for a request made with used's
// session: the stored session is cleared only if it is still that one (when
// used has no session, unconditionally).
func clearIfUnauthorized(env *Env, store *ContextStore, name string, used Context, err error) error {
	var ce *client.Error
	if !errors.As(err, &ce) || ce.Status != http.StatusUnauthorized {
		return apiFailure(err)
	}
	target := used.URL
	if uerr := store.Update(func(c *Contexts) error {
		cx, ok := c.Contexts[name]
		if !ok {
			target = ""
			return nil
		}
		if target == "" {
			target = cx.URL
		}
		if used.Session == "" || cx.Session == used.Session {
			cx.Session = ""
			c.Contexts[name] = cx
		}
		return nil
	}); uerr != nil {
		fmt.Fprintf(env.Stderr, "warning: could not clear the saved session: %v\n", uerr)
	}
	msg := fmt.Sprintf("not logged in to %s (%s): run stackstead login %s", name, target, target)
	if target == "" {
		msg = fmt.Sprintf("not logged in to %s: run stackstead login <url>", name)
	}
	return &ExitError{Code: ExitAuth, Err: &cleanError{msg: msg, err: err}}
}

// apiFailure converts a *client.Error into an ExitError whose message has
// its control characters stripped and its field errors listed; the original
// stays reachable for errors.Is. Anything else is returned unchanged.
func apiFailure(err error) error {
	var ee *ExitError
	var ce *client.Error
	if err == nil || errors.As(err, &ee) || !errors.As(err, &ce) {
		return err
	}
	msg := oneLine(ce.Message)
	for _, f := range ce.Fields {
		msg += fmt.Sprintf("\n  %s: %s", oneLine(f.Field), oneLine(f.Message))
	}
	if ce.Status == http.StatusTooManyRequests && ce.RetryAfter > 0 {
		msg += fmt.Sprintf(" (retry in %s)", formatDuration(ce.RetryAfter))
	}
	return &ExitError{Code: codeFor(ce), Err: &cleanError{msg: msg, err: err}}
}

func isLoopbackHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func loginCommand() *Command {
	var username, caFile string
	var passwordStdin, insecure bool
	return &Command{
		Name:    "login",
		Short:   "Log in to a Stackstead server and save the session as a context",
		Usage:   "login <url> [--context NAME] [--username U] [--password-stdin] [--ca-file F] [--insecure]",
		Long:    "The password is read from the terminal, or from the first line of stdin with --password-stdin.",
		MinArgs: 1, MaxArgs: 1,
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&username, "username", "admin", "account name")
			fs.BoolVar(&passwordStdin, "password-stdin", false, "read the password from the first line of stdin")
			fs.StringVar(&caFile, "ca-file", "", "PEM CA bundle that signs the server certificate (stored with the context)")
			fs.BoolVar(&insecure, "insecure", false, "skip TLS verification (stored with the context and warned about on every use)")
		},
		Run: func(ctx context.Context, env *Env, g *globalFlags, args []string) error {
			u, err := url.Parse(args[0])
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return exitf(ExitUsage, "invalid URL %q: use http://host[:port] or https://host[:port]", args[0])
			}
			if u.User != nil {
				return exitf(ExitUsage, "the URL must not contain credentials; the password is prompted for")
			}
			u.RawQuery, u.Fragment = "", ""
			u.Path = strings.TrimRight(u.Path, "/")
			baseURL := u.String()

			name := g.Context
			if name == "" {
				name = defaultContextName(baseURL)
			}
			if err := validateContextName(name); err != nil {
				return err
			}
			if insecure && caFile != "" {
				return exitf(ExitUsage, "--insecure and --ca-file cannot be combined")
			}
			if insecure && u.Scheme == "http" {
				return exitf(ExitUsage, "--insecure only applies to https URLs")
			}
			if caFile != "" {
				abs, err := filepath.Abs(caFile)
				if err != nil {
					return exitf(ExitUsage, "CA file: %v", err)
				}
				if fi, err := os.Stat(abs); err != nil || fi.IsDir() {
					return exitf(ExitUsage, "CA file %s does not exist or is not a file", abs)
				}
				caFile = abs
			}
			if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
				fmt.Fprintf(env.Stderr, "warning: %s is not TLS; the session cookie will be sent in clear text\n", baseURL)
			}
			if insecure {
				fmt.Fprintf(env.Stderr, "warning: TLS verification is disabled for context %s\n", name)
			}

			// Refuse before asking for a password or contacting the server; the
			// check inside Update below still covers a concurrent writer.
			if existing, err := newContextStore(env).Load(); err == nil {
				if err := contextConflict(existing, name, baseURL); err != nil {
					return err
				}
			}

			password, err := readSecret(ctx, env, secretSource{Stdin: passwordStdin}, "Password: ", false)
			if err != nil {
				return err
			}
			c, err := client.New(client.Config{
				BaseURL: u, CAFile: caFile, Insecure: insecure, Timeout: g.Timeout, UserAgent: version.UserAgent(),
			})
			if err != nil {
				return exitf(ExitUsage, "%v", err)
			}
			session, expires, err := c.Login(ctx, username, password)
			var ce *client.Error
			switch {
			case errors.As(err, &ce) && ce.Status == http.StatusUnauthorized:
				return exitf(ExitAuth, "invalid username or password")
			case err != nil:
				return apiFailure(err)
			}
			if err := newContextStore(env).Update(func(all *Contexts) error {
				if all.Contexts == nil {
					all.Contexts = map[string]Context{}
				}
				if err := contextConflict(*all, name, baseURL); err != nil {
					return err
				}
				all.Version = contextsVersion
				all.Contexts[name] = Context{URL: baseURL, Username: username, Session: session, ExpiresAt: expires, CAFile: caFile, Insecure: insecure}
				all.Current = name
				return nil
			}); err != nil {
				return err
			}
			if g.Output == "json" {
				return newPrinter(env, g).JSONValue(struct {
					Context   string    `json:"context"`
					URL       string    `json:"url"`
					Username  string    `json:"username"`
					ExpiresAt time.Time `json:"expiresAt"`
				}{name, baseURL, username, expires})
			}
			fmt.Fprintf(env.Stdout, "logged in to %s (%s) as %s; %s\n", name, baseURL, username, expiryText(expires, env.Now()))
			return nil
		},
	}
}

// contextConflict refuses to repoint an existing context at another server.
func contextConflict(all Contexts, name, baseURL string) error {
	if old, ok := all.Contexts[name]; ok && old.URL != baseURL {
		return exitf(ExitConflict, "context %s already points at %s; use --context <new-name> or stackstead context rm %s first", name, old.URL, name)
	}
	return nil
}

func expiryText(expires, now time.Time) string {
	if expires.IsZero() {
		return "the session has no expiry"
	}
	return "session expires in " + formatDuration(expires.Sub(now))
}

func logoutCommand() *Command {
	return &Command{
		Name:  "logout",
		Short: "Sign out of the current context and forget its session",
		Usage: "logout",
		Run: func(ctx context.Context, env *Env, g *globalFlags, _ []string) error {
			store := newContextStore(env)
			all, err := store.Load()
			if err != nil {
				return err
			}
			name, cx, err := resolveContext(all, g.Context)
			if err != nil {
				return err
			}
			if cx.Session != "" {
				// Best effort: the session is dropped locally whatever the server says.
				c, err := newAPIClient(env, g, name, cx)
				if err == nil {
					err = c.Logout(ctx)
				}
				var ce *client.Error
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if err != nil && !(errors.As(err, &ce) && ce.Status == http.StatusUnauthorized) {
					fmt.Fprintf(env.Stderr, "warning: could not sign out on the server: %s\n", cleanText(err.Error()))
				}
			}
			if err := store.Update(func(all *Contexts) error {
				if cx, ok := all.Contexts[name]; ok {
					cx.Session = ""
					all.Contexts[name] = cx
				}
				return nil
			}); err != nil {
				return err
			}
			fmt.Fprintf(env.Stderr, "logged out of %s\n", name)
			return nil
		},
	}
}

// contextView is what `context ls -o json` prints: never the session itself.
type contextView struct {
	Name      string    `json:"name"`
	Current   bool      `json:"current"`
	URL       string    `json:"url"`
	Username  string    `json:"username"`
	Session   string    `json:"sessionState"` // none | valid | expired
	ExpiresAt time.Time `json:"expiresAt"`
	TLS       string    `json:"tls"` // system | ca:<file> | insecure
}

func sessionState(cx Context, now time.Time) string {
	switch {
	case cx.Session == "":
		return "none"
	case !cx.ExpiresAt.IsZero() && !cx.ExpiresAt.After(now):
		return "expired"
	}
	return "valid"
}

func tlsMode(cx Context) string {
	switch {
	case cx.Insecure:
		return "insecure"
	case cx.CAFile != "":
		return "ca:" + cx.CAFile
	}
	return "system"
}

func contextCommand() *Command {
	return &Command{
		Name:  "context",
		Short: "List and switch saved server contexts",
		Sub: []*Command{
			{
				Name: "ls", Short: "List saved contexts", Usage: "context ls",
				Run: func(_ context.Context, env *Env, g *globalFlags, _ []string) error {
					all, err := newContextStore(env).Load()
					if err != nil {
						return err
					}
					names := make([]string, 0, len(all.Contexts))
					for n := range all.Contexts {
						names = append(names, n)
					}
					sort.Strings(names)
					now := env.Now()
					p := newPrinter(env, g)
					if p.JSON {
						views := make([]contextView, 0, len(names))
						for _, n := range names {
							cx := all.Contexts[n]
							views = append(views, contextView{n, n == all.Current, cx.URL, cx.Username, sessionState(cx, now), cx.ExpiresAt, tlsMode(cx)})
						}
						return p.JSONValue(views)
					}
					rows := make([][]string, 0, len(names))
					for _, n := range names {
						cx := all.Contexts[n]
						mark := ""
						if n == all.Current {
							mark = "*"
						}
						session := sessionState(cx, now)
						switch session {
						case "valid":
							session = "valid"
							if !cx.ExpiresAt.IsZero() {
								session = "valid until " + cx.ExpiresAt.Local().Format("2006-01-02 15:04")
							}
						case "expired":
							session = p.paint("31", session)
						}
						tls := tlsMode(cx)
						switch {
						case cx.Insecure:
							tls = p.paint("31", "INSECURE")
						}
						rows = append(rows, []string{mark, n, cx.URL, cx.Username, session, tls})
					}
					p.Table([]string{"", "name", "url", "user", "session", "tls"}, rows)
					return nil
				},
			},
			{
				Name: "current", Short: "Print the name of the current context", Usage: "context current",
				Run: func(_ context.Context, env *Env, g *globalFlags, _ []string) error {
					all, err := newContextStore(env).Load()
					if err != nil {
						return err
					}
					if all.Current == "" {
						return exitf(ExitNotFound, "no current context: run stackstead login <url>")
					}
					fmt.Fprintln(env.Stdout, all.Current)
					return nil
				},
			},
			{
				Name: "use", Short: "Make a saved context the current one", Usage: "context use <name>", MinArgs: 1, MaxArgs: 1,
				Run: func(_ context.Context, env *Env, g *globalFlags, args []string) error {
					return newContextStore(env).Update(func(all *Contexts) error {
						if _, ok := all.Contexts[args[0]]; !ok {
							return exitf(ExitNotFound, "unknown context %q (see stackstead context ls)", args[0])
						}
						all.Current = args[0]
						return nil
					})
				},
			},
			{
				Name: "rm", Short: "Remove a saved context", Usage: "context rm <name>", MinArgs: 1, MaxArgs: 1,
				Run: func(_ context.Context, env *Env, g *globalFlags, args []string) error {
					return newContextStore(env).Update(func(all *Contexts) error {
						if _, ok := all.Contexts[args[0]]; !ok {
							return exitf(ExitNotFound, "unknown context %q (see stackstead context ls)", args[0])
						}
						delete(all.Contexts, args[0])
						if all.Current == args[0] {
							all.Current = ""
						}
						return nil
					})
				},
			},
		},
	}
}

func whoamiCommand() *Command {
	return &Command{
		Name:  "whoami",
		Short: "Show the account and server of the current context",
		Usage: "whoami",
		Run: func(ctx context.Context, env *Env, g *globalFlags, _ []string) error {
			return withAPI(env, g, func(c *client.Client, name string, cx Context) error {
				me, err := c.Me(ctx)
				if err != nil {
					return err
				}
				if g.Output == "json" {
					return newPrinter(env, g).JSONValue(me)
				}
				fmt.Fprintf(env.Stdout, "%s @ %s (context %s)\n", cleanText(me.Username), cx.URL, name)
				return nil
			})
		},
	}
}

func statusCommand() *Command {
	return &Command{
		Name:  "status",
		Short: "Show the server's Docker, Compose, event and metrics status",
		Usage: "status",
		Run: func(ctx context.Context, env *Env, g *globalFlags, _ []string) error {
			return withAPI(env, g, func(c *client.Client, _ string, _ Context) error {
				st, err := c.Status(ctx)
				if err != nil {
					return err
				}
				clientVersion := version.Get().Version
				if st.Version != "" && st.Version != clientVersion {
					fmt.Fprintf(env.Stderr, "note: server version %s differs from client version %s\n", cleanText(st.Version), clientVersion)
				}
				p := newPrinter(env, g)
				if p.JSON {
					return p.JSONValue(st)
				}
				metrics := "ok"
				if st.MetricsDegraded {
					metrics = "degraded"
				}
				serverVersion := cleanText(st.Version)
				if serverVersion == "" {
					serverVersion = "unknown"
				}
				for _, kv := range [][2]string{
					{"docker", p.Status(cleanText(st.Docker))},
					{"compose", p.Status(cleanText(st.Compose))},
					{"events", p.Status(cleanText(st.Events))},
					{"metrics", p.Status(metrics)},
					{"server version", serverVersion},
					{"client version", clientVersion},
				} {
					fmt.Fprintf(env.Stdout, "%-16s %s\n", kv[0], kv[1])
				}
				return nil
			})
		},
	}
}
