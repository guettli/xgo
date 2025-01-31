package test

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/xhd2015/xgo/support/cmd"
)

type xgoCmd string

const (
	xgoCmd_build     xgoCmd = "build"
	xgoCmd_run       xgoCmd = "run"
	xgoCmd_test      xgoCmd = "test"
	xgoCmd_testBuild xgoCmd = "test_build"
)

type options struct {
	xgoCmd xgoCmd // the command is run
	exec   bool
	noTrim bool
	env    []string

	noPipeStderr bool

	init bool

	projectDir string
}

func runXgo(args []string, opts *options) (string, error) {
	if opts == nil || !opts.init {
		err := ensureXgoInit()
		if err != nil {
			return "", err
		}
	} else {
		// build the xgo binary
		err := cmd.Run("go", "build", "-o", xgoBinary, "../cmd/xgo")
		if err != nil {
			return "", err
		}
	}
	var xgoCmd string = "build"
	var extraArgs []string
	if opts != nil {
		if opts.xgoCmd == xgoCmd_build {
			xgoCmd = "build"
		} else if opts.xgoCmd == xgoCmd_run {
			xgoCmd = "run"
		} else if opts.xgoCmd == xgoCmd_test {
			xgoCmd = "test"
		} else if opts.xgoCmd == xgoCmd_testBuild {
			xgoCmd = "test"
			extraArgs = append(extraArgs, "-c")
		} else if opts.exec {
			xgoCmd = "exec"
		}
	}
	xgoArgs := []string{
		xgoCmd,
		"--xgo-src",
		"../",
		"--sync-with-link",
	}
	xgoArgs = append(xgoArgs, extraArgs...)
	// accerlate
	if opts == nil || !opts.init {
		xgoArgs = append(xgoArgs, "--no-setup")
	}
	if opts != nil && opts.projectDir != "" {
		xgoArgs = append(xgoArgs, "--project-dir", opts.projectDir)
	}
	xgoArgs = append(xgoArgs, args...)
	cmd := exec.Command(xgoBinary, xgoArgs...)
	if opts == nil || !opts.noPipeStderr {
		cmd.Stderr = os.Stderr
	}
	if opts != nil && len(opts.env) > 0 {
		cmd.Env = os.Environ()
		cmd.Env = append(cmd.Env, opts.env...)
	}

	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	outStr := string(output)
	if opts == nil || !opts.noTrim {
		outStr = strings.TrimSuffix(outStr, "\n")
	}
	return outStr, nil
}

func xgoExec(args ...string) (string, error) {
	return runXgo(args, &options{
		exec: true,
	})
}

// return clean up func
type buildRuntimeOpts struct {
	xgoBuildArgs []string
	xgoBuildEnv  []string
	runEnv       []string

	debug bool
}

func buildWithRuntimeAndOutput(dir string, opts buildRuntimeOpts) (string, error) {
	tmpFile, err := getTempFile("test")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpFile)

	// func_list depends on xgo/runtime, but xgo/runtime is
	// a separate module, so we need to merge them
	// together first
	tmpDir, funcListDir, err := tmpMergeRuntimeAndTest(dir)
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)

	xgoBuildArgs := []string{
		"-o", tmpFile,
		// "-a",
		"--project-dir", funcListDir,
	}
	if opts.debug {
		xgoBuildArgs = append(xgoBuildArgs, "-gcflags=all=-N -l")
	}
	xgoBuildArgs = append(xgoBuildArgs, opts.xgoBuildArgs...)
	xgoBuildArgs = append(xgoBuildArgs, ".")
	_, err = runXgo(xgoBuildArgs, &options{
		env: opts.xgoBuildEnv,
	})
	if err != nil {
		return "", err
	}
	if opts.debug {
		fmt.Println(tmpFile)
		time.Sleep(10 * time.Minute)
	}
	runCmd := exec.Command(tmpFile)
	runCmd.Env = os.Environ()
	runCmd.Env = append(runCmd.Env, opts.runEnv...)
	output, err := runCmd.Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func buildAndRunOutput(program string) (output string, err error) {
	return buildAndRunOutputArgs([]string{program}, buildAndOutputOptions{})
}

type buildAndOutputOptions struct {
	build      func(args []string) error
	projectDir string
}

func buildAndRunOutputArgs(args []string, opts buildAndOutputOptions) (output string, err error) {
	testBin, err := getTempFile("test")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(testBin)
	buildArgs := []string{"-o", testBin}
	buildArgs = append(buildArgs, args...)
	if opts.build != nil {
		err = opts.build(buildArgs)
	} else {
		_, err = runXgo(buildArgs, &options{
			projectDir: opts.projectDir,
		})
	}
	if err != nil {
		return "", err
	}
	out, err := exec.Command(testBin).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
