package command

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/urfave/cli/v3"
)

var LogsCommand = &cli.Command{
	Name:      "logs",
	Usage:     "Display container logs for a task",
	ArgsUsage: "<task-id>",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:    "follow",
			Aliases: []string{"f"},
			Usage:   "Follow log output",
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		if cmd.NArg() < 1 {
			return fmt.Errorf("task ID is required")
		}
		taskID := cmd.Args().First()
		containerName := "gritz-" + taskID

		args := []string{"logs"}
		if cmd.Bool("follow") {
			args = append(args, "-f")
		}
		args = append(args, containerName)

		c := exec.CommandContext(ctx, "docker", args...)
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		return c.Run()
	},
}
