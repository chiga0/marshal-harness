// Package cli implements Marshal's thin command-line boundary.
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/chiga0/marshal-harness/internal/app"
	"github.com/chiga0/marshal-harness/internal/buildinfo"
	"github.com/chiga0/marshal-harness/internal/contract"
)

// Process exit codes are stable CLI contract.
const (
	ExitOK          = 0
	ExitFailure     = 1
	ExitUsage       = 2
	ExitUnavailable = 3

	maxContractInputBytes int64 = 32 << 20
)

var taskCommands = []string{
	"plan",
	"run",
	"status",
	"verify",
	"review",
	"rework",
	"publish",
	"accept",
	"abort",
}

// Run executes one CLI invocation. Milestone 0 commands are read-only and do
// not launch Workers, create state directories, or publish changes.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		writeUsage(stderr)
		return ExitUsage
	}

	switch args[0] {
	case "help", "-h", "--help":
		writeUsage(stdout)
		return ExitOK
	case "version":
		return runVersion(args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr)
	case "contract":
		return runContract(args[1:], stdin, stdout, stderr)
	case "task":
		return runTask(args[1:], stderr)
	default:
		fmt.Fprintf(stderr, "未知命令 %q。\n", args[0])
		writeUsage(stderr)
		return ExitUsage
	}
}

func runVersion(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("version", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOutput := flags.Bool("json", false, "以 JSON 输出")
	if err := flags.Parse(args); err != nil {
		return ExitUsage
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "version 不接受位置参数。")
		return ExitUsage
	}
	info := buildinfo.Current()
	if *jsonOutput {
		if err := writeJSON(stdout, info); err != nil {
			fmt.Fprintf(stderr, "输出版本信息失败：%v\n", err)
			return ExitFailure
		}
		return ExitOK
	}
	fmt.Fprintf(stdout, "marshal %s (%s, %s, %s/%s)\n", info.Version, info.Commit, info.GoVersion, info.OS, info.Arch)
	return ExitOK
}

func runDoctor(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOutput := flags.Bool("json", false, "以 JSON 输出")
	if err := flags.Parse(args); err != nil {
		return ExitUsage
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "doctor 不接受位置参数。")
		return ExitUsage
	}
	application, err := app.New()
	if err != nil {
		fmt.Fprintf(stderr, "doctor 失败：%v\n", err)
		return ExitFailure
	}
	report := struct {
		Status          string         `json:"status"`
		Build           buildinfo.Info `json:"build"`
		ContractSchemas int            `json:"contractSchemas"`
		WorkerAdapters  int            `json:"workerAdapters"`
		Milestone       string         `json:"milestone"`
	}{
		Status:          "ok",
		Build:           buildinfo.Current(),
		ContractSchemas: application.ContractCount(),
		WorkerAdapters:  application.AdapterCount(),
		Milestone:       "0",
	}
	if *jsonOutput {
		if err := writeJSON(stdout, report); err != nil {
			fmt.Fprintf(stderr, "输出 doctor 报告失败：%v\n", err)
			return ExitFailure
		}
		return ExitOK
	}
	fmt.Fprintf(stdout, "状态：%s\nSchema：%d 份已编译\nWorker Adapter：%d（Milestone 0 预期值）\n", report.Status, report.ContractSchemas, report.WorkerAdapters)
	return ExitOK
}

func runContract(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "validate" {
		fmt.Fprintln(stderr, "用法：marshal contract validate [--schema NAME] <PATH|->")
		return ExitUsage
	}
	flags := flag.NewFlagSet("contract validate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	schemaName := flags.String("schema", "", "显式 Schema 名称；省略时从 kind 检测")
	if err := flags.Parse(args[1:]); err != nil {
		return ExitUsage
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(stderr, "contract validate 需要一个文件路径或 -。")
		return ExitUsage
	}
	data, err := readInput(flags.Arg(0), stdin)
	if err != nil {
		fmt.Fprintf(stderr, "读取契约失败：%v\n", err)
		return ExitFailure
	}
	application, err := app.New()
	if err != nil {
		fmt.Fprintf(stderr, "初始化失败：%v\n", err)
		return ExitFailure
	}
	validatedKind := ""
	if *schemaName == "" {
		record, validateErr := application.ValidateDocument(data)
		if validateErr != nil {
			fmt.Fprintf(stderr, "契约无效：%v\n", validateErr)
			return ExitFailure
		}
		validatedKind = string(record.Kind)
	} else {
		descriptor, resolveErr := contract.DescriptorByName(*schemaName)
		if resolveErr != nil {
			fmt.Fprintf(stderr, "%v\n", resolveErr)
			return ExitUsage
		}
		if validateErr := application.ValidateContract(descriptor.Kind, data); validateErr != nil {
			fmt.Fprintf(stderr, "契约无效：%v\n", validateErr)
			return ExitFailure
		}
		validatedKind = string(descriptor.Kind)
	}
	fmt.Fprintf(stdout, "有效：%s\n", validatedKind)
	return ExitOK
}

func runTask(args []string, stderr io.Writer) int {
	if len(args) != 1 || !slices.Contains(taskCommands, args[0]) {
		fmt.Fprintf(stderr, "用法：marshal task <%s>\n", strings.Join(taskCommands, "|"))
		return ExitUsage
	}
	fmt.Fprintf(stderr, "marshal task %s 尚未在 Milestone 0 实现；未执行任何 Worker、状态写入或发布副作用。\n", args[0])
	return ExitUnavailable
}

func readInput(name string, stdin io.Reader) ([]byte, error) {
	if name == "-" {
		return readBounded(stdin, maxContractInputBytes)
	}
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readBounded(file, maxContractInputBytes)
}

func readBounded(input io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(input, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("contract input exceeds %d bytes", limit)
	}
	return data, nil
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return errors.New("encode JSON: " + err.Error())
	}
	return nil
}

func writeUsage(output io.Writer) {
	fmt.Fprintln(output, `Marshal：证据门禁式 Coding Agent 编排器

用法：
  marshal version [--json]
  marshal doctor [--json]
  marshal contract validate [--schema NAME] <PATH|->
  marshal task <COMMAND>

Milestone 0 只提供只读 CLI 骨架和契约验证；task 命令不会执行副作用。`)
}
