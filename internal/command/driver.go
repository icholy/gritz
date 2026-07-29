package command

import (
	"context"

	"github.com/icholy/gritz/internal/agent"
	"github.com/icholy/gritz/internal/gritzclient"
	"github.com/urfave/cli/v3"
)

var DriverCommand = &cli.Command{
	Name:  "driver",
	Usage: "Run an agent for a task",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:    "server",
			Aliases: []string{"s"},
			Usage:   "server URL",
			Value:   gritzclient.DefaultURL,
		},
		&cli.Int64Flag{
			Name:     "task",
			Aliases:  []string{"t"},
			Usage:    "Task ID to execute",
			Required: true,
		},
		&cli.StringFlag{
			Name:  "token",
			Usage: "Authentication token for the agent",
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		// Open the append-only in-sandbox log: its logger tees the driver's slog
		// output to os.Stderr and /gritz/log, and its sink is teed into setup
		// command and Claude CLI stdio, so a completed run can be inspected
		// post-mortem via the reverse-shell. Opening is best-effort and never
		// fails the run (see agent.OpenDriverLog).
		log := agent.OpenDriverLog(agent.DefaultLogPath)
		defer log.Close()

		driver := &agent.Driver{
			TaskID: cmd.Int64("task"),
			Client: gritzclient.New(gritzclient.Options{
				BaseURL: cmd.String("server"),
				Token:   cmd.String("token"),
			}),
			Log:       log,
			Config:    agent.DefaultConfigStore,
			ServerURL: cmd.String("server"),
			Token:     cmd.String("token"),
		}
		return driver.Run(ctx)
	},
}
