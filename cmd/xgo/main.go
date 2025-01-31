package main

import (
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/xhd2015/xgo/cmd/xgo/pathsum"
	"github.com/xhd2015/xgo/support/cmd"
	"github.com/xhd2015/xgo/support/goinfo"
)

// usage:
//   xgo build main.go
//   xgo build .
//   xgo run main
//   xgo exec go version
//
// low level flags:
//   -disable-trap          disable trap
//   -disable-runtime-link  disable runtime link

func main() {
	args := os.Args[1:]

	var cmd string
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}

	if cmd == "" {
		fmt.Fprintf(os.Stderr, "xgo: requires command\nRun 'xgo help' for usage.\n")
		os.Exit(1)
	}
	if cmd == "help" {
		fmt.Print(strings.TrimPrefix(help, "\n"))
		return
	}
	if cmd == "version" {
		fmt.Println(VERSION)
		return
	}
	if cmd == "revision" {
		fmt.Println(getRevision())
		return
	}
	if cmd == "upgrade" {
		err := upgrade(args)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
		return
	}
	if cmd == "tool" {
		if len(args) == 0 {
			fmt.Fprintf(os.Stderr, "xgo tool: requires tool to run\n")
			os.Exit(1)
		}
		err := handleTool(args[0], args[1:])
		if err != nil {
			if err, ok := err.(*exec.ExitError); ok {
				os.Exit(err.ExitCode())
			}
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
		return
	}
	if cmd != "build" && cmd != "run" && cmd != "test" && cmd != "exec" {
		fmt.Fprintf(os.Stderr, "xgo %s: unknown command\nRun 'xgo help' for usage.\n", cmd)
		os.Exit(1)
	}

	err := handleBuild(cmd, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func handleBuild(cmd string, args []string) error {
	cmdBuild := cmd == "build"
	cmdTest := cmd == "test"
	cmdExec := cmd == "exec"
	opts, err := parseOptions(args)
	if err != nil {
		return err
	}
	remainArgs := opts.remainArgs
	flagA := opts.flagA
	projectDir := opts.projectDir
	output := opts.output
	flagV := opts.flagV
	flagX := opts.flagX
	flagC := opts.flagC
	logCompile := opts.logCompile
	optXgoSrc := opts.xgoSrc
	noBuildOutput := opts.noBuildOutput
	noInstrument := opts.noInstrument
	resetInstrument := opts.resetInstrument
	noSetup := opts.noSetup
	syncXgoOnly := opts.syncXgoOnly
	setupDev := opts.setupDev
	buildCompiler := opts.buildCompiler
	syncWithLink := opts.syncWithLink
	debug := opts.debug
	vscode := opts.vscode
	gcflags := opts.gcflags
	withGoroot := opts.withGoroot
	dumpIR := opts.dumpIR

	if cmdExec && len(remainArgs) == 0 {
		return fmt.Errorf("exec requires command")
	}

	goroot, err := checkGoroot(withGoroot)
	if err != nil {
		return err
	}
	// create a tmp dir for communication with exec_tool
	tmpDir, err := os.MkdirTemp("", "xgo-"+cmd)
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	var vscodeDebugFile string
	var vscodeDebugFileSuffix string
	if !noInstrument && debug != "" {
		var err error
		vscodeDebugFile, vscodeDebugFileSuffix, err = getVscodeDebugFile(tmpDir, vscode)
		if err != nil {
			return err
		}
	}

	var tmpIRFile string
	if !noInstrument {
		if dumpIR != "" {
			tmpIRFile = filepath.Join(tmpDir, "dump-ir")
		}
	}

	// build the exec tool
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("get config under home directory: %v", err)
	}
	goVersion, err := checkGoVersion(goroot, noInstrument)
	if err != nil {
		return err
	}

	goVersionName := fmt.Sprintf("go%d.%d.%d", goVersion.Major, goVersion.Minor, goVersion.Patch)
	mappedGorootName, err := pathsum.PathSum(goVersionName+"_", goroot)
	if err != nil {
		return err
	}

	xgoDir := filepath.Join(homeDir, ".xgo")
	binDir := filepath.Join(xgoDir, "bin")
	logDir := filepath.Join(xgoDir, "log")
	instrumentDir := filepath.Join(xgoDir, "go-instrument", mappedGorootName)

	execToolBin := filepath.Join(binDir, "exec_tool")
	compileLog := filepath.Join(logDir, "compile.log")
	compilerBin := filepath.Join(instrumentDir, "compile")
	compilerBuildID := filepath.Join(instrumentDir, "compile.buildid.txt")
	instrumentGoroot := filepath.Join(instrumentDir, goVersionName)
	buildCacheDir := filepath.Join(instrumentDir, "build-cache")
	revisionFile := filepath.Join(instrumentDir, "xgo-revision.txt")

	var realXgoSrc string
	if isDevelopment {
		realXgoSrc = optXgoSrc
		if realXgoSrc == "" {
			return fmt.Errorf("requires --xgo-src")
		}
	}

	if !noInstrument && !noSetup {
		err = ensureDirs(binDir, logDir, instrumentDir)
		if err != nil {
			return err
		}
		revision := getRevision()
		if isDevelopment {
			revision = revision + " DEV"
		}
		revisionChanged, err := checkRevisionChanged(revisionFile, revision)
		if err != nil {
			return err
		}
		err = syncGoroot(goroot, instrumentGoroot, resetInstrument, revisionChanged)
		if err != nil {
			return err
		}
		// patch go runtime and compiler
		err = patchRuntimeAndCompiler(instrumentGoroot, realXgoSrc, goVersion, syncWithLink || setupDev || buildCompiler, revisionChanged)
		if err != nil {
			return err
		}
		if revisionChanged {
			err := os.WriteFile(revisionFile, []byte(revision), 0755)
			if err != nil {
				return err
			}
		}
	}

	if isDevelopment && (setupDev || buildCompiler) {
		err := setupDevDir(instrumentGoroot, setupDev)
		if err != nil {
			return err
		}
		if setupDev {
			return nil
		}
	}
	if syncXgoOnly {
		return nil
	}

	var compilerChanged bool
	var toolExecFlag string
	if !noInstrument {
		compilerChanged, toolExecFlag, err = buildInstrumentTool(instrumentGoroot, realXgoSrc, compilerBin, compilerBuildID, execToolBin, debug, logCompile, noSetup)
		if err != nil {
			return err
		}
	}
	if buildCompiler {
		return nil
	}

	// before invoking exec_tool, tail follow its log
	if logCompile {
		go tailLog(compileLog)
	}

	var execCmd *exec.Cmd
	if !cmdExec {
		// GOCACHE="$shdir/build-cache" PATH=$goroot/bin:$PATH GOROOT=$goroot DEBUG_PKG=$debug go build -toolexec="$shdir/exce_tool $cmd" "${build_flags[@]}" "$@"
		buildCmdArgs := []string{cmd}
		if toolExecFlag != "" {
			buildCmdArgs = append(buildCmdArgs, toolExecFlag)
		}
		if flagA || compilerChanged {
			buildCmdArgs = append(buildCmdArgs, "-a")
		}
		if flagV {
			buildCmdArgs = append(buildCmdArgs, "-v")
		}
		if flagX {
			buildCmdArgs = append(buildCmdArgs, "-x")
		}
		if flagC {
			buildCmdArgs = append(buildCmdArgs, "-c")
		}
		if gcflags != "" {
			buildCmdArgs = append(buildCmdArgs, "-gcflags="+gcflags)
		}
		if cmdBuild || (cmdTest && flagC) {
			// output
			if noBuildOutput {
				discardOut := filepath.Join(tmpDir, "discard.out")
				buildCmdArgs = append(buildCmdArgs, "-o", discardOut)
			} else if output != "" {
				realOut := output
				if projectDir != "" {
					// make absolute
					absOutput, err := filepath.Abs(output)
					if err != nil {
						return fmt.Errorf("make output absolute: %w", err)
					}
					realOut = absOutput
				}
				buildCmdArgs = append(buildCmdArgs, "-o", realOut)
			}
		}
		buildCmdArgs = append(buildCmdArgs, remainArgs...)
		execCmd = exec.Command(filepath.Join(instrumentGoroot, "bin", "go"), buildCmdArgs...)
	} else {
		execCmd = exec.Command(remainArgs[0], remainArgs[1:]...)
	}
	execCmd.Env = os.Environ()
	execCmd.Env, err = patchEnvWithGoroot(execCmd.Env, instrumentGoroot)
	if err != nil {
		return err
	}
	if !noInstrument {
		execCmd.Env = append(execCmd.Env, "GOCACHE="+buildCacheDir)
		execCmd.Env = append(execCmd.Env, "XGO_COMPILER_BIN="+compilerBin)
		if dumpIR != "" {
			execCmd.Env = append(execCmd.Env, "XGO_DEBUG_DUMP_IR="+dumpIR)
			execCmd.Env = append(execCmd.Env, "XGO_DEBUG_DUMP_IR_FILE="+tmpIRFile)
		}
		if vscodeDebugFile != "" {
			execCmd.Env = append(execCmd.Env, "XGO_DEBUG_VSCODE="+vscodeDebugFile+vscodeDebugFileSuffix)
		}
	}
	execCmd.Stdout = os.Stdout
	execCmd.Stderr = os.Stderr
	if projectDir != "" {
		execCmd.Dir = projectDir
	}
	err = execCmd.Run()
	if err != nil {
		return err
	}

	// if dump IR is not nil, output to stdout
	if tmpIRFile != "" {
		err := copyToStdout(tmpIRFile)
		if err != nil {
			// ir file not exists
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("dump ir not effective, use -a to trigger recompile")
			}
			return err
		}
	}
	if (vscode == "stdout" || strings.HasPrefix(vscode, "stdout?")) && vscodeDebugFile != "" {
		err := copyToStdout(vscodeDebugFile)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("vscode debug not effective, use -a to trigger recompile")
			}
			return err
		}
	}
	return nil
}

func handleTool(tool string, args []string) error {
	baseName := filepath.Base(tool)
	if baseName != tool {
		return fmt.Errorf("unknown tool: %s", tool)
	}
	curName := filepath.Base(os.Args[0])
	if baseName == curName {
		// cannot invoke itself
		return fmt.Errorf("unknown tool: %s", tool)
	}
	dirName := filepath.Dir(os.Args[0])
	toolExec := filepath.Join(dirName, tool)

	var retryHome bool
	stat, statErr := os.Stat(toolExec)
	if statErr != nil {
		if !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("unknown tool: %s", tool)
		}
		retryHome = true
	} else if stat.IsDir() {
		retryHome = true
	}
	if retryHome {
		// try ~/.xgo/bin/tool
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return fmt.Errorf("unknown tool: %s", tool)
		}
		toolExec = filepath.Join(home, ".xgo", "bin", tool)
		stat, statErr := os.Stat(toolExec)
		if statErr != nil || stat.IsDir() {
			return fmt.Errorf("unknown tool: %s", tool)
		}
	}

	return cmd.Run(toolExec, args...)
}

func checkGoVersion(goroot string, noInstrument bool) (*goinfo.GoVersion, error) {
	// check if we are using expected go version
	goVersionStr, err := goinfo.GetGoVersionOutput(filepath.Join(goroot, "bin", "go"))
	if err != nil {
		return nil, err
	}
	goVersion, err := goinfo.ParseGoVersion(goVersionStr)
	if err != nil {
		return nil, err
	}
	if !noInstrument {
		minor := goVersion.Minor
		if goVersion.Major != 1 || (minor < 16 || minor > 22) {
			return nil, fmt.Errorf("only supports go1.16.0 ~ go1.22.1, current: %s", goVersionStr)
		}
	}
	return goVersion, nil
}

func ensureDirs(binDir string, logDir string, instrumentDir string) error {
	err := os.MkdirAll(binDir, 0755)
	if err != nil {
		return fmt.Errorf("create ~/.xgo/bin: %w", err)
	}
	err = os.MkdirAll(logDir, 0755)
	if err != nil {
		return fmt.Errorf("create ~/.xgo/log: %w", err)
	}
	err = os.MkdirAll(instrumentDir, 0755)
	if err != nil {
		return fmt.Errorf("create ~/.xgo/log: %w", err)
	}
	return nil
}

func buildInstrumentTool(goroot string, xgoSrc string, compilerBin string, compilerBuildIDFile string, execToolBin string, debugPkg string, logCompile bool, noSetup bool) (compilerChanged bool, toolExecFlag string, err error) {
	actualExecToolBin := execToolBin
	if !noSetup {
		// build the instrumented compiler
		err = buildCompiler(goroot, compilerBin)
		if err != nil {
			return false, "", err
		}
		compilerChanged, err = compareAndUpdateCompilerID(compilerBin, compilerBuildIDFile)
		if err != nil {
			return false, "", err
		}

		if isDevelopment {
			// build exec tool
			buildExecToolCmd := exec.Command("go", "build", "-o", execToolBin, "./exec_tool")
			buildExecToolCmd.Dir = filepath.Join(xgoSrc, "cmd")
			buildExecToolCmd.Stdout = os.Stdout
			buildExecToolCmd.Stderr = os.Stderr
			err = buildExecToolCmd.Run()
			if err != nil {
				return false, "", err
			}
		} else {
			actualExecToolBin, err = findBuiltExecTool()
			if err != nil {
				return false, "", err
			}
		}

	}
	execToolCmd := []string{actualExecToolBin, "--enable"}
	if logCompile {
		execToolCmd = append(execToolCmd, "--log-compile")
	}
	if debugPkg != "" {
		execToolCmd = append(execToolCmd, "--debug="+debugPkg)
	}
	// always add trailing '--' to mark exec tool flags end
	execToolCmd = append(execToolCmd, "--")

	toolExecFlag = "-toolexec=" + strings.Join(execToolCmd, " ")
	return compilerChanged, toolExecFlag, nil
}

// find exec_tool, first try the same dir with xgo,
// but if that is not found, we can fallback to ~/.xgo/bin/exec_tool
// because exec_tool changes rarely, so it is safe to use
// an older version.
// we may add version to check if exec_tool is compitable
func findBuiltExecTool() (string, error) {
	dirName := filepath.Dir(os.Args[0])
	absDirName, err := filepath.Abs(dirName)
	if err != nil {
		return "", err
	}
	execToolBin := filepath.Join(absDirName, "exec_tool")
	_, statErr := os.Stat(execToolBin)
	if statErr == nil {
		return execToolBin, nil
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("exec_tool not found in %s", dirName)
	}
	execToolBin = filepath.Join(homeDir, ".xgo", "bin", "exec_tool")
	_, statErr = os.Stat(execToolBin)
	if statErr == nil {
		return execToolBin, nil
	}
	return "", fmt.Errorf("exec_tool not found in %s and ~/.xgo/bin", dirName)
}
func buildCompiler(goroot string, output string) error {
	args := []string{"build"}
	if isDevelopment {
		args = append(args, "-gcflags=all=-N -l")
	}
	args = append(args, "-o", output, "./")
	cmd := exec.Command(filepath.Join(goroot, "bin", "go"), args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	env, err := patchEnvWithGoroot(os.Environ(), goroot)
	if err != nil {
		return err
	}
	cmd.Env = env
	cmd.Dir = filepath.Join(goroot, "src", "cmd", "compile")
	return cmd.Run()
}

func compareAndUpdateCompilerID(compilerFile string, compilerIDFile string) (changed bool, err error) {
	prevData, err := ioutil.ReadFile(compilerIDFile)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		err = nil
	}
	prevID := string(prevData)
	curID, err := getBuildID(compilerFile)
	if err != nil {
		return false, err
	}
	if prevID != "" && prevID == curID {
		return false, nil
	}
	err = ioutil.WriteFile(compilerIDFile, []byte(curID), 0755)
	if err != nil {
		return false, err
	}
	return true, nil
}

func getBuildID(file string) (string, error) {
	data, err := exec.Command("go", "tool", "buildid", file).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(data), "\n"), nil
}

func patchEnvWithGoroot(env []string, goroot string) ([]string, error) {
	goroot, err := filepath.Abs(goroot)
	if err != nil {
		return nil, err
	}
	return append(env,
		"GOROOT="+goroot,
		fmt.Sprintf("PATH=%s%c%s", filepath.Join(goroot, "bin"), filepath.ListSeparator, os.Getenv("PATH")),
	), nil
}

func assertDir(dir string) error {
	fileInfo, err := os.Stat(dir)
	if err != nil {
		return err
		// return fmt.Errorf("stat ~/.xgo/src: %v", err)
	}
	if !fileInfo.IsDir() {
		return fmt.Errorf("not a dir")
	}
	return nil
}

func checkGoroot(goroot string) (string, error) {
	if goroot == "" {
		goroot = runtime.GOROOT()
		if goroot == "" {
			return "", fmt.Errorf("requires GOROOT or --with-goroot")
		}
		return goroot, nil
	}
	_, err := os.Stat(goroot)
	if err == nil {
		return goroot, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if filepath.Base(goroot) != goroot {
		return "", err
	}
	// check if inside go-release
	releaseGo := filepath.Join("go-release", goroot)
	releaseStat, statErr := os.Stat(releaseGo)
	if statErr == nil && releaseStat.IsDir() {
		return releaseGo, nil
	}
	if strings.HasPrefix(goroot, "go") {
		fmt.Fprintf(os.Stderr, "WARNING %s does not exist, download it with:\n          go run ./script/download-go %s\n", goroot, goroot)
	}
	return "", err
}

// if [[ $verbose = true ]];then
//
//	    tail -fn1 "$shdir/compile.log" &
//	    trap "kill -9 $!" EXIT
//	fi
func tailLog(logFile string) {
	file, err := os.OpenFile(logFile, os.O_RDONLY|os.O_CREATE, 0755)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open compile log: %v\n", err)
		return
	}
	_, err = file.Seek(0, io.SeekEnd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "seek tail compile log: %v\n", err)
		return
	}
	buf := make([]byte, 1024)
	for {
		n, err := file.Read(buf)
		if n > 0 {
			os.Stdout.Write(buf[:n])
		}
		if err != nil {
			if err == io.EOF {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			fmt.Fprintf(os.Stderr, "tail compile log: %v\n", err)
			return
		}
	}
}
