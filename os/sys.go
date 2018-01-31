package os

import (
	"bytes"
	"fmt"
	"io"
	"os"
	pathlib "path"

	"github.com/mkishere/sshsyrup/util/termlogger"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/afero"
	"golang.org/x/crypto/ssh"
)

var (
	crlf = []byte{'\r', '\n'}
)

var (
	funcMap = make(map[string]Command)
)

// Command interface allow classes to simulate real executable
// that have access to standard I/O, filesystem, arguments, EnvVars,
// and cwd
type Command interface {
	GetHelp() string
	Exec(args []string, sys Sys) int
	Where() string
}

// System provides what most of os/sys does in the honeyport
type System struct {
	cwd           string
	fSys          afero.Fs
	sshChan       ssh.Channel
	envVars       map[string]string
	width, height int
	log           *log.Entry
	sessionLog    termlogger.LogHook
}

type Sys interface {
	Getcwd() string
	Chdir(path string) error
	In() io.Reader
	Out() io.Writer
	Err() io.ReadWriter
	Environ() (env []string)
	SetEnv(key, value string) error
	FSys() afero.Fs
	Width() int
	Height() int
}
type stdoutWrapper struct {
	io.Writer
}

type sysLogWrapper struct {
	*System
	io.ReadWriter
}

func (sys *sysLogWrapper) In() io.Reader      { return sys.ReadWriter }
func (sys *sysLogWrapper) Out() io.Writer     { return stdoutWrapper{sys.ReadWriter} }
func (sys *sysLogWrapper) Err() io.ReadWriter { return sys.ReadWriter }

func NewSystem(user string, fs afero.Fs, channel ssh.Channel, width, height int, log *log.Entry) *System {
	aferoFs := afero.Afero{fs}
	// Create user home directory if not exists
	if exists, _ := aferoFs.DirExists(usernameMapping[user].Homedir); !exists {
		aferoFs.MkdirAll(usernameMapping[user].Homedir, 0644)
	}

	return &System{
		cwd:     usernameMapping[user].Homedir,
		fSys:    aferoFs,
		envVars: map[string]string{},
		sshChan: channel,
		width:   width,
		height:  height,
		log:     log,
	}
}

// Getcwd gets current working directory
func (sys *System) Getcwd() string {
	return sys.cwd
}

// Chdir change current working directory
func (sys *System) Chdir(path string) error {
	if !pathlib.IsAbs(path) {
		path = sys.cwd + "/" + path
	}
	if exists, err := afero.DirExists(sys.fSys, path); err == nil && !exists {
		return os.ErrNotExist
	} else if err != nil {
		return err
	}
	sys.cwd = path
	return nil
}

// In returns a io.Reader that represent stdin
func (sys *System) In() io.Reader { return sys.sshChan }

// Out returns a io.Writer that represent stdout
func (sys *System) Out() io.Writer {
	return stdoutWrapper{sys.sshChan}
}

func (sys *System) Err() io.ReadWriter {
	return sys.sshChan.Stderr()
}

func (sys *System) IOStream() io.ReadWriter { return sys.sshChan }

func (sys *System) FSys() afero.Fs { return sys.fSys }

func (sys *System) Width() int { return sys.width }

func (sys *System) Height() int { return sys.height }

// Write replace \n with \r\n before writing to the underlying io.Writer.
// Copied from golang.org/x/crypto/ssh/terminal
func (sw stdoutWrapper) Write(buf []byte) (n int, err error) {
	for len(buf) > 0 {
		i := bytes.IndexByte(buf, '\n')
		todo := len(buf)
		if i >= 0 {
			todo = i
		}

		var nn int
		nn, err = sw.Writer.Write(buf[:todo])
		n += nn
		if err != nil {
			return n, err
		}
		buf = buf[todo:]

		if i >= 0 {
			if _, err = sw.Writer.Write(crlf); err != nil {
				return n, err
			}
			n++
			buf = buf[1:]
		}
	}

	return n, nil
}

func (sys *System) Environ() (env []string) {
	env = make([]string, 0, len(sys.envVars))
	for k, v := range sys.envVars {
		env = append(env, fmt.Sprintf("%v=%v", k, v))
	}
	return
}

func (sys *System) SetEnv(key, value string) error {
	sys.envVars[key] = value
	return nil
}

func (sys *System) Exec(path string, args []string) (int, error) {
	return sys.exec(path, args, nil)
}

func (sys *System) exec(path string, args []string, logger io.ReadWriter) (int, error) {
	cmd := pathlib.Base(path)
	if execFunc, ok := funcMap[cmd]; ok {

		defer func() {
			if r := recover(); r != nil {
				sys.log.WithFields(log.Fields{
					"cmd":   path,
					"args":  args,
					"error": r,
				}).Error("Command has crashed")
				sys.Err().Write([]byte("Segmentation fault\n"))
			}
		}()
		var res int
		// If logger is not nil, redirect IO to it
		if logger != nil {
			loggedSys := &sysLogWrapper{sys, logger}
			res = execFunc.Exec(args, loggedSys)
		} else {
			res = execFunc.Exec(args, sys)
		}
		return res, nil
	}

	return 127, &os.PathError{Op: "exec", Path: path, Err: os.ErrNotExist}
}

// RegisterCommand puts the command implementation into map so
// it can be invoked from command line
func RegisterCommand(name string, cmd Command) {
	funcMap[name] = cmd
}