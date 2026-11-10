// Package cli wires the command tree and the process entry point.
package cli

import (
	"context"
	stderrors "errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bborbe/errors"
	"github.com/spf13/cobra"

	"github.com/bborbe/git-ai-sync/internal/clock"
	"github.com/bborbe/git-ai-sync/internal/config"
	"github.com/bborbe/git-ai-sync/internal/conflict"
	"github.com/bborbe/git-ai-sync/internal/git"
	"github.com/bborbe/git-ai-sync/internal/launchd"
	"github.com/bborbe/git-ai-sync/internal/lock"
	"github.com/bborbe/git-ai-sync/internal/logging"
	"github.com/bborbe/git-ai-sync/internal/supervise"
	"github.com/bborbe/git-ai-sync/internal/sync"
	"github.com/bborbe/git-ai-sync/internal/version"
	"github.com/bborbe/git-ai-sync/internal/watch"
)

// Execute wires signal handling, runs the CLI, and exits non-zero on error.
func Execute() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := Run(ctx, os.Args[1:]); err != nil {
		if stderrors.Is(err, context.Canceled) {
			return
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// Run parses args and runs the selected command.
func Run(ctx context.Context, args []string) error {
	rootCmd := newRootCommand(ctx)
	rootCmd.SetArgs(args)
	return rootCmd.ExecuteContext(ctx)
}

// newRootCommand builds the cobra command tree. Tests use it to capture --version output.
func newRootCommand(ctx context.Context) *cobra.Command {
	rootCmd := &cobra.Command{
		Use:          "git-ai-sync",
		Short:        "Automatic Git repository sync",
		SilenceUsage: true,
		Version:      version.Version,
	}
	rootCmd.SetVersionTemplate("git-ai-sync {{.Version}}\n")

	rootCmd.AddCommand(newWatchCommand(ctx))
	rootCmd.AddCommand(newSyncCommand(ctx))
	rootCmd.AddCommand(newResolveCommand(ctx))
	rootCmd.AddCommand(newSuperviseCommand(ctx))
	rootCmd.AddCommand(newInstallCommand(ctx))
	rootCmd.AddCommand(newUninstallCommand(ctx))
	rootCmd.AddCommand(newStatusCommand(ctx))
	return rootCmd
}

func addConfigFlag(cmd *cobra.Command) {
	cmd.Flags().String("config", "", "Path to the config file")
}

func fileOptions(ctx context.Context, cmd *cobra.Command) (config.FileOptions, error) {
	var opts config.FileOptions
	if cmd.Flags().Changed("config") {
		value, err := cmd.Flags().GetString("config")
		if err != nil {
			return opts, errors.Wrapf(ctx, err, "read --config")
		}
		opts.ConfigPath = &value
	}
	if cmd.Flags().Changed("interval") {
		value, err := cmd.Flags().GetInt("interval")
		if err != nil {
			return opts, errors.Wrapf(ctx, err, "read --interval")
		}
		opts.Interval = &value
	}
	if cmd.Flags().Changed("strategy") {
		value, err := cmd.Flags().GetString("strategy")
		if err != nil {
			return opts, errors.Wrapf(ctx, err, "read --strategy")
		}
		opts.Strategy = &value
	}
	if cmd.Flags().Changed("log-level") {
		value, err := cmd.Flags().GetString("log-level")
		if err != nil {
			return opts, errors.Wrapf(ctx, err, "read --log-level")
		}
		opts.LogLevel = &value
	}
	return opts, nil
}

func newSuperviseCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "supervise",
		Short: "Run one sync loop per configured vault",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			runCtx := commandContext(ctx, cmd)
			opts, err := fileOptions(runCtx, cmd)
			if err != nil {
				return errors.Wrapf(runCtx, err, "read config options")
			}
			cfg, err := config.LoadFile(runCtx, opts)
			if err != nil {
				return errors.Wrapf(runCtx, err, "load config file")
			}
			logger := logging.New(cmd.ErrOrStderr(), cfg.LogLevel)
			return supervise.Run(runCtx, supervise.Options{Config: cfg, Logger: logger, Clock: clock.New()})
		},
	}
	addConfigFlag(cmd)
	cmd.Flags().Int("interval", -1, "Sync interval in seconds")
	cmd.Flags().String("strategy", "", "Pull strategy: merge or rebase")
	cmd.Flags().String("log-level", "", "Logging level")
	return cmd
}

func newInstallCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install and load the supervisor launchd agent",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			runCtx := commandContext(ctx, cmd)
			opts, err := fileOptions(runCtx, cmd)
			if err != nil {
				return errors.Wrapf(runCtx, err, "read config options")
			}
			cfg, err := config.LoadFile(runCtx, opts)
			if err != nil {
				return errors.Wrapf(runCtx, err, "load config file")
			}
			logger := logging.New(cmd.ErrOrStderr(), cfg.LogLevel)
			return launchd.Install(runCtx, launchd.Options{Config: cfg, Logger: logger})
		},
	}
	addConfigFlag(cmd)
	return cmd
}

func newUninstallCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Boot out and remove the supervisor launchd agent",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			runCtx := commandContext(ctx, cmd)
			opts, err := fileOptions(runCtx, cmd)
			if err != nil {
				return errors.Wrapf(runCtx, err, "read config options")
			}
			cfg, err := config.LoadFile(runCtx, opts)
			if err != nil {
				return errors.Wrapf(runCtx, err, "load config file")
			}
			logger := logging.New(cmd.ErrOrStderr(), cfg.LogLevel)
			return launchd.Uninstall(runCtx, launchd.Options{Config: cfg, Logger: logger})
		},
	}
	addConfigFlag(cmd)
	return cmd
}

func newStatusCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Report the configured vaults, metrics freshness and agent state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			runCtx := commandContext(ctx, cmd)
			opts, err := fileOptions(runCtx, cmd)
			if err != nil {
				return errors.Wrapf(runCtx, err, "read config options")
			}
			cfg, err := config.LoadFile(runCtx, opts)
			if err != nil {
				return errors.Wrapf(runCtx, err, "load config file")
			}
			logger := logging.New(cmd.ErrOrStderr(), cfg.LogLevel)
			out := cmd.OutOrStdout()

			for _, vault := range cfg.Vaults {
				fmt.Fprintf(out, "vault: %s\n", vault)
			}

			state, err := supervise.ReadState(runCtx, cfg.StateFile)
			if err != nil {
				if stderrors.Is(err, fs.ErrNotExist) {
					fmt.Fprintln(out, "last metrics push: never")
				} else {
					return errors.Wrapf(runCtx, err, "read state file")
				}
			} else if state.LastMetricsPush.IsZero() {
				fmt.Fprintln(out, "last metrics push: never")
			} else {
				age := time.Since(state.LastMetricsPush)
				if age < 0 {
					age = 0
				}
				fmt.Fprintf(out, "last metrics push: %s ago\n", age.Round(time.Second))
			}

			loaded, err := launchd.IsLoaded(runCtx, launchd.Options{Config: cfg, Logger: logger})
			if err != nil {
				return errors.Wrapf(runCtx, err, "check launchd agent")
			}
			if !loaded {
				fmt.Fprintln(out, "agent: not loaded")
				return errors.Errorf(runCtx, "launchd agent %s is not loaded", launchd.Label)
			}
			fmt.Fprintln(out, "agent: loaded")
			return nil
		},
	}
	addConfigFlag(cmd)
	return cmd
}

func newWatchCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "watch [path]",
		Short: "Start watching and syncing",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := config.Flags{}
			if cmd.Flags().Changed("interval") {
				value, err := cmd.Flags().GetInt("interval")
				if err != nil {
					return err
				}
				flags.Interval = &value
			}
			if cmd.Flags().Changed("strategy") {
				value, err := cmd.Flags().GetString("strategy")
				if err != nil {
					return err
				}
				flags.Strategy = &value
			}
			if cmd.Flags().Changed("log-level") {
				value, err := cmd.Flags().GetString("log-level")
				if err != nil {
					return err
				}
				flags.LogLevel = &value
			}
			cfg, err := config.Load(commandContext(ctx, cmd), flags)
			if err != nil {
				return err
			}
			logger := logging.New(os.Stderr, cfg.LogLevel)
			return watch.Run(commandContext(ctx, cmd), watch.Options{
				Path:         pathArg(args),
				Interval:     cfg.Interval,
				MaxWait:      cfg.MaxWait,
				CommitPrefix: cfg.CommitPrefix,
				Strategy:     git.Strategy(cfg.Strategy),
				Logger:       logger,
			})
		},
	}
	cmd.Flags().Int("interval", -1, "Sync interval in seconds")
	cmd.Flags().String("strategy", "", "Pull strategy: merge or rebase")
	cmd.Flags().String("log-level", "", "Logging level")
	return cmd
}

func newSyncCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync [path]",
		Short: "Run sync once",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := config.Flags{}
			if cmd.Flags().Changed("strategy") {
				value, err := cmd.Flags().GetString("strategy")
				if err != nil {
					return err
				}
				flags.Strategy = &value
			}
			if cmd.Flags().Changed("log-level") {
				value, err := cmd.Flags().GetString("log-level")
				if err != nil {
					return err
				}
				flags.LogLevel = &value
			}
			runCtx := commandContext(ctx, cmd)
			cfg, err := config.Load(runCtx, flags)
			if err != nil {
				return err
			}
			logger := logging.New(os.Stderr, cfg.LogLevel)

			repo, err := git.FindRepo(runCtx, pathArg(args))
			if err != nil {
				return err
			}
			l, err := lock.Acquire(runCtx, repo)
			if err != nil {
				return err
			}
			defer func() {
				if releaseErr := l.Release(runCtx); releaseErr != nil {
					logger.Warn(fmt.Sprintf("failed to release lock: %v", releaseErr))
				}
			}()

			result, err := sync.Run(runCtx, repo, sync.Options{
				Strategy:      git.Strategy(cfg.Strategy),
				CommitPrefix:  cfg.CommitPrefix,
				PullWhenClean: false,
				Clock:         clock.New(),
			})
			if err != nil {
				logger.Error(fmt.Sprintf("sync failed: %v", err))
				return err
			}
			if result.NoChanges {
				logger.Info("no changes to sync")
				return nil
			}
			if result.CommitErr != nil {
				logger.Error(fmt.Sprintf("commit refused: %v", result.CommitErr))
				return result.CommitErr
			}
			logger.Info("sync completed")
			return nil
		},
	}
	cmd.Flags().String("strategy", "", "Pull strategy: merge or rebase")
	cmd.Flags().String("log-level", "", "Logging level")
	return cmd
}

func newResolveCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resolve [path]",
		Short: "Resolve merge or rebase conflicts with Claude and push",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runCtx := commandContext(ctx, cmd)
			logger := logging.New(cmd.ErrOrStderr(), slog.LevelInfo)

			repo, err := git.FindRepo(runCtx, pathArg(args))
			if err != nil {
				return errors.Wrapf(runCtx, err, "find repository")
			}
			l, err := lock.Acquire(runCtx, repo)
			if err != nil {
				return errors.Wrapf(runCtx, err, "acquire repository lock")
			}
			defer func() {
				if releaseErr := l.Release(runCtx); releaseErr != nil {
					logger.Warn(fmt.Sprintf("failed to release lock: %v", releaseErr))
				}
			}()

			inRebase := git.IsInRebase(repo)
			inMerge := git.IsInMerge(repo)
			if !inRebase && !inMerge {
				return errors.Errorf(runCtx, "not in a merge or rebase conflict state: %s", repo)
			}
			files, err := git.ConflictedFiles(runCtx, repo)
			if err != nil {
				return errors.Wrapf(runCtx, err, "list conflicted files")
			}
			if len(files) == 0 {
				state := "merge"
				if inRebase {
					state = "rebase"
				}
				return errors.Errorf(runCtx, "no conflicted files found in the %s state", state)
			}
			logger.Info(fmt.Sprintf("found %d conflicted file(s): %s", len(files), strings.Join(files, ", ")))

			failed := make([]string, 0)
			for _, path := range files {
				if err := conflict.ResolveFile(runCtx, repo, path); err != nil {
					logger.Error(fmt.Sprintf("failed to resolve %s: %v", path, err))
					failed = append(failed, path)
					continue
				}
				logger.Info(fmt.Sprintf("resolved and staged %s", path))
			}
			if len(failed) > 0 {
				return errors.Errorf(runCtx, "failed to resolve %d file(s): %s", len(failed), strings.Join(failed, ", "))
			}

			if inRebase {
				if err := git.ContinueRebase(runCtx, repo); err != nil {
					return continuationError(runCtx, repo, "rebase", err)
				}
			} else {
				if err := git.ContinueMerge(runCtx, repo); err != nil {
					return continuationError(runCtx, repo, "merge", err)
				}
			}

			if err := git.Push(runCtx, repo); err != nil {
				return errors.Wrapf(runCtx, err, "push after resolving conflicts")
			}
			logger.Info("resolved all conflicts, continued, and pushed")
			return nil
		},
	}
	return cmd
}

func continuationError(ctx context.Context, repo string, kind string, cause error) error {
	conflicted, listErr := git.ConflictedFiles(ctx, repo)
	if listErr == nil && len(conflicted) > 0 {
		return errors.Wrapf(ctx, cause, "continuation failed - still have conflicts in: %s", strings.Join(conflicted, ", "))
	}
	return errors.Wrapf(ctx, cause, "continue %s", kind)
}

func commandContext(fallback context.Context, cmd *cobra.Command) context.Context {
	if cmd.Context() != nil {
		return cmd.Context()
	}
	return fallback
}

func pathArg(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return "."
}
