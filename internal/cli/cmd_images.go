package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"strings"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/client"
)

// buildArgFlag collects repeated --build-arg K=V values.
type buildArgFlag map[string]string

func (b buildArgFlag) String() string { return "" }

func (b buildArgFlag) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok || k == "" {
		return fmt.Errorf("build arg %q must be KEY=VALUE", v)
	}
	b[k] = val
	return nil
}

func imagesCommand() *Command {
	return &Command{Name: "images", Short: "List, build and push library images", Sub: []*Command{
		imagesListCommand(), imagesBuildCommand(), imagesPushCommand(),
	}}
}

func imagesListCommand() *Command {
	return &Command{Name: "ls", Short: "List library images, including those owned by paired servers", Usage: "images ls [-o table|json]",
		Run: func(ctx context.Context, env *Env, g *globalFlags, _ []string) error {
			return withAPI(env, g, func(c *client.Client, _ string, _ Context) error {
				lib, err := c.Images(ctx)
				if err != nil {
					return imagesError(err)
				}
				p := newPrinter(env, g)
				if p.JSON {
					return p.JSONValue(lib)
				}
				rows := make([][]string, 0, len(lib.Images))
				for _, img := range lib.Images {
					latest, last := "-", "-"
					if img.LatestVersion != nil {
						latest = dashIfEmpty(oneLine(img.LatestVersion.Tag))
					}
					if img.LastBuild != nil {
						last = oneLine(img.LastBuild.Status) + " " + relativeTime(img.LastBuild.StartedAt, env.Now())
					}
					targets := make([]string, 0, len(img.PushTargets))
					for _, t := range img.PushTargets {
						targets = append(targets, oneLine(t.RegistryName))
					}
					rows = append(rows, []string{oneLine(img.Slug), dashIfEmpty(oneLine(img.Source)), dashIfEmpty(oneLine(img.Owner.Name)),
						latest, last, dashIfEmpty(strings.Join(targets, ", "))})
				}
				p.Table([]string{"name", "source", "owner", "latest", "last build", "push targets"}, rows)
				return nil
			})
		}}
}

func imagesBuildCommand() *Command {
	var tag, ref string
	var detach bool
	var flags *flag.FlagSet
	args := buildArgFlag{}
	return &Command{Name: "build", Short: "Build a new version of a library image and follow the operation",
		Usage: "images build <image> [--tag T] [--build-arg K=V]... [--ref REF] [--detach]", MinArgs: 1, MaxArgs: 1,
		Long: "The tag defaults to the server's suggestion (the latest tag plus one). --ref builds a GitHub image at a branch, tag or commit; the server refuses it for upload images.",
		Flags: func(fs *flag.FlagSet) {
			flags = fs
			fs.StringVar(&tag, "tag", "", "version tag (default: the suggested next tag)")
			fs.Var(args, "build-arg", "build argument KEY=VALUE (repeatable)")
			fs.StringVar(&ref, "ref", "", "GitHub branch, tag or commit to build")
			fs.BoolVar(&detach, "detach", false, "print the operation id and return without waiting")
		},
		Run: func(ctx context.Context, env *Env, g *globalFlags, pos []string) error {
			slug := pos[0]
			tagGiven := false
			flags.Visit(func(f *flag.Flag) { tagGiven = tagGiven || f.Name == "tag" })
			if tagGiven && tag == "" {
				return exitf(ExitUsage, "--tag is empty: pass a tag or omit --tag to use the suggested one")
			}
			return withAPI(env, g, func(c *client.Client, _ string, _ Context) error {
				if tag == "" {
					detail, err := c.Image(ctx, slug)
					if err != nil {
						return imagesError(err)
					}
					if detail.SuggestedTag == "" {
						msg := fmt.Sprintf("no tag suggestion for %s: pass --tag", slug)
						if detail.TagHint != "" {
							msg += fmt.Sprintf(" (latest tag is %q)", detail.TagHint)
						}
						return exitf(ExitUsage, "%s", oneLine(msg))
					}
					tag = detail.SuggestedTag
				}
				var buildArgs map[string]string
				if len(args) > 0 {
					buildArgs = map[string]string(args)
				}
				started, err := c.BuildImage(ctx, slug, apitypes.BuildRequest{Tag: tag, BuildArgs: buildArgs, Ref: ref})
				if err != nil {
					return imagesError(err)
				}
				return startAndFollow(ctx, env, g, c, started.Operation, detach)
			})
		}}
}

func imagesPushCommand() *Command {
	var target string
	var detach bool
	return &Command{Name: "push", Short: "Push a built image version to a registry and follow the operation",
		Usage: "images push <image> <tag> [--target REGISTRY/REPOSITORY|ID] [--detach]", MinArgs: 2, MaxArgs: 2,
		Long: "--target selects one of the image's push targets by REGISTRY/REPOSITORY or by target ID; a bare registry name works when it matches one target. It is required when the image has more than one target.",
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&target, "target", "", "push target: REGISTRY/REPOSITORY, target ID, or a registry name with a single target")
			fs.BoolVar(&detach, "detach", false, "print the operation id and return without waiting")
		},
		Run: func(ctx context.Context, env *Env, g *globalFlags, pos []string) error {
			slug, tag := pos[0], pos[1]
			return withAPI(env, g, func(c *client.Client, _ string, _ Context) error {
				detail, err := c.Image(ctx, slug)
				if err != nil {
					return imagesError(err)
				}
				id, err := pickPushTarget(detail.PushTargets, target, slug)
				if err != nil {
					return err
				}
				op, err := c.PushImageVersion(ctx, slug, tag, id)
				if err != nil {
					return imagesError(err)
				}
				return startAndFollow(ctx, env, g, c, op, detach)
			})
		}}
}

// pickPushTarget resolves --target to a push target ID. It accepts
// REGISTRY/REPOSITORY, the target ID, or a bare registry name that matches
// exactly one target (one registry can hold several repositories).
func pickPushTarget(targets []apitypes.PushTarget, name, slug string) (string, error) {
	options := make([]string, 0, len(targets))
	var exact, byRegistry []apitypes.PushTarget
	for _, t := range targets {
		full := t.RegistryName + "/" + t.Repository
		options = append(options, oneLine(full)+" ("+oneLine(t.ID)+")")
		if name == "" {
			continue
		}
		if t.ID == name || full == name {
			exact = append(exact, t)
		}
		if t.RegistryName == name {
			byRegistry = append(byRegistry, t)
		}
	}
	list := strings.Join(options, ", ")
	switch {
	case len(targets) == 0:
		return "", exitf(ExitUsage, "image %s has no push targets", oneLine(slug))
	case name == "" && len(targets) == 1:
		return targets[0].ID, nil
	case name == "":
		return "", exitf(ExitUsage, "image %s has several push targets: pass --target one of %s", oneLine(slug), list)
	case len(exact) == 1:
		return exact[0].ID, nil
	case len(exact) == 0 && len(byRegistry) == 1:
		return byRegistry[0].ID, nil
	case len(exact) == 0 && len(byRegistry) > 1:
		return "", exitf(ExitUsage, "image %s has several push targets in registry %q: pass --target one of %s", oneLine(slug), oneLine(name), list)
	case len(exact) == 0:
		return "", exitf(ExitUsage, "image %s has no push target %q: use one of %s", oneLine(slug), oneLine(name), list)
	}
	return "", exitf(ExitUsage, "image %s has several push targets matching %q: pass --target one of %s", oneLine(slug), oneLine(name), list)
}

// imagesError gives image-library failures their exit codes, with the
// server's message: 409 is a conflict, 502 (an unreachable peer or GitHub)
// is "unavailable". 429 already exits 8 and validation errors exit 6.
func imagesError(err error) error {
	var ce *client.Error
	if !errors.As(err, &ce) {
		return err
	}
	switch ce.Status {
	case http.StatusConflict:
		return conflictError(err, oneLine(ce.Message))
	case http.StatusBadGateway:
		return &ExitError{Code: ExitUnavailable, Err: &cleanError{msg: oneLine(ce.Message), err: err}}
	}
	return err
}
