package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/caarlos0/env"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/goccy/bigquery-emulator/server"
	"github.com/goccy/bigquery-emulator/types"
	"github.com/jessevdk/go-flags"
)

type option struct {
	Project      string           `description:"specify the project name" long:"project" env:"PROJECT"`
	Dataset      string           `description:"specify the dataset name" long:"dataset" env:"DATASET"`
	Host         string           `description:"specify the host" long:"host" default:"0.0.0.0" env:"HOST"`
	HTTPPort     uint16           `description:"specify the http port number. this port used by bigquery api" long:"port" default:"9050" env:"PORT"`
	GRPCPort     uint16           `description:"specify the grpc port number. this port used by bigquery storage api" long:"grpc-port" default:"9060" env:"GRPC_PORT"`
	LogLevel     server.LogLevel  `description:"specify the log level (debug/info/warn/error)" long:"log-level" default:"error" env:"LOG_LEVEL"`
	LogFormat    server.LogFormat `description:"specify the log format (console/json)" long:"log-format" default:"console" env:"LOG_FORMAT"`
	Database     string           `description:"specify the database file if required. if not specified, it will be on memory" long:"database" env:"DATABASE"`
	DataFromYAML string           `description:"specify the path to the YAML file that contains the initial data" long:"data-from-yaml" env:"DATA_FROM_YAML"`
	Version      bool             `description:"print version" long:"version" short:"v"`
}

type exitCode int

const (
	exitOK    exitCode = 0
	exitError exitCode = 1
)

var (
	version  string
	revision string
)

func main() {
	os.Exit(int(run()))
}

func run() exitCode {
	opt, err := parseEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[bigquery-emulator] error parsing environment variables: %v", err)
		return exitError
	}
	args, err := parseOpt(&opt)
	if err != nil {
		flagsErr, ok := err.(*flags.Error)
		if !ok {
			fmt.Fprintf(os.Stderr, "[bigquery-emulator] unknown parsed option error: %[1]T %[1]v\n", err)
			return exitError
		}
		if flagsErr.Type == flags.ErrHelp {
			return exitOK
		}
		return exitError
	}
	if err := runServer(args, opt); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitError
	}
	return exitOK
}

func parseEnv() (option, error) {
	var opt option
	err := env.Parse(&opt)
	return opt, err
}

func parseOpt(opt *option) ([]string, error) {
	parser := flags.NewParser(opt, flags.Default)
	args, err := parser.Parse()
	return args, err
}

func runServer(args []string, opt option) error {
	if opt.Version {
		fmt.Fprintf(os.Stdout, "version: %s (%s)\n", version, revision)
		return nil
	}
	if opt.Project == "" {
		return fmt.Errorf("the required flag --project was not specified")
	}
	var db server.Storage
	if opt.Database == "" {
		db = server.TempStorage
	} else {
		db = server.Storage(fmt.Sprintf("file:%s?cache=shared", opt.Database))
	}
	project := types.NewProject(opt.Project)
	if opt.Dataset != "" {
		project.Datasets = append(project.Datasets, types.NewDataset(opt.Dataset))
	}
	bqServer, err := server.New(db)
	if err != nil {
		return err
	}
	if err := bqServer.SetProject(project.ID); err != nil {
		return err
	}
	if err := bqServer.Load(server.StructSource(project)); err != nil {
		return err
	}
	if err := bqServer.SetLogLevel(opt.LogLevel); err != nil {
		return err
	}
	if err := bqServer.SetLogFormat(opt.LogFormat); err != nil {
		return err
	}
	if opt.DataFromYAML != "" {
		if err := bqServer.Load(server.YAMLSource(opt.DataFromYAML)); err != nil {
			return err
		}
	}

	ctx := context.Background()
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)

	go func() {
		select {
		case s := <-interrupt:
			fmt.Fprintf(os.Stdout, "[bigquery-emulator] receive %s. shutdown gracefully\n", s)
			if err := bqServer.Stop(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "[bigquery-emulator] failed to stop: %v\n", err)
			}
		}
	}()

	done := make(chan error)
	go func() {
		httpAddr := fmt.Sprintf("%s:%d", opt.Host, opt.HTTPPort)
		grpcAddr := fmt.Sprintf("%s:%d", opt.Host, opt.GRPCPort)
		fmt.Fprintf(os.Stdout, "[bigquery-emulator] REST server listening at %s\n", httpAddr)
		fmt.Fprintf(os.Stdout, "[bigquery-emulator] gRPC server listening at %s\n", grpcAddr)
		done <- bqServer.Serve(ctx, httpAddr, grpcAddr)
	}()

	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}

	return nil
}
