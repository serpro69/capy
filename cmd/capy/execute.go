package main

import (
	"errors"
	"math"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/serpro69/capy/internal/execution"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/serpro69/capy/internal/security"
	"github.com/spf13/cobra"
)

func newExecuteCmd() *cobra.Command {
	var language, code, codeFile, cwd, intent string
	var timeoutMS int64
	var jsonOutput bool
	cmd := &cobra.Command{Use: "execute", Short: "Execute once with encrypted capture and bounded output", SilenceErrors: true, SilenceUsage: true}
	reject := func(cmd *cobra.Command, message string) error {
		return writeExecutionRejection(cmd, jsonOutput, 2048, 2, "invalid_argument", message)
	}
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return reject(cmd, "execute accepts named flags only")
		}
		return nil
	}
	cmd.SetFlagErrorFunc(func(cmd *cobra.Command, _ error) error {
		return reject(cmd, "invalid or unsupported execute flag; use capy execute --help")
	})
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if cmd.Flags().Changed("code") == cmd.Flags().Changed("code-file") {
			return reject(cmd, "provide exactly one of --code and --code-file")
		}
		switch executor.Language(language) {
		case executor.Shell, executor.Python, executor.Go, executor.Rust, executor.JavaScript, executor.TypeScript, executor.Ruby, executor.Perl, executor.PHP, executor.R, executor.Elixir:
		default:
			return reject(cmd, "unsupported execution language")
		}
		if cmd.Flags().Changed("timeout-ms") && (timeoutMS <= 0 || timeoutMS > math.MaxInt64/int64(time.Millisecond)) {
			return reject(cmd, "timeout-ms must be a positive integer within its supported duration")
		}
		target, err := runTarget(cmd)
		if err != nil {
			return writeExecutionRejection(cmd, jsonOutput, 2048, 1, "storage_unavailable", err.Error())
		}
		limit := target.cfg.Runs.MaxResponseBytes
		fail := func(err error) error {
			e := runstore.AdmissionError(commandRunError(err), target.projectDir)
			result := runstore.Rejection(e.Code, e.Message)
			result.Error = e
			return writeRunResponse(cmd, limit, jsonOutput, 1, func(n int) (any, error) { return execution.BoundResult(result, n), nil })
		}
		if cmd.Flags().Changed("code-file") {
			code, err = readCLIProgram(target.projectDir, codeFile)
			if err != nil {
				return fail(err)
			}
		}
		if code == "" || len(code) > security.MaxShellInputBytes {
			return reject(cmd, "code must be nonempty and at most 1 MiB")
		}
		if cmd.Flags().Changed("cwd") && cwd == "" {
			return reject(cmd, "cwd must name an existing directory")
		}
		if err := execution.CheckCLIProgram(executor.Language(language), code, security.ReadBashPolicies(target.projectDir, "")); err != nil {
			return fail(err)
		}
		key, _, err := target.cfg.ResolveStoreKey(target.projectDir)
		if err != nil {
			return fail(&runstore.Error{Code: "credential_unavailable", Message: "restore the selected run credential", Detail: execution.SafeMessage(err.Error(), 512)})
		}
		if !cmd.Flags().Changed("timeout-ms") {
			timeoutMS = int64(target.cfg.Executor.Timeout) * 1000
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		service := execution.NewCLIService(target.projectDir, key, target.cfg.Runs, execution.WithPreviewLimit(target.cfg.Executor.MaxOutputBytes))
		result := service.Execute(ctx, executor.ExecRequest{Language: executor.Language(language), Code: code, Cwd: cwd}, strings.TrimSpace(intent), time.Duration(timeoutMS)*time.Millisecond)
		exit := execution.CLIExitCode(result)
		if err := service.Close(); err != nil {
			result.Error = &runstore.Error{Code: "storage_unavailable", Message: "run store close failed; recover this run before retrying"}
			if exit == 0 {
				exit = 74
			}
		}
		return writeRunResponse(cmd, limit, jsonOutput, exit, func(n int) (any, error) { return execution.BoundResult(result, n), nil })
	}
	flags := cmd.Flags()
	flags.StringVar(&language, "language", "shell", "execution language")
	flags.StringVar(&code, "code", "", "program to execute unchanged")
	flags.StringVar(&codeFile, "code-file", "", "checked program file, relative to the selected owner (maximum 1 MiB)")
	flags.StringVar(&cwd, "cwd", "", "execution directory, relative to the selected owner")
	flags.Int64Var(&timeoutMS, "timeout-ms", 0, "execution deadline in milliseconds (default: configured executor.timeout)")
	flags.StringVar(&intent, "intent", "", "optional sanitized search preview (at most 512 UTF-8 bytes); indexing resumes with runs search")
	flags.BoolVar(&jsonOutput, "json", false, "emit the shared versioned JSON envelope")
	cmd.Long = "Execute a program once and retain encrypted output under the selected owner's .capy/runs. Waits attached until completion; --background and --wait-ms are not supported. Default output is sanitized. Recover retained output with capy runs; do not repeat execution to retrieve it.\n\nSupported on Linux and macOS 26 on Apple Silicon with CGO. Version-2 results report durable cleanup scope and state. Darwin observed cleanup does not prove every descendant is gone."
	return cmd
}

func readCLIProgram(project, path string) (string, error) {
	code, err := security.ReadCLIProgram(project, path)
	var admission *security.ProgramFileError
	if errors.As(err, &admission) {
		return "", &runstore.Error{Code: admission.Code, Message: admission.Error()}
	}
	return code, err
}
