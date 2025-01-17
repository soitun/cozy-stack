package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/cozy/cozy-stack/client"
	"github.com/cozy/cozy-stack/client/request"

	"github.com/spf13/cobra"
)

var flagJobJSONArg string
var flagJobPrintLogs bool
var flagJobPrintLogsVerbose bool
var flagJobWorkers []string
var flagJobsPurgeDuration string

var jobsCmdGroup = &cobra.Command{
	Use:   "jobs <command>",
	Short: "Launch and manage jobs and workers",
}

var jobsRunCmd = &cobra.Command{
	Use:     "run <worker>",
	Aliases: []string{"launch", "push"},
	Example: `$ cozy-stack jobs run service --domain example.mycozy.cloud --json '{"slug": "banks", "name": "onOperationOrBillCreate", "file": "onOperationOrBillCreate.js"}'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) < 1 {
			return cmd.Help()
		}
		if flagDomain == "" {
			return errMissingDomain
		}
		if flagJobJSONArg == "" {
			return errors.New("The JSON argument is missing")
		}
		c := newClient(flagDomain, "io.cozy.jobs", "io.cozy.jobs.logs")
		o := &client.JobOptions{
			Worker:    args[0],
			Arguments: json.RawMessage(flagJobJSONArg),
		}
		if flagJobPrintLogs {
			o.Logs = make(chan *client.JobLog)
			go func() {
				for log := range o.Logs {
					fmt.Printf("[%s]", log.Level)
					if flagJobPrintLogsVerbose {
						fmt.Printf("[time:%s]", log.Time.Format(time.RFC3339))
						for k, v := range log.Data {
							fmt.Printf("[%s:%s]", k, v)
						}
					}
					fmt.Printf(" %s\n", log.Message)
				}
			}()
		}
		j, err := c.JobPush(o)
		if err != nil {
			return err
		}
		b, err := json.MarshalIndent(j, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	},
}

var jobsPurgeCmd = &cobra.Command{
	Use:     "purge-old-jobs <domain>",
	Short:   `Purge old jobs from an instance`,
	Example: `$ cozy-stack jobs purge-old-jobs example.mycozy.cloud`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) != 1 {
			return cmd.Help()
		}

		var workers string
		if flagJobWorkers != nil {
			workers = strings.Join(flagJobWorkers, ",")
		}
		duration := flagJobsPurgeDuration

		q := url.Values{
			"duration": {duration},
			"workers":  {workers},
		}
		c := newClient(args[0], "io.cozy.jobs:DELETE")

		res, err := c.Req(&request.Options{
			Method:  "DELETE",
			Path:    "/jobs/purge",
			Queries: q,
		})

		if err != nil {
			return err
		}

		resContent, err := io.ReadAll(res.Body)
		if err != nil {
			return err
		}
		fmt.Println(string(resContent))
		return nil
	},
}

func init() {
	jobsCmdGroup.PersistentFlags().StringVar(&flagDomain, "domain", cozyDomain(), "specify the domain name of the instance")

	jobsRunCmd.Flags().StringVar(&flagJobJSONArg, "json", "", "specify the job arguments as raw JSON")
	jobsRunCmd.Flags().BoolVar(&flagJobPrintLogs, "logs", false, "print jobs log in stdout")
	jobsRunCmd.Flags().BoolVar(&flagJobPrintLogsVerbose, "logs-verbose", false, "verbose logging (with --logs flag)")

	jobsPurgeCmd.Flags().StringSliceVar(&flagJobWorkers, "workers", nil, "worker types to iterate over (all workers by default)")
	jobsPurgeCmd.Flags().StringVar(&flagJobsPurgeDuration, "duration", "", "duration to look for (ie. 3D, 2M)")

	jobsCmdGroup.AddCommand(jobsRunCmd)
	jobsCmdGroup.AddCommand(jobsPurgeCmd)
	RootCmd.AddCommand(jobsCmdGroup)
}
