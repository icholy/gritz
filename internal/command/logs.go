package command

import (
	"context"
	"fmt"
	"io"
	"iter"
	"os"
	"strconv"
	"time"

	"github.com/icholy/gritz/internal/configfile"
	"github.com/icholy/gritz/internal/gritzclient"
	gritzv1 "github.com/icholy/gritz/internal/proto/gritz/v1"
	"github.com/urfave/cli/v3"
)

// logPageSize bounds each ListLogChunksByTask page the log walk fetches. Chunks
// are cut at 32 KiB by the driver's shipper, so a page is a few MiB worst case.
const logPageSize = 50

// logPollInterval is the default --follow poll interval. The shipper cuts a
// chunk at most every 2s when a run is quiet, so polling faster than that only
// buys empty responses. Follow is deliberately poll-based in v1 — see the
// live-tail open question in proposals/draft/ship-driver-logs-to-server.md.
const logPollInterval = 2 * time.Second

// LogsCommand prints a task's driver log — the same byte stream the driver tees
// to /gritz/log inside the sandbox, mirrored to the server by agent.LogShipper.
// It reads the transcript over ListLogChunksByTask rather than shelling out to
// `docker logs`, so it works from anywhere the CLI can reach the server, for any
// task the caller can read, including tasks whose sandbox is long gone.
var LogsCommand = &cli.Command{
	Name:      "logs",
	Usage:     "Display the driver log for a task",
	ArgsUsage: "<task-id>",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:    "server",
			Aliases: []string{"s"},
			Usage:   "server URL",
			Value:   gritzclient.DefaultURL,
			Sources: cli.EnvVars("GRITZ_SERVER"),
		},
		&cli.BoolFlag{
			Name:    "follow",
			Aliases: []string{"f"},
			Usage:   "Follow log output",
		},
		&cli.DurationFlag{
			Name:  "interval",
			Usage: "Poll interval when following",
			Value: logPollInterval,
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		if cmd.NArg() < 1 {
			return cli.Exit("task ID required", 1)
		}
		taskID, err := strconv.ParseInt(cmd.Args().First(), 10, 64)
		if err != nil {
			return cli.Exit("invalid task ID: "+cmd.Args().First(), 1)
		}
		cfg, err := configfile.Load(nil)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		if cfg.Token == "" {
			return fmt.Errorf("not authenticated, run setup first")
		}
		client := gritzclient.New(gritzclient.Options{BaseURL: cmd.String("server"), Token: cfg.Token})
		return printTaskLogs(ctx, os.Stdout, client, logsOptions{
			TaskID:   taskID,
			Follow:   cmd.Bool("follow"),
			Interval: cmd.Duration("interval"),
		})
	},
}

// logsOptions are the printTaskLogs knobs the command's flags map onto.
type logsOptions struct {
	TaskID   int64
	Follow   bool
	Interval time.Duration
}

// printTaskLogs writes a task's whole log transcript to w, then — when
// following — keeps writing appends until ctx is cancelled. The cursor rules
// behind both walks live in gritzclient.TaskLog; this is the presentation half.
func printTaskLogs(ctx context.Context, w io.Writer, client gritzclient.Client, opts logsOptions) error {
	taskLog := gritzclient.OpenTaskLog(client, opts.TaskID, logPageSize)
	if err := writeChunks(w, taskLog.History(ctx)); err != nil {
		return err
	}
	if !opts.Follow {
		return nil
	}
	return writeChunks(w, taskLog.Follow(ctx, opts.Interval))
}

// writeChunks concatenates the raw bytes of an iterator's chunks onto w.
func writeChunks(w io.Writer, chunks iter.Seq2[*gritzv1.LogChunk, error]) error {
	for chunk, err := range chunks {
		if err != nil {
			return fmt.Errorf("failed to list log chunks: %w", err)
		}
		if _, err := w.Write(chunk.GetData()); err != nil {
			return fmt.Errorf("failed to write log output: %w", err)
		}
	}
	return nil
}
