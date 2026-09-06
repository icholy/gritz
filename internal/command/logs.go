package command

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/icholy/gritz/internal/configfile"
	"github.com/icholy/gritz/internal/gritzclient"
	gritzv1 "github.com/icholy/gritz/internal/proto/gritz/v1"
	"github.com/icholy/gritz/internal/x/common"
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
// following — keeps writing appends until ctx is cancelled.
func printTaskLogs(ctx context.Context, w io.Writer, client gritzclient.Client, opts logsOptions) error {
	// The follow cursor comes out of the history walk: it is the tail page's
	// next_page_token, the position everything appended after this point lands
	// past.
	token, err := printLogHistory(ctx, w, client, opts.TaskID)
	if err != nil {
		return err
	}
	if !opts.Follow {
		return nil
	}
	for {
		if !common.SleepContext(ctx, opts.Interval) {
			return nil
		}
		if token == "" {
			// The task has not shipped a single chunk yet, so there is no cursor to
			// poll from — re-open at the tail until the first one lands. Nothing has
			// been printed yet (an empty tail page is what leaves the cursor empty),
			// so this cannot duplicate output.
			if token, err = printLogHistory(ctx, w, client, opts.TaskID); err != nil {
				return err
			}
			continue
		}
		// Drain everything newer than the cursor before sleeping again: a run that
		// logged more than a page's worth between polls must not fall behind.
		for {
			resp, err := listLogChunks(ctx, client, opts.TaskID, token)
			if err != nil {
				return err
			}
			if err := writeChunks(w, resp.GetChunks()); err != nil {
				return err
			}
			// next_page_token walks toward newer rows and is always populated — an
			// empty poll echoes the cursor back — so it is what follow advances on.
			// Hold the old cursor if it ever comes back empty: re-opening at the
			// tail here would replay the transcript that was already printed.
			if next := resp.GetNextPageToken(); next != "" {
				token = next
			}
			// !more, not a short page: at an exact page_size boundary a full page
			// with nothing behind it is indistinguishable by length.
			if !resp.GetMore() {
				break
			}
		}
	}
}

// printLogHistory writes a task's transcript from its beginning to its current
// tail and returns the follow cursor.
//
// The walk runs backwards: an empty page token opens at the tail (the newest
// page), and prev_page_token steps toward older history until it is exhausted.
// Only the tail page's next_page_token is a valid follow cursor — the later
// pages' point back into history — so it is captured on the first response.
func printLogHistory(ctx context.Context, w io.Writer, client gritzclient.Client, taskID int64) (string, error) {
	var (
		follow string
		token  string
		pages  [][]*gritzv1.LogChunk
	)
	for {
		resp, err := listLogChunks(ctx, client, taskID, token)
		if err != nil {
			return "", err
		}
		if token == "" {
			follow = resp.GetNextPageToken()
		}
		pages = append(pages, resp.GetChunks())
		// more reports whether older rows remain; prev_page_token empties at the
		// same point, but check both so a bad page can't spin the walk.
		if !resp.GetMore() || resp.GetPrevPageToken() == "" {
			break
		}
		token = resp.GetPrevPageToken()
	}
	// Chunks within a page are oldest-first, so a page's bytes concatenate
	// directly; the pages themselves arrived newest-first, so unwind them.
	for i := len(pages) - 1; i >= 0; i-- {
		if err := writeChunks(w, pages[i]); err != nil {
			return "", err
		}
	}
	return follow, nil
}

func listLogChunks(ctx context.Context, client gritzclient.Client, taskID int64, token string) (*gritzv1.ListLogChunksByTaskResponse, error) {
	resp, err := client.ListLogChunksByTask(ctx, &gritzv1.ListLogChunksByTaskRequest{
		TaskId:    taskID,
		PageSize:  logPageSize,
		PageToken: token,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list log chunks: %w", err)
	}
	return resp, nil
}

// writeChunks concatenates a page's raw chunk bytes onto w.
func writeChunks(w io.Writer, chunks []*gritzv1.LogChunk) error {
	for _, chunk := range chunks {
		if _, err := w.Write(chunk.GetData()); err != nil {
			return fmt.Errorf("failed to write log output: %w", err)
		}
	}
	return nil
}
